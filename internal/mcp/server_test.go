package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// client is a minimal MCP client over pipes: it sends JSON-RPC lines and
// matches answers to requests by id (answers to calls may come in any order).
type client struct {
	t    *testing.T
	in   *io.PipeWriter
	mu   sync.Mutex
	got  map[string]json.RawMessage
	cond *sync.Cond
	all  []map[string]any
	id   int
	done chan error
}

func start(t *testing.T, a *app.App, o Options) *client {
	t.Helper()
	s, err := New(a, o)
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &client{t: t, in: inW, got: map[string]json.RawMessage{}, done: make(chan error, 1)}
	c.cond = sync.NewCond(&c.mu)
	go func() {
		c.done <- s.Serve(context.Background(), inR, outW)
		_ = outW.Close()
	}()
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 64<<10), 64<<20)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Errorf("the server wrote something that is not JSON: %q", sc.Text())
				continue
			}
			c.mu.Lock()
			c.all = append(c.all, m)
			if id, ok := m["id"]; ok && m["method"] == nil {
				c.got[fmt.Sprint(id)] = append(json.RawMessage(nil), sc.Bytes()...)
			}
			c.cond.Broadcast()
			c.mu.Unlock()
		}
	}()
	t.Cleanup(func() { _ = inW.Close() })
	return c
}

func (c *client) send(v any) {
	b, _ := json.Marshal(v)
	if _, err := c.in.Write(append(b, '\n')); err != nil {
		c.t.Fatal(err)
	}
}

// request sends a request and waits for its answer.
func (c *client) request(method string, params any) map[string]any {
	c.t.Helper()
	c.mu.Lock()
	c.id++
	id := c.id
	c.mu.Unlock()
	m := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		m["params"] = params
	}
	c.send(m)
	return c.wait(fmt.Sprint(id))
}

func (c *client) wait(id string) map[string]any {
	c.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.got[id] == nil {
		if time.Now().After(deadline) {
			c.t.Fatalf("no answer to request %s", id)
		}
		go func() { time.Sleep(50 * time.Millisecond); c.mu.Lock(); c.cond.Broadcast(); c.mu.Unlock() }()
		c.cond.Wait()
	}
	var m map[string]any
	_ = json.Unmarshal(c.got[id], &m)
	return m
}

func (c *client) call(name string, args map[string]any) map[string]any {
	c.t.Helper()
	m := c.request("tools/call", map[string]any{"name": name, "arguments": args})
	r, ok := m["result"].(map[string]any)
	if !ok {
		c.t.Fatalf("%s: %v", name, m)
	}
	return r
}

func errCode(r map[string]any) string {
	sc, _ := r["structuredContent"].(map[string]any)
	e, _ := sc["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func viaMCP(a *app.App) *app.App {
	m := *a
	m.Actor.Via = "mcp"
	return &m
}

var toolNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// A client lists the tools, calls a read operation, is refused one only a
// person may do and one that changes MetaRush; everything is in the log.
func TestReadOnlyServer(t *testing.T) {
	a := viaMCP(testapp.New(t))
	ctx := context.Background()
	if _, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Garage film"}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	c := start(t, a, Options{Toolset: ToolsetAll})
	init := c.request("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	res, _ := init["result"].(map[string]any)
	if res["protocolVersion"] != "2025-06-18" || res["capabilities"].(map[string]any)["tools"] == nil || !strings.Contains(res["instructions"].(string), "read-only") {
		t.Fatalf("initialize: %v", init)
	}
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	if p := c.request("ping", nil); p["result"] == nil {
		t.Fatalf("ping: %v", p)
	}

	list := c.request("tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	names := map[string]map[string]any{}
	for _, x := range list {
		tl := x.(map[string]any)
		name := tl["name"].(string)
		if !toolNameRE.MatchString(name) {
			t.Errorf("tool name %q is not one clients accept", name)
		}
		if s, ok := tl["inputSchema"].(map[string]any); !ok || s["type"] != "object" {
			t.Errorf("%s: input schema %v", name, tl["inputSchema"])
		}
		if ro := tl["annotations"].(map[string]any)["readOnlyHint"]; ro != true {
			t.Errorf("%s is offered on a read-only connection", name)
		}
		names[name] = tl
	}
	for _, want := range []string{"project_list", "project_show", "revision_list", "search"} {
		if names[want] == nil {
			t.Errorf("%s is missing", want)
		}
	}
	for _, not := range []string{"project_create", "edit_apply", "revision_approve", "storage_migrate"} {
		if names[not] != nil {
			t.Errorf("%s is offered on a read-only connection", not)
		}
	}

	// A read.
	r := c.call("project_list", map[string]any{})
	sc, _ := r["structuredContent"].(map[string]any)
	if r["isError"] != false || !strings.Contains(r["content"].([]any)[0].(map[string]any)["text"].(string), "Garage film") || sc["projects"] == nil {
		t.Fatalf("project_list: %v", r)
	}
	// Only a person approves a version.
	r = c.call("revision_approve", map[string]any{"id": "rev_nothing"})
	if r["isError"] != true || errCode(r) != "E_HUMAN_ONLY" {
		t.Fatalf("revision_approve: %v", r)
	}
	// Changes are not offered here, and calling one anyway is refused.
	r = c.call("project_create", map[string]any{"name": "Should not exist"})
	if r["isError"] != true || errCode(r) != "E_UNAUTHORIZED" || !strings.Contains(fmt.Sprint(r["content"]), "--allow-changes") {
		t.Fatalf("project_create: %v", r)
	}
	// Protocol errors.
	if e := c.request("tools/call", map[string]any{"name": "no_such_tool"})["error"].(map[string]any); e["code"].(float64) != codeInvalidParams {
		t.Fatalf("unknown tool: %v", e)
	}
	if e := c.request("resources/list", nil)["error"].(map[string]any); e["code"].(float64) != codeMethodNotFound {
		t.Fatalf("unknown method: %v", e)
	}
	_ = c.in.Close()
	if err := <-c.done; err != nil {
		t.Fatal(err)
	}

	// Every call is in the activity log, on the mcp channel.
	a.Log.Flush(ctx)
	var entries []logbook.Entry
	if err := a.DB.Find(ctx, store.CLogEntries, a.WorkspaceID(), bson.M{"channel": logbook.ChannelMCP, "kind": logbook.KindOp}, &entries); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		code := "ok"
		if e.Error != nil {
			code = e.Error.Code
		}
		seen[e.Op.Name+" "+code] = true
	}
	if !seen["project.list ok"] || !seen["revision.approve E_HUMAN_ONLY"] || !seen["project.create E_UNAUTHORIZED"] {
		t.Fatalf("log: %v", seen)
	}
}

// With --allow-changes a change takes two calls: the first returns a confirm
// token for exactly those arguments, the second does it.
func TestChangesAreConfirmed(t *testing.T) {
	a := viaMCP(testapp.New(t))
	ctx := context.Background()
	c := start(t, a, Options{AllowChanges: true, TokenTTL: time.Minute, Toolset: ToolsetAll})
	c.request("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	list := c.request("tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	var create, apply map[string]any
	for _, x := range list {
		tl := x.(map[string]any)
		switch tl["name"] {
		case "project_create":
			create = tl
		case "edit_apply":
			apply = tl
		case "revision_approve", "storage_migrate", "asset_delete":
			t.Errorf("%s is offered to a program", tl["name"])
		}
	}
	if create == nil || create["annotations"].(map[string]any)["readOnlyHint"] != false ||
		create["inputSchema"].(map[string]any)["properties"].(map[string]any)["confirmToken"] == nil {
		t.Fatalf("project_create: %v", create)
	}
	if apply == nil || !strings.Contains(apply["description"].(string), "its own way") {
		t.Fatalf("edit_apply keeps its own check: %v", apply)
	}

	first := c.call("project_create", map[string]any{"name": "Spring promo"})
	sc := first["structuredContent"].(map[string]any)
	token, _ := sc["confirmToken"].(string)
	if first["isError"] != true || errCode(first) != "E_CONFIRM_REQUIRED" || token == "" {
		t.Fatalf("first call: %v", first)
	}
	if n, _ := a.DB.C(store.CProjects).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "name": "Spring promo"}); n != 0 {
		t.Fatal("the first call created the project")
	}
	// A token is for exactly those arguments.
	if r := c.call("project_create", map[string]any{"name": "Something else", "confirmToken": token}); errCode(r) != "E_CONFIRM_REQUIRED" {
		t.Fatalf("other arguments: %v", r)
	}
	done := c.call("project_create", map[string]any{"name": "Spring promo", "confirmToken": token})
	if done["isError"] != false || !strings.Contains(fmt.Sprint(done["structuredContent"]), "Spring promo") {
		t.Fatalf("second call: %v", done)
	}
	if n, _ := a.DB.C(store.CProjects).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "name": "Spring promo"}); n != 1 {
		t.Fatalf("created %d projects", n)
	}
	// Edits keep their own flow: no token of ours, edit_apply asks for edit_check's.
	if r := c.call("edit_apply", map[string]any{"plan": map[string]any{}}); r["isError"] != true || errCode(r) == "" {
		t.Fatalf("edit_apply without its token: %v", r)
	}
	// Still only a person approves.
	if r := c.call("revision_approve", map[string]any{"id": "rev_x"}); errCode(r) != "E_HUMAN_ONLY" {
		t.Fatalf("revision_approve: %v", r)
	}
}

// An expired token is refused; a cancelled call gets no answer; the older
// protocol revision gets no structured content.
func TestTokensExpireAndCallsCancel(t *testing.T) {
	a := viaMCP(testapp.New(t))
	c := start(t, a, Options{AllowChanges: true, TokenTTL: time.Second})
	c.request("initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "old", "version": "1"}})
	first := c.call("run_change", map[string]any{"name": "project.create", "input": map[string]any{"name": "Later"}})
	if first["structuredContent"] != nil {
		t.Fatalf("2024-11-05 has no structured content: %v", first)
	}
	text := first["content"].([]any)[0].(map[string]any)["text"].(string)
	tok := regexp.MustCompile(`"(mcp1\.[0-9]+\.[0-9a-f]+)"`).FindStringSubmatch(text)
	if tok == nil {
		t.Fatalf("no token in %q", text)
	}
	time.Sleep(2100 * time.Millisecond)
	if r := c.call("run_change", map[string]any{"name": "project.create", "input": map[string]any{"name": "Later"}, "confirmToken": tok[1]}); r["isError"] != true || !strings.Contains(fmt.Sprint(r["content"]), "expired") {
		t.Fatalf("expired token: %v", r)
	}
	// Cancel a call: the server does not answer it (and keeps serving).
	c.send(map[string]any{"jsonrpc": "2.0", "id": 99, "method": "tools/call", "params": map[string]any{"name": "project_list", "arguments": map[string]any{}}})
	c.send(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": 99, "reason": "test"}})
	if p := c.request("ping", nil); p["result"] == nil {
		t.Fatalf("ping after cancel: %v", p)
	}
}
