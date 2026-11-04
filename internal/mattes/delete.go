package mattes

import (
	"context"
	"fmt"
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
	DryRun  bool `json:"dryRun,omitempty" jsonschema:"description=Only say whether it can be deleted and which versions play it."`
}

// CutoutDeleteOut says what was (or would be) deleted.
type CutoutDeleteOut struct {
	Allowed bool       `json:"allowed"`
	Deleted bool       `json:"deleted"`
	Cutout  CutoutView `json:"cutout"`
	// Blockers are the cuts whose latest version plays it, in plain words:
	// the background comes out of those clips first.
	Blockers []string `json:"blockers"`
	// Older are older versions that play it: after it is deleted they can't
	// be rendered with their new background again.
	Older   []string `json:"older"`
	Summary string   `json:"summary"`
}

// Delete deletes a cut-out for good (people only): refused while the latest
// version of a cut plays it, itself or in a cut it nests; older versions
// that play it are named, since they can't show it again.
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
	out := &CutoutDeleteOut{Cutout: ViewOf(m, clip, dur, nil), Blockers: []string{}, Older: []string{}}
	if m.File != nil {
		if out.Blockers, out.Older, err = playedBy(ctx, a, m.File.SHA256); err != nil {
			return nil, err
		}
	}
	out.Allowed = len(out.Blockers) == 0
	what := fmt.Sprintf("the cut-out of “%s” (%s)", clip, out.Cutout.Part)
	older := ""
	if len(out.Older) > 0 {
		older = fmt.Sprintf(" Older versions that play it can't show their new background again once it is gone: %s.", joinAnd(out.Older))
	}
	if !out.Allowed {
		out.Summary = fmt.Sprintf("%s can't be deleted: %s %s it. Take the background out of those clips first.", upperFirst(what), joinAnd(out.Blockers), playWord(len(out.Blockers)))
		if in.DryRun {
			return out, nil
		}
		return nil, apperr.New(apperr.Conflict, "%s", out.Summary).WithDetails(map[string]any{"blockers": out.Blockers, "older": out.Older, "matteId": m.ID})
	}
	if in.DryRun {
		out.Summary = fmt.Sprintf("%s can be deleted: its file goes for every computer of this workspace, and it can be made again.%s", upperFirst(what), older)
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
		"model": m.Provider + "/" + m.Model, "older": len(out.Older)})
	out.Deleted = true
	out.Summary = fmt.Sprintf("Deleted %s.%s", what, older)
	return out, nil
}

// playedBy names the cuts whose versions play a cut-out file — pinned in a
// clip's background, or in a cut they nest (D84), at any depth: the latest
// versions (of projects not archived) and the older ones.
func playedBy(ctx context.Context, a *app.App, sha string) (latest, older []string, err error) {
	proj := options.Find().SetProjection(bson.M{"_id": 1, "projectId": 1, "branch": 1, "number": 1})
	var playing []models.Revision
	if err := a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), bson.M{"spec.clips.background.matte": sha, "detached": bson.M{"$ne": true}}, &playing, proj); err != nil {
		return nil, nil, err
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
			return nil, nil, err
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
	projects := map[string]*models.Project{}
	for _, r := range playing {
		p, ok := projects[r.ProjectID]
		if !ok {
			var pp models.Project
			if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), r.ProjectID, &pp) == nil {
				p = &pp
			}
			projects[r.ProjectID] = p
		}
		if p == nil {
			continue
		}
		cut := "cut"
		if r.Branch != "" && r.Branch != "main" {
			cut = r.Branch + " cut"
		}
		words := fmt.Sprintf("“%s”'s %s (version %d)", p.Name, cut, r.Number)
		if !p.Archived && p.Branches[r.Branch].Head == r.ID {
			latest = append(latest, words)
		} else {
			older = append(older, words)
		}
	}
	sort.Strings(latest)
	sort.Strings(older)
	return latest, older, nil
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
