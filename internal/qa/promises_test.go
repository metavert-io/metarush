package qa

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/visual"
)

// promiseRun is a quality check of a made-up render: the plan, the cut and
// what decoding it measured, without ffmpeg.
func promiseRun(in Input, plan *editspec.RenderPlan, m models.QAMeasurements) *run {
	in.Plan, in.Scale = plan, 1
	return &run{in: in, plan: plan, fps: float64(plan.FPS), rep: &models.QAReport{Measurements: m, Findings: []models.QAFinding{}, Checks: []models.QACheck{}}}
}

func promise(id, text, measure string) models.ProjectRule {
	return models.ProjectRule{ID: id, Text: text, Measure: measure, Promise: true}
}

func overlay(id, tpl string, from, dur int, params string) editspec.PlanOverlay {
	return editspec.PlanOverlay{ID: id, Template: tpl, From: from, DurationInFrames: dur, Params: json.RawMessage(params)}
}

// A widescreen video of 72 seconds that breaks most of its promises, and a
// vertical short that keeps them all: every promise gets a plain sentence and
// a measured result, a broken one a problem (an error) with its fix, one
// MetaRush can't measure here a warning (D300). The golden file holds what
// the report says.
func TestPromisesGolden(t *testing.T) {
	type golden struct {
		Name     string             `json:"name"`
		Promises []models.QAPromise `json:"promises"`
		Findings []models.QAFinding `json:"findings"`
		Check    models.QACheck     `json:"check"`
	}
	var got []golden
	measure := func(name string, r *run) {
		r.projectRules()
		r.promises()
		r.finish()
		r.linkPromises()
		g := golden{Name: name, Promises: r.rep.Promises}
		for _, f := range r.pend {
			g.Findings = append(g.Findings, *f)
		}
		for _, c := range r.rep.Checks {
			if c.Name == "promises" {
				g.Check = c
			}
		}
		got = append(got, g)
	}

	// 1. Widescreen, 72 s: the music 8 steps under the speech, captions over
	// part of the speech, an end card at the end, an AI-generated shot with a
	// title saying so and AI narration without one.
	const fps = 30
	wide := &editspec.RenderPlan{FPS: fps, Width: 1920, Height: 1080, DurationInFrames: 72 * fps,
		Clips: []editspec.PlanClip{
			{ID: "talk", AssetID: "ast_talk", From: 0, DurationInFrames: 40 * fps, Volume: 1, HasAudio: true},
			{ID: "dream", AssetID: "ast_gen", From: 40 * fps, DurationInFrames: 6 * fps, Volume: 1},
			{ID: "talk-2", AssetID: "ast_talk", From: 46 * fps, DurationInFrames: 26 * fps, Volume: 1, HasAudio: true},
		},
		Audio: []editspec.PlanAudio{
			{ID: "music", AssetID: "ast_music", Role: editspec.RoleMusic, From: 0, DurationInFrames: 72 * fps, GainDB: -14},
			{ID: "line-1", AssetID: "ast_vo", Role: editspec.RoleVoiceover, From: 50 * fps, DurationInFrames: 5 * fps, AIVoice: true},
		},
		Captions: &editspec.PlanCaptions{Style: editspec.CaptionClean, Position: "lower", Lines: []editspec.PlanCaptionLine{
			{From: 0, To: 10 * fps, Text: "We started the garden in spring"},
			{From: 10 * fps, To: 30 * fps, Text: "and it grew all summer long"},
		}},
		Overlays: []editspec.PlanOverlay{
			overlay("ai-note", editspec.TplLowerThirdClean, 41*fps, 3*fps, `{"name":"Made with AI","title":"Imagined shot"}`),
			overlay("ada", editspec.TplLowerThirdClean, 5*fps, 4*fps, `{"name":"Ada Lovelace"}`),
			overlay("end", editspec.TplEndCard, 69*fps, 3*fps, `{"headline":"Thanks for watching"}`),
		},
	}
	spec := &editspec.Spec{Output: editspec.Output{Width: 1920, Height: 1080, FPS: 30}, Captions: &editspec.Captions{Enabled: true, Source: "transcript"},
		Resources: map[string]editspec.ResourcePin{"logo": {ID: "res_00000000000000000000000000", SHA256: strings.Repeat("a", 64)}},
		Overlays:  []editspec.Overlay{{ID: "ada", Template: editspec.TplLowerThirdClean, At: 5, Duration: 4, Params: json.RawMessage(`{"name":"Ada Lovelace"}`)}}}
	in := Input{Spec: spec, Speech: []Span{{From: 0, To: 40}, {From: 46, To: 72}}, SpeechPrec: "word",
		Loudness:  map[string]Loudness{"ast_talk": {LUFS: -20, TruePeak: -3}, "ast_music": {LUFS: -14, TruePeak: -1}, "ast_vo": {LUFS: -18, TruePeak: -2}},
		Generated: map[string]GeneratedClip{"ast_gen": {Provider: "Google Gemini"}},
		Rules: []models.ProjectRule{
			promise("rul_1", "Under 60 seconds", "max-length:60"),
			promise("rul_2", "Under 60 seconds for shorts", "max-length:60@vertical"),
			promise("rul_3", "Captions on every video", "captions"),
			promise("rul_4", "Logo in the last 3 s", "logo-end:3"),
			promise("rul_5", "Logo within the first 5 seconds", "logo-start:5"),
			promise("rul_6", "Voices 10 dB over the music", "speech-over-music:10"),
			promise("rul_7", "No music louder than speech", "speech-over-music:0"),
			promise("rul_8", "Nothing generated without disclosure", "disclosed"),
			promise("rul_9", "No faces cut off", "faces-in-frame"),
			promise("rul_10", "Delivered in 4K", "min-size:2160"),
			promise("rul_11", "At least 24 fps", "min-fps:24"),
			promise("rul_12", "First names only", "first-names"),
			promise("rul_13", "Warm and friendly", ""),
			{ID: "rul_14", Text: "Our logo in the first 2 seconds", Measure: "logo-start:2"}, // a rule, not a promise: a warning
		}}
	in.Rules[3].House, in.Rules[3].HouseOf = true, "Acme"
	measure("widescreen breaks most promises", promiseRun(in, wide, models.QAMeasurements{Duration: 72, Width: 1920, Height: 1080, FPS: 30}))

	// 2. A vertical short of 45 s that keeps every promise: its description
	// says what was made with AI, captions over all of the speech, a logo
	// through the last 3 seconds, no music, the on-screen check found no
	// face cut off.
	short := &editspec.RenderPlan{FPS: fps, Width: 1080, Height: 1920, DurationInFrames: 45 * fps,
		Clips: []editspec.PlanClip{
			{ID: "talk", AssetID: "ast_talk", From: 0, DurationInFrames: 40 * fps, Volume: 1, HasAudio: true, SourceWidth: 1080, SourceHeight: 1920, PlaybackRate: 1},
			{ID: "dream", AssetID: "ast_gen", From: 40 * fps, DurationInFrames: 5 * fps, Volume: 1, SourceWidth: 1080, SourceHeight: 1920, PlaybackRate: 1},
		},
		Captions: &editspec.PlanCaptions{Style: editspec.CaptionBold, Position: "lower", Lines: []editspec.PlanCaptionLine{
			{From: 0, To: 20 * fps, Text: "We started the garden in spring"},
			{From: 20 * fps, To: 40 * fps, Text: "and it grew all summer long"},
		}},
		Overlays: []editspec.PlanOverlay{overlay("logo", editspec.TplLogoCorner, 42*fps, 3*fps, `{"logo":"logo"}`)},
	}
	vspec := &editspec.Spec{Output: editspec.Output{Width: 1080, Height: 1920, FPS: 30}, Captions: &editspec.Captions{Enabled: true, Source: "transcript", Style: editspec.CaptionBold}}
	vin := Input{Spec: vspec, Speech: []Span{{From: 0, To: 40}}, SpeechPrec: "word", Loudness: map[string]Loudness{"ast_talk": {LUFS: -20}},
		Generated: map[string]GeneratedClip{"ast_gen": {Provider: "Google Gemini"}},
		Notes:     &models.DeliveryNotes{Title: "The garden", Description: "Our garden story.\n\nMade with AI: the shot “dream” (Google Gemini)."},
		OnScreen:  map[string]*visual.Index{"ast_talk": {Step: 1, Duration: 40}, "ast_gen": {Step: 1, Duration: 5}},
		Rules: []models.ProjectRule{
			promise("rul_1", "Under 60 seconds for shorts", "max-length:60@vertical"),
			promise("rul_2", "Captions on every video", "captions"),
			promise("rul_3", "Logo in the last 3 s", "logo-end:3"),
			promise("rul_4", "Nothing generated without disclosure", "disclosed"),
			promise("rul_5", "No music louder than speech", "speech-over-music:6"),
			promise("rul_6", "No faces cut off", "faces-in-frame"),
			promise("rul_7", "At least 1080p", "min-size:1080"),
			promise("rul_8", "30 fps", "min-fps:30"),
		}}
	r := promiseRun(vin, short, models.QAMeasurements{Duration: 45, Width: 1080, Height: 1920, FPS: 30})
	r.rep.Checks = append(r.rep.Checks, models.QACheck{Name: "on_screen", Status: "pass", Basis: models.QAObserved})
	measure("a vertical short keeps them all", r)
	if r.rep.NotesSHA == "" {
		t.Error("the description the disclosure promise read isn't fingerprinted")
	}

	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	path := filepath.Join("testdata", "promises.golden.json")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (UPDATE_GOLDEN=1 writes it)", err)
	}
	if !bytes.Equal(b, want) {
		t.Errorf("the promises' measurements differ from %s (UPDATE_GOLDEN=1 to accept):\n%s", path, b)
	}

	// What the golden says, checked by hand where it matters.
	wideG, shortG := got[0], got[1]
	status := map[string]string{}
	for _, p := range wideG.Promises {
		status[p.RuleID] = p.Status
		if p.Says == "" || p.Measured == "" {
			t.Errorf("%s: no sentence or no result: %+v", p.RuleID, p)
		}
		if p.Status == models.PromiseBroken || p.Status == models.PromiseUnmeasured {
			if p.FindingID == "" {
				t.Errorf("%s: no finding", p.RuleID)
			}
		}
	}
	for id, want := range map[string]string{"rul_1": "broken", "rul_2": "notApplicable", "rul_3": "broken", "rul_4": "kept", "rul_5": "broken", "rul_6": "broken",
		"rul_7": "kept", "rul_8": "broken", "rul_9": "unmeasured", "rul_10": "broken", "rul_11": "kept", "rul_12": "broken", "rul_13": "unmeasured"} {
		if status[id] != want {
			t.Errorf("%s: %s, want %s", id, status[id], want)
		}
	}
	if _, ok := status["rul_14"]; ok {
		t.Error("a rule that is only a rule is listed with the promises")
	}
	errs, ruleWarn := 0, false
	for _, f := range wideG.Findings {
		if f.Severity == models.QAError {
			errs++
			if f.Kind != "promise_broken" {
				t.Errorf("an error that isn't a broken promise: %+v", f)
			}
		}
		ruleWarn = ruleWarn || (f.Kind == "rule_broken" && f.Severity == models.QAWarning && strings.Contains(f.Message, "Our logo in the first 2 seconds"))
		if fs := plainwords.CheckShown(f.Message); fs != nil {
			t.Errorf("%q: %v", f.Message, fs)
		}
	}
	if errs != 7 || !ruleWarn || wideG.Check.Status != "fail" {
		t.Errorf("%d errors (want 7), the plain rule measured: %v, check %+v", errs, ruleWarn, wideG.Check)
	}
	for _, p := range shortG.Promises {
		if p.Status != models.PromiseKept {
			t.Errorf("the short: %s is %s: %s", p.Text, p.Status, p.Measured)
		}
	}
	if len(shortG.Findings) != 0 || shortG.Check.Status != "pass" {
		t.Errorf("the short: %+v %+v", shortG.Findings, shortG.Check)
	}
}

// Promises that passed when broken (review of D300): a description that
// says "generated" about something else isn't a disclosure, but MetaRush's
// own line written from the records is (and an out-of-date one isn't); a
// generated clip inside a cut played in this one counts; faces are kept only
// where every second shown was looked at; a promise broken in several places
// links every finding; captions burned into the footage count when the index
// sees them or the promise says so.
func TestPromisesHoldWhenBroken(t *testing.T) {
	const fps = 30
	disclosed := []models.ProjectRule{promise("rul_1", "Nothing generated without disclosure", "disclosed")}
	plan := &editspec.RenderPlan{FPS: fps, Width: 1080, Height: 1920, DurationInFrames: 20 * fps,
		Clips: []editspec.PlanClip{{ID: "dream", AssetID: "ast_gen", From: 0, DurationInFrames: 20 * fps, Volume: 1}}}
	run1 := func(notes *models.DeliveryNotes, p *editspec.RenderPlan) models.QAPromise {
		in := Input{Spec: &editspec.Spec{}, Generated: map[string]GeneratedClip{"ast_gen": {Provider: "Veo"}}, AssetNames: map[string]string{"ast_gen": "Spring garden"},
			Notes: notes, Rules: disclosed}
		r := promiseRun(in, p, models.QAMeasurements{Duration: 20})
		r.promises()
		return r.rep.Promises[0]
	}
	loose := run1(&models.DeliveryNotes{Title: "Spring", Description: "Chapters below were generated automatically."}, plan)
	if loose.Status != models.PromiseBroken || loose.Expected != "Made with AI: the shot “Spring garden” (Veo)." {
		t.Fatalf("an unrelated 'generated' disclosed it: %+v", loose)
	}
	if ok := run1(&models.DeliveryNotes{Description: "Our garden.\n\n" + loose.Expected}, plan); ok.Status != models.PromiseKept {
		t.Fatalf("the records' own line didn't disclose it: %+v", ok)
	}
	if old := run1(&models.DeliveryNotes{Description: "Made with AI: the shot “Autumn” (Veo)."}, plan); old.Status != models.PromiseBroken || !strings.Contains(old.Measured, "out of date") {
		t.Fatalf("an out-of-date line disclosed it: %+v", old)
	}

	// A generated clip inside a cut played in this one.
	nk := "cut:prj_00000000000000000000000000/rev_00000000000000000000000000"
	inner := &editspec.RenderPlan{FPS: fps, Width: 1080, Height: 1920, DurationInFrames: 10 * fps,
		Clips: []editspec.PlanClip{{ID: "inner-dream", AssetID: "ast_gen", From: 0, DurationInFrames: 10 * fps, Volume: 1}}}
	outer := &editspec.RenderPlan{FPS: fps, Width: 1080, Height: 1920, DurationInFrames: 10 * fps,
		Clips:  []editspec.PlanClip{{ID: "nested", AssetID: nk, From: 0, DurationInFrames: 10 * fps, Volume: 1}},
		Nested: map[string]*editspec.NestedPlan{nk: {Plan: inner}}}
	if n := run1(nil, outer); n.Status != models.PromiseBroken || !strings.Contains(n.Measured, "in a cut played in this one") {
		t.Fatalf("a generated clip in a nested cut: %+v", n)
	}

	// Faces: 5 s of 30 s indexed, the rest not, or nested.
	fplan := &editspec.RenderPlan{FPS: fps, Width: 1080, Height: 1920, DurationInFrames: 30 * fps,
		Clips: []editspec.PlanClip{
			{ID: "a", AssetID: "ast_a", From: 0, DurationInFrames: 5 * fps, Volume: 1, SourceWidth: 1920, SourceHeight: 1080, PlaybackRate: 1},
			{ID: "b", AssetID: "ast_b", From: 5 * fps, DurationInFrames: 15 * fps, Volume: 1, SourceWidth: 1920, SourceHeight: 1080, PlaybackRate: 1},
			{ID: "n", AssetID: nk, From: 20 * fps, DurationInFrames: 10 * fps, Volume: 1, SourceWidth: 1080, SourceHeight: 1920},
		}}
	in3 := Input{Spec: &editspec.Spec{}, OnScreen: map[string]*visual.Index{"ast_a": {Step: 1, Duration: 5}},
		Rules: []models.ProjectRule{promise("rul_1", "No faces cut off", "faces-in-frame")}}
	r := promiseRun(in3, fplan, models.QAMeasurements{Duration: 30})
	r.onScreen(context.Background())
	r.promises()
	if p := r.rep.Promises[0]; p.Status != models.PromiseUnmeasured || !strings.Contains(p.Measured, "“b”") || !strings.Contains(p.Measured, "the cut played in this one") {
		t.Fatalf("faces with 5 s of 30 s looked at: %+v", p)
	}

	// First names broken by two graphics: both findings are the promise's.
	spec := &editspec.Spec{Overlays: []editspec.Overlay{
		{ID: "ada", Template: editspec.TplLowerThirdClean, At: 1, Duration: 3, Params: json.RawMessage(`{"name":"Ada Lovelace"}`)},
		{ID: "sam", Template: editspec.TplLowerThirdClean, At: 6, Duration: 3, Params: json.RawMessage(`{"name":"Sam Lee"}`)},
	}}
	r = promiseRun(Input{Spec: spec, Rules: []models.ProjectRule{promise("rul_1", "First names only", "first-names")}}, &editspec.RenderPlan{FPS: fps, Width: 1920, Height: 1080, DurationInFrames: 10 * fps}, models.QAMeasurements{Duration: 10})
	r.promises()
	r.finish()
	r.linkPromises()
	if p := r.rep.Promises[0]; len(p.FindingIDs) != 2 || p.FindingID != p.FindingIDs[0] {
		t.Fatalf("first names in two graphics: %+v", p)
	}

	// Captions already in the footage (captions:burned-in) count when they are
	// read low in the picture while people speak; never when nobody could read them.
	cplan := &editspec.RenderPlan{FPS: fps, Width: 1080, Height: 1920, DurationInFrames: 10 * fps,
		Clips: []editspec.PlanClip{{ID: "talk", AssetID: "ast_t", From: 0, DurationInFrames: 10 * fps, Volume: 1, HasAudio: true, SourceWidth: 1080, SourceHeight: 1920, PlaybackRate: 1}}}
	words := &visual.Index{Step: 1, Duration: 10, Text: []visual.TextSpan{{From: 0, To: 10, Text: "and it grew all summer", Box: [4]float64{0.1, 0.8, 0.8, 0.06}}}}
	capIn := func(measure string, idx map[string]*visual.Index) models.QAPromise {
		r := promiseRun(Input{Spec: &editspec.Spec{}, Speech: []Span{{From: 0, To: 9}}, SpeechPrec: "word", OnScreen: idx,
			Rules: []models.ProjectRule{promise("rul_1", "Captions on every video", measure)}}, cplan, models.QAMeasurements{Duration: 10})
		r.promises()
		return r.rep.Promises[0]
	}
	if p := capIn("captions:burned-in", map[string]*visual.Index{"ast_t": words}); p.Status != models.PromiseKept || !strings.Contains(p.Measured, "footage's own captions") {
		t.Fatalf("the footage's own captions, read: %+v", p)
	}
	// Plain captions are the cut's own: footage words don't stand in for them.
	if p := capIn("captions", map[string]*visual.Index{"ast_t": words}); p.Status != models.PromiseBroken {
		t.Fatalf("plain captions kept by the footage's words: %+v", p)
	}
	if p := capIn("captions", nil); p.Status != models.PromiseBroken {
		t.Fatalf("no captions at all: %+v", p)
	}
	// Burned in, but nothing could read them: couldn't measure, never kept.
	if p := capIn("captions:burned-in", nil); p.Status != models.PromiseUnmeasured {
		t.Fatalf("burned-in captions nobody could read: %+v", p)
	}
	blank := &visual.Index{Step: 1, Duration: 10}
	if p := capIn("captions:burned-in", map[string]*visual.Index{"ast_t": blank}); p.Status != models.PromiseBroken {
		t.Fatalf("footage read, no captions in it: %+v", p)
	}
}

// Each change of words is its own thing to disclose, in its speaker's voice —
// not "the narration's voice", and not hidden behind the narration's — and a
// change in a cut played in this one counts and is noted too (D311).
func TestEveryChangeOfWordsIsDisclosed(t *testing.T) {
	const fps = 30
	disclosed := []models.ProjectRule{promise("rul_1", "Nothing generated without disclosure", "disclosed")}
	plan := &editspec.RenderPlan{FPS: fps, Width: 1920, Height: 1080, DurationInFrames: 20 * fps, Audio: []editspec.PlanAudio{
		{ID: "vo-1", AssetID: "ast_vo", From: 0, DurationInFrames: 3 * fps, AIVoice: true},
		{ID: "said-swc_a", AssetID: "ast_sa", From: 5 * fps, DurationInFrames: fps, AIVoice: true},
		{ID: "inner/said-swc_b", AssetID: "ast_sb", From: 12 * fps, DurationInFrames: fps, AIVoice: true, Nested: "inner"},
	}}
	said := []SaidFacts{{ChangeID: "swc_a", From: 5, To: 6, Who: "Ada Lovelace", Words: "fifteen", Before: "twelve"},
		{ChangeID: "swc_b", From: 12, To: 13, Who: "Grace Hopper", Words: "Tuesday", Before: "Monday"}}
	in := Input{Spec: &editspec.Spec{}, Said: said, Rules: disclosed, Notes: &models.DeliveryNotes{Description: "Made with AI: the narration's voice."}}
	r := promiseRun(in, plan, models.QAMeasurements{Duration: 20})
	r.promises()
	p := r.rep.Promises[0]
	if p.Status != models.PromiseBroken || !strings.Contains(p.Expected, "“fifteen”") || !strings.Contains(p.Expected, "Ada Lovelace's voice") ||
		!strings.Contains(p.Expected, "“Tuesday”") || !strings.Contains(p.Expected, "Grace Hopper's voice") || !strings.Contains(p.Expected, "the narration's voice") {
		t.Fatalf("each change must be disclosed in its speaker's voice: %+v", p)
	}
	if ok := promiseRun(Input{Spec: &editspec.Spec{}, Said: said, Rules: disclosed, Notes: &models.DeliveryNotes{Description: p.Expected}}, plan, models.QAMeasurements{Duration: 20}); true {
		ok.promises()
		if ok.rep.Promises[0].Status != models.PromiseKept {
			t.Fatalf("the records' own line discloses them: %+v", ok.rep.Promises[0])
		}
	}
	// The note for each, the nested one's too, from the plan alone (no facts given).
	n := &run{in: Input{Spec: &editspec.Spec{Mix: &editspec.Mix{Said: []editspec.SaidSound{{ID: "swc_a", Who: "Ada Lovelace", Words: "fifteen", Before: "twelve"}}}}}, fps: fps, plan: plan}
	n.changedWords()
	if len(n.pend) != 2 || !strings.Contains(n.pend[1].Message, "a cut played in this one") {
		t.Fatalf("a change in a nested cut is noted too: %+v", n.pend)
	}
}
