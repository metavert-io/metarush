package interchange

import (
	"math"
	"os"
	"regexp"
	"strings"
	"testing"
)

func readFile(t *testing.T, name string, o ReadOptions) *Read {
	t.Helper()
	b, err := os.ReadFile("testdata/import/" + name)
	if err != nil {
		t.Fatal(err)
	}
	o.Name = name
	r, err := ReadTimeline(b, o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func closeTo(a, b float64) bool { return math.Abs(a-b) < 1e-3 }

// A 23.976 fps FCPXML 1.11 timeline starting at 01:00:00:00 (media-rep file
// addresses, a dissolve, a connected title and music, chapter markers):
// exact NTSC times, nothing late, no gaps that aren't there.
func TestReadNTSCFCPXML(t *testing.T) {
	r := readFile(t, "ntsc-2398.fcpxml", ReadOptions{})
	if r.FormatName != "FCPXML 1.11" || !closeTo(r.FPS, 24000.0/1001) || !closeTo(r.Start, 3600) || !closeTo(r.Length, 20.02) || len(r.Warnings) > 0 {
		t.Fatalf("%s %v fps start %v length %v warnings %v", r.FormatName, r.FPS, r.Start, r.Length, r.Warnings)
	}
	for _, s := range r.Sources {
		if s.Path == "" || !strings.HasPrefix(s.Path, "/Volumes/Shoot/") {
			t.Fatalf("source %+v has no file", s)
		}
	}
	f := Flatten(r)
	if len(f.Shots) != 3 {
		t.Fatalf("%d shots\n%s", len(f.Shots), dumpFlat(f, nil))
	}
	a, b, c := f.Shots[0], f.Shots[1], f.Shots[2]
	if !closeTo(a.Length, 5.005) || !closeTo(a.In, 10) || !closeTo(b.oldAt, 5.005) || !closeTo(b.In, 1.001) || !closeTo(c.oldAt, 12.012) || !closeTo(c.In, 20) {
		t.Fatalf("shots\n%s", dumpFlat(f, nil))
	}
	if b.Enter.Kind != TransDissolve || !closeTo(b.Enter.Before+b.Enter.After, 1.001) || !closeTo(b.HandleBefore, 0.5005) || !closeTo(a.HandleAfter, 0.5005) {
		t.Fatalf("dissolve %+v, handles %v %v", b.Enter, b.HandleBefore, a.HandleAfter)
	}
	if !closeTo(f.Length, 18.018) || len(f.Closed) != 1 || !closeTo(f.Closed[0][1]-f.Closed[0][0], 2.002) {
		t.Fatalf("length %v closed %v", f.Length, f.Closed)
	}
	if len(f.Chapters) != 2 || f.Chapters[0].Title != "Intro" || !closeTo(f.Chapters[0].At, 1) || f.Chapters[1].Title != "The answer" || !closeTo(f.Chapters[1].At, 11.01) {
		t.Fatalf("chapters %+v", f.Chapters)
	}
	if len(f.Notes) != 1 || f.Notes[0].Text != "Fix the jump" || !closeTo(f.Notes[0].At, 2) || f.Notes[0].Shot != 0 {
		t.Fatalf("notes %+v", f.Notes)
	}
	if len(f.Sounds) != 1 || f.Sounds[0].Role != RoleMusic || f.Sounds[0].Level == nil || *f.Sounds[0].Level != -18 || !closeTo(f.Sounds[0].FadeIn, 1.001) || !closeTo(f.Sounds[0].Length, 10.01) {
		t.Fatalf("sounds %+v", f.Sounds)
	}
	if !a.OwnSound || a.Level == nil || *a.Level != -6 || b.OwnSound || !c.OwnSound {
		t.Fatalf("own sound %v %v %v level %v", a.OwnSound, b.OwnSound, c.OwnSound, a.Level)
	}
	lost := strings.Join(f.Lost, "\n")
	if !strings.Contains(lost, "the title “Ada Lovelace” at 0:01") || !strings.Contains(lost, "2 seconds with no picture at 0:10 was closed up") {
		t.Fatalf("lost: %s", lost)
	}
	// The cut renders at 24 frames a second.
	res := f.Spec(SpecOptions{Uses: map[string]Use{"fcpxml:r2": {AssetID: astA, Duration: 30}, "fcpxml:r3": {AssetID: astC, Duration: 10}, "fcpxml:r4": {AssetID: astM, Duration: 30}}})
	if res.Spec.Output.FPS != 24 || len(res.Spec.Clips) != 3 || res.Spec.Clips[1].Transition.Type != "crossfade" || len(res.Spec.Audio) != 1 {
		t.Fatalf("spec %+v", res.Spec)
	}
	if !strings.Contains(res.Spec.Clips[0].Notes, "Fix the jump") {
		t.Fatalf("notes on the first clip: %q", res.Spec.Clips[0].Notes)
	}
}

// FCPXML 1.9 moved a file's address into media-rep: 1.9, 1.10 and 1.11
// versions of MetaRush's own 1.8 export read the same.
func TestReadFCPXMLVersions(t *testing.T) {
	tl, _ := fixtureTimeline(t)
	v18 := writeFormat(t, tl, FormatFCPXML)
	base, err := ReadTimeline(v18, ReadOptions{Name: "x.fcpxml"})
	if err != nil {
		t.Fatal(err)
	}
	want := dumpFlat(Flatten(base), nil)
	src := regexp.MustCompile(`(<asset [^>]*?) src="([^"]*)"([^>]*?)/>`)
	for _, v := range []string{"1.9", "1.10", "1.11"} {
		s := strings.Replace(string(v18), `fcpxml version="1.8"`, `fcpxml version="`+v+`"`, 1)
		s = src.ReplaceAllString(s, `$1$3><media-rep kind="original-media" src="$2"/></asset>`)
		r, err := ReadTimeline([]byte(s), ReadOptions{Name: "x.fcpxml"})
		if err != nil {
			t.Fatal(err)
		}
		if r.FormatName != "FCPXML "+v || len(r.Warnings) > 0 {
			t.Fatalf("%s: %s %v", v, r.FormatName, r.Warnings)
		}
		for _, so := range r.Sources {
			if so.Path == "" {
				t.Fatalf("%s: source %s lost its file", v, so.Name)
			}
		}
		if got := dumpFlat(Flatten(r), nil); got != want {
			t.Fatalf("%s reads differently:\n%s\nwant\n%s", v, got, want)
		}
	}
}

// A drop-frame CMX 3600 EDL: read at 29.97 with a warning, a dissolve, a
// sound-and-picture event whose sound stays with its picture, a chapter.
func TestReadDropFrameEDL(t *testing.T) {
	r := readFile(t, "drop-frame.edl", ReadOptions{})
	if !closeTo(r.FPS, 30000.0/1001) || len(r.Warnings) != 1 {
		t.Fatalf("fps %v warnings %v", r.FPS, r.Warnings)
	}
	f := Flatten(r)
	if len(f.Shots) != 3 || len(f.Sounds) != 0 || !f.Shots[1].OwnSound || f.Shots[0].OwnSound {
		t.Fatalf("\n%s", dumpFlat(f, nil))
	}
	if f.Shots[2].Enter.Kind != TransDissolve || !closeTo(f.Shots[2].Enter.After, 1.001) || !closeTo(f.Shots[1].HandleAfter, 1.001) {
		t.Fatalf("dissolve %+v", f.Shots[2].Enter)
	}
	if f.Shots[2].Name != "Interview A.mov" || r.Source(f.Shots[1].Source).File != "Broll 1.mov" || !r.Source(f.Shots[0].Source).TimecodeOnly {
		t.Fatalf("names %q %q", f.Shots[2].Name, r.Source(f.Shots[1].Source).File)
	}
	if len(f.Chapters) != 1 || f.Chapters[0].Title != "Chapter one" || math.Abs(f.Chapters[0].At-6) > 0.01 {
		t.Fatalf("chapters %+v", f.Chapters)
	}
	// Given the rate, there is no warning.
	if r := readFile(t, "drop-frame.edl", ReadOptions{FPS: 29.97}); len(r.Warnings) != 0 {
		t.Fatalf("warnings %v", r.Warnings)
	}
}

// Files that aren't timelines, and AAF, are refused in plain words.
func TestReadRefuses(t *testing.T) {
	for name, data := range map[string][]byte{
		"cut.aaf":     {0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1, 0, 0},
		"notes.txt":   []byte("just some words"),
		"clips.otio":  []byte(`{"OTIO_SCHEMA": "SerializableCollection.1", "children": []}`),
		"broken.otio": []byte(`{"OTIO_SCHEMA": "Timeline.1",`),
		"lib.fcpxml":  []byte(`<?xml version="1.0"?><fcpxml version="1.10"><resources/><library><event name="E"/></library></fcpxml>`),
		"clips.xml":   []byte(`<?xml version="1.0"?><xmeml version="5"><clip id="c"><name>x</name></clip></xmeml>`),
		"empty.edl":   []byte("TITLE: nothing\nFCM: NON-DROP FRAME\n"),
		"other.xml":   []byte(`<?xml version="1.0"?><filter><effect/></filter>`),
	} {
		_, err := ReadTimeline(data, ReadOptions{Name: name})
		re, ok := err.(*ReadError)
		if !ok || re.Msg == "" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// B-roll on a lane above an interview: the picture switches to it and back,
// and the interview's sound plays on under it as one sound.
func TestFlattenBrollOverInterview(t *testing.T) {
	r := newRead(FormatOTIO, "OpenTimelineIO")
	r.FPS = 30
	r.source("a").HasVideo, r.source("a").HasAudio, r.source("a").File = true, true, "Interview.mov"
	r.source("b").HasVideo, r.source("b").File = true, "Broll.mov"
	r.Video = []*Lane{
		{Name: "V1", Kind: KindVideo, Pieces: []*Piece{{Kind: PieceClip, Name: "Interview", Source: "a", At: 0, Length: 10, In: 5, Speed: 1, Group: "g1"}}},
		{Name: "V2", Kind: KindVideo, Pieces: []*Piece{{Kind: PieceClip, Name: "Broll", Source: "b", At: 3, Length: 4, In: 0, Speed: 1, Group: "g2"}}},
	}
	r.Audio = []*Lane{{Name: "A1", Kind: KindAudio, Pieces: []*Piece{{Kind: PieceClip, Name: "Interview", Source: "a", At: 0, Length: 10, In: 5, Speed: 1, Group: "g1"}}}}
	r.finish()
	f := Flatten(r)
	if len(f.Shots) != 3 || f.Shots[1].Name != "Broll" || !closeTo(f.Shots[2].In, 12) {
		t.Fatalf("\n%s", dumpFlat(f, nil))
	}
	for _, s := range f.Shots {
		if s.OwnSound {
			t.Fatalf("%s plays its own sound; the interview's should play on as one\n%s", s.Name, dumpFlat(f, nil))
		}
	}
	if len(f.Sounds) != 1 || f.Sounds[0].Role != RoleDialogue || !closeTo(f.Sounds[0].Length, 10) || !closeTo(f.Sounds[0].In, 5) {
		t.Fatalf("sounds %+v", f.Sounds)
	}
}
