package assistant

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/store"
)

// Handing a computer's role to another one (item 34; D344–D348): its panel
// conversations travel in a private file, are put where Claude Code looks on
// the new computer and are held there from then on. One history (D345):
// resuming appends to the same session file, so the old computer never
// continues a conversation once it is handing it over — every message claims
// the conversation first (mayContinue), and a handover marks only
// conversations nobody is talking to (MarkHandingOver); the holder's full
// saves match its installation, so once a conversation lives elsewhere the
// old computer lets it go.

// mayContinue claims the conversation's next message for this computer and
// this process, in one write: it must still be held here, not be on its way
// elsewhere, and no other live MetaRush on this computer may hold it. The
// write sets it working with this process as its holder (here and in the
// database), so a handover — which marks only conversations nobody is
// talking to — never crosses a message that has started, whatever is saved
// before Claude Code is up (D345).
func (m *Manager) mayContinue(c *conv) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	me, _ := m.machine()
	pid := os.Getpid()
	coll := m.App.DB.C(store.CAssistantConversations)
	for range 3 {
		var d struct {
			InstallationID   string           `bson:"installationId"`
			InstallationName string           `bson:"installationName"`
			HandingOver      *ConvHandingOver `bson:"handingOver"`
			HolderPID        int              `bson:"holderPid"`
		}
		err := coll.FindOne(ctx, bson.M{"_id": c.id, "workspaceId": m.App.WorkspaceID()},
			options.FindOne().SetProjection(bson.M{"installationId": 1, "installationName": 1, "handingOver": 1, "holderPid": 1})).Decode(&d)
		if err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				return notFound(c.id)
			}
			return apperr.Wrap(apperr.Database, err, "MetaRush couldn't reach its database to send this message. Try again in a moment.")
		}
		switch {
		case d.InstallationID != me:
			go m.forget(c.id) // it lives on another computer now
			return otherMachine(&Conversation{InstallationID: d.InstallationID, InstallationName: d.InstallationName})
		case d.HandingOver != nil:
			c.mu.Lock()
			h := *d.HandingOver
			c.data.HandingOver = &h
			c.touch()
			c.mu.Unlock()
			return handingOverErr(&Conversation{HandingOver: d.HandingOver})
		case d.HolderPID != 0 && d.HolderPID != pid && pidAlive(d.HolderPID):
			return apperr.New(apperr.Conflict, "This conversation is already open in another copy of MetaRush on this machine.").
				WithHint("Continue it there (in the dashboard, or in the terminal that has it open), or close that one first.").
				WithDetails(map[string]any{"reason": "held_elsewhere", "pid": d.HolderPID})
		}
		holder := bson.M{"holderPid": d.HolderPID}
		if d.HolderPID == 0 {
			holder = bson.M{"holderPid": bson.M{"$in": bson.A{0, nil}}}
		}
		filter := bson.M{"_id": c.id, "workspaceId": m.App.WorkspaceID(), "installationId": me, "handingOver": bson.M{"$exists": false}}
		for k, v := range holder {
			filter[k] = v
		}
		res, err := coll.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"state": ConvWorking, "holderPid": pid}})
		if err != nil {
			return apperr.Wrap(apperr.Database, err, "MetaRush couldn't reach its database to send this message. Try again in a moment.")
		}
		if res.MatchedCount == 1 {
			c.mu.Lock()
			c.data.State, c.data.HolderPID = ConvWorking, pid
			if c.data.HandingOver != nil { // a handover that was abandoned
				c.data.HandingOver = nil
			}
			c.touch()
			c.mu.Unlock()
			return nil
		}
		// It changed between the look and the write: look again.
	}
	return apperr.New(apperr.Conflict, "MetaRush couldn't start this message. Send it again.")
}

// syncHolder brings a live conversation's holder up to date from the
// database (a handover made in a terminal, or abandoned): false when it now
// lives on another computer (it is let go).
func (m *Manager) syncHolder(ctx context.Context, c *conv) bool {
	var d struct {
		InstallationID string           `bson:"installationId"`
		HandingOver    *ConvHandingOver `bson:"handingOver"`
	}
	qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := m.App.DB.C(store.CAssistantConversations).FindOne(qctx, bson.M{"_id": c.id, "workspaceId": m.App.WorkspaceID()},
		options.FindOne().SetProjection(bson.M{"installationId": 1, "handingOver": 1})).Decode(&d); err != nil {
		return true // no answer: as it was
	}
	if me, _ := m.machine(); d.InstallationID != "" && d.InstallationID != me {
		m.forget(c.id)
		return false
	}
	c.mu.Lock()
	if (c.data.HandingOver == nil) != (d.HandingOver == nil) {
		c.data.HandingOver = d.HandingOver
		c.touch()
	}
	c.mu.Unlock()
	return true
}

// handingOverErr refuses a message for a conversation on its way elsewhere.
func handingOverErr(d *Conversation) error {
	to := "another computer"
	if d.HandingOver != nil && d.HandingOver.ToName != "" {
		to = "“" + d.HandingOver.ToName + "”"
	}
	return apperr.New(apperr.Conflict, "This conversation is moving to %s, so it isn't continued here any more.", to).
		WithHint("Continue it there once that computer has received it, or start a new conversation here.").
		WithDetails(map[string]any{"reason": "handing_over"}).WithCommand("metarush machine handover status")
}

// summaryIfSessionMissing: a conversation from another computer whose
// session isn't where Claude Code would look for it here goes on from a
// summary in a new session, rather than failing its next message (D345).
func (m *Manager) summaryIfSessionMissing(c *conv) {
	c.mu.Lock()
	mv, started, sid := c.data.Moved, c.data.SessionStarted, c.data.ClaudeSessionID
	c.mu.Unlock()
	if mv == nil || mv.Summary || !started || sid == "" {
		return
	}
	place, err := m.SessionPlaceOf(sid)
	if err != nil {
		return // can't tell: Claude Code says (exitReason)
	}
	if _, err := os.Stat(place.File); err == nil {
		return
	}
	c.mu.Lock()
	m.toSummaryLocked(c, "missing")
	c.mu.Unlock()
}

// toSummaryLocked starts the conversation's next message in a new session
// primed with a summary (caller holds c.mu).
func (m *Manager) toSummaryLocked(c *conv, why string) {
	if c.data.Moved == nil {
		c.data.Moved = &ConvMoved{At: time.Now().UTC()}
	}
	c.data.Moved.Summary, c.data.Moved.Why, c.data.Moved.Primed = true, why, false
	c.data.ClaudeSessionID = newSessionID()
	c.data.SessionStarted, c.data.SessionLost = false, false
	c.data.SessionTokens = TokenUsage{}
	c.touch()
}

// summaryLimit bounds the summary a new session starts from (characters).
const summaryLimit = 16000

// summaryOf is the conversation so far in MetaRush's own record, for a new
// session that goes on from it: the person's messages and Claude's replies
// in order (the first message always, then as many of the latest as fit),
// and the cards with what became of them.
func summaryOf(d Conversation) string {
	type said struct {
		who, text string
	}
	var lines []said
	byTurn := map[int]int{}
	for _, e := range d.Events {
		switch e.Kind {
		case "turn":
			if e.State == "note" {
				lines = append(lines, said{"MetaRush (an update, not typed by the person)", clipRunes(e.Text, 600)})
				continue
			}
			who := e.User
			if who == "" {
				who = orWords(d.UserName, "The person")
			}
			lines = append(lines, said{who, clipRunes(e.Text, 1200)})
		case "text":
			if i, ok := byTurn[e.Turn]; ok && i == len(lines)-1 {
				lines[i].text = clipRunes(lines[i].text+" "+e.Text, 1500)
				continue
			}
			lines = append(lines, said{"Claude", clipRunes(e.Text, 1500)})
			byTurn[e.Turn] = len(lines) - 1
		}
	}
	var b strings.Builder
	b.WriteString("This conversation continues on this computer from a summary MetaRush made of it: Claude Code's own record of it didn't come along, so earlier tool results and files you read are not in your memory. Read again what you need.\n")
	if d.ProjectName != "" {
		fmt.Fprintf(&b, "It is about the project %q.\n", d.ProjectName)
	}
	b.WriteString("The conversation so far, oldest first:\n")
	render := func(l said) string { return "- " + l.who + ": " + l.text + "\n" }
	used := b.Len()
	var tail []string
	first := ""
	if len(lines) > 0 {
		first = render(lines[0])
		used += len(first)
	}
	for i := len(lines) - 1; i >= 1; i-- {
		r := render(lines[i])
		if used+len(r) > summaryLimit {
			tail = append(tail, "- (earlier messages left out)\n")
			break
		}
		used += len(r)
		tail = append(tail, r)
	}
	b.WriteString(first)
	for i := len(tail) - 1; i >= 0; i-- {
		b.WriteString(tail[i])
	}
	var cards []string
	for _, a := range d.Actions {
		if a.Title == "" {
			continue
		}
		cards = append(cards, fmt.Sprintf("%s (%s)", clipRunes(a.Title, 160), a.State))
	}
	if len(cards) > 0 {
		if len(cards) > 20 {
			cards = cards[len(cards)-20:]
		}
		b.WriteString("Changes you suggested as cards, and what became of them: " + strings.Join(cards, "; ") + ".\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func clipRunes(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

func orWords(s, other string) string {
	if strings.TrimSpace(s) == "" {
		return other
	}
	return s
}

/* ---------- What a handover asks of conversations ---------- */

// HeldConversation is a conversation a computer holds, as a handover sees it.
type HeldConversation struct {
	ID          string           `json:"id"`
	Title       string           `json:"title"`
	ProjectName string           `json:"projectName,omitempty"`
	UserName    string           `json:"userName,omitempty"`
	SessionID   string           `json:"-"`
	Started     bool             `json:"-"`
	Lost        bool             `json:"lost,omitempty"`
	Legacy      bool             `json:"legacy,omitempty"`
	Archived    bool             `json:"archived,omitempty"`
	Busy        bool             `json:"busy,omitempty"`
	HandingOver *ConvHandingOver `json:"handingOver,omitempty"`
	Moved       *ConvMoved       `json:"-"`
}

// Held lists the conversations installation holds (archived ones too).
// Busy: Claude is working on it in a MetaRush on this computer.
func Held(ctx context.Context, a *app.App, installation string) ([]HeldConversation, error) {
	var docs []Conversation
	err := a.DB.Find(ctx, store.CAssistantConversations, a.WorkspaceID(), bson.M{"installationId": installation}, &docs,
		options.Find().SetProjection(bson.M{"title": 1, "projectName": 1, "userName": 1, "claudeSessionId": 1, "sessionStarted": 1, "sessionLost": 1,
			"legacy": 1, "archived": 1, "state": 1, "holderPid": 1, "handingOver": 1, "moved": 1, "updatedAt": 1, "installationId": 1, "actions.state": 1}).
			SetSort(bson.D{{Key: "updatedAt", Value: -1}}))
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read the panel's conversations.")
	}
	out := make([]HeldConversation, 0, len(docs))
	here := installation == a.Cfg.Installation.ID
	for _, d := range docs {
		out = append(out, HeldConversation{ID: d.ID, Title: d.Title, ProjectName: d.ProjectName, UserName: d.UserName, SessionID: d.ClaudeSessionID,
			Started: d.SessionStarted, Lost: d.SessionLost, Legacy: d.Legacy, Archived: d.Archived, HandingOver: d.HandingOver, Moved: d.Moved,
			Busy: here && busyNow(&d)})
	}
	return out, nil
}

// AssistantFolder is MetaRush's folder for Claude Code in an installation
// (its working folder and the conversations' folders are inside).
func AssistantFolder(a *app.App) string { return a.Cfg.AssistantDir() }

// ConversationFolder is a conversation's own folder (its proposals,
// attachments and the stream Claude Code wrote) in an installation.
func ConversationFolder(a *app.App, id string) string {
	return filepath.Join(a.Cfg.AssistantDir(), "conversations", id)
}

// SessionPlaceFor is where Claude Code on this computer keeps session id of
// the installation's working folder (by this process's environment).
func SessionPlaceFor(a *app.App, id string) (SessionPlace, error) {
	dir := filepath.Join(a.Cfg.AssistantDir(), "workspace")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return SessionPlace{}, err
	}
	return ClaudeSessionPlace(os.Getenv, realPath(dir), id)
}

// HandingOverMark is what MarkHandingOver puts on each conversation.
type HandingOverMark struct {
	Handover, To, ToName string
}

// MarkHandingOver marks this computer's conversations as on their way to
// another computer (one already marked by the same handover counts as
// marked). Busy lists those Claude is working on now: they are not marked.
func MarkHandingOver(ctx context.Context, a *app.App, ids []string, mk HandingOverMark) (marked, busy []string, err error) {
	me := a.Cfg.Installation.ID
	for _, id := range ids {
		d, lerr := loadConv(ctx, a, id)
		if lerr != nil {
			return marked, busy, lerr
		}
		switch {
		case d.InstallationID != me:
			continue
		case d.HandingOver != nil && d.HandingOver.Handover == mk.Handover:
			marked = append(marked, id)
			continue
		case d.HandingOver != nil:
			return marked, busy, apperr.New(apperr.Conflict, "“%s” is already moving to %s.", d.Title, d.HandingOver.ToName).
				WithDetails(map[string]any{"reason": "handing_over"})
		}
		if busyNow(d) {
			busy = append(busy, id)
			continue
		}
		// Only as it was looked at: no message being started (a message
		// claims the conversation with state and holder in one write) and no
		// card running.
		filter := bson.M{"_id": id, "workspaceId": a.WorkspaceID(), "installationId": me, "handingOver": bson.M{"$exists": false},
			"actions.state": bson.M{"$ne": ActionRunning}}
		if d.State != ConvWorking {
			filter["state"] = bson.M{"$ne": ConvWorking}
		} else {
			// Working, held by a MetaRush that stopped: it stopped mid-message.
			filter["state"], filter["holderPid"] = ConvWorking, d.HolderPID
		}
		res, uerr := a.DB.C(store.CAssistantConversations).UpdateOne(ctx, filter, bson.M{"$set": bson.M{
			"handingOver": ConvHandingOver{Handover: mk.Handover, To: mk.To, ToName: mk.ToName, At: time.Now().UTC()}}})
		if uerr != nil {
			return marked, busy, apperr.Wrap(apperr.Database, uerr, "MetaRush couldn't mark the conversations as moving.")
		}
		if res.MatchedCount == 0 {
			busy = append(busy, id) // a message started meanwhile
			continue
		}
		marked = append(marked, id)
	}
	return marked, busy, nil
}

// busyNow: something is going on in the conversation that a handover must
// not cross — a message being started or answered by a MetaRush that is
// running (working with a live holder, or working with no holder yet), or a
// card running.
func busyNow(d *Conversation) bool {
	if d.State == ConvWorking && (d.HolderPID == 0 || pidAlive(d.HolderPID)) {
		return true
	}
	for _, a := range d.Actions {
		if a.State == ActionRunning {
			return true
		}
	}
	return false
}

// Settle brings this computer's conversations up to date before a handover
// looks at them: a message a MetaRush that stopped left running ends as lost,
// as when the daemon starts again.
func Settle(ctx context.Context, a *app.App) {
	For(a).ensureStarted(ctx)
}

// UnmarkHandingOver takes a handover's mark off the conversations the
// computer from still holds (the handover was abandoned): they go on there.
func UnmarkHandingOver(ctx context.Context, a *app.App, handover, from string) (int, error) {
	res, err := a.DB.C(store.CAssistantConversations).UpdateMany(ctx,
		bson.M{"workspaceId": a.WorkspaceID(), "installationId": from, "handingOver.handover": handover},
		bson.M{"$unset": bson.M{"handingOver": ""}})
	if err != nil {
		return 0, apperr.Wrap(apperr.Database, err, "MetaRush couldn't put the conversations back.")
	}
	return int(res.ModifiedCount), nil
}

// Adopt is one conversation coming to this computer.
type Adopt struct {
	ID string
	// Handover is the handover moving it; From the computer holding it now.
	Handover, From, FromName string
	// Marked: the old computer marked it (a handover); a take-over from a
	// computer that is gone has no mark.
	Marked bool
	// OldDir is MetaRush's folder for Claude Code on the old computer.
	OldDir string
	// Summary: its session didn't come along (Why: missing | unreadable).
	Summary bool
	Why     string
}

// AdoptConversation makes this computer the conversation's holder (a check
// and set on the computer holding it and the handover's mark), records
// where it came from, and ends a message the old computer left running.
// Adopting one already adopted by the same handover changes nothing.
func AdoptConversation(ctx context.Context, a *app.App, in Adopt) (bool, error) {
	me, myName := a.Cfg.Installation.ID, a.Cfg.Installation.Name
	d, err := loadConv(ctx, a, in.ID)
	if err != nil {
		return false, err
	}
	if d.InstallationID == me {
		return d.Moved != nil && d.Moved.Handover == in.Handover, nil
	}
	filter := bson.M{"_id": in.ID, "workspaceId": a.WorkspaceID(), "installationId": in.From}
	if in.Marked {
		filter["handingOver.handover"] = in.Handover
	}
	mv := ConvMoved{Handover: in.Handover, From: in.From, FromName: in.FromName, At: time.Now().UTC(), OldDir: in.OldDir, NewDir: AssistantFolder(a)}
	set := bson.M{"installationId": me, "installationName": myName, "holderPid": 0, "moved": &mv}
	if in.Summary && !d.Legacy {
		mv.Summary, mv.Why = true, in.Why
		set["claudeSessionId"], set["sessionStarted"], set["sessionLost"], set["sessionTokens"] = newSessionID(), false, false, TokenUsage{}
	}
	res, err := a.DB.C(store.CAssistantConversations).UpdateOne(ctx, filter, bson.M{"$set": set, "$unset": bson.M{"handingOver": ""}})
	if err != nil {
		return false, apperr.Wrap(apperr.Database, err, "MetaRush couldn't move the conversation to this computer.")
	}
	if res.MatchedCount == 0 {
		return false, apperr.New(apperr.Conflict, "“%s” changed on the other computer while it was being moved.", d.Title).
			WithHint("Run the same command again.").WithDetails(map[string]any{"reason": "changed", "conversation": in.ID})
	}
	// A message the old computer left running ends as lost, as when MetaRush
	// stops mid-message (the next message picks up).
	m := For(a)
	m.mu.Lock()
	delete(m.convs, in.ID)
	m.mu.Unlock()
	if d.State == ConvWorking {
		if c, lerr := m.live(ctx, in.ID, true); lerr == nil {
			c.mu.Lock()
			var f followUp
			if c.runningTurn() > 0 {
				f = m.finishTurnLocked(c, nil, nil, TurnLost, "MetaRush stopped on the other computer while Claude Code was working on this. Send a message to pick up where it left off.")
			} else {
				c.data.State = ConvIdle
				c.touch()
			}
			c.mu.Unlock()
			m.afterTurn(c, f)
			m.save(c, true)
		}
	} else {
		_, _ = a.DB.C(store.CAssistantConversations).UpdateOne(ctx, bson.M{"_id": in.ID, "workspaceId": a.WorkspaceID(), "installationId": me},
			bson.M{"$set": bson.M{"state": ConvIdle}})
	}
	return true, nil
}

func loadConv(ctx context.Context, a *app.App, id string) (*Conversation, error) {
	if !ValidID(id) {
		return nil, notFound(id)
	}
	var d Conversation
	if err := a.DB.GetByID(ctx, store.CAssistantConversations, a.WorkspaceID(), id, &d); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, notFound(id)
		}
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't load a conversation from its database.")
	}
	return &d, nil
}

// SummaryOf is the summary a conversation would go on from (tests and the
// dry run's words).
func SummaryOf(d Conversation) string { return summaryOf(d) }
