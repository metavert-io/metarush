package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/ops"
)

// A layout pack Claude proposes (D266): its pack file in the
// proposals folder and `content pack propose` become a card only a person
// decides (never Auto); accepting it keeps it as the proposed pack while its
// motion graphic waits, and a person's acceptance once it is approved makes
// it the pack. A pack file outside the proposals folder makes no card.
func TestLayoutPackCards(t *testing.T) {
	a := testApp(t, "")
	projectID, _, _ := fixture(t, a)
	ctx := context.Background()
	if _, err := ops.CallTyped[ops.ContentShowOut](ctx, a, "content.own", ops.ContentOwnIn{ProjectID: projectID}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	hash, theme := strings.Repeat("ac", 32), strings.Repeat("7e", 32)
	if _, err := motion.SaveCheck(ctx, a.DB, &models.MotionComponent{WorkspaceID: a.WorkspaceID(), Hash: theme, Name: "acme-theme", Kind: models.MotionKindLibrary, ProjectID: projectID, Region: "full", SchemaJSON: `{}`,
		Files: []models.MotionFile{}, Imports: []models.MotionImport{}, Check: models.MotionCheckInfo{By: a.Actor, At: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	if _, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), theme, motion.Approve, a.Actor, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := motion.SaveCheck(ctx, a.DB, &models.MotionComponent{WorkspaceID: a.WorkspaceID(), Hash: hash, Name: "acme-title", ProjectID: projectID, Region: "full", MinDuration: 2, MaxDuration: 6, Libraries: []string{"acme-theme"},
		SchemaJSON: `{"type":"object","properties":{"headline":{"type":"string"},"wipe":{"type":"string","enum":["left","right"]}},"required":["headline"],"additionalProperties":false}`,
		Files:      []models.MotionFile{}, Imports: []models.MotionImport{}, Check: models.MotionCheckInfo{By: a.Actor, At: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	pack := `{"name":"Acme spring","pieces":{"title-card":{"motion":{"name":"acme-title","hash":"` + hash + `"},"fields":{"wipe":"left"},"logo":true},` +
		`"lower-third":{"template":"lower-third.cinematic","fields":{"align":"center"}}},"layouts":{"pip":{"shape":"circle","frame":"accent"}}}`
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "pack", Steps: []fakeclaude.Step{
			{Write: &fakeclaude.File{Name: "pack.json", Content: pack}},
			denied("metarush content pack propose --project " + projectID + " --file {{proposals}}/pack.json --note \"Acme's spring look\" --json"),
			denied("metarush content pack propose --project " + projectID + " --file /etc/hosts --json"),
			denied("metarush content pack apply --project " + projectID + " --json"),
			text("Here is a pack."),
		}},
		{Match: "", Steps: []fakeclaude.Step{text("Noted.")}},
	}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "draw a layout pack for Acme", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	cv := waitIdle(t, m, id)
	if len(cv.Actions) != 1 {
		t.Fatalf("one card (a file outside the proposals folder makes none, nor does a cut that follows the pack): %+v", cv.Actions)
	}
	card := cv.Actions[0]
	var in ops.ContentPackAcceptIn
	if card.Op != "content.pack.accept" || !card.HumanOnly || card.State != ActionPending || json.Unmarshal([]byte(card.Input), &in) != nil ||
		in.Pack == nil || in.Pack.Name != "Acme spring" || in.Origin != "assistant:"+id || in.Note != "Acme's spring look" || in.BaseVersion == nil {
		t.Fatalf("the card: %+v %+v", card, in)
	}
	// The card carries the pack as checked when it was made, its library pinned, and says so.
	if m := in.Pack.Pieces["title-card"].Motion; m == nil || m.Libs["acme-theme"] != theme {
		t.Fatalf("the card's pack pins the library: %+v", m)
	}
	if !strings.Contains(strings.Join(card.Details, " | "), "with the shared library acme-theme version 7e7e7e7e") {
		t.Fatalf("the card says the library's version: %v", card.Details)
	}
	d := strings.Join(card.Details, " | ")
	if !strings.Contains(card.Title, "Use “Acme spring” as the layout pack") || !strings.Contains(d, "Title card: the motion graphic acme-title") ||
		!strings.Contains(d, "Waits for a person to approve acme-title") || !strings.Contains(card.Review, "/motion/"+hash) {
		t.Fatalf("what the card says: %s | %s | %s", card.Title, d, card.Review)
	}
	c, _ := m.live(ctx, id, false)
	if c.autoStep(&card) || c.safeStep(&card) {
		t.Fatal("a layout pack is a person's decision, never Auto's")
	}

	// The person accepts before approving the graphic: kept as the proposed pack.
	dash := *a
	dash.Actor.Via = "dashboard"
	act, _ := json.Marshal(ConversationActIn{ID: id, ActionID: card.ID})
	res, err := ops.Call(ctx, &dash, "assistant.act", act, ops.Caller{Human: true, Remote: true})
	if err != nil {
		t.Fatal(err)
	}
	done := res.(*ConversationActOut).Action
	if done.State != ActionDone || !strings.Contains(done.Result, "it isn't the pack yet") {
		t.Fatalf("kept: %+v", done)
	}
	rec, _ := content.Record(ctx, a, projectID)
	if rec == nil || rec.Pack != nil || rec.PackDraft == nil || rec.PackDraft.Origin != "assistant:"+id {
		t.Fatalf("the settings keep it as the proposed pack: %+v", rec)
	}
	// Approved; then the person accepts the proposed pack.
	if _, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), hash, motion.Approve, a.Actor, ""); err != nil {
		t.Fatal(err)
	}
	acc, err := ops.CallTyped[ops.ContentPackOut](ctx, &dash, "content.pack.accept", ops.ContentPackAcceptIn{ProjectID: projectID}, ops.Caller{Human: true, Remote: true})
	if err != nil {
		t.Fatal(err)
	}
	if !acc.Own || acc.Pack.Name != "Acme spring" || acc.Draft != nil || acc.Pack.Version != 1 {
		t.Fatalf("the pack: %+v", acc)
	}
	// Claude's context says it.
	if line := ops.PackContext(ctx, a, projectID); !strings.Contains(line, "acme-title") {
		t.Fatalf("context: %s", line)
	}

	// The commands' cards and labels.
	mm := &Manager{App: a, names: map[string]string{}}
	for cmd, want := range map[string]string{
		"metarush content pack set --project " + projectID + " --role quote-card --template quote.kinetic":        "content.pack.set",
		"metarush content pack set --project " + projectID + " --role title-card --motion acme-title@" + hash[:8]: "",
		"metarush content pack decline --project " + projectID:                                                    "content.pack.decline",
	} {
		words, _ := splitCommand(cmd)
		call, _ := parseMetarush(words)
		x := &AssistantAction{Kind: call.Path}
		mm.describeAction(ctx, nil, &call, x)
		if x.Op != want {
			t.Errorf("%s: %+v", cmd, x)
		}
	}
	for cmd, label := range map[string]string{"metarush content pack show --json": "Looked at the layout pack", "metarush content pack propose --file x": "Proposed a layout pack"} {
		words, _ := splitCommand(cmd)
		call, _ := parseMetarush(words)
		if got := contentLabel(&call); got != label {
			t.Errorf("%s: %q", cmd, got)
		}
	}
	if words, _ := splitCommand("metarush content pack show --project x --json"); !readOnlyCall(mustParse(t, words)) {
		t.Fatal("looking at the pack is a read")
	}
}
