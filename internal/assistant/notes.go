package assistant

import (
	"context"
	"strings"
	"time"
)

// Telling Claude what happened to its cards (D142). Every outcome — a card a
// person ran, set aside or that started by itself, and work such a card
// started (a render and its quality check, a snapshot, an import) — is told
// to Claude once, by tellClaude, in one note: Claude carries on from it by
// itself. Cards decided together (one batch) are told together, once their
// work has finished; nothing is told when work merely starts. A note waiting
// behind a running turn takes in later news (send merges queued notes), and a
// message the person types takes along what Claude hasn't heard yet.
//
// With assistant.holdNews (the default, D181) only news Claude has to carry on
// from starts a note by itself — a safe step that ran by itself, or something
// that failed — and the rest waits for the person's next message: a note is a
// message of its own, which reads the whole conversation again.

// defaultTellDelay lets work that finishes close together make one note.
const defaultTellDelay = 2 * time.Second

func (m *Manager) tellDelay() time.Duration {
	if m.tellAfter > 0 {
		return m.tellAfter
	}
	return defaultTellDelay
}

// claudeLine is what Claude is told about a decided card: the outcome with
// what it needs to carry on (IDs, versions, exact problems).
func (a *AssistantAction) claudeLine() string {
	title := strings.TrimSuffix(a.Title, ".")
	switch a.State {
	case ActionDismissed:
		if a.Keeping != nil {
			// Keeping's Not now declines for good (D166).
			return "Not kept: " + title + ". MetaRush won't offer it again in this project."
		}
		// A request set aside, not refused (D186): it may come back.
		return "Not now: " + title + ". Leave it for now; I may come back to it."
	case ActionNotQuite:
		return notQuiteLine(title, a.Note)
	case ActionFailed:
		switch {
		case a.AutoPriced:
			return "Started by itself (the person's Auto setting, with their estimates off), but it failed: " + title + ". It failed: " + a.Error
		case a.AutoMode:
			return "Started by itself (the person's Auto setting), but it failed: " + title + ". It failed: " + a.Error
		case a.Auto:
			return "Started by itself (the person's setting for safe steps), but it failed: " + title + ". It failed: " + a.Error
		}
		return "Tried from the dashboard: " + title + ". It failed: " + a.Error
	}
	lead := "Done from the dashboard: "
	switch {
	case a.AutoPriced:
		lead = "Done by itself (the person's Auto setting, with their estimates off: changes that cost money run within their budgets without a click): "
	case a.AutoMode:
		lead = "Done by itself (the person's Auto setting runs changes that can be undone without a click): "
	case a.Auto:
		lead = "Done by itself (the person's setting runs safe steps without a click): "
	}
	s := lead + title + ". Result: " + strings.TrimSuffix(a.Result, ".") + a.ForClaude
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	if w := a.Work; w != nil && w.State != WorkRunning {
		s += " Then: " + strings.TrimSpace(w.ForClaude)
	}
	return s
}

// ShownLine is the person's line for the same outcome: plain words, no IDs
// (the card, the note to Claude and the CLI show it).
func (a *AssistantAction) ShownLine() string {
	title := strings.TrimSuffix(a.Title, ".")
	switch a.State {
	case ActionDismissed:
		if a.Keeping != nil {
			return "Not kept: " + title
		}
		return "Not now: " + title
	case ActionNotQuite:
		return notQuiteShown(title, a.Note)
	case ActionFailed:
		return title + " didn't work: " + firstLine(a.Error)
	}
	if w := a.Work; w != nil && w.State != WorkRunning && w.Outcome != "" {
		if a.SavesVersion && strings.HasPrefix(a.Result, "Saved as ") {
			// "Saved as version 2. The review copy of version 2 is ready…"
			return strings.TrimSuffix(strings.SplitN(a.Result, ". ", 2)[0], ".") + ". " + w.Outcome
		}
		return w.Outcome
	}
	return a.Result
}

// heard reports whether Claude has heard this decided card's outcome: told by
// a note, or decided by an earlier MetaRush (those cards carry no batch; it told
// Claude the moment they were decided). Every decision now gets a batch.
func (a *AssistantAction) heard() bool { return a.Told || a.Batch == "" }

// untold reports whether this card's outcome is still to be told to Claude.
func (a *AssistantAction) untold() bool {
	switch {
	case a.heard(), a.State == ActionPending, a.State == ActionRunning, a.State == ActionUnavailable, a.Question != nil && a.State == ActionDone:
		return false
	}
	return true
}

// untoldCount is stored with the conversation, so a MetaRush that stopped
// before telling Claude tells it when it starts again.
func untoldCount(as []AssistantAction) int {
	n := 0
	for i := range as {
		if as[i].untold() {
			n++
		}
	}
	return n
}

// readyLocked marks and returns the outcomes Claude hasn't heard yet that are
// ready to tell (caller holds c.mu): decided or finished, with no work still
// running in their batch. from is who the note comes from (D179): the person
// who made the latest of these decisions, else (a safe step, work that
// finished by itself) the person the card was for.
func (c *conv) readyLocked() (lines, shown []string, from who) {
	lines, shown, from, _ = c.takeReadyLocked(false)
	return lines, shown, from
}

// urgent reports whether Claude has to hear an outcome at once to carry on: a
// safe step that ran by itself, or something that failed (D181).
func (a *AssistantAction) urgent() bool { return a.Auto || a.State == ActionFailed }

// takeReadyLocked is readyLocked; with urgentOnly it takes nothing unless one
// of the ready outcomes is urgent (then all of them go together). told lists
// the cards it marked, for untellLocked when the note couldn't be sent.
func (c *conv) takeReadyLocked(urgentOnly bool) (lines, shown []string, from who, told []string) {
	busy := map[string]bool{} // batches with something still running
	for _, a := range c.data.Actions {
		if a.Batch != "" && (a.State == ActionRunning || (a.Work != nil && a.Work.State == WorkRunning)) {
			busy[a.Batch] = true
		}
	}
	ready := func(a *AssistantAction) bool {
		switch {
		case !a.untold():
			return false
		case a.Work != nil && a.Work.State == WorkRunning:
			return false
		case a.Batch != "" && busy[a.Batch]:
			return false
		}
		return true
	}
	if urgentOnly {
		any := false
		for i := range c.data.Actions {
			if a := &c.data.Actions[i]; ready(a) && a.urgent() {
				any = true
			}
		}
		if !any {
			return nil, nil, who{}, nil
		}
	}
	var latest time.Time
	for i := range c.data.Actions {
		a := &c.data.Actions[i]
		if !ready(a) {
			continue
		}
		a.Told = true
		told = append(told, a.ID)
		lines = append(lines, a.claudeLine())
		shown = append(shown, a.ShownLine())
		p, at := who{a.ByID, a.By}, a.CreatedAt
		if p.id == "" {
			p = c.data.sender(a.Turn)
		}
		if a.DecidedAt != nil {
			at = *a.DecidedAt
		}
		if from.id == "" || !at.Before(latest) {
			from, latest = p, at
		}
	}
	return lines, shown, from, told
}

// untellLocked marks outcomes as not told again: the message carrying them
// wasn't sent (the conversation reached its limit, its queue was full).
func (c *conv) untellLocked(ids []string) {
	for _, id := range ids {
		for i := range c.data.Actions {
			if c.data.Actions[i].ID == id {
				c.data.Actions[i].Told = false
			}
		}
	}
}

// tellClaude sends Claude one note with every outcome it hasn't heard yet
// that is ready (on the machine that holds the conversation, from the daemon).
func (m *Manager) tellClaude(c *conv) {
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed || m.terminal || !c.here(m) {
		// A MetaRush that is stopping starts no Claude Code, nor does a terminal
		// command (it would end with the command): the daemon tells Claude when
		// it starts, or the person's next message takes the news along.
		return
	}
	c.mu.Lock()
	lines, shown, from, told := c.takeReadyLocked(m.App.Cfg.Assistant.HoldsNews())
	c.mu.Unlock()
	if len(lines) == 0 {
		return // held for the person's next message (D181), or nothing new
	}
	m.save(c, true)
	m.changed(c.id)
	if _, err := m.send(context.Background(), c, message{prompt: strings.Join(lines, "\n"), shown: strings.Join(shown, "\n"), user: from.name, userID: from.id,
		rc: m.convContext(context.Background(), c), note: true}); err != nil {
		c.mu.Lock()
		c.untellLocked(told) // the next message takes them along
		c.mu.Unlock()
		m.save(c, true)
	}
}

// tellClaudeSoon tells Claude a moment after the last change, so outcomes
// arriving close together make one note.
func (m *Manager) tellClaudeSoon(c *conv) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tellTimer != nil {
		c.tellTimer.Stop()
	}
	c.tellTimer = time.AfterFunc(m.tellDelay(), func() {
		c.mu.Lock()
		c.tellTimer = nil
		c.mu.Unlock()
		m.tellClaude(c)
	})
}

// takeNews returns (and marks told) the outcomes Claude hasn't heard yet, to
// go with a message the person sends; told lists them for untellLocked when
// the message isn't sent.
func (m *Manager) takeNews(c *conv) (lines, told []string) {
	if m.terminal && m.checkConvWork(c) {
		m.save(c, true) // nothing follows the work in a terminal: bring it up to date first
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	lines, _, _, told = c.takeReadyLocked(false)
	return lines, told
}

// sendWithNews sends a person's message with the news it takes along; news
// that couldn't go stays untold.
func (m *Manager) sendWithNews(ctx context.Context, c *conv, msg message, told []string) (*ConversationSendOut, error) {
	out, err := m.send(ctx, c, msg)
	if err != nil && out == nil && len(told) > 0 {
		c.mu.Lock()
		c.untellLocked(told)
		c.mu.Unlock()
		m.save(c, true)
	}
	return out, err
}
