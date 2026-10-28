package plainwords

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// "Recipe" means only the Recipes feature (D210). What MetaRush called a
// recipe before has its own name now — a fixed copy's fix steps and their
// fingerprint, how a generated clip or picture was made (madeWith), a Remotion
// refresh's steps — so people and agents never mistake one for the other.
// This test keeps it that way:
//
//   - in Go, TypeScript and JavaScript and in the skills, the word appears only
//     in the feature's own files (recipeFeature) or on a line that names the
//     feature (recipeNames: its package, operations, commands, skill, store,
//     pages or name, so the lines that wire it in elsewhere — and the words
//     about it in shared skills — need no exception);
//   - in the other docs (README, CLAUDE.md, docs/), the old names never come
//     back (oldRecipeNames); the changelog and the feature's own docs keep
//     their history.
//
// A finding is fixed with the new names (D210) or, when it really is the
// Recipes feature, by adding its file or name below.

// recipeFeature are the Recipes feature's own files (some come with its later
// builds), the stored names D210 renamed and this test.
var recipeFeature = []string{
	"internal/recipes/",                      // the feature
	"internal/ops/recipe*.go",                // its operations
	"internal/cli/recipe*.go",                // its commands
	"skills/video-recipe/",                   // its skill
	"web/src/components/recipes/",            // its dashboard parts
	"web/src/screens/Recipes.tsx",            // the Library's Recipes tab
	"web/scripts/recipes*",                   // its browser run
	"internal/models/recipe.go",              // its records
	"internal/store/recipes.go",              // its collections' indexes (migration 38)
	"internal/activity/recipes.go",           // its runs on the activity bar
	"internal/logbook/describe_recipes.go",   // its phrases in the activity log
	"internal/links/recipes.go",              // its pages
	"internal/assistant/recipe*.go",          // its cards in Claude's panel (D211)
	"internal/api/recipes*.go",               // its files to and from other workspaces (D219)
	"internal/oldnames/",                     // the stored names before D210, read while computers upgrade
	"internal/handover/",                     // a handover moves its runs and plans to another computer (item 34)
	"internal/cli/handover.go",               // … and says so
	"web/src/screens/settings/Handover.tsx",  // … in Settings → Storage too
	"internal/plainwords/recipeword_test.go", // this test
}

// recipeNames can only mean the feature: its package, its operations,
// commands and skill, its document format, its collections and their names in
// the store (recipe_runs, CRecipe…), its tab and pages ("recipes", /recipes),
// where a run's version came from (origin recipe:<run>) and the feature's
// name, Recipes. A line with one of them is about the feature.
var recipeNames = regexp.MustCompile(strings.Join([]string{
	`internal/recipes`, `\brecipes\.[A-Z]`, "['\"`]recipe\\.[a-z]", `metarush recipe\b`, `video-recipe`, `metarush\.recipe/`,
	`\brecipe_runs\b`, `\bCRecipe\w*`, "['\"`]recipes['\"`]", `/recipes\b`, "['\"`]recipe:", `\bRecipes\b`,
	// Its kinds where shared lists name them — the activity bar's category,
	// the to-do and bell kinds (recipe_run, recipe_planned, as stored), the log's channel and the
	// actor's — and its records, IDs and pages named from other packages.
	`\b(Cat|Todo|Notify|Channel|Via)Recipe\w*`, `\brecipe_(run|planned)\b`, `\b(models|ids|links)\.Recipe\w*`,
}, "|"))

// recipeHistory are the docs that keep the old names as history: the
// changelog and the feature's own docs.
var recipeHistory = []string{"CHANGELOG.md", "docs/recipes*.md"}

// oldRecipeNames are the names D210 retired that no doc may use again: the
// ones only the old uses had (the feature may well have a recipe's hash or
// version).
var oldRecipeNames = regexp.MustCompile(`FindCopyByRecipe|FixRecipe|ResourceRecipe|RemotionRecipe|recipeSteps|\b(source|fixes|copy)\.recipe\b`)

var anyRecipe = regexp.MustCompile(`(?i)recipe`)

// codeExt are the files read for the word (and every file in skills/).
var codeExt = map[string]bool{".go": true, ".ts": true, ".tsx": true, ".js": true, ".mjs": true, ".cjs": true}

// notRead are folders this test never reads: the generated schema (from the
// Go types it reads) and test data (tests/fixtures/, and every testdata/).
var notRead = []string{"schema/", "tests/fixtures/"}

// ignoredDirs are what git ignores, for when it can't list the files.
var ignoredDirs = map[string]bool{"node_modules": true, "dist": true, "bin": true, "build": true, "coverage": true, "test-results": true, "playwright-report": true}

// repoFiles lists the repository's files, relative with slashes: what git
// tracks and the new files it doesn't ignore (so builds, bundles and other
// checkouts are never read), else every file outside hidden and skipped folders.
func repoFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Dir = root
	if out, err := cmd.Output(); err == nil {
		for _, f := range strings.Split(string(out), "\x00") {
			if f != "" {
				files = append(files, f)
			}
		}
		return files
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(p, root+string(filepath.Separator)))
		if d.IsDir() {
			if p != root && (strings.HasPrefix(d.Name(), ".") || ignoredDirs[d.Name()] || rel == "tests/fixtures/gen" || rel == "tests/out") {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// skipped reports whether a file is in a folder this test never reads.
func skipped(rel string) bool {
	return matchesAny(rel, notRead) || strings.HasPrefix(rel, "testdata/") || strings.Contains(rel, "/testdata/")
}

func matchesAny(rel string, patterns []string) bool {
	for _, p := range patterns {
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(rel, p) {
				return true
			}
			continue
		}
		if ok, _ := path.Match(p, rel); ok {
			return true
		}
	}
	return false
}

// recipeLine reports whether a line of code or skill uses the word without
// naming the feature.
func recipeLine(line string) bool {
	return anyRecipe.MatchString(line) && !recipeNames.MatchString(line)
}

func TestRecipeMeansOnlyTheFeature(t *testing.T) {
	root := repoRoot(t)
	var code, docs []string
	read := 0
	for _, rel := range repoFiles(t, root) {
		isDoc := strings.HasSuffix(rel, ".md") && (rel == "README.md" || rel == "CLAUDE.md" || strings.HasPrefix(rel, "docs/"))
		isCode := codeExt[path.Ext(rel)] || strings.HasPrefix(rel, "skills/")
		if !isDoc && !isCode || skipped(rel) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if errors.Is(err, fs.ErrNotExist) {
			continue // deleted, not yet committed
		} else if err != nil {
			t.Fatal(err)
		}
		read++
		for n, line := range strings.Split(string(b), "\n") {
			switch {
			case isCode && !matchesAny(rel, recipeFeature) && recipeLine(line):
				code = append(code, fmt.Sprintf("%s:%d: %s", rel, n+1, strings.TrimSpace(line)))
			case isDoc && !matchesAny(rel, recipeHistory) && oldRecipeNames.MatchString(line):
				docs = append(docs, fmt.Sprintf("%s:%d: %s", rel, n+1, strings.TrimSpace(line)))
			}
		}
	}
	if read < 500 {
		t.Fatalf("read only %d files from %s: is that the repository?", read, root)
	}
	sort.Strings(code)
	sort.Strings(docs)
	if len(code) > 0 {
		t.Errorf("\"Recipe\" means only the Recipes feature (D210). Use the new names (a fixed copy's fix steps and fingerprint, how a generated clip or picture was made, a refresh's steps), or add the feature's file to recipeFeature or its name to recipeNames:\n  %s",
			strings.Join(code, "\n  "))
	}
	if len(docs) > 0 {
		t.Errorf("These docs use names D210 retired; use the new ones (fixSteps, fingerprint, madeWith, refreshSteps):\n  %s", strings.Join(docs, "\n  "))
	}
}

// Lines that name the feature pass, the older uses don't, and the retired
// names are caught in docs.
func TestRecipeWordRules(t *testing.T) {
	for _, line := range []string{
		`	case "recipe.run", "recipe.again": // a recipe's run`,
		`import "github.com/metavert-io/metarush/internal/recipes"`,
		`	out, err := recipes.Run(ctx, a, in) // runs the recipe`,
		"| `metarush recipe run <name> --json` | Runs a recipe: a new project, or new cuts in the recipe's own |",
		`{ path: '/library/recipes', element: <Recipes /> },`,
		`{ id: 'recipes', label: 'Recipes' },`,
		`import { Recipes } from './Recipes';`,
		`	CRecipeRuns = "recipe_runs"`,
		`Origin: "recipe:" + run.ID,`,
		"- **recipe**: a video that worked, written down to make again (Library → Recipes).",
	} {
		if recipeLine(line) {
			t.Errorf("names the feature: %s", line)
		}
	}
	for _, line := range []string{
		`// The recipe last started is used.`,
		`Short: "A generated video's whole recipe"`,
		`	r := c.Recipe.Steps`,
		`func RecipeHash(ticked []string, take int) string {`,
		`<Details compact label="The recipe">`,
		`// the fix recipe of a copy (recipes.go)`,
		`Unknown clean-up recipe`,
	} {
		if !recipeLine(line) {
			t.Errorf("an old use got through: %s", line)
		}
	}
	for _, line := range []string{"how it was made (`source.recipe`)", "`fixes.recipe`, `fixes.copy.recipe`", "`FixRecipe`", "`recipeSteps`", "`models.RemotionRecipe`"} {
		if !oldRecipeNames.MatchString(line) {
			t.Errorf("a retired name got through: %s", line)
		}
	}
	if oldRecipeNames.MatchString("Recipes (D201–D208): a recipe is a video that worked, written down (`metarush.recipe/1`; its version is its hash). Run one with `metarush recipe run`.") {
		t.Error("the feature's own words are not retired names")
	}
}
