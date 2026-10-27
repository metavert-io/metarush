package assistant

import (
	"context"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Turned-down notes reach Claude (D186). Rejecting a version with "What
// should change?" or turning down a motion graphic with a note — from the
// dashboard or the command line — sends the note to the conversation that made
// it (the version's origin, else the project's latest conversation whose card
// saved it; for a graphic, the latest conversation whose card checked it), as
// that person's message ("I turned down version 7: …"), queued like any
// message. Only the machine that holds a conversation can continue it: a note
// from anywhere else (another computer, a terminal command) waits in the
// conversation's inbox until that machine's MetaRush sends it.

// InboxNote is a note for Claude from elsewhere, waiting to be sent.
type InboxNote struct {
	ID     string    `bson:"id" json:"id"`
	Prompt string    `bson:"prompt" json:"prompt"`
	Shown  string    `bson:"shown" json:"shown"`
	User   string    `bson:"user,omitempty" json:"user,omitempty"`
	UserID string    `bson:"userId,omitempty" json:"userId,omitempty"`
	At     time.Time `bson:"at" json:"at"`
}

// inboxEvery is how often the daemon looks for notes from elsewhere.
const inboxEvery = 3 * time.Second

func init() { ops.OnTurnedDown(turnedDown) }

// turnedDown sends a turned-down note to the conversation that made the thing.
func turnedDown(ctx context.Context, a *app.App, t ops.TurnedDown) *ops.NoteSent {
	m := For(a)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	id := originConversation(ctx, a, t)
	if id == "" {
		return nil
	}
	var doc struct {
		Title            string `bson:"title"`
		InstallationID   string `bson:"installationId"`
		InstallationName string `bson:"installationName"`
	}
	if err := a.DB.C(store.CAssistantConversations).FindOne(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID()},
		options.FindOne().SetProjection(bson.M{"title": 1, "installationId": 1, "installationName": 1})).Decode(&doc); err != nil {
		return nil
	}
	what := "the graphic " + theirs(t.MotionName)
	forClaude := ""
	if t.RevisionID != "" {
		what = versionWords(t.RevisionNumber)
		forClaude = " (" + t.RevisionID + ")"
	} else if t.MotionHash != "" {
		forClaude = " (" + t.MotionName + "@" + t.MotionHash[:min(12, len(t.MotionHash))] + ")"
	}
	shown := "I turned down " + what + ": " + t.Note
	note := InboxNote{ID: shortID("in"), Prompt: "I turned down " + what + forClaude + ": " + sentence(t.Note) + " Make a new version with that change.",
		Shown: shown, User: a.Actor.UserName, UserID: a.Actor.UserID, At: time.Now().UTC()}
	sent := &ops.NoteSent{ConversationID: id, Title: doc.Title}
	me, _ := m.machine()
	if !m.terminal && doc.InstallationID == me {
		if c, err := m.live(ctx, id, true); err == nil {
			if out, err := m.sendNote(ctx, c, note); err == nil {
				sent.Queued = out.Queued
				return sent
			}
		}
	}
	// The holding machine sends it (this is another computer, or a terminal
	// command that would end before Claude answered).
	if _, err := a.DB.C(store.CAssistantConversations).UpdateOne(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID()},
		bson.M{"$push": bson.M{"inbox": note}}); err != nil {
		return nil
	}
	sent.Queued, sent.Machine = true, doc.InstallationName
	return sent
}

// originConversation is the conversation that made a version or a graphic.
func originConversation(ctx context.Context, a *app.App, t ops.TurnedDown) string {
	if rest, ok := strings.CutPrefix(t.Origin, "assistant:"); ok {
		id, _, _ := strings.Cut(rest, "/")
		if ValidID(id) {
			if n, err := a.DB.C(store.CAssistantConversations).CountDocuments(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID()}); err == nil && n > 0 {
				return id
			}
		}
	}
	var or bson.A
	filter := bson.M{}
	switch {
	case t.RevisionID != "":
		re := bson.Regex{Pattern: regexp.QuoteMeta(t.RevisionID)}
		or = bson.A{bson.M{"link": re}, bson.M{"forClaude": re}, bson.M{"work.link": re}}
		if t.ProjectID != "" {
			filter["projectId"] = t.ProjectID
		}
	case t.MotionHash != "":
		or = bson.A{bson.M{"link": links.Motion(t.MotionHash)}, bson.M{"review": links.Motion(t.MotionHash)}}
	default:
		return ""
	}
	filter["actions"] = bson.M{"$elemMatch": bson.M{"state": ActionDone, "$or": or}}
	var docs []struct {
		ID string `bson:"_id"`
	}
	if err := a.DB.Find(ctx, store.CAssistantConversations, a.WorkspaceID(), filter, &docs,
		options.Find().SetSort(bson.D{{Key: "updatedAt", Value: -1}}).SetLimit(1).SetProjection(bson.M{"_id": 1})); err != nil || len(docs) == 0 {
		return ""
	}
	return docs[0].ID
}

// sendNote sends one note from elsewhere as its person's message (with any
// news Claude hasn't heard yet).
func (m *Manager) sendNote(ctx context.Context, c *conv, n InboxNote) (*ConversationSendOut, error) {
	news, told := m.takeNews(c)
	return m.sendWithNews(ctx, c, message{prompt: n.Prompt, shown: n.Shown, user: n.User, userID: n.UserID, rc: m.convContext(ctx, c), news: news}, told)
}

// takeInbox takes a conversation's waiting notes (atomically: a note pushed
// meanwhile stays for the next look).
func (m *Manager) takeInbox(ctx context.Context, id string) []InboxNote {
	var before struct {
		Inbox []InboxNote `bson:"inbox"`
	}
	err := m.App.DB.C(store.CAssistantConversations).FindOneAndUpdate(ctx,
		bson.M{"_id": id, "workspaceId": m.App.WorkspaceID(), "inbox.0": bson.M{"$exists": true}},
		bson.M{"$set": bson.M{"inbox": bson.A{}}},
		options.FindOneAndUpdate().SetReturnDocument(options.Before).SetProjection(bson.M{"inbox": 1})).Decode(&before)
	if err != nil {
		return nil
	}
	return before.Inbox
}

// deliverInbox sends the notes waiting in this machine's conversations (the
// daemon looks every few seconds; a person's next message takes them first).
func (m *Manager) deliverInbox(ctx context.Context) int {
	if m.terminal {
		return 0
	}
	me, _ := m.machine()
	var docs []struct {
		ID string `bson:"_id"`
	}
	if err := m.App.DB.Find(ctx, store.CAssistantConversations, m.App.WorkspaceID(), bson.M{"installationId": me, "inbox.0": bson.M{"$exists": true}}, &docs,
		options.Find().SetProjection(bson.M{"_id": 1}).SetLimit(50)); err != nil {
		return 0
	}
	sent := 0
	for _, d := range docs {
		c, err := m.live(ctx, d.ID, true)
		if err != nil {
			continue
		}
		sent += m.sendInbox(ctx, c)
	}
	return sent
}

// sendInbox sends one conversation's waiting notes, in order.
func (m *Manager) sendInbox(ctx context.Context, c *conv) int {
	notes := m.takeInbox(ctx, c.id)
	for _, n := range notes {
		_, _ = m.sendNote(ctx, c, n) // one that can't go (the limit, a lost session) has nowhere to wait
	}
	return len(notes)
}

// inboxLoop is the daemon's look for notes from elsewhere.
func (m *Manager) inboxLoop() {
	t := time.NewTicker(inboxEvery)
	defer t.Stop()
	for range t.C {
		m.mu.Lock()
		closed := m.closed
		m.mu.Unlock()
		if closed {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		m.deliverInbox(ctx)
		cancel()
	}
}
