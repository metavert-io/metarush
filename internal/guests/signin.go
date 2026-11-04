package guests

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/sso"
	"github.com/metavert-io/metarush/internal/store"
)

// Guests signing in with an organization's account (D496): only when the
// workspace's own sign-in is the organization's (the app an owner set up and
// tested), through internal/sso's checks, on the guest listener's own steps.
// The provider's email must be one it vouches for (sso.Identity.EmailTrusted:
// Google's for Gmail and Workspace accounts, Microsoft's only for its own
// directory's members, never a guest there or a personal account, another
// provider's checked one) and be a guest's with something shared now. The
// account a guest signs in with first is theirs: another account of the same
// provider showing their email later is refused (D429's rule for people).

// signInRefused is a guest sign-in turned away, with the email the provider
// gave (the person's own) for the page.
func signInRefused(reason, msg, hint string, id *sso.Identity) error {
	d := map[string]any{"reason": reason}
	if id != nil && id.Email != "" {
		d["email"] = id.Email
	}
	return apperr.New(apperr.Unauthorized, "%s", msg).WithHint(hint).WithDetails(d)
}

// ForSSO says which guest an organization sign-in is: never anyone else.
func ForSSO(ctx context.Context, a *app.App, provider string, id *sso.Identity) (*models.Guest, error) {
	return forSSO(ctx, a, provider, id, 0)
}

func forSSO(ctx context.Context, a *app.App, provider string, id *sso.Identity, tries int) (*models.Guest, error) {
	const askLink = "Ask whoever shared it with you to send you a sign-in link instead."
	switch {
	case id == nil:
		return nil, signInRefused(ReasonNotTrusted, "The sign-in didn't say who you are.", askLink, nil)
	case id.Guest:
		return nil, signInRefused(ReasonMSGuest, fmt.Sprintf("%s says this account is a guest in its directory or a personal account, so MetaRush can't go by its email.", sso.Title(provider, "")), askLink, id)
	case id.Email == "" || !id.EmailTrusted:
		return nil, signInRefused(ReasonNotTrusted, fmt.Sprintf("%s doesn't vouch for this account's email, so it can't open what was shared with you.", sso.Title(provider, "")), askLink, id)
	}
	nothing := signInRefused(ReasonNotShared, fmt.Sprintf("Nothing is shared with %s here.", id.Email), "Sign in with the account whose email it was shared with, or ask for a sign-in link.", id)
	var g models.Guest
	if err := a.DB.C(store.CGuests).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "emailKey": auth.EmailKey(id.Email)}).Decode(&g); err != nil {
		return nil, nothing
	}
	if g.State != models.GuestActive {
		return nil, nothing
	}
	if grants, err := liveGrants(ctx, a, g.ID); err != nil || len(grants) == 0 {
		return nil, nothing
	}
	if g.Account != nil && g.Account.Scope == id.Scope && g.Account.Key != id.Key {
		// Another account of the same provider showing their email (one made
		// again, a look-alike) doesn't take theirs over.
		return nil, signInRefused(ReasonOtherAcct, fmt.Sprintf("%s signs in here with another %s account.", id.Email, sso.Title(provider, "")),
			"Sign in with that account, or ask for a sign-in link.", id)
	}
	if g.Account == nil || g.Account.Scope != id.Scope {
		acct := models.GuestAccount{Key: id.Key, Scope: id.Scope, Provider: provider, LinkedAt: time.Now().UTC()}
		f := bson.M{"_id": g.ID, "workspaceId": a.WorkspaceID()}
		if g.Account == nil {
			f["account"] = nil // the first sign-in links it: two at once can't both
		} else {
			f["account.scope"] = g.Account.Scope
		}
		res, err := a.DB.C(store.CGuests).UpdateOne(ctx, f, bson.M{"$set": bson.M{"account": acct}})
		if err != nil {
			return nil, err
		}
		if res.MatchedCount == 0 {
			// Linked at the same moment: read again and go by that.
			if tries > 2 {
				return nil, apperr.New(apperr.Conflict, "MetaRush couldn't finish signing you in. Try again.")
			}
			return forSSO(ctx, a, provider, id, tries+1)
		}
		g.Account = &acct
	}
	return &g, nil
}
