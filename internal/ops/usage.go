package ops

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/store"
)

// "Where it's used" (the Used in lists, asset.where, resources.where,
// cut.where): every place a clip, a resource or a project's cut shows up —
// the projects it is in, the projects that see it through a link, the cuts
// (and their older versions) that use it, the brand kit, and cuts that nest a
// project's cut. One implementation feeds the dashboard panel, the CLI and the
// delete check.

// Use kinds.
const (
	UseProject = "project" // it is in this project
	UseLinked  = "linked"  // a project sees it through a link
	UseCut     = "cut"     // a cut (a version of it) uses it
	UseBrand   = "brand"   // the brand kit uses it
	UseNested  = "nested"  // a cut plays this project's cut
)

// Where footage is filed (D96–D98).
const (
	UseCollection = "collection" // it is on this collection
	UseArchive    = "archive"    // it is in the Archive
	UseTrash      = "trash"      // it is in the Trash
)

// UseRow is one place something is used.
type UseRow struct {
	Kind        string `json:"kind" jsonschema:"enum=project,enum=linked,enum=cut,enum=brand,enum=nested,enum=collection,enum=archive,enum=trash"`
	ProjectID   string `json:"projectId,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
	// Versions are the versions of the project's cut that use it (newest
	// first); Current says the latest version does.
	Versions []int  `json:"versions,omitempty"`
	Current  bool   `json:"current,omitempty"`
	Branch   string `json:"branch,omitempty"`
	// RevisionID is the newest version that uses it (the link goes there).
	RevisionID string   `json:"revisionId,omitempty"`
	ClipIDs    []string `json:"clipIds,omitempty" jsonschema:"description=The clips (or graphics) of the newest such version that use it."`
	// From names the project a link brings it from.
	From string `json:"from,omitempty"`
	// Text says it in plain words; Link opens it in the dashboard.
	Text string `json:"text"`
	Link string `json:"link"`
}

// usesInCuts finds the revisions whose specs match a query (any branch, any
// version), grouped by project and branch, newest first.
func usesInCuts(ctx context.Context, a *app.App, filter bson.M, match func(s *editspec.Spec) []string, what string) ([]UseRow, error) {
	var revs []models.Revision
	if err := a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), filter, &revs, options.Find().SetSort(bson.D{{Key: "number", Value: -1}}).SetProjection(bson.M{"spec.notes": 0})); err != nil {
		return nil, err
	}
	type group struct {
		row  UseRow
		head string
	}
	groups := map[string]*group{}
	var order []string
	projects := map[string]*models.Project{}
	for i := range revs {
		r := &revs[i]
		if r.Detached {
			continue
		}
		clips := match(&r.Spec)
		if len(clips) == 0 {
			continue
		}
		p := projects[r.ProjectID]
		if p == nil {
			var pp models.Project
			if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), r.ProjectID, &pp) != nil {
				continue
			}
			p = &pp
			projects[r.ProjectID] = p
		}
		key := r.ProjectID + "/" + r.Branch
		g := groups[key]
		if g == nil {
			g = &group{row: UseRow{Kind: UseCut, ProjectID: p.ID, ProjectName: p.Name, Branch: r.Branch, RevisionID: r.ID, ClipIDs: clips}, head: p.Branches[r.Branch].Head}
			groups[key] = g
			order = append(order, key)
		}
		g.row.Versions = append(g.row.Versions, r.Number)
		if r.ID == g.head {
			g.row.Current = true
		}
	}
	var out []UseRow
	for _, k := range order {
		row := groups[k].row
		sort.Sort(sort.Reverse(sort.IntSlice(row.Versions)))
		cut := "cut"
		if row.Branch != "" && row.Branch != "main" {
			cut = row.Branch + " cut"
		}
		switch {
		case row.Current && len(row.Versions) == 1:
			row.Text = fmt.Sprintf("Used in %s's %s (version %d%s)", row.ProjectName, cut, row.Versions[0], clipsText(row.ClipIDs, what))
		case row.Current:
			row.Text = fmt.Sprintf("Used in %s's %s (version %d%s) and %s", row.ProjectName, cut, row.Versions[0], clipsText(row.ClipIDs, what), olderText(row.Versions[1:]))
		case len(row.Versions) == 1:
			row.Text = fmt.Sprintf("Used in an older version of %s's %s (version %d)", row.ProjectName, cut, row.Versions[0])
		default:
			row.Text = fmt.Sprintf("Used in older versions of %s's %s: %s", row.ProjectName, cut, intsText(row.Versions))
		}
		row.Link = fmt.Sprintf("/p/%s/r/%s", row.ProjectID, row.RevisionID)
		out = append(out, row)
	}
	return out, nil
}

// clipsText says how a cut uses something, in plain words (the cut's own
// clip and graphic names stay in UseRow.ClipIDs): ", twice", ", in 3 graphics",
// ", in the narration", ", as the brand font"; "" for a single use.
func clipsText(ids []string, what string) string {
	var parts []string
	n := 0
	for _, id := range ids {
		switch {
		case what == "clip" && id == "narration":
			parts = append(parts, "in the narration")
		case what == "graphic" && id == "brand font":
			parts = append(parts, "as the brand font")
		case what == "graphic" && id == "pinned":
		default:
			n++
		}
	}
	switch {
	case n > 1 && what == "clip":
		parts = append([]string{timesText(n)}, parts...)
	case n == 1 && what == "graphic":
		parts = append([]string{"in a graphic"}, parts...)
	case n > 1:
		parts = append([]string{fmt.Sprintf("in %d %ss", n, what)}, parts...)
	}
	if len(parts) == 0 {
		return ""
	}
	return ", " + strings.Join(parts, " and ")
}

func timesText(n int) string {
	if n == 2 {
		return "twice"
	}
	return fmt.Sprintf("%d times", n)
}

func intsText(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = fmt.Sprint(n)
	}
	if len(parts) > 1 {
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
	return strings.Join(parts, ", ")
}

// olderText says which older versions ("older version 1", "older versions 1 and 3").
func olderText(ns []int) string {
	if len(ns) == 1 {
		return fmt.Sprintf("older version %d", ns[0])
	}
	return "older versions " + intsText(ns)
}

// assetUses lists where footage is: its projects, its collections, the
// Archive or the Trash, projects that see it through links, and every cut
// version that plays it (clips, sounds, narration takes).
func assetUses(ctx context.Context, a *app.App, as *models.Asset) ([]UseRow, error) {
	var out []UseRow
	var ps []models.Project
	if len(as.ProjectIDs) > 0 {
		_ = a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": as.ProjectIDs}}, &ps)
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].Name < ps[j].Name })
	for _, p := range ps {
		out = append(out, UseRow{Kind: UseProject, ProjectID: p.ID, ProjectName: p.Name, Text: "In " + p.Name, Link: fmt.Sprintf("/p/%s?tab=sources&asset=%s", p.ID, as.ID)})
	}
	// Collections, the Archive and the Trash (D96–D98): the Library links them.
	colNames := map[string]string{}
	if len(as.CollectionIDs) > 0 {
		var cs []models.Collection
		_ = a.DB.Find(ctx, store.CCollections, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": as.CollectionIDs}}, &cs)
		sort.Slice(cs, func(i, j int) bool { return strings.ToLower(cs[i].Name) < strings.ToLower(cs[j].Name) })
		for _, c := range cs {
			colNames[c.ID] = c.Name
			out = append(out, UseRow{Kind: UseCollection, ProjectID: c.ID, ProjectName: c.Name, Text: "On the " + c.Name + " collection", Link: "/library?collection=" + c.ID + "&asset=" + as.ID})
		}
	}
	if as.ArchivedAt != nil {
		out = append(out, UseRow{Kind: UseArchive, Text: "In the Archive", Link: "/library?view=archived&asset=" + as.ID})
	}
	if as.Trash != nil {
		out = append(out, UseRow{Kind: UseTrash, Text: "In the Trash", Link: "/library?view=trash&asset=" + as.ID})
	}
	// Projects that use it through a link (to one of its projects or collections).
	if owners := append(append([]string{}, as.ProjectIDs...), as.CollectionIDs...); len(owners) > 0 {
		var links []models.ProjectLink
		_ = a.DB.Find(ctx, store.CProjectLinks, a.WorkspaceID(), bson.M{"sourceId": bson.M{"$in": owners}}, &links)
		people := projectlinks.PeopleIn(ctx, a, []string{as.ID})
		for _, l := range links {
			if slices.Contains(as.ProjectIDs, l.ProjectID) || !projectlinks.Includes(l, projectlinks.AssetItem(as, people[as.ID])) {
				continue
			}
			var p, from models.Project
			if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), l.ProjectID, &p) != nil {
				continue
			}
			fromName := colNames[l.SourceID]
			if fromName == "" {
				_ = a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), l.SourceID, &from)
				fromName = from.Name
			}
			text := fmt.Sprintf("Seen in %s through its link to %s", p.Name, fromName)
			if projectlinks.IsCollection(l.SourceID) {
				text = fmt.Sprintf("Seen in %s through its link to the %s collection", p.Name, fromName)
			}
			out = append(out, UseRow{Kind: UseLinked, ProjectID: p.ID, ProjectName: p.Name, From: fromName, Text: text,
				Link: fmt.Sprintf("/p/%s?tab=sources&asset=%s", p.ID, as.ID)})
		}
	}
	filter := bson.M{"$or": bson.A{bson.M{"spec.clips.assetId": as.ID}, bson.M{"spec.audio.assetId": as.ID}, bson.M{"spec.voiceover.beats.take.assetId": as.ID},
		bson.M{"spec.voiceover.beats.take.processed.assetId": as.ID}, bson.M{"spec.voiceover.continuous.assetId": as.ID}, bson.M{"spec.voiceover.continuous.processed.assetId": as.ID}}}
	cuts, err := usesInCuts(ctx, a, filter, func(s *editspec.Spec) []string {
		var ids []string
		for _, c := range s.Clips {
			if c.AssetID == as.ID {
				ids = append(ids, c.ID)
			}
		}
		for _, t := range s.Audio {
			if t.AssetID == as.ID {
				ids = append(ids, t.ID)
			}
		}
		if len(ids) == 0 && slices.Contains(editspec.AssetIDs(s), as.ID) {
			ids = append(ids, "narration")
		}
		return ids
	}, "clip")
	if err != nil {
		return nil, err
	}
	return append(out, cuts...), nil
}

// resourceUses lists where a resource is used: cut versions that pin it, and
// the brand of Content Settings (D193).
func resourceUses(ctx context.Context, a *app.App, r *models.Resource) ([]UseRow, error) {
	var out []UseRow
	var recs []models.ContentSettings
	_ = a.DB.Find(ctx, store.CContentSettings, a.WorkspaceID(), bson.M{"$or": bson.A{bson.M{"brandKit.logo": r.ID}, bson.M{"brandKit.logoDark": r.ID}, bson.M{"brandKit.fontResource": r.ID}}}, &recs)
	for _, rec := range recs {
		k := rec.BrandKit
		if k == nil {
			continue
		}
		var roles []string
		if k.Logo == r.ID {
			roles = append(roles, "its logo")
		}
		if k.LogoDark == r.ID {
			roles = append(roles, "its logo for dark backgrounds")
		}
		if k.FontResource == r.ID {
			roles = append(roles, "its font")
		}
		if len(roles) == 0 {
			continue
		}
		name := rec.ProjectID
		var p models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), rec.ProjectID, &p) == nil {
			name = p.Name
		}
		out = append(out, UseRow{Kind: UseBrand, Text: "The brand of “" + name + "”'s settings uses it as " + strings.Join(roles, " and "), Link: links.ContentSettings(rec.ProjectID, "brand")})
	}
	// MongoDB doesn't promise to test $exists before $expr, so the expression
	// itself copes with a cut that pins nothing (no resources field).
	pins := bson.M{"$objectToArray": bson.M{"$ifNull": bson.A{"$spec.resources", bson.M{}}}}
	filter := bson.M{"spec.resources": bson.M{"$exists": true}, "$expr": bson.M{"$in": bson.A{r.ID, bson.M{"$map": bson.M{"input": pins, "as": "x", "in": "$$x.v.id"}}}}}
	cuts, err := usesInCuts(ctx, a, filter, func(s *editspec.Spec) []string {
		var names []string
		for n, p := range s.Resources {
			if p.ID == r.ID {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			return nil
		}
		var users []string
		for _, u := range editspec.ResourceUses(s, nil) {
			if slices.Contains(names, u.Name) {
				if u.Font && u.Motion == "" {
					users = append(users, "brand font")
				} else if u.BrandLogo {
					if !slices.Contains(users, "brand logo") {
						users = append(users, "brand logo")
					}
				} else if !slices.Contains(users, u.Overlay) {
					users = append(users, u.Overlay)
				}
			}
		}
		if len(users) == 0 {
			users = []string{"pinned"}
		}
		return users
	}, "graphic")
	if err != nil {
		return nil, err
	}
	return append(out, cuts...), nil
}

var nestedKeyPrefix = regexp.QuoteMeta

// cutUses lists the cut versions that nest a project's cut.
func cutUses(ctx context.Context, a *app.App, projectID string) ([]UseRow, error) {
	prefix := editspec.NestedPrefix + projectID + "/"
	rows, err := usesInCuts(ctx, a, bson.M{"spec.clips.assetId": bson.M{"$regex": "^" + nestedKeyPrefix(prefix)}}, func(s *editspec.Spec) []string {
		var ids []string
		for _, c := range s.Clips {
			if strings.HasPrefix(c.AssetID, prefix) {
				ids = append(ids, c.ID)
			}
		}
		return ids
	}, "clip")
	for i := range rows {
		rows[i].Kind = UseNested
		rows[i].Text = strings.Replace(rows[i].Text, "Used in", "Played inside", 1)
	}
	return rows, err
}
