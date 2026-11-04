package guests_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/guests"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/sso"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/withdrawn"
)

// A version that plays anything made with a consent that was withdrawn —
// here a generated picture of Ana in its graphics — is never shown to guests
// (D227): watching it, its video, a video already playing and commenting on it
// are all refused the moment the consent is withdrawn, and it can't be shared
// by name. A version nesting more cuts than are looked through isn't clear
// either.
func TestGuestsNeverSeeWhatAWithdrawnConsentMade(t *testing.T) {
	w := newWorld(t, testapp.Options{})
	ctx := context.Background()
	now := time.Now().UTC()
	w.share(t, "dana@client.example", models.GuestCommenter)
	s, _ := w.signIn(t, "dana@client.example")
	c := models.Consent{ID: ids.New(ids.Consent), WorkspaceID: w.a.WorkspaceID(), PersonID: ids.New(ids.Person), PersonName: "Ana Ruiz", Statement: "Yes.", RecordedAt: now}
	if err := w.a.DB.Insert(ctx, store.CConsents, c); err != nil {
		t.Fatal(err)
	}
	picture := ids.New(ids.Resource)
	g := models.Generation{ID: ids.New(ids.Generation), WorkspaceID: w.a.WorkspaceID(), ProjectID: w.p.ID, Kind: "picture", ConsentIDs: []string{c.ID},
		Outcome: &models.GenOutcome{State: models.GenDone, At: now, Outputs: []models.GenOutput{{ResourceID: picture, MIME: "image/png"}}}, CreatedAt: now}
	if err := w.a.DB.Insert(ctx, store.CGenerations, g); err != nil {
		t.Fatal(err)
	}
	if err := w.a.DB.Update(ctx, store.CRevisions, w.a.WorkspaceID(), w.revs[1], bson.M{"$set": bson.M{"spec.resources.face": editspec.ResourcePin{ID: picture, SHA256: strings.Repeat("ab", 32)}}}); err != nil {
		t.Fatal(err)
	}
	render := w.rnd[w.revs[1]].ID
	if _, err := w.asGuest(s, "guest.watch", guests.GuestWatchIn{Version: w.revs[1]}); err != nil {
		t.Fatalf("while the consent holds: %v", err)
	}
	// Ana withdraws her consent (as consent.revoke records it).
	if err := w.a.DB.Update(ctx, store.CConsents, w.a.WorkspaceID(), c.ID, bson.M{"$set": bson.M{"revokedAt": now}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.asGuest(s, "guest.watch", guests.GuestWatchIn{Version: w.revs[1]}); reason(err) != guests.ReasonConsent {
		t.Fatalf("watched after the consent was withdrawn: %v", err)
	}
	if _, _, err := guests.MediaFor(ctx, w.a, s, w.revs[1], render); reason(err) != guests.ReasonConsent {
		t.Fatalf("its video: %v", err)
	}
	if guests.StillShown(ctx, w.a, s, w.revs[1], render) {
		t.Fatal("a video already playing goes on")
	}
	if _, err := w.asGuest(s, "guest.comment", guests.GuestCommentIn{Version: w.revs[1], Body: "x"}); reason(err) != guests.ReasonConsent {
		t.Fatalf("commented on it: %v", err)
	}
	if _, err := w.asGuest(s, "guest.watch", guests.GuestWatchIn{Version: w.revs[0]}); err != nil {
		t.Fatalf("a version that doesn't play it: %v", err)
	}
	if _, err := callErr(ctx, w.a, "guests.share", guests.GuestsShareIn{Email: "vic@client.example", ProjectID: w.p.ID, Versions: []string{w.revs[1]}, Role: models.GuestViewer, Days: 7}, owner); reason(err) != guests.ReasonConsent {
		t.Fatalf("shared by name: %v", err)
	}
	// Past the nested cuts a check looks through, it can't be said to be clear.
	old := withdrawn.MaxNested
	withdrawn.MaxNested = 1
	t.Cleanup(func() { withdrawn.MaxNested = old })
	nested := editspec.Clip{ID: "n", AssetID: editspec.NestedPrefix + w.acme.ID + "/" + w.acmeV, In: 0, Out: 1}
	if err := w.a.DB.Update(ctx, store.CRevisions, w.a.WorkspaceID(), w.revs[0], bson.M{"$push": bson.M{"spec.clips": nested}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.asGuest(s, "guest.watch", guests.GuestWatchIn{Version: w.revs[0]}); reason(err) != guests.ReasonTooLarge ||
		!strings.Contains(apperr.From(err).Message, "too large to share with a guest") {
		t.Fatalf("a version nesting too many cuts to look through: %v", err)
	}
}

// A version with more in it than the checks look through is too large to
// share with a guest: said plainly, every time — never "try again in a
// moment" — to the guest and to an owner sharing it. Sharing the whole
// project still works; the guest just never sees that version.
func TestTooLargeToShareWithAGuest(t *testing.T) {
	w := newWorld(t, testapp.Options{})
	ctx := context.Background()
	w.share(t, "dana@client.example", models.GuestViewer)
	s, _ := w.signIn(t, "dana@client.example")
	old := leave.MaxThings
	leave.MaxThings = 2
	t.Cleanup(func() { leave.MaxThings = old })
	for i := 0; i < 2; i++ {
		_, err := w.asGuest(s, "guest.watch", guests.GuestWatchIn{Version: w.revs[0]})
		if e := apperr.From(err); reason(err) != guests.ReasonTooLarge || !strings.Contains(e.Message, "too large to share with a guest") || strings.Contains(e.Hint, "Try again") {
			t.Fatalf("watching a version too large to check: %v", err)
		}
	}
	if _, err := callErr(ctx, w.a, "guests.share", guests.GuestsShareIn{Email: "vic@client.example", ProjectID: w.p.ID, Versions: []string{w.revs[0]}, Role: models.GuestViewer, Days: 7}, owner); reason(err) != guests.ReasonTooLarge {
		t.Fatalf("shared by name: %v", err)
	}
	out := w.share(t, "vic@client.example", models.GuestViewer)
	if !strings.Contains(strings.Join(out.Warnings, " "), "too large to share with a guest") {
		t.Fatalf("the project's share doesn't say a version is too large: %+v", out.Warnings)
	}
}

// A project's grant reaches the versions its page lists — those with a
// finished review copy — and nothing else, even by ID. A version named in a
// grant of its own is the guest's before it has one.
func TestAProjectGrantReachesOnlyTheVersionsItLists(t *testing.T) {
	w := newWorld(t, testapp.Options{})
	ctx := context.Background()
	w.share(t, "dana@client.example", models.GuestCommenter)
	s, cookie := w.signIn(t, "dana@client.example")
	for _, step := range []struct {
		op string
		in any
	}{
		{"guest.watch", guests.GuestWatchIn{Version: w.revs[2]}},
		{"guest.comments", guests.GuestCommentsIn{Version: w.revs[2]}},
		{"guest.comment", guests.GuestCommentIn{Version: w.revs[2], Body: "x"}},
	} {
		if _, err := w.asGuest(s, step.op, step.in); reason(err) != guests.ReasonNotShared {
			t.Errorf("%s of a version without a review copy: %v", step.op, err)
		}
	}
	w.share(t, "dana@client.example", models.GuestCommenter, w.revs[2])
	s, err := guests.Check(ctx, w.a, cookie, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.asGuest(s, "guest.watch", guests.GuestWatchIn{Version: w.revs[2]}); reason(err) != guests.ReasonNotReady {
		t.Fatalf("a version shared by name, before its review copy: %v", err)
	}
	if _, err := w.asGuest(s, "guest.comment", guests.GuestCommentIn{Version: w.revs[2], Body: "Looking forward to it"}); err != nil {
		t.Fatalf("a comment on it: %v", err)
	}
}

// Guests never see a version that plays someone else's video (a reference),
// even after a person sent it out anyway with a note: the note covers the
// gate, not guests.
func TestReferencesStayHiddenFromGuestsEvenSentAnyway(t *testing.T) {
	w := newWorld(t, testapp.Options{})
	ctx := context.Background()
	now := time.Now().UTC()
	w.share(t, "dana@client.example", models.GuestViewer)
	s, _ := w.signIn(t, "dana@client.example")
	if _, err := w.a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": w.clip.ID}, bson.M{"$set": bson.M{"reference": models.AssetReference{By: w.a.Actor, At: now, Why: models.ReferenceMarked}}}); err != nil {
		t.Fatal(err)
	}
	key := "reference:" + w.clip.ID + "@" + w.revs[1]
	note := models.ShipOverride{ID: "ovr_1", By: w.a.Actor, At: now, Note: "The client cleared the stand-in.", Holds: []models.ShipHold{{Key: key, Kind: models.HoldReference}}}
	if err := w.a.DB.Update(ctx, store.CRevisions, w.a.WorkspaceID(), w.revs[1], bson.M{"$push": bson.M{"shipOverrides": note}}); err != nil {
		t.Fatal(err)
	}
	var rev models.Revision
	if err := w.a.DB.GetByID(ctx, store.CRevisions, w.a.WorkspaceID(), w.revs[1], &rev); err != nil {
		t.Fatal(err)
	}
	gate, err := ops.ShipGate(ctx, w.a, &w.p, &rev, "review", nil)
	if err != nil {
		t.Fatal(err)
	}
	covered := false
	for _, h := range gate.Holds {
		covered = covered || (h.Kind == models.HoldReference && h.Covered)
	}
	if !covered {
		t.Fatalf("the note doesn't cover the reference: %+v", gate.Holds)
	}
	if _, err := w.asGuest(s, "guest.watch", guests.GuestWatchIn{Version: w.revs[1]}); reason(err) != guests.ReasonHeld {
		t.Fatalf("a reference sent out anyway was shown to a guest: %v", err)
	}
}

// What a guest types reaches the activity log as its size only: the log
// keeps the IDs their request named, never their words (people and AI apps
// read the log without the quoting comments get elsewhere).
func TestAGuestsWordsStayOutOfTheActivityLog(t *testing.T) {
	w := newWorld(t, testapp.Options{})
	ctx := context.Background()
	w.share(t, "dana@client.example", models.GuestCommenter, w.revs[0])
	s, _ := w.signIn(t, "dana@client.example")
	words := "Hold the last shot longer, zebra-quartz."
	if _, err := w.asGuest(s, "guest.comment", guests.GuestCommentIn{Version: w.revs[0], Body: words}); err != nil {
		t.Fatal(err)
	}
	w.a.Log.Flush(ctx)
	var entries []struct {
		Op *struct {
			Name  string `bson:"name"`
			Input bson.M `bson:"input"`
		} `bson:"op"`
	}
	cur, err := w.a.DB.C(store.CLogEntries).Find(ctx, bson.M{})
	if err != nil {
		t.Fatal(err)
	}
	if err := cur.All(ctx, &entries); err != nil {
		t.Fatal(err)
	}
	var raw []bson.M
	if cur, err := w.a.DB.C(store.CLogEntries).Find(ctx, bson.M{}); err == nil {
		_ = cur.All(ctx, &raw)
	}
	all, _ := json.Marshal(raw)
	if strings.Contains(string(all), "zebra-quartz") {
		t.Fatal("a guest's words are in the activity log")
	}
	found := false
	for _, e := range entries {
		if e.Op == nil || e.Op.Name != "guest.comment" {
			continue
		}
		found = true
		if e.Op.Input["body"] != "[40 characters]" || e.Op.Input["version"] != w.revs[0] {
			t.Fatalf("what the log kept of the comment: %v", e.Op.Input)
		}
	}
	if !found {
		t.Fatal("the comment isn't in the activity log")
	}
}

// A provider's vouched-for email matches a guest's in any case, letters beyond
// A–Z too, but the Kelvin sign, which Unicode's lower case turns into k,
// doesn't sign in as kate@….
func TestLookAlikeLettersDontMatchAGuest(t *testing.T) {
	w := newWorld(t, testapp.Options{})
	ctx := context.Background()
	w.share(t, "kate@client.example", models.GuestViewer)
	w.share(t, "\u00c9mile@Client.example", models.GuestViewer)
	id := func(email, key string) *sso.Identity {
		return &sso.Identity{Provider: sso.OIDC, Issuer: "https://login.example", Subject: key, Key: key, Email: email, EmailTrusted: true, Scope: "oidc:https://login.example"}
	}
	if g, err := guests.ForSSO(ctx, w.a, sso.OIDC, id("\u212Aate@client.example", "k1")); reason(err) != guests.ReasonNotShared {
		t.Fatalf("the Kelvin sign signed in as kate: %v %+v", err, g)
	}
	if g, err := guests.ForSSO(ctx, w.a, sso.OIDC, id("KATE@Client.example", "k2")); err != nil || g.Email != "kate@client.example" {
		t.Fatalf("ASCII capitals: %v %+v", err, g)
	}
	if g, err := guests.ForSSO(ctx, w.a, sso.OIDC, id("\u00e9mile@client.example", "e1")); err != nil || g.Email != "\u00c9mile@Client.example" {
		t.Fatalf("a letter beyond A–Z in another case: %v %+v", err, g)
	}
	// Shared again, the email in another case: the same guest.
	w.share(t, "\u00c9MILE@client.example", models.GuestViewer)
	if n, _ := w.a.DB.C(store.CGuests).CountDocuments(ctx, bson.M{"workspaceId": w.a.WorkspaceID(), "emailKey": "\u00e9mile@client.example"}); n != 1 {
		t.Fatalf("%d guests for one email", n)
	}
}
