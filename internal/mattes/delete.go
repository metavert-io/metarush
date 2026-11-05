package mattes

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Deleting footage for good deletes its cut-outs too (D505): they are its
// picture, so they go with it — the work still being made stopped, the
// files deleted from the media store and this computer's cache (asset.delete
// runs this only when no cut uses the footage, so no version pins them).
func init() { ops.RegisterDeleteHook("cut-outs", deleteCutOuts) }

func deleteCutOuts(ctx context.Context, a *app.App, assetID string) (int, error) {
	docs, err := OfClip(ctx, a, assetID)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range docs {
		if removeCutOut(ctx, a, &docs[i]) == nil {
			n++
		}
	}
	return n, nil
}

// removeCutOut deletes one cut-out: its work stopped while it is made, its
// file from the media store and this computer's cache unless another
// cut-out record has the same bytes, then its record.
func removeCutOut(ctx context.Context, a *app.App, m *models.Matte) error {
	if m.JobID != "" && (m.State == models.MatteWaiting || m.State == models.MatteMaking) {
		_, _ = a.Queue.RequestCancel(ctx, m.JobID)
	}
	if f := m.File; f != nil {
		others, _ := a.DB.C(store.CMattes).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "file.sha256": f.SHA256, "_id": bson.M{"$ne": m.ID}})
		if others == 0 {
			if f.Key != "" {
				_ = a.Media.Delete(ctx, f.Key)
			}
			_, _ = a.Cache.Remove(f.SHA256)
		}
	}
	_, err := a.DB.C(store.CMattes).DeleteOne(ctx, bson.M{"_id": m.ID, "workspaceId": a.WorkspaceID()})
	return err
}

// CutoutDeleteIn deletes a clip's cut-out.
type CutoutDeleteIn struct {
	ID string `json:"id" jsonschema:"pattern=^mte_[0-9a-z]{26}$,description=The cut-out (footage cut-outs lists a clip's)."`
	// Forever must be true: the file goes for every computer.
	Forever bool `json:"forever,omitempty" jsonschema:"description=Must be true: the cut-out's file is deleted for every computer of the workspace (it can be made again\\, as a new file)."`
	DryRun  bool `json:"dryRun,omitempty" jsonschema:"description=Only say whether it can be deleted and what plays it."`
}

// CutoutDeleteOut says what was (or would be) deleted.
type CutoutDeleteOut struct {
	Allowed bool       `json:"allowed"`
	Deleted bool       `json:"deleted"`
	Cutout  CutoutView `json:"cutout"`
	// Blockers are what plays it, in plain words: versions of cuts (any, the
	// latest or older, a draft's too, or a cut nesting one), drafts waiting
	// for review and versions in Recipes. Versions keep what they were saved
	// with, so a cut-out stays while anything plays it.
	Blockers []string `json:"blockers"`
	Summary  string   `json:"summary"`
}

// Delete deletes a cut-out for good (owners, people only, like asset.delete):
// refused while anything plays it — any version of a cut (versions keep what
// they were saved with: one rendered again must find it), a draft waiting for
// review, a version in Recipes.
func Delete(ctx context.Context, a *app.App, in *CutoutDeleteIn) (*CutoutDeleteOut, error) {
	m, err := Load(ctx, a, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, err
	}
	clip, dur := "the clip", 0.0
	var as models.Asset
	if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), m.AssetID, &as) == nil {
		clip, dur = as.DisplayTitle(), as.Media.Duration
	}
	out := &CutoutDeleteOut{Cutout: ViewOf(m, clip, dur, nil), Blockers: []string{}}
	if m.File != nil {
		if out.Blockers, err = playedBy(ctx, a, m.File.SHA256); err != nil {
			return nil, err
		}
	}
	out.Allowed = len(out.Blockers) == 0
	what := fmt.Sprintf("the cut-out of “%s” (%s)", clip, out.Cutout.Part)
	if !out.Allowed {
		shown := out.Blockers
		if len(shown) > 5 {
			shown = append(append([]string{}, shown[:5]...), fmt.Sprintf("%d more", len(out.Blockers)-5))
		}
		out.Summary = fmt.Sprintf("%s can't be deleted: %s %s it. Versions keep what they were saved with, so a cut-out stays while anything plays it.",
			upperFirst(what), joinAnd(shown), playWord(len(out.Blockers)))
		if in.DryRun {
			return out, nil
		}
		return nil, apperr.New(apperr.Conflict, "%s", out.Summary).WithDetails(map[string]any{"blockers": out.Blockers, "matteId": m.ID})
	}
	if in.DryRun {
		out.Summary = fmt.Sprintf("%s can be deleted: nothing plays it. Its file goes for every computer of this workspace, and it can be made again.", upperFirst(what))
		return out, nil
	}
	if !in.Forever {
		return nil, apperr.New(apperr.ConfirmRequired, "Deleting %s can't be undone.", what).
			WithHint("Confirm that you want it deleted for good.").WithCommand("metarush footage cut-out-delete " + m.ID + " --forever")
	}
	if err := removeCutOut(ctx, a, m); err != nil {
		return nil, err
	}
	a.Audit(ctx, "footage.cutout.delete", "matte", m.ID, m.ProjectID, map[string]any{"assetId": m.AssetID, "from": m.From, "to": m.To,
		"model": m.Provider + "/" + m.Model})
	out.Deleted = true
	out.Summary = fmt.Sprintf("Deleted %s.", what)
	return out, nil
}

// playedBy says, in plain words, what plays a cut-out file: the versions of
// cuts that pin it in a clip's background or nest a version that does (D84,
// at any depth) — any version that isn't detached, a draft's included, in
// any project, archived or not —, drafts waiting for review whose input
// names it, and versions in Recipes whose sections keep it.
func playedBy(ctx context.Context, a *app.App, sha string) ([]string, error) {
	proj := options.Find().SetProjection(bson.M{"_id": 1, "projectId": 1, "branch": 1, "number": 1, "draftId": 1})
	var playing []models.Revision
	if err := a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), bson.M{"spec.clips.background.matte": sha, "detached": bson.M{"$ne": true}}, &playing, proj); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var frontier []string
	for _, r := range playing {
		seen[r.ID] = true
		frontier = append(frontier, editspec.NestedKey(r.ProjectID, r.ID))
	}
	for depth := 0; depth < editspec.MaxNestDepth && len(frontier) > 0; depth++ {
		var outer []models.Revision
		if err := a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), bson.M{"spec.clips.assetId": bson.M{"$in": frontier}, "detached": bson.M{"$ne": true}}, &outer, proj); err != nil {
			return nil, err
		}
		frontier = nil
		for _, r := range outer {
			if !seen[r.ID] {
				seen[r.ID] = true
				playing = append(playing, r)
				frontier = append(frontier, editspec.NestedKey(r.ProjectID, r.ID))
			}
		}
	}
	// One line per cut: its versions, newest first.
	type cutKey struct{ project, branch string }
	versions := map[cutKey][]int{}
	var order []cutKey
	for _, r := range playing {
		k := cutKey{r.ProjectID, r.Branch}
		if _, ok := versions[k]; !ok {
			order = append(order, k)
		}
		versions[k] = append(versions[k], r.Number)
	}
	var out []string
	for _, k := range order {
		name := "a project cleared out"
		var p models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), k.project, &p) == nil {
			name = "“" + p.Name + "”"
		}
		cut := "cut"
		if k.branch != "" && k.branch != "main" {
			cut = k.branch + " cut"
		}
		ns := versions[k]
		sort.Sort(sort.Reverse(sort.IntSlice(ns)))
		words := make([]string, len(ns))
		for i, n := range ns {
			words[i] = fmt.Sprint(n)
		}
		v := "version " + words[0]
		if len(words) > 1 {
			v = "versions " + joinAnd(words)
		}
		out = append(out, fmt.Sprintf("%s's %s (%s)", name, cut, v))
	}
	sort.Strings(out)
	// Drafts waiting for a person: an approval would save what they hold.
	var drafts []models.Draft
	if err := a.DB.Find(ctx, store.CDrafts, a.WorkspaceID(), bson.M{"state": bson.M{"$in": bson.A{models.DraftWaiting, models.DraftApproving}},
		"input": bson.M{"$regex": regexp.QuoteMeta(sha)}}, &drafts); err != nil {
		return nil, err
	}
	for _, d := range drafts {
		out = append(out, fmt.Sprintf("the draft “%s” waiting for review", firstNonEmpty(d.Title, d.ID)))
	}
	// Recipes: their runs make cuts that play it.
	var rvs []models.RecipeVersion
	if err := a.DB.Find(ctx, store.CRecipeVersions, a.WorkspaceID(), bson.M{"doc.sections.clips.background.matte": sha}, &rvs); err != nil {
		return nil, err
	}
	for _, v := range rvs {
		var r models.Recipe
		_ = a.DB.GetByID(ctx, store.CRecipes, a.WorkspaceID(), v.RecipeID, &r)
		out = append(out, fmt.Sprintf("“%s” (version %d) in Recipes", firstNonEmpty(r.Title, r.Name, "one"), v.Number))
	}
	return out, nil
}

func playWord(n int) string {
	if n == 1 {
		return "plays"
	}
	return "play"
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}
