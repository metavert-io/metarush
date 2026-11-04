// Package secretfields names the input fields of operations that hold a
// secret although their names don't say so, such as account.password's
// "current" and "new". The activity log hides their values like those of
// secret-named fields (internal/logbook); migration 71 took them out of the
// log entries older versions wrote with them as typed, and spooled entries
// and workspace exports are cleaned the same way (Scrub). A guard test
// (internal/cli) wants every operation's field whose name often holds a
// secret (pin, code, current…) declared here or named there as none.
package secretfields

import (
	"sort"
	"strings"
)

// Hidden is what the activity log writes in place of a hidden value
// (logbook.Redacted).
const Hidden = "[redacted]"

// byOp is each operation's secret fields, at the top level of its input.
var byOp = map[string][]string{
	"account.password": {"current", "new"},
}

// Of is an operation's secret fields as a set (nil: none).
func Of(op string) map[string]bool {
	fs := byOp[op]
	if len(fs) == 0 {
		return nil
	}
	out := make(map[string]bool, len(fs))
	for _, f := range fs {
		out[f] = true
	}
	return out
}

// All is every operation's secret fields (a copy).
func All() map[string][]string {
	out := make(map[string][]string, len(byOp))
	for op, fs := range byOp {
		out[op] = append([]string(nil), fs...)
	}
	return out
}

// minText is the shortest value taken out of an entry's texts: shorter ones
// would blank ordinary words (the log's own minimum for exact values).
const minText = 4

// Scrub takes the values of an operation's secret fields out of what a log
// entry kept of one call of it: input (the entry's shortened input) gets
// Hidden in their place, and each value is taken out of texts (its summary,
// error and command line) too. It reports whether anything changed; an entry
// already clean is left as it is.
func Scrub(op string, input map[string]any, texts ...*string) bool {
	fields := byOp[op]
	if len(fields) == 0 || input == nil {
		return false
	}
	changed := false
	var values []string
	for _, f := range fields {
		v, ok := input[f]
		if !ok || v == nil || v == "" || v == Hidden {
			continue
		}
		if s, ok := v.(string); ok {
			// A long value was kept clipped: "<start>… (+N characters)".
			if i := strings.Index(s, "… (+"); i > 0 && strings.HasSuffix(s, " characters)") {
				s = s[:i]
			}
			if len(strings.TrimSpace(s)) >= minText {
				values = append(values, s)
			}
		}
		input[f] = Hidden
		changed = true
	}
	if !changed {
		return false
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, t := range texts {
		if t == nil || *t == "" {
			continue
		}
		for _, v := range values {
			*t = strings.ReplaceAll(*t, v, Hidden)
		}
	}
	return true
}
