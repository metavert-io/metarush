// Package uploads keeps the daemon's browser-upload records: one JSON file per
// upload (plus the received bytes in a .part file) under <home>/state/uploads.
// Records carry the whole journey — bytes received, then the server-side stages
// (checking, fingerprinting, storing) with byte progress and rate — so a
// dashboard rebuilds its upload tray and progress from server state after a
// reload, and an interrupted upload resumes from the server's offset.
package uploads

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
)

// Upload states.
const (
	StateReceiving = "receiving"
	StateIngesting = "ingesting"
	StateDone      = "done"
	StateFailed    = "failed"
	StateCanceled  = "canceled"
)

// Server-side stages of a browser upload (after "sending", the browser's part).
const (
	StageSending        = "sending"        // bytes arriving from the browser
	StageChecking       = "checking"       // ffprobe: is it media, what is it
	StageFingerprinting = "fingerprinting" // full SHA-256 (the authoritative dedupe)
	StageStoring        = "storing"        // upload to the canonical media store
	StageDone           = "done"
)

// InterruptedAfter is how long a receiving upload may go without new bytes
// before it is shown as interrupted.
const InterruptedAfter = 30 * time.Second

// Record is a browser upload as the dashboard sees it (GET /api/uploads/{id}).
type Record struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	Offset      int64  `json:"offset" jsonschema:"description=Bytes the server has received."`
	Fingerprint string `json:"fingerprint,omitempty" jsonschema:"description=fp1 fingerprint the browser computed (first + last MiB); used to resume a re-dropped file."`
	ProjectID   string `json:"projectId,omitempty"`
	State       string `json:"state" jsonschema:"enum=receiving,enum=ingesting,enum=done,enum=failed,enum=canceled"`
	// Stage, StageDone/StageTotal and Rate describe the current step with byte
	// progress, persisted (throttled) so a reload shows the truth.
	Stage          string         `json:"stage" jsonschema:"enum=sending,enum=checking,enum=fingerprinting,enum=storing,enum=done"`
	StageDone      int64          `json:"stageDone"`
	StageTotal     int64          `json:"stageTotal"`
	Rate           float64        `json:"rate,omitempty" jsonschema:"description=Bytes per second in the current stage (server-measured)."`
	StageStartedAt time.Time      `json:"stageStartedAt"`
	LastProgressAt time.Time      `json:"lastProgressAt" jsonschema:"description=When bytes last moved (received or processed)."`
	Result         *ingest.Result `json:"result,omitempty"`
	Error          string         `json:"error,omitempty"`
	Dismissed      bool           `json:"dismissed,omitempty"`
	Disconnected   bool           `json:"disconnected,omitempty" jsonschema:"description=The last chunk was cut off before it finished; cleared when sending resumes."`
	Notified       bool           `json:"notified,omitempty" jsonschema:"description=An upload-interrupted notification was written."`
	// Via is where the upload came from: the dashboard (default) or the browser
	// extension; Owner is the paired browser that started it (only it may send
	// its bytes); Source is the original post the extension saved it from.
	Via       string              `json:"via,omitempty" jsonschema:"enum=dashboard,enum=extension"`
	Owner     string              `json:"owner,omitempty"`
	Source    *models.AssetSource `json:"source,omitempty"`
	CreatedAt time.Time           `json:"createdAt"`
	UpdatedAt time.Time           `json:"updatedAt"`

	// UserID and UserName are the person who started it (with remote access,
	// whoever signed in on another computer): the clip and its notices are theirs.
	UserID   string `json:"userId,omitempty"`
	UserName string `json:"userName,omitempty"`

	markAt    time.Time // rate window (memory only)
	markBytes int64
}

// Interrupted reports a receiving upload that stopped getting bytes: its last
// chunk was cut off (a closed tab, a reload, a lost connection), or nothing
// arrived for InterruptedAfter.
func (r *Record) Interrupted(now time.Time) bool {
	last := r.LastProgressAt
	if last.IsZero() {
		last = r.UpdatedAt
	}
	return r.State == StateReceiving && (r.Disconnected || now.Sub(last) > InterruptedAfter)
}

// Pct is the fraction of the file received.
func (r *Record) Pct() float64 {
	if r.Size <= 0 {
		return 0
	}
	return float64(r.Offset) / float64(r.Size)
}

// Store is one installation's upload records. Use For(stateDir) so every part
// of a process shares the same in-memory state.
type Store struct {
	dir      string
	mu       sync.Mutex
	m        map[string]*Record
	scanned  bool
	lastDisk map[string]time.Time
	// OnChange is called (outside the lock) after a record changes.
	OnChange func(r Record)
}

var (
	regMu    sync.Mutex
	registry = map[string]*Store{}
)

// For returns the shared store for a state directory.
func For(stateDir string) *Store {
	dir := filepath.Join(stateDir, "uploads")
	regMu.Lock()
	defer regMu.Unlock()
	if s, ok := registry[dir]; ok {
		return s
	}
	s := &Store{dir: dir, m: map[string]*Record{}, lastDisk: map[string]time.Time{}}
	registry[dir] = s
	return s
}

// Dir is where records and part files live.
func (s *Store) Dir() string { return s.dir }

// Part is the path of an upload's received bytes.
func (s *Store) Part(id string) string { return filepath.Join(s.dir, id+".part") }

func (s *Store) jsonPath(id string) string { return filepath.Join(s.dir, id+".json") }

// Create registers a new upload and its empty part file.
func (s *Store) Create(name string, size int64, projectID, fingerprint string) (Record, error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return Record{}, err
	}
	now := time.Now().UTC()
	r := &Record{ID: ids.New(ids.Upload), Name: name, Size: size, ProjectID: projectID, Fingerprint: fingerprint, State: StateReceiving,
		Stage: StageSending, StageTotal: size, StageStartedAt: now, LastProgressAt: now, CreatedAt: now, UpdatedAt: now}
	f, err := os.OpenFile(s.Part(r.ID), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Record{}, err
	}
	f.Close()
	s.mu.Lock()
	s.m[r.ID] = r
	s.mu.Unlock()
	s.persist(r, true)
	return *r, nil
}

// Get returns a copy of a record (loading it from disk when needed).
func (s *Store) Get(id string) (Record, bool) {
	if !ids.Valid(ids.Upload, id) {
		return Record{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.loadLocked(id)
	if !ok {
		return Record{}, false
	}
	return *r, true
}

func (s *Store) loadLocked(id string) (*Record, bool) {
	if r, ok := s.m[id]; ok {
		return r, true
	}
	b, err := os.ReadFile(s.jsonPath(id))
	if err != nil {
		return nil, false
	}
	var r Record
	if json.Unmarshal(b, &r) != nil || r.ID != id {
		return nil, false
	}
	if r.Stage == "" {
		r.Stage = StageSending
	}
	if st, err := os.Stat(s.Part(id)); err == nil && r.State == StateReceiving {
		r.Offset = st.Size()
	}
	s.m[id] = &r
	return &r, true
}

// Update changes a record under the lock. force writes to disk at once;
// otherwise disk writes are throttled to about one per second per record (the
// in-memory copy is always current).
func (s *Store) Update(id string, force bool, fn func(r *Record)) (Record, bool) {
	s.mu.Lock()
	r, ok := s.loadLocked(id)
	if !ok {
		s.mu.Unlock()
		return Record{}, false
	}
	fn(r)
	r.UpdatedAt = time.Now().UTC()
	snap := *r
	s.mu.Unlock()
	s.persist(&snap, force)
	return snap, true
}

func (s *Store) persist(r *Record, force bool) {
	s.mu.Lock()
	last := s.lastDisk[r.ID]
	write := force || time.Since(last) >= time.Second
	if write {
		s.lastDisk[r.ID] = time.Now()
	}
	s.mu.Unlock()
	if write {
		b, _ := json.Marshal(r)
		tmp := s.jsonPath(r.ID) + ".tmp"
		if os.WriteFile(tmp, b, 0o600) == nil {
			_ = os.Rename(tmp, s.jsonPath(r.ID))
		}
	}
	if s.OnChange != nil {
		s.OnChange(*r)
	}
}

// Flush writes every record's current state to disk.
func (s *Store) Flush() {
	s.mu.Lock()
	var all []Record
	for _, r := range s.m {
		all = append(all, *r)
	}
	s.mu.Unlock()
	for i := range all {
		b, _ := json.Marshal(&all[i])
		_ = os.WriteFile(s.jsonPath(all[i].ID), b, 0o600)
	}
}

// List returns records newest first: everything still active, plus finished
// uploads from the last `recent` that were not dismissed.
func (s *Store) List(recent time.Duration) []Record {
	s.mu.Lock()
	if !s.scanned {
		s.scanned = true
		entries, _ := os.ReadDir(s.dir)
		for _, e := range entries {
			if id, ok := strings.CutSuffix(e.Name(), ".json"); ok && ids.Valid(ids.Upload, id) {
				s.loadLocked(id)
			}
		}
	}
	cutoff := time.Now().Add(-recent)
	var out []Record
	for _, r := range s.m {
		active := r.State == StateReceiving || r.State == StateIngesting
		if r.Dismissed || (!active && r.UpdatedAt.Before(cutoff)) {
			continue
		}
		if r.State == StateReceiving && r.UpdatedAt.Before(time.Now().Add(-7*24*time.Hour)) {
			continue // abandoned long ago; ingest.reconcile cleans these up
		}
		out = append(out, *r)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// FindResumable finds a receiving upload of the same file (base name, size and,
// when both are known, fingerprint) so a re-dropped file continues where it stopped.
func (s *Store) FindResumable(name string, size int64, fingerprint string) (Record, bool) {
	base := filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	var best Record
	found := false
	for _, r := range s.List(0) {
		if r.State != StateReceiving || r.Size != size || r.Name != base {
			continue
		}
		if fingerprint != "" && r.Fingerprint != "" && r.Fingerprint != fingerprint {
			continue
		}
		if !found || r.Offset > best.Offset {
			best, found = r, true
		}
	}
	return best, found
}

// StageFor maps ingest progress phases to upload stages.
func StageFor(phase string) string {
	switch phase {
	case "probe":
		return StageChecking
	case "hash":
		return StageFingerprinting
	case "upload", "cache":
		return StageStoring
	case "done":
		return StageDone
	}
	return phase
}

// Progress records bytes done in a stage and keeps a smoothed rate (measured
// over windows of at least half a second).
func Progress(r *Record, stage string, done, total int64, now time.Time) {
	if r.Stage != stage {
		r.Stage, r.StageStartedAt, r.StageDone, r.Rate, r.LastProgressAt = stage, now, 0, 0, now
		r.markAt, r.markBytes = now, 0
	}
	if total > 0 {
		r.StageTotal = total
	}
	if r.markAt.IsZero() {
		r.markAt, r.markBytes = now, r.StageDone
	}
	if done > r.StageDone {
		r.StageDone, r.LastProgressAt = done, now
	}
	if dt := now.Sub(r.markAt).Seconds(); dt >= 0.5 {
		inst := float64(r.StageDone-r.markBytes) / dt
		if r.Rate > 0 {
			r.Rate = r.Rate*0.6 + inst*0.4
		} else {
			r.Rate = inst
		}
		r.markAt, r.markBytes = now, r.StageDone
	}
}
