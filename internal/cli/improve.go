package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/improve"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/ops"
)

func improveCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "improve", Short: "Find what MetaRush should do better, from the activity log (with the metarush-improve skill)",
		Long: "How MetaRush learns what to do better: `improve signals` finds trouble in the activity log (errors, retries,\n" +
			"changes Claude asked for that were refused or left unanswered, people correcting Claude, things MetaRush couldn't\n" +
			"do, long chains of steps, slow steps); Claude Code groups them into missing features and friction in a report;\n" +
			"`improve save` keeps it as improvement ideas that people keep or dismiss here or on the Improvements page."}
	cmd.AddCommand(improveAddCmd())
	cmd.AddCommand(improveSignalsCmd(), improveSaveCmd(), improveListCmd(), improveShowCmd(), improveDecideCmd("keep", "improve.keep", "Keep improvement ideas (worth building)"),
		improveDecideCmd("dismiss", "improve.dismiss", "Dismiss improvement ideas (not worth building)"), improveExportCmd(), improveReportCmd(), improveScheduleCmd())
	return cmd
}

func improveReportCmd() *cobra.Command {
	var in improve.ReportIn
	var details bool
	c := &cobra.Command{Use: "report", Short: "Make an improvement report now: Claude Code reads the log's signals (headless, no shell) and the ideas are saved",
		Long: "Everything in one step, the same as the weekly report: it finds the signals in the log, has Claude Code (in the\n" +
			"background, only able to read those log entries and write the report) turn them into improvement ideas, and saves\n" +
			"them. It uses Claude Code on this machine (billed to its sign-in; --budget caps it).",
		RunE: func(c *cobra.Command, _ []string) error {
			if !jsonMode(c) && !in.DryRun {
				fmt.Fprintln(os.Stderr, "Making an improvement report (Claude Code reads the activity log; this takes a few minutes)…")
			}
			return runOp(c, "improve.report", in, func(o any) {
				r := o.(*improve.ReportOut)
				fmt.Println(r.Message)
				for _, w := range r.Warnings {
					fmt.Println("  note: " + w)
				}
				if details {
					fmt.Printf("  %d signals, %d entries, $%.2f, folder %s\n", r.Signals, r.Entries, r.CostUSD, r.Dir)
				}
			})
		}}
	c.Flags().StringVar(&in.Since, "since", "", "how far back (default 7d): a duration or a date")
	c.Flags().BoolVar(&in.DryRun, "dry-run", false, "prepare what Claude Code would read, without running it or saving anything")
	c.Flags().Float64Var(&in.BudgetUSD, "budget", 0, "spending limit in dollars for this report (default improve.budgetUsd, or 2)")
	c.Flags().StringVar(&in.Model, "model", "", "Claude model (default improve.model, else Claude Code's default)")
	addDetails(c, &details)
	return c
}

func improveScheduleCmd() *cobra.Command {
	var weekly, day string
	var hour int
	var budget float64
	c := &cobra.Command{Use: "schedule [--weekly on|off] [--day monday] [--hour 9] [--budget 2]",
		Short: "Show or change the weekly improvement report on this machine (off unless turned on; changing it is for people only)",
		RunE: func(c *cobra.Command, _ []string) error {
			print := func(o any) {
				s := o.(*improve.ScheduleOut)
				if s.Weekly {
					fmt.Printf("The weekly improvement report is on: %ss after %d:00", strings.Title(s.Day), s.Hour) //nolint:staticcheck // one ASCII word
					if s.NextRun != nil {
						fmt.Printf(" (next %s)", s.NextRun.Local().Format("Mon Jan 2 15:04"))
					}
					fmt.Println(".")
				} else {
					fmt.Println("The weekly improvement report is off.")
				}
				if s.LastResult != "" && s.LastRun != nil {
					fmt.Printf("Last time (%s): %s\n", s.LastRun.Local().Format("Mon Jan 2 15:04"), s.LastResult)
				}
				fmt.Println(s.Note)
			}
			changing := c.Flags().Changed("weekly") || c.Flags().Changed("day") || c.Flags().Changed("hour") || c.Flags().Changed("budget")
			if !changing {
				return runOp(c, "improve.schedule", improve.ScheduleIn{}, print)
			}
			var in improve.ScheduleSetIn
			if c.Flags().Changed("weekly") {
				switch strings.ToLower(weekly) {
				case "on", "true", "yes":
					v := true
					in.Weekly = &v
				case "off", "false", "no":
					v := false
					in.Weekly = &v
				default:
					return fail(c, apperr.InvalidInput, "--weekly takes on or off", "")
				}
			}
			in.Day = day
			if c.Flags().Changed("hour") {
				in.Hour = &hour
			}
			if c.Flags().Changed("budget") {
				in.BudgetUSD = &budget
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "improve.schedule.set", in, ops.Caller{Human: interactive()}, func(o any) {
				print(o)
				fmt.Println("MetaRush picks the change up by itself.")
			})
		}}
	c.Flags().StringVar(&weekly, "weekly", "", "on or off")
	c.Flags().StringVar(&day, "day", "", "day of the week (default monday)")
	c.Flags().IntVar(&hour, "hour", 9, "local hour, 0-23 (default 9)")
	c.Flags().Float64Var(&budget, "budget", 2, "spending limit per report in dollars")
	return c
}

func improveSignalsCmd() *cobra.Command {
	var in improve.SignalsIn
	var file string
	var details bool
	c := &cobra.Command{Use: "signals", Short: "Find signals in the log: errors, retries, corrections, missing features, slow steps (default: last 30 days)",
		RunE: func(c *cobra.Command, _ []string) error {
			if file != "" {
				es, err := readEntries(file)
				if err != nil {
					return fail(c, apperr.InvalidInput, "That file isn't an exported activity log ("+err.Error()+").", "Pass a log export, or the entries file of a bug report.")
				}
				in.Entries = es
			}
			return runOp(c, "improve.signals", in, func(o any) { printSignals(o.(*improve.SignalsOut), details) })
		}}
	c.Flags().StringVar(&in.Since, "since", "", "how far back: a duration (30d) or a date")
	c.Flags().StringVar(&in.Until, "until", "", "up to this time")
	c.Flags().StringVar(&file, "file", "", "analyze an exported log (from `metarush log export` or a bug report's entries.jsonl) instead of the workspace's")
	addDetails(c, &details)
	return c
}

// readEntries reads JSON-lines log entries (an export or a bug report).
func readEntries(path string) ([]logbook.Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []logbook.Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 32<<20)
	line := 0
	for sc.Scan() {
		line++
		b := strings.TrimSpace(sc.Text())
		if b == "" {
			continue
		}
		var e struct {
			logbook.Entry
			Bodies *struct {
				Prompt   string `json:"prompt"`
				Response string `json:"response"`
				Stderr   string `json:"stderr"`
			} `json:"bodies"`
		}
		if err := json.Unmarshal([]byte(b), &e); err != nil {
			return nil, fmt.Errorf("line %d is not a log entry: %v", line, err)
		}
		if e.Bodies != nil { // full texts exported with --bodies
			if e.Claude != nil {
				if e.Claude.Prompt == "" {
					e.Claude.Prompt = e.Bodies.Prompt
				}
				if e.Claude.Response == "" {
					e.Claude.Response = e.Bodies.Response
				}
			}
			if e.Tool != nil && e.Bodies.Stderr != "" {
				e.Tool.StderrTail = e.Bodies.Stderr
			}
		}
		out = append(out, e.Entry)
	}
	return out, sc.Err()
}

var signalWords = map[string]string{improve.SigOpError: "Errors", improve.SigToolError: "Programs that failed", improve.SigJobFailure: "Failed jobs",
	improve.SigRetry: "Retries", improve.SigActionRejected: "Changes Claude asked for that were refused", improve.SigActionIgnored: "Changes left without an answer",
	improve.SigCorrection: "People correcting Claude", improve.SigRephrased: "Asked again in other words", improve.SigMissing: "Things MetaRush could not do",
	improve.SigDenied: "Blocked attempts", improve.SigLongChain: "Long chains of steps", improve.SigSlow: "Slow steps"}

func printSignals(o *improve.SignalsOut, details bool) {
	fmt.Printf("Looked at %s from %s.\n", logEntries(o.Scanned), o.Since.Local().Format("Mon 2 Jan"))
	if len(o.Signals) == 0 {
		fmt.Println("Nothing stands out.")
	}
	for _, s := range o.Signals {
		fmt.Printf("\n%s · %s (%s)\n", signalWords[s.Kind], s.Title, times(s.Count))
		for _, ex := range s.Examples {
			fmt.Printf("  %s  %s\n", ex.At.Local().Format("Jan 2 15:04"), ex.Excerpt)
			if details {
				fmt.Printf("           %s  (metarush log show %s)\n", ex.Link, ex.EntryID)
			}
		}
	}
	for _, n := range o.Notes {
		fmt.Println("\n" + n)
	}
}

func times(n int) string {
	if n == 1 {
		return "once"
	}
	return fmt.Sprintf("%d times", n)
}

func improveSaveCmd() *cobra.Command {
	var file string
	c := &cobra.Command{Use: "save --file report.md", Short: "Save an improvement report as improvement ideas (the metarush-improve skill writes it)",
		Long: "The report is Markdown with one \"## \" section per idea:\n\n" + improve.ReportFormat,
		RunE: func(c *cobra.Command, _ []string) error {
			if file == "" {
				return fail(c, apperr.InvalidInput, "say which report to save (--file report.md, or - for standard input)", "")
			}
			var b []byte
			var err error
			if file == "-" {
				b, err = io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
			} else {
				b, err = os.ReadFile(file)
			}
			if err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			return runOp(c, "improve.save", improve.SaveIn{Markdown: string(b)}, func(o any) {
				r := o.(*improve.SaveOut)
				fmt.Printf("Saved “%s”: %s new, %s found again.\n", r.Report.Title, plural(r.Created, "idea"), plural(r.Updated, "idea"))
				for _, w := range r.Warnings {
					fmt.Println("  note: " + w)
				}
				fmt.Println("Keep or dismiss them on the Improvements page.")
				if detailsMode(c) {
					fmt.Printf("report %s\nkeep or dismiss: metarush improve keep|dismiss <id>…\n", r.Report.ID)
				}
			})
		}}
	c.Flags().StringVar(&file, "file", "", "the report (Markdown), or - for standard input")
	return c
}

var ideaKinds = map[string]string{improve.KindFeatureGap: "Missing feature", improve.KindFriction: "Friction", improve.KindBug: "Problem"}

func improveListCmd() *cobra.Command {
	var in improve.ListIn
	var details bool
	c := &cobra.Command{Use: "list", Short: "List improvement ideas, best first (new and kept by default)",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "improve.list", in, func(o any) {
				out := o.(*improve.ListOut)
				if len(out.Items) == 0 {
					fmt.Println("No improvement ideas yet. Ask Claude Code to look for ways MetaRush could do better.")
				}
				for _, it := range out.Items {
					printItem(it, details)
				}
				fmt.Printf("\n%d new, %d kept, %d dismissed.\n", out.Counts.New, out.Counts.Kept, out.Counts.Dismissed)
			})
		}}
	c.Flags().StringVar(&in.State, "state", "", "new, kept, dismissed or all (default: new and kept)")
	addDetails(c, &details)
	return c
}

func printItem(it improve.Item, details bool) {
	state := map[string]string{improve.StateNew: "new", improve.StateKept: "kept", improve.StateDismissed: "dismissed"}[it.State]
	fmt.Printf("\n%s (%s, %s impact, seen %s, %s)\n", it.Title, ideaKinds[it.Kind], it.Impact, times(it.Frequency), state)
	if it.Description != "" {
		fmt.Println(indent(trunc(it.Description, 600), "  "))
	}
	if details {
		fmt.Printf("  %s  key %s  score %.0f  evidence %s\n", it.ID, it.Key, it.Score, strings.Join(it.Evidence, ", "))
	}
}

func improveShowCmd() *cobra.Command {
	var details bool
	c := &cobra.Command{Use: "show <id>", Short: "Show an improvement idea with its evidence, or a saved report", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "improve.show", improve.ShowIn{ID: args[0]}, func(o any) {
				out := o.(*improve.ShowOut)
				if out.Report != nil {
					fmt.Println(out.Report.Markdown)
					return
				}
				printItem(*out.Item, details)
				if len(out.Evidence) > 0 {
					fmt.Println("\nWhat shows it:")
					for _, r := range out.Evidence {
						printLogRow(r, details, true)
					}
				}
			})
		}}
	addDetails(c, &details)
	return c
}

func improveDecideCmd(use, op, short string) *cobra.Command {
	return &cobra.Command{Use: use + " <id>...", Short: short, Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, op, improve.DecideIn{IDs: args}, func(o any) {
				out := o.(*improve.DecideOut)
				verb := map[string]string{"improve.keep": "Kept", "improve.dismiss": "Dismissed"}[op]
				for _, it := range out.Items {
					fmt.Printf("%s: %s\n", verb, it.Title)
				}
			})
		}}
}

func improveExportCmd() *cobra.Command {
	var in improve.ExportIn
	var out string
	c := &cobra.Command{Use: "export", Short: "Export improvement ideas as Markdown (kept and new by default) to standard output or --out",
		RunE: func(c *cobra.Command, _ []string) error {
			if out != "" {
				if err := logbook.CheckOutput(out); err != nil {
					return failErr(c, err)
				}
			}
			return runOp(c, "improve.export", in, func(o any) {
				r := o.(*improve.ExportOut)
				if out == "" {
					fmt.Print(r.Markdown)
					return
				}
				if err := os.WriteFile(out, []byte(r.Markdown), 0o600); err != nil {
					fmt.Fprintln(os.Stderr, "error:", err)
					return
				}
				fmt.Printf("Saved %s to %s.\n", plural(r.Count, "idea"), out)
			})
		}}
	c.Flags().StringVar(&in.State, "state", "", "new, kept, dismissed or all (default: kept and new)")
	c.Flags().StringVar(&out, "out", "", "write to this file instead of standard output")
	return c
}

// improveAddCmd puts one idea on the list (D166): something MetaRush couldn't do.
func improveAddCmd() *cobra.Command {
	var in improve.AddIn
	c := &cobra.Command{Use: `add "<what MetaRush couldn't do>" [--description …]`, Short: "Put one idea on MetaRush's improvement list (the same title again is the same idea)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Title = args[0]
			return runOp(c, "improve.add", in, func(o any) { fmt.Println(o.(*improve.AddOut).Summary) })
		}}
	c.Flags().StringVar(&in.Description, "description", "", "what it was needed for")
	c.Flags().StringVar(&in.Kind, "kind", "", "feature-gap (default), friction or bug")
	c.Flags().StringVar(&in.Impact, "impact", "", "high, medium (default) or low")
	return c
}
