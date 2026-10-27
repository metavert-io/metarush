package editspec

import (
	"fmt"
	"strings"
)

// LineName is how people see a narration line in messages: `Narration line 3
// (“We built it twice…”)`, or `Narration line 3` while it has no words. Cut
// sheets name the line by its bullet instead (cutsheet.Locate); inside a
// sentence it reads with a small n (lowerFirst).
func LineName(i int, text string) string {
	if t := excerpt(text, 32); t != "" {
		return fmt.Sprintf("Narration line %d (“%s”)", i+1, t)
	}
	return fmt.Sprintf("Narration line %d", i+1)
}

func lineName(i int, text string) string { return LineName(i, text) }

func excerpt(text string, n int) string {
	t := strings.Join(strings.Fields(text), " ")
	r := []rune(t)
	if len(r) <= n {
		return t
	}
	cut := string(r[:n])
	if i := strings.LastIndex(cut, " "); i > n/2 {
		cut = cut[:i]
	}
	return cut + "…"
}

// Clock formats timeline seconds as m:ss.s for people.
func Clock(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	m := int(sec) / 60
	return fmt.Sprintf("%d:%04.1f", m, sec-float64(m*60))
}

// validateVoiceover checks the narration: takes exist and fit their assets,
// anchors resolve, lines stay in order, and each line fits its window.
func validateVoiceover(s *Spec, facts Facts, clips []PlanClip, total int, checkID func(id, path string)) []Issue {
	v := s.Voiceover
	if v == nil {
		return nil
	}
	out := validateVersions(v)
	fps := float64(s.Output.FPS)
	checkTake := func(path, who, assetID string, in, out_ float64) []Issue {
		var is []Issue
		a, ok := facts.Asset(assetID)
		switch {
		case !ok:
			return append(is, errI("missing_asset", path+"/assetId", "%s uses a recording that isn't in this workspace; record it again or choose another take.", who))
		case !a.HasAudio:
			is = append(is, errI("no_audio_stream", path+"/assetId", "%s uses %s, which has no sound; choose another recording.", who, a.Name))
		case a.State == "failed":
			is = append(is, errI("asset_not_ready", path+"/assetId", "%s uses %s, which couldn't be added (importing it failed); add the recording again.", who, a.Name))
		case a.State != "available":
			is = append(is, errI("asset_not_ready", path+"/assetId", "%s uses %s, which isn't ready yet (it's still being added); wait for it to finish, then check again.", who, a.Name))
		}
		if !a.InProject {
			is = append(is, errI("asset_not_in_project", path+"/assetId", "%s uses %s, which isn't part of this project yet; add it to the project first.", who, a.Name))
		}
		if out_ <= in {
			is = append(is, errI("empty_range", path, "%s plays an empty stretch of its recording (it ends before it starts).", who))
		}
		if a.Duration > 0 && out_ > a.Duration+frameTol {
			is = append(is, errI("out_of_range", path+"/out", "%s runs past the end of %s, which is only %s long.", who, a.Name, seconds(a.Duration)))
		}
		return is
	}
	if v.Layout == LayoutContinuous {
		if v.Continuous == nil {
			out = append(out, errI("continuous_missing", "/voiceover/continuous", "The narration is set to play as one recording, but no recording is chosen yet."))
		} else {
			c := v.Continuous
			out = append(out, checkTake("/voiceover/continuous", "The narration recording", c.AssetID, c.In, c.Out)...)
			if c.Processed != nil {
				out = append(out, checkTake("/voiceover/continuous/processed", "The cleaned-up narration", c.Processed.AssetID, c.In+c.Processed.Offset, c.Out+c.Processed.Offset)...)
			}
			if _, prob := resolveAnchor(c.Anchor, clips, fps, -1, s.beatGap()); prob != "" {
				out = append(out, errI("anchor_unresolved", "/voiceover/continuous/anchor", "The narration recording can't be placed: %s. Choose where it starts again.", prob))
			}
		}
	}
	beats := ResolveBeats(s, clips, total, facts)
	prevStart := -1
	for i, b := range v.Beats {
		p := fmt.Sprintf("/voiceover/beats/%d", i)
		checkID(b.ID, p)
		who := lineName(i, b.Text)
		if strings.TrimSpace(b.Text) == "" {
			out = append(out, errI("beat_empty", p+"/text", "Narration line %d has no words; write the line or remove it.", i+1))
		}
		out = append(out, anchorShape(b.Anchor, p+"/anchor", who)...)
		if t := b.Take; t != nil {
			out = append(out, checkTake(p+"/take", who, t.AssetID, t.In, t.Out)...)
			if t.Processed != nil {
				out = append(out, checkTake(p+"/take/processed", "The cleaned-up take of "+lowerFirst(who), t.Processed.AssetID, t.In+t.Processed.Offset, t.Out+t.Processed.Offset)...)
			}
			if v.Layout == LayoutContinuous && v.Continuous != nil && t.AssetID != v.Continuous.AssetID {
				out = append(out, warnI("take_outside_continuous", p+"/take", "%s has its own recording, but the narration plays as one continuous recording, so that recording won't be heard.", who))
			}
		}
		rb := beats[i]
		if rb.Problem != "" {
			out = append(out, errI("anchor_unresolved", p+"/anchor", "%s can't be placed: %s. Choose where it starts again.", who, rb.Problem))
			continue
		}
		start := float64(rb.Start) / fps
		switch {
		case rb.Start >= total:
			out = append(out, errI("beat_after_end", p+"/anchor", "%s would start at %s, after the video ends (%s); start it earlier or remove it.", who, Clock(start), Clock(float64(total)/fps)))
			continue
		case rb.Start < 0:
			out = append(out, warnI("beat_before_start", p+"/anchor", "%s would start before the video begins, so its first %.1f seconds are cut off.", who, -start))
		}
		if prevStart >= 0 && rb.Start < prevStart {
			out = append(out, warnI("beats_out_of_order", p+"/anchor", "%s now starts before the line above it (the shots were probably rearranged); move one of them so the narration plays in order.", who))
		}
		prevStart = rb.Start
		out = append(out, fitIssue(s, rb, p, who)...)
		if v.Layout == LayoutContinuous && b.Take != nil && v.Continuous != nil && b.Take.AssetID == v.Continuous.AssetID {
			drift := float64(rb.Start-rb.AnchorFrame) / fps
			switch {
			case drift < -2/fps:
				out = append(out, warnI("beat_before_anchor", p+"/anchor", "%s plays %.1f seconds before the spot it belongs to, because the narration plays as one recording and the video has changed; split the recording at each line to keep every line in place.", who, -drift))
			case drift > 0.25:
				out = append(out, warnI("beat_after_anchor", p+"/anchor", "%s plays %.1f seconds after the spot it belongs to; split the recording at each line to keep every line in place.", who, drift))
			}
		}
		// Narration over on-camera speech (the clip sound is turned down, but people notice).
		if b.Take != nil && !mixFor(s).busMuted[BusSource] {
			for _, d := range dialogueRanges(s, clips, facts) {
				from, to := max(d.From, rb.Start), min(d.To, rb.End())
				if to-from >= int(fps*0.6) {
					out = append(out, warnI("voiceover_over_dialogue", p, "%s talks over someone speaking on camera (%s–%s); the camera sound dips under the narration there, but you may want to move the line.", who, Clock(float64(from)/fps), Clock(float64(to)/fps)))
					break
				}
			}
		}
	}
	return out
}

// anchorShape checks the fields an anchor kind needs.
func anchorShape(a Anchor, path, who string) []Issue {
	switch a.Kind {
	case AnchorTime:
		if a.At == nil {
			return []Issue{errI("anchor_incomplete", path+"/at", "%s is set to start at a set time, but no time is given.", who)}
		}
	case AnchorClip:
		if a.ClipID == "" {
			return []Issue{errI("anchor_incomplete", path+"/clipId", "%s is set to start with a clip, but no clip is named.", who)}
		}
	case AnchorMoment:
		if a.AssetID == "" || a.At == nil {
			return []Issue{errI("anchor_incomplete", path, "%s is set to follow a moment in the footage, but the footage or the time is missing.", who)}
		}
	case AnchorAfter:
	default:
		return []Issue{errI("anchor_kind", path+"/kind", "%s has a kind of start point MetaRush doesn't know (%q).", who, a.Kind)}
	}
	return nil
}

// fitIssue warns when a line does not fit before the next one (or before the
// video ends, for the last line: rb.Until says which).
func fitIssue(s *Spec, rb ResolvedBeat, path, who string) []Issue {
	fps := float64(s.Output.FPS)
	window := float64(rb.Window()) / fps
	need := float64(rb.Speech) / fps
	if rb.Speech <= rb.Window()+1 {
		return nil
	}
	if rb.Estimated {
		return []Issue{warnI("beat_fit", path, "%s has %d words but only %.1f seconds %s — it needs about %.1f seconds at the narrator's pace.", who, rb.Words, window, rb.Until(), need)}
	}
	if rb.Next < 0 {
		return []Issue{warnI("beat_fit", path, "The recording of %s runs %.1f seconds, but there are only %.1f seconds before the video ends, so its last %.1f seconds are cut off.", lowerFirst(who), need, window, need-window)}
	}
	return []Issue{warnI("beat_fit", path, "The recording of %s runs %.1f seconds, but there are only %.1f seconds %s, so they overlap by %.1f seconds.", lowerFirst(who), need, window, rb.Until(), need-window)}
}

// validateMix checks bus settings for results people would not expect.
func validateMix(s *Spec) []Issue {
	m := s.Mix
	if m == nil {
		return nil
	}
	var out []Issue
	names := map[string]string{BusSource: "The camera sound", BusVoiceover: "The narration", BusMusic: "The music", BusSFX: "The sound effects"}
	buses := map[string]*Bus{BusSource: m.Source, BusVoiceover: m.Voiceover, BusMusic: m.Music, BusSFX: m.SFX}
	silent := 0
	mp := mixFor(s)
	for _, b := range Buses {
		if x := buses[b]; x != nil && x.Mute && x.Solo {
			out = append(out, warnI("bus_solo_muted", "/mix/"+b, "%s is set to play on its own (solo) and is also muted, so it stays silent.", names[b]))
		}
		if mp.busMuted[b] {
			silent++
		}
	}
	if silent == len(Buses) {
		out = append(out, warnI("mix_silent", "/mix", "Every sound is muted, so the video will be silent."))
	}
	if (m.MuteSource || mp.busMuted[BusSource]) && s.Captions != nil && s.Captions.Enabled {
		if d, _ := captionSources(s.Captions); d {
			out = append(out, warnI("captions_source_muted", "/captions/source", "The camera sound is muted, so captions of what people say on camera are left out; caption the narration instead (source voiceover)."))
		}
	}
	return out
}

// voiceoverLockViolations checks locked narration in base against next.
func voiceoverLockViolations(base, next *Spec) []Issue {
	var out []Issue
	if base.Voiceover != nil {
		if base.Voiceover.Locked && !equalJSON(base.Voiceover, next.Voiceover) {
			out = append(out, errI("locked", "/voiceover", "The narration is locked, so it can't be changed."))
		}
		nb := map[string]Beat{}
		if next.Voiceover != nil {
			for _, b := range next.Voiceover.Beats {
				nb[b.ID] = b
			}
		}
		for i, b := range base.Voiceover.Beats {
			if !b.Locked {
				continue
			}
			n, ok := nb[b.ID]
			switch {
			case !ok:
				out = append(out, errI("locked", "/voiceover/beats", "%s is locked, so it can't be removed.", lineName(i, b.Text)))
			case !equalJSON(b, n):
				out = append(out, errI("locked", "/voiceover/beats", "%s is locked, so it can't be changed.", lineName(i, b.Text)))
			}
		}
	}
	if base.Mix != nil && base.Mix.Locked && !equalJSON(base.Mix, next.Mix) {
		out = append(out, errI("locked", "/mix", "The sound mix is locked, so it can't be changed."))
	}
	return out
}
