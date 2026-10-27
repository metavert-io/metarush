package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
)

// Listing the caption looks is Claude's own; a look for the cut is a card,
// checked when it is made, that saves a new version with the person's click
// (D245), and that Auto may run.
func TestCaptionLookRequestsBecomeCards(t *testing.T) {
	a := testApp(t, "")
	projectID, _, _ := fixture(t, a)
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
	if call := parse("metarush captions looks --json"); !readOnlyCall(call) {
		t.Fatal("listing the looks is Claude's own")
	}
	call := parse("metarush cut captions " + projectID + " --look bold --on --words-per-line 2 --json")
	if readOnlyCall(call) {
		t.Fatal("changing the captions is refused for Claude")
	}
	act := &AssistantAction{Kind: call.Path}
	m.describeAction(ctx, nil, &call, act)
	var in ops.CutCaptionsIn
	_ = json.Unmarshal([]byte(act.Input), &in)
	if act.Op != "cut.captions" || in.Look != "bold" || in.Words != 2 || in.On == nil || !*in.On || in.DryRun || !strings.Contains(act.Title, "“Big and bold”") || act.Check == nil {
		t.Fatalf("card: %+v (%s)", act, act.Input)
	}
	if !savesVersion(act.Op) || !autoOps[act.Op] {
		t.Fatal("the card saves a version, and Auto may run it")
	}
	for _, s := range append([]string{act.Title}, act.Details...) {
		if err := plainwords.CheckPanel(s); err != nil {
			t.Errorf("card text %q: %v", s, err)
		}
		if strings.Contains(s, "prj_") || strings.Contains(s, "--") {
			t.Errorf("card text %q shows an ID or a flag", s)
		}
	}
	bad := parse("metarush cut captions " + projectID + " --look glitter")
	none := &AssistantAction{Kind: bad.Path}
	m.describeAction(ctx, nil, &bad, none)
	if none.Op != "" || !strings.Contains(none.Unavailable, "glitter") {
		t.Fatalf("an unknown look: %+v", none)
	}
}
