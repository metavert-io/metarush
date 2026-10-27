package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
)

// Asking which language a clip is spoken in is Claude's own; setting it is a
// card that names the language and says the words are written down again
// (D243).
func TestLanguageRequestsBecomeCards(t *testing.T) {
	a := testApp(t, "")
	_, assetID, _ := fixture(t, a)
	ctx := context.Background()
	m := &Manager{App: a, names: map[string]string{}}
	parse := func(command string) cliCall {
		t.Helper()
		words, err := splitCommand(command)
		if err != nil {
			t.Fatal(err)
		}
		call, ok := parseMetarush(words)
		if !ok {
			t.Fatalf("not a metarush command: %s", command)
		}
		return call
	}
	if call := parse("metarush footage language " + assetID + " --json"); !readOnlyCall(call) {
		t.Fatal("reading a clip's language is Claude's own")
	}
	call := parse("metarush footage language " + assetID + " --set Spanish --json")
	if readOnlyCall(call) {
		t.Fatal("setting a clip's language is refused for Claude")
	}
	act := &AssistantAction{Kind: call.Path}
	m.describeAction(ctx, nil, &call, act)
	var in ops.FootageLanguageSetIn
	_ = json.Unmarshal([]byte(act.Input), &in)
	if act.Op != "footage.language.set" || in.AssetID != assetID || in.Language != "es" || !strings.Contains(act.Title, "is spoken in Spanish") {
		t.Fatalf("card: %+v (%s)", act, act.Input)
	}
	for _, s := range append([]string{act.Title}, act.Details...) {
		if err := plainwords.CheckPanel(s); err != nil {
			t.Errorf("card text %q: %v", s, err)
		}
		if strings.Contains(s, "ast_") || strings.Contains(s, "--") {
			t.Errorf("card text %q shows an ID or a flag", s)
		}
	}
	bad := parse("metarush footage language " + assetID + " --set Klingon")
	none := &AssistantAction{Kind: bad.Path}
	m.describeAction(ctx, nil, &bad, none)
	if none.Op != "" || !strings.Contains(none.Unavailable, "language MetaRush knows") {
		t.Fatalf("an unknown language: %+v", none)
	}
}
