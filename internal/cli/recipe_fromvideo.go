package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/linkimport"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/recipes"
)

// metarush recipe from-video <link | clip | file> (D400, D401): start a recipe
// from a video you like. A link comes in as a reference through the link
// import; a file on this computer is added first and marked a reference; a
// clip in MetaRush is used as it is (--reference marks it). Once MetaRush has
// looked at the video, the draft is saved by itself; --wait does the import
// and the looking here and prints the draft.

func recipeFromVideoCmd() *cobra.Command {
	var in recipes.RecipeFromVideoIn
	var wait, details bool
	var browser string
	c := &cobra.Command{
		Use:   "from-video <link | clip | file> [--project P] [--name N] [--title T] [--wait]",
		Short: "Start a recipe from a video you like: it comes in as a reference (never played in a cut), and what it does becomes a recipe draft — your footage in sections shaped like its sections",
		Long: `Start a recipe from a video you like: a link (YouTube, TikTok, X), a clip already in MetaRush or a video file on
this computer. The video comes in as a reference: someone else's video, kept to learn from, that no cut plays. Once
MetaRush has looked at it (its words and voices, its cuts, pauses and loudness, and on a Mac what's on screen), what
it does becomes a recipe draft: your own footage in sections shaped like its sections, with their lengths, its
captions, rules (its length, a short hook, captions) and hints (pacing, the title card and end screen, music). The
draft keeps where it came from and never its footage, words or pictures. A person makes it current before it runs.

Without --wait the video is brought in and looked at by MetaRush running on this computer, and the draft is saved
by itself when it's done (it shows on the Recipes tab); asking again says where it stands. --wait does it here.`,
		Example: "  metarush recipe from-video https://www.youtube.com/watch?v=… --wait\n  metarush recipe from-video https://www.tiktok.com/@…/video/… --project \"Shop tips\"\n  metarush recipe from-video ~/Downloads/their-video.mp4 --wait",
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.From = args[0]
			if c.Flags().Changed("browser-signin") {
				in.BrowserSignIn = browser
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			// A video file on this computer: added like a drop, then started from as a reference.
			if st, err := os.Stat(in.From); err == nil && !st.IsDir() {
				abs, _ := filepath.Abs(in.From)
				b, _ := json.Marshal(ingest.FilesIn{Paths: []string{abs}, ProjectID: in.ProjectID})
				res, err := ops.Call(ctx, a, "ingest.files", b, ops.Caller{})
				if err != nil {
					return failErr(c, err)
				}
				fo := res.(*ingest.FilesOut)
				if len(fo.Results) == 0 || fo.Results[0].AssetID == "" {
					msg := "That file couldn't be added."
					if len(fo.Results) > 0 && fo.Results[0].Error != "" {
						msg = fo.Results[0].Error
					}
					return fail(c, apperr.InvalidInput, msg, "Use a video file, or a link to the video.")
				}
				in.From, in.Reference = fo.Results[0].AssetID, true
			}
			call := func() (*recipes.RecipeFromVideoOut, error) {
				b, _ := json.Marshal(in)
				res, err := ops.Call(ctx, a, "recipe.fromVideo", b, ops.Caller{})
				if err != nil {
					return nil, err
				}
				return res.(*recipes.RecipeFromVideoOut), nil
			}
			out, err := call()
			if err != nil {
				return failErr(c, err)
			}
			if wait && out.State != recipes.FromVideoDrafted {
				if out, err = waitFromVideo(c, a, out, call); err != nil {
					return failErr(c, err)
				}
			}
			return emit(c, out, func() { printFromVideo(out, details) })
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "runs make new cuts in this project (a linked video comes in there too); without it each run makes a project of its own")
	c.Flags().StringVar(&in.Name, "name", "", "the recipe's short name (default: one made from its title)")
	c.Flags().StringVar(&in.Title, "title", "", "the recipe's title")
	c.Flags().BoolVar(&in.Reference, "reference", false, "for a clip already in MetaRush: mark it as a reference too (someone else's video)")
	c.Flags().StringVar(&browser, "browser-signin", "", "download the link with this browser's sign-in, for this import only: chrome, safari, firefox, brave or edge")
	c.Flags().BoolVar(&wait, "wait", false, "bring the video in and look at it here, then print the draft")
	addDetails(c, &details)
	return c
}

// waitFromVideo brings the video in and looks at it in this terminal, then
// asks again: the draft is made as the looks finish (or by the last ask).
func waitFromVideo(c *cobra.Command, a *app.App, out *recipes.RecipeFromVideoOut, call func() (*recipes.RecipeFromVideoOut, error)) (*recipes.RecipeFromVideoOut, error) {
	ctx := c.Context()
	if out.Import != nil && len(out.Import.JobIDs) > 0 {
		if !jsonMode(c) {
			fmt.Fprintln(os.Stderr, "bringing the video in…")
		}
		done, err := runImportsHere(ctx, c, a, out.Import.JobIDs)
		if err != nil {
			return nil, err
		}
		for _, j := range done {
			if j.State != models.JobSucceeded {
				msg := "The video couldn't be brought in."
				if adv := linkimport.AdviceOf(&j); adv != nil {
					msg = adv.Message + " " + adv.Next
				}
				return nil, apperr.New(apperr.Conflict, "%s", msg)
			}
		}
		var err2 error
		if out, err2 = call(); err2 != nil {
			return nil, err2
		}
	}
	if out.State == recipes.FromVideoLooking && len(out.JobIDs) > 0 {
		if !jsonMode(c) {
			fmt.Fprintf(os.Stderr, "looking at the video (%d steps)…\n", len(out.JobIDs))
		}
		if _, err := waitJobs(c, a, out.JobIDs, 0, true); err != nil {
			return nil, err
		}
	}
	return call()
}

func printFromVideo(o *recipes.RecipeFromVideoOut, details bool) {
	fmt.Println(o.Message)
	if o.Card != nil && o.State != recipes.FromVideoImporting {
		fmt.Println("What it does:")
		for _, l := range o.Card.Lines {
			fmt.Println("  " + l)
		}
	}
	if o.Recipe != nil && o.Version != nil {
		fmt.Printf("The draft: %s, version %d (%s).\n", theirs(o.Recipe.Title), o.Version.Number, versionStateWords(o.Version.State))
		printRecipeCheck(o.Version.Check)
	}
	if o.Next != "" {
		fmt.Println(o.Next)
	}
	if details {
		if o.Clip != nil {
			fmt.Printf("    clip %s  reference %v  %s\n", o.Clip.ID, o.Clip.Reference, o.Clip.Link)
		}
		if o.Recipe != nil {
			fmt.Printf("    recipe %s (%s)  version %s  url %s\n", o.Recipe.Name, o.Recipe.ID, o.Version.Short, o.Recipe.URL)
			fmt.Println("    see its sheet: metarush recipe checkout " + o.Recipe.Name + " -")
		}
		for _, id := range o.JobIDs {
			fmt.Println("    job " + id)
		}
	}
}
