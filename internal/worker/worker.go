// Package worker leases jobs from the shared queue and runs them on this machine.
// Any installation can be a worker; workers coordinate only through MongoDB (no
// network reachability between machines is required).
package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/abilities"
	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/components"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/linkimport"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/power"
	"github.com/metavert-io/metarush/internal/qa"
	"github.com/metavert-io/metarush/internal/remotion"
	"github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/storagemove"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/transcribe"
	"github.com/metavert-io/metarush/internal/version"
	"github.com/metavert-io/metarush/internal/vision"
)

// Permanent marks an error that retrying cannot fix.
type Permanent struct{ Err error }

func (p *Permanent) Error() string { return p.Err.Error() }
func (p *Permanent) Unwrap() error { return p.Err }

func permanent(format string, a ...any) error { return &Permanent{Err: fmt.Errorf(format, a...)} }

// postponed puts the job back in the queue for a moment (after), because
// something it needs isn't there yet; reason says so in plain words. It is not
// a failure: the attempt does not count (jobs.Queue.Postpone).
type postponed struct {
	after  time.Duration
	reason string
}

func (p *postponed) Error() string { return p.reason }

// Report updates a running job's progress.
type Report func(pct float64, msg string)

// Handler runs one job type.
type Handler func(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error)

// Worker is a job runner for one installation.
type Worker struct {
	App          *app.App
	Concurrency  int
	AcceptsHeavy bool
	// OnlyJobID is inline mode: run exactly this job to its end, then return.
	// A try that puts it back in the queue (a retry after a failed attempt, or a
	// wait for something it needs) is taken again here once its wait is over.
	OnlyJobID string
	// OnlyJobIDs limits the worker to these jobs until its context ends: a
	// terminal waiting for them (`--wait` with no capable worker online) runs
	// only what it waits for and never starts other work it would abandon. It
	// does not offer itself as a worker to other machines.
	OnlyJobIDs []string
	// IgnoreBattery lets an explicitly requested (CLI --wait) worker take heavy jobs on battery.
	IgnoreBattery bool
	Log           func(format string, a ...any)

	handlers map[string]Handler
	hmu      sync.RWMutex // guards handlers: Refresh changes them while jobs run (D324)
	running  int
	mu       sync.Mutex
	started  time.Time
	Tx       transcribe.Provider
	// Langs picks each clip's recognizer by the language it is spoken in
	// (D243); nil writes every clip down with Tx in the configured language
	// (tests that stand in a transcriber).
	Langs *transcribe.Languages
	Spk   *speakers.Engine // speaker detection (nil when not installed)

	// Battery deferral episode: when it began (nil while not deferring) and when
	// waiting heavy jobs were last checked for a notification.
	deferSince   *time.Time
	lastDeferChk time.Time
	// Battery overrides the battery reading (tests); nil reads the machine.
	Battery func() power.Battery
}

// Power is the battery reading and policy verdict for this worker right now.
type Power struct {
	Battery   power.Battery
	Threshold int
	Defer     bool   // heavy jobs wait (unless allowed on battery)
	Reason    string // why, when Defer
}

// Power reads the battery and applies worker.heavyBatteryThreshold. A worker
// started by an explicit `--wait` (IgnoreBattery) never defers.
func (w *Worker) Power() Power {
	read := w.Battery
	if read == nil {
		read = power.Read
	}
	p := Power{Battery: read(), Threshold: w.App.Cfg.HeavyBatteryThreshold()}
	if w.AcceptsHeavy && !w.IgnoreBattery {
		p.Defer, p.Reason = power.DeferHeavy(p.Battery, p.Threshold)
	}
	return p
}

// New builds a worker with every handler whose tools are available.
func New(a *app.App) *Worker {
	wa := *a // same connections, but actions are attributed to the worker channel
	wa.Actor.Via = "worker"
	a = &wa
	w := &Worker{App: a, Concurrency: a.Cfg.Worker.Concurrency, AcceptsHeavy: a.Cfg.Worker.HeavyRenders, Log: func(f string, x ...any) { fmt.Fprintf(os.Stderr, f+"\n", x...) }}
	w.handlers = map[string]Handler{}
	if a.Tools.Has(tools.FFmpeg) && a.Tools.Has(tools.FFprobe) {
		for _, t := range []string{jobs.TypeProxy, jobs.TypeThumbnail, jobs.TypeContactSheet, jobs.TypeWaveform, jobs.TypeMezzanine} {
			w.handlers[t] = deriveHandler
		}
		w.handlers[jobs.TypeEvidence] = evidenceHandler
		w.handlers[jobs.TypeQA] = qaHandler
		// Picture switches to the frame (D160), after the evidence and again
		// when the on-screen index adds what to look at.
		w.handlers[jobs.TypeSwitches] = switchesHandler
		// Fixes (D152, D153): measuring a source and making its fixed copy.
		w.handlers[jobs.TypeInspect] = inspectHandler
		w.handlers[jobs.TypePrepare] = prepareHandler
	}
	if l := transcribe.NewLanguages(a.Cfg.Transcription, a.Tools); l != nil {
		w.Langs, w.Tx = l, l.For(l.Default)
		if w.Tx == nil {
			w.Tx = l.Any()
		}
		w.handlers[jobs.TypeTranscribe] = transcribeHandler
	}
	if w.Spk = speakers.NewEngine(a); w.Spk != nil && a.Tools.Has(tools.FFmpeg) {
		w.handlers[jobs.TypeDiarize] = diarizeHandler
	}
	// Rendering needs Node.js and this version's renderer; JobTypes offers it
	// only while the renderer is ready or being set up (renderer.Usable).
	if a.Tools.Has(tools.Node) {
		w.handlers[jobs.TypeRender] = renderHandler
		// Remotion sources (D131–D138): bringing one in (pinned to the machine
		// that asked) and rendering its compositions into clips (any machine).
		w.handlers[jobs.TypeRemotionImport] = remotionImportHandler
		w.handlers[jobs.TypeRemotionRender] = remotionRenderHandler
		w.handlers[jobs.TypeRemotionTask] = remotionTaskHandler
		// A refresh a person started (D173–D175): pinned to the machine that
		// asked (its keys), its steps kept apart by the system's sandbox.
		w.handlers[jobs.TypeRemotionRefresh] = remotionRefreshHandler
	}
	// A run of an ability (D234): pinned to the machine that asked (its
	// programs and keys), in the sandbox; refused plainly where there is none.
	w.handlers[jobs.TypeAbilityRun] = abilityRunHandler
	// Sending a version's changes back to its repository (D176): pinned to the
	// machine that asked (its git and gh sign-in).
	if a.Tools.Has(tools.Git) {
		w.handlers[jobs.TypeRemotionPush] = remotionPushHandler
	}
	// What's on screen (Apple Vision, macOS): the helper is built on first use
	// when only swiftc is there.
	if vision.CanRun(a.Tools) {
		w.handlers[jobs.TypeVisual] = visualHandler
	}
	// Scene descriptions are pinned to the machine that asked (its Claude Code).
	w.handlers[jobs.TypeDescribe] = describeHandler
	// Narration runs in the process that asked for it (it holds the lease); a
	// worker only ever sees one whose process stopped, and closes it plainly.
	w.handlers[jobs.TypeVoiceover] = interruptedNarrationHandler
	// Link imports are pinned to the machine that asked (jobs.Lease only hands a
	// worker its own); without yt-dlp the job fails at once with install advice
	// instead of waiting forever.
	w.handlers[jobs.TypeImport] = importHandler
	// Moving the media runs in the terminal that asked (pinned to this
	// machine); a worker only closes one whose process stopped.
	w.handlers[jobs.TypeStorageMove] = func(context.Context, *Worker, *models.Job, Report) (map[string]any, error) {
		return nil, &Permanent{Err: errors.New(storagemove.Interrupted)}
	}
	// A linked folder's large file is uploaded by the machine that has it, the
	// first time a cut uses it (pinned to that machine).
	w.handlers[jobs.TypeResourceUpload] = resourceUploadHandler
	// Repositories and web pages are fetched by the machine that asked (its
	// network, its git and gh sign-in): pinned jobs, like link imports.
	w.handlers[jobs.TypeFetch] = fetchHandler
	// Generated video is made by the machine that asked (its provider key):
	// pinned jobs that resume from the provider's ticket after a restart.
	w.handlers[jobs.TypeGenerate] = generateHandler
	return w
}

// generateHandler makes a generated video (internal/generate): a wait for a
// provider's limit puts the job back in the queue; a refusal or a request
// that can't be made fails it for good.
func generateHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	run := generate.Run
	if aiwork.Handles(j) {
		run = aiwork.Run // other kinds of AI work share the job type and its rules (D226)
	}
	res, err := run(ctx, w.App, j, fullProgress(ctx))
	var wait *generate.Wait
	var final *generate.Final
	switch {
	case errors.As(err, &wait):
		return nil, &postponed{after: wait.After, reason: wait.Reason}
	case errors.As(err, &final):
		return nil, &Permanent{Err: final.Err}
	}
	return res, err
}

type fullReportKey struct{}

// fullProgress is a handler's way to report stage, bytes and rate (imports),
// beyond Report's percent and message.
func fullProgress(ctx context.Context) func(models.Progress) {
	f, _ := ctx.Value(fullReportKey{}).(func(models.Progress))
	if f == nil {
		return func(models.Progress) {}
	}
	return f
}

// remotionImportHandler brings a Remotion project in (internal/remotion).
func remotionImportHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	res, err := remotion.RunImport(ctx, w.App, j, func(pct float64, msg string) { report(pct, msg) })
	if err != nil && remotion.IsFinal(err) {
		return nil, &Permanent{Err: err}
	}
	return res, err
}

// remotionRenderHandler renders a composition into a clip (internal/remotion).
func remotionRenderHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	res, err := remotion.RunRender(ctx, w.App, j, func(pct float64, msg string) { report(pct, msg) })
	if err != nil && remotion.IsFinal(err) {
		return nil, &Permanent{Err: err}
	}
	return res, err
}

// remotionTaskHandler maps a source, prepares or checks its word settings, or
// lifts a scene into a motion graphic (internal/remotion, D167–D171).
func remotionTaskHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	res, err := remotion.RunTask(ctx, w.App, j, func(pct float64, msg string) { report(pct, msg) })
	if err != nil && remotion.IsFinal(err) {
		return nil, &Permanent{Err: err}
	}
	return res, err
}

// remotionRefreshHandler runs a Remotion source's own steps a person started
// (internal/remotion, D173–D175); a refresh is never retried by itself.
func remotionRefreshHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	res, err := remotion.RunRefreshJob(ctx, w.App, j, func(pct float64, msg string) { report(pct, msg) })
	if err != nil && remotion.IsFinal(err) {
		return nil, &Permanent{Err: err}
	}
	return res, err
}

// abilityRunHandler runs an approved ability (internal/abilities, D234).
func abilityRunHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	res, err := abilities.RunJob(ctx, w.App, j, func(pct float64, msg string) { report(pct, msg) })
	if err != nil && abilities.IsFinal(err) {
		return nil, &Permanent{Err: err}
	}
	return res, err
}

// remotionPushHandler sends an approved version's changes back to its
// repository (internal/remotion, D176).
func remotionPushHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	res, err := remotion.RunPushJob(ctx, w.App, j, func(pct float64, msg string) { report(pct, msg) })
	if err != nil && remotion.IsFinal(err) {
		return nil, &Permanent{Err: err}
	}
	return res, err
}

// importHandler downloads a linked video and adds it (internal/linkimport).
func importHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	res, err := linkimport.Run(ctx, w.App, j, fullProgress(ctx))
	if err != nil && !linkimport.Retryable(err) {
		return nil, &Permanent{Err: err}
	}
	return res, err
}

// JobTypes lists what this worker can run now. Rendering is offered only while
// this version's renderer is ready or being set up, so a machine whose
// renderer was removed never takes renders other machines could do.
func (w *Worker) JobTypes() []string {
	var out []string
	w.hmu.RLock()
	defer w.hmu.RUnlock()
	for t := range w.handlers {
		if (t == jobs.TypeRender || t == jobs.TypeRemotionImport || t == jobs.TypeRemotionRender || t == jobs.TypeRemotionTask || t == jobs.TypeRemotionRefresh) && !renderer.New(w.App.Cfg, w.App.Tools).Usable() {
			continue
		}
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Can reports whether the worker handles a job type now.
func (w *Worker) Can(t string) bool { return slices.Contains(w.JobTypes(), t) }

// Handle makes the worker run h for jobs of type t, in place of the handler
// New chose (tests run commands with stand-in handlers).
func (w *Worker) Handle(t string, h Handler) {
	w.hmu.Lock()
	defer w.hmu.Unlock()
	w.handlers[t] = h
}

func (w *Worker) handler(t string) Handler {
	w.hmu.RLock()
	defer w.hmu.RUnlock()
	return w.handlers[t]
}

// componentGated are the job types a component brings (D324): Refresh adds
// or takes them away as components are added or removed.
var componentGated = []string{jobs.TypeProxy, jobs.TypeThumbnail, jobs.TypeContactSheet, jobs.TypeWaveform, jobs.TypeMezzanine, jobs.TypeEvidence,
	jobs.TypeQA, jobs.TypeSwitches, jobs.TypeInspect, jobs.TypePrepare, jobs.TypeTranscribe, jobs.TypeDiarize, jobs.TypeRender, jobs.TypeRemotionImport,
	jobs.TypeRemotionRender, jobs.TypeRemotionTask, jobs.TypeRemotionRefresh, jobs.TypeRemotionPush, jobs.TypeVisual}

// Refresh looks again at what this computer can do after a component was
// added or removed while MetaRush runs (D324): work whose programs are here
// now is taken on, and the transcriber is chosen again.
func (w *Worker) Refresh() {
	n := New(w.App)
	w.hmu.Lock()
	for _, t := range componentGated {
		if h, ok := n.handlers[t]; ok {
			w.handlers[t] = h
		} else {
			delete(w.handlers, t)
		}
	}
	w.hmu.Unlock()
	w.mu.Lock()
	w.Langs, w.Tx, w.Spk = n.Langs, n.Tx, n.Spk
	w.mu.Unlock()
}

// Lacks are the kinds of work this computer can't do for want of a
// component, with the words a waiting job says (D326).
func (w *Worker) Lacks() []models.WorkerLack {
	can := map[string]bool{}
	for _, t := range w.JobTypes() {
		can[t] = true
	}
	var out []models.WorkerLack
	lack := func(t, component string) {
		if can[t] {
			return
		}
		out = append(out, models.WorkerLack{JobType: t, Component: component, Says: components.Missing(component).Message})
	}
	ts := w.App.Tools
	video := ts.Has(tools.FFmpeg) && ts.Has(tools.FFprobe)
	if !video {
		for _, t := range []string{jobs.TypeProxy, jobs.TypeThumbnail, jobs.TypeContactSheet, jobs.TypeWaveform, jobs.TypeMezzanine, jobs.TypeEvidence,
			jobs.TypeQA, jobs.TypeSwitches, jobs.TypeInspect, jobs.TypePrepare} {
			lack(t, "ffmpeg")
		}
	}
	tx := "transcription"
	if !video {
		tx = "ffmpeg"
	}
	lack(jobs.TypeTranscribe, tx)
	lack(jobs.TypeDiarize, tx)
	for _, t := range []string{jobs.TypeRender, jobs.TypeRemotionImport, jobs.TypeRemotionRender, jobs.TypeRemotionTask, jobs.TypeRemotionRefresh} {
		lack(t, renderer.Component)
	}
	if runtime.GOOS == "darwin" {
		lack(jobs.TypeVisual, "screen-index")
	}
	return out
}

func (w *Worker) device() models.WorkerDevice {
	a := w.App
	tv := map[string]string{}
	for _, t := range []string{tools.FFmpeg, tools.FFprobe, tools.Node} {
		if v := a.Tools.Version(context.Background(), t); v != "" {
			tv[t] = v
		}
	}
	if w.Tx != nil {
		tv["transcriber"] = w.Tx.Name() + " " + w.Tx.Model()
	}
	if RenderReady(a) == nil {
		tv["remotion"] = remotionVersion(a)
	}
	w.mu.Lock()
	running := w.running
	since := w.deferSince
	w.mu.Unlock()
	p := w.Power()
	d := models.WorkerDevice{
		ID: a.Cfg.Installation.ID, WorkspaceID: a.WorkspaceID(), Name: a.Cfg.Installation.Name, Hostname: app.Hostname(), OS: runtime.GOOS, Arch: runtime.GOARCH,
		CPUs: runtime.NumCPU(), MemoryGB: memoryGB(), Tools: tv, JobTypes: w.JobTypes(), Lacks: w.Lacks(), Concurrency: w.Concurrency, Running: running,
		AcceptsHeavy: w.AcceptsHeavy && !p.Defer, OnBattery: p.Battery.OnBattery(), Charging: p.Battery.Charging,
		HeavyBatteryThreshold: p.Threshold, HeavyEnabled: w.AcceptsHeavy, DeferringHeavy: p.Defer, DeferReason: p.Reason,
		Version: version.Version, StartedAt: w.started, LastHeartbeat: store.Clock().UTC(), // the server's time when beat can ask it
	}
	if p.Battery.Present {
		pct := p.Battery.Percent
		d.BatteryPercent = &pct
	}
	if p.Defer {
		d.DeferSince = since
	}
	return d
}

// resumeEpisodeWindow is how recently this machine must have reported a battery
// deferral for a restart to continue that episode rather than start a new one.
const resumeEpisodeWindow = 15 * time.Minute

// resumeDeferral continues a battery episode across a restart: when this
// machine's last published state was deferring (recently) and it still is, the
// episode keeps its start, so people get one notification, not one per restart.
func (w *Worker) resumeDeferral(ctx context.Context) {
	p := w.Power()
	if !p.Defer {
		return
	}
	var prev models.WorkerDevice
	if err := w.App.DB.GetByID(ctx, store.CWorkers, w.App.WorkspaceID(), w.App.Cfg.Installation.ID, &prev); err != nil {
		return
	}
	// The heartbeat is the database server's time (beat).
	if !prev.DeferringHeavy || prev.DeferSince == nil || w.serverNow(ctx).Sub(prev.LastHeartbeat) > resumeEpisodeWindow {
		return
	}
	since := prev.DeferSince.UTC()
	w.mu.Lock()
	if w.deferSince == nil {
		w.deferSince = &since
	}
	w.mu.Unlock()
}

// trackDeferral notices when deferral starts or ends (publishing the device at
// once so dashboards show the reason) and notifies the people whose heavy jobs
// are waiting, once per episode.
func (w *Worker) trackDeferral(ctx context.Context) {
	p := w.Power()
	now := time.Now().UTC()
	w.mu.Lock()
	changed := (w.deferSince != nil) != p.Defer
	if changed {
		if p.Defer {
			w.deferSince = &now
		} else {
			w.deferSince = nil
		}
		w.lastDeferChk = time.Time{}
	}
	since := w.deferSince
	check := p.Defer && time.Since(w.lastDeferChk) > 10*time.Second
	if check {
		w.lastDeferChk = time.Now()
	}
	w.mu.Unlock()
	if changed {
		if p.Defer {
			w.Log("worker %s: deferring heavy jobs (%s)", w.App.Cfg.Installation.Name, p.Reason)
		} else {
			w.Log("worker %s: taking heavy jobs again", w.App.Cfg.Installation.Name)
		}
		if w.OnlyJobID == "" {
			w.beat(ctx)
		}
	}
	if !check || since == nil {
		return
	}
	a := w.App
	// Due and online by the database server's time: other computers wrote
	// both (D360).
	var waiting []models.Job
	err := a.DB.Find(ctx, store.CJobs, a.WorkspaceID(), bson.M{"state": models.JobQueued, "heavy": true, "allowOnBattery": bson.M{"$ne": true},
		"cancelRequested": false, "type": bson.M{"$in": w.JobTypes()}, "$expr": bson.M{"$lte": bson.A{"$nextRunAt", "$$NOW"}}}, &waiting, options.Find().SetLimit(200))
	if err != nil || len(waiting) == 0 {
		return
	}
	// Another online machine that takes heavy jobs will run them: nothing is paused.
	var others []models.WorkerDevice
	online := store.SeenWithin("lastHeartbeat", jobs.OnlineWindow)
	online["_id"], online["acceptsHeavy"] = bson.M{"$ne": a.Cfg.Installation.ID}, true
	_ = a.DB.Find(ctx, store.CWorkers, a.WorkspaceID(), online, &others)
	var paused []models.Job
	for _, j := range waiting {
		taken := false
		for _, o := range others {
			if slices.Contains(o.JobTypes, j.Type) {
				taken = true
				break
			}
		}
		if !taken {
			paused = append(paused, j)
		}
	}
	if len(paused) > 0 {
		notify.BatteryPaused(ctx, a, a.Cfg.Installation.Name, p.Battery.Percent, p.Threshold, *since, paused)
	}
}

func (w *Worker) beat(ctx context.Context) {
	d := w.device()
	// Other computers judge whether this one is online by its heartbeat:
	// the database server's time (D360).
	if now, err := w.App.DB.Now(ctx); err == nil {
		d.LastHeartbeat = now
	}
	_, err := w.App.DB.C(store.CWorkers).ReplaceOne(ctx, bson.M{"_id": d.ID, "workspaceId": d.WorkspaceID}, d, options.Replace().SetUpsert(true))
	if err != nil {
		w.Log("worker heartbeat failed (database unreachable?): %v", err)
	}
}

// Run leases and executes jobs until ctx ends (or, in inline mode, the job ends).
func (w *Worker) Run(ctx context.Context) error {
	w.hmu.RLock()
	none := len(w.handlers) == 0
	w.hmu.RUnlock()
	if none {
		return apperr.New(apperr.ToolMissing, "This machine can't do any MetaRush jobs yet: the programs they need (ffmpeg and ffprobe) aren't installed.").
			WithHint("Add FFmpeg in Settings → Components.").WithCommand("metarush components add ffmpeg")
	}
	if w.Concurrency <= 0 {
		w.Concurrency = 1
	}
	w.started = time.Now().UTC()
	// A worker restricted to some jobs (a terminal's wait) is not a worker the
	// workspace can count on: it does not publish itself or track battery.
	advertise := w.OnlyJobID == "" && len(w.OnlyJobIDs) == 0
	if advertise {
		w.resumeDeferral(ctx)
		w.beat(ctx)
		w.Log("worker %s ready: %s (concurrency %d)", w.App.Cfg.Installation.Name, strings.Join(w.JobTypes(), ", "), w.Concurrency)
	}
	slots := make(chan struct{}, w.Concurrency)
	var wg sync.WaitGroup
	beat := time.NewTicker(15 * time.Second)
	defer beat.Stop()
	poll := time.NewTicker(1500 * time.Millisecond)
	defer poll.Stop()
	inlineDone := make(chan struct{})
	inlineAgain := make(chan time.Time, 1) // inline mode: the job went back in the queue (a retry, a wait) until then
	inlineStarted := false                 // inline mode: this worker leased the job and is running it
	for {
		if w.OnlyJobID == "" {
			select {
			case <-ctx.Done():
				wg.Wait()
				return nil
			case <-beat.C:
				if advertise {
					w.beat(ctx)
				}
				continue
			case <-poll.C:
			}
			if advertise {
				w.trackDeferral(ctx)
			}
		}
		for len(slots) < cap(slots) {
			if w.OnlyJobID != "" && inlineStarted {
				// Inline: our job is running, or waiting until it may be taken
				// again (LeaseByID would take it at once, skipping that wait).
				break
			}
			j, err := w.lease(ctx)
			if err != nil {
				w.Log("lease failed: %v", err)
				break
			}
			if j == nil {
				break
			}
			slots <- struct{}{}
			wg.Add(1)
			go func(j *models.Job) {
				defer wg.Done()
				defer func() { <-slots }()
				again := w.execute(ctx, j)
				if w.OnlyJobID != "" {
					if !again.IsZero() {
						inlineAgain <- again
						return
					}
					close(inlineDone)
				}
			}(j)
			if w.OnlyJobID != "" {
				inlineStarted = true
				break
			}
		}
		if w.OnlyJobID != "" {
			select {
			case <-inlineDone:
				wg.Wait()
				return nil
			case at := <-inlineAgain:
				// It went back in the queue: for its next try after a failed
				// attempt (Fail with a retry), or to wait for something it needs
				// (Postpone). Take it again the moment the wait is over: nothing
				// else runs it (often no worker is online at all).
				wg.Wait()
				inlineStarted = false
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(at.Sub(w.serverNow(ctx))): // at is the server's time
				}
			case <-ctx.Done():
				wg.Wait()
				return ctx.Err()
			case <-time.After(2 * time.Second):
				if inlineStarted {
					continue // our own job is still running: keep waiting for it
				}
				// job not leasable yet (another worker may own it); check its state
				j, err := w.App.Queue.Get(ctx, w.OnlyJobID)
				if err != nil {
					return err
				}
				if j.State != models.JobQueued && j.State != models.JobRunning {
					return nil
				}
				if j.State == models.JobRunning && j.Lease != nil && j.Lease.ExpiresAt.After(w.serverNow(ctx)) {
					return fmt.Errorf("job %s is already running on %s", j.ID, j.Lease.OwnerName)
				}
			}
		}
	}
}

func (w *Worker) lease(ctx context.Context) (*models.Job, error) {
	q := w.App.Queue
	wk := jobs.Worker{ID: w.App.Cfg.Installation.ID, Name: w.App.Cfg.Installation.Name, JobTypes: w.JobTypes(), AcceptsHeavy: w.AcceptsHeavy, DeferHeavy: w.Power().Defer}
	if w.OnlyJobID != "" {
		return q.LeaseByID(ctx, wk, w.OnlyJobID)
	}
	if len(w.OnlyJobIDs) > 0 {
		return q.LeaseAmong(ctx, wk, w.OnlyJobIDs)
	}
	return q.Lease(ctx, wk)
}

// execute runs one leased job to its end. When the job is back in the queue for
// another try — the attempt failed and will be retried, or the handler
// postponed it (it waits for something it needs) — again is when it may run,
// in the database server's time; otherwise (done, failed for good, canceled,
// released, taken over) again is zero.
func (w *Worker) execute(parent context.Context, j *models.Job) (again time.Time) {
	a := w.App
	w.mu.Lock()
	w.running++
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.running--
		w.mu.Unlock()
	}()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	// Program runs the handler starts belong to this job in the activity log.
	ctx = logbook.WithJob(ctx, j.ID, logbook.Refs{ProjectID: j.ProjectID, AssetID: j.AssetID, RenderID: j.RenderID})
	ctx, releaseCache := a.Cache.Hold(ctx) // cached objects this job reads stay leased (never pruned mid-job)
	defer releaseCache()
	var pmu sync.Mutex
	prog := models.Progress{Pct: 0, Message: "starting"}
	report := func(pct float64, msg string) {
		pmu.Lock()
		prog = models.Progress{Pct: pct, Message: msg}
		pmu.Unlock()
	}
	ctx = context.WithValue(ctx, fullReportKey{}, func(p models.Progress) {
		pmu.Lock()
		prog = p
		pmu.Unlock()
	})
	canceled := false
	lost := false
	hbDone := make(chan struct{})
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-hbDone:
				return
			case <-t.C:
				pmu.Lock()
				p := prog
				pmu.Unlock()
				c, err := a.Queue.Heartbeat(parent, j, a.Cfg.Installation.ID, p)
				if errors.Is(err, jobs.ErrLostLease) {
					lost = true
					cancel()
					return
				}
				if c {
					canceled = true
					cancel()
					return
				}
			}
		}
	}()
	w.Log("job %s %s started", j.ID, j.Type)
	start := time.Now()
	result, err := w.handler(j.Type)(ctx, w, j, report)
	close(hbDone)
	fctx := context.WithoutCancel(parent)
	var wait *postponed
	switch {
	case lost:
		w.Log("job %s: lease lost; another worker owns it now — discarding local result", j.ID)
	case canceled:
		_ = a.Queue.Canceled(fctx, j, a.Cfg.Installation.ID)
		markRender(fctx, a, j, models.RenderCanceled, "canceled")
		w.Log("job %s canceled", j.ID)
		w.settled(fctx, j, models.JobCanceled, nil)
	case err != nil && parent.Err() != nil:
		// This worker is stopping (a terminal's wait ended, the daemon shuts
		// down): the job goes back in the queue as it was; it never fails for it.
		if rerr := a.Queue.Release(fctx, j, a.Cfg.Installation.ID); rerr != nil {
			w.Log("job %s %s: could not put it back in the queue: %v", j.ID, j.Type, rerr)
		} else {
			w.Log("job %s %s put back in the queue: this worker is stopping", j.ID, j.Type)
		}
	case errors.As(err, &wait):
		// Something it needs isn't there yet: back in the queue for a moment,
		// never a failure (the handler decides when to stop waiting).
		if perr := a.Queue.Postpone(fctx, j, a.Cfg.Installation.ID, wait.after, wait.reason); perr != nil {
			w.Log("job %s %s: could not put it back in the queue to wait: %v", j.ID, j.Type, perr)
		} else {
			again = retryAt(fctx, w, j, wait.after)
			w.Log("job %s %s: %s It starts again in %s.", j.ID, j.Type, wait.reason, wait.after)
		}
	case err != nil:
		var perm *Permanent
		retry := !errors.As(err, &perm)
		ferr := a.Queue.Fail(fctx, j, a.Cfg.Installation.ID, err, retry)
		if !retry || j.AttemptCount >= j.MaxAttempts {
			// The render says what went wrong in plain words; the job keeps the technical error.
			markRender(fctx, a, j, models.RenderFailed, plainFailure(err))
			markAnalysis(fctx, a, j, "failed")
			w.settled(fctx, j, models.JobFailed, nil)
		} else if ferr == nil {
			// Back in the queue for its next try: say when (a terminal running
			// only this job takes it again then, instead of leaving it waiting
			// for a worker that may never come).
			again = retryAt(fctx, w, j, jobs.RetryBackoff(j.AttemptCount))
		}
		w.Log("job %s %s failed after %s: %v", j.ID, j.Type, time.Since(start).Round(time.Millisecond), err)
	default:
		if result == nil {
			result = map[string]any{}
		}
		result["seconds"] = time.Since(start).Seconds()
		if err := a.Queue.Succeed(fctx, j, a.Cfg.Installation.ID, result); err != nil {
			w.Log("job %s finished but could not be marked done: %v", j.ID, err)
		} else {
			w.Log("job %s %s done in %s", j.ID, j.Type, time.Since(start).Round(time.Millisecond))
			w.settled(fctx, j, models.JobSucceeded, result)
		}
	}
	return again
}

// retryAt is when a job that went back in the queue may run again, in the
// database server's time: its nextRunAt as the queue saved it (after a failed
// attempt's backoff, or a postponement's wait). It is zero when the job has
// moved on already (canceled, or taken by another worker).
func retryAt(ctx context.Context, w *Worker, j *models.Job, wait time.Duration) time.Time {
	cur, err := w.App.Queue.Get(ctx, j.ID)
	switch {
	case err != nil:
		// The database didn't answer: the job waits as the queue saved it.
		return w.serverNow(ctx).Add(wait)
	case cur.State != models.JobQueued:
		return time.Time{}
	case cur.NextRunAt.IsZero():
		return w.serverNow(ctx)
	}
	return cur.NextRunAt
}

// serverNow is the database server's time: leases, heartbeats and when a job
// may run again are written in it (D360).
func (w *Worker) serverNow(ctx context.Context) time.Time { return w.App.DB.NowOrLocal(ctx) }

// settled writes the notifications a terminal job can complete: "file ready"
// once all of an asset's derivative jobs are terminal, a render's outcome, a QA
// report, a speaker detection someone asked for. Shared by the daemon, headless
// workers and inline CLI workers.
func (w *Worker) settled(ctx context.Context, j *models.Job, state string, result map[string]any) {
	a := w.App
	// Only a derivative job can complete "file ready"; other jobs about a clip
	// (speakers, what's on screen, scene descriptions) would only re-send it, for
	// example after the clip was renamed.
	if j.AssetID != "" && slices.Contains(notify.DerivativeTypes, j.Type) {
		notify.AssetSettled(ctx, a, j.AssetID)
	}
	// A review copy MetaRush made by itself to follow the latest version
	// (D187) rings the bell only when it failed or its check found something:
	// the cut shows it ready, and every hand edit would otherwise ring twice.
	follow, _ := j.Payload["follow"].(bool)
	if j.Type == jobs.TypeRender && j.RenderID != "" && state != models.JobCanceled && (!follow || state != models.JobSucceeded) {
		notify.RenderOutcome(ctx, a, j.RenderID)
	}
	if j.Type == jobs.TypeQA && state == models.JobSucceeded {
		if id, _ := result["reportId"].(string); id != "" {
			if follow {
				notify.QAProblems(ctx, a, j, id)
			} else {
				notify.QADone(ctx, a, j, id)
			}
		}
	}
	if j.Type == jobs.TypeDiarize {
		notify.SpeakersDone(ctx, a, j, state)
	}
	if (j.Type == jobs.TypeEvidence || j.Type == jobs.TypeVisual) && state == models.JobSucceeded && j.AssetID != "" {
		queueSwitches(ctx, w, j)
	}
	if j.Type == jobs.TypeVisual || j.Type == jobs.TypeDescribe {
		if done, err := a.Queue.Get(ctx, j.ID); err == nil {
			notify.ClipLooked(ctx, a, done, state)
		}
	}
	if j.Type == jobs.TypeVoiceover && state == models.JobFailed {
		if done, err := a.Queue.Get(ctx, j.ID); err == nil {
			notify.NarrationOutcome(ctx, a, done)
		}
	}
	if j.Type == jobs.TypeImport && state == models.JobFailed {
		if done, err := a.Queue.Get(ctx, j.ID); err == nil {
			notify.ImportFailed(ctx, a, done)
		}
	}
	if j.Type == jobs.TypeRender && j.RenderID != "" && state == models.JobSucceeded {
		if noQA, _ := j.Payload["noQA"].(bool); !noQA {
			w.queueQA(ctx, j)
		}
	}
	// What MetaRush knows about the clip changed: its descriptive name and
	// what its fixes measure follow (D150, D152).
	if j.AssetID != "" && state != models.JobCanceled && slices.Contains(factsJobTypes, j.Type) {
		ops.SourceFactsChanged(ctx, a, j.AssetID, ops.FactsAnalysis)
	}
}

// factsJobTypes are the analyses whose results say what a clip is (its name
// and its fixes' measurements).
var factsJobTypes = []string{jobs.TypeProxy, jobs.TypeTranscribe, jobs.TypeDiarize, jobs.TypeVisual, jobs.TypeEvidence}

// interruptedNarrationHandler closes a narration job whose process stopped
// (its lease expired): narration needs the machine, key and files of the
// process that started it, so the person starts it again.
func interruptedNarrationHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	return nil, &Permanent{Err: errors.New("MetaRush stopped while doing this. Start it again: lines that were already made are reused at no cost.")}
}

// queueQA checks every finished render automatically (QA, including listening
// to the speech), for the person who asked for the render. The key matches
// `metarush qa run`, so asking again returns the same check.
func (w *Worker) queueQA(ctx context.Context, j *models.Job) {
	a := w.App
	var rd models.Render
	if err := a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), j.RenderID, &rd); err != nil || rd.State != models.RenderAvailable {
		return
	}
	payload := map[string]any{"renderId": rd.ID, "revisionId": rd.RevisionID, "noSpeechCheck": false, "auto": true}
	if rd.Follow {
		payload["follow"] = true // its outcome rings the bell only for problems (D187)
	}
	_, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeQA, IdempotencyKey: "qa:" + rd.ID + ":" + qa.Version, Priority: 12, ProjectID: rd.ProjectID,
		Payload: payload, MaxAttempts: 2, Actor: j.CreatedBy})
	if err != nil {
		w.Log("render %s: could not queue its quality check: %v", rd.ID, err)
	}
}

func markRender(ctx context.Context, a *app.App, j *models.Job, state, msg string) {
	if j.RenderID == "" {
		return
	}
	_ = a.DB.Update(ctx, store.CRenders, a.WorkspaceID(), j.RenderID, bson.M{"$set": bson.M{"state": state, "error": msg}})
}

func markAnalysis(ctx context.Context, a *app.App, j *models.Job, state string) {
	if j.AssetID == "" {
		return
	}
	set := bson.M{"analysis." + j.Type: state, "updatedAt": time.Now().UTC()}
	if j.Type == jobs.TypeTranscribe {
		set["transcript.state"] = state
	}
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), j.AssetID, bson.M{"$set": set})
}
