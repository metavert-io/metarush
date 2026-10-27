package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/spoken"
)

// cut say: change a word or short phrase someone said into new words in
// their own voice copy, with their consent (D308–D312). estimate prices and
// checks it (a read); make asks for it (people only, priced first); the new
// words are checked on this computer, then place puts them in the cut as a
// new version with a review copy (people only), and undo takes them out.
func cutSayCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "say", Short: "Change a word or short phrase someone said into new words in their own voice copy, with their consent: estimate, make, place, undo, list, show"}
	cmd.AddCommand(cutSayMakeCmd(true), cutSayMakeCmd(false), cutSayPlaceCmd(false), cutSayPlaceCmd(true), cutSayListCmd(), cutSayShowCmd(), cutSayMouthCmd())
	return cmd
}

type sayFlags struct {
	in       spoken.SayIn
	from, to float64
	maxCost  float64
	wait     bool
	details  bool
}

func (f *sayFlags) add(c *cobra.Command, estimate bool) {
	fl := c.Flags()
	fl.StringVar(&f.in.AssetID, "clip", "", "the clip whose words change (with --from and --to)")
	fl.Float64Var(&f.from, "from", 0, "where the words start in the clip (seconds)")
	fl.Float64Var(&f.to, "to", 0, "where they end")
	fl.StringVar(&f.in.Quote, "quote", "", "or: the words as the cut says them (said in one place)")
	fl.StringVar(&f.in.Words, "as", "", "what to say instead")
	fl.StringVar(&f.in.Branch, "branch", "", "the cut (default: the one whose latest version plays the words)")
	fl.StringVar(&f.in.Model, "model", "", "provider/model of the speech (default: the best of the service holding the voice copy)")
	if !estimate {
		fl.Float64Var(&f.maxCost, "max-cost", 0, "the most you agree to pay in US dollars (from the estimate)")
		fl.BoolVar(&f.wait, "wait", false, "wait until the new words are made and checked")
	}
	addDetailsFlag(c, &f.details)
}

func (f *sayFlags) prepare(c *cobra.Command, args []string) {
	f.in.ProjectID = args[0]
	fl := c.Flags()
	if fl.Changed("from") {
		f.in.From = &f.from
	}
	if fl.Changed("to") {
		f.in.To = &f.to
	}
	if fl.Changed("max-cost") {
		f.in.MaxUSD = &f.maxCost
	}
}

func cutSayMakeCmd(estimate bool) *cobra.Command {
	f := &sayFlags{}
	use, short := "make <project> (--clip <clip> --from <s> --to <s> | --quote \"…\") --as \"new words\"",
		"Make new words in the speaker's own voice copy (people only, priced first); they're checked before they can go in the cut"
	if estimate {
		use = "estimate <project> (--clip <clip> --from <s> --to <s> | --quote \"…\") --as \"new words\""
		short = "Price changing words someone said into new words in their own voice copy, and check it may be made; nothing is sent or spent"
	}
	c := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		f.prepare(c, args)
		if estimate {
			return runOp(c, "cut.say.estimate", f.in, func(o any) { printSayEstimate(o.(*spoken.SayEstimate), f.details) })
		}
		if !isInteractive() {
			return fail(c, apperr.HumanOnly, "Only a person changes what someone said, in the dashboard or in their own terminal.",
				"Open the clip's Transcript in the dashboard (right-click the words: Say it differently…), or run this command yourself.")
		}
		return runSayMake(c, f)
	}}
	f.add(c, estimate)
	return c
}

// runSayMake shows the estimate and asks for the price before anything is
// sent (unless --max-cost already agreed to one).
func runSayMake(c *cobra.Command, f *sayFlags) error {
	a, err := openApp(c, "cli")
	if err != nil {
		return err
	}
	defer a.Close()
	ctx, cancel := signalCtx(c.Context())
	defer cancel()
	if f.in.MaxUSD == nil && estimateFirstHere(c, a) {
		est, err := ops.CallTyped[spoken.SayEstimate](ctx, a, "cut.say.estimate", f.in, ops.Caller{Human: true})
		if err != nil {
			return failErr(c, err)
		}
		printSayEstimate(est, f.details)
		if !est.Allowed {
			return fail(c, apperr.Conflict, "Nothing was made.", "")
		}
		if !askYes(fmt.Sprintf("Make it for about %s", moneyWords(est.USD)), false) {
			return fail(c, apperr.Canceled, "Nothing was made or spent.", "")
		}
		v := est.USD
		f.in.MaxUSD = &v
	}
	b, _ := json.Marshal(f.in)
	res, err := ops.Call(ctx, a, "cut.say.make", b, ops.Caller{Human: true})
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
	out := res.(*spoken.SayMakeOut)
	if !f.wait || out.JobID == "" {
		return emit(c, out, func() {
			fmt.Println(out.Summary)
			if f.details {
				fmt.Printf("    change %s  job %s\n", out.Change.ID, out.JobID)
			} else {
				fmt.Println("(Follow it with metarush activity, or add --wait.)")
			}
		})
	}
	if !jsonMode(c) {
		fmt.Println(out.Summary)
	}
	jobs, err := waitJobs(c, a, []string{out.JobID}, 30*time.Minute, jsonMode(c))
	if err != nil {
		return failErr(c, err)
	}
	v, err := ops.CallTyped[ops.SpokenView](ctx, a, "cut.say.show", ops.CutSayIDIn{ID: out.Change.ID}, ops.Caller{})
	if err != nil {
		return failErr(c, err)
	}
	err = emit(c, map[string]any{"make": out, "change": v, "jobs": jobs}, func() { printSpoken(c, v, f.details) })
	if err == nil && v.State != models.SpokenReady {
		return &exitError{code: 1}
	}
	return err
}

func printSayEstimate(e *spoken.SayEstimate, details bool) {
	fmt.Println(e.Summary)
	if e.Speaker != nil {
		line := fmt.Sprintf("  %s says it (named by a person)", e.Speaker.Name)
		if e.Speaker.Match != nil {
			line += fmt.Sprintf("; the voice there is %.0f%% like their other recordings", *e.Speaker.Match*100)
		}
		fmt.Println(line)
	}
	if e.Voice != nil {
		fmt.Printf("  with %s's voice copy at %s\n", e.Speaker.Name, e.Voice.ProviderTitle)
	}
	if e.Context != "" {
		fmt.Printf("  sends the words “%s”\n", e.Context)
	}
	if len(e.Cuts) > 1 {
		var ts []string
		for _, ct := range e.Cuts {
			ts = append(ts, ct.Title)
		}
		fmt.Printf("  cuts that play these words: %s\n", strings.Join(ts, ", "))
	}
	for _, p := range e.Problems[min(1, len(e.Problems)):] {
		fmt.Println("  also: " + p)
	}
	if details {
		fmt.Printf("    clip %s  %.2f–%.2f s  model %s  consent %s\n", e.AssetID, e.From, e.To, e.Model, e.ConsentID)
	}
}

func cutSayPlaceCmd(undo bool) *cobra.Command {
	var in ops.CutSayPlaceIn
	var details bool
	op, use, short := "cut.say.place", "place <change>", "Put checked new words into the cut as a new version, with a review copy (people only)"
	if undo {
		op, use, short = "cut.say.undo", "undo <change>", "Take changed words out of the cut again: a new version plays what was said"
	}
	c := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		in.ID = args[0]
		caller := ops.Caller{}
		if !undo {
			if !isInteractive() {
				return fail(c, apperr.HumanOnly, "Only a person puts changed words in a cut, in the dashboard or in their own terminal.", "")
			}
			caller.Human = true
		}
		a, err := openApp(c, "cli")
		if err != nil {
			return err
		}
		defer a.Close()
		call := func(x ops.CutSayPlaceIn) (*ops.CutSayPlaceOut, error) {
			b, _ := json.Marshal(x)
			res, err := ops.Call(c.Context(), a, op, b, caller)
			if err != nil {
				return nil, err
			}
			return res.(*ops.CutSayPlaceOut), nil
		}
		if in.ConfirmToken == "" && !in.DryRun && interactive() && !jsonMode(c) {
			dry := in
			dry.DryRun = true
			r, err := call(dry)
			if err != nil {
				return failErr(c, err)
			}
			printCutEdit(c, &r.CutEditOut, "")
			if r.NoChange || r.Check == nil || r.Check.ConfirmToken == "" {
				return nil
			}
			if !askYes("Save this change as a new version?", false) {
				return fail(c, apperr.Canceled, "not saved", "")
			}
			in.ConfirmToken = r.Check.ConfirmToken
		}
		r, err := call(in)
		if err != nil {
			return failErr(c, err)
		}
		return emit(c, r, func() {
			next := ""
			if r.Check != nil && r.Check.ConfirmToken != "" {
				next = sameCommandWith(c, "--confirm", r.Check.ConfirmToken)
			}
			printCutEdit(c, &r.CutEditOut, next)
		})
	}}
	fl := c.Flags()
	fl.StringVar(&in.Branch, "branch", "", "the cut (default: the one the change was made for)")
	fl.BoolVar(&in.DryRun, "dry-run", false, "only check: the change, its warnings and a confirm token")
	fl.StringVar(&in.ConfirmToken, "confirm", "", "token from a check of exactly this change")
	fl.BoolVar(&in.AllowUnlock, "allow-unlock", false, "permit changes to locked clips (only when the person asked)")
	fl.BoolVar(&in.NoRender, "no-render", false, "don't make a review copy of the new version")
	addDetailsFlag(c, &details)
	return c
}

func cutSayListCmd() *cobra.Command {
	var in ops.CutSayListIn
	var details bool
	c := &cobra.Command{Use: "list <project>", Short: "List words changed in a speaker's own voice copy: before and after, whose voice, how they were checked, whether the cut plays them",
		Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
			in.ProjectID = args[0]
			return runOp(c, "cut.say.list", in, func(o any) {
				out := o.(*ops.CutSayListOut)
				fmt.Println(out.Summary)
				for i := range out.Changes {
					printSpoken(c, &out.Changes[i], details)
				}
			})
		}}
	c.Flags().StringVar(&in.AssetID, "clip", "", "only the changes to this clip's words")
	addDetailsFlag(c, &details)
	return c
}

func cutSayShowCmd() *cobra.Command {
	var details bool
	c := &cobra.Command{Use: "show <change>", Short: "Show a word change made in a speaker's own voice copy: before and after, voice and consent, price, checks, versions",
		Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "cut.say.show", ops.CutSayIDIn{ID: args[0]}, func(o any) { printSpoken(c, o.(*ops.SpokenView), details) })
		}}
	addDetailsFlag(c, &details)
	return c
}

func printSpoken(c *cobra.Command, v *ops.SpokenView, details bool) {
	state := map[string]string{models.SpokenMaking: "being made", models.SpokenReady: "ready to hear", models.SpokenFailed: "didn't pass its check",
		models.SpokenPlaced: "in the cut", models.SpokenUndone: "taken out again", models.SpokenWithdrawn: "consent withdrawn"}[v.State]
	fmt.Printf("• %s — %s, %s\n", v.Says, state, v.CutTitle)
	if v.Next != "" {
		fmt.Println("  " + v.Next)
	}
	if details || detailsMode(c) {
		fmt.Printf("    change %s  clip %s  %.2f–%.2f s  sound %s  generation %s\n", v.ID, v.AssetID, v.From, v.To, v.Sound, v.GenerationID)
	}
}

// cutSayMouthCmd matches the mouth to checked new words (lip sync, D312):
// priced first, people only.
func cutSayMouthCmd() *cobra.Command {
	var in spoken.MouthIn
	var maxCost float64
	c := &cobra.Command{Use: "mouth <change>", Short: "Match the mouth to checked new words (lip sync over the stretch, with everyone's consent): priced first, people only",
		Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
			in.ID = args[0]
			if c.Flags().Changed("max-cost") {
				in.MaxUSD = &maxCost
			}
			if !in.DryRun && !isInteractive() {
				return fail(c, apperr.HumanOnly, "Only a person asks for the mouth to match new words, in the dashboard or in their own terminal.", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			print := func(o any) {
				out := o.(*spoken.MouthOut)
				fmt.Println(out.Summary)
			}
			if !in.DryRun && in.MaxUSD == nil && estimateFirstHere(c, a) {
				dry := in
				dry.DryRun = true
				est, err := ops.CallTyped[spoken.MouthOut](c.Context(), a, "cut.say.mouth", dry, ops.Caller{Human: true})
				if err != nil {
					return failErr(c, err)
				}
				print(est)
				if est.Estimate == nil || !est.Estimate.Allowed {
					return fail(c, apperr.Conflict, "Nothing was made.", "")
				}
				if !askYes(fmt.Sprintf("Match the mouth for about %s", moneyWords(est.Estimate.USD)), false) {
					return fail(c, apperr.Canceled, "Nothing was made or spent.", "")
				}
				v := est.Estimate.USD
				in.MaxUSD = &v
			}
			return runOpWith(c, a, "cut.say.mouth", in, ops.Caller{Human: !in.DryRun || isInteractive()}, print)
		}}
	c.Flags().Float64Var(&maxCost, "max-cost", 0, "the most you agree to pay in US dollars (from the estimate)")
	c.Flags().BoolVar(&in.DryRun, "dry-run", false, "only price it and check it may be made")
	c.Flags().StringVar(&in.Model, "model", "", "provider/model of the lip sync (default: the usual one)")
	return c
}
