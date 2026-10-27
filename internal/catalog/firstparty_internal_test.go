package catalog

import (
	"strings"
	"testing"
)

// A photo's subject works on Linux too, for plain-colour backgrounds: it
// says it works there, in part, rather than "needs a Mac" (D270).
func TestWorksHereInPart(t *testing.T) {
	fp, ok := firstPartyNamed("remove-background")
	if !ok {
		t.Fatal("no remove-background")
	}
	for _, c := range []struct{ os, partly string }{{"linux", "only plain-colour backgrounds"}, {"mac", ""}} {
		e := fp.entry()
		(&hereFacts{os: c.os}).fill(&e, fp)
		if e.WorksHere == nil || !*e.WorksHere || e.Missing != "" || e.Partly != c.partly {
			t.Fatalf("%s: %+v", c.os, e)
		}
		line := Line(e)
		if c.partly != "" && !strings.Contains(line, "here only plain-colour backgrounds") {
			t.Fatalf("%s line: %s", c.os, line)
		}
		if it := menuItem(e); c.partly != "" && it.Line != "A photo's subject — works here, only plain-colour backgrounds" {
			t.Fatalf("%s menu line: %q", c.os, it.Line)
		}
	}
	if r := strings.Join(fp.requires(), ", "); !strings.Contains(r, "a Mac (elsewhere, only plain-colour backgrounds)") {
		t.Fatalf("requires: %s", r)
	}
	// Without Partly, another system still says what it needs.
	faces, _ := firstPartyNamed("faces")
	e := faces.entry()
	(&hereFacts{os: "linux"}).fill(&e, faces)
	if e.WorksHere == nil || *e.WorksHere || !strings.Contains(e.Missing, "needs a Mac") {
		t.Fatalf("faces on Linux: %+v", e)
	}
}
