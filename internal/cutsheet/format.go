package cutsheet

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Numbers are printed as the shortest decimal that reads back to the same
// float64, and times/percentages are built from that decimal by string
// manipulation only, so every value survives Print → Parse exactly.

func fmtNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

var plainNum = regexp.MustCompile(`^[-+]?(\d+\.?\d*|\.\d+)$`)

// parseNum reads a plain decimal (no exponent, no thousands separators).
func parseNum(s string) (float64, bool) {
	if !plainNum.MatchString(s) {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, false
	}
	return v, true
}

// Clock prints a time the way cut sheets do (m:ss.fff).
func Clock(v float64) string { return fmtClock(v) }

// ParseClock reads a time as people write it: m:ss.fff, h:mm:ss.fff or a
// number of seconds ("12.4", "12.4s"). Negative times are refused.
func ParseClock(s string) (float64, bool) {
	v, ok := parseClock(s)
	if !ok {
		v, ok = parseNum(strings.TrimSpace(s))
	}
	return v, ok && v >= 0
}

// fmtClock prints a time as m:ss.fff (h:mm:ss.fff past an hour), trimming
// trailing zeros. Values that do not fit a clock (negative, huge) are printed
// as seconds with an "s" suffix, which the parser also accepts.
func fmtClock(v float64) string {
	if v < 0 || math.Signbit(v) || v >= 1e9 {
		return fmtNum(v) + "s"
	}
	d := fmtNum(v)
	ip, frac, _ := strings.Cut(d, ".")
	n, err := strconv.ParseInt(ip, 10, 64)
	if err != nil {
		return d + "s"
	}
	h, m, s := n/3600, (n%3600)/60, n%60
	var b strings.Builder
	if h > 0 {
		b.WriteString(strconv.FormatInt(h, 10))
		b.WriteByte(':')
		b.WriteString(pad2(m))
	} else {
		b.WriteString(strconv.FormatInt(m, 10))
	}
	b.WriteByte(':')
	b.WriteString(pad2(s))
	if frac != "" {
		b.WriteByte('.')
		b.WriteString(frac)
	}
	return b.String()
}

func pad2(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}

var (
	clockHMS = regexp.MustCompile(`^(\d+):(\d{1,2}):(\d{1,2})(?:\.(\d+))?$`)
	clockMS  = regexp.MustCompile(`^(\d+):(\d{1,2})(?:\.(\d+))?$`)
	secsRe   = regexp.MustCompile(`^([-+]?(?:\d+\.?\d*|\.\d+))\s*(?:s|sec|secs|seconds?)$`)
)

// parseClock reads m:ss.fff, h:mm:ss.fff or a number of seconds with an "s".
func parseClock(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	var h, m, sec int64
	var frac string
	if p := clockHMS.FindStringSubmatch(s); p != nil {
		h, _ = strconv.ParseInt(p[1], 10, 64)
		m, _ = strconv.ParseInt(p[2], 10, 64)
		sec, _ = strconv.ParseInt(p[3], 10, 64)
		frac = p[4]
		if m >= 60 || sec >= 60 {
			return 0, false
		}
	} else if p := clockMS.FindStringSubmatch(s); p != nil {
		m, _ = strconv.ParseInt(p[1], 10, 64)
		sec, _ = strconv.ParseInt(p[2], 10, 64)
		frac = p[3]
		if sec >= 60 {
			return 0, false
		}
	} else if p := secsRe.FindStringSubmatch(s); p != nil {
		return parseNum(p[1])
	} else {
		return 0, false
	}
	total := h*3600 + m*60 + sec
	d := strconv.FormatInt(total, 10)
	if frac != "" {
		d += "." + frac
	}
	return parseNum(d)
}

// fmtDur prints a length of time: "0.4 s".
func fmtDur(v float64) string { return fmtNum(v) + " s" }

var durRe = regexp.MustCompile(`^([-+]?(?:\d+\.?\d*|\.\d+))(s|sec|secs|seconds?)?$`)

// shiftDecimal moves the decimal point of a plain decimal string by places
// (positive = multiply by 10^places). Exact: no floating point involved.
func shiftDecimal(s string, places int) string {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimLeft(s, "+-")
	ip, fp, _ := strings.Cut(s, ".")
	digits := ip + fp
	point := len(ip) + places
	for point < 0 {
		digits = "0" + digits
		point++
	}
	for point > len(digits) {
		digits += "0"
	}
	ip, fp = strings.TrimLeft(digits[:point], "0"), strings.TrimRight(digits[point:], "0")
	if ip == "" {
		ip = "0"
	}
	out := ip
	if fp != "" {
		out += "." + fp
	}
	if neg && out != "0" {
		out = "-" + out
	}
	return out
}

// fmtPercent prints a 0–1 fraction (or linear gain) as a percentage.
func fmtPercent(v float64) string {
	if math.Signbit(v) && v == 0 {
		return "-0%"
	}
	return shiftDecimal(fmtNum(v), 2) + "%"
}

// parsePercent reads "80%" into 0.8.
func parsePercent(s string) (float64, bool) {
	t, ok := strings.CutSuffix(strings.TrimSpace(s), "%")
	if !ok || !plainNum.MatchString(t) {
		return 0, false
	}
	if t == "-0" {
		return math.Copysign(0, -1), true
	}
	return parseNum(shiftDecimal(t, -2))
}

// quote writes s as a JSON string literal without HTML escaping (also a valid
// YAML double-quoted scalar).
func quote(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}

// rawJSON prints a JSON value on one line: object keys keep their order and are
// left unquoted when they are plain names, numbers are copied literally, and
// strings are re-quoted without HTML escapes. parseRelaxed reads it back.
func rawJSON(v json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.UseNumber()
	var b strings.Builder
	if err := reprint(dec, &b, true); err != nil {
		return string(bytes.TrimSpace(v))
	}
	return b.String()
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func reprint(dec *json.Decoder, b *strings.Builder, bareKeys bool) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	switch v := t.(type) {
	case json.Delim:
		switch v {
		case '{':
			b.WriteByte('{')
			for i := 0; dec.More(); i++ {
				if i > 0 {
					b.WriteString(", ")
				}
				kt, err := dec.Token()
				if err != nil {
					return err
				}
				k, _ := kt.(string)
				if bareKeys && identRe.MatchString(k) && !reservedWord(k) {
					b.WriteString(k)
				} else {
					b.WriteString(quote(k))
				}
				b.WriteString(": ")
				if err := reprint(dec, b, bareKeys); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
			b.WriteByte('}')
		case '[':
			b.WriteByte('[')
			for i := 0; dec.More(); i++ {
				if i > 0 {
					b.WriteString(", ")
				}
				if err := reprint(dec, b, bareKeys); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
			b.WriteByte(']')
		}
	case string:
		b.WriteString(quote(v))
	case json.Number:
		b.WriteString(string(v))
	case bool:
		b.WriteString(strconv.FormatBool(v))
	case nil:
		b.WriteString("null")
	}
	return nil
}

// reservedWord: bare words that mean something else in relaxed JSON.
func reservedWord(k string) bool { return k == "true" || k == "false" || k == "null" }
