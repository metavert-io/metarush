package voiceover

import (
	"context"
	"errors"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ops"
)

// MixSetIn changes a cut's sound mix: the dashboard's mixer and `metarush mix
// set`. Unset fields stay as they are. mix.set builds and checks the change as
// an ordinary edit plan (with apply it becomes a new version); mix.preview
// resolves the changed cut into the plan the dashboard plays, saving nothing.
type MixSetIn struct {
	RevisionID string      `json:"revisionId" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
	Buses      []BusChange `json:"buses,omitempty" jsonschema:"maxItems=8,description=Level\\, mute and solo of the clips' own sound (source)\\, narration (voiceover)\\, music and sound effects (sfx)."`
	Clips      []ClipSound `json:"clips,omitempty" jsonschema:"maxItems=500,description=Turn single clips' own sound off or back on."`
	MuteSource *bool       `json:"muteSource,omitempty" jsonschema:"description=Turn the clips' own sound (camera sound) off for the whole cut\\, or back on."`
	Ducking    *bool       `json:"ducking,omitempty" jsonschema:"description=Music and clip sound get quieter automatically while someone talks."`
	// Ducking depths (dB, negative).
	MusicUnderVoiceover  *float64 `json:"musicUnderVoiceoverDb,omitempty" jsonschema:"minimum=-40,maximum=0"`
	SourceUnderVoiceover *float64 `json:"sourceUnderVoiceoverDb,omitempty" jsonschema:"minimum=-60,maximum=0"`
	MusicUnderDialogue   *float64 `json:"musicUnderDialogueDb,omitempty" jsonschema:"minimum=-40,maximum=0"`
	// Overall loudness: a target (LUFS), or back to the platform's usual level.
	TargetLUFS   *float64 `json:"targetLufs,omitempty" jsonschema:"minimum=-31,maximum=-8"`
	LoudnessAuto bool     `json:"loudnessAuto,omitempty" jsonschema:"description=Use the usual level of the cut's platform again."`
	TruePeakDBTP *float64 `json:"truePeakDbtp,omitempty" jsonschema:"minimum=-9,maximum=0"`
	Normalize    *bool    `json:"normalize,omitempty"`
	LevelSpeech  *bool    `json:"levelSpeech,omitempty" jsonschema:"description=Even out how loud people sound from clip to clip."`
	CutBlend     *float64 `json:"cutBlend,omitempty" jsonschema:"minimum=0,maximum=0.5,description=Seconds the sound blends at straight cuts so they never click (0 = sharp)."`
	Reset        bool     `json:"reset,omitempty" jsonschema:"description=Levels\\, ducking and loudness back to normal (speech leveling and cut blends stay)."`
	Apply        bool     `json:"apply,omitempty" jsonschema:"description=mix.set only: put the change in the cut as a new version (applies the plan it checked)."`
}

// MixSetOut is the change in plain words and its checked plan.
type MixSetOut struct {
	Changes []string    `json:"changes"`
	Result  *PlanResult `json:"result,omitempty"`
	Applied *Applied    `json:"applied,omitempty"`
	Summary string      `json:"summary"`
}

// MixPreviewOut is how the changed cut would sound: the plan the dashboard
// preview plays (its gain envelopes) and the mix in plain words. Nothing is saved.
type MixPreviewOut struct {
	Changes []string            `json:"changes"`
	Plan    editspec.RenderPlan `json:"plan"`
	Mix     MixOut              `json:"mix"`
	Summary string              `json:"summary"`
}

func init() {
	ops.Def("mix.set", "Change a cut's sound mix: levels, mute and solo of the clips' sound, narration, music and effects, single clips' sound, ducking, loudness, speech leveling and cut blends (checks the plan; apply makes it a new version)", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *MixSetIn) (*MixSetOut, error) { return mixSet(ctx, a, in) })
	ops.Def("mix.preview", "Hear a sound mix change before saving it: the plan the preview plays with the change, and the mix in plain words (nothing is saved)", ops.Flags{},
		func(ctx context.Context, a *app.App, in *MixSetIn) (*MixPreviewOut, error) {
			return mixPreview(ctx, a, in)
		})
}

// Change is the MixChange the input asks for.
func (in *MixSetIn) Change() MixChange {
	ch := MixChange{Buses: in.Buses, Clips: in.Clips, Reset: in.Reset, Ducking: in.Ducking, MusicUnderVoice: in.MusicUnderVoiceover,
		SourceUnderVoice: in.SourceUnderVoiceover, MusicUnderDialogue: in.MusicUnderDialogue, Loudness: in.TargetLUFS, LoudnessAuto: in.LoudnessAuto,
		PeakCeiling: in.TruePeakDBTP, Normalize: in.Normalize, LevelSpeech: in.LevelSpeech, CutBlend: in.CutBlend}
	if in.MuteSource != nil {
		ch.MuteSource, ch.UnmuteSource = *in.MuteSource, !*in.MuteSource
	}
	return ch
}

func mixSet(ctx context.Context, a *app.App, in *MixSetIn) (*MixSetOut, error) {
	rc, err := load(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	plan, what, err := MixPlan(rc.rev, in.Change())
	if err != nil {
		return nil, err
	}
	out := &MixSetOut{Changes: what}
	if out.Result, err = planCheck(ctx, a, rc.rev, plan.Instruction, plan.Ops); err != nil {
		return nil, err
	}
	out.Summary = upper(strings.Join(what, ", ")) + "."
	if in.Apply {
		out.Applied = applyResult(ctx, a, out.Result)
		out.Summary += appliedNote(out.Applied)
	} else {
		out.Summary += hintApply
	}
	return out, nil
}

func mixPreview(ctx context.Context, a *app.App, in *MixSetIn) (*MixPreviewOut, error) {
	if in.Apply {
		return nil, apperr.New(apperr.InvalidInput, "A preview saves nothing. Save the change to put it in the cut.")
	}
	rc, err := load(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	plan, what, err := MixPlan(rc.rev, in.Change())
	if err != nil {
		return nil, err
	}
	spec, _, err := editspec.ApplyOps(&rc.rev.Spec, plan.Ops, false)
	if err != nil {
		var le *editspec.LockedError
		if errors.As(err, &le) {
			return nil, apperr.New(apperr.Locked, "%s", le.Error())
		}
		return nil, apperr.New(apperr.InvalidInput, "%s", err.Error())
	}
	editspec.Normalize(spec)
	pp, err := ops.PreviewPlan(ctx, a, rc.rev.ProjectID, rc.rev.ID, editspec.Hash(spec), spec)
	if err != nil {
		return nil, err
	}
	return &MixPreviewOut{Changes: what, Plan: *pp, Mix: *mixView(rc.rev.ID, spec, pp), Summary: "With this change: " + strings.Join(what, ", ") + ". Nothing is saved yet."}, nil
}
