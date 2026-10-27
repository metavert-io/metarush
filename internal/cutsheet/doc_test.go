package cutsheet

import (
	"os"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
)

// The complete example in docs/cut-sheet.md parses without problems and
// round-trips, so the reference cannot drift from the format.
func TestDocExampleParses(t *testing.T) {
	b, err := os.ReadFile("../../docs/cut-sheet.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	_, rest, ok := strings.Cut(doc, "## A complete example")
	if !ok {
		t.Fatal("docs/cut-sheet.md has no complete example")
	}
	_, rest, _ = strings.Cut(rest, "```markdown\n")
	example, _, ok := strings.Cut(rest, "\n```\n")
	if !ok {
		t.Fatal("unterminated example")
	}
	sheet, probs := Parse(example+"\n", ParseOptions{Info: testInfo()})
	if len(probs) > 0 {
		t.Fatalf("the documented example has problems: %s", problemsText(probs))
	}
	if len(sheet.Spec.Clips) != 4 || len(sheet.Spec.Overlays) != 1 || len(sheet.Spec.Audio) != 1 {
		t.Fatalf("example parsed into %d clips, %d graphics, %d sounds", len(sheet.Spec.Clips), len(sheet.Spec.Overlays), len(sheet.Spec.Audio))
	}
	if v := sheet.Spec.Voiceover; v == nil || len(v.Beats) != 2 || v.Beats[1].Anchor.Kind != "after" || sheet.Spec.Mix == nil || sheet.Spec.Mix.Ducking == nil {
		t.Fatalf("example narration and mix: %+v %+v", sheet.Spec.Voiceover, sheet.Spec.Mix)
	}
	back, probs := Parse(Print(sheet.Spec, Options{ProjectID: sheet.ProjectID, BaseRevisionID: sheet.BaseRevisionID, Info: testInfo()}), ParseOptions{Info: testInfo()})
	if HasErrors(probs) || editspec.Hash(back.Spec) != editspec.Hash(sheet.Spec) {
		t.Fatalf("the documented example does not round-trip: %s", problemsText(probs))
	}
}
