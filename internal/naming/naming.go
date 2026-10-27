// Package naming records who names a face or a voice as a person (D274): a
// person, or software acting for one. The operation registry says who is
// acting (ops.Call); the code that writes a name stamps it with Record.
package naming

import (
	"context"
	"time"

	"github.com/metavert-io/metarush/internal/models"
)

type key struct{}

// With says who acts in ctx (a models.NamedBy* value).
func With(ctx context.Context, by string) context.Context { return context.WithValue(ctx, key{}, by) }

// From is who acts in ctx ("" when no surface said).
func From(ctx context.Context) string {
	by, _ := ctx.Value(key{}).(string)
	return by
}

// Derive says who acts from the actor and whether a person confirmed the
// action (a dashboard click, a terminal): only that is a person.
func Derive(actor models.Actor, human bool) string {
	switch {
	case actor.Via == models.ViaAssistantAuto:
		return models.NamedByAuto
	case actor.Via == models.ViaAssistant:
		return models.NamedByClaude
	case actor.Agent != "" || actor.Via == "mcp":
		return models.NamedByAgent
	case actor.Via == "worker" || actor.Via == "watch" || actor.Via == "daemon":
		return models.NamedByMachine
	case human:
		return models.NamedByPerson
	}
	return models.NamedByAgent
}

// Record is a naming made now by whoever acts in ctx (MetaRush by itself when
// nothing said).
func Record(ctx context.Context, actor models.Actor) *models.Naming {
	by := From(ctx)
	if by == "" {
		by = models.NamedByMachine
	}
	return &models.Naming{By: by, Actor: actor, At: time.Now().UTC()}
}
