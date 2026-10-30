package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// Something written into the old database after a move (D352–D358,
// docs/database-move.md): `metarush database late-writes` looks at the old
// database again (on the computer that moved it, while it keeps its address)
// and lists what was found; `late-writes show` puts both versions of one side
// by side; `late-writes carry|leave` decide, a person at their own terminal.

func databaseLateWritesCmd() *cobra.Command {
	var noLook bool
	cmd := &cobra.Command{
		Use:   "late-writes",
		Short: "Look at the old database again for anything written there after the workspace moved, and list what was found (owners)",
		Long: `After the workspace's database moved, a computer that was asleep through the move could still write
something into the old database, where nobody would see it. The computer that moved it looks at the old
database again right after the move, then about 15 minutes, an hour and a day later, and says what it
finds: records added, changed or removed there after the move. This command looks again now (on that
computer, while it keeps the old database's address; --no-look only lists) and lists what was found,
numbered. A look only reads the old database.

Then, a person at their own terminal:
  metarush database late-writes show 1     # the old database's version beside this database's
  metarush database late-writes carry 1    # carry it over (only while this database's record is still the one the move copied)
  metarush database late-writes leave 1    # this database keeps its own`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "database.late.writes", ops.DatabaseLateWritesIn{Look: !noLook}, func(o any) { printLateWrites(c, o.(*ops.DatabaseLateWritesOut)) })
		},
	}
	cmd.Flags().BoolVar(&noLook, "no-look", false, "only list what was found before; don't look at the old database now")
	cmd.AddCommand(databaseLateShowCmd(), databaseLateCarryCmd(), databaseLateLeaveCmd())
	return cmd
}

func databaseLateShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <number>",
		Short: "Show a record written into the old database after the move beside this database's own (owners, a person at a terminal)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := lateAtTerminal(c); err != nil {
				return err
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "database.late.show", ops.DatabaseLateShowIn{ID: args[0]}, ops.Caller{Human: true}, func(o any) {
				printLateShow(o.(*ops.DatabaseLateShowOut))
			})
		},
	}
}

func databaseLateCarryCmd() *cobra.Command {
	var all, yes bool
	cmd := &cobra.Command{
		Use:   "carry <number>... | --all",
		Short: "Carry records written into the old database after the move over into this one — only while this database's own record is still the one the move copied (owners, a person at a terminal)",
		Long: `Carries the old database's version of each record over into this database, only while this database's
own record is still exactly the one the move copied. When it changed here too (or another record took
its place), nothing changes: both versions are shown, and you decide. A record removed from the old
database is never removed here by MetaRush: remove it yourself if it should go, then leave it.`,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 0 && !all {
				return fail(c, apperr.InvalidInput, "Say which ones: their numbers in the list (metarush database late-writes), or --all.", "")
			}
			if err := lateAtTerminal(c); err != nil {
				return err
			}
			if !yes && !askYes(fmt.Sprintf("Carry %s over into this database?", map[bool]string{true: "every record that waits", false: "the old database's version of " + strings.Join(args, ", ")}[all]), false) {
				return fail(c, apperr.Canceled, "Nothing was carried over.", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			b, _ := json.Marshal(ops.DatabaseLateDecideIn{IDs: args, All: all})
			out, err := ops.Call(ctx, a, "database.late.carry", b, ops.Caller{Human: true})
			if err != nil {
				// Both versions, side by side, when this database's record changed too.
				if e := apperr.From(err); e != nil && e.Code == apperr.Conflict && !jsonMode(c) && len(args) == 1 {
					if s, serr := ops.CallTyped[ops.DatabaseLateShowOut](ctx, a, "database.late.show", ops.DatabaseLateShowIn{ID: args[0]}, ops.Caller{Human: true}); serr == nil {
						defer printLateShow(s)
					}
				}
				return failErr(c, err)
			}
			return emit(c, out, func() {
				r := out.(*ops.DatabaseLateCarryOut)
				fmt.Println(r.Summary)
				for _, f := range r.Refused {
					fmt.Printf("  %d. %s %s\n", f.Found.N, f.Found.Says, f.Why)
				}
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "every record that waits for a person")
	cmd.Flags().BoolVar(&yes, "yes", false, "don't ask again (still a person at a terminal)")
	return cmd
}

func databaseLateLeaveCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "leave <number>... | --all",
		Short: "Leave records written into the old database after the move as they are here: this database keeps its own (owners, a person at a terminal)",
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 0 && !all {
				return fail(c, apperr.InvalidInput, "Say which ones: their numbers in the list (metarush database late-writes), or --all.", "")
			}
			if err := lateAtTerminal(c); err != nil {
				return err
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "database.late.leave", ops.DatabaseLateDecideIn{IDs: args, All: all}, ops.Caller{Human: true}, func(o any) {
				fmt.Println(o.(*ops.DatabaseLateLeaveOut).Summary)
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "every record that waits for a person")
	return cmd
}

// lateAtTerminal: what was written into the old database late is a person's
// to see whole and to decide, at their own terminal (or in the dashboard).
func lateAtTerminal(c *cobra.Command) error {
	if agentAtTheKeys(c) || !atTerminal() {
		return failErr(c, apperr.New(apperr.HumanOnly, "Only a person sees and decides what was written into the old database late, at their own terminal: Claude, Auto and AI apps never do.").
			WithHint("Run the command yourself in a terminal, or decide in Settings → Storage → Move the database."))
	}
	return nil
}

func printLateWrites(c *cobra.Command, o *ops.DatabaseLateWritesOut) {
	if o.Problem != "" {
		fmt.Println(o.Problem)
	} else if o.Looked {
		fmt.Println("Looked at the old database again just now.")
	}
	fmt.Println(o.Summary)
	for _, f := range o.Found {
		fmt.Printf("  %d. %s\n", f.N, f.Says)
		if detailsMode(c) {
			fmt.Printf("     %s %s (%s, %s)\n", f.Collection, f.Record, f.ID, f.Kind)
		}
	}
	for _, s := range o.Steps {
		fmt.Println("  - " + s)
	}
	if len(o.Moves) > 0 {
		m := o.Moves[len(o.Moves)-1]
		if o.Waiting > 0 && m.Summary != o.Summary {
			fmt.Println(m.Summary)
		}
		if detailsMode(c) {
			for _, l := range m.Looks {
				fmt.Printf("look %s (%s): %d found\n", l.At.Local().Format("2 Jan 15:04"), l.When, l.Found)
			}
		}
	}
}

func printLateShow(s *ops.DatabaseLateShowOut) {
	fmt.Println(s.Summary)
	if len(s.Differs) > 0 {
		fmt.Println("They differ in: " + strings.Join(s.Differs, ", ") + ".")
	}
	side := func(title string, raw json.RawMessage) {
		fmt.Println(title)
		if len(raw) == 0 {
			fmt.Println("  (none)")
			return
		}
		var buf bytes.Buffer
		if json.Indent(&buf, raw, "  ", "  ") != nil {
			buf.Reset()
			buf.Write(raw)
		}
		fmt.Println("  " + buf.String())
	}
	side("In the old database:", s.Old)
	side("In this database:", s.Here)
}
