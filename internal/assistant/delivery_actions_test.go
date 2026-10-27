package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/store"
)

// Delivery notes Claude writes in the panel (D237): reading them is Claude's
// own; keeping them is a card built from the file in the proposals folder,
// checked first, which the person keeps with one click (and Auto runs).
func TestDeliveryNotesCard(t *testing.T) {
	a := testApp(t, "")
	projectID, _, headID := fixture(t, a)
	ctx := context.Background()
	m := &Manager{App: a, names: map[string]string{}}
	c := &conv{id: "cnv_notes"}
	dir := m.turnDir(c.id, 1)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	card := func(command string) *AssistantAction {
		t.Helper()
		words, err := splitCommand(command)
		if err != nil {
			t.Fatal(err)
		}
		call, ok := parseMetarush(words)
		if !ok || readOnlyCall(call) {
			t.Fatalf("%s should be a change for Claude (%+v)", command, call)
		}
		act := &AssistantAction{Kind: call.Path, State: ActionPending}
		m.describeAction(ctx, c, &call, act)
		for _, s := range append([]string{act.Title}, act.Details...) {
			if err := plainwords.CheckPanel(s); err != nil {
				t.Errorf("%s: card text %q: %v", command, s, err)
			}
		}
		return act
	}
	words, _ := splitCommand("metarush deliver notes show " + projectID + " --json")
	if call, _ := parseMetarush(words); call.Path != "deliver notes show" || !readOnlyCall(call) {
		t.Fatalf("reading notes should be Claude's own: %+v", call)
	}

	f := write("notes.md", "# Kitchen chat\n\n## Description\nLena on the garden.\n\n## Chapters\n0:00 Hello\n0:04 The garden\n\n## Show notes\nLinks we mention.\n")
	act := card("metarush deliver notes set " + projectID + " --from " + f)
	var in ops.DeliveryNotesSetIn
	_ = json.Unmarshal([]byte(act.Input), &in)
	all := strings.Join(act.Details, "|")
	if act.Op != "delivery.notes.set" || in.WrittenBy != models.WrittenByClaude || in.Revision != headID || len(in.Notes.Chapters) != 2 ||
		!strings.HasPrefix(act.Title, "Keep the delivery notes for version 1 of “Assistant film”: a title, a description, 2 chapters and show notes") ||
		!strings.Contains(all, "Title: “Kitchen chat”") || !strings.Contains(all, "0:04 The garden") || !strings.Contains(all, "at least three") {
		t.Fatalf("card %+v (%s)", act, act.Input)
	}
	c.data.Turns = []Turn{{}}
	if !c.autoStep(act) {
		t.Fatal("Auto should keep delivery notes by itself (nothing is spent, they can be changed again)")
	}

	// A file outside the proposals folder, or notes that can't be kept, make no card.
	outside := filepath.Join(t.TempDir(), "notes.md")
	_ = os.WriteFile(outside, []byte("# x\n"), 0o644)
	if bad := card("metarush deliver notes set " + projectID + " --from " + outside); bad.State != "" || !strings.Contains(bad.Unavailable, "proposals folder") {
		t.Fatalf("outside file: %+v", bad)
	}
	late := write("late.md", "## Chapters\n0:03 Late\n0:30 After the end\n")
	if bad := card("metarush deliver notes set " + projectID + " --from " + late); bad.State != "" || bad.Unavailable == "" {
		t.Fatalf("bad chapters: %+v", bad)
	}

	// The person keeps them; the version says so and plays what it played.
	if _, err := ops.Call(ctx, a, act.Op, json.RawMessage(act.Input), ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	var r models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), headID, &r)
	if r.DeliveryNotes == nil || r.DeliveryNotes.Title != "Kitchen chat" || r.DeliveryNotes.WrittenBy != "claude" {
		t.Fatalf("kept %+v", r.DeliveryNotes)
	}
	again := card("metarush deliver notes set " + projectID + " --title 'Kitchen chat, again'")
	if !strings.Contains(strings.Join(again.Details, "|"), "replace the notes") {
		t.Fatalf("replacing: %+v", again)
	}
	if off := card("metarush deliver notes set " + projectID + " --clear"); off.Op != "delivery.notes.set" || !strings.HasPrefix(off.Title, "Take the delivery notes off version 1") {
		t.Fatalf("clear: %+v", off)
	}
}

// Captions Claude translates in the panel (D238): reading the lines is
// Claude's own; keeping a translation is a card made from the file in the
// proposals folder, with examples and the glossary's names checked, marked
// as machine-translated (Auto keeps it by itself).
func TestTranslatedCaptionsCard(t *testing.T) {
	a := testApp(t, "")
	projectID, assetID, headID := fixture(t, a)
	ctx := context.Background()
	now := time.Now().UTC()
	toks := strings.Fields("Welcome to the garden at Elmwood today")
	var ws []models.Word
	for i, w := range toks {
		ws = append(ws, models.Word{Text: w, From: 1.2 + 0.5*float64(i), To: 1.6 + 0.5*float64(i), Score: 0.9})
	}
	seg := models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: assetID, From: 1.2, To: 4.6, Text: strings.Join(toks, " "),
		Speaker: "speaker_0", Words: ws, Provider: "test", Model: "truth", CreatedAt: now}
	if err := a.DB.Insert(ctx, store.CSegments, seg); err != nil {
		t.Fatal(err)
	}
	st := models.TranscriptStatus{State: "available", Provider: "test", Precision: "word", Language: "en", Segments: 1, UpdatedAt: now}
	if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), assetID, bson.M{"$set": bson.M{"transcript": st}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CallTyped[ops.SpellingAddOut](ctx, a, "transcript.spelling.add", ops.SpellingAddIn{ProjectID: projectID, Word: "Elmwood"}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	lines, err := ops.CallTyped[ops.CaptionsLinesOut](ctx, a, "captions.lines", ops.CaptionsLinesIn{ProjectID: projectID, Language: "es"}, ops.Caller{})
	if err != nil || len(lines.Lines) == 0 {
		t.Fatalf("lines %+v %v", lines, err)
	}
	m := &Manager{App: a, names: map[string]string{}}
	c := &conv{id: "cnv_translate"}
	dir := m.turnDir(c.id, 1)
	_ = os.MkdirAll(dir, 0o755)
	var tl []map[string]any
	for _, l := range lines.Lines {
		tl = append(tl, map[string]any{"n": l.N, "source": l.Source, "text": "ES " + strings.ReplaceAll(l.Source, "Elmwood", "Olmeda")})
	}
	b, _ := json.Marshal(map[string]any{"language": "es", "lines": tl})
	f := filepath.Join(dir, "spanish.json")
	_ = os.WriteFile(f, b, 0o644)

	for _, cmd := range []string{"metarush captions lines " + projectID + " --to es --missing --json", "metarush captions languages " + projectID + " --json"} {
		words, _ := splitCommand(cmd)
		if call, _ := parseMetarush(words); !readOnlyCall(call) {
			t.Fatalf("%s should be Claude's own", cmd)
		}
	}
	words, _ := splitCommand("metarush captions translate " + projectID + " --to es --from " + f)
	call, _ := parseMetarush(words)
	if readOnlyCall(call) {
		t.Fatal("keeping a translation should be a card")
	}
	act := &AssistantAction{Kind: call.Path, State: ActionPending}
	m.describeAction(ctx, c, &call, act)
	all := strings.Join(append([]string{act.Title}, act.Details...), "|")
	for _, s := range append([]string{act.Title}, act.Details...) {
		if err := plainwords.CheckPanel(s); err != nil {
			t.Errorf("card text %q: %v", s, err)
		}
	}
	var in ops.CaptionsTranslateIn
	_ = json.Unmarshal([]byte(act.Input), &in)
	if act.Op != "captions.translate" || in.Language != "es" || in.Human || len(in.Lines) != len(lines.Lines) ||
		!strings.Contains(act.Title, "Keep the Spanish captions for version 1") || !strings.Contains(act.Title, "machine-translated by Claude") ||
		!strings.Contains(all, "doesn't keep “Elmwood”") || !strings.Contains(all, "→") {
		t.Fatalf("card %+v", act)
	}
	if !c.autoStep(act) {
		t.Fatal("Auto should keep a translation by itself")
	}
	if _, err := ops.Call(ctx, a, act.Op, json.RawMessage(act.Input), ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	got, err := ops.CallTyped[ops.CaptionsLanguagesOut](ctx, a, "captions.languages", ops.CaptionsLanguagesIn{ProjectID: projectID, Revision: headID}, ops.Caller{})
	if err != nil || len(got.Languages) != 1 || !got.Languages[0].Machine || got.Languages[0].Missing != 0 {
		t.Fatalf("kept %+v %v", got, err)
	}
	rmw, _ := splitCommand("metarush captions remove-language " + projectID + " --to es")
	rmc, _ := parseMetarush(rmw)
	rm := &AssistantAction{Kind: rmc.Path, State: ActionPending}
	m.describeAction(ctx, c, &rmc, rm)
	if rm.Op != "captions.translation.remove" || !strings.HasPrefix(rm.Title, "Take the Spanish captions off") || c.autoStep(rm) {
		t.Fatalf("remove card %+v", rm)
	}
}

// Auto runs only what can be undone (D186): Claude's delivery notes and
// translations run by themselves, but not when they take notes off or write
// over what a person wrote — those ask, and say why.
func TestAutoLeavesPeoplesWordsAlone(t *testing.T) {
	a := testApp(t, "")
	on := true
	a.Cfg.Assistant.SafeSteps = &on
	projectID, assetID, headID := fixture(t, a)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := a.DB.Insert(ctx, store.CUsers, models.User{ID: a.Cfg.User.ID, WorkspaceID: a.WorkspaceID(), Name: "Tester", Email: "tester@example.com", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	toks := strings.Fields("Welcome to the garden at Elmwood today")
	var ws []models.Word
	for i, w := range toks {
		ws = append(ws, models.Word{Text: w, From: 1.2 + 0.5*float64(i), To: 1.6 + 0.5*float64(i), Score: 0.9})
	}
	if err := a.DB.Insert(ctx, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: assetID, From: 1.2, To: 4.6,
		Text: strings.Join(toks, " "), Speaker: "speaker_0", Words: ws, Provider: "test", Model: "truth", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	st := models.TranscriptStatus{State: "available", Provider: "test", Precision: "word", Language: "en", Segments: 1, UpdatedAt: now}
	if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), assetID, bson.M{"$set": bson.M{"transcript": st}}); err != nil {
		t.Fatal(err)
	}
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{text("Hi.")}}}})
	if _, err := ops.CallTyped[AssistantModeOut](ctx, a, "assistant.mode.set", AssistantModeSetIn{Mode: ModeAuto}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "hello", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m, out.Conversation.ID)
	c, _ := m.live(ctx, out.Conversation.ID, false)
	card := func(op string, in any) *AssistantAction {
		x := &AssistantAction{ID: shortID("act"), Turn: 1, Kind: op, State: ActionPending}
		setInput(x, op, in)
		return x
	}
	notes := ops.DeliveryNotesSetIn{ProjectID: projectID, Revision: headID, WrittenBy: models.WrittenByClaude, Notes: ops.DeliveryNotesDraft{Title: "Claude's title"}}
	if x := card("delivery.notes.set", notes); !m.startAuto(c, x) {
		t.Fatalf("Claude's notes over none run by themselves: %+v", x)
	}
	if x := card("delivery.notes.set", ops.DeliveryNotesSetIn{ProjectID: projectID, Revision: headID, Clear: true}); m.startAuto(c, x) || !strings.Contains(x.AutoWaits, "takes the version's delivery notes off") {
		t.Fatalf("taking notes off asks: %+v", x)
	}
	if _, err := ops.CallTyped[ops.DeliveryNotesOut](ctx, a, "delivery.notes.set", ops.DeliveryNotesSetIn{ProjectID: projectID, Notes: ops.DeliveryNotesDraft{Title: "My own title"}}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if x := card("delivery.notes.set", notes); m.startAuto(c, x) || !strings.Contains(x.AutoWaits, "a person wrote") {
		t.Fatalf("writing over a person's notes asks: %+v", x)
	}
	lines, err := ops.CallTyped[ops.CaptionsLinesOut](ctx, a, "captions.lines", ops.CaptionsLinesIn{ProjectID: projectID, Language: "es"}, ops.Caller{})
	if err != nil || len(lines.Lines) == 0 {
		t.Fatalf("lines %+v %v", lines, err)
	}
	machine := func(lang string) ops.CaptionsTranslateIn {
		in := ops.CaptionsTranslateIn{ProjectID: projectID, Revision: headID, Language: lang}
		for _, l := range lines.Lines {
			in.Lines = append(in.Lines, ops.TranslatedLine{N: l.N, Source: l.Source, Text: "MT " + l.Source})
		}
		return in
	}
	if x := card("captions.translate", machine("fr")); !m.startAuto(c, x) {
		t.Fatalf("a new language runs by itself: %+v", x)
	}
	human := machine("es")
	human.Human = true
	if _, err := ops.CallTyped[ops.CaptionsTranslateOut](ctx, a, "captions.translate", human, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if x := card("captions.translate", machine("es")); m.startAuto(c, x) || !strings.Contains(x.AutoWaits, "a person translated") {
		t.Fatalf("writing over a person's lines asks: %+v", x)
	}
}
