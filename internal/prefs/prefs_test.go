package prefs_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/prefs"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func person(t *testing.T, a *app.App, id, name string) {
	t.Helper()
	if err := a.DB.Insert(context.Background(), store.CUsers, models.User{ID: id, WorkspaceID: a.WorkspaceID(), Name: name, Email: strings.ToLower(name) + "@example.com",
		CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}

func set(ctx context.Context, a *app.App, in prefs.PreferencesSetIn) (*prefs.PreferencesOut, error) {
	return ops.CallTyped[prefs.PreferencesOut](ctx, a, "account.preferences.set", in, ops.Caller{Human: true, Remote: true})
}

func code(err error) string {
	if err == nil {
		return ""
	}
	return apperr.From(err).Code
}

// Each person's estimates are their own; owners can ask everyone for them;
// a member can't; reading never fails closed the wrong way (an unknown
// person, or one MetaRush can't read, sees estimates first).
func TestEstimatesArePersonalAndOwnersCanRequireThem(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	ws := a.WorkspaceID()
	if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: ws, WorkspaceID: ws, Name: "T", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	owner, member := a.Cfg.User.ID, ids.New(ids.User)
	person(t, a, owner, "Olive")
	person(t, a, member, "Max")
	if !prefs.EstimatesFirst(ctx, a, owner) || !prefs.EstimatesFirst(ctx, a, member) || !prefs.EstimatesFirst(ctx, a, "usr_nobody") {
		t.Fatal("on by default, for everyone")
	}

	// The member, signed in over HTTP, turns their own estimates off.
	ma := *a
	ma.Actor = models.Actor{UserID: member, UserName: "Max", Via: "dashboard"}
	mctx := ops.OverHTTP(ctx, func() bool { return false })
	off, on := false, true
	p, err := set(mctx, &ma, prefs.PreferencesSetIn{Estimates: &off})
	if err != nil || p.Estimates || p.EstimatesYours || p.Owner {
		t.Fatalf("a member's own choice: %+v %v", p, err)
	}
	if prefs.EstimatesFirst(ctx, a, member) || !prefs.EstimatesFirst(ctx, a, owner) {
		t.Fatal("one person's choice is theirs alone")
	}
	// …but can't decide for everyone.
	if _, err := set(mctx, &ma, prefs.PreferencesSetIn{RequireEstimates: &on}); code(err) != apperr.Unauthorized ||
		apperr.From(err).Message != "Only an owner of this MetaRush can change that." {
		t.Fatalf("a member asking everyone for estimates: %v", err)
	}
	// An owner can: the member sees the estimate first again, and why.
	oa := *a
	oa.Actor = models.Actor{UserID: owner, UserName: "Olive", Via: "dashboard"}
	octx := ops.OverHTTP(ctx, func() bool { return true })
	if p, err := set(octx, &oa, prefs.PreferencesSetIn{RequireEstimates: &on}); err != nil || !p.EstimatesRequired || p.RequiredBy != "Olive" || !p.Owner {
		t.Fatalf("an owner asks everyone: %+v %v", p, err)
	}
	mine, err := ops.CallTyped[prefs.PreferencesOut](mctx, &ma, "account.preferences", prefs.PreferencesIn{}, ops.Caller{Remote: true})
	if err != nil || !mine.Estimates || mine.EstimatesYours || !mine.EstimatesRequired || mine.RequiredBy != "Olive" || mine.RequiredAt == nil {
		t.Fatalf("the member's view while required: %+v %v", mine, err)
	}
	if !prefs.EstimatesFirst(ctx, a, member) {
		t.Fatal("required for everyone wins over a person's off")
	}
	// The owners stop asking: the member's own choice counts again.
	if _, err := set(octx, &oa, prefs.PreferencesSetIn{RequireEstimates: &off}); err != nil || prefs.EstimatesFirst(ctx, a, member) {
		t.Fatalf("back to each person's choice: %v", err)
	}
	if _, err := set(mctx, &ma, prefs.PreferencesSetIn{Estimates: &on}); err != nil || !prefs.EstimatesFirst(ctx, a, member) {
		t.Fatalf("the member turns them back on: %v", err)
	}
	// Someone MetaRush doesn't know can't keep a choice.
	ga := *a
	ga.Actor = models.Actor{UserID: "usr_" + strings.Repeat("0", 26), Via: "dashboard"}
	if _, err := set(mctx, &ga, prefs.PreferencesSetIn{Estimates: &off}); code(err) != apperr.NotFound {
		t.Fatalf("an unknown person: %v", err)
	}
	// Claude can't change anyone's preferences (people only, and not on its channel).
	ca := *a
	ca.Actor = models.Actor{UserID: owner, Via: models.ViaAssistant}
	if _, err := ops.CallTyped[prefs.PreferencesOut](ctx, &ca, "account.preferences.set", prefs.PreferencesSetIn{Estimates: &off}, ops.Caller{}); code(err) != apperr.Unauthorized {
		t.Fatalf("Claude's channel: %v", err)
	}
	if _, err := ops.CallTyped[prefs.PreferencesOut](ctx, a, "account.preferences.set", prefs.PreferencesSetIn{Estimates: &off}, ops.Caller{}); code(err) != apperr.HumanOnly {
		t.Fatalf("not a person: %v", err)
	}
	// Audited as the person.
	n, _ := a.DB.C(store.CAudit).CountDocuments(ctx, map[string]any{"workspaceId": ws, "action": "account.preferences", "actor.userId": member})
	w, _ := a.DB.C(store.CAudit).CountDocuments(ctx, map[string]any{"workspaceId": ws, "action": "workspace.estimates", "actor.userId": owner})
	if n != 2 || w != 2 {
		t.Fatalf("audit: %d personal, %d for everyone", n, w)
	}
}

// What Preferences says, in plain words, for each case.
func TestPreferenceWordsArePlain(t *testing.T) {
	for _, e := range []prefs.Estimates{{On: true, Theirs: true}, {On: false}, {On: true, Theirs: true, Required: true}, {On: true, Required: true}} {
		w := prefs.Words(e)
		if fs := plainwords.Check(w); len(fs) > 0 || w == "" {
			t.Errorf("not plain: %q %v", w, fs)
		}
	}
}
