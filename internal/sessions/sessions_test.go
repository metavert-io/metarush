package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSessionsLifecycle(t *testing.T) {
	dir := t.TempDir()
	st := For(dir)
	now := time.Now()
	tok, err := st.New(Session{UserID: "usr_a", UserName: "Sam", Public: true, Login: "sam@example.com"}, now)
	if err != nil || len(tok) != 64 {
		t.Fatalf("%q %v", tok, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "sessions.json"))
	if strings.Contains(string(b), tok) || !strings.Contains(string(b), Hash(tok)) {
		t.Fatal("only the cookie's hash is kept")
	}
	if fi, _ := os.Stat(filepath.Join(dir, "sessions.json")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	s, ok := st.Get(tok, now)
	if !ok || s.UserID != "usr_a" || !s.Public || s.Login != "sam@example.com" || s.ExpiresAt.Sub(now) < TTL-time.Minute {
		t.Fatalf("%+v %v", s, ok)
	}
	if _, ok := st.Get(tok, now.Add(TTL+time.Hour)); ok {
		t.Fatal("expired")
	}
	if _, ok := st.Get("nope", now); ok {
		t.Fatal("unknown")
	}
	tok2, _ := st.New(Session{UserID: "usr_b"}, now)
	if n, err := st.RemoveWhere(func(s Session) bool { return s.Public }); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if _, ok := st.Get(tok, now); ok {
		t.Fatal("removed")
	}
	if _, ok := st.Get(tok2, now); !ok {
		t.Fatal("the local session stays")
	}
	if err := st.Remove(tok2); err != nil {
		t.Fatal(err)
	}
	if len(st.List(now)) != 0 {
		t.Fatal("empty")
	}
}

// Sessions made before remote access were only an expiry time: they still
// work, as this computer's own browser.
func TestSessionsReadOlderFile(t *testing.T) {
	dir := t.TempDir()
	tok := strings.Repeat("ab", 32)
	exp := time.Now().Add(time.Hour).UTC()
	b, _ := json.Marshal(map[string]time.Time{Hash(tok): exp})
	if err := os.WriteFile(filepath.Join(dir, "sessions.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	s, ok := For(dir).Get(tok, time.Now())
	if !ok || s.Public || s.Login != "" || !s.ExpiresAt.Equal(exp) {
		t.Fatalf("%+v %v", s, ok)
	}
	// Writing keeps it (in the new form).
	if _, err := For(dir).New(Session{UserID: "usr_x"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := For(dir).Get(tok, time.Now()); !ok {
		t.Fatal("the older session survives a write")
	}
}

func TestSessionsConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	toks := make([]string, 40)
	for i := range toks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Separate stores share only the file and its lock, like two processes.
			st := &Store{dir: dir}
			toks[i], _ = st.New(Session{UserID: "usr"}, time.Now())
		}(i)
	}
	wg.Wait()
	for _, tok := range toks {
		if _, ok := For(dir).Get(tok, time.Now()); !ok {
			t.Fatal("a session was lost between writers")
		}
	}
}
