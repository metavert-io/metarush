package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/store"
)

// A timeline made for an editing app is sent to a browser on another
// computer as a .zip of its folder — only folders MetaRush made for editing
// apps, plain files only (a link put in the folder stays behind); a request
// MetaRush can't do is refused plainly; folder names are safe on every disk.
func TestATimelineFolderIsSentAsAZip(t *testing.T) {
	exports := t.TempDir()
	t.Setenv("METARUSH_EXPORTS_DIR", exports)
	d, _, rev := newDash(t, noClaude)
	d.signIn()
	// Real bytes behind the clip, as a timeline reads them.
	ctx := context.Background()
	src := filepath.Join(t.TempDir(), "take.mov")
	if err := os.WriteFile(src, bytes.Repeat([]byte("take bytes "), 400), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, _ := media.HashFile(src)
	key := media.ObjectKey(d.a.WorkspaceID(), sha)
	if _, err := d.a.Media.PutFile(ctx, key, src, media.PutOptions{SHA256: sha}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.a.DB.C(store.CAssets).UpdateMany(ctx, bson.M{"workspaceId": d.a.WorkspaceID()}, bson.M{"$set": bson.M{"sha256": sha, "size": int64(4400), "storageKey": key}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		in   any
		code int
		says string
	}{
		{"an app MetaRush doesn't know", SendToEditorIn{RevisionID: rev, App: "imovie"}, http.StatusBadRequest, "Choose DaVinci Resolve, Final Cut Pro or Premiere Pro."},
		{"a version that isn't there", SendToEditorIn{RevisionID: "rev_00000000000000000000000000", App: "premiere"}, http.StatusNotFound, "can't find that version"},
	} {
		if code, env := d.do("POST", "/api/export/timeline", c.in, true); code != c.code || !strings.Contains(env.Error.Message, c.says) {
			t.Errorf("%s: %d %+v", c.name, code, env.Error)
		}
	}
	if code, _, b := d.send("POST", "/api/export/timeline", []byte("{not json"), nil, true); code != http.StatusBadRequest || !strings.Contains(envOf(t, b).Error.Message, "couldn't read the request") {
		t.Fatalf("not JSON: %d %s", code, b)
	}
	code, env := d.do("POST", "/api/export/timeline", SendToEditorIn{RevisionID: rev, App: "finalcut"}, true)
	if code != 200 {
		t.Fatalf("send: %d %+v", code, env.Error)
	}
	var out SendToEditorOut
	if err := json.Unmarshal(env.Data, &out); err != nil {
		t.Fatal(err)
	}
	// The same again: a second folder, never over the first.
	code, env = d.do("POST", "/api/export/timeline", SendToEditorIn{RevisionID: rev, App: "finalcut"}, true)
	var second SendToEditorOut
	_ = json.Unmarshal(env.Data, &second)
	if code != 200 || second.Folder != out.Folder+" 2" {
		t.Fatalf("a second timeline: %d %q (first %q)", code, second.Folder, out.Folder)
	}
	// Something that isn't a plain file stays behind.
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("not for the zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(out.Folder, "link.txt")); err != nil {
		t.Fatal(err)
	}
	code, h, b := d.send("GET", "/api/export/download?folder="+url.QueryEscape(out.Folder), nil, nil, false)
	if code != 200 || h.Get("Content-Type") != "application/zip" || !strings.Contains(h.Get("Content-Disposition"), "for Final Cut Pro.zip") || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("download: %d %v", code, h)
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		if strings.Contains(f.Name, "link.txt") {
			t.Fatalf("a link went into the zip: %s", f.Name)
		}
	}
	sort.Strings(names)
	base := filepath.Base(out.Folder)
	if len(names) == 0 || !strings.HasPrefix(names[0], base+"/") || !strings.HasSuffix(strings.Join(names, " "), ".fcpxml") {
		t.Fatalf("zip %q", names)
	}
	for _, c := range []struct {
		folder string
		code   int
	}{
		{filepath.Dir(exports), http.StatusUnauthorized},
		{filepath.Join(out.Folder, "inner"), http.StatusUnauthorized},
		{secret, http.StatusUnauthorized},
		{filepath.Join(exports, "never made"), http.StatusNotFound},
	} {
		if code, _, b := d.send("GET", "/api/export/download?folder="+url.QueryEscape(c.folder), nil, nil, false); code != c.code {
			t.Errorf("download %s: %d %s", c.folder, code, b)
		}
	}
	if code, _, b := d.send("POST", "/api/export/reveal", []byte(`{"folder":"`+filepath.Dir(exports)+`"}`), nil, true); code != http.StatusUnauthorized ||
		!strings.Contains(envOf(t, b).Error.Message, "only opens the folders it made") {
		t.Fatalf("reveal outside: %d %s", code, b)
	}
	if code, _, _ := d.send("POST", "/api/export/reveal", []byte(`{`), nil, true); code != http.StatusBadRequest {
		t.Fatalf("reveal of nothing: %d", code)
	}

	for _, c := range []struct{ in, want string }{
		{`Pond: "take" 1/2 <final>?`, `Pond- -take- 1-2 -final--`},
		{"tab\there\x01", "tabhere"},
		{"  ...hidden.  ", "hidden"},
		{"", "Timeline"},
		{"....", "Timeline"},
		{strings.Repeat("x", 150), strings.Repeat("x", 100)},
	} {
		if got := safeFolderName(c.in); got != c.want {
			t.Errorf("safeFolderName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
