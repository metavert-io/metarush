package editspec

import (
	"fmt"
	"math"
	"strings"

	"github.com/metavert-io/metarush/internal/textalign"
)

// ResolvedBeat is a beat placed on the timeline. Frames are output frames.
type ResolvedBeat struct {
	Index       int
	ID          string
	AnchorFrame int // where the beat's anchor resolves (-1 when it cannot be resolved)
	Start       int // where its audio starts (the anchor for the beats layout)
	// Speech is how many frames the line takes: its take's length at its
	// playback rate, or — without audio — the word count at the speaking rate.
	Speech    int
	Estimated bool // Speech is an estimate from the word count
	// WindowEnd is where the line's time runs out: the start of the next line in
	// time (Next), or the end of the cut (Next = -1). NextInScript tells whether
	// that line is also the next one in the script (it is not when rearranged
	// shots moved a later line in front of this one).
	WindowEnd    int
	Next         int
	NextInScript bool
	Words        int
	Clip         string // the clip the beat starts over ("" outside every clip)
	// Audio (beats layout, with a take).
	PlayAsset  string
	TrimBefore int // source offset of the take in output frames
	Rate       float64
	Problem    string // why the anchor could not be resolved (an error)
}

// End is the frame where the beat's speech ends.
func (b ResolvedBeat) End() int { return b.Start + b.Speech }

// Window is how many frames the beat has before the next one starts (or the
// cut ends); never negative.
func (b ResolvedBeat) Window() int { return max(0, b.WindowEnd-b.Start) }

// Until names what the line's window runs up to, for messages: "before the next
// line", "before line 3 starts" (lines out of script order) or "before the video
// ends".
func (b ResolvedBeat) Until() string {
	switch {
	case b.Next < 0:
		return "before the video ends"
	case b.NextInScript:
		return "before the next line"
	}
	return fmt.Sprintf("before line %d starts", b.Next+1)
}

// pauseBefore names what has no pause before it when a line just fits.
func (b ResolvedBeat) pauseBefore() string {
	switch {
	case b.Next < 0:
		return "it ends right as the video does"
	case b.NextInScript:
		return "no pause before the next line"
	}
	return fmt.Sprintf("no pause before line %d", b.Next+1)
}

// SpeakingRate returns the words-per-minute used for estimates.
func (s *Spec) SpeakingRate() float64 {
	if s.Voiceover != nil && s.Voiceover.Settings != nil && s.Voiceover.Settings.SpeakingRateWPM > 0 {
		return s.Voiceover.Settings.SpeakingRateWPM
	}
	return DefaultWPM
}

func (s *Spec) beatGap() float64 {
	if s.Voiceover != nil && s.Voiceover.Settings != nil && s.Voiceover.Settings.Gap > 0 {
		return s.Voiceover.Settings.Gap
	}
	return 0.3
}

// FitPolicy returns the voiceover fit policy (auto by default).
func (s *Spec) FitPolicy() string {
	if s.Voiceover != nil && s.Voiceover.Settings != nil && s.Voiceover.Settings.FitPolicy != "" {
		return s.Voiceover.Settings.FitPolicy
	}
	return FitAuto
}

// Continuous reports whether the voiceover plays as one continuous recording.
func (s *Spec) Continuous() bool {
	return s.Voiceover != nil && s.Voiceover.Layout == LayoutContinuous && s.Voiceover.Continuous != nil
}

// WordCount counts the spoken words of a line.
func WordCount(text string) int {
	n := 0
	for _, t := range textalign.Tokens(text) {
		if !textalign.IsFiller(textalign.Normalize(t)) {
			n++
		}
	}
	return n
}

// EstimateSeconds is how long a line takes at wpm, with a small allowance for
// sentence pauses.
func EstimateSeconds(text string, wpm float64) float64 {
	if wpm <= 0 {
		wpm = DefaultWPM
	}
	words := float64(WordCount(text))
	pauses := float64(strings.Count(text, ".") + strings.Count(text, "?") + strings.Count(text, "!") + strings.Count(text, ";") + strings.Count(text, ":"))
	if pauses > 0 {
		pauses-- // the line's own end needs no pause
	}
	return words*60/wpm + 0.25*pauses
}

// resolveAnchor turns an anchor into a timeline frame. prevEnd is the end of the
// previous beat's speech (-1 for the first beat).
func resolveAnchor(an Anchor, clips []PlanClip, fps float64, prevEnd int, gap float64) (int, string) {
	off := int(math.Round(an.Offset * fps))
	switch an.Kind {
	case AnchorTime:
		if an.At == nil {
			return -1, "it has no start time"
		}
		return int(math.Round(*an.At*fps)) + off, ""
	case AnchorClip:
		for _, pc := range clips {
			if pc.ID == an.ClipID {
				if an.Edge == "end" {
					return pc.From + pc.DurationInFrames + off, ""
				}
				return pc.From + off, ""
			}
		}
		return -1, "it was set to start with a clip that is no longer in the video"
	case AnchorMoment:
		if an.At == nil || an.AssetID == "" {
			return -1, "the moment it should follow isn't set"
		}
		at := *an.At
		var hit *PlanClip
		for i := range clips {
			pc := &clips[i]
			if pc.AssetID != an.AssetID || at < pc.SourceIn || at >= pc.SourceOut {
				continue
			}
			if hit == nil || pc.ID == an.ClipID {
				hit = pc
			}
		}
		if hit == nil {
			if an.Quote != "" {
				return -1, fmt.Sprintf("the moment it follows (“%s”) is no longer in the video", an.Quote)
			}
			return -1, "the moment it follows is no longer in the video"
		}
		return mapSourceToTimeline(*hit, fps, at) + off, ""
	case AnchorAfter:
		g := gap
		if an.Gap != nil {
			g = *an.Gap
		}
		if prevEnd < 0 {
			return off, ""
		}
		return prevEnd + int(math.Round(g*fps)) + off, ""
	}
	return -1, "MetaRush doesn't understand its start point"
}

// ResolveBeats places every beat on the timeline. It is the single
// implementation of narration timing (validation, fit checks, the render plan
// and QA all use it).
func ResolveBeats(s *Spec, clips []PlanClip, total int, facts Facts) []ResolvedBeat {
	if s.Voiceover == nil || len(s.Voiceover.Beats) == 0 {
		return nil
	}
	fps := float64(s.Output.FPS)
	wpm := s.SpeakingRate()
	gap := s.beatGap()
	cont := s.Continuous()
	contStart := -1
	if cont {
		c := s.Voiceover.Continuous
		var prob string
		contStart, prob = resolveAnchor(c.Anchor, clips, fps, -1, gap)
		if prob != "" {
			contStart = -1
		}
	}
	out := make([]ResolvedBeat, 0, len(s.Voiceover.Beats))
	prevEnd := -1
	for i, b := range s.Voiceover.Beats {
		rb := ResolvedBeat{Index: i, ID: b.ID, Words: WordCount(b.Text), Rate: 1}
		rb.AnchorFrame, rb.Problem = resolveAnchor(b.Anchor, clips, fps, prevEnd, gap)
		rb.Start = rb.AnchorFrame
		if t := b.Take; t != nil && t.Out > t.In {
			rb.Rate = t.Rate()
			rb.Speech = int(math.Round((t.Out - t.In) / rb.Rate * fps))
			if cont {
				c := s.Voiceover.Continuous
				if contStart >= 0 && t.AssetID == c.AssetID {
					rb.Start = contStart + int(math.Round((t.In-c.In)*fps))
					rb.Rate = 1
					rb.Speech = int(math.Round((t.Out - t.In) * fps))
				}
			} else {
				asset, offset := t.PlayAsset()
				rb.PlayAsset = asset
				rb.TrimBefore = int(math.Round((t.In + offset) * fps))
				if rb.TrimBefore < 0 {
					rb.TrimBefore = 0
				}
			}
		} else {
			rb.Estimated = true
			rb.Speech = int(math.Round(EstimateSeconds(b.Text, wpm) * fps))
		}
		if rb.Speech < 1 {
			rb.Speech = 1
		}
		if rb.Problem == "" {
			for _, pc := range clips {
				if rb.Start >= pc.From && rb.Start < pc.From+pc.DurationInFrames {
					rb.Clip = pc.ID // the last clip covering it wins (crossfades)
				}
			}
			prevEnd = rb.End()
		}
		out = append(out, rb)
	}
	setWindows(out, total)
	return out
}

// setWindows gives each placed beat its window: it runs to the start of the
// next line in time (the first line that starts after it, or at the same frame
// further down the script), or to the end of the cut. Lines are normally in
// script order, so that is the next line in the script; when rearranged shots
// moved a later line in front of an earlier one, the earlier one still gets the
// time up to whatever plays after it (never a negative window).
func setWindows(out []ResolvedBeat, total int) {
	for i := range out {
		b := &out[i]
		b.WindowEnd, b.Next, b.NextInScript = total, -1, false
		if b.Problem != "" {
			continue
		}
		for j := range out {
			o := out[j]
			if j == i || o.Problem != "" || o.Start >= total {
				continue
			}
			if o.Start > b.Start || (o.Start == b.Start && j > i) {
				if b.Next < 0 || o.Start < b.WindowEnd || (o.Start == b.WindowEnd && j < b.Next) {
					b.WindowEnd, b.Next = o.Start, j
				}
			}
		}
		if b.Next >= 0 {
			// The next line in the script is the first placed one after i.
			for j := i + 1; j < len(out); j++ {
				if out[j].Problem == "" {
					b.NextInScript = j == b.Next
					break
				}
			}
		}
	}
}

// prevEnd is where the narration before beat i (in time) stops: the latest end
// of the placed lines that start before it, 0 when none does.
func prevEnd(beats []ResolvedBeat, i int) int {
	end := 0
	for j, o := range beats {
		if j == i || o.Problem != "" {
			continue
		}
		if o.Start < beats[i].Start || (o.Start == beats[i].Start && j < i) {
			end = max(end, o.End())
		}
	}
	return end
}

// voiceRanges are the timeline spans where narration plays (merged; for ducking
// and QA).
func voiceRanges(s *Spec, beats []ResolvedBeat, total int) []FrameRange {
	var rs []FrameRange
	for _, b := range beats {
		if b.Problem != "" {
			continue
		}
		hasAudio := s.Voiceover.Beats[b.Index].Take != nil
		if !hasAudio {
			continue
		}
		from, to := max(0, b.Start), min(total, b.End())
		if to > from {
			rs = append(rs, FrameRange{From: from, To: to})
		}
	}
	for _, a := range s.Audio {
		if a.Role == RoleVoiceover {
			fps := float64(s.Output.FPS)
			from := int(math.Round(a.At * fps))
			to := min(total, from+int(math.Round((a.Out-a.In)*fps)))
			if to > from {
				rs = append(rs, FrameRange{From: from, To: to})
			}
		}
	}
	return mergeRanges(rs, int(float64(s.Output.FPS)/2))
}

func mergeRanges(in []FrameRange, gap int) []FrameRange {
	rs := append([]FrameRange(nil), in...)
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && rs[j].From < rs[j-1].From; j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
	out := []FrameRange{}
	for _, r := range rs {
		if n := len(out); n > 0 && r.From <= out[n-1].To+gap {
			if r.To > out[n-1].To {
				out[n-1].To = r.To
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// fitStatus classifies a beat's speech against its window.
func fitStatus(b ResolvedBeat) string {
	switch {
	case b.Problem != "":
		return "unresolved"
	case b.Speech > b.Window()+1:
		return "over"
	case float64(b.Speech) > 0.92*float64(b.Window()):
		return "tight"
	}
	return "ok"
}

func planVoiceover(s *Spec, beats []ResolvedBeat) *PlanVoiceover {
	pv := &PlanVoiceover{Layout: LayoutBeats, SpeakingRateWPM: s.SpeakingRate(), Beats: []PlanBeat{}}
	if s.Continuous() {
		pv.Layout = LayoutContinuous
	}
	for _, b := range beats {
		bt := s.Voiceover.Beats[b.Index]
		pv.Beats = append(pv.Beats, PlanBeat{ID: b.ID, Index: b.Index, Text: bt.Text, AnchorFrame: b.AnchorFrame, From: b.Start, To: b.End(),
			WindowEnd: b.WindowEnd, Words: b.Words, Estimated: b.Estimated, HasAudio: bt.Take != nil, Fit: fitStatus(b), Problem: b.Problem})
	}
	return pv
}
