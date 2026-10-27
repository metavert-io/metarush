package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/qa"
	"github.com/metavert-io/metarush/internal/secretpath"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
)

// The ship gate (D298, D299, docs/ship-gate.md): a version can't go out to a
// client — delivery, a timeline exported into a delivery folder — while the
// quality check found problems (errors) in what would go out, hasn't checked
// it or checked it before today's promises, or a sign-off (the project's or
// the house's) is still waiting. Warnings never hold. It holds until those
// are done, or until a person sends it out anyway with a note: people only,
// recorded on the version with who and when, and written into the delivery
// record, its notes file and the project's receipt. Auto, agents and the
// review desk never do. Whether the gate is on is the workspace's (owners;
// on for new workspaces, asked once in older ones) and a set of Content
// Settings' (owners), which wins for the projects using them. One function
// (ShipGate) answers the version page, `metarush deliver ready`, delivery and
// timeline exports, so they all say the same.

// ShipGateIn names a version.
type ShipGateIn struct {
	ProjectID string `json:"projectId" jsonschema:"description=Project ID or its exact name."`
	Revision  string `json:"revision,omitempty" jsonschema:"description=The version (rN or its ID); default the latest."`
	Branch    string `json:"branch,omitempty" jsonschema:"description=The cut (default main)."`
	// Quality: what would go out (final renders by default, like delivery).
	Quality string   `json:"quality,omitempty" jsonschema:"enum=,enum=final,enum=review"`
	Presets []string `json:"presets,omitempty" jsonschema:"description=Only these shapes (default: every shape rendered)."`
}

// GatePath is one way to clear a hold: an operation the dashboard runs in one
// click (Op and Input), the command for a terminal, a page to open.
type GatePath struct {
	Label   string         `json:"label"`
	Op      string         `json:"op,omitempty"`
	Input   map[string]any `json:"input,omitempty"`
	Command string         `json:"command,omitempty"`
	Link    string         `json:"link,omitempty"`
	// People: only a person can take it (approving, ticking a sign-off).
	People bool `json:"people,omitempty"`
}

// GateProblem is one problem the quality check found, with its fix.
type GateProblem struct {
	Message string `json:"message"`
	At      string `json:"at,omitempty" jsonschema:"description=Where in the video (0:12)."`
	Fix     string `json:"fix,omitempty"`
	// Ops is the change that fixes it, when there is one (a new version of
	// the cut: write them into a plan and check it, or Fix it on the version page).
	Ops []editspec.Op `json:"ops,omitempty"`
	// Promise: it is a promise made to the client that isn't kept (D300).
	Promise bool `json:"promise,omitempty"`
}

// GateHold is one thing holding a version back.
type GateHold struct {
	models.ShipHold
	// Overridable: a person may send it out anyway with a note (the quality
	// check and sign-offs; never the approval or a render that isn't there).
	Overridable bool `json:"overridable"`
	// Covered: a person already sent it out anyway past this (Override).
	Covered  bool          `json:"covered,omitempty"`
	Problems []GateProblem `json:"problems,omitempty"`
	Paths    []GatePath    `json:"paths"`
}

// GateRender is a render that would go out, and its check.
type GateRender struct {
	RenderID string `json:"renderId"`
	Preset   string `json:"preset"`
	Quality  string `json:"quality"`
	// Check is the quality check's state: passed, warnings, problems, none,
	// running, failed or old (checked before today's promises or description).
	Check    string `json:"check" jsonschema:"enum=passed,enum=warnings,enum=problems,enum=none,enum=running,enum=failed,enum=old"`
	ReportID string `json:"reportId,omitempty"`
	Errors   int    `json:"errors"`
	Warnings int    `json:"warnings"`
}

// Gate states (ShipGateOut.State).
const (
	GateReady      = "ready"       // nothing holds it
	GateHeld       = "held"        // something holds it
	GateSentAnyway = "sent-anyway" // everything holding it was overridden by a person
	GateReminders  = "reminders"   // the gate is off: what would hold it only reminds
)

// ShipGateOut says whether a version can go out, and what holds it.
type ShipGateOut struct {
	ProjectID  string `json:"projectId"`
	Project    string `json:"project"`
	RevisionID string `json:"revisionId"`
	Number     int    `json:"number"`
	Branch     string `json:"branch"`
	// On: the gate holds this project's versions; Why says whose choice that is.
	On  bool   `json:"on"`
	Why string `json:"why"`
	// Decided is false while nobody decided for an older workspace (owners
	// are asked once; it stays off until then).
	Decided bool `json:"decided"`
	// Ready: it can go out now (nothing holds it, or a person sent it out anyway).
	Ready    bool   `json:"ready"`
	State    string `json:"state" jsonschema:"enum=ready,enum=held,enum=sent-anyway,enum=reminders"`
	Headline string `json:"headline"`
	// Holds are everything that holds it (also what a person went ahead past, Covered).
	Holds   []GateHold   `json:"holds"`
	Renders []GateRender `json:"renders"`
	// Promises are the promises made to the client as the check of its first
	// render measured them (D300).
	Promises []models.QAPromise `json:"promises"`
	// Override is the latest note that covers what holds it now; SentAnyway
	// every note that covers something holding it now, each with its person.
	Override   *models.ShipOverride  `json:"override,omitempty"`
	SentAnyway []models.ShipOverride `json:"sentAnyway"`
	Overrides  []models.ShipOverride `json:"overrides"`
	// Unmeasured counts the promises the check couldn't measure here: never
	// a hold, but the headline says so.
	Unmeasured int `json:"unmeasured"`
	// OlderComputers counts the workspace's computers on an older MetaRush,
	// which may deliver without the gate; Warnings say it in words.
	OlderComputers int      `json:"olderComputers"`
	Warnings       []string `json:"warnings"`
	Summary        string   `json:"summary"`
	// Lines say it in plain sentences, one per hold (the CLI prints them).
	Lines []string `json:"lines"`
	// heard is the changed words the version plays, as the gate checked them
	// (delivery reuses them for its notes).
	heard []SaidHeard
}

// gateKinds are the holds the gate is about (a person may send it out past them).
var gateKinds = map[string]bool{models.HoldQAMissing: true, models.HoldQARunning: true, models.HoldQAFailed: true, models.HoldQAStale: true,
	models.HoldQAErrors: true, models.HoldSignoff: true}

// GateFor says whether the gate holds a project's versions: the Content
// Settings it uses when they say, else the workspace's choice (off while
// nobody decided).
func GateFor(ctx context.Context, a *app.App, projectID string) (on bool, why string, decided bool) {
	if projectID != "" {
		if g, r, err := content.Resolve(ctx, a, projectID); err == nil && r.Owner != "" {
			if rec, err := content.Record(ctx, a, r.Owner); err == nil && rec != nil && rec.Gate != nil {
				whose := "“" + g.Name(r.Owner) + "”'s settings"
				if r.Owner == projectID {
					whose = "This project's settings"
				}
				switch rec.Gate.Mode {
				case models.GateOn:
					return true, whose + " hold every version until the quality check and the sign-offs are done.", true
				case models.GateOff:
					return false, whose + " only remind about the quality check and sign-offs: nothing is held.", true
				}
			}
		}
	}
	ws := workspaceGate(ctx, a)
	switch {
	case ws == nil:
		return false, "Nobody has decided yet whether versions wait for the quality check and sign-offs, so they only remind (an owner is asked on the To-dos page).", false
	case ws.On:
		return true, "Versions wait for the quality check and the sign-offs before they go out (the workspace's choice).", true
	}
	return false, "The quality check and sign-offs only remind: an owner turned holding versions off.", true
}

func workspaceGate(ctx context.Context, a *app.App) *models.ShipGateSettings {
	var ws models.Workspace
	if err := a.DB.GetByID(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), &ws); err != nil {
		return nil
	}
	return ws.ShipGate
}

// gateRenders are the renders of a version that would go out: the newest of
// each shape (or of the shapes asked for) at that quality.
func gateRenders(ctx context.Context, a *app.App, rev *models.Revision, quality string, presets []string) ([]models.Render, []string, error) {
	var renders []models.Render
	if err := a.DB.Find(ctx, store.CRenders, a.WorkspaceID(), bson.M{"revisionId": rev.ID, "state": models.RenderAvailable, "quality": quality}, &renders,
		options.Find().SetSort(bson.D{{Key: "completedAt", Value: -1}})); err != nil {
		return nil, nil, err
	}
	byPreset := map[string]models.Render{}
	for _, r := range renders {
		if r.Output == nil || r.SpecHash != rev.SpecHash {
			continue
		}
		if _, ok := byPreset[r.Preset]; !ok {
			byPreset[r.Preset] = r
		}
	}
	want := presets
	if len(want) == 0 {
		for p := range byPreset {
			want = append(want, p)
		}
		sort.Strings(want)
	}
	var out []models.Render
	var missing []string
	for _, p := range want {
		if r, ok := byPreset[p]; ok {
			out = append(out, r)
		} else {
			missing = append(missing, p)
		}
	}
	return out, missing, nil
}

// promisesNow are the promises a project's videos make today (its own and the house's).
func promisesNow(ctx context.Context, a *app.App, p *models.Project) []models.ProjectRule {
	var out []models.ProjectRule
	for _, r := range content.RulesFor(ctx, a, p) {
		if r.Promise {
			out = append(out, r)
		}
	}
	return out
}

// staleCheck says why a report is older than today's promises or the
// version's description ("" it isn't).
func staleCheck(rep *models.QAReport, promises []models.ProjectRule, rev *models.Revision) string {
	measured := map[string]string{}
	disclosed := false
	for _, p := range rep.Promises {
		measured[p.RuleID] = p.Measure
		disclosed = disclosed || p.Measure == "disclosed"
	}
	for _, p := range promises {
		if m, ok := measured[p.ID]; !ok || m != p.Measure {
			return fmt.Sprintf("it was checked before the promise “%s”", p.Text)
		}
	}
	if disclosed && rep.NotesSHA != qa.NotesFingerprint(rev.DeliveryNotes) {
		return "the version's description changed since it was checked"
	}
	return ""
}

// qaJob is the newest quality check job of a render (nil none).
func qaJob(ctx context.Context, a *app.App, renderID string) *models.Job {
	var j models.Job
	err := a.DB.C(store.CJobs).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "type": jobs.TypeQA, "payload.renderId": renderID},
		options.FindOne().SetSort(bson.D{{Key: "createdAt", Value: -1}})).Decode(&j)
	if errors.Is(err, mongo.ErrNoDocuments) || err != nil {
		return nil
	}
	return &j
}

// ShipGate says whether a version can go out (renders: what would go out;
// missingPresets: shapes asked for that aren't rendered).
func ShipGate(ctx context.Context, a *app.App, p *models.Project, rev *models.Revision, quality string, presets []string) (*ShipGateOut, error) {
	if quality == "" {
		quality = "final"
	}
	renders, missing, err := gateRenders(ctx, a, rev, quality, presets)
	if err != nil {
		return nil, err
	}
	out := &ShipGateOut{ProjectID: p.ID, Project: p.Name, RevisionID: rev.ID, Number: rev.Number, Branch: orBranch(rev.Branch),
		Holds: []GateHold{}, Renders: []GateRender{}, Promises: []models.QAPromise{}, Overrides: rev.ShipOverrides, Lines: []string{}, SentAnyway: []models.ShipOverride{}, Warnings: []string{}}
	if out.Overrides == nil {
		out.Overrides = []models.ShipOverride{}
	}
	out.On, out.Why, out.Decided = GateFor(ctx, a, p.ID)
	version := fmt.Sprintf("Version %d", rev.Number)
	revLink := links.Revision(p.ID, rev.ID)
	what := map[string]string{"final": "final render", "review": "review copy"}[quality]

	// The approval (delivery's own rule: a draft goes out only when asked; never overridden here).
	switch rev.Approval.State {
	case models.ApprovalApproved:
	case models.ApprovalRejected:
		out.Holds = append(out.Holds, GateHold{ShipHold: models.ShipHold{Key: "approval:" + rev.ID, Kind: models.HoldApproval, Says: version + " needs changes, so it never goes out: make a new version."},
			Paths: []GatePath{{Label: "Open it", Link: revLink}}})
	default:
		out.Holds = append(out.Holds, GateHold{ShipHold: models.ShipHold{Key: "approval:" + rev.ID, Kind: models.HoldApproval, Says: version + " isn't approved yet."},
			Paths: []GatePath{{Label: "Approve it", Op: "revision.approve", Input: map[string]any{"id": rev.ID}, Command: "metarush revision approve " + rev.ID, People: true}}})
	}
	// Something to send.
	if len(renders) == 0 || len(missing) > 0 {
		shapes := "it"
		preset := ""
		if len(missing) > 0 && len(presets) > 0 {
			var ws []string
			for _, m := range missing {
				ws = append(ws, editspec.PresetLabel(m))
			}
			shapes = "its " + andList(ws) + " shape"
			preset = missing[0]
		} else if rev.Spec.Output.Preset != "" {
			preset = rev.Spec.Output.Preset
		}
		in := map[string]any{"revisionId": rev.ID, "quality": quality}
		cmd := "metarush render submit --revision " + rev.ID + " --quality " + quality + " --wait"
		if preset != "" {
			in["preset"] = preset
			cmd += " --preset " + preset
		}
		says := fmt.Sprintf("%s has no %s yet.", version, what)
		if len(renders) > 0 {
			says = fmt.Sprintf("%s has no %s in %s yet.", version, what, shapes)
		}
		out.Holds = append(out.Holds, GateHold{ShipHold: models.ShipHold{Key: "render:" + rev.ID + ":" + quality, Kind: models.HoldRender, Says: says},
			Paths: []GatePath{{Label: "Make the " + what, Op: "render.submit", Input: in, Command: cmd}}})
	}
	// The quality check of each render that would go out.
	promises := promisesNow(ctx, a, p)
	for _, rd := range renders {
		gr := GateRender{RenderID: rd.ID, Preset: rd.Preset, Quality: rd.Quality}
		shape := editspec.PresetLabel(rd.Preset)
		of := fmt.Sprintf("the %s %s", strings.ToLower(shape), what)
		if len(renders) == 1 {
			of = "its " + what
		}
		check := GatePath{Label: "Check it", Op: "qa.run", Input: map[string]any{"id": rd.ID}, Command: "metarush qa run " + rd.ID + " --wait"}
		renderLink := links.Render(p.ID, rd.ID)
		var rep *models.QAReport
		if rd.QA != nil {
			var r models.QAReport
			if a.DB.GetByID(ctx, store.CQAReports, a.WorkspaceID(), rd.QA.ReportID, &r) == nil {
				rep = &r
			}
		}
		if rep == nil {
			gr.Check = "none"
			hold := GateHold{ShipHold: models.ShipHold{Key: "qa-missing:" + rd.ID, Kind: models.HoldQAMissing, Says: fmt.Sprintf("The quality check hasn't looked at %s yet.", of)},
				Paths: []GatePath{check}}
			switch j := qaJob(ctx, a, rd.ID); {
			case j != nil && (j.State == models.JobQueued || j.State == models.JobRunning):
				gr.Check = "running"
				hold.ShipHold = models.ShipHold{Key: "qa-running:" + rd.ID, Kind: models.HoldQARunning, Says: fmt.Sprintf("The quality check of %s isn't finished yet.", of)}
				hold.Paths = []GatePath{{Label: "See how far it is", Link: renderLink, Command: "metarush qa run " + rd.ID + " --wait"}}
			case j != nil && j.State == models.JobFailed:
				gr.Check = "failed"
				hold.ShipHold = models.ShipHold{Key: "qa-failed:" + rd.ID + ":" + j.ID, Kind: models.HoldQAFailed, Says: fmt.Sprintf("The quality check of %s couldn't finish.", of)}
				check.Label, check.Input = "Check it again", map[string]any{"id": rd.ID, "force": true}
				check.Command = "metarush qa run " + rd.ID + " --force --wait"
				hold.Paths = []GatePath{check}
			}
			out.Holds = append(out.Holds, hold)
			out.Renders = append(out.Renders, gr)
			continue
		}
		gr.ReportID, gr.Errors, gr.Warnings = rep.ID, rep.Counts.Errors, rep.Counts.Warnings
		if len(out.Promises) == 0 && len(rep.Promises) > 0 {
			// Today's promises as this check measured them (one taken back since is left out).
			cur := map[string]bool{}
			for _, pr := range promises {
				cur[pr.ID] = true
			}
			for _, pr := range rep.Promises {
				if cur[pr.RuleID] {
					out.Promises = append(out.Promises, pr)
				}
			}
		}
		if why := staleCheck(rep, promises, rev); why != "" {
			gr.Check = "old"
			out.Holds = append(out.Holds, GateHold{ShipHold: models.ShipHold{Key: "qa-stale:" + rd.ID + ":" + rep.ID, Kind: models.HoldQAStale,
				Says: fmt.Sprintf("The quality check of %s is out of date: %s.", of, why)},
				Paths: []GatePath{{Label: "Check it again", Op: "qa.run", Input: map[string]any{"id": rd.ID, "force": true}, Command: "metarush qa run " + rd.ID + " --force --wait"}}})
			out.Renders = append(out.Renders, gr)
			continue
		}
		// A promise taken back since the check no longer holds the version.
		current := map[string]bool{}
		for _, pr := range promises {
			current[pr.ID] = true
		}
		gone := map[string]bool{}
		for _, pr := range rep.Promises {
			if !current[pr.RuleID] && pr.FindingID != "" {
				gone[pr.FindingID] = true
			}
		}
		errs := 0
		for _, f := range rep.Findings {
			if f.Severity == models.QAError && !gone[f.ID] {
				errs++
			}
		}
		gr.Errors = errs
		switch {
		case errs > 0:
			gr.Check = "problems"
			hold := GateHold{ShipHold: models.ShipHold{Key: "qa-errors:" + rep.ID, Kind: models.HoldQAErrors,
				Says: fmt.Sprintf("The quality check found %s in %s.", pluralWord(errs, "problem"), of)},
				Paths: []GatePath{
					{Label: "See the problems", Link: links.QA(p.ID, rd.ID, rep.ID), Command: "metarush qa show " + rep.ID},
					{Label: "Check it again", Op: "qa.run", Input: map[string]any{"id": rd.ID, "force": true}, Command: "metarush qa run " + rd.ID + " --force --wait"},
				}}
			for _, f := range rep.Findings {
				if f.Severity != models.QAError || gone[f.ID] {
					continue
				}
				gp := GateProblem{Message: f.Message, At: shortClock(f.From), Promise: f.Kind == "promise_broken"}
				if f.Fix != nil {
					gp.Fix, gp.Ops = f.Fix.Summary, f.Fix.Ops
				}
				hold.Problems = append(hold.Problems, gp)
			}
			out.Holds = append(out.Holds, hold)
		case rep.Counts.Warnings > 0 || rep.Counts.Errors > 0:
			gr.Check = "warnings"
		default:
			gr.Check = "passed"
		}
		out.Renders = append(out.Renders, gr)
	}
	// Sign-offs still waiting: the project's own and the house's (D166, D200).
	for _, s := range OpenSignoffs(ctx, a, p, rev.Branch) {
		says := fmt.Sprintf("Still waiting for the sign-off “%s”.", s.Text)
		if s.House {
			says = fmt.Sprintf("Still waiting for the house sign-off “%s” (from “%s”'s settings).", s.Text, s.HouseOf)
		}
		cmd := fmt.Sprintf("metarush cut signoffs done %s %q", p.ID, s.Text)
		if b := orBranch(rev.Branch); b != "main" {
			cmd += " --branch " + b
		}
		out.Holds = append(out.Holds, GateHold{ShipHold: models.ShipHold{Key: "signoff:" + s.ID + "@" + orBranch(rev.Branch), Kind: models.HoldSignoff, Says: says},
			Paths: []GatePath{{Label: "Tick it", Op: "cut.signoffs.done", Input: map[string]any{"projectId": p.ID, "branch": orBranch(rev.Branch), "signoff": s.ID}, Command: cmd, People: true}}})
	}
	// Changed words whose consent is no longer in force (D309): never sent out anyway.
	heard, err := saidHeardInRevision(ctx, a, rev)
	if err != nil {
		return nil, err
	}
	out.Holds = append(out.Holds, saidHolds(p, rev, heard)...)
	out.heard = heard
	// What a person already sent it out past.
	for i := range out.Holds {
		h := &out.Holds[i]
		h.Overridable = gateKinds[h.Kind]
		for _, o := range rev.ShipOverrides {
			if h.Overridable && o.Covers(h.Key) {
				h.Covered = true
			}
		}
	}
	consent := false
	for _, h := range out.Holds {
		consent = consent || h.Kind == models.HoldConsent
	}
	if out.On || consent { // an older build knows neither the gate nor changed words' consent (D309)
		olderWarning(ctx, a, &out.OlderComputers, &out.Warnings, consent)
	}
	decide(out, rev)
	return out, nil
}

// olderWarning counts the workspace's computers on an older MetaRush: a build
// from before the gate delivers without waiting (D298), and one from before
// changed words' consent was checked delivers a version that plays words
// whose consent no longer holds (D309; consent).
func olderWarning(ctx context.Context, a *app.App, n *int, warnings *[]string, consent bool) {
	v, err := VersionAbout(ctx, a)
	if err != nil || v.Older == 0 {
		return
	}
	*n = v.Older
	what := "versions without waiting for the quality check and sign-offs"
	if consent {
		what = "this version although it plays changed words whose consent no longer holds"
	}
	*warnings = append(*warnings, fmt.Sprintf("%s in this workspace %s an older MetaRush, which may deliver %s: update %s (Settings → General).",
		sentenceStart(pluralWord(v.Older, "computer")), map[bool]string{true: "runs", false: "run"}[v.Older == 1], what, map[bool]string{true: "it", false: "them"}[v.Older == 1]))
}

// decide works out the state and the words.
func decide(out *ShipGateOut, rev *models.Revision) {
	var blocking, open, covered []GateHold
	for _, h := range out.Holds {
		switch {
		case !h.Overridable:
			blocking = append(blocking, h)
		case h.Covered:
			covered = append(covered, h)
		default:
			open = append(open, h)
		}
	}
	for _, h := range out.Holds {
		if !h.Covered {
			out.Lines = append(out.Lines, h.Says)
		}
	}
	version := fmt.Sprintf("Version %d", out.Number)
	// Every note that covers something holding it now (D299), oldest first.
	if len(covered) > 0 {
		keys := map[string]bool{}
		for _, h := range covered {
			keys[h.Key] = true
		}
		for _, o := range rev.ShipOverrides {
			for _, h := range o.Holds {
				if keys[h.Key] {
					out.SentAnyway = append(out.SentAnyway, o)
					break
				}
			}
		}
		if n := len(out.SentAnyway); n > 0 {
			o := out.SentAnyway[n-1]
			out.Override = &o
		}
	}
	for _, p := range out.Promises {
		if p.Status == models.PromiseUnmeasured {
			out.Unmeasured++
		}
	}
	switch {
	case !out.On:
		out.Ready = len(blocking) == 0
		out.State = GateReady
		out.Headline = "Ready to deliver"
		if len(open)+len(covered) > 0 {
			out.State = GateReminders
		}
		if !out.Ready {
			out.State, out.Headline = GateHeld, "Not ready yet"
		}
	case len(blocking) == 0 && len(open) == 0 && len(covered) == 0:
		out.Ready, out.State, out.Headline = true, GateReady, "Ready to deliver"
	case len(blocking) == 0 && len(open) == 0:
		out.Ready, out.State = true, GateSentAnyway
		var names []string
		for _, o := range out.SentAnyway {
			if n := actorName(o.By); !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
		out.Headline = "Ready to deliver: sent out anyway by " + andList(names)
	default:
		out.State = GateHeld
		n := len(blocking) + len(open)
		out.Headline = "Held by " + pluralWord(n, "thing")
		if n == 1 {
			out.Headline = "Held by 1 thing"
		}
	}
	if out.Ready && out.Unmeasured > 0 {
		// A promise nobody could check isn't a hold, but "ready" alone would say it was checked.
		but := fmt.Sprintf("but %s couldn't be checked here", pluralWord(out.Unmeasured, "promise"))
		if out.State == GateSentAnyway {
			out.Headline += ", " + but
		} else {
			out.Headline = "Ready, " + but
		}
	}
	switch out.State {
	case GateReady:
		out.Summary = version + " is ready to deliver: the quality check and the sign-offs are done."
		if len(out.Renders) > 0 && out.Renders[0].Check == "warnings" {
			out.Summary = version + " is ready to deliver; the quality check found things worth a look, which never hold it."
		}
	case GateReminders:
		out.Summary = version + " can go out: " + lowerFirst(strings.TrimSuffix(out.Why, ".")) + ". " + andList(linesOf(open, covered)) + "."
		out.Summary = strings.ReplaceAll(out.Summary, "..", ".")
	case GateSentAnyway:
		var said []string
		for _, o := range out.SentAnyway {
			said = append(said, fmt.Sprintf("%s on %s (“%s”)", actorName(o.By), o.At.UTC().Format("2 Jan 2006"), o.Note))
		}
		out.Summary = fmt.Sprintf("%s can go out: sent out anyway by %s.", version, strings.Join(said, "; "))
	default:
		var says []string
		for _, h := range append(blocking, open...) {
			says = append(says, lowerFirst(strings.TrimSuffix(h.Says, ".")))
		}
		out.Summary = fmt.Sprintf("%s can't go out yet: %s.", version, strings.Join(says, "; "))
	}
}

func linesOf(lists ...[]GateHold) []string {
	var out []string
	for _, l := range lists {
		for _, h := range l {
			out = append(out, lowerFirst(strings.TrimSuffix(h.Says, ".")))
		}
	}
	if len(out) > 0 {
		out[0] = sentenceStart(out[0])
	}
	return out
}

func orBranch(b string) string {
	if b == "" {
		return "main"
	}
	return b
}

// actorName is who did something, for people.
func actorName(a models.Actor) string {
	if a.UserName != "" {
		return a.UserName
	}
	return "a person"
}

// shortClock is a time in the video for people (0:12).
func shortClock(sec float64) string {
	s := int(sec + 0.5)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// heldError refuses to send out versions the gate holds, with what holds them.
func heldError(gates []*ShipGateOut, what string) *apperr.Error {
	var parts []string
	var details []map[string]any
	first := gates[0]
	consent := false
	for _, g := range gates {
		var says []string
		for _, h := range g.Holds {
			if (h.Overridable && !h.Covered) || h.Kind == models.HoldConsent {
				says = append(says, lowerFirst(strings.TrimSuffix(h.Says, ".")))
			}
			consent = consent || h.Kind == models.HoldConsent
		}
		parts = append(parts, fmt.Sprintf("version %d of “%s” is held: %s", g.Number, g.Project, strings.Join(says, "; ")))
		details = append(details, map[string]any{"revisionId": g.RevisionID, "project": g.ProjectID, "holds": g.Holds})
	}
	msg := sentenceStart(strings.Join(parts, ". ")) + "."
	cmd := fmt.Sprintf("metarush deliver ready %s --version r%d", first.ProjectID, first.Number)
	if consent {
		return apperr.New(apperr.Held, "%s", msg).
			WithHint("Take the changed words out of the cut and make a new version: nobody can send it out anyway while it plays words whose consent is no longer in force.").
			WithDetails(map[string]any{"held": details, "what": what}).WithCommand(cmd)
	}
	return apperr.New(apperr.Held, "%s", msg).
		WithHint("Fix what holds it (the version's page says how, one step each), or a person sends it out anyway with a note.").
		WithDetails(map[string]any{"held": details, "what": what, "anyway": fmt.Sprintf("metarush deliver override %s --version r%d --note \"…\"", first.ProjectID, first.Number)}).WithCommand(cmd)
}

// letsOut reports whether the gate lets a version out (off, nothing holds
// it, or a person sent it out anyway past everything that holds it); never
// while it plays changed words whose consent is no longer in force.
func (g *ShipGateOut) letsOut() bool {
	for _, h := range g.Holds {
		if h.Kind == models.HoldConsent {
			return false // with the gate off too (D309)
		}
	}
	if !g.On {
		return true
	}
	for _, h := range g.Holds {
		if h.Overridable && !h.Covered {
			return false
		}
	}
	return true
}

// gateRecord is the gate in a delivered video's record (the .json next to it).
type DeliveryGate struct {
	On  bool   `json:"on"`
	Why string `json:"why"`
	// Held is what held it (or would have, with the gate off).
	Held []models.ShipHold `json:"held"`
	// SentAnyway are the notes that let it out past them, each with its
	// person and the holds it covered (D299).
	SentAnyway []models.ShipOverride `json:"sentAnyway,omitempty"`
	// Promises are the promises made to the client, as measured.
	Promises []models.QAPromise `json:"promises,omitempty"`
}

func (g *ShipGateOut) record() *DeliveryGate {
	d := &DeliveryGate{On: g.On, Why: g.Why, Held: []models.ShipHold{}, Promises: g.Promises}
	for _, h := range g.Holds {
		if h.Overridable {
			d.Held = append(d.Held, h.ShipHold)
		}
	}
	if g.On && len(d.Held) > 0 {
		d.SentAnyway = g.SentAnyway
	}
	return d
}

// sentAnywayMarkdown is the section of a delivered video's notes file that
// says it went out although something held it (D299): every note, each with
// its person, when, and what it covered.
func sentAnywayMarkdown(d *DeliveryGate) string {
	if d == nil || len(d.SentAnyway) == 0 {
		return ""
	}
	held := map[string]string{}
	for _, h := range d.Held {
		held[h.Key] = h.Says
	}
	var b strings.Builder
	b.WriteString("## Sent out anyway\n\n")
	for i, o := range d.SentAnyway {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s sent this version out on %s although something held it:\n\n", actorName(o.By), o.At.UTC().Format("2 Jan 2006, 15:04 UTC"))
		for _, h := range o.Holds {
			if says, ok := held[h.Key]; ok {
				fmt.Fprintf(&b, "- %s\n", says)
			}
		}
		fmt.Fprintf(&b, "\nTheir note: “%s”\n", o.Note)
	}
	return b.String()
}

// TimelineGate is the gate for a timeline exported into a delivery folder:
// the quality check of the version's final renders, else its review copies;
// a version never rendered hasn't been checked.
func TimelineGate(ctx context.Context, a *app.App, p *models.Project, rev *models.Revision) (*ShipGateOut, error) {
	g, err := ShipGate(ctx, a, p, rev, "final", nil)
	if err != nil || len(g.Renders) > 0 {
		return g, err
	}
	if r, err := ShipGate(ctx, a, p, rev, "review", nil); err != nil || len(r.Renders) > 0 {
		return r, err
	}
	key := "qa-missing:" + rev.ID
	h := GateHold{ShipHold: models.ShipHold{Key: key, Kind: models.HoldQAMissing, Says: fmt.Sprintf("The quality check hasn't looked at version %d: it has no render yet.", rev.Number)},
		Overridable: true, Paths: []GatePath{{Label: "Make a review copy", Op: "render.submit", Input: map[string]any{"revisionId": rev.ID, "quality": "review"},
			Command: "metarush render submit --revision " + rev.ID + " --quality review --wait"}}}
	for _, o := range rev.ShipOverrides {
		h.Covered = h.Covered || o.Covers(key)
	}
	holds := []GateHold{h}
	for _, x := range g.Holds {
		if x.Kind != models.HoldRender {
			holds = append(holds, x)
		}
	}
	g.Holds, g.Lines = holds, []string{}
	decide(g, rev)
	return g, nil
}

// loosening refuses a step that would let versions out past the gate when
// it isn't a person's (D299): ticking or removing a sign-off while the gate
// holds the project, and (always) taking back a promise made to the client.
func loosening(ctx context.Context, a *app.App, projectID, what, command string, always bool) error {
	if callerOf(ctx).Human {
		return nil
	}
	if on, _, _ := GateFor(ctx, a, projectID); !on && !always {
		return nil
	}
	return apperr.New(apperr.HumanOnly, "Only a person can %s: versions wait for it before they go out.", what).
		WithHint("Do it yourself in the dashboard, or in your own terminal.").WithCommand(command)
}

// settingsHolds is what a set of Content Settings holds versions to (D298,
// D300): whether its gate is on, its promises and its house sign-offs.
type settingsHolds struct {
	on                 bool
	promises, signoffs []string
}

func holdsOfSettings(ctx context.Context, a *app.App, owner string) settingsHolds {
	var h settingsHolds
	ws := workspaceGate(ctx, a)
	h.on = ws != nil && ws.On
	if owner == "" {
		return h
	}
	rec, err := content.Record(ctx, a, owner)
	if err != nil || rec == nil {
		return h
	}
	if rec.Gate != nil {
		h.on = rec.Gate.Mode == models.GateOn
	}
	for _, r := range rec.HouseRules {
		if r.Promise {
			h.promises = append(h.promises, r.Text)
		}
	}
	for _, s := range rec.Signoffs {
		h.signoffs = append(h.signoffs, s.Text)
	}
	return h
}

// gateSwitch refuses, unless a person who is an owner does it, a change of
// which Content Settings projects use that drops a gate that's on, promises
// or house sign-offs (D299; the way What may leave's switches are, D289).
func gateSwitch(ctx context.Context, a *app.App, projectName, fromOwner, toOwner string) error {
	from, to := holdsOfSettings(ctx, a, fromOwner), holdsOfSettings(ctx, a, toOwner)
	has := func(list []string, w string) bool {
		for _, x := range list {
			if ruleKey(x) == ruleKey(w) {
				return true
			}
		}
		return false
	}
	var drops []string
	if from.on && !to.on {
		drops = append(drops, "holding versions back for the quality check and sign-offs")
	}
	for _, p := range from.promises {
		if !has(to.promises, p) {
			drops = append(drops, "the promise “"+p+"”")
		}
	}
	for _, s := range from.signoffs {
		if !has(to.signoffs, s) {
			drops = append(drops, "the house sign-off “"+s+"”")
		}
	}
	if len(drops) == 0 {
		return nil
	}
	msg := fmt.Sprintf("Only a person who is an owner can move %s to settings without %s.", projectName, joinPlainAnd(drops))
	c := callerOf(ctx)
	if !c.Human || a.Actor.Via == models.ViaAssistant || a.Actor.Via == models.ViaAssistantAuto || a.Actor.Via == "mcp" || a.ProposeOnly {
		return apperr.New(apperr.HumanOnly, "%s", msg).
			WithHint("An owner does it in the dashboard or in their own terminal; or copy the settings it uses now instead, which keeps all of that.").
			WithDetails(map[string]any{"reason": "gate_switch", "drops": drops})
	}
	if c.ownerOK(ctx) {
		return nil
	}
	e := apperr.From(RequireOwner(ctx, a, "content.follow"))
	e.Message = msg
	return e
}

// ---- the setting ----

// GateSettingsIn names a project (whose Content Settings may say) or nothing.
type GateSettingsIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project: also what its Content Settings say."`
}

// GateSettingsOut is the gate's setting.
type GateSettingsOut struct {
	// Workspace is the workspace's choice (nil: nobody decided yet).
	Workspace *models.ShipGateSettings `json:"workspace,omitempty"`
	// Settings is what the project's Content Settings say (nil: the workspace's choice), Of whose.
	Settings *models.GateSetting `json:"settings,omitempty"`
	Of       string              `json:"of,omitempty"`
	// On: the project's (or, without one, the workspace's) versions wait.
	On      bool   `json:"on"`
	Decided bool   `json:"decided"`
	Summary string `json:"summary"`
	// OlderComputers count the computers on an older MetaRush, which may
	// deliver without the gate; Warnings say it (only while it is on).
	OlderComputers int      `json:"olderComputers"`
	Warnings       []string `json:"warnings"`
}

// GateSetIn turns the workspace's gate on or off (owners).
type GateSetIn struct {
	On bool `json:"on" jsonschema:"description=true: versions wait for the quality check and sign-offs before they go out; false: they only remind."`
}

// ContentGateSetIn sets a Content Settings' word on the gate (owners).
type ContentGateSetIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project with its own settings (default: the default's)."`
	Mode      string `json:"mode" jsonschema:"enum=on,enum=off,enum=workspace,description=on: their videos always wait; off: they only remind; workspace: the workspace's choice."`
}

func gateSettingsOut(ctx context.Context, a *app.App, projectID string) (*GateSettingsOut, error) {
	out := &GateSettingsOut{Workspace: workspaceGate(ctx, a)}
	if projectID != "" {
		g, r, err := content.Resolve(ctx, a, projectID)
		if err != nil {
			return nil, err
		}
		if r.Owner != "" {
			if rec, err := content.Record(ctx, a, r.Owner); err == nil && rec != nil {
				out.Settings = rec.Gate
			}
			out.Of = g.Name(r.Owner)
		}
	}
	var why string
	out.On, why, out.Decided = GateFor(ctx, a, projectID)
	out.Summary = why
	out.Warnings = []string{}
	if out.On || (projectID == "" && out.Workspace != nil && out.Workspace.On) {
		olderWarning(ctx, a, &out.OlderComputers, &out.Warnings, false)
	}
	return out, nil
}

// shipGateKey is the one-time question's group key.
const shipGateKey = "ship-gate"

// AskAboutShipGate asks the owners of an older workspace, once, whether
// versions should wait for the quality check and sign-offs (D298): nothing
// changes until one answers. A workspace that decided is never asked.
func AskAboutShipGate(ctx context.Context, a *app.App) (bool, error) {
	if workspaceGate(ctx, a) != nil {
		return false, nil
	}
	return todos.Upsert(ctx, a, models.Todo{Kind: models.TodoShipGate, Scope: "workspace", GroupKey: shipGateKey, HumanOnly: true, Priority: 2,
		Question: "Should versions wait for the quality check and their sign-offs before they go out to a client?",
		Detail: "With this on, delivering a version (and exporting a timeline into a delivery folder) waits while the quality check found problems, hasn't checked the video, " +
			"or a sign-off is still waiting; a person can still send it out anyway with a note, which is kept and shows on the receipt. Things worth checking never hold it. " +
			"Today they only remind. New workspaces start with it on; an owner decides here.",
		Context: models.TodoContext{Link: "/settings/general#ship-gate"},
		Suggestions: []models.TodoSuggestion{
			{Label: "Hold versions back", Value: "on"},
			{Label: "Just remind me", Value: "off"},
		}})
}

func setWorkspaceGate(ctx context.Context, a *app.App, on bool) error {
	if !on && !callerOf(ctx).Human {
		return apperr.New(apperr.HumanOnly, "Only a person can stop versions waiting for the quality check and sign-offs.").
			WithHint("Turn it off yourself in Settings → General, or in your own terminal.").WithCommand("metarush deliver gate off")
	}
	set := models.ShipGateSettings{On: on, UpdatedAt: time.Now().UTC(), UpdatedBy: a.Actor}
	if err := a.DB.Update(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), bson.M{"$set": bson.M{"shipGate": set}}); err != nil {
		return err
	}
	_, _ = todos.Close(ctx, a, shipGateKey, models.ResolvedElsewhere, map[bool]string{true: "on", false: "off"}[on])
	a.Audit(ctx, "delivery.gate.set", "workspace", a.WorkspaceID(), "", map[string]any{"on": on})
	return nil
}

// ---- sending out anyway ----

// ShipOverrideIn sends a version out anyway (people only).
type ShipOverrideIn struct {
	ShipGateIn
	Note string `json:"note" jsonschema:"minLength=3,maxLength=500,description=Why it goes out anyway, in the person's words (kept, and shown on the receipt and in the delivery's notes)."`
	// Holds are the keys of the holds the person saw (delivery.ready's
	// holds[].key): the note covers exactly those, and anything else holding
	// it now refuses it (look again).
	Holds []string `json:"holds" jsonschema:"minItems=1,maxItems=100,description=The keys of the holds the person saw and sends it out past (delivery.ready's holds[].key)."`
}

func overrideVersion(ctx context.Context, a *app.App, in *ShipOverrideIn) (*ShipGateOut, error) {
	note := strings.TrimSpace(cleanText(in.Note))
	if len([]rune(note)) < 3 {
		return nil, invalid("Say in a few words why it goes out anyway: the note is kept with the version and shows on the receipt.")
	}
	if len([]rune(note)) > 500 {
		return nil, invalid("Say it in at most 500 letters.")
	}
	p, err := resolveProject(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	rev, err := ResolveRevision(ctx, a, p, in.Revision, in.Branch)
	if err != nil {
		return nil, err
	}
	if rev == nil {
		return nil, apperr.New(apperr.NotFound, "%s has no version yet.", p.Name)
	}
	gateOf := func() (*ShipGateOut, error) {
		g, err := ShipGate(ctx, a, p, rev, in.Quality, in.Presets)
		if err == nil && len(g.Renders) == 0 && in.Quality == "" && len(in.Presets) == 0 {
			return TimelineGate(ctx, a, p, rev) // nothing rendered: what a timeline export would need
		}
		return g, err
	}
	g, err := gateOf()
	if err != nil {
		return nil, err
	}
	var holds []models.ShipHold
	for _, h := range g.Holds {
		if h.Overridable && !h.Covered {
			holds = append(holds, h.ShipHold)
		}
	}
	for _, h := range g.Holds {
		if h.Kind == models.HoldConsent { // D309: the words come out first
			return nil, apperr.New(apperr.ConsentRequired, "Version %d can't be sent out anyway: %s", rev.Number, lowerFirst(h.Says)).
				WithHint("Take the changed words out of the cut and make a new version.")
		}
	}
	if !g.On {
		return nil, apperr.New(apperr.Conflict, "Nothing holds version %d: %s", rev.Number, lowerFirst(g.Why)).WithHint("Deliver it as it is.")
	}
	if len(holds) == 0 {
		why := "nothing holds it"
		if g.Override != nil {
			why = "it was sent out anyway already, past everything that holds it"
		}
		return nil, apperr.New(apperr.Conflict, "Version %d needs no note: %s.", rev.Number, why).WithHint("Deliver it as it is.")
	}
	// The note covers what the person saw, and only that (D299).
	if len(in.Holds) == 0 {
		return nil, invalid("Say which holds you send it out past (their keys from what holds it now), so the note covers only what you saw.")
	}
	saw := map[string]bool{}
	for _, k := range in.Holds {
		saw[strings.TrimSpace(k)] = true
	}
	var appeared []string
	now := map[string]bool{}
	for _, h := range holds {
		now[h.Key] = true
		if !saw[h.Key] {
			appeared = append(appeared, lowerFirst(strings.TrimSuffix(h.Says, ".")))
		}
	}
	gone := 0
	for k := range saw {
		if !now[k] {
			gone++
		}
	}
	if len(appeared) > 0 || gone > 0 {
		msg := fmt.Sprintf("What holds version %d changed since you looked", rev.Number)
		if len(appeared) > 0 {
			msg += ": " + strings.Join(appeared, "; ")
		}
		keys := make([]string, 0, len(holds))
		for _, h := range holds {
			keys = append(keys, h.Key)
		}
		return nil, apperr.New(apperr.Conflict, "%s.", msg).WithHint("Look at what holds it now, then send it out anyway past that, if you still mean to.").
			WithDetails(map[string]any{"holds": keys}).WithCommand(fmt.Sprintf("metarush deliver ready %s --version r%d", p.ID, rev.Number))
	}
	o := models.ShipOverride{ID: ids.New(ids.ShipOverride), By: a.Actor, At: time.Now().UTC(), Note: note, Holds: holds}
	if _, err := a.DB.C(store.CRevisions).UpdateOne(ctx, bson.M{"_id": rev.ID, "workspaceId": a.WorkspaceID()},
		bson.M{"$push": bson.M{"shipOverrides": o}}); err != nil { // append-only: every note is kept (D299)
		return nil, err
	}
	var kinds []string
	for _, h := range holds {
		if !slices.Contains(kinds, h.Kind) {
			kinds = append(kinds, h.Kind)
		}
	}
	a.Audit(ctx, "delivery.override", "revision", rev.ID, p.ID, map[string]any{"override": o.ID, "holds": len(holds), "kinds": kinds})
	rev.ShipOverrides = append(rev.ShipOverrides, o)
	g, err = gateOf()
	if err != nil {
		return nil, err
	}
	g.Summary = fmt.Sprintf("Version %d can go out: you sent it out anyway past %s. Your note is kept with it and shows on the receipt and in what delivery writes.", rev.Number, pluralWord(len(holds), "thing"))
	return g, nil
}

// recordSentOut appends that a version went out past what held it (D299):
// the receipt says "went out" for the notes it names. It tries twice and
// returns the error (callers say so; the delivered files carry the notes).
func recordSentOut(ctx context.Context, a *app.App, revisionID string, notes []models.ShipOverride, what, folder string) error {
	sent := models.ShipSent{At: time.Now().UTC(), By: a.Actor, What: what, Folder: folder}
	for _, o := range notes {
		sent.Overrides = append(sent.Overrides, o.ID)
	}
	var err error
	for try := 0; try < 2; try++ {
		var res *mongo.UpdateResult
		res, err = a.DB.C(store.CRevisions).UpdateOne(ctx, bson.M{"_id": revisionID, "workspaceId": a.WorkspaceID()}, bson.M{"$push": bson.M{"sentOut": sent}})
		if err == nil && res.MatchedCount == 0 {
			err = fmt.Errorf("version %s not found", revisionID)
		}
		if err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return err
}

// sentOutNote says, for people, that a version went out anyway but the
// version couldn't be marked so (the receipt says "allowed" until fixed).
const sentOutNote = "MetaRush couldn't note on version %d that it went out anyway, so its receipt still says it was only allowed to; the files next to the video say who sent it and why."

// ---- timeline exports into a delivery folder ----

// IsDeliveryFolder reports a folder that holds delivered videos' records
// (the .json delivery writes next to each video): a client's delivery folder.
func IsDeliveryFolder(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".json") {
			continue
		}
		if n++; n > 200 {
			break
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		head := make([]byte, 512)
		k, _ := f.Read(head)
		f.Close()
		if strings.Contains(string(head[:k]), `"metarush.delivery/1"`) {
			return true
		}
	}
	return false
}

// deliveryFoldersFile is where a computer remembers the folders delivery
// wrote into (D298): a timeline into any of them, or a folder inside one,
// waits like a delivery. It lives in the home's private state (0700 folder,
// 0600 file, never in a sync folder or a committed repository); deleting it
// only makes the gate forget those folders.
func deliveryFoldersFile(a *app.App) string {
	return filepath.Join(a.Cfg.Home(), "state", "private", "delivery-folders.json")
}

func rememberedFolders(a *app.App) []string {
	var out []string
	if b, err := os.ReadFile(deliveryFoldersFile(a)); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

// notClientFolder is a folder that is never a client's delivery folder: the
// home folder, the top of a disk or a volume, a sync folder's root.
func notClientFolder(d string) bool {
	d = filepath.Clean(d)
	home, _ := os.UserHomeDir()
	parent := filepath.Dir(d)
	if d == home || parent == d || parent == "/Volumes" || parent == "/media" || parent == "/mnt" {
		return true
	}
	if v := secretpath.Inspect(d); v.Kind == secretpath.KindSync && filepath.Clean(v.Root) == d {
		return true
	}
	return false
}

// RememberDeliveryFolder notes on this computer that delivery wrote into dir
// (never the home folder, a volume or a sync folder's root).
func RememberDeliveryFolder(a *app.App, dir string) error {
	dir = filepath.Clean(dir)
	if notClientFolder(dir) {
		return nil
	}
	list := rememberedFolders(a)
	if slices.Contains(list, dir) {
		return nil
	}
	list = append(list, dir)
	if len(list) > 500 {
		list = list[len(list)-500:]
	}
	f := deliveryFoldersFile(a)
	if err := secretpath.Check(f, "the list of client delivery folders", false); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(list, "", "  ")
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Dir(f), 0o700)
	tmp := f + ".part"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f)
}

// ClientFolder reports a folder that is, or is inside, a client's delivery
// folder: one delivery wrote into on this computer, or one holding a delivered
// video's record, looking up to three folders up (stopping at the home
// folder, a volume or a sync folder's root, which are never a client's).
func ClientFolder(a *app.App, dir string) bool {
	dir = filepath.Clean(dir)
	for _, d := range rememberedFolders(a) {
		if dir == d || strings.HasPrefix(dir, d+string(filepath.Separator)) {
			return true
		}
	}
	for i, d := 0, dir; i < 4; i++ {
		if notClientFolder(d) {
			break
		}
		if IsDeliveryFolder(d) {
			return true
		}
		d = filepath.Dir(d)
	}
	return false
}

func init() {
	Def("delivery.ready", "Whether a version can go out to a client: Ready to deliver, or what holds it (the quality check's problems, a check that is missing or out of date, sign-offs still waiting, the approval, a render) with a one-step way to clear each, and the promises made to the client as measured", Flags{},
		func(ctx context.Context, a *app.App, in *ShipGateIn) (*ShipGateOut, error) {
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			rev, err := ResolveRevision(ctx, a, p, in.Revision, in.Branch)
			if err != nil {
				return nil, err
			}
			if rev == nil {
				return nil, apperr.New(apperr.NotFound, "%s has no version yet, so there's nothing to deliver.", p.Name)
			}
			if in.Quality != "" && in.Quality != "final" && in.Quality != "review" {
				return nil, invalid("Ask about final renders or review copies; “%s” isn't one of them.", in.Quality)
			}
			return ShipGate(ctx, a, p, rev, in.Quality, in.Presets)
		})
	Def("delivery.override", "Send a version out anyway past what holds it (the quality check, sign-offs), with a note: people only; the note is kept with who and when, and shows on the receipt and in what delivery writes. Never the approval", Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *ShipOverrideIn) (*ShipGateOut, error) {
			return overrideVersion(ctx, a, in)
		})
	Def("delivery.gate", "Whether versions wait for the quality check and their sign-offs before they go out: the workspace's choice and what a project's Content Settings say", Flags{},
		func(ctx context.Context, a *app.App, in *GateSettingsIn) (*GateSettingsOut, error) {
			pid := ""
			if strings.TrimSpace(in.ProjectID) != "" {
				p, err := resolveProject(ctx, a, in.ProjectID)
				if err != nil {
					return nil, err
				}
				pid = p.ID
			}
			return gateSettingsOut(ctx, a, pid)
		})
	Def("delivery.gate.set", "Owners: make versions wait for the quality check and their sign-offs before they go out (on), or only remind (off; people only)", Flags{Mutating: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, in *GateSetIn) (*GateSettingsOut, error) {
			if err := setWorkspaceGate(ctx, a, in.On); err != nil {
				return nil, err
			}
			return gateSettingsOut(ctx, a, "")
		})
	Def("content.gate.set", "Owners: say for a project's own Content Settings whether its videos wait for the quality check and sign-offs (on), only remind (off; people only) or follow the workspace", Flags{Mutating: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, in *ContentGateSetIn) (*GateSettingsOut, error) {
			pid, err := projectRefID(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			t, g, err := content.EditTarget(ctx, a, pid)
			if err != nil {
				return nil, err
			}
			on, _, _ := GateFor(ctx, a, t.ProjectID)
			upd := bson.M{}
			switch in.Mode {
			case models.GateOn:
				upd["$set"] = bson.M{"gate": models.GateSetting{Mode: models.GateOn, UpdatedAt: time.Now().UTC(), UpdatedBy: a.Actor}}
			case models.GateOff:
				upd["$set"] = bson.M{"gate": models.GateSetting{Mode: models.GateOff, UpdatedAt: time.Now().UTC(), UpdatedBy: a.Actor}}
			case "workspace":
				upd["$unset"] = bson.M{"gate": ""}
			default:
				return nil, invalid("Say on, off or workspace.")
			}
			if in.Mode != models.GateOn && on && !callerOf(ctx).Human {
				return nil, apperr.New(apperr.HumanOnly, "Only a person can stop these videos waiting for the quality check and sign-offs.").
					WithHint("Change it yourself in the project's Settings tab (Before it goes out), or in your own terminal.")
			}
			if err := content.Update(ctx, a, t.ProjectID, upd); err != nil {
				return nil, err
			}
			a.Audit(ctx, "content.gate.set", "project", t.ProjectID, t.ProjectID, map[string]any{"mode": in.Mode})
			out, err := gateSettingsOut(ctx, a, t.ProjectID)
			if err != nil {
				return nil, err
			}
			if note := t.Note(g); note != "" {
				out.Summary += " " + note
			}
			return out, nil
		})
}

// First-party abilities (D269): how the menu and find show what these
// operations do.
func init() {
	Offer("delivery.ready", Offering{ID: "ready-to-deliver", Group: "deliver", Title: "Ready to deliver?",
		Does:  "Says whether a version can go out to a client, or what holds it — the quality check, promises not kept, sign-offs still waiting — with a step to clear each.",
		Words: []string{"held", "can it go out", "promises", "ship"}})
}
