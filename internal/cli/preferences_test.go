package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/prefs"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// metarush account preferences (D208): shows your preferences (--json too);
// changing them takes a person at a terminal; on or off, nothing else.
func TestAccountPreferencesCommand(t *testing.T) {
	a := testapp.New(t)
	home := a.Cfg.Home()
	ctx := context.Background()
	ws, uid := a.WorkspaceID(), a.Cfg.User.ID
	now := time.Now().UTC()
	if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: ws, WorkspaceID: ws, Name: "Prefs", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Insert(ctx, store.CUsers, models.User{ID: uid, WorkspaceID: ws, Name: "Tester", Email: "tester@example.com", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	read := func() prefs.PreferencesOut {
		t.Helper()
		r := runCLI(t, home, "account", "preferences", "--json")
		var doc struct {
			OK   bool                 `json:"ok"`
			Data prefs.PreferencesOut `json:"data"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil || !doc.OK {
			t.Fatalf("account preferences --json: %v\n%s%s", err, r.stdout, r.stderr)
		}
		return doc.Data
	}
	if p := read(); !p.Estimates || !p.EstimatesYours || p.EstimatesRequired || !p.Owner || p.AssistantMode != "ask" {
		t.Fatalf("on by default: %+v", p)
	}
	if r := runCLI(t, home, "account", "preferences"); r.code != 0 || !strings.Contains(r.stdout, "Estimate costs of outside AI services before edits: on") ||
		!strings.Contains(r.stdout, "Claude's changes for you: Ask first.") || strings.Contains(r.stdout, "metarush ") {
		t.Fatalf("plain: %+v", r)
	}
	if r := runCLI(t, home, "account", "preferences", "--details"); !strings.Contains(r.stdout, "metarush account preferences --estimates on|off") {
		t.Fatalf("--details names the commands: %s", r.stdout)
	}
	if r := runCLI(t, home, "account", "preferences", "--estimates", "maybe"); r.code == 0 || !strings.Contains(r.stderr+r.stdout, "--estimates takes on or off.") {
		t.Fatalf("on or off: %+v", r)
	}
	// Without a person at a terminal: refused, nothing changed.
	if r := runCLI(t, home, "account", "preferences", "--estimates", "off"); r.code == 0 || !strings.Contains(r.stderr+r.stdout, "Changing your preferences is for people.") {
		t.Fatalf("people only: %+v", r)
	}
	if !prefs.EstimatesFirst(ctx, a, uid) {
		t.Fatal("a refused change changed nothing")
	}
	// A person at a terminal (this computer's command line counts as the owner).
	old := isInteractive
	isInteractive = func() bool { return true }
	defer func() { isInteractive = old }()
	if r := runCLI(t, home, "account", "preferences", "--estimates", "off", "--json"); r.code != 0 {
		t.Fatalf("off: %+v", r)
	}
	if p := read(); p.Estimates || p.EstimatesYours {
		t.Fatalf("estimates off: %+v", p)
	}
	if r := runCLI(t, home, "account", "preferences", "--require-estimates", "on", "--json"); r.code != 0 {
		t.Fatalf("for everyone: %+v", r)
	}
	if p := read(); !p.Estimates || p.EstimatesYours || !p.EstimatesRequired {
		t.Fatalf("required for everyone: %+v", p)
	}
	if r := runCLI(t, home, "account", "preferences"); !strings.Contains(r.stdout, "An owner asks for estimates for everyone") {
		t.Fatalf("plain, required: %s", r.stdout)
	}
}
