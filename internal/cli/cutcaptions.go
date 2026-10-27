package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// captionLooksCmd is `metarush captions looks`: the vetted caption looks (D245).
func captionLooksCmd() *cobra.Command {
	var in ops.CaptionLooksIn
	c := &cobra.Command{
		Use:   "looks [--project <project>]",
		Short: "The caption looks a cut can use, what each looks like and suits (and the project's house style's)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "captions.looks", in, func(o any) {
				r := o.(*ops.CaptionLooksOut)
				for _, l := range r.Looks {
					mark := ""
					if l.ID == r.House {
						mark = " (the house style's)"
					}
					if detailsMode(c) {
						fmt.Printf("%-10s %s%s: %s\n", l.ID, l.Name, mark, l.Summary)
					} else {
						fmt.Printf("%s%s: %s\n", l.Name, mark, l.Summary)
					}
				}
			})
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "also say which look the project's house style has")
	return c
}

// cutCaptionsCmd is `metarush cut captions`: how the latest version's
// captions look (D245).
func cutCaptionsCmd() *cobra.Command {
	var in ops.CutCaptionsIn
	var on, off, details bool
	c := &cobra.Command{
		Use:   "captions <project> [--look <look>] [--size small|medium|large] [--position bottom|lower|middle|top] [--words-per-line N] [--on|--off] [--dry-run | --confirm <token>]",
		Short: "Choose how the cut's captions look: one of the vetted looks, their size and place, words per line, on or off",
		Long: `Choose how the latest version's captions look. The looks (metarush captions looks) are drawn in the
brand's colours and font:

  clean      white words on a soft shade
  emphasis   each word turns the accent colour as it is said
  boxed      words on a solid box
  bold       a few large capitals at a time with an outline, for phones
  highlight  the word being said sits on a box in the accent colour
  subtitle   smaller words with a thin outline and longer lines

  metarush cut captions "Garden walk" --look bold
  metarush cut captions "Garden walk" --size large --position top
  metarush cut captions "Garden walk" --off

A new look brings its own words per line unless --words-per-line says otherwise. It shows the change first;
in a terminal it asks before saving. Otherwise run it again with --confirm <token> to save it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ProjectID = args[0]
			if on && off {
				return fail(c, apperr.InvalidInput, "say --on or --off, not both", "")
			}
			if on || off {
				v := on
				in.On = &v
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			call := func(x ops.CutCaptionsIn) (*ops.CutCaptionsOut, error) {
				b, _ := json.Marshal(x)
				res, err := ops.Call(c.Context(), a, "cut.captions", b, ops.Caller{})
				if err != nil {
					return nil, err
				}
				return res.(*ops.CutCaptionsOut), nil
			}
			if in.ConfirmToken == "" && !in.DryRun && interactive() && !jsonMode(c) {
				dry := in
				dry.DryRun = true
				r, err := call(dry)
				if err != nil {
					return failErr(c, err)
				}
				printCaptionsChange(c, r, "")
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
				printCaptionsChange(c, r, next)
			})
		},
	}
	f := c.Flags()
	f.StringVar(&in.Look, "look", "", "clean, emphasis, boxed, bold, highlight or subtitle (or the look's name)")
	f.StringVar(&in.Size, "size", "", "small, medium or large")
	f.StringVar(&in.Position, "position", "", "bottom, lower, middle or top")
	f.IntVar(&in.Words, "words-per-line", 0, "words per line (1–12)")
	f.BoolVar(&on, "on", false, "turn the captions on")
	f.BoolVar(&off, "off", false, "turn the captions off")
	f.StringVar(&in.Branch, "branch", "", "branch (default: main)")
	f.BoolVar(&in.DryRun, "dry-run", false, "only check: the change, its warnings and a confirm token")
	f.StringVar(&in.ConfirmToken, "confirm", "", "token from a check of exactly this change")
	f.StringVarP(&in.Instruction, "message", "m", "", "what was asked (recorded with the new version)")
	f.StringVar(&in.Rationale, "why", "", "why (recorded with the new version)")
	f.BoolVar(&in.AllowUnlock, "allow-unlock", false, "permit changing locked captions (only when the person asked)")
	addDetailsFlag(c, &details)
	return c
}

// printCaptionsChange shows a captions change and that it is ready to save
// (next is the command that saves it, for --details), or the version it was
// saved as.
func printCaptionsChange(c *cobra.Command, r *ops.CutCaptionsOut, next string) {
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
