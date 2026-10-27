package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// runLibraryAction runs a filing op over clips. A refusal reads as one plain
// sentence; the per-clip rows (with IDs) are in --details and --json.
func runLibraryAction(c *cobra.Command, op string, in any, details bool) error {
	a, err := openApp(c, "cli")
	if err != nil {
		return err
	}
	defer a.Close()
	ctx, cancel := signalCtx(c.Context())
	defer cancel()
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	out, err := ops.Call(ctx, a, op, b, ops.Caller{})
	if err != nil {
		if ae := apperr.From(err); !jsonMode(c) && !details {
			ae.Details = nil
			return failErr(c, ae)
		}
		return failErr(c, err)
	}
	return emit(c, out, func() { printActionOut(out.(*ops.LibraryActionOut), details) })
}

// The Library in the terminal (D96–D98): `metarush library` (the Inbox,
// collections, the Archive, footage in projects and the Trash, with counts;
// --search covers them all), `metarush asset archive|unarchive|trash|restore`,
// `metarush collection …` and `metarush trash list|empty|settings`. Plain
// output by default; --json for scripts and agents. Deleting for good
// (`trash empty`, `asset delete --forever`) is for people, in a terminal.

func printActionOut(o *ops.LibraryActionOut, details bool) {
	fmt.Println(o.Message)
	for _, r := range o.Items {
		if details || len(o.Items) > 1 || r.Outcome == ops.OutcomeRefused {
			line := fmt.Sprintf("  %s: %s", r.Title, r.Where)
			if r.Outcome == ops.OutcomeRefused {
				line = fmt.Sprintf("  %s: not changed (%s)", r.Title, r.Reason)
			}
			if details {
				line += "  [" + r.AssetID + "]"
			}
			fmt.Println(line)
		}
		if details {
			printUses(r.Uses, true)
		}
	}
}

func libraryActionCmd(use, short, op string, withProject bool) *cobra.Command {
	var in ops.LibraryActionIn
	var details bool
	c := &cobra.Command{
		Use: use, Short: short, Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.AssetIDs = args
			return runLibraryAction(c, op, in, details)
		},
	}
	if withProject {
		c.Flags().StringVar(&in.ProjectID, "project", "", "the project the clips leave (ID or name); refused while its cut uses them")
	}
	addDetailsFlag(c, &details)
	return c
}

// assetLibraryCmds are `metarush asset archive|unarchive|trash|restore`.
func assetLibraryCmds() []*cobra.Command {
	return []*cobra.Command{
		libraryActionCmd("archive <asset>... [--project <project>]", "File clips in the Archive (“keep for later”): out of the Inbox without a project; with --project they leave that project too. Nothing is deleted", "asset.archive", true),
		libraryActionCmd("unarchive <asset>...", "Take clips out of the Archive (back in the Inbox unless a project or collection has them)", "asset.unarchive", false),
		libraryActionCmd("trash <asset>... [--project <project>]", "Move clips to the Trash (they can be restored; nothing is deleted until the Trash is emptied); refused while a project's latest cut uses one or another project has it", "asset.trash", true),
		libraryActionCmd("restore <asset>...", "Put clips in the Trash back where they were (their project, collections, the Archive; else the Inbox)", "asset.restore", false),
	}
}

func printLibraryItem(it ops.LibraryItem, details bool) {
	line := fmt.Sprintf("%-34s %8s  %s", trunc(it.Title, 34), fmtDur(it.Duration), it.Where)
	if it.Trash != nil {
		line = fmt.Sprintf("%-34s %8s  %s, %s", trunc(it.Title, 34), fmtDur(it.Duration), strings.ToLower(it.Trash.From[:1])+it.Trash.From[1:], fmtBytes(it.Size))
		if it.Trash.DeletesOn != nil {
			line += ", deleted for good on " + it.Trash.DeletesOn.Local().Format("2 Jan 2006")
		}
	}
	if it.MatchedIn != "" {
		line += "  (matched its " + matchWord(it.MatchedIn) + ")"
	}
	fmt.Println(line)
	if details {
		fmt.Printf("    %s · %s · %s\n", it.ID, it.Name, fmtBytes(it.Size))
	}
}

func matchWord(m string) string {
	switch m {
	case "words":
		return "words (what was said)"
	case "source":
		return "source (where it came from)"
	case "collection":
		return "collection's name"
	}
	return m
}

func libraryCmd() *cobra.Command {
	var in ops.LibraryIn
	var details bool
	c := &cobra.Command{
		Use:   "library [inbox|collections|archived|projects|trash|all] [--collection <name>] [--search <words>]",
		Short: "The Library: the Inbox (footage nobody has filed yet), collections, the Archive, footage in projects and the Trash, with counts; --search looks through all of them",
		Long: `Footage arrives in the Inbox and leaves it one of four ways: added to a project,
put on a collection (a named shelf: B-roll, Family, Stock…), archived ("keep
for later") or moved to the Trash. Nothing filed away is hard to find:

  metarush library                         the Inbox
  metarush library archived                the Archive
  metarush library --collection B-roll     one collection
  metarush library trash                   the Trash, with sizes
  metarush library --search "beach"        every view: titles, file names, tags,
                                           collection names and what was said`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				in.View = args[0]
			}
			if in.Query != "" && in.View == "" {
				in.View = ops.ViewAll
			}
			return runOp(c, "library.list", in, func(o any) {
				out := o.(*ops.LibraryOut)
				counts := out.Counts
				fmt.Printf("Inbox %d · Collections %d · Archived %d · In projects %d · Trash %d\n", counts.Inbox, counts.Collections, counts.Archived, counts.Projects, counts.Trash)
				fmt.Println(out.Summary)
				if out.View == ops.ViewCollections && out.Collection == nil && len(out.Collections) > 0 {
					fmt.Println("Collections:")
					for _, col := range out.Collections {
						fmt.Printf("  %-30s %s\n", col.Name, col.Text)
					}
				}
				for _, it := range out.Items {
					printLibraryItem(it, details)
				}
				if len(out.Items) < out.Total {
					fmt.Printf("…and %d more.\n", out.Total-len(out.Items))
					if details {
						fmt.Println("(--limit shows more)")
					}
				}
			})
		},
	}
	c.Flags().StringVar(&in.Collection, "collection", "", "one collection (ID or name)")
	c.Flags().StringVar(&in.Query, "search", "", "words to look for in every view, the Trash included")
	c.Flags().IntVar(&in.Limit, "limit", 0, "at most this many clips (default 500)")
	addDetailsFlag(c, &details)
	return c
}

func printCollection(r ops.CollectionRow, details bool) {
	fmt.Printf("%-30s %s\n", r.Name, r.Text)
	if details {
		fmt.Printf("    %s · %s · %s\n", r.ID, fmtDur(r.Duration), fmtBytes(r.Size))
	}
}

func collectionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "collection",
		Aliases: []string{"collections"},
		Short:   "Collections: named shelves for footage (B-roll, Family, Stock…) that projects can use",
		Long: `A collection is a shelf you name. A clip can be on several collections and in
projects too; putting it on one takes it out of the Inbox. A project can use
everything on a collection, the way it uses another project:

  metarush collection create "B-roll"
  metarush collection add "B-roll" <asset>... [--create]
  metarush project link "Spring promo" "B-roll"      the project sees every clip on it`,
	}
	var details bool
	create := &cobra.Command{
		Use: "create <name>", Short: "Create a collection", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "collection.create", ops.CollectionCreateIn{Name: args[0]}, func(o any) { fmt.Println(o.(*ops.CollectionOut).Message) })
		},
	}
	rename := &cobra.Command{
		Use: "rename <collection> <new name>", Short: "Rename a collection", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "collection.rename", ops.CollectionRenameIn{Collection: args[0], Name: args[1]}, func(o any) { fmt.Println(o.(*ops.CollectionOut).Message) })
		},
	}
	var createToo bool
	add := &cobra.Command{
		Use: "add <collection> <asset>... [--create]", Short: "Put clips on a collection (they leave the Inbox and stay in their projects); --create makes the collection if there is none by that name", Args: cobra.MinimumNArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			return runLibraryAction(c, "collection.add", ops.CollectionItemsIn{Collection: args[0], AssetIDs: args[1:], Create: createToo}, details)
		},
	}
	add.Flags().BoolVar(&createToo, "create", false, "create the collection if there is none by that name")
	addDetailsFlag(add, &details)
	remove := &cobra.Command{
		Use: "remove <collection> <asset>...", Short: "Take clips off a collection (nothing is deleted; a clip on nothing else goes back to the Inbox)", Args: cobra.MinimumNArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			return runLibraryAction(c, "collection.remove", ops.CollectionItemsIn{Collection: args[0], AssetIDs: args[1:]}, details)
		},
	}
	addDetailsFlag(remove, &details)
	list := &cobra.Command{
		Use: "list", Short: "List collections: how many clips each has and the projects that use it", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "collection.list", ops.CollectionListIn{}, func(o any) {
				out := o.(*ops.CollectionListOut)
				if len(out.Collections) == 0 {
					fmt.Println("No collections yet.")
					if details {
						fmt.Println("make one: metarush collection create <name>")
					}
				}
				for _, r := range out.Collections {
					printCollection(r, details)
				}
			})
		},
	}
	addDetailsFlag(list, &details)
	var showLimit int
	show := &cobra.Command{
		Use: "show <collection>", Short: "Show a collection: its clips (and where else each one is) and the projects that use it", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "collection.show", ops.CollectionShowIn{Collection: args[0], Limit: showLimit}, func(o any) {
				out := o.(*ops.CollectionShowOut)
				printCollection(out.Collection, details)
				for _, l := range out.Links {
					fmt.Println("  " + l.ProjectName + " uses it: " + l.Text)
				}
				for _, it := range out.Items {
					printLibraryItem(it, details)
				}
			})
		},
	}
	show.Flags().IntVar(&showLimit, "limit", 0, "at most this many clips (default 500)")
	addDetailsFlag(show, &details)
	del := &cobra.Command{
		Use: "delete <collection>", Short: "Delete a collection, never its clips (a clip on nothing else goes back to the Inbox; clips a project's cut uses through it join that project)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "collection.delete", ops.CollectionRefIn{Collection: args[0]}, func(o any) { fmt.Println(o.(*ops.CollectionDeleteOut).Message) })
		},
	}
	cmd.AddCommand(create, rename, add, remove, list, show, del)
	return cmd
}

func trashCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trash",
		Short: "The Trash: clips moved there wait until a person deletes them for good (list, empty, settings)",
		Long: `Moving a clip to the Trash (metarush asset trash) can be undone with
metarush asset restore. Deleting for good is a second step, for people only:

  metarush trash list                      what is there, from where, and how big
  metarush trash empty                     delete everything in it for good (asks first)
  metarush trash empty <asset>...          only these
  metarush trash settings --empty-after 30 delete clips 30 days after they go there
  metarush trash settings --off            nothing is deleted until you empty it (the default)`,
	}
	var details bool
	list := &cobra.Command{
		Use: "list", Short: "List what is in the Trash: from where, how big, and whether each can be deleted for good", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "trash.list", ops.TrashListIn{}, func(o any) {
				out := o.(*ops.TrashListOut)
				fmt.Println(out.Summary)
				for _, r := range out.Items {
					printLibraryItem(r.LibraryItem, details)
					if !r.CanDelete && len(r.Blockers) > 0 {
						fmt.Println("    stays: " + r.Blockers[0])
					}
				}
			})
		},
	}
	addDetailsFlag(list, &details)
	var empty ops.TrashEmptyIn
	emptyCmd := &cobra.Command{
		Use:   "empty [<asset>...] [--dry-run]",
		Short: "Delete what is in the Trash for good (or only the clips named), with previews, transcripts and speaker data, on every machine; clips a cut uses stay — people only, in their own terminal",
		RunE: func(c *cobra.Command, args []string) error {
			empty.AssetIDs = args
			if assistantMode() || !interactive() {
				return fail(c, apperr.HumanOnly, "Only a person can empty the Trash, in the dashboard or in their own terminal.", "Run this yourself in a terminal (agents, scripts and the dashboard assistant can't delete for good).")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			check := empty
			check.DryRun = true
			res, err := ops.CallTyped[ops.TrashEmptyOut](c.Context(), a, "trash.empty", check, ops.Caller{Human: true})
			if err != nil {
				return failErr(c, err)
			}
			if empty.DryRun || res.Count == 0 {
				return emit(c, res, func() {
					fmt.Println(res.Message)
					for _, k := range res.Kept {
						fmt.Printf("  stays: %s (%s)\n", k.Title, k.Reason)
					}
				})
			}
			if !jsonMode(c) {
				for _, it := range res.Items {
					fmt.Printf("  %s (%s)\n", it.Title, fmtBytes(it.Size))
				}
				for _, k := range res.Kept {
					fmt.Printf("  stays: %s (%s)\n", k.Title, k.Reason)
				}
				fmt.Println(res.Question + " This can't be undone.")
			}
			if ask(fmt.Sprintf("Type %q to delete for good", "delete"), "") != "delete" {
				return fail(c, apperr.Canceled, "nothing was deleted", "")
			}
			return runOpWith(c, a, "trash.empty", empty, ops.Caller{Human: true}, func(o any) { fmt.Println(o.(*ops.TrashEmptyOut).Message) })
		},
	}
	emptyCmd.Flags().BoolVar(&empty.DryRun, "dry-run", false, "only say what would be deleted and what stays")
	var after int
	var off bool
	settings := &cobra.Command{
		Use: "settings [--empty-after <days> | --off]", Short: "Whether the Trash empties itself after a number of days (off unless a person turns it on)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			show := func(o any) { fmt.Println(o.(*ops.TrashSettingsOut).Summary) }
			if !c.Flags().Changed("empty-after") && !off {
				return runOp(c, "trash.settings", ops.TrashSettingsIn{}, show)
			}
			if off && c.Flags().Changed("empty-after") {
				return fail(c, apperr.InvalidInput, "choose --empty-after or --off", "")
			}
			days := after
			if off {
				days = 0
			}
			if days < 0 || days > 3650 {
				return fail(c, apperr.InvalidInput, "days must be between 1 and 3650", "--off turns it off")
			}
			if assistantMode() || !interactive() {
				return fail(c, apperr.HumanOnly, "Only a person can change when the Trash empties itself.", "Run this yourself in a terminal, or use Settings in the dashboard.")
			}
			if days > 0 && !askYes(fmt.Sprintf("Clips will be deleted for good %s after they go to the Trash, on every machine. Turn this on?", plural(days, "day")), false) {
				return fail(c, apperr.Canceled, "not changed", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "trash.configure", ops.TrashConfigureIn{EmptyAfterDays: days}, ops.Caller{Human: true}, show)
		},
	}
	settings.Flags().IntVar(&after, "empty-after", 0, "delete clips this many days after they go to the Trash")
	settings.Flags().BoolVar(&off, "off", false, "never delete on its own (the default)")
	cmd.AddCommand(list, emptyCmd, settings)
	return cmd
}
