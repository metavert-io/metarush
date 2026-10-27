package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/reviewlinks"
)

// shareCmd is `metarush share …` (D313–D317): send a version for review to
// someone outside the workspace — an expiring link to its review copy, its
// words if you allow, and comments pinned to times that come back as the
// version's comments.
func shareCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "share",
		Short: "Send a version for review to someone outside the workspace: an expiring link to its review copy, with its words and comments pinned to times",
		Long: `A review link is a private page kept in the workspace's own cloud bucket until it ends: the version's review
copy, its words if you allow, and a place to leave comments pinned to times. Their comments come back as the
version's comments, marked "(outside)". Making a link is a person's step; owners decide who may and for how long.

  metarush share plan "Spring promo" --words                  what a link would show, and whether it may be made
  metarush share create "Spring promo" --version r3 --words --for "Dana" --message "Is the ending right?"
  metarush share list "Spring promo"                          the links that work now (--all: every one)
  metarush share revoke <link>                                take one back at once
  metarush share sync                                         bring in new comments now`,
	}
	cmd.AddCommand(sharePlanCmd(), shareCreateCmd(), shareListCmd(), shareLinkCmd(), shareRevokeCmd(), shareSyncCmd(), shareSettingsCmd())
	return cmd
}

func shareFlags(c *cobra.Command, in *reviewlinks.ShareCreateIn) {
	c.Flags().StringVar(&in.Revision, "version", "", "a version (rN or its ID); default the latest")
	c.Flags().StringVar(&in.Branch, "branch", "", "the cut's branch (default main)")
	c.Flags().IntVar(&in.Days, "days", 0, "how many days the link works (default 7, or the most owners allow)")
	c.Flags().BoolVar(&in.Words, "words", false, "show what is said, line by line (the Script view)")
	c.Flags().StringVar(&in.Speakers, "speakers", "", "with --words, how it names who speaks: names, labels (Speaker 1, 2…; the default) or none")
	c.Flags().BoolVar(&in.Download, "download", false, "offer the video to download")
	c.Flags().StringVar(&in.Title, "title", "", "the title the page shows (default the cut's title and version)")
	c.Flags().StringVar(&in.For, "for", "", "who it's for (the page fills in their name)")
	c.Flags().StringVar(&in.Message, "message", "", "a note the page shows them")
	c.Flags().BoolVar(&in.AsDraft, "as-draft", false, "mark the page as a draft for review (not approved yet)")
	c.Flags().StringVar(&in.Anyway, "anyway", "", "a note sending a version the ship gate holds")
}

func sharePlanCmd() *cobra.Command {
	var in reviewlinks.ShareCreateIn
	cmd := &cobra.Command{
		Use:   "plan <project> [--version rN] [--words] [--download] …",
		Short: "What a review link would show, for how long, and whether it may be made (nothing is made)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ProjectID = args[0]
			return runOp(c, "share.plan", in, func(o any) { printSharePlan(o.(*reviewlinks.SharePlanOut)) })
		},
	}
	shareFlags(cmd, &in)
	return cmd
}

func shareCreateCmd() *cobra.Command {
	var in reviewlinks.ShareCreateIn
	cmd := &cobra.Command{
		Use:   "create <project> [--version rN] [--days 7] [--words [--speakers names|labels|none]] [--download] [--for <name>] [--message <note>]",
		Short: "Make a review link to send (a person's step: typed in a terminal, or Share for review… on the version's page)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ProjectID = args[0]
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "share.create", in, ops.Caller{Human: interactive()}, func(o any) { printShare(o.(*reviewlinks.ShareOut), true, detailsMode(c)) })
		},
	}
	shareFlags(cmd, &in)
	return cmd
}

func shareListCmd() *cobra.Command {
	var in reviewlinks.ShareListIn
	cmd := &cobra.Command{
		Use:   "list [<project>] [--version rN] [--all]",
		Short: "Review links that work now (--all: every one sent), with what each shows and the comments that came back",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				in.ProjectID = args[0]
			}
			return runOp(c, "share.list", in, func(o any) {
				out := o.(*reviewlinks.ShareListOut)
				fmt.Println(out.Summary)
				for _, v := range out.Links {
					fmt.Println("  " + v.Summary)
					if detailsMode(c) {
						fmt.Printf("    %s · shows %s\n", v.Link.ID, strings.Join(v.Link.Exposes, ", "))
					}
				}
			})
		},
	}
	cmd.Flags().StringVar(&in.Revision, "version", "", "only links to this version (rN or its ID)")
	cmd.Flags().BoolVar(&in.All, "all", false, "every link sent, ended ones too")
	return cmd
}

func shareLinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "link <link>",
		Short: "The link to send again, for a review link that still works (a person's step)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "share.link", reviewlinks.ShareIDIn{ID: args[0]}, ops.Caller{Human: interactive()}, func(o any) { printShare(o.(*reviewlinks.ShareOut), true, detailsMode(c)) })
		},
	}
}

func shareRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <link>",
		Short: "Take a review link back: its page and video are deleted at once, its comment slots once their last comments are read; comments already in stay (a person's step)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "share.revoke", reviewlinks.ShareIDIn{ID: args[0]}, ops.Caller{Human: interactive()}, func(o any) { printShare(o.(*reviewlinks.ShareOut), false, detailsMode(c)) })
		},
	}
}

func shareSyncCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sync [<link>]",
		Short: "Bring in new comments from review links now, and end links whose time is up (the dashboard's computer does it every two minutes)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var in reviewlinks.ShareSyncIn
			if len(args) == 1 {
				in.ID = args[0]
			}
			return runOp(c, "share.sync", in, func(o any) {
				out := o.(*reviewlinks.ShareSyncOut)
				fmt.Println(out.Summary)
				for _, l := range out.Lines {
					fmt.Println("  " + l)
				}
			})
		},
	}
}

func shareSettingsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "Who may send review links and for how long, and whether this workspace's media store can hold their pages",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "share.settings", ops.EmptyIn{}, func(o any) { fmt.Println(o.(*reviewlinks.ShareSettingsOut).Summary) })
		},
	}
	var on, off, ownersOnly, everyone bool
	var maxDays int
	set := &cobra.Command{
		Use:   "set [--on|--off] [--owners-only|--everyone] [--max-days N]",
		Short: "Turn review links on or off, let only owners send them, or shorten the longest a link lasts (owners, a person's step)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			var in reviewlinks.ShareSettingsSetIn
			switch {
			case on && off:
				return fail(c, "E_INVALID_INPUT", "Say --on or --off, not both.", "")
			case on || off:
				v := on
				in.On = &v
			}
			switch {
			case ownersOnly && everyone:
				return fail(c, "E_INVALID_INPUT", "Say --owners-only or --everyone, not both.", "")
			case ownersOnly || everyone:
				v := ownersOnly
				in.OwnersOnly = &v
			}
			if c.Flags().Changed("max-days") {
				in.MaxDays = &maxDays
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "share.settings.set", in, ops.Caller{Human: interactive()}, func(o any) { fmt.Println(o.(*reviewlinks.ShareSettingsOut).Summary) })
		},
	}
	set.Flags().BoolVar(&on, "on", false, "let people send review links")
	set.Flags().BoolVar(&off, "off", false, "turn review links off for everyone")
	set.Flags().BoolVar(&ownersOnly, "owners-only", false, "only owners send review links")
	set.Flags().BoolVar(&everyone, "everyone", false, "anyone in the workspace sends review links")
	set.Flags().IntVar(&maxDays, "max-days", 7, "the longest a link lasts, 1 to 7 days")
	cmd.AddCommand(set)
	return cmd
}

func printSharePlan(out *reviewlinks.SharePlanOut) {
	fmt.Println(out.Summary)
	if !out.May && out.Hint != "" {
		fmt.Println(out.Hint)
	}
	for _, w := range out.Warnings {
		fmt.Println("Note: " + w)
	}
}

func printShare(out *reviewlinks.ShareOut, withURL, details bool) {
	fmt.Println(out.Summary)
	if withURL && out.URL != "" {
		fmt.Println()
		fmt.Println(out.URL)
		fmt.Println()
		fmt.Printf("It works %s. Anyone with the link can watch it; you can take it back at any time.\n", out.Ends)
	}
	for _, w := range out.Warnings {
		fmt.Println("Note: " + w)
	}
	if details {
		fmt.Printf("Link %s · shows %s · take it back: metarush share revoke %s\n", out.Link.ID, strings.Join(out.Link.Exposes, ", "), out.Link.ID)
	}
}
