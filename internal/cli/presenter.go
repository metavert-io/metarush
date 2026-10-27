package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

func presenterCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "presenter", Aliases: []string{"presenters"}, Short: "Presenters: a person's own presenter at an AI service reads your lines on camera — with their recorded consent, priced first",
		Long: `A presenter reads lines on camera from MetaRush's own sound (the person's voice copy, a recording of
them, an AI voice). A person's own presenter is made at an AI service from their approved look, a
training take of them and their consent recorded on camera, naming that service; stock presenters
are offered only where the service's licence covers commercial use and client work. Beginning,
making, testing, approving and retiring a presenter are people's steps. Retiring one (or
withdrawing the consent) deletes it at the service and lists the cuts that use its clips.

  metarush presenter start --person "Ada Lovelace" --green-screen          (asks the service for the words Ada reads)
  metarush consent record --person "Ada Lovelace" --self --recording <clip> --adult --covers presenters --service d-id --statement "…"
  metarush presenter make ada --take <clip> --reading <clip> --reading-from 31 --reading-to 39
  metarush presenter test ada --clip <clip> --max-cost 0.60
  metarush presenter approve ada
  metarush ai make presenter --presenter ada --line 3 --project <id>
  metarush presenter retire ada --note "She changed her mind."
  metarush presenter stock · metarush presenter opt-out <service>`}
	cmd.AddCommand(presenterListCmd(), presenterShowCmd(), presenterStartCmd(), presenterMakeCmd(), presenterTestCmd(), presenterDecideCmd(true),
		presenterDecideCmd(false), presenterStockCmd(), presenterOptOutCmd())
	return cmd
}

func printPresenter(v *aiwork.PresenterView, details bool) {
	fmt.Println(v.Summary)
	if v.Words != "" {
		fmt.Printf("  %s's words to read: %s", v.ProviderTitle, theirs(v.Words))
		if v.WordsUntil != nil {
			fmt.Printf(" (until %s)", v.WordsUntil.Local().Format("15:04"))
		}
		fmt.Println()
	}
	if v.Next != "" {
		fmt.Println("  next: " + v.Next)
	}
	if v.TakeAdvice != "" {
		fmt.Println("  the training take: " + v.TakeAdvice)
	}
	if v.ConsentSummary != "" {
		fmt.Println("  " + v.ConsentSummary)
	}
	if v.Clips > 0 {
		fmt.Printf("  %d clip(s) read with it\n", v.Clips)
	}
	for _, u := range v.UsedIn {
		fmt.Println("  used in " + u.Text)
	}
	if v.Erasure != "" {
		fmt.Println("  " + v.Erasure)
	}
	if !details {
		return
	}
	fmt.Printf("    presenter %s (%s), %s/%s, at the service %s\n", v.ID, v.Name, v.Provider, v.Model, v.ProviderID)
	fmt.Printf("    look %s@%s, consent %s, green screen %v\n", v.LookName, v.LookVersion, v.ConsentID, v.GreenScreen)
	if v.Take != nil {
		fmt.Printf("    training take %s %.2f–%.2f\n", v.Take.AssetID, v.Take.From, v.Take.To)
	}
	if v.Reading != nil {
		fmt.Printf("    reading %s %.2f–%.2f\n", v.Reading.AssetID, v.Reading.From, v.Reading.To)
	}
	if v.Training != nil {
		printWork(v.Training, true)
	}
	if v.Removed != nil {
		fmt.Printf("    deleted at the service %s %s\n", v.Removed.At.Local().Format(time.RFC3339), v.Removed.Detail)
	}
}

func presenterListCmd() *cobra.Command {
	var in aiwork.PresenterListIn
	var details bool
	c := &cobra.Command{Use: "list", Short: "People's own presenters, where each stands and what it needs next", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "presenter.list", in, func(o any) {
				out := o.(*aiwork.PresenterListOut)
				for i := range out.Presenters {
					printPresenter(&out.Presenters[i], details)
				}
				fmt.Println(out.Summary)
			})
		}}
	c.Flags().StringVar(&in.Person, "person", "", "one person's")
	c.Flags().BoolVar(&in.Retired, "retired", false, "retired ones too")
	addDetails(c, &details)
	return c
}

func presenterShowCmd() *cobra.Command {
	var details bool
	c := &cobra.Command{Use: "show <presenter>", Short: "A presenter: whose, the look and consent it relies on, its test clips, the clips it read and the cuts that use them", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "presenter.show", aiwork.PresenterIDIn{ID: args[0]}, func(o any) { printPresenter(o.(*aiwork.PresenterView), details) })
		}}
	addDetails(c, &details)
	return c
}

func presenterStartCmd() *cobra.Command {
	var in aiwork.PresenterStartIn
	c := &cobra.Command{Use: "start", Short: "Begin a person's presenter from their approved look: asks the service for the words they read on camera (people only)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if in.Person == "" && in.Again == "" {
				return fail(c, apperr.InvalidInput, "Say whose presenter it is (--person), or which presenter needs new words (--again).", "")
			}
			return peopleOnly(c, "presenter.start", in, "begin a presenter", func(o any) {
				out := o.(*aiwork.PresenterOut)
				fmt.Println(out.Summary)
			})
		}}
	fl := c.Flags()
	fl.StringVar(&in.Person, "person", "", "whose presenter (a name on the People page)")
	fl.StringVar(&in.Look, "look", "", "their approved look (default: their newest)")
	fl.StringVar(&in.Name, "name", "", "a short name for the presenter (default: from the person's name)")
	fl.StringVar(&in.Title, "title", "", "how it is shown (default: the person's name)")
	fl.StringVar(&in.Model, "model", "", "the service and model, provider/model (default: the usual one)")
	fl.StringVar(&in.ProjectID, "project", "", "whose Content Settings decide which services may be used")
	fl.BoolVar(&in.GreenScreen, "green-screen", false, "the training take will be filmed against a green screen (so a colour can go behind the presenter)")
	fl.StringVar(&in.Language, "language", "", "the language of the service's consent words (default en)")
	fl.StringVar(&in.Again, "again", "", "ask for new consent words for this presenter (they expire)")
	return c
}

func presenterMakeCmd() *cobra.Command {
	var in aiwork.PresenterMakeIn
	var maxCost, tfrom, tto, rfrom, rto float64
	var wait, details bool
	c := &cobra.Command{Use: "make <presenter>", Short: "Make the presenter at the service from a training take and the person reading its consent words — people only, priced first", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ID = args[0]
			fl := c.Flags()
			for _, x := range []struct {
				name string
				v    *float64
				dst  **float64
			}{{"take-from", &tfrom, &in.TakeFrom}, {"take-to", &tto, &in.TakeTo}, {"reading-from", &rfrom, &in.ReadingFrom}, {"reading-to", &rto, &in.ReadingTo}} {
				if fl.Changed(x.name) {
					*x.dst = x.v
				}
			}
			if fl.Changed("max-cost") {
				in.MaxUSD = &maxCost
			}
			return runPresenterMake(c, &in, wait, details)
		}}
	fl := c.Flags()
	fl.StringVar(&in.Take, "take", "", "a clip of the person speaking to the camera, on their own")
	fl.Float64Var(&tfrom, "take-from", 0, "where the training take starts in the clip (seconds)")
	fl.Float64Var(&tto, "take-to", 0, "where it ends")
	fl.StringVar(&in.Reading, "reading", "", "the consent recording, where the person reads the service's consent words (the default; no other recording is taken)")
	fl.Float64Var(&rfrom, "reading-from", 0, "where the reading starts")
	fl.Float64Var(&rto, "reading-to", 0, "where it ends")
	fl.StringVar(&in.Consent, "consent", "", "the consent it relies on (default: the newest that covers a presenter and names the service)")
	fl.BoolVar(&in.ShowsPerson, "shows-person", false, "you say the take shows the person (when MetaRush hasn't looked for faces in it)")
	fl.StringVar(&in.ProjectID, "project", "", "the project whose budget pays")
	fl.Float64Var(&maxCost, "max-cost", 0, "the most you agree to pay, in US dollars")
	fl.BoolVar(&in.DryRun, "dry-run", false, "price and check it only")
	fl.BoolVar(&wait, "wait", false, "make it here and wait")
	addDetails(c, &details)
	_ = c.MarkFlagRequired("take")
	return c
}

// runPresenterMake is presenter make for a person at a terminal: the
// estimate first, a yes, then the request (and, with --wait, the job).
func runPresenterMake(c *cobra.Command, in *aiwork.PresenterMakeIn, wait, details bool) error {
	if !isInteractive() {
		return fail(c, apperr.HumanOnly, "Only a person can make someone's presenter, in the dashboard or in their own terminal.", "Run this command yourself.")
	}
	a, err := openApp(c, "cli")
	if err != nil {
		return err
	}
	defer a.Close()
	ctx, cancel := signalCtx(c.Context())
	defer cancel()
	caller := ops.Caller{Human: true}
	if in.MaxUSD == nil && !in.DryRun && estimateFirstHere(c, a) {
		probe := *in
		probe.DryRun = true
		b, _ := json.Marshal(probe)
		res, err := ops.Call(ctx, a, "presenter.make", b, caller)
		if err != nil {
			return failErr(c, err)
		}
		est := res.(*aiwork.PresenterOut).Estimate
		printAIEstimate(est, details)
		if !est.Allowed {
			return fail(c, apperr.Conflict, "Nothing was sent.", "")
		}
		if !askYes(fmt.Sprintf("Make the presenter for about %s", moneyWords(est.USD)), false) {
			return fail(c, apperr.Canceled, "Nothing was sent or spent.", "")
		}
		v := est.USD
		in.MaxUSD = &v
	}
	b, _ := json.Marshal(in)
	res, err := ops.Call(ctx, a, "presenter.make", b, caller)
	if err != nil {
		ae := apperr.From(err)
		if ae.Code == apperr.ConfirmRequired && !jsonMode(c) {
			if d, ok := ae.Details.(map[string]any); ok {
				if v, ok := d["estimateUsd"].(float64); ok {
					ae = ae.WithCommand(sameCommandWith(c, "--max-cost", strconv.FormatFloat(v, 'f', 3, 64)))
				}
			}
		}
		return failErr(c, ae)
	}
	out := res.(*aiwork.PresenterOut)
	if !wait || out.DryRun || out.JobID == "" {
		return emit(c, out, func() {
			if out.DryRun && out.Estimate != nil {
				printAIEstimate(out.Estimate, details)
				fmt.Println("Nothing was sent or spent.")
				return
			}
			fmt.Println(out.Summary)
		})
	}
	if !jsonMode(c) {
		fmt.Println(out.Summary)
	}
	jobs, err := waitJobs(c, a, []string{out.JobID}, 60*time.Minute, jsonMode(c))
	if err != nil {
		return failErr(c, err)
	}
	v, err := ops.CallTyped[aiwork.PresenterView](ctx, a, "presenter.show", aiwork.PresenterIDIn{ID: out.Presenter.ID}, ops.Caller{})
	if err != nil {
		return failErr(c, err)
	}
	return emit(c, map[string]any{"run": out, "presenter": v, "jobs": jobs}, func() { printPresenter(v, details) })
}

func presenterTestCmd() *cobra.Command {
	var in aiwork.PresenterTestIn
	var maxCost, from, to float64
	var wait, details bool
	c := &cobra.Command{Use: "test <presenter>", Short: "A test clip of a presenter not yet approved, reading a clip's sound — people only, priced first", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ID = args[0]
			fl := c.Flags()
			if fl.Changed("from") {
				in.From = &from
			}
			if fl.Changed("to") {
				in.To = &to
			}
			if fl.Changed("max-cost") {
				in.MaxUSD = &maxCost
			}
			if !isInteractive() {
				return fail(c, apperr.HumanOnly, "Only a person can test a presenter, in the dashboard or in their own terminal.", "Run this command yourself.")
			}
			return runAI(c, "presenter.test", &in, &in.MaxUSD, in.DryRun, wait, details, ops.Caller{Human: true})
		}}
	fl := c.Flags()
	fl.StringVar(&in.Clip, "clip", "", "the sound it reads: a clip (AI speech in the person's voice copy or a voice that is no one's, or a recording in which every voice is named as them)")
	fl.Float64Var(&from, "from", 0, "where the part of the clip starts (seconds)")
	fl.Float64Var(&to, "to", 0, "where it ends")
	fl.StringVar(&in.ProjectID, "project", "", "the project it is for")
	fl.Float64Var(&maxCost, "max-cost", 0, "the most you agree to pay, in US dollars")
	fl.BoolVar(&in.DryRun, "dry-run", false, "price and check it only")
	fl.BoolVar(&wait, "wait", false, "make it here and wait")
	addDetails(c, &details)
	_ = c.MarkFlagRequired("clip")
	return c
}

func presenterDecideCmd(approve bool) *cobra.Command {
	var in aiwork.PresenterDecideIn
	use, short, op, what := "approve <presenter>", "Approve a presenter after its test clips, so clips can be made with it (people only)", "presenter.approve", "approve a presenter"
	if !approve {
		use, short, op, what = "retire <presenter>", "Retire a presenter: deleted at the service, nothing more made with it, clips waiting stopped (people only)", "presenter.retire", "retire a presenter"
	}
	c := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ID = args[0]
			return peopleOnly(c, op, in, what, func(o any) { fmt.Println(o.(*aiwork.PresenterOut).Summary) })
		}}
	c.Flags().StringVar(&in.Note, "note", "", "why, in your words")
	return c
}

func presenterStockCmd() *cobra.Command {
	var in aiwork.PresenterStockIn
	c := &cobra.Command{Use: "stock", Short: "A service's stock presenters and their licence (offered only where it covers commercial use and client work)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "presenter.stock", in, func(o any) {
				out := o.(*aiwork.PresenterStockOut)
				for _, s := range out.Services {
					fmt.Printf("%s: %s\n", s.ProviderTitle, s.Licence.Summary)
					for _, p := range s.Presenters {
						fmt.Printf("  %s (stock:%s)\n", p.Name, p.ID)
					}
					if s.Note != "" && s.Note != s.Licence.Summary {
						fmt.Println("  " + s.Note)
					}
				}
				fmt.Println(out.Summary)
			})
		}}
	c.Flags().StringVar(&in.Provider, "service", "", "one service's (default: every service with presenters)")
	return c
}

func presenterOptOutCmd() *cobra.Command {
	var in aiwork.PresenterOptOutIn
	c := &cobra.Command{Use: "opt-out <service>", Short: "Whether a service trains on what it is sent, and record that its training opt-out is set for this computer's key (--how, people only)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Provider = args[0]
			print := func(o any) { fmt.Println(o.(*aiwork.PresenterOptOutOut).Summary) }
			if in.How == "" && !in.Remove {
				return runOp(c, "presenter.optout", in, print)
			}
			return peopleOnly(c, "presenter.optout.set", in, "record a service's training opt-out", print)
		}}
	c.Flags().StringVar(&in.How, "how", "", "record that the opt-out is set: what you did, in your words")
	c.Flags().BoolVar(&in.Remove, "remove", false, "take the record back (the opt-out isn't set any more)")
	return c
}
