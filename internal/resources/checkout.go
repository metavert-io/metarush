package resources

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
)

// Checkouts: a repository version unpacked on this machine, read-only, for
// Claude to read (`metarush resources path <repo>`) and for code slides.
// Nothing in it is ever run; it holds only the snapshot's text files.

// checkoutMarker records which version a checkout folder holds.
const checkoutMarker = ".metarush-snapshot"

// RepoCheckout returns a read-only folder with a repository version's files
// (sha "" = the current version), unpacking it from the media store once.
func RepoCheckout(ctx context.Context, a *app.App, r *models.Resource, sha string) (string, *models.ResourceVersion, error) {
	if r.Kind != models.ResourceRepo {
		return "", nil, apperr.New(apperr.InvalidInput, "%s isn't a repository.", r.Title)
	}
	v := r.Latest()
	if sha != "" {
		v = r.Version(sha)
	}
	if v == nil || v.Repo == nil {
		return "", nil, missingVersion(r, sha)
	}
	root := filepath.Join(a.Cfg.Cache.Dir, "repos")
	dir := filepath.Join(root, r.Name+"-"+v.SHA256[:16])
	if b, err := os.ReadFile(filepath.Join(dir, checkoutMarker)); err == nil && strings.TrimSpace(string(b)) == v.SHA256 {
		return dir, v, nil
	}
	tarPath, err := a.Cache.Ensure(ctx, a.Media, v.StorageKey, v.SHA256, v.Size, nil)
	if err != nil {
		return "", v, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get the saved copy of %s from its storage.", r.Title)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", v, err
	}
	tmp, err := os.MkdirTemp(root, ".unpack-")
	if err != nil {
		return "", v, err
	}
	defer func() { _ = makeWritable(tmp); _ = os.RemoveAll(tmp) }()
	if err := unpackSnapshot(tarPath, tmp); err != nil {
		return "", v, fmt.Errorf("unpack the snapshot of %s: %w", r.Title, err)
	}
	if err := os.WriteFile(filepath.Join(tmp, checkoutMarker), []byte(v.SHA256+"\n"), 0o444); err != nil {
		return "", v, err
	}
	if err := makeReadOnly(tmp); err != nil {
		return "", v, err
	}
	// Replace an older or broken folder in one step.
	if _, err := os.Stat(dir); err == nil {
		_ = makeWritable(dir)
		_ = os.RemoveAll(dir)
	}
	if err := os.Rename(tmp, dir); err != nil {
		if b, err2 := os.ReadFile(filepath.Join(dir, checkoutMarker)); err2 == nil && strings.TrimSpace(string(b)) == v.SHA256 {
			return dir, v, nil // another process won the race
		}
		return "", v, err
	}
	return dir, v, nil
}

// unpackSnapshot writes a snapshot's regular files under dir. Anything that
// isn't a plain file with a clean relative name is refused.
func unpackSnapshot(tarPath, dir string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || !safeRepoPath(h.Name) {
			return fmt.Errorf("the snapshot holds %q, which isn't a plain file", h.Name)
		}
		total += h.Size
		if total > 4<<30 {
			return errors.New("the snapshot is larger than it should be")
		}
		dst := filepath.Join(dir, filepath.FromSlash(h.Name))
		if !strings.HasPrefix(dst, dir+string(os.PathSeparator)) {
			return fmt.Errorf("%q points outside the checkout", h.Name)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(tr, h.Size))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
}

// makeReadOnly makes every file 0444, so nobody edits a snapshot by
// accident (folders stay writable, so the cache can still remove it).
func makeReadOnly(dir string) error {
	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		return os.Chmod(p, 0o444)
	})
}

func makeWritable(dir string) error {
	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return os.Chmod(p, 0o755)
		}
		return nil
	})
}

// RepoFile reads one file of a repository version (a clean relative path).
func RepoFile(ctx context.Context, a *app.App, r *models.Resource, sha, file string) ([]byte, *models.ResourceVersion, error) {
	file = strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(strings.TrimSpace(file), "\\", "/")), "/")
	if !safeRepoPath(file) {
		return nil, nil, apperr.New(apperr.InvalidInput, "%q isn't a file in a repository.", file)
	}
	dir, v, err := RepoCheckout(ctx, a, r, sha)
	if err != nil {
		return nil, v, err
	}
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file)))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, v, apperr.New(apperr.NotFound, "%s at %s has no file %s (it may have been left out: large files and files that aren't text are).", r.Title, models.ShortSHA(v.Repo.Commit), file)
		}
		return nil, v, err
	}
	return b, v, nil
}

// ReadOverview loads a repository version's full overview (file tree, README…).
func ReadOverview(ctx context.Context, a *app.App, v *models.ResourceVersion) (*RepoOverview, error) {
	if v == nil || v.Repo == nil || v.Repo.Overview == nil {
		return nil, apperr.New(apperr.NotFound, "this version has no overview")
	}
	o := v.Repo.Overview
	p, err := a.Cache.Ensure(ctx, a.Media, o.StorageKey, o.SHA256, o.Size, nil)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var ov RepoOverview
	if err := json.Unmarshal(b, &ov); err != nil {
		return nil, err
	}
	return &ov, nil
}
