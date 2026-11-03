package editspec

import (
	"fmt"
	"strings"
)

// A reference — someone else's video, kept to learn from (D398) — never plays
// in a cut, wherever the cut would play it (D440): a clip, the cut's sound, a
// conversation's recordings and cutaways, narration (a line's take, one
// continuous recording, their cleaned-up copies), the mix's copies, and inside
// a nested cut at any depth. The check is one pass over AssetIDs, the list of
// every place a spec plays footage, so a place added there is checked too. The
// words say how a person who made the video says so; the command is behind
// Details.

// referenceIssues refuses every reference the spec plays.
func referenceIssues(s *Spec, facts Facts) []Issue {
	var out []Issue
	for _, id := range AssetIDs(s) {
		a, ok := facts.Asset(id)
		if !ok || !a.Reference {
			continue
		}
		path, who := wherePlays(s, id)
		out = append(out, referenceIssue(path, who, a))
	}
	nf, _ := facts.(*nestFacts)
	if nf == nil {
		return out
	}
	for i, c := range s.Clips {
		if !IsNested(c.AssetID) {
			continue
		}
		nc := nf.cuts[c.AssetID]
		if nc == nil || nc.spec == nil || nc.facts == nil {
			continue
		}
		if a, ok := firstReference(nc.spec, nc.facts, nc.depth); ok {
			is := errI("reference_footage", fmt.Sprintf("/clips/%d/assetId", i), "%s plays the cut “%s”, which has “%s” in it: a reference, someone else's video, kept to learn from, so no cut plays it. If it's your own video, say so on its page (This is mine) and check the change again; otherwise take it out of that cut.", upperFirst(clipWords(c)), nc.name(), a.Name)
			is.Command = "metarush footage reference " + a.ID + " --mine"
			out = append(out, is)
		}
	}
	return out
}

// referenceIssue refuses a reference where a cut plays it.
func referenceIssue(path, who string, a AssetFacts) Issue {
	is := errI("reference_footage", path, "%s plays “%s”, which is a reference: someone else's video, kept to learn from, so no cut plays it. If it's your own video, say so on its page (This is mine) and check the change again; otherwise choose footage of your own.", who, a.Name)
	is.Command = "metarush footage reference " + a.ID + " --mine"
	return is
}

// firstReference is the first reference a spec plays, in its nested cuts too.
func firstReference(s *Spec, facts Facts, depth int) (AssetFacts, bool) {
	for _, id := range AssetIDs(s) {
		if a, ok := facts.Asset(id); ok && a.Reference {
			return a, true
		}
	}
	nf, _ := facts.(*nestFacts)
	if nf == nil || depth > MaxNestDepth {
		return AssetFacts{}, false
	}
	for _, c := range s.Clips {
		if !IsNested(c.AssetID) {
			continue
		}
		if nc := nf.cuts[c.AssetID]; nc != nil && nc.spec != nil && nc.facts != nil {
			if a, ok := firstReference(nc.spec, nc.facts, depth+1); ok {
				return a, true
			}
		}
	}
	return AssetFacts{}, false
}

// ReferencesIn lists the references a spec plays (IDs, nested cuts' too) with
// facts that resolve them: what holds a version back from leaving (D441).
func ReferencesIn(s *Spec, facts Facts) []AssetFacts {
	facts = wrapFacts(s, facts)
	var out []AssetFacts
	seen := map[string]bool{}
	var walk func(s *Spec, facts Facts, depth int)
	walk = func(s *Spec, facts Facts, depth int) {
		for _, id := range AssetIDs(s) {
			if a, ok := facts.Asset(id); ok && a.Reference && !seen[id] {
				seen[id] = true
				out = append(out, a)
			}
		}
		nf, _ := facts.(*nestFacts)
		if nf == nil || depth > MaxNestDepth {
			return
		}
		for _, c := range s.Clips {
			if nc := nf.cuts[c.AssetID]; IsNested(c.AssetID) && nc != nil && nc.spec != nil && nc.facts != nil {
				walk(nc.spec, nc.facts, depth+1)
			}
		}
	}
	walk(s, facts, 0)
	return out
}

// wherePlays says where a spec plays a piece of footage: the path an issue
// points at and the words for what plays it.
func wherePlays(s *Spec, id string) (path, who string) {
	for i, c := range s.Clips {
		if c.AssetID == id {
			return fmt.Sprintf("/clips/%d/assetId", i), upperFirst(clipWords(c))
		}
	}
	for _, name := range sortedConvNames(s) {
		for j, m := range s.Conversations[name].Members {
			if m.AssetID == id {
				who := "A recording in the conversation “" + name + "”"
				if n := strings.TrimSpace(m.Name); n != "" {
					who = n + "'s recording in the conversation “" + name + "”"
				}
				return fmt.Sprintf("/conversations/%s/members/%d/assetId", name, j), who
			}
		}
	}
	for i, c := range s.Clips {
		for j, sh := range c.Shots {
			if sh.BRoll != nil && sh.BRoll.AssetID == id {
				return fmt.Sprintf("/clips/%d/shots/%d/broll/assetId", i, j), "The cutaway at " + Clock(sh.At)
			}
		}
	}
	for i, t := range s.Audio {
		if t.AssetID == id {
			return fmt.Sprintf("/audio/%d/assetId", i), upperFirst(trackWords(t))
		}
	}
	if v := s.Voiceover; v != nil {
		for i, b := range v.Beats {
			if t := b.Take; t != nil {
				if t.AssetID == id {
					return fmt.Sprintf("/voiceover/beats/%d/take/assetId", i), lineName(i, b.Text)
				}
				if t.Processed != nil && t.Processed.AssetID == id {
					return fmt.Sprintf("/voiceover/beats/%d/take/processed/assetId", i), lineName(i, b.Text)
				}
			}
		}
		if c := v.Continuous; c != nil {
			if c.AssetID == id {
				return "/voiceover/continuous/assetId", "The narration recording"
			}
			if c.Processed != nil && c.Processed.AssetID == id {
				return "/voiceover/continuous/processed/assetId", "The cleaned-up narration"
			}
		}
	}
	if m := s.Mix; m != nil {
		for i, e := range m.Enhanced {
			if e.Variant == id {
				return fmt.Sprintf("/mix/enhanced/%d/variant", i), "A clearer voice in the mix"
			}
		}
		for i, c := range m.Said {
			if c.Sound == id {
				return fmt.Sprintf("/mix/said/%d/sound", i), "Changed words in the mix"
			}
			if c.Picture == id {
				return fmt.Sprintf("/mix/said/%d/picture", i), "Changed words in the mix"
			}
		}
	}
	return "/", "This cut"
}
