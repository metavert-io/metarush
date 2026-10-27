package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/store"
)

// Conversation changes Claude asks for become cards (D111): the automatic cut
// in a style, clean-up suggestions accepted or skipped (one, or all that are
// open), an angle changed, a short made, the call looked over. Each is
// previewed with nothing saved, says what it does in plain words, and runs the
// same op as the CLI, with apply, when a person confirms it. The read-only
// conversation commands run for Claude; a call folder or the Conversation
// tab's own changes point the person there.
func TestConversationRequestsBecomeCards(t *testing.T) {
	a := testApp(t, "")
	f := callFixture(t, a)
	ctx := context.Background()
	q := `"Garden call"`
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{
		// Reads run (no cards).
		tool("Bash", map[string]any{"command": "metarush conversation show " + q + " --json"}, `{"ok":true,"data":{}}`),
		tool("Bash", map[string]any{"command": "metarush conversation moments " + q + " --count 3 --json"}, `{"ok":true,"data":{}}`),
		tool("Bash", map[string]any{"command": "metarush conversation styles --json"}, `{"ok":true,"data":{}}`),
		// Changes become cards.
		denied("metarush conversation cut " + q + " --project " + f.project + " --style reactions --apply --json"),
		denied("metarush conversation decide " + q + " c1 --accept --json"),
		denied("metarush conversation decide " + q + " open --skip --json"),
		denied("metarush conversation angle --project " + f.project + " --at 0:31 --member sam --why 'Her reaction to the bucket.' --json"),
		denied("metarush conversation angle --project " + f.project + " --at 0:12.3 --layout side-by-side --members maya,sam --json"),
		denied("metarush call short " + q + " --from 0:28.5 --to 0:41.5 --json"),
		denied("metarush conversation cleanup " + q + " --kinds call-talk,dead-air --json"),
		denied("metarush conversation import /Users/you/Documents/Zoom/2026-10-19 --project " + f.project),
		denied("metarush conversation nudge " + q + " --member sam --frames 1"),
		// Not cards: a conversation that isn't there, a style there isn't.
		denied("metarush conversation decide " + `"No such call"` + " c1 --accept"),
		denied("metarush conversation cut " + q + " --style documentary"),
		text("I asked you to confirm those."),
	}}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "cut the call, clean it up and make a short", ProjectID: f.project}})
	if err != nil {
		t.Fatal(err)
	}
	cv := waitIdle(t, m, out.Conversation.ID)
	var labels []string
	for _, e := range cv.Events {
		if e.Kind == "tool" {
			labels = append(labels, e.Tool.Label)
		}
	}
	for _, want := range []string{"Looked at “Garden call”", "Looked for the best moments in “Garden call”", "Asked to make the cut of “Garden call”",
		"Asked to skip clean-up suggestions", "Asked to make a short from “Garden call”"} {
		if !has(labels, want) {
			t.Errorf("no tool row %q in %q", want, labels)
		}
	}
	by, kinds := cardsOf(t, cv)
	want := []string{"conversation cut", "conversation decide", "conversation decide", "conversation angle", "conversation angle", "conversation short",
		"conversation cleanup", "conversation import", "conversation nudge"}
	if strings.Join(kinds, "|") != strings.Join(want, "|") {
		t.Fatalf("cards %v, want %v (reads and requests naming nothing real are never cards)", kinds, want)
	}
	for _, act := range cv.Actions {
		for _, s := range append([]string{act.Title, act.Unavailable}, act.Details...) {
			if fs := plainwords.CheckShown(s); len(fs) > 0 {
				t.Errorf("card %q: %q is not plain: %v", act.Kind, s, fs)
			}
		}
	}
	cut := by["conversation cut"][0]
	if cut.Op != "conversation.cut" || cut.State != ActionPending || cut.Title != "Make an automatic cut of “Garden call”: reaction-heavy" ||
		!strings.Contains(cut.Input, `"apply":true`) || !strings.Contains(cut.Input, f.conv) || !strings.Contains(cut.Input, `"style":"reactions"`) ||
		cut.Check == nil || !cut.Check.Valid || cut.Check.ConfirmToken != "" {
		t.Fatalf("cut card: %+v", cut)
	}
	details := strings.Join(cut.Details, "|")
	for _, w := range []string{"listening faces", "shots, each with its reason", "Replaces the current cut with a new version; the current one stays in History"} {
		if !strings.Contains(details, w) {
			t.Errorf("cut card lacks %q: %q", w, details)
		}
	}
	accept, skip := by["conversation decide"][0], by["conversation decide"][1]
	if accept.Op != "conversation.decide" || accept.Title != "Cut out the talk about the call at 0:00.4–0:02.2 (“Hi Sam, can you hear me okay?”)" ||
		!strings.Contains(accept.Input, `"ids":["c1"]`) || !strings.Contains(accept.Input, `"decision":"accept"`) || accept.Check == nil || !accept.Check.Valid ||
		!strings.Contains(strings.Join(accept.Details, "|"), "shorter") {
		t.Fatalf("accept card: %+v (details %q)", accept, accept.Details)
	}
	if skip.Title != "Skip all 4 clean-up suggestions for “Garden call”" || !strings.Contains(skip.Input, `"ids":["c1","c2","c3","c4"]`) ||
		!strings.Contains(skip.Input, `"decision":"skip"`) || !strings.Contains(strings.Join(skip.Details, "|"), "0:33.0–0:35.0: Sam's picture freezes for 2 s") ||
		!strings.Contains(strings.Join(skip.Details, "|"), "Nothing in the cut changes") {
		t.Fatalf("skip-all card: %+v", skip)
	}
	sam, layout := by["conversation angle"][0], by["conversation angle"][1]
	if sam.Op != "conversation.angle" || sam.Title != "Show Sam from 0:31.0 in the conversation" || !strings.Contains(strings.Join(sam.Details, "|"), "Why: Her reaction to the bucket.") ||
		sam.Check == nil || !sam.Check.Valid {
		t.Fatalf("angle card: %+v", sam)
	}
	if layout.Title != "Show Maya and Sam side by side from 0:12.3 in the conversation" {
		t.Fatalf("layout card: %+v", layout)
	}
	short := by["conversation short"][0]
	if short.Op != "conversation.short" || short.Title != "Make a short from 0:28.5–0:41.5 of “Garden call”" ||
		!strings.Contains(strings.Join(short.Details, "|"), "A new project, “Garden call: What surprised you the most…”, with a cut of its own: vertical 9:16") {
		t.Fatalf("short card: %+v", short)
	}
	look := by["conversation cleanup"][0]
	if look.Op != "conversation.cleanup" || look.Title != "Look “Garden call” over for what calls leave behind" || look.Details[0] != "Talk about the call and long silences" {
		t.Fatalf("clean-up card: %+v", look)
	}
	if imp := by["conversation import"][0]; imp.State != ActionUnavailable || imp.Op != "" || !strings.Contains(imp.Unavailable, "Add box") {
		t.Fatalf("a call folder is added from the dashboard: %+v", imp)
	}
	if n := by["conversation nudge"][0]; n.State != ActionUnavailable || n.Unavailable != "Do this on the project's Conversation tab." {
		t.Fatalf("the Conversation tab's own changes: %+v", n)
	}

	// Building the cards changed nothing: no version, no decision, no remembered style.
	load := func() models.Conversation {
		t.Helper()
		var c models.Conversation
		if err := a.DB.GetByID(ctx, store.CConversations, a.WorkspaceID(), f.conv, &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	before := load()
	if before.Style != "podcast" {
		t.Fatalf("a card's preview remembered the style %q", before.Style)
	}
	for _, s := range before.Cleanup {
		if s.State != "open" {
			t.Fatalf("a card's preview decided %s: %s", s.ID, s.State)
		}
	}
	var p models.Project
	_ = a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), f.project, &p)
	if p.Branches["main"].Head != f.head {
		t.Fatal("a card's preview saved a version")
	}

	// A person confirms them, on the human channel; each lands in the cut.
	if done := actAsPerson(t, a, cv.ID, accept.ID); done.State != ActionDone || !strings.Contains(done.Result, "It is in the cut now as version") ||
		!strings.HasPrefix(done.Link, "/p/"+f.project+"/r/") {
		t.Fatalf("accept ran: %+v", done)
	}
	if c := load(); c.Cleanup[0].State != "accepted" || c.Cleanup[0].Revision == "" {
		t.Fatalf("c1 after accepting: %+v", c.Cleanup[0])
	}
	if done := actAsPerson(t, a, cv.ID, sam.ID); done.State != ActionDone || !strings.Contains(done.Result, "It is in the cut now as version") {
		t.Fatalf("angle ran: %+v", done)
	}
	if done := actAsPerson(t, a, cv.ID, skip.ID); done.State != ActionDone || done.Result != "Skipped 4 suggestions" {
		t.Fatalf("skip ran: %+v", done)
	}
	if c := load(); c.Cleanup[1].State != "skipped" || c.Cleanup[3].State != "skipped" {
		t.Fatalf("after skipping: %+v", c.Cleanup)
	}
	if done := actAsPerson(t, a, cv.ID, cut.ID); done.State != ActionDone || !strings.Contains(done.Result, "It is in the cut now as version") {
		t.Fatalf("cut ran: %+v", done)
	}
	if c := load(); c.Style != "reactions" {
		t.Fatalf("a confirmed cut remembers its style: %q", c.Style)
	}
	done := actAsPerson(t, a, cv.ID, short.ID)
	if done.State != ActionDone || !strings.HasPrefix(done.Link, "/p/prj_") || !strings.HasPrefix(done.Result, "Made Garden call: What surprised you the most") {
		t.Fatalf("short ran: %+v", done)
	}
	var made models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), strings.TrimPrefix(done.Link, "/p/"), &made); err != nil || made.Branches["main"].Head == "" {
		t.Fatalf("the short's project and cut: %+v %v", made, err)
	}
}
