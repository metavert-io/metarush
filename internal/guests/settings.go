package guests

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/sso"
	"github.com/metavert-io/metarush/internal/store"
)

// Turning guests on and off (D501). The workspace's switch (owners) says
// whether guests are let in at all; off signs every guest out at once (each
// request reads it). Each computer's guest listener is its own (config.yaml
// guests.listen and guests.port): turning guests on at a computer turns its
// listener on, on 127.0.0.1 only.

// GuestsStatusIn has no options.
type GuestsStatusIn struct{}

// GuestsStatusOut says how guests stand here.
type GuestsStatusOut struct {
	// On: the workspace lets guests in (the owners' switch).
	On bool `json:"on"`
	// Listening: this computer answers guests (its guest listener is turned
	// on); Running: the listener answers now (MetaRush runs in the background).
	Listening bool `json:"listening"`
	Running   bool `json:"running"`
	// Address is where guests' browsers reach this computer's listener, and
	// Port its port on 127.0.0.1.
	Address string `json:"address"`
	Port    int    `json:"port"`
	// Internet: the guest address can be reached from the internet. Never
	// in this release: nothing is.
	Internet bool `json:"internet"`
	// SignIn says how guests sign in, in plain words.
	SignIn []string `json:"signIn"`
	// SSO is the organization's sign-in guests can use (its name), when
	// people sign in that way; SSOCallback the address to add to the
	// provider's app for guests.
	SSO         string `json:"sso,omitempty"`
	SSOCallback string `json:"ssoCallback,omitempty"`
	// Guests is how many have something shared with them now, Grants how
	// many grants work now, SignedIn how many browsers are signed in.
	Guests   int `json:"guests"`
	Grants   int `json:"grants"`
	SignedIn int `json:"signedIn"`
	// UpdatedAt and UpdatedBy say who changed the switch last.
	UpdatedAt *time.Time    `json:"updatedAt,omitempty"`
	UpdatedBy *models.Actor `json:"updatedBy,omitempty"`
	// Lines say it in plain words, one thing a line.
	Lines   []string `json:"lines"`
	Summary string   `json:"summary"`
}

// GuestsSettingsSetIn turns guests on or off, or moves this computer's
// listener (owners, people only).
type GuestsSettingsSetIn struct {
	// On lets guests in (and this computer answers them); off signs every
	// guest out at once.
	On *bool `json:"on,omitempty"`
	// Port is where this computer's guest listener answers on 127.0.0.1.
	Port *int `json:"port,omitempty" jsonschema:"minimum=1024,maximum=65535"`
}

func init() {
	ops.Def("guests.status", "Whether guests (people outside the workspace you share versions with) are let in, where this computer answers them, how they sign in, and how many have something shared now", ops.Flags{},
		func(ctx context.Context, a *app.App, _ *GuestsStatusIn) (*GuestsStatusOut, error) {
			return Status(ctx, a), nil
		})
	ops.Def("guests.settings.set", "Turn guests on or off (off signs every guest out at once), or move where this computer's guest listener answers (owners, people only)", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true},
		setSettings)
}

// Settings is the workspace's switch, read now (no cache: off counts at once).
func Settings(ctx context.Context, a *app.App) models.GuestSettings {
	var ws models.Workspace
	if err := a.DB.C(store.CWorkspaces).FindOne(ctx, bson.M{"_id": a.WorkspaceID()}, options.FindOne().SetProjection(bson.M{"guests": 1})).Decode(&ws); err != nil || ws.Guests == nil {
		return models.GuestSettings{}
	}
	return *ws.Guests
}

// On reports whether the workspace lets guests in.
func On(ctx context.Context, a *app.App) bool { return Settings(ctx, a).On }

// OnListenerChanged is told when an op changed this computer's guest
// listener (the background MetaRush starts, moves or stops it at once; set
// by internal/api).
var OnListenerChanged func(config.Guests)

// Status says how guests stand here.
func Status(ctx context.Context, a *app.App) *GuestsStatusOut {
	st := Settings(ctx, a)
	l := a.Cfg.GuestListener()
	out := &GuestsStatusOut{On: st.On, Listening: l.Listen, Address: l.Address(), Port: l.PortOrDefault(), SignIn: []string{"with a one-time sign-in link an owner sends them"}, Lines: []string{}}
	_, out.Running = ListenerRunning(a.Cfg)
	if !st.UpdatedAt.IsZero() {
		at, by := st.UpdatedAt, st.UpdatedBy
		out.UpdatedAt, out.UpdatedBy = &at, &by
	}
	if s := auth.Settings(ctx, a); s.Method == models.AuthSSO && s.SSO != nil {
		out.SSO = sso.Title(s.SSO.Provider, s.SSO.Label)
		out.SSOCallback = l.Address() + sso.GuestCallbackPath
		out.SignIn = append(out.SignIn, "with their own "+out.SSO+" account, when its email is the one you shared with")
	}
	out.Grants, out.Guests = countLive(ctx, a)
	if n, err := a.DB.C(store.CGuestSessions).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "$expr": bson.M{"$gt": bson.A{"$expiresAt", "$$NOW"}}}); err == nil {
		out.SignedIn = int(n)
	}
	line := func(s string) { out.Lines = append(out.Lines, s) }
	switch {
	case !st.On:
		out.Summary = "Guests are off: nobody outside the workspace can open what you share with them."
	case !l.Listen:
		out.Summary = "Guests are on in this workspace, but this computer doesn't answer them."
	case !out.Running:
		out.Summary = fmt.Sprintf("Guests are on. This computer answers them at %s once MetaRush runs in the background.", out.Address)
	default:
		out.Summary = fmt.Sprintf("Guests are on. This computer answers them at %s.", out.Address)
	}
	line(out.Summary)
	line("Nothing is reachable from the internet yet: the guest address is this computer's own, so guests open it only on this computer for now.")
	line("Guests sign in " + joinAnd(out.SignIn) + ".")
	if out.SSOCallback != "" {
		line("For guests to sign in with " + out.SSO + ", add " + out.SSOCallback + " to the app's addresses at the provider.")
	}
	line(fmt.Sprintf("%s with something shared now, %s signed in.", plural(out.Guests, "guest"), plural(out.SignedIn, "browser")))
	return out
}

// countLive counts grants that work now and the guests they are for.
func countLive(ctx context.Context, a *app.App) (grants, guests int) {
	var gs []models.GuestGrant
	if err := a.DB.Find(ctx, store.CGuestGrants, a.WorkspaceID(), liveGrant(), &gs, options.Find().SetProjection(bson.M{"guestId": 1}).SetLimit(10000)); err != nil {
		return 0, 0
	}
	seen := map[string]bool{}
	for _, g := range gs {
		seen[g.GuestID] = true
	}
	return len(gs), len(seen)
}

// liveGrant matches grants that work now: not taken back, not ended by the
// database server's clock.
func liveGrant() bson.M {
	return bson.M{"revokedAt": nil, "$expr": bson.M{"$gt": bson.A{"$endsAt", "$$NOW"}}}
}

func setSettings(ctx context.Context, a *app.App, in *GuestsSettingsSetIn) (*GuestsStatusOut, error) {
	if in.On == nil && in.Port == nil {
		return nil, apperr.New(apperr.InvalidInput, "Say whether guests are on or off, or the port this computer answers them at.")
	}
	l := a.Cfg.GuestListener()
	if in.Port != nil {
		if err := config.CheckGuestPort(*in.Port, a.Cfg.Dashboard.Port); err != nil {
			return nil, apperr.New(apperr.InvalidInput, "The guest listener needs %s.", err.Error()).WithDetails(map[string]any{"reason": "port"})
		}
		if ld := a.Cfg.Database.Local; ld != nil && ld.Port == *in.Port {
			return nil, apperr.New(apperr.InvalidInput, "The database on this computer uses that port: choose another for guests.").WithDetails(map[string]any{"reason": "port"})
		}
		l.Port = *in.Port
	}
	if in.On != nil && *in.On && ops.CallerFrom(ctx).Elsewhere {
		// A new way in is opened at the computer that runs MetaRush, like
		// changing how people sign in.
		return nil, apperr.New(apperr.Unauthorized, "Guests are turned on at the computer that runs MetaRush.").
			WithHint("Turn them on there, in Settings → People → Guests or a terminal.").WithDetails(map[string]any{"reason": "here_only"}).WithCommand("metarush guests on")
	}
	if in.On != nil {
		now := time.Now().UTC()
		set := models.GuestSettings{On: *in.On, UpdatedAt: now, UpdatedBy: a.Actor}
		update := bson.M{"$set": bson.M{"guests": set}, "$setOnInsert": bson.M{"workspaceId": a.WorkspaceID(), "name": a.Cfg.Workspace.Name, "createdAt": now}}
		if _, err := a.DB.C(store.CWorkspaces).UpdateOne(ctx, bson.M{"_id": a.WorkspaceID()}, update, options.UpdateOne().SetUpsert(true)); err != nil {
			return nil, err
		}
		l.Listen = *in.On
	}
	if a.Cfg.Home() != "" {
		if err := config.SetField(a.Cfg.Home(), []string{"guests"}, l); err != nil {
			return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't save this computer's guest settings.")
		}
	}
	a.Cfg.SetGuestListener(l)
	ended := 0
	if in.On != nil && !*in.On {
		// Off means off: every guest is signed out now and unused links end.
		ended = endAllSessions(ctx, a)
		revokeAllLinks(ctx, a)
	}
	details := map[string]any{"listen": l.Listen, "port": l.PortOrDefault()}
	if in.On != nil {
		details["on"] = *in.On
	}
	if ended > 0 {
		details["signedOut"] = ended
	}
	a.Audit(ctx, "guests.settings", "workspace", a.WorkspaceID(), "", details)
	if OnListenerChanged != nil {
		OnListenerChanged(l)
	}
	out := Status(ctx, a)
	if ended > 0 {
		out.Summary += " " + plural(ended, "guest browser") + " signed out."
	}
	return out, nil
}

/* ---------- Whether the listener runs ---------- */

// listenerState is the file the background MetaRush keeps while its guest
// listener answers: its process and port.
type listenerState struct {
	PID       int       `json:"pid"`
	Port      int       `json:"port"`
	StartedAt time.Time `json:"startedAt"`
}

func listenerFile(cfg *config.Config) string {
	return filepath.Join(cfg.StateDir(), "guest-listener.json")
}

// SayListening records that this process's guest listener answers at port.
func SayListening(cfg *config.Config, port int) {
	b, _ := json.Marshal(listenerState{PID: os.Getpid(), Port: port, StartedAt: time.Now().UTC()})
	_ = os.MkdirAll(cfg.StateDir(), 0o755)
	_ = os.WriteFile(listenerFile(cfg), b, 0o600)
}

// SayStopped records that it stopped.
func SayStopped(cfg *config.Config) { _ = os.Remove(listenerFile(cfg)) }

// ListenerRunning reports whether a MetaRush process on this computer
// answers guests now, and at which port.
func ListenerRunning(cfg *config.Config) (int, bool) {
	b, err := os.ReadFile(listenerFile(cfg))
	if err != nil {
		return 0, false
	}
	var st listenerState
	if json.Unmarshal(b, &st) != nil || st.PID <= 0 {
		return 0, false
	}
	if err := syscall.Kill(st.PID, 0); err != nil && err != syscall.EPERM {
		return 0, false
	}
	return st.Port, true
}
