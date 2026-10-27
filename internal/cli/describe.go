package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/ops"
)

// describeCmd is `metarush footage describe …`: written scene descriptions by
// the person's own Claude Code (D74).
func describeCmd() *cobra.Command {
	var in assistant.DescribeIn
	var wait bool
	var save string
	cmd := &cobra.Command{
		Use:   "describe [<asset-id>]",
		Short: "Have Claude Code write a sentence or two per scene (searchable); uses your Claude Code usage",
		Long: "Your own Claude Code, on this machine, looks at the clip's smart contact sheet, frames and what's on screen and writes a sentence or two\n" +
			"per scene; MetaRush keeps them and search finds clips by them. It uses your Claude Code usage and is off unless turned on for the\n" +
			"project or the workspace (`footage describe settings`). With --save, descriptions Claude wrote in a session are saved instead\n" +
			"(a JSON array of {from, to, text}).",
		Example: "  metarush footage describe ast_… --wait\n  metarush footage describe --project prj_… --all\n  metarush footage describe ast_… --save scenes.json",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				in.AssetID = args[0]
			}
			if save != "" {
				var r io.Reader = os.Stdin
				if save != "-" {
					f, err := os.Open(save)
					if err != nil {
						return fail(c, apperr.InvalidInput, err.Error(), "")
					}
					defer f.Close()
					r = f
				}
				if err := json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&in.Scenes); err != nil {
					return fail(c, apperr.InvalidInput, "the scenes file is not a JSON array of {from, to, text}: "+err.Error(), "")
				}
				if len(in.Scenes) == 0 {
					return fail(c, apperr.InvalidInput, "the scenes file has no scenes", "")
				}
			}
			if !wait || save != "" {
				return runOp(c, "footage.describe", in, func(o any) { fmt.Println(o.(*assistant.DescribeOut).Summary) })
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			out, err := ops.CallTyped[assistant.DescribeOut](c.Context(), a, "footage.describe", in, ops.Caller{Human: interactive()})
			if err != nil {
				return failErr(c, err)
			}
			js, err := waitJobs(c, a, out.Jobs, 0, false)
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, map[string]any{"describe": out, "jobs": js}, func() {
				fmt.Println(out.Summary)
				done := 0
				for _, j := range js {
					if j.State == "succeeded" {
						done++
					} else if j.Error != "" {
						fmt.Println("Couldn't describe a clip. " + ops.JobAdvice(&j))
						if detailsMode(c) {
							fmt.Println("  error: " + j.Error)
						}
					}
				}
				if done > 0 {
					fmt.Printf("Described %s.\n", plural(done, "clip"))
				}
			})
		},
	}
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "with --all: every clip of this project without descriptions")
	cmd.Flags().BoolVar(&in.All, "all", false, "every clip of the project")
	cmd.Flags().BoolVar(&in.Force, "force", false, "describe again even when a clip has descriptions")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait until Claude Code has written them")
	cmd.Flags().StringVar(&save, "save", "", "save descriptions Claude wrote in a session (a JSON file, - for stdin) instead")

	var st assistant.DescribeConfigureIn
	var on, off bool
	settings := &cobra.Command{
		Use: "settings", Short: "Whether written scene descriptions are on (and turn them on or off for the workspace or a project)",
		RunE: func(c *cobra.Command, _ []string) error {
			show := func(o any) {
				s := o.(*assistant.DescribeSettingsOut)
				fmt.Println(s.Summary)
				fmt.Println(s.ClaudeText)
				fmt.Println(s.Note)
			}
			if !on && !off && !st.Inherit {
				return runOp(c, "footage.describe.settings", assistant.DescribeSettingsIn{ProjectID: st.ProjectID}, show)
			}
			if on && off {
				return fail(c, apperr.InvalidInput, "choose --on or --off", "")
			}
			if on || off {
				v := on
				st.On = &v
			}
			if !interactive() {
				return fail(c, apperr.HumanOnly, "turning scene descriptions on or off is done by a person (they use your Claude Code usage)",
					"run this yourself in a terminal, or use Settings in the dashboard")
			}
			if on && !askYes("Scene descriptions use your own Claude Code usage. Turn them on?", false) {
				return fail(c, apperr.Canceled, "not changed", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "footage.describe.configure", st, ops.Caller{Human: true}, show)
		},
	}
	settings.Flags().StringVar(&st.ProjectID, "project", "", "this project only (default: the workspace)")
	settings.Flags().BoolVar(&on, "on", false, "turn scene descriptions on")
	settings.Flags().BoolVar(&off, "off", false, "turn them off")
	settings.Flags().BoolVar(&st.Inherit, "inherit", false, "the project follows the workspace's choice again")
	cmd.AddCommand(settings)
	return cmd
}
