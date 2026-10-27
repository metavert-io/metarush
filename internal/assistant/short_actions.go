package assistant

import (
	"context"
	"encoding/json"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ops"
)

// shortInput reads `metarush footage short <project> (--clip <clip> --from
// <time> --to <time> | --piece <clip>@<from>-<to> …) [--name] [--look]
// [--shape] [--framing] [--no-captions] [--why]` the way the CLI does (D246).
func shortInput(call *cliCall) (ops.FootageShortIn, bool) {
	if len(call.Args) < 1 {
		return ops.FootageShortIn{}, false
	}
	in := ops.FootageShortIn{ProjectID: call.Args[0], Name: clip(call.Flags["name"], 120), Look: call.Flags["look"], Shape: call.Flags["shape"],
		Framing: call.Flags["framing"], Why: clip(call.Flags["why"], 2000), Branch: call.Flags["branch"]}
	if hasBool(call, "no-captions") {
		v := false
		in.Captions = &v
	}
	if a := call.Flags["clip"]; a != "" {
		f, okF := editspec.ParseClock(call.Flags["from"])
		t, okT := editspec.ParseClock(call.Flags["to"])
		if !okF || !okT {
			return in, false
		}
		in.Pieces = append(in.Pieces, ops.ShortPiece{AssetID: a, From: f, To: t})
	}
	pieces := call.Multi["piece"]
	if len(pieces) == 0 && call.Flags["piece"] != "" {
		pieces = []string{call.Flags["piece"]}
	}
	for _, s := range pieces {
		p, err := ops.ParseShortPiece(s)
		if err != nil {
			return in, false
		}
		in.Pieces = append(in.Pieces, p)
	}
	return in, len(in.Pieces) > 0
}

// describeFootageShort: a short from any footage, checked when the card is
// made (what it plays, its shape, framing and captions), made by the
// person's click as a new cut they approve like any other — like
// `conversation short` for calls.
func (m *Manager) describeFootageShort(ctx context.Context, call *cliCall, a *AssistantAction) {
	in, ok := shortInput(call)
	if !ok {
		incomplete(a, "Make a short", "Claude did not say which project and which stretch of footage.")
		return
	}
	dry := in
	dry.DryRun, dry.ConfirmToken = true, ""
	b, _ := json.Marshal(dry)
	res, err := ops.Call(ctx, m.App, "footage.short", b, ops.Caller{})
	if err != nil {
		incomplete(a, "Make a short", firstLine(apperr.From(err).Message))
		return
	}
	r := res.(*ops.FootageShortOut)
	a.Title = "Make the short " + theirs(r.Name)
	a.Details = append([]string{r.Summary}, r.Details...)
	if chk := r.Check; chk != nil {
		co := *chk
		co.ConfirmToken, co.ExpiresAt, co.Next = "", nil, ""
		a.Check = &co
		if !co.Valid {
			a.Details = append(a.Details, pluralize(len(co.Errors), "problem")+" to fix first")
		}
	}
	a.Details = append(a.Details, "A new cut of the project; you approve it like any other")
	in.DryRun, in.ConfirmToken = false, ""
	setInput(a, "footage.short", in)
}
