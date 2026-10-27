package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/service"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/version"
)

// serviceCmd installs the daemon as a per-user background service (D76).
// Local admin commands: they change this machine's login items, not shared
// state, so they are not ops.
func serviceCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "service", Short: "Run MetaRush in the background: it starts when you log in and restarts if it stops (launchd on macOS, systemd on Linux)",
		Long: "For a machine nobody sits at, such as a Mac mini that renders for the team, and for anyone who wants the\n" +
			"dashboard always there. The service runs MetaRush with the dashboard (or, with --worker-only, just the\n" +
			"background work) and keeps a log (`metarush service logs`)."}
	cmd.AddCommand(serviceInstallCmd(), serviceUninstallCmd(), serviceStatusCmd(), serviceRestartCmd(), serviceLogsCmd(), serviceReapCmd())
	return cmd
}

func serviceOptions(c *cobra.Command, workerOnly bool, binary string) (service.Options, *config.Config, error) {
	h, err := home()
	if err != nil {
		return service.Options{}, nil, failErr(c, err)
	}
	cfg, err := config.Load(h)
	if err != nil {
		return service.Options{}, nil, notSetUp(c, h, err)
	}
	exe := binary
	if exe == "" {
		if exe, err = service.StableExecutable(); err != nil {
			return service.Options{}, nil, failErr(c, err)
		}
	} else if exe, err = filepath.Abs(expandHome(exe)); err != nil {
		return service.Options{}, nil, failErr(c, err)
	}
	return service.Options{Home: h, Exe: exe, WorkerOnly: workerOnly, Tools: tools.New(cfg.Tools)}, cfg, nil
}

func serviceInstallCmd() *cobra.Command {
	var workerOnly, dryRun, noWait bool
	var binary string
	c := &cobra.Command{Use: "install", Short: "Set up (or update) the background service on this machine and start it",
		RunE: func(c *cobra.Command, _ []string) error {
			o, cfg, err := serviceOptions(c, workerOnly, binary)
			if err != nil {
				return err
			}
			if dryRun {
				plan, err := o.InstallPlan()
				if err != nil {
					return fail(c, apperr.InvalidInput, err.Error(), "")
				}
				return emit(c, map[string]any{"dryRun": true, "plan": plan}, func() { printPlan("Would write", plan) })
			}
			if _, err := os.Stat(o.Exe); err != nil {
				return failErr(c, apperr.New(apperr.InvalidInput, "MetaRush's program file isn't where the service would run it from.").
					WithHint("Pass --binary with the path of the metarush program.").WithDetails(map[string]any{"binary": o.Exe}))
			}
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			// A daemon `metarush dashboard` started for this home stops first:
			// from now on the service runs it.
			stopped, oldPID := false, 0
			if d, ok := service.ReadDaemon(cfg.Home()); ok {
				oldPID = d.PID
				if d.Service == "" {
					if err := syscall.Kill(d.PID, syscall.SIGTERM); err == nil {
						stopped = true
						waitGone(cfg.Home(), 15*time.Second)
					}
				}
			}
			plan, err := o.Install(ctx)
			if err != nil {
				return failErr(c, apperr.Wrap(apperr.ToolFailed, err, "Setting up the background service failed.").
					WithHint("Its status says more.").WithCommand("metarush service status"))
			}
			started := false
			if !noWait {
				started = waitService(ctx, o, cfg, 40*time.Second, oldPID)
			}
			st := o.Get(ctx)
			res := map[string]any{"plan": plan, "status": st, "stoppedDaemon": stopped, "started": started, "waited": !noWait}
			if !noWait && !started {
				lines, _ := service.LogTail(o.LogFile(), 20)
				return failErr(c, apperr.New(apperr.ToolFailed, "The background service is set up, but MetaRush did not start.").
					WithHint("Its log says why; fix that, then restart the service.").
					WithDetails(map[string]any{"status": st, "log": lines, "command": "metarush service logs, then metarush service restart"}))
			}
			return emit(c, res, func() {
				if stopped {
					fmt.Println("Stopped the MetaRush the dashboard had started; the service runs it from now on.")
				}
				what := "MetaRush (with the dashboard)"
				if workerOnly {
					what = "MetaRush's background work (no dashboard)"
				}
				fmt.Printf("The background service is set up: %s now starts when you log in and restarts if it stops.\n", what)
				if noWait {
					fmt.Println("It wasn't checked yet, so it may still be starting.")
				}
				if cfg.SecretMode() == "env" {
					fmt.Println("Note: the service reads this machine's passwords and keys from MetaRush's own file, not from your terminal's environment.")
				}
				if detailsMode(c) {
					fmt.Println("logs: metarush service logs\nstatus: metarush service status\nremove: metarush service uninstall")
					if cfg.SecretMode() == "env" {
						fmt.Println("secrets file: " + config.DotEnvPath(cfg.Home()))
					}
				} else {
					fmt.Println("(Add --details to see the commands for its log, its status and removing it.)")
				}
			})
		}}
	c.Flags().BoolVar(&workerOnly, "worker-only", false, "run only the worker and background jobs, without the dashboard (for a machine that renders for others)")
	c.Flags().StringVar(&binary, "binary", "", "the metarush binary the service runs (default: this one)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "show the file and the commands without changing anything")
	c.Flags().BoolVar(&noWait, "no-wait", false, "do not wait for MetaRush to start")
	return c
}

func printPlan(verb string, plan service.Plan) {
	fmt.Printf("%s %s:\n\n%s\n", verb, plan.File, plan.Content)
	fmt.Println("Then run:")
	for _, cmd := range plan.Commands {
		fmt.Println("  " + strings.Join(cmd, " "))
	}
}

func serviceUninstallCmd() *cobra.Command {
	var dryRun bool
	c := &cobra.Command{Use: "uninstall", Short: "Stop the background service and remove it (MetaRush no longer starts at login)",
		RunE: func(c *cobra.Command, _ []string) error {
			o, _, err := serviceOptions(c, false, "")
			if err != nil {
				return err
			}
			if dryRun {
				plan, err := o.UninstallPlan()
				if err != nil {
					return fail(c, apperr.InvalidInput, err.Error(), "")
				}
				return emit(c, map[string]any{"dryRun": true, "plan": plan}, func() {
					fmt.Println("Would run:")
					for _, cmd := range plan.Commands {
						fmt.Println("  " + strings.Join(cmd, " "))
					}
					fmt.Println("and remove " + plan.File)
				})
			}
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			was := o.Get(ctx)
			plan, err := o.Uninstall(ctx)
			if err != nil {
				return failErr(c, apperr.Wrap(apperr.ToolFailed, err, "Removing the background service failed."))
			}
			return emit(c, map[string]any{"removed": was.Installed, "plan": plan}, func() {
				if !was.Installed {
					fmt.Println("MetaRush wasn't set up as a background service on this machine.")
					return
				}
				fmt.Println("The background service is removed: MetaRush no longer starts when you log in. Opening the dashboard starts it when you need it.")
			})
		}}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "show the commands without changing anything")
	return c
}

func serviceStatusCmd() *cobra.Command {
	var details bool
	c := &cobra.Command{Use: "status", Short: "Say whether the background service is installed and running",
		RunE: func(c *cobra.Command, _ []string) error {
			o, _, err := serviceOptions(c, false, "")
			if err != nil {
				return err
			}
			st := o.Get(c.Context())
			return emit(c, st, func() { printServiceStatus(st, details) })
		}}
	addDetails(c, &details)
	return c
}

func printServiceStatus(st service.Status, details bool) {
	if !st.Installed {
		fmt.Println("MetaRush isn't set up as a background service on this machine.")
		if details {
			if st.Problem != "" {
				fmt.Println("  problem: " + st.Problem)
			}
			fmt.Println("  set it up: metarush service install (add --worker-only for a machine that only renders)")
		} else {
			fmt.Println("(Add --details to see the command that sets it up.)")
		}
		return
	}
	mode := "with the dashboard"
	if st.WorkerOnly {
		mode = "for background work only (no dashboard)"
	}
	switch {
	case st.Running:
		fmt.Printf("MetaRush is running in the background %s, and starts when you log in.\n", mode)
		if st.Problem != "" {
			fmt.Println("  It runs a different copy of MetaRush than this one; set the service up again with the copy you want it to run.")
		}
	case !st.Loaded:
		fmt.Printf("MetaRush is set up as a background service %s, but it isn't loaded.\n", mode)
		fmt.Println("  Set the service up again (it also loads the next time you log in).")
	default:
		fmt.Printf("MetaRush is set up as a background service %s, but it isn't running.\n", mode)
		fmt.Println("  Restart it; its log says why it stopped.")
	}
	if st.Linger != nil && !*st.Linger {
		fmt.Println("  It runs only while you are logged in.")
	}
	if details {
		if st.Problem != "" {
			fmt.Printf("  problem: %s\n", st.Problem)
		}
		if st.Fix != "" {
			fmt.Printf("  fix: %s\n", st.Fix)
		}
		if st.Linger != nil && !*st.Linger {
			fmt.Println("  for a machine nobody logs in to: loginctl enable-linger " + os.Getenv("USER"))
		}
		fmt.Printf("  %s %s\n  file %s\n  runs %s\n  log %s\n", st.Platform, st.Label, st.File, st.Binary, st.Log)
		if st.PID > 0 {
			fmt.Printf("  pid %d\n", st.PID)
		}
		if st.LastExit != nil {
			fmt.Printf("  last exit code %d\n", *st.LastExit)
		}
	}
}

func serviceRestartCmd() *cobra.Command {
	var noWait bool
	c := &cobra.Command{Use: "restart", Short: "Restart the background service (after updating MetaRush, or to start it again)",
		RunE: func(c *cobra.Command, _ []string) error {
			o, cfg, err := serviceOptions(c, false, "")
			if err != nil {
				return err
			}
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			oldPID := 0
			if d, ok := service.ReadDaemon(cfg.Home()); ok {
				oldPID = d.PID
			}
			if err := o.Restart(ctx); err != nil {
				return failErr(c, apperr.Wrap(apperr.ToolFailed, err, "The background service couldn't be restarted.").
					WithHint("Setting it up again fixes most problems.").WithCommand("metarush service install"))
			}
			cur := o.Get(ctx)
			o.WorkerOnly = cur.WorkerOnly
			if cur.Binary != "" {
				o.Exe = cur.Binary // the copy the service runs (for the build check)
			}
			started := false
			if !noWait {
				started = waitService(ctx, o, cfg, 60*time.Second, oldPID)
				if !started {
					return failErr(c, apperr.New(apperr.ToolFailed, "MetaRush did not start again.").WithHint("Its log says why.").WithCommand("metarush service logs"))
				}
			}
			st := o.Get(ctx)
			return emit(c, map[string]any{"status": st, "started": started}, func() { fmt.Println("Restarted the background service.") })
		}}
	c.Flags().BoolVar(&noWait, "no-wait", false, "do not wait for MetaRush to start")
	return c
}

func serviceLogsCmd() *cobra.Command {
	var lines int
	var follow bool
	c := &cobra.Command{Use: "logs", Short: "Show the end of the log of MetaRush running in the background",
		RunE: func(c *cobra.Command, _ []string) error {
			h, err := home()
			if err != nil {
				return failErr(c, err)
			}
			path := filepath.Join(h, "logs", "daemon.log")
			out, err := service.LogTail(path, lines)
			if err != nil {
				if os.IsNotExist(err) {
					return emit(c, map[string]any{"path": path, "lines": []string{}}, func() { fmt.Println("There is no log yet.") })
				}
				return failErr(c, err)
			}
			if !follow || jsonMode(c) {
				return emit(c, map[string]any{"path": path, "lines": out}, func() {
					for _, l := range out {
						fmt.Println(l)
					}
				})
			}
			for _, l := range out {
				fmt.Println(l)
			}
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			return followFile(ctx, path)
		}}
	c.Flags().IntVarP(&lines, "lines", "n", 100, "how many lines")
	c.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines (Ctrl-C stops)")
	return c
}

// followFile prints what is appended to a file until ctx ends.
func followFile(ctx context.Context, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(0, 2); err != nil {
		return err
	}
	buf := make([]byte, 64<<10)
	for {
		n, _ := f.Read(buf)
		if n > 0 {
			os.Stdout.Write(buf[:n])
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
}

// waitService waits until the service's MetaRush answers: a daemon the
// service started (not the process notPID it replaces) whose dashboard's
// health check reports this build when the service runs this binary, or
// (worker only) its running daemon record.
func waitService(ctx context.Context, o service.Options, cfg *config.Config, d time.Duration, notPID int) bool {
	deadline := time.Now().Add(d)
	same := false
	if exe, err := os.Executable(); err == nil {
		if a, err1 := os.Stat(exe); err1 == nil {
			if b, err2 := os.Stat(o.Exe); err2 == nil {
				same = os.SameFile(a, b)
			}
		}
	}
	for time.Now().Before(deadline) {
		if dm, ok := service.ReadDaemon(o.Home); ok && dm.Service != "" && dm.PID != notPID {
			if o.WorkerOnly || (healthy(cfg) && (!same || healthCommit(cfg) == version.Commit)) {
				return true
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
	return false
}

// healthCommit is the build the running dashboard reports.
func healthCommit(cfg *config.Config) string {
	client := http.Client{Timeout: 1500 * time.Millisecond}
	res, err := client.Get(fmt.Sprintf("http://%s:%d/api/health", cfg.Dashboard.Host, cfg.Dashboard.Port))
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	var h struct {
		Commit string `json:"commit"`
	}
	_ = json.NewDecoder(res.Body).Decode(&h)
	return h.Commit
}

// waitGone waits for a home's daemon to exit.
func waitGone(home string, d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, ok := service.ReadDaemon(home); !ok {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// serviceReapCmd is the systemd unit's ExecStopPost: with KillMode=process
// only the daemon is signalled when the service stops, so after a crash this
// stops what it left in the service's group — never the database server
// (D254). It refuses to run anywhere but as that service.
func serviceReapCmd() *cobra.Command {
	return &cobra.Command{Use: "reap", Hidden: true, Short: "The background service's clean-up after MetaRush stops (systemd only)",
		RunE: func(c *cobra.Command, _ []string) error {
			h, err := home()
			if err != nil {
				return failErr(c, err)
			}
			pids, note, err := service.Reap(service.Options{Home: h}.Label())
			if err != nil {
				return failErr(c, apperr.New(apperr.InvalidInput, "This is the background service's own clean-up, and runs only as part of it."))
			}
			return emit(c, map[string]any{"stopped": pids, "note": note}, func() {
				if len(pids) > 0 {
					fmt.Printf("Stopped %s MetaRush had left running.\n", plural(len(pids), "program"))
				}
				if note != "" {
					fmt.Println("clean-up: " + note) // the service's log
				}
			})
		}}
}
