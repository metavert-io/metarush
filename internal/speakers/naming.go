package speakers

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/names"
	"github.com/metavert-io/metarush/internal/naming"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/voice"
)

// ErrNoVoice means the asset has no such voice.
var ErrNoVoice = errors.New("no such voice in this video")

// NameKey normalizes a person's name for lookups (case, accents and
// punctuation ignored).
func NameKey(name string) string { return names.Fold(name) }

// ResolveLabel finds a voice by label ("speaker_01"), display name ("Speaker 2"),
// number ("2") or current name ("Ada").
func ResolveLabel(voices []models.AssetSpeaker, s string) (*models.AssetSpeaker, error) {
	s = strings.TrimSpace(s)
	for i := range voices {
		if voices[i].Label == s {
			return &voices[i], nil
		}
	}
	low := strings.ToLower(strings.Join(strings.Fields(s), " "))
	label := ""
	if n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimPrefix(low, "speaker "), "voice ")); err == nil && n >= 1 {
		label = fmt.Sprintf("speaker_%02d", n-1)
	}
	key := NameKey(s)
	for i := range voices {
		v := &voices[i]
		if (label != "" && v.Label == label) || NameKey(v.DisplayName()) == key || (v.Name != "" && NameKey(v.Name) == key) {
			return v, nil
		}
	}
	return nil, ErrNoVoice
}

// ConfirmNeeded asks a person before two voices become one person: another
// voice in the same clip already has that name, or the voice sounds clearly
// different from that person's other voices (one much lower).
type ConfirmNeeded struct {
	Reason string `json:"reason" jsonschema:"enum=same_name_in_clip,enum=voices_differ"`
	Name   string `json:"name"`
	// Other is the other voice in this clip that already has the name.
	Other string `json:"other,omitempty"`
	// Differ: the two pitches are clearly apart (Pitch is this voice's,
	// OtherPitch the other voice's or the person's).
	Differ     bool    `json:"differ"`
	Pitch      float64 `json:"pitch,omitempty"`
	OtherPitch float64 `json:"otherPitch,omitempty"`
	// What is compared (D158): the person, the voice being named (its clip and
	// label) and, for same_name_in_clip, the other voice's label — so the dialog
	// can play them and show what the person's voice was learned from.
	PersonID     string `json:"personId,omitempty"`
	AssetID      string `json:"assetId,omitempty"`
	Speaker      string `json:"speaker,omitempty"`
	OtherSpeaker string `json:"otherSpeaker,omitempty"`
}

func (e *ConfirmNeeded) Error() string { return e.Message() }

// Message says it in plain words.
func (e *ConfirmNeeded) Message() string {
	differ := ""
	if e.Differ {
		differ = "These two voices sound quite different — one is much lower. "
	}
	if e.Reason == "same_name_in_clip" {
		return fmt.Sprintf("%sBoth voices in this clip will be %s — treat them as one person?", differ, e.Name)
	}
	return fmt.Sprintf("%sIs this really %s?", differ, e.Name)
}

// CloseName refuses a new name that is very close to someone already named
// (a slip, not a new person) unless a new person is asked for explicitly.
type CloseName struct {
	Name    string
	Suggest []names.Candidate
}

func (e *CloseName) Error() string {
	return fmt.Sprintf("No one called “%s” — did you mean %s?", e.Name, names.Quote(e.Suggest))
}

// NameInput names one voice of an asset.
type NameInput struct {
	AssetID, Label, Name, PersonID string
	NewPerson                      bool // create a new person even if someone has this name
	// Confirm: the person confirmed that the voices are one person (another
	// voice in the clip has the name, or the voices sound clearly different).
	Confirm bool
}

// NameResult is what naming changed.
type NameResult struct {
	Voice      models.AssetSpeaker `json:"voice"`
	Person     models.Person       `json:"person"`
	CreatedNew bool                `json:"createdPerson"`
	// MergedWith is set when this voice had the same person as another voice
	// of the video: the two are now one. Dropped is the voice that was folded
	// in (undo with Unmerge).
	MergedWith string `json:"mergedWith,omitempty"`
	Dropped    string `json:"dropped,omitempty"`
	// Recognized lists other videos where voices were labeled automatically
	// because this person's voice is now known.
	Recognized []string       `json:"recognized"`
	Library    *LibraryChange `json:"-"`
}

// Name names a voice: links it to a person (found by name, or new), merges it
// with another voice of the same video that already is that person (after the
// person confirmed it), rebuilds the voice profiles involved from confirmed
// voices only, updates transcripts and to-dos, and re-scores the library.
func Name(ctx context.Context, a *app.App, in NameInput) (*NameResult, error) {
	voices, err := Voices(ctx, a, in.AssetID, false)
	if err != nil {
		return nil, err
	}
	target, err := ResolveLabel(voices, in.Label)
	if err != nil {
		return nil, err
	}
	person, err := findPerson(ctx, a, in.Name, in.PersonID, in.NewPerson)
	if err != nil {
		return nil, err
	}
	if person != nil && !in.Confirm && !(target.PersonID == person.ID && target.Source == models.SpeakerByPerson) {
		if c := confirmNeeded(ctx, a, *target, voices, person); c != nil {
			return nil, c
		}
	}
	created := false
	if person == nil {
		if person, err = createPerson(ctx, a, in.Name); err != nil {
			return nil, err
		}
		created = true
	}
	res := &NameResult{CreatedNew: created, Recognized: []string{}}
	keep, drop := *target, (*models.AssetSpeaker)(nil)
	for i := range voices {
		o := &voices[i]
		if o.ID != target.ID && o.PersonID == person.ID {
			// Same person twice in one video: one voice, not two (the person confirmed it).
			if o.First <= target.First {
				keep, drop = *o, target
			} else {
				keep, drop = *target, o
			}
			res.MergedWith = o.Label
			break
		}
	}
	ch := newChanges()
	ch.Asset(in.AssetID)
	ch.Person(person.ID, keep.PersonID)
	if drop != nil {
		ch.Person(drop.PersonID)
		if err := mergeVoice(ctx, a, &keep, drop, models.SpeakerByPerson); err != nil {
			return nil, err
		}
		res.Dropped = drop.Label
	}
	keep.Name, keep.PersonID, keep.Source, keep.Score, keep.Suggestions, keep.UpdatedAt = person.Name, person.ID, models.SpeakerByPerson, 0, nil, time.Now().UTC()
	keep.Naming = naming.Record(ctx, a.Actor) // who named it: only a person's name counts for consent (D274)
	keep.Rejected = without(keep.Rejected, person.ID)
	if err := saveVoice(ctx, a, keep); err != nil {
		return nil, err
	}
	lib, err := ch.Apply(ctx, a)
	if err != nil {
		return nil, err
	}
	res.Library = lib
	for _, id := range lib.Recognized[person.ID] {
		if id != in.AssetID {
			res.Recognized = append(res.Recognized, id)
		}
	}
	a.Audit(ctx, "speakers.name", "asset", in.AssetID, "", map[string]any{"label": keep.Label, "personId": person.ID, "mergedWith": res.MergedWith, "createdPerson": created})
	var p models.Person
	_ = a.DB.GetByID(ctx, store.CPeople, a.WorkspaceID(), person.ID, &p)
	res.Person = p
	vs, _ := Voices(ctx, a, in.AssetID, false)
	for _, v := range vs {
		if v.Label == keep.Label {
			res.Voice = v
		}
	}
	return res, nil
}

// confirmNeeded checks naming target as person: another voice of the clip
// already is that person, or the voice's pitch is clearly unlike the person's.
func confirmNeeded(ctx context.Context, a *app.App, target models.AssetSpeaker, voices []models.AssetSpeaker, person *models.Person) *ConfirmNeeded {
	for _, o := range voices {
		if o.ID != target.ID && o.PersonID == person.ID {
			pair := EnsurePitch(ctx, a, []models.AssetSpeaker{target, o}, 1)
			c := &ConfirmNeeded{Reason: "same_name_in_clip", Name: person.Name, Other: o.DisplayName(), Pitch: pair[0].Pitch, OtherPitch: pair[1].Pitch,
				PersonID: person.ID, AssetID: target.AssetID, Speaker: target.Label, OtherSpeaker: o.Label}
			c.Differ = voice.PitchDiffers(pitchOf(pair[0]), pitchOf(pair[1]))
			return c
		}
	}
	others, err := confirmedVoices(ctx, a, person.ID)
	if err != nil || len(others) == 0 {
		return nil
	}
	var rest []models.AssetSpeaker
	for _, o := range others {
		if o.ID != target.ID {
			rest = append(rest, o)
		}
	}
	if len(rest) == 0 {
		return nil
	}
	all := EnsurePitch(ctx, a, append([]models.AssetSpeaker{target}, rest...), 3)
	pp := personPitch(all[1:])
	if voice.PitchDiffers(pitchOf(all[0]), pp) {
		return &ConfirmNeeded{Reason: "voices_differ", Name: person.Name, Differ: true, Pitch: all[0].Pitch, OtherPitch: pp.Hz,
			PersonID: person.ID, AssetID: target.AssetID, Speaker: target.Label}
	}
	return nil
}

func pitchOf(v models.AssetSpeaker) voice.Pitch { return voice.Pitch{Hz: v.Pitch, Voiced: v.Voiced} }

// snapshot is what a merge may change on the kept voice.
func snapshot(v models.AssetSpeaker) *models.SpeakerBefore {
	return &models.SpeakerBefore{Name: v.Name, PersonID: v.PersonID, Source: v.Source, Score: v.Score, Suggestions: v.Suggestions, Rejected: v.Rejected,
		Seconds: v.Seconds, Fingerprinted: v.Fingerprinted, Turns: v.Turns, Words: v.Words, First: v.First, Minor: v.Minor,
		Samples: append([]models.TimeSpan{}, v.Samples...), Confirmed: v.Confirmed, Pitch: v.Pitch, Voiced: v.Voiced}
}

// mergeVoice folds drop into keep: keep's counts and samples grow (its
// fingerprint stays its own: the profile reads both voices), drop remembers
// what keep looked like before so the merge can be undone, and its words are
// relabeled with their origin kept (SyncNames).
func mergeVoice(ctx context.Context, a *app.App, keep, drop *models.AssetSpeaker, by string) error {
	before := snapshot(*keep)
	*keep = combine(*keep, *drop)
	now := time.Now().UTC()
	drop.MergedInto, drop.MergedBy, drop.Before = keep.Label, by, before
	return a.DB.Update(ctx, store.CSpeakers, a.WorkspaceID(), drop.ID, bson.M{"$set": bson.M{"mergedInto": keep.Label, "mergedBy": by, "before": before, "updatedAt": now}})
}

// Reject records that a voice is not a given person (a person said "no"). A
// voice that was named that person is unnamed again (and asked about), and the
// person's voice profile no longer uses it.
func Reject(ctx context.Context, a *app.App, assetID, label, personID string) (*LibraryChange, error) {
	voices, err := Voices(ctx, a, assetID, false)
	if err != nil {
		return nil, err
	}
	v, err := ResolveLabel(voices, label)
	if err != nil {
		return nil, err
	}
	ch := newChanges()
	ch.Asset(assetID)
	set := bson.M{"rejected": append(without(v.Rejected, personID), personID), "updatedAt": time.Now().UTC()}
	var keep []models.SpeakerMatch
	for _, m := range v.Suggestions {
		if m.PersonID != personID {
			keep = append(keep, m)
		}
	}
	set["suggestions"] = keep
	if v.PersonID == personID {
		ch.Person(personID)
		set["name"], set["personId"], set["source"], set["score"] = "", "", "", 0.0
		// Voices merged into this one only because both sounded like that person come apart again.
		if err := unmergeAuto(ctx, a, assetID, v.Label); err != nil {
			return nil, err
		}
	}
	if err := a.DB.Update(ctx, store.CSpeakers, a.WorkspaceID(), v.ID, bson.M{"$set": set}); err != nil {
		return nil, err
	}
	return ch.Apply(ctx, a)
}

// people lists the workspace's named people as name candidates.
func candidates(ctx context.Context, a *app.App) ([]names.Candidate, map[string]models.Person, error) {
	var ps []models.Person
	if err := a.DB.Find(ctx, store.CPeople, a.WorkspaceID(), bson.M{"mergedInto": bson.M{"$in": bson.A{nil, ""}}}, &ps); err != nil {
		return nil, nil, err
	}
	out := make([]names.Candidate, 0, len(ps))
	byID := map[string]models.Person{}
	for _, p := range ps {
		out = append(out, names.Candidate{ID: p.ID, Name: p.Name, Weight: float64(p.Recordings)})
		byID[p.ID] = p
	}
	return out, byID, nil
}

// findPerson is the person a name means when naming a voice: the given ID, an
// existing person with exactly that name (case and accents ignored), or nil
// for a new person. A name that is only close to someone ("Mya" when
// "Maya" exists) is refused unless a new person is asked for.
func findPerson(ctx context.Context, a *app.App, name, personID string, newPerson bool) (*models.Person, error) {
	if personID != "" {
		return loadPerson(ctx, a, personID)
	}
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return nil, errors.New("a name is required")
	}
	if len([]rune(name)) > 80 {
		return nil, errors.New("names are at most 80 characters")
	}
	if newPerson {
		return nil, nil
	}
	cands, byID, err := candidates(ctx, a)
	if err != nil {
		return nil, err
	}
	var exact []models.Person
	for _, c := range cands {
		if names.Fold(c.Name) == names.Fold(name) {
			exact = append(exact, byID[c.ID])
		}
	}
	if len(exact) > 0 {
		sort.SliceStable(exact, func(i, j int) bool { return exact[i].Recordings > exact[j].Recordings })
		return &exact[0], nil
	}
	if close := names.Close(name, cands); len(close) > 0 {
		return nil, &CloseName{Name: name, Suggest: close}
	}
	return nil, nil
}

func createPerson(ctx context.Context, a *app.App, name string) (*models.Person, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return nil, errors.New("a name is required")
	}
	now := time.Now().UTC()
	p := models.Person{ID: ids.New(ids.Person), WorkspaceID: a.WorkspaceID(), Name: name, NameKey: NameKey(name), Model: voice.ModelName, CreatedAt: now, CreatedBy: a.Actor, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CPeople, p); err != nil {
		return nil, err
	}
	a.Audit(ctx, "people.create", "person", p.ID, "", map[string]any{"name": name})
	return &p, nil
}

// PersonFor finds the person a name means (an ID, or exactly that name, case
// and accents ignored) or adds someone new; a name only close to someone's is
// refused with CloseName unless newPerson is set. It reports whether the person is new.
func PersonFor(ctx context.Context, a *app.App, name, personID string, newPerson bool) (*models.Person, bool, error) {
	return personFor(ctx, a, name, personID, newPerson)
}

// personFor finds or creates the person a name means (see findPerson).
func personFor(ctx context.Context, a *app.App, name, personID string, newPerson bool) (*models.Person, bool, error) {
	p, err := findPerson(ctx, a, name, personID, newPerson)
	if err != nil || p != nil {
		return p, false, err
	}
	p, err = createPerson(ctx, a, name)
	return p, err == nil, err
}

// loadPerson follows merges to the person that remains.
func loadPerson(ctx context.Context, a *app.App, id string) (*models.Person, error) {
	for i := 0; i < 16; i++ {
		var p models.Person
		if err := a.DB.GetByID(ctx, store.CPeople, a.WorkspaceID(), id, &p); err != nil {
			return nil, err
		}
		if p.MergedInto == "" {
			return &p, nil
		}
		id = p.MergedInto
	}
	return nil, errors.New("person merge chain too long")
}

func sourceOf(v models.AssetSpeaker) string { return v.AssetID + "/" + v.Label }

func clusterOf(v models.AssetSpeaker) voice.Cluster {
	c := voice.Cluster{Label: v.Label, Seconds: v.Seconds, Embedded: v.Fingerprinted, Turns: v.Turns, First: v.First, Centroid: v.Centroid, Minor: v.Minor}
	for i, x := range v.Exemplars {
		sec := 2.0
		if i < len(v.ExSeconds) {
			sec = v.ExSeconds[i]
		}
		c.Exemplars = append(c.Exemplars, voice.Exemplar{Span: voice.Span{From: 0, To: sec}, Vec: x})
	}
	return c
}

// combine merges drop's speech into keep for display: counts, first
// appearance and listening samples. Fingerprints stay with each voice.
func combine(keep, drop models.AssetSpeaker) models.AssetSpeaker {
	keep.Seconds = round3(keep.Seconds + drop.Seconds)
	keep.Fingerprinted = round3(keep.Fingerprinted + drop.Fingerprinted)
	keep.Turns += drop.Turns
	keep.Words += drop.Words
	if drop.First < keep.First {
		keep.First = drop.First
	}
	keep.Minor = keep.Minor && drop.Minor
	samples := append(append([]models.TimeSpan(nil), keep.Samples...), drop.Samples...)
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].To-samples[i].From > samples[j].To-samples[j].From })
	if len(samples) > 3 {
		samples = samples[:3]
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].From < samples[j].From })
	keep.Samples = samples
	for _, s := range drop.Confirmed {
		keep.Confirmed = addSpan(keep.Confirmed, s)
	}
	return keep
}

func saveVoice(ctx context.Context, a *app.App, v models.AssetSpeaker) error {
	_, err := a.DB.C(store.CSpeakers).ReplaceOne(ctx, bson.M{"_id": v.ID, "workspaceId": a.WorkspaceID()}, v)
	return err
}

func saveProfile(ctx context.Context, a *app.App, p *models.Person, pr voice.Profile) error {
	ex := make([]models.VoiceExemplar, 0, len(pr.Exemplars))
	for _, e := range pr.Exemplars {
		ex = append(ex, models.VoiceExemplar{Source: e.Source, Vec: e.Vec, Seconds: e.Seconds})
	}
	recs, secs, err := appearances(ctx, a, p.ID)
	if err != nil {
		return err
	}
	set := bson.M{"exemplars": ex, "centroid": pr.Centroid, "model": voice.ModelName, "recordings": recs, "seconds": secs, "updatedAt": time.Now().UTC()}
	if len(ex) == 0 {
		set["centroid"] = nil
	}
	return a.DB.Update(ctx, store.CPeople, a.WorkspaceID(), p.ID, bson.M{"$set": set})
}

func appearances(ctx context.Context, a *app.App, personID string) (int, float64, error) {
	var vs []models.AssetSpeaker
	if err := a.DB.Find(ctx, store.CSpeakers, a.WorkspaceID(), bson.M{"personId": personID, "mergedInto": bson.M{"$in": bson.A{nil, ""}}}, &vs); err != nil {
		return 0, 0, err
	}
	assets := map[string]bool{}
	secs := 0.0
	for _, v := range vs {
		assets[v.AssetID] = true
		secs += v.Seconds
	}
	return len(assets), round3(secs), nil
}

// refreshStatus recounts an asset's named voices.
func refreshStatus(ctx context.Context, a *app.App, assetID string) error {
	vs, err := Voices(ctx, a, assetID, false)
	if err != nil {
		return err
	}
	count, minor, named := 0, 0, 0
	for _, v := range vs {
		if v.Minor && v.Name == "" {
			minor++
		} else {
			count++
		}
		if v.Name != "" {
			named++
		}
	}
	return a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), assetID, bson.M{"$set": bson.M{"speakers.count": count, "speakers.minor": minor, "speakers.named": named,
		"transcript.speakers": len(vs), "speakers.updatedAt": time.Now().UTC(), "updatedAt": time.Now().UTC()}})
}

// LibraryChange is what re-scoring the library changed.
type LibraryChange struct {
	// Assets are the clips whose voices changed.
	Assets []string `json:"assets"`
	// Recognized: person → clips where their voice was newly labeled automatically.
	Recognized map[string][]string `json:"recognized"`
	// Dropped are automatic labels that no longer hold (they are questions again).
	Dropped []ClipVoice `json:"dropped"`
	// Merged are voices combined because both were recognized as the same person.
	Merged []AutoMerge `json:"merged"`
	// People whose voices moved.
	People []string `json:"people"`
}

// AutoMerge is two voices of one clip combined because both were recognized as one person.
type AutoMerge struct {
	AssetID    string `json:"assetId"`
	AssetTitle string `json:"assetTitle"`
	Kept       string `json:"kept"`
	Dropped    string `json:"dropped"`
	Name       string `json:"name"`
}

// RefreshLibrary re-scores every voice that no person named — unnamed ones
// and automatic labels — against the people's current profiles: confident
// matches are labeled (and merged with another voice of the same person in
// that video), automatic labels that no longer hold become questions again,
// and the rest get suggestions.
func RefreshLibrary(ctx context.Context, a *app.App) (*LibraryChange, error) {
	out := &LibraryChange{Assets: []string{}, Recognized: map[string][]string{}, Dropped: []ClipVoice{}, Merged: []AutoMerge{}, People: []string{}}
	people, err := LoadProfiles(ctx, a)
	if err != nil {
		return nil, err
	}
	var vs []models.AssetSpeaker
	if err := a.DB.Find(ctx, store.CSpeakers, a.WorkspaceID(), bson.M{"source": bson.M{"$in": bson.A{nil, "", models.SpeakerByAuto}}, "mergedInto": bson.M{"$in": bson.A{nil, ""}},
		"model": voice.ModelName}, &vs); err != nil {
		return nil, err
	}
	changed := map[string]bool{}
	movedPeople := map[string]bool{}
	titles := map[string]string{}
	title := func(id string) string {
		if t, ok := titles[id]; ok {
			return t
		}
		var as models.Asset
		if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), id, &as) == nil {
			titles[id] = as.DisplayTitle()
		}
		return titles[id]
	}
	for _, stale := range vs {
		var v models.AssetSpeaker
		if a.DB.GetByID(ctx, store.CSpeakers, a.WorkspaceID(), stale.ID, &v) != nil || v.MergedInto != "" || v.Source == models.SpeakerByPerson {
			continue // merged or named meanwhile
		}
		allowed := make([]voice.Profile, 0, len(people))
		for _, p := range people {
			if !contains(v.Rejected, p.ID) {
				allowed = append(allowed, p)
			}
		}
		d := voice.Decide(clusterOf(v), allowed, voice.DefaultThresholds)
		var sugg []models.SpeakerMatch
		for _, m := range d.Suggestions {
			sugg = append(sugg, models.SpeakerMatch{PersonID: m.PersonID, Name: m.Name, Score: m.Score})
		}
		switch {
		case v.Source == models.SpeakerByAuto && d.Auto != nil && d.Auto.PersonID == v.PersonID:
			if v.Score != d.Auto.Score || v.Name != d.Auto.Name {
				_ = a.DB.Update(ctx, store.CSpeakers, a.WorkspaceID(), v.ID, bson.M{"$set": bson.M{"score": d.Auto.Score, "name": d.Auto.Name, "updatedAt": time.Now().UTC()}})
				changed[v.AssetID] = true
			}
		case d.Auto != nil:
			if v.PersonID != "" {
				movedPeople[v.PersonID] = true
			}
			m, err := autoLabel(ctx, a, v, d.Auto)
			if err != nil {
				return nil, err
			}
			if m != nil {
				m.AssetTitle = title(v.AssetID)
				out.Merged = append(out.Merged, *m)
			}
			changed[v.AssetID] = true
			movedPeople[d.Auto.PersonID] = true
			if !containsStr(out.Recognized[d.Auto.PersonID], v.AssetID) {
				out.Recognized[d.Auto.PersonID] = append(out.Recognized[d.Auto.PersonID], v.AssetID)
			}
		case v.Source == models.SpeakerByAuto:
			// The automatic label no longer holds: ask instead.
			movedPeople[v.PersonID] = true
			if err := unmergeAuto(ctx, a, v.AssetID, v.Label); err != nil {
				return nil, err
			}
			out.Dropped = append(out.Dropped, ClipVoice{AssetID: v.AssetID, AssetTitle: title(v.AssetID), Label: v.Label, Speaker: v.Name})
			if err := a.DB.Update(ctx, store.CSpeakers, a.WorkspaceID(), v.ID, bson.M{"$set": bson.M{"name": "", "personId": "", "source": "", "score": 0.0,
				"suggestions": sugg, "updatedAt": time.Now().UTC()}}); err != nil {
				return nil, err
			}
			changed[v.AssetID] = true
		default:
			if !sameMatches(v.Suggestions, sugg) {
				if err := a.DB.Update(ctx, store.CSpeakers, a.WorkspaceID(), v.ID, bson.M{"$set": bson.M{"suggestions": sugg, "updatedAt": time.Now().UTC()}}); err != nil {
					return nil, err
				}
				changed[v.AssetID] = true
			}
		}
	}
	for id := range changed {
		out.Assets = append(out.Assets, id)
		if err := SyncNames(ctx, a, id); err != nil {
			return nil, err
		}
		_ = refreshStatus(ctx, a, id)
		if err := Reconcile(ctx, a, id); err != nil {
			return nil, err
		}
	}
	for id := range movedPeople {
		if id != "" {
			out.People = append(out.People, id)
			_ = refreshTotals(ctx, a, id)
		}
	}
	sort.Strings(out.Assets)
	sort.Strings(out.People)
	for k := range out.Recognized {
		sort.Strings(out.Recognized[k])
	}
	return out, nil
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// refreshTotals recounts where a person speaks (recordings and seconds).
func refreshTotals(ctx context.Context, a *app.App, personID string) error {
	recs, secs, err := appearances(ctx, a, personID)
	if err != nil {
		return err
	}
	return a.DB.Update(ctx, store.CPeople, a.WorkspaceID(), personID, bson.M{"$set": bson.M{"recordings": recs, "seconds": secs, "updatedAt": time.Now().UTC()}})
}

// autoLabel names a voice after a confident match. When another voice of the
// same video already is that person, the two are combined (and the merge is
// reported so a person can undo it).
func autoLabel(ctx context.Context, a *app.App, v models.AssetSpeaker, m *voice.Match) (*AutoMerge, error) {
	defer func() { _ = refreshTotals(ctx, a, m.PersonID) }()
	voices, err := Voices(ctx, a, v.AssetID, false)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for _, o := range voices {
		if o.ID != v.ID && o.PersonID == m.PersonID {
			keep, drop := o, v
			// A voice a person named always stays the one that is kept, so its
			// fingerprint (what the profile learns from) is never replaced by
			// one that was only recognized. Between two recognized voices, the
			// earlier one stays.
			swap := v.First < o.First && o.Source != models.SpeakerByPerson
			if swap {
				keep, drop = v, o
			}
			if err := mergeVoice(ctx, a, &keep, &drop, models.SpeakerByAuto); err != nil {
				return nil, err
			}
			if swap {
				keep.Name, keep.PersonID, keep.Source, keep.Score = o.Name, o.PersonID, models.SpeakerByAuto, m.Score
			}
			keep.Suggestions, keep.UpdatedAt = nil, now
			if err := saveVoice(ctx, a, keep); err != nil {
				return nil, err
			}
			return &AutoMerge{AssetID: v.AssetID, Kept: keep.Label, Dropped: drop.Label, Name: m.Name}, nil
		}
	}
	return nil, a.DB.Update(ctx, store.CSpeakers, a.WorkspaceID(), v.ID, bson.M{"$set": bson.M{"name": m.Name, "personId": m.PersonID, "source": models.SpeakerByAuto,
		"score": m.Score, "suggestions": nil, "minor": false, "updatedAt": now}})
}

func sameMatches(a, b []models.SpeakerMatch) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].PersonID != b[i].PersonID || a[i].Score != b[i].Score {
			return false
		}
	}
	return true
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func without(xs []string, s string) []string {
	var out []string
	for _, x := range xs {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}
