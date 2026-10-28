package generate_test

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// A generated shot asked for on a computer that handed its role over is
// refused, naming the computer to make it on (D349); nothing is held or sent.
func TestShotIsMadeWhereTheRoleIs(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	testapp.HandedOver(t, e.a, "studio-mini")
	in := generate.GenShotIn{ProjectID: e.project, Prompt: "a red paper boat drifting on a pond", Seconds: 3, Resolution: "360p"}
	est, err := call[generate.GenEstimate](t, e.a, "generate.estimate", in, false)
	if err != nil || !est.Allowed {
		t.Fatalf("the estimate still works: %+v %v", est, err)
	}
	in.MaxUSD = usd(est.USD)
	_, err = call[generate.GenRunOut](t, e.a, "generate.shot", in, false)
	ae := apperr.From(err)
	if d, _ := ae.Details.(map[string]any); ae.Code != apperr.Conflict || d["reason"] != "handed_over" || d["host"] != "studio-mini" {
		t.Fatalf("refused, naming where: %v", err)
	}
	if e.fake.Calls("create") != 0 {
		t.Fatal("something reached the provider")
	}
	if n, _ := e.a.DB.C(store.CGenerations).CountDocuments(ctx, bson.M{"workspaceId": e.a.WorkspaceID()}); n != 0 {
		t.Fatal("a generation was recorded")
	}
	if l := ledger(t, e.a, "project", e.project); l.HeldMicros != 0 {
		t.Fatalf("money held: %+v", l)
	}
}
