package editspec

import (
	"math"
	"slices"
)

// ConversationPlay is a conversation clip as it plays in the timeline: who is
// heard and who is seen when (timeline seconds). QA checks conversations
// against it: every voice heard, nobody kept out of the picture for long,
// screen time that follows who talks, and the calls' glitches where they show.
type ConversationPlay struct {
	ClipID string
	Name   string // the pin's name in the cut
	ID     string // the conversation (cnv_…)
	// From and To are the clip in the timeline; SourceIn, SourceOut and Rate
	// map the conversation's clock onto it.
	From, To                  float64
	SourceIn, SourceOut, Rate float64
	People                    []PersonPlay
}

// PersonPlay is one member in a conversation clip.
type PersonPlay struct {
	Member, Name string
	// AssetID is their recording; Start and Rate place it on the
	// conversation's clock (ClockAt).
	AssetID     string
	Start, Rate float64
	// Picture and Sound: what of their recording the conversation uses;
	// Muted: their sound is left out of this clip.
	Picture, Sound, Muted bool
	Speaking              []Span // heard speaking (their own words and sounds)
	Seen                  []Span // on screen, alone or in a layout (not softened behind a card)
	Alone                 []Span // on screen full frame
}

// Timeline is the timeline second where the conversation's clock reads t
// (ok false outside the clip).
func (p ConversationPlay) Timeline(t float64) (float64, bool) {
	if t < p.SourceIn || t >= p.SourceOut {
		return 0, false
	}
	return p.From + (t-p.SourceIn)/p.Rate, true
}

// Clock is the conversation's clock at timeline second t.
func (p ConversationPlay) Clock(t float64) float64 { return p.SourceIn + (t-p.From)*p.Rate }

// ClockAt is the conversation's clock at second tau of their recording.
func (p PersonPlay) ClockAt(tau float64) float64 {
	if p.Rate <= 0 {
		return p.Start + tau
	}
	return p.Start + tau/p.Rate
}

// ConversationPlays lists the conversation clips of a resolved plan.
func ConversationPlays(s *Spec, plan *RenderPlan, facts Facts) []ConversationPlay {
	if s == nil || plan == nil || facts == nil || !HasConversations(s) {
		return nil
	}
	cf := convFactsOf(facts)
	if cf == nil {
		ns := Clone(s)
		Normalize(ns)
		cf = convFactsOf(withConversations(ns, facts))
	}
	if cf == nil {
		return nil
	}
	fps := float64(plan.FPS)
	var out []ConversationPlay
	for i, pc := range plan.Clips {
		name := ConvName(pc.AssetID)
		cs := cf.srcs[name]
		if name == "" || cs == nil || pc.Conversation == nil || i >= len(s.Clips) || fps <= 0 {
			continue
		}
		c := s.Clips[i]
		rate := pc.PlaybackRate
		if rate <= 0 {
			rate = 1
		}
		p := ConversationPlay{ClipID: c.ID, Name: name, ID: cs.pin.ID, From: float64(pc.From) / fps, To: float64(pc.From+pc.DurationInFrames) / fps,
			SourceIn: pc.SourceIn, SourceOut: pc.SourceOut, Rate: rate}
		act := cf.activity(cs)
		for _, m := range cs.pin.Members {
			pp := PersonPlay{Member: m.ID, Name: m.Name, AssetID: m.AssetID, Start: m.Start, Rate: m.Rate(), Picture: m.HasPicture(), Sound: m.UseOf() != UsePicture, Muted: m.Muted || slices.Contains(c.MutedMembers, m.ID)}
			for _, a := range act[m.ID] {
				x, y := math.Max(a.From, p.SourceIn), math.Min(a.To, p.SourceOut)
				if y > x {
					pp.Speaking = append(pp.Speaking, Span{p.From + (x-p.SourceIn)/rate, p.From + (y-p.SourceIn)/rate})
				}
			}
			for _, sh := range pc.Conversation.Shots {
				a := float64(pc.From+sh.From) / fps
				b := float64(pc.From+sh.From+sh.DurationInFrames) / fps
				for _, t := range sh.Tiles {
					if t.Member != m.ID || t.Soft {
						continue
					}
					pp.Seen = append(pp.Seen, Span{a, b})
					if sh.Kind == "member" {
						pp.Alone = append(pp.Alone, Span{a, b})
					}
				}
			}
			pp.Seen, pp.Alone = mergeSpans(pp.Seen, 1e-6), mergeSpans(pp.Alone, 1e-6)
			p.People = append(p.People, pp)
		}
		out = append(out, p)
	}
	return out
}

// heardWords is the transcript a clip's sound plays: a conversation clip's
// leaves out the people whose sound it mutes (their words are not heard, so
// they are neither captioned nor expected).
func heardWords(s *Spec, i int, assetID string, facts Facts) []TranscriptSegment {
	segs := facts.Transcript(assetID)
	if s != nil && s.Mix != nil && len(s.Mix.Said) > 0 {
		segs = s.Mix.SaidWords(assetID, segs) // words changed in the speaker's own voice (D308)
	}
	if s == nil || i < 0 || i >= len(s.Clips) || !IsConversation(assetID) {
		return segs
	}
	pin, ok := s.Conversations[ConvName(assetID)]
	if !ok {
		return segs
	}
	muted := map[string]bool{}
	for _, m := range pin.Members {
		if !m.HasSound() || slices.Contains(s.Clips[i].MutedMembers, m.ID) {
			muted[m.Name] = true
		}
	}
	if len(muted) == 0 {
		return segs
	}
	out := make([]TranscriptSegment, 0, len(segs))
	for _, sg := range segs {
		if !muted[sg.Speaker] {
			out = append(out, sg)
		}
	}
	return out
}

// SpanSeconds is the total length of spans.
func SpanSeconds(spans []Span) float64 {
	t := 0.0
	for _, s := range spans {
		t += math.Max(0, s.To-s.From)
	}
	return t
}

// SpansMinus is what of spans is not covered by others (both sorted and merged).
func SpansMinus(spans, others []Span) []Span {
	var out []Span
	j := 0
	for _, s := range spans {
		cur := s.From
		for j < len(others) && others[j].To <= cur {
			j++
		}
		for k := j; k < len(others) && others[k].From < s.To; k++ {
			if others[k].From > cur {
				out = append(out, Span{cur, others[k].From})
			}
			cur = math.Max(cur, others[k].To)
		}
		if cur < s.To {
			out = append(out, Span{cur, s.To})
		}
	}
	return out
}

// MergeSpans sorts spans and joins those closer than join seconds.
func MergeSpans(in []Span, join float64) []Span { return mergeSpans(in, join) }
