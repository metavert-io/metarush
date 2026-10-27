// Package app wires one installation's dependencies: config, the shared MongoDB,
// the canonical media store, the local cache and allowlisted tools. The CLI, the
// dashboard server and the worker all build the same App.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/cache"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/localdb"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/version"
)

// App is an opened installation.
type App struct {
	Cfg   *config.Config
	DB    *store.DB
	Media media.Store
	Cache *cache.Cache
	Tools *tools.Set
	Queue *jobs.Queue
	Actor models.Actor
	// ProposeOnly: whoever acts through this App may only propose (D248) —
	// an AI app connected that way reads and checks, and every change it
	// asks for is saved as a draft that waits for a person (draft.save);
	// the registry refuses anything else that changes MetaRush.
	ProposeOnly bool
	// Log is the activity log (operations, jobs, program runs, Claude turns).
	// Shared by copies of this App; nil records nothing.
	Log *logbook.Logger
}

// openLog starts the activity log and connects the queue and the tools to it.
func (a *App) openLog() {
	a.Log = logbook.Open(logbook.Options{Cfg: a.Cfg, DB: a.DB, Media: a.Media, Tools: a.Tools, Actor: a.Actor})
	a.Tools.OnRun = a.Log.ToolRun
	if a.Queue != nil {
		a.Queue.OnEvent = a.Log.JobEvent
	}
}

// WorkspaceID is the configured workspace.
func (a *App) WorkspaceID() string { return a.Cfg.Workspace.ID }

// Open loads config and connects. via is cli | dashboard | worker | watch.
func Open(ctx context.Context, home, via string) (*App, error) {
	cfg, err := config.Load(home)
	if err != nil {
		return nil, err
	}
	return OpenWith(ctx, cfg, via)
}

// OpenWith connects using an already-loaded config.
func OpenWith(ctx context.Context, cfg *config.Config, via string) (*App, error) {
	if cfg.Workspace.ID == "" || cfg.Installation.ID == "" {
		return nil, errors.New("MetaRush isn't set up on this machine yet")
	}
	ts := tools.New(cfg.Tools)
	if l := cfg.Database.Local; l != nil {
		// The database on this computer (D254) starts when something needs it.
		if err := localdb.New(cfg.Home(), l.Port, ts).Start(ctx); err != nil {
			// Plain words for people; what MongoDB or the system said in Details.
			details := map[string]any{}
			var le *localdb.Error
			if errors.As(err, &le) && le.Cause != nil {
				details["problem"] = le.Cause.Error()
			}
			return nil, apperr.New(apperr.Database, "The database on this computer couldn't start: %s.", strings.TrimSuffix(err.Error(), ".")).
				WithHint("MetaRush's checkup (doctor) says more.").WithDetails(details).WithCommand("metarush database status")
		}
	}
	db, err := store.Connect(ctx, cfg.Database.URI, cfg.Database.Name)
	if err != nil {
		return nil, err
	}
	ms, err := NewMediaStore(cfg)
	if err != nil {
		_ = db.Close(ctx)
		return nil, err
	}
	c, err := cache.New(cfg.Cache.Dir, cfg.Cache.MaxGB)
	if err != nil {
		_ = db.Close(ctx)
		return nil, err
	}
	for _, d := range []string{cfg.Scratch.Dir, cfg.StateDir(), cfg.LogDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	a := &App{
		Cfg: cfg, DB: db, Media: ms, Cache: c, Tools: ts,
		Queue: &jobs.Queue{DB: db, WorkspaceID: cfg.Workspace.ID},
		Actor: models.Actor{UserID: cfg.User.ID, UserName: cfg.User.Name, InstallationID: cfg.Installation.ID, Via: via},
	}
	a.openLog()
	return a, nil
}

// Close releases connections (after writing what the activity log still holds).
func (a *App) Close() {
	a.Log.Close()
	if a.DB == nil { // OpenOffline
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = a.DB.Close(ctx)
}

// NewMediaStore builds the configured canonical store.
func NewMediaStore(cfg *config.Config) (media.Store, error) { return NewMediaStoreFor(cfg.Media) }

// NewMediaStoreFor builds a store from a media section (secrets resolved).
func NewMediaStoreFor(m config.Media) (media.Store, error) {
	switch m.Provider {
	case "", "filesystem":
		return media.NewFS(m.Filesystem.Root, m.Filesystem.Shared)
	case "s3":
		s := m.S3
		return media.NewS3(media.S3Config{Endpoint: s.Endpoint, Region: s.Region, Bucket: s.Bucket, Prefix: s.Prefix, AccessKeyID: s.AccessKeyID, SecretAccessKey: s.SecretAccessKey, ForcePathStyle: s.ForcePathStyle})
	case "gcs":
		g := m.GCS
		return media.NewGCS(media.GCSConfig{Bucket: g.Bucket, Prefix: g.Prefix, CredentialsFile: g.CredentialsFile})
	default:
		return nil, fmt.Errorf("MetaRush doesn't know the storage %q that config.yaml names (it is filesystem, s3 or gcs)", m.Provider)
	}
}

// Audit appends an audit event. Failures are logged, never fatal.
func (a *App) Audit(ctx context.Context, action, targetType, targetID, projectID string, details map[string]any) {
	ev := models.AuditEvent{ID: ids.New(ids.Audit), WorkspaceID: a.WorkspaceID(), At: time.Now().UTC(), Actor: a.Actor,
		Action: action, TargetType: targetType, TargetID: targetID, ProjectID: projectID, Details: details}
	if err := a.DB.Insert(ctx, store.CAudit, ev); err != nil {
		fmt.Fprintf(os.Stderr, "warning: MetaRush couldn't write to the change history: %v\n", err)
	}
	a.Log.Audit(ctx, ev)
}

// TouchInstallation records liveness for this installation, and the MetaRush
// it runs (D130: Settings → General compares every computer's with its own). The
// daemon calls it when it starts and every minute.
func (a *App) TouchInstallation(ctx context.Context) {
	b := version.Current()
	_ = a.DB.Update(ctx, store.CInstallations, a.WorkspaceID(), a.Cfg.Installation.ID, map[string]any{"$set": map[string]any{
		"lastSeenAt": time.Now().UTC(), "version": b.Version, "commit": b.Commit, "built": b.Date}})
}

// Hostname returns a friendly machine name.
func Hostname() string {
	h, _ := os.Hostname()
	if h == "" {
		h = "unknown-host"
	}
	return h
}

// Username returns the OS user.
func Username() string {
	if u, err := user.Current(); err == nil {
		if u.Name != "" {
			return u.Name
		}
		return u.Username
	}
	return "user"
}
