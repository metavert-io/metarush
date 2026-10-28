package assistant

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/linkimport"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// Safe steps run by themselves (D141). With the person's setting on
// (assistant.safeSteps, default on), a card for a step that is free, local and
// reversible starts as soon as Claude asks: checking a motion graphic Claude
// wrote in this conversation, a review copy, and bringing in a link
// the person typed in this conversation (a page's snapshot, a repository, a
// download, a video import without a playlist or a browser sign-in).
// Approvals, saving versions, final renders, exports and anything that spends
// money always wait for a click. A safe step runs as the person the card is
// for (D179) on the human channel (Via assistant-auto), never on Claude's
// read-only one; the audit and the activity log say Claude started it under
// the setting.

// safeStep reports whether a card may start by itself.
func (c *conv) safeStep(a *AssistantAction) bool {
	if a.State != ActionPending || a.Op == "" || a.HumanOnly || len(a.SetUp) > 0 || a.Question != nil {
		return false
	}
	switch a.Op {
	case "motion.check":
		return true // the folder is this conversation's (describeMotionCheck)
	case "render.submit":
		var in ops.RenderSubmitIn
		return json.Unmarshal([]byte(a.Input), &in) == nil && (in.Quality == "" || in.Quality == "review")
	case "resources.fetch":
		var in ops.ResourceFetchIn
		return json.Unmarshal([]byte(a.Input), &in) == nil && c.personTyped(in.URL)
	case "ingest.url":
		var in linkimport.ImportIn
		if json.Unmarshal([]byte(a.Input), &in) != nil || in.Playlist > 0 || in.BrowserSignIn != "" || in.DryRun || len(in.URLs) == 0 {
			return false
		}
		for _, u := range in.URLs {
			if !c.personTyped(u) {
				return false
			}
		}
		return true
	}
	return false
}

// linkInTextRe finds web addresses in what a person typed: any address with
// http(s):// ("https://www.fernbrook.example/about", "http://192.168.1.4/page"), or
// a bare domain ("fernbrook.example.org").
var linkInTextRe = regexp.MustCompile(`(?i)\bhttps?://[^\s<>"'()\[\]{}]+|\b(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,24}(?::\d{1,5})?(?:/[^\s<>"'()\[\]{}]*)?`)

// personTyped reports whether a link appears in the person's own messages of
// this conversation (dashboard notes and anything Claude read don't count).
func (c *conv) personTyped(link string) bool {
	want := linkKey(link)
	if want == "" {
		return false
	}
	c.mu.Lock()
	var texts []string
	for _, t := range c.data.Turns {
		if !t.Note {
			texts = append(texts, t.Prompt)
		}
	}
	for _, q := range c.data.Queue {
		if !q.Note {
			texts = append(texts, q.Prompt)
		}
	}
	c.mu.Unlock()
	for _, t := range texts {
		for _, m := range linkInTextRe.FindAllString(t, -1) {
			if linkKey(strings.TrimRight(m, ".,;:!?")) == want {
				return true
			}
		}
	}
	return false
}

// linkKey compares links the way a person means them: the scheme, "www.", a
// trailing slash and a fragment don't matter; the host's case doesn't either.
func linkKey(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	key := host + strings.TrimRight(u.EscapedPath(), "/")
	if u.RawQuery != "" {
		key += "?" + u.RawQuery
	}
	return key
}

// SafeStepsOn reports the person's setting (default on).
func (m *Manager) safeStepsOn() bool { return m.App.Cfg.Assistant.RunsSafeSteps() }

// autoApp is the app a safe step runs with: the person the card is for (whose
// message Claude was answering, D179), on the human channel, marked as started
// by itself.
func (m *Manager) autoApp(c *conv, act AssistantAction) *app.App {
	a := *m.App
	c.mu.Lock()
	p := c.data.sender(act.Turn)
	c.mu.Unlock()
	if p.id == "" {
		p = who{m.App.Cfg.User.ID, m.App.Cfg.User.Name}
	}
	a.Actor = models.Actor{UserID: p.id, UserName: p.name, InstallationID: m.App.Cfg.Installation.ID, Via: models.ViaAssistantAuto}
	return &a
}

// runAuto runs a card that started by itself (a safe step, or a change Auto
// runs, D186), then tells Claude (now or, for work that takes a while, when it
// has finished). A card that saves a version renders its review copy too.
func (m *Manager) runAuto(c *conv, id string) {
	ctx := context.Background()
	cards, err := c.cardsFor([]string{id})
	if err != nil || cards[0].State != ActionRunning {
		return
	}
	act := cards[0]
	// Only while this computer holds the conversation and isn't handing it
	// over (D345): otherwise it waits for a person where it goes.
	if _, err := m.claimCards(ctx, c, []string{id}, ActionPending, ActionRunning); err != nil {
		if errReason(err) == "handing_over" {
			c.mu.Lock()
			for i := range c.data.Actions {
				if a := &c.data.Actions[i]; a.ID == id && a.State == ActionRunning {
					a.State, a.Auto, a.AutoMode, a.AutoPriced = ActionPending, false, false, false
				}
			}
			c.touch()
			c.mu.Unlock()
			m.save(c, true)
			m.changed(c.id)
		}
		return
	}
	a := m.autoApp(c, act)
	done, err := m.runCard(ctx, a, ops.Caller{}, c, act, ConversationActIn{ReviewCopy: act.SavesVersion})
	if err != nil {
		// A check before the change failed: the card says so (it ran by itself).
		done = act
		done.State, done.Error = ActionFailed, apperr.From(err).Message
	}
	now := time.Now().UTC()
	done.DecidedAt = &now
	c.putCard(done)
	setting := "assistant.safeSteps"
	if done.AutoMode {
		setting = "assistant.mode"
	}
	a.Audit(ctx, "assistant.action", "assistant_conversation", c.id, c.projectID(),
		map[string]any{"action": done.Kind, "op": done.Op, "state": done.State, "auto": true, "setting": setting, "reviewCopy": done.SavesVersion})
	m.logAction(ctx, c, done, a.Actor.UserID, a.Actor.UserName)
	if done.AutoPriced {
		m.recordAutoSpend(ctx, c, done, a.Actor) // the receipt and the bell (D241)
	}
	m.save(c, true)
	m.changed(c.id)
	m.ensureWatching()
	m.tellClaudeSoon(c)
}

// addAuto starts a card MetaRush just added by itself, when it may (the
// caller has added it after startAuto said yes).
func (m *Manager) addAuto(c *conv, id string) {
	m.save(c, true)
	go m.runAuto(c, id)
}
