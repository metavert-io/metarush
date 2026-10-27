package worker

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/derive"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcribe"
	"github.com/metavert-io/metarush/internal/transcripts"
)

// detectSpeakers finds the voices in wav and labels the segments with them.
// It never fails the caller: when detection is unavailable or fails, the
// transcript simply has no speakers and the asset says why.
func detectSpeakers(ctx context.Context, w *Worker, as *models.Asset, wav string, segs []transcribe.Segment, hint int, report Report) ([]transcribe.Segment, *speakers.Detection) {
	a := w.App
	if w.Spk == nil {
		speakers.MarkUnavailable(ctx, a, as.ID, "unavailable", "Telling voices apart isn't set up on the machine that made this transcript. Run the setup check in Settings on that machine, then listen for the voices again.")
		return transcribe.AssignSpeakers(segs, nil, 0), nil
	}
	if report != nil {
		report(0.8, "finding who speaks when")
	}
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"speakers.state": "running", "speakers.updatedAt": time.Now().UTC()}})
	det, err := w.Spk.Detect(ctx, a, wav, hint)
	if err != nil {
		w.Log("speaker detection failed for %s: %v", as.ID, err)
		speakers.MarkUnavailable(ctx, a, as.ID, "failed", err.Error())
		return transcribe.AssignSpeakers(segs, nil, 0), nil
	}
	return det.Apply(segs, as.Media.Duration), det
}

// storeSegments replaces an asset's transcript segments.
func storeSegments(ctx context.Context, a *app.App, as *models.Asset, segs []transcribe.Segment, provider, model string) (int, map[string]bool, error) {
	if _, err := a.DB.C(store.CSegments).DeleteMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": as.ID}); err != nil {
		return 0, nil, err
	}
	words, spk := 0, map[string]bool{}
	docs := make([]any, 0, len(segs))
	for i, s := range segs {
		words += len(s.Words)
		if s.Speaker != "" {
			spk[s.Speaker] = true
		}
		docs = append(docs, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Index: i, From: s.From, To: s.To,
			Text: s.Text, Speaker: s.Speaker, Words: s.Words, Provider: provider, Model: model, CreatedAt: time.Now().UTC()})
	}
	if len(docs) > 0 {
		if _, err := a.DB.C(store.CSegments).InsertMany(ctx, docs); err != nil {
			return 0, nil, err
		}
	}
	return words, spk, nil
}

// diarizeHandler re-detects the voices of an already transcribed asset (for
// example after someone said how many people speak) without re-transcribing.
func diarizeHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	a := w.App
	as, err := loadAsset(ctx, a, j)
	if err != nil {
		return nil, err
	}
	if !as.Media.HasAudio {
		return nil, permanent("asset has no audio")
	}
	hint := 0
	if as.Speakers != nil {
		hint = as.Speakers.Hint
	}
	if v, ok := j.Payload["speakers"]; ok {
		switch n := v.(type) {
		case int32:
			hint = int(n)
		case int64:
			hint = int(n)
		case float64:
			hint = int(math.Round(n))
		case int:
			hint = n
		}
	}
	src, err := original(ctx, a, as, report)
	if err != nil {
		return nil, err
	}
	dir, err := scratchDir(a, j)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	wav := filepath.Join(dir, "audio.wav")
	report(0.35, "getting the sound")
	if err := (&derive.Deriver{T: a.Tools}).ExtractAudioWAV(ctx, src, wav); err != nil {
		return nil, err
	}
	var stored []models.TranscriptSegment
	if err := a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), bson.M{"assetId": as.ID}, &stored, options.Find().SetSort(bson.D{{Key: "idx", Value: 1}})); err != nil {
		return nil, err
	}
	segs := make([]transcribe.Segment, 0, len(stored))
	provider, model := "", ""
	for _, s := range stored {
		segs = append(segs, transcribe.Segment{From: s.From, To: s.To, Text: s.Text, Words: s.Words})
		provider, model = s.Provider, s.Model
	}
	prev := speakers.CapturePrevious(ctx, a, as.ID) // names people confirmed carry over to the new voices
	segs, det := detectSpeakers(ctx, w, as, wav, segs, hint, report)
	if det == nil {
		return nil, permanent("speaker detection is unavailable or failed; see the asset's speaker status")
	}
	det.Previous = prev
	if len(stored) > 0 {
		// A fix made while the voices were found is kept: the voices go onto
		// the words as they are now (D145).
		release, err := transcripts.Hold(ctx, a, as.ID)
		if err != nil {
			return nil, err
		}
		if fresh, err := transcripts.LoadSegments(ctx, a, as.ID); err == nil && !sameWords(fresh, stored) {
			now := make([]transcribe.Segment, 0, len(fresh))
			for _, s := range fresh {
				now = append(now, transcribe.Segment{From: s.From, To: s.To, Text: s.Text, Words: s.Words})
			}
			segs = det.Apply(now, as.Media.Duration)
		}
		_, _, err = storeSegments(ctx, a, as, segs, provider, model)
		release()
		if err != nil {
			return nil, err
		}
	}
	res, err := speakers.StoreWith(ctx, a, as, det, w.Spk.Diarizer.Name())
	if err != nil {
		return nil, err
	}
	recognized(ctx, a, j, res)
	return map[string]any{"voices": det.Result.Speakers(), "brief": len(det.Result.Clusters) - det.Result.Speakers(), "raw": det.Raw, "hint": hint}, nil
}

// sameWords reports whether two versions of a clip's lines say the same words.
func sameWords(a, b []models.TranscriptSegment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Text != b[i].Text || len(a[i].Words) != len(b[i].Words) {
			return false
		}
		for k := range a[i].Words {
			if a[i].Words[k] != b[i].Words[k] {
				return false
			}
		}
	}
	return true
}

// recognized tells the person who started a job about voices that were
// recognized automatically (so they can check them).
func recognized(ctx context.Context, a *app.App, j *models.Job, res *speakers.StoreResult) {
	if res == nil || j.CreatedBy.UserID == "" {
		return
	}
	seen := map[string]bool{}
	for pid := range res.Recognized {
		seen[pid] = true
	}
	if res.Library != nil {
		for _, pid := range res.Library.People {
			seen[pid] = true
		}
	}
	for pid := range seen {
		notify.Recognized(ctx, a, j.CreatedBy.UserID, pid)
	}
}
