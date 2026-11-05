package intake_test

import (
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/intake"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/wsl"
)

// Inside WSL (D520, D521) `metarush add` takes paths as Windows gives them —
// File Explorer's "Copy as path" in quotes, C:/… — adds them from where WSL
// shows them, says they came from the Windows side and what is faster, and a
// folder there is linked with the same words; another Linux's files fail in
// plain words.
func TestAddPathsFromTheWindowsSide(t *testing.T) {
	a := testapp.New(t)
	needProbe(t, a)
	c, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	videos := filepath.Join(c, "Users", "You", "Videos")
	brand := filepath.Join(c, "Users", "You", "Brand")
	for _, d := range []string{videos, brand} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(wsl.Pretend(wsl.Info{Version: 2, Distro: "Ubuntu", DistroKnown: true, Interop: true, DriveRoot: "/mnt/", Drives: map[string]string{c: "c"}}))
	wav := tone(t, videos, "birdsong.wav", 1.5, 440)
	logo := logoPNG(t, videos, "garden-logo.png", color.RGBA{30, 140, 60, 255})
	logoPNG(t, brand, "badge.png", color.RGBA{200, 40, 40, 255})

	out := call[intake.AddPathsOut](t, a, "add.paths", intake.AddPathsIn{Paths: []string{
		`"C:\Users\You\Videos\birdsong.wav"`, `C:/Users/You/Videos/garden-logo.png`, `C:\Users\You\Brand`, `\\wsl.localhost\Debian\home\you\clip.mov`}})
	if f := fileNamed(out.Files, "birdsong.wav"); f == nil || f.Status != "ingested" || f.Path != wav {
		t.Fatalf("footage: %+v", f)
	}
	if f := fileNamed(out.Files, "garden-logo.png"); f == nil || f.Status != resources.StatusAdded || f.Path != logo {
		t.Fatalf("picture: %+v", f)
	}
	if f := fileNamed(out.Files, "clip.mov"); f == nil || f.Status != "failed" || !strings.Contains(f.Message, "another Linux on this PC (Debian)") {
		t.Fatalf("another distribution: %+v", out.Files)
	}
	if !strings.Contains(out.Slow, "2 of these files are on the Windows side") {
		t.Fatalf("slow: %q", out.Slow)
	}
	if len(out.Folders) != 1 || !strings.Contains(out.Folders[0].Folder.Slow, "On the Windows side (C:)") || out.Folders[0].Folder.HerePath != `C:\Users\You\Brand` {
		t.Fatalf("the linked folder: %+v", out.Folders)
	}
}
