package abilities

import (
	"context"
	"os"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiservices/aiconformance"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// growManifest provides larger pictures (D281): the picture and a scale in,
// a picture out; free, no network. Its test has no picture (a test can't
// take files yet), so the picture is optional and the script makes one.
const growManifest = `ability: 1
name: grow
title: Grow pictures
does: Makes a picture larger.
runs: python3 grow.py
inputs:
  picture: {kind: picture, optional: true, says: The picture}
  scale: {kind: number, min: 2, max: 4, default: 2, says: How many times larger}
outputs: [picture]
provides: [upscale]
cost: free
needs: {programs: [python3]}
licence: MIT
test: {inputs: {}}
`

// growScript copies the picture it is given (a stand-in for a model) and
// says what it was asked.
const growScript = `import json, os, shutil
inputs = json.load(open(os.environ["METARUSH_INPUTS"]))
out = os.environ["METARUSH_OUTPUT"]
src = inputs.get("picture")
if src:
    data = open(src, "rb").read() + b"grown"  # a picture after its end: still a PNG, other bytes
    open(os.path.join(out, "bigger.png"), "wb").write(data)
else:
    open(os.path.join(out, "bigger.svg"), "w").write('<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8"/>')
print("scale", inputs.get("scale"))
`

func TestProvidesIsChecked(t *testing.T) {
	cases := map[string]string{
		"provides: [video]":         "generated video has its own steps",
		"provides: [voice-copy]":    "likeness",
		"provides: [transcription]": "can't bring words back yet",
		"provides: [colour]":        "isn't a kind of AI work an ability provides",
		"provides: [music]":         `needs an input "prompt"`,
	}
	for line, want := range cases {
		m, err := ParseManifest([]byte(strings.Replace(growManifest, "provides: [upscale]", line, 1)))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(problemTexts(m.Validate(growFiles)), "; "); !strings.Contains(got, want) {
			t.Errorf("%s: %q, want %q", line, got, want)
		}
	}
	// Inputs the kind doesn't fill must have a default or be optional.
	m, _ := ParseManifest([]byte(strings.Replace(growManifest, "  scale:", "  colour: {kind: words}\n  scale:", 1)))
	if got := strings.Join(problemTexts(m.Validate(growFiles)), "; "); !strings.Contains(got, `give "colour" a default or make it optional`) {
		t.Errorf("an extra required input: %q", got)
	}
	m, _ = ParseManifest([]byte(strings.Replace(growManifest, "outputs: [picture]", "outputs: [sound]", 1)))
	if got := strings.Join(problemTexts(m.Validate(growFiles)), "; "); !strings.Contains(got, "brings back picture") {
		t.Errorf("the wrong output: %q", got)
	}
	m, _ = ParseManifest([]byte(growManifest))
	if ps := m.Validate(growFiles); len(ps) > 0 {
		t.Fatalf("a good manifest: %v", ps)
	}
}

// An approved ability that provides a kind is a provider of it: in the
// catalogue with its price and data terms, ready where it is set up, and a
// request runs it in the sandbox as a run of that work — recorded, its
// output handed to the AI work, no bell of its own.
func TestApprovedAbilityProvides(t *testing.T) {
	a := newApp(t)
	needSandbox(t, a)
	ctx := context.Background()
	v := approved(t, a, map[string]string{ManifestFile: growManifest, "grow.py": growScript})
	aiservices.Refresh(ctx, a)
	t.Cleanup(func() { syncMu.Lock(); drop(ProviderName("grow")); syncMu.Unlock() })
	p, err := aiservices.ProviderOf("ability-grow")
	if err != nil || p.Route != aiservices.RouteAbility || len(p.Models) != 1 || p.Models[0].Kind != aiservices.KindUpscale || !strings.Contains(p.Data.Summary, "nothing leaves this computer") {
		t.Fatalf("catalogue: %+v %v", p, err)
	}
	ad, err := aiservices.Get("ability-grow", aiservices.KindUpscale)
	if err != nil {
		t.Fatal(err)
	}
	e := aiservices.Env{Tools: a.Tools, Cfg: a.Cfg, Home: a.Cfg.Home(), App: a}
	if err := ad.(aiservices.Here).Ready(e, "grow"); err != nil {
		t.Fatalf("ready: %v", err)
	}
	if why := ad.(aiservices.PersonOnly).PersonOnly("grow"); why != "" {
		t.Errorf("a free ability that sends nothing asks for a person: %q", why)
	}
	m, _ := aiservices.ModelFor("ability-grow", aiservices.KindUpscale, "")
	req := aiconformance.SampleRequest(t, m, t.TempDir())
	req.Tag = "gen_test0000000000000000000"
	est, err := ad.Estimate(req, now())
	if err != nil || est.USD != 0 {
		t.Fatalf("estimate %+v %v", est, err)
	}
	tk, err := ad.Submit(aiservices.WithEnv(ctx, e), "", req)
	if err != nil {
		t.Fatal(err)
	}
	if len(tk.Ready.Outputs) != 1 || tk.Ready.Outputs[0].MIME != "image/png" {
		t.Fatalf("outputs %+v", tk.Ready.Outputs)
	}
	want, _ := os.ReadFile(req.Inputs[0].Path)
	if string(tk.Ready.Outputs[0].Data) != string(want)+"grown" {
		t.Error("the picture it made isn't what the stand-in wrote")
	}
	r, err := getRun(ctx, a, tk.ID)
	if err != nil || r.State != models.AbilityRunDone || r.Work != req.Tag || r.Hash != v.Version || len(r.Outputs) != 1 || r.Outputs[0].Status != "to the AI work" ||
		!strings.Contains(r.Log, "scale 2") {
		t.Fatalf("the run %+v %v", r, err)
	}
	var ns []models.Notification
	_ = a.DB.Find(ctx, store.CNotifications, a.WorkspaceID(), bson.M{"kind": models.NotifyAbilityRun}, &ns)
	if len(ns) != 0 {
		t.Errorf("the run rang the bell itself: %+v", ns)
	}
	// Taking the approval back takes it out of the catalogue.
	mustCall[AbilityDecisionOut](t, a, "ability.revoke", AbilityRevokeIn{Ref: "grow"}, ops.Caller{Human: true})
	aiservices.Refresh(ctx, a)
	if _, err := aiservices.ProviderOf("ability-grow"); err == nil {
		t.Error("a revoked ability still provides")
	}
	if _, err := aiservices.Get("ability-grow", aiservices.KindUpscale); err == nil {
		t.Error("a revoked ability's adapter is still registered")
	}
}

var growFiles = map[string]bool{ManifestFile: true, "grow.py": true}
