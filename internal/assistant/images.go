package assistant

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/metavert-io/metarush/internal/apperr"
)

const maxImageBytes = 25 << 20

var imageTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true, "image/gif": true}

// OpenImage opens an image Claude looked at, for the dashboard's thumbnails.
// Only images inside the scratch folder (frames, contact sheets), the media
// cache (precomputed contact sheets, stored by content hash) or the assistant
// folder are served: the path must be relative and clean, may not leave its
// root (also not through symlinks), must name an image file (cache objects have
// no extension) and its content must sniff as an image. The caller closes the file.
func (m *Manager) OpenImage(root, rel string) (*os.File, string, int64, error) {
	var base string
	switch root {
	case "scratch":
		base = m.App.Cfg.Scratch.Dir
	case "assistant":
		base = m.dir()
	case "cache":
		base = m.App.Cfg.Cache.Dir
	default:
		return nil, "", 0, apperr.New(apperr.InvalidInput, "MetaRush can't show that picture.").WithDetails(map[string]any{"root": root})
	}
	bad := apperr.New(apperr.InvalidInput, "That isn't a picture MetaRush can show.")
	switch {
	case rel == "" || len(rel) > 512 || strings.ContainsAny(rel, "\\\x00") || strings.HasPrefix(rel, "/"):
		return nil, "", 0, bad
	case path.Clean(rel) != rel || rel == "." || strings.HasPrefix(rel, "../") || rel == ".." || strings.Contains(rel, "/../"):
		return nil, "", 0, bad
	case root != "cache" && !imageExt[strings.ToLower(path.Ext(rel))]:
		return nil, "", 0, bad
	}
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return nil, "", 0, imageNotFound()
	}
	full, err := filepath.EvalSymlinks(filepath.Join(realBase, filepath.FromSlash(rel)))
	if err != nil {
		return nil, "", 0, imageNotFound()
	}
	if r, err := filepath.Rel(realBase, full); err != nil || r == "." || strings.HasPrefix(r, "..") || filepath.IsAbs(r) {
		return nil, "", 0, bad
	}
	st, err := os.Lstat(full)
	if err != nil || !st.Mode().IsRegular() {
		return nil, "", 0, imageNotFound()
	}
	if st.Size() > maxImageBytes {
		return nil, "", 0, apperr.New(apperr.InvalidInput, "That picture is too large to show here.")
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, "", 0, imageNotFound()
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		f.Close()
		return nil, "", 0, imageNotFound()
	}
	ct := http.DetectContentType(head[:n])
	if ct == "application/octet-stream" && strings.EqualFold(path.Ext(rel), ".webp") && n >= 12 && string(head[8:12]) == "WEBP" {
		ct = "image/webp"
	}
	if !imageTypes[ct] {
		f.Close()
		return nil, "", 0, bad
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, "", 0, apperr.New(apperr.Internal, "MetaRush couldn't read that picture.")
	}
	return f, ct, st.Size(), nil
}

func imageNotFound() error {
	return apperr.New(apperr.NotFound, "MetaRush can't find that picture any more.")
}
