package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/catalog"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// metarush catalogue list|show (D272): the menu, group by group, each saying
// whether it works on this computer now or what it needs first.
func TestCatalogueCommands(t *testing.T) {
	a := testapp.New(t)
	home := a.Cfg.Home()
	var list struct {
		Data catalog.CatalogueListOut `json:"data"`
	}
	r := runCLI(t, home, "catalogue", "list", "--json")
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &list) != nil {
		t.Fatalf("list: %d %s %s", r.code, r.stdout, r.stderr)
	}
	l := list.Data
	if l.BuiltIn != len(catalog.FirstParty()) || l.Added != 0 || len(l.Groups) != 6 || l.Machine == "" || !strings.Contains(l.Message, "work on") {
		t.Fatalf("list: %+v", l)
	}
	byID := map[string]catalog.MenuItem{}
	for _, it := range l.Items {
		byID[it.ID] = it
		if it.Kind != catalog.KindFirstParty || it.GroupTitle == "" || it.MadeWith != it.Name || it.Runs == "" || it.Line == "" {
			t.Fatalf("item: %+v", it)
		}
		if !it.WorksHere && !strings.HasPrefix(it.Missing, "needs ") {
			t.Fatalf("an item that doesn't work here says what it needs: %+v", it)
		}
	}
	// A key it doesn't have: needs the key (tests have none).
	if n := byID["ai-narration"]; n.WorksHere || n.Missing != "needs an ElevenLabs key" || !n.NeedsKey || n.Cost != catalog.CostPriced {
		t.Fatalf("ai narration: %+v", n)
	}
	if s := byID["steady-footage"]; s.Runs != "asset.fixes.set" || s.Fixed["tick"] == nil || s.Group != catalog.GroupFix {
		t.Fatalf("steady: %+v", s)
	}
	// Filters.
	var fix struct {
		Data catalog.CatalogueListOut `json:"data"`
	}
	if r := runCLI(t, home, "catalogue", "list", "--group", "fix", "--json"); r.code != 0 || json.Unmarshal([]byte(r.stdout), &fix) != nil {
		t.Fatalf("--group: %s", r.stderr)
	}
	for _, it := range fix.Data.Items {
		if it.Group != catalog.GroupFix {
			t.Fatalf("--group fix gave %+v", it)
		}
	}
	var now struct {
		Data catalog.CatalogueListOut `json:"data"`
	}
	if r := runCLI(t, home, "catalogue", "list", "--works-now", "--json"); r.code != 0 || json.Unmarshal([]byte(r.stdout), &now) != nil {
		t.Fatalf("--works-now: %s", r.stderr)
	}
	for _, it := range now.Data.Items {
		if !it.WorksHere {
			t.Fatalf("--works-now gave %+v", it)
		}
	}
	if r := runCLI(t, home, "catalogue", "list", "--group", "nowhere"); r.code == 0 {
		t.Fatal("an unknown group")
	}
	// Show, by ID, by name, or by the operation it is.
	for _, name := range []string{"steady-footage", "metarush/steady-footage"} {
		var show struct {
			Data catalog.CatalogueShowOut `json:"data"`
		}
		if r := runCLI(t, home, "catalogue", "show", name, "--json"); r.code != 0 || json.Unmarshal([]byte(r.stdout), &show) != nil || show.Data.Item.ID != "steady-footage" || show.Data.Description.RunWith != "asset.fixes.set" {
			t.Fatalf("show %s: %s %s", name, r.stdout, r.stderr)
		}
	}
	var op struct {
		Data catalog.CatalogueShowOut `json:"data"`
	}
	if r := runCLI(t, home, "catalogue", "show", "footage.moments", "--json"); r.code != 0 || json.Unmarshal([]byte(r.stdout), &op) != nil || op.Data.Item.ID != "best-moments" {
		t.Fatalf("show an operation: %s", r.stderr)
	}
	if r := runCLI(t, home, "catalogue", "show", "no-such-thing"); r.code == 0 {
		t.Fatal("an unknown ability")
	}
	// Plain: the groups, a line each.
	p := runCLI(t, home, "catalogue", "list")
	if p.code != 0 || !strings.Contains(p.stdout, "Understand footage (") || !strings.Contains(p.stdout, "Steady shaky footage — ") || strings.Contains(p.stdout, "asset.fixes.set") {
		t.Fatalf("plain list:\n%s", p.stdout)
	}
}

// "It costs nothing." only for what has no price and uses no key: a key to
// a service may cost money there, whatever a manifest says (D268).
func TestCatalogueCostSentences(t *testing.T) {
	for cost, want := range map[string]string{
		catalog.CostFree:   "It costs nothing.",
		catalog.CostKeyed:  "It uses a key to a service, which may charge for what it does.",
		catalog.CostPriced: "Each run is priced first and paid to the service that does it.",
		catalog.CostPlan:   "It uses your Claude Code, which counts toward your Claude plan.",
		"":                 "",
	} {
		if got := costLine(cost); got != want {
			t.Errorf("%q: %q", cost, got)
		}
	}
}
