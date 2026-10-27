package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
)

// AssetRightsIn records licensing for a music (or other third-party audio) file.
type AssetRightsIn struct {
	AssetID     string `json:"assetId"`
	License     string `json:"license" jsonschema:"minLength=1,maxLength=200,description=What allows its use (e.g. a license name or 'original music')."`
	Attribution string `json:"attribution,omitempty" jsonschema:"maxLength=500"`
	Source      string `json:"source,omitempty" jsonschema:"maxLength=500,description=Where it came from (a URL or vendor)."`
}

// AssetRightsOut is the updated asset.
type AssetRightsOut struct {
	Asset   AssetSummary        `json:"asset"`
	Rights  *models.MusicRights `json:"rights"`
	Summary string              `json:"summary"`
}

func setRights(ctx context.Context, a *app.App, in *AssetRightsIn) (*AssetRightsOut, error) {
	lic := strings.TrimSpace(in.License)
	if lic == "" {
		return nil, invalid("Say what allows using this music, for example a license name or “original music”.")
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as); err != nil {
		return nil, notFound("asset", in.AssetID)
	}
	now := time.Now().UTC()
	actor := a.Actor
	r := &models.MusicRights{License: lic, Attribution: strings.TrimSpace(in.Attribution), Source: strings.TrimSpace(in.Source), RecordedBy: &actor, RecordedAt: &now}
	if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"rights": r, "updatedAt": now}}); err != nil {
		return nil, err
	}
	_, _ = todos.Close(ctx, a, rightsKey(as.ID), models.ResolvedElsewhere, lic)
	a.Audit(ctx, "asset.rights", "asset", as.ID, "", map[string]any{"license": lic})
	as.Rights = r
	return &AssetRightsOut{Asset: summarize(as), Rights: r, Summary: fmt.Sprintf("Saved: “%s” can be used (%s).", as.DisplayTitle(), lic)}, nil
}

func rightsKey(assetID string) string { return "rights:" + assetID }

// reconcileRevisionTodos asks about music without recorded rights in a new
// head revision, and offers the cut for approval.
func reconcileRevisionTodos(ctx context.Context, a *app.App, rev *models.Revision, project *models.Project, isHead bool) {
	if rev == nil || project == nil {
		return
	}
	var ids []string
	for _, t := range rev.Spec.Audio {
		if t.Role == editspec.RoleMusic && t.Rights == nil {
			ids = append(ids, t.AssetID)
		}
	}
	if len(ids) > 0 {
		var assets []models.Asset
		_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}}, &assets, options.Find().SetProjection(bson.M{"name": 1, "title": 1, "rights": 1, "projectIds": 1}))
		for _, as := range assets {
			if as.Rights != nil && as.Rights.License != "" {
				continue
			}
			pids := append([]string{project.ID}, as.ProjectIDs...)
			_, _ = todos.Upsert(ctx, a, models.Todo{Kind: models.TodoMusicRights, Scope: "asset", AssetID: as.ID, ProjectID: project.ID, RevisionID: rev.ID,
				ProjectIDs: uniq(pids), GroupKey: rightsKey(as.ID), FreeText: true, Priority: 1,
				Question: fmt.Sprintf("May we use the music “%s”?", as.DisplayTitle()),
				Detail:   fmt.Sprintf("Version %d of “%s” uses this music, but nobody has said where it comes from or whether it's licensed. Platforms can mute or block videos with unlicensed music.", rev.Number, project.Name),
				Context:  models.TodoContext{AssetName: as.Name, AssetTitle: as.DisplayTitle(), ProjectName: project.Name, Revision: rev.Number},
				Suggestions: []models.TodoSuggestion{
					{Label: "It's our own music", Value: "Original music (owned by us)"},
					{Label: "Licensed from a music library", Value: "Licensed from a royalty-free music library"},
					{Label: "Public domain", Value: "Public domain"},
					{Label: "Creative Commons (credit needed)", Value: "Creative Commons, attribution required"},
				}})
		}
	}
	if !isHead || rev.Branch != "main" {
		return
	}
	// One approval question per project: the newest head draft replaces older ones.
	key := "approval:" + project.ID
	if rev.Approval.State != models.ApprovalDraft {
		_, _ = todos.Close(ctx, a, key+"@"+rev.ID, models.ResolvedElsewhere, rev.Approval.State)
		return
	}
	_, _ = todos.CloseStale(ctx, a, key+"@", []string{key + "@" + rev.ID})
	_, _ = todos.Upsert(ctx, a, models.Todo{Kind: models.TodoRevisionApproval, Scope: "revision", RevisionID: rev.ID, ProjectID: project.ID,
		ProjectIDs: []string{project.ID}, GroupKey: key + "@" + rev.ID, HumanOnly: true, Priority: 3,
		Question: fmt.Sprintf("Is version %d of “%s” ready?", rev.Number, project.Name),
		Detail:   "Watch it, then approve it to mark it final (only approved versions are exported), or say not yet to keep working on it.",
		Context:  models.TodoContext{ProjectName: project.Name, Revision: rev.Number},
		Suggestions: []models.TodoSuggestion{
			{Label: "Approve this version", Value: "approve"},
			{Label: "Not yet, keep working on it", Value: "reject"},
		}})
}

var briefQuestions = map[string][2]string{
	"goal":     {"What should “%s” achieve?", "For example: “get people to sign up for the workshop” or “a short teaser for the launch”. Claude uses this to choose the strongest moments."},
	"audience": {"Who is “%s” for?", "For example: “parents of young kids” or “people who already know the product”. It shapes pacing and what needs explaining."},
	"tone":     {"What should “%s” feel like?", "Pick a mood or type your own. It guides music, pacing and captions."},
}

// reconcileBriefTodos asks for the brief fields a story edit needs.
func reconcileBriefTodos(ctx context.Context, a *app.App, p *models.Project) {
	if p == nil {
		return
	}
	have := map[string]bool{"goal": p.Brief.Goal != "", "audience": p.Brief.Audience != "", "tone": p.Brief.Tone != ""}
	for _, f := range []string{"goal", "audience", "tone"} {
		key := "brief:" + p.ID + ":" + f
		if have[f] {
			_, _ = todos.Close(ctx, a, key, models.ResolvedElsewhere, "")
			continue
		}
		q := briefQuestions[f]
		t := models.Todo{Kind: models.TodoBriefMissing, Scope: "project", ProjectID: p.ID, ProjectIDs: []string{p.ID}, GroupKey: key, FreeText: true, Priority: 2,
			Question: fmt.Sprintf(q[0], p.Name), Detail: q[1], Context: models.TodoContext{ProjectName: p.Name, Field: f}}
		if f == "tone" {
			for _, s := range []string{"warm and personal", "energetic", "calm and thoughtful", "funny", "documentary"} {
				t.Suggestions = append(t.Suggestions, models.TodoSuggestion{Label: s, Value: s})
			}
		}
		_, _ = todos.Upsert(ctx, a, t)
	}
}

// closeBriefTodos resolves brief questions whose fields are now filled.
func closeBriefTodos(ctx context.Context, a *app.App, p *models.Project) {
	if p == nil {
		return
	}
	for f, have := range map[string]bool{"goal": p.Brief.Goal != "", "audience": p.Brief.Audience != "", "tone": p.Brief.Tone != ""} {
		if have {
			_, _ = todos.Close(ctx, a, "brief:"+p.ID+":"+f, models.ResolvedElsewhere, "")
		}
	}
}

func uniq(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func init() {
	Def("asset.rights", "Record where a music or audio file comes from and what allows using it", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *AssetRightsIn) (*AssetRightsOut, error) {
		return setRights(ctx, a, in)
	})
}
