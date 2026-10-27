package editspec

import (
	"math"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/textalign"
)

// LoudnessSeriesFacts is optionally implemented by Facts: an asset's momentary
// loudness (LUFS, a value every step seconds; the 400 ms window ending at
// (k+1)·step). Who is speaking in a conversation uses it for sound without
// words (laughs, short replies) and to tell a person's voice from another's
// picked up by their microphone.
type LoudnessSeriesFacts interface {
	LoudnessSeries(assetID string) (step float64, lufs []float64, ok bool)
}

// FaceBoxFacts is optionally implemented by Facts: the main face's box in a
// source range (x, y, width, height from the top-left, 0–1; the median of the
// biggest face in each frame that shows one). Layouts use it for matching
// headroom (D102).
type FaceBoxFacts interface {
	FaceBox(assetID string, from, to float64) (box [4]float64, ok bool)
}

// convFacts answers for a spec's conversation source keys (conv:<name>) as if
// they were footage and passes everything else to the facts it wraps.
type convFacts struct {
	base Facts
	out  Output
	srcs map[string]*convSource
	// levelSpeech: the spec evens out voices, so members are leveled too.
	levelSpeech bool
	truePeak    float64
}

// convSource is one pinned conversation as the engine sees it.
type convSource struct {
	name       string
	pin        ConversationPin
	end        float64 // the conversation's length on its clock
	state      string
	inProject  bool
	hasPicture bool
	hasSound   bool
	prec       string
	words      []TranscriptSegment // everyone's words on the conversation's clock
	own        map[string][]TranscriptSegment
	act        map[string][]Span // who is heard speaking when (lazily)
	levels     map[string]float64
	lufs       *float64
	peak       float64
	series     map[int]*memberSeries
	mine       map[string][]TranscriptSegment // each member's own words (bleed left out), on the conversation's clock
}

// Span is a stretch of time [From, To) in seconds.
type Span struct {
	From float64 `json:"from"`
	To   float64 `json:"to"`
}

// withConversations wraps facts so a spec's conversations look like footage.
// A spec without conversations gets its facts back unchanged (existing cuts
// resolve exactly as before).
func withConversations(s *Spec, facts Facts) Facts {
	if facts == nil || convFactsOf(facts) != nil || !HasConversations(s) {
		return facts
	}
	cf := &convFacts{base: facts, out: s.Output, srcs: map[string]*convSource{}, levelSpeech: s.LevelsSpeech(), truePeak: DefaultTruePeak}
	if s.Mix != nil && s.Mix.Loudness != nil && s.Mix.Loudness.TruePeakDBTP != nil {
		cf.truePeak = *s.Mix.Loudness.TruePeakDBTP
	}
	for _, name := range sortedConvNames(s) {
		cf.srcs[name] = cf.build(name, s.Conversations[name])
	}
	return cf
}

// wrapFacts is every wrapper a spec needs: its conversations, then its nested
// cuts (whose own facts are wrapped the same way).
func wrapFacts(s *Spec, facts Facts) Facts {
	return withNested(s, withConversations(s, facts))
}

// convFactsOf finds the conversation facts inside facts (nil when none).
func convFactsOf(f Facts) *convFacts {
	switch v := f.(type) {
	case *convFacts:
		return v
	case *nestFacts:
		if v != nil {
			return convFactsOf(v.base)
		}
	}
	return nil
}

func (cf *convFacts) build(name string, pin ConversationPin) *convSource {
	cs := &convSource{name: name, pin: pin, state: "available", inProject: true, own: map[string][]TranscriptSegment{}}
	precs := map[string]bool{}
	anyMember := false
	for _, m := range pin.Members {
		a, ok := cf.base.Asset(m.AssetID)
		if !ok {
			cs.state, cs.inProject = "missing", false
			continue
		}
		anyMember = true
		if a.State != "" && a.State != "available" && cs.state == "available" {
			cs.state = a.State
		}
		cs.inProject = cs.inProject && a.InProject
		if a.Duration > 0 {
			cs.end = math.Max(cs.end, m.ClockAt(a.Duration))
		}
		if m.HasPicture() && a.HasVideo {
			cs.hasPicture = true
		}
		if m.HasSound() && a.HasAudio {
			cs.hasSound = true
			precs[a.TranscriptPrecision] = true
			cs.own[m.ID] = cf.base.Transcript(m.AssetID)
		}
	}
	if !anyMember {
		cs.state = "missing"
	}
	switch {
	case precs[""] || len(precs) == 0:
		cs.prec = ""
	case precs["segment"]:
		cs.prec = "segment"
	default:
		cs.prec = "word"
	}
	cs.words = cf.mergeTranscripts(cs)
	cf.level(cs)
	return cs
}

// Asset answers for a conversation's source key as if it were footage.
func (cf *convFacts) Asset(id string) (AssetFacts, bool) {
	name := ConvName(id)
	if name == "" {
		return cf.base.Asset(id)
	}
	cs, ok := cf.srcs[name]
	if !ok {
		return AssetFacts{}, false
	}
	return AssetFacts{ID: id, Name: cs.title(), Kind: "conversation", State: cs.state, InProject: cs.inProject, Duration: cs.end,
		HasVideo: cs.hasPicture, HasAudio: cs.hasSound, Width: cf.out.Width, Height: cf.out.Height, FPS: float64(cf.out.FPS),
		TranscriptPrecision: cs.prec}, true
}

func (cs *convSource) title() string {
	var names []string
	for _, m := range cs.pin.Members {
		if m.UseOf() != UsePicture {
			names = append(names, m.Name)
		}
	}
	if len(names) == 0 {
		return "the conversation"
	}
	return "the conversation with " + joinAnd(names)
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// Transcript is a conversation's words on its clock (each person's own,
// named with the member's name), or the wrapped facts' transcript.
func (cf *convFacts) Transcript(id string) []TranscriptSegment {
	if name := ConvName(id); name != "" {
		if cs, ok := cf.srcs[name]; ok {
			return cs.words
		}
		return nil
	}
	return cf.base.Transcript(id)
}

// Loudness implements LoudnessFacts: a conversation's speech loudness after
// its members are leveled, and the highest peak among them.
func (cf *convFacts) Loudness(id string) (float64, float64, bool) {
	if name := ConvName(id); name != "" {
		cs, ok := cf.srcs[name]
		if !ok || cs.lufs == nil {
			return 0, 0, false
		}
		return *cs.lufs, cs.peak, true
	}
	if lf, ok := cf.base.(LoudnessFacts); ok {
		return lf.Loudness(id)
	}
	return 0, 0, false
}

// RangeLoudness implements RangeLoudnessFacts (a conversation counts as a
// whole: its members are leveled inside it).
func (cf *convFacts) RangeLoudness(id string, from, to float64) (float64, bool) {
	if name := ConvName(id); name != "" {
		cs, ok := cf.srcs[name]
		if !ok || cs.lufs == nil {
			return 0, false
		}
		return *cs.lufs, true
	}
	if rf, ok := cf.base.(RangeLoudnessFacts); ok {
		return rf.RangeLoudness(id, from, to)
	}
	return 0, false
}

// AudioChannels implements ChannelFacts (a conversation is mixed in stereo).
func (cf *convFacts) AudioChannels(id string) int {
	if IsConversation(id) {
		return 2
	}
	if c, ok := cf.base.(ChannelFacts); ok {
		return c.AudioChannels(id)
	}
	return 0
}

// FaceFocus implements FaceFacts (none for a conversation: its members are framed inside it).
func (cf *convFacts) FaceFocus(id string, from, to float64) (float64, float64, bool) {
	if IsConversation(id) {
		return 0, 0, false
	}
	if f, ok := cf.base.(FaceFacts); ok {
		return f.FaceFocus(id, from, to)
	}
	return 0, 0, false
}

// Subject implements FollowFacts (none for a conversation: its members are framed inside it).
func (cf *convFacts) Subject(id string, from, to float64) ([]SubjectSample, []float64, bool) {
	if f, ok := cf.base.(FollowFacts); ok && !IsConversation(id) {
		return f.Subject(id, from, to)
	}
	return nil, nil, false
}

// FaceBox implements FaceBoxFacts.
func (cf *convFacts) FaceBox(id string, from, to float64) ([4]float64, bool) {
	if f, ok := cf.base.(FaceBoxFacts); ok && !IsConversation(id) {
		return f.FaceBox(id, from, to)
	}
	return [4]float64{}, false
}

// LoudnessSeries implements LoudnessSeriesFacts.
func (cf *convFacts) LoudnessSeries(id string) (float64, []float64, bool) {
	if f, ok := cf.base.(LoudnessSeriesFacts); ok && !IsConversation(id) {
		return f.LoudnessSeries(id)
	}
	return 0, nil, false
}

// Beats implements MusicFacts.
func (cf *convFacts) Beats(id string) ([]float64, bool) {
	if f, ok := cf.base.(MusicFacts); ok {
		return f.Beats(id)
	}
	return nil, false
}

// MusicGrid implements MusicGridFacts.
func (cf *convFacts) MusicGrid(id string) (*MusicGrid, bool) {
	if f, ok := cf.base.(MusicGridFacts); ok {
		return f.MusicGrid(id)
	}
	return nil, false
}

// Motion implements MotionFacts.
func (cf *convFacts) Motion(hash string) (MotionInfo, bool) { return motionInfo(cf.base, hash) }

// Resource implements ResourceFacts.
func (cf *convFacts) Resource(id, sha string) (ResourceInfo, bool) {
	return resourceInfo(cf.base, ResourcePin{ID: id, SHA256: sha})
}

// Nested implements NestedFacts.
func (cf *convFacts) Nested(projectID, revisionID string) (*NestedSource, bool) {
	if nf, ok := cf.base.(NestedFacts); ok {
		return nf.Nested(projectID, revisionID)
	}
	return nil, false
}

// ---- everyone's words on the conversation's clock ----

// bleedLag is how much later another person's voice can arrive in someone
// else's microphone (an online call's delay plus the speakers): the same word
// heard that much later in another track is that track picking it up.
const bleedLag = 0.7

// mergeTranscripts puts every sound member's own words on the conversation's
// clock, named with the member's name, leaving out what their microphone only
// picked up: a second voice in their own transcript (a minority label), and
// words another member said a moment earlier (the same word, up to bleedLag
// later, D101).
func (cf *convFacts) mergeTranscripts(cs *convSource) []TranscriptSegment {
	type word struct {
		member  int
		seg     int
		idx     int
		norm    string
		from    float64
		dropped bool
	}
	var segs []TranscriptSegment
	var segMember []int
	for mi, m := range cs.pin.Members {
		own, ok := cs.own[m.ID]
		if !ok {
			continue
		}
		others := cf.otherLabels(cs, mi, own)
		for _, sg := range own {
			if others[strings.TrimSpace(sg.Speaker)] {
				continue // another voice their microphone picked up
			}
			ts := TranscriptSegment{From: round3(m.ClockAt(sg.From)), To: round3(m.ClockAt(sg.To)), Text: sg.Text, Speaker: m.Name}
			for _, w := range sg.Words {
				ts.Words = append(ts.Words, TranscriptWord{Text: w.Text, From: round3(m.ClockAt(w.From)), To: round3(m.ClockAt(w.To))})
			}
			segs = append(segs, ts)
			segMember = append(segMember, mi)
		}
	}
	drop := map[[2]int]bool{}
	// Measured: a word its own track hears far below its speaking level while
	// another member's track is at theirs is that member's voice picked up —
	// whatever the recognizer made of it (D101).
	for si, sg := range segs {
		for wi, w := range sg.Words {
			if cf.heardElsewhere(cs, segMember[si], w.From, w.To) {
				drop[[2]int{si, wi}] = true
			}
		}
	}
	// The same word in two tracks: the later one is the pick-up.
	var ws []*word
	for si, sg := range segs {
		for wi, w := range sg.Words {
			n := normWord(w.Text)
			if len([]rune(n)) < 2 || drop[[2]int{si, wi}] {
				continue
			}
			ws = append(ws, &word{member: segMember[si], seg: si, idx: wi, norm: n, from: w.From})
		}
	}
	sort.SliceStable(ws, func(i, j int) bool { return ws[i].from < ws[j].from })
	byText := map[string][]*word{}
	for _, w := range ws {
		byText[w.norm] = append(byText[w.norm], w)
	}
	for _, list := range byText {
		for j, w := range list {
			for i := j - 1; i >= 0; i-- {
				o := list[i]
				d := w.from - o.from
				if d > bleedLag {
					break
				}
				if o.member == w.member || o.dropped {
					continue
				}
				// With the tracks' loudness measured, a word is a pick-up only
				// when its track hears it well below its own speaking level (two
				// people laughing together say "ha" at once, both for real);
				// without measurements, the later of two copies is.
				if pick, known := cf.pickedUp(cs, w.member, w.from); known {
					if pick {
						w.dropped = true
						drop[[2]int{w.seg, w.idx}] = true
						break
					}
					continue
				}
				if d > 0.03 {
					w.dropped = true
					drop[[2]int{w.seg, w.idx}] = true
					break
				}
			}
		}
	}
	// A stretch that is mostly picked up is all picked up (the few words left
	// are the recognizer's guesses at the other voice).
	for si, sg := range segs {
		n := 0
		for wi := range sg.Words {
			if drop[[2]int{si, wi}] {
				n++
			}
		}
		if n > 0 && float64(n) >= 0.6*float64(len(sg.Words)) {
			for wi := range sg.Words {
				drop[[2]int{si, wi}] = true
			}
		}
	}
	var out []TranscriptSegment
	cs.mine = map[string][]TranscriptSegment{}
	for si, sg := range segs {
		mid := cs.pin.Members[segMember[si]].ID
		if len(sg.Words) == 0 {
			out = append(out, sg)
			cs.mine[mid] = append(cs.mine[mid], sg)
			continue
		}
		var kept []TranscriptWord
		var text []string
		for wi, w := range sg.Words {
			if drop[[2]int{si, wi}] {
				continue
			}
			kept = append(kept, w)
			text = append(text, strings.TrimSpace(w.Text))
		}
		if len(kept) == 0 {
			continue
		}
		if len(kept) != len(sg.Words) {
			sg.Words = kept
			sg.Text = textalign.JoinWords(text)
			sg.From, sg.To = math.Min(sg.From, kept[0].From), math.Max(kept[len(kept)-1].To, kept[0].From)
		}
		out = append(out, sg)
		cs.mine[mid] = append(cs.mine[mid], sg)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out
}

// pickUpBelow is how far under its own speaking level a word must sound in a
// member's track to be another voice their microphone picked up when the same
// word is in the other's track (dB); farAbove how far under it must be when
// only the other track's level says so, while that track is within
// otherSpeaking of its own speaking level (a quiet "mm-hmm" stays).
const (
	pickUpBelow   = 10.0
	farBelow      = 18.0
	otherSpeaking = 8.0
)

// heardElsewhere reports whether a word in member mi's track (conversation
// time [from, to)) is far below that track's speaking level while another
// member's track is at its own then or a moment before (the call's delay):
// their voice, picked up.
func (cf *convFacts) heardElsewhere(cs *convSource, mi int, from, to float64) bool {
	ms := cf.seriesOf(cs, mi)
	if ms == nil {
		return false
	}
	m := cs.pin.Members[mi]
	level := math.Inf(-1)
	for t := from; t <= math.Max(from, to-0.05)+1e-9; t += 0.1 {
		if v, ok := ms.at(m.SourceAt(t + 0.1)); ok {
			level = math.Max(level, v)
		}
	}
	if math.IsInf(level, -1) || level > ms.speech-farBelow {
		return false
	}
	for oi, o := range cs.pin.Members {
		if oi == mi || !o.HasSound() {
			continue
		}
		os := cf.seriesOf(cs, oi)
		if os == nil {
			continue
		}
		for t := from - bleedLag; t <= to+1e-9; t += 0.1 {
			if v, ok := os.at(o.SourceAt(t + 0.1)); ok && v >= os.speech-otherSpeaking {
				return true
			}
		}
	}
	return false
}

// pickedUp reports whether a word heard at conversation time t in member
// mi's track is far below that track's own speaking level (another voice the
// microphone picked up); known is false without loudness measurements.
func (cf *convFacts) pickedUp(cs *convSource, mi int, t float64) (pick, known bool) {
	ls := cf.seriesOf(cs, mi)
	if ls == nil {
		return false, false
	}
	v, ok := ls.at(cs.pin.Members[mi].SourceAt(t + 0.1))
	if !ok {
		return false, false
	}
	return v < ls.speech-pickUpBelow, true
}

// memberSeries is a member recording's momentary loudness with its speaking
// level (the median at their words) and its background (the 10th percentile).
type memberSeries struct {
	step          float64
	lufs          []float64
	speech, noise float64
}

// at is the loudness of the 400 ms around recording time tau.
func (ms *memberSeries) at(tau float64) (float64, bool) {
	if ms == nil || ms.step <= 0 {
		return 0, false
	}
	k := int(math.Round((tau+0.2)/ms.step)) - 1
	if k < 0 || k >= len(ms.lufs) {
		return 0, false
	}
	v := ms.lufs[k]
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return -120, true
	}
	return v, true
}

// seriesOf loads (once) a member's loudness and levels; nil without measurements.
func (cf *convFacts) seriesOf(cs *convSource, mi int) *memberSeries {
	if cs.series == nil {
		cs.series = map[int]*memberSeries{}
	}
	if ms, done := cs.series[mi]; done {
		return ms
	}
	cs.series[mi] = nil
	lf, ok := cf.base.(LoudnessSeriesFacts)
	if !ok || mi < 0 || mi >= len(cs.pin.Members) {
		return nil
	}
	m := cs.pin.Members[mi]
	step, vals, ok := lf.LoudnessSeries(m.AssetID)
	if !ok || step <= 0 || len(vals) == 0 {
		return nil
	}
	ms := &memberSeries{step: step, lufs: vals}
	var at []float64
	for _, sg := range cf.base.Transcript(m.AssetID) {
		for _, w := range sg.Words {
			if v, ok := ms.at((w.From + w.To) / 2); ok && v > -70 {
				at = append(at, v)
			}
		}
	}
	all := make([]float64, 0, len(vals))
	for _, v := range vals {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			all = append(all, math.Max(v, -120))
		}
	}
	if len(all) == 0 {
		return nil
	}
	sort.Float64s(all)
	ms.noise = all[len(all)/10]
	// Their speaking level: the loud end of what the track hears above its
	// background (their own voice is the loudest thing in their microphone),
	// or the middle of their words when that is higher — the words alone can
	// be pulled down by another voice the recognizer took for theirs.
	var active []float64
	for _, v := range all {
		if v > ms.noise+6 {
			active = append(active, v)
		}
	}
	if len(active) > 0 {
		ms.speech = active[len(active)*95/100]
	} else {
		ms.speech = all[len(all)*9/10]
	}
	if len(at) > 0 {
		sort.Float64s(at)
		ms.speech = math.Max(ms.speech, at[len(at)/2])
	}
	cs.series[mi] = ms
	return ms
}

// otherLabels are the voices in a member's own transcript that are someone
// else picked up by their microphone. Measured: every voice clearly quieter
// (pickUpBelow) than the loudest one in that track — their own voice is the
// loudest thing in their microphone, however little they say. Without
// measurements: every voice but the one that says most of the words.
func (cf *convFacts) otherLabels(cs *convSource, mi int, own []TranscriptSegment) map[string]bool {
	out := map[string]bool{}
	labels := map[string][]float64{}
	for _, sg := range own {
		labels[strings.TrimSpace(sg.Speaker)] = labels[strings.TrimSpace(sg.Speaker)]
	}
	if len(labels) < 2 {
		return out
	}
	if ms := cf.seriesOf(cs, mi); ms != nil {
		m := cs.pin.Members[mi]
		for _, sg := range own {
			k := strings.TrimSpace(sg.Speaker)
			for _, w := range sg.Words {
				if v, ok := ms.at(m.SourceAt((w.From+w.To)/2 + 0.1)); ok && v > -70 {
					labels[k] = append(labels[k], v)
				}
			}
		}
		med := map[string]float64{}
		loud := math.Inf(-1)
		for k, vs := range labels {
			if len(vs) == 0 {
				continue
			}
			sort.Float64s(vs)
			med[k] = vs[len(vs)/2]
			loud = math.Max(loud, med[k])
		}
		if !math.IsInf(loud, -1) {
			for k, v := range med {
				if k != "" && v < loud-pickUpBelow {
					out[k] = true
				}
			}
			return out
		}
	}
	major := majorLabel(own)
	if major == "" {
		return out
	}
	for k := range labels {
		if k != major && k != "" {
			out[k] = true
		}
	}
	return out
}

// majorLabel is the voice that says most of a recording's words when the
// transcript tells several apart ("" when one voice, or none clearly leads).
func majorLabel(segs []TranscriptSegment) string {
	count := map[string]int{}
	total := 0
	for _, sg := range segs {
		n := len(sg.Words)
		if n == 0 {
			n = len(strings.Fields(sg.Text))
		}
		count[strings.TrimSpace(sg.Speaker)] += n
		total += n
	}
	if len(count) < 2 || total == 0 {
		return ""
	}
	best, bestN := "", -1
	for k, n := range count {
		if k != "" && (n > bestN || (n == bestN && k < best)) {
			best, bestN = k, n
		}
	}
	if float64(bestN) < 0.5*float64(total) {
		return ""
	}
	return best
}

// ---- levels ----

// level measures each sound member's speech loudness and, when the cut
// evens out voices, the gain that brings each toward the typical level of
// the people in it (their speaking time weights the median); the conversation
// then plays at that level.
func (cf *convFacts) level(cs *convSource) {
	cs.levels = map[string]float64{}
	rf, _ := cf.base.(RangeLoudnessFacts)
	lf, _ := cf.base.(LoudnessFacts)
	type cand struct {
		id     string
		lufs   float64
		weight float64
		peak   float64
	}
	var cands []cand
	for _, m := range cs.pin.Members {
		if !m.HasSound() {
			continue
		}
		a, ok := cf.base.Asset(m.AssetID)
		if !ok || !a.HasAudio {
			continue
		}
		l, have := 0.0, false
		if rf != nil && a.Duration > 0 {
			l, have = rf.RangeLoudness(m.AssetID, 0, a.Duration)
		}
		peak := 0.0
		if lf != nil {
			ll, tp, ok := lf.Loudness(m.AssetID)
			if !have && ok {
				l, have = ll, true
			}
			if ok && !math.IsInf(tp, 0) && !math.IsNaN(tp) {
				peak = PeakInMix(cf.base, m.AssetID, tp)
			}
		}
		if !have || math.IsNaN(l) || math.IsInf(l, 0) || l < -70 {
			continue
		}
		w := 0.0
		for _, sg := range cs.own[m.ID] {
			w += math.Max(0, sg.To-sg.From)
		}
		cands = append(cands, cand{m.ID, round3(l), math.Max(w, 1), peak})
	}
	if len(cands) == 0 {
		return
	}
	sorted := append([]cand(nil), cands...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].lufs < sorted[j].lufs })
	totalW := 0.0
	for _, c := range sorted {
		totalW += c.weight
	}
	target, acc := sorted[len(sorted)-1].lufs, 0.0
	for _, c := range sorted {
		acc += c.weight
		if acc >= totalW/2 {
			target = c.lufs
			break
		}
	}
	peak := math.Inf(-1)
	if cf.levelSpeech {
		ceiling := cf.truePeak - CodecPeakMarginDB
		for _, c := range cands {
			g := math.Max(-MaxLevelDB, math.Min(MaxLevelDB, target-c.lufs))
			if g > 0 && c.peak+g > ceiling {
				g = math.Max(0, ceiling-c.peak)
			}
			g = math.Round(g*10) / 10
			cs.levels[c.id] = g
			peak = math.Max(peak, c.peak+g)
		}
		t := target
		cs.lufs = &t
	} else {
		e, w := 0.0, 0.0
		for _, c := range cands {
			e += c.weight * math.Pow(10, c.lufs/10)
			w += c.weight
			peak = math.Max(peak, c.peak)
		}
		l := round3(10 * math.Log10(e/w))
		cs.lufs = &l
	}
	cs.peak = peak
}
