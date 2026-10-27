package api

import (
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/gorilla/mux"

	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/media"
)

// avatarMedia serves a person's picture (D184) to signed-in browsers: the
// stored 256-pixel PNG, from the local cache.
func (s *Server) avatarMedia(w http.ResponseWriter, r *http.Request) {
	u, err := auth.GetUser(r.Context(), s.App, mux.Vars(r)["id"])
	if err != nil || u.Avatar == nil || u.Avatar.SHA256 == "" {
		http.Error(w, "No picture.", http.StatusNotFound)
		return
	}
	key := media.ObjectKey(s.App.WorkspaceID(), u.Avatar.SHA256)
	path, err := s.App.Cache.Ensure(r.Context(), s.App.Media, key, u.Avatar.SHA256, u.Avatar.Size, nil)
	if err != nil {
		http.Error(w, "MetaRush couldn't get that picture from its storage.", http.StatusServiceUnavailable)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "No picture.", http.StatusNotFound)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("Content-Length", strconv.FormatInt(u.Avatar.Size, 10))
	h.Set("Cache-Control", "private, max-age=86400")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, f)
}
