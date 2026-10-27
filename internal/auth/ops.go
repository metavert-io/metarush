package auth

import (
	"context"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/sessions"
	"github.com/metavert-io/metarush/internal/store"
)

// Account is a person of the workspace as Settings → People shows them.
type Account struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Email       string     `json:"email,omitempty"`
	Role        string     `json:"role" jsonschema:"enum=owner,enum=member"`
	State       string     `json:"state" jsonschema:"enum=active,enum=invited,enum=disabled,enum=earlier"`
	HasPassword bool       `json:"hasPassword"`
	You         bool       `json:"you"`
	Logins      []string   `json:"logins,omitempty" jsonschema:"description=Tailscale logins linked to them (owners see them)."`
	LastSignIn  *time.Time `json:"lastSignIn,omitempty"`
	InviteUntil *time.Time `json:"inviteUntil,omitempty" jsonschema:"description=When their invite link stops working (an invite not yet accepted)."`
	Work        int        `json:"work" jsonschema:"description=How many things they did, from the workspace's history (owners see it)."`
	Avatar      string     `json:"avatar,omitempty" jsonschema:"description=Their picture's address (absent: show their initials)."`
	CreatedAt   time.Time  `json:"createdAt"`
}

// AuthStatusOut is how this MetaRush signs people in.
type AuthStatusOut struct {
	Method      string   `json:"method" jsonschema:"enum=single,enum=password,enum=tailscale"`
	You         *Account `json:"you,omitempty"`
	Owner       bool     `json:"owner" jsonschema:"description=The person asking is an owner."`
	Owners      []string `json:"owners" jsonschema:"description=The owners' names: they change this computer's settings, keys, storage and who signs in."`
	PublicURL   string   `json:"publicUrl,omitempty" jsonschema:"description=The address other computers use (remote access)."`
	PublicHTTPS bool     `json:"publicHttps"`
	Here        bool     `json:"here" jsonschema:"description=Asked on this computer (the sign-in method can be changed only here)."`
	People      int      `json:"people"`
	Summary     string   `json:"summary"`
}

// AuthMethodIn chooses the sign-in method, with the owner's own sign-in.
type AuthMethodIn struct {
	Method          string `json:"method" jsonschema:"enum=single,enum=password,enum=tailscale"`
	Email           string `json:"email,omitempty" jsonschema:"maxLength=254,description=Email and password: your email."`
	Password        string `json:"password,omitempty" jsonschema:"maxLength=1024,description=Email and password: your password (12 characters or more)."`
	CurrentPassword string `json:"currentPassword,omitempty" jsonschema:"maxLength=1024,description=Your current password, when you have one."`
	TailscaleLogin  string `json:"tailscaleLogin,omitempty" jsonschema:"maxLength=254,description=Tailscale accounts: your Tailscale login."`
}

// UsersListOut lists the workspace's people.
type UsersListOut struct {
	Method string    `json:"method"`
	People []Account `json:"people"`
}

// UsersInviteIn invites a person (by email) with a one-time link.
type UsersInviteIn struct {
	Email  string `json:"email" jsonschema:"maxLength=254"`
	Name   string `json:"name,omitempty" jsonschema:"maxLength=80"`
	Role   string `json:"role,omitempty" jsonschema:"enum=,enum=member,enum=owner"`
	LinkTo string `json:"linkTo,omitempty" jsonschema:"description=An earlier person (usr_…) whose history becomes theirs (found by email when not given)."`
}

// UserLinkOut is a one-time link to send a person (shown once).
type UserLinkOut struct {
	Account   Account   `json:"person"`
	Kind      string    `json:"kind" jsonschema:"enum=invite,enum=reset"`
	Link      string    `json:"link" jsonschema:"description=The link to send them; it works once."`
	ExpiresAt time.Time `json:"expiresAt"`
	Earlier   bool      `json:"earlier" jsonschema:"description=It continues an earlier person's history."`
	Summary   string    `json:"summary"`
}

// UserRefIn names a person by ID or email.
type UserRefIn struct {
	User string `json:"user" jsonschema:"description=usr_… or their email."`
}

// UserRoleIn makes a person an owner or a member.
type UserRoleIn struct {
	User string `json:"user"`
	Role string `json:"role" jsonschema:"enum=owner,enum=member"`
}

// UserOut is one person after a change.
type UserOut struct {
	Account Account `json:"person"`
	Summary string  `json:"summary"`
}

// AccountPasswordIn changes your own password.
type AccountPasswordIn struct {
	Current string `json:"current,omitempty" jsonschema:"maxLength=1024"`
	New     string `json:"new" jsonschema:"maxLength=1024"`
}

// AccountUpdateIn changes your own name or email.
type AccountUpdateIn struct {
	Name            string `json:"name,omitempty" jsonschema:"maxLength=80"`
	Email           string `json:"email,omitempty" jsonschema:"maxLength=254"`
	CurrentPassword string `json:"currentPassword,omitempty" jsonschema:"maxLength=1024"`
}

// AuthResetOut says what auth.reset did.
type AuthResetOut struct {
	Earlier   int    `json:"earlier" jsonschema:"description=People kept as earlier people (their history stays theirs)."`
	SignedOut int    `json:"signedOut" jsonschema:"description=Browser sessions signed out on this computer."`
	Summary   string `json:"summary"`
}

func init() {
	ops.Def("auth.status", "How people sign in to this MetaRush (just you on this computer, email and password, or Tailscale accounts) and who you are", ops.Flags{}, status)
	ops.Def("auth.method.set", "Choose how people sign in, giving your own sign-in at the same step (owners, on this computer)", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, setMethodOp)
	ops.Def("auth.reset", "Put sign-in back to a fresh install's: just the owner on this computer, everyone signed out, people kept with their history (on this computer)", ops.Flags{Mutating: true, HumanOnly: true, LocalOnly: true}, resetOp)
	ops.Def("users.list", "The workspace's people: accounts, invites, earlier people and their roles", ops.Flags{}, listOp)
	ops.Def("users.invite", "Invite a person by email: a one-time link they follow to choose a password (owners)", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, inviteOp)
	ops.Def("users.resetLink", "A one-time link a person follows to choose a new password (owners)", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, resetLinkOp)
	ops.Def("users.revoke", "Take back a person's invite or reset link (owners)", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, revokeOp)
	ops.Def("users.disable", "Turn a person's account off: signed out everywhere, can't sign in (owners)", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, in *UserRefIn) (*UserOut, error) {
			return stateOp(ctx, a, in, models.UserDisabled)
		})
	ops.Def("users.enable", "Turn a person's account back on (owners)", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, in *UserRefIn) (*UserOut, error) {
			return stateOp(ctx, a, in, models.UserActive)
		})
	ops.Def("users.remove", "Remove a person's account; their name stays on what they did (owners)", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, in *UserRefIn) (*UserOut, error) {
			return stateOp(ctx, a, in, models.UserEarlier)
		})
	ops.Def("users.role", "Make a person an owner or a member (owners)", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, roleOp)
	ops.Def("account.password", "Change your own password (asks for the current one); signs you out everywhere", ops.Flags{Mutating: true, HumanOnly: true}, passwordOp)
	ops.Def("account.update", "Change your own name or email (an email change asks for your password)", ops.Flags{Mutating: true, HumanOnly: true}, updateOp)
	ops.Def("account.signoutEverywhere", "Sign yourself out on every computer and browser", ops.Flags{Mutating: true, HumanOnly: true}, signoutOp)
}

// you is the person asking (the signed-in person, or this computer's user).
func you(a *app.App) string {
	if a.Actor.UserID != "" {
		return a.Actor.UserID
	}
	return a.Cfg.User.ID
}

func personOf(ctx context.Context, a *app.App, u *models.User, owner bool) Account {
	p := Account{ID: u.ID, Name: u.Name, Email: u.Email, Role: u.Role, State: u.State, You: u.ID == you(a), LastSignIn: u.LastSignedInAt, CreatedAt: u.CreatedAt,
		Avatar: AvatarURL(u)}
	if p.Role == "" {
		p.Role = models.RoleMember
	}
	if IsOwner(ctx, a, u.ID) {
		p.Role = models.RoleOwner
	}
	if p.State == "" {
		p.State = models.UserEarlier
		if u.ID == a.Cfg.User.ID {
			p.State = models.UserActive
		}
	}
	p.HasPassword = HasPassword(ctx, a, u.ID)
	if owner {
		p.Logins = u.Logins
		n, _ := a.DB.C(store.CAudit).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "actor.userId": u.ID})
		p.Work = int(n)
		if u.State == models.UserInvited {
			var rec LinkRecord
			if a.DB.C(store.CAuthTokens).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "userId": u.ID, "kind": LinkInvite, "usedAt": nil, "revokedAt": nil}).Decode(&rec) == nil {
				p.InviteUntil = &rec.ExpiresAt
			}
		}
	}
	return p
}

func methodWords(m string) string {
	switch m {
	case models.AuthPassword:
		return "People sign in with their email and password."
	case models.AuthTailscale:
		return "People sign in with their Tailscale accounts."
	}
	return "Just you, on this computer: nobody else can sign in."
}

func status(ctx context.Context, a *app.App, _ *ops.EmptyIn) (*AuthStatusOut, error) {
	m := Method(ctx, a)
	out := &AuthStatusOut{Method: m, Here: !ops.CallerFrom(ctx).Elsewhere, Summary: methodWords(m)}
	// As owner-only ops decide (D191): the signed-in person's role over HTTP,
	// this computer's own command line counts as the owner.
	out.Owner = ops.IsOwnerCaller(ctx)
	out.Owners = OwnerNames(ctx, a)
	if u, err := GetUser(ctx, a, you(a)); err == nil {
		p := personOf(ctx, a, u, false)
		out.You = &p
	}
	rm := a.Cfg.Remote()
	out.PublicURL = rm.PublicURL
	out.PublicHTTPS = strings.HasPrefix(rm.PublicURL, "https://")
	n, _ := a.DB.C(store.CUsers).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "state": bson.M{"$in": bson.A{models.UserActive, models.UserInvited}}})
	out.People = int(n)
	return out, nil
}

func hereOnly(ctx context.Context, a *app.App) error {
	if ops.CallerFrom(ctx).Elsewhere {
		return apperr.New(apperr.Unauthorized, "The sign-in method can be changed only at %s.", a.Cfg.MachineName()).
			WithHint("Use MetaRush on that computer.").WithDetails(map[string]any{"reason": "elsewhere"})
	}
	return nil
}

func setMethodOp(ctx context.Context, a *app.App, in *AuthMethodIn) (*AuthStatusOut, error) {
	if err := hereOnly(ctx, a); err != nil {
		return nil, err
	}
	owner := you(a)
	switch in.Method {
	case models.AuthPassword:
		if _, err := SetUpPassword(ctx, a, owner, in.Email, in.Password, in.CurrentPassword); err != nil {
			return nil, err
		}
	case models.AuthTailscale:
		if err := SetUpTailscale(ctx, a, owner, in.TailscaleLogin); err != nil {
			return nil, err
		}
		if err := linkTailscaleLogins(ctx, a); err != nil {
			return nil, err
		}
	case models.AuthSingle:
		if err := SetUpSingle(ctx, a, owner); err != nil {
			return nil, err
		}
		if err := clearTailscaleLogins(a); err != nil {
			return nil, err
		}
	default:
		return nil, apperr.New(apperr.InvalidInput, "Choose single, password or tailscale.")
	}
	return status(ctx, a, nil)
}

// linkTailscaleLogins lets in the Tailscale logins of the workspace's active
// people (the Tailscale method's list in this computer's config.yaml).
func linkTailscaleLogins(ctx context.Context, a *app.App) error {
	var us []models.User
	if err := a.DB.Find(ctx, store.CUsers, a.WorkspaceID(), bson.M{"state": models.UserActive, "logins.0": bson.M{"$exists": true}}, &us); err != nil {
		return err
	}
	// Kept: accounts already let in (with remote allow) who haven't signed in yet.
	seen := map[string]bool{}
	var logins []string
	for _, l := range a.Cfg.Remote().Logins {
		if !seen[l] {
			seen[l] = true
			logins = append(logins, l)
		}
	}
	for _, u := range us {
		for _, l := range u.Logins {
			if !seen[l] {
				seen[l] = true
				logins = append(logins, l)
			}
		}
	}
	return saveLogins(a, logins)
}

func clearTailscaleLogins(a *app.App) error { return saveLogins(a, []string{}) }

func saveLogins(a *app.App, logins []string) error {
	if logins == nil {
		logins = []string{}
	}
	cur := a.Cfg.Remote()
	if err := config.SetField(a.Cfg.Home(), []string{"dashboard", "tailscaleLogins"}, logins); err != nil {
		return apperr.Wrap(apperr.Internal, err, "MetaRush couldn't save the sign-in settings.")
	}
	if err := a.Cfg.SetRemote(config.Dashboard{PublicURL: cur.PublicURL, TailscaleLogins: logins, MachineName: cur.MachineName}); err != nil {
		return apperr.Wrap(apperr.Internal, err, "MetaRush couldn't use the new sign-in settings.")
	}
	return nil
}

func resetOp(ctx context.Context, a *app.App, _ *ops.EmptyIn) (*AuthResetOut, error) {
	earlier, err := Reset(ctx, a)
	if err != nil {
		return nil, err
	}
	if err := clearTailscaleLogins(a); err != nil {
		return nil, err
	}
	n, _ := sessions.For(a.Cfg.StateDir()).RemoveWhere(func(sessions.Session) bool { return true })
	out := &AuthResetOut{Earlier: earlier, SignedOut: n}
	out.Summary = "Sign-in is back to a fresh install's: just you, on this computer. Every browser is signed out; open the dashboard with metarush dashboard."
	if earlier > 0 {
		out.Summary += " " + plural(earlier, "person keeps", "people keep") + " their history until you invite them."
	}
	return out, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return itoa(n) + " " + many
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func listOp(ctx context.Context, a *app.App, _ *ops.EmptyIn) (*UsersListOut, error) {
	var us []models.User
	if err := a.DB.Find(ctx, store.CUsers, a.WorkspaceID(), bson.M{}, &us); err != nil {
		return nil, err
	}
	owner := IsOwner(ctx, a, you(a))
	out := &UsersListOut{Method: Method(ctx, a), People: []Account{}}
	for i := range us {
		out.People = append(out.People, personOf(ctx, a, &us[i], owner))
	}
	return out, nil
}

// resolve finds a person by ID or email.
func resolve(ctx context.Context, a *app.App, ref string) (*models.User, error) {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "usr_") {
		if u, err := GetUser(ctx, a, ref); err == nil {
			return u, nil
		}
	} else if u, _ := FindByEmail(ctx, a, ref); u != nil {
		return u, nil
	}
	return nil, apperr.New(apperr.NotFound, "MetaRush can't find that person.").WithHint("Settings → People lists everyone.").WithCommand("metarush users list")
}

func linkURL(a *app.App, token string) string { return dashboardURL(a, "/welcome/"+token) }

// dashboardURL is a dashboard address for a link: the public address when
// remote access is on, else this computer's.
func dashboardURL(a *app.App, path string) string { return ops.DashboardURL(a, path) }

func inviteOp(ctx context.Context, a *app.App, in *UsersInviteIn) (*UserLinkOut, error) {
	before, _ := FindByEmail(ctx, a, in.Email)
	if before == nil && in.LinkTo != "" {
		before, _ = GetUser(ctx, a, in.LinkTo)
	}
	u, token, exp, err := Invite(ctx, a, in.Email, in.Name, in.Role, in.LinkTo)
	if err != nil {
		return nil, err
	}
	out := &UserLinkOut{Account: personOf(ctx, a, u, true), Kind: LinkInvite, Link: linkURL(a, token), ExpiresAt: exp}
	out.Earlier = before != nil && (before.State == "" || before.State == models.UserEarlier) && out.Account.Work > 0
	out.Summary = "Send " + orEmail(u) + " this link: it works once, for 7 days, and lets them choose a password."
	if out.Earlier {
		out.Summary += " It continues their earlier history (" + plural(out.Account.Work, "thing", "things") + " they did)."
	}
	if !strings.HasPrefix(out.Link, "https://") {
		out.Summary += " The link works only on this computer until remote access is on."
	}
	return out, nil
}

func resetLinkOp(ctx context.Context, a *app.App, in *UserRefIn) (*UserLinkOut, error) {
	u, err := resolve(ctx, a, in.User)
	if err != nil {
		return nil, err
	}
	u, token, exp, kind, err := ResetLink(ctx, a, u.ID)
	if err != nil {
		return nil, err
	}
	out := &UserLinkOut{Account: personOf(ctx, a, u, true), Kind: kind, Link: linkURL(a, token), ExpiresAt: exp}
	if kind == LinkInvite {
		out.Summary = orEmail(u) + " hasn't accepted their invite: here is a new one (it works once, for 7 days)."
	} else {
		out.Summary = "Send " + orEmail(u) + " this link: it works once, for an hour, and lets them choose a new password. Their current password works until then."
	}
	return out, nil
}

func revokeOp(ctx context.Context, a *app.App, in *UserRefIn) (*UserOut, error) {
	u, err := resolve(ctx, a, in.User)
	if err != nil {
		return nil, err
	}
	revokeLinks(ctx, a, u.ID, "")
	if u.State == models.UserInvited {
		now := time.Now().UTC()
		_ = a.DB.Update(ctx, store.CUsers, a.WorkspaceID(), u.ID, bson.M{"$set": bson.M{"state": models.UserEarlier, "updatedAt": now, "updatedBy": a.Actor}})
	}
	a.Audit(ctx, "users.revoke", "user", u.ID, "", nil)
	u, _ = GetUser(ctx, a, u.ID)
	return &UserOut{Account: personOf(ctx, a, u, true), Summary: "The link for " + orEmail(u) + " doesn't work any more."}, nil
}

func stateOp(ctx context.Context, a *app.App, in *UserRefIn, state string) (*UserOut, error) {
	u, err := resolve(ctx, a, in.User)
	if err != nil {
		return nil, err
	}
	if u, err = SetState(ctx, a, u.ID, state); err != nil {
		return nil, err
	}
	words := map[string]string{models.UserDisabled: " is turned off and signed out everywhere.", models.UserActive: " can sign in again.",
		models.UserEarlier: "'s account is removed; their name stays on what they did."}
	return &UserOut{Account: personOf(ctx, a, u, true), Summary: orEmail(u) + words[state]}, nil
}

func roleOp(ctx context.Context, a *app.App, in *UserRoleIn) (*UserOut, error) {
	u, err := resolve(ctx, a, in.User)
	if err != nil {
		return nil, err
	}
	if u, err = SetRole(ctx, a, u.ID, in.Role); err != nil {
		return nil, err
	}
	return &UserOut{Account: personOf(ctx, a, u, true), Summary: orEmail(u) + " is now " + map[string]string{models.RoleOwner: "an owner.", models.RoleMember: "a member."}[in.Role]}, nil
}

func passwordOp(ctx context.Context, a *app.App, in *AccountPasswordIn) (*UserOut, error) {
	if err := ChangePassword(ctx, a, you(a), in.Current, in.New); err != nil {
		return nil, err
	}
	u, _ := GetUser(ctx, a, you(a))
	return &UserOut{Account: personOf(ctx, a, u, false), Summary: "Your password is changed. You stay signed in here, and you're signed out on every other computer and browser."}, nil
}

func updateOp(ctx context.Context, a *app.App, in *AccountUpdateIn) (*UserOut, error) {
	u, err := UpdateAccount(ctx, a, you(a), in.Name, in.Email, in.CurrentPassword)
	if err != nil {
		return nil, err
	}
	return &UserOut{Account: personOf(ctx, a, u, false), Summary: "Your account is updated."}, nil
}

func signoutOp(ctx context.Context, a *app.App, _ *ops.EmptyIn) (*UserOut, error) {
	if err := SignOutEverywhere(ctx, a, you(a)); err != nil {
		return nil, err
	}
	u, _ := GetUser(ctx, a, you(a))
	return &UserOut{Account: personOf(ctx, a, u, false), Summary: "You are signed out on every computer and browser."}, nil
}
