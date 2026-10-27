package interchange

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// Whatever a timeline holds — compound clips and nested sequences used many
// times, very long names, effects, markers and titles, thousands of
// keyframes — reading it, flattening it and making a cut of it costs a small
// multiple of the file's own size, in memory and in time. Each reader gets
// files built from random mixes of those shapes.

// withinItsSize reads a timeline file and fails when it costs more than 50
// times its size in memory (plus a little) or two seconds.
func withinItsSize(t *testing.T, name, doc string) {
	t.Helper()
	bytes, took := allocated(func() {
		if r, err := ReadTimeline([]byte(doc), ReadOptions{Name: name, FPS: 25}); err == nil {
			Flatten(r).Spec(SpecOptions{})
		}
	})
	if limit := uint64(50*len(doc)) + 16<<20; bytes > limit || took > 2*time.Second {
		t.Fatalf("%s: a %.1f MB file took %d MB and %v", name, float64(len(doc))/(1<<20), bytes>>20, took)
	}
}

// shapes picks, for one file, how long its strings are, how many keyframes
// and how many uses of what is nested.
type shapes struct {
	long, keys, uses int
}

func pick(rng *rand.Rand, size, longTimes, keyBytes int) shapes {
	s := shapes{long: []int{16, 4 << 10, 1 << 20}[rng.Intn(3)], keys: []int{0, 2000, 50_000}[rng.Intn(3)], uses: []int{10, 400, 2000}[rng.Intn(3)]}
	// Keep the file near size (the long string is written longTimes times,
	// a keyframe takes keyBytes).
	for s.long*longTimes+s.keys*keyBytes+s.uses*80 > size {
		switch {
		case s.long > 16:
			s.long /= 2
		case s.keys > 0:
			s.keys /= 2
		default:
			s.uses /= 2
		}
	}
	return s
}

func TestFCPXMLStaysWithinItsSize(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for round := 0; round < 6; round++ {
		s := pick(rng, 5<<20, 16, 100)
		long := strings.Repeat("Ab", s.long/2)
		var keys, pts strings.Builder
		for i := 0; i < s.keys; i++ {
			fmt.Fprintf(&keys, `<keyframe time="%d/25s" value="-%ddB"/>`, i, i%30)
			fmt.Fprintf(&pts, `<timept time="%d/25s" value="%d/25s"/>`, i, i)
		}
		var b strings.Builder
		b.WriteString(`<fcpxml version="1.11"><resources><format id="f" frameDuration="1/25s"/><effect id="e" name="` + long + `"/>`)
		b.WriteString(`<asset id="a" name="` + long + `" start="0s" duration="100s" hasVideo="1" hasAudio="1"/>`)
		b.WriteString(`<media id="m"><sequence format="f"><spine>`)
		b.WriteString(`<asset-clip ref="a" name="` + long + `" offset="0s" duration="2s"><adjust-volume amount="-3dB"><param name="amount"><keyframeAnimation>` + keys.String() + `</keyframeAnimation></param></adjust-volume>`)
		b.WriteString(`<filter-video ref="e" name="` + long + `"/><marker start="0s" value="` + long + `" note="` + long + `"/><chapter-marker start="1s" value="` + long + `"/>`)
		b.WriteString(`<timeMap>` + pts.String() + `</timeMap></asset-clip>`)
		b.WriteString(`<transition name="` + long + `" offset="1s" duration="1s"><filter-video ref="e" name="` + long + `"/></transition>`)
		b.WriteString(`<title ref="e" offset="2s" duration="1s" name="` + long + `"><text><text-style>` + long + `</text-style></text></title></spine></sequence></media>`)
		b.WriteString(`</resources><library><event><project name="` + long + `"><sequence format="f"><spine>`)
		b.WriteString(`<clip offset="0s" duration="10s">`)
		for i := 0; i < s.uses; i++ {
			b.WriteString(`<audio ref="a"/>`)
		}
		b.WriteString(`<adjust-volume><param name="amount"><keyframeAnimation>` + keys.String() + `</keyframeAnimation></param></adjust-volume></clip>`)
		for i := 0; i < s.uses; i++ {
			fmt.Fprintf(&b, `<ref-clip ref="m" offset="%ds" duration="3s" name="%s"/>`, 10+i*3, long[:min(len(long), 64)])
		}
		b.WriteString(`</spine></sequence></project></event></library></fcpxml>`)
		withinItsSize(t, fmt.Sprintf("round-%d.fcpxml", round), b.String())
	}
}

func TestFCP7XMLStaysWithinItsSize(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for round := 0; round < 6; round++ {
		s := pick(rng, 5<<20, 14, 60)
		long := strings.Repeat("Ab", s.long/2)
		var keys strings.Builder
		for i := 0; i < s.keys; i++ {
			fmt.Fprintf(&keys, "<keyframe><when>%d</when><value>0.5</value></keyframe>", i)
		}
		var b strings.Builder
		b.WriteString(`<xmeml version="5"><sequence id="top"><name>` + long + `</name><rate><timebase>25</timebase></rate><media><video><track>`)
		for i := 0; i < s.uses; i++ {
			fmt.Fprintf(&b, `<clipitem id="c%d"><name>%s</name><start>%d</start><end>%d</end><in>0</in><out>25</out><sequence id="in"/></clipitem>`, i, long[:min(len(long), 64)], i*25, i*25+25)
		}
		b.WriteString(`</track></video><audio><track>`)
		for i := 0; i < s.uses; i++ {
			fmt.Fprintf(&b, `<clipitem id="s%d"><start>%d</start><end>%d</end><in>0</in><out>25</out><sequence id="in"/></clipitem>`, i, i*25, i*25+25)
		}
		b.WriteString(`</track></audio></media></sequence>`)
		b.WriteString(`<sequence id="in"><name>` + long + `</name><rate><timebase>25</timebase></rate><media><video><track>`)
		b.WriteString(`<clipitem id="v"><name>` + long + `</name><start>0</start><end>25</end><in>0</in><out>25</out><file id="f1"><name>` + long + `</name><pathurl>file:///` + long + `</pathurl></file>`)
		b.WriteString(`<filter><effect><name>` + long + `</name><effectid>` + long + `</effectid></effect></filter><marker><name>` + long + `</name><comment>` + long + `</comment><in>1</in></marker></clipitem>`)
		b.WriteString(`<transitionitem><start>20</start><end>30</end><alignment>center</alignment><effect><name>` + long + `</name><effectid>` + long + `</effectid></effect></transitionitem>`)
		b.WriteString(`</track></video><audio><track><clipitem id="a"><name>` + long + `</name><start>` + long + `</start><end>25</end><in>0</in><out>25</out><file id="f1"/>`)
		b.WriteString(`<filter><effect><name>Audio Levels</name><effectid>audiolevels</effectid><parameter><parameterid>level</parameterid><value>1</value>` + keys.String() + `</parameter></effect></filter></clipitem>`)
		b.WriteString(`</track></audio></media></sequence></xmeml>`)
		withinItsSize(t, fmt.Sprintf("round-%d.xml", round), b.String())
	}
}

func TestOpenTimelineIOStaysWithinItsSize(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for round := 0; round < 6; round++ {
		s := pick(rng, 5<<20, 12, 16)
		long := strings.Repeat("Ab", s.long/2)
		rt := func(v float64) string { return fmt.Sprintf(`{"OTIO_SCHEMA":"RationalTime.1","rate":25,"value":%g}`, v) }
		var keys strings.Builder
		for i := 0; i < s.keys; i++ {
			if i > 0 {
				keys.WriteString(",")
			}
			fmt.Fprintf(&keys, "[%d,-6]", i)
		}
		clipJSON := `{"OTIO_SCHEMA":"Clip.2","name":"` + long + `","source_range":{"OTIO_SCHEMA":"TimeRange.1","start_time":` + rt(0) + `,"duration":` + rt(25) + `},` +
			`"metadata":{"metarush":{"levelKeys":[` + keys.String() + `]}},"effects":[{"OTIO_SCHEMA":"Effect.1","effect_name":"` + long + `"}],` +
			`"markers":[{"OTIO_SCHEMA":"Marker.2","name":"` + long + `","marked_range":{"OTIO_SCHEMA":"TimeRange.1","start_time":` + rt(1) + `,"duration":` + rt(0) + `}}],` +
			`"media_reference":{"OTIO_SCHEMA":"ExternalReference.1","target_url":"file:///` + long + `"}}`
		var children strings.Builder
		for i := 0; i < s.uses; i++ {
			if i > 0 {
				children.WriteString(",")
			}
			children.WriteString(`{"OTIO_SCHEMA":"Clip.2","name":"c","source_range":{"OTIO_SCHEMA":"TimeRange.1","start_time":` + rt(0) + `,"duration":` + rt(25) + `}}`)
		}
		nested := `{"OTIO_SCHEMA":"Stack.1","name":"` + long + `","children":[{"OTIO_SCHEMA":"Track.1","kind":"Video","children":[` + clipJSON + `,` +
			`{"OTIO_SCHEMA":"Transition.1","name":"` + long + `","transition_type":"` + long + `","in_offset":` + rt(5) + `,"out_offset":` + rt(5) + `},` + clipJSON + `]}]}`
		doc := `{"OTIO_SCHEMA":"Timeline.1","name":"` + long + `","tracks":{"OTIO_SCHEMA":"Stack.1","children":[{"OTIO_SCHEMA":"Track.1","kind":"Video","children":[` +
			nested + `,` + children.String() + `]}]}}`
		withinItsSize(t, fmt.Sprintf("round-%d.otio", round), doc)
	}
}

func TestEDLStaysWithinItsSize(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	for round := 0; round < 6; round++ {
		s := pick(rng, 5<<20, 1, 0)
		long := strings.Repeat("Ab", min(s.long, 512<<10)/2)
		var b strings.Builder
		b.WriteString("TITLE: " + long + "\nFCM: NON-DROP FRAME\n\n")
		for i := 0; i < s.uses*4; i++ {
			fmt.Fprintf(&b, "%03d  AX       AA/V  D    025 00:00:01:00 00:00:05:00 01:00:%02d:00 01:00:%02d:00\n* FROM CLIP NAME: %s\n* LOC: 01:00:%02d:00 RED %s\n",
				i%1000, (i*4)%60, (i*4+4)%60, long[:min(len(long), 200)], (i*4)%60, long[:min(len(long), 200)])
		}
		withinItsSize(t, fmt.Sprintf("round-%d.edl", round), b.String())
	}
}

// The shapes a review found (1,000 uses each, 4 MB files): a clip's own
// levels re-read for each of its sound parts, a long transition name, marker
// value or effect name inside something used many times, and a long number
// that isn't one.
func TestReadReviewShapesStayWithinTheirSize(t *testing.T) {
	const uses = 1000
	long := strings.Repeat("A", 4<<20)
	var a strings.Builder
	a.WriteString(`<fcpxml version="1.11"><resources><format id="f" frameDuration="1/25s"/><asset id="a" name="A" start="0s" duration="100s" hasAudio="1" hasVideo="0"/></resources><library><event><project name="P"><sequence format="f"><spine><clip offset="0s" duration="10s">`)
	for i := 0; i < uses; i++ {
		a.WriteString(`<audio ref="a"/>`)
	}
	a.WriteString(`<adjust-volume><param name="amount"><keyframeAnimation>`)
	for i := 0; i < 200_000; i++ {
		a.WriteString(`<keyframe value="-6"/>`)
	}
	a.WriteString(`</keyframeAnimation></param></adjust-volume></clip></spine></sequence></project></event></library></fcpxml>`)
	withinItsSize(t, "levels.fcpxml", a.String())

	compound := func(inside string) string {
		var b strings.Builder
		b.WriteString(`<fcpxml version="1.11"><resources><format id="f" frameDuration="1/25s"/><media id="m"><sequence format="f"><spine>` + inside + `</spine></sequence></media></resources><library><event><project name="P"><sequence format="f"><spine>`)
		for i := 0; i < uses; i++ {
			b.WriteString(`<ref-clip ref="m"/>`)
		}
		b.WriteString(`</spine></sequence></project></event></library></fcpxml>`)
		return b.String()
	}
	withinItsSize(t, "transition.fcpxml", compound(`<transition name="`+long+`" offset="0s" duration="1s"/>`))
	withinItsSize(t, "marker.fcpxml", compound(`<gap offset="0s" duration="1s"><marker start="0s" value="`+long+`"/></gap>`))

	nested := func(inside string) string {
		var b strings.Builder
		b.WriteString(`<xmeml version="5"><sequence id="top"><name>Top</name><rate><timebase>25</timebase></rate><media><video><track>`)
		for i := 0; i < uses; i++ {
			fmt.Fprintf(&b, `<clipitem><start>%d</start><end>%d</end><in>0</in><out>25</out><sequence id="in"/></clipitem>`, i*25, i*25+25)
		}
		b.WriteString(`</track></video></media></sequence><sequence id="in"><name>In</name><media><video><track>` + inside + `</track></video></media></sequence></xmeml>`)
		return b.String()
	}
	withinItsSize(t, "effect.xml", nested(`<clipitem><start>0</start><end>25</end><in>0</in><out>25</out><filter><effect><name>`+long+`</name></effect></filter></clipitem>`))
	withinItsSize(t, "number.xml", nested(`<clipitem><start>`+long+`</start></clipitem>`))
}
