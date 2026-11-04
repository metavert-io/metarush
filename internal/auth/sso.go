package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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

/*
 * The app's client secret is each computer's, one per app (D450): an app is
 * the provider, its address (and Microsoft's directory) and the client ID, so
 * setting up another app never touches the secret the sign-in in use reads.
 * Each app has the secret in use and, while a new one waits for its test, a
 * next one; switching after a passing test makes the next one the one in use
 * (the old one stays until then, and an earlier app's stays for switching
 * back). The names are kept in <home>/state/sso/secrets.json (names only), so
 * the activity log knows every secret to hide.
 */

// AppID names an app's secrets: a hash of the provider, its address, the
// directory and the client ID (never the secret).
func AppID(st *models.SSOSettings) string {
	if st == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{st.Provider, st.Issuer, st.Tenant, st.ClientID}, "\x00")))
	return hex.EncodeToString(sum[:6])
}

// secretName is the key an app's secret is kept under (next: the one waiting
// for its test).
func secretName(st *models.SSOSettings, next bool) string {
	n := "sso-" + AppID(st)
	if next {
		n += "-next"
	}
	return n
}

func secretIndex(cfg *config.Config) string {
	return filepath.Join(cfg.StateDir(), "sso", "secrets.json")
}

var secretIndexMu sync.Mutex

// rememberSecretName adds (or with gone, drops) a key name from the index.
func rememberSecretName(cfg *config.Config, name string, gone bool) {
	secretIndexMu.Lock()
	defer secretIndexMu.Unlock()
	names := secretNames(cfg)
	keep := names[:0]
	for _, n := range names {
		if n != name {
			keep = append(keep, n)
		}
	}
	if !gone {
		keep = append(keep, name)
	}
	_ = os.MkdirAll(filepath.Dir(secretIndex(cfg)), 0o700)
	b, _ := json.Marshal(keep)
	tmp := secretIndex(cfg) + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, secretIndex(cfg))
	}
}

func secretNames(cfg *config.Config) []string {
	var names []string
	if b, err := os.ReadFile(secretIndex(cfg)); err == nil {
		_ = json.Unmarshal(b, &names)
	}
	return names
}

func init() {
	// The activity log hides every app's client secret wherever it would appear.
	logbook.AddSecretSource(func(cfg *config.Config) []string {
		var out []string
		for _, n := range secretNames(cfg) {
			if k, _, err := providers.Key(cfg, n); err == nil && k != "" {
				out = append(out, k)
			}
		}
		return out
	})
}

// SSOSecretEnv is the environment variable that gives (and overrides) an
// app's secret in use.
func SSOSecretEnv(st *models.SSOSettings) string { return providers.EnvVar(secretName(st, false)) }

func readSecret(cfg *config.Config, name string) (string, string, error) {
	secret, source, err := providers.Key(cfg, name)
	if err != nil {
		return "", "", apperr.Wrap(apperr.NotConfigured, err, "MetaRush couldn't read the sign-in app's secret on this computer.").
			WithHint("Check that this computer's keychain is unlocked.")
	}
	if source == providers.SourceEnvironment && config.FromDotEnv(providers.EnvVar(name)) {
		// This process read it from the home's secrets file when it started,
		// and the file no longer has it: another process removed or moved it.
		return "", "", nil
	}
	if secret != "" {
		logbook.RememberSecret(secret) // hidden wherever it would appear, wherever it came from
	}
	return secret, source, nil
}

// SSOSecret is the secret an app's sign-ins use ("" none) and where it is kept.
func SSOSecret(cfg *config.Config, st *models.SSOSettings) (secret, source string, err error) {
	if st == nil {
		return "", "", nil
	}
	return readSecret(cfg, secretName(st, false))
}

// SSONextSecret is the secret waiting for a test of an app ("" none).
func SSONextSecret(cfg *config.Config, st *models.SSOSettings) (secret, source string, err error) {
	if st == nil {
		return "", "", nil
	}
	return readSecret(cfg, secretName(st, true))
}

// testSecret is what a test of a setup signs in with: its next secret, else
// the one in use.
func testSecret(cfg *config.Config, st *models.SSOSettings) string {
	if s, _, _ := SSONextSecret(cfg, st); s != "" {
		return s
	}
	s, _, _ := SSOSecret(cfg, st)
	return s
}

// SSOSecretKept reports that this computer has a secret for an app (in use
// or waiting for its test): a setup of that app may leave it out.
func SSOSecretKept(cfg *config.Config, st *models.SSOSettings) bool { return testSecret(cfg, st) != "" }

// checkNextSecret says whether an app's new secret can be kept: its shape,
// and no variable set outside MetaRush that would win over it.
func checkNextSecret(cfg *config.Config, st *models.SSOSettings, secret string) error {
	if err := providers.ValidateKeyShape(strings.TrimSpace(secret)); err != nil {
		return apperr.New(apperr.InvalidInput, "That doesn't look like a client secret: it is 16 to 512 characters with no spaces, as the provider shows it.")
	}
	for _, n := range []string{secretName(st, true), secretName(st, false)} {
		// A variable set outside MetaRush (not its own secrets file) would
		// win over the one kept here, now or after the switch.
		if _, src, _ := readSecret(cfg, n); src == providers.SourceEnvironment {
			return apperr.New(apperr.Conflict, "This computer's settings already give MetaRush this app's secret, and it would override a saved one.").
				WithHint("Change it where it is set instead.").WithDetails(map[string]any{"envVar": providers.EnvVar(n)})
		}
	}
	return nil
}

// setNextSecret keeps an app's new secret, waiting for its test (never in the
// database, config.yaml, a log or an answer; the one in use is untouched).
// Once kept it is hidden in everything this process logs (StoreKey); one it
// refuses is hidden only in the call that sent it.
func setNextSecret(cfg *config.Config, st *models.SSOSettings, secret string) error {
	secret = strings.TrimSpace(secret)
	if err := checkNextSecret(cfg, st, secret); err != nil {
		return err
	}
	name := secretName(st, true)
	if _, err := providers.StoreKey(cfg, name, secret, false); err != nil {
		return apperr.Wrap(apperr.NotConfigured, err, "MetaRush couldn't keep the sign-in app's secret on this computer.").WithHint("Check that this computer's keychain is unlocked.")
	}
	rememberSecretName(cfg, name, false)
	return nil
}

// bumpSetup gives a stored setup (path auth.sso or auth.ssoNext) a new
// revision while it is still exactly the one read: a switch tested before
// doesn't go through (D450).
func bumpSetup(ctx context.Context, a *app.App, path string, st *models.SSOSettings) error {
	f := setupIs(path, st)
	f["_id"] = a.WorkspaceID()
	res, err := a.DB.C(store.CWorkspaces).UpdateOne(ctx, f, bson.M{"$set": bson.M{path + ".rev": newRev(), path + ".setAt": time.Now().UTC(), path + ".setBy": a.Actor}})
	forgetSettings(a)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return errSetupChanged()
	}
	return nil
}

// promoteSecret makes the secret a switch tested with the one in use (D450):
// exactly that secret, never one kept since. A secret tested from the next
// slot must still be waiting there, else the switch fails plainly; the one
// it replaces comes back with undo (the switch wasn't kept), and the waiting
// copy goes with finish (it was).
func promoteSecret(cfg *config.Config, use *SSOUse) (undo, finish func(), err error) {
	noop := func() {}
	st := use.Setup
	if !use.FromNext {
		// Tested with the secret in use: nothing to promote, but it must
		// still be the one there.
		if cur, _, _ := SSOSecret(cfg, st); subtle.ConstantTimeCompare([]byte(cur), []byte(use.Secret)) != 1 {
			return noop, noop, errSetupChanged()
		}
		return noop, noop, nil
	}
	next, _, err := SSONextSecret(cfg, st)
	if err != nil {
		return noop, noop, err
	}
	if next == "" || subtle.ConstantTimeCompare([]byte(next), []byte(use.Secret)) != 1 {
		return noop, noop, errSetupChanged()
	}
	cur := secretName(st, false)
	prev, _, _ := SSOSecret(cfg, st)
	if _, err := providers.StoreKey(cfg, cur, use.Secret, false); err != nil {
		return noop, noop, apperr.Wrap(apperr.NotConfigured, err, "MetaRush couldn't keep the sign-in app's secret on this computer.").WithHint("Check that this computer's keychain is unlocked.")
	}
	rememberSecretName(cfg, cur, false)
	undo = func() {
		if prev != "" {
			_, _ = providers.StoreKey(cfg, cur, prev, false)
		} else {
			_ = dropSecret(cfg, cur)
		}
	}
	finish = func() {
		// The waiting copy goes, unless someone kept another one meanwhile.
		if n, _, _ := SSONextSecret(cfg, st); n == use.Secret {
			_ = dropSecret(cfg, secretName(st, true))
		}
	}
	return undo, finish, nil
}

// errSetupChanged: the setup or its secret changed after it was read, so a
// switch (or a change) based on it does nothing.
func errSetupChanged() error {
	return apperr.New(apperr.Conflict, "The sign-in setup changed meanwhile (someone set it up again, gave it a new secret or forgot it), so nothing changed.").
		WithHint("Look at the setup again, and test it before switching.").WithDetails(map[string]any{"reason": "setup_changed"})
}

// newRev is a setup's new revision.
func newRev() string { return sso.Random(9) }

// setupIs matches a stored setup exactly: its app and revision.
func setupIs(path string, st *models.SSOSettings) bson.M {
	return bson.M{path + ".provider": st.Provider, path + ".issuer": st.Issuer, path + ".clientId": st.ClientID, path + ".setAt": st.SetAt,
		path + ".tenant": orMissing(st.Tenant), path + ".rev": orMissing(st.Rev)}
}

// orMissing matches a stored string, or a field left out when it is empty.
func orMissing(v string) any {
	if v == "" {
		return bson.M{"$in": bson.A{nil, ""}}
	}
	return v
}

// switchFilter matches the workspace only while the setup tested is exactly
// the one waiting (with none waiting: the one last used) and the method is
// as read, so a change by anyone in between makes a switch change nothing.
func switchFilter(a *app.App, cur models.AuthSettings) bson.M {
	f := bson.M{"_id": a.WorkspaceID()}
	if cur.SSONext != nil {
		for k, v := range setupIs("auth.ssoNext", cur.SSONext) {
			f[k] = v
		}
	} else {
		f["auth.ssoNext"] = bson.M{"$exists": false}
		for k, v := range setupIs("auth.sso", cur.SSO) {
			f[k] = v
		}
	}
	if cur.Method == models.AuthSingle {
		f["auth.method"] = bson.M{"$in": bson.A{nil, "", models.AuthSingle}}
	} else {
		f["auth.method"] = cur.Method
	}
	return f
}

// switchHook runs (in tests) at a stage of the switch — "promote" before the
// tested secret is kept, "write" before the switch is written, "link" before
// the owner's account is linked — for another owner to act in between.
var switchHook func(stage string)

func atSwitchStage(stage string) {
	if switchHook != nil {
		switchHook(stage)
	}
}

// pruneSecrets forgets every app's secret on this computer but the ones of
// keep's setups (the one in use, and the one used before it, for switching
// back): after a switch, earlier apps' secrets don't linger.
func pruneSecrets(cfg *config.Config, keep ...*models.SSOSettings) {
	want := map[string]bool{}
	for _, st := range keep {
		if st != nil {
			want[secretName(st, false)] = true
		}
	}
	for _, n := range secretNames(cfg) {
		if !want[n] {
			_ = dropSecret(cfg, n)
		}
	}
}

// dropSecret forgets one of an app's secrets.
func dropSecret(cfg *config.Config, name string) error {
	if _, err := providers.RemoveKey(cfg, name); err != nil {
		return apperr.Wrap(apperr.Conflict, err, "MetaRush couldn't remove the sign-in app's secret on this computer.")
	}
	rememberSecretName(cfg, name, true)
	return nil
}

// SSOUse is a setup with the secret a sign-in uses with it.
type SSOUse struct {
	Setup  *models.SSOSettings
	Secret string
	// Next: the setup waiting for its test (signed in with its next secret).
	Next bool
	// FromNext: Secret is the app's next secret (waiting for its test), not
	// the one in use.
	FromNext bool
}

// Client is the provider's client for it.
func (u *SSOUse) Client() *sso.Client {
	return &sso.Client{Kind: u.Setup.Provider, Issuer: u.Setup.Issuer, ClientID: u.Setup.ClientID, Secret: u.Secret, Directory: Directory(u.Setup)}
}

// Fingerprint names it as sign-ins use it on this computer: the app and the
// secret (a test of one says nothing about another, D430).
func (u *SSOUse) Fingerprint() string { return fingerprint(u.Setup, u.Secret) }

func fingerprint(st *models.SSOSettings, secret string) string {
	if st == nil {
		return ""
	}
	s := sha256.Sum256([]byte(secret))
	sum := sha256.Sum256([]byte(strings.Join([]string{st.Provider, st.Issuer, st.Tenant, st.ClientID, hex.EncodeToString(s[:])}, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// LiveUse is the setup people sign in with, and its secret in use.
func LiveUse(cfg *config.Config, st *models.SSOSettings) *SSOUse {
	if st == nil {
		return nil
	}
	s, _, _ := SSOSecret(cfg, st)
	return &SSOUse{Setup: st, Secret: s}
}

// TestUse is what a test signs in with: the setup waiting (its next secret,
// else the one in use), else the one in use or last used.
func TestUse(cfg *config.Config, cur models.AuthSettings) *SSOUse {
	st := TestedSetup(cur)
	if st == nil {
		return nil
	}
	u := &SSOUse{Setup: st, Next: cur.SSONext != nil}
	if n, _, _ := SSONextSecret(cfg, st); n != "" {
		u.Secret, u.FromNext = n, true
	} else {
		u.Secret, _, _ = SSOSecret(cfg, st)
	}
	return u
}

// Directory is the Microsoft directory a setup is for: its tenant when that
// is a directory ID, else the directory people join from ("" for any).
func Directory(st *models.SSOSettings) string {
	if st == nil || st.Provider != sso.Microsoft {
		return ""
	}
	if sso.IsDirectoryID(st.Tenant) {
		return strings.ToLower(st.Tenant)
	}
	if sso.IsDirectoryID(st.Domain) {
		return strings.ToLower(st.Domain)
	}
	return ""
}

// SSOLocalOrigin is where this computer's own browser signs in: 127.0.0.1,
// which the dashboard holds (D451; localhost may reach whatever listens on
// [::1] first). Google and other providers take it as it is; Microsoft
// through the app's manifest.
func SSOLocalOrigin(cfg *config.Config) string {
	return fmt.Sprintf("http://127.0.0.1:%d", cfg.Dashboard.Port)
}

// SSOLocalHost is the Host this computer's browser signs in at.
func SSOLocalHost(cfg *config.Config) string { return fmt.Sprintf("127.0.0.1:%d", cfg.Dashboard.Port) }

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
		case st.Tenant == "common" || st.Tenant == "consumers" || st.Tenant == sso.MicrosoftPersonal:
			// Personal Microsoft accounts: their email is an outside address
			// Microsoft once checked (D452).
			return nil, apperr.New(apperr.InvalidInput, "Personal Microsoft accounts can't sign in here: give your organization's directory (tenant) ID, or organizations for any organization's work or school accounts.").
				WithDetails(map[string]any{"reason": "tenant"})
		case sso.IsDirectoryID(st.Tenant), st.Tenant == "organizations", domainRe.MatchString(st.Tenant):
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
// the setup in use apply at once; a new provider, app or secret waits for its
// test sign-in (D430, D450). It says whether the change applied.
func SaveSSO(ctx context.Context, a *app.App, in models.SSOSettings, newSecret bool) (applied bool, err error) {
	st, err := CleanSSO(in)
	if err != nil {
		return false, err
	}
	st.SetAt, st.SetBy, st.Rev = time.Now().UTC(), a.Actor, newRev()
	cur := Settings(ctx, a)
	set := bson.M{}
	if sameSignIn(cur.SSO, st) {
		// The app in use: who may join, its name and the other sign-ins apply
		// at once; a new secret waits for its test (D450).
		set["auth.sso"] = st
		applied = !newSecret
		switch {
		case newSecret:
			set["auth.ssoNext"] = st
		case sameSignIn(cur.SSONext, st):
			// A new secret for the same app waits: it keeps the change too.
			next := *cur.SSONext
			next.Domain, next.Label, next.AlsoPassword, next.AlsoTailscale = st.Domain, st.Label, st.AlsoPassword, st.AlsoTailscale
			next.Rev = newRev()
			set["auth.ssoNext"] = next
		}
	} else {
		set["auth.ssoNext"] = st
	}
	update := bson.M{"$set": set, "$setOnInsert": bson.M{"workspaceId": a.WorkspaceID(), "name": a.Cfg.Workspace.Name, "createdAt": time.Now().UTC()}}
	if _, err := a.DB.C(store.CWorkspaces).UpdateOne(ctx, bson.M{"_id": a.WorkspaceID()}, update, options.UpdateOne().SetUpsert(true)); err != nil {
		return false, err
	}
	forgetSettings(a)
	if !applied && cur.SSONext != nil && !sameSignIn(cur.SSONext, st) {
		// Another app waited before this one: its new secret goes with it.
		_ = dropSecret(a.Cfg, secretName(cur.SSONext, true))
	}
	a.Audit(ctx, "auth.sso.set", "workspace", a.WorkspaceID(), "", map[string]any{"provider": st.Provider, "applied": applied, "joinDomain": st.Domain != "",
		"alsoPassword": st.AlsoPassword, "alsoTailscale": st.AlsoTailscale, "newSecret": newSecret})
	return applied, nil
}

// DiscardSSO forgets the setup waiting for its test, and its new secret; the
// sign-in in use is untouched (D450).
func DiscardSSO(ctx context.Context, a *app.App) error {
	forgetSettings(a) // the setup as it is now
	cur := Settings(ctx, a)
	if cur.SSONext == nil {
		return apperr.New(apperr.Conflict, "No new setup is waiting.").WithDetails(map[string]any{"reason": "nothing_waiting"})
	}
	// Only the setup read here: one set up meanwhile isn't forgotten unseen.
	f := setupIs("auth.ssoNext", cur.SSONext)
	f["_id"] = a.WorkspaceID()
	res, err := a.DB.C(store.CWorkspaces).UpdateOne(ctx, f, bson.M{"$unset": bson.M{"auth.ssoNext": ""}})
	if err != nil {
		return err
	}
	forgetSettings(a)
	if res.MatchedCount == 0 {
		return errSetupChanged()
	}
	if err := dropSecret(a.Cfg, secretName(cur.SSONext, true)); err != nil {
		return err
	}
	sso.FlowsIn(a.Cfg.StateDir()).ClearTest()
	a.Audit(ctx, "auth.sso.discard", "workspace", a.WorkspaceID(), "", map[string]any{"provider": cur.SSONext.Provider})
	return nil
}

// TestedSetup is the setup a test sign-in tries: the one waiting, else the
// one in use (or last used).
func TestedSetup(st models.AuthSettings) *models.SSOSettings {
	if st.SSONext != nil {
		return st.SSONext
	}
	return st.SSO
}

// SetupFor finds the setup (and the secret) a sign-in began with by its
// fingerprint: the one in use, or the one being tested (the setup may have
// changed while the person was at the provider: nil then).
func SetupFor(ctx context.Context, a *app.App, fingerprint string) *SSOUse {
	st := Settings(ctx, a)
	for _, u := range []*SSOUse{LiveUse(a.Cfg, st.SSO), TestUse(a.Cfg, st)} {
		if u != nil && u.Fingerprint() == fingerprint {
			return u
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
	SSOGuest           = "guest"            // a Microsoft guest or personal account (D452)
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
	l := models.SSOLink{Key: id.Key, Provider: id.Provider, Issuer: id.Issuer, Scope: id.Scope, Subject: id.Subject, LinkedAt: time.Now().UTC()}
	if id.EmailTrusted {
		l.Email = id.Email
	}
	return l
}

// otherLink reports a person already signing in with another account of the
// same issuer (Google, one Microsoft directory, one provider's address): a
// different account there showing the same email (a recreated one, a
// look-alike) doesn't take theirs over (D429). An account of another issuer
// doesn't stand in the way: after an owner moves to another provider, address
// or directory, a person's next sign-in links their new account by the email
// the new one vouches for (D453).
func otherLink(u *models.User, id *sso.Identity) bool {
	for _, l := range u.SSO {
		if l.Key != id.Key && linkScope(l) == id.Scope {
			return true
		}
	}
	return false
}

// linkScope is the issuer a link belongs to (links made before scopes were
// kept say it through their provider and issuer).
func linkScope(l models.SSOLink) string {
	if l.Scope != "" {
		return l.Scope
	}
	switch l.Provider {
	case sso.Google:
		return "google"
	case sso.Microsoft:
		if i := strings.Index(l.Issuer, ".com/"); i > 0 {
			return "microsoft:" + strings.ToLower(strings.TrimSuffix(l.Issuer[i+5:], "/v2.0"))
		}
	}
	return "oidc:" + l.Issuer
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
	if id.Guest {
		return nil, false, ssoRefusal(SSOGuest, "Guests and personal Microsoft accounts can't sign in with this sign-in.",
			"Sign in with an account of the organization itself, or ask an owner to let you sign in with a password.", id)
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
	if id.Guest {
		// Microsoft took a guest's or a personal account's email on someone's
		// word: not even an invite stands on it (D452).
		return nil, false, ssoRefusal(SSOGuest, "Guests and personal Microsoft accounts can't join with this sign-in, even with an invite.",
			"Sign in with an account of the organization itself, or ask an owner to let you sign in with a password.", id)
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
	forgetSettings(a) // the setup as it is now, not as cached
	cur := Settings(ctx, a)
	use := TestUse(a.Cfg, cur)
	if use == nil {
		return nil, nil, apperr.New(apperr.Conflict, "Set up the sign-in with your organization's account first.").
			WithDetails(map[string]any{"reason": "no_setup"}).WithCommand("metarush auth sso set")
	}
	setup := use.Setup
	switch {
	case test == nil || test.Setup != use.Fingerprint():
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
	// The tested secret becomes the one in use only now — exactly the secret
	// tested, which must still be waiting (D450); the earlier one served every
	// sign-in until this moment and comes back if the switch isn't kept.
	atSwitchStage("promote")
	undo, finish, err := promoteSecret(a.Cfg, use)
	if err != nil {
		return nil, nil, err
	}
	// The switch is written only while the setup tested is still the one
	// waiting, exactly (its revision): set up again, given a new secret or
	// forgotten by anyone meanwhile, nothing changes.
	atSwitchStage("write")
	now := time.Now().UTC()
	live := *setup
	live.SetAt, live.SetBy, live.Rev = now, a.Actor, newRev()
	res, err := a.DB.C(store.CWorkspaces).UpdateOne(ctx, switchFilter(a, cur), bson.M{
		"$set":   bson.M{"auth.method": models.AuthSSO, "auth.setAt": now, "auth.setBy": a.Actor, "auth.sso": live},
		"$unset": bson.M{"auth.ssoNext": ""}})
	if err == nil && res.MatchedCount == 0 {
		err = errSetupChanged()
	}
	if err != nil {
		undo()
		forgetSettings(a)
		return nil, nil, err
	}
	forgetSettings(a)
	// The owner keeps getting in with the account they tested with (linked
	// only once the switch is kept, so a switch that isn't leaves their
	// accounts as they were).
	atSwitchStage("link")
	if err := linkOwner(ctx, a, owner, test.Identity); err != nil {
		putBackSwitch(ctx, a, cur, live.Rev)
		_, _ = a.DB.C(store.CUsers).UpdateOne(ctx, bson.M{"_id": ownerID}, bson.M{"$set": bson.M{"sso": owner.SSO}})
		forget(ownerID)
		undo()
		return nil, nil, err
	}
	finish()
	// This app's secret and the one used before it stay (switching back needs
	// no secret the provider showed only once); older ones go.
	pruneSecrets(a.Cfg, &live, cur.SSO)
	forget(ownerID)
	a.Audit(ctx, "auth.method", "workspace", a.WorkspaceID(), "", map[string]any{"method": models.AuthSSO, "provider": live.Provider})
	u, err := GetUser(ctx, a, ownerID)
	return u, &live, err
}

// linkOwner links the account an owner tested with to them: one account per
// issuer, so an earlier link of theirs to the same issuer gives way.
func linkOwner(ctx context.Context, a *app.App, owner *models.User, id *sso.Identity) error {
	set := bson.M{"role": models.RoleOwner, "state": models.UserActive, "updatedAt": time.Now().UTC(), "updatedBy": a.Actor}
	if owner.EmailKey == "" && id.EmailTrusted {
		if v, _ := byEmailKey(ctx, a, id.Email); v == nil {
			set["email"], set["emailKey"] = id.Email, EmailKey(id.Email)
		}
	}
	keep := []models.SSOLink{}
	for _, l := range owner.SSO {
		if l.Key == id.Key || linkScope(l) != id.Scope {
			keep = append(keep, l)
		}
	}
	if len(keep) != len(owner.SSO) {
		if _, err := a.DB.C(store.CUsers).UpdateOne(ctx, bson.M{"_id": owner.ID}, bson.M{"$set": bson.M{"sso": keep}}); err != nil {
			return err
		}
	}
	return link(ctx, a, owner.ID, id, set)
}

// putBackSwitch undoes a switch that was written but couldn't link the
// owner, while the setup in use is still the one it wrote.
func putBackSwitch(ctx context.Context, a *app.App, before models.AuthSettings, rev string) {
	set := bson.M{"auth.method": before.Method, "auth.setAt": before.SetAt, "auth.setBy": before.SetBy}
	unset := bson.M{}
	if before.SSO != nil {
		set["auth.sso"] = before.SSO
	} else {
		unset["auth.sso"] = ""
	}
	if before.SSONext != nil {
		set["auth.ssoNext"] = before.SSONext
	} else {
		unset["auth.ssoNext"] = ""
	}
	update := bson.M{"$set": set}
	if len(unset) > 0 {
		update["$unset"] = unset
	}
	_, _ = a.DB.C(store.CWorkspaces).UpdateOne(ctx, bson.M{"_id": a.WorkspaceID(), "auth.sso.rev": rev}, update)
	forgetSettings(a)
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
