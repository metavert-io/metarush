package assistant

import (
	"context"

	"github.com/metavert-io/metarush/internal/models"
)

// Whose conversations, messages and cards are (D179). Remote access signs
// people in as themselves (D128, D129), so one conversation can hold several
// people: the person who started it (Conversation.UserID), who sent each
// message (Turn.UserID; older messages are the person's who started it) and
// who decided each card (AssistantAction.ByID). A card is for the person whose
// message Claude was answering when it asked; a note's message is from the
// person whose decision it reports (for a safe step or work that finished by
// itself, the person that card was for). A card counts only for its person:
// their badge, their navigator, their notification. Everyone who opens the
// conversation sees it in the thread, marked with whom it waits for, and may
// decide it (D129: any signed-in person may approve).

// who is a person: their ID and name.
type who struct{ id, name string }

// sender is who sent message n: its person, else (messages from before D179)
// the person who started the conversation. A live conversation's caller holds
// its lock.
func (d *Conversation) sender(n int) who {
	if n >= 1 && n <= len(d.Turns) {
		if t := d.Turns[n-1]; t.UserID != "" {
			return who{t.UserID, t.User}
		}
	}
	return who{d.UserID, d.UserName}
}

// cardFor is who a card of message n of a live conversation is for.
func (c *conv) cardFor(n int) who {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.data.sender(n)
}

// WaitingCounts is how many cards and questions wait for each person, in the
// order their first card appears (never nil, so a save clears older counts).
func (d *Conversation) WaitingCounts() []WaitingCount {
	out := []WaitingCount{}
	for _, a := range d.Actions {
		if a.State != ActionPending {
			continue
		}
		p := d.sender(a.Turn)
		found := false
		for i := range out {
			if out[i].UserID == p.id {
				out[i].Count++
				found = true
				break
			}
		}
		if !found {
			out = append(out, WaitingCount{UserID: p.id, UserName: p.name, Count: 1})
		}
	}
	return out
}

// countFor is how many of the counts are for one person ("" is nobody in
// particular: all of them).
func countFor(ws []WaitingCount, uid string) int {
	n := 0
	for _, w := range ws {
		if uid == "" || w.UserID == uid {
			n += w.Count
		}
	}
	return n
}

// personalize fills in what a conversation shown to someone says about
// people: who each card is for, who the waiting ones are for (live counts when
// the cards are here, else the stored ones; conversations saved before D179
// count them for the person who started it), whether the person asking (uid)
// started it and how many wait for them.
func (d *Conversation) personalize(uid string) {
	for i := range d.Actions {
		p := d.sender(d.Actions[i].Turn)
		d.Actions[i].ForID, d.Actions[i].ForName = p.id, p.name
	}
	switch {
	case len(d.Actions) > 0:
		d.WaitingFor = d.WaitingCounts()
	case d.PendingActions == 0:
		d.WaitingFor = nil
	case len(d.WaitingFor) == 0:
		d.WaitingFor = []WaitingCount{{UserID: d.UserID, UserName: d.UserName, Count: d.PendingActions}}
	}
	d.Mine = uid != "" && d.UserID == uid
	d.WaitingForYou = countFor(d.WaitingFor, uid)
}

// asActor is a context that audits and logs as this person (no channel of
// its own: the manager's app's is used).
func asActor(p who) context.Context {
	return context.WithValue(context.Background(), personKey{}, models.Actor{UserID: p.id, UserName: p.name})
}
