package editspec

import (
	"math"
	"sort"
)

// GainPoint is one keyframe of a gain envelope: F is the element-relative frame,
// G the linear gain. Remotion interpolates linearly between keyframes and holds
// the last value, so the preview and the render apply the same gain at every frame.
type GainPoint struct {
	F int     `json:"f"`
	G float64 `json:"g"`
}

// LoudnessFacts is optionally implemented by Facts: an asset's measured
// integrated loudness (LUFS) and true peak (dBFS) from technical evidence.
type LoudnessFacts interface {
	Loudness(assetID string) (lufs, truePeak float64, ok bool)
}

// ChannelFacts is optionally implemented by Facts: an asset's audio channel
// count (0 when unknown).
type ChannelFacts interface {
	AudioChannels(assetID string) int
}

// RangeLoudnessFacts is optionally implemented by Facts: the integrated loudness
// (BS.1770 gating) of a source time range, from the momentary loudness measured
// with the asset's evidence. Speech leveling uses it to know how loud the part
// of a recording a clip uses really is (a long recording's overall loudness says
// little about one answer in it).
type RangeLoudnessFacts interface {
	RangeLoudness(assetID string, from, to float64) (lufs float64, ok bool)
}

// MonoUpmixDB is where a mono source's peaks land in the stereo render: the
// renderer upmixes mono with equal power (−3 dB per channel), so loudness is
// unchanged but peaks sit 3 dB below what the file itself measures (verified
// with ebur128 on rendered narration).
const MonoUpmixDB = -3.0103

// CodecPeakMarginDB is kept below the true-peak ceiling when choosing the master
// gain: AAC encoding of the render overshoots the mixed peaks slightly (measured
// +0.1 to +0.2 dB on rendered narration), so the delivered file stays under it.
const CodecPeakMarginDB = 0.5

// PeakInMix is a source's true peak as it plays in the stereo mix.
func PeakInMix(f any, assetID string, truePeak float64) float64 {
	if cf, ok := f.(ChannelFacts); ok && cf.AudioChannels(assetID) == 1 {
		return truePeak + MonoUpmixDB
	}
	return truePeak
}

// mixParams are the effective mixer settings for a spec.
type mixParams struct {
	legacy        bool // no voiceover and no mix: reproduce the original audio behavior exactly
	auto          bool // automatic ducking
	busGain       map[string]float64
	busMuted      map[string]bool
	musicUnderVO  float64
	sourceUnderVO float64
	musicUnderDlg float64
	attack        int // frames
	release       int // frames
	target        float64
	truePeak      float64
	normalize     bool
}

func f64(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}

func mixFor(s *Spec) mixParams {
	fps := float64(s.Output.FPS)
	// A cut without narration or mix settings keeps the original mixer behavior
	// (no automatic ducking or normalization); the sound-at-cuts settings alone
	// (speech leveling, cut blends) do not change that.
	mp := mixParams{legacy: s.Voiceover == nil && (s.Mix == nil || s.Mix.onlyCutSettings()), busGain: map[string]float64{}, busMuted: map[string]bool{},
		musicUnderVO: DefaultMusicUnderVO, sourceUnderVO: DefaultSourceUnderVO, musicUnderDlg: DefaultMusicUnderDlg,
		attack: int(math.Round(DefaultDuckAttack * fps)), release: int(math.Round(DefaultDuckRelease * fps)), truePeak: DefaultTruePeak}
	mp.target = PlatformLoudness["generic"]
	if v, ok := PlatformLoudness[s.Output.Platform]; ok {
		mp.target = v
	}
	if mp.legacy {
		r := max(2, int(math.Round(fps*0.2)))
		mp.attack, mp.release = r, r
		return mp
	}
	mp.auto = true
	mp.normalize = s.Voiceover != nil && len(s.Voiceover.Beats) > 0
	m := s.Mix
	if m == nil {
		return mp
	}
	anySolo := false
	buses := map[string]*Bus{BusSource: m.Source, BusVoiceover: m.Voiceover, BusMusic: m.Music, BusSFX: m.SFX}
	for _, b := range buses {
		if b != nil && b.Solo {
			anySolo = true
		}
	}
	for name, b := range buses {
		if b == nil {
			mp.busMuted[name] = anySolo
			continue
		}
		mp.busGain[name] = f64(b.GainDB, 0)
		mp.busMuted[name] = b.Mute || (anySolo && !b.Solo)
	}
	if m.MuteSource {
		mp.busMuted[BusSource] = true
	}
	if d := m.Ducking; d != nil {
		if d.Enabled != nil {
			mp.auto = *d.Enabled
		}
		mp.musicUnderVO = -math.Abs(f64(d.MusicUnderVoiceoverDB, DefaultMusicUnderVO))
		mp.sourceUnderVO = -math.Abs(f64(d.SourceUnderVoiceoverDB, DefaultSourceUnderVO))
		mp.musicUnderDlg = -math.Abs(f64(d.MusicUnderDialogueDB, DefaultMusicUnderDlg))
		mp.attack = max(1, int(math.Round(f64(d.Attack, DefaultDuckAttack)*fps)))
		mp.release = max(1, int(math.Round(f64(d.Release, DefaultDuckRelease)*fps)))
	}
	if l := m.Loudness; l != nil {
		if l.TargetLUFS != nil {
			mp.target = *l.TargetLUFS
		}
		mp.truePeak = f64(l.TruePeakDBTP, DefaultTruePeak)
		if l.Normalize != nil {
			mp.normalize = *l.Normalize
		} else {
			mp.normalize = true
		}
	}
	return mp
}

// busFor maps an audio role to its mixer bus.
func busFor(role string) string {
	switch role {
	case RoleVoiceover:
		return BusVoiceover
	case RoleMusic:
		return BusMusic
	case RoleSFX:
		return BusSFX
	}
	return BusSource // dialogue tracks ride with the clips' own audio
}

func dbToLin(db float64) float64 { return math.Pow(10, db/20) }

func smoothstep(t float64) float64 {
	x := math.Max(0, math.Min(1, t))
	return x * x * (3 - 2*x)
}

// duckDepth is 0..1 at timeline frame t: 1 inside a range, ramping smoothly over
// attack frames before it (speech onsets are never masked) and release frames after.
func duckDepth(t int, ranges []FrameRange, attack, release int) float64 {
	depth := 0.0
	for _, r := range ranges {
		if r.To <= r.From {
			continue
		}
		d := 0.0
		switch {
		case t >= r.From && t < r.To:
			d = 1
		case t < r.From:
			d = smoothstep(1 - float64(r.From-t)/float64(max(1, attack)))
		default:
			d = smoothstep(1 - float64(t-r.To)/float64(max(1, release)))
		}
		if d > depth {
			depth = d
			if depth >= 1 {
				break
			}
		}
	}
	return depth
}

// legacyClipGain is the original clip audio gain: volume × transition ramps
// (equal-power for crossfades and the other overlapping transitions) × fade-out
// × the overlapping transition into the next clip. A clip whose sound plays
// exactly under its picture keeps this gain (older cuts are unchanged).
func legacyClipGain(c, next *PlanClip, f int) float64 {
	g := math.Max(0, c.Volume)
	t := c.TransitionIn.Type
	fadeIn := 0
	switch {
	case t == TransitionCut || t == "":
	case IsDipTransition(t):
		d := max(0, c.TransitionIn.DurationInFrames)
		fadeIn = d - d/2
	default:
		fadeIn = max(0, c.TransitionIn.DurationInFrames)
	}
	if fadeIn > 0 {
		p := math.Min(1, math.Max(0, float64(f)/float64(fadeIn)))
		if IsOverlapTransition(t) {
			g *= math.Sin(p * math.Pi / 2)
		} else {
			g *= p
		}
	}
	if out := max(0, c.FadeOutFrames); out > 0 {
		g *= math.Min(1, math.Max(0, float64(c.DurationInFrames-f)/float64(out)))
	}
	if next != nil && IsOverlapTransition(next.TransitionIn.Type) {
		d := max(0, next.TransitionIn.DurationInFrames)
		start := next.From - c.From
		if d > 0 && f >= start {
			p := math.Min(1, float64(f-start)/float64(d))
			g *= math.Sin((1 - p) * math.Pi / 2)
		}
	}
	return g
}

const envTolerance = 0.001

// envelope samples g for every frame of an element and compresses it into
// keyframes (piecewise linear within envTolerance).
func envelope(n int, g func(f int) float64) []GainPoint {
	if n <= 0 {
		return nil
	}
	vals := make([]float64, n)
	for f := range vals {
		v := g(f)
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			v = 0
		}
		vals[f] = math.Min(v, 8)
	}
	pts := []GainPoint{{F: 0, G: round5(vals[0])}}
	a := 0
	lo, hi := math.Inf(-1), math.Inf(1)
	for i := 1; i < n; i++ {
		dx := float64(i - a)
		sl, sh := (vals[i]-envTolerance-vals[a])/dx, (vals[i]+envTolerance-vals[a])/dx
		nlo, nhi := math.Max(lo, sl), math.Min(hi, sh)
		if nlo > nhi {
			pts = append(pts, GainPoint{F: i - 1, G: round5(vals[i-1])})
			a = i - 1
			lo, hi = vals[i]-envTolerance-vals[a], vals[i]+envTolerance-vals[a]
			continue
		}
		lo, hi = nlo, nhi
	}
	if last := pts[len(pts)-1]; last.F != n-1 {
		pts = append(pts, GainPoint{F: n - 1, G: round5(vals[n-1])})
	}
	return pts
}

func round5(v float64) float64 { return math.Round(v*1e5) / 1e5 }

// GainAt evaluates an envelope at element frame f (linear interpolation, the
// last value held) — the same rule the Remotion audio layer applies.
func GainAt(env []GainPoint, f float64) float64 {
	if len(env) == 0 {
		return 1
	}
	if f <= float64(env[0].F) {
		return env[0].G
	}
	for i := 1; i < len(env); i++ {
		if f <= float64(env[i].F) {
			a, b := env[i-1], env[i]
			if b.F == a.F {
				return b.G
			}
			return a.G + (b.G-a.G)*(f-float64(a.F))/float64(b.F-a.F)
		}
	}
	return env[len(env)-1].G
}

// audioElement is one sound source on the timeline during mix resolution.
type audioElement struct {
	assetID string
	from    int // timeline frame
	dur     int
	rate    float64
	gain    func(f int) float64 // linear, before the master gain
	peakDB  float64             // max gain in dB (for the true-peak guard)
	clip    *PlanClip
	audio   *PlanAudio
	window  *soundWindow // clips: where the sound plays
	lufs    *float64     // the measured loudness of what this element plays, when known (speech leveling)
	variant *EnhancedSound
}

// transcriptOf is a recording's transcript from the facts (nil without facts).
func transcriptOf(facts Facts, assetID string) []TranscriptSegment {
	if facts == nil {
		return nil
	}
	return facts.Transcript(assetID)
}

// resolveMix turns clips, audio tracks and narration into a mixed plan: it sets
// each clip's effective mute and gain envelope, emits the audio elements (with
// envelopes) and the mix summary.
func resolveMix(s *Spec, plan *RenderPlan, beats []ResolvedBeat, facts Facts, sp *soundPlan) {
	fps := float64(s.Output.FPS)
	total := plan.DurationInFrames
	mp := mixFor(s)
	if mp.busMuted[BusSource] {
		for i := range plan.Clips {
			plan.Clips[i].Muted = true
		}
	}
	plan.DialogueRanges = dialogueRangesSP(s, plan.Clips, facts, sp)
	vr := []FrameRange{}
	if s.Voiceover != nil {
		vr = voiceRanges(s, beats, total)
	} else {
		vr = voiceRanges(&Spec{Output: s.Output, Audio: s.Audio, Voiceover: &Voiceover{}}, nil, total)
	}
	// Speech leveling: each clip with speech gets a gain toward the cut's typical
	// speech level (before bus and master gains).
	levelDB, speechL, target := levelSpeech(s, plan, sp, facts, mp)
	var elems []*audioElement
	// The clips' own audio, over each clip's sound window.
	for i := range plan.Clips {
		pc := &plan.Clips[i]
		if i > 0 && !sp.cuts[i].follow {
			pc.TransitionIn.Audio = sp.cuts[i].plan(fps)
		}
		if pc.Muted || !pc.HasAudio {
			continue
		}
		var next *PlanClip
		if i+1 < len(plan.Clips) {
			next = &plan.Clips[i+1]
		}
		w := sp.win[i]
		bus := dbToLin(mp.busGain[BusSource] + levelDB[i])
		from, dur := w.from, w.to-w.from
		base := func(f int) float64 { return legacyClipGain(pc, next, f) }
		if !w.legacy {
			base = func(f int) float64 { return clipSoundGain(pc, next, w, from+f) }
		}
		g := func(f int) float64 {
			v := base(f) * bus
			if mp.auto && len(vr) > 0 {
				v *= dbToLin(mp.sourceUnderVO * duckDepth(from+f, vr, mp.attack, mp.release))
			}
			return v
		}
		e := &audioElement{assetID: pc.AssetID, from: from, dur: dur, rate: pc.PlaybackRate, gain: g,
			peakDB: 20*math.Log10(math.Max(1e-6, pc.Volume)) + mp.busGain[BusSource] + levelDB[i], clip: pc, window: &sp.win[i]}
		// Per-person levels over the stretches each person speaks, and the
		// clearer copy of the recording when one covers this clip's sound.
		if s.Mix != nil && !IsConversation(pc.AssetID) { // a conversation's people are leveled on their own tracks
			srcFrom, srcTo := srcAt(*pc, fps, float64(w.from)), srcAt(*pc, fps, float64(w.to))
			if vg := s.Mix.gainsFor(pc.AssetID, transcriptOf(facts, pc.AssetID), srcFrom-1, srcTo+1); !vg.flat {
				g0, pcv := g, *pc
				e.gain = func(f int) float64 { return g0(f) * vg.at(srcAt(pcv, fps, float64(from+f))) }
				e.peakDB += math.Max(0, vg.max)
			}
			e.variant = s.Mix.variantFor(pc.AssetID, math.Max(0, srcFrom), srcTo, facts)
		}
		if speechL[i] != nil {
			l := *speechL[i]
			e.lufs = &l
			pc.SpeechLUFS = speechL[i]
			lv := levelDB[i]
			pc.LevelDB = &lv
		}
		elems = append(elems, e)
	}
	// Audio tracks (music, sfx, dialogue, hand-placed voiceover).
	for _, a := range s.Audio {
		bus := busFor(a.Role)
		if mp.busMuted[bus] {
			continue
		}
		gainDB := f64(a.GainDB, 0)
		duck := -12.0
		if a.DuckDB != nil {
			duck = *a.DuckDB
		}
		base := dbToLin(gainDB + mp.busGain[bus])
		role := a.Role
		// duck: true = always gets quieter while someone talks, false = never
		// (absent: as before — the mix's automatic ducking or duckUnderDialogue).
		duckOn, duckOff := a.Duck != nil && *a.Duck, a.Duck != nil && !*a.Duck
		explicitDuck := (a.DuckUnderDialogue || duckOn) && duck != 0
		dlgDB := mp.musicUnderDlg
		if a.DuckDB != nil {
			dlgDB = -math.Abs(*a.DuckDB)
		}
		var segs []TranscriptSegment
		if role == RoleDialogue && s.Mix != nil && (len(s.Mix.Voices) > 0 || len(s.Mix.Enhanced) > 0) {
			segs = transcriptOf(facts, a.AssetID)
		}
		for _, p := range trackPieces(a, fps, total, facts) {
			from, dur := p.from, p.dur
			pa := &PlanAudio{ID: p.id, AssetID: a.AssetID, Role: a.Role, Bus: bus, From: from, DurationInFrames: dur,
				TrimBefore: p.trim, GainDB: gainDB, FadeInFrames: p.fadeIn, FadeOutFrames: p.fadeOut,
				DuckUnderDialogue: a.DuckUnderDialogue || duckOn, DuckDB: duck, PlaybackRate: 1}
			if p.auto {
				pa.Fit, pa.Piece, pa.EndsOnBeat, pa.EndsOn = TrackFitAuto, p.piece, p.endsOnBeat, p.endsOn
			}
			pp := p
			g := func(f int) float64 {
				v := base * pp.fade(f)
				t := from + f
				switch {
				case duckOff:
				case mp.legacy:
					if explicitDuck {
						v *= dbToLin(-math.Abs(duck) * duckDepth(t, plan.DialogueRanges, mp.attack, mp.release))
					}
				case role == RoleMusic && mp.auto:
					db := math.Min(mp.musicUnderVO*duckDepth(t, vr, mp.attack, mp.release), dlgDB*duckDepth(t, plan.DialogueRanges, mp.attack, mp.release))
					v *= dbToLin(db)
				case role == RoleDialogue && mp.auto:
					v *= dbToLin(mp.sourceUnderVO * duckDepth(t, vr, mp.attack, mp.release))
				case explicitDuck && role != RoleVoiceover:
					v *= dbToLin(-math.Abs(duck) * duckDepth(t, plan.DialogueRanges, mp.attack, mp.release))
				}
				return v
			}
			e := &audioElement{assetID: a.AssetID, from: from, dur: dur, rate: 1, gain: g, peakDB: gainDB + mp.busGain[bus], audio: pa}
			if role == RoleDialogue && s.Mix != nil {
				// A dialogue track is someone's voice too: their level and clearer copy.
				srcFrom, srcTo := float64(p.trim)/fps, float64(p.trim+dur)/fps
				if vg := s.Mix.gainsFor(a.AssetID, segs, srcFrom-1, srcTo+1); !vg.flat {
					g0, trim := g, p.trim
					e.gain = func(f int) float64 { return g0(f) * vg.at(float64(trim+f)/fps) }
					e.peakDB += math.Max(0, vg.max)
				}
				e.variant = s.Mix.variantFor(a.AssetID, srcFrom, srcTo, facts)
			}
			elems = append(elems, e)
		}
	}
	// Narration.
	if s.Voiceover != nil && !mp.busMuted[BusVoiceover] {
		voBus := mp.busGain[BusVoiceover]
		if s.Continuous() {
			c := s.Voiceover.Continuous
			start, prob := resolveAnchor(c.Anchor, plan.Clips, fps, -1, s.beatGap())
			asset, offset := c.AssetID, 0.0
			if c.UseProcessed && c.Processed != nil && c.Processed.AssetID != "" {
				asset, offset = c.Processed.AssetID, c.Processed.Offset
			}
			dur := int(math.Round((c.Out - c.In) * fps))
			if prob == "" && start < total && dur > 0 {
				trim := 0
				if start < 0 { // starts before the cut: skip its head
					trim, dur, start = -start, dur+start, 0
				}
				if start+dur > total {
					dur = total - start
				}
				gdb := f64(c.GainDB, 0) + voBus
				pa := &PlanAudio{ID: "voiceover", AssetID: asset, Role: RoleVoiceover, Bus: BusVoiceover, From: start, DurationInFrames: dur,
					TrimBefore: max(0, int(math.Round((c.In+offset)*fps))+trim), GainDB: gdb, PlaybackRate: 1, AIVoice: c.AIVoice}
				base := dbToLin(gdb)
				d := dur
				elems = append(elems, &audioElement{assetID: asset, from: start, dur: dur, rate: 1, peakDB: gdb, audio: pa,
					gain: func(f int) float64 { return base * edgeFade(f, d, 2) }})
			}
		} else {
			for _, g := range narrationPieces(s, beats, total) {
				b, bt := g.first, s.Voiceover.Beats[g.first.Index]
				start, dur, trim := b.Start, g.speech, b.TrimBefore
				if start < 0 {
					trim += int(math.Round(float64(-start) * b.Rate))
					dur += start
					start = 0
				}
				if start+dur > total {
					dur = total - start
				}
				if dur <= 0 {
					continue
				}
				gdb := f64(bt.GainDB, 0) + voBus
				pa := &PlanAudio{ID: "vo-" + bt.ID, AssetID: b.PlayAsset, Role: RoleVoiceover, Bus: BusVoiceover, BeatID: bt.ID, From: start,
					DurationInFrames: dur, TrimBefore: trim, GainDB: gdb, PlaybackRate: b.Rate, AIVoice: bt.Take.AIVoice}
				if len(g.ids) > 1 {
					pa.BeatIDs = g.ids
				}
				base := dbToLin(gdb)
				d := dur
				elems = append(elems, &audioElement{assetID: b.PlayAsset, from: start, dur: dur, rate: b.Rate, peakDB: gdb, audio: pa,
					gain: func(f int) float64 { return base * edgeFade(f, d, 2) }})
			}
		}
	}
	// Words changed in the speaker's own voice play over the clips' own sound (D308).
	elems = sayInMix(s, elems, fps, facts)
	// Master loudness: one static gain so the estimated mix meets the target.
	pm := &PlanMix{Buses: map[string]PlanBus{}, TargetLUFS: mp.target, TruePeakDBTP: mp.truePeak, VoiceRanges: vr,
		Ducking: PlanDucking{Auto: mp.auto && !mp.legacy, MusicUnderVoiceoverDB: mp.musicUnderVO, SourceUnderVoiceoverDB: mp.sourceUnderVO,
			MusicUnderDialogueDB: mp.musicUnderDlg, AttackFrames: mp.attack, ReleaseFrames: mp.release}}
	for _, b := range Buses {
		pm.Buses[b] = PlanBus{GainDB: mp.busGain[b], Muted: mp.busMuted[b]}
	}
	master := 1.0
	if lf, ok := facts.(LoudnessFacts); ok && facts != nil {
		if est, peak, ok := estimateLoudness(elems, lf, total, fps); ok {
			e := round3(est)
			pm.EstimatedLUFS = &e
			if mp.normalize {
				gain := mp.target - est
				if ceiling := mp.truePeak - CodecPeakMarginDB; peak+gain > ceiling {
					gain = ceiling - peak
					pm.PeakLimited = true
				}
				gain = math.Max(-20, math.Min(12, gain))
				pm.MasterGainDB = round3(gain)
				master = dbToLin(pm.MasterGainDB)
			}
		}
	}
	pm.Normalize = mp.normalize
	pm.LevelSpeech = s.LevelsSpeech()
	pm.SpeechTargetLUFS = target
	pm.CutBlendFrames = sp.cutBlend
	for _, e := range elems {
		g := e.gain
		env := envelope(e.dur, func(f int) float64 { return g(f) * master })
		switch {
		case e.clip != nil && e.window != nil && (e.window.separate || e.variant != nil):
			// A clearer copy plays as the clip's own sound element, like a split
			// edit's: the picture plays muted, the timing is unchanged.
			pc := e.clip
			pc.Audio = &PlanClipAudio{From: e.from, DurationInFrames: e.dur,
				TrimBefore:    max(0, pc.TrimBefore+int(math.Round(float64(e.from-pc.From)*pc.PlaybackRate))),
				BlendInFrames: e.window.blendIn, BlendOutFrames: e.window.blendOut, Gain: env}
			if e.variant != nil {
				pc.Audio.Variant = planVariant(e.variant, pc.Audio.TrimBefore, fps)
			}
		case e.clip != nil:
			e.clip.Gain = env
		case e.audio != nil:
			e.audio.Gain = env
			if e.variant != nil {
				e.audio.Variant = planVariant(e.variant, e.audio.TrimBefore, fps)
			}
			plan.Audio = append(plan.Audio, *e.audio)
		}
	}
	plan.Mix = pm
}

// plan is the cut's sound change as the RenderPlan shows it.
func (c cutSound) plan(fps float64) *PlanCutAudio {
	pa := &PlanCutAudio{Mode: c.mode, Result: c.result, CutFrame: c.cut, EditFrame: c.edit, BlendFrames: c.blend, Limited: c.limited, NoPause: c.noPause}
	if c.vfade {
		pa.EditFrame = c.cut
	}
	if c.limited != "" {
		pa.RequestedFrames = c.requested
	}
	return pa
}

// levelSpeech computes each clip's leveling gain (dB) when the cut evens out
// speech loudness: clips with at least half a second of speech are measured over
// the part they play (or, without momentary measurements, the whole recording),
// the target is their speech-time-weighted median, and each gain is limited to
// ±MaxLevelDB and, when it turns a clip up, by its peaks.
func levelSpeech(s *Spec, plan *RenderPlan, sp *soundPlan, facts Facts, mp mixParams) ([]float64, []*float64, *float64) {
	n := len(plan.Clips)
	gains, levels := make([]float64, n), make([]*float64, n)
	if !s.LevelsSpeech() || facts == nil {
		return gains, levels, nil
	}
	lf, _ := facts.(LoudnessFacts)
	rf, _ := facts.(RangeLoudnessFacts)
	type cand struct {
		i      int
		lufs   float64
		weight float64
	}
	var cs []cand
	for i := range plan.Clips {
		pc := plan.Clips[i]
		if pc.Muted || !pc.HasAudio || (pc.Volume < 0.05) {
			continue
		}
		w := sp.win[i]
		speech := speechSeconds(pc, w, facts)
		if speech < 0.5 {
			continue // no speech: it keeps its recorded level
		}
		l, ok := 0.0, false
		if rf != nil {
			l, ok = rf.RangeLoudness(pc.AssetID, w.heardIn, w.heardOut)
		}
		if !ok && lf != nil {
			l, _, ok = lf.Loudness(pc.AssetID)
		}
		if !ok || math.IsNaN(l) || math.IsInf(l, 0) || l < -70 {
			continue
		}
		cs = append(cs, cand{i, round3(l), speech})
	}
	if len(cs) == 0 {
		return gains, levels, nil
	}
	sorted := append([]cand(nil), cs...)
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
	ceiling := mp.truePeak - CodecPeakMarginDB
	for _, c := range cs {
		g := math.Max(-MaxLevelDB, math.Min(MaxLevelDB, target-c.lufs))
		if g > 0 && lf != nil {
			if _, tp, ok := lf.Loudness(plan.Clips[c.i].AssetID); ok && !math.IsInf(tp, 0) && !math.IsNaN(tp) {
				peak := PeakInMix(facts, plan.Clips[c.i].AssetID, tp) + 20*math.Log10(math.Max(1e-6, plan.Clips[c.i].Volume)) + mp.busGain[BusSource]
				if peak+g > ceiling {
					g = math.Max(0, ceiling-peak)
				}
			}
		}
		g = math.Round(g*10) / 10
		gains[c.i] = g
		l := c.lufs
		levels[c.i] = &l
	}
	t := target
	return gains, levels, &t
}

// edgeFade is a short linear fade at both ends of a narration segment (clickless cuts).
func edgeFade(f, dur, frames int) float64 {
	if dur <= 4*frames || frames <= 0 {
		return 1
	}
	if f < frames {
		return float64(f+1) / float64(frames+1)
	}
	if dur-1-f < frames {
		return float64(dur-f) / float64(frames+1)
	}
	return 1
}

// estimateLoudness approximates the mix's integrated loudness (BS.1770 gating
// over 400 ms blocks) from each source's measured loudness and its gain over
// time, plus the highest expected true peak. Sources without loudness evidence
// are left out; ok is false when nothing audible has evidence.
func estimateLoudness(elems []*audioElement, lf LoudnessFacts, total int, fps float64) (lufs, peak float64, ok bool) {
	block := max(1, int(math.Round(0.4*fps)))
	nb := (total + block - 1) / block
	if nb == 0 {
		return 0, 0, false
	}
	energy := make([]float64, nb)
	peak = math.Inf(-1)
	used := false
	for _, e := range elems {
		l, tp, has := lf.Loudness(e.assetID)
		if has && e.lufs != nil {
			l = *e.lufs // what this clip plays, as measured for speech leveling
		}
		if !has || math.IsInf(l, 0) || math.IsNaN(l) || l < -70 {
			continue
		}
		tp = PeakInMix(lf, e.assetID, tp)
		used = true
		maxG := 0.0
		for b := 0; b < nb; b++ {
			lo, hi := b*block, min(total, (b+1)*block)
			s, n := 0.0, 0
			for t := max(lo, e.from); t < min(hi, e.from+e.dur); t++ {
				g := e.gain(t - e.from)
				s += g * g
				n++
				if g > maxG {
					maxG = g
				}
			}
			if n == 0 {
				continue
			}
			ms := s / float64(hi-lo) // mean power gain over the block
			energy[b] += math.Pow(10, l/10) * ms
		}
		if !math.IsInf(tp, 0) && !math.IsNaN(tp) && maxG > 0 {
			if p := tp + 20*math.Log10(maxG); p > peak {
				peak = p
			}
		}
	}
	if !used {
		return 0, 0, false
	}
	gate := func(min float64) (float64, int) {
		s, n := 0.0, 0
		for _, e := range energy {
			if e > 0 && 10*math.Log10(e) > min {
				s += e
				n++
			}
		}
		if n == 0 {
			return math.Inf(-1), 0
		}
		return 10 * math.Log10(s/float64(n)), n
	}
	abs, n := gate(-70)
	if n == 0 {
		return 0, 0, false
	}
	rel, n := gate(abs - 10)
	if n == 0 {
		rel = abs
	}
	if math.IsInf(peak, -1) {
		peak = -120
	}
	return rel, peak, true
}

// sortedRanges returns ranges sorted by start (helper for callers outside).
func sortedRanges(in []FrameRange) []FrameRange {
	out := append([]FrameRange(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out
}
