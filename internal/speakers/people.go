package speakers

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/faces"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/naming"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
	"github.com/metavert-io/metarush/internal/voice"
)

// Appearance is one video a person speaks in.
type Appearance struct {
	AssetID    string `json:"assetId"`
	AssetName  string `json:"assetName"`  // the clip's filename
	AssetTitle string `json:"assetTitle"` // the clip as people see it

	ProjectIDs []string          `json:"projectIds"`
	Label      string            `json:"label"`
	Seconds    float64           `json:"seconds"`
	Source     string            `json:"source" jsonschema:"enum=person,enum=auto"`
	Score      float64           `json:"score,omitempty"`
	First      float64           `json:"first"`
	Samples    []models.TimeSpan `json:"samples"`
	// HasVideo: the clip has a picture (samples show as video tiles).
	HasVideo bool    `json:"hasVideo"`
	Pitch    float64 `json:"pitch,omitempty"`
}

// PersonSample is one listening sample of a person, in one video.
type PersonSample struct {
	AssetID    string  `json:"assetId"`
	AssetTitle string  `json:"assetTitle"`
	From       float64 `json:"from"`
	To         float64 `json:"to"`
	HasVideo   bool    `json:"hasVideo"`
	Source     string  `json:"source" jsonschema:"enum=person,enum=auto"`
	Score      float64 `json:"score,omitempty"`
	// Box is where their face is in the picture at From (a face a person
	// confirmed: [x, y, width, height] from the top-left, 0 to 1).
	Box []float64 `json:"box,omitempty"`
}

// PersonSummary is a person with where they appear.
type PersonSummary struct {
	models.Person
	Voiceprint  bool         `json:"voiceprint"` // has a voice profile for recognition
	Exemplars   int          `json:"exemplars"`
	Appearances []Appearance `json:"appearances"`
	// Samples are a few listening samples across different videos (named ones
	// first), so a wrong label stands out at a glance.
	Samples []PersonSample `json:"samples"`
	// Face is a moment of a video where the person speaks (a still to show next to their name).
	Face *PersonSample `json:"face,omitempty"`
	// Pitch is the typical pitch (Hz) of the voices people confirmed are them (0 when unknown).
	Pitch float64 `json:"pitch,omitempty"`
}

// People lists the workspace's people (merged ones excluded), most heard first.
func People(ctx context.Context, a *app.App, withAppearances bool) ([]PersonSummary, error) {
	var ps []models.Person
	if err := a.DB.Find(ctx, store.CPeople, a.WorkspaceID(), bson.M{"mergedInto": bson.M{"$in": bson.A{nil, ""}}}, &ps,
		options.Find().SetSort(bson.D{{Key: "seconds", Value: -1}, {Key: "name", Value: 1}})); err != nil {
		return nil, err
	}
	out := make([]PersonSummary, 0, len(ps))
	var faceOf map[string]models.FaceTrack
	if withAppearances {
		ids := make([]string, 0, len(ps))
		for _, p := range ps {
			ids = append(ids, p.ID)
		}
		faceOf = faces.BestOfPeople(ctx, a, ids)
	}
	for _, p := range ps {
		s := PersonSummary{Person: p, Voiceprint: len(p.Exemplars) > 0, Exemplars: len(p.Exemplars), Appearances: []Appearance{}, Samples: []PersonSample{}}
		if withAppearances {
			app, err := Appearances(ctx, a, p.ID)
			if err != nil {
				return nil, err
			}
			s.Appearances = app
			assets, secs := map[string]bool{}, 0.0
			var pitches []voice.Pitch
			for _, ap := range app {
				assets[ap.AssetID] = true
				secs += ap.Seconds
			}
			s.Recordings, s.Seconds = len(assets), round3(secs)
			s.Samples, s.Face = samplesOf(app, 4)
			if f, ok := faceOf[p.ID]; ok { // a face they confirmed is the best picture of them
				s.Face = &PersonSample{AssetID: f.AssetID, From: f.At, To: f.At, HasVideo: true, Source: models.SpeakerByPerson, Box: f.Box}
			}
			if cv, err := confirmedVoices(ctx, a, p.ID); err == nil {
				for _, v := range cv {
					pitches = append(pitches, voice.Pitch{Hz: v.Pitch, Voiced: v.Voiced})
				}
			}
			if pt := voice.CombinePitch(pitches); pt.Voiced >= voice.MinVoiced {
				s.Pitch = pt.Hz
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// samplesOf picks up to n samples from different videos (named appearances
// first, then recognized ones by score) and a face (the first video sample).
func samplesOf(app []Appearance, n int) ([]PersonSample, *PersonSample) {
	sorted := append([]Appearance(nil), app...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if (sorted[i].Source == models.SpeakerByPerson) != (sorted[j].Source == models.SpeakerByPerson) {
			return sorted[i].Source == models.SpeakerByPerson
		}
		return sorted[i].Seconds > sorted[j].Seconds
	})
	out := []PersonSample{}
	var face *PersonSample
	for _, ap := range sorted {
		if len(ap.Samples) == 0 {
			continue
		}
		s := ap.Samples[0]
		ps := PersonSample{AssetID: ap.AssetID, AssetTitle: ap.AssetTitle, From: s.From, To: s.To, HasVideo: ap.HasVideo, Source: ap.Source, Score: ap.Score}
		if face == nil && ap.HasVideo {
			f := ps
			face = &f
		}
		if len(out) < n {
			out = append(out, ps)
		}
	}
	return out, face
}

// Appearances lists every video a person's voice is labeled in.
func Appearances(ctx context.Context, a *app.App, personID string) ([]Appearance, error) {
	var vs []models.AssetSpeaker
	if err := a.DB.Find(ctx, store.CSpeakers, a.WorkspaceID(), bson.M{"personId": personID, "mergedInto": bson.M{"$in": bson.A{nil, ""}}}, &vs,
		options.Find().SetProjection(bson.M{"centroid": 0, "exemplars": 0, "before": 0})); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(vs))
	for _, v := range vs {
		ids = append(ids, v.AssetID)
	}
	var assets []models.Asset
	if len(ids) > 0 {
		if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}}, &assets, options.Find().SetProjection(bson.M{"name": 1, "title": 1, "projectIds": 1, "media": 1})); err != nil {
			return nil, err
		}
	}
	byID := map[string]models.Asset{}
	for _, as := range assets {
		byID[as.ID] = as
	}
	out := make([]Appearance, 0, len(vs))
	for _, v := range vs {
		as := byID[v.AssetID]
		pids := as.ProjectIDs
		if pids == nil {
			pids = []string{}
		}
		samples := v.Samples
		if samples == nil {
			samples = []models.TimeSpan{}
		}
		out = append(out, Appearance{AssetID: v.AssetID, AssetName: as.Name, AssetTitle: as.DisplayTitle(), ProjectIDs: pids, Label: v.Label, Seconds: v.Seconds, Source: v.Source,
			Score: v.Score, First: v.First, Samples: samples, HasVideo: as.Media.HasVideo, Pitch: v.Pitch})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AssetTitle < out[j].AssetTitle })
	return out, nil
}

// RenamePerson renames a person everywhere they appear.
func RenamePerson(ctx context.Context, a *app.App, personID, name string) (*models.Person, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" || len([]rune(name)) > 80 {
		return nil, errors.New("a name of 1 to 80 characters is required")
	}
	p, err := loadPerson(ctx, a, personID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := a.DB.Update(ctx, store.CPeople, a.WorkspaceID(), p.ID, bson.M{"$set": bson.M{"name": name, "nameKey": NameKey(name), "updatedAt": now}}); err != nil {
		return nil, err
	}
	assets, err := relinkVoices(ctx, a, p.ID, p.ID, name)
	if err != nil {
		return nil, err
	}
	faces.Rename(ctx, a, p.ID, name)
	for _, id := range assets {
		if err := SyncNames(ctx, a, id); err != nil {
			return nil, err
		}
		_ = Reconcile(ctx, a, id)
	}
	a.Audit(ctx, "people.rename", "person", p.ID, "", map[string]any{"from": p.Name, "to": name})
	return loadPerson(ctx, a, p.ID)
}

// relinkVoices points every voice of person from to person to (named name)
// and returns the affected assets.
func relinkVoices(ctx context.Context, a *app.App, from, to, name string) ([]string, error) {
	var vs []models.AssetSpeaker
	if err := a.DB.Find(ctx, store.CSpeakers, a.WorkspaceID(), bson.M{"personId": from}, &vs, options.Find().SetProjection(bson.M{"assetId": 1})); err != nil {
		return nil, err
	}
	if _, err := a.DB.C(store.CSpeakers).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "personId": from}, bson.M{"$set": bson.M{"personId": to, "name": name, "naming": naming.Record(ctx, a.Actor), "updatedAt": time.Now().UTC()}}); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, v := range vs {
		if !seen[v.AssetID] {
			seen[v.AssetID] = true
			out = append(out, v.AssetID)
		}
	}
	return out, nil
}

// MergePeople folds person from into person into: every voice, voice sample
// and name moves; videos where both were heard get a single voice. The
// remaining person's voice profile is rebuilt from both people's confirmed voices.
func MergePeople(ctx context.Context, a *app.App, fromID, intoID string) (*models.Person, error) {
	from, err := loadPerson(ctx, a, fromID)
	if err != nil {
		return nil, err
	}
	into, err := loadPerson(ctx, a, intoID)
	if err != nil {
		return nil, err
	}
	if from.ID == into.ID {
		return nil, errors.New("that is the same person")
	}
	assets, err := relinkVoices(ctx, a, from.ID, into.ID, into.Name)
	if err != nil {
		return nil, err
	}
	faces.Merge(ctx, a, from.ID, into.ID, into.Name)
	now := time.Now().UTC()
	set := bson.M{"mergedInto": into.ID, "updatedAt": now}
	if into.Notes == "" && from.Notes != "" {
		_ = a.DB.Update(ctx, store.CPeople, a.WorkspaceID(), into.ID, bson.M{"$set": bson.M{"notes": from.Notes}})
	}
	if err := a.DB.Update(ctx, store.CPeople, a.WorkspaceID(), from.ID, bson.M{"$set": set}); err != nil {
		return nil, err
	}
	ch := newChanges()
	ch.Person(into.ID)
	for _, id := range assets {
		if err := dedupeVoices(ctx, a, id, into.ID); err != nil {
			return nil, err
		}
		ch.Asset(id)
	}
	_, _ = todos.Close(ctx, a, duplicateKey(from.ID, into.ID), models.ResolvedElsewhere, "combined")
	_, _ = todos.CloseStale(ctx, a, "dup:"+from.ID+":", nil)
	if _, err := ch.Apply(ctx, a); err != nil {
		return nil, err
	}
	a.Audit(ctx, "people.merge", "person", into.ID, "", map[string]any{"merged": from.ID, "name": from.Name})
	return loadPerson(ctx, a, into.ID)
}

// dedupeVoices merges voices of one person within one asset into the first.
// Whoever combined the two people names the merged voice (D274): a person's
// merge is a person's naming, an AI app's or Auto's is theirs — never the
// naming one of the voices had before.
func dedupeVoices(ctx context.Context, a *app.App, assetID, personID string) error {
	voices, err := Voices(ctx, a, assetID, false)
	if err != nil {
		return err
	}
	var same []models.AssetSpeaker
	for _, v := range voices {
		if v.PersonID == personID {
			same = append(same, v)
		}
	}
	if len(same) < 2 {
		return nil
	}
	keep := same[0]
	for _, d := range same[1:] {
		dd := d
		if err := mergeVoice(ctx, a, &keep, &dd, models.SpeakerByPerson); err != nil {
			return err
		}
		if d.Source == models.SpeakerByPerson {
			keep.Source = models.SpeakerByPerson
		}
	}
	keep.Naming = naming.Record(ctx, a.Actor)
	keep.UpdatedAt = time.Now().UTC()
	return saveVoice(ctx, a, keep)
}

// SplitInput says a person's voice in one video is really someone else.
type SplitInput struct {
	PersonID, AssetID string
	// Label is one voice of the video ("" = all of the person's voices in it).
	Label string
	// Who it really is: an ID, or a name (existing, or with NewPerson a new one).
	OtherID   string
	Name      string
	NewPerson bool
}

// SplitPerson moves a person's voices in one video (optionally one voice) to
// another person — found by name, or new — when two people were wrongly
// combined. Both people's voice profiles are rebuilt, so nothing the moved
// voice contributed stays with the first person.
func SplitPerson(ctx context.Context, a *app.App, in SplitInput) (*models.Person, error) {
	p, err := loadPerson(ctx, a, in.PersonID)
	if err != nil {
		return nil, err
	}
	voices, err := Voices(ctx, a, in.AssetID, false)
	if err != nil {
		return nil, err
	}
	var move []models.AssetSpeaker
	for _, v := range voices {
		if v.PersonID == p.ID && (in.Label == "" || v.Label == in.Label || NameKey(v.DisplayName()) == NameKey(in.Label)) {
			move = append(move, v)
		}
	}
	if len(move) == 0 {
		return nil, ErrNoVoice
	}
	target, _, err := personFor(ctx, a, in.Name, in.OtherID, in.NewPerson || (in.OtherID == "" && NameKey(in.Name) == p.NameKey))
	if err != nil {
		return nil, err
	}
	if target.ID == p.ID {
		return nil, errors.New("choose a different name for the other person")
	}
	ch := newChanges()
	ch.Asset(in.AssetID)
	ch.Person(p.ID, target.ID)
	for _, v := range move {
		v.PersonID, v.Name, v.Source, v.Score, v.Suggestions, v.UpdatedAt = target.ID, target.Name, models.SpeakerByPerson, 0, nil, time.Now().UTC()
		v.Naming = naming.Record(ctx, a.Actor)
		v.Rejected = append(without(v.Rejected, p.ID), p.ID)
		if err := saveVoice(ctx, a, v); err != nil {
			return nil, err
		}
	}
	if _, err := ch.Apply(ctx, a); err != nil {
		return nil, err
	}
	a.Audit(ctx, "people.split", "person", p.ID, "", map[string]any{"assetId": in.AssetID, "label": in.Label, "to": target.ID})
	return loadPerson(ctx, a, target.ID)
}

// MarkDistinct records that two people are different: they are never asked
// about again as "the same person?", however their voices change.
func MarkDistinct(ctx context.Context, a *app.App, aID, bID string) error {
	for _, pair := range [][2]string{{aID, bID}, {bID, aID}} {
		if _, err := a.DB.C(store.CPeople).UpdateOne(ctx, bson.M{"_id": pair[0], "workspaceId": a.WorkspaceID()}, bson.M{"$addToSet": bson.M{"distinct": pair[1]}}); err != nil {
			return err
		}
	}
	_, err := todos.Close(ctx, a, duplicateKey(aID, bID), models.ResolvedElsewhere, "different people")
	return err
}

// UpdateNotes sets (or with "" clears) what people want MetaRush and Claude to
// know about a person.
func UpdateNotes(ctx context.Context, a *app.App, personID, notes string) (*models.Person, error) {
	p, err := loadPerson(ctx, a, personID)
	if err != nil {
		return nil, err
	}
	notes = strings.TrimSpace(strings.ReplaceAll(notes, "\r\n", "\n"))
	if n := len([]rune(notes)); n > MaxNotes {
		return nil, fmt.Errorf("notes are at most %d characters (these are %d)", MaxNotes, n)
	}
	upd := bson.M{"$set": bson.M{"notes": notes, "updatedAt": time.Now().UTC()}}
	if notes == "" {
		upd = bson.M{"$unset": bson.M{"notes": ""}, "$set": bson.M{"updatedAt": time.Now().UTC()}}
	}
	if err := a.DB.Update(ctx, store.CPeople, a.WorkspaceID(), p.ID, upd); err != nil {
		return nil, err
	}
	a.Audit(ctx, "people.update", "person", p.ID, "", map[string]any{"notes": len([]rune(notes))})
	return loadPerson(ctx, a, p.ID)
}

// MaxNotes is the longest a person's notes may be (characters).
const MaxNotes = 2000

func duplicateKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return "dup:" + a + ":" + b
}
