package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/abilities"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/ops"
)

// abilityRunCmds are ability plan, run and runs (D234). A plan is read-only
// (Claude may show it). A free run that sends nothing but words starts from
// any terminal; anything else is started by a person at an interactive
// terminal (or the dashboard), a priced one within what they agree to.
func abilityRunCmds() []*cobra.Command {
	return []*cobra.Command{abilityPlanCmd(), abilityStartCmd(), abilityRunsCmd()}
}

// planFlags are the flags plan and run share.
func planFlags(c *cobra.Command, in *abilities.AbilityPlanIn, inputs *[]string) {
	c.Flags().StringArrayVar(inputs, "input", nil, "an input and its value: words=\"ocean waves\", count=3, clip=<clip's ID or title> (repeat)")
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project what it brings back goes into (default: the Inbox and the workspace library)")
}

func readInputs(c *cobra.Command, in *abilities.AbilityPlanIn, inputs []string) error {
	for _, kv := range inputs {
		k, v, ok := strings.Cut(kv, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return fail(c, apperr.InvalidInput, "--input is an input's name and its value, joined by an equals sign.", "")
		}
		if in.Inputs == nil {
			in.Inputs = map[string]any{}
		}
		in.Inputs[k] = v
	}
	return nil
}

func printAbilityPlan(p *abilities.AbilityPlanOut, details bool) {
	fmt.Println(p.Message)
	for _, in := range p.Inputs {
		switch {
		case in.Ref != "":
			fmt.Printf("  %s: %s\n", in.Name, theirs(in.Title))
		default:
			fmt.Printf("  %s: %s\n", in.Name, theirs(fmt.Sprint(in.Value)))
		}
	}
	fmt.Println("What it reaches: " + p.Network)
	for _, k := range p.Keys {
		fmt.Printf("  MetaRush adds the %s key in %s, only for %s.\n", k.Name, k.Place, strings.Join(k.Hosts, ", "))
	}
	if p.LocalWords != "" {
		fmt.Println(p.LocalWords)
		if details && p.LocalProgram != nil {
			fmt.Println("  program: " + p.LocalProgram.Path)
		}
	}
	fmt.Println("What leaves this computer: " + p.Leaves)
	fmt.Println("What it brings back: " + p.Makes + " " + p.Where)
	if p.Terms != "" {
		fmt.Println("The service's terms: " + theirs(p.Terms))
	}
	cost := p.Estimate.About
	if p.Estimate.USD > 0 {
		cost = "about " + costs.Money(p.Estimate.USD)
		for _, part := range p.Estimate.Parts {
			cost += fmt.Sprintf(" (%s: %s at %s)", part.Service, part.Units, part.Rate)
		}
	}
	fmt.Println("What it costs: " + cost)
	if p.Apart != "" {
		fmt.Println("Kept apart: " + p.Apart)
	}
	for _, pr := range p.Problems {
		fmt.Println("Can't start yet: " + pr)
	}
	if p.Ready {
		fmt.Println(p.DecidesWords)
	}
	if details {
		fmt.Printf("  version: %s (%d)\n  isolation: %s\n  plan: %s\n", p.Version, p.Number, p.Isolation, p.Fingerprint)
	}
}

func abilityPlanCmd() *cobra.Command {
	var in abilities.AbilityPlanIn
	var inputs []string
	var details bool
	c := &cobra.Command{
		Use:   "plan <name[@version]> [--input name=value …] [--project …]",
		Short: "Show what a run would do before anything runs: inputs, the estimate, places and keys, what leaves this computer, what it brings back, who decides",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Ref = args[0]
			if err := readInputs(c, &in, inputs); err != nil {
				return err
			}
			return runOp(c, "ability.plan", in, func(o any) { printAbilityPlan(o.(*abilities.AbilityPlanOut), details || detailsMode(c)) })
		},
	}
	planFlags(c, &in, &inputs)
	addDetails(c, &details)
	return c
}

func abilityStartCmd() *cobra.Command {
	var in abilities.AbilityRunIn
	var inputs []string
	var details, wait bool
	var maxCost float64
	c := &cobra.Command{
		Use:   "run <name[@version]> [--input name=value …] [--project …] [--max-cost USD] [--wait]",
		Short: "Run an approved ability on this computer: the plan is shown first; a person starts any run that costs money, sends pictures, sound or footage away, or changes something outside MetaRush",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Ref = args[0]
			if err := readInputs(c, &in.AbilityPlanIn, inputs); err != nil {
				return err
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			p, err := ops.CallTyped[abilities.AbilityPlanOut](c.Context(), a, "ability.plan", in.AbilityPlanIn, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			if !jsonMode(c) {
				printAbilityPlan(p, details || detailsMode(c))
			}
			if !p.Ready {
				return fail(c, apperr.Validation, "This run can't start yet.", strings.Join(p.Problems, " "))
			}
			human := interactive()
			if p.Decides != abilities.DecideSafe {
				if !human {
					return fail(c, apperr.HumanOnly, "Only a person starts this run, in the dashboard or in their own terminal.", p.DecidesWords)
				}
				if ask("Type \"run\" to start it", "") != "run" {
					return fail(c, apperr.Canceled, "nothing was started", "")
				}
			}
			in.Plan, in.MaxUSD = p.Fingerprint, maxCost
			if p.Decides == abilities.DecidePriced && maxCost <= 0 {
				in.MaxUSD = p.Estimate.USD // the person agreed to the estimate they just read
			}
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			out, err := ops.CallTyped[abilities.AbilityRunOut](ctx, a, "ability.run", in, ops.Caller{Human: human})
			if err != nil {
				return failErr(c, err)
			}
			if !wait {
				return emit(c, out, func() {
					fmt.Println(out.Message)
					fmt.Println("MetaRush running on this computer does it; follow it in the dashboard's activity, or wait for it with --wait.")
					if details {
						fmt.Println("  run:", out.ID, " job:", out.JobID)
					}
				})
			}
			if !jsonMode(c) {
				fmt.Println(out.Message)
			}
			return waitAndSay(c, a, out.JobID, details, out)
		},
	}
	planFlags(c, &in.AbilityPlanIn, &inputs)
	c.Flags().Float64Var(&maxCost, "max-cost", 0, "the most a priced run may cost, in US dollars (default: the estimate you are shown)")
	c.Flags().BoolVar(&wait, "wait", false, "wait until it is done")
	addDetails(c, &details)
	return c
}

func abilityRunsCmd() *cobra.Command {
	var in abilities.AbilityRunsIn
	var details bool
	c := &cobra.Command{
		Use:   "runs [name] [--id run]",
		Short: "List runs of abilities, or show one with the requests it made through MetaRush and what it printed",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				in.Name = args[0]
			}
			return runOp(c, "ability.runs", in, func(o any) {
				out := o.(*abilities.AbilityRunsOut)
				for _, r := range out.Runs {
					line := fmt.Sprintf("Run %d of %s: %s on %s, %s", r.Number, theirs(r.Title), r.StateLabel, r.Machine, r.CreatedAt.Local().Format("2 Jan 15:04"))
					if r.Summary != "" {
						line += ". " + r.Summary
					} else if r.Error != "" {
						line += ". " + r.Error
					}
					fmt.Println(line)
					for _, o := range r.Outputs {
						what := o.Status
						if o.Why != "" {
							what += ": " + o.Why
						}
						fmt.Printf("  %s (%s)\n", o.Path, what)
					}
					if details {
						fmt.Printf("  run: %s  job: %s\n", r.ID, r.JobID)
					}
				}
				if out.Run != nil {
					if len(out.Requests) > 0 {
						fmt.Println("Requests it made through MetaRush:")
						for _, q := range out.Requests {
							line := fmt.Sprintf("  %s %s%s", q.Method, q.Host, q.Path)
							if q.Refused != "" {
								line += " — refused: " + q.Refused
							} else if q.Status > 0 {
								line += fmt.Sprintf(" (%d)", q.Status)
							}
							if len(q.Keys) > 0 {
								line += " with the " + strings.Join(q.Keys, ", ") + " key"
							}
							fmt.Println(line)
						}
					}
					if out.Log != "" {
						fmt.Println("What it printed:")
						fmt.Println(indent(out.Log, "    "))
					}
				}
				fmt.Println(out.Message)
			})
		},
	}
	c.Flags().StringVar(&in.ID, "id", "", "one run: its requests and what it printed too")
	c.Flags().IntVar(&in.Limit, "limit", 0, "at most this many (default 20)")
	addDetails(c, &details)
	return c
}
