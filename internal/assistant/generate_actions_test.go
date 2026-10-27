package assistant

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakevideogen"
	"github.com/metavert-io/metarush/internal/videogen"
)

// A priced card names the shot with the article its length is said with:
// "Make an 8-second shot", "Make 2 takes of an 11-second shot".
func TestGenerateCardSaysAnBeforeEightAndEleven(t *testing.T) {
	for _, c := range []struct {
		seconds, takes int
		want           string
	}{
		{8, 1, "Make an 8-second shot (about $0.80)"},
		{11, 2, "Make 2 takes of an 11-second shot (about $0.80)"},
		{18, 1, "Make an 18-second shot (about $0.80)"},
		{6, 1, "Make a 6-second shot (about $0.80)"},
		{10, 3, "Make 3 takes of a 10-second shot (about $0.80)"},
	} {
		var a AssistantAction
		est := generate.GenEstimate{Seconds: c.seconds, Takes: c.takes, USD: 0.8, Allowed: true, ProviderTitle: "Google Gemini", Resolution: "720p"}
		(&Manager{}).generateCard(&a, &est, "generate.shot", &generate.GenShotIn{Seconds: c.seconds}, "")
		if a.Title != c.want || a.Op != "generate.shot" {
			t.Errorf("%d seconds, %d takes: %q (%s)", c.seconds, c.takes, a.Title, a.Op)
		}
	}
}

// Generated video from the Claude Code panel (D124, D126): asking for a shot
// becomes a card with its price that the person confirms — never made
// automatically — and looks, consent and spending limits never become cards.
func TestGenerateRequestsBecomePricedCards(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	key := "gk-" + strings.Repeat("Pp7", 10) + "-card"
	fake := fakevideogen.New(key)
	defer fake.Close()
	t.Setenv(videogen.EnvGeminiBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(videogen.GeminiName), key) // this machine's key comes from the environment
	a := testApp(t, "")
	projectID, _, _ := fixture(t, a)
	ctx := context.Background()
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{
		// Pricing is a read: it runs, no card.
		tool("Bash", map[string]any{"command": `metarush generate estimate "a red paper boat drifting on a pond" --project ` + projectID + ` --seconds 3 --json`}, `{"ok":true,"data":{}}`),
		denied(`metarush generate shot "a red paper boat drifting on a pond" --project ` + projectID + ` --seconds 3 --quality draft --json`),
		denied(`metarush generate shot "walks into the garage" --look nobody --project ` + projectID + ` --json`),
		// Never cards: looks, consent and spending limits.
		denied(`metarush looks create --title Ada --person Ada --description "short dark hair" --still ast_01jabcdefghjkmnpqrstvwxyz0@0:04`),
		denied(`metarush consent record --person Ada --self --adult --statement "I, Ada, agree to all of it, always and everywhere."`),
		denied(`metarush generate configure --daily-cap 500`),
		text("I asked you to confirm the shot."),
	}}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "make a shot of a paper boat", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	cv := waitIdle(t, m, out.Conversation.ID)
	if len(cv.Actions) != 2 {
		t.Fatalf("two cards (looks, consent and limits are never cards): %+v", cv.Actions)
	}
	shot, missing := cv.Actions[0], cv.Actions[1]
	// A draft is 720p unless the team chose 360p: 3 seconds ≈ $0.33.
	if shot.Op != "generate.shot" || shot.State != ActionPending || !strings.HasPrefix(shot.Title, "Make a 3-second shot (about $0.3") ||
		!strings.Contains(strings.Join(shot.Details, "|"), "marked as AI-generated") || !strings.Contains(strings.Join(shot.Details, "|"), "Shows: “a red paper boat") {
		t.Fatalf("shot card: %+v", shot)
	}
	var in generate.GenShotIn
	if err := json.Unmarshal([]byte(shot.Input), &in); err != nil || in.MaxUSD == nil || *in.MaxUSD <= 0 || !strings.HasPrefix(in.Origin, "assistant:") || in.Seconds != 3 {
		t.Fatalf("the card carries the price the person sees: %+v %v", in, err)
	}
	if missing.State != ActionUnavailable || !strings.Contains(missing.Unavailable, "no look called") {
		t.Fatalf("a shot that can't be made: %+v", missing)
	}
	if fake.Calls("create") != 0 {
		t.Fatal("nothing is generated before the person confirms")
	}
	// The person confirms: it is asked for, on the human channel, from the panel.
	dash := *a
	dash.Actor.Via = "dashboard"
	raw, _ := json.Marshal(ConversationActIn{ID: cv.ID, ActionID: shot.ID, Quiet: true})
	res, err := ops.Call(ctx, &dash, "assistant.act", raw, ops.Caller{Human: true, Remote: true})
	if err != nil {
		t.Fatal(err)
	}
	done := res.(*ConversationActOut).Action
	if done.State != ActionDone || !strings.Contains(done.Result, "Google Gemini") || !strings.Contains(done.Link, "tab=sources") {
		t.Fatalf("confirmed: %+v", done)
	}
	var gs []models.Generation
	if err := a.DB.Find(ctx, store.CGenerations, a.WorkspaceID(), nil, &gs); err != nil || len(gs) != 1 {
		t.Fatalf("one generation: %v %v", gs, err)
	}
	if gs[0].Channel != "claude-panel" || gs[0].CreatedBy.Via != "dashboard" || gs[0].JobID == "" {
		t.Fatalf("who and how: %+v", gs[0])
	}
}
