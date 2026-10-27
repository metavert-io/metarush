package naming_test

import (
	"context"
	"testing"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/naming"
)

// Only a person confirming the action names as a person; apps, Auto, Claude
// and MetaRush's own work never do (D274).
func TestWhoNames(t *testing.T) {
	for _, c := range []struct {
		actor models.Actor
		human bool
		want  string
	}{
		{models.Actor{Via: "dashboard"}, true, models.NamedByPerson},
		{models.Actor{Via: "cli"}, true, models.NamedByPerson},
		{models.Actor{Via: "cli"}, false, models.NamedByAgent},
		{models.Actor{Via: "cli", Agent: "codex"}, true, models.NamedByAgent},
		{models.Actor{Via: "mcp", Agent: "cursor"}, false, models.NamedByAgent},
		{models.Actor{Via: models.ViaAssistantAuto}, true, models.NamedByAuto},
		{models.Actor{Via: models.ViaAssistant}, false, models.NamedByClaude},
		{models.Actor{Via: "worker"}, false, models.NamedByMachine},
	} {
		if got := naming.Derive(c.actor, c.human); got != c.want {
			t.Errorf("%+v human %v: %s, want %s", c.actor, c.human, got, c.want)
		}
	}
	if n := naming.Record(context.Background(), models.Actor{}); n.By != models.NamedByMachine || n.ByPerson() {
		t.Errorf("nothing said: %+v", n)
	}
	ctx := naming.With(context.Background(), models.NamedByPerson)
	if n := naming.Record(ctx, models.Actor{UserName: "Jo"}); !n.ByPerson() || n.Words() == "" {
		t.Errorf("a person: %+v", n)
	}
	var none *models.Naming
	if none.ByPerson() {
		t.Error("no record counted as a person's")
	}
}
