package reviewlinks

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcripts"
)

// plan is a link worked out before anything is made: the version and its
// review copy, what the page would show, and the first thing that stops it.
type plan struct {
	p      *models.Project
	rev    *models.Revision
	render *models.Render
	in     ShareCreateIn
	days   int
	lines  []transcripts.Cue
	// projects are whose things the version shows; sends what leaves.
	projects []string
	sends    []leave.Send
	gate     *GateWord
	out      *SharePlanOut
	stop     *apperr.Error
}

// stopWith keeps the first thing that stops the link.
func (pl *plan) stopWith(e *apperr.Error) {
	if pl.stop == nil {
		pl.stop = e
	}
}

// makePlan works a link out. Nothing is recorded: refusals of What may
// leave are asked quietly here and recorded only when a person makes it.
func makePlan(ctx context.Context, a *app.App, in ShareCreateIn) (*plan, error) {
	in.Title, in.For, in.Message, in.Anyway = oneLine(in.Title, 120), oneLine(in.For, 80), words(in.Message, 1000), words(in.Anyway, 500)
	pl := &plan{in: in}
	p, err := ops.ResolveProject(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	pl.p = p
	out := &SharePlanOut{ProjectID: p.ID, ProjectName: p.Name, Exposes: []string{}, Kinds: []string{}, Holds: []string{}, Warnings: []string{}}
	pl.out = out
	if p.Archived {
		pl.stopWith(apperr.New(apperr.Conflict, "“%s” is archived, so none of it is shared.", p.Name).WithHint("Restore the project first."))
	}
	rev, err := ops.ResolveRevision(ctx, a, p, in.Revision, in.Branch)
	if err != nil {
		return nil, err
	}
	if rev == nil {
		pl.stopWith(apperr.New(apperr.NotFound, "“%s” has no version yet, so there's nothing to share.", p.Name).WithHint("Make a cut first."))
		return pl.finish(ctx, a), nil
	}
	pl.rev = rev
	out.RevisionID, out.RevisionNumber = rev.ID, rev.Number
	out.Approved = rev.Approval.State == models.ApprovalApproved

	// Who may share, and for how long (the owners' word).
	set := loadSettings(ctx, a)
	out.MaxDays = set.maxDays()
	switch {
	case set.Off:
		pl.stopWith(apperr.New(apperr.Unauthorized, "Review links are turned off in this workspace.").
			WithHint("An owner turns them on in Settings → Storage → Review links.").WithDetails(map[string]any{"reason": "share_off"}).WithCommand("metarush share settings --on"))
	case set.OwnersOnly && !ops.IsOwnerCaller(ctx):
		pl.stopWith(ownersOnly(ctx, a))
	}
	pl.days = in.Days
	if pl.days == 0 {
		pl.days = out.MaxDays
	}
	switch {
	case pl.days < 0:
		pl.stopWith(apperr.New(apperr.InvalidInput, "Say how many days the link works, from 1 to %d.", out.MaxDays))
	case pl.days > out.MaxDays:
		pl.stopWith(apperr.New(apperr.InvalidInput, "A review link works for at most %s here.", plural(out.MaxDays, "day")).
			WithHint(maxDaysHint(out.MaxDays)))
		pl.days = out.MaxDays
	}
	out.Days = max(pl.days, 1)
	out.Ends = "until " + endWords(time.Now().Add(time.Duration(out.Days)*24*time.Hour))

	// Where the page is kept.
	kept := leave.MediaKept(a.Cfg.Media)
	out.Store = kept.Words
	if why := media.CanShare(a.Media); why != "" {
		pl.stopWith(apperr.New(apperr.NotConfigured, "%s A review link's page is kept in the workspace's cloud bucket, so this workspace can't make one.", why).
			WithHint("Move the media to a cloud bucket (Settings → Storage), or send the video yourself: download the review copy from the version's page.").
			WithDetails(map[string]any{"reason": "store_cannot_share", "store": a.Media.Name()}))
	} else if host := mediaHost(a); host != "" && nearby(host) {
		out.Warnings = append(out.Warnings, "This workspace's media is on a storage server on your own network or this computer, so only people who can reach it can open the link.")
	}
	// A link made while the media moves would be left in the storage being
	// left, where nothing here could take it back (D318).
	if ops.MoveRunningHere(ctx, a) {
		pl.stopWith(apperr.New(apperr.Conflict, "The media is being moved to another storage on this computer, so no review link can be made until the move is done.").
			WithHint("Make the link once the move has finished.").WithDetails(map[string]any{"reason": "media_moving"}))
	}

	// The review copy it plays (if it has none, that is said after what
	// would stop the link anyway: making one first would be in vain).
	pl.render = reviewCopy(ctx, a, rev.ID)
	if pl.render != nil {
		out.ReviewCopy = pl.render.ID
	}

	// What it shows.
	out.Title = in.Title
	if out.Title == "" {
		name := strings.TrimSpace(rev.Spec.Title)
		if name == "" {
			name = p.Name
		}
		out.Title = oneLine(fmt.Sprintf("%s — version %d", name, rev.Number), 120)
	}
	switch in.Speakers {
	case "":
		pl.in.Speakers = models.SpeakersLabels
	case models.SpeakersNames, models.SpeakersLabels, models.SpeakersNone:
	default:
		return nil, apperr.New(apperr.InvalidInput, "Say how the words name who speaks: names, labels or none.")
	}
	if in.Words {
		lines, err := ops.VersionLines(ctx, a, rev)
		if err != nil {
			return nil, err
		}
		pl.lines = lines
		out.HasWords = len(lines) > 0
		if !out.HasWords {
			out.Warnings = append(out.Warnings, fmt.Sprintf("Version %d has no words to show, so the page shows the video only.", rev.Number))
		}
	}
	out.Kinds = append(out.Kinds, models.LeaveFootage, models.LeaveVoices, models.LeaveFaces, models.LeavePictures)
	out.Exposes = append(out.Exposes, fmt.Sprintf("version %d's review copy: its picture and sound", rev.Number))
	if out.HasWords || ops.BurnsCaptionsIn(rev) {
		out.Kinds = append(out.Kinds, models.LeaveWords)
	}
	if out.HasWords {
		switch pl.in.Speakers {
		case models.SpeakersNames:
			out.Exposes = append(out.Exposes, "what is said, with the names of who speaks")
		case models.SpeakersLabels:
			out.Exposes = append(out.Exposes, "what is said, with who speaks as Speaker 1, 2…")
		default:
			out.Exposes = append(out.Exposes, "what is said, without who speaks")
		}
	}
	if in.Download {
		out.Exposes = append(out.Exposes, "a download of the video")
	}
	out.Exposes = append(out.Exposes, "the title “"+out.Title+"”")
	if in.Message != "" {
		out.Exposes = append(out.Exposes, "a note from who sent it")
	}
	if name := strings.TrimSpace(a.Actor.UserName); name != "" {
		out.Exposes = append(out.Exposes, "the name of who sent it ("+name+")")
	}
	out.Kinds = ordered(out.Kinds)

	// Whose things they are, and whether they may go.
	pl.projects = leave.RevisionTouches(ctx, a, rev.ID)
	if len(pl.projects) == 0 {
		pl.projects = []string{p.ID}
	}
	what := fmt.Sprintf("version %d of “%s” through a review link", rev.Number, p.Name)
	pl.sends = []leave.Send{{Place: models.PlaceReviewers, ToWords: "people outside the workspace", Kinds: out.Kinds, Projects: pl.projects, What: what}}
	if kept.Cloud {
		pl.sends = append(pl.sends, leave.Send{Place: models.PlaceStorage, To: "cloud", ToWords: kept.Words, Kinds: out.Kinds, Projects: pl.projects, What: what})
	}
	for _, s := range pl.sends {
		r, err := leave.Problem(ctx, a, s)
		if err != nil {
			return nil, err
		}
		if r != nil {
			pl.stopWith(r.Err())
			break
		}
	}

	// Something it plays was made with a consent that was withdrawn (D227):
	// it isn't shared, draft or not.
	if who, bad, err := consentGone(ctx, a, rev.ID); err != nil {
		return nil, err
	} else if bad {
		pl.stopWith(withdrawnStop(rev.Number, who).WithHint("Make a version without it first.").WithDetails(map[string]any{"reason": "consent_withdrawn", "revisionId": rev.ID}))
	}

	// The ship gate (D298): a held version goes out marked as a draft, or
	// with a person's note.
	if Gate != nil {
		w, err := Gate(ctx, a, p, rev)
		if err != nil {
			return nil, err
		}
		if w != nil && len(w.Consent) > 0 {
			out.Held, out.Holds = true, nonNil(w.Words)
			pl.stopWith(apperr.New(apperr.ConsentRequired, "Version %d can't be shared: %s.", rev.Number, joinAnd(w.Consent)).
				WithHint("Take the changed words out of the cut and make a new version first.").WithDetails(map[string]any{"reason": "consent_withdrawn", "revisionId": rev.ID}))
		} else if w != nil && w.Held {
			pl.gate = w
			out.Held, out.Holds = true, nonNil(w.Words)
			if !in.AsDraft && in.Anyway == "" {
				pl.stopWith(apperr.New(apperr.Conflict, "The ship gate holds version %d: %s.", rev.Number, joinAnd(w.Words)).
					WithHint("Share it marked as a draft for review, or send it anyway with a note saying why.").
					WithDetails(map[string]any{"reason": "held", "holds": w.Holds}))
			}
		}
	}
	if pl.render == nil {
		e := apperr.New(apperr.Conflict, "Version %d has no review copy yet, so there's nothing to show.", rev.Number).
			WithHint("Make a review copy of it first, then share it.").WithDetails(map[string]any{"reason": "no_review_copy", "revisionId": rev.ID}).
			WithCommand("metarush render submit --revision " + rev.ID + " --quality review --wait")
		if making(ctx, a, rev.ID) {
			e = apperr.New(apperr.Conflict, "Version %d's review copy is still being made.", rev.Number).
				WithHint("Share it once it's ready.").WithDetails(map[string]any{"reason": "review_copy_making", "revisionId": rev.ID})
		}
		pl.stopWith(e)
	}
	return pl.finish(ctx, a), nil
}

// finish says it in words.
func (pl *plan) finish(ctx context.Context, a *app.App) *plan {
	out := pl.out
	if pl.stop != nil {
		out.May, out.Why, out.Code, out.Hint = false, pl.stop.Message, pl.stop.Code, pl.stop.Hint
		out.Summary = pl.stop.Message
		return pl
	}
	out.May = true
	out.Summary = fmt.Sprintf("A link to version %d of “%s” would show %s, %s.", out.RevisionNumber, out.ProjectName, joinAnd(out.Exposes), out.Ends)
	return pl
}

// ownersOnly is the refusal when only owners share.
func ownersOnly(ctx context.Context, a *app.App) *apperr.Error {
	var names []string
	if ops.OwnerNames != nil {
		names = ops.OwnerNames(ctx, a)
	}
	hint := "Ask an owner to share it, or to let everyone share in Settings → Storage → Review links."
	if len(names) > 0 {
		hint = "Ask " + joinOr(names) + " to share it, or to let everyone share in Settings → Storage → Review links."
	}
	return apperr.New(apperr.Unauthorized, "Only owners share with people outside the workspace here.").WithHint(hint).
		WithDetails(map[string]any{"reason": "owners_share", "owners": nonNil(names)})
}

func maxDaysHint(n int) string {
	if n < MaxDays {
		return fmt.Sprintf("Owners set the longest to %s (Settings → Storage → Review links). Make a new link when it ends.", plural(n, "day"))
	}
	return "A cloud bucket signs a link for a week at most. Make a new link when it ends."
}

// reviewCopy is a version's newest finished review copy.
func reviewCopy(ctx context.Context, a *app.App, revisionID string) *models.Render {
	var rs []models.Render
	if err := a.DB.Find(ctx, store.CRenders, a.WorkspaceID(), bson.M{"revisionId": revisionID, "quality": "review", "state": models.RenderAvailable}, &rs,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(1)); err != nil || len(rs) == 0 || rs[0].Output == nil {
		return nil
	}
	return &rs[0]
}

// making reports a review copy of the version on its way.
func making(ctx context.Context, a *app.App, revisionID string) bool {
	n, err := a.DB.C(store.CRenders).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "revisionId": revisionID, "quality": "review",
		"state": bson.M{"$in": bson.A{models.RenderQueued, models.RenderRendering, models.RenderUploading}}})
	return err == nil && n > 0
}

// mediaHost is the media store's host when it is S3-compatible ("" else).
func mediaHost(a *app.App) string {
	if a.Cfg.Media.Provider != "s3" {
		return ""
	}
	return storeHost(a.Cfg.Media.S3.Endpoint)
}

// nearby reports a host on this computer or a private network.
func nearby(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".ts.net") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

// storeHost is the media store's host when it is S3-compatible.
func storeHost(endpoint string) string {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// oneLine is a person's short text: one line, cleaned, at most n characters.
func oneLine(s string, n int) string {
	s = ops.CleanText(strings.ToValidUTF8(s, ""))
	s = strings.Join(strings.Fields(s), " ")
	return cut(s, n)
}

// words is a person's longer text: cleaned, trimmed, at most n characters.
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

// endWords is a time a link ends, as people read it.
func endWords(t time.Time) string { return t.Local().Format("2 Jan 2006, 15:04") }

func ordered(kinds []string) []string {
	out := []string{}
	for _, k := range models.LeaveKinds {
		for _, x := range kinds {
			if x == k {
				out = append(out, k)
				break
			}
		}
	}
	return out
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func joinAnd(xs []string) string { return join(xs, "and") }
func joinOr(xs []string) string  { return join(xs, "or") }

func join(xs []string, word string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	case 2:
		return xs[0] + " " + word + " " + xs[1]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " " + word + " " + xs[len(xs)-1]
}
