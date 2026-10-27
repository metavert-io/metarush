// Package activity answers "what is MetaRush doing right now, and why is
// anything waiting?" from server state only: browser-upload records, ingest
// sessions, jobs (with deferral reasons derived from the online workers'
// published battery and threshold state), renders, QA and assistant runs. The
// dashboard's activity bar, the upload tray after a reload and
// `metarush activity` all read the same summary.
package activity

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
	"github.com/metavert-io/metarush/internal/uploads"
)

// Item categories.
const (
	CatUpload     = "upload"
	CatProcessing = "processing"
	CatRender     = "render"
	CatQA         = "qa"
	CatAssistant  = "assistant"
	CatNarration  = "narration"
	CatImport     = "import"   // a video coming in from a link (YouTube, TikTok, X)
	CatStorage    = "storage"  // the media moving to another storage
	CatGenerate   = "generate" // a clip being made with generative video (D125)
	CatRecipe     = "recipe"   // a run of a recipe (D204)
)

// Item states.
const (
	StateActive      = "active"      // running, or about to
	StateWaiting     = "waiting"     // queued with a reason (battery, no worker, busy, backoff)
	StateFailed      = "failed"      // needs attention (Retry)
	StateDone        = "done"        // finished recently
	StateInterrupted = "interrupted" // an upload that stopped receiving bytes
)

// SummaryIn filters the summary.
type SummaryIn struct {
	ProjectID     string `json:"projectId,omitempty"`
	RecentMinutes int    `json:"recentMinutes,omitempty" jsonschema:"description=How long finished items stay listed (default 10)."`
}

// Step is one derivative of a file (proxy, transcript, evidence…).
type Step struct {
	Kind    string           `json:"kind"`
	Label   string           `json:"label"`
	State   string           `json:"state" jsonschema:"enum=done,enum=running,enum=queued,enum=paused,enum=failed,enum=canceled"`
	Pct     float64          `json:"pct"`
	JobID   string           `json:"jobId"`
	Machine string           `json:"machine,omitempty"`
	Waiting *jobs.WaitReason `json:"waiting,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// Item is one thing in flight (or just finished).
type Item struct {
	Key        string           `json:"key" jsonschema:"description=Stable key: upload:<id>\\, asset:<id>\\, render:<id>\\, job:<id>\\, assistant:<id>\\, ingest:<id> or import:<id>."`
	Category   string           `json:"category" jsonschema:"enum=upload,enum=processing,enum=render,enum=qa,enum=assistant,enum=narration,enum=import,enum=storage,enum=generate,enum=recipe"` // recipe: Recipes
	Kind       string           `json:"kind"`
	Title      string           `json:"title"`
	Subtitle   string           `json:"subtitle,omitempty"`
	Stage      string           `json:"stage" jsonschema:"description=sending, checking, fingerprinting, storing, processing, ready, duplicate, queued, running, rendering, uploading, waiting, failed or done; link imports: lookup, download, adding."`
	StageLabel string           `json:"stageLabel"`
	State      string           `json:"state" jsonschema:"enum=active,enum=waiting,enum=failed,enum=done,enum=interrupted"`
	Pct        *float64         `json:"pct,omitempty" jsonschema:"description=0-1 for this item (absent when unknown)."`
	StagePct   *float64         `json:"stagePct,omitempty" jsonschema:"description=0-1 within the current stage."`
	BytesDone  int64            `json:"bytesDone,omitempty"`
	BytesTotal int64            `json:"bytesTotal,omitempty"`
	Rate       float64          `json:"rate,omitempty" jsonschema:"description=Bytes per second, when known."`
	ETASeconds *float64         `json:"etaSeconds,omitempty"`
	Machine    string           `json:"machine,omitempty"`
	Waiting    *jobs.WaitReason `json:"waiting,omitempty"`
	Error      string           `json:"error,omitempty"`
	Steps      []Step           `json:"steps,omitempty"`
	// Actions the dashboard (or CLI) can offer.
	CanRetry      bool     `json:"canRetry,omitempty"`
	RetryJobIDs   []string `json:"retryJobIds,omitempty"`
	CanProcessNow bool     `json:"canProcessNow,omitempty"`
	ProcessJobIDs []string `json:"processJobIds,omitempty"`
	// Identifiers and a dashboard deep link.
	UploadID  string              `json:"uploadId,omitempty"`
	AssetID   string              `json:"assetId,omitempty"`
	JobID     string              `json:"jobId,omitempty"`
	RenderID  string              `json:"renderId,omitempty"`
	RunID     string              `json:"runId,omitempty"`
	ProjectID string              `json:"projectId,omitempty"`
	Link      string              `json:"link,omitempty"`
	Duplicate *models.DuplicateOf `json:"duplicate,omitempty"`
	// Questions are open to-dos about a clip that finished processing ("2 questions about this clip").
	Questions *models.OpenQuestions `json:"questions,omitempty"`
	Name      string                `json:"name,omitempty" jsonschema:"description=Original filename (uploads and files)."`
	// Link imports: the link and its site. A failed row (any kind) says what
	// happened (Problem) and what to do next (Hint) in plain words; Error keeps
	// the technical detail for Details.
	URL      string `json:"url,omitempty"`
	Platform string `json:"platform,omitempty" jsonschema:"enum=youtube,enum=tiktok,enum=x,enum=web"`
	Problem  string `json:"problem,omitempty"`
	Hint     string `json:"hint,omitempty"`
	// SignInHelps offers "Use my browser's sign-in" (a bot check or sign-in wall).
	SignInHelps bool       `json:"signInHelps,omitempty"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// WaitGroup aggregates waiting jobs that share a reason ("3 transcriptions paused: battery at 18% on laptop").
type WaitGroup struct {
	Code           string   `json:"code"`
	Text           string   `json:"text"`
	Count          int      `json:"count"`
	Machine        string   `json:"machine,omitempty"`
	BatteryPercent *int     `json:"batteryPercent,omitempty"`
	Threshold      int      `json:"threshold,omitempty"`
	CanProcessNow  bool     `json:"canProcessNow"`
	JobIDs         []string `json:"jobIds"`
}

// Counts are what the activity bar shows at a glance.
type Counts struct {
	Uploads    int `json:"uploads"`
	Processing int `json:"processing"`
	Renders    int `json:"renders"`
	QA         int `json:"qa"`
	Assistant  int `json:"assistant"`
	Narration  int `json:"narration" jsonschema:"description=Narration being made (scratch or AI voice\\, cleanup\\, placing a recording)."`
	Imports    int `json:"imports" jsonschema:"description=Videos coming in from links (looking up\\, downloading\\, adding)."`
	Storage    int `json:"storage" jsonschema:"description=Moves of the media to another storage."`
	Generating int `json:"generating" jsonschema:"description=Clips being made with generative video."`
	Recipes    int `json:"recipes" jsonschema:"description=Runs of recipes going on or waiting for someone."`
	Waiting    int `json:"waiting" jsonschema:"description=Jobs waiting for a reason other than about to start."`
	Paused     int `json:"paused" jsonschema:"description=Heavy jobs deferred for battery."`
	Failed     int `json:"failed"`
	Done       int `json:"done"`
	Active     int `json:"active" jsonschema:"description=Items in flight (active, waiting or interrupted)."`
}

// WorkerBrief is an online machine and its power state.
type WorkerBrief struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Running        int    `json:"running"`
	Concurrency    int    `json:"concurrency"`
	BatteryPercent *int   `json:"batteryPercent,omitempty"`
	Charging       bool   `json:"charging,omitempty"`
	OnBattery      bool   `json:"onBattery,omitempty"`
	DeferringHeavy bool   `json:"deferringHeavy,omitempty"`
	DeferReason    string `json:"deferReason,omitempty"`
	Threshold      int    `json:"threshold"`
	This           bool   `json:"this,omitempty" jsonschema:"description=This installation."`
}

// Summary is the whole picture.
type Summary struct {
	GeneratedAt time.Time     `json:"generatedAt"`
	Headline    string        `json:"headline" jsonschema:"description=One line for the activity bar and the CLI."`
	Counts      Counts        `json:"counts"`
	Progress    *float64      `json:"progress,omitempty" jsonschema:"description=Overall progress 0-1 across items in flight."`
	Waiting     []WaitGroup   `json:"waiting"`
	Items       []Item        `json:"items"`
	Workers     []WorkerBrief `json:"workers"`
}

var stepLabels = map[string]string{"thumbnail": "Thumbnail", "proxy": "Preview copy", "contact_sheet": "Frame grid", "waveform": "Waveform",
	"mezzanine": "Steady copy", "evidence": "Scene analysis", "transcribe": "Transcript"}

var stageLabels = map[string]string{
	uploads.StageSending: "Sending", uploads.StageChecking: "Checking the file", uploads.StageFingerprinting: "Looking for copies",
	uploads.StageStoring: "Saving", "processing": "Processing", "ready": "Ready", "duplicate": "Skipped: already in MetaRush",
	"queued": "Waiting to start", "running": "Working", "rendering": "Rendering", "uploading": "Saving the video", "waiting": "Waiting",
	"failed": "Failed", "done": "Done", "interrupted": "Interrupted",
}

// stepProblems say what failed when one step of a clip's preparation failed.
var stepProblems = map[string]string{"thumbnail": "The thumbnail couldn't be made.", "proxy": "The preview copy couldn't be made.",
	"contact_sheet": "The frame grid couldn't be made.", "waveform": "The waveform couldn't be drawn.", "mezzanine": "The steady copy couldn't be made.",
	"evidence": "The scenes and pauses couldn't be found.", "transcribe": "The transcript couldn't be made.",
	"visual": "MetaRush couldn't look at what's on screen.", "describe": "The scenes couldn't be described."}

// tryAgain is what to do about a failure when nothing more specific is known.
const tryAgain = "Try again. If it keeps failing, open Details and share what it says with whoever helps you with MetaRush."

func ptr(f float64) *float64 { return &f }

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// eta estimates remaining seconds from progress since start.
func eta(pct float64, started time.Time, now time.Time) *float64 {
	el := now.Sub(started).Seconds()
	if pct < 0.03 || pct >= 1 || el < 3 {
		return nil
	}
	v := el * (1 - pct) / pct
	return &v
}

type builder struct {
	a       *app.App
	now     time.Time
	recent  time.Duration
	workers []models.WorkerDevice
	names   map[string]string // installation ID → name
	assets  map[string]*models.Asset
	projs   map[string]string
	jobs    map[string]models.Job // the jobs read, by ID (advice for failures)
	out     *Summary
}

// failed fills a failed row's plain words: what happened (problem) and what to
// do next, the same advice job details give for its job. Error keeps the
// technical text for Details.
func (b *builder) failed(it *Item, problem, jobID string) {
	it.Problem, it.Hint = problem, tryAgain
	if j, ok := b.jobs[jobID]; ok {
		if adv := ops.JobAdvice(&j); adv != "" {
			it.Hint = adv
		}
	}
}

// sentence starts s with a capital letter and ends it with a stop.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	s = capFirst(s)
	if !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "!") && !strings.HasSuffix(s, "?") {
		s += "."
	}
	return s
}

// Build computes the summary.
func Build(ctx context.Context, a *app.App, in *SummaryIn) (*Summary, error) {
	now := time.Now().UTC()
	recent := 10 * time.Minute
	if in.RecentMinutes > 0 {
		recent = time.Duration(in.RecentMinutes) * time.Minute
	}
	b := &builder{a: a, now: now, recent: recent, names: map[string]string{}, assets: map[string]*models.Asset{}, projs: map[string]string{},
		jobs: map[string]models.Job{}, out: &Summary{GeneratedAt: now, Waiting: []WaitGroup{}, Items: []Item{}, Workers: []WorkerBrief{}}}
	if err := a.DB.Find(ctx, store.CWorkers, a.WorkspaceID(), nil, &b.workers); err != nil {
		return nil, err
	}
	for _, w := range b.workers {
		b.names[w.ID] = w.Name
		if jobs.Online(w, now) {
			b.out.Workers = append(b.out.Workers, WorkerBrief{ID: w.ID, Name: w.Name, Running: w.Running, Concurrency: w.Concurrency, BatteryPercent: w.BatteryPercent,
				Charging: w.Charging, OnBattery: w.OnBattery, DeferringHeavy: w.DeferringHeavy, DeferReason: w.DeferReason, Threshold: w.HeavyBatteryThreshold,
				This: w.ID == a.Cfg.Installation.ID})
		}
	}
	// Jobs: everything open, plus recent terminal ones (for failures and the
	// steps of files still being processed).
	var open, recentDone []models.Job
	jf := bson.M{"state": bson.M{"$in": bson.A{models.JobQueued, models.JobRunning}}}
	if in.ProjectID != "" {
		jf["$or"] = bson.A{bson.M{"projectId": in.ProjectID}, bson.M{"assetId": bson.M{"$in": b.projectAssetIDs(ctx, in.ProjectID)}}}
	}
	if err := a.DB.Find(ctx, store.CJobs, a.WorkspaceID(), jf, &open, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(800)); err != nil {
		return nil, err
	}
	df := bson.M{"state": bson.M{"$in": bson.A{models.JobSucceeded, models.JobFailed, models.JobCanceled}}, "finishedAt": bson.M{"$gte": now.Add(-12 * time.Hour)}}
	if in.ProjectID != "" {
		df["$or"] = jf["$or"]
	}
	if err := a.DB.Find(ctx, store.CJobs, a.WorkspaceID(), df, &recentDone, options.Find().SetSort(bson.D{{Key: "finishedAt", Value: -1}}).SetLimit(600)); err != nil {
		return nil, err
	}
	for _, set := range [][]models.Job{open, recentDone} {
		for _, j := range set {
			b.jobs[j.ID] = j
		}
	}
	// Local browser uploads (this daemon) come first; their assets are not listed twice.
	coveredAssets := map[string]bool{}
	recs := uploads.For(a.Cfg.StateDir()).List(recent)
	for _, r := range recs {
		if in.ProjectID != "" && r.ProjectID != in.ProjectID {
			continue
		}
		if r.Result != nil && r.Result.AssetID != "" && r.Result.Status != "duplicate" {
			coveredAssets[r.Result.AssetID] = true
		}
	}
	// Derivative jobs grouped by asset.
	byAsset := map[string][]models.Job{}
	var other []models.Job
	for _, set := range [][]models.Job{open, recentDone} {
		for _, j := range set {
			if j.AssetID != "" && stepLabels[j.Type] != "" {
				byAsset[j.AssetID] = append(byAsset[j.AssetID], j)
			} else {
				other = append(other, j)
			}
		}
	}
	// Link imports: a finished import shows its clip's processing on its own row
	// ("Looking up the video" through "Ready"), so the clip isn't listed twice.
	var imports, gens []models.Job
	for _, j := range other {
		if j.Type == jobs.TypeGenerate {
			// A finished generation shows its clip's preparation on its own row.
			gens = append(gens, j)
			if id := generatedAsset(j); id != "" && j.State == models.JobSucceeded {
				coveredAssets[id] = true
			}
			continue
		}
		if j.Type != jobs.TypeImport {
			continue
		}
		imports = append(imports, j)
		if id := importedAsset(j); id != "" && j.State == models.JobSucceeded {
			coveredAssets[id] = true
		}
	}
	// Assets of covered uploads that have no job in the window still need their jobs.
	var need []string
	for id := range coveredAssets {
		if _, ok := byAsset[id]; !ok {
			need = append(need, id)
		}
	}
	if len(need) > 0 {
		var more []models.Job
		_ = a.DB.Find(ctx, store.CJobs, a.WorkspaceID(), bson.M{"assetId": bson.M{"$in": need}, "type": bson.M{"$in": notify.DerivativeTypes}}, &more)
		for _, j := range more {
			byAsset[j.AssetID] = append(byAsset[j.AssetID], j)
			b.jobs[j.ID] = j
		}
	}
	var assetIDs []string
	for id := range byAsset {
		assetIDs = append(assetIDs, id)
	}
	for _, r := range recs {
		if r.Result != nil && r.Result.AssetID != "" {
			assetIDs = append(assetIDs, r.Result.AssetID)
		}
	}
	for _, j := range other {
		if (j.Type == jobs.TypeDiarize || j.Type == jobs.TypeVisual || j.Type == jobs.TypeDescribe || j.Type == jobs.TypePrepare) && j.AssetID != "" {
			assetIDs = append(assetIDs, j.AssetID)
		}
		if j.Type == jobs.TypeImport {
			if id := importedAsset(j); id != "" {
				assetIDs = append(assetIDs, id)
			}
		}
		if j.Type == jobs.TypeGenerate {
			if id := generatedAsset(j); id != "" {
				assetIDs = append(assetIDs, id)
			}
		}
	}
	b.loadAssets(ctx, assetIDs)
	pids := map[string]bool{}
	for _, j := range append(append([]models.Job{}, imports...), gens...) {
		if j.ProjectID != "" {
			pids[j.ProjectID] = true
		}
	}
	for _, j := range other {
		if (j.Type == jobs.TypeRemotionRefresh || j.Type == jobs.TypeRemotionPush || j.Type == jobs.TypeAbilityRun) && j.ProjectID != "" {
			pids[j.ProjectID] = true // refreshes and pushes say which project (D173, D176)
		}
	}
	b.loadProjects(ctx, pids)
	for _, r := range recs {
		if in.ProjectID != "" && r.ProjectID != in.ProjectID {
			continue
		}
		b.uploadItem(r, byAsset)
	}
	b.ingestSessions(ctx, in.ProjectID, coveredAssets)
	for id, js := range byAsset {
		if coveredAssets[id] {
			continue
		}
		b.assetItem(id, js)
	}
	for _, j := range imports {
		b.importItem(j, byAsset)
	}
	b.generations(ctx, gens, byAsset)
	b.renders(ctx, in.ProjectID, other)
	b.qa(other)
	b.speakers(other)
	b.looking(other)
	b.fixing(other)
	b.fetches(other)
	b.remotionWork(other)
	b.narration(ctx, other)
	b.storageMoves(other)
	b.assistantRuns(in.ProjectID)
	b.recipeRuns(in.ProjectID) // runs of Recipes
	b.questions(ctx)
	b.finish()
	return b.out, nil
}

func (b *builder) projectAssetIDs(ctx context.Context, projectID string) bson.A {
	var as []struct {
		ID string `bson:"_id"`
	}
	_ = b.a.DB.Find(ctx, store.CAssets, b.a.WorkspaceID(), bson.M{"projectIds": projectID}, &as, options.Find().SetProjection(bson.M{"_id": 1}).SetLimit(2000))
	out := bson.A{}
	for _, x := range as {
		out = append(out, x.ID)
	}
	return out
}

func (b *builder) loadAssets(ctx context.Context, idList []string) {
	if len(idList) == 0 {
		return
	}
	var as []models.Asset
	_ = b.a.DB.Find(ctx, store.CAssets, b.a.WorkspaceID(), bson.M{"_id": bson.M{"$in": idList}}, &as,
		options.Find().SetProjection(bson.M{"media.streams": 0, "variants": 0}))
	pids := map[string]bool{}
	for i := range as {
		b.assets[as[i].ID] = &as[i]
		for _, p := range as[i].ProjectIDs {
			pids[p] = true
		}
	}
	b.loadProjects(ctx, pids)
}

func (b *builder) loadProjects(ctx context.Context, pids map[string]bool) {
	var list []string
	for p := range pids {
		if _, ok := b.projs[p]; !ok {
			list = append(list, p)
		}
	}
	if len(list) == 0 {
		return
	}
	var ps []models.Project
	_ = b.a.DB.Find(ctx, store.CProjects, b.a.WorkspaceID(), bson.M{"_id": bson.M{"$in": list}}, &ps, options.Find().SetProjection(bson.M{"name": 1}))
	for _, p := range ps {
		b.projs[p.ID] = p.Name
	}
}

func (b *builder) where(projectID string) string {
	if projectID == "" {
		return "Inbox"
	}
	if n := b.projs[projectID]; n != "" {
		return n
	}
	return "a project"
}

// steps builds the derivative steps of an asset from its jobs (latest per type).
func (b *builder) steps(js []models.Job) []Step {
	latest := map[string]models.Job{}
	for _, j := range js {
		if cur, ok := latest[j.Type]; !ok || j.CreatedAt.After(cur.CreatedAt) {
			latest[j.Type] = j
		}
	}
	var out []Step
	for _, typ := range notify.DerivativeTypes {
		j, ok := latest[typ]
		if !ok {
			continue
		}
		out = append(out, b.jobStep(j, typ, stepLabels[typ]))
	}
	return out
}

// jobStep is one job as a step of a file's row.
func (b *builder) jobStep(j models.Job, kind, label string) Step {
	s := Step{Kind: kind, Label: label, JobID: j.ID}
	switch j.State {
	case models.JobSucceeded:
		s.State, s.Pct = "done", 1
	case models.JobFailed:
		s.State, s.Error = "failed", j.Error
	case models.JobCanceled:
		s.State = "canceled"
	case models.JobRunning:
		s.State, s.Pct = "running", clamp01(j.Progress.Pct)
		if j.Lease != nil {
			s.Machine = j.Lease.OwnerName
		}
	default:
		s.State = "queued"
		s.Waiting = jobs.Why(j, b.workers, b.now)
		if s.Waiting != nil && s.Waiting.Code == jobs.WaitBattery {
			s.State = "paused"
		}
		if s.Waiting != nil {
			s.Machine = s.Waiting.Machine
		}
	}
	return s
}

// applySteps fills an item's processing stage from its steps. It reports
// whether the item is still worth showing.
func (b *builder) applySteps(it *Item, steps []Step, lastChange time.Time) bool {
	it.Steps = steps
	if len(steps) == 0 {
		return false
	}
	var sum float64
	open, failed := 0, 0
	var running []string
	var failedSteps []Step
	var waiting *jobs.WaitReason
	for _, s := range steps {
		sum += s.Pct
		switch s.State {
		case "running":
			open++
			running = append(running, s.Label)
			if it.Machine == "" {
				it.Machine = s.Machine
			}
		case "queued", "paused":
			open++
			if s.Waiting != nil && (waiting == nil || s.State == "paused") {
				waiting = s.Waiting
			}
			if s.Waiting != nil && s.Waiting.CanProcessNow {
				it.CanProcessNow = true
				it.ProcessJobIDs = append(it.ProcessJobIDs, s.JobID)
			}
		case "failed":
			failed++
			failedSteps = append(failedSteps, s)
			it.CanRetry = true
			it.RetryJobIDs = append(it.RetryJobIDs, s.JobID)
			if it.Error == "" {
				it.Error = s.Label + ": " + firstLine(s.Error)
			}
		}
	}
	if len(failedSteps) > 0 {
		// The row says what couldn't be made and what to do; the error waits in Details.
		problem := stepProblems[failedSteps[0].Kind]
		if problem == "" || len(failedSteps) > 1 {
			var what []string
			for _, s := range failedSteps {
				what = append(what, strings.ToLower(s.Label))
			}
			problem = "Some of it couldn't be made: " + andList(what) + "."
		}
		b.failed(it, problem, failedSteps[0].JobID)
	}
	it.Pct = ptr(clamp01(sum / float64(len(steps))))
	switch {
	case open > 0:
		it.Stage, it.StageLabel = "processing", "Processing"
		if len(running) > 0 {
			it.State = StateActive
			it.StageLabel = "Processing: " + strings.ToLower(strings.Join(running, ", "))
		} else {
			it.State, it.Waiting = StateWaiting, waiting
			if waiting != nil && waiting.Code == jobs.WaitStarting {
				it.State = StateActive
			}
		}
	case failed > 0:
		it.Stage, it.StageLabel, it.State = "failed", "Processing failed", StateFailed
	default:
		it.Stage, it.StageLabel, it.State = "ready", "Ready", StateDone
		if b.now.Sub(lastChange) > b.recent {
			return false
		}
	}
	return true
}

func lastChange(js []models.Job) time.Time {
	var t time.Time
	for _, j := range js {
		if j.UpdatedAt.After(t) {
			t = j.UpdatedAt
		}
	}
	return t
}

func (b *builder) uploadItem(r uploads.Record, byAsset map[string][]models.Job) {
	it := Item{Key: "upload:" + r.ID, Category: CatUpload, Kind: "upload", Title: models.DefaultTitle(r.Name), Name: r.Name, Subtitle: b.where(r.ProjectID),
		UploadID: r.ID, ProjectID: r.ProjectID, Machine: b.a.Cfg.Installation.Name, UpdatedAt: r.UpdatedAt, BytesDone: r.Offset, BytesTotal: r.Size}
	started := r.CreatedAt
	it.StartedAt = &started
	switch r.State {
	case uploads.StateReceiving:
		it.Stage, it.StageLabel, it.Rate = uploads.StageSending, "Sending", r.Rate
		it.Pct, it.StagePct = ptr(r.Pct()*0.85), ptr(r.Pct())
		it.State = StateActive
		if r.Interrupted(b.now) {
			it.Stage, it.State = "interrupted", StateInterrupted
			it.StageLabel = fmt.Sprintf("Interrupted at %d%%", int(r.Pct()*100))
			it.Rate = 0
		} else if r.Rate > 0 {
			it.ETASeconds = ptr(float64(r.Size-r.Offset) / r.Rate)
		}
	case uploads.StateIngesting:
		it.State, it.Stage, it.StageLabel = StateActive, r.Stage, stageLabels[r.Stage]
		it.BytesDone, it.BytesTotal, it.Rate = r.StageDone, r.StageTotal, r.Rate
		sp := 0.0
		if r.StageTotal > 0 {
			sp = clamp01(float64(r.StageDone) / float64(r.StageTotal))
		}
		it.StagePct = ptr(sp)
		base := map[string]float64{uploads.StageChecking: 0.85, uploads.StageFingerprinting: 0.87, uploads.StageStoring: 0.92}[r.Stage]
		span := map[string]float64{uploads.StageChecking: 0.02, uploads.StageFingerprinting: 0.05, uploads.StageStoring: 0.08}[r.Stage]
		it.Pct = ptr(base + span*sp)
		if r.Rate > 0 && r.StageTotal > r.StageDone {
			it.ETASeconds = ptr(float64(r.StageTotal-r.StageDone) / r.Rate)
		}
	case uploads.StateDone:
		res := r.Result
		if res != nil && res.Status == "duplicate" {
			it.Stage, it.State, it.Duplicate = "duplicate", StateDone, res.Duplicate
			it.StageLabel = "Skipped: already in MetaRush"
			if res.Duplicate != nil {
				it.StageLabel = fmt.Sprintf("Skipped: identical to %s", res.Duplicate.Title)
				it.AssetID = res.Duplicate.AssetID
			}
			it.Pct = ptr(1)
			break
		}
		if res != nil {
			it.AssetID = res.AssetID
			if as := b.assets[res.AssetID]; as != nil {
				it.Title = as.DisplayTitle()
				it.Link = notify.AssetLink(as)
			}
		}
		js := byAsset[it.AssetID]
		if !b.applySteps(&it, b.steps(js), maxTime(lastChange(js), r.UpdatedAt)) {
			if len(js) == 0 && b.now.Sub(r.UpdatedAt) <= b.recent {
				it.Stage, it.StageLabel, it.State, it.Pct = "ready", "Added", StateDone, ptr(1)
			} else {
				return
			}
		}
		if it.Pct != nil {
			it.Pct = ptr(0.95 + 0.05**it.Pct)
			if it.State == StateDone {
				it.Pct = ptr(1)
			}
		}
		it.BytesDone, it.BytesTotal = r.Size, r.Size
	case uploads.StateFailed:
		it.Stage, it.StageLabel, it.State, it.Error = "failed", "Upload failed", StateFailed, r.Error
		// A message meant for people says why; an error with a code waits in Details.
		it.Problem, it.Hint = "The upload didn't finish.", "Add the file again to try once more. Details says what went wrong."
		if msg := strings.TrimSpace(r.Error); msg != "" && !strings.HasPrefix(msg, "E_") && !strings.Contains(msg, "\n") {
			it.Hint = sentence(msg)
		}
	default:
		return // canceled
	}
	b.out.Items = append(b.out.Items, it)
}

func maxTime(a, c time.Time) time.Time {
	if a.After(c) {
		return a
	}
	return c
}

// viaWords say where a file being added came from.
var viaWords = map[string]string{"cli": "from the command line", "dashboard": "from the dashboard", "folder": "from a linked folder",
	"watch": "from the watched folder", "import": "from a link", "mcp": "from a connected app", "assistant": "by Claude in the dashboard"}

// ingestSessions lists uploads to the canonical store that other processes
// (CLI, watched folder, other machines) are running.
func (b *builder) ingestSessions(ctx context.Context, projectID string, covered map[string]bool) {
	f := bson.M{"state": bson.M{"$in": bson.A{"uploading", "verifying"}}, "updatedAt": bson.M{"$gte": b.now.Add(-2 * time.Minute)}}
	if projectID != "" {
		f["projectId"] = projectID
	}
	var ss []models.IngestSession
	_ = b.a.DB.Find(ctx, store.CIngest, b.a.WorkspaceID(), f, &ss, options.Find().SetLimit(100))
	for _, s := range ss {
		if covered[s.AssetID] || s.JobID != "" { // a link import's own row shows it ("Adding to MetaRush")
			continue
		}
		pct := 0.0
		if s.Size > 0 {
			pct = clamp01(float64(s.BytesDone) / float64(s.Size))
		}
		machine := b.names[s.InstallationID]
		if machine == "" {
			machine = s.InstallationID
		}
		sub := b.where(s.ProjectID)
		if from := viaWords[s.Via]; from != "" {
			sub += ", " + from
		}
		it := Item{Key: "ingest:" + s.ID, Category: CatUpload, Kind: "ingest", Title: models.DefaultTitle(s.SourceName), Name: s.SourceName, Subtitle: sub,
			Stage: uploads.StageStoring, StageLabel: "Saving", State: StateActive, Pct: ptr(0.9 * pct), StagePct: ptr(pct), BytesDone: s.BytesDone, BytesTotal: s.Size,
			Machine: machine, AssetID: s.AssetID, ProjectID: s.ProjectID, UpdatedAt: s.UpdatedAt}
		if b.now.Sub(s.UpdatedAt) > 45*time.Second {
			it.State, it.Stage, it.StageLabel = StateInterrupted, "interrupted", fmt.Sprintf("Stopped at %d%%", int(pct*100))
		}
		started := s.CreatedAt
		it.StartedAt = &started
		b.out.Items = append(b.out.Items, it)
		covered[s.AssetID] = true
	}
}

func (b *builder) assetItem(assetID string, js []models.Job) {
	as := b.assets[assetID]
	if as != nil && as.MadeNarration() {
		return // audio MetaRush made for narration: its narration item covers it
	}
	it := Item{Key: "asset:" + assetID, Category: CatProcessing, Kind: "asset", AssetID: assetID, Title: assetID, UpdatedAt: lastChange(js)}
	if as != nil {
		it.Title, it.Name, it.Link = as.DisplayTitle(), as.Name, notify.AssetLink(as)
		if len(as.ProjectIDs) > 0 {
			it.ProjectID = as.ProjectIDs[0]
		}
		it.Subtitle = b.where(it.ProjectID)
	}
	if !b.applySteps(&it, b.steps(js), it.UpdatedAt) {
		return
	}
	// A failure that a later run of the same step superseded is not shown.
	b.out.Items = append(b.out.Items, it)
}

func (b *builder) renders(ctx context.Context, projectID string, other []models.Job) {
	f := bson.M{"$or": bson.A{
		bson.M{"state": bson.M{"$in": bson.A{models.RenderQueued, models.RenderRendering, models.RenderUploading}}},
		bson.M{"state": models.RenderAvailable, "completedAt": bson.M{"$gte": b.now.Add(-b.recent)}},
		bson.M{"state": models.RenderFailed, "createdAt": bson.M{"$gte": b.now.Add(-12 * time.Hour)}},
	}}
	if projectID != "" {
		f["projectId"] = projectID
	}
	var rs []models.Render
	_ = b.a.DB.Find(ctx, store.CRenders, b.a.WorkspaceID(), f, &rs, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(60))
	if len(rs) == 0 {
		return
	}
	jobByID := map[string]models.Job{}
	for _, j := range other {
		if j.Type == jobs.TypeRender {
			jobByID[j.ID] = j
		}
	}
	pids := map[string]bool{}
	for _, r := range rs {
		pids[r.ProjectID] = true
	}
	b.loadProjects(ctx, pids)
	for _, r := range rs {
		it := Item{Key: "render:" + r.ID, Category: CatRender, Kind: "render", Title: models.RenderLabel(r.Quality, r.RevisionNumber),
			Subtitle: b.projs[r.ProjectID] + ", " + editspec.PresetLabel(r.Preset), RenderID: r.ID, JobID: r.JobID, ProjectID: r.ProjectID, Link: links.Render(r.ProjectID, r.ID), UpdatedAt: r.CreatedAt}
		if r.Follow {
			it.Subtitle += ", made by itself for the latest version" // D187
		}
		j, hasJob := jobByID[r.JobID]
		switch r.State {
		case models.RenderAvailable:
			it.Stage, it.StageLabel, it.State, it.Pct = "done", "Finished", StateDone, ptr(1)
			it.Machine = r.RenderedByName
			if r.CompletedAt != nil {
				it.UpdatedAt = *r.CompletedAt
			}
		case models.RenderFailed:
			it.Stage, it.StageLabel, it.State, it.Error = "failed", "Failed", StateFailed, firstLine(r.Error)
			it.CanRetry, it.RetryJobIDs = true, []string{r.JobID}
			b.failed(&it, "The render didn't finish.", r.JobID)
		default:
			it.State, it.Stage, it.StageLabel = StateActive, r.State, stageLabels[r.State]
			if hasJob {
				it.UpdatedAt = j.UpdatedAt
				if j.State == models.JobRunning {
					it.Pct = ptr(clamp01(j.Progress.Pct))
					if j.Progress.Message != "" {
						it.StageLabel = capFirst(j.Progress.Message)
					}
					if j.Lease != nil {
						it.Machine = j.Lease.OwnerName
					}
					if n := len(j.Attempts); n > 0 {
						st := j.Attempts[n-1].StartedAt
						it.StartedAt = &st
						it.ETASeconds = eta(j.Progress.Pct, st, b.now)
					}
				} else if j.State == models.JobQueued {
					it.Stage, it.StageLabel = "queued", "Waiting to start"
					it.Waiting = jobs.Why(j, b.workers, b.now)
					if it.Waiting != nil && it.Waiting.Code != jobs.WaitStarting {
						it.State = StateWaiting
						it.CanProcessNow, it.ProcessJobIDs = it.Waiting.CanProcessNow, []string{j.ID}
					}
				}
			}
		}
		b.out.Items = append(b.out.Items, it)
	}
}

func (b *builder) qa(other []models.Job) {
	for _, j := range other {
		if j.Type != jobs.TypeQA {
			continue
		}
		rid, _ := j.Payload["renderId"].(string)
		it := Item{Key: "job:" + j.ID, Category: CatQA, Kind: "qa", Title: "Quality check", Subtitle: b.projs[j.ProjectID], JobID: j.ID, RenderID: rid,
			ProjectID: j.ProjectID, Link: links.Render(j.ProjectID, rid), UpdatedAt: j.UpdatedAt}
		switch j.State {
		case models.JobRunning:
			it.State, it.Stage, it.StageLabel, it.Pct = StateActive, "running", capFirst(orStr(j.Progress.Message, "Checking")), ptr(clamp01(j.Progress.Pct))
			if j.Lease != nil {
				it.Machine = j.Lease.OwnerName
			}
		case models.JobQueued:
			it.State, it.Stage, it.StageLabel = StateActive, "queued", "Waiting to start"
			it.Waiting = jobs.Why(j, b.workers, b.now)
			if it.Waiting != nil && it.Waiting.Code != jobs.WaitStarting {
				it.State = StateWaiting
				it.CanProcessNow, it.ProcessJobIDs = it.Waiting.CanProcessNow, []string{j.ID}
			}
		case models.JobFailed:
			it.State, it.Stage, it.StageLabel, it.Error = StateFailed, "failed", "Failed", firstLine(j.Error)
			it.CanRetry, it.RetryJobIDs = true, []string{j.ID}
			b.failed(&it, "The quality check didn't finish.", j.ID)
		case models.JobSucceeded:
			if j.FinishedAt == nil || b.now.Sub(*j.FinishedAt) > b.recent {
				continue
			}
			it.State, it.Stage, it.Pct = StateDone, "done", ptr(1)
			it.StageLabel = qaVerdict(j.Result)
			if rep, _ := j.Result["reportId"].(string); rep != "" {
				it.Link = links.QA(j.ProjectID, rid, rep)
			}
		default:
			continue
		}
		b.out.Items = append(b.out.Items, it)
	}
}

// speakers lists speaker detection runs (someone asked MetaRush to listen for
// the voices again): "Finding who's speaking in “Kitchen chat”".
func (b *builder) speakers(other []models.Job) {
	latest := map[string]models.Job{} // one row per clip: its latest run
	for _, j := range other {
		if j.Type != jobs.TypeDiarize || j.AssetID == "" {
			continue
		}
		if cur, ok := latest[j.AssetID]; !ok || j.CreatedAt.After(cur.CreatedAt) {
			latest[j.AssetID] = j
		}
	}
	for _, j := range latest {
		title, projectID := "the clip", ""
		if as := b.assets[j.AssetID]; as != nil {
			title = as.DisplayTitle()
			if len(as.ProjectIDs) > 0 {
				projectID = as.ProjectIDs[0]
			}
		}
		it := Item{Key: "job:" + j.ID, Category: CatProcessing, Kind: "speakers", Title: "Finding who's speaking in “" + title + "”", Subtitle: b.where(projectID),
			JobID: j.ID, AssetID: j.AssetID, ProjectID: projectID, Link: links.AssetSpeakers(projectID, j.AssetID), UpdatedAt: j.UpdatedAt}
		switch j.State {
		case models.JobRunning:
			it.State, it.Stage, it.StageLabel, it.Pct = StateActive, "running", capFirst(orStr(j.Progress.Message, "Listening")), ptr(clamp01(j.Progress.Pct))
			if j.Lease != nil {
				it.Machine = j.Lease.OwnerName
			}
			if n := len(j.Attempts); n > 0 {
				st := j.Attempts[n-1].StartedAt
				it.StartedAt = &st
				it.ETASeconds = eta(j.Progress.Pct, st, b.now)
			}
		case models.JobQueued:
			it.State, it.Stage, it.StageLabel = StateActive, "queued", "Waiting to start"
			it.Waiting = jobs.Why(j, b.workers, b.now)
			if it.Waiting != nil && it.Waiting.Code != jobs.WaitStarting {
				it.State, it.Stage, it.StageLabel = StateWaiting, "waiting", "Waiting"
				it.CanProcessNow, it.ProcessJobIDs = it.Waiting.CanProcessNow, []string{j.ID}
			}
			if it.Waiting != nil {
				it.Machine = it.Waiting.Machine
			}
		case models.JobFailed:
			it.State, it.Stage, it.StageLabel, it.Error = StateFailed, "failed", "Could not tell the voices apart", firstLine(j.Error)
			it.CanRetry, it.RetryJobIDs = true, []string{j.ID}
			b.failed(&it, "MetaRush couldn't tell the voices apart in this clip.", j.ID)
		case models.JobSucceeded:
			if j.FinishedAt == nil || b.now.Sub(*j.FinishedAt) > b.recent {
				continue
			}
			it.State, it.Stage, it.Pct = StateDone, "done", ptr(1)
			it.StageLabel = voicesFound(toInt(j.Result["voices"]))
			it.Title = "Who's speaking in “" + title + "”"
		default:
			continue
		}
		b.out.Items = append(b.out.Items, it)
	}
}

// lookLabels name the looks at a clip when they are steps of its row.
var lookLabels = map[string]string{"visual": "What's on screen", "describe": "Scene descriptions"}

// preparingRow is the row of a clip whose preparation (preview, transcript,
// scene analysis…) is still running: its upload, its import or its own row.
func (b *builder) preparingRow(assetID string) *Item {
	for i := range b.out.Items {
		it := &b.out.Items[i]
		if it.AssetID == assetID && it.Stage == "processing" && (it.Category == CatUpload || it.Category == CatImport || (it.Category == CatProcessing && it.Kind == "asset")) {
			return it
		}
	}
	return nil
}

// addStep adds one more step to a row being processed, keeping what the row
// offers (retry, process now) and says ("Processing: …") in line with it.
func (b *builder) addStep(it *Item, s Step) {
	it.Steps = append(it.Steps, s)
	switch s.State {
	case "running":
		it.State = StateActive
		if it.Machine == "" {
			it.Machine = s.Machine
		}
		if strings.HasPrefix(it.StageLabel, "Processing: ") {
			it.StageLabel += ", " + strings.ToLower(s.Label)
		} else {
			it.StageLabel = "Processing: " + strings.ToLower(s.Label)
		}
	case "queued", "paused":
		if s.Waiting != nil && s.Waiting.CanProcessNow {
			it.CanProcessNow = true
			it.ProcessJobIDs = append(it.ProcessJobIDs, s.JobID)
		}
	case "failed":
		it.CanRetry = true
		it.RetryJobIDs = append(it.RetryJobIDs, s.JobID)
	}
	var sum float64
	for _, st := range it.Steps {
		sum += st.Pct
	}
	it.Pct = ptr(clamp01(sum / float64(len(it.Steps))))
}

// looking lists what MetaRush looks at in clips, one row per clip and kind:
// "Looking at what's on screen in “Kitchen chat”" (the on-screen index) and
// "Describing the scenes of “Kitchen chat”" (written descriptions).
// fetches are repositories and web pages being brought in (D104, D105).
func (b *builder) fetches(other []models.Job) {
	for _, j := range other {
		if j.Type != jobs.TypeFetch {
			continue
		}
		kind, _ := j.Payload["kind"].(string)
		addr, _ := j.Payload["url"].(string)
		what := fetchWords(kind, addr)
		it := Item{Key: "job:" + j.ID, Category: CatImport, Kind: "fetch", Title: "Getting " + what, Subtitle: b.whereResources(j.ProjectID),
			JobID: j.ID, ProjectID: j.ProjectID, Link: links.Resource(j.ProjectID, ""), UpdatedAt: j.UpdatedAt}
		switch j.State {
		case models.JobRunning:
			it.State, it.Stage, it.StageLabel, it.Pct = StateActive, "running", capFirst(orStr(j.Progress.Message, "Starting")), ptr(clamp01(j.Progress.Pct))
			if j.Lease != nil {
				it.Machine = j.Lease.OwnerName
			}
			if n := len(j.Attempts); n > 0 {
				st := j.Attempts[n-1].StartedAt
				it.StartedAt = &st
			}
		case models.JobQueued:
			it.State, it.Stage, it.StageLabel = StateActive, "queued", "Waiting to start"
			it.Waiting = jobs.Why(j, b.workers, b.now)
			if it.Waiting != nil && it.Waiting.Code != jobs.WaitStarting {
				it.State, it.Stage, it.StageLabel = StateWaiting, "waiting", "Waiting"
				it.CanProcessNow, it.ProcessJobIDs = it.Waiting.CanProcessNow, []string{j.ID}
			}
			if it.Waiting != nil {
				it.Machine = it.Waiting.Machine
			}
		case models.JobFailed:
			it.State, it.Stage, it.StageLabel, it.Error = StateFailed, "failed", "Could not get "+what, firstLine(j.Error)
			it.CanRetry, it.RetryJobIDs = true, []string{j.ID}
			b.failed(&it, "Couldn't get "+what+".", j.ID)
		case models.JobSucceeded:
			if j.FinishedAt == nil || b.now.Sub(*j.FinishedAt) > b.recent {
				continue
			}
			it.State, it.Stage, it.Pct = StateDone, "done", ptr(1)
			it.Title = capFirst(what)
			it.StageLabel = capFirst(orStr(strOf(j.Result["message"]), "Done"))
			if ids, ok := j.Result["resourceIds"].(bson.A); ok && len(ids) > 0 {
				if id, ok := ids[0].(string); ok {
					it.Link = links.Resource(j.ProjectID, id)
				}
			}
		default:
			continue
		}
		b.out.Items = append(b.out.Items, it)
	}
}

// fetchWords names what a fetch brings in: "acme/widget from GitHub", "a snapshot of example.com".
func fetchWords(kind, addr string) string {
	u, err := url.Parse(addr)
	if err != nil || u.Host == "" {
		return "a link"
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	switch kind {
	case "repo":
		return strings.Trim(u.Path, "/") + " from GitHub"
	case "picture":
		return "a picture from " + host
	}
	p := strings.TrimSuffix(u.Path, "/")
	if len(p) > 32 {
		p = p[:31] + "…"
	}
	return "a snapshot of " + host + p
}

// whereResources says where a fetch's resources go.
func (b *builder) whereResources(projectID string) string {
	if projectID == "" {
		return "Workspace library"
	}
	return b.where(projectID)
}

func (b *builder) looking(other []models.Job) {
	latest := map[string]models.Job{}
	for _, j := range other {
		if (j.Type != jobs.TypeVisual && j.Type != jobs.TypeDescribe) || j.AssetID == "" {
			continue
		}
		k := j.Type + ":" + j.AssetID
		if cur, ok := latest[k]; !ok || j.CreatedAt.After(cur.CreatedAt) {
			latest[k] = j
		}
	}
	for _, j := range latest {
		title, projectID := "the clip", ""
		if as := b.assets[j.AssetID]; as != nil {
			title = as.DisplayTitle()
			if len(as.ProjectIDs) > 0 {
				projectID = as.ProjectIDs[0]
			}
		}
		kind, doing, done, failed := "visual", "Looking at what's on screen in “%s”", "What's on screen in “%s”", "Could not look at what's on screen"
		if j.Type == jobs.TypeDescribe {
			kind, doing, done, failed = "describe", "Describing the scenes of “%s”", "The scenes of “%s”", "Could not describe the scenes"
		}
		// A clip still being prepared keeps one row: the look is one more step of it.
		if row := b.preparingRow(j.AssetID); row != nil {
			b.addStep(row, b.jobStep(j, kind, lookLabels[kind]))
			continue
		}
		it := Item{Key: "job:" + j.ID, Category: CatProcessing, Kind: kind, Title: fmt.Sprintf(doing, title), Subtitle: b.where(projectID),
			JobID: j.ID, AssetID: j.AssetID, ProjectID: projectID, Link: links.AssetOnScreen(projectID, j.AssetID), UpdatedAt: j.UpdatedAt}
		switch j.State {
		case models.JobRunning:
			it.State, it.Stage, it.StageLabel, it.Pct = StateActive, "running", capFirst(orStr(j.Progress.Message, "Looking")), ptr(clamp01(j.Progress.Pct))
			if j.Lease != nil {
				it.Machine = j.Lease.OwnerName
			}
			if n := len(j.Attempts); n > 0 {
				st := j.Attempts[n-1].StartedAt
				it.StartedAt = &st
				it.ETASeconds = eta(j.Progress.Pct, st, b.now)
			}
		case models.JobQueued:
			it.State, it.Stage, it.StageLabel = StateActive, "queued", "Waiting to start"
			it.Waiting = jobs.Why(j, b.workers, b.now)
			if it.Waiting != nil && it.Waiting.Code != jobs.WaitStarting {
				it.State, it.Stage, it.StageLabel = StateWaiting, "waiting", "Waiting"
				it.CanProcessNow, it.ProcessJobIDs = it.Waiting.CanProcessNow, []string{j.ID}
			}
			if it.Waiting != nil {
				it.Machine = it.Waiting.Machine
			}
		case models.JobFailed:
			it.State, it.Stage, it.StageLabel, it.Error = StateFailed, "failed", failed, firstLine(j.Error)
			it.CanRetry, it.RetryJobIDs = true, []string{j.ID}
			b.failed(&it, stepProblems[kind], j.ID)
		case models.JobSucceeded:
			if j.FinishedAt == nil || b.now.Sub(*j.FinishedAt) > b.recent {
				continue
			}
			it.State, it.Stage, it.Pct = StateDone, "done", ptr(1)
			it.Title = fmt.Sprintf(done, title)
			switch {
			case j.Type == jobs.TypeDescribe:
				it.StageLabel = "Described " + scenesWord(toInt(j.Result["scenes"]))
			default:
				it.StageLabel = capFirst(orStr(strOf(j.Result["summary"]), "Done"))
			}
		default:
			continue
		}
		b.out.Items = append(b.out.Items, it)
	}
}

func scenesWord(n int) string {
	if n == 1 {
		return "1 scene"
	}
	return fmt.Sprintf("%d scenes", n)
}

func strOf(v any) string {
	s, _ := v.(string)
	return s
}

func voicesFound(n int) string {
	switch n {
	case 0:
		return "No voices found"
	case 1:
		return "Found 1 voice"
	}
	return fmt.Sprintf("Found %d voices", n)
}

// narration lists narration work (scratch or AI voice, cleanup, placing a
// recording, a voice copy): it runs in the process that asked for it and
// reports per line ("Making the AI voice for line 3").
func (b *builder) narration(ctx context.Context, other []models.Job) {
	pids := map[string]bool{}
	for _, j := range other {
		if j.Type == jobs.TypeVoiceover && j.ProjectID != "" {
			pids[j.ProjectID] = true
		}
	}
	b.loadProjects(ctx, pids)
	for _, j := range other {
		if j.Type != jobs.TypeVoiceover {
			continue
		}
		title, _ := j.Payload["title"].(string)
		revID, _ := j.Payload["revisionId"].(string)
		sub := b.projs[j.ProjectID]
		if n := toInt(j.Payload["revisionNumber"]); n > 0 {
			sub = strings.TrimPrefix(fmt.Sprintf("%s, version %d", sub, n), ", ")
		}
		it := Item{Key: "job:" + j.ID, Category: CatNarration, Kind: "narration", Title: orStr(title, "Narration"), Subtitle: sub, JobID: j.ID,
			ProjectID: j.ProjectID, Link: links.Job(j.ID), UpdatedAt: j.UpdatedAt}
		if revID != "" && j.ProjectID != "" {
			it.Link = links.Revision(j.ProjectID, revID)
		}
		switch j.State {
		case models.JobRunning:
			it.State, it.Stage, it.StageLabel, it.Pct = StateActive, "running", capFirst(orStr(j.Progress.Message, "Working")), ptr(clamp01(j.Progress.Pct))
			if j.Lease != nil {
				it.Machine = j.Lease.OwnerName
			}
			if n := len(j.Attempts); n > 0 {
				st := j.Attempts[n-1].StartedAt
				it.StartedAt = &st
				it.ETASeconds = eta(j.Progress.Pct, st, b.now)
			}
		case models.JobQueued:
			it.State, it.Stage, it.StageLabel = StateActive, "queued", "Waiting to start"
			it.Waiting = jobs.Why(j, b.workers, b.now)
			if it.Waiting != nil && it.Waiting.Code != jobs.WaitStarting {
				it.State = StateWaiting
			}
		case models.JobFailed:
			// Starting it again is the way to retry (it needs the machine and files that asked for it).
			it.State, it.Stage, it.StageLabel, it.Error = StateFailed, "failed", "Could not finish", firstLine(j.Error)
			b.failed(&it, "The narration couldn't be finished.", j.ID)
		case models.JobSucceeded:
			if j.FinishedAt == nil || b.now.Sub(*j.FinishedAt) > b.recent {
				continue
			}
			it.State, it.Stage, it.StageLabel, it.Pct = StateDone, "done", "Ready to review", ptr(1)
		default:
			continue
		}
		b.out.Items = append(b.out.Items, it)
	}
}

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case float64:
		return int(x)
	}
	return 0
}

// questions marks clips that finished processing (uploads, ingest steps,
// speaker detection) while questions about them are open, with the same
// words and link as job details and the bell (todos.AboutClip).
func (b *builder) questions(ctx context.Context) {
	var idList []string
	for _, it := range b.out.Items {
		if it.State == StateDone && it.AssetID != "" && it.Stage != "duplicate" {
			idList = append(idList, it.AssetID)
		}
	}
	counts, err := todos.OpenByAsset(ctx, b.a, idList)
	if err != nil {
		return
	}
	for i := range b.out.Items {
		it := &b.out.Items[i]
		if it.State == StateDone && it.Stage != "duplicate" {
			it.Questions = todos.AboutClip(it.AssetID, counts[it.AssetID])
		}
	}
}

func qaVerdict(res map[string]any) string {
	errs, _ := res["errors"].(int32)
	warns, _ := res["warnings"].(int32)
	e, w := int(errs), int(warns)
	if v, ok := res["errors"].(int64); ok {
		e = int(v)
	}
	if v, ok := res["warnings"].(int64); ok {
		w = int(v)
	}
	// The quality check's words: a problem must be fixed, a thing worth checking may be fine.
	switch {
	case e > 0 && w > 0:
		return fmt.Sprintf("%s, %s worth checking", plural(e, "problem"), plural(w, "thing"))
	case e > 0:
		return plural(e, "problem") + " to fix"
	case w > 0:
		return plural(w, "thing") + " worth checking"
	}
	return "No problems found"
}

// okLabel says whose OK a conversation's waiting cards are for, to the person
// looking (viewer, D179): theirs ("Needs your OK"), one other person's
// ("Needs Maya's OK") or several people's. A card nobody in particular is
// asked (older conversations without a person) counts as the viewer's.
func okLabel(ws []assistant.WaitingCount, viewer string) string {
	var others []string
	for _, w := range ws {
		switch {
		case w.Count == 0:
		case viewer == "" || w.UserID == "" || w.UserID == viewer:
			return "Needs your OK"
		default:
			others = append(others, strings.TrimSpace(w.UserName))
		}
	}
	if len(others) == 1 && others[0] != "" {
		return "Needs " + others[0] + "'s OK"
	}
	return "Needs someone's OK"
}

func (b *builder) assistantRuns(projectID string) {
	// Claude Code conversations from every machine: the ones working now and
	// the ones whose last message ended recently.
	filter := bson.M{"workspaceId": b.a.WorkspaceID(), "archived": bson.M{"$ne": true},
		"$or": bson.A{bson.M{"state": assistant.ConvWorking}, bson.M{"updatedAt": bson.M{"$gte": b.now.Add(-b.recent)}}}}
	if projectID != "" {
		filter["projectId"] = projectID
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cur, err := b.a.DB.C(store.CAssistantConversations).Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "updatedAt", Value: -1}}).SetLimit(20).
			SetProjection(bson.M{"title": 1, "projectId": 1, "projectName": 1, "state": 1, "installationName": 1, "updatedAt": 1, "turns": 1, "actions": 1,
				"userId": 1, "userName": 1}))
	if err != nil {
		return
	}
	var convs []struct {
		ID               string                      `bson:"_id"`
		Title            string                      `bson:"title"`
		ProjectID        string                      `bson:"projectId"`
		ProjectName      string                      `bson:"projectName"`
		State            string                      `bson:"state"`
		InstallationName string                      `bson:"installationName"`
		UpdatedAt        time.Time                   `bson:"updatedAt"`
		UserID           string                      `bson:"userId"`
		UserName         string                      `bson:"userName"`
		Turns            []assistant.Turn            `bson:"turns"`
		Actions          []assistant.AssistantAction `bson:"actions"`
	}
	if cur.All(ctx, &convs) != nil {
		return
	}
	for _, c := range convs {
		if len(c.Turns) == 0 {
			continue
		}
		t := c.Turns[len(c.Turns)-1]
		it := Item{Key: "assistant:" + c.ID, Category: CatAssistant, Kind: "assistant", Title: "Claude Code: " + clip(c.Title, 80), Subtitle: orStr(c.ProjectName, "Workspace"),
			RunID: c.ID, ProjectID: c.ProjectID, Machine: c.InstallationName, UpdatedAt: c.UpdatedAt, Link: "/?ask=" + c.ID}
		if c.ProjectID != "" {
			it.Link = "/p/" + c.ProjectID + "?ask=" + c.ID
		}
		started := t.StartedAt
		it.StartedAt = &started
		asks := 0
		for _, a := range c.Actions {
			if a.State == assistant.ActionPending {
				asks++
			}
		}
		switch {
		case c.State == assistant.ConvWorking || t.State == assistant.TurnRunning:
			if b.now.Sub(t.StartedAt) > 2*time.Hour {
				continue // a turn the daemon lost; the panel marks it
			}
			it.State, it.Stage, it.StageLabel = StateActive, "running", "Working"
		case t.EndedAt == nil || b.now.Sub(*t.EndedAt) > b.recent:
			continue
		case t.State == assistant.TurnInterrupted || t.State == assistant.TurnStopped:
			continue
		case t.State == assistant.TurnSucceeded:
			it.UpdatedAt = *t.EndedAt
			it.State, it.Stage = StateDone, "done"
			switch {
			case asks > 0:
				cv := assistant.Conversation{UserID: c.UserID, UserName: c.UserName, Turns: c.Turns, Actions: c.Actions}
				it.StageLabel = okLabel(cv.WaitingCounts(), b.a.Actor.UserID)
			case t.Proposals == 1:
				it.StageLabel = "1 proposal ready"
			case t.Proposals > 1:
				it.StageLabel = fmt.Sprintf("%d proposals ready", t.Proposals)
			default:
				it.StageLabel = "Answered"
			}
			it.Pct = ptr(1)
		default:
			it.UpdatedAt = *t.EndedAt
			it.State, it.Stage, it.StageLabel, it.Error = StateFailed, "failed", "Did not finish", firstLine(t.Error)
			it.Problem, it.Hint = "Claude Code stopped before it finished its reply.", "Ask again in the Claude Code panel. Details says what went wrong."
			it.Pct = ptr(1)
		}
		b.out.Items = append(b.out.Items, it)
	}
}

// finish computes counts, wait groups, overall progress and the headline.
func (b *builder) finish() {
	o := b.out
	type gk struct{ code, machine string }
	groups := map[gk]*WaitGroup{}
	var order []gk
	addWait := func(r *jobs.WaitReason, jobID, typ string) {
		if r == nil || r.Code == jobs.WaitStarting {
			return
		}
		k := gk{r.Code, r.Machine}
		g, ok := groups[k]
		if !ok {
			g = &WaitGroup{Code: r.Code, Machine: r.Machine, BatteryPercent: r.BatteryPercent, Threshold: r.Threshold, CanProcessNow: r.CanProcessNow, JobIDs: []string{}}
			groups[k] = g
			order = append(order, k)
		}
		g.Count++
		g.JobIDs = append(g.JobIDs, jobID)
		g.Text = waitText(g, r, typ)
	}
	var sum float64
	n := 0
	for i := range o.Items {
		it := &o.Items[i]
		inFlight := it.State == StateActive || it.State == StateWaiting || it.State == StateInterrupted
		switch {
		case it.State == StateFailed:
			o.Counts.Failed++
		case it.State == StateDone:
			o.Counts.Done++
		}
		if inFlight {
			o.Counts.Active++
			switch it.Category {
			case CatUpload:
				if it.Stage == "processing" {
					o.Counts.Processing++
				} else {
					o.Counts.Uploads++
				}
			case CatProcessing:
				o.Counts.Processing++
			case CatRender:
				o.Counts.Renders++
			case CatQA:
				o.Counts.QA++
			case CatAssistant:
				o.Counts.Assistant++
			case CatNarration:
				o.Counts.Narration++
			case CatStorage:
				o.Counts.Storage++
			case CatRecipe:
				o.Counts.Recipes++
			case CatGenerate:
				if it.Stage == "processing" {
					o.Counts.Processing++
				} else {
					o.Counts.Generating++
				}
			case CatImport:
				if it.Stage == "processing" {
					o.Counts.Processing++
				} else {
					o.Counts.Imports++
				}
			}
			if it.Pct != nil {
				sum += *it.Pct
			}
			n++
		}
		for _, s := range it.Steps {
			if s.State == "queued" || s.State == "paused" {
				addWait(s.Waiting, s.JobID, s.Kind)
			}
		}
		if it.Waiting != nil && len(it.Steps) == 0 && it.JobID != "" {
			addWait(it.Waiting, it.JobID, it.Kind)
		}
	}
	for _, k := range order {
		g := groups[k]
		o.Waiting = append(o.Waiting, *g)
		o.Counts.Waiting += g.Count
		if g.Code == jobs.WaitBattery {
			o.Counts.Paused += g.Count
		}
	}
	if n > 0 {
		o.Progress = ptr(sum / float64(n))
	}
	// Newest and most urgent first: in flight, then failures, then done.
	rank := map[string]int{StateInterrupted: 0, StateActive: 1, StateWaiting: 2, StateFailed: 3, StateDone: 4}
	sort.SliceStable(o.Items, func(i, j int) bool {
		ri, rj := rank[o.Items[i].State], rank[o.Items[j].State]
		if ri != rj {
			return ri < rj
		}
		return o.Items[i].UpdatedAt.After(o.Items[j].UpdatedAt)
	})
	o.Headline = headline(o)
}

var waitNouns = map[string][2]string{"transcribe": {"transcription", "transcriptions"}, "render": {"render", "renders"},
	"mezzanine": {"steady copy", "steady copies"}, "qa": {"quality check", "quality checks"}, "proxy": {"preview copy", "preview copies"},
	"evidence": {"scene analysis", "scene analyses"}, "contact_sheet": {"frame grid", "frame grids"}, "waveform": {"waveform", "waveforms"},
	"thumbnail": {"thumbnail", "thumbnails"}, "speakers": {"speaker detection", "speaker detections"}, "narration": {"narration job", "narration jobs"},
	"import": {"video from a link", "videos from links"}, "fetch": {"web page or repository", "web pages or repositories"},
	"visual": {"check of what's on screen", "checks of what's on screen"}, "describe": {"scene description", "scene descriptions"},
	"generate": {"generated clip", "generated clips"}}

// waitText says what a group of waiting jobs waits for, short enough for the
// activity bar ("2 transcriptions paused: battery at 18% on laptop").
func waitText(g *WaitGroup, r *jobs.WaitReason, typ string) string {
	noun := "job"
	if v, ok := waitNouns[typ]; ok {
		noun = v[0]
		if g.Count != 1 {
			noun = v[1]
		}
	} else if g.Count != 1 {
		noun = "jobs"
	}
	what := fmt.Sprintf("%d %s", g.Count, noun)
	switch g.Code {
	case jobs.WaitBattery:
		if r.BatteryPercent != nil {
			return fmt.Sprintf("%s paused: battery at %d%% on %s", what, *r.BatteryPercent, r.Machine)
		}
		return fmt.Sprintf("%s paused: %s is on battery", what, r.Machine)
	case jobs.WaitNoWorker:
		if strings.HasPrefix(r.Text, jobs.PinnedWaitText) { // pinned jobs (link imports) wait for their machine
			return what + " waiting for MetaRush to run on " + orStr(r.Machine, "the computer that asked for it")
		}
		return what + " waiting for a machine running MetaRush"
	case jobs.WaitHeavyDisabled:
		return fmt.Sprintf("%s waiting: big jobs are turned off on %s", what, r.Machine)
	case jobs.WaitBusy:
		turn := "its turn"
		if g.Count != 1 {
			turn = "their turn"
		}
		return fmt.Sprintf("%s waiting %s on %s", what, turn, r.Machine)
	case jobs.WaitRetry:
		return what + " trying again after a problem"
	}
	return what + " waiting"
}

func headline(o *Summary) string {
	c := o.Counts
	var parts []string
	if c.Uploads > 0 {
		parts = append(parts, fmt.Sprintf("Uploading %s", plural(c.Uploads, "file")))
	}
	if c.Imports > 0 {
		parts = append(parts, fmt.Sprintf("importing %s from links", plural(c.Imports, "video")))
	}
	if c.Processing > 0 {
		parts = append(parts, fmt.Sprintf("processing %s", plural(c.Processing, "file")))
	}
	if c.Renders > 0 {
		parts = append(parts, plural(c.Renders, "render"))
	}
	if c.QA > 0 {
		parts = append(parts, plural(c.QA, "quality check"))
	}
	if c.Assistant > 0 {
		parts = append(parts, "Claude Code working")
	}
	if c.Narration == 1 {
		// One piece of narration: say exactly what it is doing ("making the AI voice for line 3").
		for _, it := range o.Items {
			if it.Category == CatNarration && (it.State == StateActive || it.State == StateWaiting) && it.StageLabel != "" {
				parts = append(parts, strings.ToLower(it.StageLabel[:1])+it.StageLabel[1:])
				break
			}
		}
	} else if c.Narration > 1 {
		parts = append(parts, fmt.Sprintf("making narration for %d cuts", c.Narration))
	}
	if c.Storage > 0 {
		parts = append(parts, "moving media to new storage")
	}
	if c.Generating > 0 {
		parts = append(parts, fmt.Sprintf("making %s with generative video", plural(c.Generating, "clip")))
	}
	if c.Recipes == 1 {
		// One run of Recipes: say what it is doing or waiting for (D204).
		for _, it := range o.Items {
			if it.Category == CatRecipe && (it.State == StateActive || it.State == StateWaiting) && it.StageLabel != "" {
				parts = append(parts, strings.ToLower(it.Title[:1])+it.Title[1:]+": "+strings.ToLower(it.StageLabel[:1])+it.StageLabel[1:])
				break
			}
		}
	} else if c.Recipes > 1 {
		parts = append(parts, fmt.Sprintf("%d runs of recipes", c.Recipes))
	}
	for _, w := range o.Waiting {
		if w.Code == jobs.WaitBattery || w.Code == jobs.WaitNoWorker || w.Code == jobs.WaitHeavyDisabled {
			parts = append(parts, w.Text)
		}
	}
	if c.Failed > 0 {
		parts = append(parts, plural(c.Failed, "failure")+" to review")
	}
	if len(parts) == 0 {
		if c.Done > 0 {
			return "All caught up"
		}
		return "Nothing running right now"
	}
	s := strings.Join(parts, ", ")
	return strings.ToUpper(s[:1]) + s[1:]
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %ss", n, one)
}

// andList joins words for a sentence: "a", "a and b", "a, b and c".
func andList(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func capFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func orStr(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return clip(s, 240)
}

func init() {
	ops.Def("activity.summary", "What MetaRush is doing now: uploads, processing, renders, QA and assistant runs with stages, progress, machines and why anything is waiting", ops.Flags{}, Build)
}
