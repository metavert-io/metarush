// Package faces keeps the faces the on-screen index follows through each clip
// (collection faces) and links them to people (D73, D114), the way voices are
// linked (D60):
//
//   - A person names a face, or answers "Who is this?" / "Is this Maya?".
//     MetaRush asks once per person, not per appearance (D155): a clip's
//     unnamed faces are grouped by how they look and by who is speaking while
//     they are on screen, the names the video shows or says are evidence
//     (D156), and only people who speak are asked about, one question per
//     person per clip (D157). One answer names the group.
//   - A face that looks very much like one person's confirmed faces, and
//     clearly unlike everyone else's, is named automatically ("looks like
//     Maya") until a person confirms it or says "Not Maya?".
//   - What a person looks like is learned only from faces a person confirmed,
//     never from automatic names, and every correction re-scores the whole
//     library, so one wrong name cannot spread. A confirmed face that looks
//     unlike that person's other faces is asked about again and does not name
//     other faces until someone answers.
//
// Fingerprints come from internal/faceid (SFace through onnxruntime); they are
// kept with each face and never sent to clients.
package faces

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/naming"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

// The voice cue (D189): a voice is evidence of who a face is only when the
// face is alone on screen while it talks for at least MinSeconds and it is at
// least MinShare of the talking then (a gate, not how sure: it was 1.0 on
// wrong answers too). Measured on answered face questions from real calls: 2–3 s was
// right 34% of the time, 4–5 s 46%, 6–9 s 98%, 10 s or more 100%. Even then
// it is never enough to ask "Is this …?" on its own.
const (
	MinSeconds = models.VoiceCueSeconds
	MinShare   = 0.6
	// SpeaksSeconds: someone talks while the face is alone on screen for at
	// least this long: enough to ask who it is (D157), not to say who.
	SpeaksSeconds = 2.0
)

// Thresholds decide what looks do (measured: docs/visual-index.md, D114).
var Thresholds = faceid.DefaultThresholds

// ConflictThreshold: a confirmed face that scores below this against the
// person's other confirmed faces probably is someone else; it is asked about
// again and does not name other faces until someone answers.
const ConflictThreshold = 0.30

// ErrNotFound is a face that does not exist in this workspace.
var ErrNotFound = errors.New("MetaRush can't find that face")

// KeyPrefix is the to-do group key prefix of a clip's face questions.
func KeyPrefix(assetID string) string { return "face:" + assetID + ":" }

// Key is the to-do group key of "is this face <person>?" (by when the face
// first shows, which stays the same when a clip is indexed again).
func Key(f models.FaceTrack, personID string) string {
	return fmt.Sprintf("%s%.1f:%s", KeyPrefix(f.AssetID), f.From, personID)
}

// recheckKey is the to-do group key of "is this confirmed face really <person>?".
func recheckKey(f models.FaceTrack) string {
	return fmt.Sprintf("%s%.1f:%s:recheck", KeyPrefix(f.AssetID), f.From, f.PersonID)
}

// Load reads a clip's faces by track number.
func Load(ctx context.Context, a *app.App, assetID string) (map[int]models.FaceTrack, error) {
	var fs []models.FaceTrack
	if err := a.DB.Find(ctx, store.CFaces, a.WorkspaceID(), bson.M{"assetId": assetID}, &fs, options.Find().SetSort(bson.D{{Key: "track", Value: 1}})); err != nil {
		return nil, err
	}
	out := make(map[int]models.FaceTrack, len(fs))
	for _, f := range fs {
		out[f.Track] = f
	}
	return out, nil
}

// List reads a clip's faces in time order.
func List(ctx context.Context, a *app.App, assetID string) ([]models.FaceTrack, error) {
	var fs []models.FaceTrack
	if err := a.DB.Find(ctx, store.CFaces, a.WorkspaceID(), bson.M{"assetId": assetID}, &fs, options.Find().SetSort(bson.D{{Key: "from", Value: 1}, {Key: "track", Value: 1}})); err != nil {
		return nil, err
	}
	if fs == nil {
		fs = []models.FaceTrack{}
	}
	return fs, nil
}

// Namer names a clip's tracks for the readout: confirmed names, faces named by
// their looks ("looks like Maya"), and open questions as possible names.
func Namer(tracks map[int]models.FaceTrack) visual.FaceName {
	return func(track int) (string, bool) {
		f, ok := tracks[track]
		switch {
		case !ok:
			return "", false
		case f.Name != "":
			return f.Name, true
		case f.Recognized != nil:
			return "looks like " + f.Recognized.Name, true
		case f.Candidate != nil:
			return f.Candidate.Name, false
		}
		return "", false
	}
}

func overlap(a0, a1, b0, b1 float64) float64 { return math.Max(0, math.Min(a1, b1)-math.Max(a0, b0)) }

func boxIoU(a, b []float64) float64 {
	if len(a) != 4 || len(b) != 4 {
		return 0
	}
	x1, y1 := math.Max(a[0], b[0]), math.Max(a[1], b[1])
	x2, y2 := math.Min(a[0]+a[2], b[0]+b[2]), math.Min(a[1]+a[3], b[1]+b[3])
	inter := math.Max(0, x2-x1) * math.Max(0, y2-y1)
	u := a[2]*a[3] + b[2]*b[3] - inter
	if u <= 0 {
		return 0
	}
	return inter / u
}

func boxSlice(b vision.Box) []float64 { return []float64{b[0], b[1], b[2], b[3]} }

// Save replaces a clip's faces after it was indexed (again). What people said
// about a face (its name, who it is not, a skipped question, the group it was
// asked about in) stays with the new face that covers the same time in the
// same place; names MetaRush gave by looks are worked out again (Candidates).
func Save(ctx context.Context, a *app.App, assetID string, idx *visual.Index) error {
	old, err := List(ctx, a, assetID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	var docs []any
	for _, tr := range idx.Tracks {
		f := models.FaceTrack{ID: ids.New(ids.Face), WorkspaceID: a.WorkspaceID(), AssetID: assetID, Track: tr.ID, From: tr.From, To: tr.To, Samples: tr.Samples,
			At: tr.Best.At, Box: boxSlice(tr.Best.Box), Quality: tr.Best.Quality, CreatedAt: now, UpdatedAt: now}
		for _, p := range tr.Prints {
			f.Prints = append(f.Prints, models.FacePrint{At: p.At, Q: p.Quality, V: p.Vec})
		}
		if len(f.Prints) > 0 {
			f.PrintModel = faceid.ModelName
		}
		best, bestScore := -1, 0.0
		for i, o := range old {
			ov := overlap(f.From, f.To, o.From, o.To) / math.Max(0.001, math.Min(f.To-f.From, o.To-o.From))
			if ov < 0.5 {
				continue
			}
			if sc := ov + boxIoU(boxSlice(tr.Box), o.Box); sc > bestScore && boxIoU(boxSlice(tr.Box), o.Box) >= 0.2 {
				best, bestScore = i, sc
			}
		}
		if best >= 0 {
			o := old[best]
			f.PersonID, f.Name, f.ConfirmedBy, f.ConfirmedAt, f.Rejected, f.Checked, f.Naming = o.PersonID, o.Name, o.ConfirmedBy, o.ConfirmedAt, o.Rejected, o.Checked, o.Naming
			f.Group, f.Skipped, f.ConfirmedVia, f.ConfirmedGroup, f.AsideFrom, f.NotNames = o.Group, o.Skipped, o.ConfirmedVia, o.ConfirmedGroup, o.AsideFrom, o.NotNames
		}
		docs = append(docs, f)
	}
	if _, err := a.DB.C(store.CFaces).DeleteMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": assetID}); err != nil {
		return err
	}
	if len(docs) == 0 {
		return nil
	}
	_, err = a.DB.C(store.CFaces).InsertMany(ctx, docs)
	return err
}

// Candidates works out, for each face of a clip, who it is or might be
// (D114, D155–D157): named by its looks when it looks very much like one
// person's confirmed faces (and the voice doesn't say otherwise); a confirmed
// face that looks unlike the person's other faces is asked about again; the
// rest are grouped per person — by how they look and by who is speaking while
// they are on screen — and each group whose person speaks is asked about once
// ("Who is this?", or "Is this Maya?" with what the voice, the looks and the
// names shown or said in the video suggest). Questions that no longer apply are
// closed. It returns the number of open questions about the clip's faces.
func Candidates(ctx context.Context, a *app.App, assetID string) (int, error) {
	rec, err := loadRecognizer(ctx, a)
	if err != nil {
		return 0, err
	}
	n, _, err := candidatesWith(ctx, a, assetID, rec)
	return n, err
}

func pct(s float64) int { return int(math.Round(s * 100)) }

// cooccurring lists, for each track, the tracks seen in the same frame.
func cooccurring(idx *visual.Index) map[int][]int {
	seen := map[[2]int]bool{}
	out := map[int][]int{}
	for _, f := range idx.Faces {
		for i, a := range f.Tracks {
			for j, b := range f.Tracks {
				if i == j || a == b || seen[[2]int{a, b}] {
					continue
				}
				seen[[2]int{a, b}] = true
				out[a] = append(out[a], b)
			}
		}
	}
	for k := range out {
		sort.Ints(out[k])
	}
	return out
}

func aboutSeconds(s float64) string {
	if s < 1.5 {
		return "about a second"
	}
	return fmt.Sprintf("about %d seconds", int(s+0.5))
}

func sameCandidate(a, b *models.FaceCandidate) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	}
	return *a == *b
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// Get reads one face.
func Get(ctx context.Context, a *app.App, faceID string) (*models.FaceTrack, error) {
	var f models.FaceTrack
	if err := a.DB.GetByID(ctx, store.CFaces, a.WorkspaceID(), faceID, &f); err != nil {
		return nil, ErrNotFound
	}
	return &f, nil
}

// Confirm records that a face is a person (a person said so), closes the
// questions about it and re-scores the library: the person's face is better
// known now.
func Confirm(ctx context.Context, a *app.App, faceID string, person models.Person) (*models.FaceTrack, error) {
	f, err := Get(ctx, a, faceID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	actor := a.Actor
	rejected := []string{}
	for _, r := range f.Rejected {
		if r != person.ID {
			rejected = append(rejected, r)
		}
	}
	set := bson.M{"personId": person.ID, "name": person.Name, "confirmedBy": actor, "confirmedAt": now, "rejected": rejected, "updatedAt": now,
		"naming": naming.Record(ctx, actor)} // who named it: only a person's name counts for consent (D274)
	unset := bson.M{"candidate": "", "recognized": "", "skipped": "", "asideFrom": ""}
	if f.Confirmed() && f.PersonID == person.ID {
		set["checked"] = true // asked again and answered yes
	} else {
		set["confirmedVia"] = "face"
		unset["confirmedGroup"] = ""
	}
	if err := a.DB.Update(ctx, store.CFaces, a.WorkspaceID(), f.ID, bson.M{"$set": set, "$unset": unset}); err != nil {
		return nil, err
	}
	_, _ = todos.CloseStale(ctx, a, fmt.Sprintf("%s%.1f:", KeyPrefix(f.AssetID), f.From), nil)
	onScreenAgain(ctx, a, f.AssetID, person.ID)
	a.Audit(ctx, "footage.faces.name", "asset", f.AssetID, "", map[string]any{"face": f.ID, "personId": person.ID, "name": person.Name})
	if _, err := Refresh(ctx, a, f.AssetID); err != nil {
		return nil, err
	}
	return Get(ctx, a, f.ID)
}

// Reject records that a face is not a person: its name goes (a confirmed
// name, or one MetaRush gave by looks), that person is never suggested for it
// again, and the library is re-scored when it was a confirmed face (what that
// person looks like changed).
func Reject(ctx context.Context, a *app.App, faceID, personID string) (*models.FaceTrack, error) {
	f, err := Get(ctx, a, faceID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	upd := bson.M{"$addToSet": bson.M{"rejected": personID}, "$set": bson.M{"updatedAt": now}}
	unset := bson.M{}
	if f.PersonID == personID {
		unset["personId"], unset["name"], unset["confirmedBy"], unset["confirmedAt"], unset["checked"], unset["naming"] = "", "", "", "", "", ""
		unset["confirmedVia"], unset["confirmedGroup"] = "", ""
	}
	if f.Candidate != nil && f.Candidate.PersonID == personID {
		unset["candidate"] = ""
	}
	if f.Recognized != nil && f.Recognized.PersonID == personID {
		unset["recognized"] = ""
	}
	if len(unset) > 0 {
		upd["$unset"] = unset
	}
	if err := a.DB.Update(ctx, store.CFaces, a.WorkspaceID(), f.ID, upd); err != nil {
		return nil, err
	}
	_, _ = todos.Close(ctx, a, Key(*f, personID), models.ResolvedElsewhere, "You said it's someone else.")
	if f.Confirmed() {
		_, _ = todos.Close(ctx, a, recheckKey(*f), models.ResolvedElsewhere, "You said it's someone else.")
	}
	a.Audit(ctx, "footage.faces.reject", "asset", f.AssetID, "", map[string]any{"face": f.ID, "personId": personID})
	if _, err := Refresh(ctx, a, f.AssetID); err != nil {
		return nil, err
	}
	return Get(ctx, a, f.ID)
}

// OfPerson lists a person's confirmed faces (best pictures first), the faces
// MetaRush named after them by their looks, and the faces it is asking about
// for them: one per open question ("Is this Maya?" about a group of faces:
// its clearest face, D155).
func OfPerson(ctx context.Context, a *app.App, personID string) (confirmed, recognized, asked []models.FaceTrack, err error) {
	if err = a.DB.Find(ctx, store.CFaces, a.WorkspaceID(), bson.M{"personId": personID}, &confirmed,
		options.Find().SetSort(bson.D{{Key: "quality", Value: -1}}).SetLimit(48).SetProjection(bson.M{"prints": 0})); err != nil {
		return nil, nil, nil, err
	}
	if err = a.DB.Find(ctx, store.CFaces, a.WorkspaceID(), bson.M{"recognized.personId": personID, "personId": bson.M{"$in": bson.A{nil, ""}}}, &recognized,
		options.Find().SetSort(bson.D{{Key: "recognized.score", Value: -1}}).SetLimit(48).SetProjection(bson.M{"prints": 0})); err != nil {
		return nil, nil, nil, err
	}
	var qs []models.Todo
	if err = a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"kind": models.TodoFaceGroup, "state": models.TodoOpen, "context.personId": personID}, &qs,
		options.Find().SetSort(bson.D{{Key: "priority", Value: 1}, {Key: "context.appearances", Value: -1}}).SetLimit(48).SetProjection(bson.M{"context.faceId": 1})); err != nil {
		return nil, nil, nil, err
	}
	var faceIDs []string
	for _, q := range qs {
		if q.Context.FaceID != "" {
			faceIDs = append(faceIDs, q.Context.FaceID)
		}
	}
	if len(faceIDs) > 0 {
		var fs []models.FaceTrack
		if err = a.DB.Find(ctx, store.CFaces, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": faceIDs}}, &fs, options.Find().SetProjection(bson.M{"prints": 0})); err != nil {
			return nil, nil, nil, err
		}
		byID := map[string]models.FaceTrack{}
		for _, f := range fs {
			byID[f.ID] = f
		}
		for _, id := range faceIDs {
			if f, ok := byID[id]; ok {
				asked = append(asked, f)
			}
		}
	}
	if confirmed == nil {
		confirmed = []models.FaceTrack{}
	}
	if recognized == nil {
		recognized = []models.FaceTrack{}
	}
	if asked == nil {
		asked = []models.FaceTrack{}
	}
	return confirmed, recognized, asked, nil
}

// BestOfPeople is each person's best confirmed face (the clearest picture).
func BestOfPeople(ctx context.Context, a *app.App, personIDs []string) map[string]models.FaceTrack {
	out := map[string]models.FaceTrack{}
	if len(personIDs) == 0 {
		return out
	}
	var fs []models.FaceTrack
	if err := a.DB.Find(ctx, store.CFaces, a.WorkspaceID(), bson.M{"personId": bson.M{"$in": personIDs}}, &fs,
		options.Find().SetSort(bson.D{{Key: "quality", Value: -1}}).SetProjection(bson.M{"personId": 1, "assetId": 1, "at": 1, "box": 1, "quality": 1})); err != nil {
		return out
	}
	for _, f := range fs {
		if _, ok := out[f.PersonID]; !ok {
			out[f.PersonID] = f
		}
	}
	return out
}

// Rename follows a person's new name onto their confirmed faces, the faces
// named after them by looks, and open questions.
func Rename(ctx context.Context, a *app.App, personID, name string) {
	ws := a.WorkspaceID()
	_, _ = a.DB.C(store.CFaces).UpdateMany(ctx, bson.M{"workspaceId": ws, "personId": personID}, bson.M{"$set": bson.M{"name": name}})
	_, _ = a.DB.C(store.CFaces).UpdateMany(ctx, bson.M{"workspaceId": ws, "recognized.personId": personID}, bson.M{"$set": bson.M{"recognized.name": name}})
	_, _ = a.DB.C(store.CFaces).UpdateMany(ctx, bson.M{"workspaceId": ws, "candidate.personId": personID}, bson.M{"$set": bson.M{"candidate.name": name}})
	// The questions that suggest them say the new name.
	var qs []models.Todo
	_ = a.DB.Find(ctx, store.CTodos, ws, bson.M{"kind": models.TodoFaceGroup, "state": models.TodoOpen, "context.personId": personID}, &qs, options.Find().SetProjection(bson.M{"assetId": 1}))
	seen := map[string]bool{}
	for _, q := range qs {
		if q.AssetID != "" && !seen[q.AssetID] {
			seen[q.AssetID] = true
			_, _ = Candidates(ctx, a, q.AssetID)
		}
	}
}

// Merge moves faces from one person to another (people.merge) and re-scores
// the library (the merged person's faces now teach what the other looks like).
func Merge(ctx context.Context, a *app.App, fromID, intoID, intoName string) {
	ws := a.WorkspaceID()
	// Who combined the two people names these faces now (D274).
	_, _ = a.DB.C(store.CFaces).UpdateMany(ctx, bson.M{"workspaceId": ws, "personId": fromID}, bson.M{"$set": bson.M{"personId": intoID, "name": intoName,
		"naming": naming.Record(ctx, a.Actor)}})
	_, _ = a.DB.C(store.CFaces).UpdateMany(ctx, bson.M{"workspaceId": ws, "recognized.personId": fromID},
		bson.M{"$set": bson.M{"recognized.personId": intoID, "recognized.name": intoName}})
	_, _ = a.DB.C(store.CFaces).UpdateMany(ctx, bson.M{"workspaceId": ws, "candidate.personId": fromID},
		bson.M{"$set": bson.M{"candidate.personId": intoID, "candidate.name": intoName}})
	_, _ = a.DB.C(store.CFaces).UpdateMany(ctx, bson.M{"workspaceId": ws, "rejected": fromID}, bson.M{"$addToSet": bson.M{"rejected": intoID}})
	_, _ = a.DB.C(store.CAssets).UpdateMany(ctx, bson.M{"workspaceId": ws, "notOnScreen": fromID}, bson.M{"$addToSet": bson.M{"notOnScreen": intoID}})
	_, _ = Refresh(ctx, a, "")
}
