package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/recipes"
)

// metarush recipe triggers (D218): when a recipe plans a run by itself — new
// footage or pictures where an input takes them from, a weekly time — whom it
// tells and the plan waiting. Turning it on or off is a person's (people only,
// in their own terminal or on the recipe's page); it never starts a run.

func recipeTriggersCmd() *cobra.Command {
	var newFiles, weekly string
	var dismiss bool
	c := &cobra.Command{
		Use:   "triggers [<recipe>] [--new-files on|off] [--weekly \"monday 09:00\"|off] [--dismiss]",
		Short: "When recipes plan a run by themselves (new footage or pictures where an input takes them from, a weekly time) and tell you; they never start one. Changing it is people only",
		Long: `A recipe can plan a run by itself and ring your bell: when new footage or pictures arrive where one of its inputs
takes them from (a linked folder, a collection, a project's footage, a repository's folder), once they stop
arriving, or every week at a time. The plan waits on the recipe's page with its estimate; nothing starts until a
person runs it, whatever Auto or the estimate preference say. Without flags this shows the recipe's (or every
recipe's) settings, whom they tell and the computer that looks. --new-files, --weekly and --dismiss (set the plan
it made aside) change them: people only, and only the person they tell or an owner while they are on.`,
		Example: "  metarush recipe triggers\n  metarush recipe triggers event-recap --new-files on\n  metarush recipe triggers podcast --weekly \"friday 17:00\"",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			recipe := ""
			if len(args) == 1 {
				recipe = args[0]
			}
			changing := c.Flags().Changed("new-files") || c.Flags().Changed("weekly") || dismiss
			if !changing {
				return runOp(c, "recipe.triggers", recipes.RecipeTriggersIn{Recipe: recipe}, func(o any) { printTriggers(c, o.(*recipes.RecipeTriggersOut)) })
			}
			if recipe == "" {
				return fail(c, apperr.InvalidInput, "Say which recipe to change.", "")
			}
			in := recipes.RecipeTriggersSetIn{Recipe: recipe, Dismiss: dismiss}
			if c.Flags().Changed("new-files") {
				var on bool
				switch strings.ToLower(strings.TrimSpace(newFiles)) {
				case "on", "yes", "true":
					on = true
				case "off", "no", "false":
				default:
					return fail(c, apperr.InvalidInput, "--new-files is on or off.", "")
				}
				in.NewFiles = &on
			}
			if c.Flags().Changed("weekly") {
				in.Weekly = &weekly
			}
			if !interactive() || assistantMode() {
				return fail(c, apperr.HumanOnly, "Only a person turns planning by itself on or off, on the recipe's page or in their own terminal.", "Open the recipe on the Library's Recipes tab, or run this command yourself in a terminal.")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "recipe.triggers.set", in, ops.Caller{Human: true}, func(o any) {
				out := o.(*recipes.RecipeTriggersOut)
				fmt.Println(out.Message)
				if len(out.Recipes) == 1 && out.Message != out.Recipes[0].Words {
					fmt.Println(out.Recipes[0].Words)
				}
			})
		},
	}
	c.Flags().StringVar(&newFiles, "new-files", "", "on: plan a run when new footage or pictures arrive where an input takes them from; off: stop")
	c.Flags().StringVar(&weekly, "weekly", "", "plan a run every week: a day and a time (\"monday 09:00\", \"fri 5pm\"); off stops it")
	c.Flags().BoolVar(&dismiss, "dismiss", false, "set aside the plan it made (nothing had started)")
	return c
}

func printTriggers(c *cobra.Command, out *recipes.RecipeTriggersOut) {
	for _, r := range out.Recipes {
		fmt.Println(r.Words)
		if r.Next != nil {
			fmt.Printf("  Next weekly plan: %s\n", r.Next.Local().Format("Mon 2 Jan 15:04"))
		}
		if p := r.Prepared; p != nil {
			line := fmt.Sprintf("  Planned %s for %s: %s", p.At.Local().Format("2 Jan 15:04"), theirs(p.For.UserName), p.Words)
			if p.Ready {
				line += "; it waits for a person to run it (" + p.Estimate.About + ")."
			} else if len(p.Problems) > 0 {
				line += "; it can't start yet: " + p.Problems[0]
			}
			fmt.Println(line)
		}
		if detailsMode(c) {
			fmt.Printf("  name: %s  url: %s\n", r.Recipe, r.URL)
		}
	}
	if len(out.Recipes) != 1 {
		fmt.Println(out.Summary)
	}
}
