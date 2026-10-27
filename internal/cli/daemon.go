package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/api"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/catchup"
	"github.com/metavert-io/metarush/internal/components"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/improve"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/intake"
	"github.com/metavert-io/metarush/internal/linkimport"
	"github.com/metavert-io/metarush/internal/localdb"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/recipes"
	"github.com/metavert-io/metarush/internal/remote"
	"github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/reviewlinks"
	"github.com/metavert-io/metarush/internal/service"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/worker"
)

// daemonState is <home>/state/daemon.json (service.Daemon reads it).
type daemonState struct {
	PID       int       `json:"pid"`
	Port      int       `json:"port"`
	StartedAt time.Time `json:"startedAt"`
	Version   string    `json:"version"`
	// Mode is full (dashboard + worker) or worker-only; Service is the service
	// manager that started it (launchd | systemd), empty for `metarush dashboard`.
	Mode    string `json:"mode,omitempty"`
	Service string `json:"service,omitempty"`
}

func serveCmd() *cobra.Command {
	var noWorker, noWatch, workerOnly bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run MetaRush in this terminal: the dashboard, background work and the watched folder (its log is printed here)",
		RunE: func(c *cobra.Command, _ []string) error {
			if workerOnly && noWorker {
				return fail(c, apperr.InvalidInput, "--worker-only and --no-worker together would run nothing.", "")
			}
			a, err := openApp(c, "dashboard")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			// One daemon per home: a second one (the background service and a
			// `metarush dashboard` daemon, say) would run a second worker.
			unlock, err := lockDaemon(ctx, a.Cfg.StateDir())
			if err != nil {
				return failErr(c, apperr.New(apperr.Conflict, "MetaRush is already running on this machine.").WithHint("Stop it first.").
					WithCommand("metarush stop (or metarush service uninstall for the background service)"))
			}
			defer unlock()
			// A move of the workspace's database keeps changes out of it
			// (D335): MetaRush here holds — the dashboard reads, no
			// background work — until the move is abandoned or this computer
			// switched to the new database.
			if m, err := a.DB.ReadMoveMarker(ctx); err == nil && m.Frozen() {
				a.SetMoveMarker(m)
				return serveHeld(c, a, ctx, cancel, workerOnly)
			}
			if applied, err := a.DB.Migrate(ctx); err != nil {
				return failErr(c, unfinishedRestore(err))
			} else if len(applied) > 0 {
				fmt.Fprintf(os.Stderr, "applied migrations: %v\n", applied)
			}
			a.TouchInstallation(ctx)
			// The database on this computer (D254): openApp started it; the
			// daemon keeps it running.
			if l := a.Cfg.Database.Local; l != nil {
				go localdb.New(a.Cfg.Home(), l.Port, a.Tools).Watch(ctx, func(f string, x ...any) { fmt.Fprintf(os.Stderr, f+"\n", x...) })
			}
			// A move started from the dashboard keeps running while this daemon
			// restarts with the new storage; it is still this process's child.
			ops.ReapDashboardMoves(a)
			srv := api.New(a)
			srv.Log = func(f string, x ...any) { fmt.Fprintf(os.Stderr, f+"\n", x...) }
			// This version's renderer: a notice for installations that rendered
			// from a checkout before, and the one-time setup after an upgrade.
			rend := renderer.New(a.Cfg, a.Tools)
			if n := rend.NoticeOnce(); n != "" {
				srv.Log("%s", n)
			}
			if !noWorker {
				go rend.FirstUse(ctx, srv.Log)
			}
			// The weekly improvement report, when it is turned on (improve.weekly).
			if exe, err := os.Executable(); err == nil {
				go improve.RunSchedule(ctx, a, exe, srv.Log)
			}
			// Footage imported before an analysis existed gets it now (once per version).
			go func() {
				outs, err := catchup.Run(ctx, a)
				if err != nil {
					srv.Log("catching up after the upgrade failed: %v", err)
				}
				if s := catchup.Describe(outs); s != "" {
					srv.Log("%s", s)
				}
			}()
			var w *worker.Worker
			if a.Cfg.Worker.Enabled && !noWorker {
				w = worker.New(a)
				go func() {
					if err := w.Run(ctx); err != nil {
						fmt.Fprintf(os.Stderr, "worker stopped: %v\n", err)
					}
				}()
			}
			// A component added or removed in Settings → Components (D324):
			// the programs are looked for again (the add did) and the
			// background work takes on what it can do now.
			components.OnChanged = func(*app.App) {
				if w != nil {
					w.Refresh()
				}
			}
			// …and one added or removed in a terminal, by setup or by Homebrew:
			// config.yaml or the choices changed.
			go components.Watch(ctx, a, 5*time.Second, func() {
				if w != nil {
					w.Refresh()
				}
				srv.Broker().Local(api.Event{Type: "settings"})
			})
			srv.WorkerFn = func() ([]string, bool, string) {
				if w == nil {
					return []string{}, false, ""
				}
				tx := ""
				if w.Tx != nil {
					tx = w.Tx.Name() + " " + w.Tx.Model()
				}
				return w.JobTypes(), true, tx
			}
			if iv := a.Cfg.Cache.PruneInterval(); iv > 0 {
				go a.Cache.EnforceQuota(ctx, iv, srv.Log) // keeps the cache under cache.maxGB (LRU; pinned/in-use kept)
			}
			settingsChanged := func() { srv.Broker().Local(api.Event{Type: "settings"}) }
			ops.OnSettingsChanged = settingsChanged
			// The watched folder hot-reloads: dashboard changes apply in-process,
			// CLI changes (`metarush watch set`) through the config.yaml poller below.
			wm := ingest.NewWatchManager(ctx, a, srv.Log)
			wm.Disabled = noWatch
			wm.OnChange = settingsChanged
			ingest.Live, srv.Watch = wm, wm
			wm.Apply(a.Cfg.Worker.Watch)
			var moved atomic.Bool // the media storage changed (`metarush storage migrate`): start again with it
			// A move of the database started (D335): start again holding.
			go watchForMove(ctx, a, func(m *store.MoveMarker) {
				if !moved.Swap(true) {
					srv.Log("%s: pausing (MetaRush starts again, reading only)", ops.MovedWords(m))
					cancel()
				}
			})
			// Linked folders (resources) stay in step the same way; their
			// paths are per machine (config.yaml resources.folders).
			fm := intake.NewFolderManager(ctx, a, srv.Log)
			fm.OnChange = func() { srv.Broker().Local(api.Event{Type: store.CResources}) }
			intake.LiveFolders = fm
			fm.Apply(a.Cfg.Resources.Folders)
			remote.OnChange = settingsChanged
			if rm := a.Cfg.Remote(); rm.On() {
				srv.Log("remote access: on at %s through tailscale serve (%d Tailscale accounts sign in by themselves)", rm.PublicURL, len(rm.Logins))
			}
			go followConfig(ctx, a, srv.Log, func(cfg *config.Config) {
				a.Cfg.SetHeavyBatteryThreshold(cfg.HeavyBatteryThreshold())
				// Remote access (D128): `metarush remote …` in a terminal takes
				// effect here at once; a removed person is turned away on their
				// next request.
				if before, after := a.Cfg.Remote(), cfg.Remote(); !sameRemote(before, after) {
					if err := a.Cfg.SetRemote(cfg.Dashboard); err == nil {
						if after.On() {
							srv.Log("remote access: on at %s (%d Tailscale accounts sign in by themselves)", after.PublicURL, len(after.Logins))
						} else {
							srv.Log("remote access: off")
						}
					}
				}
				wm.Apply(cfg.Worker.Watch)
				a.Cfg.Resources.Folders = cfg.Resources.Folders
				fm.Apply(cfg.Resources.Folders)
				a.Log.SetSettings(cfg.Log)
				settingsChanged()
				if st, err := app.NewMediaStore(cfg); err != nil {
					srv.Log("config.yaml names a media storage this machine cannot open (keeping %s): %v", a.Media.Locator(), err)
				} else if st.Locator() != a.Media.Locator() && !moved.Swap(true) {
					srv.Log("the media storage is now %s: restarting to use it", st.Locator())
					cancel()
				} else if storageKeyChanged(a.Cfg.Media, cfg.Media) && !moved.Swap(true) {
					// `metarush machine rekey`: the same storage with this machine's own key.
					srv.Log("this machine's storage key changed: restarting to use it")
					cancel()
				}
			})
			// The activity log's share of the daemon: spooled entries, dashboard-read
			// counts, finished Claude Code terminal turns, retention when it is set.
			go a.Log.RunDaemon(ctx, srv.Log)
			// Link imports: keep the downloader current (weekly, while idle; imports.autoUpdate)
			// and clear download folders nothing will use again.
			go linkimport.RunAutoUpdate(ctx, a, srv.Log)
			go linkimport.RunSweep(ctx, a)
			// The Trash empties itself only when a person turned that on (D98).
			go ops.RunTrashSchedule(ctx, a, srv.Log)
			// A review copy follows each cut's latest version once it has been
			// left alone for a minute (D187). Only daemons do this, never a
			// terminal command; the job key keeps several daemons from doubling it.
			go ops.RunReviewFollow(ctx, a, srv.Log)
			// Review links for people outside the workspace (D315): new comments
			// come in, links whose time is up end and their files are deleted.
			go reviewlinks.Run(ctx, a, srv.Log)
			// Posts to video platforms MetaRush stopped part-way go on from where
			// YouTube got to (or are marked as not posted, plainly) (D305).
			go ops.RunPublishRecovery(ctx, a, srv.Log)
			// Generated video: close requests stopped before they were sent and
			// let go of money held for requests that were never saved (D124).
			go generate.RunSweep(ctx, a)
			go aiwork.RunSweep(ctx, a)
			// Runs of Recipes this computer started go on by themselves (D204): the
			// engine advances them from the database, one look at a time.
			go recipes.RunEngine(ctx, a, srv.Log)
			// Recipes that plan by themselves (D218): new files or a weekly time
			// make a plan and ring the bell; a person starts the run, never this loop.
			go recipes.RunTriggers(ctx, a, srv.Log)
			st := daemonState{PID: os.Getpid(), Port: a.Cfg.Dashboard.Port, StartedAt: time.Now().UTC(), Version: versionString(), Mode: "full", Service: os.Getenv("METARUSH_SERVICE")}
			if workerOnly {
				st.Mode, st.Port = "worker-only", 0
			}
			b, _ := json.Marshal(st)
			_ = os.WriteFile(filepath.Join(a.Cfg.StateDir(), "daemon.json"), b, 0o600)
			defer os.Remove(filepath.Join(a.Cfg.StateDir(), "daemon.json"))
			go func() {
				t := time.NewTicker(time.Minute)
				defer t.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-t.C:
						a.TouchInstallation(ctx)
					}
				}
			}()
			if workerOnly {
				fmt.Fprintf(os.Stderr, "MetaRush %s worker running without the dashboard (workspace %s, store %s)\n", versionString(), a.Cfg.Workspace.Name, a.Media.Locator())
				<-ctx.Done()
				return restartIfMoved(moved.Load(), a, srv, nil)
			}
			fmt.Fprintf(os.Stderr, "MetaRush %s daemon on http://%s:%d (workspace %s, store %s)\n", versionString(), a.Cfg.Dashboard.Host, a.Cfg.Dashboard.Port, a.Cfg.Workspace.Name, a.Media.Locator())
			err = srv.Listen(ctx) // before moved is read: it is set while the server runs
			return restartIfMoved(moved.Load(), a, srv, err)
		},
	}
	cmd.Flags().BoolVar(&noWorker, "no-worker", false, "do not run a job worker in this daemon")
	cmd.Flags().BoolVar(&noWatch, "no-watch", false, "do not watch the inbox folder")
	cmd.Flags().BoolVar(&workerOnly, "worker-only", false, "run the worker and background jobs without the dashboard (unattended machines; see `metarush service install --worker-only`)")
	return cmd
}

// MoveCheckEvery is how often MetaRush running in the background looks at
// the database's move marker.
var MoveCheckEvery = 2 * time.Second

// watchForMove calls frozen once the workspace's database is held by a move.
func watchForMove(ctx context.Context, a *app.App, frozen func(*store.MoveMarker)) {
	t := time.NewTicker(MoveCheckEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		m, err := a.DB.MoveMarkerNow(c) // the DB's own look, about every second
		cancel()
		if err == nil && m.Frozen() {
			frozen(m)
			return
		}
	}
}

// serveHeld is MetaRush in the background while a move keeps changes out of
// the workspace's database (D335): the dashboard reads (api.NewHeld refuses
// changes), no work runs, nothing is brought up to date; the marker hears that
// this computer paused. It starts again (exec, like a storage move) once the
// move is abandoned, or config.yaml changed (this computer switched to the
// new database).
func serveHeld(c *cobra.Command, a *app.App, ctx context.Context, cancel context.CancelFunc, workerOnly bool) error {
	logf := func(f string, x ...any) { fmt.Fprintf(os.Stderr, f+"\n", x...) }
	m, _ := ops.HoldForMove(ctx, a)
	logf("MetaRush %s is holding (reading only): %s", versionString(), ops.MovedWords(m))
	if l := a.Cfg.Database.Local; l != nil {
		go localdb.New(a.Cfg.Home(), l.Port, a.Tools).Watch(ctx, logf)
	}
	srv := api.NewHeld(a)
	srv.Log = logf
	var restart atomic.Bool
	go func() {
		p := config.Path(a.Cfg.Home())
		last := time.Time{}
		if st, err := os.Stat(p); err == nil {
			last = st.ModTime()
		}
		said := time.Now()
		t := time.NewTicker(MoveCheckEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if st, err := os.Stat(p); err == nil && !st.ModTime().Equal(last) {
				logf("config.yaml changed: starting again with it")
				restart.Store(true)
				cancel()
				return
			}
			cctx, ccancel := context.WithTimeout(ctx, 5*time.Second)
			m, err := a.DB.ReadMoveMarker(cctx)
			ccancel()
			if err != nil {
				continue
			}
			a.SetMoveMarker(m)
			if !m.Frozen() {
				logf("the move of the database was abandoned: starting again")
				restart.Store(true)
				cancel()
				return
			}
			if m.State == store.MoveMoving && time.Since(said) > 10*time.Second {
				_, _ = ops.HoldForMove(ctx, a)
				said = time.Now()
			}
		}
	}()
	st := daemonState{PID: os.Getpid(), Port: a.Cfg.Dashboard.Port, StartedAt: time.Now().UTC(), Version: versionString(), Mode: "held", Service: os.Getenv("METARUSH_SERVICE")}
	if workerOnly {
		st.Port = 0
	}
	b, _ := json.Marshal(st)
	_ = os.WriteFile(filepath.Join(a.Cfg.StateDir(), "daemon.json"), b, 0o600)
	defer os.Remove(filepath.Join(a.Cfg.StateDir(), "daemon.json"))
	var err error
	if workerOnly {
		<-ctx.Done()
	} else {
		err = srv.Listen(ctx)
	}
	return restartIfMoved(restart.Load(), a, srv, err)
}

// storageKeyChanged reports whether config.yaml now gives this machine another
// key for the same storage (`metarush machine rekey`).
func storageKeyChanged(old, now config.Media) bool {
	return old.Provider == "s3" && now.Provider == "s3" &&
		(old.S3.AccessKeyID != now.S3.AccessKeyID || old.S3.SecretAccessKey != now.S3.SecretAccessKey)
}

// restartIfMoved starts the daemon again, in this same process (same PID, so
// a LaunchAgent or systemd unit keeps tracking it), once the media storage in
// config.yaml changed, or this machine's key for it: every part of MetaRush
// opens the store at start. Files and sockets close on exec, which also
// releases the daemon lock. Variables read from <home>/.env are dropped first,
// so the new process reads the file again (a rekey may have changed the
// secret there).
func restartIfMoved(moved bool, a *app.App, srv *api.Server, err error) error {
	if !moved {
		return err
	}
	exe, xerr := os.Executable()
	if xerr != nil {
		return fmt.Errorf("restart with the new media storage: %w", xerr)
	}
	if srv.Assistant != nil {
		srv.Assistant.Close() // Claude Code processes end; their conversations resume later
	}
	a.Close() // what the activity log still holds is written first
	config.ForgetDotEnv()
	return fmt.Errorf("restart with the new media storage: %w", syscall.Exec(exe, os.Args, os.Environ()))
}

// lockDaemon holds <state>/daemon.lock while a daemon runs (the system
// releases it when the process dies, so a crash never leaves it stale). A
// daemon that is still stopping gets a few seconds to let go.
func lockDaemon(ctx context.Context, stateDir string) (func(), error) {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(stateDir, "daemon.lock"), os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			_ = f.Close()
			return nil, err
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// followConfig reloads config.yaml when it changes on disk (another process ran
// `metarush watch set` or `worker settings`) and hands the new config to apply.
func followConfig(ctx context.Context, a *app.App, log func(string, ...any), apply func(*config.Config)) {
	p := config.Path(a.Cfg.Home())
	last := time.Time{}
	if st, err := os.Stat(p); err == nil {
		last = st.ModTime()
	}
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		st, err := os.Stat(p)
		if err != nil || st.ModTime().Equal(last) {
			continue
		}
		last = st.ModTime()
		cfg, err := config.Load(a.Cfg.Home())
		if err != nil {
			log("config.yaml changed but could not be loaded (keeping the previous settings): %v", err)
			continue
		}
		apply(cfg)
	}
}

func sameRemote(a, b config.Remote) bool {
	return a.PublicURL == b.PublicURL && a.MachineName == b.MachineName && strings.Join(a.Logins, "\n") == strings.Join(b.Logins, "\n")
}

func healthy(cfg *config.Config) bool {
	client := http.Client{Timeout: 1500 * time.Millisecond}
	res, err := client.Get(fmt.Sprintf("http://%s:%d/api/health", cfg.Dashboard.Host, cfg.Dashboard.Port))
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == 200
}

func dashboardCmd() *cobra.Command {
	var printOnly, noOpen, dev, remoteLink bool
	var next string
	cmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Open the dashboard (starts MetaRush in the background first when it isn't running)",
		RunE: func(c *cobra.Command, _ []string) error {
			h, err := home()
			if err != nil {
				return err
			}
			cfg, err := config.Load(h)
			if err != nil {
				return notSetUp(c, h, err)
			}
			if remoteLink {
				// One-time links sign in the owner on this computer only (D183).
				return failErr(c, apperr.New(apperr.InvalidInput, "One-time sign-in links work only on this computer now.").
					WithHint("From another computer, sign in with an account: set up sign-in and invite people in Settings → People.").WithCommand("metarush users invite <email>"))
			}
			if remoteLink && dev {
				return fail(c, apperr.InvalidInput, "--remote and --dev can't be used together.", "")
			}
			if remoteLink && !cfg.Remote().On() {
				return failErr(c, apperr.New(apperr.NotConfigured, "Remote access isn't on, so there is no address for other computers yet.").
					WithHint("Turn remote access on first.").WithCommand("metarush remote enable"))
			}
			started := false
			if d, ok := service.ReadDaemon(h); ok && d.Mode == "worker-only" && !dev {
				return failErr(c, apperr.New(apperr.Conflict, "MetaRush runs on this machine in the background without the dashboard.").
					WithHint("To use the dashboard here, set the background service up with the dashboard, or stop it first.").
					WithCommand("metarush service install (without --worker-only)"))
			}
			if !healthy(cfg) && !dev {
				// An installed background service (with the dashboard) is started
				// again rather than a second, unmanaged daemon beside it.
				svc := service.Options{Home: h, Tools: tools.New(cfg.Tools)}
				if st := svc.Get(c.Context()); st.Installed && !st.WorkerOnly && svc.Restart(c.Context()) == nil {
					fmt.Fprintln(os.Stderr, "Starting MetaRush in the background…")
				} else if err := spawnDaemon(h, cfg); err != nil {
					return failErr(c, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't start in the background.").
						WithHint("Start it in a terminal to see why.").WithCommand("metarush serve"))
				}
				deadline := time.Now().Add(20 * time.Second)
				for !healthy(cfg) {
					if time.Now().After(deadline) {
						return failErr(c, apperr.New(apperr.Internal, "MetaRush started, but it isn't answering yet.").WithHint("Its log says why.").
							WithDetails(map[string]any{"log": filepath.Join(cfg.LogDir(), "daemon.log"), "command": "metarush service logs"}))
					}
					time.Sleep(250 * time.Millisecond)
				}
				started = true
			}
			nonce, err := api.MintLaunchNonce(cfg.StateDir())
			if err != nil {
				return err
			}
			base := fmt.Sprintf("http://%s:%d", cfg.Dashboard.Host, cfg.Dashboard.Port)
			if dev {
				base = "http://localhost:4840"
			}
			if remoteLink {
				base = cfg.Remote().PublicURL // the address other computers open (remote access, D128)
			}
			u := base + "/auth?t=" + nonce
			if next != "" {
				u += "&next=" + next
			}
			res := map[string]any{"url": u, "started": started, "expiresInMinutes": 10, "dashboard": base, "remote": remoteLink}
			return emit(c, res, func() {
				if started {
					fmt.Fprintln(os.Stderr, "Started MetaRush in the background.")
					if detailsMode(c) {
						fmt.Fprintf(os.Stderr, "logs: %s\n", filepath.Join(cfg.LogDir(), "daemon.log"))
					}
				}
				if remoteLink {
					fmt.Fprintf(os.Stderr, "A one-time sign-in link for a browser on another computer on your tailnet (it works once, within 10 minutes, and signs that browser in as %s):\n", orWord(cfg.User.Name, "you"))
				}
				fmt.Println(u)
				if !printOnly && !noOpen {
					openBrowser(u)
				}
			})
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print-link", false, "print the one-time sign-in link without opening a browser")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "do not open a browser")
	cmd.Flags().BoolVar(&dev, "dev", false, "link to the Vite dev server (localhost:4840) instead of the embedded build")
	cmd.Flags().BoolVar(&remoteLink, "remote", false, "no longer used: links work only on this computer; other computers sign in with an account (metarush users invite)")
	_ = cmd.Flags().MarkHidden("remote")
	cmd.Flags().StringVar(&next, "next", "", "path to open after sign-in (e.g. /p/prj_…)")
	return cmd
}

func spawnDaemon(h string, cfg *config.Config) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.LogDir(), 0o755); err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(cfg.LogDir(), "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "--home", h, "serve")
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func openBrowser(u string) {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("open", u).Start()
	case "linux":
		_ = exec.Command("xdg-open", u).Start()
	}
}

func stopCmd() *cobra.Command {
	var keepDB bool
	c := &cobra.Command{
		Use:   "stop",
		Short: "Stop MetaRush running in the background on this machine (and the database on this computer, when it has one)",
		RunE: func(c *cobra.Command, _ []string) error {
			h, err := home()
			if err != nil {
				return err
			}
			cfg, err := config.Load(h)
			if err != nil {
				return notSetUp(c, h, err)
			}
			// The database on this computer stops after the daemon (which would
			// otherwise start it again), unless --keep-database.
			stopDB := func() (bool, error) {
				if keepDB || cfg.Database.Local == nil {
					return false, nil
				}
				srv := localdb.New(h, cfg.Database.Local.Port, tools.New(cfg.Tools))
				if srv.PID() == 0 {
					return false, nil
				}
				return true, srv.Stop(c.Context())
			}
			b, err := os.ReadFile(filepath.Join(cfg.StateDir(), "daemon.json"))
			if err != nil {
				dbStopped, dbErr := stopDB()
				if dbErr != nil {
					return failErr(c, databaseErr(dbErr, "The database on this computer didn't stop"))
				}
				return emit(c, map[string]any{"stopped": false, "reason": "not running", "databaseStopped": dbStopped}, func() {
					fmt.Println("MetaRush isn't running in the background on this machine.")
					if dbStopped {
						fmt.Println("Stopped the database on this computer.")
					}
				})
			}
			var st daemonState
			_ = json.Unmarshal(b, &st)
			if st.PID <= 0 {
				return failErr(c, apperr.New(apperr.Internal, "MetaRush couldn't read which process runs it in the background.").
					WithDetails(map[string]any{"file": filepath.Join(cfg.StateDir(), "daemon.json"), "problem": "corrupt daemon state"}))
			}
			if err := syscall.Kill(st.PID, syscall.SIGTERM); err != nil {
				_ = os.Remove(filepath.Join(cfg.StateDir(), "daemon.json"))
				return emit(c, map[string]any{"stopped": false, "reason": err.Error()}, func() {
					fmt.Println("MetaRush wasn't running in the background on this machine.")
					if detailsMode(c) {
						fmt.Println("reason:", err)
					}
				})
			}
			for i := 0; i < 40 && healthy(cfg); i++ {
				time.Sleep(250 * time.Millisecond)
			}
			waitGone(h, 15*time.Second)
			dbStopped, dbErr := stopDB()
			if dbErr != nil {
				return failErr(c, databaseErr(dbErr, "MetaRush stopped, but the database on this computer didn't"))
			}
			return emit(c, map[string]any{"stopped": true, "pid": st.PID, "service": st.Service, "databaseStopped": dbStopped}, func() {
				fmt.Println("Stopped MetaRush on this machine.")
				if dbStopped {
					fmt.Println("Stopped the database on this computer too.")
				}
				if st.Service != "" {
					fmt.Println("It runs as a background service, so it starts again when you next log in.")
				}
				if detailsMode(c) {
					fmt.Println("pid " + strconv.Itoa(st.PID))
					if st.Service != "" {
						fmt.Println("start it now: metarush service restart")
					}
				}
			})
		},
	}
	c.Flags().BoolVar(&keepDB, "keep-database", false, "leave the database on this computer running")
	return c
}

func workerCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "worker", Short: "Do MetaRush's background work on this machine, or see which machines do it"}
	var conc int
	var heavy bool
	start := &cobra.Command{
		Use:   "start",
		Short: "Do background work in this terminal (transcripts, preview copies, renders), without the dashboard",
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "worker")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			// A move of the workspace's database keeps changes out of it
			// (D335): no work runs meanwhile.
			if m, err := a.MoveMarker(ctx); err == nil && m.Frozen() {
				return failErr(c, ops.FrozenError(m))
			}
			var held atomic.Pointer[store.MoveMarker]
			go watchForMove(ctx, a, func(m *store.MoveMarker) {
				held.Store(m)
				cancel() // a stopping worker hands its jobs back (they run again later)
			})
			defer func() {
				if m := held.Load(); m != nil {
					fmt.Fprintln(os.Stderr, upperFirst(ops.MovedWords(m))+". Start the work again once it's done.")
				}
			}()
			// This version's renderer is set up the first time a worker starts.
			go renderer.New(a.Cfg, a.Tools).FirstUse(ctx, func(f string, x ...any) { fmt.Fprintf(os.Stderr, f+"\n", x...) })
			w := worker.New(a)
			if conc > 0 {
				w.Concurrency = conc
			}
			if c.Flags().Changed("heavy") {
				w.AcceptsHeavy = heavy
			}
			if iv := a.Cfg.Cache.PruneInterval(); iv > 0 {
				go a.Cache.EnforceQuota(ctx, iv, w.Log)
			}
			// `metarush worker settings --heavy-battery-threshold` applies without a restart.
			go followConfig(ctx, a, w.Log, func(cfg *config.Config) {
				a.Cfg.SetHeavyBatteryThreshold(cfg.HeavyBatteryThreshold())
				a.Log.SetSettings(cfg.Log)
			})
			go a.Log.RunDaemon(ctx, w.Log) // spooled log entries and retention on a headless worker too
			if err := w.Run(ctx); err != nil {
				return failErr(c, err)
			}
			return nil
		},
	}
	start.Flags().IntVar(&conc, "concurrency", 0, "jobs at the same time (default from config)")
	start.Flags().BoolVar(&heavy, "heavy", true, "take big jobs (final renders, transcripts)")
	cmd.AddCommand(start)
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "The machines that do background work, with their battery and when big jobs wait",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "worker.list", map[string]any{}, func(o any) {
				for _, w := range o.(*ops.WorkerListOut).Workers {
					state := "not running MetaRush"
					switch {
					case w.Online && w.Running > 0:
						state = "working: " + plural(w.Running, "job")
					case w.Online:
						state = "ready for work"
					}
					fmt.Printf("%-20s %-24s %s\n", trunc(w.Name, 20), state, powerLine(w.BatteryPercent, w.Charging, w.OnBattery, w.HeavyEnabled, w.HeavyBatteryThreshold, w.DeferringHeavy, w.DeferReason))
					if detailsMode(c) {
						fmt.Printf("    %d/%d running · %s\n", w.Running, w.Concurrency, strings.Join(w.JobTypes, ", "))
					}
				}
			})
		},
	})
	var settingsDetails bool
	settings := &cobra.Command{
		Use:   "settings",
		Short: "This machine's battery, and the level below which big jobs wait for the charger",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "worker.settings", ops.WorkerSettingsIn{}, func(o any) { printWorkerSettings(o.(*ops.WorkerSettingsOut), settingsDetails) })
		},
	}
	addDetails(settings, &settingsDetails)
	cmd.AddCommand(settings)
	var threshold int
	configure := &cobra.Command{
		Use:   "configure --heavy-battery-threshold <percent>",
		Short: "Change the battery level below which big jobs wait for the charger (0 = never wait; people only)",
		RunE: func(c *cobra.Command, _ []string) error {
			if !c.Flags().Changed("heavy-battery-threshold") {
				return fail(c, "E_INVALID_INPUT", "Nothing to change.", "Pass --heavy-battery-threshold <percent>.")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "worker.configure", ops.WorkerConfigureIn{HeavyBatteryThreshold: &threshold}, ops.Caller{Human: interactive()},
				func(o any) { printWorkerSettings(o.(*ops.WorkerSettingsOut), false) })
		},
	}
	configure.Flags().IntVar(&threshold, "heavy-battery-threshold", 25, "big jobs wait only on battery (not charging) below this percent")
	cmd.AddCommand(configure)
	return cmd
}

func powerLine(pct *int, charging, onBattery, heavyEnabled bool, threshold int, deferring bool, reason string) string {
	pw := "plugged in"
	if pct != nil {
		pw = fmt.Sprintf("battery %d%%", *pct)
		switch {
		case charging:
			pw += ", charging"
		case onBattery:
			pw += ", on battery"
		default:
			pw += ", plugged in"
		}
	}
	switch {
	case !heavyEnabled:
		return pw + "; big jobs (transcripts, scene analysis, renders) are turned off here"
	case deferring:
		return pw + "; holding big jobs: " + reason
	case threshold == 0:
		return pw + "; big jobs run on any battery level"
	}
	return fmt.Sprintf("%s; big jobs wait while on battery below %d%%", pw, threshold)
}

func printWorkerSettings(s *ops.WorkerSettingsOut, details bool) {
	var pct *int
	if s.Battery.Present {
		p := s.Battery.Percent
		pct = &p
	}
	fmt.Printf("%s: %s\n", s.Installation, powerLine(pct, s.Battery.Charging, s.Battery.OnBattery(), s.HeavyEnabled, s.HeavyBatteryThreshold, s.DeferringHeavy, s.DeferReason))
	if s.FakeBattery {
		fmt.Println("  (simulated battery reading, for testing)")
	}
	if s.Note != "" {
		fmt.Println("  " + s.Note)
	}
	if details {
		fmt.Printf("  config: %s (worker.heavyBatteryThreshold = %d)\n", s.ConfigPath, s.HeavyBatteryThreshold)
		if s.FakeBattery {
			fmt.Println("  battery reading from METARUSH_FAKE_BATTERY")
		}
	}
}

func migrateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Update the database for this version of MetaRush (it only adds, never removes)",
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			applied, err := a.DB.Migrate(c.Context())
			if err != nil {
				return failErr(c, unfinishedRestore(err))
			}
			// Then bring existing footage up to date (speaker detection, questions, voice profiles).
			outs, err := catchup.Run(c.Context(), a)
			if err != nil {
				return failErr(c, err)
			}
			// An installation that rendered from a checkout hears, once, that it
			// now renders with this version's own renderer.
			notice := renderer.New(a.Cfg, a.Tools).NoticeOnce()
			return emit(c, map[string]any{"applied": applied, "catchUp": outs, "rendererNotice": notice}, func() {
				if notice != "" {
					fmt.Println(rendererNoticeWords)
					if detailsMode(c) {
						fmt.Println(notice)
					}
				}
				if len(applied) == 0 {
					fmt.Println("The database is up to date.")
				} else {
					fmt.Printf("Updated the database (%d %s).\n", len(applied), map[bool]string{true: "step", false: "steps"}[len(applied) == 1])
				}
				if s := catchup.Describe(outs); s != "" {
					fmt.Println(s)
				}
			})
		},
	}
}

var _ = context.Background
