package editspec

import (
	"encoding/json"
	"strings"
	"testing"
)

// nestCallFacts nests a cut of call footage (D182): the inner cut plays
// Zoom's speaker view (astA, with switches and printed names) and b-roll.
type nestCallFacts struct {
	callFacts
	inner *Spec
}

func (f nestCallFacts) Nested(projectID, revisionID string) (*NestedSource, bool) {
	return &NestedSource{ProjectID: projectID, RevisionID: revisionID, ProjectName: "Calls", Title: "Interview", Number: 1,
		Spec: f.inner, Facts: f.callFacts, Visible: true, LatestID: revisionID, LatestNumber: 1}, true
}

// innerCall is a cut of the speaker view: 0–1.9 Lena, 1.9–3.9 b-roll, then
// 3.9–9.1 Maya until the speaker view switches to Lena at 8.9.
func innerCall() *Spec {
	s := &Spec{SchemaVersion: 1, Output: Output{Preset: PresetLandscape}, Clips: []Clip{
		{ID: "a", AssetID: astA, In: 1, Out: 2.9},
		{ID: "b", AssetID: astB, In: 0, Out: 2},
		{ID: "c", AssetID: astA, In: 15, Out: 20.2},
	}}
	Normalize(s)
	return s
}

func TestNestedCutsGetCallCleanUp(t *testing.T) {
	inner := innerCall()
	f := nestCallFacts{callFacts: callTestFacts(), inner: inner}
	key := NestedKey(nestProject(1), nestRevision(1))
	outer := &Spec{SchemaVersion: 1, Output: inner.Output, Clips: []Clip{{ID: "ep", AssetID: key, In: 1.85, Out: 8.5}}}
	Normalize(outer)

	// The nested cut's switches: its hard cuts and the speaker view's own.
	fs, ok := clipFootage(outer, 0, wrapFacts(outer, f))
	if !ok || len(fs.Switches) != 3 || fs.Switches[0].At != 1.9 || fs.Switches[1].At != 3.9 || fs.Switches[2].At != 8.9 || fs.Names == nil {
		t.Fatalf("nested switches: %+v", fs)
	}
	// Starting 0.05 s before the inner cut shows a sliver of Lena: a flash.
	fl := EdgeFlashes(outer, f)
	if len(fl) != 1 || fl[0].Edge != "start" || fl[0].Switch.At != 1.9 || fl[0].Flash != "Lena" {
		t.Fatalf("nested flash: %+v", fl)
	}
	if ops := EdgeFlashFix(outer, fl[0], f); len(ops) == 0 || ops[0].Op != "trimClip" || *ops[0].To != 1.9 {
		t.Fatalf("the fix trims to the inner cut: %+v", ops)
	}
	// The name bar covers the inner speaker view's names, hidden over its b-roll.
	nb, ok := BuildNameBar(outer, f, NameBarOptions{})
	if !ok || nb.Clips != 1 {
		t.Fatalf("name bar over a nested cut: %+v", nb)
	}
	var shows, hides int
	for _, ch := range nb.Overlay.Changes {
		if ch.Footage != key {
			t.Fatalf("a change keyed by something else than the nested cut: %+v", ch)
		}
		var p map[string]any
		_ = json.Unmarshal(ch.Params, &p)
		if p["hide"] == true {
			hides++
		} else {
			shows++
		}
	}
	if shows == 0 || hides == 0 {
		t.Fatalf("shown over the call, hidden over the b-roll: %+v", nb.Overlay.Changes)
	}
	outer.Overlays = []Overlay{nb.Overlay}
	if is := Validate(outer, f, nil, false); HasErrors(is) {
		t.Fatalf("the bar over a nested cut validates: %+v", is)
	}
	if mm := NameBarMismatches(outer, Resolve(Clone(outer), f, "", "", ""), f, 0.5); len(mm) != 0 {
		t.Fatalf("the bar names who is on screen: %+v", mm)
	}
	// A nested cut with a name bar of its own keeps it; the outer bar stays out.
	innerBar, ok := BuildNameBar(inner, f.callFacts, NameBarOptions{})
	if !ok {
		t.Fatal("no inner bar")
	}
	withBar := innerCall()
	withBar.Overlays = []Overlay{innerBar.Overlay}
	f2 := nestCallFacts{callFacts: f.callFacts, inner: withBar}
	outer.Overlays = nil
	if _, ok := BuildNameBar(outer, f2, NameBarOptions{}); ok {
		t.Fatal("an outer bar over a nested cut that has its own")
	}
	// Shown at another shape, the inner bar covers the names where its crops put them.
	square := &Spec{SchemaVersion: 1, Output: Output{Preset: PresetSquare}, Clips: []Clip{{ID: "ep", AssetID: key, In: 0, Out: 9}}}
	Normalize(square)
	plan := Resolve(Clone(square), f2, "", "", "")
	np, ok := plan.Nested[key]
	if !ok || np.Plan == nil {
		t.Fatalf("nested plan: %+v", plan.Nested)
	}
	var before, after map[string]any
	_ = json.Unmarshal(innerBar.Overlay.Params, &before)
	for _, o := range np.Plan.Overlays {
		if o.Template == TplNameBar {
			_ = json.Unmarshal(o.Params, &after)
		}
	}
	if after == nil || jsonText(before["cover"]) == jsonText(after["cover"]) {
		t.Fatalf("the inner bar's cover follows the square shape: before %v after %v", before["cover"], after["cover"])
	}
}

func jsonText(v any) string { b, _ := json.Marshal(v); return string(b) }

// convCallFacts gives Maya's recording the call app's printed name.
type convCallFacts struct {
	convFakeFacts
	fs map[string]*FootageShots
}

func (f convCallFacts) Footage(id string) (*FootageShots, bool) { s, ok := f.fs[id]; return s, ok }

func TestConversationClipsGetCallCleanUp(t *testing.T) {
	cf, s := convFixture()
	names := &PrintedNames{Corner: "bottom-left", Box: CoverBox{X: 0.02, Y: 0.88, W: 0.25, H: 0.07}, Shots: 1}
	f := convCallFacts{convFakeFacts: cf, fs: map[string]*FootageShots{astJ: {Switches: []PictureSwitch{}, Shots: []FootageShot{{From: 0, To: 12, Person: "Maya", By: "printed", Printed: "Maya M."}}, Names: names}}}

	// Its shots switch on the conversation's clock: a clip that starts 0.05 s
	// before Sam's shot shows a sliver of Maya.
	s.Clips[0].In = 3.25
	fl := EdgeFlashes(s, f)
	if len(fl) != 1 || fl[0].Edge != "start" || fl[0].Switch.At != 3.3 || fl[0].Flash != "Maya" || fl[0].Main != "Sam" {
		t.Fatalf("conversation flash: %+v", fl)
	}
	if ops := EdgeFlashFix(s, fl[0], f); len(ops) == 0 || *ops[0].To != 3.3 {
		t.Fatalf("the fix trims to Sam's shot: %+v", ops)
	}

	// The bar covers Maya's printed name while she is shown alone and hides
	// over Sam (no printed name) and the side-by-side (each tile's tag covers its own).
	s.Clips[0].In = 0
	nb, ok := BuildNameBar(s, f, NameBarOptions{FirstNames: true})
	if !ok || nb.Clips != 1 || strings.Join(nb.People, ",") != "Maya" {
		t.Fatalf("conversation bar: %+v", nb)
	}
	byAt := map[float64]map[string]any{}
	for _, ch := range nb.Overlay.Changes {
		var p map[string]any
		_ = json.Unmarshal(ch.Params, &p)
		byAt[ch.At] = p
		if ch.Footage != ConvKey("call") {
			t.Fatalf("keyed by the conversation: %+v", ch)
		}
	}
	if p := byAt[0]; p["hide"] != false || p["name"] != "Maya" {
		t.Fatalf("Maya alone: %+v", byAt)
	}
	if p5, ok := byAt[5]; byAt[3.3]["hide"] != true || (ok && p5["hide"] != true) {
		t.Fatalf("hidden over Sam and the layout (a repeat of the same fields is dropped): %+v", byAt)
	}
	var base map[string]any
	_ = json.Unmarshal(nb.Overlay.Params, &base)
	if base["cover"] == nil || base["firstNames"] != true {
		t.Fatalf("the bar's cover is Maya's name in her tile: %+v", base)
	}
	s.Overlays = []Overlay{nb.Overlay}
	if is := Validate(s, f, nil, false); HasErrors(is) {
		t.Fatalf("validates: %+v", is)
	}
	plan := Resolve(Clone(s), f, "", "", "")
	var layout *PlanShot
	for i, ps := range plan.Clips[0].Conversation.Shots {
		if ps.Kind == "layout" {
			layout = &plan.Clips[0].Conversation.Shots[i]
		}
	}
	if layout == nil || len(layout.Tiles) != 2 {
		t.Fatalf("layout shot: %+v", plan.Clips[0].Conversation)
	}
	for _, tl := range layout.Tiles {
		switch tl.Member {
		case "maya":
			if tl.Tag == nil || tl.Tag.Cover == nil || tl.Tag.Name != "Maya" || tl.Tag.Cover.Y < tl.Rect.H/2 {
				t.Fatalf("Maya's tile tag covers her printed name (bottom of her tile): %+v", tl.Tag)
			}
		case "sam":
			if tl.Tag != nil && tl.Tag.Cover != nil {
				t.Fatalf("Sam's tile has no printed name to cover: %+v", tl.Tag)
			}
		}
	}
	if mm := NameBarMismatches(s, plan, f, 0.5); len(mm) != 0 {
		t.Fatalf("mismatches: %+v", mm)
	}
	if n, corner := PrintedNameClips(s, f); n != 1 || corner != "bottom-left" {
		t.Fatalf("printed-name clips: %d %s", n, corner)
	}
}
