package sso

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Sign-ins in progress on this computer (D428). A sign-in starts as a ticket
// (the address a browser opens: /auth/sso/begin?t=…, from the sign-in page,
// an invite link, or a test an owner starts in Settings or a terminal); the
// browser that opens it gets the sign-in's state in a cookie and goes to the
// provider; the provider sends it back to the callback with that state. Each
// is a file under <home>/state/sso (0600, the folder 0700) named by a hash, so
// the state and ticket themselves are never on disk; a ticket works once
// within TicketLife, a sign-in once within FlowLife, claimed by renaming its
// file (a second claim, the same answer replayed, finds nothing). Only this
// computer ever reads them: the callback comes back to the address the
// sign-in began at. Their times are this computer's clock.

// Lifetimes of a ticket and of a sign-in waiting for the provider's answer.
const (
	TicketLife = 2 * time.Minute
	FlowLife   = 10 * time.Minute
	maxFlows   = 500 // waiting sign-ins kept at most (older ones go first)
)

// Purposes of a sign-in.
const (
	ForSignIn = "signin" // the sign-in page
	ForInvite = "invite" // joining with an invite link
	ForTest   = "test"   // an owner's test of a setup
)

// Flow is one sign-in in progress.
type Flow struct {
	Purpose string `json:"purpose"`
	// Public: begun on the address for other computers; Host is the Host the
	// browser used, and Redirect the callback address registered for it.
	Public   bool   `json:"public"`
	Host     string `json:"host"`
	Redirect string `json:"redirect"`
	// Next is where the browser goes after signing in (a path on this site).
	Next string `json:"next,omitempty"`
	// Setup is the fingerprint of the sign-in setup it uses (a test: the
	// setup being tested; it must still be the same when the answer comes).
	Setup string `json:"setup"`
	// UserID is the owner who asked for a test; Invite is the hash of the
	// invite link a person is joining with.
	UserID string `json:"userId,omitempty"`
	Invite string `json:"invite,omitempty"`
	// Hint is the email the provider's page suggests.
	Hint string `json:"hint,omitempty"`
	// Set when the browser begins it: what goes to the provider.
	Nonce    string    `json:"nonce,omitempty"`
	Verifier string    `json:"verifier,omitempty"`
	Created  time.Time `json:"created"`
	Begun    time.Time `json:"begun,omitzero"`
}

// Store keeps the sign-ins of one installation.
type Store struct {
	dir string
	mu  sync.Mutex
}

// FlowsIn is the store under a state folder.
func FlowsIn(stateDir string) *Store { return &Store{dir: filepath.Join(stateDir, "sso")} }

// Dir is where the store keeps its files.
func (s *Store) Dir() string { return s.dir }

// Random returns n random bytes as base64url (no padding).
func Random(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the system's randomness failed: nothing secure can go on
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Challenge is the PKCE S256 challenge of a verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Errors claiming a ticket or a sign-in.
var (
	ErrGone    = errors.New("gone") // used, ran out, or never was
	ErrExpired = errors.New("expired")
)

func (s *Store) prepare() error {
	for _, d := range []string{s.dir, filepath.Join(s.dir, "tickets"), filepath.Join(s.dir, "flows")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
		_ = os.Chmod(d, 0o700)
	}
	return nil
}

func writePrivate(p string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := p + ".tmp-" + Random(6)
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// NewTicket keeps a sign-in for the browser that opens its ticket, and
// returns the ticket (shown once, in the address).
func (s *Store) NewTicket(f Flow) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.prepare(); err != nil {
		return "", err
	}
	s.sweep()
	ticket := Random(32)
	f.Created = time.Now().UTC()
	if err := writePrivate(filepath.Join(s.dir, "tickets", hashOf(ticket)+".json"), f); err != nil {
		return "", err
	}
	return ticket, nil
}

// claim takes a file once: renamed out of the way first, so a second claim
// (a ticket opened twice, an answer replayed) finds nothing.
func claim(p string) (*Flow, error) {
	taken := p + ".taken-" + Random(6)
	if err := os.Rename(p, taken); err != nil {
		return nil, ErrGone
	}
	defer os.Remove(taken)
	b, err := os.ReadFile(taken)
	if err != nil {
		return nil, ErrGone
	}
	var f Flow
	if json.Unmarshal(b, &f) != nil {
		return nil, ErrGone
	}
	return &f, nil
}

// Begin takes a ticket and starts its sign-in: a new state, nonce and PKCE
// verifier. It returns the sign-in and its state (the cookie and the
// provider's state parameter).
func (s *Store) Begin(ticket string) (*Flow, string, error) {
	if len(ticket) < 20 || len(ticket) > 100 {
		return nil, "", ErrGone
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := claim(filepath.Join(s.dir, "tickets", hashOf(ticket)+".json"))
	if err != nil {
		return nil, "", err
	}
	if time.Since(f.Created) > TicketLife {
		return nil, "", ErrExpired
	}
	state := Random(32)
	f.Nonce, f.Verifier, f.Begun = Random(32), Random(48), time.Now().UTC()
	if err := s.prepare(); err != nil {
		return nil, "", err
	}
	if err := writePrivate(filepath.Join(s.dir, "flows", hashOf(state)+".json"), f); err != nil {
		return nil, "", err
	}
	return f, state, nil
}

// Finish takes the sign-in a state belongs to, once.
func (s *Store) Finish(state string) (*Flow, error) {
	if len(state) < 20 || len(state) > 100 || strings.ContainsAny(state, "/\\.") {
		return nil, ErrGone
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := claim(filepath.Join(s.dir, "flows", hashOf(state)+".json"))
	if err != nil {
		return nil, err
	}
	if time.Since(f.Begun) > FlowLife {
		return nil, ErrExpired
	}
	return f, nil
}

// sweep removes tickets and sign-ins past their time, and the oldest beyond
// maxFlows (someone starting sign-ins without finishing them).
func (s *Store) sweep() {
	type entry struct {
		p string
		t time.Time
	}
	var all []entry
	for _, sub := range []string{"tickets", "flows"} {
		es, _ := os.ReadDir(filepath.Join(s.dir, sub))
		for _, e := range es {
			info, err := e.Info()
			if err != nil {
				continue
			}
			p := filepath.Join(s.dir, sub, e.Name())
			if time.Since(info.ModTime()) > FlowLife+TicketLife {
				_ = os.Remove(p)
				continue
			}
			all = append(all, entry{p, info.ModTime()})
		}
	}
	for len(all) > maxFlows {
		oldest := 0
		for i := range all {
			if all[i].t.Before(all[oldest].t) {
				oldest = i
			}
		}
		_ = os.Remove(all[oldest].p)
		all = append(all[:oldest], all[oldest+1:]...)
	}
}

/* ---------- Test sign-ins ---------- */

// TestResult is the last test sign-in on this computer (state/sso/test.json):
// whether it worked, for which setup, by which owner, and who signed in.
type TestResult struct {
	Setup    string    `json:"setup"`
	UserID   string    `json:"userId"`
	OK       bool      `json:"ok"`
	Reason   string    `json:"reason,omitempty"`
	Message  string    `json:"message,omitempty"`
	Identity *Identity `json:"identity,omitempty"`
	At       time.Time `json:"at"`
}

// TestLife is how long a test sign-in lets an owner switch to its setup.
const TestLife = 30 * time.Minute

// SaveTest records a test sign-in's result.
func (s *Store) SaveTest(r TestResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.prepare(); err != nil {
		return err
	}
	r.At = time.Now().UTC()
	return writePrivate(filepath.Join(s.dir, "test.json"), r)
}

// LastTest is the last test sign-in on this computer (nil: none).
func (s *Store) LastTest() *TestResult {
	b, err := os.ReadFile(filepath.Join(s.dir, "test.json"))
	if err != nil {
		return nil
	}
	var r TestResult
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

// Fresh reports a test that still lets its owner switch to its setup.
func (r *TestResult) Fresh() bool { return r != nil && time.Since(r.At) <= TestLife }

// ClearTest forgets the last test (the setup it tested changed).
func (s *Store) ClearTest() { _ = os.Remove(filepath.Join(s.dir, "test.json")) }
