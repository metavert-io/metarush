package leave

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Whose things something touches (D290): a kept thing is refused unless
// every project it touches allows it. A clip counts under each project it is
// in (and each it was in before the Trash); a version under its project and
// the projects of every clip, nested cut, conversation member and picture its
// cut plays; a project under itself, every project its clips are also in,
// the projects it uses through links, and what its cuts' latest versions play.

// AssetProjects are the projects a clip counts under: those it is in, those
// it was in before the Trash, else "" (footage in no project: the default's).
func AssetProjects(as *models.Asset) []string {
	var out []string
	add := func(ps ...string) {
		for _, p := range ps {
			if p != "" && !contains(out, p) {
				out = append(out, p)
			}
		}
	}
	add(as.ProjectIDs...)
	if as.Trash != nil {
		add(as.Trash.ProjectIDs...)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// toucher gathers the projects things touch, each thing looked at once.
type toucher struct {
	ctx      context.Context
	a        *app.App
	out      []string
	seen     map[string]bool
	expanded map[string]bool
	// err is the first lookup that didn't answer (or ErrTooMany): a walk that
	// returns it can't say the list is whole (RevisionTouchesChecked).
	err error
}

// ErrTooMany is a walk that met more things than one walk looks at
// (MaxThings): its list can't be said to be whole, however often it is asked.
var ErrTooMany = errors.New("it uses too many things to look through")

// MaxThings is how many things (clips, versions, pictures…) one walk looks
// at (a variable for tests).
var MaxThings = 5000

// fail keeps the first lookup that didn't answer; a record that isn't there
// (deleted since) is no error.
func (t *toucher) fail(err error) {
	if err == nil || errors.Is(err, store.ErrNotFound) || errors.Is(err, mongo.ErrNoDocuments) {
		return
	}
	if t.err == nil {
		t.err = err
	}
}

func newToucher(ctx context.Context, a *app.App) *toucher {
	return &toucher{ctx: ctx, a: a, seen: map[string]bool{}, expanded: map[string]bool{}}
}

func (t *toucher) add(ps ...string) {
	for _, p := range ps {
		if !contains(t.out, p) {
			t.out = append(t.out, p)
		}
	}
}

func (t *toucher) once(key string) bool {
	if t.seen[key] {
		return false
	}
	t.seen[key] = true
	if len(t.seen) >= MaxThings {
		t.fail(ErrTooMany)
		return false
	}
	return true
}

// asset adds the projects a clip counts under.
func (t *toucher) asset(id string) {
	if !t.once("ast:" + id) {
		return
	}
	var as models.Asset
	if err := t.a.DB.GetByID(t.ctx, store.CAssets, t.a.WorkspaceID(), id, &as); err != nil {
		t.fail(err)
		return
	}
	t.add(AssetProjects(&as)...)
}

// revision adds a version's project and everything its cut plays.
func (t *toucher) revision(id string) {
	if !t.once("rev:" + id) {
		return
	}
	var r struct {
		ProjectID string        `bson:"projectId"`
		Spec      editspec.Spec `bson:"spec"`
	}
	if err := t.a.DB.C(store.CRevisions).FindOne(t.ctx, bson.M{"_id": id, "workspaceId": t.a.WorkspaceID()}).Decode(&r); err != nil {
		t.fail(err)
		return
	}
	t.add(r.ProjectID)
	t.spec(&r.Spec)
}

// spec adds what a cut plays: clips, nested cuts, conversations' members,
// pinned pictures, and every other file it plays — sound tracks, narration
// takes, clearer voices (editspec.AssetIDs).
func (t *toucher) spec(s *editspec.Spec) {
	for _, id := range editspec.AssetIDs(s) {
		if strings.HasPrefix(id, ids.Asset+"_") {
			t.asset(id)
		}
	}
	for _, c := range s.Clips {
		switch {
		case strings.HasPrefix(c.AssetID, ids.Asset+"_"):
			t.asset(c.AssetID)
		case editspec.IsNested(c.AssetID):
			if p, rev, ok := editspec.NestedRef(c.AssetID); ok {
				t.add(p)
				t.revision(rev)
			}
		}
	}
	for _, pin := range s.Conversations {
		t.doc(store.CConversations, pin.ID)
		for _, m := range pin.Members {
			t.asset(m.AssetID)
		}
	}
	for _, pin := range s.Resources {
		t.doc(store.CResources, pin.ID)
	}
}

// project adds a project and whatever it shows: its clips' other projects,
// the projects (and collections) it uses through links, and its cuts' latest
// versions.
func (t *toucher) project(id string) {
	t.add(id)
	if id == "" || t.expanded[id] {
		return
	}
	t.expanded[id] = true
	t.assetsWhere(bson.M{"$or": bson.A{bson.M{"projectIds": id}, bson.M{"trash.projectIds": id}}})
	var links []models.ProjectLink
	t.fail(t.a.DB.Find(t.ctx, store.CProjectLinks, t.a.WorkspaceID(), bson.M{"projectId": id}, &links, options.Find().SetLimit(200)))
	for _, l := range links {
		switch ids.Prefix(l.SourceID) {
		case ids.Collection:
			t.assetsWhere(bson.M{"collectionIds": l.SourceID})
		default:
			t.add(l.SourceID)
			t.assetsWhere(bson.M{"projectIds": l.SourceID})
		}
	}
	var p struct {
		Branches map[string]models.BranchHead `bson:"branches"`
	}
	if err := t.a.DB.C(store.CProjects).FindOne(t.ctx, bson.M{"_id": id, "workspaceId": t.a.WorkspaceID()}).Decode(&p); err != nil {
		t.fail(err)
	} else {
		for _, b := range p.Branches {
			if b.Head != "" {
				t.revision(b.Head)
			}
		}
	}
}

// assetsWhere adds the projects of the clips a filter finds.
func (t *toucher) assetsWhere(filter bson.M) {
	if !t.once("where:" + mustKey(filter)) {
		return
	}
	var rows []struct {
		ProjectIDs []string `bson:"projectIds"`
		Trash      *struct {
			ProjectIDs []string `bson:"projectIds"`
		} `bson:"trash"`
	}
	t.fail(t.a.DB.Find(t.ctx, store.CAssets, t.a.WorkspaceID(), filter, &rows, options.Find().SetProjection(bson.M{"projectIds": 1, "trash.projectIds": 1}).SetLimit(20000)))
	for _, r := range rows {
		as := models.Asset{ProjectIDs: r.ProjectIDs}
		if r.Trash != nil {
			as.Trash = &models.AssetTrash{ProjectIDs: r.Trash.ProjectIDs}
		}
		t.add(AssetProjects(&as)...)
	}
}

func mustKey(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// docCollections are where a record named by an ID's prefix is kept.
var docCollections = map[string]string{
	ids.Render: store.CRenders, ids.Resource: store.CResources, ids.Conversation: store.CConversations, models.QAReportPrefix: store.CQAReports,
	ids.Generation: store.CGenerations, ids.Job: store.CJobs, ids.Draft: store.CDrafts, ids.Todo: store.CTodos, ids.Look: store.CLooks,
	ids.Consent: store.CConsents, ids.AbilityRun: store.CAbilityRuns, ids.Recipe: store.CRecipes, ids.RecipeRun: store.CRecipeRuns,
	ids.RemotionSource: store.CRemotionSources, ids.Comment: store.CComments, ids.Collection: store.CCollections,
}

// doc adds what a record names: its project and the clips, versions,
// projects and pictures it refers to.
func (t *toucher) doc(coll, id string) {
	if id == "" || !t.once(coll+":"+id) {
		return
	}
	var d bson.M
	if err := t.a.DB.C(coll).FindOne(t.ctx, bson.M{"_id": id, "workspaceId": t.a.WorkspaceID()}).Decode(&d); err != nil {
		t.fail(err)
		return
	}
	if coll == store.CCollections {
		t.assetsWhere(bson.M{"collectionIds": id})
	}
	t.walkDoc("", d, 0)
}

func (t *toucher) walkDoc(key string, v any, depth int) {
	if depth > 8 {
		return
	}
	switch x := v.(type) {
	case bson.M:
		for k, y := range x {
			t.walkDoc(k, y, depth+1)
		}
	case bson.D:
		for _, e := range x {
			t.walkDoc(e.Key, e.Value, depth+1)
		}
	case bson.A:
		for _, y := range x {
			t.walkDoc(key, y, depth+1)
		}
	case []any:
		for _, y := range x {
			t.walkDoc(key, y, depth+1)
		}
	case string:
		if key == "_id" || key == "workspaceId" {
			return
		}
		t.ref(x, false)
	}
}

// ref adds what one ID names (a project fully only when asked by name).
func (t *toucher) ref(v string, full bool) {
	id := v
	if i := strings.IndexAny(id, "@:"); i > 0 {
		id = id[:i] // clip@12.4, clip:time
	}
	switch ids.Prefix(id) {
	case ids.Project:
		if full {
			t.project(id)
		} else {
			t.add(id)
		}
	case ids.Asset:
		t.asset(id)
	case ids.Revision:
		t.revision(id)
	case "":
		if editspec.IsNested(v) {
			if p, rev, ok := editspec.NestedRef(v); ok {
				t.add(p)
				t.revision(rev)
			}
		}
	default:
		if coll := docCollections[ids.Prefix(id)]; coll != "" {
			t.doc(coll, id)
		}
	}
}

// input walks an operation's input: IDs anywhere, and projects, clips and
// named things under the usual field names.
func (t *toucher) input(key string, v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, y := range x {
			t.input(k, y)
		}
	case []any:
		for _, y := range x {
			t.input(key, y)
		}
	case string:
		x = strings.TrimSpace(x)
		if x == "" {
			return
		}
		if ids.Prefix(strings.SplitN(strings.SplitN(x, "@", 2)[0], ":", 2)[0]) != "" || editspec.IsNested(x) {
			t.ref(x, true)
			return
		}
		switch key {
		case "projectId", "project", "from", "to", "settingsOf":
			if p, err := ops.ResolveProject(t.ctx, t.a, x); err == nil {
				t.project(p.ID)
			}
		case "assetId", "asset", "clip", "clipId", "footage", "sample", "samples", "consentFrom":
			if as, err := ops.ResolveFootage(t.ctx, t.a, x, ""); err == nil {
				t.add(AssetProjects(as)...)
			}
		case "ref", "name", "recipe", "source": // things named, Recipes' too
			t.named(x)
		}
	}
}

// named adds the projects of things people name: motion graphics, Remotion
// sources, resources and Recipes' entries by their name (name@version too).
func (t *toucher) named(v string) {
	name := strings.SplitN(v, "@", 2)[0]
	for _, coll := range []string{store.CMotion, store.CRemotionSources, store.CResources, store.CRecipes} {
		var rows []struct {
			ProjectID string `bson:"projectId"`
		}
		_ = t.a.DB.Find(t.ctx, coll, t.a.WorkspaceID(), bson.M{"name": name}, &rows, options.Find().SetProjection(bson.M{"projectId": 1}).SetLimit(200))
		for _, r := range rows {
			t.add(r.ProjectID)
		}
	}
}

// ProjectsIn are the projects an operation's input touches (nil: none found).
func ProjectsIn(ctx context.Context, a *app.App, in any) []string {
	b, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var v any
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	t := newToucher(ctx, a)
	t.input("", v)
	return t.out
}

// ProjectTouches are the projects a project's things touch (itself first).
func ProjectTouches(ctx context.Context, a *app.App, projectID string) []string {
	t := newToucher(ctx, a)
	t.project(projectID)
	return t.out
}

// RevisionTouches are the projects a version touches: its project and what
// its cut plays.
func RevisionTouches(ctx context.Context, a *app.App, revisionID string) []string {
	t := newToucher(ctx, a)
	t.revision(revisionID)
	return t.out
}

// RevisionTouchesChecked is RevisionTouches for a check that must fail
// closed: an error when any lookup on the way didn't answer, or when the
// version uses more things than one walk looks at — the list then can't be
// said to be whole. A version that isn't there is an error too.
func RevisionTouchesChecked(ctx context.Context, a *app.App, revisionID string) ([]string, error) {
	t := newToucher(ctx, a)
	t.revision(revisionID)
	if t.err != nil {
		return nil, t.err
	}
	if len(t.out) == 0 {
		return nil, store.ErrNotFound
	}
	return t.out, nil
}

// SpecTouches are the projects a cut touches: its project and what it plays.
func SpecTouches(ctx context.Context, a *app.App, projectID string, s *editspec.Spec) []string {
	t := newToucher(ctx, a)
	t.add(projectID)
	t.spec(s)
	return t.out
}
