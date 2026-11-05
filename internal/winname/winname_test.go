package winname

import (
	"strings"
	"testing"
)

// Windows' rules (D247, D523): refused characters become "-", device names
// get "_", a trailing dot or space goes, the extension stays, a long name is
// cut at a whole character.
func TestSafe(t *testing.T) {
	for in, want := range map[string]string{
		"clip.mov":                         "clip.mov",
		`Q&A: what's next?.mov`:            "Q&A- what's next-.mov",
		`C:\Users\Ana\Videos\harbour.mp4`:  "C--Users-Ana-Videos-harbour.mp4",
		"CON.mp4":                          "_CON.mp4",
		"lpt9":                             "_lpt9",
		"trailing dot. .mov":               "trailing dot.mov",
		"Café — Ana Müller (版本 2).mov":     "Café — Ana Müller (版本 2).mov",
		"":                                 "fallback",
		"...":                              "fallback",
		"console.mp4":                      "console.mp4",
		"tab\there.wav":                    "tabhere.wav",
		"report.notes.md":                  "report.notes.md",
		"garden_cut_r3_vertical-1080p_31s": "garden_cut_r3_vertical-1080p_31s",
	} {
		if got := Safe(in, "fallback"); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("é", 300) + ".mov"
	if got := []rune(Safe(long, "x")); len(got) != MaxStem+4 || string(got[MaxStem:]) != ".mov" {
		t.Errorf("a long name: %d characters", len(got))
	}
	if !Kept("garden_cut_r3_vertical-1080p_31s.mp4") || Kept("a:b.mp4") || Kept("NUL.txt") || Kept("") {
		t.Fatal("Kept")
	}
	if !Reserved("com3.tar.gz") || !Reserved("Aux") || Reserved("auxiliary.mov") {
		t.Fatal("Reserved")
	}
}
