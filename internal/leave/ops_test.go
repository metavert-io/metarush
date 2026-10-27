package leave_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/receipt"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

var person = ops.Caller{Human: true}

func do[O any](t *testing.T, a *app.App, name string, in any, c ops.Caller) *O {
	t.Helper()
	out, err := ops.CallTyped[O](context.Background(), a, name, in, c)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

func errOf(a *app.App, ctx context.Context, name string, in any, c ops.Caller) *apperr.Error {
	raw, _ := json.Marshal(in)
	_, err := ops.Call(ctx, a, name, raw, c)
	if err == nil {
		return nil
	}
	return apperr.From(err)
}

// member is a signed-in person who isn't an owner, over HTTP.
func member() context.Context {
	return ops.OverHTTP(context.Background(), func() bool { return false })
}

// What may leave (D288–D291): who changes it, switching settings, what AI
// readers are shown, cloud storage, and the record of what left.
func TestWhatMayLeave(t *testing.T) {
	t.Setenv("CLAUDECODE", "") // the test's own terminal isn't an AI reading
	a := testapp.New(t)
	ctx := context.Background()
	base := do[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Base"}, ops.Caller{}).Project // the default's settings: nothing set
	acme := do[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Acme"}, ops.Caller{}).Project
	other := do[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Other"}, ops.Caller{}).Project
	do[ops.ContentShowOut](t, a, "content.own", ops.ContentOwnIn{ProjectID: acme.ID}, ops.Caller{})

	// Nothing set: today's behaviour.
	shown := do[leave.LeaveOut](t, a, "content.leave", leave.LeaveIn{ProjectID: acme.ID}, ops.Caller{})
	if !shown.Rules.Empty() || !strings.HasPrefix(shown.Summary, "Anything MetaRush offers may leave") || len(shown.Places) != 6 {
		t.Fatalf("nothing set: %+v", shown)
	}

	// People only; anyone may keep more here.
	keep := leave.LeaveSetIn{ProjectID: acme.ID, Keep: map[string][]string{"ai": {"voices"}, "claude": {"words", "pictures", "faces"}, "storage": {"footage"}}}
	if e := errOf(a, ctx, "content.leave.set", keep, ops.Caller{}); e == nil || e.Code != apperr.HumanOnly {
		t.Fatalf("not a person: %v", e)
	}
	if e := errOf(a, member(), "content.leave.set", keep, ops.Caller{Human: true, Remote: true}); e != nil {
		t.Fatalf("a member keeps more here: %v", e)
	}
	got := do[leave.LeaveOut](t, a, "content.leave", leave.LeaveIn{ProjectID: acme.ID}, ops.Caller{})
	if !got.Rules.Keeps(models.PlaceAI, models.LeaveVoices) || got.SetBy == "" || !strings.Contains(got.Summary, "No voices to AI services") {
		t.Fatalf("kept: %+v", got)
	}
	// Letting more leave takes an owner.
	let := leave.LeaveSetIn{ProjectID: acme.ID, Let: map[string][]string{"ai": {"voices"}}}
	if e := errOf(a, member(), "content.leave.set", let, ops.Caller{Human: true, Remote: true}); e == nil || e.Code != apperr.Unauthorized || !strings.Contains(e.Message, "voices may go to AI services") {
		t.Fatalf("a member lets more leave: %v", e)
	}
	// So does using other settings that let more leave.
	if e := errOf(a, member(), "content.follow", ops.ContentFollowIn{ProjectID: acme.ID, UseDefault: true}, ops.Caller{Human: true, Remote: true}); e == nil || e.Code != apperr.Unauthorized {
		t.Fatalf("a member switches a client to looser settings: %v", e)
	}
	if e := errOf(a, member(), "content.follow", ops.ContentFollowIn{ProjectID: other.ID, Follow: acme.ID}, ops.Caller{Human: true, Remote: true}); e != nil {
		t.Fatalf("stricter settings are anyone's choice: %v", e)
	}
	do[ops.ContentShowOut](t, a, "content.follow", ops.ContentFollowIn{ProjectID: other.ID, UseDefault: true}, person) // an owner (this computer), a person

	// What an AI reads: Claude's channel can't read Acme's words; searching
	// every project is refused while a client keeps its words; a person can.
	claude := *a
	claude.Actor.Via = models.ViaAssistant
	if e := errOf(&claude, ctx, "transcript.find", ops.TranscriptFindIn{Query: "hello", ProjectID: acme.ID}, ops.Caller{}); e == nil || e.Code != apperr.KeptHere || !strings.Contains(e.Message, "keep words on this computer") {
		t.Fatalf("Claude reading a client's words: %v", e)
	}
	if e := errOf(&claude, ctx, "search", ops.SearchIn{Query: "hello"}, ops.Caller{}); e == nil || e.Code != apperr.KeptHere || !strings.Contains(e.Message, "one project whose rules allow it") {
		t.Fatalf("searching everything: %v", e)
	}
	if e := errOf(&claude, ctx, "search", ops.SearchIn{Query: "hello", ProjectID: other.ID}, ops.Caller{}); e != nil && e.Code == apperr.KeptHere {
		t.Fatalf("another project's words: %v", e)
	}
	if e := errOf(a, ctx, "search", ops.SearchIn{Query: "hello", ProjectID: acme.ID}, ops.Caller{}); e != nil && e.Code == apperr.KeptHere {
		t.Fatalf("a person reads them: %v", e)
	}
	mcp := *a
	mcp.Actor.Via, mcp.Actor.Agent = "mcp", "codex"
	if e := errOf(&mcp, ctx, "search", ops.SearchIn{Query: "hello", ProjectID: acme.ID}, ops.Caller{}); e == nil || e.Code != apperr.KeptHere {
		t.Fatalf("an AI app reading a client's words: %v", e)
	}
	if n := leave.ClaudeNotice(ctx, a, acme.ID, ""); !strings.Contains(n, "words, pictures and faces") {
		t.Fatalf("the panel's line: %q", n)
	}
	if n := leave.ClaudeNotice(ctx, a, other.ID, ""); n != "" {
		t.Fatalf("no line for another project: %q", n)
	}

	// Cloud storage: a client whose footage may not be in it refuses a move
	// there, and refuses adding footage while the media is there.
	cloud := config.Media{Provider: "s3", S3: config.S3{Endpoint: "https://storage.example.com", Bucket: "media"}}
	if err := ops.LeaveStorage(ctx, a, cloud); apperr.From(err).Code != apperr.KeptHere || !strings.Contains(apperr.From(err).Hint, "folder on this computer") {
		t.Fatalf("a move to a bucket: %v", err)
	}
	if err := ops.LeaveStorage(ctx, a, config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: t.TempDir()}}); err != nil {
		t.Fatalf("a folder here: %v", err)
	}
	inCloud := *a
	cfg := *a.Cfg
	cfg.Media = cloud
	inCloud.Cfg = &cfg
	if err := leave.MayStore(ctx, &inCloud, acme.ID, leave.FootageKinds, "a clip"); apperr.From(err).Code != apperr.KeptHere {
		t.Fatalf("footage for the client into a bucket: %v", err)
	}
	if err := leave.MayStore(ctx, &inCloud, other.ID, leave.FootageKinds, "a clip"); err != nil {
		t.Fatalf("another project's footage: %v", err)
	}
	if err := leave.MayStore(ctx, a, acme.ID, leave.FootageKinds, "a clip"); err != nil {
		t.Fatalf("a folder on this computer: %v", err)
	}
	if out := do[leave.LeaveOut](t, &inCloud, "content.leave", leave.LeaveIn{ProjectID: acme.ID}, ops.Caller{}); len(out.Problems) != 1 || !strings.Contains(out.Problems[0], "Settings → Storage") {
		t.Fatalf("the part says the bucket holds it: %+v", out.Problems)
	}

	// The record: refusals are kept; a project nothing of which left says so.
	n, _ := a.DB.C(store.CAudit).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "action": leave.AuditRefused, "projectId": acme.ID})
	if n < 4 {
		t.Fatalf("refusals recorded: %d", n)
	}
	l := do[receipt.ProjectLeavesOut](t, a, "project.leaves", receipt.ProjectLeavesIn{ProjectID: base.ID}, ops.Caller{})
	if !l.Left.Nothing || l.Left.Summary != "Nothing of “Base” left this computer." {
		t.Fatalf("nothing left: %+v", l.Left)
	}
	// Claude searched Other's words above, a moment ago: the receipt has it
	// (the activity log is written before the receipt is made).
	l = do[receipt.ProjectLeavesOut](t, a, "project.leaves", receipt.ProjectLeavesIn{ProjectID: other.ID}, ops.Caller{})
	if l.Left.Nothing || len(l.Left.Places) != 1 || l.Left.Places[0].Place != models.PlaceClaude || l.Left.Places[0].To != "Claude in the panel" ||
		strings.Join(l.Left.Places[0].Kinds, ",") != models.LeaveWords || l.Left.Places[0].Count != 1 {
		t.Fatalf("Claude's search of Other: %+v", l.Left)
	}
	l = do[receipt.ProjectLeavesOut](t, a, "project.leaves", receipt.ProjectLeavesIn{ProjectID: acme.ID}, ops.Caller{})
	if !l.Left.Nothing || len(l.Left.Refused) < 4 || !strings.Contains(strings.Join(l.Left.Lines, " "), "kept it here") {
		t.Fatalf("Acme's record: %+v", l.Left)
	}
	r := do[receipt.ProjectReceiptOut](t, a, "project.receipt", receipt.ProjectReceiptIn{ProjectID: acme.ID}, ops.Caller{})
	if r.Left == nil || !r.Left.Nothing || !strings.Contains(strings.Join(r.Lines, " "), "Nothing of “Acme” left this computer.") {
		t.Fatalf("the receipt says it: %+v", r.Lines)
	}

	// Nothing leaves at all: every place, every kind.
	do[leave.LeaveOut](t, a, "content.leave.set", leave.LeaveSetIn{ProjectID: acme.ID, Nothing: ptr(true), Note: ptr("the client's contract")}, person)
	if e := errOf(&claude, ctx, "search", ops.SearchIn{Query: "x", ProjectID: acme.ID}, ops.Caller{}); e == nil || !strings.Contains(e.Message, "keep everything on this computer") || !strings.Contains(e.Hint, "contract") {
		t.Fatalf("nothing leaves: %v", e)
	}
}

func ptr[T any](v T) *T { return &v }
