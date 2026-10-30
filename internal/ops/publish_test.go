package ops_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/publish"
	"github.com/metavert-io/metarush/internal/receipt"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/fakeyoutube"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// finalRenderOf stores a finished final render of a version (random bytes).
func finalRenderOf(t *testing.T, a *app.App, revisionID string) (models.Render, []byte) {
	t.Helper()
	ctx := context.Background()
	var r models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), revisionID, &r); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 96<<10)
	_, _ = rand.Read(b)
	p := filepath.Join(t.TempDir(), "final.mp4")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	sha, _ := media.HashFile(p)
	key := media.ObjectKey(a.WorkspaceID(), sha)
	if _, err := a.Media.PutFile(ctx, key, p, media.PutOptions{SHA256: sha}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rd := models.Render{ID: ids.New(ids.Render), WorkspaceID: a.WorkspaceID(), ProjectID: r.ProjectID, RevisionID: r.ID, RevisionNumber: r.Number, SpecHash: r.SpecHash,
		Preset: r.Spec.Output.Preset, Quality: "final", State: models.RenderAvailable, CreatedAt: now, CompletedAt: &now,
		Output: &models.RenderOutput{StorageKey: key, SHA256: sha, Size: int64(len(b)), Duration: 3, Width: 1080, Height: 1920, FPS: 30}}
	if err := a.DB.Insert(ctx, store.CRenders, rd); err != nil {
		t.Fatal(err)
	}
	return rd, b
}

// readyToPost is a version approved, rendered and with delivery notes, with
// YouTube set up and the person signed in: ready to post.
func readyToPost(t *testing.T) (*app.App, *fakeyoutube.Server, ops.PublishIn, string, models.Render, []byte) {
	t.Helper()
	keyring.MockInit()
	yt := fakeyoutube.New(t)
	t.Setenv(publish.EnvYouTubeBase, yt.Base())
	ops.PublishOpener = func(_ context.Context, u string) (func(), error) { return func() {}, fakeyoutube.Consent(u) }
	t.Cleanup(func() { ops.PublishOpener = nil })
	a := testapp.New(t)
	p := project(t, a, "Garden story")
	as := txClip(t, a, "Interview", p.ID, []models.Word{wd("We", 0.1, 0.3, 0.9), wd("met", 0.35, 0.6, 0.9), wd("in", 0.7, 0.9, 0.9), wd("Elmwood.", 1.0, 2.2, 0.9)})
	rid := captionedCut(t, a, p.ID, as.ID)
	do[ops.PublishConnectOut](t, a, "publish.setup", ops.PublishSetupIn{Platform: "youtube", ClientID: yt.ClientID, ClientSecret: yt.Secret})
	do[ops.PublishConnectOut](t, a, "publish.connect", ops.PublishConnectIn{Platform: "youtube"})
	do[ops.RevisionSummary](t, a, "revision.approve", ops.RevisionApproveIn{ID: rid})
	rd, video := finalRenderOf(t, a, rid)
	do[ops.DeliveryNotesOut](t, a, "delivery.notes.set", ops.DeliveryNotesSetIn{ProjectID: p.ID, Notes: ops.DeliveryNotesDraft{Title: "The garden at Elmwood", Description: "A walk."}})
	in := ops.PublishIn{ProjectID: p.ID, Platform: "youtube"}
	if plan := do[ops.PublishPlanOut](t, a, "publish.plan", in); !plan.Ready {
		t.Fatalf("not ready: %s", holdsSay(plan))
	}
	return a, yt, in, rid, rd, video
}

func holdsSay(p *ops.PublishPlanOut) string {
	var s []string
	for _, h := range p.Holds {
		s = append(s, h.Says)
	}
	return strings.Join(s, " | ")
}

// Posting to YouTube (D305): held until it is set up, signed in, approved and
// rendered; then exactly the plan is sent as the person who signed in, with the
// delivery notes' words and chapters and the captions, and recorded on the
// version. Only a person does it: never a caller that isn't one, never Claude.
func TestPublishYouTube(t *testing.T) {
	keyring.MockInit() // never the real keychain
	yt := fakeyoutube.New(t)
	t.Setenv(publish.EnvYouTubeBase, yt.Base())
	ops.PublishOpener = func(_ context.Context, u string) (func(), error) { return func() {}, fakeyoutube.Consent(u) }
	t.Cleanup(func() { ops.PublishOpener = nil })
	a := testapp.New(t)
	ctx := context.Background()
	p := project(t, a, "Garden story")
	as := txClip(t, a, "Interview", p.ID, []models.Word{wd("We", 0.1, 0.3, 0.9), wd("met", 0.35, 0.6, 0.9), wd("in", 0.7, 0.9, 0.9), wd("Elmwood.", 1.0, 2.2, 0.9)})
	rid := captionedCut(t, a, p.ID, as.ID)
	in := ops.PublishIn{ProjectID: p.ID, Platform: "youtube"}

	plan := do[ops.PublishPlanOut](t, a, "publish.plan", in)
	says := holdsSay(plan)
	if plan.Ready || !strings.Contains(says, "isn't set up on this computer") || !strings.Contains(says, "isn't approved yet") || !strings.Contains(says, "no final render") {
		t.Fatalf("holds: %s", says)
	}
	for _, s := range append([]string{plan.Summary}, plan.Notes...) {
		if err := plainwords.CheckShown(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	// An owner sets it up; the secret never comes from the command line (the CLI asks for it).
	do[ops.PublishConnectOut](t, a, "publish.setup", ops.PublishSetupIn{Platform: "youtube", ClientID: yt.ClientID, ClientSecret: yt.Secret})
	if err := doErr(a, "publish.connect", ops.PublishConnectIn{Platform: "youtube"}, ops.Caller{}); code(err) != "E_HUMAN_ONLY" {
		t.Fatalf("signing in for someone: %v", err)
	}
	conn := do[ops.PublishConnectOut](t, a, "publish.connect", ops.PublishConnectIn{Platform: "youtube"})
	if conn.As == nil || conn.As.Channel != "Garden Club" {
		t.Fatalf("connect %+v", conn)
	}
	plats := do[ops.PublishPlatformsOut](t, a, "publish.platforms", ops.PublishPlatformsIn{})
	if len(plats.Platforms) != 1 || !plats.Platforms[0].SetUp || plats.Platforms[0].As == nil {
		t.Fatalf("platforms %+v", plats)
	}

	do[ops.RevisionSummary](t, a, "revision.approve", ops.RevisionApproveIn{ID: rid})
	_, video := finalRenderOf(t, a, rid)
	do[ops.DeliveryNotesOut](t, a, "delivery.notes.set", ops.DeliveryNotesSetIn{ProjectID: p.ID, Notes: ops.DeliveryNotesDraft{Title: "The garden at Elmwood",
		Description: "A walk through the garden.", Chapters: []models.Chapter{{At: 0, Title: "We meet"}, {At: 1, Title: "Elmwood"}}}})
	plan = do[ops.PublishPlanOut](t, a, "publish.plan", in)
	if !plan.Ready || plan.Title != "The garden at Elmwood" || !strings.Contains(plan.Description, "A walk through the garden.\n\n0:00 We meet\n0:01 Elmwood") ||
		plan.Privacy != "private" || len(plan.Captions) != 1 || plan.Captions[0].Language != "en" || plan.As == nil || plan.Fingerprint == "" {
		t.Fatalf("plan %+v (holds %s)", plan, holdsSay(plan))
	}

	// Never anything but a person.
	if err := doErr(a, "publish.send", in, ops.Caller{}); code(err) != "E_HUMAN_ONLY" {
		t.Fatalf("sent for nobody: %v", err)
	}
	claude := *a
	claude.Actor.Via = models.ViaAssistant
	if err := doErr(&claude, "publish.send", in, person); err == nil {
		t.Fatal("Claude posted a video")
	}
	agent := *a
	agent.Actor.Agent = "codex"
	if err := doErr(&agent, "publish.send", in, person); code(err) != "E_HUMAN_ONLY" {
		t.Fatalf("an AI app posted a video: %v", err)
	}
	// What was looked at is what is sent.
	stale := in
	stale.Fingerprint = "0000000000000000"
	if err := doErr(a, "publish.send", stale, person); code(err) != "E_CONFLICT" {
		t.Fatalf("a stale plan: %v", err)
	}
	send := in
	send.Fingerprint = plan.Fingerprint
	out := do[ops.PublishSendOut](t, a, "publish.send", send)
	if out.Publication.State != models.PublicationPosted || out.Publication.Channel != "Garden Club" || !strings.HasPrefix(out.Publication.URL, "https://www.youtube.com/watch?v=") {
		t.Fatalf("sent %+v", out.Publication)
	}
	if len(yt.Videos) != 1 || !bytes.Equal(yt.Videos[0].Bytes, video) || yt.Videos[0].Title != "The garden at Elmwood" || yt.Videos[0].Privacy != "private" ||
		len(yt.Videos[0].Captions) != 1 || !strings.Contains(yt.Videos[0].Captions[0].Content, "Elmwood") {
		t.Fatalf("on YouTube: %+v", yt.Videos)
	}
	var r models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &r)
	if len(r.Publications) != 1 || r.Publications[0].State != models.PublicationPosted || r.Publications[0].VideoID != yt.Videos[0].ID || r.Publications[0].By.UserID != a.Actor.UserID {
		t.Fatalf("recorded %+v", r.Publications)
	}
	again := do[ops.PublishPlanOut](t, a, "publish.plan", in)
	if len(again.Earlier) != 1 {
		t.Fatalf("earlier posts %+v", again.Earlier)
	}
	// The receipt says so.
	rc, err := ops.CallTyped[receipt.ProjectReceiptOut](ctx, a, "project.receipt", receipt.ProjectReceiptIn{ProjectID: p.ID}, person)
	if err != nil || len(rc.Posted) != 1 || !strings.Contains(strings.Join(rc.Lines, " "), "Version 1 was posted to YouTube as “Garden Club” (private)") {
		t.Fatalf("receipt %+v %v", rc, err)
	}
	// Already there: a second video only when a person says so.
	if again.Posted == nil || !strings.Contains(strings.Join(again.Notes, " "), "Posting it again makes a second video there") {
		t.Fatalf("already posted: %+v", again)
	}
	send.Fingerprint = again.Fingerprint
	if err := doErr(a, "publish.send", send, person); code(err) != "E_CONFLICT" || !strings.Contains(err.Error(), "already on YouTube") || len(yt.Videos) != 1 {
		t.Fatalf("posted again without asking: %v", err)
	}
	// A refusal at YouTube is recorded too.
	yt.Quota = true
	send.Again = true
	if err := doErr(a, "publish.send", send, person); err == nil || !strings.Contains(err.Error(), "daily allowance") {
		t.Fatalf("quota: %v", err)
	}
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &r)
	if len(r.Publications) != 2 || r.Publications[1].State != models.PublicationFailed {
		t.Fatalf("failed post recorded %+v", r.Publications)
	}
	// Signing out takes the permission back.
	do[ops.PublishConnectOut](t, a, "publish.connect", ops.PublishConnectIn{Platform: "youtube", Forget: true})
	if p := do[ops.PublishPlanOut](t, a, "publish.plan", in); !strings.Contains(holdsSay(p), "aren't signed in") || len(yt.Revoked) != 1 {
		t.Fatalf("after signing out: %s", holdsSay(p))
	}
}

// Two tabs, a double click: a render goes to a channel once. The check and
// the new entry are one write, so one post goes out and the other is refused;
// while it goes, the plan shows it going out (and how much has gone) and
// holds another.
func TestPublishYouTubeOnlyOnce(t *testing.T) {
	a, yt, in, rid, rd, _ := readyToPost(t)
	ctx := context.Background()
	plan := do[ops.PublishPlanOut](t, a, "publish.plan", in)
	in.Fingerprint = plan.Fingerprint
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { errs <- doErr(a, "publish.send", in, person) }()
	}
	var refused []error
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			refused = append(refused, err)
		}
	}
	if len(yt.Videos) != 1 || len(refused) != 1 || code(refused[0]) != "E_CONFLICT" {
		t.Fatalf("%d videos, refused %v", len(yt.Videos), refused)
	}
	var r models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &r)
	if len(r.Publications) != 1 {
		t.Fatalf("recorded %+v", r.Publications)
	}

	// One going out (from another tab or a terminal): shown with its progress, and holding another.
	beat := time.Now().UTC()
	going := models.Publication{ID: "pub_going", Platform: "youtube", State: models.PublicationSending, Channel: "Garden Club", ChannelID: "UCfake", RenderID: rd.ID,
		SHA256: rd.Output.SHA256, Size: 1000, Sent: 400, Installation: a.Cfg.Installation.ID, Beat: &beat, At: beat}
	if _, err := a.DB.C(store.CRevisions).UpdateOne(ctx, map[string]any{"_id": rid}, map[string]any{"$push": map[string]any{"publications": going}}); err != nil {
		t.Fatal(err)
	}
	plan = do[ops.PublishPlanOut](t, a, "publish.plan", in)
	if plan.Ready || plan.Sending == nil || plan.Sending.Sent != 400 || !strings.Contains(plan.Summary, "is being posted to YouTube as “Garden Club” now (40% sent)") ||
		len(plan.Holds) != 1 || !plan.Holds[0].Sending {
		t.Fatalf("plan while sending: %+v", plan)
	}
	in.Again, in.Fingerprint = true, plan.Fingerprint
	if err := doErr(a, "publish.send", in, person); code(err) != "E_INVALID_INPUT" && code(err) != "E_CONFLICT" || len(yt.Videos) != 1 {
		t.Fatalf("sent while another was going out: %v", err)
	}
	if err := plainwords.CheckShown(plan.Summary); err != nil {
		t.Error(err)
	}
}

// MetaRush stopped part-way through a post (the computer went to sleep, the
// daemon was restarted): the daemon's look goes on from where YouTube got to
// with the session this computer kept, and records the video; a post with no
// session kept is marked as not posted in plain words, another computer's
// only after a day, and one still going is left alone.
func TestPublishGoesOnAfterAStop(t *testing.T) {
	a, yt, in, rid, rd, video := readyToPost(t)
	ctx := context.Background()
	old := publish.UploadChunk
	publish.UploadChunk = 32 << 10
	t.Cleanup(func() { publish.UploadChunk = old })
	person := a.Actor.UserID
	if person == "" {
		person = "owner"
	}
	// The part that went before MetaRush stopped.
	local := filepath.Join(t.TempDir(), "final.mp4")
	if err := os.WriteFile(local, video, 0o600); err != nil {
		t.Fatal(err)
	}
	session := ""
	stopped, stop := context.WithCancel(ctx)
	v := publish.Video{Path: local, Size: int64(len(video)), Title: "The garden at Elmwood", Privacy: "private", Started: func(s string) { session = s }}
	_, _ = publish.UploadYouTube(publish.WithProgress(stopped, func(sent, total int64) {
		if sent > 0 {
			stop()
		}
	}), a.Cfg, person, v)
	if session == "" || len(yt.Videos) != 0 {
		t.Fatalf("session %q, %d videos", session, len(yt.Videos))
	}
	long := time.Now().UTC().Add(-10 * time.Minute)
	add := func(id, installation string, beat time.Time) {
		x := models.Publication{ID: id, Platform: "youtube", State: models.PublicationSending, Channel: "Garden Club", ChannelID: "UCfake", RenderID: rd.ID,
			SHA256: rd.Output.SHA256, Size: rd.Output.Size, Sent: 32 << 10, Installation: installation, Beat: &beat, At: beat, By: a.Actor}
		if _, err := a.DB.C(store.CRevisions).UpdateOne(ctx, map[string]any{"_id": rid}, map[string]any{"$push": map[string]any{"publications": x}}); err != nil {
			t.Fatal(err)
		}
	}
	add("pub_stopped", a.Cfg.Installation.ID, long)
	j := &ops.PublishJournalForTest{Publication: "pub_stopped", Revision: rid, Project: in.ProjectID, Number: 1, Person: person, RenderKey: rd.Output.StorageKey,
		SHA256: rd.Output.SHA256, Size: rd.Output.Size, Session: session, Video: publish.Video{Title: "The garden at Elmwood", Privacy: "private"}}
	if err := ops.SavePublishJournal(a, j); err != nil {
		t.Fatal(err)
	}
	add("pub_lost", a.Cfg.Installation.ID, long)                        // no place kept
	add("pub_elsewhere", "ins_other", time.Now().UTC().Add(-time.Hour)) // another computer, not a day yet
	add("pub_going", a.Cfg.Installation.ID, time.Now().UTC())           // still going
	if st, err := os.Stat(filepath.Join(a.Cfg.StateDir(), "publish", "pub_stopped.json")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("journal %v %v", st, err)
	}

	said := ops.RecoverPublications(ctx, a, time.Now().UTC())
	var r models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &r)
	by := map[string]models.Publication{}
	for _, x := range r.Publications {
		by[x.ID] = x
	}
	if x := by["pub_stopped"]; x.State != models.PublicationPosted || len(yt.Videos) != 1 || x.VideoID != yt.Videos[0].ID || !bytes.Equal(yt.Videos[0].Bytes, video) {
		t.Fatalf("went on: %+v (%d videos) said %v", x, len(yt.Videos), said)
	}
	if ops.PublishJournalKept(a, "pub_stopped") {
		t.Fatal("the journal outlived the post")
	}
	if x := by["pub_lost"]; x.State != models.PublicationFailed || !strings.Contains(x.Error, "kept no place to go on from") {
		t.Fatalf("lost: %+v", x)
	}
	if by["pub_elsewhere"].State != models.PublicationSending || by["pub_going"].State != models.PublicationSending {
		t.Fatalf("left alone: %+v %+v", by["pub_elsewhere"], by["pub_going"])
	}
	for _, s := range append(said, by["pub_lost"].Error) {
		if err := plainwords.CheckShown(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	// A day later the other computer's is given up too.
	said = ops.RecoverPublications(ctx, a, time.Now().UTC().Add(25*time.Hour))
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &r)
	for _, x := range r.Publications {
		if x.ID == "pub_elsewhere" && (x.State != models.PublicationFailed || !strings.Contains(x.Error, "another computer")) {
			t.Fatalf("elsewhere after a day: %+v (%v)", x, said)
		}
	}
}

// A post goes out like a delivery: the ship gate holds it while the quality
// check or a sign-off holds the version, until a person sends it out anyway
// with a note (and then the version says it went out, by a post); a client's
// rules keep it from a platform they don't allow — said in the plan, recorded
// when a post is refused.
func TestPublishWaitsForTheShipGateAndClientRules(t *testing.T) {
	a, yt, in, rid, _, _ := readyToPost(t)
	ctx := context.Background()
	if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: a.WorkspaceID(), WorkspaceID: a.WorkspaceID(), Name: "T"}); err != nil {
		t.Fatal(err)
	}
	do[ops.GateSettingsOut](t, a, "delivery.gate.set", ops.GateSetIn{On: true})
	plan := do[ops.PublishPlanOut](t, a, "publish.plan", in)
	if plan.Ready || len(plan.Holds) == 0 || !strings.Contains(plan.Holds[0].Command, "metarush deliver ready") || !plan.Holds[0].People {
		t.Fatalf("held by the gate: %+v", plan.Holds)
	}
	in.Fingerprint = plan.Fingerprint
	if err := doErr(a, "publish.send", in, person); code(err) != "E_HELD" || len(yt.Videos) != 0 {
		t.Fatalf("posted past the gate: %v", err)
	}
	g := do[ops.ShipGateOut](t, a, "delivery.ready", ops.ShipGateIn{ProjectID: in.ProjectID})
	var keys []string
	for _, h := range g.Holds {
		if h.Overridable && !h.Covered {
			keys = append(keys, h.Key)
		}
	}
	do[ops.ShipGateOut](t, a, "delivery.override", ops.ShipOverrideIn{ShipGateIn: ops.ShipGateIn{ProjectID: in.ProjectID}, Note: "The client wants it up today.", Holds: keys})
	plan = do[ops.PublishPlanOut](t, a, "publish.plan", in)
	if !plan.Ready || !strings.Contains(strings.Join(plan.Notes, " "), "goes out past what held it") {
		t.Fatalf("sent anyway: %s %v", holdsSay(plan), plan.Notes)
	}
	in.Fingerprint = plan.Fingerprint
	do[ops.PublishSendOut](t, a, "publish.send", in)
	var r models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &r)
	if len(r.SentOut) != 1 || r.SentOut[0].What != models.SentPost || r.SentOut[0].Folder != "YouTube" || len(r.SentOut[0].Overrides) != 1 {
		t.Fatalf("went out: %+v", r.SentOut)
	}

	// The client's rules: their faces stay on this computer, out of anything delivered.
	if _, err := ops.CallTyped[leave.LeaveOut](ctx, a, "content.leave.set", leave.LeaveSetIn{ProjectID: in.ProjectID, Keep: map[string][]string{"delivery": {"faces"}}}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	refusals := func() int64 {
		n, _ := a.DB.C(store.CAudit).CountDocuments(ctx, map[string]any{"workspaceId": a.WorkspaceID(), "action": leave.AuditRefused})
		return n
	}
	plan = do[ops.PublishPlanOut](t, a, "publish.plan", in)
	if plan.Ready || !strings.Contains(holdsSay(plan), "keep faces on this computer") || refusals() != 0 {
		t.Fatalf("client rules in the plan: %s (%d recorded)", holdsSay(plan), refusals())
	}
	in.Again, in.Fingerprint = true, plan.Fingerprint
	if err := doErr(a, "publish.send", in, person); code(err) != "E_KEPT_HERE" || len(yt.Videos) != 1 || refusals() != 1 {
		t.Fatalf("posted against the client's rules: %v (%d videos, %d recorded)", err, len(yt.Videos), refusals())
	}
	// A person's post from the terminal stops at the plan and records its refusal too
	// (an AI looking at the plan doesn't).
	attempt := in
	attempt.Attempt = true
	if _, err := ops.CallTyped[ops.PublishPlanOut](ctx, a, "publish.plan", attempt, ops.Caller{}); err != nil || refusals() != 1 {
		t.Fatalf("a plan nobody asked to post recorded a refusal: %v (%d)", err, refusals())
	}
	if p, err := ops.CallTyped[ops.PublishPlanOut](ctx, a, "publish.plan", attempt, person); err != nil || refusals() != 2 || apperr.From(ops.PublishRefusal(p)).Code != apperr.KeptHere {
		t.Fatalf("a person's post wasn't recorded as refused: %v (%d)", err, refusals())
	}
}

// A version plays a clip that is also in another client's project, whose
// settings keep faces out of anything delivered: posting checks every project
// the version touches, as delivery does, so it isn't posted.
func TestPublishChecksEveryProjectAVersionTouches(t *testing.T) {
	a, yt, in, rid, _, _ := readyToPost(t)
	ctx := context.Background()
	var r models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &r); err != nil || len(r.Spec.Clips) == 0 {
		t.Fatalf("version %v", err)
	}
	other := project(t, a, "Client B")
	do[ops.ContentShowOut](t, a, "content.own", ops.ContentOwnIn{ProjectID: other.ID})
	do[ops.AssetMoveOut](t, a, "asset.move", ops.AssetMoveIn{AssetID: r.Spec.Clips[0].AssetID, To: other.ID}) // in Client B's project too
	if _, err := ops.CallTyped[leave.LeaveOut](ctx, a, "content.leave.set", leave.LeaveSetIn{ProjectID: other.ID, Keep: map[string][]string{"delivery": {"faces"}}}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	plan := do[ops.PublishPlanOut](t, a, "publish.plan", in)
	if plan.Ready || !strings.Contains(holdsSay(plan), "“Client B”") {
		t.Fatalf("Client B's rules weren't asked: %s", holdsSay(plan))
	}
	in.Fingerprint = plan.Fingerprint
	if err := doErr(a, "publish.send", in, person); err == nil || len(yt.Videos) != 0 {
		t.Fatalf("posted Client B's clip: %v (%d videos)", err, len(yt.Videos))
	}
}

// shipGateOn turns the ship gate on and sends the version out anyway with a
// note (the quality check hasn't looked at the render).
func shipGateOn(t *testing.T, a *app.App, projectID string) {
	t.Helper()
	ctx := context.Background()
	if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: a.WorkspaceID(), WorkspaceID: a.WorkspaceID(), Name: "T"}); err != nil {
		t.Fatal(err)
	}
	do[ops.GateSettingsOut](t, a, "delivery.gate.set", ops.GateSetIn{On: true})
	g := do[ops.ShipGateOut](t, a, "delivery.ready", ops.ShipGateIn{ProjectID: projectID})
	var keys []string
	for _, h := range g.Holds {
		if h.Overridable && !h.Covered {
			keys = append(keys, h.Key)
		}
	}
	do[ops.ShipGateOut](t, a, "delivery.override", ops.ShipOverrideIn{ShipGateIn: ops.ShipGateIn{ProjectID: projectID}, Note: "The client wants it up today.", Holds: keys})
}

// The connection drops part-way through a post sent past the ship gate: the
// post stays "sending" with its place kept (not failed), and what is kept
// says why in plain words — never the upload's address, which lets anyone
// send to it. When the connection is back, the daemon's look finishes it,
// and the version says it went out past the gate, as if it had never stopped.
func TestPublishGoesOnAfterADroppedConnection(t *testing.T) {
	a, yt, in, rid, _, video := readyToPost(t)
	ctx := context.Background()
	shipGateOn(t, a, in.ProjectID)
	oldChunk, oldWait := publish.UploadChunk, publish.RetryWait
	publish.UploadChunk, publish.RetryWait = 32<<10, time.Millisecond
	t.Cleanup(func() { publish.UploadChunk, publish.RetryWait = oldChunk, oldWait })
	plan := do[ops.PublishPlanOut](t, a, "publish.plan", in)
	in.Fingerprint = plan.Fingerprint
	yt.HangupFrom = 32 << 10 // the second piece never gets through
	err := doErr(a, "publish.send", in, person)
	if err == nil || !strings.Contains(err.Error(), "goes on with it by itself") {
		t.Fatalf("a dropped connection: %v", err)
	}
	leaked := func(what string) {
		t.Helper()
		var r models.Revision
		_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &r)
		var audits []models.AuditEvent
		_ = a.DB.Find(ctx, store.CAudit, a.WorkspaceID(), map[string]any{"targetId": rid}, &audits)
		b, _ := json.Marshal(map[string]any{"publications": r.Publications, "audit": audits})
		if strings.Contains(string(b), "/upload/session/") || strings.Contains(string(b), "127.0.0.1") {
			t.Fatalf("%s: the upload's address was kept:\n%s", what, b)
		}
	}
	pub := func() models.Publication {
		var r models.Revision
		_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &r)
		return r.Publications[len(r.Publications)-1]
	}
	if x := pub(); x.State != models.PublicationSending || x.Error == "" || len(x.Overrides) != 1 || !ops.PublishJournalKept(a, x.ID) {
		t.Fatalf("after the drop: %+v", x)
	}
	leaked("after the drop")
	if err := plainwords.CheckShown(pub().Error); err != nil {
		t.Errorf("%q: %v", pub().Error, err)
	}
	// Still dropping: it stays sending, place kept, nothing leaked.
	ops.RecoverPublications(ctx, a, time.Now().UTC().Add(3*time.Minute))
	if x := pub(); x.State != models.PublicationSending || x.Tries != 1 || !ops.PublishJournalKept(a, x.ID) {
		t.Fatalf("while it still drops: %+v", x)
	}
	leaked("while it still drops")
	// The connection is back.
	yt.HangupFrom = 0
	said := ops.RecoverPublications(ctx, a, time.Now().UTC().Add(6*time.Minute))
	x := pub()
	if x.State != models.PublicationPosted || len(yt.Videos) != 1 || !bytes.Equal(yt.Videos[0].Bytes, video) {
		t.Fatalf("went on: %+v %v", x, said)
	}
	var r models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &r)
	if len(r.SentOut) != 1 || r.SentOut[0].What != models.SentPost || len(r.SentOut[0].Overrides) != 1 || r.SentOut[0].Overrides[0] != x.Overrides[0] {
		t.Fatalf("the version doesn't say it went out: %+v", r.SentOut)
	}
}

// The posting daemon looks by the database server's clock, and a post's
// start, heartbeats and claims are the server's time (D360): a computer ten
// minutes fast leaves a post whose sender said it was sending 110 s ago and
// another computer's post that stopped 23 h 55 min ago; one ten minutes slow
// goes on with a post silent for 130 s — once: its claim is the server's time
// too, so the fast one doesn't take it again — and gives up on another
// computer's after 24 h 5 min.
func TestPublishRecoveryGoesByTheServersClock(t *testing.T) {
	a, yt, in, rid, rd, _ := readyToPost(t)
	ctx := context.Background()
	oldChunk, oldWait := publish.UploadChunk, publish.RetryWait
	publish.UploadChunk, publish.RetryWait = 32<<10, time.Millisecond
	t.Cleanup(func() { publish.UploadChunk, publish.RetryWait = oldChunk, oldWait })
	in.Fingerprint = do[ops.PublishPlanOut](t, a, "publish.plan", in).Fingerprint
	yt.HangupFrom = 32 << 10 // the second piece never gets through
	pub := func(id string) models.Publication {
		t.Helper()
		var r models.Revision
		_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &r)
		for _, x := range r.Publications {
			if x.ID == id || id == "" {
				return x
			}
		}
		t.Fatalf("no post %s", id)
		return models.Publication{}
	}
	beat := func(id string, at time.Time) {
		t.Helper()
		if _, err := a.DB.C(store.CRevisions).UpdateOne(ctx, bson.M{"_id": rid, "publications.id": id}, bson.M{"$set": bson.M{"publications.$.beat": at}}); err != nil {
			t.Fatal(err)
		}
	}

	restore := testutil.SkewClock(t, 10*time.Minute)
	if err := doErr(a, "publish.send", in, person); err == nil || !strings.Contains(err.Error(), "goes on with it by itself") {
		t.Fatalf("a dropped connection: %v", err)
	}
	x := pub("")
	if now := byServer(t, a); !near(x.At, now) || x.Beat == nil || !near(*x.Beat, now) {
		t.Fatalf("a post from a computer ten minutes fast started at %v, last said it was sending at %v; the server's time is %v", x.At, x.Beat, now)
	}
	ops.RecoverPublicationsNow(ctx, a)
	if got := pub(x.ID); got.Tries != 0 {
		t.Fatalf("a computer ten minutes fast took over a post that said it was sending a moment ago: %+v", got)
	}
	restore()
	beat(x.ID, byServer(t, a).Add(-110*time.Second))
	restore = testutil.SkewClock(t, 10*time.Minute)
	ops.RecoverPublicationsNow(ctx, a)
	if got := pub(x.ID); got.Tries != 0 || got.State != models.PublicationSending {
		t.Fatalf("a computer ten minutes fast took over a post silent for 110 s: %+v", got)
	}
	restore()

	beat(x.ID, byServer(t, a).Add(-130*time.Second))
	restore = testutil.SkewClock(t, -10*time.Minute)
	ops.RecoverPublicationsNow(ctx, a)
	got := pub(x.ID)
	if got.Tries != 1 || got.State != models.PublicationSending || got.Beat == nil || !near(*got.Beat, byServer(t, a)) {
		t.Fatalf("a computer ten minutes slow going on with a post silent for 130 s: %+v", got)
	}
	restore()
	restore = testutil.SkewClock(t, 10*time.Minute)
	ops.RecoverPublicationsNow(ctx, a)
	if got := pub(x.ID); got.Tries != 1 {
		t.Fatalf("a computer ten minutes fast took the post again right after another look took it: %+v", got)
	}
	restore()

	// Another computer's post, given up after a day by the server's clock.
	elsewhere := byServer(t, a).Add(-23*time.Hour - 55*time.Minute)
	other := models.Publication{ID: "pub_elsewhere", Platform: "youtube", State: models.PublicationSending, Channel: "Garden Club", ChannelID: "UCfake", RenderID: rd.ID,
		SHA256: rd.Output.SHA256, Size: rd.Output.Size, Installation: "ins_other", Beat: &elsewhere, At: elsewhere, By: a.Actor}
	if _, err := a.DB.C(store.CRevisions).UpdateOne(ctx, bson.M{"_id": rid}, bson.M{"$push": bson.M{"publications": other}}); err != nil {
		t.Fatal(err)
	}
	restore = testutil.SkewClock(t, 10*time.Minute)
	ops.RecoverPublicationsNow(ctx, a)
	if got := pub(other.ID); got.State != models.PublicationSending {
		t.Fatalf("a computer ten minutes fast gave up on another computer's post after 23 h 55 min: %+v", got)
	}
	restore()
	beat(other.ID, byServer(t, a).Add(-24*time.Hour-5*time.Minute))
	restore = testutil.SkewClock(t, -10*time.Minute)
	ops.RecoverPublicationsNow(ctx, a)
	if got := pub(other.ID); got.State != models.PublicationFailed || !strings.Contains(got.Error, "another computer") {
		t.Fatalf("a computer ten minutes slow kept another computer's post after 24 h 5 min: %+v", got)
	}
	restore()
}

// The daemon's look had marked a post as not posted (it stopped saying it was
// sending) when its sender finishes after all: it stays marked so — a post
// made again since mustn't be hidden — with what really happened noted.
func TestPublishLateVideoStaysMarkedNotPosted(t *testing.T) {
	a, yt, in, rid, rd, video := readyToPost(t)
	ctx := context.Background()
	person := a.Actor.UserID
	if person == "" {
		person = "owner"
	}
	local := filepath.Join(t.TempDir(), "final.mp4")
	_ = os.WriteFile(local, video, 0o600)
	at := time.Now().UTC().Add(-time.Hour)
	x := models.Publication{ID: "pub_late", Platform: "youtube", State: models.PublicationFailed, Error: "MetaRush stopped part-way through sending it.", Channel: "Garden Club",
		ChannelID: "UCfake", RenderID: rd.ID, SHA256: rd.Output.SHA256, Size: rd.Output.Size, Installation: a.Cfg.Installation.ID, Beat: &at, At: at, By: a.Actor}
	if _, err := a.DB.C(store.CRevisions).UpdateOne(ctx, map[string]any{"_id": rid}, map[string]any{"$push": map[string]any{"publications": x}}); err != nil {
		t.Fatal(err)
	}
	j := &ops.PublishJournalForTest{Publication: x.ID, Revision: rid, Project: in.ProjectID, Number: 1, Person: person, RenderKey: rd.Output.StorageKey, SHA256: rd.Output.SHA256,
		Size: rd.Output.Size, Video: publish.Video{Path: local, Size: rd.Output.Size, Title: "The garden at Elmwood", Privacy: "private"}}
	posted, err := ops.SendPublication(ctx, a, j, x)
	if err != nil || posted == nil || !posted.Late || len(yt.Videos) != 1 {
		t.Fatalf("late: %+v %v", posted, err)
	}
	var r models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &r)
	got := r.Publications[len(r.Publications)-1]
	if got.State != models.PublicationFailed || got.LateURL == "" || !strings.Contains(strings.Join(got.Notes, " "), "reached YouTube after all") {
		t.Fatalf("flipped: %+v", got)
	}
}

// The daemon's look for posts still sending (every minute, on every daemon)
// reads an index of only the versions with one, never every version.
func TestPublishRecoveryUsesItsIndex(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	var out bson.M
	err := a.DB.C(store.CRevisions).Database().RunCommand(ctx, bson.D{{Key: "explain", Value: bson.D{{Key: "find", Value: store.CRevisions},
		{Key: "filter", Value: ops.PublishSendingFilter(a)}}}, {Key: "verbosity", Value: "queryPlanner"}}).Decode(&out)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := json.Marshal(out["queryPlanner"])
	if !strings.Contains(string(plan), `"publications_sending"`) || strings.Contains(string(plan), "COLLSCAN") {
		t.Fatalf("the look reads every version:\n%s", plan)
	}
}

// A version's sound track plays footage that is also in another client's
// project, whose rules keep voices and footage out of anything delivered:
// every way it could leave — posting, delivery into a folder a sync service
// uploads, a timeline exported there — asks that client's rules too, as for
// a clip it plays.
func TestClientRulesCoverEverySoundAVersionPlays(t *testing.T) {
	a, yt, in, rid, _, _ := readyToPost(t)
	ctx := context.Background()
	var r models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &r); err != nil {
		t.Fatal(err)
	}
	other := project(t, a, "Client B")
	do[ops.ContentShowOut](t, a, "content.own", ops.ContentOwnIn{ProjectID: other.ID})
	voice := txClip(t, a, "Voice of B", r.ProjectID, []models.Word{wd("Secret", 0.1, 0.5, 0.9), wd("plans.", 0.6, 1.5, 0.9)})
	do[ops.AssetMoveOut](t, a, "asset.move", ops.AssetMoveIn{AssetID: voice.ID, To: other.ID})
	if _, err := ops.CallTyped[leave.LeaveOut](ctx, a, "content.leave.set", leave.LeaveSetIn{ProjectID: other.ID, Keep: map[string][]string{"delivery": {"voices", "footage", "faces", "words"}}}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	spec := r.Spec
	spec.Audio = append(spec.Audio, editspec.AudioTrack{ID: "bvoice", AssetID: voice.ID, Role: editspec.RoleDialogue, In: 0, Out: 1.5, At: 0})
	plan, _ := json.Marshal(editspec.Plan{ProjectID: r.ProjectID, BaseRevisionID: rid, Instruction: "add B's voice", Spec: &spec})
	chk := do[ops.EditCheckOut](t, a, "edit.check", ops.EditCheckIn{Plan: plan})
	if !chk.Valid {
		t.Fatalf("not valid: %+v", chk.Errors)
	}
	nrid := do[ops.EditApplyOut](t, a, "edit.apply", ops.EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken}).Revision.ID
	do[ops.RevisionSummary](t, a, "revision.approve", ops.RevisionApproveIn{ID: nrid})
	finalRenderOf(t, a, nrid)
	if touches := ops.RevisionTouches(ctx, a, nrid); !slices.Contains(touches, other.ID) {
		t.Fatalf("the version doesn't touch Client B: %v", touches)
	}
	in.Revision = nrid
	p := do[ops.PublishPlanOut](t, a, "publish.plan", in)
	if p.Ready || !strings.Contains(holdsSay(p), "“Client B”") {
		t.Fatalf("posting didn't ask Client B's rules: %s", holdsSay(p))
	}
	in.Fingerprint = p.Fingerprint
	if err := doErr(a, "publish.send", in, person); code(err) != "E_KEPT_HERE" || len(yt.Videos) != 0 {
		t.Fatalf("posted: %v (%d videos)", err, len(yt.Videos))
	}
	// A folder a sync service uploads to the cloud.
	home := t.TempDir()
	t.Setenv("HOME", home)
	synced := filepath.Join(home, "Library", "CloudStorage", "Dropbox", "Deliveries")
	if err := os.MkdirAll(synced, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := doErr(a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{nrid}, Out: synced}, person); code(err) != "E_KEPT_HERE" {
		t.Fatalf("delivered into a synced folder: %v", err)
	}
	if err := doErr(a, "export.timeline", ops.ExportTimelineIn{RevisionID: nrid, Format: "otio", Out: filepath.Join(synced, "cut.otio")}, person); code(err) != "E_KEPT_HERE" {
		t.Fatalf("exported into a synced folder: %v", err)
	}
}

// A post stops part-way (the connection drops), then the client's rules keep
// the project's footage from delivery: the daemon's look asks the plan again
// as the person who posted it, doesn't go on, marks it as not posted in plain
// words and tells that person.
func TestPublishRecoveryAsksAgainWhatHoldsIt(t *testing.T) {
	a, yt, in, rid, _, _ := readyToPost(t)
	ctx := context.Background()
	oldChunk, oldWait := publish.UploadChunk, publish.RetryWait
	publish.UploadChunk, publish.RetryWait = 32<<10, time.Millisecond
	t.Cleanup(func() { publish.UploadChunk, publish.RetryWait = oldChunk, oldWait })
	plan := do[ops.PublishPlanOut](t, a, "publish.plan", in)
	in.Fingerprint = plan.Fingerprint
	yt.HangupFrom = 40 << 10
	if err := doErr(a, "publish.send", in, person); err == nil || !strings.Contains(err.Error(), "goes on with it by itself") {
		t.Fatalf("a dropped connection: %v", err)
	}
	do[ops.ContentShowOut](t, a, "content.own", ops.ContentOwnIn{ProjectID: plan.ProjectID})
	if _, err := ops.CallTyped[leave.LeaveOut](ctx, a, "content.leave.set", leave.LeaveSetIn{ProjectID: plan.ProjectID, Keep: map[string][]string{"delivery": {"footage", "voices", "faces", "words"}}}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	yt.HangupFrom = 0
	ops.RecoverPublications(ctx, a, time.Now().UTC().Add(3*time.Minute))
	var r models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &r)
	x := r.Publications[len(r.Publications)-1]
	if len(yt.Videos) != 0 || x.State != models.PublicationFailed || !strings.Contains(x.Error, "keep") {
		t.Fatalf("went on against the client's rules: %d videos, %+v", len(yt.Videos), x)
	}
	if err := plainwords.CheckShown(x.Error); err != nil {
		t.Errorf("%q: %v", x.Error, err)
	}
	var bell []models.Notification
	_ = a.DB.Find(ctx, store.CNotifications, a.WorkspaceID(), map[string]any{"userId": x.By.UserID, "kind": models.NotifyPublishStopped}, &bell)
	if len(bell) != 1 || !strings.Contains(bell[0].Title, "wasn't posted to YouTube") {
		t.Fatalf("the person wasn't told: %+v", bell)
	}
}

// frozenSend starts a terminal's post that freezes after its first piece (a
// laptop asleep, Ctrl-Z) until wake is closed.
func frozenSend(t *testing.T, a *app.App, in ops.PublishIn) (frozen, wake chan struct{}, done chan error, out *ops.PublishSendOut) {
	t.Helper()
	oldChunk, oldBeat := publish.UploadChunk, ops.PublishBeatEvery
	publish.UploadChunk, ops.PublishBeatEvery = 32<<10, time.Hour
	t.Cleanup(func() { publish.UploadChunk, ops.PublishBeatEvery = oldChunk, oldBeat })
	frozen, wake, done = make(chan struct{}), make(chan struct{}), make(chan error, 1)
	out = &ops.PublishSendOut{}
	first := true
	pctx := publish.WithProgress(context.Background(), func(sent, total int64) {
		if first && sent > 0 && sent < total {
			first = false
			close(frozen)
			<-wake
		}
	})
	go func() {
		o, err := ops.CallTyped[ops.PublishSendOut](pctx, a, "publish.send", in, ops.Caller{Human: true})
		if o != nil {
			*out = *o
		}
		done <- err
	}()
	return frozen, wake, done, out
}

// A terminal's post freezes past the time the daemon waits; the daemon takes
// it over and finishes the same upload. When the terminal wakes, it doesn't
// finish it again or send the captions twice: it says it was posted.
func TestPublishOneSenderAtATime(t *testing.T) {
	a, yt, in, rid, _, _ := readyToPost(t)
	ctx := context.Background()
	in.Fingerprint = do[ops.PublishPlanOut](t, a, "publish.plan", in).Fingerprint
	frozen, wake, done, out := frozenSend(t, a, in)
	<-frozen
	ops.RecoverPublications(ctx, a, time.Now().UTC().Add(3*time.Minute))
	close(wake)
	if err := <-done; err != nil || out.Publication.State != models.PublicationPosted || out.Publication.LateURL != "" || !strings.HasPrefix(out.Summary, "Posted version 1") {
		t.Fatalf("the terminal says: %v %+v %q", err, out.Publication, out.Summary)
	}
	var r models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &r)
	if len(r.Publications) != 1 || r.Publications[0].State != models.PublicationPosted || len(yt.Videos) != 1 || len(yt.Videos[0].Captions) != 1 {
		t.Fatalf("recorded %+v; %d videos", r.Publications, len(yt.Videos))
	}
}

// A post sent past the ship gate that the daemon had given up on reaches
// YouTube late: it stays marked as not posted, and the version says it went
// out (the receipt says "went out", not "allowed").
func TestPublishLateArrivalPastTheShipGateWentOut(t *testing.T) {
	a, yt, in, rid, _, _ := readyToPost(t)
	ctx := context.Background()
	shipGateOn(t, a, in.ProjectID)
	in.Fingerprint = do[ops.PublishPlanOut](t, a, "publish.plan", in).Fingerprint
	frozen, wake, done, _ := frozenSend(t, a, in)
	<-frozen
	ops.RecoverPublications(ctx, a, time.Now().UTC().Add(24*time.Hour)) // gives up on it
	close(wake)
	<-done
	var r models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &r)
	x := r.Publications[len(r.Publications)-1]
	if len(yt.Videos) != 1 || x.State != models.PublicationFailed || x.LateURL == "" {
		t.Fatalf("late: %+v (%d videos)", x, len(yt.Videos))
	}
	if len(r.SentOut) != 1 || r.SentOut[0].What != models.SentPost {
		t.Fatalf("the version doesn't say it went out: %+v", r.SentOut)
	}
}
