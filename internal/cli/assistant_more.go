package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// The project so far, a person's Auto mode and where a turned-down note
// went (D185, D186).

// projectSoFarCmd prints what happened in a project so far, newest first.
func projectSoFarCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "so-far <project>",
		Short: "What happened in a project so far, newest first: versions, renders, Claude Code conversations and what was decided on their cards, open questions",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "project.sofar", assistant.ProjectSoFarIn{ProjectID: args[0]}, func(o any) { printSoFar(o.(*assistant.ProjectSoFarOut), detailsMode(c)) })
		},
	}
}

// printSoFar prints the record in plain words (IDs with --details).
func printSoFar(o *assistant.ProjectSoFarOut, details bool) {
	fmt.Printf("%s so far\n", theirs(o.ProjectName))
	when := func(t time.Time) string { return t.Local().Format("Jan 2, 3:04 PM") }
	if len(o.Versions) == 0 && len(o.Renders) == 0 && len(o.Conversations) == 0 && len(o.Todos) == 0 {
		fmt.Println("Nothing yet: no versions, renders or conversations.")
		return
	}
	if len(o.Versions) > 0 {
		fmt.Println("\nVersions")
		for _, v := range o.Versions {
			l := fmt.Sprintf("  version %d · %s", v.Number, when(v.At))
			if v.By != "" {
				l += " · " + v.By
			}
			if v.FromClaude {
				l += " (Claude's change)"
			}
			if v.Latest {
				l += " · the latest"
			}
			switch v.Approval {
			case models.ApprovalApproved:
				l += " · approved"
			case models.ApprovalRejected:
				l += " · needs changes"
			}
			fmt.Println(l)
			if v.Note != "" {
				fmt.Println("    " + theirs(v.Note))
			}
			if v.ApprovalNote != "" {
				fmt.Println("    What should change: " + theirs(v.ApprovalNote))
			}
			if details {
				fmt.Println("    version:", v.RevisionID)
			}
		}
	}
	if len(o.Renders) > 0 {
		fmt.Println("\nRenders")
		for _, r := range o.Renders {
			what := "Review copy"
			if r.Quality == "final" {
				what = "Final render"
			}
			l := fmt.Sprintf("  %s of version %d · %s", what, r.RevisionNumber, when(r.At))
			if r.QA != "" {
				l += " · " + r.QA
			}
			fmt.Println(l)
			if details {
				fmt.Println("    render:", r.RenderID)
			}
		}
	}
	if len(o.Conversations) > 0 {
		fmt.Println("\nEarlier conversations with Claude Code")
		for _, cv := range o.Conversations {
			l := "  " + theirs(cv.Title)
			if cv.StartedBy != "" {
				l += " · started by " + cv.StartedBy
			}
			fmt.Println(l)
			for i, k := range cv.Cards {
				if i == 6 {
					fmt.Printf("    and %d older\n", len(cv.Cards)-6)
					break
				}
				what := map[string]string{"done": "Done", "not_now": "Not now", "not_quite": "Not quite", "failed": "Didn't work", "waiting": "Waiting"}[k.State]
				s := "    " + what + ": " + k.Title
				if k.Note != "" {
					s += " — " + theirs(k.Note)
				}
				if k.By != "" && k.State != "waiting" {
					s += " (" + k.By + ")"
				}
				fmt.Println(s)
			}
			if details {
				fmt.Println("    conversation:", cv.ID)
			}
		}
	}
	if len(o.Todos) > 0 {
		fmt.Println("\nOpen questions")
		for _, t := range o.Todos {
			fmt.Println("  " + t.Question)
		}
	}
	if o.More {
		fmt.Println("\nOlder things were left out to keep it short (--json has everything listed).")
	}
}

// assistantModeCmd shows or chooses the person's mode: ask first or Auto.
func assistantModeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mode [ask|auto]",
		Short: "Whether Claude's changes for you wait for your OK (ask, the default) or run by themselves when they can be undone (auto); choosing is people only",
		Example: "  metarush assistant mode          # which one you use\n" +
			"  metarush assistant mode auto     # changes that can be undone run by themselves (not money, final renders, approvals, deleting)",
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			print := func(o any) {
				m := o.(*assistant.AssistantModeOut)
				fmt.Println(m.Summary)
			}
			if len(args) == 0 {
				return runOp(c, "assistant.mode", assistant.AssistantModeIn{}, print)
			}
			mode := strings.ToLower(args[0])
			if mode != assistant.ModeAsk && mode != assistant.ModeAuto {
				return fail(c, apperr.InvalidInput, "choose ask or auto", "")
			}
			if !interactive() {
				return fail(c, apperr.HumanOnly, "choosing how Claude's changes run is for people", "run this command yourself in a terminal, or use the switch in the dashboard's Claude Code panel")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "assistant.mode.set", assistant.AssistantModeSetIn{Mode: mode}, ops.Caller{Human: true}, print)
		},
	}
	return forOps(cmd, "assistant.mode", "assistant.mode.set")
}

// printToldClaude says where a turned-down note went (D186).
func printToldClaude(sent *ops.NoteSent, note string, details bool) {
	if strings.TrimSpace(note) == "" {
		return
	}
	switch {
	case sent == nil:
		fmt.Println("No Claude Code conversation made it, so your note stays with the decision. To have Claude change it, start a conversation in the project's Claude Code panel.")
	case sent.Machine != "":
		fmt.Printf("Your note is on its way to Claude in %s: MetaRush running on %s sends it.\n", theirs(sent.Title), sent.Machine)
	case sent.Queued:
		fmt.Printf("Your note went to Claude in %s; it reads it once it finishes what it is doing.\n", theirs(sent.Title))
	default:
		fmt.Printf("Your note went to Claude in %s.\n", theirs(sent.Title))
	}
	if sent != nil && details {
		fmt.Println("conversation:", sent.ConversationID)
	}
}

// assistantSpendingCmd says when Auto spends money by itself (D241): with
// each service, whether a person set the project's budget and the team's
// daily limit, and what Auto spent for you today.
func assistantSpendingCmd() *cobra.Command {
	var in assistant.AssistantSpendingIn
	cmd := &cobra.Command{
		Use:   "spending [--project <project>]",
		Short: "When Auto spends money by itself: the budgets and daily limits a person set with each service, and what it spent for you today",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "assistant.spending", in, func(o any) {
				out := o.(*assistant.AssistantSpendingOut)
				fmt.Println(out.Summary)
				for _, s := range out.Services {
					line := "  " + s.Service + ": "
					parts := []string{}
					if s.Project.Words != "" {
						p := s.Project.Words
						if s.Project.By != "" {
							p += " (set by " + s.Project.By + ")"
						}
						parts = append(parts, p)
					}
					if s.Daily.Exists && s.Daily.Words != "" {
						d := s.Daily.Words
						switch {
						case s.Daily.Default:
							d += " (the default)"
						case s.Daily.By != "":
							d += " (set by " + s.Daily.By + ")"
						}
						parts = append(parts, d)
					}
					line += strings.Join(parts, "; ")
					if s.Why != "" {
						line += " — Auto asks first: " + s.Why
					}
					fmt.Println(line)
				}
				for _, r := range out.Today {
					fmt.Printf("  today: %s (%s)\n", theirs(r.What), moneyLine(r.USD, "estimate"))
				}
			})
		},
	}
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "the project whose budgets to look at")
	return forOps(cmd, "assistant.spending")
}
