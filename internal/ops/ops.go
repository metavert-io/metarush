// Package ops is the single registry of operations. The CLI, the dashboard HTTP
// API and (later) an MCP adapter all dispatch through Call, so every surface uses
// the same validation, authorization flags and schemas.
package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/naming"
	"github.com/metavert-io/metarush/internal/store"
)

// Op is one registered operation.
type Op struct {
	Name      string
	Summary   string
	Mutating  bool
	HumanOnly bool // only a person (dashboard, or interactive terminal) may run it
	LocalOnly bool // needs this machine's filesystem; not exposed over HTTP
	// MaxInput is the largest request it takes over HTTP (DefaultMaxInput when 0).
	MaxInput int64
	// OwnerOnly: only an owner may run it (D183, D191): a signed-in person
	// whose account is an owner, on this computer's address or another's;
	// this computer's own CLI counts as the owner (owner.go).
	OwnerOnly bool
	InType    reflect.Type
	OutType   reflect.Type
	run       func(ctx context.Context, a *app.App, in any) (any, error)
}

var registry = map[string]*Op{}

// Def registers a typed operation.
func Def[I any, O any](name, summary string, flags Flags, fn func(ctx context.Context, a *app.App, in *I) (*O, error)) {
	if _, dup := registry[name]; dup {
		panic("duplicate op " + name)
	}
	registry[name] = &Op{
		Name: name, Summary: summary, Mutating: flags.Mutating, HumanOnly: flags.HumanOnly, LocalOnly: flags.LocalOnly, MaxInput: flags.MaxInput, OwnerOnly: flags.OwnerOnly,
		InType: reflect.TypeOf((*I)(nil)).Elem(), OutType: reflect.TypeOf((*O)(nil)).Elem(),
		run: func(ctx context.Context, a *app.App, in any) (any, error) { return fn(ctx, a, in.(*I)) },
	}
}

// Flags describe an operation's safety properties.
type Flags struct {
	Mutating  bool
	HumanOnly bool
	LocalOnly bool
	// MaxInput raises the largest request the op takes over HTTP (a file
	// carried in the request); 0 means DefaultMaxInput.
	MaxInput int64
	// OwnerOnly: people, sign-in, remote access, storage and keys (D183), and
	// every setting of this computer or of the workspace's limits (D191).
	OwnerOnly bool
}

// DefaultMaxInput is the largest request an op takes over HTTP.
const DefaultMaxInput = 8 << 20

// InputLimit is the largest request an op takes over HTTP.
func (o *Op) InputLimit() int64 {
	if o.MaxInput > 0 {
		return o.MaxInput
	}
	return DefaultMaxInput
}

// Caller describes who is invoking an op.
type Caller struct {
	Human bool // a person confirmed this action (dashboard click or TTY prompt)
	// Remote: it arrived over HTTP — from any browser, this computer's own
	// included (Elsewhere says it came from another computer).
	Remote bool
	// Elsewhere: it arrived through the public address (remote access, D128),
	// from a browser on another computer. Ops that act on this computer's
	// screen or widen who may sign in refuse it or say where they act.
	Elsewhere bool
	// Owner: the caller is known to be an owner (D183). Over HTTP the
	// signed-in person decides (OverHTTP, owner.go), so it stays false there.
	Owner bool
}

type callerKey struct{}

// callerOf returns who invoked the current op (zero Caller when unknown).
func callerOf(ctx context.Context) Caller {
	c, _ := ctx.Value(callerKey{}).(Caller)
	return c
}

// CallerFrom is who invoked the current op, for an op that calls another
// (the Add box hands video links to the link import as the same caller).
func CallerFrom(ctx context.Context) Caller { return callerOf(ctx) }

// Get returns an op by name.
func Get(name string) (*Op, bool) { op, ok := registry[name]; return op, ok }

// All lists ops sorted by name.
func All() []*Op {
	out := make([]*Op, 0, len(registry))
	for _, op := range registry {
		out = append(out, op)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Call decodes raw JSON input (strictly), enforces flags and runs the op. Every
// call is recorded in the activity log (refusals and errors too); successful
// dashboard reads are only counted.
func Call(ctx context.Context, a *app.App, name string, raw json.RawMessage, c Caller) (out any, err error) {
	op, ok := registry[name]
	rec := a.Log.StartOp(ctx, name, raw, a.Actor, c.Human, c.Remote)
	ctx = rec.Context(ctx)
	defer func() { rec.End(ok && op.Mutating, out, err) }()
	return dispatch(ctx, a, name, op, ok, raw, c)
}

func dispatch(ctx context.Context, a *app.App, name string, op *Op, ok bool, raw json.RawMessage, c Caller) (any, error) {
	if !ok {
		return nil, apperr.New(apperr.NotFound, "This version of MetaRush can't do that.").
			WithHint("MetaRush on this machine may be older than what asked for it; update it, then try again.").
			WithDetails(map[string]any{"op": name}).WithCommand("metarush ops list")
	}
	if op.OwnerOnly && !c.ownerOK(ctx) {
		// Before anything else about the call: a member hears that owners
		// change it, wherever they are (D191).
		return nil, ownerOnly(ctx, a, name)
	}
	if op.LocalOnly && c.Remote {
		return nil, apperr.New(apperr.Unauthorized, "That works only in MetaRush on the computer that has the files, not over the network.").
			WithDetails(map[string]any{"reason": "local_only", "op": name})
	}
	if a.Actor.Via == models.ViaAssistant && (op.Mutating || op.HumanOnly) {
		// The dashboard assistant (headless Claude Code) proposes; people apply.
		return nil, apperr.New(apperr.Unauthorized, "Claude in the dashboard can suggest this change, but only you can make it.").
			WithHint("Write the plan to the proposals folder and check it first; the person applies it in the dashboard.").
			WithDetails(map[string]any{"reason": "assistant_read_only", "op": name}).WithCommand("metarush edit check")
	}
	if a.ProposeOnly && (op.Mutating || op.HumanOnly) && op.Name != OpDraftSave {
		// An AI app that may only propose (D248): its changes wait for a
		// person as drafts, and nothing people-only is ever its to do.
		return nil, proposeRefusal(op)
	}
	if op.HumanOnly && !c.Human {
		return nil, apperr.New(apperr.HumanOnly, "Only a person can do this.").
			WithHint("Approve it in the dashboard, or do it yourself in a terminal.").
			WithDetails(map[string]any{"op": name})
	}
	if op.Mutating && a.Actor.UserID != "" && (c.Human || a.Actor.Via == "cli" || a.Actor.Via == "dashboard" || a.Actor.Via == "mcp") {
		ctx = store.WithChanger(ctx, a.Actor) // "last changed by" on projects and footage (D183)
	}
	ctx = context.WithValue(ctx, callerKey{}, c) // ops that do human-only work for some inputs (todos.answer) check it
	if naming.From(ctx) == "" {
		ctx = naming.With(ctx, naming.Derive(a.Actor, c.Human)) // who names a face or a voice (D274)
	}
	in := reflect.New(op.InType).Interface()
	if len(bytes.TrimSpace(raw)) > 0 && string(bytes.TrimSpace(raw)) != "null" {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(in); err != nil {
			return nil, apperr.Wrap(apperr.InvalidInput, err, "Something sent to MetaRush isn't in a form it accepts.").
				WithDetails(map[string]any{"op": name, "problem": err.Error()})
		}
	}
	// A move of the database keeps changes out of it (D335): while it is
	// being copied, and for good once it moved.
	if err := refuseWhileMoving(ctx, a, op); err != nil {
		return nil, err
	}
	if AIReads != nil && !op.Mutating { // What may leave: what an AI reads (D290)
		if err := AIReads(ctx, a, name, in); err != nil {
			return nil, err
		}
	}
	out, err := Run(ctx, a, op, in)
	if err == nil && AIOutput != nil {
		out = AIOutput(ctx, a, name, out)
	}
	return out, err
}

// Run executes an op with an already-typed input.
func Run(ctx context.Context, a *app.App, op *Op, in any) (any, error) {
	ctx, release := a.Cache.Hold(ctx) // cached media an op reads is not pruned while it runs
	defer release()
	out, err := op.run(ctx, a, in)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, apperr.Wrap(apperr.NotFound, err, "That was not found. It may have been deleted.")
		}
		return nil, err
	}
	return out, nil
}

// CallTyped runs an op from Go with a typed input (used by CLI commands).
func CallTyped[O any](ctx context.Context, a *app.App, name string, in any, c Caller) (*O, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	out, err := Call(ctx, a, name, b, c)
	if err != nil {
		return nil, err
	}
	o, ok := out.(*O)
	if !ok {
		return nil, fmt.Errorf("op %s returned %T", name, out)
	}
	return o, nil
}

// notFoundWords names what wasn't found the way people say it.
var notFoundWords = map[string]string{"asset": "clip", "revision": "version", "render": "render", "job": "job", "project": "project",
	"collection": "collection", "workspace": "workspace", "to-do": "to-do"}

// notFound says something asked for by its ID isn't there, in plain words;
// what it was and its ID stay in Details. ("not found" stays in the words:
// job advice looks for it.)
func notFound(what, id string) error {
	noun := notFoundWords[what]
	if noun == "" {
		noun = what
	}
	return apperr.New(apperr.NotFound, "That %s was not found. It may have been deleted.", noun).WithDetails(map[string]any{"kind": what, "id": id})
}

func invalid(f string, a ...any) error { return apperr.New(apperr.InvalidInput, f, a...) }
