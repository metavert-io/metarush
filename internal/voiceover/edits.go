package voiceover

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// Plans for script and mix changes. The CLI and the dashboard assistant's
// action cards (built on the server from the command Claude asked for) use the
// same builders, so a card applies exactly what the command would.

// AnchorSpec is a start point as given on the command line: a shot (ID or
// label, ":end" for when it ends), a clock time, a moment in the footage
// (<asset-id>@<seconds>), or after the previous line.
type AnchorSpec struct {
	Clip, At, Moment, Quote string
	Offset                  float64
	AfterPrevious           bool
	Gap                     *float64 // with AfterPrevious: the pause
}

// Anchor resolves the start point against a cut; nil when none was given.
func (f AnchorSpec) Anchor(s *editspec.Spec) (*editspec.Anchor, error) {
	n := 0
	for _, set := range []bool{f.Clip != "", f.At != "", f.Moment != "", f.AfterPrevious} {
		if set {
			n++
		}
	}
	if n == 0 {
		return nil, nil
	}
	if n > 1 {
		return nil, apperr.New(apperr.InvalidInput, "Give the line one start point.")
	}
	switch {
	case f.Clip != "":
		ref := f.Clip
		if f.Offset != 0 {
			ref += fmt.Sprintf("%+g", f.Offset)
		}
		beats, issues := editspec.ParseScript("[@"+ref+"] x", s)
		if len(issues) > 0 || len(beats) == 0 {
			msg := fmt.Sprintf("There is no shot “%s” in this cut.", f.Clip)
			if len(issues) > 0 {
				msg = issues[0].Message
			}
			return nil, apperr.New(apperr.InvalidInput, "%s", msg)
		}
		a := beats[0].Anchor
		return &a, nil
	case f.At != "":
		t, ok := editspec.ParseClock(f.At)
		if !ok {
			return nil, apperr.New(apperr.InvalidInput, "MetaRush couldn't read the time “%s”. Write it in minutes and seconds, like 1:05.5.", f.At)
		}
		return &editspec.Anchor{Kind: editspec.AnchorTime, At: &t, Offset: f.Offset}, nil
	case f.Moment != "":
		asset, at, ok := strings.Cut(f.Moment, "@")
		v, err := strconv.ParseFloat(at, 64)
		if !ok || err != nil {
			return nil, apperr.New(apperr.InvalidInput, "MetaRush couldn't read that moment in the footage. Give the clip and the second in it where the line starts.").
				WithDetails(map[string]any{"format": "<clip-id>@<seconds>", "given": f.Moment})
		}
		return &editspec.Anchor{Kind: editspec.AnchorMoment, AssetID: asset, At: &v, Quote: f.Quote, Offset: f.Offset}, nil
	}
	a := &editspec.Anchor{Kind: editspec.AnchorAfter, Offset: f.Offset}
	if f.Gap != nil {
		g := *f.Gap
		a.Gap = &g
	}
	return a, nil
}

// NextBeatID is the first free line-N ID.
func NextBeatID(s *editspec.Spec) string {
	used := map[string]bool{}
	for _, c := range s.Clips {
		used[c.ID] = true
	}
	n := 0
	if s.Voiceover != nil {
		n = len(s.Voiceover.Beats)
		for _, b := range s.Voiceover.Beats {
			used[b.ID] = true
		}
	}
	for {
		n++
		id := fmt.Sprintf("line-%d", n)
		if !used[id] {
			return id
		}
	}
}

// FindBeat finds a line by ID or number (counting from 1).
func FindBeat(r *models.Revision, ref string) (int, error) {
	if r.Spec.Voiceover != nil {
		for i, b := range r.Spec.Voiceover.Beats {
			if b.ID == ref || strconv.Itoa(i+1) == ref {
				return i, nil
			}
		}
	}
	return -1, noLine(ref)
}

func basePlan(r *models.Revision, instruction string, eops ...editspec.Op) editspec.Plan {
	return editspec.Plan{BaseRevisionID: r.ID, Branch: r.Branch, Instruction: instruction, Ops: eops}
}

// BeatAddPlan adds a line; without a start point it follows the previous line.
func BeatAddPlan(r *models.Revision, text, id, delivery, after, before string, an *editspec.Anchor) (editspec.Plan, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return editspec.Plan{}, apperr.New(apperr.InvalidInput, "The line has no words. Write what the narrator says.").WithCommand("metarush voiceover beat add --text \"…\"")
	}
	if an == nil {
		an = &editspec.Anchor{Kind: editspec.AnchorAfter}
	}
	if id == "" {
		id = NextBeatID(&r.Spec)
	}
	b := editspec.Beat{ID: id, Text: text, Anchor: *an, Delivery: delivery, State: "draft"}
	return basePlan(r, "Add a narration line: "+truncate(b.Text, 120), editspec.Op{Op: "addBeat", Beat: &b, After: after, Before: before}), nil
}

// BeatChange is what to change on a line; nil fields stay as they are (an
// empty Voice goes back to the project's voice). VoiceName is the chosen
// voice's name, kept with it so people (and price cards) see the name.
type BeatChange struct {
	Text, Delivery, State, Voice *string
	VoiceName                    string
	Lock                         bool
}

// BeatUpdatePlan changes a line's words, delivery notes, state or voice.
func BeatUpdatePlan(r *models.Revision, ref string, ch BeatChange) (editspec.Plan, error) {
	i, err := FindBeat(r, ref)
	if err != nil {
		return editspec.Plan{}, err
	}
	set := map[string]any{}
	if ch.Text != nil {
		set["text"] = strings.TrimSpace(*ch.Text)
	}
	if ch.Delivery != nil {
		set["delivery"] = *ch.Delivery
	}
	if ch.State != nil {
		set["state"] = *ch.State
	}
	if ch.Voice != nil {
		if *ch.Voice == "" {
			set["voice"] = nil
		} else {
			// The whole voice is replaced (update ops merge objects, and the old
			// voice's name or model must not stay with the new one).
			var name any
			if n := strings.TrimSpace(ch.VoiceName); n != "" {
				name = n
			}
			set["voice"] = map[string]any{"provider": "elevenlabs", "voiceId": *ch.Voice, "name": name, "modelId": nil, "settings": nil}
		}
	}
	if len(set) == 0 {
		return editspec.Plan{}, apperr.New(apperr.InvalidInput, "Nothing to change on this line. Give it new words, notes on how to say it, a voice of its own, or mark it draft or final.").
			WithCommand("metarush voiceover beat update --help")
	}
	raw, _ := json.Marshal(set)
	id := r.Spec.Voiceover.Beats[i].ID
	eops := []editspec.Op{{Op: "updateBeat", ID: id, Set: raw}}
	if ch.Lock {
		eops = append(eops, editspec.Op{Op: "lock", ID: id})
	}
	return basePlan(r, "Change "+lineRef(i, r.Spec.Voiceover.Beats[i]), eops...), nil
}

// BeatMovePlan gives a line a new start point, or a new place in the script order.
func BeatMovePlan(r *models.Revision, ref string, an *editspec.Anchor, after, before string) (editspec.Plan, error) {
	i, err := FindBeat(r, ref)
	if err != nil {
		return editspec.Plan{}, err
	}
	if an == nil && after == "" && before == "" {
		return editspec.Plan{}, apperr.New(apperr.InvalidInput, "Say where the line goes: the shot it starts on, a time, a moment in the footage, right after the line before, or another place in the script.").
			WithCommand("metarush voiceover beat move --help")
	}
	resolve := func(ref string) string {
		if ref == "" {
			return ""
		}
		if j, err := FindBeat(r, ref); err == nil {
			return r.Spec.Voiceover.Beats[j].ID
		}
		return ref
	}
	return basePlan(r, "Move "+lineRef(i, r.Spec.Voiceover.Beats[i]),
		editspec.Op{Op: "moveBeat", ID: r.Spec.Voiceover.Beats[i].ID, Anchor: an, After: resolve(after), Before: resolve(before)}), nil
}

// BeatRemovePlan removes a line.
func BeatRemovePlan(r *models.Revision, ref string) (editspec.Plan, error) {
	i, err := FindBeat(r, ref)
	if err != nil {
		return editspec.Plan{}, err
	}
	return basePlan(r, "Remove "+lineRef(i, r.Spec.Voiceover.Beats[i]), editspec.Op{Op: "removeBeat", ID: r.Spec.Voiceover.Beats[i].ID}), nil
}

// DecodeScript reads a voiceover JSON document (unknown fields are refused).
func DecodeScript(raw []byte) (*editspec.Voiceover, error) {
	var v editspec.Voiceover
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return nil, apperr.New(apperr.InvalidInput, "That file isn't a narration script MetaRush can read.").WithDetails(map[string]any{"error": err.Error()})
	}
	return &v, nil
}

// ScriptSetPlan replaces the narration with a voiceover document.
func ScriptSetPlan(r *models.Revision, v *editspec.Voiceover) editspec.Plan {
	return basePlan(r, fmt.Sprintf("Set the narration script (%s)", plural(len(v.Beats), "line")), editspec.Op{Op: "setScript", Voiceover: v})
}

// MixChange is a change to the sound mix; nil fields stay as they are.
type MixChange struct {
	Bus                               string   // source | voiceover (narration) | music | sfx (effects)
	Clip                              string   // with MuteSource/UnmuteSource: only this clip (ID or label)
	Gain                              *float64 // with Bus: level change in dB
	Mute, Unmute, Solo, Unsolo        bool     // with Bus
	MuteSource, UnmuteSource, Reset   bool
	Ducking, Normalize                *bool
	MusicUnderVoice, SourceUnderVoice *float64 // dB
	MusicUnderDialogue                *float64 // dB
	Loudness, PeakCeiling             *float64 // overall level target; loudest peaks
	LoudnessAuto                      bool     // the overall level target from the cut's platform again
	LevelSpeech                       *bool    // even out speech loudness between clips
	CutBlend                          *float64 // seconds the sound blends at straight cuts
	// Buses and Clips change several parts at once (the dashboard's mixer):
	// each bus's level, mute and solo, and each clip's own sound on or off.
	Buses []BusChange
	Clips []ClipSound
}

// BusChange sets one mixer part; nil fields stay as they are.
type BusChange struct {
	Bus    string   `json:"bus" jsonschema:"enum=source,enum=voiceover,enum=music,enum=sfx,description=source = the clips' own sound (camera sound); voiceover = narration; sfx = sound effects."`
	GainDB *float64 `json:"gainDb,omitempty" jsonschema:"minimum=-60,maximum=12,description=Level in dB (0 = as recorded\\, negative = quieter)."`
	Mute   *bool    `json:"mute,omitempty"`
	Solo   *bool    `json:"solo,omitempty" jsonschema:"description=Hear only the soloed parts."`
}

// ClipSound turns one clip's own sound off (or back on).
type ClipSound struct {
	Clip  string `json:"clip" jsonschema:"maxLength=64,description=Clip ID or label."`
	Muted bool   `json:"muted"`
}

// mixPart names a part of the sound the way people do.
func mixPart(bus string) string {
	switch bus {
	case editspec.BusSource:
		return "the camera sound"
	case editspec.BusVoiceover, "narration":
		return "the narration"
	case editspec.BusMusic:
		return "the music"
	case editspec.BusSFX, "effects":
		return "the effects"
	}
	return bus
}

func dbAmount(v float64) string {
	return strconv.FormatFloat(math.Abs(v), 'f', -1, 64) + " dB"
}

// levelFor says what an overall loudness target is for, without the unit.
func levelFor(target float64) string {
	switch {
	case target >= -15 && target <= -13:
		return "social video"
	case target >= -17 && target < -15:
		return "the web"
	case target >= -25 && target <= -22:
		return "broadcast"
	}
	return "a custom level"
}

// loudnessWords says what setting the overall loudness target does.
func loudnessWords(target float64) string {
	if l := levelFor(target); l != "a custom level" {
		return "make it as loud overall as usual for " + l
	}
	return "set how loud it sounds overall to a level of your own"
}

// MixPlan builds the plan for a mix change and says what it does in plain words
// (no units people would have to look up: "turn the music down under speech").
func MixPlan(r *models.Revision, f MixChange) (editspec.Plan, []string, error) {
	plan := editspec.Plan{BaseRevisionID: r.ID, Branch: r.Branch}
	// Per-clip mute is the clip's own setting (--mute-source --clip X).
	if f.Clip != "" && (f.MuteSource || f.UnmuteSource) {
		f.Clips = append(f.Clips, ClipSound{Clip: f.Clip, Muted: f.MuteSource})
		f.Clip, f.MuteSource, f.UnmuteSource = "", false, false
	}
	var clipOps []editspec.Op
	var clipWords []string
	for _, cs := range f.Clips {
		id, label := "", cs.Clip
		for _, cl := range r.Spec.Clips {
			if cl.ID == cs.Clip || strings.EqualFold(cl.Label, cs.Clip) {
				id = cl.ID
				if cl.Label != "" {
					label = cl.Label
				}
			}
		}
		if id == "" {
			return plan, nil, apperr.New(apperr.NotFound, "There is no clip “%s” in this cut.", cs.Clip)
		}
		clipOps = append(clipOps, editspec.Op{Op: "updateClip", ID: id, Set: []byte(fmt.Sprintf(`{"muted":%v}`, cs.Muted))})
		words := "turn off the sound of the shot " + quote(label)
		if !cs.Muted {
			words = "turn the sound of the shot " + quote(label) + " back on"
		}
		clipWords = append(clipWords, words)
	}
	if len(clipOps) > 0 && !f.mixChanged() {
		plan.Ops = clipOps
		plan.Instruction = upper(strings.Join(clipWords, ", "))
		return plan, clipWords, nil
	}
	if f.Reset {
		// Levels, ducking and loudness go back to normal; how the sound behaves at
		// cuts (speech leveling, blends) is kept.
		var keep *editspec.Mix
		if m := r.Spec.Mix; m != nil && (m.LevelSpeech != nil || m.CutBlend != nil) {
			keep = &editspec.Mix{LevelSpeech: m.LevelSpeech, CutBlend: m.CutBlend}
		}
		plan.Ops = append(clipOps, editspec.Op{Op: "setMix", Mix: keep})
		plan.Instruction = upper(strings.Join(append([]string{"reset the sound mix"}, clipWords...), ", "))
		return plan, append([]string{"reset the sound mix"}, clipWords...), nil
	}
	m := editspec.Mix{}
	if r.Spec.Mix != nil {
		m = *editspec.Clone(&editspec.Spec{Mix: r.Spec.Mix}).Mix
	}
	var what []string
	for _, bc := range f.busChanges() {
		var b **editspec.Bus
		switch bc.Bus {
		case editspec.BusSource:
			b = &m.Source
		case editspec.BusVoiceover, "narration":
			b = &m.Voiceover
		case editspec.BusMusic:
			b = &m.Music
		case editspec.BusSFX, "effects":
			b = &m.SFX
		default:
			return plan, nil, apperr.New(apperr.InvalidInput, "There is no part of the sound called “%s”. Choose the camera sound, the narration, the music or the effects.", bc.Bus).
				WithDetails(map[string]any{"parts": []string{editspec.BusSource, editspec.BusVoiceover, editspec.BusMusic, editspec.BusSFX}})
		}
		if *b == nil {
			*b = &editspec.Bus{}
		}
		part := mixPart(bc.Bus)
		if bc.GainDB != nil {
			g := math.Round(math.Max(-60, math.Min(12, *bc.GainDB))*10) / 10
			(*b).GainDB = &g
			switch {
			case g < 0:
				what = append(what, fmt.Sprintf("make %s quieter (%s)", part, dbAmount(g)))
			case g > 0:
				what = append(what, fmt.Sprintf("make %s louder (%s)", part, dbAmount(g)))
			default:
				what = append(what, "put "+part+" back at its normal level")
			}
		}
		if bc.Mute != nil {
			(*b).Mute = *bc.Mute
			what = append(what, map[bool]string{true: "mute ", false: "unmute "}[*bc.Mute]+part)
		}
		if bc.Solo != nil {
			(*b).Solo = *bc.Solo
			what = append(what, map[bool]string{true: "hear only ", false: "stop hearing only "}[*bc.Solo]+part)
		}
		if g := (*b).GainDB; g != nil && *g == 0 {
			(*b).GainDB = nil
		}
		if !(*b).Mute && !(*b).Solo && (*b).GainDB == nil {
			*b = nil // back to normal: no setting left for this part (mixes the same)
		}
	}
	if f.MuteSource || f.UnmuteSource {
		m.MuteSource = f.MuteSource
		what = append(what, map[bool]string{true: "turn off the camera sound", false: "turn the camera sound back on"}[f.MuteSource])
	}
	if f.Ducking != nil || f.MusicUnderVoice != nil || f.SourceUnderVoice != nil || f.MusicUnderDialogue != nil {
		if m.Ducking == nil {
			m.Ducking = &editspec.Ducking{}
		}
		if f.Ducking != nil {
			on := *f.Ducking
			m.Ducking.Enabled = &on
			what = append(what, map[bool]string{true: "make the music dip under voices", false: "keep the music at its level when someone talks"}[on])
		}
		set := func(v *float64, dst **float64, words string) {
			if v != nil {
				x := *v
				*dst = &x
				what = append(what, fmt.Sprintf("%s (%s)", words, dbAmount(x)))
			}
		}
		set(f.MusicUnderVoice, &m.Ducking.MusicUnderVoiceoverDB, "set how far the music dips under the narration")
		set(f.SourceUnderVoice, &m.Ducking.SourceUnderVoiceoverDB, "set how far the camera sound dips under the narration")
		set(f.MusicUnderDialogue, &m.Ducking.MusicUnderDialogueDB, "set how far the music dips when someone talks on camera")
	}
	if f.LoudnessAuto && f.Loudness == nil {
		if m.Loudness != nil {
			m.Loudness.TargetLUFS = nil
			if *m.Loudness == (editspec.MasterLoudness{}) {
				m.Loudness = nil
			}
		}
		what = append(what, "make it as loud overall as the platform expects")
	}
	if f.Loudness != nil || f.Normalize != nil || f.PeakCeiling != nil {
		if m.Loudness == nil {
			m.Loudness = &editspec.MasterLoudness{}
		}
		if f.PeakCeiling != nil {
			v := *f.PeakCeiling
			m.Loudness.TruePeakDBTP = &v
			what = append(what, "keep the loudest moments further from distorting")
			if v > -1 {
				what[len(what)-1] = "let the loudest moments get closer to distorting"
			}
		}
		if f.Loudness != nil {
			v := *f.Loudness
			m.Loudness.TargetLUFS = &v
			what = append(what, loudnessWords(v))
		}
		if f.Normalize != nil {
			on := *f.Normalize
			m.Loudness.Normalize = &on
			what = append(what, map[bool]string{true: "even out how loud it sounds overall", false: "leave how loud it sounds overall as it is"}[on])
		}
	}
	if f.LevelSpeech != nil {
		on := *f.LevelSpeech
		m.LevelSpeech = &on
		what = append(what, map[bool]string{true: "even out how loud people sound from clip to clip", false: "leave every clip's voice at its recorded level"}[on])
	}
	if f.CutBlend != nil {
		v := math.Max(0, math.Min(0.5, *f.CutBlend))
		m.CutBlend = &v
		if v == 0 {
			what = append(what, "switch the sound sharply at straight cuts")
		} else {
			what = append(what, fmt.Sprintf("blend the sound over %s at straight cuts so they never click", strconv.FormatFloat(v, 'f', -1, 64)+" s"))
		}
	}
	if len(what) == 0 {
		return plan, nil, apperr.New(apperr.InvalidInput, "Nothing to change in the sound.").WithHint("Choose what to change on the Sound tab.").WithCommand("metarush mix set --help")
	}
	plan.Ops = append(clipOps, editspec.Op{Op: "setMix", Mix: &m})
	what = append(what, clipWords...)
	plan.Instruction = "Sound mix: " + strings.Join(what, ", ")
	return plan, what, nil
}

// busChanges are the parts a change touches: the dashboard's list, plus the
// command line's single --bus with its flags.
func (f MixChange) busChanges() []BusChange {
	out := append([]BusChange(nil), f.Buses...)
	if f.Bus != "" {
		bc := BusChange{Bus: f.Bus, GainDB: f.Gain}
		switch {
		case f.Mute:
			bc.Mute = boolPtr(true)
		case f.Unmute:
			bc.Mute = boolPtr(false)
		}
		switch {
		case f.Solo:
			bc.Solo = boolPtr(true)
		case f.Unsolo:
			bc.Solo = boolPtr(false)
		}
		out = append(out, bc)
	}
	return out
}

// mixChanged reports whether anything beyond single clips' own sound changes.
func (f MixChange) mixChanged() bool {
	for _, bc := range f.busChanges() {
		if bc.GainDB != nil || bc.Mute != nil || bc.Solo != nil {
			return true
		}
	}
	return f.MuteSource || f.UnmuteSource || f.Reset || f.Ducking != nil || f.Normalize != nil || f.MusicUnderVoice != nil || f.SourceUnderVoice != nil ||
		f.MusicUnderDialogue != nil || f.Loudness != nil || f.LoudnessAuto || f.PeakCeiling != nil || f.LevelSpeech != nil || f.CutBlend != nil
}

func boolPtr(v bool) *bool { return &v }

func quote(s string) string { return "“" + s + "”" }

func upper(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
