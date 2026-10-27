package ops_test

import (
	"context"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/version"
)

// Settings → General (D130): this MetaRush, what's new in it, and every computer
// in the workspace with the version it runs, compared with this one.
func TestVersionShowComparesEveryComputer(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	ws := a.WorkspaceID()
	now := time.Now().UTC()
	inst := func(id, name, v, state string, seen time.Duration) models.Installation {
		return models.Installation{ID: id, WorkspaceID: ws, Name: name, Hostname: name + ".local", OS: "darwin", Arch: "arm64", Version: v, Commit: "abc1234",
			Built: "2026-10-17T09:00:00Z", State: state, UserID: a.Cfg.User.ID, CreatedAt: now.Add(-48 * time.Hour), LastSeenAt: now.Add(-seen)}
	}
	insts := []models.Installation{
		inst(a.Cfg.Installation.ID, "Studio Mac", "0.0.1", "", 2*time.Minute), // a stale record: what answers is this build
		inst(ids.New(ids.Installation), "Mac mini", "0.1.0-dev", models.InstallationActive, time.Minute),
		inst(ids.New(ids.Installation), "Edit laptop", "99.0.0", models.InstallationActive, time.Hour),
		inst(ids.New(ids.Installation), "Same build", version.Version, models.InstallationActive, 3*time.Hour),
		inst(ids.New(ids.Installation), "New mini", "", models.InstallationInvited, 0),
		inst(ids.New(ids.Installation), "Old laptop", "0.0.9", models.InstallationRevoked, 5*time.Hour),
	}
	for _, in := range insts {
		if err := a.DB.Insert(ctx, store.CInstallations, in); err != nil {
			t.Fatal(err)
		}
	}
	out, err := ops.CallTyped[ops.VersionShowOut](ctx, a, "version.show", ops.VersionShowIn{}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Version != version.Version || out.Release == "" || out.Commit != version.Commit || out.EditSpec == 0 || len(out.WhatsNew) == 0 {
		t.Errorf("this build: %+v", out.Info)
	}
	got := map[string]ops.ComputerVersion{}
	var order []string
	for _, c := range out.Computers {
		got[c.Name] = c
		order = append(order, c.Name)
	}
	if len(order) != 5 || order[0] != "Studio Mac" {
		t.Fatalf("computers %v: this one first, people removed from the workspace left out", order)
	}
	for name, want := range map[string]string{"Studio Mac": "same", "Mac mini": "older", "Edit laptop": "newer", "Same build": "same", "New mini": "unknown"} {
		if got[name].Standing != want {
			t.Errorf("%s: standing %q, want %q (%+v)", name, got[name].Standing, want, got[name])
		}
	}
	if c := got["Studio Mac"]; !c.This || c.Version != version.Version || c.Commit != version.Commit {
		t.Errorf("this computer reports the record, not the build that answers: %+v", c)
	}
	if c := got["Mac mini"]; c.Release != "0.1.0" || c.Version != "0.1.0-dev" || c.LastSeenAt == nil || c.State != models.InstallationActive {
		t.Errorf("Mac mini: %+v", c)
	}
	if out.Older != 1 {
		t.Errorf("older = %d, want 1", out.Older)
	}

	// The daemon keeps its computer's record current (every minute).
	a.TouchInstallation(ctx)
	var me models.Installation
	if err := a.DB.GetByID(ctx, store.CInstallations, ws, a.Cfg.Installation.ID, &me); err != nil {
		t.Fatal(err)
	}
	if me.Version != version.Version || me.Commit != version.Commit || me.Built != version.Date || time.Since(me.LastSeenAt) > time.Minute {
		t.Errorf("after TouchInstallation: %+v", me)
	}
}
