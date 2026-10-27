package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// footageLanguageCmd is `metarush footage language <clip> [--set es]` (D243):
// which language a clip is spoken in, and setting it, which writes the words
// down again with the recognizer for that language.
func footageLanguageCmd() *cobra.Command {
	var set string
	var choices, wait bool
	cmd := &cobra.Command{
		Use:   "language <asset-id> [--set <language>] [--wait]",
		Short: "Which language a clip is spoken in, or set it: its words are written down again in that language",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if set == "" {
				in := ops.FootageLanguageIn{AssetID: args[0], Choices: choices}
				return runOp(c, "footage.language", in, func(o any) { printLanguage(o.(*ops.FootageLanguageOut), detailsMode(c)) })
			}
			in := ops.FootageLanguageSetIn{AssetID: args[0], Language: set}
			if !wait {
				return runOp(c, "footage.language.set", in, func(o any) { fmt.Println(o.(*ops.FootageLanguageSetOut).Note) })
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			b, _ := json.Marshal(in)
			out, err := ops.Call(c.Context(), a, "footage.language.set", b, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			res := out.(*ops.FootageLanguageSetOut)
			var js []models.Job
			if res.JobID != "" {
				if js, err = waitJobs(c, a, []string{res.JobID}, 60*time.Minute, jsonMode(c)); err != nil {
					return failErr(c, err)
				}
			}
			b2, _ := json.Marshal(ops.FootageLanguageIn{AssetID: args[0]})
			now, err := ops.Call(c.Context(), a, "footage.language", b2, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, map[string]any{"set": res, "jobs": js, "language": now}, func() {
				if len(js) > 0 && js[0].State != models.JobSucceeded {
					fmt.Println("Writing the words down again did not finish. You can retry it from Activity in the dashboard.")
					if detailsMode(c) {
						fmt.Printf("job %s %s: %s\n", js[0].ID, js[0].State, js[0].Error)
					}
					return
				}
				printLanguage(now.(*ops.FootageLanguageOut), detailsMode(c))
			})
		},
	}
	cmd.Flags().StringVar(&set, "set", "", "the language it is spoken in, by name (Spanish) or code (es)")
	cmd.Flags().BoolVar(&choices, "choices", false, "also list the languages it can be set to")
	cmd.Flags().BoolVar(&wait, "wait", false, "with --set: wait until the words are written down again")
	return cmd
}

func printLanguage(o *ops.FootageLanguageOut, details bool) {
	fmt.Println(o.Note)
	if o.Recognizer != "" && details {
		fmt.Printf("words written down by %s\n", o.Recognizer)
	}
	if l := o.Language; l != nil && details {
		fmt.Printf("language %s, by %s", l.Code, l.By)
		if l.Score > 0 {
			fmt.Printf(", score %.2f over %.0f s", l.Score, l.Listened)
		}
		fmt.Println()
	}
	if len(o.Choices) > 0 {
		fmt.Println("Languages it can be set to:")
		for _, ch := range o.Choices {
			if details {
				fmt.Printf("  %s (%s)\n", ch.Name, ch.Code)
			} else {
				fmt.Printf("  %s\n", ch.Name)
			}
		}
	}
}
