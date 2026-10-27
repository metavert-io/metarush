package logbook

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
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

var keyLookup func(cfg *config.Config, name string) string

// SetKeyLookup tells the log how to read a key this computer keeps by its
// name (the providers package: the keychain or the home's secrets file), for
// KnownSecrets' named keys.
func SetKeyLookup(f func(cfg *config.Config, name string) string) {
	procMu.Lock()
	defer procMu.Unlock()
	keyLookup = f
}

// KnownSecrets lists the exact secrets this computer knows for an
// installation, to take them out of what leaves it (an archive of the
// workspace): its configured ones (the database's password, the storage's
// secret key, the confirm-token key), what every secret source lists
// (providers' keys), the keys named (an ability's), every secret-named value
// in the home's secrets file and in this process's environment, and every key
// this process has read. Values shorter than eight characters are left out:
// as exact text they would match ordinary words.
func KnownSecrets(cfg *config.Config, keyNames ...string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(vs ...string) {
		for _, v := range vs {
			v = strings.TrimSpace(v)
			if len(v) < minEnvSecretLen || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	procMu.Lock()
	for v := range procSecrets {
		add(v)
	}
	srcs := append([]func(*config.Config) []string(nil), sources...)
	lookup := keyLookup
	procMu.Unlock()
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if pw := urlPassword(v); pw != "" {
			add(pw)
		}
		if SecretEnvName(k) {
			add(v)
		}
	}
	if cfg == nil {
		return out
	}
	add(urlPassword(cfg.Database.URI), cfg.Media.S3.SecretAccessKey)
	if b, err := os.ReadFile(filepath.Join(cfg.StateDir(), "confirm.key")); err == nil && len(b) > 0 {
		add(hex.EncodeToString(b), base64.StdEncoding.EncodeToString(b), base64.RawStdEncoding.EncodeToString(b),
			base64.URLEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(b))
	}
	if cfg.Home() != "" {
		if b, err := os.ReadFile(config.DotEnvPath(cfg.Home())); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				k, v, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(l), "export "), "=")
				if !ok {
					continue
				}
				v = strings.Trim(strings.TrimSpace(v), `"'`)
				if pw := urlPassword(v); pw != "" {
					add(pw)
				}
				if SecretEnvName(strings.TrimSpace(k)) {
					add(v)
				}
			}
		}
	}
	for _, f := range srcs {
		add(f(cfg)...)
	}
	if lookup != nil {
		for _, n := range keyNames {
			add(lookup(cfg, n))
		}
	}
	return out
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
