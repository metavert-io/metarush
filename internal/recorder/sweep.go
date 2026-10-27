package recorder

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/store"
)

// The pieces of a recording nobody finished don't stay forever (D258): once
// it has been untouched (no piece, no finish, no change) for
// recordings.keepDays (14 by default) less two days, its maker is told on the
// bell; at keepDays, and at least a day after being told, its pieces are
// removed and it is marked thrown away ("expired"). A live recording, one
// being added and footage are never touched.

// ExpiryWarning is how long before removal the maker is told.
const ExpiryWarning = 48 * time.Hour

// Sweep runs the expiry on this computer's recordings now and then until ctx
// ends. as gives the app acting as a recording's person.
func Sweep(ctx context.Context, a *app.App, as func(userID, userName string) *app.App) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		sweepOnce(ctx, a, as, time.Now())
		t.Reset(time.Hour)
	}
}

// sweepOnce tells makers and removes pieces as of now; it says how many it told and removed.
func sweepOnce(ctx context.Context, a *app.App, as func(userID, userName string) *app.App, now time.Time) (told, removed int) {
	keep := a.Cfg.Recordings.Keep()
	warnAfter := keep - ExpiryWarning
	if warnAfter < keep/2 {
		warnAfter = keep / 2
	}
	var list []models.Recording
	if err := a.DB.Find(ctx, store.CRecordings, a.WorkspaceID(), bson.M{"installationId": a.Cfg.Installation.ID,
		"state": bson.M{"$in": bson.A{models.RecordingRecording, models.RecordingFailed}}}, &list); err != nil {
		return 0, 0
	}
	for i := range list {
		r := &list[i]
		touched := r.LastPieceAt
		if r.UpdatedAt.After(touched) {
			touched = r.UpdatedAt
		}
		idle := now.Sub(touched)
		if idle < warnAfter {
			continue
		}
		if r.ExpiryNoticeAt == nil || r.ExpiryNoticeAt.Before(touched) {
			days := int((keep - idle + 23*time.Hour) / (24 * time.Hour))
			n := models.Notification{Kind: models.NotifyRecordingExpiring, UserID: r.CreatedBy.UserID, GroupKey: "recording_expiring:" + r.ID, ProjectID: r.ProjectID, Tone: "warning",
				Title: fmt.Sprintf("“%s” didn't finish: what arrived of it is removed in %s", r.Name, dayWords(max(days, 1))),
				Body:  "Open the recorder to keep what arrived, or throw it away.", Link: "/record",
				Details: map[string]any{"recordingId": r.ID}, Machine: a.Cfg.Installation.Name}
			if r.ProjectID != "" {
				n.Link = "/record?project=" + r.ProjectID
			}
			if n.UserID == "" {
				n.UserID = a.Cfg.User.ID
			}
			if _, err := notify.Upsert(ctx, as(r.CreatedBy.UserID, r.CreatedBy.UserName), n, notify.Refresh); err == nil {
				at := now.UTC()
				// Telling the maker isn't touching the recording: updatedAt stays.
				_, _ = a.DB.C(store.CRecordings).UpdateOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID()}, bson.M{"$set": bson.M{"expiryNoticeAt": at}})
				told++
			}
			continue
		}
		if idle < keep || now.Sub(*r.ExpiryNoticeAt) < 24*time.Hour {
			continue
		}
		days := dayWords(int(keep / (24 * time.Hour)))
		// Only if nobody touched it since it was read (a person resuming it now wins).
		unchanged := bson.M{"lastPieceAt": r.LastPieceAt, "updatedAt": r.UpdatedAt}
		if partFootage(r) {
			if keepFootage(ctx, a, as, r, days, unchanged) {
				a.Audit(ctx, "recording.expire", "recording", r.ID, r.ProjectID, map[string]any{"idleDays": int(idle.Hours() / 24), "keptFootage": true})
				removed++
			}
			continue
		}
		why := fmt.Sprintf("What arrived of it was removed after %s without being kept.", days)
		if err := throwAway(ctx, a, r, "expired", why, unchanged); err == nil {
			a.Audit(ctx, "recording.expire", "recording", r.ID, r.ProjectID, map[string]any{"idleDays": int(idle.Hours() / 24)})
			removed++
		}
	}
	return told, removed
}

// partFootage: some of its tracks already are clips.
func partFootage(r *models.Recording) bool {
	for _, t := range r.Tracks {
		if t.AssetID != "" {
			return true
		}
	}
	return false
}

// keepFootage ends a recording that is partly footage: the tracks that are
// clips stay (and the recording, as what is footage of it), the pieces of the
// rest go, each said; it is linked into its project as it would have been.
func keepFootage(ctx context.Context, a *app.App, as func(userID, userName string) *app.App, r *models.Recording, days string, unchanged bson.M) bool {
	now := time.Now().UTC()
	var gone []string
	for i := range r.Tracks {
		t := &r.Tracks[i]
		if t.AssetID == "" {
			t.State, t.Error = "failed", fmt.Sprintf("removed after %s without being kept", days)
			gone = append(gone, trackWords(t.ID))
		}
	}
	r.State, r.FinishedAt, r.UpdatedAt = models.RecordingReady, &now, now
	r.Error = fmt.Sprintf("What hadn't become footage (the %s) was removed after %s without being kept; the rest is in the Library.", joinAnd(gone), days)
	filter := bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID(), "state": models.RecordingFailed}
	for k, v := range unchanged {
		filter[k] = v
	}
	res, err := a.DB.C(store.CRecordings).ReplaceOne(ctx, filter, r)
	if err != nil || res.MatchedCount == 0 {
		return false
	}
	_ = os.RemoveAll(dir(a, r.ID))
	if r.ProjectID != "" && r.LinkIn(r.ProjectID) == nil {
		if _, err := link(ctx, as(r.CreatedBy.UserID, r.CreatedBy.UserName), r, r.ProjectID); err != nil {
			r.Error += " It couldn't be linked into its project: " + plain(err)
			_ = put(ctx, a, r)
		}
	}
	return true
}

func dayWords(n int) string {
	if n == 1 {
		return "a day"
	}
	return fmt.Sprintf("%d days", n)
}
