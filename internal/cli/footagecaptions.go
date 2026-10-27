package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// footageCaptionsCmd is `metarush footage captions <clip> [--language en]
// [--save <file>]` (D285): the captions files kept with a clip, one
// language's words, or that file saved.
func footageCaptionsCmd() *cobra.Command {
	var in ops.FootageCaptionsIn
	var save string
	cmd := &cobra.Command{
		Use:   "captions <asset-id> [--language <code>] [--save <file>]",
		Short: "The captions files kept with a clip (English captions an ability made, for instance), one language's words, or that file saved",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.AssetID = args[0]
			if save != "" && in.Language == "" {
				return fail(c, apperr.InvalidInput, "Say which language's captions to save.", "Add --language, for example --language en.")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			b, _ := json.Marshal(in)
			o, err := ops.Call(c.Context(), a, "footage.captions", b, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			out := o.(*ops.FootageCaptionsOut)
			if save != "" {
				// Saved whatever the output mode, never over a file already there.
				f, err := os.OpenFile(save, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
				if err == nil {
					_, err = f.WriteString(out.Text)
					if cerr := f.Close(); err == nil {
						err = cerr
					}
				}
				if err != nil {
					return fail(c, apperr.Conflict, "MetaRush couldn't save the captions there.", "Name a file that isn't there yet.")
				}
				out.Note += " Saved."
			}
			return emit(c, out, func() {
				switch {
				case save != "":
					fmt.Println(out.Note)
				case out.Text != "":
					fmt.Print(out.Text)
				default:
					fmt.Println(out.Note)
					for _, f := range out.Files {
						line := fmt.Sprintf("  %s: %d lines", firstNonEmpty(f.Name, f.Language), f.Lines)
						if f.Translated {
							line += ", machine-translated"
						}
						if f.MadeWith != "" {
							line += ", made with " + theirs(f.MadeWith)
						}
						if f.Model != "" {
							line += " (" + theirs(f.Model) + ")"
						}
						fmt.Println(line)
					}
				}
			})
		},
	}
	cmd.Flags().StringVar(&in.Language, "language", "", "one language's captions (a code such as en)")
	cmd.Flags().StringVar(&save, "save", "", "save that language's captions file here (never over a file already there)")
	return cmd
}
