package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
	"github.com/metavert-io/metarush/internal/voice"
)

// twoVoices gives a video a transcript with two voices (both unnamed, so two
// questions are open) and returns how the clip is shown.
func twoVoices(t *testing.T, a *app.App, video string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	for i, s := range []struct {
		from, to    float64
		label, text string
	}{{0.5, 2.5, "speaker_00", "So what did you build?"}, {3, 6, "speaker_01", "A robot that waters plants."}} {
		insert(t, a, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: video, Index: i, From: s.from, To: s.to,
			Text: s.text, Speaker: s.label, Words: []models.Word{{Text: s.text, From: s.from, To: s.to}}, CreatedAt: now})
	}
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), video, bsonSet("transcript", models.TranscriptStatus{State: "available", Precision: "word", Segments: 2, Words: 2, Speakers: 2}))
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), video, &as); err != nil {
		t.Fatal(err)
	}
	det := &speakers.Detection{Run: ids.New(ids.Run), Result: &voice.Result{Raw: 2, Clusters: []voice.Cluster{testVoice("speaker_00", 1, 0.5, 8), testVoice("speaker_01", 2, 3, 6)}},
		Decision: map[string]voice.Decision{}, Prints: true}
	if err := speakers.Store(ctx, a, &as, det, "test"); err != nil {
		t.Fatal(err)
	}
	return as.DisplayTitle()
}

func testJob(t *testing.T, a *app.App, j models.Job) models.Job {
	t.Helper()
	now := time.Now().UTC()
	j.ID, j.WorkspaceID = ids.New(ids.Job), a.WorkspaceID()
	if j.Payload == nil {
		j.Payload = map[string]any{}
	}
	j.Attempts, j.IdempotencyKey, j.MaxAttempts = []models.Attempt{}, ids.New("k"), 3
	j.CreatedAt, j.UpdatedAt, j.NextRunAt = now.Add(-2*time.Minute), now, now.Add(-2*time.Minute)
	j.CreatedBy = models.Actor{UserID: a.Cfg.User.ID, Via: "dashboard"}
	if j.State == models.JobSucceeded || j.State == models.JobFailed {
		f := now.Add(-time.Minute)
		j.FinishedAt = &f
	}
	insert(t, a, store.CJobs, j)
	return j
}

// Speaker detection in job details: a plain title, the Speakers tab as "Go to",
// the clip and project as related objects, and the open questions about the
// clip with the same words and link the activity bar and the bell use.
func TestJobShowSpeakerDetectionAndQuestions(t *testing.T) {
	a, project, video, _ := todoApp(t)
	ctx := context.Background()
	title := twoVoices(t, a, video)

	queued := testJob(t, a, models.Job{Type: jobs.TypeDiarize, State: models.JobQueued, Heavy: true, AssetID: video, Payload: map[string]any{"speakers": 2}})
	out, err := JobDetail(ctx, a, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Title != "Finding who's speaking in “"+title+"”" || out.KindLabel != "Speaker detection" {
		t.Fatalf("title %q kind %q", out.Title, out.KindLabel)
	}
	if out.Waiting == nil || out.Waiting.Code != jobs.WaitNoWorker || out.Waiting.Text != "No machine running MetaRush can tell voices apart right now. It starts as soon as one is." || out.Questions != nil {
		t.Fatalf("queued: waiting %+v questions %+v", out.Waiting, out.Questions)
	}
	if out.Related.Asset == nil || out.Related.Asset.ID != video || out.Related.Project == nil || out.Related.Project.ID != project {
		t.Fatalf("related %+v", out.Related)
	}
	if out.Target == nil || out.Target.Label != "Go to the speakers" || out.Target.Link != "/p/"+project+"?asset="+video+"&panel=speakers&tab=sources" {
		t.Fatalf("target %+v", out.Target)
	}

	done := testJob(t, a, models.Job{Type: jobs.TypeDiarize, State: models.JobSucceeded, AssetID: video, Result: map[string]any{"voices": 2}})
	out, err = JobDetail(ctx, a, done.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := models.OpenQuestions{Count: 2, Text: "2 questions about this clip", Link: "/todos?asset=" + video}
	if out.Questions == nil || *out.Questions != want {
		t.Fatalf("questions %+v, want %+v", out.Questions, want)
	}
	if out.Produced == nil || out.Produced.Speakers != 2 || !strings.Contains(out.Produced.Summary, "2 voices in “"+title+"”") {
		t.Fatalf("produced %+v", out.Produced)
	}
	tx := testJob(t, a, models.Job{Type: jobs.TypeTranscribe, State: models.JobSucceeded, AssetID: video, Heavy: true})
	if out, err := JobDetail(ctx, a, tx.ID); err != nil || out.Questions == nil || *out.Questions != want || out.Target == nil || out.Target.Label != "Go to the clip" {
		t.Fatalf("finished transcription: %+v %v", out, err)
	}
	failed := testJob(t, a, models.Job{Type: jobs.TypeDiarize, State: models.JobFailed, AssetID: video, Error: "speaker detection is unavailable or failed; see the asset's speaker status"})
	if out, err := JobDetail(ctx, a, failed.ID); err != nil || out.Questions != nil || !out.CanRetry || !strings.Contains(out.Advice, "couldn't tell the voices apart") {
		t.Fatalf("failed detection: %+v %v", out, err)
	}

	// The bell: the finished detection's notification carries the live questions.
	notify.SpeakersDone(ctx, a, &done, models.JobSucceeded)
	list, err := CallTyped[NotificationListOut](ctx, a, "notification.list", NotificationListIn{}, Caller{})
	if err != nil {
		t.Fatal(err)
	}
	var row *NotificationRow
	for i := range list.Notifications {
		if list.Notifications[i].Kind == models.NotifySpeakers {
			row = &list.Notifications[i]
		}
	}
	if row == nil || row.Title != "Found 2 voices in “"+title+"”" || !strings.Contains(row.Link, "panel=speakers") || row.Questions == nil || *row.Questions != want {
		t.Fatalf("speakers notification %+v", row)
	}
	notify.SpeakersDone(ctx, a, &done, models.JobSucceeded) // once per job
	again, _ := CallTyped[NotificationListOut](ctx, a, "notification.list", NotificationListIn{}, Caller{})
	if len(again.Notifications) != len(list.Notifications) {
		t.Fatalf("a second call wrote another notification: %d → %d", len(list.Notifications), len(again.Notifications))
	}

	// Naming a voice answers its question everywhere: one left.
	if _, err := CallTyped[SpeakerRenameOut](ctx, a, "footage.speakers.rename", SpeakerRenameIn{AssetID: video, Speaker: "Speaker 2", Name: "Ada"}, Caller{}); err != nil {
		t.Fatal(err)
	}
	if out, _ := JobDetail(ctx, a, done.ID); out.Questions == nil || out.Questions.Count != 1 || out.Questions.Text != "1 question about this clip" {
		t.Fatalf("after naming one voice: %+v", out.Questions)
	}
}

// A person's name finds the clips where they speak; transcript hits say who said it.
func TestSearchFindsPeopleAndShowsSpeakerNames(t *testing.T) {
	a, project, video, _ := todoApp(t)
	ctx := context.Background()
	title := twoVoices(t, a, video)
	if _, err := CallTyped[SpeakerRenameOut](ctx, a, "footage.speakers.rename", SpeakerRenameIn{AssetID: video, Speaker: "Speaker 2", Name: "Ada Lovelace"}, Caller{}); err != nil {
		t.Fatal(err)
	}
	out := call[SearchOut](t, a, "search", SearchIn{Query: "lovelace"})
	var hit *SearchAsset
	for i := range out.Videos {
		if out.Videos[i].AssetID == video {
			hit = &out.Videos[i]
		}
	}
	if hit == nil || hit.MatchedIn != "speaker" || hit.Speaker != "Ada Lovelace" || hit.Title != title || hit.ProjectID != project {
		t.Fatalf("person search: %+v", out.Videos)
	}
	var marked []string
	for _, p := range hit.Snippet {
		if p.Hit {
			marked = append(marked, p.Text)
		}
	}
	if strings.Join(marked, "|") != "Lovelace" {
		t.Fatalf("snippet %+v", hit.Snippet)
	}
	words := call[SearchOut](t, a, "search", SearchIn{Query: "robot"})
	if len(words.Moments) != 1 || words.Moments[0].SpeakerName != "Ada Lovelace" || words.Moments[0].Speaker != "speaker_01" {
		t.Fatalf("moment speaker: %+v", words.Moments)
	}
	unnamed := call[SearchOut](t, a, "search", SearchIn{Query: "build"})
	if len(unnamed.Moments) != 1 || unnamed.Moments[0].SpeakerName != "" {
		t.Fatalf("an unnamed voice has no name: %+v", unnamed.Moments)
	}
	// Scoped to another project: not found.
	other, err := CallTyped[ProjectOut](ctx, a, "project.create", ProjectCreateIn{Name: "Other film"}, Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if scoped := call[SearchOut](t, a, "search", SearchIn{Query: "lovelace", ProjectID: other.Project.ID}); len(scoped.Videos) != 0 {
		t.Fatalf("scoped search found %+v", scoped.Videos)
	}
}

// A clip's questions follow it: dragged into a project they count on that
// project's badge, and after a rename they name the clip by its new title.
func TestClipQuestionsFollowProjectsAndTitle(t *testing.T) {
	a, project, _, _ := todoApp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	clip := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{}, Kind: "video", Name: "MVI_0042.MP4", SHA256: ids.New("sha"),
		State: models.AssetAvailable, Media: models.MediaInfo{HasVideo: true, HasAudio: true, Duration: 30}, Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
	insert(t, a, store.CAssets, clip)
	twoVoices(t, a, clip.ID)
	counts := func() *todos.Counts {
		out, err := CallTyped[todos.Counts](ctx, a, "todos.counts", TodoCountsIn{}, Caller{})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if c := counts(); c.ByAsset[clip.ID] != 2 || c.ByProject[project] != 0 {
		t.Fatalf("inbox clip: %+v", c)
	}
	call[AssetLinkOut](t, a, "asset.link", AssetLinkIn{AssetID: clip.ID, ProjectID: project})
	if c := counts(); c.ByProject[project] != 2 {
		t.Fatalf("after adding the clip to the project: %+v", c)
	}
	call[AssetLinkOut](t, a, "asset.update", AssetUpdateIn{ID: clip.ID, Title: "Kitchen chat"})
	list := listOpen(t, a, TodoListIn{AssetID: clip.ID})
	for _, td := range list.Todos {
		if !strings.Contains(td.Question, "“Kitchen chat”") || td.Context.AssetTitle != "Kitchen chat" || td.Context.AssetName != "MVI_0042.MP4" {
			t.Fatalf("question after the rename: %q %+v", td.Question, td.Context)
		}
	}
	call[AssetLinkOut](t, a, "asset.link", AssetLinkIn{AssetID: clip.ID, ProjectID: project, Unlink: true})
	if c := counts(); c.ByProject[project] != 0 || c.ByAsset[clip.ID] != 2 {
		t.Fatalf("after removing the clip from the project: %+v", c)
	}
}
