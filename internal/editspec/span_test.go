package editspec

import (
	"encoding/json"
	"strings"
	"testing"
)

// A cut of three clips, 30 fps vertical: hook (a 1–4 s, frames 0–90),
// broll (b 0–3 s, dissolving in over 0.5 s: frames 75–165), end (a 10–14 s,
// frames 165–285).
func spanSpec() *Spec {
	s := baseSpec()
	s.Clips[2].Locked = false
	return s
}

func issueCodes(issues []Issue) string {
	var out []string
	for _, i := range issues {
		out = append(out, i.Severity+":"+i.Code)
	}
	return strings.Join(out, " ")
}

func TestSpanFollowsTheClips(t *testing.T) {
	s := spanSpec()
	clips, total := Timeline(s)
	whole := Overlay{ID: "names", Template: TplNameBar, Span: &OverlaySpan{}, Params: json.RawMessage(`{"name":"Ada"}`)}
	from, dur, ok := OverlayWindow(s, whole, clips, total)
	if !ok || from != 0 || dur != total {
		t.Fatalf("whole video: from %d dur %d ok %v (total %d)", from, dur, ok, total)
	}
	// A section from broll to end, 0.5 s in and 2 s long.
	sec := Overlay{ID: "mark", Template: TplLogoCorner, Span: &OverlaySpan{From: "broll", To: "end"}, At: 0.5, Duration: 2, Params: json.RawMessage(`{"logo":"logo"}`)}
	from, dur, ok = OverlayWindow(s, sec, clips, total)
	if !ok || from != clips[1].From+15 || dur != 60 {
		t.Fatalf("section: from %d dur %d ok %v", from, dur, ok)
	}
	// Ending with the first clip: half-way into the dissolve that follows it.
	head := Overlay{ID: "first", Template: TplNameBar, Span: &OverlaySpan{To: "hook"}, Params: json.RawMessage(`{}`)}
	from, dur, _ = OverlayWindow(s, head, clips, total)
	if from != 0 || dur != clips[1].From+clips[1].TransitionIn.DurationInFrames/2 {
		t.Fatalf("to hook: from %d dur %d", from, dur)
	}
	// Trimming the clips moves the span with them; the spec is unchanged.
	s.Clips[0].Out = 3
	clips, total = Timeline(s)
	from, dur, _ = OverlayWindow(s, whole, clips, total)
	if from != 0 || dur != total {
		t.Fatalf("after a trim: from %d dur %d total %d", from, dur, total)
	}
	// A clip that isn't in the cut.
	if _, _, ok := OverlayWindow(s, Overlay{Span: &OverlaySpan{From: "gone"}}, clips, total); ok {
		t.Fatal("a span naming a missing clip resolved")
	}
	if _, _, ok := OverlayWindow(s, Overlay{Span: &OverlaySpan{From: "end", To: "hook"}}, clips, total); ok {
		t.Fatal("a backwards span resolved")
	}
}

func TestChangesBecomeSegmentsAtTheExactFrame(t *testing.T) {
	s := spanSpec()
	o := Overlay{ID: "names", Template: TplNameBar, Span: &OverlaySpan{}, Params: json.RawMessage(`{"name":"Ada","hide":true}`),
		Changes: []OverlayChange{
			{Footage: astA, At: 0, Params: json.RawMessage(`{"hide":false}`)},
			{Footage: astA, At: 2.51, Params: json.RawMessage(`{"name":"Grace"}`)},     // inside hook: frame ceil(2.51*30)-30 = 46
			{Footage: astA, At: 12, Params: json.RawMessage(`{"name":"Ada"}`)},         // inside end
			{Footage: astA, At: 20, Params: json.RawMessage(`{"name":"Never shown"}`)}, // after every clip of a
		}}
	s.Overlays = []Overlay{o}
	plan := Resolve(Clone(s), facts(), "", "", "")
	po := plan.Overlays[0]
	var segs []struct {
		From   int
		Params map[string]any
	}
	for _, sg := range po.Segments {
		var m map[string]any
		_ = json.Unmarshal(sg.Params, &m)
		segs = append(segs, struct {
			From   int
			Params map[string]any
		}{sg.From, m})
	}
	if len(segs) < 4 {
		t.Fatalf("segments: %+v", segs)
	}
	// hook starts on Ada, shown (the change at 0 is before the clip's start).
	if segs[0].From != 0 || segs[0].Params["name"] != "Ada" || segs[0].Params["hide"] != false {
		t.Fatalf("first segment: %+v", segs[0])
	}
	// Grace from the first frame showing 2.51 s of the footage: frame 46 (2.5333 s).
	if segs[1].From != 46 || segs[1].Params["name"] != "Grace" {
		t.Fatalf("second segment: %+v", segs[1])
	}
	// The b-roll (footage without changes) shows the graphic's own fields: hidden.
	if segs[2].Params["hide"] != true {
		t.Fatalf("b-roll segment: %+v", segs[2])
	}
	// end starts at 10 s of a: Grace is still the name there (latest change ≤ 10 s), then Ada from 12 s.
	if segs[3].Params["name"] != "Grace" || segs[len(segs)-1].Params["name"] != "Ada" {
		t.Fatalf("end segments: %+v", segs[3:])
	}
	if po.Segments[0].From != 0 || string(po.Params) != string(po.Segments[0].Params) {
		t.Fatalf("params are not the first segment's: %s", po.Params)
	}
	// Nothing changes on screen: no segments, params merged.
	s.Overlays[0].Changes = []OverlayChange{{Footage: astB, At: 0, Params: json.RawMessage(`{"name":"Lin"}`)}}
	s.Clips = s.Clips[1:2]
	s.Clips[0].Transition = nil
	plan = Resolve(Clone(s), facts(), "", "", "")
	if len(plan.Overlays[0].Segments) != 0 || !strings.Contains(string(plan.Overlays[0].Params), `"Lin"`) {
		t.Fatalf("a single state should be params only: %+v", plan.Overlays[0])
	}
}

func TestOlderGraphicsResolveAsBefore(t *testing.T) {
	s := spanSpec()
	s.Overlays = []Overlay{{ID: "who", Template: TplLowerThirdClean, At: 0.5, Duration: 3, Params: json.RawMessage(`{"name":"Ada","title":"Engineer"}`)}}
	plan := Resolve(Clone(s), facts(), "", "", "")
	b, _ := json.Marshal(plan.Overlays[0])
	if strings.Contains(string(b), "segments") || string(plan.Overlays[0].Params) != `{"name":"Ada","title":"Engineer"}` {
		t.Fatalf("an older graphic resolved differently: %s", b)
	}
	if plan.Overlays[0].From != 15 || plan.Overlays[0].DurationInFrames != 90 {
		t.Fatalf("timing changed: %+v", plan.Overlays[0])
	}
}

func TestSpanAndChangeValidation(t *testing.T) {
	s := spanSpec()
	s.Overlays = []Overlay{
		{ID: "names", Template: TplNameBar, Span: &OverlaySpan{}, Params: json.RawMessage(`{"name":"Ada"}`)},
		{ID: "long", Template: TplTitleCard, Span: &OverlaySpan{}, Params: json.RawMessage(`{"title":"Ada"}`)},
		{ID: "lost", Template: TplNameBar, Span: &OverlaySpan{From: "gone"}, Params: json.RawMessage(`{}`)},
		{ID: "short", Template: TplLowerThirdClean, At: 1, Duration: 0, Params: json.RawMessage(`{"name":"Ada"}`)},
		{ID: "logo", Template: TplLowerThirdClean, At: 4, Duration: 3, Params: json.RawMessage(`{"name":"Ada","logo":"brand"}`),
			Changes: []OverlayChange{{Footage: astA, At: 2, Params: json.RawMessage(`{"logo":"other"}`)}}},
		{ID: "elsewhere", Template: TplNameBar, Span: &OverlaySpan{}, Params: json.RawMessage(`{}`),
			Changes: []OverlayChange{{Footage: "ast_01jabcdefghjkmnpqrstvwxyz9", At: 2, Params: json.RawMessage(`{"name":"Zed"}`)}}},
		{ID: "badfield", Template: TplNameBar, Span: &OverlaySpan{}, Params: json.RawMessage(`{}`),
			Changes: []OverlayChange{{Footage: astA, At: 2, Params: json.RawMessage(`{"name":123}`)}}},
	}
	got := issueCodes(Validate(s, facts(), nil, false))
	for _, want := range []string{"error:overlay_too_long", "error:span_clip", "error:overlay_duration", "error:change_resource", "warning:change_unseen"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	// The name bar itself: persistent, whole video, fine.
	for _, i := range Validate(s, facts(), nil, false) {
		if strings.HasPrefix(i.Path, "/overlays/0") && i.Severity == "error" {
			t.Errorf("the name bar has an error: %+v", i)
		}
		if strings.HasPrefix(i.Path, "/overlays/6/changes/0/params") {
			goto found
		}
	}
	t.Errorf("a change's wrong field type is not reported: %s", got)
found:
}

func TestSplitsAndRemovalsKeepSpans(t *testing.T) {
	s := spanSpec()
	s.Overlays = []Overlay{
		{ID: "sec", Template: TplNameBar, Span: &OverlaySpan{From: "hook", To: "end"}, Params: json.RawMessage(`{}`)},
		{ID: "pinned", Template: TplNameBar, Span: &OverlaySpan{To: "end"}, Params: json.RawMessage(`{}`), Locked: true},
	}
	at := 12.0
	next, _, err := ApplyOps(s, []Op{{Op: "splitClip", ID: "end", At: &at}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if next.Overlays[0].Span.To != "end-2" {
		t.Fatalf("the span should end with the second part: %+v", next.Overlays[0].Span)
	}
	if next.Overlays[1].Span.To != "end" {
		t.Fatalf("a locked graphic keeps its span: %+v", next.Overlays[1].Span)
	}
	next, _, err = ApplyOps(next, []Op{{Op: "removeClip", ID: "hook"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if next.Overlays[0].Span.From != "broll" {
		t.Fatalf("a span starting with a removed clip starts with the next: %+v", next.Overlays[0].Span)
	}
}

func TestSpanEditsDiffAsUpdates(t *testing.T) {
	a := spanSpec()
	a.Overlays = []Overlay{{ID: "names", Template: TplNameBar, Span: &OverlaySpan{From: "broll"}, Params: json.RawMessage(`{"name":"Ada"}`)}}
	b := Clone(a)
	b.Overlays[0].Span = &OverlaySpan{}
	b.Overlays[0].Changes = []OverlayChange{{Footage: astA, At: 2, Params: json.RawMessage(`{"name":"Grace"}`)}}
	ops, err := DiffOps(a, b)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := ApplyOps(a, ops, false)
	if err != nil {
		t.Fatal(err)
	}
	if Hash(got) != Hash(b) {
		ga, _ := json.Marshal(got.Overlays)
		gb, _ := json.Marshal(b.Overlays)
		t.Fatalf("diff did not round-trip:\n%s\n%s\nops %+v", ga, gb, ops)
	}
}
