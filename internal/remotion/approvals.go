package remotion

import (
	"context"
	"fmt"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
)

// projectName is a project's name ("" when it can't be read).
func projectName(ctx context.Context, a *app.App, id string) string {
	var p models.Project
	if id == "" || a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), id, &p) != nil {
		return ""
	}
	return p.Name
}

// A version waiting for a person (D172): when a version of a Remotion source
// can be approved — read in the jail, its packages allowed, its check passed —
// a to-do asks a person to look at it and approve it or turn it down (one open
// question per source: the newest waiting version), and the bell tells the
// person who made it. Deciding closes the question, wherever it was decided.

func approvalKey(s *models.RemotionSource) string { return "remotion-approval:" + s.ID + "@" }

// approvable reports whether a person can approve the version now.
func approvable(v *models.RemotionVersion) bool {
	return v.State == models.RemotionWaiting && v.PackagesAllowed && v.Read != nil && v.Read.OK && (v.Check == nil || v.Check.OK)
}

// remindApproval opens (or closes) the version's question and rings the bell
// once for it. Best effort: a to-do or notification that can't be written
// never fails the work that made the version.
func remindApproval(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion) {
	if s == nil || v == nil {
		return
	}
	key := approvalKey(s)
	if !approvable(v) {
		resolution := models.ResolvedObsolete
		if v.State != models.RemotionWaiting {
			resolution = models.ResolvedElsewhere
		}
		_, _ = todos.Close(ctx, a, key+v.Hash, resolution, v.State)
		return
	}
	// Only the newest version waiting asks; older questions about this source close.
	if newer, err := newestWaiting(ctx, a, s); err == nil && newer != nil && newer.Number > v.Number {
		_, _ = todos.Close(ctx, a, key+v.Hash, models.ResolvedObsolete, "a newer version waits")
		return
	}
	_, _ = todos.CloseStale(ctx, a, key, []string{key + v.Hash})
	project := projectName(ctx, a, s.ProjectID)
	link := links.Remotion(s.ProjectID, s.ID, v.Hash)
	q := fmt.Sprintf("Can version %d of “%s” go into cuts?", v.Number, s.Title)
	if v.Parent == "" {
		q = fmt.Sprintf("Can the Remotion project “%s” go into cuts?", s.Title)
	}
	detail := "Look at its compositions under Sources first; an approved version's compositions can become clips."
	if v.Parent != "" {
		what := strings.TrimSpace(v.Message)
		if v.Check != nil && v.Check.Summary != "" {
			what = strings.TrimSpace(strings.TrimSuffix(v.Check.Summary, " It waits for a person to approve it."))
		}
		detail = "Look at its before and after pictures under Sources first; approving it moves the latest cuts' clips to it."
		if what != "" {
			detail = clipText(what, 400) + " " + detail
		}
	}
	_, _ = todos.Upsert(ctx, a, models.Todo{Kind: models.TodoRemotionApproval, Scope: "project", ProjectID: s.ProjectID, ProjectIDs: []string{s.ProjectID},
		GroupKey: key + v.Hash, HumanOnly: true, Priority: 2, Question: q, Detail: detail,
		Context: models.TodoContext{ProjectName: project, Link: link,
			Remotion: &models.TodoRemotion{SourceID: s.ID, Name: s.Name, Title: s.Title, Version: v.Hash, Number: v.Number}},
		Suggestions: []models.TodoSuggestion{
			{Label: "Approve this version", Value: "approve"},
			{Label: "Not this one", Value: "reject"},
		}})
	// The bell, for the person who made the version (once per version).
	title := fmt.Sprintf("Version %d of “%s” waits for your approval", v.Number, s.Title)
	if v.Parent == "" {
		title = fmt.Sprintf("“%s” waits for your approval", s.Title)
	}
	_, err := notify.Upsert(ctx, a, models.Notification{UserID: v.By.UserID, Kind: models.NotifyRemotionWaiting, GroupKey: "remotion-waiting:" + v.Hash,
		ProjectID: s.ProjectID, Tone: "info", Title: title, Body: strings.TrimSpace(project + ". Look at it under Sources, then approve it or turn it down."), Link: link,
		Details: map[string]any{"source": s.Name, "version": v.Hash, "versionNumber": v.Number}}, notify.Once)
	_ = err
}

// newestWaiting is the source's newest version that waits for a person.
func newestWaiting(ctx context.Context, a *app.App, s *models.RemotionSource) (*models.RemotionVersion, error) {
	vs, err := versionsOf(ctx, a, s.ID)
	if err != nil {
		return nil, err
	}
	var best *models.RemotionVersion
	for i := range vs {
		if approvable(&vs[i]) && (best == nil || vs[i].Number > best.Number) {
			best = &vs[i]
		}
	}
	return best, nil
}

// decided closes the question about a version once a person decided about it.
func decided(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion) {
	if s == nil || v == nil {
		return
	}
	_, _ = todos.Close(ctx, a, approvalKey(s)+v.Hash, models.ResolvedElsewhere, v.State)
}
