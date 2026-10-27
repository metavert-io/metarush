package cutsheet

import (
	"strings"
	"testing"
)

func clipFrom(t *testing.T, line string) (float64, float64, []Problem) {
	t.Helper()
	s, probs := Parse(sheet("## Clips\n"+line+"\n"), ParseOptions{Info: testInfo()})
	if HasErrors(probs) {
		return 0, 0, probs
	}
	return s.Spec.Clips[0].In, s.Spec.Clips[0].Out, probs
}

func TestWordAnchors(t *testing.T) {
	cases := []struct {
		name, line string
		in, out    float64
		warn       string
	}{
		// "because" starts at 8.0 after a 0.5 s gap: in point 0.1 s before it.
		// "very quickly" is said twice; only the one after the in point fits.
		// "quickly." ends at 13.1 with silence after: out point 0.15 s later.
		{"phrase to phrase", `1. a: interview-a from "because I'm going" to "very quickly"`, 7.9, 13.25, ""},
		// "very" follows "it" by 0.1 s: the in point splits that gap.
		{"tight gap before", `1. a: interview-a from "very quickly" to 0:09`, 6.55, 9, ""},
		// "quickly," is followed by "because" 0.5 s later: room for the pad.
		{"out after word", `1. a: interview-a 0:06 → "did it very quickly"`, 6, 7.65, ""},
		// "twice," is followed by "so" 0.3 s later: exactly 2 × pad, so the pad fits.
		{"pad fits gap", `1. a: interview-a 0:10 → "built it twice"`, 10, 11.45, ""},
		// Case, punctuation and apostrophes don't matter.
		{"normalized", `1. a: interview-a from "BECAUSE im GOING" to "fail"`, 7.9, 9.65, ""},
		// Split words: "I m" matches "I'm".
		{"split word", `1. a: interview-a from "because I m going" to "fail"`, 7.9, 9.65, ""},
		// One letter off still matches, with a warning.
		{"fuzzy", `1. a: interview-a from "becase I'm going" to "fail"`, 7.9, 9.65, "fuzzy_anchor"},
		// A segment without word timing gives an approximate point and says so.
		{"sentence timing", `1. a: interview-a from "what would you build" to 0:19`, 15.9, 19, "approximate_anchor"},
		// The first word of the transcript: the pad is clamped at the start.
		{"start of footage", `1. a: interview-a from "when I started" to "first robot"`, 0.4, 2.85, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in, out, probs := clipFrom(t, c.line)
			if HasErrors(probs) {
				t.Fatal(problemsText(probs))
			}
			if in != c.in || out != c.out {
				t.Fatalf("got %v → %v, want %v → %v", in, out, c.in, c.out)
			}
			if c.warn != "" && !hasCode(probs, c.warn) {
				t.Fatalf("expected warning %s, got %s", c.warn, problemsText(probs))
			}
			if c.warn == "" && len(probs) > 0 {
				t.Fatalf("unexpected problems: %s", problemsText(probs))
			}
		})
	}
}

func TestWordAnchorErrors(t *testing.T) {
	cases := []struct{ name, line, code, mention string }{
		{"ambiguous", `1. a: interview-a from "so we built" to 0:13.2`, "phrase_ambiguous", "0:09.9"},
		{"not found", `1. a: interview-a from "banana bread" to 0:13`, "phrase_not_found", ""},
		{"closest hint", `1. a: interview-a from "very quick lee" to 0:13.2`, "phrase_not_found", "very quickly"},
		{"wrong order", `1. a: interview-a from "fail" to "when I started"`, "phrase_order", ""},
		{"no transcript", `1. a: interview-a-2 from "anything" to 0:05`, "unknown_footage", ""},
		{"silent footage", `1. a: broll-c from "anything" to 0:05`, "phrase_not_found", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, probs := clipFrom(t, c.line)
			var got *Problem
			for i := range probs {
				if probs[i].Code == c.code {
					got = &probs[i]
				}
			}
			if got == nil {
				t.Fatalf("expected %s, got %s", c.code, problemsText(probs))
			}
			if c.mention != "" && !strings.Contains(got.Message+" "+got.Hint, c.mention) {
				t.Fatalf("expected %q in %q / %q", c.mention, got.Message, got.Hint)
			}
		})
	}
}

// After applying, the sheet shows exact times (and the derived quote).
func TestAnchorsBecomeTimes(t *testing.T) {
	s, probs := Parse(sheet("## Clips\n1. a: interview-a from \"because I'm going\" to \"very quickly\"\n"), ParseOptions{Info: testInfo()})
	if HasErrors(probs) {
		t.Fatal(problemsText(probs))
	}
	if len(s.Anchors) != 2 || s.Anchors[0].Words != "because I'm going" || s.Anchors[1].Field != "out" {
		t.Fatalf("anchors not recorded: %+v", s.Anchors)
	}
	text := Print(s.Spec, Options{Info: testInfo()})
	if !strings.Contains(text, "0:07.9 → 0:13.25") || !strings.Contains(text, "> “because I'm going to fail. …” … “… we built it very quickly.”") {
		t.Fatalf("expected exact times and the derived quote:\n%s", text)
	}
}
