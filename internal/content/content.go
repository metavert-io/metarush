// Package content holds Content Settings (D192, D193): what every video made
// with them uses — the brand, the spelling list, the narration voice and
// house rules. They belong to a project. Each project uses one of: the
// default (the project whose "Use for new projects" is ticked), another
// project's (followed live; following passes through chains, loops are
// refused) or its own. New projects use the default; footage in no project
// (the Inbox) uses the default's spellings.
//
// Every part is registered once (parts.go) and reads its value from the
// settings a project resolves to, so following, "Also used by" and copies work
// for every part the same way — a later part gets them by registering.
package content

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// node is a project as Content Settings see it.
type node struct {
	ID        string
	Name      string
	Archived  bool
	Uses      string // default | project | own
	Follows   string // Uses project: the project followed
	By        *models.Actor
	CreatedBy models.Actor
}

// Graph is every project's choice at one moment, and the default.
type Graph struct {
	Default string
	nodes   map[string]*node
	order   []string // by name
}

// Resolution is where a project's Content Settings come from.
type Resolution struct {
	// Project is the project asked about ("" footage in no project: the default's).
	Project string `json:"project,omitempty"`
	// Uses is the project's own choice: default, project or own.
	Uses string `json:"uses"`
	// Next is the project it follows (uses project), or the default project (uses default).
	Next string `json:"next,omitempty"`
	// Owner is the project whose own settings it gets ("" none: nothing set up).
	Owner string `json:"owner,omitempty"`
	// Chain lists the projects passed through to reach the owner.
	Chain []string `json:"chain,omitempty"`
	// Loop: the chain comes back to a project already passed (refused when
	// made; read as nothing).
	Loop bool `json:"loop,omitempty"`
}

// Own reports a project that uses its own settings.
func (r Resolution) Own() bool { return r.Project != "" && r.Owner == r.Project }

// Load reads every project's choice and the workspace's default. A followed
// project that is gone sends its followers back to the default (and tells the
// people involved), and a default that is gone is cleared.
func Load(ctx context.Context, a *app.App) (*Graph, error) {
	g, gone, err := load(ctx, a)
	if err != nil {
		return nil, err
	}
	if len(gone) > 0 {
		repairGone(ctx, a, g, gone)
	}
	return g, nil
}

type goneRef struct {
	follower string // "" the workspace's default
	target   string
}

func load(ctx context.Context, a *app.App) (*Graph, []goneRef, error) {
	g := &Graph{nodes: map[string]*node{}}
	g.Default = store.ContentDefault(ctx, a.DB, a.WorkspaceID())
	var ps []struct {
		ID        string             `bson:"_id"`
		Name      string             `bson:"name"`
		Archived  bool               `bson:"archived"`
		Content   *models.ContentUse `bson:"content"`
		CreatedBy models.Actor       `bson:"createdBy"`
	}
	if err := a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), bson.M{}, &ps, options.Find().SetProjection(bson.M{"name": 1, "archived": 1, "content": 1, "createdBy": 1})); err != nil {
		return nil, nil, err
	}
	for _, p := range ps {
		n := &node{ID: p.ID, Name: p.Name, Archived: p.Archived, Uses: models.ContentDefault, CreatedBy: p.CreatedBy}
		if p.Content != nil {
			n.Uses, n.Follows, n.By = p.Content.Uses, p.Content.Project, p.Content.By
			if n.Uses == "" {
				n.Uses = models.ContentDefault
			}
		}
		g.nodes[p.ID] = n
		g.order = append(g.order, p.ID)
	}
	sort.SliceStable(g.order, func(i, j int) bool {
		return strings.ToLower(g.nodes[g.order[i]].Name) < strings.ToLower(g.nodes[g.order[j]].Name)
	})
	var gone []goneRef
	if g.Default != "" && g.nodes[g.Default] == nil {
		gone = append(gone, goneRef{target: g.Default})
	}
	for _, id := range g.order {
		n := g.nodes[id]
		if n.Uses == models.ContentProject && (n.Follows == "" || g.nodes[n.Follows] == nil) {
			gone = append(gone, goneRef{follower: id, target: n.Follows})
		}
	}
	return g, gone, nil
}

// Name is a project's name ("" unknown).
func (g *Graph) Name(id string) string {
	if n := g.nodes[id]; n != nil {
		return n.Name
	}
	return ""
}

// Has reports a project that exists.
func (g *Graph) Has(id string) bool { return g.nodes[id] != nil }

// Archived reports an archived project.
func (g *Graph) Archived(id string) bool { return g.nodes[id] != nil && g.nodes[id].Archived }

// Projects lists every project's ID, by name.
func (g *Graph) Projects() []string { return append([]string(nil), g.order...) }

// Uses is a project's own choice (default, project or own) and the project it follows.
func (g *Graph) Uses(id string) (string, string) {
	n := g.nodes[id]
	if n == nil {
		return models.ContentDefault, ""
	}
	return n.Uses, n.Follows
}

// Resolve says where a project's settings come from ("" : the default's,
// what footage in no project uses).
func (g *Graph) Resolve(id string) Resolution {
	if id == "" {
		if g.Default == "" || g.nodes[g.Default] == nil {
			return Resolution{Uses: models.ContentDefault}
		}
		r := g.Resolve(g.Default)
		return Resolution{Uses: models.ContentDefault, Next: g.Default, Owner: r.Owner, Chain: append([]string{g.Default}, r.Chain...), Loop: r.Loop}
	}
	out := Resolution{Project: id, Uses: models.ContentDefault}
	if n := g.nodes[id]; n != nil {
		out.Uses = n.Uses
	}
	seen := map[string]bool{}
	cur := id
	for step := 0; ; step++ {
		if seen[cur] {
			out.Loop, out.Owner = true, ""
			return out
		}
		seen[cur] = true
		n := g.nodes[cur]
		if n == nil {
			return out // the project asked about isn't there
		}
		next := ""
		switch n.Uses {
		case models.ContentOwn:
			out.Owner = cur
			return out
		case models.ContentProject:
			next = n.Follows
			if next == "" || g.nodes[next] == nil {
				next = g.Default // a followed project that is gone: back to the default
			}
		default:
			next = g.Default
		}
		if next == "" || g.nodes[next] == nil {
			return out // no default yet: nothing set up
		}
		if next == cur {
			out.Owner = cur // the default uses "the default": its own
			return out
		}
		if step == 0 {
			out.Next = next
		}
		out.Chain = append(out.Chain, next)
		cur = next
	}
}

// UsedBy lists the projects, other than owner, whose settings come from
// owner's own (directly, through the default, or through a chain), by name.
func (g *Graph) UsedBy(owner string) []string {
	var out []string
	for _, id := range g.order {
		if id != owner && g.Resolve(id).Owner == owner {
			out = append(out, id)
		}
	}
	return out
}

// DefaultOwner is the project whose own settings new projects get ("" none).
func (g *Graph) DefaultOwner() string { return g.Resolve("").Owner }

// wouldLoop reports whether project following target (or, target "", the
// default) would come back to project.
func (g *Graph) wouldLoop(project, target string, asDefault bool) bool {
	c := g.clone()
	n := c.nodes[project]
	if n == nil {
		return false
	}
	if asDefault {
		c.Default = project
	} else if target == "" {
		n.Uses, n.Follows = models.ContentDefault, ""
	} else {
		n.Uses, n.Follows = models.ContentProject, target
	}
	for _, id := range c.order {
		if c.Resolve(id).Loop {
			return true
		}
	}
	return false
}

func (g *Graph) clone() *Graph {
	c := &Graph{Default: g.Default, nodes: map[string]*node{}, order: g.order}
	for k, v := range g.nodes {
		x := *v
		c.nodes[k] = &x
	}
	return c
}

// Resolve says where one project's Content Settings come from ("" : the default's).
func Resolve(ctx context.Context, a *app.App, projectID string) (*Graph, Resolution, error) {
	g, err := Load(ctx, a)
	if err != nil {
		return nil, Resolution{}, err
	}
	return g, g.Resolve(projectID), nil
}

// Record is a project's own settings record (nil when it never had one).
func Record(ctx context.Context, a *app.App, projectID string) (*models.ContentSettings, error) {
	if projectID == "" {
		return nil, nil
	}
	var r models.ContentSettings
	err := a.DB.C(store.CContentSettings).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectId": projectID}).Decode(&r)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// For is the Content Settings a project uses ("" : the default's, what
// footage in no project uses), and whose they are ("" nobody's: nothing set
// up). The record is never nil.
func For(ctx context.Context, a *app.App, projectID string) (*models.ContentSettings, string, error) {
	_, r, err := Resolve(ctx, a, projectID)
	if err != nil {
		return nil, "", err
	}
	if r.Owner == "" {
		return &models.ContentSettings{}, "", nil
	}
	rec, err := Record(ctx, a, r.Owner)
	if err != nil {
		return nil, "", err
	}
	if rec == nil {
		rec = &models.ContentSettings{ProjectID: r.Owner}
	}
	return rec, r.Owner, nil
}

// Owners are the projects whose own settings apply to footage in these
// projects: each one's (a clip in several projects uses all of theirs); no
// project, the default's.
func Owners(ctx context.Context, a *app.App, projectIDs []string) ([]string, error) {
	g, err := Load(ctx, a)
	if err != nil {
		return nil, err
	}
	if len(projectIDs) == 0 {
		projectIDs = []string{""}
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range projectIDs {
		if o := g.Resolve(id).Owner; o != "" && !seen[o] {
			seen[o] = true
			out = append(out, o)
		}
	}
	return out, nil
}

// ensure makes a project's own record when it has none yet.
func ensure(ctx context.Context, a *app.App, projectID string) (*models.ContentSettings, error) {
	now := time.Now().UTC()
	rec := models.ContentSettings{ID: ids.New(ids.ContentSettings), WorkspaceID: a.WorkspaceID(), ProjectID: projectID, CreatedAt: now, CreatedBy: a.Actor, UpdatedAt: now}
	_, err := a.DB.C(store.CContentSettings).UpdateOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectId": projectID},
		bson.M{"$setOnInsert": rec}, options.UpdateOne().SetUpsert(true))
	if err != nil && !store.IsDuplicateKey(err) {
		return nil, err
	}
	return Record(ctx, a, projectID)
}

// Save changes fields of a project's own record (made when it has none).
func Save(ctx context.Context, a *app.App, projectID string, set, unset bson.M) error {
	if _, err := ensure(ctx, a, projectID); err != nil {
		return err
	}
	if set == nil {
		set = bson.M{}
	}
	actor := a.Actor
	set["updatedAt"], set["updatedBy"] = time.Now().UTC(), actor
	up := bson.M{"$set": set}
	if len(unset) > 0 {
		up["$unset"] = unset
	}
	_, err := a.DB.C(store.CContentSettings).UpdateOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectId": projectID}, up)
	return err
}

// Update applies any update document to a project's own record (made when it has none).
func Update(ctx context.Context, a *app.App, projectID string, update bson.M) error {
	if _, err := ensure(ctx, a, projectID); err != nil {
		return err
	}
	set, _ := update["$set"].(bson.M)
	if set == nil {
		set = bson.M{}
	}
	set["updatedAt"], set["updatedBy"] = time.Now().UTC(), a.Actor
	update["$set"] = set
	_, err := a.DB.C(store.CContentSettings).UpdateOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectId": projectID}, update)
	return err
}

// Target is whose settings an edit changes.
type Target struct {
	ProjectID   string
	ProjectName string
	// Default: no project was named, so the edit changes the default's
	// settings (what new projects use).
	Default bool
	// UsedBy are the other projects using these settings.
	UsedBy []string
}

// Note says whose settings changed when it isn't obvious: the default's, or
// settings other projects use too.
func (t *Target) Note(g *Graph) string {
	var s string
	if t.Default {
		s = fmt.Sprintf("These are %s's settings, which new projects use.", quote(t.ProjectName))
	}
	if n := len(t.UsedBy); n > 0 {
		s = strings.TrimSpace(s + " " + fmt.Sprintf("Also used by %s.", projectsWord(n)))
	}
	return s
}

// EditTarget is whose settings an edit naming projectID changes: that
// project's own. A project that uses another's is refused plainly (edit them
// there, or give it its own). Without a project, the default's — refused the
// same way when the default uses another project's.
func EditTarget(ctx context.Context, a *app.App, projectID string) (*Target, *Graph, error) {
	g, err := Load(ctx, a)
	if err != nil {
		return nil, nil, err
	}
	t := &Target{}
	id := projectID
	if id == "" {
		if g.Default == "" || !g.Has(g.Default) {
			return nil, g, apperr.New(apperr.NotConfigured, "No project's settings are used for new projects yet.").
				WithHint("Say which project's settings to change, or tick “Use for new projects” in a project's Settings tab.").
				WithCommand("metarush content default --project <project>")
		}
		id, t.Default = g.Default, true
	}
	if !g.Has(id) {
		return nil, g, apperr.New(apperr.NotFound, "That project isn't in this workspace.").WithDetails(map[string]any{"projectId": id})
	}
	r := g.Resolve(id)
	if r.Owner != id {
		return nil, g, followedErr(g, id, r)
	}
	t.ProjectID, t.ProjectName, t.UsedBy = id, g.Name(id), g.UsedBy(id)
	return t, g, nil
}

// followedErr refuses an edit of a project that uses another's settings.
func followedErr(g *Graph, id string, r Resolution) *apperr.Error {
	name := g.Name(id)
	if r.Owner == "" {
		return apperr.New(apperr.Conflict, "%s uses the default settings, and no project's settings are the default yet.", quote(name)).
			WithHint("Give it its own settings, or tick “Use for new projects” on a project.").
			WithCommand("metarush content own --project " + id).
			WithDetails(map[string]any{"projectId": id, "uses": r.Uses})
	}
	owner := g.Name(r.Owner)
	return apperr.New(apperr.Conflict, "%s uses %s's settings, so they can't be changed from here.", quote(name), quote(owner)).
		WithHint(fmt.Sprintf("Change them in %s (they change for every project using them), or give %s its own settings.", quote(owner), quote(name))).
		WithCommand("metarush content own --project " + id).
		WithDetails(map[string]any{"projectId": id, "uses": r.Uses, "from": r.Owner, "fromName": owner})
}

// quote is a name in curly quotes.
func quote(s string) string { return "“" + s + "”" }

// projectsWord is "1 project", "3 projects".
func projectsWord(n int) string {
	if n == 1 {
		return "1 project"
	}
	return fmt.Sprintf("%d projects", n)
}
