package ops

import (
	"context"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/store"
)

// ProjectCreateIn creates a project.
type ProjectCreateIn struct {
	Name  string       `json:"name" jsonschema:"minLength=1,maxLength=120"`
	Brief models.Brief `json:"brief,omitempty"`
}

// ProjectOut is a project with a deep link.
type ProjectOut struct {
	Project models.Project `json:"project"`
	URL     string         `json:"url"`
}

// ProjectListIn filters projects.
type ProjectListIn struct {
	IncludeArchived bool `json:"includeArchived,omitempty"`
	// ArchivedOnly lists only deleted (archived) projects, most recently
	// deleted first: the dashboard's Archived projects (D196).
	ArchivedOnly bool `json:"archivedOnly,omitempty"`
}

// ProjectSummary is a list row.
type ProjectSummary struct {
	models.Project
	HeadRevisionID     string `json:"headRevisionId,omitempty"`
	HeadRevisionNumber int    `json:"headRevisionNumber,omitempty"`
	HeadApproval       string `json:"headApproval,omitempty"`
	ThumbAssetID       string `json:"thumbAssetId,omitempty"`
	URL                string `json:"url"`
}

// ProjectListOut lists projects.
type ProjectListOut struct {
	Projects []ProjectSummary `json:"projects"`
}

// ProjectShowIn selects a project.
type ProjectShowIn struct {
	ID string `json:"id"`
}

// ProjectShowOut is a project dashboard summary.
type ProjectShowOut struct {
	Project        models.Project `json:"project"`
	HeadRevisionID string         `json:"headRevisionId,omitempty"`
	// Assets are the project's sources, then the footage its project links
	// bring in (those carry From: the project they come from; nothing is copied).
	// Pictures are resources (resources.list), not sources.
	Assets []AssetSummary `json:"assets"`
	// Links are the projects this one uses (D83).
	Links     []ProjectLinkRow  `json:"links"`
	Revisions []RevisionSummary `json:"revisions"`
	Renders   []models.Render   `json:"renders"`
	OpenJobs  int               `json:"openJobs"`
	URL       string            `json:"url"`
	// Cuts are the project's cuts by their descriptive names (D150), main first.
	Cuts []CutView `json:"cuts"`
	// HouseRules are the house rules of the Content Settings it uses (D193),
	// marked with whose settings hold them.
	HouseRules []models.ProjectRule `json:"houseRules,omitempty"`
	// HouseSignoffs are the house sign-offs of the Content Settings it uses
	// (D200) for its main cut, with whether each was given here.
	HouseSignoffs []models.Signoff `json:"houseSignoffs,omitempty"`
}

// ProjectUpdateIn edits name/brief.
type ProjectUpdateIn struct {
	ID       string        `json:"id"`
	Name     string        `json:"name,omitempty" jsonschema:"maxLength=120"`
	Brief    *models.Brief `json:"brief,omitempty"`
	Archived *bool         `json:"archived,omitempty"`
	// TransitionStyle changes only the brief's transition style ("" clears it).
	TransitionStyle *string `json:"transitionStyle,omitempty" jsonschema:"enum=natural,enum=snappy,enum=smooth,enum=playful,enum=,description=Set just the transition style the editing skills follow (empty clears it)."`
}

// checkTransitionStyle refuses styles that do not exist.
func checkTransitionStyle(v string) error {
	if v == "" {
		return nil
	}
	for _, s := range editspec.TransitionStyles {
		if v == s {
			return nil
		}
	}
	return invalid("There's no transition style called %q: choose natural, snappy, smooth or playful.", v)
}

// WorkspaceShowIn has no fields.
type WorkspaceShowIn struct{}

// WorkspaceShowOut describes the connected workspace.
type WorkspaceShowOut struct {
	Workspace     models.Workspace      `json:"workspace"`
	Installation  models.Installation   `json:"installation"`
	Installations []models.Installation `json:"installations"`
	Database      string                `json:"database"`
	MediaStore    string                `json:"mediaStore"`
	MediaShared   bool                  `json:"mediaShared"`
	CacheDir      string                `json:"cacheDir"`
	Dashboard     string                `json:"dashboard"`
}

// WorkerListIn has no fields.
type WorkerListIn struct{}

// WorkerRow adds liveness to a worker device.
type WorkerRow struct {
	models.WorkerDevice
	Online bool `json:"online"`
}

// WorkerListOut lists workers.
type WorkerListOut struct {
	Workers []WorkerRow `json:"workers"`
}

func init() {
	Def("project.create", "Create a project with an optional creative brief", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *ProjectCreateIn) (*ProjectOut, error) {
		name := strings.TrimSpace(in.Name)
		if name == "" {
			return nil, invalid("Give the project a name.")
		}
		if err := checkTransitionStyle(in.Brief.TransitionStyle); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		p := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: name, Brief: in.Brief, Branches: map[string]models.BranchHead{},
			CreatedAt: now, CreatedBy: a.Actor, UpdatedAt: now}
		if err := a.DB.Insert(ctx, store.CProjects, p); err != nil {
			return nil, err
		}
		a.Audit(ctx, "project.create", "project", p.ID, p.ID, map[string]any{"name": name})
		// New projects use the default Content Settings (D192); a workspace's
		// first project holds them (with what the workspace kept before).
		content.FirstProject(ctx, a, p.ID)
		if np, err := resolveProject(ctx, a, p.ID); err == nil {
			p = *np
		}
		return &ProjectOut{Project: p, URL: dashboardURL(a, "/p/"+p.ID)}, nil
	})
	Def("project.list", "List projects (most recently updated first)", Flags{}, func(ctx context.Context, a *app.App, in *ProjectListIn) (*ProjectListOut, error) {
		filter := bson.M{}
		order := bson.D{{Key: "updatedAt", Value: -1}}
		switch {
		case in.ArchivedOnly:
			filter["archived"] = true
			order = bson.D{{Key: "archivedAt", Value: -1}, {Key: "updatedAt", Value: -1}}
		case !in.IncludeArchived:
			filter["archived"] = false
		}
		var ps []models.Project
		if err := a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), filter, &ps, options.Find().SetSort(order).SetLimit(500)); err != nil {
			return nil, err
		}
		out := &ProjectListOut{Projects: []ProjectSummary{}}
		var heads []string
		for _, p := range ps {
			if h, ok := p.Branches["main"]; ok {
				heads = append(heads, h.Head)
			}
		}
		headInfo := map[string]models.Revision{}
		if len(heads) > 0 {
			var revs []models.Revision
			_ = a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": heads}}, &revs, options.Find().SetProjection(bson.M{"spec": 0, "ops": 0}))
			for _, r := range revs {
				headInfo[r.ID] = r
			}
		}
		for _, p := range ps {
			s := ProjectSummary{Project: p, URL: dashboardURL(a, "/p/"+p.ID), ThumbAssetID: p.CoverAssetID}
			if h, ok := p.Branches["main"]; ok {
				s.HeadRevisionID = h.Head
				if r, ok := headInfo[h.Head]; ok {
					s.HeadRevisionNumber, s.HeadApproval = r.Number, r.Approval.State
				}
			}
			out.Projects = append(out.Projects, s)
		}
		return out, nil
	})
	Def("project.show", "Show a project: assets, revisions, renders and the current head", Flags{}, func(ctx context.Context, a *app.App, in *ProjectShowIn) (*ProjectShowOut, error) {
		var p models.Project
		if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), in.ID, &p); err != nil {
			if ids.Valid(ids.Project, in.ID) {
				return nil, notFound("project", in.ID)
			}
			// A project's name works too.
			found, rerr := resolveProject(ctx, a, in.ID)
			if rerr != nil {
				return nil, rerr
			}
			p = *found
		}
		out := &ProjectShowOut{Project: p, Assets: []AssetSummary{}, Links: []ProjectLinkRow{}, Revisions: []RevisionSummary{}, Renders: []models.Render{}, URL: dashboardURL(a, "/p/"+p.ID)}
		if h, ok := p.Branches["main"]; ok {
			out.HeadRevisionID = h.Head
		}
		assets, err := listAssets(ctx, a, bson.M{"projectIds": p.ID, "kind": bson.M{"$ne": "image"}}, 1000)
		if err != nil {
			return nil, err
		}
		out.Assets = assets
		if view, err := projectlinks.Load(ctx, a, p.ID); err == nil && len(view.Links) > 0 {
			linked, from, err := view.LinkedAssets(ctx, a)
			if err != nil {
				return nil, err
			}
			for _, as := range linked {
				if as.Kind == "image" || slices.Contains(as.ProjectIDs, p.ID) {
					continue
				}
				s := summarize(as)
				s.FromProjectID, s.From, s.FromCollection = from[as.ID], view.Names[from[as.ID]], projectlinks.IsCollection(from[as.ID])
				out.Assets = append(out.Assets, s)
			}
			for _, l := range view.Links {
				out.Links = append(out.Links, linkRow(ctx, a, l))
			}
		}
		revs, err := listRevisions(ctx, a, p.ID, "", 50)
		if err != nil {
			return nil, err
		}
		out.Revisions = revs
		if err := a.DB.Find(ctx, store.CRenders, a.WorkspaceID(), bson.M{"projectId": p.ID}, &out.Renders, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(20)); err != nil {
			return nil, err
		}
		if out.Renders == nil {
			out.Renders = []models.Render{}
		}
		n, _ := a.DB.C(store.CJobs).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectId": p.ID, "state": bson.M{"$in": bson.A{models.JobQueued, models.JobRunning}}})
		out.OpenJobs = int(n)
		out.Cuts = Cuts(ctx, a, &p)
		if out.Cuts == nil {
			out.Cuts = []CutView{}
		}
		for _, r := range content.RulesFor(ctx, a, &p) {
			if r.House {
				out.HouseRules = append(out.HouseRules, r)
			}
		}
		for _, s := range SignoffsFor(ctx, a, &p, "main") {
			if s.House {
				out.HouseSignoffs = append(out.HouseSignoffs, s)
			}
		}
		return out, nil
	})
	Def("project.update", "Rename a project, edit its brief, or archive it (archiving deletes it: people only, like project.delete)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *ProjectUpdateIn) (*ProjectOut, error) {
		if in.Archived != nil {
			// Archived is deleted (D196): the same as project.delete and
			// project.restore, so when and who are recorded.
			p, err := resolveProject(ctx, a, in.ID)
			if err != nil {
				return nil, err
			}
			if *in.Archived {
				if err := refuseArchiving(ctx); err != nil {
					return nil, err
				}
				_, err = archiveProject(ctx, a, p)
			} else {
				_, err = restoreProject(ctx, a, p)
			}
			if err != nil {
				return nil, err
			}
			in.ID, in.Archived = p.ID, nil
		}
		set := bson.M{"updatedAt": time.Now().UTC()}
		unset := bson.M{}
		if n := strings.TrimSpace(in.Name); n != "" {
			set["name"] = n
		}
		if in.Brief != nil {
			if err := checkTransitionStyle(in.Brief.TransitionStyle); err != nil {
				return nil, err
			}
			// Where the video goes carries its shape (D166): "social shorts" is vertical.
			if len(in.Brief.Deliverables) > 12 {
				return nil, invalid("A brief takes 12 places the video goes at most.")
			}
			in.Brief.Deliverables = deliverablesOf(in.Brief.Deliverables)
			set["brief"] = *in.Brief
		}
		if in.TransitionStyle != nil {
			if err := checkTransitionStyle(*in.TransitionStyle); err != nil {
				return nil, err
			}
			if *in.TransitionStyle == "" {
				unset["brief.transitionStyle"] = ""
			} else {
				set["brief.transitionStyle"] = *in.TransitionStyle
			}
		}
		upd := bson.M{"$set": set}
		if len(unset) > 0 {
			upd["$unset"] = unset
		}
		if err := a.DB.Update(ctx, store.CProjects, a.WorkspaceID(), in.ID, upd); err != nil {
			return nil, notFound("project", in.ID)
		}
		var details map[string]any
		if in.TransitionStyle != nil {
			details = map[string]any{"transitionStyle": *in.TransitionStyle}
		}
		a.Audit(ctx, "project.update", "project", in.ID, in.ID, details)
		var p models.Project
		_ = a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), in.ID, &p)
		closeBriefTodos(ctx, a, &p)
		return &ProjectOut{Project: p, URL: dashboardURL(a, "/p/"+p.ID)}, nil
	})
	Def("workspace.show", "Show the connected workspace, installations and storage", Flags{}, func(ctx context.Context, a *app.App, _ *WorkspaceShowIn) (*WorkspaceShowOut, error) {
		out := &WorkspaceShowOut{Database: a.Cfg.Database.Name, MediaStore: a.Media.Locator(), MediaShared: a.Media.Capabilities().Shared, CacheDir: a.Cfg.Cache.Dir, Dashboard: dashboardURL(a, "/")}
		if err := a.DB.GetByID(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), &out.Workspace); err != nil {
			// workspace docs are keyed by _id and also carry workspaceId = _id
			return nil, notFound("workspace", a.WorkspaceID())
		}
		_ = a.DB.GetByID(ctx, store.CInstallations, a.WorkspaceID(), a.Cfg.Installation.ID, &out.Installation)
		_ = a.DB.Find(ctx, store.CInstallations, a.WorkspaceID(), nil, &out.Installations, options.Find().SetSort(bson.D{{Key: "lastSeenAt", Value: -1}}))
		return out, nil
	})
	Def("worker.list", "List workers, their capabilities, load and liveness", Flags{}, func(ctx context.Context, a *app.App, _ *WorkerListIn) (*WorkerListOut, error) {
		var ws []models.WorkerDevice
		if err := a.DB.Find(ctx, store.CWorkers, a.WorkspaceID(), nil, &ws, options.Find().SetSort(bson.D{{Key: "lastHeartbeat", Value: -1}})); err != nil {
			return nil, err
		}
		out := &WorkerListOut{Workers: []WorkerRow{}}
		for _, w := range ws {
			out.Workers = append(out.Workers, WorkerRow{WorkerDevice: w, Online: time.Since(w.LastHeartbeat) < 45*time.Second})
		}
		return out, nil
	})
}

// OnlineWorkers returns workers that heartbeated recently and can run
// jobType, by the database server's time (heartbeats are written in it, and
// a terminal decides by it whether to do the work itself: D360).
func OnlineWorkers(ctx context.Context, a *app.App, jobType string) ([]models.WorkerDevice, error) {
	var ws []models.WorkerDevice
	filter := store.SeenWithin("lastHeartbeat", 45*time.Second) // jobs.OnlineWindow
	filter["jobTypes"] = jobType
	err := a.DB.Find(ctx, store.CWorkers, a.WorkspaceID(), filter, &ws)
	return ws, err
}
