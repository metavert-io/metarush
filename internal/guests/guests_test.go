package guests_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/guests"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/sso"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakebucket"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Guests (D490–D504) at the level of their operations: who may share and
// how, what a guest's grants let them see (and the one answer for anything
// else), viewers and commenters, links used once and ending, sign-ins ending
// the moment something is taken back, What may leave keeping a client's
// things from guests, the organization sign-in's rules for guests, and every
// request recorded. The guest listener's own checks are in internal/api.

// owner is this computer's own command line: the owner, a person.
var owner = ops.Caller{Human: true}

func call[O any](t *testing.T, ctx context.Context, a *app.App, op string, in any, c ops.Caller) *O {
	t.Helper()
	out, err := callErr(ctx, a, op, in, c)
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	return out.(*O)
}

func callErr(ctx context.Context, a *app.App, op string, in any, c ops.Caller) (any, error) {
	raw, _ := json.Marshal(in)
	return ops.Call(ctx, a, op, raw, c)
}

func reason(err error) string {
	if err == nil {
		return ""
	}
	d, _ := apperr.From(err).Details.(map[string]any)
	r, _ := d["reason"].(string)
	return r
}

// world is a workspace with guests on: "Spring promo" with three versions
// (the first two with review copies), and "Acme" with one of its own.
type world struct {
	a     *app.App
	p     models.Project
	revs  []string
	rnd   map[string]models.Render
	acme  models.Project
	acmeV string
	clip  models.Asset
	heads map[string]string
}

func newWorld(t *testing.T, o testapp.Options) *world {
	t.Helper()
	t.Setenv("CLAUDECODE", "")
	a := testapp.NewWith(t, o)
	a.Actor.UserName = "Sam Lee"
	ctx := context.Background()
	w := &world{a: a, rnd: map[string]models.Render{}}
	now := time.Now().UTC()
	if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: a.WorkspaceID(), WorkspaceID: a.WorkspaceID(), Name: "T", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	w.p = call[ops.ProjectOut](t, ctx, a, "project.create", ops.ProjectCreateIn{Name: "Spring promo"}, owner).Project
	w.acme = call[ops.ProjectOut](t, ctx, a, "project.create", ops.ProjectCreateIn{Name: "Acme"}, owner).Project
	w.clip = w.addClip(t, w.p.ID)
	for i := 0; i < 3; i++ {
		rev := w.cut(t, w.p.ID, w.clip.ID, float64(2+i))
		w.revs = append(w.revs, rev)
		if i < 2 {
			w.rnd[rev] = w.render(t, w.p.ID, rev, i+1)
		}
	}
	ac := w.addClip(t, w.acme.ID)
	w.acmeV = w.cut(t, w.acme.ID, ac.ID, 2)
	w.rnd[w.acmeV] = w.render(t, w.acme.ID, w.acmeV, 1)
	call[guests.GuestsStatusOut](t, ctx, a, "guests.settings.set", guests.GuestsSettingsSetIn{On: ptr(true)}, owner)
	return w
}

func ptr[T any](v T) *T { return &v }

func (w *world) put(t *testing.T, n int) (string, string, int64) {
	t.Helper()
	b := make([]byte, n)
	_, _ = rand.Read(b)
	p := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	sha, _ := media.HashFile(p)
	key := media.ObjectKey(w.a.WorkspaceID(), sha)
	if _, err := w.a.Media.PutFile(context.Background(), key, p, media.PutOptions{SHA256: sha, ContentType: "video/mp4"}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	return key, sha, int64(n)
}

func (w *world) addClip(t *testing.T, projectID string) models.Asset {
	t.Helper()
	key, sha, size := w.put(t, 4096)
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: w.a.WorkspaceID(), ProjectIDs: []string{projectID}, Kind: "video", Name: "garden.mov", Title: "Garden",
		SHA256: sha, Size: size, StorageKey: key, State: models.AssetAvailable, Media: models.MediaInfo{HasVideo: true, HasAudio: true, Duration: 10, Width: 1920, Height: 1080, FPS: 30},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
	if err := w.a.DB.Insert(context.Background(), store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	return as
}

func (w *world) cut(t *testing.T, projectID, assetID string, out float64) string {
	t.Helper()
	ctx := context.Background()
	if w.heads == nil {
		w.heads = map[string]string{}
	}
	spec := editspec.Spec{SchemaVersion: 1, Title: "Garden story", Output: editspec.Output{Preset: "landscape-1080p", Width: 1920, Height: 1080, FPS: 30},
		Clips: []editspec.Clip{{ID: "c", AssetID: assetID, In: 0, Out: out}}}
	plan, _ := json.Marshal(editspec.Plan{ProjectID: projectID, BaseRevisionID: w.heads[projectID], Instruction: "test cut", Spec: &spec})
	chk := call[ops.EditCheckOut](t, ctx, w.a, "edit.check", ops.EditCheckIn{Plan: plan}, ops.Caller{})
	if !chk.Valid {
		t.Fatalf("cut not valid: %+v", chk.Errors)
	}
	id := call[ops.EditApplyOut](t, ctx, w.a, "edit.apply", ops.EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken}, ops.Caller{}).Revision.ID
	w.heads[projectID] = id
	return id
}

func (w *world) render(t *testing.T, projectID, rev string, n int) models.Render {
	t.Helper()
	key, sha, size := w.put(t, 64<<10)
	now := time.Now().UTC()
	rd := models.Render{ID: ids.New(ids.Render), WorkspaceID: w.a.WorkspaceID(), ProjectID: projectID, RevisionID: rev, RevisionNumber: n, Preset: "landscape-1080p",
		Quality: "review", State: models.RenderAvailable, Output: &models.RenderOutput{StorageKey: key, SHA256: sha, Size: size, Duration: 3, Width: 1920, Height: 1080, FPS: 30},
		CreatedAt: now, CompletedAt: &now}
	if err := w.a.DB.Insert(context.Background(), store.CRenders, rd); err != nil {
		t.Fatal(err)
	}
	return rd
}

// share shares with a guest as the owner.
func (w *world) share(t *testing.T, email, role string, versions ...string) *guests.GuestsShareOut {
	t.Helper()
	return call[guests.GuestsShareOut](t, context.Background(), w.a, "guests.share", guests.GuestsShareIn{Email: email, Name: "Dana", ProjectID: w.p.ID, Versions: versions, Role: role, Days: 14}, owner)
}

// signIn is a guest's sign-in by a link the owner made: the checked request
// the guest listener would hand the guest operations.
func (w *world) signIn(t *testing.T, email string) (*guests.Signed, string) {
	t.Helper()
	ctx := context.Background()
	link := call[guests.GuestsLinkOut](t, ctx, w.a, "guests.link", guests.GuestsLinkIn{Guest: email}, owner)
	tok := link.URL[strings.Index(link.URL, "#")+1:]
	g, err := guests.Redeem(ctx, w.a, tok)
	if err != nil {
		t.Fatal(err)
	}
	cookie, err := guests.NewSession(ctx, w.a, g, "link", "", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	s, err := guests.Check(ctx, w.a, cookie, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return s, cookie
}

// asGuest calls a guest operation as the guest listener does.
func (w *world) asGuest(s *guests.Signed, op string, in any) (any, error) {
	b := *w.a
	b.Actor = guests.Actor(&s.Guest, w.a.Cfg.Installation.ID)
	return callErr(guests.WithSigned(context.Background(), s), &b, op, in, ops.Caller{Human: true, Remote: true, Elsewhere: true, Guest: true})
}

// member is a signed-in member who isn't an owner, over the dashboard.
func member() (context.Context, ops.Caller) {
	return ops.OverHTTP(context.Background(), func() bool { return false }), ops.Caller{Human: true, Remote: true}
}

func TestSharingIsAnOwnersStepAsAPerson(t *testing.T) {
	w := newWorld(t, testapp.Options{})
	ctx := context.Background()
	in := guests.GuestsShareIn{Email: "dana@client.example", ProjectID: w.p.ID, Role: models.GuestViewer, Days: 7}
	mctx, mc := member()
	if _, err := callErr(mctx, w.a, "guests.share", in, mc); reason(err) != "owner_only" {
		t.Fatalf("a member shared with a guest: %v", err)
	}
	if _, err := callErr(ctx, w.a, "guests.share", in, ops.Caller{}); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("something that isn't a person shared with a guest: %v", err)
	}
	refusals := []struct {
		name   string
		change func(*guests.GuestsShareIn)
		reason string
	}{
		{"approvers come later", func(i *guests.GuestsShareIn) { i.Role = models.GuestApprover }, "approver"},
		{"a role", func(i *guests.GuestsShareIn) { i.Role = "editor" }, "role"},
		{"an end is required", func(i *guests.GuestsShareIn) { i.Days = 0 }, "end"},
		{"at most 90 days", func(i *guests.GuestsShareIn) { i.Days = 91 }, "days"},
		{"a last day too far", func(i *guests.GuestsShareIn) {
			i.Days, i.Until = 0, time.Now().Add(200*24*time.Hour).Format("2006-01-02")
		}, "until"},
		{"a day that passed", func(i *guests.GuestsShareIn) { i.Days, i.Until = 0, "2020-01-01" }, "until"},
		{"an email", func(i *guests.GuestsShareIn) { i.Email = "not an email" }, "email"},
		{"a version of another project", func(i *guests.GuestsShareIn) { i.Versions = []string{w.acmeV} }, "version"},
	}
	for _, c := range refusals {
		x := in
		c.change(&x)
		if _, err := callErr(ctx, w.a, "guests.share", x, owner); reason(err) != c.reason {
			t.Errorf("%s: %v (%s)", c.name, err, reason(err))
		}
	}
	// A member's own email isn't a guest's.
	if err := w.a.DB.Insert(ctx, store.CUsers, models.User{ID: ids.New(ids.User), WorkspaceID: w.a.WorkspaceID(), Name: "Maya", Email: "maya@studio.example",
		EmailKey: "maya@studio.example", Role: models.RoleMember, State: models.UserActive, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	x := in
	x.Email = "Maya@Studio.example"
	if _, err := callErr(ctx, w.a, "guests.share", x, owner); reason(err) != "member" {
		t.Fatalf("a member was made a guest: %v", err)
	}
	// A dry run says what the guest would see and makes nothing.
	plan := call[guests.GuestsShareOut](t, ctx, w.a, "guests.share", guests.GuestsShareIn{Email: "dana@client.example", ProjectID: w.p.ID, Role: models.GuestCommenter, Days: 3, DryRun: true}, owner)
	if !plan.May || plan.Shared || len(plan.Exposes) == 0 || !strings.Contains(plan.Summary, "would watch and comment") {
		t.Fatalf("plan: %+v", plan)
	}
	if n, _ := w.a.DB.C(store.CGuests).CountDocuments(ctx, bson.M{"workspaceId": w.a.WorkspaceID()}); n != 0 {
		t.Fatalf("a dry run made %d guests", n)
	}
	// The share: a guest and a grant, audited, ending when it says.
	out := w.share(t, "Dana@Client.example", models.GuestViewer, "r2")
	if !out.Shared || out.Guest == nil || out.Grant == nil || len(out.Grant.RevisionIDs) != 1 || out.Grant.RevisionIDs[0] != w.revs[1] {
		t.Fatalf("share: %+v", out)
	}
	if d := time.Until(out.Grant.EndsAt); d < 13*24*time.Hour || d > 15*24*time.Hour {
		t.Fatalf("ends in %s", d)
	}
	var ev models.AuditEvent
	if err := w.a.DB.C(store.CAudit).FindOne(ctx, bson.M{"workspaceId": w.a.WorkspaceID(), "action": "guests.share"}).Decode(&ev); err != nil || ev.TargetID != out.Grant.ID {
		t.Fatalf("not audited: %v %+v", err, ev)
	}
	// Sharing again with the same email is the same guest.
	again := w.share(t, "dana@client.example", models.GuestCommenter)
	if again.Guest.ID != out.Guest.ID || again.NewGuest {
		t.Fatal("a second guest for one email")
	}
}

func TestAGuestSeesOnlyWhatTheirGrantsName(t *testing.T) {
	w := newWorld(t, testapp.Options{})
	w.share(t, "dana@client.example", models.GuestViewer, w.revs[1]) // version 2 only
	s, _ := w.signIn(t, "dana@client.example")
	out, err := w.asGuest(s, "guest.shared", guests.GuestSharedIn{})
	if err != nil {
		t.Fatal(err)
	}
	sh := out.(*guests.GuestSharedOut)
	if len(sh.Shares) != 1 || len(sh.Shares[0].Versions) != 1 || sh.Shares[0].Versions[0].Number != 2 || !sh.Shares[0].Versions[0].Ready {
		t.Fatalf("shared: %+v", sh)
	}
	if _, err := w.asGuest(s, "guest.watch", guests.GuestWatchIn{Version: w.revs[1]}); err != nil {
		t.Fatalf("watching what was shared: %v", err)
	}
	// Everything else is the one same answer: the project's other versions,
	// another project's version, a made-up one, something that isn't an ID.
	for name, v := range map[string]string{"the project's other version": w.revs[0], "a version not ready": w.revs[2], "another project's": w.acmeV,
		"one that never was": ids.New(ids.Revision), "not an ID": "r1", "a project": w.p.ID} {
		for _, op := range []string{"guest.watch", "guest.comments"} {
			var in any = guests.GuestWatchIn{Version: v}
			if op == "guest.comments" {
				in = guests.GuestCommentsIn{Version: v}
			}
			_, err := w.asGuest(s, op, in)
			if apperr.From(err).Code != apperr.NotFound || reason(err) != guests.ReasonNotShared || apperr.From(err).Message != "That isn't shared with you." {
				t.Errorf("%s through %s: %v", name, op, err)
			}
		}
	}
	// A guest reaches no member operation.
	for _, op := range []string{"project.list", "revision.show", "comment.list", "guests.list", "render.status"} {
		if _, err := w.asGuest(s, op, map[string]any{}); reason(err) != "guests_only" {
			t.Errorf("a guest called %s: %v", op, err)
		}
	}
	// And names no other guest: As is an owner's.
	if _, err := w.asGuest(s, "guest.shared", guests.GuestSharedIn{As: "someone@else.example"}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("a guest looked as another guest: %v", err)
	}
}

func TestAProjectGrantShowsEveryVersionWithAReviewCopy(t *testing.T) {
	w := newWorld(t, testapp.Options{})
	w.share(t, "dana@client.example", models.GuestViewer)
	s, _ := w.signIn(t, "dana@client.example")
	out, err := w.asGuest(s, "guest.shared", guests.GuestSharedIn{})
	if err != nil {
		t.Fatal(err)
	}
	vs := out.(*guests.GuestSharedOut).Shares[0].Versions
	if len(vs) != 2 || vs[0].Number != 2 || vs[1].Number != 1 {
		t.Fatalf("versions: %+v", vs)
	}
	// A version made later with a review copy shows too; an AI app's draft
	// nobody approved never does.
	w.rnd[w.revs[2]] = w.render(t, w.p.ID, w.revs[2], 3)
	if _, err := w.asGuest(s, "guest.watch", guests.GuestWatchIn{Version: w.revs[2]}); err != nil {
		t.Fatalf("a later version: %v", err)
	}
	ctx := context.Background()
	if err := w.a.DB.Update(ctx, store.CRevisions, w.a.WorkspaceID(), w.revs[2], bson.M{"$set": bson.M{"draftId": "drf_x"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.asGuest(s, "guest.watch", guests.GuestWatchIn{Version: w.revs[2]}); reason(err) != guests.ReasonNotShared {
		t.Fatalf("a draft was shown: %v", err)
	}
	// Another project's version stays another project's.
	if _, err := w.asGuest(s, "guest.watch", guests.GuestWatchIn{Version: w.acmeV}); reason(err) != guests.ReasonNotShared {
		t.Fatalf("another project's version: %v", err)
	}
}

func TestDeletedVersionsAndArchivedProjectsAreNotShared(t *testing.T) {
	w := newWorld(t, testapp.Options{})
	ctx := context.Background()
	w.share(t, "dana@client.example", models.GuestViewer)
	s, _ := w.signIn(t, "dana@client.example")
	if _, err := w.a.DB.C(store.CRevisions).DeleteOne(ctx, bson.M{"_id": w.revs[0]}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.asGuest(s, "guest.watch", guests.GuestWatchIn{Version: w.revs[0]}); reason(err) != guests.ReasonNotShared {
		t.Fatalf("a deleted version: %v", err)
	}
	if err := w.a.DB.Update(ctx, store.CProjects, w.a.WorkspaceID(), w.p.ID, bson.M{"$set": bson.M{"archived": true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.asGuest(s, "guest.watch", guests.GuestWatchIn{Version: w.revs[1]}); reason(err) != guests.ReasonNotShared {
		t.Fatalf("an archived project: %v", err)
	}
	out, _ := w.asGuest(s, "guest.shared", guests.GuestSharedIn{})
	if n := len(out.(*guests.GuestSharedOut).Shares); n != 0 {
		t.Fatalf("an archived project is listed: %d", n)
	}
}

func TestViewersWatchAndCommentersComment(t *testing.T) {
	w := newWorld(t, testapp.Options{})
	ctx := context.Background()
	w.share(t, "vic@client.example", models.GuestViewer)
	w.share(t, "dana@client.example", models.GuestCommenter, w.revs[0])
	viewer, _ := w.signIn(t, "vic@client.example")
	commenter, _ := w.signIn(t, "dana@client.example")
	at := 1.5
	if _, err := w.asGuest(viewer, "guest.comment", guests.GuestCommentIn{Version: w.revs[0], Body: "nice", At: &at}); reason(err) != guests.ReasonViewer {
		t.Fatalf("a viewer commented: %v", err)
	}
	out, _ := w.asGuest(viewer, "guest.comments", guests.GuestCommentsIn{Version: w.revs[0]})
	if cs := out.(*guests.GuestCommentsOut); cs.CanComment || len(cs.Comments) != 0 {
		t.Fatalf("a viewer's comments: %+v", cs)
	}
	// A commenter's comment: cleaned, bounded, pinned in the video, kept as
	// the version's, marked as a guest's.
	if _, err := w.asGuest(commenter, "guest.comment", guests.GuestCommentIn{Version: w.revs[0], Body: "Shorter ‮intro​ please", At: &at}); err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string]guests.GuestCommentIn{
		"empty":         {Version: w.revs[0], Body: "   "},
		"too long":      {Version: w.revs[0], Body: strings.Repeat("a", guests.MaxComment+1)},
		"before 0":      {Version: w.revs[0], Body: "x", At: ptr(-1.0)},
		"past the end":  {Version: w.revs[0], Body: "x", At: ptr(900.0)},
		"not in grants": {Version: w.revs[1], Body: "x"},
	} {
		if _, err := w.asGuest(commenter, "guest.comment", in); err == nil {
			t.Errorf("%s: a comment was kept", name)
		}
	}
	got, _ := w.asGuest(commenter, "guest.comments", guests.GuestCommentsIn{Version: w.revs[0]})
	cs := got.(*guests.GuestCommentsOut)
	if !cs.CanComment || len(cs.Comments) != 1 || cs.Comments[0].Body != "Shorter intro please" || *cs.Comments[0].At != 1.5 {
		t.Fatalf("their comments: %+v", cs)
	}
	// Members read it on the version, marked as a guest's; an AI reads it quoted.
	list := call[ops.CommentListOut](t, ctx, w.a, "comment.list", ops.CommentListIn{ProjectID: w.p.ID, RevisionID: w.revs[0]}, ops.Caller{})
	if len(list.Comments) != 1 || list.Comments[0].Guest == nil || list.Comments[0].Author.UserName != "Dana (guest)" || list.Comments[0].Author.Via != models.ViaGuest {
		t.Fatalf("members see: %+v", list.Comments)
	}
	ai := *w.a
	ai.Actor.Via = "mcp"
	quoted := call[ops.CommentListOut](t, ctx, &ai, "comment.list", ops.CommentListIn{ProjectID: w.p.ID, RevisionID: w.revs[0]}, ops.Caller{})
	if c := quoted.Comments[0]; !strings.HasPrefix(c.Body, "“") || c.Quoted == "" || !strings.Contains(c.Author.UserName, "(guest)") {
		t.Fatalf("an AI reads it unquoted: %+v", c)
	}
	// The viewer never sees the commenter's words; the person who shared hears.
	other, _ := w.asGuest(viewer, "guest.comments", guests.GuestCommentsIn{Version: w.revs[0]})
	if len(other.(*guests.GuestCommentsOut).Comments) != 0 {
		t.Fatal("a guest read another guest's comments")
	}
	var n models.Notification
	if err := w.a.DB.C(store.CNotifications).FindOne(ctx, bson.M{"workspaceId": w.a.WorkspaceID(), "kind": models.NotifyGuestComments}).Decode(&n); err != nil {
		t.Fatalf("no note for the person who shared: %v", err)
	}
	// Members never comment as a guest, even owners.
	if _, err := callErr(ctx, w.a, "guest.comment", guests.GuestCommentIn{Version: w.revs[0], Body: "x"}, owner); reason(err) != "guest_only" {
		t.Fatalf("an owner commented as a guest: %v", err)
	}
}

func TestTakingThingsBackEndsSignInsAtOnce(t *testing.T) {
	w := newWorld(t, testapp.Options{})
	ctx := context.Background()
	one := w.share(t, "dana@client.example", models.GuestViewer, w.revs[0])
	two := w.share(t, "dana@client.example", models.GuestViewer, w.revs[1])
	s, cookie := w.signIn(t, "dana@client.example")
	// Taking one grant back ends it at once; the other still works.
	call[guests.GuestsRevokeOut](t, ctx, w.a, "guests.revoke", guests.GuestsRevokeIn{Grant: one.Grant.ID}, owner)
	s2, err := guests.Check(ctx, w.a, cookie, "127.0.0.1")
	if err != nil {
		t.Fatalf("one grant left, signed out: %v", err)
	}
	if _, err := w.asGuest(s2, "guest.watch", guests.GuestWatchIn{Version: w.revs[0]}); reason(err) != guests.ReasonNotShared {
		t.Fatalf("a grant taken back still works: %v", err)
	}
	// A request checked before the change is held to it on its next one.
	_ = s
	// The last one going signs them out, and the sign-in is gone.
	out := call[guests.GuestsRevokeOut](t, ctx, w.a, "guests.revoke", guests.GuestsRevokeIn{Grant: two.Grant.ID}, owner)
	if out.SignedOut != 1 {
		t.Fatalf("signed out %d", out.SignedOut)
	}
	if _, err := guests.Check(ctx, w.a, cookie, "127.0.0.1"); reason(err) != guests.ReasonSignedOut {
		t.Fatalf("still signed in: %v", err)
	}
	// An ended grant (its day passed, by the database's clock) is the same.
	w.share(t, "eve@client.example", models.GuestViewer)
	_, ec := w.signIn(t, "eve@client.example")
	if _, err := w.a.DB.C(store.CGuestGrants).UpdateMany(ctx, bson.M{"workspaceId": w.a.WorkspaceID()}, bson.M{"$set": bson.M{"endsAt": time.Now().Add(-time.Minute).UTC()}}); err != nil {
		t.Fatal(err)
	}
	if _, err := guests.Check(ctx, w.a, ec, "127.0.0.1"); err == nil {
		t.Fatal("an ended grant still signs in")
	}
	// Taking a guest back ends everything of theirs.
	w.share(t, "fay@client.example", models.GuestCommenter)
	_, fc := w.signIn(t, "fay@client.example")
	call[guests.GuestsRevokeOut](t, ctx, w.a, "guests.revoke", guests.GuestsRevokeIn{Guest: "FAY@client.example"}, owner)
	if _, err := guests.Check(ctx, w.a, fc, "127.0.0.1"); err == nil {
		t.Fatal("a guest taken back is still signed in")
	}
	if _, err := callErr(ctx, w.a, "guests.link", guests.GuestsLinkIn{Guest: "fay@client.example"}, owner); err == nil {
		t.Fatal("a link for a guest taken back")
	}
	// Turning guests off signs everyone out, in plain words.
	w.share(t, "gil@client.example", models.GuestViewer)
	_, gc := w.signIn(t, "gil@client.example")
	call[guests.GuestsStatusOut](t, ctx, w.a, "guests.settings.set", guests.GuestsSettingsSetIn{On: ptr(false)}, owner)
	if _, err := guests.Check(ctx, w.a, gc, "127.0.0.1"); reason(err) != guests.ReasonGuestsOff {
		t.Fatalf("guests off, still signed in: %v", err)
	}
	if n, _ := w.a.DB.C(store.CGuestSessions).CountDocuments(ctx, bson.M{"workspaceId": w.a.WorkspaceID()}); n != 0 {
		t.Fatalf("%d sign-ins left after guests were turned off", n)
	}
	if l := w.a.Cfg.GuestListener(); l.Listen {
		t.Fatal("this computer still answers guests")
	}
}

func TestOneTimeLinksWorkOnceAndEnd(t *testing.T) {
	w := newWorld(t, testapp.Options{})
	ctx := context.Background()
	if _, err := callErr(ctx, w.a, "guests.link", guests.GuestsLinkIn{Guest: "nobody@client.example"}, owner); apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("a link for no guest: %v", err)
	}
	w.share(t, "dana@client.example", models.GuestViewer)
	mctx, mc := member()
	if _, err := callErr(mctx, w.a, "guests.link", guests.GuestsLinkIn{Guest: "dana@client.example"}, mc); reason(err) != "owner_only" {
		t.Fatalf("a member made a link: %v", err)
	}
	if _, err := callErr(ctx, w.a, "guests.link", guests.GuestsLinkIn{Guest: "dana@client.example"}, ops.Caller{}); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("a link made by something that isn't a person: %v", err)
	}
	link := call[guests.GuestsLinkOut](t, ctx, w.a, "guests.link", guests.GuestsLinkIn{Guest: "dana@client.example"}, owner)
	if !strings.HasPrefix(link.URL, w.a.Cfg.GuestListener().Address()+guests.WelcomePath+"#") {
		t.Fatalf("the link opens %s", link.URL)
	}
	tok := link.URL[strings.Index(link.URL, "#")+1:]
	if len(tok) != 43 { // 256 bits in base64url
		t.Fatalf("token of %d characters", len(tok))
	}
	if d := time.Until(link.ExpiresAt); d > guests.LinkLife || d < guests.LinkLife-time.Minute {
		t.Fatalf("works for %s", d)
	}
	// Kept only as its SHA-256: the token itself is nowhere in the database.
	for _, coll := range []string{store.CGuestLinks, store.CGuests, store.CAudit, store.CGuestGrants} {
		if n, _ := w.a.DB.C(coll).CountDocuments(ctx, bson.M{"$expr": bson.M{"$regexMatch": bson.M{"input": bson.M{"$toString": "$$ROOT"}, "regex": tok}}}); n != 0 {
			t.Fatalf("the token is kept in %s", coll)
		}
	}
	// A second link takes the first back; the second works once.
	second := call[guests.GuestsLinkOut](t, ctx, w.a, "guests.link", guests.GuestsLinkIn{Guest: "dana@client.example"}, owner)
	if _, err := guests.Redeem(ctx, w.a, tok); reason(err) != guests.ReasonLinkGone {
		t.Fatalf("the first link still works: %v", err)
	}
	tok2 := second.URL[strings.Index(second.URL, "#")+1:]
	if _, err := guests.Redeem(ctx, w.a, tok2); err != nil {
		t.Fatal(err)
	}
	if _, err := guests.Redeem(ctx, w.a, tok2); reason(err) != guests.ReasonLinkGone {
		t.Fatalf("a link worked twice: %v", err)
	}
	// One past its time (the database's clock) doesn't work.
	third := call[guests.GuestsLinkOut](t, ctx, w.a, "guests.link", guests.GuestsLinkIn{Guest: "dana@client.example"}, owner)
	if _, err := w.a.DB.C(store.CGuestLinks).UpdateMany(ctx, bson.M{"workspaceId": w.a.WorkspaceID(), "usedAt": nil}, bson.M{"$set": bson.M{"expiresAt": time.Now().Add(-time.Second).UTC()}}); err != nil {
		t.Fatal(err)
	}
	if _, err := guests.Redeem(ctx, w.a, third.URL[strings.Index(third.URL, "#")+1:]); reason(err) != guests.ReasonLinkGone {
		t.Fatalf("an ended link worked: %v", err)
	}
	// Made-up tokens of every shape are the same answer.
	for _, bad := range []string{"", "x", strings.Repeat("A", 43), strings.Repeat("A", 44), "../../etc/passwd"} {
		if _, err := guests.Redeem(ctx, w.a, bad); reason(err) != guests.ReasonLinkGone {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// A link never outlives what was shared.
	if _, err := w.a.DB.C(store.CGuestGrants).UpdateMany(ctx, bson.M{"workspaceId": w.a.WorkspaceID()}, bson.M{"$set": bson.M{"endsAt": time.Now().Add(time.Hour).UTC()}}); err != nil {
		t.Fatal(err)
	}
	short := call[guests.GuestsLinkOut](t, ctx, w.a, "guests.link", guests.GuestsLinkIn{Guest: "dana@client.example"}, owner)
	if time.Until(short.ExpiresAt) > time.Hour+time.Minute {
		t.Fatalf("a link outlives the grant: %s", short.ExpiresAt)
	}
}

func TestWhatMayLeaveKeepsAClientsThingsFromGuests(t *testing.T) {
	w := newWorld(t, testapp.Options{})
	ctx := context.Background()
	// Shared first, while nothing keeps it here.
	w.share(t, "dana@client.example", models.GuestCommenter)
	s, _ := w.signIn(t, "dana@client.example")
	// The client's rules then keep footage from guests: their page refuses it.
	call[ops.ContentShowOut](t, ctx, w.a, "content.own", ops.ContentOwnIn{ProjectID: w.p.ID}, owner)
	call[leave.LeaveOut](t, ctx, w.a, "content.leave.set", leave.LeaveSetIn{ProjectID: w.p.ID, Keep: map[string][]string{models.PlaceGuests: {models.LeaveFootage}}}, owner)
	if _, err := w.asGuest(s, "guest.watch", guests.GuestWatchIn{Version: w.revs[0]}); apperr.From(err).Code != apperr.KeptHere || reason(err) != guests.ReasonKeptHere {
		t.Fatalf("watched what the client keeps here: %v", err)
	}
	at := 0.5
	if _, err := w.asGuest(s, "guest.comment", guests.GuestCommentIn{Version: w.revs[0], Body: "x", At: &at}); reason(err) != guests.ReasonKeptHere {
		t.Fatalf("commented on what the client keeps here: %v", err)
	}
	out, _ := w.asGuest(s, "guest.shared", guests.GuestSharedIn{})
	if !out.(*guests.GuestSharedOut).Shares[0].Kept {
		t.Fatal("the page doesn't say it's kept")
	}
	// A new share is refused in words, and recorded.
	_, err := callErr(ctx, w.a, "guests.share", guests.GuestsShareIn{Email: "eve@client.example", ProjectID: w.p.ID, Role: models.GuestViewer, Days: 3}, owner)
	if apperr.From(err).Code != apperr.KeptHere {
		t.Fatalf("shared what the client keeps here: %v", err)
	}
	if n, _ := w.a.DB.C(store.CAudit).CountDocuments(ctx, bson.M{"workspaceId": w.a.WorkspaceID(), "action": "leave.refused"}); n == 0 {
		t.Fatal("the refusal wasn't recorded")
	}
	// A dry run says so without recording.
	plan := call[guests.GuestsShareOut](t, ctx, w.a, "guests.share", guests.GuestsShareIn{Email: "eve@client.example", ProjectID: w.p.ID, Role: models.GuestViewer, Days: 3, DryRun: true}, owner)
	if plan.May || plan.Code != apperr.KeptHere {
		t.Fatalf("plan: %+v", plan)
	}
	// Another client's project (its own Content Settings) is untouched.
	call[ops.ContentShowOut](t, ctx, w.a, "content.own", ops.ContentOwnIn{ProjectID: w.acme.ID}, owner)
	call[guests.GuestsShareOut](t, ctx, w.a, "guests.share", guests.GuestsShareIn{Email: "eve@client.example", ProjectID: w.acme.ID, Role: models.GuestViewer, Days: 3}, owner)
	// The receipt lists what was shared with guests.
	var gr []models.GuestGrant
	_ = w.a.DB.Find(ctx, store.CGuestGrants, w.a.WorkspaceID(), bson.M{"projectId": w.p.ID}, &gr)
	if len(gr) != 1 || len(gr[0].Kinds) == 0 || len(gr[0].Projects) == 0 {
		t.Fatalf("grants: %+v", gr)
	}
}

func TestGuestsSignInOnlyWithAnEmailTheProviderVouchesFor(t *testing.T) {
	w := newWorld(t, testapp.Options{})
	ctx := context.Background()
	w.share(t, "dana@client.example", models.GuestViewer)
	w.share(t, "old@client.example", models.GuestViewer)
	call[guests.GuestsRevokeOut](t, ctx, w.a, "guests.revoke", guests.GuestsRevokeIn{Guest: "old@client.example"}, owner)
	id := func(email string, trusted bool, key string) *sso.Identity {
		return &sso.Identity{Provider: sso.OIDC, Issuer: "https://login.example", Subject: key, Key: key, Email: email, EmailTrusted: trusted, Scope: "oidc:https://login.example"}
	}
	cases := []struct {
		name   string
		id     *sso.Identity
		reason string
	}{
		{"an email the provider doesn't vouch for", id("dana@client.example", false, "k1"), guests.ReasonNotTrusted},
		{"a Microsoft guest or personal account", &sso.Identity{Provider: sso.Microsoft, Key: "k2", Email: "dana@client.example", EmailTrusted: false, Guest: true, Scope: "microsoft:t"}, guests.ReasonMSGuest},
		{"another email than the one shared with", id("mallory@client.example", true, "k3"), guests.ReasonNotShared},
		{"a guest taken back", id("old@client.example", true, "k4"), guests.ReasonNotShared},
		{"no email", id("", true, "k5"), guests.ReasonNotTrusted},
	}
	for _, c := range cases {
		if g, err := guests.ForSSO(ctx, w.a, c.id.Provider, c.id); reason(err) != c.reason {
			t.Errorf("%s: %v %v", c.name, g, err)
		}
	}
	// The vouched-for email signs in, and its account is theirs from then on.
	g, err := guests.ForSSO(ctx, w.a, sso.OIDC, id("Dana@Client.example", true, "acct-1"))
	if err != nil || g.Email != "dana@client.example" {
		t.Fatalf("%v %+v", err, g)
	}
	if _, err := guests.ForSSO(ctx, w.a, sso.OIDC, id("dana@client.example", true, "acct-1")); err != nil {
		t.Fatalf("their own account again: %v", err)
	}
	if _, err := guests.ForSSO(ctx, w.a, sso.OIDC, id("dana@client.example", true, "acct-2")); reason(err) != guests.ReasonOtherAcct {
		t.Fatalf("another account showing their email: %v", err)
	}
}

func TestOwnersLookAtWhatAGuestSeesButNeverActAsOne(t *testing.T) {
	w := newWorld(t, testapp.Options{})
	ctx := context.Background()
	w.share(t, "dana@client.example", models.GuestCommenter, w.revs[0])
	view := call[guests.GuestSharedOut](t, ctx, w.a, "guest.shared", guests.GuestSharedIn{As: "dana@client.example"}, owner)
	if len(view.Shares) != 1 || view.Shares[0].Versions[0].ID != w.revs[0] {
		t.Fatalf("an owner's look: %+v", view)
	}
	mctx, mc := member()
	if _, err := callErr(mctx, w.a, "guest.shared", guests.GuestSharedIn{As: "dana@client.example"}, mc); reason(err) != "owner_only" {
		t.Fatalf("a member looked as a guest: %v", err)
	}
	if _, err := callErr(ctx, w.a, "guest.shared", guests.GuestSharedIn{}, owner); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("no guest named: %v", err)
	}
	// Claude in the panel and AI apps never reach the guest API (people only).
	ai := *w.a
	ai.Actor.Via = models.ViaAssistant
	if _, err := callErr(ctx, &ai, "guest.shared", guests.GuestSharedIn{As: "dana@client.example"}, ops.Caller{}); err == nil {
		t.Fatal("the assistant looked as a guest")
	}
	if _, err := callErr(ctx, w.a, "guest.shared", guests.GuestSharedIn{As: "dana@client.example"}, ops.Caller{}); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("something that isn't a person looked as a guest: %v", err)
	}
}

func TestWatchingSignsStraightFromTheBucket(t *testing.T) {
	b := fakebucket.Start(t)
	w := newWorld(t, testapp.Options{Media: &config.Media{Provider: "s3", S3: config.S3{Endpoint: b.URL, Region: "us-east-1", Bucket: b.Bucket,
		Prefix: "team", AccessKeyID: b.KeyID, SecretAccessKey: b.Secret, ForcePathStyle: true}}})
	w.share(t, "dana@client.example", models.GuestViewer)
	s, _ := w.signIn(t, "dana@client.example")
	out, err := w.asGuest(s, "guest.watch", guests.GuestWatchIn{Version: w.revs[1]})
	if err != nil {
		t.Fatal(err)
	}
	o := out.(*guests.GuestWatchOut)
	if !strings.HasPrefix(o.URL, b.URL+"/") || o.Until == nil || time.Until(*o.Until) > guests.MediaLife || strings.Contains(o.URL, "/guest/media/") {
		t.Fatalf("not a short-lived link from the bucket: %+v", o)
	}
	code, body := fetch(t, o.URL)
	if code != 200 || len(body) != int(w.rnd[w.revs[1]].Output.Size) {
		t.Fatalf("the bucket answered %d (%d bytes)", code, len(body))
	}
}

func TestWatchingMediaKeptHereGoesThroughTheGuestListener(t *testing.T) {
	w := newWorld(t, testapp.Options{})
	w.share(t, "dana@client.example", models.GuestViewer)
	s, _ := w.signIn(t, "dana@client.example")
	out, err := w.asGuest(s, "guest.watch", guests.GuestWatchIn{Version: w.revs[1]})
	if err != nil {
		t.Fatal(err)
	}
	o := out.(*guests.GuestWatchOut)
	if o.URL != guests.MediaPath(w.revs[1], w.rnd[w.revs[1]].ID) || o.Until != nil {
		t.Fatalf("watch: %+v", o)
	}
	// The listener's check: the render must be the version's review copy.
	ctx := context.Background()
	if _, _, err := guests.MediaFor(ctx, w.a, s, w.revs[1], w.rnd[w.revs[0]].ID); reason(err) != guests.ReasonNotShared {
		t.Fatalf("another version's render: %v", err)
	}
	if _, _, err := guests.MediaFor(ctx, w.a, s, w.acmeV, w.rnd[w.acmeV].ID); reason(err) != guests.ReasonNotShared {
		t.Fatalf("another project's render: %v", err)
	}
	if rd, _, err := guests.MediaFor(ctx, w.a, s, w.revs[1], w.rnd[w.revs[1]].ID); err != nil || rd.ID != w.rnd[w.revs[1]].ID {
		t.Fatalf("its own: %v", err)
	}
}

func TestGuestsListAndStatusSayItPlainly(t *testing.T) {
	w := newWorld(t, testapp.Options{})
	ctx := context.Background()
	w.share(t, "dana@client.example", models.GuestCommenter, w.revs[0])
	w.signIn(t, "dana@client.example")
	mctx, mc := member()
	list := call[guests.GuestsListOut](t, mctx, w.a, "guests.list", guests.GuestsListIn{}, mc) // members read who has what
	if len(list.Guests) != 1 || list.Guests[0].Live != 1 || list.Guests[0].SignedIn != 1 || !strings.Contains(list.Guests[0].Grants[0].Summary, "Version 1 of “Spring promo”: can watch and comment") {
		t.Fatalf("list: %+v", list)
	}
	st := call[guests.GuestsStatusOut](t, ctx, w.a, "guests.status", guests.GuestsStatusIn{}, ops.Caller{})
	if !st.On || !st.Listening || st.Internet || st.Port != config.DefaultGuestPort || st.Address != "http://127.0.0.1:4846" || st.Guests != 1 || st.SignedIn != 1 {
		t.Fatalf("status: %+v", st)
	}
	if !strings.Contains(strings.Join(st.Lines, " "), "Nothing is reachable from the internet yet") {
		t.Fatalf("lines: %v", st.Lines)
	}
	// Settings: owners only; a port that isn't for people's programs, or is
	// the dashboard's, is refused.
	if _, err := callErr(mctx, w.a, "guests.settings.set", guests.GuestsSettingsSetIn{On: ptr(false)}, mc); reason(err) != "owner_only" {
		t.Fatalf("a member turned guests off: %v", err)
	}
	for _, p := range []int{80, 70000, w.a.Cfg.Dashboard.Port} {
		if _, err := callErr(ctx, w.a, "guests.settings.set", guests.GuestsSettingsSetIn{Port: ptr(p)}, owner); reason(err) != "port" {
			t.Errorf("port %d: %v", p, err)
		}
	}
	got := call[guests.GuestsStatusOut](t, ctx, w.a, "guests.settings.set", guests.GuestsSettingsSetIn{Port: ptr(5829)}, owner)
	if got.Port != 5829 {
		t.Fatalf("port: %d", got.Port)
	}
	if l, _ := config.LoadGuests(w.a.Cfg.Home()); l.Port != 5829 || !l.Listen {
		t.Fatalf("config.yaml: %+v", l)
	}
	// Turning guests on happens at this computer, not from another one.
	ectx := ops.OverHTTP(context.Background(), func() bool { return true })
	if _, err := callErr(ectx, w.a, "guests.settings.set", guests.GuestsSettingsSetIn{On: ptr(true)}, ops.Caller{Human: true, Remote: true, Elsewhere: true}); reason(err) != "here_only" {
		t.Fatalf("turned on from another computer: %v", err)
	}
}

func TestEveryRequestIsRecordedAndUnusualVolumeTold(t *testing.T) {
	w := newWorld(t, testapp.Options{})
	ctx := context.Background()
	w.share(t, "dana@client.example", models.GuestViewer)
	s, _ := w.signIn(t, "dana@client.example")
	guests.Record(ctx, w.a, guests.Hit{Guest: &s.Guest, What: "guest.watch", ProjectID: w.p.ID, RevisionID: w.revs[1], From: "127.0.0.1", Status: 200})
	guests.Record(ctx, w.a, guests.Hit{What: "guest.signin.refused", From: "127.0.0.1", Status: 401, Refused: true, Reason: guests.ReasonLinkGone})
	// A flood from nobody signed in is one record a minute.
	for i := 0; i < 5; i++ {
		guests.Record(ctx, w.a, guests.Hit{What: "guest.refused", From: "127.0.0.9", Status: 403, Refused: true, Reason: guests.ReasonOther})
	}
	mctx, mc := member()
	if _, err := callErr(mctx, w.a, "guests.activity", guests.GuestsActivityIn{}, mc); reason(err) != "owner_only" {
		t.Fatalf("a member read guests' activity: %v", err)
	}
	act := call[guests.GuestsActivityOut](t, ctx, w.a, "guests.activity", guests.GuestsActivityIn{}, ops.Caller{})
	if len(act.Rows) != 3 {
		t.Fatalf("%d rows: %+v", len(act.Rows), act.Rows)
	}
	var says []string
	for _, r := range act.Rows {
		says = append(says, r.Who+" "+r.Says)
	}
	all := strings.Join(says, " | ")
	for _, want := range []string{"Dana (guest) opened version 2 of “Spring promo”, from 127.0.0.1", "someone not signed in tried to sign in — turned away (the sign-in link didn't work)"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in %s", want, all)
		}
	}
	// Unusual volume: owners are told once.
	guests.VolumePerHour = 3
	t.Cleanup(func() { guests.VolumePerHour = 600 })
	for i := 0; i < 6; i++ {
		guests.Record(ctx, w.a, guests.Hit{Guest: &s.Guest, What: "guest.shared", From: "127.0.0.1", Status: 200})
	}
	n, _ := w.a.DB.C(store.CNotifications).CountDocuments(ctx, bson.M{"workspaceId": w.a.WorkspaceID(), "kind": models.NotifyGuestVolume})
	if n != 1 {
		t.Fatalf("%d notes about unusual volume", n)
	}
	act = call[guests.GuestsActivityOut](t, ctx, w.a, "guests.activity", guests.GuestsActivityIn{}, ops.Caller{})
	if len(act.Unusual) != 1 || !strings.Contains(act.Summary, "Unusually many") {
		t.Fatalf("unusual: %+v", act)
	}
}

func TestClearingAProjectOutEndsItsGrants(t *testing.T) {
	w := newWorld(t, testapp.Options{})
	ctx := context.Background()
	w.share(t, "dana@client.example", models.GuestViewer)
	_, cookie := w.signIn(t, "dana@client.example")
	call[ops.ProjectArchiveOut](t, ctx, w.a, "project.delete", ops.ProjectDeleteIn{ID: w.p.ID}, owner)
	call[ops.ProjectPurgeOut](t, ctx, w.a, "project.purge", ops.ProjectPurgeIn{ID: w.p.ID, Forever: true}, owner)
	var gr models.GuestGrant
	if err := w.a.DB.C(store.CGuestGrants).FindOne(ctx, bson.M{"workspaceId": w.a.WorkspaceID()}).Decode(&gr); err != nil || gr.RevokedAt == nil || gr.EndReason == "" {
		t.Fatalf("the grant: %v %+v", err, gr)
	}
	if _, err := guests.Check(ctx, w.a, cookie, "127.0.0.1"); err == nil {
		t.Fatal("still signed in")
	}
}

func fetch(t *testing.T, u string) (int, []byte) {
	t.Helper()
	res, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}
