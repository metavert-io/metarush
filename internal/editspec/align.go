package editspec

import (
	"math"

	"github.com/metavert-io/metarush/internal/textalign"
)

// BeatAlignment is where one script line was found in a recording.
type BeatAlignment struct {
	BeatID  string   `json:"beatId"`
	Line    int      `json:"line"`
	Found   bool     `json:"found"`
	From    float64  `json:"from"` // source seconds in the recording (with a little air)
	To      float64  `json:"to"`
	Matched int      `json:"matched"` // script words heard
	Total   int      `json:"total"`
	Missing []string `json:"missing,omitempty"` // script words not heard
	Extra   []string `json:"extra,omitempty"`   // spoken words that are not in the script
}

// ScriptAlignment is a whole recording aligned to the script.
type ScriptAlignment struct {
	Beats    []BeatAlignment `json:"beats"`
	Coverage float64         `json:"coverage"` // matched / total script words
	WPM      float64         `json:"wpm"`      // measured speaking rate (words per minute of speech)
}

// Padding around a found line: a breath before, the tail of the last word after.
const (
	alignPre  = 0.12
	alignPost = 0.25
)

// lineSpan is where a line's words are in the transcript: the first and last
// observed word (indices into the words; -1 when none was heard).
type lineSpan struct{ first, last int }

// matchLines aligns the beats' words with a word-timed transcript (text
// alignment that tolerates fillers and small transcription errors). The
// alignment is monotonic, so the lines found come out in time order.
func matchLines(beats []Beat, words []TranscriptWord) ([]BeatAlignment, []lineSpan) {
	var exp []textalign.Word
	for i, b := range beats {
		exp = append(exp, textalign.FromText(b.Text, i)...)
	}
	obs := make([]textalign.Word, len(words))
	for i, w := range words {
		obs[i] = textalign.Word{Text: w.Text, Norm: textalign.Normalize(w.Text), From: w.From, To: w.To}
	}
	steps := textalign.Align(exp, obs, 0)
	spans := make([]lineSpan, len(beats))
	for i := range spans {
		spans[i] = lineSpan{-1, -1}
	}
	out := make([]BeatAlignment, len(beats))
	for i, b := range beats {
		out[i] = BeatAlignment{BeatID: b.ID, Line: i + 1}
	}
	lastGroup := 0
	for _, st := range steps {
		if st.E >= 0 {
			g := exp[st.E].Group
			lastGroup = g
			ba := &out[g]
			if !textalign.IsFiller(exp[st.E].Norm) {
				ba.Total++
			}
			switch st.Kind {
			case textalign.Match:
				ba.Matched++
				fallthrough
			case textalign.Subst:
				if spans[g].first < 0 {
					spans[g].first = st.O
				}
				spans[g].last = st.O
				if st.Kind == textalign.Subst {
					ba.Missing = append(ba.Missing, exp[st.E].Text)
					ba.Extra = append(ba.Extra, obs[st.O].Text)
				}
			case textalign.Del:
				ba.Missing = append(ba.Missing, exp[st.E].Text)
			}
			continue
		}
		// An extra spoken word: attribute it to the line being spoken.
		if !textalign.IsFiller(obs[st.O].Norm) && spans[lastGroup].first >= 0 {
			out[lastGroup].Extra = append(out[lastGroup].Extra, obs[st.O].Text)
		}
	}
	for i := range out {
		if spans[i].first >= 0 && out[i].Matched > 0 {
			out[i].Found = true
		} else {
			spans[i] = lineSpan{-1, -1}
		}
	}
	return out, spans
}

// AlignScript finds each beat's words in a recording's word-timed transcript
// (text alignment that tolerates fillers and small transcription errors) and
// measures the narrator's speaking rate. duration bounds the segments.
func AlignScript(beats []Beat, words []TranscriptWord, duration float64) ScriptAlignment {
	al, spans := matchLines(beats, words)
	out := ScriptAlignment{Beats: al}
	matched, total, speech := 0, 0, 0.0
	for i := range out.Beats {
		ba := &out.Beats[i]
		matched += ba.Matched
		total += ba.Total
		sp := spans[i]
		if !ba.Found {
			continue
		}
		from, to := words[sp.first].From, words[sp.last].To
		speech += to - from
		lo, hi := 0.0, duration
		for j := i - 1; j >= 0; j-- { // halfway into the gap after the previous line
			if out.Beats[j].Found {
				lo = (words[spans[j].last].To + from) / 2
				break
			}
		}
		for j := i + 1; j < len(spans); j++ {
			if out.Beats[j].Found {
				hi = (to + words[spans[j].first].From) / 2
				break
			}
		}
		ba.From = round3(math.Max(lo, from-alignPre))
		ba.To = round3(math.Min(hi, to+alignPost))
		if duration > 0 {
			ba.To = math.Min(ba.To, duration)
		}
	}
	if total > 0 {
		out.Coverage = round3(float64(matched) / float64(total))
	}
	if speech > 0 {
		out.WPM = math.Round(float64(matched) / speech * 60)
	}
	return out
}
