package sound

import (
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/metavert-io/metarush/internal/voice"
)

// Proof it helped: for each person in a processed copy, the voice clarity
// score (DNSMOS P.835 OVRL, with its speech and background parts) and the
// background noise estimate before and after, the voice's likeness to itself
// (TitaNet fingerprints, when installed), and plain warnings when processing
// may have gone too far.

// Score is one measurement.
type Score struct {
	Clarity *Clarity `json:"clarity,omitempty" bson:"clarity,omitempty"`
	Noise   *Noise   `json:"noise,omitempty" bson:"noise,omitempty"`
}

// PersonScore compares one person's voice before and after.
type PersonScore struct {
	Speaker  string  `json:"speaker" bson:"speaker" jsonschema:"description=The person (empty: the whole recording or the narration)."`
	Seconds  float64 `json:"seconds" bson:"seconds" jsonschema:"description=How much of their speech was measured."`
	Mode     string  `json:"mode" bson:"mode"`
	Strength float64 `json:"strength" bson:"strength"`
	Before   Score   `json:"before" bson:"before"`
	After    Score   `json:"after" bson:"after"`
	// Likeness is how much the processed voice still sounds like the person
	// (voice fingerprints, 0–1), when measured.
	Likeness *float64 `json:"likeness,omitempty" bson:"likeness,omitempty"`
	// Studio's word check: moments where the rebuilt voice said a word
	// differently play the cleaned voice instead (every word is kept).
	Kept      int      `json:"kept,omitempty" bson:"kept,omitempty" jsonschema:"description=Studio: moments where a word would have changed and the cleaned voice plays."`
	KeptWords []string `json:"keptWords,omitempty" bson:"keptWords,omitempty" jsonschema:"description=Studio: the words it would have changed."`
	SafeOnly  bool     `json:"safeOnly,omitempty" bson:"safeOnly,omitempty" jsonschema:"description=Studio without a speech recognizer here: the cleaned voice throughout."`
	Warnings  []string `json:"warnings,omitempty" bson:"warnings,omitempty"`
	Summary   string   `json:"summary" bson:"summary" jsonschema:"description=What changed\\, in plain words."`
}

// guarded adds studio's word checks to a score.
func (ps *PersonScore) guarded(gs []*Guard) {
	seen := map[string]bool{}
	for _, w := range ps.KeptWords {
		seen[w] = true
	}
	for _, g := range gs {
		if !g.Checked {
			ps.SafeOnly = true
		}
		ps.Kept += g.Replaced
		for _, w := range g.Words {
			if !seen[w] && len(ps.KeptWords) < 8 {
				seen[w] = true
				ps.KeptWords = append(ps.KeptWords, w)
			}
		}
	}
	ps.Summary = summarize(*ps)
}

// Quality measures copies. Its parts are optional: without the score's model
// or the fingerprint model the comparison says so instead.
type Quality struct {
	Scorer *Scorer
	// Embed is the voice fingerprint (16 kHz mono); nil when not installed.
	Embed func([]float32) ([]float32, error)
}

var extractorOnce struct {
	sync.Mutex
	ex  map[string]*voice.Extractor
	err map[string]error
}

// FingerprintFunc opens the TitaNet extractor diarization uses (lib: the
// sherpa-onnx C library, model: its embedding model), once per process.
func FingerprintFunc(lib, model string) func([]float32) ([]float32, error) {
	if lib == "" || model == "" {
		return nil
	}
	extractorOnce.Lock()
	defer extractorOnce.Unlock()
	if extractorOnce.ex == nil {
		extractorOnce.ex, extractorOnce.err = map[string]*voice.Extractor{}, map[string]error{}
	}
	k := lib + "\x00" + model
	ex, ok := extractorOnce.ex[k]
	if !ok {
		if _, failed := extractorOnce.err[k]; failed {
			return nil
		}
		var err error
		ex, err = voice.Open(lib, model, 2)
		if err != nil {
			extractorOnce.err[k] = err
			return nil
		}
		extractorOnce.ex[k] = ex
	}
	return ex.Embed
}

// measure scores 16 kHz mono speech.
func (q *Quality) measure(x16 []float32) Score {
	var s Score
	if q != nil && q.Scorer != nil && len(x16) >= DNSMOSRate/2 {
		if c, err := q.Scorer.Score(x16); err == nil {
			s.Clarity = &c
		}
	}
	if n, ok := EstimateNoise(x16, DNSMOSRate); ok {
		s.Noise = &n
	}
	return s
}

// ComparePerson scores one person's voice (48 kHz mono, before and after,
// both starting at start seconds): the clarity score over their speech, the
// background over their whole stretches (speech and the pauses around it,
// where the background is heard on its own; nil: the speech).
func (q *Quality) ComparePerson(speaker string, dry, out []float32, start float64, speech, whole [][2]float64, mode string, strength, coherence float64) PersonScore {
	d16 := downsample3(gather(dry, WetRate, start, speech))
	o16 := downsample3(gather(out, WetRate, start, speech))
	ps := PersonScore{Speaker: speaker, Mode: mode, Strength: strength, Seconds: math.Round(float64(len(d16))/DNSMOSRate*10) / 10}
	ps.Before, ps.After = q.measure(d16), q.measure(o16)
	if len(whole) > 0 {
		if n, ok := EstimateNoise(downsample3(gather(dry, WetRate, start, whole)), DNSMOSRate); ok {
			ps.Before.Noise = &n
		}
		if n, ok := EstimateNoise(downsample3(gather(out, WetRate, start, whole)), DNSMOSRate); ok {
			ps.After.Noise = &n
		}
	}
	if q != nil && q.Embed != nil && len(d16) >= DNSMOSRate {
		limit := min(len(d16), 30*DNSMOSRate)
		a, errA := q.Embed(d16[:limit])
		b, errB := q.Embed(o16[:min(len(o16), limit)])
		if errA == nil && errB == nil {
			l := math.Round(voice.Cosine(a, b)*100) / 100
			ps.Likeness = &l
		}
	}
	ps.Warnings = warnings(ps, coherence)
	ps.Summary = summarize(ps)
	return ps
}

// warnings are the plain cautions about a comparison.
func warnings(ps PersonScore, coherence float64) []string {
	var w []string
	who := ps.Speaker
	if who == "" {
		who = "this voice"
	}
	b, a := ps.Before.Clarity, ps.After.Clarity
	if b != nil && a != nil {
		switch {
		case a.Overall < b.Overall-0.05:
			w = append(w, fmt.Sprintf("Processing makes %s sound a little worse (voice clarity score %.1f → %.1f): try a lower strength, or leave it as recorded.", who, b.Overall, a.Overall))
		case a.Speech < b.Speech-0.15:
			w = append(w, fmt.Sprintf("The voice itself may sound processed now (thinner, a little robotic): try a lower strength for %s.", who))
		}
	}
	if ps.Mode == "studio" && ps.Strength > 0.1 && ps.Strength < 0.9 {
		w = append(w, "Studio rebuilds the voice, so blending it with the original can sound hollow; it works best at full strength.")
	} else if ps.Mode != "studio" && coherence > 0 && coherence < 0.6 && ps.Strength > 0.1 && ps.Strength < 0.9 {
		w = append(w, "The processed voice does not line up exactly with the original, so blending the two may sound hollow; try full strength or turn it off.")
	}
	if ps.Likeness != nil && *ps.Likeness < 0.75 {
		w = append(w, fmt.Sprintf("It may not sound quite like %s any more (voice likeness %d%%): listen before you keep it.", who, int(math.Round(*ps.Likeness*100))))
	}
	if ps.Seconds > 0 && ps.Seconds < 3 {
		w = append(w, fmt.Sprintf("There are only %.1f seconds of speech to measure, so the scores are a rough guide.", ps.Seconds))
	}
	return w
}

// summarize says what changed, plainly ("Voice clarity score 2.4 → 3.2 out of
// 5 (clearer); less background noise: it is now 45 dB below the voice (was
// 18 dB)."). The background is judged by the clarity score's background part
// when there is one (it hears room echo and hum too), else by the level.
func summarize(ps PersonScore) string {
	var parts []string
	b, a := ps.Before.Clarity, ps.After.Clarity
	if b != nil && a != nil {
		word := "about the same"
		switch d := a.Overall - b.Overall; {
		case d >= 0.5:
			word = "much clearer"
		case d >= 0.15:
			word = "clearer"
		case d <= -0.15:
			word = "less clear"
		}
		parts = append(parts, fmt.Sprintf("voice clarity score %.1f → %.1f out of 5 (%s)", b.Overall, a.Overall, word))
	}
	level := ""
	nb, na := ps.Before.Noise, ps.After.Noise
	if nb != nil && na != nil {
		level = fmt.Sprintf(": it is now %.0f dB below the voice (was %.0f dB)", na.SNR, nb.SNR)
	}
	bg := 0 // +1 less background, -1 more, 0 about the same
	switch {
	case b != nil && a != nil && a.Background-b.Background >= 0.3:
		bg = 1
	case b != nil && a != nil && a.Background-b.Background <= -0.3:
		bg = -1
	case nb != nil && na != nil && na.SNR-nb.SNR >= 3:
		bg = 1
	case nb != nil && na != nil && na.SNR-nb.SNR <= -3:
		bg = -1
	}
	switch {
	case bg > 0:
		parts = append(parts, "less background noise"+level)
	case bg < 0:
		parts = append(parts, "more background noise than before"+level)
	case nb != nil && na != nil:
		parts = append(parts, fmt.Sprintf("about as much background noise as before (%.0f dB below the voice)", na.SNR))
	}
	if len(parts) == 0 {
		parts = append(parts, "not measured (the voice clarity score is not set up on this computer)")
	}
	s := strings.Join(parts, "; ")
	s = strings.ToUpper(s[:1]) + s[1:] + "."
	switch {
	case ps.SafeOnly:
		s += " Studio played the cleaned voice throughout: there is no speech recognizer on this computer to check that its rebuilt voice keeps every word."
	case ps.Kept == 1:
		s += " Every word is kept: at 1 moment studio would have changed a word, so the cleaned voice plays there."
	case ps.Kept > 1:
		s += fmt.Sprintf(" Every word is kept: at %d moments studio would have changed a word, so the cleaned voice plays there.", ps.Kept)
	}
	return s
}
