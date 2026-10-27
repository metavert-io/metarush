package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/measures"
	"github.com/metavert-io/metarush/internal/ops"
)

// Call recordings (D160–D162, docs/call-recordings.md) and project rules
// (D166, docs/keeping.md) from the terminal:
//
//	metarush footage switches <clip> [--find] [--wait]
//	metarush cut flashes <project> [--dry-run | --confirm <token>]
//	metarush cut names <project> [--first-names | --full-names] [--style band|tab] [--keep-titles]
//	metarush project rules <project> | rules add <project> "<rule>" [--check] [--measure …] | rules remove <project> <rule>

func footageSwitchesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "switches <clip>",
		Short: "Where a clip's picture switches, to the frame, who is on screen in each shot, and the names a call app printed in it",
		Long: `Where a clip's picture switches (a call recording's speaker view flipping between people), found
to the frame, who is on screen in each shot (a named face, the name the call app printed, or who talks),
and where the call app printed names in the picture. MetaRush finds them by itself after analyzing a clip;
metarush footage find-switches <clip> looks again.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "footage.switches", ops.FootageSwitchesIn{AssetID: args[0]}, func(o any) {
				printSwitches(c, o.(*ops.FootageSwitchesOut))
			})
		},
	}
	return cmd
}

func footageFindSwitchesCmd() *cobra.Command {
	var wait bool
	cmd := &cobra.Command{
		Use:   "find-switches <clip> [--wait]",
		Short: "Find where a clip's picture switches, to the frame (again), and who is on screen in each shot",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			b, _ := json.Marshal(ops.FootageSwitchesFindIn{AssetID: args[0]})
			out, err := ops.Call(c.Context(), a, "footage.switches.find", b, ops.Caller{})
			if err != nil {
				a.Close()
				return failErr(c, err)
			}
			res := out.(*ops.FootageSwitchesFindOut)
			if !wait {
				a.Close()
				return emit(c, res, func() { fmt.Println(res.Summary) })
			}
			if _, err := waitJobs(c, a, res.Jobs, 0, jsonMode(c)); err != nil {
				a.Close()
				return failErr(c, err)
			}
			a.Close()
			return runOp(c, "footage.switches", ops.FootageSwitchesIn{AssetID: args[0]}, func(o any) {
				printSwitches(c, o.(*ops.FootageSwitchesOut))
			})
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until they are found (here, when no other computer can)")
	return cmd
}

func printSwitches(c *cobra.Command, o *ops.FootageSwitchesOut) {
	fmt.Println(o.Summary)
	if o.State != "ready" {
		return
	}
	details := detailsMode(c)
	for _, sh := range o.Shots {
		who := sh.Person
		switch {
		case who == "":
			who = "nobody known"
		case sh.By == "voice":
			who += " (by the voice: not sure)"
		case sh.By == "looks":
			who += " (by looks: not confirmed)"
		case sh.By == "printed" && sh.Printed != sh.Person:
			who += " (the printed name “" + sh.Printed + "”)"
		}
		line := fmt.Sprintf("  %s–%s  %s", clipAt3(sh.From), clipAt3(sh.To), who)
		if details && sh.Printed != "" {
			line += "  · printed “" + sh.Printed + "”"
		}
		fmt.Println(line)
	}
	if o.Names != nil && details {
		fmt.Printf("printed names: %s, area x %.3f y %.3f w %.3f h %.3f\n", o.Names.Corner, o.Names.Box.X, o.Names.Box.Y, o.Names.Box.W, o.Names.Box.H)
	}
	if details && o.Precision > 0 {
		fmt.Printf("precision: %.3f s (the footage's frame rate varies)\n", o.Precision)
	}
}

// clipAt3 is a footage time to the frame ("2:15.400").
func clipAt3(v float64) string {
	m := int(v) / 60
	s := v - float64(m*60)
	return fmt.Sprintf("%d:%06.3f", m, s)
}

func cutFlashesCmd() *cobra.Command {
	var f cutEditFlags
	c := &cobra.Command{
		Use:   "flashes <project> [--dry-run | --confirm <token>]",
		Short: "Trim the split-second flashes of another picture at the edges of the latest cut's clips, at the exact switch",
		Long: `Trims the split-second slivers of another picture at the start or end of the latest cut's clips
(a call recording's speaker view switching just after a clip starts), at the exact frame the picture
switches. When that would take words out, the clip's sound starts a moment earlier (or carries on), so
nothing said is lost. It shows the change first; in a terminal it asks before saving.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runCutEdit(c, "cut.flashes", &f, func(common ops.CutEditCommon) any {
				common.ProjectID = args[0]
				return ops.CutFlashesIn{CutEditCommon: common}
			})
		},
	}
	f.add(c)
	return c
}

func cutNamesCmd() *cobra.Command {
	var f cutEditFlags
	var in ops.CutNamesIn
	var first, full bool
	c := &cobra.Command{
		Use:   "names <project> [--first-names | --full-names] [--style band|tab] [--keep-titles]",
		Short: "Cover the names a call app printed in the picture with your own name bar for the whole video",
		Long: `Covers the names a call app (Zoom, Teams, Meet) printed in the picture with a name bar of your own
that stays up for the whole video and shows who is on screen, changing at the exact frame the
picture switches. It replaces the cut's name titles for the same people (--keep-titles keeps them).
First names when the project's rules say so, or with --first-names.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if first && full {
				return fail(c, "E_INVALID_INPUT", "Choose --first-names or --full-names, not both.", "")
			}
			return runCutEdit(c, "cut.names", &f, func(common ops.CutEditCommon) any {
				x := in
				x.CutEditCommon = common
				x.ProjectID = args[0]
				switch {
				case first:
					v := true
					x.FirstNames = &v
				case full:
					v := false
					x.FirstNames = &v
				}
				return x
			})
		},
	}
	f.add(c)
	c.Flags().BoolVar(&first, "first-names", false, "only first names")
	c.Flags().BoolVar(&full, "full-names", false, "whole names, even when the project's rules say first names")
	c.Flags().StringVar(&in.Style, "style", "", "band (across the bottom, the default) or tab")
	c.Flags().BoolVar(&in.KeepTitles, "keep-titles", false, "keep the cut's name titles")
	c.Flags().StringVar(&in.ID, "name", "", "the graphic's name (default names)")
	return c
}

func projectRulesCmd() *cobra.Command {
	show := func(c *cobra.Command, o *ops.RulesOut) {
		fmt.Println(o.Summary)
		for i, r := range o.Rules {
			line := fmt.Sprintf("  %d. %s", i+1, theirs(r.Text))
			var notes []string
			if r.Promise {
				notes = append(notes, "a promise to the client: a version that breaks it waits before it goes out")
			}
			if r.Check {
				notes = append(notes, "checked in every quality check")
			}
			if w := ops.MeasureWords(r.Measure); w != "" {
				notes = append(notes, "measured: "+w)
			}
			if len(notes) > 0 {
				line += " (" + strings.Join(notes, "; ") + ")"
			}
			fmt.Println(line)
		}
	}
	cmd := &cobra.Command{
		Use:   "rules <project>",
		Short: "A project's rules: plainly worded style rules and checks Claude follows and the quality check measures where it can",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "project.rules.list", ops.RulesListIn{ProjectID: args[0]}, func(o any) { show(c, o.(*ops.RulesOut)) })
		},
	}
	var add ops.RuleAddIn
	addCmd := &cobra.Command{
		Use: `add <project> "<rule>" [--check] [--promise] [--measure …]`, Short: "Add a rule (First names only on screen), with --check a problem to check for every time, or with --promise a promise made to the client that holds a version back when broken", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			add.ProjectID, add.Text = args[0], args[1]
			return runOp(c, "project.rules.add", add, func(o any) {
				r := o.(*ops.RuleAddOut)
				what := "rule"
				if r.Rule.Promise {
					what = "promise"
				}
				if r.Created {
					fmt.Println("Added the " + what + " " + theirs(r.Rule.Text) + ".")
				} else {
					fmt.Println("The project already has the " + what + " " + theirs(r.Rule.Text) + ".")
				}
				if w := ops.MeasureWords(r.Rule.Measure); w != "" {
					fmt.Println("The quality check will check that " + w + ".")
				}
			})
		},
	}
	addCmd.Flags().BoolVar(&add.Check, "check", false, "a problem to check for in every quality check of the project")
	addCmd.Flags().BoolVar(&add.Promise, "promise", false, "a promise made to the client: every quality check measures it, and a version that breaks it waits before it goes out (it must be measurable)")
	addCmd.Flags().StringVar(&add.Measure, "measure", "", "what the quality check measures (default: recognized from the words): "+measures.Usage)
	remove := &cobra.Command{
		Use: "remove <project> <rule>", Short: "Take a rule out (its number or its words; taking back a promise is a person's)", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "project.rules.remove", ops.RuleRemoveIn{ProjectID: args[0], Rule: args[1]}, ops.Caller{Human: interactive()}, func(o any) {
				fmt.Println("Taken out.")
				show(c, o.(*ops.RulesOut))
			})
		},
	}
	listCmd := &cobra.Command{
		Use: "list <project>", Short: "List a project's rules", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "project.rules.list", ops.RulesListIn{ProjectID: args[0]}, func(o any) { show(c, o.(*ops.RulesOut)) })
		},
	}
	cmd.AddCommand(listCmd, addCmd, remove)
	return cmd
}
