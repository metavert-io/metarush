package assistant

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/prefs"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeai"
	"github.com/metavert-io/metarush/internal/testutil/fakevideogen"
	"github.com/metavert-io/metarush/internal/videogen"
)

// AI services from the Claude Code panel (D224–D227): pricing and reading run;
// asking for a picture becomes a card with its price, the model and the
// alternatives that the person confirms; a voice copy and which services a
// client's work may use never become cards.
func TestAIRequestsBecomePricedCards(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	key := "gk-" + strings.Repeat("Aq4", 10) + "-card"
	fake := fakevideogen.New(key)
	defer fake.Close()
	t.Setenv(videogen.EnvGeminiBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(videogen.GeminiName), key)
	a := testApp(t, "")
	projectID, _, _ := fixture(t, a)
	ctx := context.Background()
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{
		tool("Bash", map[string]any{"command": `metarush ai estimate picture "a watercolour kite" --project ` + projectID + ` --json`}, `{"ok":true,"data":{}}`),
		tool("Bash", map[string]any{"command": `metarush ai models --kind music --json`}, `{"ok":true,"data":{}}`),
		denied(`metarush ai make picture "a watercolour kite over a hill" --project ` + projectID + ` --size 2K --aspect 16:9 --json`),
		denied(`metarush ai voices copy --person Ada --name "Ada narration" --sample ast_01jabcdefghjkmnpqrstvwxyz0`),
		denied(`metarush ai rules set --project ` + projectID + ` --none`),
		text("I asked you to confirm the picture."),
	}}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "make a picture of a kite", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	cv := waitIdle(t, m, out.Conversation.ID)
	if len(cv.Actions) != 1 {
		t.Fatalf("one card (voice copies and Content Settings are never cards): %+v", cv.Actions)
	}
	card := cv.Actions[0]
	details := strings.Join(card.Details, "|")
	if card.Op != "ai.make" || card.State != ActionPending || !strings.HasPrefix(card.Title, "Make a picture (about $0.05") || card.Estimate == nil ||
		!strings.Contains(details, "Made with Google Gemini (Nano Banana 2.1), MetaRush's usual model for this") || !strings.Contains(details, "resources library") {
		t.Fatalf("card: %+v", card)
	}
	var in aiwork.MakeIn
	if err := json.Unmarshal([]byte(card.Input), &in); err != nil || in.MaxUSD == nil || in.Options.Size != "2K" || in.Kind != "picture" || !strings.HasPrefix(in.Origin, "assistant:") {
		t.Fatalf("input %s %v", card.Input, err)
	}
	if fake.Calls("generate") != 0 {
		t.Fatal("nothing may reach Google before the person confirms")
	}
	if l := aiLabel(&cliCall{Path: "ai estimate"}); l == "" || !readOnlyCall(cliCall{Path: "ai voices list"}) || readOnlyCall(cliCall{Path: "ai voices copy"}) {
		t.Errorf("labels and read-only: %q", l)
	}
}

// Auto runs a priced AI services card by itself only where a person set both
// the project's budget and the team's daily limit (D241) — generated video's,
// which every kind of AI work shares whichever service does it (D226) — and
// pays from the conversation's project, which the card names.
func TestAutoRunsAICardsWithinVideosLimits(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	key := "gk-" + strings.Repeat("Aq4", 10) + "-auto"
	fake := fakevideogen.New(key)
	defer fake.Close()
	t.Setenv(videogen.EnvGeminiBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(videogen.GeminiName), key)
	a := testApp(t, "")
	on := true
	a.Cfg.Assistant.SafeSteps = &on // Auto's master switch
	projectID, _, _ := fixture(t, a)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := a.DB.Insert(ctx, store.CUsers, models.User{ID: a.Cfg.User.ID, WorkspaceID: a.WorkspaceID(), Name: "Tester", Email: "tester@example.com", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if n, _ := a.DB.C(store.CWorkspaces).CountDocuments(ctx, map[string]any{"_id": a.WorkspaceID()}); n == 0 {
		if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: a.WorkspaceID(), WorkspaceID: a.WorkspaceID(), Name: "T", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{
		denied(`metarush ai make picture "a watercolour kite over a hill" --project ` + projectID + ` --json`),
		text("I asked you to confirm the picture."),
	}}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "make a picture of a kite", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	cv := waitIdle(t, m, out.Conversation.ID)
	if len(cv.Actions) != 1 || cv.Actions[0].Op != "ai.make" {
		t.Fatalf("cards %+v", cv.Actions)
	}
	card := cv.Actions[0]
	if card.SpendsIn != projectID {
		t.Fatalf("the card doesn't say which project pays: %q", card.SpendsIn)
	}
	for _, p := range card.Estimate.Parts {
		if p.LimitsKey() != generate.AIServicesLimits {
			t.Fatalf("a part not covered by AI services' limits: %+v", p)
		}
	}
	c, _ := m.live(ctx, out.Conversation.ID, false)
	off := false
	if _, err := ops.CallTyped[prefs.PreferencesOut](ctx, a, "account.preferences.set", prefs.PreferencesSetIn{Estimates: &off}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CallTyped[AssistantModeOut](ctx, a, "assistant.mode.set", AssistantModeSetIn{Mode: ModeAuto}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	fresh := func(service string) *AssistantAction {
		x := card
		x.ID, x.State, x.AutoWaits = shortID("act"), ActionPending, ""
		est := *card.Estimate
		est.Parts = append([]costs.Part(nil), card.Estimate.Parts...)
		for i := range est.Parts {
			est.Parts[i].Service = service
		}
		x.Estimate = &est
		return &x
	}
	// The usual budget and limit don't count.
	if x := fresh("Google Gemini"); m.startAuto(c, x) || !strings.Contains(x.AutoWaits, "nobody set this project's budget for AI services") {
		t.Fatalf("no budget set: %+v", x)
	}
	budget, cap := 20.0, 5.0
	if _, err := ops.CallTyped[generate.GenSettingsOut](ctx, a, "generate.configure", generate.GenConfigureIn{ProjectID: projectID, BudgetUSD: &budget}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if x := fresh("Google Gemini"); m.startAuto(c, x) || !strings.Contains(x.AutoWaits, "nobody set the team's daily limit for AI services") {
		t.Fatalf("no daily limit set: %+v", x)
	}
	if _, err := ops.CallTyped[generate.GenSettingsOut](ctx, a, "generate.configure", generate.GenConfigureIn{DailyCapUSD: &cap}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	// Both set: it runs by itself, whichever service does the work — AI work
	// with ElevenLabs is paid from the same budget and limit, not the AI
	// voice's credits (which have no daily limit, so its cards ask).
	for _, service := range []string{"Google Gemini", "ElevenLabs"} {
		if x := fresh(service); !m.startAuto(c, x) || !x.AutoPriced || x.AutoWaits != "" {
			t.Fatalf("%s: limits set, it runs by itself: %+v", service, x)
		}
	}
	tts := fresh("ElevenLabs")
	tts.Op = "voiceover.tts"
	for i := range tts.Estimate.Parts {
		tts.Estimate.Parts[i].Limits = ""
	}
	if m.startAuto(c, tts) || tts.AutoPriced {
		t.Fatalf("the AI voice still asks: %+v", tts)
	}
	// Paid from another project than the conversation's: a person decides.
	x := fresh("Google Gemini")
	x.SpendsIn = ""
	if m.startAuto(c, x) || !strings.Contains(x.AutoWaits, "which project's budget") {
		t.Fatalf("no paying project: %+v", x)
	}
}

// A presenter clip Claude asks for is a priced card paid from the project,
// within AI services' limits (D241); every step of a person's own presenter
// is a person's and never a card (D273–D277).
func TestPresenterClipsBecomePricedCards(t *testing.T) {
	key := "fk-" + strings.Repeat("Pz3", 10) + "-card"
	fakeai.Install(t, key)
	t.Setenv(providers.EnvVar(fakeai.Name), key)
	a := testApp(t, "")
	projectID, assetID, _ := fixture(t, a)
	ctx := context.Background()
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{
		tool("Bash", map[string]any{"command": `metarush presenter list --json`}, `{"ok":true,"data":{}}`),
		denied(`metarush ai make presenter --presenter stock:fake-stock-amber --model fake/fake-presenter --clip ` + assetID + ` --from 1 --to 6 --project ` + projectID + ` --json`),
		denied(`metarush presenter make ada --take ` + assetID + ` --reading ` + assetID),
		denied(`metarush presenter retire ada`),
		denied(`metarush presenter opt-out fake --how "wrote to them"`),
		text("I asked you to confirm the presenter clip."),
	}}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "have a presenter read the intro", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	cv := waitIdle(t, m, out.Conversation.ID)
	if len(cv.Actions) != 1 {
		t.Fatalf("one card (a person's presenter is never a card): %+v", cv.Actions)
	}
	card := cv.Actions[0]
	details := strings.Join(card.Details, "|")
	if card.Op != "ai.make" || card.SpendsIn != projectID || card.Estimate == nil || !strings.HasPrefix(card.Title, "Make a presenter clip") ||
		!strings.Contains(details, "marked as an AI presenter") || !strings.Contains(details, "doesn't cover ads") {
		t.Fatalf("card: %+v", card)
	}
	for _, p := range card.Estimate.Parts {
		if p.LimitsKey() != generate.AIServicesLimits {
			t.Fatalf("a part not covered by AI services' limits: %+v", p)
		}
	}
	var in aiwork.MakeIn
	if err := json.Unmarshal([]byte(card.Input), &in); err != nil || in.Kind != "presenter" || in.Presenter != "stock:fake-stock-amber" || in.MaxUSD == nil {
		t.Fatalf("input %s %v", card.Input, err)
	}
	if !readOnlyCall(cliCall{Path: "presenter list"}) || readOnlyCall(cliCall{Path: "presenter opt-out", Flags: map[string]string{"how": "x"}}) || readOnlyCall(cliCall{Path: "presenter make"}) {
		t.Error("read-only presenter commands")
	}
}
