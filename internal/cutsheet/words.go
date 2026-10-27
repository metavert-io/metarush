package cutsheet

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/textalign"
)

// Words in a cut: finding what someone says in the clips of a cut, the stretch
// of footage to remove so the cuts land in the pauses around it, and
// suggestions for cleaning up speech (ums, words said twice, false starts).
// Removal ranges keep the same air as quoted-word in and out points: up to
// padAfter after the last word kept before the cut and padBefore before the
// first word kept after it, so the join sounds like a natural pause.

// Kinds of word removals.
const (
	RemoveFiller     = "filler"      // um, uh, erm…
	RemoveRepeat     = "repeat"      // "because… because"
	RemoveFalseStart = "false-start" // "we were, we went"
	RemovePhrase     = "phrase"      // words someone asked for by quoting them
	RemoveSelection  = "selection"   // words selected in a transcript
)

// WordRemoval is a stretch of one clip's footage to remove: some words and the
// pauses around them.
type WordRemoval struct {
	ClipID    string  `json:"clipId"`
	Clip      int     `json:"clip" jsonschema:"description=The clip's number in the cut (1 = the first)."`
	Kind      string  `json:"kind" jsonschema:"enum=filler,enum=repeat,enum=false-start,enum=phrase,enum=selection,enum=pause"`
	Reason    string  `json:"reason" jsonschema:"description=Why\\, in plain words (a filler word\\, said twice\\, a pause of 1.8 s…)."`
	Words     string  `json:"words" jsonschema:"description=The words removed\\, as transcribed (empty for a pause: only quiet is taken out)."`
	Before    string  `json:"before" jsonschema:"description=A few words heard just before (context)."`
	After     string  `json:"after" jsonschema:"description=A few words heard just after."`
	Speaker   string  `json:"speaker,omitempty"`
	WordsFrom float64 `json:"wordsFrom" jsonschema:"description=Where the words start in the footage (source seconds); for a pause\\, where the quiet starts."`
	WordsTo   float64 `json:"wordsTo"`
	From      float64 `json:"from" jsonschema:"description=Where the removed stretch starts in the footage (in the pause before the words)."`
	To        float64 `json:"to" jsonschema:"description=Where it ends (in the pause after them)."`
	At        float64 `json:"at" jsonschema:"description=Where the words are heard in the cut (timeline seconds)."`
	Seconds   float64 `json:"seconds" jsonschema:"description=How much shorter the cut gets."`
}

// clipWords are the words a clip plays (by their middle, as captions count
// them) and the clip's footage words around them.
type clipWords struct {
	all   []wordRef
	heard []int // indices into all
}

func wordsOfClip(c editspec.Clip, segs []editspec.TranscriptSegment) clipWords {
	cw := clipWords{all: flattenWords(segs)}
	for i, w := range cw.all {
		mid := (w.from + w.to) / 2
		if mid >= c.In && mid < c.Out {
			cw.heard = append(cw.heard, i)
		}
	}
	return cw
}

// removal is the stretch that removes heard words h0..h1 (indices into heard),
// cut in the pauses: from after the kept word before them (or the clip's start)
// to before the kept word after them (or the clip's end).
func (cw clipWords) removal(c editspec.Clip, h0, h1 int) (from, to float64) {
	first, last := cw.all[cw.heard[h0]], cw.all[cw.heard[h1]]
	from, to = c.In, c.Out
	if h0 > 0 {
		prev := cw.all[cw.heard[h0-1]]
		gap := first.from - prev.to
		switch {
		case gap >= 2*padAfter:
			from = prev.to + padAfter
		case gap > 0:
			from = prev.to + gap/2
		default:
			from = prev.to
		}
	}
	if h1+1 < len(cw.heard) {
		next := cw.all[cw.heard[h1+1]]
		gap := next.from - last.to
		switch {
		case gap >= 2*padBefore:
			to = next.from - padBefore
		case gap > 0:
			to = last.to + gap/2
		default:
			to = next.from
		}
	}
	// To the millisecond, without float noise pushing a value over (1.2 + 0.15);
	// float64(…) keeps arm64 from fusing the multiply into the add.
	from = math.Max(c.In, math.Floor(float64(from*1000)+1e-6)/1000)
	to = math.Min(c.Out, math.Ceil(float64(to*1000)-1e-6)/1000)
	return from, to
}

func (cw clipWords) text(h0, h1 int) string {
	var parts []string
	for h := h0; h <= h1; h++ {
		parts = append(parts, cw.all[cw.heard[h]].text)
	}
	return textalign.JoinWords(parts)
}

// around is a few heard words before h0 and after h1 (context).
func (cw clipWords) around(h0, h1 int) (string, string) {
	var b, a []string
	for h := max(0, h0-4); h < h0; h++ {
		b = append(b, cw.all[cw.heard[h]].text)
	}
	for h := h1 + 1; h < min(len(cw.heard), h1+5); h++ {
		a = append(a, cw.all[cw.heard[h]].text)
	}
	return textalign.JoinWords(b), textalign.JoinWords(a)
}

// wordCutter turns heard word runs of a cut's clips into removals.
type wordCutter struct {
	spec   *editspec.Spec
	placed []editspec.PlanClip
	fps    float64
	tx     func(assetID string) []editspec.TranscriptSegment
	cache  map[int]clipWords
}

func newWordCutter(s *editspec.Spec, tx func(string) []editspec.TranscriptSegment) *wordCutter {
	n := editspec.Clone(s)
	editspec.Normalize(n)
	placed, _ := editspec.Timeline(n)
	return &wordCutter{spec: n, placed: placed, fps: float64(n.Output.FPS), tx: tx, cache: map[int]clipWords{}}
}

func (wc *wordCutter) words(i int) clipWords {
	if cw, ok := wc.cache[i]; ok {
		return cw
	}
	c := wc.spec.Clips[i]
	var segs []editspec.TranscriptSegment
	if wc.tx != nil && !c.Muted {
		segs = wc.tx(c.AssetID)
	}
	cw := wordsOfClip(c, segs)
	wc.cache[i] = cw
	return cw
}

func (wc *wordCutter) make(i, h0, h1 int, kind, reason string) WordRemoval {
	c := wc.spec.Clips[i]
	cw := wc.words(i)
	from, to := cw.removal(c, h0, h1)
	before, after := cw.around(h0, h1)
	first, last := cw.all[cw.heard[h0]], cw.all[cw.heard[h1]]
	pc := wc.placed[i]
	at := (float64(pc.From) + float64((first.from-pc.SourceIn)/pc.PlaybackRate*wc.fps)) / wc.fps
	return WordRemoval{ClipID: c.ID, Clip: i + 1, Kind: kind, Reason: reason, Words: cw.text(h0, h1), Before: before, After: after, Speaker: first.speaker,
		WordsFrom: first.from, WordsTo: last.to, From: from, To: to, At: math.Round(math.Max(0, at)*1000) / 1000,
		Seconds: math.Round((to-from)/speedOf(c)*1000) / 1000}
}

func speedOf(c editspec.Clip) float64 {
	if c.Speed > 0 {
		return c.Speed
	}
	return 1
}

// FindWords finds a phrase in what the cut says (the words its clips play),
// in clip order. clip limits the search to one clip (its ID or number; "" =
// every clip). It returns each place with the stretch that removes it, and, when
// nothing matches, the closest thing that is said (a hint).
func FindWords(s *editspec.Spec, tx func(assetID string) []editspec.TranscriptSegment, phrase, clip string) ([]WordRemoval, string) {
	wc := newWordCutter(s, tx)
	var out []WordRemoval
	var best string
	bestD := math.MaxInt
	for i, c := range wc.spec.Clips {
		if clip != "" && c.ID != clip && fmt.Sprint(i+1) != clip {
			continue
		}
		cw := wc.words(i)
		heard := make([]wordRef, len(cw.heard))
		for k, h := range cw.heard {
			heard[k] = cw.all[h]
		}
		for _, m := range findPhrase(heard, phrase) {
			out = append(out, wc.make(i, m.first, m.last, RemovePhrase, "you asked for these words"))
		}
		if len(out) == 0 {
			if m, ok := closest(heard, phrase); ok && m.dist < bestD {
				bestD, best = m.dist, matchedWords(heard, m)
			}
		}
	}
	return out, best
}

// RemovalFor is the stretch that removes the words of clip i heard between
// from and to (source seconds; a word counts when its middle is inside), cut in
// the pauses around them. ok is false when no word is there.
func RemovalFor(s *editspec.Spec, tx func(assetID string) []editspec.TranscriptSegment, clipID string, from, to float64) (WordRemoval, bool) {
	wc := newWordCutter(s, tx)
	for i, c := range wc.spec.Clips {
		if c.ID != clipID {
			continue
		}
		cw := wc.words(i)
		h0, h1 := -1, -1
		for k, h := range cw.heard {
			w := cw.all[h]
			if mid := (w.from + w.to) / 2; mid >= from && mid < to {
				if h0 < 0 {
					h0 = k
				}
				h1 = k
			}
		}
		if h0 < 0 {
			return WordRemoval{}, false
		}
		return wc.make(i, h0, h1, RemoveSelection, "the words you picked"), true
	}
	return WordRemoval{}, false
}

// PhraseTime is where a clip should start on quoted words of a recording
// (edge "in": in the pause just before the first word) or end (edge "end": just
// after the last word), as a cut sheet's from "…" / to "…" does. Not found, or
// said more than once, is an error that says what is there.
func PhraseTime(segs []editspec.TranscriptSegment, phrase, edge string, duration float64) (float64, error) {
	ws := flattenWords(segs)
	if len(ws) == 0 {
		return 0, fmt.Errorf("this footage has no transcript yet, so words can't be found in it; use a time instead")
	}
	ms := findPhrase(ws, phrase)
	switch len(ms) {
	case 0:
		if m, ok := closest(ws, phrase); ok {
			return 0, fmt.Errorf("“%s” isn't said in this footage; the closest is “%s” at %s", phrase, matchedWords(ws, m), Clock(math.Round(ws[m.first].from*10)/10))
		}
		return 0, fmt.Errorf("“%s” isn't said in this footage", phrase)
	case 1:
	default:
		var at []string
		for _, m := range ms {
			at = append(at, Clock(math.Round(ws[m.first].from*10)/10))
		}
		return 0, fmt.Errorf("“%s” is said %d times (at %s); add a few words, or use a time", phrase, len(ms), strings.Join(at, ", "))
	}
	if edge == "end" {
		return snapOut(ws, ms[0].last, duration), nil
	}
	return snapIn(ws, ms[0].first), nil
}

// fillers are sounds people make while thinking (normalized words).
var fillers = map[string]bool{"um": true, "umm": true, "ummm": true, "uh": true, "uhh": true, "uhm": true, "erm": true, "er": true, "hmm": true, "hm": true, "mm": true}

// Cleanups suggests words to take out of a cut: filler sounds ("um"), words
// said twice in a row ("because because": the first goes) and false starts (a
// cut-off word, or a few words someone restarts: "we were, we went": the first
// try goes). Every suggestion is only a suggestion; a person accepts or skips it.
func Cleanups(s *editspec.Spec, tx func(assetID string) []editspec.TranscriptSegment) []WordRemoval {
	wc := newWordCutter(s, tx)
	var out []WordRemoval
	for i := range wc.spec.Clips {
		cw := wc.words(i)
		n := len(cw.heard)
		w := func(k int) wordRef { return cw.all[cw.heard[k]] }
		taken := make([]bool, n)
		take := func(h0, h1 int, kind, reason string) {
			for k := h0; k <= h1; k++ {
				taken[k] = true
			}
			out = append(out, wc.make(i, h0, h1, kind, reason))
		}
		free := func(h0, h1 int) bool {
			for k := h0; k <= h1; k++ {
				if k < 0 || k >= n || taken[k] {
					return false
				}
			}
			return true
		}
		for k := 0; k < n; k++ {
			if !free(k, k) {
				continue
			}
			x := w(k)
			if fillers[x.norm] {
				take(k, k, RemoveFiller, "a filler sound")
				continue
			}
			if t := strings.TrimRight(x.text, ",.;:!?"); strings.HasSuffix(t, "-") || strings.HasSuffix(t, "—") {
				take(k, k, RemoveFalseStart, "a word cut off halfway")
				continue
			}
			// Said twice in a row: the same 1–3 words again within a moment.
			found := false
			for m := 3; m >= 1 && !found; m-- {
				if k+2*m > n || !free(k, k+2*m-1) {
					continue
				}
				same := true
				for d := 0; d < m; d++ {
					if w(k+d).norm != w(k+m+d).norm {
						same = false
						break
					}
				}
				if same && w(k+m).from-w(k+m-1).to < 1.5 {
					take(k, k+m-1, RemoveRepeat, "said twice")
					found = true
				}
			}
			if found {
				continue
			}
			// A false start: 2–3 words, a break (a comma or dash, or a pause), then
			// the same first word again: "we were, we went".
			for m := 2; m <= 3 && !found; m++ {
				if k+m >= n || !free(k, k+m-1) || w(k).norm != w(k+m).norm || w(k).norm == "" {
					continue
				}
				last := w(k + m - 1)
				breakMark := strings.HasSuffix(last.text, ",") || strings.HasSuffix(last.text, "-") || strings.HasSuffix(last.text, "—") || strings.HasSuffix(last.text, "…") || strings.HasSuffix(last.text, "...")
				if breakMark || w(k+m).from-last.to >= 0.3 {
					take(k, k+m-1, RemoveFalseStart, "a false start")
					found = true
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

// RemovalOps turns word removals into removeWords ops, in the cut's order. A
// clip a removal cuts in two continues in its second part, so later removals in
// the same footage name that part; the parts' names are given explicitly so the
// ops replay exactly.
func RemovalOps(s *editspec.Spec, rs []WordRemoval, allowUnlock bool) ([]editspec.Op, error) {
	work := editspec.Clone(s)
	editspec.Normalize(work)
	sorted := append([]WordRemoval(nil), rs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Clip != sorted[j].Clip {
			return sorted[i].Clip < sorted[j].Clip
		}
		return sorted[i].From < sorted[j].From
	})
	family := map[string][]string{} // original clip → its current parts
	var ops []editspec.Op
	for _, r := range sorted {
		parts := family[r.ClipID]
		if parts == nil {
			parts = []string{r.ClipID}
		}
		target := ""
		for _, id := range parts {
			for _, c := range work.Clips {
				if c.ID == id && r.From < c.Out && r.To > c.In {
					target = id
				}
			}
		}
		if target == "" {
			continue // already removed with an earlier stretch
		}
		from, to := r.From, r.To
		words := r.Words
		op := editspec.Op{Op: "removeWords", ID: target, From: &from, To: &to, NewID: editspec.PartID(work, r.ClipID), Text: &words}
		next, _, err := editspec.ApplyOps(work, []editspec.Op{op}, allowUnlock)
		if err != nil {
			return nil, err
		}
		if !hasClip(next, op.NewID) {
			op.NewID = ""
		} else {
			parts = append(parts, op.NewID)
		}
		family[r.ClipID] = parts
		ops = append(ops, op)
		work = next
	}
	return ops, nil
}

func hasClip(s *editspec.Spec, id string) bool {
	for _, c := range s.Clips {
		if c.ID == id {
			return true
		}
	}
	return false
}
