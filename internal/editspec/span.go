package editspec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// Graphics that last a section or the whole video, and change their fields
// while they stay on screen (D163, docs/call-recordings.md).
//
// A graphic with a Span starts when its first clip starts and ends when its
// last clip ends (At and Duration measured inside: Duration 0 is to the end),
// so it follows the clips through every edit. Its Changes are moments in the
// footage: wherever the cut shows that footage at or after the moment (until
// the footage's next change), the change's fields are merged over the
// graphic's own; frames showing footage without changes use its own fields.
// Go resolves both into frames (PlanOverlay.From/DurationInFrames/Segments),
// so the renderer and the preview never compute a time.

// IsSpanning reports whether a graphic lasts a section or the whole video.
func IsSpanning(o Overlay) bool { return o.Span != nil }

// spanClips finds the first and last clip of a span among placed clips (-1
// when a named clip isn't in the cut).
func spanClips(sp *OverlaySpan, clips []PlanClip) (first, last int) {
	first, last = 0, len(clips)-1
	if sp == nil {
		return first, last
	}
	idx := func(id string) int {
		for i, c := range clips {
			if c.ID == id {
				return i
			}
		}
		return -1
	}
	if sp.From != "" {
		first = idx(sp.From)
	}
	if sp.To != "" {
		last = idx(sp.To)
	}
	return first, last
}

// OverlayWindow is where a graphic is on the timeline, in frames: its At and
// Duration on the finished video, or, with a span, from the start of the span's
// first clip to the end of its last (At and Duration measured inside). ok is
// false when a span names a clip the cut doesn't have or runs backwards.
func OverlayWindow(s *Spec, o Overlay, clips []PlanClip, total int) (from, dur int, ok bool) {
	fps := specFPS(s)
	if o.Span == nil {
		return int(math.Round(o.At * fps)), int(math.Round(o.Duration * fps)), true
	}
	if len(clips) == 0 {
		return 0, 0, false
	}
	first, last := spanClips(o.Span, clips)
	if first < 0 || last < 0 || first > last {
		return 0, 0, false
	}
	start := clips[first].From
	end := clips[last].From + clips[last].DurationInFrames
	if last+1 < len(clips) && IsOverlapTransition(clips[last+1].TransitionIn.Type) {
		// The next clip dissolves in: the span ends half-way through it.
		end = clips[last+1].From + clips[last+1].TransitionIn.DurationInFrames/2
	}
	if end > total {
		end = total
	}
	from = start + int(math.Round(o.At*fps))
	if o.Duration > 0 {
		dur = int(math.Round(o.Duration * fps))
		if from+dur > end {
			dur = end - from
		}
	} else {
		dur = end - from
	}
	if dur < 0 {
		dur = 0
	}
	return from, dur, true
}

// sampleAt is the footage time a clip shows at its frame k (Remotion's
// trimBefore and playback rate: the frame grid, D161).
func sampleAt(pc PlanClip, fps float64, k int) float64 {
	rate := pc.PlaybackRate
	if rate <= 0 {
		rate = 1
	}
	return (float64(pc.TrimBefore) + float64(k)*rate) / fps
}

// firstFrameAtOrAfter is the clip frame (0-based) whose footage time is at or
// after t: where a change at t first shows. It may be ≥ the clip's length.
func firstFrameAtOrAfter(pc PlanClip, fps float64, t float64) int {
	rate := pc.PlaybackRate
	if rate <= 0 {
		rate = 1
	}
	k := int(math.Ceil((t*fps-float64(pc.TrimBefore))/rate - 1e-6))
	if k < 0 {
		k = 0
	}
	return k
}

// shownRanges is, for each placed clip, the timeline frames where it is the
// clip on screen: from its start (half-way into a transition that overlaps
// the clip before) to the next clip's start (half-way into its transition).
func shownRanges(clips []PlanClip) [][2]int {
	out := make([][2]int, len(clips))
	for i, pc := range clips {
		from, to := pc.From, pc.From+pc.DurationInFrames
		if i > 0 && IsOverlapTransition(pc.TransitionIn.Type) {
			from += pc.TransitionIn.DurationInFrames / 2
		}
		if i+1 < len(clips) {
			nx := clips[i+1]
			to = nx.From
			if IsOverlapTransition(nx.TransitionIn.Type) {
				to += nx.TransitionIn.DurationInFrames / 2
			}
		}
		out[i] = [2]int{from, max(from, to)}
	}
	return out
}

// mergeFields merges a change's fields over a graphic's (a JSON merge patch:
// nested objects merge, lists and values replace, null removes).
func mergeFields(base, change json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(base)) == 0 {
		base = json.RawMessage(`{}`)
	}
	if len(bytes.TrimSpace(change)) == 0 {
		return base
	}
	return canonicalObject(MergePatch(base, change))
}

// canonicalObject re-encodes a JSON object with sorted keys, so equal fields
// compare equal.
func canonicalObject(raw json.RawMessage) json.RawMessage {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return b
}

// footageChanges are a graphic's changes per footage, in the order of their
// moments (a later change at the same moment wins).
func footageChanges(o Overlay) map[string][]OverlayChange {
	out := map[string][]OverlayChange{}
	for _, c := range o.Changes {
		out[c.Footage] = append(out[c.Footage], c)
	}
	for k := range out {
		cs := out[k]
		sort.SliceStable(cs, func(i, j int) bool { return cs[i].At < cs[j].At })
	}
	return out
}

// stateAt is a graphic's fields where a footage is shown at time t: its own
// fields with every change of that footage at or before t merged in order.
func stateAt(base json.RawMessage, changes []OverlayChange, t float64) json.RawMessage {
	out := base
	for _, c := range changes {
		if c.At > t+1e-9 {
			break
		}
		out = mergeFields(out, c.Params)
	}
	return out
}

// OverlaySegments resolves a graphic's changes into segments over its window
// [from, from+dur): the fields from each frame on, where the footage on screen
// or its changes make them differ. The first segment starts at 0.
func OverlaySegments(s *Spec, o Overlay, clips []PlanClip, from, dur int) []PlanSegment {
	if dur <= 0 {
		return nil
	}
	fps := specFPS(s)
	base := canonicalObject(o.Params)
	if len(bytes.TrimSpace(o.Params)) == 0 {
		base = json.RawMessage(`{}`)
	}
	byFootage := footageChanges(o)
	type mark struct {
		frame  int
		params json.RawMessage
	}
	var marks []mark
	end := from + dur
	for i, r := range shownRanges(clips) {
		lo, hi := max(r[0], from), min(r[1], end)
		if lo >= hi {
			continue
		}
		pc := clips[i]
		cs := byFootage[pc.AssetID]
		k0 := lo - pc.From
		marks = append(marks, mark{lo, stateAt(base, cs, sampleAt(pc, fps, k0))})
		for _, c := range cs {
			k := firstFrameAtOrAfter(pc, fps, c.At)
			f := pc.From + k
			if f <= lo || f >= hi {
				continue
			}
			marks = append(marks, mark{f, stateAt(base, cs, sampleAt(pc, fps, k))})
		}
	}
	if len(marks) == 0 {
		return []PlanSegment{{From: 0, Params: base}}
	}
	sort.SliceStable(marks, func(i, j int) bool { return marks[i].frame < marks[j].frame })
	var out []PlanSegment
	for _, m := range marks {
		rel := m.frame - from
		if len(out) > 0 && bytes.Equal(out[len(out)-1].Params, m.params) {
			continue
		}
		if len(out) > 0 && out[len(out)-1].From == rel {
			out[len(out)-1].Params = m.params
			continue
		}
		out = append(out, PlanSegment{From: rel, Params: m.params})
	}
	if out[0].From != 0 {
		out = append([]PlanSegment{{From: 0, Params: base}}, out...)
	}
	// Two neighbours may have become equal after a replacement.
	merged := out[:1]
	for _, sg := range out[1:] {
		if bytes.Equal(merged[len(merged)-1].Params, sg.Params) {
			continue
		}
		merged = append(merged, sg)
	}
	return merged
}

// spanWords names a graphic's span for messages: "the whole video", "from the
// hook to the end"…
func spanWords(sp *OverlaySpan) string {
	switch {
	case sp == nil:
		return ""
	case sp.From == "" && sp.To == "":
		return "the whole video"
	case sp.From == "":
		return fmt.Sprintf("from the start to the end of clip %s", sp.To)
	case sp.To == "":
		return fmt.Sprintf("from clip %s to the end", sp.From)
	}
	return fmt.Sprintf("from clip %s to clip %s", sp.From, sp.To)
}

// changeIssues checks a graphic's changes: each one's fields merged over the
// graphic's (in the footage's order) must be valid fields, pictures can't
// change, and a change of footage the cut doesn't show is pointed out.
func changeIssues(s *Spec, o Overlay, p, who string, validate func(json.RawMessage, string) []Issue, resourceFields []string) []Issue {
	if len(o.Changes) == 0 {
		return nil
	}
	var out []Issue
	shown := map[string]bool{}
	for _, c := range s.Clips {
		shown[c.AssetID] = true
	}
	state := map[string]json.RawMessage{}
	order := make([]int, len(o.Changes))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return o.Changes[order[a]].At < o.Changes[order[b]].At })
	unseen := 0
	for _, j := range order {
		c := o.Changes[j]
		cp := fmt.Sprintf("%s/changes/%d", p, j)
		if f := changeTouchesResource(c.Params, resourceFields); f != "" {
			out = append(out, errI("change_resource", cp+"/params", "A change of %s sets its picture (%s); pictures can't change while a graphic is on screen, so use a second graphic for that.", who, f))
			continue
		}
		if !shown[c.Footage] {
			unseen++
		}
		cur, ok := state[c.Footage]
		if !ok {
			cur = o.Params
		}
		cur = mergeFields(cur, c.Params)
		state[c.Footage] = cur
		if validate != nil {
			out = append(out, validate(cur, cp+"/params")...)
		}
	}
	if unseen > 0 {
		out = append(out, warnI("change_unseen", p+"/changes", "%s of %s %s footage the cut doesn't show, so %s never seen.", upperFirst(pluralWords(unseen, "change", "changes")), who, isAre(unseen, "is in", "are in"), isAre(unseen, "it is", "they are")))
	}
	return out
}

func pluralWords(n int, one, many string) string {
	if n == 1 {
		return "one " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func isAre(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// changeTouchesResource reports a change that would change a field naming a
// picture (resource fields can't change on screen: a cut pins what a graphic
// draws, D82).
func changeTouchesResource(params json.RawMessage, resourceFields []string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(params, &m) != nil {
		return ""
	}
	for _, f := range resourceFields {
		if _, ok := m[f]; ok {
			return f
		}
	}
	return ""
}

// narrowSpans moves span ends off clip i before it is removed: a span that
// started with it starts with the next clip, one that ended with it ends with
// the clip before (a span left with nothing is reported by validation). Locked
// graphics keep their span. It returns the graphics it changed.
func narrowSpans(s *Spec, i int) []string {
	if i < 0 || i >= len(s.Clips) {
		return nil
	}
	id := s.Clips[i].ID
	var ids []string
	for k := range s.Overlays {
		o := &s.Overlays[k]
		if o.Span == nil || o.Locked || (o.Span.From != id && o.Span.To != id) {
			continue
		}
		cp := *o.Span
		if cp.From == id && i+1 < len(s.Clips) {
			cp.From = s.Clips[i+1].ID
		}
		if cp.To == id && i > 0 {
			cp.To = s.Clips[i-1].ID
		}
		o.Span = &cp
		ids = append(ids, o.ID)
	}
	return ids
}

// retargetSpans moves span ends that named a clip which was cut in two to its
// second part, so the graphic still lasts to where it did (splitClip,
// deleteRange and removeWords make the second part). It returns the graphics
// it changed (they count as touched for rebasing).
func retargetSpans(s *Spec, orig, second string) []string {
	var ids []string
	for i := range s.Overlays {
		if sp := s.Overlays[i].Span; sp != nil && sp.To == orig && !s.Overlays[i].Locked {
			cp := *sp
			cp.To = second
			s.Overlays[i].Span = &cp
			ids = append(ids, s.Overlays[i].ID)
		}
	}
	return ids
}
