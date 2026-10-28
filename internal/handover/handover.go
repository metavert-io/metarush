// Package handover hands a computer's role in a workspace to another computer
// (plan item 34; decisions D344–D348): the dashboard and background work,
// the panel's conversations with Claude Code, the work pinned to it (recipe
// runs, "Planning by itself", queued jobs) and its folder settings.
//
// A dry run first (machine.handover.plan) lists, in plain words, everything
// tied to this computer and what happens to each. The conversations travel
// in a private file the person carries (export → receive; never the shared
// bucket), encrypted with a passphrase by default (age's scrypt mode, so `age
// -d` opens it). Receiving puts each Claude Code session where Claude Code
// looks on the new computer, re-pins the conversations, moves the work by
// check and set and sets the folders the person names. The old computer
// steps down by itself once its own running work is done: no background work
// or remote access for the workspace, and it says so; it still works as a
// client. Nothing is deleted there until a person runs `handover clean`.
// A computer that is gone is taken over (machine.takeover) from a plain copy
// of its folders or a handover file, only when it hasn't been seen for a
// while and a person confirms it is off. Every step is a person's and an
// owner's: never Auto, Claude, MCP or a review-desk draft.
package handover

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Kinds and states of a handover record.
const (
	KindHandover = "handover"
	KindTakeover = "takeover"

	// StateExporting: the old computer marked its conversations and is
	// writing the file; StateExported: the file is ready for the new
	// computer; StateReceived: the new computer has the role, the old one
	// finishes its own running work; StateDone: the old computer stepped
	// down (a take-over is done at once); StateAbandoned: a person abandoned
	// it before it was received.
	StateExporting = "exporting"
	StateExported  = "exported"
	StateReceived  = "received"
	StateDone      = "done"
	StateAbandoned = "abandoned"
)

// HandoverComputer names a computer of the workspace.
type HandoverComputer struct {
	ID   string `json:"id" bson:"id"`
	Name string `json:"name" bson:"name"`
}

// HandoverRecord is one handover of a computer's role, or a take-over
// (collection handovers).
type HandoverRecord struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"-"`
	Kind        string `bson:"kind" json:"kind" jsonschema:"enum=handover,enum=takeover"`
	// From is the computer handing its role over (or taken over); To the one
	// taking it.
	From     string `bson:"from" json:"from"`
	FromName string `bson:"fromName" json:"fromName"`
	To       string `bson:"to" json:"to"`
	ToName   string `bson:"toName" json:"toName"`
	State    string `bson:"state" json:"state" jsonschema:"enum=exporting,enum=exported,enum=received,enum=done,enum=abandoned"`
	// Active: in progress (one per computer, a unique index): until the old
	// computer stepped down, or it was abandoned.
	Active bool `bson:"active,omitempty" json:"active"`
	// Conversations are the panel's conversations it carries; Sessions their
	// Claude Code sessions on the old computer (for clean).
	Conversations []string          `bson:"conversations,omitempty" json:"conversations,omitempty"`
	Sessions      map[string]string `bson:"sessions,omitempty" json:"-"`
	// File is the handover file's name (never its folder).
	File      string       `bson:"file,omitempty" json:"file,omitempty"`
	StartedBy models.Actor `bson:"startedBy" json:"startedBy"`
	StartedAt time.Time    `bson:"startedAt" json:"startedAt"`
	// ExportedAt: the file was written; ReceivedAt: the new computer took the
	// role; SettledAt is that moment by the database server's clock (the
	// latest settled handover says which computer has the role).
	ExportedAt    *time.Time    `bson:"exportedAt,omitempty" json:"exportedAt,omitempty"`
	ReceivedAt    *time.Time    `bson:"receivedAt,omitempty" json:"receivedAt,omitempty"`
	ReceivedBy    *models.Actor `bson:"receivedBy,omitempty" json:"receivedBy,omitempty"`
	SettledAt     *time.Time    `bson:"settledAt,omitempty" json:"settledAt,omitempty"`
	SteppedDownAt *time.Time    `bson:"steppedDownAt,omitempty" json:"steppedDownAt,omitempty"`
	AbandonedAt   *time.Time    `bson:"abandonedAt,omitempty" json:"abandonedAt,omitempty"`
	AbandonedBy   *models.Actor `bson:"abandonedBy,omitempty" json:"abandonedBy,omitempty"`
	CleanedAt     *time.Time    `bson:"cleanedAt,omitempty" json:"cleanedAt,omitempty"`
	// Moved counts what moved to the new computer.
	Moved HandoverMoved `bson:"moved" json:"moved"`
	// ConfirmedOff is the person who confirmed a computer that is gone was
	// off (a take-over).
	ConfirmedOff *models.Actor `bson:"confirmedOff,omitempty" json:"confirmedOff,omitempty"`
}

// HandoverMoved counts what a handover moved.
type HandoverMoved struct {
	Conversations int `bson:"conversations" json:"conversations"`
	// Summaries are conversations that go on from a summary (their session
	// didn't come along).
	Summaries int `bson:"summaries" json:"summaries"`
	Runs      int `bson:"runs" json:"runs"`
	Triggers  int `bson:"triggers" json:"triggers"`
	Jobs      int `bson:"jobs" json:"jobs"`
	Folders   int `bson:"folders" json:"folders"`
}

// peopleOnly: a handover is a person's, every time (on top of HumanOnly):
// never Auto, Claude, MCP, an AI app or the review desk (D348).
func peopleOnly(a *app.App) error {
	if a.Actor.Via == models.ViaAssistant || a.Actor.Via == models.ViaAssistantAuto || a.Actor.Agent != "" || a.ProposeOnly || a.Actor.Via == "mcp" {
		return apperr.New(apperr.HumanOnly, "Only a person hands a computer's role over: Claude, Auto and AI apps never do.").
			WithDetails(map[string]any{"reason": "people_only"})
	}
	return nil
}

// findComputer finds a computer of the workspace by name (any case) or ID.
func findComputer(ctx context.Context, a *app.App, nameOrID string) (*models.Installation, error) {
	nameOrID = strings.TrimSpace(nameOrID)
	if nameOrID == "" {
		return nil, apperr.New(apperr.InvalidInput, "Say which computer: its name, as the list of this workspace's computers shows it.").
			WithCommand("metarush machine list")
	}
	filter := bson.M{"name": bson.M{"$regex": "^" + regexp.QuoteMeta(nameOrID) + "$", "$options": "i"}}
	if ids.Valid(ids.Installation, nameOrID) {
		filter = bson.M{"_id": nameOrID}
	}
	var list []models.Installation
	if err := a.DB.Find(ctx, store.CInstallations, a.WorkspaceID(), filter, &list, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}})); err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read this workspace's computers.")
	}
	var live []models.Installation
	for _, i := range list {
		if i.State != models.InstallationRevoked {
			live = append(live, i)
		}
	}
	switch {
	case len(live) == 1:
		return &live[0], nil
	case len(live) > 1:
		return nil, apperr.New(apperr.Conflict, "%d computers are named “%s”.", len(live), nameOrID).
			WithHint("Name the one you mean by its ID (the list's details show them).").WithCommand("metarush machine list --details")
	case len(list) > 0:
		return &list[0], nil // removed from the workspace: a take-over may still name it
	}
	return nil, apperr.New(apperr.NotFound, "There's no computer named “%s” in this workspace.", nameOrID).
		WithHint("Invite it first, and join it to the workspace there; then hand over.").WithCommand("metarush machine list")
}

func getRecord(ctx context.Context, a *app.App, id string) (*HandoverRecord, error) {
	if !ids.Valid(ids.Handover, id) {
		return nil, apperr.New(apperr.NotFound, "MetaRush can't find that handover.").WithDetails(map[string]any{"id": id})
	}
	var r HandoverRecord
	if err := a.DB.GetByID(ctx, store.CHandovers, a.WorkspaceID(), id, &r); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, apperr.New(apperr.NotFound, "MetaRush can't find that handover.").WithDetails(map[string]any{"id": id})
		}
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read the handover.")
	}
	return &r, nil
}

// activeFrom is the handover in progress from a computer (nil: none).
func activeFrom(ctx context.Context, a *app.App, from string) (*HandoverRecord, error) {
	var r HandoverRecord
	err := a.DB.C(store.CHandovers).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "from": from, "active": true}).Decode(&r)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read the handovers.")
	}
	return &r, nil
}

// HandoverRole is where a computer stands: the host of its own work, about
// to step down (the new computer has its role; its own running work
// finishes first), or a client since it stepped down.
type HandoverRole struct {
	// Role is host | stepping-down | client.
	Role string `json:"role" jsonschema:"enum=host,enum=stepping-down,enum=client"`
	// Latest is the newest settled handover involving this computer.
	Latest *HandoverRecord `json:"latest,omitempty"`
	Says   string          `json:"says"`
}

// Roles.
const (
	RoleHost         = "host"
	RoleSteppingDown = "stepping-down"
	RoleClient       = "client"
)

// RoleOf says where the computer stands: the newest settled handover or
// take-over involving it decides (D346) — from it: it steps down (or
// already did); to it, or none: it is a host.
func RoleOf(ctx context.Context, a *app.App, installation string) (HandoverRole, error) {
	var r HandoverRecord
	err := a.DB.C(store.CHandovers).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "settledAt": bson.M{"$ne": nil},
		"state": bson.M{"$in": bson.A{StateReceived, StateDone}}, "$or": bson.A{bson.M{"from": installation}, bson.M{"to": installation}}},
		options.FindOne().SetSort(bson.D{{Key: "settledAt", Value: -1}, {Key: "_id", Value: -1}})).Decode(&r)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return HandoverRole{Role: RoleHost, Says: "This computer does its own work for the workspace."}, nil
	}
	if err != nil {
		return HandoverRole{Role: RoleHost}, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read the handovers.")
	}
	out := HandoverRole{Role: RoleHost, Latest: &r, Says: "This computer does its own work for the workspace."}
	if r.From != installation {
		return out, nil
	}
	if r.State == StateReceived {
		out.Role, out.Says = RoleSteppingDown, "“"+r.ToName+"” has taken over this computer's role. This computer finishes what it is running, then steps down."
		return out, nil
	}
	out.Role = RoleClient
	out.Says = ClientWords(&r)
	return out, nil
}

// ClientWords is what a computer that stepped down says, plainly.
func ClientWords(r *HandoverRecord) string {
	at := ""
	if r.SteppedDownAt != nil {
		at = " on " + r.SteppedDownAt.Local().Format("2 Jan 2006")
	} else if r.SettledAt != nil {
		at = " on " + r.SettledAt.Local().Format("2 Jan 2006")
	}
	return "This computer is no longer the host: it handed its role to “" + r.ToName + "”" + at +
		". It works as a client — open projects, edit and use the panel here — but its background work and remote access for this workspace are off."
}
