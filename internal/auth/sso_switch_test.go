package auth

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/sso"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeoidc"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// The re-check's race (D450): a second owner on this computer forgets the
// setup waiting for its test, sets up another app or gives the waiting one a
// new secret inside the switch's window — after the tested secret is kept,
// before the switch is written — or the waiting secret is gone before it is
// kept, or the tested account is linked to someone else before the owner's
// link. Each time the switch fails plainly and nothing changes: the sign-in
// in use, its secret, the owner's accounts and the other owner's change stay
// as they are. Without anyone in between, the same switch goes through.

const switchRedirect = "http://127.0.0.1:5798/auth/sso/callback"

// switchSignIn signs in through the stand-in with a client, as the dashboard
// would, and says who it was.
func switchSignIn(t *testing.T, p *fakeoidc.Server, c *sso.Client, who fakeoidc.Person) *sso.Identity {
	t.Helper()
	p.Next = &who
	verifier := sso.Random(48)
	req := sso.Request{Redirect: switchRedirect, State: sso.Random(32), Nonce: sso.Random(32), Challenge: sso.Challenge(verifier)}
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
	id, err := c.Exchange(context.Background(), loc.Query().Get("code"), verifier, switchRedirect, req.Nonce)
	if err != nil {
		t.Fatalf("signing in as %s with %s: %v", who.Sub, c.ClientID, err)
	}
	return id
}

func switchReason(err error) string {
	if err == nil {
		return ""
	}
	d, _ := apperr.From(err).Details.(map[string]any)
	r, _ := d["reason"].(string)
	return r
}

func sameLinks(a, b []models.SSOLink) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key {
			return false
		}
	}
	return true
}

func TestASwitchRacedByAnotherOwnerChangesNothing(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(sso.Forget)
	t.Cleanup(func() { switchHook = nil })
	a := testapp.New(t)
	ctx := context.Background()
	secretA, secretB, secretB2, secretC := sso.Random(24), sso.Random(24), sso.Random(24), sso.Random(24)
	p := fakeoidc.New("app-a", secretA)
	t.Cleanup(p.Close)
	p.AddClient("app-b", secretB)
	p.AddClient("app-c", secretC)
	appA := &models.SSOSettings{Provider: sso.OIDC, Issuer: p.Issuer, ClientID: "app-a"}
	appB := &models.SSOSettings{Provider: sso.OIDC, Issuer: p.Issuer, ClientID: "app-b"}
	appC := &models.SSOSettings{Provider: sso.OIDC, Issuer: p.Issuer, ClientID: "app-c"}
	sam := fakeoidc.Person{Sub: "sub-sam", Email: "sam@example.com", EmailVerified: fakeoidc.Verified(true), Name: "Sam"}
	samElsewhere := fakeoidc.Person{Sub: "sub-sam-2", Email: "sam@example.com", EmailVerified: fakeoidc.Verified(true), Name: "Sam"}
	maya := fakeoidc.Person{Sub: "sub-maya", Email: "maya@example.com", EmailVerified: fakeoidc.Verified(true), Name: "Maya"}

	set := func(clientID, secret string) {
		t.Helper()
		if _, err := ssoSetOp(ctx, a, &SSOSetIn{Provider: "oidc", Issuer: p.Issuer, ClientID: clientID, ClientSecret: secret, Label: "Test provider"}); err != nil {
			t.Fatalf("set up %s: %v", clientID, err)
		}
	}
	fresh := func() models.AuthSettings {
		forgetSettings(a)
		return Settings(ctx, a)
	}
	// test is the owner's test sign-in with the setup a test uses, kept as
	// the dashboard's test page keeps it.
	test := func(who fakeoidc.Person) *sso.TestResult {
		t.Helper()
		use := TestUse(a.Cfg, fresh())
		id := switchSignIn(t, p, use.Client(), who)
		flows := sso.FlowsIn(a.Cfg.StateDir())
		if err := flows.SaveTest(sso.TestResult{Setup: use.Fingerprint(), UserID: you(a), OK: true, Identity: id}); err != nil {
			t.Fatal(err)
		}
		return flows.LastTest()
	}
	switchNow := func() error {
		_, _, err := SetUpSSO(ctx, a, you(a), sso.FlowsIn(a.Cfg.StateDir()).LastTest())
		return err
	}

	// A in use; Maya, a member, signs in with it.
	set("app-a", secretA)
	test(sam)
	if err := switchNow(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	mayaRec := models.User{ID: ids.New("usr"), WorkspaceID: a.WorkspaceID(), Name: "Maya", Email: "maya@example.com", EmailKey: EmailKey("maya@example.com"),
		Role: models.RoleMember, State: models.UserActive, Source: "invite", CreatedAt: now}
	if err := a.DB.Insert(ctx, store.CUsers, mayaRec); err != nil {
		t.Fatal(err)
	}
	// liveUnchanged: people still sign in with A and A's secret — Maya through
	// the provider — and B's secret never became one in use.
	liveUnchanged := func(what string) {
		t.Helper()
		st := fresh()
		if st.Method != models.AuthSSO || st.SSO == nil || st.SSO.ClientID != "app-a" {
			t.Fatalf("%s: the sign-in in use changed: %s %+v", what, st.Method, st.SSO)
		}
		live := LiveUse(a.Cfg, st.SSO)
		if live.Secret != secretA {
			t.Fatalf("%s: the secret in use changed", what)
		}
		if u, _, err := PersonForSSO(ctx, a, st.SSO, switchSignIn(t, p, live.Client(), maya), nil); err != nil || u.ID != mayaRec.ID {
			t.Fatalf("%s: Maya's sign-in with the app in use: %v %v", what, u, err)
		}
		if s, _, _ := SSOSecret(a.Cfg, appB); s != "" {
			t.Fatalf("%s: B's secret became one in use", what)
		}
		if s, _, _ := SSOSecret(a.Cfg, appA); s != secretA {
			t.Fatalf("%s: A's secret changed", what)
		}
	}
	liveUnchanged("before")

	var tested *sso.TestResult
	races := []struct {
		name, stage, reason string
		tester              fakeoidc.Person
		during              func()
		after               func() string // what's wrong with the other owner's change ("" nothing)
	}{
		{"the waiting setup forgotten after its secret was kept", "write", "setup_changed", sam,
			func() {
				if err := DiscardSSO(ctx, a); err != nil {
					t.Fatal(err)
				}
			},
			func() string {
				if fresh().SSONext != nil {
					return "the forgotten setup came back"
				}
				if s, _, _ := SSONextSecret(a.Cfg, appB); s != "" {
					return "the forgotten setup's secret came back"
				}
				return ""
			}},
		{"another app set up after the secret was kept", "write", "setup_changed", sam,
			func() { set("app-c", secretC) },
			func() string {
				if st := fresh(); st.SSONext == nil || st.SSONext.ClientID != "app-c" {
					return "the other owner's setup was overwritten"
				}
				if s, _, _ := SSONextSecret(a.Cfg, appC); s != secretC {
					return "the other owner's secret is gone"
				}
				return ""
			}},
		{"a new secret for the waiting app after its secret was kept", "write", "setup_changed", sam,
			func() {
				if _, err := ssoSecretOp(ctx, a, &SSOSecretIn{ClientSecret: secretB2}); err != nil {
					t.Fatal(err)
				}
			},
			func() string {
				if st := fresh(); st.SSONext == nil || st.SSONext.ClientID != "app-b" {
					return "the waiting setup is gone"
				}
				if s, _, _ := SSONextSecret(a.Cfg, appB); s != secretB2 {
					return "the other owner's new secret is gone"
				}
				return ""
			}},
		{"the waiting secret gone before it is kept", "promote", "setup_changed", sam,
			func() { _ = dropSecret(a.Cfg, secretName(appB, true)) },
			func() string {
				if st := fresh(); st.SSONext == nil || st.SSONext.ClientID != "app-b" {
					return "the waiting setup is gone"
				}
				return ""
			}},
		{"the tested account linked to someone else before the owner's link", "link", SSOLinkedElsewhere, samElsewhere,
			func() {
				l := linkOf(tested.Identity)
				if _, err := a.DB.C(store.CUsers).UpdateOne(ctx, bson.M{"_id": mayaRec.ID}, bson.M{"$push": bson.M{"sso": l}}); err != nil {
					t.Fatal(err)
				}
				forget(mayaRec.ID)
			},
			func() string {
				defer func() {
					_, _ = a.DB.C(store.CUsers).UpdateOne(ctx, bson.M{"_id": mayaRec.ID}, bson.M{"$pull": bson.M{"sso": bson.M{"key": tested.Identity.Key}}})
					forget(mayaRec.ID)
				}()
				if st := fresh(); st.SSONext == nil || st.SSONext.ClientID != "app-b" {
					return "the waiting setup didn't come back"
				}
				if s, _, _ := SSONextSecret(a.Cfg, appB); s != secretB {
					return "the waiting secret is gone"
				}
				return ""
			}},
	}
	for _, r := range races {
		// B waits, tested by the owner.
		if fresh().SSONext != nil {
			if err := DiscardSSO(ctx, a); err != nil {
				t.Fatal(err)
			}
		}
		set("app-b", secretB)
		tested = test(r.tester)
		before, _ := GetUser(ctx, a, you(a))
		switchHook = func(stage string) {
			if stage == r.stage {
				r.during()
			}
		}
		err := switchNow()
		switchHook = nil
		if switchReason(err) != r.reason {
			t.Fatalf("%s: the switch: %v", r.name, err)
		}
		liveUnchanged(r.name)
		if problem := r.after(); problem != "" {
			t.Fatalf("%s: %s", r.name, problem)
		}
		forget(you(a))
		if owner, _ := GetUser(ctx, a, you(a)); !sameLinks(owner.SSO, before.SSO) {
			t.Fatalf("%s: the owner's accounts changed: %+v", r.name, owner.SSO)
		}
	}

	// Without anyone in between, the same switch goes through: B in use with
	// B's secret, nothing waiting, and Maya signs in with B.
	if fresh().SSONext != nil {
		if err := DiscardSSO(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	set("app-b", secretB)
	test(sam)
	switchHook = func(string) {}
	if err := switchNow(); err != nil {
		t.Fatal(err)
	}
	switchHook = nil
	st := fresh()
	if st.SSO == nil || st.SSO.ClientID != "app-b" || st.SSONext != nil {
		t.Fatalf("after the switch: %+v %+v", st.SSO, st.SSONext)
	}
	if s, _, _ := SSOSecret(a.Cfg, appB); s != secretB {
		t.Fatal("B's secret isn't the one in use")
	}
	if s, _, _ := SSONextSecret(a.Cfg, appB); s != "" {
		t.Fatal("B's secret still waits")
	}
	if u, _, err := PersonForSSO(ctx, a, st.SSO, switchSignIn(t, p, LiveUse(a.Cfg, st.SSO).Client(), maya), nil); err != nil || u.ID != mayaRec.ID {
		t.Fatalf("Maya with B: %v %v", u, err)
	}
}
