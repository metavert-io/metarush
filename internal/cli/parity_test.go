package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/ops"
)

// parityExceptions are ops without a dedicated CLI command, each with the reason.
// Keep this list short: every feature must be controllable from the CLI.
var parityExceptions = map[string]string{}

// Every registered op needs a dedicated CLI command (the dashboard is a
// convenience layer, never the only way to do something). A command "uses" an
// op when the op name appears as a string literal in this package's sources
// (runOp, runOpWith, ops.Call and ops.CallTyped all take the name that way).
func TestEveryOpHasACLICommand(t *testing.T) {
	used := map[string]bool{}
	files, _ := filepath.Glob("*.go")
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					used[s] = true
				}
			}
			return true
		})
	}
	// Commands must also be reachable: walk the command tree once.
	if len(commands()) == 0 {
		t.Fatal("no commands")
	}
	for _, op := range ops.All() {
		if used[op.Name] {
			continue
		}
		if reason, ok := parityExceptions[op.Name]; ok && reason != "" {
			continue
		}
		t.Errorf("op %s has no dedicated CLI command (add one, or an exception with a reason)", op.Name)
	}
	for name := range parityExceptions {
		if _, ok := ops.Get(name); !ok {
			t.Errorf("parity exception for unknown op %s", name)
		}
	}
}
