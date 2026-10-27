package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/store"
)

// Whose Claude answers (D239): read from Claude Code's own sign-in, with the
// terms that apply to what Claude reads; nothing assumed when it doesn't say.
func TestWhoseClaudeAnswers(t *testing.T) {
	cases := []struct {
		raw              map[string]any
		method           string
		plan, terms, who string
	}{
		{map[string]any{"email": "host@example.com", "subscriptionType": "max"}, "claude.ai", "max", TermsConsumer, "Claude Code on Studio, signed in as host@example.com (Claude Max)"},
		{map[string]any{"account": map[string]any{"emailAddress": "team@example.com"}, "subscriptionType": "team"}, "claude.ai", "team", TermsCommercial, "Claude Code on Studio, signed in as team@example.com (Claude Team)"},
		{map[string]any{"subscriptionType": "enterprise", "orgName": "A Studio"}, "oauth_token", "enterprise", TermsCommercial, "Claude Code on Studio (Claude Enterprise)"},
		{nil, "claude.ai", "plan", TermsConsumer, "Claude Code on Studio (a Claude plan)"},
		{map[string]any{}, "api_key", "api", TermsCommercial, "Claude Code on Studio (an Anthropic API key)"},
		{map[string]any{"apiProvider": "bedrock"}, "third_party", "bedrock", TermsCloud, "Claude Code on Studio (Amazon Bedrock)"},
		{map[string]any{}, "", "unknown", TermsUnknown, "Claude Code on Studio (a sign-in Claude Code didn't name)"},
	}
	for _, c := range cases {
		a := accountOf(c.raw, c.method, "Studio")
		if a.Plan != c.plan || a.Terms != c.terms || a.Who != c.who || a.DataUse == "" || a.TermsName == "" {
			t.Errorf("%v %s: %+v", c.raw, c.method, a)
		}
		for _, s := range []string{a.Who, a.DataUse, a.TermsName} {
			if err := plainwords.CheckShown(s); err != nil {
				t.Errorf("%q: %v", s, err)
			}
		}
	}

	// The status and every conversation say it.
	a := testApp(t, "")
	projectID, _, _ := fixture(t, a)
	m := withFake(t, a, fakeclaude.Options{Email: "host@example.com", Subscription: "pro", Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{text("Hello.")}}}})
	ctx := context.Background()
	st := m.Status(ctx, true)
	if st.Account == nil || st.Account.Email != "host@example.com" || st.Account.Plan != "pro" || st.Account.Terms != TermsConsumer || st.Account.Machine != st.Machine {
		t.Fatalf("status account %+v", st.Account)
	}
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "Hello there", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	cv := waitIdle(t, m, out.Conversation.ID)
	if cv.Account == nil || !strings.Contains(cv.Account.Who, "host@example.com (Claude Pro)") || cv.Account.At.IsZero() {
		t.Fatalf("conversation account %+v", cv.Account)
	}
	// Kept with the conversation (a list shows it after a restart).
	m.save(m.convs[out.Conversation.ID], true)
	var got Conversation
	if err := a.DB.GetByID(ctx, store.CAssistantConversations, a.WorkspaceID(), out.Conversation.ID, &got); err != nil || got.Account == nil || got.Account.Terms != TermsConsumer {
		t.Fatalf("stored %+v %v", got.Account, err)
	}
}
