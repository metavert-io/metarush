package speakers

import (
	"context"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/voice"
)

// A person's voice profile is derived data: it is rebuilt from the voices a
// person confirmed are them (named, confirmed a suggestion, answered a
// question, fixed who said a line), never patched. So a wrong name that is
// later corrected — renamed, separated, rejected, its lines given to someone
// else, or found again by a new detection run — leaves nothing behind, and
// voices recognized automatically never shape it.

// ConflictThreshold: two confirmed voices of one person less alike than this
// are probably two different people (the same person scores 0.7–0.97 across
// recordings, different people 0.1–0.35; docs/speakers.md).
const ConflictThreshold = 0.30

// ProfileReport is what rebuilding a person's voice used.
type ProfileReport struct {
	Voices    int `json:"voices" jsonschema:"description=Confirmed voices the profile was built from."`
	Clips     int `json:"clips"`
	Exemplars int `json:"exemplars"`
	// Conflicts are confirmed voices that sound like someone else than the
	// person's other voices (probably a wrong name to fix).
	Conflicts []ClipVoice `json:"conflicts"`
}

// ClipVoice points at one voice of one clip.
type ClipVoice struct {
	AssetID    string  `json:"assetId"`
	AssetTitle string  `json:"assetTitle"`
	Label      string  `json:"label"`
	Speaker    string  `json:"speaker" jsonschema:"description=How the voice is shown (its name or Speaker N)."`
	Score      float64 `json:"score,omitempty" jsonschema:"description=How alike it sounds to the person's other voices."`
	// Reason for a conflict: "combined" (two voices that sound like different
	// people were combined into this one) or "unlike" (it sounds unlike the
	// person's other voices).
	Reason string `json:"reason,omitempty" jsonschema:"enum=combined,enum=unlike"`
}

// confirmedVoices are the voices a person confirmed are personID: named by a
// person, plus voices a person merged into them.
func confirmedVoices(ctx context.Context, a *app.App, personID string) ([]models.AssetSpeaker, error) {
	var keep []models.AssetSpeaker
	if err := a.DB.Find(ctx, store.CSpeakers, a.WorkspaceID(), bson.M{"personId": personID, "source": models.SpeakerByPerson,
		"mergedInto": bson.M{"$in": bson.A{nil, ""}}}, &keep); err != nil {
		return nil, err
	}
	out := append([]models.AssetSpeaker(nil), keep...)
	for _, k := range keep {
		var merged []models.AssetSpeaker
		if err := a.DB.Find(ctx, store.CSpeakers, a.WorkspaceID(), bson.M{"assetId": k.AssetID, "mergedBy": models.SpeakerByPerson,
			"mergedInto": bson.M{"$nin": bson.A{nil, ""}}}, &merged); err != nil {
			return nil, err
		}
		byLabel := map[string]models.AssetSpeaker{}
		for _, m := range merged {
			byLabel[m.Label] = m
		}
		for _, m := range merged {
			if root(withKeep(byLabel, k), m.Label) == k.Label {
				out = append(out, m)
			}
		}
	}
	return out, nil
}

func withKeep(m map[string]models.AssetSpeaker, k models.AssetSpeaker) map[string]models.AssetSpeaker {
	out := make(map[string]models.AssetSpeaker, len(m)+1)
	for l, v := range m {
		out[l] = v
	}
	out[k.Label] = k
	return out
}

// RebuildProfile recomputes a person's voice profile from the voices a person
// confirmed (at most voice.MaxExemplarsPerSource exemplars per voice, the
// newest voice.MaxExemplars in total) and their totals, and re-asks or closes
// "same person?" questions against the new profile.
func RebuildProfile(ctx context.Context, a *app.App, personID string) (*ProfileReport, error) {
	p, err := loadPerson(ctx, a, personID)
	if err != nil {
		return nil, err
	}
	vs, err := confirmedVoices(ctx, a, p.ID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].UpdatedAt.Before(vs[j].UpdatedAt) })
	rep := &ProfileReport{Conflicts: []ClipVoice{}}
	clips := map[string]bool{}
	var pr voice.Profile
	pr.ID, pr.Name = p.ID, p.Name
	for _, v := range vs {
		if len(v.Exemplars) == 0 || v.Model != voice.ModelName {
			continue
		}
		rep.Voices++
		clips[v.AssetID] = true
		for i, x := range v.Exemplars {
			if i >= voice.MaxExemplarsPerSource {
				break
			}
			sec := 2.0
			if i < len(v.ExSeconds) {
				sec = v.ExSeconds[i]
			}
			pr.Exemplars = append(pr.Exemplars, voice.ProfileExemplar{Source: sourceOf(v), Vec: x, Seconds: round3(sec)})
		}
	}
	if len(pr.Exemplars) > voice.MaxExemplars {
		pr.Exemplars = pr.Exemplars[len(pr.Exemplars)-voice.MaxExemplars:]
	}
	pr = pr.Forget("") // recompute the centroid
	rep.Clips, rep.Exemplars = len(clips), len(pr.Exemplars)
	if err := saveProfile(ctx, a, p, pr); err != nil {
		return nil, err
	}
	titles := assetTitles(ctx, a, vs)
	// A confirmed voice that sounds unlike the rest is probably a wrong name.
	if rep.Voices >= 2 {
		for _, v := range vs {
			if len(v.Centroid) == 0 {
				continue
			}
			rest := pr.Forget(sourceOf(v))
			if len(rest.Exemplars) == 0 {
				continue
			}
			if s := voice.Score(voice.Cluster{Centroid: v.Centroid}, rest).Score; s < ConflictThreshold {
				rep.Conflicts = append(rep.Conflicts, ClipVoice{AssetID: v.AssetID, AssetTitle: titles[v.AssetID], Label: v.Label, Speaker: v.DisplayName(), Score: s, Reason: "unlike"})
			}
		}
	}
	// Two voices that sound like different people combined into one of theirs.
	for _, v := range vs {
		if v.MergedInto != "" {
			continue
		}
		if c := combinedConflict(ctx, a, v); c != nil {
			c.AssetTitle = titles[v.AssetID]
			rep.Conflicts = append(rep.Conflicts, *c)
		}
	}
	if err := ReconcileDuplicates(ctx, a, p.ID); err != nil {
		return nil, err
	}
	return rep, nil
}

// combinedConflict reports a voice whose combined parts sound like different
// people (one wrongly given the other's name, then merged).
func combinedConflict(ctx context.Context, a *app.App, keep models.AssetSpeaker) *ClipVoice {
	var drops []models.AssetSpeaker
	if err := a.DB.Find(ctx, store.CSpeakers, a.WorkspaceID(), bson.M{"assetId": keep.AssetID, "mergedInto": keep.Label}, &drops); err != nil || len(drops) == 0 {
		return nil
	}
	own := keep.Exemplars
	for _, d := range drops {
		own, _ = withoutVecs(own, nil, d.Exemplars)
	}
	if len(own) == 0 {
		return nil
	}
	kc := voice.Mean(own, nil)
	for _, d := range drops {
		if len(d.Centroid) == 0 {
			continue
		}
		s := voice.Cosine(kc, d.Centroid)
		differ := voice.PitchDiffers(pitchOf(keep), pitchOf(d))
		if s < ConflictThreshold || differ {
			return &ClipVoice{AssetID: keep.AssetID, Label: keep.Label, Speaker: keep.DisplayName(), Score: round3(s), Reason: "combined"}
		}
	}
	return nil
}

// MergeWarning says, for a voice others were combined into, whether its parts
// sound like different people ("" when they sound alike or cannot be told).
func MergeWarning(ctx context.Context, a *app.App, keep models.AssetSpeaker) string {
	if c := combinedConflict(ctx, a, keep); c != nil {
		return "These voices sound like different people."
	}
	return ""
}

func assetTitles(ctx context.Context, a *app.App, vs []models.AssetSpeaker) map[string]string {
	ids := []string{}
	for _, v := range vs {
		ids = append(ids, v.AssetID)
	}
	var as []models.Asset
	_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}}, &as)
	out := map[string]string{}
	for _, x := range as {
		out[x.ID] = x.DisplayTitle()
	}
	return out
}

// Changes collects what a correction changed so the profiles, the library and
// the questions are brought up to date once at the end.
type Changes struct {
	people map[string]bool
	assets map[string]bool
}

func newChanges() *Changes { return &Changes{people: map[string]bool{}, assets: map[string]bool{}} }

// Person marks a person whose confirmed voices changed.
func (c *Changes) Person(ids ...string) {
	for _, id := range ids {
		if id != "" {
			c.people[id] = true
		}
	}
}

// Asset marks a clip whose voices changed.
func (c *Changes) Asset(ids ...string) {
	for _, id := range ids {
		if id != "" {
			c.assets[id] = true
		}
	}
}

// Apply rebuilds the changed people's profiles, re-scores every automatic
// label and suggestion in the library against them, refreshes the changed
// clips (names in transcripts, counts, questions) and returns what the library
// refresh changed.
func (c *Changes) Apply(ctx context.Context, a *app.App) (*LibraryChange, error) {
	ps := sortedKeys(c.people)
	for _, id := range ps {
		if _, err := RebuildProfile(ctx, a, id); err != nil && err != store.ErrNotFound {
			return nil, err
		}
	}
	for _, id := range sortedKeys(c.assets) {
		if err := SyncNames(ctx, a, id); err != nil {
			return nil, err
		}
		_ = refreshStatus(ctx, a, id)
		if err := Reconcile(ctx, a, id); err != nil {
			return nil, err
		}
	}
	lib, err := RefreshLibrary(ctx, a)
	if err != nil {
		return nil, err
	}
	// Totals of everyone whose voices moved, and their "same person?" questions.
	for _, id := range ps {
		_ = refreshTotals(ctx, a, id)
	}
	for _, id := range lib.People {
		_ = refreshTotals(ctx, a, id)
	}
	return lib, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// RebuildAll recomputes every named person's profile (used once after an
// upgrade, and by `people rebuild --all`).
func RebuildAll(ctx context.Context, a *app.App) (int, error) {
	var ps []models.Person
	if err := a.DB.Find(ctx, store.CPeople, a.WorkspaceID(), bson.M{"mergedInto": bson.M{"$in": bson.A{nil, ""}}}, &ps); err != nil {
		return 0, err
	}
	c := newChanges()
	for _, p := range ps {
		c.Person(p.ID)
	}
	if _, err := c.Apply(ctx, a); err != nil {
		return 0, err
	}
	return len(ps), nil
}

// RebuildPerson rebuilds one person's voice from the clips a person confirmed
// and re-scores every automatic label and suggestion against it (wrong
// automatic labels become questions again; "same person?" questions that no
// longer hold close themselves).
func RebuildPerson(ctx context.Context, a *app.App, personID string) (*LibraryChange, *ProfileReport, error) {
	p, err := loadPerson(ctx, a, personID)
	if err != nil {
		return nil, nil, err
	}
	rep, err := RebuildProfile(ctx, a, p.ID)
	if err != nil {
		return nil, nil, err
	}
	lib, err := RefreshLibrary(ctx, a)
	if err != nil {
		return nil, nil, err
	}
	_ = refreshTotals(ctx, a, p.ID)
	return lib, rep, nil
}

// MeasuredPitch is a person's typical pitch, measuring a few of their
// confirmed voices first when theirs is not known yet.
func MeasuredPitch(ctx context.Context, a *app.App, personID string) voice.Pitch {
	vs, err := confirmedVoices(ctx, a, personID)
	if err != nil {
		return voice.Pitch{}
	}
	return personPitch(EnsurePitch(ctx, a, vs, 3))
}

// personPitch is a person's typical pitch from their confirmed voices.
func personPitch(vs []models.AssetSpeaker) voice.Pitch {
	var ps []voice.Pitch
	for _, v := range vs {
		ps = append(ps, voice.Pitch{Hz: v.Pitch, Voiced: v.Voiced})
	}
	return voice.CombinePitch(ps)
}

// PersonPitch is a person's typical pitch (from voices whose pitch is known).
func PersonPitch(ctx context.Context, a *app.App, personID string) voice.Pitch {
	vs, err := confirmedVoices(ctx, a, personID)
	if err != nil {
		return voice.Pitch{}
	}
	return personPitch(vs)
}

// EnsurePitch measures the pitch of voices that have none yet (voices found
// before MetaRush measured pitch), one clip's sound at a time, at most limit
// clips. It is best effort: voices stay unmeasured when the sound is not here.
func EnsurePitch(ctx context.Context, a *app.App, vs []models.AssetSpeaker, limit int) []models.AssetSpeaker {
	byAsset := map[string][]int{}
	var order []string
	for i, v := range vs {
		if v.Pitch > 0 || v.Voiced > 0 {
			continue
		}
		if _, ok := byAsset[v.AssetID]; !ok {
			order = append(order, v.AssetID)
		}
		byAsset[v.AssetID] = append(byAsset[v.AssetID], i)
	}
	for n, assetID := range order {
		if n >= limit {
			break
		}
		var as models.Asset
		if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as) != nil || !as.Media.HasAudio {
			continue
		}
		pcm, err := pcmFor(ctx, a, &as)
		if err != nil {
			continue
		}
		segs, err := loadSegments(ctx, a, assetID)
		if err != nil {
			continue
		}
		runs := runsOf(segs)
		for _, i := range byAsset[assetID] {
			own, _ := spansFor(runs, vs[i])
			pt := voice.MeasurePitch(pcm, own)
			if pt.Voiced == 0 {
				pt.Voiced = 0.001 // measured: no voiced speech (not measured again)
			}
			vs[i].Pitch, vs[i].Voiced = pt.Hz, pt.Voiced
			_ = a.DB.Update(ctx, store.CSpeakers, a.WorkspaceID(), vs[i].ID, bson.M{"$set": bson.M{"pitch": pt.Hz, "voiced": pt.Voiced}})
		}
	}
	return vs
}

func nowUTC() time.Time { return time.Now().UTC() }
