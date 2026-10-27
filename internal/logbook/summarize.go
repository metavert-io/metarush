package logbook

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Size limits for what an entry keeps.
const (
	maxInputString = 240     // characters of one input string
	maxInputList   = 12      // items of one input list
	maxInputDepth  = 4       // nesting levels of the input
	maxInputBytes  = 4 << 10 // the whole input summary
	maxOutputIDs   = 20
	maxArgs        = 60    // program arguments kept
	maxArg         = 300   // characters of one argument
	maxStderrTail  = 16000 // characters of error output kept (longer than InlineMax: goes to the media store)
	InlineMax      = 4096  // longer texts go to the media store (or this machine's home)
	maxBodyBytes   = 1 << 20
)

// Clip shortens s to n characters, saying how much was left out.
func Clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + fmt.Sprintf("… (+%d characters)", len(r)-n)
}

// Tail keeps the last n characters of s.
func Tail(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}

// privateFields are input fields holding prompt text: kept only under the
// log.prompts rules (workspace: shortened; local and off: not in the database).
var privateFields = map[string]bool{"prompt": true}

// ShortenInput decodes an operation's JSON input and keeps a short, redacted
// summary of it.
func ShortenInput(r *Redactor, raw json.RawMessage, privacy string) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return map[string]any{"_unparsed": Clip(r.String(string(raw)), maxInputString)}
	}
	m, ok := v.(map[string]any)
	if !ok {
		return map[string]any{"_value": shorten(r, v, 0, privacy, "")}
	}
	if len(m) == 0 {
		return nil
	}
	// Notes people write about a person are kept as their size only: the log
	// records that they changed, not what they say.
	if n, ok := m["notes"].(string); ok {
		m["notes"] = fmt.Sprintf("[%d characters]", utf8.RuneCountInString(n))
	}
	out := shorten(r, m, 0, privacy, "").(map[string]any)
	if b, _ := json.Marshal(out); len(b) > maxInputBytes {
		// Too big: replace the largest values with their size, largest first.
		type kv struct {
			k string
			n int
		}
		var sizes []kv
		for k, val := range out {
			bb, _ := json.Marshal(val)
			sizes = append(sizes, kv{k, len(bb)})
		}
		sort.Slice(sizes, func(i, j int) bool { return sizes[i].n > sizes[j].n })
		total := len(b)
		for _, s := range sizes {
			if total <= maxInputBytes {
				break
			}
			out[s.k] = fmt.Sprintf("[%d bytes left out]", s.n)
			total -= s.n
		}
	}
	return out
}

func shorten(r *Redactor, v any, depth int, privacy, key string) any {
	switch x := v.(type) {
	case string:
		if privateFields[strings.ToLower(key)] {
			switch privacy {
			case PrivacyLocal:
				return fmt.Sprintf("[%d characters, kept on this machine]", utf8.RuneCountInString(x))
			case PrivacyOff:
				return fmt.Sprintf("[%d characters, not recorded]", utf8.RuneCountInString(x))
			}
		}
		return Clip(r.String(x), maxInputString)
	case map[string]any:
		if depth >= maxInputDepth {
			return fmt.Sprintf("[object with %d fields]", len(x))
		}
		out := make(map[string]any, len(x))
		for k, val := range x {
			if SecretName(k) && val != nil && val != "" {
				out[k] = Redacted
				continue
			}
			out[k] = shorten(r, val, depth+1, privacy, k)
		}
		return out
	case []any:
		if depth >= maxInputDepth {
			return fmt.Sprintf("[list of %d]", len(x))
		}
		n := len(x)
		if n > maxInputList {
			n = maxInputList
		}
		out := make([]any, 0, n+1)
		for _, val := range x[:n] {
			out = append(out, shorten(r, val, depth+1, privacy, key))
		}
		if len(x) > n {
			out = append(out, fmt.Sprintf("… and %d more", len(x)-n))
		}
		return out
	}
	return v
}

var idRe = regexp.MustCompile(`^[a-z]{2,4}_[0-9a-z]{26}$`)

// factKeys are small result values kept to describe an entry.
var factKeys = map[string]bool{"state": true, "status": true, "verdict": true, "number": true, "quality": true, "preset": true, "name": true,
	"ingested": true, "deduped": true, "duplicates": true, "resumed": true, "skipped": true, "failed": true, "valid": true,
	"created": true, "rebased": true, "total": true, "unread": true, "marked": true, "deleted": true, "count": true, "kind": true,
	"done": true, "applied": true, "dismissed": true, "kept": true, "generated": true, "reused": true, "credits": true,
	"into": true, "saved": true, "noChange": true, "ok": true}

// factObjects are result objects whose small values are kept too.
var factObjects = map[string]bool{"render": true, "job": true, "revision": true, "report": true, "project": true, "summary": true, "approval": true, "person": true}

// SummarizeOutput keeps an operation result's size, main IDs, list sizes and a
// few small values.
func SummarizeOutput(out any) *OutputInfo {
	if out == nil {
		return nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	info := &OutputInfo{Bytes: len(b)}
	var v any
	if json.Unmarshal(b, &v) != nil {
		return info
	}
	seen := map[string]bool{}
	var walk func(v any, depth int, key string)
	walk = func(v any, depth int, key string) {
		switch x := v.(type) {
		case string:
			if len(info.IDs) < maxOutputIDs && idRe.MatchString(x) && !seen[x] && (key == "id" || strings.HasSuffix(key, "Id") || strings.HasSuffix(key, "ID")) &&
				!strings.HasPrefix(x, "ws_") && !strings.HasPrefix(x, "usr_") && !strings.HasPrefix(x, "ins_") {
				seen[x] = true
				info.IDs = append(info.IDs, x)
			}
		case map[string]any:
			if depth > 3 {
				return
			}
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys) // deterministic ID order: "id" sorts before most names
			for _, k := range keys {
				walk(x[k], depth+1, k)
			}
		case []any:
			if depth <= 2 && key != "" {
				if info.Counts == nil {
					info.Counts = map[string]int{}
				}
				if _, ok := info.Counts[key]; !ok {
					info.Counts[key] = len(x)
				}
			}
			if depth > 2 {
				return
			}
			for i, val := range x {
				if i >= 5 {
					break
				}
				walk(val, depth+1, key)
			}
		}
	}
	walk(v, 0, "")
	if m, ok := v.(map[string]any); ok {
		facts := map[string]any{}
		keep := func(prefix string, mm map[string]any) {
			for k, val := range mm {
				if !factKeys[k] {
					continue
				}
				switch x := val.(type) {
				case float64, bool:
					facts[prefix+k] = x
				case string:
					if len(x) <= 40 && !idRe.MatchString(x) {
						facts[prefix+k] = x
					}
				}
			}
		}
		keep("", m)
		for name := range factObjects {
			if sub, ok := m[name].(map[string]any); ok {
				keep(name+".", sub)
			}
		}
		if len(facts) > 0 {
			info.Facts = facts
		}
	}
	return info
}

// refsFrom collects references from a decoded input and an output summary.
func refsFrom(input map[string]any, out *OutputInfo) Refs {
	var r Refs
	var walk func(v any, depth int)
	walk = func(v any, depth int) {
		switch x := v.(type) {
		case string:
			r.refFromID(x)
		case map[string]any:
			if depth > 2 {
				return
			}
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(x[k], depth+1)
			}
		case []any:
			if depth > 2 {
				return
			}
			for i, val := range x {
				if i >= 3 {
					break
				}
				walk(val, depth+1)
			}
		}
	}
	walk(input, 0)
	if out != nil {
		for _, id := range out.IDs {
			r.refFromID(id)
		}
	}
	return r
}
