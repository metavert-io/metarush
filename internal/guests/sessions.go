package guests

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Guests' sign-ins (D494). A signed-in browser holds a cookie of 256 random
// bits, made by MetaRush at every sign-in (never one a browser brought: no
// fixation) and kept only as its SHA-256. A sign-in lasts SessionLife at most
// and ends after SessionIdle unused, both by the database server's clock.
// Every request reads it again with the guest, their grants and the
// workspace's switch, so taking something back, taking a guest back or
// turning guests off counts on the very next request; taking a guest's last
// grant or the guest back deletes their sign-ins.

// newToken is 256 random bits, written in base64url.
func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashOf is how a token is kept: its SHA-256.
func hashOf(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// tokenShape reports text that could be one of MetaRush's tokens (43
// base64url characters); anything else is refused before any lookup.
func tokenShape(token string) bool {
	if len(token) != 43 {
		return false
	}
	for _, r := range token {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// NewSession signs a browser in as a guest and returns its cookie (shown to
// the browser once; only its hash is kept). via is link or sso.
func NewSession(ctx context.Context, a *app.App, g *models.Guest, via, provider, from string) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	logbook.RememberSecret(token) // never in the activity log
	now, err := a.DB.Now(ctx)
	if err != nil {
		return "", err
	}
	s := models.GuestSession{ID: hashOf(token), WorkspaceID: a.WorkspaceID(), GuestID: g.ID, Via: via, Provider: provider, From: from,
		Computer: a.Cfg.Installation.ID, CreatedAt: now, ExpiresAt: now.Add(SessionLife), LastSeenAt: now}
	if err := a.DB.Insert(ctx, store.CGuestSessions, s); err != nil {
		return "", err
	}
	_, _ = a.DB.C(store.CGuests).UpdateOne(ctx, bson.M{"_id": g.ID, "workspaceId": a.WorkspaceID()},
		bson.M{"$set": bson.M{"signedInWith": via}, "$currentDate": bson.M{"lastSeenAt": true}})
	return token, nil
}

// Check is a request's sign-in: the session behind the cookie (working by
// the database server's clock), the guest (not taken back), their grants that
// work now and the workspace's switch. Anything that no longer counts signs
// the browser out, in plain words.
func Check(ctx context.Context, a *app.App, token, from string) (*Signed, error) {
	if !tokenShape(token) {
		return nil, signedOut("")
	}
	if !On(ctx, a) {
		return nil, signedOut(ReasonGuestsOff)
	}
	var s models.GuestSession
	err := a.DB.C(store.CGuestSessions).FindOne(ctx, bson.M{"_id": hashOf(token), "workspaceId": a.WorkspaceID(), "$expr": bson.M{"$and": bson.A{
		bson.M{"$gt": bson.A{"$expiresAt", "$$NOW"}},
		bson.M{"$gt": bson.A{"$lastSeenAt", bson.M{"$subtract": bson.A{"$$NOW", SessionIdle.Milliseconds()}}}},
	}}}).Decode(&s)
	if err != nil {
		return nil, signedOut("")
	}
	g, _, err := getGuest(ctx, a, s.GuestID)
	if err != nil || g.State != models.GuestActive {
		EndSessions(ctx, a, s.GuestID)
		return nil, signedOut("revoked")
	}
	grants, err := liveGrants(ctx, a, g.ID)
	if err != nil {
		return nil, err
	}
	if len(grants) == 0 {
		EndSessions(ctx, a, g.ID)
		return nil, signedOut("ended")
	}
	// Used now (written at most once a minute: every request reads it).
	_, _ = a.DB.C(store.CGuestSessions).UpdateOne(ctx, bson.M{"_id": s.ID, "workspaceId": a.WorkspaceID(), "$expr": bson.M{"$lt": bson.A{"$lastSeenAt", bson.M{"$subtract": bson.A{"$$NOW", time.Minute.Milliseconds()}}}}},
		bson.M{"$currentDate": bson.M{"lastSeenAt": true}})
	_, _ = a.DB.C(store.CGuests).UpdateOne(ctx, bson.M{"_id": g.ID, "workspaceId": a.WorkspaceID(), "$or": bson.A{bson.M{"lastSeenAt": nil},
		bson.M{"$expr": bson.M{"$lt": bson.A{"$lastSeenAt", bson.M{"$subtract": bson.A{"$$NOW", time.Minute.Milliseconds()}}}}}}},
		bson.M{"$currentDate": bson.M{"lastSeenAt": true}})
	return &Signed{Session: s, Guest: *g, Grants: grants, From: from}, nil
}

// StillSignedIn reports whether a checked sign-in still counts (a long
// answer — a video streaming — stops soon after it doesn't).
func StillSignedIn(ctx context.Context, a *app.App, s *Signed) bool {
	if !On(ctx, a) {
		return false
	}
	n, err := a.DB.C(store.CGuestSessions).CountDocuments(ctx, bson.M{"_id": s.Session.ID, "workspaceId": a.WorkspaceID(), "$expr": bson.M{"$gt": bson.A{"$expiresAt", "$$NOW"}}})
	if err != nil || n == 0 {
		return false
	}
	g, _, err := getGuest(ctx, a, s.Guest.ID)
	if err != nil || g.State != models.GuestActive {
		return false
	}
	grants, err := liveGrants(ctx, a, s.Guest.ID)
	return err == nil && len(grants) > 0
}

// EndSession signs one browser out.
func EndSession(ctx context.Context, a *app.App, token string) {
	if !tokenShape(token) {
		return
	}
	_, _ = a.DB.C(store.CGuestSessions).DeleteOne(ctx, bson.M{"_id": hashOf(token), "workspaceId": a.WorkspaceID()})
}

// EndSessions signs every browser of a guest out and says how many.
func EndSessions(ctx context.Context, a *app.App, guestID string) int {
	res, err := a.DB.C(store.CGuestSessions).DeleteMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "guestId": guestID})
	if err != nil {
		return 0
	}
	return int(res.DeletedCount)
}

// endAllSessions signs every guest out (guests turned off).
func endAllSessions(ctx context.Context, a *app.App) int {
	res, err := a.DB.C(store.CGuestSessions).DeleteMany(ctx, bson.M{"workspaceId": a.WorkspaceID()})
	if err != nil {
		return 0
	}
	return int(res.DeletedCount)
}
