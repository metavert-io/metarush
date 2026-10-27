package editspec

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Studio editing ops: the gestures of a magnetic (gap-free) timeline written as
// typed patch operations, so they go through the same validation, locks,
// rebasing and revisions as every other edit (D57).
//
//   - splitClip {id, at, newId?}: cut a clip in two at a moment in its footage.
//   - trimClip {id, edge in|end, to, mode ripple|roll}: move a clip's start or end
//     to a new moment in its footage; ripple (default) lets the rest of the cut
//     move up or back, roll moves the neighbouring clip's edge with it so the
//     cut keeps its length.
//   - slipClip {id, by}: slide the footage under a clip (same length and place).
//   - deleteRange {from, to, newId?}: remove a stretch of the cut's timeline
//     (seconds), across clip boundaries; the rest closes up.
//   - removeWords {id, from, to, text?, newId?}: remove a stretch of one clip's
//     footage (source seconds), normally words with the pauses around them.
//
// Times on a clip are source seconds, so a change written against an older
// version still means the same moment in the footage after a rebase. deleteRange
// is the one op in timeline seconds: it touches the places of the clips it cuts
// ("at:<clip>"), and ChangedIDs reports a clip whose place moved, so an earlier
// change that shifted them makes it conflict instead of cutting the wrong part.

// StudioOps are the op names this file implements.
var StudioOps = []string{"splitClip", "trimClip", "slipClip", "deleteRange", "removeWords"}

// Trim modes.
const (
	TrimRipple = "ripple"
	TrimRoll   = "roll"
)

var partSuffix = regexp.MustCompile(`-\d+$`)

// PartID is the name the second part of a clip gets when it is cut in two:
// "<id>-2", "<id>-3"… (a trailing -N is replaced, not extended), unique among
// every item of the spec and at most 64 characters.
func PartID(s *Spec, id string) string {
	base := id
	if m := partSuffix.FindStringIndex(base); m != nil && m[0] > 0 {
		base = base[:m[0]]
	}
	if base == "" {
		base = "clip"
	}
	used := specIDs(s)
	for n := 2; ; n++ {
		suf := "-" + strconv.Itoa(n)
		b := base
		if len(b)+len(suf) > 64 {
			b = b[:64-len(suf)]
		}
		if cand := b + suf; !used[cand] {
			return cand
		}
	}
}

// specIDs is every item ID in a spec (clips, sounds, graphics, narration lines).
func specIDs(s *Spec) map[string]bool {
	used := map[string]bool{"captions": true, "voiceover": true, "mix": true}
	for _, c := range s.Clips {
		used[c.ID] = true
	}
	for _, a := range s.Audio {
		used[a.ID] = true
	}
	for _, o := range s.Overlays {
		used[o.ID] = true
	}
	if s.Voiceover != nil {
		for _, b := range s.Voiceover.Beats {
			used[b.ID] = true
		}
	}
	return used
}

func cloneClip(c Clip) Clip {
	b, _ := json.Marshal(c)
	var out Clip
	_ = json.Unmarshal(b, &out)
	return out
}

func specFPS(s *Spec) float64 {
	if s.Output.FPS > 0 {
		return float64(s.Output.FPS)
	}
	return 30
}

func speedOf(c Clip) float64 {
	if c.Speed > 0 {
		return c.Speed
	}
	return 1
}

// frames is how many output frames a source range of a clip lasts (as Timeline counts).
func frames(c Clip, in, out, fps float64) int {
	return int(math.Round((out - in) / speedOf(c) * fps))
}

// SplitParts is clip c cut in two at source time at, as splitClip does it.
func SplitParts(c Clip, at float64, newID string) (Clip, Clip) { return splitParts(c, at, newID) }

// splitParts is clip c cut at source time at: the first part keeps the clip's
// name, notes and place; the second part (newID) starts with a straight cut and
// takes over the fade-out. Everything else (footage, speed, volume, framing,
// label, reason and evidence) is the same on both parts.
func splitParts(c Clip, at float64, newID string) (Clip, Clip) {
	left, right := cloneClip(c), cloneClip(c)
	left.Out, left.FadeOut = at, 0
	right.ID, right.In, right.Transition, right.Notes = newID, at, nil, ""
	if len(c.Shots) > 0 {
		// A conversation clip's picture stays as it was: each part keeps the
		// shots in its range and starts with the shot it starts in (D99).
		left.Shots, right.Shots = ShotsFor(c.Shots, c.In, at), ShotsFor(c.Shots, at, c.Out)
	}
	return left, right
}

// cutOut removes the source range [from, to) from clip c and returns what is
// left: nothing (the whole clip goes), its end, its start, or two parts (the
// second, newID, joins with a straight cut). A part shorter than two frames is
// dropped with the range, since it could not be shown. head reports that the
// clip's start was removed.
func cutOut(c Clip, from, to, fps float64, newID string) (parts []Clip, head bool, err error) {
	from, to = round3(math.Max(from, c.In)), round3(math.Min(to, c.Out))
	if to <= from {
		return nil, false, fmt.Errorf("none of %s is in that stretch (it plays %s to %s of its footage)", clipWords(c), Clock(c.In), Clock(c.Out))
	}
	keepHead := frames(c, c.In, from, fps) >= 2
	keepTail := frames(c, to, c.Out, fps) >= 2
	switch {
	case !keepHead && !keepTail:
		return nil, true, nil
	case !keepHead:
		x := cloneClip(c)
		x.In = to
		return []Clip{x}, true, nil
	case !keepTail:
		x := cloneClip(c)
		x.Out = from
		return []Clip{x}, false, nil
	}
	left, right := splitParts(c, from, newID)
	right.In = to
	if len(c.Shots) > 0 {
		right.Shots = ShotsFor(c.Shots, to, c.Out)
	}
	return []Clip{left, right}, false, nil
}

// applyStudioOp applies one of StudioOps to s in place and returns the IDs it
// touched (for locks and rebasing).
func applyStudioOp(s *Spec, n int, op Op, allowUnlock bool) ([]string, error) {
	idx := func(id string) int {
		for i, c := range s.Clips {
			if c.ID == id {
				return i
			}
		}
		return -1
	}
	locked := func(i int) error {
		if s.Clips[i].Locked && !allowUnlock {
			return &LockedError{ID: s.Clips[i].ID, Op: n}
		}
		return nil
	}
	fps := specFPS(s)
	newID := func(of string) (string, error) {
		id := op.NewID
		if id == "" {
			return PartID(s, of), nil
		}
		if specIDs(s)[id] {
			return "", fmt.Errorf("the name “%s” for the new part is already used (newId)", id)
		}
		return id, nil
	}
	switch op.Op {
	case "splitClip":
		i := idx(op.ID)
		if i < 0 {
			return nil, fmt.Errorf("there's no clip “%s” in the cut", op.ID)
		}
		if op.At == nil {
			return nil, fmt.Errorf("it doesn't say where to split the clip (at: the moment in the clip's footage)")
		}
		c := s.Clips[i]
		at := round3(*op.At)
		if frames(c, c.In, at, fps) < 1 || frames(c, at, c.Out, fps) < 1 {
			return nil, fmt.Errorf("%s isn't inside %s (it plays %s to %s of its footage)", Clock(at), clipWords(c), Clock(c.In), Clock(c.Out))
		}
		id, err := newID(c.ID)
		if err != nil {
			return nil, err
		}
		left, right := splitParts(c, at, id)
		s.Clips[i] = left
		s.Clips = append(s.Clips[:i+1], append([]Clip{right}, s.Clips[i+1:]...)...)
		return append([]string{c.ID, id}, retargetSpans(s, c.ID, id)...), nil

	case "trimClip":
		i := idx(op.ID)
		if i < 0 {
			return nil, fmt.Errorf("there's no clip “%s” in the cut", op.ID)
		}
		if op.To == nil {
			return nil, fmt.Errorf("it doesn't say where the edge moves to (to: the new moment in the clip's footage)")
		}
		if op.Edge != "in" && op.Edge != "end" {
			return nil, fmt.Errorf("edge is in (the clip's start) or end (its end)")
		}
		mode := op.Mode
		if mode == "" {
			mode = TrimRipple
		}
		if mode != TrimRipple && mode != TrimRoll {
			return nil, fmt.Errorf("mode is ripple (the rest of the cut moves) or roll (the neighbouring clip's edge moves with it)")
		}
		c := &s.Clips[i]
		to := round3(*op.To)
		if to < 0 {
			return nil, fmt.Errorf("a clip can't start before its footage does")
		}
		touched := []string{c.ID}
		if mode == TrimRoll {
			j := i - 1
			if op.Edge == "end" {
				j = i + 1
			}
			if j < 0 || j >= len(s.Clips) {
				where := "before"
				if op.Edge == "end" {
					where = "after"
				}
				return nil, fmt.Errorf("%s has no clip %s it to roll the cut with", clipWords(*c), where)
			}
			if err := locked(j); err != nil {
				return nil, err
			}
			nb := &s.Clips[j]
			// The cut moves by the same time on the timeline for both clips.
			old := c.In
			if op.Edge == "end" {
				old = c.Out
			}
			delta := (to - old) / speedOf(*c)
			if op.Edge == "in" {
				nb.Out = round3(nb.Out + float64(delta*speedOf(*nb)))
				if nb.Out <= nb.In {
					return nil, fmt.Errorf("the cut would move past the start of %s", clipWords(*nb))
				}
			} else {
				nb.In = round3(nb.In + float64(delta*speedOf(*nb)))
				if nb.In < 0 {
					return nil, fmt.Errorf("the footage of %s doesn't start early enough to move the cut that far", clipWords(*nb))
				}
				if nb.In >= nb.Out {
					return nil, fmt.Errorf("the cut would move past the end of %s", clipWords(*nb))
				}
			}
			cut := i
			if op.Edge == "end" {
				cut = j
			}
			touched = append(touched, nb.ID, boundaryID(s, cut))
		}
		if op.Edge == "in" {
			c.In = to
		} else {
			c.Out = to
		}
		if c.Out <= c.In {
			return nil, fmt.Errorf("%s would end before it starts", clipWords(*c))
		}
		return touched, nil

	case "slipClip":
		i := idx(op.ID)
		if i < 0 {
			return nil, fmt.Errorf("there's no clip “%s” in the cut", op.ID)
		}
		if op.By == nil {
			return nil, fmt.Errorf("it doesn't say how far to slide the footage (by: seconds, positive is later)")
		}
		c := &s.Clips[i]
		in, out := round3(c.In+*op.By), round3(c.Out+*op.By)
		if in < 0 {
			return nil, fmt.Errorf("the footage of %s doesn't start early enough to slide it %s back", clipWords(*c), seconds(-*op.By))
		}
		c.In, c.Out = in, out
		return []string{c.ID}, nil

	case "removeWords":
		i := idx(op.ID)
		if i < 0 {
			return nil, fmt.Errorf("there's no clip “%s” in the cut", op.ID)
		}
		if op.From == nil || op.To == nil {
			return nil, fmt.Errorf("it doesn't say which stretch to take out (from and to, in the clip's footage)")
		}
		c := s.Clips[i]
		id, err := newID(c.ID)
		if err != nil {
			return nil, err
		}
		parts, _, err := cutOut(c, *op.From, *op.To, fps, id)
		if err != nil {
			return nil, err
		}
		if len(parts) == 0 && len(s.Clips) == 1 {
			return nil, fmt.Errorf("that would remove the whole cut")
		}
		s.Clips = append(s.Clips[:i], append(parts, s.Clips[i+1:]...)...)
		touched := []string{c.ID}
		if len(parts) == 2 {
			touched = append(touched, id)
			touched = append(touched, retargetSpans(s, c.ID, id)...)
		}
		return touched, nil

	case "deleteRange":
		return deleteRange(s, n, op, allowUnlock)
	}
	return nil, fmt.Errorf("MetaRush doesn't know this kind of change")
}

// shotSpan is the part of the timeline a clip owns (frames [from, to)): its
// picture, with overlapping transitions split at their middle (as Shots does).
type shotSpan struct{ from, to int }

func shotSpans(clips []PlanClip) []shotSpan {
	out := make([]shotSpan, len(clips))
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
		out[i] = shotSpan{from, max(from, to)}
	}
	return out
}

// deleteRange removes [from, to) of the timeline (seconds): clips inside it go,
// clips it reaches into are trimmed, and a range inside one clip cuts it in two.
// Where both sides of the new cut were trimmed, the cut is a straight one.
func deleteRange(s *Spec, n int, op Op, allowUnlock bool) ([]string, error) {
	if op.From == nil || op.To == nil {
		return nil, fmt.Errorf("it doesn't say which stretch to take out (from and to, in seconds of the video)")
	}
	norm := Clone(s)
	Normalize(norm)
	fps := specFPS(norm)
	clips, total := Timeline(norm)
	F, T := int(math.Round(*op.From*fps)), int(math.Round(*op.To*fps))
	if F < 0 || T <= F {
		return nil, fmt.Errorf("the stretch has to start before it ends")
	}
	if F >= total {
		return nil, fmt.Errorf("the cut is only %s long, so there is nothing at %s", seconds(float64(total)/fps), Clock(*op.From))
	}
	T = min(T, total)
	spans := shotSpans(clips)
	srcAt := func(i, f int) float64 {
		return round3(clips[i].SourceIn + float64(f-clips[i].From)/fps*clips[i].PlaybackRate)
	}
	var out []Clip
	var touched []string
	var split [][2]string // clips cut in two: the first part's name and the second's
	head := -1            // index in out of the clip whose start was trimmed, when the range began before it
	affected := 0
	for i, c := range s.Clips {
		sp := spans[i]
		if T <= sp.from || F >= sp.to || sp.to <= sp.from {
			out = append(out, c)
			continue
		}
		if c.Locked && !allowUnlock {
			return nil, &LockedError{ID: c.ID, Op: n}
		}
		affected++
		from, to := c.In, c.Out
		if F > sp.from {
			from = srcAt(i, F)
		}
		if T < sp.to {
			to = srcAt(i, T)
		}
		id := ""
		if F > sp.from && T < sp.to { // inside this clip: it is cut in two
			id = op.NewID
			if id == "" {
				id = PartID(s, c.ID)
			} else if specIDs(s)[id] {
				return nil, fmt.Errorf("the name “%s” for the new part is already used (newId)", id)
			}
		}
		parts, cutHead, err := cutOut(c, from, to, fps, id)
		if err != nil {
			return nil, err
		}
		touched = append(touched, c.ID, "at:"+c.ID)
		if len(parts) == 2 {
			touched = append(touched, id)
			split = append(split, [2]string{c.ID, id})
		}
		if cutHead && len(parts) == 1 && F < sp.from {
			head = len(out)
		}
		out = append(out, parts...)
	}
	if affected == 0 {
		return nil, fmt.Errorf("there's nothing in the cut between %s and %s", Clock(*op.From), Clock(*op.To))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("that would remove the whole cut")
	}
	s.Clips = out
	for _, p := range split {
		touched = append(touched, retargetSpans(s, p[0], p[1])...)
	}
	if head >= 0 && s.Clips[head].Transition != nil {
		// Both sides of this cut were trimmed: it is a new cut, a straight one.
		s.Clips[head].Transition = nil
		touched = append(touched, boundaryID(s, head))
		if head > 0 {
			touched = append(touched, s.Clips[head-1].ID)
		}
	}
	return touched, nil
}

// clipStarts is where every clip's picture starts on the timeline (frames).
func clipStarts(s *Spec) map[string]int {
	c := s
	if s.Output.FPS == 0 || hasZeroSpeed(s) {
		c = Clone(s)
		Normalize(c)
	}
	placed, _ := Timeline(c)
	out := make(map[string]int, len(placed))
	for _, pc := range placed {
		out[pc.ID] = pc.From
	}
	return out
}

func hasZeroSpeed(s *Spec) bool {
	for _, c := range s.Clips {
		if c.Speed == 0 {
			return true
		}
	}
	return false
}

// OpsSummary says in plain words what a list of ops does, counting each kind of
// edit in the order they first appear: "Moved 2 clips, trimmed 1, took out
// words in 1 place". Timeline saves use it as their instruction.
func OpsSummary(ops []Op) string {
	type kind struct {
		verb, one, many string
		clip            bool // counts clips: later clauses leave out the noun
	}
	kinds := map[string]kind{}
	key := func(op Op) (string, kind) {
		switch op.Op {
		case "moveClip":
			return "move", kind{"moved", "clip", "clips", true}
		case "insertClip":
			return "add", kind{"added", "clip", "clips", true}
		case "removeClip":
			return "remove", kind{"removed", "clip", "clips", true}
		case "splitClip":
			return "split", kind{"split", "clip", "clips", true}
		case "trimClip":
			if op.Mode == TrimRoll {
				return "roll", kind{"moved", "cut between two clips", "cuts between clips", false}
			}
			return "trim", kind{"trimmed", "clip", "clips", true}
		case "slipClip":
			return "slip", kind{"slid the footage in", "clip", "clips", true}
		case "updateClip", "replaceClip":
			return "change", kind{"changed", "clip", "clips", true}
		case "deleteRange":
			return "range", kind{"cut out", "part", "parts", false}
		case "removeWords":
			if op.Text != nil && strings.TrimSpace(*op.Text) == "" {
				// A stretch with no words: a long pause shortened (D236).
				return "pause", kind{"shortened", "pause", "pauses", false}
			}
			return "words", kind{"took out words in", "place", "places", false}
		case "setTransition":
			if op.Edge == "end" {
				return "fade", kind{"changed", "fade-out", "fade-outs", false}
			}
			return "transition", kind{"changed", "transition", "transitions", false}
		case "addOverlay", "updateOverlay", "removeOverlay":
			return "graphic", kind{"changed", "graphic", "graphics", false}
		case "addAudio", "updateAudio", "removeAudio":
			return "sound", kind{"changed", "sound", "sounds", false}
		case "lock":
			return "lock", kind{"locked", "part", "parts", false}
		case "unlock":
			return "unlock", kind{"unlocked", "part", "parts", false}
		case "setScript", "updateVoiceover", "addBeat", "updateBeat", "moveBeat", "removeBeat":
			return "narration", kind{"changed", "narration line", "narration lines", false}
		case "setMix":
			return "mix", kind{"changed", "sound mix", "sound mixes", false}
		case "setCaptions":
			return "captions", kind{"changed", "caption setting", "caption settings", false}
		case "setFixed":
			if op.Fixed == nil {
				return "original", kind{"switched", "piece of footage back to its original", "pieces of footage back to their originals", false}
			}
			return "fixed", kind{"switched", "piece of footage to its fixed copy", "pieces of footage to their fixed copies", false}
		}
		return "other", kind{"changed", "setting", "settings", false}
	}
	var order []string
	counts := map[string]int{}
	for _, op := range ops {
		k, info := key(op)
		if _, ok := counts[k]; !ok {
			order = append(order, k)
			kinds[k] = info
		}
		counts[k]++
	}
	if len(order) == 0 {
		return "No changes"
	}
	var parts []string
	clipNamed := false
	for _, k := range order {
		info, n := kinds[k], counts[k]
		noun := info.one
		if n != 1 {
			noun = info.many
		}
		switch {
		case info.clip && clipNamed:
			parts = append(parts, fmt.Sprintf("%s %d", info.verb, n))
		default:
			parts = append(parts, fmt.Sprintf("%s %d %s", info.verb, n, noun))
			clipNamed = clipNamed || info.clip
		}
	}
	s := strings.Join(parts, ", ")
	return strings.ToUpper(s[:1]) + s[1:]
}

// SplitPairs finds clips of next that are the second part of a clip of base cut
// in two: new clip ID → the clip it was split from. A second part is a new clip
// right after the first that continues the same footage at the same speed from
// where the first part now ends to where the clip ended; either part may have
// other changes too (they are field updates after the split).
func SplitPairs(base, next *Spec) map[string]string {
	baseClips := map[string]Clip{}
	for _, c := range base.Clips {
		baseClips[c.ID] = c
	}
	out := map[string]string{}
	for j := 1; j < len(next.Clips); j++ {
		nc, prev := next.Clips[j], next.Clips[j-1]
		if _, existed := baseClips[nc.ID]; existed {
			continue
		}
		b, ok := baseClips[prev.ID]
		if !ok || prev.Out <= b.In || prev.Out >= b.Out || nc.In != prev.Out || nc.Out != b.Out {
			continue
		}
		if nc.AssetID == b.AssetID && prev.AssetID == b.AssetID && speedOf(nc) == speedOf(b) && speedOf(prev) == speedOf(b) {
			out[nc.ID] = prev.ID
		}
	}
	return out
}
