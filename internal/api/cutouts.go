package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/mattes"
)

// Clips' cut-outs for the dashboard (D510): same-origin and local-first (the
// cache, else the media store), never a redirect.
//
//	/media/cutouts/{sha}       a cut-out by its file's SHA-256 (a preview plan's cutOutSources)
//	/media/cutouts/preview?…   one moment of a clip with its background replaced, as a JPEG
//	                           (mattes.Preview: assetId, at, with, colour, blur, picture,
//	                           footage, footageAt, feather, fixed, projectId, width)

func (s *Server) cutOutMedia(w http.ResponseWriter, r *http.Request) {
	sha := mux.Vars(r)["sha"]
	if len(sha) != 64 || strings.Trim(sha, "0123456789abcdef") != "" {
		writeErr(w, apperr.New(apperr.NotFound, "MetaRush can't find that cut-out."))
		return
	}
	m, err := mattes.BySHA(r.Context(), s.App, sha)
	if err != nil {
		writeErr(w, err)
		return
	}
	if m == nil || m.File == nil {
		writeErr(w, apperr.New(apperr.NotFound, "MetaRush can't find that cut-out."))
		return
	}
	p, err := s.App.Cache.Ensure(r.Context(), s.App.Media, m.File.Key, m.File.SHA256, m.File.Size, nil)
	if err != nil {
		writeErr(w, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get the clip's cut-out from its storage."))
		return
	}
	w.Header().Set("Content-Type", "video/webm")
	w.Header().Set("Cache-Control", "private, max-age=86400") // a cut-out's bytes never change
	serveFile(w, r, p, "cut-out.webm")
}

func (s *Server) cutOutPreview(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	num := func(k string) float64 {
		v, _ := strconv.ParseFloat(q.Get(k), 64)
		return v
	}
	width, _ := strconv.Atoi(q.Get("width"))
	in := &mattes.CutoutPreviewIn{AssetID: q.Get("assetId"), ProjectID: q.Get("projectId"), At: num("at"), Fixed: q.Get("fixed"), With: q.Get("with"),
		Colour: q.Get("colour"), Blur: num("blur"), Picture: q.Get("picture"), Footage: q.Get("footage"), FootageAt: num("footageAt"), Feather: num("feather"), Width: width}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	data, _, err := mattes.Preview(ctx, s.appFor(r, "dashboard"), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "private, max-age=600")
	_, _ = w.Write(data)
}
