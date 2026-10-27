package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/components"
	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/tools"
)

// rendererCmd manages this machine's renderers (D75). These are local admin
// commands like doctor: they need no database, so they work before `init`
// and while the database is unreachable.
func rendererCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "renderer", Short: "Set up and check the renderer that comes with this version of MetaRush",
		Long: "Every version of MetaRush makes its videos with its own renderer, so your renders only change when you update\n" +
			"MetaRush. It is set up by itself when MetaRush starts on this machine, or with `metarush renderer install`."}
	cmd.AddCommand(rendererStatusCmd(), rendererInstallCmd(), rendererPruneCmd(), rendererDevCmd(), rendererExportCmd())
	return cmd
}

// rendererEnv is this installation's renderer, from config.yaml when the home
// is initialized (defaults otherwise). The renderer needs no secrets: when the
// full config cannot be loaded (a locked keychain over SSH, a missing
// variable), its tools section is enough.
func rendererEnv() (*renderer.Env, *config.Config, error) {
	h, err := home()
	if err != nil {
		return nil, nil, err
	}
	cfg, err := config.Load(h)
	if err != nil {
		cfg = config.Default(h)
		if config.Exists(h) {
			t, terr := config.LoadTools(h)
			if terr != nil {
				return nil, nil, err
			}
			cfg.Tools = t
		}
	}
	return renderer.New(cfg, tools.New(cfg.Tools)), cfg, nil
}

func rendererStatusCmd() *cobra.Command {
	var details bool
	c := &cobra.Command{Use: "status", Short: "Say whether this version's renderer is set up, and which others are kept",
		RunE: func(c *cobra.Command, _ []string) error {
			env, _, err := rendererEnv()
			if err != nil {
				return failErr(c, err)
			}
			st := env.Check(true)
			if details {
				for i := range st.Versions {
					st.Versions[i].Bytes = dirSizeOf(filepath.Join(renderer.Root(env.Home), st.Versions[i].ID))
				}
			}
			return emit(c, st, func() { printRendererStatus(st, details) })
		}}
	addDetails(c, &details)
	return c
}

func printRendererStatus(st renderer.Status, details bool) {
	what := "The renderer for this version of MetaRush"
	if st.Source == renderer.SourceCheckout {
		what = "The developer copy of the video templates this machine renders from (a developer setting)"
	}
	switch {
	case st.Ready:
		fmt.Print(what + " is ready")
		if st.InstalledAt != nil {
			fmt.Printf(" (set up %s)", st.InstalledAt.Local().Format("Jan 2"))
		}
		fmt.Println(".")
		if !st.Browser {
			fmt.Println("  The first render also downloads the browser it draws the video in.")
		}
	default:
		say, do := rendererProblem(st)
		fmt.Println(say + ".")
		fmt.Println("  " + do)
	}
	if st.Notice != "" && st.Source == renderer.SourceBundled {
		fmt.Println("\n" + rendererNoticeWords)
	}
	var others []string
	for _, v := range st.Versions {
		if v.Current {
			continue
		}
		s := v.Version
		if s == "" {
			s = "an older version"
		}
		if v.InstalledAt != nil {
			s += " (" + v.InstalledAt.Local().Format("Jan 2") + ")"
		}
		others = append(others, s)
	}
	if len(others) > 0 {
		fmt.Printf("\nAlso kept on this machine, for older versions of MetaRush: %s.\n", strings.Join(others, ", "))
	}
	if details {
		fmt.Printf("\n  version %s, Remotion %s\n", st.Version, orText(st.Remotion, "unknown"))
		if st.InstalledAt != nil {
			fmt.Printf("  set up %s\n", st.InstalledAt.Local().Format("Jan 2 15:04"))
		}
		if !st.Ready {
			fmt.Printf("  problem: %s\n", st.Problem)
			if st.Fix != "" {
				fmt.Printf("  fix: %s\n", st.Fix)
			}
		}
		if st.Notice != "" {
			fmt.Printf("  notice: %s\n", st.Notice)
		}
		if len(others) > 0 {
			fmt.Println("  remove older ones: metarush renderer prune")
		}
		fmt.Printf("  id %s\n  folder %s\n  renders run in %s\n", st.ID, st.Dir, st.VideoDir)
		if st.Digest != "" {
			fmt.Printf("  package digest %s\n", st.Digest)
		}
		for _, v := range st.Versions {
			cur := ""
			if v.Current {
				cur = " (this version)"
			}
			fmt.Printf("  %s%s %s\n", v.ID, cur, fmtBytes(v.Bytes))
		}
		if st.LastError != "" {
			fmt.Println("  last setup error: " + st.LastError)
		}
	}
}

func rendererInstallCmd() *cobra.Command {
	var force bool
	var keep int
	c := &cobra.Command{Use: "install", Short: "Set up (or repair) the renderer for this version: its packages (npm ci) and its browser",
		RunE: func(c *cobra.Command, _ []string) error {
			env, _, err := rendererEnv()
			if err != nil {
				return failErr(c, err)
			}
			// `renderer install` is itself a request to set the renderer up,
			// like `setup --components renderer` (D325, D327): scripts run it
			// with no terminal (deploy.sh over SSH, scripts/acceptance.sh, the
			// Homebrew cask's next step), always under set -e.
			//   - ready (no --force): "already set up", exit 0, for anyone;
			//   - an AI app: never a download it would start (one nobody
			//     chose, waiting or left out): a note, exit 0; repairing or
			//     upgrading a wanted one stays possible (deploy from Claude Code);
			//   - a person left it out: a script doesn't override that by
			//     accident — a note, exit 0, unless --force;
			//   - otherwise it is set up, and the choice says who asked.
			pre := env.Check(false)
			if pre.Ready && !force {
				return emit(c, pre, func() { fmt.Println("The renderer for this version is already set up.") })
			}
			agent := agentAtTheKeys(c)
			person := !agent && isInteractive()
			ch, chosen := choice.Of(env.Home, renderer.Component)
			leftOut := chosen && !ch.Wanted && !ch.Waiting // a person said no
			upgrade := !chosen && env.SetsItselfUp()       // nobody chose; one was set up here before
			notNow := func(note, cmd string) error {
				return emit(c, map[string]any{"status": pre, "setUp": false, "says": note, "command": cmd}, func() {
					fmt.Println(note)
					if detailsMode(c) && cmd != "" {
						fmt.Println("to set it up anyway: " + cmd)
					}
				})
			}
			switch {
			case agent && !env.SetsItselfUp():
				note := "The renderer isn't set up on this computer, and nothing was set up now: a person adds it in Settings → Components (it downloads about 400 MB)."
				if leftOut {
					note = "The renderer was left out on this computer, so nothing was set up: a person adds it in Settings → Components."
				}
				return notNow(note, "metarush components add renderer")
			case leftOut && !person && !force:
				return notNow("The renderer was left out on this computer, so nothing was set up: a person adds it in Settings → Components.",
					"metarush renderer install --force")
			}
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			if n := env.NoticeOnce(); n != "" && !jsonMode(c) {
				fmt.Fprintln(os.Stderr, n)
			}
			last := ""
			st, err := env.Install(ctx, renderer.InstallOptions{Force: force, Keep: keep, Progress: func(s renderer.Step) {
				if jsonMode(c) {
					return
				}
				if s.Stage == "browser" && s.Progress > 0 {
					fmt.Fprintf(os.Stderr, "\r  %s", s.Message)
					if s.Progress >= 1 {
						fmt.Fprintln(os.Stderr)
					}
					return
				}
				if s.Stage != last {
					last = s.Stage
					fmt.Fprintln(os.Stderr, s.Message)
				}
			}})
			if err != nil {
				return failErr(c, err)
			}
			// The choice says what really happened (D325).
			switch {
			case person:
				_ = choice.Set(env.Home, map[string]bool{renderer.Component: true}, "a person at a terminal")
			case chosen && ch.Wanted:
				// already wanted: a repair or an upgrade keeps who chose it
			case upgrade:
				_ = choice.Set(env.Home, map[string]bool{renderer.Component: true}, "an upgrade of the renderer set up here before")
			case leftOut:
				_ = choice.Set(env.Home, map[string]bool{renderer.Component: true}, "a command run without a terminal, with --force")
			default:
				_ = choice.Set(env.Home, map[string]bool{renderer.Component: true}, "a command run without a terminal")
			}
			return emit(c, st, func() {
				if last == "" {
					fmt.Println("The renderer for this version is already set up.")
					if detailsMode(c) {
						fmt.Printf("Remotion %s\nset it up again: metarush renderer install --force\n", st.Remotion)
					}
				}
			})
		}}
	c.Flags().BoolVar(&force, "force", false, "set it up again even when it is ready")
	c.Flags().IntVar(&keep, "keep", 0, fmt.Sprintf("renderers to keep afterwards, this one included (default %d; -1 keeps all)", renderer.DefaultKeep))
	return c
}

func rendererPruneCmd() *cobra.Command {
	var keep int
	c := &cobra.Command{Use: "prune", Short: fmt.Sprintf("Remove older renderers (keeps this version's and the newest others, %d in all)", renderer.DefaultKeep),
		RunE: func(c *cobra.Command, _ []string) error {
			env, _, err := rendererEnv()
			if err != nil {
				return failErr(c, err)
			}
			ctx, cancel := context.WithTimeout(c.Context(), 2*time.Minute)
			defer cancel()
			res, err := env.Prune(ctx, keep)
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, res, func() {
				if len(res.Removed) == 0 {
					fmt.Println("Nothing to remove.")
				} else {
					fmt.Printf("Removed %d older %s (%s freed).\n", len(res.Removed), map[bool]string{true: "renderer", false: "renderers"}[len(res.Removed) == 1], fmtBytes(res.FreedBytes))
				}
				if len(res.InUse) > 0 {
					fmt.Printf("Kept %d that a running render still uses; run prune again later.\n", len(res.InUse))
				}
			})
		}}
	c.Flags().IntVar(&keep, "keep", renderer.DefaultKeep, "renderers to keep, this version's included")
	return c
}

func rendererDevCmd() *cobra.Command {
	var off bool
	c := &cobra.Command{Use: "dev <checkout's video folder> | --off",
		Short: "Developer setting: render from a live checkout (for Remotion Studio work) instead of this version's renderer",
		Long: "Points this installation at a source checkout's video/ package (tools.videoDir) and turns on tools.devRenderer, so\n" +
			"renders, stills and motion-graphic checks use whatever is in that checkout right now. Use a separate home for\n" +
			"development (the repository's .dev/home): an installation people rely on should render with its own version.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			h, err := home()
			if err != nil {
				return failErr(c, err)
			}
			if !config.Exists(h) {
				return failErr(c, apperr.New(apperr.NotConfigured, "MetaRush isn't set up on this machine yet.").WithHint("Set it up first.").WithCommand("metarush init"))
			}
			if off == (len(args) == 1) {
				return fail(c, apperr.InvalidInput, "Say which checkout to render from, or --off.", "Give the path of a source checkout's video folder.")
			}
			if off {
				if err := config.SetField(h, []string{"tools", "devRenderer"}, false); err != nil {
					return failErr(c, err)
				}
			} else {
				dir, err := filepath.Abs(expandHome(args[0]))
				if err != nil {
					return failErr(c, err)
				}
				if _, err := os.Stat(filepath.Join(dir, "render", "render.mjs")); err != nil {
					return failErr(c, apperr.New(apperr.InvalidInput, "That folder isn't a checkout's video folder (it has no renderer in it).").
						WithHint("Give the video folder of a MetaRush source checkout.").WithDetails(map[string]any{"folder": dir, "missing": "render/render.mjs"}))
				}
				if err := config.SetField(h, []string{"tools", "videoDir"}, dir); err != nil {
					return failErr(c, err)
				}
				if err := config.SetField(h, []string{"tools", "devRenderer"}, true); err != nil {
					return failErr(c, err)
				}
			}
			env, _, err := rendererEnv()
			if err != nil {
				return failErr(c, err)
			}
			st := env.Check(false)
			return emit(c, st, func() {
				if off {
					fmt.Println("Renders use this version's own renderer again.")
				} else {
					fmt.Println("Renders now use the checkout's video folder (a developer setting).")
				}
				if !st.Ready {
					say, do := rendererProblem(st)
					fmt.Println(say + ".")
					fmt.Println("  " + do)
				}
				fmt.Println("Restart MetaRush for it to take effect.")
				if detailsMode(c) {
					if !st.Ready {
						fmt.Printf("problem: %s\nfix: %s\n", st.Problem, orText(st.Fix, "-"))
					}
					if !off {
						fmt.Println("go back: metarush renderer dev --off")
					}
					fmt.Println("restart: metarush stop, then metarush dashboard (or metarush service restart)")
				} else {
					fmt.Println("(Add --details to see the commands.)")
				}
			})
		}}
	c.Flags().BoolVar(&off, "off", false, "turn the developer setting off")
	return c
}

// rendererExportCmd writes the renderer package to a folder (release archives
// carry it next to the binary). Hidden: a build tool, not an everyday command.
func rendererExportCmd() *cobra.Command {
	var dir string
	c := &cobra.Command{Use: "export --dir <folder>", Short: "Write the renderer package this binary carries to a folder", Hidden: true,
		RunE: func(c *cobra.Command, _ []string) error {
			if dir == "" {
				return fail(c, apperr.InvalidInput, "say where to write it (--dir)", "")
			}
			pkg, err := renderer.Export(dir)
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, map[string]any{"dir": dir, "files": len(pkg.Files()), "digest": pkg.Digest(), "remotion": pkg.Remotion(), "id": pkg.ID()}, func() {
				fmt.Printf("Wrote the renderer package (%d files, Remotion %s) to %s\n", len(pkg.Files()), pkg.Remotion(), dir)
			})
		}}
	c.Flags().StringVar(&dir, "dir", "", "folder to write it to")
	return c
}

// setUpRendererIfNew sets this version's renderer up in the terminal the
// first time a render is asked for here (when no daemon did it yet). A
// renderer that was removed or damaged is not set up again by itself: the
// error (not yet printed; the caller reports it) says what to do.
func setUpRendererIfNew(c *cobra.Command, a *app.App, quiet bool) error {
	env := renderer.New(a.Cfg, a.Tools)
	st := env.Check(false)
	if st.Ready || st.State == renderer.StateInstalling {
		return nil
	}
	if st.Source != renderer.SourceBundled || st.State != renderer.StateMissing {
		return &renderer.NotReady{Status: st} // left out, removed, damaged, no Node.js, or a broken developer checkout
	}
	if !env.SetsItselfUp() && (agentAtTheKeys(c) || !isInteractive()) {
		// Nobody chose it (or it waits for a person) and nobody is at a
		// terminal to ask: setting it up downloads about 400 MB, a person's
		// decision (D327).
		return components.Missing(renderer.Component).WithHint("A person adds it in Settings → Components, or runs this in their own terminal.")
	}
	talk := !quiet && !jsonMode(c)
	if talk {
		fmt.Fprintln(os.Stderr, "Setting up the renderer for this version of MetaRush (one time; it needs the internet)…")
	}
	last := ""
	_, err := env.Install(c.Context(), renderer.InstallOptions{Progress: func(s renderer.Step) {
		if talk && s.Stage != last && !(s.Stage == "browser" && s.Progress > 0) {
			last = s.Stage
			fmt.Fprintln(os.Stderr, "  "+s.Message)
		}
	}})
	return err
}

// rendererNoticeWords is the renderer's move notice in plain words (the
// notice itself names the old folder and the command; it stays behind --details).
const rendererNoticeWords = "Renders now use the renderer that comes with this version of MetaRush, not the video folder this machine used before."

// rendererProblem says in plain words what is wrong with a renderer that
// isn't ready, and what to do (Status.Problem and Fix carry the folder and the
// exact commands).
func rendererProblem(st renderer.Status) (say, do string) {
	switch {
	case st.State == renderer.StateInstalling:
		return "The renderer for this version is being set up", "Renders start when it is ready (usually a few minutes)."
	case st.Source == renderer.SourceCheckout:
		return "Renders are set to use a developer copy of the video templates, and it can't render right now", "Fix the developer setting, or turn it off."
	case st.State == renderer.StateLeftOut:
		return "The Remotion renderer isn't added on this computer, so it doesn't render here", "Add it in Settings → Components."
	case st.State == renderer.StateUnavailable && st.ID == "":
		return "This copy of MetaRush has no usable renderer", "Reinstall MetaRush."
	case st.State == renderer.StateUnavailable:
		return "Node.js isn't installed, so this machine can't render", "Install Node.js 22 or newer."
	case st.State == renderer.StateDamaged:
		return "The renderer for this version is damaged", "Set it up again."
	case st.State == renderer.StateRemoved:
		return "The renderer for this version was removed from this machine", "Set it up again."
	}
	say = "The renderer for this version isn't set up on this machine yet"
	if st.LastError != "" {
		say += ", and the last try failed"
	}
	return say, "It is set up when MetaRush starts here, or you can set it up now."
}

// rendererChecks are doctor's renderer lines.
func rendererChecks(cfg *config.Config, ts *tools.Set, add addCheck) {
	env := renderer.New(cfg, ts)
	st := env.Check(true)
	switch {
	case st.Ready && st.Source == renderer.SourceCheckout:
		add("renderer", "renderer", "warn", "renders use the developer checkout at "+st.Dir+" (Remotion "+st.Remotion+"), not this version's renderer",
			"for an installation people rely on, run `metarush renderer dev --off`",
			"Renders use a developer copy of the video templates, not this version's own renderer", "On a machine people rely on, switch back to this version's renderer.")
	case st.Ready:
		detail := "ready for this version (Remotion " + st.Remotion + ")"
		say := "Ready to render"
		if !st.Browser {
			detail += " (its browser is downloaded with the first render)"
			say += " (the first render also downloads the browser it draws in)"
		}
		add("renderer", "renderer", "ok", detail, "", say)
	case st.State == renderer.StateInstalling:
		say, do := rendererProblem(st)
		add("renderer", "renderer", "info", st.Problem, st.Fix, say, do)
	case st.State == renderer.StateLeftOut:
		say, do := rendererProblem(st)
		add("renderer", "renderer", "info", st.Problem, st.Command(), say, do)
	default:
		say, do := rendererProblem(st)
		add("renderer", "renderer", "warn", st.Problem, st.Fix, say, do)
	}
	if st.Source == renderer.SourceBundled && st.Notice != "" {
		add("renderer", "old setting", "info", "tools.videoDir ("+cfg.Tools.VideoDir+") is no longer used: renders use this version's own renderer",
			"to render from that folder for template work: `metarush renderer dev "+cfg.Tools.VideoDir+"`")
	}
}

func dirSizeOf(dir string) int64 {
	var n int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			n += info.Size()
		}
		return nil
	})
	return n
}
