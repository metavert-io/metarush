package ops

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/visual"
)

// Written scene descriptions (D74): a sentence or two per scene of a clip,
// written by the person's own Claude Code (footage describe) and kept as the
// clip's "description" insight, so search finds clips by what happens in them.

// MaxScenes is the most descriptions a clip keeps.
const MaxScenes = 80

// DescribeReady reports whether this machine's Claude Code can write
// descriptions (set by internal/assistant; nil: never).
var DescribeReady func(ctx context.Context, a *app.App) bool

// DescribeRun is what one describe conversation produced.
type DescribeRun struct {
	ConversationID string
	Scenes         []SceneDescription
	Model          string
}

// DescribeRunner has this machine's Claude Code describe a clip's scenes (set
// by internal/assistant, which runs it; nil in programs without it).
var DescribeRunner func(ctx context.Context, a *app.App, as *models.Asset, projectID string) (*DescribeRun, error)

// Descriptions are a clip's written scene descriptions.
type Descriptions struct {
	Scenes    []SceneDescription `json:"scenes"`
	By        string             `json:"by" jsonschema:"description=Who wrote them (Claude Code)."`
	Model     string             `json:"model,omitempty"`
	CreatedAt time.Time          `json:"createdAt"`
}

// LoadDescriptions reads a clip's scene descriptions (nil when it has none).
func LoadDescriptions(ctx context.Context, a *app.App, assetID string) *Descriptions {
	var ins []models.Insight
	if err := a.DB.Find(ctx, store.CInsights, a.WorkspaceID(), bson.M{"assetId": assetID, "kind": DescriptionKind}, &ins,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(1)); err != nil || len(ins) == 0 {
		return nil
	}
	d := &Descriptions{Scenes: scenesOf(ins[0].Data), Model: ins[0].Model, CreatedAt: ins[0].CreatedAt, By: "Claude Code"}
	if d.Scenes == nil {
		d.Scenes = []SceneDescription{}
	}
	return d
}

// cleanScenes checks and tidies descriptions: in time order, inside the clip,
// one plain paragraph each.
func cleanScenes(in []SceneDescription, duration float64) ([]SceneDescription, error) {
	if len(in) == 0 {
		return nil, invalid("no scenes to save")
	}
	if len(in) > MaxScenes {
		return nil, invalid("at most %d scenes per clip (got %d)", MaxScenes, len(in))
	}
	out := make([]SceneDescription, 0, len(in))
	for i, s := range in {
		text := strings.Join(strings.FieldsFunc(s.Text, func(r rune) bool { return unicode.IsSpace(r) }), " ")
		switch {
		case text == "":
			return nil, invalid("scene %d has no text", i+1)
		case len([]rune(text)) > 600:
			return nil, invalid("scene %d is longer than 600 characters: keep it to a sentence or two", i+1)
		case math.IsNaN(s.From) || math.IsNaN(s.To) || s.From < 0 || s.To < s.From:
			return nil, invalid("scene %d runs from %.2f to %.2f s: it must start at 0 or later and can't end before it starts", i+1, s.From, s.To)
		case duration > 0 && s.From > duration+0.5:
			return nil, invalid("scene %d starts at %.2f s, after the end of the clip (%.2f s)", i+1, s.From, duration)
		}
		to := s.To
		if duration > 0 {
			to = math.Min(to, duration)
		}
		out = append(out, SceneDescription{From: math.Round(s.From*100) / 100, To: math.Round(to*100) / 100, Text: text})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out, nil
}

// stopWords are left out of a description's search words.
var stopWords = map[string]bool{"the": true, "and": true, "with": true, "for": true, "are": true, "was": true, "his": true, "her": true, "their": true,
	"into": true, "from": true, "that": true, "this": true, "then": true, "while": true, "who": true, "its": true, "has": true, "have": true, "on": true,
	"in": true, "at": true, "of": true, "to": true, "an": true, "is": true, "as": true, "by": true, "it": true, "be": true}

// SaveDescriptions replaces a clip's scene descriptions.
func SaveDescriptions(ctx context.Context, a *app.App, as *models.Asset, scenes []SceneDescription, provider, model string) (int, error) {
	clean, err := cleanScenes(scenes, as.Media.Duration)
	if err != nil {
		return 0, err
	}
	set := map[string]bool{}
	var terms []string
	for _, s := range clean {
		for _, w := range visual.Words(s.Text) {
			if len([]rune(w)) >= 2 && !stopWords[w] && !set[w] {
				set[w] = true
				terms = append(terms, w)
			}
		}
	}
	sort.Strings(terms)
	data := bson.M{"scenes": clean, "terms": terms}
	ins := models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Kind: DescriptionKind, Provider: provider, Model: model,
		PromptVersion: "describe-1", Digest: as.SHA256 + ":describe-1", CreatedAt: time.Now().UTC()}
	ins.Data = map[string]any(data)
	if _, err := a.DB.C(store.CInsights).DeleteMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": as.ID, "kind": DescriptionKind}); err != nil {
		return 0, err
	}
	if err := a.DB.Insert(ctx, store.CInsights, ins); err != nil {
		return 0, err
	}
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"analysis.describe": "available", "updatedAt": time.Now().UTC()}})
	a.Audit(ctx, "footage.describe", "asset", as.ID, firstOr(as.ProjectIDs), map[string]any{"scenes": len(clean), "by": provider})
	return len(clean), nil
}

// DescribeOn says whether written descriptions are on for a clip: its first
// project's choice, else the workspace's.
func DescribeOn(ctx context.Context, a *app.App, projectID string) (bool, *bool, bool) {
	var ws models.Workspace
	_ = a.DB.GetByID(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), &ws)
	var proj *bool
	if projectID != "" {
		var p models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), projectID, &p) == nil {
			proj = p.DescribeScenes
		}
	}
	on := ws.DescribeScenes
	if proj != nil {
		on = *proj
	}
	return on, proj, ws.DescribeScenes
}

// sceneWords says how many scenes, in plain words.
func sceneWords(n int) string { return count(n, "scene", "scenes") }

// DescribeSummary is the plain sentence after saving.
func DescribeSummary(title string, n int) string {
	return fmt.Sprintf("Saved %s for “%s”; search finds them now.", sceneWords(n), title)
}
