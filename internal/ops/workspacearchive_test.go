package ops_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

type appCopy = app.App

func archiveJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func exportCodeOf(err error) string {
	c, _ := exportCode(err)
	return c
}

func exportCode(err error) (code, reason string) {
	ae := apperr.From(err)
	if ae == nil {
		return "", ""
	}
	if d, ok := ae.Details.(map[string]any); ok {
		reason, _ = d["reason"].(string)
	}
	return ae.Code, reason
}

// Exporting the workspace is an owner's and a person's, every time: a member,
// a call that isn't a person's, the dashboard's Claude, Auto, an AI app and
// a review-desk draft are all refused. From the dashboard an owner's export
// goes into MetaRush's own archives folder (never a path the browser names),
// runs in the background, is listed with how far it is, and is recorded.
func TestWorkspaceExportIsAnOwnersAndAPersons(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	ws := a.WorkspaceID()
	if _, err := a.DB.C(store.CWorkspaces).InsertOne(ctx, bson.M{"_id": ws, "workspaceId": ws, "name": "FernBrook Studio",
		"mediaStore": bson.M{"provider": a.Media.Name(), "locator": a.Media.Locator()}}); err != nil {
		t.Fatal(err)
	}
	in := ops.WorkspaceExportIn{To: filepath.Join(t.TempDir(), "archive")}
	// Not a person.
	if _, err := ops.CallTyped[ops.WorkspaceExportOut](ctx, a, "workspace.export", in, ops.Caller{}); exportCodeOf(err) != apperr.HumanOnly {
		t.Fatalf("not a person: %v", err)
	}
	// A member, over the dashboard.
	member := ops.OverHTTP(ctx, func() bool { return false })
	for _, name := range []string{"workspace.export", "workspace.exports"} {
		var input any = in
		if name == "workspace.exports" {
			input = ops.WorkspaceExportsIn{}
		}
		_, err := ops.Call(member, a, name, archiveJSON(input), ops.Caller{Human: true, Remote: true})
		if c, r := exportCode(err); c != apperr.Unauthorized || r != "owner_only" {
			t.Fatalf("a member's %s: %v", name, err)
		}
	}
	// Claude in the dashboard, Auto, an AI app, a propose-only app.
	for what, change := range map[string]func(b *appCopy){
		"the dashboard's Claude": func(b *appCopy) { b.Actor.Via = models.ViaAssistant },
		"Auto":                   func(b *appCopy) { b.Actor.Via = "assistant-auto" },
		"an AI app":              func(b *appCopy) { b.Actor.Agent = "codex" },
		"a propose-only app":     func(b *appCopy) { b.ProposeOnly = true },
	} {
		b := *a
		change(&b)
		if _, err := ops.CallTyped[ops.WorkspaceExportOut](ctx, &b, "workspace.export", in, ops.Caller{Human: true}); err == nil {
			t.Fatalf("%s exported the workspace", what)
		}
		if _, err := ops.CallTyped[ops.WorkspaceExportsOut](ctx, &b, "workspace.exports", ops.WorkspaceExportsIn{}, ops.Caller{Human: true}); err == nil {
			t.Fatalf("%s listed the exports", what)
		}
	}
	// The review desk keeps no draft of it.
	b := *a
	b.ProposeOnly = true
	_, err := ops.CallTyped[ops.DraftView](ctx, &b, "draft.save", ops.DraftSaveIn{Op: "workspace.export", Input: archiveJSON(in)}, ops.Caller{})
	if c, _ := exportCode(err); c != apperr.HumanOnly {
		t.Fatalf("a draft of an export: %v", err)
	}
	// An owner in the dashboard: a path from the browser is refused; the
	// plan and the export go into the archives folder.
	owner := ops.OverHTTP(ctx, func() bool { return true })
	dash := ops.Caller{Human: true, Remote: true}
	if _, err := ops.Call(owner, a, "workspace.export", archiveJSON(in), dash); exportCodeOf(err) != apperr.InvalidInput {
		t.Fatalf("a path from the browser: %v", err)
	}
	plan, err := ops.Call(owner, a, "workspace.export", archiveJSON(ops.WorkspaceExportIn{Plan: true}), dash)
	if err != nil || plan.(*ops.WorkspaceExportOut).Plan == nil || !strings.Contains(plan.(*ops.WorkspaceExportOut).Plan.Path, ops.ArchivesFolder(a)) {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	if entries, _ := os.ReadDir(ops.ArchivesFolder(a)); len(entries) != 0 {
		t.Fatalf("the plan wrote: %v", entries)
	}
	got, err := ops.Call(owner, a, "workspace.export", archiveJSON(ops.WorkspaceExportIn{Background: true}), dash)
	if err != nil {
		t.Fatal(err)
	}
	started := got.(*ops.WorkspaceExportOut).Export
	if started == nil || filepath.Dir(started.Path) != ops.ArchivesFolder(a) || !strings.HasSuffix(started.Name, ".tar") {
		t.Fatalf("started: %+v", started)
	}
	var rec ops.ExportRecord
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		list, err := ops.Call(owner, a, "workspace.exports", archiveJSON(ops.WorkspaceExportsIn{}), dash)
		if err != nil {
			t.Fatal(err)
		}
		out := list.(*ops.WorkspaceExportsOut)
		if len(out.Exports) == 1 && out.Exports[0].State != "running" {
			rec = out.Exports[0]
			if out.Restore == "" || out.Holds == "" {
				t.Errorf("the words: %+v", out)
			}
			break
		}
	}
	if rec.State != "done" || rec.Documents < 1 || !strings.HasPrefix(rec.Says, "The archive is ready") {
		t.Fatalf("export: %+v", rec)
	}
	if info, err := os.Stat(rec.Path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("archive: %v %v", info, err)
	}
	if info, _ := os.Stat(ops.ArchivesFolder(a)); info.Mode().Perm() != 0o700 {
		t.Errorf("archives folder: %v", info.Mode().Perm())
	}
	a.Log.Flush(ctx)
	if n, _ := a.DB.C(store.CAudit).CountDocuments(ctx, bson.M{"workspaceId": ws, "action": "workspace.export"}); n != 1 {
		t.Errorf("the export in the change history: %d", n)
	}
	if n, _ := a.DB.C(store.CLogEntries).CountDocuments(ctx, bson.M{"workspaceId": ws, "op.name": "workspace.export"}); n < 2 {
		t.Errorf("the exports in the activity log: %d", n)
	}
}

// An export keeps out every key this computer holds, not only its
// configured ones: a provider's key and an ability's key kept in the home's
// secrets file, and a key this process already read, pasted into comments.
func TestWorkspaceExportKeepsThisComputersKeysOut(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	ws := a.WorkspaceID()
	provider := "hg" + strings.Repeat("Q4w", 11)
	ability := "ab" + strings.Repeat("Z7r", 11)
	remembered := "rm" + strings.Repeat("K2p", 11)
	env := "METARUSH_HEYGEN_API_KEY=" + provider + "\nMETARUSH_STOCKPICS_API_KEY=" + ability + "\n"
	if err := os.WriteFile(filepath.Join(a.Cfg.Home(), ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	logbook.RememberSecret(remembered)
	for coll, doc := range map[string]bson.M{
		store.CWorkspaces: {"_id": ws, "workspaceId": ws, "name": "FernBrook Studio", "mediaStore": bson.M{"provider": a.Media.Name(), "locator": a.Media.Locator()}},
		store.CAbilities:  {"_id": ws + "/" + strings.Repeat("a", 64), "workspaceId": ws, "name": "stock-pictures", "facts": bson.M{"keys": bson.A{bson.M{"name": "stockpics", "env": "STOCK_KEY"}}}},
		store.CComments:   {"_id": "cmt_keys", "workspaceId": ws, "text": "keys: " + provider + " / " + ability + " / " + remembered},
	} {
		if _, err := a.DB.C(coll).InsertOne(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	to := filepath.Join(t.TempDir(), "archive")
	out, err := ops.CallTyped[ops.WorkspaceExportOut](ctx, a, "workspace.export", ops.WorkspaceExportIn{To: to}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(to, "collections", store.CComments+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for what, key := range map[string]string{"the provider's key": provider, "the ability's key": ability, "a key this process read": remembered} {
		if strings.Contains(string(b), key) {
			t.Errorf("%s is in the archive", what)
		}
	}
	if !strings.Contains(out.Export.Says, "taken out of 1 record: 1 comment") {
		t.Errorf("the export should say what it took out: %q", out.Export.Says)
	}
}

// YouTube's client secret and each person's sign-in to it (kept as JSON in
// the home's secrets file) stay out of an archive too.
func TestWorkspaceExportKeepsPublishingSignInsOut(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	ws := a.WorkspaceID()
	clientSecret := "client" + strings.Repeat("Sx9", 8)
	refresh := "refresh" + strings.Repeat("Rt4", 8)
	env := `METARUSH_PUBLISH_YOUTUBE_CLIENT={"id":"cid.apps.example","secret":"` + clientSecret + `"}` + "\n" +
		`METARUSH_PUBLISH_YOUTUBE_PERSON_` + strings.ToUpper(a.Cfg.User.ID) + `={"refresh":"` + refresh + `","account":{"name":"Maya"}}` + "\n"
	if err := os.WriteFile(filepath.Join(a.Cfg.Home(), ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	a.Cfg.Secrets = "env"
	for coll, doc := range map[string]bson.M{
		store.CWorkspaces: {"_id": ws, "workspaceId": ws, "name": "FernBrook Studio", "mediaStore": bson.M{"provider": a.Media.Name(), "locator": a.Media.Locator()}},
		store.CComments:   {"_id": "cmt_yt", "workspaceId": ws, "text": "secret " + clientSecret + " and " + refresh},
	} {
		if _, err := a.DB.C(coll).InsertOne(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	to := filepath.Join(t.TempDir(), "archive")
	if _, err := ops.CallTyped[ops.WorkspaceExportOut](ctx, a, "workspace.export", ops.WorkspaceExportIn{To: to}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(to, "collections", store.CComments+".jsonl"))
	if strings.Contains(string(b), clientSecret) || strings.Contains(string(b), refresh) {
		t.Fatalf("a publishing secret is in the archive: %s", b)
	}
}
