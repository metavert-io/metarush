package interchange

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Reading Final Cut Pro 7 XML ("xmeml", versions 4 and 5). A sequence has
// video and audio tracks of clipitems, transitionitems and generatoritems.
// start/end are sequence frames (−1 where a transition covers the edit, which
// the transition's alignment places); in/out are frames of the clip's own
// rate, extended into a transition's handles. A file is described once and
// named by its id afterwards. Nested sequences are opened up into the
// timeline's own lanes.

const premiereTicks = 254016000000 // per second (cutPointTicks)

type xmReader struct {
	r     *Read
	files map[string]*xnode
	seqs  map[string]*xnode
	depth int
	// open is the sequences being read (one inside itself is refused).
	open map[*xnode]bool
}

func xmRateOf(n *xnode) float64 {
	if n == nil {
		return 0
	}
	rate := n.child("rate")
	if rate == nil {
		if n.name == "rate" {
			rate = n
		} else {
			return 0
		}
	}
	tb, _ := number(rate.textOf("timebase"))
	if tb <= 0 || tb > 1000 {
		return 0
	}
	if strings.EqualFold(rate.textOf("ntsc"), "TRUE") {
		return tb * 1000 / 1001
	}
	return tb
}

func xmInt(n *xnode, p string) (float64, bool) {
	s := n.textOf(p)
	if s == "" {
		return 0, false
	}
	return number(s)
}

func readXMEML(data []byte, o ReadOptions) (*Read, error) {
	root, err := parseXML(data)
	if err != nil {
		return nil, readErr(err, "This Final Cut Pro 7 XML file can't be read: %s.", err.Error())
	}
	r := newRead(FormatXMEML, strings.TrimSpace("Final Cut Pro 7 XML "+root.attr("version")))
	rd := &xmReader{r: r, files: map[string]*xnode{}, seqs: map[string]*xnode{}, open: map[*xnode]bool{}}
	var top []*xnode
	var walk func(n *xnode, inClip bool)
	walk = func(n *xnode, inClip bool) {
		for _, c := range n.children {
			switch c.name {
			case "file":
				if c.child("pathurl") != nil || c.child("name") != nil || c.child("media") != nil {
					if _, ok := rd.files[c.attr("id")]; !ok {
						rd.files[c.attr("id")] = c
					}
				}
			case "sequence":
				if len(c.children) > 0 {
					rd.seqs[c.attr("id")] = c
					if !inClip {
						top = append(top, c)
					}
				}
			}
			walk(c, inClip || c.name == "clipitem")
		}
	}
	walk(root, false)
	if root.name == "sequence" {
		top = append([]*xnode{root}, top...)
	}
	if len(top) == 0 {
		return nil, &ReadError{Msg: "This Final Cut Pro 7 XML file holds clips but no sequence (timeline).", Hint: "Export the sequence itself from the editing app.", Err: ErrNotTimeline}
	}
	seq := top[0]
	for _, s := range top {
		r.Timelines = append(r.Timelines, s.textOf("name"))
		if o.Timeline != "" && strings.EqualFold(s.textOf("name"), o.Timeline) {
			seq = s
		}
	}
	r.Name = seq.textOf("name")
	r.FPS = xmRateOf(seq)
	if r.FPS <= 0 {
		r.FPS = 30
	}
	if f, ok := xmInt(seq, "timecode/frame"); ok {
		r.Start = f / r.FPS
	}
	if d, ok := xmInt(seq, "duration"); ok {
		r.Length = d / r.FPS
	}
	if sc := seq.path("media/video/format/samplecharacteristics"); sc != nil {
		r.Width, _ = strconv.Atoi(sc.textOf("width"))
		r.Height, _ = strconv.Atoi(sc.textOf("height"))
	}
	rd.sequence(seq, func(t float64) float64 { return t }, 0, math.Inf(1), 0, "")
	if r.over {
		return r, nil
	}
	sort.SliceStable(r.Video, func(i, j int) bool { return r.Video[i].z < r.Video[j].z })
	for _, m := range seq.all("marker") {
		in, _ := xmInt(m, "in")
		out, ok := xmInt(m, "out")
		mk := ReadMarker{At: in / r.FPS, Name: m.textOf("name"), Note: m.textOf("comment")}
		if ok && out > in {
			mk.Length = (out - in) / r.FPS
		}
		mk.Chapter = isChapterName(mk.Name) || strings.Contains(strings.ToLower(m.textOf("comment")), "chapter")
		r.Markers = append(r.Markers, mk)
	}
	return r, nil
}

// sequence reads a sequence's tracks: toTL maps the sequence's seconds to the
// timeline, lo/hi keep a window (nested sequences), z stacks its lanes.
func (rd *xmReader) sequence(seq *xnode, toTL func(float64) float64, lo, hi, z float64, use string) {
	if rd.depth > 16 {
		rd.r.unread("sequences nested more than 16 deep")
		return
	}
	if rd.open[seq] {
		rd.r.unread("a nested sequence that contains itself (“%s”)", seq.textOf("name"))
		return
	}
	if !rd.r.spend(1) || !rd.r.visit(seq.size) { // its whole contents, every time it is opened
		return
	}
	rd.open[seq] = true
	rd.depth++
	defer func() { rd.depth--; delete(rd.open, seq) }()
	fps := xmRateOf(seq)
	if fps <= 0 {
		fps = rd.r.FPS
	}
	media := seq.child("media")
	if media == nil {
		return
	}
	if v := media.child("video"); v != nil && use != "audio" {
		for i, t := range v.all("track") {
			lane := &Lane{Name: fmt.Sprintf("V%d", i+1), Kind: KindVideo, Off: strings.EqualFold(t.textOf("enabled"), "FALSE"), z: z + float64(i)*rd.step()}
			rd.track(t, lane, fps, toTL, lo, hi)
			rd.r.Video = append(rd.r.Video, lane)
		}
	}
	if a := media.child("audio"); a != nil && use != "video" {
		for i, t := range a.all("track") {
			lane := &Lane{Name: fmt.Sprintf("A%d", i+1), Kind: KindAudio, Off: strings.EqualFold(t.textOf("enabled"), "FALSE"), z: z + float64(i)*rd.step()}
			rd.track(t, lane, fps, toTL, lo, hi)
			rd.r.Audio = append(rd.r.Audio, lane)
		}
	}
}

// step is how far apart lanes stack at this depth (nested lanes sit between
// the outer lane they play on and the next).
func (rd *xmReader) step() float64 { return math.Pow(0.01, float64(rd.depth-1)) }

type xmItem struct {
	n          *xnode
	kind       string // clipitem | transitionitem | generatoritem
	start, end float64
}

// track reads one track's items in order, placing those under transitions.
func (rd *xmReader) track(t *xnode, lane *Lane, fps float64, toTL func(float64) float64, lo, hi float64) {
	var items []xmItem
	if !rd.r.spend(1) {
		return
	}
	for _, c := range t.children {
		switch c.name {
		case "clipitem", "transitionitem", "generatoritem":
			if !rd.r.spend(1) {
				return
			}
			s, _ := xmInt(c, "start")
			e, _ := xmInt(c, "end")
			items = append(items, xmItem{n: c, kind: c.name, start: s, end: e})
		}
	}
	// Edit points of transitions: their alignment, or Premiere's cut point.
	edits := make([]float64, len(items))
	for i, it := range items {
		if it.kind != "transitionitem" {
			continue
		}
		edits[i] = xmEdit(it)
	}
	for i, it := range items {
		switch it.kind {
		case "transitionitem":
			name := it.n.textOf("effect/name")
			edit := edits[i]
			j := Join{At: toTL(edit / fps), Before: (edit - it.start) / fps, After: (it.end - edit) / fps, Name: name, Kind: joinKind(it.n.textOf("effect/effectid"), name)}
			align := strings.ToLower(it.n.textOf("alignment"))
			switch {
			case lane.Kind == KindAudio && strings.HasPrefix(align, "start"):
				j.Kind = TransFadeIn
			case lane.Kind == KindAudio && strings.HasPrefix(align, "end"):
				j.Kind = TransFadeOut
			case lane.Kind == KindAudio:
				j.Kind = TransAudioCrossfade
			case align == "start-black" || (i == 0 || items[i-1].kind == "transitionitem") && align == "start":
				j.Kind = TransFadeIn
			case align == "end-black" || (i == len(items)-1 || items[i+1].kind == "transitionitem") && align == "end":
				j.Kind = TransFadeOut
			}
			if j.At >= lo-1e-6 && j.At <= hi+1e-6 {
				lane.Joins = append(lane.Joins, j)
			}
		case "clipitem", "generatoritem":
			start, end := it.start, it.end
			var head float64 // frames of the item before its start that a transition shows
			if start < 0 && i > 0 && items[i-1].kind == "transitionitem" {
				start = edits[i-1]
				head = edits[i-1] - items[i-1].start
			}
			if end < 0 && i+1 < len(items) && items[i+1].kind == "transitionitem" {
				end = edits[i+1]
			}
			if start < 0 || end <= start {
				continue
			}
			// The timeline span the in and out points cover (transitions' handles included).
			extStart, extEnd := it.start, it.end
			if extStart < 0 && i > 0 && items[i-1].kind == "transitionitem" {
				extStart = items[i-1].start
			}
			if extEnd < 0 && i+1 < len(items) && items[i+1].kind == "transitionitem" {
				extEnd = items[i+1].end
			}
			if it.kind == "generatoritem" {
				rd.generator(it.n, lane, toTL(start/fps), (end-start)/fps)
				continue
			}
			rd.clipitem(it.n, lane, fps, toTL, start, end, head, (extEnd-extStart)/fps, lo, hi)
		}
	}
}

// xmEdit is a transition's edit point in sequence frames.
func xmEdit(it xmItem) float64 {
	if ticks, ok := xmInt(it.n, "cutPointTicks"); ok {
		rate := xmRateOf(it.n)
		if rate > 0 {
			return it.start + math.Round(ticks/premiereTicks*rate)
		}
	}
	switch strings.ToLower(it.n.textOf("alignment")) {
	case "start", "start-black":
		return it.start
	case "end", "end-black":
		return it.end
	}
	return math.Round((it.start + it.end) / 2)
}

func (rd *xmReader) generator(n *xnode, lane *Lane, at, length float64) {
	name := firstNonEmptyStr(n.textOf("name"), n.textOf("effect/name"), "Generator")
	p := &Piece{Kind: PieceGenerator, Name: name, At: at, Length: length, Speed: 1, Off: strings.EqualFold(n.textOf("enabled"), "FALSE")}
	for _, param := range n.path("effect").all("parameter") {
		if id := param.textOf("parameterid"); id == "str" || id == "text" {
			p.Kind, p.Text = PieceTitle, clip(strings.TrimSpace(param.textOf("value")), maxTitleRunes)
		}
	}
	lane.Pieces = append(lane.Pieces, p)
}

// clipitem reads a clip at sequence frames start–end (head: frames of it
// before its start that a transition shows).
func (rd *xmReader) clipitem(n *xnode, lane *Lane, seqFPS float64, toTL func(float64) float64, start, end, head, span, lo, hi float64) {
	name := n.textOf("name")
	rate := xmRateOf(n)
	if rate <= 0 {
		rate = seqFPS
	}
	in, _ := xmInt(n, "in")
	speed := 1.0
	remapped := false
	for _, f := range n.all("filter") {
		eff := f.child("effect")
		if eff == nil {
			continue
		}
		id := strings.ToLower(eff.textOf("effectid") + " " + eff.textOf("name"))
		switch {
		case strings.Contains(id, "timeremap") || strings.Contains(id, "time remap"):
			remapped = true
			for _, p := range eff.all("parameter") {
				switch p.textOf("parameterid") {
				case "speed":
					if v, ok := number(p.textOf("value")); ok && v != 0 && math.Abs(v) <= 100000 {
						speed = v / 100
					}
				case "reverse":
					if strings.EqualFold(p.textOf("value"), "TRUE") {
						rd.r.unread("“%s” plays backwards", name)
					}
				case "variablespeed":
					if p.textOf("value") == "1" {
						rd.r.unread("the speed ramp on “%s” (read as one speed)", name)
					}
				}
			}
		case strings.Contains(id, "audiolevels") || strings.Contains(id, "audio levels"):
		case lane.Kind == KindAudio && (strings.Contains(id, "pan") || strings.Contains(id, "audiopan")):
		case lane.Kind == KindVideo && (strings.Contains(id, "basic motion") || strings.Contains(id, "basicmotion") || strings.Contains(id, "crop")):
			rd.r.unread("the position, scale or crop set on “%s”", name)
		default:
			rd.r.unread("the effect “%s” on “%s”", firstNonEmptyStr(eff.textOf("name"), "an effect"), name)
		}
	}
	if !remapped {
		// No speed filter (sound clipitems carry none): the media the in and
		// out points cover against the time they take says it.
		if out, ok := xmInt(n, "out"); ok && span > 0 && out > in {
			if ratio := (out - in) / rate / span; math.Abs(ratio-1) > 0.01 {
				speed = math.Round(ratio*10000) / 10000
			}
		}
	}
	at := toTL(start / seqFPS)
	length := (end - start) / seqFPS
	// Media seconds at the clip's first visible frame: in is where a covering
	// transition starts, head frames (sequence rate) before it.
	mediaIn := in/rate + head/seqFPS*speed
	p := &Piece{Kind: PieceClip, Name: name, At: at, Length: length, In: mediaIn, Speed: speed, Off: strings.EqualFold(n.textOf("enabled"), "FALSE")}
	if inner := n.child("sequence"); inner != nil {
		if len(inner.children) == 0 {
			inner = rd.seqs[inner.attr("id")]
		}
		if inner != nil {
			// A nested sequence: its lanes inside this clip's window.
			shift := at - mediaIn/speed
			use := ""
			if lane.Kind == KindAudio {
				use = "audio"
			} else {
				use = "video"
			}
			rd.sequence(inner, func(t float64) float64 { return shift + t/speed }, math.Max(lo, at), math.Min(hi, at+length), lane.z+0.1*rd.step(), use)
			return
		}
	}
	fileNode := n.child("file")
	if fileNode != nil {
		if full := rd.files[fileNode.attr("id")]; full != nil {
			fileNode = full
		}
	}
	if fileNode == nil {
		rd.r.unread("a clip whose file isn't described (“%s”)", name)
		return
	}
	src := rd.file(fileNode)
	p.Source = src.Key
	p.In = mediaIn
	// Linked items: the clip's picture and sound share a group.
	ids := []string{n.attr("id")}
	for _, l := range n.all("link") {
		if id := l.textOf("linkclipref"); id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	p.Group = "xmeml:" + ids[0]
	if lane.Kind == KindAudio {
		rd.levels(n, p, rate, in, head/seqFPS)
	}
	for _, m := range n.all("marker") {
		if !rd.r.spend(1) {
			return
		}
		mi, _ := xmInt(m, "in")
		mk := ReadMarker{At: at + (mi/rate-mediaIn)/speed, Name: m.textOf("name"), Note: m.textOf("comment")}
		if mo, ok := xmInt(m, "out"); ok && mo > mi {
			mk.Length = (mo - mi) / rate / speed
		}
		mk.Chapter = isChapterName(mk.Name)
		rd.r.Markers = append(rd.r.Markers, mk)
	}
	if q := trimPiece(p, lo, hi); q != nil {
		lane.Pieces = append(lane.Pieces, q)
	}
}

func (rd *xmReader) file(f *xnode) *Source {
	key := "xmeml:" + f.attr("id")
	if f.attr("id") == "" {
		key = "xmeml:" + f.textOf("pathurl") + "#" + f.textOf("name")
	}
	s := rd.r.source(key)
	if s.Name != "" {
		return s
	}
	s.Name = f.textOf("name")
	s.URL = f.textOf("pathurl")
	s.Path = fileFromURL(s.URL)
	rate := xmRateOf(f)
	if tcRate := xmRateOf(f.child("timecode")); tcRate > 0 {
		if fr, ok := xmInt(f, "timecode/frame"); ok {
			s.Start = fr / tcRate
		}
	}
	if d, ok := xmInt(f, "duration"); ok && rate > 0 {
		s.Duration = d / rate
	}
	s.FPS = rate
	for _, md := range f.all("metadata") {
		s.AssetID, s.SHA256 = noteValue(md.textOf("key"), md.textOf("value"), s.AssetID, s.SHA256)
	}
	if m := f.child("media"); m != nil {
		s.HasVideo = m.child("video") != nil
		s.HasAudio = m.child("audio") != nil
	} else {
		s.HasVideo, s.HasAudio = true, true
	}
	return s
}

// levels reads the Audio Levels filter: a level (linear gain) and keyframes
// (frames of the clip's media).
func (rd *xmReader) levels(n *xnode, p *Piece, rate, in, headSec float64) {
	for _, f := range n.all("filter") {
		eff := f.child("effect")
		if eff == nil {
			continue
		}
		id := strings.ToLower(eff.textOf("effectid") + " " + eff.textOf("name"))
		if !strings.Contains(id, "audiolevels") && !strings.Contains(id, "audio levels") {
			continue
		}
		for _, param := range eff.all("parameter") {
			if !strings.EqualFold(param.textOf("parameterid"), "level") {
				continue
			}
			if v, ok := number(param.textOf("value")); ok {
				db := round1(DB(v))
				p.Level = &db
			}
			for _, k := range param.children {
				if len(p.LevelKeys) >= maxLevelKeys {
					break
				}
				if k.name != "keyframe" {
					continue
				}
				when, ok := xmInt(k, "when")
				v, okv := number(k.textOf("value"))
				if !ok || !okv {
					continue
				}
				at := (when-in)/rate/p.Speed - headSec
				addLevelKey(p, LevelPoint{At: at, DB: round1(DB(v))})
			}
		}
	}
}
