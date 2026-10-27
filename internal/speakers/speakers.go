// Package speakers turns diarization into named voices: it detects who speaks
// when (independently of the speech recognizer), repairs over-segmentation,
// fingerprints each voice, recognizes people a workspace has named before,
// stores the voices of each asset, keeps transcript speaker names in sync and
// asks a person (to-dos) about voices it cannot name by itself.
package speakers

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcribe"
	"github.com/metavert-io/metarush/internal/voice"
)

// Engine runs speaker detection on this machine.
type Engine struct {
	Diarizer *transcribe.Diarizer
	once     sync.Once
	ex       *voice.Extractor
	exErr    error
}

// NewEngine returns nil when speaker detection is not installed.
func NewEngine(a *app.App) *Engine {
	d := transcribe.NewDiarizer(a.Tools)
	if d == nil {
		return nil
	}
	return &Engine{Diarizer: d}
}

// Extractor opens the voice-fingerprint model once (nil when unavailable:
// detection still works, but voices cannot be recognized across recordings).
func (e *Engine) Extractor() *voice.Extractor {
	e.once.Do(func() {
		threads := max(2, runtime.NumCPU()/3)
		e.ex, e.exErr = voice.Open(e.Diarizer.Library(), e.Diarizer.EmbeddingModel(), threads)
	})
	return e.ex
}

// FingerprintError explains why fingerprints are unavailable ("" if they work).
func (e *Engine) FingerprintError() string {
	if e.Extractor() != nil {
		return ""
	}
	if e.exErr != nil {
		return e.exErr.Error()
	}
	return voice.ErrUnavailable.Error()
}

// Detection is one asset's speakers before they are stored.
type Detection struct {
	Run      string
	Result   *voice.Result
	Decision map[string]voice.Decision
	Hint     int
	Raw      int
	Notes    []string
	Prints   bool
	// Previous are the clip's voices before this detection (nil for a first
	// detection): confirmed names, rejections and fixed lines carry over.
	Previous *Previous
}

// Detect finds the voices in a 16 kHz mono WAV: diarization, repair,
// fingerprints and recognition of known people. With hint > 0 the number of
// voices is fixed. When every voice is confidently a known person and there
// are fewer people than the diarizer's clusters, it re-runs diarization with
// that number (cleaner turn boundaries) and keeps the re-run if it agrees.
func (e *Engine) Detect(ctx context.Context, a *app.App, wav string, hint int) (*Detection, error) {
	people, err := LoadProfiles(ctx, a)
	if err != nil {
		return nil, err
	}
	pcm, err := voice.ReadWAV(wav)
	if err != nil {
		return nil, err
	}
	ex := e.Extractor()
	var emb voice.Embedder
	if ex != nil {
		emb = ex
	}
	run := func(n int) (*voice.Result, map[string]voice.Decision, error) {
		turns, err := e.Diarizer.Run(ctx, wav, transcribe.DiarizeOptions{Speakers: n})
		if err != nil {
			return nil, nil, err
		}
		res, err := voice.Refine(turns, pcm, emb, voice.DefaultParams, people...)
		if err != nil {
			return nil, nil, err
		}
		return res, voice.Consolidate(res, people, voice.DefaultThresholds), nil
	}
	res, dec, err := run(hint)
	if err != nil {
		return nil, err
	}
	d := &Detection{Run: ids.New(ids.Run), Result: res, Decision: dec, Hint: hint, Raw: res.Raw, Notes: res.Notes, Prints: ex != nil}
	if hint == 0 && ex != nil {
		if k, ok := allKnown(res, dec); ok && k < res.Raw {
			if res2, dec2, err := run(k); err == nil {
				if k2, ok2 := allKnown(res2, dec2); ok2 && k2 == k && samePeople(dec, dec2) {
					d.Result, d.Decision = res2, dec2
					d.Notes = append(res2.Notes, fmt.Sprintf("every voice was a known person; re-detected with %d voices", k))
				}
			}
		}
	}
	return d, nil
}

// allKnown reports whether every non-minor voice was recognized, and how many people.
func allKnown(res *voice.Result, dec map[string]voice.Decision) (int, bool) {
	n := 0
	for _, c := range res.Clusters {
		if c.Minor {
			continue
		}
		if dec[c.Label].Auto == nil {
			return 0, false
		}
		n++
	}
	return n, n > 0
}

func samePeople(a, b map[string]voice.Decision) bool {
	set := func(m map[string]voice.Decision) string {
		var ps []string
		for _, d := range m {
			if d.Auto != nil {
				ps = append(ps, d.Auto.PersonID)
			}
		}
		sort.Strings(ps)
		return strings.Join(ps, ",")
	}
	return set(a) == set(b)
}

// LoadProfiles reads every named person with a usable voice profile.
func LoadProfiles(ctx context.Context, a *app.App) ([]voice.Profile, error) {
	var ps []models.Person
	if err := a.DB.Find(ctx, store.CPeople, a.WorkspaceID(), bson.M{"mergedInto": bson.M{"$in": bson.A{nil, ""}}, "model": voice.ModelName}, &ps); err != nil {
		return nil, err
	}
	out := make([]voice.Profile, 0, len(ps))
	for _, p := range ps {
		if len(p.Exemplars) == 0 {
			continue
		}
		out = append(out, toProfile(p))
	}
	return out, nil
}

func toProfile(p models.Person) voice.Profile {
	pr := voice.Profile{ID: p.ID, Name: p.Name, Centroid: p.Centroid}
	for _, e := range p.Exemplars {
		pr.Exemplars = append(pr.Exemplars, voice.ProfileExemplar{Source: e.Source, Vec: e.Vec, Seconds: e.Seconds})
	}
	return pr
}

// Apply labels transcript segments with the detected voices (splitting a
// segment where the voice changes) and fills in names of recognized people.
func (d *Detection) Apply(segs []transcribe.Segment, dur float64) []transcribe.Segment {
	if d == nil || d.Result == nil {
		return segs
	}
	out := transcribe.AssignSpeakers(segs, d.Result.Turns, dur)
	return out
}

// StoreResult is what storing a detection changed beyond the clip itself.
type StoreResult struct {
	// Recognized: person → clips where their voice was labeled automatically
	// (this clip included, when the detection recognized them).
	Recognized map[string][]string
	Library    *LibraryChange
}

// Store replaces an asset's voices with a detection, fills in names on its
// transcript segments, updates the asset's speaker status and refreshes its
// to-dos. segs are the stored segments (already labeled by Apply).
func Store(ctx context.Context, a *app.App, as *models.Asset, d *Detection, diarizer string) error {
	_, err := StoreWith(ctx, a, as, d, diarizer)
	return err
}

// StoreWith is Store, reporting what it recognized. When the clip's voices are
// found again (d.Previous), names a person confirmed, rejected names and lines
// a person fixed carry over to the new voices by overlapping time, instead of
// being matched again.
func StoreWith(ctx context.Context, a *app.App, as *models.Asset, d *Detection, diarizer string) (*StoreResult, error) {
	now := time.Now().UTC()
	ws := a.WorkspaceID()
	segs, err := loadSegments(ctx, a, as.ID)
	if err != nil {
		return nil, err
	}
	words := map[string]int{}
	for _, s := range segs {
		n := len(s.Words)
		if n == 0 {
			n = 1
		}
		words[s.Speaker] += n
	}
	carried := carryOver(ctx, a, d.Previous, segs)
	var docs []models.AssetSpeaker
	named := 0
	res := &StoreResult{Recognized: map[string][]string{}}
	ch := newChanges()
	ch.Asset(as.ID)
	for i, c := range d.Result.Clusters {
		sp := models.AssetSpeaker{ID: ids.New(ids.Speaker), WorkspaceID: ws, AssetID: as.ID, Run: d.Run, Label: c.Label, Seconds: c.Seconds,
			Fingerprinted: c.Embedded, Turns: c.Turns, Words: words[c.Label], First: c.First, Minor: c.Minor, Color: i, CreatedAt: now, UpdatedAt: now,
			Samples: []models.TimeSpan{}, Pitch: c.Pitch, Voiced: c.Voiced}
		for _, s := range c.Samples {
			sp.Samples = append(sp.Samples, models.TimeSpan{From: s.From, To: s.To})
		}
		if len(c.Centroid) > 0 {
			sp.Model, sp.Centroid = voice.ModelName, c.Centroid
			for _, x := range c.Exemplars {
				sp.Exemplars = append(sp.Exemplars, x.Vec)
				sp.ExSeconds = append(sp.ExSeconds, x.Len())
			}
		}
		k := carried[c.Label]
		sp.Rejected = k.rejected
		dec := d.Decision[c.Label]
		switch {
		case k.person != nil:
			// A person said who this is before: that stands.
			sp.Name, sp.PersonID, sp.Source, sp.Confirmed, sp.Minor, sp.Naming = k.person.Name, k.person.ID, models.SpeakerByPerson, k.confirmed, false, k.naming
			ch.Person(k.person.ID)
			named++
		case dec.Auto != nil && !contains(k.rejected, dec.Auto.PersonID):
			sp.Name, sp.PersonID, sp.Source, sp.Score = dec.Auto.Name, dec.Auto.PersonID, models.SpeakerByAuto, dec.Auto.Score
			res.Recognized[dec.Auto.PersonID] = append(res.Recognized[dec.Auto.PersonID], as.ID)
			named++
		default:
			for _, m := range dec.Suggestions {
				if !contains(k.rejected, m.PersonID) {
					sp.Suggestions = append(sp.Suggestions, models.SpeakerMatch{PersonID: m.PersonID, Name: m.Name, Score: m.Score})
				}
			}
		}
		docs = append(docs, sp)
	}
	if d.Previous != nil {
		for _, v := range d.Previous.Voices {
			ch.Person(v.PersonID) // their voices in this clip are replaced: rebuild from what remains
		}
	}
	if _, err := a.DB.C(store.CSpeakers).DeleteMany(ctx, bson.M{"workspaceId": ws, "assetId": as.ID}); err != nil {
		return nil, err
	}
	if len(docs) > 0 {
		ins := make([]any, 0, len(docs))
		for _, x := range docs {
			ins = append(ins, x)
		}
		if _, err := a.DB.C(store.CSpeakers).InsertMany(ctx, ins); err != nil {
			return nil, err
		}
	}
	st := models.SpeakersStatus{State: "available", Run: d.Run, Diarizer: diarizer, Hint: d.Hint, Raw: d.Raw, Count: d.Result.Speakers(),
		Minor: len(d.Result.Clusters) - d.Result.Speakers(), Named: named, Notes: d.Notes, Fingerprints: d.Prints, UpdatedAt: now}
	if err := a.DB.Update(ctx, store.CAssets, ws, as.ID, bson.M{"$set": bson.M{"speakers": st, "transcript.speakers": len(d.Result.Clusters), "updatedAt": now}}); err != nil {
		return nil, err
	}
	// Lines a person fixed keep their speaker, even if detection heard them differently now.
	if err := reapplyConfirmed(ctx, a, as, d.Previous, carried, ch); err != nil {
		return nil, err
	}
	if err := SyncNames(ctx, a, as.ID); err != nil {
		return nil, err
	}
	// Recognized (auto) voices are not enrolled: only voices a person confirmed
	// shape a profile, so one wrong match cannot snowball. Totals do change.
	for _, sp := range docs {
		if sp.PersonID != "" {
			_ = refreshTotals(ctx, a, sp.PersonID)
		}
	}
	lib, err := ch.Apply(ctx, a)
	if err != nil {
		return nil, err
	}
	res.Library = lib
	for p, ids := range lib.Recognized {
		for _, id := range ids {
			if !containsStr(res.Recognized[p], id) {
				res.Recognized[p] = append(res.Recognized[p], id)
			}
		}
	}
	return res, nil
}

// MarkUnavailable records why an asset has no speakers.
func MarkUnavailable(ctx context.Context, a *app.App, assetID, state, reason string) {
	st := models.SpeakersStatus{State: state, Error: reason, UpdatedAt: time.Now().UTC()}
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), assetID, bson.M{"$set": bson.M{"speakers": st}})
}

// Voices lists an asset's current voices (merged ones excluded): Speaker 1…N
// by first appearance, brief voices after them.
func Voices(ctx context.Context, a *app.App, assetID string, includeMerged bool) ([]models.AssetSpeaker, error) {
	filter := bson.M{"assetId": assetID}
	if !includeMerged {
		filter["mergedInto"] = bson.M{"$in": bson.A{nil, ""}}
	}
	var out []models.AssetSpeaker
	if err := a.DB.Find(ctx, store.CSpeakers, a.WorkspaceID(), filter, &out, options.Find().SetSort(bson.D{{Key: "label", Value: 1}})); err != nil {
		return nil, err
	}
	if out == nil {
		out = []models.AssetSpeaker{}
	}
	return out, nil
}

// SyncNames copies voice names onto an asset's transcript segments (merged
// voices first get their target's label; each word keeps the voice it came
// from, so a merge can be undone).
func SyncNames(ctx context.Context, a *app.App, assetID string) error {
	all, err := Voices(ctx, a, assetID, true)
	if err != nil {
		return err
	}
	ws := a.WorkspaceID()
	segs := a.DB.C(store.CSegments)
	byLabel := map[string]models.AssetSpeaker{}
	for _, v := range all {
		byLabel[v.Label] = v
	}
	for _, v := range all {
		if v.MergedInto == "" {
			continue
		}
		r := root(byLabel, v.Label)
		if _, err := segs.UpdateMany(ctx, bson.M{"workspaceId": ws, "assetId": assetID, "speaker": v.Label, "speakerFrom": bson.M{"$in": bson.A{nil, ""}}},
			bson.M{"$set": bson.M{"speaker": r, "speakerFrom": v.Label}}); err != nil {
			return err
		}
		if _, err := segs.UpdateMany(ctx, bson.M{"workspaceId": ws, "assetId": assetID, "speaker": v.Label}, bson.M{"$set": bson.M{"speaker": r}}); err != nil {
			return err
		}
	}
	for _, v := range all {
		if v.MergedInto != "" {
			continue
		}
		var upd bson.M
		if v.Name != "" {
			upd = bson.M{"$set": bson.M{"speakerName": v.Name}}
		} else {
			upd = bson.M{"$unset": bson.M{"speakerName": ""}}
		}
		if _, err := segs.UpdateMany(ctx, bson.M{"workspaceId": ws, "assetId": assetID, "speaker": v.Label}, upd); err != nil {
			return err
		}
	}
	return nil
}

func root(byLabel map[string]models.AssetSpeaker, label string) string {
	for i := 0; i < 32; i++ {
		v, ok := byLabel[label]
		if !ok || v.MergedInto == "" {
			return label
		}
		label = v.MergedInto
	}
	return label
}

// WriteSamples extracts every voice's listening samples as WAV files.
func WriteSamples(ctx context.Context, a *app.App, as *models.Asset, src, dir string, voices []models.AssetSpeaker, cut func(ctx context.Context, src, out string, from, to float64) error) ([]SampleFile, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var out []SampleFile
	for _, v := range voices {
		for i, s := range v.Samples {
			name := fmt.Sprintf("%s-%s-%d.wav", strings.TrimSuffix(filepath.Base(as.Name), filepath.Ext(as.Name)), v.Label, i+1)
			p := filepath.Join(dir, safeFile(name))
			if err := cut(ctx, src, p, s.From, s.To); err != nil {
				return nil, err
			}
			out = append(out, SampleFile{Label: v.Label, Name: v.DisplayName(), From: s.From, To: s.To, Path: p})
		}
	}
	return out, nil
}

// SampleFile is one written listening sample.
type SampleFile struct {
	Label string  `json:"label"`
	Name  string  `json:"name"`
	From  float64 `json:"from"`
	To    float64 `json:"to"`
	Path  string  `json:"path"`
}

func safeFile(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
