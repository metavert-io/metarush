package motion_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func checked(t *testing.T, a *app.App, name, hash string) *models.MotionComponent {
	t.Helper()
	c, err := motion.SaveCheck(context.Background(), a.DB, &models.MotionComponent{WorkspaceID: a.WorkspaceID(), Hash: hash, Name: name, Region: "center", MinDuration: 1, MaxDuration: 5,
		SchemaJSON: `{"type":"object","properties":{"title":{"type":"string"}},"additionalProperties":false}`, Check: models.MotionCheckInfo{By: a.Actor, At: time.Now().UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func spec(name, hash string) *editspec.Spec {
	return &editspec.Spec{SchemaVersion: 1, Overlays: []editspec.Overlay{{ID: "g", Motion: &editspec.MotionRef{Name: name, Hash: hash}, At: 0, Duration: 2, Params: json.RawMessage(`{}`)}}}
}

func TestDecisionsAndRefusals(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	h := strings.Repeat("3f", 32)
	c := checked(t, a, "route-map", h)
	if c.State != models.MotionWaiting || c.CreatedAt.IsZero() {
		t.Fatalf("a new version waits for a person: %+v", c)
	}
	// Nothing renders before a person approves it.
	_, err := motion.RequireApproved(ctx, a.DB, a.WorkspaceID(), spec("route-map", h))
	if ae := apperr.From(err); ae.Code != apperr.Validation || !strings.Contains(ae.Message, "no person has approved it yet") {
		t.Fatalf("waiting: %v", err)
	}
	ok, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), h, motion.Approve, a.Actor, "looks right")
	if err != nil || ok.State != models.MotionApproved || ok.ApprovedBy == nil || len(ok.Decisions) != 1 || ok.Decisions[0].Note != "looks right" {
		t.Fatalf("approve: %v %+v", err, ok)
	}
	if got, err := motion.RequireApproved(ctx, a.DB, a.WorkspaceID(), spec("route-map", h)); err != nil || got[h] == nil {
		t.Fatalf("approved: %v", err)
	}
	if _, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), h, motion.Approve, a.Actor, ""); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("approve twice: %v", err)
	}
	// Checking the same bytes again keeps the decision (same code, same approval).
	if again := checked(t, a, "route-map", h); again.State != models.MotionApproved || len(again.Decisions) != 1 {
		t.Fatalf("re-check keeps approval: %+v", again)
	}
	// Revoke works: renders are refused again, and it says why.
	rv, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), h, motion.Revoke, a.Actor, "wrong colors")
	if err != nil || rv.State != models.MotionRevoked {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := motion.RequireApproved(ctx, a.DB, a.WorkspaceID(), spec("route-map", h)); !strings.Contains(apperr.From(err).Message, "approval was withdrawn") {
		t.Fatalf("revoked: %v", err)
	}
	if _, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), h, motion.Revoke, a.Actor, ""); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("revoke twice: %v", err)
	}
	// Turned down, then approved again by a person.
	if rj, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), h, motion.Reject, a.Actor, ""); err != nil || rj.State != models.MotionRejected {
		t.Fatalf("reject: %v", err)
	}
	if ap, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), h, motion.Approve, a.Actor, ""); err != nil || ap.State != models.MotionApproved || len(ap.Decisions) != 4 {
		t.Fatalf("approve after reject: %v", err)
	}
	// A version nobody checked.
	if _, err := motion.RequireApproved(ctx, a.DB, a.WorkspaceID(), spec("other", strings.Repeat("ab", 32))); !strings.Contains(apperr.From(err).Message, "hasn't been checked") {
		t.Fatalf("unknown: %v", err)
	}
}

func TestResolveVersions(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	v1, v2 := "3fa9c2d1"+strings.Repeat("0", 56), "3fa9c2d1"+strings.Repeat("1", 56)
	checked(t, a, "route-map", v1)
	checked(t, a, "route-map", v2)
	if c, err := motion.Resolve(ctx, a.DB, a.WorkspaceID(), "route-map"); err != nil || c.Hash != v2 {
		t.Fatalf("a name is its latest check: %v %v", c, err)
	}
	if _, err := motion.Resolve(ctx, a.DB, a.WorkspaceID(), "route-map@3fa9c2d1"); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("an ambiguous version: %v", err)
	}
	if c, err := motion.Resolve(ctx, a.DB, a.WorkspaceID(), "route-map@"+v1[:20]); err != nil || c.Hash != v1 {
		t.Fatalf("a longer prefix: %v", err)
	}
	if _, err := motion.Resolve(ctx, a.DB, a.WorkspaceID(), "nope@deadbeef"); apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("unknown: %v", err)
	}
	if vs := motion.Versions(ctx, a.DB, a.WorkspaceID(), "3fa9c2d1"); len(vs) != 2 {
		t.Fatalf("versions by prefix: %v", vs)
	}
	ls, err := motion.List(ctx, a.DB, a.WorkspaceID(), false)
	if err != nil || len(ls) != 1 {
		t.Fatalf("latest per name: %v %d", err, len(ls))
	}
	all, _ := motion.List(ctx, a.DB, a.WorkspaceID(), true)
	if len(all) != 2 {
		t.Fatalf("every version: %d", len(all))
	}
}
