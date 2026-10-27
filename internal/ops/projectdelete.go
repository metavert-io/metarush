package ops

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Deleting a project archives it (D196). Anyone working in the workspace may
// delete one, people only: it leaves Home, the project lists and search and
// waits in Archived projects, where anyone can restore it. Nothing in it
// changes: its footage stays in the Library, its versions, renders and Claude
// Code conversations stay as they are (the conversations readable, with a line
// that says the project is archived). Clearing archived projects out for good
// is an owner's (project.purge, purge.go).

// ProjectDeleteIn deletes (archives) a project.
type ProjectDeleteIn struct {
	ID string `json:"id" jsonschema:"minLength=1,description=The project (its ID or name)."`
}

// ProjectRestoreIn brings an archived project back.
type ProjectRestoreIn struct {
	ID string `json:"id" jsonschema:"minLength=1,description=The archived project (its ID or name)."`
}

// ProjectArchiveOut says what deleting or restoring did.
type ProjectArchiveOut struct {
	Project models.Project `json:"project"`
	// Already: it was archived (or in the projects) before.
	Already bool   `json:"already,omitempty"`
	Message string `json:"message"`
	URL     string `json:"url"`
	// ArchivedURL is the dashboard's Archived projects page.
	ArchivedURL string `json:"archivedUrl"`
}

// ArchivedProjectsPath is the dashboard's page of archived projects.
const ArchivedProjectsPath = "/projects/archived"

// archiveProject marks a project deleted (archived), recording when and who.
func archiveProject(ctx context.Context, a *app.App, p *models.Project) (bool, error) {
	if p.Archived {
		return true, nil
	}
	now := time.Now().UTC()
	actor := a.Actor
	if err := a.DB.Update(ctx, store.CProjects, a.WorkspaceID(), p.ID, bson.M{"$set": bson.M{"archived": true, "archivedAt": now, "archivedBy": actor, "updatedAt": now}}); err != nil {
		return false, notFound("project", p.ID)
	}
	p.Archived, p.ArchivedAt, p.ArchivedBy = true, &now, &actor
	a.Audit(ctx, "project.delete", "project", p.ID, p.ID, map[string]any{"name": p.Name})
	return false, nil
}

// restoreProject brings an archived project back to the project lists.
func restoreProject(ctx context.Context, a *app.App, p *models.Project) (bool, error) {
	if p.Purging != nil {
		return false, apperr.New(apperr.Conflict, "“%s” is being cleared out for good, so it can't come back.", p.Name).
			WithHint("An owner finishes clearing it out from Archived projects.").WithDetails(map[string]any{"reason": "purging", "projectId": p.ID})
	}
	if !p.Archived {
		return true, nil
	}
	now := time.Now().UTC()
	if err := a.DB.Update(ctx, store.CProjects, a.WorkspaceID(), p.ID, bson.M{"$set": bson.M{"archived": false, "updatedAt": now},
		"$unset": bson.M{"archivedAt": "", "archivedBy": ""}}); err != nil {
		return false, notFound("project", p.ID)
	}
	p.Archived, p.ArchivedAt, p.ArchivedBy = false, nil, nil
	a.Audit(ctx, "project.restore", "project", p.ID, p.ID, map[string]any{"name": p.Name})
	return false, nil
}

// refuseArchiving keeps deleting a project a person's: project.update's
// archived flag deletes it too.
func refuseArchiving(ctx context.Context) error {
	if callerOf(ctx).Human {
		return nil
	}
	return apperr.New(apperr.HumanOnly, "Only a person can delete a project.").
		WithHint("Delete it in the dashboard (the project's “…” menu), or in your own terminal.").WithCommand("metarush project delete <project>")
}

func archiveOut(a *app.App, p *models.Project, already bool, msg string) *ProjectArchiveOut {
	return &ProjectArchiveOut{Project: *p, Already: already, Message: msg, URL: dashboardURL(a, "/p/"+p.ID), ArchivedURL: dashboardURL(a, ArchivedProjectsPath)}
}

func init() {
	Def("project.delete", "Delete a project: it moves to Archived projects, where anyone can restore it, and leaves Home, the project lists and search (its footage stays in the Library); people only", Flags{Mutating: true, HumanOnly: true}, func(ctx context.Context, a *app.App, in *ProjectDeleteIn) (*ProjectArchiveOut, error) {
		p, err := resolveProject(ctx, a, in.ID)
		if err != nil {
			return nil, err
		}
		already, err := archiveProject(ctx, a, p)
		if err != nil {
			return nil, err
		}
		if already {
			return archiveOut(a, p, true, fmt.Sprintf("“%s” is already in Archived projects.", p.Name)), nil
		}
		return archiveOut(a, p, false, fmt.Sprintf("Deleted “%s”. It is in Archived projects, where you can restore it; its footage stays in the Library.", p.Name)), nil
	})
	Def("project.restore", "Bring a deleted (archived) project back to Home and the project lists, as it was", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *ProjectRestoreIn) (*ProjectArchiveOut, error) {
		p, err := resolveProject(ctx, a, in.ID)
		if err != nil {
			return nil, err
		}
		already, err := restoreProject(ctx, a, p)
		if err != nil {
			return nil, err
		}
		if already {
			return archiveOut(a, p, true, fmt.Sprintf("“%s” isn't archived: it is in your projects.", p.Name)), nil
		}
		return archiveOut(a, p, false, fmt.Sprintf("Restored “%s”. It is back in your projects.", p.Name)), nil
	})
}
