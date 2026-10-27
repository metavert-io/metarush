package logbook

import (
	"context"
	"net/http"
	"time"
)

// ProviderCall is one request to an online service, as the service's client
// reports it. It never carries the key, the text sent or the answer.
type ProviderCall struct {
	Provider   string // elevenlabs
	Title      string // ElevenLabs
	What       string // what the request does, in plain words, as a verb phrase: "make speech", "list voices"
	Method     string
	Endpoint   string // the address path without its query
	Status     int    // HTTP status (0: no answer)
	Duration   time.Duration
	Characters int    // characters of text sent for speech: what the provider bills
	Bytes      int    // size of the answer
	RequestID  string // the provider's ID for the request
}

// ProviderDone records a finished request (err is how it failed) in the log
// of the operation ctx belongs to.
func ProviderDone(ctx context.Context, c ProviderCall, err error) {
	if l := loggerFor(ctx); l != nil {
		l.ProviderCall(ctx, c, err)
	}
}

// ProviderCall records one request to an online service. Successful reads made
// for the dashboard (voice lists, plan usage) are not listed one by one: the
// dashboard reads that caused them are already counted.
func (l *Logger) ProviderCall(ctx context.Context, c ProviderCall, err error) {
	if l == nil {
		return
	}
	sc := ScopeOf(ctx)
	ch := sc.Channel
	if ch == "" {
		ch = l.channelFor(l.actor.Via)
	}
	ok := err == nil
	if ok && c.Method == http.MethodGet && ch == ChannelDashboard {
		return
	}
	e := Entry{Kind: KindProvider, Channel: ch, UserID: l.actor.UserID, UserName: l.actor.UserName, ParentID: sc.EntryID, Refs: refsOf(sc),
		DurationMs: ms(c.Duration), Summary: c.Title + ": " + c.What,
		Provider: &ProviderInfo{Name: c.Provider, Title: c.Title, What: c.What, Method: c.Method, Endpoint: c.Endpoint, Status: c.Status, OK: ok,
			Characters: c.Characters, Bytes: c.Bytes, RequestID: c.RequestID}}
	p := &pending{e: e}
	if !ok {
		p.e.Level = LevelError
		p.e.Error = errorInfo(err)
		p.e.Summary = c.Title + ": " + c.What + " failed: " + p.e.Error.Message
		p.versions = "-"
	}
	l.add(p)
}
