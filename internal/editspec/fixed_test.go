package editspec

import (
	"strings"
	"testing"
)

// fixedFacts adds fixed copies to the fake facts.
type fixedFacts struct {
	fakeFacts
	copies map[string]FixedInfo // assetID+":"+sha
}

func (f fixedFacts) FixedCopy(assetID, sha string) (FixedInfo, bool) {
	c, ok := f.copies[assetID+":"+sha]
	return c, ok
}

var (
	shaOne = strings.Repeat("ab", 32)
	shaTwo = strings.Repeat("cd", 32)
)

// Footage a change puts into a cut for the first time plays the fixed copy it
// uses now; footage the cut already played keeps playing what it played; a
// pin the change sets itself is left alone; pins of footage the cut no
// longer plays go (D152).
func TestPinNewFootage(t *testing.T) {
	base := &Spec{Clips: []Clip{{ID: "hook", AssetID: astA, In: 1, Out: 4}}}
	current := func(id string) string {
		return map[string]string{astA: shaOne, astB: shaTwo}[id]
	}
	next := &Spec{Clips: []Clip{{ID: "hook", AssetID: astA, In: 1, Out: 4}, {ID: "broll", AssetID: astB, In: 0, Out: 3}}}
	if !PinNewFootage(base, next, nil, current) {
		t.Fatal("new footage should be pinned")
	}
	if next.FixedSHA(astB) != shaTwo || next.FixedSHA(astA) != "" {
		t.Fatalf("pins %+v", next.Fixed)
	}
	// A change that unpins it itself (play the original) is left alone.
	again := &Spec{Clips: next.Clips}
	if PinNewFootage(base, again, map[string]bool{astB: true}, current) || again.FixedSHA(astB) != "" {
		t.Fatalf("explicit: %+v", again.Fixed)
	}
	// The cut stops playing it: its pin goes.
	gone := &Spec{Clips: []Clip{{ID: "hook", AssetID: astA, In: 1, Out: 4}}, Fixed: map[string]FixedPin{astB: {SHA256: shaTwo}}}
	if !PinNewFootage(next, gone, nil, current) || gone.Fixed != nil {
		t.Fatalf("dropped: %+v", gone.Fixed)
	}
}

func TestSetFixedOpsAndChecks(t *testing.T) {
	base := baseSpec()
	s, changed, err := ApplyOps(base, []Op{{Op: "setFixed", ID: astA, Fixed: &FixedPin{SHA256: shaOne}}}, false)
	if err != nil || s.FixedSHA(astA) != shaOne || !changed["fixed:"+astA] {
		t.Fatalf("pin: %+v %v %v", s.Fixed, changed, err)
	}
	if base.FixedSHA(astA) != "" {
		t.Fatal("the base must not change")
	}
	if sum := OpsSummary([]Op{{Op: "setFixed", ID: astA, Fixed: &FixedPin{SHA256: shaOne}}}); !strings.Contains(sum, "fixed copy") {
		t.Fatalf("summary %q", sum)
	}
	// The difference between two versions is the same op.
	ops, err := DiffOps(base, s)
	if err != nil || len(ops) != 1 || ops[0].Op != "setFixed" || ops[0].Fixed == nil || ops[0].Fixed.SHA256 != shaOne {
		t.Fatalf("diff %+v %v", ops, err)
	}
	back, err := DiffOps(s, base)
	if err != nil || len(back) != 1 || back[0].Op != "setFixed" || back[0].Fixed != nil {
		t.Fatalf("back to the original: %+v %v", back, err)
	}
	unpinned, _, err := ApplyOps(s, back, false)
	if err != nil || unpinned.FixedSHA(astA) != "" {
		t.Fatalf("unpin: %+v %v", unpinned.Fixed, err)
	}

	// Checks: a pin is a SHA-256 of a ready copy of footage the cut plays.
	f := fixedFacts{fakeFacts: facts(), copies: map[string]FixedInfo{astA + ":" + shaOne: {Words: "steadier picture", Ready: true}, astB + ":" + shaTwo: {Ready: false}}}
	codes := func(s *Spec) map[string]string {
		out := map[string]string{}
		for _, i := range Validate(s, f, nil, false) {
			if strings.HasPrefix(i.Code, "fixed_") {
				out[i.Code] = i.Severity
			}
		}
		return out
	}
	if c := codes(s); len(c) != 0 {
		t.Fatalf("a ready copy: %v", c)
	}
	bad := cloneSpecForTest(s)
	bad.Fixed[astA] = FixedPin{SHA256: "nope"}
	if c := codes(bad); c["fixed_bad"] != "error" {
		t.Fatalf("bad: %v", c)
	}
	unknown := cloneSpecForTest(s)
	unknown.Fixed[astA] = FixedPin{SHA256: shaTwo}
	if c := codes(unknown); c["fixed_unknown"] != "error" {
		t.Fatalf("unknown: %v", c)
	}
	waiting := cloneSpecForTest(s)
	waiting.Fixed[astB] = FixedPin{SHA256: shaTwo}
	if c := codes(waiting); c["fixed_not_ready"] != "error" {
		t.Fatalf("not ready: %v", c)
	}
	unused := cloneSpecForTest(s)
	unused.Fixed["ast_01jabcdefghjkmnpqrstvwxyz9"] = FixedPin{SHA256: shaOne}
	if c := codes(unused); c["fixed_unused"] != "warning" {
		t.Fatalf("unused: %v", c)
	}
	// The render plan says which footage plays a fixed copy.
	if p := Resolve(s, f, "rev_test", "prj_test", "hash"); p.Fixed[astA] != shaOne {
		t.Fatalf("plan: %+v", p.Fixed)
	}
}

func cloneSpecForTest(s *Spec) *Spec {
	c := *s
	c.Fixed = map[string]FixedPin{}
	for k, v := range s.Fixed {
		c.Fixed[k] = v
	}
	return &c
}
