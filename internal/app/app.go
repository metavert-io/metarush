// Package app wires one installation's dependencies: config, the shared MongoDB,
// the canonical media store, the local cache and allowlisted tools. The CLI, the
// dashboard server and the worker all build the same App.
package app

import (
	"context"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
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
	// ReadOnly: the database is only read through this App — an export of a
	// workspace (D331), which must work with a read-only database user and on
	// a database an older MetaRush migrated. Audit and TouchInstallation
	// write nothing; its Log has no database (entries wait in this
	// computer's spool).
	ReadOnly bool
}

// MoveMarker is the database's move marker (nil: no move holds it), from a
// look at most store.MoveLookTrusted old (D335).
func (a *App) MoveMarker(ctx context.Context) (*store.MoveMarker, error) {
	if a.DB == nil {
		return nil, nil
	}
	return a.DB.MoveMarkerNow(ctx)
}

// SetMoveMarker records a marker this process just read or wrote.
func (a *App) SetMoveMarker(m *store.MoveMarker) {
	if a.DB != nil {
		a.DB.SetMoveMarkerCache(m)
	}
}

// Held reports whether a move keeps changes out of this database now: this
// process writes no audit, doesn't touch its installation and keeps its
// activity log in the spool.
func (a *App) Held() bool {
	if a.DB == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return a.DB.FrozenNow(ctx)
}

// openLog starts the activity log and connects the queue and the tools to it.
func (a *App) openLog() {
	a.Log = logbook.Open(logbook.Options{Cfg: a.Cfg, DB: a.DB, Media: a.Media, Tools: a.Tools, Actor: a.Actor, Hold: a.Held})
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
			hint := "MetaRush's checkup (doctor) says more."
			if errors.As(err, &le) {
				if le.Cause != nil {
					details["problem"] = le.Cause.Error()
				}
				if le.Next != "" {
					hint = le.Next // what to do, when MetaRush knows (this computer's kernel, D339)
				}
			}
			return nil, apperr.New(apperr.Database, "The database on this computer couldn't start: %s.", strings.TrimSuffix(err.Error(), ".")).
				WithHint(hint).WithDetails(details).WithCommand("metarush database status")
		}
	}
	db, err := store.Connect(ctx, cfg.Database.URI, cfg.Database.Name)
	if err != nil {
		return nil, err
	}
	// A database a restore still holds (D333) is no one's to work in: every
	// way in refuses it — commands, workers, the assistant's connection — not
	// only the dashboard's start, which brings the database up to date first.
	if err := db.CheckNoUnfinishedRestore(ctx); err != nil {
		var u *store.ErrRestoreUnfinished
		if errors.As(err, &u) {
			_ = db.Close(ctx)
			return nil, UnfinishedRestore(err)
		}
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
	// A database a move keeps changes out of (D335) refuses this process's
	// writes from the start and while it runs: the DB looks at the marker
	// about every second. Before its first change, this process records the
	// MetaRush it runs on its computer's record, whichever process it is (a
	// command, an AI app's connection, the daemon): a move asks every
	// computer's record whether it knows about moves.
	db.WatchMoves(cfg.Installation.ID)
	db.OnFirstWrite(a.sayBuild)
	a.openLog()
	return a, nil
}

// UnfinishedRestore is the refusal of a database a restore still holds, in
// plain words (other errors are returned as they are).
func UnfinishedRestore(err error) error {
	var u *store.ErrRestoreUnfinished
	if !errors.As(err, &u) {
		return err
	}
	msg := strings.TrimSpace(err.Error())
	msg = strings.ToUpper(msg[:1]) + msg[1:]
	if !strings.ContainsAny(msg[len(msg)-1:], ".?!)") {
		msg += "."
	}
	return apperr.New(apperr.Conflict, "%s", msg).
		WithDetails(map[string]any{"reason": "restore_unfinished", "workspace": u.Lock.Workspace, "state": u.Lock.State})
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
	// A change let through a move's freeze (it began before) keeps its
	// history with it; anything else waits while the database is held.
	if a.ReadOnly || (a.Held() && !store.WritesThrough(ctx)) {
		return
	}
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
	if a.ReadOnly || a.Held() {
		return
	}
	b := version.Current()
	// lastSeenAt is the database server's time: other computers compare it
	// with theirs (a move waits for every computer running now, D335).
	_ = a.DB.Update(ctx, store.CInstallations, a.WorkspaceID(), a.Cfg.Installation.ID, map[string]any{"$set": map[string]any{
		"version": b.Version, "commit": b.Commit, "built": b.Date, "knowsMoves": MovesMark(b.Version, b.Commit)},
		"$currentDate": map[string]any{"lastSeenAt": true}})
}

// sayBuild records this build on the installation's record, once per
// connection (OnFirstWrite; a record made later is setup's to fill).
func (a *App) sayBuild(ctx context.Context) {
	if a.ReadOnly {
		return
	}
	b := version.Current()
	_, _ = a.DB.C(store.CInstallations).UpdateOne(ctx, bson.M{"_id": a.Cfg.Installation.ID, "workspaceId": a.WorkspaceID()},
		bson.M{"$set": bson.M{"version": b.Version, "commit": b.Commit, "built": b.Date, "knowsMoves": MovesMark(b.Version, b.Commit)}})
}

// MovesMark is what a MetaRush that knows about moving the database records
// as its installation's knowsMoves: its build. An older build started there
// later rewrites the version and commit but not this, so it no longer
// matches (D334).
func MovesMark(v, commit string) string { return v + " " + commit }

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
