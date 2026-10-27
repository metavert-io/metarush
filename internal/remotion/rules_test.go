package remotion

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/models"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGitignore(t *testing.T) {
	g := &ignorer{}
	g.add("", []byte("# comment\nnode_modules/\n*.log\n/build\nout/*\n!out/*.mp4\ndocs/**/draft-*.md\n\\#literal\n"))
	g.add("src", []byte("generated/\n!keep.log\n"))
	cases := []struct {
		p     string
		dir   bool
		want  bool
		about string
	}{
		{"node_modules", true, true, "a folder pattern"},
		{"node_modules", false, false, "a folder pattern doesn't match a file"},
		{"a/b/c.log", false, true, "a name pattern at any depth"},
		{"build", true, true, "anchored"},
		{"src/build", true, false, "anchored only at the root"},
		{"out/x.mov", false, true, "out/*"},
		{"out/x.mp4", false, false, "negated"},
		{"docs/a/b/draft-1.md", false, true, "** in the middle"},
		{"docs/draft-1.md", false, true, "** matches no folder too"},
		{"#literal", false, true, "an escaped #"},
		{"src/generated", true, true, "a nested .gitignore"},
		{"generated", true, false, "a nested .gitignore applies only below it"},
		{"src/keep.log", false, false, "a nested negation wins (last rule)"},
	}
	for _, c := range cases {
		if got := g.ignored(c.p, c.dir); got != c.want {
			t.Errorf("%s: ignored(%q) = %v, want %v", c.about, c.p, got, c.want)
		}
	}
}

func TestFileRules(t *testing.T) {
	for _, p := range []string{".env", ".env.local", "config/prod.env", ".npmrc", "keys/server.pem", "id_ed25519", "creds/client_secret.json", "my-secrets.json"} {
		if skipFile(p) != whySecretName {
			t.Errorf("%s should be left out as a secret, got %q", p, skipFile(p))
		}
	}
	for _, p := range []string{"CLAUDE.md", "docs/AGENTS.md", ".cursorrules", ".mcp.json"} {
		if skipFile(p) != whyAssistant {
			t.Errorf("%s is for coding assistants, got %q", p, skipFile(p))
		}
	}
	for _, p := range []string{"src/Root.tsx", "public/a.png", "README.md", "package.json", "remotion.config.ts"} {
		if why := skipFile(p); why != "" {
			t.Errorf("%s should be kept, got %q", p, why)
		}
	}
	dirs := map[string]string{"node_modules": whyPackages, "a/node_modules": whyPackages, ".git": whyGit, ".claude": whyAssistant, ".github": whyTooling,
		".cache": whyCache, "out": whyCache, ".sources": whyCache, "src": "", "public": "", "src/out": ""}
	for d, want := range dirs {
		if got := skipDir(d); got != want {
			t.Errorf("skipDir(%q) = %q, want %q", d, got, want)
		}
	}
	if !safeRel("src/a.tsx") || safeRel("../x") || safeRel("/abs") || safeRel("a//b") || safeRel("a\\b") || safeRel("a/\x01b") {
		t.Error("safeRel")
	}
	key := "sk_" + strings.Repeat("a1", 16) // secret-scan: allow (a made-up key shape)
	if secretIn("src/config.ts", []byte("export const key = '"+key+"';")) == "" {
		t.Error("an sk_ key in code is a secret")
	}
	if secretIn("src/config.ts", []byte("export const token: string = props.token;")) != "" {
		t.Error("code that merely mentions tokens isn't a secret")
	}
	if secretIn("public/x.png", []byte("-----BEGIN PRIVATE KEY-----")) != "" { // secret-scan: allow (the marker alone, no key)
		t.Error("pictures aren't read")
	}
}

func TestReadFolderKeepsTheProjectAndLeavesTheRestOut(t *testing.T) {
	root := t.TempDir()
	write(t, root, "package.json", `{"name":"x"}`)
	write(t, root, "package-lock.json", `{}`)
	write(t, root, ".gitignore", "*.log\ngenerated-assets/\n")
	write(t, root, "src/Root.tsx", "export const x = 1;")
	write(t, root, "src/debug.log", "noise")
	write(t, root, "public/img/a.png", "png")
	write(t, root, "public/generated-assets/b.png", "kept: public files always come along")
	write(t, root, "generated-assets/c.txt", "ignored outside public")
	write(t, root, ".env", "ELEVENLABS_API_KEY=whatever")
	write(t, root, ".claude/settings.json", `{"hooks":{}}`)
	write(t, root, "CLAUDE.md", "run npm run build")
	write(t, root, "node_modules/remotion/index.js", "x")
	write(t, root, "src/keys.ts", "export const k = '"+"AKIA"+strings.Repeat("Z", 16)+"';") // secret-scan: allow (made up)
	write(t, root, "out/Teaser.mp4", "video")
	write(t, root, "out/scratch/try.mp4", "video")
	write(t, root, "out/a.mp3", "audio")
	if err := os.Symlink("/etc/hosts", filepath.Join(root, "src", "link.ts")); err != nil {
		t.Fatal(err)
	}
	s, err := readFolder(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, f := range s.files {
		kept = append(kept, f.Path)
		if len(f.SHA256) != 64 {
			t.Errorf("%s has no hash", f.Path)
		}
	}
	want := []string{".gitignore", "package-lock.json", "package.json", "public/generated-assets/b.png", "public/img/a.png", "src/Root.tsx"}
	if !slices.Equal(kept, want) {
		t.Fatalf("kept %v, want %v", kept, want)
	}
	why := map[string]string{}
	for _, g := range s.leftOut() {
		for _, e := range g.examples {
			why[e] = g.why
		}
	}
	for p, w := range map[string]string{".env": whySecretName, "CLAUDE.md": whyAssistant, ".claude/": whyAssistant, "node_modules/": whyPackages, "src/debug.log": whyIgnored,
		"generated-assets/": whyIgnored, "src/keys.ts": whySecretText, "src/link.ts": whyLink, "out/": whyCache} {
		if why[p] != w {
			t.Errorf("%s left out as %q, want %q", p, why[p], w)
		}
	}
	var vids []string
	for _, v := range s.videos {
		vids = append(vids, v.Path)
	}
	if !slices.Equal(vids, []string{"out/Teaser.mp4", "out/scratch/try.mp4"}) {
		t.Errorf("videos %v", vids)
	}
	if projectProblem(s) != "" {
		t.Errorf("a project with both package files: %q", projectProblem(s))
	}
}

func TestManifestIsCanonicalAndChecked(t *testing.T) {
	m := NewManifest([]ManifestFile{{Path: "src/b.ts", Size: 2, SHA256: strings.Repeat("b", 64)}, {Path: "package.json", Size: 1, SHA256: strings.Repeat("a", 64)}})
	if m.Files[0].Path != "package.json" {
		t.Fatal("sorted by path")
	}
	b := m.Bytes()
	got, err := ParseManifest(b, m.Hash())
	if err != nil || got.Hash() != m.Hash() {
		t.Fatalf("round trip: %v", err)
	}
	if _, err := ParseManifest(b, strings.Repeat("0", 64)); err == nil {
		t.Error("another version's hash is refused")
	}
	if _, err := ParseManifest([]byte(strings.Replace(string(b), `"size":1`, `"size": 1`, 1)), ""); err == nil {
		t.Error("a non-canonical manifest is refused")
	}
	if _, err := ParseManifest([]byte(strings.Replace(string(b), "src/b.ts", "../b.ts", 1)), ""); err == nil {
		t.Error("an unsafe path is refused")
	}
	n := NewManifest([]ManifestFile{{Path: "package.json", Size: 1, SHA256: strings.Repeat("c", 64)}, {Path: "src/c.ts", Size: 1, SHA256: strings.Repeat("d", 64)}})
	var ch []string
	for _, c := range Diff(m, n) {
		ch = append(ch, c.Change+" "+c.Path)
	}
	if !slices.Equal(ch, []string{"changed package.json", "removed src/b.ts", "added src/c.ts"}) {
		t.Errorf("diff %v", ch)
	}
}

func lock(entries string) []byte {
	return []byte(`{"lockfileVersion":3,"packages":{"":{"name":"x"},` + entries + `}}`)
}

const okEntries = `"node_modules/remotion":{"version":"4.0.535","resolved":"https://registry.npmjs.org/remotion/-/remotion-4.0.535.tgz","integrity":"sha512-aa"},
"node_modules/@remotion/bundler":{"version":"4.0.535","resolved":"https://registry.npmjs.org/@remotion/bundler/-/bundler-4.0.535.tgz","integrity":"sha512-bb"},
"node_modules/@remotion/renderer":{"version":"4.0.535","resolved":"https://registry.npmjs.org/@remotion/renderer/-/renderer-4.0.535.tgz","integrity":"sha512-cc"},
"node_modules/d3-scale":{"version":"4.0.2","resolved":"https://registry.npmjs.org/d3-scale/-/d3-scale-4.0.2.tgz","integrity":"sha512-dd"}`

func TestPackagesAreCheckedBeforeAnythingIsInstalled(t *testing.T) {
	pkg := []byte(`{"name":"teaser","dependencies":{"remotion":"4.0.535","@remotion/cli":"4.0.535","d3-scale":"^4.0.2"},"devDependencies":{"typescript":"5.9.3"},"scripts":{"postinstall":"evil"}}`)
	pi, err := readPackages(pkg, lock(okEntries))
	if err != nil {
		t.Fatal(err)
	}
	if pi.remotion != "4.0.535" || len(pi.lockHash) != 64 || pi.envKey == "" {
		t.Fatalf("%+v", pi)
	}
	if strings.Contains(string(pi.envPkg), "postinstall") || strings.Contains(string(pi.envPkg), "scripts") {
		t.Error("the environment's package.json carries no scripts")
	}
	kinds := map[string]string{}
	for _, p := range pi.packages {
		kinds[p.Name] = p.Kind
	}
	if kinds["d3-scale"] != "extra" || kinds["remotion"] != "core" || kinds["typescript"] != "tools" {
		t.Errorf("kinds %v", kinds)
	}
	bad := map[string][]byte{
		"git":         lock(okEntries + `,"node_modules/evil":{"version":"1.0.0","resolved":"git+ssh://git@github.com/x/evil.git#abc","integrity":"sha512-ee"}`),
		"tarball url": lock(okEntries + `,"node_modules/evil":{"version":"1.0.0","resolved":"https://evil.example/evil.tgz","integrity":"sha512-ee"}`),
		"no checksum": lock(okEntries + `,"node_modules/evil":{"version":"1.0.0","resolved":"https://registry.npmjs.org/evil/-/evil-1.0.0.tgz"}`),
		"a link":      lock(okEntries + `,"node_modules/evil":{"link":true,"resolved":"../evil"}`),
		"old format":  []byte(`{"lockfileVersion":1,"dependencies":{}}`),
		"no renderer": lock(strings.Replace(okEntries, "@remotion/renderer", "@remotion/other", 2)),
	}
	for what, l := range bad {
		if _, err := readPackages(pkg, l); err == nil {
			t.Errorf("%s should be refused", what)
		}
	}
	if _, err := readPackages([]byte(`{"dependencies":{"x":"file:../x"}}`), lock(okEntries)); err == nil {
		t.Error("a file: dependency is refused")
	}
	ch := diffPackages([]models.RemotionPackage{{Name: "a", Version: "1"}, {Name: "b", Version: "1"}}, []models.RemotionPackage{{Name: "b", Version: "2"}, {Name: "c", Version: "3"}})
	if got := packageWords(ch); got != "removes a, moves b from 1 to 2 and adds c 3" {
		t.Errorf("words: %q", got)
	}
}

func TestChangedWordsAndAffectedCompositions(t *testing.T) {
	before := []byte("export const A = {\n  headline: \"Your top 20% of players drive 62% of revenue.\",\n  color: \"#805AD5\",\n};\n")
	after := []byte("export const A = {\n  headline: \"Your best players drive most of your revenue.\",\n  color: \"#805AD5\",\n};\n")
	m1 := NewManifest([]ManifestFile{{Path: "src/story.ts", Size: int64(len(before)), SHA256: sha256Hex(before)}})
	m2 := NewManifest([]ManifestFile{{Path: "src/story.ts", Size: int64(len(after)), SHA256: sha256Hex(after)}})
	cs := Diff(m1, m2)
	files, words := fileChanges(cs, func(f ManifestFile) ([]byte, bool) {
		if f.SHA256 == sha256Hex(before) {
			return before, true
		}
		return after, true
	})
	if len(files) != 1 || files[0].Lines != 2 {
		t.Fatalf("files %+v", files)
	}
	if len(words) != 1 || words[0].Before != "Your top 20% of players drive 62% of revenue." || words[0].After != "Your best players drive most of your revenue." || words[0].Line != 2 {
		t.Fatalf("words %+v", words)
	}
	if !strings.HasPrefix(changeWords(files, words), "“Your top 20%") {
		t.Errorf("summary: %s", changeWords(files, words))
	}
	comps := []models.RemotionComposition{{ID: "Teaser", File: "src/Root.tsx", Component: "src/Teaser.tsx", Files: []string{"src/Teaser.tsx", "src/story.ts"}},
		{ID: "Logo", File: "src/Root.tsx", Component: "src/Logo.tsx", Files: []string{"src/Logo.tsx"}}}
	if got := affected(comps, comps, cs, nil); !slices.Equal(got, []string{"Teaser"}) {
		t.Errorf("affected %v", got)
	}
	pub := Diff(NewManifest(nil), NewManifest([]ManifestFile{{Path: "public/a.png", Size: 1, SHA256: strings.Repeat("a", 64)}}))
	if got := affected(comps, comps, pub, []string{"a.png"}); len(got) != 2 {
		t.Errorf("a picture the code names reaches every composition: %v", got)
	}
	if got := affected(comps, comps, pub, []string{"other.png"}); len(got) != 0 {
		t.Errorf("a picture nothing names: %v", got)
	}
}

func TestPropsKeysAndFrames(t *testing.T) {
	a, _, err := canonicalProps(json.RawMessage(`{"b": 2, "a": "x"}`))
	if err != nil || a != `{"a":"x","b":2}` {
		t.Fatalf("%q %v", a, err)
	}
	if e, _, _ := canonicalProps(json.RawMessage(`{}`)); e != "" {
		t.Error("no props")
	}
	if _, _, err := canonicalProps(json.RawMessage(`[1]`)); err == nil {
		t.Error("props are an object")
	}
	s := &models.RemotionSource{ID: "rms_x"}
	v := &models.RemotionVersion{Hash: strings.Repeat("a", 64)}
	k1 := renderKey(s, v, "Intro", a, nil, variant{})
	if k1 != renderKey(s, v, "Intro", a, nil, variant{}) || k1 == renderKey(s, v, "Intro", "", nil, variant{}) || k1 == renderKey(s, v, "Intro", a, []int{0, 9}, variant{}) {
		t.Error("the key covers props and frames, and nothing else changes it")
	}
	v.Config = []models.RemotionConfigNote{{Setting: "setJpegQuality", Value: "92", Applied: true}}
	if k1 == renderKey(s, v, "Intro", a, nil, variant{}) {
		t.Error("render settings are part of the key")
	}
	if f, err := ParseFrames("0-299"); err != nil || !slices.Equal(f, []int{0, 299}) {
		t.Error("frames")
	}
	if _, err := ParseFrames("9-3"); err == nil {
		t.Error("backwards frames")
	}
	if clipID("KiteTeaserV3", "", map[string]bool{"kiteteaserv3": true}) != "kiteteaserv3-2" {
		t.Error("clip names")
	}
	if slug("Kite Teaser!") != "kite-teaser" || slug("123") != "remotion-123" {
		t.Error("slug")
	}
	if cleanRemote("https://user:tok@github.com/acme/x.git") != "https://github.com/acme/x.git" || cleanRemote("git@github.com:acme/x.git") != "github.com:acme/x.git" {
		t.Errorf("remote: %q", cleanRemote("https://user:tok@github.com/acme/x.git"))
	}
	if upperFirst("“x") != "“x" || upperFirst("abc") != "Abc" {
		t.Error("upperFirst keeps a quote whole")
	}
}

func TestFrameDifferenceFindsSmallChanges(t *testing.T) {
	dir := t.TempDir()
	img := func(name string, box bool) string {
		m := image.NewRGBA(image.Rect(0, 0, 384, 216))
		for y := 0; y < 216; y++ {
			for x := 0; x < 384; x++ {
				m.Set(x, y, color.RGBA{20, 20, 28, 255})
			}
		}
		if box { // a few words' worth of changed pixels in one corner
			for y := 180; y < 196; y++ {
				for x := 300; x < 360; x++ {
					m.Set(x, y, color.RGBA{240, 240, 248, 255})
				}
			}
		}
		p := filepath.Join(dir, name)
		f, _ := os.Create(p)
		_ = png.Encode(f, m)
		f.Close()
		return p
	}
	a, b, c := img("a.png", false), img("b.png", false), img("c.png", true)
	if d, err := frameDiff(a, b); err != nil || d != 0 {
		t.Errorf("same pictures: %v %v", d, err)
	}
	if d, _ := frameDiff(a, c); d < changeThreshold {
		t.Errorf("a small change counts: %v", d)
	}
}
