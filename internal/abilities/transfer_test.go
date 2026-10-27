package abilities

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// craft writes an ability file by hand: the archive from files, the lineage
// given (the version appended), and extra zip entries.
func craft(t *testing.T, dir, fname string, files map[string]string, lineage []string, extra map[string][]byte) (string, string) {
	t.Helper()
	in := map[string]FileIn{}
	for p, s := range files {
		in[p] = FileIn{Bytes: []byte(s)}
	}
	arc, err := FromFiles(in)
	if err != nil {
		t.Fatalf("FromFiles: %v", err)
	}
	doc := AbilityFileDoc{Format: FileFormat, Made: time.Now().UTC(), MetaRush: "test", Workspace: "Theirs", Files: []models.AbilityFile{}}
	doc.Ability.Name = "title-card"
	doc.Version.Hash = arc.Hash
	doc.Version.State = "approved"
	doc.Lineage = append(append([]string{}, lineage...), arc.Hash)
	doc.Approval.Decisions = []models.AbilityFileDecision{{State: "approved", By: "Owner there", At: time.Now()}}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	put := func(n string, b []byte) {
		w, _ := zw.Create(n)
		_, _ = w.Write(b)
	}
	db, _ := json.Marshal(doc)
	put(fileDoc, db)
	put(fileArchive, arc.Bytes)
	for n, b := range extra {
		put(n, b)
	}
	_ = zw.Close()
	p := filepath.Join(dir, fname)
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p, arc.Hash
}

// Installed packages and compiled files never come in from a file either.
func TestImportRefusesInstalledPackages(t *testing.T) {
	a := newApp(t)
	dir := t.TempDir()
	for _, extra := range []string{"node_modules/left-pad/index.js", "venv/bin/activate", "site-packages/evil/__init__.py", "make.cpython-311.pyc"} {
		files := map[string]string{ManifestFile: cardManifest, "make.py": cardScript, extra: "x = 1"}
		in := map[string]FileIn{}
		for p, s := range files {
			in[p] = FileIn{Bytes: []byte(s)}
		}
		if _, err := FromFiles(in); err == nil {
			t.Errorf("an archive with %s", extra)
		}
		f := craftRaw(t, dir, files)
		if _, err := call[AbilityImportOut](t, a, "ability.import", AbilityImportIn{File: f}, ops.Caller{}); apperr.From(err).Code != apperr.InvalidInput {
			t.Errorf("a file with %s: %v", extra, err)
		}
	}
}

// The zip's ability.yaml and notes.md must be the archive's own.
func TestImportRefusesCopiesThatDiffer(t *testing.T) {
	a := newApp(t)
	dir := t.TempDir()
	files := map[string]string{ManifestFile: cardManifest, "make.py": cardScript, NotesFile: "real notes"}
	fake := strings.Replace(cardManifest, "Makes a plain title card", "Totally harmless", 1)
	f, _ := craft(t, dir, "y.ability.zip", files, nil, map[string][]byte{ManifestFile: []byte(fake), NotesFile: []byte("fake notes")})
	if _, err := call[AbilityImportOut](t, a, "ability.import", AbilityImportIn{File: f}, ops.Caller{}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("copies that differ: %v", err)
	}
	same, _ := craft(t, dir, "s.ability.zip", files, nil, map[string][]byte{ManifestFile: []byte(cardManifest), NotesFile: []byte("real notes")})
	if _, err := call[AbilityImportOut](t, a, "ability.import", AbilityImportIn{File: same}, ops.Caller{}); err != nil {
		t.Fatalf("copies that match: %v", err)
	}
}

// A file's lineage is its own word: a version of an ability already here
// comes in only when a person says it is the same one.
func TestImportNeverTrustsLineage(t *testing.T) {
	c := newApp(t)
	dir := t.TempDir()
	fa, ha := craft(t, dir, "a.ability.zip", map[string]string{ManifestFile: cardManifest, "make.py": cardScript}, nil, nil)
	mustCall[AbilityImportOut](t, c, "ability.import", AbilityImportIn{File: fa}, ops.Caller{})
	other := strings.Replace(cardManifest, "Makes a plain title card", "Makes an unrelated card", 1)
	fc, _ := craft(t, dir, "c.ability.zip", map[string]string{ManifestFile: other, "make.py": cardScript + "\n# x"}, []string{ha}, nil)
	_, err := call[AbilityImportOut](t, c, "ability.import", AbilityImportIn{File: fc}, ops.Caller{})
	if e := apperr.From(err); e.Code != apperr.Conflict || !strings.Contains(e.Message, "can't verify") {
		t.Fatalf("a claimed history: %v", err)
	}
	got := mustCall[AbilityImportOut](t, c, "ability.import", AbilityImportIn{File: fc, AsVersion: true}, ops.Caller{})
	if !got.Of || got.Number != 2 || got.State != models.AbilityImported || !strings.Contains(got.Lineage, "can't verify") {
		t.Fatalf("as a new version when asked: %+v", got)
	}
}

// Exporting writes a file: never for Claude or an app that only proposes,
// and writing over a file takes a person.
func TestExportIsAChange(t *testing.T) {
	c := newApp(t)
	dir := t.TempDir()
	fa, _ := craft(t, dir, "a.ability.zip", map[string]string{ManifestFile: cardManifest, "make.py": cardScript}, nil, nil)
	mustCall[AbilityImportOut](t, c, "ability.import", AbilityImportIn{File: fa}, ops.Caller{})
	victim := filepath.Join(dir, "precious.txt")
	_ = os.WriteFile(victim, []byte("precious"), 0o600)
	as := *c
	as.Actor.Via = models.ViaAssistant
	if _, err := call[AbilityExportOut](t, &as, "ability.export", AbilityExportIn{Ref: "title-card", Out: victim, Force: true}, ops.Caller{}); err == nil {
		t.Fatal("Claude exported")
	}
	pr := *c
	pr.ProposeOnly = true
	if _, err := call[AbilityExportOut](t, &pr, "ability.export", AbilityExportIn{Ref: "title-card", Out: victim, Force: true}, ops.Caller{}); err == nil {
		t.Fatal("an app that only proposes exported")
	}
	if _, err := call[AbilityExportOut](t, c, "ability.export", AbilityExportIn{Ref: "title-card", Out: victim, Force: true}, ops.Caller{}); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("a script writing over a file: %v", err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "precious" {
		t.Fatal("the file was written over")
	}
	if _, err := call[AbilityExportOut](t, c, "ability.export", AbilityExportIn{Ref: "title-card", Out: victim, Force: true}, ops.Caller{Human: true}); err != nil {
		t.Fatalf("a person writing over it: %v", err)
	}
}

// Whatever export writes, import reads: an archive of text with control
// characters (six bytes each in JSON) comes in.
func TestImportTakesWhatExportWrites(t *testing.T) {
	a := newApp(t)
	dir := t.TempDir()
	big := strings.Repeat("\x01", 7<<20)
	f, _ := craft(t, dir, "big.ability.zip", map[string]string{ManifestFile: cardManifest, "make.py": cardScript, "data1.txt": big, "data2.txt": big}, nil, nil)
	if _, err := call[AbilityImportOut](t, a, "ability.import", AbilityImportIn{File: f}, ops.Caller{}); err != nil {
		t.Fatalf("a large archive of text: %v", err)
	}
}

// craftRaw writes an ability file whose archive is built without FromFiles'
// checks (what a crafted file can hold).
func craftRaw(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	var fs []File
	for p, s := range files {
		fs = append(fs, File{Path: p, Text: s})
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].Path < fs[j].Path })
	b, err := encode(fs)
	if err != nil {
		t.Fatal(err)
	}
	doc := AbilityFileDoc{Format: FileFormat, Made: time.Now().UTC(), MetaRush: "test", Files: []models.AbilityFile{}}
	doc.Ability.Name = "title-card"
	doc.Version.Hash = sum(b)
	doc.Lineage = []string{doc.Version.Hash}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	db, _ := json.Marshal(doc)
	w, _ := zw.Create(fileDoc)
	_, _ = w.Write(db)
	w, _ = zw.Create(fileArchive)
	_, _ = w.Write(b)
	_ = zw.Close()
	p := filepath.Join(dir, fmt.Sprintf("raw-%d.ability.zip", len(files)+len(b)))
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestImportRefusesOddZips(t *testing.T) {
	a := newApp(t)
	dir := t.TempDir()
	f, _ := craft(t, dir, "ok.ability.zip", map[string]string{ManifestFile: cardManifest, "make.py": cardScript}, nil, nil)
	src, _ := os.ReadFile(f)
	zr, _ := zip.NewReader(bytes.NewReader(src), int64(len(src)))
	var docB, arcB []byte
	for _, x := range zr.File {
		rc, _ := x.Open()
		var bb bytes.Buffer
		_, _ = bb.ReadFrom(rc)
		rc.Close()
		if x.Name == fileDoc {
			docB = bb.Bytes()
		} else {
			arcB = bb.Bytes()
		}
	}
	mk := func(name string, entries [][2]any) string {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, e := range entries {
			w, _ := zw.Create(e[0].(string))
			_, _ = w.Write(e[1].([]byte))
		}
		_ = zw.Close()
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, buf.Bytes(), 0o600)
		return p
	}
	cases := map[string]string{
		"dup":      mk("dup.zip", [][2]any{{fileDoc, docB}, {fileArchive, arcB}, {fileDoc, docB}}),
		"slip":     mk("slip.zip", [][2]any{{fileDoc, docB}, {fileArchive, arcB}, {"../x", []byte("x")}}),
		"dotslash": mk("ds.zip", [][2]any{{"./" + fileDoc, docB}, {fileArchive, arcB}}),
		"unknown":  mk("unk.zip", [][2]any{{fileDoc, bytes.Replace(docB, []byte(`"format"`), []byte(`"evil":1,"format"`), 1)}, {fileArchive, arcB}}),
		"hash":     mk("hash.zip", [][2]any{{fileDoc, bytes.Replace(docB, []byte(`"hash":"`), []byte(`"hash":"0`), 1)}, {fileArchive, arcB}}),
	}
	for n, p := range cases {
		_, err := call[AbilityImportOut](t, a, "ability.import", AbilityImportIn{File: p}, ops.Caller{})
		t.Logf("%s: %v", n, err)
		if err == nil {
			t.Errorf("%s accepted", n)
		}
	}
}
