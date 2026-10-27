package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/publish"
)

// publishCmd is `metarush publish …` (D305): posting an approved version to a
// video platform, as a person, every time.
func publishCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "publish",
		Short: "Post an approved version to YouTube as you (people only, every time), and sign in to it on this computer",
		Long: `Post an approved version to a video platform (YouTube) through your own sign-in on this computer. It is a
person's step every time: Claude, Auto and AI apps never post.

  metarush publish platforms                         who you're signed in as, and what to do next
  metarush publish setup youtube                     an owner, once per computer: their own Google Cloud client
  metarush publish connect youtube                   sign in with your own channel (your browser opens)
  metarush publish plan "Garden walk" --version r3   exactly what would be posted, and what holds it
  metarush publish post "Garden walk" --version r3 [--privacy unlisted]

The title, description and chapters come from the version's delivery notes; captions files go with it; it is
posted private unless you say otherwise. docs/publishing.md has the setup.`,
	}
	c.AddCommand(publishPlatformsCmd(), publishSetupCmd(), publishConnectCmd(), publishPlanCmd(), publishSendCmd())
	return c
}

func publishPlatformsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "platforms",
		Short: "The video platforms MetaRush posts to and your sign-in to each on this computer",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "publish.platforms", ops.PublishPlatformsIn{}, func(o any) {
				for _, p := range o.(*ops.PublishPlatformsOut).Platforms {
					switch {
					case !p.SetUp:
						fmt.Printf("%s: not set up on this computer.\n", p.Name)
					case p.As == nil:
						fmt.Printf("%s: set up; you aren't signed in on this computer.\n", p.Name)
					default:
						fmt.Printf("%s: signed in as “%s”.\n", p.Name, p.As.Channel)
					}
					fmt.Printf("  %s\n", p.Next)
					if p.Command != "" {
						fmt.Printf("  %s\n", p.Command)
					}
				}
			})
		},
	}
}

func publishSetupCmd() *cobra.Command {
	var in ops.PublishSetupIn
	c := &cobra.Command{
		Use:   "setup youtube [--client-id <id>] [--remove]",
		Short: "Register MetaRush with YouTube on this computer with your own Google Cloud client (owners, at this computer); the secret is asked for, never typed on the command line",
		Long: `Register MetaRush with YouTube on this computer, once, with an OAuth client from your own Google Cloud
project (the kind "Desktop app", with the YouTube Data API v3 turned on). The client ID and secret are kept in
this computer's keychain. MetaRush ships no client of its own. docs/publishing.md walks through it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Platform = args[0]
			if !in.Remove {
				if !interactive() {
					return fail(c, apperr.HumanOnly, "setting up a platform asks for its secret at this computer's terminal", "Run it yourself in a terminal on this computer.")
				}
				if in.ClientID == "" {
					in.ClientID = ask("Client ID", "")
				}
				in.ClientSecret = askSecret("Client secret")
			}
			return runHuman(c, "publish.setup", in, func(o any) { fmt.Println(o.(*ops.PublishConnectOut).Summary) })
		},
	}
	c.Flags().StringVar(&in.ClientID, "client-id", "", "the OAuth client ID (the secret is asked for)")
	c.Flags().BoolVar(&in.Remove, "remove", false, "forget the client on this computer")
	return c
}

func publishConnectCmd() *cobra.Command {
	var in ops.PublishConnectIn
	c := &cobra.Command{
		Use:   "connect youtube [--forget]",
		Short: "Sign in to YouTube with your own channel, in your browser on this computer (people only); --forget signs out and takes the permission back",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Platform = args[0]
			if !in.Forget && !jsonMode(c) {
				fmt.Println("Your browser opens Google's sign-in: choose the account with your channel and allow MetaRush to upload videos and manage their captions. Waiting…")
			}
			return runHuman(c, "publish.connect", in, func(o any) { fmt.Println(o.(*ops.PublishConnectOut).Summary) })
		},
	}
	c.Flags().BoolVar(&in.Forget, "forget", false, "sign out on this computer and take the permission back")
	return c
}

func publishPlanCmd() *cobra.Command {
	var in ops.PublishIn
	c := &cobra.Command{
		Use:   "plan <project> [--version rN] [--privacy private|unlisted|public] [--shape …]",
		Short: "Exactly what posting a version would send — the final render, title, description, chapters, captions, privacy, your channel — and what holds it; nothing is sent",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ProjectID = args[0]
			return runOp(c, "publish.plan", in, func(o any) { printPublishPlan(c, o.(*ops.PublishPlanOut)) })
		},
	}
	publishFlags(c, &in)
	return c
}

func publishFlags(c *cobra.Command, in *ops.PublishIn) {
	f := c.Flags()
	f.StringVar(&in.Revision, "version", "", "the version (rN or its ID; default the latest)")
	f.StringVar(&in.Branch, "cut", "", "the cut (its short name; default main)")
	f.StringVar(&in.Privacy, "privacy", "", "private (default), unlisted or public")
	f.StringVar(&in.Preset, "shape", "", "which final render: landscape-1080p, vertical-1080p or square-1080 (default: landscape, else the only one)")
	f.StringVar(&in.Platform, "platform", "", "")
	_ = f.MarkHidden("platform")
}

func publishSendCmd() *cobra.Command {
	var in ops.PublishIn
	var yes bool
	c := &cobra.Command{
		Use:   "post <project> [--version rN] [--privacy private|unlisted|public]",
		Short: "Post an approved version to YouTube as you: shows exactly what is sent and asks first (people only, at a terminal)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ProjectID = args[0]
			if in.Platform == "" {
				in.Platform = publish.YouTube
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			plan, err := ops.CallTyped[ops.PublishPlanOut](ctx, a, "publish.plan", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			if !jsonMode(c) {
				printPublishPlan(c, plan)
			}
			if !plan.Ready {
				if interactive() {
					// A person asked to post it: the refusal is recorded as a
					// post's is (the client's rules), with its own code; nothing is sent.
					attempt := in
					attempt.Attempt = true
					if p, err := ops.CallTyped[ops.PublishPlanOut](ctx, a, "publish.plan", attempt, ops.Caller{Human: true}); err == nil {
						plan = p
					}
				}
				return failErr(c, ops.PublishRefusal(plan))
			}
			if !interactive() {
				return fail(c, apperr.HumanOnly, "posting a video is a person's step: run it yourself in a terminal on this computer", "")
			}
			if plan.Posted != nil && !in.Again {
				// Already there: a second video only when the person says so, every time.
				if !askYes(fmt.Sprintf("Version %d is already on %s as “%s”. Post it again (a second video there)?", plan.Number, plan.PlatformName, plan.Posted.Channel), false) {
					return fail(c, apperr.Canceled, "not posted again", "")
				}
				in.Again = true
			} else if !yes && !askYes(fmt.Sprintf("Post this to %s as “%s”, %s?", plan.PlatformName, plan.As.Channel, plan.Privacy), false) {
				return fail(c, apperr.Canceled, "not posted", "")
			}
			in.Fingerprint = plan.Fingerprint
			last := -1
			pctx := publish.WithProgress(ctx, func(sent, total int64) {
				if total <= 0 || jsonMode(c) {
					return
				}
				if pct := int(sent * 100 / total); pct/10 != last/10 {
					last = pct
					fmt.Printf("  sent %d%%\n", pct)
				}
			})
			out, err := ops.CallTyped[ops.PublishSendOut](pctx, a, "publish.send", in, ops.Caller{Human: interactive()})
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() {
				fmt.Println(out.Summary)
				for _, n := range out.Publication.Notes {
					fmt.Println("  ! " + n)
				}
				if detailsMode(c) {
					fmt.Printf("video: %s\npublication: %s\n", out.Publication.VideoID, out.Publication.ID)
				}
			})
		},
	}
	publishFlags(c, &in)
	c.Flags().BoolVar(&yes, "yes", false, "don't ask again (you have read the plan)")
	c.Flags().BoolVar(&in.Again, "again", false, "post it although this render is already on your channel (a second video there)")
	return c
}

func printPublishPlan(c *cobra.Command, p *ops.PublishPlanOut) {
	fmt.Println(p.Summary)
	if !p.Ready {
		for _, h := range p.Holds {
			if h.Sending {
				fmt.Println("  … " + h.Says)
				continue
			}
			fmt.Println("  ✗ " + h.Says)
			if h.Command != "" {
				fmt.Println("      " + h.Command)
			}
		}
		return
	}
	fmt.Printf("  Title: “%s”\n", p.Title)
	if p.Description != "" {
		fmt.Println("  Description:")
		for _, l := range strings.Split(p.Description, "\n") {
			fmt.Println("    " + l)
		}
	}
	for _, cap := range p.Captions {
		fmt.Printf("  Captions: %s (%d lines)\n", cap.Name, cap.Lines)
	}
	for _, n := range p.Notes {
		fmt.Println("  ! " + n)
	}
	if len(p.Earlier) > 0 {
		fmt.Printf("  Posted before: %d time(s)", len(p.Earlier))
		if u := p.Earlier[len(p.Earlier)-1].URL; u != "" {
			fmt.Printf(", last %s", u)
		}
		fmt.Println()
	}
	if detailsMode(c) && p.Render != nil {
		fmt.Printf("  render %s (%s), sha256 %s, fingerprint %s\n", p.Render.ID, p.Render.Preset, p.Render.SHA256, p.Fingerprint)
	}
}

// runHuman runs a people-only op as the person at this terminal (refused when
// nobody is typing: a script or an agent).
func runHuman(c *cobra.Command, name string, in any, human func(out any)) error {
	a, err := openApp(c, "cli")
	if err != nil {
		return err
	}
	defer a.Close()
	return runOpWith(c, a, name, in, ops.Caller{Human: interactive()}, human)
}
