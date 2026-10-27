package assistant

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/keep"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/store"
)

func dismissAsPerson(t *testing.T, a *app.App, convID, actionID string) AssistantAction {
	t.Helper()
	dash := *a
	dash.Actor.Via = "dashboard"
	in, _ := json.Marshal(ConversationActIn{ID: convID, ActionID: actionID, Dismiss: true, Quiet: true})
	res, err := ops.Call(context.Background(), &dash, "assistant.act", in, ops.Caller{Human: true, Remote: true})
	if err != nil {
		t.Fatal(err)
	}
	return res.(*ConversationActOut).Action
}

// waitCards waits until the conversation has n cards (MetaRush's own cards
// are added when the turn ends).
func waitCards(t *testing.T, m *Manager, id string, n int) *Conversation {
	t.Helper()
	var cv *Conversation
	eventually(t, "the cards", func() bool {
		c, err := m.Get(context.Background(), id, true, true)
		if err != nil {
			t.Fatal(err)
		}
		cv = c
		return c.State == ConvIdle && len(c.Queue) == 0 && len(c.Actions) >= n
	})
	return cv
}

// "Keep this?" cards (D166): Claude's keep commands become cards that say
// what keeping does; the same thing is asked once; the links the person typed
// become cards by themselves (an email address or a file name doesn't);
// Keep runs the ordinary ops as the person, Not now declines for good, and
// the next message tells Claude what the project keeps.
func TestKeepCards(t *testing.T) {
	a := testApp(t, "")
	project, _, _ := fixture(t, a)
	ctx := context.Background()
	// The project's Content Settings are its own and the default (D192), like a workspace's first project.
	if _, err := store.MoveContentSettings(ctx, a.DB, a.WorkspaceID()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := a.DB.Insert(ctx, store.CPeople, models.Person{ID: ids.New(ids.Person), WorkspaceID: a.WorkspaceID(), Name: "Lena Okoye", NameKey: "lena okoye",
		CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	p := " --project " + project
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "two calls", Steps: []fakeclaude.Step{
			denied("metarush keep brief" + p + " --audience 'Members, neighbours and the public' --deliverable 'by email' --deliverable 'social shorts' --json"),
			denied("metarush keep rule 'First names only on screen'" + p + " --json"),
			denied("metarush keep check 'No split-second flashes of the wrong person at clip edges'" + p),
			denied("metarush keep person 'Lena' 'Volunteers at FernBrook; runs the garden club'"),
			denied("metarush keep signoff 'Lena OKs the final version'" + p),
			denied("metarush keep improve 'Steady shaky footage'"),
			denied("metarush keep spelling 'FernBrook'" + p + " --heard-as 'Fern Brook'"),
			// Asked once: the same rule again, and a person nobody knows, make no card.
			denied("metarush keep rule 'first names only, on screen.'" + p),
			denied("metarush keep person 'Nobody Known' 'Likes tea'"),
			text("I asked to keep those."),
		}},
		{Match: "Once more", Steps: []fakeclaude.Step{
			denied("metarush keep rule 'First names only on screen'" + p),
			denied("metarush keep check 'No split-second flashes of the wrong person at clip edges'" + p),
			denied("metarush keep rule 'Captions at the top'" + p),
			text("Done."),
		}},
		{Match: "And again", Steps: []fakeclaude.Step{text("OK.")}},
	}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{ProjectID: project,
		Prompt: "Cut the two calls into a short video for the open day. Their site is https://www.fernbrook.example/about; write to jo@fernbrook.example. My notes are in notes.md."}})
	if err != nil {
		t.Fatal(err)
	}
	cv := waitCards(t, m, out.Conversation.ID, 8)
	by, kinds := cardsOf(t, cv)
	want := []string{"keep brief", "keep rule", "keep check", "keep person", "keep signoff", "keep improve", "keep spelling", "keep"}
	if strings.Join(kinds, "|") != strings.Join(want, "|") {
		t.Fatalf("cards %v, want %v", kinds, want)
	}
	for _, act := range cv.Actions {
		if act.Op != "keep.add" || act.Keeping == nil || act.Keeping.ItemID == "" || act.State != ActionPending || act.Auto {
			t.Fatalf("a keep card: %+v", act)
		}
		for _, s := range append([]string{act.Title, act.Keeping.Does}, act.Details...) {
			if fs := plainwords.CheckShown(s); len(fs) > 0 {
				t.Errorf("card %q: %q is not plain: %v", act.Kind, s, fs)
			}
		}
	}
	brief := by["keep brief"][0]
	if brief.Title != "Keep this in the project's brief?" || !strings.Contains(brief.Keeping.Does, "social shorts (vertical)") {
		t.Fatalf("brief card: %+v %+v", brief, brief.Keeping)
	}
	if r := by["keep rule"][0]; r.Title != "Keep this rule for the project?" || r.Details[0] != "“First names only on screen”" ||
		!strings.Contains(r.Keeping.Does, "names in name bars and lower thirds are first names only") && !strings.Contains(r.Keeping.Does, "The quality check checks that") {
		t.Fatalf("rule card: %+v %+v", r, r.Keeping)
	}
	if c := by["keep check"][0]; c.Title != "Check this in every quality check of the project?" {
		t.Fatalf("check card: %+v", c)
	}
	// A rule asks where it goes (D193): just this project, or every project using its settings.
	if w := by["keep rule"][0].Keeping.Where; len(w) != 2 || w[0].Value != "project" || w[0].Label != "Just this project" || w[1].Value != "settings" ||
		w[1].Label != "Every project using this project's settings" {
		t.Fatalf("where a rule goes: %+v", w)
	}
	if pc := by["keep person"][0]; pc.Title != "Add this to Lena Okoye's notes?" {
		t.Fatalf("person card: %+v", pc)
	}
	if s := by["keep spelling"][0]; s.Title != "Put “FernBrook” on this project's spelling list?" {
		t.Fatalf("spelling card: %+v", s)
	}
	link := by["keep"][0]
	if link.Title != "Add fernbrook.example/about to this project's sources?" || link.Keeping.From != "metarush" || link.Keeping.Kind != models.KeepSource {
		t.Fatalf("the link the person typed: %+v %+v", link, link.Keeping)
	}
	// Nothing was kept by making cards.
	pr := keptProject(t, a, project)
	if len(pr.Rules) != 0 || len(pr.Signoffs) != 0 || len(pr.Brief.Deliverables) != 0 {
		t.Fatalf("making cards changed the project: %+v", pr)
	}

	// Keep the rule and the brief; not now for the check.
	if done := actAsPerson(t, a, cv.ID, by["keep rule"][0].ID); done.State != ActionDone || !strings.Contains(done.Result, "Added the rule “First names only on screen”") {
		t.Fatalf("kept the rule: %+v", done)
	}
	if done := actAsPerson(t, a, cv.ID, brief.ID); done.State != ActionDone {
		t.Fatalf("kept the brief: %+v", done)
	}
	if done := actAsPerson(t, a, cv.ID, by["keep signoff"][0].ID); done.State != ActionDone {
		t.Fatalf("kept the sign-off: %+v", done)
	}
	if d := dismissAsPerson(t, a, cv.ID, by["keep check"][0].ID); d.State != ActionDismissed {
		t.Fatalf("not now: %+v", d)
	}
	it, err := keep.Get(ctx, a.DB, a.WorkspaceID(), by["keep check"][0].Keeping.ItemID)
	if err != nil || it.State != models.KeepDeclined {
		t.Fatalf("declined for good: %+v %v", it, err)
	}
	pr = keptProject(t, a, project)
	if len(pr.Rules) != 1 || pr.Rules[0].Measure != "first-names" || len(pr.Brief.Deliverables) != 2 || pr.Brief.Deliverables[1].Preset != "vertical-1080p" {
		t.Fatalf("kept: %+v %+v", pr.Rules, pr.Brief)
	}

	// The next message: kept and declined things are not asked again, and
	// Claude hears what the project keeps.
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: cv.ID, AssistantMessageIn: AssistantMessageIn{Prompt: "Once more", ProjectID: project}}); err != nil {
		t.Fatal(err)
	}
	cv = waitCards(t, m, cv.ID, 9)
	cv = waitIdle(t, m, cv.ID)
	var second []string
	for _, act := range cv.Actions {
		if act.Turn == 2 {
			second = append(second, act.Kind+": "+act.Title)
		}
	}
	if len(second) != 1 || second[0] != "keep rule: Keep this rule for the project?" {
		t.Fatalf("second turn's cards: %v", second)
	}
	// Kept for every project using its settings: a house rule, not the project's own.
	for _, act := range cv.Actions {
		if act.Turn == 2 {
			done := actWhere(t, a, cv.ID, act.ID, "settings")
			if done.State != ActionDone || !strings.Contains(done.Result, "house rule “Captions at the top”") {
				t.Fatalf("kept as a house rule: %+v", done)
			}
		}
	}
	if pr := keptProject(t, a, project); len(pr.Rules) != 1 {
		t.Fatalf("the project's own rules didn't change: %+v", pr.Rules)
	}
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: cv.ID, AssistantMessageIn: AssistantMessageIn{Prompt: "And again", ProjectID: project}}); err != nil {
		t.Fatal(err)
	}
	cv = waitIdle(t, m, cv.ID)
	if third := cv.Turns[2].Sent; !strings.Contains(third, "House rules from “") || !strings.Contains(third, "“Captions at the top”") {
		t.Errorf("the house rule is in Claude's context, labelled:\n%s", third)
	}
	sent := cv.Turns[1].Sent
	for _, w := range []string{"This project's rules (follow them in everything you propose): “First names only on screen”",
		"where it goes: by email (landscape-1080p), social shorts (vertical-1080p)", "Sign-offs still waiting", "Already offered to keep in this project",
		"the person said not now: check “No split-second flashes"} {
		if !strings.Contains(sent, w) {
			t.Errorf("the message should say %q:\n%s", w, sent)
		}
	}
	// keep.list shows them.
	list, err := ops.CallTyped[ops.KeepListOut](ctx, a, "keep.list", ops.KeepListIn{ProjectID: project}, ops.Caller{})
	if err != nil || len(list.Items) < 6 {
		t.Fatalf("keep list: %+v %v", list, err)
	}
}

// actWhere keeps a "Keep this rule?" card where the person chose (D193).
func actWhere(t *testing.T, a *app.App, convID, actionID, where string) AssistantAction {
	t.Helper()
	dash := *a
	dash.Actor.Via = "dashboard"
	in, _ := json.Marshal(ConversationActIn{ID: convID, ActionID: actionID, Quiet: true, KeepWhere: where})
	res, err := ops.Call(context.Background(), &dash, "assistant.act", in, ops.Caller{Human: true, Remote: true})
	if err != nil {
		t.Fatal(err)
	}
	return res.(*ConversationActOut).Action
}

// personLinks finds what a person typed as a web address, and nothing else.
func TestPersonLinks(t *testing.T) {
	got := personLinks("See fernbrook.example.org and https://www.youtube.com/watch?v=abc123, or www.example.org/team. " +
		"Mail jo@fernbrook.example; the files are notes.md, clip.mov and v1.2; http://192.168.1.4/page and localhost:3000 are mine.")
	want := []string{"fernbrook.example.org", "https://www.youtube.com/watch?v=abc123", "www.example.org/team"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("links %q, want %q", got, want)
	}
}

func keptProject(t *testing.T, a *app.App, id string) models.Project {
	t.Helper()
	var p models.Project
	if err := a.DB.GetByID(context.Background(), store.CProjects, a.WorkspaceID(), id, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			c := color.RGBA{R: 0x1d, G: 0x4e, B: 0x89, A: 0xff}
			if x > w/3 && x < 2*w/3 {
				c = color.RGBA{R: 0xf2, G: 0xa9, B: 0x00, A: 0xff}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A picture the person attaches (D166): stored by its content, copied where
// Claude can Read it and named in the message, shown with the message, and
// offered as a "Keep this?" card (a logo for the brand kit too).
func TestAttachments(t *testing.T) {
	a := testApp(t, "")
	project, _, _ := fixture(t, a)
	ctx := AsPerson(context.Background(), a)
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "our logo", Steps: []fakeclaude.Step{text("A blue and yellow logo.")}},
	}}})
	if _, err := m.Attach(ctx, AttachIn{Name: "notes.txt", Data: base64.StdEncoding.EncodeToString([]byte("plain words"))}); code(err) != "E_INVALID_INPUT" {
		t.Fatalf("a text file isn't a picture: %v", err)
	}
	att, err := m.Attach(ctx, AttachIn{Name: "fernbrook-logo.png", Data: base64.StdEncoding.EncodeToString(pngBytes(t, 240, 120))})
	if err != nil {
		t.Fatal(err)
	}
	if att.File.MIME != "image/png" || att.File.Width != 240 || att.File.Height != 120 || len(att.File.SHA256) != 64 {
		t.Fatalf("attached: %+v", att.File)
	}
	bad := att.File
	bad.SHA256 = strings.Repeat("0", 64)
	if _, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "x", ProjectID: project, Attachments: []models.KeepFile{bad}}}); code(err) != "E_INVALID_INPUT" {
		t.Fatalf("a file that was never attached: %v", err)
	}
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "This is our logo", ProjectID: project, Attachments: []models.KeepFile{att.File}}})
	if err != nil {
		t.Fatal(err)
	}
	cv := waitCards(t, m, out.Conversation.ID, 1)
	sent := cv.Turns[0].Sent
	path := m.attachmentPath(cv.ID, att.File)
	if !strings.Contains(sent, "The person attached \"fernbrook-logo.png\" (a picture, 240 × 120") || !strings.Contains(sent, realPath(path)) {
		t.Fatalf("the message should name the file and where to Read it:\n%s", sent)
	}
	if b, err := os.ReadFile(path); err != nil || len(b) != int(att.File.Size) {
		t.Fatalf("the copy Claude reads: %v", err)
	}
	if len(cv.Turns[0].Attachments) != 1 {
		t.Fatalf("the turn keeps its files: %+v", cv.Turns[0])
	}
	card := cv.Actions[0]
	if card.Title != "Keep this logo in the sources and the brand kit?" || card.Keeping == nil || card.Image != AttachmentURL(cv.ID, att.File.SHA256) {
		t.Fatalf("the logo card: %+v", card)
	}
	f, info, err := m.OpenAttachment(context.Background(), cv.ID, att.File.SHA256)
	if err != nil || info.MIME != "image/png" {
		t.Fatalf("open: %v %+v", err, info)
	}
	f.Close()
	if _, _, err := m.OpenAttachment(context.Background(), cv.ID, strings.Repeat("ab", 32)); code(err) != "E_NOT_FOUND" {
		t.Fatalf("only the conversation's files: %v", err)
	}
}
