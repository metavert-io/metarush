package secretfields

import (
	"strings"
	"testing"
)

// Scrub hides an operation's secret fields in what its log entry kept, takes
// their values out of the entry's texts, and leaves a clean entry, another
// operation's fields of the same names and short values' texts alone.
func TestScrubTakesTheValuesOut(t *testing.T) {
	in := map[string]any{"current": "tomato garden at dusk", "new": "rosemary under the window", "note": "keep"}
	summary := "account.password failed: rosemary under the window is too common"
	command := "metarush account password"
	empty := ""
	if !Scrub("account.password", in, &summary, &command, &empty, nil) {
		t.Fatal("nothing was scrubbed")
	}
	if in["current"] != Hidden || in["new"] != Hidden || in["note"] != "keep" {
		t.Fatalf("input: %v", in)
	}
	if summary != "account.password failed: "+Hidden+" is too common" || command != "metarush account password" || empty != "" {
		t.Fatalf("texts: %q %q %q", summary, command, empty)
	}
	if Scrub("account.password", in, &summary) {
		t.Fatal("a clean entry was scrubbed again")
	}
	other := map[string]any{"current": "tomato garden at dusk", "new": "rosemary under the window"}
	if Scrub("test.other", other) || other["new"] != "rosemary under the window" {
		t.Fatalf("another operation's fields: %v", other)
	}
	// A long value was kept clipped: what was kept of it leaves the texts.
	long := strings.Repeat("Qx7k", 70)
	in = map[string]any{"new": long[:240] + "… (+40 characters)"}
	text := "it said " + long
	if !Scrub("account.password", in, &text) || strings.Contains(text, long[:240]) {
		t.Fatalf("a clipped value: %q", text)
	}
	// A value shorter than four characters is hidden where it is, never in texts.
	in = map[string]any{"current": "abc"}
	text = "abc and abcd"
	if !Scrub("account.password", in, &text) || in["current"] != Hidden || text != "abc and abcd" {
		t.Fatalf("a short value: %v %q", in, text)
	}
}

// The list names fields, and Of and All answer from it without sharing it.
func TestTheListIsKept(t *testing.T) {
	if f := Of("account.password"); !f["current"] || !f["new"] || len(f) != 2 {
		t.Fatalf("account.password: %v", f)
	}
	if Of("project.create") != nil {
		t.Fatal("an operation with none")
	}
	all := All()
	all["account.password"][0] = "changed"
	if !Of("account.password")["current"] {
		t.Fatal("All shares the list")
	}
}
