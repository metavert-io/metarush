package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/components"
	"github.com/metavert-io/metarush/internal/ops"
)

// componentsCmd lists, adds and removes MetaRush's optional components
// (D324–D327): the same registry setup, doctor, Home's Getting started and
// Settings → Components read.
func componentsCmd() *cobra.Command {
	var details bool
	cmd := &cobra.Command{
		Use:     "components [name]",
		Aliases: []string{"component"},
		Short:   "What MetaRush can use on this computer: what each optional part unlocks, whether it is here, and adding or removing one",
		Long: `MetaRush needs only itself, a place for your projects and a storage folder to start. Everything else is
an optional component that unlocks named features: FFmpeg (bringing in footage), Node.js and the Remotion renderer
(finished videos), transcription on this computer, the link downloader, PDF pages and more.

  metarush components                   what is here and what each one adds
  metarush components add renderer      add one (people only): with Homebrew, or MetaRush's own checked download
  metarush components remove renderer   take one MetaRush added off this computer

Settings → Components in the dashboard does the same.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in := components.ComponentsIn{}
			if len(args) == 1 {
				in.Name = args[0]
			}
			return runOp(c, "components.list", in, func(o any) { printComponents(o.(*components.ComponentsOut), details || detailsMode(c)) })
		},
	}
	addDetails(cmd, &details)
	cmd.AddCommand(componentsAddCmd(), componentsRemoveCmd())
	return cmd
}

func componentsAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <name>",
		Short: "Add an optional component to this computer (people only): with Homebrew where it is, else MetaRush's own checked download",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := personAddsSoftware(c); err != nil {
				return err
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			if cp, ok := components.Get(args[0]); ok && !jsonMode(c) {
				fmt.Printf("Adding %s (%s, usually %s)…\n", cp.Title, cp.Size(), cp.Time())
			}
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			if !jsonMode(c) {
				ctx = components.WithSay(ctx, func(_, msg string) {
					if msg != "" {
						fmt.Println("  " + msg)
					}
				})
			}
			c.SetContext(ctx)
			return runOpWith(c, a, "components.add", components.ComponentAddIn{Name: args[0]}, ops.Caller{Human: true}, func(o any) {
				out := o.(*components.ComponentAddOut)
				printComponentAdded(c, out.Added, out.Component)
			})
		},
	}
}

func componentsRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an optional component MetaRush added to this computer, and stop it being set up by itself (people only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := personAddsSoftware(c); err != nil {
				return err
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "components.remove", components.ComponentRemoveIn{Name: args[0]}, ops.Caller{Human: true}, func(o any) {
				out := o.(*components.ComponentRemoveOut)
				fmt.Println(out.Summary)
				if detailsMode(c) && out.Component.RemoveSays != "" {
					fmt.Println("    " + out.Component.RemoveSays)
				}
			})
		},
	}
}

// personAddsSoftware: adding or removing programs on this computer is a
// person's decision (D327) — never Claude Code (in the dashboard or in a
// terminal), an AI app or a script nobody watches.
func personAddsSoftware(c *cobra.Command) error {
	if agentAtTheKeys(c) || !isInteractive() {
		return failErr(c, apperr.New(apperr.HumanOnly, "Only a person adds or removes programs on this computer.").
			WithHint("Add it in Settings → Components, or run this command yourself in your own terminal."))
	}
	return nil
}

// agentAtTheKeys: Claude Code or another AI app runs this command.
func agentAtTheKeys(c *cobra.Command) bool {
	if os.Getenv("CLAUDECODE") != "" || assistantMode() || os.Getenv("METARUSH_AGENT") != "" {
		return true
	}
	if f := c.Flags().Lookup("agent"); f != nil && f.Value.String() != "" {
		return true
	}
	return false
}

func printComponents(o *components.ComponentsOut, details bool) {
	var required, ready, missing, elsewhere []components.ComponentState
	for _, s := range o.Components {
		switch {
		case s.Required:
			required = append(required, s)
		case s.Status == components.StatusNotHere:
			elsewhere = append(elsewhere, s)
		case s.Ready:
			ready = append(ready, s)
		default:
			missing = append(missing, s)
		}
	}
	line := func(mark string, s components.ComponentState, extra string) {
		fmt.Printf("  %s %s — %s\n", mark, s.Title, extra)
		if details {
			bits := []string{s.Name}
			for _, b := range []string{s.Version, s.Path, s.Detail} {
				if b != "" {
					bits = append(bits, b)
				}
			}
			fmt.Printf("      %s\n", strings.Join(bits, " · "))
			if !s.Ready && s.Command != "" {
				fmt.Printf("      %s\n", s.Command)
			}
		}
	}
	if len(required) > 0 {
		fmt.Println("What MetaRush needs to run:")
		for _, s := range required {
			mark := "✓"
			if !s.Ready {
				mark = "✗"
			}
			line(mark, s, s.Says)
		}
	}
	if len(ready) > 0 {
		fmt.Println("\nOn this computer:")
		for _, s := range ready {
			line("✓", s, s.Unlocks)
		}
	}
	if len(missing) > 0 {
		fmt.Println("\nYou can add:")
		for _, s := range missing {
			extra := s.Unlocks + " (" + s.Size + ")"
			switch s.Status {
			case components.StatusAdding, components.StatusFailed, components.StatusNeedsYou:
				extra += " " + s.Says
			}
			if s.Suggested {
				extra += " Suggested."
			}
			line("·", s, extra)
		}
	}
	if len(elsewhere) > 0 && details {
		fmt.Println("\nNot for this computer:")
		for _, s := range elsewhere {
			line("·", s, s.Says)
		}
	}
	fmt.Println("\n" + o.Summary)
	if len(missing) > 0 && !details {
		fmt.Println("Add one in Settings → Components. (Add --details to see the commands.)")
	}
}

func printComponentAdded(c *cobra.Command, a *components.ComponentAdded, st components.ComponentState) {
	if a == nil {
		fmt.Println(st.Says)
		return
	}
	fmt.Println(a.Says)
	if a.Done && a.Licence != "" {
		fmt.Println(a.Licence)
	}
	if a.NeedsYou && detailsMode(c) && st.Command != "" {
		fmt.Println("    " + st.Command)
	}
}
