// Package mcp serves MetaRush's operations to Model Context Protocol clients
// (Claude Desktop, Claude Code, other agents) over standard input and output:
// `metarush mcp serve` (D95). It is off unless a person configures a client to
// start it.
//
// A client's model chooses better from a few tools than from hundreds (D222),
// so a server offers a small list: by default the core, whose find, describe
// and run reach every operation (and any other catalogue that joins find,
// internal/catalog) beside a few everyday tools; with --toolset, one kind of
// work as direct tools, related operations merged into one tool that takes a
// mode; "all" is one tool per operation. Whatever the list, every call goes
// through the ops registry, with the same validation, flags and activity log
// as the command line and the dashboard. Operations only a person may do
// (HumanOnly) are never offered, and calling one is refused by the registry.
// The server is read-only unless it was started with --allow-changes; then an
// operation that changes MetaRush works in two steps (check, then confirm): the
// first call returns what it would do and a confirm token bound to exactly
// those arguments, the second call with the token does it. Operations that
// have their own check and token (edit.apply, the cut verbs, cut.apply,
// project.push) keep their own flow.
//
// Protocol: JSON-RPC 2.0, one message per line (the MCP stdio transport);
// initialize, ping, tools/list, tools/call, progress and cancellation
// notifications. The channel recorded in the activity log is "mcp".
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/invopop/jsonschema"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/catalog"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/version"
)

// ProtocolVersions are the MCP revisions this server speaks, newest first.
var ProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// JSON-RPC error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Options configure a server.
type Options struct {
	// AllowChanges offers the operations that change MetaRush, each confirmed
	// with a second call. Without it the server is read-only.
	AllowChanges bool
	// TokenTTL is how long a confirm token stays valid (default 10 minutes).
	TokenTTL time.Duration
	// Toolset is the list of tools offered: core (the default), review,
	// edit, library, admin, or all (one tool per operation).
	Toolset string
	// Propose: the connection may only propose (D248). Every change is
	// saved as a draft for a person to review (a new version beside the cut,
	// another change kept until approved); nothing people-only runs. It
	// can't be combined with AllowChanges.
	Propose bool
	// Agent names the AI app on the other end (codex, cursor…); without it
	// the client's own name (initialize's clientInfo) is used. What it saves
	// says which app made it.
	Agent string
}

// Access is the connection's access.
func (o Options) Access() Access { return AccessOf(o.AllowChanges, o.Propose) }

// Tool is one MCP tool: an operation of the registry, several operations
// chosen by a mode, or one of the core's own tools (find, describe, run).
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`

	op      *ops.Op
	special string // find, describe or run
	// about is what the tool does: its description without what every
	// change says about confirming (what the near-duplicate guard compares).
	about string

	// A tool of several operations: the ones offered by mode, in order;
	// every mode it has (changes too, on a read-only connection); the
	// fields of each mode's input; the fields any mode takes.
	modes     map[string]*ops.Op
	modeOrder []string
	allModes  map[string]*ops.Op
	fields    map[string]map[string]bool
	anyField  map[string]bool
	// renamed: a mode's fields under another name in this tool (an
	// operation's own "mode"), by mode: the tool's name → the operation's.
	renamed map[string]map[string]string
	// changes: this half of a group makes changes (else it reads); partner
	// is the other half's name, which has the modes this one hasn't.
	changes bool
	partner string
}

// Op is the registry operation behind a tool of one operation ("" for the
// others).
func (t *Tool) Op() string {
	if t.op == nil {
		return ""
	}
	return t.op.Name
}

// Ops are the operations a tool runs: its own, its modes' (in order), or
// none for find and describe ("*" for run and run_change: any read, any
// change).
func (t *Tool) Ops() []string {
	switch {
	case t.op != nil:
		return []string{t.op.Name}
	case t.special == toolRun || t.special == toolRunChange:
		return []string{"*"}
	}
	out := []string{}
	for _, m := range t.modeOrder {
		out = append(out, t.modes[m].Name)
	}
	return out
}

// About is what the tool does: its description without what every change
// says about confirming.
func (t *Tool) About() string { return t.about }

// Modes are the modes a tool offers, in order (none for a tool of one
// operation).
func (t *Tool) Modes() []string { return append([]string(nil), t.modeOrder...) }

// ToolName is an operation's tool name: MCP clients (and the model APIs
// behind them) take letters, digits, _ and - only, so dots become _.
func ToolName(op string) string { return strings.ReplaceAll(op, ".", "_") }

// Tools lists the tools of the "all" toolset: every operation a program may
// run, without the ones that change MetaRush unless allowChanges.
func Tools(allowChanges bool) ([]*Tool, error) { return toolsAll(AccessOf(allowChanges, false)) }

// toolsAll is the "all" toolset for a connection's access: one tool per
// operation it offers.
func toolsAll(acc Access) ([]*Tool, error) {
	var out []*Tool
	seen := map[string]string{}
	for _, op := range ops.All() {
		if !acc.offers(op) {
			continue
		}
		t := toolForAccess(op, acc)
		if prev, ok := seen[t.Name]; ok {
			return nil, fmt.Errorf("mcp: operations %s and %s would both be the tool %s", prev, op.Name, t.Name)
		}
		seen[t.Name] = op.Name
		out = append(out, t)
	}
	return out, nil
}

func toolFor(op *ops.Op) *Tool { return toolForAccess(op, Changes) }

// toolForAccess is an operation's tool on a connection of the given access:
// on a propose-only one a change saves a draft (D248).
func toolForAccess(op *ops.Op, acc Access) *Tool {
	t := &Tool{Name: ToolName(op.Name), Description: strings.TrimSuffix(op.Summary, ".") + ".", InputSchema: inputSchema(op.InType), op: op,
		Annotations: map[string]any{"readOnlyHint": !op.Mutating, "openWorldHint": false}}
	t.about = t.Description
	if op.Mutating && acc == Propose {
		t.Annotations["destructiveHint"] = false
		t.Description += draftWords
		props, _ := t.InputSchema["properties"].(map[string]any)
		delete(props, "confirmToken")
		props[draftNoteField] = map[string]any{"type": "string", "description": "Why, in a sentence or two: the person reads it beside the draft."}
		return t
	}
	if op.Mutating {
		t.Annotations["destructiveHint"] = destructive(op.Name)
		if ownCheck(op) {
			t.Description += " It changes MetaRush and checks first in its own way: a dry run (or its check operation) returns the confirmToken this call needs."
		} else {
			key := confirmKey(op)
			t.Description += fmt.Sprintf(" It changes MetaRush, in two steps: the first call only says what it would do and returns a token; call again with the same arguments plus %s to do it.", key)
			props, _ := t.InputSchema["properties"].(map[string]any)
			props[key] = map[string]any{"type": "string", "description": "The token the first call returned for exactly these arguments."}
		}
	}
	return t
}

// ownCheck: a change the operation checks itself, with a token its dry run or
// check operation gives (an explicit list: catalog.ChecksFirst); the others
// are confirmed in two steps.
func ownCheck(op *ops.Op) bool { return catalog.ChecksFirst(op) }

// confirmKey is the argument a change confirmed in two steps takes this
// server's token in: confirmToken, or mcpConfirmToken when the operation has
// a confirmToken of its own (brand.set's, for the cut it may change).
func confirmKey(op *ops.Op) string {
	if hasJSONField(op.InType, "confirmToken") {
		return "mcpConfirmToken"
	}
	return "confirmToken"
}

// destructive marks operations that remove or replace something.
func destructive(name string) bool {
	for _, w := range []string{"delete", "remove", "prune", "purge", "revoke", "unlink", "cancel"} {
		if strings.Contains(strings.ToLower(name), w) {
			return true
		}
	}
	return false
}

func hasJSONField(t reflect.Type, name string) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return false
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Anonymous && hasJSONField(f.Type, name) {
			return true
		}
		if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag == name {
			return true
		}
	}
	return false
}

// inputSchema is the JSON Schema of an operation's input, as the generated
// schemas describe it (nested types under $defs).
func inputSchema(t reflect.Type) map[string]any {
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

// Server is one client's connection.
type Server struct {
	a     *app.App
	o     Options
	list  []*Tool
	tools map[string]*Tool
	byOp  map[string]*ops.Op // every operation, by tool name and by name
	key   []byte             // confirm tokens: this process only

	wmu sync.Mutex
	w   io.Writer

	mu       sync.Mutex
	inflight map[string]*call
	wg       sync.WaitGroup
	proto    string
}

type call struct {
	cancel   context.CancelFunc
	canceled atomic.Bool
}

// New prepares a server for a. The app's actor should say via "mcp".
func New(a *app.App, o Options) (*Server, error) {
	if o.TokenTTL <= 0 {
		o.TokenTTL = 10 * time.Minute
	}
	if o.Propose && o.AllowChanges {
		return nil, apperr.New(apperr.InvalidInput, "Choose one: a connection either proposes changes for a person to review or makes them.")
	}
	list, err := ToolListFor(o.Toolset, o.Access())
	if err != nil {
		return nil, err
	}
	if o.Propose {
		a.ProposeOnly = true
	}
	if o.Agent != "" {
		a.Actor.Agent = models.AgentSlug(o.Agent)
	}
	s := &Server{a: a, o: o, list: list, tools: map[string]*Tool{}, byOp: map[string]*ops.Op{}, key: make([]byte, 32), inflight: map[string]*call{}, proto: ProtocolVersions[0]}
	if _, err := rand.Read(s.key); err != nil {
		return nil, err
	}
	for _, t := range list {
		s.tools[t.Name] = t
	}
	for _, op := range ops.All() {
		s.byOp[ToolName(op.Name)], s.byOp[op.Name] = op, op
	}
	return s, nil
}

// List is what tools/list returns.
func (s *Server) List() []*Tool { return s.list }

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func hasID(id json.RawMessage) bool { return len(id) > 0 && string(id) != "null" }

// Serve reads requests until the input ends (the client closed it) or ctx
// ends, answers each, and waits for calls still running.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.w = out
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	lines := make(chan []byte)
	done := make(chan error, 1)
	go func() {
		for sc.Scan() {
			lines <- append([]byte(nil), sc.Bytes()...)
		}
		done <- sc.Err()
		close(lines)
	}()
	var err error
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case line, ok := <-lines:
			if !ok {
				err = <-done
				break loop
			}
			if line = bytes.TrimSpace(line); len(line) > 0 {
				s.handle(ctx, line)
			}
		}
	}
	s.wg.Wait()
	return err
}

func (s *Server) handle(ctx context.Context, line []byte) {
	if line[0] == '[' {
		s.reply(json.RawMessage("null"), nil, &rpcError{Code: codeInvalidRequest, Message: "batches are not supported"})
		return
	}
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		s.reply(json.RawMessage("null"), nil, &rpcError{Code: codeParse, Message: "not JSON: " + err.Error()})
		return
	}
	if m.Method == "" {
		// A response to a request this server never sends: nothing to do.
		if hasID(m.ID) && len(m.Result) == 0 && len(m.Error) == 0 {
			s.reply(m.ID, nil, &rpcError{Code: codeInvalidRequest, Message: "a request needs a method"})
		}
		return
	}
	note := !hasID(m.ID)
	switch m.Method {
	case "initialize":
		s.reply(m.ID, s.initialize(m.Params), nil)
	case "ping":
		s.reply(m.ID, map[string]any{}, nil)
	case "tools/list":
		s.reply(m.ID, map[string]any{"tools": s.list}, nil)
	case "tools/call":
		if note {
			return
		}
		cctx, cancel := context.WithCancel(ctx)
		c := &call{cancel: cancel}
		key := string(m.ID)
		s.mu.Lock()
		s.inflight[key] = c
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				s.mu.Lock()
				delete(s.inflight, key)
				s.mu.Unlock()
				cancel()
			}()
			res, rerr := s.callTool(cctx, m.Params)
			if c.canceled.Load() {
				return // the client gave up on it: no answer (MCP cancellation)
			}
			s.reply(m.ID, res, rerr)
		}()
	case "notifications/cancelled":
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if json.Unmarshal(m.Params, &p) == nil {
			s.mu.Lock()
			if c := s.inflight[string(p.RequestID)]; c != nil {
				c.canceled.Store(true)
				c.cancel()
			}
			s.mu.Unlock()
		}
	default:
		if strings.HasPrefix(m.Method, "notifications/") || note {
			return
		}
		s.reply(m.ID, nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + m.Method})
	}
}

func (s *Server) initialize(params json.RawMessage) any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
		ClientInfo      struct {
			Name string `json:"name"`
		} `json:"clientInfo"`
	}
	_ = json.Unmarshal(params, &p)
	if slices.Contains(ProtocolVersions, p.ProtocolVersion) {
		s.proto = p.ProtocolVersion
	}
	if s.a.Actor.Agent == "" {
		// The app names itself (codex-mcp-client is Codex): what it saves says so.
		s.a.Actor.Agent = models.AgentSlug(p.ClientInfo.Name)
	}
	return map[string]any{
		"protocolVersion": s.proto,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{"name": "metarush", "title": "MetaRush", "version": version.Version},
		"instructions":    InstructionsFor(s.o.Toolset, s.o.Access()),
	}
}

// MaxInstructionWords is the most words a server's instructions take: every
// conversation with the server carries them.
const MaxInstructionWords = 150

// Instructions tell the client's model how to use the tools (at most
// MaxInstructionWords words).
func Instructions(toolset string, allowChanges bool) string {
	return InstructionsFor(toolset, AccessOf(allowChanges, false))
}

// InstructionsFor are the instructions of a connection of the given access.
func InstructionsFor(toolset string, acc Access) string {
	s := "MetaRush is a video editor: projects, footage, cuts (as editable cut sheets), versions, renders and their quality checks. Answers are JSON. "
	switch toolset {
	case "", ToolsetCore:
		s += "find names the operation for any task described in plain words, describe shows its input, and run does it; the other tools are everyday shortcuts. "
	case ToolsetAll:
		s += "Each tool is one operation, the same ones its command line (metarush) and dashboard use. "
	default:
		s += "These tools cover one kind of work; a tool with a mode holds several operations. find, describe and run reach everything else. "
	}
	switch acc {
	case Propose:
		s += "This connection only proposes: run_change and the tools named …_change save a draft for a person to review (a new version stays beside the cut), " +
			"and nothing changes until they approve it. Say why (note, or draftNote on the other tools). draft_list shows your drafts and what the person decided; after Not quite, make a new draft with replaces. "
	case Changes:
		s += "Reading and changing are separate tools: changes go through run_change, the tools named …_change and others marked as not read-only, " +
			"which take two calls (the first returns a confirmToken; repeat it with the token). Edits check first: a check or dryRun returns the token they need. "
	default:
		s += "This connection is read-only: nothing that changes MetaRush runs (the person can start the server with --allow-changes). "
	}
	return s + "Only a person may approve versions, delete footage or move the media: ask them to, in the dashboard or a terminal."
}

func (s *Server) send(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcError{Code: -32603, Message: "could not write the answer: " + err.Error()}})
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, _ = s.w.Write(append(b, '\n'))
}

func (s *Server) reply(id json.RawMessage, result any, e *rpcError) {
	m := map[string]any{"jsonrpc": "2.0", "id": id}
	if e != nil {
		m["error"] = e
	} else {
		m["result"] = result
	}
	s.send(m)
}

func (s *Server) notify(method string, params any) {
	s.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// callTool runs one tools/call. Problems with the call itself (an unknown
// tool, arguments that are not an object) are JSON-RPC errors; everything the
// operation says, refusals included, is a tool result.
func (s *Server) callTool(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call needs the name of a tool"}
	}
	args := map[string]any{}
	if len(p.Arguments) > 0 && string(p.Arguments) != "null" {
		if err := json.Unmarshal(p.Arguments, &args); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "arguments must be a JSON object"}
		}
	}
	if len(p.Meta.ProgressToken) > 0 && string(p.Meta.ProgressToken) != "null" {
		ctx = ops.WithProgress(ctx, s.progress(p.Meta.ProgressToken))
	}
	res, rerr := s.callTool1(ctx, p.Name, args)
	if rerr == nil {
		res = withNews(res, s.news(ctx))
	}
	return res, rerr
}

func (s *Server) callTool1(ctx context.Context, name string, args map[string]any) (map[string]any, *rpcError) {
	t := s.tools[name]
	switch {
	case t == nil:
		// A read by its tool name, under the same rules as run (a client may
		// call what an earlier list or another toolset named). A change is
		// made only through a tool this list offers for changes, which the
		// client asks a person about.
		op := s.byOp[name]
		if op == nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool: " + name}
		}
		if op.Mutating && !op.HumanOnly && s.o.Propose {
			err := proposeOnlyRefusal(name, op)
			s.record(ctx, op, args, err)
			return s.failed(err), nil
		}
		if op.Mutating && !op.HumanOnly && s.o.AllowChanges {
			err := apperr.New(apperr.Unauthorized, "This change isn't one of this connection's tools: changes have a tool of their own, which asks before each one.").
				WithDetails(map[string]any{"reason": "mcp_not_offered", "tool": name, "op": op.Name}).WithCommand(toolRunChange)
			s.record(ctx, op, args, err)
			return s.failed(err), nil
		}
		return s.runOp(ctx, ToolName(op.Name), op, args, "the same arguments"), nil
	case t.special == toolFind:
		return s.find(ctx, args), nil
	case t.special == toolDescribe:
		return s.describe(ctx, args), nil
	case t.special == toolRun:
		return s.run(ctx, args, false), nil
	case t.special == toolRunChange:
		return s.run(ctx, args, true), nil
	case t.allModes != nil:
		return s.runMode(ctx, t, args), nil
	}
	return s.runOp(ctx, t.Name, t.op, args, "the same arguments"), nil
}

// draftArgs takes a direct tool's reason for a draft out of its arguments.
func draftArgs(args map[string]any) (note string) {
	note, _ = args[draftNoteField].(string)
	delete(args, draftNoteField)
	return note
}

// runOp runs one operation for a tool: people-only operations and changes on
// a read-only connection are refused (and recorded), a change without a check
// of its own is confirmed in two steps (a token in args[confirmKey(op)],
// bound to the operation and the rest of args), and the rest runs. again
// says what the second call repeats ("the same arguments").
func (s *Server) runOp(ctx context.Context, tool string, op *ops.Op, args map[string]any, again string) map[string]any {
	switch {
	case op.HumanOnly:
		// The registry refuses it, and records the refusal.
		raw, _ := json.Marshal(args)
		_, err := ops.Call(ctx, s.a, op.Name, raw, ops.Caller{})
		if err == nil {
			err = apperr.New(apperr.HumanOnly, "Only a person can do this: ask them to do it in the dashboard or a terminal.").
				WithDetails(map[string]any{"tool": tool})
		}
		return s.failed(err)
	case op.Mutating && s.o.Propose:
		// Saved as a draft for a person to review (D248): never made here.
		note := draftArgs(args)
		return s.proposeDraft(ctx, tool, op, args, note, "")
	case op.Mutating && !s.o.AllowChanges:
		err := apperr.New(apperr.Unauthorized, "This changes MetaRush, and this connection is read-only.").
			WithHint("The person can allow changes by starting MetaRush's MCP server with changes allowed; each change is then confirmed with a second call.").
			WithDetails(map[string]any{"reason": "mcp_read_only", "tool": tool, "op": op.Name}).WithCommand("metarush mcp serve --allow-changes")
		s.record(ctx, op, args, err)
		return s.failed(err)
	case op.Mutating && !ownCheck(op):
		key := confirmKey(op)
		token, _ := args[key].(string)
		delete(args, key)
		// The token is bound to the operation and its arguments, its own
		// token aside (brand.set's comes from a later check of the cut).
		bound := args
		if key != "confirmToken" {
			bound = map[string]any{}
			for k, v := range args {
				if k != "confirmToken" {
					bound[k] = v
				}
			}
		}
		raw := canonical(bound)
		if token == "" {
			tok := s.token(op.Name, raw, time.Now().Add(s.o.TokenTTL))
			err := apperr.New(apperr.ConfirmRequired, "Not done yet: %s would change MetaRush (%s).", tool, strings.TrimSuffix(op.Summary, ".")).
				WithHint(fmt.Sprintf("to do it, call %s again with %s and %q: %q", tool, again, key, tok))
			s.record(ctx, op, args, err)
			res := s.failed(err)
			res["structuredContent"] = map[string]any{"error": errBody(err), key: tok, "expiresAt": time.Now().Add(s.o.TokenTTL).UTC().Format(time.RFC3339)}
			if !s.structured() {
				delete(res, "structuredContent")
			}
			return res
		}
		if !s.verify(op.Name, raw, token) {
			err := apperr.New(apperr.ConfirmRequired, "That token is not for these arguments, or it expired.").WithHint(fmt.Sprintf("call again without %s to get a new one", key))
			s.record(ctx, op, args, err)
			return s.failed(err)
		}
	}
	raw, _ := json.Marshal(args)
	out, err := ops.Call(ctx, s.a, op.Name, raw, ops.Caller{})
	if err != nil {
		return s.failed(err)
	}
	return s.answer(out, "")
}

// answer is a tool result for an operation's answer: its JSON (or text, when
// given) as content, and as structured content for clients that read it.
func (s *Server) answer(out any, text string) map[string]any {
	b, err := json.Marshal(out)
	if err != nil {
		return s.failed(err)
	}
	if text == "" {
		text = string(b)
	}
	res := map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": false}
	if s.structured() && len(b) > 0 && b[0] == '{' {
		res["structuredContent"] = json.RawMessage(b)
	}
	return res
}

// structured: revisions from 2025-06-18 carry structuredContent.
func (s *Server) structured() bool { return s.proto != "2024-11-05" && s.proto != "2025-03-26" }

func errBody(err error) map[string]any {
	ae := apperr.From(err)
	msg := ae.Message
	if ae.Code == apperr.Internal {
		msg = err.Error()
	}
	b := map[string]any{"code": ae.Code, "message": msg}
	if ae.Hint != "" {
		b["hint"] = ae.Hint
	}
	if ae.Details != nil {
		b["details"] = ae.Details
	}
	return b
}

// failed is a tool result for an error: the message, its stable code and
// what to do, as text (and as data for clients that read structured content).
// The exact command that fixes it (kept out of the message and the hint for
// people) is part of what to do, so the text names it too.
func (s *Server) failed(err error) map[string]any {
	b := errBody(err)
	text := fmt.Sprintf("%s [%s]", b["message"], b["code"])
	if h, _ := b["hint"].(string); h != "" {
		text += "\nHint: " + h
	}
	if cmd := apperr.From(err).Command(); cmd != "" {
		text += "\nCommand: " + cmd
	}
	res := map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": true}
	if s.structured() {
		res["structuredContent"] = map[string]any{"error": b}
	}
	return res
}

// record writes a call the server answered itself (a refusal, a check) to the
// activity log, like the registry records the calls it runs.
func (s *Server) record(ctx context.Context, op *ops.Op, args map[string]any, err error) {
	raw, _ := json.Marshal(args)
	rec := s.a.Log.StartOp(ctx, op.Name, raw, s.a.Actor, false, false)
	rec.End(op.Mutating, nil, err)
}

// canonical is the arguments as JSON with sorted keys.
func canonical(args map[string]any) []byte {
	b, _ := json.Marshal(args) // maps marshal with sorted keys
	return b
}

func (s *Server) sign(op string, raw []byte, exp int64) string {
	h := hmac.New(sha256.New, s.key)
	h.Write([]byte(op))
	h.Write([]byte{0})
	h.Write(raw)
	h.Write([]byte{0})
	h.Write([]byte(strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(h.Sum(nil))
}

// token is a confirm token for exactly this operation and these arguments.
func (s *Server) token(op string, raw []byte, exp time.Time) string {
	e := exp.Unix()
	return "mcp1." + strconv.FormatInt(e, 10) + "." + s.sign(op, raw, e)
}

func (s *Server) verify(op string, raw []byte, token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "mcp1" {
		return false
	}
	e, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > e {
		return false
	}
	return hmac.Equal([]byte(parts[2]), []byte(s.sign(op, raw, e)))
}

// progress turns an operation's progress into MCP progress notifications
// (increasing, at most a few a second).
func (s *Server) progress(token json.RawMessage) ops.ProgressFunc {
	var mu sync.Mutex
	var n int
	var last time.Time
	var lastLabel string
	return func(label string, done, total int64) {
		mu.Lock()
		if label == lastLabel && time.Since(last) < 300*time.Millisecond && (total <= 0 || done < total) {
			mu.Unlock()
			return
		}
		n++
		last, lastLabel = time.Now(), label
		seq := n
		mu.Unlock()
		msg := label
		if total > 0 {
			msg = fmt.Sprintf("%s: %d%%", label, done*100/total)
		}
		s.notify("notifications/progress", map[string]any{"progressToken": token, "progress": seq, "message": msg})
	}
}
