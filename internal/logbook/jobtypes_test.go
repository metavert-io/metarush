package logbook

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/testutil/jobtypes"
)

// Every job type has its own words in the activity log (D396): a reshaped
// render reads as a render ("made a review copy of …"), never "finished the
// render-shape for a clip"; a new job type with no phrase fails here.
func TestEveryJobTypeHasAPhrase(t *testing.T) {
	all, err := jobtypes.All()
	if err != nil || len(all) < 20 {
		t.Fatalf("the job types from queue.go: %v %v", all, err)
	}
	names := &Names{Projects: map[string]string{"prj_1": "Garden story"}}
	for _, typ := range all {
		e := &Entry{Job: &JobInfo{Type: typ, Event: JobSucceeded}, Refs: Refs{ProjectID: "prj_1"}}
		got := describeJob(e, names)
		if strings.HasPrefix(got, "finished the ") || (strings.ContainsAny(typ, "-_") && strings.Contains(got, typ)) {
			t.Errorf("%s has no phrase of its own: %q", typ, got)
		}
	}
	render := describeJob(&Entry{Job: &JobInfo{Type: "render", Event: JobSucceeded}, Refs: Refs{ProjectID: "prj_1"}}, names)
	shaped := describeJob(&Entry{Job: &JobInfo{Type: "render-shape", Event: JobSucceeded}, Refs: Refs{ProjectID: "prj_1"}}, names)
	if shaped != render || !strings.HasPrefix(shaped, "made a review copy of Garden story") {
		t.Fatalf("a copy in another shape reads as a render: %q (a render: %q)", shaped, render)
	}
}
