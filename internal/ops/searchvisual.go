package ops

import (
	"context"
	"math"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/visual"
)

// DescriptionKind is the insight that keeps written scene descriptions (D74).
const DescriptionKind = "description"

// SearchVisual is a stretch of a clip where the words show up on screen — a
// label or on-screen text from the on-device index — or in a written scene description.
type SearchVisual struct {
	AssetID     string        `json:"assetId"`
	AssetTitle  string        `json:"assetTitle"`
	ProjectID   string        `json:"projectId,omitempty"`
	ProjectName string        `json:"projectName,omitempty"`
	Filed       string        `json:"filed,omitempty" jsonschema:"description=Where a clip in no project is filed: the Inbox\\, a collection or the Archive."`
	From        float64       `json:"from"`
	To          float64       `json:"to"`
	At          float64       `json:"at" jsonschema:"description=Where to look first (source seconds)."`
	Timecode    string        `json:"timecode"`
	MatchedIn   string        `json:"matchedIn" jsonschema:"enum=label,enum=text,enum=description"`
	Text        string        `json:"text" jsonschema:"description=What matched: the label, the on-screen text or the scene description."`
	Snippet     []SnippetPart `json:"snippet"`
	HasThumb    bool          `json:"hasThumb"`
	Rank        string        `json:"rank" jsonschema:"enum=exact,enum=all,enum=forms,enum=partial"`
}

// singular is a crude singular for search words ("dogs" finds "dog").
func singular(w string) string {
	switch {
	case len(w) > 4 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) > 3 && strings.HasSuffix(w, "es") && strings.ContainsAny(w[len(w)-3:len(w)-2], "sxz"):
		return w[:len(w)-2]
	case len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		return w[:len(w)-1]
	}
	return w
}

// visualMatches finds clips by what's on screen and by their scene descriptions.
func visualMatches(ctx context.Context, a *app.App, words []string, phrase, projectID string, scope []string, projName map[string]string, limit int) []SearchVisual {
	var look []string
	seen := map[string]bool{}
	norm := make([]string, 0, len(words))
	for _, w := range words {
		s := singular(w)
		norm = append(norm, s)
		for _, x := range []string{w, s} {
			if !seen[x] {
				seen[x] = true
				look = append(look, x)
			}
		}
	}
	f := bson.M{"kind": bson.M{"$in": bson.A{visual.InsightKind, DescriptionKind}}, "data.terms": bson.M{"$in": look}}
	if scope != nil {
		f["assetId"] = bson.M{"$in": scope}
	}
	var docs []models.Insight
	if err := a.DB.Find(ctx, store.CInsights, a.WorkspaceID(), f, &docs, options.Find().SetLimit(400)); err != nil {
		return nil
	}
	if len(docs) == 0 {
		return nil
	}
	ids := map[string]bool{}
	for _, d := range docs {
		ids[d.AssetID] = true
	}
	need := make([]string, 0, len(ids))
	for id := range ids {
		need = append(need, id)
	}
	var assets []models.Asset
	_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": need}, "trash": bson.M{"$exists": false}}, &assets,
		options.Find().SetProjection(bson.M{"name": 1, "title": 1, "projectIds": 1, "variants.thumbnail": 1}))
	byID := map[string]models.Asset{}
	for _, as := range assets {
		byID[as.ID] = as
	}
	var out []SearchVisual
	for _, d := range docs {
		as, ok := byID[d.AssetID]
		if !ok {
			continue
		}
		base := SearchVisual{AssetID: as.ID, AssetTitle: as.DisplayTitle(), HasThumb: as.Variants[models.VariantThumbnail].StorageKey != ""}
		if len(as.ProjectIDs) > 0 {
			base.ProjectID = as.ProjectIDs[0]
			if projectID != "" {
				base.ProjectID = projectID
			}
			base.ProjectName = projName[base.ProjectID]
		}
		perClip := 0
		switch d.Kind {
		case visual.InsightKind:
			idx := decodeIndex(d.Data)
			if idx == nil {
				continue
			}
			hits := visual.Find(idx, norm)
			if len(hits) == 0 && len(norm) > 0 {
				hits = visual.Find(idx, words)
			}
			for _, h := range mergeHits(hits) {
				if perClip == 3 {
					break
				}
				r := RankForms // every word, but one as the start of a longer word
				switch {
				case h.Partial:
					r = RankPartial
				case h.Exact && phrase != "" && strings.EqualFold(strings.Join(visual.Words(h.Text), " "), strings.Join(norm, " ")):
					r = RankExact
				case h.Exact:
					r = RankAll
				}
				sv := base
				sv.From, sv.To, sv.MatchedIn, sv.Text, sv.Rank = h.From, h.To, h.What, h.Text, r
				sv.At = math.Round((h.From+math.Min(h.To, h.From+1))/2*10) / 10
				_, spans := match(h.Text, norm, strings.Join(norm, " "))
				sv.Snippet = snippet(h.Text, spans, 120)
				sv.Timecode = sourceTimecode(sv.At)
				out = append(out, sv)
				perClip++
			}
		case DescriptionKind:
			for _, sc := range scenesOf(d.Data) {
				if perClip == 3 {
					break
				}
				r, spans := match(sc.Text, words, phrase)
				if r == "" {
					if r2, sp2 := match(sc.Text, norm, strings.Join(norm, " ")); r2 != "" {
						r, spans = RankForms, sp2
					}
				}
				if r == "" {
					continue
				}
				sv := base
				sv.From, sv.To, sv.MatchedIn, sv.Text, sv.Rank = sc.From, sc.To, "description", sc.Text, r
				sv.At = sc.From
				sv.Snippet = snippet(sc.Text, spans, 160)
				sv.Timecode = sourceTimecode(sv.At)
				out = append(out, sv)
				perClip++
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if rankScore[out[i].Rank] != rankScore[out[j].Rank] {
			return rankScore[out[i].Rank] > rankScore[out[j].Rank]
		}
		if out[i].AssetTitle != out[j].AssetTitle {
			return out[i].AssetTitle < out[j].AssetTitle
		}
		return out[i].From < out[j].From
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// mergeHits joins stretches of the same thing that touch.
func mergeHits(hs []visual.Hit) []visual.Hit {
	sort.SliceStable(hs, func(i, j int) bool {
		if hs[i].Text != hs[j].Text {
			return hs[i].Text < hs[j].Text
		}
		return hs[i].From < hs[j].From
	})
	var out []visual.Hit
	for _, h := range hs {
		if n := len(out); n > 0 && out[n-1].Text == h.Text && out[n-1].What == h.What && h.From <= out[n-1].To+0.5 {
			out[n-1].To = math.Max(out[n-1].To, h.To)
			continue
		}
		out = append(out, h)
	}
	// Text first (it is what people search for by name), then by time.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].What != out[j].What {
			return out[i].What == "text"
		}
		return out[i].From < out[j].From
	})
	return out
}

func decodeIndex(data map[string]any) *visual.Index {
	b, err := bson.Marshal(data)
	if err != nil {
		return nil
	}
	var idx visual.Index
	if bson.Unmarshal(b, &idx) != nil {
		return nil
	}
	return &idx
}

// SceneDescription is one scene of a clip in a sentence or two (D74).
type SceneDescription struct {
	From float64 `json:"from" bson:"from"`
	To   float64 `json:"to" bson:"to"`
	Text string  `json:"text" bson:"text" jsonschema:"minLength=1,maxLength=600"`
}

func scenesOf(data map[string]any) []SceneDescription {
	b, err := bson.Marshal(data)
	if err != nil {
		return nil
	}
	var d struct {
		Scenes []SceneDescription `bson:"scenes"`
	}
	if bson.Unmarshal(b, &d) != nil {
		return nil
	}
	return d.Scenes
}
