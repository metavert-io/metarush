package faces

import (
	"testing"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

// Names people say (D156): an introduction names the voice that says it; a
// greeting names the voice spoken to (the other one of two; with more people,
// only a greeting that opens a turn, for whoever just spoke).
func TestSaidNames(t *testing.T) {
	two := []models.AssetSpeaker{{Label: "speaker_00"}, {Label: "speaker_01"}}
	segs := []models.TranscriptSegment{
		{From: 0, To: 3, Speaker: "speaker_00", Text: "Hi everyone, I'm Ada. Thanks for joining."},
		{From: 3.5, To: 6, Speaker: "speaker_01", Text: "Thanks, Ada. I'm so glad to be here."},
		{From: 6.5, To: 9, Speaker: "speaker_00", Text: "So, Ben, tell us about the garden. Thank you Ben."},
		{From: 9.5, To: 12, Speaker: "speaker_01", Text: "My name is Ben Okafor and I'm Canadian."},
		{From: 12.5, To: 14, Speaker: "speaker_00", Text: "This is great."},
	}
	got := SaidNames(segs, two)
	want := []SaidName{
		{Name: "Ada", Label: "speaker_00", Self: true},
		{Name: "Ada", Label: "speaker_00"},
		{Name: "Ben", Label: "speaker_01"},
		{Name: "Ben Okafor", Label: "speaker_01", Self: true},
	}
	if len(got) != len(want) {
		t.Fatalf("said: %+v", got)
	}
	for i, w := range want {
		if got[i].Name != w.Name || got[i].Label != w.Label || got[i].Self != w.Self {
			t.Fatalf("said %d: %+v, want %+v", i, got[i], w)
		}
	}
	if got[0].Quote != "I'm Ada" || got[1].Quote != "Thanks, Ada" {
		t.Fatalf("quotes: %q %q", got[0].Quote, got[1].Quote)
	}
	// Three people: only greetings that open a turn count, for whoever just spoke.
	three := append(two, models.AssetSpeaker{Label: "speaker_02"})
	segs3 := []models.TranscriptSegment{
		{From: 0, To: 3, Speaker: "speaker_02", Text: "And talk to your plants."},
		{From: 3.5, To: 6, Speaker: "speaker_00", Text: "Thanks, Cleo. Thanks, Ben. See you next week."},
	}
	got = SaidNames(segs3, three)
	if len(got) != 1 || got[0].Name != "Cleo" || got[0].Label != "speaker_02" {
		t.Fatalf("three voices: %+v", got)
	}
}

// Pronouns, contractions and fillers are never names (D189): a call's
// transcript read "I'm I'm going to share my screen" as the name "I'm" and
// "You're here" as "You're" (both offered as answers on a face question).
func TestPronounsAreNeverNames(t *testing.T) {
	two := []models.AssetSpeaker{{Label: "speaker_00"}, {Label: "speaker_01"}}
	segs := []models.TranscriptSegment{
		{From: 0, To: 3, Speaker: "speaker_00", Text: "I'm I'm going to share my screen."},
		{From: 3.5, To: 6, Speaker: "speaker_01", Text: "You're here. I’m I’m not sure."},
		{From: 6.5, To: 9, Speaker: "speaker_00", Text: "This is it. It's me. We're here. They're here. Thanks, You. Hi, Everyone."},
		{From: 9.5, To: 12, Speaker: "speaker_01", Text: "Um here. I'm Glad you asked. I'm Like, so tired. Well here."},
		{From: 12.5, To: 15, Speaker: "speaker_00", Text: "Hi, I'm Iris. Thanks, Lena."},
	}
	got := SaidNames(segs, two)
	if len(got) != 2 || got[0].Name != "Iris" || !got[0].Self || got[0].Label != "speaker_00" || got[1].Name != "Lena" || got[1].Label != "speaker_01" {
		t.Fatalf("only the real names: %+v", got)
	}
	for _, w := range []string{"I'm", "I’m", "You're", "you’re", "We're", "It's", "Um", "Uh", "Like", "Well", "Yes", "No", "Who", "What", "Here", "There", "This", "That", "Them", "Us", "Me"} {
		if cleanSaid(w) != "" {
			t.Errorf("%q read as a name", w)
		}
	}
	if got := cleanSaid("Iris Vale"); got != "Iris Vale" {
		t.Fatalf("a real name stays: %q", got)
	}
}

// Text right under a face, in line with it, is on their clothes (a name tape,
// a logo): never their name tile, in speaker view or a gallery.
func TestTextOnClothesIsNotANameTile(t *testing.T) {
	face := vision.Box{0.4, 0.15, 0.25, 0.4}
	if k := under(vision.Box{0.45, 0.6, 0.12, 0.04}, []vision.Box{face}); k != -1 {
		t.Fatalf("a name tape on the chest: %d", k)
	}
	if k := under(vision.Box{0.02, 0.9, 0.16, 0.05}, []vision.Box{face}); k != 0 {
		t.Fatalf("the name tile at the bottom left: %d", k)
	}
	tiles := []vision.Box{{0.2, 0.1, 0.06, 0.12}, {0.7, 0.1, 0.06, 0.12}}
	if k := under(vision.Box{0.21, 0.23, 0.05, 0.03}, tiles); k != -1 {
		t.Fatalf("a logo on a shirt in a gallery tile: %d", k)
	}
	if k := under(vision.Box{0.52, 0.42, 0.12, 0.04}, tiles); k != 1 {
		t.Fatalf("the name at the bottom left of the second tile: %d", k)
	}
}

// Shown names (D156): a name tile under each face of a gallery view goes with
// that face only; in speaker view the name at the bottom goes with the one
// face on screen; a banner or a title is not a name, nor text the index is
// unsure of (a misread sign).
func TestShownNamesGalleryAndSpeakerView(t *testing.T) {
	q := 0.7
	var samples []visual.Sample
	for i := 0; i < 13; i++ {
		at := float64(i) + 0.5
		f := vision.Frame{W: 1280, H: 720, Text: []vision.Text{{S: "● Recording...", Conf: 0.9, Box: vision.Box{0.01, 0.02, 0.2, 0.05}}}}
		switch {
		case i < 5: // gallery: two tiles side by side, names under each
			f.Faces = []vision.Face{{Conf: 1, Box: vision.Box{0.15, 0.3, 0.12, 0.2}, Quality: &q}, {Conf: 1, Box: vision.Box{0.65, 0.3, 0.12, 0.2}, Quality: &q}}
			f.Text = append(f.Text, vision.Text{S: "Ada Park", Conf: 0.9, Box: vision.Box{0.03, 0.62, 0.1, 0.04}}, vision.Text{S: "Ben Okafor", Conf: 0.9, Box: vision.Box{0.53, 0.62, 0.1, 0.04}})
		case i < 8: // someone shares their screen: nobody on screen
		default: // speaker view: Ben, his name at the bottom left; a sign under him read badly
			f.Faces = []vision.Face{{Conf: 1, Box: vision.Box{0.4, 0.15, 0.25, 0.45}, Quality: &q}}
			f.Text = append(f.Text, vision.Text{S: "Ben Okafor", Conf: 0.9, Box: vision.Box{0.02, 0.9, 0.12, 0.05}},
				vision.Text{S: "SHLE ERS SUHDAY", Conf: 0.5, Box: vision.Box{0.02, 0.8, 0.3, 0.05}})
		}
		samples = append(samples, visual.Sample{At: at, Frame: f})
	}
	idx := visual.Build(samples, 1, 13, []float64{5, 8}, "proxy")
	sn := readScreenNames(idx, func(string) (string, bool) { return "", false })
	byName := func(tr int) map[string]int {
		out := map[string]int{}
		for _, a := range sn.byTrack[tr] {
			out[a.name] = a.n
		}
		return out
	}
	var left, right, ben int
	for _, tr := range idx.Tracks {
		switch {
		case tr.From < 5 && tr.Box[0] < 0.5:
			left = tr.ID
		case tr.From < 5:
			right = tr.ID
		default:
			ben = tr.ID
		}
	}
	if m := byName(left); m["Ada Park"] != 5 || m["Ben Okafor"] != 0 {
		t.Fatalf("the left tile is Ada's: %v", m)
	}
	if m := byName(right); m["Ben Okafor"] != 5 || m["Ada Park"] != 0 {
		t.Fatalf("the right tile is Ben's: %v", m)
	}
	if m := byName(ben); m["Ben Okafor"] != 5 || len(m) != 1 {
		t.Fatalf("speaker view: Ben's name at the bottom, not a misread sign: %v", m)
	}
	shown := sn.shownFor([]int{left}, 0, nil)
	if len(shown) != 1 || shown[0].Name != "Ada Park" || shown[0].Kind != ShownTile || shown[0].Share != 1 {
		t.Fatalf("Ada's tile: %+v", shown)
	}
	// Ben's name is shown with two of his tracks: with both, a tile; with one
	// of them alone, the other track's time counts against it unless it is
	// already named Ben.
	if s := sn.shownFor([]int{right, ben}, 0, nil); len(s) != 1 || s[0].Name != "Ben Okafor" || s[0].Kind != ShownTile {
		t.Fatalf("Ben's tiles: %+v", s)
	}
	if s := sn.shownFor([]int{right}, 0, nil); len(s) != 0 {
		t.Fatalf("half of the name's time is with another face: not this face's tile: %+v", s)
	}
	if s := sn.shownFor([]int{right}, 0, func(tr int, name string) bool { return tr == ben && name == "Ben Okafor" }); len(s) != 1 {
		t.Fatalf("…unless that face is already named Ben: %+v", s)
	}
}
