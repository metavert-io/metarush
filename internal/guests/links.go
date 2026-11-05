package guests

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// One-time sign-in links an owner sends a guest (D495): 256 random bits in
// the address's fragment (#…), which browsers never send to a server, so it
// stays out of every log on the way; kept only as its SHA-256; working for
// LinkLife (never past the guest's last grant); used once, by a check and set.
// The guest page reads it and signs in only when the guest presses its
// button, so a mail scanner opening the address uses nothing; the browser
// that pressed it is the one signed in. Making a new link for a guest takes
// back their unused ones. Owners only, people only.

// GuestsLinkIn names the guest a link is for (their email or ID).
type GuestsLinkIn struct {
	Guest string `json:"guest"`
}

// GuestsLinkOut is the link to send, shown once.
type GuestsLinkOut struct {
	// URL is the link: it opens the guest page at the guest address and signs
	// in the browser that uses it, once.
	URL       string       `json:"url"`
	ExpiresAt time.Time    `json:"expiresAt"`
	Ends      string       `json:"ends"`
	Guest     models.Guest `json:"guest"`
	Warnings  []string     `json:"warnings"`
	Summary   string       `json:"summary"`
}

// WelcomePath is where a link opens the guest page.
const WelcomePath = "/welcome"

func init() {
	ops.Def("guests.link", "Make a one-time sign-in link for a guest to send them yourself (it works once, for a day at most, in the browser that opens it; earlier unused links stop working); owners, people only", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true},
		makeLink)
}

func makeLink(ctx context.Context, a *app.App, in *GuestsLinkIn) (*GuestsLinkOut, error) {
	if !On(ctx, a) {
		return nil, apperr.New(apperr.Conflict, "Guests are off here, so a sign-in link wouldn't work.").
			WithHint("Turn guests on first, in Settings → People → Guests.").WithDetails(map[string]any{"reason": ReasonGuestsOff}).WithCommand("metarush guests on")
	}
	g, err := findGuest(ctx, a, in.Guest)
	if err != nil {
		return nil, err
	}
	if g.State != models.GuestActive {
		return nil, apperr.New(apperr.Conflict, "Everything shared with %s was taken back.", who(g)).WithHint("Share something with them again first.")
	}
	grants, err := liveGrants(ctx, a, g.ID)
	if err != nil {
		return nil, err
	}
	if len(grants) == 0 {
		return nil, apperr.New(apperr.Conflict, "Nothing is shared with %s now.", who(g)).WithHint("Share a project or a version with them first.")
	}
	last := grants[0].EndsAt
	for _, gr := range grants {
		if gr.EndsAt.After(last) {
			last = gr.EndsAt
		}
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	logbook.RememberSecret(token) // never in the activity log
	now, err := a.DB.Now(ctx)
	if err != nil {
		return nil, err
	}
	exp := now.Add(LinkLife)
	if last.Before(exp) {
		exp = last
	}
	revokeLinks(ctx, a, g.ID)
	if err := a.DB.Insert(ctx, store.CGuestLinks, models.GuestLink{ID: hashOf(token), WorkspaceID: a.WorkspaceID(), GuestID: g.ID, CreatedBy: a.Actor, CreatedAt: now, ExpiresAt: exp}); err != nil {
		return nil, err
	}
	a.Audit(ctx, "guests.link", "guest", g.ID, "", map[string]any{"expiresAt": exp})
	l := a.Cfg.GuestListener()
	out := &GuestsLinkOut{URL: l.Address() + WelcomePath + "#" + token, ExpiresAt: exp, Ends: "until " + dayWords(exp), Guest: *g, Warnings: []string{}}
	if !l.Listen {
		out.Warnings = append(out.Warnings, "This computer doesn't answer guests: turn guests on here (Settings → People → Guests) before they open it.")
	}
	out.Warnings = append(out.Warnings, "Nothing is reachable from the internet yet: the link opens on this computer only, until a later release gives guests a public address.")
	out.Summary = fmt.Sprintf("A sign-in link for %s, working once %s. Send it to them yourself; anyone who has it can use it once, so send it only to them.", who(g), out.Ends)
	return out, nil
}

// revokeLinks takes back a guest's unused links.
func revokeLinks(ctx context.Context, a *app.App, guestID string) {
	_, _ = a.DB.C(store.CGuestLinks).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "guestId": guestID, "usedAt": nil, "revokedAt": nil},
		bson.M{"$currentDate": bson.M{"revokedAt": true}})
}

// revokeAllLinks takes back every unused link (guests turned off).
func revokeAllLinks(ctx context.Context, a *app.App) {
	_, _ = a.DB.C(store.CGuestLinks).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "usedAt": nil, "revokedAt": nil},
		bson.M{"$currentDate": bson.M{"revokedAt": true}})
}

// linkGone is the one answer for a link that doesn't work: used, ended,
// taken back, or never made.
func linkGone() error {
	return apperr.New(apperr.Unauthorized, "This sign-in link doesn't work any more: it was used, it ran out or it was taken back.").
		WithHint("Ask whoever shared it with you for a new one.").WithDetails(map[string]any{"reason": ReasonLinkGone})
}

// Redeem uses a link, once, and says whose it was: their guest record, with
// something shared now. Anything else is the same plain refusal.
func Redeem(ctx context.Context, a *app.App, token string) (*models.Guest, error) {
	if !tokenShape(token) {
		return nil, linkGone()
	}
	if !On(ctx, a) {
		return nil, signedOut(ReasonGuestsOff)
	}
	working := bson.M{"_id": hashOf(token), "workspaceId": a.WorkspaceID(), "usedAt": nil, "revokedAt": nil, "$expr": bson.M{"$gt": bson.A{"$expiresAt", "$$NOW"}}}
	var l models.GuestLink
	if err := a.DB.C(store.CGuestLinks).FindOne(ctx, working).Decode(&l); err != nil {
		return nil, linkGone() // a made-up link is never taken for a secret
	}
	logbook.RememberSecret(token)
	g, _, err := getGuest(ctx, a, l.GuestID)
	if err != nil || g.State != models.GuestActive {
		return nil, linkGone()
	}
	if grants, err := liveGrants(ctx, a, g.ID); err != nil || len(grants) == 0 {
		return nil, linkGone()
	}
	// Used once: the first browser to claim it is the one signed in.
	res, err := a.DB.C(store.CGuestLinks).UpdateOne(ctx, working, bson.M{"$currentDate": bson.M{"usedAt": true}})
	if err != nil {
		return nil, err
	}
	if res.ModifiedCount != 1 {
		return nil, linkGone()
	}
	return g, nil
}
