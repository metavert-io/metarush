package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// An AI app held to proposing on the command line (D248): its changes are
// refused with the command that saves them as drafts, `draft save` saves one
// in its name, nothing people-only runs, and people read the drafts, the desk
// and the packet in plain words.
func TestProposeOnTheCommandLine(t *testing.T) {
	a := testapp.New(t)
	home := a.Cfg.Home()
	s := seedPlain(t, a)
	t.Setenv("METARUSH_PROPOSE", "1")
	t.Setenv("METARUSH_AGENT", "codex")

	if r := runCLI(t, home, "edit", "check", "--plan", s.plan); r.code != 0 {
		t.Fatalf("a check on a propose-only channel: %s%s", r.stdout, r.stderr)
	}
	r := runCLI(t, home, "edit", "apply", "--plan", s.plan, "--confirm", "ct_1_00", "--details")
	if r.code == 0 || !strings.Contains(r.stderr, "can only propose") || !strings.Contains(r.stderr, "metarush draft save --plan") {
		t.Fatalf("a change on a propose-only channel: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	r = runCLI(t, home, "draft", "save", "--plan", s.plan, "--note", "A tighter hook.")
	if r.code != 0 || !strings.Contains(r.stdout, "Codex's draft") || !strings.Contains(r.stdout, "Saved as a draft") {
		t.Fatalf("draft save: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	r = runCLI(t, home, "draft", "save", "content.rules.add", "--input", `{"text": "The name bar never animates"}`)
	if r.code != 0 || !strings.Contains(r.stdout, "“The name bar never animates”") {
		t.Fatalf("a Content Settings draft: %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	r = runCLI(t, home, "--json", "draft", "list")
	var list struct {
		Data ops.DraftListOut `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &list); err != nil || len(list.Data.Drafts) != 2 || list.Data.Drafts[1].Agent != "codex" || list.Data.Drafts[1].Via != "cli" ||
		list.Data.Drafts[1].Kind != models.DraftVersion {
		t.Fatalf("draft list --json: %v\n%s", err, r.stdout)
	}
	version := list.Data.Drafts[1]
	for _, args := range [][]string{{"draft", "approve", version.ID}, {"revision", "approve", s.rev2}} {
		if r := runCLI(t, home, args...); r.code == 0 || !strings.Contains(r.stderr, "Only a person") {
			t.Errorf("metarush %s on a propose-only channel: %d\n%s%s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
		}
	}
	if r := runCLI(t, home, "mcp", "serve", "--allow-changes"); r.code == 0 || !strings.Contains(r.stderr, "Choose one") {
		t.Errorf("mcp serve --allow-changes while proposing: %d\n%s", r.code, r.stderr)
	}

	// A person reads them.
	t.Setenv("METARUSH_PROPOSE", "")
	t.Setenv("METARUSH_AGENT", "")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"revision", "waiting"}, "Codex made it, from the command line: a draft, not in the cut yet"},
		{[]string{"draft", "list"}, "Codex's draft — “Garden story”"},
		{[]string{"revision", "packet", version.ID}, "What changes:"},
		{[]string{"revision", "packet", version.Revision.ID, "--markdown"}, "## What changes"},
		{[]string{"revision", "packet", s.rev2}, "Version 2 of “Garden story”"},
	} {
		r := runCLI(t, home, c.args...)
		out := r.stdout + r.stderr
		if r.code != 0 || !strings.Contains(out, c.want) {
			t.Errorf("metarush %s: %d, want %q\n%s", strings.Join(c.args, " "), r.code, c.want, out)
		}
		if strings.Contains(strings.Join(c.args, " "), "--markdown") {
			continue // a page to hand on: its link is the point
		}
		var bad []string
		for _, f := range plainAllow.Filter(plainwords.CheckShown(out)) {
			bad = append(bad, f.String())
		}
		if len(bad) > 0 {
			t.Errorf("metarush %s — not plain: %s\n%s", strings.Join(c.args, " "), strings.Join(bad, "; "), indentOut(out))
		}
	}
}
