package logbook

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/metavert-io/metarush/internal/config"
)

// Secrets kept by other packages. The providers registry reports every API key
// this process reads or saves (RememberSecret), and long-running processes ask
// every secret source for its values when they start and every hour
// (AddSecretSource), so the log hides those keys even in text people type —
// without log code reading the keychain itself.
var (
	procMu      sync.Mutex
	procSecrets = map[string]bool{}
	openLoggers = map[*Logger]bool{}
	sources     []func(*config.Config) []string
)

// RememberSecret registers secret values with every logger of this process,
// now and later. The Claude Code hook's fingerprints include them.
func RememberSecret(values ...string) {
	var fresh []string
	procMu.Lock()
	for _, v := range values {
		v = strings.TrimSpace(v)
		if len(v) < minSecretLen || procSecrets[v] {
			continue
		}
		procSecrets[v] = true
		fresh = append(fresh, v)
	}
	ls := make([]*Logger, 0, len(openLoggers))
	for l := range openLoggers {
		ls = append(ls, l)
	}
	procMu.Unlock()
	if len(fresh) == 0 {
		return
	}
	for _, l := range ls {
		l.red.addConfigured(fresh...)
	}
}

// Remembered reports whether a value is kept out of the log (RememberSecret).
func Remembered(v string) bool {
	procMu.Lock()
	defer procMu.Unlock()
	return procSecrets[strings.TrimSpace(v)]
}

// AddSecretSource registers a function that lists secrets an installation
// keeps outside its config file (provider API keys). The daemon calls every
// source when it starts and every hour.
func AddSecretSource(f func(cfg *config.Config) []string) {
	procMu.Lock()
	defer procMu.Unlock()
	sources = append(sources, f)
}

// track makes l one of the loggers RememberSecret reaches and gives it the
// secrets this process already knows.
func (l *Logger) track() {
	procMu.Lock()
	openLoggers[l] = true
	known := make([]string, 0, len(procSecrets))
	for v := range procSecrets {
		known = append(known, v)
	}
	procMu.Unlock()
	l.red.addConfigured(known...)
}

func (l *Logger) untrack() {
	procMu.Lock()
	delete(openLoggers, l)
	procMu.Unlock()
}

// refreshSecrets asks every secret source for this installation's values.
func (l *Logger) refreshSecrets() {
	if l.cfg == nil {
		return
	}
	procMu.Lock()
	srcs := append([]func(*config.Config) []string(nil), sources...)
	procMu.Unlock()
	for _, f := range srcs {
		RememberSecret(f(l.cfg)...)
	}
}

// loggerFor is the logger of the operation ctx belongs to, or this process's
// only open logger (a request outside any operation).
func loggerFor(ctx context.Context) *Logger {
	if sc := ScopeOf(ctx); sc.rec != nil {
		return sc.rec.l
	}
	procMu.Lock()
	defer procMu.Unlock()
	if len(openLoggers) != 1 {
		return nil
	}
	for l := range openLoggers {
		return l
	}
	return nil
}

// secretInputHint cheaply rules out inputs without secret-named fields.
var secretInputHint = []string{"key", "secret", "token", "password", "authorization", "cookie", "credential"}

// SecretInputs returns the values of secret-named fields in an operation's
// JSON input (an API key being saved or tested, a password): they are hidden
// in everything this process logs afterwards — the call's own errors and the
// requests it makes included. Short values are left out (they would hide
// ordinary words).
func SecretInputs(raw json.RawMessage) []string {
	if len(raw) < 8 {
		return nil
	}
	low := strings.ToLower(string(raw))
	hit := false
	for _, h := range secretInputHint {
		if strings.Contains(low, h) {
			hit = true
			break
		}
	}
	if !hit {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	var out []string
	var walk func(v any, secret bool, depth int)
	walk = func(v any, secret bool, depth int) {
		if depth > 8 {
			return
		}
		switch x := v.(type) {
		case string:
			if secret && len(strings.TrimSpace(x)) >= minInputSecretLen {
				out = append(out, x)
			}
		case map[string]any:
			for k, val := range x {
				walk(val, secret || SecretName(k), depth+1)
			}
		case []any:
			for _, val := range x {
				walk(val, secret, depth+1)
			}
		}
	}
	walk(v, false, 0)
	return out
}

// minInputSecretLen is the shortest input value treated as a secret.
const minInputSecretLen = 12
