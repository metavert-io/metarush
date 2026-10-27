package abilities

import (
	"context"
	"fmt"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/todos"
)

// A version waiting for an owner (D231): a to-do asks for a decision (one
// open question per ability: its newest waiting version) and the bell tells
// the owners once per version. Deciding closes the question wherever it was
// decided. Best effort: a to-do or a notification that can't be written never
// fails the check.

func approvalKey(name string) string { return "ability-approval:" + name + "@" }

// remindApproval opens (or closes) the version's question and rings the bell.
func remindApproval(ctx context.Context, a *app.App, v *models.AbilityVersion) {
	if v == nil {
		return
	}
	key := approvalKey(v.Name)
	if v.State != models.AbilityWaiting {
		resolution := models.ResolvedElsewhere
		if v.State == models.AbilityFailed {
			resolution = models.ResolvedObsolete
		}
		_, _ = todos.Close(ctx, a, key+v.Hash, resolution, v.State)
		return
	}
	// Only the newest waiting version asks; older questions about it close.
	if vs, err := versionsOf(ctx, a, v.Name); err == nil {
		for _, o := range vs {
			if o.State == models.AbilityWaiting && o.Number > v.Number {
				_, _ = todos.Close(ctx, a, key+v.Hash, models.ResolvedObsolete, "a newer version waits")
				return
			}
		}
	}
	_, _ = todos.CloseStale(ctx, a, key, []string{key + v.Hash})
	link := links.Ability(v.Name, v.Hash)
	q := fmt.Sprintf("Can the ability “%s” be used?", v.Title)
	if v.Number > 1 {
		q = fmt.Sprintf("Can version %d of the ability “%s” be used?", v.Number, v.Title)
	}
	detail := v.Does + " An owner looks at what it installs, the places it reaches, its keys, what leaves this computer, its cost and its licence under Library → Abilities first."
	_, _ = todos.Upsert(ctx, a, models.Todo{Kind: models.TodoAbilityApproval, Scope: "workspace", GroupKey: key + v.Hash, HumanOnly: true, Priority: 2,
		Question: q, Detail: detail,
		Context: models.TodoContext{Link: link, Ability: &models.TodoAbility{Name: v.Name, Title: v.Title, Version: v.Hash, Number: v.Number}},
		Suggestions: []models.TodoSuggestion{
			{Label: "Approve this version", Value: "approve"},
			{Label: "Not this one", Value: "reject"},
		}})
	title := fmt.Sprintf("The ability “%s” waits for your approval", v.Title)
	if v.Number > 1 {
		title = fmt.Sprintf("Version %d of the ability “%s” waits for your approval", v.Number, v.Title)
	}
	for _, uid := range owners(ctx, a, v.By.UserID) {
		_, _ = notify.Upsert(ctx, a, models.Notification{UserID: uid, Kind: models.NotifyAbilityWaiting, GroupKey: "ability-waiting:" + v.Hash, Tone: "info",
			Title: title, Body: "Look at what it may do under Library → Abilities, then approve it or turn it down.", Link: link,
			Details: map[string]any{"ability": v.Name, "version": v.Hash, "versionNumber": v.Number}}, notify.Once)
	}
}

// decided closes the question about a version once an owner decided.
func decided(ctx context.Context, a *app.App, v *models.AbilityVersion) {
	if v != nil {
		_, _ = todos.Close(ctx, a, approvalKey(v.Name)+v.Hash, models.ResolvedElsewhere, v.State)
	}
}
