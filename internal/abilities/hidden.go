package abilities

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/plainwords"
)

// Hidden characters in an ability (D230's check). An owner approves a version
// from what its manifest says (the approval page, its inputs, its terms), and
// Claude learns how to use it from notes.md — so both must say what they
// seem to: a manifest or notes with characters that hide or reorder words, or
// codes a terminal obeys, is refused rather than cleaned (the version is
// named by its bytes, so what is approved is what was checked).

// hiddenWords is how a problem names them.
const hiddenWords = "remove its hidden characters (codes a terminal obeys, or characters that hide or reorder words): an owner approves what they read"

// oneLineField are the fields a person reads as one line: a line break in one
// could pass for another line of the approval page.
var oneLineField = map[string]bool{"title": true, "does": true, "licence": true, "cost.service": true, "cost.per": true, "local.service": true}

// hiddenProblems walks every word of a manifest — values and the names of
// inputs and outputs alike — for hidden characters, and the one-line fields
// for line breaks.
func hiddenProblems(m *Manifest) []Problem {
	seen := map[string]bool{}
	var ps []Problem
	add := func(field, text string) {
		if !seen[field] {
			seen[field] = true
			ps = append(ps, Problem{Field: field, Text: text})
		}
	}
	var walk func(v reflect.Value, path string)
	walk = func(v reflect.Value, path string) {
		switch v.Kind() {
		case reflect.String:
			s := v.String()
			if plainwords.HasHidden(s) {
				add(path, hiddenWords)
			} else if (oneLineField[path] || strings.HasSuffix(path, ".says")) && strings.ContainsAny(s, "\n\r\t") {
				add(path, "say it in one line")
			}
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem(), path)
			}
		case reflect.Slice, reflect.Array:
			for i := 0; i < v.Len(); i++ {
				walk(v.Index(i), fmt.Sprintf("%s[%d]", path, i))
			}
		case reflect.Map:
			keys := v.MapKeys()
			sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
			for _, k := range keys {
				name := fmt.Sprint(k.Interface())
				if plainwords.HasHidden(name) {
					add(path, hiddenWords)
					name = plainwords.Clean(name)
				}
				walk(v.MapIndex(k), join(path, name))
			}
		case reflect.Struct:
			t := v.Type()
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				if !f.IsExported() {
					continue
				}
				name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
				if name == "-" {
					continue
				}
				if name == "" {
					name = strings.ToLower(f.Name)
				}
				walk(v.Field(i), join(path, name))
			}
		}
	}
	walk(reflect.ValueOf(m).Elem(), "")
	return ps
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// fileProblem says where a file a person or Claude reads — ability.yaml as
// written (its comments too, which the manifest's values leave out) or
// notes.md — has hidden characters, if it has any.
func fileProblem(name string, b []byte) *Problem {
	why := hiddenWords
	if name == NotesFile {
		why = strings.Replace(hiddenWords, "an owner approves what they read", "Claude learns from these words", 1)
	}
	for i, line := range strings.Split(string(b), "\n") {
		if plainwords.HasHidden(strings.TrimSuffix(line, "\r")) {
			return &Problem{Field: name, Text: fmt.Sprintf("line %d: %s", i+1, why)}
		}
	}
	return nil
}
