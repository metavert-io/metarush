package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// Clearing archived projects out for good (D196): an owner's, people only.
// Only a deleted (archived) project can be cleared out. It removes what is the
// project's — its versions and cuts, its renders (their stored files only when
// nothing else uses the same bytes, purgemedia.go), its renders' quality
// checks, its Claude Code conversations with their saved proposals, what it
// kept, its rules and sign-offs, comments, review requests (they are on its
// versions), its own to-dos, notifications about it, its project links both
// ways, its Content Settings (projects that followed them use the default and
// are told) and its Remotion projects. Footage is never deleted: footage only
// it had goes back to the Inbox, footage other projects' cuts use through a
// link joins those projects, and its pictures and files move to the
// workspace library. Spending and the audit trail and activity log stay. It is
// refused, naming them, while another project's cut plays one of its cuts
// inside it, while MetaRush is still working on it and while Claude Code is
// answering in one of its conversations. Every step can run again: a
// clear-out stopped halfway (the project says so: purging) is finished by
// running it again, and it can't be restored meanwhile.

// ProjectPurgeIn clears archived projects out for good.
type ProjectPurgeIn struct {
	ID          string `json:"id,omitempty" jsonschema:"description=The archived project (its ID or name)."`
	AllArchived bool   `json:"allArchived,omitempty" jsonschema:"description=Every archived project."`
	// DryRun only says what would go, what moves and what stops it.
	DryRun bool `json:"dryRun,omitempty"`
	// Forever must be true to clear out: it can't be undone.
	Forever bool `json:"forever,omitempty" jsonschema:"description=Must be true: clearing out can't be undone."`
}

// PurgeCounts is what clearing out a project removes, moves and keeps.
type PurgeCounts struct {
	Cuts          int   `json:"cuts"`
	Versions      int   `json:"versions"`
	Renders       int   `json:"renders"`
	RenderBytes   int64 `json:"renderBytes" jsonschema:"description=Bytes of the renders' videos."`
	Checks        int   `json:"checks" jsonschema:"description=Quality checks of its renders."`
	Conversations int   `json:"conversations" jsonschema:"description=Claude Code conversations."`
	Kept          int   `json:"kept" jsonschema:"description=Things conversations offered to keep."`
	Rules         int   `json:"rules"`
	Signoffs      int   `json:"signoffs"`
	Comments      int   `json:"comments"`
	Todos         int   `json:"todos" jsonschema:"description=Its own to-dos (questions about footage stay)."`
	Notifications int   `json:"notifications"`
	Links         int   `json:"links" jsonschema:"description=Project links, both ways."`
	Remotion      int   `json:"remotion" jsonschema:"description=Remotion projects."`
	// ContentSettings: it had Content Settings of its own (they go);
	// SpellingWords are their spelling list's words.
	ContentSettings bool `json:"contentSettings"`
	SpellingWords   int  `json:"spellingWords"`
	// Footage: clips only it had go back to the Inbox (ToInbox); clips other
	// projects' cuts use through a link join those projects (Joins); the rest
	// stay where else they are (Stays). Footage is never deleted here.
	ToInbox   int `json:"toInbox"`
	Joins     int `json:"joins"`
	Stays     int `json:"stays"`
	Resources int `json:"resources" jsonschema:"description=Pictures and files that move to the workspace library."`
	// Followers are the projects that used its Content Settings: they use the
	// default instead and the people involved are told. Default: its
	// settings are the default for new projects (cleared).
	Followers []string `json:"followers"`
	Default   bool     `json:"default"`
	// StoredRemoved and StoredKept (after a clear-out): stored files removed,
	// and files kept because something else uses the same bytes.
	StoredRemoved int `json:"storedRemoved,omitempty"`
	StoredKept    int `json:"storedKept,omitempty"`
}

// ProjectPurgeRow is one project's clear-out.
type ProjectPurgeRow struct {
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	// Status: would (a dry run), purged, or refused (Blockers say why).
	Status   string      `json:"status" jsonschema:"enum=would,enum=purged,enum=refused"`
	Blockers []string    `json:"blockers"`
	Counts   PurgeCounts `json:"counts"`
	// Goes and Moves say it in plain words: what is deleted, and what moves or stays.
	Goes  []string `json:"goes"`
	Moves []string `json:"moves"`
}

// ProjectPurgeOut says what was (or would be) cleared out.
type ProjectPurgeOut struct {
	Projects []ProjectPurgeRow `json:"projects"`
	Purged   int               `json:"purged"`
	Refused  int               `json:"refused"`
	// Question is the warning a person confirms ("Delete “Spring promo” for good?").
	Question string `json:"question"`
	Message  string `json:"message"`
	DryRun   bool   `json:"dryRun"`
}

// PurgeHook removes what another part of MetaRush keeps about a project
// cleared out for good (the Claude Code conversations and their saved
// proposals); it returns how many records it removed and must be safe to run again.
type PurgeHook func(ctx context.Context, a *app.App, projectID string) (int, error)

var (
	purgeHooksMu sync.Mutex
	purgeHooks   = map[string]PurgeHook{}
)

// RegisterPurgeHook adds cleanup for a project cleared out for good.
func RegisterPurgeHook(name string, h PurgeHook) {
	purgeHooksMu.Lock()
	defer purgeHooksMu.Unlock()
	purgeHooks[name] = h
}

// purgeStep, in tests, stops a clear-out after a step (to show that running
// it again finishes it).
var purgeStep func(step string) error

func purgeCheckpoint(name string) error {
	if purgeStep != nil {
		return purgeStep(name)
	}
	return nil
}

func init() {
	Def("project.purge", "Clear archived (deleted) projects out for good: their versions, renders, Claude Code conversations and what they kept go; their footage goes back to the Inbox (never deleted); refused while another project's cut plays one of their cuts; owners, people only", Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, func(ctx context.Context, a *app.App, in *ProjectPurgeIn) (*ProjectPurgeOut, error) {
		return purgeProjects(ctx, a, in)
	})
}

func purgeProjects(ctx context.Context, a *app.App, in *ProjectPurgeIn) (*ProjectPurgeOut, error) {
	var targets []models.Project
	switch {
	case in.AllArchived && strings.TrimSpace(in.ID) != "":
		return nil, invalid("Name one archived project, or clear out all of them, not both.")
	case in.AllArchived:
		if err := a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), bson.M{"archived": true}, &targets, options.Find().SetSort(bson.D{{Key: "name", Value: 1}})); err != nil {
			return nil, err
		}
	case strings.TrimSpace(in.ID) == "":
		return nil, invalid("Say which archived project to clear out, or all of them.")
	default:
		p, err := resolveProject(ctx, a, in.ID)
		if err != nil {
			return nil, err
		}
		if !p.Archived {
			return nil, apperr.New(apperr.Conflict, "Only archived projects can be cleared out for good, and “%s” isn't archived.", p.Name).
				WithHint("Delete it first: it moves to Archived projects.").WithDetails(map[string]any{"reason": "not_archived", "projectId": p.ID}).
				WithCommand("metarush project delete " + p.ID)
		}
		targets = []models.Project{*p}
	}
	out := &ProjectPurgeOut{Projects: []ProjectPurgeRow{}, DryRun: in.DryRun}
	if len(targets) == 0 {
		out.Message = "There are no archived projects to clear out."
		out.Question = out.Message
		return out, nil
	}
	set := make([]string, len(targets))
	for i := range targets {
		set[i] = targets[i].ID
	}
	rows := make([]ProjectPurgeRow, len(targets))
	// nests[x] are the projects (being cleared out too) whose cuts play x's cut.
	nests := map[string][]string{}
	for i := range targets {
		row, nesters, err := planPurge(ctx, a, &targets[i], set)
		if err != nil {
			return nil, err
		}
		rows[i], nests[targets[i].ID] = row, nesters
	}
	// A project nested by one that stays (refused) stays too.
	refused := map[string]bool{}
	for i := range rows {
		if len(rows[i].Blockers) > 0 {
			refused[rows[i].ProjectID] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for i := range rows {
			id := rows[i].ProjectID
			if refused[id] {
				continue
			}
			for _, by := range nests[id] {
				if refused[by] {
					refused[id], changed = true, true
					rows[i].Blockers = append(rows[i].Blockers, fmt.Sprintf("“%s”, which stays, plays its cut inside its own.", nameIn(rows, by)))
					break
				}
			}
		}
	}
	for i := range rows {
		rows[i].Status = "would"
		if refused[rows[i].ProjectID] {
			rows[i].Status = "refused"
			out.Refused++
		}
	}
	out.Question = purgeQuestion(rows)
	if in.DryRun {
		out.Projects = rows
		out.Message = purgeSummary(rows, true)
		return out, nil
	}
	if out.Refused == len(rows) {
		first := rows[0]
		return nil, apperr.New(apperr.Conflict, "“%s” can't be cleared out yet: %s", first.Name, strings.TrimSuffix(first.Blockers[0], ".")+".").
			WithDetails(map[string]any{"reason": "refused", "projects": rows})
	}
	if !in.Forever {
		return nil, apperr.New(apperr.ConfirmRequired, "%s It can't be undone.", out.Question).
			WithHint("Confirm that you want it cleared out for good.").WithDetails(map[string]any{"projects": rows})
	}
	// Projects whose cuts play another's go first, so no cut is left
	// playing one that is gone if a clear-out stops halfway.
	for _, i := range purgeOrder(rows, nests, refused) {
		p := targets[i]
		removed, kept, err := purgeProject(ctx, a, &p)
		if err != nil {
			out.Projects = rows
			return nil, apperr.Wrap(apperr.From(err).Code, err, "Clearing out “%s” stopped before it finished. Run it again to finish it.", p.Name).
				WithDetails(map[string]any{"projectId": p.ID, "projects": rows}).WithCommand("metarush project purge " + p.ID)
		}
		rows[i].Status = "purged"
		rows[i].Counts.StoredRemoved, rows[i].Counts.StoredKept = removed, kept
		out.Purged++
	}
	out.Projects = rows
	out.Message = purgeSummary(rows, false)
	return out, nil
}

func nameIn(rows []ProjectPurgeRow, id string) string {
	for _, r := range rows {
		if r.ProjectID == id {
			return r.Name
		}
	}
	return "another project"
}

// purgeOrder lists the projects to clear out, nesting projects before the ones they nest.
func purgeOrder(rows []ProjectPurgeRow, nests map[string][]string, refused map[string]bool) []int {
	done := map[string]bool{}
	var order []int
	for len(order) < len(rows)-len(refused) {
		progressed := false
		for i, r := range rows {
			id := r.ProjectID
			if refused[id] || done[id] {
				continue
			}
			ready := true
			for _, by := range nests[id] {
				if !done[by] && !refused[by] {
					ready = false
				}
			}
			if ready {
				done[id], progressed = true, true
				order = append(order, i)
			}
		}
		if !progressed { // a circle (never saved): what remains, in order
			for i, r := range rows {
				if !refused[r.ProjectID] && !done[r.ProjectID] {
					done[r.ProjectID] = true
					order = append(order, i)
				}
			}
		}
	}
	return order
}

// planPurge says what clearing out one project removes, moves and keeps, and
// what stops it; nesters are the projects being cleared out too whose cuts
// play its cut.
func planPurge(ctx context.Context, a *app.App, p *models.Project, set []string) (ProjectPurgeRow, []string, error) {
	ws := a.WorkspaceID()
	row := ProjectPurgeRow{ProjectID: p.ID, Name: p.Name, Blockers: []string{}, Goes: []string{}, Moves: []string{}, Counts: PurgeCounts{Followers: []string{}}}
	c := &row.Counts
	count := func(coll string, filter bson.M) int {
		filter["workspaceId"] = ws
		n, _ := a.DB.C(coll).CountDocuments(ctx, filter)
		return int(n)
	}
	// What stops it: another project's cut plays its cut; work still running.
	uses, err := cutUses(ctx, a, p.ID)
	if err != nil {
		return row, nil, err
	}
	var nesters []string
	seen := map[string]bool{}
	for _, u := range uses {
		if u.ProjectID == p.ID || seen[u.ProjectID] {
			continue
		}
		seen[u.ProjectID] = true
		if slices.Contains(set, u.ProjectID) {
			nesters = append(nesters, u.ProjectID)
			continue
		}
		which := "its cut"
		if u.Current {
			which = fmt.Sprintf("its cut (version %d)", u.Versions[0])
		} else if len(u.Versions) > 0 {
			which = fmt.Sprintf("an older version of its cut (version %d)", u.Versions[0])
		}
		row.Blockers = append(row.Blockers, fmt.Sprintf("“%s” plays it inside %s: take it out of that cut first.", u.ProjectName, which))
	}
	var running []models.Job
	_ = a.DB.Find(ctx, store.CJobs, ws, bson.M{"projectId": p.ID, "state": models.JobRunning}, &running, options.Find().SetProjection(bson.M{"type": 1}).SetLimit(5))
	if len(running) > 0 {
		row.Blockers = append(row.Blockers, fmt.Sprintf("MetaRush is still working on it (a %s): try again when that's done.", jobs.Label(running[0].Type)))
	}
	if n := count(store.CAssistantConversations, bson.M{"projectId": p.ID, "state": "working"}); n > 0 {
		row.Blockers = append(row.Blockers, "Claude Code is still answering in one of its conversations: try again when it's done.")
	}

	// What goes.
	c.Cuts = len(p.Branches)
	c.Versions = count(store.CRevisions, bson.M{"projectId": p.ID})
	var renders []struct {
		Output *struct {
			Size int64 `bson:"size"`
		} `bson:"output"`
	}
	_ = a.DB.Find(ctx, store.CRenders, ws, bson.M{"projectId": p.ID}, &renders, options.Find().SetProjection(bson.M{"output.size": 1}))
	c.Renders = len(renders)
	for _, r := range renders {
		if r.Output != nil {
			c.RenderBytes += r.Output.Size
		}
	}
	c.Checks = count(store.CQAReports, bson.M{"projectId": p.ID})
	c.Conversations = count(store.CAssistantConversations, bson.M{"projectId": p.ID})
	c.Kept = count(store.CKeep, bson.M{"projectId": p.ID})
	c.Rules, c.Signoffs = len(p.Rules), len(p.Signoffs)
	c.Comments = count(store.CComments, bson.M{"projectId": p.ID})
	c.Todos = count(store.CTodos, bson.M{"projectId": p.ID})
	c.Notifications = count(store.CNotifications, bson.M{"projectId": p.ID})
	c.Links = count(store.CProjectLinks, bson.M{"$or": bson.A{bson.M{"projectId": p.ID}, bson.M{"sourceId": p.ID}}})
	c.Remotion = count(store.CRemotionSources, bson.M{"projectId": p.ID})
	c.ContentSettings = count(store.CContentSettings, bson.M{"projectId": p.ID}) > 0
	c.SpellingWords = count(store.CSpelling, bson.M{"projectId": p.ID})
	if g, err := content.Load(ctx, a); err == nil {
		for _, id := range g.Projects() {
			if uses, follows := g.Uses(id); id != p.ID && uses == models.ContentProject && follows == p.ID {
				c.Followers = append(c.Followers, g.Name(id))
			}
		}
		c.Default = g.Default == p.ID
	}

	// What moves: footage (never deleted), pictures and files.
	joins := map[string]bool{}
	var linkers []models.ProjectLink
	_ = a.DB.Find(ctx, store.CProjectLinks, ws, bson.M{"sourceId": p.ID}, &linkers)
	for _, l := range linkers {
		for _, it := range usedFrom(ctx, a, l.ProjectID, p.ID) {
			if it.Kind == models.LinkFootage {
				joins[it.ID] = true
			}
		}
	}
	var footage []models.Asset
	_ = a.DB.Find(ctx, store.CAssets, ws, bson.M{"projectIds": p.ID}, &footage, options.Find().SetProjection(bson.M{"projectIds": 1, "collectionIds": 1, "archivedAt": 1}))
	for _, as := range footage {
		switch {
		case joins[as.ID]:
			c.Joins++
		case len(as.ProjectIDs) == 1 && len(as.CollectionIDs) == 0 && as.ArchivedAt == nil:
			c.ToInbox++
		default:
			c.Stays++
		}
	}
	c.Resources = count(store.CResources, bson.M{"scope": models.ScopeProject, "projectId": p.ID})
	row.Goes, row.Moves = purgeWords(c)
	return row, nesters, nil
}

// purgeWords says what clearing out removes (goes) and what moves or stays, in plain words.
func purgeWords(c *PurgeCounts) (goes, moves []string) {
	goes, moves = []string{}, []string{}
	add := func(n int, one, many string) {
		switch {
		case n == 1:
			goes = append(goes, one)
		case n > 1:
			goes = append(goes, fmt.Sprintf(many, n))
		}
	}
	if c.Versions > 0 {
		cuts := "its cut"
		if c.Cuts > 1 {
			cuts = fmt.Sprintf("its %d cuts", c.Cuts)
		}
		if c.Versions == 1 {
			goes = append(goes, "1 version of "+cuts)
		} else {
			goes = append(goes, fmt.Sprintf("%d versions of %s", c.Versions, cuts))
		}
	}
	switch {
	case c.Renders == 1:
		goes = append(goes, "1 render ("+humanBytes(c.RenderBytes)+")")
	case c.Renders > 1:
		goes = append(goes, fmt.Sprintf("%d renders (%s)", c.Renders, humanBytes(c.RenderBytes)))
	}
	add(c.Checks, "1 quality check", "%d quality checks")
	add(c.Conversations, "1 conversation with Claude Code", "%d conversations with Claude Code")
	add(c.Kept, "1 thing it kept", "%d things it kept")
	add(c.Rules, "1 rule", "%d rules")
	add(c.Signoffs, "1 sign-off", "%d sign-offs")
	add(c.Comments, "1 comment", "%d comments")
	add(c.Todos, "1 to-do", "%d to-dos")
	add(c.Links, "1 link with another project", "%d links with other projects")
	add(c.Remotion, "1 Remotion project", "%d Remotion projects")
	if c.ContentSettings {
		goes = append(goes, "its Content Settings (brand, spelling list, narration voice and house rules)")
	}
	switch {
	case c.ToInbox == 1:
		moves = append(moves, "1 clip only it had goes back to the Inbox")
	case c.ToInbox > 1:
		moves = append(moves, fmt.Sprintf("%d clips only it had go back to the Inbox", c.ToInbox))
	}
	switch {
	case c.Joins == 1:
		moves = append(moves, "1 clip another project's cut uses joins that project")
	case c.Joins > 1:
		moves = append(moves, fmt.Sprintf("%d clips other projects' cuts use join those projects", c.Joins))
	}
	switch {
	case c.Stays == 1:
		moves = append(moves, "1 clip stays where else it is")
	case c.Stays > 1:
		moves = append(moves, fmt.Sprintf("%d clips stay where else they are", c.Stays))
	}
	switch {
	case c.Resources == 1:
		moves = append(moves, "1 picture or file moves to the workspace library")
	case c.Resources > 1:
		moves = append(moves, fmt.Sprintf("%d pictures and files move to the workspace library", c.Resources))
	}
	if len(c.Followers) > 0 {
		moves = append(moves, fmt.Sprintf("%s used its Content Settings: they use the default ones instead, and the people involved are told", quoteNames(c.Followers)))
	}
	if c.Default {
		moves = append(moves, "its Content Settings are the default for new projects: projects that use the default have none until another project's are ticked “Use for new projects”")
	}
	return goes, moves
}

func quoteNames(ns []string) string {
	q := make([]string, len(ns))
	for i, n := range ns {
		q[i] = "“" + n + "”"
	}
	return joinAnd(q)
}

// purgeQuestion is the warning a person confirms.
func purgeQuestion(rows []ProjectPurgeRow) string {
	var ok []string
	for _, r := range rows {
		if r.Status != "refused" {
			ok = append(ok, r.Name)
		}
	}
	switch len(ok) {
	case 0:
		return "None of these archived projects can be cleared out yet."
	case 1:
		return fmt.Sprintf("Delete “%s” for good?", ok[0])
	}
	return fmt.Sprintf("Clear out %d archived projects for good?", len(ok))
}

// purgeSummary says what happened (or would happen).
func purgeSummary(rows []ProjectPurgeRow, dry bool) string {
	var done, refused []string
	for _, r := range rows {
		switch r.Status {
		case "purged", "would":
			done = append(done, "“"+r.Name+"”")
		case "refused":
			refused = append(refused, fmt.Sprintf("“%s” stays: %s", r.Name, strings.TrimSuffix(r.Blockers[0], ".")))
		}
	}
	var parts []string
	if len(done) > 0 {
		if dry {
			parts = append(parts, fmt.Sprintf("Clearing out %s would delete it for good; its footage stays in the Library.", joinAnd(done)))
		} else {
			parts = append(parts, fmt.Sprintf("Cleared out %s for good. Footage only it had is back in the Inbox.", joinAnd(done)))
		}
	}
	for _, r := range refused {
		parts = append(parts, r+".")
	}
	return strings.Join(parts, " ")
}

// purgeProject clears one archived project out. Every step can run again, so a
// clear-out that stopped halfway is finished by running it again. It returns
// the stored files removed and kept.
func purgeProject(ctx context.Context, a *app.App, p *models.Project) (int, int, error) {
	ws := a.WorkspaceID()
	now := time.Now().UTC()
	// It can't come back meanwhile, and running it again finishes it.
	if p.Purging == nil {
		if _, err := a.DB.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": p.ID, "workspaceId": ws, "archived": true}, bson.M{"$set": bson.M{"purging": models.ProjectPurging{At: now, By: a.Actor}}}); err != nil {
			return 0, 0, err
		}
	}
	// Waiting work is called off (it would fail).
	if _, err := a.DB.C(store.CJobs).UpdateMany(ctx, bson.M{"workspaceId": ws, "projectId": p.ID, "state": models.JobQueued},
		bson.M{"$set": bson.M{"state": models.JobCanceled, "error": "the project was cleared out", "updatedAt": now}}); err != nil {
		return 0, 0, err
	}
	if err := purgeCheckpoint("jobs"); err != nil {
		return 0, 0, err
	}
	// Links both ways. What other projects' cuts use of it through a link
	// stays theirs: its footage joins them (its pictures and files go to the
	// workspace library below, its motion graphics stay in the Library).
	var linkers []models.ProjectLink
	if err := a.DB.Find(ctx, store.CProjectLinks, ws, bson.M{"sourceId": p.ID}, &linkers); err != nil {
		return 0, 0, err
	}
	for _, l := range linkers {
		for _, it := range usedFrom(ctx, a, l.ProjectID, p.ID) {
			if it.Kind != models.LinkFootage {
				continue
			}
			if err := a.DB.Update(ctx, store.CAssets, ws, it.ID, bson.M{"$addToSet": bson.M{"projectIds": l.ProjectID}}); err != nil && !errors.Is(err, store.ErrNotFound) {
				return 0, 0, err
			}
			refreshAssetTodos(ctx, a, it.ID)
		}
		refreshProjectCounts(ctx, a, l.ProjectID)
		if _, err := a.DB.C(store.CProjectLinks).DeleteOne(ctx, bson.M{"_id": l.ID, "workspaceId": ws}); err != nil {
			return 0, 0, err
		}
	}
	if _, err := a.DB.C(store.CProjectLinks).DeleteMany(ctx, bson.M{"workspaceId": ws, "projectId": p.ID}); err != nil {
		return 0, 0, err
	}
	if err := purgeCheckpoint("links"); err != nil {
		return 0, 0, err
	}
	// Footage leaves it (never deleted): in the Inbox when nothing else has it.
	var footage []struct {
		ID string `bson:"_id"`
	}
	if err := a.DB.Find(ctx, store.CAssets, ws, bson.M{"$or": bson.A{bson.M{"projectIds": p.ID}, bson.M{"trash.projectIds": p.ID}}}, &footage, options.Find().SetProjection(bson.M{"_id": 1})); err != nil {
		return 0, 0, err
	}
	for _, as := range footage {
		if err := a.DB.Update(ctx, store.CAssets, ws, as.ID, bson.M{"$pull": bson.M{"projectIds": p.ID, "trash.projectIds": p.ID}, "$set": bson.M{"updatedAt": now}}); err != nil && !errors.Is(err, store.ErrNotFound) {
			return 0, 0, err
		}
		refreshAssetTodos(ctx, a, as.ID)
	}
	if _, err := a.DB.C(store.CConversations).UpdateMany(ctx, bson.M{"workspaceId": ws, "projectIds": p.ID}, bson.M{"$pull": bson.M{"projectIds": p.ID}}); err != nil {
		return 0, 0, err
	}
	// Screen recordings stay in the Library: no longer linked into it, nor recorded for it.
	if _, err := a.DB.C(store.CRecordings).UpdateMany(ctx, bson.M{"workspaceId": ws, "links.projectId": p.ID}, bson.M{"$pull": bson.M{"links": bson.M{"projectId": p.ID}}}); err != nil {
		return 0, 0, err
	}
	if _, err := a.DB.C(store.CRecordings).UpdateMany(ctx, bson.M{"workspaceId": ws, "projectId": p.ID}, bson.M{"$unset": bson.M{"projectId": ""}}); err != nil {
		return 0, 0, err
	}
	if err := purgeCheckpoint("footage"); err != nil {
		return 0, 0, err
	}
	// Pictures and files move to the workspace library; its linked folders
	// and motion graphics become the workspace's.
	var res []models.Resource
	if err := a.DB.Find(ctx, store.CResources, ws, bson.M{"scope": models.ScopeProject, "projectId": p.ID}, &res); err != nil {
		return 0, 0, err
	}
	for i := range res {
		if err := resources.Promote(ctx, a, &res[i]); err != nil {
			return 0, 0, err
		}
	}
	for _, coll := range []string{store.CLinkedFolders, store.CMotion} {
		if _, err := a.DB.C(coll).UpdateMany(ctx, bson.M{"workspaceId": ws, "projectId": p.ID}, bson.M{"$unset": bson.M{"projectId": ""}}); err != nil {
			return 0, 0, err
		}
	}
	if err := purgeCheckpoint("resources"); err != nil {
		return 0, 0, err
	}
	// What is the project's goes.
	for _, coll := range []string{store.CKeep, store.CComments, store.CNotifications, store.CSpelling, store.CCaptionTranslations, store.CContentSettings,
		store.CRemotionPushes, store.CRemotionRefreshes, store.CRemotionRenders, store.CRemotionVersions, store.CRemotionSources, store.CSpokenChanges} {
		if _, err := a.DB.C(coll).DeleteMany(ctx, bson.M{"workspaceId": ws, "projectId": p.ID}); err != nil {
			return 0, 0, err
		}
	}
	if _, err := a.DB.C(store.CTodos).DeleteMany(ctx, bson.M{"workspaceId": ws, "projectId": p.ID}); err != nil {
		return 0, 0, err
	}
	if _, err := a.DB.C(store.CTodos).UpdateMany(ctx, bson.M{"workspaceId": ws, "projectIds": p.ID}, bson.M{"$pull": bson.M{"projectIds": p.ID}}); err != nil {
		return 0, 0, err
	}
	if err := purgeCheckpoint("records"); err != nil {
		return 0, 0, err
	}
	// Its Claude Code conversations and their saved proposals.
	purgeHooksMu.Lock()
	hooks := make([]PurgeHook, 0, len(purgeHooks))
	for _, h := range purgeHooks {
		hooks = append(hooks, h)
	}
	purgeHooksMu.Unlock()
	for _, h := range hooks {
		if _, err := h(ctx, a, p.ID); err != nil {
			return 0, 0, err
		}
	}
	// (Without the assistant package, its records go here.)
	if _, err := a.DB.C(store.CAssistantConversations).DeleteMany(ctx, bson.M{"workspaceId": ws, "projectId": p.ID}); err != nil {
		return 0, 0, err
	}
	if err := purgeCheckpoint("conversations"); err != nil {
		return 0, 0, err
	}
	// Its renders' stored files when nothing else uses the same bytes (the
	// records go after, so running it again finds them), then the renders,
	// their quality checks and its versions.
	removed, kept, err := purgeRenderFiles(ctx, a, p.ID)
	if err != nil {
		return 0, 0, err
	}
	if err := purgeCheckpoint("files"); err != nil {
		return 0, 0, err
	}
	for _, coll := range []string{store.CQAReports, store.CRenders, store.CDrafts, store.CRevisions} {
		if _, err := a.DB.C(coll).DeleteMany(ctx, bson.M{"workspaceId": ws, "projectId": p.ID}); err != nil {
			return 0, 0, err
		}
	}
	if err := purgeCheckpoint("versions"); err != nil {
		return 0, 0, err
	}
	// Last, the project: projects that followed its Content Settings use the
	// default ones and the people involved are told; a default that was its is cleared.
	if _, err := a.DB.C(store.CProjects).DeleteOne(ctx, bson.M{"_id": p.ID, "workspaceId": ws}); err != nil {
		return 0, 0, err
	}
	_ = content.ProjectGone(ctx, a, p.ID)
	a.Audit(ctx, "project.purge", "project", p.ID, p.ID, map[string]any{"name": p.Name, "storedRemoved": removed, "storedKept": kept})
	return removed, kept, nil
}

// purgeRenderFiles deletes the stored files of a project's renders and their
// quality checks that nothing else uses (purgemedia.go); it returns how many
// went and how many stay because something else uses the same bytes.
func purgeRenderFiles(ctx context.Context, a *app.App, projectID string) (int, int, error) {
	objs, err := purgeRenderObjects(ctx, a, projectID)
	if err != nil {
		return 0, 0, err
	}
	used, err := mediaInUse(ctx, a, objs, []string{projectID})
	if err != nil {
		return 0, 0, err // MetaRush couldn't tell what else uses them: nothing is deleted
	}
	removed, kept := 0, 0
	shas := make([]string, 0, len(objs))
	for sha := range objs {
		shas = append(shas, sha)
	}
	sort.Strings(shas)
	for _, sha := range shas {
		if used[sha] {
			kept++
			continue
		}
		if err := a.Media.Delete(ctx, objs[sha].Key); err != nil && !errors.Is(err, media.ErrNotFound) && !errors.Is(err, os.ErrNotExist) {
			return removed, kept, err
		}
		_, _ = a.Cache.Remove(sha)
		removed++
	}
	return removed, kept, nil
}
