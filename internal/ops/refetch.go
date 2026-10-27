package ops

import (
	"context"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/resources"
)

// refetch refreshes a repository (its newest commit, or the ref asked for)
// or a web page (a new snapshot): a fetch job that adds a new version.
func refetch(ctx context.Context, a *app.App, e *resources.Entry, view *projectlinks.View, ref string) (*ResourceOut, error) {
	r := &e.Resource
	v := r.Latest()
	if v == nil {
		return nil, apperr.New(apperr.NotFound, "%s has no versions", r.Title)
	}
	req := resources.FetchRequest{ResourceID: r.ID, ProjectID: r.ProjectID, Workspace: r.Scope == models.ScopeWorkspace}
	var note string
	switch r.Kind {
	case models.ResourceRepo:
		if v.Repo == nil {
			return nil, apperr.New(apperr.NotFound, "%s has no commit to start from", r.Title)
		}
		req.Kind, req.URL, req.Ref = resources.FetchRepo, v.Source.URL, firstNonEmpty(ref, v.Repo.Ref)
		note = " Cuts keep showing what they show now; code slides and cards made from it get new versions you can update to."
	default:
		if ref != "" {
			return nil, apperr.New(apperr.InvalidInput, "Only a repository can be pinned to a branch, tag or commit.")
		}
		if v.Site == nil {
			return nil, apperr.New(apperr.NotFound, "%s has no page to take again", r.Title)
		}
		req.Kind, req.URL = resources.FetchPage, firstNonEmpty(v.Site.Requested, v.Site.URL)
		note = " Cuts keep the older snapshot until you update them."
		if v.Site.Via == "extension" {
			note += " This one came from your browser: MetaRush's own snapshot is taken signed out, so it may look different (send the page again from the extension for what you see)."
		}
	}
	q, err := resources.QueueFetch(ctx, a, req)
	if err != nil {
		return nil, err
	}
	a.Audit(ctx, "resource.refresh", "resource", r.ID, r.ProjectID, map[string]any{"kind": r.Kind, "job": q.JobID})
	return &ResourceOut{Resource: ResourceRowOf(*e, view, folderNames(ctx, a)), Message: q.Message + note, JobID: q.JobID}, nil
}
