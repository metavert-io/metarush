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

// The cut's editing verbs (`metarush cut add|move|trim|split|delete|
// delete-range|remove-words`) change the cut, so Claude's own call is refused
// and becomes a card, checked when it is made (the change in plain words and
// what to watch for); the person's click saves it, like `cut transition`.

// cutEditOps maps a command to its op and the card's title.
var cutEditOps = map[string]struct{ op, title string }{
	"cut add":          {"cut.add", "Add footage to the cut"},
	"cut move":         {"cut.move", "Move a clip"},
	"cut trim":         {"cut.trim", "Trim a clip"},
	"cut split":        {"cut.split", "Split a clip"},
	"cut delete":       {"cut.delete", "Remove clips"},
	"cut delete-range": {"cut.deleteRange", "Cut out part of the video"},
	"cut remove-words": {"cut.removeWords", "Take words out"},
	"cut pauses":       {"cut.pauses", "Shorten long pauses"},
	// Call recordings (D161, D162).
	"cut flashes":   {"cut.flashes", "Trim the split-second flashes at the clips' edges"},
	"cut names":     {"cut.names", "Cover the names the call app printed with your own name bar"},
	"cut use-fixed": {"cut.useFixed", "Use a fixed copy in the cut"},
	// The layout pack on the latest cut (D264).
	"content pack apply": {"content.pack.apply", "Use the layout pack on the latest cut"},
}

// isCutEditOp reports whether an op is one of the editing verbs.
func isCutEditOp(op string) bool {
	for _, v := range cutEditOps {
		if v.op == op {
			return true
		}
	}
	return false
}

// cutEditInput reads an editing verb's command line the way the CLI does.
func cutEditInput(call *cliCall) (any, bool) {
	c := ops.CutEditCommon{Branch: call.Flags["branch"], AllowUnlock: hasBool(call, "allow-unlock")}
	msg := call.Flags["message"]
	if msg == "" {
		msg = call.Flags["m"]
	}
	c.Instruction, c.Rationale = clip(msg, 2000), clip(call.Flags["why"], 4000)
	if call.Path == "content pack apply" {
		c.ProjectID = call.Flags["project"]
		return ops.ContentPackApplyIn{CutEditCommon: c}, c.ProjectID != ""
	}
	args := call.Args
	if len(args) == 0 {
		return nil, false
	}
	c.ProjectID = args[0]
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	switch call.Path {
	case "cut add":
		if len(args) < 2 {
			return nil, false
		}
		in := ops.CutAddIn{CutEditCommon: c, Footage: args[1], From: firstNonEmpty(arg(2), call.Flags["from"]), To: firstNonEmpty(arg(3), call.Flags["to"]), Name: call.Flags["name"], Muted: hasBool(call, "muted")}
		switch {
		case call.Flags["after"] != "":
			in.Position = "after " + call.Flags["after"]
		case call.Flags["before"] != "":
			in.Position = "before " + call.Flags["before"]
		default:
			in.Position = call.Flags["at"]
		}
		return in, true
	case "cut move":
		if len(args) < 3 {
			return nil, false
		}
		return ops.CutMoveIn{CutEditCommon: c, Clip: args[1], Position: strings.Join(args[2:], " ")}, true
	case "cut trim":
		if len(args) < 2 {
			return nil, false
		}
		in := ops.CutTrimIn{CutEditCommon: c, Clip: args[1], Start: call.Flags["start"], End: call.Flags["end"], Roll: hasBool(call, "roll")}
		// --end is a switch for `cut transition`, so the parser leaves its value
		// here as an extra word after the clip.
		if _, ok := call.Flags["end"]; ok && in.End == "" && len(args) == 3 {
			in.End = args[2]
		}
		if s, ok := call.Flags["slip"]; ok {
			v, err := strconv.ParseFloat(strings.TrimSuffix(s, "s"), 64)
			if err != nil {
				return nil, false
			}
			in.Slip = &v
		}
		return in, true
	case "cut split":
		switch len(args) {
		case 2:
			return ops.CutSplitIn{CutEditCommon: c, At: args[1], Name: call.Flags["name"]}, true
		case 3:
			return ops.CutSplitIn{CutEditCommon: c, Clip: args[1], At: args[2], Name: call.Flags["name"]}, true
		}
		return nil, false
	case "cut delete":
		if len(args) < 2 {
			return nil, false
		}
		return ops.CutDeleteIn{CutEditCommon: c, Clips: args[1:]}, true
	case "cut delete-range":
		if len(args) != 3 {
			return nil, false
		}
		return ops.CutDeleteRangeIn{CutEditCommon: c, From: args[1], To: args[2]}, true
	case "cut flashes":
		return ops.CutFlashesIn{CutEditCommon: c}, true
	case "cut names":
		in := ops.CutNamesIn{CutEditCommon: c, Style: call.Flags["style"], KeepTitles: hasBool(call, "keep-titles"), ID: call.Flags["name"]}
		switch {
		case hasBool(call, "first-names"):
			v := true
			in.FirstNames = &v
		case hasBool(call, "full-names"):
			v := false
			in.FirstNames = &v
		}
		return in, true
	case "cut use-fixed":
		if len(args) < 2 {
			return nil, false
		}
		return ops.CutUseFixedIn{CutEditCommon: c, Footage: args[1], Original: hasBool(call, "original")}, true
	case "cut remove-words":
		in := ops.CutRemoveWordsIn{CutEditCommon: c, Words: arg(1), Clip: call.Flags["clip"], All: hasBool(call, "all"), Cleanup: hasBool(call, "ums") || hasBool(call, "cleanup")}
		if w := call.Flags["which"]; w != "" {
			n, err := strconv.Atoi(w)
			if err != nil {
				return nil, false
			}
			in.Which = n
		}
		for _, p := range strings.Split(call.Flags["pick"], ",") {
			if p = strings.TrimSpace(p); p != "" {
				n, err := strconv.Atoi(p)
				if err != nil {
					return nil, false
				}
				in.Pick = append(in.Pick, n)
			}
		}
		if in.Words == "" && !in.Cleanup {
			return nil, false
		}
		return in, true
	case "cut pauses":
		in := ops.CutPausesIn{CutEditCommon: c, Clip: call.Flags["clip"]}
		secs := func(v string) (float64, bool) {
			v = strings.TrimSpace(strings.ToLower(v))
			if v == "" {
				return 0, true
			}
			mult := 1.0
			if strings.HasSuffix(v, "ms") {
				v, mult = strings.TrimSuffix(v, "ms"), 0.001
			}
			n, err := strconv.ParseFloat(strings.TrimSuffix(v, "s"), 64)
			return n * mult, err == nil && n >= 0
		}
		var ok1, ok2 bool
		in.LongerThan, ok1 = secs(call.Flags["longer-than"])
		in.To, ok2 = secs(call.Flags["to"])
		if !ok1 || !ok2 {
			return nil, false
		}
		for _, p := range strings.Split(call.Flags["pick"], ",") {
			if p = strings.TrimSpace(p); p != "" {
				n, err := strconv.Atoi(p)
				if err != nil {
					return nil, false
				}
				in.Pick = append(in.Pick, n)
			}
		}
		return in, true
	}
	return nil, false
}

// withDryRun sets an editing verb's dryRun and clears its token.
func withDryRun(in any, dry bool) any {
	b, _ := json.Marshal(in)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	delete(m, "confirmToken")
	if dry {
		m["dryRun"] = true
	} else {
		delete(m, "dryRun")
	}
	return m
}

// describeCutEdit is the card for an editing verb: checked now, so it says in
// plain words what changes and what to watch for; the person saves it with one click.
func (m *Manager) describeCutEdit(ctx context.Context, call *cliCall, a *AssistantAction) {
	verb := cutEditOps[call.Path]
	in, ok := cutEditInput(call)
	if !ok {
		incomplete(a, verb.title, "Claude did not say which project and what to change.")
		return
	}
	b, _ := json.Marshal(withDryRun(in, true))
	res, err := ops.Call(ctx, m.App, verb.op, b, ops.Caller{})
	if err != nil {
		incomplete(a, verb.title, firstLine(apperr.From(err).Message))
		return
	}
	r := res.(interface{ Edit() *ops.CutEditOut }).Edit()
	if r.NoChange {
		incomplete(a, verb.title, r.Summary)
		return
	}
	a.Title = verb.title
	if uf, ok := in.(ops.CutUseFixedIn); ok {
		a.Title = m.useFixedTitle(ctx, uf)
	}
	a.Details = []string{r.Summary}
	if len(r.Changes) > 1 {
		for _, c := range r.Changes[:min(4, len(r.Changes))] {
			a.Details = append(a.Details, c)
		}
	}
	if chk := r.Check; chk != nil {
		co := *chk
		co.ConfirmToken, co.ExpiresAt, co.Next = "", nil, ""
		a.Check = &co
		seen := map[string]bool{}
		for _, p := range r.Problems {
			// What the change does at its cuts, each said once.
			if p.Severity != "error" && editspec.CutIssueCodes[p.Code] && !seen[p.Message] {
				seen[p.Message] = true
				a.Details = append(a.Details, p.Message)
			}
		}
		if !co.Valid {
			a.Details = append(a.Details, pluralize(len(co.Errors), "problem")+" to fix first")
		}
	}
	a.Details = append(a.Details, "Puts the change in the cut as a new version")
	setInput(a, verb.op, withDryRun(in, false))
}

// cutEditToken is the confirm token for running an editing verb's card outside
// the dashboard (the person confirmed the card: check it and use the token).
func cutEditToken(input json.RawMessage, call func(json.RawMessage) (*ops.CutEditOut, error)) (json.RawMessage, error) {
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return nil, err
	}
	m["dryRun"] = true
	delete(m, "confirmToken")
	dry, _ := json.Marshal(m)
	out, err := call(dry)
	if err != nil {
		return nil, err
	}
	delete(m, "dryRun")
	if out.Check != nil {
		m["confirmToken"] = out.Check.ConfirmToken
	}
	return json.Marshal(m)
}
