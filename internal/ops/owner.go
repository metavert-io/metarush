package ops

import (
	"context"
	"strings"
	"sync"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
)

/*
 * Owner-only settings (D183, D191). Everyone can see this computer's settings;
 * only an owner changes them: people and sign-in, remote access, the storage
 * and the Trash, keys and the workspace's budgets and limits, Claude Code, the
 * watched folder and link imports, the browser extension, the log and the
 * weekly improvement report, machines.
 *
 * Who counts as an owner:
 *   - a person signed in to the dashboard whose account is an owner, on this
 *     computer's own address or from another computer alike (a member signed in
 *     with a password at the host is refused too);
 *   - this computer's own command line, MCP server and daemon (no HTTP request
 *     behind the call): a person at the host's terminal can read the
 *     installation's files anyway.
 *
 * Every request that came through the dashboard's sign-in carries the person
 * (OverHTTP), so a handler that calls an op directly, or an op that calls
 * another (a card a member approves), is held to the same rule.
 */

type httpPersonKey struct{}

// httpPerson is the signed-in person behind an HTTP request; whether they are
// an owner is looked up once, when something owner-only asks.
type httpPerson struct {
	once  sync.Once
	check func() bool
	owner bool
}

func (p *httpPerson) isOwner() bool {
	p.once.Do(func() {
		if p.check != nil {
			p.owner = p.check()
		}
	})
	return p.owner
}

// OverHTTP marks ctx as a request from a signed-in browser: owner-only ops (and
// owner-only parts of ops) ask isOwner whether that person is an owner.
func OverHTTP(ctx context.Context, isOwner func() bool) context.Context {
	return context.WithValue(ctx, httpPersonKey{}, &httpPerson{check: isOwner})
}

func httpPersonOf(ctx context.Context) *httpPerson {
	p, _ := ctx.Value(httpPersonKey{}).(*httpPerson)
	return p
}

// ownerOK reports whether the caller may change owner-only settings.
func (c Caller) ownerOK(ctx context.Context) bool {
	if c.Owner {
		return true
	}
	if p := httpPersonOf(ctx); p != nil {
		return p.isOwner()
	}
	// Over HTTP without a signed-in person (it never happens through the
	// dashboard's sign-in): refused. Otherwise this computer's own CLI, MCP
	// server or daemon.
	return !c.Remote
}

// OwnerNames lists the names of this MetaRush's owners, for "ask an owner"
// (set by the accounts package).
var OwnerNames func(ctx context.Context, a *app.App) []string

// RequireOwner refuses a person who isn't an owner (D191), for an op whose
// owner-only part depends on its input (the workspace's setting; a project's
// own stays open) and for HTTP handlers outside the ops registry.
func RequireOwner(ctx context.Context, a *app.App, op string) error {
	if callerOf(ctx).ownerOK(ctx) {
		return nil
	}
	return ownerOnly(ctx, a, op)
}

// IsOwnerCaller reports whether the request or command behind ctx may change
// owner-only settings.
func IsOwnerCaller(ctx context.Context) bool { return callerOf(ctx).ownerOK(ctx) }

func ownerOnly(ctx context.Context, a *app.App, op string) error {
	var names []string
	if OwnerNames != nil {
		names = OwnerNames(ctx, a)
	}
	if names == nil {
		names = []string{}
	}
	hint := "Ask an owner to change it, or to make you an owner in Settings → People."
	if len(names) > 0 {
		hint = "Ask " + joinOr(names) + " to change it, or to make you an owner in Settings → People."
	}
	return apperr.New(apperr.Unauthorized, "Only an owner of this MetaRush can change that.").WithHint(hint).
		WithDetails(map[string]any{"reason": "owner_only", "op": op, "owners": names})
}

func joinOr(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " or " + xs[len(xs)-1]
}
