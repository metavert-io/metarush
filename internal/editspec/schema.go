package editspec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/invopop/jsonschema"
	sjs "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

// The Go structs in this package are the source of truth. JSON Schema documents
// are generated from them (for runtime validation, for skills to read, and for
// TypeScript type generation) — see `metarush schema export`.

func reflector() *jsonschema.Reflector {
	return &jsonschema.Reflector{
		DoNotReference:             false,
		ExpandedStruct:             false,
		AllowAdditionalProperties:  false,
		RequiredFromJSONSchemaTags: false,
	}
}

// SchemaFor returns the JSON Schema for a Go value.
func SchemaFor(v any, id, title string) *jsonschema.Schema {
	s := reflector().Reflect(v)
	s.ID = jsonschema.ID(id)
	s.Title = title
	return s
}

// Schemas returns every published schema document keyed by file name.
func Schemas() map[string]*jsonschema.Schema {
	out := map[string]*jsonschema.Schema{
		"edit-spec.schema.json":   SchemaFor(&Spec{}, "urn:metarush:edit-spec:1", "MetaRush edit spec v1"),
		"edit-plan.schema.json":   SchemaFor(&Plan{}, "urn:metarush:edit-plan:1", "MetaRush edit plan v1"),
		"render-plan.schema.json": SchemaFor(&RenderPlan{}, "urn:metarush:render-plan:1", "MetaRush render plan v1"),
	}
	for _, t := range Templates {
		name := "template." + t.ID + ".schema.json"
		out[name] = SchemaFor(t.params, "urn:metarush:template:"+t.ID+":"+t.Version, "Params for "+t.ID)
	}
	return out
}

type compiled struct {
	plan      *sjs.Schema
	templates map[string]*sjs.Schema
}

var (
	compileOnce sync.Once
	comp        compiled
	compileErr  error
)

func compileAll() {
	c := sjs.NewCompiler()
	add := func(name string, s *jsonschema.Schema) {
		b, err := json.Marshal(s)
		if err != nil {
			compileErr = err
			return
		}
		doc, err := sjs.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			compileErr = err
			return
		}
		if err := c.AddResource(name, doc); err != nil {
			compileErr = err
		}
	}
	add("plan.json", SchemaFor(&Plan{}, "", ""))
	comp.templates = map[string]*sjs.Schema{}
	for _, t := range Templates {
		add(t.ID+".json", SchemaFor(t.params, "", ""))
	}
	if compileErr != nil {
		return
	}
	comp.plan, compileErr = c.Compile("plan.json")
	for _, t := range Templates {
		if compileErr != nil {
			return
		}
		comp.templates[t.ID], compileErr = c.Compile(t.ID + ".json")
	}
}

// ValidatePlanJSON checks raw plan JSON against the generated schema and returns
// path-addressed issues Claude can act on.
func ValidatePlanJSON(raw []byte) []Issue {
	compileOnce.Do(compileAll)
	if compileErr != nil {
		return []Issue{{Severity: "error", Code: "schema_internal", Message: "MetaRush couldn't load its own list of what a cut may contain (" + compileErr.Error() + ")."}}
	}
	doc, err := sjs.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return []Issue{{Severity: "error", Code: "invalid_json", Message: "The change can't be read (it isn't valid JSON: " + err.Error() + ")."}}
	}
	return schemaIssues(comp.plan.Validate(doc), "")
}

// ValidateTemplateParams validates overlay params for a template.
func ValidateTemplateParams(template string, params json.RawMessage, path string) []Issue {
	compileOnce.Do(compileAll)
	s, ok := comp.templates[template]
	if !ok {
		return []Issue{{Severity: "error", Code: "unknown_template", Path: path, Message: fmt.Sprintf("There's no graphics template called %q.", template)}}
	}
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	doc, err := sjs.UnmarshalJSON(bytes.NewReader(params))
	if err != nil {
		return []Issue{{Severity: "error", Code: "invalid_json", Path: path, Message: "The graphic's settings can't be read (they aren't valid JSON: " + err.Error() + ")."}}
	}
	return schemaIssues(s.Validate(doc), path)
}

func schemaIssues(err error, prefix string) []Issue {
	if err == nil {
		return nil
	}
	ve, ok := err.(*sjs.ValidationError)
	if !ok {
		return []Issue{{Severity: "error", Code: "schema", Path: prefix, Message: "A value is outside what MetaRush accepts (" + err.Error() + ")."}}
	}
	var out []Issue
	var walk func(e *sjs.ValidationError)
	walk = func(e *sjs.ValidationError) {
		if len(e.Causes) == 0 {
			loc := "/" + strings.Join(e.InstanceLocation, "/")
			if loc == "/" {
				loc = ""
			}
			out = append(out, Issue{Severity: "error", Code: "schema", Path: prefix + loc, Message: schemaMessage(e)})
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// schemaMessage says what is wrong with a value in plain words; the path of
// the issue keeps the exact field.
func schemaMessage(e *sjs.ValidationError) string {
	field := ""
	for i := len(e.InstanceLocation) - 1; i >= 0; i-- {
		if _, err := strconv.Atoi(e.InstanceLocation[i]); err != nil {
			field = e.InstanceLocation[i]
			break
		}
	}
	the := upperFirst(schemaFieldWords(field))
	num := func(r *big.Rat) string {
		f, _ := r.Float64()
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	switch k := e.ErrorKind.(type) {
	case *kind.Maximum:
		return fmt.Sprintf("%s is %s, but it can be at most %s.", the, num(k.Got), num(k.Want))
	case *kind.ExclusiveMaximum:
		return fmt.Sprintf("%s is %s, but it has to be less than %s.", the, num(k.Got), num(k.Want))
	case *kind.Minimum:
		return fmt.Sprintf("%s is %s, but it has to be at least %s.", the, num(k.Got), num(k.Want))
	case *kind.ExclusiveMinimum:
		return fmt.Sprintf("%s is %s, but it has to be more than %s.", the, num(k.Got), num(k.Want))
	case *kind.MaxLength:
		return fmt.Sprintf("%s is %d characters long; it can be at most %d.", the, k.Got, k.Want)
	case *kind.MinLength:
		if k.Got == 0 {
			return fmt.Sprintf("%s is empty; write something there.", the)
		}
		return fmt.Sprintf("%s is %d characters long; it needs at least %d.", the, k.Got, k.Want)
	case *kind.MaxItems:
		return fmt.Sprintf("%s can have at most %d (there are %d).", the, k.Want, k.Got)
	case *kind.MinItems:
		if k.Got == 0 {
			return fmt.Sprintf("%s can't be empty.", the)
		}
		return fmt.Sprintf("%s needs at least %d (there are %d).", the, k.Want, k.Got)
	case *kind.Required:
		names := make([]string, len(k.Missing))
		for i, m := range k.Missing {
			names[i] = schemaFieldWords(m)
		}
		return upperFirst(strings.Join(names, " and ")) + " " + map[bool]string{true: "is", false: "are"}[len(names) == 1] + " missing; it has to be given here."
	case *kind.AdditionalProperties:
		return fmt.Sprintf("MetaRush doesn't know the setting%s “%s” here.", map[bool]string{true: "", false: "s"}[len(k.Properties) == 1], strings.Join(k.Properties, "”, “"))
	case *kind.Enum:
		want := make([]string, len(k.Want))
		for i, w := range k.Want {
			want[i] = fmt.Sprint(w)
		}
		return fmt.Sprintf("%s can't be %v; use %s.", the, k.Got, strings.Join(want, ", "))
	case *kind.Type:
		words := map[string]string{"string": "text", "number": "a number", "integer": "a whole number", "boolean": "true or false", "object": "a group of settings", "array": "a list", "null": "empty"}
		want := make([]string, len(k.Want))
		for i, w := range k.Want {
			want[i] = firstNonEmptyStr(words[w], w)
		}
		return fmt.Sprintf("%s should be %s, not %s.", the, strings.Join(want, " or "), firstNonEmptyStr(words[k.Got], k.Got))
	case *kind.Pattern:
		return fmt.Sprintf("%s (%q) isn't written the way MetaRush expects.", the, k.Got)
	}
	return "A value is outside what MetaRush accepts (" + e.ErrorKind.LocalizedString(printer) + ")."
}

// schemaFieldWords names a spec field for people ("the focus across").
func schemaFieldWords(field string) string {
	words := map[string]string{"focusX": "focus across", "focusY": "focus down", "fadeIn": "fade-in", "fadeOut": "fade-out", "gainDb": "volume",
		"duckDb": "dip under voices", "duration": "length", "at": "start time", "in": "start", "out": "end", "bullets": "list of points",
		"cta": "call to action", "subline": "second line", "attribution": "name of who said it", "value": "number", "fps": "frame rate",
		"id": "name", "maxWordsPerLine": "words per line", "kicker": "label above the title", "url": "address", "schemaVersion": "cut format version"}
	if field == "" {
		return "a value"
	}
	w, ok := words[field]
	if !ok {
		var b strings.Builder
		for i, r := range field {
			if i > 0 && r >= 'A' && r <= 'Z' {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
		}
		w = strings.ToLower(b.String())
	}
	return "the " + w
}

// Hash returns the content hash of a normalized spec (canonical JSON).
func Hash(s *Spec) string {
	b, _ := json.Marshal(s)
	var v any
	_ = json.Unmarshal(b, &v)
	canon, _ := json.Marshal(v) // map keys sorted by encoding/json
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:])
}

// Clone deep-copies a spec.
func Clone(s *Spec) *Spec {
	b, _ := json.Marshal(s)
	var out Spec
	_ = json.Unmarshal(b, &out)
	return &out
}

// equalJSON compares two values by their JSON encoding.
func equalJSON(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if reflect.DeepEqual(ab, bb) {
		return true
	}
	var av, bv any
	_ = json.Unmarshal(ab, &av)
	_ = json.Unmarshal(bb, &bv)
	return reflect.DeepEqual(av, bv)
}
