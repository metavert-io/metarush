package qa

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// LoudnessTargets are the delivery conventions renders are compared against.
// They are widely used conventions (platforms normalize playback themselves),
// not guarantees: social/short-form and YouTube ≈ −14 LUFS integrated, generic
// web/streaming ≈ −16 LUFS, true peak at or below −1 dBTP everywhere.
var LoudnessTargets = map[string]models.QALoudnessTarget{
	"shorts":  {Platform: "shorts", IntegratedLUFS: -14, ToleranceLU: 2, MaxTruePeakDBTP: -1, Note: "short-form social convention (≈ −14 LUFS, ≤ −1 dBTP)"},
	"tiktok":  {Platform: "tiktok", IntegratedLUFS: -14, ToleranceLU: 2, MaxTruePeakDBTP: -1, Note: "short-form social convention (≈ −14 LUFS, ≤ −1 dBTP)"},
	"reels":   {Platform: "reels", IntegratedLUFS: -14, ToleranceLU: 2, MaxTruePeakDBTP: -1, Note: "short-form social convention (≈ −14 LUFS, ≤ −1 dBTP)"},
	"youtube": {Platform: "youtube", IntegratedLUFS: -14, ToleranceLU: 2, MaxTruePeakDBTP: -1, Note: "YouTube playback reference (≈ −14 LUFS, ≤ −1 dBTP)"},
	"generic": {Platform: "generic", IntegratedLUFS: -16, ToleranceLU: 2, MaxTruePeakDBTP: -1, Note: "general web/streaming convention (≈ −16 LUFS, ≤ −1 dBTP)"},
}

// LoudnessTarget returns the convention for a platform (generic when unknown).
func LoudnessTarget(platform string) models.QALoudnessTarget {
	if t, ok := LoudnessTargets[platform]; ok {
		return t
	}
	return LoudnessTargets["generic"]
}

func (r *run) platform() string {
	if r.in.Spec != nil && r.in.Spec.Output.Platform != "" {
		return r.in.Spec.Output.Platform
	}
	return "generic"
}

// loudnessTarget uses the cut's output platform, or — when that is generic — the
// first platform in the project brief that has a convention.
func (r *run) loudnessTarget() models.QALoudnessTarget {
	if p := r.platform(); p != "generic" {
		return LoudnessTarget(p)
	}
	for _, p := range r.in.Platforms {
		if t, ok := LoudnessTargets[p]; ok {
			t.Note += "; from the project brief (output.platform is generic)"
			return t
		}
	}
	return LoudnessTarget("generic")
}

// audio checks loudness, clipping, overall silence and silence where speech is expected.
func (r *run) audio() {
	m := &r.rep.Measurements
	m.LoudnessTarget = r.loudnessTarget()
	tgt := m.LoudnessTarget
	if !r.mi.HasAudio {
		r.skip("loudness", models.QAObserved, "the render has no sound")
		r.skip("dialogue_silence", models.QAObserved, "the render has no sound")
		return
	}
	for _, s := range r.det.Silences {
		m.Silences = append(m.Silences, models.QASpan{From: round3(s.From), To: round3(s.To)})
	}
	ld := r.det.Loudness
	if ld.Measured {
		m.IntegratedLUFS, m.TruePeakDBTP, m.LoudnessRangeLU = finite(ld.IntegratedLUFS), finite(ld.TruePeakDBFS), finite(ld.RangeLU)
	}
	silent := r.silentFraction() >= 0.95 || (ld.Measured && ld.IntegratedLUFS <= -69)
	switch {
	case !m.ExpectsAudio:
		r.skip("loudness", models.QAObserved, "nothing in the cut should be heard")
	case silent:
		r.add(&models.QAFinding{Severity: models.QAError, Kind: "audio_silent", To: r.total(), Message: "The render is silent, although the cut has clips or tracks that should be heard.",
			Fix: &models.QAFix{Summary: "Render it again. If it stays silent, check on the Sound tab that the clips and tracks aren't muted or turned all the way down, and that the footage itself has sound."}})
		r.check("loudness", models.QAObserved, "", "audio_silent")
	case !ld.Measured:
		r.skip("loudness", models.QAObserved, "how loud the render is couldn't be measured")
	default:
		r.loudness(tgt, ld.IntegratedLUFS, ld.TruePeakDBFS)
		r.check("loudness", models.QAObserved, fmt.Sprintf("EBU R128 integrated %.0f LUFS ±%.0f LU, true peak ≤ %.0f dBTP (%s); > 0 dBTP = clipping error", tgt.IntegratedLUFS, tgt.ToleranceLU, tgt.MaxTruePeakDBTP, tgt.Note),
			"audio_silent", "audio_clipping", "true_peak_high", "loudness_low", "loudness_high")
	}
	switch {
	case !m.ExpectsAudio:
		r.skip("dialogue_silence", models.QAObserved, "nothing in the cut should be heard")
	case silent:
		r.skip("dialogue_silence", models.QAObserved, "the whole render is silent (that problem is listed on its own)")
	default:
		r.dialogueSilence()
	}
}

func (r *run) silentFraction() float64 {
	total := r.rep.Measurements.Duration
	if total <= 0 {
		return 0
	}
	s := 0.0
	for _, x := range r.det.Silences {
		s += x.To - x.From
	}
	return s / total
}

// gainOps scales every audible element by db (clip volume and audio-track gain),
// within the validator's limits. It returns ops plus how much of db is achievable.
func (r *run) gainOps(db float64) ([]editspec.Op, []string) {
	if r.in.Spec == nil {
		return nil, nil
	}
	var ops []editspec.Op
	var notes []string
	factor := math.Pow(10, db/20)
	for _, c := range r.in.Spec.Clips {
		pc := r.planClip(c.ID)
		if c.Muted || (pc != nil && !pc.HasAudio) || c.Locked {
			if c.Locked && !c.Muted {
				notes = append(notes, clipName(c.ID)+" is locked, so the change leaves it as it is")
			}
			continue
		}
		vol := 1.0
		if c.Volume != nil {
			vol = *c.Volume
		}
		nv := math.Round(math.Min(2, math.Max(0, vol*factor))*100) / 100
		if nv == vol {
			if vol >= 2 && db > 0 {
				notes = append(notes, clipName(c.ID)+" is already as loud as a clip can be turned up")
			}
			continue
		}
		if vol*factor > 2 {
			notes = append(notes, clipName(c.ID)+" can only go up to twice its own volume")
		}
		ops = append(ops, setOp("updateClip", c.ID, map[string]any{"volume": nv}))
	}
	for _, a := range r.in.Spec.Audio {
		if a.Locked {
			notes = append(notes, "the “"+a.ID+"” track is locked, so the change leaves it as it is")
			continue
		}
		g := 0.0
		if a.GainDB != nil {
			g = *a.GainDB
		}
		ng := math.Round(math.Min(12, math.Max(-60, g+db))*10) / 10
		if ng != g {
			ops = append(ops, setOp("updateAudio", a.ID, map[string]any{"gainDb": ng}))
		}
	}
	if len(ops) > 12 {
		ops = ops[:12]
		notes = append(notes, "the suggested change covers only the first 12 clips and tracks")
	}
	return ops, notes
}

func (r *run) planClip(id string) *editspec.PlanClip {
	for i := range r.plan.Clips {
		if r.plan.Clips[i].ID == id {
			return &r.plan.Clips[i]
		}
	}
	return nil
}

func gainFix(summary string, ops []editspec.Op, notes []string) *models.QAFix {
	if len(notes) > 0 {
		summary += " " + upperFirst(strings.Join(notes, "; ")) + "."
	}
	return &models.QAFix{Summary: summary, Ops: ops}
}

// normalizing reports whether the mixer sets the master gain itself (cuts with
// narration or a mix section): changing each clip's or track's gain would be
// undone by it, so fixes move the mix's own loudness settings instead.
func (r *run) normalizing() bool { return r.plan.Mix != nil && r.plan.Mix.Normalize }

// mixFix moves the mix's loudness target (target) or true-peak ceiling (ceiling)
// by the given dB, as a setMix op on the rendered spec's mix.
func (r *run) mixFix(summary string, target, ceiling float64) *models.QAFix {
	fix := &models.QAFix{Summary: summary}
	if r.in.Spec == nil || (r.in.Spec.Mix != nil && r.in.Spec.Mix.Locked) {
		return fix
	}
	m := editspec.Mix{}
	if r.in.Spec.Mix != nil {
		m = *r.in.Spec.Mix
	}
	l := editspec.MasterLoudness{}
	if m.Loudness != nil {
		l = *m.Loudness
	}
	if target != 0 {
		t := math.Max(-31, math.Min(-8, math.Round((r.plan.Mix.TargetLUFS+target)*10)/10))
		l.TargetLUFS = &t
	}
	if ceiling != 0 {
		c := math.Max(-9, math.Min(0, math.Round((r.plan.Mix.TruePeakDBTP+ceiling)*10)/10))
		l.TruePeakDBTP = &c
	}
	m.Loudness = &l
	fix.Ops = []editspec.Op{{Op: "setMix", Mix: &m}}
	return fix
}

func (r *run) hasNarration() bool { return r.plan.Voiceover != nil && len(r.plan.Voiceover.Beats) > 0 }

// uncleanedRecordings counts recorded or imported narration lines that play
// their original (not cleaned-up) audio.
func (r *run) uncleanedRecordings() int {
	n := 0
	if r.in.Spec == nil || r.in.Spec.Voiceover == nil {
		return 0
	}
	for _, b := range r.in.Spec.Voiceover.Beats {
		if t := b.Take; t != nil && (t.Source == models.TakeRecorded || t.Source == models.TakeImported) && !(t.UseProcessed && t.Processed != nil) {
			n++
		}
	}
	return n
}

// voiceCoverage is the share of the video where narration plays.
func (r *run) voiceCoverage() float64 {
	if r.plan.Mix == nil || r.plan.DurationInFrames == 0 {
		return 0
	}
	n := 0
	for _, v := range r.plan.Mix.VoiceRanges {
		n += v.To - v.From
	}
	return float64(n) / float64(r.plan.DurationInFrames)
}

// musicOutsideNarration estimates how loud the music plays where no narration
// is (LUFS as rendered: measured source loudness + its gain envelope, which
// includes the master gain).
func (r *run) musicOutsideNarration() (float64, bool) {
	if r.plan.Mix == nil {
		return 0, false
	}
	d := r.plan.Mix.Ducking
	near := func(f int) bool {
		for _, v := range r.plan.Mix.VoiceRanges {
			if f >= v.From-d.AttackFrames && f < v.To+d.ReleaseFrames {
				return true
			}
		}
		return false
	}
	best, ok := -200.0, false
	for _, a := range r.plan.Audio {
		if a.Role != editspec.RoleMusic {
			continue
		}
		l, has := r.in.Loudness[a.AssetID]
		if !has {
			continue
		}
		for f := a.From; f < a.From+a.DurationInFrames; f += 5 {
			if near(f) {
				continue
			}
			if v := l.LUFS + gainDB(a.Gain, a.GainDB, f-a.From); v > best {
				best, ok = v, true
			}
		}
	}
	return best, ok
}

func (r *run) hasMusic() bool {
	for _, a := range r.plan.Audio {
		if a.Role == editspec.RoleMusic {
			return true
		}
	}
	return false
}

// busFix raises (or lowers) one mixer bus by db, as a setMix op on the rendered spec's mix.
func (r *run) busFix(summary, bus string, db float64) *models.QAFix {
	fix := &models.QAFix{Summary: summary}
	if r.in.Spec == nil || (r.in.Spec.Mix != nil && r.in.Spec.Mix.Locked) {
		return fix
	}
	m := editspec.Mix{}
	if r.in.Spec.Mix != nil {
		m = *r.in.Spec.Mix
	}
	slot := map[string]**editspec.Bus{editspec.BusSource: &m.Source, editspec.BusVoiceover: &m.Voiceover, editspec.BusMusic: &m.Music, editspec.BusSFX: &m.SFX}[bus]
	if slot == nil {
		return fix
	}
	b := editspec.Bus{}
	if *slot != nil {
		b = **slot
	}
	g := db
	if b.GainDB != nil {
		g += *b.GainDB
	}
	g = math.Max(-60, math.Min(12, g))
	b.GainDB = &g
	*slot = &b
	fix.Ops = []editspec.Op{{Op: "setMix", Mix: &m}}
	return fix
}

func (r *run) loudness(tgt models.QALoudnessTarget, integrated, tp float64) {
	total := r.total()
	tpOK := !math.IsInf(tp, 0) && !math.IsNaN(tp)
	if r.normalizing() {
		r.loudnessNormalized(tgt, integrated, tp, tpOK, total)
		return
	}
	switch {
	case tpOK && tp > 0:
		cut := tp - tgt.MaxTruePeakDBTP + 0.5
		ops, notes := r.gainOps(-cut)
		r.add(&models.QAFinding{Severity: models.QAError, Kind: "audio_clipping", To: total, Message: peaksOver,
			Fix: gainFix("Turn the whole mix down"+amount(cut)+" and render again.", ops, notes)})
	case tpOK && tp > tgt.MaxTruePeakDBTP:
		cut := tp - tgt.MaxTruePeakDBTP + 0.3
		ops, notes := r.gainOps(-cut)
		r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "true_peak_high", To: total, Message: peaksHigh(tgt.Platform),
			Fix: gainFix("Turn the whole mix down"+amount(cut)+" and render again.", ops, notes)})
	}
	delta := tgt.IntegratedLUFS - integrated
	if math.Abs(delta) <= tgt.ToleranceLU {
		return
	}
	if delta > 0 {
		headroom := delta
		if tpOK {
			headroom = math.Min(delta, tgt.MaxTruePeakDBTP-tp)
		}
		msg := tooQuiet(tgt.Platform)
		if headroom < 0.5 {
			r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "loudness_low", To: total, Message: msg,
				Fix: &models.QAFix{Summary: "The loudest moments are already at the ceiling, so turning it up would make them crackle, and MetaRush can't hold them down on their own. Even out the loudest sounds in the footage first, or keep it quieter: " + quietIsOK}})
			return
		}
		ops, notes := r.gainOps(headroom)
		extra := ""
		if headroom < delta {
			extra = ": that's as far as it goes before the loudest moments would crackle"
		}
		r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "loudness_low", To: total, Message: msg,
			Fix: gainFix("Turn the whole mix up"+amount(headroom)+" and render again"+extra+".", ops, notes)})
		return
	}
	ops, notes := r.gainOps(delta)
	r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "loudness_high", To: total, Message: tooLoud(tgt.Platform),
		Fix: gainFix("Turn the whole mix down"+amount(delta)+" and render again.", ops, notes)})
}

// What people hear when the loudness is off (the measured numbers are in the
// report's measurements).
const (
	peaksOver = "The loudest moments go past the ceiling, so the sound crackles there."
	// quietIsOK ends a fix that can't make the video louder.
	quietIsOK = "sites turn quiet videos up only partly."
)

func peaksHigh(platform string) string {
	return "The loudest moments hit the ceiling, so they may crackle once " + reencodedBy(platform) + " the video."
}

func tooQuiet(platform string) string {
	return "The video is quieter than usual for " + platformName(platform) + ", so it may sound weak next to other videos."
}

func tooLoud(platform string) string {
	return "The video is louder than usual for " + platformName(platform) + ", so sites will turn it down when they play it."
}

// reencodedBy names who re-encodes a video for the platform ("TikTok re-encodes").
func reencodedBy(platform string) string {
	switch platform {
	case "shorts", "youtube":
		return "YouTube re-encodes"
	case "tiktok":
		return "TikTok re-encodes"
	case "reels":
		return "Instagram re-encodes"
	}
	return "YouTube or TikTok re-encode"
}

// loudnessNormalized reports loudness for cuts whose mixer sets the master gain:
// the plan estimated the mix from measured source loudness; this is the render.
func (r *run) loudnessNormalized(tgt models.QALoudnessTarget, integrated, tp float64, tpOK bool, total float64) {
	limited := r.plan.Mix.PeakLimited
	if tpOK && tp > tgt.MaxTruePeakDBTP {
		cut := tp - tgt.MaxTruePeakDBTP + 0.3
		sev, kind, what := models.QAWarning, "true_peak_high", peaksHigh(tgt.Platform)
		if tp > 0 {
			sev, kind, what = models.QAError, "audio_clipping", peaksOver
		}
		var fix *models.QAFix
		if limited {
			fix = r.mixFix("The mix is already as loud as its loudest moments allow, and they still came out"+amount(cut)+" too high: lower the ceiling for the loudest moments and render again.", 0, -cut)
		} else {
			fix = r.mixFix("Turn the overall loudness down"+amount(cut)+" and render again.", -cut, 0)
		}
		r.add(&models.QAFinding{Severity: sev, Kind: kind, To: total, Message: what, Fix: fix})
	}
	delta := tgt.IntegratedLUFS - integrated
	if math.Abs(delta) <= tgt.ToleranceLU {
		return
	}
	if delta > 0 {
		msg := tooQuiet(tgt.Platform)
		if limited || (tpOK && tgt.MaxTruePeakDBTP-tp < 0.5) {
			hint := "The mix is already as loud as its loudest moments allow without crackling. "
			var fix *models.QAFix
			switch {
			case r.hasNarration() && r.uncleanedRecordings() > 0:
				hint += "Clean up the narration on the Voiceover tab (that evens out its level, so its loudest moments come down), then render again; or keep it quieter: " + quietIsOK
			case r.hasNarration() && r.voiceCoverage() < 0.6 && r.hasMusic():
				// Bring the music where nobody talks to about 4 LU under the target
				// (it still drops under the narration).
				up := math.Min(6, math.Ceil(delta))
				if lvl, ok := r.musicOutsideNarration(); ok {
					up = math.Max(2, math.Min(12, math.Round(tgt.IntegratedLUFS-4-lvl)))
				}
				fix = r.busFix(fmt.Sprintf("The narration sets how loud the mix can go, and the parts without it are much quieter: turn the music up about %.0f dB on the Sound tab (it still dips under the narration), then render again.", up), editspec.BusMusic, up)
			default:
				hint += "Even out the loudest sounds, or keep it quieter: " + quietIsOK
			}
			if fix == nil {
				fix = &models.QAFix{Summary: hint}
			}
			r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "loudness_low", To: total, Message: msg, Fix: fix})
			return
		}
		if r.plan.Mix.MasterGainDB >= 11.99 {
			r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "loudness_low", To: total, Message: msg,
				Fix: &models.QAFix{Summary: "The mix is already turned up as far as MetaRush goes and is still quiet: on the Sound tab, check that the narration, the music and the clips' own sound aren't turned far down or muted."}})
			return
		}
		r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "loudness_low", To: total, Message: msg,
			Fix: r.mixFix("MetaRush's guess of how loud the mix would be came out low: turn the overall loudness up"+amount(delta)+" and render again.", delta, 0)})
		return
	}
	r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "loudness_high", To: total, Message: tooLoud(tgt.Platform),
		Fix: r.mixFix("Turn the overall loudness down"+amount(delta)+" and render again.", delta, 0)})
}

// speechSpans returns where the plan expects audible speech (timeline seconds).
func (r *run) speechSpans() ([]Span, string) {
	if len(r.in.Speech) > 0 {
		prec := r.in.SpeechPrec
		if prec == "" {
			prec = "word"
		}
		return mergeSpans(r.in.Speech, 0.25), prec
	}
	var out []Span
	for _, d := range r.plan.DialogueRanges {
		out = append(out, Span{From: float64(d.From) / r.fps, To: float64(d.To) / r.fps})
	}
	return out, "segment"
}

func mergeSpans(in []Span, gap float64) []Span {
	s := append([]Span(nil), in...)
	sort.Slice(s, func(i, j int) bool { return s[i].From < s[j].From })
	var out []Span
	for _, x := range s {
		if x.To <= x.From {
			continue
		}
		if n := len(out); n > 0 && x.From <= out[n-1].To+gap {
			out[n-1].To = math.Max(out[n-1].To, x.To)
			continue
		}
		out = append(out, x)
	}
	return out
}

// dialogueSilence flags silence where the transcripts say someone is speaking.
func (r *run) dialogueSilence() {
	speech, prec := r.speechSpans()
	if len(speech) == 0 {
		r.skip("dialogue_silence", models.QAObserved, "the clips in this cut have no transcripts yet, so there was nothing to compare (analyzing the footage makes them)")
		return
	}
	minGap := DialogueGapWord
	if prec != "word" {
		minGap = DialogueGapSeg
	}
	for _, sp := range speech {
		var longest, silent float64
		var lf, lt float64
		for _, s := range r.det.Silences {
			a, b := math.Max(s.From, sp.From), math.Min(s.To, sp.To)
			if b <= a {
				continue
			}
			silent += b - a
			if b-a > longest {
				longest, lf, lt = b-a, a, b
			}
		}
		length := sp.To - sp.From
		frac := silent / math.Max(length, 1e-6)
		if longest < minGap && !(frac >= 0.8 && length >= SilenceMinSec) {
			continue
		}
		sev := models.QAWarning
		if frac >= 0.8 || longest >= 1.0 && prec == "word" {
			sev = models.QAError
		}
		if frac >= 0.8 {
			lf, lt = sp.From, sp.To
		}
		f0, f1 := int(math.Round(lf*r.fps)), int(math.Round(lt*r.fps))
		cs := r.clipsAt(f0, f1)
		what := "the render"
		if len(cs) > 0 {
			what = clipNames(ids(cs))
		}
		msg := fmt.Sprintf("Around %s %s is silent for %s where someone should be speaking.", clock(lf), what, seconds(lt-lf))
		r.add(&models.QAFinding{Severity: sev, Kind: "silence_in_dialogue", From: lf, To: lt, ClipIDs: ids(cs), Message: msg, Fix: r.silenceFix(cs, f0, f1)})
	}
	r.check("dialogue_silence", models.QAObserved, fmt.Sprintf("silencedetect %ddB ≥%.2fs; flag silence ≥%.2fs inside word-timed speech (≥%.1fs for segment timing) or ≥80%% of a speech span", SilenceNoiseDB, SilenceMinSec, DialogueGapWord, DialogueGapSeg), "silence_in_dialogue")
}

func (r *run) silenceFix(cs []editspec.PlanClip, f0, f1 int) *models.QAFix {
	if len(cs) == 0 {
		return &models.QAFix{Summary: "Render it again and check once more."}
	}
	c := cs[len(cs)-1]
	if sc := r.specClip(c.ID); sc != nil && sc.Volume != nil && *sc.Volume < 0.05 && !sc.Locked {
		return &models.QAFix{Summary: upperFirst(clipName(c.ID)) + " is turned almost all the way down, so nobody hears what's said in it. Turn it up (or mute it, if the silence is on purpose).",
			Ops: []editspec.Op{setOp("updateClip", c.ID, map[string]any{"volume": 1})}}
	}
	return &models.QAFix{Summary: fmt.Sprintf("The transcript of %s has speech there (around %s into that footage), but the render is silent. Listen to the footage at that spot: if it has sound, render again; if it's silent, the transcript is wrong or %s should start or end somewhere else.",
		r.footage(c.AssetID), clock(r.srcTime(c, f0)), clipName(c.ID))}
}

// SpeechFromCaptions converts resolved caption lines into timeline speech spans:
// word spans when word timing exists, otherwise line spans.
func SpeechFromCaptions(c *editspec.PlanCaptions, fps int) ([]Span, string) {
	if c == nil || fps <= 0 || len(c.Lines) == 0 {
		return nil, ""
	}
	f := float64(fps)
	var out []Span
	words := false
	for _, l := range c.Lines {
		if len(l.Words) > 0 {
			words = true
			for _, w := range l.Words {
				out = append(out, Span{From: float64(w.From) / f, To: float64(w.To) / f})
			}
			continue
		}
		out = append(out, Span{From: float64(l.From) / f, To: float64(l.To) / f})
	}
	prec := "segment"
	if words && c.Precision == "word" {
		prec = "word"
	}
	return out, prec
}
