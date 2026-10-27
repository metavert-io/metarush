package interchange

import (
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

// Reading FCPXML 1.8–1.11 (and .fcpxmld bundles, whose Info.fcpxml the
// caller reads). Every story element's offset is in its parent's local time,
// and its own local time starts at its start attribute there; the primary
// storyline's offsets are sequence times from tcStart. Connected clips carry
// a lane relative to their parent; secondary storylines are spines with a
// lane. From 1.9 a file's address is a media-rep child of the asset, not its
// src attribute. Compound clips (ref-clip) and multicam clips (mc-clip, the
// active angles) are opened up into the timeline's own lanes.

type fcpFormat struct {
	fps           float64
	width, height int
}

type fcpAsset struct {
	id, name, src       string
	assetID, sha256     string // MetaRush's own note
	start, duration     float64
	hasVideo, hasAudio  bool
	format              string
	audioRate, channels int
}

// fcpTimeMap is what a timeMap says, read once per element.
type fcpTimeMap struct {
	ok            bool
	speed, t0, v0 float64
}

type fcpReader struct {
	r       *Read
	formats map[string]fcpFormat
	assets  map[string]*fcpAsset
	media   map[string]*xnode
	effects map[string]string // id → name
	seqFPS  float64
	groups  int
	depth   int
	lanes   map[string]*Lane
	// byGroup is each clip's pieces (its picture and its own sound), in order.
	byGroup map[string][]groupPiece
	// open is the compound and multicam clips being opened (one inside itself is refused).
	open map[string]bool
	// timeMaps are the timeMaps read so far (a compound clip opened again reads them once).
	timeMaps map[*xnode]fcpTimeMap
	// volumes are the adjust-volumes read so far.
	volumes map[*xnode]*fcpVolume
}

type groupPiece struct {
	p     *Piece
	video bool
}

// fcpSeconds parses FCPXML time ("3600s", "1001/30000s", "0s") exactly;
// anything but a finite time of a sane size is no time.
func fcpSeconds(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 64 {
		return 0, false
	}
	s = strings.TrimSuffix(s, "s")
	if n, d, ok := strings.Cut(s, "/"); ok {
		if !plainInt(n) || !plainInt(d) {
			return 0, false
		}
		// Most times fit in 64 bits: divided exactly enough without big numbers.
		if ni, err1 := strconv.ParseInt(n, 10, 64); err1 == nil && ni > -1<<52 && ni < 1<<52 {
			if di, err2 := strconv.ParseInt(d, 10, 64); err2 == nil && di > 0 && di < 1<<52 {
				f := float64(ni) / float64(di)
				return f, sane(f)
			}
		}
		num, ok1 := new(big.Rat).SetString(n)
		den, ok2 := new(big.Rat).SetString(d)
		if !ok1 || !ok2 || den.Sign() == 0 {
			return 0, false
		}
		f, _ := new(big.Rat).Quo(num, den).Float64()
		return f, sane(f)
	}
	f, ok := number(s)
	return f, ok && sane(f)
}

// plainInt reports digits with an optional sign (big.Rat would also take
// exponents, which can be made to cost a lot).
func plainInt(s string) bool {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "-"), "+")
	if s == "" || len(s) > 30 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func fcpAttrTime(n *xnode, k string) float64 {
	v, _ := fcpSeconds(n.attr(k))
	return v
}

func readFCPXML(data []byte, o ReadOptions) (*Read, error) {
	root, err := parseXML(data)
	if err != nil {
		return nil, readErr(err, "This FCPXML file can't be read: %s.", err.Error())
	}
	if root.name != "fcpxml" {
		return nil, &ReadError{Msg: "This isn't an FCPXML file.", Err: ErrNotTimeline}
	}
	version := strings.TrimSpace(root.attr("version"))
	r := newRead(FormatFCPXML, strings.TrimSpace("FCPXML "+version))
	if major, minor, ok := strings.Cut(version, "."); ok {
		mi, err := strconv.Atoi(minor)
		if major != "1" || err != nil || mi < 8 || mi > 11 {
			r.Warnings = append(r.Warnings, fmt.Sprintf("This is FCPXML %s; MetaRush reads 1.8 to 1.11 and read it the same way: check the cut.", version))
		}
	}
	rd := &fcpReader{r: r, formats: map[string]fcpFormat{}, assets: map[string]*fcpAsset{}, media: map[string]*xnode{}, effects: map[string]string{}, lanes: map[string]*Lane{},
		byGroup: map[string][]groupPiece{}, open: map[string]bool{}, timeMaps: map[*xnode]fcpTimeMap{},
		volumes: map[*xnode]*fcpVolume{}}
	if res := root.child("resources"); res != nil {
		rd.resources(res)
	}
	var projects []*xnode
	var walk func(n *xnode)
	walk = func(n *xnode) {
		for _, c := range n.children {
			switch c.name {
			case "library", "event":
				walk(c)
			case "project":
				projects = append(projects, c)
			}
		}
	}
	walk(root)
	if len(projects) == 0 {
		return nil, &ReadError{Msg: "This FCPXML file holds clips or events but no timeline (project).", Hint: "Export the project (the timeline) from the editing app, not a library's clips.", Err: ErrNotTimeline}
	}
	proj := projects[0]
	for _, p := range projects {
		r.Timelines = append(r.Timelines, p.attr("name"))
		if o.Timeline != "" && strings.EqualFold(p.attr("name"), o.Timeline) {
			proj = p
		}
	}
	seq := proj.child("sequence")
	if seq == nil {
		return nil, &ReadError{Msg: fmt.Sprintf("The timeline “%s” in this FCPXML file is empty.", proj.attr("name")), Err: ErrNotTimeline}
	}
	r.Name = proj.attr("name")
	f := rd.formats[seq.attr("format")]
	r.FPS, r.Width, r.Height = f.fps, f.width, f.height
	rd.seqFPS = f.fps
	r.Start = fcpAttrTime(seq, "tcStart")
	r.Length = fcpAttrTime(seq, "duration")
	spine := seq.child("spine")
	if spine == nil {
		return r, nil
	}
	rd.spine(spine, func(t float64) float64 { return t - r.Start }, 0, 0)
	rd.collect()
	return r, nil
}

func (rd *fcpReader) resources(res *xnode) {
	for _, c := range res.children {
		id := c.attr("id")
		switch c.name {
		case "format":
			f := fcpFormat{}
			if fd, ok := fcpSeconds(c.attr("frameDuration")); ok && fd >= 1.0/1000 {
				f.fps = ntscRate(1 / fd)
			}
			f.width, _ = strconv.Atoi(c.attr("width"))
			f.height, _ = strconv.Atoi(c.attr("height"))
			rd.formats[id] = f
		case "asset":
			a := &fcpAsset{id: id, name: c.attr("name"), src: c.attr("src"), start: fcpAttrTime(c, "start"), duration: fcpAttrTime(c, "duration"),
				hasVideo: c.attr("hasVideo") == "1", hasAudio: c.attr("hasAudio") == "1", format: c.attr("format")}
			a.audioRate, _ = strconv.Atoi(c.attr("audioRate"))
			a.channels, _ = strconv.Atoi(c.attr("audioChannels"))
			// FCPXML 1.9+: the file is a media-rep (the original before a proxy).
			for _, mr := range c.all("media-rep") {
				if a.src == "" || mr.attr("kind") == "original-media" || mr.attr("kind") == "" {
					if mr.attr("kind") != "proxy-media" || a.src == "" {
						a.src = mr.attr("src")
					}
				}
			}
			if c.attr("hasVideo") == "" && c.attr("hasAudio") == "" {
				a.hasVideo, a.hasAudio = true, true
			}
			if md := c.child("metadata"); md != nil {
				for _, x := range md.all("md") {
					a.assetID, a.sha256 = noteValue(x.attr("key"), x.attr("value"), a.assetID, a.sha256)
				}
			}
			rd.assets[id] = a
		case "media":
			rd.media[id] = c
		case "effect":
			rd.effects[id] = c.attr("name")
		}
	}
}

// laneKey is where a piece goes: video lanes by their number, sound by the
// lane of the clip it belongs to.
func (rd *fcpReader) lane(kind string, n int, sub string) *Lane {
	key := fmt.Sprintf("%s%d%s", kind[:1], n, sub)
	if l := rd.lanes[key]; l != nil {
		return l
	}
	name := ""
	switch {
	case kind == KindVideo && n == 0:
		name = "Storyline"
	case kind == KindVideo:
		name = fmt.Sprintf("Lane %d", n)
	case n == 0:
		name = "Storyline sound"
	default:
		name = fmt.Sprintf("Sound lane %d", n)
	}
	l := &Lane{Name: name, Kind: kind}
	l.z = float64(n)
	if sub != "" {
		l.z += 0.5
	}
	rd.lanes[key] = l
	return l
}

// collect orders the lanes: video from the lowest lane up, sound by lane.
func (rd *fcpReader) collect() {
	var video, audio []*Lane
	for _, l := range rd.lanes {
		if l.Kind == KindVideo {
			video = append(video, l)
		} else {
			audio = append(audio, l)
		}
	}
	sort.SliceStable(video, func(i, j int) bool { return video[i].z < video[j].z })
	sort.SliceStable(audio, func(i, j int) bool {
		if math.Abs(audio[i].z) != math.Abs(audio[j].z) {
			return math.Abs(audio[i].z) < math.Abs(audio[j].z)
		}
		return audio[i].z > audio[j].z
	})
	// The storyline first (Flatten treats the lowest video lane at or above 0 as the main one).
	rd.r.Video = append(rd.r.Video, video...)
	rd.r.Audio = append(rd.r.Audio, audio...)
}

// spine reads a storyline: items one after another, transitions between.
// toTL maps the spine's local time to the timeline; lane is its lane.
func (rd *fcpReader) spine(spine *xnode, toTL func(float64) float64, lane int, depth int) {
	if depth > 32 {
		rd.r.unread("storylines nested more than 32 deep")
		return
	}
	items := spine.children
	for i, c := range items {
		if c.name == "transition" {
			if !rd.r.spend(1) {
				return
			}
			rd.transition(c, items, i, toTL, lane)
			continue
		}
		rd.element(c, toTL, lane, "", depth)
	}
}

// transition is a transition in a storyline: on the edit point between the
// item before and the one after.
func (rd *fcpReader) transition(c *xnode, items []*xnode, i int, toTL func(float64) float64, lane int) {
	off := fcpAttrTime(c, "offset")
	dur := fcpAttrTime(c, "duration")
	edit := off + dur/2
	hasNext, hasPrev := false, false
	for _, n := range items[i+1:] {
		if isStory(n.name) {
			edit, hasNext = fcpAttrTime(n, "offset"), true
			break
		}
	}
	for _, n := range items[:i] {
		if isStory(n.name) {
			hasPrev = true
		}
	}
	name := c.attr("name")
	video := c.child("filter-video")
	if video != nil {
		if en := rd.effects[video.attr("ref")]; en != "" {
			name = en
		}
	}
	j := Join{At: toTL(edit), Before: math.Max(0, edit-off), After: math.Max(0, off+dur-edit), Name: name, Kind: joinKind("", name)}
	if j.Kind == JoinOther && video == nil && c.child("filter-audio") != nil {
		j.Kind = TransAudioCrossfade
	}
	if j.Kind == JoinOther && video == nil && c.child("filter-audio") == nil {
		j.Kind = TransDissolve // a transition with no filter is the default dissolve
	}
	switch {
	case !hasPrev && j.Kind == TransDissolve:
		j.Kind, j.At, j.Before, j.After = TransFadeIn, toTL(off), 0, dur
	case !hasNext && j.Kind == TransDissolve:
		j.Kind, j.At, j.Before, j.After = TransFadeOut, toTL(off+dur), dur, 0
	}
	l := rd.lane(KindVideo, lane, "")
	l.Joins = append(l.Joins, j)
}

func isStory(name string) bool {
	switch name {
	case "asset-clip", "clip", "ref-clip", "sync-clip", "mc-clip", "gap", "title", "video", "audio", "audition", "spine", "live-drawing":
		return true
	}
	return false
}

// element reads one story element under a parent whose local time toTL maps
// to the timeline; lane is the parent's absolute lane, group the clip a
// media element belongs to.
func (rd *fcpReader) element(e *xnode, toTL func(float64) float64, parentLane int, group string, depth int) {
	if !isStory(e.name) || !rd.r.spend(1) {
		return
	}
	laneRel, _ := strconv.Atoi(e.attr("lane"))
	lane := parentLane + laneRel
	at := toTL(fcpAttrTime(e, "offset"))
	start, hasStart := fcpSeconds(e.attr("start"))
	if !hasStart {
		// No start: the beginning of what it plays.
		if a := rd.assets[e.attr("ref")]; a != nil {
			start = a.start
		} else if m := rd.media[e.attr("ref")]; m != nil && m.child("sequence") != nil {
			start = fcpAttrTime(m.child("sequence"), "tcStart")
		}
	}
	dur := fcpAttrTime(e, "duration")
	local := func(t float64) float64 { return at + (t - start) }
	switch e.name {
	case "spine":
		rd.spine(e, func(t float64) float64 { return at + t }, lane, depth+1)
		return
	case "audition":
		// The pick on top is the first child.
		for _, c := range e.children {
			if isStory(c.name) {
				// An audition's clips share its offset.
				rd.element(c, func(t float64) float64 { return toTL(fcpAttrTime(e, "offset")) + (t - fcpAttrTime(c, "offset")) }, lane, group, depth+1)
				break
			}
		}
		return
	case "gap":
		rd.markers(e, nil, local)
	case "title":
		p := &Piece{Kind: PieceTitle, Name: firstNonEmptyStr(e.attr("name"), "Title"), At: at, Length: dur, Speed: 1, Text: titleText(e)}
		if e.attr("enabled") == "0" {
			p.Off = true
		}
		rd.lane(KindVideo, lane, "").Pieces = append(rd.lane(KindVideo, lane, "").Pieces, p)
		rd.markers(e, p, local)
	case "asset-clip":
		g := rd.newGroup(e)
		rd.playMedia(e, e, at, start, dur, lane, g, true, true)
	case "video", "audio":
		if group == "" {
			group = rd.newGroup(e)
		}
		if _, isAsset := rd.assets[e.attr("ref")]; !isAsset && e.name == "video" {
			// A generator (a colour, a shape, a placeholder).
			p := &Piece{Kind: PieceGenerator, Name: firstNonEmptyStr(e.attr("name"), rd.effects[e.attr("ref")], "Generator"), At: at, Length: dur, Speed: 1}
			rd.lane(KindVideo, lane, "").Pieces = append(rd.lane(KindVideo, lane, "").Pieces, p)
			break
		}
		rd.playMedia(e, e, at, start, dur, lane, group, e.name == "video", e.name == "audio")
	case "clip", "sync-clip":
		g := rd.newGroup(e)
		// Its own media without a lane, then what is anchored to it.
		for _, c := range e.children {
			if !isStory(c.name) || c.name == "spine" {
				continue
			}
			if c.attr("lane") == "" {
				rd.element(c, local, lane, g, depth+1)
			}
		}
		rd.applyClipLevels(e, g, local)
		if e.attr("enabled") == "0" {
			rd.offGroup(g)
		}
		rd.markers(e, rd.firstOf(g), local)
		rd.anchored(e, local, lane, "", depth)
		return
	case "ref-clip":
		rd.compound(e, at, start, dur, lane, depth)
	case "mc-clip":
		rd.multicam(e, at, start, dur, lane, depth)
	case "live-drawing":
		rd.r.unread("a drawing (“%s”)", e.attr("name"))
	}
	g := ""
	if e.name == "video" || e.name == "audio" {
		g = group // a clip's sound anchored to its picture stays the clip's
	}
	rd.anchored(e, local, lane, g, depth)
}

// anchored reads what is connected to an element (children with a lane, and
// secondary storylines).
func (rd *fcpReader) anchored(e *xnode, local func(float64) float64, lane int, group string, depth int) {
	for _, c := range e.children {
		if !isStory(c.name) {
			continue
		}
		if c.attr("lane") == "" && c.name != "spine" {
			continue // a clip's own parts (read with it)
		}
		rd.element(c, local, lane, group, depth+1)
	}
}

func (rd *fcpReader) newGroup(e *xnode) string {
	rd.groups++
	return fmt.Sprintf("fcp:%d", rd.groups)
}

// playMedia reads an element that plays an asset: its picture on lane, its own
// sound on the lane's sound.
func (rd *fcpReader) playMedia(e, timing *xnode, at, start, dur float64, lane int, group string, wantVideo, wantAudio bool) {
	a := rd.assets[e.attr("ref")]
	if a == nil {
		rd.r.unread("a clip whose file isn't described (“%s”)", e.attr("name"))
		return
	}
	src := rd.source(a)
	speed, mediaAt := rd.timeMap(e, start)
	in := mediaAt - a.start
	name := firstNonEmptyStr(e.attr("name"), a.name)
	enable := e.attr("srcEnable")
	off := e.attr("enabled") == "0"
	if a.hasVideo && enable != "audio" && wantVideo {
		p := &Piece{Kind: PieceClip, Name: name, Source: src.Key, At: at, Length: dur, In: in, Speed: speed, Group: group, Off: off}
		p.PictureFadeIn, p.PictureFadeOut = blendFades(e, start, dur)
		rd.put(rd.lane(KindVideo, lane, ""), p, true)
		rd.markers(e, p, func(t float64) float64 { return at + (t - start) })
		rd.effectsOf(e, name)
	}
	if a.hasAudio && enable != "video" && (wantAudio || e.name == "asset-clip") {
		aStart, aDur := start, dur
		if s, ok := fcpSeconds(e.attr("audioStart")); ok {
			aStart = s
		}
		if d, ok := fcpSeconds(e.attr("audioDuration")); ok {
			aDur = d
		}
		_, aMedia := rd.timeMap(e, aStart)
		p := &Piece{Kind: PieceClip, Name: name, Source: src.Key, At: at + (aStart - start), Length: aDur, In: aMedia - a.start, Speed: speed, Group: group, Off: off,
			Role: fcpRole(firstNonEmptyStr(e.attr("audioRole"), e.attr("role")))}
		rd.volume(e, p, func(t float64) float64 { return at + (t - start) })
		sub := ""
		if !a.hasVideo || lane != 0 {
			sub = "s"
		}
		rd.put(rd.lane(KindAudio, lane, sub), p, false)
		if !(a.hasVideo && enable != "audio" && wantVideo) {
			rd.markers(e, p, func(t float64) float64 { return at + (t - start) })
		}
	}
}

// put adds a clip's piece to a lane, noting it under its clip.
func (rd *fcpReader) put(l *Lane, p *Piece, video bool) {
	l.Pieces = append(l.Pieces, p)
	if p.Group != "" {
		rd.byGroup[p.Group] = append(rd.byGroup[p.Group], groupPiece{p, video})
	}
}

func (rd *fcpReader) source(a *fcpAsset) *Source {
	s := rd.r.source("fcpxml:" + a.id)
	if s.Name == "" {
		s.Name, s.URL, s.Path = a.name, a.src, fileFromURL(a.src)
		s.Start, s.Duration = a.start, a.duration
		s.HasVideo, s.HasAudio = a.hasVideo, a.hasAudio
		s.FPS = rd.formats[a.format].fps
		s.AssetID, s.SHA256 = a.assetID, a.sha256
	}
	return s
}

// timeMap is a clip's speed and the media time at a local time (a constant
// speed from its first and last points; ramps are read as their average).
func (rd *fcpReader) timeMap(e *xnode, local float64) (speed, media float64) {
	tm := e.child("timeMap")
	if tm == nil {
		return 1, local
	}
	m, seen := rd.timeMaps[tm]
	if !seen {
		m = rd.readTimeMap(e, tm)
		rd.timeMaps[tm] = m
	}
	if !m.ok {
		return 1, local
	}
	return m.speed, m.v0 + (local-m.t0)*m.speed
}

// readTimeMap reads a timeMap's points once.
func (rd *fcpReader) readTimeMap(e, tm *xnode) fcpTimeMap {
	pts := tm.all("timept")
	if len(pts) < 2 {
		return fcpTimeMap{}
	}
	t0, v0 := fcpAttrTime(pts[0], "time"), fcpAttrTime(pts[0], "value")
	t1, v1 := fcpAttrTime(pts[len(pts)-1], "time"), fcpAttrTime(pts[len(pts)-1], "value")
	if t1 <= t0 {
		return fcpTimeMap{}
	}
	speed := (v1 - v0) / (t1 - t0)
	for _, p := range pts[1 : len(pts)-1] {
		t, v := fcpAttrTime(p, "time"), fcpAttrTime(p, "value")
		if math.Abs(v0+(t-t0)*speed-v) > 0.02 {
			rd.r.unread("the speed ramp on “%s” (read as one speed, %.0f%%)", e.attr("name"), speed*100)
			break
		}
	}
	return fcpTimeMap{ok: true, speed: speed, t0: t0, v0: v0}
}

// applyClipLevels puts a clip's own volume on the sound of its parts.
func (rd *fcpReader) applyClipLevels(e *xnode, group string, local func(float64) float64) {
	if e.child("adjust-volume") == nil {
		return
	}
	for _, g := range rd.byGroup[group] {
		if !g.video {
			rd.volume(e, g.p, local)
		}
	}
}

func (rd *fcpReader) offGroup(group string) {
	for _, g := range rd.byGroup[group] {
		g.p.Off = true
	}
}

// firstOf is a clip's picture (else its first sound).
func (rd *fcpReader) firstOf(group string) *Piece {
	for _, g := range rd.byGroup[group] {
		if g.video {
			return g.p
		}
	}
	if gs := rd.byGroup[group]; len(gs) > 0 {
		return gs[0].p
	}
	return nil
}

// fcpVolume is what an adjust-volume says, read once per element (a clip's
// sound parts and every opening of a compound clip share it).
type fcpVolume struct {
	level           *float64
	fadeIn, fadeOut float64
	keys            [][2]float64 // local time, dB (at most maxLevelKeys)
}

func readVolume(v *xnode) *fcpVolume {
	out := &fcpVolume{}
	if db, ok := dbValue(v.attr("amount")); ok {
		out.level = &db
	}
	for _, param := range v.children {
		if param.name != "param" {
			continue
		}
		for _, c := range param.children {
			switch c.name {
			case "fadeIn":
				out.fadeIn = fcpAttrTime(c, "duration")
			case "fadeOut":
				out.fadeOut = fcpAttrTime(c, "duration")
			case "keyframeAnimation":
				for _, k := range c.children {
					if len(out.keys) >= maxLevelKeys {
						break
					}
					if k.name != "keyframe" {
						continue
					}
					if db, ok := dbValue(k.attr("value")); ok {
						out.keys = append(out.keys, [2]float64{fcpAttrTime(k, "time"), db})
					}
				}
			}
		}
	}
	return out
}

// volume puts an element's adjust-volume (level, fades, keyframes) on a piece.
func (rd *fcpReader) volume(e *xnode, p *Piece, local func(float64) float64) {
	v := e.child("adjust-volume")
	if v == nil {
		return
	}
	vol := rd.volumes[v]
	if vol == nil {
		vol = readVolume(v)
		rd.volumes[v] = vol
	}
	if vol.level != nil {
		db := *vol.level
		p.Level = &db
	}
	if vol.fadeIn > 0 {
		p.FadeIn = vol.fadeIn
	}
	if vol.fadeOut > 0 {
		p.FadeOut = vol.fadeOut
	}
	for _, k := range vol.keys {
		addLevelKey(p, LevelPoint{At: local(k[0]) - p.At, DB: k[1]})
	}
}

// dbValue reads "-6dB" (or a bare number of decibels).
func dbValue(s string) (float64, bool) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "dB"))
	if s == "" {
		return 0, false
	}
	return number(s)
}

// blendFades reads opacity keyframes that fade a picture from and to black.
func blendFades(e *xnode, start, dur float64) (in, out float64) {
	b := e.child("adjust-blend")
	if b == nil {
		return 0, 0
	}
	for _, param := range b.all("param") {
		anim := param.child("keyframeAnimation")
		if anim == nil {
			continue
		}
		keys := anim.all("keyframe")
		if len(keys) < 2 {
			continue
		}
		val := func(k *xnode) float64 { f, _ := number(k.attr("value")); return f }
		if val(keys[0]) < 0.01 && val(keys[1]) > 0.99 {
			in = fcpAttrTime(keys[1], "time") - fcpAttrTime(keys[0], "time")
		}
		n := len(keys)
		if val(keys[n-1]) < 0.01 && val(keys[n-2]) > 0.99 {
			out = fcpAttrTime(keys[n-1], "time") - fcpAttrTime(keys[n-2], "time")
		}
	}
	return in, out
}

// effectsOf lists picture effects that aren't read.
func (rd *fcpReader) effectsOf(e *xnode, name string) {
	for _, c := range e.children {
		switch c.name {
		case "filter-video", "filter-video-mask":
			rd.r.unread("the effect “%s” on “%s”", firstNonEmptyStr(c.attr("name"), rd.effects[c.attr("ref")], "an effect"), name)
		case "adjust-transform", "adjust-crop", "adjust-corners":
			rd.r.unread("the position, scale or crop set on “%s”", name)
		case "adjust-colorConform", "adjust-color":
			rd.r.unread("the colour settings on “%s”", name)
		}
	}
}

// markers reads markers and chapter markers on an element (local time).
func (rd *fcpReader) markers(e *xnode, p *Piece, local func(float64) float64) {
	for _, c := range e.children {
		if c.name != "marker" && c.name != "chapter-marker" {
			continue
		}
		if !rd.r.spend(1) {
			return
		}
		mk := ReadMarker{At: local(fcpAttrTime(c, "start")), Length: fcpAttrTime(c, "duration"), Name: c.attr("value"), Note: c.attr("note"), Chapter: c.name == "chapter-marker"}
		if c.attr("completed") != "" {
			mk.Kind = "todo"
		}
		if isChapterName(mk.Name) {
			mk.Chapter = true
		}
		rd.r.Markers = append(rd.r.Markers, mk)
	}
}

// compound opens up a compound clip: its sequence's lanes, inside its window.
func (rd *fcpReader) compound(e *xnode, at, start, dur float64, lane int, depth int) {
	ref := e.attr("ref")
	m := rd.media[ref]
	if m == nil || m.child("sequence") == nil {
		rd.r.unread("a compound clip whose contents aren't in the file (“%s”)", e.attr("name"))
		return
	}
	if rd.open[ref] {
		rd.r.unread("a compound clip that contains itself (“%s”)", e.attr("name"))
		return
	}
	rd.open[ref] = true
	defer delete(rd.open, ref)
	seq := m.child("sequence")
	if !rd.r.visit(seq.size) { // its whole contents, every time it is opened
		return
	}
	rd.inner(seq.child("spine"), func(t float64) float64 { return at + (t - start) }, at, at+dur, lane, e.attr("srcEnable"), depth, e.attr("name"), ref)
}

// multicam opens up a multicam clip: the active picture angle's pictures and
// the active sound angle's sound.
func (rd *fcpReader) multicam(e *xnode, at, start, dur float64, lane int, depth int) {
	m := rd.media[e.attr("ref")]
	mc := (*xnode)(nil)
	if m != nil {
		mc = m.child("multicam")
	}
	if mc == nil {
		rd.r.unread("a multicam clip whose angles aren't in the file (“%s”)", e.attr("name"))
		return
	}
	if ref := e.attr("ref"); rd.open[ref] {
		rd.r.unread("a multicam clip that contains itself (“%s”)", e.attr("name"))
		return
	} else {
		rd.open[ref] = true
		defer delete(rd.open, ref)
	}
	angles := map[string]*xnode{}
	for _, a := range mc.all("mc-angle") {
		angles[a.attr("angleID")] = a
	}
	video, audio := "", ""
	for _, s := range e.all("mc-source") {
		switch s.attr("srcEnable") {
		case "video":
			video = s.attr("angleID")
		case "audio":
			audio = s.attr("angleID")
		default:
			video, audio = s.attr("angleID"), s.attr("angleID")
		}
	}
	toTL := func(t float64) float64 { return at + (t - start) }
	if a := angles[video]; a != nil && rd.r.visit(a.size) {
		rd.inner(a, toTL, at, at+dur, lane, "video", depth, e.attr("name"), e.attr("ref"))
	}
	if a := angles[audio]; a != nil && rd.r.visit(a.size) {
		rd.inner(a, toTL, at, at+dur, lane, "audio", depth, e.attr("name"), e.attr("ref"))
	}
	if video == "" && audio == "" {
		rd.r.unread("a multicam clip with no angle chosen (“%s”)", e.attr("name"))
	}
}

// inner reads a nested storyline into the timeline, keeping what plays
// between lo and hi; use is "video", "audio" or "" (both).
func (rd *fcpReader) inner(spine *xnode, toTL func(float64) float64, lo, hi float64, lane int, use string, depth int, name, ref string) {
	if spine == nil {
		return
	}
	if depth > 16 {
		rd.r.unread("clips nested more than 16 deep (inside “%s”)", name)
		return
	}
	if rd.r.over {
		return
	}
	outer := rd.lanes
	rd.lanes = map[string]*Lane{}
	rd.spine(spine, toTL, 0, depth+1)
	innerLanes := rd.lanes
	rd.lanes = outer
	keys := make([]string, 0, len(innerLanes))
	for k := range innerLanes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		l := innerLanes[k]
		if (use == "audio" && l.Kind == KindVideo) || (use == "video" && l.Kind == KindAudio) {
			continue
		}
		n := int(math.Floor(l.z))
		dst := rd.lane(l.Kind, lane, fmt.Sprintf("~%s%d", clip(ref, 64), n))
		dst.z = float64(lane) + (l.z+50)/1000 // above the clip's own lane, below the next
		if l.Kind == KindVideo && n == 0 {
			dst.z = float64(lane) + 0.0001
		}
		for _, p := range l.Pieces {
			if !rd.r.spend(1) || !rd.r.visit(1+len(p.LevelKeys)) {
				return
			}
			if q := trimPiece(p, lo, hi); q != nil {
				dst.Pieces = append(dst.Pieces, q)
			}
		}
		for _, j := range l.Joins {
			if j.At > lo && j.At < hi {
				dst.Joins = append(dst.Joins, j)
			}
		}
	}
}

// trimPiece keeps the part of a piece between lo and hi (nil: none).
func trimPiece(p *Piece, lo, hi float64) *Piece {
	s, e := math.Max(p.At, lo), math.Min(p.End(), hi)
	if e-s < 1e-6 {
		return nil
	}
	q := *p
	q.In = p.In + (s-p.At)*p.Speed
	q.At, q.Length = s, e-s
	var keys []LevelPoint
	for _, k := range p.LevelKeys {
		keys = append(keys, LevelPoint{At: k.At - (s - p.At), DB: k.DB})
	}
	q.LevelKeys = keys
	return &q
}

// titleText is a title's words (the first maxTitleRunes of them).
func titleText(e *xnode) string {
	var out strings.Builder
	limit := maxTitleRunes * 4 // bytes: the runes are counted at the end
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || out.Len() >= limit {
			return
		}
		if out.Len() > 0 {
			out.WriteByte(' ')
		}
		if len(s) > limit-out.Len() {
			s = s[:limit-out.Len()]
		}
		out.WriteString(s)
	}
	for _, t := range e.all("text") {
		var b strings.Builder
		var walk func(n *xnode)
		walk = func(n *xnode) {
			if b.Len() >= limit {
				return
			}
			if n.name == "text-style" {
				s := n.text
				if len(s) > limit-b.Len() {
					s = s[:limit-b.Len()]
				}
				b.WriteString(s)
			}
			for _, c := range n.children {
				walk(c)
			}
		}
		walk(t)
		if s := strings.TrimSpace(b.String()); s != "" {
			add(s)
		} else {
			add(t.text)
		}
	}
	return clip(strings.ToValidUTF8(out.String(), ""), maxTitleRunes)
}

// fcpRole reads an audio role ("dialogue.dialogue-1" → dialogue).
func fcpRole(role string) string {
	r, _, _ := strings.Cut(strings.ToLower(role), ".")
	switch r {
	case "dialogue", "music", "effects":
		return r
	case "":
		return ""
	}
	return soundRole(r)
}

// soundRole guesses a sound's role from a name (a role, a track's name).
func soundRole(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "music") || strings.Contains(n, "song") || strings.Contains(n, "score"):
		return RoleMusic
	case strings.Contains(n, "effect") || strings.Contains(n, "sfx") || strings.Contains(n, "foley") || n == "fx":
		return RoleEffects
	case strings.Contains(n, "narration") || strings.Contains(n, "voiceover") || strings.Contains(n, "voice-over") || strings.Contains(n, "voice over") || n == "vo":
		return RoleNarration
	case strings.Contains(n, "dialog") || strings.Contains(n, "interview") || strings.Contains(n, "clip sound"):
		return RoleDialogue
	}
	return ""
}
