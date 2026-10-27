package cutsheet

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
)

// Layout pack roles in a cut sheet (D264): a role in the template's place,
// filled by the pack when the sheet is checked; MetaRush writes the template
// it was filled with and the role after "as".
func TestRolesInGraphics(t *testing.T) {
	base := minimalSpec()
	base.Clips[0].Out = 20
	head := Print(base, Options{ProjectID: testProject}) + "\n## Graphics\n\n"
	for line, want := range map[string]editspec.Overlay{
		`- intro: title card at 0:00 for 3 s · with title "Hello"`:                        {ID: "intro", Role: editspec.PackTitleCard},
		`- ada: lower third: guest at 0:04 for 3 s · with name "Ada"`:                     {ID: "ada", Role: "lower-third:guest"},
		`- ada: lower-third:guest at 0:04 for 3 s · with name "Ada"`:                      {ID: "ada", Role: "lower-third:guest"},
		`- q: quote-card at 0:08 for 4 s · with text "Yes"`:                               {ID: "q", Role: editspec.PackQuoteCard},
		`- end: endcard.cta at 0:15 for 4 s · as end screen · with headline "Bye"`:        {ID: "end", Role: editspec.PackEndScreen, Template: editspec.TplEndCard},
		`- who: lower-third.clean at 0:01 for 3 s · as lower third: host · with name "B"`: {ID: "who", Role: "lower-third:host", Template: editspec.TplLowerThirdClean},
	} {
		sheet, probs := Parse(head+line+"\n", ParseOptions{})
		if HasErrors(probs) {
			t.Fatalf("%s: %s", line, problemsText(probs))
		}
		o := sheet.Spec.Overlays[0]
		if o.ID != want.ID || o.Role != want.Role || o.Template != want.Template {
			t.Fatalf("%s: got %+v", line, o)
		}
		back := Print(sheet.Spec, Options{})
		again, probs := Parse(back, ParseOptions{})
		if HasErrors(probs) || editspec.Hash(again.Spec) != editspec.Hash(sheet.Spec) {
			t.Fatalf("%s: reprint\n%s\n%s", line, back, problemsText(probs))
		}
	}
	for line, code := range map[string]string{
		`- x: title.card at 0:00 for 3 s · as logo corner`:    "unknown_role",
		`- x: title.card at 0:00 for 3 s · as title card now`: "unknown_role",
	} {
		_, probs := Parse(head+line+"\n", ParseOptions{})
		if !hasCode(probs, code) {
			t.Fatalf("%s: want %s, got %s", line, code, problemsText(probs))
		}
	}
	// Printed: the template it was filled with, then the role.
	s := &editspec.Spec{Output: editspec.Output{Preset: editspec.PresetLandscape}, Clips: []editspec.Clip{{ID: "a", AssetID: "ast_01jabcdefghjkmnpqrstvwxya0", In: 0, Out: 9}},
		Overlays: []editspec.Overlay{{ID: "intro", Template: editspec.TplTitleCard, Role: editspec.PackTitleCard, At: 0, Duration: 3, Params: json.RawMessage(`{"title":"Hi"}`)}}}
	editspec.Normalize(s)
	if text := Print(s, Options{}); !strings.Contains(text, `- intro: title.card at 0:00 for 3 s · as title card · with title "Hi"`) {
		t.Fatalf("printed:\n%s", text)
	}
	b := editspec.Clone(s)
	b.Overlays[0].Role = editspec.PackChapterCard
	if ch := strings.Join(Describe(s, b, nil), " "); !strings.Contains(ch, "is now the layout pack's chapter card") {
		t.Fatalf("describe: %s", ch)
	}
}
