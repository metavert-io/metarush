package remotion

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

// fakeKey is a made-up key built at run time (never a literal in the tree).
func fakeKey() string { return "sk_" + strings.Repeat("f00d", 10) }

// versionOf records a version whose files are real objects.
func versionOf(t *testing.T, a *app.App, s *models.RemotionSource, files map[string]string, parent, kind string) *models.RemotionVersion {
	t.Helper()
	ctx := context.Background()
	var mfs []ManifestFile
	for p, body := range files {
		obj, err := putBytes(ctx, a, []byte(body), mimeOf(p))
		if err != nil {
			t.Fatal(err)
		}
		mfs = append(mfs, ManifestFile{Path: p, Size: int64(len(body)), SHA256: obj.SHA256})
	}
	m := NewManifest(mfs)
	mobj, err := putBytes(ctx, a, m.Bytes(), "application/json")
	if err != nil {
		t.Fatal(err)
	}
	v := &models.RemotionVersion{Hash: m.Hash(), Parent: parent, Kind: kind, Manifest: mobj, By: a.Actor, At: time.Now().UTC(), Lock: strings.Repeat("1", 64),
		PackagesAllowed: true, Read: &models.RemotionRead{OK: true}, Compositions: []models.RemotionComposition{{ID: "Intro", Width: 640, Height: 360, FPS: 30, DurationInFrames: 60, Duration: 2}}}
	got, _, err := insertVersion(ctx, a, s, v)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// setKey gives the test home an ElevenLabs key in its .env (never the keychain).
func setKey(t *testing.T, a *app.App, key string) {
	t.Helper()
	if err := os.WriteFile(config.DotEnvPath(a.Cfg.Home()), []byte("METARUSH_ELEVENLABS_API_KEY="+key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

const pkgWithScripts = `{
  "name": "refresh-film",
  "private": true,
  "scripts": {
    "make-data": "node scripts/make-data.mjs",
    "voice": "node scripts/fake-voice.mjs",
    "render": "remotion render Intro out/intro.mp4",
    "snoop": "node scripts/snoop.mjs"
  }
}`

const lockWithTools = `{"lockfileVersion": 3, "packages": {"": {"name": "refresh-film"}, "node_modules/tsx": {"version": "4.0.0", "bin": {"tsx": "dist/cli.mjs"}}, "node_modules/tsx/node_modules/inner": {"version": "1.0.0", "bin": {"inner": "x.js"}}}}`

func TestRefreshPlanSaysEverythingFirst(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	s := newSource(t, a, "planfilm")
	v := versionOf(t, a, s, map[string]string{
		"package.json":           pkgWithScripts,
		"package-lock.json":      lockWithTools,
		"scripts/make-data.mjs":  "const url = 'https://stats.example.org/api';\nconsole.log(process.env.CHILD_NAME ?? 'Leo');\n",
		"scripts/fake-voice.mjs": "const key = process.env.ELEVENLABS_API_KEY;\nfetch('https://api.elevenlabs.io/v1/x');\nconst ns = 'http://www.w3.org/2000/svg';\n",
		"scripts/x.ts":           "export {};\n",
		"src/index.ts":           "import { registerRoot } from 'remotion';\n",
		"public/picture.png":     "png",
	}, "", models.RemotionKindImport)
	plan := func(in RemotionRefreshIn) *RemotionRefreshPlanOut {
		t.Helper()
		in.Source = s.Name
		p, err := planRefresh(ctx, a, s, v, &in)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	// Nothing picked: the scripts are offered, nothing can start.
	p := plan(RemotionRefreshIn{})
	if p.Ready || len(p.Steps) != 0 || len(p.Scripts) != 4 || p.Scripts[0].Name != "make-data" || !p.Scripts[2].MakesVideos {
		t.Fatalf("empty plan: %+v", p)
	}
	// Steps as typed arguments, refused when they aren't the project's own.
	p = plan(RemotionRefreshIn{Steps: []string{"npm run make-data", "node scripts/fake-voice.mjs --fast \"two words\"", "npx tsx scripts/x.ts", "npx inner", "npm run nope", "rm -rf /",
		"node -e 1", "node scripts/x.ts", "npm run voice extra"}})
	var runs []string
	for _, st := range p.Steps {
		runs = append(runs, st.Run)
	}
	want := []string{"npm run make-data", "node scripts/fake-voice.mjs --fast two words", "npx --no tsx scripts/x.ts"}
	if strings.Join(runs, "|") != strings.Join(want, "|") {
		t.Fatalf("steps %q", runs)
	}
	if p.Steps[0].Command != "node scripts/make-data.mjs" || p.Steps[1].Args[1] != "two words" {
		t.Errorf("a script's command and quoted arguments: %+v", p.Steps)
	}
	if len(p.Problems) < 6 {
		t.Errorf("refused steps: %q", p.Problems)
	}
	for _, needle := range []string{"no tool called “inner”", "no script called “nope”", "isn't a step MetaRush runs", "never with options", "runs JavaScript files", "go after --"} {
		if !strings.Contains(strings.Join(p.Problems, " | "), needle) {
			t.Errorf("no problem saying %q: %q", needle, p.Problems)
		}
	}
	// Keys: only the ones picked, under names that don't steer programs; the name the code reads is suggested.
	p = plan(RemotionRefreshIn{Steps: []string{"npm run voice"}, Keys: []models.RemotionKey{{Provider: "elevenlabs"}}})
	if len(p.Keys) != 1 || p.Keys[0].Env != "ELEVENLABS_API_KEY" || !p.Keys[0].Read || p.Keys[0].Configured {
		t.Fatalf("suggested key: %+v", p.Keys)
	}
	if p.Ready || !strings.Contains(strings.Join(p.Problems, " "), "Settings → Integrations") {
		t.Errorf("a key this computer doesn't have stops it: %q", p.Problems)
	}
	for _, bad := range []string{"PATH", "NODE_OPTIONS", "DYLD_INSERT_LIBRARIES", "METARUSH_MONGODB_URI", "lower_case", "npm_config_registry"} {
		q := plan(RemotionRefreshIn{Steps: []string{"npm run voice"}, Keys: []models.RemotionKey{{Provider: "elevenlabs", Env: bad}}})
		if len(q.Keys) != 0 || q.Ready {
			t.Errorf("a key under %s: %+v", bad, q.Keys)
		}
	}
	if q := plan(RemotionRefreshIn{Steps: []string{"npm run voice"}, Keys: []models.RemotionKey{{Provider: "elevenlabs", Env: "A_KEY"}, {Provider: "gemini", Env: "A_KEY"}}}); len(q.Keys) != 1 {
		t.Errorf("two keys under one name: %+v", q.Keys)
	}
	setKey(t, a, fakeKey())
	p = plan(RemotionRefreshIn{Steps: []string{"npm run voice"}, Keys: []models.RemotionKey{{Provider: "elevenlabs", Env: "ELEVENLABS_API_KEY"}}, Outputs: []string{"public/voice/", "src/generated/"}})
	if !p.Keys[0].Configured || !strings.HasPrefix(p.Keys[0].Masked, "••••") || strings.Contains(p.Keys[0].Masked, "f00df00d") {
		t.Errorf("the key shown masked: %+v", p.Keys[0])
	}
	// What else the code reads, the hosts it names (not namespaces), the key's service.
	reads := map[string]bool{}
	for _, r := range p.Reads {
		reads[r.Name] = r.Set
	}
	if set, ok := reads["CHILD_NAME"]; !ok || set || !reads["ELEVENLABS_API_KEY"] {
		t.Errorf("reads: %+v", p.Reads)
	}
	hosts := map[string]string{}
	for _, h := range p.Hosts {
		hosts[h.Host] = h.Why
	}
	if !strings.Contains(hosts["stats.example.org"], "scripts/make-data.mjs") || hosts["api.elevenlabs.io"] == "" || hosts["www.w3.org"] != "" {
		t.Errorf("hosts: %v", hosts)
	}
	if runtime.GOOS == "darwin" && (!p.Ready || p.Isolation != isoMac) {
		t.Fatalf("ready on a Mac: %q %s", p.Problems, p.Isolation)
	}
	// The fingerprint is what the person saw.
	again := plan(RemotionRefreshIn{Steps: []string{"npm run voice"}, Keys: []models.RemotionKey{{Provider: "elevenlabs", Env: "ELEVENLABS_API_KEY"}}, Outputs: []string{"public/voice/", "src/generated/"}})
	other := plan(RemotionRefreshIn{Steps: []string{"npm run voice"}, Keys: []models.RemotionKey{{Provider: "elevenlabs", Env: "ELEVENLABS_API_KEY"}}, Outputs: []string{"public/"}})
	if again.Fingerprint != p.Fingerprint || other.Fingerprint == p.Fingerprint {
		t.Error("the fingerprint follows the plan")
	}
	setKey(t, a, "sk_"+strings.Repeat("beef", 10))
	if changed := plan(RemotionRefreshIn{Steps: []string{"npm run voice"}, Keys: []models.RemotionKey{{Provider: "elevenlabs", Env: "ELEVENLABS_API_KEY"}}, Outputs: []string{"public/voice/", "src/generated/"}}); changed.Fingerprint == p.Fingerprint {
		t.Error("another key on this computer is another plan")
	}
	// Outputs that aren't places in the project.
	for _, bad := range []string{"../x", "/etc", "node_modules/", ".git/hooks", "out/", "**/*.png"} {
		if q := plan(RemotionRefreshIn{Steps: []string{"npm run voice"}, Outputs: []string{bad}}); q.Ready || len(q.Outputs) != 0 {
			t.Errorf("output %q: %+v", bad, q.Outputs)
		}
	}
	if d := plan(RemotionRefreshIn{Steps: []string{"npm run voice"}}); strings.Join(d.Outputs, ",") != "public/" {
		t.Errorf("default outputs: %q", d.Outputs)
	}
}

func TestOutputsAreTakenByTheImportsRules(t *testing.T) {
	m, err := newOutputMatcher([]string{"public/voice/", "src/generated", "public/**/*.wav", "data/one.json"})
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]bool{"public/voice/a.mp3": true, "public/voice/x/y.png": true, "src/generated/v.json": true, "src/generated": true, "src/generatedx.json": false,
		"public/music/bed.wav": true, "public/bed.wav": true, "public/a.mp3": false, "data/one.json": true, "data/two.json": false, "src/story.ts": false} {
		if m.match(p) != want {
			t.Errorf("match(%q) = %v", p, !want)
		}
	}
	work := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(work, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base := NewManifest([]ManifestFile{{Path: "src/story.ts", Size: 3, SHA256: sha256Hex([]byte("old"))}, {Path: "public/voice/old.wav", Size: 3, SHA256: sha256Hex([]byte("old"))},
		{Path: "src/keep.ts", Size: 3, SHA256: sha256Hex([]byte("old"))}})
	write("src/story.ts", "new") // outside the outputs
	write("src/keep.ts", "old")
	write("public/voice/line.wav", "RIFF")
	write("public/voice/.env.local", "A=1")          // a secret's name
	write("public/voice/leak.txt", "key "+fakeKey()) // a secret's shape
	write("public/voice/node_modules/x.js", "x")     // packages: never
	write("src/generated/voice.json", `{"words":["hello"]}`)
	// A link out of the copy, and a folder linked elsewhere (review 5).
	elsewhere := t.TempDir()
	_ = os.WriteFile(filepath.Join(elsewhere, "private.wav"), []byte("PRIVATE"), 0o644)
	_ = os.Symlink(filepath.Join(elsewhere, "private.wav"), filepath.Join(work, "public", "voice", "link.wav"))
	_ = os.Symlink(elsewhere, filepath.Join(work, "src", "generated", "linked"))
	take := t.TempDir()
	outs, left, err := collectOutputs(work, take, base, []string{"public/voice/", "src/generated/"})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range outs {
		if o.removed {
			continue
		}
		if !strings.HasPrefix(o.local, take+string(filepath.Separator)) {
			t.Errorf("%s is taken from the steps' folder, not MetaRush's copy: %s", o.Path, o.local)
		}
		if b, _ := os.ReadFile(o.local); strings.Contains(string(b), "PRIVATE") {
			t.Errorf("%s holds a file from outside the copy", o.Path)
		}
	}
	// The copies stay as read when the steps' folder changes afterwards.
	write("public/voice/line.wav", "CHANGED")
	for _, o := range outs {
		if o.Path == "public/voice/line.wav" {
			if b, _ := os.ReadFile(o.local); string(b) != "RIFF" {
				t.Errorf("the copy follows the steps' folder: %q", b)
			}
		}
	}
	var got []string
	for _, o := range outs {
		got = append(got, o.change+" "+o.Path)
	}
	sort.Strings(got)
	if strings.Join(got, "|") != "added public/voice/line.wav|added src/generated/voice.json|removed public/voice/old.wav" {
		t.Errorf("taken: %q", got)
	}
	why := map[string]string{}
	for _, l := range left {
		why[l.Path] = l.Why
	}
	if why["src/story.ts"] == "" || why["public/voice/.env.local"] != whySecretName || why["public/voice/leak.txt"] != whySecretText || why["src/keep.ts"] != "" ||
		why["public/voice/link.wav"] != whyLink {
		t.Errorf("left out: %v", why)
	}
}

func TestRefreshEnvironmentIsBuiltFromNothing(t *testing.T) {
	t.Setenv("METARUSH_MONGODB_URI", "mongodb://user:pw@db.example/x") // secret-scan: allow (a made-up address)
	t.Setenv("AWS_SECRET_ACCESS_KEY", "abc")
	env := refreshEnv("/opt/node/bin/node", "/r/work", "/r/home", "/r/tmp", "/c", []string{"ELEVENLABS_API_KEY=" + fakeKey()})
	names := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		names[k] = v
	}
	if names["HOME"] != "/r/home" || names["TMPDIR"] != "/r/tmp" || !strings.HasPrefix(names["PATH"], "/opt/node/bin:/r/work/node_modules/.bin:") || names["ELEVENLABS_API_KEY"] != fakeKey() {
		t.Errorf("env: %v", names)
	}
	for k := range names {
		if strings.HasPrefix(k, "METARUSH") || strings.HasPrefix(k, "AWS") || k == "SSH_AUTH_SOCK" {
			t.Errorf("%s reached the steps", k)
		}
	}
}

func TestMacProfileHidesAndAllows(t *testing.T) {
	ap := &apart{Root: "/r", Cache: "/home/me/.metarush/cache/remotion/refresh/x", Read: []string{"/home/me/.local/node"}, Hide: []string{"/home/me", "/home/me/.metarush", "/src/teaser"}}
	p := macProfile(ap)
	order := []string{`(deny file-write* (subpath "/"))`, `(deny file-read-data file-read-xattr file-write* (subpath "/home/me")`, `(allow file-read-data file-read-xattr (subpath "/home/me/.local/node"))`,
		`(allow file-read* file-write* (subpath "/r") (subpath "/home/me/.metarush/cache/remotion/refresh/x"))`, `com.apple.SecurityServer`}
	at := -1
	for _, o := range order {
		i := strings.Index(p, o)
		if i <= at {
			t.Fatalf("%q missing or out of order in\n%s", o, p)
		}
		at = i
	}
	args := bwrapArgs(&apart{Root: "/r", Cache: "/c", Work: "/r/work", Modules: "/env/node_modules", Hide: []string{os.TempDir()}}, -1)
	joined := strings.Join(args, " ")
	for _, want := range []string{"--ro-bind / /", "--tmpfs " + os.TempDir(), "--bind /r /r", "--bind /c /c", "--ro-bind /env/node_modules /r/work/node_modules", "--chdir /r/work"} {
		if !strings.Contains(joined, want) {
			t.Errorf("bwrap args lack %q: %s", want, joined)
		}
	}
}

// refreshProject is a project whose own steps write data, a picture and a
// fake voice, and one step that tries to reach what it never may.
func refreshProject(snoop string) map[string]string {
	return map[string]string{
		"package.json":      `{"name": "refresh-film", "private": true, "scripts": {"make-data": "node scripts/make-data.mjs", "voice": "node scripts/fake-voice.mjs", "fail": "node scripts/fail.mjs"}}`,
		"package-lock.json": `{"lockfileVersion": 3, "packages": {"": {"name": "refresh-film"}}}`,
		"src/index.ts":      "export {};\n",
		"src/story.ts":      "export const HEADLINE = \"Hello\";\n",
		"scripts/make-data.mjs": `import fs from 'node:fs';
fs.mkdirSync('src/generated', { recursive: true });
fs.writeFileSync('src/generated/stats.json', JSON.stringify({ visitors: 42 }) + '\n');
fs.mkdirSync('public/refresh', { recursive: true });
// A 1×1 picture.
fs.writeFileSync('public/refresh/chart.png', Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64'));
fs.writeFileSync('notes.txt', 'written outside the outputs\n');
console.log('made the data');
`,
		"scripts/fake-voice.mjs": `import fs from 'node:fs';
const key = process.env.ELEVENLABS_API_KEY;
if (!key) { console.error('ELEVENLABS_API_KEY is not set'); process.exit(1); }
console.log('using key ' + key);
fs.mkdirSync('public/voice', { recursive: true });
const n = 800, b = Buffer.alloc(44 + n * 2);
b.write('RIFF', 0); b.writeUInt32LE(36 + n * 2, 4); b.write('WAVE', 8); b.write('fmt ', 12); b.writeUInt32LE(16, 16); b.writeUInt16LE(1, 20); b.writeUInt16LE(1, 22);
b.writeUInt32LE(8000, 24); b.writeUInt32LE(16000, 28); b.writeUInt16LE(2, 32); b.writeUInt16LE(16, 34); b.write('data', 36); b.writeUInt32LE(n * 2, 40);
for (let i = 0; i < n; i++) b.writeInt16LE(Math.round(8000 * Math.sin(i / 4)), 44 + i * 2);
fs.writeFileSync('public/voice/line.wav', b);
fs.writeFileSync('public/voice/leak.txt', 'the key was ' + key + '\n');
fs.mkdirSync('src/generated', { recursive: true });
fs.writeFileSync('src/generated/voice.json', JSON.stringify({ words: ['Hello', 'there'] }) + '\n');
`,
		"scripts/fail.mjs":  "console.log('starting');\nconsole.error('the voice service said no (a made-up failure)');\nprocess.exit(3);\n",
		"scripts/snoop.mjs": snoop,
	}
}

const snoopScript = `import fs from 'node:fs';
import { execFileSync } from 'node:child_process';
const [mrHome, original, outside] = process.argv.slice(2);
const tryIt = (f) => { try { f(); return 'allowed'; } catch (e) { return e.code || 'refused'; } };
const found = {
  config: tryIt(() => fs.readFileSync(mrHome + '/config.yaml')),
  envFile: tryIt(() => fs.readFileSync(mrHome + '/.env')),
  original: tryIt(() => fs.readFileSync(original + '/.env')),
  outside: tryIt(() => fs.writeFileSync(outside + '/written.txt', 'x')),
  keychain: tryIt(() => execFileSync('/usr/bin/security', ['list-keychains'], { stdio: 'pipe' })),
  own: tryIt(() => fs.writeFileSync('public/refresh/own.txt', 'mine')),
  home: process.env.HOME,
  env: Object.keys(process.env).sort(),
};
fs.mkdirSync('public/refresh', { recursive: true });
fs.writeFileSync('public/refresh/snoop.json', JSON.stringify(found, null, 1));
console.log('snooped');
`

// testRefresher runs refreshes without the renderer: no packages, a stand-in check.
func testRefresher(a *app.App, checked *[]string) *refresher {
	rf := newRefresher(a)
	rf.modules = func(context.Context, *Manifest, func(string)) (string, error) { return "", nil }
	rf.check = func(_ context.Context, _ *models.RemotionSource, v *models.RemotionVersion, _ func(string)) (string, error) {
		*checked = append(*checked, v.Hash)
		return "checked", nil
	}
	return rf
}

func needsSandbox(t *testing.T, a *app.App) {
	t.Helper()
	if runtime.GOOS != "darwin" || !a.Tools.Has(tools.SandboxExec) || !a.Tools.Has(tools.Node) {
		t.Skip("refreshes run here only on a Mac with sandbox-exec and Node.js (bubblewrap on Linux is checked by its arguments)")
	}
}

func startTestRefresh(t *testing.T, a *app.App, s *models.RemotionSource, in RemotionRefreshIn) *models.RemotionRefresh {
	t.Helper()
	ctx := context.Background()
	in.Source = s.Name
	fresh, err := getSource(ctx, a, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	head, _ := getVersion(ctx, a, fresh.Head)
	p, err := planRefresh(ctx, a, fresh, head, &in)
	if err != nil || !p.Ready {
		t.Fatalf("plan: %v %q", err, p.Problems)
	}
	out, err := startRefresh(ctx, a, &RemotionRefreshStartIn{RemotionRefreshIn: in, Plan: p.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	r, err := getRefresh(ctx, a, out.ID)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRefreshRunsItsStepsApartAndMakesAVersion(t *testing.T) {
	a := testapp.New(t)
	needsSandbox(t, a)
	ctx := context.Background()
	key := fakeKey()
	setKey(t, a, key)
	original := t.TempDir()
	if err := os.WriteFile(filepath.Join(original, ".env"), []byte("SECRET=never-read\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	files := refreshProject(snoopScript)
	files["package.json"] = strings.Replace(files["package.json"], `"fail": "node scripts/fail.mjs"`, `"fail": "node scripts/fail.mjs", "snoop": "node scripts/snoop.mjs"`, 1)
	s := newSource(t, a, "refreshfilm")
	s.Origin = models.RemotionOrigin{Kind: models.RemotionFromFolder, Folder: "refresh-film", Path: original, Machine: a.Cfg.Installation.Name}
	if err := a.DB.Update(ctx, "remotion_sources", a.WorkspaceID(), s.ID, map[string]any{"$set": map[string]any{"origin": s.Origin}}); err != nil {
		t.Fatal(err)
	}
	v1 := versionOf(t, a, s, files, "", models.RemotionKindImport)
	r := startTestRefresh(t, a, s, RemotionRefreshIn{
		Steps:   []string{"npm run make-data", "npm run voice", "node scripts/snoop.mjs " + a.Cfg.Home() + " " + original + " " + outside},
		Keys:    []models.RemotionKey{{Provider: "elevenlabs", Env: "ELEVENLABS_API_KEY"}},
		Outputs: []string{"public/", "src/generated/"}})
	if r.State != models.RemotionRunQueued || r.Number != 1 || r.From != v1.Hash || r.Isolation != isoMac {
		t.Fatalf("recorded: %+v", r)
	}
	var checked []string
	var said []string
	res, err := testRefresher(a, &checked).run(ctx, r.ID, r.JobID, func(_ float64, m string) { said = append(said, m) })
	if err != nil {
		fresh, _ := getRefresh(ctx, a, r.ID)
		t.Fatalf("run: %v (%+v)", err, fresh)
	}
	done, _ := getRefresh(ctx, a, r.ID)
	if done.State != models.RemotionRunDone || len(done.Steps) != 3 || !done.Steps[2].OK || done.VersionNumber != 2 || res["versionNumber"] != 2 {
		t.Fatalf("done: %+v %v", done, res)
	}
	if !strings.Contains(strings.Join(said, " | "), "step 2 of 3: “voice”") {
		t.Errorf("progress: %q", said)
	}
	nv, err := getVersion(ctx, a, done.Version)
	if err != nil {
		t.Fatal(err)
	}
	if nv.Kind != models.RemotionKindRefresh || nv.Parent != v1.Hash || !strings.Contains(nv.Message, "npm run voice") || len(checked) != 1 || checked[0] != nv.Hash {
		t.Fatalf("the new version: %+v (checked %v)", nv, checked)
	}
	m, _ := loadManifest(ctx, a, nv)
	for _, p := range []string{"src/generated/stats.json", "src/generated/voice.json", "public/refresh/chart.png", "public/voice/line.wav", "public/refresh/snoop.json", "src/story.ts"} {
		if _, ok := m.File(p); !ok {
			t.Errorf("the new version lacks %s", p)
		}
	}
	for _, p := range []string{"notes.txt", "public/voice/leak.txt"} {
		if _, ok := m.File(p); ok {
			t.Errorf("%s came into the version", p)
		}
	}
	why := map[string]string{}
	for _, l := range done.LeftOut {
		why[l.Path] = l.Why
	}
	if !strings.Contains(why["notes.txt"], "outside") || why["public/voice/leak.txt"] != whySecretText {
		t.Errorf("left out: %v", why)
	}
	// What the step that tried everything found.
	sf, _ := m.File("public/refresh/snoop.json")
	b, _ := readObject(ctx, a, sf.SHA256, sf.Size)
	var found struct {
		Config, EnvFile, Original, Outside, Keychain, Own, Home string
		Env                                                     []string
	}
	if err := json.Unmarshal(b, &found); err != nil {
		t.Fatal(err)
	}
	if found.Config == "allowed" || found.EnvFile == "allowed" || found.Original == "allowed" || found.Outside == "allowed" || found.Keychain == "allowed" || found.Own != "allowed" {
		t.Errorf("what the step could do: %+v", found)
	}
	if real, _ := os.UserHomeDir(); found.Home == real || !strings.Contains(found.Home, "metarush-refresh-") {
		t.Errorf("HOME was %s", found.Home)
	}
	for _, k := range found.Env {
		if strings.HasPrefix(k, "METARUSH") || k == "SSH_AUTH_SOCK" || k == "MONGODB_URI" {
			t.Errorf("%s reached the step", k)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "written.txt")); err == nil {
		t.Error("the step wrote outside its copy")
	}
	// The log: kept, the key hidden.
	lg, err := refreshLogOp(ctx, a, &RemotionRefreshLogIn{Source: s.Name, Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lg.Text, "made the data") || !strings.Contains(lg.Text, "using key [redacted]") || strings.Contains(lg.Text, key) || !strings.Contains(lg.Text, "step 3 of 3") {
		t.Errorf("log:\n%s", lg.Text)
	}
	// The refresh steps are remembered for the next plan.
	fresh, _ := getSource(ctx, a, s.ID)
	if fresh.Refresh == nil || len(fresh.Refresh.Steps) != 3 || fresh.Refreshes != 1 {
		t.Errorf("remembered: %+v", fresh.Refresh)
	}
	head, _ := getVersion(ctx, a, fresh.Head)
	p, _ := planRefresh(ctx, a, fresh, head, &RemotionRefreshIn{Source: s.Name})
	if !p.Remembered || len(p.Steps) != 3 || len(p.Keys) != 1 {
		t.Errorf("the next plan starts from it: %+v", p)
	}
	// A refresh that changes nothing makes no version.
	r2 := startTestRefresh(t, a, s, RemotionRefreshIn{Steps: []string{"npm run make-data"}, Outputs: []string{"src/generated/"}})
	if _, err := testRefresher(a, &checked).run(ctx, r2.ID, r2.JobID, nil); err != nil {
		t.Fatal(err)
	}
	if d2, _ := getRefresh(ctx, a, r2.ID); d2.Version != "" || !strings.Contains(d2.Summary, "no new version") {
		t.Errorf("nothing changed: %+v", d2)
	}
}

func TestRefreshStepFailureKeepsNothingAndIsNeverRunAgain(t *testing.T) {
	a := testapp.New(t)
	needsSandbox(t, a)
	ctx := context.Background()
	s := newSource(t, a, "failfilm")
	versionOf(t, a, s, refreshProject("console.log(1)\n"), "", models.RemotionKindImport)
	r := startTestRefresh(t, a, s, RemotionRefreshIn{Steps: []string{"npm run make-data", "npm run fail", "npm run make-data"}, Outputs: []string{"public/"}})
	var checked []string
	_, err := testRefresher(a, &checked).run(ctx, r.ID, r.JobID, nil)
	if err == nil || !IsFinal(err) {
		t.Fatalf("a failing step: %v", err)
	}
	got, _ := getRefresh(ctx, a, r.ID)
	if got.State != models.RemotionRunFailed || len(got.Steps) != 2 || got.Steps[1].OK || got.Steps[1].Exit != 3 ||
		!strings.Contains(got.Error, "Step 2 of 3 (npm run fail)") || !strings.Contains(got.Error, "said no") || got.Version != "" || len(checked) != 0 {
		t.Fatalf("failed: %+v", got)
	}
	if fresh, _ := getSource(ctx, a, s.ID); fresh.HeadNumber != 1 {
		t.Error("a failed refresh made a version")
	}
	// A refresh whose steps began and were cut short is never run again by itself.
	r2 := startTestRefresh(t, a, s, RemotionRefreshIn{Steps: []string{"npm run make-data"}, Outputs: []string{"public/"}})
	_ = setRefresh(ctx, a, r2.ID, map[string]any{"ran": true, "state": models.RemotionRunRunning})
	if _, err := testRefresher(a, &checked).run(ctx, r2.ID, r2.JobID, nil); err == nil || !strings.Contains(err.Error(), "stopped before it finished") {
		t.Errorf("run again: %v", err)
	}
}

func TestRefreshIsPeopleOnly(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	s := newSource(t, a, "peoplefilm")
	versionOf(t, a, s, refreshProject("console.log(1)\n"), "", models.RemotionKindImport)
	in, _ := json.Marshal(RemotionRefreshStartIn{RemotionRefreshIn: RemotionRefreshIn{Source: s.Name, Steps: []string{"npm run make-data"}}, Plan: strings.Repeat("0", 64)})
	if _, err := ops.Call(ctx, a, "remotion.refresh.start", in, ops.Caller{}); apperr.From(err).Code != apperr.HumanOnly {
		t.Errorf("without a person: %v", err)
	}
	ca := *a
	ca.Actor.Via = models.ViaAssistant
	if _, err := ops.Call(ctx, &ca, "remotion.refresh.start", in, ops.Caller{Human: true}); apperr.From(err).Code != apperr.Unauthorized {
		t.Errorf("from Claude: %v", err)
	}
	// A person, but a plan that isn't the one shown.
	if runtime.GOOS == "darwin" && a.Tools.Has(tools.SandboxExec) {
		if _, err := ops.Call(ctx, a, "remotion.refresh.start", in, ops.Caller{Human: true}); apperr.From(err).Code != apperr.Conflict {
			t.Errorf("another plan: %v", err)
		}
	}
	// Reading the plan is fine for anyone.
	raw, _ := json.Marshal(RemotionRefreshIn{Source: s.Name, Steps: []string{"npm run make-data"}})
	if _, err := ops.Call(ctx, &ca, "remotion.refresh.plan", raw, ops.Caller{}); err != nil {
		t.Errorf("Claude reads the plan: %v", err)
	}
}

// TestRefreshStepsNeverReachMetaRush: a refresh's step keeps the network
// (D174) but never reaches this computer's services — the database's port,
// the dashboard's port, any loopback port, the database's socket — and
// listens for nobody (D235's note on D174, review 7). On Linux the steps get
// a network of their own behind MetaRush's proxy.
func TestRefreshStepsNeverReachMetaRush(t *testing.T) {
	a := testapp.New(t)
	dash, err := net.Listen("tcp", "127.0.0.1:0") // stands for the dashboard
	if err != nil {
		t.Fatal(err)
	}
	defer dash.Close()
	a.Cfg.Dashboard.Port = dash.Addr().(*net.TCPAddr).Port
	ports := ownPorts(a)
	db := localDBPorts(a.Cfg.Database.URI)
	if len(db) == 0 || !slices.Contains(ports, db[0]) || !slices.Contains(ports, a.Cfg.Dashboard.Port) {
		t.Fatalf("MetaRush's own ports: %v (the database's: %v)", ports, db)
	}
	if got := localDBPorts("mongodb+srv://user:pw@cluster.example.net/x"); len(got) != 0 { // secret-scan: allow (made up)
		t.Errorf("a database elsewhere isn't this computer's: %v", got)
	}
	// Linux: a network of its own, the forwarder in front of the step.
	ap := &apart{Root: "/r", Work: "/r/work", Ports: ports, ProxyPort: 4711, ProxySocket: "/r/proxy/proxy.sock", Self: "/opt/metarush"}
	_, args, err := ap.command(isoLinux, "/usr/bin/node", []string{"x.js"})
	if j := strings.Join(args, " "); err != nil || !strings.Contains(j, "--unshare-net") || !strings.Contains(j, "sandbox-forward --listen 127.0.0.1:4711 --to /r/proxy/proxy.sock -- /usr/bin/node x.js") {
		t.Errorf("Linux: %v %s", err, j)
	}
	if runtime.GOOS != "darwin" || !a.Tools.Has(tools.SandboxExec) || !a.Tools.Has(tools.Node) {
		t.Skip("steps run here only on a Mac with sandbox-exec and Node.js")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	ap = &apart{Root: root, Work: root, Read: readableFor(a.Tools.Path(tools.Node)), Hide: hiddenFor(a, ""), Ports: ports}
	script := `const net = require("net");
const tryPort = (p) => new Promise((ok) => { const s = net.connect(p, "127.0.0.1"); s.on("connect", () => { s.destroy(); ok(p + " REACHED"); }); s.on("error", (e) => ok(p + " refused " + e.code)); });
const listen = () => new Promise((ok) => { const srv = net.createServer(); srv.on("error", (e) => ok("listen refused " + e.code)); srv.listen(0, "0.0.0.0", () => { srv.close(); ok("LISTENED"); }); });
(async () => { for (const p of process.argv.slice(1)) console.log(await tryPort(Number(p))); console.log(await listen()); })();`
	args2 := []string{"-e", script}
	for _, p := range []int{db[0], a.Cfg.Dashboard.Port} {
		args2 = append(args2, strconv.Itoa(p))
	}
	tool, argv, err := ap.command(isoMac, a.Tools.Path(tools.Node), args2)
	if err != nil {
		t.Fatal(err)
	}
	res, _ := a.Tools.Run(context.Background(), tools.Spec{Tool: tool, Args: argv, Dir: root, CleanEnv: true, Env: refreshEnv(a.Tools.Path(tools.Node), root, root, root, root, nil), Contained: true})
	out := ""
	if res != nil {
		out = string(res.Stdout) + string(res.Stderr)
	}
	if strings.Contains(out, "REACHED") || strings.Contains(out, "LISTENED") || strings.Count(out, "refused") < 3 {
		t.Fatalf("a step reached MetaRush's services or listened:\n%s", out)
	}
}
