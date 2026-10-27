package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// lookupWait is how long a sign-in check waits for the database before it
// uses what it last read (D180: browsers keep playing through an outage).
const lookupWait = 750 * time.Millisecond

// Link lifetimes and throttling (docs/accounts.md).
const (
	InviteTTL     = 7 * 24 * time.Hour
	ResetTTL      = time.Hour
	SessionMax    = 30 * 24 * time.Hour
	SessionIdle   = 7 * 24 * time.Hour
	slowAfter     = 5  // wrong passwords before each try waits
	lockAfter     = 10 // wrong passwords before the account locks
	lockFor       = 15 * time.Minute
	failureMemory = 24 * time.Hour // a count older than this starts again
)

// Link kinds.
const (
	LinkInvite = "invite"
	LinkReset  = "reset"
)

// Errors a sign-in can end with. Their messages are plain; a wrong email and
// a wrong password are the same error.
var (
	ErrBadCredentials = apperr.New(apperr.Unauthorized, "That email and password don't match.").WithDetails(map[string]any{"reason": "bad_credentials"})
	ErrDisabled       = apperr.New(apperr.Unauthorized, "This account is turned off.").WithHint("Ask the owner of this MetaRush to turn it back on.").WithDetails(map[string]any{"reason": "disabled"})
	ErrLinkGone       = apperr.New(apperr.NotFound, "This link doesn't work any more: it was used, it expired or it was taken back.").
				WithHint("Ask the owner of this MetaRush for a new one.").WithDetails(map[string]any{"reason": "link_gone"})
)

// tooSoon says how long to wait before the next try.
func tooSoon(wait time.Duration, locked bool) error {
	secs := int(math.Ceil(wait.Seconds()))
	msg := fmt.Sprintf("Too many wrong passwords. Try again in %d seconds.", secs)
	if secs > 90 {
		msg = fmt.Sprintf("Too many wrong passwords. Try again in %d minutes.", int(math.Ceil(wait.Minutes())))
	}
	return apperr.New(apperr.Unauthorized, "%s", msg).WithDetails(map[string]any{"reason": "throttled", "retryAfterSeconds": secs, "locked": locked})
}

/* ---------- The sign-in method ---------- */

var methodCache sync.Map // workspace ID → cachedMethod

type cachedMethod struct {
	method string
	at     time.Time
}

// Method is the workspace's sign-in method: single (the default), password or
// tailscale. It is read at most every few seconds per workspace.
func Method(ctx context.Context, a *app.App) string {
	if v, ok := methodCache.Load(a.WorkspaceID()); ok {
		if c := v.(cachedMethod); time.Since(c.at) < 5*time.Second {
			return c.method
		}
	}
	var ws models.Workspace
	m := models.AuthSingle
	qctx, cancel := context.WithTimeout(ctx, lookupWait)
	defer cancel()
	if err := a.DB.GetByIDAnyMember(qctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), &ws); err == nil && ws.Auth != nil && ws.Auth.Method != "" {
		m = ws.Auth.Method
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		if v, ok := methodCache.Load(a.WorkspaceID()); ok {
			c := v.(cachedMethod)
			methodCache.Store(a.WorkspaceID(), cachedMethod{method: c.method, at: time.Now()}) // ask again in a few seconds, not on every request
			return c.method                                                                    // the database didn't answer: the last known method
		}
	}
	methodCache.Store(a.WorkspaceID(), cachedMethod{method: m, at: time.Now()})
	return m
}

// SetMethod sets the sign-in method alone (tests and repairs; people use the
// SetUp… steps, which also set up the owner's own sign-in).
func SetMethod(ctx context.Context, a *app.App, method string) error {
	return setMethod(ctx, a, method)
}

func setMethod(ctx context.Context, a *app.App, method string) error {
	_, err := a.DB.C(store.CWorkspaces).UpdateOne(ctx, bson.M{"_id": a.WorkspaceID()}, bson.M{
		"$set":         bson.M{"auth": models.AuthSettings{Method: method, SetAt: time.Now().UTC(), SetBy: a.Actor}},
		"$setOnInsert": bson.M{"workspaceId": a.WorkspaceID(), "name": a.Cfg.Workspace.Name, "createdAt": time.Now().UTC()}}, options.UpdateOne().SetUpsert(true))
	methodCache.Delete(a.WorkspaceID())
	return err
}

/* ---------- People ---------- */

// GetUser reads one person of the workspace.
func GetUser(ctx context.Context, a *app.App, id string) (*models.User, error) {
	var u models.User
	if err := a.DB.GetByID(ctx, store.CUsers, a.WorkspaceID(), id, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// FindByEmail finds the person with an email (nil when nobody has it). People
// from before accounts are found by email too.
func FindByEmail(ctx context.Context, a *app.App, email string) (*models.User, error) {
	key := EmailKey(email)
	if key == "" {
		return nil, nil
	}
	var us []models.User
	if err := a.DB.Find(ctx, store.CUsers, a.WorkspaceID(), bson.M{"$or": bson.A{bson.M{"emailKey": key}, bson.M{"email": bson.M{"$regex": "^" + regexQuote(key) + "$", "$options": "i"}}, bson.M{"logins": key}}}, &us); err != nil {
		return nil, err
	}
	for i := range us {
		if us[i].EmailKey == key {
			return &us[i], nil
		}
	}
	if len(us) > 0 {
		return &us[0], nil
	}
	return nil, nil
}

func regexQuote(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\.+*?()|[]{}^$`, r) {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// IsOwner reports whether a person is an owner. Until anyone has the owner
// role (a fresh install, "just me"), the person this installation was set up
// for is the owner.
func IsOwner(ctx context.Context, a *app.App, userID string) bool {
	if userID == "" {
		return false
	}
	u, err := GetUser(ctx, a, userID)
	if err == nil && u.Role == models.RoleOwner && u.State != models.UserDisabled {
		return true
	}
	if userID != a.Cfg.User.ID {
		return false
	}
	n, err := a.DB.C(store.CUsers).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "role": models.RoleOwner, "state": bson.M{"$ne": models.UserDisabled}})
	return err == nil && n == 0
}

// SignedInOwner reports whether a signed-in person may change owner-only
// settings (D191). With "just me" sign-in only the owner's own one-time links
// sign in, so the person is the owner; otherwise their account decides.
func SignedInOwner(ctx context.Context, a *app.App, userID string) bool {
	if Method(ctx, a) == models.AuthSingle {
		return true
	}
	return IsOwner(ctx, a, userID)
}

// OwnerNames are the names of the people who may change owner-only settings
// ("Owners: Theo Hartley."), by name; on a fresh install, the person it was set
// up for.
func OwnerNames(ctx context.Context, a *app.App) []string {
	var us []models.User
	_ = a.DB.Find(ctx, store.CUsers, a.WorkspaceID(), bson.M{"role": models.RoleOwner,
		"state": bson.M{"$nin": bson.A{models.UserDisabled, models.UserInvited, models.UserEarlier}}}, &us)
	names := []string{}
	for _, u := range us {
		if n := firstNonEmpty(u.Name, u.Email); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		n := a.Cfg.User.Name
		if u, err := GetUser(ctx, a, a.Cfg.User.ID); err == nil {
			n = firstNonEmpty(u.Name, u.Email, n)
		}
		if n != "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

func init() { ops.OwnerNames = OwnerNames }

// SessionValid reports whether a session made at created is still good for a
// person: the account is active (or the owner on a fresh install) and nobody
// signed them out everywhere since.
func SessionValid(ctx context.Context, a *app.App, userID string, created time.Time) bool {
	u, ok := userForSession(ctx, a, userID)
	if !ok {
		// The person this installation was set up for, on this computer: kept
		// signed in without a record of their own (an older install), and
		// through a database outage (D180) — they are at the computer.
		return userID != "" && userID == a.Cfg.User.ID
	}
	if u.State == models.UserDisabled || u.State == models.UserInvited {
		return false
	}
	return u.SessionsValidAfter == nil || created.After(*u.SessionsValidAfter)
}

// Why a person's paired browser stops sending (D197).
const (
	SenderOff       = "account_off"     // their account is turned off
	SenderRemoved   = "account_removed" // their account was removed
	SenderNotJoined = "account_invited" // they haven't accepted their invite
	SenderJustOwner = "just_owner"      // sign-in is "Just me on this computer"
	SenderNoAccount = "no_account"      // nobody has that account
)

// MaySend says whether a browser a person paired may still send things in
// (D197), and their name now: their account is on — not turned off, removed
// or still an invite — and with "Just me on this computer" sign-in only the
// person this installation was set up for counts. It is read like a session
// check (the last answer for a few seconds, the last ten minutes' when the
// database doesn't answer), so it is cheap on every request. A pairing from
// before pairings had a person ("") is the owner's.
func MaySend(ctx context.Context, a *app.App, userID string) (name, reason string, ok bool) {
	if userID == "" || userID == a.Cfg.User.ID && Method(ctx, a) == models.AuthSingle {
		if u, found := userForSession(ctx, a, a.Cfg.User.ID); found {
			return firstNonEmpty(u.Name, u.Email, a.Cfg.User.Name), "", true
		}
		return a.Cfg.User.Name, "", true
	}
	u, found := userForSession(ctx, a, userID)
	if !found {
		if userID == a.Cfg.User.ID { // an older install without the owner's record
			return a.Cfg.User.Name, "", true
		}
		return "", SenderNoAccount, false
	}
	name = firstNonEmpty(u.Name, u.Email)
	switch u.State {
	case models.UserDisabled:
		return name, SenderOff, false
	case models.UserEarlier:
		return name, SenderRemoved, false
	case models.UserInvited:
		return name, SenderNotJoined, false
	}
	if userID != a.Cfg.User.ID && Method(ctx, a) == models.AuthSingle {
		return name, SenderJustOwner, false
	}
	return name, "", true
}

// SenderWords says in plain words why a person's browser can't send.
func SenderWords(reason, name string) string {
	who := "the account that paired this browser"
	if name != "" {
		who = name + "'s account"
	}
	switch reason {
	case SenderOff:
		return "This browser was paired by " + who + ", which is turned off in MetaRush."
	case SenderRemoved:
		return "This browser was paired by " + who + ", which was removed from MetaRush."
	case SenderNotJoined:
		return "This browser was paired by " + who + ", which hasn't joined MetaRush yet."
	case SenderJustOwner:
		return "This browser was paired by " + who + ", and MetaRush lets in only its owner now."
	}
	return "This browser was paired by an account MetaRush doesn't have any more."
}

// ensureOwnerRecord makes the installation's person a record when they have
// none yet (the owner sets up sign-in on an install that never made one).
func ensureOwnerRecord(ctx context.Context, a *app.App) error {
	u := a.Cfg.User
	if u.ID == "" {
		return apperr.New(apperr.NotConfigured, "MetaRush on this computer has no owner set up yet.").WithCommand("metarush init")
	}
	_, err := a.DB.C(store.CUsers).UpdateOne(ctx, bson.M{"_id": u.ID}, bson.M{"$setOnInsert": models.User{ID: u.ID, WorkspaceID: a.WorkspaceID(), Name: u.Name, Email: u.Email, CreatedAt: time.Now().UTC()}},
		options.UpdateOne().SetUpsert(true))
	return err
}

// signOutEverywhere ends every session a person has, on every computer.
func signOutEverywhere(ctx context.Context, a *app.App, userID string) error {
	defer forget(userID)
	now := time.Now().UTC()
	return a.DB.Update(ctx, store.CUsers, a.WorkspaceID(), userID, bson.M{"$set": bson.M{"sessionsValidAfter": now}})
}

// SignOutEverywhere ends every session a person has (the current one too).
func SignOutEverywhere(ctx context.Context, a *app.App, userID string) error {
	err := signOutEverywhere(ctx, a, userID)
	a.Audit(ctx, "account.signoutEverywhere", "user", userID, "", nil)
	return err
}

/* ---------- Passwords ---------- */

type credential struct {
	ID          string    `bson:"_id"` // the user's ID
	WorkspaceID string    `bson:"workspaceId"`
	Hash        string    `bson:"hash"`
	UpdatedAt   time.Time `bson:"updatedAt"`
}

// HasPassword reports whether a person has a password.
func HasPassword(ctx context.Context, a *app.App, userID string) bool {
	n, err := a.DB.C(store.CUserCredentials).CountDocuments(ctx, bson.M{"_id": userID, "workspaceId": a.WorkspaceID()})
	return err == nil && n > 0
}

// setPassword stores a person's new password (checked by the caller); with
// signOut every session made before it ends, on every computer.
func setPassword(ctx context.Context, a *app.App, userID, pw string, signOut bool) error {
	defer forget(userID)
	h, err := HashPassword(pw)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if _, err := a.DB.C(store.CUserCredentials).ReplaceOne(ctx, bson.M{"_id": userID}, credential{ID: userID, WorkspaceID: a.WorkspaceID(), Hash: h, UpdatedAt: now},
		options.Replace().SetUpsert(true)); err != nil {
		return err
	}
	set := bson.M{"passwordSetAt": now}
	if signOut {
		set["sessionsValidAfter"] = now
	}
	return a.DB.Update(ctx, store.CUsers, a.WorkspaceID(), userID, bson.M{"$set": set})
}

// verifyCurrent checks a person's current password (changing an email, a
// password or the sign-in method asks for it again).
func verifyCurrent(ctx context.Context, a *app.App, userID, pw string) bool {
	var c credential
	if err := a.DB.C(store.CUserCredentials).FindOne(ctx, bson.M{"_id": userID, "workspaceId": a.WorkspaceID()}).Decode(&c); err != nil {
		burn(pw)
		return false
	}
	ok, _ := VerifyPassword(pw, c.Hash)
	return ok
}

/* ---------- Signing in ---------- */

type failures struct {
	ID          string     `bson:"_id"`
	WorkspaceID string     `bson:"workspaceId"`
	EmailKey    string     `bson:"emailKey"`
	Count       int        `bson:"count"`
	LastAt      time.Time  `bson:"lastAt"`
	LockedUntil *time.Time `bson:"lockedUntil,omitempty"`
}

func failureID(a *app.App, key string) string { return a.WorkspaceID() + ":" + key }

// throttled says whether an email must wait before its next try.
func throttled(ctx context.Context, a *app.App, key string, now time.Time) error {
	var f failures
	if err := a.DB.C(store.CAuthFailures).FindOne(ctx, bson.M{"_id": failureID(a, key)}).Decode(&f); err != nil {
		return nil
	}
	if now.Sub(f.LastAt) > failureMemory {
		return nil
	}
	if f.LockedUntil != nil && now.Before(*f.LockedUntil) {
		return tooSoon(f.LockedUntil.Sub(now), true)
	}
	if f.Count >= slowAfter {
		wait := time.Duration(1<<min(f.Count-slowAfter+1, 10)) * time.Second
		wait = min(wait, lockFor)
		if next := f.LastAt.Add(wait); now.Before(next) {
			return tooSoon(next.Sub(now), false)
		}
	}
	return nil
}

// failed counts a wrong password for an email (known or not, so a lock says
// nothing about whether it has an account); the tenth locks it and tells the owners.
func failed(ctx context.Context, a *app.App, key string, now time.Time) {
	var f failures
	_ = a.DB.C(store.CAuthFailures).FindOne(ctx, bson.M{"_id": failureID(a, key)}).Decode(&f)
	if now.Sub(f.LastAt) > failureMemory {
		f.Count = 0
	}
	f.ID, f.WorkspaceID, f.EmailKey, f.LastAt = failureID(a, key), a.WorkspaceID(), key, now
	f.Count++
	if f.Count >= lockAfter {
		until := now.Add(lockFor)
		f.LockedUntil = &until
	}
	_, _ = a.DB.C(store.CAuthFailures).ReplaceOne(ctx, bson.M{"_id": f.ID}, f, options.Replace().SetUpsert(true))
	if f.Count == lockAfter {
		a.Audit(ctx, "auth.locked", "user", "", "", map[string]any{"email": key, "minutes": int(lockFor.Minutes())})
		tellOwners(ctx, a, "signin_locked:"+key+":"+now.Format("2006-01-02T15"), "warning", "Sign-in locked for "+key,
			fmt.Sprintf("Someone typed a wrong password for %s ten times, so signing in with that email is locked for %d minutes.", key, int(lockFor.Minutes())))
	}
}

// tellOwners notifies every owner.
func tellOwners(ctx context.Context, a *app.App, group, tone, title, body string) {
	var owners []models.User
	_ = a.DB.Find(ctx, store.CUsers, a.WorkspaceID(), bson.M{"role": models.RoleOwner}, &owners)
	if len(owners) == 0 && a.Cfg.User.ID != "" {
		owners = append(owners, models.User{ID: a.Cfg.User.ID})
	}
	for _, o := range owners {
		_, _ = notify.Upsert(ctx, a, models.Notification{ID: ids.New("ntf"), WorkspaceID: a.WorkspaceID(), UserID: o.ID, Kind: "signin_locked",
			GroupKey: group + ":" + o.ID, Tone: tone, Title: title, Body: body, Link: "/settings/people#users", CreatedAt: time.Now().UTC()}, notify.Once)
	}
}

// Authenticate signs a person in with their email and password. A wrong
// email and a wrong password answer the same and take the same time.
func Authenticate(ctx context.Context, a *app.App, email, pw string) (*models.User, error) {
	key := EmailKey(email)
	now := time.Now().UTC()
	if key == "" || pw == "" {
		burn(pw)
		return nil, ErrBadCredentials
	}
	if err := throttled(ctx, a, key, now); err != nil {
		return nil, err
	}
	u, _ := FindByEmail(ctx, a, key)
	var c credential
	if u == nil || a.DB.C(store.CUserCredentials).FindOne(ctx, bson.M{"_id": u.ID, "workspaceId": a.WorkspaceID()}).Decode(&c) != nil {
		burn(pw)
		failed(ctx, a, key, now)
		return nil, ErrBadCredentials
	}
	ok, rehash := VerifyPassword(pw, c.Hash)
	if !ok {
		failed(ctx, a, key, now)
		return nil, ErrBadCredentials
	}
	if u.State == models.UserDisabled {
		return nil, ErrDisabled
	}
	_, _ = a.DB.C(store.CAuthFailures).DeleteOne(ctx, bson.M{"_id": failureID(a, key)})
	if rehash {
		if h, err := HashPassword(pw); err == nil {
			_, _ = a.DB.C(store.CUserCredentials).UpdateOne(ctx, bson.M{"_id": u.ID}, bson.M{"$set": bson.M{"hash": h, "updatedAt": now}})
		}
	}
	_ = a.DB.Update(ctx, store.CUsers, a.WorkspaceID(), u.ID, bson.M{"$set": bson.M{"lastSignedInAt": now}})
	return u, nil
}

/* ---------- Invite and reset links ---------- */

// LinkRecord is an invite or a reset link, found by its token's SHA-256.
type LinkRecord struct {
	ID          string       `bson:"_id"` // SHA-256 of the token
	WorkspaceID string       `bson:"workspaceId"`
	Kind        string       `bson:"kind"`
	UserID      string       `bson:"userId"`
	CreatedBy   models.Actor `bson:"createdBy"`
	CreatedAt   time.Time    `bson:"createdAt"`
	ExpiresAt   time.Time    `bson:"expiresAt"`
	UsedAt      *time.Time   `bson:"usedAt,omitempty"`
	RevokedAt   *time.Time   `bson:"revokedAt,omitempty"`
	PurgeAt     time.Time    `bson:"purgeAt"`
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// newLink makes a one-time link for a person (taking back any earlier one of
// the same kind) and returns its token, shown once.
func newLink(ctx context.Context, a *app.App, kind, userID string, ttl time.Duration) (string, time.Time, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	now := time.Now().UTC()
	revokeLinks(ctx, a, userID, kind)
	exp := now.Add(ttl)
	rec := LinkRecord{ID: tokenHash(token), WorkspaceID: a.WorkspaceID(), Kind: kind, UserID: userID, CreatedBy: a.Actor, CreatedAt: now, ExpiresAt: exp, PurgeAt: exp.Add(30 * 24 * time.Hour)}
	if err := a.DB.Insert(ctx, store.CAuthTokens, rec); err != nil {
		return "", time.Time{}, err
	}
	return token, exp, nil
}

// revokeLinks takes back a person's unused links (of one kind, or all when kind is "").
func revokeLinks(ctx context.Context, a *app.App, userID, kind string) {
	f := bson.M{"workspaceId": a.WorkspaceID(), "userId": userID, "usedAt": nil, "revokedAt": nil}
	if kind != "" {
		f["kind"] = kind
	}
	_, _ = a.DB.C(store.CAuthTokens).UpdateMany(ctx, f, bson.M{"$set": bson.M{"revokedAt": time.Now().UTC()}})
}

// LookupLink finds a link that still works, with its person.
func LookupLink(ctx context.Context, a *app.App, token string) (*LinkRecord, *models.User, error) {
	if len(token) < 20 || len(token) > 100 {
		return nil, nil, ErrLinkGone
	}
	var rec LinkRecord
	if err := a.DB.C(store.CAuthTokens).FindOne(ctx, bson.M{"_id": tokenHash(token), "workspaceId": a.WorkspaceID()}).Decode(&rec); err != nil {
		return nil, nil, ErrLinkGone
	}
	if rec.UsedAt != nil || rec.RevokedAt != nil || time.Now().After(rec.ExpiresAt) {
		return nil, nil, ErrLinkGone
	}
	u, err := GetUser(ctx, a, rec.UserID)
	if err != nil {
		return nil, nil, ErrLinkGone
	}
	return &rec, u, nil
}

// UseLink accepts an invite (a name and a password) or a reset (a new
// password). The password is checked before the link is used up; the link
// works once. The person is signed out everywhere else.
func UseLink(ctx context.Context, a *app.App, token, name, pw string) (*models.User, error) {
	rec, u, err := LookupLink(ctx, a, token)
	if err != nil {
		return nil, err
	}
	if rec.Kind == LinkInvite {
		if name = strings.TrimSpace(name); name == "" {
			name = u.Name
		}
		if name == "" || len([]rune(name)) > 80 {
			return nil, apperr.New(apperr.InvalidInput, "Give the name people see for you (up to 80 characters).")
		}
	} else {
		name = u.Name
	}
	if err := CheckPassword(pw, u.Email, name); err != nil {
		return nil, apperr.New(apperr.InvalidInput, "%s", err.Error()).WithDetails(map[string]any{"reason": "weak_password"})
	}
	now := time.Now().UTC()
	res, err := a.DB.C(store.CAuthTokens).UpdateOne(ctx, bson.M{"_id": rec.ID, "usedAt": nil, "revokedAt": nil, "expiresAt": bson.M{"$gt": now}}, bson.M{"$set": bson.M{"usedAt": now}})
	if err != nil {
		return nil, err
	}
	if res.ModifiedCount != 1 {
		return nil, ErrLinkGone // used at the same moment elsewhere
	}
	if err := setPassword(ctx, a, u.ID, pw, true); err != nil {
		return nil, err
	}
	set := bson.M{"state": models.UserActive, "updatedAt": now}
	if rec.Kind == LinkInvite {
		set["name"] = name
	}
	if err := a.DB.Update(ctx, store.CUsers, a.WorkspaceID(), u.ID, bson.M{"$set": set}); err != nil {
		return nil, err
	}
	forget(u.ID)
	actor := models.Actor{UserID: u.ID, UserName: name, InstallationID: a.Actor.InstallationID, Via: a.Actor.Via}
	b := *a
	b.Actor = actor
	b.Audit(ctx, "auth."+rec.Kind+".used", "user", u.ID, "", nil)
	return GetUser(ctx, a, u.ID)
}

/* ---------- Owners' changes ---------- */

// ErrNoMethod refuses inviting before a sign-in method is chosen.
var ErrNoMethod = apperr.New(apperr.Conflict, "Choose how people sign in before you invite anyone.").
	WithHint("In Settings → People, choose Email and password (or Tailscale accounts) and set up your own sign-in.").WithDetails(map[string]any{"reason": "no_method"})

// Invite makes (or reuses) a person and their invite link. An earlier person
// with that email — or the one given in linkTo — becomes the account, so what
// they did before is theirs.
func Invite(ctx context.Context, a *app.App, email, name, role, linkTo string) (*models.User, string, time.Time, error) {
	if Method(ctx, a) != models.AuthPassword {
		return nil, "", time.Time{}, ErrNoMethod
	}
	key := EmailKey(email)
	if !ValidEmail(key) {
		return nil, "", time.Time{}, apperr.New(apperr.InvalidInput, "That doesn't look like an email address.")
	}
	name = strings.TrimSpace(name)
	if len([]rune(name)) > 80 {
		return nil, "", time.Time{}, apperr.New(apperr.InvalidInput, "The name is too long (80 characters at most).")
	}
	if role == "" {
		role = models.RoleMember
	}
	if role != models.RoleMember && role != models.RoleOwner {
		return nil, "", time.Time{}, apperr.New(apperr.InvalidInput, "A person is an owner or a member.")
	}
	u, err := FindByEmail(ctx, a, key)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	if u == nil && linkTo != "" {
		if u, err = GetUser(ctx, a, linkTo); err != nil {
			return nil, "", time.Time{}, apperr.New(apperr.NotFound, "MetaRush can't find that earlier person.")
		}
	}
	now := time.Now().UTC()
	switch {
	case u == nil:
		u = &models.User{ID: ids.New("usr"), WorkspaceID: a.WorkspaceID(), Name: name, Email: strings.TrimSpace(email), EmailKey: key, Role: role, State: models.UserInvited,
			Source: "invite", CreatedAt: now, InvitedBy: &a.Actor, UpdatedAt: &now, UpdatedBy: &a.Actor}
		if err := a.DB.Insert(ctx, store.CUsers, *u); err != nil {
			if store.IsDuplicateKey(err) {
				return nil, "", time.Time{}, apperr.New(apperr.Conflict, "Someone already has that email.")
			}
			return nil, "", time.Time{}, err
		}
	case u.State == models.UserActive && HasPassword(ctx, a, u.ID):
		return nil, "", time.Time{}, apperr.New(apperr.Conflict, "%s already has an account.", orEmail(u)).
			WithHint("To let them choose a new password, make a reset link instead.").WithDetails(map[string]any{"reason": "has_account", "userId": u.ID})
	case u.State == models.UserDisabled:
		return nil, "", time.Time{}, apperr.New(apperr.Conflict, "%s's account is turned off.", orEmail(u)).WithHint("Turn it back on instead.").
			WithDetails(map[string]any{"reason": "disabled", "userId": u.ID})
	default:
		// An earlier person (or an invite not yet accepted): the same record.
		set := bson.M{"email": strings.TrimSpace(email), "emailKey": key, "role": role, "state": models.UserInvited, "invitedBy": a.Actor, "updatedAt": now, "updatedBy": a.Actor}
		if name != "" {
			set["name"] = name
		}
		if err := a.DB.Update(ctx, store.CUsers, a.WorkspaceID(), u.ID, bson.M{"$set": set}); err != nil {
			if store.IsDuplicateKey(err) {
				return nil, "", time.Time{}, apperr.New(apperr.Conflict, "Someone else already has that email.")
			}
			return nil, "", time.Time{}, err
		}
	}
	token, exp, err := newLink(ctx, a, LinkInvite, u.ID, InviteTTL)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	a.Audit(ctx, "users.invite", "user", u.ID, "", map[string]any{"email": key, "role": role, "earlier": u.CreatedAt.Before(now.Add(-time.Second))})
	u, err = GetUser(ctx, a, u.ID)
	return u, token, exp, err
}

func orEmail(u *models.User) string {
	if u.Name != "" {
		return u.Name
	}
	return u.Email
}

// ResetLink makes a link a person follows to choose a new password (an
// invite again for someone who hasn't accepted theirs).
func ResetLink(ctx context.Context, a *app.App, userID string) (*models.User, string, time.Time, string, error) {
	if Method(ctx, a) != models.AuthPassword {
		return nil, "", time.Time{}, "", apperr.New(apperr.Conflict, "People sign in without passwords here, so there is nothing to reset.").WithDetails(map[string]any{"reason": "no_passwords"})
	}
	u, err := GetUser(ctx, a, userID)
	if err != nil {
		return nil, "", time.Time{}, "", apperr.New(apperr.NotFound, "MetaRush can't find that person.")
	}
	kind, ttl := LinkReset, ResetTTL
	switch u.State {
	case models.UserInvited:
		kind, ttl = LinkInvite, InviteTTL
	case models.UserDisabled:
		return nil, "", time.Time{}, "", apperr.New(apperr.Conflict, "%s's account is turned off.", orEmail(u)).WithHint("Turn it back on first.")
	case models.UserActive:
	default:
		return nil, "", time.Time{}, "", apperr.New(apperr.Conflict, "%s has no account yet.", orEmail(u)).WithHint("Invite them instead.")
	}
	token, exp, err := newLink(ctx, a, kind, u.ID, ttl)
	if err != nil {
		return nil, "", time.Time{}, "", err
	}
	a.Audit(ctx, "users.resetLink", "user", u.ID, "", map[string]any{"kind": kind})
	return u, token, exp, kind, nil
}

// lastOwner refuses a change that would leave no active owner.
func lastOwner(ctx context.Context, a *app.App, userID string) error {
	n, _ := a.DB.C(store.CUsers).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "role": models.RoleOwner, "state": models.UserActive, "_id": bson.M{"$ne": userID}})
	if n == 0 && IsOwner(ctx, a, userID) {
		return apperr.New(apperr.Conflict, "This is the only owner, and someone has to manage people and sign-in.").
			WithHint("Make someone else an owner first.").WithDetails(map[string]any{"reason": "last_owner"})
	}
	return nil
}

// SetState turns an account off or back on, or removes it (back to an earlier
// person: their history stays theirs, they can't sign in).
func SetState(ctx context.Context, a *app.App, userID, state string) (*models.User, error) {
	defer forget(userID)
	u, err := GetUser(ctx, a, userID)
	if err != nil {
		return nil, apperr.New(apperr.NotFound, "MetaRush can't find that person.")
	}
	if state != models.UserActive {
		if err := lastOwner(ctx, a, userID); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	set := bson.M{"state": state, "updatedAt": now, "updatedBy": a.Actor}
	switch state {
	case models.UserActive:
		if u.State != models.UserDisabled {
			return u, nil
		}
		if !HasPassword(ctx, a, u.ID) && Method(ctx, a) == models.AuthPassword {
			set["state"] = models.UserInvited // never accepted: they need an invite link
		}
	case models.UserDisabled:
		set["sessionsValidAfter"] = now
		revokeLinks(ctx, a, userID, "")
	case models.UserEarlier:
		set["sessionsValidAfter"] = now
		set["role"] = models.RoleMember
		revokeLinks(ctx, a, userID, "")
		_, _ = a.DB.C(store.CUserCredentials).DeleteOne(ctx, bson.M{"_id": userID, "workspaceId": a.WorkspaceID()})
	}
	if err := a.DB.Update(ctx, store.CUsers, a.WorkspaceID(), userID, bson.M{"$set": set}); err != nil {
		return nil, err
	}
	a.Audit(ctx, "users.state", "user", userID, "", map[string]any{"state": set["state"]})
	return GetUser(ctx, a, userID)
}

// SetRole makes a person an owner or a member.
func SetRole(ctx context.Context, a *app.App, userID, role string) (*models.User, error) {
	defer forget(userID)
	if role != models.RoleOwner && role != models.RoleMember {
		return nil, apperr.New(apperr.InvalidInput, "A person is an owner or a member.")
	}
	if role == models.RoleMember {
		if err := lastOwner(ctx, a, userID); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	if err := a.DB.Update(ctx, store.CUsers, a.WorkspaceID(), userID, bson.M{"$set": bson.M{"role": role, "updatedAt": now, "updatedBy": a.Actor}}); err != nil {
		return nil, apperr.New(apperr.NotFound, "MetaRush can't find that person.")
	}
	a.Audit(ctx, "users.role", "user", userID, "", map[string]any{"role": role})
	return GetUser(ctx, a, userID)
}

// ChangePassword changes a person's own password (asking for the current
// one) and signs them out everywhere else.
func ChangePassword(ctx context.Context, a *app.App, userID, current, next string) error {
	u, err := GetUser(ctx, a, userID)
	if err != nil {
		return err
	}
	if HasPassword(ctx, a, userID) && !verifyCurrent(ctx, a, userID, current) {
		return apperr.New(apperr.Unauthorized, "Your current password isn't right.").WithDetails(map[string]any{"reason": "bad_current"})
	}
	if err := CheckPassword(next, u.Email, u.Name); err != nil {
		return apperr.New(apperr.InvalidInput, "%s", err.Error()).WithDetails(map[string]any{"reason": "weak_password"})
	}
	if err := setPassword(ctx, a, userID, next, true); err != nil {
		return err
	}
	a.Audit(ctx, "account.password", "user", userID, "", nil)
	return nil
}

// UpdateAccount changes a person's own name, or email (asking for the current password).
func UpdateAccount(ctx context.Context, a *app.App, userID, name, email, current string) (*models.User, error) {
	u, err := GetUser(ctx, a, userID)
	if err != nil {
		return nil, err
	}
	set := bson.M{"updatedAt": time.Now().UTC(), "updatedBy": a.Actor}
	if n := strings.TrimSpace(name); n != "" && n != u.Name {
		if len([]rune(n)) > 80 {
			return nil, apperr.New(apperr.InvalidInput, "The name is too long (80 characters at most).")
		}
		set["name"] = n
	}
	if e := strings.TrimSpace(email); e != "" && EmailKey(e) != u.EmailKey {
		if !ValidEmail(e) {
			return nil, apperr.New(apperr.InvalidInput, "That doesn't look like an email address.")
		}
		if HasPassword(ctx, a, userID) && !verifyCurrent(ctx, a, userID, current) {
			return nil, apperr.New(apperr.Unauthorized, "Your current password isn't right.").WithDetails(map[string]any{"reason": "bad_current"})
		}
		set["email"], set["emailKey"] = e, EmailKey(e)
	}
	if err := a.DB.Update(ctx, store.CUsers, a.WorkspaceID(), userID, bson.M{"$set": set}); err != nil {
		if store.IsDuplicateKey(err) {
			return nil, apperr.New(apperr.Conflict, "Someone else already has that email.")
		}
		return nil, err
	}
	a.Audit(ctx, "account.update", "user", userID, "", nil)
	return GetUser(ctx, a, userID)
}

// SetUpPassword makes email and password the sign-in method, with the owner's
// own sign-in given at the same step (current asked for when they already
// have a password).
func SetUpPassword(ctx context.Context, a *app.App, ownerID, email, pw, current string) (*models.User, error) {
	if ownerID == a.Cfg.User.ID {
		if err := ensureOwnerRecord(ctx, a); err != nil {
			return nil, err
		}
	}
	u, err := GetUser(ctx, a, ownerID)
	if err != nil {
		return nil, err
	}
	if !ValidEmail(email) {
		return nil, apperr.New(apperr.InvalidInput, "Give your email: it is how you sign in.")
	}
	if HasPassword(ctx, a, ownerID) && !verifyCurrent(ctx, a, ownerID, current) {
		// Changing how people sign in asks for your password again, always.
		return nil, apperr.New(apperr.Unauthorized, "Your current password isn't right.").WithDetails(map[string]any{"reason": "bad_current"})
	}
	if pw != "" || !HasPassword(ctx, a, ownerID) {
		if err := CheckPassword(pw, email, u.Name); err != nil {
			return nil, apperr.New(apperr.InvalidInput, "%s", err.Error()).WithDetails(map[string]any{"reason": "weak_password"})
		}
	}
	now := time.Now().UTC()
	if err := a.DB.Update(ctx, store.CUsers, a.WorkspaceID(), ownerID, bson.M{"$set": bson.M{"email": strings.TrimSpace(email), "emailKey": EmailKey(email), "role": models.RoleOwner,
		"state": models.UserActive, "updatedAt": now, "updatedBy": a.Actor}}); err != nil {
		if store.IsDuplicateKey(err) {
			return nil, apperr.New(apperr.Conflict, "Someone else already has that email.")
		}
		return nil, err
	}
	if pw != "" {
		if err := setPassword(ctx, a, ownerID, pw, false); err != nil { // setting up keeps you signed in
			return nil, err
		}
	}
	if err := setMethod(ctx, a, models.AuthPassword); err != nil {
		return nil, err
	}
	a.Audit(ctx, "auth.method", "workspace", a.WorkspaceID(), "", map[string]any{"method": models.AuthPassword})
	return GetUser(ctx, a, ownerID)
}

// SetUpTailscale makes Tailscale accounts the sign-in method, with the
// owner's own Tailscale login; members are people whose login is linked.
func SetUpTailscale(ctx context.Context, a *app.App, ownerID, login string) error {
	key := EmailKey(login)
	if !ValidEmail(key) {
		return apperr.New(apperr.InvalidInput, "Give your Tailscale login (it looks like an email address).")
	}
	if ownerID == a.Cfg.User.ID {
		if err := ensureOwnerRecord(ctx, a); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	if err := a.DB.Update(ctx, store.CUsers, a.WorkspaceID(), ownerID, bson.M{"$addToSet": bson.M{"logins": key},
		"$set": bson.M{"role": models.RoleOwner, "state": models.UserActive, "updatedAt": now, "updatedBy": a.Actor}}); err != nil {
		return err
	}
	if err := setMethod(ctx, a, models.AuthTailscale); err != nil {
		return err
	}
	a.Audit(ctx, "auth.method", "workspace", a.WorkspaceID(), "", map[string]any{"method": models.AuthTailscale})
	return nil
}

// SetUpSingle goes back to just the owner on their computer: everyone else
// is signed out and their records stay.
func SetUpSingle(ctx context.Context, a *app.App, ownerID string) error {
	defer forgetAll()
	now := time.Now().UTC()
	if _, err := a.DB.C(store.CUsers).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "_id": bson.M{"$ne": ownerID}}, bson.M{"$set": bson.M{"sessionsValidAfter": now}}); err != nil {
		return err
	}
	_, _ = a.DB.C(store.CAuthTokens).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "usedAt": nil, "revokedAt": nil}, bson.M{"$set": bson.M{"revokedAt": now}})
	if err := setMethod(ctx, a, models.AuthSingle); err != nil {
		return err
	}
	a.Audit(ctx, "auth.method", "workspace", a.WorkspaceID(), "", map[string]any{"method": models.AuthSingle})
	return nil
}

// Reset puts sign-in back to a fresh install's (`metarush auth reset`): just
// the owner on their computer, every session on every computer signed out,
// links taken back, people without a sign-in marked earlier (their history
// stays). The owner is the person this installation was set up for.
func Reset(ctx context.Context, a *app.App) (earlier int, err error) {
	owner := a.Cfg.User.ID
	defer forgetAll()
	if err := ensureOwnerRecord(ctx, a); err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	if _, err := a.DB.C(store.CUsers).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID()}, bson.M{"$set": bson.M{"sessionsValidAfter": now}}); err != nil {
		return 0, err
	}
	var us []models.User
	if err := a.DB.Find(ctx, store.CUsers, a.WorkspaceID(), bson.M{}, &us); err != nil {
		return 0, err
	}
	for _, u := range us {
		set := bson.M{}
		switch {
		case u.ID == owner:
			set["role"], set["state"] = models.RoleOwner, models.UserActive
		case !HasPassword(ctx, a, u.ID):
			set["state"] = models.UserEarlier
			if u.EmailKey == "" {
				if k := EmailKey(firstNonEmpty(u.Email, firstLogin(u))); ValidEmail(k) {
					set["emailKey"] = k // found again when their email is invited
				}
			}
			earlier++
		}
		if len(set) > 0 {
			if err := a.DB.Update(ctx, store.CUsers, a.WorkspaceID(), u.ID, bson.M{"$set": set}); err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
				if !store.IsDuplicateKey(err) {
					return earlier, err
				}
			}
		}
	}
	_, _ = a.DB.C(store.CAuthTokens).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "usedAt": nil, "revokedAt": nil}, bson.M{"$set": bson.M{"revokedAt": now}})
	if err := setMethod(ctx, a, models.AuthSingle); err != nil {
		return earlier, err
	}
	a.Audit(ctx, "auth.reset", "workspace", a.WorkspaceID(), "", map[string]any{"earlier": earlier})
	return earlier, nil
}

func firstLogin(u models.User) string {
	if len(u.Logins) > 0 {
		return u.Logins[0]
	}
	return ""
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

// validCache keeps each person's last answer, so a signed-in browser keeps
// playing through a short database outage (D180) instead of being signed out.
var validCache sync.Map // user ID → cachedUser

type cachedUser struct {
	u  models.User
	at time.Time
}

// userForSession reads a person for a session check: the database, or what it
// said in the last ten minutes when it doesn't answer.
func userForSession(ctx context.Context, a *app.App, id string) (*models.User, bool) {
	if v, ok := validCache.Load(id); ok {
		if c := v.(cachedUser); time.Since(c.at) < 5*time.Second {
			if c.u.ID == "" {
				return nil, false // no record (read a moment ago)
			}
			return &c.u, true
		}
	}
	qctx, cancel := context.WithTimeout(ctx, lookupWait)
	defer cancel()
	var got models.User
	err := a.DB.GetByIDAnyMember(qctx, store.CUsers, a.WorkspaceID(), id, &got)
	u := &got
	if err == nil {
		validCache.Store(id, cachedUser{*u, time.Now()})
		return u, true
	} else if errors.Is(err, store.ErrNotFound) {
		validCache.Store(id, cachedUser{at: time.Now()})
		return nil, false
	}
	if v, ok := validCache.Load(id); ok {
		if c := v.(cachedUser); time.Since(c.at) < 10*time.Minute && c.u.ID != "" {
			return &c.u, true
		}
	}
	return nil, false
}

// forget drops what a session check remembers about a person (they changed here).
func forget(id string) { validCache.Delete(id) }

// forgetAll drops what session checks remember about everyone.
func forgetAll() {
	validCache.Range(func(k, _ any) bool { validCache.Delete(k); return true })
}
