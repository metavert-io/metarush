package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// Narration and the sound mix in the panel. Claude reads them with read-only
// commands; every change becomes a card whose plan (or generation input) is
// built here from the command, priced before anything is spent, and put in the
// cut as a new version when the person confirms. API keys, voice copies and
// microphone recordings are never done for the person, not even as cards.

// neverForAssistant are commands only the person runs, in their own Settings
// or terminal: no card is made (Claude only sees the refusal).
var neverForAssistant = map[string]string{
	"providers set":    "API keys are added by the person in Settings → Integrations.",
	"providers remove": "API keys are removed by the person in Settings → Integrations.",
	"providers test":   "API keys are checked by the person in Settings → Integrations.",
	"voiceover clone":  "A copy of a voice needs the consent of the person whose voice it is, given by them.",
	"voiceover record": "Recording uses the person's microphone; they start it themselves.",
}

func (m *Manager) describeVoiceover(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	if why, never := neverForAssistant[call.Path]; never {
		incomplete(a, "", why)
		return
	}
	if call.Path == "providers settings" {
		m.describeVoiceSettings(ctx, call, a)
		return
	}
	r := m.revisionFor(ctx, call)
	if r == nil {
		incomplete(a, "Change the narration", "Claude did not name the cut (--revision).")
		return
	}
	arg := first(call.Args)
	switch call.Path {
	case "voiceover script set":
		raw, why := m.proposalFile(c, call.Flags["file"], "script")
		if why != "" {
			incomplete(a, "Set the narration", why)
			return
		}
		v, err := voiceover.DecodeScript(raw)
		if err != nil {
			incomplete(a, "Set the narration", apperr.From(err).Message)
			return
		}
		m.planCard(ctx, a, voiceover.ScriptSetPlan(r, v), "Set the narration: "+pluralize(len(v.Beats), "line"), firstWords(v.Beats)...)
	case "voiceover script import":
		raw, why := m.proposalFile(c, arg, "script")
		if why != "" {
			incomplete(a, "Write the narration", why)
			return
		}
		forClip := clip(call.Flags["clip"], 64)
		out, err := callOp[voiceover.ParseOut](ctx, m, "voiceover.parse", voiceover.ParseIn{RevisionID: r.ID, Text: string(raw), Append: hasBool(call, "append"), Clip: forClip})
		if err != nil || out.Result == nil {
			incomplete(a, "Write the narration", parseProblem(out, err))
			return
		}
		title := "Write the narration: " + pluralize(len(out.Beats), "line")
		switch {
		case forClip != "":
			title = "Write the narration for " + quoted(clipLabel(r, forClip)) + ": " + pluralize(len(out.Beats), "line")
		case hasBool(call, "append"):
			title = "Add " + pluralize(len(out.Beats), "narration line")
		}
		m.planCard(ctx, a, out.Result.Plan, title, firstWords(out.Beats)...)
	case "voiceover beat add":
		if _, ok := call.Flags["to"]; ok {
			m.lineCard(ctx, call, r, a, "add")
			return
		}
		an, err := anchorOf(call).Anchor(&r.Spec)
		if err != nil {
			incomplete(a, "Add a narration line", apperr.From(err).Message)
			return
		}
		plan, err := voiceover.BeatAddPlan(r, call.Flags["text"], call.Flags["id"], call.Flags["delivery"], call.Flags["after"], call.Flags["before"], an)
		if err != nil {
			incomplete(a, "Add a narration line", apperr.From(err).Message)
			return
		}
		b := plan.Ops[0].Beat
		m.planCard(ctx, a, plan, "Add a narration line: "+quoted(clip(b.Text, 100)), "Starts "+voiceover.Placement(&r.Spec, b.Anchor))
	case "voiceover beat update":
		var ch voiceover.BeatChange
		str := func(name string) *string {
			if v, ok := call.Flags[name]; ok {
				return &v
			}
			return nil
		}
		ch.Text, ch.Delivery, ch.State, ch.Voice, ch.Lock = str("text"), str("delivery"), str("state"), str("voice"), hasBool(call, "lock")
		plan, err := voiceover.BeatUpdatePlan(r, arg, ch)
		if err != nil {
			incomplete(a, "Change a narration line", apperr.From(err).Message)
			return
		}
		var details []string
		if ch.Text != nil {
			details = append(details, "New words: "+quoted(clip(strings.TrimSpace(*ch.Text), 160)))
		}
		if ch.Delivery != nil {
			details = append(details, "How to say it: "+clip(*ch.Delivery, 160))
		}
		if ch.Voice != nil {
			if *ch.Voice == "" {
				details = append(details, "Uses the project's voice again")
			} else {
				details = append(details, "A different AI voice for this line")
			}
		}
		if ch.State != nil && *ch.State == "final" {
			details = append(details, "Marked final")
		}
		if ch.Lock {
			details = append(details, "Locked afterwards")
		}
		m.planCard(ctx, a, plan, "Change "+lineName(r, arg), details...)
	case "voiceover beat move":
		if _, ok := call.Flags["to"]; ok {
			m.lineCard(ctx, call, r, a, "move")
			return
		}
		an, err := anchorOf(call).Anchor(&r.Spec)
		if err != nil {
			incomplete(a, "Move a narration line", apperr.From(err).Message)
			return
		}
		plan, err := voiceover.BeatMovePlan(r, arg, an, call.Flags["after"], call.Flags["before"])
		if err != nil {
			incomplete(a, "Move a narration line", apperr.From(err).Message)
			return
		}
		var details []string
		if an != nil {
			details = append(details, "Starts "+voiceover.Placement(&r.Spec, *an))
		}
		m.planCard(ctx, a, plan, "Move "+lineName(r, arg), details...)
	case "voiceover beat remove":
		plan, err := voiceover.BeatRemovePlan(r, arg)
		if err != nil {
			incomplete(a, "Remove a narration line", apperr.From(err).Message)
			return
		}
		m.planCard(ctx, a, plan, "Remove "+lineName(r, arg))
	case "voiceover fit":
		out, err := callOp[voiceover.FitOut](ctx, m, "voiceover.fit", voiceover.FitIn{RevisionID: r.ID, Strategy: call.Flags["apply"], Beats: listFlag(call, "beat")})
		if err != nil {
			incomplete(a, "Make the narration fit", apperr.From(err).Message)
			return
		}
		if out.Result == nil {
			incomplete(a, "Make the narration fit", plainSummary(out.Summary))
			return
		}
		details := []string{fitWords(call.Flags["apply"])}
		if out.Over > 0 {
			details = append(details, pluralize(out.Over, "line")+" ran long")
		}
		for _, rw := range out.Rewrite {
			details = append(details, editspec.LineName(rw.Line-1, rw.Text)+" still needs fewer words")
		}
		m.planCard(ctx, a, out.Result.Plan, "Make the narration fit", details...)
	case "voiceover takes choose":
		in := voiceover.ChooseIn{RevisionID: r.ID, Beat: call.Flags["beat"], TakeID: call.Flags["take"]}
		title := "Play another take on " + lineName(r, in.Beat)
		switch {
		case hasBool(call, "original"):
			in.Version, title = "original", "Play the original recording on "+lineName(r, in.Beat)
		case hasBool(call, "processed"):
			in.Version, title = "processed", "Play the cleaned-up recording on "+lineName(r, in.Beat)
		}
		res, err := callOp[voiceover.PlanResult](ctx, m, "voiceover.choose", in)
		if err != nil {
			incomplete(a, "Switch a narration take", apperr.From(err).Message)
			return
		}
		m.planCard(ctx, a, res.Plan, title)
	case "mix set":
		if hasBool(call, "preview") {
			incomplete(a, "Hear a change to the sound mix", "A preview only shows how a change would sound; ask without --preview to change the mix.")
			return
		}
		plan, words, err := voiceover.MixPlan(r, mixChangeOf(call))
		if err != nil {
			incomplete(a, "Change the sound mix", apperr.From(err).Message)
			return
		}
		m.planCard(ctx, a, plan, mixTitle(words), mixDetails(words)...)
	case "voiceover scratch":
		in := voiceover.ScratchIn{RevisionID: r.ID, Beats: listFlag(call, "beat"), Voice: clip(call.Flags["voice"], 80), Replace: hasBool(call, "replace"), Apply: true}
		if w, err := strconv.ParseFloat(call.Flags["wpm"], 64); err == nil && w >= 80 && w <= 300 {
			in.WPM = w
		}
		n := countLines(r, in.Beats, func(t *editspec.TakeRef) bool { return t == nil || (in.Replace && t.Source == models.TakeScratch) })
		if n == 0 {
			incomplete(a, "Make a scratch voice", "Every line already has audio.")
			return
		}
		a.Title = "Make a free scratch voice for " + pluralize(n, "line")
		a.Details = []string{"A computer voice to check the timing; nothing is spent", "Puts it in the cut as a new version"}
		setInput(a, "voiceover.scratch", in)
	case "voiceover takes place":
		// Only a take that is already in the project (a recording is placed by
		// the person who made it, in the recorder or their terminal).
		const title = "Place a live take"
		if !ids.Valid(models.ReadPrefix, arg) && !ids.Valid(models.VoiceTakePrefix, arg) {
			incomplete(a, title, "A new recording is placed by the person who made it, in the recorder or their terminal.")
			return
		}
		in := voiceover.PlaceIn{RevisionID: r.ID, Read: arg, Where: "recorded", Lines: listFlag(call, "lines")}
		if hasBool(call, "planned") {
			in.Where = "planned"
		}
		out, err := callOp[voiceover.PlaceOut](ctx, m, "voiceover.place", in)
		if err != nil || out.Result == nil {
			why := "Nothing to change."
			if err != nil {
				why = apperr.From(err).Message
			}
			incomplete(a, title, why)
			return
		}
		a.Title = "Put the lines of a live take back where they were spoken"
		if in.Where == "planned" {
			a.Title = "Put the lines of a live take at their planned spots"
		}
		a.Details = []string{plainSummary(out.Summary), "The take itself does not change; you can switch back any time"}
		in.Apply = true
		setInput(a, "voiceover.place", in)
	case "voiceover tts":
		m.describeTTS(ctx, call, r, a)
	case "voiceover import":
		m.describeImport(ctx, call, r, a)
	case "voiceover cleanup":
		if hasBool(call, "list") {
			incomplete(a, "Clean up the narration", "Listing the kinds of clean-up only reads.")
			return
		}
		in := voiceover.CleanupIn{RevisionID: r.ID, Beats: listFlag(call, "beat"), Preset: call.Flags["preset"], Apply: true}
		if in.Preset == "" {
			in.Preset = "voice-clean"
		}
		var preset *voiceover.Preset
		for i := range voiceover.Presets {
			if voiceover.Presets[i].Name == in.Preset {
				preset = &voiceover.Presets[i]
			}
		}
		if preset == nil {
			incomplete(a, "Clean up the narration", "There is no clean-up called "+strconv.Quote(in.Preset)+".")
			return
		}
		keep := hasBool(call, "keep-original")
		if keep {
			f := false
			in.Use = &f
		}
		n := countLines(r, in.Beats, func(t *editspec.TakeRef) bool {
			return t != nil && (t.Source == models.TakeRecorded || t.Source == models.TakeImported)
		})
		if n == 0 {
			incomplete(a, "Clean up the narration", "No line has a recording to clean up.")
			return
		}
		a.Title = fmt.Sprintf("Clean up the recording on %s (%s)", pluralize(n, "line"), strings.ToLower(preset.Label))
		a.Details = []string{upperFirst(strings.TrimSuffix(preset.Summary, ".")), "Keeps the original so you can compare"}
		if keep {
			a.Details[1] = "The original keeps playing; you can switch any time"
		}
		setInput(a, "voiceover.cleanup", in)
	}
}

// describeTTS prices AI narration before anything is spent: the card says what
// it costs, and cannot be confirmed when the key is missing or the budget is short.
func (m *Manager) describeTTS(ctx context.Context, call *cliCall, r *models.Revision, a *AssistantAction) {
	if hasBool(call, "estimate") {
		incomplete(a, "Price the AI voice", "Pricing only reads (voiceover estimate).")
		return
	}
	in := voiceover.TTSIn{RevisionID: r.ID, Beats: listFlag(call, "beat"), Voice: clip(call.Flags["voice"], 120), ModelID: clip(call.Flags["model"], 120), Regenerate: hasBool(call, "regenerate")}
	if v, err := strconv.ParseFloat(call.Flags["speed"], 64); err == nil && v >= 0.7 && v <= 1.2 {
		in.Speed = &v
	}
	if v, err := strconv.ParseFloat(call.Flags["stability"], 64); err == nil && v >= 0 && v <= 1 {
		in.Stability = &v
	}
	ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	est, err := callOp[voiceover.EstimateOut](ectx, m, "voiceover.estimate", in)
	const title = "Make the AI voice"
	switch {
	case err != nil && apperr.Is(err, apperr.NotConfigured):
		unavailable(a, title, "ElevenLabs isn't set up on this computer yet. Add your key in Settings → Integrations, then ask again.")
		return
	case err != nil:
		unavailable(a, title, "MetaRush could not price it: "+firstLine(apperr.From(err).Message))
		return
	case len(est.Lines) == 0:
		incomplete(a, title, "Every line already has an AI voice or a recording.")
		return
	}
	n := len(est.Lines)
	// Whose voice it is belongs on the card: one voice in the title, several
	// (lines with their own voice) listed below it.
	names := est.VoiceNames
	if len(names) == 0 && est.Voice.VoiceName != "" {
		names = []string{est.Voice.VoiceName}
	}
	with := ""
	if len(names) == 1 {
		with = ", with " + clip(names[0], 40)
	}
	if est.CachedLines >= n {
		a.Title = "Use the AI voice already made for " + pluralize(n, "line") + with + " (free)"
	} else {
		// The estimate in dollars (D208); the credits are in its parts.
		a.Title = fmt.Sprintf("Make the AI voice for %s%s (%s)", pluralize(n, "line"), with, est.Cost.About)
	}
	if !est.Cost.Empty() {
		cost := est.Cost
		a.Estimate = &cost
		a.SpendsIn = r.ProjectID // the version's project pays (D241)
	}
	if len(names) > 1 {
		a.Details = append(a.Details, "Voices: "+clip(strings.Join(names, ", "), 120))
	}
	if est.CachedLines > 0 && est.CachedLines < n {
		a.Details = append(a.Details, pluralize(est.CachedLines, "line")+" already made, reused for free")
	}
	if b := est.Budget; b != nil && b.LimitCredits != nil {
		a.Details = append(a.Details, fmt.Sprintf("This project has %s of %s credits left", thousands(math.Max(0, *b.LimitCredits-b.UsedCredits)), thousands(*b.LimitCredits)))
	}
	a.Details = append(a.Details, "Puts it in the cut as a new version")
	if !est.Allowed {
		why := "This would go over the spending limit."
		if len(est.Warnings) > 0 {
			why = est.Warnings[0]
		}
		unavailable(a, a.Title, why)
		return
	}
	in.Apply = true
	setInput(a, "voiceover.tts", in)
}

// describeImport places a recording MetaRush already has (an uploaded file):
// files on disk are added from the dashboard first, like footage.
func (m *Manager) describeImport(ctx context.Context, call *cliCall, r *models.Revision, a *AssistantAction) {
	const title = "Place a narration recording"
	id := first(call.Args)
	if !ids.Valid(ids.Asset, id) {
		unavailable(a, title, "Add the recording from the dashboard first: drop the file anywhere on the page or use Upload. Then ask Claude to place it.")
		return
	}
	var as models.Asset
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	err := m.App.DB.GetByID(lctx, store.CAssets, m.App.WorkspaceID(), id, &as)
	cancel()
	if err != nil {
		incomplete(a, title, "Claude did not name a recording MetaRush has.")
		return
	}
	name := quoted(clip(clipName(&as), 60))
	if as.Transcript == nil || as.Transcript.State != "available" {
		unavailable(a, "Place the recording "+name, "MetaRush has not finished listening to this recording yet. Ask again once it has a transcript.")
		return
	}
	in := voiceover.AlignIn{RevisionID: r.ID, AssetID: id, Layout: editspec.LayoutBeats, Source: models.TakeImported, Beats: listFlag(call, "beat"), Apply: true}
	if hasBool(call, "recorded") {
		in.Source = models.TakeRecorded
	}
	whole := hasBool(call, "continuous") && call.Flags["split-at-beats"] != "true"
	if call.Flags["split-at-beats"] == "false" {
		whole = true
	}
	if whole {
		in.Layout = editspec.LayoutContinuous
		a.Title = "Place the recording " + name + " as one piece"
		a.Details = []string{"Plays it from the first line's spot"}
	} else {
		a.Title = "Place the recording " + name + " on the narration lines"
		a.Details = []string{"Splits it at each line, so every piece follows its shot"}
	}
	a.Details = append(a.Details, "Puts it in the cut as a new version")
	setInput(a, "voiceover.align", in)
}

// planCard is a card that applies a plan built here, like an "Apply plan"
// card: checked now so it shows what changes.
func (m *Manager) planCard(ctx context.Context, a *AssistantAction, plan editspec.Plan, title string, details ...string) {
	raw, err := json.Marshal(plan)
	if err != nil {
		incomplete(a, title, "MetaRush could not prepare this change.")
		return
	}
	a.Title = clip(title, 160)
	a.Details = append(a.Details, details...)
	in, _ := json.Marshal(ops.EditCheckIn{Plan: raw})
	if res, err := ops.Call(ctx, m.App, "edit.check", in, ops.Caller{}); err == nil {
		co := *res.(*ops.EditCheckOut)
		co.ConfirmToken, co.ExpiresAt, co.Next = "", nil, ""
		a.Check = &co
		if d := co.Diff; d != nil {
			a.Details = append(a.Details, diffLine(d))
		}
		if !co.Valid {
			a.Details = append(a.Details, pluralize(len(co.Errors), "problem")+" to fix first")
		}
	} else {
		a.Details = append(a.Details, "Could not check it: "+firstLine(apperr.From(err).Message))
	}
	setInput(a, "edit.apply", ops.EditApplyIn{Plan: raw})
}

// revisionFor is the cut a command names: --revision, or a project's latest cut.
func (m *Manager) revisionFor(ctx context.Context, call *cliCall) *models.Revision {
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	a := m.App
	id := call.Flags["revision"]
	if !ids.Valid(ids.Revision, id) {
		prj := call.Flags["project"]
		if !ids.Valid(ids.Project, prj) {
			return nil
		}
		var p models.Project
		if a.DB.GetByID(lctx, store.CProjects, a.WorkspaceID(), prj, &p) != nil {
			return nil
		}
		branch := call.Flags["branch"]
		if branch == "" {
			branch = "main"
		}
		if id = p.Branches[branch].Head; id == "" {
			return nil
		}
	}
	var r models.Revision
	if a.DB.GetByID(lctx, store.CRevisions, a.WorkspaceID(), id, &r) != nil {
		return nil
	}
	return &r
}

// proposalFile reads a file Claude wrote in this conversation's proposals
// folders (the only place it can write); why says what is wrong otherwise.
func (m *Manager) proposalFile(c *conv, path, what string) ([]byte, string) {
	if path == "" || path == "-" {
		return nil, "Claude did not name the " + what + " file."
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(m.workspaceDir(), path)
	}
	real := func(p string) string {
		if rp, err := filepath.EvalSymlinks(p); err == nil {
			return rp
		}
		return p
	}
	root := real(filepath.Join(m.convDir(c.id), "proposals"))
	rel, err := filepath.Rel(root, real(path))
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return nil, "The " + what + " is not in this conversation's proposals folder."
	}
	dir, file := filepath.Split(filepath.Join(root, rel))
	raw, err := readProposal(filepath.Clean(dir), file)
	if err != nil {
		return nil, "MetaRush cannot read the " + what + ": " + err.Error()
	}
	return raw, ""
}

// callOp runs a read-only op for a card (on the server, never the browser's input).
func callOp[O any](ctx context.Context, m *Manager, name string, in any) (*O, error) {
	return ops.CallTyped[O](ctx, m.App, name, in, ops.Caller{})
}

func anchorOf(call *cliCall) voiceover.AnchorSpec {
	s := voiceover.AnchorSpec{Clip: call.Flags["at-clip"], At: call.Flags["at-time"], Moment: call.Flags["at-moment"], Quote: clip(call.Flags["quote"], 200),
		AfterPrevious: hasBool(call, "after-previous")}
	if v, err := strconv.ParseFloat(call.Flags["offset"], 64); err == nil {
		s.Offset = v
	}
	if v, err := strconv.ParseFloat(call.Flags["gap"], 64); err == nil {
		s.Gap = &v
	}
	return s
}

// listFlag reads a repeatable list flag (--beat 1 --beat 3, or --beat 1,3).
func listFlag(call *cliCall, name string) []string {
	var out []string
	for _, v := range call.Multi[name] {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" && len(out) < 200 {
				out = append(out, clip(p, 64))
			}
		}
	}
	return out
}

// countLines counts the lines (all, or the chosen ones) whose take matches.
func countLines(r *models.Revision, chosen []string, match func(*editspec.TakeRef) bool) int {
	if r.Spec.Voiceover == nil {
		return 0
	}
	want := map[string]bool{}
	for _, ref := range chosen {
		if i, err := voiceover.FindBeat(r, ref); err == nil {
			want[r.Spec.Voiceover.Beats[i].ID] = true
		}
	}
	n := 0
	for _, b := range r.Spec.Voiceover.Beats {
		if (len(chosen) == 0 || want[b.ID]) && match(b.Take) {
			n++
		}
	}
	return n
}

// clipLabel names a clip for card titles: its label, else its place in the cut.
func clipLabel(r *models.Revision, ref string) string {
	for i, c := range r.Spec.Clips {
		if c.ID == ref || (c.Label != "" && strings.EqualFold(c.Label, ref)) {
			if c.Label != "" {
				return c.Label
			}
			return fmt.Sprintf("shot %d", i+1)
		}
	}
	return ref
}

// lineName names a narration line for card titles, mid-sentence: narration
// line 3 (“It started in a tiny garage.”).
func lineName(r *models.Revision, ref string) string {
	if i, err := voiceover.FindBeat(r, ref); err == nil {
		n := editspec.LineName(i, r.Spec.Voiceover.Beats[i].Text)
		return strings.ToLower(n[:1]) + n[1:]
	}
	return "narration line " + ref
}

func firstWords(beats []editspec.Beat) []string {
	if len(beats) == 0 {
		return nil
	}
	return []string{"Starts with " + quoted(clip(beats[0].Text, 120))}
}

func parseProblem(out *voiceover.ParseOut, err error) string {
	if err != nil {
		return apperr.From(err).Message
	}
	if out != nil && len(out.Issues) > 0 {
		return out.Issues[0].Message
	}
	return "The script has no lines."
}

// fitWords says what a fit strategy does to the video.
func fitWords(strategy string) string {
	switch strategy {
	case "extend-shot":
		return "Holds the shots a little longer"
	case "speed-up":
		return "Plays the AI voice a little faster"
	case "start-earlier":
		return "Starts those lines a little earlier"
	case "move-next":
		return "Moves the next line later"
	}
	return "Uses the best fix for each line"
}

func mixChangeOf(call *cliCall) voiceover.MixChange {
	num := func(name string) *float64 {
		if v, err := strconv.ParseFloat(call.Flags[name], 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
			return &v
		}
		return nil
	}
	onOff := func(name string) *bool {
		v, ok := call.Flags[name]
		if !ok {
			return nil
		}
		on := v == "on" || v == "true" || v == "yes"
		return &on
	}
	return voiceover.MixChange{Bus: call.Flags["bus"], Clip: call.Flags["clip"], Gain: num("gain"), Mute: hasBool(call, "mute"), Unmute: hasBool(call, "unmute"),
		Solo: hasBool(call, "solo"), Unsolo: hasBool(call, "unsolo"), MuteSource: hasBool(call, "mute-source"), UnmuteSource: hasBool(call, "unmute-source"),
		Reset: hasBool(call, "reset"), Ducking: onOff("ducking"), Normalize: onOff("normalize"), MusicUnderVoice: num("music-under-voice"),
		SourceUnderVoice: num("source-under-voice"), MusicUnderDialogue: num("music-under-dialogue"), Loudness: num("loudness"), PeakCeiling: num("peak-ceiling"),
		LoudnessAuto: hasBool(call, "loudness-auto"), LevelSpeech: onOff("level-speech"), CutBlend: num("cut-blend")}
}

// lineCard places a line at a point of the video (--to), tied to what plays
// there, through the same op the dashboard's narration strip uses.
func (m *Manager) lineCard(ctx context.Context, call *cliCall, r *models.Revision, a *AssistantAction, action string) {
	title := "Move " + lineName(r, first(call.Args))
	if action == "add" {
		title = "Add a narration line"
	}
	t, ok := editspec.ParseClock(call.Flags["to"])
	if !ok {
		incomplete(a, title, "Claude did not give a time to place the line at (m:ss.s).")
		return
	}
	in := voiceover.LineIn{RevisionID: r.ID, Action: action, Line: first(call.Args), To: &t, Exact: hasBool(call, "exact"),
		After: clip(call.Flags["after"], 64), Before: clip(call.Flags["before"], 64)}
	if action == "add" {
		text, delivery := clip(call.Flags["text"], 2000), clip(call.Flags["delivery"], 300)
		in.Text, in.Delivery, in.ID = &text, &delivery, clip(call.Flags["id"], 64)
		title = "Add a narration line: " + quoted(clip(strings.TrimSpace(text), 100))
	}
	out, err := callOp[voiceover.LineOut](ctx, m, "voiceover.line", in)
	if err != nil || out.Result == nil {
		why := "MetaRush could not prepare this change."
		if err != nil {
			why = apperr.From(err).Message
		}
		incomplete(a, title, why)
		return
	}
	m.planCard(ctx, a, out.Result.Plan, title, "Starts "+out.Placement)
}

// describeVoiceSettings turns `providers settings` (the narration voice, how it
// sounds, a project's spending limit, favorites) into a card in plain words.
// Voices named by ID are shown by name; the key itself is never involved.
func (m *Manager) describeVoiceSettings(ctx context.Context, call *cliCall, a *AssistantAction) {
	const title = "Change the AI voice settings"
	name := first(call.Args)
	if name == "" {
		name = providers.ElevenLabsName
	}
	if name != providers.ElevenLabsName {
		incomplete(a, title, "Claude named an integration MetaRush does not have.")
		return
	}
	in := providers.SettingsIn{Name: name, ProjectID: call.Flags["project"], Voice: clip(call.Flags["voice"], 120), LibraryOwnerID: clip(call.Flags["library-owner"], 120),
		ModelID: clip(call.Flags["model"], 120), OutputFormat: clip(call.Flags["output-format"], 40), Favorite: clip(call.Flags["favorite"], 120),
		Unfavorite: clip(call.Flags["unfavorite"], 120), Clear: listFlag(call, "clear")}
	num := func(flag string, lo, hi float64) *float64 {
		if v, err := strconv.ParseFloat(call.Flags[flag], 64); err == nil && v >= lo && v <= hi {
			return &v
		}
		return nil
	}
	in.Stability, in.SimilarityBoost, in.Style, in.Speed = num("stability", 0, 1), num("similarity", 0, 1), num("style", 0, 1), num("speed", 0.7, 1.2)
	in.BudgetCredits = num("budget", 0, 1e9)
	if v, ok := call.Flags["speaker-boost"]; ok {
		on := v == "on" || v == "true" || v == "yes" || v == ""
		in.SpeakerBoost = &on
	}
	if v, ok := call.Flags["region"]; ok {
		rg := clip(v, 4)
		in.Region = &rg
	}
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	projectName := ""
	if in.ProjectID != "" {
		var p models.Project
		if !ids.Valid(ids.Project, in.ProjectID) || m.App.DB.GetByID(lctx, store.CProjects, m.App.WorkspaceID(), in.ProjectID, &p) != nil {
			incomplete(a, title, "Claude did not name a project MetaRush has.")
			return
		}
		projectName = p.Name
	}
	if in.BudgetCredits != nil && in.ProjectID == "" {
		incomplete(a, title, "Spending limits are set per project (--project).")
		return
	}
	// Voices by name: the account's voices (or the library's) as the voice list shows them.
	voiceName := func(ref string, library bool) (string, bool) {
		vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		vin := voiceover.VoicesIn{Provider: providers.ElevenLabsName, Library: library}
		if !strings.HasPrefix(ref, "voice_") && len(ref) < 30 {
			vin.Search = ref
		}
		out, err := callOp[voiceover.VoicesOut](vctx, m, "voiceover.voices", vin)
		if err != nil {
			return "", false
		}
		for _, v := range out.Voices {
			if v.ID == ref || strings.EqualFold(v.Name, ref) {
				return v.Name, true
			}
		}
		return "", true
	}
	where := "for everyone in this workspace"
	if projectName != "" {
		where = "for " + quoted(clip(projectName, 60))
	}
	var parts []string
	if in.Voice != "" {
		n, reached := voiceName(in.Voice, in.LibraryOwnerID != "")
		switch {
		case !reached:
			unavailable(a, title, "ElevenLabs isn't set up on this computer yet. Add your key in Settings → Integrations, then ask again.")
			return
		case n == "":
			incomplete(a, title, "Claude named a voice this ElevenLabs account does not have.")
			return
		}
		parts = append(parts, "use "+clip(n, 60)+" as the narration voice "+where)
	}
	if in.ModelID != "" {
		parts = append(parts, "use another AI voice model "+where)
	}
	if in.Stability != nil {
		parts = append(parts, map[bool]string{true: "make the AI voice steadier", false: "make the AI voice livelier"}[*in.Stability >= 0.5])
	}
	if in.Speed != nil {
		switch {
		case *in.Speed > 1.005:
			parts = append(parts, fmt.Sprintf("make the AI voice speak %d%% faster", int(math.Round((*in.Speed-1)*100))))
		case *in.Speed < 0.995:
			parts = append(parts, fmt.Sprintf("make the AI voice speak %d%% slower", int(math.Round((1-*in.Speed)*100))))
		default:
			parts = append(parts, "let the AI voice speak at its normal speed")
		}
	}
	if in.SimilarityBoost != nil || in.Style != nil || in.SpeakerBoost != nil || in.OutputFormat != "" {
		parts = append(parts, "change how the AI voice sounds")
	}
	if in.Region != nil {
		parts = append(parts, "change where ElevenLabs keeps the voice data")
	}
	if b := in.BudgetCredits; b != nil {
		if *b <= 0 {
			parts = append(parts, "remove the spending limit "+where)
		} else {
			parts = append(parts, fmt.Sprintf("let %s spend up to %s credits on the AI voice", quoted(clip(projectName, 60)), thousands(*b)))
		}
	}
	if in.Favorite != "" || in.Unfavorite != "" {
		ref, add := in.Favorite, true
		if ref == "" {
			ref, add = in.Unfavorite, false
		}
		n, _ := voiceName(ref, false)
		if n == "" {
			n = "a voice"
		}
		parts = append(parts, map[bool]string{true: "add " + clip(n, 60) + " to the team's favorite voices", false: "take " + clip(n, 60) + " off the team's favorite voices"}[add])
	}
	if len(in.Clear) > 0 {
		parts = append(parts, "go back to the default for "+pluralize(len(in.Clear), "voice setting"))
	}
	if len(parts) == 0 {
		incomplete(a, title, "Claude did not say what to change.")
		return
	}
	a.Title = upperFirst(clip(parts[0], 160))
	for _, p := range parts[1:] {
		a.Details = append(a.Details, upperFirst(p))
	}
	a.Details = append(a.Details, "Applies to AI voice made from now on; lines already made keep their sound")
	setInput(a, "provider.settings", in)
}

var amountRe = regexp.MustCompile(`\s*\([^)]*\)`)

// mixTitle is the change without amounts ("Turn the music down under speech").
func mixTitle(words []string) string {
	var parts []string
	for _, w := range words {
		parts = append(parts, amountRe.ReplaceAllString(w, ""))
	}
	return upperFirst(clip(strings.Join(parts, ", "), 120))
}

// mixDetails keeps the amounts for those who want them.
func mixDetails(words []string) []string {
	var out []string
	for _, w := range words {
		if amountRe.MatchString(w) {
			out = append(out, upperFirst(w))
		}
	}
	return out
}

// thousands writes a whole number with thousands separators ("1,234").
func thousands(v float64) string {
	s := strconv.FormatInt(int64(math.Round(v)), 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// narrationResult is a generation's result for the card: its plain summary,
// with the new version's ID at the end for Claude (the dashboard hides IDs).
func narrationResult(summary string, ap *voiceover.Applied) (string, string) {
	s := plainSummary(summary)
	if ap == nil || ap.RevisionID == "" {
		return s, ""
	}
	return s + " (" + ap.RevisionID + ")", "/p/" + ap.ProjectID + "/r/" + ap.RevisionID
}
