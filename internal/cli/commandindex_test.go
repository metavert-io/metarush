package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/catalog"
	"github.com/metavert-io/metarush/internal/ops"
)

// commandOverrides name the command of operations the source can't tell
// (a command made by a helper that takes the operation's name as an argument).
var commandOverrides = map[string]string{
	"users.resetLink": "metarush users reset-password", // usersLinkCmd("reset-password", "users.resetLink", …)
	"draft.decide":    "metarush draft approve",        // draftDecideCmd("approve" | "not-quite" | "turn-down")
}

const commandIndexFile = "../catalog/commands.json"

// The command index (internal/catalog/commands.json) says which command runs
// each operation, so find can match a request against the command's words and
// describe can name it. It is generated from this package: every command's
// RunE (found by its function's file and line) and the operation names it uses
// — directly, through a helper of this package, or by its forOps annotation.
// An operation whose name is a command path ("footage.moments" → metarush
// footage moments) takes that command. METARUSH_UPDATE_COMMANDS=1 writes the
// file; otherwise the test fails while it is out of date.
func TestCommandIndex(t *testing.T) {
	got := buildCommandIndex(t)
	want, err := os.ReadFile(commandIndexFile)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("METARUSH_UPDATE_COMMANDS") == "1" {
		if !bytes.Equal(got, want) {
			if err := os.WriteFile(commandIndexFile, got, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("wrote %s", commandIndexFile)
		}
		return
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("internal/catalog/commands.json is out of date (an operation or a command changed): run\n  METARUSH_UPDATE_COMMANDS=1 go test ./internal/cli -run TestCommandIndex\nand commit the file")
	}
	// Every operation has a command (the parity test asks for one).
	var ix catalog.CommandIndex
	if err := json.Unmarshal(got, &ix); err != nil {
		t.Fatal(err)
	}
	for _, op := range ops.All() {
		if _, ok := ix.Commands[op.Name]; !ok {
			t.Errorf("no command found for %s: add it to commandOverrides", op.Name)
		}
	}
	for op, cmd := range commandOverrides {
		if _, ok := ops.Get(op); !ok {
			t.Errorf("commandOverrides names %s, which is no operation", op)
		}
		if ix.Commands[op].Command != cmd {
			t.Errorf("commandOverrides: %s is not a command", cmd)
		}
	}
}

func buildCommandIndex(t *testing.T) []byte {
	t.Helper()
	root := &cobra.Command{Use: "metarush"}
	for _, c := range commands() {
		root.AddCommand(c)
	}
	byPath := map[string]*cobra.Command{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		byPath[c.CommandPath()] = c
		for _, s := range c.Commands() {
			walk(s)
		}
	}
	walk(root)
	opNames := map[string]bool{}
	for _, op := range ops.All() {
		opNames[op.Name] = true
	}

	// The package's functions and function literals, and the closures each
	// function keeps in a variable (run := func(…) {…}) that its commands call.
	fset := token.NewFileSet()
	files, _ := filepath.Glob("*.go")
	funcs := map[string]*ast.FuncDecl{}
	lits := map[string]*ast.FuncLit{}                // "file.go:line"
	litDecl := map[*ast.FuncLit]string{}             // the function a literal is in
	locals := map[string]map[string][]*ast.FuncLit{} // function → variable → closures
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Body == nil {
				continue
			}
			funcs[fd.Name.Name] = fd
			vars := map[string][]*ast.FuncLit{}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.FuncLit:
					p := fset.Position(v.Pos())
					lits[fmt.Sprintf("%s:%d", filepath.Base(p.Filename), p.Line)] = v
					litDecl[v] = fd.Name.Name
				case *ast.AssignStmt:
					for i, rhs := range v.Rhs {
						if fl, ok := rhs.(*ast.FuncLit); ok && i < len(v.Lhs) {
							if id, ok := v.Lhs[i].(*ast.Ident); ok {
								vars[id.Name] = append(vars[id.Name], fl)
							}
						}
					}
				}
				return true
			})
			locals[fd.Name.Name] = vars
		}
	}
	// opsIn: the operation names a body uses, directly and through this
	// package's functions or the enclosing function's closures (up to three
	// calls deep).
	var opsIn func(n ast.Node, decl string, depth int, seen map[string]bool, direct, indirect map[string]bool)
	opsIn = func(n ast.Node, decl string, depth int, seen map[string]bool, direct, indirect map[string]bool) {
		ast.Inspect(n, func(x ast.Node) bool {
			switch v := x.(type) {
			case *ast.BasicLit:
				if v.Kind == token.STRING {
					if s, err := strconv.Unquote(v.Value); err == nil && opNames[s] {
						direct[s] = true
					}
				}
			case *ast.CallExpr:
				id, ok := v.Fun.(*ast.Ident)
				if !ok || depth == 0 {
					return true
				}
				if fls := locals[decl][id.Name]; len(fls) > 0 && !seen[decl+"."+id.Name] {
					// A closure of the same function: what it uses counts as the command's own.
					seen[decl+"."+id.Name] = true
					for _, fl := range fls {
						opsIn(fl.Body, decl, depth-1, seen, direct, indirect)
					}
				} else if fd := funcs[id.Name]; fd != nil && !seen[id.Name] {
					seen[id.Name] = true
					sub := map[string]bool{}
					opsIn(fd.Body, id.Name, depth-1, seen, sub, indirect)
					for o := range sub {
						indirect[o] = true
					}
				}
			}
			return true
		})
	}

	type cand struct {
		path     string
		strength int // 3 annotation, 2 direct, 1 indirect
	}
	candidates := map[string][]cand{}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		c := byPath[p]
		if a := c.Annotations[opsAnnotation]; a != "" {
			for _, o := range strings.Split(a, ",") {
				candidates[o] = append(candidates[o], cand{p, 3})
			}
		}
		if c.RunE == nil {
			continue
		}
		fn := runtime.FuncForPC(reflect.ValueOf(c.RunE).Pointer())
		file, line := fn.FileLine(fn.Entry())
		var body ast.Node
		decl := ""
		if fl := lits[fmt.Sprintf("%s:%d", filepath.Base(file), line)]; fl != nil {
			body, decl = fl.Body, litDecl[fl]
		} else {
			name := fn.Name()
			name = name[strings.LastIndex(name, ".")+1:]
			if fd := funcs[name]; fd != nil {
				body, decl = fd.Body, name
			}
		}
		if body == nil {
			continue
		}
		direct, indirect := map[string]bool{}, map[string]bool{}
		opsIn(body, decl, 3, map[string]bool{}, direct, indirect)
		for o := range direct {
			candidates[o] = append(candidates[o], cand{p, 2})
		}
		for o := range indirect {
			if !direct[o] {
				candidates[o] = append(candidates[o], cand{p, 1})
			}
		}
	}

	overlap := func(op, path string) int {
		words := map[string]bool{}
		for _, w := range catalog.Terms(path, true) {
			words[w] = true
		}
		n := 0
		for _, w := range catalog.Terms(op, true) {
			if words[w] {
				n++
			}
		}
		return n
	}
	index := map[string]catalog.CLICommand{}
	for name := range opNames {
		var pick string
		if p := "metarush " + strings.ReplaceAll(name, ".", " "); byPath[p] != nil {
			pick = p
		} else if o := commandOverrides[name]; o != "" && byPath[o] != nil {
			pick = o
		} else if cs := candidates[name]; len(cs) > 0 {
			sort.SliceStable(cs, func(i, j int) bool {
				a, b := cs[i], cs[j]
				if a.strength != b.strength {
					return a.strength > b.strength
				}
				if oa, ob := overlap(name, a.path), overlap(name, b.path); oa != ob {
					return oa > ob
				}
				if la, lb := len(strings.Fields(a.path)), len(strings.Fields(b.path)); la != lb {
					return la < lb
				}
				return a.path < b.path
			})
			pick = cs[0].path
			// Several subcommands of one command that all do it (keep source,
			// keep logo, keep brief…): the command they belong to says it best.
			same, parent := 0, pick[:strings.LastIndex(pick, " ")]
			for _, c := range cs {
				if c.strength == cs[0].strength && overlap(name, c.path) == overlap(name, cs[0].path) && strings.HasPrefix(c.path, parent+" ") {
					same++
				}
			}
			if same >= 3 && byPath[parent] != nil && parent != "metarush" {
				pick = parent
			}
		}
		if pick == "" {
			continue
		}
		c := byPath[pick]
		usage := strings.TrimSuffix(c.UseLine(), " [flags]")
		index[name] = catalog.CLICommand{Command: pick, Usage: usage, Help: strings.TrimSpace(c.Short)}
	}

	var b bytes.Buffer
	b.WriteString("{\n  \"_comment\": \"Generated: each operation's command-line command. go test ./internal/cli -run TestCommandIndex checks it; METARUSH_UPDATE_COMMANDS=1 rewrites it. Do not edit by hand.\",\n  \"commands\": {\n")
	names := make([]string, 0, len(index))
	for n := range index {
		names = append(names, n)
	}
	sort.Strings(names)
	marshal := func(v any) string {
		var out bytes.Buffer
		enc := json.NewEncoder(&out)
		enc.SetEscapeHTML(false) // usages say <project>
		_ = enc.Encode(v)
		return strings.TrimSpace(out.String())
	}
	for i, n := range names {
		k, v := marshal(n), marshal(index[n])
		sep := ","
		if i == len(names)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "    %s: %s%s\n", k, v, sep)
	}
	b.WriteString("  }\n}\n")
	return b.Bytes()
}
