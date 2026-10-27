package logops

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func call[O any](t *testing.T, a *app.App, name string, in any, c ops.Caller) (*O, error) {
	t.Helper()
	return ops.CallTyped[O](context.Background(), a, name, in, c)
}

func code(err error) string {
	if ae := apperr.From(err); ae != nil {
		return ae.Code
	}
	return ""
}

// seed writes a few entries through the real paths (ops and a Claude turn).
func seed(t *testing.T, a *app.App) (projectID string) {
	t.Helper()
	ctx := context.Background()
	p, err := call[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Spring promo"}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call[ops.ProjectShowOut](t, a, "project.show", ops.ProjectShowIn{ID: "prj_0000000000000000000000000x"}, ops.Caller{}); err == nil {
		t.Fatal("expected not found")
	}
	a.Log.Claude(ctx, logbook.Turn{Source: logbook.SourceAssistant, ConversationID: "asc_01h00000000000000000000001", Turn: 1,
		Prompt: "make the opening punchier", Response: "MetaRush has no built-in way to split a clip at a word, so I'll work around it.",
		StartedAt: time.Now(), Refs: logbook.Refs{ProjectID: p.Project.ID}})
	a.Log.Flush(ctx)
	return p.Project.ID
}

func TestListSearchShow(t *testing.T) {
	a := testapp.New(t)
	projectID := seed(t, a)
	out, err := call[LogListOut](t, a, "log.list", LogListIn{}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	texts := map[string]bool{}
	for _, r := range out.Entries {
		texts[r.Text] = true
	}
	for _, want := range []string{"Created the project Spring promo", "Couldn't open a project: it wasn't found",
		`Asked Claude Code: “make the opening punchier”`} {
		if !texts[want] {
			t.Errorf("missing row %q in %v", want, texts)
		}
	}
	probs, _ := call[LogListOut](t, a, "log.list", LogListIn{Problems: true}, ops.Caller{})
	if len(probs.Entries) != 1 || probs.Entries[0].Op.Name != "project.show" {
		t.Fatalf("problems: %+v", probs.Entries)
	}
	byProject, _ := call[LogListOut](t, a, "log.list", LogListIn{Project: projectID}, ops.Caller{})
	if len(byProject.Entries) < 2 {
		t.Fatalf("project filter: %d", len(byProject.Entries))
	}
	found, err := call[LogListOut](t, a, "log.search", LogListIn{Query: "punchier"}, ops.Caller{})
	if err != nil || len(found.Entries) != 1 || found.Entries[0].Kind != logbook.KindClaude {
		t.Fatalf("search: %+v %v", found, err)
	}
	part, _ := call[LogListOut](t, a, "log.search", LogListIn{Query: "punch"}, ops.Caller{}) // part of a word
	if len(part.Entries) != 1 {
		t.Fatalf("partial search: %d", len(part.Entries))
	}
	show, err := call[LogShowOut](t, a, "log.show", LogShowIn{ID: found.Entries[0].ID}, ops.Caller{})
	if err != nil || show.Prompt != "make the opening punchier" || !strings.Contains(show.Response, "work around") {
		t.Fatalf("show: %+v %v", show, err)
	}
	// Paging.
	page1, _ := call[LogListOut](t, a, "log.list", LogListIn{Limit: 1}, ops.Caller{})
	page2, _ := call[LogListOut](t, a, "log.list", LogListIn{Limit: 1, Before: page1.Next}, ops.Caller{})
	if page1.Next == "" || len(page2.Entries) != 1 || page2.Entries[0].ID == page1.Entries[0].ID {
		t.Fatalf("paging: %+v / %+v", page1, page2)
	}
}

// The dashboard assistant reads the log but never the words of conversations.
func TestAssistantCannotReadConversations(t *testing.T) {
	a := testapp.New(t)
	seed(t, a)
	as := *a
	as.Actor.Via = models.ViaAssistant
	out, err := call[LogListOut](t, &as, "log.list", LogListIn{}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	var turn *LogRow
	for i := range out.Entries {
		if out.Entries[i].Kind == logbook.KindClaude {
			turn = &out.Entries[i]
		}
	}
	if turn == nil || turn.Claude.Prompt != "" || turn.Claude.Response != "" || strings.Contains(turn.Text, "punchier") {
		t.Fatalf("assistant sees the conversation: %+v", turn)
	}
	if found, _ := call[LogListOut](t, &as, "log.search", LogListIn{Query: "punchier"}, ops.Caller{}); len(found.Entries) != 0 {
		t.Fatalf("assistant search found conversation words: %+v", found.Entries)
	}
	show, err := call[LogShowOut](t, &as, "log.show", LogShowIn{ID: turn.ID}, ops.Caller{})
	if err != nil || show.Prompt != "" || show.Response != "" {
		t.Fatalf("assistant show: %+v %v", show, err)
	}
	if _, err := call[LogExportOut](t, &as, "log.export", LogExportIn{}, ops.Caller{}); code(err) != apperr.Unauthorized {
		t.Fatalf("assistant export: %v", err)
	}
}

// Deleting log entries is for people only, and never automatic.
func TestPurgeIsHumanOnly(t *testing.T) {
	a := testapp.New(t)
	seed(t, a)
	ctx := context.Background()
	in := LogPurgeIn{Before: "2099-01-01"}
	if _, err := call[LogPurgeOut](t, a, "log.purge", LogPurgeIn{Before: "1d"}, ops.Caller{}); code(err) != apperr.HumanOnly {
		t.Fatalf("purge without a person: %v", err)
	}
	as := *a
	as.Actor.Via = models.ViaAssistant
	if _, err := call[LogPurgeOut](t, &as, "log.purge", LogPurgeIn{Before: "1d"}, ops.Caller{Human: true}); code(err) != apperr.Unauthorized {
		t.Fatalf("purge from the assistant: %v", err)
	}
	if _, err := call[LogPurgeOut](t, a, "log.purge", in, ops.Caller{Human: true}); code(err) != apperr.InvalidInput {
		t.Fatalf("purge into the future: %v", err)
	}
	a.Log.Flush(ctx)
	before, _ := call[LogListOut](t, a, "log.list", LogListIn{Since: "30d", Limit: 1000}, ops.Caller{})
	time.Sleep(1100 * time.Millisecond) // everything so far is older than the cut-off (second precision)
	res, err := call[LogPurgeOut](t, a, "log.purge", LogPurgeIn{Before: time.Now().UTC().Format(time.RFC3339)}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Deleted < int64(len(before.Entries)) {
		t.Fatalf("deleted %d of %d", res.Deleted, len(before.Entries))
	}
	a.Log.Flush(ctx)
	after, _ := call[LogListOut](t, a, "log.list", LogListIn{Since: "30d", Kind: []string{logbook.KindClaude}}, ops.Caller{})
	if len(after.Entries) != 0 {
		t.Fatalf("entries left: %+v", after.Entries)
	}
}

// Settings changes are for people; privacy applies at once.
func TestConfigure(t *testing.T) {
	a := testapp.New(t)
	local := "local"
	if _, err := call[LogSettingsOut](t, a, "log.configure", LogConfigureIn{Prompts: &local}, ops.Caller{}); code(err) != apperr.HumanOnly {
		t.Fatalf("configure without a person: %v", err)
	}
	out, err := call[LogSettingsOut](t, a, "log.configure", LogConfigureIn{Prompts: &local}, ops.Caller{Human: true})
	if err != nil || out.Prompts != "local" || !strings.Contains(out.Explanation, "only on this machine") {
		t.Fatalf("configure: %+v %v", out, err)
	}
	if a.Log.Privacy() != logbook.PrivacyLocal {
		t.Fatal("privacy not applied")
	}
	bad := "everywhere"
	if _, err := call[LogSettingsOut](t, a, "log.configure", LogConfigureIn{Prompts: &bad}, ops.Caller{Human: true}); code(err) != apperr.InvalidInput {
		t.Fatalf("bad prompts value: %v", err)
	}
	// The ask op's prompt is not written to the workspace in local mode.
	raw, _ := json.Marshal(map[string]any{"prompt": "my secret idea"})
	rec := a.Log.StartOp(context.Background(), "assistant.ask", raw, a.Actor, true, false)
	rec.End(true, nil, nil)
	a.Log.Flush(context.Background())
	got, _ := call[LogListOut](t, a, "log.list", LogListIn{Op: "assistant.ask"}, ops.Caller{})
	if len(got.Entries) != 1 || strings.Contains(got.Entries[0].Op.Input["prompt"].(string), "secret idea") {
		t.Fatalf("op input in local mode: %+v", got.Entries)
	}
}
