package mcp

import (
	"context"
	"encoding/json"
	"maps"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ops"
)

// Spending through MCP. A connection with --allow-changes makes changes, each
// confirmed by the app's own second call — which is no one's approval, and a
// client's "always allow" covers every change of a tool. So a change that
// spends money at an outside service needs the person's word for the whole
// connection too: --allow-spending (Options.AllowSpending). Budgets, daily
// limits, consent and people-only steps still apply.
//
// What spends is decided per call, so pricing (a dry run, an estimate) and
// work done on this computer stay open to every connection with changes:
//   - spendRules decide from the call's arguments alone;
//   - pricedBy ask the operation's own estimate of this very call (the same
//     planning the call runs: a model the Content Settings pin, a voice's
//     earlier cloud clean-up, lines made before) and the call spends when
//     the price is above nothing;
//   - claudeRuns start the person's Claude Code, which bills their API key or
//     counts against their plan;
//   - byAPerson are paid only when a person starts them (their own rule
//     refuses an app).
// A guard test wants every change an app may call that takes a price (maxUsd)
// or that MetaRush counts as paid (ops.PaidOps) in one of them.

// spendRules say, from its arguments, whether a call of the operation spends.
var spendRules = map[string]func(args map[string]any) bool{
	"generate.shot":   unlessPricing,
	"generate.again":  unlessPricing,
	"generate.extend": unlessPricing,
	"generate.edit":   unlessPricing,
	// Cut-outs are made on a computer of the workspace; one made by a model
	// elsewhere, or at an agreed price, may cost money.
	"cut.background": cutOutElsewhere,
	"footage.cutout": cutOutElsewhere,
}

// A price is a read that prices a call exactly as it would run, and where its
// answer says the cost.
type price struct {
	op    string
	in    map[string]any
	field []string
}

// pricedBy give, for a call, the estimate that prices it; ok false: the call
// only prices (nothing to ask).
var pricedBy = map[string]func(args map[string]any) (p price, ok bool){
	// AI work: the usual model or a pinned one may be a service or this computer's.
	"ai.make": func(a map[string]any) (price, bool) {
		if argFlag(a, "dryRun") {
			return price{}, false
		}
		return price{op: "ai.estimate", in: without(a, "dryRun", "maxUsd"), field: []string{"usd"}}, true
	},
	// The AI voice: lines made before are free (unless made again).
	"voiceover.tts": func(a map[string]any) (price, bool) {
		return price{op: "voiceover.estimate", in: without(a, "apply"), field: []string{"credits"}}, true
	},
	// Clearer voices: every voice the call touches keeps its own way (a voice
	// cleaned in the cloud before stays so), so only the plan can tell.
	"sound.enhance": func(a map[string]any) (price, bool) {
		if argFlag(a, "estimate") {
			return price{}, false
		}
		in := without(a, "apply")
		in["estimate"] = true
		if argText(a, "mode") != "" {
			if _, set := a["off"]; !set {
				in["off"] = false // asking for a way means hearing it, as the call does
			}
		}
		return price{op: "sound.enhance", in: in, field: []string{"estimate", "credits"}}, true
	},
}

// claudeRuns start the person's own Claude Code.
var claudeRuns = map[string]bool{
	"assistant.ask":      true,
	"assistant.continue": true,
	"voiceover.askFit":   true,
	"footage.describe":   true,
	"improve.report":     true,
}

// byAPerson are the operations an app may call whose paid calls only a person
// starts, with why.
var byAPerson = map[string]string{
	"ability.run": "a priced or keyed run is started by a person (the manifest's decider, abilities/plan.go), never an app",
}

// priceCall runs a pricing read (a variable so tests can stand in a price).
var priceCall = func(ctx context.Context, a *app.App, op string, in map[string]any) (any, error) {
	raw, _ := json.Marshal(in)
	return ops.Call(ctx, a, op, raw, ops.Caller{})
}

// spends reports whether this call of op can spend money at an outside
// service. An error is the pricing's (the call itself would meet it too).
func (s *Server) spends(ctx context.Context, op *ops.Op, args map[string]any) (bool, error) {
	if rule, ok := spendRules[op.Name]; ok {
		return rule(args), nil
	}
	if claudeRuns[op.Name] {
		return true, nil
	}
	ask, ok := pricedBy[op.Name]
	if !ok {
		return false, nil
	}
	p, ok := ask(without(args, "confirmToken", "mcpConfirmToken")) // the call's own token isn't the price's
	if !ok {
		return false, nil
	}
	out, err := priceCall(ctx, s.a, p.op, p.in)
	if err != nil {
		return false, err
	}
	b, err := json.Marshal(out)
	if err != nil {
		return true, nil
	}
	var v any
	if json.Unmarshal(b, &v) != nil {
		return true, nil
	}
	for i, k := range p.field {
		m, ok := v.(map[string]any)
		if !ok {
			return true, nil // a price it can't read counts as one
		}
		if v, ok = m[k]; !ok {
			return i < len(p.field)-1, nil // the cost left out is nothing (omitempty)
		}
	}
	cost, isNumber := v.(float64)
	return !isNumber || cost > 0, nil
}

// unlessPricing: every call spends but a dry run, which only prices.
func unlessPricing(a map[string]any) bool { return !argFlag(a, "dryRun") }

func cutOutElsewhere(a map[string]any) bool {
	if argFlag(a, "dryRun") {
		return false
	}
	if m := argText(a, "model"); m != "" {
		return !onThisComputer(m)
	}
	usd, _ := a["maxUsd"].(float64)
	return usd > 0
}

// onThisComputer: a model of MetaRush's own provider for this computer
// (provider names are read in any case).
func onThisComputer(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "local/")
}

// without is a copy of the arguments without some fields.
func without(a map[string]any, keys ...string) map[string]any {
	out := maps.Clone(a)
	if out == nil {
		out = map[string]any{}
	}
	for _, k := range keys {
		delete(out, k)
	}
	return out
}

func argText(a map[string]any, k string) string {
	s, _ := a[k].(string)
	return s
}

func argFlag(a map[string]any, k string) bool {
	b, _ := a[k].(bool)
	return b
}
