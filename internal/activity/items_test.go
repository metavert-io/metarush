package activity

import (
	"math"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/linkimport"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/uploads"
)

// Row builders read only what Build gathered, so they are tested here
// without a database: a builder with this machine's name and the workers,
// clips and projects a test puts in.

const here = "ins_here"

func testBuilder(t *testing.T) *builder {
	t.Helper()
	cfg := config.Default(t.TempDir())
	cfg.Installation = config.Ref{ID: here, Name: "studio-mac"}
	now := time.Now().UTC()
	return &builder{a: &app.App{Cfg: cfg}, now: now, recent: 10 * time.Minute, names: map[string]string{}, assets: map[string]*models.Asset{},
		projs: map[string]string{}, jobs: map[string]models.Job{}, out: &Summary{GeneratedAt: now, Waiting: []WaitGroup{}, Items: []Item{}, Workers: []WorkerBrief{}}}
}

// job is a job of a type in a state, updated half a minute ago (finished
// twenty seconds ago when it is over).
func job(typ, state string, change ...func(*models.Job)) models.Job {
	now := time.Now().UTC()
	j := models.Job{ID: ids.New(ids.Job), Type: typ, State: state, Payload: map[string]any{}, Result: map[string]any{},
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-30 * time.Second), NextRunAt: now.Add(-time.Minute)}
	if state == models.JobSucceeded || state == models.JobFailed || state == models.JobCanceled {
		f := now.Add(-20 * time.Second)
		j.FinishedAt = &f
	}
	for _, c := range change {
		c(&j)
	}
	return j
}

func running(pct float64, msg string) func(*models.Job) {
	return func(j *models.Job) {
		j.Progress = models.Progress{Pct: pct, Message: msg}
		j.Lease = &models.Lease{OwnerName: "studio-mac"}
		j.Attempts = []models.Attempt{{StartedAt: time.Now().UTC().Add(-40 * time.Second)}}
	}
}

func finishedAgo(d time.Duration) func(*models.Job) {
	return func(j *models.Job) {
		f := time.Now().UTC().Add(-d)
		j.FinishedAt = &f
	}
}

func payload(kv ...any) func(*models.Job) {
	return func(j *models.Job) {
		for i := 0; i+1 < len(kv); i += 2 {
			j.Payload[kv[i].(string)] = kv[i+1]
		}
	}
}

func result(kv ...any) func(*models.Job) {
	return func(j *models.Job) {
		for i := 0; i+1 < len(kv); i += 2 {
			j.Result[kv[i].(string)] = kv[i+1]
		}
	}
}

func rowFor(b *builder, key string) *Item {
	for i := range b.out.Items {
		if b.out.Items[i].Key == key {
			return &b.out.Items[i]
		}
	}
	return nil
}

func near(p *float64, v float64) bool { return p != nil && math.Abs(*p-v) < 1e-9 }

// known puts jobs where Build puts every job it read (failure advice reads them).
func known(b *builder, js ...models.Job) {
	for _, j := range js {
		b.jobs[j.ID] = j
	}
}

func onlineWorker(name string, types ...string) models.WorkerDevice {
	return models.WorkerDevice{ID: here, Name: name, JobTypes: types, Concurrency: 1, HeavyEnabled: true, LastHeartbeat: time.Now().UTC()}
}

// Link imports read as what they are doing: looking up, downloading with the
// rate and time left, adding, then the clip's own preparation; a failure
// says what happened and what to do, and offers the browser sign-in.
func TestImportRows(t *testing.T) {
	b := testBuilder(t)
	yt := func(j *models.Job) {
		j.Payload["url"], j.Payload["platform"], j.Payload["kind"] = "https://www.youtube.com/watch?v=dQw4w9WgXcQ", models.PlatformYouTube, "video"
	}
	clip := &models.Asset{ID: ids.New(ids.Asset), Name: "dQw4w9WgXcQ.mp4", Title: "Never gonna", ProjectIDs: []string{"prj_film"}}
	ready := &models.Asset{ID: ids.New(ids.Asset), Name: "x.mp4", Title: "Walk"}
	b.assets[clip.ID], b.assets[ready.ID] = clip, ready
	b.projs["prj_film"] = "Film"
	byAsset := map[string][]models.Job{
		clip.ID:  {job(jobs.TypeThumbnail, models.JobSucceeded), job(jobs.TypeTranscribe, models.JobRunning, running(0.5, ""))},
		ready.ID: {job(jobs.TypeThumbnail, models.JobSucceeded)},
	}
	lookup := job(jobs.TypeImport, models.JobRunning, yt, running(0, ""))
	download := job(jobs.TypeImport, models.JobRunning, yt, running(0, ""), func(j *models.Job) {
		j.Progress = models.Progress{Stage: linkimport.StageDownload, Pct: 0.5, Done: 50 << 20, Total: 100 << 20, Rate: 10 << 20, Message: "Downloading (50%)"}
	})
	adding := job(jobs.TypeImport, models.JobRunning, yt, func(j *models.Job) {
		j.Progress = models.Progress{Stage: linkimport.StageAdding, Pct: 0.5, Done: 5, Total: 10}
	})
	retry := job(jobs.TypeImport, models.JobQueued, yt, func(j *models.Job) { j.NextRunAt = time.Now().Add(5 * time.Minute) })
	pinned := job(jobs.TypeImport, models.JobQueued, yt, func(j *models.Job) { j.Installation = "ins_laptop" })
	b.workers = []models.WorkerDevice{{ID: "ins_laptop", Name: "laptop", LastHeartbeat: time.Now().Add(-time.Hour)}}
	advised := job(jobs.TypeImport, models.JobFailed, yt, payload("advice", map[string]any{"code": "bot_check", "message": "YouTube wants to check you're not a robot.",
		"next": "Import it with your browser's sign-in.", "retry": false, "signIn": true}))
	plain := job(jobs.TypeImport, models.JobFailed, yt, func(j *models.Job) { j.Error = "yt-dlp exited 1\nERROR: something odd" })
	playlist := job(jobs.TypeImport, models.JobSucceeded, payload("platform", models.PlatformYouTube, "kind", linkimport.KindPlaylist, "url", "https://www.youtube.com/playlist?list=PL1"),
		result("status", "expanded", "queued", 3))
	oldPlaylist := job(jobs.TypeImport, models.JobSucceeded, payload("kind", linkimport.KindPlaylist), result("status", "expanded"), finishedAgo(time.Hour))
	already := job(jobs.TypeImport, models.JobSucceeded, yt, result("status", "already", "assetId", clip.ID))
	preparing := job(jobs.TypeImport, models.JobSucceeded, yt, result("assetId", clip.ID), func(j *models.Job) { j.ProjectID = "prj_film" })
	added := job(jobs.TypeImport, models.JobSucceeded, payload("platform", models.PlatformWeb, "title", "Morning walk", "url", "https://example.com/walk.mp4"),
		func(j *models.Job) { j.AssetID = ids.New(ids.Asset) })
	done := job(jobs.TypeImport, models.JobSucceeded, yt, result("assetId", ready.ID))
	stale := job(jobs.TypeImport, models.JobSucceeded, yt, func(j *models.Job) { j.AssetID = ids.New(ids.Asset) }, finishedAgo(time.Hour))
	canceled := job(jobs.TypeImport, models.JobCanceled, yt)
	all := []models.Job{lookup, download, adding, retry, pinned, advised, plain, playlist, oldPlaylist, already, preparing, added, done, stale, canceled}
	known(b, all...)
	for _, j := range all {
		b.importItem(j, byAsset)
	}

	if it := rowFor(b, "import:"+lookup.ID); it == nil || it.Title != "YouTube video" || it.Subtitle != "Inbox, from YouTube" || it.Stage != linkimport.StageLookup ||
		it.StageLabel != "Looking up the video" || !near(it.Pct, 0.02) || it.Machine != "studio-mac" || it.StartedAt == nil || it.Link != links.Job(lookup.ID) {
		t.Fatalf("looking up: %+v", it)
	}
	if it := rowFor(b, "import:"+download.ID); it == nil || it.StageLabel != "Downloading (50%)" || !near(it.Pct, 0.04+0.86*0.5) || !near(it.StagePct, 0.5) ||
		it.ETASeconds == nil || !near(it.ETASeconds, 5) || it.BytesTotal != 100<<20 {
		t.Fatalf("downloading: %+v", it)
	}
	if it := rowFor(b, "import:"+adding.ID); it == nil || it.StageLabel != "Adding to MetaRush" || !near(it.Pct, 0.95) || it.BytesDone != 5 {
		t.Fatalf("adding: %+v", it)
	}
	if it := rowFor(b, "import:"+retry.ID); it == nil || it.State != StateWaiting || it.StageLabel != "Trying again after a problem" || !it.CanProcessNow || it.ProcessJobIDs[0] != retry.ID {
		t.Fatalf("retry: %+v", it)
	}
	if it := rowFor(b, "import:"+pinned.ID); it == nil || it.State != StateWaiting || it.Machine != "laptop" || it.Waiting.Code != jobs.WaitNoWorker || it.CanProcessNow {
		t.Fatalf("pinned: %+v", it)
	}
	if it := rowFor(b, "import:"+advised.ID); it == nil || it.State != StateFailed || it.Problem != "YouTube wants to check you're not a robot." ||
		it.Hint != "Import it with your browser's sign-in." || !it.SignInHelps || !it.CanRetry || !near(it.Pct, 1) {
		t.Fatalf("advice: %+v", it)
	}
	if it := rowFor(b, "import:"+plain.ID); it == nil || it.Problem != "The video couldn't be imported." || it.Hint == "" || !it.CanRetry || it.Error != "yt-dlp exited 1" {
		t.Fatalf("plain failure: %+v", it)
	}
	if it := rowFor(b, "import:"+playlist.ID); it == nil || it.Title != "Videos from YouTube" || it.State != StateDone || it.StageLabel != "Found 3 videos; each one is imported on its own" {
		t.Fatalf("playlist: %+v", it)
	}
	if it := rowFor(b, "import:"+already.ID); it == nil || it.Stage != "duplicate" || it.StageLabel != "Already in MetaRush" || it.Title != "Never gonna" || it.AssetID != clip.ID {
		t.Fatalf("already: %+v", it)
	}
	if it := rowFor(b, "import:"+preparing.ID); it == nil || it.State != StateActive || it.StageLabel != "Processing: transcript" || !near(it.Pct, 0.95+0.05*0.75) ||
		it.Subtitle != "Film, from YouTube" || it.Link != links.Asset("prj_film", clip.ID) {
		t.Fatalf("preparing: %+v", it)
	}
	if it := rowFor(b, "import:"+added.ID); it == nil || it.Title != "Morning walk" || it.Subtitle != "Inbox" || it.StageLabel != "Added" || it.State != StateDone || !near(it.Pct, 1) {
		t.Fatalf("added: %+v", it)
	}
	if it := rowFor(b, "import:"+done.ID); it == nil || it.State != StateDone || it.StageLabel != "Ready" || !near(it.Pct, 1) {
		t.Fatalf("ready: %+v", it)
	}
	for _, j := range []models.Job{oldPlaylist, stale, canceled} {
		if rowFor(b, "import:"+j.ID) != nil {
			t.Errorf("%s (%s) is no longer activity", j.ID, j.State)
		}
	}
	if importedAsset(done) != ready.ID || importedAsset(added) != added.AssetID {
		t.Fatal("importedAsset")
	}
}

// Generated clips: sending, Google making it (how long so far), waiting
// before asking again, declined, stopped, then the clip's preparation.
func TestGenerationRows(t *testing.T) {
	b := testBuilder(t)
	now := time.Now().UTC()
	g := models.Generation{ID: "gen_1", Operation: "shot", Params: models.GenParams{Seconds: 6}, Take: 1, ProviderTitle: "Google Gemini",
		Prompt: "A red fox trotting through fresh snow at dawn, low sun, long shadows, slow motion, wide shot on a long lens"}
	clip := &models.Asset{ID: ids.New(ids.Asset), Name: "fox.mp4", ProjectIDs: []string{}}
	b.assets[clip.ID] = clip
	byAsset := map[string][]models.Job{clip.ID: {job(jobs.TypeProxy, models.JobRunning, running(0.2, ""))}}
	making := job(jobs.TypeGenerate, models.JobRunning, running(0.3, ""), func(j *models.Job) { j.Progress.Stage = "making" })
	gMaking := g
	gMaking.Ticket = &models.GenTicket{ID: "int_1", SubmittedAt: now.Add(-2*time.Minute - 5*time.Second)}
	sending := job(jobs.TypeGenerate, models.JobRunning, running(0.1, ""))
	waiting := job(jobs.TypeGenerate, models.JobQueued, func(j *models.Job) { j.NextRunAt = now.Add(10 * time.Minute) })
	waitingSaid := job(jobs.TypeGenerate, models.JobQueued, func(j *models.Job) {
		j.NextRunAt = now.Add(10 * time.Minute)
		j.Progress.Message = "Google is busy; asking again at 10:42"
	})
	declined := job(jobs.TypeGenerate, models.JobFailed)
	gDeclined := g
	gDeclined.Outcome = &models.GenOutcome{State: models.GenRefused, Message: "Google's safety check declined this prompt.", Hint: "Change the prompt and try again."}
	broke := job(jobs.TypeGenerate, models.JobFailed, func(j *models.Job) { j.Error = "HTTP 500" })
	stopped := job(jobs.TypeGenerate, models.JobCanceled)
	stoppedLong := job(jobs.TypeGenerate, models.JobCanceled, finishedAgo(time.Hour))
	preparing := job(jobs.TypeGenerate, models.JobSucceeded, result("assetId", clip.ID))
	readyNow := job(jobs.TypeGenerate, models.JobSucceeded, result("assetId", ids.New(ids.Asset)))
	readyLong := job(jobs.TypeGenerate, models.JobSucceeded, result("assetId", ids.New(ids.Asset)), finishedAgo(time.Hour))
	queuedOther := job(jobs.TypeGenerate, models.JobRunning) // unknown state for the row: Stage "sending"
	gNoProvider := g
	gNoProvider.ProviderTitle = ""
	for _, c := range []struct {
		j models.Job
		g models.Generation
	}{{making, gMaking}, {sending, g}, {waiting, g}, {waitingSaid, g}, {declined, gDeclined}, {broke, g}, {stopped, g}, {stoppedLong, g}, {preparing, g},
		{readyNow, g}, {readyLong, g}, {queuedOther, gNoProvider}} {
		b.generationItem(c.j, c.g, byAsset)
	}
	if it := rowFor(b, "generate:"+making.ID); it == nil || it.StageLabel != "Google Gemini is making it (2 minutes so far)" || it.Machine != "studio-mac" ||
		!strings.HasPrefix(it.Title, "A 6-second shot: A red fox") || !strings.HasSuffix(it.Title, "…") || it.Subtitle != "Inbox, made with Google Gemini, marked as AI-generated" {
		t.Fatalf("making: %+v", it)
	}
	if it := rowFor(b, "generate:"+sending.ID); it == nil || it.Stage != "sending" || it.StageLabel != "Sending it to Google Gemini" {
		t.Fatalf("sending: %+v", it)
	}
	if it := rowFor(b, "generate:"+waiting.ID); it == nil || it.State != StateWaiting || it.StageLabel != "Waiting before it asks Google again" {
		t.Fatalf("waiting: %+v", it)
	}
	if it := rowFor(b, "generate:"+waitingSaid.ID); it == nil || it.StageLabel != "Google is busy; asking again at 10:42" {
		t.Fatalf("waiting with words: %+v", it)
	}
	if it := rowFor(b, "generate:"+declined.ID); it == nil || it.StageLabel != "Google Gemini declined it" || it.Problem != gDeclined.Outcome.Message || it.Hint != gDeclined.Outcome.Hint {
		t.Fatalf("declined: %+v", it)
	}
	if it := rowFor(b, "generate:"+broke.ID); it == nil || it.State != StateFailed || it.Problem != "The video couldn't be made." || it.Hint == "" || it.Error != "HTTP 500" {
		t.Fatalf("failed: %+v", it)
	}
	if it := rowFor(b, "generate:"+stopped.ID); it == nil || it.State != StateDone || it.StageLabel != "Stopped" {
		t.Fatalf("stopped: %+v", it)
	}
	if it := rowFor(b, "generate:"+preparing.ID); it == nil || it.State != StateActive || it.Stage != "processing" || it.AssetID != clip.ID || it.Name != "fox.mp4" {
		t.Fatalf("preparing: %+v", it)
	}
	if it := rowFor(b, "generate:"+readyNow.ID); it == nil || it.State != StateDone || it.StageLabel != "Ready in Sources" || !near(it.Pct, 1) {
		t.Fatalf("ready: %+v", it)
	}
	if it := rowFor(b, "generate:"+queuedOther.ID); it == nil || !strings.Contains(it.Subtitle, "made with the video service") {
		t.Fatalf("no provider name: %+v", it)
	}
	for _, j := range []models.Job{stoppedLong, readyLong} {
		if rowFor(b, "generate:"+j.ID) != nil {
			t.Errorf("%s is no longer activity", j.State)
		}
	}
	if generatedAsset(preparing) != clip.ID || generatedAsset(sending) != "" {
		t.Fatal("generatedAsset")
	}
}

// Quality checks, speaker detection, looks at a clip, fixed copies, fetches,
// Remotion work, storage moves and narration each read as what they do.
func TestJobRowsSayWhatTheyDo(t *testing.T) {
	b := testBuilder(t)
	clip := &models.Asset{ID: ids.New(ids.Asset), Name: "kitchen.mov", Title: "Kitchen chat", ProjectIDs: []string{"prj_film"}}
	b.assets[clip.ID] = clip
	b.projs["prj_film"] = "Film"
	inFilm := func(j *models.Job) { j.ProjectID = "prj_film" }
	onClip := func(j *models.Job) { j.AssetID = clip.ID }
	b.workers = []models.WorkerDevice{onlineWorker("studio-mac", jobs.TypeVisual, jobs.TypeDescribe, jobs.TypeFetch, jobs.TypePrepare, jobs.TypeVoiceover)}
	b.workers[0].Running = 1 // busy: queued jobs wait their turn

	qaRun := job(jobs.TypeQA, models.JobRunning, running(0.4, ""), inFilm)
	qaWait := job(jobs.TypeQA, models.JobQueued, inFilm)
	qaFail := job(jobs.TypeQA, models.JobFailed, inFilm)
	qaDone := job(jobs.TypeQA, models.JobSucceeded, inFilm, payload("renderId", "ren_1"), result("errors", int32(1), "warnings", int32(0), "reportId", "qar_1"))
	qaOld := job(jobs.TypeQA, models.JobSucceeded, finishedAgo(time.Hour))
	qaGone := job(jobs.TypeQA, models.JobCanceled)

	spkOld := job(jobs.TypeDiarize, models.JobFailed, onClip, func(j *models.Job) { j.CreatedAt = j.CreatedAt.Add(-time.Hour) })
	spkRun := job(jobs.TypeDiarize, models.JobRunning, running(0.25, ""), onClip)
	spkNone := job(jobs.TypeDiarize, models.JobSucceeded, func(j *models.Job) { j.AssetID = "ast_unknown" })
	spkFail := job(jobs.TypeDiarize, models.JobFailed, func(j *models.Job) { j.AssetID = "ast_other"; j.Error = "no audio stream" })

	lookRun := job(jobs.TypeVisual, models.JobRunning, running(0.2, ""), onClip)
	descWait := job(jobs.TypeDescribe, models.JobQueued, onClip, func(j *models.Job) { j.Heavy = true })
	lookFail := job(jobs.TypeVisual, models.JobFailed, func(j *models.Job) { j.AssetID = "ast_failed" })
	descDone := job(jobs.TypeDescribe, models.JobSucceeded, func(j *models.Job) { j.AssetID = "ast_desc" }, result("scenes", int32(3)))
	lookDone := job(jobs.TypeVisual, models.JobSucceeded, func(j *models.Job) { j.AssetID = "ast_look" }, result("summary", "two people and a whiteboard"))
	lookOld := job(jobs.TypeVisual, models.JobSucceeded, func(j *models.Job) { j.AssetID = "ast_old" }, finishedAgo(time.Hour))

	fixOld := job(jobs.TypePrepare, models.JobFailed, onClip, func(j *models.Job) { j.CreatedAt = j.CreatedAt.Add(-time.Hour) })
	fixRun := job(jobs.TypePrepare, models.JobRunning, running(0.6, ""), onClip, payload("doing", "Steadying the camera"))
	fixPic := job(jobs.TypePrepare, models.JobQueued, inFilm, payload("resourceId", "res_logo", "title", "Logo"))
	fixFail := job(jobs.TypePrepare, models.JobFailed, func(j *models.Job) { j.AssetID = "ast_fixfail" })
	fixDone := job(jobs.TypePrepare, models.JobSucceeded, func(j *models.Job) { j.AssetID = "ast_fixed" })
	fixLong := job(jobs.TypePrepare, models.JobSucceeded, func(j *models.Job) { j.AssetID = "ast_fixlong" }, finishedAgo(time.Hour))

	repo := job(jobs.TypeFetch, models.JobRunning, running(0.1, ""), payload("kind", "repo", "url", "https://github.com/acme/widget"))
	picture := job(jobs.TypeFetch, models.JobQueued, inFilm, payload("kind", "picture", "url", "https://example.com/logo.png"))
	page := job(jobs.TypeFetch, models.JobFailed, inFilm, payload("kind", "page", "url", "https://example.com/pricing"), func(j *models.Job) { j.Error = "net::ERR_NAME_NOT_RESOLVED" })
	pageDone := job(jobs.TypeFetch, models.JobSucceeded, inFilm, payload("kind", "page", "url", "https://example.com/pricing"),
		result("message", "added 1 page", "resourceIds", bson.A{"res_page"}))
	fetchOld := job(jobs.TypeFetch, models.JobSucceeded, finishedAgo(time.Hour), payload("kind", "page", "url", "https://example.com/"))
	fetchGone := job(jobs.TypeFetch, models.JobCanceled, payload("kind", "page", "url", "https://example.com/"))

	rmWait := job(jobs.TypeRemotionRefresh, models.JobQueued, inFilm)
	rmStop := job(jobs.TypeRemotionPush, models.JobCanceled, inFilm)
	rmDone := job(jobs.TypeRemotionRefresh, models.JobSucceeded, inFilm, payload("source", "rms_1"), result("version", "rmv_2"))
	rmOld := job(jobs.TypeRemotionRefresh, models.JobFailed, inFilm, finishedAgo(time.Hour))

	stQueued := job(jobs.TypeStorageMove, models.JobQueued)
	stDone := job(jobs.TypeStorageMove, models.JobSucceeded)
	stOld := job(jobs.TypeStorageMove, models.JobSucceeded, finishedAgo(time.Hour))

	nWait := job(jobs.TypeVoiceover, models.JobQueued, inFilm, func(j *models.Job) { j.Installation = "ins_gone" })
	nDone := job(jobs.TypeVoiceover, models.JobSucceeded, inFilm, payload("title", "Scratch voice", "revisionNumber", int64(3)))
	nOld := job(jobs.TypeVoiceover, models.JobSucceeded, finishedAgo(time.Hour))
	nGone := job(jobs.TypeVoiceover, models.JobCanceled)

	other := []models.Job{qaRun, qaWait, qaFail, qaDone, qaOld, qaGone, spkOld, spkRun, spkNone, spkFail, lookRun, descWait, lookFail, descDone, lookDone, lookOld,
		fixOld, fixRun, fixPic, fixFail, fixDone, fixLong, repo, picture, page, pageDone, fetchOld, fetchGone, rmWait, rmStop, rmDone, rmOld, stQueued, stDone, stOld,
		nWait, nDone, nOld, nGone}
	known(b, other...)
	b.qa(other)
	b.speakers(other)
	b.looking(other)
	b.fixing(other)
	b.fetches(other)
	b.remotionWork(other)
	b.storageMoves(other)
	b.narration(nil, other)

	check := func(name string, j models.Job, ok func(it *Item) bool) {
		t.Helper()
		if it := rowFor(b, "job:"+j.ID); it == nil || !ok(it) {
			t.Errorf("%s: %+v", name, it)
		}
	}
	gone := func(name string, js ...models.Job) {
		t.Helper()
		for _, j := range js {
			if rowFor(b, "job:"+j.ID) != nil {
				t.Errorf("%s: %s is no longer activity", name, j.State)
			}
		}
	}
	check("qa running", qaRun, func(it *Item) bool {
		return it.StageLabel == "Checking" && it.Machine == "studio-mac" && near(it.Pct, 0.4)
	})
	check("qa waiting", qaWait, func(it *Item) bool {
		return it.State == StateWaiting && it.Waiting.Code == jobs.WaitNoWorker && !it.CanProcessNow && it.ProcessJobIDs[0] == qaWait.ID
	})
	check("qa failed", qaFail, func(it *Item) bool { return it.Problem == "The quality check didn't finish." && it.CanRetry })
	check("qa done", qaDone, func(it *Item) bool {
		return it.StageLabel == "1 problem to fix" && it.Link == links.QA("prj_film", "ren_1", "qar_1") && it.RenderID == "ren_1"
	})
	gone("qa", qaOld, qaGone)

	check("speakers running", spkRun, func(it *Item) bool {
		return it.Title == "Finding who's speaking in “Kitchen chat”" && it.StageLabel == "Listening" && it.ETASeconds != nil && it.Subtitle == "Film"
	})
	gone("an older run of the same clip", spkOld)
	check("speakers none", spkNone, func(it *Item) bool {
		return it.StageLabel == "No voices found" && it.Title == "Who's speaking in “the clip”"
	})
	check("speakers failed", spkFail, func(it *Item) bool {
		return it.StageLabel == "Could not tell the voices apart" && it.Problem == "MetaRush couldn't tell the voices apart in this clip." &&
			strings.Contains(it.Hint, "no sound")
	})

	check("look running", lookRun, func(it *Item) bool {
		return it.Title == "Looking at what's on screen in “Kitchen chat”" && it.StageLabel == "Looking" && it.Link == links.AssetOnScreen("prj_film", clip.ID)
	})
	check("describe waiting", descWait, func(it *Item) bool {
		return it.Title == "Describing the scenes of “Kitchen chat”" && it.State == StateWaiting && it.Waiting.Code == jobs.WaitBusy && it.Machine == "studio-mac"
	})
	check("look failed", lookFail, func(it *Item) bool {
		return it.Problem == stepProblems["visual"] && it.StageLabel == "Could not look at what's on screen"
	})
	check("described", descDone, func(it *Item) bool {
		return it.StageLabel == "Described 3 scenes" && it.Title == "The scenes of “the clip”"
	})
	check("looked", lookDone, func(it *Item) bool { return it.StageLabel == "Two people and a whiteboard" })
	gone("look", lookOld)

	check("fix running", fixRun, func(it *Item) bool {
		return it.Title == "Steadying the camera in “Kitchen chat”" && it.Kind == "fix" && it.Link == links.Asset("prj_film", clip.ID) && it.ETASeconds != nil
	})
	gone("an older fix of the same clip", fixOld)
	check("fixing a picture", fixPic, func(it *Item) bool {
		return it.Title == "Fixing in “Logo”" && it.Link == "/p/prj_film?tab=resources" && it.State == StateWaiting && it.Machine == "studio-mac"
	})
	check("fix failed", fixFail, func(it *Item) bool {
		return it.StageLabel == "The fixed copy couldn't be made" && it.CanRetry && it.Title == "Fixing in “the clip”"
	})
	check("fixed", fixDone, func(it *Item) bool {
		return it.Title == "The fixed copy of “the clip” is ready" && it.StageLabel == "Everything after uses it now"
	})
	gone("fix", fixLong)

	check("repo", repo, func(it *Item) bool {
		return it.Title == "Getting acme/widget from GitHub" && it.Subtitle == "Workspace library" && it.StageLabel == "Starting" && it.StartedAt != nil
	})
	check("picture", picture, func(it *Item) bool {
		return it.Title == "Getting a picture from example.com" && it.State == StateWaiting && it.Subtitle == "Film"
	})
	check("page failed", page, func(it *Item) bool {
		return it.StageLabel == "Could not get a snapshot of example.com/pricing" && it.Problem == "Couldn't get a snapshot of example.com/pricing." && it.CanRetry
	})
	check("page", pageDone, func(it *Item) bool {
		return it.Title == "A snapshot of example.com/pricing" && it.StageLabel == "Added 1 page" && it.Link == links.Resource("prj_film", "res_page")
	})
	gone("fetch", fetchOld, fetchGone)

	check("remotion waiting", rmWait, func(it *Item) bool {
		return it.State == StateWaiting && it.StageLabel == "Waiting" && it.Title == "Refreshing a Remotion project from its own steps"
	})
	check("remotion stopped", rmStop, func(it *Item) bool {
		return it.State == StateFailed && it.Problem == "It didn't finish." && it.Kind == "remotion-push" && !it.CanRetry
	})
	check("remotion done", rmDone, func(it *Item) bool {
		return it.StageLabel == "Done" && it.Link == links.Remotion("prj_film", "rms_1", "rmv_2")
	})
	gone("remotion", rmOld)

	check("storage moved", stDone, func(it *Item) bool { return it.StageLabel == "Moved" && it.State == StateDone })
	gone("storage", stQueued, stOld)

	check("narration waiting", nWait, func(it *Item) bool {
		return it.State == StateWaiting && it.Title == "Narration" && it.Link == links.Job(nWait.ID)
	})
	check("narration done", nDone, func(it *Item) bool {
		return it.StageLabel == "Ready to review" && it.Title == "Scratch voice" && it.Subtitle == "Film, version 3"
	})
	gone("narration", nOld, nGone)
}

// A look at a clip still being prepared is one more step of the clip's row.
func TestLookIsAStepOfAClipBeingPrepared(t *testing.T) {
	b := testBuilder(t)
	clip := &models.Asset{ID: ids.New(ids.Asset), Name: "kitchen.mov", Title: "Kitchen chat"}
	b.assets[clip.ID] = clip
	b.workers = []models.WorkerDevice{onlineWorker("studio-mac", jobs.TypeDescribe)}
	b.assetItem(clip.ID, []models.Job{job(jobs.TypeProxy, models.JobRunning, running(0.5, ""), func(j *models.Job) { j.AssetID = clip.ID })})
	look := job(jobs.TypeVisual, models.JobRunning, running(0.2, ""), func(j *models.Job) { j.AssetID = clip.ID })
	desc := job(jobs.TypeDescribe, models.JobQueued, func(j *models.Job) { j.AssetID = clip.ID; j.NextRunAt = time.Now().Add(time.Minute) })
	b.looking([]models.Job{look, desc})
	row := rowFor(b, "asset:"+clip.ID)
	if row == nil || len(row.Steps) != 3 || row.StageLabel != "Processing: preview copy, what's on screen" || !row.CanProcessNow || row.ProcessJobIDs[0] != desc.ID {
		t.Fatalf("row %+v", row)
	}
	if rowFor(b, "job:"+look.ID) != nil || rowFor(b, "job:"+desc.ID) != nil {
		t.Fatal("the looks have no rows of their own")
	}
	// A failed look is a step to retry; a running one on a row with no
	// "Processing:" label yet starts it.
	b2 := testBuilder(t)
	it := Item{Key: "asset:x", Category: CatProcessing, Kind: "asset", AssetID: "x", Stage: "processing", StageLabel: "Processing"}
	b2.addStep(&it, Step{Kind: "visual", Label: "What's on screen", State: "failed", JobID: "job_f"})
	b2.addStep(&it, Step{Kind: "describe", Label: "Scene descriptions", State: "running", Pct: 0.5, Machine: "mini"})
	if !it.CanRetry || it.RetryJobIDs[0] != "job_f" || it.StageLabel != "Processing: scene descriptions" || it.Machine != "mini" || !near(it.Pct, 0.25) {
		t.Fatalf("steps %+v", it)
	}
}

// Browser uploads after a reload: sending (rate, time left), interrupted,
// checking and saving, skipped as a copy, processing, added, failed.
func TestUploadRows(t *testing.T) {
	b := testBuilder(t)
	now := time.Now().UTC()
	clip := &models.Asset{ID: ids.New(ids.Asset), Name: "IMG_1.MOV", Title: "Kitchen chat", ProjectIDs: []string{"prj_film"}}
	b.assets[clip.ID] = clip
	b.projs["prj_film"] = "Film"
	rec := func(change func(r *uploads.Record)) uploads.Record {
		r := uploads.Record{ID: ids.New(ids.Upload), Name: "IMG_1.MOV", Size: 1000, CreatedAt: now.Add(-time.Minute), UpdatedAt: now, LastProgressAt: now}
		change(&r)
		return r
	}
	sending := rec(func(r *uploads.Record) {
		r.State, r.Offset, r.Rate, r.ProjectID = uploads.StateReceiving, 400, 100, "prj_film"
	})
	cut := rec(func(r *uploads.Record) { r.State, r.Offset, r.Disconnected = uploads.StateReceiving, 250, true })
	checking := rec(func(r *uploads.Record) {
		r.State, r.Stage, r.StageDone, r.StageTotal, r.Rate = uploads.StateIngesting, uploads.StageFingerprinting, 50, 100, 10
	})
	copyOf := rec(func(r *uploads.Record) {
		r.State, r.Result = uploads.StateDone, &ingest.Result{Status: "duplicate", Duplicate: &models.DuplicateOf{AssetID: clip.ID, Title: "Kitchen chat"}}
	})
	copyBare := rec(func(r *uploads.Record) { r.State, r.Result = uploads.StateDone, &ingest.Result{Status: "duplicate"} })
	processing := rec(func(r *uploads.Record) {
		r.State, r.Result = uploads.StateDone, &ingest.Result{Status: "ingested", AssetID: clip.ID}
	})
	added := rec(func(r *uploads.Record) {
		r.State, r.Result = uploads.StateDone, &ingest.Result{Status: "ingested", AssetID: ids.New(ids.Asset)}
	})
	longAgo := rec(func(r *uploads.Record) {
		r.State, r.Result, r.UpdatedAt = uploads.StateDone, &ingest.Result{Status: "ingested", AssetID: ids.New(ids.Asset)}, now.Add(-time.Hour)
	})
	failedPlain := rec(func(r *uploads.Record) { r.State, r.Error = uploads.StateFailed, "the disk is full" })
	failedCode := rec(func(r *uploads.Record) { r.State, r.Error = uploads.StateFailed, "E_INTERNAL: upload store refused" })
	canceled := rec(func(r *uploads.Record) { r.State = uploads.StateCanceled })
	byAsset := map[string][]models.Job{clip.ID: {job(jobs.TypeThumbnail, models.JobSucceeded), job(jobs.TypeProxy, models.JobRunning, running(0.5, ""))}}
	for _, r := range []uploads.Record{sending, cut, checking, copyOf, copyBare, processing, added, longAgo, failedPlain, failedCode, canceled} {
		b.uploadItem(r, byAsset)
	}
	row := func(r uploads.Record) *Item { return rowFor(b, "upload:"+r.ID) }
	if it := row(sending); it == nil || it.Stage != uploads.StageSending || it.State != StateActive || !near(it.Pct, 0.4*0.85) || !near(it.StagePct, 0.4) ||
		!near(it.ETASeconds, 6) || it.Subtitle != "Film" || it.Machine != "studio-mac" || it.Title != "IMG_1" {
		t.Fatalf("sending: %+v", it)
	}
	if it := row(cut); it == nil || it.State != StateInterrupted || it.StageLabel != "Interrupted at 25%" || it.Rate != 0 || it.ETASeconds != nil {
		t.Fatalf("interrupted: %+v", it)
	}
	if it := row(checking); it == nil || it.StageLabel != "Looking for copies" || !near(it.Pct, 0.87+0.05*0.5) || !near(it.ETASeconds, 5) {
		t.Fatalf("checking: %+v", it)
	}
	if it := row(copyOf); it == nil || it.Stage != "duplicate" || it.StageLabel != "Skipped: identical to Kitchen chat" || it.AssetID != clip.ID || !near(it.Pct, 1) {
		t.Fatalf("a copy: %+v", it)
	}
	if it := row(copyBare); it == nil || it.StageLabel != "Skipped: already in MetaRush" {
		t.Fatalf("a copy, no details: %+v", it)
	}
	if it := row(processing); it == nil || it.State != StateActive || it.Title != "Kitchen chat" || !near(it.Pct, 0.95+0.05*0.75) || it.BytesDone != 1000 ||
		it.Link != links.Asset("prj_film", clip.ID) {
		t.Fatalf("processing: %+v", it)
	}
	if it := row(added); it == nil || it.State != StateDone || it.StageLabel != "Added" || !near(it.Pct, 1) {
		t.Fatalf("added: %+v", it)
	}
	if it := row(failedPlain); it == nil || it.State != StateFailed || it.Problem != "The upload didn't finish." || it.Hint != "The disk is full." {
		t.Fatalf("failed: %+v", it)
	}
	if it := row(failedCode); it == nil || it.Hint != "Add the file again to try once more. Details says what went wrong." {
		t.Fatalf("failed with a code: %+v", it)
	}
	if row(longAgo) != nil || row(canceled) != nil {
		t.Fatal("old and canceled uploads are not activity")
	}
}

// Several failed steps say what couldn't be made together; a clip whose
// steps are about to start counts as active.
func TestApplySteps(t *testing.T) {
	b := testBuilder(t)
	it := Item{}
	steps := []Step{{Kind: "transcribe", Label: "Transcript", State: "failed", JobID: "job_t", Error: "boom"}, {Kind: "proxy", Label: "Preview copy", State: "failed", JobID: "job_p"}}
	if !b.applySteps(&it, steps, b.now) || it.State != StateFailed || it.Problem != "Some of it couldn't be made: transcript and preview copy." || len(it.RetryJobIDs) != 2 ||
		it.Error != "Transcript: boom" {
		t.Fatalf("two failures: %+v", it)
	}
	starting := Item{}
	if !b.applySteps(&starting, []Step{{Kind: "proxy", State: "queued", Waiting: &jobs.WaitReason{Code: jobs.WaitStarting}}}, b.now) || starting.State != StateActive {
		t.Fatalf("about to start: %+v", starting)
	}
	if b.applySteps(&Item{}, nil, b.now) {
		t.Fatal("no steps, nothing to show")
	}
	if b.applySteps(&Item{}, []Step{{Kind: "proxy", State: "done", Pct: 1}}, b.now.Add(-time.Hour)) {
		t.Fatal("finished long ago, nothing to show")
	}
	narration := &models.Asset{ID: ids.New(ids.Asset), Tags: []string{"voiceover", "tts"}}
	b.assets[narration.ID] = narration
	b.assetItem(narration.ID, []models.Job{job(jobs.TypeWaveform, models.JobRunning)})
	b.assetItem("ast_unknown", []models.Job{job(jobs.TypeWaveform, models.JobRunning)})
	if rowFor(b, "asset:"+narration.ID) != nil {
		t.Fatal("narration audio has no row of its own")
	}
	if it := rowFor(b, "asset:ast_unknown"); it == nil || it.Title != "ast_unknown" {
		t.Fatalf("an unknown clip: %+v", it)
	}
	if b.where("") != "Inbox" || b.where("prj_unknown") != "a project" || b.whereResources("") != "Workspace library" {
		t.Fatal("where")
	}
}

// The summary's counts, wait groups, overall progress and order.
func TestFinishCountsGroupsAndOrders(t *testing.T) {
	b := testBuilder(t)
	now := b.now
	pct := 15
	battery := &jobs.WaitReason{Code: jobs.WaitBattery, Machine: "laptop", BatteryPercent: &pct, CanProcessNow: true}
	half := 0.5
	b.out.Items = []Item{
		{Key: "upload:1", Category: CatUpload, State: StateActive, Stage: "sending", Pct: &half, UpdatedAt: now.Add(-time.Minute)},
		{Key: "upload:2", Category: CatUpload, State: StateWaiting, Stage: "processing", UpdatedAt: now},
		{Key: "asset:1", Category: CatProcessing, State: StateWaiting, Steps: []Step{{Kind: "transcribe", State: "paused", JobID: "j1", Waiting: battery},
			{Kind: "transcribe", State: "paused", JobID: "j2", Waiting: battery}, {Kind: "proxy", State: "queued", JobID: "j3", Waiting: &jobs.WaitReason{Code: jobs.WaitStarting}}}},
		{Key: "render:1", Category: CatRender, State: StateActive, UpdatedAt: now},
		{Key: "job:qa", Category: CatQA, State: StateWaiting, Kind: "qa", JobID: "j4", Waiting: &jobs.WaitReason{Code: jobs.WaitBusy, Machine: "mini"}},
		{Key: "assistant:1", Category: CatAssistant, State: StateActive},
		{Key: "job:n", Category: CatNarration, State: StateActive, StageLabel: "Making the AI voice for line 2"},
		{Key: "job:s", Category: CatStorage, State: StateActive},
		{Key: "generate:1", Category: CatGenerate, State: StateActive, Stage: "making"},
		{Key: "generate:2", Category: CatGenerate, State: StateActive, Stage: "processing"},
		{Key: "import:1", Category: CatImport, State: StateInterrupted, Stage: "download"},
		{Key: "import:2", Category: CatImport, State: StateActive, Stage: "processing"},
		{Key: "failed", Category: CatRender, State: StateFailed, UpdatedAt: now},
		{Key: "done", Category: CatRender, State: StateDone, UpdatedAt: now},
	}
	b.finish()
	c := b.out.Counts
	if c.Uploads != 1 || c.Processing != 4 || c.Renders != 1 || c.QA != 1 || c.Assistant != 1 || c.Narration != 1 || c.Storage != 1 || c.Generating != 1 ||
		c.Imports != 1 || c.Failed != 1 || c.Done != 1 || c.Active != 12 || c.Waiting != 3 || c.Paused != 2 {
		t.Fatalf("counts %+v", c)
	}
	if len(b.out.Waiting) != 2 || b.out.Waiting[0].Count != 2 || b.out.Waiting[0].Text != "2 transcriptions paused: battery at 15% on laptop" ||
		b.out.Waiting[1].Text != "1 quality check waiting its turn on mini" {
		t.Fatalf("groups %+v", b.out.Waiting)
	}
	if !near(b.out.Progress, 0.5/12) { // items without a known share count as not started
		t.Fatalf("progress %v", b.out.Progress)
	}
	if b.out.Items[0].Key != "import:1" || b.out.Items[len(b.out.Items)-1].Key != "done" || b.out.Items[len(b.out.Items)-2].Key != "failed" {
		t.Fatalf("order: first %s, last %s", b.out.Items[0].Key, b.out.Items[len(b.out.Items)-1].Key)
	}
	if !strings.Contains(b.out.Headline, "2 transcriptions paused: battery at 15% on laptop") || !strings.HasPrefix(b.out.Headline, "Uploading 1 file") {
		t.Fatalf("headline %q", b.out.Headline)
	}
}
