package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// The ship gate (D298, D299): `metarush deliver ready` says whether a version
// can go out and what holds it, `deliver override` sends it out anyway with a
// note (a person at their own terminal), `deliver gate [on|off]` is the
// workspace's choice (owners) and `content gate` a Content Settings' word.

func deliverReadyCmd() *cobra.Command {
	var in ops.ShipGateIn
	cmd := &cobra.Command{
		Use:   "ready <project> [--version rN]",
		Short: "Whether a version can go out to a client: Ready to deliver, or what holds it and how to clear each (--json for every detail)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ProjectID = args[0]
			return runOp(c, "delivery.ready", in, func(o any) { printGate(c, o.(*ops.ShipGateOut)) })
		},
	}
	gateFlags(cmd, &in)
	return cmd
}

func gateFlags(cmd *cobra.Command, in *ops.ShipGateIn) {
	cmd.Flags().StringVar(&in.Revision, "version", "", "a version (rN or its ID); default the latest")
	cmd.Flags().StringVar(&in.Branch, "branch", "", "the cut (default main)")
	cmd.Flags().StringVar(&in.Quality, "quality", "", "final (default) | review")
	cmd.Flags().StringSliceVar(&in.Presets, "preset", nil, "only these shapes (vertical-1080p, landscape-1080p, square-1080)")
}

func printGate(c *cobra.Command, g *ops.ShipGateOut) {
	details := detailsMode(c)
	fmt.Printf("Version %d of %s: %s.\n", g.Number, theirs(g.Project), g.Headline)
	open := 0
	for _, h := range g.Holds {
		if h.Covered {
			continue
		}
		open++
		fmt.Println("  • " + h.Says)
		for i, p := range h.Problems {
			if i == 5 {
				fmt.Printf("      … and %d more (metarush qa show)\n", len(h.Problems)-5)
				break
			}
			line := "      " + p.Message
			if p.At != "" {
				line = "      " + p.At + "  " + p.Message
			}
			fmt.Println(line)
			if p.Fix != "" {
				fmt.Println("        Fix: " + p.Fix)
			}
		}
		for _, p := range h.Paths {
			if !details || p.Command == "" {
				continue
			}
			who := ""
			if p.People {
				who = " (a person)"
			}
			fmt.Printf("      %s%s: %s\n", p.Label, who, p.Command)
		}
	}
	for _, o := range g.SentAnyway {
		fmt.Printf("Sent out anyway by %s on %s: %s\n", personName(o.By), o.At.UTC().Format("2 Jan 2006"), theirs(o.Note))
	}
	for _, w := range g.Warnings {
		fmt.Println(w)
	}
	if !g.On {
		fmt.Println(g.Why)
	} else if open > 0 {
		fmt.Println("Clear what holds it, or send it out anyway with a note (a person): metarush deliver override " + g.ProjectID + fmt.Sprintf(" --version r%d --note \"…\"", g.Number))
	}
	if len(g.Promises) > 0 {
		fmt.Println("Promises made to the client:")
		for _, p := range g.Promises {
			mark := map[string]string{models.PromiseKept: "kept", models.PromiseBroken: "BROKEN", models.PromiseUnmeasured: "couldn't measure", models.PromiseNA: "doesn't apply"}[p.Status]
			fmt.Printf("  %-16s %s — %s\n", mark, theirs(p.Text), p.Measured)
		}
	}
	if details {
		fmt.Println("version:", g.RevisionID)
	}
}

func personName(a models.Actor) string {
	if a.UserName != "" {
		return a.UserName
	}
	return "a person"
}

func deliverOverrideCmd() *cobra.Command {
	var in ops.ShipOverrideIn
	cmd := &cobra.Command{
		Use:   `override <project> [--version rN] --note "<why it goes out anyway>"`,
		Short: "Send a version out anyway past what holds it, with a note (people only: in their own terminal or on the version's page; the note shows on the receipt)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if !interactive() {
				return fail(c, apperr.HumanOnly, "Only a person can send a version out anyway.", "Do it on the version's page in the dashboard, or run this yourself in a terminal.")
			}
			if strings.TrimSpace(in.Note) == "" {
				return fail(c, apperr.InvalidInput, "Say why it goes out anyway with --note: the note is kept with the version and shows on the receipt.", "")
			}
			in.ProjectID = args[0]
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			g, err := ops.CallTyped[ops.ShipGateOut](ctx, a, "delivery.ready", in.ShipGateIn, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			var held []string
			in.Holds = nil
			for _, h := range g.Holds {
				if h.Overridable && !h.Covered {
					held = append(held, h.Says)
					in.Holds = append(in.Holds, h.Key) // the note covers exactly what was shown (D299)
				}
			}
			if len(held) > 0 && !jsonMode(c) {
				fmt.Printf("Version %d is held by:\n", g.Number)
				for _, h := range held {
					fmt.Println("  • " + h)
				}
				if ask("Type \"send\" to send it out anyway with your note", "") != "send" {
					return fail(c, apperr.Canceled, "Nothing changed.", "")
				}
			}
			return runOpWith(c, a, "delivery.override", in, ops.Caller{Human: true}, func(o any) {
				out := o.(*ops.ShipGateOut)
				fmt.Println(out.Summary)
			})
		},
	}
	gateFlags(cmd, &in.ShipGateIn)
	cmd.Flags().StringVar(&in.Note, "note", "", "why it goes out anyway (kept with who and when; shows on the receipt and in what delivery writes)")
	return cmd
}

func deliverGateCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "gate [on|off] [--project <p>]",
		Short: "Whether versions wait for the quality check and their sign-offs before they go out; owners turn it on or off (off: people only)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			show := func(o any) { printGateSettings(o.(*ops.GateSettingsOut)) }
			if len(args) == 0 {
				return runOp(c, "delivery.gate", ops.GateSettingsIn{ProjectID: project}, show)
			}
			on := args[0] == "on"
			if !on && args[0] != "off" {
				return fail(c, apperr.InvalidInput, "Say on or off.", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "delivery.gate.set", ops.GateSetIn{On: on}, ops.Caller{Human: interactive()}, show)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "also what a project's Content Settings say")
	return cmd
}

func contentGateCmd() *cobra.Command {
	var in ops.ContentGateSetIn
	cmd := &cobra.Command{
		Use:   "gate <on|off|workspace> [--project <project>]",
		Short: "Owners: whether the videos of every project using a project's own settings wait for the quality check and sign-offs (on), only remind (off) or follow the workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Mode = args[0]
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "content.gate.set", in, ops.Caller{Human: interactive()}, func(o any) { printGateSettings(o.(*ops.GateSettingsOut)) })
		},
	}
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "a project with its own settings (default: the default's)")
	return cmd
}

func printGateSettings(o *ops.GateSettingsOut) {
	fmt.Println(o.Summary)
	for _, w := range o.Warnings {
		fmt.Println(w)
	}
	if w := o.Workspace; w != nil {
		state := "wait"
		if !w.On {
			state = "only remind"
		}
		fmt.Printf("The workspace: versions %s (%s, %s).\n", state, personName(w.UpdatedBy), w.UpdatedAt.UTC().Format("2 Jan 2006"))
	}
	if s := o.Settings; s != nil && o.Of != "" {
		fmt.Printf("%s's settings say %s.\n", theirs(o.Of), s.Mode)
	}
}
