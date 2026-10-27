package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// deliverNotesCmd is `metarush deliver notes show|set` (D237): a version's
// title, description, chapters and show notes, written by Claude Code (on the
// person's own plan) or by a person, and written next to the video when it is
// delivered.
func deliverNotesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notes",
		Short: "A version's delivery notes: its title, description, chapters and show notes (written next to the video when it is delivered)",
	}
	var sh ops.DeliveryNotesIn
	show := &cobra.Command{
		Use:   "show <project> [--version <rN>]",
		Short: "Show a version's delivery notes as delivery writes them",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			sh.ProjectID = args[0]
			return runOp(c, "delivery.notes", sh, func(o any) { printNotes(o.(*ops.DeliveryNotesOut), false) })
		},
	}
	show.Flags().StringVar(&sh.Revision, "version", "", "a version (rN or its ID); default the latest")
	show.Flags().StringVar(&sh.Branch, "branch", "", "the cut's branch (default main)")

	var in ops.DeliveryNotesSetIn
	var from, showNotesFrom string
	var chapters, titles []string
	set := &cobra.Command{
		Use:   "set <project> [--version <rN>] --from <notes.json|notes.md> | --title … --description … --chapter \"0:00 Intro\"…",
		Short: "Save a version's delivery notes (the version itself never changes); typing it yourself, --by claude when you paste what Claude Code wrote",
		Long: "The file is JSON ({\"title\", \"titles\", \"description\", \"chapters\": [{\"at\": \"1:05\", \"title\"}], \"showNotes\"}) or Markdown with\n" +
			"the sections \"# <title>\", \"## Other titles\", \"## Description\", \"## Chapters\" (lines like \"1:05 The garden\") and \"## Show notes\".\n" +
			"Chapters are times into the video, the first at 0:00. Flags given with --from replace what the file says.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ProjectID = args[0]
			if from != "" {
				b, err := os.ReadFile(from)
				if err != nil {
					return fail(c, apperr.InvalidInput, "MetaRush can't read that notes file: "+err.Error(), "")
				}
				d, err := ops.ParseDeliveryNotes(b)
				if err != nil {
					return failErr(c, err)
				}
				in.Notes = *d
			}
			var title, description *string
			if c.Flags().Changed("title") {
				v := c.Flag("title").Value.String()
				title = &v
			}
			if c.Flags().Changed("description") {
				v := c.Flag("description").Value.String()
				description = &v
			}
			if err := ops.NotesFlags(&in.Notes, title, description, titles, chapters); err != nil {
				return failErr(c, err)
			}
			if showNotesFrom != "" {
				b, err := os.ReadFile(showNotesFrom)
				if err != nil {
					return fail(c, apperr.InvalidInput, "MetaRush can't read that show notes file: "+err.Error(), "")
				}
				in.Notes.ShowNotes = string(b)
			}
			// A person typing in a terminal says who wrote them (default:
			// they did); from Claude Code or a script the channel says.
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "delivery.notes.set", in, ops.Caller{Human: interactive()}, func(o any) { printNotes(o.(*ops.DeliveryNotesOut), true) })
		},
	}
	set.Flags().StringVar(&in.Revision, "version", "", "a version (rN or its ID); default the latest")
	set.Flags().StringVar(&in.Branch, "branch", "", "the cut's branch (default main)")
	set.Flags().StringVar(&from, "from", "", "a JSON or Markdown file with the notes")
	set.Flags().String("title", "", "the title to post the video under")
	set.Flags().StringArrayVar(&titles, "other-title", nil, "another title to choose from (repeat)")
	set.Flags().String("description", "", "the description (delivery adds the chapters after it)")
	set.Flags().StringArrayVar(&chapters, "chapter", nil, "a chapter: a time into the video and its name, like \"1:05 The garden\" (repeat; the first at 0:00)")
	set.Flags().StringVar(&showNotesFrom, "show-notes-from", "", "a Markdown file with the show notes")
	set.Flags().StringVar(&in.WrittenBy, "by", "", "who wrote them, when you type the command yourself: claude (Claude Code), person or agent (default: you); from Claude Code or a script the channel says")
	set.Flags().BoolVar(&in.Clear, "clear", false, "remove the version's delivery notes")
	cmd.AddCommand(show, set)
	return cmd
}

func printNotes(out *ops.DeliveryNotesOut, saved bool) {
	fmt.Println(out.Summary)
	if out.Notes != nil && !saved {
		n := out.Notes
		if n.Title != "" {
			fmt.Printf("Title: %s\n", theirs(n.Title))
		}
		for _, t := range n.Titles {
			fmt.Printf("  or: %s\n", theirs(t))
		}
		if n.Description != "" {
			fmt.Println("Description:")
			for _, l := range strings.Split(n.Description, "\n") {
				fmt.Println("  " + l)
			}
		}
		if len(out.Chapters) > 0 {
			fmt.Println("Chapters:")
			for _, l := range out.Chapters {
				fmt.Println("  " + l)
			}
		}
		if n.ShowNotes != "" {
			fmt.Println("Show notes:")
			for _, l := range strings.Split(n.ShowNotes, "\n") {
				fmt.Println("  " + l)
			}
		}
	}
	for _, w := range out.Warnings {
		fmt.Println("  ! " + w)
	}
}
