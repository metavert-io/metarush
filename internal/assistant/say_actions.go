package assistant

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/spoken"
)

// Words changed in a speaker's own voice copy (D308–D312) in Claude's panel.
// Claude may price and read them (cut say estimate|list|show); asking for
// new words is a priced card only a person confirms, putting them in the
// cut is a person's card too, and taking them out again is a card anyone in
// the conversation may confirm. None runs by itself (never Auto).

// sayCommands are the commands handled here.
var sayCommands = map[string]bool{"cut say make": true, "cut say place": true, "cut say undo": true, "cut say mouth": true}

func (m *Manager) describeSay(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	switch call.Path {
	case "cut say mouth":
		id := first(call.Args)
		if !ids.Valid(ids.SpokenChange, id) {
			incomplete(a, "Match the mouth", "Claude did not name the change (from cut say list).")
			return
		}
		ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
		defer cancel()
		est, err := ops.CallTyped[spoken.MouthOut](ectx, m.App, "cut.say.mouth", spoken.MouthIn{ID: id, DryRun: true}, ops.Caller{Human: true})
		if err != nil || est.Estimate == nil {
			msg := "it can't be priced"
			if err != nil {
				msg = firstLine(apperr.From(err).Message)
			}
			incomplete(a, "Match the mouth", "MetaRush could not price it: "+msg)
			return
		}
		a.Title = fmt.Sprintf("Match the mouth to %s (%s)", quoted(est.Change.Words), firstNonEmpty(est.Estimate.Cost.About, costs.About(est.Estimate.USD)))
		a.HumanOnly = true
		cost := est.Estimate.Cost
		a.Estimate, a.SpendsIn = &cost, est.Estimate.ProjectID
		a.Details = []string{"Every face there must be named by a person and consent to lip sync; the result is marked as AI-made"}
		if !est.Estimate.Allowed {
			unavailable(a, a.Title, firstNonEmpty(first(est.Estimate.Problems), "It can't be made."))
			return
		}
		usd := est.Estimate.USD
		setInput(a, "cut.say.mouth", spoken.MouthIn{ID: id, MaxUSD: &usd})
		return
	case "cut say place", "cut say undo":
		id := first(call.Args)
		if !ids.Valid(ids.SpokenChange, id) {
			incomplete(a, "Change spoken words", "Claude did not name the change (from cut say list).")
			return
		}
		v, err := callOp[ops.SpokenView](ctx, m, "cut.say.show", ops.CutSayIDIn{ID: id})
		if err != nil {
			incomplete(a, "Change spoken words", "MetaRush could not find that change: "+firstLine(apperr.From(err).Message))
			return
		}
		in := ops.CutSayPlaceIn{ID: id, Branch: call.Flags["branch"]}
		if call.Path == "cut say undo" {
			a.Title = fmt.Sprintf("Put back %s as %s said it (in place of %s)", quoted(v.Before), v.Speaker.Name, quoted(v.Words))
			a.Details = []string{"A new version of " + v.CutTitle + " plays what was said; the change is kept, so it can go back in"}
			setInput(a, "cut.say.undo", in)
			return
		}
		a.Title = fmt.Sprintf("Put %s in %s's own voice copy into %s (in place of %s)", quoted(v.Words), v.Speaker.Name, v.CutTitle, quoted(v.Before))
		a.HumanOnly = true // putting words in someone's mouth is a person's step (D309)
		a.Details = []string{"Only a person puts changed words in a cut", "It's marked as changed with AI everywhere: the quality check, the export's notes, the receipt, the captions file"}
		if ch := v.Check; ch != nil && ch.Passed {
			a.Details = append(a.Details, fmt.Sprintf("Checked on this computer: heard as %s, the voice %.0f%% like %s", quoted(ch.Heard), voicePct(ch.Voice), v.Speaker.Name))
		}
		if v.State != "ready" && v.State != "undone" {
			unavailable(a, a.Title, firstNonEmpty(v.Next, "It isn't ready to go in the cut."))
			return
		}
		setInput(a, "cut.say.place", in)
		return
	}
	// cut say make: priced first, a person decides.
	ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
	defer cancel()
	in := sayIn(call)
	if in.ProjectID == "" {
		in.ProjectID = c.projectID()
	}
	if strings.TrimSpace(in.Words) == "" {
		incomplete(a, "Change spoken words", "Claude did not say what the new words are (--as).")
		return
	}
	est, err := callOp[spoken.SayEstimate](ectx, m, "cut.say.estimate", in)
	if err != nil {
		e := apperr.From(err)
		incomplete(a, "Change spoken words", "MetaRush could not read the request: "+firstLine(e.Message))
		return
	}
	who := "the speaker"
	if est.Speaker != nil {
		who = est.Speaker.Name
	}
	a.Title = fmt.Sprintf("Say %s in place of %s in %s's own voice copy (%s)", quoted(est.Words), quoted(est.Before), who, firstNonEmpty(est.Cost.About, costs.About(est.USD)))
	a.HumanOnly = true // only a person changes what someone said (D309)
	cost := est.Cost
	a.Estimate = &cost
	a.SpendsIn = est.ProjectID
	if est.Context != "" {
		a.Details = append(a.Details, "Sends the words "+quoted(est.Context))
	}
	if est.Voice != nil {
		a.Details = append(a.Details, fmt.Sprintf("Made with %s's voice copy at %s, with their consent for new words", who, est.Voice.ProviderTitle))
	}
	a.Details = append(a.Details, "Checked on this computer before anyone can put it in the cut, and marked as changed with AI everywhere")
	if !est.Allowed {
		unavailable(a, a.Title, firstNonEmpty(first(est.Problems), "It can't be made."))
		return
	}
	in.MaxUSD = &est.USD
	setInput(a, "cut.say.make", in)
}

// sayIn reads `metarush cut say make <project> --clip --from --to | --quote --as` as the op's input.
func sayIn(call *cliCall) spoken.SayIn {
	in := spoken.SayIn{ProjectID: first(call.Args), Branch: call.Flags["branch"], AssetID: call.Flags["clip"], Quote: clip(call.Flags["quote"], 400),
		Words: clip(strings.TrimSpace(call.Flags["as"]), 200), Model: clip(call.Flags["model"], 160)}
	if v, err := strconv.ParseFloat(call.Flags["from"], 64); err == nil {
		in.From = &v
	}
	if v, err := strconv.ParseFloat(call.Flags["to"], 64); err == nil {
		in.To = &v
	}
	return in
}

func voicePct(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v * 100
}
