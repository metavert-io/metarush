package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/sso"
)

// Signing in with an organization's account (D425–D434, docs/accounts.md):
// `metarush auth sso status|set|secret|test` and `metarush auth method sso`.
// Changes are an owner's, at a person's terminal on this computer; the app's
// client secret is read hidden (or from a pipe), never from a flag.

func authSSOCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "sso", Short: "Signing in with your organization's account: Google, Microsoft or another OpenID Connect provider"}
	cmd.AddCommand(ssoStatusCmd(), ssoSetCmd(), ssoSecretCmd(), ssoTestCmd())
	return cmd
}

// ssoFlags are the setup's flags (auth sso set, auth method sso).
type ssoFlags struct {
	in           auth.SSOSetIn
	secretStdin  bool
	keepSecret   bool
	alsoPassword bool
	alsoTS       bool
}

func (f *ssoFlags) add(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.in.Provider, "provider", "", "google, microsoft or oidc (another provider)")
	fl.StringVar(&f.in.Issuer, "issuer", "", "oidc: the provider's address (its issuer)")
	fl.StringVar(&f.in.Tenant, "tenant", "", "microsoft: your directory (tenant) ID, or organizations")
	fl.StringVar(&f.in.ClientID, "client-id", "", "the app's client ID, from the provider's console")
	fl.StringVar(&f.in.Label, "label", "", "oidc: the provider's name on the sign-in button")
	fl.StringVar(&f.in.Domain, "domain", "", "who may join without an invite: a Google Workspace domain, a Microsoft directory ID, or an email domain")
	fl.BoolVar(&f.alsoPassword, "also-password", false, "keep email and password sign-in working beside it")
	fl.BoolVar(&f.alsoTS, "also-tailscale", false, "keep Tailscale accounts signing in beside it")
	fl.BoolVar(&f.secretStdin, "secret-stdin", false, "read the app's client secret from a pipe instead of asking")
	fl.BoolVar(&f.keepSecret, "keep-secret", false, "keep the client secret this computer has (don't ask for it)")
}

// read asks for what the flags didn't say, and the secret (hidden).
func (f *ssoFlags) read(c *cobra.Command, a *app.App) error {
	f.in.AlsoPassword, f.in.AlsoTailscale = f.alsoPassword, f.alsoTS
	if f.in.Provider == "" {
		f.in.Provider = ask("Which provider: google, microsoft or oidc", "google")
	}
	if f.in.Provider == sso.Microsoft && f.in.Tenant == "" {
		f.in.Tenant = ask("Your directory (tenant) ID, from the app's Overview page in Microsoft Entra", "")
	}
	if f.in.Provider == sso.OIDC && f.in.Issuer == "" {
		f.in.Issuer = ask("The provider's address (its issuer)", "")
	}
	if f.in.ClientID == "" {
		f.in.ClientID = ask("The app's client ID", "")
	}
	switch {
	case f.secretStdin:
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return fail(c, apperr.InvalidInput, "Nothing came through the pipe: send the client secret on one line.", "")
		}
		f.in.ClientSecret = strings.TrimSpace(line)
	case f.keepSecret:
	default:
		has := false
		if s, _, err := auth.SSOSecret(a.Cfg); err == nil && s != "" {
			has = true
		}
		q := "The app's client secret (as the provider's console shows it)"
		if has {
			q += "; press Enter to keep this computer's"
		}
		f.in.ClientSecret = askSecret(q)
	}
	return nil
}

func ssoStatusCmd() *cobra.Command {
	return forOps(&cobra.Command{Use: "status", Short: "The organization sign-in: the setup, this computer's app secret, the addresses to register and the last test sign-in", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "auth.sso.status", ops.EmptyIn{}, func(o any) { printSSOStatus(o.(*auth.SSOStatusOut), detailsMode(c)) })
		}}, "auth.sso.status")
}

// printSSOStatus says it plainly; the provider's own identifiers behind --details.
func printSSOStatus(st *auth.SSOStatusOut, details bool) {
	fmt.Println(st.Summary)
	setup := st.Next
	if setup == nil {
		setup = st.Live
	}
	if setup == nil {
		fmt.Println("Set it up in Settings → People.")
		if details {
			fmt.Println("  set it up: metarush auth sso set --provider google|microsoft|oidc --client-id …")
		}
		return
	}
	if setup.Domain != "" {
		switch setup.Provider {
		case sso.Google:
			fmt.Printf("People of the Google Workspace domain %s join by themselves; everyone else needs an invite.\n", setup.Domain)
		case sso.Microsoft:
			fmt.Println("People of your Microsoft directory join by themselves; everyone else needs an invite.")
		default:
			fmt.Printf("People with a checked email at %s join by themselves; everyone else needs an invite.\n", setup.Domain)
		}
	} else {
		fmt.Println("Only people you invite can sign in.")
	}
	if st.Secret.Set {
		fmt.Printf("This computer's app secret: %s\n", st.Secret.Masked)
	} else {
		fmt.Println("This computer doesn't have the app's client secret yet.")
	}
	fmt.Printf("Addresses to register with %s (the app's redirect addresses):\n", setup.Title)
	fmt.Println("  " + st.Callbacks.Local)
	if st.Callbacks.Public != "" {
		fmt.Println("  " + st.Callbacks.Public)
	}
	if t := st.Test; t != nil {
		state := "worked"
		if !t.OK {
			state = "didn't work"
		}
		who := ""
		if t.Email != "" {
			who = ", as " + t.Email
		}
		fmt.Printf("Last test sign-in on this computer: %s, %s ago%s.\n", state, ago(t.At), who)
		if !t.OK && t.Message != "" {
			fmt.Println("  " + t.Message)
		}
	}
	if st.CanSwitch {
		fmt.Println("It passed its test here: you can switch to it now.")
	} else if st.SwitchWhy != "" && (st.Next != nil || !st.InUse) {
		fmt.Println(st.SwitchWhy)
	}
	if details {
		fmt.Println("  keep the app's secret here: metarush auth sso secret · test it: metarush auth sso test · switch: metarush auth method sso")
		fmt.Printf("  provider: %s · issuer: %s · client ID: %s", setup.Provider, setup.Issuer, setup.ClientID)
		if setup.Tenant != "" {
			fmt.Printf(" · tenant: %s", setup.Tenant)
		}
		fmt.Println()
		fmt.Printf("  the secret's variable: %s\n", st.Secret.EnvVar)
	}
}

func ago(t time.Time) string {
	d := time.Since(t).Round(time.Minute)
	switch {
	case d < time.Minute:
		return "moments"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	return fmt.Sprintf("%d hours", int(d.Hours()))
}

func ssoSetCmd() *cobra.Command {
	var f ssoFlags
	cmd := &cobra.Command{Use: "set", Short: "Set up signing in with your organization's account: the provider, the app, who may join and the other sign-ins (owners, people only, on this computer)",
		Example: "  metarush auth sso set --provider google --client-id 1234.apps.googleusercontent.com --domain example.com\n" +
			"  metarush auth sso set --provider microsoft --tenant <directory ID> --client-id <application ID>\n" +
			"  metarush auth sso set --provider oidc --issuer https://login.example.com --client-id metarush --label \"Example sign-in\"",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := needAPerson(c); err != nil {
				return err
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			if err := f.read(c, a); err != nil {
				return err
			}
			out, err := ops.CallTyped[auth.SSOStatusOut](c.Context(), a, "auth.sso.set", f.in, ops.Caller{Human: true})
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() {
				printSSOStatus(out, detailsMode(c))
				if !out.InUse || out.Next != nil {
					fmt.Println("Next: register the addresses above with the provider, then test the sign-in on this computer.")
				}
			})
		}}
	f.add(cmd)
	return forOps(cmd, "auth.sso.set")
}

func ssoSecretCmd() *cobra.Command {
	var fromStdin, remove bool
	cmd := &cobra.Command{Use: "secret", Short: "Keep the sign-in app's client secret on this computer, or remove it (owners, people only)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := needAPerson(c); err != nil {
				return err
			}
			in := auth.SSOSecretIn{Remove: remove}
			if !remove {
				if fromStdin {
					line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
					in.ClientSecret = strings.TrimSpace(line)
				} else {
					in.ClientSecret = askSecret("The app's client secret (as the provider's console shows it)")
				}
				if in.ClientSecret == "" {
					return fail(c, apperr.Canceled, "Nothing changed.", "")
				}
			}
			out, err := callAsPerson[auth.SSOStatusOut](c, "auth.sso.secret", in)
			if err != nil {
				return err
			}
			return emit(c, out, func() {
				if remove {
					fmt.Println("The app's client secret is removed from this computer.")
				} else {
					fmt.Printf("The app's client secret is kept on this computer (%s).\n", out.Secret.Masked)
				}
			})
		}}
	cmd.Flags().BoolVar(&fromStdin, "stdin", false, "read the secret from a pipe instead of asking")
	cmd.Flags().BoolVar(&remove, "remove", false, "remove this computer's client secret")
	return forOps(cmd, "auth.sso.secret")
}

func ssoTestCmd() *cobra.Command {
	var noOpen bool
	cmd := &cobra.Command{Use: "test", Short: "A test sign-in with your organization's account in a browser on this computer, before switching to it (owners, people only)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := needAPerson(c); err != nil {
				return err
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			res, err := runSSOTest(c, a, noOpen)
			if err != nil {
				return err
			}
			st := auth.SSOStatus(c.Context(), a)
			return emit(c, map[string]any{"test": res, "status": st}, func() {
				if res.OK {
					fmt.Println("The test sign-in worked.")
				} else {
					fmt.Println("The test sign-in didn't work.")
				}
				if res.Message != "" {
					fmt.Println(res.Message)
				}
				if st.CanSwitch {
					fmt.Println("You can switch to it now.")
					if detailsMode(c) {
						fmt.Println("  switch: metarush auth method sso")
					}
				}
			})
		}}
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "print the link instead of opening a browser")
	return forOps(cmd, "auth.sso.test")
}

// runSSOTest starts a test sign-in, opens it in this computer's browser and
// waits (10 minutes at most) for how it went.
func runSSOTest(c *cobra.Command, a *app.App, noOpen bool) (*sso.TestResult, error) {
	if !healthy(a.Cfg) {
		// The provider sends the browser back to the dashboard, which must be running.
		return nil, failErr(c, apperr.New(apperr.NotConfigured, "MetaRush's dashboard isn't running on this computer, and the test sign-in comes back to it.").
			WithHint("Start MetaRush, then test again.").WithCommand("metarush dashboard"))
	}
	started := time.Now()
	out, err := ops.CallTyped[auth.SSOTestOut](c.Context(), a, "auth.sso.test", ops.EmptyIn{}, ops.Caller{Human: true})
	if err != nil {
		return nil, failErr(c, err)
	}
	fmt.Fprintln(os.Stderr, out.Summary)
	if noOpen {
		fmt.Fprintln(os.Stderr, "Open this link in a browser on this computer:")
		fmt.Println(out.URL)
	} else {
		openBrowser(out.URL)
		fmt.Fprintln(os.Stderr, "Opening it in your browser… (it waits up to 10 minutes; Ctrl-C stops waiting)")
	}
	ctx, cancel := signalCtx(c.Context())
	defer cancel()
	ctx, stop := context.WithTimeout(ctx, sso.FlowLife+sso.TicketLife)
	defer stop()
	store := sso.FlowsIn(a.Cfg.StateDir())
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		if r := store.LastTest(); r != nil && !r.At.Before(started.Add(-time.Second)) {
			return r, nil
		}
		select {
		case <-ctx.Done():
			return nil, failErr(c, apperr.New(apperr.Canceled, "The test sign-in didn't come back in time, so nothing changed.").WithHint("Start it again when you're ready.").WithCommand("metarush auth sso test"))
		case <-t.C:
		}
	}
}

// authMethodSSO is `metarush auth method sso`: with a setup's flags it sets
// it up, runs the test sign-in and switches when it worked; without them it
// switches to the setup whose test just passed here.
func authMethodSSO(c *cobra.Command, a *app.App, f *ssoFlags) error {
	if f.in.Provider != "" || f.in.ClientID != "" {
		if err := f.read(c, a); err != nil {
			return err
		}
		st, err := ops.CallTyped[auth.SSOStatusOut](c.Context(), a, "auth.sso.set", f.in, ops.Caller{Human: true})
		if err != nil {
			return failErr(c, err)
		}
		if !jsonMode(c) {
			printSSOStatus(st, detailsMode(c))
		}
		if st.InUse && st.Next == nil {
			return emit(c, st, func() { fmt.Println("The change applies now; no new test sign-in was needed.") })
		}
		if !askYes("Register the addresses above with the provider, then test the sign-in now?", true) {
			return failErr(c, apperr.New(apperr.Canceled, "The setup is kept; nothing else changed.").WithHint("Test the sign-in when you're ready.").WithCommand("metarush auth sso test"))
		}
		res, err := runSSOTest(c, a, false)
		if err != nil {
			return err
		}
		if !res.OK {
			return failErr(c, apperr.New(apperr.Conflict, "The test sign-in didn't work, so people keep signing in as before.").WithHint(res.Message).
				WithDetails(map[string]any{"reason": "not_tested", "test": res.Reason}))
		}
		fmt.Fprintln(os.Stderr, res.Message)
	}
	if !askYes("Switch to signing in with your organization's account now?", true) {
		return fail(c, apperr.Canceled, "Nothing changed.", "")
	}
	out, err := ops.CallTyped[auth.AuthStatusOut](c.Context(), a, "auth.method.set", auth.AuthMethodIn{Method: "sso"}, ops.Caller{Human: true})
	if err != nil {
		return failErr(c, err)
	}
	return emit(c, out, func() {
		fmt.Println(out.Summary)
		fmt.Println("Invite people in Settings → People.")
		fmt.Println("If the provider ever stops working, a one-time link made on this computer still opens MetaRush, and one step at its terminal goes back to just you.")
		if detailsMode(c) {
			fmt.Println("  invite: metarush users invite <email> · open MetaRush here: metarush dashboard · back to just you: metarush auth reset")
		}
	})
}
