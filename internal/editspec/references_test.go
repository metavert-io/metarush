package editspec

import (
	"strings"
	"testing"
)

// referenceAt is the reference_footage issue at a path, if any.
func referenceAt(is []Issue, path string) (Issue, bool) {
	for _, i := range is {
		if i.Code == "reference_footage" && i.Path == path {
			return i, true
		}
	}
	return Issue{}, false
}

func refusedAt(t *testing.T, what string, is []Issue, path, assetID string) {
	t.Helper()
	i, ok := referenceAt(is, path)
	switch {
	case !ok:
		t.Errorf("%s: no reference refused at %s: %+v", what, path, is)
	case i.Severity != "error":
		t.Errorf("%s: a warning, not a refusal: %+v", what, i)
	case i.Command != "metarush footage reference "+assetID+" --mine":
		t.Errorf("%s: the command behind Details: %q", what, i.Command)
	case !strings.Contains(i.Message, "someone else's video, kept to learn from") || !strings.Contains(i.Message, "This is mine"):
		t.Errorf("%s: the words: %q", what, i.Message)
	}
}

// A reference — someone else's video, kept to learn from — plays nowhere in a
// cut (D398, D440): not as a clip, the cut's sound, a conversation's recording
// or cutaway, a narration recording or a line's take, nor inside a nested cut.
func TestAReferencePlaysNowhereInACut(t *testing.T) {
	mark := func(as map[string]AssetFacts, id string) {
		a := as[id]
		a.Reference = true
		as[id] = a
	}
	// A clip.
	f := facts()
	mark(f.assets, astA)
	refusedAt(t, "a clip", Validate(baseSpec(), f, nil, false), "/clips/0/assetId", astA)

	// The cut's sound.
	f = facts()
	mark(f.assets, astB)
	s := baseSpec()
	s.Clips = s.Clips[:1]
	s.Audio = []AudioTrack{{ID: "bed", AssetID: astB, Role: RoleMusic, At: 0, In: 0, Out: 3}}
	Normalize(s)
	f.assets[astB] = AssetFacts{ID: astB, Name: "b.mp4", Kind: "video", State: "available", InProject: true, Duration: 10, HasVideo: true, HasAudio: true, Width: 1080, Height: 1920, FPS: 30, Reference: true}
	refusedAt(t, "the cut's sound", Validate(s, f, nil, false), "/audio/0/assetId", astB)

	// A conversation's recording (one of its members).
	cf, cs := convFixture()
	mark(cf.assets, astS)
	refusedAt(t, "a conversation's recording", Validate(cs, cf, nil, false), "/conversations/call/members/1/assetId", astS)

	// A cutaway in a conversation clip.
	cf, cs = convFixture()
	mark(cf.assets, astBR)
	cs.Clips[0].Shots = append(cs.Clips[0].Shots, Shot{At: 6, BRoll: &ShotBRoll{AssetID: astBR, In: 1}})
	Normalize(cs)
	refusedAt(t, "a cutaway", Validate(cs, cf, nil, false), "/clips/0/shots/3/broll/assetId", astBR)

	// Narration: one continuous recording, and a line's take.
	vf := voFacts()
	mark(vf.assets, astVO)
	vs := voSpec()
	vs.Voiceover.Layout = LayoutContinuous
	vs.Voiceover.Continuous = &ContinuousVO{AssetID: astVO, In: 0, Out: 12, Anchor: Anchor{Kind: AnchorClip, ClipID: "hook"}}
	for i := range vs.Voiceover.Beats {
		vs.Voiceover.Beats[i].Take = nil // the recording alone plays it
	}
	Normalize(vs)
	refusedAt(t, "the narration recording", Validate(vs, vf, nil, false), "/voiceover/continuous/assetId", astVO)
	vs = voSpec()
	Normalize(vs)
	refusedAt(t, "a line's take", Validate(vs, vf, nil, false), "/voiceover/beats/0/take/assetId", astVO)

	// Inside a nested cut, levels down: the outer clip is refused, naming what's inside.
	nf := refNestFacts{nestTestFacts: nestTestFacts{visible: true}, ref: "green"}
	outer := &Spec{SchemaVersion: 1, Output: Output{Width: 1280, Height: 720, FPS: 30, Platform: "youtube"},
		Clips: []Clip{{ID: "intro", AssetID: NestedKey(nestProject(3), nestRevision(3)), In: 0, Out: 2}}}
	Normalize(outer)
	is := Validate(Clone(outer), nf, nil, false)
	refusedAt(t, "a nested cut", is, "/clips/0/assetId", "green")
	if i, _ := referenceAt(is, "/clips/0/assetId"); !strings.Contains(i.Message, "plays the cut “Cut”, which has “green.mp4” in it") {
		t.Errorf("the nested cut's words: %q", i.Message)
	}
	// Without a reference, nothing is refused.
	if is := Validate(Clone(outer), nestTestFacts{visible: true}, nil, false); hasCode(is, "reference_footage") {
		t.Errorf("nothing marked, nothing refused: %+v", is)
	}
}

// Every place AssetIDs lists has its own path and words, so a refusal points at
// where the cut plays the footage.
func TestEveryPlaceAReferenceCanPlayHasAPath(t *testing.T) {
	_, cs := convFixture()
	cs.Clips[0].Shots = append(cs.Clips[0].Shots, Shot{At: 6, BRoll: &ShotBRoll{AssetID: astBR, In: 1}})
	cs.Audio = []AudioTrack{{ID: "bed", AssetID: astB, Role: RoleMusic}}
	vs := voSpec()
	cs.Voiceover = vs.Voiceover
	cs.Voiceover.Continuous = &ContinuousVO{AssetID: astA, In: 0, Out: 12, Processed: &ProcessedRef{AssetID: "ast_01jabcdefghjkmnpqrstvwxyp0"}}
	cs.Mix = &Mix{Enhanced: []EnhancedSound{{AssetID: astJ, Variant: "ast_01jabcdefghjkmnpqrstvwxye0"}},
		Said: []SaidSound{{Sound: "ast_01jabcdefghjkmnpqrstvwxyd0", Picture: "ast_01jabcdefghjkmnpqrstvwxyc0"}}}
	ids := AssetIDs(cs)
	if len(ids) < 9 {
		t.Fatalf("the spec plays %d things: %v", len(ids), ids)
	}
	for _, id := range ids {
		if path, who := wherePlays(cs, id); path == "/" || who == "This cut" {
			t.Errorf("%s plays somewhere wherePlays doesn't know", id)
		}
	}
}

// refNestFacts marks one piece of footage a reference, in nested cuts too.
type refNestFacts struct {
	nestTestFacts
	ref string
}

func (f refNestFacts) Asset(id string) (AssetFacts, bool) {
	a, ok := f.nestTestFacts.Asset(id)
	if ok && id == f.ref {
		a.Reference = true
	}
	return a, ok
}

func (f refNestFacts) Nested(projectID, revisionID string) (*NestedSource, bool) {
	src, ok := f.nestTestFacts.Nested(projectID, revisionID)
	if src != nil && src.Facts != nil {
		src.Facts = f
	}
	return src, ok
}
