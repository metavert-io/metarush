package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/activity"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/visual"
)

func pct(p *float64) string {
	if p == nil {
		return "   "
	}
	return fmt.Sprintf("%3.0f%%", *p*100)
}

func fmtETA(s *float64) string {
	if s == nil || *s <= 0 {
		return ""
	}
	d := time.Duration(*s * float64(time.Second)).Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds left", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm %02ds left", int(d.Minutes()), int(d.Seconds())%60)
}

// addDetails registers --details, the CLI's "Details" disclosure: the plain
// output leaves out IDs, links and follow-up commands (voice rule); --details
// adds them and --json has everything.
func addDetails(cmd *cobra.Command, v *bool) {
	cmd.Flags().BoolVar(v, "details", false, "also show IDs, links and the commands that act on each item")
}

func stepChips(steps []activity.Step) string {
	var parts []string
	for _, s := range steps {
		mark := map[string]string{"done": "✓", "failed": "✗", "running": "…", "queued": "·", "paused": "‖", "canceled": "-"}[s.State]
		label := strings.ToLower(s.Label) + " " + mark
		if s.State == "paused" && s.Waiting != nil && s.Waiting.BatteryPercent != nil {
			label = fmt.Sprintf("%s paused (battery %d%%)", strings.ToLower(s.Label), *s.Waiting.BatteryPercent)
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, "  ")
}

func printActivity(s *activity.Summary, details bool) {
	fmt.Println(s.Headline)
	if s.Progress != nil {
		fmt.Printf("Overall %s: %d in progress, %d done, %d failed\n", strings.TrimSpace(pct(s.Progress)), s.Counts.Active, s.Counts.Done, s.Counts.Failed)
	}
	actionable := false
	groups := []struct{ title, cat string }{{"Uploads", activity.CatUpload}, {"Preparing footage", activity.CatProcessing}, {"Renders", activity.CatRender}, {"Quality checks", activity.CatQA}, {"Narration", activity.CatNarration}, {"Generated video", activity.CatGenerate}, {"Claude Code", activity.CatAssistant}}
	for _, g := range groups {
		var rows []activity.Item
		for _, it := range s.Items {
			if it.Category == g.cat {
				rows = append(rows, it)
			}
		}
		if len(rows) == 0 {
			continue
		}
		fmt.Printf("\n%s\n", g.title)
		for _, it := range rows {
			extra := []string{}
			if it.Rate > 0 {
				extra = append(extra, fmtBytes(int64(it.Rate))+"/s")
			}
			if e := fmtETA(it.ETASeconds); e != "" {
				extra = append(extra, e)
			}
			if it.Machine != "" {
				extra = append(extra, "on "+it.Machine)
			}
			fmt.Printf("  %-28s %-34s %s  %s\n", trunc(it.Title, 28), trunc(it.StageLabel, 34), pct(it.Pct), strings.Join(extra, "  "))
			if len(it.Steps) > 0 {
				fmt.Printf("      %s\n", stepChips(it.Steps))
			}
			if it.Waiting != nil && it.Waiting.Code != "starting" {
				fmt.Printf("      %s\n", capitalize(it.Waiting.Text))
			}
			if it.Error != "" || it.Problem != "" {
				var failed []string
				for _, s := range it.Steps {
					if s.State == "failed" {
						failed = append(failed, strings.ToLower(s.Label))
					}
				}
				switch {
				case it.Problem != "":
					fmt.Printf("      %s\n", it.Problem)
				case len(failed) > 0:
					fmt.Printf("      %s failed\n", capitalize(strings.Join(failed, ", ")))
				default:
					fmt.Println("      It did not finish.")
				}
				if it.Hint != "" {
					fmt.Printf("      %s\n", it.Hint)
				}
				if details && it.Error != "" {
					fmt.Printf("      error: %s\n", trunc(it.Error, 300))
				}
			}
			if it.CanRetry && len(it.RetryJobIDs) > 0 {
				actionable = true
				if details {
					fmt.Printf("      retry: metarush job retry %s\n", strings.Join(it.RetryJobIDs, " "))
				}
			}
			if q := it.Questions; q != nil {
				fmt.Printf("      %s, waiting on the To-dos page\n", capitalize(q.Text))
				if details {
					fmt.Printf("      questions: %s\n", q.Link)
				}
			}
			if details && it.Link != "" {
				fmt.Printf("      %s  %s\n", it.Key, it.Link)
			}
		}
	}
	if len(s.Waiting) > 0 {
		fmt.Println("\nWaiting")
		for _, w := range s.Waiting {
			fmt.Printf("  %s\n", capitalize(w.Text))
			if w.CanProcessNow {
				actionable = true
				if details {
					fmt.Printf("      process now: metarush job run-now %s\n", strings.Join(w.JobIDs, " "))
				}
			}
		}
	}
	if len(s.Workers) > 0 {
		fmt.Println("\nMachines")
		for _, w := range s.Workers {
			pw := "plugged in"
			if w.BatteryPercent != nil {
				pw = fmt.Sprintf("battery %d%%", *w.BatteryPercent)
				switch {
				case w.Charging:
					pw += ", charging"
				case w.OnBattery:
					pw += ", on battery"
				default:
					pw += ", plugged in"
				}
			}
			policy := fmt.Sprintf("holds big jobs below %d%% on battery", w.Threshold)
			if w.Threshold == 0 {
				policy = "runs big jobs on any battery level"
			}
			if w.DeferringHeavy {
				policy = "holding big jobs: " + w.DeferReason
			}
			this := ""
			if w.This {
				this = " (this machine)"
			}
			fmt.Printf("  %-18s %d of %d busy  %s  %s%s\n", trunc(w.Name, 18), w.Running, w.Concurrency, pw, policy, this)
		}
	}
	if actionable && !details {
		fmt.Println("\nRetry or start these from Activity in the dashboard.")
	}
}

func activityCmd() *cobra.Command {
	var in activity.SummaryIn
	var watch, details bool
	var every time.Duration
	cmd := &cobra.Command{
		Use:   "activity",
		Short: "What MetaRush is doing now: uploads, footage being prepared, renders, quality checks, Claude Code requests, and why anything is waiting",
		RunE: func(c *cobra.Command, _ []string) error {
			if !watch {
				return runOp(c, "activity.summary", in, func(o any) { printActivity(o.(*activity.Summary), details) })
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			raw, _ := json.Marshal(in)
			last := ""
			tty := interactive()
			for {
				out, err := ops.Call(ctx, a, "activity.summary", raw, ops.Caller{})
				if err != nil {
					if ctx.Err() != nil {
						return nil
					}
					return failErr(c, err)
				}
				s := out.(*activity.Summary)
				if jsonMode(c) {
					// One JSON document per line (NDJSON) on every change.
					b, _ := json.Marshal(map[string]any{"ok": true, "data": s})
					sig := string(b[strings.Index(string(b), `"headline"`):])
					if sig != last {
						fmt.Println(string(b))
						last = sig
					}
				} else {
					if tty {
						fmt.Print("\033[H\033[2J")
					}
					printActivity(s, details)
					fmt.Fprintf(os.Stderr, "\n(updated %s; Ctrl-C to stop)\n", time.Now().Format("15:04:05"))
				}
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(every):
				}
			}
		},
	}
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "only this project")
	cmd.Flags().IntVar(&in.RecentMinutes, "recent", 0, "keep finished items listed for this many minutes (default 10)")
	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "refresh until interrupted (with --json: one JSON document per change)")
	cmd.Flags().DurationVar(&every, "every", 2*time.Second, "refresh interval for --watch")
	addDetails(cmd, &details)
	return cmd
}

func notificationsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "notifications", Aliases: []string{"notification"}, Short: "Your notifications (footage ready, renders, quality checks, duplicates, battery pauses)"}
	var in ops.NotificationListIn
	var details bool
	list := &cobra.Command{
		Use: "list", Short: "List notifications (newest first)",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "notification.list", in, func(o any) {
				out := o.(*ops.NotificationListOut)
				switch {
				case len(out.Notifications) == 0:
					fmt.Println("No notifications.")
					return
				case out.Unread == 0:
					fmt.Println("All read.")
				default:
					fmt.Printf("%d unread\n", out.Unread)
				}
				for _, n := range out.Notifications {
					mark := " "
					if !n.Read {
						mark = "•"
					}
					fmt.Printf("%s %s  %s\n", mark, n.UpdatedAt.Local().Format("Jan 2 15:04"), n.Title)
					if n.Body != "" {
						fmt.Printf("      %s\n", trunc(n.Body, 140))
					}
					if q := n.Questions; q != nil {
						fmt.Printf("      %s, waiting on the To-dos page\n", capitalize(q.Text))
					}
					if details {
						fmt.Printf("      %s  %s\n", n.ID, n.Link)
						if n.Questions != nil {
							fmt.Printf("      questions: %s\n", n.Questions.Link)
						}
					}
				}
			})
		},
	}
	list.Flags().BoolVar(&in.Unread, "unread", false, "only unread")
	list.Flags().Int64Var(&in.Limit, "limit", 0, "maximum (default 50)")
	addDetails(list, &details)
	cmd.AddCommand(list)
	var all bool
	read := &cobra.Command{
		Use: "read <notification-id>... | --all", Short: "Mark notifications read",
		RunE: func(c *cobra.Command, args []string) error {
			if !all && len(args) == 0 {
				return fail(c, "E_INVALID_INPUT", "give notification IDs or --all", "")
			}
			return runOp(c, "notification.read", ops.NotificationReadIn{IDs: args, All: all}, func(o any) {
				out := o.(*ops.NotificationReadOut)
				fmt.Printf("Marked %d read, %d still unread\n", out.Marked, out.Unread)
			})
		},
	}
	read.Flags().BoolVar(&all, "all", false, "mark every notification read")
	cmd.AddCommand(read)
	return cmd
}

func printSnippet(parts []ops.SnippetPart, tty bool) string {
	var b strings.Builder
	for _, p := range parts {
		switch {
		case p.Hit && tty:
			b.WriteString("\033[1;33m" + p.Text + "\033[0m")
		case p.Hit:
			b.WriteString("[" + p.Text + "]")
		default:
			b.WriteString(p.Text)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func searchCmd() *cobra.Command {
	var in ops.SearchIn
	var details bool
	cmd := &cobra.Command{
		Use: "search <query>", Short: "Search what was said (transcripts with timecodes), what's on screen (things, places, on-screen text), video titles, projects, versions, comments and resources", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Query = strings.Join(args, " ")
			tty := interactive()
			return runOp(c, "search", in, func(o any) {
				out := o.(*ops.SearchOut)
				if out.Total == 0 {
					fmt.Printf("Nothing matches %q.\n", out.Query)
					return
				}
				if len(out.Moments) > 0 {
					fmt.Println("Footage moments")
					for _, m := range out.Moments {
						who := ""
						if m.SpeakerName != "" {
							who = " (" + m.SpeakerName + ")"
						}
						fmt.Printf("  %s  %-24s %s%s\n", m.Timecode, trunc(m.AssetTitle, 24), printSnippet(m.Snippet, tty), who)
						if details {
							fmt.Printf("      %s at %.2fs  [%s]  %s\n", m.AssetID, m.At, m.Rank, links.AssetAt(m.ProjectID, m.AssetID, fmt.Sprintf("%.2f", m.At), strings.Join(m.Terms, "|")))
						}
					}
				}
				if len(out.Visuals) > 0 {
					fmt.Println("On screen")
					for _, v := range out.Visuals {
						what := map[string]string{"label": "seen", "text": "on-screen text", "description": "scene"}[v.MatchedIn]
						fmt.Printf("  %s–%s  %-24s %s: %s\n", visual.Clock(v.From), visual.Clock(v.To), trunc(v.AssetTitle, 24), what, printSnippet(v.Snippet, tty))
						if details {
							fmt.Printf("      %s at %.2fs  [%s]  %s\n", v.AssetID, v.At, v.Rank, links.AssetOnScreenAt(v.ProjectID, v.AssetID, fmt.Sprintf("%.2f", v.At)))
						}
					}
				}
				if len(out.Videos) > 0 {
					fmt.Println("Videos")
					for _, v := range out.Videos {
						where := "Inbox"
						if v.ProjectName != "" {
							where = v.ProjectName
						}
						line := fmt.Sprintf("  %-30s in %s", trunc(v.Title, 30), where)
						if v.Name != "" && v.Name != v.Title {
							line += fmt.Sprintf(" (file %s)", v.Name)
						}
						if v.From != "" {
							line += ", from " + v.From
						}
						switch {
						case v.MatchedIn == "speaker":
							line += ", " + v.Speaker + " speaks in it"
						case v.MatchedIn != "" && v.MatchedIn != "title":
							line += ", matched its " + v.MatchedIn
						}
						fmt.Println(line)
						if details {
							fmt.Printf("      %s  %s\n", v.AssetID, links.Asset(v.ProjectID, v.AssetID))
						}
					}
				}
				if len(out.Resources) > 0 {
					fmt.Println("Resources")
					for _, r := range out.Resources {
						where := "the workspace library"
						switch {
						case r.From != "":
							where = "from " + r.From
						case r.ProjectName != "":
							where = "in " + r.ProjectName
						}
						line := fmt.Sprintf("  %-24s %s, %s", trunc(r.Title, 24), kindName(r.Kind), where)
						if r.MatchedIn != "name" && r.MatchedIn != "title" {
							line += fmt.Sprintf(" (%s: %s)", r.MatchedIn, printSnippet(r.Snippet, tty))
						}
						fmt.Println(line)
						if details {
							fmt.Printf("      %s (%s)\n", r.ResourceID, r.Name)
						}
					}
				}
				if len(out.Projects) > 0 {
					fmt.Println("Projects")
					for _, p := range out.Projects {
						fmt.Printf("  %s  (%s: %s)\n", p.Name, p.MatchedIn, printSnippet(p.Snippet, tty))
						if details {
							fmt.Printf("      %s  %s\n", p.ProjectID, links.Project(p.ProjectID))
						}
					}
				}
				if len(out.Revisions) > 0 {
					fmt.Println("Versions")
					for _, r := range out.Revisions {
						fmt.Printf("  %s, version %d  (%s: %s)\n", r.ProjectName, r.Number, orWord(map[string]string{"instruction": "request", "rationale": "reasoning"}[r.MatchedIn], r.MatchedIn), printSnippet(r.Snippet, tty))
						if details {
							fmt.Printf("      %s  %s\n", r.RevisionID, links.Revision(r.ProjectID, r.RevisionID))
						}
					}
				}
				if len(out.Comments) > 0 {
					fmt.Println("Comments")
					for _, cm := range out.Comments {
						fmt.Printf("  %s: %s\n", cm.ProjectName, printSnippet(cm.Snippet, tty))
						if details {
							fmt.Printf("      %s  %s\n", cm.CommentID, links.Project(cm.ProjectID))
						}
					}
				}
			})
		},
	}
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "only this project")
	cmd.Flags().IntVar(&in.Limit, "limit", 0, "results per group (default 10)")
	addDetails(cmd, &details)
	return cmd
}

var watchStateWords = map[string]string{
	ingest.WatchWatching: "watching", ingest.WatchOff: "off", ingest.WatchMissing: "folder not found",
	ingest.WatchNotRunning: "MetaRush is not running, so nothing is being added",
}

// folderPlace names a folder the way people find it: "“MetaRush Inbox” in
// your Movies folder" for ~/Movies/MetaRush Inbox (the path itself is for
// --details).
func folderPlace(display string) string {
	p := strings.TrimRight(display, "/")
	switch {
	case p == "~" || p == "":
		return "your home folder"
	case strings.HasPrefix(p, "~/"):
		rel := strings.TrimPrefix(p, "~/")
		parent, name := "", rel
		if i := strings.LastIndex(rel, "/"); i >= 0 {
			parent, name = rel[:i], rel[i+1:]
		}
		switch parent {
		case "":
			return "“" + name + "” in your home folder"
		case "Desktop":
			return "“" + name + "” on your Desktop"
		case "Movies", "Documents", "Downloads", "Pictures", "Music", "Videos":
			return "“" + name + "” in your " + parent + " folder"
		}
		return "“" + name + "” (in " + parent + ", in your home folder)"
	case strings.HasPrefix(p, "/Volumes/"):
		rest := strings.TrimPrefix(p, "/Volumes/")
		drive, sub, _ := strings.Cut(rest, "/")
		if sub == "" {
			return "the drive “" + drive + "”"
		}
		return "“" + baseName(sub) + "” on the drive “" + drive + "”"
	}
	return "“" + baseName(p) + "”"
}

func printWatch(s *ingest.WatchStatusOut, details bool) {
	if s.State == ingest.WatchNotConfigured {
		fmt.Println("Watched folder: not set up yet.")
		fmt.Printf("  Suggested: %s. Choose one in Settings > Watched folder.\n", folderPlace(s.SuggestedDisplay))
		if details {
			fmt.Printf("  command: metarush watch set %q --create\n", s.SuggestedDisplay)
		}
		return
	}
	state := watchStateWords[s.State]
	if state == "" {
		state = strings.ReplaceAll(s.State, "_", " ")
	}
	fmt.Printf("Watched folder: %s (%s)\n", folderPlace(s.DisplayDir), state)
	target := "the Inbox"
	if s.ProjectName != "" {
		target = s.ProjectName
	}
	move := "Files stay where they are once added."
	if s.MoveProcessed {
		move = "Added files move into its Ingested folder."
	}
	fmt.Printf("  New videos go to %s. %s A file is added once it stops changing for %d seconds.\n", target, move, s.StableSeconds)
	if l := s.Live; l != nil {
		if !l.LastScan.IsZero() {
			fmt.Printf("  Last checked %s.", l.LastScan.Local().Format("15:04:05"))
			if len(l.Pending) > 0 {
				fmt.Printf(" Waiting for %s to finish copying: %s.", plural(len(l.Pending), "file"), strings.Join(l.Pending, ", "))
			}
			fmt.Println()
		}
		if l.Ingesting != "" {
			fmt.Printf("  Adding %s now.\n", l.Ingesting)
		}
		if l.LastIngest != "" {
			fmt.Printf("  Last added: %s\n", l.LastIngest)
		}
		for _, e := range l.Errors {
			fmt.Printf("  Problem: %s\n", e)
		}
	}
	if s.Note != "" {
		fmt.Printf("  %s\n", s.Note)
	}
	if details {
		fmt.Printf("  path: %s\n", s.Dir)
	}
}

// watchPrinter prints the watched folder after a change, with --details when asked.
func watchPrinter(c *cobra.Command) func(o any) {
	return func(o any) { printWatch(o.(*ingest.WatchStatusOut), detailsMode(c)) }
}

func watchCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "watch", Short: "The watched folder: videos dropped into it are added once they stop changing"}
	var details bool
	status := &cobra.Command{
		Use: "status", Short: "Show the watched folder's setup and what it is doing",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "watch.status", ingest.WatchStatusIn{}, func(o any) { printWatch(o.(*ingest.WatchStatusOut), details) })
		},
	}
	addDetails(status, &details)
	cmd.AddCommand(status)
	var project string
	var move, noMove bool
	var stable int
	var create, onlyNew bool
	set := &cobra.Command{
		Use: "set <dir>", Short: "Watch a folder: files dropped into it are added automatically (applies without restarting MetaRush)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in := ingest.WatchSetIn{Dir: args[0], Create: create}
			if onlyNew {
				in.Existing = "skip"
			}
			if c.Flags().Changed("project") {
				in.ProjectID = &project
			}
			if move || noMove {
				v := move && !noMove
				in.MoveProcessed = &v
			}
			if c.Flags().Changed("stable-seconds") {
				in.StableSeconds = &stable
			}
			return runOp(c, "watch.set", in, watchPrinter(c))
		},
	}
	set.Flags().StringVar(&project, "project", "", "project for added files (empty: the Inbox)")
	set.Flags().BoolVar(&move, "move-processed", false, "move added files into an Ingested subfolder")
	set.Flags().BoolVar(&noMove, "no-move-processed", false, "leave added files where they are")
	set.Flags().IntVar(&stable, "stable-seconds", 0, "seconds a file must stop changing before it is added (2-600)")
	set.Flags().BoolVar(&create, "create", false, "create the folder if it does not exist")
	set.Flags().BoolVar(&onlyNew, "only-new", false, "leave the files already in the folder alone; add only new ones")
	cmd.AddCommand(set)
	cmd.AddCommand(&cobra.Command{
		Use: "off", Aliases: []string{"disable"}, Short: "Stop watching (keeps the folder setting)",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "watch.disable", ingest.WatchDisableIn{}, watchPrinter(c))
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "on", Aliases: []string{"enable"}, Short: "Start watching the folder that is already set up",
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			st, err := ops.CallTyped[ingest.WatchStatusOut](c.Context(), a, "watch.status", ingest.WatchStatusIn{}, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			if st.Dir == "" {
				return failErr(c, apperr.New(apperr.InvalidInput, "No watched folder is set up yet.").
					WithHint("Choose one in Settings > Watched folder.").WithCommand("metarush watch set <folder> --create"))
			}
			on := true
			return runOpWith(c, a, "watch.set", ingest.WatchSetIn{Dir: st.Dir, Enabled: &on, Existing: "import"}, ops.Caller{}, watchPrinter(c))
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "import", Short: "Also add the files that were already in the folder (when it was set to take only new files)",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "watch.import", ingest.WatchImportIn{}, watchPrinter(c))
		},
	})
	var chooseSet bool
	choose := &cobra.Command{
		Use: "choose", Short: "Pick the folder with the macOS folder picker (on this machine's screen)",
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			out, err := ops.CallTyped[ingest.WatchChooseOut](ctx, a, "watch.choose", ingest.WatchChooseIn{}, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			if !chooseSet || out.Canceled {
				return emit(c, out, func() {
					switch {
					case out.Canceled:
						fmt.Println("No folder chosen.")
					case out.Blocked != "":
						fmt.Printf("%s can't be watched: %s\n", capitalize(folderPlace(out.Display)), out.Blocked)
					default:
						fmt.Printf("%s (%s already there).\n", capitalize(folderPlace(out.Display)), plural(out.Files, "file"))
					}
					if detailsMode(c) && out.Path != "" {
						fmt.Println("path:", out.Path)
					}
				})
			}
			return runOpWith(c, a, "watch.set", ingest.WatchSetIn{Dir: out.Path}, ops.Caller{}, watchPrinter(c))
		},
	}
	choose.Flags().BoolVar(&chooseSet, "set", false, "watch the chosen folder right away")
	cmd.AddCommand(choose)
	cmd.AddCommand(&cobra.Command{
		Use: "reveal", Short: "Open the watched folder in Finder",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "watch.reveal", ingest.WatchRevealIn{}, func(o any) {
				fmt.Println("Opened the watched folder.")
				if detailsMode(c) {
					fmt.Println("path:", o.(*ingest.WatchRevealOut).Opened)
				}
			})
		},
	})
	var hidden bool
	browse := &cobra.Command{
		Use: "browse [dir]", Short: "List folders under your home folder (what the dashboard's folder picker shows)", Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in := ingest.BrowseIn{Hidden: hidden}
			if len(args) == 1 {
				in.Path = args[0]
			}
			return runOp(c, "watch.browse", in, func(o any) {
				out := o.(*ingest.BrowseOut)
				fmt.Println(out.Display)
				for _, e := range out.Entries {
					note := ""
					if e.Blocked != "" {
						note = "  (cannot be watched)"
					}
					fmt.Printf("  %s/%s\n", e.Name, note)
				}
			})
		},
	}
	browse.Flags().BoolVar(&hidden, "hidden", false, "include hidden folders")
	cmd.AddCommand(browse)
	cmd.AddCommand(&cobra.Command{
		Use: "mkdir <parent> <name>", Short: "Create a folder under your home folder", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "watch.mkdir", ingest.MkdirIn{Parent: args[0], Name: args[1]}, func(o any) {
				e := o.(*ingest.BrowseEntry)
				fmt.Printf("Created %s.\n", folderPlace(e.Display))
				if detailsMode(c) {
					fmt.Println("path:", e.Display)
				}
			})
		},
	})
	return cmd
}

func previewCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "preview", Short: "Dashboard preview support"}
	cmd.AddCommand(&cobra.Command{
		Use: "prepare <revision-id>", Short: "Copy what a version's preview plays (preview copies, sound) to this machine, so the dashboard plays it without waiting", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "preview.prepare", ops.PreviewPrepareIn{RevisionID: args[0], Wait: true}, func(o any) {
				out := o.(*ops.PreviewPrepareOut)
				for _, s := range out.Sources {
					state := "on this machine"
					if !s.Local {
						state = "not on this machine yet"
					}
					if s.Error != "" {
						state = s.Error
					}
					what := map[string]string{"proxy": "preview copy", "original": "original"}[s.Role]
					fmt.Printf("  %-28s %-13s %9s  %s\n", trunc(s.Title, 28), orWord(what, s.Role), fmtBytes(s.Size), state)
				}
				fmt.Printf("%d of %d files are on this machine.\n", out.Ready, out.Total)
			})
		},
	})
	return cmd
}

// jobKindName names a kind of job to start a sentence: "The job “Transcription”".
// The kind goes in quotes because some kinds aren't nouns ("What's on screen",
// "Moving media"): "The what's on screen will run again" doesn't read.
func jobKindName(typ string) string {
	if l := ops.JobKindLabel(typ); l != typ {
		return "The job “" + l + "”"
	}
	return "The job"
}

// startedByWords keeps who started a job readable mid-sentence: "You, in the
// dashboard" becomes "you, in the dashboard"; names stay as they are
// ("Claude Code or the command line").
func startedByWords(s string) string {
	first, _, _ := strings.Cut(s, " ")
	switch strings.TrimRight(first, ",") {
	case "You", "The", "A", "An", "Someone":
		return strings.ToLower(s[:1]) + s[1:]
	}
	return s
}

// printJob describes a job in plain words (`metarush job show`); details adds
// the ID, the dashboard link and the commands that act on it.
func printJob(j *ops.JobShowOut, details bool) {
	state := map[string]string{"queued": "waiting to start", "running": "running", "succeeded": "finished", "failed": "failed", "canceled": "canceled"}[j.State]
	fmt.Printf("%s\n%s, started by %s", j.Title, capitalize(state), startedByWords(j.StartedBy))
	if j.Machine != "" {
		fmt.Printf(", on %s", j.Machine)
	}
	if j.DurationSeconds != nil {
		fmt.Printf(", took %s", (time.Duration(*j.DurationSeconds) * time.Second).Round(time.Second))
	}
	fmt.Println()
	if j.State == "running" {
		fmt.Printf("Progress: %.0f%%\n", j.Progress.Pct*100)
		if details && j.Progress.Message != "" {
			fmt.Printf("progress: %s\n", j.Progress.Message)
		}
	}
	if j.Waiting != nil && j.Waiting.Code != "starting" {
		fmt.Printf("Waiting: %s\n", j.Waiting.Text)
		if j.CanProcessNow {
			if details {
				fmt.Println("To run it now anyway: metarush job run-now " + j.ID)
			} else {
				fmt.Println("You can start it now anyway with Process now in the dashboard.")
			}
		}
	}
	if j.Produced != nil {
		fmt.Println(j.Produced.Summary)
	}
	if q := j.Questions; q != nil {
		fmt.Printf("%s, waiting on the To-dos page.\n", capitalize(q.Text))
	}
	if q := j.Related.QAReport; q != nil {
		for _, f := range q.Findings {
			fmt.Printf("  %s %s\n", orWord(qaMarks[f.Severity], "-"), capitalize(f.Message))
			if details {
				fmt.Printf("    (%s at %s)\n", f.Severity, f.Timecode)
			}
		}
	}
	if j.Advice != "" {
		fmt.Println(j.Advice)
	} else if j.Error != "" && (j.State == "failed" || j.State == "canceled") {
		fmt.Println("It did not finish.")
	}
	if j.CanRetry && j.State == "failed" && !details {
		fmt.Println("You can retry it from the job's page in the dashboard.")
	}
	if details {
		fmt.Printf("job %s (%s)\n", j.ID, j.Type)
		fmt.Printf("dashboard: %s\n", links.Job(j.ID))
		if j.Target != nil {
			fmt.Printf("%s: %s\n", j.Target.Label, j.Target.Link)
		}
		if j.Questions != nil {
			fmt.Printf("questions: %s (metarush todos list --asset %s)\n", j.Questions.Link, j.AssetID)
		}
		if j.CanRetry {
			fmt.Println("retry: metarush job retry " + j.ID)
		}
		if j.CanCancel {
			fmt.Println("cancel: metarush job cancel " + j.ID)
		}
		if j.Error != "" {
			fmt.Printf("error: %s\n", j.Error)
		}
		fmt.Printf("log: metarush log list --job %s (dashboard: %s)\n", j.ID, j.LogLink)
	} else if j.Target != nil {
		fmt.Printf("%s in the dashboard.\n", j.Target.Label)
	}
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %ss", n, one)
}

func orWord(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// describeDuplicate is the digest line for a file that matched a stored asset.
func describeDuplicate(d *models.DuplicateOf) string {
	if d == nil {
		return ""
	}
	switch d.Action {
	case models.DuplicateKeptBoth:
		return fmt.Sprintf("identical to “%s” (in %s); kept both names, stored once", d.Title, d.Where)
	case models.DuplicateSameFile:
		return fmt.Sprintf("already stored as “%s” (in %s)", d.Title, d.Where)
	}
	return fmt.Sprintf("identical to “%s” (in %s); skipped", d.Title, d.Where)
}

var _ = context.Background
