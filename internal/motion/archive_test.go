package motion

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
)

const sampleIndex = `import { AbsoluteFill } from 'remotion';
import { z } from 'zod';
export const schema = z.object({ title: z.string() });
export const meta = { summary: 'A title', region: 'center', minDuration: 1, maxDuration: 5 };
export default function Title({ params }: { params: { title: string } }) {
  return <AbsoluteFill>{params.title}</AbsoluteFill>;
}
`

var bom = string([]byte{0xef, 0xbb, 0xbf})

func writeComponent(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, s := range files {
		f := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestArchiveIsCanonical(t *testing.T) {
	a, err := Build(writeComponent(t, map[string]string{"index.tsx": sampleIndex, "preview.json": `{"title": "Hi"}`, "lib/ease.ts": "export const k = 1;\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if !ValidHash(a.Hash) || len(a.Files) != 3 || a.Files[0].Path != "index.tsx" || a.Files[1].Path != "lib/ease.ts" || a.Files[2].Path != "preview.json" {
		t.Fatalf("archive %s %+v", a.Hash, a.Files)
	}
	// Line endings and a byte-order mark don't change the identity.
	b, err := Build(writeComponent(t, map[string]string{"index.tsx": bom + strings.ReplaceAll(sampleIndex, "\n", "\r\n"), "preview.json": `{"title": "Hi"}`, "lib/ease.ts": "export const k = 1;\r\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if b.Hash != a.Hash {
		t.Fatalf("CRLF/BOM changed the hash: %s vs %s", b.Hash, a.Hash)
	}
	// Hidden files are left out (and listed); the hash stays.
	c, err := Build(writeComponent(t, map[string]string{"index.tsx": sampleIndex, "preview.json": `{"title": "Hi"}`, "lib/ease.ts": "export const k = 1;\n", ".DS_Store": "x", ".check/frame.png": "x"}))
	if err != nil || c.Hash != a.Hash || len(c.Ignored) != 2 {
		t.Fatalf("hidden files: %v %v %v", err, c.Hash, c.Ignored)
	}
	// Parse accepts exactly the canonical bytes.
	p, err := Parse(a.Bytes)
	if err != nil || p.Hash != a.Hash {
		t.Fatalf("parse: %v", err)
	}
	if _, err := Parse(append([]byte(nil), append(a.Bytes, '\n')...)); err == nil {
		t.Fatal("a trailing newline is not canonical")
	}
	if _, err := Parse([]byte(strings.Replace(string(a.Bytes), `"files"`, `"extra":1,"files"`, 1))); err == nil {
		t.Fatal("unknown fields are refused")
	}
	// Extract writes the same files back.
	out := t.TempDir()
	if err := p.Extract(out); err != nil {
		t.Fatal(err)
	}
	again, err := Build(out)
	if err != nil || again.Hash != a.Hash {
		t.Fatalf("extract round trip: %v", err)
	}
}

// Editing one character makes a new version (which needs its own approval).
func TestOneCharacterChangesTheHash(t *testing.T) {
	a, err := Build(writeComponent(t, map[string]string{"index.tsx": sampleIndex}))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(writeComponent(t, map[string]string{"index.tsx": strings.Replace(sampleIndex, "A title", "A titlf", 1)}))
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash == b.Hash {
		t.Fatal("one character changed, the hash did not")
	}
	c, err := Build(writeComponent(t, map[string]string{"index.tsx": sampleIndex, "preview.json": `{"title":"x"}`}))
	if err != nil || c.Hash == a.Hash {
		t.Fatal("sample parameters are part of what is reviewed")
	}
}

func TestArchiveRefusals(t *testing.T) {
	cases := map[string]map[string]string{
		"needs an index.tsx":           {"main.tsx": sampleIndex},
		"can't bring its own packages": {"index.tsx": sampleIndex, "package.json": "{}"},
		"node_modules":                 {"index.tsx": sampleIndex, "node_modules/x/index.js": ""},
		"can't change how it is built": {"index.tsx": sampleIndex, "tsconfig.json": "{}"},
		"images, fonts or media":       {"index.tsx": sampleIndex, "logo.png": "x"},
		"TypeScript":                   {"index.tsx": sampleIndex, "helper.js": "x"},
		"isn't text":                   {"index.tsx": sampleIndex, "data.json": "\xff\xfe"},
		"type declarations":            {"index.tsx": sampleIndex, "env.d.ts": ""},
	}
	for want, files := range cases {
		_, err := Build(writeComponent(t, files))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", want, err)
		}
		if err != nil && apperr.From(err).Code != apperr.InvalidInput {
			t.Errorf("%s: code %s", want, apperr.From(err).Code)
		}
	}
	dir := writeComponent(t, map[string]string{"index.tsx": sampleIndex})
	if err := os.Symlink("/etc/hosts", filepath.Join(dir, "hosts.ts")); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(dir); err == nil || !strings.Contains(err.Error(), "link") {
		t.Errorf("symlink: %v", err)
	}
	big := strings.Repeat("x", MaxFileBytes+1)
	if _, err := Build(writeComponent(t, map[string]string{"index.tsx": sampleIndex, "big.ts": big})); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("big file: %v", err)
	}
	if _, err := FromFiles(map[string][]byte{"index.tsx": []byte(sampleIndex), "../x.ts": nil}); err == nil {
		t.Error("a path outside the folder is refused")
	}
}

func TestParseRef(t *testing.T) {
	for in, want := range map[string]Ref{
		"route-map@3fa9c2d1":        {Name: "route-map", Hash: "3fa9c2d1"},
		"route-map@sha256:3FA9C2D1": {Name: "route-map", Hash: "3fa9c2d1"},
		"route-map":                 {Name: "route-map"},
		"3fa9c2d1aa":                {Hash: "3fa9c2d1aa"},
	} {
		got, err := ParseRef(in)
		if err != nil || got != want {
			t.Errorf("%s: %+v %v", in, got, err)
		}
	}
	for _, bad := range []string{"Route Map", "route-map@3fa9", "route-map@xyz12345", "-x"} {
		if _, err := ParseRef(bad); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}
