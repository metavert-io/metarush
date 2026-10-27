package logbook

import "testing"

// Checking what Auto may spend reads in plain words, in the logbook and in
// the signals that suggest improvements (D241), never as the operation's
// name or its long summary.
func TestAutoSpendingReadsInPlainWords(t *testing.T) {
	e := &Entry{Kind: KindOp, Op: &OpInfo{Name: "assistant.spending", OK: true}}
	summary := "When Auto spends money by itself: with each outside service, whether a person set the project's budget"
	if got := Describe(e, &Names{}, summary); got != "Checked what Auto may spend and has spent today" {
		t.Fatalf("logbook: %q", got)
	}
	if got := OpWords("assistant.spending"); got != "check what Auto may spend and has spent today" {
		t.Fatalf("signals: %q", got)
	}
}
