package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/sso"
	"github.com/metavert-io/metarush/internal/store"
)

/*
 * Signing in with an organization's account (D425–D434, docs/accounts.md):
 * Google, Microsoft (Entra ID) or any OpenID Connect provider. The setup
 * (provider, app, who may join by themselves, whether passwords and Tailscale
 * accounts still work) is the workspace's, so every computer agrees on who may
 * sign in; the app's client secret is each computer's own, kept like a key
 * (internal/providers: the keychain or the home's secrets file). A new setup
 * waits (auth.ssoNext) until an owner's test sign-in on this computer passes,
 * and switching to it links the tested account to that owner, so an owner
 * never switches into a sign-in they can't use (D430). `metarush dashboard`
 * at this computer and `metarush auth reset` keep working whatever happens at
 * the provider (D434).
 */

// ssoSecretName is the app's client secret among this computer's keys.
const ssoSecretName = "sso-client"

func init() {
	// The activity log hides the client secret wherever it would appear.
	logbook.AddSecretSource(func(cfg *config.Config) []string {
		if k, _, err := providers.Key(cfg, ssoSecretName); err == nil && k != "" {
			return []string{k}
		}
		return nil
	})
}

// SSOSecretEnv is the environment variable that gives (and overrides) this
// computer's client secret.
func SSOSecretEnv() string { return providers.EnvVar(ssoSecretName) }

// SSOSecret is this computer's client secret for the app ("" none) and where
// it is kept.
func SSOSecret(cfg *config.Config) (secret, source string, err error) {
	secret, source, err = providers.Key(cfg, ssoSecretName)
	if err != nil {
		return "", "", apperr.Wrap(apperr.NotConfigured, err, "MetaRush couldn't read the sign-in app's secret on this computer.").
			WithHint("Check that this computer's keychain is unlocked.")
	}
	return secret, source, nil
}

// SetSSOSecret keeps this computer's client secret (never in the database,
// config.yaml, a log or an answer).
func SetSSOSecret(cfg *config.Config, secret string) (string, error) {
	secret = strings.TrimSpace(secret)
	if err := providers.ValidateKeyShape(secret); err != nil {
		return "", apperr.New(apperr.InvalidInput, "That doesn't look like a client secret: it is 16 to 512 characters with no spaces, as the provider shows it.")
	}
	logbook.RememberSecret(secret)
	if os.Getenv(SSOSecretEnv()) != "" {
		return "", apperr.New(apperr.Conflict, "This computer's settings already give MetaRush the sign-in app's secret, and it would override a saved one.").
			WithHint("Change it where it is set instead.").WithDetails(map[string]any{"envVar": SSOSecretEnv()})
	}
	src, err := providers.StoreKey(cfg, ssoSecretName, secret, false)
	if err != nil {
		return "", apperr.Wrap(apperr.NotConfigured, err, "MetaRush couldn't keep the sign-in app's secret on this computer.").WithHint("Check that this computer's keychain is unlocked.")
	}
	return src, nil
}

// RemoveSSOSecret forgets this computer's client secret.
func RemoveSSOSecret(cfg *config.Config) (bool, error) {
	ok, err := providers.RemoveKey(cfg, ssoSecretName)
	if err != nil {
		return false, apperr.Wrap(apperr.Conflict, err, "MetaRush couldn't remove the sign-in app's secret on this computer.")
	}
	return ok, nil
}

// SSOClient is the client for a setup, with this computer's secret.
func SSOClient(cfg *config.Config, st *models.SSOSettings) (*sso.Client, error) {
	secret, _, err := SSOSecret(cfg)
	if err != nil {
		return nil, err
	}
	return &sso.Client{Kind: st.Provider, Issuer: st.Issuer, ClientID: st.ClientID, Secret: secret}, nil
}

// SSOFingerprint names a setup as sign-ins use it on this computer: the
// provider, its address, the app and this computer's secret (a test of one
// says nothing about another, D430).
func SSOFingerprint(cfg *config.Config, st *models.SSOSettings) string {
	if st == nil {
		return ""
	}
	secret, _, _ := SSOSecret(cfg)
	s := sha256.Sum256([]byte(secret))
	sum := sha256.Sum256([]byte(strings.Join([]string{st.Provider, st.Issuer, st.Tenant, st.ClientID, hex.EncodeToString(s[:])}, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// SSOLocalOrigin is where this computer's own browser signs in: localhost,
// which Google and Microsoft both take as an http address (Microsoft's
// portal doesn't take 127.0.0.1 over http, D428).
func SSOLocalOrigin(cfg *config.Config) string {
	return fmt.Sprintf("http://localhost:%d", cfg.Dashboard.Port)
}

// SSOLocalHost is the Host this computer's browser signs in at.
func SSOLocalHost(cfg *config.Config) string { return fmt.Sprintf("localhost:%d", cfg.Dashboard.Port) }

// SSOCallbacks are the addresses an owner registers with the provider: this
// computer's, and with remote access on, the address for other computers.
func SSOCallbacks(cfg *config.Config) (local, public string) {
	local = SSOLocalOrigin(cfg) + sso.CallbackPath
	if rm := cfg.Remote(); rm.On() {
		public = rm.PublicURL + sso.CallbackPath
	}
	return local, public
}

/* ---------- Setting it up ---------- */

var domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// CleanSSO checks a setup and fills in what its provider fixes.
func CleanSSO(in models.SSOSettings) (*models.SSOSettings, error) {
	st := models.SSOSettings{Provider: strings.ToLower(strings.TrimSpace(in.Provider)), ClientID: strings.TrimSpace(in.ClientID),
		Domain: strings.ToLower(strings.TrimSpace(in.Domain)), Label: strings.TrimSpace(in.Label), AlsoPassword: in.AlsoPassword, AlsoTailscale: in.AlsoTailscale}
	if st.ClientID == "" || len(st.ClientID) > 255 || strings.ContainsAny(st.ClientID, " \t\r\n") {
		return nil, apperr.New(apperr.InvalidInput, "Give the app's client ID, as the provider shows it.").WithDetails(map[string]any{"reason": "client_id"})
	}
	if len([]rune(st.Label)) > 40 {
		return nil, apperr.New(apperr.InvalidInput, "The provider's name is too long (40 characters at most).")
	}
	switch st.Provider {
	case sso.Google:
		st.Issuer, st.Label = sso.GoogleIssuer, ""
		if st.Domain != "" && !domainRe.MatchString(st.Domain) {
			return nil, apperr.New(apperr.InvalidInput, "Give the Google Workspace domain as people's emails end, like example.com.").WithDetails(map[string]any{"reason": "domain"})
		}
	case sso.Microsoft:
		st.Tenant, st.Label = strings.ToLower(strings.TrimSpace(in.Tenant)), ""
		switch {
		case st.Tenant == "":
			return nil, apperr.New(apperr.InvalidInput, "Give your organization's directory (tenant) ID, from the app's Overview page in Microsoft Entra.").WithDetails(map[string]any{"reason": "tenant"})
		case sso.IsDirectoryID(st.Tenant), st.Tenant == "organizations", st.Tenant == "common", st.Tenant == "consumers", domainRe.MatchString(st.Tenant):
		default:
			return nil, apperr.New(apperr.InvalidInput, "That isn't a Microsoft directory: give its ID (from the app's Overview page), or organizations.").WithDetails(map[string]any{"reason": "tenant"})
		}
		st.Issuer = sso.MicrosoftIssuer(st.Tenant)
		if st.Domain != "" {
			if !sso.IsDirectoryID(st.Domain) {
				return nil, apperr.New(apperr.InvalidInput, "For Microsoft, people join by themselves from a directory: give its ID.").WithDetails(map[string]any{"reason": "domain"})
			}
			if sso.IsDirectoryID(st.Tenant) && st.Domain != st.Tenant {
				return nil, apperr.New(apperr.InvalidInput, "Only people from the directory this sign-in is for can sign in, so that is the only one they can join from.").WithDetails(map[string]any{"reason": "domain"})
			}
		}
	case sso.OIDC:
		st.Issuer = strings.TrimSpace(in.Issuer)
		if err := sso.CheckIssuer(st.Issuer); err != nil {
			return nil, apperr.New(apperr.InvalidInput, "%s", err.Error()).WithHint(problemHint(err)).WithDetails(map[string]any{"reason": "issuer"})
		}
		if st.Domain != "" && !domainRe.MatchString(st.Domain) {
			return nil, apperr.New(apperr.InvalidInput, "Give the email domain as people's emails end, like example.com.").WithDetails(map[string]any{"reason": "domain"})
		}
	default:
		return nil, apperr.New(apperr.InvalidInput, "Choose google, microsoft or oidc (another provider).").WithDetails(map[string]any{"reason": "provider"})
	}
	return &st, nil
}

func problemHint(err error) string {
	var p *sso.Problem
	if errors.As(err, &p) {
		return p.Hint
	}
	return ""
}

// sameSignIn reports two setups people sign in with the same way: the same
// provider, address and app (who may join and the other sign-ins may differ).
func sameSignIn(a, b *models.SSOSettings) bool {
	return a != nil && b != nil && a.Provider == b.Provider && a.Issuer == b.Issuer && a.Tenant == b.Tenant && a.ClientID == b.ClientID
}

// SaveSSO keeps a setup. Changes to who may join or to the other sign-ins of
// the setup in use apply at once; a new provider or app waits for its test
// sign-in (D430). It says whether the change applied.
func SaveSSO(ctx context.Context, a *app.App, in models.SSOSettings) (applied bool, err error) {
	st, err := CleanSSO(in)
	if err != nil {
		return false, err
	}
	st.SetAt, st.SetBy = time.Now().UTC(), a.Actor
	cur := Settings(ctx, a)
	field := "auth.ssoNext"
	if sameSignIn(cur.SSO, st) {
		field, applied = "auth.sso", true
	}
	update := bson.M{"$set": bson.M{field: st}, "$setOnInsert": bson.M{"workspaceId": a.WorkspaceID(), "name": a.Cfg.Workspace.Name, "createdAt": time.Now().UTC()}}
	if applied {
		update["$unset"] = bson.M{"auth.ssoNext": ""}
	}
	if _, err := a.DB.C(store.CWorkspaces).UpdateOne(ctx, bson.M{"_id": a.WorkspaceID()}, update, options.UpdateOne().SetUpsert(true)); err != nil {
		return false, err
	}
	forgetSettings(a)
	a.Audit(ctx, "auth.sso.set", "workspace", a.WorkspaceID(), "", map[string]any{"provider": st.Provider, "applied": applied, "joinDomain": st.Domain != "",
		"alsoPassword": st.AlsoPassword, "alsoTailscale": st.AlsoTailscale})
	return applied, nil
}

// TestedSetup is the setup a test sign-in tries: the one waiting, else the
// one in use (or last used).
func TestedSetup(st models.AuthSettings) *models.SSOSettings {
	if st.SSONext != nil {
		return st.SSONext
	}
	return st.SSO
}

// SetupFor finds the setup a sign-in began with by its fingerprint (the
// setup may have changed while the person was at the provider).
func SetupFor(ctx context.Context, a *app.App, fingerprint string) *models.SSOSettings {
	st := Settings(ctx, a)
	for _, s := range []*models.SSOSettings{st.SSO, st.SSONext} {
		if s != nil && SSOFingerprint(a.Cfg, s) == fingerprint {
			return s
		}
	}
	return nil
}

/* ---------- Who signs in ---------- */

// Why an organization sign-in is refused (details.reason).
const (
	SSONotMember       = "not_member"       // no account here and no allowed domain
	SSODisabled        = "disabled"         // their account is turned off
	SSORemoved         = "removed"          // their account was removed
	SSOLinkedElsewhere = "linked_elsewhere" // the email's person signs in with another account at the provider
	SSOInviteEmail     = "invite_email"     // an invite link followed with another email
	SSOInviteGone      = "invite_gone"      // the invite link was used, ran out or was taken back
)

func ssoRefusal(reason, msg, hint string, id *sso.Identity) error {
	d := map[string]any{"reason": reason}
	if id != nil && id.Email != "" {
		d["email"] = id.Email
	}
	if id != nil && id.Why != "" {
		d["emailWhy"] = id.Why
	}
	return apperr.New(apperr.Unauthorized, "%s", msg).WithHint(hint).WithDetails(d)
}

// ByKey finds the person a provider account is linked to (nil: nobody).
func ByKey(ctx context.Context, a *app.App, key string) (*models.User, error) {
	var us []models.User
	if err := a.DB.Find(ctx, store.CUsers, a.WorkspaceID(), bson.M{"sso.key": key}, &us); err != nil {
		return nil, err
	}
	if len(us) == 0 {
		return nil, nil
	}
	return &us[0], nil
}

// byEmailKey finds a person by their email exactly (never a Tailscale login).
func byEmailKey(ctx context.Context, a *app.App, email string) (*models.User, error) {
	key := EmailKey(email)
	if key == "" {
		return nil, nil
	}
	var us []models.User
	if err := a.DB.Find(ctx, store.CUsers, a.WorkspaceID(), bson.M{"emailKey": key}, &us); err != nil {
		return nil, err
	}
	if len(us) == 0 {
		return nil, nil
	}
	return &us[0], nil
}

func linkOf(id *sso.Identity) models.SSOLink {
	l := models.SSOLink{Key: id.Key, Provider: id.Provider, Issuer: id.Issuer, Subject: id.Subject, LinkedAt: time.Now().UTC()}
	if id.EmailTrusted {
		l.Email = id.Email
	}
	return l
}

// otherLink reports a person already signing in with another account of the
// same provider: a different account showing the same email (another
// directory's, a recreated one) doesn't take theirs over (D429).
func otherLink(u *models.User, id *sso.Identity) bool {
	for _, l := range u.SSO {
		if l.Provider == id.Provider && l.Key != id.Key {
			return true
		}
	}
	return false
}

// link adds a provider account to a person (once; the unique index refuses
// it on anyone else).
func link(ctx context.Context, a *app.App, userID string, id *sso.Identity, set bson.M) error {
	update := bson.M{"$push": bson.M{"sso": linkOf(id)}}
	if len(set) > 0 {
		update["$set"] = set
	}
	res, err := a.DB.C(store.CUsers).UpdateOne(ctx, bson.M{"_id": userID, "workspaceId": a.WorkspaceID(), "sso.key": bson.M{"$ne": id.Key}}, update)
	if err != nil {
		if store.IsDuplicateKey(err) {
			return ssoRefusal(SSOLinkedElsewhere, "That account already signs someone else in to this MetaRush.", "Sign in with your own account, or ask an owner.", id)
		}
		return err
	}
	if res.MatchedCount == 0 && len(set) > 0 {
		// Linked already: only the rest of the change.
		return a.DB.Update(ctx, store.CUsers, a.WorkspaceID(), userID, bson.M{"$set": set})
	}
	forget(userID)
	return nil
}

// PersonForSSO decides who an organization sign-in is (D429): the person the
// account is linked to; else, by an email the provider answers for, a person
// of the workspace or an invite (linked now); else, with an allowed domain,
// a new member. Never anyone else. invite is the link a person followed to
// join (nil: the sign-in page): its email must be the one the provider gives.
func PersonForSSO(ctx context.Context, a *app.App, st *models.SSOSettings, id *sso.Identity, invite *LinkRecord) (u *models.User, joined bool, err error) {
	now := time.Now().UTC()
	if invite != nil {
		return joinByInvite(ctx, a, id, invite)
	}
	if u, err = ByKey(ctx, a, id.Key); err != nil {
		return nil, false, err
	}
	if u != nil {
		switch u.State {
		case models.UserDisabled:
			return nil, false, ssoRefusal(SSODisabled, "Your account here is turned off.", "Ask an owner of this MetaRush to turn it back on.", id)
		case models.UserEarlier, "":
			if u.ID != a.Cfg.User.ID {
				return nil, false, ssoRefusal(SSORemoved, "Your account here was removed.", "Ask an owner of this MetaRush to invite you again.", id)
			}
		}
		set := bson.M{"lastSignedInAt": now}
		if u.State == models.UserInvited {
			set["state"], joined = models.UserActive, true
		}
		_ = a.DB.Update(ctx, store.CUsers, a.WorkspaceID(), u.ID, bson.M{"$set": set})
		forget(u.ID)
		u, err = GetUser(ctx, a, u.ID)
		return u, joined, err
	}
	if id.EmailTrusted {
		v, err := byEmailKey(ctx, a, id.Email)
		if err != nil {
			return nil, false, err
		}
		if v != nil {
			switch {
			case v.State == models.UserDisabled:
				return nil, false, ssoRefusal(SSODisabled, "Your account here is turned off.", "Ask an owner of this MetaRush to turn it back on.", id)
			case v.State == models.UserEarlier || v.State == "":
				return nil, false, ssoRefusal(SSONotMember, fmt.Sprintf("MetaRush on %s doesn't have an account for %s.", a.Cfg.MachineName(), id.Email),
					"Ask an owner of this MetaRush to invite you.", id)
			case otherLink(v, id):
				return nil, false, ssoRefusal(SSOLinkedElsewhere, fmt.Sprintf("The account for %s signs in with another %s account.", id.Email, sso.Title(id.Provider, st.Label)),
					"Sign in with that account, or ask an owner to remove and invite you again.", id)
			}
			set := bson.M{"lastSignedInAt": now, "updatedAt": now}
			if v.State == models.UserInvited {
				set["state"], joined = models.UserActive, true // the invite is accepted
				revokeLinks(ctx, a, v.ID, LinkInvite)
			}
			if err := link(ctx, a, v.ID, id, set); err != nil {
				return nil, false, err
			}
			if joined {
				auditAs(ctx, a, v, "auth.sso.joined", map[string]any{"provider": id.Provider, "by": "invite"})
			}
			u, err = GetUser(ctx, a, v.ID)
			return u, joined, err
		}
	}
	if st.Domain == "" || id.Domain == "" || !strings.EqualFold(st.Domain, id.Domain) {
		return nil, false, notMember(a, st, id)
	}
	// The allowed domain: a new member, with their verified email.
	nu := models.User{ID: ids.New("usr"), WorkspaceID: a.WorkspaceID(), Name: firstNonEmpty(id.Name, emailName(id.Email), "New member"), Role: models.RoleMember,
		State: models.UserActive, Source: "sso", SSO: []models.SSOLink{linkOf(id)}, CreatedAt: now, LastSignedInAt: &now}
	if id.EmailTrusted {
		nu.Email, nu.EmailKey = id.Email, EmailKey(id.Email)
	}
	if err := a.DB.Insert(ctx, store.CUsers, nu); err != nil {
		if store.IsDuplicateKey(err) {
			// Linked at the same moment by another sign-in of theirs.
			if u, _ := ByKey(ctx, a, id.Key); u != nil {
				return u, false, nil
			}
		}
		return nil, false, err
	}
	auditAs(ctx, a, &nu, "auth.sso.joined", map[string]any{"provider": id.Provider, "by": "domain"})
	tellOwnersJoined(ctx, a, &nu, sso.Title(id.Provider, st.Label))
	return &nu, true, nil
}

func notMember(a *app.App, st *models.SSOSettings, id *sso.Identity) error {
	who := id.Email
	if who == "" {
		who = "that account"
	}
	msg := fmt.Sprintf("MetaRush on %s doesn't have an account for %s.", a.Cfg.MachineName(), who)
	hint := "Ask an owner of this MetaRush to invite you."
	if id.Email != "" && !id.EmailTrusted {
		hint = "Ask an owner to invite you, then follow the invite link they send."
	}
	return ssoRefusal(SSONotMember, msg, hint, id)
}

// joinByInvite links the account of a person following their invite link:
// the provider's email must be the invited one (D429).
func joinByInvite(ctx context.Context, a *app.App, id *sso.Identity, invite *LinkRecord) (*models.User, bool, error) {
	v, err := GetUser(ctx, a, invite.UserID)
	if err != nil || invite.Kind != LinkInvite {
		return nil, false, ssoRefusal(SSOInviteGone, "This invite doesn't work any more.", "Ask an owner of this MetaRush for a new one.", id)
	}
	if id.Email == "" || id.EmailRefuted || EmailKey(id.Email) != EmailKey(v.Email) {
		return nil, false, ssoRefusal(SSOInviteEmail, fmt.Sprintf("This invite is for %s, so sign in with that account.", v.Email),
			"Choose the account with that email at the sign-in page, or ask the owner to invite the email you use.", id)
	}
	if u, _ := ByKey(ctx, a, id.Key); u != nil && u.ID != v.ID {
		return nil, false, ssoRefusal(SSOLinkedElsewhere, "That account already signs someone else in to this MetaRush.", "Sign in with your own account, or ask an owner.", id)
	}
	now := time.Now().UTC()
	res, err := a.DB.C(store.CAuthTokens).UpdateOne(ctx, bson.M{"_id": invite.ID, "usedAt": nil, "revokedAt": nil, "$expr": notExpired}, bson.M{"$set": bson.M{"usedAt": now}})
	if err != nil {
		return nil, false, err
	}
	if res.ModifiedCount != 1 {
		return nil, false, ssoRefusal(SSOInviteGone, "This invite doesn't work any more: it was used, it ran out or it was taken back.", "Ask an owner of this MetaRush for a new one.", id)
	}
	set := bson.M{"state": models.UserActive, "lastSignedInAt": now, "updatedAt": now}
	if v.Name == "" && id.Name != "" {
		set["name"] = id.Name
	}
	if err := link(ctx, a, v.ID, id, set); err != nil {
		return nil, false, err
	}
	auditAs(ctx, a, v, "auth.sso.joined", map[string]any{"provider": id.Provider, "by": "invite"})
	u, err := GetUser(ctx, a, v.ID)
	return u, true, err
}

func emailName(email string) string {
	name, _, _ := strings.Cut(email, "@")
	return name
}

// auditAs records something a person did as them.
func auditAs(ctx context.Context, a *app.App, u *models.User, action string, details map[string]any) {
	b := *a
	b.Actor = models.Actor{UserID: u.ID, UserName: u.Name, InstallationID: a.Actor.InstallationID, Via: a.Actor.Via}
	b.Audit(ctx, action, "user", u.ID, "", details)
}

// tellOwnersJoined tells every owner someone joined by themselves.
func tellOwnersJoined(ctx context.Context, a *app.App, u *models.User, provider string) {
	var owners []models.User
	_ = a.DB.Find(ctx, store.CUsers, a.WorkspaceID(), bson.M{"role": models.RoleOwner, "state": models.UserActive}, &owners)
	if len(owners) == 0 && a.Cfg.User.ID != "" {
		owners = append(owners, models.User{ID: a.Cfg.User.ID})
	}
	who := firstNonEmpty(u.Name, u.Email)
	for _, o := range owners {
		_, _ = notify.Upsert(ctx, a, models.Notification{ID: ids.New("ntf"), WorkspaceID: a.WorkspaceID(), UserID: o.ID, Kind: models.NotifyPersonJoined,
			GroupKey: "person_joined:" + u.ID + ":" + o.ID, Tone: "info", Title: who + " joined",
			Body: fmt.Sprintf("%s signed in with %s from the domain you let join, and is now a member.", who, provider),
			Link: "/settings/people#users", CreatedAt: time.Now().UTC()}, notify.Once)
	}
}

/* ---------- Switching to it ---------- */

// ErrNoTest refuses a switch without a passing test sign-in of the setup.
func errNoTest(msg string) error {
	return apperr.New(apperr.Conflict, "%s", msg).WithHint("Test the sign-in first, in Settings → People on this computer.").
		WithDetails(map[string]any{"reason": "not_tested"}).WithCommand("metarush auth sso test")
}

// SetUpSSO makes the organization's sign-in the method (D430): the setup the
// owner's test sign-in on this computer just passed, by that owner, linked to
// them so they keep getting in.
func SetUpSSO(ctx context.Context, a *app.App, ownerID string, test *sso.TestResult) (*models.User, *models.SSOSettings, error) {
	cur := Settings(ctx, a)
	setup := TestedSetup(cur)
	if setup == nil {
		return nil, nil, apperr.New(apperr.Conflict, "Set up the sign-in with your organization's account first.").
			WithDetails(map[string]any{"reason": "no_setup"}).WithCommand("metarush auth sso set")
	}
	switch {
	case test == nil || test.Setup != SSOFingerprint(a.Cfg, setup):
		return nil, nil, errNoTest("This setup hasn't had a test sign-in on this computer yet.")
	case !test.OK:
		return nil, nil, errNoTest("The last test sign-in didn't work, so MetaRush won't switch to it.")
	case !test.Fresh():
		return nil, nil, errNoTest("The test sign-in was more than 30 minutes ago.")
	case test.UserID != ownerID:
		return nil, nil, errNoTest("Someone else did the test sign-in: the owner switching does their own, so they keep getting in.")
	case test.Identity == nil || test.Identity.Key == "":
		return nil, nil, errNoTest("The test sign-in didn't say who signed in.")
	}
	if ownerID == a.Cfg.User.ID {
		if err := ensureOwnerRecord(ctx, a); err != nil {
			return nil, nil, err
		}
	}
	owner, err := GetUser(ctx, a, ownerID)
	if err != nil {
		return nil, nil, err
	}
	if other, _ := ByKey(ctx, a, test.Identity.Key); other != nil && other.ID != ownerID {
		return nil, nil, apperr.New(apperr.Conflict, "The account you tested with signs %s in already.", firstNonEmpty(other.Name, other.Email)).
			WithHint("Test again with your own account.").WithDetails(map[string]any{"reason": SSOLinkedElsewhere})
	}
	now := time.Now().UTC()
	set := bson.M{"role": models.RoleOwner, "state": models.UserActive, "updatedAt": now, "updatedBy": a.Actor}
	if owner.EmailKey == "" && test.Identity.EmailTrusted {
		if v, _ := byEmailKey(ctx, a, test.Identity.Email); v == nil {
			set["email"], set["emailKey"] = test.Identity.Email, EmailKey(test.Identity.Email)
		}
	}
	// One account per provider: an earlier link of the owner's to the same
	// provider gives way to the tested one.
	if _, err := a.DB.C(store.CUsers).UpdateOne(ctx, bson.M{"_id": ownerID}, bson.M{"$pull": bson.M{"sso": bson.M{"provider": test.Identity.Provider, "key": bson.M{"$ne": test.Identity.Key}}}}); err != nil {
		return nil, nil, err
	}
	if err := link(ctx, a, ownerID, test.Identity, set); err != nil {
		return nil, nil, err
	}
	live := *setup
	live.SetAt, live.SetBy = now, a.Actor
	if _, err := a.DB.C(store.CWorkspaces).UpdateOne(ctx, bson.M{"_id": a.WorkspaceID()}, bson.M{
		"$set":         bson.M{"auth.method": models.AuthSSO, "auth.setAt": now, "auth.setBy": a.Actor, "auth.sso": live},
		"$unset":       bson.M{"auth.ssoNext": ""},
		"$setOnInsert": bson.M{"workspaceId": a.WorkspaceID(), "name": a.Cfg.Workspace.Name, "createdAt": now}}, options.UpdateOne().SetUpsert(true)); err != nil {
		return nil, nil, err
	}
	forgetSettings(a)
	forget(ownerID)
	a.Audit(ctx, "auth.method", "workspace", a.WorkspaceID(), "", map[string]any{"method": models.AuthSSO, "provider": live.Provider})
	u, err := GetUser(ctx, a, ownerID)
	return u, &live, err
}

// ssoLinked reports a person who signs in with an organization's account.
func ssoLinked(u *models.User) bool { return len(u.SSO) > 0 }

// SSOWords says a person's provider accounts for owners ("Google: maya@example.com").
func SSOWords(u *models.User) []string {
	var out []string
	for _, l := range u.SSO {
		w := sso.Title(l.Provider, "")
		if l.Email != "" {
			w += ": " + l.Email
		}
		out = append(out, w)
	}
	return out
}
