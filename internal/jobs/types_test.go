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
		if _, ok := typeLabels[typ]; !ok {
			t.Errorf("%s has no label (typeLabels)", typ)
		}
		if doing(typ) == "do this" {
			t.Errorf("%s says no words for what it does (doing)", typ)
		}
	}
}
