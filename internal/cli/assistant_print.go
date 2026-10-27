package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/metavert-io/metarush/internal/assistant"
)

// The Claude Code panel's conversations as plain text (D140): what the person
// reads in the dashboard, in the same words — grouped steps, cards in plain
// words, notes as their plain line, no IDs, commands, picture paths or
// version codes. --details adds the conversation's and cards' IDs, the command
// that runs a card and where pictures are; --raw the exact prompts and tool I/O.
// me is the person reading (D179): someone else's conversation says who
// started it, each message says who sent it, and cards say whom they wait for
// and who decided them when that isn't the reader.

func printConversation(w io.Writer, cv *assistant.Conversation, billing string, raw, details bool, me string) {
	fmt.Fprintln(w, cv.Title)
	where := []string{}
	if details || raw {
		where = append(where, cv.ID)
	}
	where = append(where, "on "+cv.InstallationName)
	if cv.ProjectName != "" {
		where = append(where, theirs(cv.ProjectName))
	}
	if s := convStarterWords(cv, me, ""); s != "" {
		where = append(where, s)
	}
	if (details || raw) && (cv.Model != "" || cv.Effort != "") {
		where = append(where, strings.TrimSpace(cv.Model+" "+cv.Effort))
	}
	where = append(where, convStateWords(cv, me))
	fmt.Fprintf(w, "%s%s\n", strings.Join(where, " · "), spend(billing, cv.CostUSD, raw))
	if a := cv.Account; a != nil {
		// Whose Claude answers, said on every conversation (D239).
		fmt.Fprintf(w, "Answered by %s, under %s.\n", a.Who, a.TermsName)
	}
	if cv.ProjectArchived {
		// Its project was deleted (D196): readable, continued once it is restored.
		name := "This project"
		if cv.ProjectName != "" {
			name = theirs(cv.ProjectName)
		}
		fmt.Fprintf(w, "%s is archived: restore it to carry on here.\n", name)
	}
	if raw && cv.SystemPrompt != "" {
		fmt.Fprintf(w, "\n[system rules appended to Claude Code's prompt]\n%s\n", indent(cv.SystemPrompt, "  "))
	}
	p := newThreadPrinter(w, cv.ID, billing, raw, details)
	p.me = me
	for _, a := range cv.Actions {
		p.actions[a.ID] = a
		if a.ToolUseID != "" {
			p.cardTools[a.ToolUseID] = true
		}
	}
	for _, t := range cv.Turns {
		p.turns[t.N] = t
	}
	for _, e := range cv.Events {
		if e.Kind == "tool_result" && e.Tool != nil {
			p.results[e.Tool.ID] = e.Tool
		}
	}
	for _, e := range cv.Events {
		p.event(e)
	}
	p.flush()
	for _, q := range cv.Queue {
		text := q.Prompt
		if q.Note {
			text = orText(q.Shown, assistant.PlainNote(q.Prompt))
		}
		fmt.Fprintf(w, "\n(waiting to send) %s\n", text)
	}
	if !details && !raw && cv.PendingActions > 0 {
		fmt.Fprintln(w, "\n(Add --details to see how to answer the cards from this terminal.)")
	}
}

// convStateWords says where a conversation is, for the person reading (me):
// what waits for them (the cards Claude raised answering their messages,
// D179), else for whom it waits.
func convStateWords(cv *assistant.Conversation, me string) string {
	mine, others, whom := waitingSplit(cv, me)
	switch {
	case cv.State == assistant.ConvWorking:
		return "working"
	case mine == 1:
		return "1 thing waiting for you"
	case mine > 1:
		return fmt.Sprintf("%d things waiting for you", mine)
	case others == 1:
		return "1 thing waiting for " + whom
	case others > 1:
		return fmt.Sprintf("%d things waiting for %s", others, whom)
	}
	return "ready"
}

// waitingSplit is how many cards wait for me and for others, and who the
// others are ("others" when they are several people). Without per-person
// counts (an older MetaRush) they are the person's who started it.
func waitingSplit(cv *assistant.Conversation, me string) (mine, others int, whom string) {
	ws := cv.WaitingFor
	if len(ws) == 0 && cv.PendingActions > 0 {
		ws = []assistant.WaitingCount{{UserID: cv.UserID, UserName: cv.UserName, Count: cv.PendingActions}}
	}
	var names []string
	for _, w := range ws {
		if me == "" || w.UserID == "" || w.UserID == me {
			mine += w.Count
			continue
		}
		others += w.Count
		names = append(names, orText(w.UserName, "someone else"))
	}
	switch len(names) {
	case 0:
	case 1:
		whom = names[0]
	default:
		whom = "others"
	}
	return mine, others, whom
}

// convStarterWords is "started by Maya" (after prefix) for a conversation
// someone else started, "" for the reader's own (D179).
func convStarterWords(cv *assistant.Conversation, me, prefix string) string {
	if cv.Mine || cv.UserID == "" || me == "" || cv.UserID == me {
		return ""
	}
	return prefix + "started by " + orText(cv.UserName, "someone else")
}

func indent(s, pre string) string {
	return pre + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+pre)
}

// threadPrinter prints transcript events as a readable thread. Consecutive
// steps of one kind print as one line (ToolCall.Group/Many), so it holds the
// latest steps until something else comes.
type threadPrinter struct {
	w         io.Writer
	billing   string
	raw       bool
	details   bool
	convID    string
	actions   map[string]assistant.AssistantAction
	turns     map[int]assistant.Turn
	results   map[string]*assistant.ToolCall
	cardTools map[string]bool // tool calls a card stands for (not repeated as steps)
	pending   []*assistant.ToolCall
	me        string // the person reading (D179)
}

func newThreadPrinter(w io.Writer, convID, billing string, raw, details bool) *threadPrinter {
	return &threadPrinter{w: w, billing: billing, raw: raw, details: details, convID: convID, actions: map[string]assistant.AssistantAction{},
		turns: map[int]assistant.Turn{}, results: map[string]*assistant.ToolCall{}, cardTools: map[string]bool{}}
}

func (p *threadPrinter) event(e assistant.AssistantEvent) {
	w := p.w
	switch e.Kind {
	case "tool":
		if e.Tool == nil {
			return
		}
		if n := len(p.pending); n > 0 && (e.Tool.Group == "" || p.pending[n-1].Group != e.Tool.Group) {
			p.flush()
		}
		p.pending = append(p.pending, e.Tool)
		return
	case "tool_result":
		if e.Tool != nil {
			p.results[e.Tool.ID] = e.Tool
		}
		return
	}
	p.flush()
	switch e.Kind {
	case "turn":
		who := orText(e.User, "You")
		if e.State == "note" {
			fmt.Fprintf(w, "\n── From the dashboard%s\n", p.turnContext(e))
			text := e.Text
			if t, ok := p.turns[e.Turn]; !ok || t.Shown == "" {
				text = assistant.PlainNote(text) // a note from before notes had a plain line of their own
			}
			for _, l := range strings.Split(text, "\n") {
				fmt.Fprintf(w, "%s\n", l)
			}
		} else {
			answering := ""
			if a, ok := p.actions[e.ActionID]; ok && a.Question != nil {
				answering = ", answering " + theirs(a.Question.Text)
			}
			fmt.Fprintf(w, "\n── %s%s%s\n%s\n", who, answering, p.turnContext(e), e.Text)
		}
		if t, ok := p.turns[e.Turn]; ok && p.raw && t.Sent != "" {
			fmt.Fprintf(w, "[sent to Claude Code]\n%s\n", indent(t.Sent, "  "))
		}
	case "text":
		fmt.Fprintf(w, "\nClaude: %s\n", e.Text)
	case "action":
		a, ok := p.actions[e.ActionID]
		if !ok {
			return
		}
		p.card(a)
	case "proposals":
		if p.details || p.raw {
			fmt.Fprintf(w, "  ✎ %d proposal(s): metarush assistant proposals %s\n", e.Count, p.convID)
		} else if e.Count == 1 {
			fmt.Fprintln(w, "  ✎ Claude wrote a change for you to look at (its card is in the dashboard)")
		} else {
			fmt.Fprintf(w, "  ✎ Claude wrote %d changes for you to look at (their cards are in the dashboard)\n", e.Count)
		}
	case "notice":
		fmt.Fprintf(w, "  (%s)\n", e.Text)
	case "error":
		fmt.Fprintf(w, "  ! %s\n", e.Text)
	case "turn_end":
		fmt.Fprintf(w, "  — %s\n", turnFooter(e, p.billing, p.raw))
	}
}

// turnContext is "  (“fernbrook”, version 1)" after who wrote a message.
func (p *threadPrinter) turnContext(e assistant.AssistantEvent) string {
	if e.Context == nil || e.Context.ProjectName == "" {
		return ""
	}
	s := "  (" + theirs(e.Context.ProjectName)
	if e.Context.HeadRevisionNumber > 0 {
		s += fmt.Sprintf(", version %d", e.Context.HeadRevisionNumber)
	}
	return s + ")"
}

// flush prints the steps held so far as one line per group.
func (p *threadPrinter) flush() {
	if len(p.pending) == 0 {
		return
	}
	rows := p.pending
	p.pending = nil
	var shown []*assistant.ToolCall
	for _, t := range rows {
		if !p.cardTools[t.ID] { // the card says it
			shown = append(shown, t)
		}
	}
	if len(shown) == 0 {
		return
	}
	first := shown[0]
	label := orText(first.Label, "Worked on it")
	if len(shown) > 1 && first.Many != "" {
		label = first.Many
		if strings.Contains(label, "%d") {
			label = fmt.Sprintf(label, len(shown))
		}
	}
	last := p.results[shown[len(shown)-1].ID]
	switch {
	case last != nil && last.Denied && !strings.Contains(label, "isn't allowed"):
		label += " → not allowed here"
	case last != nil && last.Outcome != "" && !last.Denied:
		label += " → " + last.Outcome
	}
	fmt.Fprintln(p.w, "  · "+label)
	for _, t := range shown {
		for _, td := range t.Todos {
			mark := "[ ]"
			switch td.Status {
			case "completed":
				mark = "[x]"
			case "in_progress":
				mark = "[~]"
			}
			fmt.Fprintf(p.w, "      %s %s\n", mark, td.Content)
		}
		if p.details || p.raw {
			for _, img := range t.Images {
				fmt.Fprintf(p.w, "      picture: %s/%s\n", img.Root, img.Path)
			}
		}
		if p.raw {
			if t.Summary != "" {
				fmt.Fprintf(p.w, "      $ %s\n", t.Summary)
			}
			if r := p.results[t.ID]; r != nil && r.Output != "" {
				fmt.Fprintf(p.w, "%s\n", indent(clipText(r.Output, 1200), "      | "))
			}
		}
	}
}

// card prints an action or question card.
func (p *threadPrinter) card(a assistant.AssistantAction) {
	w := p.w
	if q := a.Question; q != nil {
		asks := "you"
		if p.forSomeoneElse(a) {
			asks = orText(a.ForName, "someone else")
		}
		fmt.Fprintf(w, "  ? Claude asks %s: %s — %s\n", asks, q.Text, p.cardStateWords(a))
		if q.Why != "" {
			fmt.Fprintf(w, "      %s\n", q.Why)
		}
		for i, c := range q.Choices {
			fmt.Fprintf(w, "      %d. %s\n", i+1, c)
		}
		switch {
		case a.State == assistant.ActionDone && a.Result != "" && p.bySomeoneElse(a):
			fmt.Fprintf(w, "      %s's answer: %s\n", a.By, a.Result)
		case a.State == assistant.ActionDone && a.Result != "":
			fmt.Fprintf(w, "      Your answer: %s\n", a.Result)
		case a.State == assistant.ActionPending && (p.details || p.raw):
			fmt.Fprintf(w, "      answer it: metarush assistant act %s %s --choice <n> | --answer \"…\"   (or in the dashboard)\n", p.convID, a.ID)
		}
		return
	}
	fmt.Fprintf(w, "  ▸ Claude asks: %s — %s\n", a.Title, p.cardStateWords(a))
	switch {
	case a.AutoPriced:
		fmt.Fprintln(w, "      Started by itself (Auto, with your estimates off: within your budgets and limits)")
	case a.Auto && !a.AutoMode:
		fmt.Fprintln(w, "      Started by itself (your setting for safe steps)")
	}
	for _, d := range a.Details {
		fmt.Fprintf(w, "      %s\n", d)
	}
	// What it costs, part by part (D208): while it waits, or with --details.
	if e := a.Estimate; e != nil && (a.State == assistant.ActionPending || p.details) {
		writeCost(w, "      ", *e, p.details)
	}
	switch a.State {
	case assistant.ActionPending:
		if a.Op != "" && (p.details || p.raw) {
			fmt.Fprintf(w, "      run it: metarush assistant act %s %s   (or in the dashboard)\n", p.convID, a.ID)
		}
	case assistant.ActionUnavailable:
		fmt.Fprintf(w, "      %s\n", a.Unavailable)
	case assistant.ActionDone:
		fmt.Fprintf(w, "      %s\n", a.ShownLine()) // how its work ended, once it has
		if wk := a.Work; wk != nil && wk.State == assistant.WorkRunning {
			fmt.Fprintf(w, "      Working on %s; Claude hears when it's done\n", wk.What)
		}
	case assistant.ActionFailed:
		fmt.Fprintf(w, "      %s\n", a.Error)
	}
	if p.details || p.raw {
		for _, f := range a.Frames {
			fmt.Fprintf(w, "      picture: %s\n", f.URL)
		}
	}
	if p.raw && a.Command != "" {
		fmt.Fprintf(w, "      $ %s\n", a.Command)
	}
}

// cardStateWords says where a card is, for the person who just decided it.
func cardStateWords(a assistant.AssistantAction) string { return (&threadPrinter{}).cardStateWords(a) }

// forSomeoneElse reports whether a card is for someone other than the reader
// (D179: the person whose message Claude was answering).
func (p *threadPrinter) forSomeoneElse(a assistant.AssistantAction) bool {
	return p.me != "" && a.ForID != "" && a.ForID != p.me
}

// bySomeoneElse reports whether someone other than the reader decided a card.
func (p *threadPrinter) bySomeoneElse(a assistant.AssistantAction) bool {
	return p.me != "" && a.ByID != "" && a.ByID != p.me && a.By != ""
}

// cardStateWords says where a card is, for the person reading: whom it waits
// for and who decided it, when that isn't them (D179).
func (p *threadPrinter) cardStateWords(a assistant.AssistantAction) string {
	by := ""
	if p.bySomeoneElse(a) {
		by = a.By
	}
	switch a.State {
	case assistant.ActionPending:
		if p.forSomeoneElse(a) {
			return "waiting for " + orText(a.ForName, "someone else")
		}
		return "waiting for you"
	case assistant.ActionRunning:
		return "running by itself"
	case assistant.ActionDone:
		done := "done"
		if a.Question != nil {
			done = "answered"
		}
		if by != "" {
			return done + " by " + by
		}
		if a.AutoPriced {
			return "done by itself (Auto, estimates off)"
		}
		if a.AutoMode {
			return "done by itself (Auto)"
		}
		return done
	case assistant.ActionNotQuite:
		if by != "" {
			return by + " said not quite"
		}
		return "not quite"
	case assistant.ActionDismissed:
		if by != "" {
			return by + " said not now"
		}
		return "not now"
	case assistant.ActionFailed:
		if by != "" {
			return "didn't work (tried by " + by + ")"
		}
		return "didn't work"
	case assistant.ActionUnavailable:
		return "not from here"
	}
	return a.State
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// turnFooter is "3 min 27 s · 33 steps" (+ cost with an API key, or the
// estimate in raw view).
func turnFooter(e assistant.AssistantEvent, billing string, raw bool) string {
	parts := []string{}
	if e.State != "" && e.State != assistant.TurnSucceeded {
		parts = append(parts, turnStateWords(e.State))
	}
	if r := e.Result; r != nil {
		if r.DurationMs > 0 {
			parts = append(parts, secondsWords(float64(r.DurationMs)/1000))
		}
		if r.Steps > 0 {
			parts = append(parts, fmt.Sprintf("%d steps", r.Steps))
		}
		if r.Tokens != nil && r.Tokens.Total > 0 {
			parts = append(parts, assistant.Millions(r.Tokens.Total)+" tokens")
		}
		if r.ContextTokens > 0 && raw {
			parts = append(parts, fmt.Sprintf("%dk context", (r.ContextTokens+500)/1000))
		}
		if s := strings.TrimSpace(spend(billing, r.CostUSD, raw)); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return "done"
	}
	return strings.Join(parts, " · ")
}

func turnStateWords(state string) string {
	switch state {
	case assistant.TurnInterrupted:
		return "interrupted"
	case assistant.TurnStopped:
		return "stopped"
	case assistant.TurnTimedOut:
		return "took too long"
	case assistant.TurnFailed:
		return "didn't finish"
	case assistant.TurnBudget:
		return "reached the limit"
	case assistant.TurnLost:
		return "cut off"
	}
	return strings.ReplaceAll(state, "_", " ")
}

// secondsWords is how long something took: "8 s", "3 min 27 s".
func secondsWords(s float64) string {
	n := int(s + 0.5)
	if n < 60 {
		if s < 10 {
			return fmt.Sprintf("%.1f s", s)
		}
		return fmt.Sprintf("%d s", n)
	}
	if n%60 == 0 {
		return fmt.Sprintf("%d min", n/60)
	}
	return fmt.Sprintf("%d min %d s", n/60, n%60)
}
