package ops

import (
	"context"
	"strings"
	"sync"

	"github.com/metavert-io/metarush/internal/app"
)

// A person turned down a version of the cut or a motion graphic with a note
// (D186): "What should change?". The note goes to the Claude Code conversation
// that made it, as that person's message, from whatever turned it down (the
// dashboard, the command line). internal/assistant registers the function that
// finds the conversation and sends it (it builds on this package).

// TurnedDown is a version or graphic turned down with a note.
type TurnedDown struct {
	ProjectID string
	// RevisionID, RevisionNumber and Origin name a version of the cut (its
	// origin says which conversation saved it, when Claude's card did).
	RevisionID     string
	RevisionNumber int
	Origin         string
	// MotionHash and MotionName name a motion graphic's version.
	MotionHash string
	MotionName string
	Note       string
}

// NoteSent says where a note for Claude went (nil: no conversation made it).
type NoteSent struct {
	ConversationID string `json:"conversationId"`
	Title          string `json:"title,omitempty"`
	// Queued: it waits behind Claude's current reply, or for the computer
	// that holds the conversation (Machine) to send it.
	Queued  bool   `json:"queued,omitempty"`
	Machine string `json:"machine,omitempty"`
}

var (
	turnedDownMu   sync.Mutex
	turnedDownHook func(ctx context.Context, a *app.App, t TurnedDown) *NoteSent
)

// OnTurnedDown registers the function told when a person turns something down
// with a note.
func OnTurnedDown(f func(ctx context.Context, a *app.App, t TurnedDown) *NoteSent) {
	turnedDownMu.Lock()
	defer turnedDownMu.Unlock()
	turnedDownHook = f
}

type quietTurnedDown struct{}

// QuietTurnedDown marks a decision whose note needn't be passed on: a card
// Claude wrote ("Reject version 7", with its own note) that a person ran —
// Claude hears that card's outcome anyway.
func QuietTurnedDown(ctx context.Context) context.Context {
	return context.WithValue(ctx, quietTurnedDown{}, true)
}

// tellTurnedDown passes a turned-down note on; best effort (a decision never
// fails because the note couldn't go).
func tellTurnedDown(ctx context.Context, a *app.App, t TurnedDown) *NoteSent {
	t.Note = strings.TrimSpace(t.Note)
	turnedDownMu.Lock()
	f := turnedDownHook
	turnedDownMu.Unlock()
	if f == nil || t.Note == "" || a == nil || ctx.Value(quietTurnedDown{}) != nil {
		return nil
	}
	return f(context.WithoutCancel(ctx), a, t)
}
