package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
)

// Question cards (D143). Claude asks the person something by writing
// <proposals>/<short-name>.question.json and ending its reply: a question, up
// to twelve choices to pick one of (or several, with multiple), a box for an
// answer of their own (other, or a question without choices), and why it
// asks. At the end of the turn each file becomes a card; the person's answer
// goes to Claude as their reply, and the card counts with the others waiting
// for them (the list, the navigator, the bell).

// QuestionSuffix ends a question file in a turn's proposals folder.
const QuestionSuffix = ".question.json"

const (
	maxChoices      = 12
	maxQuestionLen  = 300
	maxChoiceLen    = 200
	maxAnswerText   = 2000
	maxQuestionsOne = 6 // question cards per turn
)

// QuestionCard is what a question card asks.
type QuestionCard struct {
	Text     string   `bson:"text" json:"text"`
	Choices  []string `bson:"choices,omitempty" json:"choices,omitempty" jsonschema:"description=Answers to pick from (none: a free-text question)."`
	Multiple bool     `bson:"multiple,omitempty" json:"multiple,omitempty" jsonschema:"description=Several choices may be picked (tick boxes)."`
	Other    bool     `bson:"other,omitempty" json:"other,omitempty" jsonschema:"description=The person may also write an answer of their own."`
	Why      string   `bson:"why,omitempty" json:"why,omitempty" jsonschema:"description=Why Claude asks (shown under the question)."`
}

// QuestionAnswer is a person's answer to a question card.
type QuestionAnswer struct {
	Choices []int  `bson:"choices,omitempty" json:"choices,omitempty" jsonschema:"maxItems=12,description=The picked choices\\, numbered from 1."`
	Text    string `bson:"text,omitempty" json:"text,omitempty" jsonschema:"maxLength=2000,description=An answer in the person's own words."`
}

// questionFile is the file Claude writes (lenient: what it says is clipped,
// never refused for length).
type questionFile struct {
	Question string   `json:"question"`
	Choices  []string `json:"choices"`
	Multiple bool     `json:"multiple"`
	Other    *bool    `json:"other"`
	Why      string   `json:"why"`
}

// parseQuestion reads a question file into a card's question, or says why it can't.
func parseQuestion(raw []byte) (*QuestionCard, string) {
	var f questionFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, "it isn't a JSON object with a question"
	}
	q := &QuestionCard{Text: oneLine(f.Question, maxQuestionLen), Multiple: f.Multiple, Why: oneLine(f.Why, 500)}
	if utf8.RuneCountInString(q.Text) < 3 {
		return nil, "it has no question"
	}
	seen := map[string]bool{}
	for _, c := range f.Choices {
		c = oneLine(c, maxChoiceLen)
		if c == "" || seen[strings.ToLower(c)] {
			continue
		}
		seen[strings.ToLower(c)] = true
		q.Choices = append(q.Choices, c)
		if len(q.Choices) == maxChoices {
			break
		}
	}
	if len(q.Choices) < 2 {
		q.Choices, q.Multiple = nil, false
	}
	switch {
	case len(q.Choices) == 0:
		q.Other = true // a free-text question
	case f.Other != nil:
		q.Other = *f.Other
	}
	return q, ""
}

func oneLine(s string, n int) string { return clip(strings.Join(strings.Fields(s), " "), n) }

// addQuestionCards makes a card for every question file a turn wrote.
func (m *Manager) addQuestionCards(c *conv, turn int) {
	c.mu.Lock()
	if turn < 1 || turn > len(c.data.Turns) {
		c.mu.Unlock()
		return
	}
	t := c.data.Turns[turn-1]
	covered := map[string]bool{}
	for _, a := range c.data.Actions {
		if a.Proposal != "" {
			covered[a.Proposal] = true
		}
	}
	c.mu.Unlock()
	if t.Folder == "" {
		return
	}
	dir := filepath.Join(m.dir(), filepath.FromSlash(t.Folder))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), QuestionSuffix) && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) > maxQuestionsOne {
		names = names[:maxQuestionsOne]
	}
	added := false
	for _, name := range names {
		ref := t.RefPrefix + name
		if covered[ref] {
			continue
		}
		raw, err := readProposal(dir, name)
		if err != nil {
			continue
		}
		q, why := parseQuestion(raw)
		if q == nil {
			c.mu.Lock()
			c.addEvent(AssistantEvent{Kind: "notice", Turn: turn, Text: "Claude's question couldn't be shown: " + why + "."})
			c.mu.Unlock()
			continue
		}
		a := AssistantAction{ID: shortID("act"), Turn: turn, Kind: "question", Title: q.Text, Question: q, Proposal: ref, State: ActionPending,
			CreatedAt: time.Now().UTC()}
		c.mu.Lock()
		c.data.Actions = append(c.data.Actions, a)
		if len(c.data.Actions) > maxActions {
			c.data.Actions = c.data.Actions[len(c.data.Actions)-maxActions:]
		}
		c.addEvent(AssistantEvent{Kind: "action", Turn: turn, ActionID: a.ID, Text: a.Title})
		c.touch()
		c.mu.Unlock()
		added = true
	}
	if added {
		m.changed(c.id)
	}
}

// answerText is an answer in words: the picked choices, then the person's own.
func answerText(q *QuestionCard, ans QuestionAnswer) string {
	var parts []string
	for _, n := range ans.Choices {
		parts = append(parts, q.Choices[n-1])
	}
	s := strings.Join(parts, "; ")
	if t := strings.TrimSpace(ans.Text); t != "" {
		s = joinSentences(s, t)
	}
	return s
}

func joinSentences(a, b string) string {
	if a == "" {
		return b
	}
	return strings.TrimRight(a, ".") + ". " + b
}

// checkAnswer says what's wrong with an answer, in plain words ("" when fine).
func checkAnswer(q *QuestionCard, ans *QuestionAnswer) string {
	ans.Text = strings.TrimSpace(ans.Text)
	if utf8.RuneCountInString(ans.Text) > maxAnswerText {
		return fmt.Sprintf("The answer is too long (%d characters at most).", maxAnswerText)
	}
	seen := map[int]bool{}
	var picked []int
	for _, n := range ans.Choices {
		if n < 1 || n > len(q.Choices) {
			return "That choice isn't one of the question's."
		}
		if !seen[n] {
			seen[n] = true
			picked = append(picked, n)
		}
	}
	sort.Ints(picked)
	ans.Choices = picked
	switch {
	case len(picked) == 0 && ans.Text == "":
		if len(q.Choices) == 0 {
			return "Write an answer first."
		}
		return "Pick an answer first."
	case len(picked) > 1 && !q.Multiple:
		return "Pick one answer."
	case ans.Text != "" && len(q.Choices) > 0 && !q.Other:
		return "Pick one of the answers."
	}
	return ""
}

// answerQuestion records a person's answer to a question card and sends it
// to Claude as their reply (with the question, so Claude knows what it answers).
func (m *Manager) answerQuestion(ctx context.Context, a *app.App, c *conv, act AssistantAction, ans QuestionAnswer) (*ConversationActOut, error) {
	if why := checkAnswer(act.Question, &ans); why != "" {
		return nil, apperr.New(apperr.InvalidInput, "%s", why)
	}
	text := answerText(act.Question, ans)
	now := time.Now().UTC()
	act.State, act.Answer, act.Result, act.Told = ActionDone, &ans, text, true
	act.By, act.ByID, act.DecidedAt = a.Actor.UserName, a.Actor.UserID, &now
	p := c.cardFor(act.Turn)
	act.ForID, act.ForName = p.id, p.name
	c.putCard(act)
	m.save(c, true)
	m.changed(c.id)
	m.auditAs(ctx, "assistant.action", "assistant_conversation", c.id, c.projectID(), map[string]any{"action": "question", "state": act.State})
	m.logAction(ctx, c, act, a.Actor.UserID, a.Actor.UserName)
	if c.here(m) {
		// The answer is the reply of whoever gave it (D179), whoever was asked.
		news, told := m.takeNews(c)
		if _, err := m.sendWithNews(ctx, c, message{prompt: text, user: a.Actor.UserName, userID: a.Actor.UserID, rc: m.convContext(ctx, c), answerTo: act.ID,
			question: act.Question.Text, news: news}, told); err != nil {
			return nil, err
		}
	}
	snap := m.snapshot(ctx, c)
	return &ConversationActOut{Action: act, Actions: []AssistantAction{act}, Conversation: snap}, nil
}
