package remotion

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Clips in cuts (D135, D136): a composition's clip goes into a cut through
// the same check and save as any edit; when a person approves a new version,
// the latest cuts' clips from older versions move to it in a new revision
// (renders first). Older revisions keep their clips: their renders never
// change.

// settleAddTo puts a finished render where it was asked for.
func settleAddTo(ctx context.Context, a *app.App, s *models.RemotionSource, r *models.RemotionRender, adds []models.RemotionAddTo) error {
	var firstErr error
	for _, ad := range adds {
		pa := *a
		pa.Actor = ad.By
		var err error
		switch {
		case ad.Update:
			_, err = updateProject(ctx, &pa, s, ad.ProjectID, ad.Branch)
		case ad.Sound == models.RemotionSoundLayers:
			_, _, err = addClipLayers(ctx, &pa, s, r, ad)
		default:
			_, err = addClip(ctx, &pa, s, r, ad)
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	_ = setRender(ctx, a, r.Key, bson.M{"addTo": []models.RemotionAddTo{}})
	return firstErr
}

// latestRevision is a project's newest revision on a branch (nil: no cut yet).
func latestRevision(ctx context.Context, a *app.App, projectID, branch string) (*models.Project, *models.Revision, error) {
	if branch == "" {
		branch = "main"
	}
	var p models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), projectID, &p); err != nil {
		return nil, nil, apperr.New(apperr.NotFound, "That project is gone.")
	}
	head := p.Branches[branch].Head
	if head == "" {
		return &p, nil, nil
	}
	var r models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), head, &r); err != nil {
		return &p, nil, err
	}
	return &p, &r, nil
}

// saved is a change that became a new revision.
type saved struct {
	RevisionID string
	Number     int
}

// savePlan checks a plan and saves it with its own confirm token.
func savePlan(ctx context.Context, a *app.App, plan editspec.Plan) (*saved, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	chk, err := ops.CallTyped[ops.EditCheckOut](ctx, a, "edit.check", ops.EditCheckIn{Plan: raw}, ops.Caller{Human: a.Actor.Via != models.ViaAssistant})
	if err != nil {
		return nil, err
	}
	if !chk.Valid {
		msg := "the change didn't pass its check"
		if len(chk.Errors) > 0 {
			msg = chk.Errors[0].Message
		}
		return nil, apperr.New(apperr.Validation, "%s", upperFirst(strings.TrimSuffix(msg, "."))+".")
	}
	out, err := ops.CallTyped[ops.EditApplyOut](ctx, a, "edit.apply", ops.EditApplyIn{Plan: raw, ConfirmToken: chk.ConfirmToken}, ops.Caller{Human: a.Actor.Via != models.ViaAssistant})
	if err != nil {
		return nil, err
	}
	return &saved{RevisionID: out.Revision.ID, Number: out.Revision.Number}, nil
}

// addClip puts a render into a project's cut as a new clip (the project's
// first cut when it has none).
func addClip(ctx context.Context, a *app.App, s *models.RemotionSource, r *models.RemotionRender, ad models.RemotionAddTo) (*saved, error) {
	p, rev, err := latestRevision(ctx, a, ad.ProjectID, ad.Branch)
	if err != nil {
		return nil, err
	}
	in, out := 0.0, r.Duration
	if ad.In != nil {
		in = math.Max(0, math.Min(*ad.In, r.Duration))
	}
	if ad.Out != nil && *ad.Out > in {
		out = math.Min(*ad.Out, r.Duration)
	}
	instruction := fmt.Sprintf("Add “%s” from the Remotion project “%s”", r.Composition, s.Title)
	taken := map[string]bool{}
	if rev != nil {
		for _, c := range rev.Spec.Clips {
			taken[c.ID] = true
		}
	}
	id := clipID(r.Composition, ad.ClipID, taken)
	clip := editspec.Clip{ID: id, AssetID: r.AssetID, In: round3(in), Out: round3(out), Label: r.Composition}
	if rev == nil {
		spec := &editspec.Spec{SchemaVersion: editspec.SchemaVersion, Output: outputFor(r), Clips: []editspec.Clip{clip}}
		return savePlan(ctx, a, editspec.Plan{ProjectID: p.ID, Instruction: instruction, Origin: "remotion:use", Spec: spec})
	}
	after := ad.After
	if after == "" && len(rev.Spec.Clips) > 0 {
		after = rev.Spec.Clips[len(rev.Spec.Clips)-1].ID
	}
	op := editspec.Op{Op: "insertClip", After: after, Clip: &clip}
	return savePlan(ctx, a, editspec.Plan{BaseRevisionID: rev.ID, Branch: rev.Branch, Instruction: instruction, Origin: "remotion:use", Ops: []editspec.Op{op}})
}

// outputFor picks a first cut's frame from the composition's shape.
func outputFor(r *models.RemotionRender) editspec.Output {
	o := editspec.Output{Preset: editspec.PresetLandscape, FPS: 30}
	switch {
	case r.Height > r.Width:
		o.Preset = editspec.PresetVertical
	case r.Height == r.Width:
		o.Preset = editspec.PresetSquare
	}
	for _, f := range []int{24, 25, 30, 50, 60} {
		if math.Abs(r.FPS-float64(f)) < 0.01 {
			o.FPS = f
		}
	}
	return o
}

// clipID makes a clip's name from the composition's ("KiteTeaserV3" →
// "kiteteaserv3"), unique in the cut.
func clipID(comp, want string, taken map[string]bool) string {
	base := want
	if base == "" {
		base = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(comp), "-"), "-")
	}
	if base == "" || base[0] < 'a' || base[0] > 'z' {
		base = "clip-" + base
	}
	if len(base) > 56 {
		base = base[:56]
	}
	id := base
	for i := 2; taken[id]; i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	return id
}

func round3(x float64) float64 { return math.Round(x*1000) / 1000 }

// RemotionUpdateResult says what moving a project's clips did.
type RemotionUpdateResult struct {
	ProjectID string `json:"projectId"`
	Moved     int    `json:"moved" jsonschema:"description=Clips moved to the approved version."`
	Waiting   int    `json:"waiting" jsonschema:"description=Clips waiting for their render at the approved version (they move when it is done)."`
	Revision  string `json:"revision,omitempty"`
	Number    int    `json:"number,omitempty"`
	Message   string `json:"message"`
}

// updateProject moves the clips of a project's latest cut that come from a
// source to what they should be now: its newest approved version, with the
// source's current word settings (D169) and pictures (D209, the clip's own
// pictures over them) and the clip's own sound choice (D170). Clips already
// made of exactly that stay.
func updateProject(ctx context.Context, a *app.App, s *models.RemotionSource, projectID, branch string) (*RemotionUpdateResult, error) {
	res := &RemotionUpdateResult{ProjectID: projectID}
	fresh, err := getSource(ctx, a, s.ID)
	if err != nil {
		return nil, err
	}
	s = fresh
	if s.Approved == "" {
		res.Message = fmt.Sprintf("No version of “%s” is approved, so there is nothing to move to.", s.Title)
		return res, nil
	}
	nv, err := getVersion(ctx, a, s.Approved)
	if err != nil {
		return nil, err
	}
	_, rev, err := latestRevision(ctx, a, projectID, branch)
	if err != nil {
		return nil, err
	}
	if rev == nil {
		res.Message = "The project has no cut yet."
		return res, nil
	}
	var ids []string
	for _, c := range rev.Spec.Clips {
		ids = append(ids, c.AssetID)
	}
	var assets []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}, "source.remotion.sourceId": s.ID, "source.remotion.kind": models.SourceRemotionRender}, &assets); err != nil {
		return nil, err
	}
	from := map[string]*models.RemotionClipSource{}
	for i := range assets {
		from[assets[i].ID] = assets[i].Source.Remotion
	}
	var eops []editspec.Op
	sameVersion := true // every clip that moves only gets new words or pictures
	newWords, newPictures := false, false
	for _, c := range rev.Spec.Clips {
		rc := from[c.AssetID]
		if rc == nil || c.Locked {
			continue
		}
		if nv.Composition(rc.Composition) == nil {
			continue // the composition is gone in the new version: the clip stays as it is
		}
		words, wn := wordsFor(s, nv, rc.Composition)
		pics, pvals, pn := picturesFor(s, nv, rc.Composition, rc.OwnPictures)
		key := renderKey(s, nv, rc.Composition, rc.Props, rc.Frames, variant{Sound: rc.Sound, Words: words, WordsNumber: wn, Pictures: pics})
		if key == rc.RenderKey {
			continue // made of exactly that already
		}
		if rc.Version != nv.Hash && rc.Words == words && rc.Pictures == pics && unchangedIn(nv, rc) {
			continue // the check showed this composition looks the same: its clip (still from an approved version) stays
		}
		if rc.Version != nv.Hash {
			sameVersion = false
		}
		newWords = newWords || rc.Words != words
		newPictures = newPictures || rc.Pictures != pics
		r, err := getRender(ctx, a, key)
		if err != nil || r.State != models.RemotionRenderDone || r.AssetID == "" {
			if _, err := requestRender(ctx, a, renderRequest{Source: s, Version: nv, Composition: rc.Composition, Props: rc.Props, Frames: rc.Frames, Sound: rc.Sound,
				Words: words, WordsNumber: wn, Pictures: pics, PictureValues: pvals, PicturesNumber: pn, OwnPictures: rc.OwnPictures, KeepSettings: true,
				AddTo: &models.RemotionAddTo{ProjectID: projectID, Branch: branch, Update: true, By: a.Actor, At: time.Now().UTC()}}); err != nil {
				return nil, err
			}
			res.Waiting++
			continue
		}
		in, out := c.In, c.Out
		if out > r.Duration {
			out = r.Duration
		}
		if in >= out {
			in = math.Max(0, out-(c.Out-c.In))
		}
		set, _ := json.Marshal(map[string]any{"assetId": r.AssetID, "in": round3(in), "out": round3(out)})
		eops = append(eops, editspec.Op{Op: "updateClip", ID: c.ID, Set: set})
	}
	when := map[bool]string{true: "it is done", false: "they are done"}[res.Waiting == 1]
	changedWhat := "its words as changed"
	switch {
	case newWords && newPictures:
		changedWhat = "its words and pictures as changed"
	case newPictures:
		changedWhat = "its pictures as changed"
	}
	if res.Waiting > 0 && sameVersion {
		res.Message = fmt.Sprintf("Making %s of “%s” again with %s; the cut moves to %s when %s.", plural(res.Waiting, "clip", "clips"), s.Title, changedWhat, map[bool]string{true: "it", false: "them"}[res.Waiting == 1], when)
		return res, nil
	}
	if res.Waiting > 0 {
		res.Message = fmt.Sprintf("Rendering %s at version %d of “%s”; the cut moves to it when %s.", plural(res.Waiting, "clip", "clips"), nv.Number, s.Title, when)
		return res, nil
	}
	if len(eops) == 0 {
		res.Message = fmt.Sprintf("The latest cut's clips from “%s” are up to date.", s.Title)
		return res, nil
	}
	instruction := fmt.Sprintf("Move the clips from “%s” to version %d", s.Title, nv.Number)
	if sameVersion {
		instruction = fmt.Sprintf("Make the clips from “%s” again with %s", s.Title, changedWhat)
	} else if nv.ApprovedBy != nil {
		instruction += ", which " + actorName(*nv.ApprovedBy) + " approved"
	}
	sv, err := savePlan(ctx, a, editspec.Plan{BaseRevisionID: rev.ID, Branch: rev.Branch, Instruction: instruction, Origin: "remotion:update", Ops: eops})
	if err != nil {
		return nil, err
	}
	res.Moved, res.Revision, res.Number = len(eops), sv.RevisionID, sv.Number
	res.Message = fmt.Sprintf("Moved %s to version %d of “%s” (the cut is now version %d).", plural(len(eops), "clip", "clips"), nv.Number, s.Title, sv.Number)
	if sameVersion {
		res.Message = fmt.Sprintf("Moved %s of “%s” to %s (the cut is now version %d).", plural(len(eops), "clip", "clips"), s.Title, changedWhat, sv.Number)
	}
	return res, nil
}

// unchangedIn reports a clip whose composition the new version's check
// proved unchanged against the clip's own version: the clip comes from the new
// version's parent and the composition was either out of the change's reach
// or compared frame by frame and found the same.
func unchangedIn(nv *models.RemotionVersion, rc *models.RemotionClipSource) bool {
	c := nv.Check
	if c == nil || !c.OK || nv.Parent == "" || rc.Version != nv.Parent || rc.Props != "" {
		return false
	}
	in := func(xs []string) bool {
		for _, x := range xs {
			if x == rc.Composition {
				return true
			}
		}
		return false
	}
	if !in(c.Affected) {
		return true
	}
	return in(c.Compared) && !in(c.Changed)
}

// projectsUsing lists the projects whose clips come from a source (its own first).
func projectsUsing(ctx context.Context, a *app.App, s *models.RemotionSource) []string {
	out := []string{s.ProjectID}
	seen := map[string]bool{s.ProjectID: true}
	var assets []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"source.remotion.sourceId": s.ID}, &assets); err != nil {
		return out
	}
	for _, as := range assets {
		for _, p := range as.ProjectIDs {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}
