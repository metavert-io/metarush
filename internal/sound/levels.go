package sound

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// LevelsIn changes how loud things are and the automatic help, all at once (the
// Sound tab's draft, `metarush sound levels`): the parts (camera sound,
// narration, music, effects), single people, single clips' camera sound, music
// getting quieter while someone talks, voices evened out, smooth cuts, the
// overall loudness, and each music track's own level and dip. Unset fields stay
// as they are. sound.levels builds and checks the change (apply saves it);
// sound.preview answers with the plan the preview plays, saving nothing.
type LevelsIn struct {
	RevisionID  string                `json:"revisionId" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
	Parts       []voiceover.BusChange `json:"parts,omitempty" jsonschema:"maxItems=8,description=source (camera sound)\\, voiceover (narration)\\, music\\, sfx (effects): level\\, mute\\, solo."`
	People      []PersonLevel         `json:"people,omitempty" jsonschema:"maxItems=50,description=Single people's level over their speech."`
	Clips       []voiceover.ClipSound `json:"clips,omitempty" jsonschema:"maxItems=500"`
	MuteSource  *bool                 `json:"muteSource,omitempty" jsonschema:"description=Turn the camera sound off for the whole video (or back on)."`
	Ducking     *bool                 `json:"ducking,omitempty" jsonschema:"description=Music gets quieter when someone talks."`
	Depth       *float64              `json:"depth,omitempty" jsonschema:"minimum=2,maximum=40,description=How much quieter the music gets while someone talks (dB)."`
	LevelSpeech *bool                 `json:"levelSpeech,omitempty" jsonschema:"description=Even out voices from clip to clip."`
	SmoothCuts  *bool                 `json:"smoothCuts,omitempty" jsonschema:"description=Blend the sound for a moment at each cut so it never clicks."`
	Loudness    string                `json:"loudness,omitempty" jsonschema:"maxLength=8,description=auto (what the platform expects) or a target such as -14 (social video)\\, -16 (web)\\, -23 (broadcast)."`
	Music       []MusicLevel          `json:"music,omitempty" jsonschema:"maxItems=20,description=A music track's own level and whether it dips while someone talks."`
	// Original (sound.preview only): hear every voice as recorded — the clearer
	// versions switched off in the preview (the A/B).
	Original bool `json:"original,omitempty" jsonschema:"description=sound.preview only: hear every voice as recorded (A/B against the clearer versions)."`
	Apply    bool `json:"apply,omitempty" jsonschema:"description=sound.levels only: put the change in the cut as a new version."`
}

// PersonLevel is one person's level over their speech.
type PersonLevel struct {
	Name   string  `json:"name" jsonschema:"minLength=1,maxLength=120"`
	GainDB float64 `json:"gainDb" jsonschema:"minimum=-30,maximum=12,description=dB over their speech (0 = as recorded)."`
}

// MusicLevel is a music track's own level and dip.
type MusicLevel struct {
	Track  string   `json:"track" jsonschema:"minLength=1,maxLength=200"`
	GainDB *float64 `json:"gainDb,omitempty" jsonschema:"minimum=-60,maximum=12"`
	Duck   string   `json:"duck,omitempty" jsonschema:"enum=on,enum=off,enum=auto"`
	DuckDB *float64 `json:"duckDb,omitempty" jsonschema:"minimum=-30,maximum=0"`
}

// LevelsOut is the change in plain words and its checked plan.
type LevelsOut struct {
	Changes []string              `json:"changes"`
	Result  *voiceover.PlanResult `json:"result,omitempty"`
	Applied *voiceover.Applied    `json:"applied,omitempty"`
	Summary string                `json:"summary"`
}

// PreviewOut is how the changed cut would sound (nothing is saved).
type PreviewOut struct {
	Changes []string            `json:"changes"`
	Plan    editspec.RenderPlan `json:"plan"`
	Mix     voiceover.MixOut    `json:"mix"`
	Summary string              `json:"summary"`
}

// levelsPlan turns a levels change into ops (clip mutes, one setMix, music updates).
func levelsPlan(rev *models.Revision, in *LevelsIn, title func(trackID string) string) ([]editspec.Op, []string, error) {
	ch := voiceover.MixChange{Buses: in.Parts, Clips: in.Clips, Ducking: in.Ducking, LevelSpeech: in.LevelSpeech}
	if in.MuteSource != nil {
		ch.MuteSource, ch.UnmuteSource = *in.MuteSource, !*in.MuteSource
	}
	if in.Depth != nil {
		d := math.Abs(*in.Depth)
		mv, md := -d, -math.Max(0, d-2) // speech on camera dips a little less than narration
		ch.MusicUnderVoice, ch.MusicUnderDialogue = &mv, &md
	}
	if in.SmoothCuts != nil {
		b := 0.0
		if *in.SmoothCuts {
			b = editspec.DefaultCutBlend
		}
		ch.CutBlend = &b
	}
	switch l := strings.TrimSpace(in.Loudness); l {
	case "":
	case "auto":
		ch.LoudnessAuto = true
	default:
		v, err := strconv.ParseFloat(l, 64)
		if err != nil || v > -8 || v < -31 {
			return nil, nil, apperr.New(apperr.InvalidInput, "Choose how loud the whole video sounds: what the platform expects, or a level between -31 and -8 (-14 is usual for social video).")
		}
		ch.Loudness = &v
	}
	var eops []editspec.Op
	var what []string
	var mix *editspec.Mix
	mixChanged := false
	if len(ch.Buses) > 0 || len(ch.Clips) > 0 || ch.MuteSource || ch.UnmuteSource || ch.Ducking != nil || ch.MusicUnderVoice != nil ||
		ch.LevelSpeech != nil || ch.CutBlend != nil || ch.Loudness != nil || ch.LoudnessAuto {
		plan, w, err := voiceover.MixPlan(rev, ch)
		if err != nil {
			return nil, nil, err
		}
		for _, op := range plan.Ops {
			if op.Op == "setMix" {
				mix, mixChanged = op.Mix, true
				continue
			}
			eops = append(eops, op)
		}
		what = append(what, w...)
	}
	if len(in.People) > 0 {
		if !mixChanged {
			mix = cloneMix(rev.Spec.Mix)
		} else if mix == nil {
			mix = &editspec.Mix{}
		}
		for _, p := range in.People {
			name := strings.TrimSpace(p.Name)
			if name == "" {
				return nil, nil, apperr.New(apperr.InvalidInput, "Say whose voice level to change.")
			}
			g := math.Round(math.Max(-30, math.Min(12, p.GainDB))*10) / 10
			cur := mix.VoiceFor(name)
			var v *editspec.VoiceSound
			if cur != nil {
				x := *cur
				v = &x
			} else {
				v = &editspec.VoiceSound{Name: name}
			}
			if g == 0 {
				v.GainDB = nil
			} else {
				v.GainDB = &g
			}
			if v.GainDB == nil && v.Enhance == nil {
				v = nil
			}
			mix = withVoice(mix, name, "", v)
			what = append(what, personLevelWords(name, g))
		}
		mixChanged = true
	}
	if mixChanged {
		if mix != nil && mixEmpty(mix) {
			mix = nil
		}
		eops = append(eops, editspec.Op{Op: "setMix", Mix: mix})
	}
	for _, ml := range in.Music {
		idx := -1
		for i, t := range rev.Spec.Audio {
			if t.ID == ml.Track {
				idx = i
			}
		}
		if idx < 0 {
			return nil, nil, apperr.New(apperr.NotFound, "There is no music or sound called “%s” in this cut.", ml.Track)
		}
		set := map[string]any{}
		if ml.GainDB != nil {
			g := math.Round(*ml.GainDB*10) / 10
			set["gainDb"] = g
			what = append(what, fmt.Sprintf("set the music %s to %s", quote(title(ml.Track)), signedDB(g)))
		}
		switch ml.Duck {
		case "on":
			set["duck"] = true
			what = append(what, "make "+quote(title(ml.Track))+" quieter while someone talks")
		case "off":
			set["duck"] = false
			what = append(what, "keep "+quote(title(ml.Track))+" at its level while people talk")
		case "auto":
			set["duck"] = nil
		}
		if ml.DuckDB != nil {
			set["duckDb"] = -math.Abs(*ml.DuckDB)
			what = append(what, fmt.Sprintf("dip %s by %.0f dB while someone talks", quote(title(ml.Track)), math.Abs(*ml.DuckDB)))
		}
		if len(set) > 0 {
			b, _ := json.Marshal(set)
			eops = append(eops, editspec.Op{Op: "updateAudio", ID: ml.Track, Set: b})
		}
	}
	if len(eops) == 0 {
		return nil, nil, apperr.New(apperr.InvalidInput, "Nothing to change in the sound.").WithHint("Choose what to change on the Sound tab.").WithCommand("metarush sound levels --help")
	}
	return eops, what, nil
}

func personLevelWords(name string, g float64) string {
	switch {
	case g > 0:
		return fmt.Sprintf("make %s louder (%s)", name, signedDB(g))
	case g < 0:
		return fmt.Sprintf("make %s quieter (%s)", name, signedDB(g))
	}
	return "put " + name + " back at their recorded level"
}

func signedDB(g float64) string {
	s := strconv.FormatFloat(math.Abs(g), 'f', -1, 64) + " dB"
	if g > 0 {
		return "+" + s
	}
	if g < 0 {
		return "-" + s
	}
	return s
}

func soundLevels(ctx context.Context, a *app.App, in *LevelsIn) (*LevelsOut, error) {
	if in.Original {
		return nil, apperr.New(apperr.InvalidInput, "Hearing every voice as recorded is only for the preview; it saves nothing.").
			WithHint("To keep a voice as it was recorded, switch its clearer voice off on the Sound tab.").WithCommand("metarush sound enhance <person> --off")
	}
	c, err := loadCut(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	eops, what, err := levelsPlan(c.rev, in, c.trackTitle)
	if err != nil {
		return nil, err
	}
	out := &LevelsOut{Changes: what}
	if out.Result, err = planCheck(ctx, a, c.rev, "Sound: "+strings.Join(what, ", "), eops); err != nil {
		return nil, err
	}
	out.Summary = upper(strings.Join(what, ", ")) + "."
	if in.Apply {
		out.Applied = applyResult(ctx, a, out.Result)
		out.Summary += appliedNote(out.Applied)
	} else {
		out.Summary += " Apply the change to put it in the cut."
	}
	return out, nil
}

func soundPreview(ctx context.Context, a *app.App, in *LevelsIn) (*PreviewOut, error) {
	if in.Apply {
		return nil, apperr.New(apperr.InvalidInput, "A preview saves nothing. Save the change to put it in the cut.")
	}
	c, err := loadCut(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	spec := editspec.Clone(&c.rev.Spec)
	var what []string
	if hasLevelChange(in) {
		eops, w, err := levelsPlan(c.rev, in, c.trackTitle)
		if err != nil {
			return nil, err
		}
		what = w
		next, _, err := editspec.ApplyOps(spec, eops, false)
		if err != nil {
			var le *editspec.LockedError
			if errors.As(err, &le) {
				return nil, apperr.New(apperr.Locked, "%s", le.Error())
			}
			return nil, apperr.New(apperr.InvalidInput, "%s", err.Error())
		}
		spec = next
	}
	if in.Original {
		withoutClearer(spec)
		what = append(what, "every voice as recorded")
	}
	editspec.Normalize(spec)
	pp, err := ops.PreviewPlan(ctx, a, c.rev.ProjectID, c.rev.ID, editspec.Hash(spec), spec)
	if err != nil {
		return nil, err
	}
	summary := "As saved."
	if len(what) > 0 {
		summary = "With this change: " + strings.Join(what, ", ") + ". Nothing is saved yet."
	}
	return &PreviewOut{Changes: what, Plan: *pp, Mix: *voiceover.MixView(c.rev.ID, spec, pp), Summary: summary}, nil
}

func hasLevelChange(in *LevelsIn) bool {
	return len(in.Parts) > 0 || len(in.People) > 0 || len(in.Clips) > 0 || in.MuteSource != nil || in.Ducking != nil || in.Depth != nil ||
		in.LevelSpeech != nil || in.SmoothCuts != nil || in.Loudness != "" || len(in.Music) > 0
}

// withoutClearer switches every clearer voice off in a spec (the preview's A/B):
// recordings play their own sound and narration lines their original takes.
func withoutClearer(s *editspec.Spec) {
	if s.Mix != nil {
		s.Mix.Enhanced = nil
	}
	if v := s.Voiceover; v != nil {
		for i := range v.Beats {
			if t := v.Beats[i].Take; t != nil && t.Processed != nil {
				if _, _, ok := ParsePreset(t.Processed.Preset); ok {
					t.UseProcessed = false
				}
			}
		}
		if cv := v.Continuous; cv != nil && cv.Processed != nil {
			if _, _, ok := ParsePreset(cv.Processed.Preset); ok {
				cv.UseProcessed = false
			}
		}
	}
}
