package ops

import (
	"context"
	"slices"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcripts"
)

// Resources (D82) and nested cuts (D84) as validation, previews and renders
// see them. dbFacts answers editspec.ResourceFacts and editspec.NestedFacts
// from what loadFacts read.

// resourceFacts are the resources a spec pins, with what the project can see.
type resourceFacts struct {
	byID map[string]*models.Resource
	view *projectlinks.View
	// project is the project the spec belongs to ("" when unknown: renders).
	project string
	// kit are the resources of the brand the project uses (D193): its cuts
	// may pin them wherever they live.
	kit map[string]bool
}

func loadResourceFacts(ctx context.Context, a *app.App, projectID string, view *projectlinks.View, s *editspec.Spec) (*resourceFacts, error) {
	rf := &resourceFacts{byID: map[string]*models.Resource{}, view: view, project: projectID}
	idsList := editspec.ResourceIDs(s)
	if len(idsList) == 0 {
		return rf, nil
	}
	var list []models.Resource
	if err := a.DB.Find(ctx, store.CResources, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": idsList}}, &list); err != nil {
		return nil, err
	}
	for i := range list {
		rf.byID[list[i].ID] = &list[i]
	}
	if projectID != "" {
		rf.kit = kitResourceIDs(ctx, a, projectID)
	}
	return rf, nil
}

// info turns a stored resource version into editspec's view of it.
func (rf *resourceFacts) info(id, sha string) (editspec.ResourceInfo, bool) {
	if rf == nil {
		return editspec.ResourceInfo{}, false
	}
	r, ok := rf.byID[id]
	if !ok {
		return editspec.ResourceInfo{}, false
	}
	ri := editspec.ResourceInfo{ID: r.ID, Name: r.Name, Title: r.Title, Kind: r.Kind, SHA256: sha, Removed: r.Removed}
	if l := r.Latest(); l != nil {
		ri.Latest = l.SHA256
	}
	v := r.Version(sha)
	if v == nil {
		return ri, true
	}
	ri.Known = true
	ri.MIME, ri.Width, ri.Height, ri.Family = v.MIME, v.Width, v.Height, v.Family
	if v.Site != nil {
		ri.URL = v.Site.URL
	}
	for _, p := range v.Pages {
		ri.Pages = append(ri.Pages, editspec.PlanResourcePage{Number: p.Number, SHA256: p.SHA256, Width: p.Width, Height: p.Height})
	}
	ri.Available = v.Stored || len(v.Holders) > 0
	switch {
	case rf.project == "":
		ri.Visible = true // renders: the cut was checked when it was saved
	case r.Scope == models.ScopeWorkspace, r.ProjectID == rf.project, rf.kit[r.ID]:
		ri.Visible = true
	default:
		_, ri.Visible = rf.view.From(projectlinks.ResourceItem(r))
	}
	return ri, true
}

// Resource implements editspec.ResourceFacts.
func (f *dbFacts) Resource(id, sha string) (editspec.ResourceInfo, bool) {
	return f.res.info(id, sha)
}

// nestedFacts are the cuts a spec nests, loaded with their own facts.
type nestedFacts struct {
	byKey map[string]*editspec.NestedSource
}

// Nested implements editspec.NestedFacts.
func (f *dbFacts) Nested(projectID, revisionID string) (*editspec.NestedSource, bool) {
	if f.nested == nil {
		return nil, false
	}
	ns, ok := f.nested.byKey[editspec.NestedKey(projectID, revisionID)]
	return ns, ok
}

// loadNested reads every cut a spec nests (and, inside them, theirs, up to
// editspec.MaxNestDepth), with their facts, visibility and newer versions.
// outer is the project the spec belongs to ("" for renders: visibility was
// checked when the cut was saved; cycles are still refused).
func loadNested(ctx context.Context, a *app.App, outer string, view *projectlinks.View, s *editspec.Spec, depth int, chain []string) (*nestedFacts, error) {
	keys := editspec.NestedKeys(s)
	if len(keys) == 0 {
		return nil, nil
	}
	nf := &nestedFacts{byKey: map[string]*editspec.NestedSource{}}
	for _, k := range keys {
		pid, rid, ok := editspec.NestedRef(k)
		if !ok {
			continue
		}
		ns := &editspec.NestedSource{ProjectID: pid, RevisionID: rid}
		nf.byKey[k] = ns
		var rev models.Revision
		if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &rev); err != nil || rev.ProjectID != pid {
			ns.Missing = true
			continue
		}
		var p models.Project
		if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), pid, &p); err != nil {
			ns.Missing = true
			continue
		}
		ns.ProjectName, ns.Title, ns.Number = p.Name, rev.Spec.Title, rev.Number
		if h := p.Branches[rev.Branch].Head; h != "" {
			ns.LatestID = h
			if h == rev.ID {
				ns.LatestNumber = rev.Number
			} else {
				var head models.Revision
				if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), h, &head) == nil {
					ns.LatestNumber = head.Number
				}
			}
		}
		// A video can't contain itself: refuse a cut of this project, or one that
		// has this project (or one of the cuts it sits in) inside it.
		if slices.Contains(chain, pid) || (outer != "" && pid == outer) || nestsAny(ctx, a, &rev.Spec, append(append([]string{}, chain...), outer), 0) {
			ns.Cycle = true
			continue
		}
		ns.Visible = outer == "" || view.SeesCut(pid) || assumed(ctx, pid)
		if depth+1 > editspec.MaxNestDepth {
			ns.Spec = &rev.Spec
			continue
		}
		innerView, err := projectlinks.Load(ctx, a, pid)
		if err != nil {
			return nil, err
		}
		inner, err := loadFactsWith(ctx, a, pid, innerView, &rev.Spec, depth+1, append(append([]string{}, chain...), pid), transcripts.PinsOf(&rev))
		if err != nil {
			return nil, err
		}
		inner.res.project = "" // the nested cut was checked in its own project
		ns.Spec, ns.Facts = &rev.Spec, inner
	}
	return nf, nil
}

// nestsAny reports whether a spec nests (at any depth) a cut of one of the
// projects.
func nestsAny(ctx context.Context, a *app.App, s *editspec.Spec, projects []string, depth int) bool {
	if depth > editspec.MaxNestDepth+1 {
		return false
	}
	for _, k := range editspec.NestedKeys(s) {
		pid, rid, ok := editspec.NestedRef(k)
		if !ok {
			continue
		}
		for _, p := range projects {
			if p != "" && p == pid {
				return true
			}
		}
		var rev models.Revision
		if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &rev) == nil && nestsAny(ctx, a, &rev.Spec, projects, depth+1) {
			return true
		}
	}
	return false
}

// assumedKey carries things a dry run treats as linked: footage and other
// projects' cuts that saving the change links first (cut copy-in, cut nest),
// so the check judges the plan as it will be saved.
type assumedKey struct{}

func withAssumed(ctx context.Context, ids ...string) context.Context {
	set := map[string]bool{}
	if cur, ok := ctx.Value(assumedKey{}).(map[string]bool); ok {
		for k := range cur {
			set[k] = true
		}
	}
	for _, id := range ids {
		set[id] = true
	}
	return context.WithValue(ctx, assumedKey{}, set)
}

func assumed(ctx context.Context, id string) bool {
	set, _ := ctx.Value(assumedKey{}).(map[string]bool)
	return set[id]
}

// ---- cut sheets ----

// project finds the sheet's project (its project line, or its base revision's).
func sheetProjectID(ctx context.Context, a *app.App, text string) string {
	parsed, _ := cutsheet.Parse(text, cutsheet.ParseOptions{})
	if parsed == nil {
		return ""
	}
	if parsed.ProjectID != "" {
		return parsed.ProjectID
	}
	if parsed.BaseRevisionID != "" {
		var r models.Revision
		if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), parsed.BaseRevisionID, &r) == nil {
			return r.ProjectID
		}
	}
	return ""
}

// newSheetInfoFor is sheet info for reading a sheet: it knows the sheet's
// project, so resources and cuts named without a pin can be looked up.
func newSheetInfoFor(ctx context.Context, a *app.App, text string) *sheetInfo {
	info := newSheetInfo(ctx, a)
	info.project = sheetProjectID(ctx, a, text)
	return info
}

func (s *sheetInfo) linkView() *projectlinks.View {
	if s.view == nil {
		v, err := projectlinks.Load(s.ctx, s.a, s.project)
		if err != nil {
			v = &projectlinks.View{ProjectID: s.project, Names: map[string]string{}}
		}
		s.view = v
	}
	return s.view
}

// ResourceDetails implements cutsheet.ResourceLookup.
func (s *sheetInfo) ResourceDetails(id, sha string) (cutsheet.ResourceDetails, bool) {
	var r models.Resource
	if !ids.Valid(ids.Resource, id) || s.a.DB.GetByID(s.ctx, store.CResources, s.a.WorkspaceID(), id, &r) != nil {
		return cutsheet.ResourceDetails{}, false
	}
	d := cutsheet.ResourceDetails{Title: r.Title, Kind: r.Kind}
	if l := r.Latest(); l != nil && l.SHA256 != sha && r.Version(sha) != nil {
		d.Newer = true
	}
	if r.Scope == models.ScopeProject && s.project != "" && r.ProjectID != s.project {
		d.From = s.linkView().Names[r.ProjectID]
		if d.From == "" {
			var p models.Project
			if s.a.DB.GetByID(s.ctx, store.CProjects, s.a.WorkspaceID(), r.ProjectID, &p) == nil {
				d.From = p.Name
			}
		}
	}
	return d, true
}

// ResourceByName implements cutsheet.ResourceLookup: a name the sheet uses
// without a pin, as the project sees it, at the version the library has now.
func (s *sheetInfo) ResourceByName(name string) (editspec.ResourcePin, bool) {
	e, err := resources.Find(s.ctx, s.a, s.project, s.linkView(), name)
	if err != nil || e.Resource.Removed {
		return editspec.ResourcePin{}, false
	}
	v := e.Resource.Latest()
	if v == nil {
		return editspec.ResourcePin{}, false
	}
	return editspec.ResourcePin{ID: e.Resource.ID, SHA256: v.SHA256}, true
}

// CutDetails implements cutsheet.CutLookup.
func (s *sheetInfo) CutDetails(key string) (cutsheet.CutDetails, bool) {
	pid, rid, ok := editspec.NestedRef(key)
	if !ok {
		return cutsheet.CutDetails{}, false
	}
	var rev models.Revision
	if s.a.DB.GetByID(s.ctx, store.CRevisions, s.a.WorkspaceID(), rid, &rev) != nil || rev.ProjectID != pid {
		return cutsheet.CutDetails{}, false
	}
	var p models.Project
	_ = s.a.DB.GetByID(s.ctx, store.CProjects, s.a.WorkspaceID(), pid, &p)
	d := cutsheet.CutDetails{ProjectName: p.Name, Title: rev.Spec.Title, Number: rev.Number, Duration: rev.Duration}
	if h := p.Branches[rev.Branch].Head; h != "" && h != rev.ID {
		var head models.Revision
		if s.a.DB.GetByID(s.ctx, store.CRevisions, s.a.WorkspaceID(), h, &head) == nil && head.Number > rev.Number {
			d.NewerNumber = head.Number
		}
	}
	return d, true
}

// FindCut implements cutsheet.CutLookup: "Brand r4" (0: the latest version).
func (s *sheetInfo) FindCut(project string, number int) (string, string) {
	p, err := resolveProjectLoose(s.ctx, s.a, project)
	if err != nil {
		return "", plainErr(err)
	}
	if number == 0 {
		h := p.Branches["main"].Head
		if h == "" {
			return "", p.Name + " has no cut yet."
		}
		return editspec.NestedKey(p.ID, h), ""
	}
	var rs []models.Revision
	if err := s.a.DB.Find(s.ctx, store.CRevisions, s.a.WorkspaceID(), bson.M{"projectId": p.ID, "number": number}, &rs); err != nil || len(rs) == 0 {
		return "", "There's no version " + itoaInt(number) + " of " + p.Name + "."
	}
	return editspec.NestedKey(p.ID, rs[0].ID), ""
}

func itoaInt(n int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + fmtInt(n))
}

func fmtInt(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
