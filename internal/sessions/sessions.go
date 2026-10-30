// Package sessions keeps the dashboard's signed-in browsers: one record per
// session cookie, stored under the cookie's SHA-256 (never the cookie itself)
// in <home>/state/sessions.json (0600). The dashboard makes and checks them;
// `metarush remote signout` and `remote disable` remove them from another
// process, so every change happens under a file lock and is written by an
// atomic rename (a reader never sees half a file).
package sessions

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// TTL is how long a sign-in lasts.
const TTL = 30 * 24 * time.Hour

// Session is one signed-in browser. ExpiresAt and LastUsed are this
// computer's clock: only the computer that keeps the file reads them.
type Session struct {
	ExpiresAt time.Time `json:"exp"`
	// CreatedAt is compared with the person's sessionsValidAfter ("sign out
	// everywhere", written by any computer with the database server's time):
	// password and one-time link sessions are made with the server's time
	// (auth.SessionStart, D360); older sessions and the rest have this
	// computer's.
	CreatedAt time.Time `json:"created,omitzero"`
	// UserID and UserName are the person it acts as.
	UserID   string `json:"userId,omitempty"`
	UserName string `json:"userName,omitempty"`
	// Login is the Tailscale account that signed in by itself (D129); empty
	// for a session from a one-time sign-in link.
	Login string `json:"login,omitempty"`
	// Public: made through the public address (a browser on another computer,
	// through `tailscale serve`, D128). It is only good there.
	Public bool `json:"public,omitempty"`
	// From is the Tailscale address of the computer that signed in (shown to
	// the owner, for a lost device).
	From string `json:"from,omitempty"`
	// Method is how it signed in (D183): link (a one-time link on this
	// computer: the owner), password or tailscale. Empty: an older link or
	// Tailscale session (Login says which).
	Method string `json:"method,omitempty"`
	// LastUsed is when it was last used (written at most hourly): a password
	// session unused for SessionIdle ends.
	LastUsed time.Time `json:"used,omitzero"`
}

// UnmarshalJSON also reads sessions written before remote access, which held
// only the expiry time (they are this computer's own browser).
func (s *Session) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var t time.Time
		if err := json.Unmarshal(b, &t); err != nil {
			return err
		}
		*s = Session{ExpiresAt: t}
		return nil
	}
	type plain Session
	return json.Unmarshal(b, (*plain)(s))
}

// Store is the sessions file of one installation.
type Store struct {
	dir string
	mu  sync.Mutex
}

var stores = struct {
	sync.Mutex
	m map[string]*Store
}{m: map[string]*Store{}}

// For returns the store of a state folder (one per folder in a process).
func For(stateDir string) *Store {
	stores.Lock()
	defer stores.Unlock()
	if st := stores.m[stateDir]; st != nil {
		return st
	}
	st := &Store{dir: stateDir}
	stores.m[stateDir] = st
	return st
}

// Hash is how a cookie value is kept on disk.
func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (st *Store) file() string { return filepath.Join(st.dir, "sessions.json") }

func (st *Store) read() map[string]Session {
	m := map[string]Session{}
	if b, err := os.ReadFile(st.file()); err == nil {
		if json.Unmarshal(b, &m) != nil {
			// A damaged file: keep what could be read one by one.
			var raw map[string]json.RawMessage
			m = map[string]Session{}
			if json.Unmarshal(b, &raw) == nil {
				for k, v := range raw {
					var s Session
					if json.Unmarshal(v, &s) == nil {
						m[k] = s
					}
				}
			}
		}
	}
	return m
}

// change runs fn on the sessions under the file lock and writes the result.
func (st *Store) change(fn func(m map[string]Session) bool) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := os.MkdirAll(st.dir, 0o755); err != nil {
		return err
	}
	lf, err := os.OpenFile(filepath.Join(st.dir, "sessions.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) }()
	m := st.read()
	if !fn(m) {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := st.file() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, st.file())
}

// New makes a session and returns the cookie value for it (shown to the
// browser once; only its hash is kept). Expired sessions are cleared out. A
// CreatedAt given is kept (the database server's time); otherwise it is now.
func (st *Store) New(s Session, now time.Time) (string, error) {
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		return "", err
	}
	val := hex.EncodeToString(tok)
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	s.CreatedAt, s.LastUsed = s.CreatedAt.UTC(), now.UTC()
	if s.ExpiresAt.IsZero() {
		s.ExpiresAt = now.Add(TTL).UTC()
	}
	err := st.change(func(m map[string]Session) bool {
		for k, old := range m {
			if now.After(old.ExpiresAt) {
				delete(m, k)
			}
		}
		m[Hash(val)] = s
		return true
	})
	if err != nil {
		return "", err
	}
	return val, nil
}

// Get returns the session for a cookie value, when it exists and has not expired.
func (st *Store) Get(token string, now time.Time) (Session, bool) {
	if token == "" {
		return Session{}, false
	}
	s, ok := st.read()[Hash(token)]
	if !ok || !now.Before(s.ExpiresAt) {
		return Session{}, false
	}
	return s, true
}

// Touch records that a session was used, at most once an hour (the file
// isn't rewritten on every request).
func (st *Store) Touch(token string, now time.Time) {
	h := Hash(token)
	_ = st.change(func(m map[string]Session) bool {
		s, ok := m[h]
		if !ok || now.Sub(s.LastUsed) < time.Hour {
			return false
		}
		s.LastUsed = now.UTC()
		m[h] = s
		return true
	})
}

// Remove ends the session for a cookie value.
func (st *Store) Remove(token string) error {
	h := Hash(token)
	return st.change(func(m map[string]Session) bool {
		if _, ok := m[h]; !ok {
			return false
		}
		delete(m, h)
		return true
	})
}

// RemoveWhere ends every session fn picks and says how many ended.
func (st *Store) RemoveWhere(fn func(Session) bool) (int, error) {
	n := 0
	err := st.change(func(m map[string]Session) bool {
		for k, s := range m {
			if fn(s) {
				delete(m, k)
				n++
			}
		}
		return n > 0
	})
	return n, err
}

// List returns the sessions that have not expired.
func (st *Store) List(now time.Time) []Session {
	var out []Session
	for _, s := range st.read() {
		if now.Before(s.ExpiresAt) {
			out = append(out, s)
		}
	}
	return out
}
