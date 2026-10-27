package interchange

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Reading timelines other editing apps wrote (D303): one Go reader per
// format into one model, Read. OpenTimelineIO is the readers' independent
// check (scripts/interchange-check.sh), never a dependency. The model keeps
// the file's own layout — lanes of pieces, transitions on edit points,
// markers — in seconds; Flatten turns it into what a cut can play.

// FormatEDL is a CMX 3600 edit decision list (read only).
const FormatEDL = "edl"

// ReadFormats are the formats Read takes, in the order people see them.
var ReadFormats = []string{FormatOTIO, FormatFCPXML, FormatXMEML, FormatEDL}

// Piece kinds.
const (
	PieceClip      = "clip"      // footage or a sound file
	PieceTitle     = "title"     // text the editing app draws
	PieceGenerator = "generator" // a colour, slug or other picture the app makes
)

// Join kinds beyond the picture Trans* kinds and the fades.
const (
	JoinOther = "other" // a transition MetaRush has no match for (Name says which)
)

// Source is one file an imported timeline plays.
type Source struct {
	Key  string `json:"key"`
	Name string `json:"name" jsonschema:"description=What the timeline calls it."`
	// File is its file name ("Interview A.mov"), empty when the timeline names no file.
	File string `json:"file,omitempty"`
	// Path is a file on this computer the timeline points at (a file:// address).
	Path string `json:"path,omitempty"`
	URL  string `json:"url,omitempty"`
	// Start is the file's first timecode in seconds (pieces' In are measured from it).
	Start float64 `json:"start,omitempty"`
	// Duration is how long the file is, in seconds (0: the timeline doesn't say).
	Duration float64 `json:"duration,omitempty"`
	FPS      float64 `json:"fps,omitempty"`
	HasVideo bool    `json:"hasVideo"`
	HasAudio bool    `json:"hasAudio"`
	// AssetID and SHA256 come from MetaRush's own exports: which clip it is.
	AssetID string `json:"assetId,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	// TimecodeOnly: pieces' In are the file's own timecode, and the timeline
	// doesn't say where the file's first frame is (an EDL).
	TimecodeOnly bool `json:"timecodeOnly,omitempty"`
}

// LevelPoint is a sound level (dB) at seconds into a piece.
type LevelPoint struct {
	At float64 `json:"at"`
	DB float64 `json:"db"`
}

// Piece is one clip, title or generator on a lane (nested timelines and
// compound clips are opened up into the timeline's own lanes).
type Piece struct {
	Kind   string  `json:"kind"`
	Name   string  `json:"name"`
	Source string  `json:"source,omitempty"` // Source.Key
	At     float64 `json:"at"`               // timeline seconds where it starts
	Length float64 `json:"length"`           // seconds on the timeline
	// In is seconds into the source (from its first timecode) at At.
	In    float64 `json:"in"`
	Speed float64 `json:"speed"` // 1 = normal; negative = backwards
	// Off: the editing app doesn't play it (disabled, muted).
	Off bool `json:"off,omitempty"`
	// Level is the sound level in dB (nil: unchanged); fades in seconds.
	Level     *float64     `json:"level,omitempty"`
	FadeIn    float64      `json:"fadeIn,omitempty"`
	FadeOut   float64      `json:"fadeOut,omitempty"`
	LevelKeys []LevelPoint `json:"levelKeys,omitempty"`
	// PictureFadeIn/Out are fades from and to black on a picture (seconds).
	PictureFadeIn  float64 `json:"pictureFadeIn,omitempty"`
	PictureFadeOut float64 `json:"pictureFadeOut,omitempty"`
	Text           string  `json:"text,omitempty"` // a title's words
	// Group ties a clip's picture and its own sound together.
	Group string `json:"group,omitempty"`
	// Role is a sound's role when the file says it: dialogue, music, effects, narration.
	Role    string       `json:"role,omitempty"`
	Markers []ReadMarker `json:"markers,omitempty"`
	// MetaRush is what MetaRush's own exports note on the piece (its clip, role).
	MetaRush map[string]any `json:"-"`
}

// End is where the piece ends on the timeline.
func (p *Piece) End() float64 { return p.At + p.Length }

// Join is a transition on a lane's edit point.
type Join struct {
	At     float64 `json:"at"` // the edit point (timeline seconds)
	Before float64 `json:"before"`
	After  float64 `json:"after"`
	Kind   string  `json:"kind"`
	Name   string  `json:"name,omitempty"`
	// Direction and Easing come from MetaRush's own exports.
	Direction string `json:"direction,omitempty"`
	Easing    string `json:"easing,omitempty"`
	From      string `json:"from,omitempty"`
}

// Lane is a track: pieces in time order and the transitions between them.
type Lane struct {
	Name   string   `json:"name"`
	Kind   string   `json:"kind"` // video | audio
	Role   string   `json:"role,omitempty"`
	Off    bool     `json:"off,omitempty"`
	Pieces []*Piece `json:"pieces"`
	Joins  []Join   `json:"joins,omitempty"`
	z      float64  // stacking order while reading (higher is in front)
}

// ReadMarker is a marker on the timeline (seconds).
type ReadMarker struct {
	At      float64 `json:"at"`
	Length  float64 `json:"length,omitempty"`
	Name    string  `json:"name"`
	Note    string  `json:"note,omitempty"`
	Chapter bool    `json:"chapter,omitempty"`
	Color   string  `json:"color,omitempty"`
	// Kind is MetaRush's own (caption, graphic, narration) on its exports.
	Kind string `json:"kind,omitempty"`
}

// Read is a timeline as another editing app wrote it.
type Read struct {
	Format     string  `json:"format"`
	FormatName string  `json:"formatName"` // "FCPXML 1.11"
	Name       string  `json:"name"`
	FPS        float64 `json:"fps"`
	Width      int     `json:"width,omitempty"`
	Height     int     `json:"height,omitempty"`
	// Start is the timeline's first timecode (seconds); positions are measured from it.
	Start  float64 `json:"start,omitempty"`
	Length float64 `json:"length"`
	// Video lanes from the bottom (the main one) up; Audio lanes in file order.
	Video   []*Lane      `json:"video"`
	Audio   []*Lane      `json:"audio"`
	Markers []ReadMarker `json:"markers,omitempty"`
	Sources []*Source    `json:"sources"`
	// Timelines names every timeline in the file (Name is the one read).
	Timelines []string `json:"timelines,omitempty"`
	// Unread says, in plain words, what the file has that wasn't read
	// (at most 100; UnreadMore counts the rest).
	Unread     []string `json:"unread,omitempty"`
	UnreadMore int      `json:"unreadMore,omitempty"`
	// Warnings are guesses the reader had to make (an EDL's frame rate).
	Warnings []string `json:"warnings,omitempty"`
	// MetaRush is MetaRush's own note on its exports (fps, size).
	MetaRush map[string]any `json:"-"`
	sources  map[string]*Source
	// What a read has done so far (readers stop past maxReadWork or
	// maxReadVisits).
	work       int
	visits     int
	over       bool
	unreadSeen map[string]bool
}

// Limits on one read, so a small file can't open up into an enormous one
// (compound clips and nested sequences used many times over) and a large one
// can't take minutes.
const (
	// maxReadWork is how many clips, titles, transitions and story elements a
	// read opens, counting each time a compound clip or nested sequence is
	// opened up again.
	maxReadWork = 100_000
	// maxXMLElements is how many XML elements a file may have.
	maxXMLElements = 500_000
	// maxReadVisits is how many XML elements (or keyframes) a read may go
	// through, counting a compound clip's or nested sequence's whole contents
	// again each time it is opened.
	maxReadVisits = 2_000_000
	// maxJSONValues is how many values an OpenTimelineIO file may have.
	maxJSONValues = 1_000_000
	// maxLevelKeys is how many level keyframes a piece keeps (they only say
	// that its level changes, which isn't carried over).
	maxLevelKeys = 256
	// maxTitleRunes is how much of a title's words a piece keeps.
	maxTitleRunes = 1000
	// maxUnread is how many things not read are listed one by one.
	maxUnread = 100
	// maxSeconds is the furthest time a timeline may name (about three years).
	maxSeconds = 1e8
)

// errTooBig is a timeline that opens up into more than a read takes.
func errTooBig() *ReadError {
	return &ReadError{Msg: "This timeline opens up into more than MetaRush reads: over 100,000 clips, titles and transitions, or over two million parts, once its compound clips and nested sequences are opened each time they're used.",
		Hint: "Export the timeline again with its compound clips and nested sequences broken apart, or a shorter part of it."}
}

// visit counts n elements a read goes through (a compound clip's whole
// contents each time it is opened) and reports whether the read may go on.
func (r *Read) visit(n int) bool {
	if r.over {
		return false
	}
	r.visits += n
	if r.visits > maxReadVisits {
		r.over = true
	}
	return !r.over
}

// addLevelKey keeps a level keyframe, at most maxLevelKeys a piece.
func addLevelKey(p *Piece, k LevelPoint) {
	if len(p.LevelKeys) < maxLevelKeys {
		p.LevelKeys = append(p.LevelKeys, k)
	}
}

// spend counts n units of a read's work and reports whether the read may go on.
func (r *Read) spend(n int) bool {
	if r.over {
		return false
	}
	r.work += n
	if r.work > maxReadWork {
		r.over = true
	}
	return !r.over
}

// number reads a decimal number a timeline names, refusing what isn't a
// finite number of a sane size (NaN, Inf, 1e400).
func number(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f) > maxSeconds*1000 {
		return 0, false
	}
	return f, true
}

// sane reports a time in seconds a timeline may name.
func sane(t float64) bool { return !math.IsNaN(t) && !math.IsInf(t, 0) && math.Abs(t) <= maxSeconds }

// ReadOptions steer a read.
type ReadOptions struct {
	// Name is the file's name (its extension helps tell the format).
	Name string
	// Timeline picks one by name when a file holds several (default: the first).
	Timeline string
	// FPS is an EDL's frame rate (EDLs don't say it; default: a guess, with a warning).
	FPS float64
}

// MaxReadBytes is the largest timeline file read (50 MB).
const MaxReadBytes = 50 << 20

// ErrNotTimeline is a file none of the readers recognise.
var ErrNotTimeline = errors.New("not a timeline MetaRush can read")

// ReadError says in plain words why a timeline couldn't be read.
type ReadError struct {
	Msg  string
	Hint string
	Err  error
}

func (e *ReadError) Error() string { return e.Msg }
func (e *ReadError) Unwrap() error { return e.Err }

func readErr(err error, format string, args ...any) *ReadError {
	return &ReadError{Msg: fmt.Sprintf(format, args...), Err: err}
}

// DetectFormat says which reader a file needs ("" none): by its first bytes,
// then its extension.
func DetectFormat(name string, data []byte) string {
	head := bytes.TrimLeft(data, " \t\r\n\ufeff")
	head = bytes.TrimPrefix(head, []byte("\xef\xbb\xbf"))
	switch {
	case len(head) > 0 && head[0] == '{':
		return FormatOTIO
	case len(head) > 0 && head[0] == '<':
		switch xmlRoot(head) {
		case "fcpxml":
			return FormatFCPXML
		case "xmeml":
			return FormatXMEML
		}
		return ""
	case looksLikeEDL(head):
		return FormatEDL
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".edl":
		return FormatEDL
	}
	return ""
}

// xmlRoot is the name of the first element.
func xmlRoot(data []byte) string {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	for i := 0; i < 64; i++ {
		tok, err := d.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local
		}
	}
	return ""
}

// IsAAF reports an AAF file (a binary compound document).
func IsAAF(name string, data []byte) bool {
	return bytes.HasPrefix(data, []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}) || strings.EqualFold(filepath.Ext(name), ".aaf")
}

// ReadTimeline reads a timeline file's bytes.
func ReadTimeline(data []byte, o ReadOptions) (*Read, error) {
	if len(data) > MaxReadBytes {
		return nil, &ReadError{Msg: "That timeline file is larger than 50 MB, which is more than a timeline needs.", Hint: "Check that it's the timeline and not a video."}
	}
	if IsAAF(o.Name, data) {
		return nil, &ReadError{Msg: "AAF timelines can't be read yet.", Hint: "Export the timeline from the editing app as OpenTimelineIO (.otio), FCPXML, Final Cut Pro 7 XML or an EDL instead.", Err: ErrNotTimeline}
	}
	if !utf8.Valid(data) {
		data = latin1(data)
	}
	var r *Read
	var err error
	switch DetectFormat(o.Name, data) {
	case FormatOTIO:
		r, err = readOTIO(data, o)
	case FormatFCPXML:
		r, err = readFCPXML(data, o)
	case FormatXMEML:
		r, err = readXMEML(data, o)
	case FormatEDL:
		r, err = readEDL(data, o)
	default:
		return nil, &ReadError{Msg: "MetaRush can't tell what kind of timeline this is.", Hint: "It reads OpenTimelineIO (.otio), FCPXML (.fcpxml, .fcpxmld), Final Cut Pro 7 XML (.xml) and CMX 3600 EDLs (.edl).", Err: ErrNotTimeline}
	}
	if r != nil && r.over {
		return nil, errTooBig()
	}
	if err != nil {
		return nil, err
	}
	r.finish()
	return r, nil
}

// latin1 turns bytes that aren't UTF-8 (older EDLs) into UTF-8.
func latin1(b []byte) []byte {
	var out strings.Builder
	for _, c := range b {
		out.WriteRune(rune(c))
	}
	return []byte(out.String())
}

func looksLikeEDL(b []byte) bool {
	s := string(b[:min(len(b), 4096)])
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		up := strings.ToUpper(line)
		if strings.HasPrefix(up, "TITLE:") || strings.HasPrefix(up, "FCM:") {
			return true
		}
		if edlEventRe.MatchString(line) {
			return true
		}
	}
	return false
}

func newRead(format, formatName string) *Read {
	return &Read{Format: format, FormatName: formatName, Video: []*Lane{}, Audio: []*Lane{}, Sources: []*Source{}, sources: map[string]*Source{}}
}

// source finds or adds a source by key.
func (r *Read) source(key string) *Source {
	if s := r.sources[key]; s != nil {
		return s
	}
	s := &Source{Key: key}
	r.sources[key] = s
	r.Sources = append(r.Sources, s)
	return s
}

// Source is a source by key (nil when unknown).
func (r *Read) Source(key string) *Source {
	if r.sources == nil {
		r.sources = map[string]*Source{}
		for _, s := range r.Sources {
			r.sources[s.Key] = s
		}
	}
	return r.sources[key]
}

func (r *Read) unread(format string, args ...any) {
	for i, x := range args {
		if s, ok := x.(string); ok {
			args[i] = clip(s, 120) // a name can be as long as the file
		}
	}
	msg := fmt.Sprintf(format, args...)
	if r.unreadSeen == nil {
		r.unreadSeen = map[string]bool{}
		for _, u := range r.Unread {
			r.unreadSeen[u] = true
		}
	}
	if r.unreadSeen[msg] {
		return
	}
	if len(r.Unread) >= maxUnread {
		r.UnreadMore++
		if r.UnreadMore < maxUnread*100 {
			r.unreadSeen[msg] = true // counted once
		}
		return
	}
	r.unreadSeen[msg] = true
	r.Unread = append(r.Unread, msg)
}

// finish sorts lanes, fills lengths, drops empty lanes and keeps names to a
// length people read (a name can be as long as the file, and what can't be
// carried over quotes them).
func (r *Read) finish() {
	end := 0.0
	for _, group := range [][]*Lane{r.Video, r.Audio} {
		for _, l := range group {
			l.Name = clip(l.Name, 120)
			for _, p := range l.Pieces {
				p.Name, p.Text = clip(p.Name, 200), clip(p.Text, maxTitleRunes)
				for i := range p.Markers {
					p.Markers[i].Name, p.Markers[i].Note = clip(p.Markers[i].Name, 200), clip(p.Markers[i].Note, 1000)
				}
			}
			sort.SliceStable(l.Pieces, func(i, j int) bool { return l.Pieces[i].At < l.Pieces[j].At })
			sort.SliceStable(l.Joins, func(i, j int) bool { return l.Joins[i].At < l.Joins[j].At })
			for _, p := range l.Pieces {
				if p.Speed == 0 {
					p.Speed = 1
				}
				end = math.Max(end, p.End())
			}
		}
	}
	if r.Length < end-1e-9 {
		r.Length = end
	}
	sort.SliceStable(r.Markers, func(i, j int) bool { return r.Markers[i].At < r.Markers[j].At })
	for i := range r.Markers {
		r.Markers[i].Name, r.Markers[i].Note = clip(r.Markers[i].Name, 200), clip(r.Markers[i].Note, 1000)
	}
	r.Name = clip(r.Name, 200)
	for i, t := range r.Timelines {
		r.Timelines[i] = clip(t, 200)
	}
	keep := func(ls []*Lane) []*Lane {
		out := []*Lane{}
		for _, l := range ls {
			if len(l.Pieces) > 0 {
				out = append(out, l)
			}
		}
		return out
	}
	r.Video, r.Audio = keep(r.Video), keep(r.Audio)
	if r.FPS <= 0 {
		r.FPS = 30
	}
	for _, s := range r.Sources {
		if s.File == "" && s.URL != "" {
			s.File = fileName(s.URL)
		}
		s.Name, s.File = clip(s.Name, 255), clip(s.File, 255)
	}
}

// fileFromURL is the local path of a file:// address ("" otherwise).
func fileFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "file") {
		if strings.HasPrefix(raw, "/") {
			return raw
		}
		return ""
	}
	if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
		return ""
	}
	p := u.Path
	// file:///C:/Users/… from a Windows computer.
	if len(p) > 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:]
	}
	return p
}

// fileName is the last part of an address or path.
func fileName(raw string) string {
	if p := fileFromURL(raw); p != "" {
		raw = p
	} else if u, err := url.Parse(raw); err == nil && u.Path != "" {
		raw, _ = url.PathUnescape(u.Path)
	}
	raw = strings.ReplaceAll(raw, "\\", "/")
	return path.Base(raw)
}

// xnode is a parsed XML element for reading (the writers use node).
type xnode struct {
	name     string
	attrs    [][2]string // name, value (a few: a slice is smaller than a map)
	children []*xnode
	text     string
	parent   *xnode
	// size is what opening it costs: its elements (its own included) and the
	// bytes they keep (one part per bytesPerPart).
	size int
}

func (n *xnode) attr(k string) string {
	for _, a := range n.attrs {
		if a[0] == k {
			return a[1]
		}
	}
	return ""
}

func (n *xnode) child(name string) *xnode {
	for _, c := range n.children {
		if c.name == name {
			return c
		}
	}
	return nil
}

func (n *xnode) all(name string) []*xnode {
	var out []*xnode
	for _, c := range n.children {
		if c.name == name {
			out = append(out, c)
		}
	}
	return out
}

// path follows child names ("rate/timebase").
func (n *xnode) path(p string) *xnode {
	cur := n
	for _, part := range strings.Split(p, "/") {
		if cur == nil {
			return nil
		}
		cur = cur.child(part)
	}
	return cur
}

func (n *xnode) textOf(p string) string {
	if c := n.path(p); c != nil {
		return strings.TrimSpace(c.text)
	}
	return ""
}

// parseXML reads a document into a tree. Go's decoder never fetches external
// entities or expands DTD declarations, so a hostile file can't reach out or
// blow up; elements nest at most 256 deep and number at most maxXMLElements.
func parseXML(data []byte) (*xnode, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	d.Entity = xml.HTMLEntity
	d.CharsetReader = func(charset string, in io.Reader) (io.Reader, error) { return in, nil }
	var root, cur *xnode
	// Each open element's text, kept apart so long text in many pieces is
	// joined once.
	var texts []*strings.Builder
	elements := 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(texts) >= 256 {
				return nil, errors.New("the XML nests too deeply")
			}
			if elements++; elements > maxXMLElements {
				return nil, errTooManyElements
			}
			n := &xnode{name: t.Name.Local, parent: cur}
			if len(t.Attr) > 0 {
				n.attrs = make([][2]string, 0, len(t.Attr))
			}
			for _, a := range t.Attr {
				v := keepBytes(a.Value, maxAttrBytes)
				n.attrs = append(n.attrs, [2]string{a.Name.Local, v})
				n.size += len(v) / bytesPerPart
			}
			if cur == nil {
				if root != nil {
					return root, nil
				}
				root = n
			} else {
				cur.children = append(cur.children, n)
			}
			cur = n
			texts = append(texts, nil)
		case xml.EndElement:
			if cur != nil {
				if b := texts[len(texts)-1]; b != nil {
					cur.text = keepBytes(b.String(), maxTextBytes)
					cur.size += len(cur.text) / bytesPerPart
				}
				texts = texts[:len(texts)-1]
				cur.size++
				if cur.parent != nil {
					cur.parent.size += cur.size
				}
				cur = cur.parent
			}
		case xml.CharData:
			if cur != nil {
				b := texts[len(texts)-1]
				if b == nil {
					b = &strings.Builder{}
					texts[len(texts)-1] = b
				}
				if room := maxTextBytes + utf8.UTFMax - b.Len(); room > 0 {
					b.Write(t[:min(len(t), room)]) // what is past the cap is never kept
				}
			}
		}
	}
	if root == nil {
		return nil, errors.New("no XML element")
	}
	// An element never closed keeps the text it had.
	for n, i := cur, len(texts)-1; n != nil && i >= 0; n, i = n.parent, i-1 {
		if b := texts[i]; b != nil {
			n.text = keepBytes(b.String(), maxTextBytes)
		}
	}
	return root, nil
}

// What a timeline's XML keeps of each value, at the decoder: names, effects,
// markers and numbers are short; a file's address or a title's words are the
// longest a timeline needs. Anything longer is cut there, so nothing read
// later (and again on each opening of a compound clip) costs more.
const (
	maxAttrBytes = 2048
	maxTextBytes = 4096
	// bytesPerPart: an element's kept bytes count towards what opening it
	// costs (xnode.size) at one part per this many bytes.
	bytesPerPart = 16
)

// keepBytes is s cut to at most n bytes at a character boundary (a copy, so
// the rest isn't held).
func keepBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.Clone(s[:n])
}

// errTooManyElements is an XML file with more elements than a timeline has.
var errTooManyElements = errors.New("it has more than 500,000 XML elements, more than a timeline has")

// noteValue reads one key of MetaRush's own note on a file (its clip ID and
// its bytes' SHA-256), keeping only values of their shape.
func noteValue(key, value, assetID, sha string) (string, string) {
	value = strings.TrimSpace(value)
	switch key {
	case NoteAssetID:
		if len(value) <= 64 && strings.HasPrefix(value, "ast_") && !strings.ContainsAny(value, " \t\r\n/") {
			assetID = value
		}
	case NoteSHA256:
		if len(value) == 64 && strings.Trim(strings.ToLower(value), "0123456789abcdef") == "" {
			sha = strings.ToLower(value)
		}
	}
	return assetID, sha
}

// near reports two times within a microsecond.
func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// ntscRate is a frame rate as editing apps mean it: 29.97 is 30000/1001.
func ntscRate(fps float64) float64 {
	for _, base := range []float64{24, 30, 48, 60, 120} {
		if math.Abs(fps-base*1000/1001) < 0.01 {
			return base * 1000 / 1001
		}
	}
	return fps
}

// IsTimelineName reports a file name a timeline is exported under (.xml only
// when its first element says xmeml; pass the head of the file, or nil).
func IsTimelineName(name string, head []byte) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".otio", ".fcpxml", ".fcpxmld", ".edl":
		return true
	case ".xml":
		return head != nil && xmlRoot(head) == "xmeml"
	}
	return false
}
