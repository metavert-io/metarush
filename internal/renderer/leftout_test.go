package renderer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/components/choice"
)

// A renderer a person left out (at setup, or removed in Settings →
// Components) is its own state, not "missing": nothing sets it up by
// itself, and its one step is adding the component (D325).
func TestLeftOutRendererIsNotMissing(t *testing.T) {
	e, fake := env(t, nil)
	if err := choice.Set(e.Home, map[string]bool{Component: false}, "setup"); err != nil {
		t.Fatal(err)
	}
	st := e.Check(false)
	if st.State != StateLeftOut || st.Ready || !strings.HasPrefix(st.Problem, "Rendering needs the Remotion renderer") {
		t.Fatalf("left out: %+v", st)
	}
	if st.Command() != "metarush components add renderer" {
		t.Fatalf("command: %q", st.Command())
	}
	// The daemon's first start leaves it alone.
	var said []string
	e.FirstUse(context.Background(), func(f string, _ ...any) { said = append(said, f) })
	if _, err := os.Stat(filepath.Join(Root(e.Home), st.ID)); err == nil {
		t.Fatal("a left-out renderer was set up at first start")
	}
	if b, _ := os.ReadFile(filepath.Join(fake.Dir, "runs.log")); len(b) > 0 {
		t.Fatalf("npm ran for a left-out renderer: %s", b)
	}
	// The command doesn't depend on the words of the problem.
	st.Problem = "something else"
	if st.Command() != "metarush components add renderer" {
		t.Fatalf("command follows the words: %q", st.Command())
	}
	// Node.js missing is said by a field, not by the words.
	nn := Status{Source: SourceBundled, State: StateUnavailable, Needs: NeedsNode, Problem: "anything"}
	if nn.Command() != "metarush components add renderer" || (&NotReady{Status: nn}).AppError().Code != "E_TOOL_MISSING" {
		t.Fatalf("no Node.js: %q", nn.Command())
	}
}

// Nobody chose the renderer and none was ever set up here: the daemon's
// first start doesn't set it up (a person adds it); an upgrade of one that
// was set up here does (D327).
func TestFirstUseOnlyForAChosenOrUpgradedRenderer(t *testing.T) {
	e, fake := env(t, nil)
	_ = os.Remove(choice.Path(e.Home))
	st := e.Check(false)
	if st.State != StateMissing || !strings.Contains(st.Fix, "Settings → Components") {
		t.Fatalf("nobody chose: %+v", st)
	}
	e.FirstUse(context.Background(), func(string, ...any) {})
	if b, _ := os.ReadFile(filepath.Join(fake.Dir, "runs.log")); len(b) > 0 {
		t.Fatalf("set up at first start though nobody chose it: %s", b)
	}
	rec := e.loadRecord()
	rec.Installed = map[string]time.Time{"0.0.1-older": time.Now()}
	e.saveRecord(rec)
	if !e.SetsItselfUp() {
		t.Fatal("an upgrade of a renderer set up here doesn't set itself up")
	}
}
