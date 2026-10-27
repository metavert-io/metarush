package activity

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/todos"
	"github.com/metavert-io/metarush/internal/uploads"
)

func insert(t *testing.T, a *app.App, coll string, doc any) {
	t.Helper()
	if err := a.DB.Insert(context.Background(), coll, doc); err != nil {
		t.Fatal(err)
	}
}

func clipIn(t *testing.T, a *app.App, title string, projectIDs ...string) models.Asset {
	t.Helper()
	now := time.Now().UTC()
	if projectIDs == nil {
		projectIDs = []string{}
	}
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: strings.ToLower(strings.ReplaceAll(title, " ", "-")) + ".mov", Title: title,
		ProjectIDs: projectIDs, SHA256: ids.New("sha"), State: models.AssetAvailable, CreatedAt: now, UpdatedAt: now}
	insert(t, a, store.CAssets, as)
	return as
}

func setJob(t *testing.T, a *app.App, id string, set bson.M) {
	t.Helper()
	if _, err := a.DB.C(store.CJobs).UpdateOne(context.Background(), bson.M{"_id": id}, bson.M{"$set": set}); err != nil {
		t.Fatal(err)
	}
}

// Build gathers everything in flight from the database and this daemon's
// upload records: uploads, other machines' ingests, renders, generated
// clips, link imports, Claude Code conversations and open questions; a
// project's view keeps to that project.
func TestBuildGathersWhatIsInFlight(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	garden := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: "Garden film", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	trailer := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: "Trailer", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	insert(t, a, store.CProjects, garden)
	insert(t, a, store.CProjects, trailer)
	// Another machine, offline now: its name still says where an ingest runs.
	insert(t, a, store.CWorkers, models.WorkerDevice{ID: "ins_laptop", WorkspaceID: a.WorkspaceID(), Name: "laptop", LastHeartbeat: now.Add(-time.Hour)})

	// This daemon's browser uploads: one arriving, one finished whose clip was
	// prepared long ago (its jobs are found by the clip).
	walk := clipIn(t, a, "Garden walk", garden.ID)
	old := addJob(t, a, models.Job{Type: jobs.TypeThumbnail, State: models.JobSucceeded, AssetID: walk.ID})
	setJob(t, a, old.ID, bson.M{"finishedAt": now.Add(-13 * time.Hour), "updatedAt": now.Add(-13 * time.Hour)})
	st := uploads.For(a.Cfg.StateDir())
	arriving, err := st.Create("roses.mov", 1000, garden.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	st.Update(arriving.ID, true, func(r *uploads.Record) { r.Offset, r.Rate = 500, 100 })
	finished, _ := st.Create("walk.mov", 2000, garden.ID, "")
	st.Update(finished.ID, true, func(r *uploads.Record) {
		r.State, r.Result = uploads.StateDone, &ingest.Result{Status: "ingested", AssetID: walk.ID}
	})

	// Other processes' ingests into the store.
	session := func(change func(s *models.IngestSession)) models.IngestSession {
		s := models.IngestSession{ID: ids.New(ids.Ingest), WorkspaceID: a.WorkspaceID(), InstallationID: "ins_laptop", ProjectID: garden.ID, SourceName: "card-07.mov",
			Size: 100, BytesDone: 50, State: "uploading", Via: "watch", AssetID: ids.New(ids.Asset), CreatedAt: now.Add(-time.Minute), UpdatedAt: now}
		change(&s)
		insert(t, a, store.CIngest, s)
		return s
	}
	watching := session(func(*models.IngestSession) {})
	stalled := session(func(s *models.IngestSession) { s.UpdatedAt = now.Add(-50 * time.Second) }) // quiet for over 45 s
	byImport := session(func(s *models.IngestSession) { s.JobID = "job_import" })
	covered := session(func(s *models.IngestSession) { s.AssetID = walk.ID })
	elsewhere := session(func(s *models.IngestSession) { s.InstallationID, s.Via = "ins_unknown", "" })
	session(func(s *models.IngestSession) { s.UpdatedAt = now.Add(-10 * time.Minute) }) // long quiet: not shown

	// Renders: finished by itself, failed, rendering, waiting.
	render := func(state string, change func(r *models.Render)) models.Render {
		r := models.Render{ID: ids.New(ids.Render), WorkspaceID: a.WorkspaceID(), ProjectID: garden.ID, RevisionID: ids.New(ids.Revision), RevisionNumber: 2,
			Preset: "vertical-1080p", Quality: "review", State: state, CreatedAt: now.Add(-time.Minute)}
		change(&r)
		insert(t, a, store.CRenders, r)
		return r
	}
	done := render(models.RenderAvailable, func(r *models.Render) {
		c := now.Add(-time.Minute)
		r.CompletedAt, r.RenderedByName, r.Follow = &c, "studio-mac", true
	})
	earlier := render(models.RenderAvailable, func(r *models.Render) {
		c := now.Add(-5 * time.Minute)
		r.CompletedAt = &c
	})
	failJob := addJob(t, a, models.Job{Type: jobs.TypeRender, State: models.JobFailed, ProjectID: garden.ID, Error: "frame 12 failed\nat render.mjs:42"})
	failed := render(models.RenderFailed, func(r *models.Render) { r.JobID, r.Error = failJob.ID, "frame 12 failed\nat render.mjs:42" })
	runJob := addJob(t, a, models.Job{Type: jobs.TypeRender, State: models.JobRunning, Heavy: true, ProjectID: garden.ID,
		Progress: models.Progress{Pct: 0.3, Message: "drawing frame 30 of 100"}, Lease: &models.Lease{OwnerName: "studio-mac"}})
	setJob(t, a, runJob.ID, bson.M{"attempts": []models.Attempt{{StartedAt: now.Add(-time.Minute)}}})
	rendering := render(models.RenderRendering, func(r *models.Render) { r.JobID = runJob.ID })
	waitJob := addJob(t, a, models.Job{Type: jobs.TypeRender, State: models.JobQueued, Heavy: true, ProjectID: garden.ID})
	waiting := render(models.RenderQueued, func(r *models.Render) { r.JobID = waitJob.ID })

	// A generated clip on its way, one whose record is gone, and one that
	// finished and is being prepared.
	gen := models.Generation{ID: "gen_" + ids.New("g")[2:], WorkspaceID: a.WorkspaceID(), ProjectID: garden.ID, Operation: "shot", Params: models.GenParams{Seconds: 6},
		Take: 1, ProviderTitle: "Google Gemini", Prompt: "A fox in the snow", CreatedAt: now}
	insert(t, a, store.CGenerations, gen)
	making := addJob(t, a, models.Job{Type: jobs.TypeGenerate, State: models.JobRunning, ProjectID: garden.ID, Payload: map[string]any{"generationId": gen.ID},
		Progress: models.Progress{Stage: "making", Pct: 0.5}})
	orphan := addJob(t, a, models.Job{Type: jobs.TypeGenerate, State: models.JobRunning, Payload: map[string]any{"generationId": "gen_gone"}})
	fox := clipIn(t, a, "Fox", garden.ID)
	madeFox := addJob(t, a, models.Job{Type: jobs.TypeGenerate, State: models.JobSucceeded, ProjectID: garden.ID, Payload: map[string]any{"generationId": gen.ID},
		Result: map[string]any{"assetId": fox.ID}})
	addJob(t, a, models.Job{Type: jobs.TypeProxy, State: models.JobRunning, AssetID: fox.ID})

	// A link import that finished: its clip shows on the import's row only.
	imported := clipIn(t, a, "Imported talk", garden.ID)
	imp := addJob(t, a, models.Job{Type: jobs.TypeImport, State: models.JobSucceeded, ProjectID: garden.ID,
		Payload: map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ", "platform": models.PlatformYouTube, "kind": "video"}, Result: map[string]any{"assetId": imported.ID}})
	addJob(t, a, models.Job{Type: jobs.TypeTranscribe, State: models.JobRunning, AssetID: imported.ID})

	// Narration in the trailer (its project's name is looked up).
	voice := addJob(t, a, models.Job{Type: jobs.TypeVoiceover, State: models.JobRunning, ProjectID: trailer.ID, Payload: map[string]any{"title": "AI voice"},
		Progress: models.Progress{Pct: 0.2, Message: "Making the AI voice for line 1"}})
	trailerClip := clipIn(t, a, "Teaser shot", trailer.ID)
	addJob(t, a, models.Job{Type: jobs.TypeThumbnail, State: models.JobRunning, AssetID: trailerClip.ID})

	// Claude Code conversations: one stopped with an error, one interrupted,
	// one whose turn the daemon lost, one archived, one with a plan.
	conv := func(c assistant.Conversation) string {
		c.ID, c.WorkspaceID, c.InstallationName = ids.New(assistant.ConvPrefix), a.WorkspaceID(), "studio-mac"
		if c.Title == "" {
			c.Title = "Tighten the middle"
		}
		insert(t, a, store.CAssistantConversations, c)
		return c.ID
	}
	ended := now.Add(-time.Minute)
	broken := conv(assistant.Conversation{State: assistant.ConvIdle, UpdatedAt: ended, ProjectID: garden.ID, ProjectName: "Garden film",
		Turns: []assistant.Turn{{N: 1, State: assistant.TurnFailed, StartedAt: now.Add(-2 * time.Minute), EndedAt: &ended, Error: "claude exited 1\nstack"}}})
	interrupted := conv(assistant.Conversation{State: assistant.ConvIdle, UpdatedAt: ended,
		Turns: []assistant.Turn{{N: 1, State: assistant.TurnInterrupted, StartedAt: now.Add(-2 * time.Minute), EndedAt: &ended}}})
	lost := conv(assistant.Conversation{State: assistant.ConvWorking, UpdatedAt: now,
		Turns: []assistant.Turn{{N: 1, State: assistant.TurnRunning, StartedAt: now.Add(-3 * time.Hour)}}})
	archived := conv(assistant.Conversation{State: assistant.ConvWorking, UpdatedAt: now, Archived: true,
		Turns: []assistant.Turn{{N: 1, State: assistant.TurnRunning, StartedAt: now}}})
	plan := conv(assistant.Conversation{State: assistant.ConvIdle, UpdatedAt: ended, Title: strings.Repeat("a long request ", 10),
		Turns: []assistant.Turn{{N: 1, State: assistant.TurnSucceeded, StartedAt: now.Add(-2 * time.Minute), EndedAt: &ended, Proposals: 1}}})
	empty := conv(assistant.Conversation{State: assistant.ConvIdle, UpdatedAt: now})

	// A question about the finished clip.
	if _, err := todos.Upsert(ctx, a, models.Todo{Kind: models.TodoSpeakerUnnamed, Scope: "asset", AssetID: walk.ID, GroupKey: "speaker:" + walk.ID + ":s1@run",
		Question: "Who is Speaker 1 in “Garden walk”?", FreeText: true}); err != nil {
		t.Fatal(err)
	}

	s := build(t, a)
	if it := item(s, "upload:"+arriving.ID); it == nil || it.State != StateActive || it.Subtitle != "Garden film" || !near(it.StagePct, 0.5) {
		t.Fatalf("arriving: %+v", it)
	}
	if it := item(s, "upload:"+finished.ID); it == nil || it.State != StateDone || it.Title != "Garden walk" || it.Questions == nil || it.Questions.Count != 1 {
		t.Fatalf("finished upload: %+v", it)
	}
	if item(s, "asset:"+walk.ID) != nil {
		t.Fatal("an uploaded clip isn't listed twice")
	}
	if it := item(s, "ingest:"+watching.ID); it == nil || it.Machine != "laptop" || it.Subtitle != "Garden film, from the watched folder" || it.StageLabel != "Saving" ||
		!near(it.Pct, 0.45) || it.Title != "card-07" {
		t.Fatalf("watched folder ingest: %+v", it)
	}
	if it := item(s, "ingest:"+stalled.ID); it == nil || it.State != StateInterrupted || it.StageLabel != "Stopped at 50%" {
		t.Fatalf("stalled ingest: %+v", it)
	}
	if it := item(s, "ingest:"+elsewhere.ID); it == nil || it.Machine != "ins_unknown" || it.Subtitle != "Garden film" {
		t.Fatalf("an unknown machine: %+v", it)
	}
	if item(s, "ingest:"+byImport.ID) != nil || item(s, "ingest:"+covered.ID) != nil {
		t.Fatal("an import's or an upload's own ingest isn't listed again")
	}
	if it := item(s, "render:"+done.ID); it == nil || it.State != StateDone || it.Machine != "studio-mac" || !strings.HasSuffix(it.Subtitle, ", made by itself for the latest version") {
		t.Fatalf("finished render: %+v", it)
	}
	if it := item(s, "render:"+failed.ID); it == nil || it.State != StateFailed || !it.CanRetry || it.Problem != "The render didn't finish." || it.Error != "frame 12 failed" {
		t.Fatalf("failed render: %+v", it)
	}
	if it := item(s, "render:"+rendering.ID); it == nil || it.StageLabel != "Drawing frame 30 of 100" || !near(it.Pct, 0.3) || it.Machine != "studio-mac" || it.ETASeconds == nil {
		t.Fatalf("rendering: %+v", it)
	}
	if it := item(s, "render:"+waiting.ID); it == nil || it.State != StateWaiting || it.Stage != "queued" || it.Waiting == nil || it.Waiting.Code != jobs.WaitNoWorker {
		t.Fatalf("waiting render: %+v", it)
	}
	if it := item(s, "generate:"+making.ID); it == nil || it.StageLabel != "Google Gemini is making it" || it.Subtitle != "Garden film, made with Google Gemini, marked as AI-generated" {
		t.Fatalf("generating: %+v", it)
	}
	if item(s, "generate:"+orphan.ID) != nil {
		t.Fatal("a generation without its record has no row")
	}
	if it := item(s, "generate:"+madeFox.ID); it == nil || it.AssetID != fox.ID || it.Stage != "processing" || item(s, "asset:"+fox.ID) != nil {
		t.Fatalf("a generated clip being prepared: %+v", it)
	}
	if it := item(s, "import:"+imp.ID); it == nil || it.AssetID != imported.ID || it.Stage != "processing" || item(s, "asset:"+imported.ID) != nil {
		t.Fatalf("an imported clip being prepared: %+v", it)
	}
	if it := item(s, "job:"+voice.ID); it == nil || it.Subtitle != "Trailer" {
		t.Fatalf("narration: %+v", it)
	}
	if it := item(s, "assistant:"+broken); it == nil || it.State != StateFailed || it.Error != "claude exited 1" || it.Problem != "Claude Code stopped before it finished its reply." ||
		it.Link != "/p/"+garden.ID+"?ask="+broken {
		t.Fatalf("a stopped reply: %+v", it)
	}
	if it := item(s, "assistant:"+plan); it == nil || it.StageLabel != "1 proposal ready" || len([]rune(it.Title)) != len([]rune("Claude Code: "))+80 {
		t.Fatalf("a plan: %+v", it)
	}
	for _, id := range []string{interrupted, lost, archived, empty} {
		if item(s, "assistant:"+id) != nil {
			t.Errorf("conversation %s is not activity", id)
		}
	}

	// The trailer's view: only its own work.
	ts, err := Build(ctx, a, &SummaryIn{ProjectID: trailer.ID})
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, it := range ts.Items {
		keys[it.Key] = true
		if it.ProjectID != "" && it.ProjectID != trailer.ID {
			t.Errorf("another project's row in the trailer's view: %+v", it)
		}
	}
	if !keys["job:"+voice.ID] || !keys["asset:"+trailerClip.ID] || keys["upload:"+arriving.ID] || keys["ingest:"+watching.ID] || keys["render:"+done.ID] {
		t.Fatalf("trailer view: %v", keys)
	}

	// Finished work stays listed for ten minutes, or the window asked for.
	if item(s, "render:"+earlier.ID) == nil {
		t.Fatal("a render finished five minutes ago is listed")
	}
	short, err := Build(ctx, a, &SummaryIn{RecentMinutes: 2})
	if err != nil {
		t.Fatal(err)
	}
	if item(short, "render:"+earlier.ID) != nil || item(short, "render:"+done.ID) == nil {
		t.Fatal("a two-minute window keeps only the render finished a minute ago")
	}
}
