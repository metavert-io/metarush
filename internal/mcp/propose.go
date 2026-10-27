package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Access is what a connection may do (D222, D248): read only (the default),
// propose (every change is saved as a draft for a person to review, D248), or
// make changes, each confirmed with a second call (--allow-changes).
type Access string

// The three kinds of connection.
const (
	ReadOnly Access = "read-only"
	Propose  Access = "propose"
	Changes  Access = "changes"
)

// Accesses lists them, read-only first.
func Accesses() []Access { return []Access{ReadOnly, Propose, Changes} }

// AccessOf is the access a server's flags give.
func AccessOf(allowChanges, propose bool) Access {
	switch {
	case propose:
		return Propose
	case allowChanges:
		return Changes
	}
	return ReadOnly
}

// offersChanges: the connection has tools for changes (made, or saved as drafts).
func (a Access) offersChanges() bool { return a != ReadOnly }

// offers says whether a connection offers op as (part of) a tool: reads
// always; with changes allowed every change; when proposing, the changes a
// draft may hold. People-only operations never.
func (a Access) offers(op *ops.Op) bool {
	switch {
	case op.HumanOnly:
		return false
	case !op.Mutating:
		return true
	case a == Changes:
		return true
	case a == Propose:
		return ops.Draftable(op)
	}
	return false
}

// draftWords is what every tool that saves drafts says.
const draftWords = " On this connection it saves a draft for a person to review; nothing changes until they approve it."

// draftNoteField is the field a direct tool takes the app's reason in (its
// operation's own fields keep their names).
const draftNoteField = "draftNote"

// proposeDraft saves a change as a draft (draft.save) instead of making it:
// what every change does on a propose-only connection.
func (s *Server) proposeDraft(ctx context.Context, tool string, op *ops.Op, args map[string]any, note, replaces string) map[string]any {
	delete(args, "confirmToken") // a draft needs no token: a person approves it
	delete(args, "mcpConfirmToken")
	input, _ := json.Marshal(args)
	raw, _ := json.Marshal(ops.DraftSaveIn{Op: op.Name, Input: input, Note: note, Replaces: replaces})
	out, err := ops.Call(ctx, s.a, ops.OpDraftSave, raw, ops.Caller{})
	if err != nil {
		return s.failed(err)
	}
	v := out.(*ops.DraftView)
	lines := []string{fmt.Sprintf("Saved as a draft for a person to review (nothing changed): %s.", strings.TrimSuffix(v.Title, "."))}
	for _, c := range v.Changes {
		lines = append(lines, "- "+c)
	}
	lines = append(lines, "They approve it, say Not quite with what should change, or turn it down; "+toolDrafts+" shows their decision. Draft: "+v.ID)
	return s.answer(v, strings.Join(lines, "\n"))
}

// toolDrafts is the tool that lists an app's drafts (draft.list).
const toolDrafts = "draft_list"

// news says, once, what people decided about this app's drafts since it was
// last told (D249): the answer to its next call carries it.
func (s *Server) news(ctx context.Context) string {
	agent := s.a.Actor.Agent
	if agent == "" || s.a.DB == nil {
		return ""
	}
	var ds []models.Draft
	if err := s.a.DB.Find(ctx, store.CDrafts, s.a.WorkspaceID(), bson.M{"agent": agent, "decision": bson.M{"$exists": true}, "decision.seen": bson.M{"$ne": true}}, &ds,
		options.Find().SetSort(bson.D{{Key: "decision.at", Value: 1}}).SetLimit(5)); err != nil || len(ds) == 0 {
		return ""
	}
	lines := []string{"News about your drafts:"}
	idsSeen := bson.A{}
	for _, d := range ds {
		dec := d.Decision
		who := dec.By.UserName
		if who == "" {
			who = "A person"
		}
		title := strings.TrimSuffix(d.Title, ".")
		switch dec.State {
		case models.DraftApproved:
			l := fmt.Sprintf("- %s approved “%s”.", who, title)
			if dec.Result != "" {
				l += " " + dec.Result
			}
			lines = append(lines, l)
		case models.DraftNotQuite:
			lines = append(lines, fmt.Sprintf("- %s said Not quite about “%s”: “%s”. Make a new draft with that change (replaces %s).", who, title, dec.Note, d.ID))
		case models.DraftTurnedDown:
			l := fmt.Sprintf("- %s turned down “%s”.", who, title)
			if dec.Note != "" {
				l += fmt.Sprintf(" “%s”", dec.Note)
			}
			lines = append(lines, l)
		}
		idsSeen = append(idsSeen, d.ID)
	}
	_, _ = s.a.DB.C(store.CDrafts).UpdateMany(ctx, bson.M{"workspaceId": s.a.WorkspaceID(), "_id": bson.M{"$in": idsSeen}}, bson.M{"$set": bson.M{"decision.seen": true}})
	return strings.Join(lines, "\n")
}

// withNews adds the news to a tool result, as a second text block.
func withNews(res map[string]any, news string) map[string]any {
	if news == "" || res == nil {
		return res
	}
	content, _ := res["content"].([]any)
	res["content"] = append(content, map[string]any{"type": "text", "text": news})
	return res
}

// proposeOnlyRefusal is a change asked for by a tool this list doesn't offer
// on a propose-only connection.
func proposeOnlyRefusal(tool string, op *ops.Op) error {
	return apperr.New(apperr.Unauthorized, "This connection only proposes: changes have a tool of their own, which saves them as drafts for a person to review.").
		WithDetails(map[string]any{"reason": "mcp_propose_only", "tool": tool, "op": op.Name}).WithCommand(toolRunChange)
}
