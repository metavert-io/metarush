package editspec

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// DiffOps returns typed patch operations that turn base into next, so that
// ApplyOps(base, ops, true) reproduces next exactly (same Hash). Both specs
// should be normalized.
//
// The ops are kept small per item so that a change written against an older
// head can rebase onto a newer one when the two touched different things:
// one updateClip/updateAudio/updateOverlay/updateBeat with only the changed
// fields per changed item, a setTransition per changed cut into an existing
// clip, moveClip/moveBeat only for items that left the
// longest common order (locked ones are kept in place whenever possible),
// updateVoiceover for the narration's settings, setMix for the mix, and
// lock/unlock for lock changes. Unlock ops come first and lock ops last, so a
// plan that unlocks, edits and re-locks an item applies in one pass (unlocking
// still needs the user's explicit permission at apply time). A clip cut in two
// (the second part exactly as splitClip makes it) becomes a splitClip; should
// that ever fail to reproduce the edit, the plain field updates are used.
func DiffOps(base, next *Spec) ([]Op, error) {
	if ops, err := diffOps(base, next, true); err == nil {
		return ops, nil
	}
	return diffOps(base, next, false)
}

// unionNames lists the short names pinned in either spec, sorted.
func unionNames(a, b map[string]ResourcePin) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range []map[string]ResourcePin{a, b} {
		for n := range m {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

// unionConversations lists the conversation names pinned in either spec, sorted.
func unionConversations(a, b map[string]ConversationPin) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range []map[string]ConversationPin{a, b} {
		for n := range m {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

// unionFixed lists the footage with a fixed copy pinned in either spec, sorted.
func unionFixed(a, b map[string]FixedPin) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range []map[string]FixedPin{a, b} {
		for n := range m {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

func diffOps(base, next *Spec, splits bool) ([]Op, error) {
	var unlocks, ops, locks []Op
	lockChange := func(id string, was, is bool) {
		switch {
		case was && !is:
			unlocks = append(unlocks, Op{Op: "unlock", ID: id})
		case !was && is:
			locks = append(locks, Op{Op: "lock", ID: id})
		}
	}

	// --- clips: removals, splits, field updates, then order (inserts and moves) ---
	baseClips := map[string]Clip{}
	var baseOrder []string
	for _, c := range base.Clips {
		baseClips[c.ID] = c
		baseOrder = append(baseOrder, c.ID)
	}
	nextClips := map[string]bool{}
	for _, c := range next.Clips {
		nextClips[c.ID] = true
	}
	for _, c := range base.Clips {
		if !nextClips[c.ID] {
			ops = append(ops, Op{Op: "removeClip", ID: c.ID})
		}
	}
	if splits {
		pairs := SplitPairs(base, next)
		for _, nc := range next.Clips {
			orig, ok := pairs[nc.ID]
			if !ok || baseClips[orig].Locked {
				continue
			}
			left, right := splitParts(baseClips[orig], nc.In, nc.ID)
			baseClips[orig], baseClips[nc.ID] = left, right
			at := nc.In
			ops = append(ops, Op{Op: "splitClip", ID: orig, At: &at, NewID: nc.ID})
			i := indexOfString(baseOrder, orig)
			baseOrder = append(baseOrder[:i+1], append([]string{nc.ID}, baseOrder[i+1:]...)...)
		}
	}
	var cuts []Op // setTransition ops, added once the clip order is final
	for _, c := range next.Clips {
		b, ok := baseClips[c.ID]
		if !ok || equalJSON(b, c) {
			continue
		}
		// A changed cut into the clip is its own setTransition (so the clip before
		// it is covered by locks and conflicts too) whenever that op stores exactly
		// this transition; otherwise it stays a field of the update.
		skip := []string{"id", "locked"}
		var cut *Op
		if !equalJSON(b.Transition, c.Transition) && equalJSON(SetTransitionValue(c.Transition), c.Transition) {
			cut = &Op{Op: "setTransition", ID: c.ID, Transition: SetTransitionValue(c.Transition)}
			skip = append(skip, "transitionIn")
		}
		set, err := fieldPatch(b, c, skip...)
		if err == errNotPatchable {
			cc := c
			ops = append(ops, Op{Op: "replaceClip", ID: c.ID, Clip: &cc})
			continue
		}
		if err != nil {
			return nil, err
		}
		if set != nil {
			ops = append(ops, Op{Op: "updateClip", ID: c.ID, Set: set})
		}
		if cut != nil {
			cuts = append(cuts, *cut)
		}
		lockChange(c.ID, b.Locked, c.Locked)
	}
	var cur []string // simulated clip order after removals (and splits)
	weight := map[string]int{}
	for _, id := range baseOrder {
		if nextClips[id] {
			cur = append(cur, id)
			weight[id] = 1
			if baseClips[id].Locked {
				weight[id] = 1000 // prefer moving unlocked clips around locked ones
			}
		}
	}
	var common []string
	for _, c := range next.Clips {
		if _, ok := baseClips[c.ID]; ok {
			common = append(common, c.ID)
		}
	}
	keep := weightedLCS(cur, common, weight)
	for j, c := range next.Clips {
		if keep[c.ID] {
			continue
		}
		_, existed := baseClips[c.ID]
		pos := indexOfString(cur, c.ID)
		if j == 0 {
			if existed && pos == 0 {
				continue
			}
		} else if prev := indexOfString(cur, next.Clips[j-1].ID); existed && pos >= 0 && pos == prev+1 {
			continue
		}
		op := Op{Op: "moveClip", ID: c.ID}
		if !existed {
			cc := c
			op = Op{Op: "insertClip", Clip: &cc}
		}
		if existed {
			cur = removeString(cur, c.ID)
		}
		at := len(cur)
		switch {
		case j > 0:
			op.After = next.Clips[j-1].ID
			at = indexOfString(cur, op.After) + 1
		case len(cur) > 0:
			op.Before = cur[0]
			at = 0
		}
		cur = append(cur[:at], append([]string{c.ID}, cur[at:]...)...)
		ops = append(ops, op)
	}
	ops = append(ops, cuts...)

	// --- audio and overlays: no move op exists, so order changes re-add items ---
	aOps, aUnlocks, aLocks, err := appendOnlyDiff("Audio", toItems(base.Audio), toItems(next.Audio))
	if err != nil {
		return nil, err
	}
	oOps, oUnlocks, oLocks, err := appendOnlyDiff("Overlay", toItems(base.Overlays), toItems(next.Overlays))
	if err != nil {
		return nil, err
	}
	ops = append(ops, aOps...)
	ops = append(ops, oOps...)
	unlocks = append(append(unlocks, aUnlocks...), oUnlocks...)
	locks = append(append(locks, aLocks...), oLocks...)

	// --- captions ---
	if !equalJSON(base.Captions, next.Captions) {
		bc, nc := base.Captions, next.Captions
		if bc != nil && nc != nil && bc.Locked != nc.Locked && equalCaptionsIgnoringLock(bc, nc) {
			lockChange("captions", bc.Locked, nc.Locked)
		} else {
			var c *Captions
			if nc != nil {
				cc := *nc
				c = &cc
			}
			ops = append(ops, Op{Op: "setCaptions", Captions: c})
		}
	}

	// --- narration ---
	vOps, vUnlocks, vLocks, err := voiceoverOps(base.Voiceover, next.Voiceover)
	if err != nil {
		return nil, err
	}
	ops = append(ops, vOps...)
	unlocks = append(unlocks, vUnlocks...)
	locks = append(locks, vLocks...)

	// --- the mix ---
	if !equalJSON(base.Mix, next.Mix) {
		bm, nm := base.Mix, next.Mix
		if bm != nil && nm != nil && bm.Locked != nm.Locked && equalMixIgnoringLock(bm, nm) {
			lockChange("mix", bm.Locked, nm.Locked)
		} else {
			var m *Mix
			if nm != nil {
				mm := *nm
				m = &mm
			}
			ops = append(ops, Op{Op: "setMix", Mix: m})
		}
	}

	// --- whole-cut settings ---
	if !equalJSON(base.Output, next.Output) {
		o := next.Output
		ops = append(ops, Op{Op: "setOutput", Output: &o})
	}
	if !equalJSON(base.Brand, next.Brand) {
		var b *Brand
		if next.Brand != nil {
			bb := *next.Brand
			b = &bb
		}
		ops = append(ops, Op{Op: "setBrand", Brand: b})
	}
	for _, n := range unionNames(base.Resources, next.Resources) {
		p, had := base.Resources[n]
		q, has := next.Resources[n]
		switch {
		case has && (!had || p != q):
			pin := q
			ops = append(ops, Op{Op: "setResource", ID: n, Resource: &pin})
		case had && !has:
			ops = append(ops, Op{Op: "setResource", ID: n})
		}
	}
	for _, n := range unionConversations(base.Conversations, next.Conversations) {
		p, had := base.Conversations[n]
		q, has := next.Conversations[n]
		switch {
		case has && (!had || !equalJSON(p, q)):
			pin := q
			ops = append(ops, Op{Op: "setConversation", ID: n, Conversation: &pin})
		case had && !has:
			ops = append(ops, Op{Op: "setConversation", ID: n})
		}
	}
	for _, id := range unionFixed(base.Fixed, next.Fixed) {
		p, had := base.Fixed[id]
		q, has := next.Fixed[id]
		switch {
		case has && (!had || p != q):
			pin := q
			ops = append(ops, Op{Op: "setFixed", ID: id, Fixed: &pin})
		case had && !has:
			ops = append(ops, Op{Op: "setFixed", ID: id})
		}
	}
	if base.Title != next.Title {
		t := next.Title
		ops = append(ops, Op{Op: "setTitle", Text: &t})
	}
	if base.Notes != next.Notes {
		t := next.Notes
		ops = append(ops, Op{Op: "setNotes", Text: &t})
	}

	out := append(append(unlocks, ops...), locks...)
	if len(out) == 0 {
		return []Op{}, nil
	}
	got, _, err := ApplyOps(base, out, true)
	if err != nil {
		return nil, fmt.Errorf("diff ops do not apply: %w", err)
	}
	Normalize(got)
	if Hash(got) != Hash(next) {
		var differ []string
		for id := range ChangedIDs(got, next) {
			differ = append(differ, id)
		}
		sort.Strings(differ)
		return nil, fmt.Errorf("diff ops do not reproduce the edited cut (differs at %v)", differ)
	}
	return out, nil
}

// item is an audio track or overlay seen generically (ID, lock, raw JSON).
type item struct {
	ID     string
	Locked bool
	Value  any
}

func toItems[T AudioTrack | Overlay](xs []T) []item {
	out := make([]item, 0, len(xs))
	for _, x := range xs {
		switch v := any(x).(type) {
		case AudioTrack:
			out = append(out, item{ID: v.ID, Locked: v.Locked, Value: v})
		case Overlay:
			out = append(out, item{ID: v.ID, Locked: v.Locked, Value: v})
		}
	}
	return out
}

// appendOnlyDiff diffs a list whose only ops are add (append), update and remove.
// It keeps the longest prefix of next that is already in order in base and
// re-adds everything after it.
func appendOnlyDiff(kind string, base, next []item) (ops, unlocks, locks []Op, err error) {
	inNext := map[string]bool{}
	for _, n := range next {
		inNext[n.ID] = true
	}
	byID := map[string]item{}
	var cur []item
	for _, b := range base {
		byID[b.ID] = b
		if inNext[b.ID] {
			cur = append(cur, b)
		} else {
			ops = append(ops, Op{Op: "remove" + kind, ID: b.ID})
		}
	}
	// Longest prefix of next that is a subsequence of cur.
	k, ci := 0, 0
	for k < len(next) {
		for ci < len(cur) && cur[ci].ID != next[k].ID {
			ci++
		}
		if ci == len(cur) {
			break
		}
		k++
		ci++
	}
	kept := map[string]bool{}
	for _, n := range next[:k] {
		kept[n.ID] = true
	}
	for _, c := range cur {
		if !kept[c.ID] {
			ops = append(ops, Op{Op: "remove" + kind, ID: c.ID})
		}
	}
	for _, n := range next[:k] {
		b := byID[n.ID]
		if equalJSON(b.Value, n.Value) {
			continue
		}
		set, err := fieldPatch(b.Value, n.Value, "id", "locked")
		if err != nil {
			return nil, nil, nil, err
		}
		if set != nil {
			ops = append(ops, Op{Op: "update" + kind, ID: n.ID, Set: set})
		}
		switch {
		case b.Locked && !n.Locked:
			unlocks = append(unlocks, Op{Op: "unlock", ID: n.ID})
		case !b.Locked && n.Locked:
			locks = append(locks, Op{Op: "lock", ID: n.ID})
		}
	}
	for _, n := range next[k:] {
		switch v := n.Value.(type) {
		case AudioTrack:
			t := v
			ops = append(ops, Op{Op: "addAudio", Track: &t})
		case Overlay:
			o := v
			ops = append(ops, Op{Op: "addOverlay", Overlay: &o})
		}
	}
	return ops, unlocks, locks, nil
}

// fieldPatch returns the `set` object (a JSON merge patch, as updateClip and
// friends apply it) that turns a into b, ignoring the given top-level keys.
// Removed fields become null; nested objects get nested patches. It returns
// nil when nothing but the skipped keys differ.
func fieldPatch(a, b any, skip ...string) (json.RawMessage, error) {
	ab, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	bb, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	var am, bm map[string]json.RawMessage
	_ = json.Unmarshal(ab, &am)
	_ = json.Unmarshal(bb, &bm)
	for _, s := range skip {
		delete(am, s)
		delete(bm, s)
	}
	am2, _ := json.Marshal(am)
	bm2, _ := json.Marshal(bm)
	p, changed, err := objectPatch(am2, bm2, 0)
	if err != nil || !changed {
		return nil, err
	}
	return p, nil
}

// errNotPatchable means a value holds a null inside an object, which a merge
// patch cannot express (null means "remove" there).
var errNotPatchable = fmt.Errorf("the change cannot be written as a field update")

// objectPatch is the merge patch from object a to object b. At depth 0 (the
// item's own fields) null means "reset to default", which is what a null field
// decodes to; deeper down a null member cannot be expressed.
func objectPatch(a, b json.RawMessage, depth int) (json.RawMessage, bool, error) {
	var am, bm map[string]json.RawMessage
	_ = json.Unmarshal(a, &am)
	_ = json.Unmarshal(b, &bm)
	set := map[string]json.RawMessage{}
	for k, bv := range bm {
		av, ok := am[k]
		switch {
		case ok && equalRaw(av, bv):
			continue
		case string(bytes.TrimSpace(bv)) == "null":
			if depth > 0 {
				return nil, false, errNotPatchable
			}
			set[k] = json.RawMessage("null")
		case ok && isObject(av) && isObject(bv):
			sub, changed, err := objectPatch(av, bv, depth+1)
			if err != nil {
				return nil, false, err
			}
			if changed {
				set[k] = sub
			}
		default:
			if hasNullInObject(bv) {
				return nil, false, errNotPatchable
			}
			set[k] = bv
		}
	}
	for k := range am {
		if _, ok := bm[k]; !ok {
			set[k] = json.RawMessage("null")
		}
	}
	if len(set) == 0 {
		return nil, false, nil
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(set[k])
	}
	buf.WriteByte('}')
	return buf.Bytes(), true, nil
}

func isObject(v json.RawMessage) bool {
	t := bytes.TrimSpace(v)
	return len(t) > 0 && t[0] == '{'
}

// hasNullInObject reports whether v is an object that has a null member, at any
// depth through nested objects (lists are replaced whole, so they are fine).
func hasNullInObject(v json.RawMessage) bool {
	if !isObject(v) {
		return false
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(bytes.TrimSpace(v), &m)
	for _, x := range m {
		if string(bytes.TrimSpace(x)) == "null" || hasNullInObject(x) {
			return true
		}
	}
	return false
}

func equalRaw(a, b json.RawMessage) bool {
	if bytes.Equal(a, b) {
		return true
	}
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	return equalJSON(av, bv)
}

func equalMixIgnoringLock(a, b *Mix) bool {
	x, y := *a, *b
	x.Locked, y.Locked = false, false
	return equalJSON(x, y)
}

// voiceoverOps diffs the narration: the whole script when it appears or goes
// away; otherwise updateVoiceover for its settings (layout, fit settings, the
// continuous recording), and per line removeBeat, updateBeat, addBeat and
// moveBeat (lines that left the longest common order), plus lock changes.
func voiceoverOps(bv, nv *Voiceover) (ops, unlocks, locks []Op, err error) {
	switch {
	case bv == nil && nv == nil:
		return nil, nil, nil, nil
	case bv == nil || nv == nil:
		var v *Voiceover
		if nv != nil {
			vv := *nv
			vv.Beats = append([]Beat{}, nv.Beats...)
			v = &vv
		}
		return []Op{{Op: "setScript", Voiceover: v}}, nil, nil, nil
	case equalJSON(bv, nv):
		return nil, nil, nil, nil
	}
	whole := func() ([]Op, []Op, []Op, error) {
		vv := *nv
		vv.Beats = append([]Beat{}, nv.Beats...)
		return []Op{{Op: "setScript", Voiceover: &vv}}, nil, nil, nil
	}
	lockChange := func(id string, was, is bool) {
		switch {
		case was && !is:
			unlocks = append(unlocks, Op{Op: "unlock", ID: id})
		case !was && is:
			locks = append(locks, Op{Op: "lock", ID: id})
		}
	}
	// Settings, layout and the continuous recording (not the lines).
	set, err := fieldPatch(*bv, *nv, "beats", "locked")
	if err == errNotPatchable {
		return whole()
	}
	if err != nil {
		return nil, nil, nil, err
	}
	if set != nil {
		ops = append(ops, Op{Op: "updateVoiceover", Set: set})
	}
	lockChange("voiceover", bv.Locked, nv.Locked)

	// Lines: removals, field updates, then order (adds and moves).
	baseBeats := map[string]Beat{}
	for _, b := range bv.Beats {
		baseBeats[b.ID] = b
	}
	inNext := map[string]bool{}
	for _, b := range nv.Beats {
		inNext[b.ID] = true
	}
	for _, b := range bv.Beats {
		if !inNext[b.ID] {
			ops = append(ops, Op{Op: "removeBeat", ID: b.ID})
		}
	}
	for _, b := range nv.Beats {
		old, ok := baseBeats[b.ID]
		if !ok || equalJSON(old, b) {
			continue
		}
		set, err := fieldPatch(old, b, "id", "locked")
		if err == errNotPatchable {
			return whole()
		}
		if err != nil {
			return nil, nil, nil, err
		}
		if set != nil {
			ops = append(ops, Op{Op: "updateBeat", ID: b.ID, Set: set})
		}
		lockChange(b.ID, old.Locked, b.Locked)
	}
	var cur []string
	weight := map[string]int{}
	for _, b := range bv.Beats {
		if inNext[b.ID] {
			cur = append(cur, b.ID)
			weight[b.ID] = 1
			if b.Locked {
				weight[b.ID] = 1000
			}
		}
	}
	var common []string
	for _, b := range nv.Beats {
		if _, ok := baseBeats[b.ID]; ok {
			common = append(common, b.ID)
		}
	}
	keep := weightedLCS(cur, common, weight)
	for j, b := range nv.Beats {
		if keep[b.ID] {
			continue
		}
		_, existed := baseBeats[b.ID]
		pos := indexOfString(cur, b.ID)
		if j == 0 {
			if existed && pos == 0 {
				continue
			}
		} else if prev := indexOfString(cur, nv.Beats[j-1].ID); existed && pos >= 0 && pos == prev+1 {
			continue
		}
		op := Op{Op: "moveBeat", ID: b.ID}
		if !existed {
			bb := b
			op = Op{Op: "addBeat", Beat: &bb}
		} else {
			cur = removeString(cur, b.ID)
		}
		at := len(cur)
		switch {
		case j > 0:
			op.After = nv.Beats[j-1].ID
			at = indexOfString(cur, op.After) + 1
		case len(cur) > 0:
			op.Before = cur[0]
			at = 0
		}
		cur = append(cur[:at], append([]string{b.ID}, cur[at:]...)...)
		ops = append(ops, op)
	}
	return ops, unlocks, locks, nil
}

func equalCaptionsIgnoringLock(a, b *Captions) bool {
	x, y := *a, *b
	x.Locked, y.Locked = false, false
	return equalJSON(x, y)
}

// weightedLCS returns the members of a longest (by total weight) common
// subsequence of a and b (both lists of unique IDs).
func weightedLCS(a, b []string, w map[string]int) map[string]bool {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			best := max(dp[i+1][j], dp[i][j+1])
			if a[i] == b[j] {
				best = max(best, dp[i+1][j+1]+w[a[i]])
			}
			dp[i][j] = best
		}
	}
	keep := map[string]bool{}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j] && dp[i][j] == dp[i+1][j+1]+w[a[i]]:
			keep[a[i]] = true
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			i++
		default:
			j++
		}
	}
	return keep
}

func indexOfString(xs []string, s string) int {
	for i, x := range xs {
		if x == s {
			return i
		}
	}
	return -1
}

func removeString(xs []string, s string) []string {
	out := xs[:0:0]
	for _, x := range xs {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}
