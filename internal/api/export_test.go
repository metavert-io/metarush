package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/store"
)

// "Send to editing app": signed-in dashboard requests with the CSRF header
// only; MetaRush picks a new folder under the exports folder for the timeline
// and the footage; "Show in Finder" opens nothing outside it.
func TestSendToEditorWritesIntoItsOwnFolder(t *testing.T) {
	exports := t.TempDir()
	t.Setenv("METARUSH_EXPORTS_DIR", exports)
	d, _, rev := newDash(t, func(string, string) *fakeclaude.Script { return nil })
	ctx := context.Background()
	// Real bytes behind the clip's asset, so the footage can be copied.
	src := filepath.Join(t.TempDir(), "take.mov")
	b := make([]byte, 4096)
	_, _ = rand.Read(b)
	if err := os.WriteFile(src, b, 0o644); err != nil {
		t.Fatal(err)
	}
	sha, _ := media.HashFile(src)
	key := media.ObjectKey(d.a.WorkspaceID(), sha)
	if _, err := d.a.Media.PutFile(ctx, key, src, media.PutOptions{SHA256: sha}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.a.DB.C(store.CAssets).UpdateMany(ctx, bson.M{"workspaceId": d.a.WorkspaceID()}, bson.M{"$set": bson.M{"sha256": sha, "size": int64(len(b)), "storageKey": key}}); err != nil {
		t.Fatal(err)
	}

	in := SendToEditorIn{RevisionID: rev, App: "resolve", Footage: true}
	if code, _ := d.do("POST", "/api/export/timeline", in, true); code != 401 {
		t.Fatalf("without a session: %d", code)
	}
	d.signIn()
	if code, env := d.do("POST", "/api/export/timeline", in, false); code != 401 || env.Error.Details["reason"] != "csrf" {
		t.Fatalf("without the CSRF header: %d %+v", code, env.Error)
	}
	code, env := d.do("POST", "/api/export/timeline", in, true)
	if code != 200 {
		t.Fatalf("send: %d %+v", code, env.Error)
	}
	var out SendToEditorOut
	if err := json.Unmarshal(env.Data, &out); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(out.Folder) != exports || !strings.HasSuffix(out.Folder, "API film version 1 for DaVinci Resolve") {
		t.Fatalf("folder %q", out.Folder)
	}
	if out.Export.App != "DaVinci Resolve" || filepath.Dir(out.Export.File) != out.Folder || filepath.Ext(out.Export.File) != ".otio" {
		t.Fatalf("export %+v", out.Export)
	}
	if len(out.Export.Media) != 1 || out.Export.Media[0].Name != "take.mov" || filepath.Dir(out.Export.Media[0].Path) != filepath.Join(out.Folder, "Footage") {
		t.Fatalf("footage %+v", out.Export.Media)
	}
	got, err := os.ReadFile(out.Export.Media[0].Path)
	if err != nil || string(got) != string(b) {
		t.Fatalf("the footage copy differs: %v", err)
	}
	otio, _ := os.ReadFile(out.Export.File)
	if !strings.Contains(string(otio), `"OTIO_SCHEMA": "Timeline.1"`) || !strings.Contains(string(otio), "Footage/take.mov") {
		t.Fatal("the timeline should point at the footage copy")
	}
	// Again: a second folder, never the first one overwritten.
	code, env = d.do("POST", "/api/export/timeline", SendToEditorIn{RevisionID: rev, App: "premiere"}, true)
	var second SendToEditorOut
	_ = json.Unmarshal(env.Data, &second)
	if code != 200 || filepath.Ext(second.Export.File) != ".xml" || second.Folder == out.Folder || len(second.Export.Notes) == 0 {
		t.Fatalf("premiere: %d %+v", code, second)
	}
	// Show in Finder only opens folders MetaRush made for editing apps.
	if code, env := d.do("POST", "/api/export/reveal", ExportRevealIn{Folder: "/etc"}, true); code == 200 || env.Error.Code != "E_UNAUTHORIZED" {
		t.Fatalf("reveal outside the exports folder: %d %+v", code, env.Error)
	}
	if code, env := d.do("POST", "/api/export/reveal", ExportRevealIn{Folder: exports + "/../" + filepath.Base(exports) + "x"}, true); code == 200 {
		t.Fatalf("reveal of a look-alike folder: %d %+v", code, env.Error)
	}
	if code, env := d.do("POST", "/api/export/timeline", SendToEditorIn{RevisionID: rev, App: "avid"}, true); code == 200 || env.Error.Code != "E_INVALID_INPUT" {
		t.Fatalf("an unknown app: %d %+v", code, env.Error)
	}
}
