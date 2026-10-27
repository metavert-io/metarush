package editspec

import (
	"strings"
	"testing"
)

// Lines out of script order (rearranged shots moved a later line in front of an
// earlier one) get the time up to whatever plays after them, never a negative
// window; messages name the line that follows in time, and the last line runs
// up to the end of the video, not "the next line".
func TestFitWindowsFollowTime(t *testing.T) {
	f := voFacts()
	s := voSpec() // b1 at 3.0 s (broll +0.5), b2 at 7.5 s (a moment in "end"), b3 at 2.0 s (clock time): b3 plays first
	clips, total := Timeline(s)
	bs := ResolveBeats(s, clips, total, f)
	b1, b2, b3 := beatByID(bs, "b1"), beatByID(bs, "b2"), beatByID(bs, "b3")
	if b2.WindowEnd != total || b2.Next != -1 || b2.Window() != total-b2.Start {
		t.Fatalf("b2 (last in time) runs to the end: %+v (total %d)", b2, total)
	}
	if b1.Next != 1 || !b1.NextInScript || b1.WindowEnd != b2.Start {
		t.Fatalf("b1 runs to b2, the next line in time and in the script: %+v", b1)
	}
	if b3.Next != 0 || b3.NextInScript || b3.WindowEnd != b1.Start {
		t.Fatalf("b3 runs to b1, which plays after it but sits above it in the script: %+v", b3)
	}
	if b3.Until() != "before line 1 starts" || b2.Until() != "before the video ends" || b1.Until() != "before the next line" {
		t.Fatalf("wording: %q %q %q", b1.Until(), b2.Until(), b3.Until())
	}
	for _, r := range FitReport(Clone(s), f) {
		if r.Window < 0 {
			t.Fatalf("negative window: %+v", r)
		}
		if strings.Contains(r.Message, "-") && strings.Contains(r.Message, "seconds") {
			t.Fatalf("no negative seconds in messages: %q", r.Message)
		}
	}

	// The last line runs long: it is cut off by the end of the video (the last
	// shot is unlocked here so it can be held longer).
	long := strings.Repeat("word ", 30) + "end."
	s.Voiceover.Beats[1].Text = long
	s.Clips[2].Locked = false
	rep := FitReport(Clone(s), f)
	last := rep[1]
	if last.Status != "over" || !strings.Contains(last.Message, "before the video ends") || strings.Contains(last.Message, "next line") {
		t.Fatalf("last line: %+v", last)
	}
	var extend *FitFix
	for i := range last.Fixes {
		if last.Fixes[i].Strategy == FixExtendShot {
			extend = &last.Fixes[i]
		}
	}
	if extend == nil || extend.Ops[0].ID != "end" {
		t.Fatalf("the fix for the last line holds the last shot longer: %+v", last.Fixes)
	}
	issues := Validate(Clone(s), f, nil, false)
	found := false
	for _, is := range issues {
		if is.Code == "beat_fit" && strings.Contains(is.Message, "Narration line 2 (“") {
			found = strings.Contains(is.Message, "before the video ends") && !strings.Contains(is.Message, "next line")
		}
	}
	if !found {
		t.Fatalf("validation wording for the last line: %+v", issues)
	}

	// The out-of-order line runs into the line that plays after it.
	s = voSpec()
	s.Voiceover.Beats[2].Text = "A much longer hello that cannot possibly fit in one second."
	rep = FitReport(Clone(s), f)
	if rep[2].Status != "over" || !strings.Contains(rep[2].Message, "before line 1 starts") {
		t.Fatalf("out of order: %+v", rep[2])
	}
	for _, fx := range rep[2].Fixes {
		if fx.Strategy == FixMoveNext {
			t.Fatalf("line 1 is anchored to a shot, so it is not moved as a clock time: %+v", fx)
		}
	}
}

// A line that would start after the video ends says so, with a positive window.
func TestFitLineAfterTheEnd(t *testing.T) {
	f := voFacts()
	s := voSpec()
	s.Voiceover.Beats[2].Anchor = Anchor{Kind: AnchorTime, At: ptr(20)}
	clips, total := Timeline(s)
	bs := ResolveBeats(s, clips, total, f)
	if b := beatByID(bs, "b3"); b.Window() != 0 || b.Next != -1 {
		t.Fatalf("after the end: %+v", b)
	}
	rep := FitReport(Clone(s), f)
	if rep[2].Status != "over" || !strings.Contains(rep[2].Message, "after the video ends") || rep[2].Window != 0 {
		t.Fatalf("after the end: %+v", rep[2])
	}
	// b2 is no longer followed by b3 (it starts after the end): it runs to the end.
	if b := beatByID(bs, "b2"); b.Next != -1 || b.WindowEnd != total {
		t.Fatalf("b2: %+v", b)
	}
}
