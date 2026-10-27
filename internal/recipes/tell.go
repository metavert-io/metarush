package recipes

import (
	"context"
	"fmt"

	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/todos"
)

// Telling people (D204): a run that waits says so plainly on To-dos and the
// bell ("Waiting for your OK: …", "Waiting for Claude to choose the
// moments …"), the activity bar reads the runs themselves; a run that is ready
// for review, or couldn't finish, rings the bell once.

// todoKey is a run's to-do group: one open per run and step.
func todoKey(run *models.RecipeRun, step string) string { return "recipe-run:" + run.ID + ":" + step }

// tell brings To-dos and the bell up to date with a run.
func (e *engine) tell(ctx context.Context) {
	run := e.run
	link := links.RecipeRun(run.ID)
	keep := []string{}
	if run.State == models.RunWaiting {
		step := ""
		for _, s := range run.Steps {
			if s.State == models.StepWaiting {
				step = s.ID
			}
		}
		key := todoKey(run, step)
		keep = append(keep, key)
		t := models.Todo{Kind: models.TodoRecipeRun, Scope: "project", ProjectID: run.ProjectID, ProjectIDs: nonEmpty(run.ProjectID), GroupKey: key,
			Question: upperFirst(runName(run)) + ": " + run.Waiting, Priority: 1,
			Context: models.TodoContext{ProjectName: run.ProjectName, Link: link,
				Recipe: &models.TodoRecipe{RunID: run.ID, Name: run.RecipeName, Title: run.RecipeTitle, Number: run.Number, WaitingFor: run.WaitingFor}}}
		if run.WaitingFor == models.RunWaitsForPerson && step == stepApprove {
			// The sample (D294): approved only after watching it, on the run's page.
			t.HumanOnly = true
			t.Detail = "Open the run to watch and listen to the sample, then approve it (the rest of the run is planned then) or say what isn't right."
			t.Suggestions = []models.TodoSuggestion{{Label: "Stop this run", Value: "stop"}}
		} else if run.WaitingFor == models.RunWaitsForPerson {
			t.HumanOnly = true
			t.Detail = "Going on spends only what the run still needs; budgets and today's limits still hold."
			t.Suggestions = []models.TodoSuggestion{{Label: "Go on", Value: "continue"}, {Label: "Stop this run", Value: "stop"}}
		} else {
			t.Detail = "Ask Claude to choose them (in the panel or a terminal, with the video-recipe skill): it keeps MetaRush's first picks or changes them. Or keep or change them yourself on the run's page or in the cut; the run goes on by itself once every pick is kept or changed and the stand-ins are replaced."
			t.Suggestions = []models.TodoSuggestion{{Label: "Stop this run", Value: "stop"}}
		}
		_, _ = todos.Upsert(ctx, e.a, t)
		tone := "warning"
		if run.WaitingFor == models.RunWaitsForClaude {
			tone = "info"
		}
		_, _ = notify.Upsert(ctx, e.a, models.Notification{UserID: run.CreatedBy.UserID, Kind: models.NotifyRecipeRun, GroupKey: "recipe-run:" + run.ID,
			Tone: tone, Title: upperFirst(runName(run)) + " waits", Body: run.Waiting, Link: link, ProjectID: run.ProjectID}, notify.Refresh)
	}
	_, _ = todos.CloseStale(ctx, e.a, "recipe-run:"+run.ID+":", keep)
	switch run.State {
	case models.RunDone:
		body := fmt.Sprintf("Version %d is in Awaiting your review", run.RevisionNumber)
		shorts, missing := 0, 0
		for _, o := range run.Outputs {
			switch o.State {
			case models.OutputReady:
				shorts++
			case models.OutputFailed:
				missing++
			}
		}
		if shorts > 0 {
			body = fmt.Sprintf("Version %d and %s are in Awaiting your review", run.RevisionNumber, plural(shorts, "short"))
		}
		if run.QA != "" {
			body += "; the quality check " + run.QA
		}
		if missing > 0 {
			body += fmt.Sprintf("; %s couldn't be made", plural(missing, "short"))
		}
		if spent := spentWords(run.Spent); spent != "nothing" {
			body += "; it spent " + spent
		}
		_, _ = notify.Upsert(ctx, e.a, models.Notification{UserID: run.CreatedBy.UserID, Kind: models.NotifyRecipeRun, GroupKey: "recipe-run:" + run.ID,
			Tone: "success", Title: upperFirst(runName(run)) + " is ready for your review", Body: body + ".", Link: link, ProjectID: run.ProjectID, RenderID: run.Render}, notify.Refresh)
	case models.RunFailed:
		_, _ = notify.Upsert(ctx, e.a, models.Notification{UserID: run.CreatedBy.UserID, Kind: models.NotifyRecipeRun, GroupKey: "recipe-run:" + run.ID,
			Tone: "error", Title: upperFirst(runName(run)) + " didn't finish", Body: run.Problem, Link: link, ProjectID: run.ProjectID}, notify.Refresh)
	}
}

func nonEmpty(xs ...string) []string {
	var out []string
	for _, x := range xs {
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}
