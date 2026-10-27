package cli

import (
	"context"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/components"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// A wait for jobs never waits for one no computer can run (D340): queued and
// due, no computer running MetaRush takes its kind of work, and this one
// can't either (it lacks a component, or doesn't take big jobs). The wait
// finishes what it can and then ends saying, in plain words, what waits and
// what adds it — an error like any other missing program (exit 6), with the
// command that adds it behind --details. A job pinned to another computer
// waits for that computer, as before; one another computer is running, or
// could take now, is waited for.

// hereCan is what this computer's worker runs, looked at once per wait.
type hereCan struct {
	can   map[string]bool   // job types it runs
	lacks map[string]string // job type → the component it lacks for it
	heavy bool              // it takes big jobs
}

func lookHere(a *app.App) *hereCan {
	w := newWorker(a)
	h := &hereCan{can: map[string]bool{}, lacks: map[string]string{}, heavy: w.AcceptsHeavy}
	for _, t := range w.JobTypes() {
		h.can[t] = true
	}
	for _, l := range w.Lacks() {
		h.lacks[l.JobType] = l.Component
	}
	return h
}

// stuckJob is a job a wait gives up on.
type stuckJob struct {
	job       models.Job
	component string // the component this computer lacks for it ("" when none adds it)
}

// stuckJobs are the jobs among pending that no computer can run now. here is
// looked at once, when the first job waits to start.
func stuckJobs(ctx context.Context, a *app.App, pending []models.Job, here **hereCan) []stuckJob {
	now := time.Now()
	me := a.Cfg.Installation.ID
	var out []stuckJob
	for _, j := range pending {
		if j.State != models.JobQueued || j.CancelRequested || now.Before(j.NextRunAt) {
			continue // running, being stopped, or waiting for its next try
		}
		if j.Installation != "" && j.Installation != me {
			continue // it waits for the computer it belongs to
		}
		if *here == nil {
			*here = lookHere(a)
		}
		h := *here
		if h.can[j.Type] && (!j.Heavy || h.heavy) {
			continue // this terminal runs it
		}
		ws, err := ops.OnlineWorkers(ctx, a, j.Type)
		if err != nil {
			continue
		}
		elsewhere := false
		for _, w := range ws {
			if j.Installation == "" || w.ID == j.Installation {
				elsewhere = true
			}
		}
		if elsewhere {
			continue // another computer running MetaRush takes it
		}
		out = append(out, stuckJob{job: j, component: h.lacks[j.Type]})
	}
	return out
}

// waitingWork names a kind of work that waits ("Transcripts wait for …");
// plural says the verb is "wait".
var waitingWork = map[string]struct {
	words  string
	plural bool
}{
	jobs.TypeProxy: {"preview copies", true}, jobs.TypeThumbnail: {"thumbnails", true}, jobs.TypeContactSheet: {"frame grids", true},
	jobs.TypeWaveform: {"waveforms", true}, jobs.TypeMezzanine: {"steady copies", true}, jobs.TypeEvidence: {"finding scenes and pauses", false},
	jobs.TypeTranscribe: {"transcripts", true}, jobs.TypeDiarize: {"telling voices apart", false}, jobs.TypeQA: {"quality checks", true},
	jobs.TypeSwitches: {"finding where the picture changes", false}, jobs.TypeInspect: {"measuring clips for their fixes", false},
	jobs.TypePrepare: {"fixed copies", true}, jobs.TypeRender: {"renders", true}, jobs.TypeVisual: {"seeing what's on screen", false},
	jobs.TypeRemotionImport: {"bringing in Remotion projects", false}, jobs.TypeRemotionRender: {"clips from Remotion projects", true},
	jobs.TypeRemotionTask: {"work on Remotion projects", false}, jobs.TypeRemotionRefresh: {"refreshing Remotion projects", false},
	jobs.TypeRemotionPush: {"sending changes back to a repository", false},
}

// whatWaits says the kinds of work in types for the start of a sentence:
// "Preview copies, thumbnails and waveforms", and whether it takes "wait".
func whatWaits(types []string) (string, bool) {
	var words []string
	plural := len(types) > 1
	for _, t := range types {
		w, ok := waitingWork[t]
		if !ok {
			w.words = strings.ToLower(ops.JobKindLabel(t))
		}
		plural = plural || w.plural
		words = append(words, w.words)
	}
	return capitalize(andList(words)), plural
}

func andList(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// componentName is a component's title inside a sentence ("the Remotion
// renderer", "Transcription on this computer", "FFmpeg").
func componentName(c *components.Component) string {
	if strings.HasPrefix(c.Title, "The ") || strings.HasPrefix(c.Title, "A ") {
		return lowerFirst(c.Title)
	}
	return c.Title
}

// waitingErr ends a wait whose remaining jobs no computer can run: per
// component, what waits for it and where it is added; the command that adds
// it in Details (with each job, for --json).
func waitingErr(ctx context.Context, a *app.App, stuck []stuckJob) *apperr.Error {
	var order []string // components (or "" for none) in the order first met
	types := map[string][]string{}
	seenType := map[string]bool{}
	var waiting []map[string]any
	for _, s := range stuck {
		if _, ok := types[s.component]; !ok {
			order = append(order, s.component)
			types[s.component] = nil
		}
		if k := s.component + "/" + s.job.Type; !seenType[k] {
			seenType[k] = true
			types[s.component] = append(types[s.component], s.job.Type)
		}
		w := map[string]any{"jobId": s.job.ID, "type": s.job.Type, "component": s.component}
		if s.job.AssetID != "" {
			w["assetId"] = s.job.AssetID
		}
		waiting = append(waiting, w)
	}
	var says, adds, cmds []string
	missing := false
	for _, name := range order {
		what, plural := whatWaits(types[name])
		verb := map[bool]string{true: "wait", false: "waits"}[plural]
		if c, ok := components.Get(name); ok && name != "" {
			missing = true
			adds = append(adds, name)
			cmds = append(cmds, "metarush components add "+name)
			if strings.EqualFold(what, c.Title) { // "Seeing what's on screen" is its component's name too
				says = append(says, what+" "+verb+": add it in Settings → Components ("+c.Size()+").")
				continue
			}
			says = append(says, what+" "+verb+" for "+componentName(c)+": add it in Settings → Components ("+c.Size()+").")
			continue
		}
		says = append(says, what+" "+verb+": no computer running MetaRush can do it right now, and this one doesn't take that work.")
	}
	hint := "Everything else is done. What waits stays queued, and MetaRush does it once that's added."
	if !missing {
		hint = "Everything else is done. What waits stays queued, and MetaRush does it once a computer that can is running it."
	}
	if others := couldElsewhere(ctx, a, stuck); len(others) > 0 {
		hint += " Or open MetaRush on " + andList(others) + ", which can do it."
	}
	code := apperr.NoWorker
	if missing {
		code = apperr.ToolMissing
	}
	e := apperr.New(code, "%s", strings.Join(says, " ")).WithHint(hint)
	details := map[string]any{"waiting": waiting}
	if len(adds) > 0 {
		details["components"] = adds
		details["command"] = strings.Join(cmds, " && ")
	}
	return e.WithDetails(details)
}

// couldElsewhere names the other computers of the workspace that run this
// work, though MetaRush isn't running there now.
func couldElsewhere(ctx context.Context, a *app.App, stuck []stuckJob) []string {
	var types []string
	for _, s := range stuck {
		if s.job.Installation == "" {
			types = append(types, s.job.Type)
		}
	}
	if len(types) == 0 {
		return nil
	}
	var ws []models.WorkerDevice
	if err := a.DB.Find(ctx, store.CWorkers, a.WorkspaceID(), bson.M{"jobTypes": bson.M{"$in": types}, "_id": bson.M{"$ne": a.Cfg.Installation.ID}}, &ws); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var names []string
	for _, w := range ws {
		if n := strings.TrimSpace(w.Name); n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// waitingFrom is the error a wait ended with when jobs it waited for wait
// for something no computer has (nil for any other error).
func waitingFrom(err error) *apperr.Error {
	ae := apperr.From(err)
	if ae == nil || (ae.Code != apperr.ToolMissing && ae.Code != apperr.NoWorker) {
		return nil
	}
	if d, ok := ae.Details.(map[string]any); ok {
		if _, ok := d["waiting"]; ok {
			return ae
		}
	}
	return nil
}
