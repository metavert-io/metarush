package models

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// Every stored field MetaRush never shows (`json:"-"` beside a bson name, in
// any package) is on NeverShown, so a record shown whole never shows it.
// An ID (_id, workspaceId) is shown anyway.
func TestNeverShownIsWhole(t *testing.T) {
	root := filepath.Join("..")
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
		ast.Inspect(f, func(n ast.Node) bool {
			field, ok := n.(*ast.Field)
			if !ok || field.Tag == nil {
				return true
			}
			tag, err := strconv.Unquote(field.Tag.Value)
			if err != nil {
				return true
			}
			st := reflect.StructTag(tag)
			b, json := st.Get("bson"), st.Get("json")
			if json != "-" || b == "" || b == "-" {
				return true
			}
			name := strings.Split(b, ",")[0]
			if name == "_id" || name == "workspaceId" || name == "" {
				return true
			}
			if !NeverShown[name] {
				t.Errorf("%s: the stored field %q is never shown (json:\"-\") but isn't on models.NeverShown", p, name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
