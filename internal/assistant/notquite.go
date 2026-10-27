package assistant

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
)

// "Not quite…" (D186). Every card that does something (one card, each row of
// a combined card) can go back to Claude with what should change: the card is
// marked "not quite" (who, when, their note) and the note goes to Claude at
// once, as the person's own message, asking for a new version with that
// change. Question cards already take an answer in the person's words; a
// "Keep this?" card is kept or not.

const maxNotQuite = 2000

// notQuiteShown is the person's line for a card sent back: what the thread
// shows as their message.
func notQuiteShown(title, note string) string {
	return "Not quite: " + strings.TrimSuffix(strings.TrimSpace(title), ".") + ". " + sentence(note)
}

// notQuiteLine is what Claude reads: the card, the note and what to do.
func notQuiteLine(title, note string) string {
	return notQuiteShown(title, note) + " Make a new version with that change."
}

// sentence ends a note with a full stop unless it ends with its own.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s[len(s)-1:], ".!?…") {
		return s
	}
	return s + "."
}

// checkNotQuite says what's wrong with a "Not quite" (nil when fine).
func checkNotQuite(in ConversationActIn, cards []AssistantAction) (string, error) {
	note := strings.TrimSpace(in.NotQuite)
	switch {
	case note == "":
		return "", apperr.New(apperr.InvalidInput, "Say what should change.")
	case utf8.RuneCountInString(note) > maxNotQuite:
		return "", apperr.New(apperr.InvalidInput, "That's too long (%d characters at most).", maxNotQuite)
	case in.Dismiss || in.Answer != nil:
		return "", apperr.New(apperr.InvalidInput, "Choose Not quite, Not now or an answer, not two at once.")
	}
	for _, act := range cards {
		switch {
		case act.Question != nil:
			return "", apperr.New(apperr.Conflict, "That's a question: answer it on its card in your own words.")
		case act.Keeping != nil:
			return "", apperr.New(apperr.Conflict, "A “Keep this?” card is kept or not. Say Not now, then tell Claude in a message what you'd keep instead.")
		}
	}
	return note, nil
}

// notQuite marks the cards as sent back and tells Claude at once, as the
// person's message (with any news it hadn't heard yet).
func (m *Manager) notQuite(ctx context.Context, a *app.App, c *conv, cards []AssistantAction, note string) (*ConversationActOut, error) {
	now := time.Now().UTC()
	batch := shortID("b")
	out := make([]AssistantAction, 0, len(cards))
	var lines, shown []string
	for _, act := range cards {
		act.State, act.Note = ActionNotQuite, note
		act.By, act.ByID, act.DecidedAt, act.Batch, act.Told = a.Actor.UserName, a.Actor.UserID, &now, batch, true
		p := c.cardFor(act.Turn)
		act.ForID, act.ForName = p.id, p.name
		c.putCard(act)
		out = append(out, act)
		lines = append(lines, notQuiteLine(act.Title, note))
		shown = append(shown, notQuiteShown(act.Title, note))
		m.auditAs(ctx, "assistant.action", "assistant_conversation", c.id, c.projectID(),
			map[string]any{"action": act.Kind, "op": act.Op, "state": act.State, "note": clip(note, 300)})
		m.logAction(ctx, c, act, a.Actor.UserID, a.Actor.UserName)
	}
	m.save(c, true)
	m.changed(c.id)
	if len(cards) > 1 {
		// One note for several cards: said once, after the cards.
		lines, shown = nil, nil
		var titles []string
		for _, act := range cards {
			titles = append(titles, strings.TrimSuffix(act.Title, "."))
		}
		s := "Not quite: " + strings.Join(titles, "; ") + ". " + sentence(note)
		lines, shown = []string{s + " Make new versions with that change."}, []string{s}
	}
	news, told := m.takeNews(c)
	if _, err := m.sendWithNews(ctx, c, message{prompt: strings.Join(lines, "\n"), shown: strings.Join(shown, "\n"), user: a.Actor.UserName,
		userID: a.Actor.UserID, rc: m.convContext(ctx, c), news: news}, told); err != nil {
		return nil, err
	}
	snap := m.snapshot(ctx, c)
	return &ConversationActOut{Action: out[0], Actions: out, Conversation: snap}, nil
}
