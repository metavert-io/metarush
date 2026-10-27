package assistant

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/ids"
)

// AI services in the panel (D224–D227): Claude asks with `metarush ai make
// <kind> …` and the request becomes a card with its price, the model and
// why, the alternatives and the licence terms, built here (never from the
// browser): the person confirms it, so nothing is made automatically unless
// their estimates are off and Auto is on (pricedOps). Voice copies and which
// services a client's work may use are a person's decisions, never cards.

// aiCommands are the commands handled here.
var aiCommands = map[string]bool{"ai make": true, "ai cancel": true, "ai voices copy": true, "ai voices remove": true, "ai rules set": true,
	"presenter start": true, "presenter make": true, "presenter test": true, "presenter approve": true, "presenter retire": true, "presenter opt-out": true}

// aiNever are what only a person does, in the dashboard or their own terminal.
var aiNever = map[string]string{
	"ai voices copy":   "A copy of someone's voice is made by a person, with that person's consent, in the Generate panel.",
	"ai voices remove": "A voice copy is withdrawn by a person.",
	"ai rules set":     "Which AI services a client's work may use is set by a person in the project's Settings tab.",
	// A person's presenter (D273–D277): every step is a person's own.
	"presenter start":   "A person's presenter is begun by a person, with the person it shows, in the Generate panel.",
	"presenter make":    "A person's presenter is made by a person, from their recorded consent and a training take, in the Generate panel.",
	"presenter test":    "A presenter's test clips are made by a person before they approve it.",
	"presenter approve": "A presenter is approved by a person, after watching its test clips.",
	"presenter retire":  "A presenter is retired by a person (or when the consent it relies on is withdrawn).",
	"presenter opt-out": "Recording that a service's training opt-out is set is a person's step.",
}

func (m *Manager) describeAI(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	if why, never := aiNever[call.Path]; never {
		incomplete(a, "", why)
		return
	}
	arg := first(call.Args)
	switch call.Path {
	case "ai cancel":
		if !ids.Valid(ids.Generation, arg) && !ids.Valid(ids.Asset, arg) && !strings.HasPrefix(arg, "res_") {
			incomplete(a, "Stop AI work", "Claude did not name the AI work to stop.")
			return
		}
		a.Title = "Stop the AI work"
		a.Details = []string{"The service is asked to stop too; what it already made may still be charged"}
		setInput(a, "ai.cancel", aiwork.IDIn{ID: arg})
		return
	}
	ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()
	in := aiMakeIn(call)
	in.Origin = "assistant:" + c.id
	if in.ProjectID == "" {
		in.ProjectID = c.projectID()
	}
	if in.Kind == "" {
		incomplete(a, "Make something with an AI service", "Claude did not say what kind of thing to make.")
		return
	}
	est, err := callOp[aiwork.AIEstimate](ectx, m, "ai.estimate", in)
	if err != nil {
		e := apperr.From(err)
		if e.Code == apperr.InvalidInput || e.Code == apperr.NotFound {
			incomplete(a, "Make something with an AI service", "MetaRush could not read the request: "+firstLine(e.Message))
			return
		}
		unavailable(a, "Make something with an AI service", "MetaRush could not price it: "+firstLine(e.Message))
		return
	}
	in.MaxUSD = &est.USD
	m.aiCard(a, est, in)
}

// aiMakeIn reads `metarush ai make <kind> [words] --flags` as the op's input.
func aiMakeIn(call *cliCall) aiwork.MakeIn {
	words := ""
	if len(call.Args) > 1 {
		words = strings.Join(call.Args[1:], " ")
	}
	in := aiwork.MakeIn{Kind: strings.TrimSpace(first(call.Args)), ProjectID: call.Flags["project"], Model: clip(call.Flags["model"], 160),
		Prompt: clip(strings.TrimSpace(firstNonEmpty(call.Flags["prompt"], words)), 10000), Pictures: listFlag(call, "picture"), Clip: call.Flags["clip"],
		NoPeople: hasBool(call, "no-people")}
	num := func(name string) (float64, bool) {
		v, err := strconv.ParseFloat(call.Flags[name], 64)
		return v, err == nil
	}
	if v, ok := num("from"); ok {
		in.From = &v
	}
	if v, ok := num("to"); ok {
		in.To = &v
	}
	o := &in.Options
	o.Size, o.AspectRatio, o.Voice, o.Style = call.Flags["size"], call.Flags["aspect"], clip(call.Flags["voice"], 120), clip(call.Flags["style"], 300)
	o.Language, o.TargetLanguage = clip(call.Flags["language"], 16), clip(call.Flags["into"], 16)
	if v, ok := num("seconds"); ok {
		o.Seconds = v
	}
	if v, err := strconv.Atoi(call.Flags["speakers"]); err == nil {
		o.Speakers = v
	}
	o.Loop, o.Instrumental, o.OwnVoices = hasBool(call, "loop"), hasBool(call, "instrumental"), hasBool(call, "own-voices")
	// Presenters (D273): who reads, a narration line instead of a clip, a colour behind.
	in.Presenter, in.Branch, o.Background = clip(call.Flags["presenter"], 120), clip(call.Flags["cut"], 80), clip(call.Flags["background"], 16)
	if v, err := strconv.Atoi(call.Flags["line"]); err == nil && v > 0 {
		in.Line = v
	}
	return in
}

// aiCard is a priced card: what it makes, with which model and why, what it
// costs and what else could do it; it can't be confirmed when the budget,
// the key, consent or Content Settings stand in the way.
func (m *Manager) aiCard(a *AssistantAction, est *aiwork.AIEstimate, in aiwork.MakeIn) {
	title := fmt.Sprintf("Make %s (%s)", est.What, est.Cost.About)
	if est.Cost.About == "" {
		title = fmt.Sprintf("Make %s (%s)", est.What, costs.About(est.USD))
	}
	cost := est.Cost
	a.Estimate = &cost
	a.SpendsIn = est.ProjectID // the project the work is for pays (D226, D241)
	if in.Prompt != "" {
		a.Details = append(a.Details, "Words: "+quoted(clip(in.Prompt, 160)))
	}
	for _, s := range est.Inputs {
		a.Details = append(a.Details, "Sends "+s)
	}
	why := map[string]string{"default": "MetaRush's usual model for this", "asked": "the model Claude asked for", "content-settings": "the model the project's Content Settings choose"}[est.Choice]
	a.Details = append(a.Details, fmt.Sprintf("Made with %s (%s), %s", est.ProviderTitle, est.ModelTitle, why))
	for _, alt := range est.Alternatives {
		a.Details = append(a.Details, upperFirstWord(alt.Why)+": "+alt.Summary)
	}
	if est.Licence.Commercial == "plan" || est.Licence.Commercial == "unstated" || est.Licence.Broadcast == "plan" {
		a.Details = append(a.Details, "Licence: "+clip(est.Licence.Summary, 240))
	}
	if est.Consent.OK && est.Consent.Message != "" {
		a.Details = append(a.Details, est.Consent.Message)
	}
	if est.Kind == "presenter" { // who reads it, and a stock presenter's licence (D275)
		for _, n := range est.Notes {
			if strings.HasPrefix(n, "Read by") || strings.Contains(n, "licence") {
				a.Details = append(a.Details, strings.TrimSuffix(n, "."))
			}
		}
	}
	if b := est.Budget.Project; b != nil && b.LimitUSD != nil && b.LeftUSD != nil {
		a.Details = append(a.Details, fmt.Sprintf("This project has %s left of %s for AI work", usdWords(*b.LeftUSD), usdWords(*b.LimitUSD)))
	}
	a.Details = append(a.Details, landsWords(est.Kind))
	if !est.Allowed {
		why := "It can't be made right now."
		if len(est.Problems) > 0 {
			why = est.Problems[0]
		}
		unavailable(a, clip(title, 160), why)
		return
	}
	a.Title = clip(title, 160)
	setInput(a, "ai.make", in)
}

func landsWords(kind string) string {
	switch kind {
	case "picture":
		return "Lands in the resources library, marked as AI-generated; nothing is replaced"
	case "transcription":
		return "Replaces the clip's words; fixes people made are carried over"
	case "presenter":
		return "Lands in Sources as a new clip, marked as an AI presenter, with the project's own sound under it; nothing is replaced"
	}
	return "Lands in Sources as a new clip, marked as AI-generated; nothing is replaced"
}

func upperFirstWord(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// aiLabel describes the AI services commands in the panel's step lines.
func aiLabel(c *cliCall) string {
	switch c.Path {
	case "ai models":
		return "Looked at the AI services' models and prices"
	case "ai estimate":
		return "Priced something an AI service would make"
	case "ai list":
		return "Looked at the AI work made so far"
	case "ai show":
		return "Looked at how something an AI service made was made"
	case "ai voices", "ai voices list":
		return "Looked at the voice copies"
	case "ai rules", "ai rules show":
		return "Checked which AI services the project may use"
	case "ai make":
		return "Asked an AI service to make something"
	case "ai cancel":
		return "Asked to stop AI work"
	case "ai voices copy":
		return "Asked to copy a voice"
	case "ai voices remove":
		return "Asked to withdraw a voice copy"
	case "ai rules set":
		return "Asked to change which AI services the project may use"
	case "presenter list", "presenter show":
		return "Looked at the presenters"
	case "presenter stock":
		return "Looked at the stock presenters"
	case "presenter opt-out":
		return "Checked a service's training opt-out"
	case "presenter start", "presenter make", "presenter test", "presenter approve", "presenter retire":
		return "Asked to change a presenter"
	}
	return ""
}
