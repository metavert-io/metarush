package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/testutil/testdbs"
)

func TestReportSaysWhatWasFoundAndDone(t *testing.T) {
	found := []testdbs.Found{
		{Name: "metarush-test-a", Stale: true, Why: "its test process 7 ended", Dropped: true},
		{Name: "metarush-test-b"},
		{Name: "metarush-test-c", Stale: true, Why: "a script's", Err: errors.New("refusing")},
	}
	got := Report(found, true)
	for _, want := range []string{"dropped metarush-test-a (its test process 7 ended)", "kept   metarush-test-c: refusing", "3 test databases: 2 stale, dropped, 1 kept"} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
	listed := Report([]testdbs.Found{{Name: "metarush-test-x", Stale: true, Why: "old"}}, false)
	if !strings.Contains(listed, "stale  metarush-test-x (old)") || !strings.Contains(listed, "Run again with --drop") {
		t.Errorf("listing:\n%s", listed)
	}
}
