package assistant

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// captionsInput reads `metarush cut captions <project> [--look …] [--size …]
// [--position …] [--words-per-line N] [--on|--off]` the way the CLI does (D245).
func captionsInput(call *cliCall) (ops.CutCaptionsIn, bool) {
	if len(call.Args) < 1 {
		return ops.CutCaptionsIn{}, false
	}
	in := ops.CutCaptionsIn{ProjectID: call.Args[0], Branch: call.Flags["branch"], Look: call.Flags["look"], Size: call.Flags["size"],
		Position: call.Flags["position"], Instruction: clip(call.Flags["message"], 2000), Rationale: clip(call.Flags["why"], 4000)}
	if n, err := strconv.Atoi(call.Flags["words-per-line"]); err == nil {
		in.Words = n
	}
	switch {
	case hasBool(call, "on") && hasBool(call, "off"):
		return in, false
	case hasBool(call, "on"):
		v := true
		in.On = &v
	case hasBool(call, "off"):
		v := false
		in.On = &v
	}
	return in, true
}

// describeCutCaptions: how the cut's captions look, checked when the card is
// made (the change in plain words, what the check says), saved by the
// person's click as a new version — like `cut transition`.
func (m *Manager) describeCutCaptions(ctx context.Context, call *cliCall, a *AssistantAction) {
	in, ok := captionsInput(call)
	if !ok {
		incomplete(a, "Change how the captions look", "Claude did not say which project and what to change.")
		return
	}
	dry := in
	dry.DryRun, dry.ConfirmToken = true, ""
	b, _ := json.Marshal(dry)
	res, err := ops.Call(ctx, m.App, "cut.captions", b, ops.Caller{})
	if err != nil {
		incomplete(a, "Change how the captions look", firstLine(apperr.From(err).Message))
		return
	}
	r := res.(*ops.CutCaptionsOut)
	if r.NoChange {
		incomplete(a, "Change how the captions look", r.Summary)
		return
	}
	a.Title = "Captions: " + r.Now
	if r.Now == "off" {
		a.Title = "Turn the captions off"
	}
	a.Details = []string{r.Summary}
	if chk := r.Check; chk != nil {
		co := *chk
		co.ConfirmToken, co.ExpiresAt, co.Next = "", nil, ""
		a.Check = &co
		for _, w := range co.Warnings {
			if w.Code == "captions_downgraded" || w.Code == "caption_collision" {
				a.Details = append(a.Details, w.Message)
			}
		}
		if !co.Valid {
			a.Details = append(a.Details, pluralize(len(co.Errors), "problem")+" to fix first")
		}
	}
	a.Details = append(a.Details, "Puts the change in the cut as a new version")
	in.DryRun, in.ConfirmToken = false, ""
	setInput(a, "cut.captions", in)
}
