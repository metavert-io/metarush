package catalog_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/catalog"
	_ "github.com/metavert-io/metarush/internal/cli" // the whole registry, as the binary has it
	"github.com/metavert-io/metarush/internal/ops"
)

func TestFindChoosesTheRightOperation(t *testing.T) {
	sel, err := catalog.LoadSelection()
	if err != nil {
		t.Fatal(err)
	}
	es, err := catalog.EvalEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, e := range es {
		names[e.Name] = true
	}
	for _, c := range sel.Cases {
		for _, op := range append([]string{c.Op}, c.Alt...) {
			if _, ok := ops.Get(op); !ok && !names[op] {
				t.Errorf("case %d (%q) expects %s, which is nothing the catalogue has", c.N, c.Ask, op)
			}
		}
	}
	base := catalog.ScoreSelection(es, sel, catalog.BaselineRanking)
	got := catalog.ScoreSelection(es, sel, catalog.DefaultRanking)
	padded := catalog.ScoreSelection(append(append([]catalog.Entry(nil), es...), catalog.MadeUp(catalog.MadeUpEntries)...), sel, catalog.DefaultRanking)
	line := func(name string, r catalog.SelectionResult) string {
		return fmt.Sprintf("%-34s top-1 %5.1f%%  top-3 %5.1f%%  (tuned %5.1f%% / %5.1f%%, held out %5.1f%% / %5.1f%%)", name,
			100*r.All.Top1Rate(), 100*r.All.Top3Rate(), 100*r.Tuned.Top1Rate(), 100*r.Tuned.Top3Rate(), 100*r.Holdout.Top1Rate(), 100*r.Holdout.Top3Rate())
	}
	t.Logf("%d requests, %d entries (operations, first-party abilities, sample abilities)", len(sel.Cases), len(es))
	t.Log(line("BM25 over names and summaries", base))
	t.Log(line("find", got))
	t.Log(line(fmt.Sprintf("find, %d made-up entries more", catalog.MadeUpEntries), padded))
	if os.Getenv("METARUSH_EVAL_MISSES") != "" {
		for _, m := range got.Misses {
			if m.Case.Holdout && os.Getenv("METARUSH_EVAL_MISSES") != "all" {
				continue // tune on the others only
			}
			t.Logf("#%d %q want %s (rank %d) got %s", m.Case.N, m.Case.Ask, strings.Join(append([]string{m.Case.Op}, m.Case.Alt...), "|"), m.Rank, strings.Join(m.Got, ", "))
		}
	}
	// The gate (D223): find must keep choosing the right operation for the
	// eval's plain requests at least this often (catalog.SelectionGate).
	g := catalog.SelectionGate
	if !got.Holds(padded) {
		t.Errorf("find chooses worse than it did: %s; %s; the gate is top-1 %.0f%%, top-3 %.0f%%, held-out top-3 %.0f%%, top-3 with made-up entries %.0f%% (METARUSH_EVAL_MISSES=1 lists the misses)",
			line("find", got), line("with made-up entries", padded), 100*g.Top1, 100*g.Top3, 100*g.HoldoutTop3, 100*g.PaddedTop3)
	}
	if n := len(sel.Cases); n < 150 {
		t.Errorf("the eval has %d requests; it should have about 150", n)
	}
}
