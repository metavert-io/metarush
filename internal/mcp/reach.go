package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/catalog"
	"github.com/metavert-io/metarush/internal/ops"
)

// find answers the find tool: the best entries for a task, one line each
// (the structured answer has everything).
func (s *Server) find(ctx context.Context, args map[string]any) map[string]any {
	in := catalog.FindIn{}
	in.Task, _ = args["task"].(string)
	if n, ok := args["limit"].(float64); ok {
		in.Limit = int(n)
	}
	raw, _ := json.Marshal(in)
	out, err := ops.Call(ctx, s.a, "ops.find", raw, ops.Caller{})
	if err != nil {
		return s.failed(err)
	}
	fo := out.(*catalog.FindOut)
	lines := make([]string, 0, len(fo.Hits)+1)
	for _, h := range fo.Hits {
		lines = append(lines, h.Line)
	}
	if len(lines) == 0 {
		lines = append(lines, "Nothing matches those words; try others for the same task.")
	} else if s.o.Propose {
		lines = append(lines, "(This connection saves changes as drafts for a person to review.)")
	} else if !s.o.AllowChanges {
		lines = append(lines, "(This connection runs only what changes nothing.)")
	}
	return s.answer(fo, strings.Join(lines, "\n"))
}

// describe answers the describe tool.
func (s *Server) describe(ctx context.Context, args map[string]any) map[string]any {
	name, _ := args["name"].(string)
	raw, _ := json.Marshal(catalog.DescribeEntryIn{Name: name})
	out, err := ops.Call(ctx, s.a, "ops.describe", raw, ops.Caller{})
	if err != nil {
		return s.failed(err)
	}
	return s.answer(out, "")
}

// run answers run (change false: reads only) and run_change (change: changes
// only): any entry find returns, by name. An operation runs itself; another
// catalogue's entry runs through its own operation, with the caller's input
// under "input". The rest of the rules are runOp's.
func (s *Server) run(ctx context.Context, args map[string]any, change bool) map[string]any {
	tool := toolRun
	if change {
		tool = toolRunChange
	}
	name, _ := args["name"].(string)
	input := map[string]any{}
	switch v := args["input"].(type) {
	case map[string]any:
		input = v
	case nil:
	default:
		return s.failed(apperr.New(apperr.InvalidInput, "Give run its input as an object with the fields its describe lists."))
	}
	// The entry by its name (another catalogue's own names before an
	// operation's tool name), and what runs it.
	d, err := catalog.Describe(ctx, s.a, name)
	if err != nil {
		return s.failed(err)
	}
	var op *ops.Op
	if d.Kind == catalog.KindOperation {
		op, _ = ops.Get(d.Name)
	} else if op, input, err = catalog.RunTarget(name, d, input); err != nil {
		return s.failed(err)
	}
	if op == nil {
		return s.failed(apperr.New(apperr.NotFound, "MetaRush has nothing by that name.").WithDetails(map[string]any{"name": name}))
	}
	switch {
	case op.HumanOnly:
		// runOp refuses it, as the registry does.
	case op.Mutating && !change && s.o.Propose:
		err := apperr.New(apperr.Unauthorized, "This tool only reads: changes have a tool of their own, which saves them as drafts for a person to review.").
			WithDetails(map[string]any{"reason": "mcp_run_reads_only", "name": name, "op": op.Name}).WithCommand(toolRunChange)
		s.record(ctx, op, input, err)
		return s.failed(err)
	case op.Mutating && !change && s.o.AllowChanges:
		err := apperr.New(apperr.Unauthorized, "This tool only reads: changes have a tool of their own, which asks before each one.").
			WithDetails(map[string]any{"reason": "mcp_run_reads_only", "name": name, "op": op.Name}).WithCommand(toolRunChange)
		s.record(ctx, op, input, err)
		return s.failed(err)
	case !op.Mutating && change:
		return s.failed(apperr.New(apperr.InvalidInput, "That only reads: reading has a tool of its own.").
			WithDetails(map[string]any{"name": name, "op": op.Name}).WithCommand(toolRun))
	}
	if change && s.o.Propose && op.Mutating && !op.HumanOnly {
		// Saved as a draft for a person to review (D248).
		note, _ := args["note"].(string)
		replaces, _ := args["replaces"].(string)
		return s.proposeDraft(ctx, tool, op, input, note, replaces)
	}
	if tok, _ := args["confirmToken"].(string); tok != "" && op.Mutating {
		// This server's two-step token goes where the operation's tool takes
		// it; for an edit that checks itself, it is its own check's, given
		// beside the input instead of inside it.
		key := confirmKey(op)
		if ownCheck(op) {
			key = "confirmToken"
		}
		if _, has := input[key]; !has {
			input[key] = tok
		}
	}
	return s.runOp(ctx, tool, op, input, "the same name and input")
}

// runMode answers a tool of several operations: its mode chooses one, and
// the fields only other modes take are left out.
func (s *Server) runMode(ctx context.Context, t *Tool, args map[string]any) map[string]any {
	mode, _ := args["mode"].(string)
	delete(args, "mode")
	op := t.modes[mode]
	if op == nil {
		other := t.allModes[mode]
		switch {
		case other == nil:
			return s.failed(apperr.New(apperr.InvalidInput, "Say which mode of %s to run: %s.", t.Name, strings.Join(t.modeOrder, ", ")).
				WithDetails(map[string]any{"tool": t.Name, "mode": mode}))
		case other.Mutating && s.o.Propose && !t.changes:
			// A reading tool never saves a draft either: a person may have allowed it.
			err := apperr.New(apperr.Unauthorized, "This tool only reads: its changes have a tool of their own, which saves them as drafts.").
				WithDetails(map[string]any{"reason": "mcp_tool_reads_only", "tool": t.Name, "mode": mode, "op": other.Name}).WithCommand(t.partner)
			s.record(ctx, other, args, err)
			return s.failed(err)
		case other.Mutating && s.o.Propose:
			return s.runOp(ctx, t.Name, other, args, "the same arguments") // a change no draft may hold: refused
		case other.Mutating && !s.o.AllowChanges:
			return s.runOp(ctx, t.Name, other, args, "the same arguments") // refused: a change on a read-only connection
		case other.Mutating:
			// Never here: this tool only reads, so a person may have allowed it.
			err := apperr.New(apperr.Unauthorized, "This tool only reads: its changes have a tool of their own, which asks before each one.").
				WithDetails(map[string]any{"reason": "mcp_tool_reads_only", "tool": t.Name, "mode": mode, "op": other.Name}).WithCommand(t.partner)
			s.record(ctx, other, args, err)
			return s.failed(err)
		}
		return s.failed(apperr.New(apperr.InvalidInput, "This tool only makes changes: reading has a tool of its own.").
			WithDetails(map[string]any{"tool": t.Name, "mode": mode, "op": other.Name}).WithCommand(t.partner))
	}
	for k := range args {
		switch {
		case t.fields[mode][k]:
		case (k == "confirmToken" || k == "mcpConfirmToken") && op.Mutating:
			// This tool's two-step token, or a change's own.
		case t.anyField[k] || k == "confirmToken" || k == "mcpConfirmToken":
			delete(args, k) // a field of another mode
		}
	}
	for alias, field := range t.renamed[mode] {
		if v, ok := args[alias]; ok {
			delete(args, alias)
			args[field] = v
		}
	}
	return s.runOp(ctx, fmt.Sprintf("%s (mode %s)", t.Name, mode), op, args, "the same arguments")
}
