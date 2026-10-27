package faces

import (
	"context"
	"math"
	"sort"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// recognizer is what everyone with confirmed faces looks like right now:
// rebuilt from confirmed faces every time it is needed, never patched (D114).
type recognizer struct {
	th     faceid.Thresholds
	people []faceid.Profile
	// conflict: confirmed faces (by ID) that look unlike the person's other
	// confirmed faces, with the person; they are left out of the profiles
	// until a person says they really are them (Checked).
	conflict      map[string]string
	conflictScore map[string]float64
}

// Vector is a face's fingerprint: the quality-weighted mean of its frames'
// fingerprints (nil when it has none from the current model).
func Vector(f models.FaceTrack) []float32 {
	if f.PrintModel != faceid.ModelName || len(f.Prints) == 0 {
		return nil
	}
	ps := usablePrints(f.Prints)
	vs := make([][]float32, 0, len(ps))
	ws := make([]float64, 0, len(ps))
	for _, p := range ps {
		vs = append(vs, p.V)
		ws = append(ws, p.Q)
	}
	return faceid.Mean(vs, ws)
}

// PrintKeep: a frame's fingerprint less like the rest of its face's than this
// (GroupFit: it would not be named with them) is someone else's and is left
// out. Clips indexed before the frames were taken at their moments have one at
// the end of a face when the next shot started within half a step: in a
// call's speaker view, the next speaker's face (it taught one person's looks
// to another).
const PrintKeep = GroupFit

// usablePrints are a face's fingerprints without those of someone else: the
// one least like the others goes while it is below PrintKeep (on a tie, the
// later one), until one is left.
func usablePrints(ps []models.FacePrint) []models.FacePrint {
	if len(ps) < 2 {
		return ps
	}
	keep := make([]bool, len(ps))
	for i := range keep {
		keep[i] = true
	}
	for left := len(ps); left > 1; left-- {
		worst, low := -1, PrintKeep
		for i, p := range ps {
			if !keep[i] {
				continue
			}
			var rest [][]float32
			var ws []float64
			for j, o := range ps {
				if j != i && keep[j] {
					rest = append(rest, o.V)
					ws = append(ws, max(o.Q, 0.01))
				}
			}
			s := faceid.Cosine(p.V, faceid.Mean(rest, ws))
			if s < low-1e-9 || (worst >= 0 && math.Abs(s-low) <= 1e-9 && p.At > ps[worst].At) {
				worst, low = i, s
			}
		}
		if worst < 0 {
			break
		}
		keep[worst] = false
	}
	out := make([]models.FacePrint, 0, len(ps))
	for i, p := range ps {
		if keep[i] {
			out = append(out, p)
		}
	}
	return out
}

// exemplars are a confirmed face's best fingerprints for a profile.
func exemplars(f models.FaceTrack) []faceid.Exemplar {
	ps := append([]models.FacePrint(nil), usablePrints(f.Prints)...)
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Q > ps[j].Q })
	if len(ps) > faceid.MaxExemplarsPerFace {
		ps = ps[:faceid.MaxExemplarsPerFace]
	}
	out := make([]faceid.Exemplar, 0, len(ps))
	for _, p := range ps {
		out = append(out, faceid.Exemplar{Source: f.ID, Vec: p.V, Weight: p.Q})
	}
	return out
}

// loadRecognizer builds everyone's profile from their confirmed faces.
func loadRecognizer(ctx context.Context, a *app.App) (*recognizer, error) {
	r := &recognizer{th: Thresholds, conflict: map[string]string{}, conflictScore: map[string]float64{}}
	var all []models.FaceTrack
	if err := a.DB.Find(ctx, store.CFaces, a.WorkspaceID(), bson.M{"personId": bson.M{"$nin": bson.A{nil, ""}}, "confirmedBy": bson.M{"$ne": nil}, "printModel": faceid.ModelName}, &all,
		options.Find().SetSort(bson.D{{Key: "confirmedAt", Value: 1}, {Key: "_id", Value: 1}}).SetProjection(bson.M{"personId": 1, "name": 1, "prints": 1, "printModel": 1, "checked": 1,
			"confirmedBy": 1, "confirmedGroup": 1, "quality": 1})); err != nil {
		return nil, err
	}
	fs := teaching(all)
	byPerson := map[string][]models.FaceTrack{}
	var order []string
	for _, f := range fs {
		if _, ok := byPerson[f.PersonID]; !ok {
			order = append(order, f.PersonID)
		}
		byPerson[f.PersonID] = append(byPerson[f.PersonID], f)
	}
	for _, pid := range order {
		faces := byPerson[pid]
		var keep []models.FaceTrack
		for i, f := range faces {
			// With two or more other confirmed faces to compare with, a face that
			// looks unlike all of them is probably a wrong name.
			if len(faces) >= 3 && !f.Checked {
				var others []faceid.Exemplar
				for j, g := range faces {
					if j != i {
						others = append(others, exemplars(g)...)
					}
				}
				if m := faceid.Score(Vector(f), faceid.NewProfile(pid, f.Name, others)); m.Score < ConflictThreshold {
					r.conflict[f.ID], r.conflictScore[f.ID] = pid, m.Score
					continue
				}
			}
			keep = append(keep, f)
		}
		if len(keep) == 0 {
			continue
		}
		var ex []faceid.Exemplar
		for _, f := range keep {
			ex = append(ex, exemplars(f)...)
		}
		r.people = append(r.people, faceid.NewProfile(pid, keep[len(keep)-1].Name, ex))
	}
	return r, nil
}

// teaching keeps the confirmed faces a profile learns from: every face a
// person confirmed one by one, and of each group answer (D155) its
// MaxGroupFaces clearest faces, so one long call can't push every other clip
// out of a person's fingerprints. Order (oldest confirmation first) is kept.
func teaching(fs []models.FaceTrack) []models.FaceTrack {
	byGroup := map[string][]int{}
	for i, f := range fs {
		if f.ConfirmedGroup != "" {
			byGroup[f.PersonID+"/"+f.ConfirmedGroup] = append(byGroup[f.PersonID+"/"+f.ConfirmedGroup], i)
		}
	}
	drop := map[int]bool{}
	for _, idx := range byGroup {
		if len(idx) <= MaxGroupFaces {
			continue
		}
		sorted := append([]int(nil), idx...)
		sort.SliceStable(sorted, func(i, j int) bool { return fs[sorted[i]].Quality > fs[sorted[j]].Quality })
		for _, i := range sorted[MaxGroupFaces:] {
			drop[i] = true
		}
	}
	if len(drop) == 0 {
		return fs
	}
	out := make([]models.FaceTrack, 0, len(fs)-len(drop))
	for i, f := range fs {
		if !drop[i] {
			out = append(out, f)
		}
	}
	return out
}

// decide matches one face against everyone with confirmed faces.
func (r *recognizer) decide(f models.FaceTrack, not map[string]bool) faceid.Decision {
	v := Vector(f)
	if v == nil || len(r.people) == 0 {
		return faceid.Decision{}
	}
	return faceid.Decide(v, r.people, not, r.th)
}

// Change is what re-scoring the library changed.
type Change struct {
	// Clips are the clips whose faces were named, asked about or un-named again.
	Clips int `json:"clips"`
	// People were newly recognized by their looks somewhere.
	People []string `json:"people"`
}

// Refresh re-scores every face in the library against the current profiles
// (after a correction, a new confirmed face or a merge) and brings the clips
// whose outcome changed up to date: names given by looks, the questions about
// groups of faces (what their faces look like, D155), and confirmed faces to
// ask about again. also is a clip to bring up to date regardless ("" for none).
func Refresh(ctx context.Context, a *app.App, also string) (*Change, error) {
	rec, err := loadRecognizer(ctx, a)
	if err != nil {
		return nil, err
	}
	var fs []models.FaceTrack
	if err := a.DB.Find(ctx, store.CFaces, a.WorkspaceID(), bson.M{"$or": bson.A{
		bson.M{"printModel": faceid.ModelName},
		bson.M{"recognized": bson.M{"$ne": nil}},
		bson.M{"candidate.by": bson.M{"$in": bson.A{"looks", "both"}}},
	}}, &fs, options.Find().SetProjection(bson.M{"assetId": 1, "personId": 1, "confirmedBy": 1, "prints": 1, "printModel": 1, "rejected": 1,
		"recognized": 1, "candidate": 1, "checked": 1, "group": 1, "skipped": 1, "quality": 1})); err != nil {
		return nil, err
	}
	dirty := map[string]bool{}
	if also != "" {
		dirty[also] = true
	}
	// A group's suggestion by looks comes from its mean fingerprint.
	type looksOf struct {
		asset    string
		vecs     [][]float32
		qs       []float64
		rejected map[string]bool
		cur      string
	}
	groups := map[string]*looksOf{}
	for _, f := range fs {
		if f.Confirmed() {
			_, conflict := rec.conflict[f.ID]
			if conflict && !f.Checked {
				dirty[f.AssetID] = true // ask again (Candidates keeps the question open)
			}
			continue
		}
		not := map[string]bool{}
		for _, r := range f.Rejected {
			not[r] = true
		}
		d := rec.decide(f, not)
		autoPID, curAuto := "", ""
		if d.Auto != nil {
			autoPID = d.Auto.PersonID
		}
		if f.Recognized != nil {
			curAuto = f.Recognized.PersonID
		}
		switch {
		case autoPID != curAuto:
			dirty[f.AssetID] = true
		case f.Recognized != nil && d.Auto != nil && f.Recognized.Score != d.Auto.Score:
			dirty[f.AssetID] = true
		}
		if f.Recognized != nil || f.Skipped {
			continue
		}
		if f.Group == "" {
			dirty[f.AssetID] = true // not grouped yet
			continue
		}
		k := f.AssetID + "/" + f.Group
		g := groups[k]
		if g == nil {
			g = &looksOf{asset: f.AssetID, rejected: map[string]bool{}}
			groups[k] = g
		}
		g.vecs = append(g.vecs, Vector(f))
		g.qs = append(g.qs, f.Quality)
		for _, r := range f.Rejected {
			g.rejected[r] = true
		}
		if f.Candidate != nil && (f.Candidate.By == "looks" || f.Candidate.By == "both") {
			g.cur = f.Candidate.PersonID
		}
	}
	for _, g := range groups {
		c := centroid(g.vecs, g.qs)
		if c == nil {
			continue
		}
		sug := ""
		if len(rec.people) > 0 {
			if d := faceid.Decide(c, rec.people, g.rejected, rec.th); len(d.Suggestions) > 0 {
				sug = d.Suggestions[0].PersonID
			}
		}
		if sug != g.cur {
			dirty[g.asset] = true
		}
	}
	// The questions about faces asked again that no longer look unlike the rest close themselves.
	var rechecks []models.Todo
	_ = a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"kind": models.TodoFacePerson, "state": models.TodoOpen, "groupKey": bson.M{"$regex": ":recheck$"}}, &rechecks,
		options.Find().SetProjection(bson.M{"assetId": 1, "context.faceId": 1}))
	for _, t := range rechecks {
		if _, still := rec.conflict[t.Context.FaceID]; !still {
			dirty[t.AssetID] = true
		}
	}
	out := &Change{People: []string{}}
	assets := make([]string, 0, len(dirty))
	for id := range dirty {
		assets = append(assets, id)
	}
	sort.Strings(assets)
	seen := map[string]bool{}
	for _, id := range assets {
		_, fresh, err := candidatesWith(ctx, a, id, rec)
		if err != nil && err != store.ErrNotFound {
			return out, err
		}
		out.Clips++
		for _, p := range fresh {
			if !seen[p] {
				seen[p] = true
				out.People = append(out.People, p)
			}
		}
	}
	sort.Strings(out.People)
	return out, nil
}

// Recognize brings one clip's faces up to date after it was indexed: names by
// looks and questions. It returns the open questions and the people newly
// recognized in it.
func Recognize(ctx context.Context, a *app.App, assetID string) (int, []string, error) {
	rec, err := loadRecognizer(ctx, a)
	if err != nil {
		return 0, nil, err
	}
	return candidatesWith(ctx, a, assetID, rec)
}
