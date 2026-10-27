package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/worker"
)

// seedVersion makes a project with one clip and one version of its cut.
func seedVersion(t *testing.T, a *app.App) string {
	t.Helper()
	ctx := context.Background()
	ws := a.WorkspaceID()
	now := time.Now().UTC()
	if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: ws, WorkspaceID: ws, Name: "Waits", CreatedAt: now, CreatedBy: a.Actor}); err != nil {
		t.Fatal(err)
	}
	p, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Garden story"}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: ws, ProjectIDs: []string{p.Project.ID}, Kind: "video", Name: "broll-c.mp4", Title: "Garden b-roll",
		Size: 2 << 20, State: models.AssetAvailable, Media: models.MediaInfo{HasVideo: true, HasAudio: true, Duration: 10, Width: 1080, Height: 1920, FPS: 30},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
	as.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(as.ID)))
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	spec := editspec.Spec{SchemaVersion: 1, Title: "Garden story", Output: editspec.Output{Preset: "vertical-1080p", Width: 1080, Height: 1920, FPS: 30, Platform: "shorts"},
		Clips: []editspec.Clip{{ID: "garden", AssetID: as.ID, In: 0, Out: 8}}}
	plan, _ := json.Marshal(editspec.Plan{ProjectID: p.Project.ID, Instruction: "One shot of the garden", Spec: &spec})
	chk, err := ops.CallTyped[ops.EditCheckOut](ctx, a, "edit.check", ops.EditCheckIn{Plan: plan}, ops.Caller{})
	if err != nil || !chk.Valid {
		t.Fatalf("seed cut: %v %+v", err, chk)
	}
	ap, err := ops.CallTyped[ops.EditApplyOut](ctx, a, "edit.apply", ops.EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	return ap.Revision.ID
}

// The command that hung: `render submit --wait --json` with no machine running
// MetaRush online renders in the terminal. Its first try failed the way it did
// on a busy machine (a frame the page asked the renderer's frame server for was
// not answered within 28 s), so the job went back in the queue for a second
// try — and the terminal's worker had stopped, so nothing ever took it again
// and the command waited forever. Now the terminal takes the second try once
// its wait is over, and the command finishes with the video.
func TestRenderSubmitWaitInTheTerminalTriesAgain(t *testing.T) {
	a := testapp.New(t)
	home := a.Cfg.Home()
	ctx := context.Background()
	old := jobs.RetryBase
	jobs.RetryBase = 500 * time.Millisecond
	defer func() { jobs.RetryBase = old }()

	// A renderer this machine can use without setting one up: a developer
	// checkout and a stand-in Node.js (neither runs: the render is a stand-in).
	dev := t.TempDir()
	for _, d := range []string{filepath.Join(dev, "render"), filepath.Join(dev, "node_modules")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dev, "render", "render.mjs"), []byte("// stand-in\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(t.TempDir(), "node")
	if err := os.WriteFile(node, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a.Cfg.Tools.DevRenderer, a.Cfg.Tools.VideoDir, a.Cfg.Tools.Node = true, dev, node
	if err := config.Save(home, a.Cfg); err != nil {
		t.Fatal(err)
	}
	rev := seedVersion(t, a)

	// The render's first try fails like the stalled frame; the second makes the video.
	var mu sync.Mutex
	tries := 0
	prev := newWorker
	newWorker = func(a *app.App) *worker.Worker {
		w := worker.New(a)
		w.Handle(jobs.TypeRender, func(ctx context.Context, w *worker.Worker, j *models.Job, _ worker.Report) (map[string]any, error) {
			mu.Lock()
			tries++
			n := tries
			mu.Unlock()
			if n == 1 {
				return nil, errors.New(`remotion: A delayRender() "Fetching http://localhost:65251/proxy?src=http%3A%2F%2F127.0.0.1%3A65247%2Ftoken%2Fast&time=0.1666 from server" was called but not cleared after 28000ms. See https://remotion.dev/docs/timeout for help.`)
			}
			now := time.Now().UTC()
			out := models.RenderOutput{StorageKey: "renders/stand-in.mp4", SHA256: fmt.Sprintf("%064x", 1), Size: 1 << 20, Duration: 8, Width: 540, Height: 960, FPS: 30}
			if err := w.App.DB.Update(ctx, store.CRenders, w.App.WorkspaceID(), j.RenderID, bson.M{"$set": bson.M{"state": models.RenderAvailable, "output": out, "completedAt": now}}); err != nil {
				return nil, err
			}
			return map[string]any{"renderId": j.RenderID}, nil
		})
		return w
	}
	defer func() { newWorker = prev }()

	rctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	r := runCLIContext(t, rctx, home, "render", "submit", "--revision", rev, "--quality", "review", "--no-qa", "--wait", "--json")
	if rctx.Err() != nil {
		t.Fatalf("render submit --wait never finished: the render's second try was never run\n%s", indentOut(r.stdout+r.stderr))
	}
	var doc struct {
		OK   bool `json:"ok"`
		Data struct {
			Render models.Render `json:"render"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil || r.code != 0 || !doc.OK || doc.Data.Render.State != models.RenderAvailable {
		t.Fatalf("render submit --wait (exit %d): %v\n%s", r.code, err, indentOut(r.stdout+r.stderr))
	}
	j, err := a.Queue.Get(ctx, doc.Data.Render.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if j.State != models.JobSucceeded || len(j.Attempts) != 2 || j.Attempts[0].Outcome != "retry" || j.Attempts[1].Outcome != models.JobSucceeded || tries != 2 {
		t.Fatalf("the render's tries: %s %+v (stand-in ran %d times)", j.State, j.Attempts, tries)
	}
}

type runFunc func(ctx context.Context) error

func (f runFunc) Run(ctx context.Context) error { return f(ctx) }

// waitCommand is a command to wait in (no output flags set: plain words).
func waitCommand(ctx context.Context) *cobra.Command {
	c := &cobra.Command{Use: "wait"}
	c.Flags().Bool("json", false, "")
	c.Flags().Bool("details", false, "")
	c.SetContext(ctx)
	return c
}

// The worker a terminal runs for the jobs it waits for is watched: when it
// stops while one of them still needs running (queued and due), it is started
// again; when it can't run jobs here at all, or keeps stopping at once, the wait
// ends saying so in plain words instead of waiting forever.
func TestWaitStartsTheTerminalAgainWhenItStops(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	queue := func(key string) *models.Job {
		t.Helper()
		j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeProxy, IdempotencyKey: key, Priority: 1, MaxAttempts: 2})
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	me := jobs.Worker{ID: a.Cfg.Installation.ID, Name: a.Cfg.Installation.Name}

	// It stops with the job still waiting (as it used to after a failed try): started again, it runs it.
	j := queue("proxy:stops-once")
	starts := 0
	js, err := waitJobsOpt(waitCommand(ctx), a, []string{j.ID}, 30*time.Second, true, func() jobRunner {
		starts++
		if starts == 1 {
			return runFunc(func(context.Context) error { return nil })
		}
		return runFunc(func(ctx context.Context) error {
			l, err := a.Queue.LeaseByID(ctx, me, j.ID)
			if err != nil || l == nil {
				return fmt.Errorf("lease: %v", err)
			}
			return a.Queue.Succeed(ctx, l, me.ID, map[string]any{})
		})
	})
	if err != nil || len(js) != 1 || js[0].State != models.JobSucceeded || starts != 2 {
		t.Fatalf("the wait after the terminal stopped: %v %+v (started %d times)", err, js, starts)
	}

	// It keeps stopping at once: the wait ends, plainly, with the job still waiting.
	j = queue("proxy:keeps-stopping")
	starts = 0
	began := time.Now()
	_, err = waitJobsOpt(waitCommand(ctx), a, []string{j.ID}, time.Minute, true, func() jobRunner {
		starts++
		return runFunc(func(context.Context) error { return nil })
	})
	ae := apperr.From(err)
	if err == nil || ae.Code != apperr.NoWorker || starts != quickStops || time.Since(began) > 20*time.Second {
		t.Fatalf("a terminal that keeps stopping: %v (started %d times in %v)", err, starts, time.Since(began))
	}
	if f := plainwords.Check(ae.Message + " " + ae.Hint); len(f) > 0 {
		t.Fatalf("not said plainly: %v", f)
	}
	if got, _ := a.Queue.Get(ctx, j.ID); got.State != models.JobQueued {
		t.Fatalf("the job was touched: %+v", got)
	}

	// It can't run jobs here at all: the wait ends at once with what it said.
	j = queue("proxy:cannot-run")
	missing := apperr.New(apperr.ToolMissing, "This machine can't do any MetaRush jobs yet: the programs they need (ffmpeg and ffprobe) aren't installed.")
	starts = 0
	_, err = waitJobsOpt(waitCommand(ctx), a, []string{j.ID}, time.Minute, true, func() jobRunner {
		starts++
		return runFunc(func(context.Context) error { return missing })
	})
	if !errors.Is(err, missing) || starts != 1 {
		t.Fatalf("a terminal that can't run jobs: %v (started %d times)", err, starts)
	}
}

// What the terminal starts again for: a job nothing runs that may run now.
func TestRunnableHere(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		name string
		job  models.Job
		want bool
	}{
		{"queued and due", models.Job{State: models.JobQueued, NextRunAt: now.Add(-time.Second)}, true},
		{"queued, its retry still waiting", models.Job{State: models.JobQueued, NextRunAt: now.Add(10 * time.Second)}, false},
		{"queued, being stopped", models.Job{State: models.JobQueued, CancelRequested: true}, false},
		{"running on a machine that answers", models.Job{State: models.JobRunning, Lease: &models.Lease{ExpiresAt: now.Add(30 * time.Second)}}, false},
		{"running on a machine that stopped answering", models.Job{State: models.JobRunning, Lease: &models.Lease{ExpiresAt: now.Add(-time.Second)}}, true},
		{"done", models.Job{State: models.JobSucceeded}, false},
	} {
		if got := runnableHere(&c.job, now); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
