package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/catalog"
	"github.com/metavert-io/metarush/internal/mcp"
)

func TestCommandLinesNameTheirOperations(t *testing.T) {
	for line, want := range map[string]string{
		"metarush --home /tmp/h footage moments prj_1 --json":                "footage.moments",
		"METARUSH_X=1 /opt/bin/metarush cut remove-words harbor 'the fence'": "cut.removeWords",
		"metarush render submit rev_1 --preset review --wait | tail -3":      "render.submit",
		"metarush ops find make a review copy --details":                     "ops.find",
		"metarush qa show rnd_1 && metarush render list":                     "qa.show",
		"cd somewhere; metarush project list":                                "",
		"ls -la":                                                             "",
	} {
		got := strings.Join(commandOps(line), "|")
		if got != want {
			t.Errorf("%q: got %q, want %q", line, got, want)
		}
	}
	if got := commandOps("metarush add clip.mov --project p"); !slices.Contains(got, "add.paths") {
		t.Errorf("metarush add: %v", got)
	}
	// MCP calls name theirs too: run's name, a mode's operation, a tool's own.
	for _, c := range []struct {
		set, tool string
		args      map[string]any
		want      string
	}{
		{mcp.ToolsetCore, "run", map[string]any{"name": "render_submit"}, "render.submit"},
		{mcp.ToolsetCore, "find", map[string]any{"task": "x"}, ""},
		{mcp.ToolsetCore, "qa_show", nil, "qa.show"},
		{mcp.ToolsetReview, "footage", map[string]any{"mode": "transcript"}, "footage.transcript"},
		{mcp.ToolsetReview, "revision", map[string]any{"mode": "waiting"}, "review.waiting"},
		{mcp.ToolsetCore, "footage_moments", nil, "footage.moments"},
	} {
		if got := mcp.OpOf(c.set, c.tool, c.args); got != c.want {
			t.Errorf("%s %s %v: got %q, want %q", c.set, c.tool, c.args, got, c.want)
		}
	}
}

// Looking around never counts as the choice; for a change, reading doesn't
// either.
func TestLookingAroundIsNotTheChoice(t *testing.T) {
	render := catalog.SelectionCase{Op: "render.submit"}
	moments := catalog.SelectionCase{Op: "footage.moments"}
	list := catalog.SelectionCase{Op: "project.list"}
	for _, c := range []struct {
		sc     catalog.SelectionCase
		op     string
		around bool
	}{
		{render, "project.list", true}, {render, "cut.show", true}, {render, "render.submit", false}, {render, "qa.run", false},
		{moments, "project.show", true}, {moments, "footage.transcript", false}, {list, "project.list", false}, {render, "", true},
	} {
		if got := lookingAround(c.op, c.sc); got != c.around {
			t.Errorf("want %s, call %s: looking around %v, want %v", c.sc.Op, c.op, got, c.around)
		}
	}
}

// The live run stops each session at its choice and judges it, with a
// stand-in for Claude Code that looks around first, then chooses.
func TestLiveSelectionStopsAtTheChoice(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "claude")
	script := `#!/bin/sh
cat <<'EOS'
{"type":"system","subtype":"init"}
{"type":"assistant","message":{"usage":{"input_tokens":12,"cache_creation_input_tokens":3000,"cache_read_input_tokens":988},"content":[{"type":"tool_use","name":"Skill","input":{"skill":"video-render"}}]}}
{"type":"assistant","message":{"usage":{"input_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":4500},"content":[{"type":"tool_use","name":"Bash","input":{"command":"metarush project list --json"}}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"metarush render submit rev_1 --preset review"}}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"metarush qa run rnd_1"}}]}}
{"type":"result","total_cost_usd":0.02}
EOS
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	sel := &catalog.Selection{Names: map[string]string{"project": "Harbor Days"}, Cases: []catalog.SelectionCase{
		{N: 1, Ask: "Make me a review copy of {project}", Op: "render.submit"},
		{N: 2, Ask: "What's wrong with the latest render?", Op: "qa.show"},
	}}
	o := liveSelectionOpts{surface: "skills", budget: 0.1, names: sel.Names, exe: "/bin/true", home: dir, claude: fake,
		logDir: filepath.Join(dir, "logs"), workRoot: dir}
	out, err := runLiveSelection(context.Background(), sel, o, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Cases != 2 || out.Right != 1 || out.Surface != "skills" || out.TokensBefore != 4000 {
		t.Fatalf("summary: %+v", out)
	}
	r1, r2 := out.Results[0], out.Results[1]
	if !r1.Pass || r1.Op != "render.submit" || r1.Ask != "Make me a review copy of Harbor Days" || len(r1.Calls) != 2 {
		t.Fatalf("case 1: %+v", r1)
	}
	if r2.Pass || !strings.Contains(r2.Reason, "render.submit") {
		t.Fatalf("case 2: %+v", r2)
	}
	if _, err := os.Stat(filepath.Join(dir, "logs", "001.jsonl")); err != nil {
		t.Fatal(err)
	}
	if got := liveCases(&catalog.Selection{Cases: make([]catalog.SelectionCase, 10)}, liveSelectionOpts{limit: 3}); len(got) != 3 {
		t.Fatalf("limit: %d", len(got))
	}
	t.Setenv("GITHUB_ACTIONS", "true")
	if !inCI() {
		t.Fatal("CI not seen")
	}
}
