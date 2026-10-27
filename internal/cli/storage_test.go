package cli

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
)

// withStdin runs f with text on this process's stdin.
func withStdin(t *testing.T, text string, f func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString(text)
	_ = w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old; _ = r.Close() }()
	f()
}

// The process the dashboard starts for a confirmed move (D112): without its
// grant it moves nothing; with it, it moves the media like `storage migrate`,
// switches this computer and records how it went; `storage status` says so
// in plain words. Claude in the dashboard can never run it.
func TestDashboardMoveProcess(t *testing.T) {
	h := testHome(t)
	ctx := context.Background()
	a, err := app.Open(ctx, h, "cli")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	// One clip's bytes in the old folder.
	src := filepath.Join(t.TempDir(), "take.mov")
	b := make([]byte, 2048)
	_, _ = rand.Read(b)
	_ = os.WriteFile(src, b, 0o600)
	sha, _ := media.HashFile(src)
	key := media.ObjectKey(a.WorkspaceID(), sha)
	if _, err := a.Media.PutFile(ctx, key, src, media.PutOptions{SHA256: sha}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	newRoot := filepath.Join(t.TempDir(), "New media")
	target := ops.StorageTarget{Provider: "filesystem", Root: newRoot}

	mv, _, err := ops.PlanDashboardMove(ctx, a, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ops.GrantDashboardMove(a, mv.ID, false); err != nil {
		t.Fatal(err)
	}
	var r cliRun
	withStdin(t, "not-the-grant\n", func() { r = runCLI(t, h, "storage", "migrate", "--dashboard-move", mv.ID) })
	if r.code == 0 || !strings.Contains(r.stderr, "wasn't confirmed by a person") {
		t.Fatalf("without the grant: %d %q %q", r.code, r.stdout, r.stderr)
	}
	if cfg, _ := config.Load(h); cfg.Media.Filesystem.Root == newRoot {
		t.Fatal("switched without the grant")
	}
	// Claude in the dashboard can't run it, grant or not.
	t.Setenv("METARUSH_VIA", "assistant")
	withStdin(t, "x\n", func() { r = runCLI(t, h, "storage", "migrate", "--dashboard-move", mv.ID) })
	if r.code == 0 {
		t.Fatalf("assistant: %q %q", r.stdout, r.stderr)
	}
	t.Setenv("METARUSH_VIA", "")

	mv2, _, err := ops.PlanDashboardMove(ctx, a, target)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := ops.GrantDashboardMove(a, mv2.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	withStdin(t, token+"\n", func() { r = runCLI(t, h, "storage", "migrate", "--dashboard-move", mv2.ID) })
	if r.code != 0 || !strings.Contains(r.stdout, "moving the media to the folder New media on this computer") || !strings.Contains(r.stdout, "Moved 1 file") {
		t.Fatalf("move: %d %q %q", r.code, r.stdout, r.stderr)
	}
	cfg, err := config.Load(h)
	if err != nil || cfg.Media.Filesystem.Root != newRoot {
		t.Fatalf("config.yaml: %+v %v", cfg.Media, err)
	}
	if got, err := os.ReadFile(filepath.Join(newRoot, filepath.FromSlash(key))); err != nil || string(got) != string(b) {
		t.Fatalf("the clip in the new folder: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(a.Cfg.Media.Filesystem.Root, filepath.FromSlash(key))); err != nil || string(got) != string(b) {
		t.Fatalf("the old folder lost the clip: %v", err)
	}
	// The grant was used: the same process can't run again.
	withStdin(t, token+"\n", func() { r = runCLI(t, h, "storage", "migrate", "--dashboard-move", mv2.ID) })
	if r.code == 0 {
		t.Fatal("a grant used twice")
	}

	r = runCLI(t, h, "storage", "status")
	if r.code != 0 || !strings.HasPrefix(r.stdout, "This computer keeps the media in the folder New media on this computer.") ||
		!strings.Contains(r.stdout, "The last move started in the dashboard: Moved 1 file") {
		t.Fatalf("status: %d %q %q", r.code, r.stdout, r.stderr)
	}
	if p := plainwords.CheckShown(r.stdout); len(p) > 0 {
		t.Fatalf("status isn't plain: %v\n%s", p, r.stdout)
	}
	r = runCLI(t, h, "storage", "status", "--details")
	if !strings.Contains(r.stdout, "fs:"+newRoot) {
		t.Fatalf("details: %q", r.stdout)
	}
}
