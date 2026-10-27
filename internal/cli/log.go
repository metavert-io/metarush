package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/logbook/logops"
	"github.com/metavert-io/metarush/internal/ops"
)

func logCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "log", Short: "The activity log: everything MetaRush did, including what Claude was asked and answered",
		Long: "MetaRush keeps a log of what it does: everything asked of it (from the command line, the dashboard, Claude Code\n" +
			"and the machines doing background work), every job and program it ran, and every Claude conversation. Use it to\n" +
			"see what happened, to report a bug (`metarush log bundle`), and to find what MetaRush should do better\n" +
			"(`metarush improve`).\n\n" +
			"Passwords and keys are removed before anything is written. Where the words of Claude conversations are kept is\n" +
			"set with `metarush log configure --prompts workspace|local|off`."}
	cmd.AddCommand(logListCmd(), logSearchCmd(), logTailCmd(), logShowCmd(), logExportCmd(), logPurgeCmd(), logBundleCmd(), logSettingsCmd(),
		logConfigureCmd(), logHooksCmd(), logHookCmd())
	return cmd
}

// logFilters are the filter flags list, search, tail, export and bundle share.
type logFilters struct {
	in      logops.LogListIn
	details bool
}

func (f *logFilters) add(c *cobra.Command, defLimit int) {
	fl := c.Flags()
	fl.StringVar(&f.in.Since, "since", "", "how far back: a duration (2h, 30d) or a date (2026-10-01); default 7 days")
	fl.StringVar(&f.in.Until, "until", "", "up to this time (same forms as --since)")
	fl.StringSliceVar(&f.in.Kind, "kind", nil, "op, op_summary, job, tool, provider, claude, action or event (repeatable)")
	fl.StringSliceVar(&f.in.Level, "level", nil, "info, warn or error (repeatable)")
	fl.BoolVar(&f.in.Problems, "problems", false, "only errors and warnings")
	fl.StringSliceVar(&f.in.Channel, "channel", nil, "cli, dashboard, assistant, worker, watch, skill-eval, claude-terminal, mcp or recipe (Recipes' runs; repeatable)")
	fl.StringVar(&f.in.Op, "op", "", "one operation (e.g. render.submit)")
	fl.StringVar(&f.in.Project, "project", "", "project ID")
	fl.StringVar(&f.in.Asset, "asset", "", "clip (asset) ID")
	fl.StringVar(&f.in.Job, "job", "", "job ID")
	fl.StringVar(&f.in.Render, "render", "", "render ID")
	fl.StringVar(&f.in.Conversation, "conversation", "", "Claude conversation, skill-test case or Claude Code session ID")
	fl.StringVar(&f.in.Machine, "machine", "", "installation ID")
	fl.StringVar(&f.in.Before, "before", "", "continue after an earlier page (the next value of --json, or what --details prints)")
	fl.BoolVar(&f.in.Programs, "programs", false, "also show every successful program run (ffmpeg, ffprobe…) and online-service request (ElevenLabs); failed ones are always shown")
	fl.IntVar(&f.in.Limit, "limit", defLimit, "most entries to show")
	addDetails(c, &f.details)
}

func logListCmd() *cobra.Command {
	var f logFilters
	c := &cobra.Command{Use: "list", Short: "Show what MetaRush did, newest first (filters: --since, --kind, --problems, --project, --job, …)",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "log.list", f.in, func(o any) { printLogPage(o.(*logops.LogListOut), f.details) })
		}}
	f.add(c, 50)
	return c
}

func logSearchCmd() *cobra.Command {
	var f logFilters
	c := &cobra.Command{Use: "search <words>...", Short: "Search the log: what happened, errors, and what Claude was asked and answered", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			f.in.Query = strings.Join(args, " ")
			return runOp(c, "log.search", f.in, func(o any) { printLogPage(o.(*logops.LogListOut), f.details) })
		}}
	f.add(c, 50)
	return c
}

func logTailCmd() *cobra.Command {
	var f logFilters
	var follow bool
	c := &cobra.Command{Use: "tail", Short: "Show the latest entries, oldest first; --follow keeps printing new ones",
		RunE: func(c *cobra.Command, _ []string) error {
			if follow && assistantMode() {
				return failErr(c, apperr.New(apperr.Unauthorized, "The dashboard assistant can't keep following the log: that never returns.").
					WithHint("Show the latest entries once instead.").WithDetails(map[string]any{"refused": "--follow"}))
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			in := f.in
			if in.Since == "" {
				in.Since = "30d"
			}
			out, err := ops.CallTyped[logops.LogListOut](ctx, a, "log.list", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			rows := out.Entries
			reverse(rows)
			enc := json.NewEncoder(os.Stdout)
			print := func(r logops.LogRow) {
				if jsonMode(c) {
					_ = enc.Encode(r) // one JSON object per line (follow-friendly)
					return
				}
				printLogRow(r, f.details, true)
			}
			for _, r := range rows {
				print(r)
			}
			if !follow {
				return nil
			}
			seen := map[string]bool{}
			last := time.Now().UTC().Add(-time.Minute)
			for _, r := range rows {
				seen[r.ID] = true
				if r.At.After(last) {
					last = r.At
				}
			}
			t := time.NewTicker(2 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-t.C:
				}
				next := f.in
				next.Since = last.Add(-2 * time.Second).Format(time.RFC3339Nano) // entries written a moment late still show
				next.Limit = 500
				page, err := ops.CallTyped[logops.LogListOut](ctx, a, "log.list", next, ops.Caller{})
				if err != nil {
					continue
				}
				fresh := page.Entries
				reverse(fresh)
				for _, r := range fresh {
					if seen[r.ID] {
						continue
					}
					seen[r.ID] = true
					if r.At.After(last) {
						last = r.At
					}
					print(r)
				}
			}
		}}
	f.add(c, 20)
	c.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new entries until you stop it (Ctrl-C)")
	return c
}

// logEntries counts log entries ("1 log entry", "3 log entries").
func logEntries(n int) string {
	if n == 1 {
		return "1 log entry"
	}
	return fmt.Sprintf("%d log entries", n)
}

func reverse[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

var levelMark = map[string]string{logbook.LevelError: "✗", logbook.LevelWarn: "!", logbook.LevelInfo: "·"}

func printLogPage(out *logops.LogListOut, details bool) {
	if len(out.Entries) == 0 {
		fmt.Println("Nothing in the log for that time and those filters.")
		return
	}
	day := ""
	for _, r := range out.Entries {
		d := dayLabel(r.At)
		if d != day {
			if day != "" {
				fmt.Println()
			}
			fmt.Println(d)
			day = d
		}
		printLogRow(r, details, false)
	}
	if out.Next != "" {
		if details {
			fmt.Printf("\nolder entries: --before %s\n", out.Next)
		} else {
			fmt.Println("\nThere are older entries (--details shows how to see them).")
		}
	}
}

func dayLabel(t time.Time) string {
	lt := t.Local()
	now := time.Now()
	y1, m1, d1 := lt.Date()
	y2, m2, d2 := now.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return "Today"
	case lt.After(now.AddDate(0, 0, -2)) && now.AddDate(0, 0, -1).Day() == d1:
		return "Yesterday"
	case y1 == y2:
		return lt.Format("Mon 2 Jan")
	}
	return lt.Format("Mon 2 Jan 2006")
}

func printLogRow(r logops.LogRow, details, withDate bool) {
	ts := r.At.Local().Format("15:04:05")
	if withDate {
		ts = r.At.Local().Format("Jan 2 15:04:05")
	}
	line := r.Text
	if r.Duration != "" && r.Kind != logbook.KindOpSummary {
		line += " — " + r.Duration
	}
	fmt.Printf("  %s %s %s\n", ts, levelMark[r.Level], line)
	who := r.ChannelLabel
	if r.Machine != "" {
		who += " on " + r.Machine
	}
	extra := []string{who}
	if r.Error != nil && r.Error.Advice != "" {
		extra = append(extra, r.Error.Advice)
	}
	fmt.Printf("             %s\n", strings.Join(extra, " · "))
	if details {
		parts := []string{r.ID, r.Kind}
		if r.Op != nil {
			parts = append(parts, r.Op.Name)
		}
		if r.Tool != nil {
			parts = append(parts, r.Tool.Program)
		}
		if r.Job != nil {
			parts = append(parts, r.Job.Type+" "+r.Job.Event)
		}
		if r.Error != nil && r.Error.Code != "" {
			parts = append(parts, r.Error.Code)
		}
		if r.Target != nil {
			parts = append(parts, r.Target.Link)
		}
		fmt.Printf("             %s\n", strings.Join(parts, "  "))
	}
}

func logShowCmd() *cobra.Command {
	var details bool
	c := &cobra.Command{Use: "show <entry-id>", Short: "Show one log entry in full: a Claude turn's prompt and reply, a failed program's error output, what belongs to it",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "log.show", logops.LogShowIn{ID: args[0]}, func(o any) { printLogEntry(o.(*logops.LogShowOut), details) })
		}}
	addDetails(c, &details)
	return c
}

func printLogEntry(o *logops.LogShowOut, details bool) {
	r := o.Entry
	fmt.Println(r.Text)
	when := r.At.Local().Format("Mon 2 Jan 2006 15:04:05")
	who := r.ChannelLabel
	if r.Machine != "" {
		who += ", on " + r.Machine
	}
	if r.UserName != "" {
		who += ", " + r.UserName
	}
	fmt.Printf("%s · %s", when, who)
	if r.Duration != "" {
		fmt.Printf(" · took %s", r.Duration)
	}
	fmt.Println()
	if e := r.Error; e != nil {
		fmt.Printf("\nWhat went wrong: %s\n", e.Message)
		if e.Advice != "" {
			fmt.Println(e.Advice)
		} else if e.Hint != "" && details {
			fmt.Println("hint: " + e.Hint)
		}
	}
	if c := r.Claude; c != nil {
		if o.Prompt != "" {
			fmt.Printf("\nAsked:\n%s\n", indent(o.Prompt, "  "))
		}
		if o.Response != "" {
			fmt.Printf("\nClaude answered:\n%s\n", indent(o.Response, "  "))
		}
		if len(c.Tools) > 0 {
			fmt.Printf("\nWhat Claude did (%s):\n", plural(c.ToolCount, "step"))
			for _, t := range c.Tools {
				mark := "·"
				switch {
				case t.Denied:
					mark = "✗ not allowed:"
				case t.IsError:
					mark = "✗"
				}
				line := strings.TrimSpace(t.Summary)
				if t.Result != "" {
					line += " → " + t.Result
				}
				fmt.Printf("  %s %s %s\n", mark, t.Name, line)
			}
		}
		for _, a := range c.Actions {
			fmt.Printf("  Asked for a change: %s (%s)\n", a.Title, a.Decision)
		}
		if c.CostUSD > 0 && details {
			fmt.Printf("cost $%.2f, model %s\n", c.CostUSD, c.Model)
		}
	}
	if o.Stderr != "" {
		fmt.Printf("\nError output (the end):\n%s\n", indent(o.Stderr, "  "))
	}
	if o.Note != "" {
		fmt.Println("\n" + o.Note)
	}
	if o.Parent != nil {
		fmt.Println("\nPart of:")
		printLogRow(*o.Parent, details, true)
	}
	if len(o.Children) > 0 {
		fmt.Println("\nWhat it involved:")
		for _, k := range o.Children {
			printLogRow(k, details, false)
		}
	}
	if len(o.Related) > 0 {
		fmt.Printf("\nAlso in this %s:\n", map[bool]string{true: "conversation", false: "job"}[r.Refs.ConversationID != ""])
		for i, k := range o.Related {
			if i >= 12 && !details {
				fmt.Printf("  … and %d more\n", len(o.Related)-i)
				break
			}
			printLogRow(k, details, true)
		}
	}
	if details {
		b, _ := json.MarshalIndent(r.Entry, "", "  ")
		fmt.Printf("\n%s\n", b)
	} else if r.Target != nil {
		fmt.Printf("\n%s in the dashboard.\n", r.Target.Label)
	}
}

func logExportCmd() *cobra.Command {
	var f logFilters
	var out string
	var bodies bool
	c := &cobra.Command{Use: "export", Short: "Export log entries as JSON lines (one entry per line) to standard output or --out",
		RunE: func(c *cobra.Command, _ []string) error {
			var w io.Writer = os.Stdout
			if out != "" {
				if err := logbook.CheckOutput(out); err != nil {
					return failErr(c, err)
				}
				fh, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
				if err != nil {
					return fail(c, apperr.InvalidInput, err.Error(), "choose a new file name (an existing file is never replaced)")
				}
				defer fh.Close()
				w = fh
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			bw := bufio.NewWriter(w)
			defer bw.Flush()
			n, err := exportEntries(ctx, a, f.in, bodies, f.in.Limit, func(e logops.ExportEntry) error {
				b, err := json.Marshal(e)
				if err != nil {
					return err
				}
				_, err = bw.Write(append(b, '\n'))
				return err
			})
			if err != nil {
				return failErr(c, err)
			}
			if out != "" {
				_ = bw.Flush()
				if jsonMode(c) {
					return emit(c, map[string]any{"file": out, "entries": n}, func() {})
				}
				fmt.Fprintf(os.Stderr, "Saved %s to %s.\n", logEntries(n), out)
			}
			return nil
		}}
	f.add(c, 0)
	c.Flags().StringVar(&out, "out", "", "write to this new file instead of standard output")
	c.Flags().BoolVar(&bodies, "bodies", false, "include the full texts kept outside the entries (long prompts, replies, error output)")
	return c
}

// exportEntries pages through log.export (limit 0 = everything).
func exportEntries(ctx context.Context, a *app.App, in logops.LogListIn, bodies bool, limit int, each func(logops.ExportEntry) error) (int, error) {
	n := 0
	for {
		page := logops.LogExportIn{LogListIn: in, Bodies: bodies}
		page.Limit = logbook.MaxLimit
		if limit > 0 && limit-n < page.Limit {
			page.Limit = limit - n
		}
		out, err := ops.CallTyped[logops.LogExportOut](ctx, a, "log.export", page, ops.Caller{})
		if err != nil {
			return n, err
		}
		for _, e := range out.Entries {
			if err := each(e); err != nil {
				return n, err
			}
			n++
		}
		if out.Next == "" || (limit > 0 && n >= limit) {
			return n, nil
		}
		in.Before = out.Next
	}
}

func logPurgeCmd() *cobra.Command {
	var in logops.LogPurgeIn
	c := &cobra.Command{Use: "purge --before <date>", Short: "Delete log entries older than a date (people only: run it yourself in a terminal)",
		RunE: func(c *cobra.Command, _ []string) error {
			if in.Before == "" {
				return fail(c, apperr.InvalidInput, "say which date to delete before, e.g. --before 2026-09-01 or --before 90d", "")
			}
			if !interactive() {
				return fail(c, apperr.HumanOnly, "deleting log entries must be done by a person", "run this command yourself in a terminal, or use Delete old entries on the Log page")
			}
			before, err := logbook.ParseWhen(in.Before, time.Now().UTC())
			if err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			whose := "everyone's log entries in this workspace"
			if in.ThisMachine {
				whose = "this machine's log entries"
			}
			fmt.Printf("This deletes %s from before %s, and the texts only they used. It cannot be undone.\n", whose, before.Local().Format("Mon 2 Jan 2006 15:04"))
			if ask(`Type "delete" to go ahead`, "") != "delete" {
				return fail(c, apperr.Canceled, "nothing was deleted", "")
			}
			return runOpWith(c, a, "log.purge", in, ops.Caller{Human: true}, func(o any) {
				r := o.(*logops.LogPurgeOut)
				fmt.Printf("Deleted %s.\n", logEntries(int(r.Deleted)))
			})
		}}
	c.Flags().StringVar(&in.Before, "before", "", "delete entries older than this: a date (2026-09-01) or a duration back from now (90d)")
	c.Flags().BoolVar(&in.ThisMachine, "this-machine", false, "only this machine's entries")
	return c
}

func logSettingsCmd() *cobra.Command {
	var details bool
	c := &cobra.Command{Use: "settings", Short: "Show where Claude conversation text is kept, how long log entries stay, and whether terminal sessions are recorded",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "log.settings", ops.EmptyIn{}, func(o any) { printLogSettings(o.(*logops.LogSettingsOut), details) })
		}}
	addDetails(c, &details)
	return c
}

func logConfigureCmd() *cobra.Command {
	var prompts string
	var days int
	var details bool
	c := &cobra.Command{Use: "configure", Short: "Change where Claude conversation text is kept and how long log entries stay (people only)",
		Long: "Changing the log settings is for people (run it yourself in a terminal):\n" +
			"  --prompts workspace   keep what you ask Claude and its answers with the workspace (anyone who can open the workspace database can read them)\n" +
			"  --prompts local       keep them only on this machine\n" +
			"  --prompts off         do not keep them (the log still says a conversation happened)\n" +
			"  --retention-days N    remove this machine's entries after N days (0 keeps them until you delete them)",
		RunE: func(c *cobra.Command, _ []string) error {
			in := logops.LogConfigureIn{}
			if c.Flags().Changed("prompts") {
				in.Prompts = &prompts
			}
			if c.Flags().Changed("retention-days") {
				in.RetentionDays = &days
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "log.configure", in, ops.Caller{Human: interactive()}, func(o any) { printLogSettings(o.(*logops.LogSettingsOut), details) })
		}}
	c.Flags().StringVar(&prompts, "prompts", "", "workspace, local or off")
	c.Flags().IntVar(&days, "retention-days", 0, "remove this machine's entries after this many days (0 = keep until you delete them)")
	addDetails(c, &details)
	return c
}

func printLogSettings(s *logops.LogSettingsOut, details bool) {
	fmt.Println(s.Explanation)
	if details {
		fmt.Printf("\nlog.prompts: %s\nlog.retentionDays: %d\nspool: %d entries in %d files\nclaude hooks: enabled=%v events=%v settings=%s binary=%s home=%s\n",
			s.Prompts, s.RetentionDays, s.Spool.Entries, s.Spool.Files, s.Hooks.Enabled, s.Hooks.Events, s.Hooks.SettingsFile, s.Hooks.Binary, s.Hooks.Home)
	}
}

func logHooksCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "claude-hooks", Short: "Record Claude Code terminal sessions that use MetaRush (off unless you turn it on)"}
	var details bool
	status := &cobra.Command{Use: "status", Short: "Say whether Claude Code terminal sessions are recorded",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "log.hooks.status", logops.HooksIn{}, func(o any) { printHookStatus(o.(*logbook.HookStatus), details) })
		}}
	addDetails(status, &details)
	var yes bool
	enable := &cobra.Command{Use: "enable", Short: "Start recording Claude Code terminal sessions that use MetaRush (people only)",
		RunE: func(c *cobra.Command, _ []string) error {
			if !interactive() {
				return fail(c, apperr.HumanOnly, "Turning on the recording must be done by a person.", "Run this command yourself in a terminal.")
			}
			fmt.Println("MetaRush will add a few lines to Claude Code's settings on this machine. Claude Code then tells MetaRush about")
			fmt.Println("every session you run here, but only sessions that use MetaRush are recorded: what you asked, Claude's final")
			fmt.Println("answer and what it asked MetaRush to do. Other sessions are forgotten at the end of each answer. A copy of your")
			fmt.Println("current settings is kept next to them, and you can turn it off again at any time.")
			if detailsMode(c) {
				fmt.Println("turn it off: metarush log claude-hooks disable")
			}
			if !yes && !askYes("Turn it on?", false) {
				return fail(c, apperr.Canceled, "nothing was changed", "")
			}
			return runHooksOp(c, "log.hooks.enable", details)
		}}
	enable.Flags().BoolVar(&yes, "yes", false, "do not ask again (still needs an interactive terminal)")
	addDetails(enable, &details)
	disable := &cobra.Command{Use: "disable", Short: "Stop recording Claude Code terminal sessions (removes only MetaRush's hooks; people only)",
		RunE: func(c *cobra.Command, _ []string) error {
			if !interactive() {
				return fail(c, apperr.HumanOnly, "Turning off the recording must be done by a person.", "Run this command yourself in a terminal.")
			}
			return runHooksOp(c, "log.hooks.disable", details)
		}}
	addDetails(disable, &details)
	cmd.AddCommand(status, enable, disable)
	return cmd
}

func runHooksOp(c *cobra.Command, name string, details bool) error {
	a, err := openApp(c, "cli")
	if err != nil {
		return err
	}
	defer a.Close()
	return runOpWith(c, a, name, logops.HooksIn{}, ops.Caller{Human: true}, func(o any) {
		h := o.(*logops.HooksOut)
		switch {
		case h.Change == nil || !h.Change.Changed:
			fmt.Println("Nothing needed to change.")
		default:
			fmt.Println(h.Note)
		}
		if details && h.Change != nil {
			fmt.Printf("settings: %s\nbackup: %s\nadded: %v updated: %v removed: %v\n", h.Change.SettingsFile, orNone(h.Change.Backup), h.Change.Added, h.Change.Updated, h.Change.Removed)
		}
	})
}

func printHookStatus(s *logbook.HookStatus, details bool) {
	switch {
	case s.Enabled && s.OtherHome:
		fmt.Println("Claude Code terminal sessions that use MetaRush are recorded, but into another MetaRush installation on this machine.")
	case s.Enabled && !s.BinaryOK:
		fmt.Println("The recording is turned on, but the metarush program it runs is missing; turn it off and on again.")
	case s.Enabled:
		fmt.Println("Claude Code terminal sessions that use MetaRush are recorded in the log.")
	case s.Partial:
		fmt.Println("The recording is only partly set up; turn it on again to fix it.")
	default:
		fmt.Println("Claude Code terminal sessions are not recorded.")
	}
	if s.Waiting > 0 {
		fmt.Printf("%s waiting to be written to the log.\n", plural(s.Waiting, "finished answer"))
	}
	if details {
		if !s.Enabled {
			fmt.Println("turn it on: metarush log claude-hooks enable")
		}
		fmt.Printf("settings: %s\nevents: %v\nbinary: %s (ok %v)\nhome: %s\n", s.SettingsFile, s.Events, s.Binary, s.BinaryOK, s.Home)
		if s.Error != "" {
			fmt.Println("error: " + s.Error)
		}
	}
}

// logHookCmd is what Claude Code's hooks run. It is fast, prints nothing and
// always succeeds, so it can never get in the way of a Claude Code session.
func logHookCmd() *cobra.Command {
	return &cobra.Command{Use: "hook <event>", Short: "Receive a Claude Code hook event (run by Claude Code; see claude-hooks)", Hidden: true,
		Args: cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) != 1 {
				return nil
			}
			h, err := home()
			if err != nil {
				return nil
			}
			logbook.RunHook(h, args[0], os.Stdin)
			return nil
		}}
}
