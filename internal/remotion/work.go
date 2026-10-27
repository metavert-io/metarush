package remotion

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	rnd "github.com/metavert-io/metarush/internal/renderer"
)

// Work trees (per machine): a version's files laid out from the cache
// (clones or links: no extra space), with node_modules pointing at the
// version's package environment, and next to it the bundle MetaRush built
// from it. <cache>/remotion/<hash>/{work,bundle-<key>}; the oldest are
// removed when there are more than keepWork.

const (
	workDone = ".metarush-work"
	keepWork = 6
)

func workRoot(a *app.App) string { return filepath.Join(a.Cfg.Cache.Dir, "remotion") }

// loadManifest reads a version's manifest (verified against the version).
func loadManifest(ctx context.Context, a *app.App, v *models.RemotionVersion) (*Manifest, error) {
	b, err := readObject(ctx, a, v.Manifest.SHA256, v.Manifest.Size)
	if err != nil {
		return nil, err
	}
	return ParseManifest(b, v.Hash)
}

// workTree lays a version out on this machine (once) and links its packages.
// The returned release ends its use (prune leaves a tree in use alone).
func workTree(ctx context.Context, a *app.App, v *models.RemotionVersion, m *Manifest, env string) (string, func(), error) {
	base := filepath.Join(workRoot(a), v.Hash)
	dir := filepath.Join(base, "work")
	lk, err := lockShared(filepath.Join(base, ".lock"))
	if err != nil {
		return "", nil, err
	}
	fail := func(err error) (string, func(), error) {
		lk.release()
		return "", nil, err
	}
	now := time.Now()
	_ = os.Chtimes(base, now, now)
	if !exists(filepath.Join(dir, workDone)) {
		if err := lk.relock(syscall.LOCK_EX); err != nil {
			return fail(err)
		}
		if !exists(filepath.Join(dir, workDone)) {
			_ = os.RemoveAll(dir)
			for _, f := range m.Files {
				src, err := localObject(ctx, a, f.SHA256, f.Size)
				if err != nil {
					return fail(err)
				}
				if err := placeFile(src, filepath.Join(dir, filepath.FromSlash(f.Path))); err != nil {
					return fail(err)
				}
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fail(err)
			}
			if err := os.WriteFile(filepath.Join(dir, workDone), []byte(v.Hash), 0o644); err != nil {
				return fail(err)
			}
		}
		if err := lk.relock(syscall.LOCK_SH); err != nil {
			return fail(err)
		}
		pruneWork(a, v.Hash)
	}
	if env != "" {
		link := filepath.Join(dir, "node_modules")
		target := filepath.Join(env, "node_modules")
		if cur, err := os.Readlink(link); err != nil || cur != target {
			_ = os.RemoveAll(link)
			if err := os.Symlink(target, link); err != nil && !os.IsExist(err) {
				return fail(err)
			}
		}
	}
	return dir, lk.release, nil
}

// bundleDir is where a version's bundle lives: its key covers MetaRush's
// renderer package (the bundler settings and loaders) and the fonts.
func bundleDir(a *app.App, v *models.RemotionVersion) string {
	digest := "dev"
	if pkg, err := rnd.Embedded(); err == nil {
		digest = pkg.Digest()[:12]
	}
	var fonts []string
	for _, f := range v.Fonts {
		for _, x := range f.Files {
			fonts = append(fonts, x.Path+"="+x.SHA256)
		}
	}
	sort.Strings(fonts)
	key := sha256Hex([]byte("source-bundle/1\n" + digest + "\n" + v.Lock + "\n" + strings.Join(fonts, "\n")))[:16]
	return filepath.Join(workRoot(a), v.Hash, "bundle-"+key)
}

// pruneWork removes the least recently used work trees beyond keepWork.
func pruneWork(a *app.App, keep string) {
	entries, err := os.ReadDir(workRoot(a))
	if err != nil {
		return
	}
	type d struct {
		name string
		at   time.Time
	}
	var ds []d
	for _, e := range entries {
		if !e.IsDir() || !ValidHash(e.Name()) {
			continue
		}
		if info, err := e.Info(); err == nil {
			ds = append(ds, d{e.Name(), info.ModTime()})
		}
	}
	if len(ds) <= keepWork {
		return
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].at.After(ds[j].at) })
	for _, x := range ds[keepWork:] {
		if x.name == keep || time.Since(x.at) < time.Hour {
			continue
		}
		p := filepath.Join(workRoot(a), x.name)
		if lk, err := tryLockFile(filepath.Join(p, ".lock")); err == nil && lk != nil {
			_ = os.RemoveAll(p)
			lk.release()
		}
	}
}
