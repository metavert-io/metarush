package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/store"
)

// Transcript changes Claude asks for become cards (D145–D149): a change
// wherever it is said (how many places in how many videos, and the spelling
// list), a fix of the word said at a moment, who says a stretch, the spelling
// list, an undo, and one new version with every text fix and a review copy.
// Building them changes nothing; finding and the waiting fixes are reads; a
// captions file isn't written from the panel. Confirmed, each runs as the person.
func TestTranscriptChangesBecomeCards(t *testing.T) {
	a := testApp(t, "")
	f := callFixture(t, a)
	ctx := context.Background()
	person := ops.Caller{Human: true}
	// A fix a person made earlier waits for a new version.
	earlier, err := ops.CallTyped[ops.TranscriptFixOut](ctx, a, "transcript.fix", ops.TranscriptFixIn{AssetID: f.maya, From: 5.4, To: 5.787, Text: "Great!", ProjectID: f.project}, person)
	if err != nil || len(earlier.Fixes) != 1 {
		t.Fatalf("the earlier fix: %v %+v", err, earlier)
	}
	revsBefore := revisionCount(t, a, f.project)
	p := " --project " + f.project
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "Tidy the transcript", Steps: []fakeclaude.Step{
			tool("Bash", map[string]any{"command": "metarush transcript find 'forty centimeters'" + p + " --json"}, `{"ok":true,"data":{}}`),
			tool("Bash", map[string]any{"command": "metarush transcript pending list " + f.project + " --json"}, `{"ok":true,"data":{}}`),
			denied("metarush transcript replace 'forty centimeters' '40 cm'" + p + " --json"),
			denied("metarush transcript replace 'Sam' 'Samuel'" + p + " --spelling --json"),
			denied("metarush transcript fix " + f.sam + " --at 31.6 'Cherry'" + p),
			denied("metarush transcript speaker " + f.maya + " --from 0.4 --to 2.2 --new-person 'Jo'" + p),
			denied("metarush transcript spelling add Maya" + p + " --fix-existing"),
			denied("metarush transcript undo " + earlier.Fixes[0].ID),
			denied("metarush transcript pending apply " + f.project + " --json"),
			denied("metarush captions file " + f.project + " --out /tmp/version.srt"),
			// Not cards: nothing to change.
			denied("metarush transcript replace 'zucchini' 'courgette'" + p),
			denied("metarush transcript fix " + f.sam + " --at 90 'Hello'"),
			text("I asked you to confirm those."),
		}},
	}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "Tidy the transcript", ProjectID: f.project}})
	if err != nil {
		t.Fatal(err)
	}
	cv := waitIdle(t, m, out.Conversation.ID)
	by, kinds := cardsOf(t, cv)
	want := []string{"transcript replace", "transcript replace", "transcript fix", "transcript speaker", "transcript spelling add", "transcript undo",
		"transcript pending apply", "captions file"}
	if strings.Join(kinds, "|") != strings.Join(want, "|") {
		t.Fatalf("cards %v, want %v", kinds, want)
	}
	for _, act := range cv.Actions {
		for _, s := range append([]string{act.Title, act.Unavailable}, act.Details...) {
			if fs := plainwords.CheckShown(s); len(fs) > 0 {
				t.Errorf("card %q: %q is not plain: %v", act.Kind, s, fs)
			}
		}
	}
	cm, sam := by["transcript replace"][0], by["transcript replace"][1]
	if cm.Op != "transcript.replace" || cm.Title != "Change “forty centimeters” to “40 cm” in 1 place in 1 video" ||
		!strings.Contains(strings.Join(cm.Details, "|"), "“forty centimeters” deep") || !strings.Contains(cm.Input, `"only":[`) {
		t.Fatalf("change everywhere it is said: %+v", cm)
	}
	if sam.Title != "Change “Sam” to “Samuel” in 1 place in 1 video, and add it to the spelling list" || !strings.Contains(sam.Input, `"spelling":true`) {
		t.Fatalf("with the spelling list: %+v", sam)
	}
	if fx := by["transcript fix"][0]; fx.Op != "transcript.fix" || !strings.HasPrefix(fx.Title, "Fix “cherry” → “Cherry” in “") || !strings.Contains(fx.Input, `"expect":"cherry"`) {
		t.Fatalf("the word said at a moment: %+v", fx)
	}
	if sp := by["transcript speaker"][0]; sp.Op != "transcript.speaker" || !strings.HasPrefix(sp.Title, "0:00.4–0:02.2 of “") || !strings.HasSuffix(sp.Title, "” is “Jo”") ||
		!strings.Contains(sp.Input, `"newPerson":true`) {
		t.Fatalf("who says a stretch: %+v", sp)
	}
	if sl := by["transcript spelling add"][0]; sl.Title != "Add “Maya” to the spelling list of “Garden show”'s settings" || !strings.Contains(strings.Join(sl.Details, "|"), "It also corrects the transcripts") {
		t.Fatalf("the spelling list: %+v", sl)
	}
	undo := by["transcript undo"][0]
	if undo.Op != "transcript.undo" || !strings.HasPrefix(undo.Title, "Undo “Great.” → “Great!” in “") {
		t.Fatalf("undo: %+v", undo)
	}
	nv := by["transcript pending apply"][0]
	if nv.Op != "transcript.pending.apply" || nv.Title != "Make version 3 of “Garden show” with 1 text fix, and a review copy" ||
		!strings.Contains(strings.Join(nv.Details, "|"), "“Great.” → “Great!”") || !strings.Contains(strings.Join(nv.Details, "|"), "version 2 keeps its words") {
		t.Fatalf("one new version: %+v", nv)
	}
	if file := by["captions file"][0]; file.State != ActionUnavailable || !strings.Contains(file.Unavailable, "Download the captions file from the version in the dashboard") {
		t.Fatalf("a captions file is saved elsewhere: %+v", file)
	}
	// Nothing changed yet.
	if said := saidIn(t, a, f.sam); !strings.Contains(said, "forty centimeters") || !strings.Contains(said, "That cherry") {
		t.Fatalf("building cards changed a transcript: %s", said)
	}
	if n := revisionCount(t, a, f.project); n != revsBefore {
		t.Fatalf("building cards saved a version: %d → %d", revsBefore, n)
	}
	if n, _ := a.DB.C(store.CSpelling).CountDocuments(ctx, map[string]any{"workspaceId": a.WorkspaceID()}); n != 0 {
		t.Fatalf("building cards added %d words to a spelling list", n)
	}

	// A person confirms: the change, the new version (with a review copy), then the undo.
	if done := actAsPerson(t, a, cv.ID, cm.ID); done.State != ActionDone || !strings.Contains(done.Result, "Changed “forty centimeters” to “40 cm” in 1 place") {
		t.Fatalf("the change ran: %+v", done)
	}
	if said := saidIn(t, a, f.sam); !strings.Contains(said, "about 40 cm deep") {
		t.Fatalf("the transcript says: %s", said)
	}
	done := actAsPerson(t, a, cv.ID, nv.ID)
	if done.State != ActionDone || !strings.HasPrefix(done.Result, "Saved version 3 of “Garden show” with 2 text fixes. A review copy is on its way") {
		t.Fatalf("the new version ran: %+v", done)
	}
	if n := revisionCount(t, a, f.project); n != revsBefore+1 {
		t.Fatalf("one new version: %d → %d", revsBefore, n)
	}
	var renders []models.Render
	if err := a.DB.Find(ctx, store.CRenders, a.WorkspaceID(), map[string]any{"projectId": f.project}, &renders); err != nil || len(renders) != 1 || renders[0].Quality != "review" || renders[0].RevisionNumber != 3 {
		t.Fatalf("one review copy of version 3: %v %+v", err, renders)
	}
	if done := actAsPerson(t, a, cv.ID, undo.ID); done.State != ActionDone || !strings.Contains(done.Result, "Undone: it says “Great.” again") {
		t.Fatalf("the undo ran: %+v", done)
	}
}

func revisionCount(t *testing.T, a *app.App, projectID string) int64 {
	t.Helper()
	n, err := a.DB.C(store.CRevisions).CountDocuments(context.Background(), map[string]any{"workspaceId": a.WorkspaceID(), "projectId": projectID})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func saidIn(t *testing.T, a *app.App, assetID string) string {
	t.Helper()
	tx, err := ops.CallTyped[ops.TranscriptOut](context.Background(), a, "footage.transcript", ops.TranscriptIn{AssetID: assetID}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, s := range tx.Segments {
		b.WriteString(s.Text + " ")
	}
	return b.String()
}
