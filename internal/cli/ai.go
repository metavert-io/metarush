package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/localmodels"
	"github.com/metavert-io/metarush/internal/ops"
)

func aiCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "ai", Short: "Make pictures, AI speech, sound effects, music, voice changes, dubs, voice copies and cloud transcripts with AI services — priced first",
		Long: `AI services make media for your projects with the keys this computer already has (Google
Gemini, ElevenLabs). Every request is priced first, with up to two other models (cheaper, better)
and the model's licence and data terms, and goes ahead only with the price you agreed to (--max-cost,
or a yes when asked); the money counts against the project's budget and today's limit, like generated
video. Pictures land in the resources library, sound and dubs in Sources, a cloud transcript replaces
a clip's words (local transcription stays the default) — all marked as AI-generated. A voice copy needs
the person's consent (metarush consent) and is made only by a person.

  metarush ai models --kind music
  metarush ai estimate music "calm acoustic guitar for a cooking video" --seconds 30 --project <id>
  metarush ai make picture "a watercolour kite over a green hill" --size 2K --aspect 16:9 --project <id> --max-cost 0.06
  metarush ai make picture "make the sky orange" --picture kite --no-people --project <id>
  metarush ai make sound-effect "a wooden door creaks open" --seconds 3 --project <id>
  metarush ai make voice "Welcome to the garden." --voice Kore --project <id>
  metarush ai make voice-change --clip <clip> --voice <voice> --from 12 --to 40
  metarush ai make dubbing --clip <clip> --into es
  metarush ai make transcription --clip <clip>
  metarush ai make presenter --presenter ada --clip <clip>          (or --line 3: a narration line of the project's cut)

Some models run on this computer instead — free, no key, nothing leaves it: the local recognizers,
offline voices, a picture's background taken away, pictures made larger. A person sets up the ones
that need a download, once (metarush ai local).

  metarush ai local
  metarush ai local setup kokoro-v1
  metarush ai make voice "Welcome to the garden." --model local/kokoro-v1 --voice af_heart --project <id>
  metarush ai make transcription --clip <clip> --model local/parakeet-v2
  metarush ai make background-removal --picture logo --project <id>
  metarush ai make upscale --picture logo --scale 4 --project <id>
  metarush ai voices copy --person "Ada Lovelace" --name "Ada narration" --sample <clip> --consent-clip <clip>
  metarush ai voices list
  metarush ai rules set --project <id> --forbid elevenlabs --pin music=gemini/lyria-3.5
  metarush ai rules show --project <id>
  metarush ai list --project <id>`}
	cmd.AddCommand(aiModelsCmd(), aiMakeCmd(true), aiMakeCmd(false), aiListCmd(), aiShowCmd(), aiCancelCmd(), aiVoicesCmd(), aiRulesCmd(), aiLocalCmd())
	return cmd
}

func aiModelsCmd() *cobra.Command {
	var in aiwork.ModelsIn
	var details bool
	c := &cobra.Command{Use: "models", Short: "The models for each kind of AI work, with prices, licence and data terms, and what a project's Content Settings allow", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "ai.models", in, func(o any) { printAIModels(o.(*aiwork.ModelsOut), details) })
		}}
	c.Flags().StringVar(&in.Kind, "kind", "", "one kind: video, picture, voice, voice-copy, voice-change, sound-effect, music, dubbing, transcription, presenter, presenter-make, background-removal, upscale or lip-sync")
	c.Flags().StringVar(&in.ProjectID, "project", "", "say what this project's Content Settings allow")
	addDetails(c, &details)
	return c
}

func printAIModels(o *aiwork.ModelsOut, details bool) {
	for _, k := range o.Kinds {
		fmt.Println(k.Title + ":")
		for _, m := range k.Models {
			line := fmt.Sprintf("  %s (%s)", m.Title, m.ProviderTitle)
			if m.Rate != "" {
				line += ", " + m.Rate
			}
			var marks []string
			if m.Default {
				marks = append(marks, "the usual one")
			}
			if m.PinnedFor {
				marks = append(marks, "chosen in Content Settings")
			}
			if !m.Allowed {
				marks = append(marks, "not allowed by Content Settings")
			}
			switch {
			case m.GatewayOnly():
				marks = append(marks, "only through "+m.OnlyVia+", a gateway MetaRush doesn't use")
			case !m.Built:
				marks = append(marks, "not available yet")
			}
			if m.Retired {
				marks = append(marks, "retired")
			}
			switch {
			case m.NotHere != "":
				marks = append(marks, "not set up on this computer")
			case !m.KeyReady:
				marks = append(marks, "no key on this computer")
			}
			if len(marks) > 0 {
				line += " — " + strings.Join(marks, ", ")
			}
			fmt.Println(line)
			if details {
				fmt.Printf("      %s · rank %d · licence: %s (commercial %s, broadcast %s; checked %s)\n", m.Ref(), m.Rank, m.Licence.Summary, m.Licence.Commercial, m.Licence.Broadcast, m.Licence.Checked)
			}
		}
	}
	if details {
		for _, p := range o.Providers {
			fmt.Printf("%s: %s (checked %s)\n", p.Title, p.Data.Summary, p.Data.Checked)
		}
	}
	fmt.Println(o.Summary)
}

// aiFlags are the flags of ai make and ai estimate.
type aiFlags struct {
	in        aiwork.MakeIn
	maxCost   float64
	from, to  float64
	wait      bool
	details   bool
	promptArg string
}

func (f *aiFlags) add(c *cobra.Command, estimate bool) {
	fl := c.Flags()
	o := &f.in.Options
	fl.StringVar(&f.in.ProjectID, "project", "", "the project it is for (its budget, its Content Settings, where it lands)")
	fl.StringVar(&f.in.Model, "model", "", "provider/model (default: the one Content Settings choose, else the usual one; see metarush ai models)")
	fl.StringVar(&f.promptArg, "prompt", "", "the words (instead of the argument)")
	fl.StringSliceVar(&f.in.Pictures, "picture", nil, "a picture from the resources library to change or refer to (name or name@version)")
	fl.StringVar(&f.in.Clip, "clip", "", "the clip to change the voice of, dub, write down or lip-sync")
	fl.StringVar(&f.in.Sound, "sound", "", "lip sync: the clip whose sound the lips are matched to")
	fl.Float64Var(&f.from, "from", 0, "where the part of the clip starts (seconds)")
	fl.Float64Var(&f.to, "to", 0, "where it ends (seconds)")
	fl.StringVar(&o.Size, "size", "", "pictures: 1K, 2K or 4K")
	fl.StringVar(&o.AspectRatio, "aspect", "", "pictures: the shape (16:9, 1:1, 9:16…)")
	fl.IntVar(&o.Scale, "scale", 0, "upscale: how many times larger (2 or 4)")
	fl.Float64Var(&o.Seconds, "seconds", 0, "sound effects and music: how long")
	fl.BoolVar(&o.Loop, "loop", false, "a sound effect that loops")
	fl.BoolVar(&o.Instrumental, "instrumental", false, "music without singing")
	fl.StringVar(&o.Voice, "voice", "", "voices and voice change: the voice (its name or ID)")
	fl.StringVar(&o.Style, "style", "", "voices: how to say it (cheerful, a whisper…)")
	fl.StringVar(&o.Language, "language", "", "the language spoken (en, es…; default: found out)")
	fl.StringVar(&o.TargetLanguage, "into", "", "dubbing: the language to dub into (es, fr, de…)")
	fl.IntVar(&o.Speakers, "speakers", 0, "dubbing: how many people speak (default: found out)")
	fl.BoolVar(&o.OwnVoices, "own-voices", false, "dubbing: in the speakers' own voices (each needs consent) instead of similar voices")
	fl.BoolVar(&f.in.NoPeople, "no-people", false, "you say the pictures show no real people")
	fl.StringVar(&f.in.Presenter, "presenter", "", "presenters: who reads it (a presenter's short name, or stock:<id>; default for a dubbed presenter clip: the same presenter)")
	fl.IntVar(&f.in.Line, "line", 0, "presenters: read this narration line of the project's cut (1 is the first) instead of a clip")
	fl.StringVar(&f.in.Branch, "cut", "", "presenters: the cut whose narration line it is (default: the main one)")
	fl.StringVar(&o.Background, "background", "", "presenters: a colour behind the presenter (#rrggbb, or green), for one filmed against a green screen")
	if !estimate {
		fl.Float64Var(&f.maxCost, "max-cost", 0, "the most you agree to pay, in US dollars (from the estimate)")
		fl.BoolVar(&f.in.DryRun, "dry-run", false, "price and check it only")
		fl.BoolVar(&f.wait, "wait", false, "make it here and wait until it's ready")
	}
	addDetails(c, &f.details)
}

func (f *aiFlags) prepare(c *cobra.Command, args []string) {
	if len(args) > 0 {
		f.in.Kind = args[0]
		f.in.Prompt = strings.TrimSpace(strings.Join(args[1:], " "))
	}
	if f.promptArg != "" {
		f.in.Prompt = f.promptArg
	}
	if c.Flags().Changed("from") {
		f.in.From = &f.from
	}
	if c.Flags().Changed("to") {
		f.in.To = &f.to
	}
	if c.Flags().Changed("max-cost") {
		f.in.MaxUSD = &f.maxCost
	}
}

func aiMakeCmd(estimate bool) *cobra.Command {
	f := &aiFlags{}
	use, short := "make <kind> [words]", "Make a picture, speech, a sound effect, music, a voice change, a dub or a cloud transcript: priced first, made with the price you agree to"
	if estimate {
		use, short = "estimate <kind> [words]", "Price AI work and check it may be made (the model, alternatives, consent, budget, this computer's key); nothing is sent or spent"
	}
	c := &cobra.Command{Use: use, Short: short, Args: cobra.MinimumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		f.prepare(c, args)
		if estimate {
			return runOp(c, "ai.estimate", f.in, func(o any) { printAIEstimate(o.(*aiwork.AIEstimate), f.details) })
		}
		return runAI(c, "ai.make", &f.in, &f.in.MaxUSD, f.in.DryRun, f.wait, f.details, ops.Caller{})
	}}
	f.add(c, estimate)
	return c
}

// runAI asks for AI work: with no agreed price, a person at a terminal sees
// the estimate and says yes; otherwise the op refuses plainly.
func runAI(c *cobra.Command, op string, in any, maxUSD **float64, dry, wait, details bool, caller ops.Caller) error {
	a, err := openApp(c, "cli")
	if err != nil {
		return err
	}
	defer a.Close()
	ctx, cancel := signalCtx(c.Context())
	defer cancel()
	if *maxUSD == nil && !dry && estimateFirstHere(c, a) {
		raw, _ := json.Marshal(in)
		var probe map[string]any
		_ = json.Unmarshal(raw, &probe)
		probe["dryRun"] = true
		b, _ := json.Marshal(probe)
		res, err := ops.Call(ctx, a, op, b, caller)
		if err != nil {
			return failErr(c, err)
		}
		est := res.(*aiwork.MakeOut).Estimate
		if !est.Here || est.USD > 0 || !est.Allowed {
			// Free work on this computer has no price to agree (D278).
			printAIEstimate(&est, details)
			if !est.Allowed {
				return fail(c, apperr.Conflict, "Nothing was made.", "")
			}
			if !askYes(fmt.Sprintf("Make it for about %s", moneyWords(est.USD)), false) {
				return fail(c, apperr.Canceled, "Nothing was made or spent.", "")
			}
			v := est.USD
			*maxUSD = &v
		}
	}
	b, _ := json.Marshal(in)
	res, err := ops.Call(ctx, a, op, b, caller)
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
	out := res.(*aiwork.MakeOut)
	if !wait || out.DryRun || out.JobID == "" {
		return emit(c, out, func() {
			if out.DryRun {
				printAIEstimate(&out.Estimate, details)
				fmt.Println("Nothing was sent or spent.")
				return
			}
			fmt.Println(out.Summary)
			if details {
				fmt.Printf("    work %s  job %s\n", out.Work.ID, out.JobID)
			} else {
				fmt.Println("(Follow it with metarush activity, or add --wait.)")
			}
		})
	}
	if !jsonMode(c) {
		fmt.Println(out.Summary)
	}
	jobs, err := waitJobs(c, a, []string{out.JobID}, 60*time.Minute, jsonMode(c))
	if err != nil {
		return failErr(c, err)
	}
	v, err := ops.CallTyped[aiwork.WorkView](ctx, a, "ai.show", aiwork.IDIn{ID: out.Work.ID}, ops.Caller{})
	if err != nil {
		return failErr(c, err)
	}
	err = emit(c, map[string]any{"run": out, "work": v, "jobs": jobs}, func() { printWork(v, details) })
	if err == nil && v.State != aiwork.StateDone {
		return &exitError{code: 1}
	}
	return err
}

func printAIEstimate(e *aiwork.AIEstimate, details bool) {
	fmt.Println(e.Summary)
	for _, in := range e.Inputs {
		fmt.Println("  sends " + in)
	}
	switch e.Choice {
	case "content-settings":
		fmt.Println("  the model the project's Content Settings choose")
	case "default":
		fmt.Println("  MetaRush's usual model for this")
	}
	for _, alt := range e.Alternatives {
		fmt.Printf("  %s: %s\n", map[string]string{"cheaper": "cheaper", "better": "better"}[alt.Why], alt.Summary)
	}
	if b := e.Budget.Project; b != nil && b.LimitUSD != nil {
		fmt.Printf("  this project: %s of %s spent\n", moneyWords(b.SpentUSD+b.HeldUSD), moneyWords(*b.LimitUSD))
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
		fmt.Printf("    %s/%s (%s), task %s, price table %s, choice %s\n", e.Provider, e.Model, e.Stage, e.Task, e.PriceTable, e.Choice)
		fmt.Printf("    licence: commercial %s, broadcast %s (%s, checked %s)\n", e.Licence.Commercial, e.Licence.Broadcast, e.Licence.Source, e.Licence.Checked)
		fmt.Printf("    data: %s\n", e.Data)
	}
}

func printWork(v *aiwork.WorkView, details bool) {
	fmt.Println(v.Summary + " " + workCost(v))
	for _, o := range v.Outputs {
		fmt.Printf("  %s\n", theirs(o.Title))
	}
	if o := v.Outcome; o != nil && o.Hint != "" && (v.State == aiwork.StateFailed || v.State == aiwork.StateRefused) {
		fmt.Println(o.Hint)
	}
	if v.ConsentRevoked {
		fmt.Println("  A consent it relied on was withdrawn since.")
	}
	if !details {
		return
	}
	fmt.Printf("    work %s (%s, %s), %s/%s (%s), price table %s\n", v.ID, v.Kind, v.Operation, v.Provider, v.Model, v.Stage, v.PriceTable)
	if v.Prompt != "" {
		fmt.Printf("    words: %q\n", v.Prompt)
	}
	if v.Options != nil {
		b, _ := json.Marshal(v.Options)
		fmt.Printf("    settings %s\n", b)
	}
	for _, r := range v.References {
		fmt.Printf("    %s %s sha256 %s\n", r.Role, r.Ref, r.SHA256)
	}
	fmt.Printf("    request sha256 %s\n", v.RequestHash)
	if v.Choice != nil {
		fmt.Printf("    chosen: %s; offered %v\n", v.Choice.By, v.Choice.Alternatives)
	}
	if v.Terms != nil {
		fmt.Printf("    licence: %s (commercial %s, broadcast %s, checked %s)\n", v.Terms.Licence, v.Terms.Commercial, v.Terms.Broadcast, v.Terms.Checked)
	}
	if v.Ticket != nil {
		fmt.Printf("    provider request %s, sent %s\n", v.Ticket.ID, v.Ticket.SubmittedAt.Local().Format(time.RFC3339))
	}
	fmt.Printf("    estimate $%.4f", v.Estimate.USD)
	if v.Outcome != nil && v.Outcome.Actual != nil {
		fmt.Printf(", counted $%.4f (%s)", v.Outcome.Actual.USD, v.Outcome.Actual.Basis)
	}
	fmt.Println()
	if o := v.Outcome; o != nil {
		for _, out := range o.Outputs {
			fmt.Printf("    made %s%s%s%s sha256 %s (%s)\n", out.AssetID, out.ResourceID, out.VoiceID, out.PresenterID, out.SHA256, out.Delivery)
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

func workCost(v *aiwork.WorkView) string {
	switch {
	case v.Settled != nil:
		return "(" + moneyWords(v.Settled.USD) + ")"
	case v.State == aiwork.StateMaking || v.State == aiwork.StateWaiting || v.State == aiwork.StateSending:
		return "(about " + moneyWords(v.Estimate.USD) + ")"
	}
	return ""
}

func aiListCmd() *cobra.Command {
	var in aiwork.ListIn
	var details bool
	c := &cobra.Command{Use: "list", Short: "AI work with what it cost (pictures, speech, sound effects, music, dubs, transcripts, voice copies)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "ai.list", in, func(o any) {
				out := o.(*aiwork.ListOut)
				for i := range out.Work {
					printWork(&out.Work[i], details)
				}
				fmt.Println(out.Summary)
			})
		}}
	c.Flags().StringVar(&in.Kind, "kind", "", "one kind of work")
	c.Flags().StringVar(&in.ProjectID, "project", "", "one project's")
	c.Flags().IntVar(&in.Limit, "limit", 0, "at most this many (default 100)")
	addDetails(c, &details)
	return c
}

func aiShowCmd() *cobra.Command {
	var details bool
	c := &cobra.Command{Use: "show <work, clip or picture>", Short: "How a piece of AI work was made: the model and why, what was sent, licence and data terms, cost, consent and what it made", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "ai.show", aiwork.IDIn{ID: args[0]}, func(o any) { printWork(o.(*aiwork.WorkView), details) })
		}}
	addDetails(c, &details)
	return c
}

func aiCancelCmd() *cobra.Command {
	return &cobra.Command{Use: "cancel <work>", Short: "Stop AI work that is waiting or being made", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "ai.cancel", aiwork.IDIn{ID: args[0]}, func(o any) { fmt.Println(o.(*aiwork.CancelOut).Summary) })
		}}
}

func aiVoicesCmd() *cobra.Command {
	c := &cobra.Command{Use: "voices", Short: "Voice copies: list them, copy a person's voice with their consent, withdraw one"}
	c.AddCommand(aiVoicesListCmd(), aiVoiceCopyCmd(), aiVoiceRemoveCmd())
	return c
}

func aiVoicesListCmd() *cobra.Command {
	var in aiwork.VoicesIn
	c := &cobra.Command{Use: "list", Short: "The voice copies MetaRush made, whose voice each is and whether it may still be used", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "ai.voices", in, func(o any) {
				out := o.(*aiwork.VoicesOut)
				for _, v := range out.Voices {
					fmt.Println("  " + v.Summary)
				}
				fmt.Println(out.Summary)
			})
		}}
	c.Flags().StringVar(&in.Person, "person", "", "one person's voice copies")
	return c
}

func aiVoiceCopyCmd() *cobra.Command {
	var in aiwork.CopyIn
	var maxCost, from, to, cfrom, cto float64
	var wait, details bool
	c := &cobra.Command{Use: "copy", Short: "Copy a person's voice with their consent (and, where the service wants it, them reading its consent script) — people only, priced first", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			fl := c.Flags()
			if fl.Changed("max-cost") {
				in.MaxUSD = &maxCost
			}
			for _, x := range []struct {
				name string
				v    *float64
				dst  **float64
			}{{"from", &from, &in.From}, {"to", &to, &in.To}, {"consent-from", &cfrom, &in.ConsentFrom}, {"consent-to", &cto, &in.ConsentTo}} {
				if fl.Changed(x.name) {
					*x.dst = x.v
				}
			}
			if !isInteractive() {
				return fail(c, apperr.HumanOnly, "Only a person can copy someone's voice, in the dashboard or in their own terminal.", "Open the Generate panel in the dashboard, or run this command yourself.")
			}
			return runAI(c, "ai.voices.copy", &in, &in.MaxUSD, in.DryRun, wait, details, ops.Caller{Human: true})
		}}
	fl := c.Flags()
	fl.StringVar(&in.Person, "person", "", "whose voice (a name or ID from People); their consent must be recorded")
	fl.StringVar(&in.Name, "name", "", "the new voice's name")
	fl.StringVar(&in.Sample, "sample", "", "a clip with clean speech of the voice")
	fl.Float64Var(&from, "from", 0, "where the sample starts in the clip (seconds)")
	fl.Float64Var(&to, "to", 0, "where it ends")
	fl.StringVar(&in.ConsentClip, "consent-clip", "", "a clip of the same person reading the service's consent script (when it wants one)")
	fl.Float64Var(&cfrom, "consent-from", 0, "where the reading starts")
	fl.Float64Var(&cto, "consent-to", 0, "where it ends")
	fl.StringVar(&in.Language, "language", "", "the language of the consent script (default en-US)")
	fl.StringVar(&in.Model, "model", "", "provider/model (default: the usual one)")
	fl.StringVar(&in.ProjectID, "project", "", "the project it is for")
	fl.Float64Var(&maxCost, "max-cost", 0, "the most you agree to pay, in US dollars")
	fl.BoolVar(&in.DryRun, "dry-run", false, "price and check it only")
	fl.BoolVar(&wait, "wait", false, "make it here and wait")
	addDetails(c, &details)
	return c
}

func aiVoiceRemoveCmd() *cobra.Command {
	var in aiwork.VoiceRemoveIn
	c := &cobra.Command{Use: "remove <voice copy>", Short: "Withdraw a voice copy: deleted at the provider, and nothing more is said with it (people only)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ID = args[0]
			return peopleOnly(c, "ai.voices.remove", in, "withdraw a voice copy", func(o any) { fmt.Println(o.(*aiwork.VoiceRemoveOut).Summary) })
		}}
	c.Flags().StringVar(&in.Note, "note", "", "why, in your words")
	return c
}

func aiRulesCmd() *cobra.Command {
	c := &cobra.Command{Use: "rules", Short: "Which AI services a project's work may go to (the AI services part of its Content Settings)"}
	c.AddCommand(aiRulesShowCmd(), aiRulesSetCmd())
	return c
}

func aiRulesShowCmd() *cobra.Command {
	var in aiwork.RulesIn
	c := &cobra.Command{Use: "show", Short: "Which AI services a project's work may go to", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "ai.rules", in, func(o any) { fmt.Println(o.(*aiwork.RulesOut).Summary) })
		}}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project (default: the default settings)")
	return c
}

func aiRulesSetCmd() *cobra.Command {
	var in aiwork.RulesSetIn
	var none, anyOK bool
	var pins []string
	var note string
	c := &cobra.Command{Use: "set", Short: "Choose which AI services the work of projects using these Content Settings may go to: none, never some providers, or a model per kind of work (people only)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			fl := c.Flags()
			switch {
			case none && anyOK:
				return fail(c, apperr.InvalidInput, "Say --none or --any, not both.", "")
			case none:
				in.None = &none
			case anyOK:
				f := false
				in.None = &f
			}
			for _, p := range pins {
				kind, ref, ok := strings.Cut(p, "=")
				if !ok {
					return fail(c, apperr.InvalidInput, "--pin takes kind=provider/model, like music=gemini/lyria-3.5.", "")
				}
				if in.Pin == nil {
					in.Pin = map[string]string{}
				}
				in.Pin[strings.TrimSpace(kind)] = strings.TrimSpace(ref)
			}
			if fl.Changed("note") {
				in.Note = &note
			}
			return peopleOnly(c, "ai.rules.set", in, "change which AI services a project may use", func(o any) { fmt.Println(o.(*aiwork.RulesOut).Summary) })
		}}
	fl := c.Flags()
	fl.StringVar(&in.ProjectID, "project", "", "the project whose own Content Settings change (default: the default's)")
	fl.BoolVar(&none, "none", false, "nothing goes to any AI service")
	fl.BoolVar(&anyOK, "any", false, "AI services are allowed again (undoes --none)")
	fl.StringSliceVar(&in.Forbid, "forbid", nil, "never use this provider (gemini, elevenlabs)")
	fl.StringSliceVar(&in.Allow, "allow", nil, "use this provider again")
	fl.StringSliceVar(&pins, "pin", nil, "use this model for a kind of work: kind=provider/model")
	fl.StringSliceVar(&in.Unpin, "unpin", nil, "go back to the usual model for a kind of work")
	fl.StringSliceVar(&in.ForbidKinds, "forbid-kind", nil, "never make this kind of work for these projects (presenter, music…)")
	fl.StringSliceVar(&in.AllowKinds, "allow-kind", nil, "allow this kind of work again")
	fl.StringVar(&note, "note", "", "why, in your words (the client's contract)")
	fl.BoolVar(&in.Clear, "clear", false, "take every rule off")
	return c
}

func aiLocalCmd() *cobra.Command {
	var in aiwork.LocalIn
	var details bool
	c := &cobra.Command{Use: "local", Short: "The AI models that run on this computer — free, no key, nothing leaves it — and whether each is set up", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "ai.local", in, func(o any) { printAILocal(o.(*aiwork.LocalOut), details) })
		}}
	c.Flags().StringVar(&in.Kind, "kind", "", "one kind: voice, transcription, background-removal or upscale")
	addDetails(c, &details)
	c.AddCommand(aiLocalSetupCmd())
	return c
}

func aiLocalSetupCmd() *cobra.Command {
	var details bool
	c := &cobra.Command{Use: "setup <model>", Short: "Set up an AI model that runs on this computer: its pinned files are downloaded once and checked (people only)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if !jsonMode(c) {
				fmt.Println("Downloading and checking the files it needs; this can take a few minutes…")
			}
			return runOp(c, "ai.local.setup", aiwork.LocalSetupIn{Model: args[0]}, func(o any) {
				out := o.(*aiwork.LocalSetupOut)
				fmt.Println(out.Summary)
				if details && len(out.Fetched) > 0 {
					fmt.Printf("    downloaded %s\n", strings.Join(out.Fetched, ", "))
				}
			})
		}}
	addDetails(c, &details)
	return c
}

func printAILocal(o *aiwork.LocalOut, details bool) {
	for _, m := range o.Models {
		state := "ready"
		if !m.Ready {
			state = "not set up"
			var size int64
			for _, d := range m.Downloads {
				if !d.Ready {
					size += d.Bytes
				}
			}
			if size > 0 {
				state += fmt.Sprintf(" (setting it up downloads %s once)", localmodels.Size(size))
			}
		}
		fmt.Printf("%s: %s — %s\n", m.KindTitle, m.Title, state)
		if !m.Ready && m.NotHere != "" {
			fmt.Println("    " + m.NotHere)
		}
		if details {
			fmt.Printf("    %s · licence: %s\n", m.Ref, m.Licence.Summary)
			for _, d := range m.Downloads {
				fmt.Printf("    %s: %s, %s, %s (sha256 %s)\n", d.ID, d.Size, d.Licence, d.URL, d.SHA256)
			}
		}
	}
	fmt.Println(o.Summary)
}
