package qa

import (
	"fmt"
	"math"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// Cut and transition checks (QA version qa-3).
const (
	FlashShotFrames   = 2   // a shot on screen this many frames or fewer looks like a glitch
	ShortShotSeconds  = 0.4 // shorter shots are pointed out (they can be intended in fast styles)
	SoundBlipSeconds  = 0.25
	CutLoudnessJumpLU = 6.0 // speech this much louder or quieter across a cut is noticeable
	CutLoudnessWindow = 4.0 // seconds of each clip's speech next to the cut that are measured
)

// cuts reports transitions and split edits that do not play as asked or that hurt
// the speech, from the cut report Go resolved for the rendered spec (heuristic:
// plan, footage lengths and transcripts), and shots too short to read.
func (r *run) cuts() {
	const name = "transitions"
	if len(r.in.Cuts) == 0 && len(r.in.Shots) <= 1 {
		r.skip(name, models.QAHeuristic, "the cut has a single clip")
		return
	}
	for _, ci := range r.in.Cuts {
		from, to := math.Max(0, ci.At-0.3), ci.At+0.3
		ids := []string{ci.PrevClipID, ci.ClipID}
		where := clock(ci.At)
		if ci.WantSeconds > 0 && ci.GotSeconds < ci.WantSeconds {
			r.add(&models.QAFinding{Severity: models.QAInfo, Kind: "transition_shortened", Basis: models.QAHeuristic, From: from, To: to, ClipIDs: ids,
				Message: fmt.Sprintf("Around %s the %s is shorter than asked (%s instead of %s): the shots around it are too short for more.", where, typeName(ci.Type), secs(ci.GotSeconds), secs(ci.WantSeconds)),
				Fix:     r.transitionFix(ci, fmt.Sprintf("Make the %s %s long, as it plays, or use longer shots around it.", typeName(ci.Type), secs(ci.GotSeconds)), func(t *editspec.Transition) { t.Duration = ci.GotSeconds })})
		}
		if ci.Limited != "" {
			got := editspec.SplitAchieved(ci)
			var summary string
			if ci.Mode == editspec.SoundLead {
				summary = fmt.Sprintf("Let %s start only %s early, as much as the footage allows, so the cut says what really plays; or start %s later in its recording, so there is more sound before it.", ci.Voice, secs(got), ci.Who)
			} else {
				summary = fmt.Sprintf("Let %s carry over only %s, as much as the footage allows, so the cut says what really plays; or end %s earlier in its recording, so there is more sound after it.", ci.PrevVoice, secs(got), ci.PrevWho)
			}
			r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "split_too_short", Basis: models.QAHeuristic, From: from, To: to, ClipIDs: ids,
				Message: editspec.SplitLimitedMessage(ci) + " (around " + where + ").",
				Fix: r.transitionFix(ci, summary, func(t *editspec.Transition) {
					if t.Audio != nil {
						t.Audio.Duration = math.Round(got*100) / 100
					}
				})})
		}
		if ci.SoundSet && (len(ci.Clipped) > 0 || ci.NoPause) {
			msg := fmt.Sprintf("Around %s the sound change cuts %s short.", where, quote(ci.Clipped))
			if len(ci.Clipped) == 0 {
				msg = fmt.Sprintf("Around %s the sound changes in the middle of someone talking.", where)
			}
			fix := r.transitionFix(ci, "Let MetaRush move the sound change into the nearest pause.", func(t *editspec.Transition) {
				t.Audio = &editspec.SoundTransition{Mode: editspec.SoundAuto}
			})
			if ci.Mode == editspec.SoundAuto {
				fix = &models.QAFix{Summary: "There's no pause close enough: move the cut itself to a pause between words (change where the clips start or end)."}
			}
			r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "split_cuts_word", Basis: models.QAHeuristic, From: from, To: to, ClipIDs: ids, Message: msg, Fix: fix})
		}
		if ci.SoundSet && len(ci.DroppedPrev)+len(ci.DroppedNext) > 0 {
			var msg string
			if len(ci.DroppedPrev) > 0 {
				msg = fmt.Sprintf("Around %s, with %s starting early, the end of %s loses %s.", where, ci.Voice, ci.PrevWho, quote(ci.DroppedPrev))
			} else {
				msg = fmt.Sprintf("Around %s, with %s carrying over, %s loses its first words, %s.", where, ci.PrevVoice, ci.Who, quote(ci.DroppedNext))
			}
			r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "split_drops_words", Basis: models.QAHeuristic, From: from, To: to, ClipIDs: ids, Message: msg,
				Fix: r.transitionFix(ci, "Let MetaRush move the sound change into the nearest pause, so nobody's words are lost.", func(t *editspec.Transition) {
					t.Audio = &editspec.SoundTransition{Mode: editspec.SoundAuto}
				})})
		}
		if ci.Overlap && (ci.SoundFollows || ci.BlendSeconds >= 0.15) {
			msg := fmt.Sprintf("Around %s two people talk at once while the %s blends their sound.", where, typeName(ci.Type))
			if !ci.SoundFollows {
				msg = fmt.Sprintf("Around %s two people talk at once while their sound blends.", where)
			}
			r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "voices_overlap", Basis: models.QAHeuristic, From: from, To: to + 0.3, ClipIDs: ids, Message: msg,
				Fix: r.transitionFix(ci, "Keep the picture and let the sound change in a pause instead of blending.", func(t *editspec.Transition) {
					t.Audio = &editspec.SoundTransition{Mode: editspec.SoundAuto}
				})})
		}
	}
	for _, sh := range r.in.Shots {
		at := sh.At
		dur := float64(sh.VisibleFrames) / r.fps
		switch {
		case sh.VisibleFrames > 0 && sh.VisibleFrames <= FlashShotFrames:
			fix := &models.QAFix{Summary: fmt.Sprintf("Remove %s, or make it at least half a second long.", sh.Who)}
			if sc := r.specClip(sh.ClipID); sc != nil && !sc.Locked {
				fix.Ops = []editspec.Op{{Op: "removeClip", ID: sh.ClipID}}
			}
			r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "flash_shot", Basis: models.QAHeuristic, From: at, To: at + dur, ClipIDs: []string{sh.ClipID},
				Message: fmt.Sprintf("Around %s %s flashes by in %d frame%s, which looks like a glitch.", clock(at), sh.Who, sh.VisibleFrames, plural(sh.VisibleFrames)), Fix: fix})
		case sh.VisibleFrames > 0 && dur < ShortShotSeconds:
			r.add(&models.QAFinding{Severity: models.QAInfo, Kind: "short_shot", Basis: models.QAHeuristic, From: at, To: at + dur, ClipIDs: []string{sh.ClipID},
				Message: fmt.Sprintf("Around %s %s is on screen for only %s: fine in a fast montage, hard to take in otherwise.", clock(at), sh.Who, seconds(dur)),
				Fix:     &models.QAFix{Summary: "Make it longer if people should take it in."}})
		}
		if sh.Audible && sh.SoundFrames > 0 && float64(sh.SoundFrames)/r.fps < SoundBlipSeconds {
			r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "sound_blip", Basis: models.QAHeuristic, From: at, To: at + float64(sh.SoundFrames)/r.fps, ClipIDs: []string{sh.ClipID},
				Message: fmt.Sprintf("Around %s the sound of %s plays for only a moment (%s), which sounds like a blip.", clock(at), sh.Who, seconds(float64(sh.SoundFrames)/r.fps)),
				Fix:     &models.QAFix{Summary: "Turn its sound off, or let the sound around it carry over it."}})
		}
	}
	r.check(name, models.QAHeuristic, fmt.Sprintf("splits and transitions as asked (footage and neighbouring shots allow it), no word cut or lost at a sound change, one voice at a time, shots ≥ %d frames (≥ %.1f s to read), sound ≥ %.2f s", FlashShotFrames+1, ShortShotSeconds, SoundBlipSeconds),
		"transition_shortened", "split_too_short", "split_cuts_word", "split_drops_words", "voices_overlap", "flash_shot", "short_shot", "sound_blip")
}

// transitionFix is a setTransition op on the rendered spec's transition into a
// clip, changed by edit (nothing when the clip is locked).
func (r *run) transitionFix(ci editspec.CutInfo, summary string, edit func(t *editspec.Transition)) *models.QAFix {
	fix := &models.QAFix{Summary: summary}
	sc := r.specClip(ci.ClipID)
	if sc == nil || sc.Locked {
		return fix
	}
	t := editspec.Transition{Type: ci.Type}
	if sc.Transition != nil {
		t = *editspec.Clone(&editspec.Spec{Clips: []editspec.Clip{*sc}}).Clips[0].Transition
	}
	if t.Type == "" {
		t.Type = editspec.TransitionCut
	}
	edit(&t)
	fix.Ops = []editspec.Op{{Op: "setTransition", ID: ci.ClipID, Transition: &t}}
	return fix
}

// cutLoudness measures, in the render, how loud the speech is on each side of
// every cut between two speaking clips, and flags jumps people notice (observed:
// the render's own momentary loudness over the words the plan expects there).
func (r *run) cutLoudness() {
	const name = "cut_loudness"
	s := r.det.Momentary
	switch {
	case !r.mi.HasAudio:
		r.skip(name, models.QAObserved, "the render has no sound")
		return
	case s == nil:
		r.skip(name, models.QAObserved, "how loud the render is from moment to moment couldn't be measured")
		return
	}
	words := map[string][][2]float64{}
	for _, w := range r.in.ExpectedSpeech {
		if w.Kind == "dialogue" {
			words[w.ClipID] = append(words[w.ClipID], [2]float64{w.From, w.To})
		}
	}
	speech := func(ws [][2]float64, from, to float64) float64 {
		t := 0.0
		for _, w := range ws {
			t += math.Max(0, math.Min(w[1], to)-math.Max(w[0], from))
		}
		return t
	}
	measured := 0
	for _, ci := range r.in.Cuts {
		if !ci.Audible || !ci.PrevAudible || ci.Index <= 0 || ci.Index >= len(r.plan.Clips) {
			continue
		}
		// Each side is measured where only its own sound plays: the outgoing
		// clip up to the sound change, the incoming one after the blend (or the
		// overlap of a dissolve).
		a, b := &r.plan.Clips[ci.Index-1], &r.plan.Clips[ci.Index]
		aFrom, aDur, _ := a.Sound()
		bFrom, bDur, _ := b.Sound()
		edgeA, edgeB := float64(bFrom)/r.fps, float64(aFrom+aDur)/r.fps
		beforeFrom, beforeTo := math.Max(float64(aFrom)/r.fps, ci.At-CutLoudnessWindow), edgeA
		afterFrom, afterTo := edgeB, math.Min(float64(bFrom+bDur)/r.fps, ci.At+CutLoudnessWindow)
		if speech(words[ci.PrevClipID], beforeFrom, beforeTo) < 1 || speech(words[ci.ClipID], afterFrom, afterTo) < 1 {
			continue
		}
		before, okB := s.Integrated(beforeFrom, beforeTo)
		after, okA := s.Integrated(afterFrom, afterTo)
		if !okB || !okA {
			continue
		}
		measured++
		jump := round3(after - before)
		r.rep.Measurements.CutLoudness = append(r.rep.Measurements.CutLoudness, models.QACutLoudness{At: round3(ci.At), ClipID: ci.ClipID, Before: round3(before), After: round3(after), Jump: jump})
		if math.Abs(jump) < CutLoudnessJumpLU {
			continue
		}
		louder, quieter, way := ci.Who, ci.PrevWho, "louder"
		louderID := ci.ClipID
		if jump < 0 {
			louder, quieter, way = ci.PrevWho, ci.Who, "quieter"
			louderID = ci.PrevClipID
		}
		msg := fmt.Sprintf("Around %s the voice gets much %s at the cut: %s sounds %s than %s.", clock(ci.At), way, ci.Who, way, ci.PrevWho)
		var fix *models.QAFix
		if r.in.Spec != nil && !r.in.Spec.LevelsSpeech() && !(r.in.Spec.Mix != nil && r.in.Spec.Mix.Locked) {
			m := editspec.Mix{}
			if r.in.Spec.Mix != nil {
				m = *r.in.Spec.Mix
			}
			on := true
			m.LevelSpeech = &on
			fix = &models.QAFix{Summary: "Turn on “Even out voices” on the Sound tab, then render again.", Ops: []editspec.Op{{Op: "setMix", Mix: &m}}}
		} else {
			fix = &models.QAFix{Summary: fmt.Sprintf("Turn %s down about %.0f dB so it matches %s.", louder, math.Abs(jump), quieter)}
			if sc := r.specClip(louderID); sc != nil && !sc.Locked {
				vol := 1.0
				if sc.Volume != nil {
					vol = *sc.Volume
				}
				nv := math.Round(math.Max(0, vol*math.Pow(10, -math.Abs(jump)/20))*100) / 100
				fix.Ops = []editspec.Op{setOp("updateClip", louderID, map[string]any{"volume": nv})}
			}
		}
		r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "loudness_jump", Basis: models.QAObserved, From: math.Max(0, ci.At-1), To: ci.At + 1, ClipIDs: []string{ci.PrevClipID, ci.ClipID}, Message: msg, Fix: fix})
	}
	if measured == 0 {
		r.skip(name, models.QAObserved, "no cut between two clips with speech on both sides")
		return
	}
	r.check(name, models.QAObserved, fmt.Sprintf("speech loudness (EBU R128 gating over the words within %.0f s of the cut) differs by less than %.0f LU across each cut", CutLoudnessWindow, CutLoudnessJumpLU), "loudness_jump")
}

func typeName(t string) string {
	if info, ok := editspec.TransitionByType(t); ok {
		return strings.ToLower(info.Name)
	}
	return t
}

func secs(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".") + " s"
}

func quote(ws []string) string { return "“" + strings.Join(limitWords(ws, 6), " ") + "”" }

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
