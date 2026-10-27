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
	"github.com/metavert-io/metarush/internal/names"
	"github.com/metavert-io/metarush/internal/store"
)

// Collections (D96): named shelves of footage people create ("B-roll",
// "Family", "Brand footage", "Stock"). A clip can be on several collections
// and in projects too; putting it on one takes it out of the Inbox. A
// project can use everything on a collection through a project link
// (`metarush project link <project> <collection>`), the way it uses another
// project: nothing is copied, and new clips on the collection show up there.

// CollectionRow is a collection with what is on it.
type CollectionRow struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Count        int       `json:"count" jsonschema:"description=Clips on it."`
	Duration     float64   `json:"duration" jsonschema:"description=Their total length in seconds."`
	Size         int64     `json:"size"`
	CoverAssetID string    `json:"coverAssetId,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	// UsedBy are the projects that use it through a project link.
	UsedBy []LibraryRef `json:"usedBy"`
	// Text says it in plain words: "12 clips · used by Spring promo".
	Text string `json:"text"`
}

// CollectionCreateIn creates a collection.
type CollectionCreateIn struct {
	Name string `json:"name" jsonschema:"minLength=1,maxLength=80"`
}

// CollectionRenameIn renames a collection.
type CollectionRenameIn struct {
	Collection string `json:"collection" jsonschema:"minLength=1,description=The collection (ID or name)."`
	Name       string `json:"name" jsonschema:"minLength=1,maxLength=80"`
}

// CollectionItemsIn puts clips on a collection or takes them off.
type CollectionItemsIn struct {
	Collection string   `json:"collection" jsonschema:"minLength=1,description=The collection (ID or name)."`
	AssetIDs   []string `json:"assetIds" jsonschema:"minItems=1,description=The clips: IDs\\, or titles or file names."`
	// Create makes the collection when there is none by that name (add only).
	Create bool `json:"create,omitempty"`
}

// CollectionRefIn names a collection.
type CollectionRefIn struct {
	Collection string `json:"collection" jsonschema:"minLength=1,description=The collection (ID or name)."`
}

// CollectionListIn has no fields.
type CollectionListIn struct{}

// CollectionListOut lists collections.
type CollectionListOut struct {
	Collections []CollectionRow `json:"collections"`
}

// CollectionOut is a collection after a change.
type CollectionOut struct {
	Collection CollectionRow `json:"collection"`
	Message    string        `json:"message"`
}

// CollectionShowIn shows one collection.
type CollectionShowIn struct {
	Collection string `json:"collection" jsonschema:"minLength=1,description=The collection (ID or name)."`
	Limit      int    `json:"limit,omitempty" jsonschema:"minimum=0,maximum=2000"`
}

// CollectionShowOut is a collection with its clips and the projects that use it.
type CollectionShowOut struct {
	Collection CollectionRow    `json:"collection"`
	Items      []LibraryItem    `json:"items"`
	Links      []ProjectLinkRow `json:"links" jsonschema:"description=The projects that use it\\, and what they bring in."`
	Summary    string           `json:"summary"`
}

// CollectionDeleteOut says what happened to a deleted collection's clips.
type CollectionDeleteOut struct {
	Message string `json:"message"`
	// Clips were on it (none is deleted); InInbox of them are back in the Inbox.
	Clips   int `json:"clips"`
	InInbox int `json:"inInbox"`
	// Kept are clips projects' cuts use through a link to it: they were
	// added to those projects, so the cuts keep them.
	Kept []LinkItemRow `json:"kept,omitempty"`
}

// collectionKey folds a name for uniqueness.
func collectionKey(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

func cleanCollectionName(name string) (string, error) {
	n := strings.Join(strings.Fields(name), " ")
	if n == "" {
		return "", invalid("a collection needs a name")
	}
	if l := len([]rune(n)); l > 80 {
		return "", invalid("the name is %d characters; the limit is 80", l)
	}
	return n, nil
}

// resolveCollection finds a collection by ID or name (small typos forgiven).
func resolveCollection(ctx context.Context, a *app.App, ref string) (*models.Collection, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, invalid("name a collection")
	}
	var c models.Collection
	if ids.Valid(ids.Collection, ref) {
		if err := a.DB.GetByID(ctx, store.CCollections, a.WorkspaceID(), ref, &c); err != nil {
			return nil, notFound("collection", ref)
		}
		return &c, nil
	}
	if err := a.DB.C(store.CCollections).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "key": collectionKey(ref)}).Decode(&c); err == nil {
		return &c, nil
	}
	var all []models.Collection
	if err := a.DB.Find(ctx, store.CCollections, a.WorkspaceID(), bson.M{}, &all); err != nil {
		return nil, err
	}
	cands := make([]names.Candidate, 0, len(all))
	for _, x := range all {
		cands = append(cands, names.Candidate{ID: x.ID, Name: x.Name, Weight: float64(x.UpdatedAt.Unix())})
	}
	res := names.Resolve(ref, cands)
	switch {
	case res.Found != nil:
		for i := range all {
			if all[i].ID == res.Found.ID {
				return &all[i], nil
			}
		}
	case len(res.Ambiguous) > 0:
		return nil, apperr.New(apperr.InvalidInput, "%q could be %s.", ref, names.Quote(res.Ambiguous)).
			WithHint("Use the collection's full name (the Library lists them).").WithCommand("metarush collection list")
	}
	return nil, apperr.New(apperr.NotFound, "There's no collection called %q.", ref).
		WithHint("The Library lists your collections, and you can make a new one there.").WithCommand("metarush collection list")
}

func createCollection(ctx context.Context, a *app.App, name string) (*models.Collection, error) {
	n, err := cleanCollectionName(name)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	c := models.Collection{ID: ids.New(ids.Collection), WorkspaceID: a.WorkspaceID(), Name: n, Key: collectionKey(n), CreatedAt: now, CreatedBy: a.Actor, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CCollections, c); err != nil {
		if store.IsDuplicateKey(err) {
			return nil, apperr.New(apperr.Conflict, "There's already a collection called “%s”.", n).WithHint("Add clips to it, or choose another name.")
		}
		return nil, err
	}
	a.Audit(ctx, "collection.create", "collection", c.ID, "", map[string]any{"name": n})
	return &c, nil
}

// collectionRows describes collections (all of them when only is nil) with
// their clips' count, length, size, a cover and the projects that use them.
func collectionRows(ctx context.Context, a *app.App, only []string) ([]CollectionRow, error) {
	f := bson.M{}
	if only != nil {
		f["_id"] = bson.M{"$in": only}
	}
	var cs []models.Collection
	if err := a.DB.Find(ctx, store.CCollections, a.WorkspaceID(), f, &cs); err != nil {
		return nil, err
	}
	out := []CollectionRow{}
	if len(cs) == 0 {
		return out, nil
	}
	byID := map[string]*CollectionRow{}
	idsList := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, CollectionRow{ID: c.ID, Name: c.Name, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, UsedBy: []LibraryRef{}})
		idsList = append(idsList, c.ID)
	}
	for i := range out {
		byID[out[i].ID] = &out[i]
	}
	var assets []models.Asset
	_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"collectionIds": bson.M{"$in": idsList}}, &assets,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetProjection(bson.M{"collectionIds": 1, "size": 1, "kind": 1, "media.duration": 1, "variants.thumbnail.storageKey": 1}))
	for _, as := range assets {
		for _, cid := range as.CollectionIDs {
			r := byID[cid]
			if r == nil {
				continue
			}
			r.Count++
			r.Size += as.Size
			r.Duration += as.Media.Duration
			if r.CoverAssetID == "" && as.Kind == "video" && as.Variants[models.VariantThumbnail].StorageKey != "" {
				r.CoverAssetID = as.ID
			}
		}
	}
	var links []models.ProjectLink
	_ = a.DB.Find(ctx, store.CProjectLinks, a.WorkspaceID(), bson.M{"sourceId": bson.M{"$in": idsList}}, &links)
	if len(links) > 0 {
		pset := map[string]bool{}
		for _, l := range links {
			pset[l.ProjectID] = true
		}
		var ps []models.Project
		_ = a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": keys(pset)}}, &ps, options.Find().SetProjection(bson.M{"name": 1}))
		pn := map[string]string{}
		for _, p := range ps {
			pn[p.ID] = p.Name
		}
		for _, l := range links {
			if r := byID[l.SourceID]; r != nil && pn[l.ProjectID] != "" && !l.Kept {
				r.UsedBy = append(r.UsedBy, LibraryRef{ID: l.ProjectID, Name: pn[l.ProjectID]})
			}
		}
	}
	for i := range out {
		r := &out[i]
		sort.Slice(r.UsedBy, func(x, y int) bool { return r.UsedBy[x].Name < r.UsedBy[y].Name })
		r.Text = plural(r.Count, "clip")
		if len(r.UsedBy) > 0 {
			r.Text += " · used by " + joinAnd(refNames(r.UsedBy))
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

func collectionRow(ctx context.Context, a *app.App, id string) CollectionRow {
	rows, _ := collectionRows(ctx, a, []string{id})
	if len(rows) == 1 {
		return rows[0]
	}
	return CollectionRow{ID: id, UsedBy: []LibraryRef{}}
}

// linkedCutUse says which project's latest cut uses a clip through a link to
// the collection ("" when none does).
func linkedCutUse(ctx context.Context, a *app.App, c *models.Collection, as *models.Asset) string {
	var links []models.ProjectLink
	_ = a.DB.Find(ctx, store.CProjectLinks, a.WorkspaceID(), bson.M{"sourceId": c.ID}, &links)
	for _, l := range links {
		var p models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), l.ProjectID, &p) != nil || slices.Contains(as.ProjectIDs, p.ID) {
			continue
		}
		if why, used := currentCutUses(ctx, a, &p, as.ID); used {
			return why + " through its link to " + c.Name + "; take it out of the cut first"
		}
	}
	return ""
}

func init() {
	Def("collection.create", "Create a collection: a named shelf for footage (B-roll, Family, Stock…) that projects can use", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CollectionCreateIn) (*CollectionOut, error) {
		c, err := createCollection(ctx, a, in.Name)
		if err != nil {
			return nil, err
		}
		return &CollectionOut{Collection: collectionRow(ctx, a, c.ID), Message: fmt.Sprintf("Created the %s collection.", c.Name)}, nil
	})
	Def("collection.rename", "Rename a collection", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CollectionRenameIn) (*CollectionOut, error) {
		c, err := resolveCollection(ctx, a, in.Collection)
		if err != nil {
			return nil, err
		}
		n, err := cleanCollectionName(in.Name)
		if err != nil {
			return nil, err
		}
		if err := a.DB.Update(ctx, store.CCollections, a.WorkspaceID(), c.ID, bson.M{"$set": bson.M{"name": n, "key": collectionKey(n), "updatedAt": time.Now().UTC()}}); err != nil {
			if store.IsDuplicateKey(err) {
				return nil, apperr.New(apperr.Conflict, "There's already a collection called “%s”.", n)
			}
			return nil, err
		}
		a.Audit(ctx, "collection.rename", "collection", c.ID, "", map[string]any{"name": n, "previous": c.Name})
		return &CollectionOut{Collection: collectionRow(ctx, a, c.ID), Message: fmt.Sprintf("Renamed %s to %s.", c.Name, n)}, nil
	})
	Def("collection.add", "Put clips on a collection (with create, make the collection first when there is none by that name); they leave the Inbox and stay in their projects", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CollectionItemsIn) (*LibraryActionOut, error) {
		var c *models.Collection
		var err error
		created := false
		if in.Create {
			// Only that exact name (in any case): a close name must not take the clips.
			var found models.Collection
			switch {
			case ids.Valid(ids.Collection, strings.TrimSpace(in.Collection)):
				c, err = resolveCollection(ctx, a, in.Collection)
			case a.DB.C(store.CCollections).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "key": collectionKey(in.Collection)}).Decode(&found) == nil:
				c = &found
			default:
				c, err = createCollection(ctx, a, in.Collection)
				created = err == nil
				if apperr.Is(err, apperr.Conflict) { // made a moment ago by someone else
					if e := a.DB.C(store.CCollections).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "key": collectionKey(in.Collection)}).Decode(&found); e == nil {
						c, err, created = &found, nil, false
					}
				}
			}
		} else {
			c, err = resolveCollection(ctx, a, in.Collection)
		}
		if err != nil {
			return nil, err
		}
		out, err := libraryAction(ctx, a, &LibraryActionIn{AssetIDs: in.AssetIDs}, bson.M{"trash": bson.M{"$exists": false}}, "Added %s to "+c.Name+".", "wasn't added", func(as *models.Asset, row *LibraryActionRow) error {
			switch {
			case as.Trash != nil:
				row.Outcome, row.Reason = OutcomeRefused, inTrashRefusal
				return nil
			case slices.Contains(as.CollectionIDs, c.ID):
				row.Outcome = OutcomeAlready
				return nil
			}
			if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$addToSet": bson.M{"collectionIds": c.ID}, "$set": bson.M{"updatedAt": time.Now().UTC()}}); err != nil {
				return err
			}
			a.Audit(ctx, "collection.add", "asset", as.ID, firstOr(as.ProjectIDs), map[string]any{"collection": c.ID, "name": c.Name})
			return nil
		})
		if err != nil {
			return nil, err
		}
		_ = a.DB.Update(ctx, store.CCollections, a.WorkspaceID(), c.ID, bson.M{"$set": bson.M{"updatedAt": time.Now().UTC()}})
		row := collectionRow(ctx, a, c.ID)
		out.Collection, out.Created = &row, created
		if created {
			out.Message = "Created the " + c.Name + " collection. " + out.Message
		}
		return out, nil
	})
	Def("collection.remove", "Take clips off a collection (nothing is deleted; a clip on nothing else goes back to the Inbox); refused while a project's latest cut uses a clip through a link to it", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CollectionItemsIn) (*LibraryActionOut, error) {
		c, err := resolveCollection(ctx, a, in.Collection)
		if err != nil {
			return nil, err
		}
		out, err := libraryAction(ctx, a, &LibraryActionIn{AssetIDs: in.AssetIDs}, bson.M{"collectionIds": c.ID}, "Took %s off "+c.Name+".", "wasn't taken off", func(as *models.Asset, row *LibraryActionRow) error {
			if !slices.Contains(as.CollectionIDs, c.ID) {
				row.Outcome = OutcomeAlready
				return nil
			}
			if why := linkedCutUse(ctx, a, c, as); why != "" {
				row.Outcome, row.Reason = OutcomeRefused, why
				return nil
			}
			if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$pull": bson.M{"collectionIds": c.ID}, "$set": bson.M{"updatedAt": time.Now().UTC()}}); err != nil {
				return err
			}
			a.Audit(ctx, "collection.remove", "asset", as.ID, firstOr(as.ProjectIDs), map[string]any{"collection": c.ID, "name": c.Name})
			return nil
		})
		if err != nil {
			return nil, err
		}
		row := collectionRow(ctx, a, c.ID)
		out.Collection = &row
		return out, nil
	})
	Def("collection.list", "List collections with how many clips each has and the projects that use them", Flags{}, func(ctx context.Context, a *app.App, _ *CollectionListIn) (*CollectionListOut, error) {
		rows, err := collectionRows(ctx, a, nil)
		if err != nil {
			return nil, err
		}
		return &CollectionListOut{Collections: rows}, nil
	})
	Def("collection.show", "Show a collection: its clips (where else each one is) and the projects that use it", Flags{}, func(ctx context.Context, a *app.App, in *CollectionShowIn) (*CollectionShowOut, error) {
		c, err := resolveCollection(ctx, a, in.Collection)
		if err != nil {
			return nil, err
		}
		lib, err := libraryList(ctx, a, &LibraryIn{Collection: c.ID, Limit: in.Limit})
		if err != nil {
			return nil, err
		}
		out := &CollectionShowOut{Collection: collectionRow(ctx, a, c.ID), Items: lib.Items, Links: []ProjectLinkRow{}, Summary: lib.Summary}
		var links []models.ProjectLink
		_ = a.DB.Find(ctx, store.CProjectLinks, a.WorkspaceID(), bson.M{"sourceId": c.ID}, &links)
		sort.Slice(links, func(i, j int) bool { return links[i].CreatedAt.Before(links[j].CreatedAt) })
		for _, l := range links {
			out.Links = append(out.Links, linkRow(ctx, a, l))
		}
		return out, nil
	})
	Def("collection.delete", "Delete a collection (never its clips: a clip on nothing else goes back to the Inbox; clips a project's cut uses through it are added to that project)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CollectionRefIn) (*CollectionDeleteOut, error) {
		c, err := resolveCollection(ctx, a, in.Collection)
		if err != nil {
			return nil, err
		}
		out := &CollectionDeleteOut{}
		// Projects that use it keep what their cuts use: those clips join the project.
		var links []models.ProjectLink
		_ = a.DB.Find(ctx, store.CProjectLinks, a.WorkspaceID(), bson.M{"sourceId": c.ID}, &links)
		var keptNotes []string
		for _, l := range links {
			used := usedFrom(ctx, a, l.ProjectID, c.ID)
			var clips []models.LinkItem
			for _, it := range used {
				if it.Kind == models.LinkFootage {
					clips = append(clips, it)
				}
			}
			for _, it := range clips {
				_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), it.ID, bsonAddProject(l.ProjectID))
			}
			if len(clips) > 0 {
				refreshProjectCounts(ctx, a, l.ProjectID)
				var p models.Project
				_ = a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), l.ProjectID, &p)
				keptNotes = append(keptNotes, fmt.Sprintf("%s's cut uses %s from it, so %s in %s now.", p.Name, plural(len(clips), "clip"), map[bool]string{true: "it is", false: "they are"}[len(clips) == 1], p.Name))
				out.Kept = append(out.Kept, itemRows(clips)...)
			}
			_, _ = a.DB.C(store.CProjectLinks).DeleteOne(ctx, bson.M{"_id": l.ID, "workspaceId": a.WorkspaceID()})
		}
		var on []models.Asset
		_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"collectionIds": c.ID}, &on, options.Find().SetProjection(bson.M{"_id": 1}))
		out.Clips = len(on)
		if _, err := a.DB.C(store.CAssets).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "collectionIds": c.ID}, bson.M{"$pull": bson.M{"collectionIds": c.ID}, "$set": bson.M{"updatedAt": time.Now().UTC()}}); err != nil {
			return nil, err
		}
		if len(on) > 0 {
			idsList := make([]string, 0, len(on))
			for _, as := range on {
				idsList = append(idsList, as.ID)
			}
			f := InboxFilter()
			f["workspaceId"], f["_id"] = a.WorkspaceID(), bson.M{"$in": idsList}
			n, _ := a.DB.C(store.CAssets).CountDocuments(ctx, f)
			out.InInbox = int(n)
		}
		if _, err := a.DB.C(store.CCollections).DeleteOne(ctx, bson.M{"_id": c.ID, "workspaceId": a.WorkspaceID()}); err != nil {
			return nil, err
		}
		a.Audit(ctx, "collection.delete", "collection", c.ID, "", map[string]any{"name": c.Name, "clips": out.Clips, "links": len(links)})
		out.Message = fmt.Sprintf("Deleted the %s collection. No clip was deleted", c.Name)
		switch {
		case out.Clips == 0:
			out.Message += "."
		case out.InInbox == out.Clips:
			out.Message += fmt.Sprintf(": its %s %s back in the Inbox.", plural(out.Clips, "clip"), map[bool]string{true: "is", false: "are"}[out.Clips == 1])
		case out.InInbox > 0:
			out.Message += fmt.Sprintf(": %d of its %s are back in the Inbox, the rest are still filed elsewhere.", out.InInbox, plural(out.Clips, "clip"))
		default:
			out.Message += fmt.Sprintf(": its %s are still in their projects or other collections.", plural(out.Clips, "clip"))
		}
		if len(keptNotes) > 0 {
			out.Message += " " + strings.Join(keptNotes, " ")
		}
		return out, nil
	})
}
