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
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/names"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/store"
)

// Project links (D83): "use the resources from the Brand project". A link
// brings in everything the other project has by default — footage,
// resources, approved motion graphics and its cut — or a saved subset (by
// kind, tag, person or linked folder, or specific items). Nothing is copied.

// LinkFilterIn narrows a link, in people's words.
type LinkFilterIn struct {
	Kinds   []string `json:"kinds,omitempty" jsonschema:"description=footage\\, resources\\, motion\\, cuts."`
	Tags    []string `json:"tags,omitempty"`
	People  []string `json:"people,omitempty" jsonschema:"description=People's names (footage where they speak)."`
	Folders []string `json:"folders,omitempty" jsonschema:"description=Linked folders of that project (names or IDs)."`
}

// ProjectLinkIn links a project to another (or to a collection, D96).
type ProjectLinkIn struct {
	ProjectID string        `json:"projectId" jsonschema:"description=The project that will use the other's things (ID or name)."`
	From      string        `json:"from" jsonschema:"description=The project or collection whose things it uses (ID or name)."`
	Only      *LinkFilterIn `json:"only,omitempty" jsonschema:"description=Only part of it (empty: everything)."`
	Items     []string      `json:"items,omitempty" jsonschema:"description=Specific things of that project: footage\\, resource or motion graphic names\\, or cut for its cut."`
}

// ProjectUnlinkIn removes a link.
type ProjectUnlinkIn struct {
	ProjectID string `json:"projectId"`
	From      string `json:"from"`
}

// ProjectLinksIn lists a project's links.
type ProjectLinksIn struct {
	ProjectID string `json:"projectId"`
}

// LinkItemRow is one specific thing a link brings in.
type LinkItemRow struct {
	Kind string `json:"kind" jsonschema:"enum=footage,enum=resources,enum=motion,enum=cuts"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// LinkCounts says how much a link brings in now.
type LinkCounts struct {
	Footage   int  `json:"footage"`
	Resources int  `json:"resources"`
	Motion    int  `json:"motion"`
	Cut       bool `json:"cut" jsonschema:"description=Its cut can be nested or copied in."`
}

// ProjectLinkRow is one link, in words.
type ProjectLinkRow struct {
	ID          string        `json:"id"`
	ProjectID   string        `json:"projectId"`
	ProjectName string        `json:"projectName"`
	SourceID    string        `json:"sourceId"`
	SourceName  string        `json:"sourceName"`
	SourceKind  string        `json:"sourceKind" jsonschema:"enum=project,enum=collection"` // a project or a collection (D96)
	Whole       bool          `json:"whole"`
	Only        *LinkFilterIn `json:"only,omitempty"`
	Items       []LinkItemRow `json:"items,omitempty"`
	Kept        bool          `json:"kept,omitempty" jsonschema:"description=The link was removed; what cuts here use stays."`
	Counts      LinkCounts    `json:"counts"`
	// Text says what it brings in ("Everything from Brand: 12 clips, 40 resources and its cut").
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
}

// ProjectLinkOut is a link after a change.
type ProjectLinkOut struct {
	Link    *ProjectLinkRow `json:"link,omitempty"`
	Message string          `json:"message"`
	// Kept lists what stays usable after unlinking because cuts here use it.
	Kept []LinkItemRow `json:"kept,omitempty"`
}

// ProjectLinksOut lists a project's links both ways.
type ProjectLinksOut struct {
	Links  []ProjectLinkRow `json:"links" jsonschema:"description=Projects this one uses."`
	UsedBy []ProjectLinkRow `json:"usedBy" jsonschema:"description=Projects that use this one."`
}

// ProjectUseIn adds specific things of another project.
type ProjectUseIn struct {
	ProjectID string   `json:"projectId"`
	From      string   `json:"from"`
	Items     []string `json:"items" jsonschema:"minItems=1,description=Footage\\, resource or motion graphic names of that project\\, or cut for its cut."`
}

// linkRow describes a link with what it brings in now.
func linkRow(ctx context.Context, a *app.App, l models.ProjectLink) ProjectLinkRow {
	var p models.Project
	_ = a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), l.ProjectID, &p)
	src := loadLinkSource(ctx, a, l.SourceID)
	row := ProjectLinkRow{ID: l.ID, ProjectID: l.ProjectID, ProjectName: p.Name, SourceID: l.SourceID, SourceName: src.Name, SourceKind: src.kind(), Whole: l.Whole, Kept: l.Kept, CreatedAt: l.CreatedAt}
	if l.Only != nil && !l.Only.Empty() {
		f := &LinkFilterIn{Kinds: l.Only.Kinds, Tags: l.Only.Tags, Folders: l.Only.Folders}
		for _, pid := range l.Only.People {
			var per models.Person
			if a.DB.GetByID(ctx, store.CPeople, a.WorkspaceID(), pid, &per) == nil {
				f.People = append(f.People, per.Name)
			} else {
				f.People = append(f.People, pid)
			}
		}
		row.Only = f
	}
	for _, it := range l.Items {
		row.Items = append(row.Items, LinkItemRow{Kind: it.Kind, ID: it.ID, Name: it.Name})
	}
	row.Counts = linkCounts(ctx, a, l, src.Project)
	var parts []string
	if row.Counts.Footage > 0 {
		parts = append(parts, plural(row.Counts.Footage, "clip"))
	}
	if row.Counts.Resources > 0 {
		parts = append(parts, plural(row.Counts.Resources, "resource"))
	}
	if row.Counts.Motion > 0 {
		parts = append(parts, plural(row.Counts.Motion, "motion graphic"))
	}
	if row.Counts.Cut {
		parts = append(parts, "its cut")
	}
	what := "nothing yet"
	if len(parts) > 0 {
		what = joinAnd(parts)
	}
	switch {
	case l.Kept:
		row.Text = fmt.Sprintf("Kept from %s because cuts here use them: %s", src.Name, what)
	case l.Whole && src.Project == nil:
		row.Text = fmt.Sprintf("Everything on %s: %s", src.Name, what) // a collection (D96)
	case l.Whole:
		row.Text = fmt.Sprintf("Everything from %s: %s", src.Name, what)
	default:
		row.Text = fmt.Sprintf("Part of %s: %s", src.Name, what)
	}
	return row
}

// linkCounts counts what a link brings in now.
func linkCounts(ctx context.Context, a *app.App, l models.ProjectLink, src *models.Project) LinkCounts {
	var c LinkCounts
	var assets []models.Asset
	f := projectlinks.SourceFootage(l.SourceID)
	f["state"] = models.AssetAvailable
	_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), f, &assets)
	idsList := make([]string, 0, len(assets))
	for _, as := range assets {
		idsList = append(idsList, as.ID)
	}
	people := projectlinks.PeopleIn(ctx, a, idsList)
	for i := range assets {
		if !slices.Contains(assets[i].ProjectIDs, l.ProjectID) && projectlinks.Includes(l, projectlinks.AssetItem(&assets[i], people[assets[i].ID])) {
			c.Footage++
		}
	}
	if projectlinks.IsCollection(l.SourceID) {
		return c // a collection holds footage only
	}
	var res []models.Resource
	_ = a.DB.Find(ctx, store.CResources, a.WorkspaceID(), bson.M{"scope": models.ScopeProject, "projectId": l.SourceID, "removed": bson.M{"$ne": true}}, &res)
	for i := range res {
		if projectlinks.Includes(l, projectlinks.ResourceItem(&res[i])) {
			c.Resources++
		}
	}
	var comps []models.MotionComponent
	_ = a.DB.Find(ctx, store.CMotion, a.WorkspaceID(), bson.M{"projectId": l.SourceID, "state": models.MotionApproved}, &comps)
	for i := range comps {
		if projectlinks.Includes(l, projectlinks.MotionItem(&comps[i])) {
			c.Motion++
		}
	}
	if src != nil && src.Branches["main"].Head != "" && projectlinks.Includes(l, projectlinks.CutItem(l.SourceID)) {
		c.Cut = true
	}
	return c
}

// resolveLinkFilter turns people's names and folder names into IDs.
func resolveLinkFilter(ctx context.Context, a *app.App, src *linkSource, in *LinkFilterIn) (*models.LinkFilter, error) {
	if in == nil {
		return nil, nil
	}
	if src.Project == nil {
		for _, k := range in.Kinds {
			if k = strings.ToLower(strings.TrimSpace(k)); k != "" && k != models.LinkFootage && k != "clips" && k != "clip" && k != "video" && k != "videos" {
				return nil, apperr.New(apperr.InvalidInput, "%s is a collection: it holds footage only.", src.Name).WithHint("Leave out kinds, or say footage.")
			}
		}
		if len(in.Folders) > 0 {
			return nil, apperr.New(apperr.InvalidInput, "%s is a collection: it has no linked folders.", src.Name)
		}
	}
	f := &models.LinkFilter{}
	for _, k := range in.Kinds {
		k = strings.ToLower(strings.TrimSpace(k))
		switch k {
		case "clips", "clip", "video", "videos":
			k = models.LinkFootage
		case "resource", "images", "pictures", "logos":
			k = models.LinkResources
		case "motion graphics", "graphics":
			k = models.LinkMotion
		case "cut":
			k = models.LinkCuts
		}
		if !slices.Contains([]string{models.LinkFootage, models.LinkResources, models.LinkMotion, models.LinkCuts}, k) {
			return nil, apperr.New(apperr.InvalidInput, "%q isn't a kind of thing a link brings in.", k).WithHint("Kinds are footage, resources, motion and cuts.")
		}
		if !slices.Contains(f.Kinds, k) {
			f.Kinds = append(f.Kinds, k)
		}
	}
	for _, t := range in.Tags {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" && !slices.Contains(f.Tags, t) {
			f.Tags = append(f.Tags, t)
		}
	}
	if len(in.People) > 0 {
		var people []models.Person
		_ = a.DB.Find(ctx, store.CPeople, a.WorkspaceID(), bson.M{}, &people)
		cands := make([]names.Candidate, 0, len(people))
		for _, p := range people {
			cands = append(cands, names.Candidate{ID: p.ID, Name: p.Name})
		}
		for _, q := range in.People {
			if ids.Valid(ids.Person, q) {
				f.People = append(f.People, q)
				continue
			}
			res := names.Resolve(q, cands)
			switch {
			case res.Found != nil:
				f.People = append(f.People, res.Found.ID)
			case len(res.Ambiguous) > 0:
				return nil, apperr.New(apperr.InvalidInput, "%q could be %s.", q, names.Quote(res.Ambiguous))
			default:
				return nil, apperr.New(apperr.NotFound, "Nobody called %q has been named yet.", q).WithHint("Name people on the People page.").WithCommand("metarush people list")
			}
		}
	}
	if len(in.Folders) > 0 {
		var folders []models.LinkedFolder
		_ = a.DB.Find(ctx, store.CLinkedFolders, a.WorkspaceID(), bson.M{"projectId": src.ID}, &folders)
		for _, q := range in.Folders {
			found := ""
			for _, fd := range folders {
				if fd.ID == q || strings.EqualFold(fd.Name, q) {
					found = fd.ID
				}
			}
			if found == "" {
				return nil, apperr.New(apperr.NotFound, "%s has no linked folder called %q.", src.Name, q)
			}
			f.Folders = append(f.Folders, found)
		}
	}
	if f.Empty() {
		return nil, nil
	}
	return f, nil
}

// resolveLinkItems finds things of the source project (or collection) by name.
func resolveLinkItems(ctx context.Context, a *app.App, src *linkSource, refs []string) ([]models.LinkItem, error) {
	var out []models.LinkItem
	add := func(it models.LinkItem) {
		for _, x := range out {
			if x.Kind == it.Kind && x.ID == it.ID {
				return
			}
		}
		out = append(out, it)
	}
	var assets []models.Asset
	_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), projectlinks.SourceFootage(src.ID), &assets)
	var res []models.Resource
	var comps []models.MotionComponent
	if src.Project != nil {
		_ = a.DB.Find(ctx, store.CResources, a.WorkspaceID(), bson.M{"scope": models.ScopeProject, "projectId": src.ID, "removed": bson.M{"$ne": true}}, &res)
		_ = a.DB.Find(ctx, store.CMotion, a.WorkspaceID(), bson.M{"projectId": src.ID}, &comps)
	}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		low := strings.ToLower(ref)
		if (low == "cut" || low == "the cut" || low == "cuts") && src.Project != nil {
			if src.Project.Branches["main"].Head == "" {
				return nil, apperr.New(apperr.NotFound, "%s has no cut yet.", src.Name)
			}
			add(models.LinkItem{Kind: models.LinkCuts, ID: src.ID, Name: "the cut"})
			continue
		}
		matched := false
		for _, as := range assets {
			if as.ID == ref || strings.EqualFold(as.DisplayTitle(), ref) || strings.EqualFold(as.Name, ref) {
				add(models.LinkItem{Kind: models.LinkFootage, ID: as.ID, Name: as.DisplayTitle()})
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		for _, r := range res {
			if r.ID == ref || r.Name == low || strings.EqualFold(r.Title, ref) {
				add(models.LinkItem{Kind: models.LinkResources, ID: r.ID, Name: r.Title})
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		for _, c := range comps {
			if c.Hash == ref || c.Name == low {
				add(models.LinkItem{Kind: models.LinkMotion, ID: c.Hash, Name: c.Name})
				matched = true
			}
		}
		if !matched && src.Project == nil {
			return nil, apperr.New(apperr.NotFound, "The %s collection has no clip called %q.", src.Name, ref).WithHint("The collection in the Library shows what is on it.").WithCommand("metarush collection show " + src.ID)
		}
		if !matched {
			return nil, apperr.New(apperr.NotFound, "%s has nothing called %q.", src.Name, ref).WithHint("Name a clip, a resource or a motion graphic of it, or say cut for its cut; the project's page shows what it has.").
				WithCommand("metarush project show " + src.ID + " && metarush resources list --project " + src.ID)
		}
	}
	return out, nil
}

// linkSource is what a link brings things in from: a project, or a
// collection (D96), which is like a project with footage and nothing else.
type linkSource struct {
	ID, Name string
	Project  *models.Project // nil for a collection
}

func (s *linkSource) kind() string {
	if s.Project == nil {
		return "collection"
	}
	return "project"
}

// loadLinkSource reads a link's source by ID (an empty name when it is gone).
func loadLinkSource(ctx context.Context, a *app.App, id string) *linkSource {
	if projectlinks.IsCollection(id) {
		var c models.Collection
		_ = a.DB.GetByID(ctx, store.CCollections, a.WorkspaceID(), id, &c)
		return &linkSource{ID: id, Name: c.Name}
	}
	var p models.Project
	_ = a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), id, &p)
	return &linkSource{ID: id, Name: p.Name, Project: &p}
}

// resolveLinkSource finds what a project would use: a project or a
// collection, by ID or name (an exact name first; a project and a collection
// with the same name need the ID).
func resolveLinkSource(ctx context.Context, a *app.App, ref string) (*linkSource, error) {
	ref = strings.TrimSpace(ref)
	if ids.Valid(ids.Collection, ref) {
		c, err := resolveCollection(ctx, a, ref)
		if err != nil {
			return nil, err
		}
		return &linkSource{ID: c.ID, Name: c.Name}, nil
	}
	if !ids.Valid(ids.Project, ref) && ref != "" {
		var c models.Collection
		colErr := a.DB.C(store.CCollections).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "key": collectionKey(ref)}).Decode(&c)
		var exact []models.Project
		_ = a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), bson.M{"name": bson.M{"$regex": "^\\s*" + regexpQuote(ref) + "\\s*$", "$options": "i"}}, &exact)
		switch {
		case colErr == nil && len(exact) > 0:
			return nil, apperr.New(apperr.InvalidInput, "Both a project and a collection are called %q.", ref).WithHint("Choose the one you mean in the dashboard, or rename one of them.").
				WithCommand("metarush project list --json && metarush collection list --json")
		case colErr == nil:
			return &linkSource{ID: c.ID, Name: c.Name}, nil
		}
	}
	p, err := resolveProject(ctx, a, ref)
	if err != nil {
		if apperr.Is(err, apperr.NotFound) {
			if c, cerr := resolveCollection(ctx, a, ref); cerr == nil {
				return &linkSource{ID: c.ID, Name: c.Name}, nil
			}
		}
		return nil, err
	}
	return &linkSource{ID: p.ID, Name: p.Name, Project: p}, nil
}

func linkPair(ctx context.Context, a *app.App, projectRef, fromRef string) (*models.Project, *linkSource, error) {
	p, err := resolveProject(ctx, a, projectRef)
	if err != nil {
		return nil, nil, err
	}
	src, err := resolveLinkSource(ctx, a, fromRef)
	if err != nil {
		return nil, nil, err
	}
	if p.ID == src.ID {
		return nil, nil, apperr.New(apperr.InvalidInput, "A project can't use itself.")
	}
	return p, src, nil
}

func getLink(ctx context.Context, a *app.App, projectID, sourceID string) (*models.ProjectLink, error) {
	var l models.ProjectLink
	err := a.DB.C(store.CProjectLinks).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectId": projectID, "sourceId": sourceID}).Decode(&l)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// saveLink upserts a project's link to a source.
func saveLink(ctx context.Context, a *app.App, l *models.ProjectLink) error {
	now := time.Now().UTC()
	l.UpdatedAt = now
	if l.ID == "" {
		l.ID, l.WorkspaceID, l.CreatedAt, l.CreatedBy = ids.New(ids.ProjectLink), a.WorkspaceID(), now, a.Actor
		err := a.DB.Insert(ctx, store.CProjectLinks, *l)
		if store.IsDuplicateKey(err) {
			cur, gerr := getLink(ctx, a, l.ProjectID, l.SourceID)
			if gerr != nil {
				return err
			}
			l.ID, l.CreatedAt, l.CreatedBy = cur.ID, cur.CreatedAt, cur.CreatedBy
			return a.DB.Update(ctx, store.CProjectLinks, a.WorkspaceID(), l.ID, bson.M{"$set": *l})
		}
		return err
	}
	return a.DB.Update(ctx, store.CProjectLinks, a.WorkspaceID(), l.ID, bson.M{"$set": *l})
}

// usedFrom lists what the project's cuts (any version) use from a source project.
func usedFrom(ctx context.Context, a *app.App, projectID, sourceID string) []models.LinkItem {
	var revs []models.Revision
	_ = a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), bson.M{"projectId": projectID}, &revs)
	assetSet, resSet, motionSet := map[string]bool{}, map[string]bool{}, map[string]bool{}
	nested := false
	for _, r := range revs {
		for _, id := range editspec.AssetIDs(&r.Spec) {
			assetSet[id] = true
		}
		for _, id := range editspec.ResourceIDs(&r.Spec) {
			resSet[id] = true
		}
		for _, h := range editspec.MotionHashes(&r.Spec) {
			motionSet[h] = true
		}
		for _, k := range editspec.NestedKeys(&r.Spec) {
			if pid, _, _ := editspec.NestedRef(k); pid == sourceID {
				nested = true
			}
		}
	}
	var out []models.LinkItem
	if len(assetSet) > 0 {
		var assets []models.Asset
		f := projectlinks.SourceFootage(sourceID)
		f["_id"] = bson.M{"$in": keys(assetSet)}
		_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), f, &assets)
		for _, as := range assets {
			if !slices.Contains(as.ProjectIDs, projectID) {
				out = append(out, models.LinkItem{Kind: models.LinkFootage, ID: as.ID, Name: as.DisplayTitle()})
			}
		}
	}
	if len(resSet) > 0 {
		var res []models.Resource
		_ = a.DB.Find(ctx, store.CResources, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": keys(resSet)}, "scope": models.ScopeProject, "projectId": sourceID}, &res)
		for _, r := range res {
			out = append(out, models.LinkItem{Kind: models.LinkResources, ID: r.ID, Name: r.Title})
		}
	}
	if len(motionSet) > 0 {
		var comps []models.MotionComponent
		_ = a.DB.Find(ctx, store.CMotion, a.WorkspaceID(), bson.M{"hash": bson.M{"$in": keys(motionSet)}, "projectId": sourceID}, &comps)
		for _, c := range comps {
			out = append(out, models.LinkItem{Kind: models.LinkMotion, ID: c.Hash, Name: c.Name})
		}
	}
	if nested {
		out = append(out, models.LinkItem{Kind: models.LinkCuts, ID: sourceID, Name: "the cut"})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func itemRows(items []models.LinkItem) []LinkItemRow {
	out := make([]LinkItemRow, 0, len(items))
	for _, it := range items {
		out = append(out, LinkItemRow{Kind: it.Kind, ID: it.ID, Name: it.Name})
	}
	return out
}

func itemNames(items []models.LinkItem) string {
	var ns []string
	for _, it := range items {
		n := it.Name
		if it.Kind == models.LinkCuts {
			n = "its cut"
		}
		ns = append(ns, n)
	}
	return joinAnd(ns)
}

func init() {
	Def("project.link", "Let a project use another project's things — by default everything: its footage, resources, approved motion graphics and cut (nothing is copied) — or a subset", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *ProjectLinkIn) (*ProjectLinkOut, error) {
		p, src, err := linkPair(ctx, a, in.ProjectID, in.From)
		if err != nil {
			return nil, err
		}
		if err := carryLink(ctx, a, p, src); err != nil { // What may leave (D289)
			return nil, err
		}
		only, err := resolveLinkFilter(ctx, a, src, in.Only)
		if err != nil {
			return nil, err
		}
		items, err := resolveLinkItems(ctx, a, src, in.Items)
		if err != nil {
			return nil, err
		}
		l := &models.ProjectLink{ProjectID: p.ID, SourceID: src.ID}
		if cur, err := getLink(ctx, a, p.ID, src.ID); err == nil {
			l = cur
		}
		l.Only, l.Items, l.Kept = only, items, false
		l.Whole = only == nil && len(items) == 0
		if err := saveLink(ctx, a, l); err != nil {
			return nil, err
		}
		a.Audit(ctx, "project.link", "project", p.ID, p.ID, map[string]any{"from": src.ID, "whole": l.Whole, "items": len(items)})
		row := linkRow(ctx, a, *l)
		msg := fmt.Sprintf("%s uses everything from %s now (%s). Nothing was copied.", p.Name, src.Name, strings.TrimPrefix(row.Text, "Everything from "+src.Name+": "))
		if src.Project == nil && l.Whole {
			msg = fmt.Sprintf("%s uses everything on the %s collection now (%s); clips put on it later show up too. Nothing was copied.", p.Name, src.Name, strings.TrimPrefix(row.Text, "Everything on "+src.Name+": "))
		}
		if !l.Whole {
			msg = fmt.Sprintf("%s uses part of %s now: %s.", p.Name, src.Name, strings.TrimPrefix(row.Text, "Part of "+src.Name+": "))
		}
		return &ProjectLinkOut{Link: &row, Message: msg}, nil
	})
	Def("project.use", "Use specific things of another project in this one (footage, resources, motion graphics or its cut), without linking all of it", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *ProjectUseIn) (*ProjectLinkOut, error) {
		p, src, err := linkPair(ctx, a, in.ProjectID, in.From)
		if err != nil {
			return nil, err
		}
		if err := carryLink(ctx, a, p, src); err != nil { // What may leave (D289)
			return nil, err
		}
		items, err := resolveLinkItems(ctx, a, src, in.Items)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			return nil, apperr.New(apperr.InvalidInput, "Name what to use from %s.", src.Name)
		}
		l := &models.ProjectLink{ProjectID: p.ID, SourceID: src.ID}
		if cur, err := getLink(ctx, a, p.ID, src.ID); err == nil {
			l = cur
		}
		if l.Whole && !l.Kept {
			row := linkRow(ctx, a, *l)
			return &ProjectLinkOut{Link: &row, Message: fmt.Sprintf("%s already uses everything from %s.", p.Name, src.Name)}, nil
		}
		for _, it := range items {
			if !slices.ContainsFunc(l.Items, func(x models.LinkItem) bool { return x.Kind == it.Kind && x.ID == it.ID }) {
				l.Items = append(l.Items, it)
			}
		}
		l.Kept = false
		if err := saveLink(ctx, a, l); err != nil {
			return nil, err
		}
		a.Audit(ctx, "project.use", "project", p.ID, p.ID, map[string]any{"from": src.ID, "items": len(items)})
		row := linkRow(ctx, a, *l)
		return &ProjectLinkOut{Link: &row, Message: fmt.Sprintf("Using %s from %s.", itemNames(items), src.Name)}, nil
	})
	Def("project.unlink", "Stop using another project's things; whatever this project's cuts already use stays usable (and the answer says what)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *ProjectUnlinkIn) (*ProjectLinkOut, error) {
		p, src, err := linkPair(ctx, a, in.ProjectID, in.From)
		if err != nil {
			return nil, err
		}
		l, err := getLink(ctx, a, p.ID, src.ID)
		if err != nil {
			return nil, apperr.New(apperr.NotFound, "%s doesn't use %s.", p.Name, src.Name)
		}
		kept := usedFrom(ctx, a, p.ID, src.ID)
		if len(kept) == 0 {
			if _, err := a.DB.C(store.CProjectLinks).DeleteOne(ctx, bson.M{"_id": l.ID, "workspaceId": a.WorkspaceID()}); err != nil {
				return nil, err
			}
			a.Audit(ctx, "project.unlink", "project", p.ID, p.ID, map[string]any{"from": src.ID})
			return &ProjectLinkOut{Message: fmt.Sprintf("%s no longer uses %s.", p.Name, src.Name)}, nil
		}
		l.Only, l.Items, l.Whole, l.Kept = nil, kept, false, true
		if err := saveLink(ctx, a, l); err != nil {
			return nil, err
		}
		a.Audit(ctx, "project.unlink", "project", p.ID, p.ID, map[string]any{"from": src.ID, "kept": len(kept)})
		row := linkRow(ctx, a, *l)
		return &ProjectLinkOut{Link: &row, Kept: itemRows(kept), Message: fmt.Sprintf("%s no longer uses %s. Kept %s, because cuts here use them.", p.Name, src.Name, itemNames(kept))}, nil
	})
	Def("project.links", "List the projects a project uses (and the projects that use it)", Flags{}, func(ctx context.Context, a *app.App, in *ProjectLinksIn) (*ProjectLinksOut, error) {
		p, err := resolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, err
		}
		out := &ProjectLinksOut{Links: []ProjectLinkRow{}, UsedBy: []ProjectLinkRow{}}
		var ls []models.ProjectLink
		if err := a.DB.Find(ctx, store.CProjectLinks, a.WorkspaceID(), bson.M{"$or": bson.A{bson.M{"projectId": p.ID}, bson.M{"sourceId": p.ID}}}, &ls); err != nil {
			return nil, err
		}
		sort.Slice(ls, func(i, j int) bool { return ls[i].CreatedAt.Before(ls[j].CreatedAt) })
		for _, l := range ls {
			if l.ProjectID == p.ID {
				out.Links = append(out.Links, linkRow(ctx, a, l))
			} else {
				out.UsedBy = append(out.UsedBy, linkRow(ctx, a, l))
			}
		}
		return out, nil
	})
}

// carryLink asks What may leave about a project using another's things (or
// a collection's): they follow both projects' rules, and putting them where
// looser rules apply takes a person who is an owner (D289).
func carryLink(ctx context.Context, a *app.App, p *models.Project, src *linkSource) error {
	before := []string{src.ID}
	if !projectlinks.IsCollection(src.ID) {
		return leaveCarry(ctx, a, before, before, p.ID, "“"+src.Name+"”'s things")
	}
	before = nil
	var rows []models.Asset
	_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"collectionIds": src.ID}, &rows, options.Find().SetProjection(bson.M{"projectIds": 1, "trash": 1}).SetLimit(5000))
	for i := range rows {
		for _, pid := range assetProjects(&rows[i]) {
			if !slices.Contains(before, pid) {
				before = append(before, pid)
			}
		}
	}
	if len(before) == 0 {
		before = []string{""}
	}
	return leaveCarry(ctx, a, before, before, p.ID, "the clips of “"+src.Name+"”")
}
