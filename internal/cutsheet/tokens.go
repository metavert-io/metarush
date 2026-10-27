package cutsheet

import (
	"encoding/json"
	"strings"
	"unicode"
)

type tokKind int

const (
	tWord   tokKind = iota
	tString         // quoted text; text is the unquoted value
	tSep            // · or |
	tArrow          // → or ->
	tComma          // ,
	tBlock          // {…}; raw is canonical JSON
	tList           // […]; raw is canonical JSON
)

// token is one piece of a body line. Columns are 1-based rune positions; end
// is exclusive.
type token struct {
	kind tokKind
	text string
	raw  json.RawMessage
	col  int
	end  int
}

func (t token) is(words ...string) bool {
	if t.kind != tWord {
		return false
	}
	for _, w := range words {
		if strings.EqualFold(t.text, w) {
			return true
		}
	}
	return false
}

func isWordBreak(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune("·|,\"“{[→", r)
}

// tokenize splits a line (already stripped of its list marker) into tokens.
// base is the 1-based column of line[0].
func tokenize(line []rune, base int) ([]token, *Problem) {
	var out []token
	i := 0
	for i < len(line) {
		r := line[i]
		switch {
		case unicode.IsSpace(r):
			i++
		case r == '·' || r == '|':
			out = append(out, token{kind: tSep, text: string(r), col: base + i, end: base + i + 1})
			i++
		case r == '→':
			out = append(out, token{kind: tArrow, text: "→", col: base + i, end: base + i + 1})
			i++
		case r == '-' && i+1 < len(line) && line[i+1] == '>':
			out = append(out, token{kind: tArrow, text: "->", col: base + i, end: base + i + 2})
			i += 2
		case r == ',':
			out = append(out, token{kind: tComma, text: ",", col: base + i, end: base + i + 1})
			i++
		case r == '"' || r == '“':
			p := &relaxedParser{src: line, pos: i}
			s, err := p.str()
			if err != nil {
				return nil, &Problem{Severity: "error", Code: "bad_quote", Col: base + i, EndCol: base + len(line), Message: err.Error(), Hint: `Put text in "double quotes"; inside, write \" for a quote mark.`}
			}
			out = append(out, token{kind: tString, text: s, col: base + i, end: base + p.pos})
			i = p.pos
		case r == '{' || r == '[':
			raw, end, err := parseRelaxed(line, i)
			if err != nil {
				pos := i
				if re, ok := err.(*relaxedError); ok {
					pos = re.pos
				}
				return nil, &Problem{Severity: "error", Code: "bad_block", Col: base + pos, EndCol: base + pos + 1, Message: "This {…} block can't be read: " + err.Error(), Hint: `Blocks look like {label: "Opening"}: names, a colon, and values (text in quotes).`}
			}
			k := tBlock
			if r == '[' {
				k = tList
			}
			out = append(out, token{kind: k, text: string(line[i:end]), raw: raw, col: base + i, end: base + end})
			i = end
		default:
			start := i
			for i < len(line) && !isWordBreak(line[i]) && !(line[i] == '-' && i+1 < len(line) && line[i+1] == '>') {
				i++
			}
			out = append(out, token{kind: tWord, text: string(line[start:i]), col: base + start, end: base + i})
		}
	}
	return out, nil
}

// splitSeps splits tokens at top-level separators (·).
func splitSeps(ts []token) [][]token {
	var out [][]token
	var cur []token
	for _, t := range ts {
		if t.kind == tSep {
			out = append(out, cur)
			cur = nil
			continue
		}
		cur = append(cur, t)
	}
	return append(out, cur)
}

// span of a token list for error marks.
func spanOf(ts []token) (int, int) {
	if len(ts) == 0 {
		return 0, 0
	}
	return ts[0].col, ts[len(ts)-1].end
}

func joinWords(ts []token) string {
	var parts []string
	for _, t := range ts {
		parts = append(parts, t.text)
	}
	return strings.Join(parts, " ")
}
