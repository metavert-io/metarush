package reviewlinks

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcripts"
)

// Where a link's objects are kept: review/<key>/… under the store's prefix,
// apart from the workspace's own objects (ws/<workspace>/…), so a link's
// addresses name nothing of the workspace — not even its ID.
const (
	pageObject   = "index.html"
	videoObject  = "video.mp4"
	posterObject = "poster.jpg"
)

func seatObject(k int) string { return fmt.Sprintf("c%d.json", k) }

// prefixOf is where a link's objects are kept (the key is 128 random bits).
func prefixOf(_, key string) string { return "review/" + key + "/" }

// objectsOf are every object a link may have.
func objectsOf(l *models.ReviewLink) []string {
	p := prefixOf(l.WorkspaceID, l.Key)
	out := []string{p + pageObject, p + videoObject, p + posterObject}
	n := l.Seats
	if n <= 0 {
		n = Seats
	}
	for k := 0; k < n; k++ {
		out = append(out, p+seatObject(k))
	}
	return out
}

// newKey is 128 random bits, written in lower-case base32.
func newKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)), nil
}

// create makes a link: the review copy copied into the link's place, the page
// with every address signed until the link ends, and the record.
func create(ctx context.Context, a *app.App, in *ShareCreateIn) (*ShareOut, error) {
	pl, err := makePlan(ctx, a, *in)
	if err != nil {
		return nil, err
	}
	if pl.stop != nil {
		if pl.stop.Code == apperr.KeptHere {
			for _, s := range pl.sends {
				if err := leave.Check(ctx, a, s); err != nil {
					return nil, err // recorded: a person asked, and it was kept here
				}
			}
		}
		return nil, pl.stop
	}
	signer, ok := a.Media.(media.PutSigner)
	if !ok {
		return nil, apperr.New(apperr.NotConfigured, "%s", media.CanShare(a.Media))
	}
	key, err := newKey()
	if err != nil {
		return nil, err
	}
	pageID := make([]byte, 8)
	if _, err := rand.Read(pageID); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	ttl := time.Duration(pl.days) * 24 * time.Hour
	link := models.ReviewLink{ID: ids.New(ids.ReviewLink), WorkspaceID: a.WorkspaceID(), ProjectID: pl.p.ID, RevisionID: pl.rev.ID, RevisionNumber: pl.rev.Number,
		RenderID: pl.render.ID, Title: pl.out.Title, For: pl.in.For, Message: pl.in.Message, Words: pl.out.HasWords, Speakers: pl.in.Speakers,
		Download: pl.in.Download, Draft: pl.in.AsDraft, State: models.ReviewLinkLive, Key: key, Store: a.Media.Locator(), Seats: Seats, SeatSize: SeatSize,
		Exposes: pl.out.Exposes, Kinds: pl.out.Kinds, Projects: pl.projects, CreatedBy: a.Actor, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	if !link.Words {
		link.Speakers = models.SpeakersNone
	}
	if pl.gate != nil && pl.in.Anyway != "" {
		link.Anyway = &models.ReviewLinkAnyway{Note: pl.in.Anyway, Holds: pl.gate.Holds, By: a.Actor, At: now}
	}
	prefix := prefixOf(link.WorkspaceID, key)
	written := []string{}
	undo := func() {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		for _, k := range written {
			_ = a.Media.Delete(dctx, k)
		}
	}
	fail := func(err error, what string) (*ShareOut, error) {
		undo()
		return nil, apperr.Wrap(apperr.Storage, err, "The review link couldn't be made: %s.", what).
			WithHint("Check the internet connection and the media store (Settings → Storage), then try again.")
	}

	// The video (and its still) in the link's own place, so ending the link
	// can delete them without touching the review copy itself.
	out := pl.render.Output
	if err := copyObject(ctx, a, out.StorageKey, out.SHA256, out.Size, prefix+videoObject, "video/mp4"); err != nil {
		return fail(err, "the video couldn't be copied")
	}
	written = append(written, prefix+videoObject)
	poster := ""
	if out.ThumbnailKey != "" {
		if err := copyThumb(ctx, a, out.ThumbnailKey, prefix+posterObject); err == nil {
			written = append(written, prefix+posterObject)
			poster = prefix + posterObject
		}
	}

	// Every address, signed until the link ends.
	sign := func(k, ct, name string) (string, error) { return a.Media.PresignGet(ctx, k, ttl, ct, name) }
	d := pageData{V: 1, Page: hex.EncodeToString(pageID), Title: link.Title, From: strings.TrimSpace(a.Actor.UserName), For: link.For, Message: link.Message,
		Draft: link.Draft, Expires: link.ExpiresAt.Format(time.RFC3339), SeatSize: SeatSize,
		Limits: pageLimits{PerReviewer: PerReviewer, Text: MaxText, Name: MaxName}}
	if d.Video.URL, err = sign(prefix+videoObject, "video/mp4", ""); err != nil {
		return fail(err, "its links couldn't be signed")
	}
	d.Video.Type, d.Video.Duration = "video/mp4", out.Duration
	if poster != "" {
		if d.Poster, err = sign(poster, "image/jpeg", ""); err != nil {
			return fail(err, "its links couldn't be signed")
		}
	}
	if link.Download {
		u, err := sign(prefix+videoObject, "video/mp4", downloadName(link.Title)+".mp4")
		if err != nil {
			return fail(err, "its links couldn't be signed")
		}
		d.Download = &pageFile{URL: u, Name: downloadName(link.Title) + ".mp4"}
	}
	if link.Words {
		d.Words = turns(pl.lines, link.Speakers)
	}
	for k := 0; k < Seats; k++ {
		// Each slot starts empty, so a reviewer's browser reads something
		// (an empty slot) rather than a missing object.
		if err := putBytes(ctx, a, prefix+seatObject(k), []byte("{}"), "application/json"); err != nil {
			return fail(err, "its comment slots couldn't be stored")
		}
		written = append(written, prefix+seatObject(k))
		get, err := sign(prefix+seatObject(k), "application/json", "")
		if err != nil {
			return fail(err, "its links couldn't be signed")
		}
		put, headers, err := signer.PresignPut(ctx, prefix+seatObject(k), ttl, "application/json", SeatSize)
		if err != nil {
			return fail(err, "its links couldn't be signed")
		}
		d.Seats = append(d.Seats, pageSeat{Get: get, Put: put, Headers: headers})
	}
	pageURL, err := sign(prefix+pageObject, "text/html; charset=utf-8", "")
	if err != nil {
		return fail(err, "its links couldn't be signed")
	}
	origins := []string{}
	for _, u := range []string{pageURL, d.Video.URL, d.Seats[0].Get, d.Seats[0].Put} {
		if o := originOf(u); o != "" && !contains(origins, o) {
			origins = append(origins, o)
		}
	}
	page, err := buildPage(&d, origins)
	if err != nil {
		return fail(err, "its page couldn't be written")
	}
	if err := putBytes(ctx, a, prefix+pageObject, page, "text/html; charset=utf-8"); err != nil {
		return fail(err, "its page couldn't be stored")
	}
	written = append(written, prefix+pageObject)
	if err := a.DB.Insert(ctx, store.CReviewLinks, link); err != nil {
		undo()
		return nil, err
	}
	details := map[string]any{"revisionId": link.RevisionID, "number": link.RevisionNumber, "renderId": link.RenderID, "days": pl.days, "expiresAt": link.ExpiresAt,
		"words": link.Words, "speakers": link.Speakers, "download": link.Download, "draft": link.Draft, "kinds": link.Kinds, "exposes": link.Exposes}
	if link.For != "" {
		details["for"] = link.For
	}
	if link.Anyway != nil {
		details["anyway"], details["holds"] = link.Anyway.Note, link.Anyway.Holds
	}
	a.Audit(ctx, "share.create", "review_link", link.ID, link.ProjectID, details)
	o := outOf(link, pl.p.Name)
	o.URL = pageURL
	o.Warnings = pl.out.Warnings
	o.Summary = fmt.Sprintf("Made a review link to version %d of “%s”, %s. It shows %s. Send the link to whoever should review it.",
		link.RevisionNumber, pl.p.Name, o.Ends, joinAnd(link.Exposes))
	return o, nil
}

// copyObject puts a stored object at another key: inside the bucket when
// the store copies, else through this computer's cache.
func copyObject(ctx context.Context, a *app.App, from, sha string, size int64, to, contentType string) error {
	if c, ok := a.Media.(media.Copier); ok {
		err := c.Copy(ctx, from, to)
		if err == nil {
			return nil
		}
		if errors.Is(err, media.ErrNotFound) {
			return err
		}
	}
	p, err := a.Cache.Ensure(ctx, a.Media, from, sha, size, nil)
	if err != nil {
		return err
	}
	_, err = a.Media.PutFile(ctx, to, p, media.PutOptions{ContentType: contentType}, media.Resume{}, nil)
	return err
}

// copyThumb copies the review copy's still (small; not content-addressed by
// a known hash, so read through the store when it can't copy).
func copyThumb(ctx context.Context, a *app.App, from, to string) error {
	if c, ok := a.Media.(media.Copier); ok {
		if err := c.Copy(ctx, from, to); err == nil {
			return nil
		}
	}
	rc, info, err := a.Media.Get(ctx, from, 0, -1)
	if err != nil {
		return err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, 8<<20+1))
	if err != nil {
		return err
	}
	if len(b) > 8<<20 || (info.Size > 0 && int64(len(b)) != info.Size) {
		return errors.New("the still couldn't be read whole")
	}
	return putBytes(ctx, a, to, b, "image/jpeg")
}

// putBytes stores bytes at a key (through a private file: stores take files).
func putBytes(ctx context.Context, a *app.App, key string, b []byte, contentType string) error {
	dir := a.Cfg.Scratch.Dir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "review-*.part")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_, err = a.Media.PutFile(ctx, key, filepath.Clean(name), media.PutOptions{ContentType: contentType}, media.Resume{}, nil)
	return err
}

// turns groups a version's lines into who says what: a new turn when the
// speaker changes or after a pause of more than 2 s, speakers named as the
// person chose.
func turns(lines []transcripts.Cue, speakers string) []pageTurn {
	labels := map[string]string{}
	name := func(s string) string {
		switch {
		case speakers == models.SpeakersNone || s == "":
			return ""
		case speakers == models.SpeakersNames || s == "Narrator":
			return s
		}
		if l, ok := labels[s]; ok {
			return l
		}
		labels[s] = fmt.Sprintf("Speaker %d", len(labels)+1)
		return labels[s]
	}
	var out []pageTurn
	last := -1.0
	lastWho := "\x00"
	for _, l := range lines {
		who := name(l.Speaker)
		if len(out) == 0 || who != lastWho || l.From-last > 2 || len(out[len(out)-1].Lines) >= 12 {
			out = append(out, pageTurn{Speaker: who})
		}
		t := &out[len(out)-1]
		t.Lines = append(t.Lines, pageLine{From: round(l.From), To: round(l.To), Text: l.Text})
		last, lastWho = l.To, who
	}
	return out
}

func round(x float64) float64 { return float64(int64(x*1000+0.5)) / 1000 }

// downloadName is a plain file name for the download (letters, digits,
// spaces and - _ . only).
func downloadName(title string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == ' ', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	s := strings.Trim(strings.Join(strings.Fields(b.String()), " "), " .-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	if len(s) > 80 {
		s = strings.TrimRight(s[:80], " .-")
	}
	if s == "" {
		s = "review copy"
	}
	return s
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
