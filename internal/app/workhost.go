package app

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/store"
)

// Work asked for on a computer that handed its role over (D349). Such a
// computer runs no worker, recipe engine or triggers (D346), so work pinned
// to it would never run. Instead, work that needs nothing of this computer's
// own is pinned to the computer that has its role now: recipe runs and
// "Planning by itself", link imports without the browser's sign-in,
// repositories and web pages, scene descriptions (that computer's Claude
// Code answers for the role, as it does the panel's conversations). Work
// that needs this computer's own keys, sign-ins, set-up programs or files is
// refused in plain words, naming where to start it: AI work and generated
// shots (their keys and models are checked here), a link import with this
// browser's sign-in, a Remotion import, refresh or push, an ability's run, a
// post to a video platform. Work that runs in the asking process (an AI voice,
// a move of the media, a recording) runs here as before.

// WorkHost is the computer that does this computer's background work: this
// one, or the one its role went to.
type WorkHost struct {
	ID   string
	Name string
	// Here: this computer does its own background work.
	Here bool
}

// handover record states that settle a role (received: the new computer has
// it; done: the old one stepped down).
var roleSettled = bson.A{"received", "done"}

// WorkHost follows this computer's handovers to the computer that has its
// role: the newest settled handover involving a computer decides (D346), one
// from it sends the look on to the computer it went to.
func (a *App) WorkHost(ctx context.Context) (WorkHost, error) {
	return a.WorkHostOf(ctx, a.Cfg.Installation.ID, a.Cfg.Installation.Name)
}

// WorkHostOf is WorkHost for any computer of the workspace (Here: it is
// this computer).
func (a *App) WorkHostOf(ctx context.Context, id, name string) (WorkHost, error) {
	me := WorkHost{ID: id, Name: name, Here: id == a.Cfg.Installation.ID}
	if a.DB == nil || me.ID == "" {
		return me, nil
	}
	cur := me
	seen := map[string]bool{}
	for range 16 {
		if seen[cur.ID] {
			break
		}
		seen[cur.ID] = true
		var r struct {
			From   string `bson:"from"`
			To     string `bson:"to"`
			ToName string `bson:"toName"`
		}
		err := a.DB.C(store.CHandovers).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "settledAt": bson.M{"$ne": nil}, "state": bson.M{"$in": roleSettled},
			"$or": bson.A{bson.M{"from": cur.ID}, bson.M{"to": cur.ID}}},
			options.FindOne().SetSort(bson.D{{Key: "settledAt", Value: -1}, {Key: "_id", Value: -1}}).SetProjection(bson.M{"from": 1, "to": 1, "toName": 1})).Decode(&r)
		if errors.Is(err, mongo.ErrNoDocuments) || (err == nil && r.From != cur.ID) {
			return cur, nil
		}
		if err != nil {
			return me, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read where this computer's work runs.")
		}
		cur = WorkHost{ID: r.To, Name: r.ToName, Here: r.To == a.Cfg.Installation.ID}
	}
	return cur, nil
}

// PinWork is the computer to pin background work to that needs nothing of
// this computer's own: this one, or the one that has its role now.
func (a *App) PinWork(ctx context.Context) (WorkHost, error) {
	return a.WorkHost(ctx)
}

// NeedsThisComputer refuses, on a computer that handed its role over, work
// that needs this computer's own keys, sign-ins, programs or files (what
// names it plainly: "AI work", "a link import with your browser's sign-in").
func (a *App) NeedsThisComputer(ctx context.Context, what string) error {
	h, err := a.WorkHost(ctx)
	if err != nil || h.Here {
		return err
	}
	return apperr.New(apperr.Conflict, "“%s” does this workspace's work now, so %s isn't started here.", h.Name, what).
		WithHint("Start it on “" + h.Name + "” (its dashboard, or a terminal there): its keys, sign-ins and set-up are the ones used now.").
		WithDetails(map[string]any{"reason": "handed_over", "host": h.Name, "hostId": h.ID})
}
