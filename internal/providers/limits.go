package providers

import (
	"context"
	"fmt"
	"math"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// ElevenLabs' limits for Auto (D241): a project's budget in credits, counted
// when a person set it; the team has no daily limit with ElevenLabs, so Auto
// asks before spending there.
func init() {
	ops.RegisterSpendingLimits(ElevenLabsTitle, func(ctx context.Context, a *app.App, projectID string) ops.SpendingLimits {
		l := ops.SpendingLimits{Service: ElevenLabsTitle, Where: "Settings → Integrations → ElevenLabs", Project: ops.SpendingLimit{Exists: true}}
		if projectID == "" {
			return l
		}
		var p models.Project
		if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), projectID, &p); err != nil {
			return l
		}
		s, ok := p.Providers[ElevenLabsName]
		if !ok || s.BudgetCredits == nil || *s.BudgetCredits <= 0 {
			l.Project.Words = "no limit for this project"
			return l
		}
		l.Project.Set = true
		l.Project.Words = fmt.Sprintf("%s credits for this project", Commas(int64(math.Round(*s.BudgetCredits))))
		l.Project.By, l.Project.At, l.Project.Person = ops.SetByOf(s.BudgetCreditsBy)
		return l
	})
}
