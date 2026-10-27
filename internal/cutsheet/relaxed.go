package cutsheet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Relaxed JSON is what attribute blocks ({label: "x"}) and graphics parameters
// use: ordinary JSON values, except that object keys may be bare names. Number
// literals are kept exactly as written.

type relaxedError struct {
	pos int // rune offset of the problem
	msg string
}

func (e *relaxedError) Error() string { return e.msg }

var jsonNumberRe = regexp.MustCompile(`^-?(0|[1-9]\d*)(\.\d+)?([eE][-+]?\d+)?`)

// parseRelaxed reads one value from src starting at pos and returns canonical
// JSON plus the offset just past the value.
func parseRelaxed(src []rune, pos int) (json.RawMessage, int, error) {
	var buf bytes.Buffer
	p := &relaxedParser{src: src, pos: pos, out: &buf}
	if err := p.value(0); err != nil {
		return nil, p.pos, err
	}
	return buf.Bytes(), p.pos, nil
}

type relaxedParser struct {
	src []rune
	pos int
	out *bytes.Buffer
}

func (p *relaxedParser) fail(f string, a ...any) error {
	return &relaxedError{pos: p.pos, msg: fmt.Sprintf(f, a...)}
}

func (p *relaxedParser) skipSpace() {
	for p.pos < len(p.src) && unicode.IsSpace(p.src[p.pos]) {
		p.pos++
	}
}

func (p *relaxedParser) value(depth int) error {
	if depth > 32 {
		return p.fail("this value is nested too deeply")
	}
	p.skipSpace()
	if p.pos >= len(p.src) {
		return p.fail("a value is missing here")
	}
	switch c := p.src[p.pos]; {
	case c == '{':
		return p.object(depth)
	case c == '[':
		return p.array(depth)
	case c == '"' || c == '“':
		s, err := p.str()
		if err != nil {
			return err
		}
		p.out.WriteString(quote(s))
		return nil
	case c == '-' || (c >= '0' && c <= '9'):
		m := jsonNumberRe.FindString(string(p.src[p.pos:min(len(p.src), p.pos+64)]))
		if m == "" {
			return p.fail("this is not a number")
		}
		p.out.WriteString(m)
		p.pos += len([]rune(m))
		return nil
	default:
		start := p.pos
		w := p.word()
		switch w {
		case "true", "false", "null":
			p.out.WriteString(w)
			return nil
		case "":
			return p.fail("unexpected %q", string(c))
		}
		p.pos = start
		return p.fail("text values need quotes: write %s", quote(w))
	}
}

func (p *relaxedParser) word() string {
	start := p.pos
	for p.pos < len(p.src) && (p.src[p.pos] == '_' || p.src[p.pos] == '-' || unicode.IsLetter(p.src[p.pos]) || unicode.IsDigit(p.src[p.pos])) {
		p.pos++
	}
	return string(p.src[start:p.pos])
}

func (p *relaxedParser) str() (string, error) {
	open := p.src[p.pos]
	start := p.pos
	if open == '“' {
		end := -1
		for i := p.pos + 1; i < len(p.src); i++ {
			if p.src[i] == '”' {
				end = i
				break
			}
		}
		if end < 0 {
			return "", p.fail("this quote is never closed (missing ”)")
		}
		p.pos = end + 1
		return string(p.src[start+1 : end]), nil
	}
	i := p.pos + 1
	for i < len(p.src) {
		switch p.src[i] {
		case '\\':
			i += 2
			continue
		case '"':
			var s string
			if err := json.Unmarshal([]byte(string(p.src[start:i+1])), &s); err != nil {
				return "", p.fail("this quoted text has a bad escape (use \\\" for a quote and \\\\ for a backslash)")
			}
			p.pos = i + 1
			return s, nil
		}
		i++
	}
	return "", p.fail("this quote is never closed (missing \")")
}

func (p *relaxedParser) object(depth int) error {
	p.pos++ // {
	p.out.WriteByte('{')
	seen := map[string]bool{}
	for i := 0; ; i++ {
		p.skipSpace()
		if p.pos >= len(p.src) {
			return p.fail("this { is never closed (missing })")
		}
		if p.src[p.pos] == '}' {
			p.pos++
			p.out.WriteByte('}')
			return nil
		}
		if i > 0 {
			if p.src[p.pos] != ',' {
				return p.fail("expected a comma or }")
			}
			p.pos++
			p.skipSpace()
		}
		var key string
		if p.pos < len(p.src) && (p.src[p.pos] == '"' || p.src[p.pos] == '“') {
			k, err := p.str()
			if err != nil {
				return err
			}
			key = k
		} else {
			key = p.word()
			if key == "" {
				return p.fail("expected a name (like label:)")
			}
		}
		if seen[key] {
			return p.fail("%s is given twice", key)
		}
		seen[key] = true
		p.skipSpace()
		if p.pos >= len(p.src) || p.src[p.pos] != ':' {
			return p.fail("expected : after %s", key)
		}
		p.pos++
		if i > 0 {
			p.out.WriteByte(',')
		}
		p.out.WriteString(quote(key))
		p.out.WriteByte(':')
		if err := p.value(depth + 1); err != nil {
			return err
		}
	}
}

func (p *relaxedParser) array(depth int) error {
	p.pos++ // [
	p.out.WriteByte('[')
	for i := 0; ; i++ {
		p.skipSpace()
		if p.pos >= len(p.src) {
			return p.fail("this [ is never closed (missing ])")
		}
		if p.src[p.pos] == ']' {
			p.pos++
			p.out.WriteByte(']')
			return nil
		}
		if i > 0 {
			if p.src[p.pos] != ',' {
				return p.fail("expected a comma or ]")
			}
			p.pos++
			p.out.WriteByte(',')
		}
		if err := p.value(depth + 1); err != nil {
			return err
		}
	}
}

// relaxedObject parses a whole {…} block into ordered members.
func relaxedObject(src string) ([]member, error) {
	rs := []rune(strings.TrimSpace(src))
	raw, end, err := parseRelaxed(rs, 0)
	if err != nil {
		return nil, err
	}
	if end != len(rs) {
		return nil, &relaxedError{pos: end, msg: "unexpected text after the closing }"}
	}
	return orderedMembers(raw)
}

// member is one key/value of a JSON object, in source order.
type member struct {
	Key   string
	Value json.RawMessage
}

// orderedMembers splits a JSON object into members, keeping their order.
func orderedMembers(raw json.RawMessage) ([]member, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("expected an object")
	}
	var out []member
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, member{Key: kt.(string), Value: v})
	}
	return out, nil
}
