package editspec

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// callFacts adds a call recording's picture switches to the test facts: astA
// is Zoom's speaker view (Lena, a split-second of Maya at 3.0–3.2,
// Lena again, Maya from 12.0, Lena from 20.0), with names printed
// bottom left.
type callFacts struct {
	fakeFacts
	fs map[string]*FootageShots
}

func (c callFacts) Footage(id string) (*FootageShots, bool) { f, ok := c.fs[id]; return f, ok }

func speakerView() *FootageShots {
	return &FootageShots{
		Switches: []PictureSwitch{{At: 3.0, Last: 2.967}, {At: 3.2, Last: 3.167}, {At: 12.0, Last: 11.967}, {At: 20.0, Last: 19.967}},
		Shots: []FootageShot{{From: 0, To: 3, Person: "Lena", By: "face"}, {From: 3, To: 3.2, Person: "Maya", By: "voice"},
			{From: 3.2, To: 12, Person: "Lena", By: "face", Printed: "Lena O."}, {From: 12, To: 20, Person: "Maya Lindqvist", By: "printed", Printed: "Maya Lindqvist"},
			{From: 20, To: 30, Person: "Lena", By: "face"}},
		Names: &PrintedNames{Corner: "bottom-left", Box: CoverBox{X: 0, Y: 0.9, W: 0.2, H: 0.08}, Shots: 4},
	}
}

func callTestFacts() callFacts {
	f := facts()
	a := f.assets[astA]
	a.Width, a.Height = 1920, 1080
	f.assets[astA] = a
	f.tx[astA] = []TranscriptSegment{{From: 2.95, To: 3.6, Text: "So when", Words: []TranscriptWord{{Text: "So", From: 2.95, To: 3.1}, {Text: "when", From: 3.15, To: 3.6}}}}
	return callFacts{fakeFacts: f, fs: map[string]*FootageShots{astA: speakerView()}}
}

func callSpec() *Spec {
	s := &Spec{SchemaVersion: 1, Output: Output{Preset: PresetLandscape}, Clips: []Clip{
		{ID: "open", AssetID: astA, In: 1, Out: 2.9},
		{ID: "answer", AssetID: astA, In: 3.1, Out: 11.5}, // starts on 0.1 s of Maya
		{ID: "broll", AssetID: astB, In: 0, Out: 2},
		{ID: "reply", AssetID: astA, In: 15, Out: 20.2}, // ends on 0.2 s of Lena
	}}
	Normalize(s)
	return s
}

func TestFlashesAtClipEdgesAreFound(t *testing.T) {
	s := callSpec()
	got := EdgeFlashes(s, callTestFacts())
	if len(got) != 2 {
		t.Fatalf("flashes: %+v", got)
	}
	start, end := got[0], got[1]
	if start.ClipID != "answer" || start.Edge != "start" || start.Flash != "Maya" || start.Main != "Lena" || start.Frames != 3 || !start.Words {
		t.Fatalf("start flash: %+v", start)
	}
	if end.ClipID != "reply" || end.Edge != "end" || end.Flash != "Lena" || end.Main != "Maya Lindqvist" || end.Frames < 5 {
		t.Fatalf("end flash: %+v", end)
	}
	if w := FlashWords(start); w != "opens with a split-second of Maya (0.1 s) before Lena appears" {
		t.Fatalf("words: %q", w)
	}
	// The edit check says so, in plain words.
	var msgs []string
	for _, is := range Validate(s, callTestFacts(), nil, false) {
		if is.Code == "edge_flash" || is.Code == "printed_names" {
			msgs = append(msgs, is.Code+": "+is.Message)
		}
	}
	joined := strings.Join(msgs, "\n")
	if strings.Count(joined, "edge_flash") != 2 || !strings.Contains(joined, "printed_names: Names printed by the call app show at the bottom left in 3 clips") {
		t.Fatalf("edit check: %s", joined)
	}
}

func TestTheFixTrimsAtTheExactSwitchAndKeepsTheWords(t *testing.T) {
	s := callSpec()
	f := callTestFacts()
	fl := EdgeFlashes(s, f)
	ops := EdgeFlashFix(s, fl[0], f)
	if len(ops) != 2 || ops[0].Op != "trimClip" || *ops[0].To != 3.2 || ops[1].Op != "setTransition" || ops[1].Transition.Audio.Mode != SoundLead {
		b, _ := json.Marshal(ops)
		t.Fatalf("start fix: %s", b)
	}
	ops = append(ops, EdgeFlashFix(s, fl[1], f)...)
	next, _, err := ApplyOps(s, ops, false)
	if err != nil {
		t.Fatal(err)
	}
	Normalize(next)
	if left := EdgeFlashes(next, f); len(left) != 0 {
		t.Fatalf("flashes left after the fix: %+v", left)
	}
	// The trimmed clip's first frame shows the new picture exactly; the end's
	// last frame is before the switch on the frame grid.
	clips, _ := Timeline(next)
	if got := sampleAt(clips[1], 30, 0); got < 3.2-1e-9 {
		t.Fatalf("answer starts at %.4f, before the switch", got)
	}
	last := clips[3]
	if got := sampleAt(last, 30, last.DurationInFrames-1); got >= 20.0-1e-9 {
		t.Fatalf("reply's last frame shows %.4f, after the switch", got)
	}
	// The words "So when" (2.95–3.6) are still heard: the sound starts 0.1 s early.
	if a := next.Clips[1].Transition.Audio; a == nil || math.Abs(a.Duration-0.1) > 1e-9 {
		t.Fatalf("lead: %+v", next.Clips[1].Transition)
	}
}

func TestFrameGridHelpers(t *testing.T) {
	// 25 fps footage in a 30 fps cut: the switch at 3.04 starts on output frame 92 (3.0667).
	if got := StartAfter(30, 3.04); math.Abs(got-3.067) > 1e-9 {
		t.Fatalf("StartAfter = %v", got)
	}
	c := Clip{ID: "x", AssetID: astA, In: 1, Out: 9, Speed: 1}
	out := EndBefore(c, 30, 5.0, 0.021)
	pc := PlanClip{TrimBefore: 30, PlaybackRate: 1, DurationInFrames: int(math.Round((out - 1) * 30))}
	if last := sampleAt(pc, 30, pc.DurationInFrames-1); last > 5.0-0.021+1e-9 || last < 5.0-0.021-1.0/30 {
		t.Fatalf("EndBefore %v gives a last frame at %v", out, last)
	}
	// Played at double speed.
	c.Speed = 2
	out = EndBefore(c, 30, 5.0, 0.021)
	n := int(math.Round((out - 1) / 2 * 30))
	pc = PlanClip{TrimBefore: 30, PlaybackRate: 2, DurationInFrames: n}
	if last := sampleAt(pc, 30, n-1); last > 5.0-0.021+1e-9 {
		t.Fatalf("at double speed the last frame shows %v", last)
	}
}

func TestBuiltInNameBarFollowsWhoIsOnScreen(t *testing.T) {
	s := callSpec()
	s.Overlays = []Overlay{{ID: "who", Template: TplLowerThirdClean, At: 0.5, Duration: 3, Params: json.RawMessage(`{"name":"Lena O."}`)}}
	f := callTestFacts()
	nb, ok := BuildNameBar(s, f, NameBarOptions{FirstNames: true})
	if !ok {
		t.Fatal("no name bar")
	}
	if nb.Overlay.Span == nil || nb.Overlay.Template != TplNameBar || len(nb.Removed) != 1 || nb.Removed[0] != "who" {
		t.Fatalf("name bar plan: %+v", nb)
	}
	if strings.Join(nb.People, ",") != "Lena,Maya" {
		t.Fatalf("people: %v", nb.People)
	}
	s.Overlays = append(s.Overlays[:0], nb.Overlay)
	if issues := Validate(s, f, nil, false); HasErrors(issues) {
		t.Fatalf("the name bar doesn't validate: %+v", issues)
	}
	plan := Resolve(Clone(s), f, "", "", "")
	po := plan.Overlays[0]
	if po.From != 0 || po.DurationInFrames != plan.DurationInFrames || len(po.Segments) < 4 {
		t.Fatalf("resolved: %+v", po)
	}
	at := func(frame int) NameBarParams {
		var p NameBarParams
		for _, sg := range po.Segments {
			if sg.From <= frame {
				_ = json.Unmarshal(sg.Params, &p)
			}
		}
		return p
	}
	clips := plan.Clips
	if p := at(clips[0].From + 2); p.Name != "Lena" || p.Hide {
		t.Fatalf("open: %+v", p)
	}
	if p := at(clips[1].From + 1); p.Name != "Maya" {
		t.Fatalf("answer's first frames show 3.1 s (the flash): %+v", p)
	}
	// The name changes on the exact frame the picture switches back to Lena (3.2 s).
	k := firstFrameAtOrAfter(clips[1], 30, 3.2)
	if p := at(clips[1].From + k); p.Name != "Lena" {
		t.Fatalf("at the switch: %+v", p)
	}
	if p := at(clips[1].From + k - 1); p.Name != "Maya" {
		t.Fatalf("the frame before the switch: %+v", p)
	}
	if p := at(clips[2].From + 2); !p.Hide {
		t.Fatalf("over other footage the bar hides: %+v", p)
	}
	if p := at(clips[3].From + 2); p.Name != "Maya" || p.Hide {
		t.Fatalf("reply: %+v", p)
	}
	if p := at(clips[3].From + 2); p.Cover == nil || p.Cover.Y != 0.9 {
		t.Fatalf("cover: %+v", p.Cover)
	}
	// Nobody says another name than who is on screen.
	if mm := NameBarMismatches(s, plan, f, 0.5); len(mm) != 0 {
		t.Fatalf("mismatches: %+v", mm)
	}
	// A bar with a stale name is pointed out.
	for i, c := range s.Overlays[0].Changes {
		if c.At == 3.2 {
			var stale map[string]any
			_ = json.Unmarshal(c.Params, &stale)
			stale["name"] = "Somebody"
			b, _ := json.Marshal(stale)
			s.Overlays[0].Changes[i].Params = b
		}
	}
	plan = Resolve(Clone(s), f, "", "", "")
	if mm := NameBarMismatches(s, plan, f, 0.5); len(mm) == 0 {
		t.Fatal("a wrong name was not found")
	}
}
