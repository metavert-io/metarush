package assistant

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/fixes"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/linkimport"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Work a card started that takes a while (D142): a render and the quality
// check that follows it, a quality check of an existing render, or jobs (a
// snapshot or repository fetch, a link import). The machine that holds the
// conversation watches it in the database — work another machine does counts
// too — and when it ends the card gets its outcome and Claude one note.

// Work states.
const (
	WorkRunning  = "running"
	WorkFinished = "finished"
	WorkFailed   = "failed"
	WorkGone     = "gone" // stopped waiting (too long, or what it watched is gone)
)

// Kinds of work.
const (
	WorkRender = "render" // a render, then its automatic quality check
	WorkQA     = "qa"     // a quality check of an existing render
	WorkJobs   = "jobs"   // jobs (fetch, import)
	WorkRun    = "run"    // a run, until it waits for Claude or a person or is ready (Recipes, D211)
)

// ActionWork is the work a card started and how it ended.
type ActionWork struct {
	Kind     string   `bson:"kind" json:"kind" jsonschema:"enum=render,enum=qa,enum=jobs,enum=run"`
	RenderID string   `bson:"renderId,omitempty" json:"renderId,omitempty"`
	JobIDs   []string `bson:"jobIds,omitempty" json:"jobIds,omitempty"`
	RunID    string   `bson:"runId,omitempty" json:"runId,omitempty"`
	// What names it for people ("the review copy of version 2").
	What    string `bson:"what" json:"what"`
	State   string `bson:"state" json:"state" jsonschema:"enum=running,enum=finished,enum=failed,enum=gone"`
	Outcome string `bson:"outcome,omitempty" json:"outcome,omitempty" jsonschema:"description=How it ended\\, in plain words."`
	// ForClaude is what Claude is told (IDs, the quality check's findings).
	ForClaude string     `bson:"forClaude,omitempty" json:"-"`
	Link      string     `bson:"link,omitempty" json:"link,omitempty" jsonschema:"description=Dashboard route to what it made."`
	StartedAt time.Time  `bson:"startedAt" json:"startedAt"`
	EndedAt   *time.Time `bson:"endedAt,omitempty" json:"endedAt,omitempty"`
	// RenderDoneAt is when the render finished (its quality check follows).
	RenderDoneAt *time.Time `bson:"renderDoneAt,omitempty" json:"-"`
}

const (
	defaultWorkEvery = 3 * time.Second
	// workGiveUp: work still running after this long stops being watched.
	workGiveUp = 12 * time.Hour
	// qaGrace: a finished render's quality check is queued by the worker right
	// away; without one after this long, the render is reported without it.
	qaGrace = 3 * time.Minute
)

func (m *Manager) workInterval() time.Duration {
	if m.workEvery > 0 {
		return m.workEvery
	}
	return defaultWorkEvery
}

// renderWork watches a render (and its quality check).
func renderWork(out *ops.RenderOut, what string) *ActionWork {
	if out == nil || out.Render.ID == "" {
		return nil
	}
	return &ActionWork{Kind: WorkRender, RenderID: out.Render.ID, What: what, State: WorkRunning, StartedAt: time.Now().UTC(),
		Link: "/renders/" + out.Render.ID}
}

// workFor is the work an op's result started, if it has to be waited for.
func (m *Manager) workFor(ctx context.Context, res any) *ActionWork {
	now := time.Now().UTC()
	switch o := res.(type) {
	case *ops.RenderOut:
		return renderWork(o, renderWhat(o.Render))
	case *ops.QARunOut:
		if o.Report != nil || o.Job == nil {
			return nil // it had been checked already: the card says how it went
		}
		return &ActionWork{Kind: WorkQA, RenderID: o.RenderID, JobIDs: []string{o.Job.ID}, What: "the quality check", State: WorkRunning, StartedAt: now,
			Link: "/renders/" + o.RenderID}
	case *ops.ResourceFetchOut:
		if o.JobID == "" {
			return nil
		}
		what := map[string]string{"repo": "the repository", "page": "the snapshot", "picture": "the download"}[o.Kind]
		if what == "" {
			what = "it"
		}
		return &ActionWork{Kind: WorkJobs, JobIDs: []string{o.JobID}, What: what, State: WorkRunning, StartedAt: now, Link: links.Resource(o.ProjectID, "")}
	case *linkimport.ImportOut:
		if len(o.JobIDs) == 0 {
			return nil
		}
		return &ActionWork{Kind: WorkJobs, JobIDs: append([]string(nil), o.JobIDs...), What: "the import", State: WorkRunning, StartedAt: now, Link: "/library"}
	case *ops.KeepAddOut:
		// A kept link or picture is still coming in (a snapshot, a download, an import).
		if len(o.Jobs) == 0 {
			return nil
		}
		return &ActionWork{Kind: WorkJobs, JobIDs: append([]string(nil), o.Jobs...), What: "bringing it in", State: WorkRunning, StartedAt: now,
			Link: links.Resource(o.Item.ProjectID, "")}
	case *ops.FootageSwitchesFindOut:
		if len(o.Jobs) == 0 {
			return nil
		}
		return &ActionWork{Kind: WorkJobs, JobIDs: append([]string(nil), o.Jobs...), What: "finding where the picture switches", State: WorkRunning, StartedAt: now}
	case *fixes.View:
		// A fixed copy being made: Claude hears when it's ready (and the clip uses it).
		if len(o.JobIDs) == 0 {
			return nil
		}
		return &ActionWork{Kind: WorkJobs, JobIDs: append([]string(nil), o.JobIDs...), What: "the fixed copy", State: WorkRunning, StartedAt: now}
	case *fixes.LookOut:
		if o.JobID == "" {
			return nil
		}
		return &ActionWork{Kind: WorkJobs, JobIDs: []string{o.JobID}, What: "the measuring", State: WorkRunning, StartedAt: now}
	case *aiwork.MakeOut:
		// Other AI work: Claude hears when it has landed (and what it cost, D241: what it settles at).
		if o.DryRun || o.JobID == "" || o.Work == nil {
			return nil
		}
		link := "/library"
		switch {
		case o.Work.Kind == "picture":
			link = links.Resource(o.Work.ProjectID, "")
		case o.Work.ProjectID != "":
			link = "/p/" + o.Work.ProjectID + "?tab=sources"
		}
		what := strings.TrimPrefix(strings.TrimPrefix(o.Estimate.What, "a "), "an ")
		return &ActionWork{Kind: WorkJobs, JobIDs: []string{o.JobID}, What: "the " + firstNonEmpty(what, "AI work"), State: WorkRunning, StartedAt: now, Link: link}
	case *generate.GenRunOut:
		// A generated shot: Claude hears when the clip has landed, not when it was asked for.
		if o.DryRun || len(o.JobIDs) == 0 {
			return nil
		}
		link := "/library"
		if len(o.Generations) > 0 && o.Generations[0].ProjectID != "" {
			link = "/p/" + o.Generations[0].ProjectID + "?tab=sources"
		}
		what := "the generated shot"
		if len(o.JobIDs) > 1 {
			what = "the generated shots"
		}
		return &ActionWork{Kind: WorkJobs, JobIDs: append([]string(nil), o.JobIDs...), What: what, State: WorkRunning, StartedAt: now, Link: link}
	}
	return recipeWork(res) // Recipes: a run a card started (recipe_actions.go)
}

/* ---------- Watching ---------- */

// ensureWatching starts the watcher when work is running and none watches
// (the daemon's manager only: a terminal command's leaves it to the daemon).
func (m *Manager) ensureWatching() {
	m.mu.Lock()
	if m.watching || m.closed || m.terminal || !m.anyWorkLocked() {
		m.mu.Unlock()
		return
	}
	m.watching = true
	m.mu.Unlock()
	go m.watchLoop()
}

// anyWorkLocked reports whether a live conversation has running work (caller holds m.mu).
func (m *Manager) anyWorkLocked() bool {
	for _, c := range m.convs {
		c.mu.Lock()
		running := runningWork(c.data.Actions) > 0
		c.mu.Unlock()
		if running {
			return true
		}
	}
	return false
}

func runningWork(as []AssistantAction) int {
	n := 0
	for _, a := range as {
		if a.Work != nil && a.Work.State == WorkRunning {
			n++
		}
	}
	return n
}

func (m *Manager) watchLoop() {
	t := time.NewTicker(m.workInterval())
	defer t.Stop()
	for range t.C {
		m.mu.Lock()
		closed := m.closed
		m.mu.Unlock()
		if !closed { // a MetaRush that is stopping leaves the work to the next one
			m.pollWork()
		}
		m.mu.Lock()
		if m.closed || !m.anyWorkLocked() {
			m.watching = false
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
	}
}

type workItem struct {
	c  *conv
	id string
	w  ActionWork
}

// pollWork checks every running piece of work once.
func (m *Manager) pollWork() {
	m.mu.Lock()
	cs := make([]*conv, 0, len(m.convs))
	for _, c := range m.convs {
		cs = append(cs, c)
	}
	m.mu.Unlock()
	for _, c := range cs {
		if m.checkConvWork(c) {
			m.save(c, true)
			m.changed(c.id)
			m.tellClaudeSoon(c)
		}
	}
}

// checkConvWork checks a conversation's running work once and records what
// ended; it reports whether anything did.
func (m *Manager) checkConvWork(c *conv) bool {
	var items []workItem
	c.mu.Lock()
	for _, a := range c.data.Actions {
		if a.Work != nil && a.Work.State == WorkRunning {
			items = append(items, workItem{c: c, id: a.ID, w: *a.Work})
		}
	}
	c.mu.Unlock()
	changed := false
	var finished []string
	for _, it := range items {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		w, done := m.checkWork(ctx, it.w)
		cancel()
		if !done && w.RenderDoneAt == it.w.RenderDoneAt {
			continue
		}
		if done && w.State == WorkFinished {
			finished = append(finished, it.id)
		}
		c.mu.Lock()
		for i := range c.data.Actions {
			a := &c.data.Actions[i]
			if a.ID == it.id && a.Work != nil && a.Work.State == WorkRunning {
				cp := w
				a.Work = &cp
				if done {
					c.touch()
					changed = true
				}
			}
		}
		c.mu.Unlock()
	}
	m.offerSiteBrands(c, finished) // a page's logo, once its snapshot is in (D165, D166)
	if changed {
		// A change Auto paid for that ended: the day's note says what it cost (D241).
		c.mu.Lock()
		var paid []string
		for _, it := range items {
			for _, a := range c.data.Actions {
				if a.ID == it.id && a.AutoPriced && a.Work != nil && a.Work.State != WorkRunning {
					paid = append(paid, a.ID)
				}
			}
		}
		c.mu.Unlock()
		for _, id := range paid {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			m.refreshAutoSpend(ctx, c, id)
			cancel()
		}
	}
	return changed
}

// checkWork looks at a piece of work once: done when it ended (w then says how).
func (m *Manager) checkWork(ctx context.Context, w ActionWork) (ActionWork, bool) {
	end := func(state, outcome, forClaude string) (ActionWork, bool) {
		now := time.Now().UTC()
		w.State, w.Outcome, w.ForClaude, w.EndedAt = state, outcome, forClaude, &now
		return w, true
	}
	if time.Since(w.StartedAt) > workGiveUp {
		return end(WorkGone, upperFirst(w.What)+" is still not finished after 12 hours; it shows in Renders and Activity when it is",
			"Stopped waiting for "+w.What+" after 12 hours (still not finished).")
	}
	switch w.Kind {
	case WorkRender:
		return m.checkRenderWork(ctx, w, end)
	case WorkQA:
		var rd models.Render
		if err := m.App.DB.GetByID(ctx, store.CRenders, m.App.WorkspaceID(), w.RenderID, &rd); err != nil {
			return end(WorkGone, "The render to check is gone", "The render "+w.RenderID+" is gone.")
		}
		return m.checkQA(ctx, w, &rd, w.JobIDs, end)
	case WorkJobs:
		return m.checkJobs(ctx, w, end)
	case WorkRun:
		return m.checkRunWork(ctx, w, end)
	}
	return end(WorkGone, "", "")
}

type ender func(state, outcome, forClaude string) (ActionWork, bool)

// checkRenderWork follows a render, then the quality check the worker queues
// after it (unless the render asked for none).
func (m *Manager) checkRenderWork(ctx context.Context, w ActionWork, end ender) (ActionWork, bool) {
	var rd models.Render
	if err := m.App.DB.GetByID(ctx, store.CRenders, m.App.WorkspaceID(), w.RenderID, &rd); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return end(WorkGone, upperFirst(w.What)+" is gone", "The render "+w.RenderID+" is gone.")
		}
		return w, false
	}
	switch rd.State {
	case models.RenderFailed:
		why := firstLine(rd.Error)
		out := upperFirst(w.What) + " didn't finish"
		if why != "" {
			out += ": " + plainTimes(withoutCodes(why))
		}
		return end(WorkFailed, out, fmt.Sprintf("The render %s of version %d (%s) failed: %s", rd.ID, rd.RevisionNumber, rd.RevisionID, why))
	case models.RenderCanceled:
		return end(WorkFailed, upperFirst(w.What)+" was canceled", fmt.Sprintf("The render %s of version %d was canceled.", rd.ID, rd.RevisionNumber))
	case models.RenderAvailable:
	default:
		return w, false
	}
	if w.RenderDoneAt == nil {
		// The database server's time, as the render's completion is written
		// (checkQA compares it with the server's clock: D360).
		now := m.App.DB.NowOrLocal(ctx)
		if rd.CompletedAt != nil {
			now = *rd.CompletedAt
		}
		w.RenderDoneAt = &now
	}
	ready := upperFirst(w.What) + " is ready"
	if o := rd.Output; o != nil && o.Duration > 0 {
		ready += " (" + lengthWords(o.Duration) + ")"
	}
	forClaude := fmt.Sprintf("The render %s of version %d (%s) is ready", rd.ID, rd.RevisionNumber, rd.RevisionID)
	if o := rd.Output; o != nil {
		forClaude += fmt.Sprintf(": %.1f s, %d×%d", o.Duration, o.Width, o.Height)
	}
	forClaude += "."
	if noQA(ctx, m, &rd) {
		return end(WorkFinished, ready+".", forClaude)
	}
	qw, done := m.checkQA(ctx, w, &rd, nil, func(state, outcome, fc string) (ActionWork, bool) {
		if state == WorkGone && outcome == "" { // no quality check came
			return end(WorkFinished, ready+".", forClaude)
		}
		return end(WorkFinished, ready+". "+outcome, forClaude+" "+fc)
	})
	return qw, done
}

// noQA reports whether the render asked for no quality check.
func noQA(ctx context.Context, m *Manager, rd *models.Render) bool {
	if rd.JobID == "" {
		return false
	}
	var j models.Job
	if m.App.DB.GetByID(ctx, store.CJobs, m.App.WorkspaceID(), rd.JobID, &j) != nil {
		return false
	}
	v, _ := j.Payload["noQA"].(bool)
	return v
}

// checkQA follows a render's quality check: the given jobs, or the latest one
// for the render. The outcome says what it found (Claude gets the findings).
func (m *Manager) checkQA(ctx context.Context, w ActionWork, rd *models.Render, jobIDs []string, end ender) (ActionWork, bool) {
	filter := bson.M{"workspaceId": m.App.WorkspaceID(), "type": jobs.TypeQA, "payload.renderId": rd.ID}
	if len(jobIDs) > 0 {
		filter = bson.M{"workspaceId": m.App.WorkspaceID(), "_id": bson.M{"$in": jobIDs}}
	}
	var j models.Job
	err := m.App.DB.C(store.CJobs).FindOne(ctx, filter, options.FindOne().SetSort(bson.D{{Key: "createdAt", Value: -1}})).Decode(&j)
	if err != nil {
		if rd.QA != nil && rd.QA.ReportID != "" {
			return m.qaOutcome(ctx, rd.QA.ReportID, end)
		}
		if w.RenderDoneAt != nil && m.App.DB.NowOrLocal(ctx).Sub(*w.RenderDoneAt) > qaGrace {
			return end(WorkGone, "", "")
		}
		return w, false
	}
	switch j.State {
	case models.JobSucceeded:
		id, _ := j.Result["reportId"].(string)
		if id == "" && rd.QA != nil {
			id = rd.QA.ReportID
		}
		return m.qaOutcome(ctx, id, end)
	case models.JobFailed, models.JobCanceled:
		return end(WorkFinished, "Its quality check didn't finish", "Its quality check (job "+j.ID+") didn't finish: "+firstLine(j.Error))
	}
	return w, false
}

// qaOutcome says what a quality check found: plainly for the card, with the
// findings for Claude.
func (m *Manager) qaOutcome(ctx context.Context, reportID string, end ender) (ActionWork, bool) {
	var rep models.QAReport
	if reportID == "" || m.App.DB.GetByID(ctx, store.CQAReports, m.App.WorkspaceID(), reportID, &rep) != nil {
		return end(WorkFinished, "The quality check is done", "The quality check is done (its report couldn't be read).")
	}
	out := qaWords(rep.Counts, rep.Verdict)
	if rep.Counts.Errors+rep.Counts.Warnings == 0 {
		if s := rep.Measurements; s.SpeechWordsExpected > 0 && s.SpeechWordsMatched >= s.SpeechWordsExpected {
			out += ", and every word was heard"
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Quality check %s: %s (%d problems, %d worth checking, %d notes).", rep.ID, rep.Verdict, rep.Counts.Errors, rep.Counts.Warnings, rep.Counts.Info)
	n := 0
	for _, sev := range []string{models.QAError, models.QAWarning} {
		for _, f := range rep.Findings {
			if f.Severity != sev || n >= 6 {
				continue
			}
			n++
			fmt.Fprintf(&b, " [%s %s at %s] %s", f.Severity, f.Kind, f.Timecode, clip(f.Message, 200))
		}
	}
	fmt.Fprintf(&b, " Read it with `metarush qa show %s --json`.", rep.ID)
	return end(WorkFinished, out+".", b.String())
}

// checkJobs follows jobs (a fetch, an import) until all have ended.
func (m *Manager) checkJobs(ctx context.Context, w ActionWork, end ender) (ActionWork, bool) {
	var js []models.Job
	if err := m.App.DB.Find(ctx, store.CJobs, m.App.WorkspaceID(), bson.M{"_id": bson.M{"$in": w.JobIDs}}, &js); err != nil {
		return w, false
	}
	if len(js) == 0 {
		return end(WorkGone, upperFirst(w.What)+" is gone", "Its jobs are gone.")
	}
	for _, j := range js {
		if j.State == models.JobQueued || j.State == models.JobRunning {
			return w, false
		}
	}
	var outs, fcs []string
	failed := 0
	for _, j := range js {
		o, fc, ok := jobOutcome(j)
		if !ok {
			failed++
		}
		outs = append(outs, o)
		fcs = append(fcs, fc)
	}
	state := WorkFinished
	if failed == len(js) {
		state = WorkFailed
	}
	return end(state, strings.Join(outs, ". "), strings.Join(fcs, " "))
}

// jobOutcome says how one finished job ended: plainly, and for Claude.
func jobOutcome(j models.Job) (shown, forClaude string, ok bool) {
	if j.State != models.JobSucceeded {
		why := firstLine(j.Error)
		switch j.Type {
		case jobs.TypeFetch:
			return "It couldn't be brought in: " + plainTimes(withoutCodes(why)), "The fetch job " + j.ID + " failed: " + why, false
		case jobs.TypeImport:
			return "The video couldn't be brought in: " + plainTimes(withoutCodes(why)), "The import job " + j.ID + " failed: " + why, false
		case jobs.TypeGenerate:
			// The generation wrote what happened and what to do in plain words (the free tier, D178).
			gen, _ := j.Payload["generationId"].(string)
			forClaude = "The generate job " + j.ID + " (generation " + gen + ") ended without a clip: " + strings.TrimRight(why, ".") + "."
			if j.State == models.JobCanceled {
				return "The shot was stopped", forClaude, false
			}
			said := withoutCodes(appCodeRe.ReplaceAllString(why, ""))
			if said == "" {
				said, _ = j.Payload["advice"].(string)
			}
			if adv, _ := j.Payload["advice"].(string); adv != "" {
				forClaude += " What to tell the person: " + adv
			}
			if gen != "" {
				forClaude += " Read it with `metarush generate show " + gen + " --json`."
			}
			return "The shot couldn't be made: " + strings.TrimRight(strings.TrimSpace(said), "."), forClaude, false
		case jobs.TypePrepare:
			return "The fixed copy couldn't be made: " + plainTimes(withoutCodes(why)), "The prepare job " + j.ID + " (fixed copy) failed: " + why + " The clip keeps playing what it played before.", false
		case jobs.TypeInspect:
			return "It couldn't be measured: " + plainTimes(withoutCodes(why)), "The inspect job " + j.ID + " failed: " + why, false
		}
		return "It didn't finish: " + why, "Job " + j.ID + " (" + j.Type + ") failed: " + why, false
	}
	switch j.Type {
	case jobs.TypeFetch:
		names := stringsOf(j.Result["names"])
		ids := stringsOf(j.Result["resourceIds"])
		project, _ := j.Payload["projectId"].(string)
		where := ""
		if project != "" {
			where = " --project " + project
		}
		shown = "It's in"
		if len(names) == 1 {
			shown = theirs(names[0]) + " is in the resources"
		} else if len(names) > 1 {
			shown = pluralize(len(names), "resource") + " are in: " + theirsList(names)
		}
		forClaude = "The fetch job " + j.ID + " finished: resources " + strings.Join(names, ", ") + " (" + strings.Join(ids, ", ") + ")"
		if msg, _ := j.Result["message"].(string); msg != "" {
			forClaude += ": " + msg
		}
		if len(names) > 0 {
			forClaude += ". Look at it with `metarush resources show " + names[0] + where + " --json`."
		}
		return shown, forClaude, true
	case jobs.TypeSwitches:
		asset, _ := j.Payload["assetId"].(string)
		n := 0
		switch v := j.Result["switches"].(type) {
		case int:
			n = v
		case int32:
			n = int(v)
		case int64:
			n = int(v)
		case float64:
			n = int(v)
		}
		shown = "MetaRush found where the picture switches"
		switch {
		case n == 1:
			shown = "The picture switches once"
		case n > 1:
			shown = "The picture switches " + strconv.Itoa(n) + " times"
		case n == 0:
			shown = "The picture doesn't switch"
		}
		return shown, "The switches job " + j.ID + " finished: " + strconv.Itoa(n) + " switches in " + asset + "; read them with `metarush footage switches " + asset + " --json`.", true
	case jobs.TypePrepare:
		what := "clip " + j.AssetID
		if r, _ := j.Payload["resourceId"].(string); r != "" {
			what = "picture " + r
		}
		sha, _ := j.Result["sha256"].(string)
		return "The fixed copy is ready, and everything after uses it", "The prepare job " + j.ID + " finished: " + what + " uses its fixed copy now (sha256 " + sha +
			"). Saved versions of cuts still play what they played; `metarush asset card " + firstNonEmpty(j.AssetID, payloadString(j.Payload, "resourceId")) + " --json` lists the cuts that can use it (cut use-fixed).", true
	case jobs.TypeInspect:
		return "MetaRush measured it again", "The inspect job " + j.ID + " finished: read the fixes that fit with `metarush asset fixes " + j.AssetID + " --json`.", true
	case jobs.TypeImport:
		title, _ := j.Result["title"].(string)
		asset, _ := j.Result["assetId"].(string)
		if status, _ := j.Result["status"].(string); status == "expanded" {
			return "The playlist's videos are coming in", "The import job " + j.ID + " queued its playlist's videos.", true
		}
		shown = "The video is in"
		if title != "" {
			shown = theirs(title) + " is in"
		}
		return shown, "The import job " + j.ID + " finished: clip " + asset + " (" + title + ").", true
	}
	return "Done", "Job " + j.ID + " (" + j.Type + ") finished.", true
}

// appCodeRe is a stable error code at the start of a job's error ("E_PROVIDER: …").
var appCodeRe = regexp.MustCompile(`^E_[A-Z_]+:\s*`)

func stringsOf(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case bson.A:
		return stringsOf([]any(x))
	}
	return nil
}

func theirsList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = theirs(n)
	}
	return strings.Join(q, ", ")
}

// lengthWords is a video's length for people: "45 s", "6 min 53 s".
func lengthWords(sec float64) string {
	s := int(sec + 0.5)
	if s < 60 {
		return strconv.Itoa(s) + " s"
	}
	if s%60 == 0 {
		return strconv.Itoa(s/60) + " min"
	}
	return strconv.Itoa(s/60) + " min " + strconv.Itoa(s%60) + " s"
}

// payloadString is a job payload's string field ("" when missing).
func payloadString(p map[string]any, k string) string {
	s, _ := p[k].(string)
	return s
}
