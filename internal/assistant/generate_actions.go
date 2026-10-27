package assistant

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/videogen"
)

// Generated video in the panel (D124, D126): Claude asks with `metarush
// generate …` and the request becomes a card with its price, built here
// (never from the browser): the person confirms it, so nothing is ever
// generated automatically. Looks, consent and spending limits are never
// offered to the assistant, not even as cards: a person does those.

// generateCommands are the commands handled here.
var generateCommands = map[string]bool{"generate shot": true, "generate again": true, "generate extend": true, "generate edit": true, "generate cancel": true,
	"generate configure": true, "looks create": true, "looks update": true, "looks approve": true, "looks revoke": true, "looks test": true,
	"consent record": true, "consent revoke": true}

// generateNever are what only a person does, in the dashboard or their own terminal.
var generateNever = map[string]string{
	"generate configure": "What generated video may cost is set by the person in Settings, Integrations.",
	"looks create":       "A look of someone is made by the person on the People page, and a person's look needs their consent.",
	"looks update":       "A look is changed by the person on the People page.",
	"looks approve":      "A person approves a look on the People page, after looking at its test shots.",
	"looks revoke":       "A person withdraws a look on the People page.",
	"looks test":         "A look's test shots are made by the person on the People page: they show someone's likeness and cost money.",
	"consent record":     "Consent to use someone's likeness is given by that person and recorded by a person on the People page.",
	"consent revoke":     "Consent is withdrawn by a person on the People page.",
}

func (m *Manager) describeGenerate(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	if why, never := generateNever[call.Path]; never {
		incomplete(a, "", why)
		return
	}
	ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()
	origin := "assistant:" + c.id
	arg := first(call.Args)
	num := func(name string) int {
		v, _ := strconv.Atoi(call.Flags[name])
		return v
	}
	switch call.Path {
	case "generate shot":
		in := generate.GenShotIn{ProjectID: call.Flags["project"], Prompt: clip(strings.TrimSpace(firstNonEmpty(call.Flags["prompt"], strings.Join(call.Args, " "))), 4000),
			Look: clip(call.Flags["look"], 80), Seconds: num("seconds"), Quality: call.Flags["quality"], Resolution: call.Flags["resolution"], Model: clip(call.Flags["model"], 120),
			Images: listFlag(call, "image"), NoPeople: hasBool(call, "no-people"), Takes: num("takes"), Origin: origin}
		if in.ProjectID == "" {
			in.ProjectID = c.projectID()
		}
		switch strings.ToLower(call.Flags["aspect"]) {
		case "9:16", "tall", "portrait", "vertical":
			in.AspectRatio = "9:16"
		case "16:9", "wide", "landscape":
			in.AspectRatio = "16:9"
		}
		for _, fr := range []struct {
			flag string
			dst  **generate.FrameRef
		}{{"from", &in.From}, {"to", &in.To}} {
			if v := call.Flags[fr.flag]; v != "" && !strings.HasPrefix(v, "ast_") {
				*fr.dst = &generate.FrameRef{Picture: clip(v, 200)} // a picture from the resources library
			} else if v != "" {
				id, at, ok := strings.Cut(v, "@")
				t, err := clockSecs(at)
				if !ok || err != nil || !ids.Valid(ids.Asset, id) {
					incomplete(a, "Make a generated shot", "Claude gave a frame MetaRush can't read; a frame is a clip and a time.")
					return
				}
				*fr.dst = &generate.FrameRef{AssetID: id, At: t}
			}
		}
		if in.ProjectID == "" || in.Prompt == "" {
			incomplete(a, "Make a generated shot", "Claude did not say which project or what the shot shows.")
			return
		}
		est, err := callOp[generate.GenEstimate](ectx, m, "generate.estimate", in)
		if err != nil {
			unavailable(a, "Make a generated shot", "MetaRush could not price it: "+firstLine(apperr.From(err).Message))
			return
		}
		in.MaxUSD = &est.USD
		m.generateCard(a, est, "generate.shot", in, in.Prompt)
	case "generate again", "generate extend", "generate edit":
		var op string
		var in any
		switch call.Path {
		case "generate again":
			op, in = "generate.again", &generate.GenAgainIn{ID: arg, Takes: num("takes"), Quality: call.Flags["quality"], Resolution: call.Flags["resolution"], DryRun: true, Origin: origin}
		case "generate extend":
			e := &generate.GenExtendIn{ID: arg, Seconds: num("seconds"), Prompt: clip(call.Flags["prompt"], 4000), ProjectID: call.Flags["project"], Quality: call.Flags["quality"],
				Resolution: call.Flags["resolution"], NoPeople: hasBool(call, "no-people"), DryRun: true, Origin: origin}
			if v, err := strconv.ParseFloat(call.Flags["from"], 64); err == nil {
				e.From = &v
			}
			op, in = "generate.extend", e
		default:
			e := &generate.GenEditIn{ID: arg, Instruction: clip(strings.Join(call.Args[min(1, len(call.Args)):], " "), 4000), ProjectID: call.Flags["project"],
				Quality: call.Flags["quality"], Resolution: call.Flags["resolution"], NoPeople: hasBool(call, "no-people"), DryRun: true, Origin: origin}
			if v, err := strconv.ParseFloat(call.Flags["from"], 64); err == nil {
				e.From = &v
			}
			if v, err := strconv.ParseFloat(call.Flags["to"], 64); err == nil {
				e.To = &v
			}
			op, in = "generate.edit", e
		}
		if !ids.Valid(ids.Generation, arg) && !ids.Valid(ids.Asset, arg) {
			incomplete(a, "Make a generated shot", "Claude did not name a generated video or a clip.")
			return
		}
		out, err := callOp[generate.GenRunOut](ectx, m, op, in)
		if err != nil {
			unavailable(a, "Make a generated shot", "MetaRush could not price it: "+firstLine(apperr.From(err).Message))
			return
		}
		est := out.Estimate
		switch v := in.(type) {
		case *generate.GenAgainIn:
			v.DryRun, v.MaxUSD = false, &est.USD
		case *generate.GenExtendIn:
			v.DryRun, v.MaxUSD = false, &est.USD
		case *generate.GenEditIn:
			v.DryRun, v.MaxUSD = false, &est.USD
		}
		m.generateCard(a, &est, op, in, "")
	case "generate cancel":
		if !ids.Valid(ids.Generation, arg) && !ids.Valid(ids.Asset, arg) {
			incomplete(a, "Stop a generated video", "Claude did not name a generated video.")
			return
		}
		a.Title = "Stop the generated video"
		a.Details = []string{"Google Gemini is asked to stop too; what it already made may still be charged"}
		setInput(a, "generate.cancel", generate.GenIDIn{ID: arg})
	}
}

// generateCard is a priced card: the person sees what it makes and what it
// costs; it can't be confirmed when the budget, the key or consent is missing.
func (m *Manager) generateCard(a *AssistantAction, est *generate.GenEstimate, op string, in any, prompt string) {
	// "an 8-second shot", "a 6-second shot": the article follows how the number is said.
	shot := fmt.Sprintf("%s %d-second shot", plainwords.AOrAn(est.Seconds), est.Seconds)
	what := shot
	switch op {
	case "generate.again":
		what = "a new take"
		if est.Takes > 1 {
			what = pluralize(est.Takes, "new take")
		}
	case "generate.extend":
		what = fmt.Sprintf("%d more seconds of the clip", est.Seconds)
	case "generate.edit":
		what = "a changed copy of the clip"
	default:
		if est.Takes > 1 {
			what = fmt.Sprintf("%d takes of %s", est.Takes, shot)
		}
	}
	title := fmt.Sprintf("Make %s (%s)", what, costs.About(est.USD))
	// The estimate opens to what it draws on (D208): the video, what is sent and the thinking allowance.
	cost := est.Cost
	a.Estimate = &cost
	a.SpendsIn = est.ProjectID // the project the operation pays from (D241)
	if prompt != "" {
		a.Details = append(a.Details, "Shows: "+quoted(clip(prompt, 160)))
	}
	if est.Look != "" {
		a.Details = append(a.Details, "With the look "+quoted(est.Look))
	}
	q := "Draft quality"
	if est.Quality == "final" {
		q = "Final quality"
	}
	shape := "wide"
	if est.AspectRatio == "9:16" {
		shape = "tall"
	}
	a.Details = append(a.Details, fmt.Sprintf("%s (%s), %s, made with %s", q, videogen.ResolutionLabel(est.Resolution), shape, est.ProviderTitle))
	if est.Consent.OK && est.Consent.Message != "" {
		a.Details = append(a.Details, est.Consent.Message)
	}
	if b := est.Budget.Project; b != nil && b.LimitUSD != nil && b.LeftUSD != nil {
		a.Details = append(a.Details, fmt.Sprintf("This project has %s left of %s for generated video", usdWords(*b.LeftUSD), usdWords(*b.LimitUSD)))
	}
	a.Details = append(a.Details, "Lands in Sources as a new clip, marked as AI-generated; nothing is replaced")
	if !est.Allowed {
		why := "It can't be made right now."
		if len(est.Problems) > 0 {
			why = est.Problems[0]
		}
		unavailable(a, clip(title, 160), why)
		return
	}
	a.Title = clip(title, 160)
	setInput(a, op, in)
}

func usdWords(v float64) string {
	if v > 0 && v < 0.01 {
		return "less than a cent"
	}
	return fmt.Sprintf("$%.2f", v)
}

func clockSecs(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if mm, ss, ok := strings.Cut(s, ":"); ok {
		m, err := strconv.Atoi(mm)
		if err != nil {
			return 0, err
		}
		sec, err := strconv.ParseFloat(ss, 64)
		return float64(m)*60 + sec, err
	}
	return strconv.ParseFloat(s, 64)
}
