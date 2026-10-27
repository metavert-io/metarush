package sound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// Action names for tracked sound work (voiceover.ActionTitles).
const (
	ActionEnhance = "enhance"
	ActionSetup   = "sound-setup"
)

func init() {
	voiceover.ActionTitles[ActionEnhance] = "Clearer voices"
	voiceover.ActionTitles[ActionSetup] = "Setting up clearer voices"
}

// runEnhance makes voices clearer. Processing takes a while, so the cut may
// have moved on meanwhile (another person's voice made clearer, a level
// saved): then the same change is made again on the latest version, where
// everything just made is cached, so nothing is processed or paid for twice.
func runEnhance(ctx context.Context, a *app.App, in *EnhanceIn) (*EnhanceOut, error) {
	out, err := enhanceOnce(ctx, a, in)
	for i := 0; i < 3 && err == nil && out.stale; i++ {
		head, herr := latestHead(ctx, a, in.RevisionID)
		if herr != nil || head == "" || head == in.RevisionID {
			break
		}
		next := *in
		next.RevisionID = head
		in = &next
		out, err = enhanceOnce(ctx, a, in)
	}
	return out, err
}

// latestHead is the newest version on the branch a revision belongs to.
func latestHead(ctx context.Context, a *app.App, revisionID string) (string, error) {
	var rev models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), revisionID, &rev); err != nil {
		return "", err
	}
	var p models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), rev.ProjectID, &p); err != nil {
		return "", err
	}
	return p.Branches[rev.Branch].Head, nil
}

func enhanceOnce(ctx context.Context, a *app.App, in *EnhanceIn) (*EnhanceOut, error) {
	if in.Mode != "" && !contains(editspec.EnhanceModes, in.Mode) {
		return nil, apperr.New(apperr.InvalidInput, "A voice is made clearer with clean, studio or the cloud, not “%s”.", in.Mode)
	}
	c, err := loadCut(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	t, err := resolveTarget(c, in.Target)
	if err != nil {
		return nil, err
	}
	if t.kind == targetNarration {
		return enhanceNarration(ctx, a, c, t, in)
	}
	m := cloneMix(c.spec.Mix)
	affected := map[string]bool{}
	uses := c.uses()
	var setting *editspec.VoiceSound
	switch t.kind {
	case targetPerson:
		setting = changeVoice(m.VoiceFor(t.name), in)
		m = withVoice(m, t.name, "", setting)
		for _, u := range uses {
			for _, sp := range editspec.SpeakersIn(c.transcript(u.asset), u.from, u.to) {
				if sp == t.name {
					affected[u.asset] = true
				}
			}
		}
	case targetRecording:
		setting = changeVoice(m.RecordingVoice(t.asset), in)
		m = withVoice(m, "", t.asset, setting)
		affected[t.asset] = true
	case targetEveryone:
		for _, u := range uses {
			if affected[u.asset] {
				continue
			}
			affected[u.asset] = true
			setting = changeVoice(m.RecordingVoice(u.asset), in)
			m = withVoice(m, "", u.asset, setting)
		}
		if len(affected) == 0 {
			return nil, apperr.New(apperr.InvalidInput, "No clip in this cut has camera sound to make clearer.")
		}
	}
	var mine []use
	for _, u := range uses {
		if affected[u.asset] {
			mine = append(mine, u)
		}
	}
	eng := newEngine(a, c.rev.ProjectID)
	wins := windows(mine, func(id string) float64 {
		if as := c.asset(id); as != nil {
			return as.Media.Duration
		}
		return 0
	})
	jobs, wets, err := planCopies(ctx, a, eng, c, m, wins)
	if err != nil {
		return nil, err
	}
	out := &EnhanceOut{Target: t.words, Copies: []CopyView{}, Scores: []PersonScore{}}
	if setting != nil && setting.Enhance != nil {
		out.Mode, out.Strength, out.Off = setting.Enhance.Mode, setting.Enhance.StrengthOf(), setting.Enhance.Off
	}
	out.Estimate = estimate(ctx, a, eng, c.rev.ProjectID, jobs, wets)
	if in.Estimate {
		out.Summary = out.Estimate.Summary
		if !out.Estimate.Allowed {
			out.Summary += " " + out.Estimate.Problem
		}
		return out, nil
	}
	if !out.Estimate.Allowed {
		out.Estimate.recordKept(ctx, a) // asked for, not estimated (D290)
		return nil, out.Estimate.refused()
	}
	return voiceover.TrackWork(ctx, a, ActionEnhance, c.rev.ID, "", func(ctx context.Context) (*EnhanceOut, error) {
		if err := setUpFirst(ctx, a, eng.Comp, out.Estimate); err != nil {
			return nil, err
		}
		views, err := makeAll(ctx, a, eng, newQuality(a, eng.Comp), c, jobs, wets, false, nil)
		if err != nil {
			return nil, err
		}
		out.Copies = views
		var enh []editspec.EnhancedSound
		for _, e := range m.Enhanced {
			if !affected[e.AssetID] {
				enh = append(enh, e)
			}
		}
		for i, j := range jobs {
			enh = append(enh, editspec.EnhancedSound{AssetID: j.asset.ID, Start: j.win.start, Variant: views[i].Variant, Settings: j.settings})
		}
		m.Enhanced = enh
		for _, s := range mergeScores(views) {
			if t.kind != targetPerson || s.Speaker == t.name {
				out.Scores = append(out.Scores, s)
			}
		}
		for _, s := range out.Scores {
			out.Warnings = append(out.Warnings, s.Warnings...)
		}
		var mix *editspec.Mix
		if !mixEmpty(m) {
			mix = m
		}
		instr := fmt.Sprintf("Clearer voice: %s", enhanceWords(t.words, setting, in))
		res, note, err := planCheckAfterWork(ctx, a, c.rev, instr, []editspec.Op{{Op: "setMix", Mix: mix}})
		if err != nil {
			return nil, err
		}
		out.Result = res
		out.Summary = enhanceSummary(t.words, setting, in, out)
		if note != "" {
			out.Summary += " " + note
			out.stale = true
		} else if in.Apply {
			out.Applied = applyResult(ctx, a, res)
			out.Summary += appliedNote(out.Applied)
		} else {
			out.Summary += " Apply the change to put it in the cut."
		}
		return out, nil
	})
}

func mixEmpty(m *editspec.Mix) bool { return m == nil || equalJSONish(*m, editspec.Mix{}) }

func equalJSONish(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func enhanceWords(who string, v *editspec.VoiceSound, in *EnhanceIn) string {
	switch {
	case in.Remove || v == nil || v.Enhance == nil:
		return who + " as recorded"
	case v.Enhance.Off:
		return who + " plays the original voice (the clearer one is kept)"
	}
	return fmt.Sprintf("%s, %s, %d%%", who, modeWords(v.Enhance.Mode), int(math.Round(v.Enhance.StrengthOf()*100)))
}

func enhanceSummary(who string, v *editspec.VoiceSound, in *EnhanceIn, out *EnhanceOut) string {
	var s string
	switch {
	case in.Remove || v == nil || v.Enhance == nil:
		s = fmt.Sprintf("%s plays as recorded again.", upper(who))
	case v.Enhance.Off:
		s = fmt.Sprintf("%s plays the original voice; the clearer version is kept for when you switch back.", upper(who))
	default:
		s = fmt.Sprintf("%s sounds clearer (%s, %d%% strength).", upper(who), modeWords(v.Enhance.Mode), int(math.Round(v.Enhance.StrengthOf()*100)))
	}
	for _, sc := range out.Scores {
		if sc.Summary != "" && !strings.HasPrefix(sc.Summary, "Not measured") {
			name := sc.Speaker
			if name == "" {
				name = "The voice"
			}
			s += " " + name + ": " + strings.ToLower(sc.Summary[:1]) + sc.Summary[1:]
		}
	}
	reused := 0
	for _, c := range out.Copies {
		if c.Reused {
			reused++
		}
	}
	if reused > 0 && reused == len(out.Copies) {
		s += " Made before with the same settings, so it cost nothing."
	}
	if out.Estimate.Credits > 0 && reused < len(out.Copies) {
		s += fmt.Sprintf(" About %.0f ElevenLabs credits", out.Estimate.Credits)
		if out.Estimate.Cost != nil {
			s += " (" + out.Estimate.Cost.About + ")"
		}
		s += "."
	}
	return s
}

func upper(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// enhanceNarration makes the recorded narration clearer: each recorded or
// imported take gets a processed version the line plays (the take's own A/B).
func enhanceNarration(ctx context.Context, a *app.App, c *cut, t target, in *EnhanceIn) (*EnhanceOut, error) {
	v := c.spec.Voiceover
	type ref struct {
		beat  string // "" = the continuous recording
		take  editspec.TakeRef
		asset string
	}
	var refs []ref
	if v != nil {
		for _, b := range v.Beats {
			if b.Take != nil && (b.Take.Source == models.TakeRecorded || b.Take.Source == models.TakeImported) {
				refs = append(refs, ref{beat: b.ID, take: *b.Take, asset: b.Take.AssetID})
			}
		}
		if cv := v.Continuous; cv != nil && (cv.Source == models.TakeRecorded || cv.Source == models.TakeImported) {
			refs = append(refs, ref{take: editspec.TakeRef{AssetID: cv.AssetID, In: cv.In, Out: cv.Out, Processed: cv.Processed, UseProcessed: cv.UseProcessed}, asset: cv.AssetID})
		}
	}
	if len(refs) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "This cut has no recorded narration to make clearer (AI voices are clear already).")
	}
	// The current setting is read from the takes' cleaned versions.
	mode, strength, curOff := "", 0.0, false
	for _, r := range refs {
		if r.take.Processed != nil {
			if md, st, ok := ParsePreset(r.take.Processed.Preset); ok {
				mode, strength, curOff = md, st, !r.take.UseProcessed
				break
			}
		}
	}
	if in.Mode != "" {
		if in.Mode != mode && in.Strength == nil {
			strength = 0
		}
		mode = in.Mode
	}
	if mode == "" {
		mode = editspec.EnhanceClean
	}
	if strength == 0 {
		strength = editspec.DefaultStrength(mode)
	}
	if in.Strength != nil {
		strength = math.Round(math.Max(0, math.Min(1, *in.Strength))*100) / 100
	}
	off := curOff
	if in.Off != nil {
		off = *in.Off
	} else if in.Mode != "" {
		off = false
	}
	out := &EnhanceOut{Target: t.words, Mode: mode, Strength: strength, Off: off, Copies: []CopyView{}, Scores: []PersonScore{}}
	eng := newEngine(a, c.rev.ProjectID)
	version := eng.EngineVersion(ctx, mode)
	var jobs []*copyJob
	wets := map[string]*wetJob{}
	byAsset := map[string]*copyJob{}
	if !in.Remove {
		for _, r := range refs {
			if j := byAsset[r.asset]; j != nil {
				j.beats = append(j.beats, r.beat)
				continue
			}
			as := c.asset(r.asset)
			if as == nil {
				return nil, apperr.New(apperr.NotFound, "A narration recording this cut uses is missing.").WithDetails(map[string]any{"assetId": r.asset})
			}
			dur := as.Media.Duration
			end := math.Ceil(dur)
			key := WetKey(as.SHA256, mode, version, 0, dur)
			wets[key] = &wetJob{key: key, asset: as, mode: mode, from: 0, to: dur}
			p := Piece{From: 0, To: dur, Mode: mode, Strength: strength, Wet: key}
			j := &copyJob{win: window{asset: as.ID, start: 0, end: end}, asset: as, channels: 1, pieces: []Piece{p}, beats: []string{r.beat}}
			j.key = CopyKey(as.SHA256, 0, end, 1, j.pieces)
			if r, _ := cachedCopy(ctx, a, j.key); r != nil {
				j.cached = true
			}
			byAsset[r.asset] = j
			jobs = append(jobs, j)
		}
		for k, wj := range wets {
			if rec, _ := findRecord(ctx, a, "wet", k); rec != nil {
				wj.cached = true
			}
		}
		needed := map[string]bool{}
		for _, j := range jobs {
			if !j.cached {
				needed[j.pieces[0].Wet] = true
			}
		}
		for k := range wets {
			if !needed[k] {
				delete(wets, k)
			}
		}
	}
	out.Estimate = estimate(ctx, a, eng, c.rev.ProjectID, jobs, wets)
	if in.Estimate {
		out.Summary = out.Estimate.Summary
		if !out.Estimate.Allowed {
			out.Summary += " " + out.Estimate.Problem
		}
		return out, nil
	}
	if !out.Estimate.Allowed {
		out.Estimate.recordKept(ctx, a) // asked for, not estimated (D290)
		return nil, out.Estimate.refused()
	}
	return voiceover.TrackWork(ctx, a, ActionEnhance, c.rev.ID, "", func(ctx context.Context) (*EnhanceOut, error) {
		if err := setUpFirst(ctx, a, eng.Comp, out.Estimate); err != nil {
			return nil, err
		}
		views, err := makeAll(ctx, a, eng, newQuality(a, eng.Comp), c, jobs, wets, true, nil)
		if err != nil {
			return nil, err
		}
		out.Copies = views
		variant := map[string]string{}
		for i, j := range jobs {
			variant[j.asset.ID] = views[i].Variant
		}
		var eops []editspec.Op
		for _, r := range refs {
			tk := r.take
			if in.Remove {
				tk.Processed, tk.UseProcessed = nil, false
			} else {
				tk.Processed = &editspec.ProcessedRef{AssetID: variant[r.asset], Preset: PresetFor(mode, strength)}
				tk.UseProcessed = !off
			}
			if r.beat != "" {
				eops = append(eops, editspec.Op{Op: "updateBeat", ID: r.beat, Set: jsonSet(map[string]any{"take": voiceover.TakeSet(&tk)})})
				continue
			}
			var processed any
			if tk.Processed != nil {
				processed = map[string]any{"assetId": tk.Processed.AssetID, "preset": tk.Processed.Preset, "offset": 0}
			}
			eops = append(eops, editspec.Op{Op: "updateVoiceover", ID: "voiceover", Set: jsonSet(map[string]any{"continuous": map[string]any{"processed": processed, "useProcessed": tk.UseProcessed}})})
		}
		out.Scores = mergeScores(views)
		for _, s := range out.Scores {
			out.Warnings = append(out.Warnings, s.Warnings...)
		}
		words := fmt.Sprintf("the narration, %s, %d%%", modeWords(mode), int(math.Round(strength*100)))
		if in.Remove {
			words = "the narration as recorded"
		} else if off {
			words = "the narration plays the original recording"
		}
		res, note, err := planCheckAfterWork(ctx, a, c.rev, "Clearer voice: "+words, eops)
		if err != nil {
			return nil, err
		}
		out.Result = res
		vs := &editspec.VoiceSound{Enhance: &editspec.Enhance{Mode: mode, Strength: &strength, Off: off}}
		if in.Remove {
			vs = nil
		}
		out.Summary = enhanceSummary("the narration", vs, in, out)
		if note != "" {
			out.Summary += " " + note
			out.stale = true
		} else if in.Apply {
			out.Applied = applyResult(ctx, a, res)
			out.Summary += appliedNote(out.Applied)
		} else {
			out.Summary += " Apply the change to put it in the cut."
		}
		return out, nil
	})
}

// planCheck builds an ops plan against the revision and dry-runs it with
// edit.check (diff, warnings, confirm token); nothing changes until applied.
func planCheck(ctx context.Context, a *app.App, rev *models.Revision, instruction string, eops []editspec.Op) (*voiceover.PlanResult, error) {
	if len(eops) == 0 {
		return nil, nil
	}
	if r := []rune(instruction); len(r) > 4000 {
		instruction = string(r[:3999]) + "…"
	}
	plan := editspec.Plan{BaseRevisionID: rev.ID, Branch: rev.Branch, Instruction: instruction, Ops: eops}
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	out, err := ops.CallTyped[ops.EditCheckOut](ctx, a, "edit.check", ops.EditCheckIn{Plan: raw}, ops.Caller{})
	if err != nil {
		return nil, err
	}
	return &voiceover.PlanResult{Plan: plan, Check: out}, nil
}

// planCheckAfterWork is planCheck after audio was made: when the cut moved on
// meanwhile, the work is kept (cached, never billed again) and the person is
// told to run it again on the latest version.
func planCheckAfterWork(ctx context.Context, a *app.App, rev *models.Revision, instruction string, eops []editspec.Op) (*voiceover.PlanResult, string, error) {
	res, err := planCheck(ctx, a, rev, instruction, eops)
	if err != nil && apperr.Is(err, apperr.RevisionConflict) {
		return nil, "Someone else changed the cut meanwhile. The clearer voice is saved: do this again on the latest version to place it (it costs nothing again).", nil
	}
	return res, "", err
}

// applyResult saves a checked plan as a new version (with its own token).
func applyResult(ctx context.Context, a *app.App, res *voiceover.PlanResult) *voiceover.Applied {
	if res == nil {
		return nil
	}
	if res.Check == nil || !res.Check.Valid {
		msg := "the change didn't pass its check"
		if res.Check != nil && len(res.Check.Errors) > 0 {
			msg = res.Check.Errors[0].Message
		}
		return &voiceover.Applied{Problem: msg}
	}
	raw, err := json.Marshal(res.Plan)
	if err != nil {
		return &voiceover.Applied{Problem: err.Error()}
	}
	out, err := ops.CallTyped[ops.EditApplyOut](ctx, a, "edit.apply", ops.EditApplyIn{Plan: raw, ConfirmToken: res.Check.ConfirmToken}, ops.Caller{})
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) {
			return &voiceover.Applied{Problem: ae.Message}
		}
		return &voiceover.Applied{Problem: err.Error()}
	}
	return &voiceover.Applied{RevisionID: out.Revision.ID, ProjectID: out.Revision.ProjectID, Number: out.Revision.Number}
}

// plainFailure is a step that failed, said plainly: what didn't work and the
// first part of why ("Setting up clearer voices didn't work: could not
// download the voice cleaner."). The whole error stays under Details.
func plainFailure(err error, what string) *apperr.Error {
	var why string
	var ae *apperr.Error
	if errors.As(err, &ae) {
		why = ae.Message
	} else {
		why = firstClause(err.Error())
	}
	msg := what + "."
	if why = strings.TrimRight(strings.TrimSpace(why), "."); why != "" {
		msg = what + ": " + why + "."
	}
	return apperr.Wrap(apperr.ToolFailed, err, "%s", msg).WithDetails(map[string]any{"error": err.Error()})
}

// firstClause is the start of an error message, before its technical cause
// ("could not download the voice cleaner: Get https://…" → "could not
// download the voice cleaner").
func firstClause(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	s, _, _ = strings.Cut(s, ": ")
	s, _, _ = strings.Cut(s, " (")
	return s
}

func appliedNote(ap *voiceover.Applied) string {
	switch {
	case ap == nil:
		return ""
	case ap.Problem != "":
		return " It is not in the cut yet: " + strings.TrimRight(ap.Problem, ".") + "."
	}
	return fmt.Sprintf(" It is in the cut now as version %d.", ap.Number)
}
