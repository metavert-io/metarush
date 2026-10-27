package fixes

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/calls"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Marking the fixes that fit (D153): the inspect job measures a source once
// its first look is done (and again on request), on the original.

// Thresholds: a fix fits from these measurements on.
const (
	ShakeFits     = 0.25 // % of the picture's width, frame to frame
	CastFits      = 0.06 // colour cast relative to brightness
	DarkFits      = 0.30 // average brightness under it
	BrightFits    = 0.72 // average brightness over it
	FlatFits      = 0.55 // range of brightness under it
	GrainFits     = 3.0  // 8-bit levels
	BlocksFits    = 1.25 // block-edge steps over inner steps
	SpreadFits    = 6.0  // dB between the loudest and the quietest person
	CleanFits     = 25.0 // speech over background, dB, under it
	SpeechRange   = 12.0 // dB of loudness range, over it (speech)
	MusicRange    = 16.0 // dB of loudness range, over it (music)
	HumFits       = 15.0 // dB a mains hum stands out
	EndsFits      = 0.5  // seconds of silence at either end
	QuietSpeech   = -32.0
	QuietMusicDB  = -26.0
	LoudMusicDB   = -8.0
	inspectWindow = 24 * time.Hour
)

func init() {
	// Once a clip's first look is done, measure it for its fixes (once per original).
	ops.OnSourceFacts(func(ctx context.Context, a *app.App, assetID, why string) {
		if why != ops.FactsAnalysis {
			return
		}
		var as models.Asset
		if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as) != nil || as.State != models.AssetAvailable || as.SHA256 == "" || as.HasTag(models.TagEnhanced) || as.MadeNarration() {
			return
		}
		if as.Fixes != nil && as.Fixes.MarkedAt != nil && as.Fixes.MarksFor == as.SHA256 {
			return
		}
		if pending(ctx, a, as.ID) {
			return
		}
		_, _ = QueueInspect(ctx, a, &as, false)
	})
}

// pending reports whether a clip's first analyses are still to run.
func pending(ctx context.Context, a *app.App, assetID string) bool {
	n, err := a.DB.C(store.CJobs).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": assetID,
		"type": bson.M{"$in": bson.A{jobs.TypeProxy, jobs.TypeTranscribe, jobs.TypeDiarize, jobs.TypeEvidence}}, "state": bson.M{"$in": bson.A{models.JobQueued, models.JobRunning}}})
	return err == nil && n > 0
}

// QueueInspect queues the measurement of a clip (again: a fresh one).
func QueueInspect(ctx context.Context, a *app.App, as *models.Asset, again bool) (*models.Job, error) {
	sha := as.SHA256
	if len(sha) > 16 {
		sha = sha[:16]
	}
	key := TypeInspect + ":" + as.ID + ":" + sha
	if again {
		key += fmt.Sprintf(":%d", time.Now().UnixNano())
	}
	j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: TypeInspect, IdempotencyKey: key, Priority: 2, AssetID: as.ID, ProjectID: firstOf(as.ProjectIDs),
		MaxAttempts: 2, Actor: a.Actor, Payload: map[string]any{"assetId": as.ID}})
	return j, err
}

// Inspect is the inspect job: it measures the original for each fix of the
// clip's kind and records which fit.
func Inspect(ctx context.Context, a *app.App, j *models.Job, report func(float64, string)) (map[string]any, error) {
	id, _ := j.Payload["assetId"].(string)
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), id, &as); err != nil {
		return nil, Final(fmt.Errorf("clip %s not found", id))
	}
	if as.State != models.AssetAvailable {
		return nil, fmt.Errorf("clip %s is not ready yet", as.ID)
	}
	src, err := a.Cache.Ensure(ctx, a.Media, as.StorageKey, as.SHA256, as.Size, nil)
	if err != nil {
		return nil, err
	}
	kind := kindOf(ctx, a, &as)
	report(0.1, "measuring the "+map[bool]string{true: "picture and sound", false: "sound"}[as.Media.HasVideo])
	marks, err := Measure(ctx, a, &as, src, kind, nil)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	set := bson.M{"fixes.marks": marks, "fixes.markedAt": now, "fixes.marksFor": as.SHA256, "updatedAt": now}
	if as.Fixes == nil || as.Fixes.KindBy != models.TitleByPerson {
		set["fixes.kind"] = kind
	}
	if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": set}); err != nil {
		return nil, err
	}
	fits := 0
	for _, m := range marks {
		if m.Fits {
			fits++
		}
	}
	return map[string]any{"kind": kind, "measured": len(marks), "fit": fits}, nil
}

// Measure measures a file for a kind of source's fixes (the original for
// the marks, a fixed copy for the after values). only limits it to some
// fixes (nil: all of the kind's).
func Measure(ctx context.Context, a *app.App, as *models.Asset, src, kind string, only []string) ([]models.FixMark, error) {
	want := func(id string) bool {
		if len(only) > 0 {
			for _, o := range only {
				if o == id {
					return true
				}
			}
			return false
		}
		return offered(kind, id)
	}
	var marks []models.FixMark
	dur := as.Media.Duration
	if want(models.FixSteady) && as.Media.HasVideo {
		var v float64
		var err error
		between := ""
		if kind == models.SourceCall {
			// A call's picture switches (D190): the shake is measured between the
			// switches, never across one.
			v, err = ShakeBetween(ctx, a.Tools, src, dur, as.Media.FPS, callCuts(ctx, a, as))
			between = " between the picture's switches"
		} else {
			v, err = Shake(ctx, a.Tools, src, dur)
		}
		if err != nil {
			return nil, err
		}
		m := models.FixMark{Fix: models.FixSteady, Value: round(v, 3), Unit: "%", Measured: fmt.Sprintf("shake: %.2f%% of the picture's width a frame%s", v, between)}
		if v >= ShakeFits {
			m.Fits = true
			m.Why = "The camera shakes"
			if v >= 2*ShakeFits {
				m.Why = "The camera shakes a lot"
			}
		}
		marks = append(marks, m)
	}
	if want(models.FixColour) && as.Media.HasVideo {
		c, err := Colour(ctx, a.Tools, src, dur)
		if err != nil {
			return nil, err
		}
		m := models.FixMark{Fix: models.FixColour, Value: round(c.Cast, 3), Unit: "cast", Measured: fmt.Sprintf("colour cast %.0f%%, brightness %.0f%%, range %.0f%%", c.Cast*100, c.Brightness*100, c.Range*100)}
		var why []string
		if c.Cast >= CastFits {
			why = append(why, "the colours lean "+castWords(c))
		}
		switch {
		case c.Brightness < DarkFits:
			why = append(why, "it's dark")
		case c.Brightness > BrightFits:
			why = append(why, "it's too bright")
		}
		if c.Range < FlatFits {
			why = append(why, "it looks flat")
		}
		if len(why) > 0 {
			m.Fits, m.Why = true, capFirst(joinAnd(why))
		}
		marks = append(marks, m)
	}
	if want(models.FixGrain) && as.Media.HasVideo {
		g, err := Grain(ctx, a.Tools, src, dur)
		if err != nil {
			return nil, err
		}
		m := models.FixMark{Fix: models.FixGrain, Value: round(g, 2), Unit: "levels", Measured: fmt.Sprintf("grain: %.1f levels", g)}
		if g >= GrainFits {
			m.Fits, m.Why = true, "The picture is grainy"
		}
		marks = append(marks, m)
	}
	if want(models.FixBlocks) && as.Media.HasVideo {
		b, err := Blockiness(ctx, a.Tools, src, dur)
		if err != nil {
			return nil, err
		}
		m := models.FixMark{Fix: models.FixBlocks, Value: round(b, 3), Unit: "ratio", Measured: fmt.Sprintf("blockiness: %.2f (1 is none)", b)}
		if b >= BlocksFits {
			m.Fits, m.Why = true, "The picture looks blocky"
		}
		marks = append(marks, m)
	}
	needSound := as.Media.HasAudio && (want(models.FixVolumes) || want(models.FixVoices) || want(models.FixLoudness) || want(models.FixEnds))
	if needSound {
		spans, names := speechSpans(ctx, a, as)
		sf, err := Sound(ctx, a.Tools, src, spans)
		if err != nil {
			return nil, err
		}
		if want(models.FixVolumes) {
			m := models.FixMark{Fix: models.FixVolumes, Value: round(sf.Spread, 1), Unit: "dB"}
			switch {
			case len(spans) == 0:
				m.Pending = "once the transcript says who speaks when"
			case len(sf.Speakers) < 2:
				m.Measured = "one person speaks"
			default:
				m.Measured = fmt.Sprintf("%s is %.0f dB quieter than %s", nameOf(names, sf.Quietest), sf.Spread, nameOf(names, sf.Loudest))
				if sf.Spread >= SpreadFits {
					m.Fits, m.Why = true, fmt.Sprintf("%s is much quieter than %s", nameOf(names, sf.Quietest), nameOf(names, sf.Loudest))
				}
			}
			marks = append(marks, m)
		}
		if want(models.FixVoices) {
			m := models.FixMark{Fix: models.FixVoices, Value: round(sf.Clean, 1), Unit: "dB", Measured: fmt.Sprintf("speech %.0f dB over the background", sf.Clean)}
			if sf.Clean < CleanFits && sf.Background > -65 {
				m.Fits, m.Why = true, "There's background noise"
			}
			marks = append(marks, m)
		}
		if want(models.FixLoudness) {
			limit, music := SpeechRange, kind == models.SourceMusic
			if music {
				limit = MusicRange
			}
			m := models.FixMark{Fix: models.FixLoudness, Value: round(sf.Range, 1), Unit: "dB", Measured: fmt.Sprintf("loudness moves over %.0f dB; average %.0f dB", sf.Range, sf.Level)}
			lufs := sf.Level
			if l := evidence.LoadLoudness(ctx, a.DB, a.WorkspaceID(), []string{as.ID}, store.InsightMedia{})[as.ID]; l.IntegratedLUFS != nil {
				lufs = *l.IntegratedLUFS
			}
			switch {
			case sf.Range > limit:
				m.Fits, m.Why = true, "The loudness jumps around"
			case !music && lufs < QuietSpeech:
				m.Fits, m.Why = true, "It's very quiet"
			case music && lufs < QuietMusicDB:
				m.Fits, m.Why = true, "It's quieter than most music"
			case music && lufs > LoudMusicDB:
				m.Fits, m.Why = true, "It's louder than most music"
			}
			marks = append(marks, m)
		}
		if want(models.FixEnds) {
			lead, trail := sf.Start, math.Max(0, sf.Duration-sf.End)
			m := models.FixMark{Fix: models.FixEnds, Value: round(math.Max(lead, trail), 2), Unit: "s", Measured: fmt.Sprintf("%.1f s of silence at the start, %.1f s at the end", lead, trail)}
			if lead >= EndsFits || trail >= EndsFits {
				m.Fits = true
				switch {
				case lead >= EndsFits && trail >= EndsFits:
					m.Why = "There's silence at the start and the end"
				case lead >= EndsFits:
					m.Why = "There's silence at the start"
				default:
					m.Why = "There's silence at the end"
				}
			}
			marks = append(marks, m)
		}
	}
	if want(models.FixHum) && as.Media.HasAudio {
		h, f0, err := Hum(ctx, a.Tools, src)
		if err != nil {
			return nil, err
		}
		m := models.FixMark{Fix: models.FixHum, Value: round(h, 1), Unit: "dB", Measured: fmt.Sprintf("a %d Hz hum %.0f dB over the sound around it", f0, h)}
		if h >= HumFits {
			m.Fits, m.Why = true, "There's a hum"
		}
		marks = append(marks, m)
	}
	return marks, nil
}

// callCuts are where a call recording's picture switches (seconds; the
// switches insight, D160): never nil, so the shake measurement also leaves out
// switches the insight doesn't have yet.
func callCuts(ctx context.Context, a *app.App, as *models.Asset) []float64 {
	out := []float64{}
	if st := calls.Load(ctx, a.DB, a.WorkspaceID(), as.ID); st != nil {
		for _, s := range st.Switches {
			out = append(out, s.At)
		}
	}
	return out
}

// speechSpans reads the transcript's stretches with who speaks, and the
// voices' names by their labels.
func speechSpans(ctx context.Context, a *app.App, as *models.Asset) ([]Span, map[string]string) {
	names := map[string]string{}
	if as.Transcript == nil || as.Transcript.State != "available" {
		return nil, names
	}
	var segs []models.TranscriptSegment
	_ = a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), bson.M{"assetId": as.ID}, &segs, options.Find().SetSort(bson.D{{Key: "idx", Value: 1}}).
		SetProjection(bson.M{"from": 1, "to": 1, "speaker": 1, "speakerName": 1}))
	var out []Span
	for _, s := range segs {
		out = append(out, Span{From: s.From, To: s.To, Speaker: s.Speaker})
		if s.SpeakerName != "" && s.Speaker != "" {
			names[s.Speaker] = s.SpeakerName
		}
	}
	return out, names
}

func nameOf(names map[string]string, label string) string {
	if n := names[label]; n != "" {
		return n
	}
	return models.SpeakerDisplayLabel(label)
}

func castWords(c *ColourFacts) string {
	r, g, b := c.Mean[0], c.Mean[1], c.Mean[2]
	switch {
	case r > g && r > b && b < g:
		return "warm (orange)"
	case b > r && b > g:
		return "cold (blue)"
	case g > r && g > b:
		return "green"
	case r > g && b > g:
		return "magenta"
	}
	return "to one colour"
}

// measureEnds measures where music starts and stops, for "Trim silence at
// the ends" (no copy: where it plays from).
func measureEnds(ctx context.Context, a *app.App, as *models.Asset) (*models.SoundTrim, error) {
	if !as.Media.HasAudio {
		return nil, nil
	}
	src, err := a.Cache.Ensure(ctx, a.Media, as.StorageKey, as.SHA256, as.Size, nil)
	if err != nil {
		return nil, err
	}
	lv, err := audioLevels(ctx, a.Tools, src, levelStep)
	if err != nil {
		return nil, err
	}
	sf := soundFacts(lv, nil)
	end := sf.End
	if end <= sf.Start {
		end = sf.Duration
	}
	return &models.SoundTrim{Start: round(sf.Start, 2), End: round(math.Min(end, as.Media.Duration), 2), Duration: round(as.Media.Duration, 2)}, nil
}

func round(v float64, n int) float64 {
	p := math.Pow(10, float64(n))
	return math.Round(v*p) / p
}

func capFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// FinalError is a failure that trying again won't fix.
type FinalError struct{ Err error }

func (e *FinalError) Error() string { return e.Err.Error() }
func (e *FinalError) Unwrap() error { return e.Err }

// Final marks an error as final.
func Final(err error) error { return &FinalError{Err: err} }
