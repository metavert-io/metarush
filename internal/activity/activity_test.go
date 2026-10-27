package activity

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/todos"
)

func addJob(t *testing.T, a *app.App, j models.Job) models.Job {
	t.Helper()
	now := time.Now().UTC()
	j.ID, j.WorkspaceID = ids.New(ids.Job), a.WorkspaceID()
	if j.Payload == nil {
		j.Payload = map[string]any{}
	}
	j.Attempts, j.IdempotencyKey, j.MaxAttempts = []models.Attempt{}, ids.New("k"), 3
	j.CreatedAt, j.UpdatedAt, j.NextRunAt = now.Add(-time.Minute), now.Add(-30*time.Second), now.Add(-time.Minute)
	if j.State == models.JobSucceeded {
		f := now.Add(-20 * time.Second)
		j.FinishedAt = &f
	}
	if err := a.DB.Insert(context.Background(), store.CJobs, j); err != nil {
		t.Fatal(err)
	}
	return j
}

func setWorker(t *testing.T, a *app.App, w models.WorkerDevice) {
	t.Helper()
	w.ID, w.WorkspaceID = a.Cfg.Installation.ID, a.WorkspaceID()
	if _, err := a.DB.C(store.CWorkers).ReplaceOne(context.Background(), bson.M{"_id": w.ID}, w, options.Replace().SetUpsert(true)); err != nil {
		t.Fatal(err)
	}
}

func build(t *testing.T, a *app.App) *Summary {
	t.Helper()
	s, err := Build(context.Background(), a, &SummaryIn{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func item(s *Summary, key string) *Item {
	for i := range s.Items {
		if s.Items[i].Key == key {
			return &s.Items[i]
		}
	}
	return nil
}

func TestSummaryExplainsWhyWorkWaits(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: "IMG_2391.MOV", Title: "Garage interview", CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	addJob(t, a, models.Job{Type: jobs.TypeThumbnail, State: models.JobSucceeded, AssetID: as.ID})
	tx := addJob(t, a, models.Job{Type: jobs.TypeTranscribe, State: models.JobQueued, Heavy: true, AssetID: as.ID})
	pct := 18
	setWorker(t, a, models.WorkerDevice{Name: "studio-mac", JobTypes: []string{jobs.TypeThumbnail, jobs.TypeTranscribe}, Concurrency: 2,
		HeavyEnabled: true, DeferringHeavy: true, BatteryPercent: &pct, OnBattery: true, HeavyBatteryThreshold: 25,
		DeferReason: "battery at 18%, below the 25% set for big jobs", LastHeartbeat: now})

	// Low battery: the transcript step is paused, with the machine, the level and "Process now".
	s := build(t, a)
	it := item(s, "asset:"+as.ID)
	if it == nil {
		t.Fatalf("no item for the asset in %+v", s.Items)
	}
	if it.Title != "Garage interview" || it.State != StateWaiting || it.Waiting == nil || it.Waiting.Code != jobs.WaitBattery {
		t.Fatalf("item %+v waiting %+v", it, it.Waiting)
	}
	if it.Waiting.Text != "Paused: battery at 18% on studio-mac." || !it.CanProcessNow || len(it.ProcessJobIDs) != 1 || it.ProcessJobIDs[0] != tx.ID {
		t.Fatalf("waiting %+v canProcessNow %v ids %v", it.Waiting, it.CanProcessNow, it.ProcessJobIDs)
	}
	var paused *Step
	for i := range it.Steps {
		if it.Steps[i].Kind == jobs.TypeTranscribe {
			paused = &it.Steps[i]
		}
	}
	if paused == nil || paused.State != "paused" || paused.Label != "Transcript" {
		t.Fatalf("transcript step %+v", paused)
	}
	if len(s.Waiting) != 1 || s.Waiting[0].Code != jobs.WaitBattery || s.Waiting[0].Count != 1 || !s.Waiting[0].CanProcessNow {
		t.Fatalf("wait groups %+v", s.Waiting)
	}
	if s.Waiting[0].Text != "1 transcription paused: battery at 18% on studio-mac" || !strings.Contains(s.Headline, "paused: battery at 18%") {
		t.Fatalf("group text %q headline %q", s.Waiting[0].Text, s.Headline)
	}
	if s.Counts.Paused != 1 || len(s.Workers) != 1 || !s.Workers[0].DeferringHeavy || !s.Workers[0].This {
		t.Fatalf("counts %+v workers %+v", s.Counts, s.Workers)
	}

	// "Process now" (allowOnBattery) lets it start: no battery wait any more.
	if _, err := a.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": tx.ID}, bson.M{"$set": bson.M{"allowOnBattery": true}}); err != nil {
		t.Fatal(err)
	}
	s = build(t, a)
	if it := item(s, "asset:"+as.ID); it == nil || it.State != StateActive || len(s.Waiting) != 0 {
		t.Fatalf("after Process now: item %+v groups %+v", it, s.Waiting)
	}

	// No machine online that can transcribe: says so, and Process now cannot help.
	if _, err := a.DB.C(store.CWorkers).UpdateOne(ctx, bson.M{"_id": a.Cfg.Installation.ID}, bson.M{"$set": bson.M{"lastHeartbeat": now.Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	s = build(t, a)
	it = item(s, "asset:"+as.ID)
	if it == nil || it.Waiting == nil || it.Waiting.Code != jobs.WaitNoWorker || it.Waiting.CanProcessNow {
		t.Fatalf("no worker: %+v", it)
	}
	if it.Waiting.Text != "No machine running MetaRush can make transcripts right now. It starts as soon as one is." {
		t.Fatalf("no-worker text %q", it.Waiting.Text)
	}
	if len(s.Waiting) != 1 || s.Waiting[0].Code != jobs.WaitNoWorker || len(s.Workers) != 0 ||
		s.Waiting[0].Text != "1 transcription waiting for a machine running MetaRush" {
		t.Fatalf("no-worker groups %+v workers %+v", s.Waiting, s.Workers)
	}
}

// A failed step says what couldn't be made and what to do next in plain
// words; the technical error stays in Error for Details.
func TestSummaryFailureIsPlain(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: "IMG_2400.MOV", Title: "Porch talk", CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	fin := now.Add(-time.Minute)
	addJob(t, a, models.Job{Type: jobs.TypeThumbnail, State: models.JobSucceeded, AssetID: as.ID, FinishedAt: &fin})
	tx := addJob(t, a, models.Job{Type: jobs.TypeTranscribe, State: models.JobFailed, AssetID: as.ID, FinishedAt: &fin,
		Error: "sherpa-onnx-offline failed: exit status 1"})
	s := build(t, a)
	it := item(s, "asset:"+as.ID)
	if it == nil || it.State != StateFailed || !it.CanRetry || len(it.RetryJobIDs) != 1 || it.RetryJobIDs[0] != tx.ID {
		t.Fatalf("failed item %+v", it)
	}
	if it.Problem != "The transcript couldn't be made." || !strings.HasPrefix(it.Hint, "Try again.") {
		t.Fatalf("problem %q hint %q", it.Problem, it.Hint)
	}
	if !strings.Contains(it.Error, "sherpa-onnx-offline failed: exit status 1") {
		t.Fatalf("the technical error stays for Details: %q", it.Error)
	}
}

func TestSummaryRenderItemIsPlain(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: "Garage film", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CProjects, p); err != nil {
		t.Fatal(err)
	}
	j := addJob(t, a, models.Job{Type: jobs.TypeRender, State: models.JobQueued, Heavy: true, ProjectID: p.ID})
	r := models.Render{ID: ids.New(ids.Render), WorkspaceID: a.WorkspaceID(), ProjectID: p.ID, RevisionID: ids.New(ids.Revision), RevisionNumber: 3,
		Preset: "vertical-1080p", Quality: "review", JobID: j.ID, State: models.RenderQueued, CreatedAt: now}
	if err := a.DB.Insert(ctx, store.CRenders, r); err != nil {
		t.Fatal(err)
	}
	s := build(t, a)
	it := item(s, "render:"+r.ID)
	if it == nil {
		t.Fatalf("no render item in %+v", s.Items)
	}
	if it.Title != "Review copy of version 3" || it.Subtitle != "Garage film, vertical" {
		t.Fatalf("render item title %q subtitle %q", it.Title, it.Subtitle)
	}
	if !strings.Contains(it.Link, r.ID) {
		t.Fatalf("link %q", it.Link)
	}
}

func TestSummaryShowsClaudeCodeConversations(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ended := now.Add(-30 * time.Second)
	add := func(c assistant.Conversation) string {
		c.ID, c.WorkspaceID, c.Title, c.InstallationName = ids.New(assistant.ConvPrefix), a.WorkspaceID(), "Tighten the middle", "studio-mac"
		if err := a.DB.Insert(ctx, store.CAssistantConversations, c); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	working := add(assistant.Conversation{State: assistant.ConvWorking, UpdatedAt: now,
		Turns: []assistant.Turn{{N: 1, State: assistant.TurnRunning, StartedAt: now.Add(-10 * time.Second)}}})
	plans := add(assistant.Conversation{State: assistant.ConvIdle, UpdatedAt: ended, ProjectID: "prj_x", ProjectName: "Spring launch",
		Turns: []assistant.Turn{{N: 1, State: assistant.TurnSucceeded, StartedAt: now.Add(-time.Minute), EndedAt: &ended, Proposals: 2}}})
	asks := add(assistant.Conversation{State: assistant.ConvIdle, UpdatedAt: ended,
		Turns:   []assistant.Turn{{N: 1, State: assistant.TurnSucceeded, StartedAt: now.Add(-time.Minute), EndedAt: &ended}},
		Actions: []assistant.AssistantAction{{ID: "act_1", Turn: 1, State: assistant.ActionPending}}})
	// A card raised answering someone else's message is theirs to OK (D179).
	hers := add(assistant.Conversation{State: assistant.ConvIdle, UpdatedAt: ended, UserID: a.Cfg.User.ID, UserName: a.Cfg.User.Name,
		Turns: []assistant.Turn{{N: 1, State: assistant.TurnSucceeded, StartedAt: now.Add(-2 * time.Minute), EndedAt: &ended},
			{N: 2, User: "Maya", UserID: "usr_maya", State: assistant.TurnSucceeded, StartedAt: now.Add(-time.Minute), EndedAt: &ended}},
		Actions: []assistant.AssistantAction{{ID: "act_2", Turn: 2, State: assistant.ActionPending}}})
	old := now.Add(-3 * time.Hour)
	stale := add(assistant.Conversation{State: assistant.ConvIdle, UpdatedAt: old,
		Turns: []assistant.Turn{{N: 1, State: assistant.TurnSucceeded, StartedAt: old, EndedAt: &old}}})
	s := build(t, a)
	for id, want := range map[string]string{working: "Working", plans: "2 proposals ready", asks: "Needs your OK", hers: "Needs Maya's OK"} {
		it := item(s, "assistant:"+id)
		if it == nil || it.StageLabel != want || !strings.Contains(it.Link, "?ask="+id) || it.Machine != "studio-mac" {
			t.Fatalf("%s: %+v", want, it)
		}
	}
	if it := item(s, "assistant:"+plans); !strings.HasPrefix(it.Link, "/p/prj_x?") || it.Subtitle != "Spring launch" {
		t.Fatalf("project link: %+v", it)
	}
	if item(s, "assistant:"+stale) != nil {
		t.Fatal("a conversation that ended hours ago is not activity")
	}
}

// Narration work reads like what it is doing ("Making the AI voice for line 3"),
// and the audio MetaRush made for it does not show up as files of its own.
func TestSummaryNarrationIsPlain(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: "Garage film", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CProjects, p); err != nil {
		t.Fatal(err)
	}
	rev := ids.New(ids.Revision)
	run := addJob(t, a, models.Job{Type: jobs.TypeVoiceover, State: models.JobRunning, ProjectID: p.ID,
		Payload:  map[string]any{"action": "tts", "title": "AI voice", "revisionId": rev, "revisionNumber": 2, "lines": 5},
		Progress: models.Progress{Pct: 0.4, Message: "Making the AI voice for line 3"}})
	take := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "audio", Name: "Narration – line 1 (AI voice).mp3", ProjectIDs: []string{p.ID},
		Tags: []string{"voiceover", "tts", "ai-voice"}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, take); err != nil {
		t.Fatal(err)
	}
	addJob(t, a, models.Job{Type: jobs.TypeWaveform, State: models.JobRunning, AssetID: take.ID})
	s := build(t, a)
	it := item(s, "job:"+run.ID)
	if it == nil {
		t.Fatalf("no narration item in %+v", s.Items)
	}
	if it.Category != CatNarration || it.Title != "AI voice" || it.Subtitle != "Garage film, version 2" || it.StageLabel != "Making the AI voice for line 3" ||
		it.Pct == nil || *it.Pct != 0.4 || !strings.Contains(it.Link, rev) {
		t.Fatalf("narration item %+v", it)
	}
	if s.Counts.Narration != 1 || s.Headline != "Making the AI voice for line 3" {
		t.Fatalf("counts %+v headline %q", s.Counts, s.Headline)
	}
	if item(s, "asset:"+take.ID) != nil {
		t.Fatal("audio made for narration should not be listed as a file of its own")
	}
	// A failure is shown plainly and is not offered as a plain retry (it is started again where it was asked for).
	fin := now.Add(-10 * time.Second)
	failed := addJob(t, a, models.Job{Type: jobs.TypeVoiceover, State: models.JobFailed, ProjectID: p.ID, Error: "ElevenLabs is not set up on this machine", FinishedAt: &fin,
		Payload: map[string]any{"action": "tts", "title": "AI voice", "revisionId": rev, "revisionNumber": 2, "errorCode": "E_NOT_CONFIGURED"}})
	s = build(t, a)
	if f := item(s, "job:"+failed.ID); f == nil || f.State != StateFailed || f.CanRetry || f.Error != "ElevenLabs is not set up on this machine" {
		t.Fatalf("failed narration item %+v", f)
	}
}

// Speaker detection someone asked for shows in plain words, waits for battery
// like any big job, links to the clip's Speakers tab, and a finished clip says
// how many questions about it are open (the words and link job details use).
func TestSpeakerDetectionAndQuestionsInActivity(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: "MVI_0042.MP4", Title: "Kitchen chat", CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	dz := addJob(t, a, models.Job{Type: jobs.TypeDiarize, State: models.JobQueued, Heavy: true, AssetID: as.ID})
	pct := 18
	setWorker(t, a, models.WorkerDevice{Name: "studio-mac", JobTypes: []string{jobs.TypeThumbnail, jobs.TypeDiarize}, Concurrency: 2,
		HeavyEnabled: true, DeferringHeavy: true, BatteryPercent: &pct, OnBattery: true, HeavyBatteryThreshold: 25, LastHeartbeat: now})

	s := build(t, a)
	it := item(s, "job:"+dz.ID)
	if it == nil {
		t.Fatalf("no speaker detection item in %+v", s.Items)
	}
	if it.Title != "Finding who's speaking in “Kitchen chat”" || it.Category != CatProcessing || it.State != StateWaiting || it.Waiting == nil || it.Waiting.Code != jobs.WaitBattery {
		t.Fatalf("waiting item %+v (waiting %+v)", it, it.Waiting)
	}
	if !it.CanProcessNow || len(it.ProcessJobIDs) != 1 || !strings.Contains(it.Link, "panel=speakers") {
		t.Fatalf("process now %v %v, link %q", it.CanProcessNow, it.ProcessJobIDs, it.Link)
	}
	if len(s.Waiting) != 1 || s.Waiting[0].Text != "1 speaker detection paused: battery at 18% on studio-mac" {
		t.Fatalf("wait groups %+v", s.Waiting)
	}

	// Finished, with a question about the clip still open.
	fin := now.Add(-10 * time.Second)
	if _, err := a.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": dz.ID}, bson.M{"$set": bson.M{"state": models.JobSucceeded, "finishedAt": fin, "result": bson.M{"voices": 2}}}); err != nil {
		t.Fatal(err)
	}
	addJob(t, a, models.Job{Type: jobs.TypeThumbnail, State: models.JobSucceeded, AssetID: as.ID})
	if _, err := todos.Upsert(ctx, a, models.Todo{Kind: models.TodoSpeakerUnnamed, Scope: "asset", AssetID: as.ID, GroupKey: "speaker:" + as.ID + ":speaker_01@run",
		Question: "Who is Speaker 2 in “Kitchen chat”?", FreeText: true}); err != nil {
		t.Fatal(err)
	}
	s = build(t, a)
	it = item(s, "job:"+dz.ID)
	if it == nil || it.State != StateDone || it.Title != "Who's speaking in “Kitchen chat”" || it.StageLabel != "Found 2 voices" {
		t.Fatalf("finished item %+v", it)
	}
	want := models.OpenQuestions{Count: 1, Text: "1 question about this clip", Link: "/todos?asset=" + as.ID}
	if it.Questions == nil || *it.Questions != want {
		t.Fatalf("questions %+v, want %+v", it.Questions, want)
	}
	if clip := item(s, "asset:"+as.ID); clip == nil || clip.State != StateDone || clip.Questions == nil || *clip.Questions != want {
		t.Fatalf("finished clip should carry the same questions: %+v", clip)
	}
}

// Looking at what's on screen: while a clip is being prepared it is one more
// step of the clip's row (one row per clip); on its own (catching up, or after
// the rest finished) it has its own row in plain words.
func TestLookingAtAClipInActivity(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: "IMG_0042.MOV", Title: "Kitchen chat", CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	setWorker(t, a, models.WorkerDevice{Name: "studio-mac", JobTypes: []string{jobs.TypeTranscribe, jobs.TypeVisual}, Concurrency: 2, HeavyEnabled: true, LastHeartbeat: now})
	addJob(t, a, models.Job{Type: jobs.TypeProxy, State: models.JobSucceeded, AssetID: as.ID})
	tx := addJob(t, a, models.Job{Type: jobs.TypeTranscribe, State: models.JobRunning, Heavy: true, AssetID: as.ID})
	look := addJob(t, a, models.Job{Type: jobs.TypeVisual, State: models.JobRunning, Heavy: true, AssetID: as.ID})

	s := build(t, a)
	var rows []Item
	for _, it := range s.Items {
		if it.AssetID == as.ID {
			rows = append(rows, it)
		}
	}
	if len(rows) != 1 || rows[0].Key != "asset:"+as.ID {
		t.Fatalf("one row for the clip while it is prepared: %+v", rows)
	}
	var step *Step
	for i := range rows[0].Steps {
		if rows[0].Steps[i].Kind == "visual" {
			step = &rows[0].Steps[i]
		}
	}
	if step == nil || step.Label != "What's on screen" || step.State != "running" || step.JobID != look.ID || !strings.Contains(rows[0].StageLabel, "what's on screen") {
		t.Fatalf("the look is a step of the clip's row: %+v / %+v", step, rows[0])
	}

	// The rest is done; the look still runs: its own row.
	if _, err := a.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": tx.ID}, bson.M{"$set": bson.M{"state": models.JobSucceeded, "finishedAt": now}}); err != nil {
		t.Fatal(err)
	}
	s = build(t, a)
	it := item(s, "job:"+look.ID)
	if it == nil || it.Title != "Looking at what's on screen in “Kitchen chat”" || it.State != StateActive || it.Kind != "visual" {
		t.Fatalf("its own row: %+v", it)
	}
	if row := item(s, "asset:"+as.ID); row != nil {
		for _, st := range row.Steps {
			if st.Kind == "visual" {
				t.Fatalf("not twice: %+v", row)
			}
		}
	}
}
