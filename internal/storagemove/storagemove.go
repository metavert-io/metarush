// Package storagemove copies a workspace's media from one store to another
// (`metarush storage migrate`, D94): every object under the workspace's
// prefix, resumably, checking each file's SHA-256 against its content address
// before it is written and the new store's own checksum (SHA-256 for a
// folder, MD5 for S3, CRC32C and MD5 for GCS) on the way in.
//
// It only reads from the old store and never deletes anything. Bytes come
// from the old store itself, or from this machine when it already has them (a
// folder store's own files, or the local cache), so a move from a bucket
// downloads only what this machine lacks. Progress survives a stop: objects
// already in the new store are skipped, an interrupted upload continues from
// its last part (S3 multipart, GCS resumable session, the folder store's
// staging file), and an interrupted download continues from its last byte.
package storagemove

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/metavert-io/metarush/internal/cache"
	"github.com/metavert-io/metarush/internal/media"
)

// Kinds of object, by what their key promises about their bytes.
const (
	KindObject = "object" // ws/<ws>/objects/<aa>/<sha>: the SHA-256 of the bytes
	KindLog    = "log"    // ws/<ws>/logs/<aa>/<sha>.gz: the SHA-256 of the text inside
	KindOther  = "other"  // anything else under the prefix: checked by reading it back
)

// Interrupted is what a move's job says when the process doing it stopped.
const Interrupted = "MetaRush stopped while moving the media. Start the move again: it continues where it stopped, and the old storage is unchanged."

// Problem reasons.
const (
	Damaged = "damaged" // the old store's bytes do not match their content address
	Differs = "differs" // the new store already has this key with another size
	Failed  = "failed"  // it could not be copied (the error says why)
)

// Options configure a move.
type Options struct {
	Workspace string
	From, To  media.Store // both must be media.Lister
	// Cache, when set, supplies objects this machine already has (verified
	// again before use), so they are not downloaded.
	Cache *cache.Cache
	// StateDir keeps the move's journal and unfinished uploads; ScratchDir its
	// partial downloads. One folder per move (see ID).
	StateDir, ScratchDir string
	Parallel             int  // objects copied at once (default 4)
	ReadBack             bool // also read every object back from the new store and check its SHA-256
	// Progress is called often (from several goroutines, serialized).
	Progress func(Progress)
}

// Progress is how far a move is.
type Progress struct {
	Phase     string  `json:"phase"` // listing | copying | checking
	Files     int     `json:"files"` // to copy (or read back), in all
	FilesDone int     `json:"filesDone"`
	Bytes     int64   `json:"bytes"`
	BytesDone int64   `json:"bytesDone"`
	Rate      float64 `json:"rate,omitempty"` // bytes per second over the last stretch
}

// Survey is what a move would do (the dry run).
type Survey struct {
	Files        int      `json:"files"` // in the old store
	Bytes        int64    `json:"bytes"`
	Present      int      `json:"present"` // already in the new store, same size
	PresentBytes int64    `json:"presentBytes"`
	ToCopy       int      `json:"toCopy"`
	ToCopyBytes  int64    `json:"toCopyBytes"`
	Local        int      `json:"local"` // of those to copy: on this machine already (no download)
	LocalBytes   int64    `json:"localBytes"`
	Largest      int64    `json:"largest"` // the largest file to download (scratch space it needs)
	Differ       []string `json:"differ"`  // keys in both stores with different sizes
}

// Problem is one object the move could not take across.
type Problem struct {
	Key    string `json:"key"`
	Reason string `json:"reason" jsonschema:"enum=damaged,enum=differs,enum=failed"`
	Detail string `json:"detail,omitempty"`
}

// Result is what a move did.
type Result struct {
	Files        int       `json:"files"` // in the old store (every pass)
	Bytes        int64     `json:"bytes"`
	Copied       int       `json:"copied"` // copied and checked by this move (this run and earlier ones)
	CopiedBytes  int64     `json:"copiedBytes"`
	CopiedNow    int       `json:"copiedNow"` // copied by this run
	Present      int       `json:"present"`   // already in the new store before the move
	PresentBytes int64     `json:"presentBytes"`
	ReadBack     int       `json:"readBack"` // read back from the new store and checked
	Downloaded   int64     `json:"downloaded"`
	Passes       int       `json:"passes"`
	Problems     []Problem `json:"problems"`
	// Verified: every object in the old store is in the new one with its size,
	// and nothing went wrong. Only then may the caller switch over.
	Verified bool `json:"verified"`
	// Sample is a key in both stores (to check the new store once switched).
	Sample string `json:"sample,omitempty"`
}

const (
	maxPasses = 5
	// bigObject: larger downloads go one at a time, so partial files on this
	// disk stay bounded.
	bigObject   = 256 << 20
	maxProblems = 50
	spareBytes  = 512 << 20 // left free on the scratch disk
)

// ID names a move by its workspace and both stores.
func ID(workspace, from, to string) string {
	h := sha256.Sum256([]byte(workspace + "\x00" + from + "\x00" + to))
	return hex.EncodeToString(h[:8])
}

// Prefix is the part of a store a workspace's media lives under.
func Prefix(workspace string) string { return "ws/" + workspace + "/" }

// Classify says what a key's name promises about its bytes, and the SHA-256 it names.
func Classify(workspace, key string) (kind, sha string) {
	rest, ok := strings.CutPrefix(key, Prefix(workspace))
	if !ok {
		return KindOther, ""
	}
	dir, name, _ := strings.Cut(rest, "/")
	sub, file, ok := strings.Cut(name, "/")
	if !ok || strings.Contains(file, "/") {
		return KindOther, ""
	}
	switch dir {
	case "objects":
		if validSHA(file) && sub == file[:2] {
			return KindObject, file
		}
	case "logs":
		if s, ok := strings.CutSuffix(file, ".gz"); ok && validSHA(s) && sub == s[:2] {
			return KindLog, s
		}
	}
	return KindOther, ""
}

func validSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

func listers(o *Options) (media.Lister, media.Lister, error) {
	from, ok := o.From.(media.Lister)
	if !ok {
		return nil, nil, fmt.Errorf("MetaRush can't list the files in the old storage (%s)", o.From.Name())
	}
	to, ok := o.To.(media.Lister)
	if !ok {
		return nil, nil, fmt.Errorf("MetaRush can't list the files in the new storage (%s)", o.To.Name())
	}
	return from, to, nil
}

func list(ctx context.Context, l media.Lister, prefix string) ([]media.ObjectInfo, error) {
	var out []media.ObjectInfo
	err := l.List(ctx, prefix, func(oi media.ObjectInfo) error {
		if media.ValidKey(oi.Key) == nil && strings.HasPrefix(oi.Key, prefix) {
			out = append(out, oi)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, err
}

func index(objs []media.ObjectInfo) map[string]media.ObjectInfo {
	m := make(map[string]media.ObjectInfo, len(objs))
	for _, o := range objs {
		m[o.Key] = o
	}
	return m
}

// Plan lists both stores and says what a move would copy. It reads only.
func Plan(ctx context.Context, o Options) (*Survey, error) {
	from, to, err := listers(&o)
	if err != nil {
		return nil, err
	}
	prefix := Prefix(o.Workspace)
	src, err := list(ctx, from, prefix)
	if err != nil {
		return nil, fmt.Errorf("list the old storage: %w", err)
	}
	dst, err := list(ctx, to, prefix)
	if err != nil {
		return nil, fmt.Errorf("list the new storage: %w", err)
	}
	have := index(dst)
	s := &Survey{Differ: []string{}}
	for _, ob := range src {
		s.Files++
		s.Bytes += ob.Size
		d, ok := have[ob.Key]
		switch {
		case ok && d.Size == ob.Size:
			s.Present++
			s.PresentBytes += ob.Size
		case ok:
			s.Differ = append(s.Differ, ob.Key)
		default:
			s.ToCopy++
			s.ToCopyBytes += ob.Size
			if localPath(&o, ob) != "" {
				s.Local++
				s.LocalBytes += ob.Size
			} else {
				s.Largest = max(s.Largest, ob.Size)
			}
		}
	}
	return s, nil
}

// localPath is where this machine already has an object's bytes (a folder
// store's file, or the cache), unchecked; "" when it has to be downloaded.
func localPath(o *Options, ob media.ObjectInfo) string {
	if lp, ok := o.From.(interface{ LocalPath(string) (string, error) }); ok {
		if p, err := lp.LocalPath(ob.Key); err == nil {
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Size() == ob.Size {
				return p
			}
		}
	}
	if kind, sha := Classify(o.Workspace, ob.Key); kind == KindObject && o.Cache != nil && o.Cache.Has(sha, ob.Size) {
		return o.Cache.Path(sha)
	}
	return ""
}

// mover is one run of a move.
type mover struct {
	o        *Options
	from, to media.Lister
	j        *journal
	res      *Result
	prog     *tracker

	mu       sync.Mutex
	problems []Problem
	resumes  map[string]media.Resume
	bigMu    sync.Mutex
}

type task struct {
	ob       media.ObjectInfo
	readBack bool // already in the new store: only read it back
}

// Run copies everything not yet in the new store, then checks that every
// object of the old store is there. Running it again continues a stopped move.
func Run(ctx context.Context, o Options) (*Result, error) {
	from, to, err := listers(&o)
	if err != nil {
		return nil, err
	}
	if o.Parallel <= 0 {
		o.Parallel = 4
	}
	for _, d := range []string{o.StateDir, o.ScratchDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	j, err := openJournal(filepath.Join(o.StateDir, "journal.jsonl"))
	if err != nil {
		return nil, err
	}
	defer j.close()
	m := &mover{o: &o, from: from, to: to, j: j, res: &Result{Problems: []Problem{}}, prog: newTracker(o.Progress), resumes: loadResumes(o.StateDir)}
	prefix := Prefix(o.Workspace)
	seen := map[string]media.ObjectInfo{}
	for pass := 1; pass <= maxPasses; pass++ {
		m.prog.phase("listing")
		src, err := list(ctx, from, prefix)
		if err != nil {
			return nil, fmt.Errorf("list the old storage: %w", err)
		}
		dst, err := list(ctx, to, prefix)
		if err != nil {
			return nil, fmt.Errorf("list the new storage: %w", err)
		}
		have := index(dst)
		var todo []task
		fresh := 0
		for _, ob := range src {
			if _, ok := seen[ob.Key]; ok {
				continue
			}
			seen[ob.Key] = ob
			fresh++
			m.res.Files++
			m.res.Bytes += ob.Size
			d, ok := have[ob.Key]
			e, journaled := j.get(ob.Key)
			switch {
			case ok && d.Size == ob.Size:
				if journaled && e.How != howPresent {
					m.res.Copied++
					m.res.CopiedBytes += ob.Size
				} else {
					m.res.Present++
					m.res.PresentBytes += ob.Size
				}
				if m.o.ReadBack && !(journaled && e.ReadBack) {
					todo = append(todo, task{ob: ob, readBack: true})
				} else if m.res.Sample == "" {
					m.res.Sample = ob.Key
				}
			case ok:
				m.problem(Problem{Key: ob.Key, Reason: Differs, Detail: fmt.Sprintf("the new storage has %d bytes, the old one %d", d.Size, ob.Size)})
			default:
				todo = append(todo, task{ob: ob})
			}
		}
		m.res.Passes = pass
		if pass > 1 && fresh == 0 {
			break
		}
		if err := m.copyAll(ctx, todo); err != nil {
			return m.result(), err
		}
		if len(todo) == 0 {
			break
		}
	}
	// Every object of the old store must now be in the new one.
	m.prog.phase("checking")
	dst, err := list(ctx, to, prefix)
	if err != nil {
		return m.result(), fmt.Errorf("list the new storage: %w", err)
	}
	have := index(dst)
	bad := map[string]bool{}
	for _, p := range m.problems {
		bad[p.Key] = true
	}
	missing := 0
	for k, ob := range seen {
		if bad[k] {
			continue
		}
		if d, ok := have[k]; !ok || d.Size != ob.Size {
			missing++
			m.problem(Problem{Key: k, Reason: Failed, Detail: "not in the new storage after copying"})
		}
	}
	res := m.result()
	res.Verified = len(res.Problems) == 0 && missing == 0
	if res.Verified {
		_ = os.RemoveAll(o.ScratchDir) // partial downloads of this move (its own files only)
	}
	return res, nil
}

func (m *mover) result() *Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := *m.res
	r.Problems = append([]Problem{}, m.problems...)
	sort.Slice(r.Problems, func(i, j int) bool { return r.Problems[i].Key < r.Problems[j].Key })
	return &r
}

func (m *mover) problem(p Problem) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.problems = append(m.problems, p)
}

func (m *mover) tooMany() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.problems) >= maxProblems
}

func (m *mover) copyAll(ctx context.Context, todo []task) error {
	if len(todo) == 0 {
		return nil
	}
	var bytes int64
	for _, t := range todo {
		bytes += t.ob.Size
	}
	m.prog.add(len(todo), bytes)
	m.prog.phase("copying")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	work := make(chan task)
	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once
	for range min(m.o.Parallel, len(todo)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range work {
				if err := m.one(ctx, t); err != nil {
					errOnce.Do(func() { firstErr = err; cancel() })
				}
			}
		}()
	}
feed:
	for _, t := range todo {
		if m.tooMany() {
			errOnce.Do(func() { firstErr = errors.New("too many files could not be copied") })
			break
		}
		select {
		case work <- t:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()
	m.j.flush()
	if firstErr == nil {
		firstErr = ctx.Err()
	}
	return firstErr
}

// one copies (or reads back) one object. Problems with the object itself are
// recorded and the move goes on; only a stop (ctx) or a local failure (the
// journal, this disk) ends it.
func (m *mover) one(ctx context.Context, t task) error {
	ob := t.ob
	kind, want := Classify(m.o.Workspace, ob.Key)
	if t.readBack {
		if kind == KindOther {
			// Nothing in the key names these bytes: compare with the old store's.
			want, _ = m.hashBytes(ctx, m.o.From, ob.Key)
		}
		got, err := m.hashStored(ctx, m.o.To, ob.Key, kind)
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case err != nil:
			m.problem(Problem{Key: ob.Key, Reason: Failed, Detail: "read back: " + err.Error()})
		case got != want:
			m.problem(Problem{Key: ob.Key, Reason: Differs, Detail: "the copy in the new storage doesn't match the original"})
		default:
			e, _ := m.j.get(ob.Key)
			if e.How == "" {
				e = entry{Key: ob.Key, Size: ob.Size, How: howPresent}
			}
			e.ReadBack = true
			if err := m.j.add(e); err != nil {
				return err
			}
			m.mu.Lock()
			m.res.ReadBack++
			if m.res.Sample == "" {
				m.res.Sample = ob.Key
			}
			m.mu.Unlock()
		}
		m.prog.fileDone(ob.Key, ob.Size)
		return nil
	}
	path, sum, temp, err := m.local(ctx, ob, kind, want)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var de *damage
		if errors.As(err, &de) {
			m.problem(Problem{Key: ob.Key, Reason: Damaged, Detail: de.msg})
			m.prog.fileDone(ob.Key, ob.Size)
			return nil
		}
		var le *localErr
		if errors.As(err, &le) {
			return le.err
		}
		m.problem(Problem{Key: ob.Key, Reason: Failed, Detail: err.Error()})
		m.prog.fileDone(ob.Key, ob.Size)
		return nil
	}
	ct := ob.ContentType
	if ct == "" {
		if st, err := m.o.From.Stat(ctx, ob.Key); err == nil {
			ct = st.ContentType
		}
	}
	if ct == "" && kind == KindLog {
		ct = "application/gzip"
	}
	info, err := m.o.To.PutFile(ctx, ob.Key, path, media.PutOptions{ContentType: ct, SHA256: sum}, m.resume(ob.Key), func(done, total int64, r media.Resume) {
		m.saveResume(ob.Key, r)
		m.prog.upload(ob.Key, done)
	})
	if err == nil && info.Size != ob.Size {
		err = fmt.Errorf("the new storage holds %d bytes, expected %d", info.Size, ob.Size)
	}
	if err == nil && (kind == KindOther || m.o.ReadBack) {
		// Nothing in the key proves these bytes: read them back.
		got, rerr := m.hashBytes(ctx, m.o.To, ob.Key)
		switch {
		case rerr != nil:
			err = fmt.Errorf("read back: %w", rerr)
		case got != sum:
			err = errors.New("the copy in the new storage doesn't match the one in the old storage")
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m.problem(Problem{Key: ob.Key, Reason: Failed, Detail: err.Error()})
		m.prog.fileDone(ob.Key, ob.Size)
		return nil
	}
	if err := m.j.add(entry{Key: ob.Key, Size: ob.Size, SHA256: sum, How: howCopied, ReadBack: kind == KindOther || m.o.ReadBack}); err != nil {
		return err
	}
	m.clearResume(ob.Key)
	if temp {
		_ = os.Remove(path)
	}
	m.mu.Lock()
	m.res.Copied++
	m.res.CopiedBytes += ob.Size
	m.res.CopiedNow++
	if m.res.Sample == "" {
		m.res.Sample = ob.Key
	}
	m.mu.Unlock()
	m.prog.fileDone(ob.Key, ob.Size)
	return nil
}

// damage is an object whose bytes do not match their content address.
type damage struct{ msg string }

func (d *damage) Error() string { return d.msg }

// localErr is a failure on this machine (its disk), which ends the move.
type localErr struct{ err error }

func (l *localErr) Error() string { return l.err.Error() }

// local returns a checked local file with the object's bytes and their
// SHA-256: this machine's copy when it has a good one, else a download
// (resumed when a previous run stopped part way). temp says the file is the
// move's own download.
func (m *mover) local(ctx context.Context, ob media.ObjectInfo, kind, want string) (path, sum string, temp bool, err error) {
	if p := localPath(m.o, ob); p != "" {
		if s, ok, err := check(p, kind, want); err == nil && ok {
			return p, s, false, nil
		}
		// A damaged local copy: the old store's bytes are the ones that count.
	}
	part := filepath.Join(m.o.ScratchDir, partName(ob.Key))
	if err := m.download(ctx, ob, part); err != nil {
		return "", "", false, err
	}
	s, ok, err := check(part, kind, want)
	if err != nil {
		return "", "", false, &localErr{err}
	}
	if !ok {
		_ = os.Remove(part) // the move's own download
		what := "its contents don't match their fingerprint"
		if kind == KindLog {
			what = "the text inside doesn't match its fingerprint"
		}
		return "", "", false, &damage{msg: "the old storage's copy is damaged: " + what}
	}
	return part, s, true, nil
}

func partName(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:12]) + ".part"
}

// download fetches an object into part, continuing a partial file.
func (m *mover) download(ctx context.Context, ob media.ObjectInfo, part string) error {
	if ob.Size > bigObject {
		m.bigMu.Lock()
		defer m.bigMu.Unlock()
	}
	var have int64
	if st, err := os.Stat(part); err == nil {
		have = st.Size()
		if have == ob.Size {
			return nil
		}
		if have > ob.Size {
			have = 0
		}
	}
	if free, ok := freeBytes(filepath.Dir(part)); ok && free-spareBytes < ob.Size-have {
		return &localErr{fmt.Errorf("not enough free space on this computer to copy a %s file (%s free)", human(ob.Size), human(max(free, 0)))}
	}
	flags := os.O_CREATE | os.O_WRONLY
	if have == 0 {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(part, flags, 0o600)
	if err != nil {
		return &localErr{err}
	}
	defer f.Close()
	if _, err := f.Seek(have, io.SeekStart); err != nil {
		return &localErr{err}
	}
	var rc io.ReadCloser
	if have == 0 {
		rc, _, err = m.o.From.Get(ctx, ob.Key, 0, -1)
	} else {
		rc, _, err = m.o.From.Get(ctx, ob.Key, have, -1)
	}
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer rc.Close()
	buf := make([]byte, 1<<20)
	done := have
	m.prog.download(ob.Key, done)
	for {
		n, rerr := rc.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return &localErr{werr}
			}
			done += int64(n)
			m.mu.Lock()
			m.res.Downloaded += int64(n)
			m.mu.Unlock()
			m.prog.download(ob.Key, done)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("download: %w", rerr)
		}
	}
	if err := f.Sync(); err != nil {
		return &localErr{err}
	}
	if done != ob.Size {
		return fmt.Errorf("download: got %d bytes, expected %d", done, ob.Size)
	}
	return nil
}

// check hashes a local file the way its key promises: the bytes (objects),
// the text inside (log texts). It returns the SHA-256 of the bytes and
// whether they match.
func check(path, kind, want string) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", false, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	switch kind {
	case KindObject:
		return sum, sum == want, nil
	case KindLog:
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return "", false, err
		}
		inner, err := gunzipSum(f)
		if err != nil {
			return sum, false, nil
		}
		return sum, inner == want, nil
	}
	return sum, true, nil
}

// gunzipSum is the SHA-256 of a gzip stream's contents (log texts are far
// below the limit; anything bigger is not one).
func gunzipSum(r io.Reader) (string, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(zr, 1<<30+1))
	if err != nil {
		return "", err
	}
	if n > 1<<30 {
		return "", errors.New("too large for a log text")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashStored reads an object back and hashes it as its key promises.
func (m *mover) hashStored(ctx context.Context, st media.Store, key, kind string) (string, error) {
	rc, _, err := st.Get(ctx, key, 0, -1)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	if kind == KindLog {
		return gunzipSum(rc)
	}
	h := sha256.New()
	if _, err := io.Copy(h, rc); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashBytes is the SHA-256 of an object's bytes, read back from a store.
func (m *mover) hashBytes(ctx context.Context, st media.Store, key string) (string, error) {
	return m.hashStored(ctx, st, key, KindObject)
}

func freeBytes(dir string) (int64, bool) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(dir, &s); err != nil {
		return 0, false
	}
	return int64(s.Bavail) * int64(s.Bsize), true
}

func human(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

/* ---------- unfinished uploads ---------- */

func resumePath(dir string) string { return filepath.Join(dir, "uploads.json") }

func loadResumes(dir string) map[string]media.Resume {
	out := map[string]media.Resume{}
	if b, err := os.ReadFile(resumePath(dir)); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func (m *mover) resume(key string) media.Resume {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.resumes[key]
}

// saveResume keeps an S3 multipart upload or a GCS session for the next run
// (the folder store resumes from its own staging file).
func (m *mover) saveResume(key string, r media.Resume) {
	if r.UploadID == "" && r.SessionURI == "" {
		return
	}
	m.mu.Lock()
	if old, ok := m.resumes[key]; ok && old == r {
		m.mu.Unlock()
		return
	}
	m.resumes[key] = r
	b, _ := json.Marshal(m.resumes)
	m.mu.Unlock()
	writeAtomic(resumePath(m.o.StateDir), b)
}

func (m *mover) clearResume(key string) {
	m.mu.Lock()
	if _, ok := m.resumes[key]; !ok {
		m.mu.Unlock()
		return
	}
	delete(m.resumes, key)
	b, _ := json.Marshal(m.resumes)
	m.mu.Unlock()
	writeAtomic(resumePath(m.o.StateDir), b)
}

func writeAtomic(p string, b []byte) {
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

/* ---------- the journal ---------- */

const (
	howCopied  = "copied"
	howPresent = "present" // was in the new store before the move (read back on request)
)

type entry struct {
	Key      string `json:"k"`
	Size     int64  `json:"n"`
	SHA256   string `json:"h,omitempty"`
	How      string `json:"v"`
	ReadBack bool   `json:"rb,omitempty"`
}

// journal records what this move copied and checked (JSON lines), so a
// resumed run reports the whole move and does not read anything back twice.
// The new store's listing stays the authority on what is there.
type journal struct {
	mu   sync.Mutex
	f    *os.File
	w    *bufio.Writer
	m    map[string]entry
	last time.Time
}

func openJournal(p string) (*journal, error) {
	j := &journal{m: map[string]entry{}}
	if f, err := os.Open(p); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var e entry
			if json.Unmarshal(sc.Bytes(), &e) == nil && e.Key != "" {
				j.m[e.Key] = e
			}
		}
		f.Close()
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	j.f, j.w = f, bufio.NewWriter(f)
	return j, nil
}

func (j *journal) get(key string) (entry, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	e, ok := j.m[key]
	return e, ok
}

func (j *journal) add(e entry) error {
	b, _ := json.Marshal(e)
	j.mu.Lock()
	defer j.mu.Unlock()
	j.m[e.Key] = e
	if _, err := j.w.Write(append(b, '\n')); err != nil {
		return err
	}
	if time.Since(j.last) > time.Second {
		j.last = time.Now()
		return j.w.Flush()
	}
	return nil
}

func (j *journal) flush() {
	j.mu.Lock()
	defer j.mu.Unlock()
	_ = j.w.Flush()
	_ = j.f.Sync()
}

func (j *journal) close() {
	j.flush()
	_ = j.f.Close()
}

/* ---------- progress ---------- */

// flight is an object being copied: an object that has to be downloaded
// first counts its download and its upload as half each.
type flight struct {
	down, up  int64
	downloads bool
}

type tracker struct {
	fn   func(Progress)
	fnMu sync.Mutex // one call at a time

	mu      sync.Mutex
	p       Progress
	partial map[string]*flight
	last    time.Time
	mark    time.Time
	markB   int64
}

func newTracker(fn func(Progress)) *tracker {
	return &tracker{fn: fn, partial: map[string]*flight{}, mark: time.Now()}
}

func (t *tracker) phase(p string) {
	t.mu.Lock()
	t.p.Phase = p
	t.mu.Unlock()
	t.emit(true)
}

func (t *tracker) add(files int, bytes int64) {
	t.mu.Lock()
	t.p.Files += files
	t.p.Bytes += bytes
	t.mu.Unlock()
}

func (t *tracker) flight(key string) *flight {
	f := t.partial[key]
	if f == nil {
		f = &flight{}
		t.partial[key] = f
	}
	return f
}

func (t *tracker) download(key string, done int64) {
	t.mu.Lock()
	f := t.flight(key)
	f.downloads, f.down = true, done
	t.mu.Unlock()
	t.emit(false)
}

func (t *tracker) upload(key string, done int64) {
	t.mu.Lock()
	t.flight(key).up = done
	t.mu.Unlock()
	t.emit(false)
}

func (t *tracker) fileDone(key string, size int64) {
	t.mu.Lock()
	delete(t.partial, key)
	t.p.FilesDone++
	t.p.BytesDone += size
	t.mu.Unlock()
	t.emit(true)
}

func (t *tracker) emit(force bool) {
	if t.fn == nil {
		return
	}
	t.mu.Lock()
	now := time.Now()
	if !force && now.Sub(t.last) < 200*time.Millisecond {
		t.mu.Unlock()
		return
	}
	t.last = now
	p := t.p
	for _, f := range t.partial {
		if f.downloads {
			p.BytesDone += (f.down + f.up) / 2
		} else {
			p.BytesDone += f.up
		}
	}
	p.BytesDone = min(p.BytesDone, p.Bytes)
	if el := now.Sub(t.mark).Seconds(); el >= 2 {
		t.p.Rate = max(float64(p.BytesDone-t.markB)/el, 0)
		t.mark, t.markB = now, p.BytesDone
	}
	p.Rate = t.p.Rate
	t.mu.Unlock()
	t.fnMu.Lock()
	defer t.fnMu.Unlock()
	t.fn(p)
}
