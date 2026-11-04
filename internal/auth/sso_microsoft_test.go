package auth_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/sso"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeoidc"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Microsoft's rules in who a sign-in is (D452), from the reviewer's case:
// Microsoft says xms_edov for addresses it took from personal accounts,
// Google accounts and one-time codes, so a token from another directory, or
// a guest's, or a personal account's, carrying a member's, an owner's or an
// invited person's email, must never be linked to them; only the directory's
// own members are found by their email. The tokens come from a stand-in at a
// directory's address on this computer (tests never reach Microsoft) and go
// through every check a real one does.

const msRedirect = "http://127.0.0.1:5799/auth/sso/callback"

// msStandIn is a stand-in for a Microsoft directory: its address names the
// directory, as Microsoft's do.
func msStandIn(t *testing.T, directory string) (*fakeoidc.Server, string) {
	t.Helper()
	secret := sso.Random(24)
	p := fakeoidc.New("ms-app", secret)
	t.Cleanup(p.Close)
	p.Issuer = p.URL + "/" + directory + "/v2.0"
	return p, secret
}

// msSignIn signs in through the stand-in as who, with the client a setup for
// the directory dir would use.
func msSignIn(t *testing.T, p *fakeoidc.Server, secret, dir string, who fakeoidc.Person) *sso.Identity {
	t.Helper()
	c := &sso.Client{Kind: sso.Microsoft, Issuer: p.Issuer, ClientID: "ms-app", Secret: secret, Directory: dir}
	p.Next = &who
	verifier := sso.Random(48)
	req := sso.Request{Redirect: msRedirect, State: sso.Random(32), Nonce: sso.Random(32), Challenge: sso.Challenge(verifier)}
	u, err := c.AuthURL(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequest("GET", u, nil)
	res, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	loc, _ := url.Parse(res.Header.Get("Location"))
	id, err := c.Exchange(context.Background(), loc.Query().Get("code"), verifier, msRedirect, req.Nonce)
	if err != nil {
		t.Fatalf("%s: %v", who.Sub, err)
	}
	return id
}

func msPerson(sub, email, tid string, extra map[string]any) fakeoidc.Person {
	claims := map[string]any{"tid": tid, "oid": "oid-" + sub, "xms_edov": true}
	for k, v := range extra {
		claims[k] = v
	}
	return fakeoidc.Person{Sub: sub, Email: email, Name: sub, Extra: claims}
}

// reasonOf is the reason an error gives in its details.
func reasonOf(err error) string {
	if err == nil {
		return ""
	}
	d, _ := apperr.From(err).Details.(map[string]any)
	r, _ := d["reason"].(string)
	return r
}

func TestMicrosoftGuestsAndOtherDirectoriesNeverJoinByEmail(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(sso.Forget)
	a := testapp.New(t)
	ctx := context.Background()
	ours := "11111111-2222-3333-4444-555555555555"
	theirs := "99999999-8888-7777-6666-555555555555"
	// The setup is for our directory, whose people may also join by themselves.
	st := &models.SSOSettings{Provider: sso.Microsoft, Tenant: ours, Issuer: sso.MicrosoftIssuer(ours), ClientID: "ms-app", Domain: ours}
	if _, err := a.DB.C(store.CWorkspaces).UpdateOne(ctx, bson.M{"_id": a.WorkspaceID()}, bson.M{"$set": bson.M{"auth.method": models.AuthSSO, "auth.sso": st},
		"$setOnInsert": bson.M{"workspaceId": a.WorkspaceID(), "name": "T", "createdAt": time.Now().UTC()}}, options.UpdateOne().SetUpsert(true)); err != nil {
		t.Fatal(err)
	}
	auth.ForgetSettings(a)
	now := time.Now().UTC()
	person := func(email, role string) *models.User {
		u := models.User{ID: ids.New("usr"), WorkspaceID: a.WorkspaceID(), Name: email, Email: email, EmailKey: auth.EmailKey(email), Role: role, State: models.UserActive, Source: "invite", CreatedAt: now}
		if err := a.DB.Insert(ctx, store.CUsers, u); err != nil {
			t.Fatal(err)
		}
		return &u
	}
	owner := person("sam@contoso.example", models.RoleOwner)
	maya := person("maya@contoso.example", models.RoleMember)
	carol, token, _, err := auth.Invite(ctx, a, "carol@contoso.example", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	unlinked := func(who ...*models.User) {
		t.Helper()
		for _, u := range who {
			got, _ := auth.GetUser(ctx, a, u.ID)
			if len(got.SSO) != 0 {
				t.Fatalf("%s was linked: %+v", u.Email, got.SSO)
			}
		}
		if got, _ := auth.GetUser(ctx, a, carol.ID); got.State != models.UserInvited {
			t.Fatal("the invite was accepted")
		}
	}

	// Another directory's account, its email vouched for by its own directory:
	// not the owner's, a member's or the invited person's.
	other, otherSecret := msStandIn(t, theirs)
	for _, email := range []string{owner.Email, maya.Email, carol.Email} {
		id := msSignIn(t, other, otherSecret, ours, msPerson("x-"+email, email, theirs, nil))
		if id.EmailTrusted || id.Guest {
			t.Fatalf("another directory's account: %+v", id)
		}
		if _, _, err := auth.PersonForSSO(ctx, a, st, id, nil); reasonOf(err) != auth.SSONotMember {
			t.Fatalf("another directory's account as %s: %v", email, err)
		}
	}
	unlinked(owner, maya)

	// Personal accounts (their own directory), guests and people another
	// provider signed in, in our directory: never by email, never by joining.
	personal, personalSecret := msStandIn(t, sso.MicrosoftPersonal)
	ourDir, ourSecret := msStandIn(t, ours)
	for name, id := range map[string]*sso.Identity{
		"a personal account":          msSignIn(t, personal, personalSecret, sso.MicrosoftPersonal, msPerson("p1", owner.Email, sso.MicrosoftPersonal, nil)),
		"a guest (acct 1)":            msSignIn(t, ourDir, ourSecret, ours, msPerson("g1", maya.Email, ours, map[string]any{"acct": 1})),
		"a personal account as guest": msSignIn(t, ourDir, ourSecret, ours, msPerson("g2", owner.Email, ours, map[string]any{"idp": "live.com"})),
		"a guest from elsewhere":      msSignIn(t, ourDir, ourSecret, ours, msPerson("g3", "new@contoso.example", ours, map[string]any{"idp": "https://sts.windows.net/" + theirs + "/"})),
	} {
		if id.EmailTrusted || !id.Guest {
			t.Fatalf("%s: %+v", name, id)
		}
		if _, _, err := auth.PersonForSSO(ctx, a, st, id, nil); reasonOf(err) != auth.SSOGuest {
			t.Fatalf("%s: %v", name, err)
		}
	}
	unlinked(owner, maya)

	// Not even the invite link: a guest showing the invited email is turned away.
	rec, _, err := auth.LookupLink(ctx, a, token)
	if err != nil {
		t.Fatal(err)
	}
	guest := msSignIn(t, ourDir, ourSecret, ours, msPerson("g4", carol.Email, ours, map[string]any{"acct": "1"}))
	if _, _, err := auth.PersonForSSO(ctx, a, st, guest, rec); reasonOf(err) != auth.SSOGuest {
		t.Fatalf("a guest with the invite link: %v", err)
	}
	if _, _, err := auth.LookupLink(ctx, a, token); err != nil {
		t.Fatal("a guest used up the invite")
	}
	unlinked(owner, maya)

	// Our directory's own members: found by their email (acct 0), and a new
	// one joins from the directory.
	id := msSignIn(t, ourDir, ourSecret, ours, msPerson("m1", maya.Email, ours, map[string]any{"acct": 0}))
	if u, _, err := auth.PersonForSSO(ctx, a, st, id, nil); err != nil || u.ID != maya.ID {
		t.Fatalf("a member of the directory: %v %v", u, err)
	}
	id = msSignIn(t, ourDir, ourSecret, ours, msPerson("m2", "dana@contoso.example", ours, nil))
	if u, joined, err := auth.PersonForSSO(ctx, a, st, id, nil); err != nil || !joined || u.Role != models.RoleMember || u.Email != "dana@contoso.example" {
		t.Fatalf("a new member of the directory: %+v %v %v", u, joined, err)
	}
	// The invited person, a member, accepts with the link.
	id = msSignIn(t, ourDir, ourSecret, ours, msPerson("m3", carol.Email, ours, nil))
	if u, joined, err := auth.PersonForSSO(ctx, a, st, id, rec); err != nil || !joined || u.ID != carol.ID {
		t.Fatalf("the invited member: %v %v", u, err)
	}
	if got, _ := auth.GetUser(ctx, a, owner.ID); len(got.SSO) != 0 {
		t.Fatal("the owner was linked")
	}
}
