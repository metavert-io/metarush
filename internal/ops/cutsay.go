package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Words changed in the speaker's own voice (D308–D312): putting a change
// that was made and checked into a cut, taking it out again, and listing
// them. Making one (priced, consented, checked) is internal/spoken's
// cut.say.estimate and cut.say.make.

// CutSayIDIn names a change.
type CutSayIDIn struct {
	ID string `json:"id" jsonschema:"pattern=^swc_[0-9a-z]{26}$,description=The change (from cut say make or cut say list)."`
}

// CutSayPlaceIn puts a change into a cut, or takes it out (undo).
type CutSayPlaceIn struct {
	ID string `json:"id" jsonschema:"pattern=^swc_[0-9a-z]{26}$,description=The change (from cut say make or cut say list)."`
	// Branch is the cut (default: the one it was made for).
	Branch       string `json:"branch,omitempty" jsonschema:"maxLength=64,description=The cut (default: the one it was made for)."`
	DryRun       bool   `json:"dryRun,omitempty" jsonschema:"description=Only check: the change in plain words\\, warnings and a confirm token."`
	ConfirmToken string `json:"confirmToken,omitempty" jsonschema:"description=The token from a dry run of exactly this change."`
	AllowUnlock  bool   `json:"allowUnlock,omitempty" jsonschema:"description=Permit changing locked clips. Only when the person explicitly asked."`
	// NoRender skips the review copy of the new version.
	NoRender bool `json:"noRender,omitempty" jsonschema:"description=Don't make a review copy of the new version."`
}

// CutSayPlaceOut is the version with (or without) the new words.
type CutSayPlaceOut struct {
	CutEditOut
	ChangeID string `json:"changeId"`
	// RenderID is the review copy of the new version, when one was asked for.
	RenderID string `json:"renderId,omitempty"`
}

// CutSayListIn lists a project's changes, or a clip's.
type CutSayListIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=Project ID or its exact name."`
	AssetID   string `json:"assetId,omitempty" jsonschema:"pattern=^(ast_[0-9a-z]{26})?$,description=Only the changes to this clip's words."`
}

// SpokenView is a change as people read it.
type SpokenView struct {
	models.SpokenChange
	ClipTitle string `json:"clipTitle"`
	CutTitle  string `json:"cutTitle"`
	// InCut says a cut's latest version plays it: Branches are those cuts
	// (the one it was made for first), CutTitle the first of them by name.
	InCut    bool     `json:"inCut"`
	Branches []string `json:"branches,omitempty"`
	// Says is the change in one plain line; Next what can be done with it.
	Says string `json:"says"`
	Next string `json:"next,omitempty"`
}

// CutSayListOut lists changes, newest first.
type CutSayListOut struct {
	Changes []SpokenView `json:"changes"`
	Summary string       `json:"summary"`
}

func init() {
	Def("cut.say.place", "Put new words someone said in their own voice copy (made and checked with cut say make) into the cut as a new version, with a review copy; people only; checked first like every edit (a dry run gives a confirm token)", Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *CutSayPlaceIn) (*CutSayPlaceOut, error) {
			return placeSaid(ctx, a, in, false)
		})
	Def("cut.say.undo", "Take changed words out of a cut again: a new version plays what was said (the change is kept, so it can be put back)", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *CutSayPlaceIn) (*CutSayPlaceOut, error) {
			return placeSaid(ctx, a, in, true)
		})
	Def("cut.say.list", "List words changed in a speaker's own voice copy, for a project or a clip: what was said and what is said now, whose voice, how it was checked, and whether the cut plays it", Flags{},
		func(ctx context.Context, a *app.App, in *CutSayListIn) (*CutSayListOut, error) {
			return listSaid(ctx, a, in)
		})
	Def("cut.say.show", "Show one word change made in a speaker's own voice copy: the words before and after, whose voice and consent, the price, how it was fitted and checked, and the versions that play it", Flags{},
		func(ctx context.Context, a *app.App, in *CutSayIDIn) (*SpokenView, error) {
			c, err := loadSaid(ctx, a, in.ID)
			if err != nil {
				return nil, err
			}
			v := spokenViews(ctx, a, []models.SpokenChange{*c})[0]
			return &v, nil
		})
}

func loadSaid(ctx context.Context, a *app.App, id string) (*models.SpokenChange, error) {
	var c models.SpokenChange
	if err := a.DB.GetByID(ctx, store.CSpokenChanges, a.WorkspaceID(), strings.TrimSpace(id), &c); err != nil {
		return nil, apperr.New(apperr.NotFound, "There's no such change of words.").WithCommand("metarush cut say list <project>")
	}
	return &c, nil
}

// placeSaid puts a change into the cut (or takes it out) through the same
// check, confirm token, locks and rebase as every edit.
func placeSaid(ctx context.Context, a *app.App, in *CutSayPlaceIn, undo bool) (*CutSayPlaceOut, error) {
	c, err := loadSaid(ctx, a, in.ID)
	if err != nil {
		return nil, err
	}
	branch := sayFirst(in.Branch, c.Branch)
	if undo && in.Branch == "" { // the cut that plays it, when the one it was made for doesn't
		var p models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), c.ProjectID, &p) == nil {
			if bs := playingBranches(ctx, a, &p, map[string]*models.Revision{}, c); len(bs) > 0 {
				branch = bs[0]
			}
		}
	}
	t, err := loadEditTarget(ctx, a, CutEditCommon{ProjectID: c.ProjectID, Branch: branch})
	if err != nil {
		return nil, err
	}
	common := CutEditCommon{ProjectID: c.ProjectID, Branch: t.branch, DryRun: in.DryRun, ConfirmToken: in.ConfirmToken, AllowUnlock: in.AllowUnlock}
	var mix *editspec.Mix
	if t.spec.Mix != nil {
		m := *t.spec.Mix
		m.Said = append([]editspec.SaidSound(nil), t.spec.Mix.Said...)
		mix = &m
	}
	has := mix != nil && saidIndex(mix.Said, c.ID) >= 0
	var summary, instruction, mouthNote string
	var said editspec.SaidSound
	if undo {
		if !has {
			return nil, apperr.New(apperr.InvalidInput, "The latest version of %s already plays what was said (“%s”).", sayCutWords(t.project, t.branch), c.Before)
		}
		mix.Said = append(mix.Said[:saidIndex(mix.Said, c.ID)], mix.Said[saidIndex(mix.Said, c.ID)+1:]...)
		if len(mix.Said) == 0 {
			mix.Said = nil
		}
		if equalMix(mix, &editspec.Mix{}) {
			mix = nil
		}
		summary = fmt.Sprintf("“%s” is heard again as %s said it, in place of “%s”.", c.Before, c.Speaker.Name, c.Words)
		instruction = fmt.Sprintf("Undo the changed words: “%s” again in place of “%s”", c.Before, c.Words)
	} else {
		if err := mayPlace(ctx, a, c); err != nil {
			return nil, err
		}
		if has {
			return nil, apperr.New(apperr.InvalidInput, "The latest version of %s already says “%s”.", sayCutWords(t.project, t.branch), c.Words)
		}
		if !in.AllowUnlock {
			for i, cl := range t.spec.Clips {
				if cl.Locked && cl.AssetID == c.AssetID && cl.In < c.Fit.To && c.Fit.From < cl.Out {
					return nil, t.lockedErr(&editspec.LockedError{ID: t.spec.Clips[i].ID})
				}
			}
		}
		if mix == nil {
			mix = &editspec.Mix{}
		}
		said = saidSound(c)
		if said.Picture != "" { // the matched mouth only while the consents behind it hold (D312)
			why, err := mouthWithdrawn(ctx, a, c, t.project.ID)
			if err != nil {
				return nil, err
			}
			if why != "" {
				said.Picture, mouthNote = "", " The mouth isn't matched: "+why
			}
		}
		mix.Said = append(mix.Said, said)
		summary = fmt.Sprintf("%s says “%s” in place of “%s”, in their own voice copy (marked as changed with AI).", c.Speaker.Name, c.Words, c.Before)
		instruction = fmt.Sprintf("Change spoken words: “%s” → “%s” (%s's voice copy)", c.Before, c.Words, c.Speaker.Name)
	}
	verb := "say"
	if undo {
		verb = "say-undo"
	}
	edits := []editspec.Op{{Op: "setMix", Mix: mix}}
	if undo {
		edits = append(edits, pictureBack(t.spec, c)...)
	} else if said.Picture != "" {
		edits = append(edits, pictureIn(t.spec, said)...)
		summary += " The mouth matches the new words."
	}
	summary += mouthNote
	ce, err := t.finish(withPlacing(ctx, c.ID), a, common, verb, edits, summary, instruction, &CutEditOut{})
	if err != nil {
		return nil, err
	}
	out := &CutSayPlaceOut{CutEditOut: *ce, ChangeID: c.ID}
	if !ce.Saved || ce.Applied == nil {
		return out, nil
	}
	rev := ce.Applied.Revision
	now := time.Now().UTC()
	recordPlacing(ctx, a, c, models.SpokenVersion{RevisionID: rev.ID, Branch: t.branch, By: a.Actor, At: now}, undo)
	if !in.NoRender {
		rd, err := CallTyped[RenderOut](ctx, a, "render.submit", RenderSubmitIn{RevisionID: rev.ID, Quality: "review"}, callerOf(ctx))
		if err != nil {
			out.Summary += " The review copy didn't start: " + plainErr(err)
		} else {
			out.RenderID = rd.Render.ID
			out.Summary += " A review copy is on its way."
		}
	}
	action := "cut.say.place"
	if undo {
		action = "cut.say.undo"
	}
	a.Audit(ctx, action, "revision", rev.ID, c.ProjectID, map[string]any{"change": c.ID, "before": c.Before, "words": c.Words, "person": c.Speaker.PersonID,
		"consent": c.ConsentID, "generation": c.GenerationID, "aiGenerated": true, "render": out.RenderID})
	return out, nil
}

// recordPlacing notes on the change the version that put it in or took it
// out, and its state: placed, or undone once no cut plays it. A change
// withdrawn meanwhile stays withdrawn either way.
func recordPlacing(ctx context.Context, a *app.App, c *models.SpokenChange, entry models.SpokenVersion, undo bool) {
	state, list := models.SpokenPlaced, "placed"
	if undo {
		// Taken out of this cut; still placed while another cut plays it.
		list = "undone"
		var p models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), c.ProjectID, &p) != nil || len(playingBranches(ctx, a, &p, map[string]*models.Revision{}, c)) == 0 {
			state = models.SpokenUndone
		}
	}
	_ = a.DB.Update(ctx, store.CSpokenChanges, a.WorkspaceID(), c.ID, bson.M{"$set": bson.M{"updatedAt": entry.At}, "$push": bson.M{list: entry}})
	_, _ = a.DB.C(store.CSpokenChanges).UpdateOne(ctx, bson.M{"_id": c.ID, "workspaceId": a.WorkspaceID(), "state": bson.M{"$ne": models.SpokenWithdrawn}},
		bson.M{"$set": bson.M{"state": state}})
}

// mayPlace refuses a change that isn't ready, or whose consent or voice copy
// was withdrawn since (D309): it is marked withdrawn then.
func mayPlace(ctx context.Context, a *app.App, c *models.SpokenChange) error {
	switch c.State {
	case models.SpokenMaking:
		return apperr.New(apperr.Conflict, "The new words are still being made and checked.").WithHint("Wait for the note that they're ready, then put them in.")
	case models.SpokenFailed:
		return apperr.New(apperr.Conflict, "These new words didn't pass their check, so they can't go in the cut: %s", c.Why)
	case models.SpokenWithdrawn:
		return apperr.New(apperr.ConsentRequired, "%s's consent for these new words was withdrawn, so they can't go in the cut.", c.Speaker.Name)
	}
	if c.Sound == "" || c.Fit == nil {
		return apperr.New(apperr.Conflict, "These new words have no sound to play.")
	}
	if err := stillNamed(ctx, a, c); err != nil {
		return err
	}
	why, err := saidWithdrawn(ctx, a, c)
	if err != nil {
		return err
	}
	if why != "" {
		_ = a.DB.Update(ctx, store.CSpokenChanges, a.WorkspaceID(), c.ID, bson.M{"$set": bson.M{"state": models.SpokenWithdrawn, "why": why, "updatedAt": time.Now().UTC()}})
		return apperr.New(apperr.ConsentRequired, "%s The new words can't go in the cut.", why)
	}
	return nil
}

// stillNamed refuses a change whose voice a person no longer names as the
// same person (renamed, unnamed, or now named by something that isn't a
// person, D274): a person names it again first. The change itself stays.
func stillNamed(ctx context.Context, a *app.App, c *models.SpokenChange) error {
	var vs []models.AssetSpeaker
	if err := a.DB.Find(ctx, store.CSpeakers, a.WorkspaceID(), bson.M{"assetId": c.AssetID}, &vs); err != nil {
		return err
	}
	byLabel := map[string]*models.AssetSpeaker{}
	for i := range vs {
		byLabel[vs[i].Label] = &vs[i]
	}
	v := byLabel[c.Speaker.Label]
	for n := 0; v != nil && v.MergedInto != "" && n < 8; n++ {
		v = byLabel[v.MergedInto]
	}
	if v == nil || v.PersonID != c.Speaker.PersonID || v.Source != models.SpeakerByPerson || !v.NamedByPerson() {
		return apperr.New(apperr.Conflict, "The voice that says “%s” isn't named as %s by a person any more, so the new words can't go in the cut.", c.Before, c.Speaker.Name).
			WithHint("If it is " + c.Speaker.Name + ", name the voice again (who's speaking, in the clip's Speakers tab), then put the change in.")
	}
	return nil
}

// saidSound is a change as the cut's mix holds it.
func saidSound(c *models.SpokenChange) editspec.SaidSound {
	s := editspec.SaidSound{ID: c.ID, AssetID: c.AssetID, From: c.Fit.From, To: c.Fit.To, Sound: c.Sound, WordsFrom: c.Fit.WordsFrom, WordsTo: c.Fit.WordsTo,
		Words: c.Words, Before: c.Before, Who: c.Speaker.Name}
	if m := c.Mouth; m != nil && m.State == models.SpokenReady && m.AssetID != "" {
		s.Picture = m.AssetID
	}
	return s
}

func saidIndex(xs []editspec.SaidSound, id string) int {
	for i, x := range xs {
		if x.ID == id {
			return i
		}
	}
	return -1
}

func equalMix(a, b *editspec.Mix) bool {
	return editspec.Hash(&editspec.Spec{Mix: a}) == editspec.Hash(&editspec.Spec{Mix: b})
}

func sayCutWords(p *models.Project, branch string) string {
	if b, ok := p.Branches[branch]; ok && strings.TrimSpace(b.Title) != "" {
		return q(strings.TrimSpace(b.Title))
	}
	if branch == "main" || branch == "" {
		return "the cut of " + q(p.Name)
	}
	return "the cut " + q(branch) + " of " + q(p.Name)
}

func sayFirst(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return strings.TrimSpace(x)
		}
	}
	return ""
}

func listSaid(ctx context.Context, a *app.App, in *CutSayListIn) (*CutSayListOut, error) {
	f := bson.M{}
	if in.ProjectID != "" {
		p, err := resolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, err
		}
		f["projectId"] = p.ID
	}
	if in.AssetID != "" {
		f["assetId"] = in.AssetID
	}
	if len(f) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "Say which project (or clip) to list the changed words of.")
	}
	var cs []models.SpokenChange
	if err := a.DB.Find(ctx, store.CSpokenChanges, a.WorkspaceID(), f, &cs, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(200)); err != nil {
		return nil, err
	}
	out := &CutSayListOut{Changes: spokenViews(ctx, a, cs)}
	in2 := 0
	for _, v := range out.Changes {
		if v.InCut {
			in2++
		}
	}
	switch len(cs) {
	case 0:
		out.Summary = "No words were changed in anyone's voice here."
	case 1:
		out.Summary = "1 change of words; " + fmt.Sprintf("%d in the cut.", in2)
	default:
		out.Summary = fmt.Sprintf("%d changes of words; %d in the cut.", len(cs), in2)
	}
	return out, nil
}

// spokenViews adds what people read to changes: the clip and cut by name,
// whether the cut's latest version plays it, one plain line and what's next.
func spokenViews(ctx context.Context, a *app.App, cs []models.SpokenChange) []SpokenView {
	out := make([]SpokenView, 0, len(cs))
	projects := map[string]*models.Project{}
	heads := map[string]*models.Revision{} // projectID/branch → its latest version
	for _, c := range cs {
		v := SpokenView{SpokenChange: c}
		var as models.Asset
		if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), c.AssetID, &as) == nil {
			v.ClipTitle = as.DisplayTitle()
		}
		p, ok := projects[c.ProjectID]
		if !ok {
			var pr models.Project
			if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), c.ProjectID, &pr) == nil {
				p = &pr
			}
			projects[c.ProjectID] = p
		}
		if p != nil {
			v.CutTitle = sayCutWords(p, c.Branch)
			v.Branches = playingBranches(ctx, a, p, heads, &c)
			v.InCut = len(v.Branches) > 0
			if v.InCut && v.Branches[0] != c.Branch {
				v.CutTitle = sayCutWords(p, v.Branches[0])
			}
		}
		v.Says = fmt.Sprintf("“%s” → “%s” in %s's own voice copy", c.Before, c.Words, c.Speaker.Name)
		switch c.State {
		case models.SpokenMaking:
			v.Next = "It's being made and checked."
		case models.SpokenReady, models.SpokenUndone:
			if !v.InCut {
				v.Next = "Listen to it, then put it in the cut."
			}
		case models.SpokenPlaced:
			if v.InCut {
				v.Next = "Undo puts back what was said."
			}
		case models.SpokenFailed:
			v.Next = c.Why
			if !strings.Contains(strings.ToLower(c.Why), "try again") {
				v.Next = strings.TrimSpace(c.Why + " You can make it again.")
			}
		case models.SpokenWithdrawn:
			v.Next = c.Why
		}
		if v.Check != nil && v.Check.Voice != nil {
			v.Says += fmt.Sprintf(" (heard as “%s”, the voice %.0f%% alike)", v.Check.Heard, math.Round(*v.Check.Voice*100))
		}
		out = append(out, v)
	}
	return out
}

// playingBranches lists the project's cuts whose latest version plays a
// change: the one it was made for first, then the others by name.
func playingBranches(ctx context.Context, a *app.App, p *models.Project, heads map[string]*models.Revision, c *models.SpokenChange) []string {
	names := make([]string, 0, len(p.Branches))
	for b := range p.Branches {
		if b != c.Branch {
			names = append(names, b)
		}
	}
	sort.Strings(names)
	if _, ok := p.Branches[c.Branch]; ok {
		names = append([]string{c.Branch}, names...)
	}
	var out []string
	for _, b := range names {
		key := p.ID + "/" + b
		r, ok := heads[key]
		if !ok {
			if id := p.Branches[b].Head; id != "" {
				var rv models.Revision
				if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), id, &rv) == nil {
					r = &rv
				}
			}
			heads[key] = r
		}
		if r != nil && r.Spec.Mix != nil && saidIndex(r.Spec.Mix.Said, c.ID) >= 0 {
			out = append(out, b)
		}
	}
	return out
}

// saidIn lists the changes made to a clip's words that are ready or that a
// cut plays (undone in one cut but still in another, or withdrawn but not
// taken out yet, they stay marked).
func saidIn(ctx context.Context, a *app.App, assetID string) []TranscriptSaid {
	var cs []models.SpokenChange
	if err := a.DB.Find(ctx, store.CSpokenChanges, a.WorkspaceID(), bson.M{"assetId": assetID,
		"state": bson.M{"$in": bson.A{models.SpokenReady, models.SpokenPlaced, models.SpokenUndone, models.SpokenWithdrawn}}}, &cs,
		options.Find().SetSort(bson.D{{Key: "from", Value: 1}}).SetLimit(200)); err != nil || len(cs) == 0 {
		return nil
	}
	var out []TranscriptSaid
	for _, v := range spokenViews(ctx, a, cs) {
		if !v.InCut && v.State != models.SpokenReady {
			continue
		}
		out = append(out, TranscriptSaid{ChangeID: v.ID, From: v.From, To: v.To, Before: v.Before, Words: v.Words, Who: v.Speaker.Name, CutTitle: v.CutTitle, InCut: v.InCut, State: v.State})
	}
	return out
}

// pictureIn plays the stretch with the mouth matched (D312) in place of the
// recording's picture: each clip that plays the whole stretch is split around
// it and its middle plays the matched clip (the new sound plays over it, as
// over the recording).
func pictureIn(s *editspec.Spec, said editspec.SaidSound) []editspec.Op {
	var out []editspec.Op
	fps := float64(s.Output.FPS)
	if fps <= 0 {
		fps = 30
	}
	frame := 1 / fps
	taken := map[string]bool{}
	for _, c := range s.Clips {
		taken[c.ID] = true
	}
	free := func(base string) string {
		for i := 1; ; i++ {
			id := fmt.Sprintf("%s-said-%d", base, i)
			if len(id) > 64 {
				id = fmt.Sprintf("said-%d", i)
			}
			if !taken[id] {
				taken[id] = true
				return id
			}
		}
	}
	for _, c := range s.Clips {
		speed := c.Speed
		if speed == 0 {
			speed = 1
		}
		if c.AssetID != said.AssetID || c.In > said.From+0.001 || said.To > c.Out+0.001 || speed != 1 {
			continue
		}
		mid := c.ID
		if said.From-c.In >= frame {
			at := said.From
			mid = free(c.ID)
			out = append(out, editspec.Op{Op: "splitClip", ID: c.ID, At: &at, NewID: mid})
		}
		if c.Out-said.To >= frame {
			at := said.To
			out = append(out, editspec.Op{Op: "splitClip", ID: mid, At: &at, NewID: free(c.ID)})
		}
		set, _ := json.Marshal(map[string]any{"assetId": said.Picture, "in": 0, "out": round3(said.To - said.From)})
		out = append(out, editspec.Op{Op: "updateClip", ID: mid, Set: set})
	}
	return out
}

// pictureBack plays the recording's own picture again where the matched
// mouth played (undo).
func pictureBack(s *editspec.Spec, c *models.SpokenChange) []editspec.Op {
	if c.Mouth == nil || c.Mouth.AssetID == "" || c.Fit == nil {
		return nil
	}
	var out []editspec.Op
	for _, cl := range s.Clips {
		if cl.AssetID != c.Mouth.AssetID {
			continue
		}
		set, _ := json.Marshal(map[string]any{"assetId": c.AssetID, "in": round3(c.Fit.From + cl.In), "out": round3(c.Fit.From + cl.Out)})
		out = append(out, editspec.Op{Op: "updateClip", ID: cl.ID, Set: set})
	}
	return out
}

type placingKey struct{}

// withPlacing says Put it in the cut is placing this change (its own checks ran).
func withPlacing(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, placingKey{}, id)
}

// saidAllowed holds every edit to the cut's changed words (D308, D309): words
// a version already had stay; new or altered ones come only from Put it in
// the cut, or from a person's own edit of a change that was made, checked and
// is still in force (its consent and voice copy, a person's name for the
// voice). Anything else — an AI app, Claude, a script, a draft — is refused.
func saidAllowed(ctx context.Context, a *app.App, projectID string, base, next *editspec.Spec) error {
	if err := saidInPlace(ctx, a, next); err != nil {
		return err
	}
	if next == nil || next.Mix == nil || len(next.Mix.Said) == 0 {
		return nil
	}
	had := map[string]editspec.SaidSound{}
	if base != nil && base.Mix != nil {
		for _, s := range base.Mix.Said {
			had[s.ID] = s
		}
	}
	placing, _ := ctx.Value(placingKey{}).(string)
	for _, s := range next.Mix.Said {
		if o, ok := had[s.ID]; ok && equalSaid(o, s) {
			continue
		}
		if s.ID == placing {
			continue // Put it in the cut checked it
		}
		if !callerOf(ctx).Human {
			return apperr.New(apperr.HumanOnly, "Only a person puts changed words in a cut (“%s” in place of “%s”): use Put it in the cut.", s.Words, s.Before).
				WithCommand("metarush cut say place " + s.ID)
		}
		c, err := loadSaid(ctx, a, s.ID)
		if err != nil {
			return apperr.New(apperr.InvalidInput, "The cut names changed words (“%s”) that MetaRush didn't make and check here.", s.Words)
		}
		if c.ProjectID != projectID || c.Fit == nil || c.Sound != s.Sound || c.AssetID != s.AssetID || c.Words != s.Words || c.Before != s.Before ||
			math.Abs(c.Fit.From-s.From) > 0.002 || math.Abs(c.Fit.To-s.To) > 0.002 {
			return apperr.New(apperr.InvalidInput, "The changed words “%s” in this edit aren't the ones that were made and checked; put them in with Put it in the cut.", s.Words)
		}
		if s.Picture != "" && (c.Mouth == nil || c.Mouth.State != models.SpokenReady || c.Mouth.AssetID != s.Picture) {
			return apperr.New(apperr.InvalidInput, "The matched mouth for “%s” in this edit isn't the one that was made; put the change in with Put it in the cut.", s.Words)
		}
		if s.Picture != "" {
			if why, err := mouthWithdrawn(ctx, a, c, projectID); err != nil {
				return err
			} else if why != "" {
				return apperr.New(apperr.ConsentRequired, "%s The mouth matched to “%s” can't go in the cut.", why, s.Words)
			}
		}
		// Made, checked and still in force (being made, failed and withdrawn are refused there).
		if err := mayPlace(ctx, a, c); err != nil {
			return err
		}
	}
	return nil
}

// saidStillIn is what a version's changed words count as when a change is
// built on it: all of them while it is its cut's latest version, else only
// those its cut's latest version still plays (a person may have taken the
// others out since; any other comes back only as a person's own edit).
func saidStillIn(ctx context.Context, a *app.App, p *models.Project, base *models.Revision) (*editspec.Spec, error) {
	if base.Spec.Mix == nil || len(base.Spec.Mix.Said) == 0 {
		return &base.Spec, nil
	}
	head := p.Branches[orBranch(base.Branch)].Head
	if head == base.ID {
		return &base.Spec, nil
	}
	plays := map[string]editspec.SaidSound{}
	if head != "" {
		var hr models.Revision
		if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), head, &hr); err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		} else if err == nil && hr.Spec.Mix != nil {
			for _, sd := range hr.Spec.Mix.Said {
				plays[sd.ID] = sd
			}
		}
	}
	var kept []editspec.SaidSound
	for _, sd := range base.Spec.Mix.Said {
		if h, ok := plays[sd.ID]; ok && equalSaid(h, sd) {
			kept = append(kept, sd)
		}
	}
	return &editspec.Spec{Mix: &editspec.Mix{Said: kept}}, nil
}

// saidInPlace keeps new words where they replace what was said (D308): a
// change's new sound plays only through its own mix.said entry, and its
// matched mouth only as the picture over that stretch — never as an ordinary
// track or clip, for anyone.
func saidInPlace(ctx context.Context, a *app.App, next *editspec.Spec) error {
	if next == nil {
		return nil
	}
	rest := *next // everything but the changed words themselves
	if next.Mix != nil {
		m := *next.Mix
		m.Said = nil
		rest.Mix = &m
	}
	ids := editspec.AssetIDs(&rest)
	if len(ids) == 0 {
		return nil
	}
	in := bson.A{}
	for _, id := range ids {
		in = append(in, id)
	}
	var cs []models.SpokenChange
	if err := a.DB.Find(ctx, store.CSpokenChanges, a.WorkspaceID(), bson.M{"$or": bson.A{bson.M{"sound": bson.M{"$in": in}}, bson.M{"mouth.assetId": bson.M{"$in": in}}}}, &cs); err != nil {
		return err
	}
	var sounds []models.Asset // new sounds whose change has no record here
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": in}, "source.generated.said": bson.M{"$exists": true}}, &sounds); err != nil {
		return err
	}
	refuse := func(words string) error {
		return apperr.New(apperr.InvalidInput, "The new words “%s” play only where they replace what was said, through Put it in the cut; they can't go in as a track or a clip of their own.", words).
			WithHint("Put the change in the cut (cut say place), or leave them out.")
	}
	used := map[string]bool{}
	for _, id := range ids {
		used[id] = true
	}
	for _, c := range cs {
		if c.Sound != "" && used[c.Sound] {
			return refuse(c.Words)
		}
	}
	for _, as := range sounds {
		if g := as.Source.Generated; g != nil && g.Said != nil {
			return refuse(firstNonEmpty(g.Said.Words, "these"))
		}
	}
	// A matched mouth: only clips over the stretch of a change this cut plays with that picture.
	noClips := rest
	noClips.Clips = nil
	elsewhere := map[string]bool{}
	for _, id := range editspec.AssetIDs(&noClips) {
		elsewhere[id] = true
	}
	stretch := map[string]float64{}
	if next.Mix != nil {
		for _, sd := range next.Mix.Said {
			if sd.Picture != "" {
				stretch[sd.Picture] = sd.To - sd.From
			}
		}
	}
	for _, c := range cs {
		m := c.Mouth
		if m == nil || m.AssetID == "" || !used[m.AssetID] {
			continue
		}
		ok := !elsewhere[m.AssetID]
		long, placed := stretch[m.AssetID]
		for _, cl := range next.Clips {
			if cl.AssetID == m.AssetID && (!placed || cl.In < -0.01 || cl.Out > long+0.01) {
				ok = false
			}
		}
		if !ok {
			return apperr.New(apperr.InvalidInput, "The mouth matched to “%s” plays only over those words, through Put it in the cut; it can't go in as a clip of its own.", c.Words).
				WithHint("Put the change in the cut (cut say place), or leave it out.")
		}
	}
	return nil
}

func equalSaid(a, b editspec.SaidSound) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
