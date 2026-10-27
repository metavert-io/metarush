package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/fixes"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/sources"
	"github.com/metavert-io/metarush/internal/store"
)

// Names, version notes and fixes asked for in the Claude Code panel become
// cards with plain titles and the op the CLI runs (D150–D152); reading a
// source's card, its fixes and the better names waiting stays Claude's own.
func TestSourceRequestsBecomeCards(t *testing.T) {
	a := testApp(t, "")
	projectID, assetID, headID := fixture(t, a)
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
			for _, bad := range []string{"rev_", "ast_", "prj_", "--", "sha256"} {
				if strings.Contains(s, bad) {
					t.Errorf("%s: card text %q has %q", command, s, bad)
				}
			}
		}
		return act
	}
	// Reading stays Claude's own.
	for _, cmd := range []string{"metarush asset card " + assetID, "metarush asset fixes " + assetID + " --json", "metarush asset names --project " + projectID} {
		words, _ := splitCommand(cmd)
		call, _ := parseMetarush(words)
		if !readOnlyCall(call) {
			t.Errorf("%s should be read-only for Claude", cmd)
		}
	}

	// A clip's name, typed or MetaRush's suggestion; its file name stays.
	ren := card("metarush asset update " + assetID + " --title 'Kitchen chat with Lena'")
	if ren.Op != "asset.update" || ren.Title != "Call “interview” “Kitchen chat with Lena”" || !strings.Contains(strings.Join(ren.Details, "|"), "“interview.mov”, stays on its info card") {
		t.Fatalf("rename card: %+v", ren)
	}
	if none := card("metarush asset update " + assetID + " --suggested"); none.State != "" || !strings.Contains(none.Unavailable, "no better name") {
		t.Fatalf("a suggestion that isn't there became a card: %+v", none)
	}
	sug := &models.NameSuggestion{Title: "Lena talks about the garden (30 s)", Why: "From who speaks and what about.", Basis: []string{"people", "topic"}, At: time.Now().UTC()}
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": assetID}, bson.M{"$set": bson.M{"nameSuggestion": sug}}); err != nil {
		t.Fatal(err)
	}
	take := card("metarush asset update " + assetID + " --suggested")
	var ui ops.AssetUpdateIn
	_ = json.Unmarshal([]byte(take.Input), &ui)
	if take.Op != "asset.update" || !ui.Suggested || ui.Title != sug.Title || !strings.Contains(take.Title, "“Lena talks about the garden (30 s)”") {
		t.Fatalf("suggestion card: %+v (%s)", take, take.Input)
	}
	all := card("metarush asset names --accept-all --project " + projectID)
	var ni sources.NamesAcceptIn
	_ = json.Unmarshal([]byte(all.Input), &ni)
	if all.Op != "asset.names.accept" || len(ni.AssetIDs) != 1 || ni.AssetIDs[0] != assetID || !strings.Contains(all.Title, "“Lena talks about the garden (30 s)”") {
		t.Fatalf("names card: %+v (%s)", all, all.Input)
	}

	// Fixes: ticked in plain words; the original is kept.
	fx := card("metarush asset fixes " + assetID + " --tick steady,hum")
	var si fixes.SetIn
	_ = json.Unmarshal([]byte(fx.Input), &si)
	if fx.Op != "asset.fixes.set" || fx.Title != "Steady the camera and remove hum in “interview”" || len(si.Tick) != 2 ||
		!strings.Contains(strings.Join(fx.Details, "|"), "the original is kept") || !strings.Contains(strings.Join(fx.Details, "|"), "listens to the words") {
		t.Fatalf("fixes card: %+v (%s)", fx, fx.Input)
	}
	if back := card("metarush asset fixes " + assetID + " --none"); back.Title != "Play the original of “interview” again" {
		t.Fatalf("back to the original: %+v", back)
	}
	if off := card("metarush asset fixes " + assetID + " --untick steady"); off.Title != "Stop steadying the camera in “interview”" {
		t.Fatalf("untick: %+v", off)
	}
	if look := card("metarush asset fixes " + assetID + " --look"); look.Op != "asset.fixes.look" {
		t.Fatalf("measure again: %+v", look)
	}
	if bad := card("metarush asset fixes " + assetID + " --tick wobble"); bad.State != "" {
		t.Fatalf("an unknown fix became a card: %+v", bad)
	}

	// The cut's name and a version's note.
	cr := card("metarush cut rename " + projectID + " 'Main open day video'")
	if cr.Op != "cut.rename" || cr.Title != "Call the cut “Main open day video”" {
		t.Fatalf("cut rename card: %+v", cr)
	}
	note := card("metarush revision note " + headID + " 'flicker fixes and headline cards'")
	if note.Op != "revision.note" || note.Title != "Note on version 1: “flicker fixes and headline cards”" {
		t.Fatalf("note card: %+v", note)
	}
	// Run the note and the cut name as the person; the panel then names them.
	dash := *a
	dash.Actor.Via = "dashboard"
	if _, err := ops.Call(ctx, &dash, note.Op, json.RawMessage(note.Input), ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.Call(ctx, &dash, cr.Op, json.RawMessage(cr.Input), ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	m2 := &Manager{App: a, names: map[string]string{}}
	if got := m2.displayName(ctx, headID); got != "version 1, “flicker fixes and headline cards”" {
		t.Fatalf("version name %q", got)
	}
	if got := m2.cutName(ctx, projectID, "main"); got != "Main open day video" {
		t.Fatalf("cut name %q", got)
	}

	// Using a fixed copy in the cut: checked now, saved as a new version on a click.
	sha := strings.Repeat("cd", 32)
	now := time.Now().UTC()
	cp := models.SourceCopy{ID: "cpy_test", WorkspaceID: a.WorkspaceID(), AssetID: assetID, SourceSHA256: strings.Repeat("ab", 32), Fingerprint: "r1",
		FixSteps: models.FixSteps{Version: 1, Kind: models.SourceCamera, Steps: []models.FixStep{{Fix: models.FixSteady, Engine: "vidstab"}}},
		State:    models.FixReady, SHA256: sha, StorageKey: "k2", Size: 900, MIME: "video/mp4", CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CSourceCopies, cp); err != nil {
		t.Fatal(err)
	}
	fixed := models.SourceFixes{Kind: models.SourceCamera, Ticked: []string{models.FixSteady}, Fingerprint: "r1", State: models.FixReady,
		Copy: &models.FixedCopyRef{ID: cp.ID, SHA256: sha, StorageKey: "k2", Size: 900, MIME: "video/mp4", Fingerprint: "r1", Fixes: []string{models.FixSteady}, At: now}}
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": assetID}, bson.M{"$set": bson.M{"fixes": fixed}}); err != nil {
		t.Fatal(err)
	}
	use := card("metarush cut use-fixed " + projectID + " " + assetID)
	if use.Op != "cut.useFixed" || use.Title != "Use the steadier picture of “interview” in “Main open day video”" || use.Check == nil || !use.Check.Valid || !savesVersion(use.Op) {
		t.Fatalf("use-fixed card: %+v", use)
	}
	res, err := ops.Call(ctx, &dash, use.Op, json.RawMessage(use.Input), ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if r := res.(*ops.CutEditOut); r.Applied == nil || r.Applied.Revision.Number != 2 {
		t.Fatalf("use-fixed ran: %+v", r)
	}
	var p models.Project
	_ = a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), projectID, &p)
	var head models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), p.Branches["main"].Head, &head)
	if head.Spec.FixedSHA(assetID) != sha {
		t.Fatalf("the new version doesn't play the fixed copy: %+v", head.Spec.Fixed)
	}
	var first models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), headID, &first)
	if first.Spec.FixedSHA(assetID) != "" {
		t.Fatal("the saved version changed")
	}
}
