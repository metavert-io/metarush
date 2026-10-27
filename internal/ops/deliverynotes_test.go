package ops_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// reviewRender stores a finished review copy of a version (random bytes in
// the media store), so delivery can export it.
func reviewRender(t *testing.T, a *app.App, revisionID string) models.Render {
	t.Helper()
	ctx := context.Background()
	var r models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), revisionID, &r); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4096)
	_, _ = rand.Read(b)
	p := filepath.Join(t.TempDir(), "render.mp4")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	sha, _ := media.HashFile(p)
	key := media.ObjectKey(a.WorkspaceID(), sha)
	if _, err := a.Media.PutFile(ctx, key, p, media.PutOptions{SHA256: sha}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rd := models.Render{ID: ids.New(ids.Render), WorkspaceID: a.WorkspaceID(), ProjectID: r.ProjectID, RevisionID: r.ID, RevisionNumber: r.Number, SpecHash: r.SpecHash,
		Preset: r.Spec.Output.Preset, Quality: "review", State: models.RenderAvailable, CreatedAt: now, CompletedAt: &now,
		Output: &models.RenderOutput{StorageKey: key, SHA256: sha, Size: int64(len(b)), Duration: r.Duration, Width: 540, Height: 960, FPS: 30}}
	if err := a.DB.Insert(ctx, store.CRenders, rd); err != nil {
		t.Fatal(err)
	}
	return rd
}

// A version's delivery notes (D237): written by Claude or a person, checked
// against the places videos go, kept outside the spec, and written next to the
// video when it is delivered.
func TestDeliveryNotes(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := project(t, a, "Garden story")
	as := txClip(t, a, "Interview", p.ID, []models.Word{wd("We", 0.1, 0.3, 0.9), wd("met", 0.35, 0.6, 0.9), wd("in", 0.7, 0.9, 0.9), wd("Elmwood.", 1.0, 2.2, 0.9)})
	rid := captionedCut(t, a, p.ID, as.ID)
	var before models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &before)

	none := do[ops.DeliveryNotesOut](t, a, "delivery.notes", ops.DeliveryNotesIn{ProjectID: p.ID})
	if none.Notes != nil || none.Number != 1 || !strings.Contains(none.Summary, "no delivery notes") {
		t.Fatalf("no notes yet: %+v", none)
	}

	// Markdown as Claude writes it, chapters in the description or their own section.
	md := "# The garden at Elmwood\n\n## Other titles\n- How we met\n- Elmwood, a garden story\n\n## Description\nA walk through the garden.\n\n" +
		"## Chapters\n0:00 We meet\n1.5 Not a time\n"
	if _, err := ops.ParseDeliveryNotes([]byte(md)); err == nil {
		t.Fatal("a chapter without a time was read")
	}
	md = strings.Replace(md, "1.5 Not a time", "0:01 Elmwood", 1) + "\n## Show notes\nWe talk about **Elmwood**.\n"
	d, err := ops.ParseDeliveryNotes([]byte(md))
	if err != nil || d.Title != "The garden at Elmwood" || len(d.Titles) != 2 || d.Description != "A walk through the garden." || len(d.Chapters) != 2 ||
		d.Chapters[1].At != 1 || d.Chapters[1].Title != "Elmwood" || !strings.Contains(d.ShowNotes, "**Elmwood**") {
		t.Fatalf("markdown %+v %v", d, err)
	}
	// JSON too, with times as text or seconds.
	j, err := ops.ParseDeliveryNotes([]byte(`{"title":"T","chapters":[{"at":"0:00","title":"Start"},{"at":1.2,"title":"Next"},"0:02 Last"]}`))
	if err != nil || len(j.Chapters) != 3 || j.Chapters[1].At != 1.2 || j.Chapters[2].Title != "Last" {
		t.Fatalf("json %+v %v", j, err)
	}

	// Checked first: chapters inside the video, the first at 0:00.
	bad := []ops.DeliveryNotesDraft{
		{Chapters: []models.Chapter{{At: 1, Title: "Late start"}, {At: 2, Title: "Next"}}},
		{Chapters: []models.Chapter{{At: 0, Title: "Start"}, {At: 9, Title: "After the end"}}},
		{Title: strings.Repeat("x", 101)},
		{},
	}
	for _, n := range bad {
		if err := doErr(a, "delivery.notes.set", ops.DeliveryNotesSetIn{ProjectID: p.ID, Notes: n}, person); code(err) != "E_INVALID_INPUT" {
			t.Fatalf("%+v accepted: %v", n, err)
		}
	}

	set := do[ops.DeliveryNotesOut](t, a, "delivery.notes.set", ops.DeliveryNotesSetIn{ProjectID: p.ID, Revision: "r1", Notes: *d, WrittenBy: models.WrittenByClaude})
	if set.Notes == nil || set.Notes.WrittenBy != "claude" || len(set.Chapters) != 2 || set.Chapters[1] != "0:01 Elmwood" ||
		!strings.Contains(set.Summary, "a title, 2 other titles, a description, 2 chapters and show notes") || !strings.Contains(set.Summary, "Claude wrote them") {
		t.Fatalf("set %+v", set)
	}
	// Too few and too short chapters for YouTube: said, not refused.
	if len(set.Warnings) < 2 || !strings.Contains(strings.Join(set.Warnings, " "), "at least three") {
		t.Fatalf("warnings %v", set.Warnings)
	}
	for _, s := range append([]string{set.Summary}, set.Warnings...) {
		if err := plainwords.CheckShown(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	if !strings.Contains(set.Markdown, "# The garden at Elmwood") || !strings.Contains(set.Markdown, "A walk through the garden.\n\n0:00 We meet\n0:01 Elmwood") ||
		!strings.Contains(set.Markdown, "written by Claude Code") {
		t.Fatalf("markdown %s", set.Markdown)
	}
	// Outside the spec: the version plays what it played.
	var after models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &after)
	if after.SpecHash != before.SpecHash || after.DeliveryNotes == nil || after.DeliveryNotes.Title != "The garden at Elmwood" {
		t.Fatalf("version %+v", after.DeliveryNotes)
	}

	// Delivery writes them into the record and next to the video.
	reviewRender(t, a, rid)
	dir := t.TempDir()
	out, err := ops.CallTyped[ops.DeliverOut](ctx, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: dir, Quality: "review", AllowDraft: true}, person)
	if err != nil || len(out.Files) != 1 || !strings.Contains(out.Note, ".notes.md") {
		t.Fatalf("deliver %+v %v", out, err)
	}
	var sc ops.DeliverySidecar
	b, _ := os.ReadFile(out.Files[0].Sidecar)
	_ = json.Unmarshal(b, &sc)
	if sc.Notes == nil || sc.Notes.Title != "The garden at Elmwood" || !strings.HasSuffix(sc.NotesFile, ".notes.md") {
		t.Fatalf("sidecar %s", b)
	}
	nf, err := os.ReadFile(filepath.Join(dir, sc.NotesFile))
	if err != nil || !strings.Contains(string(nf), "0:01 Elmwood") {
		t.Fatalf("notes file %q %v", nf, err)
	}

	// Nobody said who wrote them: the channel says (D237) — an agent through
	// MCP or a script, a person at the dashboard or a terminal.
	mcp := *a
	mcp.Actor.Via = "mcp"
	ag, err := ops.CallTyped[ops.DeliveryNotesOut](ctx, &mcp, "delivery.notes.set", ops.DeliveryNotesSetIn{ProjectID: p.ID, Notes: ops.DeliveryNotesDraft{Title: "From an agent"}}, ops.Caller{})
	if err != nil || ag.Notes.WrittenBy != models.WrittenByAgent || !strings.Contains(ag.Markdown, "written by an agent") {
		t.Fatalf("an agent's notes: %+v %v", ag, err)
	}
	if pn := do[ops.DeliveryNotesOut](t, a, "delivery.notes.set", ops.DeliveryNotesSetIn{ProjectID: p.ID, Notes: ops.DeliveryNotesDraft{Title: "Mine"}}); pn.Notes.WrittenBy != models.WrittenByPerson {
		t.Fatalf("a person's notes: %+v", pn.Notes)
	}
	// Only a person says who wrote them: an agent or a script saying "a
	// person" (which would keep Auto from writing over them) or "Claude" is
	// recorded as what its channel is.
	cli := *a
	cli.Actor.Via = "cli"
	t.Setenv("CLAUDECODE", "")
	for _, c := range []struct {
		name   string
		a      *app.App
		caller ops.Caller
		said   string
		want   string
	}{
		{"MCP saying a person", &mcp, ops.Caller{}, models.WrittenByPerson, models.WrittenByAgent},
		{"MCP saying Claude", &mcp, ops.Caller{}, models.WrittenByClaude, models.WrittenByAgent},
		{"a script saying a person", &cli, ops.Caller{}, models.WrittenByPerson, models.WrittenByAgent},
		{"a person pasting Claude's", a, person, models.WrittenByClaude, models.WrittenByClaude},
		{"a person saying an agent", a, person, models.WrittenByAgent, models.WrittenByAgent},
	} {
		got, err := ops.CallTyped[ops.DeliveryNotesOut](ctx, c.a, "delivery.notes.set", ops.DeliveryNotesSetIn{ProjectID: p.ID, Notes: ops.DeliveryNotesDraft{Title: c.name}, WrittenBy: c.said}, c.caller)
		if err != nil || got.Notes.WrittenBy != c.want {
			t.Errorf("%s: %+v %v, want %s", c.name, got, err, c.want)
		}
	}
	t.Setenv("CLAUDECODE", "1") // Claude Code in a terminal ran the command
	if got, err := ops.CallTyped[ops.DeliveryNotesOut](ctx, &cli, "delivery.notes.set", ops.DeliveryNotesSetIn{ProjectID: p.ID, Notes: ops.DeliveryNotesDraft{Title: "From Claude Code"}, WrittenBy: models.WrittenByPerson}, ops.Caller{}); err != nil || got.Notes.WrittenBy != models.WrittenByClaude {
		t.Fatalf("Claude Code in a terminal: %+v %v", got, err)
	}
	if err := doErr(&mcp, "delivery.notes.set", ops.DeliveryNotesSetIn{ProjectID: p.ID, Notes: ops.DeliveryNotesDraft{Title: "x"}, WrittenBy: "robot"}, ops.Caller{}); code(err) != "E_INVALID_INPUT" {
		t.Fatalf("an unknown writer: %v", err)
	}

	// A person clears them.
	cl := do[ops.DeliveryNotesOut](t, a, "delivery.notes.set", ops.DeliveryNotesSetIn{ProjectID: p.ID, Clear: true})
	if cl.Notes != nil || !strings.Contains(cl.Summary, "no delivery notes now") {
		t.Fatalf("clear %+v", cl)
	}
}
