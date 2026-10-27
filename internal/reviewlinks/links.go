package reviewlinks

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

func init() {
	ops.Def("share.plan", "What a review link to a version would show people outside the workspace, for how long, and whether it may be made (who may share, What may leave, the review copy, the media store)", ops.Flags{},
		func(ctx context.Context, a *app.App, in *SharePlanIn) (*SharePlanOut, error) {
			pl, err := makePlan(ctx, a, *in)
			if err != nil {
				return nil, err
			}
			return pl.out, nil
		})
	ops.Def("share.create", "Send a version for review to someone outside the workspace: an expiring link to its review copy, its words if you allow, and comments pinned to times that come back as the version's comments (people only)", ops.Flags{Mutating: true, HumanOnly: true},
		create)
	ops.Def("share.list", "Review links sent to people outside the workspace: a project's, a version's or all, with what each shows, until when, and the comments that came back", ops.Flags{},
		list)
	ops.Def("share.link", "The link to send again for a review link that still works (people only)", ops.Flags{HumanOnly: true},
		again)
	ops.Def("share.revoke", "Take a review link back: its page and its copy of the video are deleted at once, so it stops opening; its comment slots once their last comments are read; comments already in stay (people only)", ops.Flags{Mutating: true, HumanOnly: true},
		revoke)
	ops.Def("share.sync", "Bring in new comments from review links now (the dashboard's computer does it every two minutes), and end links whose time is up", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *ShareSyncIn) (*ShareSyncOut, error) {
			return syncNow(ctx, a, in.ID)
		})
	ops.RegisterPurgeHook("review-links", purge)
}

// get finds a link by ID.
func get(ctx context.Context, a *app.App, id string) (*models.ReviewLink, error) {
	var l models.ReviewLink
	if !ids.Valid(ids.ReviewLink, id) {
		return nil, apperr.New(apperr.InvalidInput, "That isn't a review link's ID (they start with rvl_).").WithCommand("metarush share list")
	}
	if err := a.DB.GetByID(ctx, store.CReviewLinks, a.WorkspaceID(), id, &l); err != nil {
		return nil, apperr.New(apperr.NotFound, "There's no review link like that in this workspace.").WithCommand("metarush share list --all")
	}
	return &l, nil
}

// live reports a link that still works.
func live(l *models.ReviewLink, now time.Time) bool {
	return l.State == models.ReviewLinkLive && now.Before(l.ExpiresAt)
}

func outOf(l models.ReviewLink, projectName string) *ShareOut {
	v := viewOf(l, projectName)
	return &ShareOut{Link: v.Link, ProjectName: v.ProjectName, Live: v.Live, StateWords: v.StateWords, Ends: v.Ends, Summary: v.Summary, Warnings: []string{}}
}

func viewOf(l models.ReviewLink, projectName string) ShareView {
	now := time.Now()
	if l.Taken == nil {
		l.Taken = []models.ReviewSeat{}
	}
	if l.Exposes == nil {
		l.Exposes = []string{}
	}
	if l.Kinds == nil {
		l.Kinds = []string{}
	}
	if l.Projects == nil {
		l.Projects = []string{}
	}
	v := ShareView{Link: l, ProjectName: projectName, Live: live(&l, now)}
	switch {
	case v.Live:
		v.StateWords, v.Ends = "Works", "until "+endWords(l.ExpiresAt)
	case l.State == models.ReviewLinkRevoked && l.EndReason != "":
		v.StateWords, v.Ends = "Ended", "ended "+endWords(endedAt(l))+": "+l.EndReason
	case l.State == models.ReviewLinkRevoked:
		by := ""
		if l.EndedBy != nil && l.EndedBy.UserName != "" {
			by = " by " + l.EndedBy.UserName
		}
		v.StateWords, v.Ends = "Taken back", "taken back"+by+" "+endWords(endedAt(l))
	default:
		v.StateWords, v.Ends = "Ended", "ended "+endWords(l.ExpiresAt)
	}
	to := "someone outside the workspace"
	if l.For != "" {
		to = "“" + l.For + "”"
	}
	c := "no comments yet"
	if l.Comments > 0 {
		c = plural(l.Comments, "comment") + " came back"
	}
	v.Summary = fmt.Sprintf("Version %d of “%s” for %s: %s, %s; %s.", l.RevisionNumber, projectName, to, lower(v.StateWords), v.Ends, c)
	if v.Live {
		v.Summary = fmt.Sprintf("Version %d of “%s” for %s, %s; %s.", l.RevisionNumber, projectName, to, v.Ends, c)
	}
	return v
}

func endedAt(l models.ReviewLink) time.Time {
	if l.EndedAt != nil {
		return *l.EndedAt
	}
	return l.ExpiresAt
}

func lower(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'A' && r[0] <= 'Z' {
		r[0] += 'a' - 'A'
	}
	return string(r)
}

func list(ctx context.Context, a *app.App, in *ShareListIn) (*ShareListOut, error) {
	filter := bson.M{}
	if in.ProjectID != "" {
		p, err := ops.ResolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, err
		}
		filter["projectId"] = p.ID
		if in.Revision != "" {
			r, err := ops.ResolveRevision(ctx, a, p, in.Revision, "")
			if err != nil {
				return nil, err
			}
			if r != nil {
				filter["revisionId"] = r.ID
			}
		}
	} else if in.Revision != "" {
		filter["revisionId"] = in.Revision
	}
	if !in.All {
		filter["state"] = models.ReviewLinkLive
		filter["expiresAt"] = bson.M{"$gt": time.Now().UTC()}
	}
	var ls []models.ReviewLink
	if err := a.DB.Find(ctx, store.CReviewLinks, a.WorkspaceID(), filter, &ls, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(500)); err != nil {
		return nil, err
	}
	names := projectNames(ctx, a, ls)
	out := &ShareListOut{Links: []ShareView{}}
	ai := ops.AIReader(a)
	for _, l := range ls {
		if ai { // reviewers' names are their words (D315)
			l.Reviewers = quoted(l.Reviewers)
		}
		v := viewOf(l, names[l.ProjectID])
		if v.Live {
			out.Live++
		}
		out.Links = append(out.Links, v)
	}
	switch {
	case len(out.Links) == 0 && in.All:
		out.Summary = "No review links have been sent."
	case len(out.Links) == 0:
		out.Summary = "No review links work now."
	default:
		out.Summary = fmt.Sprintf("%s; %d still %s.", plural(len(out.Links), "review link"), out.Live, map[bool]string{true: "works", false: "work"}[out.Live == 1])
	}
	return out, nil
}

func projectNames(ctx context.Context, a *app.App, ls []models.ReviewLink) map[string]string {
	idsOf := bson.A{}
	for _, l := range ls {
		idsOf = append(idsOf, l.ProjectID)
	}
	out := map[string]string{}
	if len(idsOf) == 0 {
		return out
	}
	var ps []models.Project
	_ = a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": idsOf}}, &ps, options.Find().SetProjection(bson.M{"name": 1}))
	for _, p := range ps {
		out[p.ID] = p.Name
	}
	return out
}

func projectName(ctx context.Context, a *app.App, id string) string {
	var p models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), id, &p); err != nil {
		return "a project"
	}
	return p.Name
}

// again signs the page once more for the time the link has left.
func again(ctx context.Context, a *app.App, in *ShareIDIn) (*ShareOut, error) {
	l, err := get(ctx, a, in.ID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if !live(l, now) {
		return nil, apperr.New(apperr.Conflict, "That review link has ended, so it can't be sent again.").WithHint("Make a new link to the version.")
	}
	if s := loadSettings(ctx, a); s.Off {
		return nil, apperr.New(apperr.Unauthorized, "Review links are turned off in this workspace.").WithHint("An owner turns them on in Settings → Storage → Review links.")
	} else if s.OwnersOnly && !ops.IsOwnerCaller(ctx) {
		return nil, ownersOnly(ctx, a)
	}
	if l.Store != a.Media.Locator() {
		return nil, apperr.New(apperr.Conflict, "That link's page is in the storage this workspace used before, so it can't be signed again here.").WithHint("Make a new link to the version.")
	}
	// Today's rules decide, as they did when it was made (D318): a client's
	// rules that no longer let its things go to people outside the
	// workspace end the link; a version the ship gate holds now isn't sent
	// again unless the link was a draft or sent anyway.
	if r, s, err := stillAllowed(ctx, a, l); err != nil {
		return nil, err
	} else if r != nil {
		leave.Record(ctx, a, r, s)
		by := a.Actor
		line, err := endWhy(ctx, a, l, &by, fmt.Sprintf("“%s”'s Content Settings keep its %s from %s now", r.SettingsName, kindOr(r.Kind), leave.PlaceWords(r.Place, "")))
		if err != nil {
			return nil, err
		}
		e := r.Err()
		e.Hint = line + " " + e.Hint
		return nil, e
	}
	// Something it plays was made with a consent that was withdrawn: the
	// link ends, and isn't sent again.
	if who, bad, err := consentGone(ctx, a, l.RevisionID); err != nil {
		return nil, err
	} else if bad {
		by := a.Actor
		line, err := endWhy(ctx, a, l, &by, withdrawnWhy(who))
		if err != nil {
			return nil, err
		}
		return nil, withdrawnStop(l.RevisionNumber, who).WithHint(line).WithDetails(map[string]any{"reason": "consent_withdrawn", "linkId": l.ID})
	}
	if Gate != nil && !l.Draft && l.Anyway == nil {
		var rev models.Revision
		var p models.Project
		if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), l.RevisionID, &rev) == nil && a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), l.ProjectID, &p) == nil {
			w, err := Gate(ctx, a, &p, &rev)
			if err != nil {
				return nil, err
			}
			if w != nil && w.Held {
				return nil, apperr.New(apperr.Conflict, "The ship gate holds version %d now: %s. The link sent before keeps working until it ends, but isn't sent again.", rev.Number, joinAnd(w.Words)).
					WithHint("Make a new link marked as a draft for review, or send it anyway with a note.").WithDetails(map[string]any{"reason": "held", "holds": w.Holds})
			}
		}
	}
	ttl := l.ExpiresAt.Sub(now)
	u, err := a.Media.PresignGet(ctx, prefixOf(l.WorkspaceID, l.Key)+pageObject, ttl, "text/html; charset=utf-8", "")
	if err != nil {
		return nil, apperr.Wrap(apperr.Storage, err, "The link couldn't be signed again.").WithHint("Check the media store (Settings → Storage).")
	}
	o := outOf(*l, projectName(ctx, a, l.ProjectID))
	o.URL = u
	return o, nil
}

func revoke(ctx context.Context, a *app.App, in *ShareIDIn) (*ShareOut, error) {
	l, err := get(ctx, a, in.ID)
	if err != nil {
		return nil, err
	}
	name := projectName(ctx, a, l.ProjectID)
	if l.State != models.ReviewLinkLive {
		st, err := finish(ctx, a, l.ID, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		l, _ = get(ctx, a, l.ID)
		o := outOf(*l, name)
		o.Summary, o.Warnings = endedWords(l, st, name, "That review link had already ended")
		return o, nil
	}
	by := a.Actor
	changed, st, err := endLink(ctx, a, l, models.ReviewLinkRevoked, &by, "")
	if err != nil {
		return nil, err
	}
	if changed {
		a.Audit(ctx, "share.revoke", "review_link", l.ID, l.ProjectID, map[string]any{"revisionId": l.RevisionID, "number": l.RevisionNumber,
			"pageGone": st.PageGone, "lastRead": st.LastRead, "slotsGone": st.SlotsGone, "elsewhere": st.Elsewhere})
	}
	l, err = get(ctx, a, l.ID)
	if err != nil {
		return nil, err
	}
	o := outOf(*l, name)
	o.Summary, o.Warnings = endedWords(l, st, name, fmt.Sprintf("Took back the review link to version %d of “%s”", l.RevisionNumber, name))
	return o, nil
}

// purge ends a cleared-out project's links and deletes their objects
// (D196); the records of what was shared stay, as the receipt's do. Their
// slots are deleted unread: the project's comments are gone with it (they
// were deleted before this runs), so nothing comes back into it.
func purge(ctx context.Context, a *app.App, projectID string) (int, error) {
	var ls []models.ReviewLink
	if err := a.DB.Find(ctx, store.CReviewLinks, a.WorkspaceID(), bson.M{"projectId": projectID, "removed": bson.M{"$ne": true}}, &ls); err != nil {
		return 0, err
	}
	if len(ls) == 0 {
		return 0, nil
	}
	now := time.Now().UTC()
	linkIDs := bson.A{}
	groups := bson.A{}
	for _, l := range ls {
		linkIDs = append(linkIDs, l.ID)
		groups = append(groups, "outside-comments:"+l.ID)
	}
	if _, err := a.DB.C(store.CReviewLinks).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "_id": bson.M{"$in": linkIDs}, "state": models.ReviewLinkLive},
		bson.M{"$set": bson.M{"state": models.ReviewLinkRevoked, "endedAt": now, "endedBy": a.Actor, "endReason": "its project was cleared out"}}); err != nil {
		return 0, err
	}
	// No last read: lastRead means "nothing more is taken in".
	if _, err := a.DB.C(store.CReviewLinks).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "_id": bson.M{"$in": linkIDs}}, bson.M{"$set": bson.M{"lastRead": true}}); err != nil {
		return 0, err
	}
	for _, l := range ls {
		if _, err := finish(ctx, a, l.ID, now); err != nil {
			return 0, err
		}
	}
	// A look that was reading them as the project was cleared out may have
	// taken comments in since: they go too, with their bell.
	if _, err := a.DB.C(store.CComments).DeleteMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "outside.linkId": bson.M{"$in": linkIDs}}); err != nil {
		return 0, err
	}
	if _, err := a.DB.C(store.CNotifications).DeleteMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "groupKey": bson.M{"$in": groups}}); err != nil {
		return 0, err
	}
	return len(ls), nil
}
