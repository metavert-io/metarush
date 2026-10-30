package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/linkimport"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/worker"
)

const rememberedBrowser = "remembered"

func importCmd() *cobra.Command {
	var in linkimport.ImportIn
	var wait, updateTools, checkTools, details bool
	var autoUpdate, browser string
	cmd := &cobra.Command{
		Use:   "import <link>...",
		Short: "Import videos from links (YouTube, TikTok, X): downloaded on this machine, then added like dropped files, with the original post kept on the clip",
		Long: `Import videos from links. Each link is looked up, downloaded on this machine (best quality up to 4K, as MP4)
and added through the normal pipeline: duplicates are caught, and a preview, transcript and speakers follow. The
clip keeps where it came from (site, account, date, original title, stated license) for credits.

Playlists and channel pages import nothing unless you ask: --playlist 5 imports their first five videos.

If a site wants to check that you're not a bot, add --browser-signin chrome (or safari, firefox, brave, edge): the
downloader then uses that browser's sign-in for this import only. MetaRush never copies or keeps your cookies.

Without --wait the import is handed to MetaRush running on this machine (follow it with metarush activity);
--wait imports here and also waits for the preview and transcript.

A whole Remotion project (a folder or a GitHub repository) comes in with: metarush import remotion <folder | address>.
A timeline exported from another editing app becomes a cut with: metarush import timeline <file> --project <project>.`,
		Example: "  metarush import https://www.youtube.com/watch?v=aqz-KE-bpKQ --project prj_…\n  metarush import <link> --browser-signin chrome --wait\n  metarush import --update-tools",
		RunE: func(c *cobra.Command, args []string) error {
			switch {
			case updateTools:
				return runOp(c, "ingest.url.update", linkimport.ImportToolsIn{}, func(o any) {
					r := o.(*linkimport.ImportToolsUpdate)
					fmt.Println(r.Message)
					if details {
						fmt.Printf("    ran %s (yt-dlp from %s)\n", r.How, r.ManagedBy)
						for _, l := range strings.Split(r.Output, "\n") {
							if l = strings.TrimSpace(l); l != "" {
								fmt.Println("    " + l)
							}
						}
					}
				})
			case checkTools:
				return runOp(c, "ingest.url.tools", linkimport.ImportToolsIn{}, func(o any) { printImportTools(o.(*linkimport.ImportTools), details) })
			case autoUpdate != "":
				var on bool
				switch strings.ToLower(autoUpdate) {
				case "on", "true", "yes":
					on = true
				case "off", "false", "no":
				default:
					return fail(c, apperr.InvalidInput, "--auto-update takes on or off", "")
				}
				return runOp(c, "ingest.url.configure", linkimport.ImportConfigureIn{AutoUpdate: &on}, func(o any) {
					if on {
						fmt.Println("MetaRush will keep the downloader (yt-dlp) up to date on this machine, about once a week when no import is running. It never updates Python, Deno or other Homebrew packages by itself: when a newer yt-dlp needs them, Settings > Link imports says so and you update it there with Update now.")
					} else {
						fmt.Println("Automatic downloader updates are off on this machine. Update it yourself with Update now in Settings > Link imports.")
					}
				})
			}
			if len(args) == 0 {
				return fail(c, apperr.InvalidInput, "Give at least one link to import.", "Paste the address of the video, like https://www.youtube.com/watch?v=…")
			}
			in.URLs = args
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			if c.Flags().Changed("browser-signin") {
				in.BrowserSignIn = browser
				if browser == rememberedBrowser {
					in.BrowserSignIn = a.Cfg.Imports.Browser
					if in.BrowserSignIn == "" {
						return fail(c, apperr.InvalidInput, "say which browser's sign-in to use", "--browser-signin chrome (or safari, firefox, brave, edge)")
					}
				}
			}
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			b, _ := json.Marshal(in)
			res, err := ops.Call(ctx, a, "ingest.url", b, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			out := res.(*linkimport.ImportOut)
			if !wait || out.DryRun || len(out.JobIDs) == 0 {
				return emit(c, out, func() { printImport(out, details, true) })
			}
			if !jsonMode(c) {
				printImport(out, details, false)
			}
			done, err := runImportsHere(ctx, c, a, out.JobIDs)
			if err != nil {
				return failErr(c, err)
			}
			// Then the clips' own processing (preview, transcript, speakers), like `ingest --wait`.
			var assetIDs []string
			for _, j := range done {
				if j.State != models.JobSucceeded {
					continue
				}
				if id, _ := j.Result["assetId"].(string); id != "" {
					assetIDs = append(assetIDs, id)
				}
				// A post with several videos made one clip each.
				if rows, ok := j.Result["assets"].(bson.A); ok {
					for _, r := range rows {
						var row struct {
							AssetID string `bson:"assetId"`
						}
						if b, err := bson.Marshal(r); err == nil && bson.Unmarshal(b, &row) == nil && row.AssetID != "" && !slices.Contains(assetIDs, row.AssetID) {
							assetIDs = append(assetIDs, row.AssetID)
						}
					}
				}
			}
			var processing []models.Job
			if len(assetIDs) > 0 {
				var js []models.Job
				_ = a.DB.Find(ctx, store.CJobs, a.WorkspaceID(), bson.M{"assetId": bson.M{"$in": assetIDs}, "type": bson.M{"$in": notify.DerivativeTypes}}, &js)
				var ids []string
				for _, j := range js {
					ids = append(ids, j.ID)
				}
				if len(ids) > 0 {
					if !jsonMode(c) {
						fmt.Fprintf(os.Stderr, "waiting for the preview, transcript and scene analysis (%d jobs)…\n", len(ids))
					}
					processing, err = waitJobs(c, a, ids, 0, true)
					if err != nil {
						return failErr(c, err)
					}
				}
			}
			payload := map[string]any{"import": out, "imports": done, "jobs": processing}
			failed := 0
			for _, j := range done {
				if j.State != models.JobSucceeded {
					failed++
				}
			}
			err = emit(c, payload, func() { printImportResults(a, done, processing, details) })
			if err == nil && failed > 0 {
				return &exitError{code: 1}
			}
			return err
		},
	}
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "add the videos to this project (default: the inbox)")
	cmd.Flags().StringVar(&browser, "browser-signin", "", "download with this browser's sign-in, for this import only: chrome, safari, firefox, brave or edge (alone: the browser used last time)")
	cmd.Flags().Lookup("browser-signin").NoOptDefVal = rememberedBrowser
	cmd.Flags().IntVar(&in.Playlist, "playlist", 0, "for a playlist or channel link: import its first N videos (without it such links import nothing)")
	cmd.Flags().BoolVar(&in.DryRun, "dry-run", false, "look the links up (title, account, length, license) without importing")
	cmd.Flags().BoolVar(&wait, "wait", false, "import here and wait for the preview and transcript too")
	cmd.Flags().BoolVar(&updateTools, "update-tools", false, "update the downloader (yt-dlp) the way it was installed")
	cmd.Flags().BoolVar(&checkTools, "check-tools", false, "show the downloader on this machine: version, age, JavaScript runtime")
	cmd.Flags().StringVar(&autoUpdate, "auto-update", "", "on | off: let MetaRush update the downloader about once a week")
	addDetails(cmd, &details)
	// `metarush import remotion <folder | GitHub address>`: a whole Remotion project (D131).
	cmd.AddCommand(importRemotionCmd())
	// `metarush import timeline <file>`: a timeline from another editing app (D303, D304).
	cmd.AddCommand(importTimelineCmd())
	// `metarush import cloud <remote:folder>`: files from a Google Drive or Dropbox folder (D220, D306).
	cmd.AddCommand(importCloudCmd())
	return cmd
}

func printImport(out *linkimport.ImportOut, details, note bool) {
	for _, r := range out.Imports {
		line := r.Message
		if r.Next != "" {
			line += "\n    " + r.Next
		}
		fmt.Println(line)
		if details && r.Code == "playlist" {
			fmt.Println("    to import its first videos: add --playlist N (how many)")
		}
		if details {
			fmt.Printf("    %s  %s", r.Status, r.Input)
			if r.JobID != "" {
				fmt.Printf("  job %s", r.JobID)
			}
			if r.AssetID != "" {
				fmt.Printf("  clip %s", r.AssetID)
			}
			fmt.Println()
		}
	}
	if note && out.Note != "" {
		fmt.Println(out.Note)
	}
}

// runImportsHere runs the imports in this terminal (unless MetaRush on this
// machine already runs them), printing each stage, and returns the jobs.
func runImportsHere(ctx context.Context, c *cobra.Command, a *app.App, ids []string) ([]models.Job, error) {
	var out []models.Job
	for i := 0; i < len(ids); i++ {
		id := ids[i]
		var stop context.CancelFunc
		var running chan struct{} // closed when this terminal's worker returns
		idle := func() bool {
			if running == nil {
				return true
			}
			select {
			case <-running:
				return true
			default:
				return false
			}
		}
		last := ""
		for {
			cur, err := a.Queue.Get(ctx, id)
			if err != nil {
				if stop != nil {
					stop()
				}
				return nil, err
			}
			// Run it here unless MetaRush on this machine runs it: at the start,
			// and again once a retry's wait is over.
			// (nextRunAt is the database server's time: D360.)
			if cur.State == models.JobQueued && !a.DB.NowOrLocal(ctx).Before(cur.NextRunAt) && idle() && !linkimport.Running(ctx, a) {
				if stop != nil {
					stop()
				}
				w := worker.New(a)
				w.OnlyJobID, w.IgnoreBattery = id, true
				w.Log = func(string, ...any) {}
				var wctx context.Context
				wctx, stop = context.WithCancel(ctx)
				done := make(chan struct{})
				running = done
				go func() {
					defer close(done)
					_ = w.Run(wctx)
				}()
			}
			if line := importLine(cur); line != last && !jsonMode(c) {
				fmt.Fprintln(os.Stderr, "  "+line)
				last = line
			}
			if cur.State == models.JobSucceeded || cur.State == models.JobFailed || cur.State == models.JobCanceled {
				out = append(out, *cur)
				// A playlist import queued one import per video: run those too.
				if more, ok := cur.Result["jobIds"].(bson.A); ok {
					for _, m := range more {
						if s, ok := m.(string); ok {
							ids = append(ids, s)
						}
					}
				}
				break
			}
			select {
			case <-ctx.Done():
				if stop != nil {
					stop()
				}
				// Stopping here leaves the import to resume later (its partial download is kept).
				return out, ctx.Err()
			case <-time.After(700 * time.Millisecond):
			}
		}
		if stop != nil {
			stop()
		}
	}
	return out, nil
}

// importLine is one progress line: "Downloading 45% (12.3 MB of 27.1 MB, 3.2 MB/s)".
func importLine(j *models.Job) string {
	p := j.Progress
	switch j.State {
	case models.JobQueued:
		if j.AttemptCount > 0 {
			return "Trying again after a problem…"
		}
		return "Waiting to start…"
	case models.JobSucceeded:
		return "Added to MetaRush."
	case models.JobFailed:
		return "Couldn't import it."
	case models.JobCanceled:
		return "Stopped."
	}
	label := linkimport.StageLabels[p.Stage]
	if p.Message != "" && p.Stage != "" {
		label = p.Message
	}
	if label == "" {
		label = linkimport.StageLabels[linkimport.StageLookup]
	}
	switch p.Stage {
	case linkimport.StageDownload:
		s := fmt.Sprintf("%s %d%%", label, int(p.Pct*100))
		if p.Total > 0 {
			s += fmt.Sprintf(" (%s of %s", fmtBytes(p.Done), fmtBytes(p.Total))
			if p.Rate > 0 {
				s += ", " + fmtBytes(int64(p.Rate)) + "/s"
			}
			s += ")"
		}
		return s
	case linkimport.StageAdding:
		return fmt.Sprintf("%s %d%%", label, int(p.Pct*100))
	}
	return label + "…"
}

func printImportResults(a *app.App, done, processing []models.Job, details bool) {
	fmt.Println()
	for _, j := range done {
		p := linkimport.PayloadOf(&j)
		switch j.State {
		case models.JobSucceeded:
			title, _ := j.Result["title"].(string)
			status, _ := j.Result["status"].(string)
			switch status {
			case "already":
				fmt.Printf("Already in MetaRush: “%s”.\n", title)
			case "expanded":
				fmt.Printf("Found %v videos in “%s”; each is imported on its own (Activity in the dashboard shows them).\n", j.Result["queued"], title)
			default:
				fmt.Printf("Imported “%s” from %s.\n", title, models.PlatformName(p.Platform))
			}
		default:
			if adv := linkimport.AdviceOf(&j); adv != nil {
				fmt.Printf("Couldn't import %s: %s\n    %s\n", p.Input, adv.Message, adv.Next)
				if adv.SignIn && details {
					fmt.Printf("    with your browser's sign-in: metarush import %s --browser-signin chrome\n", p.Input)
				}
			} else {
				fmt.Printf("Couldn't import %s. Activity in the dashboard can try again.\n", p.Input)
			}
			if details && j.Error != "" {
				fmt.Printf("    details: %s\n", j.Error)
			}
		}
		if details {
			fmt.Printf("    job %s", j.ID)
			if id, _ := j.Result["assetId"].(string); id != "" {
				fmt.Printf(", clip %s", id)
			}
			fmt.Println()
		}
	}
	if len(processing) > 0 {
		ready, failed := 0, 0
		for _, j := range processing {
			switch j.State {
			case models.JobSucceeded:
				ready++
			case models.JobFailed:
				failed++
			}
		}
		fmt.Printf("Processing: %d of %d steps ready", ready, len(processing))
		if failed > 0 {
			fmt.Printf(", %d didn't finish (retry from Activity in the dashboard)", failed)
		}
		fmt.Println(".")
	}
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func printImportTools(st *linkimport.ImportTools, details bool) {
	if !st.Installed {
		fmt.Println("The downloader for link imports (yt-dlp) isn't installed on this machine.")
	} else {
		fmt.Println("Downloader: " + st.Summary)
		how := map[string]string{"homebrew": "updated with Homebrew (brew upgrade yt-dlp)", "self": "updates itself (yt-dlp -U)", "pip": "installed with pip (update it with pip)", "unknown": "installed in a way MetaRush can't update"}[st.ManagedBy]
		if how != "" {
			fmt.Println("It is " + how + ".")
		}
		if st.AutoUpdate {
			fmt.Println("MetaRush keeps it up to date about once a week.")
		} else {
			fmt.Println("Automatic updates are off (turn them on in Settings > Link imports).")
		}
		if st.LastResult != "" && st.LastCheck != nil {
			fmt.Printf("Last check %s: %s\n", st.LastCheck.Local().Format("2 Jan 15:04"), st.LastResult)
		}
	}
	for _, p := range st.Problems {
		fmt.Println("! " + p)
	}
	if details {
		fmt.Printf("  path: %s\n  JavaScript runtime: %s\n  challenge scripts: %s\n  PO-token plugins: %v\n  ffmpeg: %s\n", st.Path, orNone(st.JSRuntime), orNone(st.ChallengeScripts), st.POTokenProviders, orNone(st.FFmpeg))
	}
}
