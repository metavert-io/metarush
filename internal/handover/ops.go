package handover

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// HandoverStatusIn has no fields.
type HandoverStatusIn struct{}

// HandoverStatusOut says where this computer stands and what is in progress.
type HandoverStatusOut struct {
	Computer HandoverComputer `json:"computer"`
	Role     HandoverRole     `json:"role"`
	// InProgress are the handovers from or to this computer that aren't
	// finished (a file made but not received; received, the old computer
	// not stepped down yet).
	InProgress []HandoverRecord `json:"inProgress"`
	// Recent are the workspace's latest handovers and take-overs.
	Recent []HandoverRecord `json:"recent"`
	// Waiting is what this computer finishes before it steps down.
	Waiting []HandoverWork `json:"waiting"`
	// Clean is a handover from this computer that was received while its
	// copies of the conversations are still here (clean is offered).
	Clean   *HandoverRecord `json:"clean,omitempty"`
	Summary string          `json:"summary"`
}

func init() {
	ops.Def("machine.handover.plan", "Say what handing this computer's role to another computer would do: its panel conversations and whether each session is here, recipe runs and planning by itself, pinned work, watched and linked folders, remote access, extension pairings, which keys are here (names only), the background role, and the steps only a person takes (owners, people only; nothing changes)",
		ops.Flags{HumanOnly: true, OwnerOnly: true}, planHandover)
	ops.Def("machine.handover.status", "Where this computer stands — the host, stepping down or a client since it handed its role over — and the handovers in progress (people only)",
		ops.Flags{HumanOnly: true}, handoverStatus)
	ops.Def("machine.handover.export", "Make the handover file on the old computer: the panel conversations with their Claude Code sessions and folders, the pinned-work list and the folder settings, encrypted with a passphrase given at the terminal (owners, people only, in a terminal)",
		ops.Flags{Mutating: true, HumanOnly: true, LocalOnly: true, OwnerOnly: true}, exportHandover)
	ops.Def("machine.handover.receive", "Receive a handover file on the new computer: sessions put where Claude Code looks, conversations held here from now on, pinned work moved, folders set where the person says (owners, people only, in a terminal; plan first)",
		ops.Flags{Mutating: true, HumanOnly: true, LocalOnly: true, OwnerOnly: true}, receiveHandover)
	ops.Def("machine.handover.abandon", "Abandon a handover that hasn't been received: its conversations go on on the old computer (owners, people only)",
		ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, abandonHandover)
	ops.Def("machine.handover.clean", "On the old computer, after the new one received its role: remove this computer's copies of the handed-over conversations (owners, people only, in a terminal; says what it would remove unless yes)",
		ops.Flags{Mutating: true, HumanOnly: true, LocalOnly: true, OwnerOnly: true}, cleanHandover)
	ops.Def("machine.takeover", "Take over the role of a computer that is gone: only when it hasn't been seen for a while and a person confirms it is off; its conversations from a plain copy of its folders or a handover file, its pinned work and folders move here (owners, people only, in a terminal; plan first)",
		ops.Flags{Mutating: true, HumanOnly: true, LocalOnly: true, OwnerOnly: true}, takeOver)
}

func handoverStatus(ctx context.Context, a *app.App, _ *HandoverStatusIn) (*HandoverStatusOut, error) {
	if err := peopleOnly(a); err != nil {
		return nil, err
	}
	me := a.Cfg.Installation.ID
	out := &HandoverStatusOut{Computer: HandoverComputer{ID: me, Name: a.Cfg.Installation.Name}, InProgress: []HandoverRecord{}, Recent: []HandoverRecord{}, Waiting: []HandoverWork{}}
	role, err := RoleOf(ctx, a, me)
	if err != nil {
		return nil, err
	}
	out.Role = role
	if err := a.DB.Find(ctx, store.CHandovers, a.WorkspaceID(), bson.M{"active": true, "$or": bson.A{bson.M{"from": me}, bson.M{"to": me}}}, &out.InProgress,
		options.Find().SetSort(bson.D{{Key: "startedAt", Value: -1}})); err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read the handovers.")
	}
	if err := a.DB.Find(ctx, store.CHandovers, a.WorkspaceID(), bson.M{}, &out.Recent,
		options.Find().SetSort(bson.D{{Key: "startedAt", Value: -1}}).SetLimit(5)); err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read the handovers.")
	}
	if role.Role == RoleSteppingDown {
		if out.Waiting, err = stillThere(ctx, a, me); err != nil {
			return nil, err
		}
		if out.Waiting == nil {
			out.Waiting = []HandoverWork{}
		}
	}
	var c HandoverRecord
	if a.DB.C(store.CHandovers).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "from": me, "kind": KindHandover, "state": bson.M{"$in": bson.A{StateReceived, StateDone}},
		"cleanedAt": nil, "overtakenBy": nil}, options.FindOne().SetSort(bson.D{{Key: "settledAt", Value: -1}})).Decode(&c) == nil {
		out.Clean = &c
	}
	switch {
	case len(out.InProgress) > 0:
		r := out.InProgress[0]
		switch {
		case r.From == me && (r.State == StateExporting || r.State == StateExported):
			out.Summary = fmt.Sprintf("Handing this computer's role to “%s”: the handover file waits to be received there. This computer no longer continues the conversations in it.", r.ToName)
		case r.To == me && (r.State == StateExporting || r.State == StateExported):
			out.Summary = fmt.Sprintf("“%s” is handing its role to this computer: receive the handover file here.", r.FromName)
		case r.From == me && r.State == StateReceiving:
			out.Summary = fmt.Sprintf("“%s” is receiving this computer's role now.", r.ToName)
		case r.State == StateReceiving:
			out.Summary = fmt.Sprintf("This computer is receiving “%s”'s role. If that stopped part way, receive the same file again.", r.FromName)
		case r.From == me:
			out.Summary = role.Says
		default:
			out.Summary = fmt.Sprintf("This computer has “%s”'s role; “%s” steps down once its own running work is done.", r.FromName, r.FromName)
		}
	default:
		out.Summary = role.Says
	}
	return out, nil
}

/* ---------- Stepping down, in the background ---------- */

// StepDownEvery is how often MetaRush running in the background looks at
// this computer's role.
var StepDownEvery = 10 * time.Second

// Look is one look at this computer's role, for MetaRush running in the
// background: once another computer has received the role, the work that
// can move moves, and once nothing of its own runs here any more the
// handover is done (it stepped down). It returns the role after the look.
func Look(ctx context.Context, a *app.App) (HandoverRole, []HandoverWork, error) {
	me := a.Cfg.Installation.ID
	role, err := RoleOf(ctx, a, me)
	if err == nil && role.Role == RoleHost {
		err = settleOvertaken(ctx, a, me, role.Latest)
	}
	if err != nil || role.Role != RoleSteppingDown {
		return role, nil, err
	}
	rec := role.Latest
	if _, err := moveWork(ctx, a, me, rec.To, rec.ToName, false); err != nil {
		return role, nil, err
	}
	waiting, err := stillThere(ctx, a, me)
	if err != nil || len(waiting) > 0 {
		return role, waiting, err
	}
	res, err := a.DB.C(store.CHandovers).UpdateOne(ctx, bson.M{"_id": rec.ID, "workspaceId": a.WorkspaceID(), "state": StateReceived},
		bson.M{"$set": bson.M{"state": StateDone}, "$unset": bson.M{"active": ""}, "$currentDate": bson.M{"steppedDownAt": true}})
	if err != nil {
		return role, nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't record that this computer stepped down.")
	}
	if res.ModifiedCount == 1 {
		b := *a
		b.Actor.Via = "daemon"
		b.Audit(ctx, "machine.handover.stepdown", "handover", rec.ID, "", map[string]any{"to": rec.ToName})
	}
	role, err = RoleOf(ctx, a, me)
	return role, nil, err
}

// settleOvertaken ends this computer's own handovers that a newer one
// overtook: the role came back to it (handed back, or taken over) before it
// had stepped down, so there is nothing left to step down from. Otherwise
// such a handover would stay in progress for good, and no handover from this
// computer could start again.
func settleOvertaken(ctx context.Context, a *app.App, me string, latest *HandoverRecord) error {
	if latest == nil || latest.To != me || latest.SettledAt == nil {
		return nil
	}
	_, err := a.DB.C(store.CHandovers).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "from": me, "active": true, "state": StateReceived,
		"settledAt": bson.M{"$lt": *latest.SettledAt}},
		bson.M{"$set": bson.M{"state": StateDone, "overtakenBy": latest.ID}, "$unset": bson.M{"active": ""}, "$currentDate": bson.M{"steppedDownAt": true}})
	if err != nil {
		return apperr.Wrap(apperr.Database, err, "MetaRush couldn't read the handovers.")
	}
	return nil
}

// Watch follows this computer's role while MetaRush runs in the background:
// changed is called once the role is no longer what it started as — a host
// (or one stepping down) became a client, or a client became a host again.
func Watch(ctx context.Context, a *app.App, client bool, logf func(string, ...any), changed func(HandoverRole)) {
	said := -1
	t := time.NewTicker(StepDownEvery)
	defer t.Stop()
	for {
		lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		role, waiting, err := Look(lctx, a)
		cancel()
		if err == nil {
			switch {
			case client && role.Role == RoleHost:
				changed(role)
				return
			case !client && role.Role == RoleClient:
				changed(role)
				return
			case role.Role == RoleSteppingDown && len(waiting) != said && logf != nil:
				said = len(waiting)
				logf("“%s” has taken over this computer's role: %s still to finish here before it steps down", role.Latest.ToName, plural(len(waiting), "piece of work", "pieces of work"))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
