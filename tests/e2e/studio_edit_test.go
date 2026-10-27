package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ops"
)

func common(project string) ops.CutEditCommon { return ops.CutEditCommon{ProjectID: project} }

func dry(c ops.CutEditCommon) ops.CutEditCommon { c.DryRun = true; return c }

func withToken(c ops.CutEditCommon, tok string) ops.CutEditCommon { c.ConfirmToken = tok; return c }

// planOps reads the ops of a verb's plan.
func planOps(t *testing.T, raw json.RawMessage) []editspec.Op {
	t.Helper()
	var p editspec.Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p.Ops
}

// The editing verbs: checked first (plain words, a token), saved with the
// token, by clip name or number; the cut sheet's names; errors in plain words.
func TestCutEditingVerbs(t *testing.T) {
	e := newCutEnv(t) // hook (talk 0.9–2.5), broll (0–2), payoff (talk 5.8–7.8), end (broll 4–6)
	c := common(e.project)

	trim := call[ops.CutEditOut](t, e.a, "cut.trim", ops.CutTrimIn{CutEditCommon: dry(c), Clip: "hook", End: "-0.3"})
	if trim.Check == nil || !trim.Check.Valid || trim.Check.ConfirmToken == "" || trim.Saved {
		t.Fatalf("dry run: %+v", trim)
	}
	if ops := planOps(t, trim.Plan); len(ops) != 1 || ops[0].Op != "trimClip" || *ops[0].To != 2.2 || ops[0].Edge != "end" {
		t.Fatalf("plan: %s", trim.Plan)
	}
	if !strings.Contains(trim.Summary, "now ends 0.3 s earlier (at 0:02.2 in its footage)") || trim.Duration >= trim.DurationBefore {
		t.Fatalf("summary %q (%v → %v)", trim.Summary, trim.DurationBefore, trim.Duration)
	}
	// Without a token (and not in the dashboard) it only checks.
	if again := call[ops.CutEditOut](t, e.a, "cut.trim", ops.CutTrimIn{CutEditCommon: c, Clip: "hook", End: "-0.3"}); again.Saved || again.Check == nil {
		t.Fatalf("without a token: %+v", again)
	}
	saved := call[ops.CutEditOut](t, e.a, "cut.trim", ops.CutTrimIn{CutEditCommon: withToken(c, trim.Check.ConfirmToken), Clip: "hook", End: "-0.3"})
	if !saved.Saved || saved.Applied.Revision.Number != 2 || e.clip(e.head(), "hook").Out != 2.2 {
		t.Fatalf("saved: %+v", saved)
	}
	if !strings.HasPrefix(e.head().Origin, "cut-trim:") || e.head().Instruction == "" {
		t.Fatalf("origin %q instruction %q", e.head().Origin, e.head().Instruction)
	}

	save := func(name string, in func(ops.CutEditCommon) any) *ops.CutEditOut {
		t.Helper()
		d := call[ops.CutEditOut](t, e.a, name, in(dry(c)))
		if d.Check == nil || !d.Check.Valid {
			t.Fatalf("%s: %+v %+v", name, d, d.Problems)
		}
		return call[ops.CutEditOut](t, e.a, name, in(withToken(c, d.Check.ConfirmToken)))
	}
	save("cut.move", func(x ops.CutEditCommon) any {
		return ops.CutMoveIn{CutEditCommon: x, Clip: "payoff", Position: "start"}
	})
	if clipOrder(e.head()) != "payoff,hook,broll,end" {
		t.Fatalf("move: %s", clipOrder(e.head()))
	}
	// Split at 1 s into the video: payoff plays 5.8–7.8, so at 6.8 of its footage.
	sp := save("cut.split", func(x ops.CutEditCommon) any { return ops.CutSplitIn{CutEditCommon: x, At: "0:01"} })
	if clipOrder(e.head()) != "payoff,payoff-2,hook,broll,end" || e.clip(e.head(), "payoff").Out != 6.8 || !strings.Contains(sp.Changes[0], "Split payoff in two") {
		t.Fatalf("split: %s %v", clipOrder(e.head()), sp.Changes)
	}
	// Words in the middle of payoff: it is cut around them (in the pauses).
	rw := save("cut.removeWords", func(x ops.CutEditCommon) any { return ops.CutRemoveWordsIn{CutEditCommon: x, Words: "we did"} })
	if len(rw.Removals) != 1 || rw.Removals[0].ClipID != "payoff" || clipOrder(e.head()) != "payoff,payoff-3,payoff-2,hook,broll,end" {
		t.Fatalf("remove words: %+v %s", rw.Removals, clipOrder(e.head()))
	}
	if p, p3 := e.clip(e.head(), "payoff"), e.clip(e.head(), "payoff-3"); p.Out != 6.21 || p3.In != 6.61 {
		t.Fatalf("cut in the pauses: %v / %v", p.Out, p3.In)
	}
	save("cut.delete", func(x ops.CutEditCommon) any { return ops.CutDeleteIn{CutEditCommon: x, Clips: []string{"broll", "1"}} })
	if clipOrder(e.head()) != "payoff-3,payoff-2,hook,end" {
		t.Fatalf("delete: %s", clipOrder(e.head()))
	}
	add := save("cut.add", func(x ops.CutEditCommon) any {
		return ops.CutAddIn{CutEditCommon: x, Footage: "broll-c", From: "1", To: "0:02.5", Position: "after hook"}
	})
	if clipOrder(e.head()) != "payoff-3,payoff-2,hook,broll-c,end" || e.clip(e.head(), "broll-c").In != 1 || !strings.Contains(add.Summary, "after") {
		t.Fatalf("add: %s %q", clipOrder(e.head()), add.Summary)
	}
	// A stretch of the timeline across two clips.
	before := e.head()
	save("cut.deleteRange", func(x ops.CutEditCommon) any { return ops.CutDeleteRangeIn{CutEditCommon: x, From: "1", To: "2"} })
	if after := e.head(); after.Duration > before.Duration-0.9 {
		t.Fatalf("range: %v → %v", before.Duration, after.Duration)
	}

	// Plain errors.
	if _, err := ops.CallTyped[ops.CutEditOut](e.ctx, e.a, "cut.trim", ops.CutTrimIn{CutEditCommon: dry(c), Clip: "nope", End: "-1"}, ops.Caller{}); !apperr.Is(err, apperr.NotFound) || !strings.Contains(apperr.From(err).Hint, "payoff-3, payoff-2, hook") {
		t.Fatalf("unknown clip: %v", err)
	}
	// Nothing to clean up is an answer, not an error.
	if out, err := ops.CallTyped[ops.CutEditOut](e.ctx, e.a, "cut.removeWords", ops.CutRemoveWordsIn{CutEditCommon: dry(c), Cleanup: true}, ops.Caller{}); err != nil || !out.NoChange || len(out.Removals) != 0 || !strings.HasPrefix(out.Summary, "No ums") {
		t.Fatalf("nothing to clean up: %+v %v", out, err)
	}
	if _, err := ops.CallTyped[ops.CutEditOut](e.ctx, e.a, "cut.removeWords", ops.CutRemoveWordsIn{CutEditCommon: dry(c), Words: "nobody was watching"}, ops.Caller{}); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("words never said: %v", err)
	}
	e.applyPlan(editspec.Plan{BaseRevisionID: e.head().ID, Instruction: "lock the end", Ops: []editspec.Op{{Op: "lock", ID: "end"}}})
	_, err := ops.CallTyped[ops.CutEditOut](e.ctx, e.a, "cut.trim", ops.CutTrimIn{CutEditCommon: dry(c), Clip: "end", Start: "+0.2"}, ops.Caller{})
	if !apperr.Is(err, apperr.Locked) || !strings.Contains(apperr.From(err).Message, "which is locked") {
		t.Fatalf("locked: %v", err)
	}
	// Too long for its footage: a problem in plain words, no token.
	long := call[ops.CutEditOut](t, e.a, "cut.trim", ops.CutTrimIn{CutEditCommon: dry(c), Clip: "hook", End: "40"})
	if long.Check == nil || long.Check.Valid || len(long.Problems) == 0 || !strings.Contains(long.Problems[0].Message, "is only") {
		t.Fatalf("out of range: %+v", long.Problems)
	}
	// The dashboard saves without a token (its own dialog is the gate).
	e.a.Actor.Via = "dashboard"
	defer func() { e.a.Actor.Via = "cli" }()
	if d := call[ops.CutEditOut](t, e.a, "cut.move", ops.CutMoveIn{CutEditCommon: c, Clip: "hook", Position: "start"}); !d.Saved || !strings.HasPrefix(e.head().Origin, "cut-move:studio") {
		t.Fatalf("dashboard: %+v", d)
	}
}

// Long pauses anywhere (D236): a dry run lists each pause with the stretch that
// shortens it; saving cuts in the middle of the quiet; nothing to shorten is an
// answer; lengths are checked in plain words.
func TestCutPauses(t *testing.T) {
	e := newCutEnv(t)
	c := common(e.project)
	// A long take of the interview: "…going to fail." [2 s] "And we did it…".
	add := call[ops.CutEditOut](t, e.a, "cut.add", ops.CutAddIn{CutEditCommon: dry(c), Footage: "interview-a", From: "0:01", To: "0:07.6", Name: "take"})
	call[ops.CutEditOut](t, e.a, "cut.add", ops.CutAddIn{CutEditCommon: withToken(c, add.Check.ConfirmToken), Footage: "interview-a", From: "0:01", To: "0:07.6", Name: "take"})
	before := e.head()

	d := call[ops.CutEditOut](t, e.a, "cut.pauses", ops.CutPausesIn{CutEditCommon: dry(c)})
	if len(d.Removals) != 1 || d.Removals[0].ClipID != "take" || d.Removals[0].Kind != "pause" || d.Removals[0].From != 4.25 || d.Removals[0].To != 5.75 {
		t.Fatalf("pauses: %+v", d.Removals)
	}
	if d.Check == nil || !d.Check.Valid || !strings.Contains(d.Summary, "Shortens 1 pause longer than 1 s to 0.5 s each (1.5 s shorter in all)") {
		t.Fatalf("dry run: %q %+v", d.Summary, d.Check)
	}
	saved := call[ops.CutEditOut](t, e.a, "cut.pauses", ops.CutPausesIn{CutEditCommon: withToken(c, d.Check.ConfirmToken)})
	after := e.head()
	if !saved.Saved || !strings.HasPrefix(after.Origin, "cut-pauses:") || after.Duration > before.Duration-1.4 || clipOrder(after) != clipOrder(before)+",take-2" {
		t.Fatalf("saved: %+v %q %v → %v %s", saved.Applied, after.Origin, before.Duration, after.Duration, clipOrder(after))
	}
	// The same lengths again: nothing left to shorten, said plainly.
	if none := call[ops.CutEditOut](t, e.a, "cut.pauses", ops.CutPausesIn{CutEditCommon: dry(c)}); !none.NoChange || !strings.HasPrefix(none.Summary, "No pauses longer than 1 s") {
		t.Fatalf("nothing left: %+v", none)
	}
	// A shorter limit finds the half-second pause before "because"; picking checks its numbers.
	short := call[ops.CutEditOut](t, e.a, "cut.pauses", ops.CutPausesIn{CutEditCommon: dry(c), LongerThan: 0.4, To: 0.2})
	if len(short.Removals) == 0 {
		t.Fatalf("shorter pauses: %+v", short)
	}
	if _, err := ops.CallTyped[ops.CutEditOut](e.ctx, e.a, "cut.pauses", ops.CutPausesIn{CutEditCommon: dry(c), LongerThan: 0.4, To: 0.2, Pick: []int{9}}, ops.Caller{}); !apperr.Is(err, apperr.InvalidInput) {
		t.Fatalf("a pick that isn't there: %v", err)
	}
	if _, err := ops.CallTyped[ops.CutEditOut](e.ctx, e.a, "cut.pauses", ops.CutPausesIn{CutEditCommon: dry(c), LongerThan: 0.5, To: 0.8}, ops.Caller{}); !apperr.Is(err, apperr.InvalidInput) || !strings.Contains(apperr.From(err).Message, "less than they are") {
		t.Fatalf("lengths: %v", err)
	}
}

// edit.preview: what a plan would make, with its render plan, in plain words;
// a conflict with a newer version is explained, not raised.
func TestEditPreview(t *testing.T) {
	e := newCutEnv(t)
	plan := func(base string, ops ...editspec.Op) json.RawMessage {
		raw, _ := json.Marshal(editspec.Plan{BaseRevisionID: base, Origin: "timeline:studio", Ops: ops})
		return raw
	}
	to := 2.0
	pv := call[ops.EditPreviewOut](t, e.a, "edit.preview", ops.EditPreviewIn{Plan: plan(e.rev1,
		editspec.Op{Op: "trimClip", ID: "hook", Edge: "end", To: &to}, editspec.Op{Op: "moveClip", ID: "end", Before: "hook"})})
	if !pv.Valid || pv.Spec == nil || pv.RenderPlan == nil || len(pv.RenderPlan.Clips) != 4 || pv.RenderPlan.Sources[e.talk] == "" {
		t.Fatalf("preview: %+v", pv)
	}
	if pv.Summary != "Trimmed 1 clip, moved 1" || pv.Spec.Clips[0].ID != "end" || len(pv.Changes) == 0 {
		t.Fatalf("summary %q changes %v", pv.Summary, pv.Changes)
	}
	// Nothing was saved.
	if e.head().ID != e.rev1 {
		t.Fatal("a preview saved a version")
	}
	// A problem, in plain words, and no render plan.
	far := 40.0
	bad := call[ops.EditPreviewOut](t, e.a, "edit.preview", ops.EditPreviewIn{Plan: plan(e.rev1, editspec.Op{Op: "trimClip", ID: "hook", Edge: "end", To: &far})})
	if bad.Valid || bad.RenderPlan != nil || len(bad.Errors) == 0 || !strings.Contains(bad.Errors[0].Message, "hook ends at 0:40, but") {
		t.Fatalf("bad: %+v", bad.Errors)
	}
	// Saved from the timeline: the summary is the instruction.
	raw := plan(e.rev1, editspec.Op{Op: "trimClip", ID: "hook", Edge: "end", To: &to})
	e.a.Actor.Via = "dashboard"
	res := call[ops.EditApplyOut](t, e.a, "edit.apply", ops.EditApplyIn{Plan: raw})
	e.a.Actor.Via = "cli"
	if res.Revision.Instruction != "Trimmed 1 clip" || res.Revision.Origin != "timeline:studio" {
		t.Fatalf("timeline save: %+v", res.Revision)
	}
	// Written against r1 while r2 changed hook: a conflict, explained.
	again := 1.8
	cf := call[ops.EditPreviewOut](t, e.a, "edit.preview", ops.EditPreviewIn{Plan: plan(e.rev1, editspec.Op{Op: "trimClip", ID: "hook", Edge: "end", To: &again})})
	if cf.Conflict == nil || cf.Conflict.HeadNumber != 2 || !strings.Contains(cf.Conflict.Message, "the hook clip") || !strings.Contains(cf.Conflict.Message, "on the timeline") {
		t.Fatalf("conflict: %+v", cf.Conflict)
	}
	// Disjoint: combined with the newer version, as saving would.
	inPt := 0.5
	rb := call[ops.EditPreviewOut](t, e.a, "edit.preview", ops.EditPreviewIn{Plan: plan(e.rev1, editspec.Op{Op: "trimClip", ID: "broll", Edge: "in", To: &inPt})})
	if !rb.Rebased || !rb.Valid || rb.Spec.Clips[0].Out != 2 {
		t.Fatalf("rebase: %+v", rb)
	}
}
