package voiceover

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeeleven"
	"github.com/metavert-io/metarush/internal/worker"
)

func narrationJobs(t *testing.T, f *fx) []models.Job {
	t.Helper()
	var js []models.Job
	if err := f.a.DB.Find(context.Background(), store.CJobs, f.a.WorkspaceID(), bson.M{"type": jobs.TypeVoiceover}, &js); err != nil {
		t.Fatal(err)
	}
	return js
}

func notifications(t *testing.T, f *fx, kind string) []models.Notification {
	t.Helper()
	var ns []models.Notification
	if err := f.a.DB.Find(context.Background(), store.CNotifications, f.a.WorkspaceID(), bson.M{"kind": kind}, &ns); err != nil {
		t.Fatal(err)
	}
	return ns
}

// Narration work is a job people can follow: input mistakes create none, the
// work itself reads plainly in job details, and the person is told when it is
// ready (but not once per audio file it made).
func TestNarrationWorkIsAPlainJob(t *testing.T) {
	f := setup(t)
	key := "sk" + "_" + strings.Repeat("cd56", 10) + "f00d"
	fake := fakeeleven.New(key)
	defer fake.Close()
	t.Setenv(providers.EnvElevenLabsBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), "")
	rev := f.cut("Out here, the light arrives slowly.", "And then it is everywhere.")

	// Not set up yet: a plain error, and no job (nothing started).
	if _, err := call[GenerateOut](t, f.a, "voiceover.tts", TTSIn{RevisionID: rev}); !apperr.Is(err, apperr.NotConfigured) {
		t.Fatalf("not configured: %v", err)
	}
	if js := narrationJobs(t, f); len(js) != 0 {
		t.Fatalf("an input mistake must not create a job: %+v", js)
	}

	if _, err := call[providers.Status](t, f.a, "provider.set", providers.SetIn{Name: providers.ElevenLabsName, Key: key}); err != nil {
		t.Fatal(err)
	}
	if _, err := call[providers.SettingsOut](t, f.a, "provider.settings", providers.SettingsIn{Name: providers.ElevenLabsName, Voice: "Ada"}); err != nil {
		t.Fatal(err)
	}
	out, err := call[GenerateOut](t, f.a, "voiceover.tts", TTSIn{RevisionID: rev})
	if err != nil {
		t.Fatal(err)
	}
	if out.JobID == "" {
		t.Fatal("the generation should name its job")
	}
	d, err := call[ops.JobShowOut](t, f.a, "job.show", ops.JobIDIn{ID: out.JobID})
	if err != nil {
		t.Fatal(err)
	}
	if d.State != models.JobSucceeded || d.KindLabel != "Narration" || d.Title != "AI voice for Film, version 1" || d.Produced == nil || d.Produced.Summary != out.Summary ||
		d.Target == nil || d.Target.Kind != "version" || d.Related.Revision == nil || d.Related.Revision.ID != rev || d.CanRetry {
		t.Fatalf("job details: %+v", d)
	}
	ready := notifications(t, f, models.NotifyNarrationReady)
	if len(ready) != 1 || ready[0].Title != "AI voice ready for 2 lines" || !strings.Contains(ready[0].Link, rev) || ready[0].Tone != "success" {
		t.Fatalf("ready notification: %+v", ready)
	}
	for _, tk := range out.Takes {
		var as models.Asset
		if err := f.a.DB.GetByID(context.Background(), store.CAssets, f.a.WorkspaceID(), tk.AssetID, &as); err != nil || !as.MadeNarration() {
			t.Fatalf("the AI voice files are narration MetaRush made: %+v %v", as.Tags, err)
		}
	}
}

// Work put in the cut right away (apply) says so, and links to that version
// instead of asking the person to apply it.
func TestAppliedNarrationSaysItIsInTheCut(t *testing.T) {
	f := setup(t)
	key := "sk" + "_" + strings.Repeat("ab12", 10) + "beef"
	fake := fakeeleven.New(key)
	defer fake.Close()
	t.Setenv(providers.EnvElevenLabsBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), "")
	rev := f.cut("Out here, the light arrives slowly.", "And then it is everywhere.")
	if _, err := call[providers.Status](t, f.a, "provider.set", providers.SetIn{Name: providers.ElevenLabsName, Key: key}); err != nil {
		t.Fatal(err)
	}
	if _, err := call[providers.SettingsOut](t, f.a, "provider.settings", providers.SettingsIn{Name: providers.ElevenLabsName, Voice: "Ada"}); err != nil {
		t.Fatal(err)
	}
	out, err := call[GenerateOut](t, f.a, "voiceover.tts", TTSIn{RevisionID: rev, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.Applied == nil || out.Applied.Problem != "" || out.Applied.RevisionID == "" || out.Applied.RevisionID == rev {
		t.Fatalf("the AI voice should be in a new version: %+v", out.Applied)
	}
	ready := notifications(t, f, models.NotifyNarrationReady)
	want := fmt.Sprintf("It is in the cut now as version %d.", out.Applied.Number)
	if len(ready) != 1 || ready[0].Body != want || !strings.Contains(ready[0].Link, out.Applied.RevisionID) || ready[0].Details["revisionId"] != out.Applied.RevisionID {
		t.Fatalf("ready notification: %+v", ready)
	}
}

// A failure after the work started is recorded with a plain message, its
// stable code and advice; stopping it between lines cancels quietly.
func TestNarrationFailureAndStop(t *testing.T) {
	f := setup(t)
	rev := f.cut("Hello there.", "And again.")
	ctx := context.Background()

	_, err := track(ctx, f.a, ActionTTS, rev, "", func(ctx context.Context) (*GenerateOut, error) {
		if err := step(ctx, 0, 2, "Making the AI voice for line 1"); err != nil {
			return nil, err
		}
		return nil, apperr.New(apperr.Provider, "ElevenLabs is busy right now")
	})
	if !apperr.Is(err, apperr.Provider) {
		t.Fatalf("the original error comes back: %v", err)
	}
	js := narrationJobs(t, f)
	if len(js) != 1 || js[0].State != models.JobFailed || js[0].Error != "ElevenLabs is busy right now" || js[0].Payload["errorCode"] != apperr.Provider {
		t.Fatalf("failed job: %+v", js)
	}
	d, err := call[ops.JobShowOut](t, f.a, "job.show", ops.JobIDIn{ID: js[0].ID})
	if err != nil || !strings.Contains(d.Advice, "ElevenLabs could not do it right now") || d.CanRetry {
		t.Fatalf("advice: %+v %v", d, err)
	}
	failed := notifications(t, f, models.NotifyNarrationFailed)
	// The notification says what to do (the job's advice); the error itself is in Details.
	if len(failed) != 1 || failed[0].Title != "Could not make the AI voice" || !strings.Contains(failed[0].Body, "ElevenLabs could not do it right now") ||
		failed[0].Details["error"] != "ElevenLabs is busy right now" || failed[0].Details["code"] != apperr.Provider {
		t.Fatalf("failure notification: %+v", failed)
	}
	if _, err := call[models.Job](t, f.a, "job.retry", ops.JobIDIn{ID: js[0].ID}); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("narration is started again where it was asked for, not retried: %v", err)
	}

	// The person stops it: the next line notices, the job is canceled, nobody is notified.
	var stopped string
	_, err = track(ctx, f.a, ActionScratch, rev, "", func(ctx context.Context) (*GenerateOut, error) {
		if err := step(ctx, 0, 2, "Making the scratch voice for line 1"); err != nil {
			return nil, err
		}
		t := ctx.Value(trackKey{}).(*tracker)
		stopped = t.job.ID
		if _, err := f.a.Queue.RequestCancel(ctx, stopped); err != nil {
			return nil, err
		}
		time.Sleep(1100 * time.Millisecond)
		return nil, step(ctx, 1, 2, "Making the scratch voice for line 2")
	})
	if !apperr.Is(err, apperr.Canceled) {
		t.Fatalf("stop: %v", err)
	}
	j, _ := f.a.Queue.Get(ctx, stopped)
	if j == nil || j.State != models.JobCanceled {
		t.Fatalf("stopped job: %+v", j)
	}
	if n := len(notifications(t, f, models.NotifyNarrationFailed)) + len(notifications(t, f, models.NotifyNarrationReady)); n != 1 {
		t.Fatalf("stopping must not notify: %d notifications", n)
	}
}

// If the process making narration stops, its lease runs out and a worker
// closes the job plainly (never redoing the work elsewhere) and tells the person.
func TestInterruptedNarrationIsClosedPlainly(t *testing.T) {
	f := setup(t)
	rev := f.cut("Hello there.")
	ctx := context.Background()
	// Narration jobs get a second attempt only so a worker can close them.
	var budget int
	_, _ = track(ctx, f.a, ActionTTS, rev, "", func(ctx context.Context) (*GenerateOut, error) {
		if err := step(ctx, 0, 1, "Making the AI voice for line 1"); err != nil {
			return nil, err
		}
		budget = ctx.Value(trackKey{}).(*tracker).job.MaxAttempts
		return nil, apperr.New(apperr.Provider, "busy")
	})
	if budget != 2 {
		t.Fatalf("narration attempt budget %d, want 2", budget)
	}
	// A process making narration stops: its job stays running until the lease runs out.
	j, err := f.a.Queue.Start(ctx, jobs.EnqueueOptions{Type: jobs.TypeVoiceover, ProjectID: f.project, MaxAttempts: 2, Actor: f.a.Actor,
		Payload: map[string]any{"action": ActionTTS, "title": ActionTitles[ActionTTS], "revisionId": rev, "revisionNumber": 1, "lines": 1}},
		jobs.Worker{ID: f.a.Cfg.Installation.ID, Name: "studio"})
	if err != nil {
		t.Fatal(err)
	}
	id := j.ID
	if _, err := f.a.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"lease.expiresAt": time.Now().Add(-time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	w := worker.New(f.a)
	w.OnlyJobID, w.IgnoreBattery, w.AcceptsHeavy = id, true, true
	w.Log = func(string, ...any) {}
	rctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := w.Run(rctx); err != nil {
		t.Fatal(err)
	}
	j, _ = f.a.Queue.Get(ctx, id)
	if j.State != models.JobFailed || !strings.HasPrefix(j.Error, "MetaRush stopped while doing this") {
		t.Fatalf("interrupted job: %s %q", j.State, j.Error)
	}
	var ns []models.Notification
	for _, n := range notifications(t, f, models.NotifyNarrationFailed) {
		if n.JobID == id {
			ns = append(ns, n)
		}
	}
	if len(ns) != 1 || ns[0].Title != "Could not make the AI voice" || !strings.Contains(ns[0].Body, "Start it again") ||
		!strings.HasPrefix(fmt.Sprint(ns[0].Details["error"]), "MetaRush stopped while doing this") {
		t.Fatalf("interrupted notification: %+v", ns)
	}
	d, err := call[ops.JobShowOut](t, f.a, "job.show", ops.JobIDIn{ID: id})
	if err != nil || !strings.Contains(d.Advice, "Start it again from the same computer") {
		t.Fatalf("advice: %+v %v", d, err)
	}
}
