package interchange

import (
	"encoding/json"
	"math"
	"sort"

	"github.com/metavert-io/metarush/internal/editspec"
)

func jsonUnmarshal(b []byte, v any) error {
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// Silent is the level written for silence (editing apps' minimum).
const Silent = -96.0

// DB converts a linear gain to decibels (Silent at or below zero).
func DB(g float64) float64 {
	if g <= 1e-5 {
		return Silent
	}
	return 20 * math.Log10(g)
}

// Gain converts decibels to a linear gain.
func Gain(db float64) float64 {
	if db <= Silent {
		return 0
	}
	return math.Pow(10, db/20)
}

// shapeLevels reads a sound item's gain envelope (linear, item-relative
// frames; MetaRush's mixer output with volume, blends, fades, leveling,
// ducking and master gain) into what editing apps keep: a fade at each edge
// that starts or ends in silence, one level, and level keys where the level
// changes inside the item (ducking under narration). fadeIn and fadeOut are
// the fades in frames when the plan states them (0: none), or -1 to find them
// in the envelope (a clip's sound: blends, edge fades, transitions).
func shapeLevels(it *Item, env []editspec.GainPoint, fallback float64, fadeIn, fadeOut int) {
	n := it.Duration
	if len(env) == 0 || n <= 0 {
		it.LevelDB = round1(DB(fallback))
		return
	}
	vals := make([]float64, n)
	peak := 0.0
	for f := range vals {
		vals[f] = editspec.GainAt(env, float64(f))
		peak = math.Max(peak, vals[f])
	}
	if peak <= 1e-5 {
		it.LevelDB = Silent
		return
	}
	// The usual level (the middle half's median), then a ramp from (near)
	// silence at the head and to silence at the tail: where the sound is more
	// than half a decibel below that level.
	mid := append([]float64(nil), vals[n/4:max(n/4+1, 3*n/4)]...)
	sort.Float64s(mid)
	plateau := mid[len(mid)/2]
	if plateau <= 1e-5 {
		plateau = peak
	}
	head, tail := 0, n-1
	if fadeIn < 0 && vals[0] < 0.5*plateau {
		for head < n/2 && vals[head] < 0.944*plateau {
			head++
		}
	} else if fadeIn > 0 {
		head = min(fadeIn, n/2)
	}
	if fadeOut < 0 && vals[n-1] < 0.5*plateau {
		for tail > max(head, n/2) && vals[tail] < 0.944*plateau {
			tail--
		}
	} else if fadeOut > 0 {
		tail = max(head, n-1-min(fadeOut, n/2))
	}
	it.FadeIn = head
	if tail < n-1 {
		it.FadeOut = n - 1 - tail
	}
	lo, hi := head, tail+1 // the level between the fades
	if lo >= hi {
		lo, hi = 0, n
	}
	dbs := make([]float64, 0, hi-lo)
	for f := lo; f < hi; f++ {
		dbs = append(dbs, DB(vals[f]))
	}
	sorted := append([]float64(nil), dbs...)
	sort.Float64s(sorted)
	base := sorted[len(sorted)/2]
	it.LevelDB = round1(base)
	spread := 0.0
	for _, d := range dbs {
		spread = math.Max(spread, math.Abs(d-base))
	}
	if spread <= 0.5 {
		return // one level is close enough everywhere between the fades
	}
	// Keys: the envelope's own break points between the fades, plus both ends.
	keys := []LevelKey{{Frame: lo, DB: round1(DB(vals[lo]))}}
	for _, p := range env {
		if p.F > lo && p.F < hi-1 {
			keys = append(keys, LevelKey{Frame: p.F, DB: round1(DB(vals[p.F]))})
		}
	}
	if hi-1 > lo {
		keys = append(keys, LevelKey{Frame: hi - 1, DB: round1(DB(vals[hi-1]))})
	}
	it.Keys = dedupeKeys(keys)
}

// dedupeKeys drops keys that a straight line between their neighbours already
// gives (within 0.1 dB), so editing apps get few keyframes.
func dedupeKeys(k []LevelKey) []LevelKey {
	if len(k) <= 2 {
		return k
	}
	out := []LevelKey{k[0]}
	for i := 1; i < len(k)-1; i++ {
		a, b, c := out[len(out)-1], k[i], k[i+1]
		if c.Frame == a.Frame {
			continue
		}
		lin := a.DB + (c.DB-a.DB)*float64(b.Frame-a.Frame)/float64(c.Frame-a.Frame)
		if math.Abs(lin-b.DB) > 0.1 {
			out = append(out, b)
		}
	}
	return append(out, k[len(k)-1])
}

// LevelAt is an item's level (dB) at an item frame: its keys when it has them, else its level.
func (it *Item) LevelAt(f int) float64 {
	if len(it.Keys) == 0 {
		return it.LevelDB
	}
	if f <= it.Keys[0].Frame {
		return it.Keys[0].DB
	}
	for i := 1; i < len(it.Keys); i++ {
		if f <= it.Keys[i].Frame {
			a, b := it.Keys[i-1], it.Keys[i]
			if b.Frame == a.Frame {
				return b.DB
			}
			return a.DB + (b.DB-a.DB)*float64(f-a.Frame)/float64(b.Frame-a.Frame)
		}
	}
	return it.Keys[len(it.Keys)-1].DB
}
