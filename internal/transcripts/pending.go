package transcripts

import (
	"context"
	"math"
	"sort"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Fixes collect as pending for every cut whose words they touch (D148): a
// saved version keeps the words it was made with, so a fix made since waits
// until someone makes a new version (nothing renders by itself). A fix is
// pending for a version when it was made after the version's pin for that
// clip and is still in force, or when the version has it and it was taken
// back since.

// PendingFix is a change to words a saved version uses, made since it was saved.
type PendingFix struct {
	models.TranscriptFix
	Clip string `json:"clip" jsonschema:"description=The clip's title."`
	// TakenBack: the version has this fix; it was undone since.
	TakenBack bool `json:"takenBack,omitempty"`
	// At is where it is heard in the cut (seconds on the cut's timeline), or -1
	// when that isn't known (narration, a conversation's recordings).
	At float64 `json:"at"`
}

// Pending is what is waiting for a new version of a cut.
type Pending struct {
	Fixes []PendingFix
	Clips int
}

// window is a stretch of a clip a cut uses (source seconds), where it starts
// on the cut's timeline and how fast it plays (start < 0: unknown).
type window struct {
	from, to float64
	start    float64
	rate     float64
}

// slack: words just outside a clip can still be heard (split edits).
const slack = 1.0

// usedWindows lists the stretches of each clip a spec uses.
func usedWindows(s *editspec.Spec) map[string][]window {
	out := map[string][]window{}
	placed, _ := editspec.Timeline(s)
	fps := float64(s.Output.FPS)
	if fps <= 0 {
		fps = 30
	}
	for i, c := range s.Clips {
		if editspec.IsNested(c.AssetID) || editspec.IsConversation(c.AssetID) {
			continue
		}
		start, rate := -1.0, c.Speed
		if rate <= 0 {
			rate = 1
		}
		if i < len(placed) {
			start = float64(placed[i].From) / fps
		}
		out[c.AssetID] = append(out[c.AssetID], window{from: c.In, to: c.Out, start: start, rate: rate})
	}
	for _, id := range editspec.ConversationAssetIDs(s) {
		out[id] = append(out[id], window{from: 0, to: math.Inf(1), start: -1, rate: 1})
	}
	if v := s.Voiceover; v != nil {
		for _, b := range v.Beats {
			if b.Take != nil {
				out[b.Take.AssetID] = append(out[b.Take.AssetID], window{from: b.Take.In, to: b.Take.Out, start: -1, rate: 1})
			}
		}
		if c := v.Continuous; c != nil {
			out[c.AssetID] = append(out[c.AssetID], window{from: 0, to: math.Inf(1), start: -1, rate: 1})
		}
	}
	return out
}

// PendingFor lists the fixes made since a saved version that touch words it uses.
func PendingFor(ctx context.Context, a *app.App, r *models.Revision) (*Pending, error) {
	used := usedWindows(&r.Spec)
	out := &Pending{Fixes: []PendingFix{}}
	if len(used) == 0 {
		return out, nil
	}
	pins := PinsOf(r)
	assetIDs := make([]string, 0, len(used))
	minPin := math.MaxInt
	for id := range used {
		assetIDs = append(assetIDs, id)
		p, _ := pins.Pin(id)
		minPin = min(minPin, p.Rev)
	}
	sort.Strings(assetIDs)
	var fixes []models.TranscriptFix
	if err := a.DB.Find(ctx, store.CTranscriptFixes, a.WorkspaceID(), bson.M{"assetId": bson.M{"$in": assetIDs},
		"$or": bson.A{bson.M{"rev": bson.M{"$gt": minPin}}, bson.M{"undone.rev": bson.M{"$gt": minPin}}}}, &fixes,
		options.Find().SetSort(bson.D{{Key: "assetId", Value: 1}, {Key: "from", Value: 1}, {Key: "rev", Value: 1}})); err != nil {
		return nil, err
	}
	if len(fixes) == 0 {
		return out, nil
	}
	titles := clipTitles(ctx, a, assetIDs)
	clips := map[string]bool{}
	for _, f := range fixes {
		p, _ := pins.Pin(f.AssetID)
		made := f.Rev > p.Rev
		takenBack := f.Undone != nil && f.Undone.Rev > p.Rev
		if made == takenBack { // not in the version and not now, or in both
			continue
		}
		if f.Superseded && made {
			continue
		}
		at, ok := touches(used[f.AssetID], f)
		if !ok {
			continue
		}
		clips[f.AssetID] = true
		out.Fixes = append(out.Fixes, PendingFix{TranscriptFix: f, Clip: titles[f.AssetID], TakenBack: !made, At: at})
	}
	out.Clips = len(clips)
	sort.SliceStable(out.Fixes, func(i, j int) bool {
		a, b := out.Fixes[i], out.Fixes[j]
		if (a.At < 0) != (b.At < 0) {
			return a.At >= 0
		}
		if a.At != b.At {
			return a.At < b.At
		}
		return a.From < b.From
	})
	return out, nil
}

// touches reports whether a fix is in (or next to) a stretch the cut uses,
// and where it is heard on the cut's timeline.
func touches(ws []window, f models.TranscriptFix) (float64, bool) {
	if f.Kind == models.FixTranscribed {
		at := -1.0
		for _, w := range ws {
			if w.start >= 0 && (at < 0 || w.start < at) {
				at = w.start
			}
		}
		return at, len(ws) > 0
	}
	best, ok := -1.0, false
	for _, w := range ws {
		if f.To < w.from-slack || f.From > w.to+slack {
			continue
		}
		ok = true
		if w.start >= 0 {
			at := w.start + math.Max(0, f.From-w.from)/w.rate
			if best < 0 || at < best {
				best = at
			}
		}
	}
	return math.Round(best*1000) / 1000, ok
}

func clipTitles(ctx context.Context, a *app.App, ids []string) map[string]string {
	var as []models.Asset
	_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}}, &as, options.Find().SetProjection(bson.M{"name": 1, "title": 1}))
	out := map[string]string{}
	for i := range as {
		out[as[i].ID] = as[i].DisplayTitle()
	}
	return out
}
