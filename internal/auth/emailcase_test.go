package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/sso"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// People's emails match in any case, letters beyond A–Z too: a member kept as
// Émile@… signs in with a password, gets a reset link, is invited again and is
// found by an organization's sign-in, typed or sent as émile@…. The two
// characters whose lower case is an ASCII letter keep their own key: the
// Kelvin sign's "Kate" is never kate@….
func TestEmailsMatchInAnyCaseButLookAlikes(t *testing.T) {
	keyring.MockInit()
	a := testapp.New(t)
	ctx := context.Background()
	const pw = "sunflowers by the gate"
	if _, err := SetUpPassword(ctx, a, a.Cfg.User.ID, "owner@studio.example", "tomato garden at dusk", ""); err != nil {
		t.Fatal(err)
	}
	member := func(email, name string) *models.User {
		t.Helper()
		u, token, _, err := Invite(ctx, a, email, name, models.RoleMember, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := UseLink(ctx, a, token, name, pw); err != nil {
			t.Fatal(err)
		}
		return u
	}
	emile := member("Émile@Studio.example", "Émile")
	member("kate@client.example", "Kate")

	// Signing in with a password, the email typed in another case.
	for _, typed := range []string{"émile@studio.example", "ÉMILE@STUDIO.EXAMPLE"} {
		if u, err := Authenticate(ctx, a, typed, pw); err != nil || u.ID != emile.ID {
			t.Fatalf("signing in as %q: %v", typed, err)
		}
	}
	if _, err := Authenticate(ctx, a, "Kate@client.example", pw); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("the Kelvin sign signed in as kate: %v", err)
	}
	if u, _ := FindByEmail(ctx, a, "Kate@client.example"); u != nil {
		t.Fatalf("the Kelvin sign found %s", u.Email)
	}

	// A reset link for the email in another case.
	reset, err := ops.CallTyped[UserLinkOut](ctx, a, "users.resetLink", UserRefIn{User: "émile@studio.example"}, ops.Caller{Human: true})
	if err != nil || reset.Account.ID != emile.ID || reset.Kind != LinkReset {
		t.Fatalf("a reset link: %+v %v", reset, err)
	}

	// Invited again in another case: the same person, not a second one.
	annie, _, _, err := Invite(ctx, a, "Ännie@Client.example", "Ännie", models.RoleMember, "")
	if err != nil {
		t.Fatal(err)
	}
	again, _, _, err := Invite(ctx, a, "ännie@client.example", "", models.RoleMember, "")
	if err != nil || again.ID != annie.ID {
		t.Fatalf("invited again: %+v %v", again, err)
	}
	if n, _ := a.DB.C(store.CUsers).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "emailKey": "ännie@client.example"}); n != 1 {
		t.Fatalf("%d people for one email", n)
	}

	// An organization's sign-in: the email the provider vouches for, in
	// another case, is the member's; the Kelvin sign's isn't kate's.
	st := &models.SSOSettings{Provider: sso.OIDC, Issuer: "https://login.example", ClientID: "app"}
	id := func(email, key string) *sso.Identity {
		return &sso.Identity{Provider: sso.OIDC, Issuer: "https://login.example", Subject: key, Key: key, Email: email, EmailTrusted: true, Scope: "oidc:https://login.example"}
	}
	if u, _, err := PersonForSSO(ctx, a, st, id("ÉMILE@studio.example", "sub-emile"), nil); err != nil || u.ID != emile.ID {
		t.Fatalf("the provider's émile@…: %+v %v", u, err)
	}
	if u, _, err := PersonForSSO(ctx, a, st, id("Kate@client.example", "sub-kelvin"), nil); err == nil {
		t.Fatalf("the Kelvin sign signed in as %s", u.Email)
	}
	// Following an invite: the provider's email, in another case, is the invited one.
	ole, token, _, err := Invite(ctx, a, "Øle@Studio.example", "Øle", models.RoleMember, "")
	if err != nil {
		t.Fatal(err)
	}
	rec, _, err := LookupLink(ctx, a, token)
	if err != nil {
		t.Fatal(err)
	}
	if u, joined, err := PersonForSSO(ctx, a, st, id("øle@studio.example", "sub-ole"), rec); err != nil || u.ID != ole.ID || !joined {
		t.Fatalf("an invite followed with the email in another case: %+v %v %v", u, joined, err)
	}
}
