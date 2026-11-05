package worker

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/mattes"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakelocal"
)

// A computer with a model that finds people takes cut-out work (D505): it
// offers the matte job, says which models it has on its record, and makes
// the clip's cut-out; its files reach a render's loopback server by hash,
// and one that isn't there any more stops the render plainly.
func TestAComputerWithAModelMakesCutOuts(t *testing.T) {
	fakelocal.Matter(t)
	s := newShop(t)
	ctx := context.Background()
	w := s.worker()
	if !w.Can(jobs.TypeMatte) || len(w.mattes()) == 0 {
		t.Fatalf("a computer with a model: %v %v", w.JobTypes(), w.mattes())
	}
	if slices.ContainsFunc(w.Lacks(), func(l models.WorkerLack) bool { return l.JobType == jobs.TypeMatte }) {
		t.Fatalf("lacks %v", w.Lacks())
	}
	res := ingest.File(ctx, s.a, s.footage("host", 2, false, false), ingest.Options{ProjectID: s.project, Via: "cli", Transcribe: false})
	if res.AssetID == "" {
		t.Fatalf("ingest: %+v", res)
	}
	out, err := ops.CallTyped[mattes.CutoutOut](ctx, s.a, "footage.cutout", mattes.CutoutIn{AssetID: res.AssetID}, ops.Caller{Human: true})
	if err != nil || out.JobID == "" {
		t.Fatalf("asked: %+v %v", out, err)
	}
	j := runJobs(t, w, out.JobID)[out.JobID]
	if j.State != models.JobSucceeded {
		t.Fatalf("the matte job: %s %s", j.State, j.Error)
	}
	m, err := mattes.Load(ctx, s.a, out.Matte.ID)
	if err != nil || m.State != models.MatteReady || m.File == nil {
		t.Fatalf("the cut-out: %+v %v", m, err)
	}
	// A render gets the file by hash; nested plans' too.
	plan := &editspec.RenderPlan{Clips: []editspec.PlanClip{{ID: "host", Background: &editspec.PlanBackground{With: "colour", CutOut: m.File.SHA256}}},
		Nested: map[string]*editspec.NestedPlan{"cut:x/y": {Plan: &editspec.RenderPlan{Clips: []editspec.PlanClip{{ID: "inner", Background: &editspec.PlanBackground{With: "blur", CutOut: m.File.SHA256}}}}}}}
	local, types := map[string]string{}, map[string]string{}
	if err := cutOutFiles(ctx, s.a, plan, nil, local, types); err != nil {
		t.Fatal(err)
	}
	p := local["c-"+m.File.SHA256]
	if st, err := os.Stat(p); err != nil || st.Size() != m.File.Size || types["c-"+m.File.SHA256] != "video/webm" || len(local) != 1 {
		t.Fatalf("the render's files: %v %v (%v)", local, types, err)
	}
	gone := &editspec.RenderPlan{Clips: []editspec.PlanClip{{ID: "host", Background: &editspec.PlanBackground{With: "colour", CutOut: strings.Repeat("0", 64)}}}}
	if err := cutOutFiles(ctx, s.a, gone, nil, map[string]string{}, map[string]string{}); err == nil || !strings.Contains(err.Error(), "cut-out") {
		t.Fatalf("a cut-out that isn't there: %v", err)
	}
	if err := cutOutFiles(ctx, s.a, &editspec.RenderPlan{}, nil, local, types); err != nil {
		t.Fatalf("a plan without cut-outs: %v", err)
	}
	// A cut-out the plan keeps pinned but MetaRush doesn't have (inside a
	// nested cut too), or one that isn't made, stops the render plainly:
	// never the clip as recorded in its place.
	missing := &editspec.RenderPlan{Clips: []editspec.PlanClip{{ID: "host", Background: &editspec.PlanBackground{With: "colour", CutOut: m.File.SHA256}}},
		Nested: map[string]*editspec.NestedPlan{"cut:x/y": {Plan: &editspec.RenderPlan{Clips: []editspec.PlanClip{{ID: "inner",
			Background: &editspec.PlanBackground{With: "blur", CutOut: strings.Repeat("1", 64), Missing: true}}}}}}}
	if err := cutOutFiles(ctx, s.a, missing, nil, map[string]string{}, map[string]string{}); err == nil || !strings.Contains(plainFailure(err), "isn't ready in MetaRush (it is gone, or not made)") {
		t.Fatalf("a nested cut's missing cut-out: %v", err)
	}
	if _, err := s.a.DB.C(store.CMattes).UpdateOne(ctx, bson.M{"_id": m.ID}, bson.M{"$set": bson.M{"state": models.MatteMaking}}); err != nil {
		t.Fatal(err)
	}
	if err := cutOutFiles(ctx, s.a, plan, nil, map[string]string{}, map[string]string{}); err == nil || !strings.Contains(plainFailure(err), "isn't ready in MetaRush (it is gone, or not made)") {
		t.Fatalf("a cut-out not made: %v", err)
	}
	// Facts for renders answer for the cut-outs the cut pins.
	if _, ok := (&renderFacts{}).Matte(m.File.SHA256); ok {
		t.Fatal("facts without extras know a cut-out")
	}
}

// A computer without a model doesn't take the work and says what adds it.
func TestAComputerWithoutAModelSaysWhatAddsCutOuts(t *testing.T) {
	s := newShop(t)
	t.Setenv("PATH", t.TempDir()) // no helper on PATH either
	w := s.worker()
	if len(w.mattes()) > 0 {
		t.Skip("this computer has a model that finds people")
	}
	if w.Can(jobs.TypeMatte) {
		t.Fatal("a computer without a model offers cut-outs")
	}
	i := slices.IndexFunc(w.Lacks(), func(l models.WorkerLack) bool { return l.JobType == jobs.TypeMatte })
	if i < 0 || w.Lacks()[i].Component != matteComponent() {
		t.Fatalf("lacks %v", w.Lacks())
	}
}
