package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// importTimelineCmd is `metarush import timeline <file>` (D303, D304): a
// timeline exported from another editing app becomes a new cut.
func importTimelineCmd() *cobra.Command {
	var in ops.TimelineImportIn
	var relink, add, skip []string
	var newProject string
	var readOnly, details bool
	c := &cobra.Command{
		Use:   "timeline <file> --project <project> [--relink <footage>=<clip>] [--add <footage>=<file>] [--skip <footage>] [--dry-run | --confirm <token>]",
		Short: "Make a new cut from a timeline another editing app exported (OpenTimelineIO, FCPXML, Final Cut Pro 7 XML, EDL), matched to your footage",
		Long: `Make a new cut from a timeline exported from another editing app: an OpenTimelineIO file (.otio), FCPXML
1.8 to 1.11 (.fcpxml, or an .fcpxmld bundle), Final Cut Pro 7 XML (.xml) or a CMX 3600 EDL (.edl).

Each clip is matched to footage in the Library: by MetaRush's own note (a timeline MetaRush exported), or its
name, length and timecode; the files a timeline names are never opened. For footage it doesn't find it asks:
relink it to a clip, add a video or sound file you name (added once you confirm),
or leave it out. Dissolves, fades, wipes, split edits, sound levels and speed come over; chapter markers become
the version's chapters and other markers notes on the clips; stretches with no picture are closed up. What can't
be carried over is listed. The cut goes through the normal edit check.

  metarush import timeline ~/Desktop/rough-cut.fcpxml --project "Garden walk"
  metarush import timeline cut.edl --project "Garden walk" --fps 25 --relink "Garden 3.mov=Wide shot" --skip "Logo.mov"
  metarush import timeline cut.otio --project "Garden walk" --read     (only say what would come in)

In a terminal it asks about footage it can't find and asks before saving; otherwise run it again with --confirm <token>.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.File = args[0]
			for _, s := range relink {
				k, v, ok := strings.Cut(s, "=")
				if !ok || strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
					return fail(c, apperr.InvalidInput, "--relink is <footage>=<clip>, like \"Garden 3.mov=Wide shot\"", "")
				}
				in.Answers = append(in.Answers, ops.TimelineAnswer{Footage: strings.TrimSpace(k), Relink: strings.TrimSpace(v)})
			}
			for _, s := range add {
				k, v, ok := strings.Cut(s, "=")
				if !ok || strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
					return fail(c, apperr.InvalidInput, "--add is <footage>=<file on this computer>, like \"Garden 3.mov=Garden 3.mov\"", "")
				}
				in.Answers = append(in.Answers, ops.TimelineAnswer{Footage: strings.TrimSpace(k), Add: strings.TrimSpace(v)})
			}
			for _, s := range skip {
				in.Answers = append(in.Answers, ops.TimelineAnswer{Footage: strings.TrimSpace(s), Skip: true})
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := c.Context()
			if readOnly {
				res, err := ops.Call(ctx, a, "timeline.read", mustMarshal(ops.TimelineReadIn{File: in.File, ProjectID: in.ProjectID, Timeline: in.Timeline, FPS: in.FPS, Answers: in.Answers}), ops.Caller{})
				if err != nil {
					return failErr(c, err)
				}
				r := res.(*ops.TimelineReadOut)
				return emit(c, r, func() { printTimelineRead(c, r) })
			}
			if newProject != "" {
				p, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: newProject}, ops.Caller{})
				if err != nil {
					return failErr(c, err)
				}
				in.ProjectID = p.Project.ID
			}
			if strings.TrimSpace(in.ProjectID) == "" {
				return fail(c, apperr.InvalidInput, "say which project the cut goes into", "--project \"Garden walk\", or --new-project \"Name\"")
			}
			call := func(x ops.TimelineImportIn) (*ops.TimelineImportOut, error) {
				res, err := ops.Call(ctx, a, "timeline.import", mustMarshal(x), ops.Caller{})
				if err != nil {
					return nil, err
				}
				return res.(*ops.TimelineImportOut), nil
			}
			if in.ConfirmToken == "" && !in.DryRun && interactive() && !jsonMode(c) {
				dry := in
				dry.DryRun = true
				r, err := call(dry)
				if err != nil {
					return failErr(c, err)
				}
				// Ask about footage it didn't find, then check again.
				if r.Read.Questions > 0 {
					printTimelineRead(c, &r.Read)
					for _, ft := range r.Read.Footage {
						if ft.Question == "" {
							continue
						}
						if ans, ok := askFootage(ft); ok {
							in.Answers = append(in.Answers, ans)
						} else {
							return fail(c, apperr.Canceled, "not imported", "")
						}
					}
					dry = in
					dry.DryRun = true
					if r, err = call(dry); err != nil {
						return failErr(c, err)
					}
				}
				printTimelineImport(c, r, "")
				if r.Check == nil || r.Check.ConfirmToken == "" {
					return nil
				}
				if !askYes("Make this cut?", false) {
					return fail(c, apperr.Canceled, "not imported", "")
				}
				in.ConfirmToken = r.Check.ConfirmToken
			}
			r, err := call(in)
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, r, func() {
				next := ""
				if r.Check != nil && r.Check.ConfirmToken != "" {
					next = sameCommandWith(c, "--confirm", r.Check.ConfirmToken)
				}
				printTimelineImport(c, r, next)
			})
		},
	}
	f := c.Flags()
	f.StringVar(&in.ProjectID, "project", "", "the project the cut goes into (name or ID)")
	f.StringVar(&newProject, "new-project", "", "make a new project with this name for the cut")
	f.StringVar(&in.Timeline, "timeline", "", "which timeline, when the file holds several (by name)")
	f.Float64Var(&in.FPS, "fps", 0, "an EDL's frame rate (EDLs don't say it): 23.976, 24, 25, 29.97, 30…")
	f.StringArrayVar(&relink, "relink", nil, "use a clip from the Library for footage it didn't find: <footage>=<clip name or ID> (repeat)")
	f.StringArrayVar(&add, "add", nil, "add a file on this computer for footage it didn't find: <footage>=<file> (repeat)")
	f.StringArrayVar(&skip, "skip", nil, "leave this footage out of the cut (repeat)")
	f.StringVar(&in.CutName, "name", "", "the new cut's name (default: the timeline's)")
	f.StringVar(&in.Branch, "branch", "", "the new cut's short name (default: from its name)")
	f.BoolVar(&readOnly, "read", false, "only say what would come in; change nothing")
	f.BoolVar(&in.DryRun, "dry-run", false, "only check: what comes in, the edit check and a confirm token")
	f.StringVar(&in.ConfirmToken, "confirm", "", "token from a check of exactly this import")
	addDetailsFlag(c, &details)
	return c
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// askFootage asks a person what to do with footage the Library doesn't have.
func askFootage(ft ops.TimelineFootage) (ops.TimelineAnswer, bool) {
	fmt.Println()
	fmt.Println(ft.Question)
	for _, cand := range ft.Candidates {
		fmt.Printf("  maybe “%s” (%s)\n", cand.Title, cand.Why)
	}
	for {
		choice := strings.ToLower(strings.TrimSpace(ask("Relink, add the file, or skip? [r/a/s, or q to stop]", "")))
		switch {
		case strings.HasPrefix(choice, "r"):
			def := ""
			if len(ft.Candidates) > 0 {
				def = ft.Candidates[0].AssetID
			}
			if clip := strings.TrimSpace(ask("Which clip (its name or ID)?", def)); clip != "" {
				return ops.TimelineAnswer{Footage: ft.Key, Relink: clip}, true
			}
		case strings.HasPrefix(choice, "a"):
			// No suggestion: the timeline's own path is never offered (a
			// timeline can name any file), only what the person types.
			if path := strings.TrimSpace(ask("Which file (a video or sound file on this computer)?", "")); path != "" {
				return ops.TimelineAnswer{Footage: ft.Key, Add: path}, true
			}
		case strings.HasPrefix(choice, "s"):
			return ops.TimelineAnswer{Footage: ft.Key, Skip: true}, true
		case strings.HasPrefix(choice, "q"):
			return ops.TimelineAnswer{}, false
		}
	}
}

func printTimelineRead(c *cobra.Command, r *ops.TimelineReadOut) {
	details := detailsMode(c)
	fmt.Println(r.Summary)
	if len(r.Timelines) > 1 {
		fmt.Printf("  The file holds %d timelines; this is “%s” (choose another with --timeline).\n", len(r.Timelines), r.Name)
	}
	for _, w := range r.Warnings {
		fmt.Println("  ! " + w)
	}
	fmt.Println("Footage:")
	for _, ft := range r.Footage {
		switch {
		case ft.Question != "":
			fmt.Printf("  ? %s\n", ft.Question)
		case ft.How == "skipped":
			fmt.Printf("  - “%s” is left out\n", ft.Name)
		case ft.How == "will-add":
			fmt.Printf("  + “%s”: %s\n", ft.Name, ft.Why)
		default:
			fmt.Printf("  ✓ “%s” is “%s” (%s)\n", ft.Name, ft.Title, ft.Why)
		}
		if details {
			fmt.Printf("      key %s, clip %s, %d shot(s), %d sound(s)\n", ft.Key, ft.AssetID, ft.Shots, ft.Sounds)
		}
	}
	if len(r.Chapters) > 0 {
		fmt.Println("Chapters:")
		for _, ch := range r.Chapters {
			fmt.Printf("  %s %s\n", ops.ChapterClock(ch.At), ch.Title)
		}
	}
	if r.Notes > 0 {
		fmt.Printf("%d marker(s) become notes on the clips.\n", r.Notes)
	}
	if len(r.Lost) > 0 {
		fmt.Println("What can't be carried over:")
		for _, l := range r.Lost {
			fmt.Println("  - " + upperFirstWord(l))
		}
	}
	if details {
		for _, cl := range r.Clips {
			fmt.Println("  " + cl.Line)
		}
	}
}

func printTimelineImport(c *cobra.Command, r *ops.TimelineImportOut, next string) {
	if r.Applied != nil {
		fmt.Printf("Made the cut “%s”: %d clips, %s.\n", r.CutName, r.Read.Shots, plainSeconds(r.Read.Seconds))
		if r.ChaptersSaved {
			fmt.Printf("Its %d chapter(s) are in the version's delivery notes.\n", len(r.Read.Chapters))
		}
		for _, w := range r.Applied.Warnings {
			fmt.Println("  ! " + w.Message)
		}
		for _, w := range r.Read.Warnings {
			fmt.Println("  ! " + w)
		}
		fmt.Println("Approve it like any cut once you've watched it.")
		if detailsMode(c) {
			fmt.Printf("branch: %s\nrevision: %s\n%s\n", r.Branch, r.Applied.Revision.ID, r.Link)
		}
		return
	}
	printTimelineRead(c, &r.Read)
	chk := r.Check
	if chk != nil && !chk.Valid {
		fmt.Println("This cut can't be made yet:")
		for _, e := range chk.Errors {
			fmt.Println("  ✗ " + e.Message)
		}
		return
	}
	if chk != nil {
		for _, w := range chk.Warnings {
			fmt.Println("  ! " + w.Message)
		}
	}
	readyToSave(c, next)
}
