package guests

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/withdrawn"
)

// The guest API (D492): new, narrow operations — what was shared with this
// guest, a version's review copy to watch, their comments and a new one —
// answering for the guest whose sign-in the guest listener checked (Signed in
// the context, Caller.Guest), never the members' operations with checks
// bolted on. Everything is held to the guest's grants that work now: a
// version their grants don't name, one deleted since, a project's other
// versions and anything else get the same answer (notShared). An owner may
// look at what a guest sees (As, read-only; the registry lets owners alone
// do it), never act as one.

// GuestSharedIn asks what was shared with the guest.
type GuestSharedIn struct {
	// As is an owner looking at what a guest sees: the guest's email or ID.
	As string `json:"as,omitempty"`
}

// GuestVersion is a version as a guest sees it.
type GuestVersion struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	// Cut is the cut's name ("" for the main cut).
	Cut      string    `json:"cut,omitempty"`
	Made     time.Time `json:"made"`
	Duration float64   `json:"duration"`
	// Ready: it has a review copy to watch; Shape its shape (widescreen,
	// vertical, square).
	Ready bool   `json:"ready"`
	Shape string `json:"shape,omitempty"`
}

// GuestShare is one thing shared with the guest.
type GuestShare struct {
	Project string `json:"project"`
	// Role is viewer or commenter; CanComment the same as a yes or no.
	Role       string    `json:"role"`
	CanComment bool      `json:"canComment"`
	Ends       time.Time `json:"ends"`
	EndsWords  string    `json:"endsWords"`
	// From is who shared it; Message their note.
	From    string `json:"from,omitempty"`
	Message string `json:"message,omitempty"`
	// Kept: a client's rules keep this project's things from guests now.
	Kept     bool           `json:"kept,omitempty"`
	Versions []GuestVersion `json:"versions"`
}

// GuestSelf is the guest, as their page greets them.
type GuestSelf struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email"`
}

// GuestSharedOut is what was shared with the guest, newest first.
type GuestSharedOut struct {
	Guest   GuestSelf    `json:"guest"`
	Shares  []GuestShare `json:"shares"`
	Summary string       `json:"summary"`
}

// GuestWatchIn asks for a version's review copy.
type GuestWatchIn struct {
	Version string `json:"version"`
	As      string `json:"as,omitempty"`
}

// GuestWatchOut is where the guest's browser watches it: a link signed
// straight from the workspace's bucket for MediaLife, or, with the media on
// this computer, the guest listener's own address (checked on every request).
type GuestWatchOut struct {
	Version GuestVersion `json:"version"`
	Project string       `json:"project"`
	URL     string       `json:"url"`
	Type    string       `json:"type"`
	// Poster is a still to show before it plays ("" none).
	Poster string `json:"poster,omitempty"`
	// Until is when a signed link stops working (the page asks again).
	Until      *time.Time `json:"until,omitempty"`
	CanComment bool       `json:"canComment"`
	Summary    string     `json:"summary"`
}

// GuestCommentsIn asks for the guest's comments on a version.
type GuestCommentsIn struct {
	Version string `json:"version"`
	As      string `json:"as,omitempty"`
}

// GuestComment is one of the guest's comments.
type GuestComment struct {
	ID   string    `json:"id"`
	At   *float64  `json:"at,omitempty"`
	Body string    `json:"body"`
	Made time.Time `json:"made"`
}

// GuestCommentsOut is their comments on the version, oldest first.
type GuestCommentsOut struct {
	Comments   []GuestComment `json:"comments"`
	CanComment bool           `json:"canComment"`
	Summary    string         `json:"summary"`
}

// GuestCommentIn is a new comment, pinned to a time or not.
type GuestCommentIn struct {
	Version string   `json:"version"`
	At      *float64 `json:"at,omitempty" jsonschema:"minimum=0"`
	Body    string   `json:"body" jsonschema:"maxLength=2000"`
}

// GuestCommentOut is the comment kept.
type GuestCommentOut struct {
	Comment GuestComment `json:"comment"`
	Summary string       `json:"summary"`
}

func init() {
	ops.Def("guest.shared", "What was shared with this guest: the projects and versions they may watch, until when, and whether they may comment (the guest page; owners may look at what a guest sees)", ops.Flags{HumanOnly: true, Guest: true},
		func(ctx context.Context, a *app.App, in *GuestSharedIn) (*GuestSharedOut, error) {
			s, err := current(ctx, a, in.As)
			if err != nil {
				return nil, err
			}
			return shared(ctx, a, s)
		})
	ops.Def("guest.watch", "Watch a version shared with this guest: its review copy, through a short-lived link (the guest page; owners may look at what a guest sees)", ops.Flags{HumanOnly: true, Guest: true},
		func(ctx context.Context, a *app.App, in *GuestWatchIn) (*GuestWatchOut, error) {
			s, err := current(ctx, a, in.As)
			if err != nil {
				return nil, err
			}
			return watch(ctx, a, s, in.Version)
		})
	ops.Def("guest.comments", "This guest's comments on a version shared with them (the guest page; owners may look at what a guest sees)", ops.Flags{HumanOnly: true, Guest: true},
		func(ctx context.Context, a *app.App, in *GuestCommentsIn) (*GuestCommentsOut, error) {
			s, err := current(ctx, a, in.As)
			if err != nil {
				return nil, err
			}
			return comments(ctx, a, s, in.Version)
		})
	ops.Def("guest.comment", "Leave a comment on a version shared with this guest as a commenter, pinned to a time or not (the guest page only; it comes back to the version)", ops.Flags{Mutating: true, HumanOnly: true, Guest: true, MaxInput: 16 << 10},
		func(ctx context.Context, a *app.App, in *GuestCommentIn) (*GuestCommentOut, error) {
			s := SignedFrom(ctx)
			if s == nil || !ops.CallerFrom(ctx).Guest {
				return nil, signedOut("")
			}
			return comment(ctx, a, s, in)
		})
}

// current is the guest an operation answers for: the one the guest listener
// signed in, or, for an owner (the registry checked), the guest they name.
func current(ctx context.Context, a *app.App, as string) (*Signed, error) {
	if ops.CallerFrom(ctx).Guest {
		s := SignedFrom(ctx)
		if s == nil {
			return nil, signedOut("")
		}
		if as != "" {
			return nil, apperr.New(apperr.InvalidInput, "Guests see only what was shared with them.")
		}
		return s, nil
	}
	if strings.TrimSpace(as) == "" {
		return nil, apperr.New(apperr.InvalidInput, "Say which guest's view to look at: their email.").WithCommand("metarush guests list")
	}
	g, err := findGuest(ctx, a, as)
	if err != nil {
		return nil, err
	}
	grants, err := liveGrants(ctx, a, g.ID)
	if err != nil {
		return nil, err
	}
	if g.State != models.GuestActive {
		grants = nil
	}
	return &Signed{Guest: *g, Grants: grants}, nil
}

/* ---------- What was shared ---------- */

func shared(ctx context.Context, a *app.App, s *Signed) (*GuestSharedOut, error) {
	out := &GuestSharedOut{Guest: GuestSelf{Name: s.Guest.Name, Email: s.Guest.Email}, Shares: []GuestShare{}}
	nv := 0
	for _, gr := range s.Grants {
		var p models.Project
		if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), gr.ProjectID, &p); err != nil || p.Archived || p.Purging != nil {
			continue
		}
		sh := GuestShare{Project: p.Name, Role: gr.Role, CanComment: gr.Role == models.GuestCommenter, Ends: gr.EndsAt, EndsWords: "until " + dayWords(gr.EndsAt),
			From: gr.CreatedBy.UserName, Message: gr.Message, Versions: []GuestVersion{}}
		if r, _ := leave.Problem(leave.Quiet(ctx), a, leave.Send{Place: models.PlaceGuests, Kinds: []string{models.LeaveFootage}, Projects: []string{p.ID}, What: "a guest's page"}); r != nil {
			sh.Kept = true
		}
		var revs []models.Revision
		var err error
		if len(gr.RevisionIDs) == 0 {
			revs, err = projectVersions(ctx, a, p.ID, 100)
		} else {
			err = a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": gr.RevisionIDs}, "projectId": p.ID, "draftId": bson.M{"$in": bson.A{nil, ""}}}, &revs,
				options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetProjection(bson.M{"spec.clips": 0, "spec.overlays": 0, "ops": 0, "transcripts": 0}))
		}
		if err != nil {
			return nil, err
		}
		for i := range revs {
			sh.Versions = append(sh.Versions, versionView(ctx, a, &p, &revs[i]))
		}
		nv += len(sh.Versions)
		out.Shares = append(out.Shares, sh)
	}
	switch {
	case len(out.Shares) == 0:
		out.Summary = "Nothing is shared with you here now."
	default:
		out.Summary = fmt.Sprintf("%s shared with you.", plural(nv, "version"))
	}
	return out, nil
}

// versionView is a version as a guest sees it.
func versionView(ctx context.Context, a *app.App, p *models.Project, rev *models.Revision) GuestVersion {
	v := GuestVersion{ID: rev.ID, Number: rev.Number, Cut: cutName(p, rev.Branch), Made: rev.CreatedAt, Duration: rev.Duration}
	if rd := reviewCopy(ctx, a, rev); rd != nil {
		v.Ready, v.Shape = true, ops.RenderShape(rd, rev).Word
		if rd.Output != nil && rd.Output.Duration > 0 {
			v.Duration = rd.Output.Duration
		}
	}
	v.Duration = math.Round(v.Duration*100) / 100
	return v
}

// reviewCopy is the finished review copy a guest watches: the newest in the
// version's own shape, else the newest in another (as review links play).
func reviewCopy(ctx context.Context, a *app.App, rev *models.Revision) *models.Render {
	var rs []models.Render
	if err := a.DB.Find(ctx, store.CRenders, a.WorkspaceID(), bson.M{"revisionId": rev.ID, "quality": "review", "state": models.RenderAvailable, "output": bson.M{"$ne": nil}}, &rs,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(20)); err != nil {
		return nil
	}
	rd, _ := ops.PickRender(rs, rev)
	return rd
}

// seen is a version a guest may see: its project, and the role the grants
// naming it give (the most they allow).
type seen struct {
	rev  *models.Revision
	p    *models.Project
	role string
	// grant is the grant giving the role (its owner hears of comments).
	grant *models.GuestGrant
}

// version finds a version the guest's grants name — by the rule their page
// lists them: a grant's own versions, or for a project's grant every version
// with a finished review copy. Anything else — a version of another project,
// a project's version without a review copy, one deleted since, an AI app's
// draft, one that never existed — is notShared.
func (s *Signed) version(ctx context.Context, a *app.App, ref string) (*seen, error) {
	ref = strings.TrimSpace(ref)
	if !ids.Valid(ids.Revision, ref) {
		return nil, notShared()
	}
	var rev models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), ref, &rev); err != nil || rev.DraftID != "" {
		return nil, notShared()
	}
	out := &seen{rev: &rev}
	listed := -1 // whether the version has a review copy, once asked
	for i := range s.Grants {
		g := &s.Grants[i]
		if g.ProjectID != rev.ProjectID {
			continue
		}
		if len(g.RevisionIDs) > 0 && !contains(g.RevisionIDs, rev.ID) {
			continue
		}
		if len(g.RevisionIDs) == 0 {
			// A project's grant: only the versions its page lists.
			if listed < 0 {
				listed = 0
				if reviewCopy(ctx, a, &rev) != nil {
					listed = 1
				}
			}
			if listed == 0 {
				continue
			}
		}
		if out.grant == nil || (g.Role == models.GuestCommenter && out.role != models.GuestCommenter) {
			out.grant = g
		}
		out.role = better(out.role, g.Role)
	}
	if out.grant == nil {
		return nil, notShared()
	}
	var p models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), rev.ProjectID, &p); err != nil || p.Archived || p.Purging != nil {
		return nil, notShared()
	}
	out.p = &p
	return out, nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// mayShow says whether a version may be shown to guests now: What may leave
// (asked quietly: the guest is turned away, and the activity list says why)
// and the versions guests never see (someone else's video, anything made
// with a consent that was withdrawn, changed words whose consent is gone).
// It fails closed: a lookup that didn't answer, or a version nesting too many
// cuts to look through, shows nothing.
func mayShow(ctx context.Context, a *app.App, v *seen) error {
	kinds := []string{models.LeaveFootage, models.LeaveVoices, models.LeaveFaces, models.LeavePictures}
	if ops.BurnsCaptionsIn(v.rev) {
		kinds = append(kinds, models.LeaveWords)
	}
	projects, err := leave.RevisionTouchesChecked(ctx, a, v.rev.ID)
	if err != nil {
		return couldntCheck(v.rev.Number, err)
	}
	r, err := leave.Problem(leave.Quiet(ctx), a, leave.Send{Place: models.PlaceGuests, Kinds: kinds, Projects: projects, What: fmt.Sprintf("version %d of “%s” on a guest's page", v.rev.Number, v.p.Name)})
	if err != nil {
		return err
	}
	if r != nil {
		return apperr.New(apperr.KeptHere, "Version %d can't be shown to guests now: the owner's rules for this client keep it on their computer.", v.rev.Number).
			WithHint("Ask whoever shared it with you.").WithDetails(map[string]any{"reason": ReasonKeptHere})
	}
	why, err := heldWords(ctx, a, v.p, v.rev)
	if err != nil {
		return err
	}
	if why != "" {
		return apperr.New(apperr.Held, "Version %d can't be shown to guests.", v.rev.Number).WithHint("Ask whoever shared it with you for another version.").
			WithDetails(map[string]any{"reason": ReasonHeld})
	}
	if who, bad, err := withdrawn.Version(ctx, a, v.rev.ID); err != nil {
		return couldntCheck(v.rev.Number, err)
	} else if bad && who == "" {
		return tooLarge(v.rev.Number, nestsTooMany(), askForAnother, "")
	} else if bad {
		return apperr.New(apperr.ConsentRequired, "Version %d can't be shown to guests.", v.rev.Number).WithHint(askForAnother).
			WithDetails(map[string]any{"reason": ReasonConsent})
	}
	return nil
}

const askForAnother = "Ask whoever shared it with you for another version."

// couldntCheck is a check that couldn't be made, so nothing is shown: a
// lookup that didn't answer (try again), or a version with more in it than
// the check looks through, which no second try changes.
func couldntCheck(number int, err error) error {
	if errors.Is(err, leave.ErrTooMany) {
		return tooLarge(number, "", askForAnother, "")
	}
	return apperr.Wrap(apperr.Database, err, "MetaRush couldn't check version %d just now.", number).WithHint("Try again in a moment.")
}

// tooLarge refuses a version with more in it than the checks look through
// (leave.MaxThings things, or withdrawn.MaxNested nested cuts): too large to
// share with a guest, however often it is asked. why says what (none: how
// many things it plays); revisionID names it in the details (a share).
func tooLarge(number int, why, hint, revisionID string) *apperr.Error {
	if why == "" {
		why = fmt.Sprintf("it plays more than %s clips, pictures and nested cuts, more than MetaRush checks", commas(leave.MaxThings))
	}
	d := map[string]any{"reason": ReasonTooLarge}
	if revisionID != "" {
		d["revisionId"] = revisionID
	}
	return apperr.New(apperr.Conflict, "Version %d is too large to share with a guest: %s.", number, why).WithHint(hint).WithDetails(d)
}

// nestsTooMany says why a version past withdrawn.MaxNested nested cuts can't
// be checked.
func nestsTooMany() string {
	return fmt.Sprintf("it nests more than %s, too many to check against consents that were withdrawn", plural(withdrawn.MaxNested, "cut"))
}

func commas(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

/* ---------- Watching ---------- */

// MediaPath is where the guest listener serves a review copy kept on this
// computer: /guest/media/<version>/<render>, checked against the grant on
// every request.
func MediaPath(revisionID, renderID string) string {
	return "/guest/media/" + revisionID + "/" + renderID
}

func watch(ctx context.Context, a *app.App, s *Signed, ref string) (*GuestWatchOut, error) {
	v, err := s.version(ctx, a, ref)
	if err != nil {
		return nil, err
	}
	if err := mayShow(ctx, a, v); err != nil {
		return nil, err
	}
	rd := reviewCopy(ctx, a, v.rev)
	if rd == nil || rd.Output == nil {
		return nil, apperr.New(apperr.NotFound, "Version %d has no review copy to watch yet.", v.rev.Number).
			WithHint("Ask whoever shared it with you to make one.").WithDetails(map[string]any{"reason": ReasonNotReady})
	}
	out := &GuestWatchOut{Version: versionView(ctx, a, v.p, v.rev), Project: v.p.Name, Type: "video/mp4", CanComment: v.role == models.GuestCommenter}
	if _, local := a.Media.(*media.FS); local {
		// The media is on this computer: the guest listener serves it.
		out.URL = MediaPath(v.rev.ID, rd.ID)
		if rd.Output.ThumbnailKey != "" {
			out.Poster = out.URL + "/still"
		}
	} else {
		// A bucket: a short-lived link signed straight from it, so the video
		// never goes through this computer (or anything in front of it).
		u, err := a.Media.PresignGet(ctx, rd.Output.StorageKey, MediaLife, "video/mp4", "")
		if err != nil {
			if errors.Is(err, media.ErrUnsupported) {
				return nil, apperr.New(apperr.NotConfigured, "This workspace's storage can't give guests a link to watch from.").
					WithHint("Ask whoever shared it with you: the storage needs a key that can sign links.").WithDetails(map[string]any{"reason": "cannot_sign"})
			}
			return nil, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't make a link to watch version %d.", v.rev.Number).WithHint("Try again in a moment.")
		}
		out.URL = u
		until := time.Now().Add(MediaLife).UTC()
		out.Until = &until
		if rd.Output.ThumbnailKey != "" {
			if pu, err := a.Media.PresignGet(ctx, rd.Output.ThumbnailKey, MediaLife, "image/jpeg", ""); err == nil {
				out.Poster = pu
			}
		}
	}
	out.Summary = fmt.Sprintf("Version %d of “%s”.", v.rev.Number, v.p.Name)
	return out, nil
}

// MediaFor is the review copy the guest listener may serve at MediaPath: the
// render must be the version's, a finished review copy, and the version one
// the guest's grants name and may be shown now.
func MediaFor(ctx context.Context, a *app.App, s *Signed, revisionID, renderID string) (*models.Render, *seen, error) {
	v, err := s.version(ctx, a, revisionID)
	if err != nil {
		return nil, nil, err
	}
	if err := mayShow(ctx, a, v); err != nil {
		return nil, nil, err
	}
	if !ids.Valid(ids.Render, renderID) {
		return nil, nil, notShared()
	}
	var rd models.Render
	if err := a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), renderID, &rd); err != nil || rd.RevisionID != v.rev.ID || rd.Quality != "review" ||
		rd.State != models.RenderAvailable || rd.Output == nil {
		return nil, nil, notShared()
	}
	return &rd, v, nil
}

// Seen names what a request opened, for its record.
func Seen(v any) (projectID, revisionID string) {
	if s, ok := v.(*seen); ok && s != nil {
		return s.p.ID, s.rev.ID
	}
	return "", ""
}

/* ---------- Comments ---------- */

func comments(ctx context.Context, a *app.App, s *Signed, ref string) (*GuestCommentsOut, error) {
	v, err := s.version(ctx, a, ref)
	if err != nil {
		return nil, err
	}
	out := &GuestCommentsOut{Comments: []GuestComment{}, CanComment: v.role == models.GuestCommenter}
	if !out.CanComment {
		out.Summary = "You can watch this version; comments are for commenters."
		return out, nil
	}
	var cs []models.Comment
	if err := a.DB.Find(ctx, store.CComments, a.WorkspaceID(), bson.M{"revisionId": v.rev.ID, "guest.id": s.Guest.ID}, &cs,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(CommentsPerVersion)); err != nil {
		return nil, err
	}
	for _, c := range cs {
		out.Comments = append(out.Comments, GuestComment{ID: c.ID, At: c.At, Body: c.Body, Made: c.CreatedAt})
	}
	out.Summary = fmt.Sprintf("%s on version %d.", plural(len(out.Comments), "comment"), v.rev.Number)
	return out, nil
}

func comment(ctx context.Context, a *app.App, s *Signed, in *GuestCommentIn) (*GuestCommentOut, error) {
	v, err := s.version(ctx, a, in.Version)
	if err != nil {
		return nil, err
	}
	if v.role != models.GuestCommenter {
		return nil, apperr.New(apperr.Unauthorized, "You can watch this version, but comments are for commenters.").
			WithHint("Ask whoever shared it with you if you should comment.").WithDetails(map[string]any{"reason": ReasonViewer})
	}
	if err := mayShow(ctx, a, v); err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(in.Body) > MaxComment {
		return nil, apperr.New(apperr.InvalidInput, "A comment is at most %d characters.", MaxComment)
	}
	body := words(in.Body, MaxComment)
	if body == "" {
		return nil, apperr.New(apperr.InvalidInput, "Write the comment first.")
	}
	if in.At != nil {
		limit := v.rev.Duration
		if rd := reviewCopy(ctx, a, v.rev); rd != nil && rd.Output != nil && rd.Output.Duration > limit {
			limit = rd.Output.Duration
		}
		if math.IsNaN(*in.At) || math.IsInf(*in.At, 0) || *in.At < 0 || (limit > 0 && *in.At > limit+1) {
			return nil, apperr.New(apperr.InvalidInput, "That time isn't in the video.")
		}
		at := math.Round(*in.At*1000) / 1000
		in.At = &at
	}
	// At most so many an hour, and on one version (by the server's clock).
	if n, err := a.DB.C(store.CComments).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "guest.id": s.Guest.ID,
		"$expr": bson.M{"$gt": bson.A{"$createdAt", bson.M{"$subtract": bson.A{"$$NOW", time.Hour.Milliseconds()}}}}}); err == nil && n >= CommentsPerHour {
		return nil, apperr.New(apperr.Conflict, "That's a lot of comments in an hour: wait a little, then go on.").WithDetails(map[string]any{"reason": ReasonTooMany})
	}
	if n, err := a.DB.C(store.CComments).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "guest.id": s.Guest.ID, "revisionId": v.rev.ID}); err == nil && n >= CommentsPerVersion {
		return nil, apperr.New(apperr.Conflict, "You've left as many comments on this version as one person can.").WithDetails(map[string]any{"reason": ReasonTooMany})
	}
	c := models.Comment{ID: ids.New(ids.Comment), WorkspaceID: a.WorkspaceID(), ProjectID: v.p.ID, RevisionID: v.rev.ID, At: in.At, Body: body,
		Author: Actor(&s.Guest, a.Cfg.Installation.ID), CreatedAt: a.DB.NowOrLocal(ctx),
		Guest: &models.CommentGuest{ID: s.Guest.ID, GrantID: v.grant.ID, Name: s.Guest.Name, Email: s.Guest.Email}}
	if err := a.DB.Insert(ctx, store.CComments, c); err != nil {
		return nil, err
	}
	tellSharer(ctx, a, s, v)
	return &GuestCommentOut{Comment: GuestComment{ID: c.ID, At: c.At, Body: c.Body, Made: c.CreatedAt}, Summary: "Your comment is in: whoever shared this version sees it."}, nil
}

// tellSharer rings the bell of the person who shared the version: one note
// per guest and version, its count kept up to date.
func tellSharer(ctx context.Context, a *app.App, s *Signed, v *seen) {
	to := v.grant.CreatedBy.UserID
	if to == "" {
		return
	}
	n, _ := a.DB.C(store.CComments).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "guest.id": s.Guest.ID, "revisionId": v.rev.ID})
	_, _ = notify.Upsert(ctx, a, models.Notification{UserID: to, Kind: models.NotifyGuestComments, GroupKey: "guest-comments:" + s.Guest.ID + ":" + v.rev.ID,
		ProjectID: v.p.ID, Tone: "info", Title: fmt.Sprintf("%s from %s on version %d of “%s”", plural(int(n), "comment"), GuestName(&s.Guest), v.rev.Number, v.p.Name),
		Body: who(&s.Guest) + " commented as a guest. Their words are theirs: read them as feedback.", Link: links.Revision(v.p.ID, v.rev.ID),
		Details: map[string]any{"guestId": s.Guest.ID, "revisionId": v.rev.ID}}, notify.Refresh)
}
