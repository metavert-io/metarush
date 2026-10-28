package aiwork_test

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// On a computer that handed its role over (D349), AI work is refused in plain
// words naming where to start it: its keys and models are checked here, and
// nothing here would ever run it. Estimates still work.
func TestAIWorkIsStartedWhereTheRoleIs(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	testapp.HandedOver(t, e.a, "studio-mini")
	in := aiwork.MakeIn{Kind: aiservices.KindPicture, ProjectID: e.project, Model: "fake/fake-picture", Prompt: "a watercolour kite over a hill", Options: models.GenOptions{Size: "2K", AspectRatio: "16:9"}}
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
	if err != nil || !est.Allowed {
		t.Fatalf("the estimate still works: %+v %v", est, err)
	}
	in.MaxUSD = usd(est.USD)
	_, err = call[aiwork.MakeOut](t, e.a, "ai.make", in, false)
	ae := apperr.From(err)
	if d, _ := ae.Details.(map[string]any); ae.Code != apperr.Conflict || d["reason"] != "handed_over" || d["host"] != "studio-mini" {
		t.Fatalf("refused, naming where: %v %+v", err, ae.Details)
	}
	if e.fake.Calls("submit") != 0 {
		t.Fatal("something reached the provider")
	}
	for _, c := range []string{store.CGenerations, store.CJobs} {
		if n, _ := e.a.DB.C(c).CountDocuments(ctx, bson.M{"workspaceId": e.a.WorkspaceID()}); n != 0 {
			t.Fatalf("%s: %d left behind", c, n)
		}
	}
}
