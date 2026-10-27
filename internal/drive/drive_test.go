package drive

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/testutil/fakerclone"
	"github.com/metavert-io/metarush/internal/tools"
)

func TestMain(m *testing.M) {
	fakerclone.MaybeRun()
	os.Exit(m.Run())
}

// drives makes a fake rclone with a Google Drive "gdrive" (a folder Art with
// two pictures and a clip) and an S3 remote "photos".
func drives(t *testing.T, st fakerclone.State) (*tools.Set, string, string) {
	t.Helper()
	root := t.TempDir()
	art := filepath.Join(root, "Teaser", "Art")
	if err := os.MkdirAll(art, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	for i, f := range []struct{ name, body string }{{"alice.png", "\x89PNG alice"}, {"bob.png", "\x89PNG bob"}, {"walk.mp4", "a clip"}} {
		p := filepath.Join(art, f.name)
		if err := os.WriteFile(p, []byte(f.body), 0o644); err != nil {
			t.Fatal(err)
		}
		at := old.Add(time.Duration(i) * time.Minute)
		_ = os.Chtimes(p, at, at)
	}
	if st.Remotes == nil {
		st.Remotes = []fakerclone.Remote{{Name: "gdrive", Type: "drive", Root: root}, {Name: "photos", Type: "s3", Root: t.TempDir()}}
	}
	tool, state := fakerclone.Write(t, st)
	log := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("METARUSH_FAKE_RCLONE_LOG", log)
	return tools.New(config.Tools{Rclone: tool}), state, log
}

func code(err error) string {
	if err == nil {
		return ""
	}
	return apperr.From(err).Code
}

func TestParseFolder(t *testing.T) {
	for in, want := range map[string]Folder{
		"gdrive:Teaser/Art":          {Remote: "gdrive", Path: "Teaser/Art"},
		" gdrive:/Teaser/Art/ ":      {Remote: "gdrive", Path: "Teaser/Art"},
		"my drive:Shared/Ein Ordner": {Remote: "my drive", Path: "Shared/Ein Ordner"},
		"gdrive:":                    {Remote: "gdrive"},
		"gdrive:a:b":                 {Remote: "gdrive", Path: "a:b"},
	} {
		got, err := ParseFolder(in)
		if err != nil || got != want {
			t.Errorf("ParseFolder(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "Teaser/Art", ":local:/etc", ":drive:x", "-v:x", " :x", "gdrive,root_folder_id=x:y", "gdrive:../secret", "gdrive:a/./b", "gdrive:a//b", "gdrive:a\x00b", "gdrive:a\nb", "tail :x"} {
		if _, err := ParseFolder(bad); err == nil {
			t.Errorf("ParseFolder(%q) should be refused", bad)
		}
	}
}

// rclone's list of remotes, with the descriptions newer versions print after the type.
func TestParseRemotes(t *testing.T) {
	got := parseRemotes([]byte("gdrive:      drive  Work: the shared one\nmy drive:    drive\nphotos:      s3\n\nbroken line\n"))
	if len(got) != 3 || got[0] != (Remote{Name: "gdrive", Type: "drive"}) || got[1] != (Remote{Name: "my drive", Type: "drive"}) || got[2] != (Remote{Name: "photos", Type: "s3"}) {
		t.Fatalf("%+v", got)
	}
}

func TestCheckSaysHowToSetItUp(t *testing.T) {
	ctx := context.Background()
	// Not installed: the plain steps and the command that installs it.
	st := Check(ctx, tools.New(config.Tools{Rclone: filepath.Join(t.TempDir(), "rclone")}))
	if st.Installed || st.Problem == "" || !strings.Contains(st.Fix, "rclone's own setup") || st.Command != "brew install rclone" {
		t.Fatalf("not installed: %+v", st)
	}
	// Installed without a Google Drive.
	ts, _, _ := drives(t, fakerclone.State{Remotes: []fakerclone.Remote{{Name: "photos", Type: "s3", Root: t.TempDir()}}})
	st = Check(ctx, ts)
	if !st.Installed || st.Version != fakerclone.Version || st.Ready() || st.Command != "rclone config" || len(st.Remotes) != 1 {
		t.Fatalf("no drive: %+v", st)
	}
	// Ready.
	ts, _, _ = drives(t, fakerclone.State{})
	st = Check(ctx, ts)
	if !st.Ready() || strings.Join(st.Drives, ",") != "gdrive" || st.Problem != "" {
		t.Fatalf("ready: %+v", st)
	}
	// Settings locked with a password: said plainly, never asked for.
	ts, _, _ = drives(t, fakerclone.State{Locked: true})
	if st = Check(ctx, ts); st.Ready() || !strings.Contains(st.Problem, "locked with a password") {
		t.Fatalf("locked: %+v", st)
	}
}

// Under go test the person's real rclone (their Google account) never runs.
func TestRealRcloneIsRefusedInTests(t *testing.T) {
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.Rclone) {
		t.Skip("no rclone on this computer")
	}
	if st := Check(context.Background(), ts); st.Problem != "MetaRush can't use rclone here." {
		t.Fatalf("the real rclone ran: %+v", st)
	}
	if _, err := List(context.Background(), ts, Folder{Remote: "gdrive", Path: "x"}); err == nil {
		t.Fatal("listing with the real rclone")
	}
}

func TestListAndFetch(t *testing.T) {
	ctx := context.Background()
	ts, _, log := drives(t, fakerclone.State{Docs: []string{"Notes"}})
	f, _ := ParseFolder("gdrive:Teaser/Art")
	files, err := List(ctx, ts, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("Google documents and folders are left out: %+v", files)
	}
	if files[0].Name != "walk.mp4" || files[2].Name != "alice.png" {
		t.Fatalf("newest first: %+v", files)
	}
	alice := files[2]
	if alice.ID != fakerclone.ID("gdrive", "Teaser/Art/alice.png") || alice.SHA256 == "" || alice.MD5 == "" || alice.Checksum() != "sha256:"+alice.SHA256 || !alice.Picture() || alice.Footage() {
		t.Fatalf("alice: %+v", alice)
	}
	if !files[0].Footage() || files[0].Picture() {
		t.Fatalf("the clip: %+v", files[0])
	}
	scratch := t.TempDir()
	got, err := Fetch(ctx, ts, f, alice, scratch, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(got.Path); string(b) != "\x89PNG alice" || got.SHA256 != alice.SHA256 || got.Size != alice.Size {
		t.Fatalf("fetched %+v %q", got, b)
	}
	if filepath.Dir(filepath.Dir(got.Path)) != scratch {
		t.Fatalf("fetched outside scratch: %s", got.Path)
	}
	// Too large for what a Drive folder may give.
	if _, err := Fetch(ctx, ts, f, alice, scratch, 4); code(err) != apperr.InvalidInput {
		t.Fatalf("size cap: %v", err)
	}
	// Changed since it was listed: refused.
	if err := os.WriteFile(filepath.Join(filepathRoot(t, ts), "Teaser", "Art", "alice.png"), []byte("\x89PNG ALICE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Fetch(ctx, ts, f, alice, scratch, 1<<20); code(err) != apperr.Conflict {
		t.Fatalf("a changed file: %v", err)
	}
	// Every call names its arguments one by one, never asks for a password,
	// and only reads (version, the remotes, a listing, a copy by ID).
	fh, err := os.Open(log)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	n := 0
	for sc.Scan() {
		var args []string
		if err := json.Unmarshal(sc.Bytes(), &args); err != nil {
			t.Fatal(err)
		}
		n++
		if args[0] != "--ask-password=false" {
			t.Fatalf("asks for a password: %v", args)
		}
		cmd := args[3]
		if cmd != "version" && cmd != "listremotes" && cmd != "lsjson" && cmd != "backend" {
			t.Fatalf("an unexpected command: %v", args)
		}
	}
	if n < 5 {
		t.Fatalf("calls: %d", n)
	}
}

// filepathRoot is the fake's gdrive folder.
func filepathRoot(t *testing.T, ts *tools.Set) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(filepath.Dir(ts.Path(tools.Rclone)), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var st fakerclone.State
	_ = json.Unmarshal(b, &st)
	return st.Remotes[0].Root
}

func TestPlainProblems(t *testing.T) {
	ctx := context.Background()
	ts, state, _ := drives(t, fakerclone.State{})
	// A remote that isn't there, one that isn't a Google Drive, a folder that isn't there.
	if _, err := List(ctx, ts, Folder{Remote: "work", Path: "Art"}); code(err) != apperr.NotConfigured || !strings.Contains(err.Error(), "no Google Drive called “work”") {
		t.Fatalf("missing remote: %v", err)
	}
	if _, err := List(ctx, ts, Folder{Remote: "photos", Path: "Art"}); code(err) != apperr.InvalidInput || !strings.Contains(err.Error(), "isn't a Google Drive") {
		t.Fatalf("not a drive: %v", err)
	}
	if _, err := List(ctx, ts, Folder{Remote: "gdrive", Path: "Nope"}); code(err) != apperr.NotFound || !strings.Contains(err.Error(), "There's no folder “Nope”") {
		t.Fatalf("no folder: %v", err)
	}
	// A sign-in Google wants again: the command that reconnects it.
	st := fakerclone.Load(state)
	st.Remotes[0].Expired = true
	if err := fakerclone.Save(state, st); err != nil {
		t.Fatal(err)
	}
	_, err := List(ctx, ts, Folder{Remote: "gdrive", Path: "Teaser/Art"})
	if ae := apperr.From(err); ae.Code != apperr.NotFound || !strings.Contains(ae.Message, "sign in") || ae.Command() != "rclone config reconnect gdrive:" {
		t.Fatalf("expired: %+v", ae)
	}
}

// Without checksums from Google (an older rclone), the size still guards a fetch.
func TestNoChecksums(t *testing.T) {
	ctx := context.Background()
	ts, _, _ := drives(t, fakerclone.State{Checksums: "none"})
	f := Folder{Remote: "gdrive", Path: "Teaser/Art"}
	files, err := List(ctx, ts, f)
	if err != nil {
		t.Fatal(err)
	}
	if files[0].Checksum() != "" {
		t.Fatalf("no checksums: %+v", files[0])
	}
	got, err := Fetch(ctx, ts, f, files[0], t.TempDir(), 0)
	if err != nil || got.SHA256 == "" {
		t.Fatalf("fetched %+v %v", got, err)
	}
}

// Dropbox through rclone (D306): only Dropbox remotes, IDs and Dropbox's
// content hash from the listing, a file fetched by its ID — found again by it
// after a rename — and checked against the listing.
func TestDropbox(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dir := filepath.Join(root, "Teaser", "Art")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Larger than one 4 MiB block, so the content hash's blocks count.
	big := strings.Repeat("frame", 1<<20)
	_ = os.WriteFile(filepath.Join(dir, "walk.mp4"), []byte(big), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "logo.png"), []byte("\x89PNG logo"), 0o644)
	ts, state, log := drives(t, fakerclone.State{Remotes: []fakerclone.Remote{{Name: "dropbox", Type: "dropbox", Root: root}, {Name: "gdrive", Type: "drive", Root: t.TempDir()}}})

	if st := CheckOf(ctx, ts, DropboxType); !st.Ready() || strings.Join(st.Drives, ",") != "dropbox" {
		t.Fatalf("dropbox status %+v", st)
	}
	f, err := ParseFolderOf(DropboxType, "dropbox:Teaser/Art")
	if err != nil || f.Kind != DropboxType || f.Words() != "“Teaser/Art” in the Dropbox “dropbox”" {
		t.Fatalf("folder %+v %v", f, err)
	}
	// A Google Drive remote isn't a Dropbox, and the other way round.
	if _, err := List(ctx, ts, Folder{Remote: "gdrive", Path: "x", Kind: DropboxType}); code(err) != apperr.InvalidInput || !strings.Contains(err.Error(), "isn't a Dropbox") {
		t.Fatalf("a drive as a dropbox: %v", err)
	}
	if _, err := List(ctx, ts, Folder{Remote: "dropbox", Path: "Teaser/Art"}); code(err) != apperr.InvalidInput || !strings.Contains(err.Error(), "isn't a Google Drive") {
		t.Fatalf("a dropbox as a drive: %v", err)
	}
	files, err := List(ctx, ts, f)
	if err != nil {
		t.Fatal(err)
	}
	var walk File
	for _, fl := range files {
		if fl.Name == "walk.mp4" {
			walk = fl
		}
	}
	if !strings.HasPrefix(walk.ID, "id:") || walk.Dropbox != DropboxContentHash([]byte(big)) || walk.Checksum() != "dropbox:"+walk.Dropbox || walk.SHA256 != "" {
		t.Fatalf("walk %+v", walk)
	}
	got, err := Fetch(ctx, ts, f, walk, t.TempDir(), 64<<20)
	if err != nil || got.Dropbox != walk.Dropbox || got.Size != int64(len(big)) {
		t.Fatalf("fetched %+v %v", got, err)
	}
	// Renamed since it was listed: found by its ID.
	if err := os.Rename(filepath.Join(dir, "walk.mp4"), filepath.Join(dir, "garden walk.mp4")); err != nil {
		t.Fatal(err)
	}
	st := fakerclone.Load(state)
	st.Remotes[0].Renamed = map[string]string{"Teaser/Art/garden walk.mp4": "Teaser/Art/walk.mp4"}
	_ = fakerclone.Save(state, st)
	if got, err := Fetch(ctx, ts, f, walk, t.TempDir(), 64<<20); err != nil || got.Dropbox != walk.Dropbox {
		t.Fatalf("renamed: %+v %v", got, err)
	}
	// Changed since: refused.
	_ = os.WriteFile(filepath.Join(dir, "garden walk.mp4"), []byte(strings.Repeat("FRAME", 1<<20)), 0o644)
	if _, err := Fetch(ctx, ts, f, walk, t.TempDir(), 64<<20); code(err) != apperr.Conflict || !strings.Contains(err.Error(), "isn't the file Dropbox listed") || strings.Contains(err.Error(), "Google") {
		t.Fatalf("changed: %v", err)
	}
	if _, err := Fetch(ctx, ts, f, walk, t.TempDir(), 16); code(err) != apperr.InvalidInput || !strings.Contains(err.Error(), "from a Dropbox folder") {
		t.Fatalf("too large: %v", err)
	}
	// Listed moments ago in the same request: fetched without listing again.
	files, _ = List(ctx, ts, f)
	before, _ := os.ReadFile(log)
	for _, fl := range files {
		if fl.Name == "garden walk.mp4" {
			if got, err := Fetch(ctx, ts, f, fl, t.TempDir(), 64<<20, JustListed); err != nil || got.Dropbox != fl.Dropbox {
				t.Fatalf("just listed: %+v %v", got, err)
			}
		}
	}
	after, _ := os.ReadFile(log)
	if calls := strings.Split(strings.TrimSpace(string(after[len(before):])), "\n"); len(calls) != 1 || !strings.Contains(calls[0], "copyto") {
		t.Fatalf("a just-listed fetch ran %v", calls)
	}
	// Gone: plainly.
	_ = os.Remove(filepath.Join(dir, "garden walk.mp4"))
	if _, err := Fetch(ctx, ts, f, walk, t.TempDir(), 64<<20); code(err) != apperr.NotFound || !strings.Contains(err.Error(), "no longer there") {
		t.Fatalf("gone: %v", err)
	}
	// Only reads, never asks for a password; Dropbox files are copied by path, never copyid.
	b, _ := os.ReadFile(log)
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var args []string
		_ = json.Unmarshal([]byte(line), &args)
		if args[0] != "--ask-password=false" || (args[3] != "version" && args[3] != "listremotes" && args[3] != "lsjson" && args[3] != "copyto") {
			t.Fatalf("an unexpected call: %v", args)
		}
	}
	// A sign-in Dropbox wants again.
	st = fakerclone.Load(state)
	st.Remotes[0].Expired = true
	_ = fakerclone.Save(state, st)
	_, err = List(ctx, ts, f)
	if ae := apperr.From(err); !strings.Contains(ae.Message, "Dropbox asks you to sign in again") || ae.Command() != "rclone config reconnect dropbox:" {
		t.Fatalf("expired: %+v", ae)
	}
}

// Dropbox's content hash, as Dropbox documents it: the SHA-256 of each 4 MiB
// block's SHA-256 (an empty file hashes no blocks).
func TestDropboxContentHash(t *testing.T) {
	if got := DropboxContentHash(nil); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty: %s", got)
	}
	one := DropboxContentHash([]byte("abc"))
	d := newDropboxHash()
	_, _ = d.Write([]byte("a"))
	_, _ = d.Write([]byte("bc"))
	if d.hex() != one {
		t.Fatal("writing in pieces changes the hash")
	}
}
