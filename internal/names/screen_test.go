package names

import "testing"

// Text in the picture is read as a person's name only when it looks like one
// (D156): call-app name tiles and lower thirds yes; app labels, banners,
// titles, signs and numbers no.
func TestFromScreen(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"Lena Okoye", "Lena Okoye", true},
		{"Lena", "Lena", true},
		{"Lena's iPhone", "Lena", true},
		{"Ben’s MacBook Pro", "Ben", true},
		{"Maya Lindqvist (She/Her)", "Maya Lindqvist", true},
		{"Ada Park (Host)", "Ada Park", true},
		{"Jane Doe, CEO", "Jane Doe", true},
		{"Jane Doe | Founder", "Jane Doe", true},
		{"JANE O'BRIEN", "Jane O'Brien", true},
		{"Ana de Souza", "Ana de Souza", true},
		{"José Álvarez", "José Álvarez", true},
		{"L. Okoye", "L. Okoye", true},
		{"Anne-Marie Dufour", "Anne-Marie Dufour", true},
		{"● Recording...", "", false},
		{"Mute", "", false},
		{"Gallery View", "", false},
		{"Share Screen", "", false},
		{"Garden club · weekly call", "", false},
		{"Weekly Call", "", false},
		{"EXIT", "", false},
		{"Episode 3", "", false},
		{"iPhone (2)", "", false},
		{"thanks everyone", "", false},
		{"AB", "", false},
		{"The Quick Brown Fox Jumps", "", false},
		{"", "", false},
	} {
		got, ok := FromScreen(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("FromScreen(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// Names that can mean the same person: the same, or one is the start of the
// other word by word.
func TestSameName(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"Lena", "Lena Okoye", true},
		{"lena okoye", "Lena Okoye", true},
		{"L Okoye", "Lena Okoye", true},
		{"Len", "Lena Okoye", false},
		{"Okoye", "Lena Okoye", false},
		{"Ada Park", "Ben Okafor", false},
		{"Cleo", "Cleo Marsh", true},
		{"", "Cleo", false},
	} {
		if got := SameName(c.a, c.b); got != c.same {
			t.Errorf("SameName(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
