// Package guests lets people outside the workspace — a client approving a
// cut from a work laptop, a lawyer checking a version — watch versions an
// owner shared with them, and leave comments, without joining the workspace
// (D490–D504, docs/guests.md).
//
// A guest is not a member: an email with grants. Each grant names a project
// (every version of it with a review copy) or some of its versions, a role —
// viewer or commenter — and an end. Owners share, send one-time sign-in links
// and take things back, people only; What may leave is asked for every grant
// and again for every version a guest opens (a client's rules can keep their
// things from guests).
//
// Guests reach MetaRush only through the guest listener (internal/api,
// guests*.go): a second web server on 127.0.0.1 with its own router, its own
// Host check and its own cookie, which calls the guest operations here
// (guest.shared, guest.watch, guest.comments, guest.comment) as the guest whose
// sign-in it checked — never the members' operations. Every guest request is
// recorded (activity.go) and owners hear of unusual volume. In this release
// the guest address is this computer's own: nothing is reachable from the
// internet.
package guests

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

const (
	// DefaultDays is how long a grant lasts unless the owner says otherwise;
	// MaxDays the longest an owner can give at once (sharing again extends).
	DefaultDays = 14
	MaxDays     = 90
	// SessionLife is the longest a guest stays signed in; SessionIdle how
	// long unused before their sign-in ends (both by the database server's
	// clock).
	SessionLife = 12 * time.Hour
	SessionIdle = 2 * time.Hour
	// LinkLife is how long a one-time sign-in link works (never past the
	// guest's last grant).
	LinkLife = 24 * time.Hour
	// MediaLife is how long a signed link to a review copy in the bucket
	// works: the page asks for a new one when it stops working, and each new
	// one asks the grant again. A link handed out can't be called back, so a
	// video from the bucket stops within MediaLife of being taken back (a
	// request already streaming goes on: the bucket checks a link only when a
	// request starts). Links are signed by this computer's clock and checked
	// by the bucket's, so a few minutes leave room for clocks that differ.
	MediaLife = 5 * time.Minute
	// KeepActivity is how long guests' requests are kept.
	KeepActivity = 90 * 24 * time.Hour
	// MaxComment, MaxName and MaxMessage are lengths in characters;
	// CommentsPerHour the most comments one guest leaves in an hour, and
	// CommentsPerVersion on one version.
	MaxComment         = 2000
	MaxName            = 80
	MaxMessage         = 500
	CommentsPerHour    = 60
	CommentsPerVersion = 200
	// tokenBytes is a link's and a cookie's randomness: 256 bits.
	tokenBytes = 32
)

// VolumePerHour is how many requests by one guest in an hour count as
// unusual: owners are told (once a day per guest). A variable for tests.
var VolumePerHour = 600

// Signed is a guest's request whose sign-in the guest listener checked: the
// browser's session, the guest, and their grants that work now.
type Signed struct {
	Session models.GuestSession
	Guest   models.Guest
	Grants  []models.GuestGrant
	// From is the visitor's address for this request (the connection's; a
	// forwarded address is believed nowhere yet, D499).
	From string
}

type signedKey struct{}

// WithSigned is ctx carrying a checked guest request: the guest operations
// answer for that guest (the guest listener alone sets it).
func WithSigned(ctx context.Context, s *Signed) context.Context {
	return context.WithValue(ctx, signedKey{}, s)
}

// SignedFrom is the checked guest request behind ctx (nil: none).
func SignedFrom(ctx context.Context) *Signed {
	s, _ := ctx.Value(signedKey{}).(*Signed)
	return s
}

// Actor is how a guest appears in records: their own ID and "<name> (guest)".
func Actor(g *models.Guest, installation string) models.Actor {
	return models.Actor{UserID: g.ID, UserName: GuestName(g), InstallationID: installation, Via: models.ViaGuest}
}

// GuestName names a guest for members: the name the owner gave, else their
// email, and "(guest)".
func GuestName(g *models.Guest) string {
	n := strings.TrimSpace(g.Name)
	if n == "" {
		n = g.Email
	}
	return n + " (guest)"
}

// who names a guest in a sentence: the name the owner gave, else their email.
func who(g *models.Guest) string {
	if n := strings.TrimSpace(g.Name); n != "" {
		return n
	}
	return g.Email
}

// Why a guest is turned away (details.reason).
const (
	ReasonNotShared  = "not_shared"        // nothing a grant names (or it doesn't exist)
	ReasonSignedOut  = "signed_out"        // no sign-in, or it ended
	ReasonGuestsOff  = "guests_off"        // owners turned guests off
	ReasonKeptHere   = "kept_here"         // a client's rules keep it from guests
	ReasonNotReady   = "not_ready"         // no review copy yet
	ReasonHeld       = "held"              // it plays someone else's video or changed words
	ReasonConsent    = "consent_withdrawn" // it plays something made with a consent that was withdrawn
	ReasonTooLarge   = "too_large"         // more in it than the checks look through
	ReasonViewer     = "viewer"            // a viewer can't comment
	ReasonTooMany    = "too_many"          // too many comments
	ReasonLinkGone   = "link_gone"         // a link used, ended or taken back
	ReasonNotTrusted = "not_trusted"       // the provider doesn't vouch for the email
	ReasonMSGuest    = "ms_guest"          // a Microsoft guest or personal account
	ReasonOtherAcct  = "other_account"
	ReasonNoSSO      = "no_sso" // the organization's sign-in isn't how people sign in here
	ReasonThrottled  = "throttled"
	ReasonOther      = "other_address" // a Host that isn't the guest address
	ReasonCSRF       = "csrf"
)

// notShared is the one answer for anything a guest's grants don't name: a
// version or project of someone else's, one deleted since, one that never
// existed. The same words for all of them, so nothing is learned by asking.
func notShared() error {
	return apperr.New(apperr.NotFound, "That isn't shared with you.").
		WithHint("Open what was shared with you from your guest page.").WithDetails(map[string]any{"reason": ReasonNotShared})
}

// signedOut is the answer when a guest's sign-in doesn't count (any more).
func signedOut(why string) error {
	msg := "You're not signed in as a guest here."
	switch why {
	case ReasonGuestsOff:
		msg = "Guests can't open MetaRush here right now: its owners turned guests off."
	case "revoked":
		msg = "The owner took back what they shared with you, so you're signed out."
	case "ended":
		msg = "What was shared with you has ended, so you're signed out."
	}
	reason := ReasonSignedOut
	if why == ReasonGuestsOff {
		reason = ReasonGuestsOff
	}
	return apperr.New(apperr.Unauthorized, "%s", msg).WithHint("Ask whoever shared it with you for a new sign-in link.").
		WithDetails(map[string]any{"reason": reason})
}

// oneLine is someone's short text: one line, cleaned, at most n characters.
func oneLine(s string, n int) string {
	s = ops.CleanText(strings.ToValidUTF8(s, ""))
	s = strings.Join(strings.Fields(s), " ")
	return cut(s, n)
}

// words is someone's longer text: cleaned, trimmed, at most n characters.
func words(s string, n int) string {
	s = strings.TrimSpace(ops.CleanText(strings.ToValidUTF8(s, "")))
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return cut(s, n)
}

func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:n]))
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	case 2:
		return xs[0] + " and " + xs[1]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// dayWords is a time as people read an end ("20 Nov 2026, 18:00").
func dayWords(t time.Time) string { return t.Local().Format("2 Jan 2006, 15:04") }

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// roleWords says what a role lets a guest do.
func roleWords(role string) string {
	if role == models.GuestCommenter {
		return "watch and comment"
	}
	return "watch"
}

// better is the role that lets a guest do more.
func better(a, b string) string {
	if a == models.GuestCommenter || b == models.GuestCommenter {
		return models.GuestCommenter
	}
	return models.GuestViewer
}
