package generate

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// What other kinds of AI work (internal/aiwork, D226) share with generated
// video: write-once records, consent, the stored bytes of what is sent, and
// the money (ReserveWork, Settle, BudgetView).

// SetOnce writes a part of a generation record only while it is still empty (D125).
func SetOnce(ctx context.Context, a *app.App, genID, field string, v any) error {
	return setOnce(ctx, a, genID, field, v)
}

// AppendTo adds an entry to a list in a generation record.
func AppendTo(ctx context.Context, a *app.App, genID, field string, v any) error {
	return appendTo(ctx, a, genID, field, v)
}

// ActiveConsent is the consent in force for a person in a project ("" = no
// project); an error when there is none.
func ActiveConsent(ctx context.Context, a *app.App, personID, projectID string, now time.Time) (*models.Consent, error) {
	return activeConsent(ctx, a, personID, projectID, now)
}

// ActiveConsentFor is the newest consent in force for a person in a project
// that covers a use (D274: shots, voice, presenters; "" any).
func ActiveConsentFor(ctx context.Context, a *app.App, personID, projectID, use string, now time.Time) (*models.Consent, error) {
	return activeConsentFor(ctx, a, personID, projectID, use, now)
}

// FindLook finds a look by its ID or short name.
func FindLook(ctx context.Context, a *app.App, ref string) (*models.GenLook, error) {
	return findLook(ctx, a, ref)
}

// ErrChildOrPublic says a person is recorded as a child or a public figure:
// MetaRush never makes or changes their likeness or voice, consent or not.
var ErrChildOrPublic = errors.New("a child or a public figure")

// ConsentForUse is a person's consent in force for a project that names a use
// and the service it may go to for it (D279, D309); new words also need their
// voice in the same consent (they are said in a copy of it). A person
// recorded as a child or a public figure is never covered (ErrChildOrPublic);
// none is errNoConsent.
func ConsentForUse(ctx context.Context, a *app.App, personID, projectID, use, service string, now time.Time) (*models.Consent, error) {
	var cs []models.Consent
	if err := a.DB.Find(ctx, store.CConsents, a.WorkspaceID(), bson.M{"personId": personID, "revokedAt": bson.M{"$exists": false}}, &cs,
		options.Find().SetSort(bson.D{{Key: "recordedAt", Value: -1}})); err != nil {
		return nil, err
	}
	for i := range cs {
		if cs[i].PublicFigure || !cs[i].Adult {
			return nil, ErrChildOrPublic
		}
	}
	for i := range cs {
		if cs[i].Covers(projectID, now) && cs[i].CoversUseAt(use, service) &&
			(use != models.ConsentUseNewWords || cs[i].CoversUse(models.ConsentVoice)) {
			return &cs[i], nil
		}
	}
	return nil, errNoConsent
}

// NamedFace is a face the on-screen index shows, named by a person.
type NamedFace = namedFace

// FacesIn says who the on-screen index shows in [from, to] of a clip: the
// people named, how many faces nobody named, whom those look like, and
// whether the clip was looked at at all.
func FacesIn(ctx context.Context, a *app.App, as *models.Asset, from, to float64) ([]NamedFace, int, []string, bool) {
	return facesIn(ctx, a, as, from, to)
}

// LoadConsent loads a consent record.
func LoadConsent(ctx context.Context, a *app.App, id string) (*models.Consent, error) {
	return loadConsent(ctx, a, id)
}

// RevokedConsents says which of the generations' consents were withdrawn.
func RevokedConsents(ctx context.Context, a *app.App, gs []models.Generation) map[string]bool {
	return revokedConsents(ctx, a, gs)
}

// Stored is a file kept in the media store by its content.
type Stored = stored

// Keep stores a local file by its content.
func Keep(ctx context.Context, a *app.App, path, mime string) (*Stored, error) {
	return keep(ctx, a, path, mime)
}

// Local returns a stored file on this machine (from the cache, fetched when needed).
func Local(ctx context.Context, a *app.App, key, sha string, size int64) (string, error) {
	return local(ctx, a, key, sha, size)
}

// Scratch is a file name in a fresh scratch folder, and what removes the folder.
func Scratch(a *app.App, name string) (string, func(), error) { return scratch(a, name) }

// ClipPart keeps [from, to) of a clip as a small MP4 (sound kept).
func ClipPart(ctx context.Context, a *app.App, as *models.Asset, from, to float64) (*Stored, error) {
	return part(ctx, a, as, from, to, false)
}

// Original is a clip's file on this machine.
func Original(ctx context.Context, a *app.App, as *models.Asset) (string, error) {
	return original(ctx, a, as)
}

// Picture keeps a resources-library picture as a JPEG and names its version (name@sha).
func Picture(ctx context.Context, a *app.App, projectID, ref string) (*Stored, *models.Resource, string, error) {
	return picture(ctx, a, projectID, ref)
}

// PictureAsIs keeps a resources-library picture's own file, its size and
// type unchanged (a background taken away, a picture made larger: D279),
// and names its version (name@sha).
func PictureAsIs(ctx context.Context, a *app.App, projectID, ref string) (*Stored, *models.Resource, string, error) {
	return pictureAsIs(ctx, a, projectID, ref)
}

// Money says an amount the way people read prices.
func Money(usd float64) string { return money(usd) }

// ChannelOf says through which surface a request came.
func ChannelOf(actor models.Actor, origin string) string { return channelOf(actor, origin) }

// PersonName is a person's name ("someone" when unknown).
func PersonName(ctx context.Context, a *app.App, personID string) string {
	return personName(ctx, a, personID)
}

// Clip shortens words to n characters.
func Clip(s string, n int) string { return clip(s, n) }

// UpperFirst capitalizes the first letter.
func UpperFirst(s string) string { return upperFirst(strings.TrimSpace(s)) }

var (
	revokeMu    sync.Mutex
	revokeHooks []func(ctx context.Context, a *app.App, c *models.Consent) []string
)

// OnConsentRevoked registers what else stops relying on a consent when it is
// withdrawn (voice copies, D227); each hook returns what it withdrew, in
// plain words.
func OnConsentRevoked(fn func(ctx context.Context, a *app.App, c *models.Consent) []string) {
	revokeMu.Lock()
	defer revokeMu.Unlock()
	revokeHooks = append(revokeHooks, fn)
}

func consentRevoked(ctx context.Context, a *app.App, c *models.Consent) []string {
	revokeMu.Lock()
	hooks := append([]func(context.Context, *app.App, *models.Consent) []string{}, revokeHooks...)
	revokeMu.Unlock()
	var out []string
	for _, h := range hooks {
		out = append(out, h(ctx, a, c)...)
	}
	return out
}
