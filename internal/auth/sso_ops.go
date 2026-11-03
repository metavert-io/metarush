package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/sso"
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
	// Local is this computer's own browser's (http://localhost:<port>/auth/sso/callback).
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
	Live      *SSOSetup       `json:"live,omitempty"`
	Next      *SSOSetup       `json:"next,omitempty"`
	InUse     bool            `json:"inUse" jsonschema:"description=People sign in with their organization's account now."`
	Secret    SSOSecretState  `json:"secret"`
	Callbacks SSOCallbacksOut `json:"callbacks"`
	Test      *SSOTestView    `json:"test,omitempty"`
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
	out.Secret.EnvVar = SSOSecretEnv()
	if secret, src, err := SSOSecret(a.Cfg); err == nil && secret != "" {
		out.Secret.Set, out.Secret.Masked, out.Secret.Source = true, providers.Mask(secret), src
	}
	local, public := SSOCallbacks(a.Cfg)
	out.Callbacks = SSOCallbacksOut{Local: local, Public: public, PublicHTTPS: strings.HasPrefix(public, "https://")}
	tested := TestedSetup(cur)
	if t := sso.FlowsIn(a.Cfg.StateDir()).LastTest(); t != nil {
		v := &SSOTestView{OK: t.OK, At: t.At, Fresh: t.Fresh(), Mine: t.UserID == you(a), Reason: t.Reason, Message: t.Message, ForSetup: "other"}
		switch t.Setup {
		case SSOFingerprint(a.Cfg, cur.SSONext):
			v.ForSetup = "next"
		case SSOFingerprint(a.Cfg, cur.SSO):
			v.ForSetup = "live"
		}
		if t.Identity != nil {
			v.Email, v.Name, v.Provider = t.Identity.Email, t.Identity.Name, t.Identity.Provider
		}
		out.Test = v
	}
	out.CanSwitch, out.SwitchWhy = canSwitch(ctx, a, cur, tested, out.Test)
	out.Summary = ssoSummary(cur, out)
	return out
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
			s += " A new setup (" + out.Next.Title + ") waits for its test sign-in."
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
	if in.ClientSecret != "" {
		if _, err := SetSSOSecret(a.Cfg, in.ClientSecret); err != nil {
			return nil, err
		}
		a.Audit(ctx, "auth.sso.secret", "installation", a.Cfg.Installation.ID, "", map[string]any{"set": true})
	}
	if _, err := SaveSSO(ctx, a, *st); err != nil {
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
	switch {
	case in.Remove:
		if _, err := RemoveSSOSecret(a.Cfg); err != nil {
			return nil, err
		}
	case in.ClientSecret != "":
		a.Log.Secret(in.ClientSecret)
		if _, err := SetSSOSecret(a.Cfg, in.ClientSecret); err != nil {
			return nil, err
		}
	default:
		return nil, apperr.New(apperr.InvalidInput, "Give the app's client secret, or remove it.")
	}
	a.Audit(ctx, "auth.sso.secret", "installation", a.Cfg.Installation.ID, "", map[string]any{"set": !in.Remove})
	return SSOStatus(ctx, a), nil
}

func ssoTestOp(ctx context.Context, a *app.App, _ *ops.EmptyIn) (*SSOTestOut, error) {
	if err := hereOnly(ctx, a); err != nil {
		return nil, err
	}
	cur := Settings(ctx, a)
	setup := TestedSetup(cur)
	if setup == nil {
		return nil, apperr.New(apperr.Conflict, "Set up the sign-in with your organization's account first.").WithDetails(map[string]any{"reason": "no_setup"}).WithCommand("metarush auth sso set")
	}
	client, err := SSOClient(a.Cfg, setup)
	if err != nil {
		return nil, err
	}
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
		Setup: SSOFingerprint(a.Cfg, setup), UserID: you(a), Hint: hint})
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
