package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/guests"
	"github.com/metavert-io/metarush/internal/ops"
)

// guestsCmd is `metarush guests …` (D490–D504): people outside the workspace
// an owner shares a project or some of its versions with, who watch the
// review copies and, as commenters, leave comments on MetaRush's guest page.
// Sharing, sign-in links and taking things back are an owner's, a person's
// step (typed at a terminal, or in the dashboard).
func guestsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "guests",
		Short: "Share versions with guests outside the workspace: who has what, sign-in links, taking things back, what they did",
		Long: `A guest is someone outside the workspace an owner shares a project (every version with a review copy) or some
of its versions with, as a viewer (watches) or a commenter (watches and comments), until a day the owner chooses.
Guests open MetaRush's guest page, never the dashboard. Nothing is reachable from the internet yet: the guest
address is this computer's own.

  metarush guests status                                            are guests on, where they are answered, how they sign in
  metarush guests on                                                let guests in (this computer answers them; owners)
  metarush guests share "Spring promo" --email dana@example.com --role commenter --days 14
  metarush guests link dana@example.com                             a one-time sign-in link to send them
  metarush guests list                                              who has what
  metarush guests view dana@example.com                             what a guest sees (owners)
  metarush guests revoke dana@example.com                           take back everything shared with them
  metarush guests activity                                          what guests did (owners)`,
	}
	cmd.AddCommand(guestsStatusCmd(), guestsSwitchCmd(true), guestsSwitchCmd(false), guestsSetCmd(), guestsShareCmd(), guestsListCmd(), guestsLinkCmd(),
		guestsRevokeCmd(), guestsActivityCmd(), guestsViewCmd(), guestsCommentsCmd())
	return cmd
}

func guestsStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Whether guests are let in, where this computer answers them, how they sign in, and how many have something shared",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "guests.status", guests.GuestsStatusIn{}, func(o any) { printGuestsStatus(c, o.(*guests.GuestsStatusOut)) })
		},
	}
}

func printGuestsStatus(c *cobra.Command, out *guests.GuestsStatusOut) {
	for _, l := range out.Lines {
		fmt.Println(l)
	}
	if detailsMode(c) {
		fmt.Printf("  guest address %s (port %d), listening %v, answering now %v\n", out.Address, out.Port, out.Listening, out.Running)
		if out.SSOCallback != "" {
			fmt.Printf("  address for guests' sign-in with %s: %s\n", out.SSO, out.SSOCallback)
		}
	}
}

// guestsSwitchCmd is `guests on` and `guests off`.
func guestsSwitchCmd(on bool) *cobra.Command {
	var port int
	use, short := "off", "Turn guests off: every guest is signed out at once, and unused sign-in links stop working (owners, a person's step)"
	if on {
		use, short = "on", "Let guests in: this computer answers them on 127.0.0.1 (owners, a person's step, at this computer)"
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			v := on
			in := guests.GuestsSettingsSetIn{On: &v}
			if on && c.Flags().Changed("port") {
				in.Port = &port
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "guests.settings.set", in, ops.Caller{Human: interactive()}, func(o any) { printGuestsStatus(c, o.(*guests.GuestsStatusOut)) })
		},
	}
	if on {
		cmd.Flags().IntVar(&port, "port", 0, "where this computer answers guests on 127.0.0.1 (default 4846)")
	}
	return cmd
}

func guestsSetCmd() *cobra.Command {
	var port int
	cmd := &cobra.Command{
		Use:   "set --port N",
		Short: "Move where this computer answers guests on 127.0.0.1 (owners, a person's step)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if !c.Flags().Changed("port") {
				return fail(c, apperr.InvalidInput, "Say the port: --port 4847.", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "guests.settings.set", guests.GuestsSettingsSetIn{Port: &port}, ops.Caller{Human: interactive()}, func(o any) { printGuestsStatus(c, o.(*guests.GuestsStatusOut)) })
		},
	}
	cmd.Flags().IntVar(&port, "port", 0, "the port, from 1024 to 65535 (not the dashboard's)")
	return cmd
}

func guestsShareCmd() *cobra.Command {
	var in guests.GuestsShareIn
	cmd := &cobra.Command{
		Use:   "share <project> --email <email> --role viewer|commenter (--days N | --until YYYY-MM-DD) [--version rN …] [--name …] [--message …] [--dry-run]",
		Short: "Share a project (every version with a review copy) or some of its versions with a guest, until a day you choose (owners, a person's step)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ProjectID = args[0]
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "guests.share", in, ops.Caller{Human: interactive()}, func(o any) {
				out := o.(*guests.GuestsShareOut)
				fmt.Println(out.Summary)
				if out.May || out.Shared {
					fmt.Println("The guest sees: " + strings.Join(out.Exposes, "; ") + ".")
				} else if out.Hint != "" {
					fmt.Println(out.Hint)
				}
				for _, w := range out.Warnings {
					fmt.Println("  " + w)
				}
				if out.Shared && detailsMode(c) && out.Grant != nil {
					fmt.Printf("  %s for guest %s\n", out.Grant.ID, out.Grant.GuestID)
				}
			})
		},
	}
	cmd.Flags().StringVar(&in.Email, "email", "", "the guest's email (they sign in with a link sent there, or their organization's account)")
	cmd.Flags().StringVar(&in.Name, "name", "", "what to call them (\"Dana at the client\")")
	cmd.Flags().StringSliceVar(&in.Versions, "version", nil, "a version to share (rN or its ID; repeatable); none: every version with a review copy")
	cmd.Flags().StringVar(&in.Role, "role", "", "viewer (watches) or commenter (watches and comments)")
	cmd.Flags().IntVar(&in.Days, "days", 0, "how many days it works, from now (at most 90)")
	cmd.Flags().StringVar(&in.Until, "until", "", "its last day instead, like 2026-11-30")
	cmd.Flags().StringVar(&in.Message, "message", "", "a note their guest page shows")
	cmd.Flags().BoolVar(&in.DryRun, "dry-run", false, "say what the guest would see and whether it may be shared, sharing nothing")
	return cmd
}

func guestsListCmd() *cobra.Command {
	var in guests.GuestsListIn
	cmd := &cobra.Command{
		Use:   "list [<project>] [--all]",
		Short: "Guests and what each may see: projects or versions, viewer or commenter, until when, signed in or not",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				in.ProjectID = args[0]
			}
			return runOp(c, "guests.list", in, func(o any) {
				out := o.(*guests.GuestsListOut)
				fmt.Println(out.Summary)
				for _, g := range out.Guests {
					fmt.Println("  " + g.Summary)
					for _, gr := range g.Grants {
						line := "    " + gr.Summary
						if detailsMode(c) {
							line += " (" + gr.Grant.ID + ")"
						}
						fmt.Println(line)
					}
				}
			})
		},
	}
	cmd.Flags().BoolVar(&in.All, "all", false, "also what ended or was taken back")
	return cmd
}

func guestsLinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "link <guest's email>",
		Short: "A one-time sign-in link for a guest, to send them yourself: it works once, for a day at most (owners, a person's step)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "guests.link", guests.GuestsLinkIn{Guest: args[0]}, ops.Caller{Human: interactive()}, func(o any) {
				out := o.(*guests.GuestsLinkOut)
				fmt.Println(out.Summary)
				fmt.Println(out.URL)
				for _, w := range out.Warnings {
					fmt.Println("  " + w)
				}
			})
		},
	}
}

func guestsRevokeCmd() *cobra.Command {
	var grant string
	cmd := &cobra.Command{
		Use:   "revoke <guest's email> | --grant <id>",
		Short: "Take back everything shared with a guest (or one thing, --grant): it stops at once and, with nothing left, they are signed out (owners, a person's step)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in := guests.GuestsRevokeIn{Grant: grant}
			if len(args) == 1 {
				in.Guest = args[0]
			}
			if (in.Guest == "") == (in.Grant == "") {
				return fail(c, apperr.InvalidInput, "Say whose things to take back (their email), or one thing with --grant.", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "guests.revoke", in, ops.Caller{Human: interactive()}, func(o any) { fmt.Println(o.(*guests.GuestsRevokeOut).Summary) })
		},
	}
	cmd.Flags().StringVar(&grant, "grant", "", "one thing shared (its ID, from `metarush guests list --details`)")
	return cmd
}

func guestsActivityCmd() *cobra.Command {
	var in guests.GuestsActivityIn
	cmd := &cobra.Command{
		Use:   "activity [--guest <email>] [--project <project>] [--refused] [--limit N]",
		Short: "What guests did, newest first: who, from which address, what they opened, watched or wrote, and what was turned away (owners)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "guests.activity", in, func(o any) {
				out := o.(*guests.GuestsActivityOut)
				fmt.Println(out.Summary)
				for _, r := range out.Rows {
					fmt.Printf("  %s  %s %s\n", r.Activity.At.Local().Format("2 Jan 15:04"), r.Who, r.Says)
				}
			})
		},
	}
	cmd.Flags().StringVar(&in.Guest, "guest", "", "one guest's (their email)")
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "one project's")
	cmd.Flags().BoolVar(&in.Refused, "refused", false, "only what was turned away")
	cmd.Flags().IntVar(&in.Limit, "limit", 0, "how many (default 100, at most 500)")
	return cmd
}

// guestsViewCmd shows an owner what a guest sees (the guest API, read-only,
// as that guest; the registry lets owners alone look).
func guestsViewCmd() *cobra.Command {
	var version string
	cmd := &cobra.Command{
		Use:   "view <guest's email> [--version <id>]",
		Short: "What a guest sees: what was shared with them, or one version's review copy as their page plays it (owners, a person's step)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			if version != "" {
				return runOpWith(c, a, "guest.watch", guests.GuestWatchIn{Version: version, As: args[0]}, ops.Caller{Human: interactive()}, func(o any) {
					out := o.(*guests.GuestWatchOut)
					fmt.Println(out.Summary)
					if detailsMode(c) {
						fmt.Println("  " + out.URL)
					}
				})
			}
			return runOpWith(c, a, "guest.shared", guests.GuestSharedIn{As: args[0]}, ops.Caller{Human: interactive()}, func(o any) {
				out := o.(*guests.GuestSharedOut)
				fmt.Println(out.Summary)
				for _, sh := range out.Shares {
					can := "watch"
					if sh.CanComment {
						can = "watch and comment"
					}
					fmt.Printf("  %s: can %s %s\n", theirs(sh.Project), can, sh.EndsWords)
					for _, v := range sh.Versions {
						ready := ""
						if !v.Ready {
							ready = " (no review copy yet)"
						}
						line := fmt.Sprintf("    version %d%s", v.Number, ready)
						if detailsMode(c) {
							line += " " + v.ID
						}
						fmt.Println(line)
					}
				}
			})
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "one version's review copy (its ID)")
	return cmd
}

func guestsCommentsCmd() *cobra.Command {
	var version string
	cmd := &cobra.Command{
		Use:   "comments <guest's email> --version <id>",
		Short: "A guest's comments on a version shared with them, as their page shows them (owners, a person's step)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "guest.comments", guests.GuestCommentsIn{Version: version, As: args[0]}, ops.Caller{Human: interactive()}, func(o any) {
				out := o.(*guests.GuestCommentsOut)
				fmt.Println(out.Summary)
				for _, cm := range out.Comments {
					at := ""
					if cm.At != nil {
						at = fmt.Sprintf(" at %d:%02d", int(*cm.At)/60, int(*cm.At)%60)
					}
					fmt.Printf("  %s%s: %s\n", cm.Made.Local().Format("2 Jan 15:04"), at, theirs(cm.Body))
				}
			})
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "the version (its ID)")
	return cmd
}
