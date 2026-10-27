package ops

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// The Trash is two steps (D98). Moving a clip there is instant and can be
// undone (asset.trash, asset.restore). Deleting for good is for people only
// (trash.empty, and asset.delete for one clip), with a warning that says how
// much goes; it uses asset.delete's checks: refused while any cut (any
// version) uses a clip, stored bytes something else uses are kept, and
// everything derived from the clip goes with it. Nothing is emptied
// automatically unless a person turns on "Empty the Trash after N days".

// TrashListIn has no fields.
type TrashListIn struct{}

// TrashRow is a clip in the Trash and whether it can be deleted for good.
type TrashRow struct {
	LibraryItem
	CanDelete bool `json:"canDelete"`
	// Blockers say what keeps it (a cut uses it), with where (Uses).
	Blockers []string `json:"blockers"`
	Uses     []UseRow `json:"uses"`
}

// TrashListOut is everything in the Trash.
type TrashListOut struct {
	Items          []TrashRow `json:"items"`
	Count          int        `json:"count"`
	Size           int64      `json:"size"`
	EmptyAfterDays int        `json:"emptyAfterDays" jsonschema:"description=0: nothing is deleted until a person empties the Trash."`
	Summary        string     `json:"summary"`
}

// TrashEmptyIn deletes what is in the Trash for good (people only).
type TrashEmptyIn struct {
	// AssetIDs are the clips to delete (default: everything in the Trash).
	AssetIDs []string `json:"assetIds,omitempty" jsonschema:"description=Only these clips (IDs or titles; they must be in the Trash). Default: everything in it."`
	// DryRun only says what would go and what stays.
	DryRun bool `json:"dryRun,omitempty"`
}

// TrashEmptyItem is a clip that goes (or went).
type TrashEmptyItem struct {
	AssetID string `json:"assetId"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
	Size    int64  `json:"size"`
}

// TrashKeptItem is a clip that stays in the Trash, and why.
type TrashKeptItem struct {
	AssetID string   `json:"assetId"`
	Title   string   `json:"title"`
	Reason  string   `json:"reason"`
	Uses    []UseRow `json:"uses"`
}

// TrashEmptyOut says what was (or would be) deleted for good.
type TrashEmptyOut struct {
	Items []TrashEmptyItem `json:"items" jsonschema:"description=What goes (dry run) or went."`
	Kept  []TrashKeptItem  `json:"kept"`
	Count int              `json:"count"`
	Size  int64            `json:"size" jsonschema:"description=Bytes of the clips themselves (their previews go too)."`
	// Question is the warning a person confirms: "Permanently delete 3 videos (1.2 GB)?"
	Question string `json:"question"`
	Deleted  bool   `json:"deleted"`
	Message  string `json:"message"`
	// Removed counts what went with them (transcripts, voices, analysis,
	// questions, stored files…); KeptFiles are stored files something else uses.
	Removed   map[string]int `json:"removed,omitempty"`
	KeptFiles int            `json:"keptFiles,omitempty"`
}

// TrashSettingsIn has no fields.
type TrashSettingsIn struct{}

// TrashSettingsOut is "Empty the Trash after N days".
type TrashSettingsOut struct {
	EmptyAfterDays int        `json:"emptyAfterDays" jsonschema:"description=0: off (the default): nothing is deleted until a person empties the Trash."`
	UpdatedAt      *time.Time `json:"updatedAt,omitempty"`
	UpdatedBy      string     `json:"updatedBy,omitempty"`
	Summary        string     `json:"summary"`
}

// TrashConfigureIn turns "Empty the Trash after N days" on or off (people only).
type TrashConfigureIn struct {
	EmptyAfterDays int `json:"emptyAfterDays" jsonschema:"minimum=0,maximum=3650,description=Days a clip stays in the Trash before it is deleted for good; 0 turns it off."`
}

// trashDays is the workspace's "Empty the Trash after N days" (0: off).
func trashDays(ctx context.Context, a *app.App) int {
	var ws models.Workspace
	if a.DB.GetByID(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), &ws) != nil || ws.Trash == nil {
		return 0
	}
	return ws.Trash.EmptyAfterDays
}

func trashSettingsOut(ctx context.Context, a *app.App) *TrashSettingsOut {
	var ws models.Workspace
	out := &TrashSettingsOut{}
	if a.DB.GetByID(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), &ws) == nil && ws.Trash != nil {
		out.EmptyAfterDays = ws.Trash.EmptyAfterDays
		if !ws.Trash.UpdatedAt.IsZero() {
			t := ws.Trash.UpdatedAt
			out.UpdatedAt = &t
			out.UpdatedBy = firstNonBlank(ws.Trash.UpdatedBy.UserName, ws.Trash.UpdatedBy.UserID)
		}
	}
	if out.EmptyAfterDays > 0 {
		out.Summary = fmt.Sprintf("Clips are deleted for good %s after they are moved to the Trash.", plural(out.EmptyAfterDays, "day"))
	} else {
		out.Summary = "Nothing in the Trash is deleted until you empty it."
	}
	return out
}

// kindWord names what is being deleted: videos, recordings or clips.
func kindWord(kinds []string, n int) string {
	word := "clip"
	all := func(k string) bool {
		for _, x := range kinds {
			if x != k {
				return false
			}
		}
		return len(kinds) > 0
	}
	switch {
	case all("video"):
		word = "video"
	case all("audio"):
		word = "recording"
	}
	return plural(n, word)
}

// emptyTrash deletes clips in the Trash for good (or, dry, says what would
// go): those no cut uses; the rest stay.
func emptyTrash(ctx context.Context, a *app.App, in *TrashEmptyIn) (*TrashEmptyOut, error) {
	out := &TrashEmptyOut{Items: []TrashEmptyItem{}, Kept: []TrashKeptItem{}}
	var list []models.Asset
	if refs := uniqueStrings(in.AssetIDs); len(refs) > 0 {
		for _, ref := range refs {
			as, err := resolveFootage(ctx, a, ref, bson.M{"trash": bson.M{"$exists": true}})
			if err != nil {
				return nil, err
			}
			if as.Trash == nil {
				out.Kept = append(out.Kept, TrashKeptItem{AssetID: as.ID, Title: as.DisplayTitle(), Reason: "It isn't in the Trash.", Uses: []UseRow{}})
				continue
			}
			list = append(list, *as)
		}
	} else if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"trash": bson.M{"$exists": true}}, &list, options.Find().SetSort(bson.D{{Key: "trash.at", Value: 1}})); err != nil {
		return nil, err
	}
	var kinds []string
	var allowed []models.Asset
	for i := range list {
		as := &list[i]
		blockers, uses, err := deleteCheck(ctx, a, as, "")
		if err != nil {
			return nil, err
		}
		if len(blockers) > 0 {
			out.Kept = append(out.Kept, TrashKeptItem{AssetID: as.ID, Title: as.DisplayTitle(), Reason: blockers[0], Uses: uses})
			continue
		}
		allowed = append(allowed, *as)
		kinds = append(kinds, as.Kind)
		out.Items = append(out.Items, TrashEmptyItem{AssetID: as.ID, Title: as.DisplayTitle(), Kind: as.Kind, Size: as.Size})
		out.Size += as.Size
	}
	out.Count = len(out.Items)
	what := kindWord(kinds, out.Count)
	if out.Count == 1 {
		what = "“" + out.Items[0].Title + "”"
	}
	keptText := ""
	if n := len(out.Kept); n == 1 {
		keptText = fmt.Sprintf(" “%s” stays in the Trash: %s", out.Kept[0].Title, out.Kept[0].Reason)
	} else if n > 1 {
		keptText = fmt.Sprintf(" %d clips stay in the Trash because they're still in use.", n)
	}
	if out.Count == 0 {
		out.Question = ""
		if len(list) == 0 && len(out.Kept) == 0 {
			out.Message = "The Trash is empty."
		} else {
			out.Message = "Nothing can be deleted." + keptText
		}
		if in.DryRun || len(out.Kept) == 0 {
			return out, nil
		}
		return nil, apperr.New(apperr.Conflict, "%s", out.Message).WithDetails(map[string]any{"kept": out.Kept})
	}
	out.Question = fmt.Sprintf("Permanently delete %s (%s)?", what, sizeText(out.Size))
	if in.DryRun {
		out.Message = out.Question + " Their previews, transcripts and speaker data go too, for every machine. This can't be undone." + keptText
		return out, nil
	}
	out.Removed = map[string]int{}
	done := []TrashEmptyItem{}
	var freed int64
	for i := range allowed {
		as := &allowed[i]
		// Still in the Trash, and still unused, right now.
		var cur models.Asset
		if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), as.ID, &cur) != nil || cur.Trash == nil {
			continue
		}
		if blockers, uses, err := deleteCheck(ctx, a, &cur, ""); err != nil || len(blockers) > 0 {
			if err == nil {
				out.Kept = append(out.Kept, TrashKeptItem{AssetID: cur.ID, Title: cur.DisplayTitle(), Reason: blockers[0], Uses: uses})
			}
			continue
		}
		removed, kept, err := deleteAsset(ctx, a, &cur)
		if err != nil {
			return nil, err
		}
		for k, v := range removed {
			out.Removed[k] += v
		}
		out.KeptFiles += kept
		freed += cur.Size
		done = append(done, TrashEmptyItem{AssetID: cur.ID, Title: cur.DisplayTitle(), Kind: cur.Kind, Size: cur.Size})
	}
	out.Items, out.Count, out.Size, out.Deleted = done, len(done), freed, len(done) > 0
	what = kindWord(kindsOf(done), len(done))
	if len(done) == 1 {
		what = "“" + done[0].Title + "”"
	}
	out.Message = fmt.Sprintf("Deleted %s for good (%s).", what, sizeText(freed)) + keptFilesText(out.KeptFiles) + keptText
	a.Audit(ctx, "trash.empty", "workspace", a.WorkspaceID(), "", map[string]any{"deleted": len(done), "bytes": freed, "kept": len(out.Kept)})
	return out, nil
}

func kindsOf(items []TrashEmptyItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Kind)
	}
	return out
}

func init() {
	Def("trash.list", "List what is in the Trash: when each clip went there, from where, whether it can be deleted for good, and the total size", Flags{}, func(ctx context.Context, a *app.App, _ *TrashListIn) (*TrashListOut, error) {
		var list []models.Asset
		if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"trash": bson.M{"$exists": true}}, &list, options.Find().SetSort(bson.D{{Key: "trash.at", Value: -1}}).SetProjection(bson.M{"media.streams": 0})); err != nil {
			return nil, err
		}
		days := trashDays(ctx, a)
		names := loadLibraryNames(ctx, a, list)
		out := &TrashListOut{Items: []TrashRow{}, EmptyAfterDays: days}
		for i := range list {
			as := &list[i]
			blockers, uses, err := deleteCheck(ctx, a, as, "")
			if err != nil {
				return nil, err
			}
			out.Items = append(out.Items, TrashRow{LibraryItem: libraryItem(as, names, days), CanDelete: len(blockers) == 0, Blockers: blockers, Uses: uses})
			out.Size += as.Size
		}
		out.Count = len(out.Items)
		switch {
		case out.Count == 0:
			out.Summary = "The Trash is empty."
		case days > 0:
			out.Summary = fmt.Sprintf("%s in the Trash (%s). Each is deleted for good %s after it was moved there.", plural(out.Count, "clip"), sizeText(out.Size), plural(days, "day"))
		default:
			out.Summary = fmt.Sprintf("%s in the Trash (%s). Nothing here is deleted until you empty the Trash.", plural(out.Count, "clip"), sizeText(out.Size))
		}
		return out, nil
	})
	Def("trash.empty", "Delete clips in the Trash for good (all of it, or the ones named), with their previews, transcripts and speaker data, on every machine; clips a cut uses stay; people only, owners", Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, emptyTrash)
	Def("trash.settings", "Whether the Trash empties itself after a number of days (off unless a person turns it on)", Flags{}, func(ctx context.Context, a *app.App, _ *TrashSettingsIn) (*TrashSettingsOut, error) {
		return trashSettingsOut(ctx, a), nil
	})
	Def("trash.configure", "Turn “Empty the Trash after N days” on (a person decides) or off (0); owners", Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, func(ctx context.Context, a *app.App, in *TrashConfigureIn) (*TrashSettingsOut, error) {
		if in.EmptyAfterDays < 0 || in.EmptyAfterDays > 3650 {
			return nil, invalid("days must be between 1 and 3650 (0 turns it off)")
		}
		set := models.TrashSettings{EmptyAfterDays: in.EmptyAfterDays, UpdatedAt: time.Now().UTC(), UpdatedBy: a.Actor}
		if err := a.DB.Update(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), bson.M{"$set": bson.M{"trash": set}}); err != nil {
			return nil, err
		}
		a.Audit(ctx, "trash.configure", "workspace", a.WorkspaceID(), "", map[string]any{"emptyAfterDays": in.EmptyAfterDays})
		return trashSettingsOut(ctx, a), nil
	})
}

// EmptyExpiredTrash deletes, for good, the clips that have been in the
// Trash longer than "Empty the Trash after N days" allows, when a person
// turned it on (never in a restored copy whose deletions are paused); clips a
// cut uses stay. Each clip is claimed first, so two
// machines never delete the same one. It returns how many went.
func EmptyExpiredTrash(ctx context.Context, a *app.App, now time.Time) (int, error) {
	days := trashDays(ctx, a)
	if days <= 0 {
		return 0, nil
	}
	// A restored copy deletes nothing by itself until an owner adopts it: its
	// original may still use the same footage.
	if a.DB.DeletionsPaused(ctx, a.WorkspaceID()) {
		return 0, nil
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	var list []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"trash.at": bson.M{"$lt": cutoff}}, &list, options.Find().SetProjection(bson.M{"_id": 1}).SetLimit(500)); err != nil {
		return 0, err
	}
	n := 0
	for _, x := range list {
		claim := models.TrashClaim{By: a.Cfg.Installation.ID, At: now}
		res, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": x.ID, "workspaceId": a.WorkspaceID(), "trash.at": bson.M{"$lt": cutoff},
			"$or": bson.A{bson.M{"trash.claim": bson.M{"$exists": false}}, bson.M{"trash.claim.at": bson.M{"$lt": now.Add(-time.Hour)}}}},
			bson.M{"$set": bson.M{"trash.claim": claim}})
		if err != nil {
			return n, err
		}
		if res.ModifiedCount == 0 {
			continue // another machine has it, or it was restored
		}
		var as models.Asset
		if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), x.ID, &as) != nil || as.Trash == nil {
			continue
		}
		blockers, _, err := deleteCheck(ctx, a, &as, "")
		if err != nil || len(blockers) > 0 {
			// A cut uses it: it stays in the Trash (a person sees why there).
			_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$unset": bson.M{"trash.claim": ""}})
			continue
		}
		if _, _, err := deleteAsset(ctx, a, &as); err != nil {
			return n, err
		}
		n++
	}
	if n > 0 {
		a.Audit(ctx, "trash.autoEmpty", "workspace", a.WorkspaceID(), "", map[string]any{"deleted": n, "afterDays": days})
	}
	return n, nil
}

// RunTrashSchedule empties what has been in the Trash too long, once an hour
// while the daemon runs (only when a person turned that on).
func RunTrashSchedule(ctx context.Context, a *app.App, logf func(string, ...any)) {
	t := time.NewTimer(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if n, err := EmptyExpiredTrash(ctx, a, time.Now().UTC()); err != nil && ctx.Err() == nil {
			logf("emptying the Trash failed: %v", err)
		} else if n > 0 {
			logf("deleted %s from the Trash for good (older than the workspace's setting)", plural(n, "clip"))
		}
		t.Reset(time.Hour)
	}
}
