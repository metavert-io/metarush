package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// Moving the workspace's database (D334–D338, docs/database-move.md):
// database plan | move | switch | abandon | move-status. The new database's
// address is never taken on the command line, where it would stay in the
// shell's history: --to address reads it from standard input (or asks for it,
// hidden, at a terminal), --to env:NAME from an environment variable, and
// --to saved uses the one an earlier plan kept in this computer's keychain.
// Moving, switching and abandoning are a person's, at their own terminal (or
// in the dashboard), never Claude Code's or an AI app's.

// moveTo is the --to, --name and --only-this-computer of plan and move.
type moveTo struct {
	to, name string
	only     bool
}

func (m *moveTo) flags(c *cobra.Command, what string) {
	f := c.Flags()
	f.StringVar(&m.to, "to", "", what+": here (the database on this computer), address (another MongoDB: its address read from standard input, never typed on the command line), env:NAME (its address in that environment variable) or saved (the address an earlier plan kept on this computer)")
	f.StringVar(&m.name, "name", "", "the new database's name (default metarush)")
	f.BoolVar(&m.only, "only-this-computer", false, "moving to the database on this computer: this computer will be the only one using the workspace (other computers couldn't reach it)")
}

// readAddress reads a database address from standard input (hidden at a terminal).
func readAddress(c *cobra.Command, what string) (string, error) {
	var uri string
	if term.IsTerminal(int(os.Stdin.Fd())) {
		uri = promptSecret(what)
	} else {
		line, err := bufio.NewReader(io.LimitReader(c.InOrStdin(), 16<<10)).ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		uri = line
	}
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return "", apperr.New(apperr.InvalidInput, "No database address came in.").
			WithHint("Paste it when asked, or send it on standard input (it is never typed on the command line).")
	}
	return uri, nil
}

// target reads --to: the address, when one is given, is kept on this computer
// for the move (ops.SaveMoveAddress) and the operation names only "saved".
func (m *moveTo) target(c *cobra.Command, a *app.App, switching bool) (ops.DatabaseTargetIn, error) {
	in := ops.DatabaseTargetIn{Name: m.name, OnlyThisComputer: m.only}
	to := strings.TrimSpace(m.to)
	low := strings.ToLower(to)
	keep := func(uri string) error {
		if err := ops.SaveMoveAddress(a.Cfg, uri); err != nil {
			return err
		}
		in.To = ops.MoveToSaved
		return nil
	}
	switch {
	case strings.HasPrefix(low, "mongodb://") || strings.HasPrefix(low, "mongodb+srv://") || strings.Contains(to, "@"):
		return in, apperr.New(apperr.InvalidInput, "A database address isn't taken on the command line, where it would stay in the shell's history.").
			WithHint("Send it on standard input instead (--to address), or name an environment variable that holds it (--to env:NAME).")
	case to == "here" && !switching:
		in.To = ops.MoveToHere
	case to == "address":
		uri, err := readAddress(c, "The new database's address")
		if err != nil {
			return in, err
		}
		return in, keep(uri)
	case strings.HasPrefix(to, "env:"):
		name := strings.TrimPrefix(to, "env:")
		uri := strings.TrimSpace(os.Getenv(name))
		if name == "" || uri == "" {
			return in, apperr.New(apperr.InvalidInput, "That environment variable is empty.").WithDetails(map[string]any{"variable": name})
		}
		return in, keep(uri)
	case to == "saved" || to == "keychain":
		in.To = ops.MoveToSaved
	case to == "":
		return in, apperr.New(apperr.InvalidInput, "Say where the database goes.").
			WithHint("--to here for the database on this computer; --to address for another MongoDB (its address read from standard input).")
	default:
		return in, apperr.New(apperr.InvalidInput, "Say where the database goes: here, address, env:NAME or saved.")
	}
	return in, nil
}

func databaseMoveCmds() []*cobra.Command {
	return []*cobra.Command{databasePlanCmd(), databaseMoveCmd(), databaseSwitchCmd(), databaseAbandonCmd(), databaseMoveStatusCmd()}
}

func databasePlanCmd() *cobra.Command {
	var m moveTo
	cmd := &cobra.Command{
		Use:   "plan --to here|address|env:NAME|saved [--name <database>]",
		Short: "Say what moving the workspace's database would do — what moves, how long it takes, what each computer needs — and check the new database; nothing is changed (owners)",
		Long: `Plans a move of the workspace's database to another one: the database on this computer (--to here,
set up the way setup does it), or another MongoDB such as MongoDB Atlas or your own server. Its address
is never typed on the command line: --to address reads it from standard input (or asks for it at a
terminal), --to env:NAME reads an environment variable; it is kept in this computer's keychain for the
move (--to saved uses it again). The plan says how much moves, how long it takes, which computers use
the workspace and what each needs (its own sign-in for the new database), what waits meanwhile, and
whether the new database answers, holds nothing and takes writes. It changes nothing.`,
		Example: `  metarush database plan --to here
  metarush database plan --to address --name studio < atlas-address.txt`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			in, err := m.target(c, a, false)
			if err != nil {
				return failErr(c, err)
			}
			return runOpWith(c, a, "database.plan", ops.DatabasePlanIn{DatabaseTargetIn: in}, ops.Caller{Human: interactive()}, func(o any) {
				printDatabasePlan(c, o.(*ops.DatabasePlanOut))
			})
		},
	}
	m.flags(cmd, "where the database goes")
	return cmd
}

func databaseMoveCmd() *cobra.Command {
	var m moveTo
	var yes bool
	var dashboardMove string
	cmd := &cobra.Command{
		Use:   "move --to here|address|env:NAME|saved [--name <database>]",
		Short: "Move the workspace's database to another one: changes pause on every computer, everything is copied and checked, this computer switches; the old database is kept (owners, a person at a terminal)",
		Long: `Moves the workspace's database to another one, the way the plan says (run it first). Changes pause on
every computer while it moves: MetaRush on each one stops its background work and refuses changes,
reading keeps working. Everything is copied into the new, empty database, which stays locked until every
kind of record is checked against the old one; then this computer switches to it and the old database is
marked as moved, so every computer still using it refuses changes there until it switches too (with its
own sign-in: metarush database switch). The old database is never deleted.

Stopped part-way, run it again to continue, or abandon it (metarush database abandon) to let changes go
on in the old database. Only a person at this computer moves it (or in Settings → Storage).`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if dashboardMove != "" {
				return runDashboardDatabaseMove(c, dashboardMove)
			}
			if err := personAtTerminal(c, "moves"); err != nil {
				return err
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			in, err := m.target(c, a, false)
			if err != nil {
				return failErr(c, err)
			}
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			plan, err := ops.CallTyped[ops.DatabasePlanOut](ctx, a, "database.plan", ops.DatabasePlanIn{DatabaseTargetIn: in}, ops.Caller{Human: true})
			if err != nil {
				return failErr(c, err)
			}
			if plan.Nothing || !plan.Ready {
				if !jsonMode(c) {
					printDatabasePlan(c, plan)
				}
				if plan.Nothing {
					return emit(c, plan, func() {})
				}
				return failErr(c, apperr.New(apperr.Conflict, "Nothing was moved: %s", strings.TrimSpace(lowerFirstWord(plan.Problems[0]))).
					WithDetails(map[string]any{"reason": "not_ready", "problems": plan.Problems}))
			}
			if !jsonMode(c) {
				printDatabasePlan(c, plan)
			}
			if !yes && ask(`Type "move" to go ahead`, "") != "move" {
				return fail(c, apperr.Canceled, "Nothing was moved.", "")
			}
			pp := newProgress(c)
			ctx = ops.WithProgress(ctx, pp.update)
			b, _ := json.Marshal(ops.DatabaseMoveIn{DatabaseTargetIn: in})
			out, err := ops.Call(ctx, a, "database.move", b, ops.Caller{Human: true})
			pp.done()
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() { printDatabaseMove(c, out.(*ops.DatabaseMoveOut)) })
		},
	}
	m.flags(cmd, "where the database goes")
	cmd.Flags().BoolVar(&yes, "yes", false, "don't ask again before moving (still a person at a terminal)")
	// The process the dashboard starts for a move a person confirmed there
	// (D338); it needs that move's one-time grant on standard input.
	cmd.Flags().StringVar(&dashboardMove, "dashboard-move", "", "a move confirmed in the dashboard (started by the dashboard itself)")
	_ = cmd.Flags().MarkHidden("dashboard-move")
	return cmd
}

// runDashboardDatabaseMove is the process the dashboard starts for a move a
// person confirmed there: it takes the one-time grant from standard input and
// moves the database like `database move` does. It writes to the move's log.
func runDashboardDatabaseMove(c *cobra.Command, id string) error {
	if assistantMode() || agentAtTheKeys(c) {
		return fail(c, apperr.HumanOnly, "Only a person moves the workspace's database.", "")
	}
	token, _ := bufio.NewReader(io.LimitReader(os.Stdin, 256)).ReadString('\n')
	a, err := openApp(c, "dashboard")
	if err != nil {
		return err
	}
	defer a.Close()
	ctx, cancel := signalCtx(c.Context())
	defer cancel()
	ctx, run, err := ops.ClaimDashboardDatabaseMove(ctx, a, id, token)
	if err != nil {
		return failErr(c, err)
	}
	if run.By != "" {
		a.Actor.UserName = firstNonEmpty(a.Actor.UserName, run.By)
	}
	fmt.Printf("%s moving the database to %s\n", time.Now().Format(time.RFC3339), run.To.Words)
	ctx = ops.WithProgress(ctx, func(label string, done, total int64) {})
	out, err := ops.CallTyped[ops.DatabaseMoveOut](ctx, a, "database.move", ops.DatabaseMoveIn{DatabaseTargetIn: run.Target}, ops.Caller{Human: true})
	if err != nil {
		fmt.Printf("%s %s\n", time.Now().Format(time.RFC3339), apperr.From(err).Message)
		return failErr(c, err)
	}
	fmt.Printf("%s %s\n", time.Now().Format(time.RFC3339), out.Summary)
	return nil
}

func databaseSwitchCmd() *cobra.Command {
	var to, name string
	cmd := &cobra.Command{
		Use:   "switch --to address|env:NAME|saved [--name <database>]",
		Short: "Switch this computer to the database the workspace moved to, with this computer's own sign-in (owners, a person at this computer)",
		Long: `After another computer moved the workspace's database, this computer refuses changes until it switches
too. Give it this computer's own sign-in for the new database (a database user of its own: sign-ins are
never copied between computers), read from standard input (--to address) or an environment variable
(--to env:NAME), never the command line. MetaRush checks it reaches the very copy the move made, then
switches; MetaRush running in the background starts again with it. Settings → Storage does the same.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := personAtTerminal(c, "switches"); err != nil {
				return err
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			m := moveTo{to: to, name: name}
			if _, err := m.target(c, a, true); err != nil {
				return failErr(c, err)
			}
			return runOpWith(c, a, "database.switch", ops.DatabaseSwitchIn{Name: name}, ops.Caller{Human: true}, func(o any) {
				s := o.(*ops.DatabaseSwitchOut)
				fmt.Println(s.Summary)
				for _, n := range s.Next {
					fmt.Println("  - " + n)
				}
				if detailsMode(c) {
					fmt.Printf("From: %s\nTo:   %s (%s, database %s)\n", s.From.Words, s.To.Words, s.To.Host, s.To.Name)
				}
			})
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "where this computer's sign-in for the new database comes from: address (standard input), env:NAME or saved")
	cmd.Flags().StringVar(&name, "name", "", "the new database's name (default: the one the move named)")
	return cmd
}

func databaseAbandonCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "abandon",
		Short: "Abandon a move of the database that stopped: changes go on in the old database; the copy keeps its lock (owners, a person at a terminal)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := personAtTerminal(c, "abandons a move of"); err != nil {
				return err
			}
			if !yes && !askYes("Abandon the move? Changes go on in the old database; the copy stays locked and unused.", false) {
				return fail(c, apperr.Canceled, "Nothing was changed.", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "database.abandon", ops.DatabaseAbandonIn{}, ops.Caller{Human: true}, func(o any) {
				fmt.Println(o.(*ops.DatabaseAbandonOut).Summary)
			})
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "don't ask again (still a person at a terminal)")
	return cmd
}

func databaseMoveStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "move-status",
		Short: "Say whether the workspace is being moved to another database or moved (then switch this computer too), and how the last move here went",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "database.move.status", ops.DatabaseMoveStatusIn{}, func(o any) { printDatabaseMoveStatus(c, o.(*ops.DatabaseMoveStatusOut)) })
		},
	}
}

func printDatabasePlan(c *cobra.Command, p *ops.DatabasePlanOut) {
	fmt.Println(p.Summary)
	if p.Nothing {
		return
	}
	if len(p.Counts) > 0 {
		var parts []string
		for _, n := range p.Counts {
			parts = append(parts, fmt.Sprintf("%s %s", thousandsInt(n.Count), n.What))
		}
		fmt.Println("It holds " + strings.Join(parts, ", ") + ".")
	}
	for _, f := range p.Frozen {
		fmt.Println("  " + f)
	}
	fmt.Println("Computers:")
	for _, cp := range p.Computers {
		name := cp.Name
		if cp.This {
			name += " (this computer)"
		}
		fmt.Printf("  %s: %s\n", name, cp.Needs)
	}
	for _, pr := range p.Problems {
		fmt.Println("! " + pr)
	}
	for _, n := range p.Notes {
		fmt.Println("  " + n)
	}
	fmt.Println(p.Kept)
	if detailsMode(c) {
		fmt.Printf("From: %s (%s, database %s)\nTo:   %s (%s, database %s)\n", p.From.Words, p.From.Host, p.From.Name, p.To.Words, p.To.Host, p.To.Name)
		fmt.Printf("Records: %d (%s) in %d kinds; about %ds\n", p.Documents, fmtBytes(p.Bytes), len(p.Collections), p.Seconds)
		for _, col := range p.Collections {
			if col.Carried {
				fmt.Printf("  %-28s %8d  %s\n", col.Name, col.Count, fmtBytes(col.Bytes))
			} else {
				fmt.Printf("  %-28s %8d  stays: %s\n", col.Name, col.Count, col.Why)
			}
		}
	}
}

func printDatabaseMove(c *cobra.Command, r *ops.DatabaseMoveOut) {
	fmt.Println(r.Summary)
	if len(r.Next) > 0 {
		fmt.Println("Next:")
		for _, n := range r.Next {
			fmt.Println("  - " + n)
		}
	}
	if detailsMode(c) {
		fmt.Printf("From: %s\nTo:   %s (%s, database %s)\n", r.From.Words, r.To.Words, r.To.Host, r.To.Name)
		fmt.Printf("Records: %d in %d kinds, checked; %.0fs\n", r.Documents, len(r.Collections), r.Seconds)
	}
}

func printDatabaseMoveStatus(c *cobra.Command, s *ops.DatabaseMoveStatusOut) {
	fmt.Println(s.Summary)
	if r := s.Run; r != nil && (s.Moving || r.State != "done" || s.State == "") && r.Summary != s.Summary {
		fmt.Println("The last move started on this computer: " + r.Summary)
		for _, n := range r.Next {
			fmt.Println("  - " + n)
		}
	}
	for _, st := range s.Steps {
		fmt.Println("  - " + st)
	}
	if detailsMode(c) {
		fmt.Printf("This computer: %s (%s, database %s)\n", s.This.Words, s.This.Host, s.This.Name)
		if s.Command != "" {
			fmt.Println("switch: " + s.Command)
		}
	}
}

func lowerFirstWord(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
