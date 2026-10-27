package plainwords

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The messages MetaRush writes for people — errors and their hints, edit
// problems, quality-check findings, to-dos, notifications, activity rows, job
// advice, log summaries — are plain words (skills/_shared/voice.md). This test
// reads every Go source file and checks the text at the places such messages
// are made: apperr.New/Wrap and WithHint, editspec's errI/warnI, the CLI's
// fail, and sentence-like strings given to fields people read (Message, Hint,
// Summary, Title, Body, Text, Question, Advice…). It also catches IDs and
// file paths put into those messages through %s. Intentional exceptions are in
// testdata/messages-allow.txt, each with its reason.

// messageCalls are functions whose string arguments are read by people:
// name → index of the format/message argument (the rest are its values).
var messageCalls = map[string]int{
	"apperr.New":  1,
	"apperr.Wrap": 2,
	"WithHint":    0,
	"errI":        2,
	"warnI":       2,
	"infoI":       2,
}

// messageFields are struct fields whose text people read.
var messageFields = map[string]bool{
	"Message": true, "Hint": true, "Summary": true, "Title": true, "Body": true, "Text": true, "Question": true,
	"Advice": true, "Headline": true, "StageLabel": true, "Problem": true, "Next": true, "Why": true, "Says": true,
	"Plain": true, "Explanation": true, "Subtitle": true, "Detail": true, "Note": true, "Context": true,
}

// idArg matches values that are IDs or paths when they are put into a message.
var idArg = regexp.MustCompile(`(^|\.)([A-Za-z]*ID|[A-Za-z]*Id|id|ID|[A-Za-z]*Path|path|[A-Za-z]*Dir|dir|absPath|full)$`)

type msgFinding struct {
	pos  string // file:line
	file string
	text string
	what string
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatalf("repo root not found from %s", dir)
	}
	return dir
}

// literalText joins the literal parts of a string expression (a literal, a
// concatenation, fmt.Sprintf/Errorf) and returns the non-literal values put
// into it. ok is false when the expression has no literal text.
func literalText(e ast.Expr) (text string, args []ast.Expr, ok bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", nil, false
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return "", nil, false
		}
		return s, nil, true
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", nil, false
		}
		l, la, lok := literalText(v.X)
		r, ra, rok := literalText(v.Y)
		if !lok && !rok {
			return "", nil, false
		}
		if !lok {
			la, l = append(la, v.X), l+"…"
		}
		if !rok {
			ra, r = append(ra, v.Y), r+"…"
		}
		return l + r, append(la, ra...), true
	case *ast.CallExpr:
		if name := callName(v); (name == "fmt.Sprintf" || name == "fmt.Errorf" || name == "printer.Sprintf") && len(v.Args) > 0 {
			s, _, ok := literalText(v.Args[0])
			return s, v.Args[1:], ok
		}
	case *ast.ParenExpr:
		return literalText(v.X)
	}
	return "", nil, false
}

func callName(c *ast.CallExpr) string {
	switch f := c.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if x, ok := f.X.(*ast.Ident); ok {
			return x.Name + "." + f.Sel.Name
		}
		return f.Sel.Name // a method: e.WithHint
	}
	return ""
}

// cliUsageError reports whether a call builds (or adds a hint to) an
// E_INVALID_INPUT error: in the CLI that is a usage error about the command
// just typed.
func cliUsageError(c *ast.CallExpr) bool {
	for e := ast.Expr(c); e != nil; {
		call, ok := e.(*ast.CallExpr)
		if !ok {
			return false
		}
		if callName(call) == "apperr.New" || callName(call) == "apperr.Wrap" {
			return len(call.Args) > 0 && exprString(call.Args[0]) == "apperr.InvalidInput"
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		e = sel.X // e.g. apperr.New(…).WithHint(…)
	}
	return false
}

func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.IndexExpr:
		return exprString(v.X) + "[]"
	}
	return ""
}

// verbs replaces format verbs with a neutral word so the checker reads the sentence.
var verbs = regexp.MustCompile(`%[-+# 0-9.*]*[a-zA-Z%]`)

func sentenceLike(s string) bool {
	return strings.Contains(strings.TrimSpace(s), " ")
}

func scanMessages(t *testing.T) []msgFinding {
	root := repoRoot(t)
	var out []msgFinding
	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "testdata", "testutil", "plainwords":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(fset, p, src, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		check := func(at token.Pos, e ast.Expr, where string, allow ...string) {
			text, args, ok := literalText(e)
			if !ok {
				return
			}
			plain := verbs.ReplaceAllString(text, "x")
			pos := fmt.Sprintf("%s:%d", rel, fset.Position(at).Line)
			for _, fd := range Allow(allow).Filter(Check(plain)) {
				out = append(out, msgFinding{pos, rel, text, where + ": " + fd.String()})
			}
			for _, a := range args {
				if s := exprString(a); s != "" && idArg.MatchString(s) {
					out = append(out, msgFinding{pos, rel, text, where + ": shows " + s})
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.CallExpr:
				name := callName(v)
				if i, ok := messageCalls[name]; ok && i < len(v.Args) {
					var usage []string
					if strings.HasPrefix(rel, "internal/cli/") && cliUsageError(v) {
						usage = []string{KindFlag, KindCommand}
					}
					check(v.Pos(), v.Args[i], name, usage...)
				}
				if strings.HasPrefix(rel, "internal/cli/") && name == "fail" && len(v.Args) >= 4 {
					// A usage error is about the command just typed: it may name that
					// command's flags and its help.
					var usage []string
					if c := exprString(v.Args[1]); c == "apperr.InvalidInput" {
						usage = []string{KindFlag, KindCommand}
					} else if lit, ok := v.Args[1].(*ast.BasicLit); ok && lit.Value == `"E_INVALID_INPUT"` {
						usage = []string{KindFlag, KindCommand}
					}
					check(v.Pos(), v.Args[2], "fail", usage...)
					check(v.Pos(), v.Args[3], "fail hint", usage...)
				}
			case *ast.KeyValueExpr:
				if k, ok := v.Key.(*ast.Ident); ok && messageFields[k.Name] {
					if text, _, ok := literalText(v.Value); ok && sentenceLike(text) {
						check(v.Pos(), v.Value, k.Name)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type allowEntry struct {
	file, text, reason string
	used               bool
}

// loadAllow reads testdata/messages-allow.txt: "file | text it contains | reason".
func loadAllow(t *testing.T, name string) []*allowEntry {
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []*allowEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, " | ")
		if len(parts) != 3 || strings.TrimSpace(parts[2]) == "" {
			t.Fatalf("%s: %q: want \"file | text | reason\"", name, line)
		}
		out = append(out, &allowEntry{file: strings.TrimSpace(parts[0]), text: strings.TrimSpace(parts[1]), reason: parts[2]})
	}
	return out
}

func TestMessagesArePlain(t *testing.T) {
	allow := loadAllow(t, "messages-allow.txt")
	found := scanMessages(t)
	var bad []string
	for _, f := range found {
		ok := false
		for _, a := range allow {
			if a.file == f.file && strings.Contains(f.text, a.text) {
				a.used, ok = true, true
			}
		}
		if !ok {
			bad = append(bad, fmt.Sprintf("%s: %s\n    %q", f.pos, f.what, f.text))
		}
	}
	sort.Strings(bad)
	if report := os.Getenv("PLAINWORDS_REPORT"); report != "" {
		_ = os.WriteFile(report, []byte(strings.Join(bad, "\n")+"\n"), 0o644)
	}
	for _, b := range bad {
		t.Error(b)
	}
	for _, a := range allow {
		if !a.used {
			t.Errorf("messages-allow.txt: %s | %s matches nothing any more; remove it", a.file, a.text)
		}
	}
}
