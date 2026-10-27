package voiceover

import (
	"context"
	"fmt"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
)

// Script versions (D86): the script exactly as the person gave it, and Claude's
// version fitted to the video, taken line by line. Both are kept; the one in use
// is the narration's lines, the other waits with its sound and places.

// FitProposal is Claude's fitted version of the script, as Claude writes it
// (a JSON file in its proposals folder, or in a terminal session).
type FitProposal struct {
	Lines []FitProposalLine `json:"lines" jsonschema:"maxItems=300"`
	Note  string            `json:"note,omitempty" jsonschema:"maxLength=2000,description=What Claude changed overall and why."`
}

// FitProposalLine is Claude's version of one line of the person's script.
type FitProposalLine struct {
	ID     string           `json:"id" jsonschema:"maxLength=64,description=The person's line (its ID or its number)."`
	Text   string           `json:"text" jsonschema:"maxLength=2000,description=Claude's words for the line (the person's words when only the timing changes)."`
	Anchor *editspec.Anchor `json:"anchor,omitempty" jsonschema:"description=Where the line starts (a shot, a moment, a clock time); leave out to follow the line before."`
	At     string           `json:"at,omitempty" jsonschema:"maxLength=20,description=Or a point of the video (m:ss.s): the line is tied to the shot playing there."`
	Reason string           `json:"reason,omitempty" jsonschema:"maxLength=600,description=Why it changed, in plain words."`
}

// FittedIn puts Claude's fitted version in use (a new proposal), or changes
// which of its lines the person keeps as they wrote them.
type FittedIn struct {
	RevisionID string       `json:"revisionId" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
	Proposal   *FitProposal `json:"proposal,omitempty" jsonschema:"description=Claude's fitted version (leave out to change which of its lines you keep)."`
	Keep       []string     `json:"keep,omitempty" jsonschema:"maxItems=300,description=Lines (IDs or numbers) where you keep your own words and start."`
	Use        []string     `json:"use,omitempty" jsonschema:"maxItems=300,description=Lines where you take Claude's version (undoes keep)."`
	Apply      bool         `json:"apply,omitempty" jsonschema:"description=Put the change in the cut as a new version (applies the plan it checked)."`
}

// FitLineView is one line of the two versions side by side.
type FitLineView struct {
	BeatID   string `json:"beatId"`
	Line     int    `json:"line"`
	Original string `json:"original"`
	Text     string `json:"text"`
	// Words compares them word by word (same, del: only the person's, add: only Claude's).
	Words             []editspec.WordOp `json:"words"`
	Change            string            `json:"change" jsonschema:"enum=none,enum=words,enum=timing,enum=both"`
	OriginalPlacement string            `json:"originalPlacement"`
	Placement         string            `json:"placement"`
	Reason            string            `json:"reason,omitempty"`
	Kept              bool              `json:"kept,omitempty"`
	Locked            bool              `json:"locked,omitempty"`
}

// FittedOut is the comparison and the plan that makes the change.
type FittedOut struct {
	Lines   []FitLineView `json:"lines"`
	Changed int           `json:"changed"`
	Kept    int           `json:"kept"`
	Note    string        `json:"note,omitempty"`
	// Active is the version in use after the change.
	Active   string      `json:"active" jsonschema:"enum=original,enum=fitted"`
	Warnings []string    `json:"warnings,omitempty"`
	Result   *PlanResult `json:"result,omitempty"`
	Applied  *Applied    `json:"applied,omitempty"`
	Summary  string      `json:"summary"`
}

// UseIn switches the script version in use.
type UseIn struct {
	RevisionID string `json:"revisionId" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
	Version    string `json:"version" jsonschema:"enum=original,enum=fitted,description=original: your script as you gave it; fitted: Claude's version fitted to the video."`
	Apply      bool   `json:"apply,omitempty"`
}

// UseOut is the switch and its plan.
type UseOut struct {
	Active string `json:"active" jsonschema:"enum=original,enum=fitted"`
	// AudioKept: lines of the version set aside that play audio; it stays with
	// that version (switching back brings it back).
	AudioKept int         `json:"audioKept,omitempty"`
	Result    *PlanResult `json:"result,omitempty"`
	Applied   *Applied    `json:"applied,omitempty"`
	Summary   string      `json:"summary"`
}

func fitted(ctx context.Context, a *app.App, in *FittedIn) (*FittedOut, error) {
	rc, err := load(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	v := rc.spec.Voiceover
	if v == nil || len(v.Beats) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "This cut has no narration script to fit yet.").WithHint("Paste or write a script on the Voiceover tab first.").
			WithCommand(fmt.Sprintf("metarush voiceover script import <file> --revision %s", rc.rev.ID))
	}
	orig := v.OriginalLines()
	out := &FittedOut{Lines: []FitLineView{}}
	var f *editspec.FittedScript
	if in.Proposal != nil {
		var warn []string
		if f, warn, err = buildFitted(rc, orig, in.Proposal); err != nil {
			return nil, err
		}
		out.Warnings = append(out.Warnings, warn...)
	} else {
		if v.Fitted == nil {
			return nil, notFitted(rc)
		}
		f = editspec.Clone(&editspec.Spec{Voiceover: &editspec.Voiceover{Fitted: v.Fitted}}).Voiceover.Fitted
	}
	toggled := map[string]bool{}
	for _, list := range []struct {
		refs []string
		keep bool
	}{{in.Keep, true}, {in.Use, false}} {
		for _, ref := range list.refs {
			i := beatIndex(orig, ref)
			if i < 0 {
				return nil, apperr.New(apperr.NotFound, "Your script has no line “%s”.", ref)
			}
			fl := f.Line(orig[i].ID)
			if fl == nil {
				continue
			}
			if fl.Kept != list.keep {
				fl.Kept = list.keep
				toggled[fl.ID] = true
			}
		}
	}
	nv := *editspec.Clone(&editspec.Spec{Voiceover: v}).Voiceover
	nv.Fitted = f
	var eops []editspec.Op
	instruction := ""
	switch {
	case in.Proposal != nil:
		beats := editspec.FittedBeats(orig, f)
		if v.ActiveVersion() == editspec.VersionFitted {
			// A new fitted version replaces the old one; lines whose words stay
			// keep the sound and place they had in it.
			for k := range beats {
				if j := beatIndex(v.Beats, beats[k].ID); j >= 0 && editspec.SameWords(v.Beats[j].Text, beats[k].Text) {
					beats[k].Take, beats[k].Anchor, beats[k].State = v.Beats[j].Take, v.Beats[j].Anchor, v.Beats[j].State
				}
			}
		} else {
			nv.Parked = orig
		}
		nv.Beats, nv.Active = beats, editspec.VersionFitted
		eops = []editspec.Op{{Op: "setScript", Voiceover: &nv}}
		instruction = fmt.Sprintf("Use Claude's version of the script fitted to the video (%s changed)", plural(f.Changed(), "line"))
	case len(toggled) == 0:
		return fittedView(rc, orig, f, out, v.ActiveVersion()), nil
	default:
		set := map[string]any{"fitted": f}
		origByID := map[string]*editspec.Beat{}
		for i := range orig {
			origByID[orig[i].ID] = &orig[i]
		}
		if v.ActiveVersion() == editspec.VersionFitted {
			for _, b := range v.Beats {
				if !toggled[b.ID] || b.Locked {
					continue
				}
				fl := f.Line(b.ID)
				nb := editspec.SwitchLine(b, origByID[b.ID], fl, fl.Kept)
				eops = append(eops, setOp(b.ID, map[string]any{"text": nb.Text, "anchor": nb.Anchor}))
			}
		} else {
			parked := append([]editspec.Beat(nil), nv.Parked...)
			for k, b := range parked {
				if fl := f.Line(b.ID); fl != nil && toggled[b.ID] && !b.Locked {
					parked[k] = editspec.SwitchLine(b, origByID[b.ID], fl, fl.Kept)
				}
			}
			set["parked"] = parked
		}
		raw, _ := jsonRaw(set)
		eops = append([]editspec.Op{{Op: "updateVoiceover", Set: raw}}, eops...)
		instruction = fmt.Sprintf("Change which lines of Claude's fitted script you keep as you wrote them (%s)", plural(len(toggled), "line"))
	}
	if out.Result, err = planCheck(ctx, a, rc.rev, instruction, eops); err != nil {
		return nil, err
	}
	active := v.ActiveVersion()
	if in.Proposal != nil {
		active = editspec.VersionFitted
	}
	fittedView(rc, orig, f, out, active)
	if in.Apply {
		out.Applied = applyResult(ctx, a, out.Result)
		out.Summary += appliedNote(out.Applied)
	} else {
		out.Summary += hintApply
	}
	return out, nil
}

// fittedView fills the side-by-side comparison and its summary.
func fittedView(rc *revCtx, orig []editspec.Beat, f *editspec.FittedScript, out *FittedOut, active string) *FittedOut {
	out.Note, out.Active = f.Note, active
	for i, b := range orig {
		fl := f.Line(b.ID)
		lv := FitLineView{BeatID: b.ID, Line: i + 1, Original: b.Text, Text: b.Text, OriginalPlacement: versionPlacement(rc.spec, b.Anchor, i == 0), Locked: b.Locked}
		lv.Placement = lv.OriginalPlacement
		if fl != nil {
			lv.Text, lv.Reason, lv.Kept = fl.Text, fl.Reason, fl.Kept || b.Locked
			if fl.Anchor != nil {
				lv.Placement = versionPlacement(rc.spec, *fl.Anchor, i == 0)
			} else {
				lv.Placement = versionPlacement(rc.spec, editspec.Anchor{Kind: editspec.AnchorAfter}, i == 0)
			}
			words := !editspec.SameWords(b.Text, fl.Text)
			timing := (fl.Anchor == nil && b.Anchor.Kind != editspec.AnchorAfter) || (fl.Anchor != nil && !sameAnchor(*fl.Anchor, b.Anchor))
			switch {
			case words && timing:
				lv.Change = "both"
			case words:
				lv.Change = "words"
			case timing:
				lv.Change = "timing"
			default:
				lv.Change = "none"
			}
		} else {
			lv.Change = "none"
		}
		lv.Words = editspec.DiffWords(lv.Original, lv.Text)
		if lv.Change != "none" {
			out.Changed++
			if lv.Kept {
				out.Kept++
			}
		}
		out.Lines = append(out.Lines, lv)
	}
	switch {
	case out.Changed == 0:
		out.Summary = "Claude's version is the same as your script."
	case out.Kept == 0:
		out.Summary = fmt.Sprintf("Claude fitted your script to the video: %s of %d changed.", plural(out.Changed, "line"), len(orig))
	default:
		out.Summary = fmt.Sprintf("Claude fitted your script to the video: %s of %d changed; you keep your own words on %d.", plural(out.Changed, "line"), len(orig), out.Kept)
	}
	out.Summary += " Your original script is kept."
	return out
}

// buildFitted turns Claude's proposal into the fitted version of the person's
// lines: one entry per line, in script order; a line Claude left out stays the
// person's own.
func buildFitted(rc *revCtx, orig []editspec.Beat, p *FitProposal) (*editspec.FittedScript, []string, error) {
	if len(p.Lines) == 0 {
		return nil, nil, apperr.New(apperr.Validation, "Claude's fitted script has no lines")
	}
	if len(p.Lines) > 300 {
		return nil, nil, apperr.New(apperr.Validation, "Claude's fitted script has more than 300 lines")
	}
	byID := map[string]FitProposalLine{}
	for _, l := range p.Lines {
		i := beatIndex(orig, strings.TrimSpace(l.ID))
		if i < 0 {
			return nil, nil, apperr.New(apperr.Validation, "Claude's fitted script names a line your script doesn't have (%q); each entry fits one of your lines, by its ID or number", l.ID)
		}
		id := orig[i].ID
		if _, dup := byID[id]; dup {
			return nil, nil, apperr.New(apperr.Validation, "Claude's fitted script has line %d twice", i+1)
		}
		byID[id] = l
	}
	var warn []string
	f := &editspec.FittedScript{Note: clipText(strings.TrimSpace(p.Note), 2000)}
	idx := map[string]int{}
	for i, b := range rc.spec.Voiceover.Beats {
		idx[b.ID] = i
	}
	for i, b := range orig {
		fl := editspec.FittedLine{ID: b.ID, Original: b.Text, Text: b.Text}
		pl, ok := byID[b.ID]
		if ok {
			if t := strings.Join(strings.Fields(pl.Text), " "); t != "" {
				fl.Text = clipText(t, 2000)
			} else {
				warn = append(warn, fmt.Sprintf("Claude gave %s no words, so it keeps yours", lineRef(i, b)))
			}
			fl.Reason = clipText(strings.TrimSpace(pl.Reason), 600)
			an := pl.Anchor
			if an == nil && strings.TrimSpace(pl.At) != "" {
				t, okT := editspec.ParseClock(pl.At)
				if !okT {
					warn = append(warn, fmt.Sprintf("Claude gave %s a start that isn't a time (“%s”), so it follows the line before", lineRef(i, b), pl.At))
				} else {
					j, inCut := idx[b.ID]
					if !inCut {
						j = -1
					}
					na := ReanchorAt(rc.spec, rc.clips, rc.beats, j, t, false)
					an = &na
				}
			}
			if an != nil {
				if an.Kind == editspec.AnchorAfter && an.Gap == nil && an.Offset == 0 {
					an = nil
				} else if _, prob := editspec.AnchorFrame(rc.spec, rc.clips, *an, 0); prob != "" {
					warn = append(warn, fmt.Sprintf("Claude's start for %s can't be placed (%s), so it follows the line before", lineRef(i, b), prob))
					an = nil
				}
			}
			fl.Anchor = an
		}
		f.Lines = append(f.Lines, fl)
	}
	return f, warn, nil
}

func use(ctx context.Context, a *app.App, in *UseIn) (*UseOut, error) {
	rc, err := load(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	v := rc.spec.Voiceover
	if v == nil {
		return nil, apperr.New(apperr.InvalidInput, "This cut has no narration.")
	}
	if in.Version != editspec.VersionOriginal && in.Version != editspec.VersionFitted {
		return nil, apperr.New(apperr.InvalidInput, "Choose your original script or Claude's version fitted to the video.").WithDetails(map[string]any{"versions": []string{editspec.VersionOriginal, editspec.VersionFitted}})
	}
	if v.ActiveVersion() == in.Version {
		return nil, apperr.New(apperr.InvalidInput, "The narration already uses %s.", versionWords(in.Version))
	}
	nv := *editspec.Clone(&editspec.Spec{Voiceover: v}).Voiceover
	switch in.Version {
	case editspec.VersionFitted:
		if v.Fitted == nil {
			return nil, notFitted(rc)
		}
		beats := nv.Parked
		if len(beats) == 0 {
			beats = editspec.FittedBeats(nv.Beats, nv.Fitted)
		}
		nv.Parked, nv.Beats, nv.Active = nv.Beats, beats, editspec.VersionFitted
	default:
		beats := nv.Parked
		if len(beats) == 0 {
			if strings.TrimSpace(v.Original) == "" {
				return nil, apperr.New(apperr.InvalidInput, "Your original script isn't kept with this cut.")
			}
			var issues []editspec.Issue
			beats, issues = editspec.ParseScriptAsIs(v.Original, rc.spec)
			if editspec.HasErrors(issues) || len(beats) == 0 {
				return nil, apperr.New(apperr.Validation, "MetaRush couldn't read your original script back into lines.")
			}
		}
		nv.Parked, nv.Beats, nv.Active = nv.Beats, beats, ""
	}
	out := &UseOut{Active: in.Version}
	if out.Result, err = planCheck(ctx, a, rc.rev, "Use "+versionWords(in.Version), []editspec.Op{{Op: "setScript", Voiceover: &nv}}); err != nil {
		return nil, err
	}
	// Each version keeps its own audio and places: say so when the lines
	// coming back have less audio than the ones being set aside.
	note := ""
	if had, has := withAudio(nv.Parked), withAudio(nv.Beats); had > has {
		out.AudioKept = had
		note = " The audio of the other version stays with it; switch back to hear it."
	}
	out.Summary = "This switches to " + versionWords(in.Version) + "; the other version is kept." + note + hintApply
	if in.Apply {
		out.Applied = applyResult(ctx, a, out.Result)
		if out.Applied != nil && out.Applied.Problem == "" {
			out.Summary = upper(versionWords(in.Version)) + " is in use now; the other version is kept." + note
		}
		out.Summary = settle(out.Summary, out.Applied)
	}
	return out, nil
}

// notFitted is the error when Claude hasn't fitted the script yet.
func notFitted(rc *revCtx) *apperr.Error {
	return apperr.New(apperr.InvalidInput, "Claude hasn't fitted this script to the video yet.").
		WithHint("Choose “Fit it to the video with Claude” on the Voiceover tab first.").
		WithCommand(fmt.Sprintf("metarush voiceover script fit --revision %s", rc.rev.ID))
}

// withAudio counts the lines that play audio (a recording or an AI voice take).
func withAudio(beats []editspec.Beat) int {
	n := 0
	for _, b := range beats {
		if b.Take != nil {
			n++
		}
	}
	return n
}

// versionPlacement says where a line of one version starts; a line with no
// start point of its own (an as-is script) has no timing yet.
func versionPlacement(s *editspec.Spec, a editspec.Anchor, first bool) string {
	if a.Kind == editspec.AnchorAfter && a.Gap == nil && a.Offset == 0 {
		if first {
			return "when the video starts (no timing yet)"
		}
		return "after the line before (no timing yet)"
	}
	return Placement(s, a)
}

func versionWords(v string) string {
	if v == editspec.VersionFitted {
		return "Claude's version fitted to the video"
	}
	return "your original script"
}
