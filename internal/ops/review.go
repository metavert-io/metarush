package ops

import (
	"context"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// What waits for a person's review (D188): the latest version of a cut that is
// still a draft, when Claude made it or someone asked to have it reviewed (a
// review copy asked for, or Ask for review). A hand edit the studio saved in
// the background is the person's own work in progress, so it doesn't wait.

// madeByAgent names the AI app that made a version ("" for a person's own
// work, D188, D249): a change Claude suggested that a person applied in the
// dashboard, one saved from Claude Code (the terminal's skills, the panel),
// or one an app that named itself saved (Codex, Cursor… through the command
// line or MCP). Gestures on the studio's timeline, saved in the background,
// are a person's.
func madeByAgent(r *models.Revision) string {
	if strings.HasPrefix(r.Origin, "assistant:") {
		return models.AgentClaude
	}
	if strings.HasPrefix(r.Origin, "timeline:") {
		return ""
	}
	if r.CreatedBy.Agent != "" {
		return r.CreatedBy.Agent
	}
	switch r.CreatedBy.Via {
	case models.ViaAssistant, models.ViaAssistantAuto, "cli", "mcp":
		return models.AgentClaude // before apps had names, Claude Code drove the command line
	}
	return ""
}

// madeByClaude reports a version Claude made (in the panel, Claude Code or
// Claude Desktop).
func madeByClaude(r *models.Revision) bool {
	switch madeByAgent(r) {
	case models.AgentClaude, "claude-code", "claude-desktop":
		return true
	}
	return false
}

// askedByPerson reports a request a person made (not MetaRush by itself, not
// Claude's read-only channel).
func askedByPerson(who models.Actor) bool {
	switch who.Via {
	case "worker", "watch", models.ViaAssistant:
		return false
	}
	return true
}

// markReviewAsked records, once, that someone asked to have r reviewed. It sits
// outside the spec, like the approval: the version itself never changes.
func markReviewAsked(ctx context.Context, a *app.App, r *models.Revision, how string) *models.ReviewAsk {
	if r.ReviewAsked != nil {
		return r.ReviewAsked
	}
	ask := &models.ReviewAsk{By: a.Actor, At: time.Now().UTC(), How: how}
	res, err := a.DB.C(store.CRevisions).UpdateOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID(), "reviewAsked": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"reviewAsked": ask}})
	if err != nil || res.ModifiedCount == 0 {
		return nil
	}
	r.ReviewAsked = ask
	a.Audit(ctx, "revision.askReview", "revision", r.ID, r.ProjectID, map[string]any{"number": r.Number, "how": how})
	return ask
}

// WaitingWhy says why something waits for review: claude (Claude made it),
// agent (another AI app made it), draft (an AI app's draft, not in the cut
// yet) or asked (someone asked to have it reviewed).
const (
	WaitingClaude = "claude"
	WaitingAgent  = "agent"
	WaitingDraft  = "draft"
	WaitingAsked  = "asked"
)

// ReviewWaitingIn limits the list.
type ReviewWaitingIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=Only this project's cuts."`
	Limit     int    `json:"limit,omitempty" jsonschema:"description=At most this many (default 50)."`
}

// ReviewWaitingRow is one version waiting for review.
type ReviewWaitingRow struct {
	ProjectID    string          `json:"projectId"`
	ProjectName  string          `json:"projectName"`
	ThumbAssetID string          `json:"thumbAssetId,omitempty"`
	CutTitle     string          `json:"cutTitle,omitempty" jsonschema:"description=The cut's name when the project has more than one cut."`
	Revision     RevisionSummary `json:"revision"`
	Why          string          `json:"why" jsonschema:"enum=claude,enum=agent,enum=draft,enum=asked"`
	// Said is why, in plain words ("Claude made it", "Codex made it, through
	// MetaRush's tools for AI apps", "Maya asked for a review copy").
	Said string `json:"said"`
	// Draft: an AI app's draft, not in the cut until a person approves it
	// (D249): what changes, its review copy, checks and receipt.
	Draft *DraftView `json:"draft,omitempty"`
}

// ReviewWaitingOut lists what waits for review, newest first: versions (the
// cuts' latest ones and AI apps' drafts of new ones), and other changes AI
// apps proposed (Content Settings, names…).
type ReviewWaitingOut struct {
	Versions []ReviewWaitingRow `json:"versions"`
	Changes  []DraftView        `json:"changes"`
}

// AskReviewIn selects a version.
type AskReviewIn struct {
	ID string `json:"id"`
}

func init() {
	Def("review.waiting", "Awaiting your review: each cut's latest version when it is a draft an AI app (Claude, Codex, Cursor…) made or someone asked to have reviewed, and the drafts AI apps proposed — versions not in the cut yet and other changes (hand edits saved in the background don't wait)", Flags{}, func(ctx context.Context, a *app.App, in *ReviewWaitingIn) (*ReviewWaitingOut, error) {
		return ReviewWaiting(ctx, a, in)
	})
	Def("revision.askReview", "Ask for review: the version waits in “Awaiting your review” for people to approve it or say what should change", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *AskReviewIn) (*RevisionSummary, error) {
		r, p, err := getRevision(ctx, a, in.ID)
		if err != nil {
			return nil, err
		}
		markReviewAsked(ctx, a, r, models.ReviewAskAsked)
		s := summarizeRev(a, *r, p.Branches[r.Branch].Head)
		return &s, nil
	})
}

// ReviewWaiting lists the cuts' latest versions that wait for a person's
// review (D188), newest first.
func ReviewWaiting(ctx context.Context, a *app.App, in *ReviewWaitingIn) (*ReviewWaitingOut, error) {
	filter := bson.M{"archived": false}
	if in.ProjectID != "" {
		p, err := resolveProject(ctx, a, in.ProjectID) // its ID or name
		if err != nil {
			return nil, err
		}
		filter["_id"] = p.ID
	}
	var ps []models.Project
	if err := a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), filter, &ps, options.Find().SetSort(bson.D{{Key: "updatedAt", Value: -1}}).SetLimit(500)); err != nil {
		return nil, err
	}
	var heads []string
	for _, p := range ps {
		for _, b := range p.Branches {
			if b.Head != "" {
				heads = append(heads, b.Head)
			}
		}
	}
	out := &ReviewWaitingOut{Versions: []ReviewWaitingRow{}, Changes: []DraftView{}}
	byID := map[string]*models.Project{}
	for i := range ps {
		byID[ps[i].ID] = &ps[i]
	}
	if err := waitingDrafts(ctx, a, in, byID, out); err != nil {
		return nil, err
	}
	if len(heads) == 0 {
		return finishWaiting(out, in.Limit), nil
	}
	var revs []models.Revision
	if err := a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": heads}, "approval.state": models.ApprovalDraft},
		&revs, options.Find().SetProjection(bson.M{"ops": 0, "transcripts": 0})); err != nil {
		return nil, err
	}
	for i := range revs {
		r := &revs[i]
		p := byID[r.ProjectID]
		if p == nil || p.Branches[r.Branch].Head != r.ID {
			continue
		}
		row := ReviewWaitingRow{ProjectID: p.ID, ProjectName: p.Name, ThumbAssetID: p.CoverAssetID, Revision: summarizeRev(a, *r, r.ID)}
		switch agent := madeByAgent(r); {
		case madeByClaude(r):
			row.Why, row.Said = WaitingClaude, "Claude made it"
		case agent != "":
			row.Why, row.Said = WaitingAgent, AgentWords(agent)+" made it"
			if w := ViaWords(r.CreatedBy.Via); w != "" {
				row.Said += ", " + w
			}
		case r.ReviewAsked != nil:
			row.Why = WaitingAsked
			who := r.ReviewAsked.By.UserName
			if who == "" {
				who = "Someone"
			}
			row.Said = who + " asked for a review copy"
			if r.ReviewAsked.How == models.ReviewAskAsked {
				row.Said = who + " asked for review"
			}
		default:
			continue
		}
		if len(p.Branches) > 1 {
			row.CutTitle = p.Branches[r.Branch].Title
		}
		out.Versions = append(out.Versions, row)
	}
	return finishWaiting(out, in.Limit), nil
}

// finishWaiting sorts what waits, newest first, and keeps at most limit of each.
func finishWaiting(out *ReviewWaitingOut, limit int) *ReviewWaitingOut {
	sort.SliceStable(out.Versions, func(i, j int) bool {
		return out.Versions[i].Revision.CreatedAt.After(out.Versions[j].Revision.CreatedAt)
	})
	sort.SliceStable(out.Changes, func(i, j int) bool { return out.Changes[i].CreatedAt.After(out.Changes[j].CreatedAt) })
	if limit <= 0 {
		limit = 50
	}
	if len(out.Versions) > limit {
		out.Versions = out.Versions[:limit]
	}
	if len(out.Changes) > limit {
		out.Changes = out.Changes[:limit]
	}
	return out
}

// waitingDrafts adds the AI apps' drafts that wait for review (D249): a
// version draft as a version row (not in the cut), the rest as changes. A
// draft of a project that was archived doesn't wait.
func waitingDrafts(ctx context.Context, a *app.App, in *ReviewWaitingIn, projects map[string]*models.Project, out *ReviewWaitingOut) error {
	filter := bson.M{"state": bson.M{"$in": bson.A{models.DraftWaiting, models.DraftApproving}}}
	if in.ProjectID != "" {
		ids := make([]string, 0, len(projects))
		for id := range projects {
			ids = append(ids, id)
		}
		filter["projectId"] = bson.M{"$in": ids}
	}
	var ds []models.Draft
	if err := a.DB.Find(ctx, store.CDrafts, a.WorkspaceID(), filter, &ds, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(200)); err != nil {
		return err
	}
	for i := range ds {
		d := &ds[i]
		p := projects[d.ProjectID]
		if d.ProjectID != "" && p == nil {
			continue // archived, or past the projects looked at
		}
		v := viewDraft(ctx, a, d)
		if d.Kind != models.DraftVersion || v.Revision == nil {
			out.Changes = append(out.Changes, *v)
			continue
		}
		row := ReviewWaitingRow{ProjectID: p.ID, ProjectName: p.Name, ThumbAssetID: p.CoverAssetID, CutTitle: v.CutTitle, Revision: *v.Revision,
			Why: WaitingDraft, Said: v.Said + ": a draft, not in the cut yet", Draft: v}
		out.Versions = append(out.Versions, row)
	}
	return nil
}
