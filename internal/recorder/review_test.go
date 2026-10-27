package recorder

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Two finishes at once (the browser's Stop and `recording finish --keep`)
// make footage once: one finish runs, one conversation, one clip per track;
// the other answers with the work in progress.
func TestTwoFinishesAtOnceMakeFootageOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.create(t, "screen", "camera", "voice")
	for _, tr := range []string{"screen", "camera", "voice"} {
		f.send(t, r.ID, tr, f.bytesOf(tr))
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = Finish(ctx, f.a, &RecordingFinishIn{Recording: r.ID, Clock: clockFor(f, "screen", "camera", "voice")})
		}()
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("a finish failed: %v", err)
		}
	}
	got, _ := get(ctx, f.a, r.ID)
	audits, _ := f.a.DB.C(store.CAudit).CountDocuments(ctx, bson.M{"action": "recording.finish", "targetId": r.ID})
	convs, _ := f.a.DB.C(store.CConversations).CountDocuments(ctx, bson.M{"workspaceId": f.a.WorkspaceID()})
	clips, _ := f.a.DB.C(store.CAssets).CountDocuments(ctx, bson.M{"workspaceId": f.a.WorkspaceID(), "tags": "screen-recording"})
	if got.State != models.RecordingReady || audits != 1 || convs != 1 || clips != 3 || got.AddingBy != "" || got.AddingAt != nil {
		t.Fatalf("state %s, %d finishes ran, %d conversations, %d clips, lease %q", got.State, audits, convs, clips, got.AddingBy)
	}
}

// A finishing process that died leaves a lease nobody renews: a later finish
// (or the daemon's recovery) takes it over, and a stale one can be thrown
// away; a fresh lease is left alone, and a process that lost its lease
// writes nothing more.
func TestADeadFinishIsTakenOver(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	stuck := func() *models.Recording {
		r := f.create(t, "voice")
		f.send(t, r.ID, "voice", f.voice)
		old := time.Now().UTC().Add(-10 * time.Minute)
		r.State, r.AddingAt, r.AddingBy = models.RecordingAdding, &old, "a-process-that-died"
		if _, err := f.a.DB.C(store.CRecordings).ReplaceOne(ctx, bson.M{"_id": r.ID}, r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := stuck()
	out, err := Finish(ctx, f.a, &RecordingFinishIn{Recording: r.ID, Keep: true})
	if err != nil || out.Recording.State != models.RecordingReady || out.Recording.Track("voice").AssetID == "" {
		t.Fatalf("taking over a dead finish: %v %+v", err, out)
	}
	// The daemon's recovery does the same once the lease is stale.
	r2 := stuck()
	as := func(u, n string) *app.App { return actingAs(f.a, models.Actor{UserID: u, UserName: n}) }
	if !takeOver(ctx, f.a, r2.ID, as) {
		t.Fatal("recovery didn't take a stale lease over")
	}
	if got, _ := get(ctx, f.a, r2.ID); got.State != models.RecordingReady {
		t.Fatalf("recovered: %s", got.State)
	}
	// A stale one can be thrown away; a fresh one can't (its finish is alive).
	r3 := stuck()
	if _, err := ops.CallTyped[RecordingInfo](ctx, f.a, "recording.discard", RecordingRefIn{Recording: r3.ID}, ops.Caller{Human: true}); err != nil {
		t.Fatalf("discarding a stuck recording: %v", err)
	}
	r4 := stuck()
	now := time.Now().UTC()
	_, _ = f.a.DB.C(store.CRecordings).UpdateOne(ctx, bson.M{"_id": r4.ID}, bson.M{"$set": bson.M{"addingAt": now}})
	if out, err := Finish(ctx, f.a, &RecordingFinishIn{Recording: r4.ID, Keep: true}); err != nil || out.Recording.State != models.RecordingAdding {
		t.Fatalf("a live finish is left alone: %v %+v", err, out)
	}
	if _, err := ops.CallTyped[RecordingInfo](ctx, f.a, "recording.discard", RecordingRefIn{Recording: r4.ID}, ops.Caller{Human: true}); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("discarding while a finish is alive: %v", err)
	}
	if takeOver(ctx, f.a, r4.ID, as) {
		t.Fatal("recovery took a live lease over")
	}
	// A process whose lease was taken writes nothing more.
	r4.AddingBy = "an-older-lease"
	if err := put(ctx, f.a, r4); err != errLeaseLost {
		t.Fatalf("a lost lease wrote: %v", err)
	}
}

// Only the person recording it, or an owner, ends a recording that isn't
// footage yet; its footage stays theirs; others see it once it is footage.
func TestOnlyTheMakerOrAnOwnerEndsARecording(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	bob := *f.a
	bob.Actor.UserID, bob.Actor.UserName = ids.New(ids.User), "Bob"
	member := ops.OverHTTP(ctx, func() bool { return false })
	owner := ops.OverHTTP(ctx, func() bool { return true })
	r := f.create(t, "voice")
	f.send(t, r.ID, "voice", f.voice[:len(f.voice)/2])
	if _, err := ops.CallTyped[RecordingInfo](member, &bob, "recording.finish", RecordingFinishIn{Recording: r.ID, Keep: true}, ops.Caller{}); !apperr.Is(err, apperr.Unauthorized) {
		t.Fatalf("another member finished it: %v", err)
	}
	if _, err := ops.CallTyped[RecordingInfo](member, &bob, "recording.discard", RecordingRefIn{Recording: r.ID}, ops.Caller{Human: true}); !apperr.Is(err, apperr.Unauthorized) {
		t.Fatalf("another member threw it away: %v", err)
	}
	if _, err := ops.CallTyped[RecordingInfo](member, &bob, "recording.show", RecordingRefIn{Recording: r.ID}, ops.Caller{}); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("another member sees an unfinished recording: %v", err)
	}
	if lo, _ := ops.CallTyped[RecordingListOut](member, &bob, "recording.list", RecordingListIn{}, ops.Caller{}); len(lo.Recordings) != 0 {
		t.Fatalf("another member lists it: %d", len(lo.Recordings))
	}
	// An owner may keep what arrived; the footage is still the maker's.
	out, err := ops.CallTyped[RecordingInfo](owner, &bob, "recording.finish", RecordingFinishIn{Recording: r.ID, Keep: true}, ops.Caller{})
	if err != nil || out.Recording.State != models.RecordingReady {
		t.Fatalf("an owner keeping it: %v", err)
	}
	var as models.Asset
	_ = f.a.DB.GetByID(ctx, store.CAssets, f.a.WorkspaceID(), out.Recording.Track("voice").AssetID, &as)
	if as.CreatedBy.UserID != f.a.Actor.UserID {
		t.Fatalf("the footage is credited to %q, not its maker", as.CreatedBy.UserName)
	}
	// Footage is everyone's to see.
	if lo, _ := ops.CallTyped[RecordingListOut](member, &bob, "recording.list", RecordingListIn{}, ops.Caller{}); len(lo.Recordings) != 1 {
		t.Fatalf("another member doesn't see the footage: %d", len(lo.Recordings))
	}
	// The maker ends their own.
	r2 := f.create(t, "voice")
	if _, err := ops.CallTyped[RecordingInfo](member, f.a, "recording.discard", RecordingRefIn{Recording: r2.ID}, ops.Caller{Human: true}); err != nil {
		t.Fatalf("the maker throwing theirs away: %v", err)
	}
}

// A recording holds at most so much, pieces never take the disk below the
// floor, and pieces of a recording nobody finished go after a warning.
func TestPiecesHaveACapAFloorAndAnExpiry(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	saveMax, saveFree := maxBytes, freeSpace
	t.Cleanup(func() { maxBytes, freeSpace = saveMax, saveFree })
	maxBytes = func(*app.App) int64 { return 50_000 }
	r := f.create(t, "voice")
	f.send(t, r.ID, "voice", f.voice[:40_000])
	_, err := Append(ctx, f.a, r.ID, "voice", 40_000, 20_000, strings.NewReader(string(f.voice[40_000:60_000])))
	if ae := apperr.From(err); ae.Code != apperr.Conflict || !strings.Contains(ae.Message, "the most one recording keeps") {
		t.Fatalf("past the cap: %v", err)
	} else if d, _ := ae.Details.(map[string]any); d["reason"] != "full" {
		t.Fatalf("details %v", ae.Details)
	}
	if Offset(f.a, r.ID, "voice") != 40_000 {
		t.Fatal("a piece past the cap was written")
	}
	maxBytes = saveMax
	freeSpace = func(string) int64 { return 1 << 30 } // 1 GB free, under the 2 GB floor
	if _, err := Append(ctx, f.a, r.ID, "voice", 40_000, 1000, strings.NewReader(string(f.voice[40_000:41_000]))); !apperr.Is(err, apperr.Storage) || !strings.Contains(err.Error(), "nearly out of disk space") {
		t.Fatalf("under the free-space floor: %v", err)
	}
	freeSpace = saveFree

	// Untouched for 12 days: its maker is told; at 14 days, a day after, its pieces go.
	as := func(u, n string) *app.App { return actingAs(f.a, models.Actor{UserID: u, UserName: n}) }
	idle := r.ID
	live := f.create(t, "voice")
	f.send(t, live.ID, "voice", f.voice[:1000])
	long := time.Now().UTC().Add(-13 * 24 * time.Hour)
	_, _ = f.a.DB.C(store.CRecordings).UpdateOne(ctx, bson.M{"_id": idle}, bson.M{"$set": bson.M{"lastPieceAt": long, "updatedAt": long}})
	now := time.Now()
	told, removed := sweepOnce(ctx, f.a, as, now)
	if told != 1 || removed != 0 {
		t.Fatalf("day 13: told %d, removed %d", told, removed)
	}
	var n models.Notification
	if err := f.a.DB.C(store.CNotifications).FindOne(ctx, bson.M{"groupKey": "recording_expiring:" + idle}).Decode(&n); err != nil || n.UserID != f.a.Actor.UserID || !strings.Contains(n.Title, "removed in") {
		t.Fatalf("the maker wasn't told: %v %+v", err, n)
	}
	if told, removed := sweepOnce(ctx, f.a, as, now.Add(time.Hour)); told != 0 || removed != 0 {
		t.Fatalf("told twice, or removed too soon: %d %d", told, removed)
	}
	if _, err := os.Stat(dir(f.a, idle)); err != nil {
		t.Fatal("pieces went before the day after the warning")
	}
	if _, removed := sweepOnce(ctx, f.a, as, now.Add(25*time.Hour)); removed != 1 {
		t.Fatalf("day 14: removed %d", removed)
	}
	got, _ := get(ctx, f.a, idle)
	if got.State != models.RecordingDiscarded || got.Ended != "expired" || !strings.Contains(got.Error, "removed after 14 days") {
		t.Fatalf("expired: %s %s %q", got.State, got.Ended, got.Error)
	}
	if _, err := os.Stat(dir(f.a, idle)); !os.IsNotExist(err) {
		t.Fatal("the pieces are still there")
	}
	if l, _ := get(ctx, f.a, live.ID); l.State != models.RecordingRecording {
		t.Fatalf("a live recording was touched: %s", l.State)
	}
}

// Archived projects take no recordings; the sound may not move a track far
// from a clock that was measured (it matched the wrong moment), and the
// recording says why it kept the clock.
func TestArchivedProjectsAndFarSoundMoves(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.create(t, "camera", "voice")
	f.send(t, r.ID, "camera", f.camera)
	f.send(t, r.ID, "voice", f.voice)
	c := clockFor(f, "camera", "voice")
	c.Tracks[1].Start = 0.9 // the clock says 0.9 s; the sound says 0.1 s
	out, err := Finish(ctx, f.a, &RecordingFinishIn{Recording: r.ID, Clock: c})
	if err != nil || out.Recording.State != models.RecordingReady {
		t.Fatalf("finish: %v", err)
	}
	var conv models.Conversation
	_ = f.a.DB.GetByID(ctx, store.CConversations, f.a.WorkspaceID(), out.Recording.Links[0].ConversationID, &conv)
	cam, _ := conv.Member("camera")
	voice, _ := conv.Member("voice")
	if conv.Sync.Method != "clock" || math.Abs(voice.Start-cam.Start-0.9) > 0.001 {
		t.Fatalf("the sound moved a measured clock by 0.8 s: %+v", conv.Sync)
	}
	if got, _ := get(ctx, f.a, r.ID); !strings.Contains(got.Lined, "more than the clock can be off") {
		t.Fatalf("why the clock stayed: %q", got.Lined)
	}
	// An archived project takes no link and no cut.
	pid := ids.New(ids.Project)
	now := time.Now().UTC()
	_ = f.a.DB.Insert(ctx, store.CProjects, models.Project{ID: pid, WorkspaceID: f.a.WorkspaceID(), Name: "Old launch", Archived: true, Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now})
	if _, err := ops.CallTyped[RecordingInfo](ctx, f.a, "recording.link", RecordingLinkIn{Recording: r.ID, Project: pid}, ops.Caller{}); !apperr.Is(err, apperr.InvalidInput) && !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("linked into an archived project: %v", err)
	}
	if _, err := ops.CallTyped[RecordingCutOut](ctx, f.a, "recording.cut", RecordingCutIn{Recording: r.ID, Project: pid}, ops.Caller{}); !apperr.Is(err, apperr.InvalidInput) && !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("a cut in an archived project: %v", err)
	}
	if got, _ := get(ctx, f.a, r.ID); got.LinkIn(pid) != nil {
		t.Fatal("linked into an archived project")
	}
}

// A finishing process that lost its lease and carries on (a laptop waking
// from sleep) never touches what the one that took over uses: one clip per
// track, every track footage, the pieces removed only once the record says so.
func TestALoserLeavesTheTakersWorkAlone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	saveS, saveR := LeaseStale, LeaseRenew
	t.Cleanup(func() { LeaseStale, LeaseRenew = saveS, saveR })
	LeaseStale, LeaseRenew = 200*time.Millisecond, time.Hour // the loser never renews in time
	r := f.create(t, "screen", "camera", "voice")
	for _, tr := range []string{"screen", "camera", "voice"} {
		f.send(t, r.ID, tr, f.bytesOf(tr))
	}
	if ok, err := claim(ctx, f.a, r); !ok || err != nil {
		t.Fatal(ok, err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); makeFootage(ctx, f.a, r) }()
	time.Sleep(350 * time.Millisecond) // the loser's lease is stale now; it is still adding the screen
	out, err := Finish(ctx, f.a, &RecordingFinishIn{Recording: r.ID, Keep: true})
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := get(ctx, f.a, r.ID)
	if got.State != models.RecordingReady || got.Error != "" || out.Recording.State != models.RecordingReady {
		t.Fatalf("taken over: stored %s %q; the taker answered %s", got.State, got.Error, out.Recording.State)
	}
	for _, tr := range got.Tracks {
		n, _ := f.a.DB.C(store.CAssets).CountDocuments(ctx, bson.M{"workspaceId": f.a.WorkspaceID(), "tags": bson.M{"$all": bson.A{"screen-recording", tr.ID}}})
		if tr.AssetID == "" || tr.State != "ready" || n != 1 {
			t.Fatalf("%s: %s %q, %d clips", tr.ID, tr.State, tr.Error, n)
		}
	}
	if convs, _ := f.a.DB.C(store.CConversations).CountDocuments(ctx, bson.M{"workspaceId": f.a.WorkspaceID()}); convs != 1 {
		t.Fatalf("%d conversations", convs)
	}
	if _, err := os.Stat(dir(f.a, r.ID)); !os.IsNotExist(err) {
		t.Fatal("pieces left after it became footage")
	}
}

// A finish that died after adding the screen but before the record said so:
// the one taking over makes the same file (bit-exact) and uses that clip.
func TestATakeOverMidIngestMakesNoSecondClip(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	r := f.create(t, "screen", "voice")
	f.send(t, r.ID, "screen", f.screen)
	f.send(t, r.ID, "voice", f.voice)
	if ok, err := claim(ctx, f.a, r); !ok || err != nil {
		t.Fatal(ok, err)
	}
	work := workDir(f.a, r)
	_ = os.MkdirAll(work, 0o700)
	first, err := trackFootage(ctx, f.a, r, &r.Tracks[0], work)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-10 * time.Minute)
	_, _ = f.a.DB.C(store.CRecordings).UpdateOne(ctx, bson.M{"_id": r.ID}, bson.M{"$set": bson.M{"addingAt": old}})
	out, err := Finish(ctx, f.a, &RecordingFinishIn{Recording: r.ID, Keep: true})
	if err != nil || out.Recording.State != models.RecordingReady {
		t.Fatalf("taking over: %v", err)
	}
	n, _ := f.a.DB.C(store.CAssets).CountDocuments(ctx, bson.M{"workspaceId": f.a.WorkspaceID(), "tags": "screen"})
	if n != 1 || out.Recording.Track("screen").AssetID != first {
		t.Fatalf("%d screen clips; the recording's is %s, the first was %s", n, out.Recording.Track("screen").AssetID, first)
	}
}

// The expiry never throws away a recording that is partly footage: its clips
// stay, the rest's pieces go; and it acts only on what it read (a person
// resuming meanwhile wins).
func TestTheSweepKeepsFootageAndYieldsToAPerson(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	as := func(u, n string) *app.App { return actingAs(f.a, models.Actor{UserID: u, UserName: n}) }
	r := f.create(t, "camera", "voice")
	f.send(t, r.ID, "camera", f.camera)
	f.send(t, r.ID, "voice", f.voice)
	work := filepath.Join(dir(f.a, r.ID), "w")
	_ = os.MkdirAll(work, 0o700)
	voiceClip, err := trackFootage(ctx, f.a, r, r.Track("voice"), work)
	if err != nil {
		t.Fatal(err)
	}
	long := time.Now().UTC().Add(-13 * 24 * time.Hour).Truncate(time.Millisecond)
	r.State = models.RecordingFailed
	r.Track("voice").State, r.Track("voice").AssetID = "ready", voiceClip
	r.LastPieceAt, r.UpdatedAt = long, long
	_, _ = f.a.DB.C(store.CRecordings).ReplaceOne(ctx, bson.M{"_id": r.ID}, r)
	now := time.Now()
	sweepOnce(ctx, f.a, as, now)
	if _, removed := sweepOnce(ctx, f.a, as, now.Add(25*time.Hour)); removed != 1 {
		t.Fatalf("removed %d", removed)
	}
	got, _ := get(ctx, f.a, r.ID)
	if got.State != models.RecordingReady || got.Track("voice").AssetID != voiceClip || got.Track("camera").State != "failed" || !strings.Contains(got.Error, "the camera") || len(got.Links) != 1 {
		t.Fatalf("partly footage: %s %q %+v links %d", got.State, got.Error, got.Tracks, len(got.Links))
	}
	if _, err := os.Stat(dir(f.a, r.ID)); !os.IsNotExist(err) {
		t.Fatal("the unfinished pieces are still there")
	}
	// Touched since it was read: the expiry yields.
	r2 := f.create(t, "voice")
	f.send(t, r2.ID, "voice", f.voice[:1000])
	read, _ := get(ctx, f.a, r2.ID)
	_, _ = f.a.DB.C(store.CRecordings).UpdateOne(ctx, bson.M{"_id": r2.ID}, bson.M{"$set": bson.M{"lastPieceAt": time.Now().UTC().Add(time.Second)}})
	if err := throwAway(ctx, f.a, read, "expired", "x", bson.M{"lastPieceAt": read.LastPieceAt, "updatedAt": read.UpdatedAt}); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("expired a recording someone touched: %v", err)
	}
	if again, _ := get(ctx, f.a, r2.ID); again.State != models.RecordingRecording {
		t.Fatalf("state %s", again.State)
	}
}

// Out of disk space: a member is told whom to ask; an owner is told to free space.
func TestTheFreeSpaceHintNamesTheOwners(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	saveFree, saveNames := freeSpace, ops.OwnerNames
	t.Cleanup(func() { freeSpace, ops.OwnerNames = saveFree, saveNames })
	freeSpace = func(string) int64 { return 1 << 20 }
	ops.OwnerNames = func(context.Context, *app.App) []string { return []string{"Alice", "Sam"} }
	r := f.create(t, "voice")
	member := ops.OverHTTP(ctx, func() bool { return false })
	_, err := Append(member, f.a, r.ID, "voice", 0, 10, strings.NewReader("0123456789"))
	if ae := apperr.From(err); ae.Code != apperr.Storage || !strings.Contains(ae.Hint, "Ask Alice or Sam to free some space on test-machine") {
		t.Fatalf("a member's hint: %+v", ae)
	}
	_, err = Append(ctx, f.a, r.ID, "voice", 0, 10, strings.NewReader("0123456789"))
	if ae := apperr.From(err); !strings.HasPrefix(ae.Hint, "Free some space on test-machine") {
		t.Fatalf("an owner's hint: %q", ae.Hint)
	}
}

// An AI app that only proposes can't make a recording footage, link it or
// cut from it: each is refused as a draft with a plain reason.
func TestRecordingChangesAreNoDrafts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for op, want := range map[string]string{
		"recording.finish":  "who recorded it",
		"recording.link":    "which a person decides",
		"recording.cut":     "which a person decides",
		"recording.discard": "Only a person",
	} {
		o, _ := ops.Get(op)
		if ops.Draftable(o) {
			t.Fatalf("%s can be a draft", op)
		}
		_, err := ops.CallTyped[ops.DraftView](ctx, f.a, "draft.save", ops.DraftSaveIn{Op: op, Input: []byte(`{"recording":"rcd_01m5t7ecgkst3011mjvy99g0jc"}`)}, ops.Caller{})
		if ae := apperr.From(err); err == nil || !strings.Contains(ae.Message, want) {
			t.Fatalf("%s as a draft: %v", op, err)
		}
	}
}
