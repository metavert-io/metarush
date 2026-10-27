package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gorilla/mux"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/derive"
	"github.com/metavert-io/metarush/internal/models"
)

// frameSlots caps how many frames are cut at once (a page of sample tiles asks
// for many; each is one short ffmpeg run).
var frameSlots = make(chan struct{}, 3)

// assetFrame serves one still of a video (/media/assets/{id}/frame?t=12.3&w=320):
// sample tiles show the moment a voice speaks with it. Frames are cut from the
// locally cached preview copy (or original) and kept in scratch space, keyed by
// the content hash, the time (to 0.1 s) and the width.
func (s *Server) assetFrame(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	as, ok := s.asset(w, r, mux.Vars(r)["id"], "MetaRush can't find that clip.")
	if !ok {
		return
	}
	if !as.HasVideo || !as.Ready() {
		writeErr(w, apperr.New(apperr.NotFound, "“%s” has no picture.", as.Title))
		return
	}
	t, _ := strconv.ParseFloat(r.URL.Query().Get("t"), 64)
	if math.IsNaN(t) || t < 0 {
		t = 0
	}
	t = math.Round(t*10) / 10
	if d := as.Duration; d > 0 && t > d-0.05 {
		// The last 0.1 s step that still shows a picture (clamping before
		// rounding could land back on the very end, where there is no frame).
		t = math.Max(0, math.Floor((d-0.05)*10)/10)
	}
	width, _ := strconv.Atoi(r.URL.Query().Get("w"))
	switch {
	case width <= 0:
		width = 320
	case width < 64:
		width = 64
	case width > 960:
		width = 960
	}
	width = (width + 31) / 32 * 32
	key := sha256.Sum256([]byte(fmt.Sprintf("%s|%.1f|%d", as.Original.SHA256, t, width)))
	dir := filepath.Join(s.App.Cfg.Scratch.Dir, "frames")
	out := filepath.Join(dir, hex.EncodeToString(key[:16])+".jpg")
	if _, err := os.Stat(out); err != nil {
		select {
		case frameSlots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-frameSlots }()
		if _, err := os.Stat(out); err != nil { // another request may have made it meanwhile
			src := ""
			if px, ok := as.Variants[models.VariantProxy]; ok {
				if p, err := s.App.Cache.Ensure(ctx, s.App.Media, px.Key, px.SHA256, px.Size, nil); err == nil {
					src = p
				}
			}
			if src == "" {
				p, err := s.App.Cache.Ensure(ctx, s.App.Media, as.Original.Key, as.Original.SHA256, as.Original.Size, nil)
				if err != nil {
					writeErr(w, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get the footage for this still."))
					return
				}
				src = p
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				writeErr(w, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't make a folder for the still."))
				return
			}
			tmp := out + ".part.jpg"
			if err := (&derive.Deriver{T: s.App.Tools}).Frame(ctx, src, tmp, t, width); err != nil {
				_ = os.Remove(tmp)
				writeErr(w, apperr.Wrap(apperr.ToolFailed, err, "MetaRush couldn't take a still from the footage."))
				return
			}
			if err := os.Rename(tmp, out); err != nil {
				writeErr(w, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't save the still."))
				return
			}
		}
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	serveFile(w, r, out, "frame.jpg")
}
