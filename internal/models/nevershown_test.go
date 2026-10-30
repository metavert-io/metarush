package models

import (
	"fmt"
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

// neverShownProblems reads Go source and says which fields a record shown whole
// would show though it shouldn't: a stored field MetaRush never shows
// (`json:"-"`) that isn't on NeverShown, and a stored field whose Go, JSON or
// BSON name looks like a secret, holding text, whose stored name isn't hidden
// (the stored name is what a shown record has, and the field needn't have a
// `json` tag at all).
func neverShownProblems(t *testing.T, path, src string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
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
		b, jsonName := strings.Split(st.Get("bson"), ",")[0], strings.Split(st.Get("json"), ",")[0]
		if b == "" || b == "-" {
			return true // not stored by that name
		}
		if st.Get("json") == "-" && b != "_id" && b != "workspaceId" && !NeverShown[b] {
			out = append(out, fmt.Sprintf("%s: the stored field %q is never shown (json:\"-\") but isn't on models.NeverShown", path, b))
		}
		secret := LooksSecret(b) || LooksSecret(jsonName)
		for _, name := range field.Names {
			secret = secret || LooksSecret(name.Name)
		}
		if secret && holdsText(field.Type) && !HiddenField(b, true) {
			out = append(out, fmt.Sprintf("%s: the stored field %q looks like a secret but a record shown whole would show it: add it to models.NeverShown", path, b))
		}
		return true
	})
	return out
}

// holdsText reports a field's type that a secret can be in (not a number, a
// yes-or-no or a time).
func holdsText(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		switch x.Name {
		case "int", "int32", "int64", "uint", "uint32", "uint64", "float32", "float64", "bool":
			return false
		}
	case *ast.StarExpr:
		return holdsText(x.X)
	case *ast.SelectorExpr:
		return x.Sel.Name != "Time" && x.Sel.Name != "Duration"
	}
	return true
}

// Every stored field MetaRush never shows (`json:"-"` beside a bson name, in
// any package) is on NeverShown, so a record shown whole never shows it, and
// every stored field with a secret-looking name is hidden, by NeverShown or by
// its name. An ID (_id, workspaceId) is shown anyway.
func TestNeverShownIsWhole(t *testing.T) {
	root := filepath.Join("..")
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") || strings.Contains(p, "testutil") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, problem := range neverShownProblems(t, p, string(src)) {
			t.Error(problem)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The check itself: a new field is caught however it is tagged, and a record's
// secret-looking fields are hidden by name, but not a count of tokens.
func TestNeverShownCatchesNewFields(t *testing.T) {
	src := `package x
type R struct {
	Hidden string ` + "`bson:\"newHidden\" json:\"-\"`" + `
	APIToken string ` + "`bson:\"at\"`" + `
	Client string ` + "`bson:\"cl\" json:\"clientSecret\"`" + `
	Plain string ` + "`bson:\"plain\" json:\"plain\"`" + `
	TokenLimit int ` + "`bson:\"tl\"`" + `
	Named string ` + "`bson:\"apiToken\"`" + `
}`
	got := strings.Join(neverShownProblems(t, "x.go", src), "\n")
	for _, want := range []string{`"newHidden"`, `"at"`, `"cl"`} {
		if !strings.Contains(got, want) {
			t.Errorf("not caught: %s in\n%s", want, got)
		}
	}
	for _, clean := range []string{`"plain"`, `"tl"`, `"apiToken"`} {
		if strings.Contains(got, clean) {
			t.Errorf("caught wrongly: %s in\n%s", clean, got)
		}
	}
	for _, name := range []string{"apiToken", "client_secret", "password", "API-Key", "refreshToken", "sessionUri"} {
		if !HiddenField(name, true) {
			t.Errorf("%s isn't hidden", name)
		}
	}
	for _, name := range []string{"text", "name", "title", "assetId"} {
		if HiddenField(name, true) {
			t.Errorf("%s is hidden", name)
		}
	}
	if HiddenField("tokenLimitMillions", false) {
		t.Error("a number with token in its name is hidden")
	}
}
