package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// What a project keeps (D166, docs/keeping.md):
//
//	metarush keep source <link> --project <p>          a website, video or repository → the sources
//	metarush keep logo <picture> --project <p> [--brand] [--from-site <page>]
//	metarush keep brief --project <p> [--goal …] [--audience …] [--deliverable "by email"]…
//	metarush keep rule "<rule>" --project <p> [--measure …]
//	metarush keep check "<problem>" --project <p> [--measure …]
//	metarush keep promise "<promise to the client>" --project <p> [--measure …] [--every-project]
//	metarush keep person "<name>" "<fact>"
//	metarush keep signoff "<who OKs what>" --project <p>
//	metarush keep improve "<what MetaRush couldn't do>" [--why …]
//	metarush keep spelling "<word>" --project <p> [--heard-as …]
//	metarush keep list --project <p> · metarush keep decline <item>
//
// At a terminal each keeps the thing at once (the skills ask the person
// first). In the Claude panel they are refused for Claude and become "Keep
// this?" cards the person decides on.

func keepCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "keep", Short: "Keep what a conversation brought in: sources, a logo and the brand kit, the brief, rules, promises to the client and checks, people's notes, sign-offs, ideas for MetaRush, spellings"}
	var project, why string
	addCommon := func(c *cobra.Command) {
		c.Flags().StringVar(&project, "project", "", "the project it is kept in")
		c.Flags().StringVar(&why, "why", "", "where it came from (what the person said)")
	}
	run := func(c *cobra.Command, in ops.KeepAddIn) error {
		in.ProjectID = project
		if why != "" {
			in.Detail.Why = why
		}
		return runOp(c, "keep.add", in, func(o any) {
			out := o.(*ops.KeepAddOut)
			fmt.Println(out.Summary)
			if detailsMode(c) {
				fmt.Println("kept as:", out.Item.ID)
			}
		})
	}
	source := &cobra.Command{Use: "source <link> --project <p>", Short: "Add a website, video or repository to a project's sources", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return run(c, ops.KeepAddIn{Kind: models.KeepSource, Text: args[0], Detail: models.KeepDetail{URL: args[0]}})
		}}
	addCommon(source)
	var brandKit bool
	var fromSite string
	logo := &cobra.Command{Use: "logo <picture> --project <p> [--brand] [--from-site <page>]", Aliases: []string{"picture"}, Short: "Keep a picture in the sources (with --brand: as the brand kit's logo, with its colours)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			d := models.KeepDetail{BrandKit: brandKit, FromSite: fromSite}
			if strings.Contains(args[0], "://") {
				d.URL = args[0]
			} else {
				d.ResourceID = args[0]
			}
			return run(c, ops.KeepAddIn{Kind: models.KeepPicture, Text: args[0], Detail: d})
		}}
	addCommon(logo)
	logo.Flags().BoolVar(&brandKit, "brand", false, "also the project's brand kit logo (and its colours)")
	logo.Flags().StringVar(&fromSite, "from-site", "", "use this website snapshot's brand: its logo, colours and font")
	var goal, audience, tone string
	var deliverables []string
	brief := &cobra.Command{Use: "brief --project <p> [--goal …] [--audience …] [--tone …] [--deliverable …]…", Short: "Keep goals, the audience and where the video goes in the project's brief", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			d := models.KeepDetail{Goal: goal, Audience: audience, Tone: tone}
			for _, x := range deliverables {
				d.Deliverables = append(d.Deliverables, models.Deliverable{Name: x})
			}
			return run(c, ops.KeepAddIn{Kind: models.KeepBrief, Detail: d})
		}}
	addCommon(brief)
	brief.Flags().StringVar(&goal, "goal", "", "what the video is for")
	brief.Flags().StringVar(&audience, "audience", "", "who it is for")
	brief.Flags().StringVar(&tone, "tone", "", "how it should feel")
	brief.Flags().StringArrayVar(&deliverables, "deliverable", nil, "where it goes (by email, on the website, social shorts); repeat for more")
	var measure string
	var everyProject bool
	where := func() string {
		if everyProject {
			return "settings"
		}
		return ""
	}
	rule := &cobra.Command{Use: `rule "<rule>" --project <p> [--every-project]`, Short: "Keep a style rule for the project — or with --every-project a house rule of the settings it uses (Claude follows it; the quality check measures what it can)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return run(c, ops.KeepAddIn{Kind: models.KeepRule, Text: args[0], Detail: models.KeepDetail{Measure: measure}, Where: where()})
		}}
	addCommon(rule)
	rule.Flags().StringVar(&measure, "measure", "", "what the quality check measures (default: recognized from the words)")
	rule.Flags().BoolVar(&everyProject, "every-project", false, "a house rule: every project using the same settings follows it")
	check := &cobra.Command{Use: `check "<problem>" --project <p> [--every-project]`, Short: "Check for a problem in every quality check of the project (one raised more than once) — or with --every-project of every project using its settings", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return run(c, ops.KeepAddIn{Kind: models.KeepCheck, Text: args[0], Detail: models.KeepDetail{Measure: measure}, Where: where()})
		}}
	addCommon(check)
	check.Flags().StringVar(&measure, "measure", "", "what the quality check measures (default: recognized from the words)")
	check.Flags().BoolVar(&everyProject, "every-project", false, "every project using the same settings checks for it")
	promise := &cobra.Command{Use: `promise "<promise to the client>" --project <p> [--measure …] [--every-project]`, Short: "Keep a promise made to the client (captions on every video, under 60 seconds for shorts): every quality check measures it, and a version that breaks it waits before it goes out — or with --every-project for every project using the same settings", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return run(c, ops.KeepAddIn{Kind: models.KeepPromise, Text: args[0], Detail: models.KeepDetail{Measure: measure}, Where: where()})
		}}
	addCommon(promise)
	promise.Flags().StringVar(&measure, "measure", "", "what the quality check measures (default: recognized from the words; a promise must be measurable)")
	promise.Flags().BoolVar(&everyProject, "every-project", false, "every project using the same settings makes this promise")
	person := &cobra.Command{Use: `person "<name>" "<fact>"`, Short: "Add a fact about a person to their notes in People", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			return run(c, ops.KeepAddIn{Kind: models.KeepPerson, Text: args[1], Detail: models.KeepDetail{Person: args[0]}})
		}}
	addCommon(person)
	signoff := &cobra.Command{Use: `signoff "<who OKs what>" --project <p> [--every-project]`, Short: "Add a sign-off the cut needs before it goes out — or with --every-project a house sign-off every project using the same settings needs", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return run(c, ops.KeepAddIn{Kind: models.KeepSignoff, Text: args[0], Where: where()})
		}}
	addCommon(signoff)
	signoff.Flags().BoolVar(&everyProject, "every-project", false, "a house sign-off: every project using the same settings needs it")
	improve := &cobra.Command{Use: `improve "<what MetaRush couldn't do>"`, Short: "Add something MetaRush couldn't do to its improvement list", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return run(c, ops.KeepAddIn{Kind: models.KeepImprove, Text: args[0]})
		}}
	addCommon(improve)
	var heard []string
	spelling := &cobra.Command{Use: `spelling "<word>" --project <p> [--heard-as …]`, Short: "Put how a word is written on the project's spelling list", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return run(c, ops.KeepAddIn{Kind: models.KeepSpelling, Text: args[0], Detail: models.KeepDetail{HeardAs: heard}})
		}}
	addCommon(spelling)
	spelling.Flags().StringArrayVar(&heard, "heard-as", nil, "how transcripts write it instead; repeat for more")
	var listState string
	list := &cobra.Command{Use: "list --project <p>", Short: "What the project's conversations offered to keep: asked, kept and declined", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "keep.list", ops.KeepListIn{ProjectID: project, State: listState}, func(o any) {
				out := o.(*ops.KeepListOut)
				fmt.Println(out.Summary)
				for _, it := range out.Items {
					state := map[string]string{models.KeepAsked: "waiting for you", models.KeepKept: "kept", models.KeepDeclined: "not kept"}[it.State]
					line := fmt.Sprintf("  %s: %s (%s)", keepKindWords(it.Kind), theirs(keepText(it)), state)
					if detailsMode(c) {
						line += "  · " + it.ID
					}
					fmt.Println(line)
				}
			})
		}}
	list.Flags().StringVar(&project, "project", "", "the project")
	list.Flags().StringVar(&listState, "state", "", "only asked, kept or declined")
	decline := &cobra.Command{Use: "decline <item>", Short: "Decline something offered to keep: it is never offered again", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "keep.decline", ops.KeepDeclineIn{Item: args[0]}, func(o any) { fmt.Println(o.(*ops.KeepAddOut).Summary) })
		}}
	cmd.AddCommand(source, logo, brief, rule, promise, check, person, signoff, improve, spelling, list, decline)
	return cmd
}

// deliverableShapeWords says a deliverable's shape in plain words.
func deliverableShapeWords(preset string) string {
	switch {
	case strings.HasPrefix(preset, "vertical"):
		return "vertical"
	case strings.HasPrefix(preset, "square"):
		return "square"
	}
	return "widescreen"
}

// keepKindWords says a kind of kept thing in plain words.
func keepKindWords(kind string) string {
	return map[string]string{models.KeepSource: "source", models.KeepPicture: "picture", models.KeepBrief: "brief", models.KeepRule: "rule",
		models.KeepPerson: "note about a person", models.KeepSignoff: "sign-off", models.KeepCheck: "check", models.KeepImprove: "idea for MetaRush",
		models.KeepSpelling: "spelling", models.KeepPromise: "promise to the client"}[kind]
}

func keepText(it models.KeepItem) string {
	if it.Text != "" {
		return it.Text
	}
	if it.Detail.Goal != "" {
		return it.Detail.Goal
	}
	if it.Detail.Audience != "" {
		return it.Detail.Audience
	}
	var ds []string
	for _, d := range it.Detail.Deliverables {
		ds = append(ds, d.Name)
	}
	return strings.Join(ds, ", ")
}

// cutSignoffsCmd is `metarush cut signoffs`: the OKs a cut needs before it goes out.
func cutSignoffsCmd() *cobra.Command {
	var branch string
	show := func(c *cobra.Command, o *ops.SignoffsOut) {
		fmt.Println(o.Summary)
		for i, s := range o.Signoffs {
			mark := "[ ]"
			if s.Done {
				mark = "[x]"
			}
			line := fmt.Sprintf("  %d. %s %s", i+1, mark, theirs(s.Text))
			if s.Done && s.DoneBy != nil {
				line += " · " + s.DoneBy.UserName
				if s.Version > 0 {
					line += fmt.Sprintf(", version %d", s.Version)
				}
			}
			fmt.Println(line)
		}
	}
	cmd := &cobra.Command{Use: "signoffs <project>", Short: "The OKs a cut needs before it goes out (a reminder before approving, exporting and delivering)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "cut.signoffs.list", ops.SignoffsIn{ProjectID: args[0], Branch: branch}, func(o any) { show(c, o.(*ops.SignoffsOut)) })
		}}
	cmd.Flags().StringVar(&branch, "branch", "", "branch (default: main)")
	add := &cobra.Command{Use: `add <project> "<who OKs what>"`, Short: "Add a sign-off (Lena OKs the final version)", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "cut.signoffs.add", ops.SignoffAddIn{SignoffsIn: ops.SignoffsIn{ProjectID: args[0], Branch: branch}, Text: args[1]}, func(o any) { show(c, o.(*ops.SignoffsOut)) })
		}}
	var note string
	verb := func(use, op, short string) *cobra.Command {
		x := &cobra.Command{Use: use + " <project> <sign-off>", Short: short, Args: cobra.ExactArgs(2),
			RunE: func(c *cobra.Command, args []string) error {
				a, err := openApp(c, "cli")
				if err != nil {
					return err
				}
				defer a.Close()
				// While versions wait for sign-offs (D299), ticking or removing one is a person's: at their own terminal.
				return runOpWith(c, a, op, ops.SignoffDecideIn{SignoffsIn: ops.SignoffsIn{ProjectID: args[0], Branch: branch}, Signoff: args[1], Note: note}, ops.Caller{Human: interactive()}, func(o any) { show(c, o.(*ops.SignoffsOut)) })
			}}
		x.Flags().StringVar(&branch, "branch", "", "branch (default: main)")
		return x
	}
	done := verb("done", "cut.signoffs.done", "Tick a sign-off: that OK was given")
	done.Flags().StringVar(&note, "note", "", "a note (how they said it)")
	add.Flags().StringVar(&branch, "branch", "", "branch (default: main)")
	list := &cobra.Command{Use: "list <project>", Short: "List the sign-offs a cut needs", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "cut.signoffs.list", ops.SignoffsIn{ProjectID: args[0], Branch: branch}, func(o any) { show(c, o.(*ops.SignoffsOut)) })
		}}
	list.Flags().StringVar(&branch, "branch", "", "branch (default: main)")
	cmd.AddCommand(list, add, done, verb("undo", "cut.signoffs.undo", "Untick a sign-off: the OK is needed again"), verb("remove", "cut.signoffs.remove", "Take a sign-off off the checklist"))
	return cmd
}
