package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
)

// A short from any footage asked for in the panel is a card checked when it
// is made (what it plays, for phones, the crop, captions), made by the
// person's click as a new cut (D246); several stretches come with --piece.
func TestFootageShortRequestsBecomeCards(t *testing.T) {
	a := testApp(t, "")
	projectID, assetID, _ := fixture(t, a)
	ctx := context.Background()
	m := &Manager{App: a, names: map[string]string{}}
	card := func(command string) *AssistantAction {
		t.Helper()
		words, err := splitCommand(command)
		if err != nil {
			t.Fatal(err)
		}
		call, ok := parseMetarush(words)
		if !ok {
			t.Fatalf("not a metarush command: %s", command)
		}
		if readOnlyCall(call) {
			t.Fatalf("%s should be refused for Claude", command)
		}
		act := &AssistantAction{Kind: call.Path}
		m.describeAction(ctx, nil, &call, act)
		for _, s := range append([]string{act.Title}, act.Details...) {
			if err := plainwords.CheckPanel(s); err != nil {
				t.Errorf("%s: card text %q: %v", command, s, err)
			}
			if strings.Contains(s, "ast_") || strings.Contains(s, "prj_") || strings.Contains(s, "--") {
				t.Errorf("card text %q shows an ID or a flag", s)
			}
		}
		return act
	}
	act := card("metarush footage short " + projectID + " --clip " + assetID + " --from 0:02 --to 0:20 --name 'The kitchen' --why 'A strong first line' --json")
	var in ops.FootageShortIn
	_ = json.Unmarshal([]byte(act.Input), &in)
	if act.Op != "footage.short" || len(in.Pieces) != 1 || in.Pieces[0].From != 2 || in.Pieces[0].To != 20 || in.Name != "The kitchen" || in.DryRun ||
		!strings.Contains(act.Title, "“The kitchen”") || !strings.Contains(strings.Join(act.Details, "|"), "for phones (tall)") || act.Check == nil {
		t.Fatalf("card: %+v (%s)", act, act.Input)
	}
	c := &conv{}
	pending := func(a *AssistantAction) *AssistantAction { x := *a; x.State = ActionPending; return &x }
	if !savesVersion(act.Op) || !autoOps[act.Op] || !c.autoStep(pending(act)) {
		t.Fatal("the card saves a version, and Auto may run it")
	}
	// Following movement is a person's choice (D115): a short that asks for it waits for a click.
	follow := card("metarush footage short " + projectID + " --clip " + assetID + " --from 0:02 --to 0:20 --framing follow")
	if follow.Op != "footage.short" || c.autoStep(pending(follow)) {
		t.Fatalf("a short that follows runs by itself under Auto: %+v", follow)
	}
	two := card("metarush footage short " + projectID + " --piece " + assetID + "@0:01-0:05 --piece " + assetID + "@0:10-0:14 --no-captions")
	_ = json.Unmarshal([]byte(two.Input), &in)
	if two.Op != "footage.short" || len(in.Pieces) != 2 || in.Captions == nil || *in.Captions {
		t.Fatalf("two stretches: %+v (%s)", two, two.Input)
	}
	if bad := card("metarush footage short " + projectID + " --clip " + assetID + " --from 0:02"); bad.Op != "" || bad.Unavailable == "" {
		t.Fatalf("no end: %+v", bad)
	}
}
