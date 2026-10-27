package editspec

import (
	"fmt"
	"math"
	"strings"

	"github.com/metavert-io/metarush/internal/textalign"
)

// CutInfo describes one cut as resolved: the picture transition and what the
// sound does there. Validation turns it into warnings and QA into findings with
// fixes, so both say the same thing. Names are plain words for people.
type CutInfo struct {
	Index      int     `json:"index" jsonschema:"description=The incoming clip's position in the cut."`
	ClipID     string  `json:"clipId"`
	PrevClipID string  `json:"prevClipId"`
	At         float64 `json:"at" jsonschema:"description=Timeline seconds of the picture cut."`
	Type       string  `json:"type"`
	Name       string  `json:"name" jsonschema:"description=The transition in words (Dissolve 0.5 s)."`
	// WantSeconds/GotSeconds: an overlapping transition shortened to fit the shots around it.
	WantSeconds float64 `json:"wantSeconds,omitempty"`
	GotSeconds  float64 `json:"gotSeconds,omitempty"`
	// Sound.
	SoundFollows     bool     `json:"soundFollows" jsonschema:"description=The sound changes with the picture transition (no split or blend)."`
	SoundSet         bool     `json:"soundSet,omitempty" jsonschema:"description=The transition itself says how the sound changes (a split edit or auto)."`
	Mode             string   `json:"mode,omitempty"`
	Result           string   `json:"result,omitempty"`
	OffsetSeconds    float64  `json:"offsetSeconds" jsonschema:"description=Where the incoming sound starts relative to the picture cut (negative = early)."`
	BlendSeconds     float64  `json:"blendSeconds"`
	RequestedSeconds float64  `json:"requestedSeconds,omitempty"`
	Limited          string   `json:"limited,omitempty" jsonschema:"enum=footage,enum=neighbours"`
	FootageSeconds   float64  `json:"footageSeconds,omitempty" jsonschema:"description=With limited footage: how much sound the recording has beyond the picture on that side."`
	NoPause          bool     `json:"noPause,omitempty"`
	Clipped          []string `json:"clipped,omitempty" jsonschema:"description=Words cut partway."`
	DroppedPrev      []string `json:"droppedPrev,omitempty" jsonschema:"description=Words the previous clip includes that are no longer heard."`
	DroppedNext      []string `json:"droppedNext,omitempty" jsonschema:"description=Words this clip includes that are no longer heard."`
	Overlap          bool     `json:"overlap,omitempty" jsonschema:"description=Two voices are heard at once."`
	Audible          bool     `json:"audible"`
	PrevAudible      bool     `json:"prevAudible"`
	Who              string   `json:"who" jsonschema:"description=The incoming clip in words."`
	PrevWho          string   `json:"prevWho"`
	Voice            string   `json:"voice"`
	PrevVoice        string   `json:"prevVoice"`
	Person           string   `json:"person,omitempty"`
	PrevPerson       string   `json:"prevPerson,omitempty"`
}

// ShotInfo is how long one clip is seen and heard.
type ShotInfo struct {
	Index         int     `json:"index"`
	ClipID        string  `json:"clipId"`
	Who           string  `json:"who"`
	At            float64 `json:"at"`
	VisibleFrames int     `json:"visibleFrames"`
	SoundFrames   int     `json:"soundFrames"`
	Audible       bool    `json:"audible"`
}

// CutReport resolves every cut of a spec (a normalized copy is used).
func CutReport(s *Spec, facts Facts) []CutInfo {
	s = Clone(s)
	Normalize(s)
	facts = wrapFacts(s, facts)
	clips, _ := Timeline(s)
	fps := float64(s.Output.FPS)
	sp := resolveSound(s, clips, facts)
	mp := mixFor(s)
	names := shotNames(s, clips, sp, facts)
	out := []CutInfo{}
	for i := 1; i < len(clips); i++ {
		a, b := clips[i-1], clips[i]
		cs := sp.cuts[i]
		ci := CutInfo{Index: i, ClipID: b.ID, PrevClipID: a.ID, At: round3(float64(cs.cut) / fps), Type: b.TransitionIn.Type,
			Name: TransitionName(s.Clips[i].Transition), SoundFollows: cs.follow,
			SoundSet: s.Clips[i].Transition != nil && s.Clips[i].Transition.Audio != nil && s.Clips[i].Transition.Audio.Mode != "",
			Audible:  audible(s, i, facts, mp), PrevAudible: audible(s, i-1, facts, mp),
			Who: names[i].clip(), PrevWho: names[i-1].clip(), Voice: names[i].voice(), PrevVoice: names[i-1].voice(),
			Person: names[i].person, PrevPerson: names[i-1].person}
		if tr := s.Clips[i].Transition; tr != nil && IsOverlapTransition(b.TransitionIn.Type) {
			want := framesOf(tr.Duration, fps)
			if b.TransitionIn.DurationInFrames < want {
				ci.WantSeconds, ci.GotSeconds = round3(tr.Duration), round3(float64(b.TransitionIn.DurationInFrames)/fps)
			}
		}
		if !cs.follow {
			ci.Mode, ci.Result = cs.mode, cs.result
			ci.OffsetSeconds = round3(float64(cs.edit-cs.cut) / fps)
			ci.BlendSeconds = round3(float64(cs.blend) / fps)
			if cs.limited != "" {
				ci.Limited, ci.RequestedSeconds, ci.FootageSeconds = cs.limited, round3(float64(cs.requested)/fps), round3(float64(cs.footage)/fps)
			}
			ci.NoPause = cs.noPause
		}
		// What it does to speech only matters where both sides are heard.
		if ci.Audible || ci.PrevAudible {
			w := cs.words
			if ci.PrevAudible {
				ci.DroppedPrev = w.droppedA
			}
			if ci.Audible {
				ci.DroppedNext = w.droppedB
			}
			wa, wb := clipTimelineWords(a, facts, fps), clipTimelineWords(b, facts, fps)
			wa, wb = ownWords(clipSpeakers(a, b, wa, wb))
			ci.Clipped = audibleClipped(cs, ci.Audible, ci.PrevAudible, wa, wb, float64(sp.win[i-1].from))
			ci.Overlap = w.overlap && ci.Audible && ci.PrevAudible
		}
		out = append(out, ci)
	}
	return out
}

// audibleClipped keeps the cut words of the sides that are actually heard.
func audibleClipped(cs cutSound, inAudible, outAudible bool, wa, wb []tlWord, aStart float64) []string {
	if inAudible && outAudible {
		return cs.words.clipped
	}
	var none []tlWord
	e0, e1 := cs.edit, cs.edit+cs.blend
	if cs.follow || cs.vfade {
		e0, e1 = cs.cut, cs.cut
	}
	if !inAudible {
		wb = none
	}
	if !outAudible {
		wa = none
	}
	return splitWords(wa, wb, cs.cut, e0, e1, aStart).clipped
}

func audible(s *Spec, i int, facts Facts, mp mixParams) bool {
	if i < 0 || i >= len(s.Clips) || facts == nil {
		return false
	}
	c := s.Clips[i]
	if c.Muted || mp.busMuted[BusSource] || (c.Volume != nil && *c.Volume < 0.05) {
		return false
	}
	a, ok := facts.Asset(c.AssetID)
	return ok && a.HasAudio
}

// Shots lists how long each clip is on screen and heard.
func Shots(s *Spec, facts Facts) []ShotInfo {
	s = Clone(s)
	Normalize(s)
	facts = wrapFacts(s, facts)
	clips, _ := Timeline(s)
	fps := float64(s.Output.FPS)
	sp := resolveSound(s, clips, facts)
	mp := mixFor(s)
	names := shotNames(s, clips, sp, facts)
	out := []ShotInfo{}
	for i, pc := range clips {
		from, to := pc.From, pc.From+pc.DurationInFrames
		if i > 0 && IsOverlapTransition(pc.TransitionIn.Type) {
			from += pc.TransitionIn.DurationInFrames / 2
		}
		if i+1 < len(clips) {
			nx := clips[i+1]
			to = nx.From
			if IsOverlapTransition(nx.TransitionIn.Type) {
				to += nx.TransitionIn.DurationInFrames / 2
			}
		}
		w := sp.win[i]
		out = append(out, ShotInfo{Index: i, ClipID: pc.ID, Who: names[i].clip(), At: round3(float64(pc.From) / fps), VisibleFrames: max(0, to-from),
			SoundFrames: w.to - w.from, Audible: audible(s, i, facts, mp)})
	}
	return out
}

// shotName is how people see a clip in messages.
type shotName struct {
	person string // the named person who talks most in it
	label  string
	at     float64
}

func (n shotName) clip() string {
	switch {
	case n.person != "":
		return n.person + "'s clip"
	case n.label != "":
		return "the “" + n.label + "” shot"
	}
	return "the shot at " + Clock(n.at)
}

func (n shotName) voice() string {
	if n.person != "" {
		return n.person + "'s voice"
	}
	return "the sound of " + n.clip()
}

func shotNames(s *Spec, clips []PlanClip, sp *soundPlan, facts Facts) []shotName {
	fps := float64(s.Output.FPS)
	out := make([]shotName, len(clips))
	for i, pc := range clips {
		out[i] = shotName{person: mainSpeaker(pc, sp.win[i], facts), label: strings.TrimSpace(pc.Label), at: float64(pc.From) / fps}
	}
	return out
}

// quoteWords shows a few words people can find: “you know what”.
func quoteWords(ws []string) string {
	if len(ws) > 6 {
		ws = append(append([]string{}, ws[:6]...), "…")
	}
	return "“" + strings.Trim(textalign.JoinWords(ws), " ") + "”"
}

// CutIssueCodes are the codes of the warnings about transitions and the sound
// at cuts (all in plain words, so they can be shown to people as they are).
var CutIssueCodes = map[string]bool{"sound_first_clip": true, "direction_ignored": true, "easing_ignored": true, "transition_no_duration": true,
	"split_no_duration": true, "transition_clamped": true, "split_limited": true, "split_no_pause": true, "split_cuts_word": true,
	"split_drops_words": true, "voices_overlap": true, "flash_shot": true}

// cutIssues are the plain warnings about transitions and sound at cuts.
func cutIssues(s *Spec, facts Facts) []Issue {
	var out []Issue
	if len(s.Clips) > 0 {
		if tr := s.Clips[0].Transition; tr != nil && tr.Audio != nil && tr.Audio.Mode != "" && tr.Audio.Mode != SoundCut {
			out = append(out, warnI("sound_first_clip", "/clips/0/transitionIn/audio", "The first clip has nothing before it, so its sound can't start early or carry over; that sound setting is ignored."))
		}
	}
	for i, c := range s.Clips {
		tr := c.Transition
		if tr == nil {
			continue
		}
		p := fmt.Sprintf("/clips/%d/transitionIn", i)
		info, ok := TransitionByType(tr.Type)
		if ok && tr.Direction != "" && !transitionHasDirection(tr.Type) {
			out = append(out, warnI("direction_ignored", p+"/direction", "A %s doesn't move in a direction, so “%s” is ignored.", strings.ToLower(info.Name), tr.Direction))
		}
		if ok && tr.Easing != "" && !info.Easing {
			out = append(out, warnI("easing_ignored", p+"/easing", "A %s has no pace to choose, so its pace setting is ignored.", strings.ToLower(info.Name)))
		}
		if ok && tr.Type != TransitionCut && tr.Duration <= 0 {
			out = append(out, warnI("transition_no_duration", p+"/duration", "The %s into %s has no length, so it plays as a straight cut; give it a length (about %s).", strings.ToLower(info.Name), clipWords(c), seconds(info.DefaultDuration)))
		}
		if a := tr.Audio; a != nil && (a.Mode == SoundLead || a.Mode == SoundCarry) && a.Duration <= 0 {
			out = append(out, warnI("split_no_duration", p+"/audio/duration", "The sound of %s is set to %s, but for no time at all; give it a length (for example 0.4 s).", clipWords(c), map[string]string{SoundLead: "start early", SoundCarry: "carry over"}[a.Mode]))
		}
	}
	for _, ci := range CutReport(s, facts) {
		p := fmt.Sprintf("/clips/%d/transitionIn", ci.Index)
		where := "at " + Clock(ci.At)
		if ci.GotSeconds > 0 || ci.WantSeconds > 0 {
			out = append(out, warnI("transition_clamped", p, "The %s into %s is shortened to %s so it fits the shots around it.", typeName(ci.Type), ci.Who, seconds(ci.GotSeconds)))
		}
		if ci.Limited != "" {
			out = append(out, warnI("split_limited", p+"/audio", "%s.", SplitLimitedMessage(ci)))
		}
		if ci.SoundSet {
			if ci.NoPause {
				out = append(out, warnI("split_no_pause", p+"/audio", "There is no pause near the cut %s, so the sound changes as close to it as it can%s; you could move the cut to a pause.", where, cutsWords(ci.Clipped)))
			} else if len(ci.Clipped) > 0 {
				out = append(out, warnI("split_cuts_word", p+"/audio", "The sound change %s cuts %s short; let MetaRush find a pause (sound “auto”) or change the timing.", where, quoteWords(ci.Clipped)))
			}
			if len(ci.DroppedPrev) > 0 {
				out = append(out, warnI("split_drops_words", p+"/audio", "With %s starting %s early, the end of %s loses %s.", ci.Voice, seconds(math.Abs(ci.OffsetSeconds)), ci.PrevWho, quoteWords(ci.DroppedPrev)))
			}
			if len(ci.DroppedNext) > 0 {
				out = append(out, warnI("split_drops_words", p+"/audio", "With %s carrying over, %s loses its first words, %s.", ci.PrevVoice, ci.Who, quoteWords(ci.DroppedNext)))
			}
		}
		if ci.Overlap {
			if ci.SoundFollows {
				out = append(out, warnI("voices_overlap", p, "During the %s %s both people talk at once; let the sound change in a pause (sound “auto”) or use a straight cut.", typeName(ci.Type), where))
			} else if ci.BlendSeconds >= 0.15 {
				out = append(out, warnI("voices_overlap", p+"/audio", "Both voices are heard together while the sound blends %s; a shorter blend keeps them apart.", where))
			}
		}
	}
	for _, sh := range Shots(s, facts) {
		if sh.VisibleFrames > 0 && sh.VisibleFrames <= 2 {
			out = append(out, warnI("flash_shot", fmt.Sprintf("/clips/%d", sh.Index), "%s is on screen for only %d frame%s, which looks like a glitch; make it longer or remove it.", upperFirst(sh.Who), sh.VisibleFrames, map[bool]string{true: "", false: "s"}[sh.VisibleFrames == 1]))
		}
	}
	return out
}

// SplitAchieved is how far a limited split actually moves the sound (seconds).
func SplitAchieved(ci CutInfo) float64 {
	got := math.Abs(ci.OffsetSeconds)
	if (ci.Mode == SoundLead && ci.OffsetSeconds > 0) || (ci.Mode == SoundCarry && ci.OffsetSeconds < 0) {
		got = 0
	}
	return got
}

// SplitLimitedMessage says, in plain words, why a split is shorter than asked.
func SplitLimitedMessage(ci CutInfo) string {
	got := SplitAchieved(ci)
	want := seconds(ci.RequestedSeconds)
	switch {
	case ci.Mode == SoundLead && ci.Limited == "footage" && got < 0.02:
		return fmt.Sprintf("%s has no sound before it starts, so %s starts with the picture instead of %s early", upperFirst(ci.Who), ci.Voice, want)
	case ci.Mode == SoundLead && ci.Limited == "footage":
		return fmt.Sprintf("%s has only %s of sound before it starts, so %s starts %s early instead of %s", upperFirst(ci.Who), seconds(ci.FootageSeconds), ci.Voice, seconds(got), want)
	case ci.Mode == SoundLead:
		return fmt.Sprintf("The shot before %s is short, so %s can only start %s early instead of %s", ci.Who, ci.Voice, seconds(got), want)
	case ci.Limited == "footage" && got < 0.02:
		return fmt.Sprintf("%s has no sound after it ends, so %s ends with the picture instead of carrying over %s", upperFirst(ci.PrevWho), ci.PrevVoice, want)
	case ci.Limited == "footage":
		return fmt.Sprintf("%s has only %s of sound after it ends, so %s carries over %s instead of %s", upperFirst(ci.PrevWho), seconds(ci.FootageSeconds), ci.PrevVoice, seconds(got), want)
	}
	return fmt.Sprintf("%s is short, so %s can only carry over %s instead of %s", upperFirst(ci.Who), ci.PrevVoice, seconds(got), want)
}

// typeName is a transition type in words, for sentences ("the dissolve").
func typeName(t string) string {
	if info, ok := TransitionByType(t); ok {
		return strings.ToLower(info.Name)
	}
	return t
}

func cutsWords(ws []string) string {
	if len(ws) == 0 {
		return ""
	}
	return " and cuts " + quoteWords(ws) + " short"
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}
