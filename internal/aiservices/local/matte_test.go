package local_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiservices/aiconformance"
	"github.com/metavert-io/metarush/internal/aiservices/local"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/matting"
	"github.com/metavert-io/metarush/internal/matting/mattingtest"
	"github.com/metavert-io/metarush/internal/testutil/fakelocal"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/videogen"
	"github.com/metavert-io/metarush/internal/visionmodels"
)

// footageRequest is a request for a clip's cut-out of real footage: the
// clip's own file and a stretch of it, as the cut-out job sends it.
func footageRequest(t *testing.T, model, dir string, from, to float64) *aiservices.Request {
	t.Helper()
	src := mattingtest.Footage(t, dir, 2, 320, 180, 25)
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return &aiservices.Request{Kind: aiservices.KindVideoBackground, Task: aiservices.TaskMatte, Model: model,
		Inputs: []aiservices.Media{{Role: aiservices.RoleClip, MIME: "video/mp4", Path: src, Name: "footage.mp4", SHA256: hex.EncodeToString(sum[:]),
			Size: int64(len(b)), Seconds: to - from, From: from, To: to}}}
}

func matteEnv(t *testing.T) aiservices.Env {
	t.Helper()
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.FFmpeg) || !ts.Has(tools.FFprobe) {
		t.Skip("ffmpeg isn't installed here")
	}
	return aiservices.Env{Tools: ts, Cfg: &config.Config{}, Home: t.TempDir(), Scratch: t.TempDir()}
}

// Both models on this computer pass the conformance suite (D505): a matte of
// the stretch asked for comes back as a video file, nothing is kept after
// Forget, and the request's hash never depends on where the file is.
func TestVideoBackgroundConforms(t *testing.T) {
	m := fakelocal.Matter(t)
	e := matteEnv(t)
	a, err := aiservices.Get(local.Name, aiservices.KindVideoBackground)
	if err != nil {
		t.Fatal(err)
	}
	aiconformance.Run(t, aiconformance.Harness{Adapter: a, Env: &e, Request: func(t *testing.T, mod *aiservices.Model, dir string) *aiservices.Request {
		return footageRequest(t, mod.ID, dir, 0.2, 1.4)
	}})
	if m.Frames.Load() == 0 {
		t.Fatal("the model never saw a frame")
	}
	// A matte made with no folder of the caller's goes in one of its own,
	// and Forget takes it away again.
	bare := e
	bare.Scratch = ""
	ctx := aiservices.WithEnv(context.Background(), bare)
	tk, err := a.Submit(ctx, "", footageRequest(t, local.ModelMODNet, t.TempDir(), 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	out := tk.Ready.Outputs[0]
	if out.File == "" || out.Measures["frames"] != 25 || out.Measures["people"] != 1 || tk.Ready.Usage.Seconds != 1 {
		t.Fatalf("the matte: %+v, usage %+v", out, tk.Ready.Usage)
	}
	if err := a.Forget(ctx, "", tk, tk.Ready.Outputs); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(out.File)); !os.IsNotExist(err) {
		t.Fatalf("the matte's folder is still there: %v", err)
	}
}

// Footage where no one is found has no background to replace: said plainly,
// and nothing kept.
func TestVideoBackgroundFindsNobody(t *testing.T) {
	m := fakelocal.Matter(t)
	m.Nobody = true
	e := matteEnv(t)
	a, _ := aiservices.Get(local.Name, aiservices.KindVideoBackground)
	_, err := a.Submit(aiservices.WithEnv(context.Background(), e), "", footageRequest(t, "", t.TempDir(), 0, 1))
	var ve *videogen.Error
	if !errors.As(err, &ve) || ve.Kind != videogen.KindUnusable || !strings.Contains(ve.Message, "No one was found") {
		t.Fatalf("nobody: %v", err)
	}
	m.Nobody, m.FailAt = false, 3
	_, err = a.Submit(aiservices.WithEnv(context.Background(), e), "", footageRequest(t, "", t.TempDir(), 0, 1))
	if !errors.As(err, &ve) || ve.Kind != videogen.KindUnusable || !strings.Contains(ve.Detail, "failed") {
		t.Fatalf("a model that fails: %v", err)
	}
	if _, err := a.Submit(aiservices.WithEnv(context.Background(), e), "", &aiservices.Request{Kind: aiservices.KindVideoBackground}); err == nil {
		t.Fatal("no footage, yet it ran")
	}
}

// Each model says what it needs on this computer, with the component that
// adds it; the usual model is Apple Vision's, which gives way to MODNet off
// a Mac.
func TestVideoBackgroundSaysWhatItNeeds(t *testing.T) {
	if err := local.MatterReady(aiservices.Env{}, local.ModelMODNet); err == nil {
		t.Fatal("ready with no computer")
	}
	e := matteEnv(t)
	t.Setenv("METARUSH_TOOL_DIRS", t.TempDir()) // only an empty folder: no FFmpeg here
	none := aiservices.Env{Tools: tools.New(config.Tools{}), Home: t.TempDir()}
	if err := local.MatterReady(none, local.ModelMODNet); err == nil || !strings.Contains(err.Error(), "needs FFmpeg") {
		t.Fatalf("no FFmpeg: %v", err)
	}
	err := local.MatterReady(e, local.ModelMODNet)
	if err == nil || !(strings.Contains(err.Error(), "transcriber's engine") || strings.Contains(err.Error(), "isn't on this computer (about 26 MB)")) {
		t.Fatalf("MODNet without its engine or model: %v", err)
	}
	if runtime.GOOS != "darwin" {
		if err := local.MatterReady(e, local.ModelSubject); err == nil || !strings.Contains(err.Error(), "needs a Mac") {
			t.Fatalf("Apple Vision off a Mac: %v", err)
		}
	}
	a, _ := aiservices.Get(local.Name, aiservices.KindVideoBackground)
	usual, err := aiservices.ModelFor(local.Name, aiservices.KindVideoBackground, "")
	if err != nil || usual.ID != local.ModelSubject {
		t.Fatalf("the usual model: %+v %v", usual, err)
	}
	if got, want := a.(aiservices.Here).Ready(e, ""), local.MatterReady(e, usual.ID); (got == nil) != (want == nil) {
		t.Fatalf("no model named: %v, the usual one: %v", got, want)
	}
	if v := local.MatteVersion(context.Background(), e, local.ModelMODNet); !strings.HasPrefix(v, "sha256:") {
		t.Fatalf("MODNet's version %q", v)
	}
	if v := local.MatteVersion(context.Background(), e, local.ModelSubject); !strings.HasPrefix(v, "metarush-vision") {
		t.Fatalf("Apple Vision's version %q", v)
	}
	if local.MatteVersion(context.Background(), aiservices.Env{}, local.ModelSubject) != "" {
		t.Fatal("a version with no computer")
	}
	// The runner MetaRush picks for each model.
	m, err := local.Matter(e, local.ModelMODNet)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.(matting.MODNet); !ok || m.(matting.MODNet).Path != visionmodels.StoreIn(e.Tools.ModelsDir()).MattePath() {
		t.Fatalf("MODNet's runner: %#v", m)
	}
}

// An on-screen helper too old to find the people in footage (protocol 2) is
// ready for it only where MetaRush builds it again: its own helper, on a Mac
// with Apple's Command Line Tools. One the settings point at that MetaRush
// didn't build is never built again, so it isn't ready, and says what to
// do; MetaRush's own without the tools to build it says to install them.
func TestAnOlderHelperIsReadyOnlyWhereMetaRushBuildsItAgain(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Apple Vision's helper is a Mac's")
	}
	bin := t.TempDir()
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		p, err := exec.LookPath(name)
		if err != nil {
			t.Skip("ffmpeg isn't installed here")
		}
		if err := os.Symlink(p, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(tools.EnvToolDirs, bin)               // only what is put there
	t.Setenv("METARUSH_VISION_CACHE", t.TempDir()) // no build of this version to copy
	older := func(path string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		script := "#!/bin/sh\ncase \"$1\" in\n  --version) echo 'metarush-vision 2 macOS 26.5';;\n  *) exit 2;;\nesac\n"
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	swiftc := func(there bool) {
		t.Helper()
		p := filepath.Join(bin, "swiftc")
		if !there {
			_ = os.Remove(p)
			return
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	envWith := func(home, helper string) aiservices.Env {
		return aiservices.Env{Tools: tools.New(config.Tools{Vision: helper}), Cfg: &config.Config{}, Home: home}
	}

	home := t.TempDir()
	theirs := envWith(home, older(filepath.Join(t.TempDir(), "built-by-hand", "metarush-vision")))
	swiftc(true)
	err := local.MatterReady(theirs, local.ModelSubject)
	if err == nil || !strings.Contains(err.Error(), "wasn't built by MetaRush") || !strings.Contains(apperr.From(err).Hint, "take its path out of MetaRush's settings") {
		t.Fatalf("a helper the settings point at, with Swift here: %v", err)
	}
	if d, _ := apperr.From(err).Details.(map[string]any); d["setting"] != "tools.vision" {
		t.Fatalf("the details name the setting: %+v", apperr.From(err).Details)
	}
	own := envWith(home, older(config.VisionToolPath(home)))
	if err := local.MatterReady(own, local.ModelSubject); err != nil {
		t.Fatalf("MetaRush's own helper, with Swift here to build it again: %v", err)
	}
	swiftc(false)
	own = envWith(home, config.VisionToolPath(home))
	if err := local.MatterReady(own, local.ModelSubject); err == nil || apperr.From(err).Command() != "xcode-select --install" {
		t.Fatalf("MetaRush's own helper, nothing to build it again with: %v", err)
	}
}
