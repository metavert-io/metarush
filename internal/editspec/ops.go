package editspec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
)

// ApplyOps applies typed patch operations to a copy of base. It returns the new
// spec and the set of item IDs (plus pseudo-IDs like "captions", "output") the ops
// touched — used for lock checks and for safe automatic rebasing.
func ApplyOps(base *Spec, ops []Op, allowUnlock bool) (*Spec, map[string]bool, error) {
	s := Clone(base)
	touched := map[string]bool{}
	clipIdx := func(id string) int {
		for i, c := range s.Clips {
			if c.ID == id {
				return i
			}
		}
		return -1
	}
	findLocked := func(id string) bool {
		for _, c := range s.Clips {
			if c.ID == id {
				return c.Locked
			}
		}
		for _, a := range s.Audio {
			if a.ID == id {
				return a.Locked
			}
		}
		for _, o := range s.Overlays {
			if o.ID == id {
				return o.Locked
			}
		}
		if id == "captions" && s.Captions != nil {
			return s.Captions.Locked
		}
		if s.Voiceover != nil {
			if id == "voiceover" {
				return s.Voiceover.Locked
			}
			for _, b := range s.Voiceover.Beats {
				if b.ID == id {
					return b.Locked || s.Voiceover.Locked
				}
			}
		}
		if id == "mix" && s.Mix != nil {
			return s.Mix.Locked
		}
		return false
	}
	beatIdx := func(id string) int {
		if s.Voiceover == nil {
			return -1
		}
		for i, b := range s.Voiceover.Beats {
			if b.ID == id {
				return i
			}
		}
		return -1
	}
	beatAt := func(op Op) (int, error) {
		n := len(s.Voiceover.Beats)
		switch {
		case op.After != "":
			i := beatIdx(op.After)
			if i < 0 {
				return 0, fmt.Errorf("there's no narration line “%s” to put it after", op.After)
			}
			return i + 1, nil
		case op.Before != "":
			i := beatIdx(op.Before)
			if i < 0 {
				return 0, fmt.Errorf("there's no narration line “%s” to put it before", op.Before)
			}
			return i, nil
		}
		return n, nil
	}
	voLocked := func(n int) error {
		if s.Voiceover != nil && s.Voiceover.Locked && !allowUnlock {
			return &LockedError{ID: "voiceover", Op: n}
		}
		return nil
	}
	insertAt := func(op Op) (int, error) {
		switch {
		case op.After != "":
			i := clipIdx(op.After)
			if i < 0 {
				return 0, fmt.Errorf("there's no clip “%s” to put it after", op.After)
			}
			return i + 1, nil
		case op.Before != "":
			i := clipIdx(op.Before)
			if i < 0 {
				return 0, fmt.Errorf("there's no clip “%s” to put it before", op.Before)
			}
			return i, nil
		default:
			return len(s.Clips), nil
		}
	}
	// cutChanged handles a change to the cut into clip i, however an op makes it
	// (setTransition, or a clip update, replacement or insert that carries
	// transitionIn): a transition that reaches into the previous clip (an
	// overlap, a dip, a split sound) changes how that clip ends, so a locked
	// previous clip refuses it; and the cut touches the clip before it and the
	// pair itself, so a concurrent change to either clip or a reorder conflicts.
	cutChanged := func(n, i int, was, now *Transition) error {
		if equalJSON(was, now) {
			return nil
		}
		if i > 0 && s.Clips[i-1].Locked && !allowUnlock && (reachesBack(was) || reachesBack(now)) {
			return &LockedError{ID: s.Clips[i-1].ID, Op: n}
		}
		touched[boundaryID(s, i)] = true
		if i > 0 {
			touched[s.Clips[i-1].ID] = true
		}
		return nil
	}
	for n, op := range ops {
		// Plan-authoring problems: the change (its number and kind) and what is wrong,
		// naming plan fields in parentheses; plainOpError keeps only the sentence.
		fail := func(f string, a ...any) error {
			return fmt.Errorf("Change %d (%s): %s", n+1, op.Op, fmt.Sprintf(f, a...))
		}
		if op.ID != "" && op.Op != "unlock" && op.Op != "lock" && findLocked(op.ID) && !allowUnlock {
			return nil, nil, &LockedError{ID: op.ID, Op: n}
		}
		switch op.Op {
		case "insertClip":
			if op.Clip == nil {
				return nil, nil, fail("it has no clip to add (clip)")
			}
			i, err := insertAt(op)
			if err != nil {
				return nil, nil, fail("%v", err)
			}
			s.Clips = append(s.Clips[:i], append([]Clip{*op.Clip}, s.Clips[i:]...)...)
			touched[op.Clip.ID] = true
			if err := cutChanged(n, i, nil, op.Clip.Transition); err != nil {
				return nil, nil, err
			}
		case "removeClip":
			i := clipIdx(op.ID)
			if i < 0 {
				return nil, nil, fail("there's no clip “%s” in the cut", op.ID)
			}
			for _, id := range narrowSpans(s, i) {
				touched[id] = true
			}
			s.Clips = append(s.Clips[:i], s.Clips[i+1:]...)
			touched[op.ID] = true
		case "replaceClip":
			i := clipIdx(op.ID)
			if i < 0 || op.Clip == nil {
				return nil, nil, fail("it needs the name of a clip in the cut and the clip that replaces it (id and clip)")
			}
			was := s.Clips[i].Transition
			s.Clips[i] = *op.Clip
			touched[op.ID], touched[op.Clip.ID] = true, true
			if err := cutChanged(n, i, was, op.Clip.Transition); err != nil {
				return nil, nil, err
			}
		case "updateClip":
			i := clipIdx(op.ID)
			if i < 0 {
				return nil, nil, fail("there's no clip “%s” in the cut", op.ID)
			}
			was := s.Clips[i].Transition
			if err := mergeInto(&s.Clips[i], op.Set); err != nil {
				return nil, nil, fail("%v", err)
			}
			touched[op.ID] = true
			if err := cutChanged(n, i, was, s.Clips[i].Transition); err != nil {
				return nil, nil, err
			}
		case "moveClip":
			i := clipIdx(op.ID)
			if i < 0 {
				return nil, nil, fail("there's no clip “%s” in the cut", op.ID)
			}
			c := s.Clips[i]
			s.Clips = append(s.Clips[:i], s.Clips[i+1:]...)
			j, err := insertAt(op)
			if err != nil {
				return nil, nil, fail("%v", err)
			}
			s.Clips = append(s.Clips[:j], append([]Clip{c}, s.Clips[j:]...)...)
			touched[op.ID], touched["order"] = true, true
		case "addOverlay":
			if op.Overlay == nil {
				return nil, nil, fail("it has no graphic to add (overlay)")
			}
			s.Overlays = append(s.Overlays, *op.Overlay)
			touched[op.Overlay.ID] = true
		case "updateOverlay":
			i := indexOf(len(s.Overlays), func(k int) bool { return s.Overlays[k].ID == op.ID })
			if i < 0 {
				return nil, nil, fail("there's no graphic “%s” in the cut", op.ID)
			}
			if err := mergeInto(&s.Overlays[i], op.Set); err != nil {
				return nil, nil, fail("%v", err)
			}
			touched[op.ID] = true
		case "removeOverlay":
			i := indexOf(len(s.Overlays), func(k int) bool { return s.Overlays[k].ID == op.ID })
			if i < 0 {
				return nil, nil, fail("there's no graphic “%s” in the cut", op.ID)
			}
			s.Overlays = append(s.Overlays[:i], s.Overlays[i+1:]...)
			touched[op.ID] = true
		case "addAudio":
			if op.Track == nil {
				return nil, nil, fail("it has no sound to add (track)")
			}
			s.Audio = append(s.Audio, *op.Track)
			touched[op.Track.ID] = true
		case "updateAudio":
			i := indexOf(len(s.Audio), func(k int) bool { return s.Audio[k].ID == op.ID })
			if i < 0 {
				return nil, nil, fail("there's no sound “%s” in the cut", op.ID)
			}
			if err := mergeInto(&s.Audio[i], op.Set); err != nil {
				return nil, nil, fail("%v", err)
			}
			touched[op.ID] = true
		case "removeAudio":
			i := indexOf(len(s.Audio), func(k int) bool { return s.Audio[k].ID == op.ID })
			if i < 0 {
				return nil, nil, fail("there's no sound “%s” in the cut", op.ID)
			}
			s.Audio = append(s.Audio[:i], s.Audio[i+1:]...)
			touched[op.ID] = true
		case "setCaptions":
			if s.Captions != nil && s.Captions.Locked && !allowUnlock {
				return nil, nil, &LockedError{ID: "captions", Op: n}
			}
			s.Captions = op.Captions
			touched["captions"] = true
		case "setOutput":
			if op.Output == nil {
				return nil, nil, fail("it doesn't say the video's new size and shape (output)")
			}
			s.Output = *op.Output
			touched["output"] = true
		case "setBrand":
			s.Brand = op.Brand
			touched["brand"] = true
		case "setResource":
			if !ResourceNamePattern.MatchString(op.ID) {
				return nil, nil, fail("%q can't be a resource's short name (lowercase letters, digits, - and _, starting with a letter)", op.ID)
			}
			if op.Resource == nil {
				if _, ok := s.Resources[op.ID]; !ok {
					return nil, nil, fail("there's no resource “%s” in this cut", op.ID)
				}
				delete(s.Resources, op.ID)
				if len(s.Resources) == 0 {
					s.Resources = nil
				}
			} else {
				if s.Resources == nil {
					s.Resources = map[string]ResourcePin{}
				}
				s.Resources[op.ID] = *op.Resource
			}
			touched["resource:"+op.ID] = true
		case "setConversation":
			if !ConversationNamePattern.MatchString(op.ID) {
				return nil, nil, fail("%q can't be a conversation's short name (lowercase letters, digits, - and _, starting with a letter)", op.ID)
			}
			if op.Conversation == nil {
				if _, ok := s.Conversations[op.ID]; !ok {
					return nil, nil, fail("no conversation %q in this cut", op.ID)
				}
				delete(s.Conversations, op.ID)
				if len(s.Conversations) == 0 {
					s.Conversations = nil
				}
			} else {
				if s.Conversations == nil {
					s.Conversations = map[string]ConversationPin{}
				}
				s.Conversations[op.ID] = *op.Conversation
			}
			touched["conversation:"+op.ID] = true
		case "setFixed":
			if !FixedFootagePattern.MatchString(op.ID) {
				return nil, nil, fail("%q isn't footage (setFixed names the footage by its ID, ast_…)", op.ID)
			}
			if op.Fixed == nil {
				if _, ok := s.Fixed[op.ID]; !ok {
					return nil, nil, fail("the cut already plays the original of that footage")
				}
				delete(s.Fixed, op.ID)
				if len(s.Fixed) == 0 {
					s.Fixed = nil
				}
			} else {
				if !shaHexRe.MatchString(op.Fixed.SHA256) {
					return nil, nil, fail("a fixed copy is named by its SHA-256 (64 hexadecimal characters)")
				}
				if s.Fixed == nil {
					s.Fixed = map[string]FixedPin{}
				}
				s.Fixed[op.ID] = *op.Fixed
			}
			touched["fixed:"+op.ID] = true
		case "setNotes":
			if op.Text != nil {
				s.Notes = *op.Text
			}
			touched["notes"] = true
		case "setTitle":
			if op.Text != nil {
				s.Title = *op.Text
			}
			touched["title"] = true
		case "setScript":
			if err := voLocked(n); err != nil {
				return nil, nil, err
			}
			if s.Voiceover != nil {
				for _, b := range s.Voiceover.Beats {
					touched[b.ID] = true
				}
			}
			if op.Voiceover != nil {
				v := *op.Voiceover
				v.Beats = append([]Beat(nil), op.Voiceover.Beats...)
				for _, b := range v.Beats {
					touched[b.ID] = true
				}
				s.Voiceover = &v
			} else {
				s.Voiceover = nil
			}
			touched["voiceover"], touched["beats-order"] = true, true
		case "updateVoiceover":
			if err := voLocked(n); err != nil {
				return nil, nil, err
			}
			var keys map[string]json.RawMessage
			if json.Unmarshal(op.Set, &keys) == nil {
				if _, ok := keys["beats"]; ok {
					return nil, nil, fail("the narration lines can't be changed this way (use setScript, or addBeat, updateBeat, moveBeat and removeBeat)")
				}
			}
			if s.Voiceover == nil {
				s.Voiceover = &Voiceover{Beats: []Beat{}}
			}
			if err := mergeInto(s.Voiceover, op.Set); err != nil {
				return nil, nil, fail("%v", err)
			}
			touched["voiceover"] = true
		case "addBeat":
			if op.Beat == nil {
				return nil, nil, fail("it has no narration line to add (beat)")
			}
			if err := voLocked(n); err != nil {
				return nil, nil, err
			}
			if s.Voiceover == nil {
				s.Voiceover = &Voiceover{Beats: []Beat{}}
			}
			if beatIdx(op.Beat.ID) >= 0 {
				return nil, nil, fail("there's already a narration line called “%s”", op.Beat.ID)
			}
			i, err := beatAt(op)
			if err != nil {
				return nil, nil, fail("%v", err)
			}
			bs := s.Voiceover.Beats
			s.Voiceover.Beats = append(bs[:i:i], append([]Beat{*op.Beat}, bs[i:]...)...)
			touched[op.Beat.ID], touched["beats-order"] = true, true
		case "updateBeat":
			i := beatIdx(op.ID)
			if i < 0 {
				return nil, nil, fail("there's no narration line “%s”", op.ID)
			}
			if err := mergeInto(&s.Voiceover.Beats[i], op.Set); err != nil {
				return nil, nil, fail("%v", err)
			}
			touched[op.ID] = true
		case "moveBeat":
			i := beatIdx(op.ID)
			if i < 0 {
				return nil, nil, fail("there's no narration line “%s”", op.ID)
			}
			if op.Anchor == nil && op.After == "" && op.Before == "" {
				return nil, nil, fail("it doesn't say where the line goes (anchor, or after or before to reorder the script)")
			}
			if op.Anchor != nil {
				s.Voiceover.Beats[i].Anchor = *op.Anchor
			}
			if op.After != "" || op.Before != "" {
				b := s.Voiceover.Beats[i]
				s.Voiceover.Beats = append(s.Voiceover.Beats[:i], s.Voiceover.Beats[i+1:]...)
				j, err := beatAt(op)
				if err != nil {
					return nil, nil, fail("%v", err)
				}
				bs := s.Voiceover.Beats
				s.Voiceover.Beats = append(bs[:j:j], append([]Beat{b}, bs[j:]...)...)
				touched["beats-order"] = true
			}
			touched[op.ID] = true
		case "removeBeat":
			i := beatIdx(op.ID)
			if i < 0 {
				return nil, nil, fail("there's no narration line “%s”", op.ID)
			}
			s.Voiceover.Beats = append(s.Voiceover.Beats[:i], s.Voiceover.Beats[i+1:]...)
			touched[op.ID], touched["beats-order"] = true, true
		case "setMix":
			if s.Mix != nil && s.Mix.Locked && !allowUnlock {
				return nil, nil, &LockedError{ID: "mix", Op: n}
			}
			if op.Mix != nil {
				m := *op.Mix
				s.Mix = &m
			} else {
				s.Mix = nil
			}
			touched["mix"] = true
		case "setTransition":
			i := clipIdx(op.ID)
			if i < 0 {
				return nil, nil, fail("there's no clip “%s” in the cut", op.ID)
			}
			if op.Edge == "end" {
				// The clip's fade-out (the end of the whole video when it is last).
				fade := 0.0
				if t := op.Transition; t != nil {
					if t.Type != TransitionFade && t.Type != TransitionDip && t.Type != TransitionCut {
						return nil, nil, fail("a clip can end with a fade out or a fade through black (type fade or dip-to-black), not %q", t.Type)
					}
					if t.Type != TransitionCut {
						fade = t.Duration
					}
				}
				s.Clips[i].FadeOut = round3(fade)
				touched[op.ID] = true
				touched[boundaryID(s, i+1)] = true
				break
			}
			if op.Edge != "" && op.Edge != "in" {
				return nil, nil, fail("edge is in (the cut into the clip) or end (its fade-out)")
			}
			t := SetTransitionValue(op.Transition)
			if err := cutChanged(n, i, s.Clips[i].Transition, t); err != nil {
				return nil, nil, err
			}
			s.Clips[i].Transition = t
			// The op names one cut, so it always touches the pair (even unchanged).
			touched[op.ID] = true
			touched[boundaryID(s, i)] = true
			if i > 0 {
				touched[s.Clips[i-1].ID] = true
			}
		case "splitClip", "trimClip", "slipClip", "deleteRange", "removeWords":
			// The timeline's gestures (studio.go): source times on one clip, or a
			// stretch of the timeline; locks of every clip they change are checked.
			ids, err := applyStudioOp(s, n, op, allowUnlock)
			var le *LockedError
			if errors.As(err, &le) {
				return nil, nil, err
			}
			if err != nil {
				return nil, nil, fail("%v", err)
			}
			for _, id := range ids {
				touched[id] = true
			}
		case "lock", "unlock":
			if op.Op == "unlock" && !allowUnlock {
				return nil, nil, fail("unlocking something needs the person's OK first (allowUnlock)")
			}
			if !setLock(s, op.ID, op.Op == "lock") {
				return nil, nil, fail("there's nothing called “%s” in the cut", op.ID)
			}
			touched[op.ID] = true
		default:
			return nil, nil, fail("MetaRush doesn't know this kind of change")
		}
	}
	return s, touched, nil
}

// SetTransitionValue is the transition a setTransition op stores for t: nil for
// a plain cut (no length, direction, easing or sound), otherwise a copy with the
// type filled in and the length rounded to milliseconds.
func SetTransitionValue(t *Transition) *Transition {
	if t == nil || (t.Type == TransitionCut && t.Audio == nil && t.Duration == 0 && t.Direction == "" && t.Easing == "") {
		return nil
	}
	x := *t
	if t.Audio != nil {
		a := *t.Audio
		if t.Audio.Blend != nil {
			b := *t.Audio.Blend
			a.Blend = &b
		}
		x.Audio = &a
	}
	if x.Type == "" {
		x.Type = TransitionCut
	}
	x.Duration = round3(x.Duration)
	return &x
}

// reachesBack reports whether a transition changes how the previous clip plays
// (its last frames or its sound), not only the clip it leads into.
func reachesBack(t *Transition) bool {
	if t == nil {
		return false
	}
	if t.Audio != nil && t.Audio.Mode != "" {
		return true
	}
	return IsOverlapTransition(t.Type) || IsDipTransition(t.Type)
}

// boundaryID names the cut into clip i ("cut:<previous>><clip>"; "^" before the
// first clip, "$" after the last). Rebase compares them so a transition written
// for one pair of shots never silently lands between two others.
func boundaryID(s *Spec, i int) string {
	prev, next := "^", "$"
	if i-1 >= 0 && i-1 < len(s.Clips) {
		prev = s.Clips[i-1].ID
	}
	if i >= 0 && i < len(s.Clips) {
		next = s.Clips[i].ID
	}
	return "cut:" + prev + ">" + next
}

// LockedError reports an attempt to modify a locked item.
type LockedError struct {
	ID string
	Op int
}

// LockedWords names a lockable part of a cut by its ID in plain words: "the
// sound mix", "the narration", "the captions", or the item's name in quotes.
func LockedWords(id string) string {
	switch id {
	case "mix":
		return "the sound mix"
	case "voiceover":
		return "the narration"
	case "captions":
		return "the captions"
	case "":
		return "something"
	}
	return "“" + id + "”"
}

func (e *LockedError) Error() string {
	return fmt.Sprintf("This change touches %s, which is locked", LockedWords(e.ID))
}

func setLock(s *Spec, id string, v bool) bool {
	for i := range s.Clips {
		if s.Clips[i].ID == id {
			s.Clips[i].Locked = v
			return true
		}
	}
	for i := range s.Audio {
		if s.Audio[i].ID == id {
			s.Audio[i].Locked = v
			return true
		}
	}
	for i := range s.Overlays {
		if s.Overlays[i].ID == id {
			s.Overlays[i].Locked = v
			return true
		}
	}
	if id == "captions" && s.Captions != nil {
		s.Captions.Locked = v
		return true
	}
	if s.Voiceover != nil {
		if id == "voiceover" {
			s.Voiceover.Locked = v
			return true
		}
		for i := range s.Voiceover.Beats {
			if s.Voiceover.Beats[i].ID == id {
				s.Voiceover.Beats[i].Locked = v
				return true
			}
		}
	}
	if id == "mix" && s.Mix != nil {
		s.Mix.Locked = v
		return true
	}
	return false
}

func indexOf(n int, f func(int) bool) int {
	for i := 0; i < n; i++ {
		if f(i) {
			return i
		}
	}
	return -1
}

// mergeInto applies set to dst (a pointer to a struct) as a JSON merge patch
// (RFC 7386): given fields replace the old ones, nested objects are merged
// field by field, lists are replaced, and null resets a field to its default.
// Unknown fields are rejected; "id" cannot be changed. The result is decoded
// into a fresh value, so nothing stale survives from the old item.
func mergeInto(dst any, set json.RawMessage) error {
	if len(set) == 0 {
		return fmt.Errorf("it doesn't say what to change (set)")
	}
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(set, &patch); err != nil {
		return fmt.Errorf("what to change has to be a group of settings (set: %v)", err)
	}
	if _, ok := patch["id"]; ok {
		return fmt.Errorf("a change can't rename what it changes (set.id)")
	}
	cur, _ := json.Marshal(dst)
	merged := MergePatch(cur, set)
	fresh := reflect.New(reflect.TypeOf(dst).Elem())
	dec := json.NewDecoder(bytes.NewReader(merged))
	dec.DisallowUnknownFields()
	if err := dec.Decode(fresh.Interface()); err != nil {
		return err
	}
	reflect.ValueOf(dst).Elem().Set(fresh.Elem())
	return nil
}

// MergePatch applies an RFC 7386 JSON merge patch to target.
func MergePatch(target, patch json.RawMessage) json.RawMessage {
	var pm map[string]json.RawMessage
	if json.Unmarshal(patch, &pm) != nil || pm == nil {
		return patch // not an object: the patch replaces the value
	}
	var tm map[string]json.RawMessage
	if json.Unmarshal(target, &tm) != nil || tm == nil {
		tm = map[string]json.RawMessage{}
	}
	for k, v := range pm {
		if string(bytes.TrimSpace(v)) == "null" {
			delete(tm, k)
			continue
		}
		tm[k] = MergePatch(tm[k], v)
	}
	out, _ := json.Marshal(tm)
	return out
}

// ChangedIDs lists item IDs (and pseudo-IDs) that differ between two specs.
func ChangedIDs(a, b *Spec) map[string]bool {
	out := map[string]bool{}
	cmp := func(am, bm map[string]any) {
		for id, av := range am {
			if bv, ok := bm[id]; !ok || !equalJSON(av, bv) {
				out[id] = true
			}
		}
		for id := range bm {
			if _, ok := am[id]; !ok {
				out[id] = true
			}
		}
	}
	ca, cb := map[string]any{}, map[string]any{}
	var oa, ob []string
	for _, c := range a.Clips {
		ca[c.ID] = c
		oa = append(oa, c.ID)
	}
	for _, c := range b.Clips {
		cb[c.ID] = c
		ob = append(ob, c.ID)
	}
	cmp(ca, cb)
	// Where each clip starts on the timeline: deleteRange is written in timeline
	// seconds, so it conflicts when an earlier change moved the clips it cuts.
	if len(a.Clips) > 0 && len(b.Clips) > 0 {
		sa, sb := clipStarts(a), clipStarts(b)
		for id, fa := range sa {
			if fb, ok := sb[id]; ok && fa != fb {
				out["at:"+id] = true
			}
		}
	}
	if !equalJSON(oa, ob) {
		out["order"] = true
		// Which shots are next to each other changed: those cuts are different cuts.
		ba, bb := map[string]bool{}, map[string]bool{}
		for i := 0; i <= len(a.Clips); i++ {
			ba[boundaryID(a, i)] = true
		}
		for i := 0; i <= len(b.Clips); i++ {
			bb[boundaryID(b, i)] = true
		}
		for id := range ba {
			if !bb[id] {
				out[id] = true
			}
		}
		for id := range bb {
			if !ba[id] {
				out[id] = true
			}
		}
	}
	aa, ab := map[string]any{}, map[string]any{}
	for _, t := range a.Audio {
		aa[t.ID] = t
	}
	for _, t := range b.Audio {
		ab[t.ID] = t
	}
	cmp(aa, ab)
	va, vb := map[string]any{}, map[string]any{}
	for _, o := range a.Overlays {
		va[o.ID] = o
	}
	for _, o := range b.Overlays {
		vb[o.ID] = o
	}
	cmp(va, vb)
	if !equalJSON(a.Captions, b.Captions) {
		out["captions"] = true
	}
	ba, bb := map[string]any{}, map[string]any{}
	var boa, bob []string
	if a.Voiceover != nil {
		for _, x := range a.Voiceover.Beats {
			ba[x.ID] = x
			boa = append(boa, x.ID)
		}
	}
	if b.Voiceover != nil {
		for _, x := range b.Voiceover.Beats {
			bb[x.ID] = x
			bob = append(bob, x.ID)
		}
	}
	cmp(ba, bb)
	if !equalJSON(boa, bob) {
		out["beats-order"] = true
	}
	if !equalJSON(voiceoverShell(a.Voiceover), voiceoverShell(b.Voiceover)) {
		out["voiceover"] = true
	}
	if !equalJSON(a.Mix, b.Mix) {
		out["mix"] = true
	}
	if !equalJSON(a.Output, b.Output) {
		out["output"] = true
	}
	if !equalJSON(a.Brand, b.Brand) {
		out["brand"] = true
	}
	for n, p := range a.Resources {
		if q, ok := b.Resources[n]; !ok || q != p {
			out["resource:"+n] = true
		}
	}
	for n := range b.Resources {
		if _, ok := a.Resources[n]; !ok {
			out["resource:"+n] = true
		}
	}
	for id, p := range a.Fixed {
		if q, ok := b.Fixed[id]; !ok || q != p {
			out["fixed:"+id] = true
		}
	}
	for id := range b.Fixed {
		if _, ok := a.Fixed[id]; !ok {
			out["fixed:"+id] = true
		}
	}
	if a.Notes != b.Notes {
		out["notes"] = true
	}
	if a.Title != b.Title {
		out["title"] = true
	}
	return out
}

// voiceoverShell is the voiceover without its beats (settings, layout, continuous, lock).
func voiceoverShell(v *Voiceover) *Voiceover {
	if v == nil {
		return nil
	}
	c := *v
	c.Beats = nil
	return &c
}

// Rebase re-applies ops written against base onto head when they touch nothing
// that changed between base and head. Clip insertion order changes conflict with
// moves; anything else overlapping is a conflict.
func Rebase(base, head *Spec, ops []Op, allowUnlock bool) (*Spec, []string, error) {
	_, touched, err := ApplyOps(base, ops, allowUnlock)
	if err != nil {
		return nil, nil, err
	}
	changed := ChangedIDs(base, head)
	var conflicts []string
	for id := range touched {
		if changed[id] {
			conflicts = append(conflicts, id)
		}
	}
	if touched["order"] && changed["order"] {
		conflicts = append(conflicts, "order")
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return nil, dedupe(conflicts), nil
	}
	out, _, err := ApplyOps(head, ops, allowUnlock)
	return out, nil, err
}

func dedupe(in []string) []string {
	var out []string
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// Diff is a reviewer-friendly comparison between two specs.
type Diff struct {
	DurationBefore  float64     `json:"durationBefore"`
	DurationAfter   float64     `json:"durationAfter"`
	ClipsAdded      []string    `json:"clipsAdded"`
	ClipsRemoved    []string    `json:"clipsRemoved"`
	ClipsChanged    []FieldDiff `json:"clipsChanged"`
	Reordered       bool        `json:"reordered"`
	OverlaysAdded   []string    `json:"overlaysAdded"`
	OverlaysRemoved []string    `json:"overlaysRemoved"`
	OverlaysChanged []FieldDiff `json:"overlaysChanged"`
	AudioAdded      []string    `json:"audioAdded"`
	AudioRemoved    []string    `json:"audioRemoved"`
	AudioChanged    []FieldDiff `json:"audioChanged"`
	CaptionsChanged bool        `json:"captionsChanged"`
	OutputChanged   bool        `json:"outputChanged"`
	BrandChanged    bool        `json:"brandChanged"`
	NotesChanged    bool        `json:"notesChanged"`
	BeatsAdded      []string    `json:"beatsAdded"`
	BeatsRemoved    []string    `json:"beatsRemoved"`
	BeatsChanged    []FieldDiff `json:"beatsChanged"`
	BeatsReordered  bool        `json:"beatsReordered"`
	// VoiceoverChanged covers narration settings, layout and the continuous recording.
	VoiceoverChanged bool     `json:"voiceoverChanged"`
	MixChanged       bool     `json:"mixChanged"`
	Summary          []string `json:"summary"`
}

// FieldDiff lists changed fields of one item.
type FieldDiff struct {
	ID     string   `json:"id"`
	Fields []string `json:"fields"`
}

// Compare diffs a against b (a = before).
func Compare(a, b *Spec) Diff {
	d := Diff{ClipsAdded: []string{}, ClipsRemoved: []string{}, ClipsChanged: []FieldDiff{}, OverlaysAdded: []string{}, OverlaysRemoved: []string{}, OverlaysChanged: []FieldDiff{}, AudioAdded: []string{}, AudioRemoved: []string{}, AudioChanged: []FieldDiff{},
		BeatsAdded: []string{}, BeatsRemoved: []string{}, BeatsChanged: []FieldDiff{}, Summary: []string{}}
	_, ta := Timeline(a)
	_, tb := Timeline(b)
	d.DurationBefore, d.DurationAfter = float64(ta)/float64(a.Output.FPS), float64(tb)/float64(b.Output.FPS)
	diffItems := func(am, bm map[string]any, order []string, added, removed *[]string, changed *[]FieldDiff) {
		for _, id := range order {
			av, inA := am[id]
			bv, inB := bm[id]
			switch {
			case inA && !inB:
				*removed = append(*removed, id)
			case !inA && inB:
				*added = append(*added, id)
			case !equalJSON(av, bv):
				*changed = append(*changed, FieldDiff{ID: id, Fields: changedFields(av, bv)})
			}
		}
	}
	ca, cb, order := map[string]any{}, map[string]any{}, []string{}
	var oa, ob []string
	for _, c := range a.Clips {
		ca[c.ID] = c
		oa = append(oa, c.ID)
		order = append(order, c.ID)
	}
	for _, c := range b.Clips {
		cb[c.ID] = c
		ob = append(ob, c.ID)
		if _, ok := ca[c.ID]; !ok {
			order = append(order, c.ID)
		}
	}
	diffItems(ca, cb, order, &d.ClipsAdded, &d.ClipsRemoved, &d.ClipsChanged)
	common := func(xs []string, keep map[string]any) []string {
		var out []string
		for _, x := range xs {
			if _, ok := keep[x]; ok {
				out = append(out, x)
			}
		}
		return out
	}
	d.Reordered = !equalJSON(common(oa, cb), common(ob, ca))
	va, vb, vo := map[string]any{}, map[string]any{}, []string{}
	for _, o := range a.Overlays {
		va[o.ID] = o
		vo = append(vo, o.ID)
	}
	for _, o := range b.Overlays {
		vb[o.ID] = o
		if _, ok := va[o.ID]; !ok {
			vo = append(vo, o.ID)
		}
	}
	diffItems(va, vb, vo, &d.OverlaysAdded, &d.OverlaysRemoved, &d.OverlaysChanged)
	aa, ab, ao := map[string]any{}, map[string]any{}, []string{}
	for _, t := range a.Audio {
		aa[t.ID] = t
		ao = append(ao, t.ID)
	}
	for _, t := range b.Audio {
		ab[t.ID] = t
		if _, ok := aa[t.ID]; !ok {
			ao = append(ao, t.ID)
		}
	}
	diffItems(aa, ab, ao, &d.AudioAdded, &d.AudioRemoved, &d.AudioChanged)
	bta, btb, bto := map[string]any{}, map[string]any{}, []string{}
	var boa, bob []string
	if a.Voiceover != nil {
		for _, x := range a.Voiceover.Beats {
			bta[x.ID] = x
			bto = append(bto, x.ID)
			boa = append(boa, x.ID)
		}
	}
	if b.Voiceover != nil {
		for _, x := range b.Voiceover.Beats {
			btb[x.ID] = x
			bob = append(bob, x.ID)
			if _, ok := bta[x.ID]; !ok {
				bto = append(bto, x.ID)
			}
		}
	}
	diffItems(bta, btb, bto, &d.BeatsAdded, &d.BeatsRemoved, &d.BeatsChanged)
	d.BeatsReordered = !equalJSON(common(boa, btb), common(bob, bta))
	d.VoiceoverChanged = !equalJSON(voiceoverShell(a.Voiceover), voiceoverShell(b.Voiceover))
	d.MixChanged = !equalJSON(a.Mix, b.Mix)
	d.CaptionsChanged = !equalJSON(a.Captions, b.Captions)
	d.OutputChanged = !equalJSON(a.Output, b.Output)
	d.BrandChanged = !equalJSON(a.Brand, b.Brand)
	d.NotesChanged = a.Notes != b.Notes
	// People read these lines (the version page, action cards); the dashboard
	// shows the length on its own and leaves out the line starting "duration ".
	if dd := d.DurationAfter - d.DurationBefore; math.Abs(dd) >= 0.05 {
		way := "longer"
		if dd < 0 {
			way = "shorter"
		}
		d.Summary = append(d.Summary, fmt.Sprintf("duration %.1f s → %.1f s (%.1f s %s)", d.DurationBefore, d.DurationAfter, math.Abs(dd), way))
	}
	add := func(n int, one, what string) {
		if n > 0 {
			d.Summary = append(d.Summary, plural(n, one)+" "+what)
		}
	}
	add(len(d.ClipsAdded), "clip", "added")
	add(len(d.ClipsRemoved), "clip", "removed")
	add(len(d.ClipsChanged), "clip", "changed")
	add(len(d.OverlaysAdded)+len(d.OverlaysRemoved)+len(d.OverlaysChanged), "graphic", "changed")
	add(len(d.AudioAdded)+len(d.AudioRemoved)+len(d.AudioChanged), "sound", "changed")
	if d.Reordered {
		d.Summary = append(d.Summary, "clips reordered")
	}
	trans := 0
	for _, ch := range d.ClipsChanged {
		for _, f := range ch.Fields {
			if f == "transitionIn" {
				trans++
			}
		}
	}
	if trans > 0 {
		d.Summary = append(d.Summary, plural(trans, "transition")+" changed")
	}
	if n := len(d.BeatsAdded); n > 0 {
		d.Summary = append(d.Summary, plural(n, "narration line")+" added")
	}
	if n := len(d.BeatsRemoved); n > 0 {
		d.Summary = append(d.Summary, plural(n, "narration line")+" removed")
	}
	if n := len(d.BeatsChanged); n > 0 {
		d.Summary = append(d.Summary, plural(n, "narration line")+" changed")
	}
	if d.BeatsReordered {
		d.Summary = append(d.Summary, "narration lines reordered")
	}
	if d.VoiceoverChanged {
		d.Summary = append(d.Summary, "narration settings changed")
	}
	if d.MixChanged {
		d.Summary = append(d.Summary, "sound mix changed")
	}
	if d.CaptionsChanged {
		d.Summary = append(d.Summary, "captions changed")
	}
	if d.OutputChanged {
		d.Summary = append(d.Summary, "video size or format changed")
	}
	if len(d.Summary) == 0 {
		d.Summary = append(d.Summary, "no changes")
	}
	return d
}

func changedFields(a, b any) []string {
	var am, bm map[string]json.RawMessage
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	_ = json.Unmarshal(ab, &am)
	_ = json.Unmarshal(bb, &bm)
	keys := map[string]bool{}
	for k := range am {
		keys[k] = true
	}
	for k := range bm {
		keys[k] = true
	}
	var out []string
	for k := range keys {
		if !bytes.Equal(am[k], bm[k]) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// plural counts things in words ("1 narration line", "3 narration lines").
func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %ss", n, one)
}
