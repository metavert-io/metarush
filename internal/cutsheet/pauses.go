package cutsheet

import (
	"math"
	"sort"
	"strconv"

	"github.com/metavert-io/metarush/internal/editspec"
)

// Long pauses (D236): gaps between words shortened, in any footage. A pause
// is the quiet between two words heard in one clip of the cut (word timings,
// so it works for every recording, calls included); one longer than a length
// the person chooses is shortened to another, the cut landing in the middle of
// the quiet so the words on both sides keep their air. Pauses at the cuts
// between clips are the cut's own (trim a clip to change them), and words
// whose times were estimated from a sentence never count. Each suggestion is
// a WordRemoval of kind "pause" with no words, so it is heard first, kept or
// skipped and saved exactly like the cleanup suggestions (RemovalOps).

// RemovePause is a stretch of quiet shortened between two words.
const RemovePause = "pause"

// Default pause lengths: pauses longer than PauseLongerThan seconds are
// shortened to PauseTo seconds (both as they play, on the video's timeline).
const (
	PauseLongerThan = 1.0
	PauseTo         = 0.5
	// pauseMinCut is the least worth cutting: under it the join isn't worth a cut.
	pauseMinCut = 0.1
)

// Pauses finds pauses between words of the cut's clips longer than longerThan
// seconds and the stretch that shortens each to keep seconds (timeline
// seconds; a clip's speed is taken into account), in the cut's order.
func Pauses(s *editspec.Spec, tx func(assetID string) []editspec.TranscriptSegment, longerThan, keep float64) []WordRemoval {
	if longerThan <= 0 {
		longerThan = PauseLongerThan
	}
	if keep < 0 || keep >= longerThan {
		keep = math.Min(PauseTo, longerThan/2)
	}
	wc := newWordCutter(s, tx)
	var out []WordRemoval
	for i, c := range wc.spec.Clips {
		cw := wc.words(i)
		if len(cw.heard) < 2 {
			continue
		}
		speed := speedOf(c)
		// The heard words in time order; overlapping speech (two people at
		// once) counts until the later of its ends.
		order := append([]int(nil), cw.heard...)
		sort.SliceStable(order, func(a, b int) bool { return cw.all[order[a]].from < cw.all[order[b]].from })
		pos := make(map[int]int, len(cw.heard)) // index into all → index into heard
		for k, h := range cw.heard {
			pos[h] = k
		}
		end, endIdx := cw.all[order[0]].to, order[0]
		for k := 1; k < len(order); k++ {
			w := cw.all[order[k]]
			prev := cw.all[endIdx]
			gap := w.from - end
			if !prev.approx && !w.approx && gap/speed > longerThan {
				air := keep * speed // the quiet kept, in source seconds
				from := math.Floor(float64((end+air/2)*1000)+1e-6) / 1000
				to := math.Ceil(float64((w.from-air/2)*1000)-1e-6) / 1000
				from, to = math.Max(c.In, from), math.Min(c.Out, to)
				if (to-from)/speed >= pauseMinCut {
					out = append(out, wc.pause(i, pos[endIdx], pos[order[k]], end, w.from, from, to))
				}
			}
			if w.to > end {
				end, endIdx = w.to, order[k]
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

// pause is the suggestion for the quiet between heard words h0 and h1 of
// clip i (indices into its heard words): quietFrom–quietTo is the whole pause,
// from–to the stretch taken out of it.
func (wc *wordCutter) pause(i, h0, h1 int, quietFrom, quietTo, from, to float64) WordRemoval {
	c := wc.spec.Clips[i]
	cw := wc.words(i)
	// The words heard up to the pause (h0 the last of them) and from it on.
	before, after := cw.around(h0+1, h1-1)
	pc := wc.placed[i]
	speed := speedOf(c)
	at := (float64(pc.From) + float64((from-pc.SourceIn)/pc.PlaybackRate*wc.fps)) / wc.fps
	long := math.Round((quietTo-quietFrom)/speed*10) / 10
	return WordRemoval{ClipID: c.ID, Clip: i + 1, Kind: RemovePause, Reason: "a pause of " + strconv.FormatFloat(long, 'f', -1, 64) + " s",
		Before: before, After: after, Speaker: cw.all[cw.heard[h1]].speaker,
		WordsFrom: quietFrom, WordsTo: quietTo, From: from, To: to, At: math.Round(math.Max(0, at)*1000) / 1000,
		Seconds: math.Round((to-from)/speed*1000) / 1000}
}
