package ops

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// SetPurgeStep stops a clear-out after a step (tests in ops_test show that
// running it again finishes it).
func SetPurgeStep(f func(step string) error) { purgeStep = f }

// Every collection MetaRush keeps says what clearing out a project (D196)
// does to it and how it can use stored files, so a new collection can't be
// missed by the clear-out or by the check that keeps shared files: the
// collections are the store package's C… constants and any package's
// Collection constant.
func TestPurgeCatalogueIsWhole(t *testing.T) {
	root := filepath.Join("..")
	found := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
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
	for v, where := range found {
		e, ok := purgeCatalogue[v]
		if !ok {
			t.Errorf("collection %q (%s) is missing from purgeCatalogue: say what clearing out a project does to it and how it uses stored files", v, where)
			continue
		}
		if e.Purge == "" {
			t.Errorf("collection %q: say what clearing out a project does to it", v)
		}
		if e.Media == mediaPaths {
			if _, ok := mediaFields[v]; !ok {
				t.Errorf("collection %q reads stored files through fields, but mediaFields doesn't name them", v)
			}
		}
	}
	for v := range purgeCatalogue {
		if _, ok := found[v]; !ok {
			t.Errorf("purgeCatalogue names %q, which isn't a collection any more", v)
		}
	}
}

// markHashes finds a file's SHA-256 wherever it is in a document's bytes: a
// field of its own, a key, inside a longer run of hex.
func TestMarkHashes(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	other := strings.Repeat("cd", 32)
	want := map[string]bool{sha: true, other: true}
	for _, doc := range []string{"sha256\x00" + sha + "\x00", "key\x00ws/w/objects/ab/" + sha + "\x00", "x" + strings.Repeat("0", 10) + sha} {
		used := map[string]bool{}
		markHashes([]byte(doc), want, used)
		if !used[sha] || used[other] {
			t.Fatalf("%q: %v", doc, used)
		}
	}
	used := map[string]bool{}
	markHashes([]byte("nothing "+sha[:63]), want, used)
	if len(used) != 0 {
		t.Fatalf("a shorter run matched: %v", used)
	}
}
