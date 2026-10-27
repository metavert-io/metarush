package assistant

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/metavert-io/metarush/internal/ops"
)

// Filing requests from the Claude Code panel become cards with plain titles and
// the same op the CLI runs; deleting for good never does (the person does it in
// the Library); a request without clip IDs is not a card.
func TestLibraryRequestsBecomeCards(t *testing.T) {
	a := testApp(t, "")
	projectID, assetID, _ := fixture(t, a)
	m := &Manager{App: a, names: map[string]string{}}
	ctx := context.Background()
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
		act := &AssistantAction{Kind: call.Path}
		m.describeAction(ctx, nil, &call, act)
		return act
	}
	title := m.nameOf(ctx, assetID)
	for _, c := range []struct{ cmd, op, title string }{
		{"metarush asset archive " + assetID, "asset.archive", "Archive “" + title + "”"},
		{"metarush asset trash " + assetID + " --project " + projectID, "asset.trash", "Move “" + title + "” to the Trash"},
		{"metarush asset restore " + assetID, "asset.restore", "Restore “" + title + "” from the Trash"},
		{"metarush collection add B-roll " + assetID + " --create", "collection.add", "Put “" + title + "” on “B-roll”"},
		{"metarush collection create Family", "collection.create", "Create the collection “Family”"},
	} {
		act := card(c.cmd)
		if act.State != ActionPending && act.State != "" || act.Op != c.op || act.Title != c.title {
			t.Fatalf("%s: %+v", c.cmd, act)
		}
	}
	// The input is built here, with the project the clip leaves.
	tr := card("metarush asset trash " + assetID + " --project " + projectID)
	var in ops.LibraryActionIn
	if err := json.Unmarshal([]byte(tr.Input), &in); err != nil || in.ProjectID != projectID || len(in.AssetIDs) != 1 {
		t.Fatalf("trash input %q %v", tr.Input, err)
	}
	if add := card("metarush collection add B-roll " + assetID + " --create"); add.Input == "" {
		t.Fatal("no input for collection add")
	} else {
		var ci ops.CollectionItemsIn
		_ = json.Unmarshal([]byte(add.Input), &ci)
		if !ci.Create || ci.Collection != "B-roll" {
			t.Fatalf("collection add input %+v", ci)
		}
	}
	if del := card("metarush trash empty"); del.State != ActionUnavailable || del.Op != "" {
		t.Fatalf("emptying the Trash became a runnable card: %+v", del)
	}
	if del := card("metarush asset delete " + assetID + " --forever"); del.State != ActionUnavailable || del.Op != "" {
		t.Fatalf("deleting for good became a runnable card: %+v", del)
	}
	if bad := card("metarush asset archive Kitchen"); bad.State != "" || bad.Op != "" {
		t.Fatalf("a request without clip IDs became a card: %+v", bad)
	}
}
