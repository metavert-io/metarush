package wsarchive

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/cache"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/storagemove"
)

// ListMedia lists every object a workspace keeps in a store, by key, with
// what its key promises (storagemove's kinds; an object's SHA-256 is its
// content address).
func ListMedia(ctx context.Context, st media.Store, ws string) ([]MediaEntry, error) {
	l, ok := st.(media.Lister)
	if !ok {
		return nil, fmt.Errorf("MetaRush can't list the files in that storage (%s)", st.Name())
	}
	prefix := storagemove.Prefix(ws)
	var out []MediaEntry
	err := l.List(ctx, prefix, func(oi media.ObjectInfo) error {
		if media.ValidKey(oi.Key) != nil || !strings.HasPrefix(oi.Key, prefix) {
			return nil
		}
		kind, sha := storagemove.Classify(ws, oi.Key)
		e := MediaEntry{Key: oi.Key, Size: oi.Size, Kind: kind}
		if kind == storagemove.KindObject {
			e.SHA256 = sha
		}
		out = append(out, e)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, err
}

// writeMediaIndex writes media.jsonl and returns its digest.
func writeMediaIndex(path string, entries []MediaEntry) (File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return File{}, err
	}
	h := sha256.New()
	w := bufio.NewWriter(io.MultiWriter(f, h))
	var n int64
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			f.Close()
			return File{}, err
		}
		b = append(b, '\n')
		m, err := w.Write(b)
		n += int64(m)
		if err != nil {
			f.Close()
			return File{}, err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return File{}, err
	}
	if err := f.Close(); err != nil {
		return File{}, err
	}
	return File{Path: MediaIndexName, Bytes: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// readMediaIndex reads media.jsonl.
func readMediaIndex(path string) ([]MediaEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []MediaEntry
	br := bufio.NewReaderSize(f, 1<<16)
	for n := 1; ; n++ {
		line, err := readLine(br)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(string(line)) == "" {
			continue
		}
		var e MediaEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", MediaIndexName, n, err)
		}
		if media.ValidKey(e.Key) != nil {
			return nil, fmt.Errorf("%s line %d names a file outside the workspace's media", MediaIndexName, n)
		}
		out = append(out, e)
	}
	return out, nil
}

// includeOptions copy a workspace's media into an archive.
type includeOptions struct {
	Workspace            string
	From                 media.Store
	Dir                  string // the archive's media folder (a folder store)
	Cache                *cache.Cache
	StateDir, ScratchDir string
	Progress             func(storagemove.Progress)
}

// includeMedia copies every object of the workspace into the archive's
// media folder with storagemove (checked by content address on the way in,
// resumable), then lists what the folder holds with each file's SHA-256.
func includeMedia(ctx context.Context, o includeOptions) ([]MediaEntry, *storagemove.Result, error) {
	to, err := media.NewFS(o.Dir, false)
	if err != nil {
		return nil, nil, err
	}
	res, err := storagemove.Run(ctx, storagemove.Options{Workspace: o.Workspace, From: o.From, To: to, Cache: o.Cache,
		StateDir: o.StateDir, ScratchDir: o.ScratchDir, Progress: o.Progress})
	if err != nil {
		return nil, res, err
	}
	if !res.Verified {
		return nil, res, fmt.Errorf("%s couldn't be copied into the archive", plural(int64(len(res.Problems)), "file", "files"))
	}
	_ = os.Remove(filepath.Join(o.Dir, ".staging")) // the folder store's own staging folder, empty once every copy is in
	entries, err := ListMedia(ctx, to, o.Workspace)
	if err != nil {
		return nil, res, err
	}
	for i, e := range entries {
		if e.Kind == storagemove.KindObject {
			continue // its key is its SHA-256, checked as it was copied
		}
		sum, err := fileSHA256(filepath.Join(o.Dir, filepath.FromSlash(e.Key)))
		if err != nil {
			return nil, res, err
		}
		entries[i].SHA256 = sum
	}
	return entries, res, nil
}

// fileSHA256 hashes a file.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// privateTree makes every folder under root 0700 and every file 0600: an
// archive holds everything about the work, people, consent and transcripts
// included.
func privateTree(root string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		mode := os.FileMode(0o600)
		if d.IsDir() {
			mode = 0o700
		}
		return os.Chmod(p, mode)
	})
}

// mediaCheck is what a target storage has of an archive's referenced media.
type mediaCheck struct {
	Present int
	Missing []MediaEntry
	Differ  []MediaEntry
}

// checkReferenced looks for each object of the index in the target storage
// (by listing it once).
func checkReferenced(ctx context.Context, st media.Store, ws string, want []MediaEntry) (*mediaCheck, error) {
	have, err := ListMedia(ctx, st, ws)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]int64, len(have))
	for _, h := range have {
		byKey[h.Key] = h.Size
	}
	c := &mediaCheck{}
	for _, e := range want {
		size, ok := byKey[e.Key]
		switch {
		case !ok:
			c.Missing = append(c.Missing, e)
		case size != e.Size:
			c.Differ = append(c.Differ, e)
		default:
			c.Present++
		}
	}
	return c, nil
}
