package editspec

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Voices: per-person sound settings (mix.voices) and the processed copies they
// play (mix.enhanced). One rule decides who is speaking at any moment of a
// recording — the speaker of the nearest transcript segment, a gap between two
// people split at its middle — and both the per-person level (the mixer's gain
// envelopes) and the clearer voice (sound enhance builds the copy) follow it,
// with short crossfades where one person's stretch meets another's.

// VoiceCrossfade is how long a person's settings take to blend into the next
// person's at the edge of their stretch (seconds, centered on the edge).
const VoiceCrossfade = 0.08

// SpeakerSpan is a stretch of a recording that belongs to one speaker (the
// name the speaker data shows; "" when nobody is named).
type SpeakerSpan struct {
	From, To float64
	Speaker  string
}

// SpeakerSpans splits [from, to) of a recording (source seconds) into the
// stretches each speaker owns: every moment belongs to the speaker of the
// nearest transcript segment (a gap between two people is split at its middle;
// the time before the first segment belongs to its speaker, the time after the
// last to its speaker). Consecutive stretches of the same speaker are merged.
// Without segments the whole range belongs to "".
func SpeakerSpans(segs []TranscriptSegment, from, to float64) []SpeakerSpan {
	if to <= from {
		return nil
	}
	type seg struct {
		from, to float64
		who      string
	}
	var ss []seg
	for _, s := range segs {
		if s.To <= s.From {
			continue
		}
		ss = append(ss, seg{s.From, s.To, strings.TrimSpace(s.Speaker)})
	}
	if len(ss) == 0 {
		return []SpeakerSpan{{From: from, To: to}}
	}
	sort.SliceStable(ss, func(i, j int) bool { return ss[i].from < ss[j].from })
	// Boundaries between consecutive segments: the middle of the gap (or of an overlap).
	var out []SpeakerSpan
	start := math.Inf(-1)
	for i, s := range ss {
		end := math.Inf(1)
		if i+1 < len(ss) {
			end = (s.to + ss[i+1].from) / 2
			if ss[i+1].from < s.to { // overlapping segments: split the overlap
				end = (ss[i+1].from + math.Min(s.to, ss[i+1].to)) / 2
			}
		}
		a, b := math.Max(start, from), math.Min(end, to)
		start = end
		if b <= a {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Speaker == s.who && math.Abs(out[n-1].To-a) < 1e-9 {
			out[n-1].To = b
			continue
		}
		out = append(out, SpeakerSpan{From: a, To: b, Speaker: s.who})
	}
	return out
}

// voiceKey compares names the way people write them.
func voiceKey(name string) string { return strings.ToLower(strings.Join(strings.Fields(name), " ")) }

// VoiceFor finds a person's settings (by name, ignoring case and spacing).
func (m *Mix) VoiceFor(name string) *VoiceSound {
	if m == nil || strings.TrimSpace(name) == "" {
		return nil
	}
	k := voiceKey(name)
	for i := range m.Voices {
		if m.Voices[i].Name != "" && voiceKey(m.Voices[i].Name) == k {
			return &m.Voices[i]
		}
	}
	return nil
}

// RecordingVoice finds the settings for a whole recording (assetId voices).
func (m *Mix) RecordingVoice(assetID string) *VoiceSound {
	if m == nil || assetID == "" {
		return nil
	}
	for i := range m.Voices {
		if m.Voices[i].Name == "" && m.Voices[i].AssetID == assetID {
			return &m.Voices[i]
		}
	}
	return nil
}

// voiceAt is the setting that applies to a speaker's stretch of a recording:
// the person's own, else the whole recording's (nil when neither is set).
func (m *Mix) voiceAt(assetID, speaker string) *VoiceSound {
	if v := m.VoiceFor(speaker); v != nil {
		return v
	}
	return m.RecordingVoice(assetID)
}

// DefaultStrength is how much of the processed voice is heard when a setting
// does not say: all of it. Blending the original back in returns its noise
// (at 80% the background comes back only 14 dB down) and the two versions
// smear each other; measured on noisy speech, the voice clarity score drops
// from 3.2 at full strength to 3.1 at 95% and 2.9 at 80% (D89). A lower
// strength stays available for people who want some of the room back.
func DefaultStrength(string) float64 { return 1 }

// StrengthOf is an enhancement's strength (its own, or the mode's default).
func (e *Enhance) StrengthOf() float64 {
	if e == nil {
		return 0
	}
	if e.Strength != nil {
		return math.Max(0, math.Min(1, *e.Strength))
	}
	return DefaultStrength(e.Mode)
}

// Active reports whether an enhancement changes what is heard.
func (e *Enhance) Active() bool { return e != nil && !e.Off && e.Mode != "" && e.StrengthOf() > 0 }

// voiceGains is the per-person level of one recording: the gain (linear) at a
// source time, with VoiceCrossfade ramps (linear in dB) between stretches.
type voiceGains struct {
	edges []voiceEdge // sorted by t
	first float64     // dB before the first edge
	max   float64     // the highest dB anywhere (peak guard)
	flat  bool        // no person's level is changed: gain 1 everywhere
}

type voiceEdge struct {
	t        float64 // the boundary (source seconds)
	from, to float64 // dB on either side
}

// gainsFor builds the per-person levels of a recording over [from, to).
func (m *Mix) gainsFor(assetID string, segs []TranscriptSegment, from, to float64) voiceGains {
	vg := voiceGains{flat: true}
	if m == nil || len(m.Voices) == 0 || to <= from {
		return vg
	}
	dbOf := func(who string) float64 {
		if v := m.voiceAt(assetID, who); v != nil && v.GainDB != nil {
			return *v.GainDB
		}
		return 0
	}
	spans := SpeakerSpans(segs, from, to)
	if len(spans) == 0 {
		return vg
	}
	vg.first = dbOf(spans[0].Speaker)
	vg.max = vg.first
	any := vg.first != 0
	for i := 1; i < len(spans); i++ {
		a, b := dbOf(spans[i-1].Speaker), dbOf(spans[i].Speaker)
		if b > vg.max {
			vg.max = b
		}
		if b != 0 {
			any = true
		}
		if a != b {
			vg.edges = append(vg.edges, voiceEdge{t: spans[i].From, from: a, to: b})
		}
	}
	vg.flat = !any
	return vg
}

// at is the linear gain at source time t.
func (vg voiceGains) at(t float64) float64 {
	if vg.flat {
		return 1
	}
	db := vg.first
	half := VoiceCrossfade / 2
	for _, e := range vg.edges {
		switch {
		case t >= e.t+half:
			db = e.to
			continue
		case t > e.t-half:
			p := (t - (e.t - half)) / VoiceCrossfade
			db = e.from + (e.to-e.from)*p
		}
		break
	}
	return dbToLin(db)
}

// variantFor finds the processed copy that covers [from, to) of a recording
// (source seconds), or nil. A copy is used only when it covers the whole range
// (its length comes from the facts).
func (m *Mix) variantFor(assetID string, from, to float64, facts Facts) *EnhancedSound {
	if m == nil || facts == nil || len(m.Enhanced) == 0 {
		return nil
	}
	const tol = 0.02
	for i := range m.Enhanced {
		e := &m.Enhanced[i]
		if e.AssetID != assetID || e.Variant == "" {
			continue
		}
		v, ok := facts.Asset(e.Variant)
		if !ok || v.Duration <= 0 || !v.HasAudio || (v.State != "" && v.State != "available") {
			continue
		}
		if e.Start <= from+tol && to <= e.Start+v.Duration+tol {
			return e
		}
	}
	return nil
}

// planVariant is the plan's reference to a copy for an element whose own
// source starts trimBefore output frames into the recording.
func planVariant(e *EnhancedSound, trimBefore int, fps float64) *PlanVariant {
	return &PlanVariant{AssetID: e.Variant, TrimBefore: max(0, trimBefore-int(math.Round(e.Start*fps)))}
}

// VoiceSettingsFor is the canonical description of the voice settings that
// shape a processed copy of a recording: for each speaker heard in it (sorted)
// and the recording itself, the enhancement mode and strength. A voice whose
// enhancement is off (the A/B "original plays") stays original in the copy,
// so it is left out like a voice without one; level changes do not change the
// copy. Two copies made with the same description of the same stretch are the
// same. "" when nothing in the recording is enhanced.
func (m *Mix) VoiceSettingsFor(assetID string, speakers []string) string {
	if m == nil {
		return ""
	}
	desc := func(e *Enhance) string {
		return e.Mode + " " + strconv.FormatFloat(math.Round(e.StrengthOf()*100), 'f', 0, 64) + "%"
	}
	var parts []string
	seen := map[string]bool{}
	names := append([]string(nil), speakers...)
	sort.Slice(names, func(i, j int) bool { return voiceKey(names[i]) < voiceKey(names[j]) })
	whole := m.RecordingVoice(assetID)
	for _, n := range names {
		k := voiceKey(n)
		if seen[k] {
			continue
		}
		seen[k] = true
		v := m.VoiceFor(n)
		if v == nil || v.Enhance == nil {
			continue // the whole recording's setting (below) covers them
		}
		if !v.Enhance.Active() {
			continue // this person stays original
		}
		parts = append(parts, fmt.Sprintf("%s: %s", strings.TrimSpace(n), desc(v.Enhance)))
	}
	if whole != nil && whole.Enhance.Active() {
		parts = append(parts, "everyone else: "+desc(whole.Enhance))
	}
	return strings.Join(parts, "; ")
}

// SpeakersIn lists the speakers heard in [from, to) of a recording, in the
// order they first speak.
func SpeakersIn(segs []TranscriptSegment, from, to float64) []string {
	var out []string
	seen := map[string]bool{}
	for _, sp := range SpeakerSpans(segs, from, to) {
		if !seen[sp.Speaker] {
			seen[sp.Speaker] = true
			out = append(out, sp.Speaker)
		}
	}
	return out
}

// normalizeVoices keeps voice settings in a canonical form (stable hashes,
// lossless cut sheets): strengths in whole percent, copy starts in whole
// seconds.
func normalizeVoices(m *Mix) {
	if m == nil {
		return
	}
	for i := range m.Voices {
		if e := m.Voices[i].Enhance; e != nil && e.Strength != nil {
			x := math.Round(math.Max(0, math.Min(1, *e.Strength))*100) / 100
			e.Strength = &x
		}
		if g := m.Voices[i].GainDB; g != nil {
			x := math.Round(*g*10) / 10
			m.Voices[i].GainDB = &x
		}
	}
	for i := range m.Enhanced {
		m.Enhanced[i].Start = math.Max(0, math.Floor(m.Enhanced[i].Start+1e-9))
	}
}
