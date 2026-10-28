package cli

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// A computer that handed its role over still follows the workspace while it
// runs as a client: a move of the database starts it again holding (it
// changes things too, through its dashboard and panel), and a new media
// storage in config.yaml starts it again with that storage.
func TestClientFollowsMovesAndConfig(t *testing.T) {
	was := MoveCheckEvery
	MoveCheckEvery = 50 * time.Millisecond
	t.Cleanup(func() { MoveCheckEvery = was })

	run := func(t *testing.T, change func(home string, db *store.DB, ws string)) (bool, string) {
		t.Helper()
		a := testapp.New(t)
		testapp.HandedOver(t, a, "studio-mini")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var mu sync.Mutex
		var said []string
		logf := func(f string, x ...any) {
			mu.Lock()
			said = append(said, f)
			mu.Unlock()
		}
		var restart atomic.Bool
		stopped := make(chan struct{})
		clientWatch(ctx, a, logf, &restart, func() { cancel(); close(stopped) })
		change(a.Cfg.Home(), a.DB, a.WorkspaceID())
		select {
		case <-stopped:
		case <-time.After(20 * time.Second):
		}
		mu.Lock()
		defer mu.Unlock()
		return restart.Load(), strings.Join(said, "\n")
	}

	t.Run("a move of the database", func(t *testing.T) {
		restarted, said := run(t, func(_ string, db *store.DB, ws string) {
			if err := db.StartMoveMarker(context.Background(), store.MoveMarker{Move: "mvclient1", Workspace: ws,
				To: store.MoveTarget{Kind: "server", Words: "a MongoDB server on this computer", Name: "elsewhere", Reach: "every-computer"},
				By: "Maya", From: "ins_other", FromName: "studio-mini"}); err != nil {
				t.Fatal(err)
			}
		})
		if !restarted || !strings.Contains(said, "pausing") {
			t.Fatalf("the client starts again holding: %v %q", restarted, said)
		}
	})
	t.Run("a new media storage", func(t *testing.T) {
		restarted, said := run(t, func(home string, _ *store.DB, _ string) {
			time.Sleep(1100 * time.Millisecond) // a new modification time on every file system
			if err := config.SetField(home, []string{"media", "filesystem", "root"}, filepath.Join(t.TempDir(), "elsewhere")); err != nil {
				t.Fatal(err)
			}
		})
		if !restarted || !strings.Contains(said, "media storage is now") {
			t.Fatalf("the client starts again with the new storage: %v %q", restarted, said)
		}
	})
}
