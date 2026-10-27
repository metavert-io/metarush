package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ops"
)

// Pictures from a repository's folders (D216) in the Claude Code panel: asking
// to keep (or stop keeping) a folder's pictures, or to bring a repository in
// with them, becomes a card that runs the same op as the CLI; listing them
// needs no card, and a folder MetaRush can't keep from makes none.
func TestRepositoryPictureRequestsBecomeCards(t *testing.T) {
	a := testApp(t, "")
	m := &Manager{App: a, names: map[string]string{}}
	ctx := context.Background()
	prj := ids.New(ids.Project)
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
		if label := resourceLabel(&call); label == "" {
			t.Fatalf("%s: no step label", command)
		}
		return act
	}
	set := func(act *AssistantAction) ops.RepoPicturesSetIn {
		t.Helper()
		var in ops.RepoPicturesSetIn
		if act.Op != "resources.pictures.set" || json.Unmarshal([]byte(act.Input), &in) != nil {
			t.Fatalf("not a pictures card: %+v", act)
		}
		return in
	}
	act := card("metarush resources pictures widget --keep art/characters/ --project " + prj + " --json")
	in := set(act)
	if act.Title != "Keep the pictures in “art/characters” of “widget”" || strings.Join(in.Keep, ",") != "art/characters" || in.Ref != "widget" || in.ProjectID != prj ||
		!strings.Contains(strings.Join(act.Details, " | "), "nothing from it ever runs") {
		t.Fatalf("keep: %+v %+v", act, in)
	}
	act = card("metarush resources pictures widget --drop art/old --json")
	if in = set(act); act.Title != "Stop keeping “art/old” of “widget” in step" || strings.Join(in.Drop, ",") != "art/old" || len(in.Keep) != 0 {
		t.Fatalf("drop: %+v %+v", act, in)
	}
	act = card("metarush resources pictures widget --keep art/a --keep art/b,. --drop old --json")
	if in = set(act); act.Title != "Change which folders of “widget” it keeps pictures from" || strings.Join(in.Keep, ",") != "art/a,art/b,." || strings.Join(in.Drop, ",") != "old" {
		t.Fatalf("both: %+v %+v", act, in)
	}
	if act = card("metarush resources pictures widget --json"); act.State != ActionUnavailable || act.Op != "" {
		t.Fatalf("listing needs no card: %+v", act)
	}
	for _, cmd := range []string{
		"metarush resources pictures widget --keep ../outside --json",
		"metarush resources pictures widget --keep art --project Teaser --json", // a project by name
		"metarush resources add https://acme.example --pictures art --json",     // a page keeps no folders
		"metarush resources add https://github.com/acme/widget --pictures .git/hooks --json",
	} {
		if act := card(cmd); act.State != "" || act.Op != "" {
			t.Fatalf("%s became a card: %+v", cmd, act)
		}
	}
	act = card("metarush resources add https://github.com/acme/widget --pictures art/characters --project " + prj + " --json")
	var fi ops.ResourceFetchIn
	if act.Op != "resources.fetch" || json.Unmarshal([]byte(act.Input), &fi) != nil || strings.Join(fi.Pictures, ",") != "art/characters" ||
		!strings.Contains(strings.Join(act.Details, " | "), "the pictures in “art/characters” as pictures of their own") {
		t.Fatalf("a repository with its pictures: %+v %+v", act, fi)
	}
	if !autoOps["resources.pictures.set"] {
		t.Fatal("keeping a folder's pictures is undone like a fetch: Auto runs it")
	}
}
