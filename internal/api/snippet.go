package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gorilla/mux"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/derive"
	"github.com/metavert-io/metarush/internal/models"
)

// assetSnippet serves a short listening clip of an asset's audio
// (/media/assets/{id}/snippet?from=12.3&to=15.8, at most 15 s): the speaker
// and to-do views play voices with it. Clips are cut from the locally cached
// proxy (or original) and kept in scratch space.
func (s *Server) assetSnippet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	as, ok := s.asset(w, r, mux.Vars(r)["id"], "MetaRush can't find that clip.")
	if !ok {
		return
	}
	from, err1 := strconv.ParseFloat(r.URL.Query().Get("from"), 64)
	to, err2 := strconv.ParseFloat(r.URL.Query().Get("to"), 64)
	if err1 != nil || err2 != nil || from < 0 || to <= from || to-from > derive.MaxSnippet || (as.Duration > 0 && from >= as.Duration) {
		writeErr(w, apperr.New(apperr.InvalidInput, "MetaRush plays up to %.0f seconds of a clip at a time, from inside the clip.", derive.MaxSnippet))
		return
	}
	if !as.HasAudio || !as.Ready() {
		writeErr(w, apperr.New(apperr.NotFound, "“%s” has no sound to play.", as.Title))
		return
	}
	key := sha256.Sum256([]byte(fmt.Sprintf("%s|%.3f|%.3f", as.Original.SHA256, from, to)))
	dir := filepath.Join(s.App.Cfg.Scratch.Dir, "snippets")
	out := filepath.Join(dir, hex.EncodeToString(key[:16])+".wav")
	if _, err := os.Stat(out); err != nil {
		src := ""
		if px, ok := as.Variants[models.VariantProxy]; ok && as.HasVideo {
			if p, err := s.App.Cache.Ensure(ctx, s.App.Media, px.Key, px.SHA256, px.Size, nil); err == nil {
				src = p
			}
		}
		if src == "" {
			p, err := s.App.Cache.Ensure(ctx, s.App.Media, as.Original.Key, as.Original.SHA256, as.Original.Size, nil)
			if err != nil {
				writeErr(w, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get the footage to play this part."))
				return
			}
			src = p
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			writeErr(w, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't make a folder for this part of the sound."))
			return
		}
		tmp := out + ".part.wav"
		if err := (&derive.Deriver{T: s.App.Tools}).AudioSnippet(ctx, src, tmp, from, to); err != nil {
			_ = os.Remove(tmp)
			writeErr(w, apperr.Wrap(apperr.ToolFailed, err, "MetaRush couldn't cut out this part of the sound."))
			return
		}
		if err := os.Rename(tmp, out); err != nil {
			writeErr(w, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't save this part of the sound."))
			return
		}
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	serveFile(w, r, out, "snippet.wav")
}
