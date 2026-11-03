// Package jobtypes reads every job type internal/jobs declares, from its
// source, so tests elsewhere can hold their words to all of them without
// importing jobs (logbook can't: jobs imports it). A type added to jobs'
// const block shows up here at once.
package jobtypes

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// All are the job types (their values: "render", "render-shape", …) the
// constants named Type… in internal/jobs/queue.go declare.
func All() ([]string, error) {
	_, here, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(here), "..", "..", "jobs", "queue.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, sp := range g.Specs {
			vs := sp.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if !strings.HasPrefix(n.Name, "Type") || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					v, _ := strconv.Unquote(lit.Value)
					out = append(out, v)
				}
			}
		}
	}
	return out, nil
}
