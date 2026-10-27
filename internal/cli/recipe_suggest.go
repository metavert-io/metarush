package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/recipes"
)

// metarush recipe suggestions (D217): what people kept changing in a recipe's
// runs ("you shortened the intro in 4 of 5 runs"), each with why, and what a
// draft with them would change; --save makes that draft, which only a person
// makes current.

func recipeSuggestionsCmd() *cobra.Command {
	var in recipes.RecipeSuggestionsSaveIn
	var save bool
	c := &cobra.Command{
		Use:   "suggestions <recipe> [--only key]… [--save [--note …]]",
		Short: "Suggested changes to a recipe from what people changed in its runs (\"you shortened the intro in 4 of 5 runs\"), with what a draft would change; --save saves them as a draft a person makes current",
		Long: `MetaRush compares each approved run's version with the one the run made: a fixed section's clips made shorter,
longer or taken out, a chosen section's moments in all, the recipe's graphics (taken out, other words, shown for
longer or less), the captions, its music and sounds, its narration lines. A change people made in more than half
of the runs looked at (at least two) is a suggestion, with why. --save saves them as a draft (--only keeps some,
by the key --details shows); it is always a draft: a person makes it current.`,
		Example: "  metarush recipe suggestions app-teaser\n  metarush recipe suggestions app-teaser --save --note \"a shorter intro\"",
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Recipe = args[0]
			if save {
				return runOp(c, "recipe.suggestions.save", in, func(o any) {
					out := o.(*recipes.RecipeUpdateOut)
					fmt.Println(out.Message)
					printRecipeChanges(out.Changes, out.RemovedRules)
					printRecipeCheck(out.Check)
				})
			}
			return runOp(c, "recipe.suggestions", in.RecipeSuggestionsIn, func(o any) { printRecipeSuggestions(c, o.(*recipes.RecipeSuggestionsOut)) })
		},
	}
	c.Flags().StringSliceVar(&in.Only, "only", nil, "only these suggestions go into the draft (their keys, shown with --details)")
	c.Flags().BoolVar(&save, "save", false, "save the suggested changes as a draft; a person makes it current")
	c.Flags().StringVar(&in.Note, "note", "", "with --save: what the draft changes, in a few words")
	return c
}

func printRecipeSuggestions(c *cobra.Command, out *recipes.RecipeSuggestionsOut) {
	fmt.Println(out.Summary)
	for _, s := range out.Suggestions {
		line := "  - " + s.Reason
		if !s.InDraft {
			line += " (left out of the draft)"
		}
		if detailsMode(c) {
			line += " [" + s.Key + "]"
		}
		fmt.Println(line)
	}
	if len(out.Changes) > 0 {
		if out.Base != nil {
			fmt.Printf("A draft from version %d would change:\n", out.Base.Number)
		}
		printRecipeChanges(out.Changes, nil)
		printRecipeCheck(out.Check)
		fmt.Println("It is a suggestion: saved, it is a draft a person makes current.")
		if detailsMode(c) {
			fmt.Println("next: metarush recipe suggestions " + out.Recipe + " --save")
		}
	}
}
