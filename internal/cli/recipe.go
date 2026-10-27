package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/recipes"
)

// metarush recipe … (D201–D207, docs/recipes.md): save a video that worked,
// look at recipes and their versions, plan a run (the estimate first), start
// it (people only), make it again with changes, follow and compare runs.

func recipeCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "recipe",
		Short: "Recipes: videos that worked, written down so they can be made again — save one from a cut, plan a run with its estimate, start it, make it again with changes",
		Long: `A recipe is a video that worked, written down: what changes each time (its inputs: new footage, pictures,
words, a voice), what stays (sections, graphics, music, the look) and the rules that made it good. A run makes
a new project, or a new cut in the project it was saved from, through the ordinary steps: versions, a review
copy and the quality check you review and approve as always. Planning a run says what it costs first; only a
person starts one.`,
	}
	c.AddCommand(recipeListCmd(), recipeShowCmd(), recipeProposeCmd(), recipeSaveCmd(), recipeUpdateCmd(), recipeCheckoutCmd(), recipeCheckCmd(), recipePublishCmd(), recipeArchiveCmd(),
		recipePlanCmd(), recipeRunCmd(), recipeAgainCmd(), recipeRunsCmd(), recipePicksCmd(), recipeCompareCmd(), recipeContinueCmd(), recipeStopCmd())
	c.AddCommand(recipeDiffCmd())                        // D212
	c.AddCommand(recipeSuggestionsCmd())                 // D217
	c.AddCommand(recipeTriggersCmd())                    // D218
	c.AddCommand(recipeExportCmd(), recipeImportCmd())   // D219
	c.AddCommand(recipeStartersCmd(), recipeSampleCmd()) // D293, D294
	return c
}

func recipeListCmd() *cobra.Command {
	var in recipes.RecipeListIn
	c := &cobra.Command{
		Use:   "list [--archived] [--project P] [--query words]",
		Short: "List recipes: what each makes, its inputs and its last runs",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "recipe.list", in, func(o any) {
				out := o.(*recipes.RecipeListOut)
				for _, r := range out.Recipes {
					printRecipeRow(c, &r)
				}
				fmt.Println(out.Summary)
			})
		},
	}
	c.Flags().BoolVar(&in.Archived, "archived", false, "archived recipes too")
	c.Flags().StringVar(&in.ProjectID, "project", "", "only recipes saved from this project")
	c.Flags().StringVar(&in.Query, "query", "", "words in a recipe's name or title")
	return c
}

func printRecipeRow(c *cobra.Command, r *recipes.RecipeRow) {
	line := theirs(r.Title) + " — " + r.Makes
	switch {
	case r.Archived:
		line += " (archived)"
	case r.Current == 0:
		line += " (a draft: a person makes it current before it runs)"
	case r.Draft > 0:
		line += fmt.Sprintf(" (version %d; draft %d waits for a person)", r.Current, r.Draft)
	default:
		line += fmt.Sprintf(" (version %d)", r.Current)
	}
	fmt.Println(line)
	if r.Lands != "" {
		fmt.Println("  Each run makes " + r.Lands + ".")
	}
	if r.Planned {
		fmt.Println("  It planned a run by itself: it waits for a person to run it.")
	} else if r.PlansByItself {
		fmt.Println("  It plans runs by itself and tells someone; it never starts one.")
	}
	var ins []string
	for _, in := range r.Inputs {
		ins = append(ins, in.Name+" ("+recipeKindName(in.Kind)+")")
	}
	if len(ins) > 0 {
		fmt.Println("  Inputs: " + strings.Join(ins, ", "))
	}
	for _, run := range r.LastRuns {
		fmt.Printf("  Run %d, %s by %s: %s%s\n", run.Number, run.At.Local().Format("2 Jan 15:04"), run.By, strings.ToLower(run.StateLabel[:1])+run.StateLabel[1:], approvedWords(run.Approved))
	}
	if detailsMode(c) {
		fmt.Printf("  name: %s  id: %s  url: %s\n", r.Name, r.ID, r.URL)
	}
}

func approvedWords(ok bool) string {
	if ok {
		return " (approved)"
	}
	return ""
}

func recipeKindName(k string) string {
	switch k {
	case models.RecipeWords:
		return "words"
	case models.RecipeChoice:
		return "a choice"
	case models.RecipeFootage:
		return "footage"
	case models.RecipePictures:
		return "pictures"
	case models.RecipeVoice:
		return "a voice"
	}
	return k
}

func recipeShowCmd() *cobra.Command {
	var in recipes.RecipeRefIn
	var doc bool
	c := &cobra.Command{
		Use:   "show <recipe> [--version N] [--doc]",
		Short: "Show a recipe: its inputs (type, default, limits), sections, sound, rules, hints, versions, check and runs",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Recipe = args[0]
			return runOp(c, "recipe.show", in, func(o any) {
				out := o.(*recipes.RecipeShowOut)
				if doc {
					b, _ := json.MarshalIndent(out.Doc, "", "  ")
					fmt.Println(string(b))
					return
				}
				fmt.Print(out.Readable)
				fmt.Println("## Versions")
				for _, v := range out.Versions {
					line := fmt.Sprintf("- %d: %s, by %s, %s", v.Number, versionStateWords(v.State), v.By, v.At.Local().Format("2 Jan 15:04"))
					if v.Note != "" {
						line += " — " + theirs(v.Note)
					}
					if !v.OK {
						line += " (its check found problems)"
					}
					if detailsMode(c) {
						line += " [" + v.Short + "]"
					}
					fmt.Println(line)
				}
				printRecipeCheck(out.Version.Check)
				if len(out.Runs) > 0 {
					fmt.Println("## Runs")
					for _, r := range out.Runs {
						fmt.Printf("- Run %d, %s by %s: %s; spent %s%s\n", r.Number, r.At.Local().Format("2 Jan 15:04"), r.By, strings.ToLower(r.StateLabel[:1])+r.StateLabel[1:], r.SpentWords, approvedWords(r.Approved))
					}
				}
			})
		},
	}
	c.Flags().StringVar(&in.Version, "version", "", "a version: its number, current or draft")
	c.Flags().BoolVar(&doc, "doc", false, "print the version's document (what update's patches change)")
	return c
}

func versionStateWords(s string) string {
	switch s {
	case models.RecipeCurrent:
		return "current"
	case models.RecipeDraft:
		return "draft"
	}
	return "earlier"
}

func printRecipeCheck(chk *models.RecipeCheck) {
	if chk == nil {
		return
	}
	switch {
	case !chk.OK:
		fmt.Printf("Its check found %s:\n", countWords(len(chk.Problems), "problem"))
		for _, p := range chk.Problems {
			fmt.Println("  - " + p.Message)
		}
	case len(chk.Warnings) > 0:
		fmt.Printf("Its check passed, with %s to look at:\n", countWords(len(chk.Warnings), "thing"))
	default:
		fmt.Printf("Its check passed: with its own inputs it makes a video of %s.\n", fmtDur(chk.Duration))
	}
	for _, w := range chk.Warnings {
		fmt.Println("  - " + w.Message)
	}
}

func recipeProposeCmd() *cobra.Command {
	var in recipes.RecipeSaveIn
	c := &cobra.Command{
		Use:   "propose --from <project> [--branch b] [--version N] [--vary key]… [--keep key]… [--fixed section]… [--chosen section]…",
		Short: "Show what saving a project's cut as a recipe would vary each run and keep (nothing is saved)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "recipe.propose", in, func(o any) { printRecipeProposal(c, o.(*recipes.RecipeSaveOut)) })
		},
	}
	recipeSaveFlags(c, &in)
	return c
}

func printRecipeProposal(c *cobra.Command, out *recipes.RecipeSaveOut) {
	fmt.Println(out.Message)
	var varies, stays []string
	for _, it := range out.Proposal {
		line := "  " + it.What
		if it.Why != "" {
			line += " — " + it.Why
		}
		if detailsMode(c) && it.Movable {
			line += " [" + it.Key + "]"
		}
		if it.Varies {
			varies = append(varies, line)
		} else {
			stays = append(stays, line)
		}
	}
	if len(varies) > 0 {
		fmt.Println("Changes each run:")
		fmt.Println(strings.Join(varies, "\n"))
	}
	if len(stays) > 0 {
		fmt.Println("Stays the same:")
		fmt.Println(strings.Join(stays, "\n"))
	}
	printRecipeCheck(out.Check)
	if out.DryRun && !detailsMode(c) {
		fmt.Println("(Add --details to see the name each item moves by.)")
	}
}

func recipeSaveFlags(c *cobra.Command, in *recipes.RecipeSaveIn) {
	c.Flags().StringVar(&in.From, "from", "", "the project whose cut it is (ID or name)")
	c.Flags().StringVar(&in.Branch, "branch", "", "which cut (default: the main one)")
	c.Flags().StringVar(&in.Version, "version", "", "which version of the cut (default: its newest approved one)")
	c.Flags().StringVar(&in.Title, "title", "", "its title (default: the cut's name)")
	c.Flags().BoolVar(&in.NewProjects, "new-projects", false, "every run makes a project of its own")
	c.Flags().StringSliceVar(&in.Vary, "vary", nil, "a proposal item to change each run")
	c.Flags().StringSliceVar(&in.Keep, "keep", nil, "a proposal input to keep the same every run")
	c.Flags().StringSliceVar(&in.Fixed, "fixed", nil, "a section MetaRush builds the same every run")
	c.Flags().StringSliceVar(&in.Chosen, "chosen", nil, "a section of footage Claude chooses again each run")
	c.Flags().StringSliceVar(&in.Pack, "pack", nil, "a pack every run also makes (shorts: of the conversation the cut plays, each its own project)")
	c.Flags().StringArrayVar(&in.Hints, "hint", nil, "a hint that guides Claude")
	_ = c.MarkFlagRequired("from")
}

func recipeSaveCmd() *cobra.Command {
	var in recipes.RecipeSaveIn
	c := &cobra.Command{
		Use:   "save --from <project> [--name app-teaser] [--title …] [--vary key]… [--keep key]… [--fixed section]… [--chosen section]… [--dry-run]",
		Short: "Save a project's cut as a recipe: what varies (its footage, words in graphics, pictures, the voice) and what stays are proposed; --dry-run shows the proposal",
		Long: `Save a cut that worked as a recipe. MetaRush reads its approved version (else its newest) and proposes what
varies each run — the project's own footage (Claude chooses the moments again), words in graphics, word
settings of clips made from code, pictures that aren't the brand's, the AI voice — and what stays: the brand
through Content Settings, music, intros and outros, captions, the mix, rules and sign-offs. --dry-run shows the
proposal; --vary and --keep move inputs, --fixed and --chosen move sections. Runs add new cuts to this project,
or make a project each with --new-projects. When a person saves it in a terminal, version 1 is current; Claude's
save is a draft a person makes current.`,
		Example: "  metarush recipe save --from \"App teaser\" --dry-run\n  metarush recipe save --from \"App teaser\" --keep footage --vary title",
		Args:    cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			if in.DryRun {
				return runOpWith(c, a, "recipe.propose", in, ops.Caller{}, func(o any) { printRecipeProposal(c, o.(*recipes.RecipeSaveOut)) })
			}
			return runOpWith(c, a, "recipe.save", in, ops.Caller{Human: interactive() && !assistantMode()}, func(o any) {
				out := o.(*recipes.RecipeSaveOut)
				fmt.Println(out.Message)
				printRecipeCheck(out.Check)
			})
		},
	}
	recipeSaveFlags(c, &in)
	c.Flags().StringVar(&in.Name, "name", "", "its short name (default: from its title)")
	c.Flags().StringVar(&in.Note, "note", "", "a few words about this version")
	c.Flags().BoolVar(&in.DryRun, "dry-run", false, "show the proposal; save nothing (as recipe propose)")
	c.Flags().BoolVar(&in.Draft, "draft", false, "keep version 1 a draft")
	_ = c.MarkFlagRequired("from")
	return c
}

func recipeUpdateCmd() *cobra.Command {
	var in recipes.RecipeUpdateIn
	var patch string
	var fromDrive, fromDropbox []string
	c := &cobra.Command{
		Use:   "update <recipe> [--patch file.json|-] [--title …] [--hint …]… [--rule …]… [--from-drive|--from-dropbox input=remote:path]… [--note …] [--dry-run]",
		Short: "Make a draft version of a recipe with changes: a merge patch over its document (recipe show --doc), a title, hints or rules to add, an input that takes its files from a Google Drive folder; a person makes it current (--dry-run says what it would change)",
		Long: `Makes a draft version of a recipe (a person makes it current). --from-drive art=gdrive:Teaser/Art makes the
pictures or footage input "art" take the newest files of a Google Drive folder each run, read through your own
rclone without syncing the folder: the plan lists it, the run fetches only what it uses. Set rclone up
first with its own setup (rclone config: add a Google Drive and sign in); --from-drive art= takes it away.
--from-dropbox does the same with a Dropbox folder.`,
		Example: "  metarush recipe update app-teaser --hint \"Name the app in the first two seconds\"\n  metarush recipe update app-teaser --from-drive art=gdrive:Teaser/Character\\ art --dry-run",
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Recipe = args[0]
			for _, kv := range fromDropbox {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || strings.TrimSpace(k) == "" {
					return fail(c, apperr.InvalidInput, "Give --from-dropbox as input=remote:path, like art=dropbox:Teaser/Art.", "")
				}
				if in.FromDropbox == nil {
					in.FromDropbox = map[string]string{}
				}
				in.FromDropbox[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
			for _, kv := range fromDrive {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || strings.TrimSpace(k) == "" {
					return fail(c, apperr.InvalidInput, "Give --from-drive as input=remote:path, like art=gdrive:Teaser/Art.", "")
				}
				if in.FromDrive == nil {
					in.FromDrive = map[string]string{}
				}
				in.FromDrive[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
			if patch != "" {
				raw, err := readPatch(patch)
				if err != nil {
					return fail(c, apperr.InvalidInput, "The patch can't be read: "+err.Error(), "")
				}
				in.Patch = raw
			}
			return runOp(c, "recipe.update", in, func(o any) {
				out := o.(*recipes.RecipeUpdateOut)
				fmt.Println(out.Message)
				printRecipeChanges(out.Changes, out.RemovedRules)
				printRecipeCheck(out.Check)
			})
		},
	}
	c.Flags().BoolVar(&in.DryRun, "dry-run", false, "say what the draft would change; save nothing")
	c.Flags().StringVar(&in.Version, "version", "", "the version it starts from (default: the newest draft, else the current one)")
	c.Flags().StringVar(&patch, "patch", "", "a JSON merge patch over the document (a file, or - for stdin)")
	c.Flags().StringVar(&in.Title, "title", "", "a new title")
	c.Flags().StringArrayVar(&in.AddHints, "hint", nil, "a hint to add")
	c.Flags().StringArrayVar(&in.AddRules, "rule", nil, "a rule to add")
	c.Flags().StringArrayVar(&fromDrive, "from-drive", nil, "input=remote:path: the input takes the newest files of a Google Drive folder through your rclone (empty takes it away)")
	c.Flags().StringArrayVar(&fromDropbox, "from-dropbox", nil, "input=remote:path: the input takes the newest files of a Dropbox folder through your rclone (empty takes it away)")
	c.Flags().StringVar(&in.Note, "note", "", "what the draft changes, in a few words")
	return c
}

func readPatch(p string) (json.RawMessage, error) {
	var r io.Reader = os.Stdin
	if p != "-" {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, 4<<20))
	if err != nil {
		return nil, err
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("it isn't JSON")
	}
	return b, nil
}

// isSheetArg reports whether a command's argument is a recipe sheet (a file,
// or - for stdin) rather than a recipe's name.
func isSheetArg(arg string) bool {
	if arg == "-" || strings.HasSuffix(arg, ".md") {
		return true
	}
	st, err := os.Stat(arg)
	return err == nil && st.Mode().IsRegular()
}

func recipeCheckoutCmd() *cobra.Command {
	var in recipes.RecipeRefIn
	var force bool
	c := &cobra.Command{
		Use:   "checkout <recipe> [file|-] [--version N] [--force]",
		Short: "Write a recipe version as its sheet (Markdown: inputs, sections, the pack, rules, hints and the cut) to read, edit and keep in git; recipe check reads it back",
		Long: `Writes a version of a recipe (default: the current one, else the newest draft) as its recipe sheet, by default
to <name>.recipe.md here (- prints it). Edit it, then 'metarush recipe check <file>' says what is wrong and what it
changes, and 'metarush recipe check <file> --save' saves it as a draft version a person makes current. Printing and
reading a sheet keep the version exactly as it is. A sheet with a new name: makes a new recipe.`,
		Example: "  metarush recipe checkout app-teaser\n  metarush recipe checkout app-teaser teaser.recipe.md --version 3",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			in.Recipe = args[0]
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			out, err := ops.CallTyped[recipes.RecipeCheckoutOut](c.Context(), a, "recipe.checkout", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			file := out.File
			if len(args) == 2 {
				file = args[1]
			}
			if file == "-" {
				if jsonMode(c) {
					return emit(c, out, func() {})
				}
				fmt.Print(out.Sheet)
				return nil
			}
			if err := assistantPath(file); err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			if old, err := os.ReadFile(file); err == nil && string(old) != out.Sheet && !force {
				return fail(c, apperr.Conflict, sheetName(file)+" is already there with other words in it; nothing was written.", "Check that one (it may have changes not saved yet), or write over it with --force.")
			}
			if err := os.WriteFile(file, []byte(out.Sheet), 0o644); err != nil {
				return fail(c, apperr.InvalidInput, "can't write "+sheetName(file)+": "+err.Error(), "")
			}
			return emit(c, struct {
				*recipes.RecipeCheckoutOut
				Written string `json:"written"`
			}{out, file}, func() {
				fmt.Printf("Wrote version %d of %s to %s.\n", out.Version.Number, theirs(strings.TrimSpace(out.Name)), sheetName(file))
				fmt.Println("Edit it, then check it (metarush recipe check " + file + ") and save it as a draft (--save).")
			})
		},
	}
	c.Flags().StringVar(&in.Version, "version", "", "a version: its number, current or draft")
	c.Flags().BoolVar(&force, "force", false, "write over a file that has other words in it")
	return c
}

// printSheetCheck says whether a recipe sheet reads, what it changes and what its check found.
func printSheetCheck(c *cobra.Command, file string, out *recipes.RecipeCheckOut) {
	fmt.Println(out.Message)
	if out.Valid && !out.Same {
		printList("", out.Changes)
	}
	printProblems(file, out.Problems, detailsMode(c))
	if out.Valid {
		chk := out.Check
		printRecipeCheck(&chk)
	}
	if detailsMode(c) && out.Valid && !out.Same {
		fmt.Println("next: metarush recipe check " + file + " --save")
	}
}

func recipeCheckCmd() *cobra.Command {
	var in recipes.RecipeCheckIn
	var docFile string
	var save, write bool
	var note string
	c := &cobra.Command{
		Use:   "check [<recipe> | <file.recipe.md> | -] [--version N] [--doc file.json] [--save [--note …] [--write]]",
		Short: "Check a recipe or a recipe sheet: its inputs and where they go, approvals, a dry assembly with its own inputs, and what a sheet changes (nothing is saved); --save saves a sheet as a draft",
		Long: `Checks a recipe version (by name), a document (--doc), or a recipe sheet (a file from 'metarush recipe
checkout', or - for stdin): a sheet's problems by line, what it changes compared with the version it was written
from, and the recipe's own check. --save saves the sheet as a draft — a new version of the recipe it names, or a new
recipe — that only a person makes current; --write then rewrites the file as the saved version.`,
		Example: "  metarush recipe check app-teaser\n  metarush recipe check app-teaser.recipe.md\n  metarush recipe check app-teaser.recipe.md --save --note \"a shorter intro\"",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 && isSheetArg(args[0]) {
				text, err := readSheet(args[0])
				if err != nil {
					return fail(c, apperr.InvalidInput, err.Error(), "")
				}
				if !save {
					return runOp(c, "recipe.check", recipes.RecipeCheckIn{Sheet: text}, func(o any) { printSheetCheck(c, args[0], o.(*recipes.RecipeCheckOut)) })
				}
				return runOp(c, "recipe.write", recipes.RecipeWriteIn{Sheet: text, Note: note}, func(o any) {
					out := o.(*recipes.RecipeWriteOut)
					fmt.Println(out.Message)
					printList("", out.Changes)
					var notes []cutsheet.Problem
					for _, p := range out.Problems {
						if p.Severity != "error" {
							notes = append(notes, p)
						}
					}
					printProblems(args[0], notes, detailsMode(c))
					printRecipeCheck(out.Check)
					if write && args[0] != "-" && !out.Same {
						if err := os.WriteFile(args[0], []byte(out.Sheet), 0o644); err == nil {
							fmt.Printf("Updated %s to version %d.\n", sheetName(args[0]), out.Version.Number)
						}
					} else if !out.Same && args[0] != "-" {
						fmt.Printf("Your copy of %s still says it was written from the version before; check the recipe out again before editing more.\n", sheetName(args[0]))
					}
				})
			}
			if save {
				return fail(c, apperr.InvalidInput, "--save saves a recipe sheet; give the sheet's file.", "Write one with metarush recipe checkout <recipe>.")
			}
			if len(args) == 1 {
				in.Recipe = args[0]
			}
			if docFile != "" {
				raw, err := readPatch(docFile)
				if err != nil {
					return fail(c, apperr.InvalidInput, "The recipe document can't be read: "+err.Error(), "")
				}
				in.Doc = raw
			}
			return runOp(c, "recipe.check", in, func(o any) {
				out := o.(*recipes.RecipeCheckOut)
				fmt.Println(out.Message)
				chk := out.Check
				printRecipeCheck(&chk)
				for _, s := range chk.Stills {
					if detailsMode(c) {
						fmt.Printf("  still of “%s” at %s: %s\n", s.Section, fmtDur(s.Timeline), s.URL)
					}
				}
			})
		},
	}
	c.Flags().StringVar(&in.Version, "version", "", "a version: its number, current or draft")
	c.Flags().StringVar(&docFile, "doc", "", "a recipe document to check (a file, or - for stdin)")
	c.Flags().BoolVar(&save, "save", false, "save the sheet as a draft (a new version, or a new recipe); a person makes it current")
	c.Flags().StringVar(&note, "note", "", "with --save: what the draft changes, in a few words")
	c.Flags().BoolVar(&write, "write", false, "with --save: rewrite the file as the saved version")
	return c
}

func recipePublishCmd() *cobra.Command {
	var in recipes.RecipeRefIn
	c := &cobra.Command{
		Use:   "publish <recipe> [--version N]",
		Short: "Make a version of a recipe current — the one runs make (people only; default: the newest draft)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Recipe = args[0]
			if !interactive() || assistantMode() {
				return fail(c, apperr.HumanOnly, "Only a person makes a version of a recipe current, in the dashboard or in their own terminal.", "Open the recipe on the Library's Recipes tab, or run this command yourself in a terminal.")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			show, err := ops.CallTyped[recipes.RecipeShowOut](c.Context(), a, "recipe.show", recipes.RecipeRefIn{Recipe: in.Recipe, Version: orDraft(in.Version)}, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			fmt.Print(show.Readable)
			printRecipeCheck(show.Version.Check)
			// What it changes from the version runs make now (D212).
			if show.Recipe.Current > 0 && show.Recipe.Current != show.Version.Number {
				if d, err := ops.CallTyped[recipes.RecipeDiffOut](c.Context(), a, "recipe.diff", recipes.RecipeDiffIn{Recipe: in.Recipe, To: fmt.Sprint(show.Version.Number), From: "current"}, ops.Caller{}); err == nil {
					fmt.Println(d.Summary)
					printRecipeChanges(d.Parts, d.RemovedRules)
				}
			}
			if ask(fmt.Sprintf("Type \"current\" to make version %d of %s current", show.Version.Number, theirs(show.Recipe.Title)), "") != "current" {
				return fail(c, apperr.Canceled, "Nothing changed.", "")
			}
			in.Version = fmt.Sprint(show.Version.Number)
			return runOpWith(c, a, "recipe.publish", in, ops.Caller{Human: true}, func(o any) {
				fmt.Println(o.(*recipes.RecipePublishOut).Message)
			})
		},
	}
	c.Flags().StringVar(&in.Version, "version", "", "a version: its number (default: the newest draft)")
	return c
}

func orDraft(v string) string {
	if v == "" {
		return "draft"
	}
	return v
}

func recipeArchiveCmd() *cobra.Command {
	var in recipes.RecipeArchiveIn
	c := &cobra.Command{
		Use:   "archive <recipe> [--restore]",
		Short: "Archive a recipe (it leaves the Recipes tab and can't run; versions and runs stay), or restore it — people only",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Recipe = args[0]
			if !interactive() || assistantMode() {
				return fail(c, apperr.HumanOnly, "Only a person archives or restores a recipe, in the dashboard or in their own terminal.", "Open the recipe on the Library's Recipes tab, or run this command yourself in a terminal.")
			}
			word := "archive"
			if in.Restore {
				word = "restore"
			}
			if ask(fmt.Sprintf("Type %q to %s it", word, word), "") != word {
				return fail(c, apperr.Canceled, "Nothing changed.", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "recipe.archive", in, ops.Caller{Human: true}, func(o any) {
				fmt.Println(o.(*recipes.RecipeArchiveOut).Message)
			})
		},
	}
	c.Flags().BoolVar(&in.Restore, "restore", false, "put it back on the Recipes tab")
	return c
}

// inputFlags reads --input name=value (repeat a name for more clips or
// pictures); Claude's cards in the panel read them the same way (D211).
func inputFlags(raw []string) ([]recipes.RecipeInputValue, error) {
	return recipes.InputsFromFlags(raw)
}

func recipePlanCmd() *cobra.Command {
	var in recipes.RecipePlanIn
	var raw []string
	c := &cobra.Command{
		Use:   "plan <recipe> [--input name=value]… [--again run] [--note …]",
		Short: "Plan a run (nothing starts): the inputs, where the video goes, each step, and the estimate with each step's outside service, model and source",
		Example: "  metarush recipe plan app-teaser --input headline=\"Read free\" --input art=hero,villain\n" +
			"  metarush recipe plan --again app-teaser#4 --input voice=Cleo",
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				in.Recipe = args[0]
			}
			vals, err := inputFlags(raw)
			if err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			in.Inputs = vals
			return runOp(c, "recipe.plan", in, func(o any) { printRecipePlan(c, o.(*recipes.RecipePlanOut)) })
		},
	}
	recipeInputFlags(c, &raw, &in.Note)
	c.Flags().StringVar(&in.Version, "version", "", "plan another version (runs make the current one)")
	c.Flags().StringVar(&in.Repeats, "again", "", "plan making a run again (recipe#number)")
	return c
}

func recipeInputFlags(c *cobra.Command, raw *[]string, note *string) {
	c.Flags().StringArrayVar(raw, "input", nil, "name=value for an input (words, a choice, a voice, or clips and pictures by name; repeat or separate with commas for more)")
	c.Flags().StringVar(note, "note", "", "what this run is for or changes")
}

func printRecipePlan(c *cobra.Command, p *recipes.RecipePlanOut) {
	fmt.Println(p.Summary)
	if len(p.Inputs) > 0 {
		fmt.Println("Inputs:")
		for _, in := range p.Inputs {
			mark := ""
			if in.Changed {
				mark = " (new)"
			}
			fmt.Printf("  %s: %s%s\n", in.Name, in.Words, mark)
		}
	}
	fmt.Println("Steps:")
	for i, s := range p.Steps {
		line := fmt.Sprintf("  %d. %s", i+1, s.Label)
		if s.Detail != "" {
			line += " — " + s.Detail
		}
		fmt.Println(line)
	}
	if len(p.Pack) > 0 {
		fmt.Println("Shorts (MetaRush's first picks; Claude keeps or changes them):")
		for _, o := range p.Pack {
			line := fmt.Sprintf("  %s %d: %s–%s of %s", o.Output, o.N, editspec.Clock(o.From), editspec.Clock(o.To), theirs(o.ConversationName))
			if o.First != "" {
				line += " " + theirs(shortWords(o.First, 60))
			}
			if len(o.Why) > 0 {
				line += " (" + strings.Join(o.Why, ", ") + ")"
			}
			fmt.Println(line)
		}
	}
	fmt.Println("Estimate: " + p.Estimate.About)
	writeCost(os.Stdout, "  ", p.Estimate, detailsMode(c))
	printSamplePlan(c, p.Sample)
	if !p.AskForEstimate && p.Estimate.USD > 0 {
		fmt.Println("  Your estimates are off (Settings → Preferences): it starts without asking about the price.")
	}
	for _, b := range p.Budget {
		fmt.Println("  " + b)
	}
	if len(p.Changes) > 0 && p.Repeats != nil {
		fmt.Println("What changes: " + strings.Join(p.Changes, "; "))
	}
	if len(p.Reused) > 0 && p.Repeats != nil {
		fmt.Println("Reused: " + strings.Join(p.Reused, "; "))
	}
	for _, w := range p.Warnings {
		fmt.Println("Note: " + w)
	}
	if !p.Ready {
		fmt.Println("It can't start yet:")
		for _, pr := range p.Problems {
			fmt.Println("  - " + pr)
		}
	}
	if detailsMode(c) {
		fmt.Println("fingerprint: " + p.Fingerprint)
	}
}

func recipeRunCmd() *cobra.Command {
	var in recipes.RecipeRunIn
	var raw []string
	var maxCost float64
	var wait bool
	c := &cobra.Command{
		Use:   "run <recipe> [--input name=value]… [--note …] [--max-cost N] [--wait]",
		Short: "Start a run of a recipe — people only: the plan and its estimate are shown first, and you agree to it by typing start",
		Example: "  metarush recipe run app-teaser --input headline=\"Read free\" --wait\n" +
			"  metarush recipe run event-recap --input footage=\"Gala 1, Gala 2\" --max-cost 3",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Recipe = args[0]
			vals, err := inputFlags(raw)
			if err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			in.Inputs = vals
			return startRecipeRunWith(c, in.RecipePlanIn, flagValue(c, "max-cost", maxCost), wait, strings.TrimSpace(in.SkipSample) != "",
				func(a *app.App, fp string, usd *float64) (*recipes.RecipeRunOut, error) {
					in.Fingerprint, in.MaxUSD = fp, usd
					return ops.CallTyped[recipes.RecipeRunOut](c.Context(), a, "recipe.run", in, ops.Caller{Human: true})
				})
		},
	}
	recipeInputFlags(c, &raw, &in.Note)
	c.Flags().Float64Var(&maxCost, "max-cost", 0, "the most it may spend in US dollars (default: the estimate, or the sample's when the run starts with one)")
	c.Flags().StringVar(&in.SkipSample, "skip-sample", "", "start the whole run without the sample its plan asks for first, saying why (kept with the run)")
	c.Flags().BoolVar(&wait, "wait", false, "follow the run here (running its renders on this computer when no other can) until it is ready or waits for someone")
	return c
}

func flagValue(c *cobra.Command, name string, v float64) *float64 {
	if c.Flags().Changed(name) {
		return &v
	}
	return nil
}

func recipeAgainCmd() *cobra.Command {
	var in recipes.RecipeAgainIn
	var raw []string
	var maxCost float64
	var wait bool
	c := &cobra.Command{
		Use:     "again <run> [--input name=value]… [--note …] [--max-cost N] [--wait]",
		Short:   "Make a run's video again with changes (new inputs, another voice) — a new version of the same cut; people only, the plan first",
		Example: "  metarush recipe again app-teaser#4 --input voice=Cleo --note \"a warmer voice\"",
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Run = args[0]
			vals, err := inputFlags(raw)
			if err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			in.Inputs = vals
			plan := recipes.RecipePlanIn{Repeats: in.Run, Inputs: in.Inputs, Note: in.Note}
			return startRecipeRunWith(c, plan, flagValue(c, "max-cost", maxCost), wait, strings.TrimSpace(in.SkipSample) != "",
				func(a *app.App, fp string, usd *float64) (*recipes.RecipeRunOut, error) {
					in.Fingerprint, in.MaxUSD = fp, usd
					return ops.CallTyped[recipes.RecipeRunOut](c.Context(), a, "recipe.again", in, ops.Caller{Human: true})
				})
		},
	}
	recipeInputFlags(c, &raw, &in.Note)
	c.Flags().Float64Var(&maxCost, "max-cost", 0, "the most it may spend in US dollars (default: the estimate, or the sample's when it starts with one)")
	c.Flags().StringVar(&in.SkipSample, "skip-sample", "", "make it again without the sample its plan asks for first, saying why (kept with the run)")
	c.Flags().BoolVar(&wait, "wait", false, "follow the run here until it is ready or waits for someone")
	return c
}

// startRecipeRun shows the plan, asks the person to agree, starts the run and
// (with wait) follows it here. With the person's estimates off (D208) the
// price isn't asked about: typing start is still theirs.
func startRecipeRun(c *cobra.Command, plan recipes.RecipePlanIn, maxUSD *float64, wait bool,
	start func(a *app.App, fingerprint string, usd *float64) (*recipes.RecipeRunOut, error)) error {
	return startRecipeRunWith(c, plan, maxUSD, wait, false, start)
}

// startRecipeRunWith is startRecipeRun for a plan that may start with a
// sample (D294): unless the person skips it, what they agree to now is the
// sample's price; the rest is agreed once they approve it.
func startRecipeRunWith(c *cobra.Command, plan recipes.RecipePlanIn, maxUSD *float64, wait, skipSample bool,
	start func(a *app.App, fingerprint string, usd *float64) (*recipes.RecipeRunOut, error)) error {
	if !interactive() || assistantMode() {
		return fail(c, apperr.HumanOnly, "Only a person starts a run of a recipe, in the dashboard or in their own terminal.",
			"Show the person the plan (recipe plan); they start it on the Library's Recipes tab or with this command in their own terminal.")
	}
	a, err := openApp(c, "cli")
	if err != nil {
		return err
	}
	defer a.Close()
	p, err := ops.CallTyped[recipes.RecipePlanOut](c.Context(), a, "recipe.plan", plan, ops.Caller{})
	if err != nil {
		return failErr(c, err)
	}
	printRecipePlan(c, p)
	if !p.Ready {
		return fail(c, apperr.Validation, "This run can't start yet.", strings.Join(p.Problems, " "))
	}
	usd, about := p.Estimate.USD, p.Estimate.About
	sample := p.Sample != nil && !skipSample
	if sample {
		usd, about = p.Sample.Estimate.USD, p.Sample.Estimate.About
	}
	if p.AskForEstimate {
		if maxUSD != nil && *maxUSD+0.0005 < usd {
			return fail(c, apperr.ConfirmRequired, fmt.Sprintf("The run costs %s, more than the most you said it may spend.", about), "Raise the most it may spend, or change the inputs.")
		}
		if maxUSD == nil && usd > 0 {
			v := usd
			maxUSD = &v
		}
	}
	q := "Type \"start\" to start it"
	if sample {
		q = "Type \"start\" to make the sample first"
	}
	if p.AskForEstimate && usd > 0 {
		q += " (" + about + ")"
	}
	if ask(q, "") != "start" {
		return fail(c, apperr.Canceled, "Nothing started.", "")
	}
	out, err := start(a, p.Fingerprint, maxUSD)
	if err != nil {
		return failErr(c, err)
	}
	if !wait {
		return emit(c, out, func() {
			fmt.Println(out.Message)
			fmt.Println("MetaRush carries it on while it runs on this computer; follow it on the Library's Recipes tab.")
			printRunDetails(c, &out.Run)
		})
	}
	if !jsonMode(c) {
		fmt.Println(out.Message)
	}
	return followRun(c, a, out.Run.ID)
}

// followRun advances a run here, running the jobs it waits on in this
// terminal when no other computer takes them, until it ends or waits for someone.
func followRun(c *cobra.Command, a *app.App, id string) error {
	ctx, cancel := signalCtx(c.Context())
	defer cancel()
	last := ""
	for {
		run, err := recipes.Advance(ctx, a, id)
		if err != nil {
			return failErr(c, err)
		}
		v := recipesRunView(c, a, run.ID)
		if v == nil {
			return fail(c, apperr.NotFound, "The run is gone.", "")
		}
		if !jsonMode(c) && v.StateLabel != last {
			fmt.Println(v.StateLabel)
			last = v.StateLabel
		}
		switch v.State {
		case models.RunDone, models.RunFailed, models.RunStopped, models.RunWaiting:
			return emit(c, recipes.RecipeRunOut{Run: *v, Message: v.StateLabel}, func() { printRunEnd(c, v) })
		}
		if pending := recipes.PendingJobs(ctx, a, run); len(pending) > 0 {
			if _, err := waitJobs(c, a, pending, 2*time.Hour, true); err != nil {
				return failErr(c, err)
			}
			continue
		}
		select {
		case <-ctx.Done():
			return fail(c, apperr.Canceled, "Stopped following the run; it carries on.", "")
		case <-time.After(2 * time.Second):
		}
	}
}

func recipesRunView(c *cobra.Command, a *app.App, id string) *recipes.RecipeRunView {
	out, err := ops.CallTyped[recipes.RecipeRunsOut](c.Context(), a, "recipe.runs", recipes.RecipeRunsIn{Run: id}, ops.Caller{})
	if err != nil || out.Run == nil {
		return nil
	}
	return out.Run
}

func printRunEnd(c *cobra.Command, v *recipes.RecipeRunView) {
	switch v.State {
	case models.RunDone:
		line := fmt.Sprintf("Version %d is in Awaiting your review", v.RevisionNumber)
		if v.QA != "" {
			line += "; the quality check " + v.QA
		}
		fmt.Println(line + "; it spent " + v.SpentWords + ".")
	case models.RunWaiting:
		if v.Claude != "" {
			fmt.Println(v.Claude)
		}
	case models.RunFailed, models.RunStopped:
		if v.Problem != "" {
			fmt.Println(v.Problem)
		}
	}
	printRunDetails(c, v)
}

func printRunDetails(c *cobra.Command, v *recipes.RecipeRunView) {
	if !detailsMode(c) {
		return
	}
	fmt.Printf("  run: %s (%s)\n", v.Ref, v.ID)
	if v.ProjectID != "" {
		fmt.Printf("  project: %s  cut: %s\n", v.ProjectID, v.Branch)
	}
	if v.Revision != "" {
		fmt.Printf("  version: %s\n", v.Revision)
	}
	fmt.Println("  url: " + v.URL)
}

func recipeRunsCmd() *cobra.Command {
	var in recipes.RecipeRunsIn
	var wait bool
	c := &cobra.Command{
		Use:   "runs [<recipe>] [--run recipe#N] [--project P] [--wait]",
		Short: "List runs of recipes (when, who, where, cost, approved), or show one with its steps and what it waits for; --wait follows it here",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				in.Recipe = args[0]
			}
			if wait {
				if in.Run == "" {
					return fail(c, apperr.InvalidInput, "Say which run to follow with --run.", "")
				}
				a, err := openApp(c, "cli")
				if err != nil {
					return err
				}
				defer a.Close()
				v := recipesRunView(c, a, in.Run)
				if v == nil {
					return fail(c, apperr.NotFound, "There's no such run.", "")
				}
				return followRun(c, a, v.ID)
			}
			return runOp(c, "recipe.runs", in, func(o any) {
				out := o.(*recipes.RecipeRunsOut)
				if out.Run != nil {
					printRecipeRun(c, out.Run)
					return
				}
				for _, r := range out.Runs {
					line := fmt.Sprintf("%s, run %d, %s by %s: %s; spent %s%s", theirs(r.Title), r.Number, r.At.Local().Format("2 Jan 15:04"), r.By,
						strings.ToLower(r.StateLabel[:1])+r.StateLabel[1:], r.SpentWords, approvedWords(r.Approved))
					if r.Project != "" {
						line += " — in " + theirs(r.Project)
					}
					if detailsMode(c) {
						line += " [" + r.Ref + "]"
					}
					fmt.Println(line)
				}
				fmt.Println(out.Summary)
			})
		},
	}
	c.Flags().StringVar(&in.Run, "run", "", "one run in full (recipe#number)")
	c.Flags().StringVar(&in.ProjectID, "project", "", "runs whose video is in this project")
	c.Flags().IntVar(&in.Limit, "limit", 0, "how many (default 50)")
	c.Flags().BoolVar(&wait, "wait", false, "follow the run (--run) here until it is ready or waits for someone")
	return c
}

func recipePicksCmd() *cobra.Command {
	var keep, drop bool
	var part, from, to, footage, why string
	var n int
	c := &cobra.Command{
		Use:   "picks <run> [--keep] [--part <section|output> --n N (--from T --to T [--footage clip] [--why …] | --drop)]",
		Short: "Keep or change a run's first picks: the moments MetaRush picked for its chosen sections and the stretches of its shorts (recipe runs --run shows them); the run goes on once every pick is kept or changed",
		Long: `A run whose chosen sections get new footage starts with MetaRush's first picks from the footage's moments, and
the shorts of its pack with first picks from the conversation's best moments; it waits until they are kept or
changed. --keep keeps every pick still as MetaRush made it. --part and --n change one: a section's moment (--from
and --to in its footage, --footage for another clip) or a short's stretch (--from and --to on the conversation's
clock); one more than a part has adds one; --drop takes one out. Times are seconds or m:ss.`,
		Example: "  metarush recipe picks podcast#4 --keep\n" +
			"  metarush recipe picks podcast#4 --part shorts --n 2 --from 1:12 --to 1:40 --why \"the story about the bucket\"\n" +
			"  metarush recipe picks recap#2 --part moments --n 1 --from 3.5 --to 9 --footage \"Gala 2\" --keep",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in := recipes.RecipePicksIn{Run: args[0], Keep: keep}
			if part != "" {
				ch := recipes.RecipePickChange{Part: part, N: n, Footage: footage, Why: why, Drop: drop}
				if !drop {
					f, err := clockSeconds(from)
					if err != nil || from == "" {
						return fail(c, apperr.InvalidInput, "Give where it starts with --from (seconds or m:ss).", "")
					}
					t, err := clockSeconds(to)
					if err != nil || to == "" {
						return fail(c, apperr.InvalidInput, "Give where it ends with --to (seconds or m:ss).", "")
					}
					ch.From, ch.To = &f, &t
				}
				in.Set = append(in.Set, ch)
			}
			return runOp(c, "recipe.picks", in, func(o any) {
				out := o.(*recipes.RecipePicksOut)
				fmt.Println(out.Message)
				printRunPicks(&out.Run)
				printRunDetails(c, &out.Run)
			})
		},
	}
	c.Flags().BoolVar(&keep, "keep", false, "keep every pick still as MetaRush made it")
	c.Flags().StringVar(&part, "part", "", "the chosen section or pack output a change is for")
	c.Flags().IntVar(&n, "n", 0, "which of its picks (1, 2…; one more than it has adds one)")
	c.Flags().StringVar(&from, "from", "", "where the moment or stretch starts (seconds or m:ss)")
	c.Flags().StringVar(&to, "to", "", "where it ends")
	c.Flags().StringVar(&footage, "footage", "", "a section's moment: the clip it comes from (default: the pick's)")
	c.Flags().StringVar(&why, "why", "", "why this moment (kept with it)")
	c.Flags().BoolVar(&drop, "drop", false, "take that pick out (a short isn't made)")
	return c
}

// printRunPicks lists a run's first picks and the shorts of its pack.
func printRunPicks(v *recipes.RecipeRunView) {
	words := map[string]string{models.PickFirst: "first pick", "standin": "stand-in to replace", models.PickKept: "kept", models.PickChanged: "changed", models.PickDropped: "dropped"}
	for _, p := range v.Picks {
		line := fmt.Sprintf("  %s %d: %s of %s — %s", p.Part, p.N, p.When, theirs(p.FootageTitle), words[p.State])
		if len(p.Why) > 0 && p.State != "standin" {
			line += " (" + strings.Join(p.Why, ", ") + ")"
		}
		if p.Words != "" {
			line += " " + theirs(shortWords(p.Words, 60))
		}
		fmt.Println(line)
	}
	for _, s := range v.Shorts {
		if s.Project != "" {
			fmt.Printf("  Short %d: %s — %s\n", s.N, theirs(s.Project), outputStateWords(s.State))
		}
	}
}

func shortWords(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

func outputStateWords(s string) string {
	switch s {
	case models.OutputPlanned:
		return "not made yet"
	case models.OutputMaking:
		return "being made"
	case models.OutputReady:
		return "ready for your review"
	case models.OutputDropped:
		return "not made"
	case models.OutputFailed:
		return "couldn't be made"
	}
	return s
}

func printRecipeRun(c *cobra.Command, v *recipes.RecipeRunView) {
	fmt.Printf("Run %d of %s — %s\n", v.Number, theirs(v.RecipeTitle), v.StateLabel)
	if v.ProjectName != "" {
		fmt.Println("  In " + theirs(v.ProjectName))
	}
	for _, in := range v.Inputs {
		fmt.Printf("  %s: %s\n", in.Name, in.Words)
	}
	for i, s := range v.Steps {
		line := fmt.Sprintf("  %d. %s: %s", i+1, s.Label, stepStateWords(s.State))
		if s.Detail != "" {
			line += " — " + s.Detail
		}
		fmt.Println(line)
	}
	line := "  Estimate " + v.Estimate.About
	if v.AgreedWords != "" {
		line += ", agreed " + v.AgreedWords
	}
	fmt.Println(line + "; spent " + v.SpentWords + ".")
	printRunSample(v)
	if detailsMode(c) {
		writeCost(os.Stdout, "  ", v.Estimate, true)
	}
	if len(v.Picks) > 0 || len(v.Shorts) > 0 {
		fmt.Println("  Picks and shorts:")
		printRunPicks(v)
	}
	if v.Claude != "" {
		fmt.Println("  For Claude: " + v.Claude)
	}
	if v.Problem != "" {
		fmt.Println("  " + v.Problem)
	}
	printRunDetails(c, v)
}

func stepStateWords(s string) string {
	switch s {
	case models.StepPending:
		return "not yet"
	case models.StepRunning:
		return "going on"
	case models.StepWaiting:
		return "waiting"
	case models.StepDone:
		return "done"
	case models.StepSkipped:
		return "not needed"
	case models.StepFailed:
		return "didn't finish"
	}
	return s
}

func recipeCompareCmd() *cobra.Command {
	var in recipes.RecipeCompareIn
	c := &cobra.Command{
		Use:   "compare <run> <run>",
		Short: "Compare two runs of a recipe: changed inputs, notes, recipe versions, cost and the cut-sheet difference",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			in.Left, in.Right = args[0], args[1]
			return runOp(c, "recipe.compare", in, func(o any) {
				out := o.(*recipes.RecipeCompareOut)
				fmt.Printf("Run %d and run %d of %s\n", out.Left.Number, out.Right.Number, theirs(out.Left.Title))
				for _, r := range out.Inputs {
					if r.Changed {
						fmt.Printf("  %s: %s → %s\n", r.Name, r.Left, r.Right)
					}
				}
				fmt.Println("  " + out.Versions)
				fmt.Println("  " + out.Cost)
				for _, n := range out.Notes {
					fmt.Println("  " + n)
				}
				if out.Cut != nil {
					fmt.Println("The cut:")
					for _, ch := range out.Cut.Changes {
						fmt.Println("  - " + ch)
					}
				}
				for _, p := range out.Problems {
					fmt.Println(p)
				}
			})
		},
	}
	return c
}

func recipeContinueCmd() *cobra.Command {
	var in recipes.RecipeContinueIn
	var moreCost float64
	var wait bool
	c := &cobra.Command{
		Use:   "continue <run> [--more-cost N] [--wait]",
		Short: "Let a run that waits for a person go on (after raising a budget or recording consent), agreeing to spend more when it asks — people only",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Run = args[0]
			if !interactive() || assistantMode() {
				return fail(c, apperr.HumanOnly, "Only a person lets a waiting run go on, in the dashboard or in their own terminal.", "Open the run on the Library's Recipes tab, or run this command yourself in a terminal.")
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
			prompt := "Type \"go on\" to let it go on"
			if in.MoreUSD == nil && v.WaitingUSD > 0 {
				// It asks to spend more: going on agrees to what it asks.
				more := v.WaitingUSD
				in.MoreUSD = &more
				prompt = fmt.Sprintf("Type \"go on\" to agree to %s more and let it go on", costs.Money(more))
			}
			if ask(prompt, "") != "go on" {
				return fail(c, apperr.Canceled, "Nothing changed.", "")
			}
			out, err := ops.CallTyped[recipes.RecipeRunOut](c.Context(), a, "recipe.continue", in, ops.Caller{Human: true})
			if err != nil {
				return failErr(c, err)
			}
			if !wait {
				return emit(c, out, func() { fmt.Println(out.Message) })
			}
			if !jsonMode(c) {
				fmt.Println(out.Message)
			}
			return followRun(c, a, out.Run.ID)
		},
	}
	c.Flags().Float64Var(&moreCost, "more-cost", 0, "agree to spend this much more in US dollars")
	c.Flags().BoolVar(&wait, "wait", false, "follow the run here afterwards")
	return c
}

func recipeStopCmd() *cobra.Command {
	var in recipes.RecipeStopIn
	c := &cobra.Command{
		Use:   "stop <run>",
		Short: "Stop a run: work it asked for that hasn't started is called off; what it made stays",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Run = args[0]
			return runOp(c, "recipe.stop", in, func(o any) { fmt.Println(o.(*recipes.RecipeRunOut).Message) })
		},
	}
	return c
}

// countWords says a count with its noun ("1 problem", "3 problems").
func countWords(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %ss", n, one)
}
