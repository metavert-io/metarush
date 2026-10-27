package vision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// maskHelper copies the picture to the output for `mask` (found), or prints
// found:false when the picture's name says so.
func maskHelper(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "metarush-vision")
	script := "#!/bin/sh\ncase \"$1\" in\n  --version) echo 'metarush-vision " + Protocol + " macOS 26.5';;\n" +
		"  mask) [ \"$2\" = \"--\" ] || exit 2; case \"$3\" in *empty*) echo '{\"found\":false}';; *) cp \"$3\" \"$4\" && echo '{\"found\":true,\"instances\":1}';; esac;;\n" +
		"  *) exit 2;;\nesac\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// Remove the background on a Mac (D152): the helper's mask command, with
// paths MetaRush wrote only; no subject is said plainly, not an error.
func TestMaskCutsTheSubjectOut(t *testing.T) {
	ctx := context.Background()
	if runtime.GOOS != "darwin" {
		if _, err := Mask(ctx, tools.New(config.Tools{}), "/a.png", "/b.png"); !errors.Is(err, ErrNotMac) {
			t.Fatalf("not a Mac: %v", err)
		}
		return
	}
	ts := tools.New(config.Tools{Vision: maskHelper(t)})
	dir := t.TempDir()
	in, out := filepath.Join(dir, "picture.png"), filepath.Join(dir, "subject.png")
	if err := os.WriteFile(in, []byte("png bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := Mask(ctx, ts, in, out)
	if err != nil || !found {
		t.Fatalf("mask: %v %v", found, err)
	}
	if b, _ := os.ReadFile(out); string(b) != "png bytes" {
		t.Fatalf("output %q", b)
	}
	empty := filepath.Join(dir, "empty.png")
	_ = os.WriteFile(empty, []byte("x"), 0o600)
	if found, err := Mask(ctx, ts, empty, filepath.Join(dir, "none.png")); err != nil || found {
		t.Fatalf("no subject: %v %v", found, err)
	}
	for _, bad := range [][2]string{{"rel.png", out}, {in, filepath.Join(dir, "-x.png")}} {
		if _, err := Mask(ctx, ts, bad[0], bad[1]); err == nil {
			t.Fatalf("refused paths: %v", bad)
		}
	}
}
