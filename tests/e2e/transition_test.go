package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ops"
)

func f64p(v float64) *float64 { return &v }

// cut.transition changes one cut of the latest version: checked first (plain
// words, warnings, a token), saved with the token, by clip name or number, with
// what was not asked for kept; locks and the end fade work as everywhere else.
func TestCutTransitionOp(t *testing.T) {
	e := newCutEnv(t)
	dry := call[ops.CutTransitionOut](t, e.a, "cut.transition", ops.CutTransitionIn{ProjectID: e.project, Clip: "payoff", Type: "dissolve", DryRun: true})
	if dry.NoChange || dry.Check == nil || !dry.Check.Valid || dry.Check.ConfirmToken == "" || dry.Applied != nil {
		t.Fatalf("dry run: %+v", dry)
	}
	if dry.Words != "dissolve 0.5 s" || !strings.Contains(dry.Summary, "now comes in with a dissolve 0.5 s (was a straight cut)") || dry.Into == "" {
		t.Fatalf("words %q, summary %q, into %q", dry.Words, dry.Summary, dry.Into)
	}
	var plan editspec.Plan
	if err := json.Unmarshal(dry.Plan, &plan); err != nil || len(plan.Ops) != 1 || plan.Ops[0].Op != "setTransition" || plan.Ops[0].ID != "payoff" || plan.BaseRevisionID != e.rev1 {
		t.Fatalf("plan: %s (%v)", dry.Plan, err)
	}
	// Without a token (and not in the dashboard) it only checks.
	again := call[ops.CutTransitionOut](t, e.a, "cut.transition", ops.CutTransitionIn{ProjectID: e.project, Clip: "payoff", Type: "dissolve"})
	if again.Applied != nil || again.Check == nil {
		t.Fatalf("without a token: %+v", again)
	}
	// A token for another change is refused.
	if _, err := ops.CallTyped[ops.CutTransitionOut](e.ctx, e.a, "cut.transition", ops.CutTransitionIn{ProjectID: e.project, Clip: "payoff", Type: "dissolve", Duration: f64p(0.8), ConfirmToken: dry.Check.ConfirmToken}, ops.Caller{}); !apperr.Is(err, apperr.ConfirmRequired) {
		t.Fatalf("token for another change: %v", err)
	}
	saved := call[ops.CutTransitionOut](t, e.a, "cut.transition", ops.CutTransitionIn{ProjectID: e.project, Clip: "payoff", Type: "dissolve", ConfirmToken: dry.Check.ConfirmToken})
	if saved.Applied == nil || saved.Applied.Revision.Number != 2 {
		t.Fatalf("saved: %+v", saved)
	}
	if tr := e.clip(e.head(), "payoff").Transition; tr == nil || tr.Type != editspec.TransitionCrossfade || tr.Duration != 0.5 {
		t.Fatalf("payoff comes in with %+v", tr)
	}
	// By number, keeping the dissolve and adding the sound: an L-cut.
	carry := call[ops.CutTransitionOut](t, e.a, "cut.transition", ops.CutTransitionIn{ProjectID: e.project, Clip: "3", Type: "keep", Sound: "carry", SoundSeconds: f64p(0.25), DryRun: true})
	if carry.Words != "dissolve 0.5 s · voice carries 0.25 s" || carry.ClipID != "payoff" {
		t.Fatalf("carry: %q %q", carry.Words, carry.ClipID)
	}
	call[ops.CutTransitionOut](t, e.a, "cut.transition", ops.CutTransitionIn{ProjectID: e.project, Clip: "3", Sound: "carry", SoundSeconds: f64p(0.25), ConfirmToken: carry.Check.ConfirmToken})
	sheet := call[ops.CutShowOut](t, e.a, "cut.show", ops.CutShowIn{ProjectID: e.project})
	if !strings.Contains(sheet.Sheet, "↳ dissolve 0.5 s · voice carries 0.25 s\n\n3. payoff:") {
		t.Fatalf("the sheet's ↳ line:\n%s", sheet.Sheet)
	}
	// The same change again: nothing to do.
	same := call[ops.CutTransitionOut](t, e.a, "cut.transition", ops.CutTransitionIn{ProjectID: e.project, Clip: "payoff", Type: "dissolve"})
	if !same.NoChange || same.Check != nil || !strings.Contains(same.Summary, "already comes in with a dissolve 0.5 s, voice carries 0.25 s") {
		t.Fatalf("no change: %+v", same)
	}
	// The named person's voice: Ada talks in the hook.
	lead := call[ops.CutTransitionOut](t, e.a, "cut.transition", ops.CutTransitionIn{ProjectID: e.project, Clip: "hook", Type: "cut", Sound: "lead", DryRun: true})
	if lead.Words != "cut · Ada's voice starts 0.4 s early" {
		t.Fatalf("lead words: %q", lead.Words)
	}
	// Errors in plain words.
	if _, err := ops.CallTyped[ops.CutTransitionOut](e.ctx, e.a, "cut.transition", ops.CutTransitionIn{ProjectID: e.project, Clip: "nope", Type: "dissolve"}, ops.Caller{}); !apperr.Is(err, apperr.NotFound) || !strings.Contains(apperr.From(err).Hint, "hook, broll, payoff, end") {
		t.Fatalf("unknown clip: %v", err)
	}
	if _, err := ops.CallTyped[ops.CutTransitionOut](e.ctx, e.a, "cut.transition", ops.CutTransitionIn{ProjectID: e.project, Clip: "end", Type: "dissolve", Direction: "left"}, ops.Caller{}); !apperr.Is(err, apperr.InvalidInput) {
		t.Fatalf("a dissolve has no direction: %v", err)
	}
	// The end fade.
	end := call[ops.CutTransitionOut](t, e.a, "cut.transition", ops.CutTransitionIn{ProjectID: e.project, Clip: "end", Type: "fade out", Duration: f64p(1.5), End: true, DryRun: true})
	if end.Edge != "end" || end.Words != "fades out over 1.5 s" || !end.Check.Valid {
		t.Fatalf("end: %+v", end)
	}
	// A locked clip before the cut refuses a transition that reaches into it.
	lock := editspec.Plan{BaseRevisionID: e.head().ID, Instruction: "lock the b-roll", Ops: []editspec.Op{{Op: "lock", ID: "broll"}}}
	e.applyPlan(lock)
	_, err := ops.CallTyped[ops.CutTransitionOut](e.ctx, e.a, "cut.transition", ops.CutTransitionIn{ProjectID: e.project, Clip: "payoff", Type: "fade through black", DryRun: true}, ops.Caller{})
	if !apperr.Is(err, apperr.Locked) || !strings.Contains(apperr.From(err).Message, "the clip before") {
		t.Fatalf("locked clip before the cut: %v", err)
	}
	// Taking the dissolve away changes the locked clip's end just the same.
	if _, err := ops.CallTyped[ops.CutTransitionOut](e.ctx, e.a, "cut.transition", ops.CutTransitionIn{ProjectID: e.project, Clip: "payoff", Type: "cut", Sound: "cut", DryRun: true}, ops.Caller{}); !apperr.Is(err, apperr.Locked) {
		t.Fatalf("removing a dissolve next to a locked clip: %v", err)
	}
	// A fade in stays inside its own clip, so it is fine after a locked one.
	e.applyPlan(editspec.Plan{BaseRevisionID: e.head().ID, Instruction: "lock the payoff", Ops: []editspec.Op{{Op: "lock", ID: "payoff"}}})
	if fade := call[ops.CutTransitionOut](t, e.a, "cut.transition", ops.CutTransitionIn{ProjectID: e.project, Clip: "end", Type: "fade in", DryRun: true}); fade.Check == nil || !fade.Check.Valid {
		t.Fatalf("a fade in after a locked clip: %+v", fade)
	}
}
