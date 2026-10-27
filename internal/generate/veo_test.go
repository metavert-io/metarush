package generate_test

import (
	"context"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakevertex"
)

// Veo through Google Cloud (D282) is chosen by its model: priced by the
// second, held to the same budgets, rules and records as Gemini's shots, its
// key the service account's (set up by an owner through the integrations),
// and the video kept like any generated shot.
func TestShotWithVeo(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	f, key := fakevertex.Start(t)
	f.ReadyAfter = 0
	in := generate.GenShotIn{ProjectID: e.project, Prompt: "a red paper boat drifting on a pond", Model: "vertex/veo-3.1-fast-generate-001", Seconds: 8, Resolution: "720p"}
	est, err := call[generate.GenEstimate](t, e.a, "generate.estimate", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if est.Provider != "vertex" || est.KeyReady || est.Allowed || !strings.Contains(strings.Join(est.Problems, " "), "Google Cloud Vertex AI isn't set up on this computer") {
		t.Fatalf("before the key: %+v", est)
	}
	// An owner gives MetaRush the service account's key (never an argument).
	if _, err := ops.CallTyped[providers.Status](ctx, e.a, "provider.set", providers.SetIn{Name: providers.VertexName, Key: key}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	st, err := ops.CallTyped[providers.StatusOut](ctx, e.a, "provider.status", providers.NameIn{Name: providers.VertexName}, ops.Caller{})
	if err != nil || !st.Status.Configured || !strings.Contains(st.Status.Masked, "metarush-veo@") {
		t.Fatalf("status %+v %v", st, err)
	}
	est, err = call[generate.GenEstimate](t, e.a, "generate.estimate", in, false)
	if err != nil || !est.Allowed || est.USD != 0.80 || est.PerSecondUSD != 0.10 || len(est.Cost.Parts) != 1 || !strings.Contains(est.Cost.Parts[0].Rate, "a second at 720p") {
		t.Fatalf("estimate %+v %v", est, err)
	}
	in.MaxUSD = usd(est.USD)
	out, err := call[generate.GenRunOut](t, e.a, "generate.shot", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, ctx, out.JobIDs[0]); err != nil {
		t.Fatal(err)
	}
	v, err := call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: out.Generations[0].ID}, false)
	if err != nil || v.State != generate.StateDone || v.Provider != "vertex" || len(v.Clips) != 1 || v.Outcome.Actual == nil || v.Outcome.Actual.USD != 0.80 {
		t.Fatalf("view %+v %v", v, err)
	}
	var as models.Asset
	if err := e.a.DB.GetByID(ctx, store.CAssets, e.a.WorkspaceID(), v.Clips[0].AssetID, &as); err != nil {
		t.Fatal(err)
	}
	if as.Source == nil || as.Source.Generated == nil || as.Source.Generated.Provider != "vertex" || as.Source.Generated.Watermark != "SynthID" || !as.HasTag(models.TagAIGenerated) {
		t.Fatalf("clip %+v", as.Source)
	}
	if ps := f.Problems(); len(ps) > 0 {
		t.Errorf("the fake saw: %v", ps)
	}
	// Veo makes 4, 6 or 8 seconds; Content Settings can rule Google Cloud out by name.
	if _, err := call[generate.GenEstimate](t, e.a, "generate.estimate", generate.GenShotIn{ProjectID: e.project, Prompt: "x", Model: "vertex/veo-3.1-fast-generate-001", Seconds: 5, Resolution: "720p"}, false); codeOf(err) != apperr.InvalidInput {
		t.Fatalf("5 seconds: %v", err)
	}
}
