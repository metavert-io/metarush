package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/worker"
)

// onlyFFmpeg plays a computer that has FFmpeg and nothing else MetaRush looks
// for (no speech recognizer: transcription is a component left out). It
// returns false when this computer has no FFmpeg to lend.
func onlyFFmpeg(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		p, err := exec.LookPath(name)
		if err != nil {
			return false
		}
		if err := os.Symlink(p, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(tools.EnvToolDirs, dir)
	return true
}

// testClip makes a short clip with picture and sound (a tone at hz).
func testClip(t *testing.T, name string, hz int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=64x36:rate=24:duration=2",
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:duration=2", hz), "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", p)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	return p
}

// The command that waited forever: `ingest --wait` on a computer without
// transcription (an optional component since D324), with no other computer
// running MetaRush. The preview copies were made in the terminal; the
// transcript waited for a component nobody had, and the command sat there
// saying nothing. Now it finishes what it can and says, in plain words, what
// waits and what adds it (the command that does is behind --details), ending
// like any command that lacks a program (exit 6).
func TestIngestWaitSaysWhatWaitsForAComponent(t *testing.T) {
	if !onlyFFmpeg(t) {
		t.Skip("needs FFmpeg on this computer")
	}
	a := testapp.New(t)
	home := a.Cfg.Home()
	clip := testClip(t, "garden-walk.mp4", 330)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r := runCLIContext(t, ctx, home, "ingest", clip, "--wait")
	if ctx.Err() != nil {
		t.Fatalf("ingest --wait never finished: it waited for a transcript no computer can make\n%s", indentOut(r.stdout+r.stderr))
	}
	out := r.stdout + r.stderr
	t.Logf("plain:\n%s", indentOut(out))
	if r.code != exitCodeFor(apperr.ToolMissing) {
		t.Errorf("exit %d, want %d (a program it needs is missing)\n%s", r.code, exitCodeFor(apperr.ToolMissing), indentOut(out))
	}
	plainOK(t, "ingest --wait without transcription", out)
	wantIn(t, "ingest --wait without transcription", out,
		"Added “garden-walk”",                                 // what it did
		"preview copy ready",                                  // what it finished here
		"Transcripts wait for Transcription on this computer", // what waits, and for what
		"Settings → Components",                               // where it is added
	)
	if strings.Contains(out, "metarush components add") {
		t.Errorf("the command that adds it belongs behind --details:\n%s", indentOut(out))
	}

	// --details names the command; --json carries everything in one document.
	again := testClip(t, "garden-pond.mp4", 440)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel2()
	r = runCLIContext(t, ctx2, home, "ingest", again, "--wait", "--json")
	if ctx2.Err() != nil {
		t.Fatalf("ingest --wait --json never finished\n%s", indentOut(r.stdout+r.stderr))
	}
	var doc struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details struct {
				Command string `json:"command"`
				Waiting []struct {
					JobID     string `json:"jobId"`
					Type      string `json:"type"`
					Component string `json:"component"`
				} `json:"waiting"`
				Ingest struct {
					Ingested int `json:"ingested"`
				} `json:"ingest"`
				Jobs []models.Job `json:"jobs"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatalf("not one JSON document: %v\n%s", err, indentOut(r.stdout+r.stderr))
	}
	d := doc.Error.Details
	if doc.OK || doc.Error.Code != apperr.ToolMissing || r.code != exitCodeFor(apperr.ToolMissing) || d.Command != "metarush components add transcription" ||
		d.Ingest.Ingested != 1 || len(d.Waiting) == 0 || d.Waiting[0].Type != jobs.TypeTranscribe || d.Waiting[0].Component != "transcription" {
		t.Fatalf("ingest --wait --json (exit %d):\n%s", r.code, indentOut(r.stdout+r.stderr))
	}
	done := map[string]string{}
	for _, j := range d.Jobs {
		done[j.Type] = j.State
	}
	if done[jobs.TypeProxy] != models.JobSucceeded || done[jobs.TypeTranscribe] != models.JobQueued {
		t.Errorf("what it finished, and what still waits: %v", done)
	}
}

// Any wait for jobs stops waiting for a job no computer can run: not this
// one (it lacks the component) and no other running MetaRush. One another
// computer is running is waited for, as before; one a computer that isn't
// running now could do is named.
func TestWaitStopsForJobsNoComputerCanRun(t *testing.T) {
	// A computer with FFmpeg (stand-ins: nothing here runs them) and no recognizer.
	dir := t.TempDir()
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(tools.EnvToolDirs, dir)
	a := testapp.New(t)
	ctx := context.Background()
	queue := func(typ, key string) *models.Job {
		t.Helper()
		j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: typ, IdempotencyKey: key, Priority: 1, MaxAttempts: 2, Heavy: typ == jobs.TypeTranscribe})
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	prev := newWorker
	defer func() { newWorker = prev }()
	newWorker = func(a *app.App) *worker.Worker {
		w := worker.New(a)
		// Preview copies are made here (a stand-in), transcripts can't be.
		w.Handle(jobs.TypeProxy, func(context.Context, *worker.Worker, *models.Job, worker.Report) (map[string]any, error) {
			return map[string]any{}, nil
		})
		return w
	}

	proxy, tx := queue(jobs.TypeProxy, "proxy:stuck-1"), queue(jobs.TypeTranscribe, "transcribe:stuck-1")
	began := time.Now()
	js, err := waitJobsOpt(waitCommand(ctx), a, []string{proxy.ID, tx.ID}, time.Minute, true, nil)
	ae := apperr.From(err)
	if err == nil || ae.Code != apperr.ToolMissing || time.Since(began) > 30*time.Second {
		t.Fatalf("a wait for a transcript no computer can make: %v after %v", err, time.Since(began))
	}
	plainOK(t, "the wait's ending", ae.Message+"\n"+ae.Hint)
	wantIn(t, "the wait's ending", ae.Message, "Transcripts wait for Transcription on this computer", "Settings → Components")
	if ae.Command() != "metarush components add transcription" {
		t.Errorf("the command that adds it: %q", ae.Command())
	}
	states := map[string]string{}
	for _, j := range js {
		states[j.Type] = j.State
	}
	if states[jobs.TypeProxy] != models.JobSucceeded || states[jobs.TypeTranscribe] != models.JobQueued {
		t.Errorf("finished what it could, left the rest queued: %v", states)
	}

	// Another computer running MetaRush can make transcripts: the wait is for it.
	now := time.Now().UTC()
	studio := models.WorkerDevice{ID: "ins_studio", WorkspaceID: a.WorkspaceID(), Name: "studio-mac", JobTypes: []string{jobs.TypeTranscribe},
		Concurrency: 1, AcceptsHeavy: true, HeavyEnabled: true, LastHeartbeat: now}
	if err := a.DB.Insert(ctx, store.CWorkers, studio); err != nil {
		t.Fatal(err)
	}
	tx2 := queue(jobs.TypeTranscribe, "transcribe:elsewhere")
	_, err = waitJobsOpt(waitCommand(ctx), a, []string{tx2.ID}, 4*time.Second, true, nil)
	if err == nil || apperr.From(err).Code == apperr.ToolMissing {
		t.Fatalf("a transcript another computer can make is waited for: %v", err)
	}

	// It stopped running: the wait ends, and says which computer could make it.
	if err := a.DB.Update(ctx, store.CWorkers, a.WorkspaceID(), studio.ID, bson.M{"$set": bson.M{"lastHeartbeat": now.Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	_, err = waitJobsOpt(waitCommand(ctx), a, []string{tx2.ID}, time.Minute, true, nil)
	ae = apperr.From(err)
	if ae.Code != apperr.ToolMissing {
		t.Fatalf("a transcript only a stopped computer can make: %v", err)
	}
	plainOK(t, "the wait's ending", ae.Message+"\n"+ae.Hint)
	wantIn(t, "the wait's ending", ae.Message+" "+ae.Hint, "studio-mac")
}
