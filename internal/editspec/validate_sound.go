package editspec

import (
	"fmt"
	"strings"
)

// validateSound checks the per-person settings (mix.voices), the processed
// copies recordings play (mix.enhanced) and music timing (audio fit/loop).
// Problems that only make a setting do nothing are warnings; references that
// cannot play are errors.
func validateSound(s *Spec, facts Facts, clips []PlanClip) []Issue {
	var out []Issue
	for i, a := range s.Audio {
		p := fmt.Sprintf("/audio/%d", i)
		switch a.Fit {
		case "", TrackFitAuto, TrackFitManual:
		default:
			out = append(out, errI("bad_fit", p+"/fit", "%s is either timed to the video (fit auto) or placed by hand (fit manual), not %q.", upperFirst(trackWords(a)), a.Fit))
		}
		if a.Loop && a.Fit != TrackFitAuto {
			out = append(out, warnI("loop_needs_fit", p+"/loop", "%s only loops when it is timed to the video (fit auto); placed by hand, it plays once.", upperFirst(trackWords(a))))
		}
	}
	m := s.Mix
	if m == nil || (len(m.Voices) == 0 && len(m.Enhanced) == 0) {
		return out
	}
	// Who speaks in the cut, from the clips' own sound and the dialogue tracks.
	type heard struct {
		asset    string
		from, to float64
		clip     int // -1 for a dialogue track
	}
	var plays []heard
	for i, c := range clips {
		if c.Muted || i >= len(s.Clips) {
			continue
		}
		plays = append(plays, heard{c.AssetID, c.SourceIn, c.SourceOut, i})
	}
	for _, a := range s.Audio {
		if a.Role == RoleDialogue {
			plays = append(plays, heard{a.AssetID, a.In, a.Out, -1})
		}
	}
	speakersOf := func(asset string, from, to float64) []string {
		if facts == nil {
			return nil
		}
		return SpeakersIn(facts.Transcript(asset), from, to)
	}
	inCut := map[string]bool{} // voiceKey of every speaker heard
	assetsPlayed := map[string]bool{}
	for _, h := range plays {
		assetsPlayed[h.asset] = true
		for _, sp := range speakersOf(h.asset, h.from, h.to) {
			inCut[voiceKey(sp)] = true
		}
	}
	seen := map[string]int{}
	for i, v := range m.Voices {
		p := fmt.Sprintf("/mix/voices/%d", i)
		key := ""
		switch {
		case (v.Name == "") == (v.AssetID == ""):
			out = append(out, errI("voice_target", p, "Each voice setting is for one person (name) or one recording (assetId), not both or neither."))
			continue
		case v.Name != "":
			key = "n:" + voiceKey(v.Name)
		default:
			key = "a:" + v.AssetID
		}
		if j, dup := seen[key]; dup {
			out = append(out, errI("duplicate_voice", p, "%s has two voice settings (voice settings %d and %d); keep one.", voiceWho(v, facts), j+1, i+1))
			continue
		}
		seen[key] = i
		if e := v.Enhance; e != nil && !containsStr(EnhanceModes, e.Mode) {
			out = append(out, errI("bad_enhance", p+"/enhance/mode", "%s: a voice is made clearer with clean, studio or cloud, not %q.", voiceWho(v, facts), e.Mode))
		}
		if facts == nil {
			continue
		}
		switch {
		case v.Name != "" && !inCut[voiceKey(v.Name)]:
			out = append(out, warnI("voice_not_in_cut", p+"/name", "Nobody called %s speaks in this cut, so their voice settings change nothing (names come from the voices people named).", strings.TrimSpace(v.Name)))
		case v.AssetID != "" && !assetsPlayed[v.AssetID]:
			out = append(out, warnI("voice_not_in_cut", p+"/assetId", "%s isn't heard in this cut, so its voice settings change nothing.", voiceWho(v, facts)))
		}
	}
	for i, e := range m.Enhanced {
		p := fmt.Sprintf("/mix/enhanced/%d", i)
		if facts == nil {
			continue
		}
		src, ok := facts.Asset(e.AssetID)
		if !ok {
			out = append(out, errI("missing_asset", p+"/assetId", "A clearer copy of a voice belongs to a recording that isn't in this workspace any more; turn that clearer voice off on the Sound tab."))
			continue
		}
		v, ok := facts.Asset(e.Variant)
		switch {
		case !ok:
			out = append(out, errI("missing_asset", p+"/variant", "The clearer copy of %s is missing; make it again on the Sound tab.", src.Name))
			continue
		case !v.HasAudio:
			out = append(out, errI("no_audio_stream", p+"/variant", "The clearer copy of %s has no sound; make it again on the Sound tab.", src.Name))
			continue
		case v.State != "" && v.State != "available":
			out = append(out, errI("asset_not_ready", p+"/variant", "The clearer copy of %s is still being saved; wait a moment, then check again.", src.Name))
			continue
		}
		want := m.VoiceSettingsFor(e.AssetID, speakersOf(e.AssetID, e.Start, e.Start+v.Duration))
		switch {
		case want == "":
			out = append(out, warnI("enhanced_unused", p, "The clearer copy of %s isn't needed any more (nobody in it is set to sound clearer), but it keeps playing until it is removed.", src.Name))
		case want != e.Settings:
			out = append(out, Issue{Severity: "warning", Code: "enhanced_outdated", Path: p,
				Message: fmt.Sprintf("The clearer copy of %s was made with other settings (%s; now %s); make it again on the Sound tab to update it.", src.Name, e.Settings, want)})
		}
	}
	// Clips whose people should sound clearer but have no copy covering them.
	if facts != nil {
		for _, h := range plays {
			if h.clip < 0 {
				continue
			}
			who := activeIn(m, h.asset, speakersOf(h.asset, h.from, h.to))
			if who == "" || m.variantFor(h.asset, h.from, h.to, facts) != nil {
				continue
			}
			out = append(out, Issue{Severity: "warning", Code: "enhance_missing", Path: fmt.Sprintf("/clips/%d", h.clip),
				Message: fmt.Sprintf("%s plays %s original voice; make the clearer copy for it on the Sound tab.", upperFirst(clipWords(s.Clips[h.clip])), who)})
		}
	}
	return out
}

// activeIn names whose voice in a stretch should sound clearer ("Maya's",
// "everyone's"); "" when nobody's.
func activeIn(m *Mix, asset string, speakers []string) string {
	if rv := m.RecordingVoice(asset); rv != nil && rv.Enhance.Active() {
		return "everyone's"
	}
	var names []string
	for _, sp := range speakers {
		if v := m.VoiceFor(sp); v != nil && v.Enhance.Active() {
			names = append(names, strings.TrimSpace(sp)+"'s")
		}
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// voiceWho names whom a voice setting is for: the person, else the
// recording's title (messages start with it).
func voiceWho(v VoiceSound, facts Facts) string {
	if v.Name != "" {
		return strings.TrimSpace(v.Name)
	}
	if facts != nil {
		if a, ok := facts.Asset(v.AssetID); ok && a.Name != "" {
			return a.Name
		}
	}
	return "A recording"
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
