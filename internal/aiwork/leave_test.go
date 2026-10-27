package aiwork_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/receipt"
	"github.com/metavert-io/metarush/internal/store"
)

func refusals(t *testing.T, e *env) int64 {
	t.Helper()
	n, err := e.a.DB.C(store.CAudit).CountDocuments(context.Background(), bson.M{"workspaceId": e.a.WorkspaceID(), "action": leave.AuditRefused})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func setLeave(t *testing.T, e *env, in leave.LeaveSetIn) {
	t.Helper()
	t.Setenv("CLAUDECODE", "") // a person's terminal, not Claude Code's (letting more leave takes a person)
	in.ProjectID = e.project
	if _, err := call[leave.LeaveOut](t, e.a, "content.leave.set", in, true); err != nil {
		t.Fatal(err)
	}
}

// What may leave (D290): a client's voices kept from AI services refuse a
// cloud transcript at the estimate (said, not recorded) and at making it
// (recorded); a list of who may receive refuses another provider; and the
// rules are asked again just before a request is sent.
func TestWhatMayLeaveAIServices(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := ops.CallTyped[ops.ContentShowOut](ctx, e.a, "content.own", ops.ContentOwnIn{ProjectID: e.project}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	clip := e.sound(t, "talk", 4)
	setLeave(t, e, leave.LeaveSetIn{Keep: map[string][]string{"ai": {"voices"}}, Note: ptr("the client's contract")})
	tr := aiwork.MakeIn{Kind: aiservices.KindTranscription, ProjectID: e.project, Model: "fake/fake-transcription", Clip: clip.ID}
	_, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", tr, false)
	if codeOf(err) != apperr.KeptHere || !strings.Contains(apperr.From(err).Message, "keep voices on this computer") || !strings.Contains(apperr.From(err).Hint, "contract") {
		t.Fatalf("an estimate says it: %v", err)
	}
	if n := refusals(t, e); n != 0 {
		t.Fatalf("an estimate records nothing: %d", n)
	}
	tr.MaxUSD = usd(1)
	if _, err := call[aiwork.MakeOut](t, e.a, "ai.make", tr, false); codeOf(err) != apperr.KeptHere {
		t.Fatalf("making it: %v", err)
	}
	if n := refusals(t, e); n != 1 {
		t.Fatalf("a refusal is recorded: %d", n)
	}
	// Words may still go: a picture from words is made.
	pic := aiwork.MakeIn{Kind: aiservices.KindPicture, ProjectID: e.project, Model: "fake/fake-picture", Prompt: "a kite", Options: models.GenOptions{Size: "1K", AspectRatio: "1:1"}}
	if est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", pic, false); err != nil || !est.Allowed {
		t.Fatalf("words may go: %+v %v", est, err)
	}
	// Only some providers may receive things: the fake one is refused.
	setLeave(t, e, leave.LeaveSetIn{Only: map[string][]string{"ai": {"elevenlabs"}}})
	if _, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", pic, false); codeOf(err) != apperr.KeptHere || !strings.Contains(apperr.From(err).Message, "allow only ElevenLabs among AI services") {
		t.Fatalf("only some providers: %v", err)
	}
	// Asked again before sending: a request made while allowed stops at $0
	// when the rules change before it is sent.
	setLeave(t, e, leave.LeaveSetIn{Anyone: []string{"ai"}})
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", pic, false)
	if err != nil {
		t.Fatal(err)
	}
	pic.MaxUSD = usd(est.USD)
	out, err := call[aiwork.MakeOut](t, e.a, "ai.make", pic, false)
	if err != nil {
		t.Fatal(err)
	}
	setLeave(t, e, leave.LeaveSetIn{Keep: map[string][]string{"ai": {"words"}}})
	_, _ = e.run(t, out.JobID)
	if e.fake.Calls("submit") != 0 {
		t.Fatal("nothing reaches the provider once the rules keep it here")
	}
	if g := e.gen(t, out.Work.ID); g.Outcome == nil || g.Outcome.State == models.GenDone {
		t.Fatalf("the request stops: %+v", g.Outcome)
	}
	if n := refusals(t, e); n != 2 {
		t.Fatalf("the late refusal is recorded too: %d", n)
	}
}

func ptr[T any](v T) *T { return &v }

// The receipt says what left (D291): a picture made from words is words sent
// to the provider; nothing else left.
func TestReceiptSaysWhatLeft(t *testing.T) {
	t.Setenv("CLAUDECODE", "") // a person's terminal: its estimates aren't an AI reading
	e := setup(t)
	if l, err := call[receipt.ProjectLeavesOut](t, e.a, "project.leaves", receipt.ProjectLeavesIn{ProjectID: e.project}, false); err != nil || !l.Left.Nothing {
		t.Fatalf("nothing yet: %+v %v", l, err)
	}
	e.make(t, aiwork.MakeIn{Kind: aiservices.KindPicture, ProjectID: e.project, Model: "fake/fake-picture", Prompt: "a kite", Options: models.GenOptions{Size: "1K", AspectRatio: "1:1"}})
	l, err := call[receipt.ProjectLeavesOut](t, e.a, "project.leaves", receipt.ProjectLeavesIn{ProjectID: e.project}, false)
	if err != nil || l.Left.Nothing || len(l.Left.Places) != 1 {
		t.Fatalf("left: %+v %v", l, err)
	}
	p := l.Left.Places[0]
	if p.Place != models.PlaceAI || p.Count != 1 || strings.Join(p.Kinds, ",") != models.LeaveWords || !strings.Contains(strings.Join(l.Left.Lines, " "), "left this computer for") {
		t.Fatalf("the place: %+v %v", p, l.Left.Lines)
	}
}

func (e *env) otherProject(t *testing.T) string {
	t.Helper()
	other := ids.New(ids.Project)
	now := time.Now().UTC()
	if err := e.a.DB.Insert(context.Background(), store.CProjects, models.Project{ID: other, WorkspaceID: e.a.WorkspaceID(), Name: "Other", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return other
}

// A clip in two projects: work asked for from the other project, which keeps
// nothing here, is still refused by the clip's own client's rules.
func TestSharedClipFollowsEveryProjectsRules(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := ops.CallTyped[ops.ContentShowOut](ctx, e.a, "content.own", ops.ContentOwnIn{ProjectID: e.project}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	clip := e.sound(t, "talk", 4)
	b := e.otherProject(t)
	if _, err := e.a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": clip.ID}, bson.M{"$addToSet": bson.M{"projectIds": b}}); err != nil {
		t.Fatal(err)
	}
	setLeave(t, e, leave.LeaveSetIn{Keep: map[string][]string{"ai": {"voices"}}})
	for _, kind := range []string{aiservices.KindTranscription, aiservices.KindDubbing, aiservices.KindVoiceChange} {
		in := aiwork.MakeIn{Kind: kind, ProjectID: b, Model: "fake/fake-" + kind, Clip: clip.ID, Options: models.GenOptions{TargetLanguage: "fr", Voice: "Kore"}, MaxUSD: usd(5)}
		if _, err := call[aiwork.MakeOut](t, e.a, "ai.make", in, false); codeOf(err) != apperr.KeptHere {
			t.Errorf("%s from the other project: %v", kind, err)
		}
	}
}

// The receipt of a clip's project lists work on it asked for from another
// project, or from none.
func TestReceiptListsWorkAskedForElsewhere(t *testing.T) {
	t.Setenv("CLAUDECODE", "") // a person's terminal: its estimates aren't an AI reading
	e := setup(t)
	clip := e.sound(t, "talk", 4)
	b := e.otherProject(t)
	e.make(t, aiwork.MakeIn{Kind: aiservices.KindTranscription, Model: "fake/fake-transcription", Clip: clip.ID})
	e.make(t, aiwork.MakeIn{Kind: aiservices.KindDubbing, ProjectID: b, Model: "fake/fake-dubbing", Clip: clip.ID, Options: models.GenOptions{TargetLanguage: "es"}})
	l, err := call[receipt.ProjectLeavesOut](t, e.a, "project.leaves", receipt.ProjectLeavesIn{ProjectID: e.project}, false)
	if err != nil {
		t.Fatal(err)
	}
	if l.Left.Nothing || len(l.Left.Places) != 1 || l.Left.Places[0].Count != 2 || !strings.Contains(strings.Join(l.Left.Places[0].Kinds, ","), models.LeaveVoices) {
		t.Fatalf("both requests sent the clip's voices: %+v", l.Left)
	}
}

// A voice copy's dry run says a refusal and records nothing.
func TestVoiceCopyDryRunRecordsNothing(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := ops.CallTyped[ops.ContentShowOut](ctx, e.a, "content.own", ops.ContentOwnIn{ProjectID: e.project}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	in, _ := e.copyReady(t, "Ada Lovelace")
	setLeave(t, e, leave.LeaveSetIn{Keep: map[string][]string{"ai": {"voices"}}})
	in.DryRun, in.MaxUSD = true, nil
	if _, err := call[aiwork.MakeOut](t, e.a, "ai.voices.copy", in, true); codeOf(err) != apperr.KeptHere {
		t.Fatalf("dry run: %v", err)
	}
	if n := refusals(t, e); n != 0 {
		t.Errorf("a dry run recorded %d refusals", n)
	}
}
