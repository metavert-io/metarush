package editspec

import (
	"math"
	"sort"
)

// SpeechWord is a word the cut is expected to play, in timeline seconds.
type SpeechWord struct {
	Text   string
	From   float64
	To     float64
	Kind   string // dialogue | narration
	ClipID string
	BeatID string
	// Speaker is who says it (as captions name them; Narrator for the narration).
	Speaker string
}

// SpeechSpan is a timeline interval in seconds.
type SpeechSpan struct{ From, To float64 }

// ExpectedSpeech lists every word the cut should play — the clips' own
// transcript words inside each clip's in/out (unless their sound is muted) and
// the narration's words from its takes — plus spans where speech plays but no
// word timing exists (segment-only transcripts) and the cut points where
// words are expected to be clipped. QA compares it with what a render says.
func ExpectedSpeech(s *Spec, facts Facts) (words []SpeechWord, unknown []SpeechSpan, edges []float64) {
	s = Clone(s)
	Normalize(s)
	facts = wrapFacts(s, facts)
	clips, total := Timeline(s)
	fps := float64(s.Output.FPS)
	mp := mixFor(s)
	sourceMuted := mp.busMuted[BusSource]
	sp := resolveSound(s, clips, facts)
	for i, pc := range clips {
		// Where words may be cut: the edges of each clip's sound (its picture's
		// edges unless a split edit or blend moves them).
		w := sp.win[i]
		edges = append(edges, float64(w.from)/fps, float64(w.to)/fps)
		c := s.Clips[i]
		if c.Muted || sourceMuted || facts == nil || (c.Volume != nil && *c.Volume < 0.05) {
			continue
		}
		if a, ok := facts.Asset(c.AssetID); !ok || !a.HasAudio {
			continue
		}
		in, out := w.heardIn, w.heardOut
		for _, seg := range heardWords(s, i, c.AssetID, facts) {
			if seg.To <= in || seg.From >= out {
				continue
			}
			if len(seg.Words) == 0 {
				from, to := math.Max(seg.From, in), math.Min(seg.To, out)
				unknown = append(unknown, SpeechSpan{toTimeline(pc, fps, from), toTimeline(pc, fps, to)})
				continue
			}
			for _, wd := range seg.Words {
				mid := (wd.From + wd.To) / 2
				if mid < in || mid >= out {
					continue
				}
				words = append(words, SpeechWord{Text: wd.Text, From: toTimeline(pc, fps, math.Max(wd.From, in)), To: toTimeline(pc, fps, math.Min(wd.To, out)),
					Kind: "dialogue", ClipID: c.ID, Speaker: seg.Speaker})
			}
		}
	}
	if s.Voiceover != nil && facts != nil && !mp.busMuted[BusVoiceover] {
		beats := ResolveBeats(s, clips, total, facts)
		for _, rb := range beats {
			b := s.Voiceover.Beats[rb.Index]
			if rb.Problem != "" || b.Take == nil {
				continue
			}
			start := float64(rb.Start) / fps
			edges = append(edges, start, float64(rb.End())/fps)
			t := b.Take
			rate := rb.Rate
			if rate <= 0 {
				rate = 1
			}
			found := false
			for _, seg := range facts.Transcript(t.AssetID) {
				for _, w := range seg.Words {
					mid := (w.From + w.To) / 2
					if mid < t.In || mid >= t.Out {
						continue
					}
					found = true
					words = append(words, SpeechWord{Text: w.Text, From: start + (math.Max(w.From, t.In)-t.In)/rate, To: start + (math.Min(w.To, t.Out)-t.In)/rate,
						Kind: "narration", BeatID: b.ID, Speaker: NarrationSpeaker})
				}
			}
			if !found {
				unknown = append(unknown, SpeechSpan{start, float64(rb.End()) / fps})
			}
		}
	}
	end := float64(total) / fps
	kept := words[:0]
	for _, w := range words {
		if w.From < end {
			kept = append(kept, w)
		}
	}
	words = kept
	sort.SliceStable(words, func(i, j int) bool { return words[i].From < words[j].From })
	sort.Float64s(edges)
	return words, unknown, edges
}

func toTimeline(pc PlanClip, fps, t float64) float64 {
	return float64(mapSourceToTimeline(pc, fps, t)) / fps
}
