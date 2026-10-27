package interchange

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Reading CMX 3600 edit decision lists. An event says which source (a reel
// or a clip named in a comment) plays from source in to out at record in to
// out, on which channels, after a cut, a dissolve or a wipe. An EDL doesn't
// say its frame rate: the caller gives it, or it is guessed from the
// timecodes with a warning. Source timecodes are the files' own, which an
// EDL doesn't tie to the file's first frame (Source.TimecodeOnly).

var (
	edlEventRe   = regexp.MustCompile(`^(\d{1,6})\s+(\S+)\s+(\S+)\s+(C|D|W\d{0,3}|K\s*[BO]?|KB|KO)\s+(?:(\d{1,4})\s+)?(\d{1,2}[:;.]\d{2}[:;.]\d{2}[:;.]\d{2})\s+(\d{1,2}[:;.]\d{2}[:;.]\d{2}[:;.]\d{2})\s+(\d{1,2}[:;.]\d{2}[:;.]\d{2}[:;.]\d{2})\s+(\d{1,2}[:;.]\d{2}[:;.]\d{2}[:;.]\d{2})`)
	edlTCRe      = regexp.MustCompile(`(\d{1,2})[:;.](\d{2})[:;.](\d{2})([:;.])(\d{2})`)
	edlCommentRe = regexp.MustCompile(`^(FROM CLIP NAME|TO CLIP NAME|FROM CLIP|TO CLIP|SOURCE FILE|LOC):`)
	edlM2Re      = regexp.MustCompile(`^M2\s+(.+?)\s+(-?\d+(?:\.\d+)?)\s+(\d{1,2}[:;.]\d{2}[:;.]\d{2}[:;.]\d{2})\s*$`)
)

type edlEvent struct {
	num, reel, channels, trans string
	dur                        int
	src, rec                   [2]string
	clipName, toName, file     string
	fromFile, toFile           string
	speedFPS                   float64
	hasSpeed                   bool
	markers                    []ReadMarker
	locs                       []string
}

// edlTC is a timecode in frames: base frames per second, dropFrame for 29.97 DF.
func edlTC(tc string, base int, drop bool) (int, bool) {
	m := edlTCRe.FindStringSubmatch(tc)
	if m == nil {
		return 0, false
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	s, _ := strconv.Atoi(m[3])
	f, _ := strconv.Atoi(m[5])
	frames := ((h*60+mi)*60+s)*base + f
	if drop || m[4] == ";" || m[4] == "." {
		// Drop-frame: two frame numbers (four at 60) skipped each minute but every tenth.
		d := base / 15
		total := 60*h + mi
		frames -= d * (total - total/10)
	}
	return frames, true
}

func readEDL(data []byte, o ReadOptions) (*Read, error) {
	r := newRead(FormatEDL, "CMX 3600 EDL")
	var events []*edlEvent
	drop := false
	maxFrame := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var last *edlEvent
	var pendingM2 []struct {
		reel string
		fps  float64
	}
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		trim := strings.TrimSpace(line)
		up := strings.ToUpper(trim)
		switch {
		case trim == "":
		case strings.HasPrefix(up, "TITLE:"):
			r.Name = strings.TrimSpace(trim[6:])
		case strings.HasPrefix(up, "FCM:"):
			drop = strings.Contains(up, "DROP") && !strings.Contains(up, "NON")
		case edlEventRe.MatchString(trim):
			m := edlEventRe.FindStringSubmatch(trim)
			ev := &edlEvent{num: m[1], reel: m[2], channels: strings.ToUpper(m[3]), trans: strings.ToUpper(strings.ReplaceAll(m[4], " ", "")), src: [2]string{m[6], m[7]}, rec: [2]string{m[8], m[9]}}
			ev.dur, _ = strconv.Atoi(m[5])
			for _, tc := range []string{m[6], m[7], m[8], m[9]} {
				if strings.ContainsAny(tc, ";.") {
					drop = true
				}
				if mm := edlTCRe.FindStringSubmatch(tc); mm != nil {
					f, _ := strconv.Atoi(mm[5])
					maxFrame = max(maxFrame, f)
				}
			}
			for _, p := range pendingM2 {
				if sameReel(p.reel, ev.reel) {
					ev.speedFPS, ev.hasSpeed = p.fps, true
				}
			}
			pendingM2 = nil
			if !r.spend(1) {
				return r, nil
			}
			events = append(events, ev)
			last = ev
		case edlM2Re.MatchString(trim):
			m := edlM2Re.FindStringSubmatch(trim)
			fps, _ := strconv.ParseFloat(m[2], 64)
			if last != nil && sameReel(last.reel, m[1]) {
				last.speedFPS, last.hasSpeed = fps, true
			} else {
				pendingM2 = append(pendingM2, struct {
					reel string
					fps  float64
				}{m[1], fps})
			}
		case strings.HasPrefix(trim, "*") || edlCommentRe.MatchString(up):
			if last == nil {
				continue
			}
			body := strings.TrimSpace(strings.TrimPrefix(trim, "*"))
			ub := strings.ToUpper(body)
			switch {
			case strings.HasPrefix(ub, "FROM CLIP NAME:"):
				last.clipName = strings.TrimSpace(body[len("FROM CLIP NAME:"):])
			case strings.HasPrefix(ub, "TO CLIP NAME:"):
				last.toName = strings.TrimSpace(body[len("TO CLIP NAME:"):])
			case strings.HasPrefix(ub, "SOURCE FILE:"):
				last.file = strings.TrimSpace(body[len("SOURCE FILE:"):])
			case strings.HasPrefix(ub, "TO CLIP:"):
				last.toFile = strings.TrimSpace(body[len("TO CLIP:"):])
			case strings.HasPrefix(ub, "FROM CLIP:"):
				last.fromFile = strings.TrimSpace(body[len("FROM CLIP:"):])
			case strings.HasPrefix(ub, "LOC:"):
				last.locs = append(last.locs, strings.TrimSpace(body[len("LOC:"):]))
			}
		default:
			// SPLIT, AUD and other lines MetaRush doesn't use.
			if !strings.HasPrefix(up, "SPLIT") && !strings.HasPrefix(up, "AUD") && !strings.HasPrefix(up, ">>>") && !strings.HasPrefix(up, "EFFECTS NAME IS") {
				r.unread("a line it doesn't understand (“%s”)", clip(trim, 40))
			}
		}
	}
	if len(events) == 0 {
		return nil, &ReadError{Msg: "This EDL has no events in it.", Err: ErrNotTimeline}
	}
	fps, base := o.FPS, 0
	switch {
	case fps > 0:
		fps = ntscRate(fps)
		base = int(math.Round(fps))
	case drop:
		fps, base = 30000.0/1001, 30
		r.Warnings = append(r.Warnings, "This EDL doesn't say its frame rate; its drop-frame timecode means 29.97 frames a second, so it was read at that.")
	default:
		switch {
		case maxFrame >= 25:
			fps = 30
		case maxFrame >= 24:
			fps = 25
		default:
			fps = 24
		}
		base = int(fps)
		r.Warnings = append(r.Warnings, fmt.Sprintf("This EDL doesn't say its frame rate; it was read at %g frames a second (its timecodes count up to frame %d). If that's wrong, say the rate.", fps, maxFrame))
	}
	r.FPS = fps
	sec := func(tc string) float64 {
		f, _ := edlTC(tc, base, drop && base == 30)
		return float64(f) / fps
	}
	r.Start = math.Inf(1)
	for _, ev := range events {
		r.Start = math.Min(r.Start, sec(ev.rec[0]))
	}
	lanes := map[string]*Lane{}
	laneFor := func(kind, name string) *Lane {
		if l := lanes[kind+name]; l != nil {
			return l
		}
		l := &Lane{Name: name, Kind: kind}
		lanes[kind+name] = l
		if kind == KindVideo {
			r.Video = append(r.Video, l)
		} else {
			r.Audio = append(r.Audio, l)
		}
		return l
	}
	for i, ev := range events {
		at := sec(ev.rec[0]) - r.Start
		length := sec(ev.rec[1]) - sec(ev.rec[0])
		srcIn := sec(ev.src[0])
		if length <= 0 {
			continue // the outgoing side of a dissolve or wipe (zero length)
		}
		fromBlack := i > 0 && events[i-1].num == ev.num && isBlack(events[i-1].reel)
		if isBlack(ev.reel) {
			// Black: nothing plays; a dissolve into it fades the picture before out.
			if ev.trans == "D" && ev.dur > 0 {
				if l := lanes[KindVideo+"V"]; l != nil && len(l.Pieces) > 0 {
					prev := l.Pieces[len(l.Pieces)-1]
					if near(prev.End(), at) {
						d := float64(ev.dur) / fps
						prev.Length += d // it plays on into the dissolve
						l.Joins = append(l.Joins, Join{At: prev.End(), Before: d, Kind: TransFadeOut, Name: "Dissolve to black"})
					}
				}
			}
			continue
		}
		speed := 1.0
		if ev.hasSpeed {
			speed = ev.speedFPS / fps
			if speed == 0 {
				r.unread("the freeze frame in event %s", ev.num)
				speed = 1
			}
			if speed < 0 {
				r.unread("event %s plays backwards", ev.num)
				speed = -speed
			}
		}
		name := firstNonEmptyStr(ev.toName, ev.clipName, ev.reel)
		if ev.trans == "D" || strings.HasPrefix(ev.trans, "W") {
			// The incoming side is TO CLIP NAME when both are given.
			name = firstNonEmptyStr(ev.toName, ev.clipName, ev.reel)
		}
		if strings.HasPrefix(ev.trans, "K") {
			r.unread("the key (a picture over another) in event %s", ev.num)
		}
		key := "edl:" + strings.ToLower(name)
		s := r.source(key)
		if s.Name == "" {
			s.Name = name
			if filepath.Ext(name) != "" {
				s.File = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
			}
			file := ev.file
			if file == "" && (ev.trans == "D" || strings.HasPrefix(ev.trans, "W")) {
				file = ev.toFile
			}
			if file == "" {
				file = firstNonEmptyStr(ev.fromFile, ev.toFile)
			}
			if file != "" {
				ev.file = file
				s.URL = ev.file
				s.Path = fileFromURL(ev.file)
				if s.Path == "" && filepath.IsAbs(ev.file) {
					s.Path = ev.file
				}
				s.File = fileName(ev.file)
			}
			s.TimecodeOnly = true
			s.HasVideo, s.HasAudio = true, true
		}
		group := "edl:" + ev.num + ":" + strings.ToLower(name)
		video := strings.Contains(ev.channels, "V") || ev.channels == "B"
		var audio []string
		ch := strings.ReplaceAll(ev.channels, "/V", "")
		switch {
		case ev.channels == "B":
			audio = []string{"A1"}
		case ch == "AA" || strings.HasPrefix(ch, "AA"):
			audio = []string{"A1", "A2"}
		case strings.HasPrefix(ch, "A"):
			for _, part := range strings.Split(ch, "/") {
				part = strings.TrimSpace(part)
				if part == "A" {
					part = "A1"
				}
				if strings.HasPrefix(part, "A") {
					audio = append(audio, part)
				}
			}
		}
		mk := func() *Piece {
			return &Piece{Kind: PieceClip, Name: name, Source: key, At: at, Length: length, In: srcIn, Speed: speed, Group: group}
		}
		if video {
			p := mk()
			laneFor(KindVideo, "V").Pieces = append(laneFor(KindVideo, "V").Pieces, p)
			if ev.trans == "D" || strings.HasPrefix(ev.trans, "W") {
				kind := TransDissolve
				if strings.HasPrefix(ev.trans, "W") {
					kind = TransWipe
				}
				j := Join{At: at, Before: 0, After: float64(ev.dur) / fps, Kind: kind, Name: map[string]string{TransDissolve: "Dissolve", TransWipe: "Wipe"}[kind]}
				if fromBlack || i == 0 {
					j.Kind = TransFadeIn
				}
				laneFor(KindVideo, "V").Joins = append(laneFor(KindVideo, "V").Joins, j)
			}
		}
		for _, a := range audio {
			p := mk()
			l := laneFor(KindAudio, a)
			l.Pieces = append(l.Pieces, p)
			if ev.trans == "D" && !video {
				l.Joins = append(l.Joins, Join{At: at, After: float64(ev.dur) / fps, Kind: TransAudioCrossfade, Name: "Cross fade"})
			}
		}
		if !video && len(audio) == 0 {
			r.unread("event %s plays on channels MetaRush doesn't know (“%s”)", ev.num, ev.channels)
		}
		for _, loc := range ev.locs {
			fields := strings.Fields(loc)
			if len(fields) == 0 {
				continue
			}
			mk := ReadMarker{At: sec(fields[0]) - r.Start}
			rest := fields[1:]
			if len(rest) > 0 && isEDLColor(rest[0]) {
				mk.Color = strings.ToLower(rest[0])
				rest = rest[1:]
			}
			mk.Name = strings.Join(rest, " ")
			mk.Chapter = isChapterName(mk.Name)
			r.Markers = append(r.Markers, mk)
		}
	}
	if math.IsInf(r.Start, 1) {
		r.Start = 0
	}
	return r, nil
}

// isBlack: the reel names EDLs use for black.
func isBlack(reel string) bool {
	switch strings.ToUpper(reel) {
	case "BL", "BLK", "BLACK":
		return true
	}
	return false
}

// sameReel: reel names match, one maybe cut short to eight characters.
func sameReel(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if strings.EqualFold(a, b) {
		return true
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return len(a) >= 6 && strings.HasPrefix(strings.ToLower(b), strings.ToLower(a))
}

func isEDLColor(s string) bool {
	switch strings.ToUpper(s) {
	case "RED", "GREEN", "BLUE", "CYAN", "MAGENTA", "YELLOW", "BLACK", "WHITE", "ORANGE", "PURPLE", "PINK":
		return true
	}
	return false
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s // no more bytes than runes allowed
	}
	count := 0
	for j := range s {
		if count == n-1 {
			// More than one rune left: cut here (without counting the rest).
			if _, size := utf8.DecodeRuneInString(s[j:]); size < len(s[j:]) {
				return s[:j] + "…"
			}
			return s
		}
		count++
	}
	return s
}
