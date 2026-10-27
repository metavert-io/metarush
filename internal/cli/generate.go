package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/videogen"
)

func generateCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "generate", Aliases: []string{"gen"}, Short: "Make clips with generative video (Google Gemini): a shot from words, a look or frames, takes, extensions and edits — priced first",
		Long: `Generative video makes new clips with Google Gemini. Every request is priced first and
only goes ahead with the price you agreed to (--max-cost, or a yes when asked); the money is set aside
against the project's budget and today's limit, and settled with what it cost. Results are ordinary clips
in the project's Sources, marked as AI-generated everywhere. A person's likeness needs their look and their
consent (metarush looks, metarush consent); MetaRush never makes a child's or a public figure's likeness.

  metarush generate estimate "a red paper boat drifting on a pond" --project <id> --seconds 3 --quality draft
  metarush generate shot "a red paper boat drifting on a pond" --project <id> --seconds 3 --max-cost 0.20 --wait
  metarush generate shot "walks into the garage" --look ada --project <id> --seconds 6
  metarush generate shot "the sun comes out" --from <clip>@0:12 --project <id> --no-people
  metarush generate again <generation> --takes 3          # variations: new takes, nothing replaced
  metarush generate extend <clip> --seconds 5 --prompt "the boat reaches the shore"
  metarush generate edit <clip> "make it night"
  metarush generate list --project <id>
  metarush generate show <generation>`}
	cmd.AddCommand(genShotCmd(false), genShotCmd(true), genAgainCmd(), genExtendCmd(), genEditCmd(), genListCmd(), genShowCmd(), genCancelCmd(), genSettingsCmd(), genConfigureCmd())
	return cmd
}

// shotFlags are the flags shared by shot and estimate.
type shotFlags struct {
	in        generate.GenShotIn
	from, to  string
	maxCost   float64
	aspect    string
	wait      bool
	details   bool
	promptArg string
}

func (f *shotFlags) add(c *cobra.Command, estimate bool) {
	fl := c.Flags()
	fl.StringVar(&f.in.ProjectID, "project", "", "the project the shot is for")
	fl.StringVar(&f.in.Look, "look", "", "who (or which character) appears: a look's short name")
	fl.IntVar(&f.in.Seconds, "seconds", 0, "how long (3 to 10; default 6)")
	fl.StringVar(&f.aspect, "aspect", "", "16:9 (wide, default) or 9:16 (tall)")
	fl.StringVar(&f.in.Quality, "quality", "", "draft (cheaper, default) or final")
	fl.StringVar(&f.in.Resolution, "resolution", "", "360p, 720p, 1080p or 4k (overrides --quality)")
	fl.StringVar(&f.in.Model, "model", "", "the video model (default: the team's; vertex/veo-3.1-fast-generate-001 or vertex/veo-3.1-generate-001 for Veo through Google Cloud)")
	fl.StringVar(&f.from, "from", "", "start from this frame: <clip>@<time> (continue the shot), or a picture from the resources library (its name)")
	fl.StringVar(&f.to, "to", "", "end on this frame: <clip>@<time> or a picture (with --from: a shot between two frames)")
	fl.StringSliceVar(&f.in.Images, "image", nil, "a picture from the resources library (name or name@version); the prompt can say <IMAGE_REF_0>…")
	fl.BoolVar(&f.in.NoPeople, "no-people", false, "you say the frames and pictures show no real people")
	fl.IntVar(&f.in.Takes, "takes", 0, "how many takes at once (1 to 4)")
	fl.StringVar(&f.promptArg, "prompt", "", "the prompt (instead of the argument)")
	if !estimate {
		fl.Float64Var(&f.maxCost, "max-cost", 0, "the most you agree to pay for all takes, in US dollars (from the estimate)")
		fl.BoolVar(&f.in.DryRun, "dry-run", false, "price and check it only")
		fl.BoolVar(&f.wait, "wait", false, "make it here and wait until the clip is in Sources")
	}
	addDetails(c, &f.details)
}

func (f *shotFlags) prepare(c *cobra.Command, args []string) error {
	f.in.Prompt = strings.TrimSpace(strings.Join(args, " "))
	if f.promptArg != "" {
		f.in.Prompt = f.promptArg
	}
	switch strings.ToLower(f.aspect) {
	case "", "16:9", "wide", "landscape":
		if f.aspect != "" {
			f.in.AspectRatio = "16:9"
		}
	case "9:16", "tall", "portrait", "vertical":
		f.in.AspectRatio = "9:16"
	default:
		return fail(c, apperr.InvalidInput, "--aspect takes 16:9 (wide) or 9:16 (tall)", "")
	}
	var err error
	if f.in.From, err = frameFlag(f.from); err != nil {
		return failErr(c, err)
	}
	if f.in.To, err = frameFlag(f.to); err != nil {
		return failErr(c, err)
	}
	if c.Flags().Changed("max-cost") {
		v := f.maxCost
		f.in.MaxUSD = &v
	}
	return nil
}

// frameFlag reads <clip>@<time> (seconds, or m:ss.s), or a picture from the
// resources library (its name, name@version or ID).
func frameFlag(s string) (*generate.FrameRef, error) {
	if s == "" {
		return nil, nil
	}
	if !strings.HasPrefix(s, "ast_") {
		return &generate.FrameRef{Picture: s}, nil
	}
	id, at, ok := strings.Cut(s, "@")
	t, err := clockSeconds(at)
	if !ok || err != nil || !strings.HasPrefix(id, "ast_") {
		return nil, apperr.New(apperr.InvalidInput, "A frame is a clip and a time, like ast_…@0:12.5.")
	}
	return &generate.FrameRef{AssetID: id, At: t}, nil
}

func clockSeconds(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if m, sec, ok := strings.Cut(s, ":"); ok {
		mm, err := strconv.Atoi(m)
		if err != nil {
			return 0, err
		}
		ss, err := strconv.ParseFloat(sec, 64)
		if err != nil {
			return 0, err
		}
		return float64(mm)*60 + ss, nil
	}
	return strconv.ParseFloat(s, 64)
}

func genShotCmd(estimate bool) *cobra.Command {
	f := &shotFlags{}
	use, short := "shot <prompt>", "Make a new shot: priced first, made with the price you agree to (--max-cost or a yes), added to Sources"
	if estimate {
		use, short = "estimate <prompt>", "Price a shot and check it may be made (consent, budget, this computer's key); nothing is sent or spent"
	}
	c := &cobra.Command{Use: use, Short: short, RunE: func(c *cobra.Command, args []string) error {
		if err := f.prepare(c, args); err != nil {
			return err
		}
		if estimate {
			return runOp(c, "generate.estimate", f.in, func(o any) { printEstimate(o.(*generate.GenEstimate), f.details) })
		}
		return runGenerate(c, "generate.shot", &f.in, &f.in.MaxUSD, f.in.DryRun, f.wait, f.details)
	}}
	f.add(c, estimate)
	return c
}

// runGenerate asks for generations: with no agreed price, a person at a
// terminal sees the estimate and says yes; otherwise the op refuses plainly.
func runGenerate(c *cobra.Command, op string, in any, maxUSD **float64, dry, wait, details bool) error {
	a, err := openApp(c, "cli")
	if err != nil {
		return err
	}
	defer a.Close()
	ctx, cancel := signalCtx(c.Context())
	defer cancel()
	// The estimate first (D208); with the person's estimates off the request
	// goes ahead without --max-cost, within the budget and today's limit.
	if *maxUSD == nil && !dry && estimateFirstHere(c, a) {
		raw, _ := json.Marshal(in)
		var probe map[string]any
		_ = json.Unmarshal(raw, &probe)
		probe["dryRun"] = true
		b, _ := json.Marshal(probe)
		res, err := ops.Call(ctx, a, op, b, ops.Caller{})
		if err != nil {
			return failErr(c, err)
		}
		est := res.(*generate.GenRunOut).Estimate
		printEstimate(&est, details)
		if !est.Allowed {
			return fail(c, apperr.Budget, "Nothing was made.", "")
		}
		if !askYes(fmt.Sprintf("Make it for about %s", moneyWords(est.USD)), false) {
			return fail(c, apperr.Canceled, "Nothing was made or spent.", "")
		}
		v := est.USD
		*maxUSD = &v
	}
	b, _ := json.Marshal(in)
	res, err := ops.Call(ctx, a, op, b, ops.Caller{Human: isInteractive()})
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
	out := res.(*generate.GenRunOut)
	if !wait || out.DryRun || len(out.JobIDs) == 0 {
		return emit(c, out, func() { printRun(out, details) })
	}
	if !jsonMode(c) {
		fmt.Println(out.Summary)
	}
	jobs, err := waitJobs(c, a, out.JobIDs, 30*time.Minute, jsonMode(c))
	if err != nil {
		return failErr(c, err)
	}
	var views []generate.GenView
	for _, g := range out.Generations {
		if v, err := ops.CallTyped[generate.GenView](ctx, a, "generate.show", generate.GenIDIn{ID: g.ID}, ops.Caller{}); err == nil {
			views = append(views, *v)
		}
	}
	failed := 0
	for _, j := range jobs {
		if j.State != models.JobSucceeded {
			failed++
		}
	}
	err = emit(c, map[string]any{"run": out, "generations": views, "jobs": jobs}, func() {
		for _, v := range views {
			fmt.Printf("%s %s\n", v.Summary, costWords(v))
			printGenHint(&v)
			if details {
				printGenDetails(&v)
			}
		}
	})
	if err == nil && failed > 0 {
		return &exitError{code: 1}
	}
	return err
}

func moneyWords(usd float64) string {
	if usd < 0.01 && usd > 0 {
		return "less than a cent"
	}
	return fmt.Sprintf("$%.2f", usd)
}

func costWords(v generate.GenView) string {
	switch {
	case v.Settled != nil:
		return "(" + moneyWords(v.Settled.USD) + ")"
	case v.State == generate.StateMaking || v.State == generate.StateWaiting || v.State == generate.StateSending:
		return "(about " + moneyWords(v.Estimate.USD) + ")"
	}
	return ""
}

func printEstimate(e *generate.GenEstimate, details bool) {
	fmt.Println(e.Summary)
	for _, r := range e.References {
		fmt.Println("  " + r)
	}
	if b := e.Budget.Project; b != nil {
		if b.LimitUSD != nil {
			fmt.Printf("  this project: %s of %s spent\n", moneyWords(b.SpentUSD+b.HeldUSD), moneyWords(*b.LimitUSD))
		} else {
			fmt.Printf("  this project: %s spent, no project limit\n", moneyWords(b.SpentUSD+b.HeldUSD))
		}
	}
	if t := e.Budget.Today; t.LimitUSD != nil {
		fmt.Printf("  today: %s of %s spent\n", moneyWords(t.SpentUSD+t.HeldUSD), moneyWords(*t.LimitUSD))
	}
	if e.Consent.Message != "" && e.Consent.OK {
		fmt.Println("  " + e.Consent.Message)
	}
	for _, p := range e.Problems {
		fmt.Println("  ✗ " + p)
	}
	for _, n := range e.Notes {
		fmt.Println("  note: " + n)
	}
	printCost(e.Cost, details)
	if details {
		fmt.Printf("    model %s (%s), %s, %ds %s %s, %d takes, per take $%.3f ($%.3f a second), price table %s\n", e.Model, e.Stage, e.Task, e.Seconds, e.Resolution, e.AspectRatio,
			e.Takes, e.PerTakeUSD, e.PerSecondUSD, e.PriceTable)
		fmt.Printf("    tokens per take: video %d, input %d, thinking allowance %d\n", e.VideoTokens, e.InputTokens, e.ThinkingTokens)
	}
}

func printRun(out *generate.GenRunOut, details bool) {
	if out.DryRun {
		printEstimate(&out.Estimate, details)
		fmt.Println("Nothing was sent or spent.")
		return
	}
	fmt.Println(out.Summary)
	if details {
		for _, g := range out.Generations {
			fmt.Printf("    generation %s  job %s  take %d\n", g.ID, g.JobID, g.Take)
		}
	} else {
		fmt.Println("(Follow it with metarush activity, or add --wait.)")
	}
}

// printGenHint says what to do about a generation that couldn't be made (its
// outcome's hint: the free tier's names Google's page for the project's tier).
func printGenHint(v *generate.GenView) {
	if o := v.Outcome; o != nil && o.Hint != "" && (v.State == generate.StateFailed || v.State == generate.StateRefused) {
		fmt.Println(o.Hint)
	}
}

func printGenDetails(v *generate.GenView) {
	fmt.Printf("    generation %s (%s, take %d, group %s)\n", v.ID, v.Operation, v.Take, v.GroupID)
	fmt.Printf("    %s %s (%s), price table %s\n", v.ProviderTitle, v.Model, v.Stage, v.PriceTable)
	fmt.Printf("    prompt: %q\n", v.Prompt)
	if v.SentPrompt != "" {
		fmt.Printf("    sent: %q\n", v.SentPrompt)
	}
	if v.Template != nil {
		fmt.Printf("    template %s %v\n", v.Template.Name, v.Template.Vars)
	}
	if v.Look != nil {
		fmt.Printf("    look %s@%s\n", v.Look.Name, v.Look.Version)
	}
	for _, r := range v.References {
		fmt.Printf("    %s %s sha256 %s", r.Role, r.Ref, r.SHA256)
		if len(r.People) > 0 {
			fmt.Printf(" shows %s", strings.Join(r.People, ", "))
		}
		fmt.Println()
	}
	p := v.Params
	fmt.Printf("    %ds %s %s %s, task %s\n", p.Seconds, p.Resolution, p.AspectRatio, p.Quality, p.Task)
	fmt.Printf("    request sha256 %s\n", v.RequestHash)
	if v.Ticket != nil {
		fmt.Printf("    provider request %s, sent %s\n", v.Ticket.ID, v.Ticket.SubmittedAt.Local().Format(time.RFC3339))
	}
	fmt.Printf("    estimate $%.3f", v.Estimate.USD)
	if v.Outcome != nil && v.Outcome.Actual != nil {
		fmt.Printf(", charged $%.3f (%s; video %d, input %d, thinking %d tokens)", v.Outcome.Actual.USD, v.Outcome.Actual.Basis, v.Outcome.Actual.VideoTokens, v.Outcome.Actual.InputTokens, v.Outcome.Actual.ThinkingTokens)
	}
	fmt.Println()
	if o := v.Outcome; o != nil {
		if s := o.Safety; s != nil {
			fmt.Printf("    safety: blocked=%v side=%s category=%s codes=%v %q\n", s.Blocked, s.Side, s.Category, s.SupportCodes, s.Message)
		}
		for _, out := range o.Outputs {
			fmt.Printf("    clip %s sha256 %s %.2fs %dx%d (%s)\n", out.AssetID, out.SHA256, out.Duration, out.Width, out.Height, out.Delivery)
		}
		if o.Detail != "" {
			fmt.Printf("    provider said: %s\n", o.Detail)
		}
	}
	if f := v.Forgotten; f != nil {
		fmt.Printf("    deleted at the provider: %v %s\n", f.OK, f.Detail)
	}
	fmt.Printf("    asked by %s via %s on %s, %s\n", v.CreatedBy.UserName, v.Channel, v.Machine, v.CreatedAt.Local().Format(time.RFC3339))
}

func genAgainCmd() *cobra.Command {
	var in generate.GenAgainIn
	var maxCost float64
	var wait, details bool
	c := &cobra.Command{Use: "again <generation or clip>", Short: "New takes of a generated shot, made the same way (--takes 3 for variations); nothing is replaced", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ID = args[0]
			if c.Flags().Changed("max-cost") {
				in.MaxUSD = &maxCost
			}
			return runGenerate(c, "generate.again", &in, &in.MaxUSD, in.DryRun, wait, details)
		}}
	fl := c.Flags()
	fl.IntVar(&in.Takes, "takes", 0, "how many new takes (1 to 4)")
	fl.StringVar(&in.Quality, "quality", "", "draft or final (default: as before)")
	fl.StringVar(&in.Resolution, "resolution", "", "360p, 720p, 1080p or 4k")
	fl.Float64Var(&maxCost, "max-cost", 0, "the most you agree to pay, in US dollars")
	fl.BoolVar(&in.DryRun, "dry-run", false, "price it only")
	fl.BoolVar(&wait, "wait", false, "make it here and wait")
	addDetails(c, &details)
	return c
}

func genExtendCmd() *cobra.Command {
	var in generate.GenExtendIn
	var maxCost, from float64
	var wait, details bool
	c := &cobra.Command{Use: "extend <generation or clip>", Short: "Continue a clip (generated or your footage): a new clip, priced first", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ID = args[0]
			if c.Flags().Changed("max-cost") {
				in.MaxUSD = &maxCost
			}
			if c.Flags().Changed("from") {
				in.From = &from
			}
			return runGenerate(c, "generate.extend", &in, &in.MaxUSD, in.DryRun, wait, details)
		}}
	fl := c.Flags()
	fl.IntVar(&in.Seconds, "seconds", 0, "how many seconds to add (3 to 10; default 6)")
	fl.StringVar(&in.Prompt, "prompt", "", "what happens next")
	fl.Float64Var(&from, "from", 0, "for a long clip: where the part to continue starts (seconds)")
	fl.StringVar(&in.ProjectID, "project", "", "the project the new clip goes to (default: the clip's)")
	fl.StringVar(&in.Quality, "quality", "", "draft or final")
	fl.StringVar(&in.Resolution, "resolution", "", "360p, 720p, 1080p or 4k")
	fl.BoolVar(&in.NoPeople, "no-people", false, "you say the clip shows no real people")
	fl.Float64Var(&maxCost, "max-cost", 0, "the most you agree to pay, in US dollars")
	fl.BoolVar(&in.DryRun, "dry-run", false, "price it only")
	fl.BoolVar(&wait, "wait", false, "make it here and wait")
	addDetails(c, &details)
	return c
}

func genEditCmd() *cobra.Command {
	var in generate.GenEditIn
	var maxCost, from, to float64
	var wait, details bool
	c := &cobra.Command{Use: "edit <generation or clip> <instruction>", Short: "Change a clip by an instruction (make it night): a new clip, priced first", Args: cobra.MinimumNArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			in.ID, in.Instruction = args[0], strings.Join(args[1:], " ")
			if c.Flags().Changed("max-cost") {
				in.MaxUSD = &maxCost
			}
			if c.Flags().Changed("from") {
				in.From = &from
			}
			if c.Flags().Changed("to") {
				in.To = &to
			}
			return runGenerate(c, "generate.edit", &in, &in.MaxUSD, in.DryRun, wait, details)
		}}
	fl := c.Flags()
	fl.Float64Var(&from, "from", 0, "for a long clip: where the part to change starts (seconds)")
	fl.Float64Var(&to, "to", 0, "where it ends (seconds)")
	fl.StringVar(&in.ProjectID, "project", "", "the project the new clip goes to (default: the clip's)")
	fl.StringVar(&in.Quality, "quality", "", "draft or final")
	fl.StringVar(&in.Resolution, "resolution", "", "360p, 720p, 1080p or 4k")
	fl.BoolVar(&in.NoPeople, "no-people", false, "you say the clip shows no real people")
	fl.Float64Var(&maxCost, "max-cost", 0, "the most you agree to pay, in US dollars")
	fl.BoolVar(&in.DryRun, "dry-run", false, "price it only")
	fl.BoolVar(&wait, "wait", false, "make it here and wait")
	addDetails(c, &details)
	return c
}

func genListCmd() *cobra.Command {
	var in generate.GenListIn
	var details bool
	c := &cobra.Command{Use: "list", Short: "Generated videos (a project's, a look's, or one clip's takes) with what they cost", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "generate.list", in, func(o any) {
				out := o.(*generate.GenListOut)
				for _, v := range out.Generations {
					fmt.Printf("%s %s\n", v.Summary, costWords(v))
					if details {
						fmt.Printf("    %s  %s take %d  %s\n", v.ID, v.Operation, v.Take, v.CreatedAt.Local().Format("Jan 2 15:04"))
					}
				}
				fmt.Println(out.Summary)
			})
		}}
	c.Flags().StringVar(&in.ProjectID, "project", "", "one project's")
	c.Flags().StringVar(&in.Look, "look", "", "only ones with this look")
	c.Flags().StringVar(&in.AssetID, "clip", "", "the takes of the generation that made this clip")
	c.Flags().IntVar(&in.Limit, "limit", 0, "at most this many (default 100)")
	addDetails(c, &details)
	return c
}

func genShowCmd() *cobra.Command {
	var details bool
	c := &cobra.Command{Use: "show <generation or clip>", Short: "How a generated video was made: prompt, look, pictures, settings, cost, safety, lineage, where it is used", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "generate.show", generate.GenIDIn{ID: args[0]}, func(o any) {
				v := o.(*generate.GenView)
				fmt.Printf("%s %s\n", v.Summary, costWords(*v))
				printGenHint(v)
				fmt.Printf("Prompt: %s\n", v.Prompt)
				if v.Look != nil {
					fmt.Printf("Look: %s\n", v.Look.Title)
				}
				fmt.Printf("Made with %s (%s), %d seconds, %s, %s.\n", v.ProviderTitle, v.ModelTitle, v.Params.Seconds, videogen.ResolutionLabel(v.Params.Resolution), map[string]string{"9:16": "tall", "16:9": "wide"}[v.Params.AspectRatio])
				if v.ConsentRevoked {
					fmt.Println("A consent it relied on has been withdrawn since.")
				}
				for _, u := range v.UsedIn {
					fmt.Println("Used: " + u.Text)
				}
				if v.Parent != nil {
					fmt.Printf("Came from: %s\n", v.Parent.Prompt)
				}
				if n := len(v.Children); n > 0 {
					fmt.Printf("Other takes and versions: %d\n", n)
				}
				if details {
					printGenDetails(v)
				}
			})
		}}
	addDetails(c, &details)
	return c
}

func genCancelCmd() *cobra.Command {
	return &cobra.Command{Use: "cancel <generation or clip>", Short: "Stop a generated video that is waiting or being made", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "generate.cancel", generate.GenIDIn{ID: args[0]}, func(o any) { fmt.Println(o.(*generate.GenCancelOut).Summary) })
		}}
}

func genSettingsCmd() *cobra.Command {
	var in generate.GenSettingsIn
	var details bool
	c := &cobra.Command{Use: "settings", Short: "The models and their prices, draft and final quality, today's limit and a project's budget", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "generate.settings", in, func(o any) { printGenSettings(o.(*generate.GenSettingsOut), details) })
		}}
	c.Flags().StringVar(&in.ProjectID, "project", "", "also show this project's budget")
	addDetails(c, &details)
	return c
}

func printGenSettings(s *generate.GenSettingsOut, details bool) {
	fmt.Println(s.Summary)
	for _, m := range s.Models {
		mark := ""
		if m.Default {
			mark = " (used for new shots)"
		}
		fmt.Printf("%s%s\n", m.Describe, mark)
		var prices []string
		for _, r := range m.Resolutions {
			prices = append(prices, fmt.Sprintf("%s %s a second", videogen.ResolutionLabel(r), moneyWords(m.PerSecondUSD[r])))
		}
		fmt.Println("  " + strings.Join(prices, ", "))
	}
	fmt.Printf("Drafts: %s. Final quality: %s.\n", videogen.ResolutionLabel(s.DraftResolution), videogen.ResolutionLabel(s.FinalResolution))
	cap := fmt.Sprintf("Today's limit for the team: %s", moneyWords(s.DailyCapUSD))
	if s.DailyCapDefault {
		cap += " (the default)"
	}
	fmt.Println(cap + ".")
	if s.Budget.Project != nil {
		switch {
		case s.ProjectBudgetUSD == nil:
			fmt.Println("This project has no budget of its own (today's limit still applies).")
		case s.ProjectBudgetDefault:
			fmt.Printf("This project's budget: %s (the default).\n", moneyWords(*s.ProjectBudgetUSD))
		default:
			fmt.Printf("This project's budget: %s.\n", moneyWords(*s.ProjectBudgetUSD))
		}
	}
	if !s.KeyReady {
		fmt.Println("Add the Google Gemini key on this computer in Settings, Integrations.")
	}
	if details {
		fmt.Printf("    provider %s, models %v, provider's own limits per 10 minutes by tier %v\n", s.Provider, s.Model, s.SpendLimitsPer10Minutes)
	}
}

func genConfigureCmd() *cobra.Command {
	var in generate.GenConfigureIn
	var cap, budget float64
	var details bool
	c := &cobra.Command{Use: "configure", Short: "Change the model, draft and final quality, today's limit or a project's budget (people only)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if !isInteractive() {
				return fail(c, apperr.HumanOnly, "Only a person changes what generated video may cost, in the dashboard or in their own terminal.", "Open Settings, Integrations in the dashboard.")
			}
			if c.Flags().Changed("daily-cap") {
				in.DailyCapUSD = &cap
			}
			if c.Flags().Changed("budget") {
				in.BudgetUSD = &budget
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "generate.configure", in, ops.Caller{Human: true}, func(o any) { printGenSettings(o.(*generate.GenSettingsOut), details) })
		}}
	fl := c.Flags()
	fl.Float64Var(&cap, "daily-cap", 0, "the most the team may spend on generated video in a day (US dollars, 1 to 1000)")
	fl.StringVar(&in.ProjectID, "project", "", "the project whose budget to set")
	fl.Float64Var(&budget, "budget", 0, "the project's budget in US dollars (0: no project limit)")
	fl.BoolVar(&in.ClearBudget, "clear-budget", false, "put the project back on the default budget")
	fl.StringVar(&in.Model, "model", "", "the video model for new shots")
	fl.StringVar(&in.DraftResolution, "draft", "", "draft quality: 360p or 720p")
	fl.StringVar(&in.FinalResolution, "final", "", "final quality: 1080p or 4k")
	addDetails(c, &details)
	return c
}

// generateLinked keeps the generate package's ops registered in every binary.
var _ = generate.Provider
