package generate

import (
	"context"
	"fmt"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/videogen"
)

// AIServicesLimits is the name AI services' spending limits are registered
// under (costs.Part.Limits): every kind of AI work, whichever service does it,
// is paid from generated video's project budget and daily limit (D226).
const AIServicesLimits = "AI services"

// Generated video's limits for Auto (D241): a project's budget and the
// team's daily limit, each counted only when a person set it (the defaults
// don't count, nor does a project with no limit). AI services share them.
func init() {
	title := "Google Gemini"
	if info, err := videogen.Info(Provider); err == nil && info.Title != "" {
		title = info.Title
	}
	ops.RegisterSpendingLimits(title, func(ctx context.Context, a *app.App, projectID string) ops.SpendingLimits {
		return spendingLimits(ctx, a, title, "Settings → Integrations → Generated video", projectID)
	})
	ops.RegisterSpendingLimits(AIServicesLimits, func(ctx context.Context, a *app.App, projectID string) ops.SpendingLimits {
		return spendingLimits(ctx, a, AIServicesLimits, "Settings → Integrations → Generated video (AI services share its budget and daily limit)", projectID)
	})
}

// spendingLimits are generated video's budget and daily limit for a project,
// under a service's name.
func spendingLimits(ctx context.Context, a *app.App, service, where, projectID string) ops.SpendingLimits {
	l := ops.SpendingLimits{Service: service, Where: where, Daily: ops.SpendingLimit{Exists: true}, Project: ops.SpendingLimit{Exists: true}}
	st, err := loadSettings(ctx, a, projectID)
	if err != nil {
		return l
	}
	l.Daily.Default = st.dailyCapDefault
	l.Daily.Set = !st.dailyCapDefault
	l.Daily.Words = money(st.dailyCap) + " a day for the team"
	l.Daily.By, l.Daily.At, l.Daily.Person = ops.SetByOf(st.dailyCapBy)
	if projectID == "" {
		return l
	}
	l.Project.Default = st.projectBudgetDefault
	switch {
	case st.projectBudgetDefault:
		l.Project.Words = money(*st.projectBudget) + " for this project (the default)"
	case st.projectBudget == nil:
		l.Project.Words = "no limit for this project"
	default:
		l.Project.Set = true
		l.Project.Words = fmt.Sprintf("%s for this project", money(*st.projectBudget))
	}
	l.Project.By, l.Project.At, l.Project.Person = ops.SetByOf(st.projectBudgetBy)
	return l
}
