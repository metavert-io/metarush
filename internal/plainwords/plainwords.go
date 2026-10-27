// Package plainwords checks text meant for people against the voice guide
// (skills/_shared/voice.md): plain CLI output (without --details or --json),
// dashboard copy outside a Details disclosure, and Claude's replies say what a
// change means for the video in everyday words. It finds what a person should
// not have to read: internal IDs and tokens, command flags and commands, file
// paths, JSON, internal codes and unexplained jargon.
//
// The jargon list lives in rules.json, which the dashboard's own check
// (web/scripts/plain-words-test.mjs) reads too, so both sides agree. The
// small rules of English that MetaRush's own sentences share live here too,
// so every surface says them alike (AOrAn: "an 8-second shot").
package plainwords

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Kinds of findings.
const (
	KindID      = "id"      // an internal ID (ast_…, rev_…) or a confirm token
	KindFlag    = "flag"    // a command flag (--details)
	KindCommand = "command" // a command to type (metarush cut check …)
	KindPath    = "path"    // an absolute file path
	KindJSON    = "json"    // JSON
	KindCode    = "code"    // an internal code (E_NOT_FOUND, heavy_crop)
	KindJargon  = "jargon"  // a term from rules.json without a plain-words explanation
	KindLeak    = "leak"    // a value that never got filled in (undefined, NaN)
)

// Finding is one thing in a text that a person should not have to read.
type Finding struct {
	Kind  string `json:"kind"`
	Text  string `json:"text"`
	Plain string `json:"plain,omitempty"` // jargon: what to say instead
}

func (f Finding) String() string {
	if f.Plain != "" {
		return fmt.Sprintf("%s %q (say: %s)", f.Kind, f.Text, f.Plain)
	}
	return fmt.Sprintf("%s %q", f.Kind, f.Text)
}

// Term is one entry of the jargon list.
type Term struct {
	Term string `json:"term"`
	// Plain is what to say instead (or where the term belongs).
	Plain string `json:"plain"`
	// Case: match the exact case ("QA", "LUFS"); otherwise any case.
	Case bool `json:"case,omitempty"`
	// Kind overrides "jargon" (e.g. "leak" for undefined/NaN).
	Kind string `json:"kind,omitempty"`
}

//go:embed rules.json
var rulesJSON []byte

var rules struct {
	Jargon []Term      `json:"jargon"`
	Panel  []PanelRule `json:"panel"`
}

// PanelRule is one of the Claude Code panel's stricter rules (D140): what its
// step lines, cards and notes never show (file names, version codes,
// millisecond times, tool words).
type PanelRule struct {
	Kind    string `json:"kind"`
	Pattern string `json:"pattern"`
	Plain   string `json:"plain"`
	re      *regexp.Regexp
}

// CheckPanel is CheckShown with the panel's stricter rules (rules.json
// "panel") applied to MetaRush's own words: what the Claude Code panel shows
// (step lines, cards, notes) says what Claude's work means for the video,
// never its files, tools or versions. People's own words (“…”) are left to
// CheckShown.
func CheckPanel(text string) []Finding {
	out := CheckShown(text)
	ours, _ := SplitTheirs(text)
	ours = urlRe.ReplaceAllString(ours, " ")
	seen := map[string]bool{}
	for _, r := range rules.Panel {
		for _, m := range r.re.FindAllString(ours, -1) {
			m = strings.TrimSpace(m)
			if k := r.Kind + "\x00" + m; !seen[k] {
				seen[k] = true
				out = append(out, Finding{Kind: r.Kind, Text: m, Plain: r.Plain})
			}
		}
	}
	return out
}

type compiled struct {
	t  Term
	re *regexp.Regexp
}

var terms []compiled

func init() {
	if err := json.Unmarshal(rulesJSON, &rules); err != nil {
		panic("plainwords: rules.json: " + err.Error())
	}
	for i := range rules.Panel {
		rules.Panel[i].re = regexp.MustCompile(rules.Panel[i].Pattern)
	}
	for _, t := range rules.Jargon {
		q := regexp.QuoteMeta(t.Term)
		// Word edges only where the term starts or ends with a word character
		// ("[object Object]" has none).
		if isWord(rune(t.Term[0])) {
			q = `(?:^|[^\p{L}\p{N}_-])(` + q
		} else {
			q = `(` + q
		}
		if isWord(rune(t.Term[len(t.Term)-1])) {
			q += `)(?:$|[^\p{L}\p{N}_-])`
		} else {
			q += `)`
		}
		if !t.Case {
			q = `(?i)` + q
		}
		terms = append(terms, compiled{t, regexp.MustCompile(q)})
	}
}

func isWord(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

// Jargon returns the shared jargon list (for tests and tools).
func Jargon() []Term { return append([]Term(nil), rules.Jargon...) }

var (
	// A link is for clicking, not reading: an ID or path inside one is fine.
	urlRe = regexp.MustCompile(`(?i)\b(?:https?|file)://[^\s)>\]"'` + "`" + `]+`)
	// Internal IDs: a short prefix and a 26-character ULID (ast_01h…), confirm
	// tokens, extension tokens and full content hashes.
	idRe    = regexp.MustCompile(`\b[a-z]{1,4}_[0-9a-z]{26}\b|\bct_\d+_[0-9a-f]{16,}\b|\bmrx_[0-9A-Za-z_-]{8,}|\b[0-9a-f]{40,64}\b`)
	flagRe  = regexp.MustCompile(`(?:^|[\s(\[` + "`" + `"'=])(--[a-z][a-z0-9-]{1,40})\b`)
	cmdRe   = regexp.MustCompile(`\b(metarush\s+[a-z][a-z-]*(?:\s+[a-z][a-z-]*)?|(?:brew|npm|npx|pip3?|git|gh|yt-dlp|ffmpeg|ffprobe)\s+(?:install|upgrade|update|run|clone|auth|-[a-z]))`)
	pathRe  = regexp.MustCompile(`(?:^|[\s(\[` + "`" + `"'=:])((?:~|<home>|\.\.?|/(?:Users|home|private|tmp|var|opt|etc|Volumes|Applications|Library|usr|mnt|srv|root))/[^\s"'` + "`" + `)\]]*|[A-Z]:\\[^\s"']+)`)
	jsonRe  = regexp.MustCompile(`\{\s*"[A-Za-z_][A-Za-z0-9_]*"\s*:|\[\s*\{\s*"`)
	errCode = regexp.MustCompile(`\bE_[A-Z][A-Z_]{2,}\b`)
	// snake_case words (heavy_crop, no_worker): internal codes, unless they are
	// a file name (take_01.mov keeps its extension).
	snakeRe = regexp.MustCompile(`\b[a-z]{2,}(?:_[a-z]{2,})+\b`)
)

// Check lists what in text a person should not have to read. Jargon is fine
// with a plain-words explanation right after it ("LUFS (how loud it sounds)")
// or inside a parenthesis that explains something else.
func Check(text string) []Finding {
	var out []Finding
	seen := map[string]bool{}
	add := func(kind, s, plain string) {
		k := kind + "\x00" + s
		if !seen[k] {
			seen[k] = true
			out = append(out, Finding{Kind: kind, Text: s, Plain: plain})
		}
	}
	text = urlRe.ReplaceAllString(text, " ")
	for _, m := range idRe.FindAllString(text, -1) {
		add(KindID, m, "")
	}
	for _, m := range flagRe.FindAllStringSubmatch(text, -1) {
		add(KindFlag, m[1], "")
	}
	for _, m := range cmdRe.FindAllString(text, -1) {
		add(KindCommand, m, "")
	}
	for _, m := range pathRe.FindAllStringSubmatch(text, -1) {
		add(KindPath, strings.TrimRight(m[1], ".,;:"), "")
	}
	if m := jsonRe.FindString(text); m != "" {
		add(KindJSON, m, "")
	}
	for _, m := range errCode.FindAllString(text, -1) {
		add(KindCode, m, "")
	}
	for _, loc := range snakeRe.FindAllStringIndex(text, -1) {
		rest := text[loc[1]:]
		if strings.HasPrefix(rest, ".") && len(rest) > 1 && isWord(rune(rest[1])) {
			continue // a file name: take_two.mov
		}
		if loc[0] > 0 && strings.ContainsRune("./@#-", rune(text[loc[0]-1])) {
			continue // part of a name or handle (@some_one, a.b_c)
		}
		add(KindCode, text[loc[0]:loc[1]], "")
	}
	for _, c := range terms {
		for _, m := range c.re.FindAllStringSubmatchIndex(text, -1) {
			at, end := m[2], m[3]
			before := text[max(0, at-60):at]
			inParens := strings.LastIndex(before, "(") > strings.LastIndex(before, ")")
			if inParens || Explained(text[end:]) {
				continue
			}
			kind := KindJargon
			if c.t.Kind != "" {
				kind = c.t.Kind
			}
			add(kind, text[at:end], c.t.Plain)
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return order(out[i].Kind) < order(out[j].Kind) })
	return out
}

func order(kind string) int {
	return strings.Index("id flag command path json code jargon leak", kind)
}

// People's own words — clip titles, file names, people's, projects' and
// resources' names, what someone said — are theirs, not MetaRush's: a person
// typed them, or a call app named a file, so they may look like anything a
// code does (riverside_sam_raw-video-cfr_garden_show_0002). Text shown to
// people marks them (D113): the dashboard wraps them in <Theirs>
// (data-user-content), and plain text — CLI output, MetaRush's messages,
// Claude's replies — puts them in curly quotes, “…” (the voice guide's rule;
// the CLI writes them with theirs()). CheckShown checks such text: what is
// marked is skipped, except internal IDs and tokens and missing values, which
// never belong in a name. Everything else is checked as Check does, so a real
// code in MetaRush's own words is still found.

// TheirsOpen and TheirsClose mark people's own words in plain text.
const (
	TheirsOpen  = "“"
	TheirsClose = "”"
)

// SplitTheirs separates a text into MetaRush's words (people's words replaced
// by “it”, so sentences still read) and the people's words that were marked.
func SplitTheirs(text string) (ours string, theirs []string) {
	var b strings.Builder
	for {
		i := strings.Index(text, TheirsOpen)
		if i < 0 {
			break
		}
		j := strings.Index(text[i+len(TheirsOpen):], TheirsClose)
		if j < 0 {
			break // an opening quote without its closing one marks nothing
		}
		b.WriteString(text[:i])
		b.WriteString(TheirsOpen + "it" + TheirsClose)
		theirs = append(theirs, text[i+len(TheirsOpen):i+len(TheirsOpen)+j])
		text = text[i+len(TheirsOpen)+j+len(TheirsClose):]
	}
	b.WriteString(text)
	return b.String(), theirs
}

// CheckShown lists what in text shown to a person they should not have to
// read, with people's own words (marked “…”) checked only for internal IDs,
// tokens and missing values.
func CheckShown(text string) []Finding {
	ours, theirs := SplitTheirs(text)
	out := Check(ours)
	for _, t := range theirs {
		out = append(out, InTheirs(t)...)
	}
	return out
}

// InTheirs is what people's own words may not hold either: an internal ID or
// token (a name never needs one) or a value that never got filled in.
func InTheirs(text string) []Finding {
	var out []Finding
	for _, f := range Check(text) {
		if f.Kind == KindID || f.Kind == KindLeak {
			out = append(out, f)
		}
	}
	return out
}

// Explained reports whether a term is followed by a short plain-words
// explanation ("LUFS (how loud it sounds)", "B-roll — the extra shots").
func Explained(after string) bool {
	if len(after) > 80 {
		after = after[:80]
	}
	after = strings.ToLower(strings.TrimLeft(after, "  "))
	for _, p := range []string{"(", "— ", "- ", ", which", ", meaning", ", that is", "means", "is when", "is the", "are the", ": "} {
		if strings.HasPrefix(after, p) {
			return true
		}
	}
	return false
}

// Allow says which findings an intentional case may have: kind or
// kind:text (text compared case-insensitively).
type Allow []string

// Filter drops the findings an allowlist entry covers.
func (a Allow) Filter(fs []Finding) []Finding {
	var out []Finding
	for _, f := range fs {
		ok := false
		for _, e := range a {
			kind, text, hasText := strings.Cut(e, ":")
			if kind == f.Kind && (!hasText || strings.EqualFold(text, f.Text)) {
				ok = true
				break
			}
		}
		if !ok {
			out = append(out, f)
		}
	}
	return out
}
