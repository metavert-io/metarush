package assistant

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/mattes"
	"github.com/metavert-io/metarush/internal/ops"
)

// Claude's background commands (D510, D512): `cut background` reads like the
// CLI's (never Claude's price), `footage cut-outs` is a read and the others
// are cards labelled plainly; a card runs by itself only when nothing would
// leave; the cut-out a card starts is its work.
func TestBackgroundCardsReadLikeTheCLI(t *testing.T) {
	parse := func(cmd string) *cliCall {
		words, err := splitCommand(cmd)
		if err != nil {
			t.Fatal(err)
		}
		call, ok := parseMetarush(words)
		if !ok {
			t.Fatalf("not a metarush command: %q", cmd)
		}
		return &call
	}
	in, ok := cutEditInput(parse(`metarush cut background prj_x --clip hook --with footage --footage waves --footage-at 0:03.5 --edges 4 --blur 30 --model local/modnet --max-cost 5 --json`))
	bg, isBg := in.(mattes.CutBackgroundIn)
	if !ok || !isBg || bg.ProjectID != "prj_x" || bg.Clip != "hook" || bg.With != "footage" || bg.Footage != "waves" || bg.FootageAt != 3.5 ||
		bg.Feather == nil || *bg.Feather != 4 || bg.Blur != 30 || bg.Model != "local/modnet" || bg.MaxUSD != nil {
		t.Fatalf("the input: %+v (%v)", in, ok)
	}
	if in, ok := cutEditInput(parse(`metarush cut background prj_x 2 --off`)); !ok || !in.(mattes.CutBackgroundIn).Off || in.(mattes.CutBackgroundIn).Clip != "2" {
		t.Fatalf("off by number: %+v", in)
	}
	for _, bad := range []string{`metarush cut background prj_x --with blur`, `metarush cut background prj_x --clip 1 --blur strong`, `metarush cut background prj_x --clip 1 --footage-at soon`} {
		if _, ok := cutEditInput(parse(bad)); ok {
			t.Errorf("%q read as a change", bad)
		}
	}
	if v, ok := clockOf("1:02.5"); !ok || v != 62.5 {
		t.Fatalf("clock: %v %v", v, ok)
	}
	// Reads and labels.
	if !readOnlyCall(*parse("metarush footage cut-outs ast_x --json")) || readOnlyCall(*parse("metarush footage cut-out ast_x --json")) || readOnlyCall(*parse("metarush cut background prj_x --clip 1")) {
		t.Fatal("only the list of cut-outs is a read")
	}
	p := newParser("/ws", "/as/conversations/asc_01jabcdefghjkmnpqrstvwxyz0", "/scratch", "/as", func(string) string { return "" })
	for cmd, want := range map[string]string{
		"metarush cut background prj_x --clip 1 --with blur": "Asked to replace a clip's background",
		"metarush cut background prj_x --clip 1 --off":       "Asked to take a clip's new background out",
		"metarush footage cut-out ast_x --json":              "Asked to cut the people out of the clip",
		"metarush footage cut-outs ast_x --json":             "Looked at the cut-outs of the clip",
	} {
		if got := p.bashLabel(parse(cmd), cmd); got != want {
			t.Errorf("label for %q: %q", cmd, got)
		}
	}
	// Auto: only a model on a computer of the workspace, and never a price.
	c := &conv{}
	for input, auto := range map[string]bool{`{"clip":"1"}`: true, `{"model":"local/modnet"}`: true, `{"model":"fake/fake-video-background"}`: false, `{"maxUsd":1}`: false} {
		for _, op := range []string{"cut.background", "footage.cutout"} {
			a := &AssistantAction{State: ActionPending, Op: op, Input: input}
			if got := c.autoStep(a); got != auto {
				t.Errorf("%s %s: auto %v", op, input, got)
			}
		}
	}
	// The cut-out a card starts is its work; one already made or priced isn't.
	co := &mattes.CutoutOut{JobID: "job_1", Matte: mattes.CutoutView{State: "waiting", ProjectID: "prj_x"}}
	w := backgroundWork(&mattes.CutBackgroundOut{CutEditOut: ops.CutEditOut{ProjectID: "prj_x"}, Waiting: true, Cutout: co})
	if w == nil || w.Kind != WorkJobs || len(w.JobIDs) != 1 || w.JobIDs[0] != "job_1" || !strings.Contains(w.Link, "prj_x") {
		t.Fatalf("work: %+v", w)
	}
	if backgroundWork(&mattes.CutBackgroundOut{}) != nil || cutoutWork(&mattes.CutoutOut{DryRun: true, JobID: "job_2"}, "") != nil ||
		cutoutWork(&mattes.CutoutOut{JobID: "job_3", Matte: mattes.CutoutView{State: "ready"}}, "") != nil {
		t.Fatal("no work for a saved change, a dry run or a cut-out already made")
	}
	if w := cutoutWork(&mattes.CutoutOut{JobID: "job_4"}, ""); w == nil || w.Link != "/library" {
		t.Fatalf("a cut-out outside a project: %+v", w)
	}
}
