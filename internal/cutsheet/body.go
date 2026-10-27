package cutsheet

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/metavert-io/metarush/internal/editspec"
)

var (
	headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	clipRe    = regexp.MustCompile(`^(\d+)[.)](\s+|$)`)
	bulletRe  = regexp.MustCompile(`^[-*+](\s+|$)`)
	transRe   = regexp.MustCompile(`^(↳|=>)\s*`)
	labelRe   = regexp.MustCompile(`(?i)^(why|evidence|rights|notes|delivery|take|shot|member|change)\s*:\s?`)
)

var sectionNames = map[string]string{
	"clips": "clips", "shots": "clips", "cut": "clips", "the cut": "clips",
	"graphics": "graphics", "overlays": "graphics", "motion graphics": "graphics", "titles": "graphics",
	"sound": "sound", "music": "sound", "music and sound": "sound", "music & sound": "sound", "audio": "sound", "sounds": "sound",
	"captions": "captions", "subtitles": "captions",
	"voiceover": "voiceover", "voice over": "voiceover", "narration": "voiceover",
	"conversations": "conversations", "conversation": "conversations", "calls": "conversations",
}

// indentCol is the 1-based column of the first non-space rune.
func indentCol(raw string) (string, int) {
	rs := []rune(raw)
	i := 0
	for i < len(rs) && (rs[i] == ' ' || rs[i] == '\t') {
		i++
	}
	return string(rs[i:]), i + 1
}

func (p *parser) body(lines []string, first int) {
	section := ""
	var cur *item       // item receiving block lines
	var pending *item   // ↳ waiting for its clip
	var afterTrans bool // the last non-blank line was a ↳
	numbersWarned := false
	for idx, raw := range lines {
		ln := first + idx
		// Inside the original script's fence every line is the script's own,
		// exactly as written, until the same fence closes it.
		if p.origFence != "" {
			if raw == p.origFence {
				s := strings.Join(p.origLines, "\n")
				p.original, p.origFence, p.origLines = &s, "", nil
				continue
			}
			p.origLines = append(p.origLines, raw)
			continue
		}
		text, col := indentCol(raw)
		trimmed := strings.TrimRight(text, " \t")
		if p.origWait && trimmed != "" {
			p.origWait = false
			if m := fenceRe.FindStringSubmatch(trimmed); m != nil && col == 1 {
				p.origFence, p.origLines = m[1], nil
				continue
			}
			p.errAt(ln, col, col+len([]rune(trimmed)), "missing_fence", "The original script goes between two fence lines (```) under its heading.", "Like:\n### Original script\n\n```text\nyour script, exactly as you wrote it\n```")
		}
		if trimmed == "" {
			if cur != nil {
				cur.events = append(cur.events, noteEvent{kind: 1, line: ln})
			} else if section == "" {
				p.notes = append(p.notes, noteEvent{kind: 1, line: ln})
			}
			continue
		}
		if afterTrans && !clipRe.MatchString(trimmed) && !transRe.MatchString(trimmed) && !headingRe.MatchString(trimmed) {
			p.errAt(ln, col, col+len([]rune(trimmed)), "not_attached", "Only a clip can follow a transition (↳) line.", "Move this text under a clip, or the ↳ line just above the clip it introduces.")
			afterTrans = false
			continue
		}
		if m := headingRe.FindStringSubmatch(trimmed); m != nil && !strings.HasPrefix(trimmed, "#!") {
			if pending != nil {
				p.errAt(pending.line, 1, 2, "dangling_transition", "This transition isn't followed by a clip.", "A ↳ line goes right above the clip it brings in.")
				pending, afterTrans = nil, false
			}
			cur = nil
			name := strings.ToLower(strings.TrimSpace(m[2]))
			if len(m[1]) == 1 {
				if section != "" {
					p.errAt(ln, col, col+len([]rune(trimmed)), "misplaced_title", "A # title heading only belongs at the top of the sheet.", "Section headings use two hashes: ## Clips, ## Graphics, ## Sound, ## Captions.")
					continue
				}
				p.h1(strings.TrimSpace(m[2]), ln, col)
				continue
			}
			if len(m[1]) == 3 && section == "voiceover" && originalHeading[name] {
				if p.originalLine > 0 {
					p.errAt(ln, col, col+len([]rune(trimmed)), "duplicate", fmt.Sprintf("The original script is given twice (the first is on line %d).", p.originalLine), "Keep one.")
					continue
				}
				p.originalLine, p.origWait = ln, true
				continue
			}
			if len(m[1]) != 2 {
				p.errAt(ln, col, col+len([]rune(trimmed)), "unknown_heading", "Only ## section headings are used in a cut sheet (and ### Original script under ## Voiceover).", "Use ## Clips, ## Voiceover, ## Graphics, ## Sound or ## Captions; to keep this line as a note, start it with \\.")
				continue
			}
			sec, ok := sectionNames[name]
			if !ok {
				p.errAt(ln, col+3, col+len([]rune(trimmed)), "unknown_section", fmt.Sprintf("MetaRush doesn't know a section called %q.", strings.TrimSpace(m[2])), "Sections are ## Clips, ## Voiceover, ## Graphics, ## Sound and ## Captions.")
				section = "unknown"
				continue
			}
			if sec == "voiceover" {
				p.voSection = true
				if _, ok := p.sm.Keys["voiceover"]; !ok {
					p.sm.Keys["voiceover"] = Span{Line: ln, Col: col, EndCol: col + len([]rune(trimmed))}
				}
			}
			if prev, dup := p.sections[sec]; dup {
				p.errAt(ln, col, col+len([]rune(trimmed)), "duplicate_section", fmt.Sprintf("There are two ## %s sections (the first is on line %d).", strings.TrimSpace(m[2]), prev), "Merge them into one.")
			}
			p.sections[sec] = ln
			section = sec
			if sec == "conversations" {
				p.convSection = ln
			}
			if sec == "clips" {
				p.hasClips = true
				p.sm.Keys["clips"] = Span{Line: ln, Col: col, EndCol: col + len([]rune(trimmed))}
			}
			continue
		}
		switch section {
		case "":
			p.introLine(trimmed, ln, col)
			continue
		case "unknown":
			continue
		case "clips":
			if m := transRe.FindStringIndex(trimmed); m != nil {
				if pending != nil {
					p.errAt(ln, col, col+1, "duplicate", "Two transitions in a row.", "Keep one ↳ line above each clip.")
				}
				rs := []rune(trimmed)
				skip := len([]rune(trimmed[:m[1]]))
				pending = p.transitionLine(rs[skip:], ln, col+skip)
				cur, afterTrans = nil, true
				continue
			}
			if m := clipRe.FindStringIndex(trimmed); m != nil {
				rs := []rune(trimmed)
				skip := len([]rune(trimmed[:m[1]]))
				if n := clipRe.FindStringSubmatch(trimmed)[1]; n != fmt.Sprint(len(p.clips)+1) && !numbersWarned {
					numbersWarned = true
					p.warnAt(ln, col, col+len(n), "clip_numbers", fmt.Sprintf("This clip is numbered %s but it is clip %d in the list.", n, len(p.clips)+1), "The order of the lines is what counts; MetaRush renumbers clips when it writes the sheet.")
				}
				it := p.clipLine(rs[skip:], ln, col+skip)
				it.trans = pending
				pending, afterTrans = nil, false
				p.clips = append(p.clips, it)
				cur = it
				continue
			}
		case "voiceover":
			if m := recordingRe.FindStringIndex(trimmed); m != nil {
				rs := []rune(trimmed)
				skip := len([]rune(trimmed[:m[1]]))
				it := p.recordingLine(rs[skip:], ln, col+skip)
				p.recordings = append(p.recordings, it)
				cur = it
				continue
			}
			if m := bulletRe.FindStringIndex(trimmed); m != nil {
				rs := []rune(trimmed)
				skip := len([]rune(trimmed[:m[1]]))
				it := p.beatLine(rs[skip:], ln, col+skip)
				p.beats = append(p.beats, it)
				cur = it
				continue
			}
		case "conversations":
			if m := bulletRe.FindStringIndex(trimmed); m != nil {
				rs := []rune(trimmed)
				skip := len([]rune(trimmed[:m[1]]))
				it := p.convLine(rs[skip:], ln, col+skip)
				p.convs = append(p.convs, it)
				cur = it
				continue
			}
		case "graphics", "sound", "captions":
			if m := bulletRe.FindStringIndex(trimmed); m != nil {
				rs := []rune(trimmed)
				skip := len([]rune(trimmed[:m[1]]))
				var it *item
				switch section {
				case "graphics":
					it = p.overlayLine(rs[skip:], ln, col+skip)
					p.overlays = append(p.overlays, it)
				case "sound":
					it = p.soundLine(rs[skip:], ln, col+skip)
					p.audio = append(p.audio, it)
				default:
					it = p.segmentLine(rs[skip:], ln, col+skip)
					p.segments = append(p.segments, it)
				}
				cur = it
				continue
			}
		}
		if cur == nil {
			p.errAt(ln, col, col+len([]rune(trimmed)), "not_attached", "This line isn't under a clip, graphic or sound.", "Put notes about the whole cut above ## Clips; notes about one clip go right under it.")
			continue
		}
		p.blockLine(cur, trimmed, ln, col)
	}
	if pending != nil {
		p.errAt(pending.line, 1, 2, "dangling_transition", "This transition isn't followed by a clip.", "A ↳ line goes right above the clip it brings in.")
	}
	switch {
	case p.origFence != "":
		p.errAt(p.originalLine, 1, 4, "fence_not_closed", "The original script's fence is never closed.", "End it with a line of just "+p.origFence+".")
		p.origFence = ""
	case p.origWait:
		p.errAt(p.originalLine, 1, 4, "missing_fence", "The original script goes between two fence lines (```) under its heading.", "")
	}
}

// The original script (D86): a ### heading under ## Voiceover and the text
// between fences (three or more backticks, the same number to close).
var (
	originalHeading = map[string]bool{"original script": true, "original": true, "the original script": true, "your original script": true}
	fenceRe         = regexp.MustCompile("^(`{3,})[A-Za-z0-9_-]*$")
)

// originalFence is the fence that can hold s exactly (longer than any run of
// backticks in it), or "" when s can't be written between fences unchanged
// (carriage returns or other control characters).
func originalFence(s string) string {
	longest, run := 0, 0
	for _, r := range s {
		if r == '\r' || (unicode.IsControl(r) && r != '\n' && r != '\t') || r == 0xFEFF {
			return ""
		}
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}

func (p *parser) h1(title string, ln, col int) {
	switch {
	case p.title == nil:
		p.title = &title
		p.titleSp = Span{Line: ln, Col: col, EndCol: col + len([]rune(title)) + 2}
	case *p.title != title:
		p.errAt(ln, col, col+len([]rune(title))+2, "title_mismatch", fmt.Sprintf("The heading says %q but the settings say the title is %q.", title, *p.title), "Change one so they match (or remove the heading; the title lives in the settings).")
	}
}

// introLine is a line above ## Clips: the treatment notes for the whole cut.
func (p *parser) introLine(t string, ln, col int) {
	if m := labelRe.FindStringSubmatch(t); m != nil {
		label := strings.ToLower(m[1])
		rest := t[len(m[0]):]
		if label == "notes" {
			if s, ok := exactString(rest); ok {
				if p.exact != nil {
					p.errAt(ln, col, col+5, "duplicate", "The cut's notes are given twice.", "")
				}
				p.exact = &s
				p.sm.Keys["notes"] = Span{Line: ln, Col: col, EndCol: col + len([]rune(t))}
				return
			}
			t = rest
		} else {
			p.errAt(ln, col, col+len([]rune(m[0])), "not_attached", fmt.Sprintf("%s: lines belong under a clip, graphic or sound.", strings.TrimSpace(m[1])), "Move it under the item it is about, or start the line with \\ to keep it as a note.")
			return
		}
	}
	if strings.HasPrefix(t, ">") {
		p.errAt(ln, col, col+1, "quote_outside", "Lines starting with > are filled in by MetaRush and only appear under clips and graphics.", "To keep this as a note, start the line with \\>.")
		return
	}
	if _, ok := p.sm.Keys["notes"]; !ok {
		p.sm.Keys["notes"] = Span{Line: ln, Col: col, EndCol: col + len([]rune(t))}
	}
	p.notes = append(p.notes, noteEvent{kind: 0, text: unescapeNote(t), line: ln})
}

func unescapeNote(t string) string { return strings.TrimPrefix(t, `\`) }

// exactString reads `"…"` when it is one complete JSON string literal.
func exactString(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, `"`) || !strings.HasSuffix(s, `"`) || len(s) < 2 {
		return "", false
	}
	var out string
	if json.Unmarshal([]byte(s), &out) != nil {
		return "", false
	}
	return out, true
}

// blockLine is a line under an item: derived quote, Why, Evidence, Rights,
// Notes or plain note text.
func (p *parser) blockLine(it *item, t string, ln, col int) {
	if strings.HasPrefix(t, ">") {
		if it.kind == "clip" || it.kind == "overlay" || it.kind == "beat" {
			it.events = append(it.events, noteEvent{kind: 2, line: ln})
			return // derived: regenerated on export, ignored here
		}
		p.errAt(ln, col, col+1, "quote_outside", "Lines starting with > are filled in by MetaRush and only appear under clips and graphics.", "To keep this as a note, start the line with \\>.")
		return
	}
	if m := labelRe.FindStringSubmatch(t); m != nil {
		label := strings.ToLower(m[1])
		restRunes := []rune(t[len(m[0]):])
		restCol := col + len([]rune(m[0]))
		rest := string(restRunes)
		it.events = append(it.events, noteEvent{kind: 2, line: ln})
		switch label {
		case "why":
			if _, dup := it.m["rationale"]; dup {
				p.errAt(ln, col, col+4, "duplicate", "There are two Why: lines for "+it.what()+".", "Keep one (it can be long).")
				return
			}
			why := strings.TrimSpace(rest)
			if s, ok := exactString(why); ok {
				why = s
			}
			if why == "" {
				p.errAt(ln, col, col+4, "bad_value", "Why: needs a reason after it.", "")
				return
			}
			if it.kind == "segment" || it.kind == "recording" || it.kind == "conversation" {
				p.errAt(ln, col, col+4, "not_supported", "Caption lines, the recording and conversations don't have a Why:.", "")
				return
			}
			it.m["rationale"] = json.RawMessage(quote(why))
			it.spans["rationale"] = Span{Line: ln, Col: col, EndCol: col + len([]rune(t))}
		case "evidence":
			if it.kind != "clip" && it.kind != "overlay" && it.kind != "beat" {
				p.errAt(ln, col, col+9, "not_supported", "Only clips, graphics and narration lines have Evidence: lines.", "")
				return
			}
			if ev, ok := p.evidenceLine(restRunes, ln, restCol); ok {
				it.evidence = append(it.evidence, ev)
			}
		case "rights":
			if it.kind != "audio" {
				p.errAt(ln, col, col+7, "not_supported", "Only sounds have a Rights: line.", "")
				return
			}
			p.rightsLine(it, restRunes, ln, restCol)
		case "delivery":
			if it.kind != "beat" {
				p.errAt(ln, col, col+9, "not_supported", "Only narration lines have a Delivery: line.", "")
				return
			}
			d := strings.TrimSpace(rest)
			if s, ok := exactString(d); ok {
				d = s
			}
			if d == "" {
				p.errAt(ln, col, col+9, "bad_value", "Delivery: needs a note on how to say the line, like warm, a little slower.", "")
				return
			}
			p.set(it, "delivery", json.RawMessage(quote(d)), Span{Line: ln, Col: col, EndCol: col + len([]rune(t))}, "Delivery")
		case "take":
			p.takeLine(it, restRunes, ln, restCol, col)
		case "shot":
			if it.kind != "clip" || !editspec.IsConversation(it.assetID) {
				p.errAt(ln, col, col+5, "not_supported", "Only a conversation clip has Shot: lines.", "A conversation clip names its conversation: 1. talk: conversation call 0:00 → 12:30.")
				return
			}
			p.shotLine(it, restRunes, ln, restCol)
		case "change":
			if it.kind != "overlay" {
				p.errAt(ln, col, col+7, "not_supported", "Only graphics have Change: lines.", "A Change: line under a graphic says from which moment of a footage its fields change, like Change: at lena-call 2:15.4 · with name \"Lena\".")
				return
			}
			p.changeLine(it, restRunes, ln, restCol)
		case "member":
			if it.kind != "conversation" {
				p.errAt(ln, col, col+7, "not_supported", "Member: lines belong under a conversation in ## Conversations.", "")
				return
			}
			p.memberLine(it, restRunes, ln, restCol)
		case "notes":
			if s, ok := exactString(rest); ok {
				if it.exact != nil {
					p.errAt(ln, col, col+6, "duplicate", "There are two Notes: lines for "+it.what()+".", "")
					return
				}
				it.exact, it.exactLine = &s, ln
				return
			}
			it.events = it.events[:len(it.events)-1]
			it.events = append(it.events, noteEvent{kind: 0, text: strings.TrimSpace(rest), line: ln})
		}
		return
	}
	if it.kind == "segment" {
		p.errAt(ln, col, col+len([]rune(t)), "not_attached", "Caption lines can't have notes under them.", "Each caption is one line: - 0:01.2 → 0:03.4 \"text\".")
		return
	}
	if it.kind == "conversation" {
		p.errAt(ln, col, col+len([]rune(t)), "not_attached", "A conversation has only Member: lines under it.", "Put notes about the cut above ## Clips, or under a clip.")
		return
	}
	if it.kind == "beat" || it.kind == "recording" {
		p.errAt(ln, col, col+len([]rune(t)), "not_attached", "Narration has no notes under its lines.", "Under a narration line you can write Delivery:, Take:, Why: and Evidence: lines; put notes about the narration above ## Clips.")
		return
	}
	it.events = append(it.events, noteEvent{kind: 0, text: unescapeNote(t), line: ln})
}

// ---- shared pieces of item lines ----

type point struct {
	ok       bool
	reported bool // the failure was already reported
	time     float64
	phrase   string
	isPhrase bool
	sp       Span
}

func (p *parser) readPoint(ts []token, j *int, line int, allowPhrase bool) point {
	if *j >= len(ts) {
		return point{}
	}
	t := ts[*j]
	switch t.kind {
	case tString:
		*j++
		if !allowPhrase {
			p.errAt(line, t.col, t.end, "phrase_not_allowed", "Use a time here, like 0:12.5.", "Quoted words can only pick where clips and sounds start and end.")
			return point{reported: true}
		}
		if strings.TrimSpace(t.text) == "" {
			p.errAt(line, t.col, t.end, "bad_phrase", "These quotes are empty.", "Put the words that are said, like \"very quickly\".")
			return point{reported: true}
		}
		return point{ok: true, phrase: t.text, isPhrase: true, sp: Span{line, t.col, t.end}}
	case tWord:
		if v, ok := parseClock(t.text); ok {
			*j++
			return point{ok: true, time: v, sp: Span{line, t.col, t.end}}
		}
		if _, ok := parseNum(t.text); ok && *j+1 < len(ts) && ts[*j+1].is("s", "sec", "secs", "second", "seconds") {
			v, _ := parseClock(t.text + "s")
			sp := Span{line, t.col, ts[*j+1].end}
			*j += 2
			return point{ok: true, time: v, sp: sp}
		}
	}
	return point{}
}

func badTime(p *parser, line int, t token, what string) {
	hint := "Times are minutes:seconds, like 0:12.5 or 1:03.25 (12.5s also works)."
	if _, ok := parseNum(t.text); ok {
		hint = fmt.Sprintf("Write it as a clock time: %s, or add s for seconds (%ss).", fmtClockGuess(t.text), t.text)
	}
	p.errAt(line, t.col, t.end, "bad_time", fmt.Sprintf("%q isn't a time MetaRush can read (%s).", t.text, what), hint)
}

func fmtClockGuess(s string) string {
	if v, ok := parseNum(s); ok && v >= 0 {
		return fmtClock(v)
	}
	return "0:12.5"
}

// readRange reads `A → B`, `from A to B` (A, B: times or quoted words).
func (p *parser) readRange(ts []token, line int, allowPhrase bool, what string) (point, point, int, bool) {
	j := 0
	if j < len(ts) && ts[j].is("from") {
		j++
	}
	if j >= len(ts) {
		at := line
		col := 1
		if len(ts) > 0 {
			col = ts[len(ts)-1].end
		}
		p.errAt(at, col, col+1, "missing_range", "Say where "+what+" starts and ends, like 0:11.6 → 0:18.4.", "")
		return point{}, point{}, j, false
	}
	a := p.readPoint(ts, &j, line, allowPhrase)
	if !a.ok {
		switch {
		case a.reported:
		case j < len(ts) && ts[j].kind == tWord:
			badTime(p, line, ts[j], "the start")
		default:
			p.errAt(line, ts[j].col, ts[j].end, "missing_range", "Expected where "+what+" starts, like 0:11.6.", "")
		}
		return point{}, point{}, j, false
	}
	if j < len(ts) && (ts[j].kind == tArrow || ts[j].is("to")) {
		j++
	} else {
		col := a.sp.EndCol
		p.errAt(line, col, col+1, "missing_arrow", "Expected → between the start and the end.", "Write it like 0:11.6 → 0:18.4 (-> works too).")
		return point{}, point{}, j, false
	}
	b := p.readPoint(ts, &j, line, allowPhrase)
	if !b.ok {
		switch {
		case b.reported:
		case j < len(ts) && ts[j].kind == tWord:
			badTime(p, line, ts[j], "the end")
		default:
			p.errAt(line, a.sp.EndCol, a.sp.EndCol+2, "missing_range", "The end time is missing.", "")
		}
		return point{}, point{}, j, false
	}
	return a, b, j, true
}

// readDuration reads "0.4 s", "0.4s" or "2 seconds".
func readDuration(ts []token) (float64, bool) {
	switch len(ts) {
	case 1:
		if m := durRe.FindStringSubmatch(ts[0].text); m != nil && ts[0].kind == tWord {
			return parseNum(m[1])
		}
	case 2:
		if ts[0].kind == tWord && ts[1].is("s", "sec", "secs", "second", "seconds") {
			return parseNum(ts[0].text)
		}
	}
	return 0, false
}

func numberWithSuffix(t token, suffixes ...string) (float64, bool) {
	s := t.text
	for _, suf := range suffixes {
		if strings.HasSuffix(s, suf) {
			s = strings.TrimSuffix(s, suf)
			break
		}
	}
	return parseNum(s)
}

func jsonNum(v float64) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (p *parser) set(it *item, key string, v json.RawMessage, sp Span, what string) {
	if _, dup := it.m[key]; dup {
		p.errAt(sp.Line, sp.Col, sp.EndCol, "duplicate", fmt.Sprintf("%s is given twice for %s.", what, it.what()), "Keep one of them.")
		return
	}
	it.m[key] = v
	it.order = append(it.order, key)
	it.spans[key] = sp
}

// mergeBlock adds a {…} block's members to an item.
func (p *parser) mergeBlock(it *item, t token, line int) {
	ms, err := orderedMembers(t.raw)
	if err != nil {
		p.errAt(line, t.col, t.end, "bad_block", "A {…} block holds named values, like {label: \"Opening\"}.", "")
		return
	}
	sp := Span{line, t.col, t.end}
	it.spans["{}"] = sp
	for _, m := range ms {
		p.set(it, m.Key, m.Value, sp, m.Key)
	}
}

func (p *parser) idToken(it *item, ts []token, line int) int {
	if len(ts) > 0 && ts[0].kind == tWord && strings.HasSuffix(ts[0].text, ":") && len(ts[0].text) > 1 {
		id := strings.TrimSuffix(ts[0].text, ":")
		if !validClipID(id) {
			p.errAt(line, ts[0].col, ts[0].end-1, "bad_name", fmt.Sprintf("%q can't be a name here.", id), "Names start with a lowercase letter and use letters, digits, - and _ (like hook or broll-2).")
			return 1
		}
		it.id = id
		p.set(it, "id", json.RawMessage(quote(id)), Span{line, ts[0].col, ts[0].end - 1}, "The name")
		return 1
	}
	return 0
}

// ---- clips ----

func (p *parser) clipLine(rest []rune, line, col int) *item {
	it := newItem("clip", line)
	ts, prob := tokenize(rest, col)
	if prob != nil {
		prob.Line = line
		p.probs = append(p.probs, *prob)
		it.broken = true
		return it
	}
	segs := splitSeps(ts)
	head := segs[0]
	i := p.idToken(it, head, line)
	if it.id == "" {
		it.auto = true
	}
	if i >= len(head) {
		p.errAt(line, col, col+len(rest), "missing_footage", "Say which footage this clip uses and where it starts and ends.", "Like: 1. hook: interview-a 0:11.6 → 0:18.4")
		it.broken = true
		return it
	}
	ft := head[i]
	next := i + 1
	if p.startsRemotion(head, i) {
		// A composition of a Remotion source (D135): its clip, with props from `· with …`.
		props := ""
		rest := [][]token{segs[0]}
		for _, seg := range segs[1:] {
			if len(seg) > 0 && seg[0].is("with") {
				js, ok := p.withJSON(seg, line, "a composition's props")
				if !ok {
					it.broken = true
					return it
				}
				props = canonicalJSON(js)
				continue
			}
			rest = append(rest, seg)
		}
		segs = rest
		r, n, ok := p.remotionClip(it, head, i, line, props)
		if !ok {
			it.broken = true
			return it
		}
		it.assetID = r.AssetID
		sp := Span{line, ft.col, head[i+n-1].end}
		p.set(it, "assetId", json.RawMessage(quote(r.AssetID)), sp, "The composition")
		next = i + n
		if next >= len(head) {
			// No range: the whole composition.
			p.set(it, "in", jsonNum(0), sp, "The start")
			p.set(it, "out", jsonNum(r.Duration), sp, "The end")
			for _, seg := range segs[1:] {
				p.clipAttr(it, seg, line)
			}
			return it
		}
	} else if key, ok := p.convSource(head, i); ok {
		// A conversation the sheet pins (D99): conversation call 0:00 → 12:30.
		it.assetID = key
		p.set(it, "assetId", json.RawMessage(quote(key)), Span{line, ft.col, head[i+1].end}, "The conversation")
		next = i + 2
	} else if p.startsNested(head, i) {
		// A cut from another project (D84): cut from <name> 0:00 → 0:12.5.
		key, n, ok := p.cutRef(head, i, line)
		if !ok {
			it.broken = true
			return it
		}
		it.assetID = key
		p.set(it, "assetId", json.RawMessage(quote(key)), Span{line, ft.col, head[i+n-1].end}, "The cut")
		next = i + n
	} else {
		assetID, ok := p.resolveFootage(ft, line)
		if !ok {
			it.broken = true
			return it
		}
		it.assetID = assetID
		p.set(it, "assetId", json.RawMessage(quote(assetID)), Span{line, ft.col, ft.end}, "The footage")
	}
	a, b, used, ok := p.readRange(head[next:], line, true, "the clip")
	if !ok {
		it.broken = true
		return it
	}
	if extra := head[next+used:]; len(extra) > 0 {
		c0, c1 := spanOf(extra)
		p.errAt(line, c0, c1, "unexpected", fmt.Sprintf("Unexpected %q after the times.", joinWords(extra)), "Separate settings with · (like 0:01 → 0:04 · muted).")
	}
	in, out, ok := p.resolveRange(it, a, b)
	if !ok {
		it.broken = true
		return it
	}
	p.set(it, "in", jsonNum(in), a.sp, "The start")
	p.set(it, "out", jsonNum(out), b.sp, "The end")
	for _, seg := range segs[1:] {
		p.clipAttr(it, seg, line)
	}
	return it
}

var clipSettings = "speed 1.5×, volume 80%, muted, fill or fit, focus 40% across / 30% down, focus on faces, follow movement, zoom 1.2×, fades out 1 s, label \"…\", locked, or a {name: value} block"

func (p *parser) clipAttr(it *item, seg []token, line int) {
	if len(seg) == 0 {
		return
	}
	sp := func() Span { c0, c1 := spanOf(seg); return Span{line, c0, c1} }
	t := seg[0]
	if p.mutedAttr(it, seg, line) {
		return
	}
	switch {
	case len(seg) == 1 && t.kind == tBlock:
		p.mergeBlock(it, t, line)
	case t.is("speed") && len(seg) >= 2:
		ts := seg[1:]
		if len(ts) == 2 && ts[1].is("×", "x", "times") {
			ts = ts[:1]
		}
		v, ok := numberWithSuffix(ts[0], "×", "x")
		if !ok || len(ts) != 1 {
			p.errAt(line, seg[1].col, seg[len(seg)-1].end, "bad_value", "Speed is a number like 1.5× (2× is twice as fast, 0.5× half speed).", "")
			return
		}
		p.set(it, "speed", jsonNum(v), sp(), "Speed")
	case t.is("volume") && len(seg) == 2:
		v, ok := parsePercent(seg[1].text)
		if !ok {
			p.errAt(line, seg[1].col, seg[1].end, "bad_value", "A clip's volume is a percentage, like 80% (100% is unchanged).", "")
			return
		}
		p.set(it, "volume", jsonNum(v), sp(), "Volume")
	case t.is("muted", "mute", "silent") && len(seg) == 1:
		p.set(it, "muted", json.RawMessage("true"), sp(), "muted")
	case t.is("locked") && len(seg) == 1:
		p.set(it, "locked", json.RawMessage("true"), sp(), "locked")
	case t.is("fill", "fit") && len(seg) == 1:
		p.framingSet(it, "mode", json.RawMessage(quote(strings.ToLower(t.text))), sp(), "fill or fit")
	case t.is("focus"):
		p.focus(it, seg, line)
	case t.is("follow") && followWords(seg[1:]):
		p.framingSet(it, "auto", json.RawMessage(quote(editspec.FramingFollow)), sp(), "follow movement")
	case t.is("zoom") && len(seg) >= 2:
		v, ok := numberWithSuffix(seg[1], "×", "x")
		if !ok || len(seg) > 3 || (len(seg) == 3 && !seg[2].is("×", "x")) {
			p.errAt(line, seg[1].col, seg[len(seg)-1].end, "bad_value", "Zoom is a number like 1.2× (1× shows the whole width).", "")
			return
		}
		p.framingSet(it, "zoom", jsonNum(v), sp(), "Zoom")
	case t.is("fades", "fade") && len(seg) >= 3 && seg[1].is("out"):
		v, ok := readDuration(seg[2:])
		if !ok {
			p.errAt(line, seg[2].col, seg[len(seg)-1].end, "bad_value", "How long the fade-out lasts, like 1 s.", "")
			return
		}
		p.set(it, "fadeOut", jsonNum(v), sp(), "The fade-out")
	case t.is("label", "called") && len(seg) == 2 && seg[1].kind == tString:
		p.set(it, "label", json.RawMessage(quote(seg[1].text)), sp(), "The label")
	default:
		s := sp()
		p.errAt(line, s.Col, s.EndCol, "unknown_setting", fmt.Sprintf("MetaRush doesn't know %q for a clip.", joinWords(seg)), "Clip settings: "+clipSettings+".")
	}
}

func (p *parser) framingSet(it *item, key string, v json.RawMessage, sp Span, what string) {
	if it.framing == nil {
		it.framing = map[string]json.RawMessage{}
		it.framingSp = sp
	}
	if _, dup := it.framing[key]; dup {
		p.errAt(sp.Line, sp.Col, sp.EndCol, "duplicate", what+" is given twice.", "")
		return
	}
	it.framing[key] = v
	it.spans["framing"] = sp
}

// followWords are the words after "follow" in "follow movement" (also "the
// action", "the subject").
func followWords(ts []token) bool {
	var ws []string
	for _, t := range ts {
		if t.kind != tWord {
			return false
		}
		ws = append(ws, strings.ToLower(t.text))
	}
	switch strings.Join(ws, " ") {
	case "movement", "the action", "the subject":
		return true
	}
	return false
}

// focus reads "focus 30% across", "focus 40% down" or both, or "focus on faces".
func (p *parser) focus(it *item, seg []token, line int) {
	ts := seg[1:]
	if len(ts) == 2 && ts[0].kind == tWord && ts[1].kind == tWord && strings.EqualFold(ts[0].text, "on") && strings.EqualFold(ts[1].text, "faces") {
		p.framingSet(it, "auto", json.RawMessage(quote(editspec.FramingFaces)), Span{line, ts[0].col, ts[1].end}, "focus on faces")
		return
	}
	k, got := 0, false
	for k < len(ts) {
		if ts[k].kind == tComma {
			k++
			continue
		}
		if k+1 < len(ts) && ts[k+1].kind == tWord {
			if v, ok := parsePercent(ts[k].text); ok {
				if key, ok := map[string]string{"across": "focusX", "down": "focusY"}[strings.ToLower(ts[k+1].text)]; ok {
					p.framingSet(it, key, jsonNum(v), Span{line, ts[k].col, ts[k+1].end}, "focus "+strings.ToLower(ts[k+1].text))
					got = true
					k += 2
					continue
				}
			}
		}
		break
	}
	if got && k == len(ts) {
		return
	}
	c0, c1 := spanOf(seg)
	p.errAt(line, c0, c1, "bad_value", "Focus says which part of the picture to keep when cropping.", "Write focus 30% across, 40% down (0% is the left or top edge, 100% the right or bottom), or focus on faces to keep the main face in frame.")
}

// resolveRange turns the two points into source times, finding quoted words in
// the footage's transcript.
func (p *parser) resolveRange(it *item, a, b point) (float64, float64, bool) {
	if !a.isPhrase && !b.isPhrase {
		return a.time, b.time, true
	}
	name := p.nameOf(it.assetID)
	if name == "" {
		name = "this footage"
	}
	if p.o.Info == nil {
		sp := a.sp
		if !a.isPhrase {
			sp = b.sp
		}
		p.errAt(sp.Line, sp.Col, sp.EndCol, "no_transcript", "Quoted words can only be found when checking against the project.", "Check the sheet against the project (that reads the transcripts), or write times instead.")
		return 0, 0, false
	}
	segs := p.o.Info.Transcript(it.assetID)
	convLen, isConv := 0.0, false
	if editspec.IsConversation(it.assetID) {
		segs, convLen, isConv = p.convTranscript(it.assetID)
	}
	if segs == nil {
		sp := a.sp
		if !a.isPhrase {
			sp = b.sp
		}
		p.errAt(sp.Line, sp.Col, sp.EndCol, "no_transcript", fmt.Sprintf("%s has no transcript yet, so MetaRush can't find quoted words in it.", name), "Make a transcript for it first (analyze the footage), or write times instead.")
		return 0, 0, false
	}
	ws := flattenWords(segs)
	duration := 0.0
	if ft, ok := p.o.Info.Footage(it.assetID); ok {
		duration = ft.Duration
	}
	if isConv {
		duration = convLen
	}
	type cand struct {
		t float64
		m phraseMatch
	}
	find := func(pt point, isIn bool) ([]cand, bool) {
		ms := findPhrase(ws, pt.phrase)
		if len(ms) == 0 {
			hint := "Check the spelling against the transcript, or write a time instead."
			if c, ok := closest(ws, pt.phrase); ok {
				at := ws[c.first].from
				if !isIn {
					at = ws[c.last].to
				}
				hint = fmt.Sprintf("The closest words are “%s” at %s. Check the spelling, or write a time instead.", matchedWords(ws, c), fmtClock(round3(at)))
			}
			p.errAt(pt.sp.Line, pt.sp.Col, pt.sp.EndCol, "phrase_not_found", fmt.Sprintf("MetaRush can't find the words “%s” in %s.", pt.phrase, name), hint)
			return nil, false
		}
		var out []cand
		for _, m := range ms {
			if isIn {
				out = append(out, cand{snapIn(ws, m.first), m})
			} else {
				out = append(out, cand{snapOut(ws, m.last, duration), m})
			}
		}
		return out, true
	}
	var ins, outs []cand
	if a.isPhrase {
		c, ok := find(a, true)
		if !ok {
			return 0, 0, false
		}
		ins = c
	} else {
		ins = []cand{{t: a.time, m: phraseMatch{first: -1}}}
	}
	if b.isPhrase {
		c, ok := find(b, false)
		if !ok {
			return 0, 0, false
		}
		outs = c
	} else {
		outs = []cand{{t: b.time, m: phraseMatch{first: -1}}}
	}
	type pair struct{ in, out cand }
	var pairs []pair
	for _, x := range ins {
		for _, y := range outs {
			if x.t < y.t {
				pairs = append(pairs, pair{x, y})
			}
		}
	}
	listCands := func(cs []cand) string {
		var parts []string
		for i, c := range cs {
			if i == 5 {
				parts = append(parts, fmt.Sprintf("and %d more", len(cs)-5))
				break
			}
			parts = append(parts, fmt.Sprintf("%s (“%s”)", fmtClock(round3(c.t)), context(ws, c.m)))
		}
		return strings.Join(parts, ", ")
	}
	if len(pairs) == 0 {
		pt := b
		if !b.isPhrase {
			pt = a
		}
		p.errAt(pt.sp.Line, pt.sp.Col, pt.sp.EndCol, "phrase_order", fmt.Sprintf("The clip would end before it starts: “%s” comes too early or too late in %s.", pt.phrase, name), "Check which words start and end the clip.")
		return 0, 0, false
	}
	if len(pairs) > 1 {
		inSet, outSet := map[float64]bool{}, map[float64]bool{}
		for _, pr := range pairs {
			inSet[pr.in.t], outSet[pr.out.t] = true, true
		}
		if len(inSet) > 1 && a.isPhrase {
			var cs []cand
			for _, c := range ins {
				if inSet[c.t] {
					cs = append(cs, c)
				}
			}
			p.errAt(a.sp.Line, a.sp.Col, a.sp.EndCol, "phrase_ambiguous", fmt.Sprintf("“%s” is said %d times in %s.", a.phrase, len(cs), name), "Add a word or two so it's unique, or write the time instead: "+listCands(cs)+".")
			return 0, 0, false
		}
		var cs []cand
		for _, c := range outs {
			if outSet[c.t] {
				cs = append(cs, c)
			}
		}
		p.errAt(b.sp.Line, b.sp.Col, b.sp.EndCol, "phrase_ambiguous", fmt.Sprintf("“%s” is said %d times in %s after the start.", b.phrase, len(cs), name), "Add a word or two so it's unique, or write the time instead: "+listCands(cs)+".")
		return 0, 0, false
	}
	pr := pairs[0]
	for _, x := range []struct {
		pt    point
		c     cand
		field string
	}{{a, pr.in, "in"}, {b, pr.out, "out"}} {
		if !x.pt.isPhrase {
			continue
		}
		approx := ws[x.c.m.first].approx || ws[x.c.m.last].approx
		p.sheet.Anchors = append(p.sheet.Anchors, Anchor{ClipID: it.id, Field: x.field, Phrase: x.pt.phrase, Time: x.c.t, Words: matchedWords(ws, x.c.m), Exact: x.c.m.exact && !approx, Line: x.pt.sp.Line})
		if approx {
			p.warnAt(x.pt.sp.Line, x.pt.sp.Col, x.pt.sp.EndCol, "approximate_anchor", fmt.Sprintf("%s only has sentence-level timing, so the point near “%s” is approximate.", name, x.pt.phrase), "Check it in the preview, or re-run transcription for word timing.")
		} else if !x.c.m.exact {
			p.warnAt(x.pt.sp.Line, x.pt.sp.Col, x.pt.sp.EndCol, "fuzzy_anchor", fmt.Sprintf("Used “%s” for “%s” (close, not exact).", matchedWords(ws, x.c.m), x.pt.phrase), "")
		}
	}
	return pr.in.t, pr.out.t, true
}

func round3(v float64) float64 { return float64(int64(v*1000+0.5)) / 1000 }

// ---- transitions ----

func (p *parser) transitionLine(rest []rune, line, col int) *item {
	it := newItem("transition", line)
	ts, prob := tokenize(rest, col)
	if prob != nil {
		prob.Line = line
		p.probs = append(p.probs, *prob)
		it.broken = true
		return it
	}
	segs := splitSeps(ts)
	head := segs[0]
	if len(head) == 1 && head[0].kind == tBlock {
		p.mergeBlock(it, head[0], line)
		return it
	}
	k := len(head)
	for k > 0 && head[k-1].kind == tWord && (head[k-1].is("s", "sec", "secs", "second", "seconds") || durRe.MatchString(head[k-1].text)) {
		k--
	}
	nameToks, durToks := head[:k], head[k:]
	typ, ok := transitionByWords(joinWords(nameToks))
	// The moving transitions name their direction after the name: slide left.
	var dirTok *token
	if n := len(nameToks); !ok && n > 1 && nameToks[n-1].kind == tWord && isDirection(strings.ToLower(nameToks[n-1].text)) {
		if t, ok2 := transitionByWords(joinWords(nameToks[:n-1])); ok2 {
			if !moves(t) {
				d := nameToks[n-1]
				p.errAt(line, d.col, d.end, "no_direction", fmt.Sprintf("A %s doesn't move in a direction.", TransitionWords(t)), "Only "+movingVocabulary()+" take a direction (left, right, up or down).")
				it.broken = true
				return it
			}
			typ, ok, dirTok = t, true, &nameToks[n-1]
			nameToks = nameToks[:n-1]
		}
	}
	if !ok || len(nameToks) == 0 {
		c0, c1 := spanOf(head)
		p.errAt(line, c0, max(c1, c0+1), "unknown_transition", fmt.Sprintf("MetaRush doesn't know the transition %q.", joinWords(nameToks)), "Transitions are "+transitionVocabulary()+", optionally with a length (dissolve 0.4 s) and, for "+movingVocabulary()+", a direction (slide left 0.4 s).")
		it.broken = true
		return it
	}
	c0, c1 := spanOf(nameToks)
	p.set(it, "type", json.RawMessage(quote(typ)), Span{line, c0, c1}, "The transition")
	if dirTok != nil {
		p.set(it, "direction", json.RawMessage(quote(strings.ToLower(dirTok.text))), Span{line, dirTok.col, dirTok.end}, "The direction")
	}
	if len(durToks) > 0 {
		v, ok := readDuration(durToks)
		d0, d1 := spanOf(durToks)
		if !ok {
			p.errAt(line, d0, d1, "bad_value", "A transition's length is in seconds, like 0.4 s.", "")
		} else {
			p.set(it, "duration", jsonNum(v), Span{line, d0, d1}, "The length")
		}
	}
	for _, seg := range segs[1:] {
		if len(seg) == 1 && seg[0].kind == tBlock {
			p.mergeBlock(it, seg[0], line)
			continue
		}
		text := joinWords(seg)
		matched := false
		for _, cl := range TransitionClauses {
			if fs, ok := cl.Parse(text); ok {
				for k, v := range fs {
					s0, s1 := spanOf(seg)
					p.set(it, k, v, Span{line, s0, s1}, k)
				}
				matched = true
				break
			}
		}
		if !matched {
			s0, s1 := spanOf(seg)
			hint := "After the transition only a {…} block can follow for now."
			if len(TransitionClauses) > 0 {
				var ex []string
				for _, cl := range TransitionClauses {
					ex = append(ex, cl.Example)
				}
				hint = "Known phrases: " + strings.Join(ex, "; ") + "."
			}
			p.errAt(line, s0, s1, "unknown_transition_phrase", fmt.Sprintf("MetaRush doesn't know the transition phrase %q.", text), hint)
		}
	}
	return it
}

// ---- graphics ----

func (p *parser) overlayLine(rest []rune, line, col int) *item {
	it := newItem("overlay", line)
	ts, prob := tokenize(rest, col)
	if prob != nil {
		prob.Line = line
		p.probs = append(p.probs, *prob)
		it.broken = true
		return it
	}
	segs := splitSeps(ts)
	head := segs[0]
	i := p.idToken(it, head, line)
	if key, n := roleAt(head, i); n > 0 {
		// A role from the layout pack in the template's place (D264): the
		// pack fills it when the sheet is checked.
		p.set(it, "role", json.RawMessage(quote(key)), Span{line, head[i].col, head[i+n-1].end}, "The part")
		i += n
	} else if i < len(head) && head[i].kind == tWord && (head[i].is("motion", "motion:") || strings.Contains(head[i].text, "@")) {
		// A project motion graphic: motion route-map@3fa9c2d1 (D68).
		n, ok := p.motionRef(it, head, i, line)
		if !ok {
			it.broken = true
			return it
		}
		i += n
	} else if i < len(head) && head[i].kind == tWord && !head[i].is("at") && !head[i].is("for") && !head[i].is("from") {
		tpl := head[i]
		if !contains(enumTemplate, tpl.text) {
			p.errAt(line, tpl.col, tpl.end, "unknown_template", fmt.Sprintf("There's no graphics template called %q.", tpl.text), "Templates: "+strings.Join(enumTemplate, ", ")+".")
		}
		p.set(it, "template", json.RawMessage(quote(tpl.text)), Span{line, tpl.col, tpl.end}, "The template")
		i++
	}
	if i < len(head) && ((head[i].is("for") && i+1 < len(head) && head[i+1].is("the")) || head[i].is("from")) {
		// A graphic that lasts a section or the whole video (D163).
		if !p.spanWords(it, head[i:], line) {
			it.broken = true
			return it
		}
		p.overlaySettings(it, segs[1:], line)
		return it
	}
	if i >= len(head) || !head[i].is("at") {
		c := col
		if i < len(head) {
			c = head[i].col
		}
		p.errAt(line, c, c+1, "missing_at", "Say when the graphic shows and for how long, like at 0:01.5 for 3 s.", "")
		it.broken = true
		return it
	}
	i++
	j := 0
	pt := p.readPoint(head[i:], &j, line, false)
	if !pt.ok {
		if i < len(head) {
			badTime(p, line, head[i], "when the graphic shows")
		} else {
			p.errAt(line, head[i-1].end, head[i-1].end+1, "missing_at", "The time after at is missing.", "")
		}
		it.broken = true
		return it
	}
	p.set(it, "at", jsonNum(pt.time), pt.sp, "The start")
	i += j
	if i >= len(head) || !head[i].is("for") {
		p.errAt(line, pt.sp.EndCol, pt.sp.EndCol+1, "missing_duration", "Say how long the graphic shows, like for 3 s.", "")
		it.broken = true
		return it
	}
	v, ok := readDuration(head[i+1:])
	d0, d1 := spanOf(head[i:])
	if !ok {
		p.errAt(line, d0, d1, "bad_value", "How long the graphic shows, in seconds, like for 3 s.", "")
		it.broken = true
		return it
	}
	p.set(it, "duration", jsonNum(v), Span{line, d0, d1}, "The length")
	p.overlaySettings(it, segs[1:], line)
	return it
}

// overlaySettings reads what follows a graphic's place: locked, with …, {…}.
func (p *parser) overlaySettings(it *item, segs [][]token, line int) {
	for _, seg := range segs {
		switch {
		case len(seg) == 0:
		case len(seg) == 1 && seg[0].kind == tBlock:
			p.mergeBlock(it, seg[0], line)
		case len(seg) == 1 && seg[0].is("locked"):
			p.set(it, "locked", json.RawMessage("true"), Span{line, seg[0].col, seg[0].end}, "locked")
		case seg[0].is("with"):
			p.params(it, seg, line)
		case seg[0].is("as"):
			key, n := roleAt(seg, 1)
			if n == 0 || 1+n != len(seg) {
				s0, s1 := spanOf(seg)
				p.errAt(line, s0, s1, "unknown_role", fmt.Sprintf("A layout pack has no part called %q.", joinWords(seg[1:])), "Parts: as title card, as lower third, as end screen, as quote card, as chapter card (a variant after a colon: as lower third: guest).")
				continue
			}
			p.set(it, "role", json.RawMessage(quote(key)), Span{line, seg[0].col, seg[len(seg)-1].end}, "The part")
		default:
			s0, s1 := spanOf(seg)
			p.errAt(line, s0, s1, "unknown_setting", fmt.Sprintf("MetaRush doesn't know %q for a graphic.", joinWords(seg)), "Graphic settings: as title card (the layout pack's part), locked, with name \"…\", title \"…\" (the template's fields), or a {name: value} block.")
		}
	}
}

// spanWords reads where a graphic lasts (D163): for the whole video, from
// <clip> to <clip>, from <clip> to the end, from the start to <clip>.
func (p *parser) spanWords(it *item, ts []token, line int) bool {
	s0, s1 := spanOf(ts)
	sp := Span{line, s0, s1}
	words := strings.ToLower(joinWords(ts))
	bad := func() bool {
		p.errAt(line, s0, s1, "bad_span", "Say how long the graphic lasts: at 0:01.5 for 3 s, for the whole video, or from <clip> to <clip>.", "A section: from hook to answer (or from hook to the end, from the start to answer).")
		return false
	}
	if words == "for the whole video" {
		p.set(it, "span", json.RawMessage("{}"), sp, "The span")
		return true
	}
	if len(ts) < 4 || !ts[0].is("from") {
		return bad()
	}
	// from X to Y, where X is a clip or "the start" and Y a clip or "the end".
	k := 1
	from := ""
	switch {
	case ts[k].is("the") && k+1 < len(ts) && ts[k+1].is("start"):
		k += 2
	case ts[k].kind == tWord && validClipID(ts[k].text):
		from = ts[k].text
		k++
	default:
		return bad()
	}
	if k >= len(ts) || !ts[k].is("to") {
		return bad()
	}
	k++
	to := ""
	switch {
	case k+2 == len(ts) && ts[k].is("the") && ts[k+1].is("end"):
	case k+1 == len(ts) && ts[k].kind == tWord && validClipID(ts[k].text):
		to = ts[k].text
	default:
		return bad()
	}
	b, _ := json.Marshal(editspec.OverlaySpan{From: from, To: to})
	p.set(it, "span", b, sp, "The span")
	return true
}

// changeLine reads `Change: at <footage> 2:15.40 · with name "Lena"` under a
// graphic (D163): from that moment of the footage its fields change.
func (p *parser) changeLine(it *item, rest []rune, line, col int) {
	sub := newItem("change", line)
	ts, prob := tokenize(rest, col)
	if prob != nil {
		prob.Line = line
		p.probs = append(p.probs, *prob)
		return
	}
	segs := splitSeps(ts)
	head := segs[0]
	if len(head) == 1 && head[0].kind == tBlock {
		p.mergeBlock(sub, head[0], line)
	} else {
		bad := func() {
			s0, s1 := spanOf(head)
			p.errAt(line, s0, max(s1, s0+1), "bad_change", "A change says at which moment of which footage the graphic's fields change.", `Like: Change: at lena-call 2:15.40 · with name "Lena"`)
		}
		if len(head) < 3 || !head[0].is("at") || head[1].kind != tWord {
			bad()
			return
		}
		// The footage: a short name, a conversation (its clock) or a cut from
		// another project (its timeline), as clip lines name them (D182).
		var id string
		next := 2
		if key, ok := p.convSource(head, 1); ok {
			id, next = key, 3
		} else if p.startsNested(head, 1) {
			key, n, ok := p.cutRef(head, 1, line)
			if !ok {
				return
			}
			id, next = key, 1+n
		} else {
			fid, ok := p.resolveFootage(head[1], line)
			if !ok {
				return
			}
			id = fid
		}
		if next != len(head)-1 {
			bad()
			return
		}
		tm := head[next]
		at, ok := parseClock(tm.text)
		if !ok || at < 0 {
			p.errAt(line, tm.col, tm.end, "bad_time", "The moment in the footage, like 2:15.40.", "")
			return
		}
		p.set(sub, "footage", json.RawMessage(quote(id)), Span{line, head[1].col, head[next-1].end}, "The footage")
		p.set(sub, "at", jsonNum(at), Span{line, tm.col, tm.end}, "The moment")
	}
	for _, seg := range segs[1:] {
		switch {
		case len(seg) == 0:
		case len(seg) == 1 && seg[0].kind == tBlock:
			p.mergeBlock(sub, seg[0], line)
		case seg[0].is("with"):
			p.params(sub, seg, line)
		default:
			s0, s1 := spanOf(seg)
			p.errAt(line, s0, s1, "unknown_setting", fmt.Sprintf("MetaRush doesn't know %q for a change.", joinWords(seg)), "After the moment: with name \"…\" (the fields that change), or a {name: value} block.")
			return
		}
	}
	if _, ok := sub.m["params"]; !ok {
		sub.m["params"] = json.RawMessage("{}")
	}
	var c editspec.OverlayChange
	if !p.decodeItem(sub, &c) {
		return
	}
	b, _ := json.Marshal(c)
	it.changes = append(it.changes, b)
}

// params reads `with name "Ada", title "Engineer"` into the params object.
func (p *parser) params(it *item, seg []token, line int) {
	var parts []string
	seen := map[string]bool{}
	ts := seg[1:]
	for k := 0; k < len(ts); {
		if ts[k].kind != tWord || k+1 >= len(ts) {
			p.errAt(line, ts[k].col, ts[len(ts)-1].end, "bad_params", "Write graphic fields as name value pairs, like with name \"Ada\", title \"Engineer\".", "")
			return
		}
		key := ts[k].text
		if seen[key] {
			p.errAt(line, ts[k].col, ts[k].end, "duplicate", key+" is given twice.", "")
			return
		}
		seen[key] = true
		val := ts[k+1]
		var raw string
		switch val.kind {
		case tString:
			raw = quote(val.text)
		case tBlock, tList:
			raw = string(val.raw)
		case tWord:
			switch {
			case val.text == "true" || val.text == "false" || val.text == "null":
				raw = val.text
			case jsonNumberRe.FindString(val.text) == val.text:
				raw = val.text
			default:
				p.errAt(line, val.col, val.end, "bad_params", fmt.Sprintf("Put %s's text in quotes: %s.", key, quote(val.text)), "")
				return
			}
		default:
			p.errAt(line, val.col, val.end, "bad_params", "A value is missing after "+key+".", "")
			return
		}
		parts = append(parts, quote(key)+":"+raw)
		k += 2
		if k < len(ts) {
			if ts[k].kind != tComma {
				p.errAt(line, ts[k].col, ts[k].end, "bad_params", "Separate graphic fields with commas.", "")
				return
			}
			k++
		}
	}
	s0, s1 := spanOf(seg)
	p.set(it, "params", json.RawMessage("{"+strings.Join(parts, ",")+"}"), Span{line, s0, s1}, "The graphic's fields")
}

// ---- sound ----

var roleAliases = map[string]string{
	"music": editspec.RoleMusic, "voiceover": editspec.RoleVoiceover, "voice over": editspec.RoleVoiceover, "voice-over": editspec.RoleVoiceover, "narration": editspec.RoleVoiceover,
	"sound effect": editspec.RoleSFX, "sound effects": editspec.RoleSFX, "sfx": editspec.RoleSFX, "effect": editspec.RoleSFX, "dialogue": editspec.RoleDialogue, "dialog": editspec.RoleDialogue,
}

func (p *parser) soundLine(rest []rune, line, col int) *item {
	it := newItem("audio", line)
	ts, prob := tokenize(rest, col)
	if prob != nil {
		prob.Line = line
		p.probs = append(p.probs, *prob)
		it.broken = true
		return it
	}
	segs := splitSeps(ts)
	head := segs[0]
	i := p.idToken(it, head, line)
	if it.id == "" {
		p.errAt(line, col, col+1, "missing_name", "Each sound needs a short name first, like - bed: music-bed 0:00 → 0:20 · music · starts at 0:00.", "")
	}
	if i >= len(head) {
		p.errAt(line, col, col+len(rest), "missing_footage", "Say which audio this uses and which part of it.", "Like: - bed: music-bed 0:00 → 0:20 · music · starts at 0:00")
		it.broken = true
		return it
	}
	assetID, ok := p.resolveFootage(head[i], line)
	if !ok {
		it.broken = true
		return it
	}
	it.assetID = assetID
	p.set(it, "assetId", json.RawMessage(quote(assetID)), Span{line, head[i].col, head[i].end}, "The audio")
	a, b, used, ok := p.readRange(head[i+1:], line, true, "the sound")
	if !ok {
		it.broken = true
		return it
	}
	if extra := head[i+1+used:]; len(extra) > 0 {
		c0, c1 := spanOf(extra)
		p.errAt(line, c0, c1, "unexpected", fmt.Sprintf("Unexpected %q after the times.", joinWords(extra)), "Separate settings with ·.")
	}
	in, out, ok := p.resolveRange(it, a, b)
	if !ok {
		it.broken = true
		return it
	}
	p.set(it, "in", jsonNum(in), a.sp, "The start")
	p.set(it, "out", jsonNum(out), b.sp, "The end")
	for _, seg := range segs[1:] {
		p.soundAttr(it, seg, line)
	}
	if _, ok := it.m["role"]; !ok {
		p.errAt(line, col, col+len(rest), "missing_role", "Say what kind of sound this is: music, voiceover, sound effect or dialogue.", "Add it after the times, like · music.")
	}
	if _, ok := it.m["at"]; !ok {
		p.errAt(line, col, col+len(rest), "missing_at", "Say where the sound starts in the video, like · starts at 0:00.", "")
	}
	return it
}

var soundSettings = "music / voiceover / sound effect / dialogue, starts at 0:00, volume -14 dB, fades in 1 s, fades out 2 s, ducks under speech (by -12 dB), fits the video, placed by hand, loops, ends on a phrase, ends on a beat, quieter when someone talks, never quieter, locked, or a {name: value} block"

// soundWordSettings are the sound settings written as fixed words.
var soundWordSettings = []struct {
	words, key, value, what string
}{
	{"fits the video", "fit", `"auto"`, "The fit"},
	{"fit to the video", "fit", `"auto"`, "The fit"},
	{"placed by hand", "fit", `"manual"`, "The fit"},
	{"loops", "loop", "true", "Looping"},
	{"loop", "loop", "true", "Looping"},
	{"ends on a phrase", "ending", `"phrase"`, "Where it ends"},
	{"ends on a beat", "ending", `"beat"`, "Where it ends"},
	{"quieter when someone talks", "duck", "true", "Getting quieter"},
	{"never quieter", "duck", "false", "Getting quieter"},
}

func (p *parser) soundAttr(it *item, seg []token, line int) {
	if len(seg) == 0 {
		return
	}
	c0, c1 := spanOf(seg)
	sp := Span{line, c0, c1}
	words := strings.ToLower(joinWords(seg))
	if role, ok := roleAliases[words]; ok {
		p.set(it, "role", json.RawMessage(quote(role)), sp, "The kind of sound")
		return
	}
	for _, w := range soundWordSettings {
		if words == w.words {
			p.set(it, w.key, json.RawMessage(w.value), sp, w.what)
			return
		}
	}
	t := seg[0]
	switch {
	case len(seg) == 1 && t.kind == tBlock:
		p.mergeBlock(it, t, line)
	case len(seg) == 1 && t.is("locked"):
		p.set(it, "locked", json.RawMessage("true"), sp, "locked")
	case t.is("starts", "at"):
		k := 1
		if t.is("starts") && len(seg) > 1 && seg[1].is("at") {
			k = 2
		}
		j := 0
		pt := p.readPoint(seg[k:], &j, line, false)
		if !pt.ok || k+j != len(seg) {
			p.errAt(line, c0, c1, "bad_time", "Where the sound starts in the video, like starts at 0:03.2.", "")
			return
		}
		p.set(it, "at", jsonNum(pt.time), sp, "The start in the video")
	case t.is("volume") && len(seg) >= 2:
		ts := seg[1:]
		if len(ts) == 2 && ts[1].is("db") {
			ts = ts[:1]
		}
		v, ok := numberWithSuffix(ts[0], "dB", "db")
		if !ok || len(ts) != 1 {
			p.errAt(line, c0, c1, "bad_value", "A sound's volume is in decibels, like volume -14 dB (0 dB is unchanged; lower is quieter).", "")
			return
		}
		p.set(it, "gainDb", jsonNum(v), sp, "Volume")
	case t.is("fades", "fade") && len(seg) >= 3 && seg[1].is("in", "out"):
		v, ok := readDuration(seg[2:])
		if !ok {
			p.errAt(line, c0, c1, "bad_value", "How long the fade lasts, like fades in 1 s.", "")
			return
		}
		key := map[string]string{"in": "fadeIn", "out": "fadeOut"}[strings.ToLower(seg[1].text)]
		p.set(it, key, jsonNum(v), sp, "The fade")
	case t.is("ducks", "duck") && len(seg) >= 3 && seg[1].is("under") && seg[2].is("speech", "dialogue", "talking", "voices"):
		p.set(it, "duckUnderDialogue", json.RawMessage("true"), sp, "Ducking")
		rest := seg[3:]
		if len(rest) == 0 {
			return
		}
		if rest[0].is("by") {
			rest = rest[1:]
		}
		if len(rest) == 2 && rest[1].is("db") {
			rest = rest[:1]
		}
		v, ok := 0.0, false
		if len(rest) == 1 {
			v, ok = numberWithSuffix(rest[0], "dB", "db")
		}
		if !ok {
			p.errAt(line, c0, c1, "bad_value", "How much quieter the sound gets under speech, like ducks under speech by -12 dB.", "")
			return
		}
		p.set(it, "duckDb", jsonNum(v), sp, "How much it dips under speech")
	default:
		p.errAt(line, c0, c1, "unknown_setting", fmt.Sprintf("MetaRush doesn't know %q for a sound.", joinWords(seg)), "Sound settings: "+soundSettings+".")
	}
}

func (p *parser) rightsLine(it *item, rest []rune, line, col int) {
	ts, prob := tokenize(rest, col)
	if prob != nil {
		prob.Line = line
		p.probs = append(p.probs, *prob)
		return
	}
	if len(ts) == 1 && ts[0].kind == tBlock {
		p.set(it, "rights", ts[0].raw, Span{line, ts[0].col, ts[0].end}, "Rights")
		return
	}
	var parts []string
	seen := map[string]bool{}
	for _, seg := range splitSeps(ts) {
		if len(seg) != 2 || !seg[0].is("license", "licence", "attribution", "credit", "source") || seg[1].kind != tString {
			c0, c1 := spanOf(seg)
			p.errAt(line, c0, max(c1, c0+1), "bad_rights", "Rights are license \"…\", attribution \"…\" and source \"…\", separated by ·.", "")
			return
		}
		key := map[string]string{"license": "license", "licence": "license", "attribution": "attribution", "credit": "attribution", "source": "source"}[strings.ToLower(seg[0].text)]
		if seen[key] {
			p.errAt(line, seg[0].col, seg[0].end, "duplicate", key+" is given twice.", "")
			return
		}
		seen[key] = true
		parts = append(parts, quote(key)+":"+quote(seg[1].text))
	}
	p.set(it, "rights", json.RawMessage("{"+strings.Join(parts, ",")+"}"), Span{line, col, col + len(rest)}, "Rights")
}

// ---- captions ----

func (p *parser) segmentLine(rest []rune, line, col int) *item {
	it := newItem("segment", line)
	ts, prob := tokenize(rest, col)
	if prob != nil {
		prob.Line = line
		p.probs = append(p.probs, *prob)
		it.broken = true
		return it
	}
	segs := splitSeps(ts)
	head := segs[0]
	a, b, used, ok := p.readRange(head, line, false, "the caption")
	if !ok {
		it.broken = true
		return it
	}
	p.set(it, "from", jsonNum(a.time), a.sp, "The start")
	p.set(it, "to", jsonNum(b.time), b.sp, "The end")
	tail := head[used:]
	if len(tail) == 0 || tail[len(tail)-1].kind != tString {
		p.errAt(line, b.sp.EndCol, b.sp.EndCol+1, "missing_text", "Put the caption text in quotes after the times.", "Like: - 0:01.2 → 0:03.4 Ada: \"We built it twice.\"")
		it.broken = true
		return it
	}
	text := tail[len(tail)-1]
	p.set(it, "text", json.RawMessage(quote(text.text)), Span{line, text.col, text.end}, "The text")
	if sp := tail[:len(tail)-1]; len(sp) > 0 {
		last := sp[len(sp)-1]
		if last.kind != tWord || !strings.HasSuffix(last.text, ":") {
			c0, c1 := spanOf(sp)
			p.errAt(line, c0, c1, "bad_speaker", "Before the quoted text, only the speaker's name with a colon can go (like Ada:).", "")
		} else {
			name := strings.TrimSuffix(joinWords(sp), ":")
			c0, c1 := spanOf(sp)
			p.set(it, "speaker", json.RawMessage(quote(name)), Span{line, c0, c1}, "The speaker")
		}
	}
	for _, seg := range segs[1:] {
		if len(seg) == 1 && seg[0].kind == tBlock {
			p.mergeBlock(it, seg[0], line)
			continue
		}
		c0, c1 := spanOf(seg)
		p.errAt(line, c0, max(c1, c0+1), "unknown_setting", "A caption line is just times, an optional speaker and the text.", "")
	}
	return it
}

// ---- evidence ----

func (p *parser) evidenceLine(rest []rune, line, col int) (json.RawMessage, bool) {
	ts, prob := tokenize(rest, col)
	if prob != nil {
		prob.Line = line
		p.probs = append(p.probs, *prob)
		return nil, false
	}
	if len(ts) == 1 && ts[0].kind == tBlock {
		return ts[0].raw, true
	}
	segs := splitSeps(ts)
	if len(segs[0]) != 1 || segs[0][0].kind != tWord || !contains(enumEvidence, strings.ToLower(segs[0][0].text)) {
		c0, c1 := spanOf(segs[0])
		p.errAt(line, max(c0, col), max(c1, col+1), "bad_evidence", "Evidence starts with what kind it is: "+orList(enumEvidence)+".", "Like: Evidence: transcript · interview-a 0:16.1 → 0:18.3 · quote \"…\"")
		return nil, false
	}
	m := map[string]json.RawMessage{"kind": json.RawMessage(quote(strings.ToLower(segs[0][0].text)))}
	keys := []string{"kind"}
	put := func(k string, v json.RawMessage, t token) bool {
		if _, dup := m[k]; dup {
			p.errAt(line, t.col, t.end, "duplicate", k+" is given twice in this evidence.", "")
			return false
		}
		m[k] = v
		keys = append(keys, k)
		return true
	}
	for _, seg := range segs[1:] {
		if len(seg) == 0 {
			continue
		}
		t := seg[0]
		switch {
		case len(seg) == 1 && t.kind == tBlock:
			ms, err := orderedMembers(t.raw)
			if err != nil {
				p.errAt(line, t.col, t.end, "bad_block", "A {…} block holds named values.", "")
				return nil, false
			}
			for _, mm := range ms {
				if !put(mm.Key, mm.Value, t) {
					return nil, false
				}
			}
		case t.is("quote", "note", "ref") && len(seg) == 2 && seg[1].kind == tString:
			if !put(strings.ToLower(t.text), json.RawMessage(quote(seg[1].text)), t) {
				return nil, false
			}
		default:
			k := 0
			if t.kind == tWord && !t.is("from", "to") {
				if _, isTime := parseClock(t.text); !isTime {
					id, ok := p.resolveFootage(t, line)
					if !ok {
						return nil, false
					}
					if !put("assetId", json.RawMessage(quote(id)), t) {
						return nil, false
					}
					k = 1
				}
			}
			r := seg[k:]
			if len(r) == 0 {
				continue
			}
			switch {
			case r[0].is("to"):
				j := 1
				pt := p.readPoint(r, &j, line, false)
				if !pt.ok || j != len(r) {
					c0, c1 := spanOf(r)
					p.errAt(line, c0, c1, "bad_time", "Expected a time after to.", "")
					return nil, false
				}
				put("to", jsonNum(pt.time), r[0])
			default:
				j := 0
				if r[0].is("from") {
					j = 1
				}
				a := p.readPoint(r, &j, line, false)
				if !a.ok {
					c0, c1 := spanOf(r)
					p.errAt(line, c0, c1, "bad_evidence", fmt.Sprintf("Couldn't read %q in this evidence.", joinWords(r)), "Evidence parts: footage and times (interview-a 0:16.1 → 0:18.3), quote \"…\", note \"…\", ref \"…\".")
					return nil, false
				}
				put("from", jsonNum(a.time), r[0])
				if j < len(r) {
					if r[j].kind != tArrow && !r[j].is("to") {
						p.errAt(line, r[j].col, r[j].end, "bad_evidence", "Expected → between the two times.", "")
						return nil, false
					}
					j++
					b := p.readPoint(r, &j, line, false)
					if !b.ok || j != len(r) {
						c0, c1 := spanOf(r)
						p.errAt(line, c0, c1, "bad_time", "Expected the end time.", "")
						return nil, false
					}
					put("to", jsonNum(b.time), r[0])
				}
			}
		}
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, quote(k)+":"+string(m[k]))
	}
	raw := json.RawMessage("{" + strings.Join(parts, ",") + "}")
	var e editspec.Evidence
	if err := strictDecode(raw, &e); err != nil {
		p.errAt(line, col, col+len(rest), "bad_evidence", friendlyDecode(err, "evidence"), "")
		return nil, false
	}
	return raw, true
}

var _ = unicode.IsSpace

// roleAt reads a layout pack role at ts[i] (D264): its words ("title card",
// "end screen") or its name ("title-card"), with a variant after a colon
// ("lower third: guest"). n is the tokens used (0: no role there).
func roleAt(ts []token, i int) (key string, n int) {
	for words := 2; words >= 1; words-- {
		if i+words > len(ts) {
			continue
		}
		var parts []string
		for k := i; k < i+words; k++ {
			if ts[k].kind != tWord {
				parts = nil
				break
			}
			parts = append(parts, ts[k].text)
		}
		if parts == nil {
			continue
		}
		phrase := strings.Join(parts, " ")
		last := ts[i+words-1].text
		if strings.HasSuffix(last, ":") && i+words < len(ts) && ts[i+words].kind == tWord {
			// lower third: guest
			if k, ok := editspec.ParseRoleWords(phrase + " " + ts[i+words].text); ok && strings.Contains(k, ":") {
				return k, words + 1
			}
			continue
		}
		if k, ok := editspec.ParseRoleWords(phrase); ok && !strings.Contains(k, ":") {
			return k, words
		}
		if words == 1 && strings.Contains(last, ":") && !strings.HasSuffix(last, ":") {
			// lower-third:guest
			if k, ok := editspec.ParseRoleWords(last); ok {
				return k, 1
			}
		}
	}
	return "", 0
}
