package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/todos"
)

// todosCmd is `metarush todos …`: questions MetaRush needs a person to answer.
func todosCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "todos", Short: "Questions MetaRush needs you to answer (who is speaking, music rights, the project brief…)"}
	var li ops.TodoListIn
	var details bool
	list := &cobra.Command{
		Use: "list", Short: "Open questions (filter with --project, --asset, --kind)",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "todos.list", li, func(o any) { printTodos(o.(*ops.TodoListOut), details) })
		},
	}
	addDetails(list, &details)
	list.Flags().StringVar(&li.ProjectID, "project", "", "only this project")
	list.Flags().StringVar(&li.AssetID, "asset", "", "only this video")
	list.Flags().StringVar(&li.Kind, "kind", "", "speaker_unnamed | speaker_match | music_rights | brief_missing | duplicate_person | revision_approval | face_group | face_person | remotion_approval | recipe_run")
	list.Flags().StringVar(&li.State, "state", "", "open (default), answered, dismissed or all")
	list.Flags().Int64Var(&li.Limit, "limit", 0, "max questions")
	cmd.AddCommand(list)
	show := &cobra.Command{
		Use: "show <todo-id>", Short: "One question: what to listen to or look at, and how to answer", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "todos.show", ops.TodoIn{ID: args[0]}, func(o any) { printTodo(o.(*ops.TodoShowOut), details) })
		},
	}
	addDetails(show, &details)
	cmd.AddCommand(show)
	var suggestion int
	var samePerson, newPerson bool
	var sameAs string
	answer := &cobra.Command{
		Use: `answer <todo-id> ["<answer>"] [--suggestion N] [--same-as <todo-id>]`, Short: "Answer a question (it is applied right away: names the voice or the faces, saves the rights…)", Args: cobra.RangeArgs(1, 2),
		Long: "For \"who is this?\" questions the answer is a name: someone already named (case and accents ignored), or someone new.\n" +
			"A name very close to someone's is refused (use theirs, or --new-person); naming a voice like another voice in the same clip asks first\n" +
			"(or pass --same-person). A question about a group of faces is answered once for all of them; --same-as joins it with another\n" +
			"face question of the same clip (they are one person).",
		RunE: func(c *cobra.Command, args []string) error {
			in := ops.TodoAnswerIn{ID: args[0], Suggestion: suggestion, SamePerson: samePerson, NewPerson: newPerson, SameAs: sameAs}
			if len(args) == 2 {
				in.Answer = args[1]
			}
			if in.Answer == "" && in.Suggestion == 0 && in.SameAs == "" {
				return failErr(c, apperr.New(apperr.InvalidInput, "Give an answer, or pick one with --suggestion N.").
					WithHint("The question lists its choices on the To-dos page.").WithCommand("metarush todos show "+args[0]))
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			// Approving a cut is for people: allowed from an interactive terminal only.
			caller := ops.Caller{Human: interactive()}
			out, err := ops.CallTyped[ops.TodoAnswerOut](c.Context(), a, "todos.answer", in, caller)
			if apperr.Is(err, apperr.ConfirmRequired) && !in.SamePerson && interactive() && !jsonMode(c) {
				if !askYes(apperr.From(err).Message, false) {
					fmt.Println("Nothing changed. Answer with another name, or say they are one person with --same-person.")
					return nil
				}
				in.SamePerson = true
				out, err = ops.CallTyped[ops.TodoAnswerOut](c.Context(), a, "todos.answer", in, caller)
			}
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() { fmt.Println(out.Done) })
		},
	}
	answer.Flags().IntVar(&suggestion, "suggestion", 0, "pick suggested answer N (1 = first)")
	answer.Flags().BoolVar(&samePerson, "same-person", false, "confirm that two voices of the clip are one person (or that a different-sounding voice is them)")
	answer.Flags().BoolVar(&newPerson, "new-person", false, "the name is someone new (even if it is close to someone's)")
	answer.Flags().StringVar(&sameAs, "same-as", "", "a question about faces: the same person as this other face question of the clip")
	cmd.AddCommand(answer)
	var gi ops.TodoGroupsIn
	groups := &cobra.Command{
		Use: "groups [--project <id>] [--asset <id>]", Short: "Who is on screen: one question per person per clip, with how often they are seen and why a name is suggested",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "todos.groups", gi, func(o any) { printFaceGroups(o.(*ops.TodoGroupsOut), detailsMode(c)) })
		},
	}
	groups.Flags().StringVar(&gi.ProjectID, "project", "", "only this project")
	groups.Flags().StringVar(&gi.AssetID, "asset", "", "only this video")
	cmd.AddCommand(groups)
	cmd.AddCommand(&cobra.Command{
		Use: "accept-shown <asset-id>", Short: "Use the names a clip shows (a call app's name under each picture, a lower third) for the people it asks about", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "todos.acceptShown", ops.TodoAcceptShownIn{AssetID: args[0]}, func(o any) {
				out := o.(*ops.TodoAcceptShownOut)
				fmt.Println(out.Summary)
				for _, x := range out.Accepted {
					fmt.Println("  " + x.Done)
				}
				for _, f := range out.Failed {
					fmt.Println("  Not answered: " + f)
				}
			})
		},
	})
	var note string
	dismiss := &cobra.Command{
		Use: "dismiss <todo-id>", Short: "Skip a question (it won't be asked again)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "todos.dismiss", ops.TodoDismissIn{ID: args[0], Note: note}, func(o any) { fmt.Println(o.(*ops.TodoAnswerOut).Done) })
		},
	}
	dismiss.Flags().StringVar(&note, "note", "", "why (optional)")
	cmd.AddCommand(dismiss)
	cmd.AddCommand(todosNotCmd(), todosSkipFacesCmd())
	cmd.AddCommand(&cobra.Command{
		Use: "count", Short: "How many questions are open, per project and video",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "todos.counts", ops.TodoCountsIn{}, func(o any) {
				n := o.(*todos.Counts)
				switch n.Total {
				case 0:
					fmt.Println("Nothing to answer right now.")
				case 1:
					fmt.Println("1 question waiting for you.")
				default:
					fmt.Printf("%d questions waiting for you.\n", n.Total)
				}
			})
		},
	})
	var sy ops.TodoSyncIn
	sync := &cobra.Command{
		Use: "sync [--project <id>] [--asset <id>]", Short: "Re-check what needs asking (and close questions that no longer apply)",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "todos.sync", sy, func(o any) { fmt.Println(o.(*ops.TodoSyncOut).Summary) })
		},
	}
	sync.Flags().StringVar(&sy.ProjectID, "project", "", "only this project")
	sync.Flags().StringVar(&sy.AssetID, "asset", "", "only this video")
	cmd.AddCommand(sync)
	return cmd
}

// todoWhere is the clip (by its title) or project a question is about.
func todoWhere(t models.Todo) string {
	if w := orWord(t.Context.AssetTitle, t.Context.AssetName); w != "" {
		return w
	}
	return t.Context.ProjectName
}

// printTodos lists open questions in plain words; details adds each one's ID
// and the command that shows it.
func printTodos(out *ops.TodoListOut, details bool) {
	fmt.Println(out.Summary)
	for i, t := range out.Todos {
		where := todoWhere(t)
		fmt.Printf("%2d. %s", i+1, t.Question)
		if where != "" && !strings.Contains(t.Question, where) {
			fmt.Printf("  (%s)", where)
		}
		fmt.Println()
		if t.Kind == models.TodoFaceGroup {
			fmt.Printf("    %s\n", seenLine(t))
		}
		if len(t.Suggestions) > 0 {
			labels := make([]string, len(t.Suggestions))
			for k, s := range t.Suggestions {
				labels[k] = s.Label
			}
			fmt.Printf("    answers: %s\n", strings.Join(labels, " · "))
		}
		if details {
			fmt.Printf("    metarush todos show %s\n", t.ID)
		}
	}
	if len(out.Todos) > 0 && !details {
		fmt.Println("Answer them on the To-dos page in the dashboard.")
	}
}

// printFaceGroups lists who is on screen (one question per person per clip):
// the question, how often they are seen, why a name is suggested, the answers;
// details adds each question's ID, its sample faces and the commands.
func printFaceGroups(out *ops.TodoGroupsOut, details bool) {
	fmt.Println(out.Summary)
	for i, t := range out.Groups {
		fmt.Printf("%2d. %s  (%s)\n", i+1, t.Question, theirs(todoWhere(t)))
		fmt.Printf("    %s\n", seenLine(t))
		for _, ev := range t.Context.Evidence {
			fmt.Printf("    · %s\n", ev.Text)
		}
		if len(t.Suggestions) > 0 {
			labels := make([]string, len(t.Suggestions))
			for k, s := range t.Suggestions {
				labels[k] = s.Label
			}
			fmt.Printf("    answers: %s\n", strings.Join(labels, " · "))
		}
		if details {
			fmt.Printf("    metarush todos answer %s --suggestion 1   (or a name, or --same-as <question>)\n", t.ID)
			for _, f := range t.Context.Faces {
				fmt.Printf("    face %s at %s\n", f.FaceID, fmtDur(f.At))
			}
		}
	}
	for _, s := range out.Shown {
		fmt.Println(s.Summary)
		if details {
			fmt.Printf("  metarush todos accept-shown %s\n", s.AssetID)
		}
	}
	if len(out.Groups) > 0 && !details {
		fmt.Println("Answer them on the To-dos page in the dashboard (one answer names all of a person's faces).")
	}
}

// seenLine says how often a group of faces is seen, and whether a cut uses them.
func seenLine(t models.Todo) string {
	n := t.Context.Appearances
	s := fmt.Sprintf("seen %d times", n)
	switch n {
	case 1:
		s = "seen once"
	case 2:
		s = "seen twice"
	}
	s += fmt.Sprintf(", %s on screen", plainSeconds(t.Context.OnScreen))
	if t.Context.InCut {
		s += ", in your cut"
	}
	return s
}

// printTodo shows one question: what to listen to and how to answer; details
// adds the commands that save the clips and answer it.
func printTodo(out *ops.TodoShowOut, details bool) {
	t := out.Todo
	fmt.Println(t.Question)
	if t.Detail != "" {
		fmt.Println(t.Detail)
	}
	for _, s := range t.Context.Samples {
		fmt.Printf("  listen: %s to %s in “%s”\n", fmtDur(s.From), fmtDur(s.To), todoWhere(t))
	}
	if details && len(t.Context.Samples) > 0 {
		fmt.Printf("  (save the clips: metarush footage speakers %s --samples)\n", t.AssetID)
	}
	if t.State != models.TodoOpen {
		fmt.Printf("Already %s: %s\n", t.State, t.Answer)
		return
	}
	fmt.Println(out.HowToAnswer)
	if !details {
		fmt.Println("Answer it on the To-dos page in the dashboard.")
		return
	}
	if len(t.Suggestions) > 0 {
		fmt.Printf("  metarush todos answer %s --suggestion 1\n", t.ID)
	}
	if t.FreeText {
		fmt.Printf("  metarush todos answer %s \"your answer\"\n", t.ID)
	}
}
