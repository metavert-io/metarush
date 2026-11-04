package ops

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/faces"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/looker"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visionmodels"
	"github.com/metavert-io/metarush/internal/visual"
)

// openModelsCall stores a call recording as a computer with the open models
// indexes it (D405): the frames the open models answer — faces with their
// five points, a capture quality and a roll, the name tile's words at the
// open models' confidence, where the eye is drawn from the faces, and none
// of the labels, animals, bodies or hands Apple Vision adds — an index that
// says it was made by the open models, and its faces.
func openModelsCall(t *testing.T, a *app.App, projectID string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	views := alternating(8,
		callView{print: lookAlong(0, 40, 0.3), tile: "Ada Park", label: "speaker_00"},
		callView{print: lookAlong(1, 41, 0.3), tile: "Ben Okafor", label: "speaker_01"})
	dur := views[len(views)-1].to
	as := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{projectID}, Kind: "video", Name: "porch call.mp4", Title: "Porch call",
		State: models.AssetAvailable, Media: models.MediaInfo{Duration: dur, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now})
	insert(t, a, store.CAssets, as)
	var samples []visual.Sample
	var cuts []float64
	for i, v := range views {
		if i > 0 {
			cuts = append(cuts, v.from)
		}
	}
	for s := 0.5; s < dur; s++ {
		f := vision.Frame{W: 960, H: 540, Labels: []vision.Label{}, Animals: []vision.Detection{}, Humans: []vision.Detection{}}
		var prints [][]float32
		for _, v := range views {
			if s < v.from || s >= v.to {
				continue
			}
			// The face drifts right through the turn (framing follows it).
			x := 0.3 + 0.03*(s-v.from)
			q, roll := 0.58, 0.01
			face := vision.Face{Conf: 0.94, Box: vision.Box{x, 0.18, 0.25, 0.44}, Quality: &q, Roll: &roll, LMC: 0.94,
				LM: [][2]float64{{x + 0.08, 0.32}, {x + 0.17, 0.32}, {x + 0.125, 0.4}, {x + 0.09, 0.48}, {x + 0.16, 0.48}}}
			f.Faces = append(f.Faces, face)
			prints = append(prints, v.print)
			f.Text = append(f.Text, vision.Text{S: v.tile, Conf: 1, Box: vision.Box{0.02, 0.9, 0.11, 0.045}}, vision.Text{S: "• Recording...", Conf: 0.97, Box: vision.Box{0.012, 0.024, 0.14, 0.05}})
			f.Attention = &vision.Attention{At: [2]float64{x + 0.125, 0.4}, Spread: [2]float64{0.07, 0.13}, Peak: 0.94, Objects: []vision.Detection{{Conf: 0.94, Box: face.Box}}}
		}
		samples = append(samples, visual.Sample{At: s, Frame: f, Prints: prints})
	}
	idx := visual.Build(samples, 1, dur, cuts, "proxy")
	idx.Backend, idx.Sees = visual.BackendOpenModels, looker.SeesOpen
	insert(t, a, store.CInsights, models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Kind: visual.InsightKind,
		Provider: looker.OpenModels, Model: visionmodels.ModelName, PromptVersion: visual.Version, Digest: "d", Data: visual.ToMap(idx), CreatedAt: now})
	if err := faces.Save(ctx, a, as.ID, idx); err != nil {
		t.Fatal(err)
	}
	for i, v := range views {
		insert(t, a, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Index: i, From: v.from, To: v.to - 0.2,
			Speaker: v.label, Text: "We planted beans along the porch rail this spring.", CreatedAt: now})
	}
	for _, l := range []string{"speaker_00", "speaker_01"} {
		insert(t, a, store.CSpeakers, models.AssetSpeaker{ID: ids.New(ids.Speaker), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Run: "run_1", Label: l,
			Seconds: 20, Samples: []models.TimeSpan{{From: 0, To: 3}}, CreatedAt: now, UpdatedAt: now})
	}
	return as.ID
}

// Every reader works on an index the open models made (D405): the readout
// says what looked and what it doesn't look for (the one label is the
// person its faces show), faces are listed and asked
// about once per person, the names the call shows are evidence and one click
// names them, search finds the words on screen, and a cut that follows
// movement follows the face.
func TestReadersWorkOnAnOpenModelsIndex(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Porch"})
	clip := openModelsCall(t, a, p.Project.ID)

	v := call[VisualsOut](t, a, "footage.visuals", VisualsIn{AssetID: clip})
	if v.State != "available" || v.LookedWith != visual.BackendOpenModels || !slices.Equal(v.NotLookedFor, []string{"labels", "animals", "people", "gestures"}) ||
		!strings.Contains(v.Note, "open models") || len(v.Lines) == 0 || len(v.Faces) != 8 || len(v.Labels) != 1 || v.Labels[0].Label != "person" {
		t.Fatalf("the readout: %+v", v)
	}
	if !strings.Contains(v.Summary, "Ada Park") || !strings.Contains(v.Lines[0].Line, "1 face") {
		t.Fatalf("the readout's words: %s / %+v", v.Summary, v.Lines[0])
	}

	// One question per person, with the name the call shows as its evidence.
	if _, err := faces.Candidates(ctx, a, clip); err != nil {
		t.Fatal(err)
	}
	gs := call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{ProjectID: p.Project.ID})
	if len(gs.Groups) != 2 || len(gs.Shown) != 1 {
		t.Fatalf("questions: %s %+v %+v", gs.Summary, gs.Groups, gs.Shown)
	}
	for _, g := range gs.Groups {
		if g.Context.Shown == "" || len(g.Context.Faces) == 0 {
			t.Fatalf("a question with the name shown and sample faces: %+v", g.Context)
		}
	}
	out := call[TodoAcceptShownOut](t, a, "todos.acceptShown", TodoAcceptShownIn{AssetID: clip})
	if len(out.Accepted) != 2 {
		t.Fatalf("accepted: %+v", out)
	}
	for _, f := range call[FacesOut](t, a, "footage.faces", FacesIn{AssetID: clip}).Faces {
		if f.Name == "" {
			t.Fatalf("a face the one click didn't name: %+v", f)
		}
	}

	// Search finds the words on screen.
	s := call[SearchOut](t, a, "search", SearchIn{Query: "okafor", ProjectID: p.Project.ID})
	if len(s.Visuals) == 0 || s.Visuals[0].AssetID != clip {
		t.Fatalf("search: %+v", s.Visuals)
	}

	// A vertical cut that follows movement follows the face the open models found.
	r := savePlan(t, a, fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"output":{"preset":"vertical-1080p","fps":30,"framing":"follow"},"clips":[{"id":"open","assetId":%q,"in":0,"out":4}]}}`,
		p.Project.ID, clip))
	fr, _ := framingOf(t, a, r.Revision.ID)
	if fr.From != editspec.FramingFollow || len(fr.Path) < 2 || fr.Path[len(fr.Path)-1].X <= fr.Path[0].X {
		t.Fatalf("following: %+v", fr)
	}
}

// A fixed copy the open models looked at first keeps only the labels of Apple
// Vision's index of the original (D467): the readout says where its places,
// things and animals come from, and that gestures weren't looked for.
func TestVisualsSayAFixedCopyKeptOnlyTheOriginalsLabels(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Jetty"})
	clip := openModelsCall(t, a, p.Project.ID)
	copySHA := strings.Repeat("c", 64)
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": clip}, bson.M{"$set": bson.M{"fixes.copy": models.FixedCopyRef{ID: "copy_steady", SHA256: copySHA,
		StorageKey: "k", Size: 1, MIME: "video/mp4", Fixes: []string{models.FixSteady}, At: time.Now().UTC()}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.C(store.CInsights).UpdateOne(ctx, bson.M{"assetId": clip, "kind": visual.InsightKind}, bson.M{"$set": bson.M{"media": copySHA,
		"data.kept": []string{"labels", "animals", "people"}}, "$push": bson.M{"data.labels": visual.LabelSpan{Label: "dog", Kind: visual.KindAnimal, From: 1, To: 3, Conf: 0.9}}}); err != nil {
		t.Fatal(err)
	}
	v := call[VisualsOut](t, a, "footage.visuals", VisualsIn{AssetID: clip})
	if !slices.Equal(v.NotLookedFor, []string{"gestures"}) || !strings.Contains(v.Note, "from Apple Vision's earlier look at the original") ||
		!strings.Contains(v.Note, "gestures weren't looked for") || !slices.ContainsFunc(v.Labels, func(l VisualLabel) bool { return l.Label == "dog" }) {
		t.Fatalf("the readout: not looked for %v, note %q, labels %+v", v.NotLookedFor, v.Note, v.Labels)
	}
}
