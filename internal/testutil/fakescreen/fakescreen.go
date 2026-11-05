// Package fakescreen stands in the open models that look at what's on screen
// (D413): it writes stand-in models with the real ones' names and shapes
// (onnxtest) where a computer looks for them, pins them by their own
// checksums for the test, and puts a transcriber's install beside them whose
// folder holds onnxruntime — a link to the real library, so the stand-ins
// really run. Tests that need onnxruntime skip plainly without it.
package fakescreen

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/onnx/onnxtest"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/visionmodels"
)

// Staged is where the stand-ins went: set them on a computer's tools.
type Staged struct {
	SherpaDir, ModelsDir string
}

// Apply points a computer's tool settings at the stand-ins.
func (s Staged) Apply(t *config.Tools) {
	t.SherpaDir, t.ModelsDir = s.SherpaDir, s.ModelsDir
}

// Stage writes the stand-in models and the transcriber's folder (onnxruntime
// linked from the library tests use; the test is skipped without one) and
// makes them the pinned files until the test ends.
func Stage(t testing.TB) Staged {
	t.Helper()
	lib := onnxtest.Library(t)
	models := t.TempDir()
	dir := visionmodels.Dir(models)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	stand := map[string]string{
		"screen-faces": onnxtest.FaceFinder(t, src), "screen-text-find": onnxtest.TextFinder(t, src), "screen-text-read": onnxtest.TextReader(t, src),
		"screen-text-chars": onnxtest.TextChars(t, src), "screen-attention": onnxtest.Saliency(t, src), "cut-outs": onnxtest.Subject(t, src),
		"video-cut-outs": onnxtest.Matte(t, src),
	}
	old := visionmodels.Files
	t.Cleanup(func() { visionmodels.Files = old })
	var files []visionmodels.File
	for _, f := range old {
		b, err := os.ReadFile(stand[f.ID])
		if err != nil {
			t.Fatalf("no stand-in for %s: %v", f.ID, err)
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
	for _, d := range []string{"bin", "lib"} {
		if err := os.MkdirAll(filepath.Join(sherpa, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sherpa, "bin", tools.Sherpa), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	name := "libonnxruntime.so"
	if runtime.GOOS == "darwin" {
		name = "libonnxruntime.dylib"
	}
	if err := os.Symlink(lib, filepath.Join(sherpa, "lib", name)); err != nil {
		t.Fatal(err)
	}
	return Staged{SherpaDir: sherpa, ModelsDir: models}
}
