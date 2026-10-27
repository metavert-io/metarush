package cli

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/prefs"
)

// Accounts and sign-in (D183, docs/accounts.md): `metarush auth status|method|
// reset`, `metarush users …`, `metarush account …`. Changes are people-only
// (an interactive terminal on this computer, which counts as the owner).
// Passwords are read hidden from the terminal, never from a flag.

func authCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "How people sign in to this MetaRush: just you on this computer, email and password, or Tailscale accounts"}
	cmd.AddCommand(authStatusCmd(), authMethodCmd(), authResetCmd())
	return cmd
}

func usersCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "users", Short: "The workspace's people: invite them, make reset-password links, turn accounts off or on, roles"}
	cmd.AddCommand(usersListCmd(), usersInviteCmd(), usersLinkCmd("reset-password", "users.resetLink", "A one-time link a person follows to choose a new password (an invite again if they haven't accepted theirs)"),
		usersRefCmd("revoke", "users.revoke", "Take back a person's invite or reset link"),
		usersRefCmd("disable", "users.disable", "Turn a person's account off: signed out everywhere, can't sign in"),
		usersRefCmd("enable", "users.enable", "Turn a person's account back on"),
		usersRefCmd("remove", "users.remove", "Remove a person's account; their name stays on what they did"),
		usersRoleCmd())
	return cmd
}

func accountCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "account", Short: "Your own account: password, name and email, signing out everywhere, your preferences"}
	cmd.AddCommand(accountPasswordCmd(), accountUpdateCmd(), accountSignoutCmd(), accountAvatarCmd(), accountPreferencesCmd())
	return cmd
}

// accountPreferencesCmd is Settings → Preferences in a terminal (D208): your
// estimates (on by default), your Ask first or Auto choice, and for owners
// estimates for everyone. Changing them is people only.
func accountPreferencesCmd() *cobra.Command {
	var estimates, require string
	cmd := &cobra.Command{Use: "preferences", Short: "Your preferences: estimate costs of outside AI services before edits (on by default), Ask first or Auto; owners can ask everyone for estimates",
		Example: "  metarush account preferences                          # what you chose\n" +
			"  metarush account preferences --estimates off          # paid steps run within your budgets and limits, without the estimate first\n" +
			"  metarush account preferences --require-estimates on   # owners: everyone sees the estimate first, whatever they chose",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			print := func(o any) { printPreferences(o.(*prefs.PreferencesOut), detailsMode(c)) }
			if estimates == "" && require == "" {
				return runOp(c, "account.preferences", prefs.PreferencesIn{}, print)
			}
			var in prefs.PreferencesSetIn
			for _, f := range []struct {
				flag, value string
				dst         **bool
			}{{"--estimates", estimates, &in.Estimates}, {"--require-estimates", require, &in.RequireEstimates}} {
				if f.value == "" {
					continue
				}
				on, ok := map[string]bool{"on": true, "off": false}[strings.ToLower(f.value)]
				if !ok {
					return fail(c, apperr.InvalidInput, f.flag+" takes on or off.", "")
				}
				*f.dst = &on
			}
			if !isInteractive() {
				return fail(c, apperr.HumanOnly, "Changing your preferences is for people.", "Run it yourself in a terminal, or use Settings → Preferences in the dashboard.")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "account.preferences.set", in, ops.Caller{Human: true}, print)
		}}
	cmd.Flags().StringVar(&estimates, "estimates", "", "on: costs of outside AI services are estimated before your edits (the default); off: paid steps run within your budgets and limits without the estimate first")
	cmd.Flags().StringVar(&require, "require-estimates", "", "owners: on asks everyone for the estimate first, whatever they chose; off lets each person choose")
	return forOps(cmd, "account.preferences", "account.preferences.set")
}

// printPreferences says them plainly (the commands that change them behind --details).
func printPreferences(p *prefs.PreferencesOut, details bool) {
	state := "on"
	if !p.Estimates {
		state = "off"
	}
	fmt.Println("Estimate costs of outside AI services before edits: " + state)
	fmt.Println("  " + p.Summary)
	mode := "Ask first"
	if p.AssistantMode == "auto" {
		mode = "Auto"
	}
	line := "Claude's changes for you: " + mode
	if !p.AutoOffered {
		line += " (steps don't run by themselves on this computer)"
	}
	fmt.Println(line + ".")
	fmt.Println("Light or dark: chosen in each browser (Settings → Preferences, or the menu at the top right).")
	if details {
		fmt.Println("  change them: metarush account preferences --estimates on|off · metarush assistant mode ask|auto")
		if p.Owner {
			fmt.Println("  for everyone (owners): metarush account preferences --require-estimates on|off")
		}
	}
}

// needAPerson refuses a sign-in change outside an interactive terminal.
func needAPerson(c *cobra.Command) error {
	if interactive() {
		return nil
	}
	return failErr(c, apperr.New(apperr.HumanOnly, "Changing people and sign-in needs a person at this computer.").
		WithHint("Run it in your own terminal, or use Settings → People in the dashboard."))
}

// askPassword reads a password without echo; spaces count (only the line end is dropped).
func askPassword(q string) string {
	fmt.Printf("%s (input hidden): ", q)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(b), "\r\n")
}

// newPassword asks for a new password twice and checks it before anything is sent.
func newPassword(c *cobra.Command, email, name string) (string, error) {
	fmt.Printf("Choose a password: at least %d characters (a few words together works well).\n", auth.MinPassword)
	for range 3 {
		pw := askPassword("New password")
		if err := auth.CheckPassword(pw, email, name); err != nil {
			fmt.Println(err.Error())
			continue
		}
		if askPassword("Type it again") != pw {
			fmt.Println("Those don't match. Try again.")
			continue
		}
		return pw, nil
	}
	return "", fail(c, apperr.Canceled, "Nothing changed.", "")
}

func callAsPerson[O any](c *cobra.Command, name string, in any) (*O, error) {
	a, err := openApp(c, "cli")
	if err != nil {
		return nil, err
	}
	defer a.Close()
	out, err := ops.CallTyped[O](c.Context(), a, name, in, ops.Caller{Human: true})
	if err != nil {
		return nil, failErr(c, err)
	}
	return out, nil
}

func authStatusCmd() *cobra.Command {
	return forOps(&cobra.Command{Use: "status", Short: "How people sign in here, and who you are", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "auth.status", ops.EmptyIn{}, func(o any) {
				st := o.(*auth.AuthStatusOut)
				fmt.Println(st.Summary)
				if st.You != nil {
					fmt.Printf("You: %s%s (%s)\n", st.You.Name, map[bool]string{true: " <" + st.You.Email + ">", false: ""}[st.You.Email != ""], st.You.Role)
				}
				fmt.Printf("%d people can sign in or have an invite.\n", st.People)
				if len(st.Owners) > 0 {
					fmt.Printf("Owners (they change this computer's settings): %s\n", strings.Join(st.Owners, ", "))
				}
				if st.PublicURL != "" {
					fmt.Println("Other computers use " + st.PublicURL)
				}
			})
		}}, "auth.status")
}

func authMethodCmd() *cobra.Command {
	var email, login string
	cmd := &cobra.Command{Use: "method single|password|tailscale", Short: "Choose how people sign in, giving your own sign-in at the same step (people only, on this computer)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := needAPerson(c); err != nil {
				return err
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			in := auth.AuthMethodIn{Method: args[0]}
			switch args[0] {
			case "password":
				if email == "" {
					email = ask("Your email (how you sign in)", a.Cfg.User.Email)
				}
				in.Email = email
				if auth.HasPassword(c.Context(), a, a.Cfg.User.ID) {
					in.CurrentPassword = askPassword("Your current password")
					if !askYes("Choose a new password too?", false) {
						break
					}
				}
				if in.Password, err = newPassword(c, email, a.Cfg.User.Name); err != nil {
					return err
				}
			case "tailscale":
				if login == "" {
					login = ask("Your Tailscale login", a.Cfg.User.Email)
				}
				in.TailscaleLogin = login
			case "single":
				if !askYes("Go back to just you on this computer? Everyone else is signed out (their history stays).", false) {
					return fail(c, apperr.Canceled, "Nothing changed.", "")
				}
			default:
				return fail(c, apperr.InvalidInput, "Choose single, password or tailscale.", "")
			}
			out, err := ops.CallTyped[auth.AuthStatusOut](c.Context(), a, "auth.method.set", in, ops.Caller{Human: true})
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() {
				fmt.Println(out.Summary)
				if out.Method != "single" {
					fmt.Println("Invite people with: metarush users invite <email>  (or Settings → People)")
				}
			})
		}}
	cmd.Flags().StringVar(&email, "email", "", "password: your email")
	cmd.Flags().StringVar(&login, "login", "", "tailscale: your Tailscale login")
	return forOps(cmd, "auth.method.set")
}

func authResetCmd() *cobra.Command {
	return forOps(&cobra.Command{Use: "reset", Short: "Put sign-in back to a fresh install's: just you on this computer, everyone signed out, people kept with their history (people only)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := needAPerson(c); err != nil {
				return err
			}
			if !askYes("Put sign-in back to just you on this computer? Every browser is signed out on every computer; nobody's work changes.", false) {
				return fail(c, apperr.Canceled, "Nothing changed.", "")
			}
			out, err := callAsPerson[auth.AuthResetOut](c, "auth.reset", ops.EmptyIn{})
			if err != nil {
				return err
			}
			return emit(c, out, func() { fmt.Println(out.Summary) })
		}}, "auth.reset")
}

func usersListCmd() *cobra.Command {
	return forOps(&cobra.Command{Use: "list", Short: "The workspace's people, with their roles and whether they can sign in", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "users.list", ops.EmptyIn{}, func(o any) {
				out := o.(*auth.UsersListOut)
				for _, p := range out.People {
					state := map[string]string{"active": "can sign in", "invited": "invited", "disabled": "turned off", "earlier": "earlier history, no account"}[p.State]
					line := fmt.Sprintf("  %-24s %-30s %-7s %s", trunc(p.Name, 24), trunc(p.Email, 30), p.Role, state)
					if p.Work > 0 {
						line += fmt.Sprintf(" · %d things done", p.Work)
					}
					if p.You {
						line += " · you"
					}
					fmt.Println(line)
				}
			})
		}}, "users.list")
}

func printLink(out *auth.UserLinkOut) {
	fmt.Println(out.Summary)
	fmt.Printf("\n    %s\n\n", out.Link)
	fmt.Println("It is shown only now. It works until " + out.ExpiresAt.Local().Format("Mon 2 Jan 15:04") + ".")
}

func usersInviteCmd() *cobra.Command {
	var in auth.UsersInviteIn
	cmd := &cobra.Command{Use: "invite <email>", Short: "Invite a person: a one-time link they follow to choose a password (people only)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := needAPerson(c); err != nil {
				return err
			}
			in.Email = args[0]
			out, err := callAsPerson[auth.UserLinkOut](c, "users.invite", in)
			if err != nil {
				return err
			}
			return emit(c, out, func() { printLink(out) })
		}}
	cmd.Flags().StringVar(&in.Name, "name", "", "the name people see for them (they can change it)")
	cmd.Flags().StringVar(&in.Role, "role", "", "member (default) or owner")
	cmd.Flags().StringVar(&in.LinkTo, "link-to", "", "an earlier person (usr_…) whose history becomes theirs (found by email when not given)")
	return forOps(cmd, "users.invite")
}

func usersLinkCmd(use, op, short string) *cobra.Command {
	return forOps(&cobra.Command{Use: use + " <email|usr_…>", Short: short + " (people only)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := needAPerson(c); err != nil {
				return err
			}
			out, err := callAsPerson[auth.UserLinkOut](c, op, auth.UserRefIn{User: args[0]})
			if err != nil {
				return err
			}
			return emit(c, out, func() { printLink(out) })
		}}, op)
}

func usersRefCmd(use, op, short string) *cobra.Command {
	return forOps(&cobra.Command{Use: use + " <email|usr_…>", Short: short + " (people only)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := needAPerson(c); err != nil {
				return err
			}
			out, err := callAsPerson[auth.UserOut](c, op, auth.UserRefIn{User: args[0]})
			if err != nil {
				return err
			}
			return emit(c, out, func() { fmt.Println(out.Summary) })
		}}, op)
}

func usersRoleCmd() *cobra.Command {
	return forOps(&cobra.Command{Use: "role <email|usr_…> owner|member", Short: "Make a person an owner or a member (people only)", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			if err := needAPerson(c); err != nil {
				return err
			}
			out, err := callAsPerson[auth.UserOut](c, "users.role", auth.UserRoleIn{User: args[0], Role: args[1]})
			if err != nil {
				return err
			}
			return emit(c, out, func() { fmt.Println(out.Summary) })
		}}, "users.role")
}

func accountPasswordCmd() *cobra.Command {
	return forOps(&cobra.Command{Use: "password", Short: "Change your own password (asks for the current one); signs you out everywhere", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := needAPerson(c); err != nil {
				return err
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			in := auth.AccountPasswordIn{}
			if auth.HasPassword(c.Context(), a, a.Cfg.User.ID) {
				in.Current = askPassword("Your current password")
			}
			if in.New, err = newPassword(c, a.Cfg.User.Email, a.Cfg.User.Name); err != nil {
				return err
			}
			out, err := ops.CallTyped[auth.UserOut](c.Context(), a, "account.password", in, ops.Caller{Human: true})
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() { fmt.Println(out.Summary) })
		}}, "account.password")
}

func accountUpdateCmd() *cobra.Command {
	var in auth.AccountUpdateIn
	cmd := &cobra.Command{Use: "update", Short: "Change your own name or email (an email change asks for your password)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := needAPerson(c); err != nil {
				return err
			}
			if in.Email != "" {
				in.CurrentPassword = askPassword("Your current password")
			}
			out, err := callAsPerson[auth.UserOut](c, "account.update", in)
			if err != nil {
				return err
			}
			return emit(c, out, func() { fmt.Println(out.Summary) })
		}}
	cmd.Flags().StringVar(&in.Name, "name", "", "your name as people see it")
	cmd.Flags().StringVar(&in.Email, "email", "", "your email (how you sign in)")
	return forOps(cmd, "account.update")
}

func accountSignoutCmd() *cobra.Command {
	return forOps(&cobra.Command{Use: "signout-everywhere", Short: "Sign yourself out on every computer and browser", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := needAPerson(c); err != nil {
				return err
			}
			out, err := callAsPerson[auth.UserOut](c, "account.signoutEverywhere", ops.EmptyIn{})
			if err != nil {
				return err
			}
			return emit(c, out, func() { fmt.Println(out.Summary) })
		}}, "account.signoutEverywhere")
}

func accountAvatarCmd() *cobra.Command {
	var remove bool
	cmd := &cobra.Command{Use: "avatar [picture]", Short: "Set your picture from a PNG, JPEG or WebP file (kept as a small square), or remove it with --remove", Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in := auth.AvatarIn{Remove: remove}
			if !remove {
				if len(args) != 1 {
					return fail(c, apperr.InvalidInput, "Give the picture's file, or --remove.", "")
				}
				b, err := os.ReadFile(args[0])
				if err != nil {
					return fail(c, apperr.InvalidInput, "MetaRush couldn't read that file.", "")
				}
				in.Data = base64.StdEncoding.EncodeToString(b)
			}
			out, err := callAsPerson[auth.AvatarOut](c, "account.avatar", in)
			if err != nil {
				return err
			}
			return emit(c, out, func() { fmt.Println(out.Summary) })
		}}
	cmd.Flags().BoolVar(&remove, "remove", false, "remove your picture (your initials show instead)")
	return forOps(cmd, "account.avatar")
}
