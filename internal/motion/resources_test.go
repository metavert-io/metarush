package motion

import (
	"encoding/json"
	"testing"

	"github.com/metavert-io/metarush/internal/models"
)

// useResource(params.x) needs x marked as a resource in the schema, or a cut
// couldn't pin the picture it names (D107); names in quotes need nothing.
func TestUnmarkedResourceParams(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"shot":{"type":"string","format":"metarush-resource"},"title":{"type":"string"}}}`)
	refs := []models.MotionResourceRef{{Name: "logo", File: "index.tsx", Line: 3}, {Param: "shot", File: "index.tsx", Line: 4}, {Param: "title", File: "index.tsx", Line: 9}}
	got := unmarkedResourceParams(refs, schema)
	if len(got) != 1 || got[0].Code != "resource_param" || got[0].Line != 9 || got[0].Severity != "error" {
		t.Fatalf("problems: %+v", got)
	}
	if got := unmarkedResourceParams(refs[:2], schema); len(got) != 0 {
		t.Fatalf("a marked parameter and a quoted name: %+v", got)
	}
	if got := unmarkedResourceParams(refs[1:2], nil); len(got) != 1 {
		t.Fatalf("no schema: %+v", got)
	}
}
