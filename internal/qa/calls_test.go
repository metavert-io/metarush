package qa

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// callingFacts adds a call recording's picture switches to the test facts:
// astA switches to a split-second of Maya at 3.0–3.2, with names printed
// bottom left.
type callingFacts struct {
	*fakeFacts
	fs map[string]*editspec.FootageShots
}

func (c callingFacts) Footage(id string) (*editspec.FootageShots, bool) {
	f, ok := c.fs[id]
	return f, ok
}

func callFootage() *editspec.FootageShots {
	return &editspec.FootageShots{
		Switches: []editspec.PictureSwitch{{At: 3.0, Last: 2.967}, {At: 3.2, Last: 3.167}},
		Shots: []editspec.FootageShot{{From: 0, To: 3, Person: "Lena Okoye", By: "face"}, {From: 3, To: 3.2, Person: "Maya", By: "voice"},
			{From: 3.2, To: 60, Person: "Lena Okoye", By: "face"}},
		Names: &editspec.PrintedNames{Corner: "bottom-left", Box: editspec.CoverBox{X: 0, Y: 0.9, W: 0.2, H: 0.08}, Shots: 2},
	}
}

func TestCallFootageAndProjectRules(t *testing.T) {
	ts := toolset(t)
	f := facts(true)
	cf := callingFacts{fakeFacts: f, fs: map[string]*editspec.FootageShots{astA: callFootage()}}
	s := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30, Platform: "generic"},
		Clips:    []editspec.Clip{{ID: "intro", AssetID: astB, In: 0, Out: 2}, {ID: "answer", AssetID: astA, In: 3.1, Out: 6}},
		Captions: &editspec.Captions{Enabled: true, Source: "manual", Position: "bottom", Segments: []editspec.CaptionSegment{{From: 0.2, To: 1.5, Text: "Hello there."}}},
		Overlays: []editspec.Overlay{{ID: "who", Template: editspec.TplLowerThirdClean, At: 2.2, Duration: 2.5, Params: json.RawMessage(`{"name":"Lena Okoye"}`)}},
	}
	plan := resolve(t, s, f)
	path := render(t, ts, "960x540", float64(plan.DurationInFrames)/30, "", "")
	flashes := editspec.EdgeFlashes(s, cf)
	var fixes [][]editspec.Op
	for _, fl := range flashes {
		fixes = append(fixes, editspec.EdgeFlashFix(s, fl, cf))
	}
	nb, ok := editspec.BuildNameBar(s, cf, editspec.NameBarOptions{FirstNames: true})
	if !ok {
		t.Fatal("no name bar")
	}
	rules := []models.ProjectRule{
		{Text: "No quick flips between people at the start of clips", Check: true, Measure: "no-flashes"},
		{Text: "Captions at the top", Measure: "captions-top", House: true, HouseOf: "fernbrook"}, // a house rule of the settings it uses (D193)
		{Text: "First names only on screen", Measure: "first-names"},
		{Text: "Under one minute", Measure: "max-length:60"},
		{Text: "Nothing too long for an email"},
	}
	rep := analyze(t, ts, path, plan, s, func(in *Input) {
		withCuts(s, f)(in)
		in.Calls = &CallFacts{Flashes: flashes, FlashFixes: fixes, PrintedClips: 1, Corner: "bottom-left", NameBar: &nb}
		in.Rules = rules
	})
	dump(t, rep)
	fl := find(rep, "edge_flash")
	if fl == nil || fl.Severity != models.QAError || !strings.Contains(fl.Message, "opens with a split-second of Maya (0.1 s) before Lena Okoye appears") ||
		fl.Fix == nil || len(fl.Fix.Ops) == 0 || fl.Fix.Ops[0].Op != "trimClip" {
		t.Fatalf("flash: %+v", fl)
	}
	pn := find(rep, "printed_names")
	if pn == nil || pn.Fix == nil || pn.Fix.Ops[len(pn.Fix.Ops)-1].Op != "addOverlay" || pn.Fix.Ops[0].Op != "removeOverlay" {
		t.Fatalf("printed names: %+v", pn)
	}
	var broken []string
	for _, x := range rep.Findings {
		if x.Kind == "rule_broken" {
			broken = append(broken, x.Message)
		}
	}
	joined := strings.Join(broken, "\n")
	if !strings.Contains(joined, "The house rule “Captions at the top” (from “fernbrook”'s settings): the captions are at the bottom") ||
		!strings.Contains(joined, "The project's rule “First names only on screen”: the name title who shows “Lena Okoye”") {
		t.Fatalf("rules: %s", joined)
	}
	if strings.Contains(joined, "one minute") {
		t.Fatalf("a short video broke the length rule: %s", joined)
	}
	if eye := find(rep, "rule_by_eye"); eye == nil || !strings.Contains(eye.Message, "Nothing too long for an email") {
		t.Fatalf("rules to look at: %+v", eye)
	}
	// Every fix applies to the rendered cut.
	for _, k := range []string{"edge_flash", "printed_names"} {
		if _, _, err := editspec.ApplyOps(s, find(rep, k).Fix.Ops, false); err != nil {
			t.Fatalf("%s fix does not apply: %v", k, err)
		}
	}
	for _, x := range rep.Findings {
		if x.Kind == "rule_broken" && x.Fix != nil && len(x.Fix.Ops) > 0 {
			if _, _, err := editspec.ApplyOps(s, x.Fix.Ops, false); err != nil {
				t.Fatalf("rule fix %q does not apply: %v", x.Message, err)
			}
		}
	}
	got := map[string]string{}
	for _, c := range rep.Checks {
		got[c.Name] = c.Status
	}
	if got["call_footage"] != "fail" || got["project_rules"] != "warning" {
		t.Fatalf("checks: %v", got)
	}
	if rep.Version != "qa-7" {
		t.Fatalf("version %s", rep.Version)
	}
}
