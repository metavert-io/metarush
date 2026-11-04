package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/sso"
	"github.com/metavert-io/metarush/internal/store"
)

// SSOSetup is an organization sign-in's setup as Settings → People shows it.
type SSOSetup struct {
	Provider string `json:"provider" jsonschema:"enum=google,enum=microsoft,enum=oidc"`
	// Title is the provider's name on the button ("Google", "Okta").
	Title    string `json:"title"`
	Label    string `json:"label,omitempty"`
	Issuer   string `json:"issuer"`
	Tenant   string `json:"tenant,omitempty"`
	ClientID string `json:"clientId"`
	// Domain: who may join without an invite (a Google Workspace domain, a
	// Microsoft directory ID, an email domain).
	Domain        string    `json:"domain,omitempty"`
	AlsoPassword  bool      `json:"alsoPassword"`
	AlsoTailscale bool      `json:"alsoTailscale"`
	SetAt         time.Time `json:"setAt"`
	SetBy         string    `json:"setBy,omitempty"`
}

// SSOSecretState says whether this computer has the app's client secret
// (never the secret).
type SSOSecretState struct {
	Set    bool   `json:"set"`
	Masked string `json:"masked,omitempty" jsonschema:"description=The last four characters, as ••••3f2a."`
	Source string `json:"source,omitempty" jsonschema:"enum=keychain,enum=file,enum=environment"`
	EnvVar string `json:"envVar"`
}

// SSOCallbacksOut are the addresses to register with the provider.
type SSOCallbacksOut struct {
	// Local is this computer's own browser's (http://127.0.0.1:<port>/auth/sso/callback).
	Local string `json:"local"`
	// Public is the address for other computers' (remote access on).
	Public      string `json:"public,omitempty"`
	PublicHTTPS bool   `json:"publicHttps"`
}

// SSOTestView is the last test sign-in on this computer.
type SSOTestView struct {
	OK       bool      `json:"ok"`
	At       time.Time `json:"at"`
	Fresh    bool      `json:"fresh" jsonschema:"description=Recent enough to switch with (30 minutes)."`
	Mine     bool      `json:"mine" jsonschema:"description=Done by the person asking."`
	ForSetup string    `json:"forSetup" jsonschema:"enum=next,enum=live,enum=other,description=Which setup it tested: the one waiting, the one in use, or one changed since."`
	Email    string    `json:"email,omitempty" jsonschema:"description=Who signed in."`
	Name     string    `json:"name,omitempty"`
	Provider string    `json:"provider,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	Message  string    `json:"message"`
}

// SSOStatusOut is the organization sign-in on this computer.
type SSOStatusOut struct {
	Method string `json:"method" jsonschema:"enum=single,enum=password,enum=tailscale,enum=sso"`
	// Live is the setup in use (method sso) or last used; Next one waiting
	// for its test sign-in before it can be switched to.
	Live  *SSOSetup `json:"live,omitempty"`
	Next  *SSOSetup `json:"next,omitempty"`
	InUse bool      `json:"inUse" jsonschema:"description=People sign in with their organization's account now."`
	// Secret is this computer's secret of the app in use (or last used);
	// NextSecret the one the setup waiting for its test signs in with.
	Secret     SSOSecretState  `json:"secret"`
	NextSecret *SSOSecretState `json:"nextSecret,omitempty"`
	Callbacks  SSOCallbacksOut `json:"callbacks"`
	// Earlier: people who signed in with an earlier provider, address or
	// directory and not yet with this one (their next sign-in links them by an
	// email this one vouches for, D453).
	Earlier int          `json:"earlier"`
	Test    *SSOTestView `json:"test,omitempty"`
	// CanSwitch: the setup waiting (or the one last used) passed a test
	// sign-in by the person asking, here, within 30 minutes.
	CanSwitch bool   `json:"canSwitch"`
	SwitchWhy string `json:"switchWhy,omitempty" jsonschema:"description=Why it can't be switched to yet."`
	Owner     bool   `json:"owner"`
	Here      bool   `json:"here"`
	Summary   string `json:"summary"`
}

// SSOSetIn sets up the organization's sign-in (owners, on this computer).
type SSOSetIn struct {
	Provider string `json:"provider" jsonschema:"enum=google,enum=microsoft,enum=oidc"`
	Issuer   string `json:"issuer,omitempty" jsonschema:"maxLength=512,description=Another provider: its address (its issuer, as its discovery document says)."`
	Tenant   string `json:"tenant,omitempty" jsonschema:"maxLength=253,description=Microsoft: your directory (tenant) ID, or organizations."`
	ClientID string `json:"clientId" jsonschema:"maxLength=255"`
	Label    string `json:"label,omitempty" jsonschema:"maxLength=40,description=Another provider: its name on the button."`
	Domain   string `json:"domain,omitempty" jsonschema:"maxLength=253,description=Who may join without an invite: a Google Workspace domain, a Microsoft directory ID or an email domain."`
	// AlsoPassword and AlsoTailscale keep those sign-ins working beside it.
	AlsoPassword  bool `json:"alsoPassword,omitempty"`
	AlsoTailscale bool `json:"alsoTailscale,omitempty"`
	// ClientSecret is the app's client secret, kept on this computer only.
	ClientSecret string `json:"clientSecret,omitempty" jsonschema:"maxLength=512"`
}

// SSOSecretIn sets or removes this computer's client secret.
type SSOSecretIn struct {
	ClientSecret string `json:"clientSecret,omitempty" jsonschema:"maxLength=512"`
	Remove       bool   `json:"remove,omitempty"`
}

// SSOTestOut starts a test sign-in in a browser on this computer.
type SSOTestOut struct {
	URL       string    `json:"url" jsonschema:"description=Open it in a browser on this computer: it goes to the provider and comes back here."`
	ExpiresAt time.Time `json:"expiresAt"`
	Summary   string    `json:"summary"`
}

func init() {
	ops.Def("auth.sso.status", "Signing in with your organization's account (Google, Microsoft or another provider): the setup, this computer's app secret, the addresses to register and the last test sign-in", ops.Flags{}, ssoStatusOp)
	ops.Def("auth.sso.set", "Set up signing in with your organization's account: the provider, the app, who may join by themselves and whether passwords or Tailscale still work (owners, on this computer)", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, ssoSetOp)
	ops.Def("auth.sso.secret", "Keep or remove the sign-in app's client secret on this computer (owners, on this computer)", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, ssoSecretOp)
	ops.Def("auth.sso.test", "Start a test sign-in with your organization's account in a browser on this computer, before switching to it (owners)", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, ssoTestOp)
	ops.Def("auth.sso.discard", "Forget the new sign-in setup waiting for its test, and its new secret; the sign-in in use stays as it is (owners, on this computer)", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, ssoDiscardOp)
}

func viewOf(st *models.SSOSettings) *SSOSetup {
	if st == nil {
		return nil
	}
	return &SSOSetup{Provider: st.Provider, Title: sso.Title(st.Provider, st.Label), Label: st.Label, Issuer: st.Issuer, Tenant: st.Tenant, ClientID: st.ClientID,
		Domain: st.Domain, AlsoPassword: st.AlsoPassword, AlsoTailscale: st.AlsoTailscale, SetAt: st.SetAt, SetBy: st.SetBy.UserName}
}

// SSOStatus is the organization sign-in as this computer sees it.
func SSOStatus(ctx context.Context, a *app.App) *SSOStatusOut {
	cur := Settings(ctx, a)
	out := &SSOStatusOut{Method: cur.Method, Live: viewOf(cur.SSO), Next: viewOf(cur.SSONext), InUse: cur.Method == models.AuthSSO,
		Owner: ops.IsOwnerCaller(ctx), Here: !ops.CallerFrom(ctx).Elsewhere}
	out.Secret = secretState(a.Cfg, cur.SSO, false)
	if cur.SSONext != nil {
		ns := secretState(a.Cfg, cur.SSONext, true)
		out.NextSecret = &ns
	}
	local, public := SSOCallbacks(a.Cfg)
	out.Callbacks = SSOCallbacksOut{Local: local, Public: public, PublicHTTPS: strings.HasPrefix(public, "https://")}
	live, test := LiveUse(a.Cfg, cur.SSO), TestUse(a.Cfg, cur)
	if t := sso.FlowsIn(a.Cfg.StateDir()).LastTest(); t != nil {
		v := &SSOTestView{OK: t.OK, At: t.At, Fresh: t.Fresh(), Mine: t.UserID == you(a), Reason: t.Reason, Message: t.Message, ForSetup: "other"}
		switch {
		case cur.SSONext != nil && test != nil && t.Setup == test.Fingerprint():
			v.ForSetup = "next"
		case live != nil && t.Setup == live.Fingerprint():
			v.ForSetup = "live"
		case cur.SSONext == nil && test != nil && t.Setup == test.Fingerprint():
			v.ForSetup = "live"
		}
		if t.Identity != nil {
			v.Email, v.Name, v.Provider = t.Identity.Email, t.Identity.Name, t.Identity.Provider
		}
		out.Test = v
	}
	out.CanSwitch, out.SwitchWhy = canSwitch(ctx, a, cur, TestedSetup(cur), out.Test)
	if cur.Method == models.AuthSSO && cur.SSO != nil {
		out.Earlier = earlierPeople(ctx, a, cur.SSO)
	}
	out.Summary = ssoSummary(cur, out)
	return out
}

// secretState says whether this computer has an app's secret (next: the one
// a test signs in with), never the secret.
func secretState(cfg *config.Config, st *models.SSOSettings, next bool) SSOSecretState {
	out := SSOSecretState{}
	if st == nil {
		return out
	}
	out.EnvVar = SSOSecretEnv(st)
	secret, src, _ := SSOSecret(cfg, st)
	if next {
		if n, nsrc, _ := SSONextSecret(cfg, st); n != "" {
			secret, src = n, nsrc
		}
	}
	if secret != "" {
		out.Set, out.Masked, out.Source = true, providers.Mask(secret), src
	}
	return out
}

// earlierPeople counts active people who signed in with an organization's
// account of another issuer than the one in use, and not yet with it.
func earlierPeople(ctx context.Context, a *app.App, live *models.SSOSettings) int {
	var us []models.User
	if err := a.DB.Find(ctx, store.CUsers, a.WorkspaceID(), bson.M{"state": models.UserActive, "sso.0": bson.M{"$exists": true}}, &us); err != nil {
		return 0
	}
	n := 0
	for _, u := range us {
		current := false
		for _, l := range u.SSO {
			current = current || scopeOfSetup(live, l)
		}
		if !current {
			n++
		}
	}
	return n
}

// scopeOfSetup reports a link made with the setup's issuer.
func scopeOfSetup(st *models.SSOSettings, l models.SSOLink) bool {
	scope := linkScope(l)
	switch st.Provider {
	case sso.Google:
		return scope == "google"
	case sso.Microsoft:
		if d := Directory(st); d != "" {
			return scope == "microsoft:"+d
		}
		return strings.HasPrefix(scope, "microsoft:")
	}
	return scope == "oidc:"+st.Issuer || scope == "oidc:"+strings.TrimSuffix(st.Issuer, "/")
}

func canSwitch(ctx context.Context, a *app.App, cur models.AuthSettings, tested *models.SSOSettings, t *SSOTestView) (bool, string) {
	switch {
	case tested == nil:
		return false, "Nothing is set up yet."
	case cur.Method == models.AuthSSO && cur.SSONext == nil:
		return false, "People sign in with it already."
	case t == nil || t.ForSetup == "other" || (cur.SSONext != nil && t.ForSetup != "next"):
		return false, "Test the sign-in on this computer first."
	case !t.OK:
		return false, "The last test sign-in didn't work."
	case !t.Fresh:
		return false, "The test sign-in was more than 30 minutes ago: test it again."
	case !t.Mine:
		return false, "Someone else did the test sign-in: do your own, so you keep getting in."
	case ops.CallerFrom(ctx).Elsewhere:
		return false, fmt.Sprintf("How people sign in changes only at %s.", a.Cfg.MachineName())
	}
	return true, ""
}

func ssoSummary(cur models.AuthSettings, out *SSOStatusOut) string {
	switch {
	case cur.Method == models.AuthSSO && cur.SSO != nil:
		s := "People sign in with " + out.Live.Title + "."
		if cur.SSO.AlsoPassword && cur.SSO.AlsoTailscale {
			s += " Passwords and Tailscale accounts still work too."
		} else if cur.SSO.AlsoPassword {
			s += " Passwords still work too."
		} else if cur.SSO.AlsoTailscale {
			s += " Tailscale accounts still work too."
		}
		if out.Next != nil {
			s += " A new setup (" + out.Next.Title + ") waits for its test sign-in; until it is switched to, sign-ins keep using the one in use."
		}
		if out.Earlier > 0 {
			s += fmt.Sprintf(" %d of the people signed in with an earlier provider; their next sign-in links them when %s vouches for their email, else they need an invite again.", out.Earlier, out.Live.Title)
		}
		return s
	case out.Next != nil || out.Live != nil:
		t := TestedSetup(cur)
		if out.CanSwitch {
			return "Signing in with " + sso.Title(t.Provider, t.Label) + " passed its test: you can switch to it."
		}
		return "Signing in with " + sso.Title(t.Provider, t.Label) + " is set up but not in use. " + out.SwitchWhy
	}
	return "Signing in with your organization's account isn't set up."
}

func ssoStatusOp(ctx context.Context, a *app.App, _ *ops.EmptyIn) (*SSOStatusOut, error) {
	return SSOStatus(ctx, a), nil
}

func ssoSetOp(ctx context.Context, a *app.App, in *SSOSetIn) (*SSOStatusOut, error) {
	if err := hereOnly(ctx, a); err != nil {
		return nil, err
	}
	if in.ClientSecret != "" {
		a.Log.Secret(in.ClientSecret) // never in the activity log
	}
	st, err := CleanSSO(models.SSOSettings{Provider: in.Provider, Issuer: in.Issuer, Tenant: in.Tenant, ClientID: in.ClientID, Label: in.Label, Domain: in.Domain,
		AlsoPassword: in.AlsoPassword, AlsoTailscale: in.AlsoTailscale})
	if err != nil {
		return nil, err
	}
	// The provider must answer at that address before anything is kept.
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := (&sso.Client{Kind: st.Provider, Issuer: st.Issuer, ClientID: st.ClientID}).Discover(dctx); err != nil {
		return nil, ssoProblem(err)
	}
	if in.ClientSecret == "" && st.Provider != sso.OIDC && testSecret(a.Cfg, st) == "" {
		// Google and Microsoft need the app's secret; this computer has none for this app.
		return nil, apperr.New(apperr.InvalidInput, "Give the app's client secret: this computer doesn't have one for this app yet.").
			WithDetails(map[string]any{"reason": "no_secret"})
	}
	if in.ClientSecret != "" {
		// Kept as this app's next secret: the sign-in in use keeps its own
		// until a test passes and an owner switches (D450).
		if err := setNextSecret(a.Cfg, st, in.ClientSecret); err != nil {
			return nil, err
		}
		a.Audit(ctx, "auth.sso.secret", "installation", a.Cfg.Installation.ID, "", map[string]any{"set": true, "waits": true})
	}
	if _, err := SaveSSO(ctx, a, *st, in.ClientSecret != ""); err != nil {
		return nil, err
	}
	return SSOStatus(ctx, a), nil
}

// ssoProblem is a provider problem as an error with its reason.
func ssoProblem(err error) error {
	if p, ok := err.(*sso.Problem); ok {
		e := apperr.New(apperr.InvalidInput, "%s", p.Msg).WithHint(p.Hint).WithDetails(map[string]any{"reason": p.Reason})
		if p.Detail != "" {
			e = e.WithDetails(map[string]any{"reason": p.Reason, "detail": p.Detail})
		}
		return e
	}
	return err
}

func ssoSecretOp(ctx context.Context, a *app.App, in *SSOSecretIn) (*SSOStatusOut, error) {
	if err := hereOnly(ctx, a); err != nil {
		return nil, err
	}
	forgetSettings(a) // the setup as it is now
	cur := Settings(ctx, a)
	// Every change here gives the setup a new revision first (only while it
	// is the one read), then changes the secret: a switch tested before
	// doesn't go through, and one written first leaves this change undone
	// (D450).
	switch {
	case in.Remove:
		switch {
		case cur.SSONext != nil:
			if err := bumpSetup(ctx, a, "auth.ssoNext", cur.SSONext); err != nil {
				return nil, err
			}
			if err := dropSecret(a.Cfg, secretName(cur.SSONext, true)); err != nil {
				return nil, err
			}
		case cur.SSO != nil && cur.Method != models.AuthSSO:
			if err := bumpSetup(ctx, a, "auth.sso", cur.SSO); err != nil {
				return nil, err
			}
			if err := dropSecret(a.Cfg, secretName(cur.SSO, false)); err != nil {
				return nil, err
			}
		case cur.SSO != nil:
			return nil, apperr.New(apperr.Conflict, "People sign in with this app's secret now, so MetaRush keeps it.").
				WithHint("Give a new secret instead (it waits for a test), or choose another way to sign in first.").WithDetails(map[string]any{"reason": "in_use"})
		default:
			return nil, apperr.New(apperr.Conflict, "No sign-in with an organization's account is set up.").WithDetails(map[string]any{"reason": "no_setup"})
		}
	case in.ClientSecret != "":
		a.Log.Secret(in.ClientSecret)
		target := cur.SSONext
		if target == nil {
			target = cur.SSO
		}
		if target == nil {
			return nil, apperr.New(apperr.Conflict, "Set up the sign-in with your organization's account first.").WithDetails(map[string]any{"reason": "no_setup"}).WithCommand("metarush auth sso set")
		}
		if err := checkNextSecret(a.Cfg, target, in.ClientSecret); err != nil {
			return nil, err
		}
		// A new secret waits for its test: the sign-in in use keeps the one it
		// has until an owner switches after a passing test (D450).
		created := ""
		if cur.SSONext != nil {
			if err := bumpSetup(ctx, a, "auth.ssoNext", cur.SSONext); err != nil {
				return nil, err
			}
		} else {
			// The app in use (or last used) gets a waiting copy of its setup,
			// unless something started waiting meanwhile.
			next := *cur.SSO
			next.SetAt, next.SetBy, next.Rev = time.Now().UTC(), a.Actor, newRev()
			f := setupIs("auth.sso", cur.SSO)
			f["_id"], f["auth.ssoNext"] = a.WorkspaceID(), bson.M{"$exists": false}
			res, err := a.DB.C(store.CWorkspaces).UpdateOne(ctx, f, bson.M{"$set": bson.M{"auth.ssoNext": next}})
			forgetSettings(a)
			if err != nil {
				return nil, err
			}
			if res.MatchedCount == 0 {
				return nil, errSetupChanged()
			}
			created = next.Rev
		}
		if err := setNextSecret(a.Cfg, target, in.ClientSecret); err != nil {
			if created != "" {
				// The waiting copy goes with the secret it was for.
				_, _ = a.DB.C(store.CWorkspaces).UpdateOne(ctx, bson.M{"_id": a.WorkspaceID(), "auth.ssoNext.rev": created}, bson.M{"$unset": bson.M{"auth.ssoNext": ""}})
				forgetSettings(a)
			}
			return nil, err
		}
	default:
		return nil, apperr.New(apperr.InvalidInput, "Give the app's client secret, or remove it.")
	}
	a.Audit(ctx, "auth.sso.secret", "installation", a.Cfg.Installation.ID, "", map[string]any{"set": !in.Remove, "waits": !in.Remove})
	return SSOStatus(ctx, a), nil
}

func ssoDiscardOp(ctx context.Context, a *app.App, _ *ops.EmptyIn) (*SSOStatusOut, error) {
	if err := hereOnly(ctx, a); err != nil {
		return nil, err
	}
	if err := DiscardSSO(ctx, a); err != nil {
		return nil, err
	}
	return SSOStatus(ctx, a), nil
}

func ssoTestOp(ctx context.Context, a *app.App, _ *ops.EmptyIn) (*SSOTestOut, error) {
	if err := hereOnly(ctx, a); err != nil {
		return nil, err
	}
	cur := Settings(ctx, a)
	use := TestUse(a.Cfg, cur)
	if use == nil {
		return nil, apperr.New(apperr.Conflict, "Set up the sign-in with your organization's account first.").WithDetails(map[string]any{"reason": "no_setup"}).WithCommand("metarush auth sso set")
	}
	setup, client := use.Setup, use.Client()
	if client.Secret == "" && setup.Provider != sso.OIDC {
		return nil, apperr.New(apperr.Conflict, "This computer doesn't have the sign-in app's client secret yet.").
			WithHint("Copy it from the provider's console into Settings → People, or a terminal.").WithDetails(map[string]any{"reason": "no_secret"}).WithCommand("metarush auth sso secret")
	}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := client.Discover(dctx); err != nil {
		return nil, ssoProblem(err)
	}
	local, _ := SSOCallbacks(a.Cfg)
	hint := ""
	if u, err := GetUser(ctx, a, you(a)); err == nil && ValidEmail(u.Email) {
		hint = u.Email
	}
	ticket, err := sso.FlowsIn(a.Cfg.StateDir()).NewTicket(sso.Flow{Purpose: sso.ForTest, Host: SSOLocalHost(a.Cfg), Redirect: local,
		Setup: use.Fingerprint(), UserID: you(a), Hint: hint})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't start the test sign-in.")
	}
	a.Log.Secret(ticket)
	a.Audit(ctx, "auth.sso.test", "workspace", a.WorkspaceID(), "", map[string]any{"provider": setup.Provider})
	return &SSOTestOut{URL: SSOLocalOrigin(a.Cfg) + sso.BeginPath + "?t=" + ticket, ExpiresAt: time.Now().Add(sso.TicketLife).UTC(),
		Summary: "Open the link in a browser on this computer within 2 minutes and sign in with your own " + sso.Title(setup.Provider, setup.Label) + " account. MetaRush checks every step and keeps nothing but who you are."}, nil
}

// setSSOMethod switches to the organization's sign-in (auth.method.set sso).
func setSSOMethod(ctx context.Context, a *app.App) error {
	st := sso.FlowsIn(a.Cfg.StateDir())
	if _, _, err := SetUpSSO(ctx, a, you(a), st.LastTest()); err != nil {
		return err
	}
	st.ClearTest() // a test switches once
	return nil
}
