package wsarchive_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/wsarchive"
)

// sourceCollections are the collections MetaRush's code names: the store
// package's C… constants and any package's Collection constant (the same
// reading as the clear-out's catalogue test).
func sourceCollections(t *testing.T) map[string]string {
	t.Helper()
	found := map[string]string{}
	err := filepath.WalkDir(filepath.Join(".."), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || strings.Contains(p, "testutil") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			g, ok := decl.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, spec := range g.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					coll := f.Name.Name == "store" && strings.HasPrefix(name.Name, "C") && len(name.Name) > 1 && name.Name[1] >= 'A' && name.Name[1] <= 'Z'
					if !coll && name.Name != "Collection" {
						continue
					}
					if i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					v, _ := strconv.Unquote(lit.Value)
					if name.Name == "Collection" && !strings.Contains(v, "_") && f.Name.Name != "sound" && f.Name.Name != "catchup" {
						continue // an ID prefix or a word, not a collection
					}
					found[v] = p + ": " + name.Name
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) < 40 {
		t.Fatalf("found only %d collections: %v", len(found), found)
	}
	return found
}

// Every collection MetaRush keeps says what an archive does with it, and
// why: a new collection can never be left out of an export without a reason.
// Both what the code names and what a migrated database holds count.
func TestEveryCollectionIsClassified(t *testing.T) {
	found := sourceCollections(t)
	for v, where := range found {
		e, ok := wsarchive.Catalogue[v]
		if !ok {
			t.Errorf("collection %q (%s) is missing from wsarchive.Catalogue: say whether an export takes it as it is, transforms it or leaves it out, and why", v, where)
			continue
		}
		if strings.TrimSpace(e.Why) == "" {
			t.Errorf("collection %q: say why", v)
		}
		switch e.How {
		case wsarchive.AsIs, wsarchive.Transformed, wsarchive.LeftOut, wsarchive.InManifest:
		default:
			t.Errorf("collection %q: unknown way %q", v, e.How)
		}
	}
	for v := range wsarchive.Catalogue {
		if _, ok := found[v]; !ok {
			t.Errorf("wsarchive.Catalogue names %q, which isn't a collection any more", v)
		}
	}
	db := testutil.DB(t)
	colls, err := wsarchive.ListCollections(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range colls {
		if _, ok := wsarchive.Catalogue[c.Name]; !ok {
			t.Errorf("a migrated database holds %q, which wsarchive.Catalogue doesn't classify", c.Name)
		}
	}
	// What the docs and the plan list is the catalogue itself.
	if got := len(wsarchive.Classify()); got != len(wsarchive.Catalogue) {
		t.Fatalf("Classify lists %d of %d", got, len(wsarchive.Catalogue))
	}
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "workspace-export.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range wsarchive.Classify() {
		if !strings.Contains(string(doc), "| `"+c.Name+"` |") {
			t.Errorf("docs/workspace-export.md's table doesn't say what an export does with %s", c.Name)
		}
	}
}
