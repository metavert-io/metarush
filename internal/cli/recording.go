package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
	screenrec "github.com/metavert-io/metarush/internal/recorder"
)

// Screen recordings made in the dashboard (D258–D260): recording happens in a
// browser (Studio → Record your screen); these commands list, finish, link
// and cut what was recorded.
func recordingCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "recording", Aliases: []string{"recordings"}, Short: "Screen recordings made in the dashboard: list and show them, keep what arrived, link one into a project, start a cut from one",
		Long: `A screen recording is made in the dashboard (the Add box's "Record your screen"): the screen, the
camera and the microphone are recorded in the browser as separate files on one clock and sent to the
computer that runs MetaRush while they record. Each becomes footage; in a project they are one
conversation, so a cut can show the screen with the camera in a corner, or side by side.

Examples:
  metarush recording list --project <id>
  metarush recording show <recording> --details
  metarush recording finish <recording> --keep        # after a closed tab: keep what arrived
  metarush recording link <recording> --project <id>
  metarush recording cut <recording> --layout pip --apply
  metarush recording discard <recording>`}
	cmd.AddCommand(recListCmd(), recShowCmd(), recFinishCmd(), recLinkCmd(), recCutCmd(), recDiscardCmd())
	return cmd
}

// printRecording shows a recording in plain words.
func printRecording(v *screenrec.RecordingInfo, details bool) {
	fmt.Println(v.Text)
	for _, t := range v.Tracks {
		line := fmt.Sprintf("  %-7s %s", t.ID, t.Text)
		if t.Title != "" {
			line += " — " + theirs(t.Title)
		}
		fmt.Println(line)
		if details {
			for _, rt := range v.Recording.Tracks {
				if rt.ID == t.ID {
					fmt.Printf("  %-7s clip %s · format %s · %d bytes · start %+.3f s · drift %+.1f ppm\n", "", orText(t.AssetID, "-"), rt.Format, rt.Bytes, rt.Start, rt.Drift)
				}
			}
		}
	}
	for _, l := range v.Links {
		line := "In " + theirs(l.ProjectName)
		if l.Sync != "" {
			line += ": " + l.Sync
		}
		fmt.Println(line + ".")
	}
	if v.Recording.Lined != "" && len(v.Links) > 0 {
		fmt.Println("Its tracks are " + v.Recording.Lined + ".")
	}
	if v.Next != "" {
		fmt.Println(v.Next)
	}
	if details {
		r := v.Recording
		fmt.Printf("ID %s · state %s · received on %s · %d pauses", r.ID, r.State, r.MachineName, len(r.Pauses))
		if r.Clock != nil {
			fmt.Printf(" · clock spread %.1f ms", r.Clock.Spread)
		}
		fmt.Println()
	}
}

func recListCmd() *cobra.Command {
	var in screenrec.RecordingListIn
	cmd := &cobra.Command{Use: "list", Short: "The screen recordings, newest first: their tracks, length and whether they are footage yet",
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			if in.ProjectID != "" {
				p, err := ops.ResolveProject(c.Context(), a, in.ProjectID)
				if err != nil {
					return failErr(c, err)
				}
				in.ProjectID = p.ID
			}
			return runOpWith(c, a, "recording.list", in, ops.Caller{}, func(o any) {
				out := o.(*screenrec.RecordingListOut)
				if len(out.Recordings) == 0 {
					fmt.Println("No screen recordings yet. Record one from the dashboard: the Add box's Record your screen.")
					return
				}
				for i := range out.Recordings {
					fmt.Println("• " + out.Recordings[i].Text)
				}
			})
		}}
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "only the ones made for or linked into this project")
	cmd.Flags().IntVar(&in.Limit, "limit", 0, "at most this many (default 100)")
	cmd.Flags().BoolVar(&in.Discarded, "discarded", false, "also the ones thrown away")
	return cmd
}

func recShowCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "show <recording>", Short: "A screen recording's tracks, where each starts on its clock, its footage and its projects", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "recording.show", screenrec.RecordingRefIn{Recording: args[0]}, func(o any) {
				printRecording(o.(*screenrec.RecordingInfo), detailsMode(c))
			})
		}}
	return cmd
}

func recFinishCmd() *cobra.Command {
	var keep bool
	cmd := &cobra.Command{Use: "finish <recording>", Short: "Make a screen recording footage (with --keep: what arrived, after a closed tab or a lost connection)", Args: cobra.ExactArgs(1),
		Long: `Runs on the computer that received the recording's pieces. The browser finishes a recording by
itself when it stops; use this when the tab closed or the connection dropped mid-recording.

Examples:
  metarush recording finish <recording> --keep`,
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "recording.finish", screenrec.RecordingFinishIn{Recording: args[0], Keep: keep}, func(o any) {
				printRecording(o.(*screenrec.RecordingInfo), detailsMode(c))
			})
		}}
	cmd.Flags().BoolVar(&keep, "keep", false, "keep what arrived, even if pieces arrived a moment ago")
	return cmd
}

func recLinkCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{Use: "link <recording> --project <project>", Short: "Link a screen recording into a project: its screen, camera and voice join it as one conversation", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if project == "" {
				return fail(c, apperr.InvalidInput, "say which project: --project <id or name>", "")
			}
			return runOp(c, "recording.link", screenrec.RecordingLinkIn{Recording: args[0], Project: project}, func(o any) {
				printRecording(o.(*screenrec.RecordingInfo), detailsMode(c))
			})
		}}
	cmd.Flags().StringVar(&project, "project", "", "the project (ID or name)")
	return cmd
}

func recCutCmd() *cobra.Command {
	var in screenrec.RecordingCutIn
	var confirm string
	cmd := &cobra.Command{Use: "cut <recording>", Short: "Start a cut from a screen recording: the camera in a corner, side by side, the screen only or the camera only", Args: cobra.ExactArgs(1),
		Long: `Layouts:
  pip            the screen full frame, the camera in a corner (default with a camera)
  side-by-side   the screen and the camera in two columns
  screen         the screen only (default without a camera)
  camera         the camera only
The voice plays throughout. The first cut of a project is its main cut; otherwise it starts a new cut
named after the recording.

Examples:
  metarush recording cut <recording> --layout pip
  metarush recording cut <recording> --project <id> --layout side-by-side --apply`,
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			in.Recording = args[0]
			out, err := ops.CallTyped[screenrec.RecordingCutOut](c.Context(), a, "recording.cut", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			if confirm != "" && out.Result != nil {
				return planFlow(c, a, out.Result.Plan, confirm, map[string]any{"changes": out.Changes, "branch": out.Branch}, func() { fmt.Println(out.Summary) })
			}
			return resultFlow(c, a, out, pending(out.Result, out.Applied), func() {
				fmt.Println(out.Summary)
				for _, ch := range out.Changes {
					fmt.Println("  • " + ch)
				}
				if out.NewCut && out.Branch != "main" {
					fmt.Printf("It starts a new cut (%s).\n", out.Branch)
				}
			})
		}}
	f := cmd.Flags()
	f.StringVar(&in.Project, "project", "", "the project (default: the one it was recorded for); linked first when it isn't")
	f.StringVar(&in.Layout, "layout", "", "pip | side-by-side | screen | camera")
	f.StringVar(&in.Shape, "shape", "", "landscape (16:9, the default) | vertical | square")
	f.StringVar(&in.Branch, "branch", "", "the cut to start (default: a new cut named after the recording)")
	f.BoolVar(&in.Apply, "apply", false, "make it a version of the cut")
	f.StringVar(&confirm, "confirm", "", "apply with the token from the check")
	return cmd
}

func recDiscardCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "discard <recording>", Short: "Throw away a screen recording that isn't footage yet (people only)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			// Throwing a recording away is a person's choice, asked at a terminal.
			if !interactive() {
				return fail(c, apperr.HumanOnly, "Only a person can throw a recording away: run this at a terminal, or use the recorder in the dashboard.", "")
			}
			if ask("Throw this recording away for good? Type yes", "") != "yes" {
				return fail(c, apperr.Canceled, "nothing was thrown away", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "recording.discard", screenrec.RecordingRefIn{Recording: args[0]}, ops.Caller{Human: true}, func(o any) {
				printRecording(o.(*screenrec.RecordingInfo), false)
			})
		}}
	return cmd
}
