package faces

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/names"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
	"github.com/metavert-io/metarush/internal/visual"
)

// What MetaRush asks about a clip's faces (D155–D157): faces named by their
// looks are named, confirmed faces that look unlike the person's others are
// asked about again, and the rest are grouped per person and asked about once
// per group that matters, with the names the video shows or says as evidence.

// GroupKeyPrefix is the to-do group key prefix of a clip's face groups.
func GroupKeyPrefix(assetID string) string { return "facegroup:" + assetID + ":" }

// groupKey is the to-do key of one question about a group: who it suggests
// (a person, a new name, or nobody) is part of it, so new evidence asks anew
// while an answered or skipped question is never asked again.
func groupKey(assetID, group, ident string) string {
	return GroupKeyPrefix(assetID) + group + ":" + ident
}

// Evidence weights for a group's suggestion (D156, D189): which name is best
// supported. A call app's name tile is strong; looks count by how alike; a
// lower third, a name said or a title count less; the voice heard while the
// faces are alone on screen counts least (in calls the picture switches late
// and often shows the listener: it was right about a third of the time at 2–3
// seconds), and on its own it never makes MetaRush ask "Is this …?".
const (
	weightVoiceNamed = 1.0
	weightVoiceAuto  = 0.75
	weightTile       = 3.0
	weightLower      = 2.0
	weightTitle      = 1.0
	weightSaidSelf   = 2.0
	weightSaidTo     = 1.0
	weightAside      = 1.5
	weightAlternate  = 2.0 // a second name is offered when it has at least this
	maxPicks         = 3   // quick picks on "Who is this?"
)

// clipVoice is one voice of the clip and who it is.
type clipVoice struct {
	label, display string
	personID, name string
	byPerson       bool // a person named it (not recognized automatically)
	minor          bool
}

// suggestion is a name a group might be, with why.
type suggestion struct {
	key      string // person ID, or "new:<folded name>"
	personID string
	name     string
	isNew    bool
	weight   float64
	by       map[string]bool
	// basis are the kinds of evidence it rests on (D189: models.Basis…);
	// strong says some of it can make MetaRush ask "Is this …?" (the looks or
	// a name shown, and not muted).
	basis    map[string]bool
	strong   bool
	evidence []models.TodoEvidence
	looks    float64
	voiceSec float64
	share    float64
	shown    string
}

// basisList is a suggestion's kinds of evidence in their usual order.
func (s *suggestion) basisList() []string {
	var out []string
	for _, k := range models.BasisKinds {
		if s.basis[k] {
			out = append(out, k)
		}
	}
	return out
}

// groupState is one group of a clip's unnamed faces and what is known about it.
type groupState struct {
	id       string
	faces    []models.FaceTrack // time order
	vecs     [][]float32
	qs       []float64
	centroid []float32
	// voice is the voice cue (D189): a voice heard while these faces are alone
	// on screen for at least MinSeconds and most of the talking then.
	voice      string
	voiceSec   float64
	voiceShare float64
	// heard is the voice heard with them for at least SpeaksSeconds (most of
	// the talking then): enough to say they speak, not who they are.
	heard    string
	heardSec float64
	// talking is the longest any one voice is heard while these faces are
	// alone on screen (with or without one voice being most of the talking).
	talking  float64
	onScreen float64
	long     bool
	inCut    bool
	first    float64
	shown    []ShownName
	best     *suggestion
	alts     []suggestion
	// picks are the names offered on "Who is this?" (evidence too weak to ask
	// "Is this …?"), strongest first.
	picks  []suggestion
	muted  []string // muted kinds of evidence that pointed at a name here
	speaks bool
	bucket string
	asked  bool
	key    string
}

func (g *groupState) tracks() []int {
	out := make([]int, 0, len(g.faces))
	for _, f := range g.faces {
		out = append(out, f.Track)
	}
	return out
}

// clipFacts is what a clip offers to the questions about its faces.
type clipFacts struct {
	as          models.Asset
	projectName string
	pids        []string
	idx         *visual.Index
	segs        []models.TranscriptSegment
	voices      map[string]clipVoice
	ties        map[int]*voiceTie
	said        []SaidName
	screen      *screenNames
	people      []names.Candidate
	cuts        [][2]float64
	// muted are the kinds of evidence this workspace's answers muted (D189);
	// cueOff the people whose voice is no evidence in this clip (two answers
	// here said a face isn't them where it rested on their voice); notHere
	// the people a person said appear nowhere on screen in it.
	muted   map[string]todos.KindScore
	cueOff  map[string]bool
	notHere map[string]bool
}

func loadClipFacts(ctx context.Context, a *app.App, as models.Asset, idx *visual.Index) *clipFacts {
	cf := &clipFacts{as: as, idx: idx, voices: map[string]clipVoice{}}
	cf.pids = as.ProjectIDs
	if cf.pids == nil {
		cf.pids = []string{}
	}
	if len(as.ProjectIDs) > 0 {
		var p models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), as.ProjectIDs[0], &p) == nil {
			cf.projectName = p.Name
		}
	}
	_ = a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), bson.M{"assetId": as.ID}, &cf.segs,
		options.Find().SetProjection(bson.M{"from": 1, "to": 1, "speaker": 1, "text": 1}).SetSort(bson.D{{Key: "from", Value: 1}}))
	var vs []models.AssetSpeaker
	_ = a.DB.Find(ctx, store.CSpeakers, a.WorkspaceID(), bson.M{"assetId": as.ID, "mergedInto": bson.M{"$in": bson.A{nil, ""}}}, &vs,
		options.Find().SetProjection(bson.M{"label": 1, "personId": 1, "name": 1, "source": 1, "minor": 1, "mergedInto": 1}))
	for _, v := range vs {
		cv := clipVoice{label: v.Label, display: v.DisplayName(), minor: v.Minor}
		if v.PersonID != "" && v.Name != "" {
			cv.personID, cv.name, cv.byPerson = v.PersonID, v.Name, v.Source == models.SpeakerByPerson
		}
		cf.voices[v.Label] = cv
	}
	cf.ties = aloneVoices(idx, cf.segs)
	cf.said = SaidNames(cf.segs, vs)
	var ps []models.Person
	_ = a.DB.Find(ctx, store.CPeople, a.WorkspaceID(), bson.M{"mergedInto": bson.M{"$in": bson.A{nil, ""}}}, &ps,
		options.Find().SetProjection(bson.M{"name": 1, "recordings": 1}))
	for _, p := range ps {
		cf.people = append(cf.people, names.Candidate{ID: p.ID, Name: p.Name, Weight: float64(p.Recordings)})
	}
	cf.screen = readScreenNames(idx, func(text string) (string, bool) {
		f := names.Fold(text)
		for _, c := range cf.people {
			if f != "" && names.Fold(c.Name) == f {
				return c.Name, true
			}
		}
		return "", false
	})
	cf.cuts = cutRanges(ctx, a, as)
	cf.muted = todos.Muted(ctx, a)
	cf.cueOff = todos.CueOff(ctx, a, as.ID)
	cf.notHere = map[string]bool{}
	for _, pid := range as.NotOnScreen {
		cf.notHere[pid] = true
		cf.cueOff[pid] = true
	}
	return cf
}

// cutRanges are the stretches of a clip the latest cuts of its projects use.
func cutRanges(ctx context.Context, a *app.App, as models.Asset) [][2]float64 {
	var out [][2]float64
	for _, pid := range as.ProjectIDs {
		var p models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), pid, &p) != nil {
			continue
		}
		h, ok := p.Branches["main"]
		if !ok || h.Head == "" {
			continue
		}
		var r models.Revision
		if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), h.Head, &r) != nil {
			continue
		}
		for _, c := range r.Spec.Clips {
			if c.AssetID == as.ID && c.Out > c.In {
				out = append(out, [2]float64{c.In, c.Out})
			}
		}
	}
	return out
}

// MarkCuts brings the open questions about faces in a project's clips up to
// date after its cut changed (D157): "In your cut" and their order (the
// people in the cut first) follow the latest cut. Nothing is asked again.
func MarkCuts(ctx context.Context, a *app.App, projectID string) error {
	var open []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"state": models.TodoOpen, "kind": models.TodoFaceGroup, "projectIds": projectID}, &open,
		options.Find().SetProjection(bson.M{"assetId": 1, "context.groupId": 1, "context.inCut": 1, "priority": 1})); err != nil {
		return err
	}
	cuts := map[string][][2]float64{}
	for _, t := range open {
		rs, ok := cuts[t.AssetID]
		if !ok {
			var as models.Asset
			if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), t.AssetID, &as); err != nil {
				continue
			}
			rs = cutRanges(ctx, a, as)
			cuts[t.AssetID] = rs
		}
		var fs []models.FaceTrack
		if err := a.DB.Find(ctx, store.CFaces, a.WorkspaceID(), bson.M{"assetId": t.AssetID, "group": t.Context.GroupID}, &fs,
			options.Find().SetProjection(bson.M{"from": 1, "to": 1})); err != nil {
			return err
		}
		in := false
		for _, f := range fs {
			for _, r := range rs {
				if overlap(f.From, f.To, r[0], r[1]) > 0 {
					in = true
				}
			}
		}
		prio := 3
		if in {
			prio = 2
		}
		if in == t.Context.InCut && t.Priority == prio {
			continue
		}
		if _, err := a.DB.C(store.CTodos).UpdateOne(ctx, bson.M{"_id": t.ID, "workspaceId": a.WorkspaceID(), "state": models.TodoOpen},
			bson.M{"$set": bson.M{"context.inCut": in, "priority": prio, "updatedAt": time.Now().UTC()}}); err != nil {
			return err
		}
	}
	return nil
}

// resolve finds who a name shown or said in the video is: someone in People
// with that name (or whose name it starts, word by word: "Lena" is "Lena
// Okoye" when only she fits), else someone new.
func (cf *clipFacts) resolve(name string) (id, full string, isNew bool) {
	var hits []names.Candidate
	for _, c := range cf.people {
		if names.SameName(c.Name, name) {
			hits = append(hits, c)
		}
	}
	for _, h := range hits {
		if names.Fold(h.Name) == names.Fold(name) {
			return h.ID, h.Name, false
		}
	}
	if len(hits) == 1 {
		return hits[0].ID, hits[0].Name, false
	}
	return "", name, true
}

// candidatesWith works out, for each face of a clip, whether it is named by
// its looks, asks again about confirmed faces that look unlike the person's
// others, groups the rest and asks about the groups that matter. It returns
// the open questions about the clip's faces and the people named by looks
// for the first time in this clip.
func candidatesWith(ctx context.Context, a *app.App, assetID string, rec *recognizer) (int, []string, error) {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as); err != nil {
		return 0, nil, err
	}
	st := visual.Load(ctx, a.DB, a.WorkspaceID(), assetID)
	if st == nil || len(st.Index.Tracks) == 0 {
		if _, err := closeOldQuestions(ctx, a, assetID, nil); err != nil {
			return 0, nil, err
		}
		_, err := todos.CloseStale(ctx, a, GroupKeyPrefix(assetID), nil)
		return 0, nil, err
	}
	tracks, err := Load(ctx, a, assetID)
	if err != nil {
		return 0, nil, err
	}
	idx := st.Index
	cf := loadClipFacts(ctx, a, as, idx)
	together := cooccurring(idx)
	ordered := make([]int, 0, len(tracks))
	for id := range tracks {
		ordered = append(ordered, id)
	}
	sort.Ints(ordered)

	// 1. Looks first, for every face not confirmed, so faces on screen together
	// can be compared. The voice heard meanwhile never stops a name by looks
	// (D189): in calls the picture often shows the listener while someone
	// else talks, and a close, clear match by looks is right (D114).
	looks := map[int]*faceid.Match{}
	for _, id := range ordered {
		f := tracks[id]
		if f.Confirmed() {
			continue
		}
		not := map[string]bool{}
		for _, r := range f.Rejected {
			not[r] = true
		}
		for pid := range cf.notHere {
			not[pid] = true
		}
		for _, o := range together[id] { // someone confirmed on screen at the same time is not this face too
			if g, ok := tracks[o]; ok && g.Confirmed() {
				not[g.PersonID] = true
			}
		}
		d := rec.decide(f, not)
		if d.Auto == nil {
			continue
		}
		looks[id] = d.Auto
	}
	for _, id := range ordered { // two faces on screen together can't both be the same person
		m := looks[id]
		if m == nil {
			continue
		}
		for _, o := range together[id] {
			if other := looks[o]; other != nil && other.PersonID == m.PersonID && (other.Score > m.Score || (other.Score == m.Score && o < id)) {
				delete(looks, id)
				break
			}
		}
	}

	// 2. Confirmed faces: asked again when they look unlike the person's others.
	var keepOld []string
	open := 0
	var fresh []string
	now := time.Now().UTC()
	for _, id := range ordered {
		f := tracks[id]
		if !f.Confirmed() {
			continue
		}
		if pid, ok := rec.conflict[f.ID]; ok && pid == f.PersonID && !f.Checked {
			key := recheckKey(f)
			keepOld = append(keepOld, key)
			at := f.At
			t := models.Todo{Kind: models.TodoFacePerson, Scope: "asset", AssetID: as.ID, ProjectIDs: cf.pids, GroupKey: key, FreeText: true, Priority: 2,
				Question: fmt.Sprintf("Is this really %s?", f.Name),
				Detail: fmt.Sprintf("This face in “%s” was named %s, but it doesn't look like %s's other faces. Until you answer, MetaRush doesn't use it to recognize %s elsewhere.",
					as.DisplayTitle(), f.Name, f.Name, f.Name),
				Context: models.TodoContext{AssetName: as.Name, AssetTitle: as.DisplayTitle(), ProjectName: cf.projectName, FrameAt: &at, Box: f.Box, FaceID: f.ID,
					PersonID: f.PersonID, PersonName: f.Name, Score: rec.conflictScore[f.ID]},
				Suggestions: []models.TodoSuggestion{{Label: "Yes, it's " + f.Name, Value: "yes", PersonID: f.PersonID}, {Label: "No, someone else", Value: "no"}}}
			if _, err := todos.Upsert(ctx, a, t); err != nil {
				return open, fresh, err
			}
			open++
		}
	}

	// 3. Names by looks: set or taken away.
	for _, id := range ordered {
		f := tracks[id]
		if f.Confirmed() {
			if f.Recognized != nil || f.Candidate != nil {
				_ = a.DB.Update(ctx, store.CFaces, a.WorkspaceID(), f.ID, bson.M{"$unset": bson.M{"recognized": "", "candidate": ""}, "$set": bson.M{"updatedAt": now}})
				f.Recognized, f.Candidate = nil, nil
				tracks[id] = f
			}
			continue
		}
		var recog *models.FaceRecognized
		if m := looks[id]; m != nil {
			recog = &models.FaceRecognized{PersonID: m.PersonID, Name: m.Name, Score: m.Score}
		}
		switch {
		case recog == nil && f.Recognized != nil:
			if err := a.DB.Update(ctx, store.CFaces, a.WorkspaceID(), f.ID, bson.M{"$unset": bson.M{"recognized": ""}, "$set": bson.M{"updatedAt": now}}); err != nil {
				return open, fresh, err
			}
		case recog != nil && (f.Recognized == nil || f.Recognized.PersonID != recog.PersonID || f.Recognized.Score != recog.Score || f.Recognized.Name != recog.Name):
			recog.At = now
			if f.Recognized != nil && f.Recognized.PersonID == recog.PersonID {
				recog.At = f.Recognized.At
			} else {
				fresh = append(fresh, recog.PersonID)
			}
			set := bson.M{"recognized": recog, "updatedAt": now}
			upd := bson.M{"$set": set}
			if f.Candidate != nil {
				upd["$unset"] = bson.M{"candidate": ""}
			}
			if err := a.DB.Update(ctx, store.CFaces, a.WorkspaceID(), f.ID, upd); err != nil {
				return open, fresh, err
			}
		}
		f.Recognized = recog
		tracks[id] = f
	}

	// 4. The rest, grouped and asked about once per person that matters.
	groups := cf.group(ctx, a, tracks, ordered, rec)
	asked, err := cf.ask(ctx, a, groups, tracks, rec)
	if err != nil {
		return open, fresh, err
	}
	open += asked
	keepGroups := []string{}
	for _, g := range groups {
		if g.asked {
			keepGroups = append(keepGroups, g.key)
		}
	}
	if _, err := closeOldQuestions(ctx, a, assetID, keepOld); err != nil {
		return open, fresh, err
	}
	_, err = todos.CloseStale(ctx, a, GroupKeyPrefix(assetID), keepGroups)
	return open, fresh, err
}

// FoldedNote is the answer recorded on the per-appearance questions earlier
// versions asked, closed because the faces are asked about once per person.
const FoldedNote = "Asked once for all of this person's faces in the clip."

// closeOldQuestions closes a clip's per-face questions other than keep: the
// "Is this …?" per appearance that earlier versions asked (folded into the
// group questions, D159), and "Is this really …?" that no longer apply.
func closeOldQuestions(ctx context.Context, a *app.App, assetID string, keep []string) (int, error) {
	var openTs []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"state": models.TodoOpen, "kind": models.TodoFacePerson, "assetId": assetID}, &openTs,
		options.Find().SetProjection(bson.M{"groupKey": 1})); err != nil {
		return 0, err
	}
	kept := map[string]bool{}
	for _, k := range keep {
		kept[k] = true
	}
	n := 0
	for _, t := range openTs {
		if kept[t.GroupKey] || !strings.HasPrefix(t.GroupKey, KeyPrefix(assetID)) {
			continue
		}
		note := FoldedNote
		if strings.HasSuffix(t.GroupKey, ":recheck") {
			note = ""
		}
		if ok, err := todos.Close(ctx, a, t.GroupKey, models.ResolvedObsolete, note); err != nil {
			return n, err
		} else if ok {
			n++
		}
	}
	return n, nil
}

// group groups the clip's unnamed faces (not confirmed, not named by looks,
// not skipped), keeps the groups on the faces, and gathers each group's facts.
func (cf *clipFacts) group(ctx context.Context, a *app.App, tracks map[int]models.FaceTrack, ordered []int, rec *recognizer) []*groupState {
	var its []item
	var fs []models.FaceTrack
	for _, id := range ordered {
		f := tracks[id]
		if f.Confirmed() || f.Recognized != nil || f.Skipped {
			continue
		}
		fs = append(fs, f)
		its = append(its, item{vec: Vector(f), q: f.Quality, group: f.Group, tie: cf.ties[id], box: f.Box})
	}
	gids := groupItems(its, func() string { return ids.New(ids.FaceGroup) })
	byID := map[string]*groupState{}
	var out []*groupState
	now := time.Now().UTC()
	for i, f := range fs {
		gid := gids[i]
		if f.Group != gid {
			_ = a.DB.Update(ctx, store.CFaces, a.WorkspaceID(), f.ID, bson.M{"$set": bson.M{"group": gid, "updatedAt": now}})
			f.Group = gid
			tracks[f.Track] = f
		}
		g := byID[gid]
		if g == nil {
			g = &groupState{id: gid, first: math.Inf(1)}
			byID[gid] = g
			out = append(out, g)
		}
		g.faces = append(g.faces, f)
		g.vecs = append(g.vecs, its[i].vec)
		g.qs = append(g.qs, f.Quality)
	}
	// Faces named in this clip, by person: confirmed by a person or named by
	// their looks (D189). A voice whose person's faces here look nothing like a
	// group is not that group's voice.
	namedHere := map[string][]faceid.Exemplar{}
	nameOf := map[int]string{}
	for _, id := range ordered {
		f := tracks[id]
		switch {
		case f.Confirmed():
			nameOf[id] = f.Name
			namedHere[f.PersonID] = append(namedHere[f.PersonID], exemplars(f)...)
		case f.Recognized != nil:
			nameOf[id] = f.Recognized.Name
			namedHere[f.Recognized.PersonID] = append(namedHere[f.Recognized.PersonID], exemplars(f)...)
		}
	}
	for _, g := range out {
		tie := &voiceTie{by: map[string]float64{}}
		for _, f := range g.faces {
			tie.add(cf.ties[f.Track])
			d := f.To - f.From
			g.onScreen += d
			if d >= MinFaceSeconds {
				g.long = true
			}
			g.first = math.Min(g.first, f.From)
			for _, r := range cf.cuts {
				if overlap(f.From, f.To, r[0], r[1]) > 0 {
					g.inCut = true
				}
			}
		}
		g.voice, g.voiceSec, g.voiceShare = tie.strongest()
		g.heard, g.heardSec, _ = tie.strongestAt(SpeaksSeconds)
		for _, s := range tie.by {
			g.talking = math.Max(g.talking, s)
		}
		g.centroid = centroid(g.vecs, g.qs)
		g.shown = cf.screen.shownFor(g.tracks(), g.first, func(tr int, name string) bool {
			n := nameOf[tr]
			return n != "" && names.SameName(n, name)
		})
		cf.suggest(g, rec, namedHere)
	}
	return out
}

// unlike says whether a group looks nothing like the faces named as a person
// in the same clip (so that person's voice, heard with it, is someone else's).
func (g *groupState) unlike(personID, name string, namedHere map[string][]faceid.Exemplar) bool {
	ex := namedHere[personID]
	if len(ex) == 0 || g.centroid == nil {
		return false
	}
	return faceid.Score(g.centroid, faceid.NewProfile(personID, name, ex)).Score < SameClipUnlike
}

// suggest works out the name a group is best supported as (D156, D189), and
// the names to offer when nothing is strong enough to ask "Is this …?".
func (cf *clipFacts) suggest(g *groupState, rec *recognizer, namedHere map[string][]faceid.Exemplar) {
	rejected := map[string]bool{}
	notNames := map[string]bool{}
	aside := map[string]int{}
	for _, f := range g.faces {
		for _, r := range f.Rejected {
			rejected[r] = true
		}
		for _, n := range f.NotNames {
			notNames[n] = true
		}
		if f.AsideFrom != "" {
			aside[f.AsideFrom]++
		}
	}
	for pid := range cf.notHere {
		rejected[pid] = true
	}
	cands := map[string]*suggestion{}
	var order []string
	mutedHere := map[string]bool{}
	// add records one piece of evidence for a name: its weight counts unless
	// its kind is muted (D189), and it can make MetaRush ask "Is this …?" only
	// when it is the looks or a name shown.
	add := func(personID, name string, isNew bool, w float64, by, basis string, ev models.TodoEvidence) *suggestion {
		if (personID != "" && rejected[personID]) || notNames[names.Fold(name)] {
			return nil
		}
		key := personID
		if key == "" {
			key = "new:" + names.Fold(name)
		}
		s := cands[key]
		if s == nil {
			s = &suggestion{key: key, personID: personID, name: name, isNew: isNew, by: map[string]bool{}, basis: map[string]bool{}}
			cands[key] = s
			order = append(order, key)
		}
		s.basis[basis] = true
		if _, muted := cf.muted[basis]; muted {
			mutedHere[basis] = true
		} else {
			s.weight += w
			s.by[by] = true
			if by == "looks" || by == "shown" {
				s.strong = true
			}
		}
		ev.Name, ev.PersonID = s.name, personID
		s.evidence = append(s.evidence, ev)
		return s
	}
	// The voice heard while these faces are alone on screen. When it is someone
	// these faces are not (a person said so, or that person's faces here look
	// nothing like them), the voice is someone else's: these faces don't speak.
	voiceIsOther := false
	if v, ok := cf.voices[g.heard]; ok && g.heard != "" && v.personID != "" {
		voiceIsOther = rejected[v.personID] || g.unlike(v.personID, v.name, namedHere)
	}
	// Only a voice heard for MinSeconds is evidence of who they are (the voice
	// cue), and never on its own (D189).
	if v, ok := cf.voices[g.voice]; ok && g.voice != "" && v.personID != "" && !cf.cueOff[v.personID] && !g.unlike(v.personID, v.name, namedHere) {
		w, text := weightVoiceNamed, fmt.Sprintf("%s is talking while this face is on screen (%s).", v.name, aboutSeconds(g.voiceSec))
		if !v.byPerson {
			w, text = weightVoiceAuto, fmt.Sprintf("%s is talking while this face is on screen (%s; recognized by voice, not confirmed yet).", v.name, aboutSeconds(g.voiceSec))
		}
		if s := add(v.personID, v.name, false, w, "voice", models.BasisVoiceCue, models.TodoEvidence{Kind: "voice", Text: text}); s != nil {
			s.voiceSec, s.share = g.voiceSec, g.voiceShare
		}
	}
	// How the faces look.
	if g.centroid != nil && rec != nil && len(rec.people) > 0 {
		d := faceid.Decide(g.centroid, rec.people, rejected, rec.th)
		for i, m := range d.Suggestions {
			if i > 0 {
				break
			}
			w := math.Min(2.5, 1+(m.Score-rec.th.Suggest)*5)
			if s := add(m.PersonID, m.Name, false, w, "looks", models.BasisLooks, models.TodoEvidence{Kind: "looks", Score: m.Score,
				Text: fmt.Sprintf("Looks like %s in other clips (%d%% alike).", m.Name, pct(m.Score))}); s != nil {
				s.looks = m.Score
			}
		}
	}
	// Names the video shows with them.
	for _, sh := range g.shown {
		id, full, isNew := cf.resolve(sh.Name)
		at := sh.At
		var w float64
		var text, basis string
		switch sh.Kind {
		case ShownTile:
			w, text, basis = weightTile, fmt.Sprintf("The video shows “%s” under this face.", sh.Name), models.BasisTile
		case ShownLower:
			w, text, basis = weightLower, fmt.Sprintf("A title shows “%s” while this face is on screen.", sh.Name), models.BasisLower
		default:
			w, text, basis = weightTitle, fmt.Sprintf("A title before this face says “%s”.", sh.Name), models.BasisTitle
		}
		if s := add(id, full, isNew, w, "shown", basis, models.TodoEvidence{Kind: "shown", Text: text, At: &at}); s != nil && sh.Kind != ShownTitle && s.shown == "" {
			s.shown = sh.Name
		}
	}
	// Names said to or by the voice heard with them.
	if link := firstOf(g.voice, g.heard); link != "" {
		for _, sn := range cf.said {
			if sn.Label != link {
				continue
			}
			id, full, isNew := cf.resolve(sn.Name)
			at := sn.At
			if sn.Self {
				add(id, full, isNew, weightSaidSelf, "said", models.BasisSaid, models.TodoEvidence{Kind: "said", At: &at, Text: fmt.Sprintf("This voice says “%s”.", sn.Quote)})
			} else {
				add(id, full, isNew, weightSaidTo, "said", models.BasisSaid, models.TodoEvidence{Kind: "said", At: &at, Text: fmt.Sprintf("Someone says “%s” to them.", sn.Quote)})
			}
		}
	}
	// Left out when someone named the group these faces were in.
	for pid, n := range aside {
		if n*2 < len(g.faces) {
			continue
		}
		name := ""
		for _, c := range cf.people {
			if c.ID == pid {
				name = c.Name
			}
		}
		if name != "" {
			add(pid, name, false, weightAside, "aside", models.BasisAside, models.TodoEvidence{Kind: "aside", Text: fmt.Sprintf("It was left out when you named %s's other faces here: it looks less like them.", name)})
		}
	}
	// A new name that is the start of a fuller one ("Ada" said, "Ada Park" shown) is that one.
	for _, k := range order {
		s := cands[k]
		if s == nil || !s.isNew {
			continue
		}
		for _, k2 := range order {
			o := cands[k2]
			if o == nil || o == s || !names.SameName(o.name, s.name) || len(names.Fold(o.name)) < len(names.Fold(s.name)) {
				continue
			}
			if !o.isNew || len(names.Fold(o.name)) > len(names.Fold(s.name)) {
				o.weight += s.weight
				o.strong = o.strong || s.strong
				for b := range s.by {
					o.by[b] = true
				}
				for b := range s.basis {
					o.basis[b] = true
				}
				for _, ev := range s.evidence {
					ev.Name, ev.PersonID = o.name, o.personID
					o.evidence = append(o.evidence, ev)
				}
				if o.shown == "" {
					o.shown = s.shown
				}
				cands[k] = nil
				break
			}
		}
	}
	var list []*suggestion
	for _, k := range order {
		if s := cands[k]; s != nil {
			list = append(list, s)
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].weight != list[j].weight {
			return list[i].weight > list[j].weight
		}
		if list[i].strong != list[j].strong {
			return list[i].strong
		}
		if list[i].isNew != list[j].isNew {
			return !list[i].isNew
		}
		return list[i].name < list[j].name
	})
	// "Is this …?" needs the looks or a name shown (D189): the voice heard, a
	// name said or being left out of someone's faces only offer that name as a
	// quick pick on "Who is this?".
	switch {
	case len(list) > 0 && list[0].strong:
		g.best = list[0]
		for _, s := range list[1:] {
			// A second name with good support of its own; never the voice heard
			// alone, against what the faces look like or the name shown.
			if s.weight >= weightAlternate && len(g.alts) < 2 && !(len(s.by) == 1 && s.by["voice"]) {
				g.alts = append(g.alts, *s)
			}
		}
	default:
		for _, s := range list {
			if len(g.picks) < maxPicks {
				g.picks = append(g.picks, *s)
			}
		}
	}
	for _, k := range models.BasisKinds {
		if mutedHere[k] {
			g.muted = append(g.muted, k)
		}
	}
	// Does this person speak? A voice is heard while their faces are alone on
	// screen (and it isn't someone they are not), or the name the video shows
	// with them belongs to a voice here.
	// Without one voice that is most of the talking (two voices of one person
	// that the voice detection kept apart, say), someone talking while they are
	// alone on screen for long enough is enough.
	g.speaks = (g.heard != "" && !voiceIsOther) || (g.heard == "" && g.talking >= SpeaksSeconds)
	if !g.speaks && g.best != nil && g.best.by["shown"] {
		for _, v := range cf.voices {
			if v.personID != "" && (v.personID == g.best.personID || names.SameName(v.name, g.best.name)) {
				g.speaks = true
			}
		}
		for _, sn := range cf.said {
			if sn.Self && names.SameName(sn.Name, g.best.name) {
				g.speaks = true
			}
		}
	}
}

func firstOf(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// ask picks the groups to ask about (D157) and keeps their questions and the
// faces' "maybe" names up to date. It returns how many questions are open.
func (cf *clipFacts) ask(ctx context.Context, a *app.App, groups []*groupState, tracks map[int]models.FaceTrack, rec *recognizer) (int, error) {
	// One question per person per video: groups that point at the same person
	// (the same suggestion, or the same voice) share a bucket; one of them asks.
	voiceBucket := map[string]string{}
	voiceBest := map[string]float64{}
	for _, g := range groups {
		if g.best != nil {
			g.bucket = g.best.key
			// The voice heard with them is theirs only when it is the person
			// asked about (not someone talking over a name shown, D189).
			if v, ok := cf.voices[g.heard]; ok && g.heard != "" && (v.personID == "" || v.personID == g.best.personID) && g.heardSec > voiceBest[g.heard] {
				voiceBucket[g.heard], voiceBest[g.heard] = g.bucket, g.heardSec
			}
		}
	}
	for _, g := range groups {
		switch {
		case g.bucket != "":
		case g.heard != "" && voiceBucket[g.heard] != "":
			g.bucket = voiceBucket[g.heard]
		case g.heard != "":
			g.bucket = "voice:" + g.heard
		default:
			g.bucket = "group:" + g.id
		}
	}
	winner := map[string]*groupState{}
	better := func(x, y *groupState) bool {
		if x.inCut != y.inCut {
			return x.inCut
		}
		if math.Abs(x.onScreen-y.onScreen) > 0.01 {
			return x.onScreen > y.onScreen
		}
		return x.first < y.first
	}
	for _, g := range groups {
		if !g.speaks || !g.long || g.onScreen < MinFaceSeconds {
			continue
		}
		if w := winner[g.bucket]; w == nil || better(g, w) {
			winner[g.bucket] = g
		}
	}
	n := 0
	now := time.Now().UTC()
	for _, g := range groups {
		// The faces' "maybe": who the group might be (only a name MetaRush asks about).
		var cand *models.FaceCandidate
		if s := g.best; s != nil {
			cand = &models.FaceCandidate{PersonID: s.personID, Name: s.name, Seconds: math.Round(s.voiceSec*10) / 10, Share: math.Round(s.share*100) / 100, By: byWord(s.by), Score: s.looks}
		}
		for _, f := range g.faces {
			if sameCandidate(f.Candidate, cand) {
				continue
			}
			upd := bson.M{"$set": bson.M{"updatedAt": now}}
			if cand != nil {
				upd["$set"].(bson.M)["candidate"] = cand
			} else {
				upd["$unset"] = bson.M{"candidate": ""}
			}
			if err := a.DB.Update(ctx, store.CFaces, a.WorkspaceID(), f.ID, upd); err != nil {
				return n, err
			}
		}
		if winner[g.bucket] != g {
			continue
		}
		g.asked = true
		t := cf.question(g)
		g.key = t.GroupKey
		if _, err := todos.Upsert(ctx, a, t); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// byWord says why a group might be someone, in the faces' terms.
func byWord(by map[string]bool) string {
	switch {
	case by["voice"] && by["looks"]:
		return "both"
	case by["voice"]:
		return "voice"
	case by["looks"]:
		return "looks"
	case by["shown"]:
		return "shown"
	case by["said"]:
		return "said"
	case by["aside"]:
		return "aside"
	}
	return ""
}

// question builds the to-do that asks about a group.
func (cf *clipFacts) question(g *groupState) models.Todo {
	as := cf.as
	title := as.DisplayTitle()
	samples := samplesOf(g.faces)
	tf := make([]models.TodoFace, 0, len(samples))
	for _, f := range samples {
		tf = append(tf, models.TodoFace{FaceID: f.ID, At: f.At, Box: f.Box})
	}
	t := models.Todo{Kind: models.TodoFaceGroup, Scope: "asset", AssetID: as.ID, ProjectIDs: cf.pids, FreeText: true, Priority: 3,
		Context: models.TodoContext{AssetName: as.Name, AssetTitle: title, ProjectName: cf.projectName, GroupID: g.id, Faces: tf,
			Appearances: len(g.faces), OnScreen: math.Round(g.onScreen*10) / 10, InCut: g.inCut}}
	if g.inCut {
		t.Priority = 2
	}
	if len(samples) > 0 {
		at := samples[0].At
		t.Context.FrameAt, t.Context.Box, t.Context.FaceID = &at, samples[0].Box, samples[0].ID
	}
	display := ""
	if label, sec := firstOf(g.voice, g.heard), math.Max(g.voiceSec, g.heardSec); label != "" {
		display = models.SpeakerDisplayLabel(label)
		if v, ok := cf.voices[label]; ok && v.display != "" {
			display = v.display
		}
		t.Context.Label, t.Context.Speaker, t.Context.Seconds = label, display, math.Round(sec*10)/10
	}
	seen := fmt.Sprintf("Seen %s in “%s” (%s on screen).", times(len(g.faces)), title, aboutDuration(g.onScreen))
	var why []string
	ident := "who"
	if s := g.best; s != nil {
		t.Question = fmt.Sprintf("Is this %s?", s.name)
		t.Context.PersonID, t.Context.PersonName, t.Context.Score = s.personID, s.name, s.looks
		t.Context.Shown = s.shown
		t.Context.Evidence = append(t.Context.Evidence, s.evidence...)
		ident = s.key
		yes := models.TodoSuggestion{Label: "Yes, it's " + s.name, Value: "yes", PersonID: s.personID, Basis: s.basisList()}
		if s.isNew {
			yes = models.TodoSuggestion{Label: "Yes, it's " + s.name, Value: s.name, New: true, Basis: s.basisList()}
		}
		t.Suggestions = append(t.Suggestions, yes)
		for _, o := range g.alts {
			t.Suggestions = append(t.Suggestions, models.TodoSuggestion{Label: fmt.Sprintf("%s (%s)", o.name, altWhy(o)), Value: o.name, PersonID: o.personID, New: o.isNew, Basis: o.basisList()})
			t.Context.Evidence = append(t.Context.Evidence, o.evidence...)
		}
		t.Suggestions = append(t.Suggestions, models.TodoSuggestion{Label: "No, someone else", Value: "no"})
		for _, ev := range s.evidence {
			why = append(why, ev.Text)
		}
		if s.isNew {
			why = append(why, fmt.Sprintf("%s isn't in your people yet: answering yes adds them.", s.name))
		}
	} else {
		t.Question = "Who is this?"
		// Quick picks: the names the evidence points at, too weak to ask about
		// (the voice heard, a name said, D189), then the people talking in this
		// clip whose faces aren't named here yet.
		picked := map[string]bool{}
		for _, p := range g.picks {
			t.Suggestions = append(t.Suggestions, models.TodoSuggestion{Label: fmt.Sprintf("%s (%s)", p.name, altWhy(p)), Value: p.name, PersonID: p.personID, New: p.isNew, Basis: p.basisList()})
			t.Context.Evidence = append(t.Context.Evidence, p.evidence...)
			picked[p.key] = true
			for _, ev := range p.evidence {
				why = append(why, ev.Text)
			}
		}
		if len(g.picks) == 0 && display != "" {
			text := fmt.Sprintf("%s is talking while this face is on screen (%s).", display, aboutSeconds(t.Context.Seconds))
			t.Context.Evidence = append(t.Context.Evidence, models.TodoEvidence{Kind: "voice", Text: text})
			why = append(why, text)
		}
		var quick []clipVoice
		for _, v := range cf.voices {
			if v.personID != "" && !picked[v.personID] && !cf.cueOff[v.personID] && !g.rejects(v.personID) {
				quick = append(quick, v)
			}
		}
		sort.Slice(quick, func(i, j int) bool {
			hi, hj := quick[i].label == firstOf(g.voice, g.heard), quick[j].label == firstOf(g.voice, g.heard)
			if hi != hj {
				return hi
			}
			return quick[i].name < quick[j].name
		})
		for _, v := range quick {
			if len(t.Suggestions) >= maxPicks {
				break
			}
			picked[v.personID] = true
			t.Suggestions = append(t.Suggestions, models.TodoSuggestion{Label: v.name + " (talks in this clip)", Value: v.name, PersonID: v.personID})
		}
	}
	if len(why) > 2 {
		why = why[:2]
	}
	parts := append([]string{seen}, why...)
	for _, k := range g.muted {
		if s, ok := cf.muted[k]; ok {
			parts = append(parts, todos.MutedNote(s))
			break
		}
	}
	if len(g.faces) > 1 {
		parts = append(parts, "One answer names all of them.")
	}
	t.Detail = strings.Join(parts, " ")
	t.GroupKey = groupKey(as.ID, g.id, ident)
	return t
}

// rejects says whether a person said these faces are not personID.
func (g *groupState) rejects(personID string) bool {
	for _, f := range g.faces {
		if contains(f.Rejected, personID) {
			return true
		}
	}
	return false
}

// altWhy says in a few words why a second name or a pick is offered.
func altWhy(s suggestion) string {
	switch {
	case s.basis[models.BasisTile] || s.basis[models.BasisLower] || s.basis[models.BasisTitle]:
		return "shown in the video"
	case s.basis[models.BasisLooks]:
		return fmt.Sprintf("looks %d%% alike", pct(s.looks))
	case s.basis[models.BasisSaid]:
		return "said in the video"
	case s.basis[models.BasisVoiceCue]:
		return "talking then"
	case s.basis[models.BasisAside]:
		return "left out of their other faces"
	}
	return "also possible"
}

// times says how often: "once", "twice", "12 times".
func times(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return fmt.Sprintf("%d times", n)
}

// aboutDuration says a length in plain words: "about 40 seconds", "about 12 minutes".
func aboutDuration(s float64) string {
	switch {
	case s < 1.5:
		return "about a second"
	case s < 60:
		return fmt.Sprintf("about %d seconds", int(s+0.5))
	case s < 90:
		return "about a minute"
	default:
		return fmt.Sprintf("about %d minutes", int(s/60+0.5))
	}
}
