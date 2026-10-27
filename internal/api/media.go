package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/playback"
)

type object struct {
	key, sha, mime, name string
	size                 int64
}

// recordHeader says, on a media answer, that the clip's or render's record
// came from the memory (D180: the database didn't answer).
const recordHeader = "X-MetaRush-Record"

// asset is what a media request needs from a clip's record (D180): what the
// database says, or, when it can't answer, what MetaRush remembers it said.
// The session and Host checks ran before; the caller applies the same rules
// to either. It writes the error itself (ok false): a clip the database
// doesn't have is notFound; a database that didn't answer, with nothing
// remembered, is a plain E_DATABASE, quickly.
func (s *Server) asset(w http.ResponseWriter, r *http.Request, id, notFound string) (*playback.Asset, bool) {
	as, from, err := playback.AssetFor(r.Context(), s.App, id)
	s.answered(w, from, err)
	if err != nil {
		lookupFailed(w, err, notFound)
		return nil, false
	}
	return as, true
}

// render is asset's counterpart for renders.
func (s *Server) render(w http.ResponseWriter, r *http.Request, id, notFound string) (*playback.Render, bool) {
	rd, from, err := playback.RenderFor(r.Context(), s.App, id)
	s.answered(w, from, err)
	if err != nil {
		lookupFailed(w, err, notFound)
		return nil, false
	}
	return rd, true
}

func lookupFailed(w http.ResponseWriter, err error, notFound string) {
	if errors.Is(err, playback.ErrNotFound) {
		writeErr(w, apperr.New(apperr.NotFound, "%s", notFound))
		return
	}
	writeErr(w, err) // E_DATABASE in plain words, or the request ended
}

// answered marks a media answer that came from the memory and tells the
// daemon's log once when the database stops and starts answering again.
func (s *Server) answered(w http.ResponseWriter, from playback.From, err error) {
	down := from == playback.FromMemory || apperr.Is(err, apperr.Database)
	if from == playback.FromMemory {
		w.Header().Set(recordHeader, "memory")
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return // the browser stopped asking: that says nothing about the database
	}
	if s.mediaDown.Swap(down) == down {
		return
	}
	if down {
		s.Log("The database isn't answering: clips and renders play from what MetaRush remembers about them (%d remembered), new ones wait for the database.", playback.Remembered(s.App))
	} else {
		s.Log("The database answers again: clips and renders play as usual.")
	}
}

func (s *Server) assetMedia(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
	as, ok := s.asset(w, r, v["id"], "MetaRush can't find that clip.")
	if !ok {
		return
	}
	// Which media (D152): ?media=original is the original even while a fixed
	// copy is current, ?media=<sha> one fixed copy (a version that pins it);
	// without it, the media the footage uses now.
	file, variants, mime, err := mediaOf(r, s, as)
	if err != nil {
		s.answered(w, playback.FromDatabase, err)
		writeErr(w, err) // E_DATABASE in plain words, or the request ended
		return
	}
	if file == nil {
		writeErr(w, apperr.New(apperr.NotFound, "MetaRush can't find that copy of “%s”.", as.Title).WithDetails(map[string]any{"media": r.URL.Query().Get("media")}))
		return
	}
	var o object
	switch v["variant"] {
	case "original":
		if !as.Ready() {
			writeErr(w, apperr.New(apperr.Conflict, "“%s” isn't ready to play yet.", as.Title).WithDetails(map[string]any{"state": as.State}))
			return
		}
		o = object{file.key, file.sha, mime, as.Name, file.size}
	default:
		vr, ok := variants[v["variant"]]
		if !ok {
			writeErr(w, apperr.New(apperr.NotFound, "“%s” has no %s yet.", as.Title, variantWords(v["variant"])).WithDetails(map[string]any{"variant": v["variant"]}))
			return
		}
		o = object{vr.Key, vr.SHA256, vr.MIME, as.Name + "." + vr.Kind, vr.Size}
	}
	download := r.URL.Query().Get("download") == "1"
	// Playback sources (proxies, and originals a preview plays) are served from
	// this machine: from the cache once present, otherwise streamed through the
	// daemon while the cache fills in the background. A redirect to a presigned
	// object-store URL makes the browser's media elements buffer and re-seek,
	// which the preview player hears as repeated words.
	if !download && (v["variant"] == models.VariantProxy || r.URL.Query().Get("preview") == "1") {
		if !ops.LocalMedia(s.App, o.key, o.sha, o.size) && o.size > 0 {
			ops.CacheInBackground(s.App, o.key, o.sha, o.size)
		}
		s.serveLocalFirst(w, r, o)
		return
	}
	s.serveObject(w, r, o, download)
}

// serveLocalFirst serves from the cache or a local store, else streams ranges
// through the daemon (same origin, never a redirect).
func (s *Server) serveLocalFirst(w http.ResponseWriter, r *http.Request, o object) {
	if o.mime != "" {
		w.Header().Set("Content-Type", o.mime)
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if s.App.Cache.Has(o.sha, o.size) {
		s.App.Cache.Touch(o.sha)
		serveFile(w, r, s.App.Cache.Path(o.sha), o.name)
		return
	}
	if fsStore, ok := s.App.Media.(*media.FS); ok {
		if p, err := fsStore.LocalPath(o.key); err == nil {
			serveFile(w, r, p, o.name)
			return
		}
	}
	s.streamRange(w, r, o)
}

func (s *Server) renderMedia(w http.ResponseWriter, r *http.Request) {
	const notReady = "That render isn't ready to watch."
	rd, ok := s.render(w, r, mux.Vars(r)["id"], notReady)
	if !ok {
		return
	}
	if rd.Output == nil {
		writeErr(w, apperr.New(apperr.NotFound, notReady))
		return
	}
	name := fmt.Sprintf("metarush-r%d-%s-%s.mp4", rd.RevisionNumber, rd.Preset, rd.ID[max(0, len(rd.ID)-6):])
	s.serveObject(w, r, object{rd.Output.Key, rd.Output.SHA256, rd.Output.MIME, name, rd.Output.Size}, r.URL.Query().Get("download") == "1")
}

func (s *Server) renderThumb(w http.ResponseWriter, r *http.Request) {
	const none = "That render has no picture yet."
	rd, ok := s.render(w, r, mux.Vars(r)["id"], none)
	if !ok {
		return
	}
	if rd.Output == nil || rd.ThumbnailKey == "" {
		writeErr(w, apperr.New(apperr.NotFound, none))
		return
	}
	k := rd.ThumbnailKey
	s.serveObject(w, r, object{k, k[strings.LastIndex(k, "/")+1:], "image/jpeg", "thumb.jpg", -1}, false)
}

// serveObject streams from the local cache when present (Range via
// http.ServeContent), redirects to a short-lived presigned URL for object stores,
// or streams ranges from the store. Files are never buffered in memory.
func (s *Server) serveObject(w http.ResponseWriter, r *http.Request, o object, download bool) {
	if download {
		w.Header().Set("Content-Disposition", attachment(o.name))
	}
	if o.mime != "" {
		w.Header().Set("Content-Type", o.mime)
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if s.App.Cache.Has(o.sha, o.size) {
		s.App.Cache.Touch(o.sha) // viewing counts as a use (LRU pruning)
		serveFile(w, r, s.App.Cache.Path(o.sha), o.name)
		return
	}
	if fsStore, ok := s.App.Media.(*media.FS); ok {
		if p, err := fsStore.LocalPath(o.key); err == nil {
			serveFile(w, r, p, o.name)
			return
		}
	}
	if s.App.Media.Capabilities().PresignedGet {
		dl := ""
		if download {
			dl = o.name
		}
		u, err := s.App.Media.PresignGet(r.Context(), o.key, 15*time.Minute, o.mime, dl)
		if err == nil {
			http.Redirect(w, r, u, http.StatusFound)
			return
		}
	}
	s.streamRange(w, r, o)
}

// variantWords names a stored copy of a clip the way people do.
func variantWords(v string) string {
	switch v {
	case models.VariantProxy:
		return "preview copy"
	case models.VariantMezzanine:
		return "steady copy"
	case models.VariantThumbnail, models.VariantContactSheet:
		return "pictures"
	case models.VariantWaveform, models.VariantPeaks:
		return "sound picture"
	}
	return "copy like that"
}

func serveFile(w http.ResponseWriter, r *http.Request, p, name string) {
	f, err := os.Open(p)
	if err != nil {
		writeErr(w, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't open this machine's copy of the file."))
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	http.ServeContent(w, r, name, st.ModTime(), f)
}

func (s *Server) streamRange(w http.ResponseWriter, r *http.Request, o object) {
	var start, length int64 = 0, -1
	status := http.StatusOK
	if rh := r.Header.Get("Range"); strings.HasPrefix(rh, "bytes=") && !strings.Contains(rh, ",") {
		a, b, _ := strings.Cut(strings.TrimPrefix(rh, "bytes="), "-")
		if v, err := strconv.ParseInt(a, 10, 64); err == nil {
			start = v
			if e, err := strconv.ParseInt(b, 10, 64); err == nil && e >= v {
				length = e - v + 1
			}
			status = http.StatusPartialContent
		}
	}
	rc, info, err := s.App.Media.Get(r.Context(), o.key, start, length)
	if errors.Is(err, media.ErrNotFound) {
		writeErr(w, apperr.New(apperr.NotFound, "The file is missing from MetaRush's storage."))
		return
	}
	if err != nil {
		writeErr(w, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't read the file from its storage."))
		return
	}
	defer rc.Close()
	total := info.Size
	if o.size > 0 {
		total = o.size
	}
	w.Header().Set("Accept-Ranges", "bytes")
	if status == http.StatusPartialContent {
		end := total - 1
		if length > 0 {
			end = start + length - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, total))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	} else if total > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(total, 10))
	}
	w.WriteHeader(status)
	if r.Method == "HEAD" {
		return
	}
	_, _ = io.Copy(w, rc)
}
