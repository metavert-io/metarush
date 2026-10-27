package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/prefs"
	"github.com/metavert-io/metarush/internal/store"
)

// Auto runs a card that spends money on an outside AI service by itself only
// for the person whose estimates are off (D208), within their budgets, and
// never while an owner asks everyone for estimates; such a card says why it
// ran. Ask first always waits; a card without its estimate, people-only
// decisions and setup always ask.
func TestAutoRunsPricedCardsOnlyWithEstimatesOff(t *testing.T) {
	a := testApp(t, "")
	on, off := true, false
	a.Cfg.Assistant.SafeSteps = &on
	projectID, _, head := fixture(t, a)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := a.DB.Insert(ctx, store.CUsers, models.User{ID: a.Cfg.User.ID, WorkspaceID: a.WorkspaceID(), Name: "Tester", Email: "tester@example.com", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	ws := a.WorkspaceID()
	if n, _ := a.DB.C(store.CWorkspaces).CountDocuments(ctx, map[string]any{"_id": ws}); n == 0 {
		if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: ws, WorkspaceID: ws, Name: "T", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{{Match: "hello", Steps: []fakeclaude.Step{text("Hi.")}}}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "hello", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	eventually(t, "the reply", func() bool {
		cv, _ := m.Get(ctx, id, false, false)
		return cv != nil && cv.State == ConvIdle
	})
	c, _ := m.live(ctx, id, false)
	priced := func(op, input string) *AssistantAction {
		service := "ElevenLabs"
		if strings.HasPrefix(op, "generate.") || op == "looks.test" {
			service = "Google Gemini"
		}
		est := costs.Of(costs.Part{Service: service, What: "the AI voice", Amount: 1500, Unit: "credits", USD: 0.12})
		return &AssistantAction{ID: shortID("act"), Turn: 1, Op: op, Input: input, State: ActionPending, Estimate: &est, SpendsIn: projectID}
	}
	cloud := `{"revisionId":"` + head + `","target":"Sam","mode":"cloud"}`
	prefsSet := func(in prefs.PreferencesSetIn) {
		t.Helper()
		if _, err := ops.CallTyped[prefs.PreferencesOut](ctx, a, "account.preferences.set", in, ops.Caller{Human: true}); err != nil {
			t.Fatal(err)
		}
	}

	// Ask first: nothing priced runs by itself.
	if x := priced("voiceover.tts", `{}`); m.startAuto(c, x) || x.State != ActionPending {
		t.Fatal("Ask first: a priced card waits")
	}
	// Auto with estimates on (the default): still asks, as before.
	if _, err := ops.CallTyped[AssistantModeOut](ctx, a, "assistant.mode.set", AssistantModeSetIn{Mode: ModeAuto}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if x := priced("voiceover.tts", `{}`); m.startAuto(c, x) || x.State != ActionPending {
		t.Fatal("Auto with estimates on: the estimate waits for the person")
	}
	if mo, _ := m.Mode(ctx); mo.EstimatesOff || !strings.Contains(mo.Summary, "Anything that costs money") {
		t.Fatalf("the mode's words with estimates on: %+v", mo)
	}
	// Estimates off: priced cards run by themselves too — only where a person
	// set the project's budget and the team's daily limit with that service
	// (D241). The defaults don't count.
	prefsSet(prefs.PreferencesSetIn{Estimates: &off})
	gen := priced("generate.shot", `{}`)
	if m.startAuto(c, gen) || gen.AutoPriced || !strings.Contains(gen.AutoWaits, "nobody set this project's budget for Google Gemini") {
		t.Fatalf("the usual budget doesn't count: %+v", gen)
	}
	budget, cap := 20.0, 5.0
	if _, err := ops.CallTyped[generate.GenSettingsOut](ctx, a, "generate.configure", generate.GenConfigureIn{ProjectID: projectID, BudgetUSD: &budget}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if x := priced("generate.shot", `{}`); m.startAuto(c, x) || !strings.Contains(x.AutoWaits, "nobody set the team's daily limit for Google Gemini") {
		t.Fatalf("the usual daily limit doesn't count: %+v", x)
	}
	// A daily limit with no person recorded (set before MetaRush kept who)
	// doesn't count either.
	if err := a.DB.Update(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), map[string]any{"$set": map[string]any{"providers.gemini.dailyCapUsd": 5.0}}); err != nil {
		t.Fatal(err)
	}
	if x := priced("generate.shot", `{}`); m.startAuto(c, x) || !strings.Contains(x.AutoWaits, "doesn't know who set the team's daily limit") {
		t.Fatalf("a limit nobody is recorded as setting: %+v", x)
	}
	if _, err := ops.CallTyped[generate.GenSettingsOut](ctx, a, "generate.configure", generate.GenConfigureIn{DailyCapUSD: &cap}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	for _, x := range []*AssistantAction{priced("generate.shot", `{}`), priced("generate.again", `{}`), priced("generate.extend", `{}`), priced("generate.edit", `{}`)} {
		if !m.startAuto(c, x) || !x.AutoPriced || !x.AutoMode || !x.Auto || x.State != ActionRunning || x.AutoWaits != "" {
			t.Fatalf("estimates off, limits set: %s runs by itself: %+v", x.Op, x)
		}
	}
	// A card paid from another project than the conversation's asks, whatever
	// that project's limits; so does one that doesn't say which project pays.
	another, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Another film"}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CallTyped[generate.GenSettingsOut](ctx, a, "generate.configure", generate.GenConfigureIn{ProjectID: another.Project.ID, BudgetUSD: &budget}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	elsewhere := priced("generate.again", `{}`)
	elsewhere.SpendsIn = another.Project.ID
	if m.startAuto(c, elsewhere) || elsewhere.AutoPriced || !strings.Contains(elsewhere.AutoWaits, "another project's budget") {
		t.Fatalf("paid from another project: %+v", elsewhere)
	}
	unsaid := priced("generate.shot", `{}`)
	unsaid.SpendsIn = ""
	if m.startAuto(c, unsaid) || !strings.Contains(unsaid.AutoWaits, "which project's budget") {
		t.Fatalf("no paying project: %+v", unsaid)
	}
	// ElevenLabs has no daily limit for the team: its cards ask, and say why.
	for _, x := range []*AssistantAction{priced("voiceover.tts", `{}`), priced("sound.enhance", cloud)} {
		if m.startAuto(c, x) || x.AutoPriced || x.AutoWaits == "" {
			t.Fatalf("no daily limit with ElevenLabs: %s asks: %+v", x.Op, x)
		}
	}
	// A service that doesn't say its limits asks too.
	other := priced("generate.shot", `{}`)
	other.Estimate.Parts[0].Service = "A new service"
	if m.startAuto(c, other) || !strings.Contains(other.AutoWaits, "doesn't know the limits") {
		t.Fatalf("unknown limits: %+v", other)
	}
	if mo, _ := m.Mode(ctx); !mo.EstimatesOff || !strings.Contains(mo.Summary, "so do changes that cost money") || !strings.Contains(mo.Summary, "daily limit") {
		t.Fatalf("the mode's words with estimates off: %+v", mo)
	}
	sp, err := ops.CallTyped[AssistantSpendingOut](ctx, a, "assistant.spending", AssistantSpendingIn{ProjectID: projectID}, ops.Caller{Human: true})
	// AI services pay from generated video's limits (D226), so they are listed with them.
	if err != nil || !strings.Contains(sp.Summary, "with AI services and Google Gemini") || !strings.Contains(sp.Summary, "With ElevenLabs it asks first") {
		t.Fatalf("spending %+v %v", sp, err)
	}
	for _, sv := range sp.Services {
		if sv.Service == "Google Gemini" && (!sv.AutoMay || sv.Daily.By != "Tester" || sv.Project.By != "Tester") {
			t.Fatalf("who set them: %+v", sv)
		}
	}
	// What Auto spent goes on the receipt's records and the bell, one note a day.
	ran := priced("generate.shot", `{}`)
	ran.Title, ran.State, ran.AutoPriced = "Make a shot of a beach", ActionDone, true
	who := models.Actor{UserID: a.Cfg.User.ID, UserName: "Tester"}
	m.recordAutoSpend(ctx, c, *ran, who)
	m.recordAutoSpend(ctx, c, *ran, who) // the same card twice counts once
	ran2 := priced("generate.again", `{}`)
	ran2.Title, ran2.State, ran2.AutoPriced = "Make another take", ActionDone, true
	m.recordAutoSpend(ctx, c, *ran2, who)
	var recs []models.AutoSpend
	_ = a.DB.Find(ctx, store.CAutoSpending, a.WorkspaceID(), map[string]any{"projectId": projectID}, &recs)
	// Recorded under the project that paid, which the card names.
	if n, _ := a.DB.C(store.CAutoSpending).CountDocuments(ctx, map[string]any{"projectId": map[string]any{"$ne": projectID}}); n != 0 {
		t.Fatalf("recorded under another project: %d", n)
	}
	// "Today" is this computer's day, as people count it.
	if len(recs) != 2 || recs[0].ForName != "Tester" || recs[0].USD != 0.12 || recs[0].Day != time.Now().Local().Format("2006-01-02") {
		t.Fatalf("auto spending %+v", recs)
	}
	var notes []models.Notification
	_ = a.DB.Find(ctx, store.CNotifications, a.WorkspaceID(), map[string]any{"kind": models.NotifyAutoSpent}, &notes)
	if len(notes) != 1 || notes[0].Title != "Auto spent $0.24 by itself today" || !strings.Contains(notes[0].Body, "2 changes") || notes[0].Link != "/settings/preferences#auto-spending" {
		t.Fatalf("bell %+v", notes)
	}
	// A generated shot still being made: "up to" its estimate; refused, it
	// cost nothing, and the note says what was really spent.
	genID := ids.New(ids.Generation)
	if err := a.DB.Insert(ctx, store.CGenerations, models.Generation{ID: genID, WorkspaceID: a.WorkspaceID(), ProjectID: projectID, JobID: "job_auto_shot",
		Estimate: models.GenCost{USD: 0.5}, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	ran3 := priced("generate.shot", `{}`)
	ran3.Title, ran3.State, ran3.AutoPriced = "Make a shot of the pond", ActionRunning, true
	ran3.Work = &ActionWork{Kind: WorkJobs, JobIDs: []string{"job_auto_shot"}, State: WorkRunning}
	m.recordAutoSpend(ctx, c, *ran3, who)
	bell := func() models.Notification {
		var ns []models.Notification
		_ = a.DB.Find(ctx, store.CNotifications, a.WorkspaceID(), map[string]any{"kind": models.NotifyAutoSpent}, &ns)
		if len(ns) != 1 {
			t.Fatalf("one note a day: %+v", ns)
		}
		return ns[0]
	}
	if n := bell(); n.Title != "Auto spent up to $0.74 by itself today" {
		t.Fatalf("pending: %q", n.Title)
	}
	if err := a.DB.Update(ctx, store.CGenerations, a.WorkspaceID(), genID, map[string]any{"$set": map[string]any{
		"outcome": models.GenOutcome{State: models.GenRefused, At: time.Now().UTC()}, "settled": models.GenSettlement{USD: 0, At: time.Now().UTC()}}}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.data.Actions = append(c.data.Actions, *ran3)
	c.mu.Unlock()
	m.refreshAutoSpend(ctx, c, ran3.ID)
	if n := bell(); n.Title != "Auto spent $0.24 by itself today" {
		t.Fatalf("refused: Auto spent nothing on it: %q", n.Title)
	}
	sp2, _ := ops.CallTyped[AssistantSpendingOut](ctx, a, "assistant.spending", AssistantSpendingIn{ProjectID: projectID}, ops.Caller{Human: true})
	if sp2.TodayUSD != 0.24 || sp2.TodayPending {
		t.Fatalf("spending today %+v", sp2)
	}
	// What still asks, whatever the estimates.
	noEstimate := priced("voiceover.tts", `{}`)
	noEstimate.Estimate = nil
	setup := priced("sound.enhance", cloud)
	setup.SetUp = []string{"studio"}
	people := priced("generate.shot", `{}`)
	people.HumanOnly = true
	question := priced("voiceover.tts", `{}`)
	question.Question = &QuestionCard{Text: "Which voice?"}
	for name, x := range map[string]*AssistantAction{"a card without its estimate": noEstimate, "a setup": setup, "a people-only decision": people,
		"a question": question, "a look test": priced("looks.test", `{}`), "a final render": priced("render.submit", `{"quality":"final"}`)} {
		if m.startAuto(c, x) || x.AutoPriced {
			t.Errorf("%s still asks: %+v", name, x)
		}
	}
	// A clean voice is free and runs under Auto as before (not a priced step).
	_ = cap
	if x := priced("sound.enhance", `{"revisionId":"`+head+`","target":"Sam","mode":"clean"}`); !m.startAuto(c, x) || x.AutoPriced {
		t.Fatalf("a clean voice: %+v", x)
	}
	// An owner asks everyone for estimates: priced cards ask again.
	prefsSet(prefs.PreferencesSetIn{RequireEstimates: &on})
	if x := priced("voiceover.tts", `{}`); m.startAuto(c, x) || x.State != ActionPending {
		t.Fatal("required for everyone: the estimate waits for the person")
	}
	if mo, _ := m.Mode(ctx); mo.EstimatesOff {
		t.Fatalf("required for everyone: %+v", mo)
	}
}
