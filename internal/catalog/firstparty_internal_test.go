package catalog

import (
	"strings"
	"testing"
)

// A photo's subject works everywhere in part — plain-colour backgrounds —
// and fully where something can cut a subject out (Apple Vision on a Mac, the
// cut-out model elsewhere, D414): it says it works, in part, rather than what
// it needs (D270).
func TestWorksHereInPart(t *testing.T) {
	fp, ok := firstPartyNamed("remove-background")
	if !ok {
		t.Fatal("no remove-background")
	}
	if r := strings.Join(fp.requires(), ", "); !strings.Contains(r, "(without it, only plain-colour backgrounds)") {
		t.Fatalf("requires: %s", r)
	}
	// What looks at the picture is no longer a Mac's alone: elsewhere it says
	// what to add, not "needs a Mac".
	faces, _ := firstPartyNamed("faces")
	if len(faces.Needs.OS) != 0 || faces.Needs.Setup != "on-screen-helper" {
		t.Fatalf("faces: %+v", faces.Needs)
	}
	e := faces.entry()
	(&hereFacts{os: "linux"}).fill(&e, faces)
	if e.Missing != "" && strings.Contains(e.Missing, "needs a Mac") {
		t.Fatalf("faces on Linux: %+v", e)
	}
}

// The words for the systems and keys an ability needs read as people say them,
// whichever computer the test runs on.
func TestNeedsWordsReadPlainly(t *testing.T) {
	for _, c := range []struct {
		os   []string
		want string
	}{
		{[]string{"mac"}, "a Mac"},
		{[]string{"linux"}, "Linux"},
		{[]string{"mac", "linux"}, "a Mac or Linux"},
	} {
		if got := osWords(c.os); got != c.want {
			t.Errorf("osWords(%v) = %q, want %q", c.os, got, c.want)
		}
	}
	if got := keyWords(nil); got != "" {
		t.Errorf("no keys: %q", got)
	}
	if got := keyTitle("no-such-service"); got != "no-such-service" {
		t.Errorf("a key no service has: %q", got)
	}
}
