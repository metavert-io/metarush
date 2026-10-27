package editspec

import (
	"encoding/json"
	"strings"
	"testing"
)

type fakeFacts struct {
	assets map[string]AssetFacts
	tx     map[string][]TranscriptSegment
}

func (f fakeFacts) Asset(id string) (AssetFacts, bool)       { a, ok := f.assets[id]; return a, ok }
func (f fakeFacts) Transcript(id string) []TranscriptSegment { return f.tx[id] }

const astA = "ast_01jabcdefghjkmnpqrstvwxyz0"
const astB = "ast_01jabcdefghjkmnpqrstvwxyz1"

func facts() fakeFacts {
	return fakeFacts{
		assets: map[string]AssetFacts{
			astA: {ID: astA, Name: "a.mov", Kind: "video", State: "available", InProject: true, Duration: 30, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, TranscriptPrecision: "word"},
			astB: {ID: astB, Name: "b.mp4", Kind: "video", State: "available", InProject: true, Duration: 10, HasVideo: true, HasAudio: false, Width: 1080, Height: 1920, FPS: 30},
		},
		tx: map[string][]TranscriptSegment{
			astA: {{From: 1, To: 4, Text: "We built it twice.", Words: []TranscriptWord{{Text: "We", From: 1, To: 1.3}, {Text: "built", From: 1.3, To: 1.8}, {Text: "it", From: 1.8, To: 2.0}, {Text: "twice.", From: 2.0, To: 2.6}}}},
		},
	}
}

func baseSpec() *Spec {
	s := &Spec{Output: Output{Preset: PresetVertical}, Clips: []Clip{
		{ID: "hook", AssetID: astA, In: 1, Out: 4},
		{ID: "broll", AssetID: astB, In: 0, Out: 3, Transition: &Transition{Type: TransitionCrossfade, Duration: 0.5}},
		{ID: "end", AssetID: astA, In: 10, Out: 14, Locked: true},
	}}
	Normalize(s)
	return s
}

func TestSchemaRejectsBadPlan(t *testing.T) {
	raw := []byte(`{"spec":{"schemaVersion":1,"output":{"fps":29},"clips":[{"id":"Bad ID","assetId":"nope","in":0,"out":1,"bogus":true}]}}`)
	issues := ValidatePlanJSON(raw)
	if len(issues) < 3 {
		t.Fatalf("expected several schema issues, got %v", issues)
	}
	joined, _ := json.Marshal(issues)
	for _, want := range []string{"/spec/output/fps", "/spec/clips/0/id", "/spec/clips/0/assetId"} {
		if !strings.Contains(string(joined), want) {
			t.Errorf("missing issue for %s in %s", want, joined)
		}
	}
	ok := []byte(`{"spec":{"schemaVersion":1,"output":{"preset":"vertical-1080p"},"clips":[{"id":"a","assetId":"` + astA + `","in":0,"out":1}]}}`)
	if issues := ValidatePlanJSON(ok); len(issues) != 0 {
		t.Fatalf("valid plan rejected: %v", issues)
	}
}

func TestTimelineCrossfade(t *testing.T) {
	s := baseSpec()
	clips, total := Timeline(s)
	if clips[0].DurationInFrames != 90 || clips[1].From != 75 || total != 75+90+120 {
		t.Fatalf("unexpected timeline: %+v total=%d", clips, total)
	}
}

func TestValidateAndCaptions(t *testing.T) {
	s := baseSpec()
	s.Clips[0].Out = 99 // beyond duration
	s.Captions = &Captions{Enabled: true, Style: CaptionEmphasis}
	Normalize(s)
	issues := Validate(s, facts(), nil, false)
	if !HasErrors(issues) {
		t.Fatalf("expected out_of_range error, got %v", issues)
	}
	s.Clips[0].Out = 4
	issues = Validate(s, facts(), nil, false)
	if HasErrors(issues) {
		t.Fatalf("unexpected errors: %v", issues)
	}
	plan := Resolve(s, facts(), "rev_x", "prj_x", Hash(s))
	if plan.Captions == nil || plan.Captions.Precision != "word" || len(plan.Captions.Lines) == 0 {
		t.Fatalf("captions not resolved: %+v", plan.Captions)
	}
	if plan.Captions.Lines[0].Words[0].From != 0 {
		t.Fatalf("first caption word should start at timeline frame 0, got %d", plan.Captions.Lines[0].Words[0].From)
	}
}

func TestLocksAndRebase(t *testing.T) {
	base := baseSpec()
	if _, _, err := ApplyOps(base, []Op{{Op: "removeClip", ID: "end"}}, false); err == nil {
		t.Fatal("removing a locked clip must fail")
	}
	next, _, err := ApplyOps(base, []Op{{Op: "updateClip", ID: "hook", Set: json.RawMessage(`{"out":3.5}`)}}, false)
	if err != nil || next.Clips[0].Out != 3.5 {
		t.Fatalf("update failed: %v", err)
	}
	if v := Validate(next, facts(), base, false); HasErrors(v) {
		t.Fatalf("unexpected: %v", v)
	}
	// Someone else changed broll; our hook edit rebases cleanly.
	head, _, _ := ApplyOps(base, []Op{{Op: "updateClip", ID: "broll", Set: json.RawMessage(`{"out":2.5}`)}}, false)
	rebased, conflicts, err := Rebase(base, head, []Op{{Op: "updateClip", ID: "hook", Set: json.RawMessage(`{"out":3.5}`)}}, false)
	if err != nil || len(conflicts) != 0 || rebased.Clips[0].Out != 3.5 || rebased.Clips[1].Out != 2.5 {
		t.Fatalf("rebase failed: %v %v", err, conflicts)
	}
	// Both touched broll: conflict.
	_, conflicts, _ = Rebase(base, head, []Op{{Op: "updateClip", ID: "broll", Set: json.RawMessage(`{"in":0.5}`)}}, false)
	if len(conflicts) != 1 || conflicts[0] != "broll" {
		t.Fatalf("expected broll conflict, got %v", conflicts)
	}
	d := Compare(base, rebased)
	if len(d.ClipsChanged) != 2 {
		t.Fatalf("diff: %+v", d)
	}
}

func TestTemplateParams(t *testing.T) {
	if is := ValidateTemplateParams(TplDataCard, json.RawMessage(`{"value":"40%","label":"faster"}`), "/p"); len(is) == 0 {
		t.Fatal("data card without source must fail")
	}
	if is := ValidateTemplateParams(TplLowerThirdClean, json.RawMessage(`{"name":"Ada Lovelace","title":"Engineer"}`), "/p"); len(is) != 0 {
		t.Fatalf("valid params rejected: %v", is)
	}
}

func TestHashStable(t *testing.T) {
	a, b := baseSpec(), baseSpec()
	if Hash(a) != Hash(b) {
		t.Fatal("hash must be deterministic")
	}
	b.Clips[0].Out = 3.9
	if Hash(a) == Hash(b) {
		t.Fatal("hash must change with content")
	}
}
