package interchange

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Reading OpenTimelineIO JSON (.otio): a Timeline (or the first of a
// collection) whose tracks are Stack → Track → Clip / Gap / Transition /
// Stack. Times are RationalTime values at their own rates; a clip's source
// range is in its media's own time, measured from the media's available
// range. MetaRush's own exports carry its notes under "metarush" metadata
// (the clip, levels, fades, transition kinds), which round-trip.

type ojson = map[string]any

func oSchema(m ojson) string {
	s, _ := m["OTIO_SCHEMA"].(string)
	name, _, _ := strings.Cut(s, ".")
	return name
}

func oStr(m ojson, k string) string { s, _ := m[k].(string); return s }

func oNum(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case json.Number:
		f, _ := x.Float64()
		return f
	}
	return 0
}

func oMap(m ojson, k string) ojson { x, _ := m[k].(map[string]any); return x }

func oList(m ojson, k string) []any { x, _ := m[k].([]any); return x }

// oTime is a RationalTime in seconds, and its rate.
func oTime(v any) (sec, rate float64, ok bool) {
	m, _ := v.(map[string]any)
	if m == nil {
		return 0, 0, false
	}
	rate = oNum(m["rate"])
	if !(rate >= 1.0/1000 && rate <= 1e6) {
		return 0, 0, false
	}
	sec = oNum(m["value"]) / rate
	if !sane(sec) {
		return 0, 0, false
	}
	return sec, rate, true
}

// oRange is a TimeRange: start and duration in seconds, and the rate.
func oRange(v any) (start, dur, rate float64, ok bool) {
	m, _ := v.(map[string]any)
	if m == nil {
		return 0, 0, 0, false
	}
	start, rate, ok1 := oTime(m["start_time"])
	dur, r2, ok2 := oTime(m["duration"])
	if !ok1 || !ok2 {
		return 0, 0, 0, false
	}
	if rate <= 0 {
		rate = r2
	}
	return start, dur, rate, true
}

func oMeta(m ojson) map[string]any {
	md := oMap(m, "metadata")
	if md == nil {
		return nil
	}
	x, _ := md["metarush"].(map[string]any)
	return x
}

func readOTIO(data []byte, o ReadOptions) (*Read, error) {
	// Counted before anything is made of it: a small file of empty values
	// would otherwise take a lot of memory to hold.
	if n := jsonValues(data); n > maxJSONValues {
		return nil, &ReadError{Msg: fmt.Sprintf("This OpenTimelineIO file has more than %s values, which is more than a timeline has.", "a million"),
			Hint: "Check that it's the timeline itself; export a shorter part of it if it is."}
	}
	var doc ojson
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, readErr(err, "This OpenTimelineIO file can't be read: %s.", plainJSONErr(err))
	}
	r := newRead(FormatOTIO, "OpenTimelineIO")
	tl := doc
	if oSchema(doc) == "SerializableCollection" {
		var found []ojson
		for _, c := range oList(doc, "children") {
			if m, _ := c.(map[string]any); m != nil && oSchema(m) == "Timeline" {
				found = append(found, m)
			}
		}
		if len(found) == 0 {
			return nil, &ReadError{Msg: "This OpenTimelineIO file holds clips but no timeline.", Hint: "Export the timeline itself (a sequence), not a bin of clips.", Err: ErrNotTimeline}
		}
		tl = found[0]
		for _, t := range found {
			name := oStr(t, "name")
			r.Timelines = append(r.Timelines, name)
			if o.Timeline != "" && strings.EqualFold(name, o.Timeline) {
				tl = t
			}
		}
	}
	if oSchema(tl) != "Timeline" {
		return nil, &ReadError{Msg: "This OpenTimelineIO file has no timeline in it.", Err: ErrNotTimeline}
	}
	r.Name = oStr(tl, "name")
	r.MetaRush = oMeta(tl)
	if s, rate, ok := oTime(tl["global_start_time"]); ok {
		r.Start, r.FPS = s, ntscRate(rate)
	}
	if md := r.MetaRush; md != nil {
		r.Width, r.Height = int(oNum(md["width"])), int(oNum(md["height"]))
		if f := oNum(md["fps"]); f > 0 {
			r.FPS = f
		}
	}
	stack := oMap(tl, "tracks")
	if stack == nil {
		return nil, &ReadError{Msg: "This OpenTimelineIO timeline has no tracks.", Err: ErrNotTimeline}
	}
	rd := &otioReader{r: r}
	rd.stack(stack, otioWin{lo: math.Inf(-1), hi: math.Inf(1), depth: 1})
	for _, m := range oList(stack, "markers") {
		if mk, ok := rd.marker(m, 0, 0); ok {
			r.Markers = append(r.Markers, mk)
		}
	}
	sort.SliceStable(r.Video, func(i, j int) bool { return r.Video[i].z < r.Video[j].z })
	if r.FPS <= 0 {
		r.FPS = rd.rate
	}
	return r, nil
}

func plainJSONErr(err error) string {
	if se, ok := err.(*json.SyntaxError); ok {
		return fmt.Sprintf("it stops making sense at byte %d", se.Offset)
	}
	return "it isn't the JSON OpenTimelineIO writes"
}

type otioReader struct {
	r    *Read
	rate float64 // the first rate seen (timelines without a global start time)
	keys int
}

// otioWin is where a stack or track plays on the timeline: its own time 0 at
// offset, kept between lo and hi, its lanes stacked from z.
type otioWin struct {
	offset, lo, hi, z float64
	depth             int
}

func (w otioWin) step() float64 { return math.Pow(0.01, float64(w.depth-1)) }

// stack reads a Stack's children (tracks, or anything played in parallel)
// into the timeline's lanes; nested stacks are opened up in their window.
func (rd *otioReader) stack(stack ojson, w otioWin) {
	if w.depth > 16 {
		rd.r.unread("timelines nested more than 16 deep")
		return
	}
	for i, c := range oList(stack, "children") {
		t, _ := c.(map[string]any)
		if t == nil {
			continue
		}
		if !rd.r.spend(1) {
			return
		}
		cw := w
		cw.z = w.z + float64(i)*w.step()
		switch oSchema(t) {
		case "Track", "Sequence":
			rd.track(t, cw)
		case "Stack":
			cw.depth++
			rd.stack(t, cw)
		case "Clip":
			// A clip straight in a stack plays on a lane of its own.
			lane := &Lane{Name: oStr(t, "name"), Kind: KindVideo, z: cw.z}
			if p, _ := rd.clip(t, w.offset); p != nil {
				if q := trimPiece(p, w.lo, w.hi); q != nil {
					lane.Pieces = append(lane.Pieces, q)
				}
			}
			rd.r.Video = append(rd.r.Video, lane)
		default:
			rd.r.unread("a %s at the top of the timeline", strings.ToLower(oSchema(t)))
		}
	}
}

func (rd *otioReader) track(t ojson, w otioWin) {
	kind := KindVideo
	if strings.EqualFold(oStr(t, "kind"), "Audio") {
		kind = KindAudio
	}
	lane := &Lane{Name: oStr(t, "name"), Kind: kind, Off: t["enabled"] == false, z: w.z}
	if md := oMeta(t); md != nil {
		lane.Role, _ = md["role"].(string)
	}
	if len(oList(t, "effects")) > 0 {
		rd.r.unread("effects on the track “%s”", lane.Name)
	}
	for _, m := range oList(t, "markers") {
		if mk, ok := rd.marker(m, w.offset, 0); ok && mk.At >= w.lo-1e-6 && mk.At <= w.hi+1e-6 {
			rd.r.Markers = append(rd.r.Markers, mk)
		}
	}
	cursor := w.offset
	children := oList(t, "children")
	for i, c := range children {
		m, _ := c.(map[string]any)
		if m == nil {
			continue
		}
		if !rd.r.spend(1) {
			break
		}
		switch oSchema(m) {
		case "Transition":
			before, _, _ := oTime(m["in_offset"])
			after, _, _ := oTime(m["out_offset"])
			j := Join{At: cursor, Before: before, After: after, Name: oStr(m, "name")}
			j.Kind = joinKind(oStr(m, "transition_type"), j.Name)
			if md := oMeta(m); md != nil {
				if k, _ := md["kind"].(string); k != "" {
					j.Kind = k
				}
				j.Direction, _ = md["direction"].(string)
				j.Easing, _ = md["easing"].(string)
				j.From, _ = md["transition"].(string)
			}
			// A transition at either end of a track fades from or to nothing.
			if i == 0 && j.Kind == TransDissolve {
				j.Kind = TransFadeIn
			} else if i == len(children)-1 && j.Kind == TransDissolve {
				j.Kind = TransFadeOut
			}
			if j.At > w.lo-1e-6 && j.At < w.hi+1e-6 {
				lane.Joins = append(lane.Joins, j)
			}
		case "Gap":
			_, dur, _, _ := oRange(m["source_range"])
			cursor += dur
		case "Clip":
			p, dur := rd.clip(m, cursor)
			if p != nil {
				if q := trimPiece(p, w.lo, w.hi); q != nil {
					lane.Pieces = append(lane.Pieces, q)
				}
			}
			cursor += dur
		case "Stack", "Track", "Sequence":
			// A nested timeline: opened up into the timeline's own lanes,
			// keeping the part this track plays.
			start, dur, rate, ok := oRange(m["source_range"])
			if !ok {
				dur = otioLength(m)
			}
			rd.seeRate(rate)
			if s := rd.effects(m, oStr(m, "name")); s != 1 {
				rd.r.unread("the speed change on the nested timeline “%s”", oStr(m, "name"))
			}
			if m["enabled"] == false {
				cursor += dur
				continue
			}
			nw := otioWin{offset: cursor - start, lo: math.Max(w.lo, cursor), hi: math.Min(w.hi, cursor+dur), z: w.z + 0.1*w.step(), depth: w.depth + 1}
			if oSchema(m) == "Stack" {
				rd.stack(m, nw)
			} else {
				rd.track(m, nw)
			}
			cursor += dur
		default:
			rd.r.unread("a %s on the track “%s”", strings.ToLower(oSchema(m)), lane.Name)
		}
	}
	if kind == KindVideo {
		rd.r.Video = append(rd.r.Video, lane)
	} else {
		rd.r.Audio = append(rd.r.Audio, lane)
	}
}

// otioLength is how long a stack or track without a source range lasts: its
// longest track, or the sum of its children.
func otioLength(m ojson) float64 { return otioLengthIn(m, 0) }

func otioLengthIn(m ojson, depth int) float64 {
	if depth > 32 {
		return 0
	}
	total := 0.0
	for _, c := range oList(m, "children") {
		x, _ := c.(map[string]any)
		if x == nil {
			continue
		}
		var d float64
		switch oSchema(x) {
		case "Transition":
			continue
		case "Stack", "Track", "Sequence":
			if _, dd, _, ok := oRange(x["source_range"]); ok {
				d = dd
			} else {
				d = otioLengthIn(x, depth+1)
			}
		default:
			_, d, _, _ = oRange(x["source_range"])
		}
		if oSchema(m) == "Stack" {
			total = math.Max(total, d)
		} else {
			total += d
		}
	}
	return total
}

func (rd *otioReader) seeRate(rate float64) {
	if rd.rate <= 0 && rate > 0 {
		rd.rate = ntscRate(rate)
	}
}

// clip reads a Clip at a timeline position: the piece (nil for one with no
// usable range) and how long it lasts on the track.
func (rd *otioReader) clip(m ojson, at float64) (*Piece, float64) {
	start, dur, rate, ok := oRange(m["source_range"])
	ref := oMap(m, "media_reference")
	if refs := oMap(m, "media_references"); refs != nil { // Clip.2
		key := oStr(m, "active_media_reference_key")
		if key == "" {
			key = "DEFAULT_MEDIA"
		}
		ref, _ = refs[key].(map[string]any)
	}
	avStart, avDur, avRate, avOK := oRange(ref["available_range"])
	if !ok {
		if !avOK {
			rd.r.unread("a clip with no length (“%s”)", oStr(m, "name"))
			return nil, 0
		}
		start, dur, rate = avStart, avDur, avRate
	}
	rd.seeRate(rate)
	p := &Piece{Kind: PieceClip, Name: oStr(m, "name"), At: at, Length: dur, Speed: 1, Off: m["enabled"] == false}
	switch oSchema(ref) {
	case "ExternalReference":
		target := oStr(ref, "target_url")
		key := "otio:" + target
		if target == "" {
			key = fmt.Sprintf("otio:#%d", rd.keys)
			rd.keys++
		}
		s := rd.r.source(key) // the outer timeline lists every file, nested ones too
		s.URL, s.Path = target, fileFromURL(target)
		s.Name = firstNonEmptyStr(oStr(ref, "name"), p.Name)
		if avOK {
			s.Start, s.Duration, s.FPS = avStart, avDur, ntscRate(avRate)
		}
		if md := oMeta(ref); md != nil {
			s.AssetID, _ = md["assetId"].(string)
			s.SHA256, _ = md["sha256"].(string)
		}
		s.HasVideo, s.HasAudio = true, true // OpenTimelineIO doesn't say; the track's kind does
		p.Source = key
		p.In = start - s.Start
	case "GeneratorReference":
		p.Kind = PieceGenerator
		p.Text = oStr(ref, "generator_kind")
	case "MissingReference", "":
		key := "otio:missing:" + p.Name
		s := rd.r.source(key)
		s.Name = firstNonEmptyStr(oStr(ref, "name"), p.Name)
		if avOK {
			s.Start, s.Duration, s.FPS = avStart, avDur, ntscRate(avRate)
			p.In = start - avStart
		} else {
			p.In = start
		}
		s.HasVideo, s.HasAudio = true, true
		p.Source = key
	default:
		rd.r.unread("“%s” plays a %s, which can't be read", p.Name, strings.ToLower(oSchema(ref)))
		p.Kind = PieceGenerator
	}
	p.Speed = rd.effects(m, p.Name)
	if md := oMeta(m); md != nil {
		p.MetaRush = md
		p.Group, _ = md["clip"].(string)
		p.Role, _ = md["role"].(string)
		fps := rd.r.FPS
		if fps <= 0 {
			fps = rate
		}
		if v, ok := md["levelDb"]; ok {
			db := oNum(v)
			p.Level = &db
		}
		if f := oNum(md["fadeInFrames"]); f > 0 && fps > 0 {
			p.FadeIn = f / fps
		}
		if f := oNum(md["fadeOutFrames"]); f > 0 && fps > 0 {
			p.FadeOut = f / fps
		}
		if keys, ok := md["levelKeys"].([]any); ok && fps > 0 {
			for _, k := range keys {
				if len(p.LevelKeys) >= maxLevelKeys {
					break
				}
				pair, _ := k.([]any)
				if len(pair) == 2 {
					addLevelKey(p, LevelPoint{At: oNum(pair[0]) / fps, DB: oNum(pair[1])})
				}
			}
		}
	}
	for _, mk := range oList(m, "markers") {
		// A clip's markers are in its media's time.
		if x, ok := rd.marker(mk, at, start); ok {
			p.Markers = append(p.Markers, x)
		}
	}
	return p, dur
}

// effects reads a clip's effects: a constant speed (LinearTimeWarp); the rest
// are listed as not read.
func (rd *otioReader) effects(m ojson, name string) float64 {
	speed := 1.0
	for _, e := range oList(m, "effects") {
		em, _ := e.(map[string]any)
		switch oSchema(em) {
		case "LinearTimeWarp":
			if s := oNum(em["time_scalar"]); s != 0 {
				speed *= s
			}
		case "FreezeFrame":
			rd.r.unread("the freeze frame on “%s”", name)
		default:
			n := firstNonEmptyStr(oStr(em, "effect_name"), oStr(em, "name"), "an effect")
			rd.r.unread("the effect “%s” on “%s”", n, name)
		}
	}
	return speed
}

// marker reads a Marker; at is where its container starts on the timeline,
// mediaStart the container's own time there.
func (rd *otioReader) marker(v any, at, mediaStart float64) (ReadMarker, bool) {
	m, _ := v.(map[string]any)
	if m == nil {
		return ReadMarker{}, false
	}
	start, dur, _, ok := oRange(m["marked_range"])
	if !ok {
		return ReadMarker{}, false
	}
	mk := ReadMarker{At: at + start - mediaStart, Length: dur, Name: oStr(m, "name"), Note: oStr(m, "comment"), Color: strings.ToLower(oStr(m, "color"))}
	if md := oMeta(m); md != nil {
		mk.Kind, _ = md["kind"].(string)
		if c, _ := md["chapter"].(bool); c {
			mk.Chapter = true
		}
	}
	if isChapterName(mk.Name) {
		mk.Chapter = true
	}
	return mk, true
}

// joinKind names a transition from its type and name.
func joinKind(typ, name string) string {
	n := strings.ToLower(name + " " + typ)
	switch {
	case strings.Contains(n, "dip") && strings.Contains(n, "white"):
		return TransDipWhite
	case strings.Contains(n, "dip") || strings.Contains(n, "fade to color") || strings.Contains(n, "fade through"):
		return TransDipBlack
	case strings.Contains(n, "wipe"):
		return TransWipe
	case strings.Contains(n, "push"):
		return TransPush
	case strings.Contains(n, "slide"):
		return TransSlide
	case strings.Contains(n, "zoom"):
		return TransZoom
	case strings.Contains(n, "whip"):
		return TransWhip
	case strings.Contains(n, "dissolve") || strings.Contains(n, "cross fade") || strings.Contains(n, "crossfade") || strings.Contains(n, "cross-fade") || typ == "SMPTE_Dissolve":
		return TransDissolve
	}
	return JoinOther
}

// isChapterName: markers people name "Chapter 1: …" or "Chapter: …".
func isChapterName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(n, "chapter ") || strings.HasPrefix(n, "chapter:") || n == "chapter"
}

func firstNonEmptyStr(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func roundTo(v, step float64) float64 { return math.Round(v/step) * step }

// jsonValues counts the values in JSON text (objects, arrays and the values
// in them) without making anything of them: '{', '[' and ',' outside strings,
// plus one.
func jsonValues(data []byte) int {
	n, in, esc := 1, false, false
	for _, c := range data {
		switch {
		case esc:
			esc = false
		case in:
			switch c {
			case '\\':
				esc = true
			case '"':
				in = false
			}
		case c == '"':
			in = true
		case c == '{' || c == '[' || c == ',':
			n++
		}
	}
	return n
}
