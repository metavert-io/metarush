package editspec

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Nested cuts (D84): a clip can show another project's cut, pinned to one
// revision. Its source key is cut:<projectId>/<revisionId>, and to the rest of
// the engine it looks like footage: its length is the cut's length, its
// "transcript" is the speech the cut plays, its loudness the cut's estimated
// loudness. So the picture timeline, split edits, ducking, captions, the
// speech check and the cut report work on it unchanged. Resolve draws its
// picture from a nested plan resolved at this cut's size and frame rate, and
// flattens its sound into this plan's audio with the outer clip's envelope
// multiplied in, so Go stays the only mixer.

// NestedPrefix starts a nested cut's source key.
const NestedPrefix = "cut:"

// MaxNestDepth is how deep cuts may nest (a cut in a cut in a cut).
const MaxNestDepth = 3

var nestedKey = regexp.MustCompile(`^cut:(prj_[0-9a-z]{26})/(rev_[0-9a-z]{26})$`)

// NestedKey is the source key of a project's cut at one revision.
func NestedKey(projectID, revisionID string) string {
	return NestedPrefix + projectID + "/" + revisionID
}

// NestedRef splits a nested cut's source key.
func NestedRef(assetID string) (projectID, revisionID string, ok bool) {
	m := nestedKey.FindStringSubmatch(assetID)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// IsNested reports a nested cut's source key.
func IsNested(assetID string) bool { return strings.HasPrefix(assetID, NestedPrefix) }

// NestedSource is what facts know about a nested cut.
type NestedSource struct {
	ProjectID   string
	RevisionID  string
	ProjectName string
	Title       string
	Number      int
	// Spec is that revision's cut; Facts are for it (its own project).
	Spec  *Spec
	Facts Facts
	// Visible: the project can use it (the source project is linked).
	Visible bool
	// Cycle: the cut contains the project it would be nested in (directly or
	// deeper), or is that project's own.
	Cycle bool
	// Missing: there is no such revision.
	Missing bool
	// LatestID and LatestNumber are the newest version on that cut's branch.
	LatestID     string
	LatestNumber int
}

// NestedFacts is optionally implemented by Facts: the cuts a spec nests.
type NestedFacts interface {
	Nested(projectID, revisionID string) (*NestedSource, bool)
}

// NestedPlan is a nested cut in the RenderPlan.
type NestedPlan struct {
	ProjectID   string `json:"projectId"`
	RevisionID  string `json:"revisionId"`
	Number      int    `json:"number"`
	Title       string `json:"title,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
	// NewerRevisionID and NewerNumber name the latest version of that cut when
	// it is newer than this one ("a newer version is available").
	NewerRevisionID string `json:"newerRevisionId,omitempty"`
	NewerNumber     int    `json:"newerNumber,omitempty"`
	// Plan is the cut as it plays here (this cut's size and frame rate). Its
	// sound is already in the outer plan's audio; its captions are left out
	// when the outer cut has captions on (one caption style per video).
	Plan *RenderPlan `json:"plan"`
}

// NestedKeys lists the nested cuts a spec plays, in clip order.
func NestedKeys(s *Spec) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range s.Clips {
		if IsNested(c.AssetID) && !seen[c.AssetID] {
			seen[c.AssetID] = true
			out = append(out, c.AssetID)
		}
	}
	return out
}

// nestedCut is a nested cut resolved for one outer spec.
type nestedCut struct {
	key   string
	src   *NestedSource
	spec  *Spec       // the nested cut at the outer size and frame rate
	facts Facts       // its facts (its own nested cuts resolved)
	plan  *RenderPlan // its plan
	dur   float64
	words []TranscriptSegment // the speech it plays (its timeline seconds)
	prec  string
	sound bool
	lufs  *float64
	peak  float64
	depth int
	// tooDeep: nesting goes past MaxNestDepth; the cut isn't resolved.
	tooDeep bool
	// shots are its picture switches on its timeline (footage, computed once).
	shots *FootageShots
}

// nestFacts answers for nested cuts' source keys and passes everything else
// to the facts it wraps.
type nestFacts struct {
	base  Facts
	cuts  map[string]*nestedCut
	depth int
	outer Output
}

// withNested wraps facts so a spec's nested cuts look like footage. A spec
// without nested cuts gets its facts back unchanged (existing cuts resolve
// exactly as before).
func withNested(s *Spec, facts Facts) Facts { return withNestedAt(s, facts, 0) }

func withNestedAt(s *Spec, facts Facts, depth int) Facts {
	if facts == nil {
		return nil
	}
	if _, already := facts.(*nestFacts); already {
		return facts
	}
	keys := NestedKeys(s)
	if len(keys) == 0 {
		return facts
	}
	nf := &nestFacts{base: facts, cuts: map[string]*nestedCut{}, depth: depth, outer: s.Output}
	src, _ := facts.(NestedFacts)
	for _, k := range keys {
		nc := &nestedCut{key: k, depth: depth + 1}
		nf.cuts[k] = nc
		p, r, ok := NestedRef(k)
		if !ok || src == nil {
			continue
		}
		ns, ok := src.Nested(p, r)
		if !ok || ns == nil {
			continue
		}
		nc.src = ns
		if ns.Missing || ns.Cycle || ns.Spec == nil {
			continue
		}
		if depth+1 > MaxNestDepth {
			nc.tooDeep = true
			continue
		}
		inner := Clone(ns.Spec)
		Normalize(inner)
		reshaped := inner.Output.Width != s.Output.Width || inner.Output.Height != s.Output.Height
		inner.Output.Preset, inner.Output.Width, inner.Output.Height = s.Output.Preset, s.Output.Width, s.Output.Height
		inner.Output.FPS, inner.Output.Platform = s.Output.FPS, s.Output.Platform
		nc.spec = inner
		nc.facts = withNestedAt(inner, withConversations(inner, ns.Facts), depth+1)
		if tooDeep(nc.facts) {
			// Somewhere inside it, cuts nest past the limit: refused here too.
			nc.tooDeep, nc.spec, nc.facts = true, nil, nil
			continue
		}
		if reshaped && nameBarOf(inner) != nil {
			reframeNameBars(inner, nc.facts) // its bar covers the names where this shape's crops put them (D182)
		}
		nc.plan = resolve(Clone(inner), nc.facts, ns.RevisionID, ns.ProjectID, "")
		nc.dur = float64(nc.plan.DurationInFrames) / float64(inner.Output.FPS)
		nc.words, nc.prec = nestedTranscript(inner, nc.facts)
		nc.sound = len(innerSounds(nc.plan)) > 0
		if m := nc.plan.Mix; m != nil && m.EstimatedLUFS != nil {
			l := *m.EstimatedLUFS + m.MasterGainDB
			nc.lufs = &l
			nc.peak = math.Min(0, l+12) // a cut's crest: speech peaks sit about 12 dB above its loudness
		}
	}
	return nf
}

// tooDeep reports nesting past MaxNestDepth anywhere inside these facts.
func tooDeep(f Facts) bool {
	nf, ok := f.(*nestFacts)
	if !ok || nf == nil {
		return false
	}
	for _, nc := range nf.cuts {
		if nc.tooDeep || tooDeep(nc.facts) {
			return true
		}
	}
	return false
}

// Asset answers for a nested cut's source key as if it were footage.
func (f *nestFacts) Asset(id string) (AssetFacts, bool) {
	nc, ok := f.cuts[id]
	if !ok {
		return f.base.Asset(id)
	}
	af := AssetFacts{ID: id, Name: nc.name(), Kind: "cut", State: "available", HasVideo: true,
		Width: f.outer.Width, Height: f.outer.Height, FPS: float64(f.outer.FPS)}
	switch {
	case nc.src == nil || nc.src.Missing:
		return AssetFacts{}, false
	case nc.plan == nil:
		af.State = "unavailable"
		af.InProject = nc.src.Visible
		return af, true
	}
	af.InProject = nc.src.Visible && !nc.src.Cycle
	af.Duration = nc.dur
	af.HasAudio = nc.sound
	af.TranscriptPrecision = nc.prec
	return af, true
}

func (nc *nestedCut) name() string {
	if nc.src == nil {
		return "a cut from another project"
	}
	t := nc.src.Title
	if t == "" {
		t = nc.src.ProjectName
	}
	if t == "" {
		t = "the cut"
	}
	return t
}

// Transcript is a nested cut's speech (its timeline seconds).
func (f *nestFacts) Transcript(id string) []TranscriptSegment {
	if nc, ok := f.cuts[id]; ok {
		return nc.words
	}
	return f.base.Transcript(id)
}

// Loudness implements LoudnessFacts.
func (f *nestFacts) Loudness(id string) (float64, float64, bool) {
	if nc, ok := f.cuts[id]; ok {
		if nc.lufs == nil {
			return 0, 0, false
		}
		return *nc.lufs, nc.peak, true
	}
	if lf, ok := f.base.(LoudnessFacts); ok {
		return lf.Loudness(id)
	}
	return 0, 0, false
}

// RangeLoudness implements RangeLoudnessFacts (none for nested cuts: they are
// leveled as a whole).
func (f *nestFacts) RangeLoudness(id string, from, to float64) (float64, bool) {
	if _, ok := f.cuts[id]; ok {
		return 0, false
	}
	if rf, ok := f.base.(RangeLoudnessFacts); ok {
		return rf.RangeLoudness(id, from, to)
	}
	return 0, false
}

// AudioChannels implements ChannelFacts (a nested cut is mixed in stereo).
func (f *nestFacts) AudioChannels(id string) int {
	if _, ok := f.cuts[id]; ok {
		return 2
	}
	if cf, ok := f.base.(ChannelFacts); ok {
		return cf.AudioChannels(id)
	}
	return 0
}

// Motion implements MotionFacts.
func (f *nestFacts) Motion(hash string) (MotionInfo, bool) { return motionInfo(f.base, hash) }

// Subject implements FollowFacts for the outer cut's own footage (a nested
// cut follows its own clips with its own facts). FaceFocus is deliberately
// not passed through here: cuts with nested cuts have always kept the middle,
// and revisions must render the same over time (D73).
func (f *nestFacts) Subject(id string, from, to float64) ([]SubjectSample, []float64, bool) {
	if ff, ok := f.base.(FollowFacts); ok && !IsNested(id) {
		return ff.Subject(id, from, to)
	}
	return nil, nil, false
}

// MusicGrid implements MusicGridFacts (music with an ending in a cut that
// nests another keeps its phrases, D120).
func (f *nestFacts) MusicGrid(id string) (*MusicGrid, bool) {
	if g, ok := f.base.(MusicGridFacts); ok {
		return g.MusicGrid(id)
	}
	return nil, false
}

// Resource implements ResourceFacts.
func (f *nestFacts) Resource(id, sha string) (ResourceInfo, bool) {
	return resourceInfo(f.base, ResourcePin{ID: id, SHA256: sha})
}

// Nested implements NestedFacts.
func (f *nestFacts) Nested(projectID, revisionID string) (*NestedSource, bool) {
	if nf, ok := f.base.(NestedFacts); ok {
		return nf.Nested(projectID, revisionID)
	}
	return nil, false
}

// nestedTranscript is the speech a cut plays, as transcript segments on its
// own timeline: each clip's words in its sound window (with who says them)
// and the narration's words (Narrator). Spans with speech but no word timing
// become segments without words.
func nestedTranscript(s *Spec, facts Facts) ([]TranscriptSegment, string) {
	words, unknown, _ := ExpectedSpeech(s, facts)
	var segs []TranscriptSegment
	var cur *TranscriptSegment
	key := ""
	for _, w := range words {
		k := w.Kind + "\x00" + w.ClipID + w.BeatID + "\x00" + w.Speaker
		if cur == nil || k != key || w.From-cur.To > 1.2 {
			segs = append(segs, TranscriptSegment{From: w.From, To: w.To, Speaker: w.Speaker})
			cur = &segs[len(segs)-1]
			key = k
		}
		cur.Words = append(cur.Words, TranscriptWord{Text: w.Text, From: w.From, To: w.To})
		cur.To = math.Max(cur.To, w.To)
		if cur.Text != "" {
			cur.Text += " "
		}
		cur.Text += w.Text
	}
	for _, u := range unknown {
		segs = append(segs, TranscriptSegment{From: u.From, To: u.To, Text: "…"})
	}
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].From < segs[j].From })
	prec := ""
	switch {
	case len(unknown) > 0:
		prec = "segment"
	case len(words) > 0:
		prec = "word"
	}
	return segs, prec
}

// innerSound is one sound a nested plan plays, on its timeline.
type innerSound struct {
	id, assetID, role, bus string
	from, dur, trim        int
	frac                   *float64 // the rest of the trim below a frame (recordings placed to the sample, D119)
	rate                   float64
	gain                   []GainPoint
	aiVoice                bool
	variant                *PlanVariant // a clearer copy that plays instead (D88)
}

// at is where the sound is skip frames into its element: the exact place for
// a sound placed to the sample, else the nearest frame as nested cuts always
// played (D119).
func (snd innerSound) at(trim int, frac *float64, skip int) (int, *float64) {
	if frac == nil {
		return trim + int(math.Round(float64(skip)*snd.rate)), nil
	}
	return SplitTrim(ExactTrim(trim, frac) + float64(skip)*snd.rate)
}

// innerSounds lists a resolved plan's sound sources (clips' own sound and its
// audio elements, nested cuts' sound already among them).
func innerSounds(p *RenderPlan) []innerSound {
	var out []innerSound
	for _, pc := range p.Clips {
		if pc.Muted || !pc.HasAudio || IsNested(pc.AssetID) || IsConversation(pc.AssetID) {
			continue // their sound is among the audio elements
		}
		rate := pc.PlaybackRate
		if rate <= 0 {
			rate = 1
		}
		s := innerSound{id: pc.ID, assetID: pc.AssetID, role: RoleDialogue, bus: BusSource, from: pc.From, dur: pc.DurationInFrames, trim: pc.TrimBefore, rate: rate, gain: pc.Gain}
		if a := pc.Audio; a != nil {
			s.from, s.dur, s.trim, s.gain, s.variant = a.From, a.DurationInFrames, a.TrimBefore, a.Gain, a.Variant
		}
		out = append(out, s)
	}
	for _, a := range p.Audio {
		rate := a.PlaybackRate
		if rate <= 0 {
			rate = 1
		}
		out = append(out, innerSound{id: a.ID, assetID: a.AssetID, role: a.Role, bus: a.Bus, from: a.From, dur: a.DurationInFrames, trim: a.TrimBefore, frac: a.TrimFraction, rate: rate, gain: a.Gain, aiVoice: a.AIVoice, variant: a.Variant})
	}
	return out
}

// attachNested puts the nested cuts' plans into the outer plan and turns each
// nested clip's sound into audio elements: the nested cut's own sounds over
// the part of its timeline the clip's sound window plays, each with its own
// envelope times the outer clip's (volume, transitions, ducking, master).
func attachNested(s *Spec, plan *RenderPlan, nf *nestFacts) {
	if nf == nil || len(nf.cuts) == 0 {
		return
	}
	captionsOn := s.Captions != nil && s.Captions.Enabled
	plan.Nested = map[string]*NestedPlan{}
	for i := range plan.Clips {
		pc := &plan.Clips[i]
		nc := nf.cuts[pc.AssetID]
		if nc == nil || nc.plan == nil {
			continue
		}
		if _, done := plan.Nested[pc.AssetID]; !done {
			inner := *nc.plan
			if captionsOn {
				inner.Captions = nil // the outer cut captions this speech in its own style
			}
			np := &NestedPlan{ProjectID: nc.src.ProjectID, RevisionID: nc.src.RevisionID, Number: nc.src.Number, Title: nc.src.Title, ProjectName: nc.src.ProjectName, Plan: &inner}
			if nc.src.LatestID != "" && nc.src.LatestID != nc.src.RevisionID && nc.src.LatestNumber > nc.src.Number {
				np.NewerRevisionID, np.NewerNumber = nc.src.LatestID, nc.src.LatestNumber
			}
			plan.Nested[pc.AssetID] = np
		}
		from, dur, env := pc.Sound()
		hadSound := !pc.Muted && pc.HasAudio
		// The nested picture plays silently: its sound is the elements below.
		pc.Gain, pc.Audio = nil, nil
		if !hadSound || dur <= 0 {
			continue
		}
		innerFrom := from - pc.From + pc.TrimBefore // where the window starts on the nested timeline
		innerTo := innerFrom + dur
		for _, snd := range innerSounds(nc.plan) {
			a, b := max(snd.from, innerFrom), min(snd.from+snd.dur, innerTo)
			if a >= b {
				continue
			}
			start := from + (a - innerFrom)
			n := b - a
			skip := a - snd.from
			sg, og := snd.gain, env
			offOuter := start - from
			g := envelope(n, func(f int) float64 { return GainAt(sg, float64(skip+f)) * GainAt(og, float64(offOuter+f)) })
			bus := snd.bus
			if bus == "" {
				bus = BusSource
			}
			role := snd.role
			if role == "" {
				role = RoleDialogue
			}
			pa := PlanAudio{ID: pc.ID + "/" + snd.id, AssetID: snd.assetID, Role: role, Bus: bus, From: start, DurationInFrames: n,
				GainDB: 0, PlaybackRate: snd.rate, AIVoice: snd.aiVoice, Gain: g, Nested: pc.ID}
			pa.TrimBefore, pa.TrimFraction = snd.at(snd.trim, snd.frac, skip)
			if v := snd.variant; v != nil { // the nested cut's clearer voice, from the same place
				pa.Variant = &PlanVariant{AssetID: v.AssetID}
				pa.Variant.TrimBefore, pa.Variant.TrimFraction = snd.at(v.TrimBefore, v.TrimFraction, skip)
			}
			plan.Audio = append(plan.Audio, pa)
		}
	}
	sort.SliceStable(plan.Audio, func(i, j int) bool {
		if plan.Audio[i].Nested == "" || plan.Audio[j].Nested == "" {
			return false // the cut's own elements keep their order
		}
		return plan.Audio[i].From < plan.Audio[j].From
	})
}

// nestedIssues validates nested clips.
func nestedIssues(s *Spec, facts Facts) []Issue {
	nf, _ := facts.(*nestFacts)
	var out []Issue
	for i, c := range s.Clips {
		if !IsNested(c.AssetID) {
			continue
		}
		p := fmt.Sprintf("/clips/%d", i)
		if _, _, ok := NestedRef(c.AssetID); !ok {
			out = append(out, errI("bad_cut_ref", p+"/assetId", "%s plays a cut from another project, but doesn't say which one in a way MetaRush can read (assetId: cut:<projectId>/<revisionId>).", upperFirst(clipWords(c))))
			continue
		}
		var nc *nestedCut
		if nf != nil {
			nc = nf.cuts[c.AssetID]
		}
		switch {
		case nc == nil || nc.src == nil:
			// missing_asset is reported with the clip
		case nc.src.Cycle:
			out = append(out, errI("nested_cycle", p+"/assetId", "%s already has this project inside it, so it can't go into this cut (a video can't contain itself).", upperFirst(nc.name())))
		case nc.tooDeep:
			out = append(out, errI("nested_too_deep", p+"/assetId", "%s has cuts inside cuts more than %d levels deep; use a cut that doesn't contain other cuts.", upperFirst(nc.name()), MaxNestDepth))
		case !nc.src.Visible:
			out = append(out, errI("nested_not_linked", p+"/assetId", "%s is in %s, which isn't linked to this project yet; link that project to this one first.", upperFirst(nc.name()), firstNonEmptyStr(nc.src.ProjectName, "another project")))
		}
		if c.Speed != 0 && c.Speed != 1 {
			out = append(out, errI("nested_speed", p+"/speed", "%s is a cut from another project, which plays at its own speed, so it can't be sped up or slowed down.", upperFirst(clipWords(c))))
		}
		if c.Framing != nil {
			out = append(out, warnI("nested_framing", p+"/framing", "%s is a cut from another project, which already fills the frame, so its framing settings are ignored.", upperFirst(clipWords(c))))
		}
		if nc != nil && nc.src != nil && !nc.src.Cycle && nc.src.LatestID != "" && nc.src.LatestID != nc.src.RevisionID && nc.src.LatestNumber > nc.src.Number {
			out = append(out, warnI("nested_newer", p+"/assetId", "A newer version of %s is available (version %d; this cut uses version %d). Update it whenever you want.", nc.name(), nc.src.LatestNumber, nc.src.Number))
		}
		if nc != nil && nc.src != nil && nc.src.Spec != nil {
			cs := Clone(nc.src.Spec)
			Normalize(cs)
			if o := cs.Output; o.Width*s.Output.Height != o.Height*s.Output.Width {
				out = append(out, warnI("nested_reframed", p, "%s was made %s; here it plays %s, so its shots are framed again to fit.", upperFirst(nc.name()), shapeWord(o), shapeWord(s.Output)))
			}
		}
	}
	return out
}

func shapeWord(o Output) string {
	switch {
	case o.Width > o.Height:
		return "landscape"
	case o.Width < o.Height:
		return "vertical"
	}
	return "square"
}

func firstNonEmptyStr(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// NestedInfo describes the nested cuts a spec plays (for cut sheets and the
// dashboard), from facts that implement NestedFacts.
type NestedInfo struct {
	Key          string  `json:"key"`
	ProjectID    string  `json:"projectId"`
	RevisionID   string  `json:"revisionId"`
	ProjectName  string  `json:"projectName"`
	Title        string  `json:"title,omitempty"`
	Number       int     `json:"number"`
	Duration     float64 `json:"duration"`
	LatestID     string  `json:"latestId,omitempty"`
	LatestNumber int     `json:"latestNumber,omitempty"`
	Newer        bool    `json:"newer"`
}

// Nesteds lists a spec's nested cuts with what facts know about them.
func Nesteds(s *Spec, facts Facts) []NestedInfo {
	nf, _ := withNested(s, facts).(*nestFacts)
	var out []NestedInfo
	for _, k := range NestedKeys(s) {
		p, r, _ := NestedRef(k)
		ni := NestedInfo{Key: k, ProjectID: p, RevisionID: r}
		if nf != nil {
			if nc := nf.cuts[k]; nc != nil && nc.src != nil {
				ni.ProjectName, ni.Title, ni.Number, ni.Duration = nc.src.ProjectName, nc.src.Title, nc.src.Number, nc.dur
				ni.LatestID, ni.LatestNumber = nc.src.LatestID, nc.src.LatestNumber
				ni.Newer = nc.src.LatestID != "" && nc.src.LatestID != r && nc.src.LatestNumber > nc.src.Number
			}
		}
		out = append(out, ni)
	}
	return out
}

// PlanSourceIDs lists every asset a plan plays, its nested cuts' included
// (the callers fill Sources for them).
func PlanSourceIDs(p *RenderPlan) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(p *RenderPlan)
	walk = func(p *RenderPlan) {
		if p == nil {
			return
		}
		add := func(id string) {
			if id != "" && !IsNested(id) && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
		for _, c := range p.Clips {
			if !IsConversation(c.AssetID) {
				add(c.AssetID)
			}
			if c.Audio != nil && c.Audio.Variant != nil {
				add(c.Audio.Variant.AssetID) // its clearer copy (D88)
			}
		}
		conversationTileAssets(p, add) // the recordings a conversation's shots show (D99)
		for _, a := range p.Audio {
			add(a.AssetID)
			if a.Variant != nil {
				add(a.Variant.AssetID)
			}
		}
		keys := make([]string, 0, len(p.Nested))
		for k := range p.Nested {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walk(p.Nested[k].Plan)
		}
	}
	walk(p)
	return out
}

// PlanMotionHashes lists the motion graphics a plan draws, nested cuts'
// included, and the shared libraries they are bundled with (D171).
func PlanMotionHashes(p *RenderPlan) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(p *RenderPlan)
	walk = func(p *RenderPlan) {
		if p == nil {
			return
		}
		for _, o := range p.Overlays {
			if o.Motion != nil && !seen[o.Motion.Hash] {
				seen[o.Motion.Hash] = true
				out = append(out, o.Motion.Hash)
			}
		}
		for _, o := range p.Overlays {
			if o.Motion == nil {
				continue
			}
			names := make([]string, 0, len(o.Motion.Libs))
			for n := range o.Motion.Libs {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				if h := o.Motion.Libs[n]; h != "" && !seen[h] {
					seen[h] = true
					out = append(out, h)
				}
			}
		}
		for _, k := range sortedNestedKeys(p) {
			walk(p.Nested[k].Plan)
		}
	}
	walk(p)
	return out
}

// PlanResourceFiles lists the resource files a plan draws (SHA-256), nested
// cuts' included: the keys of ResourceSources.
func PlanResourceFiles(p *RenderPlan) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(p *RenderPlan)
	walk = func(p *RenderPlan) {
		if p == nil {
			return
		}
		files := make([]string, 0, len(p.ResourceSources))
		for sha := range p.ResourceSources {
			files = append(files, sha)
		}
		sort.Strings(files)
		for _, sha := range files {
			if !seen[sha] {
				seen[sha] = true
				out = append(out, sha)
			}
		}
		for _, k := range sortedNestedKeys(p) {
			walk(p.Nested[k].Plan)
		}
	}
	walk(p)
	return out
}

func sortedNestedKeys(p *RenderPlan) []string {
	keys := make([]string, 0, len(p.Nested))
	for k := range p.Nested {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// FillNested copies the outer plan's media maps into its nested plans (the
// renderer and the preview look sources up in the plan they draw).
func FillNested(p *RenderPlan) {
	for _, k := range sortedNestedKeys(p) {
		in := p.Nested[k].Plan
		if in == nil {
			continue
		}
		in.Sources, in.SourceChannels, in.ResourceSources, in.MotionSources, in.MotionStills = p.Sources, p.SourceChannels, p.ResourceSources, p.MotionSources, p.MotionStills
		FillNested(in)
	}
}

var _ = json.Marshal
