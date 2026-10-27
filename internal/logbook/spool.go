package logbook

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The spool holds entries that could not be written (database or media store
// unreachable): JSON lines in <home>/state/log-spool, already redacted. Any
// later process (the next command, or the daemon every 30 seconds) writes them
// and deletes the file. Entries keep their IDs, so a replay never duplicates.

const staleClaim = 10 * time.Minute

func (l *Logger) spoolDir() string {
	if l.cfg == nil {
		return ""
	}
	return l.cfg.LogSpoolDir()
}

// spool appends records to a new spool file.
func (l *Logger) spool(recs []*spoolRecord) error {
	dir := l.spoolDir()
	if dir == "" {
		return fmt.Errorf("no installation home")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	name := fmt.Sprintf("%d-%d-%s.jsonl", time.Now().UnixNano(), os.Getpid(), hex.EncodeToString(rnd[:]))
	tmp := filepath.Join(dir, "."+name+".tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, r := range recs {
		if err := enc.Encode(r); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

// SpoolStatus counts what is waiting in the spool.
type SpoolStatus struct {
	Files   int       `json:"files"`
	Entries int       `json:"entries"`
	Oldest  time.Time `json:"oldest,omitempty"`
}

// Spooled reports the spool's contents.
func (l *Logger) Spooled() SpoolStatus {
	var st SpoolStatus
	if l == nil {
		return st
	}
	files, _ := filepath.Glob(filepath.Join(l.spoolDir(), "*.jsonl"))
	for _, f := range files {
		st.Files++
		if info, err := os.Stat(f); err == nil && (st.Oldest.IsZero() || info.ModTime().Before(st.Oldest)) {
			st.Oldest = info.ModTime().UTC()
		}
		if b, err := os.ReadFile(f); err == nil {
			st.Entries += strings.Count(string(b), "\n")
		}
	}
	return st
}

// FlushSpool writes spooled entries to the database (at most maxFiles files;
// 0 means all). Files are claimed by renaming, so concurrent processes never
// write the same file twice; a claim left by a process that died is released
// after ten minutes.
func (l *Logger) FlushSpool(ctx context.Context, maxFiles int) (written int, err error) {
	if l == nil || (l.db == nil && l.insertF == nil) || l.held() {
		return 0, nil
	}
	dir := l.spoolDir()
	if claims, _ := filepath.Glob(filepath.Join(dir, "*.jsonl.claim-*")); len(claims) > 0 {
		for _, c := range claims {
			if info, err := os.Stat(c); err == nil && time.Since(info.ModTime()) > staleClaim {
				_ = os.Rename(c, c[:strings.Index(c, ".claim-")])
			}
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	sort.Strings(files) // oldest first (names start with the time)
	for i, f := range files {
		if maxFiles > 0 && i >= maxFiles {
			break
		}
		if ctx.Err() != nil {
			return written, ctx.Err()
		}
		claimed := fmt.Sprintf("%s.claim-%d", f, os.Getpid())
		if err := os.Rename(f, claimed); err != nil {
			continue // another process took it
		}
		recs, rerr := readSpool(claimed)
		if rerr != nil {
			// Unreadable: keep it aside for a person to look at, never loop on it.
			_ = os.Rename(claimed, f+".bad")
			continue
		}
		failed := l.write(ctx, recs) // records were redacted before they were spooled
		if len(failed) > 0 {
			// Still unreachable: put the remainder back and stop for now.
			tmp := claimed + ".rest"
			if werr := writeSpoolFile(tmp, failed); werr == nil {
				_ = os.Rename(tmp, f)
				_ = os.Remove(claimed)
			} else {
				_ = os.Rename(claimed, f)
			}
			return written + len(recs) - len(failed), fmt.Errorf("%d spooled log entries are still waiting", len(failed))
		}
		written += len(recs)
		_ = os.Remove(claimed)
	}
	return written, nil
}

func readSpool(path string) ([]*spoolRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []*spoolRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r spoolRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, sc.Err()
}

func writeSpoolFile(path string, recs []*spoolRecord) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, r := range recs {
		if err := enc.Encode(r); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}
