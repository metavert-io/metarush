package ops

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/power"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
)

// JobExpediteIn selects queued jobs to run now.
type JobExpediteIn struct {
	IDs []string `json:"ids" jsonschema:"minItems=1,maxItems=500,description=Queued job IDs."`
}

// JobExpediteOut lists the jobs that will run now.
type JobExpediteOut struct {
	Expedited []string `json:"expedited"`
	Skipped   []string `json:"skipped" jsonschema:"description=IDs that were not queued (already running or finished)."`
	Note      string   `json:"note"`
}

// WorkerSettingsIn has no fields.
type WorkerSettingsIn struct{}

// WorkerConfigureIn changes this installation's worker settings.
type WorkerConfigureIn struct {
	HeavyBatteryThreshold *int `json:"heavyBatteryThreshold,omitempty" jsonschema:"minimum=0,maximum=100,description=Defer heavy jobs only on battery, not charging, below this percent (0 = never defer)."`
}

// WorkerSettingsOut is this installation's power state and worker policy.
type WorkerSettingsOut struct {
	Installation          string        `json:"installation"`
	HeavyBatteryThreshold int           `json:"heavyBatteryThreshold"`
	HeavyEnabled          bool          `json:"heavyEnabled" jsonschema:"description=worker.heavyRenders: this machine takes heavy jobs at all."`
	Battery               power.Battery `json:"battery"`
	DeferringHeavy        bool          `json:"deferringHeavy"`
	DeferReason           string        `json:"deferReason,omitempty"`
	FakeBattery           bool          `json:"fakeBattery,omitempty" jsonschema:"description=The battery reading comes from the METARUSH_FAKE_BATTERY testing knob."`
	ConfigPath            string        `json:"configPath"`
	Note                  string        `json:"note,omitempty"`
}

// NotificationListIn filters this person's notifications.
type NotificationListIn struct {
	Unread bool  `json:"unread,omitempty"`
	Limit  int64 `json:"limit,omitempty"`
}

// NotificationRow is a notification as listed: for a clip that is ready (or
// whose voices were found again), the questions about it that are still open.
type NotificationRow struct {
	models.Notification
	Questions *models.OpenQuestions `json:"questions,omitempty"`
	LogLink   string                `json:"logLink,omitempty" jsonschema:"description=For problems: the activity log filtered to what went wrong (See log)."`
}

// notificationLogLink points a problem notification at its log entries.
func notificationLogLink(n models.Notification) string {
	if n.Tone != "error" && n.Tone != "warning" {
		return ""
	}
	switch {
	case n.JobID != "":
		return links.Log("job", n.JobID)
	case n.RenderID != "":
		return links.Log("render", n.RenderID)
	case n.AssetID != "":
		return links.Log("asset", n.AssetID)
	}
	return ""
}

// NotificationListOut lists notifications (newest first) and the unread count.
type NotificationListOut struct {
	Notifications []NotificationRow `json:"notifications"`
	Unread        int64             `json:"unread"`
}

// NotificationReadIn marks notifications read.
type NotificationReadIn struct {
	IDs []string `json:"ids,omitempty"`
	All bool     `json:"all,omitempty"`
}

// NotificationReadOut reports what changed.
type NotificationReadOut struct {
	Marked int64 `json:"marked"`
	Unread int64 `json:"unread"`
}

func workerSettings(a *app.App) *WorkerSettingsOut {
	b := power.Read()
	t := a.Cfg.HeavyBatteryThreshold()
	out := &WorkerSettingsOut{Installation: a.Cfg.Installation.Name, HeavyBatteryThreshold: t, HeavyEnabled: a.Cfg.Worker.HeavyRenders, Battery: b,
		FakeBattery: b.Source == "fake", ConfigPath: config.Path(a.Cfg.Home())}
	if out.HeavyEnabled {
		out.DeferringHeavy, out.DeferReason = power.DeferHeavy(b, t)
	}
	return out
}

func unreadCount(ctx context.Context, a *app.App) int64 {
	n, _ := a.DB.C(store.CNotifications).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "userId": a.Actor.UserID, "read": false})
	return n
}

// OnSettingsChanged lets the daemon apply worker-settings changes made through
// the ops endpoint at once (other processes are picked up by its config reload).
var OnSettingsChanged func()

func init() {
	Def("job.expedite", "Process queued jobs now: heavy jobs may run on a machine that defers them for battery, and retry backoff is skipped", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *JobExpediteIn) (*JobExpediteOut, error) {
		if len(in.IDs) == 0 {
			return nil, invalid("Say which jobs to start now.")
		}
		done, err := a.Queue.Expedite(ctx, in.IDs)
		if err != nil {
			return nil, err
		}
		out := &JobExpediteOut{Expedited: done, Skipped: []string{}}
		seen := map[string]bool{}
		for _, id := range done {
			seen[id] = true
		}
		for _, id := range in.IDs {
			if !seen[id] {
				out.Skipped = append(out.Skipped, id)
			}
		}
		if len(done) > 0 {
			a.Audit(ctx, "job.expedite", "job", done[0], "", map[string]any{"jobs": done})
			out.Note = "They start within a few seconds, even on battery."
		}
		return out, nil
	})
	Def("worker.settings", "Show this installation's battery state and heavy-job battery policy", Flags{}, func(ctx context.Context, a *app.App, _ *WorkerSettingsIn) (*WorkerSettingsOut, error) {
		return workerSettings(a), nil
	})
	Def("worker.configure", "Change this installation's worker settings (heavy-job battery threshold) in its config.yaml; owners", Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, func(ctx context.Context, a *app.App, in *WorkerConfigureIn) (*WorkerSettingsOut, error) {
		if in.HeavyBatteryThreshold == nil {
			return nil, invalid("Nothing to change: give the battery level below which big jobs wait.")
		}
		t := *in.HeavyBatteryThreshold
		if t < 0 || t > 100 {
			return nil, invalid("The battery level must be between 0 and 100 percent (got %d).", t)
		}
		prev := a.Cfg.HeavyBatteryThreshold()
		if err := config.SetField(a.Cfg.Home(), []string{"worker", "heavyBatteryThreshold"}, t); err != nil {
			return nil, apperr.Wrap(apperr.Internal, err, "Couldn't save the battery setting in config.yaml on this machine.")
		}
		a.Cfg.SetHeavyBatteryThreshold(t)
		a.Audit(ctx, "worker.configure", "installation", a.Cfg.Installation.ID, "", map[string]any{"heavyBatteryThreshold": t, "previous": prev})
		if OnSettingsChanged != nil {
			OnSettingsChanged()
		}
		out := workerSettings(a)
		out.Note = "Saved. MetaRush on this machine uses the new setting within a few seconds."
		return out, nil
	})
	Def("notification.list", "List your notifications (newest first) with the unread count", Flags{}, func(ctx context.Context, a *app.App, in *NotificationListIn) (*NotificationListOut, error) {
		filter := bson.M{"userId": a.Actor.UserID}
		if in.Unread {
			filter["read"] = false
		}
		if in.Limit <= 0 || in.Limit > 200 {
			in.Limit = 50
		}
		var ns []models.Notification
		if err := a.DB.Find(ctx, store.CNotifications, a.WorkspaceID(), filter, &ns, options.Find().SetSort(bson.D{{Key: "updatedAt", Value: -1}}).SetLimit(in.Limit)); err != nil {
			return nil, err
		}
		// Open questions are counted now, so the bell agrees with job details and the activity bar.
		var clips []string
		for _, n := range ns {
			if n.AssetID != "" && (n.Kind == models.NotifyFileReady || n.Kind == models.NotifySpeakers) {
				clips = append(clips, n.AssetID)
			}
		}
		open, _ := todos.OpenByAsset(ctx, a, clips)
		out := &NotificationListOut{Notifications: make([]NotificationRow, 0, len(ns))}
		for _, n := range ns {
			row := NotificationRow{Notification: n, LogLink: notificationLogLink(n)}
			if n.Kind == models.NotifyFileReady || n.Kind == models.NotifySpeakers {
				row.Questions = todos.AboutClip(n.AssetID, open[n.AssetID])
			}
			out.Notifications = append(out.Notifications, row)
		}
		out.Unread = unreadCount(ctx, a)
		return out, nil
	})
	Def("notification.read", "Mark notifications read (by ID, or all)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *NotificationReadIn) (*NotificationReadOut, error) {
		if !in.All && len(in.IDs) == 0 {
			return nil, invalid("Say which notifications to mark read, or all of them.")
		}
		filter := bson.M{"workspaceId": a.WorkspaceID(), "userId": a.Actor.UserID, "read": false}
		if !in.All {
			filter["_id"] = bson.M{"$in": in.IDs}
		}
		now := time.Now().UTC()
		res, err := a.DB.C(store.CNotifications).UpdateMany(ctx, filter, bson.M{"$set": bson.M{"read": true, "readAt": now}})
		if err != nil {
			return nil, err
		}
		if res.ModifiedCount > 0 {
			a.Audit(ctx, "notification.read", "user", a.Actor.UserID, "", map[string]any{"marked": res.ModifiedCount, "all": in.All})
		}
		return &NotificationReadOut{Marked: res.ModifiedCount, Unread: unreadCount(ctx, a)}, nil
	})
}
