package qa

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/metavert-io/metarush/internal/derive"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/textalign"
	"github.com/metavert-io/metarush/internal/transcribe"
)

// Narration QA thresholds.
const (
	NarrationGapLU     = 10.0 // narration should be this much louder than what plays under it
	NarrationPeakDBTP  = -0.5 // narration peaks above this (after its gain) distort
	RecordingClipDBFS  = -0.1 // a recording whose own peaks reach this level was clipped when recorded
	ScriptMatchMin     = 0.85 // share of a line's words a recording must contain
	AnchorTolFrames    = 2
	ContinuitySkewSecs = 1.5 // words further apart than this are never the same word
)

// Loudness is a source asset's measured loudness. TruePeak is the file's own
// peak; a Mono source's peaks land 3 dB lower in the stereo render.
type Loudness struct {
	LUFS     float64
	TruePeak float64
	Mono     bool
}

// mixPeak is where a source's true peak lands in the stereo render.
func (l Loudness) mixPeak() float64 {
	if l.Mono {
		return l.TruePeak + editspec.MonoUpmixDB
	}
	return l.TruePeak
}

// clock formats timeline seconds as m:ss for people ("0:27").
func clock(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	s := int(math.Round(sec))
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func lineLabel(line int, text string) string {
	t := strings.Join(strings.Fields(text), " ")
	if r := []rune(t); len(r) > 36 {
		t = string(r[:35]) + "…"
	}
	return fmt.Sprintf("narration line %d (“%s”)", line, t)
}

// gainDB is an element's gain (dB) at an element-relative frame: its envelope,
// or its static gain when it has none.
func gainDB(env []editspec.GainPoint, base float64, f int) float64 {
	g := editspec.GainAt(env, float64(f))
	if len(env) == 0 {
		g = math.Pow(10, base/20)
	}
	if g <= 1e-6 {
		return -120
	}
	return 20 * math.Log10(g)
}

// narration checks a cut with a voiceover: narration over on-camera speech,
// music or clip sound too loud under it, distortion, recordings that do not
// match their lines, and lines that play before their spot. Plan-based
// (heuristic); the observed checks are speech continuity and silence.
func (r *run) narration() {
	pv := r.plan.Voiceover
	if pv == nil || len(pv.Beats) == 0 || r.in.Spec == nil || r.in.Spec.Voiceover == nil {
		r.skip("narration", models.QAHeuristic, "the cut has no narration")
		return
	}
	beats := r.in.Spec.Voiceover.Beats
	byID := map[string]editspec.Beat{}
	for _, b := range beats {
		byID[b.ID] = b
	}
	// Narration elements and their gain (dB) at a timeline frame.
	type elem struct {
		a    editspec.PlanAudio
		beat string
	}
	var vo, others []elem
	for _, a := range r.plan.Audio {
		if a.Bus == editspec.BusVoiceover || a.Role == editspec.RoleVoiceover {
			vo = append(vo, elem{a, a.BeatID})
		} else {
			others = append(others, elem{a, ""})
		}
	}
	fps := r.fps
	for _, pb := range pv.Beats {
		b := byID[pb.ID]
		label := lineLabel(pb.Index+1, b.Text)
		if !pb.HasAudio || pb.Problem != "" {
			continue
		}
		from, to := float64(pb.From)/fps, float64(pb.To)/fps
		// Before its spot (continuous narration after re-edits).
		if pb.From < pb.AnchorFrame-AnchorTolFrames {
			early := float64(pb.AnchorFrame-pb.From) / fps
			r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "narration_early", Basis: models.QAHeuristic, From: from, To: from + early,
				Message: fmt.Sprintf("Around %s %s starts %s before the moment it belongs to, because the narration plays as one recording and the video changed.", clock(from), label, seconds(early)),
				Fix:     &models.QAFix{Summary: "Split the recording at each line so every line moves with its shot (import it again on the Voiceover tab and choose to split it at each line), or move where the narration starts."}})
		}
		// Over on-camera speech.
		for _, d := range r.plan.DialogueRanges {
			a, z := math.Max(float64(d.From)/fps, from), math.Min(float64(d.To)/fps, to)
			if z-a >= 0.6 {
				cs := r.clipsAt(int(a*fps), int(z*fps))
				where, sound := "on camera", "the clip's own sound"
				if len(cs) > 0 {
					where, sound = "in "+clipNames(ids(cs)), clipNames(ids(cs))+"' own sound"
					if len(cs) == 1 {
						sound = clipName(cs[0].ID) + "'s own sound"
					}
				}
				r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "narration_over_dialogue", Basis: models.QAHeuristic, From: a, To: z, ClipIDs: ids(cs),
					Message: fmt.Sprintf("Around %s %s plays over someone talking %s. Their voice is turned down, but two voices at once are hard to follow.", clock(a), label, where),
					Fix:     &models.QAFix{Summary: "Move the line to a pause, shorten it, or turn off " + sound + " there."}})
				break
			}
		}
		// The element that plays this line.
		var e *elem
		for i := range vo {
			if vo[i].beat == pb.ID || (vo[i].a.ID == "voiceover" && pb.From >= vo[i].a.From && pb.From < vo[i].a.From+vo[i].a.DurationInFrames) {
				e = &vo[i]
				break
			}
		}
		if e == nil {
			continue
		}
		vl, haveVO := r.in.Loudness[e.a.AssetID]
		mid := (pb.From + pb.To) / 2
		voDB := gainDB(e.a.Gain, e.a.GainDB, mid-e.a.From)
		// Distortion: the recording itself, or its gain pushes peaks over the top.
		if haveVO {
			switch {
			case vl.TruePeak >= RecordingClipDBFS:
				r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "narration_clipping", Basis: models.QAHeuristic, From: from, To: to,
					Message: fmt.Sprintf("The recording for %s already crackles: its loudest moments hit the top of the scale when it was recorded.", label),
					Fix:     &models.QAFix{Summary: "Record it again a little further from the microphone, or with the microphone turned down; cleaning it up can't undo the crackle."}})
			case vl.mixPeak()+voDB > NarrationPeakDBTP:
				r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "narration_clipping", Basis: models.QAHeuristic, From: from, To: to,
					Message: fmt.Sprintf("Around %s %s is turned up so far that its loudest moments crackle.", clock(from), label),
					Fix:     &models.QAFix{Summary: fmt.Sprintf("Turn the narration down about %.0f dB on the Sound tab.", vl.mixPeak()+voDB-NarrationPeakDBTP+1)}})
			}
		}
		// Masking: music and clip sound under the line should be ~10 LU quieter.
		if haveVO {
			bed := 0.0
			var loudest string
			loudestDB := -200.0
			for _, o := range others {
				if mid < o.a.From || mid >= o.a.From+o.a.DurationInFrames {
					continue
				}
				ol, ok := r.in.Loudness[o.a.AssetID]
				if !ok {
					continue
				}
				l := ol.LUFS + gainDB(o.a.Gain, o.a.GainDB, mid-o.a.From)
				bed += math.Pow(10, l/10)
				if l > loudestDB {
					loudestDB, loudest = l, o.a.Role
				}
			}
			for _, c := range r.plan.Clips {
				from, dur, gain := c.Sound()
				if c.Muted || !c.HasAudio || mid < from || mid >= from+dur {
					continue
				}
				if cl, ok := r.in.Loudness[c.AssetID]; ok {
					if c.SpeechLUFS != nil {
						cl.LUFS = *c.SpeechLUFS // what this clip plays, as speech leveling measured it
					}
					l := cl.LUFS + gainDB(gain, 20*math.Log10(math.Max(1e-6, c.Volume)), mid-from)
					bed += math.Pow(10, l/10)
					if l > loudestDB {
						loudestDB, loudest = l, "clip"
					}
				}
			}
			if bed > 0 {
				gap := vl.LUFS + voDB - 10*math.Log10(bed)
				if gap < NarrationGapLU {
					what := map[string]string{"music": "the music", "sfx": "the sound effects", "clip": "the clips' own sound", "dialogue": "the dialogue track"}[loudest]
					if what == "" {
						what = "the sound under it"
					}
					need := NarrationGapLU - gap
					msg := fmt.Sprintf("Around %s %s is almost as loud as %s, so the words may be hard to hear.", clock(from), what, label)
					if gap < 0 {
						msg = fmt.Sprintf("Around %s %s is louder than %s, so the words will be hard to hear.", clock(from), what, label)
					}
					fix := &models.QAFix{Summary: fmt.Sprintf("Turn %s down about %.0f dB on the Sound tab, or let it dip further while the narration plays.", what, math.Ceil(need))}
					if pm := r.plan.Mix; pm != nil {
						if vb := pm.Buses[editspec.BusVoiceover]; vb.GainDB <= -12 {
							// The narration itself is turned far down: that is the likely mistake.
							fix = &models.QAFix{Summary: fmt.Sprintf("The narration is turned down %.0f dB in the mix: turn it back up on the Sound tab.", -vb.GainDB)}
						}
					}
					r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "narration_masked", Basis: models.QAHeuristic, From: from, To: to, Message: msg, Fix: fix})
				}
			}
		}
		// Recordings that do not say the line.
		if t := b.Take; t != nil && (t.Source == models.TakeRecorded || t.Source == models.TakeImported) {
			words := r.in.TakeWords[t.AssetID]
			var heard []textalign.Word
			for _, w := range words {
				if m := (w.From + w.To) / 2; m >= t.In && m < t.Out {
					heard = append(heard, textalign.Word{Text: w.Text, Norm: textalign.Normalize(w.Text), From: w.From, To: w.To})
				}
			}
			if len(heard) > 0 {
				exp := textalign.FromText(b.Text, 0)
				steps := textalign.Align(exp, heard, 0)
				matched, total := textalign.Score(exp, steps)
				var missing, extra []string
				for _, s := range steps {
					switch s.Kind {
					case textalign.Del:
						if !textalign.IsFiller(exp[s.E].Norm) {
							missing = append(missing, exp[s.E].Text)
						}
					case textalign.Ins:
						if !textalign.IsFiller(heard[s.O].Norm) {
							extra = append(extra, heard[s.O].Text)
						}
					case textalign.Subst:
						missing, extra = append(missing, exp[s.E].Text), append(extra, heard[s.O].Text)
					}
				}
				if total > 0 && float64(matched)/float64(total) < ScriptMatchMin {
					var parts []string
					if len(missing) > 0 {
						parts = append(parts, "it leaves out “"+strings.Join(limitWords(missing, 6), " ")+"”")
					}
					if len(extra) > 0 {
						parts = append(parts, "it says “"+strings.Join(limitWords(extra, 6), " ")+"” instead")
					}
					r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "narration_script_mismatch", Basis: models.QAHeuristic, From: from, To: to,
						Message: fmt.Sprintf("The recording for %s doesn't match the script: %s.", label, strings.Join(parts, " and ")),
						Fix:     &models.QAFix{Summary: "Record the line again on the Voiceover tab, or change the script to what was said."}})
				}
			}
		}
	}
	r.check("narration", models.QAHeuristic, fmt.Sprintf("narration ≥ %.0f LU above what plays under it; peaks ≤ %.1f dB; recordings say ≥ %.0f%% of their line; lines start at their spot (±%d frames)", NarrationGapLU, NarrationPeakDBTP, ScriptMatchMin*100, AnchorTolFrames),
		"narration_early", "narration_over_dialogue", "narration_clipping", "narration_masked", "narration_script_mismatch")
}

// speechSource names where words the cut plays come from: narration line 2,
// the “hook” clip.
func (r *run) speechSource(w editspec.SpeechWord) string {
	if w.Kind == "narration" {
		if pv := r.plan.Voiceover; pv != nil {
			for _, b := range pv.Beats {
				if b.ID == w.BeatID {
					return fmt.Sprintf("narration line %d", b.Index+1)
				}
			}
		}
		return "the narration"
	}
	if w.ClipID != "" {
		return clipName(w.ClipID)
	}
	return ""
}

func limitWords(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return append(append([]string{}, xs[:n]...), "…")
}

// speechContinuity transcribes the render's own audio and compares it with the
// speech the plan expects (clip transcripts inside each clip, narration takes):
// repeated words (a glitch that plays a slice twice), missing words and speech
// nothing in the plan accounts for. Observed; tolerant of transcription noise.
func (r *run) speechContinuity(ctx context.Context) {
	const name = "speech_continuity"
	switch {
	case !r.mi.HasAudio:
		r.skip(name, models.QAObserved, "the render has no sound")
		return
	case r.in.Transcriber == nil:
		r.skip(name, models.QAObserved, "this machine has no transcriber set up, so the render's speech wasn't checked word by word")
		return
	case len(r.in.ExpectedSpeech) == 0:
		r.skip(name, models.QAObserved, "the cut has no speech with word timings to compare")
		return
	}
	dir := r.in.Scratch
	if dir == "" {
		dir = os.TempDir()
	}
	wav := filepath.Join(dir, "qa-speech.wav")
	defer os.Remove(wav)
	if err := (&derive.Deriver{T: r.t}).ExtractAudioWAV(ctx, r.in.Path, wav); err != nil {
		r.skipErr(name, models.QAObserved, "the render's sound couldn't be read, so its speech wasn't checked word by word", err)
		return
	}
	tr, err := r.in.Transcriber.Transcribe(ctx, wav, dir, r.in.SpeechLanguage)
	if err != nil {
		r.skipErr(name, models.QAObserved, "making a transcript of the render didn't work, so its speech wasn't checked word by word", err)
		return
	}
	var obs []textalign.Word
	for _, s := range tr.Segments {
		for _, w := range s.Words {
			if inSpans(w.From, w.To, r.in.UnknownSpeech) {
				continue // speech whose words the plan does not know (segment-only transcripts)
			}
			obs = append(obs, textalign.Word{Text: w.Text, Norm: textalign.Normalize(w.Text), From: w.From, To: w.To})
		}
	}
	// Groups are the clip or narration line a word belongs to ("a whole line is
	// missing"); findings name them as people know them.
	exp := make([]textalign.Word, 0, len(r.in.ExpectedSpeech))
	groupOf := map[string]int{}
	groupName := map[int]string{}
	for _, w := range r.in.ExpectedSpeech {
		key := w.Kind + "\x00" + w.ClipID + w.BeatID
		g, ok := groupOf[key]
		if !ok {
			g = len(groupOf)
			groupOf[key] = g
			groupName[g] = r.speechSource(w)
		}
		exp = append(exp, textalign.Word{Text: w.Text, Norm: textalign.Normalize(w.Text), From: w.From, To: w.To, Group: g})
	}
	steps := textalign.Align(exp, obs, ContinuitySkewSecs)
	opts := textalign.DefaultContinuity()
	opts.Edges = r.in.SpeechEdges
	issues := textalign.Continuity(exp, obs, steps, opts)
	// Timing jumps: a stutter too short to be heard as a word still shifts what follows.
	for _, j := range textalign.Jumps(exp, obs, steps, opts) {
		dup := false
		for _, is := range issues {
			if (is.Kind == textalign.Repeated || is.Kind == textalign.Missing) && math.Abs(is.From-j.From) < 1.5 {
				dup = true
			}
		}
		if !dup {
			issues = append(issues, j)
		}
	}
	r.rep.Measurements.SpeechWordsExpected, r.rep.Measurements.SpeechWordsHeard = len(exp), len(obs)
	matched, _ := textalign.Score(exp, steps)
	r.rep.Measurements.SpeechWordsMatched = matched
	for _, is := range issues {
		f0, f1 := int(is.From*r.fps), int(math.Max(is.To, is.From+0.1)*r.fps)
		cs := r.clipsAt(f0, f1)
		words := "“" + strings.Join(limitWords(is.Words, 8), " ") + "”"
		if is.Kind == textalign.Jump && len(is.Words) == 2 {
			words = "“" + is.Words[0] + "” and “" + is.Words[1] + "”"
		}
		f := &models.QAFinding{Basis: models.QAObserved, From: is.From, To: math.Max(is.To, is.From+0.1), ClipIDs: ids(cs), Severity: models.QAWarning}
		// from: where the words come from (" from narration line 2", " from the “hook” clip").
		from := ""
		if n := groupName[is.Group]; n != "" && is.Kind != textalign.Unexpected {
			from = " from " + n
		}
		switch is.Kind {
		case textalign.Repeated:
			f.Kind = "speech_repeated"
			if is.Phrase {
				f.Severity = models.QAError
				f.Message = fmt.Sprintf("Around %s a phrase%s plays twice in the render: %s.", clock(is.From), from, words)
			} else {
				f.Message = fmt.Sprintf("Around %s a word%s repeats in the render: %s.", clock(is.From), from, words)
			}
			f.Fix = &models.QAFix{Summary: "If the cut doesn't repeat it, the render went wrong: render again and check again. If the footage itself repeats the word, move where the clip starts or ends."}
		case textalign.Missing:
			f.Kind = "speech_missing"
			switch {
			case is.Phrase:
				f.Severity = models.QAError
				f.Message = fmt.Sprintf("Around %s a whole phrase%s is missing from the render: %s.", clock(is.From), from, words)
			case len(is.Words) == 1:
				f.Message = fmt.Sprintf("Around %s the word %s%s can't be heard in the render.", clock(is.From), words, from)
			default:
				f.Message = fmt.Sprintf("Around %s the words %s%s can't be heard in the render.", clock(is.From), words, from)
			}
			f.Fix = &models.QAFix{Summary: "Check that the clip or narration line plays there and isn't muted or turned down; if the cut looks right, render again."}
		case textalign.Jump:
			delta := is.To - is.From
			f.To = math.Max(is.From, is.From+delta) + 0.1
			if delta > 0 {
				f.Kind = "speech_repeated"
				f.Message = fmt.Sprintf("Around %s a moment of sound%s plays twice or stutters in the render, so everything after it is %s late (between %s).", clock(is.From), from, seconds(delta), words)
			} else {
				f.Kind = "speech_missing"
				f.Message = fmt.Sprintf("Around %s a moment of speech%s is skipped in the render, so everything after it is %s early (between %s).", clock(is.From), from, seconds(delta), words)
			}
			if math.Abs(delta) >= 1 {
				f.Severity = models.QAError
			}
			f.Fix = &models.QAFix{Summary: "The cut doesn't do this: render again and check again. If it keeps happening, the footage file may be damaged at that spot."}
		case textalign.Unexpected:
			f.Kind = "speech_unexpected"
			f.Message = fmt.Sprintf("Around %s the render has speech the cut doesn't explain: %s.", clock(is.From), words)
			f.Fix = &models.QAFix{Summary: "Look for a clip whose sound should be off, a track playing where it shouldn't, or a clip whose start or end takes in extra words."}
		default:
			continue
		}
		r.add(f)
	}
	r.check(name, models.QAObserved, fmt.Sprintf("render transcribed locally (%s) and aligned to the planned words (±%.1fs); fillers and single transcription slips ignored; a repeated or missing phrase (≥%d words or a whole line) is an error", r.in.Transcriber.Name(), ContinuitySkewSecs, opts.PhraseWords),
		"speech_repeated", "speech_missing", "speech_unexpected")
}

func inSpans(from, to float64, spans []editspec.SpeechSpan) bool {
	mid := (from + to) / 2
	for _, s := range spans {
		if mid >= s.From-0.2 && mid <= s.To+0.2 {
			return true
		}
	}
	return false
}

// Transcriber is what speech continuity needs from a transcription provider.
type Transcriber = transcribe.Provider
