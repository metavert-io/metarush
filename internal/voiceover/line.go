package voiceover

import (
	"context"
	"math"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// LineIn adds, changes, moves or removes one narration line (the dashboard's
// script editor and `metarush voiceover beat add|update|move|remove`).
type LineIn struct {
	RevisionID string `json:"revisionId" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
	Action     string `json:"action" jsonschema:"enum=add,enum=change,enum=move,enum=remove"`
	// Line names the line (ID or number counting from 1); not for add.
	Line string `json:"line,omitempty" jsonschema:"maxLength=64,description=Line ID or number (change\\, move\\, remove)."`
	// add: the new line's ID (default line-N).
	ID       string  `json:"id,omitempty" jsonschema:"pattern=^[a-z][a-z0-9_-]*$,maxLength=64"`
	Text     *string `json:"text,omitempty" jsonschema:"maxLength=2000"`
	Delivery *string `json:"delivery,omitempty" jsonschema:"maxLength=300,description=How to say it (shown to whoever records it\\, sent to the AI voice as context)."`
	State    *string `json:"state,omitempty" jsonschema:"enum=draft,enum=final"`
	// Voice is an ElevenLabs voice for this line only ("" = the project's voice again).
	Voice     *string `json:"voice,omitempty" jsonschema:"maxLength=120"`
	VoiceName string  `json:"voiceName,omitempty" jsonschema:"maxLength=120"`
	// Where it starts: a structured start point, or To — a point on the
	// timeline that is tied to what plays there (the shot, the moment or the
	// previous line it was tied to), or with Exact kept as a clock time.
	Anchor *editspec.Anchor `json:"anchor,omitempty"`
	To     *float64         `json:"to,omitempty" jsonschema:"minimum=0,description=Timeline seconds: start the line here\\, tied to the shot playing there."`
	Exact  bool             `json:"exact,omitempty" jsonschema:"description=With to: keep the line at that exact clock time."`
	// Script order (add, move): after or before another line.
	After  string `json:"after,omitempty" jsonschema:"maxLength=64"`
	Before string `json:"before,omitempty" jsonschema:"maxLength=64"`
	Lock   bool   `json:"lock,omitempty" jsonschema:"description=change: lock the line afterwards."`
	Apply  bool   `json:"apply,omitempty" jsonschema:"description=Put the change in the cut as a new version (applies the plan it checked)."`
}

// LineOut is the checked change (and the version it made, with apply).
type LineOut struct {
	Placement string      `json:"placement,omitempty" jsonschema:"description=Where the line starts now\\, in plain words (add\\, move)."`
	Result    *PlanResult `json:"result,omitempty"`
	Applied   *Applied    `json:"applied,omitempty"`
	Summary   string      `json:"summary"`
}

func init() {
	ops.Def("voiceover.line", "Add, change, move or remove one narration line (words, delivery notes, its own voice, where it starts — tied to a shot, a moment, the previous line or a clock time — or its place in the script), and check the plan", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *LineIn) (*LineOut, error) { return line(ctx, a, in) })
}

func line(ctx context.Context, a *app.App, in *LineIn) (*LineOut, error) {
	rc, err := load(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	r := rc.rev
	an := in.Anchor
	if in.To != nil {
		if an != nil {
			return nil, apperr.New(apperr.InvalidInput, "Give the line one start point: what it is tied to, or a time to move it to.")
		}
		idx := -1
		if in.Action != "add" {
			if idx, err = FindBeat(r, in.Line); err != nil {
				return nil, err
			}
		}
		na := ReanchorAt(rc.spec, rc.clips, rc.beats, idx, *in.To, in.Exact)
		an = &na
	}
	var plan editspec.Plan
	switch in.Action {
	case "add":
		text := ""
		if in.Text != nil {
			text = *in.Text
		}
		delivery := ""
		if in.Delivery != nil {
			delivery = *in.Delivery
		}
		plan, err = BeatAddPlan(r, text, in.ID, delivery, beatRef(r, in.After), beatRef(r, in.Before), an)
	case "change":
		plan, err = BeatUpdatePlan(r, in.Line, BeatChange{Text: in.Text, Delivery: in.Delivery, State: in.State, Voice: in.Voice, VoiceName: in.VoiceName, Lock: in.Lock})
	case "move":
		plan, err = BeatMovePlan(r, in.Line, an, in.After, in.Before)
	case "remove":
		plan, err = BeatRemovePlan(r, in.Line)
	default:
		return nil, apperr.New(apperr.InvalidInput, "A narration line can be added, changed, moved or removed, not “%s”.", in.Action)
	}
	if err != nil {
		return nil, err
	}
	out := &LineOut{}
	switch {
	case in.Action == "add" && len(plan.Ops) > 0 && plan.Ops[0].Beat != nil:
		out.Placement = Placement(rc.spec, plan.Ops[0].Beat.Anchor)
	case an != nil:
		out.Placement = Placement(rc.spec, *an)
	}
	if out.Result, err = planCheck(ctx, a, r, plan.Instruction, plan.Ops); err != nil {
		return nil, err
	}
	out.Summary = strings.TrimRight(plan.Instruction, ".") + "."
	if out.Placement != "" {
		out.Summary += " It starts " + out.Placement + "."
	}
	if in.Apply {
		out.Applied = applyResult(ctx, a, out.Result)
		out.Summary += appliedNote(out.Applied)
	} else {
		out.Summary += hintApply
	}
	return out, nil
}

// beatRef turns a line number into its ID (IDs pass through).
func beatRef(r *models.Revision, ref string) string {
	if ref == "" {
		return ""
	}
	if i, err := FindBeat(r, ref); err == nil {
		return r.Spec.Voiceover.Beats[i].ID
	}
	return ref
}

// ReanchorAt is the start point for a line placed at timeline second t: by
// default a conversational one that follows the edit — the moment or shot the
// line was already tied to when t is still inside it, the previous line when t
// is where that line ends (plus its pause), else the shot playing at t (the
// incoming one during a transition), with the offset rounded to a tenth of a
// second; with exact, a clock time. beat is the line's index (-1 for a new line).
func ReanchorAt(s *editspec.Spec, clips []editspec.PlanClip, beats []editspec.ResolvedBeat, beat int, t float64, exact bool) editspec.Anchor {
	fps := float64(s.Output.FPS)
	t = math.Max(0, t)
	if exact {
		at := round1(t)
		return editspec.Anchor{Kind: editspec.AnchorTime, At: &at}
	}
	f := int(math.Round(t * fps))
	var cur *editspec.Anchor
	if beat >= 0 && s.Voiceover != nil && beat < len(s.Voiceover.Beats) {
		a := s.Voiceover.Beats[beat].Anchor
		cur = &a
	}
	inClip := func(pc editspec.PlanClip) bool { return f >= pc.From && f < pc.From+pc.DurationInFrames }
	if cur != nil {
		switch cur.Kind {
		case editspec.AnchorMoment:
			// Still inside the clip that shows the moment: keep "… after <moment>".
			if beat < len(beats) && beats[beat].Problem == "" {
				for _, pc := range clips {
					if inClip(pc) && pc.AssetID == cur.AssetID && cur.At != nil && *cur.At >= pc.SourceIn && *cur.At < pc.SourceOut {
						a := *cur
						a.Offset = round1(t - (float64(beats[beat].AnchorFrame)/fps - cur.Offset))
						if math.Abs(a.Offset) <= 60 {
							return a
						}
					}
				}
			}
		case editspec.AnchorAfter:
			// Dropped where the previous line ends (plus its pause): still "after".
			if beat > 0 && beat < len(beats) && beats[beat].Problem == "" {
				if math.Abs(float64(f-beats[beat].AnchorFrame)) <= 0.15*fps {
					return *cur
				}
			}
		}
	}
	var hit *editspec.PlanClip
	for i := range clips {
		pc := &clips[i]
		if inClip(*pc) && (hit == nil || pc.From >= hit.From) {
			hit = pc // the latest-starting clip covering t: the incoming one in a transition
		}
	}
	if hit == nil && len(clips) > 0 && f >= clips[len(clips)-1].From {
		hit = &clips[len(clips)-1] // at (or past) the very end
	}
	if hit == nil {
		at := round1(t)
		return editspec.Anchor{Kind: editspec.AnchorTime, At: &at}
	}
	off := round1(t - float64(hit.From)/fps)
	if math.Abs(off) < 0.05 {
		off = 0
	}
	return editspec.Anchor{Kind: editspec.AnchorClip, ClipID: hit.ID, Offset: off}
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
