package faces

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/todos"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

// participant of a synthetic call: who they are, what their face looks like
// (a fingerprint axis) and the name the call app prints under their picture.
type participant struct {
	name  string
	axis  int
	tile  string
	label string // their voice
}

// callTurn is one turn of the call: who talks, for how long, and what they say.
type callTurn struct {
	who  int
	dur  float64
	text string
}

// speakerViewCall stores a synthetic online call recorded in speaker view
// (D155): the picture follows whoever talks, half a second late, with a hard
// cut at every switch (so every appearance is a new face track); the talker's
// name is printed at the bottom left; every appearance of a face is a little
// different (another moment of the same webcam). It returns the clip, and the
// truth: which participant each face track shows.
func speakerViewCall(t *testing.T, a *app.App, title string, ps []participant, turns []callTurn, extra func(at float64) []vision.Face) (string, map[int]int) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	type view struct {
		who      int
		from, to float64
		look     []float32
		box      vision.Box
	}
	var views []view
	var segs []models.TranscriptSegment
	at := 0.0
	rng := rand.New(rand.NewSource(7))
	for i, tr := range turns {
		segs = append(segs, models.TranscriptSegment{From: at, To: at + tr.dur, Speaker: ps[tr.who].label, Text: tr.text})
		sw := at + 0.5
		if i == 0 {
			sw = 0
		}
		if n := len(views); n > 0 {
			views[n-1].to = sw
		}
		p := ps[tr.who]
		look := look(p.axis, 20+rng.Intn(60), 0.25+0.2*rng.Float64())
		dx := 0.02 * (rng.Float64() - 0.5)
		views = append(views, view{who: tr.who, from: sw, look: look, box: vision.Box{0.35 + dx, 0.15, 0.3, 0.5}})
		at += tr.dur + 0.3
	}
	dur := at + 1
	views[len(views)-1].to = dur
	var cuts []float64
	for _, v := range views[1:] {
		cuts = append(cuts, v.from)
	}
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{}, Kind: "video", Name: strings.ToLower(strings.ReplaceAll(title, " ", "-")) + ".mp4",
		Title: title, SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(title))), Size: 1, MIME: "video/mp4", StorageKey: "k-" + title, State: models.AssetAvailable,
		Media: models.MediaInfo{Duration: dur, HasVideo: true, HasAudio: true, Width: 1280, Height: 720, Streams: []models.Stream{}}, Variants: map[string]models.Variant{},
		CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	var samples []visual.Sample
	for s := 0.5; s < dur; s++ {
		f := vision.Frame{W: 1280, H: 720}
		var prints [][]float32
		for _, v := range views {
			if s >= v.from && s < v.to {
				q := 0.7
				f.Faces = append(f.Faces, vision.Face{Conf: 1, Box: v.box, Quality: &q})
				prints = append(prints, v.look)
				if tile := ps[v.who].tile; tile != "" {
					f.Text = append(f.Text, vision.Text{S: tile, Conf: 0.9, Box: vision.Box{0.02, 0.9, 0.16, 0.05}})
				}
			}
		}
		if extra != nil {
			for _, e := range extra(s) {
				f.Faces = append(f.Faces, e)
				prints = append(prints, nil)
			}
		}
		f.Text = append(f.Text, vision.Text{S: "● Recording...", Conf: 0.9, Box: vision.Box{0.01, 0.02, 0.2, 0.05}})
		samples = append(samples, visual.Sample{At: s, Frame: f, Prints: prints})
	}
	idx := visual.Build(samples, 1, dur, cuts, "proxy")
	ins := models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Kind: visual.InsightKind, Provider: "apple-vision",
		Digest: "d", Data: visual.ToMap(idx), CreatedAt: now}
	if err := a.DB.Insert(ctx, store.CInsights, ins); err != nil {
		t.Fatal(err)
	}
	if err := Save(ctx, a, as.ID, idx); err != nil {
		t.Fatal(err)
	}
	for i, s := range segs {
		s.ID, s.WorkspaceID, s.AssetID, s.Index = ids.New(ids.Segment), a.WorkspaceID(), as.ID, i
		if err := a.DB.Insert(ctx, store.CSegments, s); err != nil {
			t.Fatal(err)
		}
	}
	voiced := map[string]bool{}
	for _, p := range ps {
		if voiced[p.label] {
			continue
		}
		voiced[p.label] = true
		v := models.AssetSpeaker{ID: ids.New(ids.Speaker), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Run: "run_1", Label: p.label, Samples: []models.TimeSpan{},
			CreatedAt: now, UpdatedAt: now}
		if err := a.DB.Insert(ctx, store.CSpeakers, v); err != nil {
			t.Fatal(err)
		}
	}
	// The truth: which participant each track shows (by its fingerprint).
	truth := map[int]int{}
	for _, tr := range idx.Tracks {
		mid := (tr.From + tr.To) / 2
		for _, v := range views {
			if mid >= v.from && mid < v.to {
				truth[tr.ID] = v.who
			}
		}
	}
	return as.ID, truth
}

// nameVoice names one voice of a clip after a person (as a person would).
func nameVoice(t *testing.T, a *app.App, assetID, label string, p models.Person) {
	t.Helper()
	if _, err := a.DB.C(store.CSpeakers).UpdateOne(context.Background(), bson.M{"assetId": assetID, "label": label},
		bson.M{"$set": bson.M{"personId": p.ID, "name": p.Name, "source": models.SpeakerByPerson}}); err != nil {
		t.Fatal(err)
	}
}

// perAppearance counts the questions MetaRush asked before D155: one per face
// track alone on screen while one named person spoke (≥ 2 s then, ≥ MinShare).
func perAppearance(t *testing.T, a *app.App, assetID string) int {
	t.Helper()
	ctx := context.Background()
	st := visual.Load(ctx, a.DB, a.WorkspaceID(), assetID)
	var segs []models.TranscriptSegment
	if err := a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), bson.M{"assetId": assetID}, &segs); err != nil {
		t.Fatal(err)
	}
	var vs []models.AssetSpeaker
	if err := a.DB.Find(ctx, store.CSpeakers, a.WorkspaceID(), bson.M{"assetId": assetID}, &vs); err != nil {
		t.Fatal(err)
	}
	named := map[string]string{}
	for _, v := range vs {
		if v.PersonID != "" {
			named[v.Label] = v.PersonID
		}
	}
	n := 0
	for _, tie := range aloneVoices(st.Index, segs) {
		by := map[string]float64{}
		for l, s := range tie.by {
			if pid := named[l]; pid != "" {
				by[pid] += s
			}
		}
		for _, s := range by {
			if s >= SpeaksSeconds && s/tie.total >= MinShare {
				n++
				break
			}
		}
	}
	return n
}

// threePeople is the call of the acceptance fixture, in miniature: Ada hosts,
// Ben answers, Cleo joins late; 36 turns, so dozens of appearances.
func threePeople() ([]participant, []callTurn) {
	ps := []participant{{"Ada Park", 0, "Ada Park", "speaker_00"}, {"Ben Okafor", 1, "Ben Okafor", "speaker_01"}, {"Cleo Marsh", 2, "Cleo Marsh", "speaker_02"}}
	turns := []callTurn{{0, 4, "Hi everyone, I'm Ada. Thanks for joining the garden call."}, {1, 3.5, "Thanks, Ada. Happy to be here."}}
	rng := rand.New(rand.NewSource(3))
	last := 1
	for len(turns) < 36 {
		who := rng.Intn(3)
		if who == last {
			continue
		}
		text := "We talked about the tomatoes on the balcony."
		if who == 2 && last != 2 && !strings.Contains(fmt.Sprint(turns), "I'm Cleo") {
			text = "Hi, I'm Cleo, sorry I'm late."
		}
		turns = append(turns, callTurn{who, 2.2 + 4*rng.Float64(), text})
		last = who
	}
	return ps, turns
}

// A speaker-view call asked once per appearance (D155) —
// dozens of "Is this Ada Park?" — now asks once per person, with the names the
// call shows under the pictures as evidence; nothing is named before a person
// answers, and the answers name every face correctly.
func TestOneQuestionPerPersonInASpeakerViewCall(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	ps, turns := threePeople()
	asset, truth := speakerViewCall(t, a, "Garden call", ps, turns, nil)
	ada, ben := person(t, a, "Ada Park"), person(t, a, "Ben Okafor")
	// The voices are named already (Cleo's not yet).
	nameVoice(t, a, asset, "speaker_00", ada)
	nameVoice(t, a, asset, "speaker_01", ben)
	tracks, _ := Load(ctx, a, asset)
	before := perAppearance(t, a, asset)
	n, err := Candidates(ctx, a, asset)
	if err != nil {
		t.Fatal(err)
	}
	qs := openQuestions(t, a, asset)
	t.Logf("%d appearances of 3 people; questions before (one per appearance): %d; after: %d", len(tracks), before, n)
	if len(tracks) < 30 || before < 20 {
		t.Fatalf("the call should have dozens of appearances and old questions: %d tracks, %d questions", len(tracks), before)
	}
	if n != 3 || len(qs) != 3 {
		t.Fatalf("one question per person: %d %+v", n, qs)
	}
	seen := 0
	for _, p := range ps {
		q := question(qs, "Is this "+p.name+"?")
		if q == nil {
			t.Fatalf("no question for %s: %+v", p.name, qs)
		}
		appearances := 0
		for _, who := range truth {
			if ps[who].name == p.name {
				appearances++
			}
		}
		if q.Context.Appearances != appearances || len(q.Context.Faces) < 4 || len(q.Context.Faces) > MaxSamples {
			t.Fatalf("%s: seen %d times (truth %d), %d samples", p.name, q.Context.Appearances, appearances, len(q.Context.Faces))
		}
		seen += q.Context.Appearances
		if q.Context.Shown != p.tile || !hasEvidence(q, "shown", "The video shows “"+p.tile+"” under this face.") {
			t.Fatalf("%s: the name under the picture is evidence: %+v", p.name, q.Context.Evidence)
		}
		if !strings.HasPrefix(q.Detail, fmt.Sprintf("Seen %d times in “Garden call”", appearances)) {
			t.Fatalf("plain detail: %q", q.Detail)
		}
	}
	if seen != len(tracks) {
		t.Fatalf("every appearance is in one question: %d of %d", seen, len(tracks))
	}
	cleo := question(qs, "Is this Cleo Marsh?")
	if cleo.Context.PersonID != "" || !cleo.Suggestions[0].New || !hasEvidence(cleo, "said", "This voice says “I'm Cleo”.") {
		t.Fatalf("Cleo isn't in People yet: the answer adds her; she says her name: %+v %+v", cleo.Suggestions, cleo.Context.Evidence)
	}
	if q := question(qs, "Is this Ada Park?"); q.Context.PersonID != ada.ID || !hasEvidence(q, "voice", "") || !hasEvidence(q, "said", "") {
		t.Fatalf("Ada: her voice, her tile and what she says: %+v", q.Context.Evidence)
	}
	// Nothing is learned or named before a person answers.
	tracks, _ = Load(ctx, a, asset)
	for _, f := range tracks {
		if f.Confirmed() || f.Recognized != nil {
			t.Fatalf("named before anyone answered: %+v", f)
		}
	}
	var people int64
	people, _ = a.DB.C(store.CPeople).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID()})
	if people != 2 {
		t.Fatalf("no one added from the names shown before an answer: %d people", people)
	}
	// One answer per person names every face, and only the right ones.
	cleoP := person(t, a, "Cleo Marsh")
	for _, x := range []struct {
		q string
		p models.Person
	}{{"Is this Ada Park?", ada}, {"Is this Ben Okafor?", ben}, {"Is this Cleo Marsh?", cleoP}} {
		q := question(openQuestions(t, a, asset), x.q)
		if q == nil {
			t.Fatalf("%s is no longer asked", x.q)
		}
		res, err := NameGroup(ctx, a, asset, q.Context.GroupID, x.p)
		if err != nil || res.Named != q.Context.Appearances || res.Aside != 0 {
			t.Fatalf("%s: named %+v, %v", x.q, res, err)
		}
	}
	tracks, _ = Load(ctx, a, asset)
	for id, f := range tracks {
		if want := ps[truth[id]].name; f.Name != want || f.ConfirmedVia != "group" {
			t.Fatalf("track %d is %s, named %q (%s)", id, want, f.Name, f.ConfirmedVia)
		}
	}
	if n, _ := Candidates(ctx, a, asset); n != 0 {
		t.Fatalf("nothing left to ask: %d", n)
	}
	// What the answers teach: each group's six clearest faces, not every appearance.
	rec, err := loadRecognizer(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range rec.people {
		if len(p.Exemplars) > MaxGroupFaces*faceid.MaxExemplarsPerFace {
			t.Fatalf("%s learned from %d fingerprints", p.Name, len(p.Exemplars))
		}
	}
}

func hasEvidence(q *models.Todo, kind, text string) bool {
	for _, ev := range q.Context.Evidence {
		if ev.Kind == kind && (text == "" || ev.Text == text) {
			return true
		}
	}
	return false
}

// Ask only what matters (D157): someone shown while another person talks (a
// reaction) shares that person's question and, once that person's faces are
// named, isn't asked about; a face seen for under a second is never asked
// about on its own; a face of someone who never speaks isn't either.
func TestAskOnlyWhatMatters(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	ps := []participant{{"Ada Park", 0, "", "speaker_00"}, {"Dee", 5, "", "speaker_09"}}
	turns := []callTurn{{0, 5, "Hi."}, {0, 5, "Let me tell you about the garden."}, {1, 0.4, ""}, {0, 6, "And the tomatoes."}, {1, 2.6, ""}, {0, 4, "Thanks."}}
	asset, _ := speakerViewCall(t, a, "Reactions", ps, turns, nil)
	// Dee never talks: her two appearances are reaction shots while Ada talks.
	if _, err := a.DB.C(store.CSegments).UpdateMany(ctx, bson.M{"assetId": asset, "speaker": "speaker_09"}, bson.M{"$set": bson.M{"speaker": "speaker_00"}}); err != nil {
		t.Fatal(err)
	}
	ada := person(t, a, "Ada Park")
	nameVoice(t, a, asset, "speaker_00", ada)
	if _, err := Candidates(ctx, a, asset); err != nil {
		t.Fatal(err)
	}
	qs := openQuestions(t, a, asset)
	if len(qs) != 1 || qs[0].Question != "Who is this?" || len(qs[0].Suggestions) == 0 || qs[0].Suggestions[0].Label != "Ada Park (talking then)" {
		t.Fatalf("one question for the voice: Ada's own faces (seen longest), her voice a quick pick: %+v", qs)
	}
	ada0 := qs[0]
	tracks, _ := Load(ctx, a, asset)
	for _, f := range tracks {
		if f.Group == ada0.Context.GroupID && f.Prints[0].V[5] > 0.5 {
			t.Fatalf("Dee's face is not in Ada's question: %+v", f)
		}
	}
	if _, err := NameGroup(ctx, a, asset, ada0.Context.GroupID, ada); err != nil {
		t.Fatal(err)
	}
	if n, _ := Candidates(ctx, a, asset); n != 0 {
		t.Fatalf("Dee never speaks (Ada's voice is heard over her, and Ada's faces here look nothing like her): not asked, %d", n)
	}
	tracks, _ = Load(ctx, a, asset)
	for _, f := range tracks {
		if f.Prints[0].V[5] > 0.5 && f.Confirmed() {
			t.Fatalf("Dee was named %s", f.Name)
		}
	}
}

// Faces of one person that look different (another camera) but are on screen
// with the same voice are asked about one at a time: at most one open question
// per person per clip.
func TestOneOpenQuestionPerPersonPerClip(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	ps := []participant{{"Ada Park", 0, "", "speaker_00"}, {"Ada Park", 7, "", "speaker_00"}, {"Ben Okafor", 1, "", "speaker_01"}}
	turns := []callTurn{{0, 5, "One."}, {2, 4, "Two."}, {1, 5, "Three."}, {2, 4, "Four."}, {0, 5, "Five."}, {2, 3, "Six."}, {1, 4, "Seven."}}
	asset, _ := speakerViewCall(t, a, "Two cameras", ps, turns, nil)
	ada := person(t, a, "Ada Park")
	nameVoice(t, a, asset, "speaker_00", ada)
	if _, err := Candidates(ctx, a, asset); err != nil {
		t.Fatal(err)
	}
	qs := openQuestions(t, a, asset)
	var asksAda int
	for _, q := range qs {
		if q.Context.Label == "speaker_00" {
			asksAda++
		}
	}
	if asksAda != 1 || len(qs) != 2 {
		t.Fatalf("one question about Ada's voice at a time (and Ben's): %+v", qs)
	}
	groups := map[string]bool{}
	tracks, _ := Load(ctx, a, asset)
	for _, f := range tracks {
		if f.Prints[0].V[0] > 0.5 || f.Prints[0].V[7] > 0.5 {
			groups[f.Group] = true
		}
	}
	if len(groups) != 2 {
		t.Fatalf("her two looks are two groups (they look nothing alike): %v", groups)
	}
}

// The grouping itself: fingerprints first (average linkage), then the voice
// (somewhat alike, same voice), then faces without fingerprints by voice and
// place; existing groups keep their IDs and never join each other by
// themselves.
func TestGroupItems(t *testing.T) {
	tie := func(l string, s float64) *voiceTie {
		return &voiceTie{by: map[string]float64{l: s}, total: s}
	}
	box := []float64{0.35, 0.15, 0.3, 0.5}
	n := 0
	newID := func() string { n++; return fmt.Sprintf("g%d", n) }
	// Joining by voice takes the voice cue's 6 seconds (D189).
	items := []item{
		{vec: look(0, 10, 0.3), tie: tie("a", 7), box: box},                // 0: A
		{vec: look(0, 11, 0.35), tie: tie("a", 7), box: box},               // 1: A
		{vec: look(1, 12, 0.3), tie: tie("b", 7), box: box},                // 2: B
		{vec: look(0, 13, 0.9), tie: tie("a", 7), box: box},                // 3: A at another angle (0.41 alike): joins by voice
		{vec: nil, tie: tie("b", 6), box: box},                             // 4: no fingerprint, B's voice, B's place
		{vec: nil, tie: tie("c", 2), box: []float64{0, 0, 0.1, 0.1}},       // 5: nobody's
		{vec: look(1, 14, 0.2), group: "old", tie: tie("b", 7), box: box},  // 6: an existing group of B
		{vec: look(2, 15, 0.2), group: "old2", tie: tie("c", 7), box: box}, // 7: another existing group
		{vec: look(1, 16, 0.3), group: "old3", tie: tie("b", 7), box: box}, // 8: an existing group that looks like 6 but stays apart
	}
	// A voice heard for less than that ties nothing.
	if got := groupItems([]item{{vec: look(0, 10, 0.3), tie: tie("a", 5)}, {vec: look(0, 13, 0.9), tie: tie("a", 5)}}, newID); got[0] == got[1] {
		t.Fatalf("5 seconds of a voice joined two groups: %v", got)
	}
	got := groupItems(items, newID)
	same := func(i, j int) bool { return got[i] == got[j] }
	switch {
	case !same(0, 1) || !same(0, 3):
		t.Fatalf("A's faces are one group: %v", got)
	case got[2] != "old" && got[2] != "old3":
		t.Fatalf("B's new face joins an existing group of B: %v", got)
	case !same(4, 2):
		t.Fatalf("a face without a fingerprint joins its voice's group in its place: %v", got)
	case same(5, 0) || same(5, 2) || same(5, 7):
		t.Fatalf("a face of nobody's place stays on its own: %v", got)
	case got[6] != "old" || got[7] != "old2" || got[8] != "old3":
		t.Fatalf("existing groups keep their IDs and stay apart: %v", got)
	case same(0, 2):
		t.Fatalf("A and B apart: %v", got)
	}
}

// A group answer names the faces that fit and sets aside the one that looks
// less like the rest (GroupFit).
func TestFits(t *testing.T) {
	vecs := [][]float32{look(0, 10, 0.3), look(0, 11, 0.3), look(0, 12, 0.35), look(4, 13, 0.3), nil}
	got := fits(vecs, []float64{0.7, 0.7, 0.7, 0.7, 0.7})
	if !got[0] || !got[1] || !got[2] || got[3] || !got[4] {
		t.Fatalf("fits: %v", got)
	}
	if c := centroid(vecs, nil); c == nil || math.Abs(faceid.Cosine(c, look(0, 10, 0.3))) < 0.5 {
		t.Fatalf("centroid of several fingerprints: %v", c != nil)
	}
	fs := make([]models.FaceTrack, 20)
	for i := range fs {
		fs[i] = models.FaceTrack{ID: fmt.Sprint(i), From: float64(i * 10), To: float64(i*10 + 3), Quality: float64(i%7) / 10, Box: []float64{0, 0, 0.1, 0.1}}
	}
	s := samplesOf(fs)
	if len(s) != MaxSamples || s[0].Quality != 0.6 {
		t.Fatalf("samples: the clearest first, %d in all: %+v", len(s), s)
	}
	sort.Slice(s, func(i, j int) bool { return s[i].From < s[j].From })
	if s[len(s)-1].From < 100 {
		t.Fatalf("samples spread over the clip: %+v", s)
	}
}

// Older installations (D159): the per-appearance questions earlier versions
// asked fold into one question per person (closed as no longer needed, with a
// note), and asking again changes nothing.
func TestOldQuestionsFoldIntoGroups(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	ps, turns := threePeople()
	asset, _ := speakerViewCall(t, a, "Old call", ps, turns, nil)
	ada, ben := person(t, a, "Ada Park"), person(t, a, "Ben Okafor")
	nameVoice(t, a, asset, "speaker_00", ada)
	nameVoice(t, a, asset, "speaker_01", ben)
	// What an earlier MetaRush left open: "Is this …?" for every appearance.
	tracks, _ := Load(ctx, a, asset)
	old := 0
	for _, f := range tracks {
		pid, name := ada.ID, ada.Name
		if f.Prints[0].V[1] > 0.5 {
			pid, name = ben.ID, ben.Name
		} else if f.Prints[0].V[2] > 0.5 {
			continue
		}
		at := f.At
		if _, err := todos.Upsert(ctx, a, models.Todo{Kind: models.TodoFacePerson, Scope: "asset", AssetID: asset, GroupKey: Key(f, pid), FreeText: true, Priority: 3,
			Question: "Is this " + name + "?", Context: models.TodoContext{FaceID: f.ID, PersonID: pid, PersonName: name, FrameAt: &at, Box: f.Box}}); err != nil {
			t.Fatal(err)
		}
		old++
	}
	if old < 20 || len(openQuestions(t, a, asset)) != old {
		t.Fatalf("the old questions: %d", old)
	}
	if _, err := Candidates(ctx, a, asset); err != nil {
		t.Fatal(err)
	}
	if qs := openQuestions(t, a, asset); len(qs) != 3 {
		t.Fatalf("%d old questions became %d: %+v", old, len(qs), qs)
	}
	var folded []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"assetId": asset, "kind": models.TodoFacePerson}, &folded); err != nil {
		t.Fatal(err)
	}
	for _, q := range folded {
		if q.State != models.TodoDismissed || q.Resolution != models.ResolvedObsolete || q.Answer != FoldedNote {
			t.Fatalf("folded: %+v", q)
		}
	}
	if _, err := Candidates(ctx, a, asset); err != nil || len(openQuestions(t, a, asset)) != 3 {
		t.Fatalf("asking again changes nothing: %v", err)
	}
}
