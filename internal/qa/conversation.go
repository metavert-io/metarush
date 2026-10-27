package qa

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/models"
)

// Conversation checks (D99–D103): every voice is heard, nobody is kept out of
// the picture for long, screen time follows who talks, and the calls' own
// glitches (a frozen webcam, a sound dropout) don't show.

// Conversation is a conversation clip as QA checks it: how it plays
// (editspec.ConversationPlays) and what its record knows.
type Conversation struct {
	editspec.ConversationPlay
	Style string // podcast, interview, panel, reactions, broll ("" when unknown)
	// MaxShot is the longest the picture should stay on one person (the
	// style's limit; 0 means ConvMaxShot).
	MaxShot float64
	// OffCamera are members kept off camera on purpose (an off-camera interviewer).
	OffCamera []string
	// Dropouts are moments a member's sound drops out, Freezes moments their
	// picture stalls (the conversation's clock; clean-up found them on the
	// originals).
	Dropouts []Glitch
	Freezes  []Glitch
	// Unsure names the people whose recordings may be out of step (lining up was unsure).
	Unsure []string
}

// Glitch is a moment one member's recording goes wrong (the conversation's clock).
type Glitch struct {
	Member   string
	From, To float64
}

// Conversation thresholds.
const (
	ConvMaxShot   = 30.0 // the picture stays on one person at most this long (seconds) unless the style says otherwise
	ConvQuieterDB = 6.0  // a person this much quieter than the loudest is hard to hear
	ConvSilentDB  = 20.0 // ... this much quieter: they can't really be heard
	ConvMinSolo   = 1.5  // seconds of words only they say, to measure how loud someone is
	ConvTalkShare = 0.2  // someone doing this share of the talking ...
	ConvSeenRatio = 0.5  // ... should be on screen at least this much of that share
	ConvGlitchMin = 0.4  // a frozen picture on screen at least this long (seconds) is reported
)

func (r *run) conversations() {
	if len(r.in.Conversations) == 0 {
		return
	}
	for _, cv := range r.in.Conversations {
		m := models.QAConversation{ClipID: cv.ClipID, Name: cv.Name, Style: cv.Style, People: []models.QAConversationPerson{}, Limit: cv.limit()}
		for _, p := range cv.People {
			m.People = append(m.People, models.QAConversationPerson{Name: p.Name, Speaking: round3(editspec.SpanSeconds(p.Speaking)),
				OnScreen: round3(editspec.SpanSeconds(p.Seen)), Muted: p.Muted})
			for _, a := range p.Alone {
				m.Longest = math.Max(m.Longest, round3(a.To-a.From))
			}
		}
		r.rep.Measurements.Conversations = append(r.rep.Measurements.Conversations, m)
	}
	r.convVoices()
	r.convPicture()
	r.convGlitches()
}

func (cv Conversation) limit() float64 {
	if cv.MaxShot > 0 {
		return cv.MaxShot
	}
	return ConvMaxShot
}

func (cv Conversation) speaking(except string) []editspec.Span {
	var all []editspec.Span
	for _, p := range cv.People {
		if p.Member != except && !p.Muted && p.Sound {
			all = append(all, p.Speaking...)
		}
	}
	return editspec.MergeSpans(all, 0)
}

func (cv Conversation) person(id string) *editspec.PersonPlay {
	for i := range cv.People {
		if cv.People[i].Member == id {
			return &cv.People[i]
		}
	}
	return nil
}

// convVoices measures, in the render, how loud each person is over the words
// only they say, and flags anyone much quieter than the loudest (observed).
func (r *run) convVoices() {
	const name = "conversation_voices"
	s := r.det.Momentary
	switch {
	case !r.mi.HasAudio:
		r.skip(name, models.QAObserved, "no audio stream")
		return
	case s == nil:
		r.skip(name, models.QAObserved, "the render's loudness over time could not be measured")
		return
	}
	measured := 0
	for ci, cv := range r.in.Conversations {
		loud := map[string]float64{}
		best, bestName := math.Inf(-1), ""
		for _, p := range cv.People {
			if !p.Sound || p.Muted {
				continue
			}
			solo := editspec.SpansMinus(p.Speaking, cv.speaking(p.Member))
			if editspec.SpanSeconds(solo) < ConvMinSolo {
				continue
			}
			l, ok := integratedOver(s, solo)
			if !ok {
				l = -70 // speaking alone, yet nothing measurable comes out
			}
			loud[p.Member] = l
			for k := range r.rep.Measurements.Conversations[ci].People {
				if pm := &r.rep.Measurements.Conversations[ci].People[k]; pm.Name == p.Name {
					v := round3(l)
					pm.Loudness = &v
				}
			}
			if l > best {
				best, bestName = l, p.Name
			}
		}
		if len(loud) > 0 {
			measured++
		}
		for _, p := range cv.People {
			l, ok := loud[p.Member]
			if !ok || len(loud) < 2 {
				continue
			}
			gap := best - l
			if gap < ConvQuieterDB {
				continue
			}
			sev, kind := models.QAWarning, "conversation_quiet"
			msg := fmt.Sprintf("%s sounds much quieter than %s (about %.0f dB).", p.Name, bestName, gap)
			if gap >= ConvSilentDB {
				sev, kind = models.QAError, "conversation_inaudible"
				msg = fmt.Sprintf("%s can hardly be heard: about %.0f dB quieter than %s.", p.Name, gap, bestName)
			}
			sp := p.Speaking
			from, to := cv.From, cv.To
			if len(sp) > 0 {
				from, to = sp[0].From, sp[len(sp)-1].To
			}
			r.add(&models.QAFinding{Severity: sev, Kind: kind, Basis: models.QAObserved, From: from, To: to, ClipIDs: []string{cv.ClipID}, Message: msg, Fix: r.voiceUp(p.Name, gap)})
		}
		// Someone whose sound this clip leaves out, though they talk in it.
		cardClip := r.hasCard(cv.ClipID)
		for _, p := range cv.People {
			if p.Muted && p.Sound && !cardClip && editspec.SpanSeconds(p.Speaking) >= 3 {
				r.add(&models.QAFinding{Severity: models.QAInfo, Kind: "conversation_muted", Basis: models.QAHeuristic, From: p.Speaking[0].From, To: p.Speaking[len(p.Speaking)-1].To, ClipIDs: []string{cv.ClipID},
					Message: fmt.Sprintf("%s's microphone is off in this clip, so their %s of talk is heard only through the others' microphones, if at all.", p.Name, secs(math.Round(editspec.SpanSeconds(p.Speaking))))})
			}
		}
	}
	if measured == 0 {
		r.skip(name, models.QAObserved, "nobody speaks alone long enough to measure")
		return
	}
	r.check(name, models.QAObserved, fmt.Sprintf("each person's loudness in the render over the words only they say (≥%.1f s) is within %.0f dB of the loudest; %.0f dB or more = can't be heard", ConvMinSolo, ConvQuieterDB, ConvSilentDB),
		"conversation_quiet", "conversation_inaudible", "conversation_muted")
}

// voiceUp is the fix that raises a person's level (mix.voices) by most of the gap.
func (r *run) voiceUp(name string, gap float64) *models.QAFix {
	up := math.Min(12, math.Round(gap-2))
	fix := &models.QAFix{Summary: fmt.Sprintf("Turn %s up about %.0f dB (Sound tab), then render again.", name, up)}
	if r.in.Spec == nil || (r.in.Spec.Mix != nil && r.in.Spec.Mix.Locked) {
		return fix
	}
	m := editspec.Mix{}
	if r.in.Spec.Mix != nil {
		m = *editspec.Clone(&editspec.Spec{Mix: r.in.Spec.Mix}).Mix
	}
	if v := m.VoiceFor(name); v != nil {
		g := up
		if v.GainDB != nil {
			g = math.Min(12, *v.GainDB+up)
		}
		v.GainDB = &g
	} else {
		g := up
		m.Voices = append(m.Voices, editspec.VoiceSound{Name: name, GainDB: &g})
	}
	fix.Ops = []editspec.Op{{Op: "setMix", Mix: &m}}
	return fix
}

func (r *run) hasCard(clipID string) bool {
	for _, c := range r.plan.Clips {
		if c.ID != clipID || c.Conversation == nil {
			continue
		}
		for _, sh := range c.Conversation.Shots {
			if sh.Kind == "card" {
				return true
			}
		}
	}
	return false
}

// integratedOver is the gated loudness (as Series.Integrated) of the 400 ms
// blocks that fall inside the spans.
func integratedOver(s *evidence.Series, spans []editspec.Span) (float64, bool) {
	if s == nil || s.Step <= 0 {
		return 0, false
	}
	var es []float64
	j := 0
	for k, l := range s.LUFS {
		end := float64(k+1) * s.Step
		start := end - 0.4
		for j < len(spans) && spans[j].To < end-1e-9 {
			j++
		}
		if j >= len(spans) {
			break
		}
		if start < spans[j].From-1e-9 || l <= -70 {
			continue
		}
		es = append(es, math.Pow(10, l/10))
	}
	if len(es) < 3 {
		return 0, false
	}
	mean := func(xs []float64) float64 {
		t := 0.0
		for _, x := range xs {
			t += x
		}
		return t / float64(len(xs))
	}
	abs := 10 * math.Log10(mean(es))
	var rel []float64
	for _, e := range es {
		if 10*math.Log10(e) > abs-10 {
			rel = append(rel, e)
		}
	}
	if len(rel) == 0 {
		return abs, true
	}
	return 10 * math.Log10(mean(rel)), true
}

// convPicture checks what the picture does with the people (from the plan):
// no shot of one person beyond the style's limit, nobody talking at length
// unseen, and screen time that roughly follows who talks.
func (r *run) convPicture() {
	for _, cv := range r.in.Conversations {
		lim := cv.limit()
		for _, p := range cv.People {
			for _, a := range p.Alone {
				if a.To-a.From <= lim+0.5 {
					continue
				}
				msg := fmt.Sprintf("The picture stays on %s for %s (%s).", p.Name, secs(math.Round(a.To-a.From)), clock(a.From)+"–"+clock(a.To))
				if others := editspec.SpanSeconds(clipSpans(cv.speaking(p.Member), a.From, a.To)); others >= 2 {
					msg = fmt.Sprintf("The picture stays on %s for %s (%s) while others talk too.", p.Name, secs(math.Round(a.To-a.From)), clock(a.From)+"–"+clock(a.To))
				}
				r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "conversation_one_sided", Basis: models.QAHeuristic, From: a.From, To: a.To, ClipIDs: []string{cv.ClipID},
					Message: msg, Fix: r.breakShot(cv, p, a)})
			}
			if !p.Picture || p.Muted || slices.Contains(cv.OffCamera, p.Member) {
				continue
			}
			for _, t := range editspec.MergeSpans(p.Speaking, 1.5) {
				if t.To-t.From < lim {
					continue
				}
				unseen := editspec.SpansMinus([]editspec.Span{t}, p.Seen)
				if editspec.SpanSeconds(unseen) < lim {
					continue
				}
				r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "conversation_unseen", Basis: models.QAHeuristic, From: t.From, To: t.To, ClipIDs: []string{cv.ClipID},
					Message: fmt.Sprintf("%s talks for %s (%s) without being seen.", p.Name, secs(math.Round(t.To-t.From)), clock(t.From)+"–"+clock(t.To)),
					Fix:     r.showAt(cv, p, unseen[0].From, fmt.Sprintf("Show %s while they talk.", p.Name))})
			}
		}
		// Screen time against talking time.
		length := cv.To - cv.From
		var talk float64
		for _, p := range cv.People {
			if p.Picture && !p.Muted && !slices.Contains(cv.OffCamera, p.Member) {
				talk += editspec.SpanSeconds(p.Speaking)
			}
		}
		if length < 20 || talk < 10 {
			continue
		}
		for _, p := range cv.People {
			if !p.Picture || p.Muted || slices.Contains(cv.OffCamera, p.Member) {
				continue
			}
			share := editspec.SpanSeconds(p.Speaking) / talk
			seen := editspec.SpanSeconds(p.Seen) / length
			if share < ConvTalkShare || seen >= share*ConvSeenRatio {
				continue
			}
			r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "conversation_balance", Basis: models.QAHeuristic, From: cv.From, To: cv.To, ClipIDs: []string{cv.ClipID},
				Message: fmt.Sprintf("%s does %.0f%% of the talking but is on screen only %.0f%% of the time.", p.Name, share*100, seen*100),
				Fix:     &models.QAFix{Summary: fmt.Sprintf("Show %s more while they talk: make the cut again in a style that follows the talker, or change angles on the timeline.", p.Name)}})
		}
	}
	r.check("conversation_picture", models.QAHeuristic, fmt.Sprintf("no shot of one person longer than the style's limit (default %.0f s); nobody talks that long unseen; someone doing ≥%.0f%% of the talking is on screen at least %.0f%% of that share", ConvMaxShot, ConvTalkShare*100, ConvSeenRatio*100),
		"conversation_one_sided", "conversation_unseen", "conversation_balance")
}

// clipSpans is the part of spans inside [from, to).
func clipSpans(spans []editspec.Span, from, to float64) []editspec.Span {
	var out []editspec.Span
	for _, s := range spans {
		a, b := math.Max(s.From, from), math.Min(s.To, to)
		if b > a {
			out = append(out, editspec.Span{From: a, To: b})
		}
	}
	return out
}

// convGlitches reports the recordings' frozen pictures where they are on
// screen and sound dropouts where they are heard, and recordings that may be
// out of step.
func (r *run) convGlitches() {
	for _, cv := range r.in.Conversations {
		for _, f := range r.convFreezes(cv) {
			p := cv.person(f.member)
			r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "conversation_freeze", Basis: models.QAObserved, From: f.from, To: f.to, ClipIDs: []string{cv.ClipID},
				Message: fmt.Sprintf("%s's picture freezes for %s at %s (their recording stalls there).", p.Name, secs(math.Round((f.to-f.from)*10)/10), clock(f.from)),
				Fix:     r.coverFreeze(cv, *p, f.from, f.to)})
		}
		for _, g := range cv.Dropouts {
			p := cv.person(g.Member)
			if p == nil || !p.Sound || p.Muted {
				continue
			}
			a, okA := cv.Timeline(math.Max(g.From, cv.SourceIn))
			b, okB := cv.Timeline(math.Min(g.To, cv.SourceOut-1e-6))
			if !okA || !okB || b-a < 0.05 {
				continue
			}
			r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "conversation_dropout", Basis: models.QAObserved, From: a, To: b, ClipIDs: []string{cv.ClipID},
				Message: fmt.Sprintf("%s's sound drops out for %s at %s.", p.Name, secs(math.Round((b-a)*10)/10), clock(a)),
				Fix:     &models.QAFix{Summary: "If no words are lost there, cut the moment out (the conversation's clean-up suggests where); otherwise leave it."}})
		}
		for _, n := range cv.Unsure {
			r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "conversation_sync", Basis: models.QAHeuristic, From: cv.From, To: cv.To, ClipIDs: []string{cv.ClipID},
				Message: fmt.Sprintf("%s's recording may be out of step with the others: lining it up was unsure.", n),
				Fix:     &models.QAFix{Summary: "Check it on the conversation's page: nudge it until lips and voice match, or line it up with a clap."}})
		}
	}
	r.check("conversation_glitches", models.QAObserved, fmt.Sprintf("no frozen webcam picture ≥%.1f s on screen (the recordings' own evidence); no sound dropout where a person is heard; every recording lined up surely", ConvGlitchMin),
		"conversation_freeze", "conversation_dropout", "conversation_sync")
}

type convFreeze struct {
	member   string
	from, to float64 // timeline seconds the frozen picture is on screen
}

// convFreezes maps each member's frozen stretches (their recording's evidence)
// onto the timeline where that member is on screen.
func (r *run) convFreezes(cv Conversation) []convFreeze {
	var out []convFreeze
	for _, p := range cv.People {
		if !p.Picture {
			continue
		}
		// On the conversation's clock: the recording's evidence and what clean-up found.
		var spans []editspec.Span
		if ev, ok := r.in.Source[p.AssetID]; ok && ev.Available {
			for _, z := range ev.Freezes {
				spans = append(spans, editspec.Span{From: p.ClockAt(z.From), To: p.ClockAt(z.To)})
			}
		}
		for _, g := range cv.Freezes {
			if g.Member == p.Member {
				spans = append(spans, editspec.Span{From: g.From, To: g.To})
			}
		}
		for _, z := range editspec.MergeSpans(spans, 0) {
			a, okA := cv.Timeline(math.Max(z.From, cv.SourceIn))
			b, okB := cv.Timeline(math.Min(z.To, cv.SourceOut-1e-6))
			if !okA || !okB {
				continue
			}
			for _, s := range clipSpans(p.Seen, a, b) {
				if s.To-s.From >= ConvGlitchMin {
					out = append(out, convFreeze{member: p.Member, from: s.From, to: s.To})
				}
			}
		}
	}
	return out
}

// convFreezeSpans are the frozen stretches of members on screen in timeline
// frames: the conversation check reports them, so frozen-frame detection
// counts them as explained.
func (r *run) convFreezeSpans() []fspan {
	var out []fspan
	for _, cv := range r.in.Conversations {
		for _, f := range r.convFreezes(cv) {
			name := f.member
			if p := cv.person(f.member); p != nil {
				name = p.Name
			}
			out = append(out, fspan{int(math.Round(f.from * r.fps)), int(math.Round(f.to * r.fps)), name + "'s recording freezes here (see the conversation check)"})
		}
	}
	return out
}

// ---- fixes: new shots for the clip ----

// shotsFix is a fix that changes a conversation clip's shots.
func (r *run) shotsFix(clipID, summary string, edit func(shots []editspec.Shot) []editspec.Shot) *models.QAFix {
	fix := &models.QAFix{Summary: summary}
	sc := r.specClip(clipID)
	if sc == nil || sc.Locked {
		return fix
	}
	shots := edit(editspec.ClipShots(r.in.Spec, *sc))
	slices.SortStableFunc(shots, func(a, b editspec.Shot) int {
		switch {
		case a.At < b.At:
			return -1
		case a.At > b.At:
			return 1
		}
		return 0
	})
	fix.Ops = []editspec.Op{setOp("updateClip", clipID, map[string]any{"shots": shots})}
	return fix
}

// splitLayout is the layout that shows everyone with a picture in this frame.
func (r *run) splitLayout(cv Conversation) string {
	n := 0
	for _, p := range cv.People {
		if p.Picture && !slices.Contains(cv.OffCamera, p.Member) {
			n++
		}
	}
	switch {
	case n > 3:
		return editspec.LayoutGrid
	case r.plan.Height > r.plan.Width:
		return editspec.LayoutStacked
	}
	return editspec.LayoutSideBySide
}

// breakShot breaks a long shot of one person with everyone together for a
// few seconds in the middle.
func (r *run) breakShot(cv Conversation, p editspec.PersonPlay, a editspec.Span) *models.QAFix {
	mid := round3(cv.Clock((a.From + a.To) / 2))
	back := round3(cv.Clock(math.Min(a.To-0.5, (a.From+a.To)/2+3)))
	layout := r.splitLayout(cv)
	return r.shotsFix(cv.ClipID, fmt.Sprintf("Cut to everyone together (%s) for a few seconds in the middle.", strings.ToLower(editspec.LayoutLabel(layout))), func(shots []editspec.Shot) []editspec.Shot {
		return append(shots,
			editspec.Shot{At: mid, Layout: layout, Rationale: fmt.Sprintf("Breaks a long shot of %s.", p.Name)},
			editspec.Shot{At: back, Member: p.Member, Rationale: fmt.Sprintf("Back to %s.", p.Name)})
	})
}

// showAt shows a person from a timeline second on.
func (r *run) showAt(cv Conversation, p editspec.PersonPlay, t float64, why string) *models.QAFix {
	at := round3(cv.Clock(t))
	return r.shotsFix(cv.ClipID, fmt.Sprintf("Cut to %s at %s.", p.Name, clock(t)), func(shots []editspec.Shot) []editspec.Shot {
		return append(shots, editspec.Shot{At: at, Member: p.Member, Rationale: why})
	})
}

// coverFreeze shows someone else while a person's picture is frozen, then
// goes back to what was showing.
func (r *run) coverFreeze(cv Conversation, p editspec.PersonPlay, from, to float64) *models.QAFix {
	other := ""
	best := -1.0
	for _, q := range cv.People {
		if q.Member == p.Member || !q.Picture || slices.Contains(cv.OffCamera, q.Member) {
			continue
		}
		if t := editspec.SpanSeconds(clipSpans(q.Speaking, from, to)); t > best {
			other, best = q.Member, t
		}
	}
	if other == "" {
		return &models.QAFix{Summary: "Cut the frozen moment out, or cover it with another shot."}
	}
	o := cv.person(other)
	a, b := round3(cv.Clock(from)), round3(cv.Clock(to))
	return r.shotsFix(cv.ClipID, fmt.Sprintf("Show %s while %s's picture is frozen.", o.Name, p.Name), func(shots []editspec.Shot) []editspec.Shot {
		return editspec.CoverShots(shots, a, b, other, fmt.Sprintf("%s's picture is frozen here.", p.Name), "Back after the frozen moment.")
	})
}
