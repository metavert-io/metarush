package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/transcribe"
	"github.com/metavert-io/metarush/internal/vision"
)

// Better visual understanding, end to end on visual-a (a talking head, a hard
// cut, a slow pan, a person walking in, an EXIT sign, a dog): visual changes in
// evidence and moments, a smart sheet with a frame for every shot, the
// on-screen index (readout, search), and the face question: "Who is this?"
// while the voice has no name, and still "Who is this?" once it has, with
// Samantha as the first quick pick: the voice alone never asks "Is this
// Samantha?" (D189). Picking her names the face; "not Samantha" removes it.
func TestVisualUnderstanding(t *testing.T) {
	in, ts := setup(t)
	a := in.app
	src := filepath.Join("..", "fixtures", "gen", "visual-a.mp4")
	if _, err := os.Stat(src); err != nil {
		t.Skip("tests/fixtures/gen/visual-a.mp4 missing (scripts/make-fixtures.sh)")
	}
	if !vision.CanRun(ts) {
		t.Skip("the on-screen index needs a Mac with the Xcode Command Line Tools")
	}
	if transcribe.Select(config.Transcription{Provider: "auto"}, ts) == nil || transcribe.NewDiarizer(ts) == nil {
		t.Skip("local transcription / speaker detection not installed")
	}
	prj := call[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Visual"}).Project.ID
	asset := process(t, a, src, prj)

	// 1. Change-aware sampling: cuts, the pan and the person walking in.
	mo := call[ops.MomentsOut](t, a, "footage.moments", ops.MomentsIn{AssetID: asset})
	kinds := map[string]int{}
	for _, m := range mo.Moments {
		if m.Kind == "visual" {
			kinds[m.Change]++
			if len(m.Frames) == 0 {
				t.Errorf("a visual moment without frames to look at: %+v", m)
			}
		}
		if m.Kind == "scene" {
			t.Errorf("new evidence lists visual changes, not raw scenes: %+v", m)
		}
	}
	if kinds[evidence.ChangeCut] != 4 || kinds[evidence.ChangeGradual] < 1 || kinds[evidence.ChangeMotion] < 1 {
		t.Fatalf("visual moments: %v", kinds)
	}

	// 2. A smart sheet shows every shot (5) and says why each frame is there.
	kf := call[ops.KeyframesOut](t, a, "footage.keyframes", ops.KeyframesIn{AssetID: asset})
	if kf.Shots != 5 {
		t.Fatalf("shots: %d", kf.Shots)
	}
	seenShot := map[int]bool{}
	for _, tl := range kf.Tiles {
		seenShot[tl.Shot] = true
		if tl.Caption == "" || tl.Why == "" {
			t.Fatalf("tile without a label: %+v", tl)
		}
	}
	if len(seenShot) != 5 {
		t.Fatalf("every shot needs a frame: %+v", kf.Tiles)
	}
	speech := false
	for _, tl := range kf.Tiles {
		if tl.At < 4 && tl.Words != "" {
			speech = true
		}
	}
	if !speech {
		t.Fatalf("a frame during the talking head carries the words said then: %+v", kf.Tiles)
	}

	// 3. The on-screen index: readout and search.
	v := call[ops.VisualsOut](t, a, "footage.visuals", ops.VisualsIn{AssetID: asset})
	if v.State != "available" {
		t.Fatalf("not indexed: %s %s", v.State, v.Note)
	}
	var lines []string
	for _, l := range v.Lines {
		lines = append(lines, l.Line)
	}
	all := strings.Join(lines, "\n")
	if !strings.HasPrefix(lines[0], "0:00–0:06 close-up · 1 face") || !strings.Contains(all, "text “EXIT”") || !strings.Contains(all, "dog") {
		t.Fatalf("readout:\n%s", all)
	}
	for _, q := range []string{"exit", "EXIT sign", "dog", "beach"} {
		s := call[ops.SearchOut](t, a, "search", ops.SearchIn{Query: q})
		found := false
		for _, h := range s.Visuals {
			if h.AssetID == asset {
				found = true
			}
		}
		if !found {
			t.Errorf("search %q: no on-screen result (%+v)", q, s.Visuals)
		}
	}

	// 4. Faces to people (D155): the face alone on screen while the voice
	// speaks is asked about once — "Who is this?" before the voice has a name,
	// and after it too, with Samantha as the first quick pick (the voice alone
	// never asks "Is this Samantha?", D189); only the answer names it.
	qs := call[ops.TodoListOut](t, a, "todos.list", ops.TodoListIn{AssetID: asset, Kind: models.TodoFaceGroup}).Todos
	if len(qs) != 1 || qs[0].Question != "Who is this?" || qs[0].Context.Label != "speaker_00" {
		t.Fatalf("one question about the face that talks, before the voice has a name: %+v", qs)
	}
	call[ops.SpeakerRenameOut](t, a, "footage.speakers.rename", ops.SpeakerRenameIn{AssetID: asset, Speaker: "Speaker 1", Name: "Samantha"})
	qs = call[ops.TodoListOut](t, a, "todos.list", ops.TodoListIn{AssetID: asset, Kind: models.TodoFaceGroup}).Todos
	if len(qs) != 1 || qs[0].Question != "Who is this?" || len(qs[0].Suggestions) == 0 || qs[0].Suggestions[0].Value != "Samantha" || qs[0].Context.FaceID == "" || len(qs[0].Context.Box) != 4 || qs[0].Context.FrameAt == nil || *qs[0].Context.FrameAt >= 6 {
		t.Fatalf("face question: %+v", qs)
	}
	ans := call[ops.TodoAnswerOut](t, a, "todos.answer", ops.TodoAnswerIn{ID: qs[0].ID, Suggestion: 1})
	if !strings.Contains(ans.Done, "Samantha") {
		t.Fatalf("answer: %+v", ans)
	}
	v = call[ops.VisualsOut](t, a, "footage.visuals", ops.VisualsIn{AssetID: asset, To: 6})
	if len(v.Lines) == 0 || !strings.Contains(v.Lines[0].Line, "1 face (Samantha)") {
		t.Fatalf("named face in the readout: %+v", v.Lines)
	}
	ps := call[ops.PersonShowOut](t, a, "people.show", ops.PersonIn{ID: "Samantha"})
	if len(ps.Faces) != 1 || ps.Faces[0].AssetID != asset {
		t.Fatalf("Samantha's faces: %+v", ps.Faces)
	}
	// "Not Samantha?" takes the name away and she is not suggested for that face again.
	call[ops.FaceOut](t, a, "footage.faces.reject", ops.FaceRejectIn{FaceID: ps.Faces[0].ID})
	fs := call[ops.FacesOut](t, a, "footage.faces", ops.FacesIn{AssetID: asset})
	for _, f := range fs.Faces {
		if f.Name != "" || (f.Maybe != nil && f.Maybe.Name == "Samantha") {
			t.Fatalf("after “not Samantha”: %+v", f)
		}
	}
	if n := len(call[ops.TodoListOut](t, a, "todos.list", ops.TodoListIn{AssetID: asset, Kind: models.TodoFaceGroup}).Todos); n != 0 {
		t.Fatalf("not asked again: %d", n)
	}

	// Indexing again keeps what people said about the faces.
	again, err := ops.CallTyped[ops.AnalyzeOut](context.Background(), a, "footage.analyze", ops.AnalyzeIn{AssetID: asset, Force: true, Transcribe: new(bool)}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	waitAll(t, a, again.Jobs)
	fs = call[ops.FacesOut](t, a, "footage.faces", ops.FacesIn{AssetID: asset})
	kept := false
	for _, f := range fs.Faces {
		if f.From < 6 && f.Maybe == nil && f.Name == "" {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("indexed again: the talking head's face lost “not Samantha”: %+v", fs.Faces)
	}
	if n := len(call[ops.TodoListOut](t, a, "todos.list", ops.TodoListIn{AssetID: asset, Kind: models.TodoFaceGroup}).Todos); n != 0 {
		t.Fatalf("indexed again: asked again (%d)", n)
	}
}
