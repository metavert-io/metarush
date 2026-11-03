package ops

import (
	"testing"

	"github.com/metavert-io/metarush/internal/testutil/jobtypes"
)

// Every job type has a label on its job page and in the log (D396): a
// reshaped render is a "Render", and a new type with none fails here.
func TestEveryJobTypeHasAKindLabel(t *testing.T) {
	all, err := jobtypes.All()
	if err != nil || len(all) < 20 {
		t.Fatalf("the job types from queue.go: %v %v", all, err)
	}
	for _, typ := range all {
		if jobKindLabel[typ] == "" {
			t.Errorf("%s has no label (jobKindLabel)", typ)
		}
	}
	if JobKindLabel("render-shape") != "Render" {
		t.Fatal("a copy in another shape is a Render")
	}
}
