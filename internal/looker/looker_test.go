package looker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/onnx"
	"github.com/metavert-io/metarush/internal/onnx/onnxtest"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visionmodels"
)

// How a computer looks is chosen (D408): Apple Vision on a Mac, the open
// models anywhere else; a Mac can be told to use the open models; Apple
// Vision can't be chosen where it doesn't exist.
func TestChosen(t *testing.T) {
	cfg := &config.Config{}
	for _, c := range []struct{ setting, system, want string }{
		{"", "darwin", AppleVision}, {"auto", "darwin", AppleVision}, {"open-models", "darwin", OpenModels}, {"apple-vision", "darwin", AppleVision},
		{"", "linux", OpenModels}, {"apple-vision", "linux", OpenModels}, {"open-models", "linux", OpenModels}, {"", "windows", OpenModels},
	} {
		cfg.ScreenIndex.Backend = c.setting
		if got := ChosenOn(cfg, c.system); got != c.want {
			t.Errorf("%q on %s: %s, want %s", c.setting, c.system, got, c.want)
		}
	}
	if got := ChosenOn(nil, "linux"); got != OpenModels {
		t.Errorf("no settings on Linux: %s", got)
	}
	// The live setting wins over the file it was read from; the environment
	// (tests, measurements) over both.
	cfg = &config.Config{}
	cfg.SetScreenIndexChoice(OpenModels)
	if got := ChosenOn(cfg, "darwin"); got != OpenModels {
		t.Errorf("the live choice: %s", got)
	}
	t.Setenv("METARUSH_SCREEN_INDEX", AppleVision)
	if got := ChosenOn(cfg, "darwin"); got != AppleVision {
		t.Errorf("the environment: %s", got)
	}
}

// stagedModels puts stand-in model files where the open models are looked
// for, pinned by their own checksums for the test, and a stand-in
// transcriber install whose folder holds onnxruntime (a file named like it;
// lib, when set, is linked in as the real library).
func stagedModels(t *testing.T, lib string) (*tools.Set, func()) {
	t.Helper()
	models := t.TempDir()
	dir := visionmodels.Dir(models)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	stand := map[string]string{
		"screen-faces": onnxtest.FaceFinder(t, src), "screen-text-find": onnxtest.TextFinder(t, src), "screen-text-read": onnxtest.TextReader(t, src),
		"screen-text-chars": onnxtest.TextChars(t, src), "screen-attention": onnxtest.Saliency(t, src), "cut-outs": onnxtest.Subject(t, src),
	}
	old := visionmodels.Files
	var files []visionmodels.File
	for _, f := range old {
		b, err := os.ReadFile(stand[f.ID])
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f.Name), b, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		f.SHA256, f.Bytes = hex.EncodeToString(sum[:]), int64(len(b))
		files = append(files, f)
	}
	visionmodels.Files = files
	sherpa := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sherpa, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sherpa, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sherpa, "bin", tools.Sherpa), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	name := "libonnxruntime.so"
	if runtime.GOOS == "darwin" {
		name = "libonnxruntime.dylib"
	}
	if lib != "" {
		if err := os.Symlink(lib, filepath.Join(sherpa, "lib", name)); err != nil {
			t.Fatal(err)
		}
	} else if err := os.WriteFile(filepath.Join(sherpa, "lib", name), []byte("not a library"), 0o644); err != nil {
		t.Fatal(err)
	}
	ffmpeg := filepath.Join(sherpa, "bin", "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ts := tools.New(config.Tools{SherpaDir: sherpa, ModelsDir: models, FFmpeg: ffmpeg})
	return ts, func() { visionmodels.Files = old }
}

// The open models' state, in plain words: what they need (the transcriber's
// engine, then the models), ready once both are here; and only ready, not
// set up by themselves.
func TestOpenModelsStatus(t *testing.T) {
	if !onnx.Available {
		t.Skip("onnxruntime isn't bound on this system")
	}
	cfg := &config.Config{}
	cfg.SetScreenIndexChoice(OpenModels)
	t.Setenv(tools.EnvToolDirs, t.TempDir())
	bare := tools.New(config.Tools{ModelsDir: t.TempDir()})
	st := Check(context.Background(), cfg, bare)
	if st.Ready || st.Backend != OpenModels || !strings.Contains(st.Text, "transcriber's engine") || !strings.Contains(st.Fix, "Transcription on this computer") {
		t.Fatalf("without the engine: %+v", st)
	}
	ts, done := stagedModels(t, "")
	defer done()
	if st := Check(context.Background(), cfg, ts); !st.Ready || !CanRun(cfg, ts) || !strings.Contains(st.Text, "open models") ||
		strings.Join(st.Sees, ",") != "faces,landmarks,text,attention" {
		t.Fatalf("with both: %+v", st)
	}
	// A model changed after it was added isn't used.
	f, _ := visionmodels.Store{}.ByID("screen-attention")
	p := filepath.Join(ts.ModelsDir(), visionmodels.Folder, f.Name)
	if err := os.WriteFile(p, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := Check(context.Background(), cfg, ts); st.Ready || !strings.Contains(st.Text, "changed") {
		t.Fatalf("a changed model: %+v", st)
	}
	if CanRun(cfg, ts) {
		t.Fatal("a computer whose models changed can look")
	}
}

// Open loads the open models (here stand-ins, through the real runtime) and
// looks with them; where they aren't ready it says what adds them, and a
// person who left the component out is told in the component's words.
func TestOpenWithTheOpenModels(t *testing.T) {
	cfg := config.Default(t.TempDir())
	cfg.SetScreenIndexChoice(OpenModels)
	t.Setenv(tools.EnvToolDirs, t.TempDir())
	_, _, _, err := Open(context.Background(), cfg, tools.New(config.Tools{ModelsDir: t.TempDir()}))
	if !apperr.Is(err, apperr.ToolMissing) || apperr.From(err).Command() != "metarush components add screen-index" {
		t.Fatalf("not here: %v", err)
	}
	if err := choice.Set(cfg.Home(), map[string]bool{Component: false}, "Sam"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := Open(context.Background(), cfg, tools.New(config.Tools{ModelsDir: t.TempDir()})); !apperr.Is(err, apperr.ToolMissing) || apperr.From(err).Message != LeftOutWords() {
		t.Fatalf("left out: %v", err)
	}
	lib := onnxtest.Library(t)
	ts, done := stagedModels(t, lib)
	defer done()
	lk, st, built, err := Open(context.Background(), cfg, ts)
	if err != nil || built || !st.Ready || lk.Backend() != OpenModels || lk.Model() != visionmodels.ModelName || len(lk.Sees()) != 4 {
		t.Fatalf("%v %+v %v", lk, st, err)
	}
	if Here(context.Background(), cfg, ts) == nil {
		t.Fatal("a ready computer has a looker here")
	}
	dir := t.TempDir()
	var n int
	if err := lk.Analyze(context.Background(), dir, vision.Options{}, func(vision.Frame) { n++ }); err != nil || n != 0 {
		t.Fatalf("an empty folder: %d %v", n, err)
	}
}

// Cutting a subject out follows the choice (D414): the cut-out model where
// the open models are this computer's way, said plainly when it is missing.
func TestCutOutFollowsTheChoice(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetScreenIndexChoice(OpenModels)
	t.Setenv(tools.EnvToolDirs, t.TempDir())
	err := CutOutReady(cfg, tools.New(config.Tools{ModelsDir: t.TempDir()}))
	if !apperr.Is(err, apperr.NotConfigured) || apperr.From(err).Command() != "metarush components add cut-outs" {
		t.Fatalf("missing: %v", err)
	}
	lib := onnxtest.Library(t)
	ts, done := stagedModels(t, lib)
	defer done()
	if st := CheckCutOut(cfg, ts); !st.Ready || st.Backend != OpenModels || st.Engine != CutOutOpen {
		t.Fatalf("%+v", st)
	}
	in, out := filepath.Join(t.TempDir(), "in.png"), filepath.Join(t.TempDir(), "out.png")
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(in, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if found, err := CutOut(context.Background(), cfg, ts, in, out); err != nil || found {
		t.Fatalf("a dark picture has no subject: %v %v", found, err)
	}
	// On a Mac that keeps Apple Vision, the helper does it.
	if runtime.GOOS == "darwin" {
		mac := &config.Config{}
		if st := CheckCutOut(mac, tools.New(config.Tools{})); st.Backend != AppleVision || st.Engine != CutOutApple {
			t.Fatalf("a Mac: %+v", st)
		}
	}
}

// A Mac whose person left Seeing what's on screen out doesn't offer to look
// when its helper isn't here (D446): the helper is never built for it, so the
// clip would only fail there. A helper that is here still runs.
func TestCanRunHonoursALeftOutHelper(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Apple Vision runs only on a Mac")
	}
	dir := t.TempDir()
	for _, p := range []string{"ffmpeg", "ffprobe", "swiftc"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(tools.EnvToolDirs, dir)
	t.Setenv("METARUSH_VISION_CACHE", t.TempDir())
	cfg := config.Default(t.TempDir())
	ts := tools.New(config.Tools{})
	if Chosen(cfg) != AppleVision || !CanRun(cfg, ts) {
		t.Fatal("a Mac with Swift offers to look (it builds the helper on first use)")
	}
	if err := choice.Set(cfg.Home(), map[string]bool{Component: false}, "Sam"); err != nil {
		t.Fatal(err)
	}
	if CanRun(cfg, ts) {
		t.Fatal("a Mac whose person left the helper out offers to look")
	}
	if err := os.WriteFile(filepath.Join(dir, tools.Vision), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !CanRun(cfg, tools.New(config.Tools{})) {
		t.Fatal("a helper that is here runs, left out or not")
	}
}
