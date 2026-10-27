package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// Captions in other languages (D238): `metarush captions languages|lines|
// translate|remove-language`. Claude Code translates the lines on the
// person's own plan; MetaRush keeps them and writes the files.
func captionsTranslationCmds() []*cobra.Command {
	var li ops.CaptionsLanguagesIn
	languages := &cobra.Command{
		Use:   "languages <project> [--version <rN>]",
		Short: "The languages a project's captions are translated into, and how many of a version's lines each has",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			li.ProjectID = args[0]
			return runOp(c, "captions.languages", li, func(o any) {
				out := o.(*ops.CaptionsLanguagesOut)
				fmt.Println(out.Summary)
				for _, l := range out.Languages {
					line := fmt.Sprintf("  %-24s %d of %d lines translated", l.Name, l.Translated, l.Lines)
					if l.Outdated > 0 {
						line += fmt.Sprintf(", %d to translate again", l.Outdated)
					}
					if l.Machine {
						line += " (machine-translated)"
					}
					fmt.Println(line)
				}
			})
		},
	}
	languages.Flags().StringVar(&li.Revision, "version", "", "a version (rN or its ID); default the latest")
	languages.Flags().StringVar(&li.Branch, "branch", "", "the cut's branch (default main)")

	var ln ops.CaptionsLinesIn
	var lnVersionWords bool
	lines := &cobra.Command{
		Use:   "lines <project> --to <language> [--version <rN>] [--missing]",
		Short: "A version's caption lines to translate into a language, what is translated already, and the spelling list as the glossary",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ln.ProjectID = args[0]
			if lnVersionWords {
				ln.Words = "version"
			}
			return runOp(c, "captions.lines", ln, func(o any) {
				out := o.(*ops.CaptionsLinesOut)
				fmt.Println(out.Summary)
				for _, l := range out.Lines {
					mark := " "
					switch l.State {
					case ops.LineMissing:
						mark = "·"
					case ops.LineOutdated:
						mark = "!"
					}
					fmt.Printf("  %s %3d  %s\n", mark, l.N, theirs(l.Source))
					if l.Text != "" {
						fmt.Printf("         %s\n", theirs(l.Text))
					}
				}
			})
		},
	}
	lines.Flags().StringVar(&ln.Language, "to", "", "the language (es, fr, de, pt-BR… or its name)")
	lines.Flags().StringVar(&ln.Revision, "version", "", "a version (rN or its ID); default the latest")
	lines.Flags().StringVar(&ln.Branch, "branch", "", "the cut's branch (default main)")
	lines.Flags().BoolVar(&ln.Missing, "missing", false, "only the lines that need translating")
	lines.Flags().BoolVar(&lnVersionWords, "version-words", false, "the words the version was made with, not today's")

	var tr ops.CaptionsTranslateIn
	var from string
	var trVersionWords bool
	translate := &cobra.Command{
		Use:   "translate <project> --to <language> --from <translation.json> [--version <rN>] [--human]",
		Short: "Keep a translation of a version's caption lines (marked as machine-translated unless --human); delivery writes a captions file per language",
		Long: "The file is {\"language\": \"es\", \"lines\": [{\"n\": 1, \"source\": \"…\", \"text\": \"…\"}]}, the lines as `captions lines` lists them.\n" +
			"Lines are matched by their words (or their number), so a later version that shows the same line has its translation too.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			tr.ProjectID = args[0]
			if trVersionWords {
				tr.Words = "version"
			}
			if from == "" {
				return fail(c, apperr.InvalidInput, "say which file holds the translation (--from)", "")
			}
			b, err := os.ReadFile(from)
			if err != nil {
				return fail(c, apperr.InvalidInput, "MetaRush can't read that translation: "+err.Error(), "")
			}
			lang, ls, err := ops.ParseTranslation(b)
			if err != nil {
				return failErr(c, err)
			}
			if tr.Language == "" {
				tr.Language = lang
			}
			tr.Lines = ls
			return runOp(c, "captions.translate", tr, func(o any) {
				out := o.(*ops.CaptionsTranslateOut)
				fmt.Println(out.Summary)
				for _, w := range out.Warnings {
					fmt.Println("  ! " + w)
				}
			})
		},
	}
	translate.Flags().StringVar(&tr.Language, "to", "", "the language (es, fr, de, pt-BR… or its name); default the file's")
	translate.Flags().StringVar(&from, "from", "", "the file with the translated lines")
	translate.Flags().StringVar(&tr.Revision, "version", "", "a version (rN or its ID); default the latest")
	translate.Flags().StringVar(&tr.Branch, "branch", "", "the cut's branch (default main)")
	translate.Flags().BoolVar(&tr.Human, "human", false, "a person translated it: not marked as machine-translated")
	translate.Flags().BoolVar(&trVersionWords, "version-words", false, "the lines are the version's own words, not today's")

	var rm ops.CaptionsLanguageRemoveIn
	remove := &cobra.Command{
		Use:   "remove-language <project> --to <language>",
		Short: "Take a language's translated captions off a project (delivered videos keep their files)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			rm.ProjectID = args[0]
			return runOp(c, "captions.translation.remove", rm, func(o any) { fmt.Println(o.(*ops.CaptionsLanguageRemoveOut).Summary) })
		},
	}
	remove.Flags().StringVar(&rm.Language, "to", "", "the language")
	return []*cobra.Command{languages, lines, translate, remove}
}
