package editspec

import "sort"

// Fixed copies in a cut (D152). spec.fixed pins, for a piece of footage, the
// fixed copy the cut plays (a steadier picture, cleaned-up voices); footage
// without a pin plays its original. A saved version keeps playing what it was
// saved with, whatever fixes are ticked later.

// FixedFacts is an optional part of Facts: whether a fixed copy of a piece of
// footage exists and is ready, and what it fixes, in words.
type FixedFacts interface {
	FixedCopy(assetID, sha string) (FixedInfo, bool)
}

// FixedInfo describes a fixed copy for checks and cut sheets.
type FixedInfo struct {
	// Words are its fixes in a few words ("steadier picture, balanced colour").
	Words string
	Ready bool
}

// FixedAssets lists the footage pinned to a fixed copy, sorted.
func FixedAssets(s *Spec) []string {
	out := make([]string, 0, len(s.Fixed))
	for id := range s.Fixed {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// fixedIssues checks the pins: each names footage the cut plays and a fixed
// copy of it that is ready.
func fixedIssues(s *Spec, facts Facts) []Issue {
	if len(s.Fixed) == 0 {
		return nil
	}
	used := map[string]bool{}
	for _, id := range AssetIDs(s) {
		used[id] = true
	}
	ff, _ := facts.(FixedFacts)
	var out []Issue
	for _, id := range FixedAssets(s) {
		pin := s.Fixed[id]
		path := "/fixed/" + id
		name := id
		if f, ok := facts.Asset(id); ok && f.Name != "" {
			name = "“" + f.Name + "”"
		}
		if !shaHexRe.MatchString(pin.SHA256) {
			out = append(out, errI("fixed_bad", path, "The fixed copy of %s should be named by its SHA-256 (64 hexadecimal characters).", name))
			continue
		}
		if !used[id] {
			out = append(out, warnI("fixed_unused", path, "The cut no longer plays %s, so its fixed copy isn't used.", name))
			continue
		}
		if ff == nil {
			continue
		}
		info, ok := ff.FixedCopy(id, pin.SHA256)
		switch {
		case !ok:
			out = append(out, errI("fixed_unknown", path, "MetaRush doesn't have the fixed copy of %s that this cut asks for; play the original, or tick its fixes again.", name))
		case !info.Ready:
			out = append(out, errI("fixed_not_ready", path, "The fixed copy of %s isn't ready yet; wait for it, or play the original.", name))
		}
	}
	return out
}

// PinNewFootage pins the fixed copy of footage a change puts into the cut for
// the first time (D152): footage next plays that base doesn't, has no pin and
// whose current media is a fixed copy (current returns its SHA-256, "" for
// the original). explicit lists footage the change pins or unpins itself,
// which is left alone. Pins of footage next no longer plays are dropped. It
// reports whether next changed.
func PinNewFootage(base, next *Spec, explicit map[string]bool, current func(assetID string) string) bool {
	changed := false
	before := map[string]bool{}
	if base != nil {
		for _, id := range AssetIDs(base) {
			before[id] = true
		}
	}
	now := map[string]bool{}
	for _, id := range AssetIDs(next) {
		now[id] = true
		if before[id] || explicit[id] {
			continue
		}
		if _, pinned := next.Fixed[id]; pinned {
			continue
		}
		if sha := current(id); sha != "" {
			if next.Fixed == nil {
				next.Fixed = map[string]FixedPin{}
			}
			next.Fixed[id] = FixedPin{SHA256: sha}
			changed = true
		}
	}
	for id := range next.Fixed {
		if !now[id] && !explicit[id] {
			delete(next.Fixed, id)
			changed = true
		}
	}
	if len(next.Fixed) == 0 && next.Fixed != nil {
		next.Fixed = nil
	}
	return changed
}

// ExplicitFixed lists the footage a list of ops pins or unpins itself.
func ExplicitFixed(ops []Op) map[string]bool {
	out := map[string]bool{}
	for _, op := range ops {
		if op.Op == "setFixed" {
			out[op.ID] = true
		}
	}
	return out
}
