package ops_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// A review copy follows the latest version (D187).

// followRenders are a version's review copies that aren't canceled.
func followRenders(t *testing.T, a *app.App, revisionID string) []models.Render {
	t.Helper()
	var rs []models.Render
	if err := a.DB.Find(context.Background(), store.CRenders, a.WorkspaceID(), bson.M{"revisionId": revisionID, "quality": "review", "state": bson.M{"$ne": models.RenderCanceled}}, &rs); err != nil {
		t.Fatal(err)
	}
	return rs
}

func follow(t *testing.T, a *app.App, at time.Time) *ops.FollowResult {
	t.Helper()
	res, err := ops.FollowLatest(context.Background(), a, at)
	if err != nil {
		t.Fatalf("following the latest versions: %v", err)
	}
	return res
}

// handEdit saves a change the way the studio's timeline does (in the
// background, from the dashboard), on top of base.
func handEdit(t *testing.T, a *app.App, projectID, base string, clipIDs ...string) string {
	t.Helper()
	ha := *a
	ha.Actor.Via = "dashboard"
	spec := editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: "vertical-1080p", Width: 1080, Height: 1920, FPS: 30, Platform: "shorts"}}
	for i, id := range clipIDs {
		spec.Clips = append(spec.Clips, editspec.Clip{ID: "c" + string(rune('a'+i)), AssetID: id, In: 0, Out: 2 + float64(i)})
	}
	plan, _ := json.Marshal(editspec.Plan{ProjectID: projectID, BaseRevisionID: base, Instruction: "Trimmed a clip", Origin: "timeline:studio", Spec: &spec})
	chk := do[ops.EditCheckOut](t, &ha, "edit.check", ops.EditCheckIn{Plan: plan})
	if !chk.Valid {
		t.Fatalf("hand edit not valid: %+v", chk.Errors)
	}
	return do[ops.EditApplyOut](t, &ha, "edit.apply", ops.EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken}).Revision.ID
}

// Once a cut has been left alone for a minute, its latest version gets a
// review copy by itself: a heavy job (it waits on battery like other big
// jobs), keyed like any request for that copy, made as MetaRush for the person
// who saved the version. Versions saved while someone is still editing wait;
// a newer version replaces an older version's copy that hasn't started, and a
// copy that is rendering is never stopped.
func TestReviewCopyFollowsTheLatestVersion(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := project(t, a, "Garden story")
	c := clip(t, a, "Garage interview", p.ID)
	v1 := saveCut(t, a, p.ID, "", c.ID)

	if res := follow(t, a, time.Now()); len(res.Queued) != 0 {
		t.Fatalf("a version saved a moment ago got a review copy at once: %+v", res.Queued)
	}
	later := time.Now().Add(ops.ReviewFollowQuiet + time.Second)
	res := follow(t, a, later)
	if len(res.Queued) != 1 || res.Queued[0].RevisionID != v1 {
		t.Fatalf("after a quiet minute: queued %+v, want version 1's review copy", res.Queued)
	}
	rd := res.Queued[0]
	if !rd.Follow || rd.Quality != "review" || rd.CreatedBy.Via != "worker" || rd.CreatedBy.UserID != a.Actor.UserID {
		t.Fatalf("the review copy: %+v", rd)
	}
	j, err := a.Queue.Get(ctx, rd.JobID)
	if err != nil || !j.Heavy || j.IdempotencyKey != "render:"+v1+":vertical-1080p:review" || j.Payload["follow"] != true {
		t.Fatalf("its job: %+v (%v)", j, err)
	}
	if again := follow(t, a, later); len(again.Queued) != 0 {
		t.Fatalf("looking again queued another: %+v", again.Queued)
	}
	// A person asking for that review copy gets the same render.
	asked := do[ops.RenderOut](t, a, "render.submit", ops.RenderSubmitIn{RevisionID: v1, Quality: "review"})
	if asked.Created || asked.Render.ID != rd.ID {
		t.Fatalf("asking for the copy MetaRush already queued: %+v", asked)
	}

	// Someone keeps editing: the newest version waits for its quiet minute,
	// and version 1's copy (not started) stays until then.
	v2 := handEdit(t, a, p.ID, v1, c.ID)
	if res := follow(t, a, time.Now().Add(ops.ReviewFollowQuiet/2)); len(res.Queued)+len(res.Superseded) != 0 {
		t.Fatalf("while still editing: %+v", res)
	}
	res = follow(t, a, time.Now().Add(ops.ReviewFollowQuiet+time.Second))
	if len(res.Queued) != 1 || res.Queued[0].RevisionID != v2 || len(res.Superseded) != 1 || res.Superseded[0].ID != rd.ID {
		t.Fatalf("a newer version: %+v", res)
	}
	if old, _ := a.Queue.Get(ctx, rd.JobID); old.State != models.JobCanceled {
		t.Fatalf("version 1's copy that hadn't started: job %s", old.State)
	}
	if len(followRenders(t, a, v1)) != 0 {
		t.Fatal("version 1's replaced copy still counts")
	}

	// Version 2's copy is rendering when version 3 comes: it finishes.
	w := jobs.Worker{ID: "ins_renderer", Name: "renderer", JobTypes: []string{jobs.TypeRender}, AcceptsHeavy: true}
	leased, err := a.Queue.Lease(ctx, w)
	if err != nil || leased == nil || leased.ID != res.Queued[0].JobID {
		t.Fatalf("leasing version 2's copy: %+v %v", leased, err)
	}
	v3 := handEdit(t, a, p.ID, v2, c.ID, c.ID)
	res = follow(t, a, time.Now().Add(ops.ReviewFollowQuiet+time.Second))
	if len(res.Queued) != 1 || res.Queued[0].RevisionID != v3 || len(res.Superseded) != 0 {
		t.Fatalf("a newer version while the older copy renders: %+v", res)
	}
	if running, _ := a.Queue.Get(ctx, leased.ID); running.State != models.JobRunning || running.CancelRequested {
		t.Fatalf("the copy that was rendering: %s (stop asked: %v)", running.State, running.CancelRequested)
	}

	// A copy someone stopped is never made again by itself.
	v3copy := res.Queued[0]
	do[ops.RenderOut](t, a, "render.cancel", ops.RenderIDIn{ID: v3copy.ID})
	if res := follow(t, a, time.Now().Add(ops.ReviewFollowQuiet+time.Second)); len(res.Queued) != 0 {
		t.Fatalf("a stopped copy was queued again: %+v", res.Queued)
	}
}

// The setting turns it off for the workspace (and back on); with several
// machines' daemons looking at once, one review copy is queued, one job.
func TestReviewFollowSettingAndSeveralMachines(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: a.WorkspaceID(), WorkspaceID: a.WorkspaceID(), Name: "T"}); err != nil {
		t.Fatal(err)
	}
	p := project(t, a, "Kitchen chat")
	c := clip(t, a, "Kitchen", p.ID)
	v1 := saveCut(t, a, p.ID, "", c.ID)
	later := time.Now().Add(ops.ReviewFollowQuiet + time.Second)

	if s := do[ops.FollowOut](t, a, "render.follow.show", ops.EmptyIn{}); !s.On || s.QuietSeconds != int(ops.ReviewFollowQuiet/time.Second) {
		t.Fatalf("by default: %+v", s)
	}
	off := do[ops.FollowOut](t, a, "render.follow.set", ops.FollowSetIn{On: false})
	if off.On || off.UpdatedBy == nil || off.UpdatedBy.UserID != a.Actor.UserID {
		t.Fatalf("turned off: %+v", off)
	}
	if res := follow(t, a, later); len(res.Queued) != 0 {
		t.Fatalf("queued while turned off: %+v", res.Queued)
	}
	do[ops.FollowOut](t, a, "render.follow.set", ops.FollowSetIn{On: true})

	// Another machine's daemon on the same database.
	cfg := *a.Cfg
	cfg.Installation = config.Ref{ID: "ins_second", Name: "second machine"}
	b := *a
	b.Cfg = &cfg
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 8)
	for i := range 8 {
		who := a
		if i%2 == 1 {
			who = &b
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := ops.FollowLatest(ctx, who, later); err != nil {
				errs <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("following on two machines: %v", err)
	}
	if rs := followRenders(t, a, v1); len(rs) != 1 {
		t.Fatalf("%d review copies of version 1 after two machines looked at once, want 1", len(rs))
	}
	n, _ := a.DB.C(store.CJobs).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "idempotencyKey": "render:" + v1 + ":vertical-1080p:review"})
	if n != 1 {
		t.Fatalf("%d jobs for version 1's review copy, want 1", n)
	}
	if s := do[ops.FollowOut](t, a, "render.follow.show", ops.EmptyIn{}); len(s.Making) != 1 {
		t.Fatalf("what it is making: %+v", s.Making)
	}
}

// "Awaiting your review" (D188) lists each cut's latest draft when Claude made
// it or someone asked to have it reviewed; a hand edit saved in the
// background, and the review copy MetaRush makes of it by itself, don't put
// it there.
func TestAwaitingReviewListsWhatSomeoneShouldLookAt(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	waiting := func() map[string]ops.ReviewWaitingRow {
		t.Helper()
		out := do[ops.ReviewWaitingOut](t, a, "review.waiting", ops.ReviewWaitingIn{})
		m := map[string]ops.ReviewWaitingRow{}
		for _, v := range out.Versions {
			m[v.ProjectID] = v
		}
		return m
	}
	claude := project(t, a, "Claude's cut")
	byClaude := saveCut(t, a, claude.ID, "", clip(t, a, "A", claude.ID).ID) // from the terminal: Claude Code

	hand := project(t, a, "Hand edits")
	hc := clip(t, a, "B", hand.ID)
	h1 := saveCut(t, a, hand.ID, "", hc.ID)
	h2 := handEdit(t, a, hand.ID, h1, hc.ID)

	asked := project(t, a, "Asked for a copy")
	ac := clip(t, a, "C", asked.ID)
	a2 := handEdit(t, a, asked.ID, saveCut(t, a, asked.ID, "", ac.ID), ac.ID)
	pa := *a
	pa.Actor.Via = "dashboard"
	do[ops.RenderOut](t, &pa, "render.submit", ops.RenderSubmitIn{RevisionID: a2, Quality: "review"})

	pressed := project(t, a, "Ask for review")
	pc := clip(t, a, "D", pressed.ID)
	p2 := handEdit(t, a, pressed.ID, saveCut(t, a, pressed.ID, "", pc.ID), pc.ID)
	if s := do[ops.RevisionSummary](t, &pa, "revision.askReview", ops.AskReviewIn{ID: p2}); s.ReviewAsked == nil || s.ReviewAsked.How != models.ReviewAskAsked {
		t.Fatalf("ask for review: %+v", s)
	}

	approved := project(t, a, "Approved")
	ap := saveCut(t, a, approved.ID, "", clip(t, a, "E", approved.ID).ID)
	do[ops.RevisionSummary](t, a, "revision.approve", ops.RevisionApproveIn{ID: ap})

	// MetaRush makes the hand edit's review copy by itself: still not waiting.
	if res := follow(t, a, time.Now().Add(ops.ReviewFollowQuiet+time.Second)); len(followRenders(t, a, h2)) != 1 {
		t.Fatalf("the hand edit's review copy: %+v", res)
	}

	got := waiting()
	if v, ok := got[claude.ID]; !ok || v.Revision.ID != byClaude || v.Why != ops.WaitingClaude || !v.Revision.ByClaude {
		t.Errorf("Claude's version: %+v (listed %v)", v, ok)
	}
	if v, ok := got[hand.ID]; ok {
		t.Errorf("a hand edit saved in the background is waiting: %+v", v)
	}
	if v, ok := got[asked.ID]; !ok || v.Revision.ID != a2 || v.Why != ops.WaitingAsked || v.Said != "tester asked for a review copy" {
		t.Errorf("a version someone asked a review copy of: %+v (listed %v)", v, ok)
	}
	if v, ok := got[pressed.ID]; !ok || v.Revision.ID != p2 || v.Said != "tester asked for review" {
		t.Errorf("a version someone asked to have reviewed: %+v (listed %v)", v, ok)
	}
	if v, ok := got[approved.ID]; ok {
		t.Errorf("an approved version is waiting: %+v", v)
	}
	var r models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), h2, &r)
	if r.ReviewAsked != nil {
		t.Errorf("MetaRush's own review copy counted as someone asking: %+v", r.ReviewAsked)
	}
	// One project only.
	one := do[ops.ReviewWaitingOut](t, a, "review.waiting", ops.ReviewWaitingIn{ProjectID: "Claude's cut"})
	if len(one.Versions) != 1 || one.Versions[0].ProjectID != claude.ID {
		t.Errorf("one project's: %+v", one.Versions)
	}
}

// savedAt makes a cut's latest version look saved at t (the version, its
// branch and the project).
func savedAt(t *testing.T, a *app.App, projectID, revisionID string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := a.DB.Update(ctx, store.CRevisions, a.WorkspaceID(), revisionID, bson.M{"$set": bson.M{"createdAt": at}}); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Update(ctx, store.CProjects, a.WorkspaceID(), projectID, bson.M{"$set": bson.M{"branches.main.updatedAt": at, "updatedAt": at}}); err != nil {
		t.Fatal(err)
	}
}

// The daemons look by the database server's clock (D360): a computer ten
// minutes fast doesn't queue a copy of a version saved under a minute ago, nor
// skip one saved within two hours; one ten minutes slow queues a version quiet
// for a minute and skips one saved over two hours ago.
func TestReviewCopyFollowsByTheServersClock(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	busy, older, oldest := project(t, a, "Still editing"), project(t, a, "Saved a while ago"), project(t, a, "Saved long ago")
	vBusy := saveCut(t, a, busy.ID, "", clip(t, a, "Porch", busy.ID).ID)
	vOlder := saveCut(t, a, older.ID, "", clip(t, a, "Dock", older.ID).ID)
	vOldest := saveCut(t, a, oldest.ID, "", clip(t, a, "Barn", oldest.ID).ID)
	now := byServer(t, a)
	savedAt(t, a, busy.ID, vBusy, now.Add(-50*time.Second))
	savedAt(t, a, older.ID, vOlder, now.Add(-ops.ReviewFollowWindow+5*time.Minute))

	restore := testutil.SkewClock(t, 10*time.Minute)
	if _, err := ops.FollowLook(ctx, a); err != nil {
		t.Fatal(err)
	}
	if n := len(followRenders(t, a, vBusy)); n != 0 {
		t.Fatalf("a computer ten minutes fast made a copy of a version saved 50 seconds ago (%d)", n)
	}
	if n := len(followRenders(t, a, vOlder)); n != 1 {
		t.Fatalf("a computer ten minutes fast skipped a version saved under two hours ago (%d copies)", n)
	}
	restore()

	now = byServer(t, a)
	savedAt(t, a, busy.ID, vBusy, now.Add(-70*time.Second))
	savedAt(t, a, oldest.ID, vOldest, now.Add(-ops.ReviewFollowWindow-5*time.Minute))
	restore = testutil.SkewClock(t, -10*time.Minute)
	if _, err := ops.FollowLook(ctx, a); err != nil {
		t.Fatal(err)
	}
	if n := len(followRenders(t, a, vBusy)); n != 1 {
		t.Fatalf("a computer ten minutes slow made no copy of a version quiet for 70 seconds (%d)", n)
	}
	if n := len(followRenders(t, a, vOldest)); n != 0 {
		t.Fatalf("a computer ten minutes slow made a copy of a version saved over two hours ago (%d)", n)
	}
	restore()
}

// An AI app's draft is saved at the server's time, and its review copy waits
// for a quiet minute by that clock, whatever the clocks of the computers that
// saved it and look at it say.
func TestDraftReviewCopyByTheServersClock(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := project(t, a, "Drafts and clocks")
	v1 := saveCut(t, a, p.ID, "", clip(t, a, "Field", p.ID).ID)
	restore := testutil.SkewClock(t, 10*time.Minute)
	d := do[ops.DraftView](t, agentApp(a, "codex", "mcp"), ops.OpDraftSave, ops.DraftSaveIn{Op: "edit.apply", Input: draftInput(t, ops.EditApplyIn{Plan: clipOutPlan(t, v1, "ca", 2)})})
	if now := byServer(t, a); !near(d.CreatedAt, now) || !near(revisionOf(t, a, d.Revision.ID).CreatedAt, now) {
		t.Fatalf("the draft (%v) and its version (%v) weren't saved at the server's time %v", d.CreatedAt, revisionOf(t, a, d.Revision.ID).CreatedAt, now)
	}
	if _, err := ops.FollowLook(ctx, a); err != nil {
		t.Fatal(err)
	}
	if n := len(followRenders(t, a, d.Revision.ID)); n != 0 {
		t.Fatalf("a computer ten minutes fast made a copy of a draft saved a moment ago (%d)", n)
	}
	restore()
	if err := a.DB.Update(ctx, store.CDrafts, a.WorkspaceID(), d.ID, bson.M{"$set": bson.M{"createdAt": byServer(t, a).Add(-70 * time.Second)}}); err != nil {
		t.Fatal(err)
	}
	restore = testutil.SkewClock(t, -10*time.Minute)
	if _, err := ops.FollowLook(ctx, a); err != nil {
		t.Fatal(err)
	}
	if n := len(followRenders(t, a, d.Revision.ID)); n != 1 {
		t.Fatalf("a computer ten minutes slow made no copy of a draft quiet for 70 seconds (%d)", n)
	}
	restore()
}
