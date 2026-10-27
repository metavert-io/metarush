package transcribe

import "math"

// snapReach is how far a word edge moves onto a pause's edge: a word that
// ends up to this long before a pause runs to it (the sound between is its
// tail), and the first word after a pause that starts up to this long after
// it starts there.
const snapReach = 1.0

// snapToSilences puts the words' edges on the pauses ffmpeg found (D244).
// whisper.cpp's decoder starts the word after a pause up to a second early,
// inside the pause, and its DTW times make words shorter than they sound, so
// on its own neither shows a pause as long as it is. For every pause of at
// least minPause seconds, the words starting before its middle are before it
// and the others after it: the last word before it ends where the pause
// starts (if it ended inside the pause, after it, or up to snapReach before
// it), and the first word after it starts where the pause ends (if it started
// inside it or up to snapReach after it). Other words inside a pause are left
// as they are. Segment edges follow their words.
func snapToSilences(segs []Segment, sil []span, minPause float64) []Segment {
	type ref struct{ s, w int }
	var order []ref
	for i := range segs {
		for j := range segs[i].Words {
			order = append(order, ref{i, j})
		}
	}
	if len(order) == 0 {
		return segs
	}
	word := func(k int) *wordRef {
		r := order[k]
		return &wordRef{&segs[r.s].Words[r.w].From, &segs[r.s].Words[r.w].To}
	}
	for _, p := range sil {
		if p.to-p.from < minPause {
			continue
		}
		mid := (p.from + p.to) / 2
		// The first word starting at or after the pause's middle.
		k := 0
		for k < len(order) && *word(k).from < mid {
			k++
		}
		if k > 0 {
			b := word(k - 1)
			if *b.to > p.from-snapReach && *b.from < p.from {
				*b.to = p.from
			}
		}
		if k < len(order) {
			a := word(k)
			if *a.from < p.to+snapReach && *a.to > p.to {
				*a.from = p.to
			}
		}
	}
	for i := range segs {
		ws := segs[i].Words
		for j := range ws {
			ws[j].From, ws[j].To = round3(ws[j].From), round3(math.Max(ws[j].To, ws[j].From))
		}
		if len(ws) > 0 {
			segs[i].From, segs[i].To = ws[0].From, ws[len(ws)-1].To
		}
	}
	return segs
}

type wordRef struct{ from, to *float64 }
