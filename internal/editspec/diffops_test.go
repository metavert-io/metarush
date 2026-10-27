package editspec_test

import (
	"encoding/json"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/editspec/specgen"
)

// DiffOps must reproduce any edit exactly, for random specs and random edits.
func TestDiffOpsReproducesEdits(t *testing.T) {
	for seed := int64(1); seed <= 600; seed++ {
		g := specgen.New(seed)
		base := g.Spec()
		next := g.Mutate(base)
		ops, err := editspec.DiffOps(base, next)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		got, _, err := editspec.ApplyOps(base, ops, true)
		if err != nil {
			t.Fatalf("seed %d: apply: %v", seed, err)
		}
		editspec.Normalize(got)
		if editspec.Hash(got) != editspec.Hash(next) {
			t.Fatalf("seed %d: ops did not reproduce the edit", seed)
		}
		if editspec.Hash(base) == editspec.Hash(next) && len(ops) != 0 {
			t.Fatalf("seed %d: no change but %d ops", seed, len(ops))
		}
	}
}

// A trim written against an old head rebases onto a head that changed another
// clip; the same trim conflicts when the head changed that clip too.
func TestDiffOpsRebase(t *testing.T) {
	g := specgen.New(7)
	g.Plain = true
	base := g.Spec()
	for len(base.Clips) < 3 {
		base = g.Spec()
	}
	for i := range base.Clips {
		base.Clips[i].Locked = false
	}
	mine := editspec.Clone(base)
	mine.Clips[0].Out = mine.Clips[0].In + 1.25
	ops, err := editspec.DiffOps(base, mine)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Op != "updateClip" || ops[0].ID != base.Clips[0].ID {
		t.Fatalf("expected one updateClip, got %+v", ops)
	}
	theirs := editspec.Clone(base)
	theirs.Clips[2].Out = theirs.Clips[2].In + 0.75
	rebased, conflicts, err := editspec.Rebase(base, theirs, ops, false)
	if err != nil || len(conflicts) != 0 {
		t.Fatalf("disjoint edit should rebase: %v %v", err, conflicts)
	}
	if rebased.Clips[0].Out != mine.Clips[0].Out || rebased.Clips[2].Out != theirs.Clips[2].Out {
		t.Fatal("rebased cut lost one of the edits")
	}
	theirs.Clips[0].In += 0.1
	if _, conflicts, _ := editspec.Rebase(base, theirs, ops, false); len(conflicts) != 1 {
		t.Fatalf("overlapping edit should conflict, got %v", conflicts)
	}
}

// Reordering prefers moving unlocked clips so locked ones keep their place.
func TestDiffOpsMovesUnlockedClips(t *testing.T) {
	s := &editspec.Spec{Output: editspec.Output{Preset: editspec.PresetVertical}, Clips: []editspec.Clip{
		{ID: "a", AssetID: specgen.Assets[0], In: 0, Out: 1, Locked: true},
		{ID: "b", AssetID: specgen.Assets[0], In: 1, Out: 2},
	}}
	editspec.Normalize(s)
	next := editspec.Clone(s)
	next.Clips[0], next.Clips[1] = next.Clips[1], next.Clips[0]
	ops, err := editspec.DiffOps(s, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Op != "moveClip" || ops[0].ID != "b" {
		t.Fatalf("expected b to move, got %+v", ops)
	}
	if _, _, err := editspec.ApplyOps(s, ops, false); err != nil {
		t.Fatalf("moving the unlocked clip must not need an unlock: %v", err)
	}
}

// A nested partial update keeps the other nested fields (merge patch), and
// lists are replaced whole without leftovers from the old entries.
func TestUpdateMergesNestedObjects(t *testing.T) {
	x, from := 0.2, 3.0
	s := &editspec.Spec{Output: editspec.Output{Preset: editspec.PresetVertical}, Clips: []editspec.Clip{{ID: "a", AssetID: specgen.Assets[0], In: 1, Out: 2,
		Framing:  &editspec.Framing{Mode: "fit", FocusX: &x},
		Evidence: []editspec.Evidence{{Kind: "frame", From: &from, Note: "old"}}}}}
	editspec.Normalize(s)
	next, _, err := editspec.ApplyOps(s, []editspec.Op{{Op: "updateClip", ID: "a", Set: json.RawMessage(`{"framing":{"focusY":0.4},"evidence":[{"kind":"user"}]}`)}}, false)
	if err != nil {
		t.Fatal(err)
	}
	c := next.Clips[0]
	if c.Framing.Mode != "fit" || c.Framing.FocusX == nil || *c.Framing.FocusY != 0.4 {
		t.Fatalf("nested merge lost fields: %+v", c.Framing)
	}
	if len(c.Evidence) != 1 || c.Evidence[0].From != nil || c.Evidence[0].Note != "" {
		t.Fatalf("list replace kept stale data: %+v", c.Evidence)
	}
}

// null in an update resets a field (it used to be silently ignored).
func TestUpdateNullResetsField(t *testing.T) {
	v := 0.8
	s := &editspec.Spec{Output: editspec.Output{Preset: editspec.PresetVertical}, Clips: []editspec.Clip{{ID: "a", AssetID: specgen.Assets[0], In: 1, Out: 2, Volume: &v, Muted: true, Label: "x"}}}
	editspec.Normalize(s)
	next, _, err := editspec.ApplyOps(s, []editspec.Op{{Op: "updateClip", ID: "a", Set: json.RawMessage(`{"volume":null,"muted":null,"label":null}`)}}, false)
	if err != nil {
		t.Fatal(err)
	}
	c := next.Clips[0]
	if c.Volume != nil || c.Muted || c.Label != "" {
		t.Fatalf("null did not reset: %+v", c)
	}
}

// Narration changes become line-level ops (so a line edit rebases past someone
// else's change to another line), settings become updateVoiceover, and the mix
// is one setMix.
func TestDiffOpsNarrationAndMix(t *testing.T) {
	at := func(v float64) *float64 { return &v }
	s := &editspec.Spec{Output: editspec.Output{Preset: editspec.PresetVertical}, Clips: []editspec.Clip{
		{ID: "hook", AssetID: specgen.Assets[0], In: 0, Out: 4}, {ID: "broll", AssetID: specgen.Assets[1], In: 0, Out: 3},
	}, Voiceover: &editspec.Voiceover{Beats: []editspec.Beat{
		{ID: "one", Text: "When I started.", Anchor: editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "hook"}},
		{ID: "two", Text: "I had no idea.", Anchor: editspec.Anchor{Kind: editspec.AnchorAfter}},
		{ID: "three", Text: "So we built it twice.", Anchor: editspec.Anchor{Kind: editspec.AnchorTime, At: at(5)}},
	}}}
	editspec.Normalize(s)
	next := editspec.Clone(s)
	v := next.Voiceover
	v.Beats[1].Text = "I had absolutely no idea."
	v.Beats[0], v.Beats[2] = v.Beats[2], v.Beats[0] // three, two, one
	v.Beats = append(v.Beats, editspec.Beat{ID: "four", Text: "Then a question.", Anchor: editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "broll", Edge: "end"}})
	v.Settings = &editspec.VoiceoverSettings{FitPolicy: editspec.FitAsk}
	next.Mix = &editspec.Mix{MuteSource: true, Music: &editspec.Bus{GainDB: at(-6)}}
	ops, err := editspec.DiffOps(s, next)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, op := range ops {
		kinds[op.Op]++
	}
	if kinds["updateBeat"] != 1 || kinds["addBeat"] != 1 || kinds["moveBeat"] == 0 || kinds["updateVoiceover"] != 1 || kinds["setMix"] != 1 || kinds["setScript"] != 0 {
		t.Fatalf("unexpected ops: %+v", ops)
	}
	// Someone else rewords line one meanwhile: my edit to line two still rebases.
	mine := editspec.Clone(s)
	mine.Voiceover.Beats[1].Text = "I had absolutely no idea."
	myOps, _ := editspec.DiffOps(s, mine)
	theirs := editspec.Clone(s)
	theirs.Voiceover.Beats[0].Text = "When I first started."
	if _, conflicts, err := editspec.Rebase(s, theirs, myOps, false); err != nil || len(conflicts) != 0 {
		t.Fatalf("edits to different lines should rebase: %v %v", err, conflicts)
	}
	// Locking the narration is a lock op; removing it is setScript with no script.
	locked := editspec.Clone(s)
	locked.Voiceover.Locked = true
	if ops, _ := editspec.DiffOps(s, locked); len(ops) != 1 || ops[0].Op != "lock" || ops[0].ID != "voiceover" {
		t.Fatalf("lock: %+v", ops)
	}
	none := editspec.Clone(s)
	none.Voiceover = nil
	if ops, _ := editspec.DiffOps(s, none); len(ops) != 1 || ops[0].Op != "setScript" || ops[0].Voiceover != nil {
		t.Fatalf("remove: %+v", ops)
	}
}
