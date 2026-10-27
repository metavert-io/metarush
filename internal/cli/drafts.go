package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// Drafts from AI apps and the review desk (D248–D250): an AI app that may only
// propose saves its changes as drafts (`metarush draft save`, or any change on
// a propose-only connection: --propose, METARUSH_PROPOSE), a person decides
// about them (`draft approve|not-quite|turn-down`, people only), and
// `revision packet` is the one page to hand a draft or version to someone.

// agentFlags are who is acting and whether it may only propose: --agent and
// --propose, or METARUSH_AGENT and METARUSH_PROPOSE (an AI app's settings can
// set them for every command it runs).
func agentFlags(c *cobra.Command) (agent string, propose bool) {
	if c != nil {
		agent, _ = c.Flags().GetString("agent")
		propose, _ = c.Flags().GetBool("propose")
	}
	if agent == "" {
		agent = os.Getenv("METARUSH_AGENT")
	}
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("METARUSH_PROPOSE"))); v {
	case "", "0", "false", "no", "off":
	default:
		propose = true
	}
	return models.AgentSlug(agent), propose
}

func draftCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "draft", Short: "Drafts AI apps proposed: save one (what an app that may only propose does instead of a change), list them, and decide (people only)"}
	cmd.AddCommand(draftSaveCmd(), draftListCmd(), draftDecideCmd("approve"), draftDecideCmd("not-quite"), draftDecideCmd("turn-down"))
	return cmd
}

func draftSaveCmd() *cobra.Command {
	var input, sheet, plan, note, replaces string
	cmd := &cobra.Command{
		Use:   "save [operation] [--input <json|@file|->] [--sheet <file>] [--plan <file>] --note <why>",
		Short: "Propose a change without making it: a new version (a cut sheet, an edit plan, a cut verb) kept beside the cut, or another change kept until a person approves it",
		Example: "  metarush draft save --sheet intro.cut.md --note \"The intro dragged; I took out the first sentence.\"\n" +
			"  metarush draft save content.rules.add --input '{\"text\": \"The name bar never animates\"}' --note \"You asked for still name bars twice.\"\n" +
			"  metarush draft save cut.trim --input '{\"project\": \"Garden story\", \"clip\": \"intro\", \"end\": \"0:12\"}'",
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in := ops.DraftSaveIn{Note: note, Replaces: replaces}
			if len(args) == 1 {
				in.Op = args[0]
			}
			fields := map[string]any{}
			if input != "" {
				raw, err := readDraftInput(input)
				if err != nil {
					return fail(c, apperr.InvalidInput, err.Error(), "Give --input inline, as @file, or as - to read it from standard input.")
				}
				if err := json.Unmarshal(raw, &fields); err != nil {
					return fail(c, apperr.InvalidInput, "--input isn't an object with the change's fields.", "Its fields are the ones the change's description lists (describe shows them).")
				}
			}
			switch {
			case sheet != "":
				s, err := readSheet(sheet)
				if err != nil {
					return fail(c, apperr.InvalidInput, err.Error(), "")
				}
				in.Op, fields["sheet"] = firstOf(in.Op, "cut.apply"), s
			case plan != "":
				src := "@" + strings.TrimPrefix(plan, "@")
				if plan == "-" {
					src = "-"
				}
				raw, err := readDraftInput(src)
				if err != nil {
					return fail(c, apperr.InvalidInput, err.Error(), "")
				}
				in.Op, fields["plan"] = firstOf(in.Op, "edit.apply"), json.RawMessage(raw)
			}
			if in.Op == "" {
				return fail(c, apperr.InvalidInput, "Say which change to propose: its name, --sheet or --plan.", "Find names the change for what it does.")
			}
			in.Input, _ = json.Marshal(fields)
			return runOp(c, "draft.save", in, func(o any) {
				v := o.(*ops.DraftView)
				printDraft(v, detailsMode(c))
				fmt.Println("Saved as a draft: nothing changes until a person approves it.")
			})
		},
	}
	cmd.Flags().StringVar(&input, "input", "", "the change's input as JSON, @file or - (as `metarush ops describe <operation>` shows it)")
	cmd.Flags().StringVar(&sheet, "sheet", "", "a cut sheet file (or -): a new version of its cut")
	cmd.Flags().StringVar(&plan, "plan", "", "an edit plan file (or -): a new version")
	cmd.Flags().StringVar(&note, "note", "", "why, in a sentence or two: the person reads it beside the change")
	cmd.Flags().StringVar(&replaces, "replaces", "", "the draft this one takes the place of (after a person said Not quite)")
	return cmd
}

// readDraftInput reads JSON given inline, as @file or as - (standard input).
func readDraftInput(v string) ([]byte, error) {
	switch {
	case v == "-":
		return io.ReadAll(io.LimitReader(os.Stdin, 8<<20))
	case strings.HasPrefix(v, "@"):
		p := strings.TrimPrefix(v, "@")
		if err := assistantPath(p); err != nil {
			return nil, err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("can't read that file: %w", err)
		}
		return b, nil
	}
	return []byte(v), nil
}

func draftListCmd() *cobra.Command {
	var in ops.DraftListIn
	cmd := &cobra.Command{
		Use:   "list [--by <app>|--all] [--state waiting|approved|not_quite|turned_down|replaced] [--project <project>]",
		Short: "Drafts AI apps proposed, newest first, with what a person decided (an app sees its own unless it asks for all)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "draft.list", in, func(o any) {
				ds := o.(*ops.DraftListOut).Drafts
				if len(ds) == 0 {
					fmt.Println("No drafts.")
					return
				}
				for i := range ds {
					printDraft(&ds[i], detailsMode(c))
				}
			})
		},
	}
	cmd.Flags().StringVar(&in.Agent, "by", "", "only this AI app's drafts (codex, cursor…)")
	cmd.Flags().BoolVar(&in.All, "all", false, "every app's drafts")
	cmd.Flags().StringVar(&in.State, "state", "", "only drafts in this state")
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "only this project's (its ID or name)")
	cmd.Flags().IntVar(&in.Limit, "limit", 0, "at most this many (default 50)")
	return cmd
}

// draftDecideCmd is approve, not-quite or turn-down: a person's decision, in
// their own terminal (or the dashboard).
func draftDecideCmd(verb string) *cobra.Command {
	var note string
	short := map[string]string{
		"approve":   "Approve an AI app's draft (people only): a version goes into the cut, a change is made as you",
		"not-quite": "Say what should change in an AI app's draft (people only): the note goes back to the app for its next draft",
		"turn-down": "Turn down an AI app's draft (people only)",
	}[verb]
	decision := map[string]string{"approve": ops.DecideApprove, "not-quite": ops.DecideNotQuite, "turn-down": ops.DecideTurnDown}[verb]
	cmd := &cobra.Command{
		Use: verb + " <draft> [--note <words>]", Short: short, Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if !interactive() {
				return fail(c, apperr.HumanOnly, "Only a person decides about a draft.", "Decide in the dashboard (Awaiting your review), or run this yourself in a terminal.")
			}
			if decision == ops.DecideNotQuite && strings.TrimSpace(note) == "" {
				return fail(c, apperr.InvalidInput, "Say what should change.", "Add --note with what the app should do differently.")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			if decision != ops.DecideNotQuite {
				word := strings.ReplaceAll(verb, "-", " ")
				if ask(fmt.Sprintf("Type %q to %s this draft", word, word), "") != word {
					return fail(c, apperr.Canceled, "Nothing changed.", "")
				}
			}
			return runOpWith(c, a, "draft.decide", ops.DraftDecideIn{ID: args[0], Decision: decision, Note: note}, ops.Caller{Human: true}, func(o any) {
				v := o.(*ops.DraftView)
				switch v.State {
				case models.DraftApproved:
					if v.Decision != nil && v.Decision.Result != "" {
						fmt.Println(v.Decision.Result)
					} else {
						fmt.Println("Approved.")
					}
				case models.DraftNotQuite:
					fmt.Printf("%s hears what should change when it next looks at its drafts.\n", v.AgentName)
				case models.DraftTurnedDown:
					fmt.Println("Turned down.")
				case models.DraftInCut:
					if v.Decision != nil && v.Decision.Result != "" {
						fmt.Println(v.Decision.Result)
					}
				}
				if detailsMode(c) {
					fmt.Println("draft:", v.ID)
				}
			})
		},
	}
	cmd.Flags().StringVar(&note, "note", "", "what should change, or why (kept with the decision; the app reads it)")
	return cmd
}

// printDraft prints one draft for people.
func printDraft(v *ops.DraftView, details bool) {
	where := ""
	if v.ProjectName != "" {
		where = " — " + theirs(v.ProjectName)
	}
	what := v.Title
	if v.Kind == models.DraftVersion && v.Revision != nil {
		what = fmt.Sprintf("version %d: %s", v.Revision.Number, lowerFirst(v.Title))
	}
	fmt.Printf("%s's draft%s, %s: %s (%s).\n", v.AgentName, where, plainWhen(v.CreatedAt), what, draftStateLine(v))
	for _, ch := range v.Changes {
		fmt.Println("  - " + ch)
	}
	if v.Note != "" {
		fmt.Println("  Why: " + theirs(v.Note))
	}
	if v.Decision != nil && v.Decision.Note != "" {
		fmt.Printf("  %s said: %s\n", orSomeone(v.Decision.By.UserName), theirs(v.Decision.Note))
	}
	if v.Tried != nil {
		fmt.Println("  Approving it didn't work: " + v.Tried.Message)
	}
	if v.ReviewCopy != nil {
		fmt.Println("  Review copy: " + lowerFirst(v.ReviewCopy.Said) + ".")
	}
	if details {
		fmt.Println("  draft:", v.ID)
		if v.Revision != nil {
			fmt.Println("  version:", v.Revision.ID)
		}
		fmt.Println("  " + v.URL)
	}
}

func draftStateLine(v *ops.DraftView) string {
	switch v.State {
	case models.DraftWaiting:
		if v.Kind == models.DraftVersion {
			return "waiting for review, not in the cut yet"
		}
		return "waiting for review"
	case models.DraftApproved:
		return "approved"
	case models.DraftNotQuite:
		return "not quite: a person said what should change"
	case models.DraftTurnedDown:
		return "turned down"
	case models.DraftReplaced:
		return "a newer draft took its place"
	case models.DraftApproving:
		return "being approved"
	case models.DraftInCut:
		return "its change is in the cut"
	}
	return v.State
}

// revisionPacketCmd is `metarush revision packet <draft or version>` (D250).
func revisionPacketCmd() *cobra.Command {
	var markdown bool
	var out string
	cmd := &cobra.Command{
		Use:   "packet <draft or version> [--markdown] [--out <file>]",
		Short: "One page for a draft or version, to hand to a client or colleague: what changed and why, the checks, the cost, who made it and what is still waiting",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in := ops.PacketIn{ID: args[0], Format: "markdown"}
			return runOp(c, "review.packet", in, func(o any) {
				pk := o.(*ops.Packet)
				if out != "" {
					if err := os.WriteFile(out, []byte(pk.Markdown), 0o644); err != nil {
						fmt.Fprintln(os.Stderr, "Couldn't write the page to that file.")
						return
					}
					fmt.Println("Wrote the page as Markdown.")
					return
				}
				if markdown {
					fmt.Print(pk.Markdown)
					return
				}
				printPacket(pk, detailsMode(c))
			})
		},
	}
	cmd.Flags().BoolVar(&markdown, "markdown", false, "print the page as Markdown")
	cmd.Flags().StringVar(&out, "out", "", "write the page as Markdown to this file")
	return cmd
}

func printPacket(pk *ops.Packet, details bool) {
	fmt.Println(pk.Title)
	fmt.Printf("%s. Made by %s, %s.\n", pk.Status, pk.Who.Made, plainWhen(pk.Who.At))
	fmt.Println("What changes:")
	for _, ch := range pk.Changes {
		fmt.Println("  - " + ch)
	}
	for _, w := range pk.Why {
		fmt.Println("Why: " + theirs(w))
	}
	fmt.Println("Checks: " + pk.Checks.Said)
	for _, p := range pk.Checks.Problems {
		fmt.Println("  - problem: " + p)
	}
	for _, w := range append(append([]string{}, pk.Checks.Warnings...), pk.Checks.Worth...) {
		fmt.Println("  - worth checking: " + w)
	}
	fmt.Println("Cost: " + pk.Receipt)
	if len(pk.Waiting) > 0 {
		fmt.Println("Waiting:")
		for _, w := range pk.Waiting {
			fmt.Println("  - " + w)
		}
	}
	if pk.Decision != "" {
		fmt.Println("Decision: " + pk.Decision)
	}
	fmt.Println(pk.URL)
	if details {
		fmt.Println("id:", pk.ID)
	}
}
