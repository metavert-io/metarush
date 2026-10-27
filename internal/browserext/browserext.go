// Package browserext pairs the "Send to MetaRush" browser extension with this
// machine. A person asks for a one-time code (Settings → Browser extension, or
// `metarush extension pair`), types it into the extension, and the extension
// gets a token that can only add footage: start uploads, send their bytes and
// ask for a link import. It can never read anything back — not clips, projects,
// jobs or the activity — and it is bound to the extension origin that paired.
//
// Codes and tokens are stored hashed in <home>/state/browser-extension.json
// (0600), per machine; revoking a browser deletes its token at once.
//
// A pairing belongs to the person who made it (D197): anyone signed in pairs
// their own browser with a code of their own (several people's codes can wait
// at once), what that browser sends is theirs, and it stops working while
// their account is turned off or removed. Pairings from before belong to the
// person this installation was set up for (its owner): the file is moved over
// the first time it is read with that person known (version 2).
package browserext

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/ids"
)

// CodeTTL is how long a pairing code works.
const CodeTTL = 10 * time.Minute

// maxCodeTries burns a code after this many wrong attempts.
const maxCodeTries = 5

// IDPrefix prefixes paired-browser IDs (ext_…).
const IDPrefix = "ext"

// TokenPrefix marks extension tokens (mrx_ + 64 hex), so the activity log's
// redaction and the secret scan recognize them.
const TokenPrefix = "mrx_"

// Token scopes: what a paired browser may send. Footage (start uploads, send
// their bytes, ask for link imports) is what every pairing could always do;
// pages (send the page on screen as a web page snapshot in the resources
// library, D105) came later, so a browser paired before it pairs again.
// Neither scope reads anything back.
const (
	ScopeFootage = "footage"
	ScopePages   = "pages"
)

// newScopes are what a pairing gets now.
var newScopes = []string{ScopeFootage, ScopePages}

// Browser is a paired browser as people see it (never its token).
type Browser struct {
	ID         string     `json:"id"`
	Name       string     `json:"name" jsonschema:"description=The browser the extension said it runs in (Chrome\\, Brave\\, Edge…)."`
	Origin     string     `json:"origin" jsonschema:"description=The extension's address (chrome-extension://…); the token works only from there."`
	Version    string     `json:"version,omitempty" jsonschema:"description=The extension's version when it paired."`
	PairedAt   time.Time  `json:"pairedAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	Sent       int        `json:"sent" jsonschema:"description=Videos it has sent to MetaRush."`
	// Pages counts the web pages it has sent (Send this page).
	Pages int `json:"pages,omitempty"`
	// Scopes are what it may send: footage, and pages for browsers paired since
	// "Send this page" (empty: a browser paired before scopes, footage only).
	Scopes []string `json:"scopes,omitempty"`
	// UserID is the person who paired it (D197): what it sends is theirs, and
	// it works only while their account is on. UserName is their name (when
	// it paired; lists show the name they have now).
	UserID   string `json:"userId,omitempty"`
	UserName string `json:"userName,omitempty"`
}

// Person is who a pairing (or a waiting code) belongs to.
type Person struct {
	ID, Name string
}

// Can reports whether the browser's pairing allows a scope.
func (b Browser) Can(scope string) bool {
	if len(b.Scopes) == 0 {
		return scope == ScopeFootage // paired before scopes
	}
	for _, s := range b.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

type record struct {
	Browser
	TokenHash string `json:"tokenHash"`
}

type code struct {
	Hash      string    `json:"hash"`
	ExpiresAt time.Time `json:"expiresAt"`
	Tries     int       `json:"tries"`
	// UserID and UserName: whose code it is (the pairing it makes is theirs).
	UserID   string `json:"userId,omitempty"`
	UserName string `json:"userName,omitempty"`
}

// stateVersion 2: every pairing and code has its person (D197).
const stateVersion = 2

type state struct {
	Version  int      `json:"version,omitempty"`
	Browsers []record `json:"browsers"`
	// Codes are the waiting codes, one per person.
	Codes []code `json:"codes,omitempty"`
	// Code is the one waiting code of version 1 (moved into Codes).
	Code *code `json:"code,omitempty"`
}

// Store is this installation's pairings.
type Store struct {
	path string
	mu   sync.Mutex
	// owner is who pairings from before people had pairings belong to: the
	// person this installation was set up for (see Owned).
	owner Person
}

var (
	regMu    sync.Mutex
	registry = map[string]*Store{}
)

// For returns the shared store for a state directory.
func For(stateDir string) *Store {
	p := filepath.Join(stateDir, "browser-extension.json")
	regMu.Lock()
	defer regMu.Unlock()
	if s, ok := registry[p]; ok {
		return s
	}
	s := &Store{path: p}
	registry[p] = s
	return s
}

// Owned names who pairings from before people had pairings belong to (the
// installation's owner) and returns the store; the file moves over to
// version 2 the next time it is read.
func (s *Store) Owned(owner Person) *Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner.ID != "" {
		s.owner = owner
	}
	return s
}

// load reads the file (the caller holds s.mu), moving an older one over:
// pairings and the waiting code without a person become the owner's.
func (s *Store) load() state {
	var st state
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.Version < stateVersion && s.owner.ID != "" {
		for i := range st.Browsers {
			if st.Browsers[i].UserID == "" {
				st.Browsers[i].UserID, st.Browsers[i].UserName = s.owner.ID, s.owner.Name
			}
		}
		if st.Code != nil {
			c := *st.Code
			c.UserID, c.UserName = s.owner.ID, s.owner.Name
			st.Codes = append(st.Codes, c)
			st.Code = nil
		}
		st.Version = stateVersion
		if _, err := os.Stat(s.path); err == nil {
			_ = s.save(st)
		}
	}
	return st
}

// whose is a pairing's person (one from before people had pairings, read
// before the owner was known: the owner's).
func (s *Store) whose(id string) string {
	if id == "" {
		return s.owner.ID
	}
	return id
}

func (s *Store) save(st state) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func hash(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }

// codeAlphabet has no look-alikes (no 0/O, 1/I/L, U).
const codeAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"

// NewCode replaces the person's waiting code with a fresh one ("ABCD-EFGH",
// about 39 bits) that works once, for CodeTTL; the browser it pairs is theirs.
func (s *Store) NewCode(now time.Time, who Person) (string, time.Time, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	var c strings.Builder
	for i, x := range b {
		if i == 4 {
			c.WriteByte('-')
		}
		c.WriteByte(codeAlphabet[int(x)%len(codeAlphabet)])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.load()
	exp := now.Add(CodeTTL)
	who.ID = s.whose(who.ID)
	keep := st.Codes[:0]
	for _, x := range st.Codes {
		if s.whose(x.UserID) != who.ID && now.Before(x.ExpiresAt) {
			keep = append(keep, x) // other people's codes still waiting
		}
	}
	st.Codes = append(keep, code{Hash: hash(normalizeCode(c.String())), ExpiresAt: exp, UserID: who.ID, UserName: who.Name})
	return c.String(), exp, s.save(st)
}

func normalizeCode(c string) string {
	return strings.NewReplacer("-", "", " ", "").Replace(strings.ToUpper(strings.TrimSpace(c)))
}

// Pending reports whether the person's code is waiting to be used, and until when.
func (s *Store) Pending(now time.Time, userID string) (bool, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.load()
	userID = s.whose(userID)
	for _, c := range st.Codes {
		if s.whose(c.UserID) == userID && now.Before(c.ExpiresAt) {
			return true, c.ExpiresAt
		}
	}
	return false, time.Time{}
}

// Pairing refusals (plain words; the API maps them to codes).
var (
	ErrNoCode      = errors.New("There's no pairing code waiting. Make one in MetaRush: Settings → Browser extension.")
	ErrBadCode     = errors.New("That code isn't right.")
	ErrCodeExpired = errors.New("That code has expired. Make a new one in MetaRush: Settings → Browser extension.")
	ErrBadOrigin   = errors.New("Only the MetaRush browser extension can pair.")
	ErrBadToken    = errors.New("This browser isn't paired with MetaRush any more.")
)

var originRe = regexp.MustCompile(`^(chrome-extension://[a-p]{32}|moz-extension://[0-9a-f-]{36}|safari-web-extension://[0-9A-Fa-f-]{36})$`)

// ExtensionOrigin reports whether origin is a browser extension's own address
// (web pages can't send one).
func ExtensionOrigin(origin string) bool { return originRe.MatchString(origin) }

// Pair exchanges a code for a token bound to the extension origin; the
// pairing is the code's person's. The token is returned once and stored only
// as a hash. A wrong code counts against every waiting code (each burns after
// maxCodeTries wrong tries made while it waited).
func (s *Store) Pair(codeText, origin, name, version string, now time.Time) (string, Browser, error) {
	if !ExtensionOrigin(origin) {
		return "", Browser{}, ErrBadOrigin
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.load()
	if len(st.Codes) == 0 {
		return "", Browser{}, ErrNoCode
	}
	h := hash(normalizeCode(codeText))
	match := -1
	for i, c := range st.Codes {
		if subtle.ConstantTimeCompare([]byte(h), []byte(c.Hash)) == 1 {
			match = i
		}
	}
	if match < 0 {
		keep := st.Codes[:0]
		for _, c := range st.Codes {
			c.Tries++
			if c.Tries < maxCodeTries && now.Before(c.ExpiresAt) {
				keep = append(keep, c) // burned: too many wrong guesses (or expired)
			}
		}
		st.Codes = keep
		_ = s.save(st)
		return "", Browser{}, ErrBadCode
	}
	used := st.Codes[match]
	st.Codes = append(st.Codes[:match], st.Codes[match+1:]...) // one-time
	if now.After(used.ExpiresAt) {
		_ = s.save(st)
		return "", Browser{}, ErrCodeExpired
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", Browser{}, err
	}
	tok := TokenPrefix + hex.EncodeToString(raw)
	b := Browser{ID: ids.New(IDPrefix), Name: clip(strings.TrimSpace(name), 40), Origin: origin, Version: clip(version, 20), PairedAt: now.UTC(),
		Scopes: append([]string(nil), newScopes...), UserID: s.whose(used.UserID), UserName: used.UserName}
	if b.UserName == "" && b.UserID == s.owner.ID {
		b.UserName = s.owner.Name
	}
	if b.Name == "" {
		b.Name = "A browser"
	}
	// One pairing per extension origin: pairing again replaces the old token.
	keep := st.Browsers[:0]
	for _, r := range st.Browsers {
		if r.Origin != origin {
			keep = append(keep, r)
		}
	}
	st.Browsers = append(keep, record{Browser: b, TokenHash: hash(tok)})
	return tok, b, s.save(st)
}

// Verify checks a token and the origin it arrives from, and records the use.
func (s *Store) Verify(token, origin string, now time.Time) (Browser, error) {
	if !strings.HasPrefix(token, TokenPrefix) || len(token) != len(TokenPrefix)+64 {
		return Browser{}, ErrBadToken
	}
	h := hash(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.load()
	for i, r := range st.Browsers {
		if subtle.ConstantTimeCompare([]byte(h), []byte(r.TokenHash)) == 1 {
			if r.Origin != origin {
				return Browser{}, ErrBadOrigin
			}
			t := now.UTC()
			if r.LastUsedAt == nil || t.Sub(*r.LastUsedAt) > time.Minute {
				st.Browsers[i].LastUsedAt = &t
				_ = s.save(st)
			}
			b := st.Browsers[i].Browser
			b.UserID = s.whose(b.UserID)
			return b, nil
		}
	}
	return Browser{}, ErrBadToken
}

// Sent counts a video a browser sent.
func (s *Store) Sent(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.load()
	for i := range st.Browsers {
		if st.Browsers[i].ID == id {
			st.Browsers[i].Sent++
			_ = s.save(st)
			return
		}
	}
}

// SentPage counts a web page a browser sent.
func (s *Store) SentPage(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.load()
	for i := range st.Browsers {
		if st.Browsers[i].ID == id {
			st.Browsers[i].Pages++
			_ = s.save(st)
			return
		}
	}
}

// List returns the paired browsers, everyone's.
func (s *Store) List() []Browser {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Browser{}
	for _, r := range s.load().Browsers {
		b := r.Browser
		b.UserID = s.whose(b.UserID)
		out = append(out, b)
	}
	return out
}

// ListFor returns one person's paired browsers.
func (s *Store) ListFor(userID string) []Browser {
	out := []Browser{}
	for _, b := range s.List() {
		if b.UserID == s.whose(userID) {
			out = append(out, b)
		}
	}
	return out
}

// Get returns a paired browser.
func (s *Store) Get(id string) (Browser, bool) {
	for _, b := range s.List() {
		if b.ID == id {
			return b, true
		}
	}
	return Browser{}, false
}

// Revoke unpairs one browser (id) or all of them (id == ""), and cancels
// waiting codes (all of them with id == ""). It returns how many were unpaired.
func (s *Store) Revoke(id string) (int, error) {
	return s.RevokeWhere(func(b Browser) bool { return id == "" || b.ID == id }, id == "")
}

// RevokeFor unpairs every browser a person paired, and cancels their waiting code.
func (s *Store) RevokeFor(userID string) (int, error) {
	userID = s.whose(userID)
	s.mu.Lock()
	st := s.load()
	keep := st.Codes[:0]
	for _, c := range st.Codes {
		if s.whose(c.UserID) != userID {
			keep = append(keep, c)
		}
	}
	st.Codes = keep
	err := s.save(st)
	s.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return s.RevokeWhere(func(b Browser) bool { return b.UserID == userID }, false)
}

// RevokeWhere unpairs the browsers that match (and every waiting code, with allCodes).
func (s *Store) RevokeWhere(match func(Browser) bool, allCodes bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.load()
	n := 0
	keep := st.Browsers[:0]
	for _, r := range st.Browsers {
		b := r.Browser
		b.UserID = s.whose(b.UserID)
		if match(b) {
			n++
			continue
		}
		keep = append(keep, r)
	}
	st.Browsers = keep
	if allCodes {
		st.Codes, st.Code = nil, nil
	}
	return n, s.save(st)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
