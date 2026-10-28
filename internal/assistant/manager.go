// Package assistant is the dashboard's Claude Code panel: real multi-turn
// conversations with the person's local Claude Code, run headless in a
// per-installation workspace with the MetaRush skills, a `metarush` shim in
// read-only mode and a strict tool allowlist. Each active conversation has one
// long-lived Claude Code process (stream-json in and out); follow-ups are
// written to its stdin, and a process that ended (idle timeout, daemon restart)
// is respawned with `--resume <session>` under the same locked-down flags.
//
// The assistant never changes shared state itself. It writes edit plans and
// render requests to per-turn proposals folders, and a change command it tries
// to run is refused and shown as an action card; people apply and run those from
// the dashboard (the human channel). Conversations are stored in MongoDB so they
// can be browsed from any machine; only the machine holding the Claude Code
// session can continue one. MetaRush makes no model calls itself.
package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/version"
)

// ID prefixes: conversations (asc_…) and the single requests of earlier
// versions (asr_…), which are imported as one-turn conversations.
const (
	ConvPrefix = "asc"
	RunPrefix  = "asr"
)

const (
	maxPromptRunes = 8000
	maxQueue       = 10
	maxEvents      = 3000
	maxEventBytes  = 3 << 20
	maxActions     = 100
	maxLogBytes    = 64 << 20
	defaultMaxLive = 3
	defaultList    = 50
)

// managers are per installation home (the daemon has one; a CLI process that
// holds a conversation inline has its own).
var managers = struct {
	sync.Mutex
	m map[string]*Manager
}{m: map[string]*Manager{}}

// Manager owns the conversations of one installation in this process.
type Manager struct {
	App *app.App
	// Exe is the metarush binary the shim executes (the running daemon's).
	Exe string
	// OnChange is called (outside locks) when a conversation is created or changes.
	OnChange func(id string)
	// OnTurnEnd is called (outside locks) once each message's turn has ended,
	// with the conversation as it is then (no transcript) and that turn.
	OnTurnEnd func(c Conversation, t Turn)

	mu      sync.Mutex
	convs   map[string]*conv // conversations of this installation loaded for live use
	started bool
	closed  bool
	reaping bool

	probeMu sync.Mutex
	probed  *probeResult
	// billingMode is the latest probe's billing ("plan", "api", "unknown"),
	// read without the probe's lock (limits are checked under a conversation's).
	billingMode atomic.Value
	planMu      sync.Mutex
	planUsage   *PlanUsage // the Claude plan's usage, as Claude Code last said (D181)
	probedAt    time.Time

	namesMu sync.Mutex
	names   map[string]string

	modelsMu sync.Mutex
	models   *AssistantModelsOut

	// watching: a goroutine follows the work cards started (work.go).
	watching bool

	// terminal: this manager serves a terminal command (or another short-lived
	// process), not the dashboard daemon. It never starts safe steps by itself,
	// follows work or tells Claude in the background, and leaves the work and
	// news a stopped MetaRush left behind to the daemon, so a command run while
	// the daemon is up never acts on the daemon's conversations; a message sent
	// from it brings the conversation's running work up to date first and takes
	// the news along (D141, D142).
	terminal bool

	// Test overrides (zero = config / defaults).
	timeout   time.Duration // one turn
	idle      time.Duration // an idle process is ended after this long
	grace     time.Duration // interrupt → SIGINT → kill steps
	maxLive   int
	workEvery time.Duration // how often running work is looked at
	tellAfter time.Duration // outcomes arriving within this make one note
}

// New builds the dashboard daemon's manager for an opened installation and
// makes it the one ops use.
func New(a *app.App, exe string) *Manager {
	m := newManager(a, exe, false)
	go m.inboxLoop() // notes for Claude from elsewhere (D186)
	return m
}

// NewTerminal builds the manager of a terminal command (see Manager.terminal).
func NewTerminal(a *app.App, exe string) *Manager { return newManager(a, exe, true) }

func newManager(a *app.App, exe string, terminal bool) *Manager {
	m := &Manager{App: a, Exe: exe, convs: map[string]*conv{}, names: map[string]string{}, terminal: terminal}
	managers.Lock()
	managers.m[a.Cfg.Home()] = m
	managers.Unlock()
	return m
}

// For returns this process's manager for the app's installation (creating a
// terminal one: the daemon makes its own with New).
func For(a *app.App) *Manager {
	managers.Lock()
	m := managers.m[a.Cfg.Home()]
	managers.Unlock()
	if m != nil {
		return m
	}
	exe, _ := os.Executable()
	return NewTerminal(a, exe)
}

// Close ends every Claude Code process this manager holds (the conversations
// stay resumable) and forgets the manager.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	cs := make([]*conv, 0, len(m.convs))
	for _, c := range m.convs {
		cs = append(cs, c)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, c := range cs {
		wg.Add(1)
		go func(c *conv) {
			defer wg.Done()
			c.mu.Lock()
			c.stopping = true
			c.mu.Unlock()
			m.endProcess(c)
			m.save(c, true)
		}(c)
	}
	wg.Wait()
	managers.Lock()
	if managers.m[m.App.Cfg.Home()] == m {
		delete(managers.m, m.App.Cfg.Home())
	}
	managers.Unlock()
}

func (m *Manager) dir() string          { return m.App.Cfg.AssistantDir() }
func (m *Manager) workspaceDir() string { return filepath.Join(m.dir(), "workspace") }

// binDir is the shim's folder: next to Claude Code's working folder, never in
// it, and first on its PATH (the only way Claude reaches the shim).
func (m *Manager) binDir() string             { return filepath.Join(m.dir(), "shim") }
func (m *Manager) convDir(id string) string   { return filepath.Join(m.dir(), "conversations", id) }
func (m *Manager) legacyDir(id string) string { return filepath.Join(m.dir(), "runs", id) }
func (m *Manager) machine() (id, name string) {
	return m.App.Cfg.Installation.ID, m.App.Cfg.Installation.Name
}
func (m *Manager) turnDir(id string, n int) string {
	return filepath.Join(m.convDir(id), "proposals", fmt.Sprintf("t%d", n))
}

func (m *Manager) turnTimeout() time.Duration {
	if m.timeout > 0 {
		return m.timeout
	}
	return time.Duration(m.App.Cfg.Assistant.Timeout()) * time.Minute
}

func (m *Manager) idleTimeout() time.Duration {
	if m.idle > 0 {
		return m.idle
	}
	return time.Duration(m.App.Cfg.Assistant.Idle()) * time.Minute
}

func (m *Manager) graceTime() time.Duration {
	if m.grace > 0 {
		return m.grace
	}
	return 4 * time.Second
}

func (m *Manager) liveLimit() int {
	if m.maxLive > 0 {
		return m.maxLive
	}
	return defaultMaxLive
}

func (m *Manager) changed(id string) {
	if m.OnChange != nil {
		m.OnChange(id)
	}
}

// ValidID reports whether id names a conversation (or an imported request).
func ValidID(id string) bool { return ids.Valid(ConvPrefix, id) || ids.Valid(RunPrefix, id) }

func notFound(id string) error {
	return apperr.New(apperr.NotFound, "MetaRush can't find that conversation.").WithDetails(map[string]any{"id": id})
}

func shortID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}

func newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

/* ---------- Live conversation state ---------- */

// conv is a conversation of this installation while this process uses it.
type conv struct {
	id      string
	mu      sync.Mutex
	data    Conversation
	notify  chan struct{}
	metaVer int
	bytes   int

	partial    AssistantPartial
	partialVer int

	proc     *proc
	turnProc *proc
	starting bool

	lastActive   time.Time
	turnTimer    *time.Timer
	interrupting bool
	stopping     bool
	timedOut     bool
	// liveUsed is what the running message's replies used so far, and
	// overLimit says it was stopped there at the conversation's limit (D181).
	liveUsed  TokenUsage
	overLimit bool
	respawn   bool
	// keptAtSpawn: the shared folders were closed to Claude when its process
	// started (D290).
	keptAtSpawn bool

	saveMu    sync.Mutex
	saveVer   int
	savedVer  int
	saveTimer *time.Timer

	tellTimer *time.Timer // tells Claude a moment after the last outcome (notes.go)

	// stored: its record is in the database (loaded from it, or saved once),
	// so saving never makes it again; gone: its project was cleared out for
	// good (D196) and it is no longer saved.
	stored bool
	gone   bool
}

func newConv(d Conversation) *conv {
	c := &conv{id: d.ID, data: d, notify: make(chan struct{}), lastActive: time.Now()}
	for _, e := range d.Events {
		c.bytes += eventBytes(e)
	}
	return c
}

// touch records a change (caller holds c.mu) and wakes every follower.
func (c *conv) touch() {
	c.metaVer++
	c.data.UpdatedAt = time.Now().UTC()
	c.wake()
}

func (c *conv) wake() {
	close(c.notify)
	c.notify = make(chan struct{})
}

func (c *conv) here(m *Manager) bool {
	id, _ := m.machine()
	return c.data.InstallationID == id
}

func (c *conv) projectID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.data.ProjectID
}

func (c *conv) lastContext() TurnContext {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.data.lastContextLocked()
}

// lastContextLocked is the latest message's context (caller holds c.mu).
func (d *Conversation) lastContextLocked() TurnContext {
	if n := len(d.Turns); n > 0 {
		return d.Turns[n-1].Context
	}
	return TurnContext{}
}

func (c *conv) runningTurn() int {
	if n := len(c.data.Turns); n > 0 && c.data.Turns[n-1].State == TurnRunning {
		return n
	}
	return 0
}

// copyLocked copies the conversation for use outside the lock (caller holds
// c.mu): its slices are copied, so readers never share them with writers.
func (c *conv) copyLocked(events bool) Conversation {
	d := c.data
	d.Turns = append([]Turn(nil), c.data.Turns...)
	d.Queue = append([]QueuedMessage(nil), c.data.Queue...)
	d.Actions = append([]AssistantAction(nil), c.data.Actions...)
	d.Events = nil
	if events {
		d.Events = append([]AssistantEvent(nil), c.data.Events...)
	}
	if c.data.Moved != nil {
		mv := *c.data.Moved
		d.Moved = &mv
	}
	if c.data.HandingOver != nil {
		h := *c.data.HandingOver
		d.HandingOver = &h
	}
	return d
}

func eventBytes(e AssistantEvent) int {
	n := 96 + len(e.Text)
	if e.Tool != nil {
		n += len(e.Tool.Output) + len(e.Tool.Input) + len(e.Tool.Summary) + len(e.Tool.Label)
		for _, t := range e.Tool.Todos {
			n += len(t.Content) + len(t.ActiveForm)
		}
	}
	if e.Result != nil {
		n += len(e.Result.Text)
	}
	return n
}

// addEvent appends a transcript event (caller holds c.mu).
func (c *conv) addEvent(e AssistantEvent) {
	seq := 1
	if n := len(c.data.Events); n > 0 {
		seq = c.data.Events[n-1].Seq + 1
	}
	e.Seq = seq
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	c.data.Events = append(c.data.Events, e)
	c.bytes += eventBytes(e)
	if len(c.data.Events) > maxEvents || c.bytes > maxEventBytes {
		c.compact()
	}
	c.metaVer++
	c.wake()
}

// compact keeps the stored transcript bounded: raw tool output of older turns
// goes first, then whole tool rows of the oldest turns. Prose, the person's
// messages and turn footers are kept. The full stream stays in stream.jsonl.
func (c *conv) compact() {
	cur := len(c.data.Turns)
	for i := range c.data.Events {
		e := &c.data.Events[i]
		if e.Tool != nil && e.Turn < cur && (e.Tool.Output != "" || e.Tool.Input != "") {
			t := *e.Tool
			t.Output, t.Input = "", ""
			e.Tool = &t
		}
	}
	c.recount()
	for turn := 1; turn < cur && (len(c.data.Events) > maxEvents*8/10 || c.bytes > maxEventBytes*8/10); turn++ {
		kept := c.data.Events[:0]
		for _, e := range c.data.Events {
			if e.Turn == turn && (e.Kind == "tool" || e.Kind == "tool_result") {
				continue
			}
			kept = append(kept, e)
		}
		c.data.Events = kept
		c.recount()
	}
	c.data.Compacted = true
}

func (c *conv) recount() {
	c.bytes = 0
	for _, e := range c.data.Events {
		c.bytes += eventBytes(e)
	}
}

/* ---------- Snapshots ---------- */

// present prepares a stored or live conversation for a caller (uid is the
// person asking): computed flags, who its cards are for (D179), and the raw
// parts (exact messages, system rules, tool I/O) only when asked.
func (m *Manager) present(d Conversation, events, raw, live bool, uid string) Conversation {
	me, _ := m.machine()
	d.Here = d.InstallationID == me
	d.Live = live
	d.Resumable = !d.Legacy && !d.SessionLost && d.HandingOver == nil
	d.LimitTokens = m.limitTokens(&d)
	if m.billing() == "plan" {
		d.BudgetUSD = 0 // a Claude plan has no dollar cap (D181): older conversations kept theirs
	}
	if !events {
		d.Events = nil
	}
	d.Turns = append([]Turn(nil), d.Turns...)
	d.Queue = append([]QueuedMessage(nil), d.Queue...)
	d.Actions = append([]AssistantAction(nil), d.Actions...)
	d.WaitingFor = append([]WaitingCount(nil), d.WaitingFor...)
	if len(d.Actions) > 0 { // live counts (lists carry the stored ones)
		d.PendingActions, d.Watching = pendingCount(d.Actions), runningWork(d.Actions)
	}
	d.personalize(uid)
	if events {
		d.Events = append([]AssistantEvent(nil), d.Events...)
	}
	if raw {
		d.SystemPrompt = systemRules
		return d
	}
	for i := range d.Turns {
		d.Turns[i].Sent = ""
	}
	for i := range d.Actions {
		d.Actions[i].Command = ""
		plainCard(&d.Actions[i]) // what older versions wrote on cards (D140)
	}
	for i := range d.Events {
		if t := d.Events[i].Tool; t != nil && (t.Input != "" || t.Output != "") {
			cp := *t
			cp.Input, cp.Output = "", ""
			d.Events[i].Tool = &cp
		}
	}
	d.Events = plainEvents(d.Events, d.Turns)
	return d
}

/* ---------- Start-up: legacy import and recovery ---------- */

// ensureStarted imports this machine's single requests from earlier versions
// and recovers conversations whose process died with the previous daemon.
func (m *Manager) ensureStarted(ctx context.Context) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()
	ctx = context.WithoutCancel(ctx)
	m.importLegacy(ctx)
	m.recover(ctx)
	if !m.terminal {
		go m.sweepGone(ctx) // conversations of projects cleared out on another machine (D196)
	}
}

func (m *Manager) recover(ctx context.Context) {
	me, _ := m.machine()
	or := bson.A{bson.M{"state": ConvWorking}, bson.M{"holderPid": bson.M{"$gt": 0}}}
	if !m.terminal { // work to follow and news to tell are the daemon's (D142)
		or = append(or, bson.M{"watching": bson.M{"$gt": 0}}, bson.M{"actions.state": ActionRunning}, bson.M{"untold": bson.M{"$gt": 0}})
	}
	var docs []Conversation
	if err := m.App.DB.Find(ctx, store.CAssistantConversations, m.App.WorkspaceID(), bson.M{"installationId": me, "$or": or}, &docs); err != nil {
		return
	}
	defer m.ensureWatching() // work cards started goes on being followed (D142)
	var tell []*conv         // outcomes Claude hadn't heard when MetaRush stopped
	defer func() {
		for _, c := range tell {
			m.tellClaudeSoon(c)
		}
	}()
	for _, d := range docs {
		if d.HolderPID != 0 && d.HolderPID != os.Getpid() && pidAlive(d.HolderPID) {
			continue // another MetaRush process on this machine holds it
		}
		for i := range d.Actions {
			// A safe step that was running by itself when MetaRush stopped waits
			// for a click now (nothing is run twice by itself).
			if a := &d.Actions[i]; a.State == ActionRunning && !m.terminal {
				a.State, a.Auto, a.AutoMode = ActionPending, false, false
			}
		}
		m.mu.Lock()
		c, ok := m.convs[d.ID]
		if !ok {
			c = newConv(d)
			c.stored = true
			m.convs[d.ID] = c
		}
		m.mu.Unlock()
		if ok {
			continue
		}
		c.mu.Lock()
		c.data.HolderPID = 0
		var f followUp
		if c.runningTurn() > 0 {
			f = m.finishTurnLocked(c, nil, nil, TurnLost, "MetaRush stopped while Claude Code was working on this. Send a message to pick up where it left off.")
		} else {
			c.data.State = ConvIdle
			c.touch()
		}
		untold := untoldCount(c.data.Actions) > 0 && !m.terminal
		c.mu.Unlock()
		m.afterTurn(c, f)
		if untold {
			tell = append(tell, c)
		}
	}
}

/* ---------- Loading ---------- */

func (m *Manager) loadDoc(ctx context.Context, id string) (*Conversation, error) {
	if !ValidID(id) {
		return nil, notFound(id)
	}
	var d Conversation
	if err := m.App.DB.GetByID(ctx, store.CAssistantConversations, m.App.WorkspaceID(), id, &d); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, notFound(id)
		}
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't load this conversation from its database.")
	}
	return &d, nil
}

// live returns a conversation of this machine for live use (loading it).
func (m *Manager) live(ctx context.Context, id string, _ bool) (*conv, error) {
	if !ValidID(id) {
		return nil, notFound(id)
	}
	m.ensureStarted(ctx)
	m.mu.Lock()
	if c := m.convs[id]; c != nil {
		m.mu.Unlock()
		return c, nil
	}
	m.mu.Unlock()
	d, err := m.loadDoc(ctx, id)
	if err != nil {
		return nil, err
	}
	if me, _ := m.machine(); d.InstallationID != me {
		return nil, otherMachine(d)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.convs[id]; c != nil {
		return c, nil
	}
	c := newConv(*d)
	c.stored = true
	m.convs[id] = c
	return c, nil
}

func otherMachine(d *Conversation) error {
	name := d.InstallationName
	if name == "" {
		name = "another machine"
	}
	return apperr.New(apperr.Conflict, "This conversation is running on %s.", name).
		WithHint("Continue it on " + name + ", or start a new conversation here.").
		WithDetails(map[string]any{"reason": "other_machine", "installationId": d.InstallationID, "installationName": d.InstallationName})
}

/* ---------- Reading ---------- */

// Get returns one conversation (from memory when this process holds it).
func (m *Manager) Get(ctx context.Context, id string, events, raw bool) (*Conversation, error) {
	if !ValidID(id) {
		return nil, notFound(id)
	}
	m.ensureStarted(ctx)
	uid, _ := m.person(ctx)
	m.mu.Lock()
	c := m.convs[id]
	m.mu.Unlock()
	if c != nil && !m.syncHolder(ctx, c) {
		c = nil // it lives on another computer now (D345)
	}
	if c != nil {
		c.mu.Lock()
		d := c.copyLocked(events)
		live := c.proc != nil && c.proc.alive()
		c.mu.Unlock()
		out := m.present(d, events, raw, live, uid)
		out.ProjectArchived = m.projectArchived(ctx, out.ProjectID)
		out.KeptHere = m.keptHere(ctx, out.ProjectID, out.Account)
		return &out, nil
	}
	d, err := m.loadDoc(ctx, id)
	if err != nil {
		return nil, err
	}
	out := m.present(*d, events, raw, d.HolderPID != 0 && pidAlive(d.HolderPID) && d.InstallationID == m.App.Cfg.Installation.ID, uid)
	out.ProjectArchived = m.projectArchived(ctx, out.ProjectID)
	out.KeptHere = m.keptHere(ctx, out.ProjectID, out.Account)
	return &out, nil
}

// List returns conversations, most recently active first, without transcripts.
// A project's list is everyone's and the workspace-wide one the asking
// person's, unless AllUsers or Mine says otherwise (D179); Others counts the
// other people's conversations that match either way.
func (m *Manager) List(ctx context.Context, in ConversationListIn) (*ConversationListOut, error) {
	m.ensureStarted(ctx)
	if in.AllUsers && in.Mine {
		return nil, apperr.New(apperr.InvalidInput, "Choose everyone's conversations or just yours, not both.")
	}
	filter := bson.M{}
	if in.Archived {
		filter["archived"] = true
	} else {
		filter["archived"] = bson.M{"$ne": true}
	}
	if in.ProjectID != "" {
		if !ids.Valid(ids.Project, in.ProjectID) {
			return nil, apperr.New(apperr.InvalidInput, "MetaRush can't find that project.").WithDetails(map[string]any{"projectId": in.ProjectID})
		}
		filter["projectId"] = in.ProjectID
	}
	if q := strings.TrimSpace(in.Search); q != "" {
		if utf8.RuneCountInString(q) > 200 {
			return nil, apperr.New(apperr.InvalidInput, "The search is too long (200 characters at most).")
		}
		re := bson.Regex{Pattern: regexp.QuoteMeta(q), Options: "i"}
		filter["$or"] = bson.A{bson.M{"title": re}, bson.M{"turns.prompt": re}, bson.M{"projectName": re}}
	}
	uid, _ := m.person(ctx)
	everyone := in.AllUsers || (in.ProjectID != "" && !in.Mine) || uid == ""
	others := 0
	if uid != "" {
		of := bson.M{"workspaceId": m.App.WorkspaceID(), "userId": bson.M{"$ne": uid}}
		for k, v := range filter {
			of[k] = v
		}
		if n, err := m.App.DB.C(store.CAssistantConversations).CountDocuments(ctx, of); err == nil {
			others = int(n)
		}
	}
	if !everyone {
		filter["userId"] = uid
	}
	limit := in.Limit
	if limit <= 0 || limit > 200 {
		limit = defaultList
	}
	var docs []Conversation
	opts := options.Find().SetSort(bson.D{{Key: "updatedAt", Value: -1}}).SetLimit(int64(limit)).
		SetProjection(bson.M{"events": 0, "turns": 0, "queue": 0, "actions": 0})
	if err := m.App.DB.Find(ctx, store.CAssistantConversations, m.App.WorkspaceID(), filter, &docs, opts); err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't list the conversations from its database.")
	}
	me, name := m.machine()
	out := &ConversationListOut{Conversations: []Conversation{}, Machine: name, InstallationID: me, AllUsers: everyone, Others: others}
	for _, d := range docs {
		live := false
		m.mu.Lock()
		c := m.convs[d.ID]
		m.mu.Unlock()
		if c != nil {
			c.mu.Lock()
			d.State, d.CostUSD, d.Title, d.TurnCount, d.UpdatedAt = c.data.State, c.data.CostUSD, c.data.Title, c.data.TurnCount, c.data.UpdatedAt
			d.ProposalCount, d.LastPrompt, d.Archived = c.data.ProposalCount, c.data.LastPrompt, c.data.Archived
			live = c.proc != nil && c.proc.alive()
			d.PendingActions, d.Watching, d.WaitingFor = pendingCount(c.data.Actions), runningWork(c.data.Actions), c.data.WaitingCounts()
			c.mu.Unlock()
		} else if d.InstallationID == me && d.HolderPID != 0 {
			live = pidAlive(d.HolderPID)
		}
		out.Conversations = append(out.Conversations, m.present(d, false, false, live, uid))
	}
	return out, nil
}

// Status reports availability (cached for a minute unless refresh) and what runs here.
func (m *Manager) Status(ctx context.Context, refresh bool) AssistantStatus {
	st := m.probe(ctx, refresh).status
	m.mu.Lock()
	for id, c := range m.convs {
		c.mu.Lock()
		if c.proc != nil && c.proc.alive() {
			st.Live++
		}
		if c.data.State == ConvWorking {
			st.Working = append(st.Working, id)
		}
		c.mu.Unlock()
	}
	m.mu.Unlock()
	sort.Strings(st.Working)
	if len(st.Working) > 0 {
		st.ActiveRunID = st.Working[0]
	}
	st.Waiting = m.waitingFor(ctx)
	st.PlanUsage = m.PlanUsage()
	return st
}

// waitingFor counts the cards and questions waiting for the person asking on
// this machine (the only place they can be decided): the ones for them in any
// conversation, someone else's too (D179). Conversations saved before D179
// count all theirs for the person who started them.
func (m *Manager) waitingFor(ctx context.Context) int {
	me, _ := m.machine()
	uid, _ := m.person(ctx)
	filter := bson.M{"installationId": me, "archived": bson.M{"$ne": true}, "pendingActions": bson.M{"$gt": 0}}
	if uid != "" {
		filter["$or"] = bson.A{bson.M{"waitingFor.userId": uid}, bson.M{"waitingFor": bson.M{"$exists": false}, "userId": uid}}
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	var docs []struct {
		ID             string         `bson:"_id"`
		UserID         string         `bson:"userId"`
		PendingActions int            `bson:"pendingActions"`
		WaitingFor     []WaitingCount `bson:"waitingFor"`
	}
	if err := m.App.DB.Find(cctx, store.CAssistantConversations, m.App.WorkspaceID(), filter, &docs,
		options.Find().SetProjection(bson.M{"pendingActions": 1, "waitingFor": 1, "userId": 1}).SetLimit(500)); err != nil {
		return 0
	}
	n := 0
	for _, d := range docs {
		switch {
		case uid == "":
			n += d.PendingActions
		case len(d.WaitingFor) == 0:
			if d.UserID == uid {
				n += d.PendingActions
			}
		default:
			n += countFor(d.WaitingFor, uid)
		}
	}
	return n
}

/* ---------- Sending messages ---------- */

// message is one message on its way to Claude Code.
type message struct {
	prompt string
	rc     TurnContext
	// user and userID are who sends it (D179): the person typing or
	// answering, or for a note the person whose decision it reports.
	user, userID string
	note         bool // a dashboard update (an action's outcome), not typed by the person
	// shown is what the person reads for it when that differs from the prompt
	// (a note's plain line; an answer, which Claude gets with its question).
	shown string
	// answerTo is the question card this message answers; question is its
	// question, told to Claude with the answer.
	answerTo, question string
	// news are card outcomes Claude hasn't heard yet, told with this message
	// (in its context, not as the person's words).
	news []string
	// files are the pictures and PDFs attached to it; attached says where
	// Claude reads them (set when the turn starts).
	files    []models.KeepFile
	attached []string
	// compact is MetaRush's own request that Claude summarize the
	// conversation (D181): sent as the raw /compact command.
	compact bool
	// soFar is what happened in the project before this conversation (D185):
	// told with a conversation's first message, and kept through summaries.
	soFar string
	// whatsNew says MetaRush was updated since the conversation's last
	// message, and what changed (D185); set when the turn starts.
	whatsNew string
	// moved says, once, where a conversation from another computer finds its
	// files now; summary is the conversation so far for one that goes on
	// from a summary in a new session (D345).
	moved, summary string
}

// shownText is what the person reads for a message.
func (msg message) shownText() string {
	if msg.shown != "" {
		return msg.shown
	}
	return msg.prompt
}

func validPrompt(p string) (string, error) {
	p = strings.TrimSpace(p)
	switch {
	case p == "":
		return "", apperr.New(apperr.InvalidInput, "Write what you want Claude to do.")
	case utf8.RuneCountInString(p) > maxPromptRunes:
		return "", apperr.New(apperr.InvalidInput, "The message is too long (%d characters at most).", maxPromptRunes)
	case strings.ContainsRune(p, 0) || !utf8.ValidString(p):
		return "", apperr.New(apperr.InvalidInput, "The message has characters MetaRush can't send. Type it again or paste it as plain text.")
	}
	return p, nil
}

var modelRe = regexp.MustCompile(`^(default|sonnet|opus|haiku|fable|opusplan|best|claude-[a-z0-9][a-z0-9.\-]{1,70})(\[1m\])?$`)

// ValidModel checks a --model value (an alias or a full model name).
func ValidModel(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "default" {
		return "", nil
	}
	if !modelRe.MatchString(s) {
		return "", apperr.New(apperr.InvalidInput, "“%s” isn't a Claude model.", s).WithHint("Choose a model in Settings → Claude Code, or use a name such as sonnet, opus or haiku.")
	}
	return s, nil
}

// ValidBudget checks a per-conversation budget in dollars.
func ValidBudget(v float64) error {
	if math.IsNaN(v) || v < 0.05 || v > 100 {
		return apperr.New(apperr.InvalidInput, "The spending limit must be between $0.05 and $100.")
	}
	return nil
}

// Ask starts a new conversation with its first message.
func (m *Manager) Ask(ctx context.Context, in AssistantAskIn) (*ConversationSendOut, error) {
	prompt, err := validPrompt(in.Prompt)
	if err != nil {
		return nil, err
	}
	if in.BudgetUSD != 0 {
		if err := ValidBudget(in.BudgetUSD); err != nil {
			return nil, err
		}
	}
	if in.TokenLimitM != 0 {
		if err := ValidTokenLimit(in.TokenLimitM); err != nil {
			return nil, err
		}
	}
	if _, err := ValidModel(in.Model); err != nil {
		return nil, err
	}
	title := strings.TrimSpace(in.Title)
	if utf8.RuneCountInString(title) > 120 {
		return nil, apperr.New(apperr.InvalidInput, "The title is too long (120 characters at most).")
	}
	pr := m.probe(ctx, false)
	if !pr.status.Available {
		return nil, apperr.New(apperr.NotConfigured, "%s", pr.status.Reason).WithHint(pr.status.Hint).WithDetails(map[string]any{"reason": "assistant_unavailable"})
	}
	budget := m.App.Cfg.Assistant.BudgetFor(pr.status.Billing == "plan")
	if in.BudgetUSD != 0 && pr.status.Billing != "plan" {
		budget = in.BudgetUSD
	}
	model, effort := orDefault(in.Model), orDefault(strings.ToLower(in.Effort))
	if _, _, err := m.resolveModel(ctx, firstNonEmpty(model, m.App.Cfg.Assistant.Model), firstNonEmpty(effort, m.App.Cfg.Assistant.Effort)); err != nil {
		return nil, err
	}
	rc, err := m.resolveContext(ctx, in.AssistantMessageIn)
	if err != nil {
		return nil, err
	}
	if m.projectArchived(ctx, rc.ProjectID) {
		return nil, archivedErr(rc.ProjectName)
	}
	files, err := m.checkAttachments(ctx, in.Attachments)
	if err != nil {
		return nil, err
	}
	m.ensureStarted(ctx)
	now := time.Now().UTC()
	me, machine := m.machine()
	uid, uname := m.person(ctx)
	d := Conversation{ID: ids.New(ConvPrefix), WorkspaceID: m.App.WorkspaceID(), Title: deriveTitle(prompt), TitleSource: "prompt",
		UserID: uid, UserName: uname, InstallationID: me, InstallationName: machine,
		ProjectID: rc.ProjectID, ProjectName: rc.ProjectName, State: ConvIdle, ClaudeSessionID: newSessionID(), BudgetUSD: budget, Model: model,
		Effort: effort, FirstPrompt: clip(prompt, 500), CreatedAt: now, UpdatedAt: now, TokenLimitM: in.TokenLimitM, Counting: true}
	if title != "" {
		d.Title, d.TitleSource = title, "user"
	}
	c := newConv(d)
	m.mu.Lock()
	m.convs[d.ID] = c
	m.mu.Unlock()
	m.save(c, true)
	m.changed(c.id)
	m.auditAs(ctx, "assistant.ask", "assistant_conversation", c.id, rc.ProjectID, map[string]any{"prompt": clip(prompt, 300), "budgetUsd": budget, "tokenLimitMillions": in.TokenLimitM, "model": model, "effort": effort})
	msg := message{prompt: prompt, rc: rc, user: uname, userID: uid, files: files}
	if rc.ProjectID != "" {
		// A conversation that starts in a project hears what happened there
		// before it: versions, renders, earlier conversations, open cards (D185).
		msg.soFar = m.soFarText(ctx, rc.ProjectID, c.id)
	}
	return m.send(ctx, c, msg)
}

// Continue sends a follow-up message (queued while a turn is running).
func (m *Manager) Continue(ctx context.Context, in AssistantContinueIn) (*ConversationSendOut, error) {
	prompt, err := validPrompt(in.Prompt)
	if err != nil {
		return nil, err
	}
	c, err := m.live(ctx, in.ID, true)
	if err != nil {
		return nil, err
	}
	if pid := c.projectID(); m.projectArchived(ctx, pid) {
		c.mu.Lock()
		name := c.data.ProjectName
		c.mu.Unlock()
		return nil, archivedErr(name)
	}
	// One project per conversation (D185): the message carries the
	// conversation's project, whatever page it was typed on.
	rc, err := m.scopeContext(ctx, c.projectID(), in.AssistantMessageIn)
	if err != nil {
		return nil, err
	}
	files, err := m.checkAttachments(ctx, in.Attachments)
	if err != nil {
		return nil, err
	}
	if in.Model != nil || in.Effort != nil {
		if _, err := m.Configure(ctx, ConversationConfigureIn{ID: in.ID, Model: in.Model, Effort: in.Effort}); err != nil {
			return nil, err
		}
	}
	// Notes from elsewhere waiting for this machine go first (D186).
	m.sendInbox(ctx, c)
	// Anyone who can open the conversation may continue it; the message is
	// theirs (D179: its turn, its audit and its log entry name them).
	// Outcomes Claude hasn't heard yet go along with it.
	uid, uname := m.person(ctx)
	news, told := m.takeNews(c)
	return m.sendWithNews(ctx, c, message{prompt: prompt, rc: rc, user: uname, userID: uid, news: news, files: files}, told)
}

type personKey struct{}

// AsPerson carries who is asking (the dashboard's signed-in person, or the
// CLI's configured user) into the manager, which is shared by every request.
func AsPerson(ctx context.Context, a *app.App) context.Context {
	return context.WithValue(ctx, personKey{}, a.Actor)
}

// person is who is asking: from AsPerson, else this installation's user.
func (m *Manager) person(ctx context.Context) (string, string) {
	if act, ok := ctx.Value(personKey{}).(models.Actor); ok && act.UserID != "" {
		return act.UserID, act.UserName
	}
	return m.App.Cfg.User.ID, m.App.Cfg.User.Name
}

// auditAs records an audit event as the person asking.
func (m *Manager) auditAs(ctx context.Context, action, targetType, targetID, projectID string, details map[string]any) {
	a := *m.App
	if act, ok := ctx.Value(personKey{}).(models.Actor); ok && act.UserID != "" {
		a.Actor.UserID, a.Actor.UserName = act.UserID, act.UserName
		if act.Via != "" {
			a.Actor.Via = act.Via
		}
	}
	a.Audit(ctx, action, targetType, targetID, projectID, details)
}

func budgetError(d *Conversation) error {
	return apperr.New(apperr.Conflict, "This conversation reached its spending limit ($%.2f of $%.2f).", d.CostUSD, d.BudgetUSD).
		WithHint("To keep going, type /budget and a higher amount in the message box (for example /budget 5), or start a new conversation.").
		WithDetails(map[string]any{"reason": "budget", "costUsd": d.CostUSD, "budgetUsd": d.BudgetUSD}).
		WithCommand("metarush assistant configure " + d.ID + " --budget <dollars>")
}

// send starts a turn now, or queues the message behind the running one.
func (m *Manager) send(ctx context.Context, c *conv, msg message) (*ConversationSendOut, error) {
	c.mu.Lock()
	switch {
	case c.data.Legacy:
		c.mu.Unlock()
		return nil, apperr.New(apperr.Conflict, "This is a single request from an older version of MetaRush, so it can't be continued.").
			WithHint("Start a new conversation.").WithDetails(map[string]any{"reason": "legacy"})
	case c.data.SessionLost:
		c.mu.Unlock()
		return nil, apperr.New(apperr.Conflict, "This conversation's Claude Code session is no longer on this machine.").
			WithHint("Start a new conversation.").WithDetails(map[string]any{"reason": "session_lost"})
	case !msg.compact && m.overLimitLocked(c, TokenUsage{}) != "":
		which := m.overLimitLocked(c, TokenUsage{})
		d := c.data
		c.mu.Unlock()
		return nil, m.limitError(&d, which)
	case c.data.State == ConvWorking || c.starting || len(c.data.Queue) > 0:
		if len(c.data.Queue) >= maxQueue {
			c.mu.Unlock()
			return nil, apperr.New(apperr.Conflict, "%d messages are already waiting for Claude.", maxQueue).
				WithHint("Wait until Claude gets to them, or remove one you no longer need.").WithDetails(map[string]any{"reason": "queue_full"})
		}
		if n := len(c.data.Queue); msg.note && n > 0 && c.data.Queue[n-1].Note {
			// News waiting behind a running turn: one note takes in the rest
			// (several outcomes close together make one message for Claude).
			last := &c.data.Queue[n-1]
			last.Prompt = joinLines(last.Prompt, msg.prompt)
			last.Shown = joinLines(last.Shown, msg.shown)
			if msg.userID != "" { // the latest decision is what Claude carries on from
				last.User, last.UserID = msg.user, msg.userID
			}
			c.touch()
			c.mu.Unlock()
			m.save(c, true)
			m.changed(c.id)
			snap := m.snapshot(ctx, c)
			return &ConversationSendOut{Conversation: snap, Queued: true, QueueID: last.ID}, nil
		}
		q := QueuedMessage{ID: shortID("q"), Prompt: msg.prompt, Shown: msg.shown, AnswerTo: msg.answerTo, Question: msg.question, News: msg.news,
			Context: msg.rc, User: msg.user, UserID: msg.userID, At: time.Now().UTC(), Note: msg.note, Attachments: msg.files}
		c.data.Queue = append(c.data.Queue, q)
		c.touch()
		c.mu.Unlock()
		m.save(c, true)
		m.changed(c.id)
		snap := m.snapshot(ctx, c)
		return &ConversationSendOut{Conversation: snap, Queued: true, QueueID: q.ID}, nil
	}
	c.starting = true
	c.mu.Unlock()
	n, err := m.startTurn(c, msg)
	if err != nil && n == 0 {
		return nil, err // refused before a turn was recorded (another computer has it now)
	}
	snap := m.snapshot(ctx, c)
	if err != nil {
		return &ConversationSendOut{Conversation: snap, Turn: n}, err
	}
	return &ConversationSendOut{Conversation: snap, Turn: n}, nil
}

// snapshot is the conversation (without its transcript) as the person asking sees it.
func (m *Manager) snapshot(ctx context.Context, c *conv) Conversation {
	uid, _ := m.person(ctx)
	c.mu.Lock()
	d := c.copyLocked(false)
	live := c.proc != nil && c.proc.alive()
	c.mu.Unlock()
	return m.present(d, false, false, live, uid)
}

// startTurn sends msg to Claude Code (spawning or resuming its process).
// c.starting is set by the caller.
func (m *Manager) startTurn(c *conv, msg message) (int, error) {
	// One history (D345): a message starts only while this computer still
	// holds the conversation and isn't handing it over.
	if err := m.mayContinue(c); err != nil {
		c.mu.Lock()
		c.starting = false
		c.mu.Unlock()
		return 0, err
	}
	if msg.compact {
		if pid := c.projectID(); pid != "" {
			// What the project went through survives the summary (D185).
			sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			msg.soFar = m.soFarText(sctx, pid, c.id)
			cancel()
		}
	}
	if len(msg.files) > 0 {
		// Copied where Claude can Read them before the turn starts (D166).
		actx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		msg.attached = m.attachedLines(actx, c.id, msg.files)
		cancel()
	}
	p, spawnErr := m.ensureProc(c)
	acct := m.probe(context.Background(), false).status.Account // whose Claude answers (D239)
	now := time.Now().UTC()
	c.mu.Lock()
	if acct != nil {
		a := *acct
		a.At = now
		c.data.Account = &a
	}
	n := len(c.data.Turns) + 1
	folder := m.turnDir(c.id, n)
	relFolder, _ := filepath.Rel(m.dir(), folder)
	turn := Turn{N: n, Prompt: msg.prompt, Context: msg.rc, User: msg.user, UserID: msg.userID, State: TurnRunning, StartedAt: now, Note: msg.note || msg.compact, Compact: msg.compact,
		Folder: filepath.ToSlash(relFolder), RefPrefix: fmt.Sprintf("t%d/", n), Model: firstNonEmpty(c.data.Model, m.App.Cfg.Assistant.Model),
		Effort: firstNonEmpty(c.data.Effort, m.App.Cfg.Assistant.Effort), AnswerTo: msg.answerTo, Attachments: msg.files}
	if msg.shown != "" && msg.shown != msg.prompt {
		turn.Shown = msg.shown
	}
	c.data.TurnCount = n
	if !msg.note && !msg.compact {
		c.data.LastPrompt = clip(msg.prompt, 500)
		if c.data.FirstPrompt == "" {
			c.data.FirstPrompt = clip(msg.prompt, 500)
		}
	}
	// A conversation keeps the project it started in (D185): its messages
	// never move it (older builds moved it to each message's page).
	if msg.rc.ProjectID != "" && msg.rc.ProjectID == c.data.ProjectID && msg.rc.ProjectName != "" {
		c.data.ProjectName = msg.rc.ProjectName // a renamed project
	}
	if !msg.compact {
		// What changed since this conversation's last message, once per
		// upgrade (D185); conversations from before start counting now.
		cur := version.Current().Version
		if prev := c.data.MetaRushVersion; prev != "" && prev != cur {
			if d, ok := version.Compare(cur, prev); ok && d > 0 {
				msg.whatsNew = whatsNewNote(version.Changelog(), prev, cur)
			}
		}
		c.data.MetaRushVersion = cur
	}
	if mv := c.data.Moved; mv != nil && !msg.compact {
		// A conversation from another computer (D345): Claude hears once
		// where its files are now, and one going on from a summary gets it
		// (marked below, once the message goes).
		if !mv.Told && mv.OldDir != "" && mv.NewDir != "" && mv.OldDir != mv.NewDir {
			msg.moved = fmt.Sprintf("This conversation moved to another computer: what was in %s is now in %s.", mv.OldDir, mv.NewDir)
		}
		if mv.Summary && !mv.Primed {
			msg.summary = summaryOf(c.data)
		}
	}
	rc := msg.rc
	if spawnErr == nil {
		if err := os.MkdirAll(folder, 0o700); err != nil {
			spawnErr = err
		}
	}
	failure := ""
	if spawnErr == nil && !p.alive() {
		// It exited before this turn was recorded (its exit handler saw no turn).
		_, failure = m.exitReason(c, p)
		if c.proc == p {
			c.proc = nil
		}
	}
	if spawnErr != nil || failure != "" {
		if failure == "" {
			failure = describeSpawnErr(spawnErr)
		} else {
			spawnErr = apperr.New(apperr.ToolFailed, "%s", failure)
		}
		turn.State, turn.Error, turn.EndedAt = TurnFailed, failure, &now
		c.data.Turns = append(c.data.Turns, turn)
		c.addEvent(AssistantEvent{Kind: "turn", Turn: n, Text: msg.shownText(), User: msg.user, UserID: msg.userID, Context: &rc, State: noteState(msg.note), ActionID: msg.answerTo,
			Attachments: msg.files})
		c.addEvent(AssistantEvent{Kind: "error", Turn: n, Text: turn.Error})
		c.addEvent(AssistantEvent{Kind: "turn_end", Turn: n, State: TurnFailed})
		var dropped int
		dropped, c.data.Queue = len(c.data.Queue), nil
		if dropped > 0 {
			c.addEvent(AssistantEvent{Kind: "notice", Turn: n, Text: fmt.Sprintf("%s not sent.", pluralize(dropped, "queued message"))})
		}
		c.starting = false
		c.data.State = ConvIdle
		if c.proc == nil || !c.proc.alive() {
			c.data.HolderPID = 0 // claimed for this message (mayContinue), no process holds it now
		}
		c.touch()
		c.mu.Unlock()
		m.save(c, true)
		m.changed(c.id)
		return n, apperr.From(spawnErr)
	}
	if mv := c.data.Moved; mv != nil && !msg.compact {
		mv.Told = true
		if msg.summary != "" {
			mv.Primed = true
			c.addEvent(AssistantEvent{Kind: "notice", Turn: n, Text: "Continuing from a summary of this conversation: Claude Code’s own record of it didn’t come along from " +
				orWords(mv.FromName, "the other computer") + "."})
		}
	}
	sent := composeMessage(rc, msg, n, realPath(folder))
	if msg.compact {
		sent = msg.prompt // Claude Code's own command, as typed
		if msg.soFar != "" {
			sent += " Also keep this record of the project so far (MetaRush's own, newest first): " + strings.Join(strings.Fields(msg.soFar), " ")
		}
	}
	turn.Sent = sent
	turn.Resumed = p.resumed && p.turns == 0
	p.turns++
	c.data.Turns = append(c.data.Turns, turn)
	// The person reads what they typed (or a note's plain line); that Claude
	// Code was resumed is technical (Turn.Resumed, the raw view).
	c.addEvent(AssistantEvent{Kind: "turn", Turn: n, Text: msg.shownText(), User: msg.user, UserID: msg.userID, Context: &rc, State: noteState(msg.note || msg.compact), ActionID: msg.answerTo,
		Attachments: msg.files})
	c.data.State = ConvWorking
	c.liveUsed, c.overLimit = TokenUsage{}, false
	c.starting = false
	c.turnProc = p
	c.lastActive = time.Now()
	c.interrupting, c.timedOut = false, false
	c.partial = AssistantPartial{Turn: n}
	c.partialVer++
	if c.turnTimer != nil {
		c.turnTimer.Stop()
	}
	c.turnTimer = time.AfterFunc(m.turnTimeout(), func() { m.onTimeout(c, n) })
	sid := c.data.ClaudeSessionID
	c.touch()
	c.mu.Unlock()
	m.save(c, true)
	m.changed(c.id)
	if err := p.write(userMessage(sent, sid)); err != nil {
		p.kill() // its exit ends the turn
	}
	return n, nil
}

// orDefault maps "default" (and blanks) to "": use the next level's setting.
func orDefault(s string) string {
	s = strings.TrimSpace(s)
	if s == "default" {
		return ""
	}
	return s
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func noteState(note bool) string {
	if note {
		return "note"
	}
	return ""
}

func realPath(p string) string {
	if rp, err := filepath.EvalSymlinks(p); err == nil {
		return rp
	}
	return p
}

func describeSpawnErr(err error) string {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Message
	}
	return "MetaRush couldn't start Claude Code. Send your message again; if it keeps happening, open Claude Code in a terminal to see why."
}

func userMessage(text, sessionID string) map[string]any {
	return map[string]any{"type": "user", "session_id": sessionID, "parent_tool_use_id": nil,
		"message": map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": text}}}}
}

/* ---------- Processes ---------- */

// proc is one Claude Code process of a conversation.
type proc struct {
	pid       int
	stdin     *os.File
	stdinMu   sync.Mutex
	done      chan struct{}
	err       error
	stderr    string
	cancel    context.CancelFunc
	parser    *parser
	resumed   bool
	turns     int
	costMode  int // 0 unknown, 1 cumulative session total, 2 this process only
	spawnCost float64
	ctl       int
	logf      *os.File
	logged    int
}

func (p *proc) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *proc) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p.stdinMu.Lock()
	defer p.stdinMu.Unlock()
	if p.stdin == nil {
		return errors.New("stdin closed")
	}
	_, err = p.stdin.Write(append(b, '\n'))
	return err
}

func (p *proc) closeStdin() {
	p.stdinMu.Lock()
	defer p.stdinMu.Unlock()
	if p.stdin != nil {
		_ = p.stdin.Close()
		p.stdin = nil
	}
}

func (p *proc) kill() {
	p.closeStdin()
	p.cancel() // the whole process group
}

// interrupt asks Claude Code to stop the current turn (the process stays).
func (p *proc) interrupt() error {
	p.stdinMu.Lock()
	p.ctl++
	id := fmt.Sprintf("mr_interrupt_%d", p.ctl)
	p.stdinMu.Unlock()
	return p.write(map[string]any{"type": "control_request", "request_id": id, "request": map[string]any{"subtype": "interrupt"}})
}

// ensureProc returns the conversation's live process, spawning (or resuming) it.
func (m *Manager) ensureProc(c *conv) (*proc, error) {
	// What may leave (D290): a process started before a client's rules
	// closed (or opened) the shared folders starts again under the new ones.
	kept := leave.AnyKeptFromClaude(context.Background(), m.App)
	c.mu.Lock()
	if c.proc != nil && kept != c.keptAtSpawn {
		c.respawn = true
	}
	if p := c.proc; p != nil && p.alive() && !c.respawn {
		c.mu.Unlock()
		return p, nil
	}
	old := c.proc
	c.respawn = false
	c.mu.Unlock()
	if old != nil && old.alive() {
		m.endProcess(c)
	}
	if err := m.makeRoom(c); err != nil {
		return nil, err
	}
	if err := m.claim(c); err != nil {
		return nil, err
	}
	p, err := m.spawn(c)
	if err != nil {
		m.release(c)
		return nil, err
	}
	c.mu.Lock()
	c.proc = p
	c.data.HolderPID = os.Getpid()
	c.mu.Unlock()
	m.startReaper()
	return p, nil
}

// claim records this process as the conversation's holder, refusing when
// another live MetaRush process on this machine holds it.
func (m *Manager) claim(c *conv) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var cur struct {
		HolderPID int `bson:"holderPid"`
	}
	err := m.App.DB.C(store.CAssistantConversations).FindOne(ctx, bson.M{"_id": c.id, "workspaceId": m.App.WorkspaceID()},
		options.FindOne().SetProjection(bson.M{"holderPid": 1})).Decode(&cur)
	if err == nil && cur.HolderPID != 0 && cur.HolderPID != os.Getpid() && pidAlive(cur.HolderPID) {
		return apperr.New(apperr.Conflict, "This conversation is already open in another copy of MetaRush on this machine.").
			WithHint("Continue it there (in the dashboard, or in the terminal that has it open), or close that one first.").
			WithDetails(map[string]any{"reason": "held_elsewhere", "pid": cur.HolderPID})
	}
	me, _ := m.machine()
	_, _ = m.App.DB.C(store.CAssistantConversations).UpdateOne(ctx, bson.M{"_id": c.id, "workspaceId": m.App.WorkspaceID(), "installationId": me},
		bson.M{"$set": bson.M{"holderPid": os.Getpid()}})
	return nil
}

func (m *Manager) release(c *conv) {
	c.mu.Lock()
	c.data.HolderPID = 0
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = m.App.DB.C(store.CAssistantConversations).UpdateOne(ctx, bson.M{"_id": c.id, "workspaceId": m.App.WorkspaceID(), "holderPid": os.Getpid()},
		bson.M{"$set": bson.M{"holderPid": 0}})
}

// makeRoom keeps at most liveLimit processes: the least recently used idle one
// is ended (its conversation resumes later).
func (m *Manager) makeRoom(c *conv) error {
	m.mu.Lock()
	var live []*conv
	for _, o := range m.convs {
		if o == c {
			continue
		}
		o.mu.Lock()
		if o.proc != nil && o.proc.alive() {
			live = append(live, o)
		}
		o.mu.Unlock()
	}
	m.mu.Unlock()
	if len(live) < m.liveLimit() {
		return nil
	}
	var lru *conv
	var lruAt time.Time
	for _, o := range live {
		o.mu.Lock()
		idle := o.data.State == ConvIdle && !o.starting && len(o.data.Queue) == 0
		at := o.lastActive
		o.mu.Unlock()
		if idle && (lru == nil || at.Before(lruAt)) {
			lru, lruAt = o, at
		}
	}
	if lru == nil {
		return apperr.New(apperr.Conflict, "%d conversations are already working on this machine.", len(live)).
			WithHint("Wait for one to finish, or stop one.").WithDetails(map[string]any{"reason": "busy"})
	}
	m.endProcess(lru)
	return nil
}

// spawn starts Claude Code for a conversation under the locked-down flags.
func (m *Manager) spawn(c *conv) (*proc, error) {
	pr := m.probe(context.Background(), false)
	if !pr.status.Available {
		return nil, apperr.New(apperr.NotConfigured, "%s", pr.status.Reason).WithHint(pr.status.Hint).WithDetails(map[string]any{"reason": "assistant_unavailable"})
	}
	if err := m.prepareDirs(c.id); err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't get Claude Code's working folder ready.")
	}
	scratch, proposals, cwd, cdir := realPath(m.App.Cfg.Scratch.Dir), realPath(filepath.Join(m.convDir(c.id), "proposals")), realPath(m.workspaceDir()), realPath(m.convDir(c.id))
	cache := realPath(m.App.Cfg.Cache.Dir)
	home := realPath(m.App.Cfg.Home())
	scratchRoot, cacheRoot := readRoot(scratch, home), readRoot(cache, home)
	// What may leave (D290): while any client's rules keep faces, pictures or
	// words from Claude, the shared frame and cache folders are closed to it
	// (their files are anyone's, found by name, listing or search); what it
	// may see is copied into its conversation's own folder (leave.aiOutput).
	keepAll := leave.AnyKeptFromClaude(context.Background(), m.App)
	if keepAll {
		scratchRoot, cacheRoot = "", ""
	}
	c.mu.Lock()
	c.keptAtSpawn = keepAll
	c.mu.Unlock()
	allowed, err := allowedTools(scratchRoot, cacheRoot, proposals)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't set up what Claude Code may do here.")
	}
	denied, err := deniedFor(realPath(m.binDir()))
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't set up what Claude Code may do here.")
	}
	if keepAll {
		denied = append(denied, keptReads(scratch, cache)...)
	}
	m.summaryIfSessionMissing(c)
	c.mu.Lock()
	o := spawnOpts{steps: m.App.Cfg.Assistant.Steps(), model: c.data.Model, effort: c.data.Effort,
		sessionID: c.data.ClaudeSessionID, resume: c.data.SessionStarted, autoCompact: m.App.Cfg.Assistant.AutoCompact()}
	if cap := m.dollarCap(&c.data); cap > 0 {
		o.capped, o.budget = true, cap-c.data.CostUSD // real spend: Claude Code stops itself there too
	}
	if o.model == "" {
		o.model = m.App.Cfg.Assistant.Model
	}
	if o.effort == "" {
		o.effort = m.App.Cfg.Assistant.Effort
	}
	spawnCost := c.data.CostUSD
	c.mu.Unlock()
	if !pr.caps.sessionID && !o.resume {
		o.sessionID = "" // learn it from the stream instead
	}
	args := claudeArgs(o, pr.caps, allowed, denied)
	env := childEnv(os.Environ(), m.binDir(), m.App.Cfg.Home(), cdir)
	if m.App.Cfg.Assistant.CachesAnHour() {
		// The conversation stays in the prompt cache through a pause (D181).
		env = append(env, "CLAUDE_CODE_PROMPT_CACHE_TTL=1h", "CLAUDE_CODE_SUBAGENT_PROMPT_CACHE_TTL=1h")
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't start Claude Code.")
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &proc{stdin: w, done: make(chan struct{}), cancel: cancel, resumed: o.resume, spawnCost: spawnCost,
		parser: newParser(cwd, cdir, scratch, realPath(m.dir()), func(id string) string { return m.lookupName(context.Background(), id) })}
	p.parser.aliases = [][2]string{{"scratch", m.App.Cfg.Scratch.Dir}, {"assistant", m.dir()}, {"cache", cache}, {"cache", m.App.Cfg.Cache.Dir}}
	p.logf, _ = os.OpenFile(filepath.Join(m.convDir(c.id), "stream.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if st, err := os.Stat(filepath.Join(m.convDir(c.id), "stream.jsonl")); err == nil {
		p.logged = int(st.Size())
	}
	started := make(chan int, 1)
	var closeR sync.Once
	go func() {
		_, err := m.App.Tools.Run(ctx, tools.Spec{Tool: tools.Claude, Args: args, Dir: cwd, Env: env, CleanEnv: true, MaxLine: 32 << 20, Stdin: r,
			OnStart: func(pid int) {
				closeR.Do(func() { _ = r.Close() })
				started <- pid
			},
			OnLine: func(line string) { m.onLine(c, p, line) }})
		closeR.Do(func() { _ = r.Close() })
		select {
		case started <- 0:
		default:
		}
		p.err = err
		var ef *tools.ErrFailed
		if errors.As(err, &ef) {
			p.stderr = ef.Stderr
		}
		if p.logf != nil {
			_ = p.logf.Close()
		}
		p.closeStdin()
		close(p.done)
		m.onExit(c, p)
	}()
	pid := <-started
	if pid == 0 {
		<-p.done
		e := apperr.New(apperr.ToolFailed, "Claude Code didn't start. Send your message again; if it keeps happening, open Claude Code once in a terminal to see why.")
		if p.err != nil {
			e = e.WithDetails(map[string]any{"error": firstLine(p.err.Error())})
		}
		return nil, e
	}
	p.pid = pid
	action := logbook.ConvLaunch
	if o.resume {
		action = logbook.ConvResume
	}
	m.logLifecycle(c, action, map[string]any{"model": o.model, "effort": o.effort, "budgetLeftUsd": o.budget, "tokensUsed": c.tokensUsed()})
	return p, nil
}

// prepareDirs writes the workspace (skills, CLAUDE.md), the shim in its own
// folder and the conversation folder, before every launch and resume. Files
// from the earlier single-folder layout are removed.
func (m *Manager) prepareDirs(id string) error {
	for _, old := range []string{filepath.Join(m.dir(), ".claude"), filepath.Join(m.dir(), "CLAUDE.md"), filepath.Join(m.dir(), "bin")} {
		_ = os.RemoveAll(old)
	}
	if err := prepareWorkspace(m.workspaceDir()); err != nil {
		return err
	}
	if err := writeShim(m.binDir(), m.Exe, m.App.Cfg.Home()); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(m.convDir(id), "proposals"), 0o700); err != nil {
		return err
	}
	return os.MkdirAll(m.App.Cfg.Scratch.Dir, 0o755)
}

// endProcess ends a conversation's process (closing stdin first, then the
// process group) and waits for it. The conversation stays resumable.
func (m *Manager) endProcess(c *conv) {
	c.mu.Lock()
	p := c.proc
	c.mu.Unlock()
	if p == nil {
		return
	}
	p.closeStdin()
	select {
	case <-p.done:
		return
	case <-time.After(m.graceTime()):
	}
	p.cancel()
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
	}
}

/* ---------- Stream handling ---------- */

func (m *Manager) onLine(c *conv, p *proc, line string) {
	if p.logf != nil && p.logged < maxLogBytes {
		n, _ := p.logf.WriteString(line + "\n")
		p.logged += n
	}
	out := p.parser.line(line)
	c.mu.Lock()
	if c.proc != p && c.turnProc != p {
		c.mu.Unlock()
		return
	}
	turn := c.runningTurn()
	if out.init {
		c.data.SessionStarted = true
		if p.parser.model != "" {
			c.data.ModelUsed = p.parser.model
		}
		if p.parser.version != "" {
			c.data.ClaudeVersion = p.parser.version
		}
	}
	if sid := p.parser.sessionID; sid != "" && c.data.ClaudeSessionID == "" {
		c.data.ClaudeSessionID = sid
	}
	for _, e := range out.events {
		e.Turn = turn
		c.addEvent(e)
	}
	stop := false
	if turn > 0 && out.used.sum() > 0 {
		// Counted as it goes: a message that reaches the limit stops there (D181).
		c.liveUsed = c.liveUsed.plus(out.used)
		if !c.overLimit && !c.data.Turns[turn-1].Compact && m.overLimitLocked(c, c.liveUsed) == "tokens" {
			c.overLimit, stop = true, true
		}
	}
	if out.compacted != nil {
		c.data.Compactions++
		if out.compacted.Post > 0 {
			c.data.CompactedTo, c.data.ContextTokens = out.compacted.Post, out.compacted.Post
		}
	}
	if op := out.partial; op != nil && turn > 0 {
		switch {
		case op.start:
			c.partial = AssistantPartial{Turn: turn, Block: c.partial.Block + 1, Phase: op.phase}
		case op.delta != "":
			if len(c.partial.Text) < 64<<10 {
				c.partial.Text += op.delta
			}
			c.partial.Turn, c.partial.Phase = turn, "text"
		case op.clear:
			c.partial.Text, c.partial.Phase = "", ""
		}
		c.partialVer++
		c.wake()
	}
	var f followUp
	if out.result != nil && turn > 0 {
		f = m.finishTurnLocked(c, p, out.result, "", "")
	}
	c.mu.Unlock()
	if out.plan != nil {
		m.setPlanUsage(out.plan)
	}
	if stop {
		_ = p.interrupt()
	}
	for _, d := range out.denied {
		if a := m.actionFor(context.Background(), c, turn, d); a != nil {
			auto := m.startAuto(c, a) // a safe step, or a change Auto runs, starts by itself (D141, D186)
			c.mu.Lock()
			c.data.Actions = append(c.data.Actions, *a)
			if len(c.data.Actions) > maxActions {
				c.data.Actions = c.data.Actions[len(c.data.Actions)-maxActions:]
			}
			c.addEvent(AssistantEvent{Kind: "action", Turn: turn, ActionID: a.ID, Text: a.Title})
			c.touch()
			c.mu.Unlock()
			m.changed(c.id)
			if auto {
				m.save(c, true)
				go m.runAuto(c, a.ID)
			}
		}
	}
	if out.result != nil {
		m.afterTurn(c, f)
	} else if len(out.events) > 0 {
		m.save(c, false)
	}
}

// followUp is what happens after a turn ends (outside the lock).
type followUp struct {
	ended    bool
	next     *message
	sleep    bool
	title    string
	audit    map[string]any
	turn     int
	response string // the final reply in full (activity log)
	budget   bool   // the conversation reached its spending limit
	sender   who    // who sent the message (the turn's audit is theirs, D179)
}

// finishTurnLocked ends the running turn (caller holds c.mu). With a result it
// derives the state from Claude Code's report; otherwise state/msg are used.
func (m *Manager) finishTurnLocked(c *conv, p *proc, res *TurnResult, state, msg string) followUp {
	n := c.runningTurn()
	if n == 0 {
		return followUp{}
	}
	t := &c.data.Turns[n-1]
	now := time.Now().UTC()
	if res != nil {
		state, msg = m.resultState(c, res)
		total := res.TotalCostUSD
		if p != nil {
			if p.costMode == 0 {
				p.costMode = 1
				if p.resumed && total+1e-9 < p.spawnCost {
					p.costMode = 2
				}
			}
			if p.costMode == 2 {
				total += p.spawnCost
			}
		}
		if total > c.data.CostUSD {
			t.CostUSD = total - c.data.CostUSD
			c.data.CostUSD = total
		}
		var used TokenUsage
		switch {
		case res.session.sum() > 0:
			used = countSince(c.data.SessionTokens, res.session, res.message, c.data.Counting)
			c.data.SessionTokens, c.data.Counting = res.session.norm(), true
		case res.message.sum() > 0:
			used = res.message.norm()
		default:
			used = c.liveUsed.norm()
		}
		c.countLocked(t, used)
		t.DurationMs, t.Steps, t.Denials = res.DurationMs, res.Steps, res.Denials
		if res.ContextTokens > 0 {
			t.ContextTokens, c.data.ContextTokens = res.ContextTokens, res.ContextTokens
		}
		if res.ContextWindow > 0 {
			c.data.ContextWindow = res.ContextWindow
		}
	} else {
		t.DurationMs = now.Sub(t.StartedAt).Milliseconds()
		// No report (stopped, timed out, it exited): what its replies used,
		// which the session's next total already includes.
		c.data.SessionTokens = c.data.SessionTokens.plus(c.liveUsed)
		c.countLocked(t, c.liveUsed)
	}
	if state == TurnFailed && t.Compact && c.data.CompactedTo < c.data.ContextTokens {
		c.data.CompactedTo = c.data.ContextTokens // a summary that failed isn't tried again until the conversation grows
	}
	t.State, t.Error, t.EndedAt = state, msg, &now
	files := 0
	if t.Folder != "" {
		files = countProposals(filepath.Join(m.dir(), filepath.FromSlash(t.Folder)))
	}
	t.Proposals = files
	total := 0
	for _, tt := range c.data.Turns {
		total += tt.Proposals
	}
	c.data.ProposalCount = total
	if files > 0 {
		c.addEvent(AssistantEvent{Kind: "proposals", Turn: n, Count: files})
	}
	switch {
	case state == TurnInterrupted:
		c.addEvent(AssistantEvent{Kind: "notice", Turn: n, Text: "Interrupted."})
	case msg != "":
		c.addEvent(AssistantEvent{Kind: "error", Turn: n, Text: msg})
	}
	tr := &TurnResult{CostUSD: t.CostUSD, TotalCostUSD: c.data.CostUSD, DurationMs: t.DurationMs, Steps: t.Steps, Denials: t.Denials,
		ContextTokens: c.data.ContextTokens, ContextWindow: c.data.ContextWindow, Tokens: t.Tokens, TotalTokens: c.data.Tokens.Total}
	if res != nil {
		tr.Subtype, tr.IsError = res.Subtype, res.IsError
	}
	c.addEvent(AssistantEvent{Kind: "turn_end", Turn: n, State: state, Result: tr})
	f := followUp{ended: true, turn: n, sender: c.data.sender(n)}
	if p != nil {
		f.response, p.parser.final = p.parser.final, ""
	}
	if p != nil && p.parser.title != "" && c.data.TitleSource == "prompt" {
		c.data.Title, c.data.TitleSource = p.parser.title, "claude"
		f.title = p.parser.title
	}
	c.data.State = ConvIdle
	if c.turnTimer != nil {
		c.turnTimer.Stop()
		c.turnTimer = nil
	}
	c.turnProc = nil
	c.interrupting, c.timedOut = false, false
	c.lastActive = time.Now()
	c.partial.Text, c.partial.Phase = "", ""
	c.partialVer++
	reached := m.overLimitLocked(c, TokenUsage{})
	f.budget = reached != "" || state == TurnBudget
	if reached != "" && state != TurnBudget {
		c.addEvent(AssistantEvent{Kind: "notice", Turn: n, Text: m.limitText(&c.data, reached)})
	}
	c.overLimit, c.liveUsed = false, TokenUsage{}
	switch {
	case reached != "" || state == TurnBudget || c.stopping:
		if k := len(c.data.Queue); k > 0 {
			c.data.Queue = nil
			c.addEvent(AssistantEvent{Kind: "notice", Turn: n, Text: fmt.Sprintf("%s not sent.", pluralize(k, "queued message"))})
		}
	case len(c.data.Queue) > 0:
		q := c.data.Queue[0]
		c.data.Queue = c.data.Queue[1:]
		f.next = &message{prompt: q.Prompt, rc: q.Context, user: q.User, userID: q.UserID, note: q.Note, shown: q.Shown, answerTo: q.AnswerTo, question: q.Question, news: q.News,
			files: q.Attachments}
		c.starting = true
		c.data.State = ConvWorking // the queued message starts right away
	case state == TurnSucceeded && m.shouldCompactLocked(c):
		// Summarized right after a reply, while the cache is warm (D181).
		f.next = m.compactMessage(c)
		c.starting = true
		c.data.State = ConvWorking
	}
	if c.respawn && f.next == nil {
		f.sleep = true
	}
	f.audit = map[string]any{"turn": n, "state": state, "costUsd": t.CostUSD, "proposals": files, "prompt": clip(t.Prompt, 300)}
	c.touch()
	return f
}

func (m *Manager) resultState(c *conv, res *TurnResult) (string, string) {
	switch {
	case c.stopping:
		return TurnStopped, ""
	case c.timedOut:
		return TurnTimedOut, m.timedOutText()
	case c.interrupting:
		return TurnInterrupted, ""
	case c.overLimit:
		return TurnBudget, m.limitText(&c.data, "tokens")
	case strings.Contains(res.Subtype, "budget"):
		return TurnBudget, m.limitText(&c.data, "budget")
	case strings.Contains(res.Subtype, "max_turns"):
		return TurnFailed, fmt.Sprintf("Stopped: Claude Code used all %d steps it may take for one message. Ask it to carry on, or split the request into smaller ones.", m.App.Cfg.Assistant.Steps())
	case res.IsError:
		if res.Text != "" {
			return TurnFailed, "Claude Code reported a problem: " + firstLine(res.Text)
		}
		return TurnFailed, "Claude Code reported a problem and stopped. Send your message again to retry."
	}
	return TurnSucceeded, ""
}

// afterTurn persists, announces and continues with the next queued message.
func (m *Manager) afterTurn(c *conv, f followUp) {
	if f.ended && f.turn > 0 {
		m.addSheetCards(c, f.turn)
		m.addQuestionCards(c, f.turn)
		m.addKeepCards(c, f.turn) // links the person typed, files they attached (D166)
	}
	m.save(c, true)
	m.changed(c.id)
	if !f.ended {
		return
	}
	if f.title != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, _ = m.App.DB.C(store.CAssistantConversations).UpdateOne(ctx, bson.M{"_id": c.id, "workspaceId": m.App.WorkspaceID(), "titleSource": "prompt"},
			bson.M{"$set": bson.M{"title": f.title, "titleSource": "claude"}})
		cancel()
	}
	if f.audit != nil {
		m.auditAs(asActor(f.sender), "assistant.turn", "assistant_conversation", c.id, c.projectID(), f.audit)
	}
	if f.turn > 0 {
		m.logTurn(c, f.turn, f.response)
		if f.budget {
			c.mu.Lock()
			cost, budget, used, limit := c.data.CostUSD, c.data.BudgetUSD, c.data.Tokens.Total, m.limitTokens(&c.data)
			c.mu.Unlock()
			m.logLifecycle(c, logbook.ConvBudget, map[string]any{"turn": f.turn, "costUsd": cost, "budgetUsd": budget, "tokens": used, "limitTokens": limit})
		}
	}
	if m.OnTurnEnd != nil && f.turn > 0 {
		if snap := m.snapshot(context.Background(), c); f.turn <= len(snap.Turns) {
			m.OnTurnEnd(snap, snap.Turns[f.turn-1])
		}
	}
	if f.next != nil {
		next := *f.next
		go func() { _, _ = m.startTurn(c, next) }()
	} else if f.sleep {
		go m.endProcess(c)
	}
}

func (m *Manager) onExit(c *conv, p *proc) {
	c.mu.Lock()
	if c.proc == p {
		c.proc = nil
		c.data.HolderPID = 0
	}
	var f followUp
	if c.turnProc == p && c.runningTurn() > 0 {
		state, msg := m.exitReason(c, p)
		f = m.finishTurnLocked(c, p, nil, state, msg)
	}
	c.touch()
	c.mu.Unlock()
	m.afterTurn(c, f)
	if !f.ended {
		m.release(c)
	}
}

// exitReason is how a turn ends when its process exits (caller holds c.mu).
func (m *Manager) exitReason(c *conv, p *proc) (string, string) {
	switch {
	case c.stopping:
		return TurnStopped, ""
	case c.timedOut:
		return TurnTimedOut, m.timedOutText()
	case c.interrupting:
		return TurnInterrupted, ""
	case strings.Contains(p.stderr, "No conversation found") && c.data.Moved != nil && !c.data.Moved.Summary:
		// A conversation from another computer whose session Claude Code
		// didn't find here goes on from a summary (D345).
		m.toSummaryLocked(c, "missing")
		return TurnFailed, "Claude Code couldn't find its own record of this conversation on this computer, so MetaRush goes on from a summary of it. Send your message again."
	case strings.Contains(p.stderr, "No conversation found"):
		c.data.SessionLost = true
		return TurnFailed, "This conversation's Claude Code session is no longer on this machine, so it cannot be resumed. Start a new conversation."
	}
	msg := "Claude Code stopped unexpectedly. Send your message again to retry."
	if s := strings.TrimSpace(p.stderr); s != "" {
		msg += " It said: " + firstLine(s)
	} else if p.err != nil && !errors.Is(p.err, context.Canceled) {
		msg += " It said: " + firstLine(p.err.Error())
	}
	return TurnFailed, msg
}

// timedOutText says that a message ran out of time, and what to do.
func (m *Manager) timedOutText() string {
	return fmt.Sprintf("Stopped after %d minutes, the time limit for one message. Ask Claude to carry on, or split the request into smaller ones.", int(m.turnTimeout()/time.Minute))
}

func (m *Manager) onTimeout(c *conv, n int) {
	c.mu.Lock()
	if c.runningTurn() != n {
		c.mu.Unlock()
		return
	}
	c.timedOut = true
	p := c.turnProc
	c.mu.Unlock()
	if p != nil {
		m.escalate(c, p, n)
	}
}

// escalate stops a turn: the stream-json interrupt first (the process stays),
// then SIGINT (Claude Code ends the turn and exits; the session is kept), then
// killing the process group.
func (m *Manager) escalate(c *conv, p *proc, n int) {
	_ = p.interrupt()
	go func() {
		if m.waitTurnEnd(c, n, m.graceTime()) {
			return
		}
		if p.pid > 0 {
			_ = syscall.Kill(p.pid, syscall.SIGINT)
		}
		if m.waitTurnEnd(c, n, m.graceTime()) {
			return
		}
		p.kill()
	}()
}

func (m *Manager) waitTurnEnd(c *conv, n int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		running := c.runningTurn() == n
		c.mu.Unlock()
		if !running {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

/* ---------- Interrupt, stop, settings ---------- */

// Interrupt stops the running turn; the conversation (and its process) go on.
// Queued messages are handed back unsent.
func (m *Manager) Interrupt(ctx context.Context, id string) (*ConversationInterruptOut, error) {
	c, err := m.live(ctx, id, true)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	n := c.runningTurn()
	if n == 0 {
		requeued := c.data.Queue
		c.data.Queue = nil
		if len(requeued) > 0 {
			c.touch()
		}
		c.mu.Unlock()
		if len(requeued) == 0 {
			return nil, apperr.New(apperr.Conflict, "Claude isn't working on anything in this conversation.").WithDetails(map[string]any{"reason": "idle"})
		}
		m.save(c, true)
		m.changed(c.id)
		return &ConversationInterruptOut{Conversation: m.snapshot(ctx, c), Requeued: requeued}, nil
	}
	requeued := c.data.Queue
	c.data.Queue = nil
	c.interrupting = true
	p := c.turnProc
	var f followUp
	if p == nil {
		f = m.finishTurnLocked(c, nil, nil, TurnInterrupted, "")
	}
	c.touch()
	c.mu.Unlock()
	if p != nil {
		m.escalate(c, p, n)
		m.waitTurnEnd(c, n, 3*m.graceTime())
	} else {
		m.afterTurn(c, f)
	}
	if requeued == nil {
		requeued = []QueuedMessage{}
	}
	m.save(c, true)
	m.changed(c.id)
	m.logLifecycle(c, logbook.ConvInterrupt, map[string]any{"turn": n, "handedBack": len(requeued)})
	return &ConversationInterruptOut{Conversation: m.snapshot(ctx, c), Requeued: requeued}, nil
}

// Stop ends the conversation's Claude Code process (and its turn, if one is
// running). The conversation can be continued later (it resumes the session).
func (m *Manager) Stop(ctx context.Context, id string) (*Conversation, error) {
	c, err := m.live(ctx, id, true)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.stopping = true
	c.data.Queue = nil
	n := c.runningTurn()
	p := c.turnProc
	var f followUp
	if n > 0 && p == nil {
		f = m.finishTurnLocked(c, nil, nil, TurnStopped, "")
	}
	c.touch()
	c.mu.Unlock()
	m.afterTurn(c, f)
	if n > 0 && p != nil {
		_ = p.interrupt()
		m.waitTurnEnd(c, n, m.graceTime())
	}
	m.endProcess(c)
	c.mu.Lock()
	c.stopping = false
	c.mu.Unlock()
	m.save(c, true)
	m.changed(c.id)
	m.logLifecycle(c, logbook.ConvStop, map[string]any{"turn": n})
	snap := m.snapshot(ctx, c)
	return &snap, nil
}

// Dequeue removes a waiting message.
func (m *Manager) Dequeue(ctx context.Context, in ConversationDequeueIn) (*Conversation, error) {
	c, err := m.live(ctx, in.ID, true)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	found := false
	kept := c.data.Queue[:0]
	for _, q := range c.data.Queue {
		if q.ID == in.QueueID {
			found = true
			continue
		}
		kept = append(kept, q)
	}
	c.data.Queue = kept
	if found {
		c.touch()
	}
	c.mu.Unlock()
	if !found {
		return nil, apperr.New(apperr.NotFound, "That waiting message is gone (it may have been sent already).").WithDetails(map[string]any{"queueId": in.QueueID})
	}
	m.save(c, true)
	m.changed(c.id)
	snap := m.snapshot(ctx, c)
	return &snap, nil
}

// Configure changes the budget or model; the process restarts (resuming the
// session) before the next message so the new settings apply.
func (m *Manager) Configure(ctx context.Context, in ConversationConfigureIn) (*Conversation, error) {
	if in.BudgetUSD != nil {
		if err := ValidBudget(*in.BudgetUSD); err != nil {
			return nil, err
		}
	}
	if in.TokenLimitM != nil {
		if err := ValidTokenLimit(*in.TokenLimitM); err != nil {
			return nil, err
		}
	}
	if in.BudgetUSD == nil && in.TokenLimitM == nil && in.Model == nil && in.Effort == nil {
		return nil, apperr.New(apperr.InvalidInput, "Nothing to change: choose a limit, a model or an effort level.")
	}
	c, err := m.live(ctx, in.ID, true)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	model, effort := c.data.Model, c.data.Effort
	c.mu.Unlock()
	if in.Model != nil {
		model = *in.Model
	}
	if in.Effort != nil {
		effort = *in.Effort
	}
	model, effort = orDefault(model), orDefault(strings.ToLower(effort))
	// Validate the combination that will run (installation defaults fill the gaps).
	if _, _, err := m.resolveModel(ctx, firstNonEmpty(model, m.App.Cfg.Assistant.Model), firstNonEmpty(effort, m.App.Cfg.Assistant.Effort)); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.data.Legacy {
		c.mu.Unlock()
		return nil, apperr.New(apperr.Conflict, "This single request from an older version of MetaRush can't be changed.")
	}
	changes := map[string]any{}
	if in.BudgetUSD != nil {
		c.data.BudgetUSD = *in.BudgetUSD
		changes["budgetUsd"] = *in.BudgetUSD
	}
	if in.TokenLimitM != nil {
		c.data.TokenLimitM = *in.TokenLimitM // MetaRush keeps it: Claude Code needn't restart
		changes["tokenLimitMillions"] = *in.TokenLimitM
	}
	if in.Model != nil {
		c.data.Model = model
		changes["model"] = model
	}
	if in.Effort != nil {
		c.data.Effort = effort
		changes["effort"] = effort
	}
	live := c.proc != nil && c.proc.alive() && (in.BudgetUSD != nil || in.Model != nil || in.Effort != nil)
	working := c.data.State == ConvWorking || c.starting
	if live {
		c.respawn = true
	}
	c.addEvent(AssistantEvent{Kind: "notice", Turn: len(c.data.Turns), Text: settingsNote(in, model, effort, c.data.BudgetUSD, m.limitTokens(&c.data))})
	c.touch()
	c.mu.Unlock()
	if live && !working {
		m.endProcess(c)
	}
	m.save(c, true)
	m.changed(c.id)
	m.auditAs(ctx, "assistant.configure", "assistant_conversation", c.id, c.projectID(), changes)
	snap := m.snapshot(ctx, c)
	return &snap, nil
}

func settingsNote(in ConversationConfigureIn, model, effort string, budget float64, limit int64) string {
	var parts []string
	if in.BudgetUSD != nil {
		parts = append(parts, fmt.Sprintf("Spending limit set to $%.2f", budget))
	}
	if in.TokenLimitM != nil {
		parts = append(parts, "Limit set to "+Millions(limit)+" tokens")
	}
	if in.Model != nil {
		if model == "" {
			parts = append(parts, "Model: the default")
		} else {
			parts = append(parts, "Model: "+model)
		}
	}
	if in.Effort != nil {
		if effort == "" {
			parts = append(parts, "Effort: the default")
		} else {
			parts = append(parts, "Effort: "+effort)
		}
	}
	return strings.Join(parts, " · ")
}

// Rename sets a conversation's title (from any machine).
func (m *Manager) Rename(ctx context.Context, in ConversationRenameIn) (*Conversation, error) {
	title := strings.TrimSpace(in.Title)
	if title == "" || utf8.RuneCountInString(title) > 120 || strings.ContainsAny(title, "\x00\n\r") {
		return nil, apperr.New(apperr.InvalidInput, "The title must be one line of 1 to 120 characters.")
	}
	return m.setMeta(ctx, in.ID, bson.M{"title": title, "titleSource": "user"}, func(d *Conversation) { d.Title, d.TitleSource = title, "user" }, "assistant.rename")
}

// Archive hides (or restores) a conversation (from any machine).
func (m *Manager) Archive(ctx context.Context, in ConversationArchiveIn) (*Conversation, error) {
	v := true
	if in.Archived != nil {
		v = *in.Archived
	}
	return m.setMeta(ctx, in.ID, bson.M{"archived": v}, func(d *Conversation) { d.Archived = v }, "assistant.archive")
}

func (m *Manager) setMeta(ctx context.Context, id string, set bson.M, apply func(*Conversation), action string) (*Conversation, error) {
	if !ValidID(id) {
		return nil, notFound(id)
	}
	m.ensureStarted(ctx)
	res, err := m.App.DB.C(store.CAssistantConversations).UpdateOne(ctx, bson.M{"_id": id, "workspaceId": m.App.WorkspaceID()}, bson.M{"$set": set})
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't save the change to this conversation.")
	}
	if res.MatchedCount == 0 {
		return nil, notFound(id)
	}
	m.mu.Lock()
	c := m.convs[id]
	m.mu.Unlock()
	if c != nil {
		c.mu.Lock()
		apply(&c.data)
		c.touch()
		c.mu.Unlock()
	}
	m.changed(id)
	m.auditAs(ctx, action, "assistant_conversation", id, "", map[string]any(set))
	return m.Get(ctx, id, false, false)
}

/* ---------- Idle reaping ---------- */

func (m *Manager) startReaper() {
	m.mu.Lock()
	if m.reaping || m.closed {
		m.mu.Unlock()
		return
	}
	m.reaping = true
	m.mu.Unlock()
	every := m.idleTimeout() / 4
	if every > 30*time.Second {
		every = 30 * time.Second
	}
	if every < 20*time.Millisecond {
		every = 20 * time.Millisecond
	}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for range t.C {
			m.mu.Lock()
			if m.closed {
				m.mu.Unlock()
				return
			}
			var idle []*conv
			for _, c := range m.convs {
				c.mu.Lock()
				if c.proc != nil && c.proc.alive() && c.data.State == ConvIdle && !c.starting && len(c.data.Queue) == 0 && !c.stopping &&
					time.Since(c.lastActive) > m.idleTimeout() {
					idle = append(idle, c)
				}
				c.mu.Unlock()
			}
			m.mu.Unlock()
			for _, c := range idle {
				go m.endProcess(c)
			}
		}
	}()
}

/* ---------- Persistence ---------- */

// save writes the conversation to MongoDB (debounced unless immediate). Fields
// people change from any machine (title, archived) are written only by
// Rename/Archive and picked up here.
func (m *Manager) save(c *conv, immediate bool) {
	c.mu.Lock()
	if !immediate {
		if c.saveTimer == nil {
			c.saveTimer = time.AfterFunc(1500*time.Millisecond, func() {
				c.mu.Lock()
				c.saveTimer = nil
				c.mu.Unlock()
				m.save(c, true)
			})
		}
		c.mu.Unlock()
		return
	}
	if c.saveTimer != nil {
		c.saveTimer.Stop()
		c.saveTimer = nil
	}
	c.saveVer++
	v := c.saveVer
	d := c.copyLocked(true)
	if c.starting {
		// A message being started is working already (mayContinue claimed it):
		// a save meanwhile never says otherwise (D345).
		d.State = ConvWorking
	}
	c.mu.Unlock()

	c.saveMu.Lock()
	defer c.saveMu.Unlock()
	if v <= c.savedVer || c.isGone() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	set := bson.M{"state": d.State, "turns": d.Turns, "queue": d.Queue, "events": d.Events, "actions": d.Actions, "costUsd": d.CostUSD,
		"budgetUsd": d.BudgetUSD, "model": d.Model, "effort": d.Effort, "modelUsed": d.ModelUsed, "claudeVersion": d.ClaudeVersion, "contextTokens": d.ContextTokens,
		"contextWindow": d.ContextWindow, "turnCount": d.TurnCount, "firstPrompt": d.FirstPrompt, "lastPrompt": d.LastPrompt,
		"proposalCount": d.ProposalCount, "compacted": d.Compacted, "holderPid": d.HolderPID, "sessionStarted": d.SessionStarted,
		"claudeSessionId": d.ClaudeSessionID, "projectId": d.ProjectID, "projectName": d.ProjectName, "updatedAt": d.UpdatedAt,
		"sessionLost": d.SessionLost, "pendingActions": pendingCount(d.Actions), "waitingFor": d.WaitingCounts(), "watching": runningWork(d.Actions),
		"untold": untoldCount(d.Actions), "tokens": d.Tokens, "sessionTokens": d.SessionTokens, "counting": d.Counting, "tokenLimitM": d.TokenLimitM,
		"compactions": d.Compactions, "compactedTo": d.CompactedTo, "metarushVersion": d.MetaRushVersion}
	if d.Account != nil {
		set["account"] = d.Account // whose Claude answered its latest message (D239)
	}
	if d.Moved != nil {
		set["moved"] = d.Moved // it came from another computer (D345)
	}
	onInsert := bson.M{"workspaceId": d.WorkspaceID, "userId": d.UserID, "userName": d.UserName, "installationId": d.InstallationID,
		"installationName": d.InstallationName, "createdAt": d.CreatedAt, "legacy": d.Legacy, "title": d.Title, "titleSource": d.TitleSource, "archived": d.Archived}
	// Only its first save makes the record: one that is gone (its project was
	// cleared out for good, D196) is never made again.
	c.mu.Lock()
	stored := c.stored
	c.mu.Unlock()
	filter := bson.M{"_id": d.ID, "workspaceId": d.WorkspaceID}
	if stored {
		// Only the computer holding it writes it (D345): once it moved to
		// another one, this one lets it go (forget, below).
		filter["installationId"] = d.InstallationID
	}
	res, err := m.App.DB.C(store.CAssistantConversations).UpdateOne(ctx, filter,
		bson.M{"$set": set, "$setOnInsert": onInsert}, options.UpdateOne().SetUpsert(!stored))
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: MetaRush couldn't save the conversation “%s”: %v\n", d.Title, err)
		return
	}
	if stored && res.MatchedCount == 0 {
		go m.forget(d.ID)
		return
	}
	c.mu.Lock()
	c.stored = true
	c.mu.Unlock()
	c.savedVer = v
	var meta struct {
		Title       string `bson:"title"`
		TitleSource string `bson:"titleSource"`
		Archived    bool   `bson:"archived"`
	}
	if m.App.DB.C(store.CAssistantConversations).FindOne(ctx, bson.M{"_id": d.ID},
		options.FindOne().SetProjection(bson.M{"title": 1, "titleSource": 1, "archived": 1})).Decode(&meta) == nil {
		c.mu.Lock()
		if meta.Title != c.data.Title && c.data.TitleSource != "claude" || meta.Archived != c.data.Archived || (meta.TitleSource == "user" && c.data.TitleSource != "user") {
			c.data.Title, c.data.TitleSource, c.data.Archived = meta.Title, meta.TitleSource, meta.Archived
			c.metaVer++
			c.wake()
		}
		c.mu.Unlock()
	}
}

/* ---------- Following (SSE) ---------- */

// FollowState is what a follower sees after its last read.
type FollowState struct {
	Events     []AssistantEvent
	Meta       *Conversation // set when it changed since metaVer
	MetaVer    int
	Partial    *AssistantPartial // set when it changed since partialVer
	PartialVer int
	Wait       <-chan struct{} // closed on the next change (live), or a poll timer
}

// Follow returns events after seq `after`, the conversation's metadata when it
// changed since metaVer (-1: always) and the live partial text, plus a channel
// for the next change. Conversations this process does not hold are polled.
func (m *Manager) Follow(ctx context.Context, id string, after, metaVer, partialVer int, raw bool) (*FollowState, error) {
	if !ValidID(id) {
		return nil, notFound(id)
	}
	m.ensureStarted(ctx)
	uid, _ := m.person(ctx)
	m.mu.Lock()
	c := m.convs[id]
	m.mu.Unlock()
	if c != nil {
		c.mu.Lock()
		st := &FollowState{Events: eventsAfter(c.data.Events, after), MetaVer: c.metaVer, PartialVer: c.partialVer, Wait: c.notify}
		var d Conversation
		send := c.metaVer != metaVer
		if send {
			d = c.copyLocked(false)
		}
		live := c.proc != nil && c.proc.alive()
		if c.partialVer != partialVer {
			pp := c.partial
			st.Partial = &pp
		}
		turns := append([]Turn(nil), c.data.Turns...)
		c.mu.Unlock()
		if send {
			meta := m.present(d, false, raw, live, uid)
			meta.ProjectArchived = m.projectArchived(ctx, meta.ProjectID)
			meta.KeptHere = m.keptHere(ctx, meta.ProjectID, meta.Account)
			st.Meta = &meta
		}
		if !raw {
			st.Events = plainEvents(stripEvents(st.Events), turns)
		}
		return st, nil
	}
	d, err := m.loadDoc(ctx, id)
	if err != nil {
		return nil, err
	}
	ver := int(d.UpdatedAt.UnixMilli() % math.MaxInt32)
	st := &FollowState{Events: eventsAfter(d.Events, after), MetaVer: ver, PartialVer: partialVer}
	if ver != metaVer {
		meta := m.present(*d, false, raw, false, uid)
		meta.ProjectArchived = m.projectArchived(ctx, meta.ProjectID)
		meta.KeptHere = m.keptHere(ctx, meta.ProjectID, meta.Account)
		st.Meta = &meta
	}
	if !raw {
		st.Events = plainEvents(stripEvents(st.Events), d.Turns)
	}
	poll := make(chan struct{})
	time.AfterFunc(3*time.Second, func() { close(poll) })
	st.Wait = poll
	return st, nil
}

func pendingCount(as []AssistantAction) int {
	n := 0
	for _, a := range as {
		if a.State == ActionPending {
			n++
		}
	}
	return n
}

func eventsAfter(evs []AssistantEvent, after int) []AssistantEvent {
	i := sort.Search(len(evs), func(i int) bool { return evs[i].Seq > after })
	return append([]AssistantEvent(nil), evs[i:]...)
}

func stripEvents(evs []AssistantEvent) []AssistantEvent {
	for i := range evs {
		if t := evs[i].Tool; t != nil && (t.Input != "" || t.Output != "") {
			cp := *t
			cp.Input, cp.Output = "", ""
			evs[i].Tool = &cp
		}
	}
	return evs
}

/* ---------- Context and prompts ---------- */

var clipIDRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// resolveContext validates the IDs from the dashboard and looks up their names,
// with what Claude reads about the project (people, library, what it keeps).
func (m *Manager) resolveContext(ctx context.Context, in AssistantMessageIn) (TurnContext, error) {
	rc, err := m.resolveIDs(ctx, in)
	if err != nil {
		return rc, err
	}
	m.enrich(ctx, &rc)
	return rc, nil
}

// scopeContext is a message's context in its conversation (D185): a
// conversation keeps the project it started in, so every message carries that
// project's context (its current version, its people, what it keeps) wherever
// it was typed; what the page has selected (the version in view, the clip, the
// playhead) goes along only when the page is that project. Footage picked
// outside any project goes along too. A whole-workspace conversation stays
// workspace-level. A page in another project is a one-line hint, never a move.
func (m *Manager) scopeContext(ctx context.Context, convProject string, in AssistantMessageIn) (TurnContext, error) {
	page, err := m.resolveIDs(ctx, in)
	if err != nil {
		return page, err
	}
	if page.ProjectID == convProject {
		m.enrich(ctx, &page)
		return page, nil
	}
	scoped := AssistantMessageIn{ProjectID: convProject}
	if page.ProjectID == "" {
		scoped.AssetID = in.AssetID // footage picked in the Library, say
		if in.TimeBase == "source" {
			scoped.Time, scoped.TimeBase = in.Time, in.TimeBase
		}
	}
	rc, err := m.resolveIDs(ctx, scoped)
	if err != nil {
		if convProject != "" && apperr.From(err).Code == apperr.NotFound {
			// The project is gone: the conversation reads the workspace.
			rc, err = m.resolveIDs(ctx, AssistantMessageIn{AssetID: scoped.AssetID, Time: scoped.Time, TimeBase: scoped.TimeBase})
		}
		if err != nil {
			return rc, err
		}
	}
	if page.ProjectID != "" {
		rc.viewing = fmt.Sprintf("%q (%s)", page.ProjectName, page.ProjectID)
	}
	m.enrich(ctx, &rc)
	return rc, nil
}

// convContext is a conversation's own context now (its project and current
// version), for messages that come from MetaRush rather than a page: notes,
// answers, a "Not quite", a turned-down version.
func (m *Manager) convContext(ctx context.Context, c *conv) TurnContext {
	rc, err := m.scopeContext(ctx, c.projectID(), AssistantMessageIn{})
	if err != nil {
		return c.lastContext()
	}
	return rc
}

// enrich adds what Claude reads about the project in view: who speaks, what it
// can use, what it keeps (message-only).
func (m *Manager) enrich(ctx context.Context, rc *TurnContext) {
	a := m.App
	rc.people = peopleContext(ctx, a, rc.AssetID, rc.ProjectID)
	rc.library = libraryContext(ctx, a, rc.ProjectID)
	if rc.ProjectID != "" {
		var p models.Project
		if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), rc.ProjectID, &p); err == nil {
			rc.memory = memoryContext(ctx, a, &p)
		}
	}
	// What the client keeps from Claude (What may leave, D290): said plainly,
	// so Claude doesn't look for it another way.
	if line := leave.ClaudeRule(ctx, a, rc.ProjectID, m.terms()); line != "" {
		rc.memory = append(rc.memory, "- "+line)
	}
}

// keptReads deny Claude every file under the scratch and cache folders
// (D290); Claude Code applies Read rules to listing and searching too.
func keptReads(scratch, cache string) []string {
	var out []string
	for _, r := range []string{scratch, cache} {
		if r != "" && filepath.IsAbs(r) && !unsafeRuleChars.MatchString(r) {
			out = append(out, "Read(/"+r+"/**)")
		}
	}
	return out
}

// terms are the terms of the Claude account answering here (D239), "" unknown.
func (m *Manager) terms() string {
	if acct := m.probe(context.Background(), false).status.Account; acct != nil {
		return acct.Terms
	}
	return ""
}

// keptHere is the panel's line for a conversation whose client keeps things
// from Claude (What may leave, D290).
func (m *Manager) keptHere(ctx context.Context, projectID string, acct *ClaudeAccount) string {
	terms := ""
	if acct != nil {
		terms = acct.Terms
	} else {
		terms = m.terms()
	}
	return leave.ClaudeNotice(ctx, m.App, projectID, terms)
}

// resolveIDs validates the IDs from the dashboard and looks up their names.
func (m *Manager) resolveIDs(ctx context.Context, in AssistantMessageIn) (TurnContext, error) {
	a := m.App
	rc := TurnContext{ClipID: in.ClipID, TimeBase: in.TimeBase}
	if in.ClipID != "" && !clipIDRe.MatchString(in.ClipID) {
		return rc, apperr.New(apperr.InvalidInput, "MetaRush can't tell which clip is selected.").WithDetails(map[string]any{"clipId": in.ClipID})
	}
	if in.TimeBase != "" && in.TimeBase != "source" && in.TimeBase != "timeline" {
		return rc, apperr.New(apperr.InvalidInput, "MetaRush can't tell where the playhead is.").WithDetails(map[string]any{"timeBase": in.TimeBase})
	}
	if in.Time != nil {
		t := *in.Time
		if math.IsNaN(t) || math.IsInf(t, 0) || t < 0 || t > 1e7 {
			return rc, apperr.New(apperr.InvalidInput, "The playhead time is outside the video.")
		}
		rc.Time = &t
	}
	projectID := in.ProjectID
	if in.RevisionID != "" {
		if !ids.Valid(ids.Revision, in.RevisionID) {
			return rc, apperr.New(apperr.InvalidInput, "MetaRush can't find that version of the cut.").WithDetails(map[string]any{"revisionId": in.RevisionID})
		}
		var rev models.Revision
		if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), in.RevisionID, &rev); err != nil {
			return rc, apperr.New(apperr.NotFound, "MetaRush can't find that version of the cut.").WithDetails(map[string]any{"revisionId": in.RevisionID})
		}
		if projectID != "" && rev.ProjectID != projectID {
			return rc, apperr.New(apperr.InvalidInput, "Version %d belongs to another project.", rev.Number).WithDetails(map[string]any{"revisionId": rev.ID})
		}
		projectID, rc.RevisionID, rc.RevisionNumber = rev.ProjectID, rev.ID, rev.Number
	}
	if projectID != "" {
		if !ids.Valid(ids.Project, projectID) {
			return rc, apperr.New(apperr.InvalidInput, "MetaRush can't find that project.").WithDetails(map[string]any{"projectId": projectID})
		}
		var p models.Project
		if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), projectID, &p); err != nil {
			return rc, apperr.New(apperr.NotFound, "MetaRush can't find that project.").WithDetails(map[string]any{"projectId": projectID})
		}
		rc.ProjectID, rc.ProjectName = p.ID, p.Name
		if h := p.Branches["main"].Head; h != "" {
			var head models.Revision
			if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), h, &head); err == nil {
				rc.HeadRevisionID, rc.HeadRevisionNumber = head.ID, head.Number
			}
		}
	}
	if in.AssetID != "" {
		if !ids.Valid(ids.Asset, in.AssetID) {
			return rc, apperr.New(apperr.InvalidInput, "MetaRush can't find that clip.").WithDetails(map[string]any{"assetId": in.AssetID})
		}
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as); err != nil {
			return rc, apperr.New(apperr.NotFound, "MetaRush can't find that clip.").WithDetails(map[string]any{"assetId": in.AssetID})
		}
		rc.AssetID, rc.AssetName = as.ID, clipName(&as)
	}
	return rc, nil
}

// composeMessage is what Claude Code receives for one turn: the resolved
// context, this turn's proposals folder and the person's words. The thread only
// shows the person's words (the rest is the raw view).
func composeMessage(rc TurnContext, msg message, n int, proposals string) string {
	var b strings.Builder
	if msg.note {
		fmt.Fprintf(&b, "Message %d of this conversation: an update from the MetaRush dashboard (not typed by the person).\n", n)
	} else {
		fmt.Fprintf(&b, "Message %d of this conversation, from the MetaRush dashboard", n)
		if msg.user != "" {
			fmt.Fprintf(&b, " (from %s)", msg.user)
		}
		b.WriteString(".\n")
	}
	if msg.whatsNew != "" {
		b.WriteString("\n" + msg.whatsNew + "\n")
	}
	if msg.moved != "" {
		b.WriteString("\n" + msg.moved + "\n")
	}
	if msg.summary != "" {
		b.WriteString("\n" + msg.summary + "\n")
	}
	b.WriteString("\nContext:\n")
	if rc.ProjectID != "" {
		fmt.Fprintf(&b, "- Project %q (%s): this conversation is about this project\n", rc.ProjectName, rc.ProjectID)
		if rc.HeadRevisionID != "" {
			fmt.Fprintf(&b, "- Current head of main: r%d (%s)\n", rc.HeadRevisionNumber, rc.HeadRevisionID)
		} else {
			b.WriteString("- The project has no revisions yet (a first cut must be a full spec)\n")
		}
		if rc.viewing != "" {
			fmt.Fprintf(&b, "- The person wrote this while looking at another project, %s; this conversation stays about %q\n", rc.viewing, rc.ProjectName)
		}
	} else {
		b.WriteString("- No project: this conversation is about the whole workspace\n")
		if rc.viewing != "" {
			fmt.Fprintf(&b, "- The person is looking at project %s\n", rc.viewing)
		}
	}
	if rc.RevisionID != "" && rc.RevisionID != rc.HeadRevisionID {
		fmt.Fprintf(&b, "- Revision in view: r%d (%s)\n", rc.RevisionNumber, rc.RevisionID)
	}
	if rc.ClipID != "" {
		fmt.Fprintf(&b, "- Selected clip: %q\n", rc.ClipID)
	}
	switch {
	case rc.AssetID != "":
		at := ""
		if rc.Time != nil && rc.TimeBase == "source" {
			at = " at " + timecode(*rc.Time) + " source time"
		}
		fmt.Fprintf(&b, "- Selected footage: %q (%s)%s\n", rc.AssetName, rc.AssetID, at)
	case rc.Time != nil && rc.TimeBase == "timeline" && *rc.Time > 0.05:
		fmt.Fprintf(&b, "- Playhead: %s into the cut\n", timecode(*rc.Time))
	}
	for _, l := range rc.people {
		b.WriteString(l + "\n")
	}
	for _, l := range rc.library {
		b.WriteString(l + "\n")
	}
	for _, l := range rc.memory {
		b.WriteString(l + "\n")
	}
	if msg.soFar != "" {
		fmt.Fprintf(&b, "\nThe project so far (from MetaRush, newest first; `metarush project so-far %s --json` reads it again):\n%s\n", rc.ProjectID, msg.soFar)
	}
	if len(msg.attached) > 0 {
		b.WriteString("\nAttached to this message:\n")
		for _, l := range msg.attached {
			b.WriteString(l + "\n")
		}
	}
	if len(msg.news) > 0 {
		b.WriteString("\nFrom the dashboard since your last reply (cards the person decided, or work that finished):\n")
		for _, l := range msg.news {
			b.WriteString("- " + l + "\n")
		}
	}
	if msg.question != "" {
		fmt.Fprintf(&b, "\nThis message answers your question card %q.\n", msg.question)
	}
	fmt.Fprintf(&b, "\nProposals folder for this message: %s\n\nMessage:\n%s\n", proposals, msg.prompt)
	return b.String()
}

// joinLines joins two texts as lines (either may be empty).
func joinLines(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "\n" + b
}

func timecode(s float64) string {
	m := int(s) / 60
	return fmt.Sprintf("%d:%06.3f", m, s-float64(m*60))
}

var titleTrim = regexp.MustCompile(`[\s.,;:!?…]+$`)
var sentenceEnd = regexp.MustCompile(`[.?!](\s|$)`)
var danglingWord = map[string]bool{"a": true, "an": true, "and": true, "or": true, "but": true, "the": true, "to": true, "of": true, "in": true, "on": true, "at": true, "with": true, "for": true, "from": true, "into": true, "so": true, "that": true}

// deriveTitle is the instant title from the first message (Claude's own
// title replaces it after the first reply unless the person renamed it). A
// short first sentence wins over a cut-off one ("What's in this project? Look
// at…" → "What's in this project").
func deriveTitle(prompt string) string {
	line := strings.TrimSpace(strings.SplitN(prompt, "\n", 2)[0])
	if loc := sentenceEnd.FindStringIndex(line); loc != nil {
		if n := len(strings.Fields(line[:loc[0]])); n >= 2 && n <= 6 {
			line = line[:loc[0]]
		}
	}
	words := strings.Fields(line)
	if len(words) > 6 {
		words = words[:6]
		// Don't end on a dangling "and"/"the"/"of".
		for len(words) > 2 && danglingWord[strings.ToLower(titleTrim.ReplaceAllString(words[len(words)-1], ""))] {
			words = words[:len(words)-1]
		}
	}
	t := titleTrim.ReplaceAllString(strings.Join(words, " "), "")
	if t == "" {
		return "New conversation"
	}
	r, size := utf8.DecodeRuneInString(t)
	t = strings.ToUpper(string(r)) + t[size:]
	return clip(t, 60)
}
