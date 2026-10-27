package cache

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/media"
)

// RepairProblem is one thing repair found.
type RepairProblem struct {
	SHA256  string `json:"sha256,omitempty"`
	Path    string `json:"path"`
	Problem string `json:"problem"` // corrupt | partial | stray | pinned-missing
	Action  string `json:"action"`  // removed | would-remove | refetched | would-refetch | refetch-failed | skipped-in-use
	Bytes   int64  `json:"bytes,omitempty"`
	Error   string `json:"error,omitempty"`
}

// RepairResult summarizes a repair pass.
type RepairResult struct {
	Checked        int             `json:"checked"`
	CheckedBytes   int64           `json:"checkedBytes"`
	OK             int             `json:"ok"`
	Corrupt        int             `json:"corrupt"`
	CorruptBytes   int64           `json:"corruptBytes"`
	Partials       int             `json:"partials"`
	PartialBytes   int64           `json:"partialBytes"`
	Strays         int             `json:"strays"`
	SkippedInUse   int             `json:"skippedInUse"`
	PinnedMissing  int             `json:"pinnedMissing"`
	Refetched      int             `json:"refetched"`
	RefetchFailed  int             `json:"refetchFailed"`
	Problems       []RepairProblem `json:"problems"`
	DryRun         bool            `json:"dryRun"`
	DurationSecond float64         `json:"durationSeconds"`
}

// strayAge is how long a temporary file must sit unmodified before repair
// treats it as abandoned (an Adopt copy in another process updates it while
// it runs).
const strayAge = 15 * time.Minute

// Repair re-hashes every cached object and removes corrupt ones, removes
// abandoned partial downloads and stray temporary files, and re-fetches pinned
// objects that are missing (refetch is called with each such hash; nil skips
// re-fetching). Partial downloads leased by a live process are left alone. With
// dryRun nothing is changed. progress, when set, is called before each object
// is hashed.
func (c *Cache) Repair(ctx context.Context, dryRun bool, refetch func(ctx context.Context, sha string) error, progress func(i, n int, sha string, size int64)) (RepairResult, error) {
	start := time.Now()
	res := RepairResult{DryRun: dryRun, Problems: []RepairProblem{}}
	act := func(real string) string {
		if dryRun {
			return map[string]string{"removed": "would-remove", "refetched": "would-refetch"}[real]
		}
		return real
	}
	rel := func(p string) string {
		if r, err := filepath.Rel(c.Dir, p); err == nil {
			return r
		}
		return p
	}
	type obj struct {
		path string
		sha  string
		size int64
	}
	var objs []obj
	inUse := c.InUse()
	_ = filepath.WalkDir(filepath.Join(c.Dir, "objects"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		name := d.Name()
		shard := filepath.Base(filepath.Dir(p))
		if validSHA(name) && shard == name[:2] {
			objs = append(objs, obj{p, name, info.Size()})
			return nil
		}
		// Temporary or misplaced file.
		if strings.HasSuffix(name, ".tmp") && time.Since(info.ModTime()) < strayAge {
			return nil // an Adopt copy may still be writing it
		}
		res.Strays++
		res.Problems = append(res.Problems, RepairProblem{Path: rel(p), Problem: "stray", Action: act("removed"), Bytes: info.Size()})
		if !dryRun {
			_ = os.Chmod(p, 0o644)
			_ = os.Remove(p)
		}
		return nil
	})
	sort.Slice(objs, func(i, j int) bool { return objs[i].path < objs[j].path })
	for i, o := range objs {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if progress != nil {
			progress(i+1, len(objs), o.sha, o.size)
		}
		unlock := c.lock(o.sha)
		got, err := media.HashFile(o.path)
		if err != nil {
			unlock()
			if errors.Is(err, fs.ErrNotExist) {
				continue // pruned meanwhile
			}
			return res, err
		}
		res.Checked++
		res.CheckedBytes += o.size
		if got == o.sha {
			res.OK++
			unlock()
			continue
		}
		res.Corrupt++
		res.CorruptBytes += o.size
		p := RepairProblem{SHA256: o.sha, Path: rel(o.path), Problem: "corrupt", Action: act("removed"), Bytes: o.size}
		if !dryRun {
			_ = os.Chmod(o.path, 0o644)
			if err := os.Remove(o.path); err != nil {
				p.Action, p.Error = "remove-failed", err.Error()
			}
		}
		unlock()
		res.Problems = append(res.Problems, p)
	}
	// Partial downloads nobody is working on.
	entries, _ := os.ReadDir(filepath.Join(c.Dir, "partial"))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(c.Dir, "partial", e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		if validSHA(e.Name()) && (inUse[e.Name()] || c.leasedNow(e.Name())) {
			res.SkippedInUse++
			res.Problems = append(res.Problems, RepairProblem{SHA256: e.Name(), Path: rel(p), Problem: "partial", Action: "skipped-in-use", Bytes: info.Size()})
			continue
		}
		res.Partials++
		res.PartialBytes += info.Size()
		res.Problems = append(res.Problems, RepairProblem{SHA256: e.Name(), Path: rel(p), Problem: "partial", Action: act("removed"), Bytes: info.Size()})
		if !dryRun {
			_ = os.Remove(p)
		}
	}
	// Pinned objects must be present.
	var pinned []string
	for sha := range c.pins() {
		pinned = append(pinned, sha)
	}
	sort.Strings(pinned)
	for _, sha := range pinned {
		if _, err := os.Stat(c.Path(sha)); err == nil {
			continue
		}
		res.PinnedMissing++
		p := RepairProblem{SHA256: sha, Path: rel(c.Path(sha)), Problem: "pinned-missing", Action: act("refetched")}
		if !dryRun {
			switch {
			case refetch == nil:
				p.Action = "refetch-failed"
				p.Error = "no canonical store available"
				res.RefetchFailed++
			default:
				if err := refetch(ctx, sha); err != nil {
					p.Action, p.Error = "refetch-failed", err.Error()
					res.RefetchFailed++
				} else {
					res.Refetched++
					if st, err := os.Stat(c.Path(sha)); err == nil {
						p.Bytes = st.Size()
					}
				}
			}
		}
		res.Problems = append(res.Problems, p)
	}
	res.DurationSecond = time.Since(start).Seconds()
	return res, nil
}
