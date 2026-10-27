// Package voiceover implements narration on top of the edit spec: the script
// lives in the spec (versioned, approved through edit check/apply); this
// package makes and finds its audio — free scratch voices (macOS say,
// espeak-ng), ElevenLabs generation with word timing, imported and recorded
// takes aligned to the script, voice cleanup — and turns each result into an
// ordinary edit plan the person reviews before it becomes a revision.
//
// Takes are ordinary audio assets (tagged voiceover) plus a voiceover_takes
// record (how it was made, cost, cache key, word-timing quality). Generated
// speech is always marked as an AI voice.
package voiceover

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/textalign"
)

// revCtx is a revision resolved for narration work.
type revCtx struct {
	rev     *models.Revision
	project *models.Project
	spec    *editspec.Spec
	facts   editspec.Facts
	clips   []editspec.PlanClip
	total   int
	beats   []editspec.ResolvedBeat
}

func load(ctx context.Context, a *app.App, revisionID string) (*revCtx, error) {
	if revisionID == "" {
		return nil, apperr.New(apperr.InvalidInput, "Choose the version of the cut the narration belongs to.")
	}
	var rev models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), revisionID, &rev); err != nil {
		return nil, apperr.New(apperr.NotFound, "That version of the cut isn't in this workspace.").WithDetails(map[string]any{"revisionId": revisionID})
	}
	var p models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), rev.ProjectID, &p); err != nil {
		return nil, apperr.New(apperr.NotFound, "The project this cut belongs to isn't in this workspace.").WithDetails(map[string]any{"projectId": rev.ProjectID})
	}
	spec := editspec.Clone(&rev.Spec)
	editspec.Normalize(spec)
	facts, err := ops.LoadFacts(ctx, a, rev.ProjectID, spec)
	if err != nil {
		return nil, err
	}
	clips, total := editspec.Timeline(spec)
	rc := &revCtx{rev: &rev, project: &p, spec: spec, facts: facts, clips: clips, total: total}
	rc.beats = editspec.ResolveBeats(spec, clips, total, facts)
	return rc, nil
}

func (rc *revCtx) fps() float64 { return float64(rc.spec.Output.FPS) }

// selectBeats picks the beats an operation works on: the listed IDs (or 1-based
// line numbers), else every beat for which keep returns true.
func (rc *revCtx) selectBeats(idsOrLines []string, keep func(editspec.Beat) bool) ([]int, error) {
	if rc.spec.Voiceover == nil || len(rc.spec.Voiceover.Beats) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "This cut has no narration script yet.").WithHint("Write or paste one on the Voiceover tab.").
			WithCommand(fmt.Sprintf("metarush voiceover script import <file> --revision %s", rc.rev.ID))
	}
	beats := rc.spec.Voiceover.Beats
	if len(idsOrLines) == 0 {
		var out []int
		for i, b := range beats {
			if keep == nil || keep(b) {
				out = append(out, i)
			}
		}
		return out, nil
	}
	var out []int
	for _, ref := range idsOrLines {
		i := beatIndex(beats, ref)
		if i < 0 {
			return nil, noLine(ref)
		}
		out = append(out, i)
	}
	return out, nil
}

// beatIndex finds a beat by ID or 1-based line number.
func beatIndex(beats []editspec.Beat, ref string) int {
	for i, b := range beats {
		if b.ID == ref {
			return i
		}
	}
	var n int
	if _, err := fmt.Sscanf(ref, "%d", &n); err == nil && fmt.Sprint(n) == ref && n >= 1 && n <= len(beats) {
		return n - 1
	}
	return -1
}

// PlanResult is an edit plan built for the person to review, with its check.
type PlanResult struct {
	Plan  editspec.Plan     `json:"plan"`
	Check *ops.EditCheckOut `json:"check"`
}

// Applied is the version of the cut a generation was put into (with apply).
type Applied struct {
	RevisionID string `json:"revisionId,omitempty"`
	ProjectID  string `json:"projectId,omitempty"`
	Number     int    `json:"number,omitempty"`
	Problem    string `json:"problem,omitempty" jsonschema:"description=Why it could not be put in the cut (the audio is kept)."`
}

// applyResult applies a generation's checked plan as a new version of the cut,
// with the check's own confirm token. Failing to apply never loses the audio:
// the problem is reported and the plan stays available.
func applyResult(ctx context.Context, a *app.App, res *PlanResult) *Applied {
	if res == nil {
		return nil
	}
	if res.Check == nil || !res.Check.Valid {
		msg := "the change didn't pass its check"
		if res.Check != nil && len(res.Check.Errors) > 0 {
			msg = res.Check.Errors[0].Message
		}
		return &Applied{Problem: msg}
	}
	raw, err := json.Marshal(res.Plan)
	if err != nil {
		return &Applied{Problem: err.Error()}
	}
	out, err := ops.CallTyped[ops.EditApplyOut](ctx, a, "edit.apply", ops.EditApplyIn{Plan: raw, ConfirmToken: res.Check.ConfirmToken}, ops.Caller{})
	if err != nil {
		return &Applied{Problem: apperr.From(err).Message}
	}
	return &Applied{RevisionID: out.Revision.ID, ProjectID: out.Revision.ProjectID, Number: out.Revision.Number}
}

// Generation summaries end with what to do next; once the work is in the cut
// (apply) that sentence gives way to where it went.
const (
	hintApply   = " Apply the change to put it in the cut."
	hintScratch = " Listen in the preview, then apply the change to add it to the cut."
)

// settle rewrites a generation summary once apply has run.
func settle(summary string, ap *Applied) string {
	if ap != nil && ap.Problem == "" {
		summary = strings.Replace(summary, hintScratch, " Listen in the preview.", 1)
		summary = strings.Replace(summary, hintApply, "", 1)
	}
	return summary + appliedNote(ap)
}

// plural counts things in words ("1 line", "3 lines").
func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %ss", n, one)
}

// seconds says a short length in words, to a tenth ("0.4 seconds", "1 second").
func seconds(v float64) string {
	s := strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64)
	if s == "1" {
		return "1 second"
	}
	return s + " seconds"
}

// appliedNote says where the work went, for the summary.
func appliedNote(ap *Applied) string {
	switch {
	case ap == nil:
		return ""
	case ap.Problem != "":
		return " It is not in the cut yet: " + strings.TrimRight(ap.Problem, ".") + "."
	}
	return fmt.Sprintf(" It is in the cut now as version %d.", ap.Number)
}

func (o *GenerateOut) apply(ctx context.Context, a *app.App) {
	o.Applied = applyResult(ctx, a, o.Result)
	o.Summary = settle(o.Summary, o.Applied)
}

func (o *CleanupOut) apply(ctx context.Context, a *app.App) {
	o.Applied = applyResult(ctx, a, o.Result)
	o.Summary = settle(o.Summary, o.Applied)
}

func (o *AlignOut) apply(ctx context.Context, a *app.App) {
	o.Applied = applyResult(ctx, a, o.Result)
	o.Summary = settle(o.Summary, o.Applied)
}

// planCheck builds an ops plan against the revision and dry-runs it with
// edit.check (diff, warnings, confirm token). Nothing changes until edit.apply.
func planCheck(ctx context.Context, a *app.App, rev *models.Revision, instruction string, eops []editspec.Op) (*PlanResult, error) {
	if len(eops) == 0 {
		return nil, nil
	}
	plan := editspec.Plan{BaseRevisionID: rev.ID, Branch: rev.Branch, Instruction: truncate(instruction, 4000), Ops: eops}
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	out, err := ops.CallTyped[ops.EditCheckOut](ctx, a, "edit.check", ops.EditCheckIn{Plan: raw}, ops.Caller{})
	if err != nil {
		return nil, err
	}
	return &PlanResult{Plan: plan, Check: out}, nil
}

// planCheckAfterWork is planCheck for operations that already made audio: when
// the cut moved on meanwhile, the work is kept (cached, never billed again) and
// the person is told to place it on the latest version instead of losing it.
func planCheckAfterWork(ctx context.Context, a *app.App, rev *models.Revision, instruction string, eops []editspec.Op) (*PlanResult, string, error) {
	res, err := planCheck(ctx, a, rev, instruction, eops)
	if err != nil && apperr.Is(err, apperr.RevisionConflict) {
		return nil, "Someone else changed the cut meanwhile. The audio is saved: do this again on the latest version to place it (it costs nothing again).", nil
	}
	return res, "", err
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func setOp(id string, fields map[string]any) editspec.Op {
	for k, v := range fields {
		if t, ok := v.(*editspec.TakeRef); ok {
			fields[k] = TakeSet(t)
		} else if t, ok := v.(editspec.TakeRef); ok {
			fields[k] = TakeSet(&t)
		} else if an, ok := v.(editspec.Anchor); ok {
			fields[k] = AnchorSet(an)
		}
	}
	b, _ := json.Marshal(fields)
	return editspec.Op{Op: "updateBeat", ID: id, Set: b}
}

// TakeSet is a take as a complete updateBeat value: every field is present
// (nulls and falses included), so a new take fully replaces the old one —
// update ops merge nested objects, and an omitted field would otherwise keep
// the previous take's value (for example its cleaned-up version).
func TakeSet(t *editspec.TakeRef) map[string]any {
	if t == nil {
		return nil
	}
	var processed any
	if t.Processed != nil {
		processed = map[string]any{"assetId": t.Processed.AssetID, "preset": t.Processed.Preset, "offset": t.Processed.Offset}
	}
	// Zero values (not null) for plain fields: JSON null does not reset them.
	return map[string]any{"takeId": t.TakeID, "assetId": t.AssetID, "in": t.In, "out": t.Out, "speed": t.Speed, "source": t.Source,
		"aiVoice": t.AIVoice, "processed": processed, "useProcessed": t.UseProcessed}
}

// AnchorSet is a start point as a complete updateBeat value: every field the
// start point does not use is null, so it replaces the old one instead of
// merging into it (a "clip" start point must not keep an old moment's time).
func AnchorSet(a editspec.Anchor) map[string]any {
	opt := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	m := map[string]any{"kind": a.Kind, "clipId": opt(a.ClipID), "edge": opt(a.Edge), "assetId": opt(a.AssetID), "quote": opt(a.Quote), "ref": opt(a.Ref),
		"offset": nil, "at": nil, "gap": nil}
	if a.Offset != 0 {
		m["offset"] = a.Offset
	}
	if a.At != nil {
		m["at"] = *a.At
	}
	if a.Gap != nil {
		m["gap"] = *a.Gap
	}
	return m
}

// TakeView is a take as people see it.
type TakeView struct {
	models.VoiceTake
	Chosen  bool   `json:"chosen"`
	Summary string `json:"summary"` // plain words: "AI voice (Ada), 3.2 s"
}

func takeSummary(t models.VoiceTake) string {
	what := map[string]string{models.TakeScratch: "Scratch voice", models.TakeTTS: "AI voice", models.TakeRecorded: "Your recording", models.TakeImported: "Imported recording"}[t.Source]
	if t.VoiceName != "" {
		what += " (" + t.VoiceName + ")"
	}
	s := fmt.Sprintf("%s, %.1f s", what, t.Duration)
	if len(t.Cleanups) > 0 {
		s += ", cleaned up"
	}
	return s
}

func insertTake(ctx context.Context, a *app.App, t *models.VoiceTake) error {
	if t.ID == "" {
		t.ID = ids.New(models.VoiceTakePrefix)
	}
	t.WorkspaceID, t.CreatedBy, t.CreatedAt = a.WorkspaceID(), a.Actor, time.Now().UTC()
	t.TextHash = textHash(t.Text)
	return a.DB.Insert(ctx, store.CVoiceTakes, t)
}

func textHash(text string) string {
	sum := sha256.Sum256([]byte(strings.Join(strings.Fields(text), " ")))
	return hex.EncodeToString(sum[:8])
}

func findCached(ctx context.Context, a *app.App, cacheKey string) *models.VoiceTake {
	var ts []models.VoiceTake
	if err := a.DB.Find(ctx, store.CVoiceTakes, a.WorkspaceID(), bson.M{"cacheKey": cacheKey}, &ts, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(5)); err != nil {
		return nil
	}
	for i := range ts {
		var as models.Asset
		if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), ts[i].AssetID, &as) == nil && as.State == models.AssetAvailable {
			return &ts[i]
		}
	}
	return nil
}

// adoptCached reuses an identical take (same text, voice, settings): for
// another project or line it links the asset and records a free copy.
func adoptCached(ctx context.Context, a *app.App, t *models.VoiceTake, projectID, beatID string) (*models.VoiceTake, error) {
	if t.ProjectID == projectID && t.BeatID == beatID {
		return t, nil
	}
	if t.ProjectID != projectID {
		if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), t.AssetID, bson.M{"$addToSet": bson.M{"projectIds": projectID}}); err != nil {
			return nil, err
		}
	}
	c := *t
	c.ID, c.ProjectID, c.BeatID, c.Credits = "", projectID, beatID, 0
	c.Note = "reused an identical take (no charge)"
	if err := insertTake(ctx, a, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func takeRef(t *models.VoiceTake) *editspec.TakeRef {
	return &editspec.TakeRef{TakeID: t.ID, AssetID: t.AssetID, In: round3(t.In), Out: round3(t.Out), Source: t.Source, AIVoice: t.AIVoice}
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// ingestAudio stores a file as a project asset tagged voiceover, without
// scheduling local transcription (callers supply word timing, or ask for it).
func ingestAudio(ctx context.Context, a *app.App, projectID, path, name string, tags []string, transcribe bool) (*models.Asset, error) {
	res := ingest.File(ctx, a, path, ingest.Options{ProjectID: projectID, Tags: append([]string{"voiceover"}, tags...), Via: a.Actor.Via, Transcribe: transcribe, DisplayName: name})
	if res.Status == "failed" || res.Status == "skipped" || res.AssetID == "" {
		return nil, apperr.New(apperr.UnsupportedMedia, "MetaRush couldn't store the narration audio.").WithDetails(map[string]any{"error": res.Error})
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), res.AssetID, &as); err != nil {
		return nil, err
	}
	if projectID != "" && !contains(as.ProjectIDs, projectID) {
		_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$addToSet": bson.M{"projectIds": projectID}})
	}
	return &as, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// storeWords records word timing for an audio asset as its transcript (one
// segment), unless it already has a real transcript. precision is "word" for
// provider alignment, "segment" for estimates.
func storeWords(ctx context.Context, a *app.App, as *models.Asset, provider, model, precision string, words []models.Word, overwrite bool) error {
	if as.Transcript != nil && as.Transcript.State == "available" && !overwrite {
		return nil
	}
	if len(words) == 0 {
		return nil
	}
	if _, err := a.DB.C(store.CSegments).DeleteMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": as.ID}); err != nil {
		return err
	}
	texts := make([]string, len(words))
	for i, w := range words {
		texts[i] = w.Text
	}
	seg := models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Index: 0, From: words[0].From, To: words[len(words)-1].To,
		Text: strings.Join(texts, " "), Words: words, Provider: provider, Model: model, CreatedAt: time.Now().UTC()}
	if err := a.DB.Insert(ctx, store.CSegments, seg); err != nil {
		return err
	}
	st := models.TranscriptStatus{State: "available", Provider: provider, Model: model, Precision: precision, Language: "", Segments: 1, Words: len(words), UpdatedAt: time.Now().UTC()}
	return a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"transcript": st, "analysis.transcribe": "available"}})
}

// estimateWords spreads a line's words over [lead, dur-tail) in proportion to their length.
func estimateWords(text string, from, to float64) []models.Word {
	toks := textalign.Tokens(text)
	if len(toks) == 0 || to <= from {
		return nil
	}
	total := 0
	for _, t := range toks {
		total += len([]rune(t)) + 1
	}
	var out []models.Word
	acc := 0
	span := to - from
	for _, t := range toks {
		n := len([]rune(t)) + 1
		a := from + span*float64(acc)/float64(total)
		acc += n
		b := from + span*float64(acc)/float64(total)
		out = append(out, models.Word{Text: t, From: round3(a), To: round3(math.Max(a+0.04, b-0.04))})
	}
	return out
}

// scratchFile is a temp path under this installation's scratch folder.
func scratchFile(a *app.App, name string) (string, func(), error) {
	dir := filepath.Join(a.Cfg.Scratch.Dir, "voiceover", ids.New("tmp"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, err
	}
	return filepath.Join(dir, name), func() { _ = os.RemoveAll(dir) }, nil
}

// beatName is the plain name of a line (i counts from 0) at the start of a
// sentence: Narration line 3 (“It started in a tiny garage.”).
func beatName(i int, b editspec.Beat) string { return editspec.LineName(i, b.Text) }

// lineRef is the plain name of a line in the middle of a sentence ("narration line 3 (“…”)").
func lineRef(i int, b editspec.Beat) string { return lowerFirst(beatName(i, b)) }

// noLine is the error for a line the cut doesn't have (named by number or ID).
func noLine(ref string) *apperr.Error {
	if n, err := strconv.Atoi(ref); err == nil && n > 0 {
		return apperr.New(apperr.NotFound, "This cut has no narration line %d.", n)
	}
	return apperr.New(apperr.NotFound, "This cut has no narration line called “%s”.", ref)
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// detailsOf is an output as error details: the same fields, as a map, so the
// command kept with WithCommand stays beside them.
func detailsOf(v any) map[string]any {
	m := map[string]any{}
	if b, err := json.Marshal(v); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}
