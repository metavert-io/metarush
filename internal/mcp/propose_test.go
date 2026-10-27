package mcp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// A cut to propose changes to: a project with one clip and its first version.
func cutToPropose(t *testing.T, a *app.App) (projectID, version string) {
	t.Helper()
	ctx := context.Background()
	p, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Garden story"}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 2048)
	_, _ = rand.Read(b)
	f := filepath.Join(t.TempDir(), "f.mov")
	if err := os.WriteFile(f, b, 0o644); err != nil {
		t.Fatal(err)
	}
	sha, _ := media.HashFile(f)
	key := media.ObjectKey(a.WorkspaceID(), sha)
	if _, err := a.Media.PutFile(ctx, key, f, media.PutOptions{SHA256: sha}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{p.Project.ID}, Kind: "video", Name: "garage.mov", Title: "Garage interview",
		SHA256: sha, Size: int64(len(b)), StorageKey: key, State: models.AssetAvailable, Media: models.MediaInfo{HasVideo: true, HasAudio: true, Duration: 10, Width: 1920, Height: 1080, FPS: 30},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	spec := editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: "vertical-1080p", Width: 1080, Height: 1920, FPS: 30, Platform: "shorts"},
		Clips: []editspec.Clip{{ID: "ca", AssetID: as.ID, In: 0, Out: 3}}}
	plan, _ := json.Marshal(editspec.Plan{ProjectID: p.Project.ID, Instruction: "first cut", Spec: &spec})
	chk, err := ops.CallTyped[ops.EditCheckOut](ctx, a, "edit.check", ops.EditCheckIn{Plan: plan}, ops.Caller{})
	if err != nil || !chk.Valid {
		t.Fatalf("first cut: %v %+v", err, chk)
	}
	out, err := ops.CallTyped[ops.EditApplyOut](ctx, a, "edit.apply", ops.EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	return p.Project.ID, out.Revision.ID
}

func trimPlan(base string, out float64) map[string]any {
	return map[string]any{"plan": map[string]any{"baseRevisionId": base, "instruction": "A shorter intro",
		"ops": []any{map[string]any{"op": "updateClip", "id": "ca", "set": map[string]any{"out": out}}}}}
}

// An app connected to propose only (D248): it reads and checks, its changes
// are saved as drafts beside the cut (run_change and the …_change tools), it
// is told the person's decisions on its next call, and it can't approve,
// render, spend or reach a change through a reading tool. The client's own
// name says which app it is.
func TestProposeOnlyConnection(t *testing.T) {
	base := testapp.New(t)
	ctx := context.Background()
	pid, v1 := cutToPropose(t, base)
	a := viaMCP(base)
	c := start(t, a, Options{Propose: true})
	init := c.request("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "codex-mcp-client", "version": "1"}})
	instr, _ := init["result"].(map[string]any)["instructions"].(string)
	if !strings.Contains(instr, "only proposes") {
		t.Fatalf("instructions: %s", instr)
	}
	names := toolNames(c)
	if len(names) > MaxCoreTools || !slices.Contains(names, "run_change") || !slices.Contains(names, "draft_list") || slices.Contains(names, "render_submit") {
		t.Fatalf("core when proposing: %v", names)
	}
	if _, err := New(viaMCP(base), Options{Propose: true, AllowChanges: true}); err == nil {
		t.Fatal("a connection both proposing and changing")
	}

	// Reads and checks run; run never saves a draft; people's things are refused.
	if r := c.call("cut_show", map[string]any{"projectId": pid}); r["isError"] != false {
		t.Fatalf("cut_show: %v", r)
	}
	if r := c.call("run", map[string]any{"name": "edit.apply", "input": trimPlan(v1, 2)}); errCode(r) != apperr.Unauthorized {
		t.Fatalf("run with a change: %v", r)
	}
	for _, name := range []string{"revision.approve", "draft.decide"} {
		if r := c.call("run_change", map[string]any{"name": name, "input": map[string]any{"id": v1}}); errCode(r) != apperr.HumanOnly {
			t.Fatalf("run_change %s: %v", name, r)
		}
	}
	if r := c.call("run_change", map[string]any{"name": "render.submit", "input": map[string]any{"revisionId": v1, "quality": "final"}}); errCode(r) != apperr.Unauthorized || !strings.Contains(text(r), "final renders") {
		t.Fatalf("a final render as a draft: %v", r)
	}
	if r := c.call("project_create", map[string]any{"name": "Other"}); errCode(r) != apperr.Unauthorized {
		t.Fatalf("a change by its tool name: %v", r)
	}

	// A change is saved as a draft: the cut doesn't move.
	r := c.call("run_change", map[string]any{"name": "edit.apply", "input": trimPlan(v1, 2), "note": "The intro dragged."})
	if r["isError"] != false || !strings.Contains(text(r), "Saved as a draft") {
		t.Fatalf("run_change: %v", r)
	}
	var d ops.DraftView
	b, _ := json.Marshal(r["structuredContent"])
	_ = json.Unmarshal(b, &d)
	if d.Kind != models.DraftVersion || d.Agent != "codex" || d.Via != "mcp" || d.Note != "The intro dragged." {
		t.Fatalf("the draft: %+v", d)
	}
	var p models.Project
	_ = base.DB.GetByID(ctx, store.CProjects, base.WorkspaceID(), pid, &p)
	if p.Branches["main"].Head != v1 {
		t.Fatalf("a draft moved the cut to %s", p.Branches["main"].Head)
	}
	// So is a Content Settings change, here through a toolset's …_change tool.
	ce := start(t, viaMCP(base), Options{Propose: true, Toolset: ToolsetEdit, Agent: "Cursor"})
	ce.request("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "cursor-vscode", "version": "1"}})
	if rr := ce.call("brand_change", map[string]any{"mode": "set", "projectId": pid, "primary": "#123456", draftNoteField: "Your logo's blue."}); rr["isError"] != false || !strings.Contains(text(rr), "Saved as a draft") {
		t.Fatalf("brand_change set: %v", rr)
	}
	if rr := ce.call("brand", map[string]any{"mode": "set", "projectId": pid, "primary": "#123456"}); errCode(rr) != apperr.Unauthorized {
		t.Fatalf("a change through the reading tool: %v", rr)
	}
	brand, _ := ops.CallTyped[ops.BrandShowOut](ctx, base, "brand.show", ops.BrandShowIn{ProjectID: pid}, ops.Caller{})
	if brand.Kit.Primary == "#123456" {
		t.Fatal("a draft changed the brand")
	}
	list, _ := ops.CallTyped[ops.DraftListOut](ctx, base, "draft.list", ops.DraftListIn{All: true}, ops.Caller{})
	if len(list.Drafts) != 2 || list.Drafts[0].Agent != "cursor" || list.Drafts[0].Note != "Your logo's blue." || list.Drafts[1].Agent != "codex" {
		t.Fatalf("drafts: %+v", list.Drafts)
	}

	// A person says Not quite: the app hears it on its next call, once.
	person := *base
	person.Actor.Via = "dashboard"
	if _, err := ops.CallTyped[ops.DraftView](ctx, &person, "draft.decide", ops.DraftDecideIn{ID: d.ID, Decision: ops.DecideNotQuite, Note: "Keep the first second"}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	next := c.call("draft_list", map[string]any{})
	if !strings.Contains(allText(next), "News about your drafts") || !strings.Contains(allText(next), "Keep the first second") || !strings.Contains(allText(next), d.ID) {
		t.Fatalf("the news: %v", next)
	}
	if again := c.call("project_list", map[string]any{}); strings.Contains(allText(again), "News about your drafts") {
		t.Fatalf("told twice: %v", again)
	}
	// Its next draft takes the old one's place.
	r = c.call("run_change", map[string]any{"name": "edit.apply", "input": trimPlan(v1, 2.5), "note": "Kept the first second.", "replaces": d.ID})
	if r["isError"] != false {
		t.Fatalf("the next draft: %v", r)
	}
	list, _ = ops.CallTyped[ops.DraftListOut](ctx, base, "draft.list", ops.DraftListIn{Agent: "codex", State: models.DraftReplaced}, ops.Caller{})
	if len(list.Drafts) != 1 || list.Drafts[0].ID != d.ID {
		t.Fatalf("replaced: %+v", list.Drafts)
	}
}

// Every list keeps its guards when proposing: no tool both reads and saves,
// none offers what no draft may hold, and the core stays within its limit.
func TestProposeListsOfferOnlyDrafts(t *testing.T) {
	for _, set := range Toolsets() {
		list, err := ToolListFor(set, Propose)
		if err != nil {
			t.Fatal(err)
		}
		if set == ToolsetCore && len(list) > MaxCoreTools || set != ToolsetCore && set != ToolsetAll && len(list) > MaxToolsetTools {
			t.Errorf("%s: %d tools", set, len(list))
		}
		hasDrafts := false
		for _, tl := range list {
			hasDrafts = hasDrafts || tl.Name == toolDrafts
			ro, _ := tl.Annotations["readOnlyHint"].(bool)
			for _, name := range tl.Ops() {
				op, ok := ops.Get(name)
				if !ok {
					continue
				}
				if op.HumanOnly || (op.Mutating && !ops.Draftable(op)) {
					t.Errorf("%s: %s offers %s, which no draft may hold", set, tl.Name, name)
				}
				if op.Mutating == ro {
					t.Errorf("%s: %s mixes reading and saving drafts (%s)", set, tl.Name, name)
				}
			}
			if !ro && tl.special == "" && tl.op != nil && !strings.Contains(tl.Description, "saves a draft") {
				t.Errorf("%s: %s doesn't say it saves a draft: %s", set, tl.Name, tl.Description)
			}
		}
		if !hasDrafts {
			t.Errorf("%s: no %s", set, toolDrafts)
		}
	}
}

// allText is every text block of a tool result.
func allText(r map[string]any) string {
	var out []string
	content, _ := r["content"].([]any)
	for _, c := range content {
		if m, ok := c.(map[string]any); ok {
			if s, ok := m["text"].(string); ok {
				out = append(out, s)
			}
		}
	}
	return strings.Join(out, "\n")
}
