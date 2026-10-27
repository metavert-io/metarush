package logbook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/version"
)

// Collection holds the log entries.
const Collection = store.CLogEntries

// Tuning.
const (
	flushEvery     = time.Second
	flushBatch     = 200
	maxQueue       = 20000
	aggregateEvery = 5 * time.Minute
	writeTimeout   = 8 * time.Second
	closeBudget    = 3 * time.Second
)

// Options open a logger.
type Options struct {
	Cfg   *config.Config
	DB    *store.DB   // nil: everything goes to the spool until a later process flushes it
	Media media.Store // nil: long texts are spooled
	Tools *tools.Set
	// Actor is the process's own identity (its channel is used for events that
	// happen outside an operation).
	Actor models.Actor
}

// Logger buffers entries and writes them in the background. A nil *Logger is
// valid and records nothing (tests, tools that run without an installation).
type Logger struct {
	cfg     *config.Config
	db      *store.DB
	ms      media.Store
	tools   *tools.Set
	actor   models.Actor
	ws      string
	inst    string
	machine string
	red     *Redactor

	procChannel  string // claude-terminal / skill-eval when the environment says so
	conversation string // the Claude session the command-line process runs in
	command      string // this process's command line (command-line channels)

	mu       sync.Mutex
	queue    []*pending
	dropped  int
	agg      map[aggKey]*CountInfo
	jobPct   map[string]int
	daemon   bool
	settings config.Log
	local    localBodies // whether log.prompts: local can keep texts in this home

	wake    chan struct{}
	stop    chan struct{}
	done    chan struct{}
	closing bool // Close has started (it still writes what is queued)
	closed  bool // Close has finished: later entries are dropped

	// Test hooks.
	now     func() time.Time
	insertF func(ctx context.Context, docs []any) error
}

type aggKey struct{ op, channel string }

// pending is an entry waiting to be prepared (redacted, summarized) and written.
type pending struct {
	e        Entry
	raw      json.RawMessage // operation input, shortened in the background
	out      any             // operation output, summarized in the background
	bodies   []spoolBody     // long texts
	versions string          // a program whose version should be recorded ("" none, "-" just MetaRush)
}

// spoolBody is a long text waiting to be stored (redacted before it is spooled).
type spoolBody struct {
	Slot string `json:"slot"` // prompt | response | stderr
	Text string `json:"text"`
}

// spoolRecord is one line of a spool file.
type spoolRecord struct {
	Entry  Entry       `json:"entry"`
	Bodies []spoolBody `json:"bodies,omitempty"`
}

// Open builds a logger and starts its background writer.
func Open(o Options) *Logger {
	l := &Logger{cfg: o.Cfg, db: o.DB, ms: o.Media, tools: o.Tools, actor: o.Actor, red: NewRedactor(),
		agg: map[aggKey]*CountInfo{}, jobPct: map[string]int{}, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
		now: func() time.Time { return time.Now().UTC() }}
	if o.Cfg != nil {
		l.ws, l.inst, l.machine = o.Cfg.Workspace.ID, o.Cfg.Installation.ID, o.Cfg.Installation.Name
		l.settings = o.Cfg.Log
	}
	l.red.FromConfig(o.Cfg)
	l.red.FromEnvironment()
	l.track() // provider keys and other secrets this process already knows
	switch {
	case os.Getenv("METARUSH_LOG_CHANNEL") == ChannelSkillEval:
		l.procChannel = ChannelSkillEval
	case os.Getenv("CLAUDECODE") == "1":
		l.procChannel = ChannelClaudeTerminal
	}
	assistantRun := os.Getenv("METARUSH_VIA") == models.ViaAssistant
	l.conversation = os.Getenv("METARUSH_LOG_CONVERSATION")
	if dir := os.Getenv("METARUSH_ASSISTANT_RUN_DIR"); l.conversation == "" && dir != "" {
		// The dashboard's Claude Code runs this command inside a conversation.
		if base := filepath.Base(dir); ids.Prefix(base) != "" {
			l.conversation = base
		}
	}
	if l.conversation == "" && l.procChannel == ChannelClaudeTerminal && !assistantRun {
		l.conversation = os.Getenv("CLAUDE_CODE_SESSION_ID")
	}
	l.command = commandLine(os.Args)
	if o.Cfg != nil && l.procChannel == ChannelClaudeTerminal && !assistantRun && l.conversation != "" {
		markSessionRelevant(o.Cfg.ClaudeHooksDir(), l.conversation) // the hook records this session
	}
	go l.loop()
	return l
}

// commandLine is the process's arguments without the binary and --home.
func commandLine(args []string) string {
	if len(args) < 2 {
		return ""
	}
	var out []string
	skip := false
	for _, a := range args[1:] {
		switch {
		case skip:
			skip = false
			continue
		case a == "--home":
			skip = true
			continue
		case strings.HasPrefix(a, "--home="):
			continue
		}
		out = append(out, a)
	}
	return "metarush " + strings.Join(out, " ")
}

// Redactor is the logger's redactor (for callers that write log data elsewhere,
// such as the bug-report bundle).
func (l *Logger) Redactor() *Redactor {
	if l == nil {
		return NewRedactor()
	}
	return l.red
}

// Secret registers secret values seen at run time (dashboard session cookies).
func (l *Logger) Secret(values ...string) {
	if l != nil {
		l.red.Add(values...)
	}
}

// Privacy is where Claude prompt and response text is kept (log.prompts).
func (l *Logger) Privacy() string {
	return l.Settings().PromptsMode()
}

// Settings are the log settings in effect (config.yaml's log section).
func (l *Logger) Settings() config.Log {
	if l == nil {
		return config.Log{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.settings
}

// SetSettings changes the log settings while running (log.configure, or the
// daemon noticing that config.yaml changed).
func (l *Logger) SetSettings(s config.Log) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.settings = s
	l.mu.Unlock()
}

// InstallationID is this machine's installation.
func (l *Logger) InstallationID() string {
	if l == nil {
		return ""
	}
	return l.inst
}

// channelFor maps an actor's via to a log channel, applying the process's
// environment (Claude Code in a terminal, skill tests) to command-line calls.
func (l *Logger) channelFor(via string) string {
	switch via {
	case models.ViaAssistant:
		return ChannelAssistant
	case "dashboard", models.ViaAssistantAuto: // a safe step that started by itself runs for the person, from the dashboard (D141)
		return ChannelDashboard
	case "worker":
		return ChannelWorker
	case "watch":
		return ChannelWatch
	case "mcp":
		return ChannelMCP
	case models.ViaRecipe:
		return ChannelRecipe
	case "", "cli":
		if l.procChannel != "" {
			return l.procChannel
		}
		return ChannelCLI
	}
	return via
}

func cliChannel(ch string) bool {
	return ch == ChannelCLI || ch == ChannelClaudeTerminal || ch == ChannelSkillEval || ch == ChannelAssistant
}

// add queues an entry. It never blocks on I/O.
func (l *Logger) add(p *pending) {
	if l == nil {
		return
	}
	e := &p.e
	if e.ID == "" {
		e.ID = ids.New(EntryPrefix)
	}
	if e.At.IsZero() {
		e.At = l.now()
	}
	e.WorkspaceID, e.InstallationID, e.Version = l.ws, l.inst, version.Version
	if e.Machine == "" {
		e.Machine = l.machine
	}
	if e.Level == "" {
		e.Level = LevelInfo
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	if len(l.queue) >= maxQueue {
		l.dropped++
		l.mu.Unlock()
		return
	}
	l.queue = append(l.queue, p)
	n := len(l.queue)
	l.mu.Unlock()
	if n >= flushBatch {
		select {
		case l.wake <- struct{}{}:
		default:
		}
	}
}

func (l *Logger) loop() {
	defer close(l.done)
	t := time.NewTicker(flushEvery)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-l.wake:
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
		l.flushQueue(ctx)
		cancel()
	}
}

// Flush writes everything queued so far (and the dashboard-read counts) now.
func (l *Logger) Flush(ctx context.Context) {
	if l == nil {
		return
	}
	l.flushAggregates()
	l.flushQueue(ctx)
}

// Close stops the background writer and writes what is left, within a few
// seconds; whatever cannot reach the database goes to the spool.
func (l *Logger) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.closing {
		l.mu.Unlock()
		return
	}
	l.closing = true
	l.mu.Unlock()
	close(l.stop)
	<-l.done
	defer func() {
		l.mu.Lock()
		l.closed = true
		l.mu.Unlock()
		l.untrack()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), closeBudget)
	defer cancel()
	l.flushAggregates()
	if l.cfg != nil && !l.daemon {
		l.IngestHookTurns(ctx) // turns the Claude Code hook finished since the last process
	}
	l.flushQueue(ctx)
	if !l.daemon && l.db != nil && ctx.Err() == nil {
		sctx, scancel := context.WithTimeout(ctx, time.Second)
		l.FlushSpool(sctx, 2) // a little catching up; the daemon does the rest
		scancel()
	}
}

func (l *Logger) take() []*pending {
	l.mu.Lock()
	defer l.mu.Unlock()
	q := l.queue
	l.queue = nil
	if l.dropped > 0 {
		q = append(q, &pending{e: Entry{ID: ids.New(EntryPrefix), At: l.now(), Kind: KindEvent, Level: LevelWarn, Channel: l.channelFor(l.actor.Via),
			WorkspaceID: l.ws, InstallationID: l.inst, Machine: l.machine, Version: version.Version,
			Summary: fmt.Sprintf("%d log entries were left out because too many arrived at once", l.dropped),
			Event:   &EventInfo{Action: "log.dropped", Details: map[string]any{"dropped": l.dropped}}}})
		l.dropped = 0
	}
	return q
}

func (l *Logger) flushQueue(ctx context.Context) {
	batch := l.take()
	if len(batch) == 0 {
		return
	}
	recs := make([]*spoolRecord, 0, len(batch))
	for _, p := range batch {
		recs = append(recs, l.prepare(ctx, p))
	}
	if failed := l.write(ctx, recs); len(failed) > 0 {
		if err := l.spool(failed); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %d log entries could not be saved: %v\n", len(failed), err)
		}
	}
}

// prepare turns a pending entry into its final, redacted form.
func (l *Logger) prepare(ctx context.Context, p *pending) *spoolRecord {
	e := p.e
	if e.Op != nil && len(p.raw) > 0 {
		e.Op.Input = ShortenInput(l.red, p.raw, l.Privacy())
	}
	if e.Op != nil && p.out != nil {
		e.Op.Output = SummarizeOutput(p.out)
	}
	if e.Op != nil {
		// What an operation is about: the IDs it was given, and for changes the
		// objects it made (a read's results are not what it is about).
		out := e.Op.Output
		if !e.Op.Mutating {
			out = nil
		}
		e.Refs.merge(refsFrom(e.Op.Input, out))
	}
	if p.versions != "" {
		e.Versions = l.versions(ctx, p.versions)
	}
	l.redactEntry(&e)
	rec := &spoolRecord{Entry: e}
	for _, b := range p.bodies {
		if b.Text == "" {
			continue
		}
		t := b.Text
		if len(t) > maxBodyBytes {
			t = t[:maxBodyBytes] + "\n… (cut: longer than 1 MB)"
		}
		rec.Bodies = append(rec.Bodies, spoolBody{Slot: b.Slot, Text: l.red.String(t)})
	}
	return rec
}

// versions records MetaRush's and the programs' versions with an error.
func (l *Logger) versions(ctx context.Context, tool string) map[string]string {
	v := map[string]string{"metarush": version.Version, "os": runtime.GOOS + "/" + runtime.GOARCH}
	if l.tools != nil {
		for k, x := range l.tools.CachedVersions() {
			v[k] = x
		}
		if tool != "-" && tool != "" && v[tool] == "" {
			if x := l.tools.Version(ctx, tool); x != "" {
				v[tool] = x
			}
		}
	}
	return v
}

// redactEntry hides secrets in every free-text field of an entry. Identifiers,
// references and body keys are left alone.
func (l *Logger) redactEntry(e *Entry) {
	r := l.red
	e.Summary = r.String(e.Summary)
	if e.Error != nil {
		e.Error.Message = Clip(r.String(e.Error.Message), 2000)
		e.Error.Hint = r.String(e.Error.Hint)
		e.Error.Advice = r.String(e.Error.Advice)
	}
	if e.Op != nil {
		e.Op.Command = Clip(strings.Join(r.Args(strings.Fields(e.Op.Command)), " "), 600)
		for i := range e.Op.Actions {
			e.Op.Actions[i] = r.String(e.Op.Actions[i])
		}
		if e.Op.Output != nil && e.Op.Output.Facts != nil {
			e.Op.Output.Facts = r.Value(e.Op.Output.Facts).(map[string]any)
		}
	}
	if e.Job != nil {
		e.Job.Message = r.String(e.Job.Message)
		if e.Job.Detail != nil {
			e.Job.Detail = shorten(r, e.Job.Detail, 0, l.Privacy(), "").(map[string]any)
		}
	}
	if e.Tool != nil {
		args := e.Tool.Args
		if len(args) > maxArgs {
			args = append(append([]string{}, args[:maxArgs]...), fmt.Sprintf("… and %d more", len(args)-maxArgs))
		}
		args = r.Args(args)
		for i := range args {
			args[i] = Clip(args[i], maxArg)
		}
		e.Tool.Args = args
		e.Tool.StderrTail = r.String(e.Tool.StderrTail)
	}
	if c := e.Claude; c != nil {
		c.Title = r.String(c.Title)
		c.Prompt = r.String(c.Prompt)
		c.Response = r.String(c.Response)
		c.Model, c.Effort, c.State = r.String(c.Model), r.String(c.Effort), r.String(c.State)
		for i := range c.Tools {
			c.Tools[i].Summary = Clip(r.String(c.Tools[i].Summary), 300)
			c.Tools[i].Result = Clip(r.String(c.Tools[i].Result), 300)
		}
		for i := range c.Actions {
			redactAction(r, &c.Actions[i])
		}
	}
	if e.Action != nil {
		redactAction(r, e.Action)
	}
	if e.Event != nil {
		e.Event.Action = r.String(e.Event.Action)
		if e.Event.Details != nil {
			e.Event.Details = shorten(r, e.Event.Details, 0, l.Privacy(), "").(map[string]any)
		}
	}
	if p := e.Provider; p != nil {
		p.What, p.Endpoint, p.RequestID = r.String(p.What), Clip(r.String(p.Endpoint), 300), Clip(r.String(p.RequestID), 100)
	}
	for k, v := range e.Versions {
		e.Versions[k] = r.String(v)
	}
	e.UserName = r.String(e.UserName)
}

func redactAction(r *Redactor, a *ActionInfo) {
	a.Title = Clip(r.String(a.Title), 300)
	a.Kind = r.String(a.Kind)
	a.Result = Clip(r.String(a.Result), 500)
	a.Error = Clip(r.String(a.Error), 1000)
	a.By = r.String(a.By)
}

// write stores bodies and inserts entries. It returns the records that could
// not be written (the caller spools them).
func (l *Logger) write(ctx context.Context, recs []*spoolRecord) []*spoolRecord {
	if l.db == nil && l.insertF == nil {
		return recs
	}
	var failed, ready []*spoolRecord
	for _, rec := range recs {
		if err := l.storeBodies(ctx, rec); err != nil {
			failed = append(failed, rec)
			continue
		}
		ready = append(ready, rec)
	}
	if len(ready) == 0 {
		return failed
	}
	docs := make([]any, len(ready))
	for i, rec := range ready {
		docs[i] = rec.Entry
	}
	err := l.insert(ctx, docs)
	if err == nil {
		return failed
	}
	var bwe mongo.BulkWriteException
	if errors.As(err, &bwe) && bwe.WriteConcernError == nil {
		bad := map[int]bool{}
		for _, we := range bwe.WriteErrors {
			if we.Code != 11000 { // a duplicate means a replayed spool entry is already stored
				bad[we.Index] = true
			}
		}
		for i, rec := range ready {
			if bad[i] {
				failed = append(failed, rec)
			}
		}
		return failed
	}
	return append(failed, ready...)
}

func (l *Logger) insert(ctx context.Context, docs []any) error {
	if l.insertF != nil {
		return l.insertF(ctx, docs)
	}
	_, err := l.db.C(Collection).InsertMany(ctx, docs, optUnordered)
	return err
}

// --- Operations ---

// OpRecord is an operation call in progress. A nil *OpRecord is valid.
type OpRecord struct {
	l      *Logger
	id     string
	name   string
	start  time.Time
	raw    json.RawMessage
	actor  models.Actor
	human  bool
	remote bool
	parent Scope

	mu      sync.Mutex
	actions []string
	job     string // the job this call is running itself (it leased it)
}

// setJob notes the job this call runs itself: the programs it runs and the
// requests it makes from now on belong to that job.
func (r *OpRecord) setJob(id string) {
	r.mu.Lock()
	r.job = id
	r.mu.Unlock()
}

// endJob forgets job id once it has finished.
func (r *OpRecord) endJob(id string) {
	r.mu.Lock()
	if r.job == id {
		r.job = ""
	}
	r.mu.Unlock()
}

func (r *OpRecord) currentJob() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.job
}

// refsOf are the references of something done in scope sc: the scope's
// objects and the job being run (by a worker, or by the operation itself).
func refsOf(sc Scope) Refs {
	refs := sc.Refs
	if refs.JobID == "" {
		refs.JobID = sc.JobID
	}
	if refs.JobID == "" && sc.rec != nil {
		refs.JobID = sc.rec.currentJob()
	}
	return refs
}

// StartOp begins recording an operation call.
func (l *Logger) StartOp(ctx context.Context, name string, raw json.RawMessage, actor models.Actor, human, remote bool) *OpRecord {
	if l == nil {
		return nil
	}
	// A key being saved or tested is hidden from everything the call logs,
	// including the requests it makes before its own entry is written.
	l.red.Add(SecretInputs(raw)...)
	return &OpRecord{l: l, id: ids.New(EntryPrefix), name: name, start: time.Now(), raw: append(json.RawMessage(nil), raw...), actor: actor,
		human: human, remote: remote, parent: ScopeOf(ctx)}
}

// Context returns ctx carrying this call as the scope of what it causes.
func (r *OpRecord) Context(ctx context.Context) context.Context {
	if r == nil {
		return ctx
	}
	refs := r.parent.Refs
	if refs.ConversationID == "" {
		refs.ConversationID = r.l.conversation
	}
	return WithScope(ctx, Scope{EntryID: r.id, Op: r.name, JobID: r.parent.JobID, Channel: r.l.channelFor(r.actor.Via), Refs: refs, rec: r})
}

func (r *OpRecord) addAction(a string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.actions) < 20 {
		r.actions = append(r.actions, a)
	}
}

// End records the call's outcome. Successful dashboard reads are only counted.
func (r *OpRecord) End(mutating bool, out any, err error) {
	if r == nil {
		return
	}
	l := r.l
	dur := time.Since(r.start)
	ch := l.channelFor(r.actor.Via)
	if err == nil && !mutating && ch == ChannelDashboard {
		l.count(r.name, ch, dur)
		return
	}
	r.mu.Lock()
	actions := append([]string(nil), r.actions...)
	r.mu.Unlock()
	e := Entry{ID: r.id, Kind: KindOp, Channel: ch, UserID: r.actor.UserID, UserName: r.actor.UserName, Agent: r.actor.Agent, DurationMs: ms(dur),
		ParentID: r.parent.EntryID, Refs: r.parent.Refs,
		Op: &OpInfo{Name: r.name, Mutating: mutating, Human: r.human, Remote: r.remote, OK: err == nil, Actions: actions}}
	if e.Refs.ConversationID == "" {
		e.Refs.ConversationID = l.conversation
	}
	if cliChannel(ch) && r.parent.EntryID == "" {
		e.Op.Command = l.command
	}
	p := &pending{e: e, raw: r.raw, out: out}
	if err != nil {
		p.e.Level = LevelError
		p.e.Error = errorInfo(err)
		p.versions = "-"
		p.e.Summary = r.name + " failed: " + p.e.Error.Message
	} else {
		p.e.Summary = r.name
	}
	l.add(p)
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func newID() string { return ids.New(EntryPrefix) }

// errorInfo turns an error into the entry's error.
func errorInfo(err error) *EntryError {
	ae := apperr.From(err)
	ee := &EntryError{Code: ae.Code, Message: ae.Message, Hint: ae.Hint}
	if ae.Code == apperr.Internal {
		ee.Message = err.Error()
	}
	if d, ok := ae.Details.(map[string]any); ok {
		if r, ok := d["reason"].(string); ok {
			ee.Reason = r
		}
	}
	return ee
}

// count adds one successful dashboard read to the current period.
func (l *Logger) count(op, channel string, d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := aggKey{op, channel}
	c := l.agg[k]
	now := l.now()
	if c == nil {
		c = &CountInfo{Op: op, From: now}
		l.agg[k] = c
	}
	c.Count++
	m := ms(d)
	c.TotalMs += m
	if m > c.MaxMs {
		c.MaxMs = m
	}
	c.To = now
}

// flushAggregates turns the counted dashboard reads into one entry per
// operation and channel.
func (l *Logger) flushAggregates() {
	l.mu.Lock()
	agg := l.agg
	l.agg = map[aggKey]*CountInfo{}
	l.mu.Unlock()
	for k, c := range agg {
		if c.Count == 0 {
			continue
		}
		cc := *c
		l.add(&pending{e: Entry{Kind: KindOpSummary, Channel: k.channel, At: cc.To, UserID: l.actor.UserID, UserName: l.actor.UserName,
			DurationMs: cc.TotalMs, Summary: fmt.Sprintf("%s read %d times", k.op, cc.Count), Counts: &cc,
			Op: &OpInfo{Name: k.op, OK: true, Remote: true}}})
	}
}

// --- Events outside operations (audit) ---

// Audit records an audited change: inside an operation it is attached to the
// operation's entry; elsewhere (watched folder, uploads, workers) it becomes an
// entry of its own.
func (l *Logger) Audit(ctx context.Context, ev models.AuditEvent) {
	if l == nil {
		return
	}
	sc := ScopeOf(ctx)
	if sc.rec != nil {
		sc.rec.addAction(ev.Action)
		return
	}
	if sc.JobID != "" || coveredAudits[ev.Action] {
		return // the job's own events (or the Claude turn entry) already tell this
	}
	refs := sc.Refs
	refs.ProjectID = firstNonEmpty(ev.ProjectID, refs.ProjectID)
	refs.refFromID(ev.TargetID)
	ch := firstNonEmpty(sc.Channel, l.channelFor(ev.Actor.Via))
	l.add(&pending{e: Entry{Kind: KindEvent, Channel: ch, UserID: ev.Actor.UserID, UserName: ev.Actor.UserName, ParentID: sc.EntryID, Refs: refs,
		Summary: ev.Action, Event: &EventInfo{Action: ev.Action, TargetType: ev.TargetType, TargetID: ev.TargetID, Details: ev.Details}}})
}

// coveredAudits are audited changes another entry already records in full.
var coveredAudits = map[string]bool{"assistant.turn": true}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
