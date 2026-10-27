package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/ops"
)

// cutTransitionCmd is `metarush cut transition`: one cut of the latest
// version, in the cut sheet's words (`↳ dissolve 0.5 s · voice carries 0.3 s`).
func cutTransitionCmd() *cobra.Command {
	var in ops.CutTransitionIn
	var lead, carry, within, blend float64
	var auto, withPicture, details bool
	c := &cobra.Command{
		Use:   "transition <project> <clip> <type> [duration] [--direction left|right|up|down] [--easing …] [--lead <s> | --carry <s> | --auto] [--dry-run | --confirm <token>]",
		Short: "Change how a clip comes in: the transition (dissolve, fade through black, slide left…) and the sound (start a voice early, let it carry over, or change in a pause)",
		Long: `Change one cut of the latest version. <clip> is the clip the cut leads into: its name in
the cut sheet or its number (1 = the first clip). <type> is a transition in plain words
(` + "`metarush edit transitions`" + ` lists them; "keep" keeps the picture as it is).
Anything you leave out stays as it is; a new type starts from its usual length.

  metarush cut transition "Spring promo" 3 dissolve 0.5
  metarush cut transition "Spring promo" maya cut --lead 0.4     # her voice starts 0.4 s before we see her
  metarush cut transition "Spring promo" 4 cut --carry 0.3       # the voice carries 0.3 s over the next shot
  metarush cut transition "Spring promo" 4 cut --auto            # the sound changes in the nearest pause
  metarush cut transition "Spring promo" 2 slide 0.4 --direction left --easing "slows down"
  metarush cut transition "Spring promo" 6 "fade out" 1.5 --end  # how the last clip fades out

It shows the change and its warnings first; in a terminal it asks before saving. Otherwise
run it again with --confirm <token> to save it as a new version.`,
		Args: cobra.MinimumNArgs(3),
		RunE: func(c *cobra.Command, args []string) error {
			in.ProjectID, in.Clip = args[0], args[1]
			words := args[2:]
			if n := len(words); n > 1 {
				if v, ok := parseSeconds(words[n-1]); ok {
					in.Duration = &v
					words = words[:n-1]
				}
			}
			typ := strings.Join(words, " ")
			switch {
			case strings.EqualFold(typ, "keep"):
				typ = ""
			case in.End:
			default:
				if _, ok := cutsheet.TransitionType(typ); !ok {
					if t, ok := cutsheet.TransitionType(strings.ReplaceAll(typ, "-", " ")); ok {
						typ = t
					} else {
						return fail(c, apperr.InvalidInput, fmt.Sprintf("MetaRush doesn't know the transition %q.", typ), "See `metarush edit transitions` (dissolve, fade through black, slide…).")
					}
				}
			}
			in.Type = typ
			modes := 0
			flags := c.Flags()
			if flags.Changed("lead") {
				in.Sound, in.SoundSeconds, modes = "lead", &lead, modes+1
			}
			if flags.Changed("carry") {
				in.Sound, in.SoundSeconds, modes = "carry", &carry, modes+1
			}
			if auto {
				in.Sound, modes = "auto", modes+1
				if flags.Changed("within") {
					in.SoundSeconds = &within
				}
			} else if flags.Changed("within") {
				return fail(c, apperr.InvalidInput, "--within goes with --auto (how far MetaRush may move the sound).", "")
			}
			if withPicture {
				in.Sound, modes = "cut", modes+1
			}
			if modes > 1 {
				return fail(c, apperr.InvalidInput, "Choose one way for the sound: --lead, --carry, --auto or --with-picture.", "")
			}
			if flags.Changed("blend") {
				in.Blend = &blend
			}
			if in.End && modes > 0 {
				return fail(c, apperr.InvalidInput, "A fade-out has no split sound; leave out --lead, --carry and --auto with --end.", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			call := func(x ops.CutTransitionIn) (*ops.CutTransitionOut, error) {
				b, _ := json.Marshal(x)
				res, err := ops.Call(c.Context(), a, "cut.transition", b, ops.Caller{})
				if err != nil {
					return nil, err
				}
				return res.(*ops.CutTransitionOut), nil
			}
			if in.ConfirmToken == "" && !in.DryRun && interactive() && !jsonMode(c) {
				// A person at a terminal: show the change and ask (the same gate as
				// the confirm token, which other callers must pass).
				dry := in
				dry.DryRun = true
				r, err := call(dry)
				if err != nil {
					return failErr(c, err)
				}
				printTransitionChange(c, r, "")
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
				printTransitionChange(c, r, next)
			})
		},
	}
	f := c.Flags()
	f.StringVar(&in.Direction, "direction", "", "wipe, slide, push, whip: the way the picture moves (left, right, up, down)")
	f.StringVar(&in.Easing, "easing", "", "the pace: smooth, even speed, speeds up, slows down")
	f.Float64Var(&lead, "lead", 0, "this clip's sound starts this many seconds before we see it")
	f.Float64Var(&carry, "carry", 0, "the previous clip's sound carries this many seconds over this clip")
	f.BoolVar(&auto, "auto", false, "MetaRush moves the sound change into the nearest pause, so no word is cut")
	f.Float64Var(&within, "within", 0, "with --auto: the most it may move the sound, in seconds (default 1)")
	f.BoolVar(&withPicture, "with-picture", false, "the sound changes with the picture (no early or carried-over sound)")
	f.Float64Var(&blend, "blend", 0, "seconds over which the two sounds blend")
	f.BoolVar(&in.End, "end", false, "set how the clip fades out instead (fade out, fade through black, or cut for none)")
	f.StringVar(&in.Branch, "branch", "", "branch (default: main)")
	f.BoolVar(&in.DryRun, "dry-run", false, "only check: the change, its warnings and a confirm token")
	f.StringVar(&in.ConfirmToken, "confirm", "", "token from a check of exactly this change")
	f.StringVarP(&in.Instruction, "message", "m", "", "what was asked (recorded with the new version)")
	f.StringVar(&in.Rationale, "why", "", "why (recorded with the new version)")
	f.BoolVar(&in.AllowUnlock, "allow-unlock", false, "permit changes to locked clips (only when the person asked)")
	addDetailsFlag(c, &details)
	return c
}

// parseSeconds reads "0.5", "0.5s" or "1.5 s".
func parseSeconds(s string) (float64, bool) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "s"))
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil && v >= 0
}

// printTransitionChange shows a changed cut between two clips and that it is
// ready to save (next is the command that saves it, for --details; "" when the
// person is asked right away), or the version it was saved as.
func printTransitionChange(c *cobra.Command, r *ops.CutTransitionOut, next string) {
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
	if details {
		fmt.Printf("cut sheet line: ↳ %s\n", r.Words)
	}
	readyToSave(c, next)
}
