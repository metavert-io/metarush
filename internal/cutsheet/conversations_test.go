package cutsheet

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
)

const convSheet = `---
cut: 1
output:
  shape: landscape
footage:
  interview-a: ast_01jabcdefghjkmnpqrstvwxyz0
  sam-cam: ast_01jabcdefghjkmnpqrstvwxyz3
---

## Conversations

- call: cnv_01jabcdefghjkmnpqrstvwxyz0 · quieter when not speaking
  Member: ada "Ada" · interview-a · starts at 0:00
  Member: sam "Sam" · sam-cam · sound only · starts at 0:01.5 · drift -12 ppm

## Clips

1. talk: conversation call from "we built it twice" to "very quickly" · without sound from sam
   Shot: 0:10 ada · why "She says it twice."
   Shot: 0:11.6 side by side
`

func TestConversationSheetQuotedWords(t *testing.T) {
	sheet, probs := Parse(convSheet, ParseOptions{Info: testInfo()})
	if HasErrors(probs) {
		t.Fatalf("problems: %s", problemsText(probs))
	}
	c := sheet.Spec.Clips[0]
	// Ada's recording starts with the conversation, so its words are where
	// they are in her transcript: "we built it twice" starts at 10.22.
	if c.AssetID != editspec.ConvKey("call") || c.In <= 10.0 || c.In > 10.22 || c.Out < 13.1 || c.Out > 13.4 {
		t.Fatalf("quoted words in the conversation: in %.3f out %.3f", c.In, c.Out)
	}
	if len(c.Shots) != 2 || c.Shots[0].Member != "ada" || c.Shots[1].Layout != editspec.LayoutSideBySide || len(c.MutedMembers) != 1 {
		t.Fatalf("clip: %+v", c)
	}
	pin := sheet.Spec.Conversations["call"]
	if len(pin.Members) != 2 || pin.Members[1].Use != editspec.UseSound || pin.Members[1].Start != 1.5 || pin.Members[1].Drift != -12 || pin.Gate.Mode != editspec.GateSpeaking {
		t.Fatalf("pin: %+v", pin)
	}
}

func TestConversationSheetProblems(t *testing.T) {
	for _, tc := range []struct{ change, code string }{
		{"   Shot: 0:11.6 side by side\n", ""},
		{"1. talk: conversation call", "1. other: interview-a 0:00 → 0:01\n   Shot: 0:00 ada\n2. talk: conversation call"},
		{"   Shot: 0:10 ada", "   Shot: 0:10 a wide shot of everyone"},
		{"   Shot: 0:10 ada", "   Shot: soon ada"},
		{"  Member: ada \"Ada\" · interview-a · starts at 0:00", "  Member: Ada · interview-a · starts at 0:00"},
		{"· without sound from sam", "· without sound from"},
		{"· quieter when not speaking", "· quieter when nobody talks"},
	} {
		text := convSheet
		if tc.code != "" {
			text = strings.Replace(convSheet, tc.change, tc.code, 1)
		}
		_, probs := Parse(text, ParseOptions{Info: testInfo()})
		if tc.code == "" {
			if HasErrors(probs) {
				t.Fatalf("unexpected: %s", problemsText(probs))
			}
			continue
		}
		if !HasErrors(probs) {
			t.Errorf("%q: expected a problem", tc.code)
		}
	}
}

// A graphic that follows a conversation or a nested cut (the name bar, D182)
// spells its changes the way clip lines name them, and reads back.
func TestChangesFollowConversationsAndNestedCuts(t *testing.T) {
	sheetText := strings.Replace(convSheet, `from "we built it twice" to "very quickly"`, "0:09 → 0:20", 1)
	sh, probs := Parse(sheetText, ParseOptions{})
	if HasErrors(probs) {
		t.Fatal(problemsText(probs))
	}
	s := sh.Spec
	s.Overlays = []editspec.Overlay{{ID: "names", Template: editspec.TplNameBar, Span: &editspec.OverlaySpan{}, Params: json.RawMessage(`{"hide":true}`),
		Changes: []editspec.OverlayChange{{Footage: editspec.ConvKey("call"), At: 10, Params: json.RawMessage(`{"hide":false,"name":"Ada"}`)}}}}
	text := mustRoundTrip(t, "conversation changes", s, Options{})
	if !strings.Contains(text, `Change: at conversation call 0:10 · with hide false, name "Ada"`) {
		t.Fatalf("conversation change line:\n%s", text)
	}
	n := nestedSpec()
	n.Overlays = append(n.Overlays, editspec.Overlay{ID: "names", Template: editspec.TplNameBar, Span: &editspec.OverlaySpan{}, Params: json.RawMessage(`{"hide":true}`),
		Changes: []editspec.OverlayChange{{Footage: brandIntro, At: 1.5, Params: json.RawMessage(`{"hide":false,"name":"Lena"}`)}}})
	text = mustRoundTrip(t, "nested changes", n, Options{ProjectID: testProject, BaseRevisionID: testBase, Info: testLib()})
	if !strings.Contains(text, `Change: at cut from brand 0:01.5 · with hide false, name "Lena"`) {
		t.Fatalf("nested change line:\n%s", text)
	}
}
