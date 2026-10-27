package sound

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/textalign"
	"github.com/metavert-io/metarush/internal/transcribe"
)

// Guard is studio's word check on one processed voice: the local recognizer
// heard the cleaned voice and the rebuilt one, and wherever a word differed
// the cleaned voice plays (D90).
type Guard struct {
	Checked  bool     `json:"checked" bson:"checked" jsonschema:"description=The words were checked (false: no recognizer here\\, so the cleaned voice plays throughout)."`
	Replaced int      `json:"replaced" bson:"replaced" jsonschema:"description=Moments where a word differed and the cleaned voice plays."`
	Seconds  float64  `json:"seconds" bson:"seconds" jsonschema:"description=How long those moments are together."`
	Words    []string `json:"words,omitempty" bson:"words,omitempty" jsonschema:"description=The words it would have changed (“seedlings” → “sea legs”)."`
}

// guardPad is how much around a differing word the safe version plays, and
// guardFade the crossfade into and out of it.
const (
	guardPad  = 0.12
	guardFade = 0.025
)

// guard checks a rebuilt voice (w, lined up with base) against the cleaned
// voice it was made from and puts the cleaned voice wherever a word differs.
func (e *Engine) guard(ctx context.Context, w *Wet, base *Audio) error {
	dir, done, err := e.tempDir()
	if err != nil {
		return err
	}
	defer done()
	heard := func(a *Audio, name string) ([]textalign.Word, error) {
		wav := filepath.Join(dir, name+"-48k.wav")
		if err := WriteWAV(wav, a, true); err != nil {
			return nil, err
		}
		a16, err := e.Extract(ctx, wav, 0, 0, 16000, 1)
		if err != nil {
			return nil, err
		}
		wav16 := filepath.Join(dir, name+".wav")
		if err := WriteWAV(wav16, a16, false); err != nil {
			return nil, err
		}
		return e.Hear(ctx, wav16)
	}
	before, err := heard(base, "cleaned")
	if err != nil {
		return fmt.Errorf("could not check the studio voice's words: %w", err)
	}
	after, err := heard(w.Audio, "rebuilt")
	if err != nil {
		return fmt.Errorf("could not check the studio voice's words: %w", err)
	}
	spans, words := differingWords(before, after)
	g := &Guard{Checked: true, Replaced: len(spans), Words: words}
	x, s := w.Audio.Data, base.Data
	rate := float64(w.Audio.Rate)
	fade := int(guardFade * rate)
	for _, sp := range spans {
		a, b := max(0, int(sp[0]*rate)), min(len(x), len(s), int(math.Ceil(sp[1]*rate)))
		if b <= a {
			continue
		}
		g.Seconds += float64(b-a) / rate
		for i := max(0, a-fade); i < min(b+fade, len(x), len(s)); i++ {
			// e: how much of the cleaned voice plays (ramps at both edges).
			e := 1.0
			if i < a {
				e = 1 - float64(a-i)/float64(fade)
			} else if i >= b {
				e = 1 - float64(i-b+1)/float64(fade)
			}
			x[i] = float32(float64(x[i])*(1-e) + float64(s[i])*e)
		}
	}
	g.Seconds = math.Round(g.Seconds*100) / 100
	w.Guard = g
	return nil
}

// lineUp shifts x (a processed version of ref) so it lines up with ref: the
// speech envelopes (10 ms steps, ±0.3 s), then the waveforms (±10 ms) when
// they still look alike.
func lineUp(ref, x []float32, rate int) []float32 {
	hop := rate / 100
	lag, _ := bestLagF(envelope(ref, hop), envelope(x, hop), 30)
	lag *= hop
	seg := min(len(x), len(ref), rate*8)
	if seg > rate/2 {
		start := max(0, min(min(len(x), len(ref))-seg, findLoud(ref, hop)))
		fine, corr := bestLag(ref[start:start+seg], shift(x, lag)[start:start+seg], rate/100)
		if corr > 0.5 {
			lag += fine
		}
	}
	if lag == 0 {
		return x
	}
	return shift(x, lag)
}

// differingWords aligns the words heard before and after and returns the
// stretches (seconds) where they differ, merged, and the differences in words.
func differingWords(before, after []textalign.Word) ([][2]float64, []string) {
	steps := textalign.Align(before, after, 0.6)
	// Exact words: the alignment forgives transcription noise ("fruits" and
	// "fruit" align), but a rebuilt voice must say the same word.
	for i, st := range steps {
		if st.Kind == textalign.Match && before[st.E].Norm != after[st.O].Norm {
			steps[i].Kind = textalign.Subst
		}
	}
	var spans [][2]float64
	var words []string
	add := func(from, to float64) {
		spans = append(spans, [2]float64{math.Max(0, from-guardPad), to + guardPad})
	}
	for i := 0; i < len(steps); i++ {
		st := steps[i]
		switch st.Kind {
		case textalign.Match:
			continue
		case textalign.Ins:
			if textalign.IsFiller(after[st.O].Norm) {
				continue
			}
			add(after[st.O].From, after[st.O].To)
			words = append(words, fmt.Sprintf("“%s” added", after[st.O].Text))
		case textalign.Del:
			if textalign.IsFiller(before[st.E].Norm) {
				continue
			}
			add(before[st.E].From, before[st.E].To)
			words = append(words, fmt.Sprintf("“%s” lost", before[st.E].Text))
		case textalign.Subst:
			add(math.Min(before[st.E].From, after[st.O].From), math.Max(before[st.E].To, after[st.O].To))
			words = append(words, fmt.Sprintf("“%s” → “%s”", before[st.E].Text, after[st.O].Text))
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	var merged [][2]float64
	for _, sp := range spans {
		if n := len(merged); n > 0 && sp[0] <= merged[n-1][1] {
			merged[n-1][1] = math.Max(merged[n-1][1], sp[1])
			continue
		}
		merged = append(merged, sp)
	}
	if len(words) > 8 {
		words = append(words[:8], fmt.Sprintf("and %d more", len(words)-8))
	}
	return merged, words
}

// hearWith makes Engine.Hear from a transcript provider: timed words (a
// segment without word times spreads its words evenly over it).
func hearWith(p transcribe.Provider, scratch, language string) func(context.Context, string) ([]textalign.Word, error) {
	return func(ctx context.Context, wav16 string) ([]textalign.Word, error) {
		if err := os.MkdirAll(scratch, 0o755); err != nil {
			return nil, err
		}
		dir, err := os.MkdirTemp(scratch, "hear-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		tr, err := p.Transcribe(ctx, wav16, dir, language)
		if err != nil {
			return nil, err
		}
		var out []textalign.Word
		for _, sg := range tr.Segments {
			if len(sg.Words) > 0 {
				for _, w := range sg.Words {
					if n := textalign.Normalize(w.Text); n != "" {
						out = append(out, textalign.Word{Text: strings.TrimSpace(w.Text), Norm: n, From: w.From, To: w.To})
					}
				}
				continue
			}
			toks := textalign.Tokens(sg.Text)
			for i, t := range toks {
				d := (sg.To - sg.From) / float64(len(toks))
				out = append(out, textalign.Word{Text: t, Norm: textalign.Normalize(t), From: sg.From + float64(i)*d, To: sg.From + float64(i+1)*d})
			}
		}
		return out, nil
	}
}
