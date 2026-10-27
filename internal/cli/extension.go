package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/browserext"
	"github.com/metavert-io/metarush/internal/ops"
)

func extensionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "extension",
		Short: "The “Send to MetaRush” browser extension: where it is, pair a browser, unpair",
		Long: `The “Send to MetaRush” extension adds a button to YouTube, TikTok and X video pages. It saves the video in your
own signed-in browser (or asks MetaRush to download it) and sends it to MetaRush on this computer.

  metarush extension path     the folder to load in chrome://extensions (Developer mode, Load unpacked)
  metarush extension pair     a one-time code to type into the extension (works for 10 minutes)
  metarush extension list     your paired browsers (an owner sees everyone's)
  metarush extension revoke   unpair a browser (--all for every one of yours, --everyone for everyone's)

A pairing is the person's who made it: what the browser sends is theirs, and it stops while their
account is turned off. Here at the host's terminal you are the owner. A paired browser can only send
videos and pages to MetaRush; it can't read your projects or clips.`,
	}
	var details bool
	path := &cobra.Command{
		Use: "path", Short: "Print the folder to load as an unpacked extension (kept up to date by MetaRush)",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "extension.status", browserext.ExtensionStatusIn{}, func(o any) {
				st := o.(*browserext.ExtensionStatusOut)
				fmt.Println(st.Folder)
				if details {
					fmt.Printf("version %s, connects to %s\n", st.Version, st.Address)
				}
			})
		},
	}
	addDetails(path, &details)
	cmd.AddCommand(path)
	list := &cobra.Command{
		Use: "list", Short: "List your browsers paired with this machine (an owner sees everyone's, with whose each is)",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "extension.status", browserext.ExtensionStatusIn{}, func(o any) {
				st := o.(*browserext.ExtensionStatusOut)
				line := func(b browserext.Browser, who, stopped string) {
					used := "not used yet"
					if b.LastUsedAt != nil {
						used = "last used " + b.LastUsedAt.Local().Format("2 Jan 15:04")
					}
					fmt.Printf("%s%s, paired %s, %s, %d sent\n", b.Name, who, b.PairedAt.Local().Format("2 Jan 2006"), used, b.Sent)
					if stopped != "" {
						fmt.Println("    stopped: " + stopped)
					}
					if details {
						fmt.Printf("    %s  %s  version %s\n", b.ID, b.Origin, b.Version)
					}
				}
				others := 0
				for _, b := range st.Everyone {
					if !b.Mine {
						others++
					}
				}
				if len(st.Browsers) == 0 {
					fmt.Println("None of your browsers is paired with this machine yet. Pair one in the dashboard (Settings, Browser extension) or here.")
					if details {
						fmt.Println("metarush extension pair")
					}
				} else {
					fmt.Println("Your browsers:")
					for _, b := range st.Browsers {
						line(b, "", "")
					}
				}
				if others > 0 {
					fmt.Println("Everyone else's browsers:")
					for _, b := range st.Everyone {
						if !b.Mine {
							line(b.Browser, " ("+b.Person+")", b.Stopped)
						}
					}
				}
				if st.CodeWaiting && st.CodeExpiresAt != nil {
					fmt.Printf("Your pairing code is waiting until %s.\n", st.CodeExpiresAt.Local().Format("15:04"))
				}
			})
		},
	}
	addDetails(list, &details)
	cmd.AddCommand(list)
	cmd.AddCommand(&cobra.Command{
		Use: "pair", Short: "Make a one-time code that pairs a browser's extension with this machine, for you (here: the owner); people only, in a terminal",
		RunE: func(c *cobra.Command, _ []string) error {
			// Pairing gives a browser the right to send footage here: a person decides.
			if !interactive() {
				return fail(c, apperr.HumanOnly, "Pairing a browser needs a person at this machine.", "Run it in your own terminal, or use Settings, Browser extension in the dashboard.")
			}
			if !askYes("Pair a browser with MetaRush on this machine? It can then send videos here (nothing else).", true) {
				return fail(c, apperr.Canceled, "Not paired.", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "extension.pair", browserext.ExtensionPairIn{}, ops.Caller{Human: true}, func(o any) {
				p := o.(*browserext.ExtensionPairOut)
				fmt.Printf("\n    %s\n\n", p.Code)
				fmt.Printf("Type this code into the “Send to MetaRush” extension (it works once, until %s). The browser is paired for %s.\n", p.ExpiresAt.Local().Format(time.Kitchen), p.For)
				for _, s := range p.Steps[2:] {
					fmt.Println(s)
				}
			})
		},
	})
	var all, everyone bool
	revoke := &cobra.Command{
		Use: "revoke [browser-id] | --all | --everyone", Short: "Unpair a browser (its token stops working at once): one, --all of yours, or --everyone's (owners)", Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in := browserext.ExtensionRevokeIn{All: all, Everyone: everyone}
			if len(args) == 1 {
				in.ID = args[0]
			}
			if in.ID == "" && !all && !everyone {
				return fail(c, apperr.InvalidInput, "Say which browser, --all (yours) or --everyone.", "The list of paired browsers shows their IDs with --details.")
			}
			return runOp(c, "extension.revoke", in, func(o any) {
				out := o.(*browserext.ExtensionRevokeOut)
				switch out.Revoked {
				case 0:
					fmt.Println("No browser was paired.")
				case 1:
					fmt.Println("Unpaired 1 browser. It can't send videos here any more.")
				default:
					fmt.Printf("Unpaired %d browsers. They can't send videos here any more.\n", out.Revoked)
				}
			})
		},
	}
	revoke.Flags().BoolVar(&all, "all", false, "unpair every browser you paired")
	revoke.Flags().BoolVar(&everyone, "everyone", false, "unpair every browser anyone paired (owners)")
	cmd.AddCommand(revoke)
	return cmd
}
