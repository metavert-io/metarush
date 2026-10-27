package interchange

import (
	"fmt"
	"math"
	"net/url"
	"path/filepath"
	"strconv"
)

// Final Cut Pro 7 XML ("xmeml", version 5: Final Cut Pro 7, Premiere Pro,
// DaVinci Resolve), checked against Apple's DTD for version 5. Transitions
// sit on edit points with the outgoing and incoming clips extended into them
// (start/end -1, as Final Cut Pro 7 writes them); stereo sound is a pair of
// linked tracks; levels are the Audio Levels filter (linear gain) with
// keyframes at media frames.

// XMEMLVersion is the xmeml version written.
const XMEMLVersion = "5"

type xmWriter struct {
	fps     int
	clipN   int
	seqN    int
	files   map[string]string // media ID → file ID
	written map[string]bool   // file elements already written in full
	notes   *[]string
	refs    []xmRef // every clipitem of the current sequence, for links
}

type xmRef struct {
	node      *node
	link      string // the clip (Item.Link)
	id        string
	mediatype string
	track     int // 1-based track index within the media type
	clipIndex int // 1-based index among the track's clipitems
	group     int // audio: the stereo group
}

func xmRate(fps float64) *node {
	base, ntsc := xmTimebase(fps)
	return el("rate").add(textEl("timebase", strconv.Itoa(base)), textEl("ntsc", ntsc))
}

// xmTimebase is xmeml's timebase and NTSC flag for a frame rate (29.97 = 30 NTSC).
func xmTimebase(fps float64) (int, string) {
	if fps <= 0 {
		return 30, "FALSE"
	}
	base := int(math.Round(fps))
	if math.Abs(fps-float64(base)) < 0.005 {
		return base, "FALSE"
	}
	base = int(math.Ceil(fps))
	return base, "TRUE"
}

func xmTimecode(fps float64) *node {
	return el("timecode").add(xmRate(fps), textEl("string", "00:00:00:00"), textEl("frame", "0"), textEl("displayformat", "NDF"))
}

// PathURL is a file://localhost URL, as Final Cut Pro 7 writes them.
func PathURL(path string) string {
	u := url.URL{Scheme: "file", Host: "localhost", Path: filepath.ToSlash(path)}
	return u.String()
}

// WriteXMEML writes the timeline as Final Cut Pro 7 XML (xmeml version 5).
func WriteXMEML(tl *Timeline, meta map[string]any) ([]byte, []string, error) {
	notes := append([]string(nil), tl.Notes...)
	w := &xmWriter{fps: tl.FPS, files: map[string]string{}, written: map[string]bool{}, notes: &notes}
	seq, err := w.sequence(tl)
	if err != nil {
		return nil, nil, err
	}
	root := el("xmeml", "version", XMEMLVersion).add(seq)
	notes = append(notes, xmemlNotes(tl)...)
	return document(root, "xmeml"), dedupe(notes), nil
}

func (w *xmWriter) sequence(tl *Timeline) (*node, error) {
	w.seqN++
	fps := float64(tl.FPS)
	seq := el("sequence", "id", fmt.Sprintf("sequence-%d", w.seqN))
	seq.add(textEl("name", cleanText(tl.Name)), textEl("duration", strconv.Itoa(tl.Duration)), xmRate(fps), xmTimecode(fps))
	video := el("video").add(el("format").add(el("samplecharacteristics").add(xmRate(fps),
		textEl("width", strconv.Itoa(tl.Width)), textEl("height", strconv.Itoa(tl.Height)),
		textEl("anamorphic", "FALSE"), textEl("pixelaspectratio", "square"), textEl("fielddominance", "none"))))
	audio := el("audio").add(
		el("format").add(el("samplecharacteristics").add(textEl("depth", "16"), textEl("samplerate", "48000"))),
		el("outputs").add(xmOutput(1), xmOutput(2)))
	// Build every track's clipitems first, then add links (they name items on other tracks).
	type built struct {
		parent *node
		track  *node
	}
	outer := w.refs
	w.refs = nil
	defer func() { w.refs = outer }()
	var tracks []built
	for vi, t := range tl.Video {
		tn, err := w.track(tl, t, vi+1, 0, "video")
		if err != nil {
			return nil, err
		}
		tracks = append(tracks, built{video, tn})
	}
	idx := 0
	for _, t := range tl.Audio {
		for ch := 1; ch <= 2; ch++ { // a stereo pair of tracks per sound track
			idx++
			tn, err := w.track(tl, t, idx, ch, "audio")
			if err != nil {
				return nil, err
			}
			tn.add(textEl("outputchannelindex", strconv.Itoa(ch)))
			tracks = append(tracks, built{audio, tn})
		}
	}
	w.addLinks()
	for _, b := range tracks {
		b.parent.add(b.track)
	}
	seq.add(el("media").add(video, audio))
	for _, m := range tl.Markers {
		mk := el("marker").add(textEl("name", cleanText(m.Name)), textEl("comment", cleanText(m.Comment)),
			textEl("in", strconv.Itoa(m.Start)), textEl("out", strconv.Itoa(m.Start+max(1, m.Duration))))
		seq.add(mk)
	}
	return seq, nil
}

func xmOutput(i int) *node {
	n := strconv.Itoa(i)
	return el("group").add(textEl("index", n), textEl("numchannels", "1"), textEl("downmix", "0"), el("channel").add(textEl("index", n)))
}

// track writes one xmeml track. For sound, ch is the channel of the stereo
// pair (1 left, 2 right); a mono file plays on both at -3 dB, the way
// MetaRush's renderer centres mono sound.
func (w *xmWriter) track(tl *Timeline, t *Track, index, ch int, mediatype string) (*node, error) {
	tn := el("track")
	segs := layoutTrack(t, tl.FPS, w.notes, false)
	clipIndex := 0
	for i, s := range segs {
		switch s.kind {
		case segTrans:
			tn.add(w.transition(s, t.Kind))
		case segItem:
			clipIndex++
			var head, tail *segment
			if i > 0 && segs[i-1].kind == segTrans && segs[i-1].after > 0 {
				head = &segs[i-1]
			}
			if i+1 < len(segs) && segs[i+1].kind == segTrans && segs[i+1].before > 0 {
				tail = &segs[i+1]
			}
			ci, err := w.clipitem(tl, s, head, tail, mediatype, ch)
			if err != nil {
				return nil, err
			}
			w.refs = append(w.refs, xmRef{node: ci, link: s.item.Link, id: ci.attrs[0][1], mediatype: mediatype, track: index, clipIndex: clipIndex, group: groupOf(ch)})
			tn.add(ci)
		}
	}
	return tn.add(textEl("enabled", "TRUE"), textEl("locked", "FALSE")), nil
}

func groupOf(ch int) int {
	if ch == 0 {
		return 0
	}
	return 1
}

func (w *xmWriter) clipitem(tl *Timeline, s segment, head, tail *segment, mediatype string, ch int) (*node, error) {
	fps := tl.FPS
	it := s.item
	w.clipN++
	id := fmt.Sprintf("clipitem-%d", w.clipN)
	ci := el("clipitem", "id", id)
	start, end := strconv.Itoa(s.start), strconv.Itoa(s.end())
	in := s.source
	out := s.source + float64(s.duration)*it.Speed
	if head != nil {
		start = "-1"
		in -= float64(head.before) * it.Speed
	}
	if tail != nil {
		end = "-1"
		out += float64(tail.after) * it.Speed
	}
	ci.add(textEl("name", cleanText(it.Name)), textEl("enabled", "TRUE"))
	if it.Nested != nil {
		inner, err := w.sequence(it.Nested)
		if err != nil {
			return nil, err
		}
		ci.add(textEl("duration", strconv.Itoa(it.Nested.Duration)), xmRate(float64(fps)), textEl("start", start), textEl("end", end),
			textEl("in", strconv.Itoa(int(math.Round(in)))), textEl("out", strconv.Itoa(int(math.Round(out)))), inner)
		return ci, nil
	}
	m := it.Media
	if m == nil {
		return nil, fmt.Errorf("the clip “%s” has no footage file", it.Name)
	}
	ci.add(textEl("duration", strconv.Itoa(int(math.Round(m.Duration*float64(fps))))), xmRate(float64(fps)),
		textEl("start", start), textEl("end", end), textEl("in", strconv.Itoa(int(math.Round(in)))), textEl("out", strconv.Itoa(int(math.Round(out)))))
	if mediatype == "video" {
		ci.add(textEl("pixelaspectratio", "square"))
	}
	ci.add(w.file(m, fps))
	if mediatype == "audio" {
		src := ch
		extra := 0.0
		if m.Channels == 1 {
			src, extra = 1, -3.0103 // a mono file on both sides of the pair
		}
		ci.add(el("sourcetrack").add(textEl("mediatype", "audio"), textEl("trackindex", strconv.Itoa(src))))
		ci.add(levelsFilter(it, s, extra))
	} else if it.Speed != 1 {
		ci.add(timeRemap(it.Speed))
	}
	return ci, nil
}

func (w *xmWriter) file(m *Media, fps int) *node {
	id, ok := w.files[m.ID]
	if !ok {
		id = fmt.Sprintf("file-%d", len(w.files)+1)
		w.files[m.ID] = id
	}
	if w.written[id] {
		return el("file", "id", id)
	}
	w.written[id] = true
	rate := float64(fps)
	if m.HasVideo && m.FPS > 0 {
		rate = m.FPS
	}
	f := el("file", "id", id).add(textEl("name", cleanText(m.Name)), textEl("pathurl", PathURL(m.Path)), xmRate(rate),
		textEl("duration", strconv.Itoa(int(math.Round(m.Duration*rate)))), xmTimecode(rate))
	// MetaRush's own note: which clip it is (D304).
	for _, kv := range metaRushNote(m) {
		f.add(el("metadata").add(textEl("key", kv[0]), textEl("type", "UTF-8"), textEl("value", kv[1])))
	}
	media := el("media")
	if m.HasVideo {
		media.add(el("video").add(el("samplecharacteristics").add(xmRate(rate), textEl("width", strconv.Itoa(m.Width)), textEl("height", strconv.Itoa(m.Height)),
			textEl("anamorphic", "FALSE"), textEl("pixelaspectratio", "square"), textEl("fielddominance", "none"))))
	}
	if m.HasAudio {
		sr, chs := m.SampleRate, m.Channels
		if sr <= 0 {
			sr = 48000
		}
		if chs <= 0 {
			chs = 2
		}
		media.add(el("audio").add(el("samplecharacteristics").add(textEl("depth", "16"), textEl("samplerate", strconv.Itoa(sr))),
			textEl("channelcount", strconv.Itoa(chs))))
	}
	return f.add(media)
}

// levelsFilter is the Audio Levels filter: the level as linear gain, and
// keyframes at media frames (the frames in and out count) where it changes.
func levelsFilter(it *Item, s segment, extraDB float64) *node {
	param := el("parameter").add(textEl("parameterid", "level"), textEl("name", "Level"), textEl("valuemin", "0"), textEl("valuemax", "3.98109"),
		textEl("value", gainString(it.LevelDB+extraDB)))
	shift := s.start - it.Start // keys are relative to MetaRush's own window
	for _, k := range it.Keys {
		f := k.Frame - shift
		if f < 0 || f > s.duration {
			continue
		}
		media := s.source + float64(f)*it.Speed
		param.add(el("keyframe").add(textEl("when", strconv.Itoa(int(math.Round(media)))), textEl("value", gainString(k.DB+extraDB))))
	}
	eff := el("effect").add(textEl("name", "Audio Levels"), textEl("effectid", "audiolevels"), textEl("effectcategory", "audiolevels"),
		textEl("effecttype", "audiolevels"), textEl("mediatype", "audio"), param)
	return el("filter").add(eff)
}

func gainString(db float64) string {
	g := Gain(db)
	return strconv.FormatFloat(math.Round(g*1e5)/1e5, 'f', -1, 64)
}

func timeRemap(speed float64) *node {
	p := func(id, v string) *node {
		return el("parameter").add(textEl("parameterid", id), textEl("name", id), textEl("value", v))
	}
	eff := el("effect").add(textEl("name", "Time Remap"), textEl("effectid", "timeremap"), textEl("effectcategory", "motion"),
		textEl("effecttype", "motion"), textEl("mediatype", "video"),
		p("variablespeed", "0"), p("speed", strconv.FormatFloat(math.Round(speed*10000)/100, 'f', -1, 64)), p("reverse", "FALSE"), p("frameblending", "FALSE"))
	return el("filter").add(eff)
}

func (w *xmWriter) transition(s segment, trackKind string) *node {
	before, after := s.before, s.after
	align := "center"
	switch {
	case s.trans == TransFadeIn:
		align = "start-black"
	case s.trans == TransFadeOut:
		align = "end-black"
	case before-after > 1 || after-before > 1:
		// A centred transition must be symmetric (readers put the edit point
		// in its middle): keep the shorter side on both.
		short := min(before, after)
		*w.notes = append(*w.notes, fmt.Sprintf("the %s at %s sits evenly over the cut and is %d frame(s) shorter in Final Cut Pro 7 XML",
			transitionName(s.trans, trackKind), Clock(s.cut, w.fps), before+after-2*short))
		before, after = short, short
	}
	tr := el("transitionitem").add(textEl("start", strconv.Itoa(s.cut-before)), textEl("end", strconv.Itoa(s.cut+after)), textEl("alignment", align), xmRate(float64(w.fps)))
	var eff *node
	if trackKind == KindAudio {
		eff = el("effect").add(textEl("name", "Cross Fade (+3dB)"), textEl("effectid", "KGAudioTransCrossFade3dB"), textEl("effecttype", "transition"), textEl("mediatype", "audio"))
	} else if s.trans == TransDipBlack || s.trans == TransDipWhite {
		eff = el("effect").add(textEl("name", "Dip to Color Dissolve"), textEl("effectid", "Dip to Color Dissolve"), textEl("effectcategory", "Dissolve"),
			textEl("effecttype", "transition"), textEl("mediatype", "video"))
	} else {
		eff = el("effect").add(textEl("name", "Cross Dissolve"), textEl("effectid", "Cross Dissolve"), textEl("effectcategory", "Dissolve"),
			textEl("effecttype", "transition"), textEl("mediatype", "video"), textEl("wipecode", "0"), textEl("wipeaccuracy", "100"),
			textEl("startratio", "0"), textEl("endratio", "1"), textEl("reverse", "FALSE"))
	}
	return tr.add(eff)
}

func xmemlNotes(tl *Timeline) []string {
	var out []string
	for _, t := range tl.Video {
		for i, c := range t.Cuts {
			if i == 0 {
				continue
			}
			switch c.Kind {
			case TransWipe, TransSlide, TransPush, TransZoom, TransWhip:
				out = append(out, fmt.Sprintf("the %s at %s is a Cross Dissolve of the same length in Final Cut Pro 7 XML", plainTransition(c.Kind), Clock(t.Items[i].Start, tl.FPS)))
			case TransDipWhite:
				out = append(out, fmt.Sprintf("the fade through white at %s is a Dip to Color Dissolve: pick white in the editing app", Clock(t.Items[i].Start, tl.FPS)))
			}
		}
	}
	return append(out, commonNotes(tl)...)
}

// addLinks links each clip's picture and sound clipitems (Final Cut Pro 7
// and Premiere move and trim linked items together), the way Final Cut Pro 7
// writes them: every member lists every member, itself included.
func (w *xmWriter) addLinks() {
	groups := map[string][]xmRef{}
	for _, r := range w.refs {
		if r.link != "" {
			groups[r.link] = append(groups[r.link], r)
		}
	}
	for _, r := range w.refs {
		g := groups[r.link]
		if len(g) < 2 {
			continue
		}
		for _, m := range g {
			l := el("link").add(textEl("linkclipref", m.id), textEl("mediatype", m.mediatype), textEl("trackindex", strconv.Itoa(m.track)), textEl("clipindex", strconv.Itoa(m.clipIndex)))
			if m.mediatype == "audio" {
				l.add(textEl("groupindex", strconv.Itoa(m.group)))
			}
			r.node.add(l)
		}
	}
}
