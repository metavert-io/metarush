package remotion

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
)

// Every file of every version is an object in the media store, addressed by
// its SHA-256 (D131), so every machine of the workspace can read and render
// any version, and a file shared by versions is stored once.

func objectOf(a *app.App, sha string, size int64) models.RemotionObject {
	return models.RemotionObject{Key: media.ObjectKey(a.WorkspaceID(), sha), SHA256: sha, Size: size}
}

// putFile stores a local file whose SHA-256 is known (nothing is sent when
// the store has it already) and adopts it into this machine's cache.
func putFile(ctx context.Context, a *app.App, path, sha string, size int64, mime string) (models.RemotionObject, error) {
	obj := objectOf(a, sha, size)
	if info, err := a.Media.Stat(ctx, obj.Key); err != nil || info.Size != size {
		if mime == "" {
			mime = "application/octet-stream"
		}
		if _, err := a.Media.PutFile(ctx, obj.Key, path, media.PutOptions{ContentType: mime, SHA256: sha}, media.Resume{}, nil); err != nil {
			return obj, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't save a file of the Remotion project in its storage. Try again in a moment.")
		}
	}
	if !a.Cache.Has(sha, size) {
		_, _ = a.Cache.Adopt(path, sha)
	}
	return obj, nil
}

// putBytes stores bytes by their content.
func putBytes(ctx context.Context, a *app.App, data []byte, mime string) (models.RemotionObject, error) {
	sha := sha256Hex(data)
	if a.Cache.Has(sha, int64(len(data))) {
		if info, err := a.Media.Stat(ctx, media.ObjectKey(a.WorkspaceID(), sha)); err == nil && info.Size == int64(len(data)) {
			return objectOf(a, sha, int64(len(data))), nil
		}
	}
	if err := os.MkdirAll(a.Cfg.Scratch.Dir, 0o755); err != nil {
		return models.RemotionObject{}, err
	}
	f, err := os.CreateTemp(a.Cfg.Scratch.Dir, "remotion-object-")
	if err != nil {
		return models.RemotionObject{}, err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return models.RemotionObject{}, err
	}
	if err := f.Close(); err != nil {
		return models.RemotionObject{}, err
	}
	return putFile(ctx, a, name, sha, int64(len(data)), mime)
}

// putLocal stores a file whose hash isn't known yet.
func putLocal(ctx context.Context, a *app.App, path, mime string) (models.RemotionObject, error) {
	sha, err := media.HashFile(path)
	if err != nil {
		return models.RemotionObject{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return models.RemotionObject{}, err
	}
	return putFile(ctx, a, path, sha, st.Size(), mime)
}

// localObject is a verified local copy of an object (from the cache, fetched
// when this machine doesn't have it).
func localObject(ctx context.Context, a *app.App, sha string, size int64) (string, error) {
	if !ValidHash(sha) {
		return "", apperr.New(apperr.NotFound, "MetaRush doesn't have that file of the Remotion project.")
	}
	p, err := a.Cache.Ensure(ctx, a.Media, media.ObjectKey(a.WorkspaceID(), sha), sha, size, nil)
	if err != nil {
		return "", apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get a file of the Remotion project from its storage.")
	}
	return p, nil
}

// readObject reads a small object (a manifest).
func readObject(ctx context.Context, a *app.App, sha string, size int64) ([]byte, error) {
	p, err := localObject(ctx, a, sha, size)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// uploadFiles stores a snapshot's files, a few at a time.
func uploadFiles(ctx context.Context, a *app.App, files []snapFile, progress func(done, total int)) error {
	type job struct{ f snapFile }
	jobs := make(chan job)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	done := 0
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				mu.Lock()
				stop := firstErr != nil
				mu.Unlock()
				if stop {
					continue
				}
				_, err := putFile(ctx, a, j.f.local, j.f.SHA256, j.f.Size, mimeOf(j.f.Path))
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
				}
				done++
				if progress != nil {
					progress(done, len(files))
				}
				mu.Unlock()
			}
		}()
	}
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f.SHA256] {
			mu.Lock()
			done++
			mu.Unlock()
			continue
		}
		seen[f.SHA256] = true
		select {
		case jobs <- job{f}:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

func mimeOf(p string) string {
	switch filepath.Ext(p) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".mp4":
		return "video/mp4"
	case ".json":
		return "application/json"
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".md", ".txt", ".css":
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}
