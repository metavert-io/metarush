package ops

import (
	"context"
	"errors"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/store"
)

// A review copy follows the latest version (D187). Once a cut's latest version
// has been left alone for ReviewFollowQuiet (no newer version: hand edits save
// one every couple of seconds while someone works), the daemon queues a review
// copy of it, unless that version has one or one is on its way. A newer
// version replaces an older version's copy that hasn't started (never one that
// is rendering). The job key is the one render.submit uses for that version,
// shape and quality, so several daemons sharing a database never queue it
// twice and a person asking for it gets the same render. Only the daemon does
// this (RunReviewFollow); terminal commands never do.
var (
	// ReviewFollowQuiet is how long a cut stays unchanged before its review copy is queued.
	ReviewFollowQuiet = time.Minute
	// ReviewFollowWindow: only versions saved this recently are followed, so
	// turning it on (or upgrading) never queues copies of every older cut.
	ReviewFollowWindow = 2 * time.Hour
	// ReviewFollowEvery is how often the daemon looks.
	ReviewFollowEvery = 15 * time.Second
)

// FollowSetIn turns it on or off.
type FollowSetIn struct {
	On bool `json:"on" jsonschema:"description=true: a review copy follows each cut's latest version (the default); false: review copies only when someone asks."`
}

// FollowOut is the setting and what it is making now.
type FollowOut struct {
	On           bool          `json:"on"`
	QuietSeconds int           `json:"quietSeconds" jsonschema:"description=How long a cut stays unchanged before MetaRush makes its review copy."`
	UpdatedAt    *time.Time    `json:"updatedAt,omitempty"`
	UpdatedBy    *models.Actor `json:"updatedBy,omitempty"`
	// Making are the review copies MetaRush is making by itself right now.
	Making []models.Render `json:"making"`
}

// FollowResult is what one look did.
type FollowResult struct {
	Queued     []models.Render `json:"queued"`
	Superseded []models.Render `json:"superseded"`
}

func init() {
	Def("render.follow.show", "Whether a review copy follows each cut's latest version (made by itself about a minute after the last change), and the copies it is making now", Flags{}, func(ctx context.Context, a *app.App, _ *EmptyIn) (*FollowOut, error) {
		return followOut(ctx, a)
	})
	Def("render.follow.set", "Turn the review copy that follows each cut's latest version on (the default) or off", Flags{Mutating: true, OwnerOnly: true}, func(ctx context.Context, a *app.App, in *FollowSetIn) (*FollowOut, error) {
		set := models.ReviewFollowSettings{Off: !in.On, UpdatedAt: time.Now().UTC(), UpdatedBy: a.Actor}
		if err := a.DB.Update(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), bson.M{"$set": bson.M{"reviewFollow": set}}); err != nil {
			return nil, err
		}
		a.Audit(ctx, "render.follow.set", "workspace", a.WorkspaceID(), "", map[string]any{"on": in.On})
		if OnSettingsChanged != nil {
			OnSettingsChanged()
		}
		return followOut(ctx, a)
	})
}

func followSettings(ctx context.Context, a *app.App) *models.ReviewFollowSettings {
	var ws models.Workspace
	if err := a.DB.GetByID(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), &ws); err != nil {
		return nil
	}
	return ws.ReviewFollow
}

// FollowOn reports whether review copies follow the latest versions (on unless a person turned it off).
func FollowOn(ctx context.Context, a *app.App) bool {
	s := followSettings(ctx, a)
	return s == nil || !s.Off
}

func followOut(ctx context.Context, a *app.App) (*FollowOut, error) {
	out := &FollowOut{On: true, QuietSeconds: int(ReviewFollowQuiet / time.Second), Making: []models.Render{}}
	if s := followSettings(ctx, a); s != nil {
		out.On = !s.Off
		at, by := s.UpdatedAt, s.UpdatedBy
		out.UpdatedAt, out.UpdatedBy = &at, &by
	}
	if err := a.DB.Find(ctx, store.CRenders, a.WorkspaceID(), bson.M{"follow": true, "state": bson.M{"$in": bson.A{models.RenderQueued, models.RenderRendering, models.RenderUploading}}},
		&out.Making, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(50)); err != nil {
		return nil, err
	}
	if out.Making == nil {
		out.Making = []models.Render{}
	}
	return out, nil
}

// RunReviewFollow is the daemon's share of D187: every ReviewFollowEvery it
// queues the review copies of cuts that have been quiet long enough.
func RunReviewFollow(ctx context.Context, a *app.App, logf func(string, ...any)) {
	t := time.NewTimer(ReviewFollowEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		res, err := FollowLatest(ctx, a, time.Now().UTC())
		if err != nil && ctx.Err() == nil {
			logf("making review copies of the latest versions failed: %v", err)
		}
		if res != nil {
			for _, r := range res.Queued {
				logf("making a review copy of version %d by itself (it follows the latest version)", r.RevisionNumber)
			}
			for _, r := range res.Superseded {
				logf("version %d's review copy hadn't started, so a newer version's replaces it", r.RevisionNumber)
			}
		}
		t.Reset(ReviewFollowEvery)
	}
}

// FollowLatest looks once: for each cut whose latest version was saved
// between ReviewFollowWindow and ReviewFollowQuiet ago, it queues that
// version's review copy (D187).
func FollowLatest(ctx context.Context, a *app.App, now time.Time) (*FollowResult, error) {
	res := &FollowResult{Queued: []models.Render{}, Superseded: []models.Render{}}
	if !FollowOn(ctx, a) {
		return res, nil
	}
	var ps []models.Project
	if err := a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), bson.M{"archived": false, "updatedAt": bson.M{"$gte": now.Add(-ReviewFollowWindow)}}, &ps,
		options.Find().SetSort(bson.D{{Key: "updatedAt", Value: -1}}).SetLimit(200)); err != nil {
		return res, err
	}
	var errs []error
	for i := range ps {
		p := &ps[i]
		branches := make([]string, 0, len(p.Branches))
		for b := range p.Branches {
			branches = append(branches, b)
		}
		sort.Strings(branches)
		for _, b := range branches {
			head := p.Branches[b]
			if head.Head == "" || head.UpdatedAt.Before(now.Add(-ReviewFollowWindow)) {
				continue
			}
			var r models.Revision
			if err := a.DB.C(store.CRevisions).FindOne(ctx, bson.M{"_id": head.Head, "workspaceId": a.WorkspaceID()}, options.FindOne().SetProjection(bson.M{"ops": 0})).Decode(&r); err != nil {
				if !errors.Is(err, mongo.ErrNoDocuments) {
					errs = append(errs, err)
				}
				continue
			}
			if r.CreatedAt.After(now.Add(-ReviewFollowQuiet)) || r.CreatedAt.Before(now.Add(-ReviewFollowWindow)) || len(r.Spec.Clips) == 0 {
				continue // still being edited, too old, or nothing to play
			}
			if err := followOne(ctx, a, &r, res); err != nil {
				errs = append(errs, err)
			}
		}
	}
	// AI apps' drafts of new versions (D249) get theirs too: they wait for a
	// person, who should see the real video.
	var ds []models.Draft
	if err := a.DB.Find(ctx, store.CDrafts, a.WorkspaceID(), bson.M{"state": models.DraftWaiting, "kind": models.DraftVersion,
		"createdAt": bson.M{"$gte": now.Add(-ReviewFollowWindow), "$lte": now.Add(-ReviewFollowQuiet)}}, &ds,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(100)); err != nil {
		errs = append(errs, err)
	}
	copies := map[string]int{} // follow copies of each app's drafts in the last hour
	for _, d := range ds {
		n, seen := copies[d.Agent]
		if !seen {
			n = draftCopiesLastHour(ctx, a, d.Agent, now)
		}
		if n >= DraftCopiesPerHour {
			continue // that app's drafts wait for the next hour (or a person's ask)
		}
		var r models.Revision
		if err := a.DB.C(store.CRevisions).FindOne(ctx, bson.M{"_id": d.RevisionID, "workspaceId": a.WorkspaceID()}, options.FindOne().SetProjection(bson.M{"ops": 0})).Decode(&r); err != nil {
			if !errors.Is(err, mongo.ErrNoDocuments) {
				errs = append(errs, err)
			}
			continue
		}
		if len(r.Spec.Clips) == 0 {
			continue
		}
		before := len(res.Queued)
		if err := followOne(ctx, a, &r, res); err != nil {
			errs = append(errs, err)
		}
		copies[d.Agent] = n + len(res.Queued) - before
	}
	return res, errors.Join(errs...)
}

// DraftCopiesPerHour is how many review copies MetaRush makes by itself of one
// app's drafts in an hour.
var DraftCopiesPerHour = 12

// draftCopiesLastHour counts the review copies MetaRush made by itself of an
// app's drafts in the hour before now.
func draftCopiesLastHour(ctx context.Context, a *app.App, agent string, now time.Time) int {
	var ds []models.Draft
	if err := a.DB.Find(ctx, store.CDrafts, a.WorkspaceID(), bson.M{"agent": agent, "kind": models.DraftVersion, "createdAt": bson.M{"$gte": now.Add(-ReviewFollowWindow - time.Hour)}}, &ds,
		options.Find().SetProjection(bson.M{"revisionId": 1})); err != nil || len(ds) == 0 {
		return 0
	}
	revs := bson.A{}
	for _, d := range ds {
		revs = append(revs, d.RevisionID)
	}
	n, _ := a.DB.C(store.CRenders).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "follow": true, "revisionId": bson.M{"$in": revs},
		"createdAt": bson.M{"$gte": now.Add(-time.Hour)}})
	return int(n)
}

// followOne queues the review copy of one cut's latest version r, unless it
// has one, had one asked for before, or would be refused.
func followOne(ctx context.Context, a *app.App, r *models.Revision, res *FollowResult) error {
	n, err := a.DB.C(store.CRenders).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "revisionId": r.ID, "quality": "review",
		"state": bson.M{"$in": bson.A{models.RenderQueued, models.RenderRendering, models.RenderUploading, models.RenderAvailable}}})
	if err != nil || n > 0 {
		return err
	}
	// Asked for once (by anyone, with any outcome): never again by itself.
	err = a.DB.C(store.CJobs).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "idempotencyKey": renderKey(r.ID, r.Spec.Output.Preset, "review")}).Err()
	if err == nil {
		return nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return err
	}
	// A graphic nobody approved would stop the render: no copy until a person decides.
	if hashes := editspec.MotionHashes(&r.Spec); len(hashes) > 0 {
		infos, err := motion.Infos(ctx, a.DB, a.WorkspaceID(), hashes)
		if err != nil {
			return err
		}
		for _, h := range hashes {
			if infos[h].State != models.MotionApproved {
				return nil
			}
		}
	}
	if err := supersede(ctx, a, r, res); err != nil {
		return err
	}
	// MetaRush makes it, for the person who saved the version (their bell
	// hears if it fails or its quality check finds something).
	fa := *a
	fa.Actor = models.Actor{UserID: r.CreatedBy.UserID, UserName: r.CreatedBy.UserName, InstallationID: a.Cfg.Installation.ID, Via: "worker"}
	out, err := submitRender(ctx, &fa, r, &RenderSubmitIn{RevisionID: r.ID, Quality: "review"}, true)
	var ae *apperr.Error
	if errors.As(err, &ae) && ae.Code != apperr.Internal && ae.Code != apperr.Storage {
		return nil // refused (a Remotion clip nobody approved, asked for at the same moment): nothing to do by itself
	}
	if err != nil {
		return err
	}
	if out.Created {
		res.Queued = append(res.Queued, out.Render)
	}
	return nil
}

// supersede cancels the review copies MetaRush queued by itself for older
// versions of r's cut that haven't started: the newest version's replaces
// them. A copy a worker already renders is never touched.
func supersede(ctx context.Context, a *app.App, r *models.Revision, res *FollowResult) error {
	if r.DraftID != "" {
		return nil // an AI app's draft is beside the cut: it replaces no one's copy
	}
	var olds []models.Render
	if err := a.DB.Find(ctx, store.CRenders, a.WorkspaceID(), bson.M{"projectId": r.ProjectID, "follow": true, "state": models.RenderQueued, "revisionId": bson.M{"$ne": r.ID}}, &olds); err != nil {
		return err
	}
	for _, o := range olds {
		if o.RevisionNumber >= r.Number {
			continue
		}
		var ov models.Revision
		if err := a.DB.C(store.CRevisions).FindOne(ctx, bson.M{"_id": o.RevisionID, "workspaceId": a.WorkspaceID()}, options.FindOne().SetProjection(bson.M{"branch": 1, "draftId": 1})).Decode(&ov); err != nil || ov.Branch != r.Branch || ov.DraftID != "" {
			continue // another cut's, or a draft's (drafts keep theirs)
		}
		ok, err := a.Queue.CancelQueued(ctx, o.JobID)
		if err != nil {
			return err
		}
		if !ok {
			continue // it started meanwhile: it finishes
		}
		_, _ = a.DB.C(store.CRenders).UpdateOne(ctx, bson.M{"_id": o.ID, "workspaceId": a.WorkspaceID(), "state": models.RenderQueued},
			bson.M{"$set": bson.M{"state": models.RenderCanceled}})
		o.State = models.RenderCanceled
		res.Superseded = append(res.Superseded, o)
	}
	return nil
}
