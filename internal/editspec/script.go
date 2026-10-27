package editspec

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// ParseScript turns a plain-text narration script into beats. Paragraphs
// (separated by blank lines) become lines of narration. Optional markup at the
// start of a paragraph places it:
//
//	[@broll]        start with the clip "broll" (clip ID or label)
//	[@broll+0.5]    0.5 s after that clip starts (also -0.5)
//	[@broll:end]    when that clip ends (also :end-1)
//	[0:12.4]        at an exact clock time (m:ss.s, h:mm:ss or seconds)
//	[after+0.8]     0.8 s after the previous line ends
//	(warm, slower)  delivery notes for whoever records it (not spoken)
//
// Lines starting with # are comments. Paragraphs without a start point follow
// the previous line; when no paragraph has one, they are spread across the
// cut's clips in order (each just after its cut). Beat IDs are line-1, line-2…
func ParseScript(text string, s *Spec) ([]Beat, []Issue) {
	paras := splitParagraphs(text)
	var beats []Beat
	var issues []Issue
	used := map[string]bool{}
	for _, c := range s.Clips {
		used[c.ID] = true
	}
	for _, a := range s.Audio {
		used[a.ID] = true
	}
	for _, o := range s.Overlays {
		used[o.ID] = true
	}
	anyAnchor := false
	type parsed struct {
		beat     Beat
		anchored bool
	}
	var ps []parsed
	for i, p := range paras {
		b, anchored, is := parseParagraph(p, i, s)
		issues = append(issues, is...)
		if strings.TrimSpace(b.Text) == "" {
			continue
		}
		if anchored {
			anyAnchor = true
		}
		ps = append(ps, parsed{b, anchored})
	}
	clips := s.Clips
	for k := range ps {
		if ps[k].anchored {
			continue
		}
		switch {
		case !anyAnchor && len(clips) > 0 && len(ps) <= len(clips):
			ci := int(math.Round(float64(k) * float64(len(clips)) / float64(len(ps))))
			ci = min(ci, len(clips)-1)
			ps[k].beat.Anchor = Anchor{Kind: AnchorClip, ClipID: clips[ci].ID, Offset: 0.3}
		case !anyAnchor && len(clips) > 0 && k < len(clips):
			ps[k].beat.Anchor = Anchor{Kind: AnchorClip, ClipID: clips[k].ID, Offset: 0.3}
		case k == 0 && len(clips) > 0:
			ps[k].beat.Anchor = Anchor{Kind: AnchorClip, ClipID: clips[0].ID, Offset: 0.3}
		default:
			ps[k].beat.Anchor = Anchor{Kind: AnchorAfter}
		}
	}
	n := 0
	for _, p := range ps {
		n++
		id := fmt.Sprintf("line-%d", n)
		for used[id] {
			id += "x"
		}
		used[id] = true
		p.beat.ID = id
		p.beat.State = "draft"
		beats = append(beats, p.beat)
	}
	return beats, issues
}

// ParseScriptAsIs reads a script the way the person wrote it, assuming no
// timing (D86): each paragraph is a line — a script without blank lines has one
// line per line of text, and a single block of text one line per sentence.
// Markup at the start of a line places it (as in ParseScript); every other line
// follows the one before, the first starting with the video, so lines play one
// after another at the speaking pace until a recording sets their timing.
func ParseScriptAsIs(text string, s *Spec) ([]Beat, []Issue) {
	units := SplitScript(text)
	var beats []Beat
	var issues []Issue
	used := map[string]bool{}
	for _, c := range s.Clips {
		used[c.ID] = true
	}
	for _, a := range s.Audio {
		used[a.ID] = true
	}
	for _, o := range s.Overlays {
		used[o.ID] = true
	}
	n := 0
	for i, u := range units {
		b, anchored, is := parseParagraph(u, i, s)
		issues = append(issues, is...)
		if strings.TrimSpace(b.Text) == "" {
			continue
		}
		if !anchored {
			b.Anchor = Anchor{Kind: AnchorAfter}
		}
		n++
		id := fmt.Sprintf("line-%d", n)
		for used[id] {
			id += "x"
		}
		used[id] = true
		b.ID, b.State = id, "draft"
		beats = append(beats, b)
	}
	return beats, issues
}

// SplitScript cuts a script into lines as the person wrote it: paragraphs
// (blank lines between them); without blank lines, each line of text; a single
// block of text, each sentence. Comment lines (#) are left out.
func SplitScript(text string) []string {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		lines = append(lines, l)
	}
	paras := splitParagraphs(strings.Join(lines, "\n"))
	if len(paras) != 1 {
		return paras
	}
	var rows []string
	ends := 0
	for _, l := range lines {
		if t := strings.Join(strings.Fields(l), " "); t != "" {
			rows = append(rows, t)
			if strings.ContainsAny(lastRune(strings.TrimRight(t, "\"”’')]")), ".!?…:;") {
				ends++
			}
		}
	}
	// One line per row, unless the rows are a paragraph wrapped at a width
	// (most of them stop mid-sentence): then its sentences.
	if len(rows) > 1 && ends*2 >= len(rows) {
		return rows
	}
	return Sentences(paras[0])
}

func lastRune(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return ""
	}
	return string(r[len(r)-1])
}

// sentenceAbbrev are words that end with a period without ending a sentence.
var sentenceAbbrev = map[string]bool{"mr": true, "mrs": true, "ms": true, "dr": true, "st": true, "jr": true, "sr": true, "prof": true,
	"vs": true, "etc": true, "e.g": true, "i.e": true, "no": true, "approx": true, "fig": true, "mt": true}

// Sentences splits text at sentence ends (. ! ? … followed by a space and a new
// sentence), keeping the punctuation; markup at the very start stays with the
// first sentence. Common abbreviations and initials do not end a sentence.
func Sentences(text string) []string {
	text = strings.Join(strings.Fields(text), " ")
	r := []rune(text)
	var out []string
	start := 0
	for i := 0; i < len(r); i++ {
		c := r[i]
		if c != '.' && c != '!' && c != '?' && c != '…' {
			continue
		}
		j := i + 1
		for j < len(r) && (r[j] == '.' || r[j] == '!' || r[j] == '?' || r[j] == '"' || r[j] == '”' || r[j] == '’' || r[j] == '\'' || r[j] == ')') {
			j++
		}
		if j >= len(r) || r[j] != ' ' || j+1 >= len(r) {
			continue
		}
		next := r[j+1]
		if !(unicodeUpper(next) || (next >= '0' && next <= '9') || next == '"' || next == '“' || next == '(' || next == '[' || next == '‘') {
			continue
		}
		if c == '.' {
			// The word before the period: an abbreviation or an initial does not end a sentence.
			k := i
			for k > start && r[k-1] != ' ' {
				k--
			}
			w := strings.ToLower(strings.Trim(string(r[k:i]), "\"“(‘'"))
			if sentenceAbbrev[w] || (len([]rune(w)) == 1 && unicodeUpper(r[k])) {
				continue
			}
		}
		out = append(out, strings.TrimSpace(string(r[start:j])))
		start = j + 1
		i = j
	}
	if rest := strings.TrimSpace(string(r[start:])); rest != "" {
		out = append(out, rest)
	}
	return out
}

func unicodeUpper(c rune) bool {
	return strings.ToUpper(string(c)) == string(c) && strings.ToLower(string(c)) != string(c)
}

func splitParagraphs(text string) []string {
	var paras []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			paras = append(paras, strings.Join(cur, " "))
			cur = nil
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
			flush()
		case strings.HasPrefix(t, "#"):
		default:
			cur = append(cur, t)
		}
	}
	flush()
	return paras
}

var (
	reMarkup = regexp.MustCompile(`^\s*(\[[^\]]*\]|\([^)]*\))`)
	reOffset = regexp.MustCompile(`^(.*?)\s*([+-]\s*\d+(?:\.\d+)?)\s*s?$`)
	reAfter  = regexp.MustCompile(`^after(?:\s*([+-]\s*\d+(?:\.\d+)?)\s*s?)?$`)
)

func parseParagraph(p string, idx int, s *Spec) (Beat, bool, []Issue) {
	path := fmt.Sprintf("/script/%d", idx)
	var b Beat
	var issues []Issue
	anchored := false
	rest := p
	var notes []string
	for {
		m := reMarkup.FindStringSubmatchIndex(rest)
		if m == nil {
			break
		}
		tok := rest[m[2]:m[3]]
		rest = rest[m[1]:]
		if strings.HasPrefix(tok, "(") {
			notes = append(notes, strings.TrimSpace(tok[1:len(tok)-1]))
			continue
		}
		inner := strings.TrimSpace(tok[1 : len(tok)-1])
		an, ok, msg := parseAnchorToken(inner, s)
		if !ok {
			issues = append(issues, errI("script_markup", path, "Paragraph %d: %s.", idx+1, msg))
			continue
		}
		b.Anchor, anchored = an, true
	}
	b.Text = strings.Join(strings.Fields(rest), " ")
	if len(notes) > 0 {
		b.Delivery = strings.Join(notes, "; ")
	}
	return b, anchored, issues
}

// parseAnchorToken understands @clip[+-s], @clip:end[+-s], after[+-s] and clock times.
func parseAnchorToken(tok string, s *Spec) (Anchor, bool, string) {
	low := strings.ToLower(strings.TrimSpace(tok))
	if m := reAfter.FindStringSubmatch(low); m != nil {
		a := Anchor{Kind: AnchorAfter}
		if m[1] != "" {
			v, _ := strconv.ParseFloat(strings.ReplaceAll(m[1], " ", ""), 64)
			if v >= 0 {
				g := v
				a.Gap = &g
			} else {
				a.Offset = v
			}
		}
		return a, true, ""
	}
	if strings.HasPrefix(tok, "@") {
		return parseClipRef(strings.TrimSpace(tok[1:]), s)
	}
	if t, ok := ParseClock(tok); ok {
		return Anchor{Kind: AnchorTime, At: &t}, true, ""
	}
	return Anchor{}, false, fmt.Sprintf("MetaRush can't read [%s] (write [@clip+0.5], [0:12.4] or [after])", tok)
}

// parseClipRef reads clip[:start|:end][+-seconds]; a clip ID that itself ends in
// "-2" wins over reading "-2" as an offset.
func parseClipRef(ref string, s *Spec) (Anchor, bool, string) {
	edge, offStr := "", ""
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		suf := strings.ToLower(ref[i+1:])
		for _, e := range []string{"start", "end"} {
			if strings.HasPrefix(suf, e) {
				edge, offStr, ref = e, strings.TrimSpace(suf[len(e):]), strings.TrimSpace(ref[:i])
				break
			}
		}
		if edge == "" {
			return Anchor{}, false, fmt.Sprintf("MetaRush can't read [@%s] (write [@clip], [@clip+0.5] or [@clip:end])", ref)
		}
	}
	id := resolveClipRef(ref, s)
	if id == "" && offStr == "" {
		if m := reOffset.FindStringSubmatch(ref); m != nil {
			if id = resolveClipRef(strings.TrimSpace(m[1]), s); id != "" {
				offStr = m[2]
			} else {
				ref = strings.TrimSpace(m[1])
			}
		}
	}
	if id == "" {
		return Anchor{}, false, fmt.Sprintf("there's no clip called “%s” in this cut", ref)
	}
	a := Anchor{Kind: AnchorClip, ClipID: id}
	if edge == "end" {
		a.Edge = "end"
	}
	if offStr != "" {
		v, err := strconv.ParseFloat(strings.TrimSuffix(strings.ReplaceAll(offStr, " ", ""), "s"), 64)
		if err != nil {
			return Anchor{}, false, fmt.Sprintf("MetaRush can't read the offset “%s” (like +0.5 or -1)", offStr)
		}
		a.Offset = v
	}
	return a, true, ""
}

// resolveClipRef finds a clip by ID, then by label (case-insensitive).
func resolveClipRef(ref string, s *Spec) string {
	for _, c := range s.Clips {
		if c.ID == ref {
			return c.ID
		}
	}
	for _, c := range s.Clips {
		if strings.EqualFold(c.Label, ref) || strings.EqualFold(strings.ReplaceAll(c.Label, " ", "-"), ref) {
			return c.ID
		}
	}
	return ""
}

// ParseClock reads 12.4, 12.4s, 0:12.4, 1:02:03.5 as seconds.
func ParseClock(v string) (float64, bool) {
	v = strings.TrimSuffix(strings.TrimSpace(v), "s")
	if v == "" {
		return 0, false
	}
	parts := strings.Split(v, ":")
	if len(parts) > 3 {
		return 0, false
	}
	total := 0.0
	for i, p := range parts {
		x, err := strconv.ParseFloat(p, 64)
		if err != nil || x < 0 || (i > 0 && x >= 60) {
			return 0, false
		}
		total = total*60 + x
	}
	return round3(total), true
}
