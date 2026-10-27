package logbook

import (
	"encoding/json"
	"testing"
)

// Notes about a person are logged as a change, never as their text, and the
// row names the person.
func TestPeopleNotesAreLoggedAsAChangeOnly(t *testing.T) {
	in := ShortenInput(NewRedactor(), json.RawMessage(`{"id":"Maya","notes":"Maya Lindqvist prefers her full name. Never mention her age."}`), PrivacyWorkspace)
	if in["notes"] != "[60 characters]" || in["id"] != "Maya" {
		t.Fatalf("notes must be logged as their size only: %+v", in)
	}
	out := SummarizeOutput(map[string]any{"person": map[string]any{"id": "per_01jabcdefghjkmnpqrstvwxyz0", "name": "Maya Lindqvist"}, "summary": "Saved the notes for Maya Lindqvist."})
	e := Entry{Kind: KindOp, Op: &OpInfo{Name: "people.update", OK: true, Mutating: true, Output: out}}
	if got := Describe(&e, &Names{}, ""); got != "Updated notes for Maya Lindqvist" {
		t.Fatalf("row: %q", got)
	}
	e.Op.Output = nil
	if got := Describe(&e, &Names{}, ""); got != "Updated notes for someone" {
		t.Fatalf("row without a name: %q", got)
	}
	for op, want := range map[string]string{
		"footage.speakers.assign":  "Fixed who said a line in a clip",
		"footage.speakers.unmerge": "Separated two voices in a clip",
		"footage.speakers.detect":  "Listened for the voices in the footage",
		"people.rebuild":           "Rebuilt a person's voice from confirmed clips",
		"people.distinct":          "Said two people are different",
	} {
		e := Entry{Kind: KindOp, Op: &OpInfo{Name: op, OK: true}}
		if got := Describe(&e, &Names{}, ""); got != want {
			t.Errorf("%s: %q, want %q", op, got, want)
		}
	}
}
