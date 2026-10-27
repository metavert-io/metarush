package components

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/testutil/fakebrew"
	"github.com/metavert-io/metarush/internal/testutil/fakenode"
	"github.com/metavert-io/metarush/internal/tools"
)

// testEnv is a home whose computer has only what the test puts in bin
// (METARUSH_TOOL_DIRS), with its own models folder.
func testEnv(t *testing.T) (*Env, string) {
	t.Helper()
	h := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(tools.EnvToolDirs, bin)
	t.Setenv(EnvBase, "")
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Tools.ModelsDir = filepath.Join(h, "models")
	cfg.Media = config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: filepath.Join(h, "media")}}
	if err := config.Save(h, cfg); err != nil {
		t.Fatal(err)
	}
	return &Env{Cfg: cfg, Tools: tools.New(cfg.Tools)}, bin
}

func program(t *testing.T, dir, name, says string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho '"+says+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// The registry is whole: MetaRush needs only itself, a place for projects
// and a storage folder; every optional component says in plain words what
// it unlocks, how big it is and how it is added on a Mac and on Linux; the
// renderer says Remotion's licence; the pins are checked downloads.
func TestRegistryIsWhole(t *testing.T) {
	var required []string
	seen := map[string]bool{}
	for _, c := range All() {
		if seen[c.Name] {
			t.Errorf("%s twice", c.Name)
		}
		seen[c.Name] = true
		if c.Required {
			required = append(required, c.Name)
			continue
		}
		if c.Title == "" || c.Thing == "" || c.Feature == "" || c.Unlocks == "" || len(c.Features) == 0 {
			t.Errorf("%s lacks words: %+v", c.Name, c)
		}
		if c.DownloadMB <= 0 && c.DiskMB <= 0 && c.Name != "sandbox" {
			t.Errorf("%s has no size", c.Name)
		}
		for _, n := range c.Needs {
			if nc, ok := Get(n); !ok || nc.Required {
				t.Errorf("%s needs %s, which isn't an optional component", c.Name, n)
			}
		}
		for _, goos := range []string{"darwin", "linux"} {
			if !c.on(goos) {
				continue
			}
			// Without Homebrew and without a pin there is still a way: a
			// step, or words for a person.
			found := false
			for _, w := range c.ways {
				if len(w.Systems) == 0 || slices.Contains(w.Systems, goos) {
					if w.Step != "" || w.Says != "" || len(w.Pins) > 0 {
						found = true
					}
				}
			}
			if !found && len(c.ways) > 0 {
				t.Errorf("%s has no way to be added on %s without Homebrew", c.Name, goos)
			}
			if len(c.ways) == 0 {
				t.Errorf("%s has no way to be added", c.Name)
			}
		}
		for _, w := range c.ways {
			for _, id := range w.Pins {
				if !slices.ContainsFunc(pins, func(p Pin) bool { return p.ID == id }) {
					t.Errorf("%s names pin %s, which doesn't exist", c.Name, id)
				}
			}
		}
	}
	if !slices.Equal(required, []string{"metarush", "database", "storage"}) {
		t.Errorf("required: %v (only the program, a place for projects and a storage folder)", required)
	}
	r, _ := Get("renderer")
	if !strings.Contains(r.Licence, "Remotion Company License") || !strings.Contains(r.Licence, "3 people") {
		t.Errorf("the renderer doesn't say Remotion's licence: %q", r.Licence)
	}
	var suggested []string
	for _, c := range All() {
		if c.Suggested {
			suggested = append(suggested, c.Name)
		}
	}
	for _, want := range []string{"ffmpeg", "node", "renderer", "transcription", "link-import", "pdf-pages"} {
		if !slices.Contains(suggested, want) {
			t.Errorf("%s should be suggested", want)
		}
	}
	for _, heavy := range []string{"other-languages", "studio-voice", "ai-models", "google-drive", "tailscale", "claude-code"} {
		if slices.Contains(suggested, heavy) {
			t.Errorf("%s shouldn't be suggested (heavy, or a person's own choice)", heavy)
		}
	}
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, p := range pins {
		if !hex64.MatchString(p.SHA256) || !strings.HasPrefix(p.URL, "https://") || p.Bytes <= 0 || p.Title == "" || p.Licence == "" {
			t.Errorf("pin %s (%s) isn't a checked download", p.ID, p.URL)
		}
		if !strings.HasPrefix(p.To, "tools/") && !strings.HasPrefix(p.To, "models/") {
			t.Errorf("pin %s goes to %s (only the home's tools folder or the models folder)", p.ID, p.To)
		}
		if archive(p.URL) == (p.Take == "") {
			t.Errorf("pin %s: an archive keeps a part of itself, a file is kept whole", p.ID)
		}
		if p.Config != "" && toolSetting(config.Tools{Node: "x", FFmpeg: "x", FFprobe: "x", YtDlp: "x", SherpaDir: "x"}, p.Config) == "" {
			t.Errorf("pin %s sets an unknown setting %s", p.ID, p.Config)
		}
	}
	for _, a := range []string{"remotion", "yt-dlp", "poppler", "whisper"} {
		if _, ok := Get(a); !ok {
			t.Errorf("%s isn't a name people can type", a)
		}
	}
}

// Every word a component says is plain (skills/_shared/voice.md).
func TestComponentWordsArePlain(t *testing.T) {
	e, _ := testEnv(t)
	check := func(where, s string) {
		if s == "" {
			return
		}
		for _, f := range plainwords.Check(s) {
			t.Errorf("%s: %s", where, f)
		}
	}
	for _, c := range All() {
		check(c.Name+" title", c.Title)
		check(c.Name+" unlocks", c.Unlocks)
		check(c.Name+" needed", c.Needed())
		check(c.Name+" licence", c.Licence)
		check(c.Name+" remove", c.removeSays)
		for _, f := range c.Features {
			check(c.Name+" feature", f)
		}
		for _, w := range c.ways {
			check(c.Name+" way", w.Says)
		}
		st := e.StateOf(context.Background(), c)
		check(c.Name+" says", st.Says)
	}
}

// A missing program says which component adds it and how, in plain words,
// with the command behind Details (D326).
func TestMissingProgramSaysWhatAddsIt(t *testing.T) {
	err := &tools.ErrMissing{Name: tools.FFprobe}
	if !strings.Contains(err.Error(), "FFmpeg") || !strings.Contains(err.Error(), "Settings → Components") {
		t.Fatalf("message: %q", err.Error())
	}
	ae := apperr.From(err)
	if ae.Code != apperr.ToolMissing {
		t.Fatalf("code %s", ae.Code)
	}
	d, _ := ae.Details.(map[string]any)
	if d["component"] != "ffmpeg" || d["command"] != "metarush components add ffmpeg" {
		t.Fatalf("details: %v", ae.Details)
	}
	if m := Missing("renderer"); m.Message != "Rendering needs the Remotion renderer (about 400 MB). Add it in Settings → Components." {
		t.Fatalf("renderer: %q", m.Message)
	}
	// A program no component brings keeps the old words.
	if !strings.Contains((&tools.ErrMissing{Name: tools.Launchctl}).Error(), "is not installed") {
		t.Fatal("launchctl")
	}
}

// Detection lives in one place: with nothing on the computer only the
// program itself is ready among what it needs to run without setup, and a
// program put there is found.
func TestDetection(t *testing.T) {
	e, bin := testEnv(t)
	ctx := context.Background()
	for _, n := range []string{"ffmpeg", "node", "transcription", "link-import", "pdf-pages", "google-drive"} {
		if e.Ready(ctx, n) {
			t.Errorf("%s ready with nothing on the computer", n)
		}
	}
	if !e.Ready(ctx, "metarush") {
		t.Error("MetaRush itself")
	}
	program(t, bin, "ffmpeg", "ffmpeg version 7.1 Copyright")
	e.Tools.Rescan(e.Cfg.Tools)
	ffmpeg, _ := Get("ffmpeg")
	f := e.Find(ctx, ffmpeg)
	if f.Ready || f.Partly == "" {
		t.Fatalf("ffmpeg without ffprobe: %+v", f)
	}
	program(t, bin, "ffprobe", "ffprobe version 7.1")
	e.Tools.Rescan(e.Cfg.Tools)
	if !e.Ready(ctx, "ffmpeg") {
		t.Fatal("ffmpeg and ffprobe aren't found")
	}
	// What it says next depends on the system, not on the computer the test
	// runs on: a Mac without Homebrew downloads it, Linux leaves it to a person.
	e.GOOS, e.GOARCH = "darwin", "arm64"
	if st := e.StateOf(ctx, ffmpeg); st.Status != StatusReady || st.How != HowDownload || st.Command != "metarush components add ffmpeg" {
		t.Fatalf("state on a Mac: %+v", st)
	}
	e.GOOS, e.GOARCH = "linux", "amd64"
	if st := e.StateOf(ctx, ffmpeg); st.Status != StatusReady || st.How != HowYourself || !strings.HasPrefix(st.Command, "sudo apt install ffmpeg") {
		t.Fatalf("state on Linux: %+v", st)
	}
	e.GOOS, e.GOARCH = "", ""
	program(t, bin, "node", "v16.20.0")
	e.Tools.Rescan(e.Cfg.Tools)
	node, _ := Get("node")
	if f := e.Find(ctx, node); f.Ready || !strings.Contains(f.Partly, "too old") {
		t.Fatalf("an old Node.js: %+v", f)
	}
}

// Homebrew where it is: one call with typed arguments, the programs found
// at once, the choice recorded; adding it again changes nothing.
func TestAddWithHomebrew(t *testing.T) {
	e, bin := testEnv(t)
	brew, brewBin, log := fakebrew.Write(t)
	t.Setenv(tools.EnvToolDirs, bin+string(os.PathListSeparator)+brewBin)
	e.Cfg.Tools.Brew = brew
	if err := config.SetField(e.Home(), []string{"tools", "brew"}, brew); err != nil {
		t.Fatal(err)
	}
	e.Tools = tools.New(e.Cfg.Tools)
	ctx := context.Background()
	pdf, _ := Get("pdf-pages")
	if st := e.StateOf(ctx, pdf); st.How != HowHomebrew || st.Status != StatusMissing {
		t.Fatalf("before: %+v", st)
	}
	var said []string
	changed := 0
	res, err := e.Add(ctx, "poppler", AddOptions{By: "Sam", Say: func(_, m string) { said = append(said, m) }, Changed: func() { changed++ }})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Done || res.How != HowHomebrew || !e.Ready(ctx, "pdf-pages") || changed != 1 || len(said) == 0 {
		t.Fatalf("added: %+v, ready %v, changed %d, said %v", res, e.Ready(ctx, "pdf-pages"), changed, said)
	}
	if calls := fakebrew.Calls(t, log); len(calls) != 1 || calls[0] != "install --formula poppler" {
		t.Fatalf("brew calls: %v", calls)
	}
	if ch, ok := choice.Of(e.Home(), "pdf-pages"); !ok || !ch.Wanted || ch.By != "Sam" {
		t.Fatalf("choice: %+v %v", ch, ok)
	}
	again, err := e.Add(ctx, "pdf-pages", AddOptions{})
	if err != nil || !again.Already || len(fakebrew.Calls(t, log)) != 1 {
		t.Fatalf("again: %+v %v", again, err)
	}
	// Homebrew's copy isn't MetaRush's to remove: it says so, and remembers the choice.
	rm, err := e.Remove(ctx, "pdf-pages", "Sam")
	if err != nil || rm.Removed || !rm.StillHere || !strings.Contains(rm.Says, "Homebrew") {
		t.Fatalf("remove: %+v %v", rm, err)
	}
	if !choice.Declined(e.Home(), "pdf-pages") {
		t.Fatal("the removal isn't remembered")
	}
	// A formula Homebrew can't install fails plainly, with its words behind Details.
	rclone, _ := Get("google-drive")
	saved := rclone.ways
	rclone.ways = []Way{{Brew: []string{"fail-rclone"}}}
	defer func() { rclone.ways = saved }()
	_, err = e.Add(ctx, "google-drive", AddOptions{})
	ae := apperr.From(err)
	if err == nil || ae.Code != apperr.ToolFailed || !strings.Contains(ae.Message, "couldn't be added") {
		t.Fatalf("failing formula: %v", err)
	}
	if st := e.StateOf(ctx, rclone); st.Status != StatusFailed || st.LastError == "" {
		t.Fatalf("after a failure: %+v", st)
	}
}

// Under go test the real Homebrew never runs.
func TestRealHomebrewRefusedUnderTest(t *testing.T) {
	e, bin := testEnv(t)
	program(t, bin, "brew", "Homebrew 4")
	e.Tools.Rescan(e.Cfg.Tools)
	_, err := e.Add(context.Background(), "pdf-pages", AddOptions{})
	if err == nil || !strings.Contains(err.Error(), "Tests never run the real Homebrew") {
		t.Fatalf("got %v", err)
	}
}

func targz(t *testing.T, files map[string]string, links map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		mode := int64(0o644)
		if strings.Contains(name, "/bin/") {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	for name, to := range links {
		if err := tw.WriteHeader(&tar.Header{Name: name, Linkname: to, Typeflag: tar.TypeSymlink, Mode: 0o777}); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = zw.Close()
	return buf.Bytes()
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// MetaRush's own pinned download where Homebrew isn't there: from a server
// on this computer standing in for the publisher's, checked before it is
// unpacked, put in <home>/tools and named in config.yaml; a download that
// isn't the pinned file is refused and nothing is kept; removing it takes
// it all away again.
func TestAddWithAPinnedDownload(t *testing.T) {
	e, _ := testEnv(t)
	ctx := context.Background()
	good := targz(t, map[string]string{
		"node-v22.9.9-test/bin/node":                            "#!/bin/sh\necho v22.9.9\n",
		"node-v22.9.9-test/lib/node_modules/npm/bin/npm-cli.js": "// npm\n",
	}, map[string]string{"node-v22.9.9-test/bin/npm": "../lib/node_modules/npm/bin/npm-cli.js"})
	bad := append([]byte{}, good...)
	bad[len(bad)-1] ^= 1
	serve := map[string][]byte{"/node-good.tar.gz": good, "/node-bad.tar.gz": bad}
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if b, ok := serve[r.URL.Path]; ok {
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv(EnvBase, srv.URL)
	pin := func(file string) []Pin {
		return []Pin{{ID: "node", Title: "Node.js 22.9.9", URL: "https://nodejs.example/" + file, SHA256: sum(good), Bytes: int64(len(good)),
			Take: "node-v22.9.9-test", To: "tools/node", Config: "node", ConfigPath: "bin/node", Licence: "MIT"}}
	}
	e.Pins = pin("node-bad.tar.gz")
	node, _ := Get("node")
	if st := e.StateOf(ctx, node); st.How != HowDownload {
		t.Fatalf("how: %+v", st)
	}
	_, err := e.Add(ctx, "node", AddOptions{})
	if !apperr.Is(err, apperr.Integrity) {
		t.Fatalf("a changed download: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.Home(), "tools", "node")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a changed download was kept")
	}
	e.Pins = pin("node-good.tar.gz")
	res, err := e.Add(ctx, "node", AddOptions{})
	if err != nil || !res.Done || res.How != HowDownload {
		t.Fatalf("add: %+v %v", res, err)
	}
	want := filepath.Join(e.Home(), "tools", "node", "bin", "node")
	if got := e.Tools.Path(tools.Node); got != want {
		t.Fatalf("node is %q, want %q", got, want)
	}
	if npm, err := os.Readlink(filepath.Join(e.Home(), "tools", "node", "bin", "npm")); err != nil || npm != "../lib/node_modules/npm/bin/npm-cli.js" {
		t.Fatalf("npm link: %q %v", npm, err)
	}
	cfg, err := config.Load(e.Home())
	if err != nil || cfg.Tools.Node != want {
		t.Fatalf("config.yaml names %q (%v)", cfg.Tools.Node, err)
	}
	if !slices.Equal(asked, []string{"/node-bad.tar.gz", "/node-good.tar.gz"}) {
		t.Fatalf("downloads: %v", asked)
	}
	rm, err := e.Remove(ctx, "node", "Sam")
	if err != nil || !rm.Removed || rm.StillHere {
		t.Fatalf("remove: %+v %v", rm, err)
	}
	if _, err := os.Stat(filepath.Join(e.Home(), "tools", "node")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the removed component's files are still there")
	}
	if cfg, _ := config.Load(e.Home()); cfg.Tools.Node != "" || e.Tools.Has(tools.Node) {
		t.Fatalf("still pointed at: %q", cfg.Tools.Node)
	}
}

// An archive can't write outside its folder or link out of it.
func TestUnpackStaysInside(t *testing.T) {
	d := t.TempDir()
	for name, b := range map[string][]byte{
		"escape": targz(t, map[string]string{"../evil": "x"}, nil),
		"link":   targz(t, map[string]string{"a/bin/x": "x"}, map[string]string{"a/bin/y": "../../../etc/passwd"}),
		"abs":    targz(t, nil, map[string]string{"a/z": "/etc/passwd"}),
	} {
		f := filepath.Join(d, name+".tar.gz")
		if err := os.WriteFile(f, b, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := unpack(f, filepath.Join(d, name), "x.tar.gz"); err == nil {
			t.Errorf("%s: unpacked", name)
		}
	}
}

// What a component needs is added first, and a component only a person can
// add here says how instead of failing; under go test MetaRush's own steps
// run only as the test's stand-ins.
func TestNeedsFirstAndPeopleSteps(t *testing.T) {
	e, bin := testEnv(t)
	ctx := context.Background()
	marker := filepath.Join(e.Home(), "thing")
	test := &Component{Name: "test-thing", Title: "A test thing", Thing: "a test thing", Feature: "Testing", Unlocks: "Tests.", Features: []string{"Tests"},
		DownloadMB: 1, Needs: []string{"stand-in-voice"},
		detect: func(context.Context, *Env) Found { _, err := os.Stat(marker); return Found{Ready: err == nil} },
		ways:   []Way{{Step: "faces"}}}
	registry = append(registry, test)
	defer func() { registry = registry[:len(registry)-1] }()
	// Linux without Homebrew: the stand-in voice is a person's to add.
	e.GOOS = "linux"
	res, err := e.Add(ctx, "test-thing", AddOptions{})
	if err != nil || res == nil || !res.NeedsYou || !strings.Contains(res.Says, "espeak-ng") {
		t.Fatalf("needs a person first: %+v %v", res, err)
	}
	program(t, bin, "espeak-ng", "eSpeak NG 1.52")
	e.Tools.Rescan(e.Cfg.Tools)
	if _, err := e.Add(ctx, "test-thing", AddOptions{}); err == nil || !strings.Contains(err.Error(), "never set components up for real") {
		t.Fatalf("a real step under go test: %v", err)
	}
	ran := 0
	e.Steps = map[string]StepFunc{"faces": func(_ context.Context, _ *Env, say func(string)) error {
		ran++
		say("Making the test thing…")
		return os.WriteFile(marker, nil, 0o644)
	}}
	res, err = e.Add(ctx, "test-thing", AddOptions{})
	if err != nil || !res.Done || ran != 1 {
		t.Fatalf("with the stand-in step: %+v %v (ran %d)", res, err, ran)
	}
}

// Required components come with setup, so they are never added or removed
// on their own; unknown names say which there are.
func TestRequiredAndUnknown(t *testing.T) {
	e, _ := testEnv(t)
	ctx := context.Background()
	if _, err := e.Add(ctx, "database", AddOptions{}); !apperr.Is(err, apperr.InvalidInput) {
		t.Fatalf("add database: %v", err)
	}
	if _, err := e.Remove(ctx, "storage", ""); !apperr.Is(err, apperr.InvalidInput) {
		t.Fatalf("remove storage: %v", err)
	}
	_, err := e.Add(ctx, "teleporter", AddOptions{})
	if !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("unknown: %v", err)
	}
	// On Linux a Mac-only component isn't offered.
	e.GOOS = "linux"
	for _, c := range e.Optional() {
		if c.Name == "screen-index" || c.Name == "faces" {
			t.Errorf("%s offered on Linux", c.Name)
		}
	}
	si, _ := Get("screen-index")
	if st := e.StateOf(ctx, si); st.Status != StatusNotHere || st.Command != "" {
		t.Fatalf("screen-index on Linux: %+v", st)
	}
}

// Downloads never reach the internet under go test.
func TestDownloadsRefusedUnderTest(t *testing.T) {
	if _, err := source(Pin{URL: "https://nodejs.org/dist/x.tar.gz"}); err == nil {
		t.Fatal("the internet under go test")
	}
	t.Setenv(EnvBase, "https://example.com")
	if _, err := source(Pin{URL: "https://nodejs.org/dist/x.tar.gz"}); err == nil {
		t.Fatal("a stand-in server elsewhere")
	}
}

// A crafted archive can't write outside its folder through a chain of
// links (a link to its own folder, then a link that climbs out through it):
// the unpack is refused and nothing lands outside.
func TestUnpackRefusesLinkChains(t *testing.T) {
	top := t.TempDir()
	dst := filepath.Join(top, "tools", ".x-1", "files")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	link := func(name, to string) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Linkname: to, Typeflag: tar.TypeSymlink, Mode: 0o777}); err != nil {
			t.Fatal(err)
		}
	}
	link("a/x", ".")
	link("a/x/x/x/up", "../../..")
	body := "escaped"
	if err := tw.WriteHeader(&tar.Header{Name: "a/x/x/x/up/victim", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = zw.Close()
	f := filepath.Join(top, "chain.tar.gz")
	if err := os.WriteFile(f, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unpack(f, dst, "chain.tar.gz"); err == nil {
		t.Error("an archive that climbs out through a chain of links was unpacked")
	}
	_ = filepath.Walk(top, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Name() == "victim" && !strings.HasPrefix(p, dst+string(filepath.Separator)) {
			t.Errorf("a file landed outside the archive's folder: %s", p)
		}
		return nil
	})
}

// Removing the renderer never pulls it from under a render: while a render
// uses it, removing is refused plainly and the folder stays; afterwards it
// goes.
func TestRemovingTheRendererWaitsForRenders(t *testing.T) {
	e, _ := testEnv(t)
	pkg, err := renderer.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	fake := fakenode.Write(t, pkg.Remotion())
	e.Cfg.Tools.Node, e.Cfg.Tools.NPM = fake.Node, fake.NPM
	e.Tools = tools.New(e.Cfg.Tools)
	ctx := context.Background()
	rend := renderer.New(e.Cfg, e.Tools)
	st, err := rend.Install(ctx, renderer.InstallOptions{})
	if err != nil || !st.Ready {
		t.Fatalf("install: %v %+v", err, st)
	}
	release, err := renderer.Use(st)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Remove(ctx, "renderer", "Sam")
	if !apperr.Is(err, apperr.Conflict) || !strings.Contains(apperr.From(err).Message, "render") {
		t.Errorf("removing while a render uses it: %v", err)
	}
	if _, serr := os.Stat(st.Dir); serr != nil {
		t.Fatal("the renderer was removed under a render")
	}
	release()
	if res, err := e.Remove(ctx, "renderer", "Sam"); err != nil || !res.Removed {
		t.Fatalf("after the render: %+v %v", res, err)
	}
	if _, serr := os.Stat(st.Dir); serr == nil {
		t.Fatal("the renderer is still there")
	}
}

// A download only follows redirects to the places its publisher uses
// (HTTPS, the pin's own host or its listed hosts; a test server only to
// itself): a redirect anywhere else is refused before anything is kept.
func TestDownloadsFollowOnlyKnownRedirects(t *testing.T) {
	e, _ := testEnv(t)
	good := targz(t, map[string]string{"node-v22.9.9-test/bin/node": "#!/bin/sh\necho v22.9.9\n"}, nil)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(good) }))
	defer other.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
	}))
	defer first.Close()
	t.Setenv(EnvBase, first.URL)
	e.Pins = []Pin{{ID: "node", Title: "Node.js 22.9.9", URL: "https://nodejs.example/node.tar.gz", SHA256: sum(good), Bytes: int64(len(good)),
		Take: "node-v22.9.9-test", To: "tools/node", Config: "node", ConfigPath: "bin/node", Licence: "MIT"}}
	_, err := e.Add(context.Background(), "node", AddOptions{})
	if err == nil || !strings.Contains(err.Error(), "sent the download elsewhere") {
		t.Fatalf("a redirect to another place: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(e.Home(), "tools", "node")); serr == nil {
		t.Fatal("a redirected download was kept")
	}
	// The policy itself: HTTPS and known hosts only.
	p := Pin{URL: "https://evermeet.cx/ffmpeg/ffprobe-9.0.2.zip", Hosts: []string{"e.deolaha.ca"}}
	check := redirects(p.URL, p.Hosts)
	for u, ok := range map[string]bool{"https://e.deolaha.ca:4242/pub/ffprobe/ffprobe-9.0.2.zip": true, "http://e.deolaha.ca/x": false, "https://evil.example/x": false} {
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		if got := check(req, []*http.Request{{}}) == nil; got != ok {
			t.Errorf("%s: allowed %v, want %v", u, got, ok)
		}
	}
}

// A program MetaRush downloaded is written down by its checksum; changed
// afterwards, it is no longer counted as here and MetaRush won't run it,
// saying plainly to add it again (like the local models' programs).
func TestChangedProgramsAreNotRun(t *testing.T) {
	e, _ := testEnv(t)
	good := targz(t, map[string]string{"node-v22.9.9-test/bin/node": "#!/bin/sh\necho v22.9.9\n"}, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(good) }))
	defer srv.Close()
	t.Setenv(EnvBase, srv.URL)
	e.Pins = []Pin{{ID: "node", Title: "Node.js 22.9.9", URL: "https://nodejs.example/node.tar.gz", SHA256: sum(good), Bytes: int64(len(good)),
		Take: "node-v22.9.9-test", To: "tools/node", Config: "node", ConfigPath: "bin/node", Licence: "MIT"}}
	ctx := context.Background()
	if _, err := e.Add(ctx, "node", AddOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Tools.Run(ctx, tools.Spec{Tool: tools.Node, Args: []string{"--version"}}); err != nil {
		t.Fatalf("the program as downloaded: %v", err)
	}
	prog := filepath.Join(e.Home(), "tools", "node", "bin", "node")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\necho changed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	node, _ := Get("node")
	if f := e.Find(ctx, node); f.Ready || !strings.Contains(f.Partly, "changed") {
		t.Errorf("a changed program counts as here: %+v", f)
	}
	_, err := e.Tools.Run(ctx, tools.Spec{Tool: tools.Node, Args: []string{"--version"}})
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Errorf("a changed program ran: %v", err)
	}
}

// An unversioned download (osxexperts' FFmpeg zips keep one address) is
// pinned by the program inside, which its publisher states: a zip that was
// packed again around the same program is taken; a different program is
// refused, saying plainly that the publisher replaced it.
func TestUnversionedDownloadsArePinnedByTheirProgram(t *testing.T) {
	e, _ := testEnv(t)
	zipOf := func(body, comment string) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create("ffprobe")
		_, _ = w.Write([]byte(body))
		_ = zw.SetComment(comment)
		_ = zw.Close()
		return buf.Bytes()
	}
	program := "#!/bin/sh\necho 'ffprobe version 9'\n"
	pinned := zipOf(program, "")
	repacked := zipOf(program, "packed again") // same program, other bytes around it
	other := zipOf("#!/bin/sh\necho 'something else'\n", "")
	serve := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(serve[r.URL.Path]) }))
	defer srv.Close()
	t.Setenv(EnvBase, srv.URL)
	pin := Pin{ID: "ffprobe", Title: "FFprobe 9.0", URL: "https://www.osxexperts.example/ffprobe9arm.zip", SHA256: sum(pinned), Bytes: int64(len(pinned)),
		ProgramSHA256: sum([]byte(program)), Take: "ffprobe", To: "tools/ffmpeg/ffprobe", Exec: true, Config: "ffprobe", Licence: "GPL-3.0"}
	ctx := context.Background()
	serve["/ffprobe9arm.zip"] = repacked
	if err := e.fetch(ctx, pin, func(string) {}); err != nil {
		t.Fatalf("the same program packed again: %v", err)
	}
	serve["/ffprobe9arm.zip"] = other
	err := e.fetch(ctx, pin, func(string) {})
	if err == nil || !strings.Contains(apperr.From(err).Message, "replaced it") {
		t.Fatalf("another program at the same address: %v", err)
	}
}

// Links that each look inside by their words can still point outside
// together (x/y/d → ../../z, then t → x/y/d/../../../outside): fetch refuses
// them, and never moves or changes a file outside the unpack folder.
func TestFetchRefusesLinksThatPointOutTogether(t *testing.T) {
	e, _ := testEnv(t)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	dir := func(n string) { _ = tw.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeDir, Mode: 0o755}) }
	link := func(n, to string) {
		_ = tw.WriteHeader(&tar.Header{Name: n, Linkname: to, Typeflag: tar.TypeSymlink, Mode: 0o777})
	}
	dir("z/")
	dir("x/y/")
	link("x/y/d", "../../z")
	link("t", "x/y/d/../../../outside")
	_ = tw.Close()
	_ = zw.Close()
	arch := buf.Bytes()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(arch) }))
	defer srv.Close()
	t.Setenv(EnvBase, srv.URL)
	thing := filepath.Join(e.Home(), "tools", "thing")
	victim := filepath.Join(thing, "outside", "victim")
	_ = os.MkdirAll(filepath.Dir(victim), 0o755)
	if err := os.WriteFile(victim, []byte("a file outside the unpack folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := Pin{ID: "x", Title: "X", URL: "https://example.test/x.tar.gz", SHA256: sum(arch), Bytes: int64(len(arch)),
		Take: "t/victim", To: "tools/thing/prog", Exec: true}
	if err := e.fetch(context.Background(), p, func(string) {}); err == nil {
		t.Error("an archive whose links point out together was taken")
	}
	if st, err := os.Stat(victim); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("the file outside was moved or changed: %v %v", err, st)
	}
}

// The check of downloaded programs can't be switched off by deleting its
// record, covers every file of Node.js (npm runs through it), uses the
// checksums built into the pins, and knows yt-dlp as MetaRush installed it.
func TestProgramCheckCantBeSidestepped(t *testing.T) {
	e, _ := testEnv(t)
	good := targz(t, map[string]string{
		"node-v22.9.9-test/bin/node":                            "#!/bin/sh\necho v22.9.9\n",
		"node-v22.9.9-test/lib/node_modules/npm/bin/npm-cli.js": "// npm\n",
	}, nil)
	ytdlp := []byte("#!/bin/sh\necho 2026.10.21\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "yt-dlp_test") {
			_, _ = w.Write(ytdlp)
			return
		}
		_, _ = w.Write(good)
	}))
	defer srv.Close()
	t.Setenv(EnvBase, srv.URL)
	e.Pins = []Pin{{ID: "node", Title: "Node.js 22.9.9", URL: "https://nodejs.example/node.tar.gz", SHA256: sum(good), Bytes: int64(len(good)),
		Take: "node-v22.9.9-test", To: "tools/node", Config: "node", ConfigPath: "bin/node", Licence: "MIT"},
		{ID: "yt-dlp", Title: "yt-dlp", URL: "https://github.com/yt-dlp/yt-dlp_test", SHA256: sum(ytdlp), Bytes: int64(len(ytdlp)),
			To: "tools/yt-dlp/yt-dlp", Exec: true, Config: "ytDlp", Licence: "Unlicense"}}
	ctx := context.Background()
	if _, err := e.Add(ctx, "node", AddOptions{}); err != nil {
		t.Fatal(err)
	}
	run := func() error {
		_, err := e.Tools.Run(ctx, tools.Spec{Tool: tools.Node, Args: []string{"--version"}})
		return err
	}
	// npm, which runs through the trusted node, is checked too.
	npm := filepath.Join(e.Home(), "tools", "node", "lib", "node_modules", "npm", "bin", "npm-cli.js")
	if err := os.WriteFile(npm, []byte("// changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(); err == nil {
		t.Error("node ran with a changed npm beside it")
	}
	_ = os.WriteFile(npm, []byte("// npm\n"), 0o644)
	if err := run(); err != nil {
		t.Fatalf("node as downloaded: %v", err)
	}
	// Deleting the record doesn't switch the check off.
	prog := filepath.Join(e.Home(), "tools", "node", "bin", "node")
	_ = os.WriteFile(prog, []byte("#!/bin/sh\necho tampered\n"), 0o755)
	_ = os.Remove(filepath.Join(e.Home(), "tools", pinnedFile))
	if err := run(); err == nil {
		t.Error("with its record deleted, a changed program ran")
	}
	// yt-dlp: only the program MetaRush installed (or updated) runs.
	if _, err := e.Add(ctx, "link-import", AddOptions{}); err != nil {
		t.Fatal(err)
	}
	yt := filepath.Join(e.Home(), "tools", "yt-dlp", "yt-dlp")
	if _, err := e.Tools.Run(ctx, tools.Spec{Tool: tools.YtDlp, Args: []string{"--version"}}); err != nil {
		t.Fatalf("yt-dlp as installed: %v", err)
	}
	_ = os.WriteFile(yt, []byte("#!/bin/sh\necho something else\n"), 0o755)
	if _, err := e.Tools.Run(ctx, tools.Spec{Tool: tools.YtDlp, Args: []string{"--version"}}); err == nil {
		t.Error("another program at yt-dlp's place ran")
	}
	// MetaRush's own update of it is written down again.
	tools.Recorded(yt)
	if _, err := e.Tools.Run(ctx, tools.Spec{Tool: tools.YtDlp, Args: []string{"--version"}}); err != nil {
		t.Errorf("yt-dlp after MetaRush updated it: %v", err)
	}
}

// A program Homebrew puts on the PATH outside MetaRush is seen by the
// running MetaRush (the watcher looks at the program folders too).
func TestWatchSeesProgramsHomebrewAdds(t *testing.T) {
	e, bin := testEnv(t)
	a := &app.App{Cfg: e.Cfg, Tools: e.Tools}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fired := make(chan struct{}, 4)
	go Watch(ctx, a, 20*time.Millisecond, func() { fired <- struct{}{} })
	time.Sleep(60 * time.Millisecond)
	program(t, bin, "ffmpeg", "ffmpeg version 9")
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("a program Homebrew put on the PATH isn't seen")
	}
	if !e.Tools.Has(tools.FFmpeg) {
		t.Fatal("the running MetaRush doesn't have it")
	}
}

// An archive is unpacked only as far as what it keeps, within a size its
// download allows: an unversioned zip can't fill the disk before its
// program is checked.
func TestUnpackKeepsOnlyWhatItTakes(t *testing.T) {
	d := t.TempDir()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("ffprobe")
	_, _ = w.Write([]byte("program"))
	big, _ := zw.CreateHeader(&zip.FileHeader{Name: "padding.bin", Method: zip.Deflate})
	_, _ = big.Write(bytes.Repeat([]byte{0}, 96<<20)) // 96 MB of zeros, a few KB packed
	_ = zw.Close()
	f := filepath.Join(d, "x.zip")
	if err := os.WriteFile(f, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(d, "files")
	if err := unpackOnly(f, out, "x.zip", "ffprobe", int64(buf.Len())); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "padding.bin")); err == nil {
		t.Error("a file it doesn't keep was unpacked")
	}
	// The same archive's padding, if it were what's kept, is past what its download allows.
	if err := unpackOnly(f, filepath.Join(d, "files2"), "x.zip", "padding.bin", int64(buf.Len())); err == nil {
		t.Error("a file much larger than its download allows was unpacked")
	}
}

// A single program is also checked against the checksum built into its pin,
// so writing the record again over a changed program doesn't make it run.
func TestRewrittenRecordDoesntPassAChangedProgram(t *testing.T) {
	e, _ := testEnv(t)
	program := []byte("#!/bin/sh\necho 'ffprobe version 9'\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(program) }))
	defer srv.Close()
	t.Setenv(EnvBase, srv.URL)
	pin := Pin{ID: "ffprobe", Title: "FFprobe 9.0", URL: "https://ffmpeg.example/ffprobe", SHA256: sum(program), Bytes: int64(len(program)),
		To: "tools/ffmpeg/ffprobe", Exec: true, Config: "ffprobe", Licence: "GPL-3.0"}
	if err := e.fetch(context.Background(), pin, func(string) {}); err != nil {
		t.Fatal(err)
	}
	prog := filepath.Join(e.Home(), "tools", "ffmpeg", "ffprobe")
	if err := trusted(prog); err != nil {
		t.Fatalf("as downloaded: %v", err)
	}
	_ = os.WriteFile(prog, []byte("#!/bin/sh\necho changed\n"), 0o755)
	_ = e.record(pin) // the record written again over the changed program
	if err := trusted(prog); err == nil {
		t.Error("a changed program passed once its record was written again")
	}
}
