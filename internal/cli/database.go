package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/localdb"
	"github.com/metavert-io/metarush/internal/secretpath"
	"github.com/metavert-io/metarush/internal/service"
	"github.com/metavert-io/metarush/internal/tools"
)

// databaseCmd looks after the database on this computer (D254). Local admin
// commands like `service`: they act on this computer's programs and folders,
// not on shared state, so they are not ops, and they need no database
// connection (they work while it is stopped).
func databaseCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "database", Short: "The workspace's database: whether the one on this computer runs (start, stop, upgrade it), and moving the workspace to another database",
		Long: "When MetaRush keeps your projects in a database on this computer (`metarush setup` sets one up), these\n" +
			"commands check it, start and stop it, and move it to the version this MetaRush comes with. MetaRush starts it\n" +
			"by itself whenever it needs it, and keeps it running while MetaRush runs. A database elsewhere (MongoDB\n" +
			"Atlas, your own server) is looked after where it runs: `status` says which one MetaRush uses.\n\n" +
			"`plan` and `move` move the whole workspace to another database (this computer's, Atlas or your own server),\n" +
			"checked before anything switches and never deleting the old one; `switch` moves another computer over after\n" +
			"that, with its own sign-in; `abandon` lets changes go on in the old database after a move stopped."}
	cmd.AddCommand(databaseStatusCmd(), databaseStartCmd(), databaseStopCmd(), databaseUpgradeCmd())
	cmd.AddCommand(databaseMoveCmds()...)
	return cmd
}

// localDatabase is this home's database on this computer, or nil when
// MetaRush uses a database elsewhere. It reads only what it needs from
// config.yaml (no keychain), so it works when the rest can't be read.
func localDatabase(c *cobra.Command) (*localdb.Server, string, error) {
	h, err := home()
	if err != nil {
		return nil, "", failErr(c, err)
	}
	if !config.Exists(h) {
		return nil, h, notSetUp(c, h, errors.New("not set up"))
	}
	l, err := config.LoadLocalDatabase(h)
	if err != nil {
		return nil, h, notSetUp(c, h, err)
	}
	if l == nil {
		return nil, h, nil
	}
	ts, err := config.LoadTools(h)
	if err != nil {
		return nil, h, notSetUp(c, h, err)
	}
	return localdb.New(h, l.Port, tools.New(ts)), h, nil
}

// DatabaseStatus is `metarush database status --json`.
type DatabaseStatus struct {
	// Local: MetaRush uses the database on this computer; Status says how it is.
	Local  bool            `json:"local"`
	Status *localdb.Status `json:"status,omitempty"`
	// Elsewhere is where the database is when it isn't on this computer (its
	// host, never the sign-in), and Atlas says it is MongoDB Atlas.
	Elsewhere string `json:"elsewhere,omitempty"`
	Atlas     bool   `json:"atlas,omitempty"`
}

func databaseStatusCmd() *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Say which database MetaRush uses, and whether the one on this computer runs",
		RunE: func(c *cobra.Command, _ []string) error {
			srv, h, err := localDatabase(c)
			if err != nil {
				return err
			}
			out := DatabaseStatus{}
			if srv == nil {
				if cfg, err := config.Load(h); err == nil {
					out.Elsewhere = config.HostOf(cfg.Database.URI)
					out.Atlas = strings.Contains(out.Elsewhere, ".mongodb.net")
				}
				return emit(c, out, func() {
					switch {
					case out.Atlas:
						fmt.Println("MetaRush keeps your projects in a database on MongoDB Atlas, not on this computer.")
					default:
						fmt.Println("MetaRush keeps your projects in a database elsewhere, not on this computer.")
					}
					if detailsMode(c) && out.Elsewhere != "" {
						fmt.Println("host: " + out.Elsewhere)
					}
				})
			}
			st := srv.Status(c.Context())
			out.Local, out.Status = true, &st
			return emit(c, out, func() { printDatabaseStatus(st, detailsMode(c)) })
		}}
}

func printDatabaseStatus(st localdb.Status, details bool) {
	switch {
	case !st.SetUp:
		fmt.Println("The database on this computer isn't set up yet.")
		if details {
			fmt.Println("  set it up: metarush setup")
		}
		return
	case st.Running:
		fmt.Printf("The database on this computer is running and holds %s.\n", fmtBytes(st.DataBytes))
	case st.CantStart != "":
		fmt.Println("The database on this computer can't start right now.")
	default:
		fmt.Println("The database on this computer isn't running. MetaRush starts it whenever it needs it.")
	}
	switch {
	case st.CantStart != "":
		fmt.Println("  " + st.CantStart)
	case st.UpgradeUnfinished:
		fmt.Println("  Its last upgrade didn't finish. Upgrade it again to finish it: it takes a moment.")
	case st.UpgradeReady:
		fmt.Println("  A newer version of it comes with this MetaRush. Upgrade it when it suits you: it takes a minute.")
	case st.UpgradeWaits != "":
		fmt.Println("  " + st.UpgradeWaits)
	}
	if details {
		fmt.Printf("  MongoDB %s on 127.0.0.1:%d", st.Version, st.Port)
		if st.PID > 0 {
			fmt.Printf(" (pid %d)", st.PID)
		}
		fmt.Printf("\n  folder %s\n  log %s\n", st.Folder, st.Log)
		if st.Kernel != "" {
			fmt.Printf("  Linux kernel %s\n", st.Kernel)
		}
		if st.UpgradeReady {
			fmt.Printf("  upgrade to %s: metarush database upgrade\n", st.Pinned)
		}
	} else if st.UpgradeReady {
		fmt.Println("(Add --details to see the command that upgrades it.)")
	}
}

// noLocalDatabase says MetaRush's database isn't on this computer.
func noLocalDatabase(c *cobra.Command) error {
	return failErr(c, apperr.New(apperr.NotConfigured, "MetaRush's database isn't on this computer, so there is nothing to do here.").
		WithHint("A database elsewhere, such as MongoDB Atlas, is looked after where it runs."))
}

func databaseStartCmd() *cobra.Command {
	return &cobra.Command{Use: "start", Short: "Start the database on this computer (MetaRush also does when it needs it)",
		RunE: func(c *cobra.Command, _ []string) error {
			srv, _, err := localDatabase(c)
			if err != nil {
				return err
			}
			if srv == nil {
				return noLocalDatabase(c)
			}
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			was := srv.Status(ctx).Running
			if err := srv.Start(ctx); err != nil {
				return failErr(c, databaseErr(err, "The database on this computer didn't start"))
			}
			st := srv.Status(ctx)
			return emit(c, map[string]any{"started": !was, "status": st}, func() {
				if was {
					fmt.Println("The database on this computer was already running.")
				} else {
					fmt.Println("Started the database on this computer.")
				}
			})
		}}
}

// daemonHere says whether MetaRush runs in the background for this home: it
// keeps the database running, so stopping or upgrading the database waits
// for it to stop first.
func daemonHere(h string) bool {
	_, ok := service.ReadDaemon(h)
	return ok
}

func databaseStopCmd() *cobra.Command {
	return &cobra.Command{Use: "stop", Short: "Stop the database on this computer cleanly",
		RunE: func(c *cobra.Command, _ []string) error {
			srv, h, err := localDatabase(c)
			if err != nil {
				return err
			}
			if srv == nil {
				return noLocalDatabase(c)
			}
			if daemonHere(h) {
				return failErr(c, apperr.New(apperr.Conflict, "MetaRush is running on this computer and keeps its database running.").
					WithHint("Stop MetaRush instead: that stops the database too.").WithCommand("metarush stop"))
			}
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			was := srv.PID() > 0
			if err := srv.Stop(ctx); err != nil {
				return failErr(c, databaseErr(err, "The database on this computer didn't stop"))
			}
			return emit(c, map[string]any{"stopped": was}, func() {
				if was {
					fmt.Println("Stopped the database on this computer. MetaRush starts it again when it needs it.")
				} else {
					fmt.Println("The database on this computer wasn't running.")
				}
			})
		}}
}

func databaseUpgradeCmd() *cobra.Command {
	var yes, dryRun bool
	c := &cobra.Command{Use: "upgrade", Short: "Move the database on this computer to the version this MetaRush comes with (a person, in a terminal)",
		Long: "The new version is downloaded from MongoDB and checked first; then the database stops for a moment and\n" +
			"starts again on the new version with all your projects. It never goes back to an older version, and it\n" +
			"moves one release at a time. MetaRush must not be running meanwhile (`metarush stop`). Only a person at this\n" +
			"computer upgrades it; anyone, an agent too, may ask what it would do (--dry-run).",
		RunE: func(c *cobra.Command, _ []string) error {
			srv, h, err := localDatabase(c)
			if err != nil {
				return err
			}
			if srv == nil {
				return noLocalDatabase(c)
			}
			step, err := srv.PlanUpgrade()
			if errors.Is(err, localdb.ErrUpToDate) {
				return emit(c, map[string]any{"upgraded": false, "upToDate": true, "step": step}, func() {
					fmt.Println("The database on this computer is up to date.")
				})
			}
			if err != nil {
				return failErr(c, databaseErr(err, "The database on this computer can't be upgraded"))
			}
			if dryRun {
				return emit(c, map[string]any{"upgraded": false, "dryRun": true, "step": step}, func() {
					if step.Finish {
						fmt.Println("Upgrading would finish the database's last upgrade. Nothing was changed.")
					} else {
						fmt.Println("Upgrading would move the database on this computer to a newer version, keeping all your projects. Nothing was changed.")
					}
					if detailsMode(c) {
						fmt.Printf("  MongoDB %s → %s\n", step.From, step.To)
					}
				})
			}
			// People only (D254), like other changes to this computer: an
			// agent plans it (--dry-run); a person at a terminal runs it.
			if !interactive() || jsonMode(c) {
				return failErr(c, apperr.New(apperr.HumanOnly, "Only a person at this computer can upgrade its database.").
					WithHint("Run it in your own terminal. To see what it would do first, ask for a dry run.").
					WithCommand("metarush database upgrade (dry run: metarush database upgrade --dry-run)"))
			}
			if daemonHere(h) {
				return failErr(c, apperr.New(apperr.Conflict, "MetaRush is running on this computer; it must stop while the database moves to the new version.").
					WithHint("Stop MetaRush, upgrade the database, then open MetaRush again.").WithCommand("metarush stop && metarush database upgrade"))
			}
			if !yes && !askYes("Upgrade the database on this computer now? It stops for a moment and keeps all your projects.", true) {
				return fail(c, "E_CANCELED", "Nothing was changed.", "")
			}
			cfg, err := config.Load(h)
			if err != nil {
				return notSetUp(c, h, err)
			}
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			ctx, stop := context.WithTimeout(ctx, 30*time.Minute)
			defer stop()
			if !jsonMode(c) && !step.Finish {
				fmt.Printf("Downloading the new version of the database (about %d MB)…\n", srv.DownloadMB())
			}
			admin, err := localAdminURI(cfg)
			if err != nil {
				return failErr(c, databaseErr(err, "The database on this computer can't be upgraded"))
			}
			step, err = srv.Upgrade(ctx, admin, nil)
			if err != nil {
				return failErr(c, databaseErr(err, "The database on this computer wasn't upgraded"))
			}
			return emit(c, map[string]any{"upgraded": true, "step": step}, func() {
				fmt.Println("The database on this computer runs the new version, with all your projects.")
				if detailsMode(c) {
					fmt.Printf("  MongoDB %s → %s\n", step.From, step.To)
				}
			})
		}}
	c.Flags().BoolVar(&yes, "yes", false, "don't ask again (still a person at a terminal)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "say what upgrading would do, change nothing")
	return c
}

// databaseErr is a plain error about the database on this computer: what
// happened, then the reason localdb gives in words. What MongoDB or the
// system said (its cause) goes into Details only.
func databaseErr(err error, what string) error {
	if errors.Is(err, context.Canceled) {
		return apperr.New("E_CANCELED", "%s: it was stopped.", what)
	}
	if le := (*localdb.Error)(nil); errors.Is(err, localdb.ErrKernel) && errors.As(err, &le) {
		// This computer's Linux kernel stops MongoDB 8 (D339): the words say why
		// and what to do; nothing about the database itself is wrong.
		return apperr.New(apperr.Database, "%s", sentence(le.Plain)).WithHint(le.Next).
			WithDetails(map[string]any{"problem": localdb.ErrKernel.Error(), "kernel": localdb.ReadKernel().Release})
	}
	if errors.Is(err, localdb.ErrNoBuild) {
		return apperr.New(apperr.NotConfigured, "%s: MongoDB doesn't make its database for this kind of computer.", what).
			WithHint("Use a database elsewhere instead, such as MongoDB Atlas (it has a free tier).")
	}
	var unsafe *secretpath.UnsafeError
	if errors.As(err, &unsafe) {
		ae := apperr.New(apperr.UnsafeSecretPath, "%s: %s.", what, strings.TrimSuffix(localdb.ErrUnsafePlace.Error(), ".")).
			WithDetails(map[string]any{"problem": unsafe.Error(), "path": unsafe.Verdict.Path, "service": unsafe.Verdict.Service, "root": unsafe.Verdict.Root})
		if unsafe.Verdict.Kind == secretpath.KindGit {
			return ae.WithHint("Set MetaRush up in a folder outside the repository.").WithCommand("metarush setup --home <a folder outside the repository>")
		}
		return ae.WithHint("Set MetaRush up in a folder outside synced folders, such as the suggested one in your home folder.").
			WithCommand("metarush setup --home ~/.metarush")
	}
	if ae := (*apperr.Error)(nil); errors.As(err, &ae) {
		return ae // already plain (an unsafe place for a secret, say)
	}
	var ue interface{ AppError() *apperr.Error }
	if errors.As(err, &ue) {
		return ue.AppError()
	}
	plain, details := err.Error(), map[string]any{}
	var le *localdb.Error
	if errors.As(err, &le) {
		plain = le.Plain
		if le.Cause != nil {
			details["problem"] = le.Cause.Error()
		}
	}
	return apperr.New(apperr.Database, "%s: %s.", what, strings.TrimSuffix(plain, ".")).
		WithHint("Its log says more.").WithDetails(details).WithCommand("metarush database status --details")
}
