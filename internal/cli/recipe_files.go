package cli

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/recipes"
)

// metarush recipe export|import (D219): a recipe as a file another workspace
// brings in — its document and sheet, the pictures it pins, the footage its
// cut plays, its approved code by hash — and bringing one in as a draft whose
// code waits for a person here.

func recipeExportCmd() *cobra.Command {
	var in recipes.RecipeExportIn
	c := &cobra.Command{
		Use:   "export <recipe> [file] [--version N] [--max-size MB] [--force]",
		Short: "Export a recipe to a file another workspace can bring in: its document and sheet, the pictures it pins, the footage its cut plays and its approved code by hash",
		Long: `Writes a version of a recipe (default: the current one, else the newest draft) to a recipe file,
by default <name>.recipe.zip here: recipe.json (the version, its history, its document and what each file
is), its sheet to read, and the files themselves by SHA-256 — the pictures, fonts and other resources it
pins, the footage its cut plays (with the fixes the cut pins applied), and the approved code it uses by
hash: motion graphics and their shared libraries, Remotion versions. Footage an input gives by default is
left out when the file would pass --max-size (2048 MB), the largest first; what doesn't come along is
listed with why. Where it is brought in (metarush recipe import), its words are data and its code waits for
a person's approval there.`,
		Example: "  metarush recipe export app-teaser\n  metarush recipe export app-teaser teaser.recipe.zip --version 3",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			in.Recipe = args[0]
			if len(args) == 2 {
				in.Out = args[1]
			}
			if in.Out != "" {
				if err := assistantPath(in.Out); err != nil {
					return fail(c, apperr.InvalidInput, err.Error(), "")
				}
			}
			return runOp(c, "recipe.export", in, func(o any) {
				out := o.(*recipes.RecipeExportOut)
				fmt.Println(out.Message)
				fmt.Printf("The file: %s (%s).\n", theirs(filepath.Base(out.File)), byteWords(out.Bytes))
				for _, x := range out.LeftOut {
					fmt.Printf("  · not in it: %s — %s\n", x.What, x.Why)
				}
				if detailsMode(c) {
					for _, cd := range out.Code {
						fmt.Printf("  code: %s %s@%s\n", cd.Kind, cd.Name, cd.Hash)
					}
					fmt.Println("file: " + out.File)
				}
				if out.Next != "" {
					fmt.Println(out.Next)
				}
			})
		},
	}
	c.Flags().StringVar(&in.Version, "version", "", "a version: its number, current or draft")
	c.Flags().IntVar(&in.MaxMB, "max-size", 0, "the most the file may hold, in MB (default 2048)")
	c.Flags().BoolVar(&in.Force, "force", false, "write over a file that is already there")
	return c
}

func recipeImportCmd() *cobra.Command {
	var in recipes.RecipeImportIn
	c := &cobra.Command{
		Use:   "import <file> [--project P] [--name short-name] [--note …] [--dry-run]",
		Short: "Bring in a recipe file another workspace exported, as a draft: its pictures, footage and code come into one project here, and its code waits for a person's approval",
		Long: `Brings a recipe file (metarush recipe export made it in another workspace) in as a draft: a new version
of the recipe it shares its history with here (one brought in before, or the one it came from), else a new
recipe under its own name (the next free one when the name is taken); --name makes it a new recipe with that
name. What it uses comes into one project (--project, else a new one named after the recipe, or the one an
earlier import of it used): pictures and footage keep where they came from, motion graphics, shared libraries
and Remotion versions are kept by hash and wait here — a check, then a person's approval — before anything
renders with them. Its words are another workspace's: a person here makes the version current. --dry-run
says what would come in and changes nothing.`,
		Example: "  metarush recipe import app-teaser.recipe.zip --dry-run\n  metarush recipe import app-teaser.recipe.zip --project \"Teasers\"",
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.File = args[0]
			return runOp(c, "recipe.import", in, func(o any) {
				out := o.(*recipes.RecipeImportOut)
				fmt.Println(out.Message)
				for _, cd := range out.Code {
					what := map[string]string{"motion": "the motion graphic", "library": "the shared library", "remotion": "the Remotion project"}[cd.Kind]
					line := fmt.Sprintf("  · %s %s: %s", what, theirs(cd.Name), cd.State)
					if cd.Next != "" && !cd.Ready {
						line += " — " + cd.Next
					}
					fmt.Println(line)
					if detailsMode(c) && cd.Command != "" {
						fmt.Println("    next: " + cd.Command)
					}
				}
				printList("", out.Notes)
				if out.Check != nil {
					printRecipeCheck(out.Check)
				}
				if out.Next != "" {
					fmt.Println(out.Next)
				}
			})
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project that keeps what comes with it (default: a new one named after the recipe)")
	c.Flags().StringVar(&in.Name, "name", "", "bring it in as a new recipe with this short name")
	c.Flags().StringVar(&in.Note, "note", "", "a note for the draft version")
	c.Flags().BoolVar(&in.DryRun, "dry-run", false, "say what would come in; change nothing")
	return c
}

// byteWords says a file's size plainly ("12 MB").
func byteWords(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", n>>20)
	}
	return fmt.Sprintf("%d KB", (n+1023)>>10)
}
