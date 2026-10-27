// Package catchup brings existing footage up to date after an upgrade. When a
// release adds an analysis (finding who speaks, asking questions, cleaner
// voice profiles), clips imported before it get it too: each step has a name
// and a version, the version a workspace completed is recorded (collection
// catch_ups), and a step runs again only when its version goes up. Steps are
// idempotent (job idempotency keys, group-keyed questions), so two machines
// starting at once do no double work. The daemon runs them when it starts and
// `metarush migrate` at its end; `metarush footage speakers detect --all` and
// `metarush todos sync` do the same work on request.
package catchup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/calls"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/faces"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/sources"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

// ErrNotHere means this machine cannot do a step (the on-screen index needs a
// Mac): the step is not recorded, so the next machine that can does it, and
// nothing is reported as a problem.
var ErrNotHere = errors.New("this machine cannot do this step")

// Collection records what each workspace caught up on.
const Collection = "catch_ups"

// Step is one backfill.
type Step struct {
	Name    string
	Version int
	// About says what it does, in plain words.
	About string
	Run   func(ctx context.Context, a *app.App) (string, error)
}

// Outcome is what one step did.
type Outcome struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	Ran     bool   `json:"ran"`
	Summary string `json:"summary,omitempty"`
	Error   string `json:"error,omitempty"`
	// NotHere: this machine cannot do the step; another one will.
	NotHere bool `json:"notHere,omitempty"`
}

// Record is a step a workspace completed.
type Record struct {
	ID          string    `bson:"_id"`
	WorkspaceID string    `bson:"workspaceId"`
	Name        string    `bson:"name"`
	Version     int       `bson:"version"`
	Summary     string    `bson:"summary,omitempty"`
	CompletedAt time.Time `bson:"completedAt"`
}

// Steps run in order: voices are rebuilt before new detections use them, and
// questions are checked last.
var Steps = []Step{
	{Name: "voice-profiles", Version: 1, About: "rebuild every person's voice from the clips a person confirmed", Run: rebuildVoices},
	{Name: "speakers", Version: speakers.CatchUpVersion, About: "find who speaks in clips imported before speaker detection", Run: detectSpeakers},
	{Name: "visual-changes", Version: 1, About: "find cuts, slow changes and movement in clips analyzed before visual change detection", Run: visualChanges},
	{Name: "visual-index", Version: 2, About: "look at what's on screen in clips imported before the on-screen index, and again in clips looked at before faces were recognized across clips (needs a Mac)", Run: visualIndex},
	{Name: "face-questions", Version: 1, About: "ask who is on screen once per person instead of at every appearance, with the names the video shows or says as suggestions", Run: faceQuestions},
	{Name: "questions", Version: 1, About: "ask the questions earlier versions did not (project brief, music rights, ready to approve)", Run: syncQuestions},
	{Name: "image-resources", Version: 1, About: "make pictures imported before the resources library into resources (the same bytes; nothing is copied)", Run: imageResources},
	{Name: "switches", Version: 1, About: "find where the picture switches, to the frame, in clips analyzed before (call recordings: flashes at clip edges, name bars)", Run: pictureSwitches},
	{Name: "name-suggestions", Version: 1, About: "suggest descriptive names for clips added before MetaRush named them (a person uses them; names people gave are kept)", Run: nameSuggestions},
	{Name: "person-questions", Version: 1, About: "look again at the open questions about people with the stricter rules (the voice needs 6 seconds and never asks on its own; pronouns are never names) and keep score of the answers given so far", Run: personQuestions},
	{Name: "ship-gate", Version: 1, About: "ask an owner once whether versions should wait for the quality check and their sign-offs before they go out (nothing changes until one answers)", Run: shipGateQuestion},
}

// shipGateQuestion asks an older workspace's owners, once, whether versions
// wait for the quality check and sign-offs (D298); a workspace that decided
// (every new one starts with it on) is never asked.
func shipGateQuestion(ctx context.Context, a *app.App) (string, error) {
	asked, err := ops.AskAboutShipGate(ctx, a)
	if err != nil {
		return "", err
	}
	if asked {
		return "asked whether versions should wait for the quality check and sign-offs", nil
	}
	return "nothing to ask: the workspace decided already", nil
}

// nameSuggestions works out a descriptive name for every clip added before
// MetaRush named clips (D150). It only suggests: the Library offers them, a
// person uses them, and names people gave are kept.
func nameSuggestions(ctx context.Context, a *app.App) (string, error) {
	var list []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"state": models.AssetAvailable, "tags": bson.M{"$ne": models.TagEnhanced}, "kind": bson.M{"$ne": "image"}}, &list,
		options.Find().SetProjection(bson.M{"_id": 1})); err != nil {
		return "", err
	}
	for _, as := range list {
		if _, err := sources.Refresh(ctx, a, as.ID); err != nil {
			return "", err
		}
	}
	n, err := a.DB.C(store.CAssets).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "nameSuggestion": bson.M{"$ne": nil}})
	if err != nil || n == 0 {
		return "", err
	}
	return fmt.Sprintf("better names for %d %s, waiting next to their titles and in each project's Sources (Use all)", n, plural(int(n), "clip", "clips")), nil
}

// Run runs the steps this workspace has not completed at their current version.
func Run(ctx context.Context, a *app.App) ([]Outcome, error) { return RunSteps(ctx, a, Steps) }

// RunSteps runs the given steps (tests use their own).
func RunSteps(ctx context.Context, a *app.App, steps []Step) ([]Outcome, error) {
	done := map[string]int{}
	cur, err := a.DB.C(Collection).Find(ctx, bson.M{"workspaceId": a.WorkspaceID()})
	if err != nil {
		return nil, err
	}
	var recs []Record
	if err := cur.All(ctx, &recs); err != nil {
		return nil, err
	}
	for _, r := range recs {
		done[r.Name] = r.Version
	}
	out := []Outcome{}
	for _, s := range steps {
		o := Outcome{Name: s.Name, Version: s.Version}
		if done[s.Name] >= s.Version {
			out = append(out, o)
			continue
		}
		summary, err := s.Run(ctx, a)
		if errors.Is(err, ErrNotHere) {
			o.NotHere = true
			out = append(out, o)
			continue // not recorded: a machine that can will do it
		}
		if err != nil {
			o.Error = err.Error()
			out = append(out, o)
			continue // not recorded: tried again next time
		}
		o.Ran, o.Summary = true, summary
		rec := Record{ID: a.WorkspaceID() + "/" + s.Name, WorkspaceID: a.WorkspaceID(), Name: s.Name, Version: s.Version, Summary: summary, CompletedAt: time.Now().UTC()}
		if _, err := a.DB.C(Collection).ReplaceOne(ctx, bson.M{"_id": rec.ID}, rec, options.Replace().SetUpsert(true)); err != nil {
			return out, err
		}
		a.Audit(ctx, "catchup."+s.Name, "workspace", a.WorkspaceID(), "", map[string]any{"version": s.Version, "summary": summary})
		out = append(out, o)
	}
	return out, nil
}

// stepWords say what a step was doing, for a step that could not finish.
var stepWords = map[string]string{"voice-profiles": "relearning people's voices", "speakers": "finding who speaks in older clips",
	"visual-changes": "finding cuts and movement in older clips", "visual-index": "looking at what's on screen in older clips",
	"questions": "asking the questions earlier versions didn't", "image-resources": "adding older pictures to Resources",
	"face-questions": "asking who is on screen once per person", "person-questions": "looking again at the questions about people"}

// Describe says in plain words what a run did ("" when nothing needed doing).
func Describe(outs []Outcome) string {
	var parts []string
	for _, o := range outs {
		switch {
		case o.Error != "":
			what := stepWords[o.Name]
			if what == "" {
				what = "one of the steps"
			}
			parts = append(parts, fmt.Sprintf("couldn't finish %s; MetaRush tries again next time", what))
		case o.Ran && o.Summary != "":
			parts = append(parts, strings.TrimSuffix(o.Summary, "."))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "Caught up after the upgrade: " + strings.Join(parts, "; ") + "."
}

func rebuildVoices(ctx context.Context, a *app.App) (string, error) {
	n, err := speakers.RebuildAll(ctx, a)
	if err != nil {
		return "", err
	}
	// People are told about voices recognized against the rebuilt profiles.
	for _, p := range peopleIDs(ctx, a) {
		notify.Recognized(ctx, a, a.Actor.UserID, p)
	}
	if n == 0 {
		return "", nil
	}
	return fmt.Sprintf("relearned %d %s from the clips you confirmed", n, plural(n, "person's voice", "people's voices")), nil
}

func peopleIDs(ctx context.Context, a *app.App) []string {
	ps, err := speakers.People(ctx, a, false)
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

func detectSpeakers(ctx context.Context, a *app.App) (string, error) {
	out, err := ops.CallTyped[ops.SpeakerDetectOut](ctx, a, "footage.speakers.detect", ops.SpeakerDetectIn{All: true}, ops.Caller{})
	if err != nil {
		return "", err
	}
	if len(out.Jobs) == 0 {
		return "", nil
	}
	return fmt.Sprintf("listening for the voices in %d %s", len(out.Jobs), plural(len(out.Jobs), "clip", "clips")), nil
}

func syncQuestions(ctx context.Context, a *app.App) (string, error) {
	before, err := ops.CallTyped[ops.TodoListOut](ctx, a, "todos.list", ops.TodoListIn{Limit: 1}, ops.Caller{})
	if err != nil {
		return "", err
	}
	out, err := ops.CallTyped[ops.TodoSyncOut](ctx, a, "todos.sync", ops.TodoSyncIn{}, ops.Caller{})
	if err != nil {
		return "", err
	}
	if n := out.Open - before.Counts.Total; n > 0 {
		return fmt.Sprintf("%d new %s for you", n, plural(n, "question", "questions")), nil
	}
	return "", nil
}

// imageResources gives every picture that was imported as footage (before
// the resources library existed) a resource in its projects, or in the
// workspace library when it is in the Inbox (D79). The asset stays.
func imageResources(ctx context.Context, a *app.App) (string, error) {
	var imgs []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"kind": "image", "state": models.AssetAvailable}, &imgs); err != nil {
		return "", err
	}
	n := 0
	for _, as := range imgs {
		p, err := a.Cache.Ensure(ctx, a.Media, as.StorageKey, as.SHA256, as.Size, nil)
		if err != nil {
			continue // tried again the next time the step version goes up; the asset is untouched
		}
		scopes := as.ProjectIDs
		if len(scopes) == 0 {
			scopes = []string{""}
		}
		for _, pid := range scopes {
			r, err := resources.AddFile(ctx, a, p, resources.AddOptions{ProjectID: pid, FileName: as.Name, Title: as.DisplayTitle(), Tags: as.Tags, Via: "catch-up",
				Source: models.ResourceSource{Kind: models.FromFile, Name: as.Name, AssetID: as.ID}})
			if err == nil && r.Status == resources.StatusAdded {
				n++
			}
		}
	}
	if n == 0 {
		return "", nil
	}
	return fmt.Sprintf("%d %s you imported earlier are in Resources now", n, plural(n, "picture", "pictures")), nil
}

// visualChanges queues the evidence detectors again for clips whose evidence
// predates visual change detection (evidence version 2), quietly behind new work.
func visualChanges(ctx context.Context, a *app.App) (string, error) {
	var old []struct {
		AssetID string `bson:"assetId"`
	}
	if err := a.DB.Find(ctx, store.CInsights, a.WorkspaceID(), bson.M{"kind": "technical", "promptVersion": bson.M{"$ne": evidence.Version}}, &old,
		options.Find().SetProjection(bson.M{"assetId": 1})); err != nil {
		return "", err
	}
	if len(old) == 0 {
		return "", nil
	}
	ids := make([]string, 0, len(old))
	for _, o := range old {
		ids = append(ids, o.AssetID)
	}
	var assets []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}, "state": models.AssetAvailable, "media.hasVideo": true}, &assets,
		options.Find().SetProjection(bson.M{"_id": 1})); err != nil {
		return "", err
	}
	n := 0
	for _, as := range assets {
		if _, created, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeEvidence, IdempotencyKey: jobs.TypeEvidence + ":" + as.ID + ":" + evidence.Version,
			Priority: 2, AssetID: as.ID, Payload: map[string]any{"assetId": as.ID}, Actor: a.Actor}); err != nil {
			return "", err
		} else if created {
			n++
		}
	}
	if n == 0 {
		return "", nil
	}
	return fmt.Sprintf("finding cuts, slow changes and movement in %d %s", n, plural(n, "clip", "clips")), nil
}

// pictureSwitches queues the switches step (D160) for clips with cuts that
// were analyzed before it existed.
func pictureSwitches(ctx context.Context, a *app.App) (string, error) {
	var have []struct {
		AssetID string `bson:"assetId"`
	}
	if err := a.DB.Find(ctx, store.CInsights, a.WorkspaceID(), bson.M{"kind": calls.InsightKind}, &have, options.Find().SetProjection(bson.M{"assetId": 1})); err != nil {
		return "", err
	}
	done := map[string]bool{}
	for _, h := range have {
		done[h.AssetID] = true
	}
	var assets []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"state": models.AssetAvailable, "media.hasVideo": true}, &assets); err != nil {
		return "", err
	}
	var ids []string
	for _, as := range assets {
		if !done[as.ID] {
			ids = append(ids, as.ID)
		}
	}
	ev := evidence.LoadStored(ctx, a.DB, a.WorkspaceID(), ids)
	idx := visual.LoadMany(ctx, a.DB, a.WorkspaceID(), ids)
	n := 0
	for i := range assets {
		as := &assets[i]
		if done[as.ID] {
			continue
		}
		var ix *visual.Index
		ver := ""
		if st := idx[as.ID]; st != nil && st.Index != nil {
			ix, ver = st.Index, st.Index.Version
		}
		if !calls.Worth(as, ev[as.ID], ix) {
			continue
		}
		if _, created, err := calls.Queue(ctx, a, as, ver, a.Actor); err != nil {
			return "", err
		} else if created {
			n++
		}
	}
	if n == 0 {
		return "", nil
	}
	return fmt.Sprintf("finding where the picture switches in %d %s", n, plural(n, "clip", "clips")), nil
}

// canLook says whether this machine can run the on-screen index (tests replace it).
var canLook = vision.CanRun

// visualIndex queues the on-screen index for clips that do not have one, and
// again for clips an older index looked at (vision-1: no face fingerprints,
// attention or cuts, so their faces can't be recognized across clips and
// framing can't follow movement; names people gave faces are kept). Only a
// Mac can do it; elsewhere the step waits for one (ErrNotHere).
func visualIndex(ctx context.Context, a *app.App) (string, error) {
	if !canLook(a.Tools) {
		return "", ErrNotHere
	}
	var assets []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"state": models.AssetAvailable, "media.hasVideo": true, "analysis.visual": bson.M{"$exists": false}}, &assets,
		options.Find().SetProjection(bson.M{"_id": 1})); err != nil {
		return "", err
	}
	n := 0
	for _, as := range assets {
		if _, created, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeVisual, IdempotencyKey: jobs.TypeVisual + ":" + as.ID, Priority: 1, Heavy: true,
			AssetID: as.ID, Payload: map[string]any{"assetId": as.ID}, Actor: a.Actor}); err != nil {
			return "", err
		} else if created {
			n++
		}
	}
	var older []struct {
		AssetID string `bson:"assetId"`
	}
	if err := a.DB.Find(ctx, store.CInsights, a.WorkspaceID(), bson.M{"kind": visual.InsightKind, "promptVersion": bson.M{"$ne": visual.Version}}, &older,
		options.Find().SetProjection(bson.M{"assetId": 1})); err != nil {
		return "", err
	}
	again := 0
	seen := map[string]bool{}
	for _, o := range older {
		if seen[o.AssetID] {
			continue
		}
		seen[o.AssetID] = true
		var as models.Asset
		if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), o.AssetID, &as) != nil || as.State != models.AssetAvailable || !as.Media.HasVideo {
			continue
		}
		if _, created, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeVisual, IdempotencyKey: jobs.TypeVisual + ":" + as.ID + "@" + visual.Version, Priority: 1, Heavy: true,
			AssetID: as.ID, Payload: map[string]any{"assetId": as.ID}, Actor: a.Actor}); err != nil {
			return "", err
		} else if created {
			again++
		}
	}
	switch {
	case n == 0 && again == 0:
		return "", nil
	case again == 0:
		return fmt.Sprintf("looking at what's on screen in %d %s", n, plural(n, "clip", "clips")), nil
	case n == 0:
		return fmt.Sprintf("looking again at %d %s, so faces can be recognized across clips", again, plural(again, "clip", "clips")), nil
	}
	return fmt.Sprintf("looking at what's on screen in %d %s, and again at %d older %s so faces can be recognized across clips", n, plural(n, "clip", "clips"), again, plural(again, "one", "ones")), nil
}

// faceQuestions asks about the faces of every clip once per person (D155,
// D159): the per-appearance "Is this …?" questions earlier versions asked are
// folded into one question per person, and the names the video shows or says
// become suggestions. Idempotent: questions are keyed by their group.
func faceQuestions(ctx context.Context, a *app.App) (string, error) {
	countOpen := func() (int, error) {
		n, err := a.DB.C(store.CTodos).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "state": models.TodoOpen,
			"kind": bson.M{"$in": bson.A{models.TodoFacePerson, models.TodoFaceGroup}}})
		return int(n), err
	}
	before, err := countOpen()
	if err != nil {
		return "", err
	}
	var fs []struct {
		AssetID string `bson:"assetId"`
	}
	if err := a.DB.Find(ctx, store.CFaces, a.WorkspaceID(), bson.M{}, &fs, options.Find().SetProjection(bson.M{"assetId": 1})); err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for _, f := range fs {
		if seen[f.AssetID] {
			continue
		}
		seen[f.AssetID] = true
		if _, err := faces.Candidates(ctx, a, f.AssetID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
	}
	after, err := countOpen()
	if err != nil {
		return "", err
	}
	switch {
	case before > 0 && after != before:
		return fmt.Sprintf("asked who is on screen once per person instead of at every appearance: %d %s became %d", before, plural(before, "question", "questions"), after), nil
	case before == 0 && after > 0:
		return fmt.Sprintf("%d %s about who is on screen", after, plural(after, "question", "questions")), nil
	}
	return "", nil
}

// personQuestions brings the open questions about people up to the rules of
// D189: the voice heard while a face is alone on screen needs 6 seconds and
// never asks "Is this …?" on its own, pronouns and contractions are never
// names, a "(talking then)" answer never contradicts the looks or a name shown.
// Every clip with an open question about a person is worked out again: what
// no longer qualifies closes, junk answers go, the rest stays; questions a
// person answered or skipped are never reopened. The answers given so far are
// kept score of (what each name rested on, and whether it was right). Safe
// to repeat: questions are keyed by their group, answers scored once.
func personQuestions(ctx context.Context, a *app.App) (string, error) {
	scored, err := todos.Backfill(ctx, a)
	if err != nil {
		return "", err
	}
	kinds := bson.A{models.TodoFacePerson, models.TodoFaceGroup, models.TodoSpeakerMatch, models.TodoSpeakerUnnamed}
	var open []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"state": models.TodoOpen, "kind": bson.M{"$in": kinds}}, &open,
		options.Find().SetProjection(bson.M{"assetId": 1, "groupKey": 1})); err != nil {
		return "", err
	}
	before := map[string]bool{}
	clips := map[string]bool{}
	var order []string
	for _, t := range open {
		before[t.GroupKey] = true
		if t.AssetID != "" && !clips[t.AssetID] {
			clips[t.AssetID] = true
			order = append(order, t.AssetID)
		}
	}
	for _, id := range order {
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), id, &as); err != nil {
			continue // the clip is gone; its questions close with it
		}
		var err error
		if as.Speakers != nil && as.Speakers.State == "available" {
			err = speakers.Reconcile(ctx, a, id) // the voices' questions, then the faces' (Reconcile ends with them)
		} else {
			_, err = faces.Candidates(ctx, a, id)
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
	}
	var after []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"state": models.TodoOpen, "kind": bson.M{"$in": kinds}}, &after,
		options.Find().SetProjection(bson.M{"groupKey": 1})); err != nil {
		return "", err
	}
	kept := 0
	for _, t := range after {
		if before[t.GroupKey] {
			kept++
		}
	}
	closed := len(open) - kept
	var parts []string
	if closed > 0 {
		parts = append(parts, fmt.Sprintf("closed %d %s about people that rested on too little", closed, plural(closed, "question", "questions")))
	}
	if n := len(after) - kept; n > 0 {
		parts = append(parts, fmt.Sprintf("asked %d again with only the evidence that holds", n))
	}
	if scored > 0 {
		parts = append(parts, fmt.Sprintf("kept score of %d earlier %s", scored, plural(scored, "answer", "answers")))
	}
	return strings.Join(parts, ", "), nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
