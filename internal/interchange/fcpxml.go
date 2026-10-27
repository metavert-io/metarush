package interchange

import (
	"fmt"
	"math"
	"strconv"
)

// FCPXML 1.8 (Final Cut Pro 10.4 and later, DaVinci Resolve). Pictures are the
// primary storyline; each clip's own sound is its audio component with split
// edits as audioStart/audioDuration, levels and fades on adjust-volume;
// narration, music and effects are connected clips below it; overlapping
// pictures get a Cross Dissolve centred on the edit point; fades and fades
// through black are opacity keyframes (exact, and they need no extra footage).

// FCPXMLVersion is the FCPXML version written (checked against its DTD).
const FCPXMLVersion = "1.8"

// Final Cut Pro's own identifiers for its built-in Cross Dissolve.
const fcpCrossDissolveUID = "FxPlug:4731E73A-8DAC-4113-9A30-AE85B1761265"

type fcpGrid struct{ num, den int64 } // a frame lasts num/den seconds

func gridFor(fps float64) fcpGrid {
	near := func(a, b float64) bool { return math.Abs(a-b) < 0.005 }
	switch {
	case near(fps, 24000.0/1001):
		return fcpGrid{1001, 24000}
	case near(fps, 30000.0/1001):
		return fcpGrid{1001, 30000}
	case near(fps, 60000.0/1001):
		return fcpGrid{1001, 60000}
	}
	if r := math.Round(fps); r > 0 && near(fps, r) {
		return fcpGrid{1, int64(r)}
	}
	if fps <= 0 {
		return fcpGrid{1, 30}
	}
	return fcpGrid{100, int64(math.Round(fps * 100))}
}

// rational formats num/den seconds as FCPXML time ("3s", "1001/30000s").
func rational(num, den int64) string {
	if num == 0 {
		return "0s"
	}
	g := gcd(abs64(num), den)
	num, den = num/g, den/g
	if den == 1 {
		return strconv.FormatInt(num, 10) + "s"
	}
	return strconv.FormatInt(num, 10) + "/" + strconv.FormatInt(den, 10) + "s"
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

func abs64(a int64) int64 {
	if a < 0 {
		return -a
	}
	return a
}

// fcpTime is timeline frames as FCPXML time.
func fcpTime(frames, fps int) string { return rational(int64(frames), int64(fps)) }

// onGrid snaps seconds to a frame grid: the time and the rounding (seconds).
func onGrid(sec float64, g fcpGrid) (string, float64) {
	frames := math.Round(sec * float64(g.den) / float64(g.num))
	snapped := frames * float64(g.num) / float64(g.den)
	return rational(int64(frames)*g.num, g.den), snapped - sec
}

// secTime is seconds as FCPXML time at a fine (1/48000 s) resolution.
func secTime(sec float64) string { return rational(int64(math.Round(sec*48000)), 48000) }

type fcpWriter struct {
	fps     int
	nextID  int
	formats []*node
	assets  []*node
	effects []*node
	media   []*node
	fmtKey  map[string]string
	assetID map[string]string
	cross   string // effect ID of the Cross Dissolve ("" until used)
	notes   *[]string
}

func (w *fcpWriter) id() string { w.nextID++; return fmt.Sprintf("r%d", w.nextID) }

func (w *fcpWriter) format(g fcpGrid, width, height int) string {
	key := fmt.Sprintf("%d/%d %dx%d", g.num, g.den, width, height)
	if id := w.fmtKey[key]; id != "" {
		return id
	}
	id := w.id()
	n := el("format", "id", id, "frameDuration", rational(g.num, g.den))
	if width > 0 && height > 0 {
		n.set("width", strconv.Itoa(width)).set("height", strconv.Itoa(height))
	}
	w.formats = append(w.formats, n)
	w.fmtKey[key] = id
	return id
}

func (w *fcpWriter) asset(m *Media) string {
	if id := w.assetID[m.ID]; id != "" {
		return id
	}
	id := w.id()
	n := el("asset", "id", id, "name", cleanText(mediaTitle(m)), "src", FileURL(m.Path), "start", "0s")
	if m.HasVideo {
		g := gridFor(m.FPS)
		frames := int64(math.Floor(m.Duration*float64(g.den)/float64(g.num) + 1e-6))
		n.set("duration", rational(frames*g.num, g.den)).set("hasVideo", "1").set("format", w.format(g, m.Width, m.Height))
	} else {
		n.set("duration", secTime(m.Duration)).set("hasVideo", "0")
	}
	if m.HasAudio {
		ch, rate := m.Channels, m.SampleRate
		if ch <= 0 {
			ch = 2
		}
		if rate <= 0 {
			rate = 48000
		}
		n.set("hasAudio", "1").set("audioSources", "1").set("audioChannels", strconv.Itoa(ch)).set("audioRate", strconv.Itoa(rate))
	} else {
		n.set("hasAudio", "0")
	}
	if md := metaRushNote(m); len(md) > 0 {
		// MetaRush's own note: which clip it is, so a timeline coming back
		// finds it without opening the file (D304).
		meta := el("metadata")
		for _, kv := range md {
			meta.add(el("md", "key", kv[0], "value", kv[1]))
		}
		n.add(meta)
	}
	w.assets = append(w.assets, n)
	w.assetID[m.ID] = id
	return id
}

func (w *fcpWriter) crossDissolve() string {
	if w.cross == "" {
		w.cross = w.id()
		w.effects = append(w.effects, el("effect", "id", w.cross, "name", "Cross Dissolve", "uid", fcpCrossDissolveUID))
	}
	return w.cross
}

// WriteFCPXML writes the timeline as FCPXML 1.8.
func WriteFCPXML(tl *Timeline, meta map[string]any) ([]byte, []string, error) {
	notes := append([]string(nil), tl.Notes...)
	w := &fcpWriter{fps: tl.FPS, fmtKey: map[string]string{}, assetID: map[string]string{}, notes: &notes}
	seq, err := w.sequence(tl)
	if err != nil {
		return nil, nil, err
	}
	res := el("resources")
	for _, group := range [][]*node{w.formats, w.assets, w.effects, w.media} {
		res.add(group...)
	}
	project := el("project", "name", cleanText(tl.Name)).add(seq)
	root := el("fcpxml", "version", FCPXMLVersion).add(res, el("library").add(el("event", "name", "MetaRush").add(project)))
	notes = append(notes, fcpxmlNotes(tl)...)
	return document(root, "fcpxml"), dedupe(notes), nil
}

// fcpPlaced is a primary-storyline element with what gets attached to it.
type fcpPlaced struct {
	node       *node
	offset     int     // timeline frame
	duration   int     // frames
	localStart float64 // the element's local time at offset (seconds)
	anchored   []*node
	markers    []*node
	tail       []*node // filters after markers
	detached   *Item   // its sound plays another file (a clearer voice, D88): a connected clip
}

func (w *fcpWriter) sequence(tl *Timeline) (*node, error) {
	fps := tl.FPS
	seqFmt := w.format(fcpGrid{1, int64(fps)}, tl.Width, tl.Height)
	seq := el("sequence", "format", seqFmt, "duration", fcpTime(tl.Duration, fps), "tcStart", "0s", "tcFormat", "NDF", "audioLayout", "stereo", "audioRate", "48k")
	spine := el("spine")
	if len(tl.Video) == 0 {
		return seq.add(spine), nil
	}
	sounds := map[string]*Item{}
	for _, t := range tl.Audio {
		if t.Role == RoleClipSound {
			for _, it := range t.Items {
				sounds[it.Link] = it
			}
		}
	}
	pic := tl.Video[0]
	var placed []*fcpPlaced
	var children []*node
	var detached []*Item
	for _, s := range layoutTrack(pic, fps, w.notes, true) {
		switch s.kind {
		case segGap:
			p := &fcpPlaced{node: el("gap", "name", "Gap", "offset", fcpTime(s.start, fps), "duration", fcpTime(s.duration, fps)), offset: s.start, duration: s.duration}
			placed = append(placed, p)
			children = append(children, p.node)
		case segTrans:
			// Overlapping pictures only (overlapsOnly): a Cross Dissolve on the edit point.
			tr := el("transition", "name", "Cross Dissolve", "offset", fcpTime(s.cut-s.before, fps), "duration", fcpTime(s.before+s.after, fps))
			tr.add(el("filter-video", "ref", w.crossDissolve(), "name", "Cross Dissolve"))
			children = append(children, tr)
		case segItem:
			p, err := w.pictureClip(tl, pic, s, sounds)
			if err != nil {
				return nil, err
			}
			placed = append(placed, p)
			children = append(children, p.node)
			if p.detached != nil {
				detached = append(detached, p.detached)
			}
		}
	}
	// Clip sounds that play another file (clearer voices): connected clips in
	// the first lane below, at their own timing.
	lane := 0
	if len(detached) > 0 {
		lane--
		for _, it := range detached {
			p := parentAt(placed, it.Start)
			if p == nil {
				continue
			}
			c, err := w.audioClip(it, p, lane)
			if err != nil {
				return nil, err
			}
			p.anchored = append(p.anchored, c)
		}
	}
	// Narration, music, effects and added dialogue: connected clips below the storyline.
	for _, t := range tl.Audio {
		if t.Role == RoleClipSound {
			continue
		}
		lane--
		for _, it := range t.Items {
			p := parentAt(placed, it.Start)
			if p == nil {
				continue
			}
			c, err := w.audioClip(it, p, lane)
			if err != nil {
				return nil, err
			}
			p.anchored = append(p.anchored, c)
		}
	}
	for _, m := range tl.Markers {
		p := parentAt(placed, m.Start)
		if p == nil {
			continue
		}
		mk := el("marker", "start", secTime(p.localStart+float64(m.Start-p.offset)/float64(fps)), "duration", fcpTime(max(1, m.Duration), fps), "value", cleanText(m.Name))
		if m.Comment != "" {
			mk.set("note", cleanText(m.Comment))
		}
		p.markers = append(p.markers, mk)
	}
	for _, p := range placed {
		p.node.add(p.anchored...)
		p.node.add(p.markers...)
		p.node.add(p.tail...)
	}
	spine.add(children...)
	return seq.add(spine), nil
}

// parentAt is the storyline element playing at a timeline frame (the last one
// for frames past the end).
func parentAt(placed []*fcpPlaced, f int) *fcpPlaced {
	for _, p := range placed {
		if f >= p.offset && f < p.offset+p.duration {
			return p
		}
	}
	if len(placed) > 0 && f >= placed[len(placed)-1].offset {
		return placed[len(placed)-1]
	}
	return nil
}

func (w *fcpWriter) pictureClip(tl *Timeline, pic *Track, s segment, sounds map[string]*Item) (*fcpPlaced, error) {
	fps := tl.FPS
	it := s.item
	p := &fcpPlaced{offset: s.start, duration: s.duration}
	if it.Nested != nil {
		mid := w.id()
		inner, err := w.sequence(it.Nested)
		if err != nil {
			return nil, err
		}
		w.media = append(w.media, el("media", "id", mid, "name", cleanText(it.Name)).add(inner))
		p.localStart = s.source / float64(fps)
		p.node = el("ref-clip", "ref", mid, "offset", fcpTime(s.start, fps), "name", cleanText(it.Name), "start", secTime(p.localStart), "duration", fcpTime(s.duration, fps))
		return p, nil
	}
	m := it.Media
	if m == nil {
		return nil, fmt.Errorf("the clip “%s” has no footage file", it.Name)
	}
	ref := w.asset(m)
	g := gridFor(m.FPS)
	srcSec := s.source / float64(fps)
	sp := it.Speed
	local := func(mediaSec float64) float64 { return mediaSec / sp } // local (retimed) clip time
	startStr, delta := onGrid(local(srcSec), g)
	p.localStart = local(srcSec) + delta
	n := el("asset-clip", "ref", ref, "offset", fcpTime(s.start, fps), "name", cleanText(it.Name), "start", startStr, "duration", fcpTime(s.duration, fps), "tcFormat", "NDF")
	if sp != 1 {
		end := m.Duration
		n.add(el("timeMap").add(
			el("timept", "time", "0s", "value", "0s", "interp", "linear"),
			el("timept", "time", secTime(end/sp), "value", secTime(end), "interp", "linear")))
	}
	if f := it.Framing; f != nil && f.Mode == "fill" && m.Width > 0 && m.Height > 0 && tl.Width > 0 && tl.Height > 0 &&
		math.Abs(float64(m.Width)/float64(m.Height)-float64(tl.Width)/float64(tl.Height)) > 0.01 {
		n.add(el("adjust-conform", "type", "fill"))
	}
	// Fades from and to black: the clip's own, and each half of a fade through black.
	fadeIn, fadeOut := it.FadeIn, it.FadeOut
	idx := indexOf(pic.Items, it)
	if idx >= 0 {
		if c := pic.Cuts[idx]; c.Kind == TransDipBlack || c.Kind == TransDipWhite {
			fadeIn = max(fadeIn, c.DipIn)
		}
		if idx+1 < len(pic.Items) {
			if c := pic.Cuts[idx+1]; c.Kind == TransDipBlack || c.Kind == TransDipWhite {
				fadeOut = max(fadeOut, c.DipOut)
			}
		}
	}
	if fadeIn > 0 || fadeOut > 0 {
		fadeIn, fadeOut = min(fadeIn, s.duration), min(fadeOut, s.duration)
		at := func(frames int) string { return secTime(p.localStart + float64(frames)/float64(fps)) }
		anim := el("keyframeAnimation")
		key := func(frames int, v string) {
			anim.add(el("keyframe", "time", at(frames), "value", v, "interp", "linear", "curve", "linear"))
		}
		if fadeIn > 0 {
			key(0, "0")
			key(fadeIn, "1")
		}
		if fadeOut > 0 {
			if fadeIn == 0 || s.duration-fadeOut > fadeIn {
				key(s.duration-fadeOut, "1")
			}
			key(s.duration, "0")
		}
		n.add(el("adjust-blend", "amount", "1").add(el("param", "name", "amount").add(anim)))
	}
	snd := sounds[it.Link]
	switch {
	case snd != nil && snd.Media != nil && snd.Media.ID != m.ID:
		// The clip's sound plays another file (a clearer voice): the picture
		// plays muted and the sound is a connected clip (sequence).
		if m.HasAudio {
			n.set("srcEnable", "video")
		}
		p.detached = snd
	case snd == nil && m.HasAudio:
		n.set("srcEnable", "video")
	case snd != nil:
		// The sound's own window (in the clip's local, retimed time): a split
		// edit when it differs from the picture's.
		aStart := local(snd.Source/float64(fps)) + delta
		if math.Abs(aStart-p.localStart) > 1e-9 || snd.Duration != s.duration {
			n.set("audioStart", secTime(aStart)).set("audioDuration", fcpTime(snd.Duration, fps))
		}
		n.add(volumeNode(snd, aStart, fps))
	}
	p.node = n
	return p, nil
}

func indexOf(items []*Item, it *Item) int {
	for i, x := range items {
		if x == it {
			return i
		}
	}
	return -1
}

// volumeNode is a sound's level, fades and level keys; localStart is the
// sound's first frame in the clip's local time (seconds).
func volumeNode(it *Item, localStart float64, fps int) *node {
	v := el("adjust-volume", "amount", dbString(it.LevelDB))
	if it.FadeIn == 0 && it.FadeOut == 0 && len(it.Keys) == 0 {
		return v
	}
	param := el("param", "name", "amount")
	if it.FadeIn > 0 {
		param.add(el("fadeIn", "type", "linear", "duration", fcpTime(it.FadeIn, fps)))
	}
	if it.FadeOut > 0 {
		param.add(el("fadeOut", "type", "linear", "duration", fcpTime(it.FadeOut, fps)))
	}
	if len(it.Keys) > 0 {
		anim := el("keyframeAnimation")
		for _, k := range it.Keys {
			anim.add(el("keyframe", "time", secTime(localStart+float64(k.Frame)/float64(fps)), "value", dbString(k.DB), "interp", "linear", "curve", "linear"))
		}
		param.add(anim)
	}
	return v.add(param)
}

func dbString(db float64) string {
	if db <= Silent {
		return fmt.Sprintf("%gdB", Silent)
	}
	return strconv.FormatFloat(round1(db), 'f', -1, 64) + "dB"
}

// audioClip is a connected sound below the storyline.
func (w *fcpWriter) audioClip(it *Item, p *fcpPlaced, lane int) (*node, error) {
	fps := w.fps
	m := it.Media
	if m == nil {
		return nil, fmt.Errorf("the sound “%s” has no file", it.Name)
	}
	ref := w.asset(m)
	offLocal := p.localStart + float64(it.Start-p.offset)/float64(fps)
	srcSec := it.Source / float64(fps)
	if it.Speed != 1 {
		srcSec /= it.Speed
	}
	role := "dialogue"
	switch it.Role {
	case RoleMusic:
		role = "music"
	case RoleEffects:
		role = "effects"
	}
	n := el("asset-clip", "ref", ref, "lane", strconv.Itoa(lane), "offset", secTime(offLocal), "name", cleanText(it.Name), "start", secTime(srcSec),
		"duration", fcpTime(it.Duration, fps), "audioRole", role)
	if m.HasVideo {
		n.set("srcEnable", "audio")
	}
	if it.Speed != 1 {
		n.add(el("timeMap").add(
			el("timept", "time", "0s", "value", "0s", "interp", "linear"),
			el("timept", "time", secTime(m.Duration/it.Speed), "value", secTime(m.Duration), "interp", "linear")))
	}
	n.add(volumeNode(it, srcSec, fps))
	return n, nil
}

func fcpxmlNotes(tl *Timeline) []string {
	var out []string
	for _, t := range tl.Video {
		for i, c := range t.Cuts {
			if i == 0 {
				continue
			}
			switch c.Kind {
			case TransWipe, TransSlide, TransPush, TransZoom, TransWhip:
				out = append(out, fmt.Sprintf("the %s at %s is a Cross Dissolve of the same length in Final Cut Pro", plainTransition(c.Kind), Clock(t.Items[i].Start, tl.FPS)))
			case TransDipWhite:
				out = append(out, fmt.Sprintf("the fade through white at %s fades through black in Final Cut Pro", Clock(t.Items[i].Start, tl.FPS)))
			}
		}
	}
	return append(out, commonNotes(tl)...)
}
