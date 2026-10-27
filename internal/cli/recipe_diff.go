package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/recipes"
)

// metarush recipe diff (D212): what changed between two versions of a recipe,
// in plain words — the same words the recipe's page shows for its versions
// and Claude's cards for a draft or a version made current.

func recipeDiffCmd() *cobra.Command {
	var in recipes.RecipeDiffIn
	c := &cobra.Command{
		Use:   "diff <recipe> [--to N|draft|current] [--from N|current]",
		Short: "Say what changed between two versions of a recipe: what changes each run, sections, rules and hints, and the title, the video, sound and graphics (default: the newest draft against the version it was made from)",
		Example: "  metarush recipe diff app-teaser                      # the newest draft against the version it was made from\n" +
			"  metarush recipe diff app-teaser --to 3 --from current  # what making version 3 current would change",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Recipe = args[0]
			return runOp(c, "recipe.diff", in, func(o any) {
				out := o.(*recipes.RecipeDiffOut)
				fmt.Println(out.Summary)
				printRecipeChanges(out.Parts, out.RemovedRules)
				if detailsMode(c) {
					from := "none"
					if out.From != nil {
						from = fmt.Sprintf("%d [%s]", out.From.Number, out.From.Short)
					}
					fmt.Printf("  versions: %d [%s] against %s\n", out.To.Number, out.To.Short, from)
				}
			})
		},
	}
	c.Flags().StringVar(&in.To, "to", "", "the version to look at: its number, current or draft (default: the newest draft, else the current one)")
	c.Flags().StringVar(&in.From, "from", "", "the version it is compared with (default: the one it was made from, else the one before it)")
	return c
}

// printRecipeChanges prints what changed, part by part, and the rules a
// version no longer keeps.
func printRecipeChanges(parts []recipes.RecipeChangePart, removed []string) {
	for _, p := range parts {
		fmt.Println(p.Label + ":")
		for _, ch := range p.Changes {
			fmt.Println("  - " + ch)
		}
	}
	if len(removed) > 0 {
		fmt.Printf("It no longer keeps %s: %s.\n", countWords(len(removed), "rule"), quotedList(removed))
	}
}

func quotedList(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ", "
		}
		out += "“" + x + "”"
	}
	return out
}
