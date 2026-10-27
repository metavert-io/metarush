package assistant

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ops"
)

// transitionInput reads `metarush cut transition <project> <clip> <type>
// [seconds] [--lead|--carry <s> | --auto [--within <s>] | --with-picture]
// [--blend <s>] [--direction …] [--easing …] [--end]` the way the CLI does.
func transitionInput(call *cliCall) (ops.CutTransitionIn, bool) {
	if len(call.Args) < 3 {
		return ops.CutTransitionIn{}, false
	}
	in := ops.CutTransitionIn{ProjectID: call.Args[0], Clip: call.Args[1]}
	words := call.Args[2:]
	if n := len(words); n > 1 {
		if v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(words[n-1], "s")), 64); err == nil && v >= 0 {
			in.Duration = &v
			words = words[:n-1]
		}
	}
	in.Type = strings.Join(words, " ")
	if strings.EqualFold(in.Type, "keep") {
		in.Type = ""
	}
	num := func(k string) *float64 {
		if s, ok := call.Flags[k]; ok {
			if v, err := strconv.ParseFloat(strings.TrimSuffix(s, "s"), 64); err == nil {
				return &v
			}
		}
		return nil
	}
	switch {
	case num("lead") != nil:
		in.Sound, in.SoundSeconds = "lead", num("lead")
	case num("carry") != nil:
		in.Sound, in.SoundSeconds = "carry", num("carry")
	case hasBool(call, "auto"):
		in.Sound, in.SoundSeconds = "auto", num("within")
	case hasBool(call, "with-picture"):
		in.Sound = "cut"
	}
	in.Blend = num("blend")
	in.Direction, in.Easing, in.Branch = call.Flags["direction"], call.Flags["easing"], call.Flags["branch"]
	in.End, in.AllowUnlock = hasBool(call, "end"), hasBool(call, "allow-unlock")
	msg := call.Flags["message"]
	if msg == "" {
		msg = call.Flags["m"]
	}
	in.Instruction, in.Rationale = clip(msg, 2000), clip(call.Flags["why"], 4000)
	return in, true
}

// describeCutTransition is the card for `metarush cut transition …`: checked
// now, so it says in plain words what changes and what to watch for; the person
// saves it with one click.
func (m *Manager) describeCutTransition(ctx context.Context, call *cliCall, a *AssistantAction) {
	in, ok := transitionInput(call)
	if !ok {
		incomplete(a, "Change a transition", "Claude did not say which project, clip and transition.")
		return
	}
	dry := in
	dry.DryRun, dry.ConfirmToken = true, ""
	b, _ := json.Marshal(dry)
	res, err := ops.Call(ctx, m.App, "cut.transition", b, ops.Caller{})
	if err != nil {
		incomplete(a, "Change a transition", firstLine(apperr.From(err).Message))
		return
	}
	r := res.(*ops.CutTransitionOut)
	if r.NoChange {
		incomplete(a, "Change a transition", r.Summary)
		return
	}
	a.Title = "Change the transition into " + clip(r.Into, 80)
	if r.Edge == "end" {
		a.Title = "Change how " + clip(r.Into, 80) + " fades out"
	}
	a.Details = []string{r.Summary}
	if chk := r.Check; chk != nil {
		co := *chk
		co.ConfirmToken, co.ExpiresAt, co.Next = "", nil, ""
		a.Check = &co
		for _, w := range co.Warnings {
			if editspec.CutIssueCodes[w.Code] { // what this change does at the cut, in plain words
				a.Details = append(a.Details, w.Message)
			}
		}
		if !co.Valid {
			a.Details = append(a.Details, pluralize(len(co.Errors), "problem")+" to fix first")
		}
	}
	a.Details = append(a.Details, "Puts the change in the cut as a new version")
	in.DryRun, in.ConfirmToken = false, ""
	setInput(a, "cut.transition", in)
}
