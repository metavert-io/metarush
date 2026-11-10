package cli

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/abilities"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/plainwords"
)

// poison is what an AI app could write into an ability's manifest or a
// motion graphic's summary: a code that erases the line, a clipboard write, a
// bell, a right-to-left override, a zero-width space and a raw 0x9B byte.
const poison = "hosts: none\x1b[2K\x1b]52;c;ZXZpbA==\x07 \u202Eexe.txt\u200B\x9b2J"

// A typed decision shows its screen and asks its question without hidden
// characters, says that some were taken out, and counts only the exact word.
func TestDecideShowsACleanScreen(t *testing.T) {
	old := stdin
	defer func() { stdin = old }()
	for _, tc := range []struct {
		answer string
		want   bool
	}{{"approve\n", true}, {"approve now\n", false}, {"\n", false}} {
		stdin = bufio.NewReader(strings.NewReader(tc.answer))
		var got bool
		out := stdout(t, func() {
			got = decide(func() {
				fmt.Println("What it may reach: " + poison)
				fmt.Printf("Its inputs:\n  style: %s\n", poison)
			}, "Type \"approve\" to approve version 2 of "+poison, "approve")
		})
		if got != tc.want {
			t.Errorf("answer %q: decide = %v, want %v", tc.answer, got, tc.want)
		}
		if plainwords.HasHidden(out) {
			t.Fatalf("hidden characters reached the terminal: %+q", out)
		}
		for _, want := range []string{"What it may reach: hosts: none[2K]52;c;ZXZpbA== exe.txt2J", "Its inputs:\n  style:", "Note: MetaRush took 12 hidden characters out of what's shown above", "Note: MetaRush took 6 hidden characters out of the question below", "Type \"approve\" to approve version 2 of hosts: none"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q in:\n%s", want, out)
			}
		}
	}
	// Nothing hidden: no note.
	stdin = bufio.NewReader(strings.NewReader("yes\n"))
	out := stdout(t, func() { decide(func() { fmt.Println("plain") }, "Type \"yes\" to go on", "yes") })
	if strings.Contains(out, "Note:") || !strings.Contains(out, "plain\n") {
		t.Errorf("a plain screen changed: %q", out)
	}
	// What is printed before the question, through a screen, is cleaned and counted too.
	stdin = bufio.NewReader(strings.NewReader("run\n"))
	out = stdout(t, func() {
		var page screen
		page.show(func() { fmt.Println("plan: " + poison) })
		page.show(func() { fmt.Println("\u202Emore") })
		if !page.decide("Type \"run\" to start it", "run") {
			t.Error("run wasn't accepted")
		}
	})
	if plainwords.HasHidden(out) || !strings.Contains(out, "took 6 hidden characters out of what's shown above") || !strings.Contains(out, "took a hidden character out of what's shown above") {
		t.Errorf("screen: %q", out)
	}
}

// The approval of an ability shows its page through a clean screen: what an AI
// app wrote in a manifest can't rewrite what the owner reads.
func TestAbilityApprovalScreenIsClean(t *testing.T) {
	old := stdin
	defer func() { stdin = old }()
	stdin = bufio.NewReader(strings.NewReader("approve\n"))
	show := &abilities.AbilityShowOut{Message: "stock-footage, version 2: waits for an owner.", TakenBack: poison}
	show.Ability.Facts.Inputs = []models.AbilityInput{{Name: "style", Kind: "words", Says: poison}}
	show.Ability.Check.Warnings = []string{poison}
	show.Ability.Number = 2
	show.Here.Machine = "studio"
	show.Here.Ready = true
	show.Page = abilities.AbilityPage{Installs: []string{poison}, Licence: poison, Terms: poison}
	out := stdout(t, func() {
		decide(func() { printAbilityShow(show, false) }, fmt.Sprintf("Type %q to %s version %d", "approve", "approve", 2), "approve")
	})
	if plainwords.HasHidden(out) {
		t.Fatalf("hidden characters reached the terminal: %+q", out)
	}
	if !strings.Contains(out, "Note: MetaRush took") || !strings.Contains(out, "style (words): “hosts: none") {
		t.Errorf("screen:\n%s", out)
	}
}

// Every typed decision in the commands goes through decide or a screen: ask
// gets its question as a literal (or Sprintf of one) that names no word to
// type ("…" or %q in it); a question built any other way, or one asking for a
// word, fails here outside decide.go and askYes (its screen and question
// would reach the terminal as written).
func TestTypedDecisionsGoThroughAScreen(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	typed := func(s string) bool {
		return strings.Contains(s, `"`) || strings.Contains(s, "%q") || strings.Contains(s, "“")
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "decide.go" {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == "askYes" {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "ask" || len(call.Args) != 2 {
					return true
				}
				checked++
				where := fset.Position(call.Pos())
				q := call.Args[0]
				s, ok := literal(q)
				if c, isCall := q.(*ast.CallExpr); !ok && isCall && len(c.Args) > 0 {
					if sel, isSel := c.Fun.(*ast.SelectorExpr); isSel && sel.Sel.Name == "Sprintf" {
						s, ok = literal(c.Args[0])
					}
				}
				switch {
				case !ok:
					t.Errorf("%s: ask gets a question built at run time; give it a literal, or ask through decide", where)
				case typed(s):
					t.Errorf("%s: a typed decision asks directly (%q); use decide (or a screen) so its screen and question are cleaned", where, s)
				}
				return true
			})
		}
	}
	if checked < 20 {
		t.Fatalf("only %d questions found: did the files move?", checked)
	}
}

func literal(e ast.Expr) (string, bool) {
	b, ok := e.(*ast.BasicLit)
	if !ok || b.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(b.Value)
	return s, err == nil
}
