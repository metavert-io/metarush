package abilities

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sjs "github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const exampleManifest = `ability: 1
name: stock-footage
title: Stock footage
does: Finds free stock videos and photos that match a few words and brings them in with their licence.
runs: python3 run.py
inputs:
  words: {kind: words, max: 100, says: What to look for}
  count: {kind: number, min: 1, max: 6, default: 3}
outputs: [footage, picture]
cost: free
keys: [stock]
hosts: [api.example.com, videos.example.com]
sends: [words]
changes-outside: no
likeness: no
needs: {programs: [python3], os: [mac, linux]}
licence: MIT
search: [stock, b-roll, footage]
test: {inputs: {words: ocean waves, count: 1}}
`

func problems(t *testing.T, yml string, files map[string]bool) []string {
	t.Helper()
	m, err := ParseManifest([]byte(yml))
	if err != nil {
		return []string{err.Error()}
	}
	var out []string
	for _, p := range m.Validate(files) {
		out = append(out, p.String())
	}
	return out
}

func TestExampleManifest(t *testing.T) {
	if ps := problems(t, exampleManifest, map[string]bool{"run.py": false, ManifestFile: false}); len(ps) != 0 {
		t.Fatalf("the example is a good manifest: %v", ps)
	}
	m, _ := ParseManifest([]byte(exampleManifest))
	if m.NetworkMode() != NetworkMetaRush || m.Keys[0].EnvName() != "STOCK_API_KEY" || bool(m.ChangesOutside) || !m.Cost.Free || m.SendsAway() {
		t.Errorf("defaults: %+v", m)
	}
	v, err := m.Values(map[string]any{"words": "  beach  "}, false)
	if err != nil || v["words"] != "beach" || v["count"] != 3.0 {
		t.Errorf("values: %v %v", v, err)
	}
	if _, err := m.Values(map[string]any{"count": "9", "words": "x"}, false); err == nil || !strings.Contains(err.Error(), "at most 6") {
		t.Errorf("a number out of range: %v", err)
	}
	if _, err := m.Values(map[string]any{"colour": "red", "words": "x"}, false); err == nil {
		t.Error("an unknown input is refused")
	}
	if _, err := m.Values(map[string]any{}, false); err == nil || !strings.Contains(err.Error(), "words is needed") {
		t.Errorf("a required input: %v", err)
	}
}

func TestManifestProblems(t *testing.T) {
	cases := []struct {
		name, from, to, want string
	}{
		{"unknown field", "licence: MIT", "licence: MIT\nmagic: yes", "isn't a field"},
		{"bad name", "name: stock-footage", "name: Stock", "lowercase"},
		{"program not in needs", "needs: {programs: [python3], os: [mac, linux]}", "needs: {os: [mac]}", "needs.programs must list it"},
		{"code on the command line", "runs: python3 run.py", "runs: python3 -c print(1)", "not code given on the command line"},
		{"a host that is an address", "hosts: [api.example.com, videos.example.com]", "hosts: [api.example.com, 10.0.0.1]", "not an address"},
		{"this computer", "hosts: [api.example.com, videos.example.com]", "hosts: [api.example.com, localhost]", "this computer"},
		{"key to another host", "keys: [stock]", "keys: [{name: stock, hosts: [evil.example.net]}]", "isn't one of its hosts"},
		{"built-in key elsewhere", "keys: [stock]", "keys: [elevenlabs]", "goes only to its own service"},
		{"key under a steering name", "keys: [stock]", "keys: [{name: stock, env: NODE_OPTIONS}]", "steers programs"},
		{"open network with keys", "sends: [words]", "sends: [words]\nnetwork: open", "can't keep keys"},
		{"no sends", "sends: [words]\n", "", "say what leaves"},
		{"priced without a date", "cost: free", "cost: {usd: 0.04, per: picture}", "the day it was read"},
		{"priced from a missing input", "cost: free", "cost: {usd: 0.04, per: picture, as-of: 2026-10-01, from: pages}", "isn't a number input"},
		{"no test", "test: {inputs: {words: ocean waves, count: 1}}", "", "give the inputs"},
		{"a test with an unknown input", "test: {inputs: {words: ocean waves, count: 1}}", "test: {inputs: {colour: red}}", "no input called"},
		{"bad output", "outputs: [footage, picture]", "outputs: [footage, website]", "isn't something MetaRush takes in"},
		{"yes or no", "likeness: no", "likeness: maybe", "say yes or no"},
		{"footage leaves without saying", "  count: {kind: number, min: 1, max: 6, default: 3}", "  count: {kind: number, min: 1, max: 6, default: 3}\n  clip: {kind: footage, optional: true}", "sends must say"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			yml := strings.Replace(exampleManifest, c.from, c.to, 1)
			if yml == exampleManifest {
				t.Fatalf("the case changed nothing")
			}
			ps := problems(t, yml, map[string]bool{"run.py": false})
			if !strings.Contains(strings.Join(ps, "\n"), c.want) {
				t.Fatalf("want %q in %v", c.want, ps)
			}
		})
	}
}

func TestLocalProgram(t *testing.T) {
	yml := strings.Replace(exampleManifest, "runs: python3 run.py", "runs: ./bin/tool --fast", 1)
	if ps := problems(t, yml, map[string]bool{"bin/tool": true}); len(ps) != 0 {
		t.Fatalf("a program of the folder: %v", ps)
	}
	if ps := problems(t, yml, map[string]bool{"bin/tool": false}); !strings.Contains(strings.Join(ps, " "), "chmod +x") {
		t.Fatalf("not executable: %v", ps)
	}
	if ps := problems(t, yml, map[string]bool{}); !strings.Contains(strings.Join(ps, " "), "has no bin/tool") {
		t.Fatalf("missing: %v", ps)
	}
	m, _ := ParseManifest([]byte(strings.Replace(exampleManifest, "runs: python3 run.py", `runs: python3 "my script.py" --x`, 1)))
	if argv, err := m.Argv(); err != nil || len(argv) != 3 || argv[1] != "my script.py" {
		t.Errorf("quoted words: %v %v", argv, err)
	}
}

func writeFolder(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, s := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(p, ".sh") || strings.HasPrefix(p, "bin/") {
			mode = 0o755
		}
		if err := os.WriteFile(full, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestArchiveIsCanonical(t *testing.T) {
	files := map[string]string{ManifestFile: exampleManifest, "run.py": "print('hi')\r\n", "notes.md": "Use it for b-roll.", "bin/tool": "\x00\x01binary", ".env": "SECRET=1", ".git/HEAD": "ref"}
	a, err := Build(writeFolder(t, files))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(writeFolder(t, files))
	if err != nil || a.Hash != b.Hash || !ValidHash(a.Hash) {
		t.Fatalf("the same folder is the same version: %v", err)
	}
	if strings.Join(a.Ignored, ",") != ".env,.git" {
		t.Errorf("hidden files left out: %v", a.Ignored)
	}
	if got, _ := a.File("run.py"); string(got) != "print('hi')\r\n" {
		t.Errorf("text is kept exactly: %q", got)
	}
	if got, _ := a.File("bin/tool"); string(got) != "\x00\x01binary" || !a.Programs()["bin/tool"] || a.Programs()["run.py"] {
		t.Errorf("binary files and programs: %q %v", got, a.Programs())
	}
	p, err := Parse(a.Bytes)
	if err != nil || p.Hash != a.Hash {
		t.Fatalf("parse: %v", err)
	}
	// Not canonical: the same files in another order.
	var doc archiveDoc
	_ = json.Unmarshal(a.Bytes, &doc)
	doc.Files[0], doc.Files[1] = doc.Files[1], doc.Files[0]
	bad, _ := json.Marshal(doc)
	if _, err := Parse(bad); err == nil {
		t.Error("an archive not in canonical form is refused")
	}
	// One byte changes the version; so does a program's mark.
	files["run.py"] = "print('hi!')\r\n"
	c, _ := Build(writeFolder(t, files))
	if c.Hash == a.Hash {
		t.Error("a changed file is a new version")
	}
	out := t.TempDir()
	if err := a.Extract(out); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Join(out, "bin", "tool")); err != nil || st.Mode()&0o111 == 0 || st.Mode()&0o222 != 0 {
		t.Errorf("programs are executable and read-only: %v %v", st.Mode(), err)
	}
	if m, err := a.Manifest(); err != nil || m.Name != "stock-footage" || a.Notes() != "Use it for b-roll." {
		t.Errorf("manifest and notes: %v", err)
	}
}

func TestArchiveRefusals(t *testing.T) {
	key := "sk_live_" + strings.Repeat("a1b2", 8) // secret-scan: allow (made up)
	for name, files := range map[string]map[string]string{
		"a key in the code":  {ManifestFile: exampleManifest, "run.py": "KEY = '" + key + "'"},
		"no manifest":        {"run.py": "print(1)"},
		"installed packages": {ManifestFile: exampleManifest, "node_modules/x/index.js": "1"},
		"a private key":      {ManifestFile: exampleManifest, "id": "-----BEGIN RSA PRIVATE KEY-----\nabc"}, // secret-scan: allow (the marker alone)
		"MetaRush join file": {ManifestFile: exampleManifest, "metarush-join-abc.json": "{}"},
	} {
		if _, err := Build(writeFolder(t, files)); err == nil {
			t.Errorf("%s: refused", name)
		}
	}
	dir := writeFolder(t, map[string]string{ManifestFile: exampleManifest})
	if err := os.Symlink("/etc/hosts", filepath.Join(dir, "hosts")); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(dir); err == nil || !strings.Contains(err.Error(), "link") {
		t.Errorf("links are refused: %v", err)
	}
}

func TestParseRef(t *testing.T) {
	for in, want := range map[string]Ref{"stock": {Name: "stock"}, "stock@3fa9c2d1": {Name: "stock", Hash: "3fa9c2d1"}, "3FA9C2D1aa": {Hash: "3fa9c2d1aa"}} {
		if got, err := ParseRef(in); err != nil || got != want {
			t.Errorf("%s: %+v %v", in, got, err)
		}
	}
	for _, bad := range []string{"Stock", "stock@zz", "stock@123", "@"} {
		if _, err := ParseRef(bad); err == nil {
			t.Errorf("%s is refused", bad)
		}
	}
}

func TestManifestSchema(t *testing.T) {
	b, err := json.Marshal(ManifestSchema())
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"changes-outside"`, `"as-of"`, `"runs"`, `"licence"`, `"free"`} {
		if !strings.Contains(s, want) {
			t.Errorf("the schema names %s", want)
		}
	}
	if strings.Contains(s, `"changesOutside"`) {
		t.Error("the schema uses the manifest's own names")
	}
	// The example passes the generated schema; an unknown field doesn't.
	c := sjs.NewCompiler()
	doc, err := sjs.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddResource("ability.json", doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile("ability.json")
	if err != nil {
		t.Fatal(err)
	}
	asJSON := func(yml string) any {
		var v any
		if err := yaml.Unmarshal([]byte(yml), &v); err != nil {
			t.Fatal(err)
		}
		jb, _ := json.Marshal(v)
		d, err := sjs.UnmarshalJSON(bytes.NewReader(jb))
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if err := sch.Validate(asJSON(exampleManifest)); err != nil {
		t.Errorf("the example passes the schema: %v", err)
	}
	priced := strings.Replace(exampleManifest, "cost: free", "cost: {usd: 0.04, per: picture, as-of: \"2026-10-01\", from: count}", 1) // quoted: a generic YAML reader makes a date of it
	priced = strings.Replace(priced, "keys: [stock]", "keys: [{name: stock, env: STOCK_KEY, hosts: [api.example.com]}]", 1)
	if err := sch.Validate(asJSON(priced)); err != nil {
		t.Errorf("a priced example passes the schema: %v", err)
	}
	if err := sch.Validate(asJSON(exampleManifest + "magic: 1\n")); err == nil {
		t.Error("the schema refuses unknown fields")
	}
}

// TestProgramsInsideTheHomeFolder: a program a person would pin inside their
// home folder (~/bin) is refused, with what to do instead: an ability never
// reads the home folder (review 1).
func TestProgramsInsideTheHomeFolder(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home folder")
	}
	dir, err := os.MkdirTemp(home, ".metarush-ability-test-")
	if err != nil {
		t.Skip("can't make a folder in the home folder")
	}
	defer os.RemoveAll(dir)
	prog := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := pinProgram("ffmpeg", prog); err == nil || !strings.Contains(err.Error(), "inside your home folder") {
		t.Fatalf("a program in the home folder: %v", err)
	}
	if !hiddenProgram(filepath.Join(home, "bin", "python3")) || hiddenProgram("/usr/bin/python3") {
		t.Error("the home folder is hidden, the system isn't")
	}
}

// TestKeyPlace: a key goes in one place, a header (Authorization unless it
// says) or a query parameter; headers that steer the request can't carry one.
func TestKeyPlace(t *testing.T) {
	m, _ := ParseManifest([]byte(exampleManifest))
	if h, q := m.Keys[0].Place(); h != "Authorization" || q != "" {
		t.Errorf("default: %q %q", h, q)
	}
	for yml, want := range map[string]string{
		"keys: [{name: stock, header: x-api-key}]":         "",
		"keys: [{name: stock, query: api_key}]":            "",
		"keys: [{name: stock, header: X-Key, query: key}]": "one place",
		"keys: [{name: stock, header: Host}]":              "can't carry a key",
		"keys: [{name: stock, header: Cookie}]":            "can't carry a key",
		"keys: [{name: stock, query: \"a b\"}]":            "isn't a query parameter",
	} {
		ps := strings.Join(problems(t, strings.Replace(exampleManifest, "keys: [stock]", yml, 1), map[string]bool{"run.py": false}), " ")
		if (want == "" && ps != "") || (want != "" && !strings.Contains(ps, want)) {
			t.Errorf("%s: %q", yml, ps)
		}
	}
	m, _ = ParseManifest([]byte(strings.Replace(exampleManifest, "keys: [stock]", "keys: [{name: stock, header: x-api-key}]", 1)))
	if h, _ := m.Keys[0].Place(); h != "X-Api-Key" {
		t.Errorf("a header's name: %q", h)
	}
}

// TestOutputsHoldingAKey: a file that holds a key the run had, in any
// written form, is left out; result.json's words lose it too (review 2).
func TestOutputsHoldingAKey(t *testing.T) {
	const key = "pk/0123456789+abcdefghij" // secret-scan: allow (made up); gitleaks:allow
	m, _ := ParseManifest([]byte(strings.Replace(exampleManifest, "outputs: [footage, picture]", "outputs: [picture]", 1)))
	dir := t.TempDir()
	svg := func(s string) string { return `<svg xmlns="http://www.w3.org/2000/svg"><desc>` + s + `</desc></svg>` }
	_ = os.WriteFile(filepath.Join(dir, "clean.svg"), []byte(svg("fine")), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "raw.svg"), []byte(svg(key)), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "b64.svg"), []byte(svg(base64.StdEncoding.EncodeToString([]byte(key)))), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "esc.svg"), []byte(svg(url.QueryEscape(key))), 0o644)
	_ = os.WriteFile(filepath.Join(dir, ResultFile), []byte(`{"files":[{"path":"clean.svg","title":"t `+key+`","source":"https://x.example/?k=`+url.QueryEscape(key)+`"}]}`), 0o644)
	outs := collectOutputs(dir, m, 1<<20, keyForms([]string{key}))
	ok := map[string]bool{}
	for _, o := range outs {
		ok[o.Rel] = o.OK
		if o.Rel == "clean.svg" && (strings.Contains(o.Title, key) || strings.Contains(o.Source, url.QueryEscape(key))) {
			t.Errorf("result.json's words keep the key: %+v", o)
		}
	}
	if !ok["clean.svg"] || ok["raw.svg"] || ok["b64.svg"] || ok["esc.svg"] {
		t.Fatalf("outputs holding a key are left out: %v", ok)
	}
	if s := scrub("log: "+key+" and "+url.QueryEscape(key), keyForms([]string{key})); strings.Contains(s, key) || strings.Contains(s, url.QueryEscape(key)) {
		t.Fatalf("scrub: %q", s)
	}
}

// TestOutputsAreCopiesNeverLinks: what a run brings back is copied out of
// its room without following a link — a link to a file or a folder elsewhere
// is left out — and everything after reads MetaRush's copy, which the program
// can't change any more (review 5).
func TestOutputsAreCopiesNeverLinks(t *testing.T) {
	m, _ := ParseManifest([]byte(strings.Replace(exampleManifest, "outputs: [footage, picture]", "outputs: [picture]", 1)))
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><rect width="1" height="1"/></svg>`)
	elsewhere := t.TempDir()
	_ = os.WriteFile(filepath.Join(elsewhere, "secret.svg"), svg, 0o644)
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "made.svg"), svg, 0o644)
	_ = os.WriteFile(filepath.Join(dir, "sub", "also.svg"), svg, 0o644)
	_ = os.Symlink(filepath.Join(elsewhere, "secret.svg"), filepath.Join(dir, "link.svg"))
	_ = os.Symlink(elsewhere, filepath.Join(dir, "linked-folder"))
	take := t.TempDir()
	outs, _ := takeOutputs(dir, take, m, 1<<20, nil)
	got := map[string]roomOutput{}
	for _, o := range outs {
		got[o.Rel] = o
	}
	if !got["made.svg"].OK || !got["sub/also.svg"].OK {
		t.Fatalf("the files it made: %+v", outs)
	}
	if got["link.svg"].OK || !strings.Contains(got["link.svg"].Why, "link") {
		t.Errorf("a link to a file elsewhere: %+v", got["link.svg"])
	}
	for rel, o := range got {
		if strings.HasPrefix(rel, "linked-folder/") && o.OK {
			t.Errorf("a file through a linked folder: %+v", o)
		}
	}
	made := got["made.svg"]
	if !strings.HasPrefix(made.Abs, take+string(filepath.Separator)) {
		t.Fatalf("what is taken in is MetaRush's copy: %s", made.Abs)
	}
	_ = os.WriteFile(filepath.Join(dir, "made.svg"), []byte("changed afterwards"), 0o644)
	if b, _ := os.ReadFile(made.Abs); !bytes.Equal(b, svg) {
		t.Fatal("the copy follows the room")
	}
}

// TestKeysAreNeverSafe: "free" is the manifest's word; with any key a person
// starts each run (review 4).
func TestKeysAreNeverSafe(t *testing.T) {
	m, _ := ParseManifest([]byte(exampleManifest)) // free, with a key
	if d := decider(factsOf(m)); d != DecideKeyed {
		t.Fatalf("free with a key: %s", d)
	}
	m, _ = ParseManifest([]byte(strings.Replace(strings.Replace(exampleManifest, "keys: [stock]\n", "", 1), "hosts: [api.example.com, videos.example.com]", "hosts: [api.example.com]", 1)))
	if d := decider(factsOf(m)); d != DecideSafe {
		t.Fatalf("free, no key, words only: %s", d)
	}
}
