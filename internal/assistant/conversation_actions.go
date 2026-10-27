package assistant

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/conversation"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// Conversations from online calls in the Claude Code panel (D99–D103, D111).
// When Claude asks to make the automatic cut, to accept or skip clean-up
// suggestions, to change what a conversation clip shows, to look a call over
// or to make a short, the card is built here from Claude's command: the
// conversation is found by its name, the change is previewed and checked with
// nothing saved (conversation.Preview*), and the card names the people and
// moments in plain words. The person confirms it; the dashboard then runs the
// same op as the CLI, with apply, on the human channel.

// conversationCommands are the commands describeConversation turns into cards.
var conversationCommands = map[string]bool{"conversation cut": true, "conversation decide": true, "conversation angle": true,
	"conversation short": true, "conversation cleanup": true, "conversation import": true, "conversation create": true,
	"conversation sync": true, "conversation nudge": true, "conversation update": true, "conversation pin": true,
	"conversation match": true, "conversation settings": true}

// onConversationTab are changes made on the project's Conversation tab, not
// from a card (the title the card shows for each).
var onConversationTab = map[string]string{
	"conversation sync":     "Line the recordings up again",
	"conversation nudge":    "Move a recording earlier or later",
	"conversation update":   "Change the conversation",
	"conversation pin":      "Play the conversation's latest line-up in the cut",
	"conversation match":    "Match the webcams",
	"conversation settings": "Change how the cut plays the conversation",
}

func (m *Manager) describeConversation(ctx context.Context, call *cliCall, a *AssistantAction) {
	if title, ok := onConversationTab[call.Path]; ok {
		unavailable(a, title, "Do this on the project's Conversation tab.")
		return
	}
	switch call.Path {
	case "conversation import":
		unavailable(a, "Bring in a call's recordings", "Add them from the dashboard: drop the call's files in the Add box, which offers Make a conversation from these.")
	case "conversation create":
		unavailable(a, "Make a conversation", "Make it on the project's Conversation tab, or drop the call's recordings in the Add box, which offers Make a conversation from these.")
	case "conversation cut":
		m.describeConvCut(ctx, call, a)
	case "conversation decide":
		m.describeConvDecide(ctx, call, a)
	case "conversation angle":
		m.describeConvAngle(ctx, call, a)
	case "conversation short":
		m.describeConvShort(ctx, call, a)
	case "conversation cleanup":
		m.describeConvCleanup(ctx, call, a)
	}
}

// conversationNamed finds the conversation a command names (its ID or name):
// in the project first when one is named, else anywhere in the workspace.
func (m *Manager) conversationNamed(ctx context.Context, ref, projectID string) (*conversation.CallInfo, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, apperr.New(apperr.InvalidInput, "Claude did not name the conversation.")
	}
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	look := func(pid string) (*conversation.CallOut, error) {
		return callOp[conversation.CallOut](lctx, m, "conversation.show", conversation.CallRefIn{Conversation: ref, ProjectID: pid})
	}
	out, err := look(projectID)
	if err != nil && projectID != "" && apperr.Is(err, apperr.NotFound) {
		out, err = look("")
	}
	if err != nil {
		return nil, err
	}
	return &out.Conversation, nil
}

// clockFlag reads a time flag (1:02.5 or seconds); ok is false when it is there but isn't a time.
func clockFlag(call *cliCall, name string) (*float64, bool) {
	v, has := call.Flags[name]
	if !has {
		return nil, true
	}
	t, ok := editspec.ParseClock(strings.TrimSpace(v))
	if !ok {
		return nil, false
	}
	return &t, true
}

// shapeWords names a cut's shape.
func shapeWords(shape string) string {
	switch shape {
	case "vertical":
		return "Vertical 9:16"
	case "square":
		return "Square 1:1"
	}
	return "Landscape 16:9"
}

// sanitizedCheck is a plan's check for a card: no confirm token (the card's op
// checks again when it runs).
func sanitizedCheck(res *ops.EditCheckOut) *ops.EditCheckOut {
	if res == nil {
		return nil
	}
	co := *res
	co.ConfirmToken, co.ExpiresAt, co.Next = "", nil, ""
	return &co
}

// checkDetails puts a preview's check on the card: the diff in words and the
// problems to fix first.
func checkDetails(a *AssistantAction, res *ops.EditCheckOut) {
	co := sanitizedCheck(res)
	if co == nil {
		return
	}
	a.Check = co
	if d := co.Diff; d != nil {
		a.Details = append(a.Details, diffLine(d))
	}
	if !co.Valid {
		a.Details = append(a.Details, pluralize(len(co.Errors), "problem")+" to fix first")
	}
}

// projectOf reads --project (a project ID, or none).
func projectOf(call *cliCall) (string, bool) {
	p := strings.TrimSpace(call.Flags["project"])
	return p, p == "" || ids.Valid(ids.Project, p)
}

// lowerFirst lowers the first letter ("Cuts it out." → "cuts it out.").
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func (m *Manager) describeConvCut(ctx context.Context, call *cliCall, a *AssistantAction) {
	const title = "Make the cut of a conversation"
	project, ok := projectOf(call)
	if !ok {
		incomplete(a, title, "Claude did not name the project by its ID.")
		return
	}
	info, err := m.conversationNamed(ctx, first(call.Args), project)
	if err != nil {
		incomplete(a, title, apperr.From(err).Message)
		return
	}
	in := conversation.CallCutIn{CallRefIn: conversation.CallRefIn{Conversation: info.ID}, Project: project, Branch: clip(strings.TrimSpace(call.Flags["branch"]), 40),
		Style: strings.ToLower(strings.TrimSpace(call.Flags["style"])), Shape: strings.ToLower(strings.TrimSpace(call.Flags["shape"])),
		Interviewer: clip(strings.TrimSpace(call.Flags["interviewer"]), 120), Questions: strings.ToLower(strings.TrimSpace(call.Flags["questions"])),
		BRoll: listFlag(call, "broll"), Layout: strings.ToLower(strings.TrimSpace(call.Flags["layout"]))}
	var okFrom, okTo bool
	in.From, okFrom = clockFlag(call, "from")
	in.To, okTo = clockFlag(call, "to")
	if !okFrom || !okTo {
		incomplete(a, title, "Claude gave a time that isn't one (like 1:02.5).")
		return
	}
	if _, has := call.Flags["captions"]; has {
		on := hasBool(call, "captions")
		in.Captions = &on
	}
	if hasBool(call, "no-name-tags") {
		off := false
		in.NameTags = &off
	}
	style := in.Style
	if style == "" {
		style = info.Style
	}
	if style == "" {
		style = "podcast"
	}
	st, known := conversation.Styles[style]
	if !known {
		incomplete(a, title, "Claude asked for a style there isn't: podcast, interview, panel, reactions or broll.")
		return
	}
	switch in.Shape {
	case "", "landscape", "vertical", "square":
	default:
		incomplete(a, title, "A cut is landscape, vertical or square.")
		return
	}
	a.Title = clip("Make an automatic cut of "+quoted(info.Name)+": "+lowerFirst(st.Label), 160)
	po, err := conversation.PreviewCut(ctx, m.App, in)
	if err != nil { // the cut can't be made as asked: the card says why
		unavailable(a, a.Title, apperr.From(err).Message)
		return
	}
	a.Details = []string{st.About}
	if in.Shape != "" {
		a.Details = append(a.Details, shapeWords(in.Shape))
	}
	if in.From != nil || in.To != nil {
		from, to := 0.0, info.Length
		if in.From != nil {
			from = *in.From
		}
		if in.To != nil {
			to = math.Min(to, *in.To)
		}
		a.Details = append(a.Details, "From "+editspec.Clock(from)+" to "+editspec.Clock(to)+" of the conversation")
	}
	if po.Result != nil && po.Result.Plan.Spec != nil {
		shots := 0
		for _, c := range po.Result.Plan.Spec.Clips {
			shots += len(c.Shots)
		}
		a.Details = append(a.Details, pluralize(shots, "shot")+", each with its reason")
		if cp := po.Result.Plan.Spec.Captions; cp != nil && cp.Enabled {
			a.Details = append(a.Details, "Captions with each person's name")
		}
	}
	if in.NameTags != nil && !*in.NameTags {
		a.Details = append(a.Details, "No name tags")
	}
	if po.Result != nil {
		checkDetails(a, po.Result.Check)
		if po.Result.Plan.BaseRevisionID != "" {
			a.Details = append(a.Details, "Replaces the current cut with a new version; the current one stays in History")
		} else {
			a.Details = append(a.Details, "Becomes the project's first cut")
		}
	}
	in.Apply = true
	setInput(a, "conversation.cut", in)
}

// cleanupNoun names what a clean-up suggestion is about.
func cleanupNoun(s conversation.CleanupInfo) string {
	switch s.Kind {
	case conversation.KindDeadAir:
		return "the long silence"
	case conversation.KindCallTalk:
		return "the talk about the call"
	case conversation.KindFreeze:
		if s.Who != "" {
			return possessiveName(s.Who) + " frozen picture"
		}
		return "the frozen picture"
	case conversation.KindDropout:
		return "the sound dropout"
	case conversation.KindChrome:
		return "the app's labels"
	}
	return "it"
}

// possessiveName is "Sam's" (or "James'").
func possessiveName(name string) string {
	if strings.HasSuffix(name, "s") {
		return name + "'"
	}
	return name + "'s"
}

// acceptWords says what accepting a suggestion does to the cut, in a few
// words: "cut out the long silence at 0:41–0:45".
func acceptWords(s conversation.CleanupInfo) string {
	switch s.Fix {
	case conversation.FixCut:
		w := "cut out " + cleanupNoun(s) + " at " + s.When
		if s.Keep > 0 {
			w = "shorten " + cleanupNoun(s) + " at " + s.When
		}
		if s.Kind == conversation.KindCallTalk && s.Quote != "" {
			w += " (" + quoted(clip(s.Quote, 60)) + ")"
		}
		return w
	case conversation.FixSwitch:
		return "show someone else over " + cleanupNoun(s) + " at " + s.When
	case conversation.FixCrop:
		if s.Who != "" {
			return "crop the app's labels off " + possessiveName(s.Who) + " picture"
		}
		return "crop the app's labels off the picture"
	}
	return "mark " + cleanupNoun(s) + " at " + s.When + " as looked at"
}

func (m *Manager) describeConvDecide(ctx context.Context, call *cliCall, a *AssistantAction) {
	const title = "Decide about clean-up suggestions"
	decision, n := "", 0
	for _, d := range []string{"accept", "skip", "reopen"} {
		if hasBool(call, d) {
			decision, n = d, n+1
		}
	}
	if n != 1 {
		incomplete(a, title, "Claude did not say whether to accept or skip them.")
		return
	}
	if len(call.Args) < 2 {
		incomplete(a, title, "Claude did not name the conversation and the suggestions.")
		return
	}
	project, ok := projectOf(call)
	if !ok {
		incomplete(a, title, "Claude did not name the project by its ID.")
		return
	}
	info, err := m.conversationNamed(ctx, call.Args[0], project)
	if err != nil {
		incomplete(a, title, apperr.From(err).Message)
		return
	}
	var picked []conversation.CleanupInfo
	seen := map[string]bool{}
	all := false
	for _, arg := range call.Args[1:] {
		for _, ref := range strings.Split(arg, ",") {
			ref = strings.ToLower(strings.TrimSpace(ref))
			if ref == "" {
				continue
			}
			found := false
			for _, s := range info.Suggestions {
				if (ref == "open" && s.State == "open") || strings.EqualFold(s.ID, ref) {
					found = true
					if !seen[s.ID] {
						seen[s.ID] = true
						picked = append(picked, s)
					}
				}
			}
			if ref == "open" {
				all, found = true, true
			}
			if !found {
				incomplete(a, title, "Claude named a clean-up suggestion "+quoted(info.Name)+" doesn't have.")
				return
			}
		}
	}
	if len(picked) == 0 {
		incomplete(a, title, "Nothing is open to decide in "+quoted(info.Name)+".")
		return
	}
	idsPicked := make([]string, 0, len(picked))
	for _, s := range picked {
		idsPicked = append(idsPicked, s.ID)
	}
	in := conversation.CallDecideIn{CallRefIn: conversation.CallRefIn{Conversation: info.ID}, IDs: idsPicked, Decision: decision, Project: project,
		Branch: clip(strings.TrimSpace(call.Flags["branch"]), 40), Apply: true}
	list := func() {
		for i, s := range picked {
			if i == 6 {
				a.Details = append(a.Details, fmt.Sprintf("And %d more", len(picked)-6))
				break
			}
			a.Details = append(a.Details, s.When+": "+strings.TrimSuffix(strings.TrimSpace(s.Text), "."))
		}
	}
	every := ""
	if all {
		every = "all "
	}
	switch decision {
	case "skip":
		if len(picked) == 1 {
			a.Title = clip("Skip the clean-up at "+picked[0].When+": leave "+cleanupNoun(picked[0])+" as it is", 160)
			a.Details = []string{strings.TrimSpace(picked[0].Text)}
		} else {
			a.Title = fmt.Sprintf("Skip %s%d clean-up suggestions for %s", every, len(picked), quoted(info.Name))
			list()
		}
		a.Details = append(a.Details, "Nothing in the cut changes; you can bring them back on the Conversation tab")
	case "reopen":
		a.Title = fmt.Sprintf("Bring back %s for %s", pluralize(len(picked), "clean-up suggestion"), quoted(info.Name))
		list()
		a.Details = append(a.Details, "They are open again, to accept or skip")
	default:
		if len(picked) == 1 {
			a.Title = clip(upperFirst(acceptWords(picked[0])), 160)
			a.Details = []string{strings.TrimSpace(picked[0].Text)}
		} else {
			a.Title = fmt.Sprintf("Accept %s%d clean-up suggestions for %s", every, len(picked), quoted(info.Name))
			list()
		}
		po, err := conversation.PreviewDecide(ctx, m.App, in)
		if err != nil {
			unavailable(a, a.Title, apperr.From(err).Message)
			return
		}
		if po.Result != nil {
			checkDetails(a, po.Result.Check)
			a.Details = append(a.Details, "Puts it in the cut as a new version")
		} else {
			a.Details = append(a.Details, "The cut doesn't play this part of the conversation, so nothing in it changes")
		}
	}
	setInput(a, "conversation.decide", in)
}

func (m *Manager) describeConvAngle(ctx context.Context, call *cliCall, a *AssistantAction) {
	const title = "Change what the conversation shows"
	r := m.revisionFor(ctx, call)
	if r == nil {
		incomplete(a, title, "Claude did not name the project by its ID (--project).")
		return
	}
	at, ok := clockFlag(call, "at")
	if !ok || at == nil {
		incomplete(a, title, "Claude did not give the moment (--at, like 0:31.2).")
		return
	}
	in := conversation.CallAngleIn{Project: r.ProjectID, Branch: r.Branch, Clip: clip(strings.TrimSpace(call.Flags["clip"]), 64), At: *at,
		Member: clip(strings.TrimSpace(call.Flags["member"]), 120), Layout: strings.ToLower(strings.TrimSpace(call.Flags["layout"])),
		Members: listFlag(call, "members"), Card: clip(strings.TrimSpace(call.Flags["card"]), 240), BRoll: clip(strings.TrimSpace(call.Flags["broll"]), 300),
		Remove: hasBool(call, "remove"), Why: clip(strings.TrimSpace(call.Flags["why"]), 1000), Apply: true}
	if v, ok := clockFlag(call, "broll-in"); !ok {
		incomplete(a, title, "Claude gave a time that isn't one (like 0:04).")
		return
	} else if v != nil {
		in.BRollIn = *v
	}
	if v, has := call.Flags["dissolve"]; has {
		d, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || d < 0 || d > 2 {
			incomplete(a, title, "A switch blends for at most 2 seconds.")
			return
		}
		in.Dissolve = &d
	}
	// People by the names the cut gives them.
	name := func(ref string) string {
		for _, pin := range r.Spec.Conversations {
			for _, mem := range pin.Members {
				if mem.ID == ref || strings.EqualFold(mem.Name, ref) {
					return mem.Name
				}
			}
		}
		return ref
	}
	var people []string
	for _, x := range in.Members {
		people = append(people, name(x))
	}
	when := editspec.Clock(*at)
	switch {
	case in.Remove:
		a.Title = "Take out the shot at " + when + " in the conversation"
		a.Details = []string{"The shot before it plays on"}
	case in.Member != "":
		a.Title = "Show " + name(in.Member) + " from " + when + " in the conversation"
	case in.Layout != "":
		who := ""
		if len(people) > 0 {
			who = joinWords(people) + " "
		}
		a.Title = "Show " + who + editspec.LayoutLabel(in.Layout) + " from " + when + " in the conversation"
	case in.Card != "":
		a.Title = clip("Show a card from "+when+" in the conversation: "+quoted(in.Card), 160)
		if len(people) > 0 {
			a.Details = []string{"Over " + joinWords(people)}
		}
	case in.BRoll != "":
		what := quoted(clip(in.BRoll, 80))
		if ids.Valid(ids.Asset, in.BRoll) {
			what = quoted(m.nameOf(ctx, in.BRoll))
		}
		a.Title = "Cut away to " + what + " from " + when + " in the conversation"
	default:
		incomplete(a, title, "Claude did not say what to show: a person, a layout, a card or a cutaway.")
		return
	}
	if in.Dissolve != nil && *in.Dissolve > 0 {
		a.Details = append(a.Details, "Blends into it over "+strconv.FormatFloat(*in.Dissolve, 'f', -1, 64)+" s")
	}
	if in.Why != "" {
		a.Details = append(a.Details, "Why: "+clip(in.Why, 300))
	}
	po, err := conversation.PreviewAngle(ctx, m.App, in)
	if err != nil {
		unavailable(a, a.Title, apperr.From(err).Message)
		return
	}
	if po.Result != nil {
		checkDetails(a, po.Result.Check)
	}
	a.Details = append(a.Details, "Puts it in the cut as a new version")
	setInput(a, "conversation.angle", in)
}

// joinWords lists names in a sentence: "Maya", "Maya and Sam", "A, B and C".
func joinWords(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func (m *Manager) describeConvShort(ctx context.Context, call *cliCall, a *AssistantAction) {
	const title = "Make a short from a conversation"
	project, ok := projectOf(call)
	if !ok {
		incomplete(a, title, "Claude did not name the project by its ID.")
		return
	}
	info, err := m.conversationNamed(ctx, first(call.Args), project)
	if err != nil {
		incomplete(a, title, apperr.From(err).Message)
		return
	}
	from, okFrom := clockFlag(call, "from")
	to, okTo := clockFlag(call, "to")
	if !okFrom || !okTo || from == nil || to == nil {
		incomplete(a, title, "Claude did not give the stretch (--from and --to, like 0:25.7).")
		return
	}
	in := conversation.CallShortIn{CallRefIn: conversation.CallRefIn{Conversation: info.ID}, From: *from, To: *to, Name: clip(strings.TrimSpace(call.Flags["name"]), 120),
		Shape: strings.ToLower(strings.TrimSpace(call.Flags["shape"])), Style: strings.ToLower(strings.TrimSpace(call.Flags["style"])), Nest: hasBool(call, "nest"), Apply: true}
	if hasBool(call, "no-captions") {
		off := false
		in.Captions = &off
	}
	if in.Style != "" {
		if _, known := conversation.Styles[in.Style]; !known {
			incomplete(a, title, "Claude asked for a style there isn't: podcast, interview, panel, reactions or broll.")
			return
		}
	}
	switch in.Shape {
	case "", "landscape", "vertical", "square":
	default:
		incomplete(a, title, "A cut is landscape, vertical or square.")
		return
	}
	a.Title = clip("Make a short from "+editspec.Clock(*from)+"–"+editspec.Clock(*to)+" of "+quoted(info.Name), 160)
	po, err := conversation.PreviewShort(ctx, m.App, in)
	if err != nil {
		unavailable(a, a.Title, apperr.From(err).Message)
		return
	}
	shape := in.Shape
	if shape == "" {
		shape = "vertical"
	}
	if in.Nest {
		a.Details = []string{"A new project, " + quoted(po.Project) + ", that plays the episode's own stretch as it is"}
	} else {
		a.Details = []string{"A new project, " + quoted(po.Project) + ", with a cut of its own: " + lowerFirst(shapeWords(shape)) + ", the picture following who talks"}
		if in.Style != "" {
			a.Details = append(a.Details, upperFirst(lowerFirst(conversation.Styles[in.Style].Label)))
		}
	}
	if in.Captions == nil || *in.Captions {
		a.Details = append(a.Details, "Captions with each person's name")
	}
	a.Details = append(a.Details, "The conversation's footage comes from its project; nothing is copied")
	setInput(a, "conversation.short", in)
}

func (m *Manager) describeConvCleanup(ctx context.Context, call *cliCall, a *AssistantAction) {
	const title = "Look a call over for glitches"
	project, ok := projectOf(call)
	if !ok {
		incomplete(a, title, "Claude did not name the project by its ID.")
		return
	}
	info, err := m.conversationNamed(ctx, first(call.Args), project)
	if err != nil {
		incomplete(a, title, apperr.From(err).Message)
		return
	}
	in := conversation.CallCleanupIn{CallRefIn: conversation.CallRefIn{Conversation: info.ID}, Kinds: listFlag(call, "kinds")}
	kindWords := map[string]string{conversation.KindCallTalk: "talk about the call", conversation.KindDeadAir: "long silences", conversation.KindFreeze: "frozen webcams",
		conversation.KindDropout: "sound dropouts", conversation.KindChrome: "the app's labels on a picture"}
	var looking []string
	for _, k := range in.Kinds {
		w, known := kindWords[k]
		if !known {
			incomplete(a, title, "Claude asked to look for something clean-up doesn't find: call-talk, dead-air, freeze, dropout or chrome.")
			return
		}
		looking = append(looking, w)
	}
	if len(looking) == 0 {
		for _, k := range conversation.CleanupKinds {
			looking = append(looking, kindWords[k])
		}
	}
	if v, has := call.Flags["dead-air"]; has {
		d, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || d <= 0 || d > 60 {
			incomplete(a, title, "Dead air is a silence of a few seconds (at most 60).")
			return
		}
		in.DeadAir = d
	}
	a.Title = "Look " + quoted(info.Name) + " over for what calls leave behind"
	a.Details = []string{upperFirst(joinWords(looking))}
	if in.DeadAir > 0 {
		a.Details = append(a.Details, "Silences of "+strconv.FormatFloat(in.DeadAir, 'f', -1, 64)+" s or more count as long")
	}
	a.Details = append(a.Details, "Each one becomes a suggestion to accept or skip; nothing in the cut changes yet")
	if n := info.Cleanup.Accepted + info.Cleanup.Skipped; n > 0 {
		a.Details = append(a.Details, "What was decided before is kept")
	}
	setInput(a, "conversation.cleanup", in)
}

// conversationResult is what a conversation card did, in plain words, and
// where to see it; forClaude has the IDs Claude carries on with.
func conversationResult(res any) (result, link, forClaude string, ok bool) {
	switch o := res.(type) {
	case *conversation.CallPlanOut:
		result, link, forClaude = appliedResult(o.Summary, o.Applied)
		return result, link, forClaude, true
	case *conversation.CallDecideOut:
		result, link, forClaude = appliedResult(o.Summary, o.Applied)
		return result, link, forClaude, true
	case *conversation.CallShortOut:
		// The new project and its cut, each in parentheses on its own: the
		// note Claude reads keeps them, the dashboard drops them.
		result = plainSummary(o.Summary)
		if o.ProjectID != "" {
			link, forClaude = "/p/"+o.ProjectID, " ("+o.ProjectID+")"
		}
		if o.Applied != nil && o.Applied.RevisionID != "" {
			forClaude += " (" + o.Applied.RevisionID + ")"
		}
		return result, link, forClaude, true
	case *conversation.CallCleanupOut:
		// The suggestions themselves are for Claude to read (conversation show).
		if len(o.Conversation.ProjectIDs) > 0 {
			link = "/p/" + o.Conversation.ProjectIDs[0] + "?tab=conversation"
		}
		return plainSummary(o.Summary), link, "", true
	}
	return "", "", "", false
}

// appliedResult is a change's plain summary and the new version it made (its
// ID for Claude only).
func appliedResult(summary string, ap *voiceover.Applied) (result, link, forClaude string) {
	result = upperFirst(plainSummary(summary)) // "an automatic cut…" once "Ready: " is gone
	if ap == nil || ap.RevisionID == "" {
		return result, "", ""
	}
	return result, "/p/" + ap.ProjectID + "/r/" + ap.RevisionID, " (" + ap.RevisionID + ")"
}
