package mcp

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/catalog"
	"github.com/metavert-io/metarush/internal/ops"
)

// ToolList lists the tools a server offers for a toolset ("" is the core),
// without the ones that change MetaRush unless allowChanges.
func ToolList(set string, allowChanges bool) ([]*Tool, error) {
	return ToolListFor(set, AccessOf(allowChanges, false))
}

// ToolListFor lists the tools a connection of the given access offers for a
// toolset ("" is the core): reads only; the changes a draft may hold, each
// saving one (Propose); or every change (Changes).
func ToolListFor(set string, acc Access) ([]*Tool, error) {
	if set == ToolsetAll {
		return toolsAll(acc)
	}
	defs, err := toolsetDefs(set)
	if err != nil {
		return nil, err
	}
	var out []*Tool
	seen := map[string]bool{}
	for _, d := range defs {
		if d.only != "" && d.only != acc {
			continue
		}
		ts, err := buildTools(d, acc)
		if err != nil {
			return nil, fmt.Errorf("mcp: toolset %s: %w", set, err)
		}
		for _, t := range ts {
			if seen[t.Name] {
				return nil, fmt.Errorf("mcp: toolset %s has two tools named %s", set, t.Name)
			}
			seen[t.Name] = true
			out = append(out, t)
		}
	}
	return out, nil
}

// buildTools makes a definition's tools: none for a change a connection
// doesn't offer, the read half and (changes offered) the change half of a
// group.
func buildTools(d toolDef, acc Access) ([]*Tool, error) {
	switch d.special {
	case toolFind:
		return []*Tool{findTool()}, nil
	case toolDescribe:
		return []*Tool{describeTool()}, nil
	case toolRun:
		return []*Tool{runTool(acc)}, nil
	case toolRunChange:
		if !acc.offersChanges() {
			return nil, nil
		}
		return []*Tool{runChangeTool(acc)}, nil
	}
	if d.name == "" && len(d.ops) == 1 {
		op, ok := ops.Get(d.ops[0])
		switch {
		case !ok:
			return nil, fmt.Errorf("no operation %s", d.ops[0])
		case op.HumanOnly:
			return nil, fmt.Errorf("%s is people only and is never offered", op.Name)
		case !acc.offers(op):
			return nil, nil
		}
		t := toolForAccess(op, acc)
		if d.about != "" {
			// A shorter description than the operation's summary.
			t.about = strings.TrimSuffix(d.about, ".") + "."
			t.Description = strings.Replace(t.Description, strings.TrimSuffix(op.Summary, ".")+".", t.about, 1)
		}
		return []*Tool{t}, nil
	}
	var out []*Tool
	reads, err := modeTool(d, false, acc)
	if err != nil {
		return nil, err
	}
	if reads != nil {
		out = append(out, reads)
	}
	if acc.offersChanges() {
		changes, err := modeTool(d, true, acc)
		if err != nil {
			return nil, err
		}
		if changes != nil {
			out = append(out, changes)
		}
	}
	return out, nil
}

// modeTool is one half of a group as a tool: its reads (named for the
// group) or its changes (<name>_change). Its mode chooses the operation, and
// its input has every field of theirs (a field only other modes take is left
// out of a call); it knows the other half's modes, to say which tool has
// them.
func modeTool(d toolDef, changes bool, acc Access) (*Tool, error) {
	name, about, partner := d.name, d.about, d.name+changeSuffix
	if changes {
		name, about, partner = d.name+changeSuffix, d.changeAbout, d.name
	}
	t := &Tool{Name: name, partner: partner, changes: changes, modes: map[string]*ops.Op{}, allModes: map[string]*ops.Op{},
		fields: map[string]map[string]bool{}, anyField: map[string]bool{}, renamed: map[string]map[string]string{}}
	props := map[string]any{}
	defs := map[string]any{}
	var lines []string
	readOnly, destroys, twoStep, own, twoStepOwnField := true, false, false, false, false
	for _, spec := range d.ops {
		mode, name := modeOf(d.name, spec)
		op, ok := ops.Get(name)
		switch {
		case !ok:
			return nil, fmt.Errorf("tool %s: no operation %s", d.name, name)
		case op.HumanOnly:
			return nil, fmt.Errorf("tool %s: %s is people only and is never offered", d.name, op.Name)
		case strings.Contains(mode, ".") || mode == "":
			return nil, fmt.Errorf("tool %s: %s needs a mode name (mode=%s)", d.name, op.Name, op.Name)
		case t.allModes[mode] != nil:
			return nil, fmt.Errorf("tool %s: two modes named %s", d.name, mode)
		}
		t.allModes[mode] = op
		if op.Mutating != changes || !acc.offers(op) {
			continue // the other half's, or a change no draft may hold
		}
		t.modes[mode] = op
		t.modeOrder = append(t.modeOrder, mode)
		s := inputSchema(op.InType)
		t.fields[mode] = map[string]bool{}
		opProps, _ := s["properties"].(map[string]any)
		req, _ := s["required"].([]any)
		for _, k := range sortedKeys(opProps) {
			field := opProps[k]
			if k == "mode" {
				// The operation's own mode: <mode>Mode in this tool, whose
				// mode chooses the operation.
				alias := mode + "Mode"
				if _, clash := opProps[alias]; clash {
					return nil, fmt.Errorf("tool %s: %s has fields mode and %s", d.name, op.Name, alias)
				}
				t.renamed[mode] = map[string]string{alias: k}
				for i, r := range req {
					if r == k {
						req[i] = alias
					}
				}
				k = alias
			}
			t.fields[mode][k], t.anyField[k] = true, true
			if have, ok := props[k]; ok && !sameJSON(have, field) {
				props[k] = mergedField(have, field)
				continue
			}
			props[k] = field
		}
		if ds, ok := s["$defs"].(map[string]any); ok {
			for k, v := range ds {
				if have, ok := defs[k]; ok && !sameJSON(have, v) {
					return nil, fmt.Errorf("tool %s: two operations define %s differently", d.name, k)
				}
				defs[k] = v
			}
		}
		mark := ""
		if op.Mutating {
			readOnly = false
			destroys = destroys || destructive(op.Name)
			switch {
			case acc == Propose:
				mark = " [saves a draft]"
			case ownCheck(op):
				own, mark = true, " [changes, checks first]"
			case confirmKey(op) != "confirmToken":
				// Its own confirmToken is for something it checks later
				// (brand.set's cut); this tool's token goes beside it.
				twoStepOwnField, mark = true, " [changes; this tool's token in mcpConfirmToken]"
			default:
				twoStep, mark = true, " [changes]"
			}
		}
		lines = append(lines, mode+needsWords(s)+": "+strings.TrimSuffix(catalog.Short(strings.TrimSpace(op.Summary)), ".")+mark)
	}
	if len(t.modes) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(about) == "" {
		return nil, fmt.Errorf("group %s: say what its %s are for", d.name, map[bool]string{true: "changes", false: "reads"}[changes])
	}
	props["mode"] = map[string]any{"type": "string", "enum": slices.Clone(t.modeOrder), "description": "Which of the tool's operations to run (see the modes in the description)."}
	desc := strings.TrimSuffix(about, ".") + ". Modes — " + strings.Join(lines, "; ") + "."
	t.about = desc
	if !readOnly && acc == Propose {
		// Every change here is saved as a draft: no tokens, a reason instead.
		props[draftNoteField] = map[string]any{"type": "string", "description": "Why, in a sentence or two: the person reads it beside the draft."}
		t.Description = desc + strings.TrimSuffix(draftWords, ".") + " (" + draftNoteField + ": why)."
		schema := map[string]any{"type": "object", "properties": props, "required": []any{"mode"}, "additionalProperties": false}
		if len(defs) > 0 {
			schema["$defs"] = defs
		}
		t.InputSchema = schema
		t.Annotations = map[string]any{"readOnlyHint": false, "openWorldHint": false, "destructiveHint": false}
		return t, nil
	}
	if twoStep || own || twoStepOwnField {
		tok := map[string]any{"type": "string"}
		switch {
		case twoStep && (own || twoStepOwnField):
			tok["description"] = "A change's confirmToken: its own check's or dryRun's for a mode that has one, else this tool's first call's for exactly these arguments."
		case own || twoStepOwnField:
			tok["description"] = "The confirmToken the change's own check or dryRun returned."
		default:
			tok["description"] = "The confirmToken the first call returned for exactly these arguments."
		}
		props["confirmToken"] = tok
	}
	if twoStepOwnField {
		props["mcpConfirmToken"] = map[string]any{"type": "string", "description": "This tool's token for a mode that also takes a confirmToken of its own: its first call returns it."}
	}
	if twoStep || twoStepOwnField {
		desc += " A [changes] mode works in two steps: the first call only says what it would do and returns a token; call again with the same arguments plus that token to do it."
	}
	if own {
		desc += " A mode that checks first takes dryRun (or its check mode), which returns the confirmToken the change needs."
	}
	t.Description = desc
	schema := map[string]any{"type": "object", "properties": props, "required": []any{"mode"}, "additionalProperties": false}
	if len(defs) > 0 {
		schema["$defs"] = defs
	}
	t.InputSchema = schema
	t.Annotations = map[string]any{"readOnlyHint": readOnly, "openWorldHint": false}
	if !readOnly {
		t.Annotations["destructiveHint"] = destroys
	}
	return t, nil
}

// needsWords names a mode's required fields: " (projectId, at)".
func needsWords(schema map[string]any) string {
	req, _ := schema["required"].([]any)
	var names []string
	for _, r := range req {
		if s, ok := r.(string); ok {
			names = append(names, s)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return " (" + strings.Join(names, ", ") + ")"
}

// mergedField is a field two modes take in different forms: either form,
// each with its description.
func mergedField(a, b any) any {
	var alts []any
	for _, x := range []any{a, b} {
		if m, ok := x.(map[string]any); ok && m["description"] == mergedWords {
			if forms, ok := m["anyOf"].([]any); ok {
				alts = append(alts, forms...)
				continue
			}
		}
		alts = append(alts, x)
	}
	return map[string]any{"anyOf": alts, "description": mergedWords}
}

const mergedWords = "Its form depends on the mode."

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The core's own tools.

func findTool() *Tool {
	t := &Tool{Name: toolFind, special: toolFind,
		Description: "Find the MetaRush operation for a task described in plain words (\"make a review copy\"): best matches first, " +
			"one line each (name — what it does). Then describe one and run it.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []any{"task"}, "properties": map[string]any{
			"task":  map[string]any{"type": "string", "description": "The task in plain words."},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": catalog.MaxLimit, "description": "How many (8 unless asked)."},
		}},
		Annotations: map[string]any{"readOnlyHint": true, "openWorldHint": false}}
	t.about = t.Description
	return t
}

func describeTool() *Tool {
	t := &Tool{Name: toolDescribe, special: toolDescribe,
		Description: "Describe a name find returned: its input (JSON Schema, with an example), its answer's fields, its command, " +
			"and whether it changes anything or is people only.",
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []any{"name"}, "properties": map[string]any{
			"name": map[string]any{"type": "string", "description": "A name find returned."},
		}},
		Annotations: map[string]any{"readOnlyHint": true, "openWorldHint": false}}
	t.about = t.Description
	return t
}

func runTool(acc Access) *Tool {
	d := "Run a name find returned that only reads, with the input its describe shows. "
	switch acc {
	case Changes:
		d += "It never changes anything: changes are run_change's. "
	case Propose:
		d += "It never changes anything: run_change saves changes as drafts. "
	default:
		d += "This connection is read-only. "
	}
	d += "People-only things are refused."
	props := map[string]any{
		"name":  map[string]any{"type": "string", "description": "A name find returned."},
		"input": map[string]any{"type": "object", "description": "Its input (omit when it takes none)."},
	}
	return &Tool{Name: toolRun, special: toolRun, Description: d, about: d,
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []any{"name"}, "properties": props},
		Annotations: map[string]any{"readOnlyHint": true, "openWorldHint": false}}
}

// runChangeTool makes a change find returned: offered only with changes
// allowed, and only for changes, so a client can ask a person for each. On a
// propose-only connection it saves the change as a draft instead (D248).
func runChangeTool(acc Access) *Tool {
	if acc == Propose {
		d := "Propose a change find returned (its describe says changes: true), with its input: it is saved as a draft for a person " +
			"to review, and nothing changes until they approve it (a new version stays beside the cut). Say why in note. " +
			"Reads are run's; people-only things, spending money and final renders are refused."
		props := map[string]any{
			"name":     map[string]any{"type": "string", "description": "A name find returned."},
			"input":    map[string]any{"type": "object", "description": "Its input (no confirm token needed)."},
			"note":     map[string]any{"type": "string", "description": "Why, in a sentence or two: the person reads it beside the draft."},
			"replaces": map[string]any{"type": "string", "description": "The draft this one takes the place of, after a person said Not quite."},
		}
		return &Tool{Name: toolRunChange, special: toolRunChange, Description: d, about: d,
			InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []any{"name"}, "properties": props},
			Annotations: map[string]any{"readOnlyHint": false, "openWorldHint": false, "destructiveHint": false}}
	}
	d := "Make a change find returned (its describe says changes: true), with its input. Two calls: the first only says what " +
		"it would do and returns a confirmToken; repeat it with the token. Edits take their own check's confirmToken inside input. " +
		"Reads are run's; people-only things are refused."
	props := map[string]any{
		"name":         map[string]any{"type": "string", "description": "A name find returned."},
		"input":        map[string]any{"type": "object", "description": "Its input (omit when it takes none)."},
		"confirmToken": map[string]any{"type": "string", "description": "The token the first call returned for this name and input."},
	}
	return &Tool{Name: toolRunChange, special: toolRunChange, Description: d, about: d,
		InputSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []any{"name"}, "properties": props},
		Annotations: map[string]any{"readOnlyHint": false, "openWorldHint": false, "destructiveHint": true}}
}

// Size is what a client loads before its first call: the tool list as
// tools/list sends it, and the server's instructions.
type Size struct {
	Tools      int `json:"tools"`
	Characters int `json:"characters"`
	// Tokens is an estimate, four characters a token.
	Tokens int `json:"tokens"`
}

// ListSize measures a list (as ToolList made it for set and allowChanges).
func ListSize(list []*Tool, set string, allowChanges bool) Size {
	return ListSizeFor(list, set, AccessOf(allowChanges, false))
}

// ListSizeFor measures a list as ToolListFor made it.
func ListSizeFor(list []*Tool, set string, acc Access) Size {
	b, _ := json.Marshal(map[string]any{"tools": list})
	n := len(b) + len(InstructionsFor(set, acc))
	return Size{Tools: len(list), Characters: n, Tokens: (n + 3) / 4}
}

// OpOf says which operation a call of a tool in a toolset runs: a tool's
// own, its mode's, or the name run was given (an operation's name, or
// another catalogue's entry as it is named); "" for find and describe, or a
// tool the toolset doesn't have and no operation is named after.
func OpOf(set, tool string, args map[string]any) string {
	list, err := ToolList(set, true)
	if err != nil {
		return ""
	}
	for _, t := range list {
		if t.Name != tool {
			continue
		}
		switch {
		case t.special == toolRun || t.special == toolRunChange:
			name, _ := args["name"].(string)
			if op, ok := catalog.OpName(name); ok {
				return op.Name
			}
			return name
		case t.special != "":
			return ""
		case t.allModes != nil:
			mode, _ := args["mode"].(string)
			if op := t.allModes[mode]; op != nil {
				return op.Name
			}
			return ""
		}
		return t.op.Name
	}
	if op, ok := catalog.OpName(tool); ok {
		return op.Name
	}
	return ""
}
