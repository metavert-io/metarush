package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
)

// Watcher ingests files dropped into a folder once they stop changing. Recordings
// still being written are never uploaded: a file must keep the same size and
// modification time for StableFor, and ffprobe must be able to read it.
type Watcher struct {
	App       *app.App
	Dir       string
	StableFor time.Duration
	Move      bool // move successfully ingested files into <Dir>/Ingested (never delete)
	ProjectID string
	Log       func(format string, a ...any)

	mu       sync.Mutex
	seen     map[string]obs
	done     map[string]bool // path|size|mtime already ingested
	skipped  map[string]bool // files that were already there when watching started with "only new files"
	Status   WatchStatus
	lastSave time.Time
}

func skippedFile(stateDir string) string { return filepath.Join(stateDir, "watch-skipped.json") }

// MarkExistingSkipped records every file now in dir as "leave alone", so the
// watcher only ingests files added from now on. It returns how many there were.
func MarkExistingSkipped(stateDir, dir string) (int, error) {
	files, _ := Collect([]string{dir}, true)
	m := map[string]bool{}
	if b, err := os.ReadFile(skippedFile(stateDir)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	n := 0
	for _, f := range files {
		if strings.Contains(f, string(filepath.Separator)+"Ingested"+string(filepath.Separator)) {
			continue
		}
		if st, err := os.Stat(f); err == nil {
			m[key(f, st.Size(), st.ModTime())] = true
			n++
		}
	}
	b, _ := json.Marshal(m)
	return n, os.WriteFile(skippedFile(stateDir), b, 0o644)
}

// ClearSkipped forgets the "leave alone" marks, so files that were already in the
// folder are ingested too ("Import what's already there").
func ClearSkipped(stateDir string) int {
	m := map[string]bool{}
	if b, err := os.ReadFile(skippedFile(stateDir)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	_ = os.Remove(skippedFile(stateDir))
	return len(m)
}

// CountSkipped reports how many files are being left alone.
func CountSkipped(stateDir, dir string) int {
	m := map[string]bool{}
	if b, err := os.ReadFile(skippedFile(stateDir)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	n := 0
	for k := range m {
		if strings.HasPrefix(k, dir+string(filepath.Separator)) {
			n++
		}
	}
	return n
}

type obs struct {
	size   int64
	mtime  time.Time
	stable time.Time
}

// WatchStatus is the live state of the watched folder (shown in the dashboard
// and by `metarush watch status`).
type WatchStatus struct {
	Dir     string `json:"dir"`
	Enabled bool   `json:"enabled"`
	// Missing: the folder does not exist (an unmounted drive, a deleted folder).
	// The watcher keeps checking and resumes when it reappears.
	Missing    bool      `json:"missing,omitempty"`
	Pending    []string  `json:"pending" jsonschema:"description=Files waiting to stop changing before they are ingested."`
	Ingesting  string    `json:"ingesting,omitempty" jsonschema:"description=The file being ingested right now."`
	LastScan   time.Time `json:"lastScan"`
	LastIngest string    `json:"lastIngest,omitempty"`
	// The last file ingested, structured.
	LastIngestAt    *time.Time `json:"lastIngestAt,omitempty"`
	LastIngestName  string     `json:"lastIngestName,omitempty"`
	LastIngestTitle string     `json:"lastIngestTitle,omitempty"`
	LastIngestAsset string     `json:"lastIngestAsset,omitempty"`
	LastIngestState string     `json:"lastIngestStatus,omitempty"`
	Errors          []string   `json:"errors"`
	Ingested        int        `json:"ingested" jsonschema:"description=Files ingested since the watcher started."`
}

func (w *Watcher) stateFile() string { return filepath.Join(w.App.Cfg.StateDir(), "watch-done.json") }

// StatusFile is where a running watcher publishes its status for other processes.
func StatusFile(stateDir string) string { return filepath.Join(stateDir, "watch-status.json") }

func (w *Watcher) load() {
	w.done = map[string]bool{}
	if b, err := os.ReadFile(w.stateFile()); err == nil {
		_ = json.Unmarshal(b, &w.done)
	}
	w.skipped = map[string]bool{}
	if b, err := os.ReadFile(skippedFile(w.App.Cfg.StateDir())); err == nil {
		_ = json.Unmarshal(b, &w.skipped)
	}
}

func (w *Watcher) save() {
	b, _ := json.Marshal(w.done)
	_ = os.WriteFile(w.stateFile(), b, 0o644)
}

// publish writes the status file (at most every few seconds unless forced).
func (w *Watcher) publish(force bool) {
	if !force && time.Since(w.lastSave) < 5*time.Second {
		return
	}
	w.lastSave = time.Now()
	s := w.Snapshot()
	b, _ := json.Marshal(s)
	tmp := StatusFile(w.App.Cfg.StateDir()) + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, StatusFile(w.App.Cfg.StateDir()))
	}
}

// Run polls until ctx ends. Polling (not fsnotify) behaves identically on local
// disks, external drives and network mounts. A missing folder is reported, not
// created (an unmounted drive must not turn into an empty local folder).
func (w *Watcher) Run(ctx context.Context) error {
	if w.Dir == "" {
		return fmt.Errorf("watch folder not configured")
	}
	if w.StableFor <= 0 {
		w.StableFor = 6 * time.Second
	}
	if w.Log == nil {
		w.Log = func(string, ...any) {}
	}
	w.seen = map[string]obs{}
	w.load()
	w.mu.Lock()
	w.Status = WatchStatus{Dir: w.Dir, Enabled: true, Pending: []string{}, Errors: []string{}}
	w.mu.Unlock()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		w.scan(ctx)
		w.publish(false)
		select {
		case <-ctx.Done():
			w.mu.Lock()
			w.Status.Enabled = false
			w.mu.Unlock()
			w.publish(true)
			return nil
		case <-t.C:
		}
	}
}

func key(path string, size int64, mtime time.Time) string {
	return fmt.Sprintf("%s|%d|%d", path, size, mtime.UnixNano())
}

func (w *Watcher) scan(ctx context.Context) {
	now := time.Now()
	if st, err := os.Stat(w.Dir); err != nil || !st.IsDir() {
		w.mu.Lock()
		changed := !w.Status.Missing
		w.Status.Missing, w.Status.Pending, w.Status.LastScan = true, []string{}, now
		w.mu.Unlock()
		if changed {
			w.Log("watch: %s is missing; waiting for it to reappear", w.Dir)
			w.publish(true)
		}
		return
	}
	w.mu.Lock()
	w.Status.Missing = false
	w.mu.Unlock()
	files, _ := Collect([]string{w.Dir}, true)
	var pending []string
	present := map[string]bool{}
	for _, f := range files {
		if strings.Contains(f, string(filepath.Separator)+"Ingested"+string(filepath.Separator)) {
			continue
		}
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		present[f] = true
		k := key(f, st.Size(), st.ModTime())
		if w.done[k] || w.skipped[k] {
			continue
		}
		o, ok := w.seen[f]
		if !ok || o.size != st.Size() || !o.mtime.Equal(st.ModTime()) {
			w.seen[f] = obs{size: st.Size(), mtime: st.ModTime(), stable: now}
			pending = append(pending, filepath.Base(f))
			continue
		}
		if now.Sub(o.stable) < w.StableFor || now.Sub(st.ModTime()) < w.StableFor {
			pending = append(pending, filepath.Base(f))
			continue
		}
		w.Log("watch: adding %s", f)
		w.mu.Lock()
		w.Status.Ingesting = filepath.Base(f)
		w.mu.Unlock()
		w.publish(true)
		// Unattended drops keep both names: a copy under another filename is
		// recorded as an alias of the existing asset (never stored twice).
		res := File(ctx, w.App, f, Options{ProjectID: w.ProjectID, Via: "watch", Transcribe: true, Duplicates: DuplicatesKeep})
		w.mu.Lock()
		w.Status.Ingesting = ""
		w.mu.Unlock()
		switch res.Status {
		case "ingested", "deduped", "resumed", "duplicate":
			w.done[k] = true
			at := time.Now().UTC()
			w.mu.Lock()
			w.Status.LastIngest = lastAddedText(filepath.Base(f), res)
			w.Status.LastIngestAt, w.Status.LastIngestName, w.Status.LastIngestTitle = &at, filepath.Base(f), res.Title
			w.Status.LastIngestAsset, w.Status.LastIngestState = res.AssetID, res.Status
			w.Status.Ingested++
			w.mu.Unlock()
			if w.Move {
				dst := filepath.Join(w.Dir, "Ingested", filepath.Base(f))
				if _, err := os.Stat(dst); err == nil {
					dst = filepath.Join(w.Dir, "Ingested", fmt.Sprintf("%d-%s", now.Unix(), filepath.Base(f)))
				}
				_ = os.MkdirAll(filepath.Dir(dst), 0o755)
				if err := os.Rename(f, dst); err == nil {
					st2, _ := os.Stat(dst)
					if st2 != nil {
						w.done[key(dst, st2.Size(), st2.ModTime())] = true
					}
				}
			}
			w.save()
			w.publish(true)
		case "skipped":
			w.done[k] = true // not media: don't retry every scan
			w.save()
			w.Log("watch: skipped %s: %s", f, res.Error)
		default:
			w.Log("watch: %s failed (will retry): %s", f, res.Error)
			w.mu.Lock()
			w.Status.Errors = append([]string{filepath.Base(f) + ": " + res.Error}, w.Status.Errors...)
			if len(w.Status.Errors) > 10 {
				w.Status.Errors = w.Status.Errors[:10]
			}
			w.mu.Unlock()
			delete(w.seen, f)
			w.publish(true)
		}
	}
	for f := range w.seen {
		if !present[f] {
			delete(w.seen, f)
		}
	}
	w.mu.Lock()
	if pending == nil {
		pending = []string{}
	}
	w.Status.Pending, w.Status.LastScan = pending, now
	w.mu.Unlock()
}

// lastAddedText says what became of the last file, in plain words (the clip
// itself is LastIngestAsset): "kitchen.mov (as “Kitchen chat”)".
func lastAddedText(name string, res Result) string {
	if d := res.Duplicate; d != nil {
		switch d.Action {
		case models.DuplicateKeptBoth:
			return fmt.Sprintf("%s (the same as “%s”; both names are kept, the file is stored once)", name, d.Title)
		case models.DuplicateSameFile:
			return fmt.Sprintf("%s (already in MetaRush as “%s”)", name, d.Title)
		}
		return fmt.Sprintf("%s (the same as “%s”, so it was skipped)", name, d.Title)
	}
	if res.Title != "" {
		return fmt.Sprintf("%s (as “%s”)", name, res.Title)
	}
	return name
}

// Snapshot returns a copy of the status.
func (w *Watcher) Snapshot() WatchStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.Status
	s.Pending = append([]string{}, s.Pending...)
	s.Errors = append([]string{}, s.Errors...)
	return s
}
