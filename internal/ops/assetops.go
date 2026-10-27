package ops

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
)

// Moving footage between projects, where it is used, and deleting it for
// good. Removing footage from a project never deletes it: it stays in the
// workspace (in the Inbox when no project has it). Deleting is separate,
// people only, refused while anything uses the footage, and it only removes
// stored files nothing else references.

// AssetRefIn names footage: its ID, or its title or file name within a project.
type AssetRefIn struct {
	AssetID   string `json:"assetId" jsonschema:"minLength=1,description=The footage's ID (or its title or file name\\, with projectId)."`
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=Where to look it up by name (ID or name)."`
}

// AssetWhereOut lists every place footage is used.
type AssetWhereOut struct {
	Asset   AssetSummary `json:"asset"`
	Uses    []UseRow     `json:"uses"`
	Summary string       `json:"summary"`
}

// AssetMoveIn moves footage from one project to another.
type AssetMoveIn struct {
	AssetID string `json:"assetId" jsonschema:"minLength=1"`
	From    string `json:"from,omitempty" jsonschema:"description=The project it leaves (ID or name); empty: the Inbox."`
	To      string `json:"to" jsonschema:"minLength=1,description=The project it goes to (ID or name)."`
}

// AssetMoveOut is the footage after moving.
type AssetMoveOut struct {
	Asset   AssetSummary `json:"asset"`
	Message string       `json:"message"`
}

// AssetDeleteIn deletes footage for good.
type AssetDeleteIn struct {
	AssetID string `json:"assetId" jsonschema:"minLength=1"`
	// ProjectID is the project it is being deleted from (it may be in no other).
	ProjectID string `json:"projectId,omitempty"`
	// Forever must be true: deleting can't be undone.
	Forever bool `json:"forever,omitempty" jsonschema:"description=Must be true: the footage\\, its preview\\, transcript and speaker data are removed for every machine."`
	DryRun  bool `json:"dryRun,omitempty" jsonschema:"description=Only say whether it can be deleted (and what stops it)."`
}

// AssetDeleteOut says what was (or would be) deleted.
type AssetDeleteOut struct {
	Allowed bool `json:"allowed"`
	Deleted bool `json:"deleted"`
	// Blockers say what stops the delete, in plain words, with where (Uses).
	Blockers []string `json:"blockers"`
	Uses     []UseRow `json:"uses"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	// Removed counts what went: transcript segments, voices, analysis,
	// questions, files in the media store and local copies; Kept are stored
	// files other footage or resources still use.
	Removed map[string]int `json:"removed,omitempty"`
	Kept    int            `json:"kept,omitempty"`
}

// DeleteHook removes what another part of MetaRush keeps about an asset
// (the visual index, for example) when the asset is deleted. Builds register
// theirs with RegisterDeleteHook; each returns how many records it removed.
type DeleteHook func(ctx context.Context, a *app.App, assetID string) (int, error)

var (
	deleteHooksMu sync.Mutex
	deleteHooks   = map[string]DeleteHook{}
)

// RegisterDeleteHook adds cleanup for asset deletion (name: what it removes).
func RegisterDeleteHook(name string, h DeleteHook) {
	deleteHooksMu.Lock()
	defer deleteHooksMu.Unlock()
	deleteHooks[name] = h
}

// findAsset resolves footage by ID, or by title or file name in a project.
func findAsset(ctx context.Context, a *app.App, ref, projectRef string) (*models.Asset, error) {
	ref = strings.TrimSpace(ref)
	var as models.Asset
	if ids.Valid(ids.Asset, ref) {
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), ref, &as); err != nil {
			return nil, notFound("asset", ref)
		}
		return &as, nil
	}
	filter := bson.M{}
	if projectRef != "" {
		p, err := resolveProject(ctx, a, projectRef)
		if err != nil {
			return nil, err
		}
		filter["projectIds"] = p.ID
	}
	var list []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), filter, &list); err != nil {
		return nil, err
	}
	var hits []models.Asset
	for _, x := range list {
		if strings.EqualFold(x.DisplayTitle(), ref) || strings.EqualFold(x.Name, ref) || x.HasName(ref) {
			hits = append(hits, x)
		}
	}
	switch len(hits) {
	case 1:
		return &hits[0], nil
	case 0:
		return nil, apperr.New(apperr.NotFound, "There's no footage called %q.", ref).
			WithHint("Check the name in the Library (titles and file names both work).").WithCommand("metarush asset list")
	}
	return nil, apperr.New(apperr.InvalidInput, "%d clips are called %q.", len(hits), ref).
		WithHint("Say which project it's in too, or give one of them another title in the Library.").WithCommand("metarush asset list")
}

// currentCutUses reports the project's current cut (any branch head) using the footage.
func currentCutUses(ctx context.Context, a *app.App, p *models.Project, assetID string) (string, bool) {
	for branch, h := range p.Branches {
		var r models.Revision
		if h.Head == "" || a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), h.Head, &r) != nil {
			continue
		}
		if slices.Contains(specAssetIDs(&r.Spec), assetID) {
			cut := "cut"
			if branch != "main" {
				cut = branch + " cut"
			}
			return fmt.Sprintf("%s's %s (version %d) uses it", p.Name, cut, r.Number), true
		}
	}
	return "", false
}

func projectNames(ctx context.Context, a *app.App, ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	var ps []models.Project
	_ = a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}}, &ps)
	var out []string
	for _, p := range ps {
		out = append(out, p.Name)
	}
	sort.Strings(out)
	return out
}

func init() {
	Def("asset.where", "List every place footage is used: the projects it is in, projects that see it through a link, and the cuts (and their older versions) that play it", Flags{}, func(ctx context.Context, a *app.App, in *AssetRefIn) (*AssetWhereOut, error) {
		as, err := findAsset(ctx, a, in.AssetID, in.ProjectID)
		if err != nil {
			return nil, err
		}
		uses, err := assetUses(ctx, a, as)
		if err != nil {
			return nil, err
		}
		if uses == nil {
			uses = []UseRow{}
		}
		summary := "In the Inbox, not in any project or cut."
		if len(uses) > 0 {
			var projects, cuts int
			var filed []string
			for _, u := range uses {
				switch u.Kind {
				case UseCut:
					cuts++
				case UseProject:
					projects++
				case UseCollection:
					filed = append(filed, "on the "+u.ProjectName+" collection")
				case UseArchive:
					filed = append(filed, "in the Archive")
				}
			}
			summary = fmt.Sprintf("In %s; used in %s.", plural(projects, "project"), plural(cuts, "cut"))
			switch {
			case as.Trash != nil:
				summary = fmt.Sprintf("In the Trash; used in %s.", plural(cuts, "cut"))
			case projects == 0 && len(filed) > 0:
				summary = fmt.Sprintf("Not in a project (filed %s); used in %s.", joinAnd(filed), plural(cuts, "cut"))
			case projects == 0:
				summary = fmt.Sprintf("Not in a project (the Inbox); used in %s.", plural(cuts, "cut"))
			case len(filed) > 0:
				summary = fmt.Sprintf("In %s, also filed %s; used in %s.", plural(projects, "project"), joinAnd(filed), plural(cuts, "cut"))
			}
		}
		return &AssetWhereOut{Asset: summarize(*as), Uses: uses, Summary: summary}, nil
	})
	Def("asset.move", "Move footage from one project (or the Inbox) to another; the footage itself doesn't change", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *AssetMoveIn) (*AssetMoveOut, error) {
		as, err := findAsset(ctx, a, in.AssetID, in.From)
		if err != nil {
			return nil, err
		}
		if err := refuseTrashed(as); err != nil {
			return nil, err
		}
		to, err := resolveProject(ctx, a, in.To)
		if err != nil {
			return nil, err
		}
		var from *models.Project
		if strings.TrimSpace(in.From) != "" {
			if from, err = resolveProject(ctx, a, in.From); err != nil {
				return nil, err
			}
			if from.ID == to.ID {
				return nil, apperr.New(apperr.InvalidInput, "%s is already in %s.", as.DisplayTitle(), to.Name)
			}
			if !slices.Contains(as.ProjectIDs, from.ID) {
				return nil, apperr.New(apperr.InvalidInput, "%s isn't in %s.", as.DisplayTitle(), from.Name)
			}
			if why, used := currentCutUses(ctx, a, from, as.ID); used {
				return nil, apperr.New(apperr.Conflict, "%s can't leave %s: %s.", as.DisplayTitle(), from.Name, why).
					WithHint("Take it out of that cut first, or add it to " + to.Name + " too (it can be in both).")
			}
		}
		fromID := ""
		if from != nil {
			fromID = from.ID
		}
		if err := carryAsset(ctx, a, as, fromID, to.ID); err != nil { // What may leave (D289)
			return nil, err
		}
		now := time.Now().UTC()
		if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$addToSet": bson.M{"projectIds": to.ID}, "$set": bson.M{"updatedAt": now}}); err != nil {
			return nil, err
		}
		if from != nil {
			if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$pull": bson.M{"projectIds": from.ID}}); err != nil {
				return nil, err
			}
			refreshProjectCounts(ctx, a, from.ID)
		}
		refreshProjectCounts(ctx, a, to.ID)
		refreshAssetTodos(ctx, a, as.ID)
		_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), as.ID, as)
		where := "the Inbox"
		if from != nil {
			where = from.Name
		}
		a.Audit(ctx, "asset.move", "asset", as.ID, to.ID, map[string]any{"from": where, "to": to.ID})
		return &AssetMoveOut{Asset: summarize(*as), Message: fmt.Sprintf("Moved “%s” from %s to %s.", as.DisplayTitle(), where, to.Name)}, nil
	})
	Def("asset.delete", "Delete footage for good — the file, its preview, transcript and speaker data, on every machine — when no project but this one has it and no cut (no version of any cut) uses it; people only", Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, func(ctx context.Context, a *app.App, in *AssetDeleteIn) (*AssetDeleteOut, error) {
		as, err := findAsset(ctx, a, in.AssetID, "")
		if err != nil {
			return nil, err
		}
		out := &AssetDeleteOut{Title: as.DisplayTitle(), Blockers: []string{}, Uses: []UseRow{}}
		var here string
		if in.ProjectID != "" {
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			here = p.ID
		}
		blockers, uses, err := deleteCheck(ctx, a, as, here)
		if err != nil {
			return nil, err
		}
		out.Blockers, out.Uses = append(out.Blockers, blockers...), append(out.Uses, uses...)
		out.Allowed = len(out.Blockers) == 0
		if !out.Allowed {
			out.Message = fmt.Sprintf("“%s” can't be deleted: %s", as.DisplayTitle(), out.Blockers[0])
			if in.DryRun {
				return out, nil
			}
			return nil, apperr.New(apperr.Conflict, "%s", out.Message).WithDetails(map[string]any{"blockers": out.Blockers, "uses": out.Uses})
		}
		if in.DryRun {
			out.Message = fmt.Sprintf("“%s” can be deleted. That removes the video, its preview, transcript and speaker data from storage for every machine, and can't be undone.", as.DisplayTitle())
			return out, nil
		}
		if !in.Forever {
			return nil, apperr.New(apperr.ConfirmRequired, "Deleting “%s” can't be undone.", as.DisplayTitle()).
				WithHint("Confirm that you want it deleted for good.").WithCommand("metarush asset delete " + as.ID + " --forever")
		}
		removed, kept, err := deleteAsset(ctx, a, as)
		if err != nil {
			return nil, err
		}
		out.Deleted, out.Removed, out.Kept = true, removed, kept
		out.Message = fmt.Sprintf("Deleted “%s” for good.", as.DisplayTitle()) + keptFilesText(kept)
		return out, nil
	})
}

// keptFilesText says that stored files other things still use were kept
// ("" when none was): " 1 file stays in storage because …".
func keptFilesText(n int) string {
	switch {
	case n <= 0:
		return ""
	case n == 1:
		return " 1 file stays in storage because other footage or resources use it too."
	}
	return fmt.Sprintf(" %d files stay in storage because other footage or resources use them too.", n)
}

// deleteCheck says what stops footage from being deleted for good (D83; the
// Trash uses the same check, D98): another project has it (here is the one
// it is deleted from), or a cut, any version of it, uses it. Collections and
// the Archive never stop it: they go with it.
func deleteCheck(ctx context.Context, a *app.App, as *models.Asset, here string) ([]string, []UseRow, error) {
	blockers, uses := []string{}, []UseRow{}
	var others []string
	for _, pid := range as.ProjectIDs {
		if pid != here {
			others = append(others, pid)
		}
	}
	if n := projectNames(ctx, a, others); len(n) > 0 {
		blockers = append(blockers, "Also in "+joinAnd(n)+": remove it from there first, or keep it.")
	}
	all, err := assetUses(ctx, a, as)
	if err != nil {
		return nil, nil, err
	}
	for _, u := range all {
		if u.Kind == UseCut {
			uses = append(uses, u)
			blockers = append(blockers, u.Text+": remove it from the cut first.")
		}
	}
	return blockers, uses, nil
}

// deleteAsset removes an asset and everything derived from it, then the
// stored files nothing else references. The caller checked nothing uses it.
func deleteAsset(ctx context.Context, a *app.App, as *models.Asset) (map[string]int, int, error) {
	ws := a.WorkspaceID()
	removed := map[string]int{}
	del := func(coll, label string) {
		res, err := a.DB.C(coll).DeleteMany(ctx, bson.M{"workspaceId": ws, "assetId": as.ID})
		if err == nil && res.DeletedCount > 0 {
			removed[label] += int(res.DeletedCount)
		}
	}
	// People whose voices were heard here: their profiles are rebuilt without it.
	var voices []models.AssetSpeaker
	_ = a.DB.Find(ctx, store.CSpeakers, ws, bson.M{"assetId": as.ID}, &voices)
	var people []string
	for _, v := range voices {
		if v.PersonID != "" && !slices.Contains(people, v.PersonID) {
			people = append(people, v.PersonID)
		}
	}
	del(store.CSegments, "transcript segments")
	del(store.CSpeakers, "voices")
	del(store.CInsights, "analysis") // what's on screen and scene descriptions are insights too
	del(store.CFaces, "faces")
	del(store.CTodos, "questions")
	del(store.CVoiceTakes, "narration takes")
	del(store.CIngest, "upload records")
	del(store.CNotifications, "notifications")
	deleteHooksMu.Lock()
	hooks := make(map[string]DeleteHook, len(deleteHooks))
	for k, h := range deleteHooks {
		hooks[k] = h
	}
	deleteHooksMu.Unlock()
	for name, h := range hooks {
		if n, err := h(ctx, a, as.ID); err == nil && n > 0 {
			removed[name] += n
		}
	}
	// Work still waiting for it is canceled (it would fail).
	_, _ = a.DB.C(store.CJobs).UpdateMany(ctx, bson.M{"workspaceId": ws, "assetId": as.ID, "state": models.JobQueued},
		bson.M{"$set": bson.M{"state": models.JobCanceled, "error": "the footage was deleted", "updatedAt": time.Now().UTC()}})
	// Stored files: the original and its variants, unless something else uses the bytes.
	type obj struct{ key, sha string }
	objs := []obj{{as.StorageKey, as.SHA256}}
	for _, v := range as.Variants {
		objs = append(objs, obj{v.StorageKey, v.SHA256})
	}
	kept := 0
	for _, o := range objs {
		if o.key == "" || o.sha == "" {
			continue
		}
		if sharedBytes(ctx, a, as.ID, o.sha) {
			kept++
			continue
		}
		if err := a.Media.Delete(ctx, o.key); err == nil {
			removed["stored files"]++
		}
		if ok, _ := a.Cache.Remove(o.sha); ok {
			removed["local copies"]++
		}
	}
	if _, err := a.DB.C(store.CAssets).DeleteOne(ctx, bson.M{"_id": as.ID, "workspaceId": ws}); err != nil {
		return removed, kept, err
	}
	for _, pid := range as.ProjectIDs {
		refreshProjectCounts(ctx, a, pid)
	}
	for _, pid := range people {
		_, _ = speakers.RebuildProfile(ctx, a, pid)
	}
	a.Audit(ctx, "asset.delete", "asset", as.ID, firstOr(as.ProjectIDs), map[string]any{"name": as.Name, "title": as.DisplayTitle(), "sha256": as.SHA256, "removed": removed, "kept": kept})
	return removed, kept, nil
}

// sharedBytes reports whether anything but this asset uses a stored file:
// another asset (or its variants) or a resource (a version, page picture or
// thumbnail).
func sharedBytes(ctx context.Context, a *app.App, assetID, sha string) bool {
	ws := a.WorkspaceID()
	or := bson.A{bson.M{"sha256": sha}}
	for _, k := range []string{models.VariantProxy, models.VariantThumbnail, models.VariantContactSheet, models.VariantWaveform, models.VariantPeaks, models.VariantMezzanine} {
		or = append(or, bson.M{"variants." + k + ".sha256": sha})
	}
	if n, err := a.DB.C(store.CAssets).CountDocuments(ctx, bson.M{"workspaceId": ws, "_id": bson.M{"$ne": assetID}, "$or": or}); err != nil || n > 0 {
		return true // when in doubt, keep it
	}
	key := media.ObjectKey(ws, sha)
	// Everything else that keeps files in the store by their hash: resources,
	// renders (a render someone also imported as footage), QA frames and
	// motion graphics' review pictures.
	for _, q := range []struct {
		coll string
		or   bson.A
	}{
		{store.CResources, bson.A{bson.M{"versions.sha256": sha}, bson.M{"versions.pages.sha256": sha}, bson.M{"versions.thumbnail.sha256": sha}}},
		{store.CRenders, bson.A{bson.M{"output.sha256": sha}, bson.M{"output.storageKey": key}, bson.M{"output.thumbnailKey": key}}},
		{store.CQAReports, bson.A{bson.M{"renderSha256": sha}, bson.M{"frames.sha256": sha}, bson.M{"contactSheet.sha256": sha}, bson.M{"momentsSheet.sha256": sha}}},
		{store.CMotion, bson.A{bson.M{"sheet.sha256": sha}, bson.M{"video.sha256": sha}, bson.M{"frames.object.sha256": sha}}},
	} {
		n, err := a.DB.C(q.coll).CountDocuments(ctx, bson.M{"workspaceId": ws, "$or": q.or})
		if err != nil || n > 0 {
			return true // when in doubt, keep it
		}
	}
	return false
}
