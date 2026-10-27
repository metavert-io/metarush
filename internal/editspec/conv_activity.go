package editspec

import (
	"math"
	"sort"
	"strings"
)

// Who is speaking in a conversation (D101): each member's own words, plus
// sound without words (laughs, short replies) where their track is well above
// its own background, minus what their microphone only picked up from someone
// else. The gate, the automatic cut and QA read the same activity.

const (
	actPre  = 0.05 // activity starts this long before a word
	actPost = 0.10 // ... and ends this long after it
	actJoin = 0.25 // gaps shorter than this are joined

	// The gate opens a little before a person's first sound (the future is
	// known), holds after their last, and closes smoothly.
	gateEarly   = 0.08
	gateAttack  = 0.05
	gateHold    = 0.25
	gateRelease = 0.15
	gateBridge  = 0.6 // shorter gaps between speech stay open (no pumping between words)
)

// mergeSpans sorts spans and joins those closer than join.
func mergeSpans(in []Span, join float64) []Span {
	sp := make([]Span, 0, len(in))
	for _, s := range in {
		if s.To > s.From {
			sp = append(sp, s)
		}
	}
	sort.Slice(sp, func(i, j int) bool { return sp[i].From < sp[j].From })
	var out []Span
	for _, s := range sp {
		if n := len(out); n > 0 && s.From <= out[n-1].To+join {
			if s.To > out[n-1].To {
				out[n-1].To = s.To
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// overlaps reports whether any span overlaps [a, b).
func overlaps(spans []Span, a, b float64) bool {
	i := sort.Search(len(spans), func(i int) bool { return spans[i].To > a })
	return i < len(spans) && spans[i].From < b
}

// activity is who is heard speaking when, per member (sorted, merged spans on
// the conversation's clock), computed once per resolution.
func (cf *convFacts) activity(cs *convSource) map[string][]Span {
	if cs.act != nil {
		return cs.act
	}
	words := map[string][]Span{}
	for _, m := range cs.pin.Members {
		if !m.HasSound() {
			continue
		}
		var sp []Span
		for _, sg := range cs.mine[m.ID] {
			if len(sg.Words) == 0 {
				if strings.TrimSpace(sg.Text) != "" {
					sp = append(sp, Span{sg.From - actPre, sg.To + actPost})
				}
				continue
			}
			for _, w := range sg.Words {
				sp = append(sp, Span{w.From - actPre, w.To + actPost})
			}
		}
		words[m.ID] = mergeSpans(sp, actJoin)
	}
	act := map[string][]Span{}
	for mi, m := range cs.pin.Members {
		ws, ok := words[m.ID]
		if !ok {
			continue
		}
		var others []Span
		for id, o := range words {
			if id != m.ID {
				others = append(others, o...)
			}
		}
		others = mergeSpans(others, 0)
		extra := cf.wordless(cs, mi, ws, others)
		act[m.ID] = mergeSpans(append(append([]Span(nil), ws...), extra...), actJoin)
	}
	cs.act = act
	return act
}

// wordless finds a member's sound without words: stretches of at least 0.3 s
// well above their background and not far below their speaking level while
// someone else speaks (that would be the other voice picked up).
func (cf *convFacts) wordless(cs *convSource, mi int, own, others []Span) []Span {
	ms := cf.seriesOf(cs, mi)
	if ms == nil || ms.speech-ms.noise < 10 {
		return nil // no measurements, or too noisy to tell sound from background
	}
	m := cs.pin.Members[mi]
	thr := math.Max(ms.noise+10, ms.speech-15)
	var out []Span
	k := 0
	n := len(ms.lufs)
	for k < n {
		if ms.lufs[k] <= thr || math.IsNaN(ms.lufs[k]) {
			k++
			continue
		}
		j := k
		peak := ms.lufs[k]
		for j+1 < n && ms.lufs[j+1] > thr {
			j++
			peak = math.Max(peak, ms.lufs[j])
		}
		// Window k covers the 400 ms ending at (k+1)·step.
		a := m.ClockAt(float64(k+1)*ms.step - 0.3)
		b := m.ClockAt(float64(j+1) * ms.step)
		k = j + 1
		if b-a < 0.3 || overlaps(own, a, b) {
			continue
		}
		if overlaps(others, a-bleedLag, b) && peak < ms.speech-6 {
			continue // the other person's voice in this microphone
		}
		out = append(out, Span{a, b})
	}
	return out
}

// gate is a member's gain while the gate follows their speech: 1 while they
// speak (open gateEarly before, held gateHold after; gaps under gateBridge stay
// open), down by depth dB elsewhere, with linear-in-dB ramps.
type gate struct {
	open  []Span
	depth float64
}

func newGate(act []Span, depth float64) gate {
	open := make([]Span, 0, len(act))
	for _, s := range act {
		open = append(open, Span{s.From - gateEarly, s.To + gateHold})
	}
	return gate{open: mergeSpans(open, gateBridge), depth: depth}
}

// at is the linear gain at conversation time t.
func (g gate) at(t float64) float64 {
	if g.depth <= 0 {
		return 1
	}
	i := sort.Search(len(g.open), func(i int) bool { return g.open[i].To > t })
	db := -g.depth
	if i < len(g.open) {
		s := g.open[i]
		if t >= s.From {
			return 1
		}
		if d := s.From - t; d < gateAttack {
			db = math.Max(db, -g.depth*d/gateAttack)
		}
	}
	if i > 0 {
		if d := t - g.open[i-1].To; d >= 0 && d < gateRelease {
			db = math.Max(db, -g.depth*d/gateRelease)
		}
	}
	return dbToLin(db)
}

// ---- exported views for the automatic cut, clean-up and QA ----

// ConversationView is what the engine knows about one pinned conversation:
// its length, everyone's words on its clock (Words, each member's own in Own),
// who is heard speaking when (Activity, member ID → spans) and each member's
// leveling gain (dB) when the cut evens out voices.
type ConversationView struct {
	Name     string
	Pin      ConversationPin
	Length   float64
	Words    []TranscriptSegment
	Own      map[string][]TranscriptSegment
	Activity map[string][]Span
	Levels   map[string]float64
	LUFS     *float64
}

// Conversation resolves a pinned conversation of s against facts (nil when s
// doesn't pin it).
func Conversation(s *Spec, facts Facts, name string) *ConversationView {
	if s == nil || facts == nil {
		return nil
	}
	if _, ok := s.Conversations[name]; !ok {
		return nil
	}
	cf := convFactsOf(facts)
	if cf == nil || cf.srcs[name] == nil {
		ns := Clone(s)
		Normalize(ns)
		cf = convFactsOf(withConversations(ns, facts))
	}
	if cf == nil {
		return nil
	}
	cs := cf.srcs[name]
	if cs == nil {
		return nil
	}
	return &ConversationView{Name: name, Pin: cs.pin, Length: cs.end, Words: cs.words, Own: cs.mine, Activity: cf.activity(cs), Levels: cs.levels, LUFS: cs.lufs}
}

// PinWords is a pinned conversation's words on its clock (each person's own,
// named) and its length, from the facts about its members' recordings. Cut
// sheets find quoted words in a conversation clip with it.
func PinWords(pin ConversationPin, out Output, facts Facts) ([]TranscriptSegment, float64) {
	if facts == nil {
		return nil, 0
	}
	s := &Spec{Output: out, Conversations: map[string]ConversationPin{"c": pin}}
	Normalize(s)
	cf := convFactsOf(withConversations(s, facts))
	if cf == nil || cf.srcs["c"] == nil {
		return nil, 0
	}
	return cf.srcs["c"].words, cf.srcs["c"].end
}

// Transcripts is a spec's transcript lookup that also answers for its
// conversations (conv:<name>: everyone's words on the conversation's clock).
func Transcripts(s *Spec, facts Facts) func(assetID string) []TranscriptSegment {
	if facts == nil {
		return func(string) []TranscriptSegment { return nil }
	}
	return withConversations(s, facts).Transcript
}
