package remote

import (
	"context"
	"mime"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Each Tailscale account that signs in by itself is a person in the
// workspace (a `usr_` user): the one whose logins hold it, else the one whose
// email it is (the same person on another of their computers), else a new
// person named as Tailscale names them. Their actions are theirs in the audit
// trail, the activity log, "Approved by" and notifications.

// creating serializes first sign-ins in this process (two requests of the
// same new person must not make two people).
var creating sync.Mutex

// DecodeHeader reads a Tailscale identity header value: Tailscale writes
// values with letters outside ASCII as RFC 2047 "Q" words (=?utf-8?q?…?=).
// Control characters are dropped and the result is trimmed.
func DecodeHeader(v string) string {
	if strings.Contains(v, "=?") {
		if d, err := new(mime.WordDecoder).DecodeHeader(v); err == nil {
			v = d
		}
	}
	v = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, v)
	return strings.TrimSpace(v)
}

// DisplayName is the name a new person gets: Tailscale's display name, else
// the part of the account before the @.
func DisplayName(login, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name, _, _ = strings.Cut(login, "@")
	}
	if r := []rune(name); len(r) > 80 {
		name = string(r[:80])
	}
	return name
}

// PersonFor returns the person a Tailscale account is, making them on their
// first sign-in (created reports that). It records when they signed in.
func PersonFor(ctx context.Context, a *app.App, login, displayName string) (models.User, bool, error) {
	login = strings.ToLower(strings.TrimSpace(login))
	if login == "" {
		return models.User{}, false, apperr.New(apperr.InvalidInput, "There is no Tailscale account to sign in with.")
	}
	creating.Lock()
	defer creating.Unlock()
	now := time.Now().UTC()
	u, err := findLogin(ctx, a, login)
	if err != nil {
		return models.User{}, false, err
	}
	if u != nil {
		_, _ = a.DB.C(store.CUsers).UpdateOne(ctx, bson.M{"_id": u.ID, "workspaceId": a.WorkspaceID()},
			bson.M{"$set": bson.M{"lastSignedInAt": now}, "$addToSet": bson.M{"logins": login}})
		u.LastSignedInAt = &now
		return *u, false, nil
	}
	nu := models.User{ID: ids.New(ids.User), WorkspaceID: a.WorkspaceID(), Name: DisplayName(login, displayName), Logins: []string{login},
		CreatedAt: now, LastSignedInAt: &now, Source: "tailscale"}
	if looksLikeEmail(login) {
		nu.Email = login
	}
	if err := a.DB.Insert(ctx, store.CUsers, nu); err != nil {
		return models.User{}, false, apperr.Wrap(apperr.Database, err, "MetaRush couldn't add this person to the workspace.")
	}
	return nu, true, nil
}

// findLogin finds the person a Tailscale account belongs to (nil: nobody yet).
func findLogin(ctx context.Context, a *app.App, login string) (*models.User, error) {
	var us []models.User
	err := a.DB.Find(ctx, store.CUsers, a.WorkspaceID(), bson.M{"$or": bson.A{
		bson.M{"logins": login},
		bson.M{"email": bson.Regex{Pattern: "^" + regexp.QuoteMeta(login) + "$", Options: "i"}},
	}}, &us)
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't look up who this is in its database.")
	}
	var byEmail *models.User
	for i := range us {
		for _, l := range us[i].Logins {
			if l == login {
				return &us[i], nil
			}
		}
		if byEmail == nil {
			byEmail = &us[i]
		}
	}
	return byEmail, nil
}

// Known returns the people the given Tailscale accounts already are (by account).
func Known(ctx context.Context, a *app.App, logins []string) map[string]models.User {
	out := map[string]models.User{}
	if len(logins) == 0 || a.DB == nil {
		return out
	}
	for _, l := range logins {
		if u, err := findLogin(ctx, a, l); err == nil && u != nil {
			out[l] = *u
		}
	}
	return out
}

// LinkThisComputer records that this computer's Tailscale account is the
// person who runs MetaRush here (config.yaml's user), so they are the same
// person whether they use this computer or another one. It never takes an
// account another person already has.
func LinkThisComputer(ctx context.Context, a *app.App, login string) (bool, error) {
	login = strings.ToLower(strings.TrimSpace(login))
	if login == "" || a.Cfg.User.ID == "" {
		return false, nil
	}
	creating.Lock()
	defer creating.Unlock()
	u, err := findLogin(ctx, a, login)
	if err != nil {
		return false, err
	}
	if u != nil && u.ID != a.Cfg.User.ID {
		return false, nil
	}
	res, err := a.DB.C(store.CUsers).UpdateOne(ctx, bson.M{"_id": a.Cfg.User.ID, "workspaceId": a.WorkspaceID()}, bson.M{"$addToSet": bson.M{"logins": login}})
	if err != nil {
		return false, apperr.Wrap(apperr.Database, err, "MetaRush couldn't save who this computer's Tailscale account is.")
	}
	return res.ModifiedCount > 0, nil
}

func looksLikeEmail(s string) bool {
	_, domain, ok := strings.Cut(s, "@")
	return ok && strings.Contains(domain, ".")
}
