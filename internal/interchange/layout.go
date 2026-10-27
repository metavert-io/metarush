package interchange

import (
	"fmt"
	"math"
)

// Segments: how editing apps that place transitions on the edit point
// (OpenTimelineIO, Final Cut Pro 7 XML) see a track. Items abut at edit
// points; a transition overlaps Before frames of the outgoing side and After
// frames of the incoming side, using media beyond each item's own range
// ("handles").

// Segment kinds.
const (
	segGap   = "gap"
	segItem  = "item"
	segTrans = "transition"
)

// Transition kinds on segments, beyond the picture Trans* kinds.
const (
	TransFadeIn         = "fade-in"         // from black (picture) or silence (sound), at an item's head
	TransFadeOut        = "fade-out"        // to black or silence, at an item's tail
	TransAudioCrossfade = "audio-crossfade" // two sounds blend across the edit point
)

type segment struct {
	kind     string
	item     *Item
	start    int     // timeline frame where the segment begins (items, gaps)
	duration int     // frames (items, gaps)
	source   float64 // items: media position at start, in timeline-rate frames
	// Transitions.
	trans  string
	before int // frames before the edit point
	after  int // frames after it
	cut    int // the edit point
	dir    string
	easing string
	from   string
}

func (s segment) end() int { return s.start + s.duration }

// layoutTrack lays a track out as gaps, items and transitions on edit points,
// shrinking transitions to the media each side has (dips use footage beyond a
// clip's own range in editing apps). notes collects what changed. With
// overlapsOnly, only overlapping pictures and sounds become transitions
// (FCPXML draws dips and fades with opacity and volume instead).
func layoutTrack(t *Track, fps int, notes *[]string, overlapsOnly bool) []segment {
	type edge struct {
		cut           int // edit point between items[i-1] and items[i]
		before, after int
		kind          string
		dir, easing   string
		from          string
	}
	n := len(t.Items)
	if n == 0 {
		return nil
	}
	starts := make([]int, n) // edit-point start of each item
	ends := make([]int, n)
	for i, it := range t.Items {
		starts[i], ends[i] = it.Start, it.End()
	}
	edges := make([]*edge, n) // edges[i]: the transition into items[i] from items[i-1]
	for i := 1; i < n; i++ {
		a, b := t.Items[i-1], t.Items[i]
		var c Cut
		if t.Cuts != nil && i < len(t.Cuts) {
			c = t.Cuts[i]
		}
		ov := a.End() - b.Start
		switch {
		case ov > 0: // overlapping pictures (dissolve, wipe…) or sounds (a blend)
			before := ov / 2
			cut := b.Start + before
			starts[i], ends[i-1] = cut, cut
			kind := TransAudioCrossfade
			if t.Kind == KindVideo {
				kind = c.Kind
				if kind == "" {
					kind = TransDissolve
				}
			}
			edges[i] = &edge{cut: cut, before: before, after: ov - before, kind: kind, dir: c.Direction, easing: c.Easing, from: c.From}
		case overlapsOnly:
		case ov == 0 && (c.Kind == TransDipBlack || c.Kind == TransDipWhite):
			edges[i] = &edge{cut: b.Start, before: c.DipOut, after: c.DipIn, kind: c.Kind, from: c.From}
		case ov == 0 && a.FadeOut > 0 && b.FadeIn > 0 && t.Kind == KindVideo:
			// A fade out straight into a fade in is a dip through black.
			edges[i] = &edge{cut: b.Start, before: a.FadeOut, after: b.FadeIn, kind: TransDipBlack, from: "fade"}
		}
	}
	// Handles: a transition needs media before the incoming item's first frame
	// and after the outgoing item's last frame. Overlaps have them (MetaRush
	// played those frames); dips may not.
	for i := 1; i < n; i++ {
		e := edges[i]
		if e == nil || e.kind == TransAudioCrossfade || (e.kind != TransDipBlack && e.kind != TransDipWhite) {
			continue
		}
		a, b := t.Items[i-1], t.Items[i]
		// Before the edit point the incoming clip shows media from before its
		// first frame; after it the outgoing clip shows media past its last.
		srcB := b.Source + float64(starts[i]-b.Start)*b.Speed
		availBefore := int(math.Floor(srcB/b.Speed + 1e-9))
		availAfter := handleAfter(a, ends[i-1], fps)
		before := min(e.before, availBefore, ends[i-1]-starts[i-1])
		after := min(e.after, availAfter, ends[i]-starts[i])
		if before != e.before || after != e.after {
			*notes = append(*notes, fmt.Sprintf("the fade through %s at %s is shorter there: the footage has no more frames on one side", dipColour(e.kind), Clock(e.cut, fps)))
		}
		if before+after == 0 {
			edges[i] = nil
			continue
		}
		e.before, e.after = before, after
	}
	var segs []segment
	cursor := 0
	for i, it := range t.Items {
		start, end := starts[i], ends[i]
		if end <= start {
			continue
		}
		if start > cursor {
			segs = append(segs, segment{kind: segGap, start: cursor, duration: start - cursor})
		}
		if e := edges[i]; e != nil {
			segs = append(segs, segment{kind: segTrans, trans: e.kind, before: e.before, after: e.after, cut: e.cut, dir: e.dir, easing: e.easing, from: e.from})
		} else if it.FadeIn > 0 && !overlapsOnly {
			segs = append(segs, segment{kind: segTrans, trans: TransFadeIn, before: 0, after: min(it.FadeIn, end-start), cut: start})
		}
		segs = append(segs, segment{kind: segItem, item: it, start: start, duration: end - start, source: it.Source + float64(start-it.Start)*it.Speed})
		cursor = end
		nextJoined := i+1 < n && edges[i+1] != nil
		if it.FadeOut > 0 && !nextJoined && !overlapsOnly {
			segs = append(segs, segment{kind: segTrans, trans: TransFadeOut, before: min(it.FadeOut, end-start), after: 0, cut: end})
		}
	}
	return segs
}

// handleAfter is how many timeline frames of media an item has past an edit
// point at timeline frame end.
func handleAfter(it *Item, end, fps int) int {
	if it.Media == nil || it.Media.Duration <= 0 {
		return 0
	}
	used := it.Source + float64(end-it.Start)*it.Speed // media position at end, timeline-rate frames
	left := it.Media.Duration*float64(fps) - used
	if left <= 0 {
		return 0
	}
	return int(math.Floor(left/it.Speed + 1e-9))
}

func dipColour(kind string) string {
	if kind == TransDipWhite {
		return "white"
	}
	return "black"
}

// Timecode prints a timeline frame as HH:MM:SS:FF.
func Timecode(f, fps int) string {
	if fps <= 0 {
		fps = 30
	}
	if f < 0 {
		f = 0
	}
	ff := f % fps
	s := f / fps
	return fmt.Sprintf("%02d:%02d:%02d:%02d", s/3600, s/60%60, s%60, ff)
}

// Clock prints a timeline frame as m:ss for people ("1:04").
func Clock(f, fps int) string {
	if fps <= 0 {
		fps = 30
	}
	s := f / fps
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}
