package remote

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/sessions"
)

// The remote-access operations, in the one registry the CLI and the dashboard
// share; each has a `metarush remote` command. Everything that widens or
// narrows who can come in is people-only (HumanOnly), and nothing here ever
// changes Tailscale: the CLI runs `tailscale serve` only with --apply, for a
// person at an interactive terminal who said yes.

// RemoteStatusIn has no options.
type RemoteStatusIn struct{}

// RemotePerson is a Tailscale account that signs in by itself.
type RemotePerson struct {
	Login string `json:"login"`
	// Name is who they are in MetaRush, once they signed in (or have the same email).
	Name   string `json:"name,omitempty"`
	UserID string `json:"userId,omitempty"`
	// ThisComputer: the Tailscale account this computer is signed in with.
	ThisComputer   bool       `json:"thisComputer,omitempty"`
	LastSignedInAt *time.Time `json:"lastSignedInAt,omitempty"`
	// Browsers is how many browsers are signed in with this account now.
	Browsers int `json:"browsers"`
}

// RemoteStatusOut is remote access on this computer.
type RemoteStatusOut struct {
	// On: there is a public address (dashboard.publicURL).
	On  bool   `json:"on"`
	URL string `json:"url,omitempty"`
	// Machine is how the dashboard names this computer to people elsewhere.
	Machine string `json:"machine"`
	// SignInWithTailscale: listed Tailscale accounts are signed in by themselves.
	SignInWithTailscale bool           `json:"signInWithTailscale"`
	People              []RemotePerson `json:"people"`
	Tailscale           Tailscale      `json:"tailscale"`
	Serve               Serve          `json:"serve"`
	// Ready: people on the tailnet can open the address now.
	Ready bool `json:"ready"`
	// ServeCommand starts tailscale serve for this dashboard; StopCommand stops it.
	ServeCommand string `json:"serveCommand"`
	StopCommand  string `json:"stopCommand"`
	// Steps is what is left to do, in plain words, first things first.
	Steps   []string `json:"steps"`
	Summary string   `json:"summary"`
	// SignedOut is how many browsers this change signed out (disable, remove).
	SignedOut int `json:"signedOut,omitempty"`
	// Linked: this computer's Tailscale account was recorded as the person who runs MetaRush here.
	Linked bool `json:"linked,omitempty"`
}

// RemoteEnableIn sets remote access up.
type RemoteEnableIn struct {
	Allow []string `json:"allow,omitempty" jsonschema:"description=Tailscale accounts (name@example.com) that are signed in by themselves; this computer's own account is added with them."`
	// MachineName is how the dashboard names this computer to people on other computers.
	MachineName string `json:"machineName,omitempty" jsonschema:"description=How the dashboard names this computer to people on other computers (default: its name on the tailnet)."`
	URL         string `json:"url,omitempty" jsonschema:"description=Another address than https://<this computer's Tailscale name>\\, for example with another HTTPS port."`
}

// RemoteDisableIn has no options.
type RemoteDisableIn struct{}

// RemoteAllowIn adds or removes Tailscale accounts.
type RemoteAllowIn struct {
	Add    []string `json:"add,omitempty" jsonschema:"description=Tailscale accounts to let in (only at this computer)."`
	Remove []string `json:"remove,omitempty" jsonschema:"description=Tailscale accounts to stop letting in (their browsers are signed out at once)."`
}

// RemoteSignoutIn signs browsers on other computers out.
type RemoteSignoutIn struct {
	Login string `json:"login,omitempty" jsonschema:"description=Only this Tailscale account's browsers."`
	All   bool   `json:"all,omitempty" jsonschema:"description=Every browser that signed in through the public address."`
}

// RemoteSignoutOut says how many browsers were signed out.
type RemoteSignoutOut struct {
	SignedOut int    `json:"signedOut"`
	Message   string `json:"message"`
}

func init() {
	ops.Def("remote.status", "Remote access over Tailscale: the address people use from their other computers, whether tailscale serve hands it to this dashboard, HTTPS certificates, and who signs in with their Tailscale account", ops.Flags{},
		func(ctx context.Context, a *app.App, _ *RemoteStatusIn) (*RemoteStatusOut, error) {
			return Status(ctx, a), nil
		})
	ops.Def("remote.enable", "Set up remote access over Tailscale (this computer's Tailscale name as the address, who signs in with their Tailscale account) and say which tailscale serve command starts it; people only, owners", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, enable)
	ops.Def("remote.disable", "Turn remote access off: the address and the Tailscale accounts are cleared and browsers on other computers are signed out; says how to stop tailscale serve; people only, owners", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, disable)
	ops.Def("remote.allow", "Add or remove Tailscale accounts that are signed in by themselves through remote access (adding only from this computer); people only, owners", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, allow)
	ops.Def("remote.signout", "Sign out browsers that signed in from other computers (all, or one Tailscale account's); people only, owners", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, signout)
}

// Status says how remote access stands (it reads Tailscale, changing nothing).
func Status(ctx context.Context, a *app.App) *RemoteStatusOut {
	rm := a.Cfg.Remote()
	port := a.Cfg.Dashboard.Port
	ts := ReadTailscale(ctx, a.Tools)
	sv := ReadServe(ctx, a.Tools, port)
	out := &RemoteStatusOut{On: rm.On(), URL: rm.PublicURL, Machine: a.Cfg.MachineName(), SignInWithTailscale: rm.IdentitySignIn(),
		Tailscale: ts, Serve: sv, ServeCommand: ServeCommand(rm, port), StopCommand: StopCommand(rm), Steps: []string{}}
	if !rm.On() && ts.DNSName != "" {
		if would, err := config.RemoteFrom(config.Dashboard{PublicURL: "https://" + ts.DNSName}); err == nil {
			out.ServeCommand = ServeCommand(would, port)
		}
	}
	out.People = people(ctx, a, rm, ts)
	step := func(s string) { out.Steps = append(out.Steps, s) }
	if !rm.On() {
		step("Turn remote access on in a terminal on this computer (Settings shows the command).")
	}
	if ts.Problem != "" {
		step(ts.Problem)
	}
	matches := sv.Active && strings.EqualFold(sv.Address, rm.PublicURL)
	switch {
	case !rm.On():
	case sv.Known && !sv.Active:
		step(fmt.Sprintf("Start Tailscale's secure address for MetaRush: run the tailscale serve command in a terminal on %s.", out.Machine))
	case sv.Active && !matches:
		step(fmt.Sprintf("Tailscale hands %s to MetaRush, but MetaRush is set up for %s. Turn remote access on again so the two agree.", sv.Address, rm.PublicURL))
	}
	for _, e := range sv.Elsewhere {
		if rm.On() && !sv.Active && e == rm.PublicURL+"/" {
			step(fmt.Sprintf("Tailscale already serves something else at %s. Stop that there, or give MetaRush another HTTPS port (turn remote access on again with another address).", rm.PublicURL))
		}
	}
	if sv.Funnel {
		step("Tailscale Funnel puts this address on the public internet. MetaRush turns those visits away, but turn Funnel off for it in Tailscale.")
	}
	if rm.On() && !rm.IdentitySignIn() {
		step(fmt.Sprintf("No one signs in with their Tailscale account yet, so people on other computers need a sign-in link from %s. Add people to let them in by themselves.", out.Machine))
	}
	if !rm.On() && len(rm.Logins) > 0 {
		step("The people listed can sign in once remote access is on.")
	}
	out.Ready = rm.On() && ts.Running && matches && (!rm.HTTPS || ts.HTTPSReady)
	switch {
	case out.Ready:
		out.Summary = fmt.Sprintf("People on your tailnet can open MetaRush on %s at %s.", out.Machine, rm.PublicURL)
	case rm.On():
		out.Summary = fmt.Sprintf("Remote access is set up for %s, but it doesn't work yet.", rm.PublicURL)
	default:
		out.Summary = "Remote access is off: MetaRush answers on this computer only."
	}
	return out
}

func people(ctx context.Context, a *app.App, rm config.Remote, ts Tailscale) []RemotePerson {
	out := []RemotePerson{}
	known := Known(ctx, a, rm.Logins)
	signed := map[string]int{}
	for _, s := range sessions.For(a.Cfg.StateDir()).List(time.Now()) {
		if s.Public && s.Login != "" {
			signed[s.Login]++
		}
	}
	for _, l := range rm.Logins {
		p := RemotePerson{Login: l, ThisComputer: l == ts.Login, Browsers: signed[l]}
		if u, ok := known[l]; ok {
			p.Name, p.UserID, p.LastSignedInAt = u.Name, u.ID, u.LastSignedInAt
		}
		out = append(out, p)
	}
	return out
}

// save writes the remote-access settings to config.yaml (each key on its own,
// the rest of the file untouched) and puts them in effect in this process; a
// running dashboard elsewhere picks them up from the file.
func save(a *app.App, rm config.Remote) error {
	logins := rm.Logins
	if logins == nil {
		logins = []string{}
	}
	for _, f := range []struct {
		key string
		v   any
	}{{"publicURL", rm.PublicURL}, {"tailscaleLogins", logins}, {"machineName", rm.MachineName}} {
		if err := config.SetField(a.Cfg.Home(), []string{"dashboard", f.key}, f.v); err != nil {
			return apperr.Wrap(apperr.Internal, err, "MetaRush couldn't save the remote access settings.")
		}
	}
	if err := a.Cfg.SetRemote(config.Dashboard{PublicURL: rm.PublicURL, TailscaleLogins: rm.Logins, MachineName: rm.MachineName}); err != nil {
		return apperr.Wrap(apperr.Internal, err, "MetaRush couldn't use the new remote access settings.")
	}
	if OnChange != nil {
		OnChange()
	}
	return nil
}

// OnChange, when set (the daemon), is told when remote access settings change in this process.
var OnChange func()

func normalizeAll(list []string) ([]string, error) {
	var out []string
	for _, l := range list {
		n, err := config.NormalizeLogin(l)
		if err != nil {
			return nil, apperr.New(apperr.InvalidInput, "%s.", capitalize(err.Error())).WithDetails(map[string]any{"login": l})
		}
		out = append(out, n)
	}
	return out, nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] == '“' && len(r) > 1 {
		return s
	}
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

func enable(ctx context.Context, a *app.App, in *RemoteEnableIn) (*RemoteStatusOut, error) {
	ts := ReadTailscale(ctx, a.Tools)
	ownProxy := strings.TrimSpace(in.URL) != "" && (!ts.Installed || !ts.Running)
	switch {
	case ownProxy:
		// Remote access is an HTTPS address in front of this dashboard (D183):
		// Tailscale is one way to make it, any HTTPS proxy on this computer another.
	case !ts.Installed:
		return nil, apperr.New(apperr.ToolMissing, "Tailscale isn't installed on this computer, and remote access works through it.").
			WithHint("Install Tailscale from tailscale.com and sign in to your tailnet, or give the https:// address that already reaches this computer.").
			WithCommand("metarush remote enable --url https://<address>")
	case ts.Error != "":
		return nil, apperr.New(apperr.NotConfigured, "MetaRush couldn't read Tailscale's status on this computer.").
			WithHint("Open Tailscale and check that it is connected, then try again.").WithDetails(map[string]any{"problem": ts.Error})
	case !ts.Running:
		return nil, apperr.New(apperr.NotConfigured, "Tailscale isn't connected on this computer.").
			WithHint("Open Tailscale and connect to your tailnet, then try again.").WithDetails(map[string]any{"state": ts.State})
	case ts.DNSName == "" && strings.TrimSpace(in.URL) == "":
		return nil, apperr.New(apperr.NotConfigured, "This computer has no Tailscale name yet because MagicDNS is off for your tailnet.").
			WithHint("Turn MagicDNS on in the Tailscale admin console (under DNS), then try again.")
	}
	cur := a.Cfg.Remote()
	allow, err := normalizeAll(in.Allow)
	if err != nil {
		return nil, err
	}
	if ops.CallerFrom(ctx).Elsewhere {
		// Widening who may sign in happens only at this computer (D129), like
		// remote.allow; turning remote access on from elsewhere makes no sense.
		return nil, apperr.New(apperr.Unauthorized, "Only someone at %s can turn on remote access or let more people in.", a.Cfg.MachineName()).
			WithHint("Use MetaRush on that computer.").WithDetails(map[string]any{"reason": "elsewhere"})
	}
	d := config.Dashboard{PublicURL: strings.TrimSpace(in.URL), TailscaleLogins: append(append([]string{}, cur.Logins...), allow...), MachineName: cur.MachineName}
	if d.PublicURL == "" {
		d.PublicURL = "https://" + ts.DNSName
	}
	if len(allow) > 0 && ts.Login != "" {
		d.TailscaleLogins = append(d.TailscaleLogins, ts.Login) // the person at this computer, from their other computers too
	}
	if n := strings.TrimSpace(in.MachineName); n != "" {
		d.MachineName = n
	} else if d.MachineName == "" {
		d.MachineName = ts.HostName
	}
	rm, err := config.RemoteFrom(d)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "%s.", capitalize(err.Error()))
	}
	if err := save(a, rm); err != nil {
		return nil, err
	}
	linked := false
	if ts.Login != "" {
		linked, _ = LinkThisComputer(ctx, a, ts.Login)
	}
	a.Audit(ctx, "remote.enable", "installation", a.Cfg.Installation.ID, "", map[string]any{"url": rm.PublicURL, "logins": rm.Logins, "machineName": rm.MachineName, "linked": linked})
	out := Status(ctx, a)
	out.Linked = linked
	return out, nil
}

func disable(ctx context.Context, a *app.App, _ *RemoteDisableIn) (*RemoteStatusOut, error) {
	before := a.Cfg.Remote()
	if err := save(a, config.Remote{MachineName: before.MachineName}); err != nil {
		return nil, err
	}
	n, _ := sessions.For(a.Cfg.StateDir()).RemoveWhere(func(s sessions.Session) bool { return s.Public })
	a.Audit(ctx, "remote.disable", "installation", a.Cfg.Installation.ID, "", map[string]any{"url": before.PublicURL, "logins": before.Logins, "signedOut": n})
	out := Status(ctx, a)
	out.StopCommand, out.SignedOut = StopCommand(before), n
	if out.Serve.Active {
		out.Summary = "Remote access is off: MetaRush turns away visits from other computers. Tailscale still passes its address along; stop that with the tailscale serve command."
	}
	return out, nil
}

func allow(ctx context.Context, a *app.App, in *RemoteAllowIn) (*RemoteStatusOut, error) {
	add, err := normalizeAll(in.Add)
	if err != nil {
		return nil, err
	}
	remove, err := normalizeAll(in.Remove)
	if err != nil {
		return nil, err
	}
	if len(add)+len(remove) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "Say which Tailscale accounts to add or remove.")
	}
	if len(add) > 0 && ops.CallerFrom(ctx).Elsewhere {
		return nil, apperr.New(apperr.Unauthorized, "Only someone at %s can let more people in.", a.Cfg.MachineName()).
			WithHint("Removing people works from here. To add someone, use MetaRush on that computer.").WithDetails(map[string]any{"reason": "elsewhere"})
	}
	cur := a.Cfg.Remote()
	set := map[string]bool{}
	for _, l := range cur.Logins {
		set[l] = true
	}
	for _, l := range add {
		set[l] = true
	}
	var removed []string
	for _, l := range remove {
		if set[l] {
			removed = append(removed, l)
		}
		delete(set, l)
	}
	list := make([]string, 0, len(set))
	for l := range set {
		list = append(list, l)
	}
	sort.Strings(list)
	rm, err := config.RemoteFrom(config.Dashboard{PublicURL: cur.PublicURL, TailscaleLogins: list, MachineName: cur.MachineName})
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "%s.", capitalize(err.Error()))
	}
	if err := save(a, rm); err != nil {
		return nil, err
	}
	n := 0
	if len(remove) > 0 {
		gone := map[string]bool{}
		for _, l := range remove {
			gone[l] = true
		}
		n, _ = sessions.For(a.Cfg.StateDir()).RemoveWhere(func(s sessions.Session) bool { return s.Public && gone[s.Login] })
	}
	a.Audit(ctx, "remote.allow", "installation", a.Cfg.Installation.ID, "", map[string]any{"added": add, "removed": removed, "signedOut": n, "logins": rm.Logins})
	out := Status(ctx, a)
	out.SignedOut = n
	return out, nil
}

func signout(ctx context.Context, a *app.App, in *RemoteSignoutIn) (*RemoteSignoutOut, error) {
	login := ""
	if !in.All {
		if strings.TrimSpace(in.Login) == "" {
			return nil, apperr.New(apperr.InvalidInput, "Say whose browsers to sign out, or all of them.")
		}
		l, err := normalizeAll([]string{in.Login})
		if err != nil {
			return nil, err
		}
		login = l[0]
	}
	n, err := sessions.For(a.Cfg.StateDir()).RemoveWhere(func(s sessions.Session) bool { return s.Public && (in.All || s.Login == login) })
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't sign those browsers out.")
	}
	a.Audit(ctx, "remote.signout", "installation", a.Cfg.Installation.ID, "", map[string]any{"login": login, "all": in.All, "signedOut": n})
	msg := "No browser on another computer was signed in."
	switch {
	case n == 1:
		msg = "Signed out 1 browser on another computer. It needs to sign in again."
	case n > 1:
		msg = fmt.Sprintf("Signed out %d browsers on other computers. They need to sign in again.", n)
	}
	return &RemoteSignoutOut{SignedOut: n, Message: msg}, nil
}
