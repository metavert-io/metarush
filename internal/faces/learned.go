package faces

import (
	"context"
	"math"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/naming"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
	"github.com/metavert-io/metarush/internal/visual"
)

// What a person's face is learned from (D158): the faces a person confirmed
// are them — one entry per face named on its own, one per group answer (D155)
// with how many faces it named — with how alike each is to the others and to
// the face in question, the odd one marked, so a wrong one can be taken back
// where "Not Maya?" shows it.

// LearnedFace is one entry a person's face is learned from.
type LearnedFace struct {
	// FaceID is its clearest face (the one to show).
	FaceID     string    `json:"faceId"`
	AssetID    string    `json:"assetId"`
	AssetTitle string    `json:"assetTitle"`
	At         float64   `json:"at"`
	Box        []float64 `json:"box"`
	// Faces is how many faces this answer named (1 for a face named on its own).
	Faces int `json:"faces"`
	// Group is the group answer it comes from ("" for a face named on its own).
	Group string `json:"group,omitempty"`
	// Alike is how alike it looks to the person's other learned faces (0–1;
	// absent without fingerprints or other faces); LikeThis how alike it looks
	// to the face in question.
	Alike    *float64 `json:"alike,omitempty"`
	LikeThis *float64 `json:"likeThis,omitempty"`
	// Closest: of the learned faces, the one the face in question looks most
	// like (why it was named after them).
	Closest bool `json:"closest,omitempty"`
	// Teaches: its fingerprints are used to recognize the person (a face asked
	// about again because it looks unlike the others is not, until answered).
	Teaches bool   `json:"teaches"`
	Odd     bool   `json:"odd,omitempty"`
	Why     string `json:"why,omitempty"`
}

// FaceInQuestion is the face being judged, compared with what the person's
// face is learned from.
type FaceInQuestion struct {
	FaceID  string    `json:"faceId"`
	AssetID string    `json:"assetId"`
	At      float64   `json:"at"`
	Box     []float64 `json:"box"`
	// Score is how alike it looks to the person's learned face (0–1).
	Score float64 `json:"score"`
	// Recognized: MetaRush names it after the person by its looks right now.
	Recognized bool `json:"recognized"`
}

// LearnedFaces lists the faces personID's face is learned from, compared with
// the face in question when one is given, and marks the one least like the
// others.
func LearnedFaces(ctx context.Context, a *app.App, personID, faceID string) ([]LearnedFace, *FaceInQuestion, error) {
	var fs []models.FaceTrack
	if err := a.DB.Find(ctx, store.CFaces, a.WorkspaceID(), bson.M{"personId": personID, "confirmedBy": bson.M{"$ne": nil}}, &fs,
		options.Find().SetSort(bson.D{{Key: "confirmedAt", Value: 1}, {Key: "_id", Value: 1}})); err != nil {
		return nil, nil, err
	}
	var target *models.FaceTrack
	if faceID != "" {
		f, err := Get(ctx, a, faceID)
		if err != nil {
			return nil, nil, err
		}
		target = f
	}
	rec, err := loadRecognizer(ctx, a)
	if err != nil {
		return nil, nil, err
	}
	type entry struct {
		LearnedFace
		vecs  [][]float32
		qs    []float64
		bestQ float64
	}
	var es []*entry
	byGroup := map[string]*entry{}
	for _, f := range fs {
		if target != nil && f.ID == target.ID {
			continue
		}
		var e *entry
		if f.ConfirmedGroup != "" {
			e = byGroup[f.ConfirmedGroup]
		}
		if e == nil {
			e = &entry{LearnedFace: LearnedFace{FaceID: f.ID, AssetID: f.AssetID, At: f.At, Box: f.Box, Group: f.ConfirmedGroup}, bestQ: f.Quality}
			es = append(es, e)
			if f.ConfirmedGroup != "" {
				byGroup[f.ConfirmedGroup] = e
			}
		}
		e.Faces++
		if v := Vector(f); v != nil {
			e.vecs, e.qs = append(e.vecs, v), append(e.qs, f.Quality)
		}
		if _, conflict := rec.conflict[f.ID]; !conflict || f.Checked {
			e.Teaches = e.Teaches || Vector(f) != nil
		}
		if f.Quality > e.bestQ { // the clearest face of a group answer is the one shown
			e.FaceID, e.AssetID, e.At, e.Box, e.bestQ = f.ID, f.AssetID, f.At, f.Box, f.Quality
		}
	}
	var tv []float32
	if target != nil {
		tv = Vector(*target)
	}
	vecOf := make([][]float32, len(es))
	for i, e := range es {
		vecOf[i] = centroid(e.vecs, e.qs)
	}
	// How alike each is to the others (the face in question doesn't count: a
	// person is asking whether it is them), and to the face in question.
	closest, best := -1, math.Inf(-1)
	for i, e := range es {
		if vecOf[i] == nil {
			continue
		}
		sum, n := 0.0, 0
		for j := range es {
			if j != i && vecOf[j] != nil {
				sum += faceid.Cosine(vecOf[i], vecOf[j])
				n++
			}
		}
		if n > 0 {
			s := round3(sum / float64(n))
			e.Alike = &s
		}
		if tv != nil {
			s := round3(faceid.Cosine(vecOf[i], tv))
			e.LikeThis = &s
			if s > best {
				closest, best = i, s
			}
		}
	}
	if closest >= 0 && len(es) > 1 {
		es[closest].Closest = true
	}
	// The odd one: with three or more, the one unlike the rest (as D114 asks
	// again about a confirmed face unlike the person's others).
	odd, worst := -1, math.Inf(1)
	for i, e := range es {
		if e.Alike != nil && *e.Alike < worst {
			odd, worst = i, *e.Alike
		}
	}
	name := ""
	for _, f := range fs {
		name = f.Name
	}
	if len(es) >= 3 && odd >= 0 && worst < ConflictThreshold {
		es[odd].Odd = true
		es[odd].Why = "Looks unlike " + name + "'s other faces."
	}
	titles := map[string]string{}
	var ids []string
	for _, e := range es {
		ids = append(ids, e.AssetID)
	}
	var as []models.Asset
	_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}}, &as, options.Find().SetProjection(bson.M{"name": 1, "title": 1}))
	for _, x := range as {
		titles[x.ID] = x.DisplayTitle()
	}
	out := make([]LearnedFace, 0, len(es))
	for _, e := range es {
		e.AssetTitle = titles[e.AssetID]
		out = append(out, e.LearnedFace)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Closest != out[j].Closest {
			return out[i].Closest
		}
		return out[i].Odd && !out[j].Odd
	})
	var q *FaceInQuestion
	if target != nil {
		q = &FaceInQuestion{FaceID: target.ID, AssetID: target.AssetID, At: target.At, Box: target.Box,
			Recognized: target.Recognized != nil && target.Recognized.PersonID == personID}
		for _, p := range rec.people {
			if p.ID == personID && tv != nil {
				q.Score = faceid.Score(tv, p).Score
			}
		}
	}
	return out, q, nil
}

// TakeBack takes back what a person's face was learned from: a face they
// confirmed, or (for a face named through a group answer) every face that
// answer named. They are not that person (never suggested again); when other is
// given they are that person instead, as the same kind of answer. The library
// is re-scored once. It returns how many faces changed.
func TakeBack(ctx context.Context, a *app.App, personID, faceID string, other *models.Person) (int, error) {
	f, err := Get(ctx, a, faceID)
	if err != nil {
		return 0, err
	}
	if f.PersonID != personID {
		return 0, ErrNotFound
	}
	filter := bson.M{"workspaceId": a.WorkspaceID(), "_id": f.ID}
	if f.ConfirmedGroup != "" {
		filter = bson.M{"workspaceId": a.WorkspaceID(), "personId": personID, "confirmedGroup": f.ConfirmedGroup}
	}
	var fs []models.FaceTrack
	if err := a.DB.Find(ctx, store.CFaces, a.WorkspaceID(), filter, &fs, options.Find().SetProjection(bson.M{"_id": 1, "assetId": 1, "from": 1, "personId": 1})); err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	ids := make([]string, 0, len(fs))
	for _, x := range fs {
		ids = append(ids, x.ID)
	}
	upd := bson.M{"$addToSet": bson.M{"rejected": personID},
		"$unset": bson.M{"personId": "", "name": "", "confirmedBy": "", "confirmedAt": "", "confirmedVia": "", "confirmedGroup": "", "checked": "", "candidate": "", "recognized": "", "naming": ""},
		"$set":   bson.M{"updatedAt": now}}
	if other != nil {
		upd = bson.M{"$addToSet": bson.M{"rejected": personID},
			"$set":   bson.M{"personId": other.ID, "name": other.Name, "confirmedBy": a.Actor, "confirmedAt": now, "naming": naming.Record(ctx, a.Actor), "updatedAt": now},
			"$unset": bson.M{"checked": "", "candidate": "", "recognized": ""}}
	}
	if _, err := a.DB.C(store.CFaces).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "_id": bson.M{"$in": ids}}, upd); err != nil {
		return 0, err
	}
	for _, x := range fs {
		_, _ = todos.Close(ctx, a, recheckKey(models.FaceTrack{AssetID: x.AssetID, From: x.From, PersonID: personID}), models.ResolvedElsewhere, "You said it's someone else.")
	}
	a.Audit(ctx, "footage.faces.takeback", "asset", f.AssetID, "", map[string]any{"personId": personID, "faces": len(ids), "group": f.ConfirmedGroup})
	if _, err := Refresh(ctx, a, f.AssetID); err != nil {
		return len(ids), err
	}
	return len(ids), nil
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// PersonOnScreen is someone whose confirmed faces are alone on screen while a
// voice talks: a suggestion for that voice's question, never its name (D157).
type PersonOnScreen struct {
	PersonID string
	Name     string
	Seconds  float64
}

// OnScreenWhileTalking lists, for each voice of a clip, the people whose faces
// a person confirmed are alone on screen while it talks (at least MinSeconds,
// and at least MinShare of the time it is heard over a lone face), and the
// names each voice says for itself ("I'm Iris").
func OnScreenWhileTalking(ctx context.Context, a *app.App, assetID string) (map[string][]PersonOnScreen, map[string][]SaidName) {
	onScreen := map[string][]PersonOnScreen{}
	said := map[string][]SaidName{}
	var segs []models.TranscriptSegment
	_ = a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), bson.M{"assetId": assetID}, &segs,
		options.Find().SetProjection(bson.M{"from": 1, "to": 1, "speaker": 1, "text": 1}).SetSort(bson.D{{Key: "from", Value: 1}}))
	var vs []models.AssetSpeaker
	_ = a.DB.Find(ctx, store.CSpeakers, a.WorkspaceID(), bson.M{"assetId": assetID, "mergedInto": bson.M{"$in": bson.A{nil, ""}}}, &vs,
		options.Find().SetProjection(bson.M{"label": 1, "minor": 1, "mergedInto": 1}))
	for _, sn := range SaidNames(segs, vs) {
		if sn.Self {
			said[sn.Label] = append(said[sn.Label], sn)
		}
	}
	st := visual.Load(ctx, a.DB, a.WorkspaceID(), assetID)
	if st == nil || len(st.Index.Faces) == 0 {
		return onScreen, said
	}
	tracks, err := Load(ctx, a, assetID)
	if err != nil {
		return onScreen, said
	}
	ties := aloneVoices(st.Index, segs)
	// People whose voice is no evidence here (D189): said to be nowhere on
	// screen in the clip, or two answers here said a face wasn't them.
	off := todos.CueOff(ctx, a, assetID)
	var as models.Asset
	if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as) == nil {
		for _, pid := range as.NotOnScreen {
			off[pid] = true
		}
	}
	byLabel := map[string]map[string]float64{}
	total := map[string]float64{}
	names := map[string]string{}
	for tr, t := range ties {
		f, ok := tracks[tr]
		for l, s := range t.by {
			total[l] += s
			if ok && f.Confirmed() {
				if byLabel[l] == nil {
					byLabel[l] = map[string]float64{}
				}
				byLabel[l][f.PersonID] += s
				names[f.PersonID] = f.Name
			}
		}
	}
	for l, m := range byLabel {
		for pid, s := range m {
			if s >= MinSeconds && s/total[l] >= MinShare && !off[pid] {
				onScreen[l] = append(onScreen[l], PersonOnScreen{PersonID: pid, Name: names[pid], Seconds: s})
			}
		}
		sort.Slice(onScreen[l], func(i, j int) bool { return onScreen[l][i].Seconds > onScreen[l][j].Seconds })
	}
	return onScreen, said
}
