// Package choice keeps what a person chose for this home's optional
// components (D325): added or left out, by whom and when, in
// <home>/state/components.json. It imports nothing of MetaRush's own, so
// packages a component sets up (the renderer, say) can ask it whether to set
// themselves up without a person asking.
package choice

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
)

// Entry is one component's choice.
type Entry struct {
	// Wanted: the person wants it on this computer (false: they left it out
	// or removed it, or it waits for a person).
	Wanted bool `json:"wanted"`
	// Waiting: setup was run by an AI app or a script nobody answered, so it
	// waits for a person to add it (D327); like left out, nothing sets it up
	// by itself.
	Waiting bool `json:"waiting,omitempty"`
	// By is who chose: "setup" for setup's suggested answers, else the person.
	By string    `json:"by,omitempty"`
	At time.Time `json:"at"`
}

// Record is every choice made for one home.
type Record struct {
	Components map[string]Entry `json:"components"`
}

var mu sync.Mutex

// Path is the record's file.
func Path(home string) string { return filepath.Join(home, "state", "components.json") }

// Load reads the record (empty when nothing was chosen yet).
func Load(home string) Record {
	r := Record{Components: map[string]Entry{}}
	b, err := os.ReadFile(Path(home))
	if err != nil {
		return r
	}
	_ = json.Unmarshal(b, &r)
	if r.Components == nil {
		r.Components = map[string]Entry{}
	}
	return r
}

// Of is one component's choice; ok is false when nobody chose yet.
func Of(home, name string) (Entry, bool) {
	e, ok := Load(home).Components[name]
	return e, ok
}

// Declined reports whether a person left a component out or removed it, or
// it waits for a person, so nothing sets it up by itself.
func Declined(home, name string) bool {
	e, ok := Of(home, name)
	return ok && !e.Wanted
}

// LeftOut is the error a feature returns instead of setting up a component
// a person left out or removed (D325): the component's own sentence (its
// words in internal/components, which a test keeps equal), E_TOOL_MISSING,
// and the one step that adds it.
func LeftOut(name, sentence string) *apperr.Error {
	return apperr.New(apperr.ToolMissing, "%s", sentence).
		WithDetails(map[string]any{"component": name, "place": "/settings/components"}).
		WithCommand("metarush components add " + name)
}

// Set records a choice (several at once share one write).
func Set(home string, wanted map[string]bool, by string) error {
	entries := map[string]Entry{}
	for n, w := range wanted {
		entries[n] = Entry{Wanted: w}
	}
	return set(home, entries, by)
}

// SetWaiting records components that wait for a person (setup run by an AI
// app or a script): not wanted yet, never set up by themselves. A choice a
// person already made is kept.
func SetWaiting(home string, names []string, by string) error {
	entries := map[string]Entry{}
	cur := Load(home)
	for _, n := range names {
		if _, decided := cur.Components[n]; decided {
			continue
		}
		entries[n] = Entry{Waiting: true}
	}
	if len(entries) == 0 {
		return nil
	}
	return set(home, entries, by)
}

func set(home string, entries map[string]Entry, by string) error {
	mu.Lock()
	defer mu.Unlock()
	r := Load(home)
	now := time.Now().UTC()
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		e := entries[n]
		e.By, e.At = by, now
		r.Components[n] = e
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	p := Path(home)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
