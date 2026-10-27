package reviewlinks

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// settings are the owners' word on review links (nil: anyone may share, for
// up to a week).
type settings struct{ models.ReviewLinkSettings }

func (s settings) maxDays() int {
	if s.MaxDays >= 1 && s.MaxDays <= MaxDays {
		return s.MaxDays
	}
	return MaxDays
}

func loadSettings(ctx context.Context, a *app.App) settings {
	var ws models.Workspace
	if err := a.DB.GetByID(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), &ws); err != nil || ws.ReviewLinks == nil {
		return settings{}
	}
	return settings{*ws.ReviewLinks}
}

func settingsOut(ctx context.Context, a *app.App) *ShareSettingsOut {
	s := loadSettings(ctx, a)
	out := &ShareSettingsOut{On: !s.Off, OwnersOnly: s.OwnersOnly, MaxDays: s.maxDays(), Store: leave.MediaKept(a.Cfg.Media).Words, Ended: []string{}}
	if !s.UpdatedAt.IsZero() {
		at, by := s.UpdatedAt, s.UpdatedBy
		out.UpdatedAt, out.UpdatedBy = &at, &by
	}
	out.Why = media.CanShare(a.Media)
	out.CanShare = out.Why == ""
	switch {
	case s.Off:
		out.Summary = "Review links are turned off: nobody can share with people outside the workspace."
	case s.OwnersOnly:
		out.Summary = fmt.Sprintf("Only owners share with people outside the workspace, for up to %s.", plural(out.MaxDays, "day"))
	default:
		out.Summary = fmt.Sprintf("Anyone in the workspace can share a version with people outside it, for up to %s.", plural(out.MaxDays, "day"))
	}
	if !out.CanShare {
		out.Summary += " " + out.Why + " A review link's page is kept in the workspace's cloud bucket."
	}
	return out
}

func setSettings(ctx context.Context, a *app.App, in *ShareSettingsSetIn) (*ShareSettingsOut, error) {
	s := loadSettings(ctx, a).ReviewLinkSettings
	if in.On != nil {
		s.Off = !*in.On
	}
	if in.OwnersOnly != nil {
		s.OwnersOnly = *in.OwnersOnly
	}
	if in.MaxDays != nil {
		if *in.MaxDays < 1 || *in.MaxDays > MaxDays {
			return nil, apperr.New(apperr.InvalidInput, "A review link works for 1 to %d days (a cloud bucket signs a link for a week at most).", MaxDays)
		}
		s.MaxDays = *in.MaxDays
	}
	s.UpdatedAt, s.UpdatedBy = time.Now().UTC(), a.Actor
	if err := a.DB.Update(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), bson.M{"$set": bson.M{"reviewLinks": s}}); err != nil {
		return nil, err
	}
	a.Audit(ctx, "share.settings.set", "workspace", a.WorkspaceID(), "", map[string]any{"on": !s.Off, "ownersOnly": s.OwnersOnly, "maxDays": settings{s}.maxDays()})
	out := settingsOut(ctx, a)
	switch {
	case s.Off:
		// Off means off (D318): links that still work end now.
		ls, err := liveLinks(ctx, a, nil)
		if err != nil {
			return nil, err
		}
		by := a.Actor
		for i := range ls {
			line, err := endWhy(ctx, a, &ls[i], &by, "owners turned review links off")
			if err != nil {
				return nil, err
			}
			out.Ended = append(out.Ended, line)
		}
		if n := len(out.Ended); n > 0 {
			out.Summary += " " + plural(n, "link") + " that still worked ended."
		}
	case s.OwnersOnly || in.MaxDays != nil:
		if ls, _ := liveLinks(ctx, a, nil); len(ls) > 0 {
			out.Summary += fmt.Sprintf(" %s already sent keep working until each ends; take one back on its version's page if it should end now.", plural(len(ls), "link"))
		}
	}
	return out, nil
}

func init() {
	ops.Def("share.settings", "Who may send review links to people outside the workspace and for how long, and whether this workspace's media store can hold their pages", ops.Flags{},
		func(ctx context.Context, a *app.App, _ *ops.EmptyIn) (*ShareSettingsOut, error) {
			return settingsOut(ctx, a), nil
		})
	ops.Def("share.settings.set", "Turn review links for people outside the workspace on or off, let only owners send them, or shorten the longest a link lasts (owners)", ops.Flags{Mutating: true, OwnerOnly: true, HumanOnly: true},
		setSettings)
}

// First-party abilities (D269): how the menu and find show sharing for
// review, and what it needs (a cloud bucket that signs links).
func init() {
	ops.RegisterReady("review-links", func(_ context.Context, a *app.App) (bool, string) {
		if why := media.CanShare(a.Media); why != "" {
			return false, "media kept in a cloud bucket (Settings → Storage)"
		}
		return true, ""
	})
	ops.Offer("share.create", ops.Offering{ID: "share-for-review", Group: "deliver", Title: "Share a version for review",
		Does:  "Sends someone outside the workspace an expiring link to a version's review copy and its words, and brings their comments back.",
		Words: []string{"client review link", "send to the client", "outside reviewer", "review without an account", "feedback link"},
		Needs: ops.Needs{Setup: "review-links", Leaves: "the review copy, and its words if you include them, go to people outside the workspace through your cloud bucket"}})
}
