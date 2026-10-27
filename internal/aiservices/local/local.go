// Package local holds the adapters of models that run on this computer
// (D278): the local recognizers as a transcription provider, offline voices,
// a picture's background taken away and pictures made larger. They pass the
// same conformance suite as every other adapter, are priced at nothing,
// need no key and send nothing anywhere: what they make stays in MetaRush.
//
// Each call gets the computer's programs and settings through the context
// (aiservices.WithEnv); a model that isn't set up here says so in plain words
// with the step that sets it up (aiservices.Here). Model files are downloaded
// only by a person or by setup, pinned by SHA-256, never under go test.
package local

import (
	"context"
	"io"
	"os"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/localmodels"
	"github.com/metavert-io/metarush/internal/videogen"
)

// Name is the provider's ID in the catalogue.
const Name = aiservices.LocalProvider

// base is what every local adapter shares: its names, the catalogue's word
// on its models and prices, and a one-shot call's polling (the work is done
// by the time Submit returns).
type base struct{ kind string }

func (b base) Provider() string { return Name }
func (b base) Kind() string     { return b.kind }

func (b base) Capabilities(model string) (*aiservices.Model, error) {
	return aiservices.ModelFor(Name, b.kind, model)
}

func (b base) Estimate(req *aiservices.Request, at time.Time) (*aiservices.Estimate, error) {
	m, err := aiservices.ModelFor(Name, b.kind, req.Model)
	if err != nil {
		return nil, err
	}
	return aiservices.EstimateFor(Name, m, req, at)
}

// Poll returns the result Submit answered with: a local call ends before it
// returns, so after a restart there is nothing to ask about (MetaRush keeps
// the outputs before it saves the ticket).
func (base) Poll(_ context.Context, _ string, t *aiservices.Ticket) (*aiservices.Status, error) {
	if t != nil && t.Ready != nil {
		return t.Ready, nil
	}
	return nil, &videogen.Error{Kind: videogen.KindExpired, Provider: Name, Message: "It was made on this computer and nothing is kept to ask about later."}
}

// Fetch writes an output's bytes.
func (base) Fetch(_ context.Context, _ string, _ *aiservices.Ticket, out *aiservices.Output, dst io.Writer) (int64, error) {
	if len(out.Data) == 0 {
		return 0, &videogen.Error{Kind: videogen.KindExpired, Provider: Name, Message: "Nothing was made to keep."}
	}
	n, err := dst.Write(out.Data)
	return int64(n), err
}

// Cancel has nothing to stop: the call has ended.
func (base) Cancel(context.Context, string, *aiservices.Ticket) error { return nil }

// Forget has nothing to delete: nothing was sent anywhere.
func (base) Forget(context.Context, string, *aiservices.Ticket, []aiservices.Output) error {
	return nil
}

// Stores is where a computer keeps the pinned files of its local models
// (tests stand one in).
var Stores = func(e aiservices.Env) localmodels.Store { return localmodels.Store{Home: e.Home} }

// env is the computer the call runs on, and a folder of its own for it.
func env(ctx context.Context, what string) (aiservices.Env, func(), error) {
	e, ok := aiservices.EnvFrom(ctx)
	if !ok {
		return e, nil, apperr.New(apperr.Internal, "MetaRush didn't say which computer should make %s.", what)
	}
	done := func() {}
	if e.Scratch == "" {
		dir, err := os.MkdirTemp("", "metarush-local-*")
		if err != nil {
			return e, nil, err
		}
		e.Scratch, done = dir, func() { _ = os.RemoveAll(dir) }
	}
	return e, done, nil
}

// notHere is the error of a model that can't run on this computer: never
// retried, said plainly with the step that sets it up.
func notHere(err error) error {
	ae := apperr.From(err)
	return &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: ae.Message, Hint: ae.Hint}
}

// ready is a ticket carrying a finished result.
func ready(st *aiservices.Status) *aiservices.Ticket {
	return &aiservices.Ticket{SubmittedAt: time.Now().UTC(), Ready: st}
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// Downloads are the pinned downloads a model needs before it runs here
// (nil: nothing to download — set up by bootstrap, or built from MetaRush's
// own source).
func Downloads(kind, model string) []string {
	switch kind {
	case aiservices.KindVoice:
		return []string{ProgramPin, firstNonEmpty(model, ModelKokoro)}
	case aiservices.KindUpscale:
		return []string{UpscalePin}
	}
	return nil
}
