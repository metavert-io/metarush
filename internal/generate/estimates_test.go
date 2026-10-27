package generate_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/prefs"
	"github.com/metavert-io/metarush/internal/store"
)

// The estimate opens to what it draws on (D208): the video at its
// resolution, what is sent with the prompt and the thinking allowance, each
// with the model, the source, its units and its price; the parts add up to
// the price a person agrees to.
func TestTheEstimateOpensToWhatItDrawsOn(t *testing.T) {
	e := setup(t)
	in := generate.GenShotIn{ProjectID: e.project, Prompt: "a red paper boat drifting on a pond", Seconds: 3, Resolution: "360p", Takes: 2}
	est, err := call[generate.GenEstimate](t, e.a, "generate.estimate", in, false)
	if err != nil {
		t.Fatal(err)
	}
	c := est.Cost
	if c.USD != est.USD || c.About != costs.About(est.USD) || len(c.Parts) != 3 || len(c.Notes) == 0 {
		t.Fatalf("estimate %+v", c)
	}
	video, sent, thinking := c.Parts[0], c.Parts[1], c.Parts[2]
	for _, p := range c.Parts {
		if p.Service != "Google Gemini" || p.Model != "Gemini Omni 1.1 Flash" || p.What != "generated shots" || !strings.Contains(p.Rate, "Google Gemini's prices of") {
			t.Fatalf("a part says the service, the model and the price: %+v", p)
		}
	}
	if video.Source != "“a red paper boat drifting on a pond”" || video.Amount != 6 || video.Unit != "seconds of video" ||
		!strings.HasPrefix(video.Units, "3 seconds of video at 360p, 2 takes (") || !strings.Contains(video.Rate, "per million tokens of video") {
		t.Fatalf("the video: %+v", video)
	}
	if sent.Source != "What is sent: the prompt" || sent.Unit != "tokens" || thinking.Source != "An allowance for the model's thinking" || thinking.Amount != 6000 {
		t.Fatalf("what is sent and the thinking: %+v %+v", sent, thinking)
	}
	sum := 0.0
	for _, p := range c.Parts {
		sum += p.USD
	}
	if d := sum - est.USD; d > 0.002 || d < -0.002 {
		t.Fatalf("the parts add up to the price: %.4f vs %.4f", sum, est.USD)
	}
}

// Estimates off (D208): a shot goes ahead without an agreed price, within the
// project's budget and today's limit; an owner who asks everyone for
// estimates brings the step back; a person's own choice is theirs alone.
func TestEstimatesOffSkipTheEstimateStep(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	uid := e.a.Cfg.User.ID
	if err := e.a.DB.Insert(ctx, store.CUsers, models.User{ID: uid, WorkspaceID: e.a.WorkspaceID(), Name: "Tester", Email: "tester@example.com", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	in := generate.GenShotIn{ProjectID: e.project, Prompt: "a paper boat", Seconds: 3, Resolution: "360p"}
	// On by default: the price must be agreed.
	if p, err := call[prefs.PreferencesOut](t, e.a, "account.preferences", prefs.PreferencesIn{}, false); err != nil || !p.Estimates || !p.EstimatesYours || p.EstimatesRequired || !p.Owner {
		t.Fatalf("on by default: %+v %v", p, err)
	}
	if _, err := call[generate.GenRunOut](t, e.a, "generate.shot", in, false); codeOf(err) != apperr.ConfirmRequired {
		t.Fatalf("estimates on: the price first: %v", err)
	}
	// Only a person turns them off.
	off := false
	if _, err := call[prefs.PreferencesOut](t, e.a, "account.preferences.set", prefs.PreferencesSetIn{Estimates: &off}, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("people only: %v", err)
	}
	p, err := call[prefs.PreferencesOut](t, e.a, "account.preferences.set", prefs.PreferencesSetIn{Estimates: &off}, true)
	if err != nil || p.Estimates || p.EstimatesYours || !strings.HasPrefix(p.Summary, "Estimates are off for you") {
		t.Fatalf("off: %+v %v", p, err)
	}
	est, _ := call[generate.GenEstimate](t, e.a, "generate.estimate", in, false)
	out, err := call[generate.GenRunOut](t, e.a, "generate.shot", in, false)
	if err != nil {
		t.Fatalf("estimates off: no agreed price needed: %v", err)
	}
	if g := out.Generations[0]; g.Budget == nil || g.Budget.ReservedUSD != est.USD {
		t.Fatalf("the estimate is still what is set aside: %+v", g.Budget)
	}
	var ev models.AuditEvent
	if err := e.a.DB.C(store.CAudit).FindOne(ctx, bson.M{"workspaceId": e.a.WorkspaceID(), "action": "generate.shot"}).Decode(&ev); err != nil || ev.Details["estimateFirst"] != false {
		t.Fatalf("the audit says the estimate step was skipped: %+v %v", ev.Details, err)
	}
	// Budgets still decide: a shot over the project's budget is refused.
	if _, err := call[generate.GenSettingsOut](t, e.a, "generate.configure", generate.GenConfigureIn{ProjectID: e.project, BudgetUSD: usd(est.USD * 1.5)}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := call[generate.GenRunOut](t, e.a, "generate.shot", in, false); codeOf(err) != apperr.Budget {
		t.Fatalf("over the budget, estimates off or not: %v", err)
	}
	// An owner asks everyone for estimates: the step comes back.
	on := true
	p, err = call[prefs.PreferencesOut](t, e.a, "account.preferences.set", prefs.PreferencesSetIn{RequireEstimates: &on}, true)
	if err != nil || !p.Estimates || p.EstimatesYours || !p.EstimatesRequired || p.RequiredBy != "tester" || !strings.HasPrefix(p.Summary, "An owner asks for estimates for everyone") {
		t.Fatalf("required: %+v %v", p, err)
	}
	if _, err := call[generate.GenSettingsOut](t, e.a, "generate.configure", generate.GenConfigureIn{ProjectID: e.project, BudgetUSD: usd(0)}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := call[generate.GenRunOut](t, e.a, "generate.shot", in, false); codeOf(err) != apperr.ConfirmRequired {
		t.Fatalf("required by an owner: the price first again: %v", err)
	}
	if !prefs.EstimatesFirst(ctx, e.a, uid) || prefs.EstimatesFirst(ctx, e.a, "") != true {
		t.Fatal("the hook follows the owners' word")
	}
	// Nothing to change is a plain refusal.
	if _, err := call[prefs.PreferencesOut](t, e.a, "account.preferences.set", prefs.PreferencesSetIn{}, true); codeOf(err) != apperr.InvalidInput {
		t.Fatalf("nothing to change: %v", err)
	}
}
