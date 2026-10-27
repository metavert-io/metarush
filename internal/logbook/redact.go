package logbook

import (
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/metavert-io/metarush/internal/config"
)

// Redacted replaces every hidden value.
const Redacted = "[redacted]"

// Minimum lengths of exact secret values: shorter configured secrets would turn
// ordinary words into redactions; environment values need to look like keys.
const (
	minSecretLen    = 4
	minEnvSecretLen = 8
)

// Redactor hides secrets in everything the log writes: the exact values of
// configured secrets (database password, storage keys, provider API keys, the
// confirm-token key, dashboard cookies and sign-in nonces), well-known credential
// patterns (URL passwords, AWS and Tigris keys, API keys, bearer tokens,
// private-key blocks, JWTs) and values of fields whose names say they are
// secret (key, secret, token, password, authorization, cookie). It is safe for
// concurrent use.
type Redactor struct {
	mu       sync.RWMutex
	values   map[string]bool
	config   map[string]bool // configured secrets (the hook gets their fingerprints)
	replacer *strings.Replacer
}

// NewRedactor returns a redactor that knows no exact values yet (patterns and
// field names still apply).
func NewRedactor() *Redactor { return &Redactor{values: map[string]bool{}, config: map[string]bool{}} }

// Add registers exact secret values (and their URL-escaped spellings where they
// differ). Values shorter than four characters are ignored.
func (r *Redactor) Add(values ...string) {
	r.add(minSecretLen, false, values...)
}

// addConfigured registers secrets from the installation's configuration.
func (r *Redactor) addConfigured(values ...string) {
	r.add(minSecretLen, true, values...)
}

func (r *Redactor) add(min int, configured bool, values ...string) {
	if r == nil {
		return
	}
	var fresh []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		if len(v) < min {
			continue
		}
		for _, form := range []string{v, url.QueryEscape(v), url.PathEscape(v)} {
			fresh = append(fresh, form)
		}
	}
	if len(fresh) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	changed := false
	for _, v := range fresh {
		if configured {
			r.config[v] = true
		}
		if !r.values[v] {
			r.values[v] = true
			changed = true
		}
	}
	if changed {
		r.replacer = nil
	}
}

// AddBytes registers a binary secret (such as an HMAC key) in the spellings it
// can appear in as text: hex and base64.
func (r *Redactor) AddBytes(b []byte) {
	if len(b) == 0 {
		return
	}
	r.addConfigured(hex.EncodeToString(b), base64.StdEncoding.EncodeToString(b), base64.RawStdEncoding.EncodeToString(b),
		base64.URLEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(b))
	if isPrintable(string(b)) {
		r.addConfigured(string(b))
	}
}

// configured returns the registered configured secrets.
func (r *Redactor) configured() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.config))
	for v := range r.config {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func isPrintable(s string) bool {
	for _, c := range s {
		if !unicode.IsPrint(c) {
			return false
		}
	}
	return true
}

// Values returns the registered exact values, longest first (for scans in tests
// and for the bundle's final check).
func (r *Redactor) Values() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.values))
	for v := range r.values {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}

func (r *Redactor) exact() *strings.Replacer {
	r.mu.RLock()
	rep := r.replacer
	r.mu.RUnlock()
	if rep != nil {
		return rep
	}
	vals := r.Values()
	if len(vals) == 0 {
		return nil
	}
	pairs := make([]string, 0, 2*len(vals))
	for _, v := range vals { // longest first, so a value inside a longer one cannot leave a tail
		pairs = append(pairs, v, Redacted)
	}
	rep = strings.NewReplacer(pairs...)
	r.mu.Lock()
	r.replacer = rep
	r.mu.Unlock()
	return rep
}

type pattern struct {
	re   *regexp.Regexp
	repl string
}

// patterns are credential shapes hidden wherever they appear.
var patterns = []pattern{
	// Passwords in URLs: mongodb(+srv)://user:password@host, https://user:pass@…
	{regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]{1,20}://[^\s:/@'"<>]+):([^\s@'"<>/]+)@`), "${1}:" + Redacted + "@"},
	// Private-key blocks (PEM), even when the end line is missing.
	{regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|\z)`), Redacted},
	// AWS access key IDs.
	{regexp.MustCompile(`\b(?:A3T[A-Z0-9]|AKIA|ASIA|AGPA|AIDA|AROA|AIPA|ANPA|ANVA|APKA)[A-Z0-9]{16}\b`), Redacted},
	// Tigris access keys and secrets.
	{regexp.MustCompile(`\bt(?:id|sec)_[A-Za-z0-9_+/=-]{8,}`), Redacted},
	// ElevenLabs keys (sk_…) and similar underscore-style API keys.
	{regexp.MustCompile(`\bsk_[A-Za-z0-9]{24,}\b`), Redacted},
	// Anthropic, OpenAI and other sk-… keys.
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), Redacted},
	// GitHub, Slack and Google API tokens.
	{regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,})`), Redacted},
	{regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`), Redacted},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`), Redacted},
	// JSON Web Tokens.
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), Redacted},
	// Bearer and Basic authorization values.
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "${1} " + Redacted},
	// name: value / name=value where the name says it is secret.
	{regexp.MustCompile(`(?i)\b((?:x-)?(?:api[_-]?key|access[_-]?key(?:[_-]?id)?|secret[_-]?access[_-]?key|client[_-]?secret|secret|password|passwd|passphrase|pwd|token|access[_-]?token|refresh[_-]?token|auth[_-]?token|authorization|set-cookie|cookie))(["']?[ \t]*[:=][ \t]*["']?)([^\s"',;&]+)`), "${1}${2}" + Redacted},
	// The dashboard's session cookie and one-time sign-in links.
	{regexp.MustCompile(`\bmr_session=[^\s;"']+`), "mr_session=" + Redacted},
	{regexp.MustCompile(`(/auth\?t=)[^\s&"']+`), "${1}" + Redacted},
	// Signed object-store URLs.
	{regexp.MustCompile(`(?i)([?&](?:X-Amz-Signature|X-Amz-Credential|X-Amz-Security-Token|X-Goog-Signature|X-Goog-Credential|Signature|sig)=)[^&\s"']+`), "${1}" + Redacted},
}

// String hides secrets in a text.
func (r *Redactor) String(s string) string {
	if s == "" {
		return s
	}
	if r != nil {
		if rep := r.exact(); rep != nil {
			s = rep.Replace(s)
		}
	}
	for _, p := range patterns {
		if p.re.MatchString(s) {
			s = p.re.ReplaceAllString(s, p.repl)
		}
	}
	return s
}

// Args hides secrets in a command line: values after secret-looking flags
// (--password x, --token=x), NAME=value pairs with secret-looking names, and
// anything String hides.
func (r *Redactor) Args(args []string) []string {
	out := make([]string, len(args))
	hideNext := false
	for i, a := range args {
		if hideNext {
			out[i], hideNext = Redacted, false
			continue
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			name := strings.TrimLeft(a, "-")
			if k, _, ok := strings.Cut(name, "="); ok {
				if SecretName(k) {
					out[i] = a[:len(a)-len(name)] + k + "=" + Redacted
					continue
				}
			} else if SecretName(name) {
				out[i], hideNext = a, true
				continue
			}
		} else if k, _, ok := strings.Cut(a, "="); ok && envName.MatchString(k) && SecretName(k) {
			out[i] = k + "=" + Redacted
			continue
		}
		out[i] = r.String(a)
	}
	return out
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// secretWords make a field secret wherever they appear in its name.
var secretWords = map[string]bool{"secret": true, "secrets": true, "password": true, "passwd": true, "passphrase": true, "pwd": true,
	"authorization": true, "cookie": true, "cookies": true, "credential": true, "credentials": true, "token": true, "apikey": true,
	"privatekey": true, "nonce": true, "jwt": true, "bearer": true}

// publicKeys are "…key" names that are identifiers, not secrets.
var publicKeys = map[string]bool{"storage": true, "group": true, "idempotency": true, "object": true, "cache": true, "sort": true,
	"primary": true, "partition": true, "dedupe": true, "lookup": true, "map": true, "event": true, "label": true, "hot": true,
	"content": true, "media": true, "item": true, "row": true, "field": true, "query": true, "index": true, "public": true, "s3": true,
	"name": true, "file": true, "body": true, "raw": true, "react": true, "short": true, "foreign": true, "composite": true, "cut": true}

// SecretName reports whether a field or flag name says its value is secret:
// it contains secret, token, password, authorization, cookie or credential, or
// it is a key that is not an identifier (apiKey, secretAccessKey, key — but
// not storageKey, groupKey or idempotencyKey).
func SecretName(name string) bool {
	words := splitWords(name)
	if len(words) == 0 {
		return false
	}
	for _, w := range words {
		if secretWords[w] {
			return true
		}
	}
	last := words[len(words)-1]
	if last != "key" && last != "keys" {
		return false
	}
	if len(words) == 1 {
		return true
	}
	return !publicKeys[words[len(words)-2]]
}

// splitWords splits camelCase, snake_case, kebab-case and dotted names into
// lowercase words ("secretAccessKey" → secret, access, key).
func splitWords(name string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(name)
	for i, c := range rs {
		switch {
		case c == '_' || c == '-' || c == '.' || c == ' ' || c == '/':
			flush()
		case unicode.IsUpper(c):
			// A new word starts at an upper-case letter that follows a lower-case
			// letter or digit, or that starts "Key" in "APIKey".
			if len(cur) > 0 && (unicode.IsLower(cur[len(cur)-1]) || unicode.IsDigit(cur[len(cur)-1]) ||
				(i+1 < len(rs) && unicode.IsLower(rs[i+1]) && unicode.IsUpper(cur[len(cur)-1]))) {
				flush()
			}
			cur = append(cur, c)
		default:
			cur = append(cur, c)
		}
	}
	flush()
	return words
}

// Value hides secrets in decoded JSON (maps, lists and strings): values of
// secret-named keys are replaced whole, every string is passed through String.
func (r *Redactor) Value(v any) any {
	switch x := v.(type) {
	case string:
		return r.String(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if SecretName(k) && val != nil {
				out[k] = Redacted
				continue
			}
			out[k] = r.Value(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = r.Value(val)
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if SecretName(k) && val != "" {
				out[k] = Redacted
				continue
			}
			out[k] = r.String(val)
		}
		return out
	}
	return v
}

// secretEnvWords are environment-variable name words that mark a secret.
var secretEnvWords = map[string]bool{"KEY": true, "KEYS": true, "SECRET": true, "SECRETS": true, "TOKEN": true, "PASSWORD": true,
	"PASSWD": true, "PASS": true, "PASSPHRASE": true, "CREDENTIAL": true, "CREDENTIALS": true, "COOKIE": true, "AUTH": true,
	"APIKEY": true, "PAT": true, "PRIVATE": true}

// plainEnvEnds are last words of variable names that hold identifiers or
// locations, never secrets (…_KEY_ID, …_TOKEN_FILE, SSH_AUTH_SOCK).
var plainEnvEnds = map[string]bool{"ID": true, "PATH": true, "DIR": true, "FILE": true, "SOCK": true, "HOST": true, "PORT": true,
	"NAME": true, "USER": true, "EMAIL": true, "MODE": true, "TYPE": true, "HELPER": true, "CMD": true, "COMMAND": true, "URL": true}

// SecretEnvName reports whether an environment variable's name says it holds a secret.
func SecretEnvName(k string) bool {
	words := strings.Split(strings.ToUpper(k), "_")
	if plainEnvEnds[words[len(words)-1]] {
		return false
	}
	for _, w := range words {
		if secretEnvWords[w] {
			return true
		}
	}
	return false
}

// FromEnvironment registers secrets found in this process's environment:
// values of variables whose names look secret (…_KEY, …SECRET…, …TOKEN…,
// …PASSWORD…) and passwords inside URL values (database URIs).
func (r *Redactor) FromEnvironment() {
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || v == "" {
			continue
		}
		if pw := urlPassword(v); pw != "" {
			r.Add(pw)
		}
		if SecretEnvName(k) {
			r.add(minEnvSecretLen, false, v)
		}
	}
}

// FromConfig registers the configured secrets of an installation: the
// database password, storage keys, the confirm-token key and the dashboard's
// pending sign-in nonces.
func (r *Redactor) FromConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	if pw := urlPassword(cfg.Database.URI); pw != "" {
		r.addConfigured(pw)
	}
	r.addConfigured(cfg.Media.S3.SecretAccessKey, cfg.Media.S3.AccessKeyID)
	state := cfg.StateDir()
	if b, err := os.ReadFile(filepath.Join(state, "confirm.key")); err == nil {
		r.AddBytes(b)
	}
	if ents, err := os.ReadDir(filepath.Join(state, "nonces")); err == nil {
		for _, e := range ents {
			r.Add(e.Name())
		}
	}
}

func urlPassword(v string) string {
	if !strings.Contains(v, "://") || !strings.Contains(v, "@") {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(v))
	if err != nil || u.User == nil {
		return ""
	}
	pw, _ := u.User.Password()
	return pw
}
