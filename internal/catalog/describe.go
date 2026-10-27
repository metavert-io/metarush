package catalog

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode"

	"github.com/invopop/jsonschema"

	"github.com/metavert-io/metarush/internal/ops"
)

// Description says how to use one entry: its input, what it returns, its
// flags and an example (ops.describe, MCP's describe).
type Description struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// About is what it does.
	About string `json:"about"`
	// Command is the command line that does it, Usage the same with its
	// arguments.
	Command string `json:"command,omitempty"`
	Usage   string `json:"usage,omitempty"`
	// Changes: it changes something. PeopleOnly: only a person may do it;
	// an agent's call is refused. OwnerOnly: only an owner of the workspace.
	// ThisComputerOnly: only where the files are, never over the network.
	Changes          bool `json:"changes"`
	PeopleOnly       bool `json:"peopleOnly"`
	OwnerOnly        bool `json:"ownerOnly"`
	ThisComputerOnly bool `json:"thisComputerOnly"`
	// OwnCheck: a change it makes is checked by its own dry run or check
	// operation, whose confirmToken the input carries (edit.check, then
	// edit.apply).
	OwnCheck bool `json:"ownCheck,omitempty"`
	// Input is the JSON Schema of its input.
	Input map[string]any `json:"input"`
	// Returns are the fields of its answer.
	Returns []ReturnField `json:"returns"`
	// Example is an input to start from (angle brackets: fill in).
	Example map[string]any `json:"example"`
	// RunWith is the operation that runs another catalogue's entry, with
	// RunInput as its input.
	RunWith  string         `json:"runWith,omitempty"`
	RunInput map[string]any `json:"runInput,omitempty"`
	// RunOwnInput: the input is RunWith's own, with RunInput's fields fixed
	// (a first-party ability); otherwise it goes under RunInput's "input".
	RunOwnInput bool `json:"runOwnInput,omitempty"`

	// Title is its name in a menu; Group the part of the menu it is in.
	Title string `json:"title,omitempty"`
	Group string `json:"group,omitempty"`
	// Cost: free (no price, no key), priced (each run priced first, paid
	// to the service), keyed (it uses a key to a service, which may charge)
	// or plan (it uses the person's Claude Code).
	Cost string `json:"cost,omitempty" jsonschema:"enum=,enum=free,enum=priced,enum=keyed,enum=plan"`
	// Leaves says what leaves this computer when it runs.
	Leaves string `json:"leaves,omitempty"`
	// Requires says what it needs on a computer ("a Mac", "rclone").
	Requires []string `json:"requires,omitempty"`
	// WorksHere: it works on this computer now (absent: nobody can tell).
	WorksHere *bool `json:"worksHere,omitempty"`
	// Missing says what it needs here first; Blocked: run refuses it until
	// that is done.
	Missing string `json:"missing,omitempty"`
	Blocked bool   `json:"blocked,omitempty"`
	// Partly says what it still does here when it works here only in part.
	Partly string `json:"partly,omitempty"`
	// Note is what to know before running it, in plain words (an ability is
	// planned first).
	Note string `json:"note,omitempty"`
	// FirstParty names the first-party ability an operation is
	// ("metarush/best-moments"); MadeWith is what a receipt or a run's
	// step records for it.
	FirstParty string `json:"firstParty,omitempty"`
	MadeWith   string `json:"madeWith,omitempty"`
}

// ReturnField is one field of an answer.
type ReturnField struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// About says what it holds, when the schema says.
	About string `json:"about,omitempty"`
}

// DescribeOp describes an operation.
func DescribeOp(op *ops.Op) *Description {
	d := &Description{Name: op.Name, Kind: KindOperation, About: strings.TrimSpace(op.Summary),
		Changes: op.Mutating, PeopleOnly: op.HumanOnly, OwnerOnly: op.OwnerOnly, ThisComputerOnly: op.LocalOnly,
		OwnCheck: ChecksFirst(op),
		Input:    InputSchema(op.InType), Returns: Returns(op.OutType)}
	if c, ok := CommandFor(op.Name); ok {
		d.Command, d.Usage = c.Command, c.Usage
	}
	d.Example = Example(d.Input)
	return d
}

// InputSchema is the JSON Schema of an operation's input, as the generated
// schemas describe it (nested types under $defs): what MCP clients are given.
func InputSchema(t reflect.Type) map[string]any {
	r := &jsonschema.Reflector{AllowAdditionalProperties: false, ExpandedStruct: true}
	b, _ := json.Marshal(r.ReflectFromType(t))
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	delete(m, "$schema")
	delete(m, "$id")
	m["type"] = "object"
	if _, ok := m["properties"].(map[string]any); !ok {
		m["properties"] = map[string]any{}
	}
	return m
}

// HasField says whether a struct type (or one it embeds) has a JSON field of
// that name.
func HasField(t reflect.Type, name string) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return false
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Anonymous && HasField(f.Type, name) {
			return true
		}
		if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag == name {
			return true
		}
	}
	return false
}

// Returns lists the top-level fields of an operation's answer.
func Returns(t reflect.Type) []ReturnField {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return []ReturnField{{Name: "", Type: typeWords(t)}}
	}
	out := []ReturnField{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			tag := f.Tag.Get("json")
			name, _, _ := strings.Cut(tag, ",")
			if name == "-" {
				continue
			}
			if f.Anonymous && name == "" {
				ft := f.Type
				for ft.Kind() == reflect.Pointer {
					ft = ft.Elem()
				}
				if ft.Kind() == reflect.Struct {
					walk(ft)
					continue
				}
			}
			if name == "" {
				name = f.Name
			}
			out = append(out, ReturnField{Name: name, Type: typeWords(f.Type), About: schemaDescription(f.Tag.Get("jsonschema"))})
		}
	}
	walk(t)
	return out
}

func schemaDescription(tag string) string {
	_, after, ok := strings.Cut(tag, "description=")
	if !ok {
		return ""
	}
	// The description runs to the next tag key (",enum=" and the like).
	for _, k := range []string{",enum=", ",default=", ",minimum=", ",maximum=", ",format=", ",example=", ",title="} {
		if i := strings.Index(after, k); i >= 0 {
			after = after[:i]
		}
	}
	return strings.ReplaceAll(after, `\,`, ",")
}

// typeWords names a Go type the way JSON sees it.
func typeWords(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return "string"
		}
		return "list of " + typeWords(t.Elem())
	case reflect.Map:
		return "object"
	case reflect.Struct:
		if t.PkgPath() == "time" && t.Name() == "Time" {
			return "time"
		}
		if t.Name() != "" {
			return "object (" + t.Name() + ")"
		}
		return "object"
	case reflect.Interface:
		return "any"
	}
	return t.Kind().String()
}

// Example is an input to start from: the required fields, each a placeholder
// in angle brackets, never a value: no real ID, and no yes, number or choice
// filled in for the caller (a consent's "adult" or "self" is the person's to
// say, never the example's).
func Example(schema map[string]any) map[string]any {
	props, _ := schema["properties"].(map[string]any)
	req, _ := schema["required"].([]any)
	out := map[string]any{}
	names := make([]string, 0, len(req))
	for _, r := range req {
		if s, ok := r.(string); ok {
			names = append(names, s)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		p, _ := props[n].(map[string]any)
		out[n] = exampleValue(n, p)
	}
	return out
}

func exampleValue(name string, p map[string]any) any {
	if enum, ok := p["enum"].([]any); ok && len(enum) > 0 {
		var vs []string
		for _, e := range enum {
			vs = append(vs, fmt.Sprint(e))
		}
		return "<one of: " + strings.Join(vs, ", ") + ">"
	}
	switch p["type"] {
	case "integer", "number":
		return "<" + spaced(name) + ": a number>"
	case "boolean":
		return "<" + spaced(name) + ": true or false>"
	case "array":
		return []any{}
	case "object":
		return map[string]any{}
	}
	return "<" + spaced(name) + ">"
}

// spaced: "revisionId" → "revision id".
func spaced(name string) string {
	var b strings.Builder
	for i, r := range name {
		if unicode.IsUpper(r) && i > 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}
