package notify

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/links"
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

// What waits is counted in the job labels' words with their plurals (never
// "2 steady copys" or "3 visuals"); kinds that read the same are one count,
// and kinds MetaRush has no words for are jobs.
func TestBatteryPausedSaysWhatWaitsPlainly(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	since := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	var waiting []models.Job
	add := func(user, typ string, n int) {
		for range n {
			waiting = append(waiting, models.Job{ID: ids.New(ids.Job), Type: typ, CreatedBy: models.Actor{UserID: user}})
		}
	}
	add("usr_maya", "mezzanine", 2)
	add("usr_maya", "evidence", 1)
	add("usr_maya", "visual", 3)
	add("usr_maya", "render", 1)
	add("usr_maya", "render-shape", 1)
	add("usr_maya", "a-kind-from-a-newer-build", 1)
	add("usr_sam", "evidence", 1)
	BatteryPaused(ctx, a, "studio-mac", 18, 25, since, waiting)
	want := map[string][2]string{
		"usr_maya": {"1 job, 3 looks at what's on screen, 2 renders, 1 scene analysis and 2 steady copies paused on studio-mac",
			"Battery at 18% (below 25%). They resume when you plug in, or choose Process now in the activity bar."},
		"usr_sam": {"1 scene analysis paused on studio-mac",
			"Battery at 18% (below 25%). It resumes when you plug in, or choose Process now in the activity bar."},
	}
	got := notifications(t, a, models.NotifyBatteryPaused)
	if len(got) != len(want) {
		t.Fatalf("%d notifications, want %d", len(got), len(want))
	}
	for _, n := range got {
		if w := want[n.UserID]; n.Title != w[0] || n.Body != w[1] {
			t.Errorf("%s: %q / %q, want %q / %q", n.UserID, n.Title, n.Body, w[0], w[1])
		}
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

// noteFor is a person's notification about one outcome (its group key).
func noteFor(t *testing.T, a *app.App, userID, groupKey string) (models.Notification, bool) {
	t.Helper()
	var n models.Notification
	err := a.DB.C(store.CNotifications).FindOne(context.Background(), bson.M{"workspaceId": a.WorkspaceID(), "userId": userID, "groupKey": groupKey}).Decode(&n)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return n, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return n, true
}

// mustNote is noteFor for a notification that must be there.
func mustNote(t *testing.T, a *app.App, userID, groupKey string) models.Notification {
	t.Helper()
	n, ok := noteFor(t, a, userID, groupKey)
	if !ok {
		t.Fatalf("no notification %q for %s", groupKey, userID)
	}
	return n
}

// noNote fails when the person has a notification about the outcome.
func noNote(t *testing.T, a *app.App, userID, groupKey string) {
	t.Helper()
	if n, ok := noteFor(t, a, userID, groupKey); ok {
		t.Fatalf("a notification %q nobody should get: %+v", groupKey, n)
	}
}

// countNotes counts the workspace's notifications that match filter.
func countNotes(t *testing.T, a *app.App, filter bson.M) int64 {
	t.Helper()
	filter["workspaceId"] = a.WorkspaceID()
	c, err := a.DB.C(store.CNotifications).CountDocuments(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// setRead marks a notification read or unread, as the bell does.
func setRead(t *testing.T, a *app.App, id string, read bool) {
	t.Helper()
	upd := bson.M{"$set": bson.M{"read": true, "readAt": time.Now().UTC()}}
	if !read {
		upd = bson.M{"$set": bson.M{"read": false}, "$unset": bson.M{"readAt": ""}}
	}
	if _, err := a.DB.C(store.CNotifications).UpdateOne(context.Background(), bson.M{"_id": id, "workspaceId": a.WorkspaceID()}, upd); err != nil {
		t.Fatal(err)
	}
}

// strs is a list from a notification's details, as read back (a BSON array).
func strs(v any) []string {
	l, _ := v.(bson.A)
	out := make([]string, 0, len(l))
	for _, x := range l {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// newJob is a job a's person started; insertJob stores it for code that reads it back.
func newJob(a *app.App, typ, state string) *models.Job {
	now := time.Now().UTC().Add(-time.Minute)
	return &models.Job{ID: ids.New(ids.Job), WorkspaceID: a.WorkspaceID(), Type: typ, State: state, Payload: map[string]any{}, Attempts: []models.Attempt{},
		IdempotencyKey: ids.New("k"), CreatedBy: models.Actor{UserID: a.Cfg.User.ID}, CreatedAt: now, UpdatedAt: now, NextRunAt: now}
}

func insertJob(t *testing.T, a *app.App, j *models.Job) {
	t.Helper()
	if err := a.DB.Insert(context.Background(), store.CJobs, j); err != nil {
		t.Fatal(err)
	}
}

// addAsset stores a clip: a video a's person added, unless as says otherwise.
// Each clip has its own content (a workspace stores a file once).
func addAsset(t *testing.T, a *app.App, as models.Asset) models.Asset {
	t.Helper()
	now := time.Now().UTC().Add(-time.Minute)
	as.ID, as.WorkspaceID, as.CreatedAt, as.UpdatedAt = ids.New(ids.Asset), a.WorkspaceID(), now, now
	as.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(as.ID)))
	if as.Kind == "" {
		as.Kind = "video"
	}
	if as.CreatedBy.UserID == "" {
		as.CreatedBy = models.Actor{UserID: a.Cfg.User.ID}
	}
	if err := a.DB.Insert(context.Background(), store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	return as
}

func addProject(t *testing.T, a *app.App, name string) models.Project {
	t.Helper()
	now := time.Now().UTC()
	p := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: name, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(context.Background(), store.CProjects, p); err != nil {
		t.Fatal(err)
	}
	return p
}

// anotherConnection is a's workspace through a second connection to its
// database, as another MetaRush process (the daemon, a worker, a command) has
// its own. beforeWrite runs once, just before the connection's first write:
// for Upsert that is after it looked for the notification and before it
// writes it, where two processes race.
func anotherConnection(t *testing.T, a *app.App, beforeWrite func()) *app.App {
	t.Helper()
	db, err := store.Connect(context.Background(), a.Cfg.Database.URI, a.DB.Name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(context.Background()) })
	db.OnFirstWrite(func(context.Context) { beforeWrite() })
	return &app.App{Cfg: a.Cfg, DB: db, Actor: a.Actor}
}

// stderrOf is what f writes to standard error.
func stderrOf(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	func() {
		defer func() { os.Stderr = old }()
		f()
	}()
	_ = w.Close()
	out, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// Upsert keeps one notification per person and outcome: Once keeps the first;
// Refresh changes it in place, unread again, only when what it says changed; a
// process that loses the race to write it writes nothing; a database error is
// returned, never taken for a write.
func TestUpsertKeepsOneNotificationPerOutcome(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	user := a.Cfg.User.ID

	t.Run("no recipient or no outcome: dropped", func(t *testing.T) {
		for _, n := range []models.Notification{
			{Kind: models.NotifyRenderFinished, GroupKey: "render:rnd_nobody", Title: "Final render of version 2 finished"},
			{Kind: models.NotifyRenderFinished, UserID: user, Title: "Final render of version 2 finished"},
		} {
			if wrote, err := Upsert(ctx, a, n, Refresh); wrote || err != nil {
				t.Fatalf("Upsert(%+v) = %v, %v; want it dropped", n, wrote, err)
			}
		}
		if c := countNotes(t, a, bson.M{"title": "Final render of version 2 finished"}); c != 0 {
			t.Fatalf("%d notifications stored without a recipient or a group key", c)
		}
	})

	t.Run("once keeps the first", func(t *testing.T) {
		first := models.Notification{Kind: models.NotifyDuplicateSkipped, UserID: user, GroupKey: "duplicate:ast_garage:IMG_2391 copy.MOV",
			Title: "IMG_2391 copy.MOV was already in MetaRush", Body: "It is identical to “Garage interview” (in the inbox), so nothing was stored twice."}
		if wrote, err := Upsert(ctx, a, first, Once); !wrote || err != nil {
			t.Fatalf("first write = %v, %v", wrote, err)
		}
		n := mustNote(t, a, user, first.GroupKey)
		// What the caller left out: the info tone, this computer, the workspace; new and unread.
		if n.Tone != "info" || n.Machine != a.Cfg.Installation.Name || n.WorkspaceID != a.WorkspaceID() || n.Read || n.ReadAt != nil ||
			!strings.HasPrefix(n.ID, models.NotificationPrefix+"_") || n.CreatedAt.IsZero() || !n.UpdatedAt.Equal(n.CreatedAt) {
			t.Fatalf("first notification %+v", n)
		}
		setRead(t, a, n.ID, true)
		again := first
		again.Title, again.Tone = "IMG_2391 copy.MOV was skipped again", "warning"
		if wrote, err := Upsert(ctx, a, again, Once); wrote || err != nil {
			t.Fatalf("second write = %v, %v; want nothing written", wrote, err)
		}
		if got := mustNote(t, a, user, first.GroupKey); got.ID != n.ID || got.Title != first.Title || got.Tone != "info" || !got.Read {
			t.Fatalf("Once changed the first notification: %+v", got)
		}
	})

	t.Run("refresh changes it only when its words or tone change", func(t *testing.T) {
		n := models.Notification{Kind: models.NotifyRenderFailed, UserID: user, GroupKey: "render:rnd_refresh", Tone: "error",
			Title: "Review copy of version 3 failed", Body: "Garage interview. Open it to see what went wrong and try again.",
			Link: "/renders/rnd_refresh", Details: map[string]any{"error": "exit status 1"}}
		if wrote, err := Upsert(ctx, a, n, Refresh); !wrote || err != nil {
			t.Fatalf("first write = %v, %v", wrote, err)
		}
		first := mustNote(t, a, user, n.GroupKey)
		setRead(t, a, first.ID, true)

		// The same words with another link and details: left as it was, still read.
		same := n
		same.Link, same.Details = "/p/prj_garage?render=rnd_refresh&tab=renders", map[string]any{"error": "exit status 2"}
		if wrote, err := Upsert(ctx, a, same, Refresh); wrote || err != nil {
			t.Fatalf("the same words = %v, %v; want nothing written", wrote, err)
		}
		if got := mustNote(t, a, user, n.GroupKey); !got.Read || got.ReadAt == nil || got.Link != n.Link || got.Details["error"] != "exit status 1" {
			t.Fatalf("the same words changed the notification: %+v", got)
		}

		// A new title, body or tone: changed in place, link and details too, and unread again.
		cur := n
		for _, c := range []struct {
			what string
			edit func(*models.Notification)
		}{
			{"title", func(n *models.Notification) { n.Title = "Review copy of version 3 finished" }},
			{"body", func(n *models.Notification) { n.Body = "Garage interview, vertical" }},
			{"tone", func(n *models.Notification) { n.Tone = "success" }},
		} {
			setRead(t, a, first.ID, true)
			c.edit(&cur)
			cur.Link, cur.Details = "/p/prj_garage?render=rnd_refresh&tab=renders", map[string]any{"changed": c.what}
			if wrote, err := Upsert(ctx, a, cur, Refresh); !wrote || err != nil {
				t.Fatalf("a new %s = %v, %v; want it written", c.what, wrote, err)
			}
			got := mustNote(t, a, user, n.GroupKey)
			if got.ID != first.ID || !got.CreatedAt.Equal(first.CreatedAt) || got.Read || got.ReadAt != nil ||
				got.Title != cur.Title || got.Body != cur.Body || got.Tone != cur.Tone || got.Link != cur.Link || got.Details["changed"] != c.what {
				t.Fatalf("after a new %s: %+v", c.what, got)
			}
		}
		if c := countNotes(t, a, bson.M{"groupKey": n.GroupKey}); c != 1 {
			t.Fatalf("%d notifications for one outcome", c)
		}
	})

	t.Run("another process writes the same outcome first", func(t *testing.T) {
		n := models.Notification{Kind: models.NotifyRenderFinished, UserID: user, GroupKey: "render:rnd_race", Tone: "success",
			Title: "Final render of version 4 finished", Body: "Garage interview, vertical"}
		theirs := n
		theirs.Machine = "studio-mac"
		raced := false
		mine := anotherConnection(t, a, func() {
			raced = true
			if wrote, err := Upsert(ctx, a, theirs, Once); !wrote || err != nil {
				t.Errorf("the other process's write = %v, %v", wrote, err)
			}
		})
		wrote, err := Upsert(ctx, mine, n, Refresh)
		if !raced {
			t.Fatal("the other process did not write between this one's look and its write")
		}
		if wrote || err != nil {
			t.Fatalf("losing the race = %v, %v; want nothing written and no error", wrote, err)
		}
		if got := mustNote(t, a, user, n.GroupKey); got.Machine != "studio-mac" || countNotes(t, a, bson.M{"groupKey": n.GroupKey}) != 1 {
			t.Fatalf("after the race: %+v", got)
		}
	})

	t.Run("a database error is returned, not taken for a write", func(t *testing.T) {
		n := models.Notification{Kind: models.NotifyRenderFailed, UserID: user, GroupKey: "render:rnd_stopped", Tone: "error",
			Title: "Final render of version 5 failed", Body: "Garage interview. Open it to see what went wrong and try again."}
		// The look fails: the call was stopped before it began.
		stopped, stop := context.WithCancel(ctx)
		stop()
		if wrote, err := Upsert(stopped, a, n, Refresh); wrote || !errors.Is(err, context.Canceled) {
			t.Fatalf("a stopped look = %v, %v", wrote, err)
		}
		// The write fails: the call is stopped after the look.
		call, stopCall := context.WithCancel(ctx)
		defer stopCall()
		if wrote, err := Upsert(call, anotherConnection(t, a, stopCall), n, Refresh); wrote || err == nil || store.IsDuplicateKey(err) {
			t.Fatalf("a failed write = %v, %v", wrote, err)
		}
		noNote(t, a, user, n.GroupKey)

		// A change whose write fails leaves the notification as it was.
		if wrote, err := Upsert(ctx, a, n, Refresh); !wrote || err != nil {
			t.Fatalf("write = %v, %v", wrote, err)
		}
		setRead(t, a, mustNote(t, a, user, n.GroupKey).ID, true)
		change, stopChange := context.WithCancel(ctx)
		defer stopChange()
		changed := n
		changed.Tone, changed.Title, changed.Body = "success", "Final render of version 5 finished", "Garage interview, vertical"
		if wrote, err := Upsert(change, anotherConnection(t, a, stopChange), changed, Refresh); wrote || err == nil {
			t.Fatalf("a failed change = %v, %v", wrote, err)
		}
		if got := mustNote(t, a, user, n.GroupKey); got.Title != n.Title || got.Tone != "error" || !got.Read {
			t.Fatalf("a failed change changed the notification: %+v", got)
		}
	})

	t.Run("warn speaks only when a notification was not written", func(t *testing.T) {
		if out := stderrOf(t, func() { warn(a, "render", nil) }); strings.Contains(out, "notification") {
			t.Fatalf("a warning without an error: %q", out)
		}
		out := stderrOf(t, func() { warn(a, "render", errors.New("connection reset by peer")) })
		if !strings.Contains(out, "warning: notification (render) not written: connection reset by peer\n") {
			t.Fatalf("warning %q", out)
		}
	})
}

// AssetSettled writes one "file ready" note once every step of a new file has
// ended, saying what is ready and what failed or was stopped, and nothing new
// while that stays the same; files whose own work tells the person get none.
func TestAssetSettledSaysWhatIsReadyAndWhatFailed(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	user := a.Cfg.User.ID
	now := time.Now().UTC().Add(-time.Minute)

	t.Run("no clip, no steps or no record: nothing", func(t *testing.T) {
		AssetSettled(ctx, a, "")
		bare := addAsset(t, a, models.Asset{Name: "IMG_0001.MOV"})
		addJob(t, a, bare.ID, "diarize", models.JobSucceeded, "", now) // not a step of a new file
		AssetSettled(ctx, a, bare.ID)
		gone := ids.New(ids.Asset)
		addJob(t, a, gone, "thumbnail", models.JobSucceeded, "", now)
		AssetSettled(ctx, a, gone)
		if c := countNotes(t, a, bson.M{"kind": models.NotifyFileReady}); c != 0 {
			t.Fatalf("%d file-ready notes", c)
		}
	})

	t.Run("a step failed and one was stopped: one note, then nothing new", func(t *testing.T) {
		prj := ids.New(ids.Project)
		as := addAsset(t, a, models.Asset{Name: "IMG_0042.MOV", Title: "Kitchen walkthrough", ProjectIDs: []string{prj}})
		for _, typ := range []string{"thumbnail", "proxy", "contact_sheet", "waveform"} {
			addJob(t, a, as.ID, typ, models.JobSucceeded, "", now)
		}
		addJob(t, a, as.ID, "mezzanine", models.JobCanceled, "", now)
		addJob(t, a, as.ID, "evidence", models.JobFailed, "ffmpeg: scene detection failed: exit status 1\nffmpeg version 7.1", now)
		transcript := addJob(t, a, as.ID, "transcribe", models.JobQueued, "", now)
		key := "file_ready:" + as.ID
		AssetSettled(ctx, a, as.ID)
		noNote(t, a, user, key) // the transcript is still to come

		if _, err := a.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": transcript}, bson.M{"$set": bson.M{"state": models.JobSucceeded}}); err != nil {
			t.Fatal(err)
		}
		AssetSettled(ctx, a, as.ID)
		n := mustNote(t, a, user, key)
		if n.Kind != models.NotifyFileReady || n.Tone != "error" || n.Read || n.Title != "Kitchen walkthrough is ready, but the scene analysis failed" {
			t.Fatalf("note %+v", n)
		}
		if want := "You can now watch it and read what was said. You can retry the scene analysis from the activity bar. The steady copy was stopped."; n.Body != want {
			t.Fatalf("body %q, want %q", n.Body, want)
		}
		if n.Link != links.Asset(prj, as.ID) || n.ProjectID != prj || n.AssetID != as.ID {
			t.Fatalf("link %q, project %q, clip %q", n.Link, n.ProjectID, n.AssetID)
		}
		if r := strs(n.Details["ready"]); !slices.Equal(r, []string{"thumbnail", "preview copy", "frame grid", "waveform", "transcript"}) {
			t.Fatalf("ready %v", r)
		}
		if f, s := strs(n.Details["failed"]), strs(n.Details["canceled"]); !slices.Equal(f, []string{"scene analysis"}) || !slices.Equal(s, []string{"steady copy"}) {
			t.Fatalf("failed %v, stopped %v", f, s)
		}
		if n.Details["error"] != "ffmpeg: scene detection failed: exit status 1" || n.Details["name"] != "IMG_0042.MOV" {
			t.Fatalf("details %v", n.Details)
		}
		// Every step that ends calls it; the same outcome writes nothing new.
		AssetSettled(ctx, a, as.ID)
		if again := mustNote(t, a, user, key); again.ID != n.ID || !again.UpdatedAt.Equal(n.UpdatedAt) || countNotes(t, a, bson.M{"groupKey": key}) != 1 {
			t.Fatalf("the same outcome again: %+v", again)
		}
	})

	t.Run("narration, a clearer copy or a generated shot: their own work tells", func(t *testing.T) {
		for _, as := range []models.Asset{
			{Name: "narration-line-3.wav", Kind: "audio", Tags: []string{"voiceover", "scratch"}},
			{Name: "IMG_0042 clearer.wav", Kind: "audio", Tags: []string{models.TagEnhanced}},
			{Name: "harbour-at-dawn.mp4", Source: &models.AssetSource{Platform: models.PlatformGenerated}},
		} {
			as = addAsset(t, a, as)
			addJob(t, a, as.ID, "proxy", models.JobSucceeded, "", now)
			AssetSettled(ctx, a, as.ID)
			noNote(t, a, user, "file_ready:"+as.ID)
		}
	})

	t.Run("steps no person started: the person who added the file hears", func(t *testing.T) {
		addedBy := ids.New(ids.User)
		as := addAsset(t, a, models.Asset{Name: "voice-memo.m4a", Title: "Voice memo", Kind: "audio", CreatedBy: models.Actor{UserID: addedBy}})
		for _, typ := range []string{"proxy", "waveform", "transcribe"} {
			j := newJob(a, typ, models.JobSucceeded)
			j.AssetID, j.CreatedBy = as.ID, models.Actor{}
			insertJob(t, a, j)
		}
		AssetSettled(ctx, a, as.ID)
		n := mustNote(t, a, addedBy, "file_ready:"+as.ID)
		if n.Tone != "success" || n.Title != "Voice memo is ready" || n.Body != "You can now listen to it and read what was said." || n.Link != links.Asset("", as.ID) {
			t.Fatalf("note %+v", n)
		}
	})
}

// A narration note says what is ready (and where it is in the cut) or, when it
// could not be made, what to do next; stopped work says nothing.
func TestNarrationOutcomeSaysWhatIsReadyOrWhatToDo(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	user := a.Cfg.User.ID
	prj := ids.New(ids.Project)
	narration := func(action, state string, result map[string]any) *models.Job {
		j := newJob(a, "voiceover", state)
		j.ProjectID, j.Payload["action"], j.Result = prj, action, result
		return j
	}

	t.Run("ready", func(t *testing.T) {
		for _, c := range []struct {
			action string
			lines  any // as a result holds it: from Go, the database or JSON
			title  string
		}{
			{"scratch", 3, "Scratch narration ready for 3 lines"},
			{"tts", int64(1), "AI voice ready for 1 line"},
			{"cleanup", float64(2), "Narration cleaned up: 2 lines"},
			{"align", int32(4), "Your recording is placed: 4 lines"},
			{"place", 1, "Your take is placed: 1 line"},
			{"enhance", nil, "Clearer voices ready"},
			{"sound-setup", nil, "Clearer voices are set up on this computer"},
			{"", nil, "Narration ready"},
		} {
			j := narration(c.action, models.JobSucceeded, map[string]any{"lines": c.lines})
			NarrationOutcome(ctx, a, j)
			n := mustNote(t, a, user, "narration:"+j.ID)
			if n.Kind != models.NotifyNarrationReady || n.Tone != "success" || n.Title != c.title || n.Read || n.JobID != j.ID ||
				n.Body != "Listen in the preview, then apply the change to add it to the cut." || n.Link != links.Project(prj) {
				t.Errorf("%q: %+v", c.action, n)
			}
		}
	})

	t.Run("where it links, and what it says once it is in the cut", func(t *testing.T) {
		rev, applied := ids.New(ids.Revision), ids.New(ids.Revision)
		onVersion := narration("tts", models.JobSucceeded, map[string]any{"lines": 2})
		onVersion.Payload["revisionId"] = rev
		inCut := narration("tts", models.JobSucceeded, map[string]any{"lines": 2, "appliedRevisionId": applied, "appliedNumber": 7})
		inCutNoNumber := narration("scratch", models.JobSucceeded, map[string]any{"lines": 2, "appliedRevisionId": applied})
		voiceCopy := narration("clone", models.JobSucceeded, nil)
		noProject := newJob(a, "voiceover", models.JobSucceeded)
		noProject.Payload["action"], noProject.Result = "scratch", map[string]any{"lines": 1}
		for _, c := range []struct {
			j                 *models.Job
			title, body, link string
		}{
			{onVersion, "AI voice ready for 2 lines", "Listen in the preview, then apply the change to add it to the cut.", links.Revision(prj, rev)},
			{inCut, "AI voice ready for 2 lines", "It is in the cut now as version 7.", links.Revision(prj, applied)},
			{inCutNoNumber, "Scratch narration ready for 2 lines", "It is in the cut now.", links.Revision(prj, applied)},
			{voiceCopy, "Your voice copy is ready", "Choose it as the narration voice in Settings, Integrations.", links.Project(prj)},
			{noProject, "Scratch narration ready for 1 line", "Listen in the preview, then apply the change to add it to the cut.", links.Job(noProject.ID)},
		} {
			NarrationOutcome(ctx, a, c.j)
			if n := mustNote(t, a, user, "narration:"+c.j.ID); n.Title != c.title || n.Body != c.body || n.Link != c.link {
				t.Errorf("%v %v: %q / %q / %q, want %q / %q / %q", c.j.Payload, c.j.Result, n.Title, n.Body, n.Link, c.title, c.body, c.link)
			}
		}
		if n := mustNote(t, a, user, "narration:"+inCut.ID); n.Details["revisionId"] != applied {
			t.Errorf("details %v", n.Details)
		}
	})

	t.Run("could not", func(t *testing.T) {
		old := JobAdvice
		t.Cleanup(func() { JobAdvice = old })
		JobAdvice = nil
		for _, c := range []struct{ action, title string }{
			{"scratch", "Could not make the scratch narration"},
			{"tts", "Could not make the AI voice"},
			{"cleanup", "Could not clean up the narration"},
			{"align", "Could not place your recording"},
			{"place", "Could not place your take"},
			{"clone", "Could not make your voice copy"},
			{"enhance", "Could not make the voices clearer"},
			{"sound-setup", "Could not set up clearer voices"},
			{"", "Narration could not be made"},
		} {
			j := narration(c.action, models.JobFailed, nil)
			j.Error = "voice service answered 503\nretry-after: 30"
			NarrationOutcome(ctx, a, j)
			n := mustNote(t, a, user, "narration:"+j.ID)
			if n.Kind != models.NotifyNarrationFailed || n.Tone != "error" || n.Title != c.title ||
				n.Body != "Open it to see what went wrong, then try again." || n.Details["error"] != "voice service answered 503" {
				t.Errorf("%q: %+v", c.action, n)
			}
			if _, ok := n.Details["code"]; ok {
				t.Errorf("%q: a code where the job had none: %v", c.action, n.Details)
			}
		}

		// The advice job details give (JobAdvice, which the ops package sets) is the sentence.
		const overBudget = "This project reached its narration spending limit. Raise the limit in Settings, Integrations, shorten the script, or use the free scratch voice."
		JobAdvice = func(j *models.Job) string {
			if code, _ := j.Payload["errorCode"].(string); code == "E_BUDGET" {
				return overBudget
			}
			return ""
		}
		over := narration("tts", models.JobFailed, nil)
		over.Error, over.Payload["errorCode"] = "narration budget reached: 12.40 of 12.00", "E_BUDGET"
		other := narration("tts", models.JobFailed, nil)
		other.Error = "exit status 1"
		NarrationOutcome(ctx, a, over)
		NarrationOutcome(ctx, a, other)
		if n := mustNote(t, a, user, "narration:"+over.ID); n.Body != overBudget || n.Details["code"] != "E_BUDGET" || n.Details["error"] != "narration budget reached: 12.40 of 12.00" {
			t.Errorf("with advice: %+v", n)
		}
		if n := mustNote(t, a, user, "narration:"+other.ID); n.Body != "Open it to see what went wrong, then try again." {
			t.Errorf("with no advice for it: %q", n.Body)
		}
	})

	t.Run("stopped or running work, another kind of job or none: nothing", func(t *testing.T) {
		stopped := narration("tts", models.JobCanceled, nil)
		running := narration("tts", models.JobRunning, nil)
		other := newJob(a, "import", models.JobFailed)
		for _, j := range []*models.Job{stopped, running, other, nil} {
			NarrationOutcome(ctx, a, j)
		}
		for _, j := range []*models.Job{stopped, running, other} {
			noNote(t, a, user, "narration:"+j.ID)
		}
	})
}

// An import that could not be done says why and what to do, in the import's
// own words when it recorded advice; a finished import needs no note.
func TestImportFailedSaysWhyAndWhatToDo(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	user := a.Cfg.User.ID
	failed := func(payload map[string]any, errText string) *models.Job {
		j := newJob(a, "import", models.JobFailed)
		j.Payload, j.Error = payload, errText
		return j
	}

	t.Run("with the import's advice", func(t *testing.T) {
		const message = "YouTube wants to check that you're not a bot before it lets MetaRush download this video."
		const next = "Choose “Use my browser's sign-in” to download it with your browser's sign-in, or save it with the MetaRush browser extension."
		prj := ids.New(ids.Project)
		j := failed(map[string]any{"platform": "youtube", "title": "Cooking with Maya", "url": "https://www.youtube.com/watch?v=abc123",
			"advice": map[string]any{"code": "signin_required", "message": message, "next": next, "signIn": true}},
			"ERROR: [youtube] abc123: Sign in to confirm you're not a bot\nUse --cookies-from-browser")
		j.ProjectID = prj
		ImportFailed(ctx, a, j)
		n := mustNote(t, a, user, "import:"+j.ID)
		if n.Kind != models.NotifyImportFailed || n.Tone != "error" || n.Read || n.Title != "Could not import “Cooking with Maya” from YouTube" ||
			n.Body != message+" "+next || n.Link != "/p/"+prj+"?tab=sources&activity=open" || n.JobID != j.ID || n.ProjectID != prj {
			t.Fatalf("note %+v", n)
		}
		if n.Details["code"] != "signin_required" || n.Details["signIn"] != true || n.Details["url"] != "https://www.youtube.com/watch?v=abc123" ||
			n.Details["error"] != "ERROR: [youtube] abc123: Sign in to confirm you're not a bot" {
			t.Fatalf("details %v", n.Details)
		}
	})

	t.Run("advice as read back from the database, and each site by name", func(t *testing.T) {
		x := failed(map[string]any{"platform": "x", "title": "Launch day", "advice": bson.D{{Key: "code", Value: "private"},
			{Key: "message", Value: "This video is private."}, {Key: "next", Value: "If it was shared with you, import it with your browser's sign-in."}}},
			"ERROR: protected post")
		tiktok := failed(map[string]any{"platform": "tiktok"}, "ERROR: [TikTok] 7300: Video unavailable")
		ImportFailed(ctx, a, x)
		ImportFailed(ctx, a, tiktok)
		if n := mustNote(t, a, user, "import:"+x.ID); n.Title != "Could not import “Launch day” from X" ||
			n.Body != "This video is private. If it was shared with you, import it with your browser's sign-in." || n.Details["code"] != "private" || n.Details["signIn"] != nil {
			t.Fatalf("from X: %+v", n)
		}
		if n := mustNote(t, a, user, "import:"+tiktok.ID); n.Title != "Could not import the video from TikTok" {
			t.Fatalf("from TikTok: %+v", n)
		}
	})

	t.Run("no advice, title or project", func(t *testing.T) {
		j := failed(map[string]any{"url": "https://example.com/clip"}, "exit status 1")
		ImportFailed(ctx, a, j)
		n := mustNote(t, a, user, "import:"+j.ID)
		if n.Title != "Could not import the video from the web" || n.Body != "Something went wrong while importing it. Choose Retry in the activity bar to try again." ||
			n.Link != "/?activity=open" || n.Details["error"] != "exit status 1" || n.Details["url"] != "https://example.com/clip" {
			t.Fatalf("note %+v", n)
		}
		if _, ok := n.Details["code"]; ok {
			t.Fatalf("a code without advice: %v", n.Details)
		}
	})

	t.Run("a finished import, another job or none: nothing", func(t *testing.T) {
		done := newJob(a, "import", models.JobSucceeded)
		narration := newJob(a, "voiceover", models.JobFailed)
		for _, j := range []*models.Job{done, narration, nil} {
			ImportFailed(ctx, a, j)
		}
		noNote(t, a, user, "import:"+done.ID)
		noNote(t, a, user, "import:"+narration.ID)
	})
}

// A render's note says it finished or failed; a quality check's says what it
// found; a check of a copy MetaRush made by itself rings only for problems.
func TestRenderAndQualityCheckNotes(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	user := a.Cfg.User.ID
	p := addProject(t, a, "Garage interview")
	now := time.Now().UTC()
	addRender := func(rd models.Render) models.Render {
		t.Helper()
		rd.ID, rd.WorkspaceID, rd.ProjectID, rd.CreatedBy, rd.CreatedAt = ids.New(ids.Render), a.WorkspaceID(), p.ID, models.Actor{UserID: user}, now
		if err := a.DB.Insert(ctx, store.CRenders, rd); err != nil {
			t.Fatal(err)
		}
		return rd
	}
	addReport := func(rep models.QAReport) models.QAReport {
		t.Helper()
		rep.ID, rep.WorkspaceID, rep.ProjectID, rep.RenderID, rep.CreatedAt = ids.New(models.QAReportPrefix), a.WorkspaceID(), p.ID, ids.New(ids.Render), now
		if err := a.DB.Insert(ctx, store.CQAReports, rep); err != nil {
			t.Fatal(err)
		}
		return rep
	}
	qa := newJob(a, "qa", models.JobSucceeded)

	t.Run("a render finished", func(t *testing.T) {
		rd := addRender(models.Render{State: models.RenderAvailable, Quality: "final", RevisionNumber: 4, Preset: editspec.PresetVertical, RenderedByName: "studio-mac"})
		RenderOutcome(ctx, a, rd.ID)
		n := mustNote(t, a, user, "render:"+rd.ID)
		if n.Kind != models.NotifyRenderFinished || n.Tone != "success" || n.Title != "Final render of version 4 finished" || n.Body != "Garage interview, vertical" ||
			n.Machine != "studio-mac" || n.Link != links.Render(p.ID, rd.ID) || n.RenderID != rd.ID || n.ProjectID != p.ID || n.Details["project"] != "Garage interview" {
			t.Fatalf("note %+v", n)
		}
	})

	t.Run("a render failed", func(t *testing.T) {
		rd := addRender(models.Render{State: models.RenderFailed, Quality: "review", RevisionNumber: 5, Preset: editspec.PresetLandscape,
			Error: "no frame for 45 s: the renderer stopped\nat chromium"})
		RenderOutcome(ctx, a, rd.ID)
		n := mustNote(t, a, user, "render:"+rd.ID)
		if n.Kind != models.NotifyRenderFailed || n.Tone != "error" || n.Title != "Review copy of version 5 failed" ||
			n.Body != "Garage interview. Open it to see what went wrong and try again." || n.Details["error"] != "no frame for 45 s: the renderer stopped" ||
			n.Machine != a.Cfg.Installation.Name || n.Link != links.Render(p.ID, rd.ID) {
			t.Fatalf("note %+v", n)
		}
	})

	t.Run("a render still going, or none: nothing", func(t *testing.T) {
		going := addRender(models.Render{State: models.RenderRendering, Quality: "final", RevisionNumber: 6, Preset: editspec.PresetSquare})
		RenderOutcome(ctx, a, going.ID)
		missing := ids.New(ids.Render)
		RenderOutcome(ctx, a, missing)
		noNote(t, a, user, "render:"+going.ID)
		noNote(t, a, user, "render:"+missing)
	})

	t.Run("a check with things worth checking", func(t *testing.T) {
		rep := addReport(models.QAReport{Quality: "final", RevisionNumber: 4, Counts: models.QACounts{Warnings: 2},
			Findings: []models.QAFinding{{Severity: models.QAWarning, Kind: "loudness_low", Message: "integrated loudness -18 LUFS is 4 LU below the -14 LUFS convention"}}})
		QADone(ctx, a, qa, rep.ID)
		n := mustNote(t, a, user, "qa:"+rep.ID)
		if n.Kind != models.NotifyQADone || n.Tone != "warning" || n.Title != "Quality check of the final render of version 4: 2 things worth checking" ||
			n.Body != "It lists what to look at, with the moment in the video for each." || n.Link != links.QA(p.ID, rep.RenderID, rep.ID) || n.JobID != qa.ID {
			t.Fatalf("note %+v", n)
		}
	})

	t.Run("a clean check", func(t *testing.T) {
		rep := addReport(models.QAReport{Quality: "review", RevisionNumber: 5})
		QADone(ctx, a, qa, rep.ID)
		if n := mustNote(t, a, user, "qa:"+rep.ID); n.Tone != "success" || n.Title != "Quality check of the review copy of version 5 passed" ||
			n.Body != "Nothing looks wrong in the finished video." {
			t.Fatalf("note %+v", n)
		}
	})

	t.Run("a copy MetaRush made by itself: only problems ring", func(t *testing.T) {
		clean := addReport(models.QAReport{Quality: "review", RevisionNumber: 6})
		bad := addReport(models.QAReport{Quality: "review", RevisionNumber: 6, Counts: models.QACounts{Errors: 1, Warnings: 1}})
		missing := ids.New(models.QAReportPrefix)
		for _, id := range []string{clean.ID, bad.ID, missing} {
			QAProblems(ctx, a, qa, id)
		}
		QADone(ctx, a, qa, missing)
		noNote(t, a, user, "qa:"+clean.ID)
		noNote(t, a, user, "qa:"+missing)
		if n := mustNote(t, a, user, "qa:"+bad.ID); n.Tone != "error" || n.Title != "Quality check of the review copy of version 6: 1 problem, 1 thing worth checking" {
			t.Fatalf("note %+v", n)
		}
	})
}

// Speaker detection someone asked for says how many voices it found and what
// is left to name; looking at a clip someone asked for (what's on screen, the
// scenes described) says what it found; automatic runs stay quiet.
func TestSpeakersAndLookingAtAClipNotes(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	user := a.Cfg.User.ID
	prj := ids.New(ids.Project)
	kitchen := func(speakers *models.SpeakersStatus) models.Asset {
		return addAsset(t, a, models.Asset{Name: "IMG_0042.MOV", Title: "Kitchen walkthrough", ProjectIDs: []string{prj}, Speakers: speakers})
	}
	jobOn := func(typ, state string, as models.Asset) *models.Job {
		j := newJob(a, typ, state)
		j.AssetID = as.ID
		return j
	}

	t.Run("voices found", func(t *testing.T) {
		for _, c := range []struct {
			speakers    *models.SpeakersStatus
			title, body string
		}{
			{nil, "No voices found in “Kitchen walkthrough”", "Nobody seems to speak in it."},
			{&models.SpeakersStatus{State: "available", Count: 1}, "Found 1 voice in “Kitchen walkthrough”", "Listen to each voice and give it a name."},
			{&models.SpeakersStatus{State: "available", Count: 2, Named: 2}, "Found 2 voices in “Kitchen walkthrough”", "Every voice has a name."},
			{&models.SpeakersStatus{State: "available", Count: 3, Named: 2}, "Found 3 voices in “Kitchen walkthrough”", "One voice has no name yet. Listen and name it."},
			{&models.SpeakersStatus{State: "available", Count: 4, Named: 1}, "Found 4 voices in “Kitchen walkthrough”", "3 voices have no name yet. Listen and name them."},
		} {
			as := kitchen(c.speakers)
			j := jobOn("diarize", models.JobSucceeded, as)
			SpeakersDone(ctx, a, j, models.JobSucceeded)
			n := mustNote(t, a, user, "speakers:"+j.ID)
			if n.Kind != models.NotifySpeakers || n.Tone != "success" || n.Title != c.title || n.Body != c.body ||
				n.Link != links.AssetSpeakers(prj, as.ID) || n.AssetID != as.ID || n.ProjectID != prj || n.JobID != j.ID {
				t.Errorf("%+v: %+v", c.speakers, n)
			}
		}
	})

	t.Run("voices could not be told apart", func(t *testing.T) {
		as := addAsset(t, a, models.Asset{Name: "voice-memo.m4a", Kind: "audio"}) // in the inbox, named after its file
		j := jobOn("diarize", models.JobFailed, as)
		j.Error = "diarization model missing\nlooked in models/"
		SpeakersDone(ctx, a, j, models.JobFailed)
		n := mustNote(t, a, user, "speakers:"+j.ID)
		if n.Tone != "error" || n.Title != "Could not tell the voices apart in “voice-memo”" || n.Body != "Open the clip to try again." ||
			n.Details["error"] != "diarization model missing" || n.Link != links.AssetSpeakers("", as.ID) {
			t.Fatalf("note %+v", n)
		}
	})

	t.Run("speakers with no clip, still running or a clip that is gone: nothing", func(t *testing.T) {
		noClip := newJob(a, "diarize", models.JobSucceeded)
		running := jobOn("diarize", models.JobRunning, kitchen(nil))
		gone := newJob(a, "diarize", models.JobSucceeded)
		gone.AssetID = ids.New(ids.Asset)
		SpeakersDone(ctx, a, noClip, models.JobSucceeded)
		SpeakersDone(ctx, a, running, models.JobRunning)
		SpeakersDone(ctx, a, gone, models.JobSucceeded)
		for _, j := range []*models.Job{noClip, running, gone} {
			noNote(t, a, user, "speakers:"+j.ID)
		}
	})

	t.Run("looked at what's on screen, or described the scenes", func(t *testing.T) {
		as := kitchen(nil)
		asked := func(typ, state string, result map[string]any) *models.Job {
			j := jobOn(typ, state, as)
			j.Payload["requested"], j.Result = true, result
			return j
		}
		for _, c := range []struct {
			j                 *models.Job
			kind, title, body string
		}{
			{asked("visual", models.JobSucceeded, map[string]any{"summary": "a kitchen, two people and a whiteboard with a floor plan"}), models.NotifyOnScreen,
				"Looked at what's on screen in “Kitchen walkthrough”", "A kitchen, two people and a whiteboard with a floor plan."},
			{asked("visual", models.JobSucceeded, nil), models.NotifyOnScreen, "Looked at what's on screen in “Kitchen walkthrough”", ""},
			{asked("describe", models.JobSucceeded, map[string]any{"scenes": 4}), models.NotifyDescribed,
				"Described the scenes in “Kitchen walkthrough”", "Claude Code described 4 scenes; they are searchable now."},
			{asked("describe", models.JobSucceeded, nil), models.NotifyDescribed,
				"Described the scenes in “Kitchen walkthrough”", "Claude Code wrote a sentence or two per scene; they are searchable now."},
		} {
			ClipLooked(ctx, a, c.j, models.JobSucceeded)
			n := mustNote(t, a, user, c.kind+":"+c.j.ID)
			if n.Kind != c.kind || n.Tone != "success" || n.Title != c.title || n.Body != c.body || n.Link != links.AssetOnScreen(prj, as.ID) || n.JobID != c.j.ID {
				t.Errorf("%s %v: %+v", c.j.Type, c.j.Result, n)
			}
		}
		for _, c := range []struct{ typ, kind, title string }{
			{"visual", models.NotifyOnScreen, "Could not look at what's on screen in “Kitchen walkthrough”"},
			{"describe", models.NotifyDescribed, "Could not look at the scenes in “Kitchen walkthrough”"},
		} {
			j := asked(c.typ, models.JobFailed, nil)
			j.Error = "vision request failed: timeout\nafter 3 tries"
			ClipLooked(ctx, a, j, models.JobFailed)
			n := mustNote(t, a, user, c.kind+":"+j.ID)
			if n.Tone != "error" || n.Title != c.title || n.Body != "Open the clip's details to see why, then try again." || n.Details["error"] != "vision request failed: timeout" {
				t.Errorf("%s failed: %+v", c.typ, n)
			}
		}
	})

	t.Run("looking that nobody asked to hear about, or at no clip: nothing", func(t *testing.T) {
		as := kitchen(nil)
		automatic := jobOn("visual", models.JobSucceeded, as)
		nobody := jobOn("visual", models.JobSucceeded, as)
		nobody.Payload["requested"], nobody.CreatedBy = true, models.Actor{}
		noClip := newJob(a, "visual", models.JobSucceeded)
		noClip.Payload["requested"] = true
		running := jobOn("describe", models.JobRunning, as)
		running.Payload["requested"] = true
		gone := newJob(a, "describe", models.JobSucceeded)
		gone.AssetID, gone.Payload["requested"] = ids.New(ids.Asset), true
		ClipLooked(ctx, a, automatic, models.JobSucceeded)
		ClipLooked(ctx, a, nobody, models.JobSucceeded)
		ClipLooked(ctx, a, noClip, models.JobSucceeded)
		ClipLooked(ctx, a, running, models.JobRunning)
		ClipLooked(ctx, a, gone, models.JobSucceeded)
		if c := countNotes(t, a, bson.M{"jobId": bson.M{"$in": bson.A{automatic.ID, nobody.ID, noClip.ID, running.ID, gone.ID}}}); c != 0 {
			t.Fatalf("%d notes nobody asked for", c)
		}
	})
}

// A voice or face recognized automatically asks for a check: one note per
// person, about one clip or several; a new clip marks it unread again, a check
// only updates it, and once nothing is left to check it is read.
func TestRecognizedVoicesAndFacesAskForACheck(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	user := a.Cfg.User.ID
	now := time.Now().UTC()
	prj := ids.New(ids.Project)
	person := func(name, mergedInto string) models.Person {
		t.Helper()
		p := models.Person{ID: ids.New(ids.Person), WorkspaceID: a.WorkspaceID(), Name: name, NameKey: strings.ToLower(name), MergedInto: mergedInto,
			CreatedAt: now, CreatedBy: models.Actor{UserID: user}}
		if err := a.DB.Insert(ctx, store.CPeople, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	voice := func(assetID string, p models.Person, score float64) string {
		t.Helper()
		v := models.AssetSpeaker{ID: ids.New(ids.Speaker), WorkspaceID: a.WorkspaceID(), AssetID: assetID, Run: "run_1", Label: "speaker_00",
			Name: p.Name, PersonID: p.ID, Source: models.SpeakerByAuto, Score: score, Samples: []models.TimeSpan{}}
		if err := a.DB.Insert(ctx, store.CSpeakers, v); err != nil {
			t.Fatal(err)
		}
		return v.ID
	}
	face := func(assetID string, p models.Person, score float64) string {
		t.Helper()
		f := models.FaceTrack{ID: ids.New(ids.Face), WorkspaceID: a.WorkspaceID(), AssetID: assetID, Track: 1, Box: []float64{0.4, 0.2, 0.2, 0.3},
			Recognized: &models.FaceRecognized{PersonID: p.ID, Name: p.Name, Score: score, At: now}}
		if err := a.DB.Insert(ctx, store.CFaces, f); err != nil {
			t.Fatal(err)
		}
		return f.ID
	}
	set := func(coll, id string, fields bson.M) {
		t.Helper()
		if _, err := a.DB.C(coll).UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": fields}); err != nil {
			t.Fatal(err)
		}
	}
	maya := person("Maya", "")
	kitchen := addAsset(t, a, models.Asset{Name: "IMG_0042.MOV", Title: "Kitchen walkthrough", ProjectIDs: []string{prj}})
	garage := addAsset(t, a, models.Asset{Name: "IMG_2391.MOV", Title: "Garage interview"})
	both := []string{kitchen.ID, garage.ID}
	slices.Sort(both)

	t.Run("a voice", func(t *testing.T) {
		key := "recognized:" + maya.ID
		inKitchen := voice(kitchen.ID, maya, 0.873)
		Recognized(ctx, a, user, maya.ID)
		n := mustNote(t, a, user, key)
		if n.Kind != models.NotifyRecognized || n.Tone != "info" || n.Read || n.Title != "Recognized Maya's voice in “Kitchen walkthrough”" ||
			n.Body != "It's probably Maya (87% voice match). Check that it really is them, or choose “Not Maya?”." ||
			n.Link != links.AssetSpeakers(prj, kitchen.ID) || n.AssetID != kitchen.ID || n.ProjectID != prj {
			t.Fatalf("one video: %+v", n)
		}
		setRead(t, a, n.ID, true)

		// Recognized in another video: the same note, about both, unread again.
		inGarage := voice(garage.ID, maya, 0.91)
		Recognized(ctx, a, user, maya.ID)
		n = mustNote(t, a, user, key)
		if n.Read || n.Title != "Recognized Maya's voice in 2 more videos" || n.Link != links.Person(maya.ID) ||
			n.Body != "Check that it really is Maya in each of them, or choose “Not Maya?” where it isn't." || !slices.Equal(strs(n.Details["assets"]), both) {
			t.Fatalf("two videos: %+v", n)
		}
		setRead(t, a, n.ID, true)

		// Someone checks one: the note follows quietly and stays read.
		set(store.CSpeakers, inGarage, bson.M{"source": models.SpeakerByPerson})
		Recognized(ctx, a, user, maya.ID)
		if n = mustNote(t, a, user, key); !n.Read || n.Title != "Recognized Maya's voice in “Kitchen walkthrough”" {
			t.Fatalf("after a check: %+v", n)
		}

		// Nothing left to check: a note still unread is marked read.
		setRead(t, a, n.ID, false)
		set(store.CSpeakers, inKitchen, bson.M{"source": models.SpeakerByPerson})
		Recognized(ctx, a, user, maya.ID)
		if n = mustNote(t, a, user, key); !n.Read || n.ReadAt == nil || countNotes(t, a, bson.M{"groupKey": key}) != 1 {
			t.Fatalf("with nothing left to check: %+v", n)
		}
	})

	t.Run("a face", func(t *testing.T) {
		key := "recognized-face:" + maya.ID
		inKitchen := face(kitchen.ID, maya, 0.82)
		RecognizedFaces(ctx, a, user, maya.ID)
		n := mustNote(t, a, user, key)
		if n.Kind != models.NotifyRecognized || n.Tone != "info" || n.Read || n.Title != "Recognized Maya's face in “Kitchen walkthrough”" ||
			n.Body != "It looks like Maya (82% alike). Check that it really is them on Maya's page, or choose “Not Maya?”." ||
			n.Link != links.Person(maya.ID) || n.AssetID != kitchen.ID || n.ProjectID != prj || n.Details["by"] != "looks" {
			t.Fatalf("one clip: %+v", n)
		}
		setRead(t, a, n.ID, true)

		// Recognized in another clip: the same note, about both, unread again.
		inGarage := face(garage.ID, maya, 0.9)
		RecognizedFaces(ctx, a, user, maya.ID)
		n = mustNote(t, a, user, key)
		if n.Read || n.Title != "Recognized Maya's face in 2 more clips" ||
			n.Body != "Check that it really is Maya in each of them on their page, or choose “Not Maya?” where it isn't." || !slices.Equal(strs(n.Details["assets"]), both) {
			t.Fatalf("two clips: %+v", n)
		}
		setRead(t, a, n.ID, true)

		// Someone confirms one: the note follows quietly and stays read.
		set(store.CFaces, inGarage, bson.M{"personId": maya.ID, "name": maya.Name})
		RecognizedFaces(ctx, a, user, maya.ID)
		if n = mustNote(t, a, user, key); !n.Read || n.Title != "Recognized Maya's face in “Kitchen walkthrough”" {
			t.Fatalf("after a check: %+v", n)
		}

		// Nothing left to check: a note still unread is marked read.
		setRead(t, a, n.ID, false)
		set(store.CFaces, inKitchen, bson.M{"personId": maya.ID, "name": maya.Name})
		RecognizedFaces(ctx, a, user, maya.ID)
		if n = mustNote(t, a, user, key); !n.Read || n.ReadAt == nil || countNotes(t, a, bson.M{"groupKey": key}) != 1 {
			t.Fatalf("with nothing left to check: %+v", n)
		}
	})

	t.Run("no one to tell, nobody named, or a merged person: nothing", func(t *testing.T) {
		mayaR := person("Maya R.", maya.ID)
		voice(kitchen.ID, mayaR, 0.9)
		face(kitchen.ID, mayaR, 0.9)
		ghost := ids.New(ids.Person)
		for _, pid := range []string{"", ghost, mayaR.ID} {
			Recognized(ctx, a, user, pid)
			RecognizedFaces(ctx, a, user, pid)
		}
		Recognized(ctx, a, "", maya.ID)
		RecognizedFaces(ctx, a, "", maya.ID)
		for _, key := range []string{"recognized:" + ghost, "recognized-face:" + ghost, "recognized:" + mayaR.ID, "recognized-face:" + mayaR.ID} {
			noNote(t, a, user, key)
		}
		if c := countNotes(t, a, bson.M{"userId": ""}); c != 0 {
			t.Fatalf("%d notes for nobody", c)
		}
	})
}

// A skipped duplicate, an interrupted upload and Claude Code's proposals each
// leave one note, for the person they concern, saying what to do.
func TestDuplicateUploadAndAssistantNotes(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	user := a.Cfg.User.ID
	p := addProject(t, a, "Garage interview")

	t.Run("a duplicate", func(t *testing.T) {
		take := addAsset(t, a, models.Asset{Name: "IMG_2391.MOV", Title: "Garage interview take 2", ProjectIDs: []string{p.ID}})
		inbox := addAsset(t, a, models.Asset{Name: "drone-pass.mp4"})
		for range 2 { // dropped twice: still one note
			DuplicateSkipped(ctx, a, &take, "IMG_2391 copy.MOV", "upload")
		}
		DuplicateSkipped(ctx, a, &inbox, "drone-pass (1).mp4", "watch")
		key := "duplicate:" + take.ID + ":IMG_2391 copy.MOV"
		n := mustNote(t, a, user, key)
		if n.Kind != models.NotifyDuplicateSkipped || n.Tone != "info" || n.Title != "IMG_2391 copy.MOV was already in MetaRush" ||
			n.Body != "It is identical to “Garage interview take 2” (in Garage interview), so nothing was stored twice." ||
			n.Link != links.Asset(p.ID, take.ID) || n.Details["via"] != "upload" || n.Details["original"] != "IMG_2391.MOV" || countNotes(t, a, bson.M{"groupKey": key}) != 1 {
			t.Fatalf("note %+v", n)
		}
		if n := mustNote(t, a, user, "duplicate:"+inbox.ID+":drone-pass (1).mp4"); n.Body != "It is identical to “drone-pass” (in the inbox), so nothing was stored twice." ||
			n.Link != links.Asset("", inbox.ID) {
			t.Fatalf("in the inbox: %+v", n)
		}
	})

	t.Run("an upload that stopped", func(t *testing.T) {
		up := ids.New(ids.Upload)
		UploadInterrupted(ctx, a, up, "IMG_2391.MOV", 42, p.ID)
		n := mustNote(t, a, user, "upload_interrupted:"+up)
		if n.Kind != models.NotifyUploadInterrupted || n.Tone != "warning" || n.Title != "Upload of IMG_2391.MOV stopped at 42%" ||
			n.Body != "Drop the same file again to resume where it stopped." || n.Link != "/p/"+p.ID+"?tab=sources&activity=open" ||
			n.ProjectID != p.ID || fmt.Sprint(n.Details["percent"]) != "42" || n.Details["uploadId"] != up {
			t.Fatalf("note %+v", n)
		}
		// The person whose browser uploaded it; with nobody known, this computer's person.
		browserUser, toInbox, unknown := ids.New(ids.User), ids.New(ids.Upload), ids.New(ids.Upload)
		UploadInterrupted(ctx, &app.App{Cfg: a.Cfg, DB: a.DB, Actor: models.Actor{UserID: browserUser}}, toInbox, "drone-pass.mp4", 7, "")
		if n := mustNote(t, a, browserUser, "upload_interrupted:"+toInbox); n.Link != "/?activity=open" || n.Title != "Upload of drone-pass.mp4 stopped at 7%" {
			t.Fatalf("to the inbox: %+v", n)
		}
		UploadInterrupted(ctx, &app.App{Cfg: a.Cfg, DB: a.DB}, unknown, "b-roll.mov", 88, "")
		mustNote(t, a, user, "upload_interrupted:"+unknown)

		// A note that can't be written is reported, and the upload goes on.
		stopped, stop := context.WithCancel(ctx)
		stop()
		lost := ids.New(ids.Upload)
		if out := stderrOf(t, func() { UploadInterrupted(stopped, a, lost, "b-roll.mov", 3, "") }); !strings.Contains(out, "warning: notification (upload) not written: ") {
			t.Fatalf("standard error %q", out)
		}
		noNote(t, a, user, "upload_interrupted:"+lost)
	})

	t.Run("Claude Code's proposals", func(t *testing.T) {
		conv := ids.New(ids.Conversation)
		AssistantTurn(ctx, a, conv, 1, "", "", 0, 0, user) // nothing left for the person
		AssistantTurn(ctx, a, conv, 2, "", "", 2, 0, user)
		AssistantTurn(ctx, a, conv, 3, p.ID, p.Name, 1, 0, user)
		AssistantTurn(ctx, a, conv, 4, p.ID, p.Name, 1, 1, user)
		noNote(t, a, user, "assistant:"+conv+":t1")
		for _, c := range []struct{ turn, title, body, link string }{
			{"t2", "Claude Code proposed 2 changes", "Review them in the Claude Code panel.", "/?ask=" + conv},
			{"t3", "Claude Code proposed 1 change", "For Garage interview. Review them in the Claude Code panel.", "/p/" + p.ID + "?ask=" + conv},
			{"t4", "Claude Code needs your OK", "For Garage interview. Review them in the Claude Code panel.", "/p/" + p.ID + "?ask=" + conv},
		} {
			n := mustNote(t, a, user, "assistant:"+conv+":"+c.turn)
			if n.Kind != models.NotifyAssistantProposal || n.Tone != "info" || n.Title != c.title || n.Body != c.body || n.Link != c.link {
				t.Errorf("turn %s: %+v", c.turn, n)
			}
		}
	})
}

// The small helpers the sentences are built with.
func TestWordHelpers(t *testing.T) {
	for _, c := range []struct {
		v    any
		want int
	}{{3, 3}, {int32(4), 4}, {int64(5), 5}, {6.0, 6}, {"7", 0}, {nil, 0}} {
		if got := intOf(c.v); got != c.want {
			t.Errorf("intOf(%#v) = %d, want %d", c.v, got, c.want)
		}
	}
	for want, items := range map[string][]string{
		"":                                  nil,
		"the transcript":                    {"the transcript"},
		"the transcript and the frame grid": {"the transcript", "the frame grid"},
		"the thumbnail, the transcript and the frame grid": {"the thumbnail", "the transcript", "the frame grid"},
	} {
		if got := list(items); got != want {
			t.Errorf("list(%q) = %q, want %q", items, got, want)
		}
	}
	if capFirst("") != "" || capFirst("two people at a table") != "Two people at a table" {
		t.Errorf("capFirst: %q, %q", capFirst(""), capFirst("two people at a table"))
	}
	if got := jobKinds(map[string]int{"render": 1, "diarize": 2, "transcribe": 3, "": 1}); got != "1 job, 1 render, 2 speaker detections and 3 transcriptions" {
		t.Errorf("jobKinds = %q", got)
	}
	if got := counts(2, 1); got != "2 problems, 1 thing worth checking" {
		t.Errorf("counts = %q", got)
	}
	if lineCount(1) != "1 line" || lineCount(0) != "0 lines" {
		t.Errorf("lineCount: %q, %q", lineCount(1), lineCount(0))
	}
	if got := firstLine("  exit status 1\nstack trace"); got != "exit status 1" {
		t.Errorf("firstLine = %q", got)
	}
	if got := firstLine(strings.Repeat("x", 250)); got != strings.Repeat("x", 200)+"…" {
		t.Errorf("a long first line: %q", got)
	}
}
