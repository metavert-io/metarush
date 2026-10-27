package mcp

// These tests use the whole operation registry: the package's external tests
// (guards_test.go) import the command line, which registers every operation
// in this test binary, as in the real one.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/catalog"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func toolNames(c *client) []string {
	var names []string
	for _, x := range c.request("tools/list", nil)["result"].(map[string]any)["tools"].([]any) {
		names = append(names, x.(map[string]any)["name"].(string))
	}
	return names
}

func text(r map[string]any) string {
	return r["content"].([]any)[0].(map[string]any)["text"].(string)
}

// The core: a few tools, and find, describe and run reach the rest.
func TestCoreFindsDescribesAndRuns(t *testing.T) {
	a := viaMCP(testapp.New(t))
	ctx := context.Background()
	if _, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Harbor days"}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	c := start(t, a, Options{})
	init := c.request("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	instr := init["result"].(map[string]any)["instructions"].(string)
	if !strings.Contains(instr, "find") || !strings.Contains(instr, "read-only") || len(strings.Fields(instr)) > MaxInstructionWords {
		t.Fatalf("instructions: %q", instr)
	}
	names := toolNames(c)
	want := []string{"find", "describe", "run", "activity_summary", "project_list", "project_show", "search", "cut_show", "cut_check", "qa_show"}
	if !slices.Equal(names, want) {
		t.Fatalf("core tools: %v", names)
	}

	// find: one line each, best first.
	r := c.call("find", map[string]any{"task": "make me a review copy I can watch"})
	lines := strings.Split(text(r), "\n")
	if r["isError"] != false || !strings.HasPrefix(lines[0], "render.submit — ") || !strings.Contains(lines[0], "(changes something)") || len(lines) > catalog.DefaultLimit+1 {
		t.Fatalf("find: %v", text(r))
	}
	if hits := r["structuredContent"].(map[string]any)["hits"].([]any); len(hits) == 0 || hits[0].(map[string]any)["name"] != "render.submit" {
		t.Fatalf("find's structured answer: %v", r["structuredContent"])
	}
	if r := c.call("find", map[string]any{"task": " "}); errCode(r) != apperr.InvalidInput {
		t.Fatalf("find without words: %v", r)
	}
	// describe: the input, with an example.
	r = c.call("describe", map[string]any{"name": "project.show"})
	d := r["structuredContent"].(map[string]any)
	if r["isError"] != false || d["input"] == nil || d["example"] == nil || d["command"] != "metarush project show" || d["changes"] != false {
		t.Fatalf("describe: %v", r)
	}
	// run: a read runs; a change is refused on a read-only connection; only a person approves.
	r = c.call("run", map[string]any{"name": "project.list"})
	if r["isError"] != false || !strings.Contains(text(r), "Harbor days") {
		t.Fatalf("run project.list: %v", r)
	}
	r = c.call("run", map[string]any{"name": "project_create", "input": map[string]any{"name": "Nope"}})
	if errCode(r) != apperr.Unauthorized || !strings.Contains(fmt.Sprint(r["content"]), "--allow-changes") {
		t.Fatalf("run a change, read-only: %v", r)
	}
	if r := c.call("run", map[string]any{"name": "revision.approve", "input": map[string]any{"id": "rev_x"}}); errCode(r) != apperr.HumanOnly {
		t.Fatalf("run revision.approve: %v", r)
	}
	if r := c.call("run", map[string]any{"name": "no.such.thing"}); errCode(r) != apperr.NotFound {
		t.Fatalf("run an unknown name: %v", r)
	}
	if r := c.call("run", map[string]any{"name": "project.list", "input": "everything"}); errCode(r) != apperr.InvalidInput {
		t.Fatalf("run with input that is not an object: %v", r)
	}
}

// With changes allowed, run confirms a change in two steps, the token bound
// to the name and the input.
func TestRunConfirmsChanges(t *testing.T) {
	a := viaMCP(testapp.New(t))
	ctx := context.Background()
	c := start(t, a, Options{AllowChanges: true, TokenTTL: time.Minute})
	c.request("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	if names := toolNames(c); len(names) != MaxCoreTools || !slices.Contains(names, "run_change") || !slices.Contains(names, "render_submit") || slices.Contains(names, "cut_apply") {
		t.Fatalf("core with changes: %v", names)
	}
	// run only reads, even here: a person who always allows it allows no change.
	if r := c.call("run", map[string]any{"name": "project.create", "input": map[string]any{"name": "Spring promo"}}); errCode(r) != apperr.Unauthorized || !strings.Contains(text(r), "run_change") {
		t.Fatalf("run made a change: %v", r)
	}
	if r := c.call("run_change", map[string]any{"name": "project.list"}); errCode(r) != apperr.InvalidInput {
		t.Fatalf("run_change ran a read: %v", r)
	}
	// A change not on this list, called by its tool name, isn't made either.
	if r := c.call("project_create", map[string]any{"name": "Spring promo"}); errCode(r) != apperr.Unauthorized || !strings.Contains(text(r), "run_change") {
		t.Fatalf("an unlisted change by its tool name: %v", r)
	}
	first := c.call("run_change", map[string]any{"name": "project.create", "input": map[string]any{"name": "Spring promo"}})
	token, _ := first["structuredContent"].(map[string]any)["confirmToken"].(string)
	if errCode(first) != apperr.ConfirmRequired || token == "" || !strings.Contains(text(first), "same name and input") {
		t.Fatalf("first call: %v", first)
	}
	if r := c.call("run_change", map[string]any{"name": "project.create", "input": map[string]any{"name": "Other"}, "confirmToken": token}); errCode(r) != apperr.ConfirmRequired {
		t.Fatalf("another input with that token: %v", r)
	}
	if r := c.call("run_change", map[string]any{"name": "project.create", "input": map[string]any{"name": "Spring promo"}, "confirmToken": token}); r["isError"] != false {
		t.Fatalf("second call: %v", r)
	}
	if n, _ := a.DB.C(store.CProjects).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "name": "Spring promo"}); n != 1 {
		t.Fatalf("made %d projects", n)
	}
	// An edit keeps its own check: no token of ours, its own refusal.
	if r := c.call("run_change", map[string]any{"name": "edit.apply", "input": map[string]any{"plan": map[string]any{}}}); r["isError"] != true || errCode(r) == apperr.ConfirmRequired && strings.Contains(text(r), "same name and input") {
		t.Fatalf("edit.apply through run: %v", r)
	}
}

// abilityIn and abilityOut are a stand-in for another catalogue's operation:
// what runs its entries.
type abilityIn struct {
	Name  string         `json:"name"`
	Input map[string]any `json:"input,omitempty"`
}
type abilityOut struct {
	Ran   string         `json:"ran"`
	Input map[string]any `json:"input"`
}

func init() {
	ops.Def("test.ability.run", "Run a test ability (tests only)", ops.Flags{Mutating: true},
		func(_ context.Context, _ *app.App, in *abilityIn) (*abilityOut, error) {
			return &abilityOut{Ran: in.Name, Input: in.Input}, nil
		})
}

type testAbilities struct{}

func (testAbilities) Kind() string { return "test-ability" }
func (testAbilities) Entries(context.Context, *app.App) ([]catalog.Entry, error) {
	return []catalog.Entry{{Name: "stock-music-finder", Kind: "test-ability", About: "Find licensed stock music by mood and tempo", Changes: true,
		RunWith: "test.ability.run", RunInput: map[string]any{"name": "stock-music-finder"}}}, nil
}
func (testAbilities) Describe(_ context.Context, _ *app.App, name string) (*catalog.Description, error) {
	if name != "stock-music-finder" {
		return nil, apperr.New(apperr.NotFound, "no such ability")
	}
	return &catalog.Description{Name: name, Kind: "test-ability", About: "Find licensed stock music by mood and tempo", Changes: true,
		Input: map[string]any{"type": "object"}, RunWith: "test.ability.run", RunInput: map[string]any{"name": name}}, nil
}

// Another catalogue joins find, describe and run with nothing new in the
// server: its entries run through its own operation, under the same rules.
func TestAnotherCatalogueRunsThroughItsOperation(t *testing.T) {
	defer catalog.Register(testAbilities{})()
	a := viaMCP(testapp.New(t))
	c := start(t, a, Options{AllowChanges: true, TokenTTL: time.Minute})
	c.request("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	if r := c.call("find", map[string]any{"task": "licensed stock music for a calm mood"}); !strings.HasPrefix(text(r), "stock-music-finder — ") {
		t.Fatalf("find: %v", text(r))
	}
	if r := c.call("describe", map[string]any{"name": "stock-music-finder"}); r["structuredContent"].(map[string]any)["runWith"] != "test.ability.run" {
		t.Fatalf("describe: %v", r)
	}
	in := map[string]any{"mood": "calm"}
	first := c.call("run_change", map[string]any{"name": "stock-music-finder", "input": in})
	token, _ := first["structuredContent"].(map[string]any)["confirmToken"].(string)
	if errCode(first) != apperr.ConfirmRequired || token == "" {
		t.Fatalf("a change through another catalogue is confirmed too: %v", first)
	}
	r := c.call("run_change", map[string]any{"name": "stock-music-finder", "input": in, "confirmToken": token})
	out := r["structuredContent"].(map[string]any)
	if r["isError"] != false || out["ran"] != "stock-music-finder" || out["input"].(map[string]any)["mood"] != "calm" {
		t.Fatalf("run: %v", r)
	}
}

// A toolset's tool with modes: the mode chooses the operation, fields of
// other modes are left out, an operation's own "mode" field is renamed, and
// changes keep their rules.
func TestToolsWithModes(t *testing.T) {
	a := viaMCP(testapp.New(t))
	ctx := context.Background()
	p, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Garage film"}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	list, err := ToolList(ToolsetReview, false)
	if err != nil {
		t.Fatal(err)
	}
	var footage, revision *Tool
	for _, tl := range list {
		switch tl.Name {
		case "footage":
			footage = tl
		case "revision":
			revision = tl
		}
	}
	props := footage.InputSchema["properties"].(map[string]any)
	if props["mode"].(map[string]any)["enum"] == nil || props["sheetMode"] == nil || !strings.Contains(footage.Description, "sheet") {
		t.Fatalf("footage tool: %v", footage.InputSchema)
	}
	if slices.Contains(revision.Modes(), "askReview") || revision.Annotations["readOnlyHint"] != true {
		t.Fatalf("a read-only revision tool offers asking for review: %v", revision.Modes())
	}

	c := start(t, a, Options{Toolset: ToolsetReview})
	c.request("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	r := c.call("project", map[string]any{"mode": "show", "id": p.Project.ID})
	if r["isError"] != false || !strings.Contains(text(r), "Garage film") {
		t.Fatalf("project show: %v", r)
	}
	// "id" is show's field, not list's: left out of the list.
	if r := c.call("project", map[string]any{"mode": "list", "id": p.Project.ID}); r["isError"] != false {
		t.Fatalf("project list with another mode's field: %v", r)
	}
	if r := c.call("project", map[string]any{"mode": "explode"}); errCode(r) != apperr.InvalidInput || !strings.Contains(text(r), "list, show") {
		t.Fatalf("unknown mode: %v", r)
	}
	if r := c.call("revision", map[string]any{"mode": "askReview", "id": "rev_x"}); errCode(r) != apperr.Unauthorized {
		t.Fatalf("a change's mode on a read-only connection: %v", r)
	}

	// With changes: the mode's change is confirmed in two steps.
	lc := start(t, a, Options{Toolset: ToolsetLibrary, AllowChanges: true, TokenTTL: time.Minute})
	lc.request("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	// The reading half never changes anything, even with changes allowed.
	if r := lc.call("collection", map[string]any{"mode": "create", "name": "Stock shots"}); errCode(r) != apperr.Unauthorized || !strings.Contains(text(r), "collection_change") {
		t.Fatalf("collection (reads) made a change: %v", r)
	}
	if r := lc.call("collection_change", map[string]any{"mode": "list"}); errCode(r) != apperr.InvalidInput {
		t.Fatalf("collection_change ran a read: %v", r)
	}
	first := lc.call("collection_change", map[string]any{"mode": "create", "name": "Stock shots"})
	token, _ := first["structuredContent"].(map[string]any)["confirmToken"].(string)
	if errCode(first) != apperr.ConfirmRequired || token == "" {
		t.Fatalf("collection create: %v", first)
	}
	if r := lc.call("collection_change", map[string]any{"mode": "create", "name": "Stock shots", "confirmToken": token}); r["isError"] != false {
		t.Fatalf("collection create, confirmed: %v", r)
	}
	if r := lc.call("collection", map[string]any{"mode": "list", "confirmToken": "left over"}); r["isError"] != false || !strings.Contains(text(r), "Stock shots") {
		t.Fatalf("collection list: %v", r)
	}
}

// brand.set has a confirmToken of its own (for the cut it may change) yet
// saves the kit at once: it is confirmed in two steps like any change, with
// this server's token in mcpConfirmToken, and a token of its own field is no
// way around that.
func TestBrandSetIsConfirmedInTwoSteps(t *testing.T) {
	a := viaMCP(testapp.New(t))
	ctx := context.Background()
	p, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Brand film"}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	pid := p.Project.ID
	if _, err := ops.CallTyped[ops.ContentShowOut](ctx, a, "content.own", ops.ContentOwnIn{ProjectID: pid}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	primary := func() string {
		out, err := ops.CallTyped[ops.BrandShowOut](ctx, a, "brand.show", ops.BrandShowIn{ProjectID: pid}, ops.Caller{})
		if err != nil {
			t.Fatal(err)
		}
		return out.Kit.Primary
	}
	c := start(t, a, Options{AllowChanges: true, TokenTTL: time.Minute, Toolset: ToolsetAll})
	c.request("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	var tool map[string]any
	for _, x := range c.request("tools/list", nil)["result"].(map[string]any)["tools"].([]any) {
		if x.(map[string]any)["name"] == "brand_set" {
			tool = x.(map[string]any)
		}
	}
	props := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
	if props["mcpConfirmToken"] == nil || props["confirmToken"] == nil || !strings.Contains(tool["description"].(string), "two steps") {
		t.Fatalf("brand_set: %v", tool)
	}
	if r := c.call("ops_describe", map[string]any{"name": "brand.set"}); r["isError"] != false || r["structuredContent"].(map[string]any)["ownCheck"] == true {
		t.Fatalf("describe says brand.set checks itself: %v", r)
	}
	// A token in its own field is no way around the confirmation.
	if r := c.call("brand_set", map[string]any{"projectId": pid, "primary": "#123456", "confirmToken": "ct_1_x"}); errCode(r) != apperr.ConfirmRequired {
		t.Fatalf("brand_set with its own token only: %v", r)
	}
	first := c.call("brand_set", map[string]any{"projectId": pid, "primary": "#123456"})
	token, _ := first["structuredContent"].(map[string]any)["mcpConfirmToken"].(string)
	if errCode(first) != apperr.ConfirmRequired || token == "" || primary() == "#123456" {
		t.Fatalf("first call: %v (primary %s)", first, primary())
	}
	if r := c.call("brand_set", map[string]any{"projectId": pid, "primary": "#123456", "mcpConfirmToken": token}); r["isError"] != false || primary() != "#123456" {
		t.Fatalf("second call: %v (primary %s)", r, primary())
	}
	// Through run too.
	c = start(t, a, Options{AllowChanges: true, TokenTTL: time.Minute})
	c.request("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	first = c.call("run_change", map[string]any{"name": "brand.set", "input": map[string]any{"projectId": pid, "primary": "#654321"}})
	token, _ = first["structuredContent"].(map[string]any)["mcpConfirmToken"].(string)
	if errCode(first) != apperr.ConfirmRequired || token == "" {
		t.Fatalf("run brand.set: %v", first)
	}
	if r := c.call("run_change", map[string]any{"name": "brand.set", "input": map[string]any{"projectId": pid, "primary": "#654321"}, "confirmToken": token}); r["isError"] != false || primary() != "#654321" {
		t.Fatalf("run brand.set, confirmed: %v (primary %s)", r, primary())
	}
}

type lookIn struct {
	Name  string         `json:"name"`
	Input map[string]any `json:"input,omitempty"`
}

func init() {
	ops.Def("test.ability.look", "Look something up for a test ability (tests only)", ops.Flags{},
		func(_ context.Context, _ *app.App, in *lookIn) (*abilityOut, error) {
			return &abilityOut{Ran: in.Name, Input: in.Input}, nil
		})
}

// shadowAbilities has an entry named like an operation's MCP tool name.
type shadowAbilities struct{}

func (shadowAbilities) Kind() string { return "test-shadow" }
func (shadowAbilities) Entries(context.Context, *app.App) ([]catalog.Entry, error) {
	return []catalog.Entry{{Name: "footage_moments", Kind: "test-shadow", About: "Stock footage of quiet moments", RunWith: "test.ability.look",
		RunInput: map[string]any{"name": "footage_moments"}}}, nil
}
func (shadowAbilities) Describe(_ context.Context, _ *app.App, name string) (*catalog.Description, error) {
	if name != "footage_moments" {
		return nil, apperr.New(apperr.NotFound, "no such ability")
	}
	return &catalog.Description{Name: name, Kind: "test-shadow", About: "Stock footage of quiet moments", Input: map[string]any{"type": "object"},
		RunWith: "test.ability.look", RunInput: map[string]any{"name": name}}, nil
}

// An entry named like an operation's tool name runs as itself, never as the
// operation.
func TestAnEntryNamedLikeAToolRunsAsItself(t *testing.T) {
	defer catalog.Register(shadowAbilities{})()
	a := viaMCP(testapp.New(t))
	c := start(t, a, Options{})
	c.request("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	r := c.call("run", map[string]any{"name": "footage_moments", "input": map[string]any{"mood": "quiet"}})
	out, _ := r["structuredContent"].(map[string]any)
	if r["isError"] != false || out["ran"] != "footage_moments" || out["input"].(map[string]any)["mood"] != "quiet" {
		t.Fatalf("run footage_moments: %v", r)
	}
}
