package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/remote"
	"github.com/metavert-io/metarush/internal/tools"
)

// Remote access over Tailscale (D128, D129): `metarush remote status|enable|
// disable|allow|remove|signout`. Everything but status is people-only: it
// needs an interactive terminal and a yes. Nothing here changes Tailscale
// unless the person adds --apply and says yes again.

func remoteCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "remote", Short: "Open the dashboard from your other computers over Tailscale: turn it on or off, see how it stands, choose who signs in with their Tailscale account"}
	cmd.AddCommand(remoteStatusCmd(), remoteEnableCmd(), remoteDisableCmd(), remoteAllowCmd(false), remoteAllowCmd(true), remoteSignoutCmd())
	return cmd
}

// needPerson refuses a remote-access change outside an interactive terminal.
func needPerson(c *cobra.Command) error {
	if interactive() {
		return nil
	}
	return failErr(c, apperr.New(apperr.HumanOnly, "Changing who can open MetaRush from other computers needs a person at this computer.").
		WithHint("Run it in your own terminal, or use Settings, Remote access, in the dashboard on this computer."))
}

func remoteStatusCmd() *cobra.Command {
	var details bool
	cmd := &cobra.Command{Use: "status", Short: "How remote access stands: the address, Tailscale, HTTPS certificates, tailscale serve and who signs in with their Tailscale account", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "remote.status", remote.RemoteStatusIn{}, func(o any) { printRemote(o.(*remote.RemoteStatusOut), details) })
		}}
	addDetails(cmd, &details)
	return cmd
}

func remoteEnableCmd() *cobra.Command {
	var in remote.RemoteEnableIn
	var apply, details bool
	cmd := &cobra.Command{Use: "enable", Short: "Turn remote access on: this computer's Tailscale name becomes the dashboard's address for your other computers (people only; --apply also starts tailscale serve after asking)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := needPerson(c); err != nil {
				return err
			}
			who := "People on your tailnet who have a sign-in link from this computer"
			if len(in.Allow) > 0 {
				who = strings.Join(in.Allow, ", ") + " (and you, from your other computers)"
			}
			if !jsonMode(c) && !askYes("Turn on remote access? "+who+" will be able to open MetaRush on this computer from theirs.", true) {
				return fail(c, apperr.Canceled, "Nothing changed.", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			out, err := ops.CallTyped[remote.RemoteStatusOut](c.Context(), a, "remote.enable", in, ops.Caller{Human: true})
			if err != nil {
				return failErr(c, err)
			}
			if apply && !out.Ready {
				out = applyServe(c, a, out)
			}
			return emit(c, out, func() {
				printRemote(out, details)
				if !out.Serve.Active && !apply {
					fmt.Println("\nTo start it, run this in a terminal on this computer:")
					fmt.Printf("\n    %s\n\n", out.ServeCommand)
					fmt.Println("(Or run this again with --apply: MetaRush runs it after asking you.)")
				}
			})
		}}
	cmd.Flags().StringSliceVar(&in.Allow, "allow", nil, "a Tailscale account (name@example.com) that signs in by itself; repeat for more (your own account is added with them)")
	cmd.Flags().StringVar(&in.MachineName, "machine-name", "", "how the dashboard names this computer to people elsewhere (default: its name on the tailnet)")
	cmd.Flags().StringVar(&in.URL, "url", "", "another address than this computer's Tailscale name (for another HTTPS port)")
	cmd.Flags().BoolVar(&apply, "apply", false, "also start tailscale serve for MetaRush (after asking)")
	addDetails(cmd, &details)
	return cmd
}

// applyServe runs `tailscale serve` for the person, after a plain yes. It
// never runs when HTTPS certificates are off: Tailscale would stop and wait
// for someone to turn them on in a browser.
func applyServe(c *cobra.Command, a *app.App, out *remote.RemoteStatusOut) *remote.RemoteStatusOut {
	switch {
	case jsonMode(c):
		return out // --json never asks; it says what to run
	case !out.Tailscale.Running || (out.Tailscale.DNSName != "" && !out.Tailscale.HTTPSReady):
		fmt.Println("Tailscale isn't ready for this yet (see below), so MetaRush didn't start tailscale serve.")
		return out
	case out.Serve.Active:
		return out
	}
	for _, e := range out.Serve.Elsewhere {
		if e == out.URL+"/" {
			// Something else is served at that address: never replace it unasked.
			fmt.Println("Tailscale already serves something else at this address, so MetaRush didn't start tailscale serve (it would replace it).")
			return out
		}
	}
	if !askYes(fmt.Sprintf("Start Tailscale's secure address now (it runs “%s”)?", out.ServeCommand), true) {
		return out
	}
	msg, err := remote.StartServe(c.Context(), a.Tools, a.Cfg.Remote(), a.Cfg.Dashboard.Port)
	if err != nil {
		fmt.Printf("Tailscale didn't start it: %s\n", err)
		return out
	}
	if detailsMode(c) && msg != "" {
		fmt.Println(msg)
	}
	return remote.Status(c.Context(), a)
}

func remoteDisableCmd() *cobra.Command {
	var apply, details bool
	cmd := &cobra.Command{Use: "disable", Short: "Turn remote access off: the address and the Tailscale accounts are cleared and browsers on other computers are signed out (people only; --apply also stops tailscale serve after asking)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := needPerson(c); err != nil {
				return err
			}
			if !jsonMode(c) && !askYes("Turn off remote access? Browsers on other computers are signed out at once.", true) {
				return fail(c, apperr.Canceled, "Nothing changed.", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			before := a.Cfg.Remote()
			out, err := ops.CallTyped[remote.RemoteStatusOut](c.Context(), a, "remote.disable", remote.RemoteDisableIn{}, ops.Caller{Human: true})
			if err != nil {
				return failErr(c, err)
			}
			if apply && out.Serve.Active && !jsonMode(c) && askYes(fmt.Sprintf("Stop Tailscale's secure address for MetaRush too (it runs “%s”)?", out.StopCommand), true) {
				if msg, err := remote.StopServe(c.Context(), a.Tools, before); err != nil {
					fmt.Printf("Tailscale didn't stop it: %s\n", err)
				} else {
					if detailsMode(c) && msg != "" {
						fmt.Println(msg)
					}
					st := remote.Status(c.Context(), a)
					out.Serve, out.Summary = st.Serve, st.Summary
				}
			}
			return emit(c, out, func() {
				fmt.Println(out.Summary)
				if out.SignedOut > 0 {
					fmt.Printf("Signed out %s on other computers.\n", plural(out.SignedOut, "browser"))
				}
				if out.Serve.Active {
					fmt.Println("\nTo stop Tailscale passing its address to MetaRush, run this in a terminal on this computer:")
					fmt.Printf("\n    %s\n\n", out.StopCommand)
				}
				if details {
					printRemoteDetails(out)
				}
			})
		}}
	cmd.Flags().BoolVar(&apply, "apply", false, "also stop tailscale serve for MetaRush (after asking)")
	addDetails(cmd, &details)
	return cmd
}

func remoteAllowCmd(removing bool) *cobra.Command {
	var details bool
	use, short := "allow <tailscale-account>...", "Let Tailscale accounts sign in by themselves through remote access (people only, at this computer)"
	if removing {
		use, short = "remove <tailscale-account>...", "Stop letting Tailscale accounts in: their browsers are signed out at once (people only)"
	}
	cmd := &cobra.Command{Use: use, Short: short, Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := needPerson(c); err != nil {
				return err
			}
			in := remote.RemoteAllowIn{Add: args}
			q := "Let " + strings.Join(args, ", ") + " open MetaRush on this computer from theirs, signed in with their Tailscale account?"
			if removing {
				in = remote.RemoteAllowIn{Remove: args}
				q = "Stop letting " + strings.Join(args, ", ") + " in? Their browsers are signed out at once."
			}
			if !jsonMode(c) && !askYes(q, true) {
				return fail(c, apperr.Canceled, "Nothing changed.", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "remote.allow", in, ops.Caller{Human: true}, func(o any) {
				out := o.(*remote.RemoteStatusOut)
				printPeople(out)
				if out.SignedOut > 0 {
					fmt.Printf("Signed out %s on other computers.\n", plural(out.SignedOut, "browser"))
				}
				if !out.On {
					fmt.Println("Remote access is off, so this takes effect once it is on.")
				}
				if details {
					printRemoteDetails(out)
				}
			})
		}}
	addDetails(cmd, &details)
	return cmd
}

func remoteSignoutCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{Use: "signout [tailscale-account]", Short: "Sign out browsers on other computers: one Tailscale account's, or --all (people only; for a lost or shared computer)", Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := needPerson(c); err != nil {
				return err
			}
			in := remote.RemoteSignoutIn{All: all}
			if len(args) == 1 {
				in.Login = args[0]
			}
			if !all && in.Login == "" {
				return fail(c, apperr.InvalidInput, "Say whose browsers to sign out, or --all for every browser on another computer.", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "remote.signout", in, ops.Caller{Human: true}, func(o any) { fmt.Println(o.(*remote.RemoteSignoutOut).Message) })
		}}
	cmd.Flags().BoolVar(&all, "all", false, "every browser that signed in from another computer")
	return cmd
}

/* ---------- doctor ---------- */

// remoteChecks are doctor's checks of remote access (D128), when it is on:
// Tailscale connected, HTTPS certificates, tailscale serve handing the address
// to this dashboard, and never Tailscale Funnel.
func remoteChecks(ctx context.Context, cfg *config.Config, t *tools.Set, add func(area, name, status, detail, fix string, plain ...string)) {
	rm := cfg.Remote()
	if !rm.On() {
		if len(rm.Logins) > 0 {
			add("remote", "remote access", "warn", "dashboard.tailscaleLogins is set but dashboard.publicURL is empty", "metarush remote enable",
				"Some Tailscale accounts are listed, but remote access is off, so they can't sign in", "Turn remote access on, or take them off the list.")
		}
		return
	}
	ts := remote.ReadTailscale(ctx, t)
	sv := remote.ReadServe(ctx, t, cfg.Dashboard.Port)
	switch {
	case !ts.Installed || !ts.Running || ts.Error != "":
		add("remote", "tailscale", "fail", orWord(ts.Error, ts.State), "open Tailscale and connect", "People can't open MetaRush from other computers: "+strings.TrimSuffix(lowerFirst(ts.Problem), "."), "Connect Tailscale on this computer.")
	case rm.HTTPS && !ts.HTTPSReady:
		add("remote", "https certificates", "fail", "CertDomains does not include "+ts.DNSName, "Tailscale admin console: DNS → HTTPS Certificates → Enable",
			"HTTPS certificates are off for your tailnet, so the secure address doesn't work", "Turn them on in the Tailscale admin console: DNS, then HTTPS Certificates, then Enable.")
	default:
		add("remote", "tailscale", "ok", ts.DNSName+" ("+ts.Version+")", "", "Tailscale is connected and has HTTPS certificates for this computer")
	}
	switch {
	case !sv.Known:
		add("remote", "tailscale serve", "warn", orWord(sv.Error, "serve status unreadable"), remote.ServeCommand(rm, cfg.Dashboard.Port),
			"MetaRush couldn't check what Tailscale serves", "Check that Tailscale is running.")
	case !sv.Active:
		add("remote", "tailscale serve", "warn", "no serve handler for 127.0.0.1:"+fmt.Sprint(cfg.Dashboard.Port), remote.ServeCommand(rm, cfg.Dashboard.Port),
			"Tailscale isn't handing the address to MetaRush yet, so other computers can't open it", "Start it with the tailscale serve command (metarush remote status --details shows it).")
	case !strings.EqualFold(sv.Address, rm.PublicURL):
		add("remote", "tailscale serve", "warn", sv.Address+" is served, config has "+rm.PublicURL, "metarush remote enable",
			"Tailscale serves MetaRush at a different address than MetaRush expects, so visits are turned away", "Turn remote access on again so the two agree.")
	default:
		add("remote", "tailscale serve", "ok", sv.Address+" → "+sv.Target, "", "Tailscale hands "+sv.Address+" to MetaRush")
	}
	if sv.Funnel {
		add("security", "tailscale funnel", "fail", "AllowFunnel is on for "+sv.Address, "tailscale funnel --https=443 off",
			"Tailscale Funnel puts MetaRush's address on the public internet (MetaRush turns those visits away)", "Turn Funnel off for this address.")
	}
	if rm.IdentitySignIn() {
		add("remote", "sign-in", "ok", strings.Join(rm.Logins, ", "), "", fmt.Sprintf("%s sign in with their Tailscale account", plural(len(rm.Logins), "account")))
	} else {
		add("remote", "sign-in", "ok", "sign-in links only", "", "People on other computers sign in with a link from this computer")
	}
}

/* ---------- printing ---------- */

func printRemote(out *remote.RemoteStatusOut, details bool) {
	fmt.Println(out.Summary)
	ts := out.Tailscale
	fmt.Println()
	switch {
	case !ts.Installed:
		fmt.Println("Tailscale: not installed on this computer")
	case ts.Running:
		fmt.Printf("Tailscale: connected; this computer is “%s” on your tailnet\n", orWord(ts.HostName, "unnamed"))
	case ts.Error != "":
		fmt.Println("Tailscale: couldn't be read")
	default:
		fmt.Println("Tailscale: not connected")
	}
	if ts.Installed && ts.DNSName != "" {
		if ts.HTTPSReady {
			fmt.Println("HTTPS certificates: on for your tailnet")
		} else {
			fmt.Println("HTTPS certificates: off for your tailnet (the secure address needs them)")
		}
	}
	switch {
	case out.Serve.Active:
		fmt.Printf("Tailscale serve: hands %s to MetaRush\n", out.Serve.Address)
	case out.Serve.Known:
		fmt.Println("Tailscale serve: not handing an address to MetaRush")
	}
	if out.On {
		fmt.Printf("Computer name people see: %s\n", out.Machine)
	}
	printPeople(out)
	if len(out.Steps) > 0 {
		fmt.Println("\nStill to do:")
		for _, s := range out.Steps {
			fmt.Println("  - " + s)
		}
	}
	if details {
		printRemoteDetails(out)
	}
}

func printPeople(out *remote.RemoteStatusOut) {
	if len(out.People) == 0 {
		if out.On {
			fmt.Println("Signing in with a Tailscale account: no one yet (people need a sign-in link from this computer)")
		}
		return
	}
	fmt.Println("Signs in with their Tailscale account:")
	for _, p := range out.People {
		line := "  " + p.Login
		if p.Name != "" {
			line = "  " + p.Name + " (" + p.Login + ")"
		}
		var notes []string
		if p.ThisComputer {
			notes = append(notes, "this computer's account")
		}
		if p.LastSignedInAt != nil {
			notes = append(notes, "last signed in "+p.LastSignedInAt.Local().Format("2 Jan 15:04"))
		} else {
			notes = append(notes, "hasn't signed in yet")
		}
		if p.Browsers > 0 {
			notes = append(notes, plural(p.Browsers, "browser")+" signed in now")
		}
		fmt.Println(line + ", " + strings.Join(notes, ", "))
	}
}

func printRemoteDetails(out *remote.RemoteStatusOut) {
	ts := out.Tailscale
	fmt.Println("\ndetails:")
	fmt.Printf("  url: %s\n  tailscale name: %s\n  tailscale account: %s\n  tailnet: %s\n  tailscale version: %s\n", orWord(out.URL, "-"), orWord(ts.DNSName, "-"), orWord(ts.Login, "-"), orWord(ts.Tailnet, "-"), orWord(ts.Version, "-"))
	fmt.Printf("  certificate names: %s\n", orWord(strings.Join(ts.CertDomains, ", "), "none"))
	if out.Serve.Active {
		fmt.Printf("  serve: %s -> %s (funnel %v)\n", out.Serve.Address, out.Serve.Target, out.Serve.Funnel)
	}
	for _, e := range out.Serve.Elsewhere {
		fmt.Printf("  serve also offers: %s\n", e)
	}
	if ts.Error != "" {
		fmt.Printf("  tailscale said: %s\n", ts.Error)
	}
	fmt.Printf("  start: %s\n  stop: %s\n", out.ServeCommand, out.StopCommand)
	for _, p := range out.People {
		fmt.Printf("  person: %s %s\n", p.Login, orWord(p.UserID, "(not signed in yet)"))
	}
}
