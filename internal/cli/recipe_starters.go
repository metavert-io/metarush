package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/recipes"
)

// metarush recipe starters [--add] (D293): the starter recipes MetaRush comes
// with, brought in as drafts a person makes current; metarush recipe sample
// (D294): approving a run's sample — or saying it isn't right — before the
// rest of the run is planned.

func recipeStartersCmd() *cobra.Command {
	var add bool
	var only []string
	c := &cobra.Command{
		Use:   "starters [--add [--only call-episode]…]",
		Short: "The starter recipes MetaRush comes with (a call becomes an episode with shorts; a release tag becomes a release video); --add brings them in as drafts",
		Long: `Lists the starter recipes MetaRush comes with: what each makes, what a run gives it, the abilities
its runs use and whether this workspace has it. --add brings them (or those --only names) in as drafts,
with their example footage and pictures in the project "Starter recipes": a person looks at each one and
makes it current before it runs. Adding again brings in only a newer version of a starter (a draft of the
recipe it came in as).`,
		Example: "  metarush recipe starters\n  metarush recipe starters --add\n  metarush recipe starters --add --only release-video",
		Args:    cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if !add {
				if len(only) > 0 {
					return fail(c, apperr.InvalidInput, "--only goes with --add.", "")
				}
				return runOp(c, "recipe.starters", recipes.RecipeStartersIn{}, func(o any) {
					out := o.(*recipes.RecipeStartersOut)
					fmt.Println(out.Summary)
					for _, s := range out.Starters {
						fmt.Printf("\n%s — %s\n", theirs(s.Title), s.Words)
						fmt.Println("  Makes " + s.Makes + ".")
						fmt.Println("  A run gives it " + strings.Join(s.Gives, ", ") + ".")
						fmt.Println("  " + s.Summary)
						if detailsMode(c) {
							fmt.Printf("  name: %s  version: %d  uses: %s\n", s.Name, s.Version, strings.Join(s.Uses, ", "))
						}
					}
					if out.Summary != "Every starter recipe is here." {
						fmt.Println("\nAdd them as drafts: metarush recipe starters --add")
					}
				})
			}
			return runOp(c, "recipe.starters.add", recipes.RecipeStartersAddIn{Only: only}, func(o any) {
				out := o.(*recipes.RecipeStartersAddOut)
				fmt.Println(out.Message)
				for _, x := range out.Added {
					fmt.Println("  " + x.Message)
					if detailsMode(c) && x.URL != "" {
						fmt.Println("    url: " + x.URL)
					}
				}
				if out.Next != "" {
					fmt.Println(out.Next)
				}
			})
		},
	}
	c.Flags().BoolVar(&add, "add", false, "bring the starter recipes in as drafts")
	c.Flags().StringArrayVar(&only, "only", nil, "only this starter (its short name; repeat for more)")
	return c
}

func recipeSampleCmd() *cobra.Command {
	var in recipes.RecipeSampleIn
	var approve bool
	var notQuite string
	var moreCost float64
	var wait bool
	c := &cobra.Command{
		Use:   "sample <run> (--approve [--more-cost N] | --not-quite \"what isn't right\") [--wait]",
		Short: "Approve a run's sample — the rest of the run is planned then — or say what isn't right (the run stops); people only",
		Long: `A run whose estimate has an expensive kind of work (the AI voice, generated shots, voices made clearer
in the cloud) makes a sample first: a short line in the AI voice, one shot, a stretch of the footage made
clearer, in a short cut of its own with a review copy. Watch and listen to it, then --approve it: the rest
of the run is planned with today's prices and asks you to agree to what it costs (--more-cost agrees at
once, when the rest costs no more). --not-quite stops the run, keeping what you said.`,
		Example: "  metarush recipe sample call-episode#3 --approve\n  metarush recipe sample call-episode#3 --not-quite \"the voices sound thin\"",
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Run = args[0]
			switch {
			case approve && notQuite != "":
				return fail(c, apperr.InvalidInput, "Approve the sample or say it isn't quite right, not both.", "")
			case approve:
				in.Decision = "approve"
			case notQuite != "":
				in.Decision, in.Note = "not-quite", notQuite
			default:
				return fail(c, apperr.InvalidInput, "Say --approve, or --not-quite with what isn't right.", "")
			}
			if !interactive() || assistantMode() {
				return fail(c, apperr.HumanOnly, "Only a person approves a run's sample, after watching and listening to it — in the dashboard or in their own terminal.",
					"Open the run on the Library's Recipes tab, or run this command yourself in a terminal.")
			}
			in.MoreUSD = flagValue(c, "more-cost", moreCost)
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			v := recipesRunView(c, a, in.Run)
			if v == nil {
				return fail(c, apperr.NotFound, "There's no such run.", "")
			}
			printRecipeRun(c, v)
			word := "approve"
			if in.Decision == "not-quite" {
				word = "stop"
			}
			if ask(fmt.Sprintf("Type %q to %s", word, map[string]string{"approve": "approve the sample (the rest is planned then)", "stop": "stop the run"}[word]), "") != word {
				return fail(c, apperr.Canceled, "Nothing changed.", "")
			}
			out, err := ops.CallTyped[recipes.RecipeSampleOut](c.Context(), a, "recipe.sample", in, ops.Caller{Human: true})
			if err != nil {
				return failErr(c, err)
			}
			if !wait || out.Run.State == models.RunStopped || out.Run.WaitingFor == models.RunWaitsForPerson {
				return emit(c, out, func() {
					fmt.Println(out.Message)
					if out.Next != "" {
						fmt.Println(out.Next)
					}
				})
			}
			if !jsonMode(c) {
				fmt.Println(out.Message)
			}
			return followRun(c, a, out.Run.ID)
		},
	}
	c.Flags().BoolVar(&approve, "approve", false, "approve the sample: the rest of the run is planned")
	c.Flags().StringVar(&notQuite, "not-quite", "", "what isn't right about the sample (the run stops)")
	c.Flags().Float64Var(&moreCost, "more-cost", 0, "agree at once to spend up to this much on the rest, in US dollars")
	c.Flags().BoolVar(&wait, "wait", false, "follow the run here afterwards")
	return c
}

// printSamplePlan says what a plan's sample is (D294).
func printSamplePlan(c *cobra.Command, sp *recipes.RecipeSamplePlan) {
	if sp == nil {
		return
	}
	fmt.Println("A sample first (" + sp.Estimate.About + "): you watch and listen to it and approve it before the rest is planned.")
	for _, k := range sp.Kinds {
		line := "  · " + k.What
		if k.Source != "" {
			line += " — " + k.Source
		}
		line += fmt.Sprintf(" (%s; this kind costs %s in the whole run)", costs.About(k.USD), costs.About(k.RunUSD))
		fmt.Println(line)
	}
	if detailsMode(c) {
		writeCost(c.OutOrStdout(), "  ", sp.Estimate, true)
	}
}

// printRunSample says where a run's sample stands (D294).
func printRunSample(v *recipes.RecipeRunView) {
	if v.SampleWords != "" {
		fmt.Println("  " + v.SampleWords)
	}
}
