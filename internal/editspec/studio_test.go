package editspec

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func fp(v float64) *float64 { return &v }

// studioSpec: three clips of 4 s, 3 s and 4 s at 30 fps (11 s), the last with a fade-out.
func studioSpec() *Spec {
	s := &Spec{Output: Output{Preset: PresetLandscape, FPS: 30}, Clips: []Clip{
		{ID: "hook", AssetID: astA, In: 1, Out: 5, Label: "Hook", Notes: "keep the laugh"},
		{ID: "broll", AssetID: astB, In: 0, Out: 3},
		{ID: "end", AssetID: astA, In: 10, Out: 14, FadeOut: 1},
	}}
	Normalize(s)
	return s
}

func clipIDs(s *Spec) string {
	var ids []string
	for _, c := range s.Clips {
		ids = append(ids, c.ID)
	}
	return strings.Join(ids, ",")
}

func clipOf(s *Spec, id string) *Clip {
	for i := range s.Clips {
		if s.Clips[i].ID == id {
			return &s.Clips[i]
		}
	}
	return nil
}

func length(s *Spec) float64 {
	_, total := Timeline(s)
	return float64(total) / float64(s.Output.FPS)
}

func TestSplitClip(t *testing.T) {
	s := studioSpec()
	next, touched, err := ApplyOps(s, []Op{{Op: "splitClip", ID: "end", At: fp(12.5)}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if clipIDs(next) != "hook,broll,end,end-2" {
		t.Fatalf("clips %s", clipIDs(next))
	}
	l, r := clipOf(next, "end"), clipOf(next, "end-2")
	if l.Out != 12.5 || l.FadeOut != 0 || r.In != 12.5 || r.Out != 14 || r.FadeOut != 1 || r.Transition != nil {
		t.Fatalf("parts %+v / %+v", l, r)
	}
	if length(next) != length(s) {
		t.Fatalf("a split changes nothing you see: %v vs %v", length(next), length(s))
	}
	if !touched["end"] || !touched["end-2"] || len(touched) != 2 {
		t.Fatalf("touched %v", touched)
	}
	// The second part keeps the label but not the person's notes; a given name is used.
	named, _, err := ApplyOps(s, []Op{{Op: "splitClip", ID: "hook", At: fp(3), NewID: "hook-b"}}, false)
	if err != nil || clipOf(named, "hook-b").Label != "Hook" || clipOf(named, "hook-b").Notes != "" || clipOf(named, "hook").Notes != "keep the laugh" {
		t.Fatalf("named split: %v %+v", err, named.Clips)
	}
	// Splitting a part again numbers on: end-2 → end-3.
	again, _, err := ApplyOps(next, []Op{{Op: "splitClip", ID: "end-2", At: fp(13)}}, false)
	if err != nil || clipIDs(again) != "hook,broll,end,end-2,end-3" {
		t.Fatalf("split again: %v %s", err, clipIDs(again))
	}
	for _, bad := range []float64{10, 14, 9, 20} {
		if _, _, err := ApplyOps(s, []Op{{Op: "splitClip", ID: "end", At: fp(bad)}}, false); err == nil {
			t.Fatalf("split at %v should fail", bad)
		}
	}
	if _, _, err := ApplyOps(s, []Op{{Op: "splitClip", ID: "end", At: fp(12), NewID: "hook"}}, false); err == nil {
		t.Fatal("a used name is refused")
	}
	locked, _, _ := ApplyOps(s, []Op{{Op: "lock", ID: "end"}}, false)
	var le *LockedError
	if _, _, err := ApplyOps(locked, []Op{{Op: "splitClip", ID: "end", At: fp(12)}}, false); !errors.As(err, &le) {
		t.Fatalf("locked: %v", err)
	}
}

func TestTrimClipRippleAndRoll(t *testing.T) {
	s := studioSpec()
	rip, touched, err := ApplyOps(s, []Op{{Op: "trimClip", ID: "hook", Edge: "end", To: fp(4)}}, false)
	if err != nil || clipOf(rip, "hook").Out != 4 || math.Abs(length(rip)-(length(s)-1)) > 1e-9 || len(touched) != 1 {
		t.Fatalf("ripple end: %v %v %v", err, length(rip), touched)
	}
	rip, _, err = ApplyOps(s, []Op{{Op: "trimClip", ID: "broll", Edge: "in", To: fp(0.5)}}, false)
	if err != nil || clipOf(rip, "broll").In != 0.5 || math.Abs(length(rip)-(length(s)-0.5)) > 1e-9 {
		t.Fatalf("ripple in: %v %v", err, length(rip))
	}
	// Roll the cut between hook and broll 0.5 s later: hook gets longer, broll starts later; same length.
	roll, touched, err := ApplyOps(s, []Op{{Op: "trimClip", ID: "broll", Edge: "in", To: fp(0.5), Mode: TrimRoll}}, false)
	if err != nil || clipOf(roll, "hook").Out != 5.5 || clipOf(roll, "broll").In != 0.5 || length(roll) != length(s) {
		t.Fatalf("roll in: %v %+v", err, roll.Clips)
	}
	if !touched["hook"] || !touched["broll"] || !touched["cut:hook>broll"] {
		t.Fatalf("roll touched %v", touched)
	}
	roll, _, err = ApplyOps(s, []Op{{Op: "trimClip", ID: "broll", Edge: "end", To: fp(2.5), Mode: TrimRoll}}, false)
	if err != nil || clipOf(roll, "broll").Out != 2.5 || clipOf(roll, "end").In != 9.5 || length(roll) != length(s) {
		t.Fatalf("roll end: %v %+v", err, roll.Clips)
	}
	// A slower clip moves its edge less footage for the same cut (speed 2: twice the footage).
	fast := Clone(s)
	fast.Clips[0].Speed = 2
	fast.Clips[0].Out = 9 // 4 s on the timeline
	roll, _, err = ApplyOps(fast, []Op{{Op: "trimClip", ID: "broll", Edge: "in", To: fp(0.5), Mode: TrimRoll}}, false)
	if err != nil || clipOf(roll, "hook").Out != 10 {
		t.Fatalf("roll with speed: %v %+v", err, roll.Clips[0])
	}
	if _, _, err := ApplyOps(s, []Op{{Op: "trimClip", ID: "hook", Edge: "in", To: fp(2), Mode: TrimRoll}}, false); err == nil {
		t.Fatal("the first clip has nothing before it to roll with")
	}
	if _, _, err := ApplyOps(s, []Op{{Op: "trimClip", ID: "hook", Edge: "end", To: fp(0.5)}}, false); err == nil {
		t.Fatal("an end before the start is refused")
	}
	if _, _, err := ApplyOps(s, []Op{{Op: "trimClip", ID: "hook", To: fp(2)}}, false); err == nil {
		t.Fatal("the edge is required")
	}
	locked, _, _ := ApplyOps(s, []Op{{Op: "lock", ID: "hook"}}, false)
	var le *LockedError
	if _, _, err := ApplyOps(locked, []Op{{Op: "trimClip", ID: "broll", Edge: "in", To: fp(0.5), Mode: TrimRoll}}, false); !errors.As(err, &le) || le.ID != "hook" {
		t.Fatalf("rolling into a locked clip: %v", err)
	}
	if _, _, err := ApplyOps(locked, []Op{{Op: "trimClip", ID: "broll", Edge: "in", To: fp(0.5)}}, false); err != nil {
		t.Fatalf("a ripple trim next to a locked clip is fine: %v", err)
	}
}

func TestSlipClip(t *testing.T) {
	s := studioSpec()
	next, touched, err := ApplyOps(s, []Op{{Op: "slipClip", ID: "end", By: fp(-2.5)}}, false)
	if err != nil || clipOf(next, "end").In != 7.5 || clipOf(next, "end").Out != 11.5 || length(next) != length(s) || len(touched) != 1 {
		t.Fatalf("slip: %v %+v", err, next.Clips[2])
	}
	if _, _, err := ApplyOps(s, []Op{{Op: "slipClip", ID: "hook", By: fp(-2)}}, false); err == nil {
		t.Fatal("sliding before the footage starts is refused")
	}
}

func TestRemoveWords(t *testing.T) {
	s := studioSpec()
	mid, touched, err := ApplyOps(s, []Op{{Op: "removeWords", ID: "hook", From: fp(2), To: fp(2.6)}}, false)
	if err != nil || clipIDs(mid) != "hook,hook-2,broll,end" {
		t.Fatalf("inside: %v %s", err, clipIDs(mid))
	}
	if clipOf(mid, "hook").Out != 2 || clipOf(mid, "hook-2").In != 2.6 || clipOf(mid, "hook-2").Transition != nil || math.Abs(length(mid)-(length(s)-0.6)) > 1e-9 {
		t.Fatalf("parts: %+v", mid.Clips[:2])
	}
	if !touched["hook"] || !touched["hook-2"] {
		t.Fatalf("touched %v", touched)
	}
	head, _, err := ApplyOps(s, []Op{{Op: "removeWords", ID: "hook", From: fp(0.5), To: fp(1.7)}}, false)
	if err != nil || clipIDs(head) != "hook,broll,end" || clipOf(head, "hook").In != 1.7 {
		t.Fatalf("head: %v %+v", err, head.Clips[0])
	}
	tail, _, err := ApplyOps(s, []Op{{Op: "removeWords", ID: "hook", From: fp(4.2), To: fp(9)}}, false)
	if err != nil || clipOf(tail, "hook").Out != 4.2 {
		t.Fatalf("tail: %v %+v", err, tail.Clips[0])
	}
	// What would be left is under two frames: the whole clip goes.
	gone, _, err := ApplyOps(s, []Op{{Op: "removeWords", ID: "broll", From: fp(0.02), To: fp(3)}}, false)
	if err != nil || clipIDs(gone) != "hook,end" {
		t.Fatalf("whole clip: %v %s", err, clipIDs(gone))
	}
	if _, _, err := ApplyOps(s, []Op{{Op: "removeWords", ID: "hook", From: fp(6), To: fp(7)}}, false); err == nil {
		t.Fatal("a range outside the clip is refused")
	}
	one := &Spec{Output: s.Output, Clips: []Clip{s.Clips[0]}}
	if _, _, err := ApplyOps(one, []Op{{Op: "removeWords", ID: "hook", From: fp(0), To: fp(9)}}, false); err == nil {
		t.Fatal("removing the whole cut is refused")
	}
}

func TestDeleteRange(t *testing.T) {
	s := studioSpec() // hook 0–4, broll 4–7, end 7–11
	inside, touched, err := ApplyOps(s, []Op{{Op: "deleteRange", From: fp(1), To: fp(2)}}, false)
	if err != nil || clipIDs(inside) != "hook,hook-2,broll,end" || clipOf(inside, "hook").Out != 2 || clipOf(inside, "hook-2").In != 3 {
		t.Fatalf("inside one clip: %v %+v", err, inside.Clips)
	}
	if !touched["at:hook"] || !touched["hook-2"] {
		t.Fatalf("touched %v", touched)
	}
	// Across clips: the end of hook, all of broll and the start of end; one straight cut joins them.
	withFade := Clone(s)
	withFade.Clips[2].Transition = &Transition{Type: TransitionDip, Duration: 0.6}
	across, touched, err := ApplyOps(withFade, []Op{{Op: "deleteRange", From: fp(3), To: fp(8)}}, false)
	if err != nil || clipIDs(across) != "hook,end" {
		t.Fatalf("across: %v %s", err, clipIDs(across))
	}
	if clipOf(across, "hook").Out != 4 || clipOf(across, "end").In != 11 || clipOf(across, "end").Transition != nil || math.Abs(length(across)-6) > 1e-9 {
		t.Fatalf("across parts: %+v", across.Clips)
	}
	if !touched["broll"] || !touched["at:broll"] || !touched["cut:hook>end"] {
		t.Fatalf("across touched %v", touched)
	}
	// Exactly one clip, from cut to cut: like removing it (the next cut keeps its transition).
	exact, _, err := ApplyOps(withFade, []Op{{Op: "deleteRange", From: fp(4), To: fp(7)}}, false)
	if err != nil || clipIDs(exact) != "hook,end" || clipOf(exact, "end").Transition == nil {
		t.Fatalf("exactly a clip: %v %+v", err, exact.Clips)
	}
	// The start of a clip from its cut: the same cut, so its transition stays.
	start, _, err := ApplyOps(withFade, []Op{{Op: "deleteRange", From: fp(7), To: fp(8)}}, false)
	if err != nil || clipOf(start, "end").In != 11 || clipOf(start, "end").Transition == nil {
		t.Fatalf("start of a clip: %v %+v", err, start.Clips)
	}
	// Past the end: clipped to the end; beyond it: nothing there.
	tail, _, err := ApplyOps(s, []Op{{Op: "deleteRange", From: fp(10), To: fp(99)}}, false)
	if err != nil || clipOf(tail, "end").Out != 13 || clipOf(tail, "end").FadeOut != 1 {
		t.Fatalf("to the end: %v %+v", err, tail.Clips)
	}
	if _, _, err := ApplyOps(s, []Op{{Op: "deleteRange", From: fp(20), To: fp(21)}}, false); err == nil {
		t.Fatal("nothing past the end")
	}
	if _, _, err := ApplyOps(s, []Op{{Op: "deleteRange", From: fp(0), To: fp(11)}}, false); err == nil {
		t.Fatal("not the whole cut")
	}
	locked, _, _ := ApplyOps(s, []Op{{Op: "lock", ID: "broll"}}, false)
	var le *LockedError
	if _, _, err := ApplyOps(locked, []Op{{Op: "deleteRange", From: fp(3), To: fp(5)}}, false); !errors.As(err, &le) || le.ID != "broll" {
		t.Fatalf("locked: %v", err)
	}
	if _, _, err := ApplyOps(locked, []Op{{Op: "deleteRange", From: fp(1), To: fp(2)}}, false); err != nil {
		t.Fatalf("a range away from the locked clip: %v", err)
	}
	// A dissolve: the shots meet in its middle (broll from 3.0, hook and broll meet at 3.5).
	dis := Clone(s)
	dis.Clips[1].Transition = &Transition{Type: TransitionCrossfade, Duration: 1}
	half, _, err := ApplyOps(dis, []Op{{Op: "deleteRange", From: fp(3.6), To: fp(4)}}, false)
	if err != nil || clipIDs(half) != "hook,broll,broll-2,end" || clipOf(half, "hook").Out != 5 || clipOf(half, "broll").Out != 0.6 || clipOf(half, "broll-2").In != 1 {
		t.Fatalf("inside broll's half of the dissolve: %v %+v", err, half.Clips)
	}
	over, _, err := ApplyOps(dis, []Op{{Op: "deleteRange", From: fp(3.4), To: fp(3.6)}}, false)
	if err != nil || clipOf(over, "hook").Out != 4.4 || clipOf(over, "broll").In != 0.6 || clipOf(over, "broll").Transition != nil {
		t.Fatalf("across the middle of a dissolve: %v %+v", err, over.Clips)
	}
}

// Times on a clip are footage times, so they rebase over changes elsewhere; a
// stretch of the timeline conflicts when an earlier change moved what it cuts.
func TestStudioOpsRebase(t *testing.T) {
	base := studioSpec()
	earlier, _, _ := ApplyOps(base, []Op{{Op: "trimClip", ID: "hook", Edge: "end", To: fp(4)}}, false) // everything after hook moves 1 s
	later, _, _ := ApplyOps(base, []Op{{Op: "trimClip", ID: "end", Edge: "end", To: fp(13)}}, false)

	words := []Op{{Op: "removeWords", ID: "end", From: fp(11), To: fp(11.5)}}
	if r, conflicts, err := Rebase(base, earlier, words, false); err != nil || len(conflicts) != 0 || clipIDs(r) != "hook,broll,end,end-2" || clipOf(r, "hook").Out != 4 {
		t.Fatalf("words rebase over an earlier trim: %v %v", conflicts, err)
	}
	rng := []Op{{Op: "deleteRange", From: fp(8), To: fp(9)}} // inside "end"
	if _, conflicts, _ := Rebase(base, earlier, rng, false); !contains(conflicts, "at:end") {
		t.Fatalf("a timeline range after a change that moved it: %v", conflicts)
	}
	if r, conflicts, err := Rebase(base, later, []Op{{Op: "deleteRange", From: fp(1), To: fp(2)}}, false); err != nil || len(conflicts) != 0 || clipOf(r, "end").Out != 13 {
		t.Fatalf("a range before the other change rebases: %v %v", conflicts, err)
	}
	split := []Op{{Op: "splitClip", ID: "broll", At: fp(1), NewID: "broll-2"}}
	if _, conflicts, _ := Rebase(base, later, split, false); len(conflicts) != 0 {
		t.Fatalf("split rebases: %v", conflicts)
	}
	trimmedBroll, _, _ := ApplyOps(base, []Op{{Op: "trimClip", ID: "broll", Edge: "end", To: fp(2)}}, false)
	if _, conflicts, _ := Rebase(base, trimmedBroll, split, false); !contains(conflicts, "broll") {
		t.Fatalf("split of a clip someone trimmed: %v", conflicts)
	}
	roll := []Op{{Op: "trimClip", ID: "broll", Edge: "in", To: fp(0.5), Mode: TrimRoll}}
	if _, conflicts, _ := Rebase(base, earlier, roll, false); !contains(conflicts, "hook") {
		t.Fatalf("a roll into a clip someone trimmed: %v", conflicts)
	}
}

// A cut sheet that cuts a clip in two saves as a splitClip; anything else stays field updates.
func TestDiffOpsFindsSplits(t *testing.T) {
	base := studioSpec()
	next, _, _ := ApplyOps(base, []Op{{Op: "splitClip", ID: "end", At: fp(12), NewID: "end-b"}}, false)
	next.Clips[2].Label = "First half"
	ops, err := DiffOps(base, next)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, o := range ops {
		kinds = append(kinds, o.Op)
	}
	if strings.Join(kinds, ",") != "splitClip,updateClip" || ops[0].ID != "end" || *ops[0].At != 12 || ops[0].NewID != "end-b" || strings.Contains(string(ops[1].Set), "out") {
		t.Fatalf("ops %v %s", kinds, ops[1].Set)
	}
	got, _, err := ApplyOps(base, ops, true)
	if err != nil || Hash(got) != Hash(next) {
		t.Fatalf("reproduces: %v", err)
	}
	// The second part changed too: an ordinary update after the split.
	changed := Clone(next)
	changed.Clips[3].Muted = true
	ops, err = DiffOps(base, changed)
	if err != nil || len(ops) != 3 || ops[0].Op != "splitClip" || ops[2].Op != "updateClip" || ops[2].ID != "end-b" {
		t.Fatalf("changed second part: %v %+v", err, ops)
	}
	if got, _, _ := ApplyOps(base, ops, true); Hash(got) != Hash(changed) {
		t.Fatal("changed second part does not reproduce")
	}
	// Footage that does not continue (another take, a jump) is an added clip.
	jump := Clone(next)
	jump.Clips[3].In = 12.4
	if ops, err := DiffOps(base, jump); err != nil || ops[0].Op == "splitClip" {
		t.Fatalf("a jump is not a split: %v %+v", err, ops)
	}
	if pairs := SplitPairs(base, next); pairs["end-b"] != "end" || len(pairs) != 1 {
		t.Fatalf("pairs %v", pairs)
	}
}

func TestOpsSummary(t *testing.T) {
	ops := []Op{{Op: "moveClip"}, {Op: "trimClip", Mode: TrimRipple}, {Op: "moveClip"}, {Op: "removeWords"}, {Op: "setTransition"}, {Op: "trimClip", Mode: TrimRoll}}
	if got := OpsSummary(ops); got != "Moved 2 clips, trimmed 1, took out words in 1 place, changed 1 transition, moved 1 cut between two clips" {
		t.Fatalf("summary %q", got)
	}
	if got := OpsSummary([]Op{{Op: "deleteRange"}, {Op: "splitClip"}, {Op: "splitClip"}}); got != "Cut out 1 part, split 2 clips" {
		t.Fatalf("summary %q", got)
	}
	if got := OpsSummary(nil); got != "No changes" {
		t.Fatalf("empty %q", got)
	}
}

func TestPartID(t *testing.T) {
	s := studioSpec()
	s.Overlays = []Overlay{{ID: "hook-2", Template: TplTitleCard, At: 0, Duration: 2, Params: json.RawMessage(`{"title":"x"}`)}}
	if got := PartID(s, "hook"); got != "hook-3" {
		t.Fatalf("skips used IDs: %s", got)
	}
	long := strings.Repeat("a", 64)
	if got := PartID(s, long); len(got) != 64 || !strings.HasSuffix(got, "-2") {
		t.Fatalf("long: %s", got)
	}
}
