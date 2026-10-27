package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/coverflush"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/naming"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/worker"
)

func home() (string, error) { return config.ResolveHome(homeFlag) }

// openApp loads the installation and connects to shared state.
func openApp(c *cobra.Command, via string) (*app.App, error) {
	h, err := home()
	if err != nil {
		return nil, err
	}
	if via == "cli" && assistantMode() {
		via = models.ViaAssistant // ops refuse every mutating op on this channel
	}
	ctx, cancel := context.WithTimeout(c.Context(), 20*time.Second)
	defer cancel()
	a, err := app.Open(ctx, h, via)
	if err != nil {
		if ae := apperr.From(err); ae.Code == apperr.UnsafeSecretPath || ae.Code == apperr.Database || ae.Code == apperr.Conflict {
			return nil, failErr(c, ae)
		}
		code := "E_NOT_CONFIGURED"
		if strings.Contains(err.Error(), "MongoDB") {
			code = "E_DATABASE"
		}
		return nil, failErr(c, apperr.New(code, "%s", err.Error()).WithHint(checkupHint).WithCommand("metarush doctor"))
	}
	// An AI app names itself, and may be held to proposing (D248).
	a.Actor.Agent, a.ProposeOnly = agentFlags(c)
	return a, nil
}

// checkupHint points a person at MetaRush's checkup (`metarush doctor`, the
// command itself is kept with --details).
const checkupHint = "MetaRush's checkup (doctor) says what is wrong and how to fix it."

// shellArg quotes a word for a shell when it needs it.
func shellArg(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@%+,", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// commandLine is a command to type, for --details: name is the command
// ("metarush edit apply"), args are quoted for a shell, and the --home this
// command was given comes along so it reaches the same installation.
func commandLine(c *cobra.Command, name string, args ...string) string {
	parts := []string{name}
	for _, a := range args {
		parts = append(parts, shellArg(a))
	}
	if f := c.Flags().Lookup("home"); f != nil && f.Changed {
		parts = append(parts, "--home", shellArg(f.Value.String()))
	}
	return strings.Join(parts, " ")
}

// sameCommandWith is the command that ran, as typed (without --details,
// --json, --dry-run and --confirm, which only show or check), plus extra: the
// exact command that saves a change this one checked.
func sameCommandWith(c *cobra.Command, extra ...string) string {
	parts := []string{c.CommandPath()}
	for _, a := range c.Flags().Args() {
		parts = append(parts, shellArg(a))
	}
	c.Flags().Visit(func(f *pflag.Flag) {
		switch f.Name {
		case "details", "json", "dry-run", "confirm":
			return
		}
		switch v := f.Value.(type) {
		case pflag.SliceValue:
			for _, s := range v.GetSlice() {
				parts = append(parts, "--"+f.Name, shellArg(s))
			}
		default:
			if f.Value.Type() == "bool" {
				if f.Value.String() == "true" {
					parts = append(parts, "--"+f.Name)
				} else {
					parts = append(parts, "--"+f.Name+"=false")
				}
				return
			}
			parts = append(parts, "--"+f.Name, shellArg(f.Value.String()))
		}
	})
	for _, e := range extra {
		parts = append(parts, shellArg(e))
	}
	return strings.Join(parts, " ")
}

// signalCtx is canceled by Ctrl-C or SIGTERM (the daemon, the worker and long
// commands stop gracefully). In a coverage build the counters are written as
// soon as the signal arrives (D195): a daemon a script stops might not get
// to exit by itself.
func signalCtx(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	flushStop := coverflush.OnSignal(os.Interrupt, syscall.SIGTERM)
	return ctx, func() {
		flushStop()
		stop()
	}
}

// runOp opens the app, calls an op with typed input and prints the result.
func runOp(c *cobra.Command, name string, in any, human func(out any)) error {
	a, err := openApp(c, "cli")
	if err != nil {
		return err
	}
	defer a.Close()
	return runOpWith(c, a, name, in, ops.Caller{}, human)
}

func runOpWith(c *cobra.Command, a *app.App, name string, in any, caller ops.Caller, human func(out any)) error {
	ctx, cancel := signalCtx(c.Context())
	defer cancel()
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	if interactive() && a.Actor.Agent == "" {
		ctx = naming.With(ctx, models.NamedByPerson) // a person at a terminal names faces and voices (D274)
	}
	out, err := ops.Call(ctx, a, name, b, caller)
	if err != nil {
		return failErr(c, err)
	}
	return emit(c, out, func() {
		if human != nil {
			human(out)
		} else {
			printJSON(out)
		}
	})
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// readPlan reads a plan from a file or stdin ("-").
func readPlan(path string) (json.RawMessage, error) {
	var r io.Reader
	if path == "-" || path == "" {
		r = os.Stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, 16<<20))
	if err != nil {
		return nil, err
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("plan is not valid JSON")
	}
	return b, nil
}

func interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

var stdin = bufio.NewReader(os.Stdin)

func ask(q, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", q, def)
	} else {
		fmt.Printf("%s: ", q)
	}
	line, _ := stdin.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func askSecret(q string) string {
	fmt.Printf("%s (input hidden): ", q)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func askYes(q string, def bool) bool {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	ans := strings.ToLower(ask(q+" ("+d+")", ""))
	if ans == "" {
		return def
	}
	return ans == "y" || ans == "yes"
}

func fmtBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(u), 0
	for x := n / u; x >= u; x /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func fmtDur(s float64) string {
	m := int(s) / 60
	return fmt.Sprintf("%d:%04.1f", m, s-float64(m*60))
}

// newWorker builds the workers a terminal runs for the jobs a command waits
// for (tests give them stand-in handlers).
var newWorker = worker.New

// jobRunner is a worker this terminal runs (worker.Worker.Run).
type jobRunner interface {
	Run(ctx context.Context) error
}

// waitJobs waits for jobs to finish. When no capable worker is online it runs a
// worker in this process (and says so) so CLI-driven workflows never hang.
func waitJobs(c *cobra.Command, a *app.App, jobIDs []string, timeout time.Duration, quiet bool) ([]models.Job, error) {
	return waitJobsOpt(c, a, jobIDs, timeout, quiet, nil)
}

// waitJobsOpt is waitJobs for a command that runs its job in this terminal
// itself (render submit and qa run, with --local or when no machine running
// MetaRush can take it): here builds that worker. The wait starts it, and
// starts it again whenever it stops while the job still needs running here
// (see terminal.keepGoing). With here nil, the wait starts a worker in this
// terminal when no capable worker is online, watched the same way.
func waitJobsOpt(c *cobra.Command, a *app.App, jobIDs []string, timeout time.Duration, quiet bool, here func() jobRunner) ([]models.Job, error) {
	ctx, cancel := signalCtx(c.Context())
	defer cancel()
	if timeout > 0 {
		var tc context.CancelFunc
		ctx, tc = context.WithTimeout(ctx, timeout)
		defer tc()
	}
	pending := map[string]bool{}
	for _, id := range jobIDs {
		pending[id] = true
	}
	details := detailsMode(c)
	var term *terminal  // the worker this terminal runs for these jobs, once started
	var inlineErr error // this machine cannot run a job the wait needs (an unusable renderer)
	defer func() {
		if term != nil {
			// A job is marked done in the database just before the worker records it
			// (activity log, follow-up jobs): let it finish before this command exits.
			term.stop(5 * time.Second)
		}
	}()
	say := func(f string, x ...any) {
		if !quiet && !jsonMode(c) && details {
			fmt.Fprintf(os.Stderr, "  "+f+"\n", x...)
		}
	}
	if here != nil {
		term = startTerminal(ctx, here)
	}
	startInline := func(pending []models.Job) {
		if term != nil {
			return
		}
		reason, plainReason := "", ""
		for _, j := range pending {
			ws, _ := ops.OnlineWorkers(ctx, a, j.Type)
			capable := 0
			for _, w := range ws {
				if !j.Heavy || w.AcceptsHeavy {
					capable++
				}
			}
			if capable == 0 {
				reason = "no worker is online"
				plainReason = "No machine running MetaRush can take this right now"
				if len(ws) > 0 {
					reason = "the online worker defers heavy jobs (" + j.Type + ") while on battery power"
					plainReason = "The machine running MetaRush is holding big jobs while it is on battery"
				}
				break
			}
		}
		if reason == "" {
			return
		}
		for _, j := range pending {
			if j.Type == jobs.TypeRender || j.Type == jobs.TypeRemotionImport || j.Type == jobs.TypeRemotionRender || j.Type == jobs.TypeRemotionTask || j.Type == jobs.TypeRemotionRefresh {
				// The first render asked for in a terminal sets this version's
				// renderer up here when no daemon did it yet.
				if err := setUpRendererIfNew(c, a, quiet); err != nil {
					inlineErr = err
					return
				}
				break
			}
		}
		switch {
		case quiet || jsonMode(c):
		case details:
			fmt.Fprintf(os.Stderr, "%s — processing in this terminal (a persistent worker: `metarush worker start` or `metarush dashboard`)\n", reason)
		default:
			fmt.Fprintf(os.Stderr, "%s, so this terminal is doing it.\n", plainReason)
		}
		term = startTerminal(ctx, func() jobRunner {
			w := newWorker(a)
			w.IgnoreBattery = true // the person is explicitly waiting
			// Only the jobs this command waits for: anything else it started would
			// be cut off when the wait ends (a render's automatic quality check
			// stays queued for a worker, or for `metarush qa run --wait`).
			w.OnlyJobIDs = append([]string(nil), jobIDs...)
			// The worker's own log lines (job IDs, timings) are technical detail.
			w.Log = say
			return w
		})
	}
	var out []models.Job
	last := map[string]string{}
	names := newJobWords(a)
	for {
		out = out[:0]
		var pendingJobs []models.Job
		done := true
		for id := range pending {
			j, err := a.Queue.Get(ctx, id)
			if err != nil {
				return nil, err
			}
			out = append(out, *j)
			switch j.State {
			case models.JobSucceeded, models.JobFailed, models.JobCanceled:
			default:
				done = false
				pendingJobs = append(pendingJobs, *j)
			}
			if quiet || jsonMode(c) {
				continue
			}
			// A line whenever what there is to say changes: in plain words what
			// the job is doing to which clip; with --details its ID, type, state,
			// progress and the worker's own message.
			msg := names.progress(ctx, j)
			if details {
				msg = fmt.Sprintf("%s %.0f%% %s", j.State, j.Progress.Pct*100, j.Progress.Message)
			}
			if msg != "" && last[id] != msg {
				if details {
					fmt.Fprintf(os.Stderr, "  %s %-13s %s\n", j.ID, j.Type, msg)
				} else {
					fmt.Fprintf(os.Stderr, "  %s\n", msg)
				}
				last[id] = msg
			}
		}
		if done {
			return out, nil
		}
		if term == nil {
			startInline(pendingJobs)
			if inlineErr != nil {
				return out, inlineErr
			}
		} else if err := term.keepGoing(ctx, pendingJobs, say); err != nil {
			return out, err
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(1200 * time.Millisecond):
		}
	}
}

// terminal is the worker a waiting command runs in this terminal for the jobs
// it waits for. Nothing else may run them (often no machine running MetaRush is
// online at all), so a worker that stops while one of them still needs running
// is started again rather than left to wait forever.
type terminal struct {
	build  func() jobRunner
	cancel context.CancelFunc
	done   chan struct{} // closed when Run returned
	err    error         // what Run returned (read once done is closed)
	ran    time.Duration // how long it ran (read once done is closed)
	starts int
	quick  int // starts in a row that stopped within quickStop
}

// A worker that stops within quickStop of starting, quickStops times in a row,
// while its job still waits, is not started again: the wait ends saying so.
const (
	quickStop  = 5 * time.Second
	quickStops = 4
)

func startTerminal(ctx context.Context, build func() jobRunner) *terminal {
	t := &terminal{build: build}
	t.start(ctx)
	return t
}

func (t *terminal) start(ctx context.Context) {
	r := t.build()
	wctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	t.cancel, t.done = cancel, done
	t.starts++
	began := time.Now()
	go func() {
		defer close(done)
		err := r.Run(wctx)
		t.err, t.ran = err, time.Since(began)
	}()
}

// stopped reports whether the worker's Run returned.
func (t *terminal) stopped() bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

// stop ends the worker, waiting up to grace for it to finish recording the job
// it just completed.
func (t *terminal) stop(grace time.Duration) {
	t.cancel()
	select {
	case <-t.done:
	case <-time.After(grace):
	}
}

// keepGoing starts the worker again when it stopped while a job the command
// waits for still needs running here: queued and due (its retry's wait is
// over), or left running by a worker that stopped answering (lease expired).
// A job another machine is running is left to it. A worker that cannot run
// jobs here at all, or keeps stopping at once, ends the wait with a plain error
// instead of a wait that never ends.
func (t *terminal) keepGoing(ctx context.Context, pending []models.Job, say func(string, ...any)) error {
	if !t.stopped() {
		return nil
	}
	now := time.Now()
	var stuck *models.Job
	for i := range pending {
		if runnableHere(&pending[i], now) {
			stuck = &pending[i]
			break
		}
	}
	if stuck == nil {
		return nil
	}
	var ae *apperr.Error
	if errors.As(t.err, &ae) {
		return ae // this machine can't run it (its programs are missing): already said plainly
	}
	if t.ran < quickStop {
		t.quick++
	} else {
		t.quick = 0
	}
	if t.quick >= quickStops {
		// The job is still queued: asking again carries on with it.
		return apperr.Wrap(apperr.NoWorker, t.err, "MetaRush stopped working on this in the terminal and couldn't carry on, so it is still waiting to be done.").
			WithHint("Ask for it again here, or open MetaRush on a computer that can do it.").
			WithDetails(map[string]any{"jobId": stuck.ID, "type": stuck.Type, "state": stuck.State, "starts": t.starts})
	}
	say("the worker in this terminal stopped (%v) while job %s is %s; starting it again", t.err, stuck.ID, stuck.State)
	t.start(ctx)
	return nil
}

// runnableHere reports whether nothing runs a job now and it may run: queued
// and due, or running under a lease that expired.
func runnableHere(j *models.Job, now time.Time) bool {
	switch j.State {
	case models.JobQueued:
		return !j.CancelRequested && !now.Before(j.NextRunAt)
	case models.JobRunning:
		return j.Lease == nil || j.Lease.ExpiresAt.Before(now)
	}
	return false
}

// jobWords names jobs for people while a command waits for them or lists
// them: the clip a job works on by its title, a render as "the review copy
// of version 3". It reads the names straight from the database (no op call, so
// nothing extra lands in the activity log) and remembers them.
type jobWords struct {
	a       *app.App
	titles  map[string]string         // asset ID → its title ("" when it can't be read)
	renders map[string]*models.Render // render ID → the render (nil when it can't be read)
}

func newJobWords(a *app.App) *jobWords {
	return &jobWords{a: a, titles: map[string]string{}, renders: map[string]*models.Render{}}
}

func (w *jobWords) title(ctx context.Context, assetID string) string {
	if assetID == "" {
		return ""
	}
	t, ok := w.titles[assetID]
	if !ok && w.a != nil {
		var as models.Asset
		if w.a.DB.GetByID(ctx, store.CAssets, w.a.WorkspaceID(), assetID, &as) == nil {
			t = as.DisplayTitle()
		}
		w.titles[assetID] = t
	}
	return t
}

func (w *jobWords) render(ctx context.Context, j *models.Job) *models.Render {
	id := j.RenderID
	if id == "" {
		id = payloadString(j, "renderId")
	}
	if id == "" {
		return nil
	}
	r, ok := w.renders[id]
	if !ok && w.a != nil {
		var rd models.Render
		if w.a.DB.GetByID(ctx, store.CRenders, w.a.WorkspaceID(), id, &rd) == nil {
			r = &rd
		}
		w.renders[id] = r
	}
	return r
}

// renderWords names a render in a sentence: "the review copy of version 3".
func renderWords(quality string, revisionNumber int) string {
	return "the " + lowerFirst(models.RenderLabel(quality, revisionNumber))
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func payloadString(j *models.Job, key string) string {
	s, _ := j.Payload[key].(string)
	return s
}

// doing is what a running job does, in plain words ("Transcribing “Kitchen
// chat”", "Making the review copy of version 3").
func (w *jobWords) doing(ctx context.Context, j *models.Job) string {
	switch j.Type {
	case jobs.TypeRender:
		if r := w.render(ctx, j); r != nil {
			return "Making " + renderWords(r.Quality, r.RevisionNumber)
		}
		return "Rendering"
	case jobs.TypeQA:
		if r := w.render(ctx, j); r != nil {
			return "Checking " + renderWords(r.Quality, r.RevisionNumber)
		}
		return "Checking a render"
	case jobs.TypeImport:
		if t := payloadString(j, "title"); t != "" {
			return "Importing “" + t + "” from " + models.PlatformName(payloadString(j, "platform"))
		}
		return "Importing a video from " + models.PlatformName(payloadString(j, "platform"))
	case jobs.TypeFetch:
		what := "Getting"
		switch payloadString(j, "kind") {
		case "repo":
			what = "Getting the repository"
		case "page", "website":
			what = "Taking a snapshot of"
		}
		return strings.TrimSpace(what + " " + payloadString(j, "url"))
	case jobs.TypeResourceUpload:
		return "Uploading “" + orWord(payloadString(j, "title"), "a resource") + "”"
	case jobs.TypeVoiceover:
		return orWord(payloadString(j, "title"), "Narration")
	case jobs.TypeRemotionImport:
		from := payloadString(j, "from")
		if payloadString(j, "kind") == models.RemotionFromGitHub {
			return "Bringing in the Remotion project " + strings.TrimPrefix(from, "https://")
		}
		return "Bringing in the Remotion project in the folder " + path.Base(from)
	case jobs.TypeRemotionRender:
		return "Making a clip of “" + orWord(payloadString(j, "composition"), "a composition") + "”"
	case jobs.TypePrepare:
		// "Steadying the camera in “Kitchen chat”" (the fixes say what it does).
		if d := payloadString(j, "doing"); d != "" {
			t := w.title(ctx, j.AssetID)
			if t == "" {
				t = payloadString(j, "title")
			}
			if t != "" {
				return d + " in “" + t + "”"
			}
			return d
		}
	case jobs.TypeRemotionTask:
		return orWord(payloadString(j, "title"), "Working on a Remotion project")
	case jobs.TypeRemotionRefresh:
		return orWord(payloadString(j, "title"), "Refreshing a Remotion project from its own steps")
	case jobs.TypeRemotionPush:
		return orWord(payloadString(j, "title"), "Sending changes back to a Remotion project's repository")
	case jobs.TypeAbilityRun:
		return orWord(payloadString(j, "title"), "Running an ability")
	}
	if t := w.title(ctx, j.AssetID); t != "" {
		return ops.JobVerb(j.Type) + " “" + t + "”"
	}
	return ops.JobKindLabel(j.Type)
}

// subject is what a job worked on, to start a line about how it went.
func (w *jobWords) subject(ctx context.Context, j *models.Job) string {
	switch j.Type {
	case jobs.TypeRender, jobs.TypeQA:
		if r := w.render(ctx, j); r != nil {
			if j.Type == jobs.TypeQA {
				return "The quality check of " + renderWords(r.Quality, r.RevisionNumber)
			}
			return capitalize(renderWords(r.Quality, r.RevisionNumber))
		}
	case jobs.TypeImport, jobs.TypeFetch, jobs.TypeResourceUpload, jobs.TypeVoiceover, jobs.TypeRemotionImport, jobs.TypeRemotionRender, jobs.TypeRemotionTask,
		jobs.TypeRemotionRefresh, jobs.TypeRemotionPush, jobs.TypeAbilityRun:
		return w.doing(ctx, j)
	}
	if t := w.title(ctx, j.AssetID); t != "" {
		return "“" + t + "”"
	}
	return ops.JobKindLabel(j.Type)
}

// jobReadyWords say what a finished job made, after the clip's title.
var jobReadyWords = map[string]string{
	jobs.TypeProxy: "preview copy ready", jobs.TypeThumbnail: "thumbnail ready", jobs.TypeContactSheet: "frame grid ready",
	jobs.TypeWaveform: "waveform ready", jobs.TypeMezzanine: "steady copy ready", jobs.TypeEvidence: "scenes and pauses found",
	jobs.TypeTranscribe: "transcript ready", jobs.TypeDiarize: "speakers found", jobs.TypeVisual: "looked at what's on screen",
	jobs.TypeDescribe: "scenes described", jobs.TypeInspect: "measured for its fixes", jobs.TypePrepare: "fixed copy ready",
	jobs.TypeSwitches: "picture changes found",
}

// progress is the line a person reads about a job while a command waits for
// it ("" while it only waits to start: it is said once it starts).
func (w *jobWords) progress(ctx context.Context, j *models.Job) string {
	switch j.State {
	case models.JobQueued:
		if j.AttemptCount > 0 {
			return "Trying again: " + lowerFirst(w.doing(ctx, j)) + "…"
		}
		return ""
	case models.JobRunning:
		s := w.doing(ctx, j) + "…"
		// In steps of ten: a new line only when it has really moved on.
		if p := int(j.Progress.Pct * 10); p > 0 && p < 10 {
			s += fmt.Sprintf(" %d%%", p*10)
		}
		return s
	}
	return w.outcome(ctx, j)
}

// outcome says how a finished job went, in one line.
func (w *jobWords) outcome(ctx context.Context, j *models.Job) string {
	subject := w.subject(ctx, j)
	clip := strings.HasPrefix(subject, "“")
	switch j.State {
	case models.JobSucceeded:
		switch {
		case j.Type == jobs.TypeRender:
			return subject + " is ready."
		case j.Type == jobs.TypeQA:
			return subject + " is done."
		case clip && jobReadyWords[j.Type] != "":
			return subject + ": " + jobReadyWords[j.Type]
		}
		return subject + ": done"
	case models.JobCanceled:
		return subject + ": stopped"
	case models.JobFailed:
		if clip {
			return subject + ": the " + strings.ToLower(ops.JobKindLabel(j.Type)) + " didn't finish"
		}
		return subject + " didn't finish."
	}
	return ""
}

// jobStateWords say a job's state for people.
var jobStateWords = map[string]string{models.JobQueued: "waiting to start", models.JobRunning: "running", models.JobSucceeded: "finished",
	models.JobFailed: "didn't finish", models.JobCanceled: "stopped"}
