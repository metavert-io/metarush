package notify

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func notifications(t *testing.T, a *app.App, kind string) []models.Notification {
	t.Helper()
	var out []models.Notification
	if err := a.DB.Find(context.Background(), store.CNotifications, a.WorkspaceID(), bson.M{"kind": kind}, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func addJob(t *testing.T, a *app.App, assetID, typ, state, errMsg string, created time.Time) string {
	t.Helper()
	j := models.Job{ID: ids.New(ids.Job), WorkspaceID: a.WorkspaceID(), Type: typ, State: state, AssetID: assetID, Error: errMsg,
		Payload: map[string]any{}, Attempts: []models.Attempt{}, IdempotencyKey: ids.New("k"), CreatedBy: models.Actor{UserID: a.Cfg.User.ID},
		CreatedAt: created, UpdatedAt: created, NextRunAt: created}
	if err := a.DB.Insert(context.Background(), store.CJobs, j); err != nil {
		t.Fatal(err)
	}
	return j.ID
}

func TestFileReadyIsOnePerAssetAndRefreshesOnlyOnChange(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC().Add(-time.Minute)
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: "IMG_2391.MOV", Title: "Garage interview",
		CreatedBy: models.Actor{UserID: a.Cfg.User.ID}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"thumbnail", "proxy", "evidence"} {
		addJob(t, a, as.ID, typ, models.JobSucceeded, "", now)
	}
	tx := addJob(t, a, as.ID, "transcribe", models.JobRunning, "", now)

	AssetSettled(ctx, a, as.ID)
	if n := notifications(t, a, models.NotifyFileReady); len(n) != 0 {
		t.Fatalf("notified while the transcript was still running: %+v", n)
	}
	if _, err := a.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": tx}, bson.M{"$set": bson.M{"state": models.JobSucceeded}}); err != nil {
		t.Fatal(err)
	}
	AssetSettled(ctx, a, as.ID)
	AssetSettled(ctx, a, as.ID) // every finishing job calls it; still one notification
	got := notifications(t, a, models.NotifyFileReady)
	if len(got) != 1 {
		t.Fatalf("%d file-ready notifications, want 1", len(got))
	}
	n := got[0]
	if n.Title != "Garage interview is ready" || n.Tone != "success" || n.Read {
		t.Fatalf("notification %+v", n)
	}
	if n.Body != "You can now watch it, read what was said and see its scenes." {
		t.Fatalf("body %q", n.Body)
	}
	if !strings.Contains(n.Link, as.ID) || n.UserID != a.Cfg.User.ID {
		t.Fatalf("link %q user %q", n.Link, n.UserID)
	}

	// Read it; the same outcome again leaves it read.
	if _, err := a.DB.C(store.CNotifications).UpdateOne(ctx, bson.M{"_id": n.ID}, bson.M{"$set": bson.M{"read": true}}); err != nil {
		t.Fatal(err)
	}
	AssetSettled(ctx, a, as.ID)
	if again := notifications(t, a, models.NotifyFileReady); len(again) != 1 || !again[0].Read {
		t.Fatalf("an unchanged outcome re-marked the notification unread: %+v", again)
	}

	// A re-run that fails changes the outcome: same notification, unread again,
	// plain words in the sentence and the technical error kept in details.
	addJob(t, a, as.ID, "transcribe", models.JobFailed, "sherpa-onnx-offline failed: exit status 1\nstack…", now.Add(30*time.Second))
	AssetSettled(ctx, a, as.ID)
	again := notifications(t, a, models.NotifyFileReady)
	if len(again) != 1 {
		t.Fatalf("%d notifications after the re-run", len(again))
	}
	f := again[0]
	if f.Read || f.Tone != "error" || f.Title != "Garage interview is ready, but the transcript failed" {
		t.Fatalf("after failure: %+v", f)
	}
	if strings.Contains(f.Body, "sherpa") || !strings.Contains(f.Body, "retry the transcript") {
		t.Fatalf("body should be plain and say what to do: %q", f.Body)
	}
	if f.Details["error"] != "sherpa-onnx-offline failed: exit status 1" {
		t.Fatalf("details %v", f.Details)
	}
}

func TestBatteryPausedOncePerEpisode(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	since := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	waiting := []models.Job{
		{ID: ids.New(ids.Job), Type: "transcribe", CreatedBy: models.Actor{UserID: a.Cfg.User.ID}},
		{ID: ids.New(ids.Job), Type: "transcribe", CreatedBy: models.Actor{UserID: a.Cfg.User.ID}},
		{ID: ids.New(ids.Job), Type: "evidence", CreatedBy: models.Actor{UserID: "usr_someone_else"}},
	}
	BatteryPaused(ctx, a, "studio-mac", 18, 25, since, waiting)
	BatteryPaused(ctx, a, "studio-mac", 17, 25, since, waiting) // later check, same episode
	got := notifications(t, a, models.NotifyBatteryPaused)
	if len(got) != 2 { // one per person whose work waits
		t.Fatalf("%d notifications, want 2 (one per person)", len(got))
	}
	for _, n := range got {
		if n.UserID == a.Cfg.User.ID && n.Title != "2 transcriptions paused on studio-mac" {
			t.Fatalf("title %q", n.Title)
		}
	}
	// A new episode (unplugged again later) notifies again.
	BatteryPaused(ctx, a, "studio-mac", 20, 25, since.Add(time.Hour), waiting[:1])
	if n := notifications(t, a, models.NotifyBatteryPaused); len(n) != 3 {
		t.Fatalf("%d notifications after a second episode, want 3", len(n))
	}
}

func TestReadySentence(t *testing.T) {
	cases := map[string][]string{
		"You can now watch it, read what was said and see its scenes.": {"thumbnail", "preview copy", "frame grid", "transcript", "scene analysis"},
		"You can now read what was said.":                              {"transcript", "waveform"},
		"It is stored and ready to use.":                               {"thumbnail"},
		"":                                                             nil,
	}
	for want, ready := range cases {
		if got := readySentence(ready); got != want {
			t.Errorf("readySentence(%v) = %q, want %q", ready, got, want)
		}
	}
	// A sound file (an imported narration, music) is listened to and has no scenes.
	if got := readySentenceOf([]string{"preview copy", "transcript", "scene analysis", "waveform"}, true); got != "You can now listen to it and read what was said." {
		t.Errorf("sound file: %q", got)
	}
	if got := readySentenceOf([]string{"scene analysis", "waveform"}, true); got != "It is stored and ready to use." {
		t.Errorf("sound file, analysis only: %q", got)
	}
}

// The quality-check notification says what the speech check heard, in plain words.
func TestQADoneSaysWhatTheSpeechCheckHeard(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	rep := models.QAReport{ID: ids.New(models.QAReportPrefix), WorkspaceID: a.WorkspaceID(), RenderID: ids.New(ids.Render), RevisionNumber: 3, Quality: "review",
		Counts: models.QACounts{Errors: 1, Warnings: 1}, CreatedAt: now,
		Findings: []models.QAFinding{
			{Severity: models.QAWarning, Kind: "loudness_low", Message: "integrated loudness -18 LUFS is 4 LU below the -14 LUFS convention"},
			{Severity: models.QAError, Kind: "speech_repeated", Message: "Around 0:27 a phrase plays twice in the rendered video"},
		}}
	if err := a.DB.Insert(ctx, store.CQAReports, rep); err != nil {
		t.Fatal(err)
	}
	j := &models.Job{ID: ids.New(ids.Job), CreatedBy: models.Actor{UserID: a.Cfg.User.ID}}
	QADone(ctx, a, j, rep.ID)
	ns := notifications(t, a, models.NotifyQADone)
	if len(ns) != 1 || !strings.HasPrefix(ns[0].Body, "Around 0:27 a phrase plays twice in the rendered video. ") {
		t.Fatalf("qa notification: %+v", ns)
	}
	clean := models.QAReport{ID: ids.New(models.QAReportPrefix), WorkspaceID: a.WorkspaceID(), RenderID: ids.New(ids.Render), RevisionNumber: 4, Quality: "final", CreatedAt: now,
		Measurements: models.QAMeasurements{SpeechWordsExpected: 14, SpeechWordsMatched: 14}}
	if err := a.DB.Insert(ctx, store.CQAReports, clean); err != nil {
		t.Fatal(err)
	}
	QADone(ctx, a, j, clean.ID)
	for _, n := range notifications(t, a, models.NotifyQADone) {
		if n.GroupKey == "qa:"+clean.ID && n.Body != "Nothing looks wrong in the finished video, and every expected word was heard." {
			t.Fatalf("clean report: %q", n.Body)
		}
	}
}
