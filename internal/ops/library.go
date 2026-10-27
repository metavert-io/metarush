package ops

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// The Library (D96–D98). The Inbox is a triage pile: footage in no project,
// on no collection, not archived and not in the Trash. Each clip leaves it
// by being added to a project, put on a collection, archived or moved to the
// Trash; library.list shows every view with counts, and its search covers
// them all, so nothing filed away is hard to find.

// Library views.
const (
	ViewInbox       = "inbox"
	ViewCollections = "collections"
	ViewArchived    = "archived"
	ViewProjects    = "projects"
	ViewTrash       = "trash"
	ViewAll         = "all"
)

// InboxFilter matches footage in the Inbox (D97). Clearer copies MetaRush
// made of a recording (D88) are never footage, wherever they are.
func InboxFilter() bson.M {
	return bson.M{"projectIds.0": bson.M{"$exists": false}, "collectionIds.0": bson.M{"$exists": false},
		"archivedAt": bson.M{"$exists": false}, "trash": bson.M{"$exists": false}, "tags": bson.M{"$ne": models.TagEnhanced}}
}

// viewFilter matches the footage of one Library view (a collection's own
// items when collectionID is set).
func viewFilter(view, collectionID string) bson.M {
	f := bson.M{}
	switch view {
	case ViewCollections:
		if collectionID != "" {
			f["collectionIds"] = collectionID
		} else {
			f["collectionIds.0"] = bson.M{"$exists": true}
		}
	case ViewArchived:
		f["archivedAt"] = bson.M{"$exists": true}
	case ViewProjects:
		f["projectIds.0"] = bson.M{"$exists": true}
	case ViewTrash:
		f["trash"] = bson.M{"$exists": true}
	case ViewAll:
	default:
		f = InboxFilter()
	}
	f["kind"] = bson.M{"$ne": "image"}            // pictures are resources (D79)
	f["tags"] = bson.M{"$ne": models.TagEnhanced} // clearer copies of recordings are not footage (D88)
	return f
}

// libraryNames are the projects' and collections' names a list of clips needs.
type libraryNames struct {
	projects    map[string]string
	collections map[string]string
}

func loadLibraryNames(ctx context.Context, a *app.App, assets []models.Asset) libraryNames {
	n := libraryNames{projects: map[string]string{}, collections: map[string]string{}}
	pset, cset := map[string]bool{}, map[string]bool{}
	for i := range assets {
		as := &assets[i]
		for _, p := range as.ProjectIDs {
			pset[p] = true
		}
		for _, c := range as.CollectionIDs {
			cset[c] = true
		}
		if as.Trash != nil {
			for _, p := range as.Trash.ProjectIDs {
				pset[p] = true
			}
			for _, c := range as.Trash.CollectionIDs {
				cset[c] = true
			}
		}
	}
	if len(pset) > 0 {
		var ps []models.Project
		_ = a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": keys(pset)}}, &ps, options.Find().SetProjection(bson.M{"name": 1, "archived": 1}))
		for _, p := range ps {
			n.projects[p.ID] = p.Name
			if p.Archived {
				n.projects[p.ID] = p.Name + " (archived)" // deleted: in Archived projects (D196)
			}
		}
	}
	if len(cset) > 0 {
		var cs []models.Collection
		_ = a.DB.Find(ctx, store.CCollections, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": keys(cset)}}, &cs)
		for _, c := range cs {
			n.collections[c.ID] = c.Name
		}
	}
	return n
}

func (n libraryNames) refs(idsList []string, names map[string]string) []LibraryRef {
	out := []LibraryRef{}
	for _, id := range idsList {
		if name, ok := names[id]; ok {
			out = append(out, LibraryRef{ID: id, Name: name})
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// placesText says where clips are, in plain words: "Spring promo, the B-roll
// collection and the Archive" ("" for nowhere).
func placesText(projects, collections []string, archived bool) string {
	parts := append([]string{}, projects...)
	switch len(collections) {
	case 0:
	case 1:
		parts = append(parts, "the "+collections[0]+" collection")
	default:
		parts = append(parts, "the "+joinAnd(collections)+" collections")
	}
	if archived {
		parts = append(parts, "the Archive")
	}
	return joinAnd(parts)
}

func refNames(rs []LibraryRef) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Name)
	}
	return out
}

// whereText says where a clip is now: "In the Inbox", "In Spring promo and
// the B-roll collection", "In the Trash".
func whereText(as *models.Asset, n libraryNames) string {
	if as.Trash != nil {
		return "In the Trash"
	}
	s := placesText(refNames(n.refs(as.ProjectIDs, n.projects)), refNames(n.refs(as.CollectionIDs, n.collections)), as.ArchivedAt != nil)
	if s == "" {
		return "In the Inbox"
	}
	return "In " + s
}

// trashFromText says where a clip in the Trash was: "From Spring promo", "From the Inbox".
func trashFromText(t *models.AssetTrash, n libraryNames) string {
	s := placesText(refNames(n.refs(t.ProjectIDs, n.projects)), refNames(n.refs(t.CollectionIDs, n.collections)), t.Archived)
	if s == "" {
		return "From the Inbox"
	}
	return "From " + s
}

// LibraryRef names a project or a collection.
type LibraryRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// LibraryTrash is a clip's stay in the Trash.
type LibraryTrash struct {
	At time.Time `json:"at"`
	By string    `json:"by,omitempty" jsonschema:"description=Who moved it there."`
	// From says where it was (Restore puts it back there).
	From string `json:"from"`
	// DeletesOn is when "Empty the Trash after N days" deletes it (only while that is on).
	DeletesOn *time.Time `json:"deletesOn,omitempty"`
}

// LibraryItem is one clip in a Library view.
type LibraryItem struct {
	AssetSummary
	// Where says where it is, in plain words ("In the B-roll collection").
	Where       string        `json:"where"`
	Projects    []LibraryRef  `json:"projects"`
	Collections []LibraryRef  `json:"collections"`
	Trash       *LibraryTrash `json:"trash,omitempty"`
	// MatchedIn says what a search matched: title, filename, alias, tag,
	// source (where an import came from), collection (its name) or words (what was said).
	MatchedIn string `json:"matchedIn,omitempty" jsonschema:"enum=title,enum=filename,enum=alias,enum=tag,enum=source,enum=collection,enum=words,enum="`
}

func libraryItem(as *models.Asset, n libraryNames, emptyAfterDays int) LibraryItem {
	it := LibraryItem{AssetSummary: summarize(*as), Where: whereText(as, n), Projects: n.refs(as.ProjectIDs, n.projects), Collections: n.refs(as.CollectionIDs, n.collections)}
	if as.Trash != nil {
		t := &LibraryTrash{At: as.Trash.At, By: firstNonBlank(as.Trash.By.UserName, as.Trash.By.UserID), From: trashFromText(as.Trash, n)}
		if emptyAfterDays > 0 {
			d := as.Trash.At.Add(time.Duration(emptyAfterDays) * 24 * time.Hour)
			t.DeletesOn = &d
		}
		it.Trash = t
	}
	return it
}

func firstNonBlank(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

// LibraryActionIn names the clips a Library action applies to.
type LibraryActionIn struct {
	AssetIDs []string `json:"assetIds" jsonschema:"minItems=1,description=The clips: IDs\\, or titles or file names."`
	// ProjectID is the project the clips are filed out of (Archive or Move to
	// Trash from a project's Sources): they leave it, which is refused while
	// that project's cut uses them.
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=The project they are filed out of (ID or name); they leave it."`
}

// LibraryActionRow is what happened to one clip.
type LibraryActionRow struct {
	AssetID string `json:"assetId"`
	Title   string `json:"title"`
	// Outcome is done, already (nothing to do) or refused (Reason says why, Uses where).
	Outcome string   `json:"outcome" jsonschema:"enum=done,enum=already,enum=refused"`
	Reason  string   `json:"reason,omitempty"`
	Uses    []UseRow `json:"uses,omitempty"`
	// Where says where it is now.
	Where string `json:"where"`
}

// LibraryActionOut is the result of a Library action on several clips.
type LibraryActionOut struct {
	Items   []LibraryActionRow `json:"items"`
	Done    int                `json:"done"`
	Already int                `json:"already"`
	Refused int                `json:"refused"`
	Message string             `json:"message"`
	// Collection is the collection a clip was added to or removed from
	// (Created: collection.add made it).
	Collection *CollectionRow `json:"collection,omitempty"`
	Created    bool           `json:"created,omitempty"`
}

// Row outcomes.
const (
	OutcomeDone    = "done"
	OutcomeAlready = "already"
	OutcomeRefused = "refused"
)

// resolveFootage finds a clip by ID, or by title or file name among the
// clips scope matches (the whole workspace when scope is empty).
func resolveFootage(ctx context.Context, a *app.App, ref string, scope bson.M) (*models.Asset, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, invalid("name a clip")
	}
	var as models.Asset
	if ids.Valid(ids.Asset, ref) {
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), ref, &as); err != nil {
			return nil, notFound("asset", ref)
		}
		return &as, nil
	}
	filter := bson.M{}
	for k, v := range scope {
		filter[k] = v
	}
	re := bson.M{"$regex": "^" + regexpQuote(ref) + "$", "$options": "i"}
	filter["$or"] = bson.A{bson.M{"title": re}, bson.M{"name": re}, bson.M{"aliases.name": re}}
	var list []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), filter, &list, options.Find().SetLimit(20)); err != nil {
		return nil, err
	}
	if len(list) == 0 {
		// Titles are stored only once someone renames a clip; older ones go by the file name.
		var all []models.Asset
		f := bson.M{"title": bson.M{"$exists": false}}
		for k, v := range scope {
			f[k] = v
		}
		_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), f, &all, options.Find().SetProjection(bson.M{"media.streams": 0}).SetLimit(5000))
		for _, x := range all {
			if strings.EqualFold(x.DisplayTitle(), ref) {
				list = append(list, x)
			}
		}
	}
	switch len(list) {
	case 1:
		return &list[0], nil
	case 0:
		return nil, apperr.New(apperr.NotFound, "There's no clip called %q.", ref).
			WithHint("Check the name in the Library (titles and file names both work).").WithCommand("metarush library --json")
	}
	return nil, apperr.New(apperr.InvalidInput, "%d clips are called %q.", len(list), ref).
		WithHint("Choose it in the Library instead, or give one of them another title.").WithCommand("metarush library --json")
}

// libraryAction runs one filing action over several clips, one at a time;
// each happens, was already so, or is refused with the reason. When nothing
// happened and something was refused, the whole call fails with the reasons.
func libraryAction(ctx context.Context, a *app.App, in *LibraryActionIn, scope bson.M, doneFmt, verbNot string,
	each func(as *models.Asset, row *LibraryActionRow) error) (*LibraryActionOut, error) {
	refs := uniqueStrings(in.AssetIDs)
	if len(refs) == 0 {
		return nil, invalid("name at least one clip")
	}
	if len(refs) > 500 {
		return nil, invalid("%d clips at once is too many (500 at most)", len(refs))
	}
	out := &LibraryActionOut{Items: []LibraryActionRow{}}
	var changed []models.Asset
	for _, ref := range refs {
		as, err := resolveFootage(ctx, a, ref, scope)
		if err != nil {
			return nil, err
		}
		row := LibraryActionRow{AssetID: as.ID, Title: as.DisplayTitle(), Outcome: OutcomeDone}
		if err := each(as, &row); err != nil {
			return nil, err
		}
		switch row.Outcome {
		case OutcomeDone:
			out.Done++
		case OutcomeAlready:
			out.Already++
		case OutcomeRefused:
			out.Refused++
		}
		var cur models.Asset
		if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), as.ID, &cur) == nil {
			changed = append(changed, cur)
		} else {
			changed = append(changed, *as)
		}
		out.Items = append(out.Items, row)
	}
	names := loadLibraryNames(ctx, a, changed)
	for i := range out.Items {
		out.Items[i].Where = whereText(&changed[i], names)
	}
	out.Message = actionMessage(out, doneFmt, verbNot)
	if out.Done+out.Already == 0 && out.Refused > 0 {
		return nil, apperr.New(apperr.Conflict, "%s", out.Message).WithDetails(map[string]any{"items": out.Items})
	}
	return out, nil
}

// actionMessage says what an action did: "Archived “Kitchen chat”.",
// "Archived 3 clips. “Pan” wasn't archived: Spring promo's cut (version 2)
// uses it." doneFmt has one %s for the clip or the count.
func actionMessage(out *LibraryActionOut, doneFmt, verbNot string) string {
	var done, refused []LibraryActionRow
	for _, r := range out.Items {
		switch r.Outcome {
		case OutcomeDone, OutcomeAlready:
			done = append(done, r)
		case OutcomeRefused:
			refused = append(refused, r)
		}
	}
	var parts []string
	if len(done) == 1 {
		parts = append(parts, fmt.Sprintf(doneFmt, "“"+done[0].Title+"”"))
	} else if len(done) > 1 {
		parts = append(parts, fmt.Sprintf(doneFmt, plural(len(done), "clip")))
	}
	for i, r := range refused {
		if i == 3 {
			parts = append(parts, fmt.Sprintf("%d more weren't.", len(refused)-3))
			break
		}
		parts = append(parts, fmt.Sprintf("“%s” %s: %s", r.Title, verbNot, strings.TrimSuffix(r.Reason, ".")+"."))
	}
	return strings.Join(parts, " ")
}

func uniqueStrings(xs []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, x := range xs {
		x = strings.TrimSpace(x)
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// inTrashRefusal is the answer for filing a clip that is in the Trash.
const inTrashRefusal = "it's in the Trash; restore it first"

// currentCutUse says which project's latest cut uses the clip, if any (any
// branch head): those can't lose it.
func currentCutUse(ctx context.Context, a *app.App, as *models.Asset) (string, []UseRow, error) {
	uses, err := assetUses(ctx, a, as)
	if err != nil {
		return "", nil, err
	}
	for _, u := range uses {
		if u.Kind == UseCut && u.Current {
			return strings.Replace(u.Text, "Used in ", "", 1) + " uses it; take it out of the cut first", []UseRow{u}, nil
		}
	}
	return "", nil, nil
}

// leaveProject checks that a clip can leave a project (it is in it, and the
// project's latest cut doesn't use it); "" when it can.
func leaveProject(ctx context.Context, a *app.App, p *models.Project, as *models.Asset) string {
	if !slices.Contains(as.ProjectIDs, p.ID) {
		return "it isn't in " + p.Name
	}
	if why, used := currentCutUses(ctx, a, p, as.ID); used {
		return why + "; take it out of the cut first"
	}
	return ""
}

// pauseQuestions puts a clip's open questions aside while it is in the Trash
// (they come back with it).
func pauseQuestions(ctx context.Context, a *app.App, assetID string) {
	now := time.Now().UTC()
	_, _ = a.DB.C(store.CTodos).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": assetID, "state": models.TodoOpen},
		bson.M{"$set": bson.M{"state": models.TodoDismissed, "resolution": models.ResolvedObsolete, "pausedFor": "trash", "updatedAt": now, "answeredAt": now}})
}

// resumeQuestions reopens the questions pauseQuestions put aside.
func resumeQuestions(ctx context.Context, a *app.App, assetID string) {
	var paused []models.Todo
	_ = a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"assetId": assetID, "pausedFor": "trash"}, &paused, options.Find().SetProjection(bson.M{"_id": 1}))
	for _, t := range paused {
		_, err := a.DB.C(store.CTodos).UpdateOne(ctx, bson.M{"_id": t.ID, "workspaceId": a.WorkspaceID(), "pausedFor": "trash"},
			bson.M{"$set": bson.M{"state": models.TodoOpen, "updatedAt": time.Now().UTC()}, "$unset": bson.M{"resolution": "", "answeredAt": "", "pausedFor": ""}})
		if err != nil { // the same question was asked again meanwhile: keep that one
			_, _ = a.DB.C(store.CTodos).UpdateOne(ctx, bson.M{"_id": t.ID, "workspaceId": a.WorkspaceID()}, bson.M{"$unset": bson.M{"pausedFor": ""}})
		}
	}
	refreshAssetTodos(ctx, a, assetID)
}

// trashAsset moves one clip to the Trash: it leaves its project (from), its
// collections and the Archive, which are remembered for Restore.
func trashAsset(ctx context.Context, a *app.App, as *models.Asset, from string) error {
	now := time.Now().UTC()
	t := models.AssetTrash{At: now, By: a.Actor, CollectionIDs: as.CollectionIDs, Archived: as.ArchivedAt != nil}
	if from != "" {
		t.ProjectIDs = []string{from}
	}
	filter := bson.M{"_id": as.ID, "workspaceId": a.WorkspaceID(), "trash": bson.M{"$exists": false}}
	res, err := a.DB.C(store.CAssets).UpdateOne(ctx, filter, bson.M{"$set": bson.M{"trash": t, "projectIds": []string{}, "updatedAt": now},
		"$unset": bson.M{"collectionIds": "", "archivedAt": "", "archivedBy": ""}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return nil // it went to the Trash a moment ago
	}
	pauseQuestions(ctx, a, as.ID)
	if from != "" {
		refreshProjectCounts(ctx, a, from)
	}
	a.Audit(ctx, "asset.trash", "asset", as.ID, from, map[string]any{"title": as.DisplayTitle(), "collections": len(as.CollectionIDs), "archived": t.Archived})
	return nil
}

// RestoreFromTrash puts a clip in the Trash back where it was: its project
// and collections (those that still exist) and the Archive; the Inbox when
// none is left. It does nothing for a clip that isn't in the Trash.
func RestoreFromTrash(ctx context.Context, a *app.App, as *models.Asset) error {
	if as.Trash == nil {
		return nil
	}
	t := as.Trash
	var projects, collections []string
	if len(t.ProjectIDs) > 0 {
		var ps []models.Project
		_ = a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": t.ProjectIDs}}, &ps, options.Find().SetProjection(bson.M{"_id": 1}))
		for _, p := range ps {
			projects = append(projects, p.ID)
		}
	}
	if len(t.CollectionIDs) > 0 {
		var cs []models.Collection
		_ = a.DB.Find(ctx, store.CCollections, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": t.CollectionIDs}}, &cs)
		for _, c := range cs {
			collections = append(collections, c.ID)
		}
	}
	now := time.Now().UTC()
	upd := bson.M{"$set": bson.M{"updatedAt": now}, "$unset": bson.M{"trash": ""}}
	add := bson.M{}
	if len(projects) > 0 {
		add["projectIds"] = bson.M{"$each": projects}
	}
	if len(collections) > 0 {
		add["collectionIds"] = bson.M{"$each": collections}
	}
	if len(add) > 0 {
		upd["$addToSet"] = add
	}
	if t.Archived {
		upd["$set"].(bson.M)["archivedAt"] = now
		upd["$set"].(bson.M)["archivedBy"] = a.Actor
	}
	res, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": as.ID, "workspaceId": a.WorkspaceID(), "trash": bson.M{"$exists": true}}, upd)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return nil
	}
	for _, p := range projects {
		refreshProjectCounts(ctx, a, p)
	}
	resumeQuestions(ctx, a, as.ID)
	a.Audit(ctx, "asset.restore", "asset", as.ID, firstOr(projects), map[string]any{"title": as.DisplayTitle(), "projects": len(projects), "collections": len(collections), "archived": t.Archived})
	// A fresh value: decoding into the old one would keep fields the document no longer has (trash).
	var fresh models.Asset
	if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), as.ID, &fresh) == nil {
		*as = fresh
	}
	return nil
}

// refuseTrashed is the error for putting a clip that is in the Trash somewhere.
func refuseTrashed(as *models.Asset) error {
	if as.Trash == nil {
		return nil
	}
	return apperr.New(apperr.Conflict, "“%s” is in the Trash.", as.DisplayTitle()).
		WithHint("Restore it from the Trash in the Library first.").WithCommand("metarush asset restore " + as.ID)
}

func init() {
	Def("asset.archive", "File clips in the Archive, the one “keep for later” shelf: they leave the Inbox (and, with a project, that project) without going to a project; nothing is deleted", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *LibraryActionIn) (*LibraryActionOut, error) {
		var p *models.Project
		scope := bson.M{"trash": bson.M{"$exists": false}}
		if strings.TrimSpace(in.ProjectID) != "" {
			var err error
			if p, err = resolveProject(ctx, a, in.ProjectID); err != nil {
				return nil, err
			}
			scope["projectIds"] = p.ID
		}
		return libraryAction(ctx, a, in, scope, "Archived %s.", "wasn't archived", func(as *models.Asset, row *LibraryActionRow) error {
			if as.Trash != nil {
				row.Outcome, row.Reason = OutcomeRefused, inTrashRefusal
				return nil
			}
			now := time.Now().UTC()
			upd := bson.M{"$set": bson.M{"updatedAt": now}}
			if as.ArchivedAt == nil {
				upd["$set"].(bson.M)["archivedAt"] = now
				upd["$set"].(bson.M)["archivedBy"] = a.Actor
			}
			if p != nil {
				if why := leaveProject(ctx, a, p, as); why != "" {
					row.Outcome, row.Reason = OutcomeRefused, why
					return nil
				}
				if err := carryAsset(ctx, a, as, p.ID, ""); err != nil { // What may leave (D289)
					row.Outcome, row.Reason = OutcomeRefused, strings.TrimSuffix(lowerFirst(apperr.From(err).Message), ".")
					return nil
				}
				upd["$pull"] = bson.M{"projectIds": p.ID}
			} else if as.ArchivedAt != nil {
				row.Outcome = OutcomeAlready
				return nil
			}
			if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, upd); err != nil {
				return err
			}
			if p != nil {
				refreshProjectCounts(ctx, a, p.ID)
				refreshAssetTodos(ctx, a, as.ID)
			}
			pid := ""
			if p != nil {
				pid = p.ID
			}
			a.Audit(ctx, "asset.archive", "asset", as.ID, pid, map[string]any{"title": as.DisplayTitle()})
			return nil
		})
	})
	Def("asset.unarchive", "Take clips out of the Archive: back in the Inbox unless a project or collection has them", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *LibraryActionIn) (*LibraryActionOut, error) {
		out, err := libraryAction(ctx, a, in, bson.M{"archivedAt": bson.M{"$exists": true}}, "Took %s out of the Archive.", "wasn't taken out", func(as *models.Asset, row *LibraryActionRow) error {
			if as.ArchivedAt == nil {
				row.Outcome = OutcomeAlready
				return nil
			}
			if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"updatedAt": time.Now().UTC()}, "$unset": bson.M{"archivedAt": "", "archivedBy": ""}}); err != nil {
				return err
			}
			a.Audit(ctx, "asset.unarchive", "asset", as.ID, firstOr(as.ProjectIDs), map[string]any{"title": as.DisplayTitle()})
			return nil
		})
		if err == nil && len(out.Items) == 1 && out.Done == 1 {
			out.Message = fmt.Sprintf("Took “%s” out of the Archive. It's %s now.", out.Items[0].Title, lowerFirst(out.Items[0].Where))
		}
		return out, err
	})
	Def("asset.trash", "Move clips to the Trash: they disappear from the Inbox, their collections and the Archive (and, with a project, that project) until restored; nothing is deleted until the Trash is emptied. Refused while any project's latest cut uses a clip or another project has it", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *LibraryActionIn) (*LibraryActionOut, error) {
		var p *models.Project
		scope := bson.M{}
		if strings.TrimSpace(in.ProjectID) != "" {
			var err error
			if p, err = resolveProject(ctx, a, in.ProjectID); err != nil {
				return nil, err
			}
			scope["projectIds"] = p.ID
		}
		return libraryAction(ctx, a, in, scope, "Moved %s to the Trash.", "wasn't moved to the Trash", func(as *models.Asset, row *LibraryActionRow) error {
			if as.Trash != nil {
				row.Outcome = OutcomeAlready
				return nil
			}
			here := ""
			if p != nil {
				if !slices.Contains(as.ProjectIDs, p.ID) {
					row.Outcome, row.Reason = OutcomeRefused, "it isn't in "+p.Name
					return nil
				}
				here = p.ID
			}
			var others []string
			for _, pid := range as.ProjectIDs {
				if pid != here {
					others = append(others, pid)
				}
			}
			if n := projectNames(ctx, a, others); len(n) > 0 {
				what := "it's in "
				if here != "" {
					what = "it's also in "
				}
				row.Outcome, row.Reason = OutcomeRefused, what+joinAnd(n)+"; remove it from there first"
				return nil
			}
			why, uses, err := currentCutUse(ctx, a, as)
			if err != nil {
				return err
			}
			if why != "" {
				row.Outcome, row.Reason, row.Uses = OutcomeRefused, why, uses
				return nil
			}
			return trashAsset(ctx, a, as, here)
		})
	})
	Def("asset.restore", "Put clips in the Trash back where they were: their project, collections and the Archive (the Inbox when none is left)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *LibraryActionIn) (*LibraryActionOut, error) {
		out, err := libraryAction(ctx, a, in, bson.M{"trash": bson.M{"$exists": true}}, "Restored %s.", "wasn't restored", func(as *models.Asset, row *LibraryActionRow) error {
			if as.Trash == nil {
				row.Outcome = OutcomeAlready
				return nil
			}
			return RestoreFromTrash(ctx, a, as)
		})
		if err == nil && len(out.Items) == 1 && out.Done == 1 {
			out.Message = fmt.Sprintf("Restored “%s”. It's back %s.", out.Items[0].Title, lowerFirst(out.Items[0].Where))
		}
		return out, err
	})
	Def("library.list", "The Library: the Inbox, collections, the Archive, footage in projects and the Trash, with counts; a search covers every view", Flags{}, libraryList)
}

// LibraryIn picks a Library view.
type LibraryIn struct {
	View string `json:"view,omitempty" jsonschema:"enum=inbox,enum=collections,enum=archived,enum=projects,enum=trash,enum=all,enum=,description=Default: the Inbox (all: every clip\\, the Trash included)."`
	// Collection shows one collection (ID or name); the view is collections.
	Collection string `json:"collection,omitempty"`
	// Query searches every view: titles, file names, tags, where an import
	// came from, collection names and what was said.
	Query string `json:"query,omitempty"`
	Limit int    `json:"limit,omitempty" jsonschema:"minimum=0,maximum=2000"`
}

// LibraryCounts are the clips in each view (matches, while searching).
type LibraryCounts struct {
	Inbox       int `json:"inbox"`
	Collections int `json:"collections"`
	Archived    int `json:"archived"`
	Projects    int `json:"projects"`
	Trash       int `json:"trash"`
	All         int `json:"all"`
}

// LibraryOut is one Library view.
type LibraryOut struct {
	View        string          `json:"view"`
	Query       string          `json:"query,omitempty"`
	Items       []LibraryItem   `json:"items"`
	Total       int             `json:"total" jsonschema:"description=Clips in this view (Items may stop at the limit)."`
	Counts      LibraryCounts   `json:"counts"`
	Collections []CollectionRow `json:"collections" jsonschema:"description=Every collection\\, with how many clips are on it."`
	// Collection is the collection shown (view collections with a collection).
	Collection *CollectionRow `json:"collection,omitempty"`
	// TrashSize is the size of everything in the Trash; EmptyAfterDays is
	// "Empty the Trash after N days" (0: off, nothing is deleted on its own).
	TrashSize      int64  `json:"trashSize"`
	EmptyAfterDays int    `json:"emptyAfterDays"`
	Summary        string `json:"summary"`
}

var viewNames = map[string]string{ViewInbox: "in the Inbox", ViewCollections: "on collections", ViewArchived: "in the Archive", ViewProjects: "in projects", ViewTrash: "in the Trash", ViewAll: "in the Library"}

func libraryList(ctx context.Context, a *app.App, in *LibraryIn) (*LibraryOut, error) {
	view := strings.ToLower(strings.TrimSpace(in.View))
	switch {
	case view == "" && strings.TrimSpace(in.Query) != "":
		view = ViewAll // a search covers every view unless one is named
	case view == "":
		view = ViewInbox
	}
	if _, ok := viewNames[view]; !ok {
		return nil, invalid("%q isn't a view of the Library (inbox, collections, archived, projects, trash or all)", in.View)
	}
	out := &LibraryOut{View: view, Query: strings.TrimSpace(in.Query), Items: []LibraryItem{}, Collections: []CollectionRow{}}
	var col *models.Collection
	if strings.TrimSpace(in.Collection) != "" {
		c, err := resolveCollection(ctx, a, in.Collection)
		if err != nil {
			return nil, err
		}
		col, out.View, view = c, ViewCollections, ViewCollections
	}
	limit := int64(in.Limit)
	if limit <= 0 {
		limit = 500
	}
	if limit > 2000 {
		limit = 2000
	}
	days := trashDays(ctx, a)
	out.EmptyAfterDays = days
	// A search covers every view: first the matching clips, then each view counts them.
	var matched map[string]string
	if out.Query != "" {
		var err error
		if matched, err = libraryMatches(ctx, a, out.Query); err != nil {
			return nil, err
		}
	}
	scoped := func(f bson.M) bson.M {
		if matched != nil {
			idsList := make([]string, 0, len(matched))
			for id := range matched {
				idsList = append(idsList, id)
			}
			f["_id"] = bson.M{"$in": idsList}
		}
		return f
	}
	count := func(v string) int {
		n, _ := a.DB.C(store.CAssets).CountDocuments(ctx, withWorkspace(a, scoped(viewFilter(v, ""))))
		return int(n)
	}
	out.Counts = LibraryCounts{Inbox: count(ViewInbox), Collections: count(ViewCollections), Archived: count(ViewArchived), Projects: count(ViewProjects), Trash: count(ViewTrash), All: count(ViewAll)}
	colID := ""
	if col != nil {
		colID = col.ID
	}
	filter := scoped(viewFilter(view, colID))
	total, err := a.DB.C(store.CAssets).CountDocuments(ctx, withWorkspace(a, filter))
	if err != nil {
		return nil, err
	}
	out.Total = int(total)
	sortKey := bson.D{{Key: "createdAt", Value: -1}}
	switch view {
	case ViewTrash:
		sortKey = bson.D{{Key: "trash.at", Value: -1}}
	case ViewArchived:
		sortKey = bson.D{{Key: "archivedAt", Value: -1}}
	}
	var assets []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), filter, &assets, options.Find().SetSort(sortKey).SetLimit(limit).SetProjection(bson.M{"media.streams": 0})); err != nil {
		return nil, err
	}
	if matched != nil {
		sort.SliceStable(assets, func(i, j int) bool {
			return rankScore[matchRank(matched[assets[i].ID])] > rankScore[matchRank(matched[assets[j].ID])]
		})
	}
	names := loadLibraryNames(ctx, a, assets)
	for i := range assets {
		it := libraryItem(&assets[i], names, days)
		if matched != nil {
			it.MatchedIn = matchField(matched[assets[i].ID])
		}
		out.Items = append(out.Items, it)
	}
	rows, err := collectionRows(ctx, a, nil)
	if err != nil {
		return nil, err
	}
	out.Collections = rows
	if col != nil {
		for i := range rows {
			if rows[i].ID == col.ID {
				r := rows[i]
				out.Collection = &r
			}
		}
	}
	out.TrashSize = trashSize(ctx, a)
	out.Summary = librarySummary(out, col)
	return out, nil
}

func withWorkspace(a *app.App, f bson.M) bson.M {
	g := bson.M{"workspaceId": a.WorkspaceID()}
	for k, v := range f {
		g[k] = v
	}
	return g
}

// librarySummary says what a view holds: "12 clips in the Inbox, waiting to be filed."
func librarySummary(out *LibraryOut, col *models.Collection) string {
	if out.Query != "" {
		if out.Counts.All == 0 {
			return fmt.Sprintf("Nothing in the Library matches “%s”.", out.Query)
		}
		verb := "match"
		if out.Counts.All == 1 {
			verb = "matches"
		}
		return fmt.Sprintf("%s %s “%s”: %d in the Inbox, %d on collections, %d archived, %d in projects, %d in the Trash.",
			plural(out.Counts.All, "clip"), verb, out.Query, out.Counts.Inbox, out.Counts.Collections, out.Counts.Archived, out.Counts.Projects, out.Counts.Trash)
	}
	switch {
	case col != nil:
		return fmt.Sprintf("%s on the %s collection.", plural(out.Total, "clip"), col.Name)
	case out.View == ViewInbox && out.Total == 0:
		return "The Inbox is empty: everything has been filed."
	case out.View == ViewInbox:
		return fmt.Sprintf("%s in the Inbox, waiting to be added to a project or filed away.", plural(out.Total, "clip"))
	case out.View == ViewTrash && out.Total == 0:
		return "The Trash is empty."
	case out.View == ViewTrash:
		s := fmt.Sprintf("%s in the Trash (%s).", plural(out.Total, "clip"), sizeText(out.TrashSize))
		if out.EmptyAfterDays > 0 {
			return s + fmt.Sprintf(" Each is deleted for good %s after it was moved there.", plural(out.EmptyAfterDays, "day"))
		}
		return s + " Nothing here is deleted until you empty the Trash."
	}
	return fmt.Sprintf("%s %s.", plural(out.Total, "clip"), viewNames[out.View])
}

// sizeText is a size in plain words: 1.2 GB.
func sizeText(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d bytes", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v, i := float64(n)/u, 0
	for v >= u && i < len(units)-1 {
		v /= u
		i++
	}
	if v < 10 {
		return fmt.Sprintf("%.1f %s", v, units[i])
	}
	return fmt.Sprintf("%.0f %s", v, units[i])
}

// trashSize adds up the clips in the Trash.
func trashSize(ctx context.Context, a *app.App) int64 {
	cur, err := a.DB.C(store.CAssets).Aggregate(ctx, bson.A{
		bson.M{"$match": bson.M{"workspaceId": a.WorkspaceID(), "trash": bson.M{"$exists": true}}},
		bson.M{"$group": bson.M{"_id": nil, "size": bson.M{"$sum": "$size"}}},
	})
	if err != nil {
		return 0
	}
	var rows []struct {
		Size int64 `bson:"size"`
	}
	if cur.All(ctx, &rows) != nil || len(rows) == 0 {
		return 0
	}
	return rows[0].Size
}

// libraryMatches finds the clips a Library search matches, anywhere (the
// Trash included), with what matched and how well ("rank:field").
func libraryMatches(ctx context.Context, a *app.App, q string) (map[string]string, error) {
	words := queryWords(q)
	if len(words) == 0 {
		return nil, invalid("search for at least one word")
	}
	if len(words) > 12 {
		words = words[:12]
	}
	phrase := ""
	if len(words) > 1 {
		phrase = strings.Join(words, " ")
	}
	out := map[string]string{}
	better := func(id, rank, field string) {
		if cur, ok := out[id]; !ok || rankScore[rank] > rankScore[matchRank(cur)] {
			out[id] = rank + ":" + field
		}
	}
	re := wordRegex(words)
	var assets []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"kind": bson.M{"$ne": "image"}, "$or": bson.A{bson.M{"title": re}, bson.M{"name": re}, bson.M{"aliases.name": re},
		bson.M{"tags": re}, bson.M{"source.title": re}, bson.M{"source.handle": re}, bson.M{"source.uploader": re}}}, &assets,
		options.Find().SetProjection(bson.M{"title": 1, "name": 1, "aliases": 1, "tags": 1, "source": 1}).SetLimit(2000)); err != nil {
		return nil, err
	}
	for i := range assets {
		as := &assets[i]
		cands := []struct{ field, text string }{{"title", as.DisplayTitle()}, {"filename", as.Name}}
		for _, al := range as.Aliases {
			cands = append(cands, struct{ field, text string }{"alias", al.Name})
		}
		for _, t := range as.Tags {
			cands = append(cands, struct{ field, text string }{"tag", t})
		}
		if s := as.Source; s != nil {
			cands = append(cands, struct{ field, text string }{"source", s.Title}, struct{ field, text string }{"source", s.Handle}, struct{ field, text string }{"source", s.Uploader})
		}
		for _, c := range cands {
			if r, _ := match(c.text, words, phrase); r != "" {
				better(as.ID, r, c.field)
			}
		}
	}
	// Collections whose name matches: everything on them.
	var cols []models.Collection
	_ = a.DB.Find(ctx, store.CCollections, a.WorkspaceID(), bson.M{"name": re}, &cols)
	for _, c := range cols {
		r, _ := match(c.Name, words, phrase)
		if r == "" {
			continue
		}
		var on []models.Asset
		_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"collectionIds": c.ID, "kind": bson.M{"$ne": "image"}}, &on, options.Find().SetProjection(bson.M{"_id": 1}).SetLimit(2000))
		for _, as := range on {
			better(as.ID, r, "collection")
		}
	}
	// What was said (the transcript text index).
	var segs []models.TranscriptSegment
	_ = a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), bson.M{"$text": bson.M{"$search": q}}, &segs, options.Find().SetProjection(bson.M{"assetId": 1, "text": 1}).SetLimit(500))
	if len(segs) > 0 {
		seen := map[string]bool{}
		var need []string
		for _, s := range segs {
			if !seen[s.AssetID] {
				seen[s.AssetID] = true
				need = append(need, s.AssetID)
			}
		}
		var footage []models.Asset
		_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": need}, "kind": bson.M{"$ne": "image"}}, &footage, options.Find().SetProjection(bson.M{"_id": 1}))
		ok := map[string]bool{}
		for _, as := range footage {
			ok[as.ID] = true
		}
		for _, s := range segs {
			if !ok[s.AssetID] {
				continue
			}
			r, _ := match(s.Text, words, phrase)
			if r == "" {
				r = RankForms
			}
			better(s.AssetID, r, "words")
		}
	}
	return out, nil
}

func matchRank(m string) string {
	r, _, _ := strings.Cut(m, ":")
	return r
}

func matchField(m string) string {
	_, f, _ := strings.Cut(m, ":")
	return f
}

// fillFiled says, for search results in no project, where the clip is
// filed: the Inbox, a collection or the Archive.
func fillFiled(ctx context.Context, a *app.App, out *SearchOut) {
	need := map[string]bool{}
	for _, m := range out.Moments {
		if m.ProjectID == "" {
			need[m.AssetID] = true
		}
	}
	for _, v := range out.Visuals {
		if v.ProjectID == "" {
			need[v.AssetID] = true
		}
	}
	for _, v := range out.Videos {
		if v.ProjectID == "" {
			need[v.AssetID] = true
		}
	}
	if len(need) == 0 {
		return
	}
	var assets []models.Asset
	_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": keys(need)}}, &assets,
		options.Find().SetProjection(bson.M{"projectIds": 1, "collectionIds": 1, "archivedAt": 1}))
	names := loadLibraryNames(ctx, a, assets)
	where := map[string]string{}
	for i := range assets {
		where[assets[i].ID] = strings.TrimPrefix(whereText(&assets[i], names), "In ")
	}
	for i := range out.Moments {
		if out.Moments[i].ProjectID == "" {
			out.Moments[i].Filed = where[out.Moments[i].AssetID]
		}
	}
	for i := range out.Visuals {
		if out.Visuals[i].ProjectID == "" {
			out.Visuals[i].Filed = where[out.Visuals[i].AssetID]
		}
	}
	for i := range out.Videos {
		if out.Videos[i].ProjectID == "" {
			out.Videos[i].Filed = where[out.Videos[i].AssetID]
		}
	}
}

// ResolveFootage finds a clip by ID, or by title or file name: within a
// project when projectID is set, else in the whole workspace (for packages
// that name footage the way people do, such as conversations).
func ResolveFootage(ctx context.Context, a *app.App, ref, projectID string) (*models.Asset, error) {
	scope := bson.M{}
	if projectID != "" && !ids.Valid(ids.Asset, strings.TrimSpace(ref)) {
		scope["projectIds"] = projectID
	}
	return resolveFootage(ctx, a, ref, scope)
}
