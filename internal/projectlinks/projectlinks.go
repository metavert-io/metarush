// Package projectlinks decides what a project can use from the projects it is
// linked to (D83). A link brings in, by default, everything the other project
// has — its footage, resources, approved motion graphics and cuts — marked
// "from <project>"; a saved filter (kinds, tags, people, linked folders) or a
// list of items narrows it. Nothing is copied: the same assets, resource
// versions and revisions are used. Links are not transitive: a project sees
// what its linked projects have of their own, not what they link in.
//
// A link's source can also be a collection (D96): a named shelf of footage,
// which a link treats like a project with footage and nothing else.
package projectlinks

import (
	"context"
	"slices"
	"sort"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// IsCollection reports whether a link source is a collection (D96).
func IsCollection(sourceID string) bool { return ids.Valid(ids.Collection, sourceID) }

// SourceFootage matches the footage a link source has: a project's clips, or
// what is on a collection. Clearer copies of its recordings (D88) belong to the
// cuts that play them, not to what a link brings in.
func SourceFootage(sourceID string) bson.M {
	notCopies := bson.M{"$ne": models.TagEnhanced}
	if IsCollection(sourceID) {
		return bson.M{"collectionIds": sourceID, "tags": notCopies}
	}
	return bson.M{"projectIds": sourceID, "tags": notCopies}
}

// View is what one project can see through its links.
type View struct {
	ProjectID string
	Links     []models.ProjectLink
	// Names are the projects' names (this one and every linked one), by ID.
	Names map[string]string
}

// Item describes a thing a link may bring in.
type Item struct {
	Kind string // footage | resources | motion | cuts
	ID   string
	// Owners are the projects it belongs to (an asset can be in several).
	Owners  []string
	Tags    []string
	People  []string // person IDs speaking in it (footage)
	Folder  string   // linked folder (resources)
	Removed bool
}

// Load reads a project's links (empty View for "").
func Load(ctx context.Context, a *app.App, projectID string) (*View, error) {
	v := &View{ProjectID: projectID, Names: map[string]string{}}
	if projectID == "" {
		return v, nil
	}
	if err := a.DB.Find(ctx, store.CProjectLinks, a.WorkspaceID(), bson.M{"projectId": projectID}, &v.Links); err != nil {
		return nil, err
	}
	sort.Slice(v.Links, func(i, j int) bool { return v.Links[i].CreatedAt.Before(v.Links[j].CreatedAt) })
	ids := []string{projectID}
	for _, l := range v.Links {
		ids = append(ids, l.SourceID)
	}
	var ps []models.Project
	if err := a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}}, &ps); err != nil {
		return nil, err
	}
	for _, p := range ps {
		v.Names[p.ID] = p.Name
	}
	var cs []models.Collection
	if len(ids) > 1 {
		if err := a.DB.Find(ctx, store.CCollections, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}}, &cs); err != nil {
			return nil, err
		}
	}
	for _, c := range cs {
		v.Names[c.ID] = c.Name
	}
	return v, nil
}

// SourceIDs are the linked projects (in link order).
func (v *View) SourceIDs() []string {
	if v == nil {
		return nil
	}
	out := make([]string, 0, len(v.Links))
	for _, l := range v.Links {
		out = append(out, l.SourceID)
	}
	return out
}

// Link returns the link to a source project (nil when there is none).
func (v *View) Link(sourceID string) *models.ProjectLink {
	if v == nil {
		return nil
	}
	for i := range v.Links {
		if v.Links[i].SourceID == sourceID {
			return &v.Links[i]
		}
	}
	return nil
}

// From says whether the project sees it and from where: "" for its own (or
// the workspace's), a source project's ID when a link brings it in. ok is
// false when the project can't see it.
func (v *View) From(it Item) (from string, ok bool) {
	if v == nil {
		return "", false
	}
	if slices.Contains(it.Owners, v.ProjectID) {
		return "", true
	}
	if it.Removed {
		return "", false
	}
	for _, l := range v.Links {
		if slices.Contains(it.Owners, l.SourceID) && Includes(l, it) {
			return l.SourceID, true
		}
	}
	return "", false
}

// Includes reports whether a link brings in an item of its source project.
func Includes(l models.ProjectLink, it Item) bool {
	for _, x := range l.Items {
		if x.Kind == it.Kind && x.ID == it.ID {
			return true
		}
	}
	if l.Kept {
		return false // only what cuts used stays after unlinking
	}
	if l.Only.Empty() {
		return len(l.Items) == 0 // the whole project, unless specific items narrow it
	}
	f := l.Only
	if len(f.Kinds) > 0 && !slices.Contains(f.Kinds, it.Kind) {
		return false
	}
	if len(f.Tags) > 0 && !intersects(f.Tags, it.Tags) {
		return false
	}
	if len(f.People) > 0 && !intersects(f.People, it.People) {
		return false
	}
	if len(f.Folders) > 0 && !slices.Contains(f.Folders, it.Folder) {
		return false
	}
	return true
}

func intersects(a, b []string) bool {
	for _, x := range a {
		if slices.Contains(b, x) {
			return true
		}
	}
	return false
}

// AssetItem describes footage for Includes: its projects and its
// collections own it.
func AssetItem(as *models.Asset, people []string) Item {
	owners := as.ProjectIDs
	if len(as.CollectionIDs) > 0 {
		owners = append(append([]string{}, as.ProjectIDs...), as.CollectionIDs...)
	}
	return Item{Kind: models.LinkFootage, ID: as.ID, Owners: owners, Tags: as.Tags, People: people}
}

// ResourceItem describes a resource for Includes (workspace resources are
// everyone's: callers treat them as their own).
func ResourceItem(r *models.Resource) Item {
	it := Item{Kind: models.LinkResources, ID: r.ID, Tags: r.AllTags(), Removed: r.Removed}
	if r.ProjectID != "" {
		it.Owners = []string{r.ProjectID}
	}
	if r.Folder != nil {
		it.Folder = r.Folder.FolderID
	}
	return it
}

// CutItem describes another project's cut for Includes.
func CutItem(projectID string) Item {
	return Item{Kind: models.LinkCuts, ID: projectID, Owners: []string{projectID}}
}

// MotionItem describes a motion graphic version (checked in a project folder).
func MotionItem(c *models.MotionComponent) Item {
	it := Item{Kind: models.LinkMotion, ID: c.Hash}
	if c.ProjectID != "" {
		it.Owners = []string{c.ProjectID}
	}
	return it
}

// PeopleIn returns the person IDs heard in each asset (named voices).
func PeopleIn(ctx context.Context, a *app.App, assetIDs []string) map[string][]string {
	out := map[string][]string{}
	if len(assetIDs) == 0 {
		return out
	}
	var voices []models.AssetSpeaker
	_ = a.DB.Find(ctx, store.CSpeakers, a.WorkspaceID(), bson.M{"assetId": bson.M{"$in": assetIDs}, "personId": bson.M{"$exists": true, "$ne": ""}}, &voices)
	for _, s := range voices {
		if !slices.Contains(out[s.AssetID], s.PersonID) {
			out[s.AssetID] = append(out[s.AssetID], s.PersonID)
		}
	}
	return out
}

// LinkedAssets lists the footage a project sees through its links, with the
// project each comes from (its own footage is not included).
func (v *View) LinkedAssets(ctx context.Context, a *app.App) ([]models.Asset, map[string]string, error) {
	from := map[string]string{}
	if v == nil || len(v.Links) == 0 {
		return nil, from, nil
	}
	srcs := v.SourceIDs()
	var list []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"$or": bson.A{bson.M{"projectIds": bson.M{"$in": srcs}}, bson.M{"collectionIds": bson.M{"$in": srcs}}},
		"state": models.AssetAvailable, "tags": bson.M{"$ne": models.TagEnhanced}}, &list); err != nil { // not clearer copies (D88)
		return nil, nil, err
	}
	ids := make([]string, 0, len(list))
	for _, as := range list {
		ids = append(ids, as.ID)
	}
	people := PeopleIn(ctx, a, ids)
	var out []models.Asset
	for i := range list {
		as := &list[i]
		if slices.Contains(as.ProjectIDs, v.ProjectID) {
			continue
		}
		if src, ok := v.From(AssetItem(as, people[as.ID])); ok && src != "" {
			from[as.ID] = src
			out = append(out, *as)
		}
	}
	return out, from, nil
}

// SeesAsset reports whether the project can use an asset (its own or linked).
func (v *View) SeesAsset(ctx context.Context, a *app.App, as *models.Asset) bool {
	if v == nil {
		return false
	}
	if slices.Contains(as.ProjectIDs, v.ProjectID) {
		return true
	}
	if len(v.Links) == 0 {
		return false
	}
	people := PeopleIn(ctx, a, []string{as.ID})
	_, ok := v.From(AssetItem(as, people[as.ID]))
	return ok
}

// SeesCut reports whether the project can use another project's cuts.
func (v *View) SeesCut(projectID string) bool {
	if v == nil {
		return false
	}
	_, ok := v.From(CutItem(projectID))
	return ok
}
