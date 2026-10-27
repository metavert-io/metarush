package speakers

import (
	"context"
	"errors"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/voice"
)

// ErrNotMerged means the voice was never combined with another one.
var ErrNotMerged = errors.New("that voice was not combined with another one")

// ErrLegacyMerge means two voices were combined before MetaRush kept track of
// whose words were whose, and they cannot be told apart on this machine
// (speaker detection is not installed here).
var ErrLegacyMerge = errors.New("these voices were combined before MetaRush kept track of whose words were whose; fix who said what line by line instead, or separate them on a machine that can tell voices apart")

// UnmergeResult says what separating voices did.
type UnmergeResult struct {
	Kept      models.AssetSpeaker   `json:"kept"`
	Separated []models.AssetSpeaker `json:"separated"`
	Library   *LibraryChange        `json:"-"`
}

// Unmerge separates voices that were combined: label is a voice that was
// folded into another (it comes back), or a voice others were folded into
// (they all come back). Its words, counts and samples return, and the voice it
// was merged into is as it was before; voice profiles are rebuilt.
func Unmerge(ctx context.Context, a *app.App, assetID, label string) (*UnmergeResult, error) {
	all, err := Voices(ctx, a, assetID, true)
	if err != nil {
		return nil, err
	}
	v, err := ResolveLabel(all, label)
	if err != nil {
		return nil, err
	}
	byLabel := map[string]models.AssetSpeaker{}
	for _, x := range all {
		byLabel[x.Label] = x
	}
	var drops []models.AssetSpeaker
	keepLabel := v.Label
	if v.MergedInto != "" {
		drops = []models.AssetSpeaker{*v}
		keepLabel = v.MergedInto
	} else {
		for _, x := range all {
			if x.MergedInto == v.Label {
				drops = append(drops, x)
			}
		}
	}
	if len(drops) == 0 {
		return nil, ErrNotMerged
	}
	// The most recent merge is undone first (its snapshot is of the voice as it is now).
	sort.SliceStable(drops, func(i, j int) bool { return drops[i].UpdatedAt.After(drops[j].UpdatedAt) })
	ch := newChanges()
	ch.Asset(assetID)
	var pcm []float32
	var emb voice.Embedder
	res := &UnmergeResult{Separated: []models.AssetSpeaker{}}
	for _, d := range drops {
		keep, ok := byLabel[keepLabel]
		if !ok {
			return nil, ErrNoVoice
		}
		legacy := d.Before == nil
		if legacy && pcm == nil {
			var as models.Asset
			if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as); err != nil {
				return nil, err
			}
			if emb = embedFor(a); emb == nil {
				if n, _ := countFrom(ctx, a, assetID, d.Label); n == 0 {
					return nil, ErrLegacyMerge
				}
			} else if pcm, err = pcmFor(ctx, a, &as); err != nil {
				return nil, ErrLegacyMerge
			}
		}
		kept, err := unmergeOne(ctx, a, keep, d, pcm, emb, ch)
		if err != nil {
			return nil, err
		}
		byLabel[keepLabel] = kept
	}
	lib, err := ch.Apply(ctx, a)
	if err != nil {
		return nil, err
	}
	res.Library = lib
	vs, _ := Voices(ctx, a, assetID, false)
	for _, x := range vs {
		if x.Label == keepLabel {
			res.Kept = x
		}
		for _, d := range drops {
			if x.Label == d.Label {
				res.Separated = append(res.Separated, x)
			}
		}
	}
	a.Audit(ctx, "speakers.unmerge", "asset", assetID, "", map[string]any{"kept": keepLabel, "separated": len(drops)})
	return res, nil
}

func countFrom(ctx context.Context, a *app.App, assetID, label string) (int64, error) {
	return a.DB.C(store.CSegments).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": assetID, "speakerFrom": label})
}

// unmergeOne gives drop its words back and restores keep.
func unmergeOne(ctx context.Context, a *app.App, keep, drop models.AssetSpeaker, pcm []float32, emb voice.Embedder, ch *Changes) (models.AssetSpeaker, error) {
	ws := a.WorkspaceID()
	now := time.Now().UTC()
	ch.Person(keep.PersonID, drop.PersonID)
	n, err := countFrom(ctx, a, keep.AssetID, drop.Label)
	if err != nil {
		return keep, err
	}
	if n > 0 {
		// Words remember which voice they came from.
		if _, err := a.DB.C(store.CSegments).UpdateMany(ctx, bson.M{"workspaceId": ws, "assetId": keep.AssetID, "speakerFrom": drop.Label},
			bson.M{"$set": bson.M{"speaker": drop.Label}, "$unset": bson.M{"speakerFrom": "", "speakerName": ""}}); err != nil {
			return keep, err
		}
	} else if emb != nil && pcm != nil {
		// Combined before origins were kept: each line goes to the voice it sounds like.
		if err := splitByVoice(ctx, a, keep, drop, pcm, emb); err != nil {
			return keep, err
		}
	} else {
		return keep, ErrLegacyMerge
	}
	if b := drop.Before; b != nil {
		ch.Person(b.PersonID)
		keep.Name, keep.PersonID, keep.Source, keep.Score, keep.Suggestions, keep.Rejected = b.Name, b.PersonID, b.Source, b.Score, b.Suggestions, b.Rejected
		keep.Seconds, keep.Fingerprinted, keep.Turns, keep.Words, keep.First, keep.Minor = b.Seconds, b.Fingerprinted, b.Turns, b.Words, b.First, b.Minor
		keep.Samples, keep.Confirmed, keep.Pitch, keep.Voiced = b.Samples, b.Confirmed, b.Pitch, b.Voiced
	} else {
		// Before snapshots existed, a merge also put both fingerprints on the kept voice.
		keep.Exemplars, keep.ExSeconds = withoutVecs(keep.Exemplars, keep.ExSeconds, drop.Exemplars)
		if len(keep.Exemplars) > 0 {
			keep.Centroid = voice.Mean(keep.Exemplars, keep.ExSeconds)
		}
		keep.Samples = withoutSpans(keep.Samples, drop.Samples)
		segs, err := loadSegments(ctx, a, keep.AssetID)
		if err != nil {
			return keep, err
		}
		runs := runsOf(segs)
		keep.Seconds, keep.Words, keep.Turns, keep.First = voiceStats(runs, keep.Label)
		keep.Fingerprinted = round3(max(0, keep.Fingerprinted-drop.Fingerprinted))
	}
	if keep.Samples == nil {
		keep.Samples = []models.TimeSpan{}
	}
	keep.UpdatedAt = now
	if err := saveVoice(ctx, a, keep); err != nil {
		return keep, err
	}
	if err := a.DB.Update(ctx, store.CSpeakers, ws, drop.ID, bson.M{"$unset": bson.M{"mergedInto": "", "mergedBy": "", "before": ""}, "$set": bson.M{"updatedAt": now}}); err != nil {
		return keep, err
	}
	return keep, nil
}

// splitByVoice moves the lines of keep that sound like drop back to drop.
func splitByVoice(ctx context.Context, a *app.App, keep, drop models.AssetSpeaker, pcm []float32, emb voice.Embedder) error {
	own, _ := withoutVecs(keep.Exemplars, keep.ExSeconds, drop.Exemplars)
	keepC := voice.Mean(own, nil)
	dropC := drop.Centroid
	if len(keepC) == 0 || len(dropC) == 0 {
		return ErrLegacyMerge
	}
	segs, err := loadSegments(ctx, a, keep.AssetID)
	if err != nil {
		return err
	}
	type pick struct {
		i    int
		drop bool
		sure bool
	}
	var picks []pick
	for i, s := range segs {
		if s.Speaker != keep.Label || s.SpeakerFrom != "" {
			continue
		}
		p := pick{i: i}
		if v := voice.EmbedSpan(pcm, voice.Span{From: s.From, To: s.To}, emb); v != nil {
			dk, dd := voice.Cosine(v, keepC), voice.Cosine(v, dropC)
			p.drop, p.sure = dd > dk, abs(dd-dk) >= 0.05
		}
		picks = append(picks, p)
	}
	// Lines too short to tell go with the nearest line that could be told.
	for k := range picks {
		if picks[k].sure {
			continue
		}
		best, bd := -1, 1e18
		for j := range picks {
			if !picks[j].sure {
				continue
			}
			if d := abs(segs[picks[j].i].From - segs[picks[k].i].From); d < bd {
				best, bd = j, d
			}
		}
		if best >= 0 {
			picks[k].drop = picks[best].drop
		}
	}
	for _, p := range picks {
		if !p.drop {
			continue
		}
		if err := a.DB.Update(ctx, store.CSegments, a.WorkspaceID(), segs[p.i].ID, bson.M{"$set": bson.M{"speaker": drop.Label}, "$unset": bson.M{"speakerName": ""}}); err != nil {
			return err
		}
	}
	return nil
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// withoutVecs removes from vs the vectors that are in drop (exact copies).
func withoutVecs(vs [][]float32, secs []float64, drop [][]float32) ([][]float32, []float64) {
	var out [][]float32
	var os []float64
	for i, v := range vs {
		same := false
		for _, d := range drop {
			if equalVec(v, d) {
				same = true
				break
			}
		}
		if same {
			continue
		}
		out = append(out, v)
		if i < len(secs) {
			os = append(os, secs[i])
		}
	}
	return out, os
}

func equalVec(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func withoutSpans(xs, drop []models.TimeSpan) []models.TimeSpan {
	out := []models.TimeSpan{}
	for _, x := range xs {
		same := false
		for _, d := range drop {
			if x == d {
				same = true
			}
		}
		if !same {
			out = append(out, x)
		}
	}
	return out
}

// unmergeAuto separates voices that were combined into label only because both
// sounded like the same person (when that no longer holds).
func unmergeAuto(ctx context.Context, a *app.App, assetID, label string) error {
	var drops []models.AssetSpeaker
	if err := a.DB.Find(ctx, store.CSpeakers, a.WorkspaceID(), bson.M{"assetId": assetID, "mergedInto": label, "mergedBy": models.SpeakerByAuto}, &drops); err != nil {
		return err
	}
	if len(drops) == 0 {
		return nil
	}
	sort.SliceStable(drops, func(i, j int) bool { return drops[i].UpdatedAt.After(drops[j].UpdatedAt) })
	var keep models.AssetSpeaker
	if err := a.DB.C(store.CSpeakers).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": assetID, "label": label, "mergedInto": bson.M{"$in": bson.A{nil, ""}}}).Decode(&keep); err != nil {
		return err
	}
	ch := newChanges()
	for _, d := range drops {
		k, err := unmergeOne(ctx, a, keep, d, nil, nil, ch)
		if err != nil && !errors.Is(err, ErrLegacyMerge) {
			return err
		}
		keep = k
	}
	return nil
}
