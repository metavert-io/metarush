package jobs

import (
	"testing"

	"github.com/metavert-io/metarush/internal/testutil/jobtypes"
)

// Every job type is named for people and says what it does (D396): a new
// type with no label or no words fails here, not on a person's screen.
func TestEveryJobTypeIsNamed(t *testing.T) {
	all, err := jobtypes.All()
	if err != nil || len(all) < 20 {
		t.Fatalf("the job types from queue.go: %v %v", all, err)
	}
	for _, typ := range all {
		if w, ok := typeLabels[typ]; !ok {
			t.Errorf("%s has no label (typeLabels)", typ)
		} else if w[0] == "" || w[1] == "" || w[0] == w[1] {
			t.Errorf("%s needs words for one job and for more than one: %q", typ, w)
		}
		if doing(typ) == "do this" {
			t.Errorf("%s says no words for what it does (doing)", typ)
		}
	}
	// Counted, they read as people say them ("2 steady copies", never "2
	// steady copys"); a type without a label is a job.
	for typ, want := range map[string][2]string{TypeMezzanine: {"steady copy", "steady copies"}, TypeEvidence: {"scene analysis", "scene analyses"},
		TypeVisual: {"look at what's on screen", "looks at what's on screen"}, TypeFetch: {"repository or web page", "repositories or web pages"},
		"no-such-job": {"job", "jobs"}} {
		if one, many := Words(typ); one != want[0] || many != want[1] {
			t.Errorf("Words(%s) = %q, %q; want %q", typ, one, many, want)
		}
	}
}
