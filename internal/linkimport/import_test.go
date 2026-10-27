package linkimport_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/activity"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/linkimport"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeytdlp"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/worker"
)

func TestMain(m *testing.M) {
	fakeytdlp.MaybeRun() // this test binary doubles as the fake yt-dlp
	os.Exit(m.Run())
}

// importApp is a throwaway installation whose downloader is the fake.
func importApp(t *testing.T) (*app.App, string) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		if _, err := os.Stat("/opt/homebrew/bin/ffmpeg"); err != nil {
			t.Skip("ffmpeg is needed for import tests")
		}
	}
	a := testapp.New(t)
	logFile := filepath.Join(t.TempDir(), "ytdlp-calls.log")
	t.Setenv("METARUSH_FAKE_YTDLP_LOG", logFile)
	a.Cfg.Tools.YtDlp = fakeytdlp.Write(t)
	a.Tools = tools.New(a.Cfg.Tools)
	return a, logFile
}

func call[O any](t *testing.T, a *app.App, op string, in any) (*O, error) {
	t.Helper()
	return ops.CallTyped[O](context.Background(), a, op, in, ops.Caller{})
}

// runImports runs this machine's queued imports the way the daemon's worker
// does, one at a time (inline worker, as `metarush import --wait` does).
func runImports(t *testing.T, a *app.App, ids []string) {
	t.Helper()
	for _, id := range ids {
		w := worker.New(a)
		w.OnlyJobID = id
		w.Log = func(string, ...any) {}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		if err := w.Run(ctx); err != nil {
			cancel()
			t.Fatalf("run import %s: %v", id, err)
		}
		cancel()
	}
}

func job(t *testing.T, a *app.App, id string) *models.Job {
	t.Helper()
	j, err := a.Queue.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestImportYouTubeLink(t *testing.T) {
	a, logFile := importApp(t)
	ctx := context.Background()
	p, err := call[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Reactions"})
	if err != nil {
		t.Fatal(err)
	}
	pid := p.Project.ID
	out, err := call[linkimport.ImportOut](t, a, "ingest.url", linkimport.ImportIn{URLs: []string{"https://youtu.be/ccvideo0001"}, ProjectID: pid})
	if err != nil {
		t.Fatal(err)
	}
	if out.Queued != 1 || len(out.JobIDs) != 1 || out.Imports[0].Status != "queued" || out.Running {
		t.Fatalf("one import queued (no daemon running here): %+v", out)
	}
	if !strings.Contains(out.Note, "open MetaRush to start it") || !strings.Contains(out.Note, a.Cfg.Installation.Name) {
		t.Errorf("without a running MetaRush the note says how to start the import: %q", out.Note)
	}
	j := job(t, a, out.JobIDs[0])
	if j.Installation != a.Cfg.Installation.ID || j.Type != jobs.TypeImport || j.ProjectID != pid {
		t.Fatalf("the import is pinned to this machine: %+v", j)
	}
	// Another machine's worker never takes it.
	other, err := a.Queue.Lease(ctx, jobs.Worker{ID: ids.New(ids.Installation), Name: "other", JobTypes: []string{jobs.TypeImport}})
	if err != nil || other != nil {
		t.Fatalf("another machine leased a pinned import: %v %v", other, err)
	}
	if w := jobs.Why(*j, []models.WorkerDevice{{ID: a.Cfg.Installation.ID, Name: "test-machine"}}, time.Now()); w == nil || w.Code != jobs.WaitNoWorker || !strings.Contains(w.Text, "test-machine") {
		t.Fatalf("waits for its machine, by name: %+v", w)
	}

	runImports(t, a, out.JobIDs)
	j = job(t, a, out.JobIDs[0])
	if j.State != models.JobSucceeded {
		t.Fatalf("import %s: %s", j.State, j.Error)
	}
	assetID, _ := j.Result["assetId"].(string)
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as); err != nil {
		t.Fatal(err)
	}
	s := as.Source
	if s == nil || s.Platform != "youtube" || s.VideoID != "ccvideo0001" || s.Handle != "@fakechannel" || s.Uploader != "Fake Channel" || s.UploadDate != "2024-03-12" ||
		s.Tool != "yt-dlp" || s.ToolVersion != fakeytdlp.Version || s.URL != "https://www.youtube.com/watch?v=ccvideo0001" || !strings.Contains(s.License, "Creative Commons") || s.SignedIn {
		t.Fatalf("provenance on the clip: %+v", s)
	}
	if as.DisplayTitle() != "Fake video ccvideo0001" || as.Name != "Fake video ccvideo0001.mp4" || as.State != models.AssetAvailable || len(as.ProjectIDs) != 1 || as.ProjectIDs[0] != pid {
		t.Fatalf("the clip is titled after the original and in the project: %q %q %s %v", as.DisplayTitle(), as.Name, as.State, as.ProjectIDs)
	}
	// The normal pipeline ran: derivative jobs for the clip, and no leftover download.
	var derived []models.Job
	_ = a.DB.Find(ctx, store.CJobs, a.WorkspaceID(), bson.M{"assetId": assetID, "type": bson.M{"$in": bson.A{"proxy", "transcribe", "thumbnail"}}}, &derived)
	if len(derived) < 2 {
		t.Fatalf("preview and transcript jobs were queued like for a dropped file: %d", len(derived))
	}
	if _, err := os.Stat(linkimport.Dir(a, j.ID)); !os.IsNotExist(err) {
		t.Errorf("the download folder is removed once the clip is added")
	}
	// Activity: one row for the import (not a second one for the clip).
	sum, err := activity.Build(ctx, a, &activity.SummaryIn{})
	if err != nil {
		t.Fatal(err)
	}
	var rows []activity.Item
	for _, it := range sum.Items {
		if it.AssetID == assetID || it.JobID == j.ID {
			rows = append(rows, it)
		}
	}
	if len(rows) != 1 || rows[0].Category != activity.CatImport || rows[0].Title != "Fake video ccvideo0001" || len(rows[0].Steps) == 0 {
		t.Fatalf("one import row with the clip's processing steps: %+v", rows)
	}
	// The yt-dlp calls: typed options, the link after "--", no cookie file.
	b, _ := os.ReadFile(logFile)
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var argv []string
		_ = json.Unmarshal([]byte(line), &argv)
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "--cookies ") || strings.Contains(joined, "--cookies-from-browser") {
			t.Errorf("no browser sign-in was asked for, and never a cookie file: %s", joined)
		}
		if strings.Contains(joined, "-J") && (!strings.Contains(joined, "--ignore-config") || !strings.Contains(joined, " -- https://www.youtube.com/watch?v=ccvideo0001")) {
			t.Errorf("lookup arguments: %s", joined)
		}
	}
	// The same link again: already in MetaRush, nothing downloaded.
	again, err := call[linkimport.ImportOut](t, a, "ingest.url", linkimport.ImportIn{URLs: []string{"https://www.youtube.com/watch?v=ccvideo0001&t=5"}})
	if err != nil || again.Already != 1 || again.Imports[0].AssetID != assetID || again.Queued != 0 {
		t.Fatalf("a second paste finds the clip: %+v %v", again, err)
	}
}

func TestImportNeedsTheBrowserSignIn(t *testing.T) {
	a, logFile := importApp(t)
	ctx := context.Background()
	link := "https://www.youtube.com/watch?v=bot-check01"
	out, err := call[linkimport.ImportOut](t, a, "ingest.url", linkimport.ImportIn{URLs: []string{link}})
	if err != nil {
		t.Fatal(err)
	}
	runImports(t, a, out.JobIDs)
	j := job(t, a, out.JobIDs[0])
	if j.State != models.JobFailed || j.AttemptCount != 1 {
		t.Fatalf("a bot check fails at once (never retried by itself): %s after %d attempts", j.State, j.AttemptCount)
	}
	adv := linkimport.AdviceOf(j)
	if adv == nil || adv.Code != linkimport.AdviceSignIn || !adv.SignIn || !strings.Contains(adv.Message, "not a bot") {
		t.Fatalf("advice: %+v", adv)
	}
	sum, _ := activity.Build(ctx, a, &activity.SummaryIn{})
	found := false
	for _, it := range sum.Items {
		if it.JobID == j.ID {
			found = true
			if it.State != activity.StateFailed || !it.SignInHelps || it.Problem == "" || it.Hint == "" || !it.CanRetry || strings.Contains(it.Problem, "ERROR") {
				t.Fatalf("activity says it plainly and offers the sign-in: %+v", it)
			}
		}
	}
	if !found {
		t.Fatal("the failed import is in the activity summary")
	}
	var n models.Notification
	if err := a.DB.C(store.CNotifications).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "groupKey": "import:" + j.ID}).Decode(&n); err != nil || n.Kind != models.NotifyImportFailed || !strings.Contains(n.Body, "not a bot") {
		t.Fatalf("a notification tells the person: %+v %v", n, err)
	}
	// "Use my browser's sign-in": the same import again, opted in for Chrome.
	retry, err := call[linkimport.ImportOut](t, a, "ingest.url", linkimport.ImportIn{URLs: []string{link}, BrowserSignIn: "chrome"})
	if err != nil || retry.Queued != 1 || retry.JobIDs[0] != j.ID {
		t.Fatalf("the same job is queued again: %+v %v", retry, err)
	}
	runImports(t, a, retry.JobIDs)
	j = job(t, a, j.ID)
	if j.State != models.JobSucceeded {
		t.Fatalf("with the browser sign-in: %s %s", j.State, j.Error)
	}
	var as models.Asset
	_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), j.Result["assetId"].(string), &as)
	if as.Source == nil || !as.Source.SignedIn {
		t.Fatalf("the clip records that the browser sign-in was used: %+v", as.Source)
	}
	cfg, err := config.Load(a.Cfg.Home())
	if err != nil || cfg.Imports.Browser != "chrome" {
		t.Fatalf("the browser is remembered on this machine: %+v %v", cfg.Imports, err)
	}
	b, _ := os.ReadFile(logFile)
	if !strings.Contains(string(b), `"--cookies-from-browser","chrome"`) || strings.Contains(string(b), `"--cookies",`) {
		t.Fatalf("yt-dlp read the browser's cookies itself; no cookie file: %s", b)
	}
	// Opt-in is per import: the next one does not use the browser by itself.
	next, _ := call[linkimport.ImportOut](t, a, "ingest.url", linkimport.ImportIn{URLs: []string{"https://youtu.be/plainvideo1"}})
	nj := job(t, a, next.JobIDs[0])
	if p := linkimport.PayloadOf(nj); p.Browser != "" {
		t.Fatalf("the remembered browser is only a suggestion: %+v", p)
	}
}

func TestImportRefusals(t *testing.T) {
	a, _ := importApp(t)
	out, err := call[linkimport.ImportOut](t, a, "ingest.url", linkimport.ImportIn{URLs: []string{
		"https://www.youtube.com/playlist?list=PL6B3937A5D230E335", "https://www.tiktok.com/@someone", "not a link", "http://127.0.0.1:4845/x",
		"https://www.youtube.com/watch?v=drmvideo001"}})
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]string{}
	for _, r := range out.Imports {
		codes[r.Input] = r.Status + "/" + r.Code
	}
	if codes["https://www.youtube.com/playlist?list=PL6B3937A5D230E335"] != "refused/playlist" || codes["https://www.tiktok.com/@someone"] != "refused/playlist" ||
		codes["not a link"] != "refused/not_a_link" || codes["http://127.0.0.1:4845/x"] != "refused/local" || out.Refused != 4 || out.Queued != 1 {
		t.Fatalf("playlists and profiles import nothing by default; bad links are refused: %v", codes)
	}
	for _, r := range out.Imports {
		if r.Code == "playlist" && !strings.Contains(r.Next, "Import the first 5") {
			t.Errorf("the refusal offers to import the first videos: %+v", r)
		}
	}
	// DRM: refused plainly, never retried.
	runImports(t, a, out.JobIDs)
	j := job(t, a, out.JobIDs[0])
	adv := linkimport.AdviceOf(j)
	if j.State != models.JobFailed || adv == nil || adv.Code != linkimport.AdviceDRM || adv.Retry || j.AttemptCount != 1 {
		t.Fatalf("DRM refusal: %s %+v", j.State, adv)
	}
	var n int64
	n, _ = a.DB.C(store.CAssets).CountDocuments(context.Background(), bson.M{"workspaceId": a.WorkspaceID()})
	if n != 0 {
		t.Fatalf("nothing was added: %d clips", n)
	}
}

func TestImportPlaylistWithLimit(t *testing.T) {
	a, _ := importApp(t)
	out, err := call[linkimport.ImportOut](t, a, "ingest.url", linkimport.ImportIn{URLs: []string{"https://www.youtube.com/playlist?list=PL6B3937A5D230E335"}, Playlist: 2})
	if err != nil || out.Queued != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	if m := out.Imports[0].Message; m != "Importing the first 2 videos of the playlist." {
		t.Fatalf("the answer says how many: %q", m)
	}
	runImports(t, a, out.JobIDs)
	j := job(t, a, out.JobIDs[0])
	if j.State != models.JobSucceeded || j.Result["status"] != "expanded" {
		t.Fatalf("the playlist job lists the videos: %s %v %s", j.State, j.Result, j.Error)
	}
	var children []models.Job
	_ = a.DB.Find(context.Background(), store.CJobs, a.WorkspaceID(), bson.M{"type": jobs.TypeImport, "payload.parent": j.ID}, &children)
	if len(children) != 2 {
		t.Fatalf("one import per video, at most the limit: %d", len(children))
	}
	for _, c := range children {
		if c.Installation != a.Cfg.Installation.ID {
			t.Fatalf("each video import stays on this machine: %+v", c)
		}
	}
}

func TestImportPostWithTwoVideos(t *testing.T) {
	a, _ := importApp(t)
	out, err := call[linkimport.ImportOut](t, a, "ingest.url", linkimport.ImportIn{URLs: []string{"https://x.com/fakeaccount/status/12345678901234567/video/1"}})
	if err != nil || out.Queued != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	// "multi" in the link makes the fake answer with a post that has two videos.
	_ = a.DB.Update(context.Background(), store.CJobs, a.WorkspaceID(), out.JobIDs[0], bson.M{"$set": bson.M{"payload.url": "https://x.com/fakeaccount/status/12345678901234567?multi"}})
	runImports(t, a, out.JobIDs)
	j := job(t, a, out.JobIDs[0])
	assets, _ := j.Result["assets"].(bson.A)
	if j.State != models.JobSucceeded || len(assets) != 2 {
		t.Fatalf("each video of the post becomes a clip: %s %v %s", j.State, j.Result, j.Error)
	}
	var as []models.Asset
	_ = a.DB.Find(context.Background(), store.CAssets, a.WorkspaceID(), bson.M{"source.platform": "x"}, &as)
	if len(as) != 2 || as[0].Source.Handle != "@fakeaccount" {
		t.Fatalf("two clips from the post with its account: %d", len(as))
	}
	titles := []string{as[0].DisplayTitle(), as[1].DisplayTitle()}
	if !strings.HasSuffix(titles[0], "(video 1 of 2)") && !strings.HasSuffix(titles[1], "(video 1 of 2)") || as[0].Source.Title != "A post with a video" {
		t.Fatalf("the clips are told apart; the source keeps the original title: %v / %q", titles, as[0].Source.Title)
	}
}

func TestToolsStatus(t *testing.T) {
	a, _ := importApp(t)
	st, err := call[linkimport.ImportTools](t, a, "ingest.url.tools", linkimport.ImportToolsIn{})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Installed || st.Version != fakeytdlp.Version || st.ManagedBy != "self" || st.JSRuntime != "deno 2.9.7" || st.ChallengeScripts != "0.8.0" || len(st.POTokenProviders) != 0 {
		t.Fatalf("status from the debug header: %+v", st)
	}
	up, err := call[linkimport.ImportToolsUpdate](t, a, "ingest.url.update", linkimport.ImportToolsIn{})
	if err != nil || up.How != "yt-dlp -U" || up.Updated {
		t.Fatalf("a self-contained yt-dlp updates itself: %+v %v", up, err)
	}
	off := false
	st, err = call[linkimport.ImportTools](t, a, "ingest.url.configure", linkimport.ImportConfigureIn{AutoUpdate: &off})
	if err != nil || st.AutoUpdate {
		t.Fatalf("automatic updates can be turned off: %+v %v", st, err)
	}
	if st.LastCheck == nil || st.LastResult == "" {
		t.Fatalf("the last update check is remembered: %+v", st)
	}
}
