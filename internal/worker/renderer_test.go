package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/testutil/fakenode"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

// A render never runs with a renderer that is not this version's: one that
// was never set up or was removed fails the render for good with what to do,
// and one being set up by another process is waited for.
func TestRenderNeedsThisVersionsRenderer(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	pkg, err := renderer.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	fake := fakenode.Write(t, pkg.Remotion())
	a.Cfg.Tools.Node, a.Cfg.Tools.NPM = fake.Node, fake.NPM
	a.Tools = tools.New(a.Cfg.Tools)
	nothing := func(float64, string) {}

	_, _, err = rendererFor(ctx, a, nothing)
	var p *Permanent
	if !errors.As(err, &p) || !strings.Contains(err.Error(), "isn't set up") || !strings.Contains(err.Error(), "metarush renderer install") {
		t.Fatalf("never set up: %v", err)
	}
	if New(a).Can(jobs.TypeRender) {
		t.Fatal("offered to render without a renderer")
	}

	env := renderer.New(a.Cfg, a.Tools)
	st, err := env.Install(ctx, renderer.InstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got, release, err := rendererFor(ctx, a, nothing)
	if err != nil || got.VideoDir != st.VideoDir || got.ID != pkg.ID() {
		t.Fatalf("ready: %+v %v", got, err)
	}
	release()
	if !New(a).Can(jobs.TypeRender) || RenderReady(a) != nil {
		t.Fatal("a ready renderer is not offered")
	}

	if err := os.RemoveAll(st.Dir); err != nil {
		t.Fatal(err)
	}
	_, _, err = rendererFor(ctx, a, nothing)
	if !errors.As(err, &p) || !strings.Contains(err.Error(), "was removed") || !strings.Contains(err.Error(), "metarush renderer install") {
		t.Fatalf("removed: %v", err)
	}

	// Another process is setting it up: the render waits, then runs.
	lock, err := os.OpenFile(filepath.Join(renderer.Root(a.Cfg.Home()), ".lock"), os.O_CREATE|os.O_RDONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	if !New(a).Can(jobs.TypeRender) {
		t.Fatal("a machine setting its renderer up should take renders (they wait)")
	}
	var mu sync.Mutex
	var said []string
	go func() {
		time.Sleep(time.Second)
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_, _ = env.Install(ctx, renderer.InstallOptions{})
	}()
	got, release, err = rendererFor(ctx, a, func(_ float64, msg string) {
		mu.Lock()
		said = append(said, msg)
		mu.Unlock()
	})
	if err != nil || !got.Ready {
		t.Fatalf("after waiting: %+v %v", got, err)
	}
	release()
	mu.Lock()
	defer mu.Unlock()
	if len(said) == 0 || !strings.Contains(said[0], "waiting for the renderer") {
		t.Fatalf("said %v", said)
	}
}
