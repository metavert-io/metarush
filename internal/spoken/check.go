package spoken

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/transcribe"
	"github.com/metavert-io/metarush/internal/transcripts"
	"github.com/metavert-io/metarush/internal/voice"
)

// The checks of new words (D310): the recognizer on this computer must hear
// exactly the words asked for, and the voice must sound like the person
// (TitaNet, the fingerprints speaker detection uses).

// Hear writes down a 16 kHz mono WAV with the recognizer on this computer.
// Tests replace it.
var Hear = func(ctx context.Context, a *app.App, wav, scratch, language string) ([]models.Word, error) {
	p := transcribe.Select(a.Cfg.Transcription, a.Tools)
	if p == nil {
		return nil, errNoRecognizer
	}
	tr, err := p.Transcribe(ctx, wav, scratch, language)
	if err != nil {
		return nil, err
	}
	var out []models.Word
	for _, s := range tr.Segments {
		if len(s.Words) > 0 {
			out = append(out, s.Words...)
			continue
		}
		for _, w := range strings.Fields(s.Text) { // a recognizer without word times: the words, spread over the line
			out = append(out, models.Word{Text: w, From: s.From, To: s.To})
		}
	}
	return out, nil
}

// Embed fingerprints 16 kHz mono speech. Tests replace it.
var Embed = func(ctx context.Context, a *app.App, pcm []float32) ([]float32, error) {
	e := speakers.SharedEngine(a)
	if e == nil {
		return nil, errNoFingerprints
	}
	ex := e.Extractor()
	if ex == nil {
		return nil, errNoFingerprints
	}
	return ex.Embed(pcm)
}

var (
	errNoRecognizer   = errors.New("It needs the transcriber on this computer (the setup check sets it up).")
	errNoFingerprints = errors.New("It needs voice fingerprints on this computer (speaker detection; the setup check sets it up).")
)

func canHear(a *app.App) error {
	if transcribe.Select(a.Cfg.Transcription, a.Tools) == nil {
		return errNoRecognizer
	}
	return nil
}

func canFingerprint(a *app.App) error {
	e := speakers.SharedEngine(a)
	if e == nil || e.FingerprintError() != "" {
		return errNoFingerprints
	}
	return nil
}

// voiceNeeded is how much new speech must sound like the person, by how
// much of it there is (docs/speakers.md: the same person scores 0.55 or more
// in 85% of 2-second stretches and 97% of 3-second ones, other people stay
// below about 0.34). Under a second it can't be told.
func voiceNeeded(seconds float64) float64 {
	switch {
	case seconds >= 3:
		return 0.50
	case seconds >= 2:
		return 0.45
	case seconds >= 1:
		return 0.40
	}
	return math.Inf(1)
}

// WordsWindow is how much of the speech made around the new words their own
// voice check hears: the new words, and the words right by them up to this
// much (a lone word is too short to tell).
const WordsWindow = 1.5

// wordsWindow is the stretch of the speech made (secs long) whose voice is
// checked for the new words [from, to): centred on them, at least
// WordsWindow long, inside the speech.
func wordsWindow(from, to, secs float64) (float64, float64) {
	n := math.Max(WordsWindow, to-from)
	if n >= secs {
		return 0, secs
	}
	w0 := math.Min(math.Max(0, (from+to)/2-n/2), secs-n)
	return w0, w0 + n
}

// voiceCheck fingerprints the speech made (the whole of it: the speaker's
// words around the new ones make it long enough to tell) and compares it with
// the person's voice: their profile when MetaRush has one, else the voice in
// the recording.
func voiceCheck(ctx context.Context, a *app.App, pcm []float32, personID string, own []float32) (score float64, err error) {
	vec, err := Embed(ctx, a, pcm)
	if err != nil {
		return 0, err
	}
	c := voice.Cluster{Centroid: vec}
	if profs, err := speakers.LoadProfiles(ctx, a); err == nil {
		for _, p := range profs {
			if p.ID == personID {
				return voice.Score(c, p).Score, nil
			}
		}
	}
	if len(own) > 0 {
		return math.Round(voice.Cosine(vec, own)*1000) / 1000, nil
	}
	return 0, errors.New("MetaRush has nothing of this person's voice to compare with")
}

// sameWords reports whether the words heard are the words asked for: the
// same words in order, ignoring capitals and punctuation; a number may be
// written in figures or in words.
func sameWords(want, heard string) bool {
	w, h := numberWords(tokens(want)), numberWords(tokens(heard))
	if len(w) != len(h) || len(w) == 0 {
		return false
	}
	for i := range w {
		if w[i] != h[i] {
			return false
		}
	}
	return true
}

func tokens(s string) []string {
	var out []string
	for _, t := range strings.Fields(strings.ReplaceAll(s, "-", " ")) {
		if n := transcripts.Norm(t); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// numberWords writes numbers in words ("15" → "fifteen", "2026" → "two
// thousand twenty six"), so figures and words compare equal.
func numberWords(ts []string) []string {
	var out []string
	for _, t := range ts {
		if n, err := strconv.Atoi(t); err == nil && n >= 0 && n < 1000000 {
			out = append(out, spellNumber(n)...)
			continue
		}
		if t == "and" && len(out) > 0 && isNumberWord(out[len(out)-1]) {
			continue // "one hundred and five"
		}
		out = append(out, t)
	}
	return out
}

var (
	ones = []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve",
		"thirteen", "fourteen", "fifteen", "sixteen", "seventeen", "eighteen", "nineteen"}
	tens = []string{"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety"}
)

func isNumberWord(w string) bool {
	if w == "hundred" || w == "thousand" {
		return true
	}
	for _, x := range ones {
		if x == w {
			return true
		}
	}
	for _, x := range tens {
		if x != "" && x == w {
			return true
		}
	}
	return false
}

func spellNumber(n int) []string {
	switch {
	case n < 20:
		return []string{ones[n]}
	case n < 100:
		if n%10 == 0 {
			return []string{tens[n/10]}
		}
		return []string{tens[n/10], ones[n%10]}
	case n < 1000:
		out := []string{ones[n/100], "hundred"}
		if n%100 != 0 {
			out = append(out, spellNumber(n%100)...)
		}
		return out
	}
	out := append(spellNumber(n/1000), "thousand")
	if n%1000 != 0 {
		out = append(out, spellNumber(n%1000)...)
	}
	return out
}

// findWords finds the new words in what the recognizer heard of the speech
// made: the context asked for is before + new + after; the heard words are
// lined up with it, and the new words' first and last heard words returned
// (indexes into heard), or ok=false when they weren't all heard.
func findNew(heard []models.Word, before, words, after []string) (i, j int, ok bool) {
	want := append(append(append([]string{}, before...), words...), after...)
	h := make([]string, len(heard))
	for k, w := range heard {
		h[k] = transcripts.Norm(w.Text)
	}
	wn := make([]string, len(want))
	for k, w := range want {
		wn[k] = transcripts.Norm(w)
	}
	// Longest common subsequence, then the heard words matched to the new ones.
	n, m := len(wn), len(h)
	dp := make([][]int, n+1)
	for x := range dp {
		dp[x] = make([]int, m+1)
	}
	for x := n - 1; x >= 0; x-- {
		for y := m - 1; y >= 0; y-- {
			switch {
			case wn[x] != "" && wn[x] == h[y]:
				dp[x][y] = dp[x+1][y+1] + 1
			case dp[x+1][y] >= dp[x][y+1]:
				dp[x][y] = dp[x+1][y]
			default:
				dp[x][y] = dp[x][y+1]
			}
		}
	}
	match := make([]int, n)
	for x := range match {
		match[x] = -1
	}
	for x, y := 0, 0; x < n && y < m; {
		switch {
		case wn[x] != "" && wn[x] == h[y]:
			match[x] = y
			x, y = x+1, y+1
		case dp[x+1][y] >= dp[x][y+1]:
			x++
		default:
			y++
		}
	}
	lo, hi := len(before), len(before)+len(words)-1
	if match[lo] < 0 || match[hi] < 0 {
		// The ends of the new words weren't matched one to one (a number heard
		// in figures, a word split in two): find them between their neighbours.
		left, right := -1, m
		for x := lo - 1; x >= 0; x-- {
			if match[x] >= 0 {
				left = match[x]
				break
			}
		}
		for x := hi + 1; x < n; x++ {
			if match[x] >= 0 {
				right = match[x]
				break
			}
		}
		if right-left-1 <= 0 {
			return 0, 0, false
		}
		var got []string
		for y := left + 1; y < right; y++ {
			got = append(got, heard[y].Text)
		}
		if !sameWords(strings.Join(words, " "), strings.Join(got, " ")) {
			return 0, 0, false
		}
		return left + 1, right - 1, true
	}
	for x := lo; x <= hi; x++ {
		if match[x] < 0 {
			return 0, 0, false
		}
	}
	return match[lo], match[hi], true
}
