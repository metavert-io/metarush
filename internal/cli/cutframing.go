package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// cutFramingCmd is `metarush cut framing`: how the latest version frames its
// crops, for the whole cut or one clip (D73, D115).
func cutFramingCmd() *cobra.Command {
	var in ops.CutFramingIn
	var details bool
	c := &cobra.Command{
		Use:   "framing <project> follow|faces|center|cut [--clip <clip>] [--dry-run | --confirm <token>]",
		Short: "Choose how crops are framed: follow the subject as it moves, keep faces in frame, or keep the middle (the whole cut, or one clip)",
		Long: `Choose how the latest version frames its crops when the footage is cropped to the video's shape:

  follow   the crop moves with the subject as it moves: the main face, or what stands out
           where no face is seen; smoothly, and never across the footage's own cuts
  faces    the crop keeps the main face of each clip in frame (one place per clip)
  center   the crop keeps the middle of the picture
  cut      (with --clip) the clip does what the whole cut says

  metarush cut framing "Garden walk" follow
  metarush cut framing "Garden walk" faces --clip 3

Following and keeping faces in frame use what MetaRush saw in the footage (a Mac looks at it),
so a clip it hasn't looked at yet keeps the middle until it has. It shows the change first; in a
terminal it asks before saving. Otherwise run it again with --confirm <token> to save it.`,
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			in.ProjectID, in.Framing = args[0], args[1]
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			call := func(x ops.CutFramingIn) (*ops.CutFramingOut, error) {
				b, _ := json.Marshal(x)
				res, err := ops.Call(c.Context(), a, "cut.framing", b, ops.Caller{})
				if err != nil {
					return nil, err
				}
				return res.(*ops.CutFramingOut), nil
			}
			if in.ConfirmToken == "" && !in.DryRun && interactive() && !jsonMode(c) {
				dry := in
				dry.DryRun = true
				r, err := call(dry)
				if err != nil {
					return failErr(c, err)
				}
				printFramingChange(c, r, "")
				if r.NoChange || r.Check == nil || r.Check.ConfirmToken == "" {
					return nil
				}
				if !askYes("Save this change as a new version?", false) {
					return fail(c, apperr.Canceled, "not saved", "")
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
				printFramingChange(c, r, next)
			})
		},
	}
	f := c.Flags()
	f.StringVar(&in.Clip, "clip", "", "one clip: its name in the cut sheet or its number (default: the whole cut)")
	f.StringVar(&in.Branch, "branch", "", "branch (default: main)")
	f.BoolVar(&in.DryRun, "dry-run", false, "only check: the change, its warnings and a confirm token")
	f.StringVar(&in.ConfirmToken, "confirm", "", "token from a check of exactly this change")
	f.StringVarP(&in.Instruction, "message", "m", "", "what was asked (recorded with the new version)")
	f.StringVar(&in.Rationale, "why", "", "why (recorded with the new version)")
	f.BoolVar(&in.AllowUnlock, "allow-unlock", false, "permit changes to locked clips (only when the person asked)")
	addDetailsFlag(c, &details)
	return c
}

// printFramingChange shows a framing change and that it is ready to save
// (next is the command that saves it, for --details), or the version it was
// saved as.
func printFramingChange(c *cobra.Command, r *ops.CutFramingOut, next string) {
	details := detailsMode(c)
	switch {
	case r.NoChange:
		fmt.Println("Nothing to change: " + r.Summary)
		return
	case r.Applied != nil:
		fmt.Printf("Saved as version %d: %s\n", r.Applied.Revision.Number, r.Summary)
		if r.Applied.Rebased {
			fmt.Println("Someone else saved changes meanwhile; yours were combined with theirs.")
		}
		for _, w := range r.Applied.Warnings {
			fmt.Println("  ! " + w.Message)
		}
		if details {
			fmt.Printf("revision: %s\n%s\n", r.Applied.Revision.ID, r.Applied.URL)
		}
		return
	}
	chk := r.Check
	if chk == nil {
		fmt.Println(r.Summary)
		return
	}
	if !chk.Valid {
		fmt.Printf("This change can't be made yet: %s\n", r.Summary)
		for _, e := range chk.Errors {
			fmt.Println("  ✗ " + e.Message)
		}
		return
	}
	fmt.Println(r.Summary)
	for _, w := range chk.Warnings {
		fmt.Println("  ! " + w.Message)
	}
	readyToSave(c, next)
}
