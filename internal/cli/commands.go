package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/uploads"
	"github.com/metavert-io/metarush/internal/version"
	"github.com/metavert-io/metarush/internal/worker"
)

func projectCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "Create, list and inspect projects; delete, restore and clear out archived ones"}
	var brief models.Brief
	var platforms, constraints []string
	create := &cobra.Command{
		Use: "create <name>", Short: "Create a project", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			brief.Platforms, brief.Constraints = platforms, constraints
			return runOp(c, "project.create", ops.ProjectCreateIn{Name: strings.Join(args, " "), Brief: brief}, func(o any) {
				p := o.(*ops.ProjectOut)
				fmt.Printf("Created “%s”.\n%s\n", p.Project.Name, p.URL)
				if detailsMode(c) {
					fmt.Println("project:", p.Project.ID)
				}
			})
		},
	}
	create.Flags().StringVar(&brief.Goal, "goal", "", "what the video should achieve")
	create.Flags().StringVar(&brief.Audience, "audience", "", "who it is for")
	create.Flags().StringVar(&brief.Tone, "tone", "", "tone (e.g. intimate, energetic, documentary)")
	create.Flags().Float64Var(&brief.TargetSecs, "target-seconds", 0, "target duration")
	create.Flags().StringSliceVar(&platforms, "platform", nil, "target platform(s)")
	create.Flags().StringSliceVar(&constraints, "constraint", nil, "constraints (e.g. 'no advertising tone')")
	create.Flags().StringVar(&brief.TransitionStyle, "transition-style", "", "how transitions should feel: natural, snappy, smooth or playful")
	cmd.AddCommand(create, projectSoFarCmd(), projectReceiptCmd(), projectLeavesCmd())
	var archived bool
	list := &cobra.Command{
		Use: "list", Short: "List projects",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "project.list", ops.ProjectListIn{IncludeArchived: archived}, func(o any) {
				ps := o.(*ops.ProjectListOut).Projects
				details := detailsMode(c)
				if len(ps) == 0 {
					fmt.Println("No projects yet.")
					if details {
						fmt.Println(`start one: metarush project create "My film"`)
					}
				}
				for _, p := range ps {
					line := fmt.Sprintf("%-32s %s · %s · changed %s", trunc(p.Name, 32), plural(p.AssetCount, "clip"), plural(p.RevisionSeq, "version"), plainWhen(p.UpdatedAt))
					if p.Archived {
						line += " · archived"
					}
					if details {
						line += "  " + p.ID
					}
					fmt.Println(line)
				}
			})
		},
	}
	list.Flags().BoolVar(&archived, "archived", false, "include archived projects")
	cmd.AddCommand(list)
	cmd.AddCommand(&cobra.Command{
		Use: "show <id>", Short: "Show a project", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "project.show", ops.ProjectShowIn{ID: args[0]}, func(o any) { printProjectShow(o.(*ops.ProjectShowOut), detailsMode(c)) })
		},
	})
	var upName, upGoal, upAudience, upTone, upStyle string
	var upTarget float64
	var upPlatforms, upDeliverables []string
	var archive, unarchive bool
	update := &cobra.Command{
		Use: "update <id>", Short: "Rename a project or edit its brief (--archive deletes it, like project delete; --unarchive restores it)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in := ops.ProjectUpdateIn{ID: args[0], Name: upName}
			if archive || unarchive {
				// Archiving is deleting (D196): project delete / restore.
				if archive && unarchive {
					return fail(c, apperr.InvalidInput, "Choose --archive or --unarchive.", "")
				}
				for _, f := range []string{"name", "goal", "audience", "tone", "target-seconds", "platform", "deliverable", "transition-style"} {
					if c.Flags().Changed(f) {
						return fail(c, apperr.InvalidInput, "Delete or restore the project on its own.", "metarush project delete <project> moves it to Archived projects; metarush project restore brings it back.")
					}
				}
				if archive {
					return deleteProject(c, args[0], false)
				}
				return runOp(c, "project.restore", ops.ProjectRestoreIn{ID: args[0]}, func(o any) { fmt.Println(o.(*ops.ProjectArchiveOut).Message) })
			}
			if c.Flags().Changed("transition-style") {
				v := strings.ToLower(strings.TrimSpace(upStyle))
				if v == "none" {
					v = ""
				}
				in.TransitionStyle = &v
			}
			if c.Flags().Changed("goal") || c.Flags().Changed("audience") || c.Flags().Changed("tone") || c.Flags().Changed("target-seconds") || c.Flags().Changed("platform") ||
				c.Flags().Changed("deliverable") {
				a, err := openApp(c, "cli")
				if err != nil {
					return err
				}
				cur, err := ops.CallTyped[ops.ProjectShowOut](c.Context(), a, "project.show", ops.ProjectShowIn{ID: args[0]}, ops.Caller{})
				a.Close()
				if err != nil {
					return failErr(c, err)
				}
				b := cur.Project.Brief
				if c.Flags().Changed("goal") {
					b.Goal = upGoal
				}
				if c.Flags().Changed("audience") {
					b.Audience = upAudience
				}
				if c.Flags().Changed("tone") {
					b.Tone = upTone
				}
				if c.Flags().Changed("target-seconds") {
					b.TargetSecs = upTarget
				}
				if c.Flags().Changed("platform") {
					b.Platforms = upPlatforms
				}
				if c.Flags().Changed("deliverable") {
					// Where the video goes (D166); its shape follows from the words.
					b.Deliverables = nil
					for _, d := range upDeliverables {
						if d = strings.TrimSpace(d); d != "" && !strings.EqualFold(d, "none") {
							b.Deliverables = append(b.Deliverables, models.Deliverable{Name: d})
						}
					}
				}
				in.Brief = &b
			}
			return runOp(c, "project.update", in, func(o any) {
				p := o.(*ops.ProjectOut).Project
				fmt.Printf("Saved “%s”.\n", p.Name)
				if detailsMode(c) {
					fmt.Println("project:", p.ID)
				}
				if c.Flags().Changed("transition-style") {
					if p.Brief.TransitionStyle == "" {
						fmt.Println("Transitions: no style set (the editing skills use Natural).")
					} else {
						fmt.Printf("Transitions: %s.\n", styleWords(p.Brief.TransitionStyle))
					}
				}
			})
		},
	}
	update.Flags().StringVar(&upName, "name", "", "new name")
	update.Flags().StringVar(&upGoal, "goal", "", "brief: goal")
	update.Flags().StringVar(&upAudience, "audience", "", "brief: audience")
	update.Flags().StringVar(&upTone, "tone", "", "brief: tone")
	update.Flags().Float64Var(&upTarget, "target-seconds", 0, "brief: target duration")
	update.Flags().StringSliceVar(&upPlatforms, "platform", nil, "brief: target platform(s)")
	update.Flags().StringArrayVar(&upDeliverables, "deliverable", nil, "brief: where the video goes (by email, on the website, social shorts: its shape follows); repeat for more, none clears")
	update.Flags().StringVar(&upStyle, "transition-style", "", "how transitions should feel: natural, snappy, smooth or playful (none clears it)")
	update.Flags().BoolVar(&archive, "archive", false, "delete the project: it moves to Archived projects (the same as project delete)")
	update.Flags().BoolVar(&unarchive, "unarchive", false, "restore an archived project (the same as project restore)")
	cmd.AddCommand(update)
	cmd.AddCommand(projectDeleteCmds()...) // delete | restore | archived | purge (projectdelete.go, D196)
	cmd.AddCommand(projectFolderCmds()...) // checkout | status | pull | push (cut.go)
	cmd.AddCommand(projectLinkCmds()...)   // link | unlink | links | use (resources.go)
	cmd.AddCommand(projectRulesCmd())      // rules | rules add | rules remove (calls.go)
	return cmd
}

func auditCmd() *cobra.Command {
	var in ops.AuditListIn
	cmd := &cobra.Command{
		Use: "audit", Short: "Read the audit trail: who did what, from which machine and channel",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "audit.list", in, func(o any) {
				details := detailsMode(c)
				events := o.(*ops.AuditListOut).Events
				if len(events) == 0 {
					fmt.Println("Nothing recorded yet.")
				}
				for _, e := range events {
					who := orWord(e.Actor.UserName, "someone")
					// The action is the record's own name (project.create, render.submit…).
					fmt.Printf("%s  %-22s %s, %s\n", e.At.Local().Format("2 Jan 15:04:05"), e.Action, who, viaWords(e.Actor.Via))
					if details {
						fmt.Printf("    %s %s · user %s · machine %s · via %s\n", e.TargetType, e.TargetID, e.Actor.UserID, e.Actor.InstallationID, e.Actor.Via)
					}
				}
			})
		},
	}
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "only this project")
	cmd.Flags().StringVar(&in.TargetID, "target", "", "only events about this ID")
	cmd.Flags().Int64Var(&in.Limit, "limit", 0, "maximum events (default 100)")
	return cmd
}

func assetCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "asset", Short: "List and inspect footage"}
	var in ops.AssetListIn
	list := &cobra.Command{
		Use: "list [project-id]", Short: "List footage (a project's, the Inbox's or all of it)", Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				in.ProjectID = args[0]
			}
			return runOp(c, "asset.list", in, func(o any) {
				details := detailsMode(c)
				assets := o.(*ops.AssetListOut).Assets
				if len(assets) == 0 {
					fmt.Println("No footage here yet.")
				}
				for _, as := range assets {
					line := assetLine(as)
					if as.Width > 0 && as.Height > 0 {
						line += fmt.Sprintf("  %d×%d", as.Width, as.Height)
					}
					if t := transcriptWords(as.Transcript, as.HasAudio); t != "" {
						line += "  " + t
					}
					fmt.Println(line)
					if len(as.Aliases) > 0 {
						fmt.Printf("      also called: %s\n", strings.Join(as.Aliases, ", "))
					}
					if details {
						tx := "-"
						if as.Transcript != nil {
							tx = as.Transcript.State
						}
						fmt.Printf("      %s  state %s  transcript %s  %s\n", as.ID, as.State, tx, strings.Join(as.Variants, ","))
					}
				}
			})
		},
	}
	list.Flags().StringVar(&in.ProjectID, "project", "", "project ID")
	list.Flags().BoolVar(&in.Unassigned, "unassigned", false, "only the Inbox (footage nobody has filed yet)")
	list.Flags().StringVar(&in.Query, "query", "", "filename filter")
	cmd.AddCommand(list)
	cmd.AddCommand(&cobra.Command{
		Use: "show <id>", Short: "Show one clip: what it is, how far along it is, where it came from (--json has everything)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "asset.show", ops.AssetShowIn{ID: args[0]}, func(o any) { printAssetShow(o.(*ops.AssetShowOut), detailsMode(c)) })
		},
	})
	var project string
	var unlink bool
	link := &cobra.Command{
		Use: "link <asset-id> --project <project>", Short: "Add a clip to a project (or take it out with --unlink: the footage stays in the workspace, in the Inbox when no project has it)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "asset.link", ops.AssetLinkIn{AssetID: args[0], ProjectID: project, Unlink: unlink}, func(o any) {
				as := o.(*ops.AssetLinkOut).Asset
				switch {
				case !unlink:
					fmt.Printf("Added “%s”.\n", as.Title)
				case len(as.ProjectIDs) == 0 && len(as.CollectionIDs) == 0 && !as.Archived:
					fmt.Printf("Removed “%s” from the project; it's back in the Inbox.\n", as.Title)
				default:
					fmt.Printf("Removed “%s” from the project.\n", as.Title)
				}
			})
		},
	}
	link.Flags().StringVar(&project, "project", "", "project (ID or name)")
	link.Flags().BoolVar(&unlink, "unlink", false, "remove from the project instead (never deletes the footage)")
	cmd.AddCommand(link)
	cmd.AddCommand(assetExtraCmds()...)   // where | move | delete (resources.go)
	cmd.AddCommand(assetLibraryCmds()...) // archive | unarchive | trash | restore (library.go)
	var title string
	var suggested bool
	update := &cobra.Command{
		Use: "update <asset-id> --title <title> | --suggested", Short: "Rename a clip: a descriptive title (its original file name stays as file info), or MetaRush's suggested name with --suggested", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if !c.Flags().Changed("title") && !suggested {
				return fail(c, "E_INVALID_INPUT", "Nothing to change: give the new name with --title, or take MetaRush's suggestion with --suggested.", "")
			}
			return runOp(c, "asset.update", ops.AssetUpdateIn{ID: args[0], Title: title, Suggested: suggested}, func(o any) {
				as := o.(*ops.AssetLinkOut).Asset
				fmt.Printf("Renamed it “%s” (the file is still %s).\n", as.Title, as.Name)
				if detailsMode(c) {
					fmt.Println("clip:", as.ID)
				}
			})
		},
	}
	update.Flags().StringVar(&title, "title", "", "new descriptive title (yours: MetaRush never replaces it)")
	update.Flags().BoolVar(&suggested, "suggested", false, "use the name MetaRush suggests (asset card or asset names shows it)")
	cmd.AddCommand(update)
	cmd.AddCommand(assetSourceCmds()...) // names | card | fixes (sources.go)
	var dupProject string
	var keep, skip bool
	dup := &cobra.Command{
		Use: "duplicate <asset-id> <filename> --keep|--skip", Short: "Decide about a file identical to a clip you already have: --keep keeps its name too (stored once), --skip changes nothing", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			if keep == skip {
				return fail(c, "E_INVALID_INPUT", "Choose one: --keep or --skip.", "")
			}
			action := "skip"
			if keep {
				action = "keep"
			}
			return runOp(c, "asset.duplicate", ops.AssetDuplicateIn{AssetID: args[0], Name: args[1], Action: action, ProjectID: dupProject}, func(o any) {
				out := o.(*ops.AssetDuplicateOut)
				fmt.Printf("%s: %s\n", args[1], describeDuplicate(&out.Duplicate))
			})
		},
	}
	dup.Flags().BoolVar(&keep, "keep", false, "keep both names (stored once; added to --project)")
	dup.Flags().BoolVar(&skip, "skip", false, "skip the copy")
	dup.Flags().StringVar(&dupProject, "project", "", "project the copy was meant for")
	cmd.AddCommand(dup)
	cmd.AddCommand(assetRightsCmd())
	return cmd
}

func ingestCmd() *cobra.Command {
	var in ingest.FilesIn
	var noTranscribe, wait, keepBoth bool
	cmd := &cobra.Command{
		Use:   "ingest <file-or-folder>...",
		Short: "Add footage: each file is copied into MetaRush's storage (files it already has are skipped), then preview copies and transcripts are made",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Paths = args
			if noTranscribe {
				f := false
				in.Transcribe = &f
			}
			if keepBoth {
				in.Duplicates = ingest.DuplicatesKeep
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			details := detailsMode(c)
			if !jsonMode(c) {
				last := map[string]string{}
				ingest.Progress = func(path, phase string, done, total int64) {
					pct := 0.0
					if total > 0 {
						pct = 100 * float64(done) / float64(total)
					}
					step := phase
					if !details {
						step = ingestPhaseWords[phase]
						if step == "" {
							step = phase
						}
					}
					line := fmt.Sprintf("%-8s %3.0f%%", step, pct)
					if last[path] != line && (phase != "upload" || int(pct)%5 == 0) {
						fmt.Fprintf(os.Stderr, "\r  %-40s %s", trunc(baseName(path), 40), line)
						last[path] = line
						if phase == "done" {
							fmt.Fprintln(os.Stderr)
						}
					}
				}
			}
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			b, _ := json.Marshal(in)
			out, err := ops.Call(ctx, a, "ingest.files", b, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			res := out.(*ingest.FilesOut)
			var waited []models.Job
			if wait && !in.DryRun {
				var jobIDs []string
				clips := 0
				for _, r := range res.Results {
					jobIDs = append(jobIDs, r.JobIDs...)
					if len(r.JobIDs) > 0 {
						clips++
					}
				}
				if len(jobIDs) > 0 {
					switch {
					case jsonMode(c):
					case details:
						fmt.Fprintf(os.Stderr, "waiting for %d processing job(s)…\n", len(jobIDs))
					default:
						fmt.Fprintf(os.Stderr, "Preparing %s: preview copies, transcripts and the rest…\n", plural(clips, "clip"))
					}
					waited, err = waitJobs(c, a, jobIDs, 0, false)
					if err != nil {
						return failErr(c, err)
					}
				}
			}
			payload := map[string]any{"ingest": res}
			if waited != nil {
				payload["jobs"] = waited
			}
			return emit(c, payload, func() { printIngest(res, waited, wait && !in.DryRun, details) })
		},
	}
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "add to this project")
	cmd.Flags().BoolVarP(&in.Recursive, "recursive", "r", false, "descend into subfolders")
	cmd.Flags().StringSliceVar(&in.Tags, "tag", nil, "tags to apply")
	cmd.Flags().BoolVar(&noTranscribe, "no-transcribe", false, "don't make transcripts")
	cmd.Flags().BoolVar(&in.DryRun, "dry-run", false, "only list the files; add nothing")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until preview copies and transcripts are ready (made here if no other machine can)")
	cmd.Flags().BoolVar(&keepBoth, "keep-both", false, "a file identical to a clip you already have, under another name: keep that name too (default: skip it)")
	var preProject string
	precheck := &cobra.Command{
		Use: "precheck <file>...", Short: "Before adding: which files MetaRush already has, which repeat in the list, and which are new", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			files, _ := ingest.Collect(args, true)
			in := uploads.PrecheckIn{ProjectID: preProject}
			for _, f := range files {
				fp, size, err := media.FingerprintFile(f)
				if err != nil {
					return failErr(c, err)
				}
				sample, err := media.SampleFile(f)
				if err != nil {
					return failErr(c, err)
				}
				in.Files = append(in.Files, uploads.PrecheckFile{Name: f, Size: size, Fingerprint: fp, Sample: sample})
			}
			if len(in.Files) == 0 {
				return fail(c, "E_INVALID_INPUT", "No files found there.", "")
			}
			return runOp(c, "asset.precheck", in, func(o any) {
				out := o.(*uploads.PrecheckOut)
				details := detailsMode(c)
				for _, r := range out.Results {
					f := in.Files[r.Index].Name
					name := baseName(f)
					if details {
						name = f
					}
					switch {
					case r.Duplicate != nil:
						fmt.Printf("already there  %s  identical to “%s” (in %s)\n", name, r.Duplicate.Title, r.Duplicate.Where)
						if details {
							fmt.Printf("               clip %s\n", r.Duplicate.AssetID)
						}
					case r.BatchDuplicateOf != nil:
						fmt.Printf("repeated       %s  identical to %s (in this list)\n", name, baseName(in.Files[*r.BatchDuplicateOf].Name))
					default:
						fmt.Printf("new            %s\n", name)
					}
				}
				switch out.Duplicates {
				case 0:
					fmt.Println("\nNone of them is in MetaRush yet.")
				case 1:
					fmt.Println("\n1 of them is already in MetaRush.")
				default:
					fmt.Printf("\n%d of them are already in MetaRush.\n", out.Duplicates)
				}
			})
		},
	}
	precheck.Flags().StringVar(&preProject, "project", "", "the project the files are meant for")
	cmd.AddCommand(precheck)
	var sess ingest.SessionsIn
	sessions := &cobra.Command{
		Use: "sessions", Short: "List uploads into MetaRush's storage: in progress, interrupted or finished",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "ingest.sessions", sess, func(o any) {
				details := detailsMode(c)
				sessions := o.(*ingest.SessionsOut).Sessions
				if len(sessions) == 0 {
					fmt.Println("No uploads yet.")
				}
				for _, s := range sessions {
					pct := 0.0
					if s.Size > 0 {
						pct = 100 * float64(s.BytesDone) / float64(s.Size)
					}
					fmt.Printf("%-30s %-12s %3.0f%% of %-9s %s, %s\n", trunc(s.SourceName, 30), uploadStateWords(s.State), pct, fmtBytes(s.Size), viaWords(s.Via), plainWhen(s.UpdatedAt))
					if details {
						fmt.Printf("    %s  %s\n", s.ID, s.State)
					}
				}
			})
		},
	}
	sessions.Flags().StringVar(&sess.State, "state", "", "filter by state")
	sessions.Flags().Int64Var(&sess.Limit, "limit", 0, "maximum (default 100)")
	cmd.AddCommand(sessions)
	var rec ingest.ReconcileIn
	reconcile := &cobra.Command{
		Use: "reconcile", Short: "Clean up uploads that were abandoned (never touches footage that is ready)",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "ingest.reconcile", rec, func(o any) {
				out := o.(*ingest.ReconcileOut)
				verb := "Cleaned up"
				if out.DryRun {
					verb = "Would clean up"
				}
				fmt.Printf("%s %s and %s.\n", verb, plural(len(out.AbortedUploads), "abandoned upload"), plural(len(out.StaleSessions), "upload record"))
				if out.DryRun {
					fmt.Println("Nothing has changed yet.")
				}
			})
		},
	}
	reconcile.Flags().Float64Var(&rec.OlderThanHours, "older-than-hours", 0, "only uploads idle this long (default 24)")
	reconcile.Flags().BoolVar(&rec.DryRun, "dry-run", false, "report without changing anything")
	cmd.AddCommand(reconcile)
	return cmd
}

func footageCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "footage", Short: "What is in your footage: transcripts, pauses, scenes, frames and who speaks"}
	var an ops.AnalyzeIn
	var noTx, noVisual, wait bool
	analyze := &cobra.Command{
		Use: "analyze", Short: "Prepare footage (again): transcripts, pauses and loud parts, and on a Mac what's on screen",
		RunE: func(c *cobra.Command, args []string) error {
			if noTx {
				f := false
				an.Transcribe = &f
			}
			if noVisual {
				f := false
				an.Visual = &f
			}
			if !wait {
				return runOp(c, "footage.analyze", an, func(o any) { printAnalyze(o.(*ops.AnalyzeOut), nil, detailsMode(c)) })
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			b, _ := json.Marshal(an)
			out, err := ops.Call(c.Context(), a, "footage.analyze", b, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			res := out.(*ops.AnalyzeOut)
			js, err := waitJobs(c, a, res.Jobs, 0, false)
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, map[string]any{"analyze": res, "jobs": js}, func() { printAnalyze(res, js, detailsMode(c)) })
		},
	}
	analyze.Flags().StringVar(&an.AssetID, "asset", "", "one clip")
	analyze.Flags().StringVar(&an.ProjectID, "project", "", "every clip in a project")
	analyze.Flags().BoolVar(&noTx, "no-transcribe", false, "no transcripts: only pauses, loud parts and picture changes")
	analyze.Flags().BoolVar(&noVisual, "no-visual", false, "do not look at what's on screen")
	analyze.Flags().BoolVar(&an.Force, "force", false, "do it again even if it was done")
	analyze.Flags().BoolVar(&wait, "wait", false, "wait for the results (made here if no other machine can)")
	cmd.AddCommand(analyze)
	var tr ops.TranscriptIn
	transcript := &cobra.Command{
		Use: "transcript <asset-id>", Short: "Print what was said, with times", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			tr.AssetID = args[0]
			return runOp(c, "footage.transcript", tr, func(o any) { printTranscript(o.(*ops.TranscriptOut), detailsMode(c)) })
		},
	}
	transcript.Flags().Float64Var(&tr.From, "from", 0, "start (s)")
	transcript.Flags().Float64Var(&tr.To, "to", 0, "end (s)")
	transcript.Flags().BoolVar(&tr.Words, "words", false, "include word timings (JSON)")
	transcript.Flags().StringVar(&tr.Speaker, "speaker", "", `only what one person says ("Ada", "Speaker 2")`)
	transcript.Flags().BoolVar(&tr.Original, "original", false, "the transcript as the computer wrote it, before anyone fixed it")
	cmd.AddCommand(transcript)
	cmd.AddCommand(speakersCmd())
	cmd.AddCommand(footageLanguageCmd())
	cmd.AddCommand(footageCaptionsCmd())
	cmd.AddCommand(footageShortCmd())
	cmd.AddCommand(visualsCmd())
	cmd.AddCommand(facesCmd())
	cmd.AddCommand(describeCmd())
	cmd.AddCommand(&cobra.Command{
		Use: "evidence <asset-id>", Short: "What MetaRush measured in a clip: pauses, cuts and picture changes, black or frozen picture, loudness", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "footage.evidence", ops.EvidenceIn{AssetID: args[0]}, func(o any) { printEvidence(o.(*ops.EvidenceOut), detailsMode(c)) })
		},
	})
	var mo ops.MomentsIn
	moments := &cobra.Command{
		Use: "moments [project-id|asset-id]", Short: "Moments worth a look across a project or one clip: what was said, pauses and picture changes, with times", Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 && strings.HasPrefix(args[0], "ast_") {
				mo.AssetID = args[0]
			} else if err := projectFrom(c, args, &mo.ProjectID); err != nil {
				return err
			}
			return runOp(c, "footage.moments", mo, func(o any) { printMoments(o.(*ops.MomentsOut), detailsMode(c)) })
		},
	}
	moments.Flags().StringVar(&mo.ProjectID, "project", "", "project ID or name")
	moments.Flags().StringVar(&mo.Query, "query", "", "words to look for in what was said")
	moments.Flags().IntVar(&mo.Limit, "limit", 0, "most moments")
	cmd.AddCommand(moments)
	var fr ops.FramesIn
	var at string
	frames := &cobra.Command{
		Use: "frames <asset-id> --at 1.5,3,7.25", Short: "Save still frames as pictures (so Claude can look at them)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			fr.AssetID = args[0]
			for _, s := range strings.Split(at, ",") {
				if s = strings.TrimSpace(s); s != "" {
					v, err := strconv.ParseFloat(s, 64)
					if err != nil {
						return fail(c, apperr.InvalidInput, fmt.Sprintf("%q isn't a time: give seconds, like 1.5.", s), "")
					}
					fr.At = append(fr.At, v)
				}
			}
			return runOp(c, "footage.frames", fr, func(o any) {
				out := o.(*ops.FramesOut)
				var times []string
				for _, f := range out.Frames {
					times = append(times, fmtDur(f.At))
				}
				fmt.Printf("Saved %s: at %s.\n", plural(len(out.Frames), "still frame"), joinWords(times))
				if !detailsMode(c) {
					fmt.Println("(Add --details to see where they are.)")
					return
				}
				for _, f := range out.Frames {
					fmt.Printf("%8.3fs  %s\n", f.At, f.Path)
				}
				if out.Note != "" {
					fmt.Println("note:", out.Note)
				}
			})
		},
	}
	frames.Flags().StringVar(&at, "at", "", "times in the clip, in seconds, separated by commas")
	frames.Flags().IntVar(&fr.Width, "width", 0, "picture width (default 960)")
	frames.Flags().StringVar(&fr.OutDir, "out", "", "folder to save them in")
	frames.Flags().StringVar(&fr.Source, "source", "auto", "auto | proxy (the preview copy) | original")
	cmd.AddCommand(frames)
	var sh ops.SheetIn
	var sheetDetails bool
	sheet := &cobra.Command{
		Use: "sheet <asset-id>", Short: "A labeled grid of frames from a clip: one per shot, then picture changes, new speakers and enough to cover it (or --mode even)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			sh.AssetID = args[0]
			return runOp(c, "footage.sheet", sh, func(o any) {
				s := o.(*ops.SheetOut)
				how := "one per shot, then the moments the picture changes, new speakers and enough to cover the clip"
				if s.Mode == "even" {
					how = "evenly spaced"
				}
				n := len(s.Tiles)
				if n == 0 {
					n = len(s.Times)
				}
				fmt.Printf("Made a grid of %s (%s).\n", plural(n, "frame"), how)
				if !sheetDetails {
					fmt.Println("(Add --details to see where it is and why each frame was chosen.)")
					return
				}
				fmt.Println(s.Path)
				for _, t := range s.Tiles {
					fmt.Println(tileLine(t))
				}
			})
		},
	}
	sheet.Flags().Float64Var(&sh.From, "from", 0, "range start (s)")
	sheet.Flags().Float64Var(&sh.To, "to", 0, "range end (s)")
	sheet.Flags().IntVar(&sh.Frames, "frames", 0, "most frames (smart: default 20; even: exactly this many, default 12)")
	sheet.Flags().StringVar(&sh.Mode, "mode", "smart", "smart | even")
	sheet.Flags().StringVar(&sh.OutDir, "out", "", "folder to save it in")
	sheet.Flags().BoolVar(&sheetDetails, "details", false, "also say where it is, each frame's label and why it was chosen")
	cmd.AddCommand(sheet)
	var kf ops.KeyframesIn
	keyframes := &cobra.Command{
		Use: "keyframes <asset-id>", Short: "The frames a grid of the clip would show, and why (no picture)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			kf.AssetID = args[0]
			return runOp(c, "footage.keyframes", kf, func(o any) {
				k := o.(*ops.KeyframesOut)
				fmt.Printf("%s: %s, %s\n", k.Title, plural(len(k.Tiles), "frame"), plural(k.Shots, "shot"))
				for _, t := range k.Tiles {
					fmt.Println(tileLine(t))
				}
			})
		},
	}
	keyframes.Flags().Float64Var(&kf.From, "from", 0, "range start (s)")
	keyframes.Flags().Float64Var(&kf.To, "to", 0, "range end (s)")
	keyframes.Flags().IntVar(&kf.Tiles, "frames", 0, "most frames (smart: default 20; even: exactly this many, default 12)")
	keyframes.Flags().StringVar(&kf.Mode, "mode", "smart", "smart | even")
	cmd.AddCommand(keyframes)
	cmd.AddCommand(footageSwitchesCmd(), footageFindSwitchesCmd()) // picture switches to the frame, who is on screen, printed names (calls.go)
	return cmd
}

func editCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "edit", Short: "Check and save changes to a cut, written as plan files (the only way a cut changes)"}
	var planPath, confirm string
	var allowUnlock bool
	// applyCommand is the command that saves a checked plan.
	applyCommand := func(c *cobra.Command, plan string, unlock bool, token string) string {
		args := []string{"--plan", plan, "--confirm", token}
		if unlock {
			args = append(args, "--allow-unlock")
		}
		return commandLine(c, "metarush edit apply", args...)
	}
	check := &cobra.Command{
		Use: "check --plan <file|->", Short: "Check a change before saving it: its problems, what it changes, and whether it is ready to save",
		RunE: func(c *cobra.Command, _ []string) error {
			raw, err := readPlan(planPath)
			if err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			return runOp(c, "edit.check", ops.EditCheckIn{Plan: raw, AllowUnlock: allowUnlock}, func(o any) {
				r := o.(*ops.EditCheckOut)
				printCheck(c, r, applyCommand(c, planPath, allowUnlock, r.ConfirmToken))
			})
		},
	}
	check.Flags().StringVar(&planPath, "plan", "-", "the plan file (- reads it from the input)")
	check.Flags().BoolVar(&allowUnlock, "allow-unlock", false, "allow changes to locked parts (only when the person asked)")
	cmd.AddCommand(check)
	apply := &cobra.Command{
		Use: "apply --plan <file> --confirm <token>", Short: "Save a checked change as a new version (earlier versions stay as they are)",
		RunE: func(c *cobra.Command, _ []string) error {
			raw, err := readPlan(planPath)
			if err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			if c.Flags().Changed("dry-run") {
				return runOp(c, "edit.check", ops.EditCheckIn{Plan: raw, AllowUnlock: allowUnlock}, func(o any) {
					r := o.(*ops.EditCheckOut)
					printCheck(c, r, applyCommand(c, planPath, allowUnlock, r.ConfirmToken))
				})
			}
			return runOp(c, "edit.apply", ops.EditApplyIn{Plan: raw, ConfirmToken: confirm, AllowUnlock: allowUnlock}, func(o any) { printApplied(o.(*ops.EditApplyOut), detailsMode(c)) })
		},
	}
	apply.Flags().StringVar(&planPath, "plan", "-", "the plan file (- reads it from the input)")
	apply.Flags().StringVar(&confirm, "confirm", "", "the token `edit check` gave for exactly this plan")
	apply.Flags().BoolVar(&allowUnlock, "allow-unlock", false, "allow changes to locked parts (only when the person asked)")
	apply.Flags().Bool("dry-run", false, "same as `edit check`")
	cmd.AddCommand(apply)
	cmd.AddCommand(&cobra.Command{
		Use: "templates", Short: "List the ready-made graphics (titles, name titles, quotes, end cards, slides) and how long each can be",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "edit.templates", ops.EmptyIn{}, func(o any) {
				details := detailsMode(c)
				for _, t := range o.(*ops.TemplatesOut).Templates {
					fmt.Printf("%-24s %s to %s  %s\n", t.ID, secs(t.MinDuration), secs(t.MaxDuration), t.Summary)
					if details {
						fmt.Printf("%-24s version %s · %s of the frame\n", "", t.Version, t.Region)
					}
				}
				if details {
					fmt.Println("\neach one's settings: metarush schema show template.<id>")
				}
			})
		},
	})
	cmd.AddCommand(editTransitionsCmd())
	var previewUnlock bool
	preview := &cobra.Command{
		Use:   "preview --plan <file|->",
		Short: "Try a change without saving it: what it does to the cut and its problems, in plain words",
		Long: `Try a change without saving anything (no token needed): what it changes, in plain
words, problems that would stop it from being saved, and whether a newer version
changes the same parts. --json adds the cut it makes and its frame-by-frame plan
(what the dashboard's timeline shows after every gesture).`,
		RunE: func(c *cobra.Command, _ []string) error {
			raw, err := readPlan(planPath)
			if err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			return runOp(c, "edit.preview", ops.EditPreviewIn{Plan: raw, AllowUnlock: previewUnlock}, func(o any) { printPreview(o.(*ops.EditPreviewOut)) })
		},
	}
	preview.Flags().StringVar(&planPath, "plan", "-", "the plan file (- reads it from the input)")
	preview.Flags().BoolVar(&previewUnlock, "allow-unlock", false, "allow changes to locked parts (only when the person asked)")
	cmd.AddCommand(preview)
	// Spec-mandated alias: `metarush edit plan validate <file>`.
	planCmd := &cobra.Command{Use: "plan", Short: "Plan files"}
	planCmd.AddCommand(&cobra.Command{
		Use: "validate <file>", Short: "Check a plan file (the same as edit check)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			raw, err := readPlan(args[0])
			if err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			return runOp(c, "edit.check", ops.EditCheckIn{Plan: raw}, func(o any) {
				r := o.(*ops.EditCheckOut)
				printCheck(c, r, applyCommand(c, args[0], false, r.ConfirmToken))
			})
		},
	})
	cmd.AddCommand(planCmd)
	return cmd
}

// printApplied says which version a saved change made.
func printApplied(r *ops.EditApplyOut, details bool) {
	fmt.Printf("Saved version %d (%s).\n", r.Revision.Number, seconds(r.Revision.Duration))
	if r.Rebased {
		fmt.Println("Someone saved a newer version meanwhile; your change doesn't overlap it, so both are kept.")
	}
	fmt.Println(r.URL)
	for _, s := range r.Diff.Summary {
		fmt.Println("  - " + s)
	}
	printIssues(nil, r.Warnings, details)
	if details {
		fmt.Println("version:", r.Revision.ID)
	}
}

// printIssues lists a plan's problems (✗ must be fixed) and warnings (! worth
// checking) in their own words; details adds each one's code and where in the
// plan it is.
func printIssues(errs, warns []editspec.Issue, details bool) {
	for _, group := range []struct {
		mark string
		list []editspec.Issue
	}{{"✗", errs}, {"!", warns}} {
		for _, e := range group.list {
			fmt.Printf("  %s %s\n", group.mark, e.Message)
			if details {
				fmt.Printf("    (%s at %s)\n", e.Code, orNone(e.Path))
			}
		}
	}
}

func printPreview(r *ops.EditPreviewOut) {
	switch {
	case r.Conflict != nil:
		fmt.Println(r.Conflict.Message)
		fmt.Println("Saving this would be refused; start again from the latest version, or keep yours by writing it against the latest.")
		return
	case r.Valid:
		fmt.Printf("%s. The video would be %s long.\n", r.Summary, seconds(r.Duration))
	default:
		fmt.Printf("%s, but it can't be saved as it is:\n", r.Summary)
	}
	for _, p := range r.Errors {
		fmt.Println("  ✗ " + p.Message)
		if p.Hint != "" {
			fmt.Println("    " + p.Hint)
		}
	}
	printList("", r.Changes)
	for _, p := range r.Warnings {
		fmt.Println("  ! " + p.Message)
	}
	if r.Rebased {
		fmt.Println("Someone saved a newer version; this doesn't overlap theirs, so saving would keep both.")
	}
	fmt.Println("Nothing was saved.")
}

// printCheck says whether a checked plan can be saved and what it changes;
// next is the exact command that saves it (shown with --details).
func printCheck(c *cobra.Command, r *ops.EditCheckOut, next string) {
	details := detailsMode(c)
	switch {
	case r.Valid && r.BaseRevisionID == "":
		fmt.Printf("Looks good: a first cut, %s long.\n", seconds(r.Duration))
	case r.Valid:
		fmt.Printf("Looks good: the cut will be %s long.\n", seconds(r.Duration))
	case len(r.Errors) > 0:
		fmt.Printf("This can't be saved yet: %s to fix.\n", plural(len(r.Errors), "problem"))
	default:
		fmt.Println("This can't be saved yet.")
	}
	printIssues(r.Errors, r.Warnings, details)
	if r.Diff != nil {
		for _, s := range r.Diff.Summary {
			fmt.Println("  - " + s)
		}
	}
	if r.Rebased {
		fmt.Println("Someone saved a newer version; your change doesn't overlap it, so both are kept.")
	}
	if details {
		fmt.Printf("based on: %s", orNone(r.BaseRevisionID))
		if r.Rebased {
			fmt.Printf(" (the latest is %s)", r.HeadRevisionID)
		}
		fmt.Println()
	}
	if r.ConfirmToken != "" {
		readyToSave(c, next)
	} else if details && r.Next != "" {
		fmt.Println("next: " + r.Next)
	}
}

func revisionCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "revision", Short: "Look at, compare and approve a project's versions"}
	cmd.AddCommand(revisionNoteCmd())                            // note (sources.go)
	cmd.AddCommand(revisionWaitingCmd(), revisionAskReviewCmd()) // waiting, ask-review (follow.go, D188)
	cmd.AddCommand(revisionPacketCmd())                          // packet (drafts.go, D250)
	var branch string
	list := &cobra.Command{
		Use: "list [project-id]", Short: "List a project's versions (* marks the latest)", Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			p, _ := c.Flags().GetString("project")
			if err := projectFrom(c, args, &p); err != nil {
				return err
			}
			return runOp(c, "revision.list", ops.RevisionListIn{ProjectID: p, Branch: branch}, func(o any) {
				revs := o.(*ops.RevisionListOut).Revisions
				if len(revs) == 0 {
					fmt.Println("No versions yet: this project has no cut.")
				}
				for _, r := range revs {
					printRevisionRow(r, detailsMode(c))
				}
			})
		},
	}
	list.Flags().String("project", "", "project ID or name")
	list.Flags().StringVar(&branch, "branch", "", "branch")
	cmd.AddCommand(list)
	cmd.AddCommand(&cobra.Command{
		Use: "show <id>", Short: "Show a version: what was asked, its clips, warnings and renders (--json has the whole cut)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "revision.show", ops.RevisionShowIn{ID: args[0]}, func(o any) { printRevisionShow(o.(*ops.RevisionShowOut), detailsMode(c)) })
		},
	})
	var cmpFormat, cmpProject string
	compare := &cobra.Command{
		Use: "compare <left> <right>", Short: "Compare two versions (--format md: the changes in plain words plus the cut sheet lines that differ)", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			if cmpFormat == "md" {
				return revisionCompareMarkdown(c, cmpProject, args[0], args[1])
			}
			return runOp(c, "revision.compare", ops.RevisionCompareIn{Left: args[0], Right: args[1]}, func(o any) {
				out := o.(*ops.RevisionCompareOut)
				d := out.Diff
				fmt.Printf("From version %d to version %d:\n", out.Left.Number, out.Right.Number)
				if len(d.Summary) == 0 && len(d.ClipsChanged) == 0 {
					fmt.Println("  Nothing changed.")
				}
				for _, s := range d.Summary {
					fmt.Println("  - " + s)
				}
				for _, ch := range d.ClipsChanged {
					var fields []string
					for _, f := range ch.Fields {
						fields = append(fields, orWord(clipFieldWords[f], f))
					}
					fmt.Printf("  - %s: %s changed\n", ch.ID, joinWords(fields))
				}
				if detailsMode(c) {
					fmt.Printf("versions: %s → %s\n", out.Left.ID, out.Right.ID)
				}
			})
		},
	}
	compare.Flags().StringVar(&cmpFormat, "format", "text", "text (summary) or md (the changes in plain words plus the cut sheet lines that differ)")
	compare.Flags().StringVar(&cmpProject, "project", "", "project ID or name (needed to name versions by number)")
	cmd.AddCommand(compare)
	cmd.AddCommand(&cobra.Command{
		Use: "plan <id>", Short: "Show the frame-by-frame plan a render of a version follows (--details or --json for all of it)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "revision.plan", ops.RevisionPlanIn{ID: args[0]}, func(o any) { printFramePlan(o.(*editspec.RenderPlan), detailsMode(c)) })
		},
	})
	var reject bool
	var note string
	approve := &cobra.Command{
		Use: "approve <id>", Short: "Approve a version (people only: in their own terminal or the dashboard)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if !interactive() {
				return fail(c, apperr.HumanOnly, "Only a person can approve a version.", "Approve it in the dashboard, or run this yourself in a terminal.")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			verb := "approve"
			if reject {
				verb = "reject"
			}
			if ask(fmt.Sprintf("Type %q to %s this version", verb, verb), "") != verb {
				return fail(c, apperr.Canceled, "Nothing changed.", "")
			}
			return runOpWith(c, a, "revision.approve", ops.RevisionApproveIn{ID: args[0], Reject: reject, Note: note}, ops.Caller{Human: true}, func(o any) {
				r := o.(*ops.RevisionSummary)
				if r.Approval.State == models.ApprovalRejected {
					fmt.Printf("Version %d now needs changes.\n", r.Number)
				} else {
					fmt.Printf("Version %d is now %s.\n", r.Number, approvalWords(r.Approval.State))
				}
				if r.Reminder != "" {
					fmt.Println(r.Reminder) // sign-offs still waiting (D166): a reminder, never a lock
				}
				if reject {
					printToldClaude(r.ToldClaude, note, detailsMode(c)) // the note reaches the conversation that made it (D186)
				}
				if detailsMode(c) {
					fmt.Println("version:", r.ID)
				}
			})
		},
	}
	approve.Flags().BoolVar(&reject, "reject", false, "turn it down instead")
	approve.Flags().StringVar(&note, "note", "", "a note kept with the decision")
	cmd.AddCommand(approve)
	return cmd
}

// clipFieldWords name what changed about a clip.
var clipFieldWords = map[string]string{"in": "start", "out": "end", "assetId": "footage", "speed": "speed", "muted": "sound on or off",
	"volume": "level", "framing": "framing", "transitionIn": "transition", "fadeOut": "fade-out", "label": "label", "locked": "lock", "notes": "notes",
	"rationale": "reason", "evidence": "what it is based on"}

// printRevisionShow describes one version: what was asked, its length, clips,
// warnings and renders; details adds IDs (the whole cut is --json).
func printRevisionShow(o *ops.RevisionShowOut, details bool) {
	r := o.Summary
	title := ""
	if r.Title != "" {
		title = " of “" + r.Title + "”"
	}
	latest := ""
	if r.IsHead {
		latest = " (the latest)"
	}
	fmt.Printf("Version %d%s%s: %s, %s.\n", r.Number, title, latest, seconds(r.Duration), approvalWords(r.Approval.State))
	by := orWord(r.CreatedBy.UserName, "someone")
	if r.Instruction != "" {
		fmt.Printf("“%s” — %s, %s\n", r.Instruction, by, plainWhen(r.CreatedAt))
	} else {
		fmt.Printf("Saved by %s, %s.\n", by, plainWhen(r.CreatedAt))
	}
	if r.Rationale != "" {
		fmt.Println("Why: " + r.Rationale)
	}
	shape := presetWords(r.Output.Preset)
	if r.Output.Width > 0 {
		shape += fmt.Sprintf(" %d×%d", r.Output.Width, r.Output.Height)
	}
	if r.Output.Platform != "" {
		shape += " for " + r.Output.Platform
	}
	fmt.Printf("%s and %s; %s.\n", capitalize(plural(r.Clips, "clip")), plural(r.Overlays, "graphic"), strings.TrimSpace(shape))
	if len(o.Spec.Clips) > 0 {
		var clips []string
		for _, cl := range o.Spec.Clips {
			speed := cl.Speed
			if speed <= 0 {
				speed = 1
			}
			clips = append(clips, fmt.Sprintf("%s (%s)", cl.ID, seconds((cl.Out-cl.In)/speed)))
		}
		fmt.Println("Clips: " + strings.Join(clips, ", "))
	}
	if r.Approval.Note != "" {
		fmt.Println("Approval note: " + r.Approval.Note)
	}
	if len(r.Warnings) > 0 {
		fmt.Println("Worth checking:")
		printIssues(nil, r.Warnings, details)
	}
	for _, rd := range o.Renders {
		line := fmt.Sprintf("%s: %s", capitalize(renderWords(rd.Quality, rd.RevisionNumber)), renderStateWords(rd.State))
		if q := qaWords(rd.QA); q != "" {
			line += ", " + q
		}
		fmt.Println(line + ".")
		if details {
			fmt.Println("  render:", rd.ID)
		}
	}
	fmt.Println(r.URL)
	if details {
		fmt.Printf("version: %s · project %s · branch %s · spec %s\n", r.ID, r.ProjectID, r.Branch, short8(r.SpecHash))
		if r.ParentID != "" {
			fmt.Println("made from:", r.ParentID)
		}
	}
}

// printFramePlan sums up a version's frame-by-frame plan; details prints all
// of it (the same document as --json's data).
func printFramePlan(p *editspec.RenderPlan, details bool) {
	secondsLong := 0.0
	if p.FPS > 0 {
		secondsLong = float64(p.DurationInFrames) / float64(p.FPS)
	}
	fmt.Printf("The frame-by-frame plan: %d frames at %d a second (%s), %d×%d.\n", p.DurationInFrames, p.FPS, seconds(secondsLong), p.Width, p.Height)
	fmt.Printf("%s, %s and %s.\n", capitalize(plural(len(p.Clips), "clip")), plural(len(p.Audio), "sound track"), plural(len(p.Overlays), "graphic"))
	if !details {
		fmt.Println("(Add --details to see all of it.)")
		return
	}
	printJSON(p)
}

func renderCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "render", Short: "Render versions to video files"}
	cmd.AddCommand(renderFollowCmd()) // follow [on|off] (follow.go, D187)
	var in ops.RenderSubmitIn
	var wait, local bool
	submit := &cobra.Command{
		Use: "submit --revision <id>", Short: "Render a version (with --wait, wait until the video is ready)",
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			details := detailsMode(c)
			b, _ := json.Marshal(in)
			out, err := ops.Call(c.Context(), a, "render.submit", b, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			res := out.(*ops.RenderOut)
			label := capitalize(renderWords(res.Render.Quality, res.Render.RevisionNumber))
			if !wait {
				return emit(c, res, func() {
					switch {
					case res.Render.State == models.RenderAvailable:
						fmt.Printf("%s is already there.\n", label)
					case !res.Created:
						fmt.Printf("%s is already on its way; it isn't done yet.\n", label)
					default:
						fmt.Printf("%s is queued; it isn't done yet.\n", label)
					}
					fmt.Println(res.URL)
					if res.Note != "" {
						fmt.Println(res.Note)
					}
					if details {
						fmt.Printf("render %s · job %s\n", res.Render.ID, res.Render.JobID)
					}
				})
			}
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			var here func() jobRunner // this terminal renders it (the wait starts and watches the worker)
			if local || res.Workers == 0 {
				if res.Job != nil && res.Job.State == models.JobQueued {
					switch {
					case jsonMode(c):
					case details:
						fmt.Fprintln(os.Stderr, "rendering in this terminal…")
					default:
						fmt.Fprintln(os.Stderr, "Rendering it here, in this terminal…")
					}
					if err := setUpRendererIfNew(c, a, false); err != nil {
						return failErr(c, err)
					}
					if !newWorker(a).Can("render") {
						if err := worker.RenderReady(a); err != nil {
							return failErr(c, err)
						}
						return failErr(c, apperr.New(apperr.ToolMissing, "This machine can't render.").WithHint(checkupHint).WithCommand("metarush doctor"))
					}
					here = func() jobRunner {
						w := newWorker(a)
						w.OnlyJobID = res.Render.JobID
						// The worker's own log lines (job IDs, timings) are technical detail.
						w.Log = func(f string, x ...any) {
							if !jsonMode(c) && details {
								fmt.Fprintf(os.Stderr, "  "+f+"\n", x...)
							}
						}
						return w
					}
				}
			}
			if _, err := waitJobsOpt(c, a, []string{res.Render.JobID}, 0, false, here); err != nil {
				return failErr(c, err)
			}
			b2, _ := json.Marshal(ops.RenderIDIn{ID: res.Render.ID})
			st, err := ops.Call(ctx, a, "render.status", b2, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			final := st.(*ops.RenderOut)
			if final.Render.State != models.RenderAvailable {
				return failErr(c, renderFailure(final))
			}
			return emit(c, final, func() {
				o := final.Render.Output
				fmt.Printf("%s is ready (%s, %s).\n", label, seconds(o.Duration), fmtBytes(o.Size))
				fmt.Println(final.URL)
				if !in.NoQA {
					fmt.Println("A quality check runs next; it also listens to the speech.")
				}
				if details {
					fmt.Printf("render %s · %dx%d, %.2fs\n", final.Render.ID, o.Width, o.Height, o.Duration)
					fmt.Println("local file: " + commandLine(c, "metarush render fetch", final.Render.ID) + " --out <folder>")
					if !in.NoQA {
						fmt.Println("when it is done: " + commandLine(c, "metarush qa show", final.Render.ID))
					}
				}
			})
		},
	}
	submit.Flags().StringVar(&in.RevisionID, "revision", "", "the version (its ID)")
	submit.Flags().StringVar(&in.Preset, "preset", "", "vertical-1080p | landscape-1080p | square-1080 (default: the version's)")
	submit.Flags().StringVar(&in.Quality, "quality", "final", "review (fast, half size) | final")
	submit.Flags().BoolVar(&wait, "wait", false, "wait until the video is ready (rendered here if no other machine can)")
	submit.Flags().BoolVar(&local, "local", false, "with --wait: always render here, in this terminal")
	submit.Flags().BoolVar(&in.NoQA, "no-qa", false, "skip the automatic quality check of the finished render")
	cmd.AddCommand(submit)
	cmd.AddCommand(&cobra.Command{
		Use: "status <render-id>", Short: "How a render is doing", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "render.status", ops.RenderIDIn{ID: args[0]}, func(o any) { printRenderStatus(o.(*ops.RenderOut), detailsMode(c)) })
		},
	})
	var rl ops.RenderListIn
	list := &cobra.Command{
		Use: "list [project-id]", Short: "List renders (newest first)", Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				rl.ProjectID = args[0]
			}
			return runOp(c, "render.list", rl, func(o any) {
				renders := o.(*ops.RenderListOut).Renders
				if len(renders) == 0 {
					fmt.Println("No renders yet.")
				}
				for _, r := range renders {
					printRenderRow(r, detailsMode(c))
				}
			})
		},
	}
	list.Flags().StringVar(&rl.ProjectID, "project", "", "project ID")
	list.Flags().StringVar(&rl.RevisionID, "revision", "", "only this version (its ID)")
	cmd.AddCommand(list)
	cmd.AddCommand(&cobra.Command{
		Use: "cancel <render-id>", Short: "Stop a render", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "render.cancel", ops.RenderIDIn{ID: args[0]}, func(o any) {
				r := o.(*ops.RenderOut)
				label := capitalize(renderWords(r.Render.Quality, r.Render.RevisionNumber))
				switch {
				case r.Render.State == models.RenderCanceled:
					fmt.Printf("%s is stopped.\n", label)
				case r.Job != nil && (r.Job.State == models.JobSucceeded || r.Job.State == models.JobFailed):
					fmt.Printf("%s had already finished; nothing to stop.\n", label)
				default:
					fmt.Printf("%s stops in a moment.\n", label)
				}
				if detailsMode(c) {
					fmt.Println("render:", r.Render.ID)
				}
			})
		},
	})
	var outPath string
	fetch := &cobra.Command{
		Use: "fetch <render-id>", Short: "Save a finished render as a video file (checked against the original)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "render.fetch", ops.RenderFetchIn{ID: args[0], Out: outPath}, func(o any) {
				r := o.(*ops.RenderFetchOut)
				fmt.Printf("Saved it as %s (%s).\n", baseName(r.Path), fmtBytes(r.Size))
				if detailsMode(c) {
					fmt.Println("file:", r.Path)
					fmt.Println("sha256:", r.SHA256)
				}
			})
		},
	}
	fetch.Flags().StringVar(&outPath, "out", "", "the file or folder to save it in")
	cmd.AddCommand(fetch)
	cmd.AddCommand(&cobra.Command{
		Use: "open <render-id>", Short: "Open a render in the dashboard", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			cmdD := dashboardCmd()
			cmdD.SetArgs([]string{"--next", "/renders/" + args[0]})
			cmdD.SetContext(c.Context())
			return cmdD.Execute()
		},
	})
	return cmd
}

func jobCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "job", Short: "What MetaRush is working on, job by job: look at, stop, retry or start them now"}
	var in ops.JobListIn
	list := &cobra.Command{
		Use: "list", Short: "List jobs (newest first)",
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "job.list", in, ops.Caller{}, func(o any) {
				details := detailsMode(c)
				names := newJobWords(a)
				js := o.(*ops.JobListOut).Jobs
				if len(js) == 0 {
					fmt.Println("No jobs.")
				}
				for i := range js {
					j := &js[i]
					line := fmt.Sprintf("%-46s %-16s %s", trunc(names.doing(c.Context(), j), 46), orWord(jobStateWords[j.State], j.State), plainWhen(j.CreatedAt))
					if j.State == models.JobRunning {
						line += fmt.Sprintf(" · %.0f%%", j.Progress.Pct*100)
						if j.Lease != nil && j.Lease.OwnerName != "" {
							line += " on " + j.Lease.OwnerName
						}
					}
					fmt.Println(line)
					if details {
						fmt.Printf("    %s %s %s\n", j.ID, j.Type, trunc(j.Error, 120))
					}
				}
			})
		},
	}
	list.Flags().StringVar(&in.State, "state", "", "only jobs in this state: queued, running, succeeded, failed or canceled")
	list.Flags().StringVar(&in.ProjectID, "project", "", "only this project's")
	list.Flags().StringVar(&in.AssetID, "asset", "", "only this clip's")
	list.Flags().StringVar(&in.Type, "type", "", "only this kind (transcribe, render, qa…)")
	cmd.AddCommand(list)
	var showDetails bool
	show := &cobra.Command{Use: "show <job-id>", Short: "Explain a job: what it is, who started it, why it waits, what it made and where that is", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		return runOp(c, "job.show", ops.JobIDIn{ID: args[0]}, func(o any) { printJob(o.(*ops.JobShowOut), showDetails) })
	}}
	addDetails(show, &showDetails)
	cmd.AddCommand(show)
	for _, v := range []struct{ name, op, short string }{{"cancel", "job.cancel", "Stop a job"}, {"retry", "job.retry", "Try a job that didn't finish again"}} {
		v := v
		cmd.AddCommand(&cobra.Command{Use: v.name + " <job-id>", Short: v.short, Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, v.op, ops.JobIDIn{ID: args[0]}, func(o any) {
				j := o.(*models.Job)
				kind := jobKindName(j.Type)
				switch {
				case j.State == models.JobCanceled:
					fmt.Printf("%s is stopped.\n", kind)
				case v.name == "cancel" && j.CancelRequested:
					fmt.Printf("%s stops in a moment.\n", kind)
				case j.State == models.JobQueued:
					fmt.Printf("%s will run again.\n", kind)
				default:
					fmt.Printf("%s is %s.\n", kind, orWord(jobStateWords[j.State], j.State))
				}
				if detailsMode(c) {
					fmt.Printf("job %s (%s): %s\n", j.ID, j.Type, j.State)
				}
			})
		}})
	}
	cmd.AddCommand(&cobra.Command{
		Use: "run-now <job-id>...", Aliases: []string{"expedite"}, Short: "Start waiting jobs now: even big ones a machine holds back on battery, and without waiting to try again", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "job.expedite", ops.JobExpediteIn{IDs: args}, func(o any) {
				out := o.(*ops.JobExpediteOut)
				if n := len(out.Expedited); n > 0 {
					fmt.Printf("%s will start now.\n", capitalize(plural(n, "job")))
				}
				switch n := len(out.Skipped); n {
				case 0:
				case 1:
					fmt.Println("1 was already running or finished, so it was left alone.")
				default:
					fmt.Printf("%d were already running or finished, so they were left alone.\n", n)
				}
				if out.Note != "" {
					fmt.Println(out.Note)
				}
				if detailsMode(c) {
					for _, id := range out.Expedited {
						fmt.Printf("  %s will run now\n", id)
					}
					for _, id := range out.Skipped {
						fmt.Printf("  %s left alone\n", id)
					}
				}
			})
		},
	})
	var timeout time.Duration
	wait := &cobra.Command{
		Use: "wait <job-id>...", Short: "Wait for jobs to finish (they run here if no other machine can)", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			js, err := waitJobs(c, a, args, timeout, false)
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, js, func() {
				names := newJobWords(a)
				for i := range js {
					fmt.Println(names.outcome(c.Context(), &js[i]))
					if detailsMode(c) {
						fmt.Printf("    %s %s %s %s\n", js[i].ID, js[i].Type, js[i].State, js[i].Error)
					}
				}
			})
		},
	}
	wait.Flags().DurationVar(&timeout, "timeout", 0, "give up after this long")
	cmd.AddCommand(wait)
	return cmd
}

func commentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "comment", Short: "Review comments"}
	var in ops.CommentAddIn
	var at float64
	add := &cobra.Command{
		Use: "add --project <id> <text>", Short: "Add a comment", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Body = strings.Join(args, " ")
			if c.Flags().Changed("at") {
				in.At = &at
			}
			return runOp(c, "comment.add", in, func(o any) {
				cm := o.(*models.Comment)
				if cm.At != nil {
					fmt.Printf("Comment added, at %s.\n", fmtDur(*cm.At))
				} else {
					fmt.Println("Comment added.")
				}
				if detailsMode(c) {
					fmt.Println("comment:", cm.ID)
				}
			})
		},
	}
	add.Flags().StringVar(&in.ProjectID, "project", "", "project ID")
	add.Flags().StringVar(&in.RevisionID, "revision", "", "the version it is about (its ID)")
	add.Flags().StringVar(&in.AssetID, "asset", "", "the clip it is about (its ID)")
	add.Flags().Float64Var(&at, "at", 0, "time in seconds")
	cmd.AddCommand(add)
	var li ops.CommentListIn
	list := &cobra.Command{
		Use: "list --project <id>", Short: "List comments",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "comment.list", li, func(o any) {
				comments := o.(*ops.CommentListOut).Comments
				if len(comments) == 0 {
					fmt.Println("No comments yet.")
				}
				for _, cm := range comments {
					at := ""
					if cm.At != nil {
						at = "at " + fmtDur(*cm.At) + ": "
					}
					resolved := ""
					if cm.Resolved {
						resolved = " (resolved)"
					}
					body := cm.Body
					if cm.Outside != nil && cm.Quoted == "" {
						body = theirs(body) // someone outside the workspace wrote it (D315)
					}
					fmt.Printf("%s  %s: %s%s%s\n", cm.CreatedAt.Local().Format("2 Jan 15:04"), orWord(cm.Author.UserName, "someone"), at, body, resolved)
					if cm.Quoted != "" {
						fmt.Println("    (" + cm.Quoted + ")")
					}
					if detailsMode(c) {
						fmt.Printf("    %s %s %s\n", cm.ID, cm.RevisionID, cm.AssetID)
					}
				}
			})
		},
	}
	list.Flags().StringVar(&li.ProjectID, "project", "", "project ID")
	list.Flags().StringVar(&li.RevisionID, "revision", "", "only comments on this version (its ID)")
	cmd.AddCommand(list)
	return cmd
}

func opsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "ops", Short: "Low-level access to the operation registry (same ops the dashboard uses)"}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List operations",
		RunE: func(c *cobra.Command, _ []string) error {
			type row struct {
				Name      string `json:"name"`
				Summary   string `json:"summary"`
				Mutating  bool   `json:"mutating"`
				HumanOnly bool   `json:"humanOnly"`
				LocalOnly bool   `json:"localOnly"`
			}
			var rows []row
			for _, op := range ops.All() {
				rows = append(rows, row{op.Name, op.Summary, op.Mutating, op.HumanOnly, op.LocalOnly})
			}
			return emit(c, rows, func() {
				for _, r := range rows {
					flags := ""
					if r.Mutating {
						flags += " mutating"
					}
					if r.HumanOnly {
						flags += " human-only"
					}
					fmt.Printf("%-20s %s%s\n", r.Name, r.Summary, flags)
				}
			})
		},
	})
	var input string
	call := &cobra.Command{
		Use: "call <name> --input '<json>'", Short: "Call an operation with JSON input", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			raw := json.RawMessage(input)
			if input == "-" {
				raw, err = readPlan("-")
				if err != nil {
					return err
				}
			}
			out, err := ops.Call(c.Context(), a, args[0], raw, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() { printJSON(out) })
		},
	}
	call.Flags().StringVar(&input, "input", "{}", "JSON input (or - for stdin)")
	cmd.AddCommand(call, opsFindCmd(), opsDescribeCmd())
	return cmd
}

func workspaceCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "workspace", Short: "The workspace: where its footage is kept, the machines in it, exporting and restoring it"}
	cmd.AddCommand(workspaceArchiveCmds()...)
	cmd.AddCommand(&cobra.Command{
		Use: "show", Short: "Show the workspace this machine is in",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "workspace.show", ops.WorkspaceShowIn{}, func(o any) { printWorkspace(o.(*ops.WorkspaceShowOut), detailsMode(c)) })
		},
	})
	return cmd
}

// osWords names an operating system.
func osWords(goos string) string {
	switch goos {
	case "darwin":
		return "Mac"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	}
	return goos
}

func printWorkspace(w *ops.WorkspaceShowOut, details bool) {
	fmt.Printf("Workspace “%s”\n", w.Workspace.Name)
	where := "cloud storage"
	if strings.HasPrefix(w.MediaStore, "fs:") {
		where = "a folder on this machine"
	}
	shared := "only this machine uses it"
	if w.MediaShared {
		shared = "every machine in the workspace uses it"
	}
	fmt.Printf("Footage is kept in MetaRush's storage: %s (%s). This machine keeps its own copy of what it uses.\n", where, shared)
	fmt.Println("Dashboard: " + w.Dashboard)
	if len(w.Installations) > 0 {
		fmt.Println("Machines")
	}
	for _, i := range w.Installations {
		line := "  " + i.Name
		if i.ID == w.Installation.ID {
			line += " (this machine)"
		}
		// The release in plain words (0.2.0); the exact build is technical (D130).
		line += fmt.Sprintf(" · %s · MetaRush %s · seen %s", osWords(i.OS), version.Describe(i.Version, i.Commit, i.Built).Release, plainWhen(i.LastSeenAt))
		switch i.State {
		case "revoked":
			line += " · no longer allowed in"
		case "invited":
			line += " · invited, not joined yet"
		}
		fmt.Println(line)
		if details {
			fmt.Printf("      %s %s/%s %s · MetaRush %s, commit %s\n", i.ID, i.OS, i.Arch, i.Hostname, i.Version, orText(i.Commit, "-"))
		}
	}
	if details {
		fmt.Printf("workspace %s\ndatabase %s\nmedia %s (shared: %v)\ncache %s\n", w.Workspace.ID, w.Database, w.MediaStore, w.MediaShared, w.CacheDir)
	}
}

// projectFrom accepts a project ID positionally or via --project (never both differently).
func projectFrom(c *cobra.Command, args []string, dst *string) error {
	if len(args) == 1 {
		if *dst != "" && *dst != args[0] {
			return fail(c, apperr.InvalidInput, "Two different projects were given: "+*dst+" and "+args[0]+".", "Give the project once, as an argument or with --project.")
		}
		*dst = args[0]
	}
	if *dst == "" {
		return failErr(c, apperr.New(apperr.InvalidInput, "Say which project: its name or ID.").
			WithHint("Give it as an argument or with --project.").WithCommand("metarush project list"))
	}
	return nil
}

// transcriptAliasCmd lets `metarush transcript [show] <asset>` work — a form Claude reaches for naturally.
func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// Plain words for the values the printers show (skills/_shared/voice.md).

// viaWords says where something was done from.
func viaWords(via string) string {
	switch via {
	case "dashboard":
		return "in the dashboard"
	case "cli":
		return "from the command line or Claude Code"
	case models.ViaAssistant:
		return "by Claude Code in the dashboard"
	case models.ViaAssistantAuto:
		return "by Claude Code in the dashboard, by itself (your setting for safe steps)"
	case "mcp":
		return "from an app connected through MCP"
	case "watch":
		return "by the watched folder"
	case "worker":
		return "automatically"
	case "extension":
		return "from the browser extension"
	case "":
		return "somewhere"
	}
	return "via " + via
}

func approvalWords(state string) string {
	switch state {
	case models.ApprovalApproved:
		return "approved"
	case models.ApprovalRejected:
		return "needs changes" // D188: Draft, Approved, Needs changes everywhere
	case "", models.ApprovalDraft:
		return "draft"
	}
	return state
}

// kindWords names a kind of footage.
func kindWords(kind string) string {
	switch kind {
	case "image":
		return "picture"
	case "":
		return "file"
	}
	return kind
}

// presetWords names an output shape ("vertical-1080p" → "vertical").
func presetWords(preset string) string {
	switch preset {
	case "vertical-1080p":
		return "vertical"
	case "landscape-1080p":
		return "landscape"
	case "square-1080":
		return "square"
	}
	return preset
}

func renderStateWords(state string) string {
	switch state {
	case models.RenderQueued:
		return "waiting to start"
	case models.RenderRendering:
		return "rendering"
	case models.RenderUploading:
		return "being saved"
	case models.RenderAvailable:
		return "ready"
	case models.RenderFailed:
		return "didn't finish"
	case models.RenderCanceled:
		return "stopped"
	}
	return state
}

// qaWords is a render's quality check in a few words, the way the dashboard's
// chip says it ("" when it has none).
func qaWords(q *models.RenderQA) string {
	switch {
	case q == nil:
		return ""
	case q.Errors > 0:
		return "quality check: " + plural(q.Errors, "problem") + " found"
	case q.Warnings > 0:
		return fmt.Sprintf("quality check: %d to check", q.Warnings)
	}
	return "quality check passed"
}

// assetLine is one clip in a list: its title, file name, kind, length and how
// far along it is.
func assetLine(as ops.AssetSummary) string {
	line := fmt.Sprintf("%-28s %-28s %-7s %8s", theirsCol(as.Title, 28), theirsCol(as.Name, 28), kindWords(as.Kind), seconds(as.Duration))
	switch as.State {
	case models.AssetAvailable:
	case models.AssetFailed:
		line += "  couldn't be added"
	default:
		line += "  still being added"
	}
	if as.From != "" {
		line += "  from " + as.From
	}
	return line
}

// transcriptWords says where a clip's transcript stands ("" for a clip
// without sound).
func transcriptWords(t *models.TranscriptStatus, hasAudio bool) string {
	switch {
	case t == nil && hasAudio:
		return "no transcript yet"
	case t == nil:
		return ""
	case t.State == "available" && t.Words == 0 && t.Segments == 0:
		return "no speech found"
	case t.State == "available":
		return "transcript ready"
	case t.State == "pending":
		return "transcribing"
	case t.State == "failed":
		return "the transcript didn't finish"
	}
	return "no transcript"
}

// fps is a frame rate without needless digits ("30", "29.97").
func fps(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
}

// mediaWords describes a file's picture and sound: "Video, 27.0 s, 1920×1080
// at 30 frames a second, with sound."
func mediaWords(kind string, m models.MediaInfo) string {
	parts := []string{capitalize(kindWords(kind))}
	if m.Duration > 0 {
		parts = append(parts, seconds(m.Duration))
	}
	if m.Width > 0 && m.Height > 0 {
		pic := fmt.Sprintf("%d×%d", m.Width, m.Height)
		if m.FPS > 0 && kind == "video" {
			pic += " at " + fps(m.FPS) + " frames a second"
		}
		parts = append(parts, pic)
	}
	switch {
	case kind == "video" && m.HasAudio:
		parts = append(parts, "with sound")
	case kind == "video":
		parts = append(parts, "no sound")
	}
	return strings.Join(parts, ", ") + "."
}

func printAssetShow(o *ops.AssetShowOut, details bool) {
	as := o.Asset
	title := as.DisplayTitle()
	if as.Name != "" && as.Name != title {
		fmt.Printf("%s (%s)\n", theirs(title), theirs(as.Name))
	} else {
		fmt.Println(theirs(title))
	}
	fmt.Printf("%s, %s.\n", strings.TrimSuffix(mediaWords(as.Kind, as.Media), "."), fmtBytes(as.Size))
	switch as.State {
	case models.AssetAvailable:
	case models.AssetFailed:
		fmt.Println("It couldn't be added.")
	default:
		fmt.Println("It is still being added.")
	}
	if as.Media.VFR {
		fmt.Println("It changes its frame rate while it plays, so MetaRush plays a steady copy of it.")
	}
	if as.Media.HDR {
		fmt.Println("It is HDR (very bright, rich colour).")
	}
	if t := as.Transcript; t != nil && t.State == "available" && t.Words > 0 {
		line := fmt.Sprintf("Transcript: %s", plural(t.Words, "word"))
		if t.Speakers > 1 {
			line += fmt.Sprintf(", %d speakers", t.Speakers)
		}
		fmt.Println(line + ".")
	} else if w := transcriptWords(as.Transcript, as.Media.HasAudio); w != "" {
		fmt.Println("Transcript: " + w + ".")
	}
	if sp := as.Speakers; sp != nil && sp.State == "available" && sp.Count > 0 {
		fmt.Printf("Voices: %d, %d named.\n", sp.Count, sp.Named)
	}
	if as.Kind == "video" {
		if _, ok := as.Variants[models.VariantProxy]; ok {
			fmt.Println("Preview copy: ready.")
		} else {
			fmt.Println("Preview copy: not made yet.")
		}
	}
	if s := as.Source; s != nil {
		from := []string{"From " + models.PlatformName(s.Platform)}
		if who := orWord(s.Handle, s.Uploader); who != "" {
			from = append(from, who)
		}
		if s.UploadDate != "" {
			from = append(from, "posted "+s.UploadDate)
		}
		fmt.Println(strings.Join(from, " · ") + ".")
		if s.Title != "" && s.Title != title {
			fmt.Printf("Original title: “%s”\n", s.Title)
		}
		if s.License != "" {
			fmt.Println("Licence the site states: " + s.License)
		}
		if s.URL != "" {
			fmt.Println(s.URL)
		}
	}
	if r := as.Rights; r != nil && r.License != "" {
		line := "Rights: " + r.License
		if r.Attribution != "" {
			line += " (credit: " + r.Attribution + ")"
		}
		fmt.Println(line)
	}
	if len(as.Tags) > 0 {
		fmt.Println("Tags: " + strings.Join(as.Tags, ", "))
	}
	if len(as.Aliases) > 0 {
		var names []string
		for _, al := range as.Aliases {
			names = append(names, al.Name)
		}
		fmt.Println("Also called: " + strings.Join(names, ", "))
	}
	switch {
	case as.Trash != nil:
		fmt.Println("It is in the Trash.")
	case as.ArchivedAt != nil:
		fmt.Println("It is in the Archive.")
	}
	running, failed := 0, []string{}
	for _, j := range o.Jobs {
		switch j.State {
		case models.JobQueued, models.JobRunning:
			running++
		case models.JobFailed:
			failed = append(failed, strings.ToLower(ops.JobKindLabel(j.Type)))
		}
	}
	if running > 0 {
		fmt.Printf("MetaRush is still preparing %s for it.\n", plural(running, "thing"))
	}
	if len(failed) > 0 {
		fmt.Printf("Didn't finish: %s. Retry from Activity in the dashboard.\n", strings.Join(failed, ", "))
	}
	if o.Cached {
		fmt.Println("This machine has its own copy.")
	}
	if details {
		fmt.Printf("clip %s · %s · sha256 %s\n", as.ID, as.MIME, as.SHA256)
		fmt.Printf("stored as %s · state %s", as.StorageKey, as.State)
		if as.StateReason != "" {
			fmt.Printf(" (%s)", as.StateReason)
		}
		fmt.Println()
		for _, st := range as.Media.Streams {
			fmt.Printf("  stream %d: %s %s\n", st.Index, st.Type, st.Codec)
		}
		var vs []string
		for k := range as.Variants {
			vs = append(vs, k)
		}
		sort.Strings(vs)
		fmt.Println("  made:", strings.Join(vs, ", "))
		for _, j := range o.Jobs {
			fmt.Printf("  job %s %-13s %s %s\n", j.ID, j.Type, j.State, trunc(j.Error, 120))
		}
	}
}

// printTranscript prints what was said, line by line with times and (when
// more than one person speaks) who.
func printTranscript(t *ops.TranscriptOut, details bool) {
	if len(t.Segments) == 0 {
		fmt.Println(t.Note)
		return
	}
	names := map[string]string{}
	for _, v := range t.Speakers {
		names[v.Label] = v.DisplayName
	}
	for _, s := range t.Segments {
		sp := ""
		if s.Speaker != "" && len(t.Speakers) > 1 {
			sp = names[s.Speaker] + ": "
			if s.SpeakerName != "" {
				sp = s.SpeakerName + ": "
			}
		}
		fmt.Printf("[%s–%s] %s%s\n", fmtDur(s.From), fmtDur(s.To), sp, s.Text)
	}
	if details && t.Note != "" {
		fmt.Println("note:", t.Note)
	}
}

// joinWords lists items the way people write them: "a, b and c".
func joinWords(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// aroundClock is a moment as people say it: "0:27", "1:02:05".
func aroundClock(t float64) string {
	s := int(math.Round(t))
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// evidenceSpan is one measured stretch of a clip.
type evidenceSpan struct {
	From     float64 `bson:"from"`
	To       float64 `bson:"to"`
	At       float64 `bson:"at"`
	Duration float64 `bson:"duration"`
	Kind     string  `bson:"kind"`
}

// technicalEvidence is what the evidence job measured (the "technical" insight).
type technicalEvidence struct {
	Silences      []evidenceSpan `bson:"silences"`
	Scenes        []evidenceSpan `bson:"scenes"`
	Black         []evidenceSpan `bson:"black"`
	Freezes       []evidenceSpan `bson:"freezes"`
	VisualChanges []evidenceSpan `bson:"visualChanges"`
	Loudness      *struct {
		IntegratedLUFS *float64 `bson:"integratedLufs"`
		RangeLU        *float64 `bson:"rangeLu"`
		TruePeak       *float64 `bson:"truePeakDbfs"`
	} `bson:"loudness"`
	Notes []string `bson:"notes"`
}

// insightAs reads an insight's data into v (a BSON round trip: nested values
// come back from the database as documents and arrays of any kind).
func insightAs(in models.Insight, v any) bool {
	b, err := bson.Marshal(in.Data)
	return err == nil && bson.Unmarshal(b, v) == nil
}

// printEvidence says what MetaRush measured in a clip, in a few lines; details
// adds every measurement (the full document is --json).
func printEvidence(e *ops.EvidenceOut, details bool) {
	kind := "audio"
	if e.Media.HasVideo {
		kind = "video"
	}
	name := "“" + orWord(e.Title, e.Name) + "”"
	if e.Name != "" && e.Name != e.Title {
		name += " (" + e.Name + ")"
	}
	fmt.Printf("%s: %s\n", name, mediaWords(kind, e.Media))
	var tech *technicalEvidence
	var onScreen string
	for _, in := range e.Insights {
		switch in.Kind {
		case "technical":
			var t technicalEvidence
			if insightAs(in, &t) {
				tech = &t
			}
		case "visual":
			var v struct {
				Summary string `bson:"summary"`
			}
			if insightAs(in, &v) {
				onScreen = v.Summary
			}
		}
	}
	if tech == nil {
		fmt.Println("It hasn't been measured yet.")
	} else {
		switch n := len(tech.Silences); n {
		case 0:
			fmt.Println("No pauses.")
		default:
			longest := tech.Silences[0]
			for _, s := range tech.Silences {
				if s.To-s.From > longest.To-longest.From {
					longest = s
				}
			}
			fmt.Printf("%s, the longest %s at %s.\n", capitalize(plural(n, "pause")), seconds(longest.To-longest.From), aroundClock(longest.From))
		}
		cuts, slow, moves := 0, 0, 0
		for _, v := range tech.VisualChanges {
			switch v.Kind {
			case "cut":
				cuts++
			case "gradual":
				slow++
			default:
				moves++
			}
		}
		if len(tech.VisualChanges) == 0 {
			cuts = len(tech.Scenes)
		}
		var changes []string
		if cuts > 0 {
			changes = append(changes, plural(cuts, "cut"))
		}
		if slow > 0 {
			changes = append(changes, plural(slow, "slow change")+" (a pan, zoom, light or someone coming into view)")
		}
		if moves > 0 {
			changes = append(changes, plural(moves, "burst")+" of movement")
		}
		if len(changes) == 0 {
			fmt.Println("The picture doesn't cut or change much.")
		} else {
			fmt.Printf("In the picture: %s.\n", joinWords(changes))
		}
		var bad []string
		for _, b := range tech.Black {
			bad = append(bad, "black at "+aroundClock(b.From))
		}
		for _, f := range tech.Freezes {
			bad = append(bad, "frozen at "+aroundClock(f.From))
		}
		if len(bad) > 0 {
			fmt.Printf("Picture problems: %s.\n", joinWords(bad))
		} else if e.Media.HasVideo {
			fmt.Println("No black or frozen picture.")
		}
	}
	if onScreen != "" {
		fmt.Printf("On screen: %s.\n", strings.TrimSuffix(onScreen, "."))
	}
	if !details {
		return
	}
	fmt.Printf("clip %s\n", e.AssetID)
	var states []string
	for k, v := range e.Analysis {
		states = append(states, k+" "+v)
	}
	sort.Strings(states)
	fmt.Println("analysis:", strings.Join(states, ", "))
	if tech != nil {
		if l := tech.Loudness; l != nil && l.IntegratedLUFS != nil {
			line := fmt.Sprintf("loudness: %.1f LUFS", *l.IntegratedLUFS)
			if l.RangeLU != nil {
				line += fmt.Sprintf(", range %.1f LU", *l.RangeLU)
			}
			if l.TruePeak != nil {
				line += fmt.Sprintf(", peak %.1f dBFS", *l.TruePeak)
			}
			fmt.Println(line)
		}
		for _, s := range tech.Silences {
			fmt.Printf("  pause %s–%s (%.2fs)\n", fmtDur(s.From), fmtDur(s.To), s.To-s.From)
		}
		for _, v := range tech.VisualChanges {
			fmt.Printf("  picture %s %s–%s\n", v.Kind, fmtDur(v.From), fmtDur(v.To))
		}
		for _, n := range tech.Notes {
			fmt.Println("  note: " + n)
		}
	}
	if e.Note != "" {
		fmt.Println("note:", e.Note)
	}
}

// momentKindWords name a kind of moment.
var momentKindWords = map[string]string{"speech": "said", "pause": "pause", "visual": "picture", "scene": "new scene", "black": "black", "freeze": "frozen"}

// dropLeadingID leaves out an ID at the start of a line ("ast_… (Title):
// no transcript" → "Title: no transcript").
func dropLeadingID(s string) string {
	id, rest, ok := strings.Cut(s, " ")
	if !ok || !strings.Contains(id, "_") || len(id) < 20 {
		return s
	}
	if t, after, ok := strings.Cut(strings.TrimPrefix(rest, "("), "):"); ok && strings.HasPrefix(rest, "(") {
		return t + ":" + after
	}
	return rest
}

// printMoments lists the moments worth a look, one a line: the clip, what
// kind, when, and the words or what changes.
func printMoments(m *ops.MomentsOut, details bool) {
	if len(m.Moments) == 0 && len(m.Missing) == 0 {
		fmt.Println("Nothing found.")
	}
	for _, x := range m.Moments {
		what := orWord(momentKindWords[x.Kind], x.Kind)
		text := x.Detail
		if x.Text != "" {
			text = "“" + trunc(x.Text, 70) + "”"
			if x.Detail != "" { // who said it
				text = x.Detail + ": " + text
			}
		}
		fmt.Printf("%-24s %-9s %s–%s  %s\n", theirsCol(x.Asset, 24), what, fmtDur(x.From), fmtDur(x.To), text)
		if details {
			fmt.Printf("    %s  %s", x.AssetID, x.Kind)
			if len(x.Frames) > 0 {
				var fs []string
				for _, f := range x.Frames {
					fs = append(fs, strconv.FormatFloat(f, 'f', -1, 64))
				}
				fmt.Printf("  frames at %s", strings.Join(fs, ","))
			}
			fmt.Println()
		}
	}
	for _, s := range m.Missing {
		if details {
			fmt.Println("missing:", s)
		} else {
			fmt.Println("Not ready yet: " + dropLeadingID(s))
		}
	}
}

// printAnalyze says what preparing footage again started (or, after
// waiting, how it went).
func printAnalyze(res *ops.AnalyzeOut, waited []models.Job, details bool) {
	switch {
	case waited == nil && len(res.Jobs) == 0:
		fmt.Println("Nothing new to do.")
	case waited == nil:
		fmt.Printf("Started %s; the activity bar shows how they go.\n", plural(len(res.Jobs), "job"))
	default:
		unfinished := 0
		for _, j := range waited {
			if j.State != models.JobSucceeded {
				unfinished++
			}
		}
		if unfinished == 0 {
			fmt.Printf("Done: %s finished.\n", plural(len(waited), "job"))
		} else {
			fmt.Printf("%d of %s didn't finish: Activity in the dashboard shows which, and can try again.\n", unfinished, plural(len(waited), "job"))
		}
	}
	if n := len(res.Skipped); n > 0 {
		fmt.Printf("%s already done, so left alone.\n", capitalize(plural(n, "job")))
	}
	if waited == nil && res.Note != "" {
		fmt.Println(res.Note)
	}
	if details {
		for _, id := range res.Jobs {
			fmt.Println("  job " + id)
		}
		for _, s := range res.Skipped {
			fmt.Println("  skipped: " + s)
		}
	}
}

// uploadStateWords say how an upload stands.
func uploadStateWords(state string) string {
	switch state {
	case "hashing":
		return "checking"
	case "uploading":
		return "uploading"
	case "verifying":
		return "checking"
	case "available":
		return "finished"
	case "deduped":
		return "already there"
	case "failed":
		return "stopped"
	}
	return strings.ReplaceAll(state, "_", " ")
}

// ingestPhaseWords name the steps of adding a file, for the progress line.
var ingestPhaseWords = map[string]string{"probe": "reading", "hash": "checking", "upload": "storing", "cache": "copying", "done": "added"}

// ingestLine says what happened to one file that was added.
func ingestLine(r ingest.Result) string {
	named := "“" + orWord(r.Title, r.Name) + "”"
	if r.Title != "" && r.Name != "" && r.Name != r.Title {
		named += " (" + r.Name + ")"
	}
	switch {
	case r.Resource != nil && r.Resource.Message != "":
		return r.Resource.Message
	case r.Duplicate != nil:
		return r.Name + ": " + describeDuplicate(r.Duplicate)
	}
	switch r.Status {
	case "ingested":
		return "Added " + named
	case "resumed":
		return "Added " + named + ", picking up where an earlier upload stopped"
	case "would-ingest":
		return "Would add " + r.Name
	case "skipped":
		return "Skipped " + r.Name + ": " + r.Error
	case "failed":
		return "Couldn't add " + r.Name + ": " + r.Error
	}
	return r.Name + ": " + r.Status
}

func printIngest(res *ingest.FilesOut, waited []models.Job, didWait, details bool) {
	for _, r := range res.Results {
		fmt.Println(ingestLine(r))
		if details {
			fmt.Printf("    %s  %s", r.Status, orNone(r.AssetID))
			if r.SessionID != "" {
				fmt.Printf("  session %s", r.SessionID)
			}
			if len(r.JobIDs) > 0 {
				fmt.Printf("  jobs %s", strings.Join(r.JobIDs, " "))
			}
			fmt.Println()
		}
	}
	var parts []string
	count := func(n int, one, many string) {
		switch {
		case n == 1:
			parts = append(parts, "1 "+one)
		case n > 1:
			parts = append(parts, fmt.Sprintf("%d %s", n, many))
		}
	}
	count(res.Ingested, "added", "added")
	count(res.Resumed, "finished an earlier upload", "finished earlier uploads")
	count(res.Deduped, "was already in MetaRush", "were already in MetaRush")
	count(res.Duplicates, "identical to a clip you have, skipped", "identical to clips you have, skipped")
	count(res.Skipped, "skipped", "skipped")
	count(res.Failed, "couldn't be added", "couldn't be added")
	switch {
	case res.DryRun:
		fmt.Println("\nNothing was added: this only lists what would be.")
	case len(parts) > 0:
		fmt.Println("\n" + capitalize(strings.Join(parts, ", ")) + ".")
	}
	if details {
		fmt.Printf("counts: %d ingested, %d deduped, %d duplicates, %d resumed, %d skipped, %d failed\n", res.Ingested, res.Deduped, res.Duplicates, res.Resumed, res.Skipped, res.Failed)
	}
	if !didWait || len(waited) == 0 {
		if res.Note != "" {
			fmt.Println(res.Note)
		}
		return
	}
	unfinished := 0
	for _, j := range waited {
		if j.State != models.JobSucceeded {
			unfinished++
		}
	}
	if unfinished == 0 {
		fmt.Println("The preview copies, transcripts and the rest are ready.")
	} else {
		fmt.Printf("%s didn't finish: Activity in the dashboard shows which, and can try again.\n", capitalize(plural(unfinished, "step")))
	}
}

func printProjectShow(p *ops.ProjectShowOut, details bool) {
	fmt.Println(p.Project.Name)
	if p.Project.Archived {
		// Deleted (D196): in Archived projects until someone restores it or an owner clears it out.
		when := ""
		if p.Project.ArchivedAt != nil {
			when = " " + plainWhen(*p.Project.ArchivedAt)
		}
		if p.Project.ArchivedBy != nil && p.Project.ArchivedBy.UserName != "" {
			when += " by " + p.Project.ArchivedBy.UserName
		}
		fmt.Printf("Deleted%s: it is in Archived projects. Restore it to work on it again.\n", when)
		if details {
			fmt.Println("metarush project restore " + p.Project.ID)
		}
	}
	var head *ops.RevisionSummary
	for i := range p.Revisions {
		if p.Revisions[i].IsHead {
			head = &p.Revisions[i]
		}
	}
	switch {
	case head != nil:
		fmt.Printf("Latest: version %d (%s, %s).", head.Number, seconds(head.Duration), approvalWords(head.Approval.State))
	default:
		fmt.Print("No cut yet.")
	}
	if p.OpenJobs > 0 {
		fmt.Printf(" MetaRush is still working on %s for it.", plural(p.OpenJobs, "thing"))
	}
	fmt.Println()
	fmt.Println(p.URL)
	if details {
		fmt.Printf("project: %s   latest version: %s\n", p.Project.ID, orNone(p.HeadRevisionID))
	}
	if len(p.Assets) > 0 {
		fmt.Println("\nFootage")
		for _, as := range p.Assets {
			fmt.Println("  " + assetLine(as))
			if details {
				fmt.Printf("      %s  %s\n", as.ID, strings.Join(as.Variants, ","))
			}
		}
	}
	if len(p.Links) > 0 {
		fmt.Println("\nUses")
		for _, l := range p.Links {
			fmt.Println("  " + l.Text)
		}
	}
	// What the project keeps (D166): where it goes, its rules, the sign-offs waiting.
	if ds := p.Project.Brief.Deliverables; len(ds) > 0 {
		var parts []string
		for _, d := range ds {
			parts = append(parts, theirs(d.Name)+" ("+deliverableShapeWords(d.Preset)+")")
		}
		fmt.Println("\nWhere it goes: " + strings.Join(parts, ", "))
	}
	if rs := p.Project.Rules; len(rs) > 0 {
		fmt.Println("\nRules")
		for _, r := range rs {
			line := "  " + theirs(r.Text)
			if r.Check {
				line += " (checked in every quality check)"
			}
			fmt.Println(line)
		}
	}
	if hs := p.HouseRules; len(hs) > 0 {
		fmt.Println("\nHouse rules (from " + theirs(hs[0].HouseOf) + "'s settings)")
		for _, r := range hs {
			line := "  " + theirs(r.Text)
			if r.Check {
				line += " (checked in every quality check)"
			}
			fmt.Println(line)
		}
	}
	if open := ops.OpenOf(append(ops.OwnSignoffs(&p.Project, ""), p.HouseSignoffs...)); len(open) > 0 {
		fmt.Println("\n" + ops.SignoffReminder(open))
	}
	if len(p.Revisions) > 0 {
		fmt.Println("\nVersions")
		for _, r := range p.Revisions {
			printRevisionRow(r, details)
		}
	}
	if len(p.Renders) > 0 {
		fmt.Println("\nRenders")
		for i, r := range p.Renders {
			if i == 5 && !details {
				fmt.Printf("  …and %d more\n", len(p.Renders)-5)
				break
			}
			printRenderRow(r, details)
		}
	}
}

// printRevisionRow is one version in a list: "* version 3 · 32.3 s · draft ·
// Theo · “what was asked”" (* marks the latest); details adds its ID and branch.
func printRevisionRow(r ops.RevisionSummary, details bool) {
	mark := " "
	if r.IsHead {
		mark = "*"
	}
	parts := []string{fmt.Sprintf("version %d", r.Number), seconds(r.Duration), approvalWords(r.Approval.State)}
	if r.CreatedBy.UserName != "" {
		parts = append(parts, r.CreatedBy.UserName)
	}
	if r.Instruction != "" {
		parts = append(parts, "“"+trunc(r.Instruction, 60)+"”")
	}
	fmt.Printf("%s %s\n", mark, strings.Join(parts, " · "))
	if details {
		fmt.Printf("    %s  %s\n", r.ID, r.Branch)
	}
}

// renderFailure is the error for a render that did not finish: what happened
// and what to do, in plain words; the program's own error is its cause and the
// render its details (both shown with --details and --json).
func renderFailure(r *ops.RenderOut) error {
	label := capitalize(renderWords(r.Render.Quality, r.Render.RevisionNumber))
	msg := r.Render.Error
	if r.Job != nil && r.Job.Error != "" {
		msg = r.Job.Error
	}
	var e *apperr.Error
	switch {
	case r.Render.State == models.RenderCanceled:
		e = apperr.New(apperr.ToolFailed, "%s was stopped before it finished.", label)
	case msg != "":
		e = apperr.Wrap(apperr.ToolFailed, errors.New(msg), "%s didn't finish.", label)
	default:
		e = apperr.New(apperr.ToolFailed, "%s didn't finish.", label)
	}
	if r.Job != nil {
		e = e.WithHint(ops.JobAdvice(r.Job))
	}
	return e.WithDetails(r)
}

// printRenderStatus says how a render is doing.
func printRenderStatus(r *ops.RenderOut, details bool) {
	label := capitalize(renderWords(r.Render.Quality, r.Render.RevisionNumber))
	switch r.Render.State {
	case models.RenderAvailable:
		if o := r.Render.Output; o != nil {
			fmt.Printf("%s is ready (%s, %s).\n", label, seconds(o.Duration), fmtBytes(o.Size))
		} else {
			fmt.Printf("%s is ready.\n", label)
		}
		if q := qaWords(r.Render.QA); q != "" {
			fmt.Println(capitalize(q) + ".")
		}
	case models.RenderQueued:
		fmt.Printf("%s is waiting to start.\n", label)
	case models.RenderFailed:
		fmt.Printf("%s didn't finish.\n", label)
		if r.Job != nil {
			fmt.Println(ops.JobAdvice(r.Job))
		}
	case models.RenderCanceled:
		fmt.Printf("%s was stopped.\n", label)
	default:
		line := fmt.Sprintf("%s is %s", label, renderStateWords(r.Render.State))
		if r.Job != nil && r.Job.Progress.Pct > 0 {
			line += fmt.Sprintf(": %.0f%%", r.Job.Progress.Pct*100)
		}
		fmt.Println(line + ".")
	}
	fmt.Println(r.URL)
	if details {
		fmt.Printf("render %s · %s · %s %s\n", r.Render.ID, r.Render.State, r.Render.Preset, r.Render.Quality)
		if r.Job != nil {
			fmt.Printf("job %s · %s %.0f%% %s\n", r.Job.ID, r.Job.State, r.Job.Progress.Pct*100, r.Job.Progress.Message)
			if r.Job.Error != "" {
				fmt.Println("error:", r.Job.Error)
			}
		}
		if r.Render.Error != "" {
			fmt.Println("render error:", r.Render.Error)
		}
	}
}

// printRenderRow is one render in a list: its version, quality, shape, state
// and quality check; details adds its ID.
func printRenderRow(r models.Render, details bool) {
	line := fmt.Sprintf("  version %-3d %-7s %-10s %-16s %s", r.RevisionNumber, r.Quality, presetWords(r.Preset), renderStateWords(r.State), plainWhen(r.CreatedAt))
	if q := qaWords(r.QA); q != "" {
		line += " · " + q
	}
	fmt.Println(line)
	if details {
		fmt.Printf("      %s\n", r.ID)
	}
}
