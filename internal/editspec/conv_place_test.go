package editspec

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// Conversation placement (D119). Cuts made before sample-aligned placement
// place each member's sound and picture on a frame of the cut (up to one frame
// late); their resolved plans are frozen in testdata/conversation-plans.json,
// captured from the engine before the change, and must never move.
//
//	go test ./internal/editspec -run TestConversationPlansResolveIdentically -update

const astSamCopy = "ast_01jabcdefghjkmnpqrstvwxyc0"

// convPlacementFacts is convFixture's call plus a gallery view and a clearer
// copy of Sam's recording.
func convPlacementFacts() convFakeFacts {
	f, _ := convFixture()
	f.assets[astG] = AssetFacts{ID: astG, Name: "gallery.mp4", Kind: "video", State: "available", InProject: true, Duration: 12, HasVideo: true, HasAudio: true, Width: 1280, Height: 720, FPS: 30}
	f.assets[astSamCopy] = AssetFacts{ID: astSamCopy, Name: "sam-clearer.wav", Kind: "audio", State: "available", InProject: true, Duration: 9, HasAudio: true}
	return f
}

// convPlacementSpecs are conversation cuts as they were written before
// sample-aligned placement existed (their pins have no sampleAligned).
func convPlacementSpecs() map[string]*Spec {
	base := func() *Spec { _, s := convFixture(); return s }
	member := func(s *Spec, i int, start, drift float64) {
		p := s.Conversations["call"]
		p.Members[i].Start, p.Members[i].Drift = start, drift
		s.Conversations["call"] = p
	}
	out := map[string]*Spec{}

	// Sam starts 1.229 s into the call on a clock 208.3 ppm fast; the picture
	// switches between them, a layout and picture in picture.
	a := base()
	member(a, 1, 1.229, 208.3)
	a.Clips[0].Shots = append(a.Clips[0].Shots, Shot{At: 6.5, Layout: LayoutPiP, Members: []string{"sam", "maya"}})
	out["sub-frame-starts"] = a

	// Maya's recording started before the call's clock, Sam's starts after
	// the clip does (his clock slow); 25 frames a second; the clip from 0.5 s.
	b := base()
	b.Output.FPS = 25
	member(b, 0, -0.733, 0)
	member(b, 1, 1.017, -150)
	b.Clips[0].In, b.Clips[0].Out = 0.5, 9
	b.Clips[0].Shots[0].At = 0.5
	out["early-and-late-25fps"] = b

	// Two parts of the call with a cut between them; the first leaves
	// Maya's microphone out.
	c := base()
	member(c, 1, 0.987, 40)
	c.Clips = []Clip{
		{ID: "talk", AssetID: ConvKey("call"), In: 1.37, Out: 4.2, MutedMembers: []string{"maya"},
			Shots: []Shot{{At: 1.37, Member: "maya"}, {At: 3.3, Member: "sam"}}},
		{ID: "talk-2", AssetID: ConvKey("call"), In: 4.9, Out: 7.77, Shots: []Shot{{At: 4.9, Layout: LayoutSideBySide}},
			Transition: &Transition{Type: TransitionCut, Audio: &SoundTransition{Mode: SoundAuto}}},
	}
	out["two-parts-muted"] = c

	// Sam's clearer voice plays from a processed copy of his recording.
	d := base()
	member(d, 1, 1.229, 0)
	d.Mix = &Mix{Voices: []VoiceSound{{Name: "Sam", Enhance: &Enhance{Mode: EnhanceClean}}},
		Enhanced: []EnhancedSound{{AssetID: astS, Start: 0, Variant: astSamCopy, Settings: "Sam: clean 100%"}}}
	out["clearer-copy"] = d

	// A shared gallery view (picture only) next to the two people.
	e := base()
	member(e, 1, 1.2345, 12.5)
	p := e.Conversations["call"]
	p.Members = append(p.Members, ConversationMember{ID: "gallery", Name: "Gallery view", AssetID: astG, Use: UsePicture, Start: 0.2})
	e.Conversations["call"] = p
	e.Clips[0].Shots = []Shot{{At: 0, Member: "gallery"}, {At: 2.2, Member: "sam"}}
	out["gallery-view"] = e

	for _, s := range out {
		Normalize(s)
	}
	return out
}

func TestConversationPlansResolveIdentically(t *testing.T) {
	path := filepath.Join("testdata", "conversation-plans.json")
	f := convPlacementFacts()
	got := map[string]legacyGolden{}
	for name, s := range convPlacementSpecs() {
		h := Hash(s)
		plan := Resolve(Clone(s), f, "rev_legacy", "prj_legacy", h)
		b, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		got[name] = legacyGolden{Hash: h, Plan: b}
	}
	if *updateGolden {
		b, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s missing: %v", path, err)
	}
	var want map[string]legacyGolden
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(got) {
		t.Fatalf("golden has %d specs, test builds %d", len(want), len(got))
	}
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("%s: no longer built", name)
			continue
		}
		if g.Hash != w.Hash {
			t.Errorf("%s: spec hash changed: %s → %s (old revisions must keep their hash)", name, w.Hash, g.Hash)
		}
		var a, b any
		_ = json.Unmarshal(w.Plan, &a)
		_ = json.Unmarshal(g.Plan, &b)
		if !equalJSON(a, b) {
			t.Errorf("%s: resolved plan changed for an old conversation cut\n want %s\n  got %s", name, w.Plan, g.Plan)
		}
	}
}

// memberErrors measures a resolved plan's placement: for every member's sound
// element and picture tile, how far the recording plays from where the pin
// puts it at the element's (or shot's) first frame, in seconds (positive:
// late, what should be heard then comes later). It returns the largest error
// per member.
func memberErrors(t *testing.T, s *Spec, plan *RenderPlan) map[string]float64 {
	t.Helper()
	fps := float64(plan.FPS)
	worst := map[string]float64{}
	note := func(who string, e float64) {
		if w, ok := worst[who]; !ok || math.Abs(e) > math.Abs(w) {
			worst[who] = e
		}
	}
	clipOf := func(id string) (PlanClip, Clip) {
		for i, pc := range plan.Clips {
			if pc.ID == id {
				return pc, s.Clips[i]
			}
		}
		t.Fatalf("no clip %s", id)
		return PlanClip{}, Clip{}
	}
	for _, a := range plan.Audio {
		if a.Conversation == "" {
			continue
		}
		pc, c := clipOf(a.Conversation)
		pin := s.Conversations[ConvName(c.AssetID)]
		m, _ := pin.Member(a.Member)
		clock := pc.SourceIn + float64(a.From-pc.From)/fps // the conversation's clock at the element's first frame
		plays := ExactTrim(a.TrimBefore, a.TrimFraction) / fps
		note(a.Member, clock-m.ClockAt(plays))
		if v := a.Variant; v != nil {
			var start float64
			for _, e := range s.Mix.Enhanced {
				if e.Variant == v.AssetID {
					start = e.Start
				}
			}
			if d := (ExactTrim(v.TrimBefore, v.TrimFraction)/fps + start) - plays; math.Abs(d) > 1e-9 {
				t.Errorf("%s: the clearer copy plays %.6f s away from the recording", a.ID, d)
			}
		}
	}
	for i, pc := range plan.Clips {
		if pc.Conversation == nil {
			continue
		}
		pin := s.Conversations[ConvName(s.Clips[i].AssetID)]
		for _, sh := range pc.Conversation.Shots {
			for _, tl := range sh.Tiles {
				m, ok := pin.Member(tl.Member)
				if !ok {
					continue
				}
				clock := pc.SourceIn + float64(sh.From+tl.Delay)/fps
				note(tl.Member+" (picture)", clock-m.ClockAt(ExactTrim(tl.TrimBefore, tl.TrimFraction)/fps))
			}
		}
	}
	return worst
}

// The old cuts with sampleAligned on: every recording plays exactly where its
// pin says, sound and picture alike (the nearest frame, trimBefore, is what
// it was), and nothing else in the plan changes. The log is the before/after
// measurement docs/conversations.md quotes.
func TestConversationSampleAligned(t *testing.T) {
	f := convPlacementFacts()
	raw, err := os.ReadFile(filepath.Join("testdata", "conversation-plans.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]legacyGolden
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	specs := convPlacementSpecs()
	names := make([]string, 0, len(specs))
	for n := range specs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		old := specs[name]
		var oldPlan RenderPlan
		if err := json.Unmarshal(golden[name].Plan, &oldPlan); err != nil {
			t.Fatal(err)
		}
		s := Clone(old)
		for k, p := range s.Conversations {
			p.SampleAligned = true
			s.Conversations[k] = p
		}
		plan := Resolve(Clone(s), f, "rev_legacy", "prj_legacy", golden[name].Hash)
		before, after := memberErrors(t, old, &oldPlan), memberErrors(t, s, plan)
		whos := make([]string, 0, len(after))
		for who := range after {
			whos = append(whos, who)
		}
		sort.Strings(whos)
		for _, who := range whos {
			e := after[who]
			if math.Abs(e) > 1e-7 {
				t.Errorf("%s: %s plays %.3f µs from where the pin puts it", name, who, e*1e6)
			}
			if b := before[who]; math.Abs(b) > 1/float64(plan.FPS)+1e-9 {
				t.Errorf("%s: before, %s was off by more than a frame (%.2f ms)", name, who, b*1e3)
			}
			t.Logf("%-22s %-20s frame-aligned %+7.3f ms   sample-aligned %+.4f µs", name, who, before[who]*1e3, e*1e6)
		}
		// Only the fractions are new: without them the plan is the old one.
		b, _ := json.Marshal(plan)
		var got any
		_ = json.Unmarshal(b, &got)
		stripFractions(got)
		var want any
		_ = json.Unmarshal(golden[name].Plan, &want)
		if !equalJSON(want, got) {
			t.Errorf("%s: placing to the sample changed more than the fractions\n want %s\n  got %s", name, golden[name].Plan, b)
		}
	}
}

func stripFractions(v any) {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "trimFraction")
		for _, e := range x {
			stripFractions(e)
		}
	case []any:
		for _, e := range x {
			stripFractions(e)
		}
	}
}

// Two people's tracks line up with each other to the sample at every frame,
// not only at their first: each one's place in its recording, mapped back to
// the conversation's clock, is the same moment.
func TestConversationTracksLineUp(t *testing.T) {
	f := convPlacementFacts()
	s := convPlacementSpecs()["sub-frame-starts"]
	p := s.Conversations["call"]
	p.SampleAligned = true
	s.Conversations["call"] = p
	plan := Resolve(Clone(s), f, "rev_x", "prj_x", Hash(s))
	fps := float64(plan.FPS)
	pc := plan.Clips[0]
	for frame := 40; frame < pc.DurationInFrames; frame += 37 {
		var clocks []float64
		for _, a := range plan.Audio {
			if a.Conversation == "" || frame < a.From || frame >= a.From+a.DurationInFrames {
				continue
			}
			m, _ := p.Member(a.Member)
			tau := ExactTrim(a.TrimBefore, a.TrimFraction)/fps + float64(frame-a.From)/fps*a.PlaybackRate
			clocks = append(clocks, m.ClockAt(tau))
		}
		if len(clocks) != 2 {
			t.Fatalf("frame %d: %d tracks play", frame, len(clocks))
		}
		if d := math.Abs(clocks[0] - clocks[1]); d > 1/48000.0 {
			t.Fatalf("frame %d: the tracks are %.1f µs apart (more than a sample at 48 kHz)", frame, d*1e6)
		}
	}
}

// convNestFacts nests the conversation cut ("the episode") in another cut, as
// a short made with --nest plays it.
type convNestFacts struct {
	convFakeFacts
	inner *Spec
}

func (f convNestFacts) Nested(projectID, revisionID string) (*NestedSource, bool) {
	if projectID != nestProject(1) || revisionID != nestRevision(1) {
		return &NestedSource{ProjectID: projectID, RevisionID: revisionID, Missing: true}, true
	}
	return &NestedSource{ProjectID: projectID, RevisionID: revisionID, ProjectName: "Episode", Title: "Cut", Number: 1,
		Spec: f.inner, Facts: f.convFakeFacts, Visible: true, LatestID: revisionID, LatestNumber: 1}, true
}

// A cut that nests a sample-aligned conversation keeps every recording exact
// (the nested cut's sound is flattened into the outer mix); one that nests an
// older conversation plays it as it always did.
func TestNestedConversationKeepsTheSample(t *testing.T) {
	for _, exact := range []bool{false, true} {
		inner := convPlacementSpecs()["sub-frame-starts"]
		p := inner.Conversations["call"]
		p.SampleAligned = exact
		inner.Conversations["call"] = p
		f := convNestFacts{convFakeFacts: convPlacementFacts(), inner: inner}
		outer := &Spec{Output: inner.Output, Clips: []Clip{{ID: "ep", AssetID: NestedKey(nestProject(1), nestRevision(1)), In: 1.1, Out: 6}}}
		Normalize(outer)
		plan := Resolve(Clone(outer), f, "rev_y", "prj_y", Hash(outer))
		fps := float64(plan.FPS)
		innerPlan := Resolve(Clone(inner), f.convFakeFacts, "", "", "")
		seen := 0
		for _, a := range plan.Audio {
			if a.Nested != "ep" {
				continue
			}
			var src *PlanAudio
			for i := range innerPlan.Audio {
				if "ep/"+innerPlan.Audio[i].ID == a.ID {
					src = &innerPlan.Audio[i]
				}
			}
			if src == nil || src.Member == "" {
				continue
			}
			seen++
			m, _ := p.Member(src.Member)
			innerFrame := a.From - plan.Clips[0].From + plan.Clips[0].TrimBefore // the episode's frame playing at the element's start
			want := m.SourceAt(float64(innerFrame) / fps)
			got := ExactTrim(a.TrimBefore, a.TrimFraction) / fps
			switch {
			case exact && (a.TrimFraction == nil || math.Abs(got-want) > 1e-7):
				t.Errorf("%s: plays %.3f µs from the pin inside the short", a.ID, (got-want)*1e6)
			case !exact && a.TrimFraction != nil:
				t.Errorf("%s: an older conversation got a fraction", a.ID)
			case !exact && a.TrimBefore != src.TrimBefore+int(math.Round(float64(innerFrame-src.From)*src.PlaybackRate)):
				t.Errorf("%s: an older conversation's nested sound moved", a.ID)
			}
		}
		if seen != 2 {
			t.Fatalf("exact %v: %d nested member sounds", exact, seen)
		}
	}
}
