package logbook

import (
	"strings"
	"testing"
)

// A cut.transition entry reads as the change people asked for, from the small
// facts the log keeps of its result (no plan or spec is stored).
func TestCutTransitionReadsInPlainWords(t *testing.T) {
	names := &Names{Projects: map[string]string{"prj_01jabcdefghjkmnpqrstvwxyz0": "Spring promo"}}
	entry := func(out map[string]any, input map[string]any) *Entry {
		return &Entry{Kind: KindOp, Refs: Refs{ProjectID: "prj_01jabcdefghjkmnpqrstvwxyz0"},
			Op: &OpInfo{Name: "cut.transition", Mutating: true, OK: true, Input: input, Output: SummarizeOutput(out)}}
	}
	for _, c := range []struct {
		out  map[string]any
		in   map[string]any
		want string
	}{
		{map[string]any{"into": "Sam's clip", "saved": true, "applied": map[string]any{"revision": map[string]any{"number": 4}}}, nil, "Changed the transition into Sam's clip in Spring promo"},
		{map[string]any{"into": "Sam's clip", "check": map[string]any{"valid": true}}, map[string]any{"dryRun": true}, "Checked a change to the transition into Sam's clip in Spring promo"},
		{map[string]any{"into": "Sam's clip", "noChange": true}, nil, "Looked at the transition into Sam's clip in Spring promo (nothing to change)"},
		{map[string]any{}, nil, "Changed a transition in Spring promo"},
	} {
		if got := Describe(entry(c.out, c.in), names, ""); !strings.HasPrefix(got, c.want) {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
	if got := Describe(&Entry{Kind: KindOp, Op: &OpInfo{Name: "edit.transitions", OK: true}}, names, ""); !strings.HasPrefix(got, "Listed the transitions") {
		t.Errorf("edit.transitions: %q", got)
	}
}

// The cut's editing verbs read as what was done, or only checked.
func TestCutVerbsReadInPlainWords(t *testing.T) {
	names := &Names{Projects: map[string]string{"prj_01jabcdefghjkmnpqrstvwxyz0": "Spring promo"}}
	entry := func(op string, out map[string]any) *Entry {
		return &Entry{Kind: KindOp, Refs: Refs{ProjectID: "prj_01jabcdefghjkmnpqrstvwxyz0"},
			Op: &OpInfo{Name: op, Mutating: true, OK: true, Output: SummarizeOutput(out)}}
	}
	for _, c := range []struct {
		op   string
		out  map[string]any
		want string
	}{
		{"cut.trim", map[string]any{"saved": true}, "Trimmed a clip in Spring promo"},
		{"cut.trim", map[string]any{"check": map[string]any{"valid": true}}, "Checked a trim in Spring promo"},
		{"cut.split", map[string]any{"saved": true}, "Split a clip in Spring promo"},
		{"cut.move", map[string]any{"noChange": true}, "Looked at moving a clip in Spring promo (nothing to change)"},
		{"cut.deleteRange", map[string]any{"saved": true}, "Cut out part of Spring promo"},
		{"cut.removeWords", map[string]any{"saved": true}, "Took words out of Spring promo"},
		{"cut.add", map[string]any{"saved": true}, "Added a clip to Spring promo"},
		{"cut.delete", map[string]any{}, "Checked removing clips from Spring promo"},
		{"edit.preview", map[string]any{}, "Previewed a change to Spring promo"},
	} {
		if got := Describe(entry(c.op, c.out), names, ""); !strings.HasPrefix(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.op, got, c.want)
		}
	}
}
