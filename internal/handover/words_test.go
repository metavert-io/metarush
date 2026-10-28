package handover

import (
	"strings"
	"testing"
)

// A computer that was taken over says who took its role; one that handed it
// over says whom it handed it to; and the count agrees with its verb.
func TestStepDownWordsSayWhatHappened(t *testing.T) {
	handed := ClientWords(&HandoverRecord{Kind: KindHandover, ToName: "Edit mini"})
	if !strings.Contains(handed, "it handed its role to “Edit mini”") {
		t.Fatalf("after a handover: %q", handed)
	}
	taken := ClientWords(&HandoverRecord{Kind: KindTakeover, ToName: "Edit mini"})
	if !strings.Contains(taken, "“Edit mini” took over its role") || strings.Contains(taken, "handed") {
		t.Fatalf("after a take-over: %q", taken)
	}
	if got := goOn(1); got != "1 conversation goes" {
		t.Fatalf("one: %q", got)
	}
	if got := goOn(3); got != "3 conversations go" {
		t.Fatalf("three: %q", got)
	}
}
