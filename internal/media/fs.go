package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FS stores objects under a root directory. It is the solo/local store, or a
// shared store when Root is a mount every collaborator reaches (NAS). Separate
// laptops' disks are never a shared store.
type FS struct {
	Root   string
	Shared bool
}

// NewFS creates the root and staging directories.
func NewFS(root string, shared bool) (*FS, error) {
	if root == "" {
		return nil, errors.New("filesystem store needs a root directory")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(abs, ".staging"), 0o755); err != nil {
		return nil, err
	}
	return &FS{Root: abs, Shared: shared}, nil
}

func (f *FS) Name() string    { return "filesystem" }
func (f *FS) Locator() string { return "fs:" + f.Root }
func (f *FS) Capabilities() Capabilities {
	return Capabilities{RangeRead: true, PresignedGet: false, ResumableUpload: true, Shared: f.Shared}
}

func (f *FS) path(key string) (string, error) {
	if err := ValidKey(key); err != nil {
		return "", err
	}
	p := filepath.Join(f.Root, filepath.FromSlash(key))
	rel, err := filepath.Rel(f.Root, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("key escapes store root: %q", key)
	}
	return p, nil
}

func (f *FS) stagingPath(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(f.Root, ".staging", hex.EncodeToString(sum[:8])+".part")
}

// PutFile copies path into the store, appending to a previous partial copy when one
// exists (resume), then atomically renames it into place.
func (f *FS) PutFile(ctx context.Context, key, path string, opts PutOptions, r Resume, progress Progress) (ObjectInfo, error) {
	dst, err := f.path(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	src, err := os.Open(path)
	if err != nil {
		return ObjectInfo{}, err
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return ObjectInfo{}, err
	}
	total := st.Size()
	if info, err := f.Stat(ctx, key); err == nil && info.Size == total {
		if progress != nil {
			progress(total, total, r)
		}
		return info, nil // content-addressed: already present
	}
	part := f.stagingPath(key)
	var offset int64
	if pst, err := os.Stat(part); err == nil && pst.Size() <= total {
		offset = pst.Size()
	}
	out, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return ObjectInfo{}, err
	}
	if _, err := out.Seek(offset, io.SeekStart); err != nil {
		out.Close()
		return ObjectInfo{}, err
	}
	if _, err := src.Seek(offset, io.SeekStart); err != nil {
		out.Close()
		return ObjectInfo{}, err
	}
	buf := make([]byte, 4<<20)
	done := offset
	for {
		if err := ctx.Err(); err != nil {
			out.Close()
			return ObjectInfo{}, err
		}
		n, rerr := src.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return ObjectInfo{}, werr
			}
			done += int64(n)
			if progress != nil {
				progress(done, total, Resume{PartsDone: int(done >> 20)})
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return ObjectInfo{}, rerr
		}
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return ObjectInfo{}, err
	}
	if err := out.Close(); err != nil {
		return ObjectInfo{}, err
	}
	if opts.SHA256 != "" {
		got, err := hashFile(part)
		if err != nil {
			return ObjectInfo{}, err
		}
		if got != opts.SHA256 {
			os.Remove(part)
			return ObjectInfo{}, fmt.Errorf("integrity check failed for %s: sha256 %s != %s", key, got, opts.SHA256)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return ObjectInfo{}, err
	}
	if err := os.Rename(part, dst); err != nil {
		return ObjectInfo{}, err
	}
	return f.Stat(ctx, key)
}

func (f *FS) Get(ctx context.Context, key string, offset, length int64) (io.ReadCloser, ObjectInfo, error) {
	p, err := f.path(key)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	file, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ObjectInfo{}, ErrNotFound
	}
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	st, _ := file.Stat()
	info := ObjectInfo{Key: key, Size: st.Size(), ModTime: st.ModTime()}
	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			file.Close()
			return nil, ObjectInfo{}, err
		}
	}
	if length < 0 {
		return file, info, nil
	}
	return struct {
		io.Reader
		io.Closer
	}{io.LimitReader(file, length), file}, info, nil
}

func (f *FS) Stat(_ context.Context, key string) (ObjectInfo, error) {
	p, err := f.path(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	st, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return ObjectInfo{}, ErrNotFound
	}
	if err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{Key: key, Size: st.Size(), ModTime: st.ModTime(), ETag: fmt.Sprintf("%d-%d", st.Size(), st.ModTime().UnixNano())}, nil
}

// LocalPath exposes the file path for zero-copy serving (filesystem driver only).
func (f *FS) LocalPath(key string) (string, error) { return f.path(key) }

func (f *FS) Delete(_ context.Context, key string) error {
	p, err := f.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (f *FS) PresignGet(context.Context, string, time.Duration, string, string) (string, error) {
	return "", ErrUnsupported
}

func (f *FS) ListIncomplete(context.Context) ([]Incomplete, error) {
	entries, err := os.ReadDir(filepath.Join(f.Root, ".staging"))
	if err != nil {
		return nil, err
	}
	var out []Incomplete
	for _, e := range entries {
		if info, err := e.Info(); err == nil && strings.HasSuffix(e.Name(), ".part") {
			out = append(out, Incomplete{Key: e.Name(), UploadID: e.Name(), Initiated: info.ModTime()})
		}
	}
	return out, nil
}

func (f *FS) AbortIncomplete(_ context.Context, in Incomplete) error {
	if strings.ContainsAny(in.UploadID, `/\`) {
		return fmt.Errorf("invalid staging name %q", in.UploadID)
	}
	return os.Remove(filepath.Join(f.Root, ".staging", in.UploadID))
}

// HashFile returns the hex SHA-256 of a file.
func HashFile(path string) (string, error) { return hashFile(path) }

func hashFile(path string) (string, error) {
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
