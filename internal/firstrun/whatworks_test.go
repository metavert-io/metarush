package firstrun

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/components"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/localdb"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/wsl"

	_ "github.com/metavert-io/metarush/internal/videogen" // the generated-video services' keys (Google Gemini, Vertex AI)
)

// standIns makes a folder of stand-in programs (node says it is 22, ffmpeg
// and ffprobe say their versions, espeak-ng is there) and returns it.
func standIns(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	says := map[string]string{"node": "v22.11.0", "ffmpeg": "ffmpeg version 7.1", "ffprobe": "ffprobe version 7.1", "espeak-ng": "eSpeak NG 1.51"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\necho '"+says[n]+"'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// onlyStandIns makes MetaRush see only these stand-in programs.
func onlyStandIns(t *testing.T, names ...string) *tools.Set {
	t.Helper()
	t.Setenv(tools.EnvToolDirs, standIns(t, names...))
	return tools.New(config.Tools{})
}

// freshConfig is a new installation's settings in a folder of the test's.
func freshConfig(t *testing.T) *config.Config {
	t.Helper()
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Secrets = "env"
	return cfg
}

func item(t *testing.T, w WhatWorks, key string) WorksItem {
	t.Helper()
	for _, it := range append(append([]WorksItem{}, w.NoKeys...), w.WithKeys...) {
		if it.Key == key {
			return it
		}
	}
	t.Fatalf("no %s in %+v", key, w)
	return WorksItem{}
}

// writeELF writes a small 64-bit ELF file whose dynamic section needs libs —
// all the renderer's library check reads of a program (as in
// internal/renderer's tests).
func writeELF(t *testing.T, path string, libs ...string) {
	t.Helper()
	dynstr := []byte{0}
	var offs []uint64
	for _, l := range libs {
		offs = append(offs, uint64(len(dynstr)))
		dynstr = append(append(dynstr, l...), 0)
	}
	var dynamic bytes.Buffer
	for _, o := range offs {
		_ = binary.Write(&dynamic, binary.LittleEndian, [2]uint64{1, o}) // DT_NEEDED
	}
	_ = binary.Write(&dynamic, binary.LittleEndian, [2]uint64{0, 0}) // DT_NULL
	shstr := []byte("\x00.dynstr\x00.dynamic\x00.shstrtab\x00")
	const ehsize, shentsize = 64, 64
	dynstrOff := uint64(ehsize)
	dynOff := dynstrOff + uint64(len(dynstr))
	shstrOff := dynOff + uint64(dynamic.Len())
	shOff := shstrOff + uint64(len(shstr))
	var b bytes.Buffer
	b.Write([]byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	w(uint16(3))  // ET_DYN
	w(uint16(62)) // x86-64
	w(uint32(1))
	w(uint64(0)) // entry
	w(uint64(0)) // no program headers
	w(shOff)
	w(uint32(0))
	w(uint16(ehsize))
	w(uint16(56))
	w(uint16(0))
	w(uint16(shentsize))
	w(uint16(4)) // sections
	w(uint16(3)) // .shstrtab
	b.Write(dynstr)
	b.Write(dynamic.Bytes())
	b.Write(shstr)
	sh := func(name, typ uint32, off, size uint64, link uint32, entsize uint64) {
		w(name)
		w(typ)
		w(uint64(0))
		w(uint64(0))
		w(off)
		w(size)
		w(link)
		w(uint32(0))
		w(uint64(1))
		w(entsize)
	}
	sh(0, 0, 0, 0, 0, 0)
	sh(1, 3, dynstrOff, uint64(len(dynstr)), 0, 0) // .dynstr
	sh(9, 6, dynOff, uint64(dynamic.Len()), 1, 16) // .dynamic
	sh(18, 3, shstrOff, uint64(len(shstr)), 0, 0)  // .shstrtab
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o755); err != nil {
		t.Fatal(err)
	}
}

// On a computer with none of the free tools, everything that needs one says
// which component adds it, in plain words; Works answers the same by key.
func TestWhatWorksOnAComputerWithNothing(t *testing.T) {
	t.Cleanup(wsl.Pretend(wsl.Info{}))
	ts := onlyStandIns(t)
	cfg := freshConfig(t)
	ForgetClaude()
	defer ForgetClaude()
	w := Build(context.Background(), cfg, ts)
	if w.Summary != "Most of MetaRush works free on this computer with no keys; what isn't ready yet says what it needs." || w.Windows != nil {
		t.Fatalf("%+v", w)
	}
	for _, c := range []struct{ key, says, component, next string }{
		{"transcripts", "Made on this computer, free, once transcription is added; nothing is uploaded.", "transcription", "Add the transcriber and its speech models in Settings → Components ("},
		{"editing", "Editing needs FFmpeg, the free video tool, to bring your footage in.", "ffmpeg", "Add FFmpeg in Settings → Components ("},
		{"renders", "Finished videos need Node.js and the Remotion renderer on this computer.", renderer.Component, "Add "},
		{"checks", "Before a video goes out, MetaRush watches and listens to it: words cut off, captions over faces, sound too quiet.", "ffmpeg", "Add FFmpeg in Settings → Components ("},
		{"scratch-voice", "A free, robotic voice to time narration before you record it or choose an AI voice.", "stand-in-voice", "Add the stand-in voice in Settings → Components (optional)."},
	} {
		it := item(t, w, c.key)
		if it.Ready || it.Says != c.says || it.Component != c.component || !strings.HasPrefix(it.Next, c.next) || it.Command != "metarush components add "+c.component {
			t.Errorf("%s: %+v", c.key, it)
		}
		for _, f := range plainwords.Check(it.Says + " " + it.Next) {
			t.Errorf("%s not plain: %s", c.key, f)
		}
	}
	if it := item(t, w, "editing"); it.Needs != "FFmpeg" {
		t.Errorf("editing needs %q", it.Needs)
	}
	by := Works(cfg, ts)
	if len(by) != len(w.NoKeys)+len(w.WithKeys) {
		t.Fatalf("works: %d, what works: %d + %d", len(by), len(w.NoKeys), len(w.WithKeys))
	}
	for _, it := range append(append([]WorksItem{}, w.NoKeys...), w.WithKeys...) {
		if got := by[it.Key]; got.Ready != it.Ready || got.Says != it.Says {
			t.Errorf("works %s: %+v, what works: %+v", it.Key, got, it)
		}
	}
}

// With Node.js and no FFmpeg, finished videos say FFmpeg is what they need;
// with both (and a stand-in voice) the free tools work, the renderer sets
// itself up the first time, and a damaged renderer says it needs setting up
// again.
func TestWhatWorksWithTheFreeTools(t *testing.T) {
	t.Cleanup(wsl.Pretend(wsl.Info{}))
	cfg := freshConfig(t)
	ForgetClaude()
	defer ForgetClaude()

	w := Build(context.Background(), cfg, onlyStandIns(t, "node"))
	if rd := item(t, w, "renders"); rd.Ready || rd.Says != "Finished videos need FFmpeg on this computer." || rd.Component != "ffmpeg" {
		t.Fatalf("renders without FFmpeg: %+v", rd)
	}

	ts := onlyStandIns(t, "node", "ffmpeg", "ffprobe", "espeak-ng")
	w = Build(context.Background(), cfg, ts)
	for _, key := range []string{"editing", "checks", "scratch-voice"} {
		if it := item(t, w, key); !it.Ready || it.Next != "" || it.Component != "" {
			t.Errorf("%s: %+v", key, it)
		}
	}
	rd := item(t, w, "renders")
	if !rd.Ready || rd.Says != "Made on this computer at full quality. The first time, MetaRush takes a few minutes to get ready for it by itself." ||
		rd.Command != "metarush components add renderer" || rd.Component != renderer.Component {
		t.Fatalf("renders, set up the first time: %+v", rd)
	}

	// A renderer folder without its setup record is damaged.
	st := renderer.New(cfg, ts).Check(false)
	if st.Dir == "" {
		t.Fatalf("no renderer folder: %+v", st)
	}
	if err := os.MkdirAll(st.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rd = item(t, Build(context.Background(), cfg, ts), "renders")
	if rd.Ready || rd.Says != "Finished videos need the Remotion renderer on this computer." || rd.Component != renderer.Component || rd.Next == "" {
		t.Fatalf("a damaged renderer: %+v", rd)
	}
}

// Claude Code that doesn't start, or answers in ways MetaRush doesn't know,
// is said plainly: never a guess about how it pays.
func TestClaudeCodeAnswersOddly(t *testing.T) {
	t.Cleanup(wsl.Pretend(wsl.Info{}))
	claude := func(script string) *tools.Set {
		t.Helper()
		p := filepath.Join(t.TempDir(), "claude")
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
			t.Fatal(err)
		}
		return tools.New(config.Tools{Claude: p})
	}
	ForgetClaude()
	defer ForgetClaude()
	got := CheckClaude(context.Background(), claude("echo 'cannot start' >&2\nexit 1\n"))
	if !got.Installed || got.Ready || got.Says != "Claude Code is on this computer but doesn't start." || got.Next != "Open Claude Code once in a terminal to see what's wrong." || got.Command != "claude" {
		t.Fatalf("doesn't start: %+v", got)
	}
	ForgetClaude()
	got = CheckClaude(context.Background(), claude(`case "$1" in
--version) printf '2.5.0 (Claude Code)\nA newer version is available.\n';;
auth) echo '{"loggedIn": true, "authMethod": "third_party"}';;
esac
`))
	if !got.Installed || !got.Ready || got.Version != "2.5.0" || got.Billing != "unknown" || got.Says != "Claude Code is installed and signed in." {
		t.Fatalf("an unknown sign-in: %+v", got)
	}
	for _, m := range []string{"third_party", "", "something else"} {
		if b := billing(m); b != "unknown" {
			t.Errorf("billing(%q) = %q", m, b)
		}
	}
}

// What a key adds: one line per kind of paid service, named by the first
// service of its kind, with the key to add (and "a" or "an" before its name)
// while this computer has none.
func TestWhatAKeyAdds(t *testing.T) {
	cfg := freshConfig(t)
	video := 0
	first := ""
	for _, p := range providers.All() {
		t.Setenv(providers.EnvVar(p.Name()), "")
		os.Unsetenv(providers.EnvVar(p.Name())) // restored after the test (t.Setenv)
		if p.Category() == providers.CategoryVideo {
			video++
			if first == "" {
				first = p.Title()
			}
		}
	}
	if video < 2 {
		t.Fatalf("%d generated-video services: the test needs two", video)
	}
	ws := withKeys(cfg)
	shots, voices := 0, 0
	for _, it := range ws {
		switch it.Key {
		case "generated-shots":
			shots++
			if it.Ready || it.Name != "Generated shots" || it.Needs != article(first)+" "+first+" key" || it.Next != "Add "+article(first)+" "+first+" key in Settings → Integrations." ||
				!strings.Contains(it.Says, "with "+first+".") || it.Place != "/settings/integrations" {
				t.Errorf("generated shots: %+v", it)
			}
		case "ai-voices":
			voices++
			if it.Ready || it.Needs != "an ElevenLabs key" || it.Next != "Add an ElevenLabs key in Settings → Integrations." {
				t.Errorf("AI voices: %+v", it)
			}
		default:
			t.Errorf("unexpected %+v", it)
		}
		for _, f := range plainwords.Check(it.Says + " " + it.Next) {
			t.Errorf("%s not plain: %s", it.Key, f)
		}
	}
	if shots != 1 || voices != 1 {
		t.Fatalf("%d lines for generated shots, %d for AI voices", shots, voices)
	}
	if article("Google Gemini") != "a" || article("ElevenLabs") != "an" || article("") != "a" {
		t.Fatal("article")
	}
}

// Where the projects are kept, in plain words: a database elsewhere, MongoDB
// Atlas, or the database on this computer — not running, or kept from
// starting by this computer's Linux kernel (with what to do).
func TestDatabaseState(t *testing.T) {
	ts := onlyStandIns(t)
	cfg := freshConfig(t)
	cfg.Database.URI = "mongodb://db.example.internal:27017/metarush"
	if d := DatabaseState(context.Background(), cfg, ts); d.Local || !d.Running || d.Says != "Your projects are kept in a database elsewhere, which your other computers can share." {
		t.Fatalf("elsewhere: %+v", d)
	}
	cfg.Database.URI = "mongodb://cluster0.abcde.mongodb.net:27017/metarush"
	if d := DatabaseState(context.Background(), cfg, ts); d.Says != "Your projects are kept in a database on MongoDB Atlas, which your other computers can share." {
		t.Fatalf("Atlas: %+v", d)
	}
	cfg.Database.Local = &config.LocalDatabase{Port: 27199}
	prev := localdb.ReadKernel
	t.Cleanup(func() { localdb.ReadKernel = prev })
	localdb.ReadKernel = func() localdb.Kernel { return localdb.Kernel{} }
	if d := DatabaseState(context.Background(), cfg, ts); !d.Local || d.Running ||
		d.Says != "Your projects are kept in a database on this computer. It isn't running right now; MetaRush starts it when it needs it." {
		t.Fatalf("here, not running: %+v", d)
	}
	// MongoDB 8 set up here, and a Linux kernel it refuses.
	if err := os.MkdirAll(filepath.Join(cfg.Home(), "database"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Home(), "database", "state.json"), []byte(`{"version": "8.0.34", "port": 27199, "initiated": true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	localdb.ReadKernel = func() localdb.Kernel { return localdb.Kernel{Release: "6.19.4-061904-generic"} }
	d := DatabaseState(context.Background(), cfg, ts)
	if !d.Local || d.Running || !strings.HasPrefix(d.Says, "Your projects are kept in a database on this computer, which can't start right now. ") ||
		len(d.Says) <= len("Your projects are kept in a database on this computer, which can't start right now. ") {
		t.Fatalf("kept from starting: %+v", d)
	}
	for _, f := range plainwords.Check(d.Says) {
		t.Errorf("not plain: %s", f)
	}
}

// The components Getting started offers: only the optional ones not here,
// the suggested ones first.
func TestMissingComponents(t *testing.T) {
	ts := onlyStandIns(t, "espeak-ng")
	cfg := freshConfig(t)
	got := MissingComponents(context.Background(), &components.Env{Cfg: cfg, Tools: ts})
	names := map[string]bool{}
	suggestedDone := false
	for _, c := range got {
		names[c.Name] = true
		if c.Required || c.Ready || c.Status == components.StatusNotHere {
			t.Errorf("offered: %+v", c)
		}
		if !c.Suggested {
			suggestedDone = true
		} else if suggestedDone {
			t.Errorf("%s is suggested but comes after one that isn't", c.Name)
		}
	}
	if !names["ffmpeg"] || !names["node"] || names["stand-in-voice"] || !suggestedDone {
		t.Fatalf("%v", names)
	}
}

// setup.status says what works here and where the projects are kept, and is
// the first run while the workspace has nothing; inside WSL the notes' folder
// paths are an owner's to see.
func TestSetupStatus(t *testing.T) {
	a := testapp.New(t)
	c, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	obs := filepath.Join(c, "Users", "Sam", "Videos", "OBS")
	if err := os.MkdirAll(obs, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wsl.Pretend(wsl.Info{Version: 2, Distro: "Ubuntu-24.04", DistroKnown: true, Name: "Ubuntu", Interop: true, Systemd: true,
		DriveRoot: "/mnt/", Drives: map[string]string{c: "c"}}))
	a.Cfg.Worker.Watch = config.Watch{Enabled: true, Dir: obs}
	ctx := context.Background()
	for _, who := range []struct {
		ctx   context.Context
		owner bool
	}{{ops.OverHTTP(ctx, func() bool { return false }), false}, {ops.OverHTTP(ctx, func() bool { return true }), true}} {
		out, err := ops.CallTyped[SetupStatusOut](who.ctx, a, "setup.status", SetupStatusIn{Refresh: true}, ops.Caller{})
		if err != nil {
			t.Fatal(err)
		}
		if !out.FirstRun || out.Machine != a.Cfg.Installation.Name || out.Components == nil || out.Database.Local || len(out.NoKeys) < 5 {
			t.Fatalf("%+v", out)
		}
		n, ok := out.Windows.Note("files")
		if !ok || n.Says != "Your watched folder is on the Windows side (C:): "+wsl.FolderSlowly || strings.Contains(n.Command, obs) != who.owner {
			t.Errorf("owner %v: %+v", who.owner, n)
		}
		if out.Claude.Installed || !strings.Contains(out.Claude.Says, "isn't installed in Ubuntu.") {
			t.Errorf("Claude Code: %+v", out.Claude)
		}
	}
}
