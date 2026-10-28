package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/handover"
	"github.com/metavert-io/metarush/internal/ops"
)

// Handing a computer's role to another computer (plan item 34, D344–D348):
// `metarush machine handover plan|export|receive|status|abandon|clean` and
// `metarush machine take-over`. Export, receive, clean and take-over are a
// person's, at their own terminal: never Claude Code or another AI app. The
// passphrase is asked for at the terminal (or read from standard input),
// never an argument.

// promptPassphrase reads a passphrase without echo (a variable for tests).
var promptPassphrase = askPassword

func machineHandoverCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "handover", Short: "Hand this computer's role to another computer: the dashboard and background work, the panel's conversations, pinned work and folders (people only)",
		Long: `Moves what is tied to this computer to another computer of the workspace, in four steps:

  here:  metarush machine handover plan --to "<computer>"    # what happens to each thing; nothing changes
  here:  metarush machine handover export --to "<computer>"  # the handover file, protected by a passphrase
  there: metarush machine handover receive <file>            # the conversations go on there; the work moves
  here:  metarush machine handover clean                     # later, only when you're sure: this computer's copies go

Carry the file yourself (a drive, AirDrop or scp), never through a shared or synced folder. This computer steps
down by itself once the other one received it and its own running work is done: it then works as a client.
Keys never travel: each computer keeps its own (the plan says which to set there).`}
	cmd.AddCommand(handoverPlanCmd(), handoverExportCmd(), handoverReceiveCmd(), handoverStatusCmd(), handoverAbandonCmd(), handoverCleanCmd())
	return cmd
}

// handoverAtTerminal: a handover's files and passphrase are a person's, at
// their own terminal — never Claude Code or another AI app (D348).
func handoverAtTerminal(c *cobra.Command) error {
	if agentAtTheKeys(c) || !atTerminal() {
		return failErr(c, apperr.New(apperr.HumanOnly, "Only a person hands a computer's role over, at their own terminal: Claude, Auto and AI apps never do.").
			WithHint("Run the command yourself in a terminal on this computer (Settings → Storage shows the plan)."))
	}
	return nil
}

// readPassphrase is the file's passphrase: from standard input, or asked at
// the terminal (twice for a new file).
func readPassphrase(c *cobra.Command, fromStdin, twice bool) (string, error) {
	if fromStdin {
		r := bufio.NewReader(io.LimitReader(c.InOrStdin(), 4<<10))
		line, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		p := strings.TrimRight(line, "\r\n")
		if p == "" {
			return "", failErr(c, apperr.New(apperr.InvalidInput, "There was no passphrase on standard input."))
		}
		return p, nil
	}
	if !isInteractive() {
		return "", failErr(c, apperr.New(apperr.InvalidInput, "The handover file's passphrase is asked for in a terminal.").
			WithHint("Run it in a terminal, or give the passphrase on standard input with --passphrase-stdin."))
	}
	if !twice {
		return promptPassphrase("The handover file's passphrase"), nil
	}
	fmt.Printf("Choose a passphrase for the handover file: at least %d characters (a few words together works well). You'll type it again on the other computer.\n", handover.MinPassphrase)
	for range 3 {
		p := promptPassphrase("Passphrase")
		if err := handover.CheckPassphrase(p); err != nil {
			fmt.Println(apperr.From(err).Message)
			continue
		}
		if promptPassphrase("Type it again") != p {
			fmt.Println("Those don't match. Try again.")
			continue
		}
		return p, nil
	}
	return "", failErr(c, apperr.New(apperr.Canceled, "No passphrase was chosen, so nothing changed."))
}

// unprotectedWord is what a person types to make a handover file without a
// passphrase.
const unprotectedWord = "unprotected"

// confirmUnprotected: a file anyone who gets it can read is made only when the
// person at the terminal types that they mean it — never from a script or a
// flag alone.
func confirmUnprotected(c *cobra.Command) error {
	if !isInteractive() {
		return failErr(c, apperr.New(apperr.InvalidInput, "A handover file without a passphrase is made only when you confirm it, typing at the terminal.").
			WithHint("Run it in a terminal and type "+unprotectedWord+" when asked, or leave the file protected by a passphrase.").
			WithDetails(map[string]any{"reason": "confirm_unprotected"}))
	}
	fmt.Fprintln(os.Stderr, "Warning: "+handover.NoPassphraseWarning)
	fmt.Fprintf(os.Stderr, "Type %q to make it without a passphrase: ", unprotectedWord)
	line, _ := bufio.NewReader(io.LimitReader(c.InOrStdin(), 256)).ReadString('\n')
	if strings.TrimSpace(line) != unprotectedWord {
		return failErr(c, apperr.New(apperr.Canceled, "Nothing was made: without a passphrase, a handover file is made only when you type %s.", unprotectedWord).
			WithHint("Leave out --no-passphrase to protect the file with a passphrase."))
	}
	return nil
}

func handoverPlanCmd() *cobra.Command {
	var in handover.HandoverPlanIn
	c := &cobra.Command{Use: "plan --to <computer>", Short: "Say what handing this computer's role over would do with each thing tied to it (nothing changes)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if agentAtTheKeys(c) {
				return failErr(c, apperr.New(apperr.HumanOnly, "Only a person hands a computer's role over: Claude, Auto and AI apps never do."))
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "machine.handover.plan", in, ops.Caller{Human: true}, func(o any) { printHandoverPlan(o.(*handover.HandoverPlan), detailsMode(c)) })
		}}
	c.Flags().StringVar(&in.To, "to", "", "the computer taking the role over (its name)")
	return c
}

func printHandoverPlan(p *handover.HandoverPlan, details bool) {
	to := "the new computer"
	if p.To != nil {
		to = theirs(p.To.Name)
	}
	fmt.Printf("Handing %s's role to %s — nothing has changed yet.\n", theirs(p.From.Name), to)
	fmt.Println(p.Summary)
	if p.Role.Role != handover.RoleHost {
		fmt.Println(p.Role.Says)
	}
	section := func(title string) { fmt.Printf("\n%s\n", title) }
	if len(p.Conversations) > 0 {
		section("Conversations in Claude's panel:")
		for _, c := range p.Conversations {
			fmt.Printf("  - %s: %s\n", theirs(c.Title), c.Words)
		}
	}
	if len(p.Work) > 0 {
		section("Work tied to this computer:")
		for _, w := range p.Work {
			fmt.Printf("  - %s: %s\n", capitalize(w.Words), w.Why)
		}
	}
	if len(p.Folders) > 0 {
		section("Folders:")
		for _, f := range p.Folders {
			fmt.Printf("  - %s\n", f.Words)
		}
	}
	section("Remote access:")
	fmt.Printf("  %s\n", p.Remote.Words)
	if len(p.Extensions) > 0 {
		section("Browser extensions paired here:")
		for _, e := range p.Extensions {
			fmt.Printf("  - %s: %s\n", theirs(e.Name), e.Words)
		}
	}
	section("Keys on this computer (names only; they never travel):")
	for _, k := range p.Keys {
		fmt.Printf("  - %s: %s\n", k.Name, k.Words)
	}
	if len(p.Abilities) > 0 {
		section("Abilities set up here:")
		for _, ab := range p.Abilities {
			fmt.Printf("  - %s: %s\n", theirs(ab.Name), ab.Words)
		}
	}
	section(p.Background.Name + ":")
	fmt.Printf("  %s\n", p.Background.Words)
	printHandoverSteps(p.Steps)
	if len(p.Blocking) > 0 {
		section("Before the handover file can be made:")
		for _, b := range p.Blocking {
			fmt.Printf("  - %s\n", b)
		}
	}
	section("Next:")
	for i, n := range p.Next {
		fmt.Printf("  %d. %s\n", i+1, n)
	}
	if details {
		fmt.Printf("\n  this computer %s (%s)\n", p.From.Name, p.From.ID)
		if p.To != nil {
			fmt.Printf("  new computer  %s (%s)\n", p.To.Name, p.To.ID)
		}
		for _, c := range p.Conversations {
			fmt.Printf("  conversation %s: session %s\n", c.ID, c.Session)
		}
		for _, f := range p.Folders {
			fmt.Printf("  %s folder %s\n", f.Kind, f.Path)
		}
	}
}

func printHandoverSteps(steps []handover.HandoverStep) {
	if len(steps) == 0 {
		return
	}
	fmt.Println("\nSteps only a person takes:")
	for _, s := range steps {
		where := "On the new computer"
		if s.Where == "old" {
			where = "Here"
		}
		line := fmt.Sprintf("  - %s: %s", where, s.Words)
		if s.Command != "" {
			line += "\n      " + s.Command
		}
		if s.Click != "" {
			line += "\n      (" + s.Click + ")"
		}
		fmt.Println(line)
	}
}

func handoverExportCmd() *cobra.Command {
	var in handover.HandoverExportIn
	var fromStdin bool
	c := &cobra.Command{Use: "export --to <computer> [--out <file>]", Short: "Make the handover file: the panel's conversations with their Claude Code sessions, the pinned-work list and the folders (people only, at a terminal)", Args: cobra.NoArgs,
		Long: `Makes the private handover file that carries this computer's conversations to the computer taking its role.
It is protected by a passphrase you choose (asked for here, or read from standard input with --passphrase-stdin);
--no-passphrase writes it unprotected once you type "unprotected" to confirm: anyone who has it can read the
conversations. From now on this computer no longer continues those conversations. It never writes over a file,
nor into a synced folder or a repository.
Running it again makes a new file for the same handover; "metarush machine handover abandon" puts everything back.`,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := handoverAtTerminal(c); err != nil {
				return err
			}
			pass := ""
			if in.NoPassphrase {
				if err := confirmUnprotected(c); err != nil {
					return err
				}
			} else {
				p, err := readPassphrase(c, fromStdin, !fromStdin)
				if err != nil {
					return err
				}
				pass = p
			}
			return runHandover(c, "machine.handover.export", in, pass, func(o any) {
				r := o.(*handover.HandoverExportOut)
				fmt.Println(r.Summary)
				for _, w := range r.Warnings {
					fmt.Println("Warning: " + w)
				}
				if r.Encrypted {
					fmt.Println("The file is protected by your passphrase.")
				}
				fmt.Println("\nNext:")
				for i, n := range r.Next {
					fmt.Printf("  %d. %s\n", i+1, n)
				}
				if detailsMode(c) {
					fmt.Printf("\n  file %s (%d bytes)\n  handover %s\n", r.File, r.Bytes, r.Handover.ID)
				} else {
					fmt.Printf("\n  The file: %s\n", r.File)
				}
			})
		}}
	f := c.Flags()
	f.StringVar(&in.To, "to", "", "the computer taking the role over (its name)")
	f.StringVar(&in.Out, "out", "", "where the file goes (default: a new file in ~/.metarush-bundles)")
	f.BoolVar(&in.NoPassphrase, "no-passphrase", false, "write the file unprotected (anyone who has it can read the conversations)")
	f.BoolVar(&fromStdin, "passphrase-stdin", false, "read the passphrase from standard input (one line) instead of asking")
	return c
}

// runHandover calls a handover op as the person at this terminal, with the
// passphrase in the call's context (never its input).
func runHandover(c *cobra.Command, name string, in any, pass string, human func(any)) error {
	a, err := openApp(c, "cli")
	if err != nil {
		return err
	}
	defer a.Close()
	out, err := callHandover(c, a, name, in, pass)
	if err != nil {
		return failErr(c, err)
	}
	return emit(c, out, func() { human(out) })
}

func callHandover(c *cobra.Command, a *app.App, name string, in any, pass string) (any, error) {
	ctx, cancel := signalCtx(c.Context())
	defer cancel()
	if pass != "" {
		ctx = handover.WithPassphrase(ctx, pass)
	}
	b, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	return ops.Call(ctx, a, name, b, ops.Caller{Human: true})
}

func handoverReceiveCmd() *cobra.Command {
	var in handover.HandoverReceiveIn
	var fromStdin, skipAll bool
	var folders, skips []string
	c := &cobra.Command{Use: "receive <file> [--folder OLD=NEW]... [--skip-folder OLD]...", Short: "Receive a handover file on the computer taking the role over (people only, at a terminal)", Args: cobra.ExactArgs(1),
		Long: `Receives the handover file made on the old computer: each conversation's Claude Code session is put where
Claude Code looks on this computer and the conversation goes on here; recipe runs, planning by itself and queued
pinned work move here; the folders go where you say. A conversation whose session didn't come along goes on from a
summary of it. --plan says what it would do and changes nothing. Folders: --folder OLD=NEW sets one, --skip-folder
OLD leaves one out; any not given are asked for here.`,
		RunE: func(c *cobra.Command, args []string) error {
			if err := handoverAtTerminal(c); err != nil {
				return err
			}
			in.File = args[0]
			for _, f := range folders {
				old, to, ok := strings.Cut(f, "=")
				if !ok || old == "" || to == "" {
					return fail(c, apperr.InvalidInput, "--folder takes the folder on the other computer and the folder here, joined by =.", "")
				}
				in.Folders = append(in.Folders, handover.HandoverFolderChoice{Path: old, To: to})
			}
			for _, s := range skips {
				in.Folders = append(in.Folders, handover.HandoverFolderChoice{Path: s, Skip: true})
			}
			pass := ""
			if enc, err := handover.Encrypted(in.File); err == nil && enc {
				p, err := readPassphrase(c, fromStdin, false)
				if err != nil {
					return err
				}
				pass = p
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			// What the file holds first: the folders it needs a place for.
			plan := in
			plan.Plan = true
			po, err := callHandover(c, a, "machine.handover.receive", plan, pass)
			if err != nil {
				return failErr(c, err)
			}
			if in.Plan {
				return emit(c, po, func() { printReceive(po.(*handover.HandoverReceiveOut)) })
			}
			given := map[string]bool{}
			for _, f := range in.Folders {
				given[f.Path] = true
			}
			for _, f := range po.(*handover.HandoverReceiveOut).Folders {
				if given[f.Path] {
					continue
				}
				if skipAll || !isInteractive() || jsonMode(c) {
					if skipAll {
						in.Folders = append(in.Folders, handover.HandoverFolderChoice{Path: f.Path, Skip: true})
					}
					continue
				}
				what := "the watched folder"
				if f.Kind == "linked" {
					what = "the linked folder " + theirs(f.Name)
				}
				to := ask(fmt.Sprintf("Where is %s (%s there) on this computer? Press Enter to skip it", what, f.Path), "")
				if to == "" {
					in.Folders = append(in.Folders, handover.HandoverFolderChoice{Path: f.Path, Skip: true})
				} else {
					in.Folders = append(in.Folders, handover.HandoverFolderChoice{Path: f.Path, To: to})
				}
			}
			out, err := callHandover(c, a, "machine.handover.receive", in, pass)
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() { printReceive(out.(*handover.HandoverReceiveOut)) })
		}}
	f := c.Flags()
	f.BoolVar(&in.Plan, "plan", false, "say what receiving it would do (nothing changes)")
	f.StringArrayVar(&folders, "folder", nil, "OLD=NEW: where a folder of the old computer is on this one")
	f.StringArrayVar(&skips, "skip-folder", nil, "OLD: leave that folder of the old computer out")
	f.BoolVar(&skipAll, "skip-folders", false, "leave out every folder not given with --folder")
	f.BoolVar(&fromStdin, "passphrase-stdin", false, "read the passphrase from standard input (one line) instead of asking")
	return c
}

func printReceive(r *handover.HandoverReceiveOut) {
	fmt.Println(r.Summary)
	if len(r.Conversations) > 0 {
		fmt.Println("\nConversations:")
		for _, c := range r.Conversations {
			fmt.Printf("  - %s: %s\n", theirs(c.Title), c.Words)
		}
	}
	if r.Plan && len(r.Folders) > 0 {
		fmt.Println("\nFolders that need a place here (or a skip):")
		for _, f := range r.Folders {
			fmt.Printf("  - %s\n", f.Words)
		}
	}
	if len(r.Waiting) > 0 {
		fmt.Printf("\nStill finishing on %s before it steps down:\n", theirs(r.From.Name))
		for _, w := range r.Waiting {
			fmt.Printf("  - %s\n", capitalize(w.Words))
		}
	}
	printHandoverSteps(r.Steps)
}

func handoverStatusCmd() *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Where this computer stands — the host, stepping down or a client — and the handovers in progress", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "machine.handover.status", handover.HandoverStatusIn{}, ops.Caller{Human: !agentAtTheKeys(c)}, func(o any) {
				s := o.(*handover.HandoverStatusOut)
				fmt.Println(s.Summary)
				if len(s.Waiting) > 0 {
					fmt.Println("\nStill to finish here before this computer steps down:")
					for _, w := range s.Waiting {
						fmt.Printf("  - %s\n", capitalize(w.Words))
					}
				}
				if s.Clean != nil && s.Role.Role != handover.RoleHost {
					fmt.Printf("\nThis computer still has its copies of the conversations handed to %s. When you're sure all is well there:\n  metarush machine handover clean\n", theirs(s.Clean.ToName))
				}
				if detailsMode(c) {
					for _, r := range s.Recent {
						fmt.Printf("  %s %s %s → %s: %s\n", r.ID, r.Kind, r.FromName, r.ToName, r.State)
					}
				}
			})
		}}
}

func handoverAbandonCmd() *cobra.Command {
	var in handover.HandoverAbandonIn
	c := &cobra.Command{Use: "abandon [handover]", Short: "Abandon a handover that wasn't received: its conversations go on here as before (people only)", Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if agentAtTheKeys(c) || !isInteractive() {
				return failErr(c, apperr.New(apperr.HumanOnly, "Only a person abandons a handover, at their own terminal.").WithHint("Or use Settings → Storage."))
			}
			if len(args) == 1 {
				in.ID = args[0]
			}
			if !jsonMode(c) && !askYes("Abandon the handover? Its conversations go on on this computer, and the handover file can't be received any more.", false) {
				return fail(c, apperr.Canceled, "Nothing changed.", "")
			}
			return runHandover(c, "machine.handover.abandon", in, "", func(o any) { fmt.Println(o.(*handover.HandoverAbandonOut).Summary) })
		}}
	return c
}

func handoverCleanCmd() *cobra.Command {
	var in handover.HandoverCleanIn
	c := &cobra.Command{Use: "clean [--yes]", Short: "After the new computer received its role: remove this computer's copies of the handed-over conversations (people only, at a terminal; never by itself)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := handoverAtTerminal(c); err != nil {
				return err
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			yes := in.Yes
			in.Yes = false
			po, err := callHandover(c, a, "machine.handover.clean", in, "")
			if err != nil {
				return failErr(c, err)
			}
			p := po.(*handover.HandoverCleanOut)
			if !yes {
				if jsonMode(c) || !isInteractive() {
					return emit(c, p, func() { fmt.Println(p.Summary) })
				}
				fmt.Println(p.Summary)
				if ask(`Type "clean" to remove them`, "") != "clean" {
					return fail(c, apperr.Canceled, "Nothing was removed.", "")
				}
			}
			in.Yes = true
			out, err := callHandover(c, a, "machine.handover.clean", in, "")
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() { fmt.Println(out.(*handover.HandoverCleanOut).Summary) })
		}}
	c.Flags().BoolVar(&in.Yes, "yes", false, "remove them without asking again")
	c.Flags().StringVar(&in.ID, "handover", "", "the handover (default: the latest one from this computer that was received)")
	return c
}

func machineTakeOverCmd() *cobra.Command {
	var in handover.HandoverTakeoverIn
	var fromStdin, skipAll bool
	var folders, skips []string
	c := &cobra.Command{Use: "take-over <gone computer> [--from <folder or handover file>]", Short: "Take over the role of a computer that is gone (off for good): its conversations, pinned work and folders come here (people only, at a terminal)", Args: cobra.ExactArgs(1),
		Long: `For a computer that can't hand its role over any more. Only when it hasn't been seen for 30 minutes and you
confirm it is off: a computer still running would do the same work twice. Its conversations come from --from: a
plain copy of its folders (Claude Code's ~/.claude/projects folder and MetaRush's assistant/conversations folder,
with its config.yaml for the folder settings — never its keys) or a handover file it made; any without a session go
on from a summary. --plan says what it would do. Last, remove it from the workspace: metarush machine revoke.`,
		RunE: func(c *cobra.Command, args []string) error {
			if err := handoverAtTerminal(c); err != nil {
				return err
			}
			in.From = args[0]
			for _, f := range folders {
				old, to, ok := strings.Cut(f, "=")
				if !ok || old == "" || to == "" {
					return fail(c, apperr.InvalidInput, "--folder takes the folder on the other computer and the folder here, joined by =.", "")
				}
				in.Folders = append(in.Folders, handover.HandoverFolderChoice{Path: old, To: to})
			}
			for _, s := range skips {
				in.Folders = append(in.Folders, handover.HandoverFolderChoice{Path: s, Skip: true})
			}
			pass := ""
			if in.Source != "" {
				if enc, err := handover.Encrypted(in.Source); err == nil && enc {
					p, err := readPassphrase(c, fromStdin, false)
					if err != nil {
						return err
					}
					pass = p
				}
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			plan := in
			plan.Plan = true
			po, err := callHandover(c, a, "machine.takeover", plan, pass)
			if err != nil {
				return failErr(c, err)
			}
			p := po.(*handover.HandoverTakeoverOut)
			if in.Plan || !p.Allowed {
				return emit(c, p, func() { printTakeover(p) })
			}
			if !jsonMode(c) {
				printTakeover(p)
			}
			given := map[string]bool{}
			for _, f := range in.Folders {
				given[f.Path] = true
			}
			for _, f := range p.Folders {
				if given[f.Path] {
					continue
				}
				if skipAll || !isInteractive() || jsonMode(c) {
					if skipAll {
						in.Folders = append(in.Folders, handover.HandoverFolderChoice{Path: f.Path, Skip: true})
					}
					continue
				}
				what := "the watched folder"
				if f.Kind == "linked" {
					what = "the linked folder " + theirs(f.Name)
				}
				to := ask(fmt.Sprintf("Where is %s (%s there) on this computer? Press Enter to skip it", what, f.Path), "")
				if to == "" {
					in.Folders = append(in.Folders, handover.HandoverFolderChoice{Path: f.Path, Skip: true})
				} else {
					in.Folders = append(in.Folders, handover.HandoverFolderChoice{Path: f.Path, To: to})
				}
			}
			if !isInteractive() {
				return failErr(c, apperr.New(apperr.HumanOnly, "Confirming that a computer is off is a person's, at their own terminal."))
			}
			in.ConfirmedOff = ask(fmt.Sprintf("Is %s switched off for good? A computer still running would do the same work twice. Type its name to confirm", theirs(p.From.Name)), "")
			out, err := callHandover(c, a, "machine.takeover", in, pass)
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() { printTakeover(out.(*handover.HandoverTakeoverOut)) })
		}}
	f := c.Flags()
	f.StringVar(&in.Source, "from", "", "a plain copy of its folders, or a handover file it made")
	f.BoolVar(&in.Plan, "plan", false, "say what taking over would do (nothing changes)")
	f.StringArrayVar(&folders, "folder", nil, "OLD=NEW: where a folder of the gone computer is on this one")
	f.StringArrayVar(&skips, "skip-folder", nil, "OLD: leave that folder out")
	f.BoolVar(&skipAll, "skip-folders", false, "leave out every folder not given with --folder")
	f.BoolVar(&fromStdin, "passphrase-stdin", false, "read a handover file's passphrase from standard input")
	return c
}

func printTakeover(r *handover.HandoverTakeoverOut) {
	fmt.Println(r.Summary)
	if len(r.Conversations) > 0 {
		fmt.Println("\nConversations:")
		for _, c := range r.Conversations {
			fmt.Printf("  - %s: %s\n", theirs(c.Title), c.Words)
		}
	}
	if len(r.Work) > 0 && r.Handover == nil {
		fmt.Println("\nWork tied to it:")
		for _, w := range r.Work {
			why := w.Why
			if why == "" && w.Moves {
				why = "It moves here."
			}
			fmt.Printf("  - %s: %s\n", capitalize(w.Words), why)
		}
	}
	if r.Handover == nil && len(r.Folders) > 0 {
		fmt.Println("\nFolders:")
		for _, f := range r.Folders {
			fmt.Printf("  - %s\n", f.Words)
		}
	}
	printHandoverSteps(r.Steps)
}
