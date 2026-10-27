package interchange

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A few hundred bytes of FCPXML whose compound clips each play the next one
// twice open up into 2^n clips: the read stops at its budget with a plain
// answer instead of taking the memory and minutes it would.
func TestReadStopsAWideCompoundClip(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<fcpxml version="1.11"><resources><format id="f" frameDuration="1/25s"/><asset id="a" name="A" start="0s" duration="10s" hasVideo="1" hasAudio="1"/>`)
	const levels = 18
	for i := 0; i < levels; i++ {
		fmt.Fprintf(&b, `<media id="m%d"><sequence><spine>`, i)
		if i == levels-1 {
			b.WriteString(`<asset-clip ref="a" offset="0s" duration="1s"/><asset-clip ref="a" offset="1s" duration="1s"/>`)
		} else {
			fmt.Fprintf(&b, `<ref-clip ref="m%d" offset="0s" duration="2s"/><ref-clip ref="m%d" offset="2s" duration="2s"/>`, i+1, i+1)
		}
		b.WriteString(`</spine></sequence></media>`)
	}
	b.WriteString(`</resources><library><event><project name="Wide"><sequence format="f"><spine><ref-clip ref="m0" offset="0s" duration="4s"/></spine></sequence></project></event></library></fcpxml>`)
	if b.Len() > 4000 {
		t.Fatalf("the file is %d bytes", b.Len())
	}
	start := time.Now()
	_, err := ReadTimeline([]byte(b.String()), ReadOptions{Name: "wide.fcpxml"})
	var re *ReadError
	if !errors.As(err, &re) || !strings.Contains(re.Msg, "more than MetaRush reads") || re.Hint == "" {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}

// A compound clip that plays itself (or a clip that plays it) is read once
// and listed, never opened again and again.
func TestReadRefusesACompoundClipInsideItself(t *testing.T) {
	doc := `<fcpxml version="1.10"><resources><format id="f" frameDuration="1/25s"/><asset id="a" name="A" start="0s" duration="10s" hasVideo="1"/>
<media id="m1" name="Loop"><sequence><spine><asset-clip ref="a" offset="0s" duration="1s"/><ref-clip ref="m2" name="Inner" offset="1s" duration="1s"/></spine></sequence></media>
<media id="m2" name="Inner"><sequence><spine><ref-clip ref="m1" name="Loop" offset="0s" duration="1s"/></spine></sequence></media>
</resources><library><event><project name="P"><sequence format="f"><spine><ref-clip ref="m1" name="Loop" offset="0s" duration="2s"/></spine></sequence></project></event></library></fcpxml>`
	r, err := ReadTimeline([]byte(doc), ReadOptions{Name: "loop.fcpxml"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(r.Unread, "; "), "a compound clip that contains itself (“Loop”)") {
		t.Fatalf("unread %v", r.Unread)
	}
	if r.work > 50 {
		t.Fatalf("the read did %d units of work", r.work)
	}
}

// Final Cut Pro 7 XML: nested sequences named by ID, each used twice, and a
// sequence inside itself.
func TestReadStopsWideAndLoopingNestedSequences(t *testing.T) {
	seq := func(id, body string) string {
		return fmt.Sprintf(`<sequence id="%s"><name>%s</name><rate><timebase>25</timebase></rate><media><video><track>%s</track></video></media></sequence>`, id, id, body)
	}
	nest := func(id string, at int) string {
		return fmt.Sprintf(`<clipitem id="c%s%d"><name>n</name><start>%d</start><end>%d</end><in>0</in><out>25</out><sequence id="%s"/></clipitem>`, id, at, at, at+25, id)
	}
	var b strings.Builder
	b.WriteString(`<xmeml version="5">`)
	const levels = 18
	for i := 0; i < levels; i++ {
		id := fmt.Sprintf("s%d", i)
		if i == levels-1 {
			b.WriteString(seq(id, `<clipitem id="leaf"><name>Leaf</name><start>0</start><end>25</end><in>0</in><out>25</out><file id="f1"><name>a.mov</name><pathurl>file:///a.mov</pathurl></file></clipitem>`))
			continue
		}
		next := fmt.Sprintf("s%d", i+1)
		b.WriteString(seq(id, nest(next, 0)+nest(next, 25)))
	}
	b.WriteString(`</xmeml>`)
	_, err := ReadTimeline([]byte(b.String()), ReadOptions{Name: "wide.xml"})
	var re *ReadError
	if !errors.As(err, &re) || !strings.Contains(re.Msg, "more than MetaRush reads") {
		// Nesting stops at 16 deep: 2^16 sequences are still over the budget.
		t.Fatalf("err = %v", err)
	}

	loop := `<xmeml version="5">` + seq("top", `<clipitem id="x"><name>Me</name><start>0</start><end>25</end><in>0</in><out>25</out><sequence id="top"/></clipitem>`) + `</xmeml>`
	r, err := ReadTimeline([]byte(loop), ReadOptions{Name: "loop.xml"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(r.Unread, "; "), "a nested sequence that contains itself (“top”)") {
		t.Fatalf("unread %v", r.Unread)
	}
}

// Text in many pieces is joined once (it was joined piece by piece, which
// takes minutes at 50 MB), and a file with more elements than a timeline has
// is refused plainly.
func TestReadXMLTextAndElementLimits(t *testing.T) {
	const pieces = 300_000
	doc := "<a>" + strings.Repeat("x<!---->", pieces) + "</a>"
	start := time.Now()
	root, err := parseXML([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(root.text) != maxTextBytes { // kept to the cap, joined once
		t.Fatalf("text of %d", len(root.text))
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
	many := "<fcpxml>" + strings.Repeat("<b/>", maxXMLElements) + "</fcpxml>"
	_, err = ReadTimeline([]byte(many), ReadOptions{Name: "many.fcpxml"})
	if err == nil || !strings.Contains(err.Error(), "more than 500,000 XML elements") {
		t.Fatalf("err = %v", err)
	}
}

// NaN, Inf and absurd times are no times: nothing a read returns can't be
// written as JSON, and nothing is placed a thousand years out.
func TestReadRefusesTimesThatAreNotNumbers(t *testing.T) {
	for _, s := range []string{"NaNs", "Infs", "-Infs", "1e400s", "1/0s", "1e9/1s", "1e5/1e-5s", "99999999999s"} {
		if v, ok := fcpSeconds(s); ok {
			t.Errorf("fcpSeconds(%q) = %v", s, v)
		}
	}
	for _, s := range []string{"NaN", "Inf", "+Inf", "1e309", "nan"} {
		if v, ok := number(s); ok {
			t.Errorf("number(%q) = %v", s, v)
		}
	}
	if v, ok := fcpSeconds("1001/30000s"); !ok || math.Abs(v-1001.0/30000) > 1e-12 {
		t.Fatalf("fcpSeconds = %v %v", v, ok)
	}
	fcp := `<fcpxml version="1.11"><resources><format id="f" frameDuration="NaNs"/><asset id="a" name="A" start="NaNs" duration="Infs" hasVideo="1" hasAudio="1"/></resources>
<library><event><project name="P"><sequence format="f" tcStart="NaNs" duration="Infs"><spine>
<asset-clip ref="a" offset="NaNs" start="1e400s" duration="2s"><adjust-volume amount="NaNdB"/><marker start="Infs" value="m"/></asset-clip>
<asset-clip ref="a" offset="2s" duration="2s"/></spine></sequence></project></event></library></fcpxml>`
	xm := `<xmeml version="5"><sequence id="s"><name>S</name><rate><timebase>NaN</timebase></rate><duration>Inf</duration><media><video><track>
<clipitem id="c"><name>C</name><start>0</start><end>NaN</end><in>Inf</in><out>25</out><file id="f"><name>a.mov</name><duration>NaN</duration></file></clipitem>
<clipitem id="d"><name>D</name><start>0</start><end>25</end><in>0</in><out>25</out><file id="f"/></clipitem>
</track></video></media></sequence></xmeml>`
	otio := `{"OTIO_SCHEMA":"Timeline.1","name":"T","global_start_time":{"OTIO_SCHEMA":"RationalTime.1","rate":1e-300,"value":1},"tracks":{"OTIO_SCHEMA":"Stack.1","children":[{"OTIO_SCHEMA":"Track.1","kind":"Video","children":[
{"OTIO_SCHEMA":"Clip.1","name":"c","source_range":{"OTIO_SCHEMA":"TimeRange.1","start_time":{"OTIO_SCHEMA":"RationalTime.1","rate":1e-300,"value":5},"duration":{"OTIO_SCHEMA":"RationalTime.1","rate":24,"value":1e300}}},
{"OTIO_SCHEMA":"Clip.1","name":"d","source_range":{"OTIO_SCHEMA":"TimeRange.1","start_time":{"OTIO_SCHEMA":"RationalTime.1","rate":24,"value":0},"duration":{"OTIO_SCHEMA":"RationalTime.1","rate":24,"value":24}}}]}]}}`
	for name, doc := range map[string]string{"nan.fcpxml": fcp, "nan.xml": xm, "nan.otio": otio} {
		r, err := ReadTimeline([]byte(doc), ReadOptions{Name: name})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := json.Marshal(r); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		f := Flatten(r)
		if _, err := json.Marshal(f); err != nil {
			t.Fatalf("%s: flat: %v", name, err)
		}
		for _, l := range append(append([]*Lane{}, r.Video...), r.Audio...) {
			for _, p := range l.Pieces {
				if !sane(p.At) || !sane(p.Length) || !sane(p.In) || math.Abs(p.At) > 1e6 {
					t.Fatalf("%s: piece %+v", name, p)
				}
			}
		}
	}
}

// What isn't read is listed once each, at most 100 one by one.
func TestReadUnreadIsCapped(t *testing.T) {
	r := newRead(FormatEDL, "EDL")
	for i := 0; i < 1000; i++ {
		r.unread("thing %d", i)
		r.unread("thing %d", i)
	}
	if len(r.Unread) != maxUnread || r.UnreadMore != 900 {
		t.Fatalf("%d listed, %d more", len(r.Unread), r.UnreadMore)
	}
	f := Flatten(r)
	if !strings.Contains(strings.Join(f.Lost, "; "), "900 more things in the timeline like these aren't carried over") {
		t.Fatalf("lost %v", f.Lost)
	}
}

// The picture is the topmost clip at every moment (the first listed when two
// on one lane overlap), the same as looking at each moment in turn.
func TestFlattenPicksTheTopmostClip(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for round := 0; round < 200; round++ {
		r := newRead(FormatOTIO, "OpenTimelineIO")
		r.FPS = 25
		r.source("s").HasVideo = true
		lanes := 1 + rng.Intn(4)
		for li := 0; li < lanes; li++ {
			l := &Lane{Name: fmt.Sprintf("V%d", li), Kind: KindVideo}
			for k := 0; k < 1+rng.Intn(8); k++ {
				at := float64(rng.Intn(40)) / 2
				l.Pieces = append(l.Pieces, &Piece{Kind: PieceClip, Name: fmt.Sprintf("%d-%d", li, k), Source: "s", At: at, Length: float64(1+rng.Intn(12)) / 2, In: float64(100*li + 10*k), Speed: 1})
			}
			r.Video = append(r.Video, l)
		}
		r.finish()
		f := Flatten(r)
		for _, sh := range f.Shots {
			mid := sh.oldAt + sh.Length/2
			want := ""
			bestLane := -1
			for li, l := range r.Video {
				for _, p := range l.Pieces {
					if p.At <= mid && p.End() >= mid && li > bestLane {
						want, bestLane = p.Name, li
					}
				}
			}
			if sh.Name != want {
				t.Fatalf("round %d: at %v the shot is %s, the topmost is %s\n%s", round, mid, sh.Name, want, dumpFlat(f, nil))
			}
		}
	}
}

// A long flat timeline (tens of thousands of clips, each with its sound, a
// marker each) flattens and becomes a cut in seconds, not minutes; the lists
// of what doesn't carry over stay short.
func TestFlattenALongTimelineQuickly(t *testing.T) {
	const n = 40_000
	r := newRead(FormatOTIO, "OpenTimelineIO")
	r.FPS = 25
	v := &Lane{Name: "V1", Kind: KindVideo}
	a := &Lane{Name: "A1", Kind: KindAudio}
	for i := 0; i < n; i++ {
		src := fmt.Sprintf("s%d", i%50)
		s := r.source(src)
		s.HasVideo, s.HasAudio = true, true
		at := float64(i) * 2
		v.Pieces = append(v.Pieces, &Piece{Kind: PieceClip, Name: src, Source: src, At: at, Length: 1.5, In: at, Speed: 1, Group: fmt.Sprint(i)})
		a.Pieces = append(a.Pieces, &Piece{Kind: PieceClip, Name: src, Source: src, At: at, Length: 1.5, In: at, Speed: 1, Group: fmt.Sprint(i)})
		v.Joins = append(v.Joins, Join{At: at, Kind: TransDissolve, Before: 0.2, After: 0.2})
		r.Markers = append(r.Markers, ReadMarker{At: at + 0.5, Name: fmt.Sprintf("m%d", i)})
	}
	r.Video, r.Audio = []*Lane{v}, []*Lane{a}
	r.finish()
	start := time.Now()
	f := Flatten(r)
	uses := map[string]Use{}
	for i := 0; i < 25; i++ {
		uses[fmt.Sprintf("s%d", i)] = Use{AssetID: fmt.Sprintf("ast_%d", i), Duration: 1e6}
	}
	res := f.Spec(SpecOptions{Uses: uses})
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("took %v", d)
	}
	if len(f.Shots) != n || len(f.Notes) != maxNotes {
		t.Fatalf("%d shots, %d notes", len(f.Shots), len(f.Notes))
	}
	if len(f.Lost) > maxLost+1 || len(res.Lost) > maxLost+1 {
		t.Fatalf("%d and %d lost", len(f.Lost), len(res.Lost))
	}
	if len(res.Spec.Clips) != 500 {
		t.Fatalf("%d clips", len(res.Spec.Clips))
	}
}

// allocated runs f and says how many bytes it allocated and how long it took.
func allocated(f func()) (uint64, time.Duration) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	f()
	d := time.Since(start)
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc, d
}

// Opening a nested sequence again costs everything in it again: a sound clip
// with thousands of level keyframes in a sequence used hundreds of times is
// stopped by the read's budget instead of copying the keyframes each time.
func TestReadChargesNestedSequencesForWhatIsInThem(t *testing.T) {
	var keys strings.Builder
	for i := 0; i < 10000; i++ {
		fmt.Fprintf(&keys, "<keyframe><when>%d</when><value>0.5</value></keyframe>", i)
	}
	nested := `<sequence id="inner"><name>Inner</name><rate><timebase>25</timebase></rate><media><audio><track><clipitem id="snd"><name>Sound</name><start>0</start><end>25</end><in>0</in><out>25</out>` +
		`<file id="f1"><name>a.wav</name><pathurl>file:///a.wav</pathurl></file><filter><effect><name>Audio Levels</name><effectid>audiolevels</effectid><parameter><parameterid>level</parameterid><value>1</value>` +
		keys.String() + `</parameter></effect></filter></clipitem></track></audio></media></sequence>`
	var top strings.Builder
	top.WriteString(`<xmeml version="5"><sequence id="top"><name>Top</name><rate><timebase>25</timebase></rate><media><audio><track>`)
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&top, `<clipitem id="c%d"><name>n</name><start>%d</start><end>%d</end><in>0</in><out>25</out><sequence id="inner"/></clipitem>`, i, i*25, i*25+25)
	}
	top.WriteString(`</track></audio></media></sequence>` + nested + `</xmeml>`)
	doc := []byte(top.String())
	bytes, took := allocated(func() { _, _ = ReadTimeline(doc, ReadOptions{Name: "levels.xml"}) })
	if bytes > 96<<20 || took > 5*time.Second {
		t.Fatalf("a %d KB file took %d MB and %v", len(doc)>>10, bytes>>20, took)
	}
}

// A title's words are kept to a thousand characters, so a compound clip with
// a very long title used hundreds of times doesn't copy all of it each time.
func TestReadKeepsTitlesShort(t *testing.T) {
	long := strings.Repeat("words ", 200_000) // 1.2 MB
	doc := `<fcpxml version="1.10"><resources><format id="f" frameDuration="1/25s"/><effect id="e" name="Basic Title"/>` +
		`<media id="m"><sequence format="f"><spine><title ref="e" offset="0s" duration="1s" name="T"><text><text-style>` + long + `</text-style></text></title></spine></sequence></media>` +
		`</resources><library><event><project name="P"><sequence format="f"><spine>`
	for i := 0; i < 400; i++ {
		doc += fmt.Sprintf(`<ref-clip ref="m" offset="%ds" duration="1s"/>`, i)
	}
	doc += `</spine></sequence></project></event></library></fcpxml>`
	var r *Read
	var err error
	bytes, took := allocated(func() { r, err = ReadTimeline([]byte(doc), ReadOptions{Name: "titles.fcpxml"}) })
	if bytes > 96<<20 || took > 5*time.Second {
		t.Fatalf("took %d MB and %v", bytes>>20, took)
	}
	if err == nil {
		for _, l := range r.Video {
			for _, p := range l.Pieces {
				if len([]rune(p.Text)) > maxTitleRunes {
					t.Fatalf("a title of %d characters", len([]rune(p.Text)))
				}
			}
		}
	}
}

// A speed ramp with thousands of points inside a compound clip used hundreds
// of times is stopped by the budget, not walked point by point each time.
func TestReadChargesCompoundClipsForTheirSpeedPoints(t *testing.T) {
	var pts strings.Builder
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&pts, `<timept time="%d/25s" value="%d/25s"/>`, i, i)
	}
	doc := `<fcpxml version="1.10"><resources><format id="f" frameDuration="1/25s"/><asset id="a" name="A" start="0s" duration="1000s" hasVideo="1"/>` +
		`<media id="m"><sequence format="f"><spine><asset-clip ref="a" offset="0s" duration="1s"><timeMap>` + pts.String() + `</timeMap></asset-clip></spine></sequence></media>` +
		`</resources><library><event><project name="P"><sequence format="f"><spine>`
	for i := 0; i < 800; i++ {
		doc += fmt.Sprintf(`<ref-clip ref="m" offset="%ds" duration="1s"/>`, i)
	}
	doc += `</spine></sequence></project></event></library></fcpxml>`
	bytes, took := allocated(func() { _, _ = ReadTimeline([]byte(doc), ReadOptions{Name: "ramps.fcpxml"}) })
	if took > 5*time.Second || bytes > 256<<20 {
		t.Fatalf("a %d KB file took %v and %d MB", len(doc)>>10, took, bytes>>20)
	}
}

// An OpenTimelineIO file of millions of empty values is refused before
// anything is made of it.
func TestReadCountsOpenTimelineIOValuesFirst(t *testing.T) {
	doc := []byte(`{"OTIO_SCHEMA":"Timeline.1","name":"T","metadata":[` + strings.Repeat("{},", 3_000_000) + "{}]}") // 9 MB
	var err error
	bytes, took := allocated(func() { _, err = ReadTimeline(doc, ReadOptions{Name: "empty.otio"}) })
	if bytes > 16<<20 || took > 5*time.Second {
		t.Fatalf("took %d MB and %v", bytes>>20, took)
	}
	var re *ReadError
	if !errors.As(err, &re) || !strings.Contains(re.Msg, "more than a million values") {
		t.Fatalf("err = %v", err)
	}
	if n := jsonValues([]byte(`{"a":"x,{[","b":[1,2,{"c":"\"}"}]}`)); n != 7 {
		t.Fatalf("counted %d values", n)
	}
}
