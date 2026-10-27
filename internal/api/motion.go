package api

import (
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/gorilla/mux"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
)

// motionMedia serves what a person reviews before approving a motion graphic
// (D68): its contact sheet, test frames and preview video — pictures and
// video only, never the component's code, so nothing it wrote runs in the
// dashboard. Local-first and same-origin: fetched into the cache, never a
// redirect.
//
//	/media/motion/{hash}/sheet      the contact sheet (JPEG)
//	/media/motion/{hash}/preview    the preview video (MP4)
//	/media/motion/{hash}/preview-{shape}  its preview in another shape it was checked in (D164)
//	/media/motion/{hash}/frame-{n}  test frame n (PNG, from 0)
//	/media/motion/{hash}/still-{n}  test frame n without the check's backdrop: the live preview's still (D187)
func (s *Server) motionMedia(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
	hash := v["hash"]
	if !motion.ValidHash(hash) {
		writeErr(w, apperr.New(apperr.NotFound, "MetaRush can't find that motion graphic."))
		return
	}
	c, err := motion.Get(r.Context(), s.App.DB, s.App.WorkspaceID(), hash)
	if err != nil {
		writeErr(w, apperr.New(apperr.NotFound, "MetaRush can't find that motion graphic."))
		return
	}
	var obj *models.MotionObject
	mime, name := "", ""
	switch kind := v["kind"]; {
	case kind == "sheet":
		obj, mime, name = c.Sheet, "image/jpeg", c.Name+"-frames.jpg"
	case kind == "preview":
		obj, mime, name = c.Video, "video/mp4", c.Name+"-preview.mp4"
	case strings.HasPrefix(kind, "preview-"):
		// The preview in another shape it was checked in (D164).
		for i := range c.Shapes {
			if c.Shapes[i].Label == strings.TrimPrefix(kind, "preview-") && c.Shapes[i].Video != nil {
				obj, mime, name = c.Shapes[i].Video, "video/mp4", c.Name+"-"+c.Shapes[i].Label+"-preview.mp4"
			}
		}
	case strings.HasPrefix(kind, "frame-"):
		n, err := strconv.Atoi(strings.TrimPrefix(kind, "frame-"))
		if err == nil && n >= 0 && n < len(c.Frames) {
			obj, mime, name = &c.Frames[n].Object, "image/png", c.Name+"-frame-"+strconv.Itoa(n)+".png"
		}
	case strings.HasPrefix(kind, "still-"):
		// The live preview's still (D187): test frame n with the check's
		// backdrop taken out, so it sits over the video in the graphic's place.
		n, err := strconv.Atoi(strings.TrimPrefix(kind, "still-"))
		if err != nil || n < 0 || n >= len(c.Frames) || c.Frames[n].Object.Key == "" {
			writeErr(w, apperr.New(apperr.NotFound, "This motion graphic has no %s.", motionKindWords(kind)))
			return
		}
		s.motionStill(w, r, c, n)
		return
	}
	if obj == nil || obj.Key == "" {
		writeErr(w, apperr.New(apperr.NotFound, "This motion graphic has no %s.", motionKindWords(v["kind"])))
		return
	}
	p, err := s.App.Cache.Ensure(r.Context(), s.App.Media, obj.Key, obj.SHA256, obj.Size, nil)
	if err != nil {
		writeErr(w, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get the motion graphic's %s from its storage.", motionKindWords(v["kind"])))
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; media-src 'self'; sandbox")
	serveFile(w, r, p, name)
}

// motionKindWords names what was asked for (sheet, preview, frame-N) in plain words.
func motionKindWords(kind string) string {
	switch {
	case kind == "sheet":
		return "test frames"
	case kind == "preview" || strings.HasPrefix(kind, "preview-"):
		return "preview"
	case strings.HasPrefix(kind, "frame-"), strings.HasPrefix(kind, "still-"):
		return "test frame like that"
	}
	return "picture like that"
}

// motionStill serves test frame n of a checked version as the preview's
// still (D187): pixels only, made from the frame the jail drew, kept in
// memory by the frame's SHA-256 (frames never change) and cached by the
// browser. Signed in and same-origin like every other picture.
func (s *Server) motionStill(w http.ResponseWriter, r *http.Request, c *models.MotionComponent, n int) {
	obj := c.Frames[n].Object
	etag := `"still1-` + obj.SHA256 + `"`
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("Cache-Control", "private, max-age=86400")
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; sandbox")
	h.Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body, ok := stills.get(obj.SHA256)
	if !ok {
		p, err := s.App.Cache.Ensure(r.Context(), s.App.Media, obj.Key, obj.SHA256, obj.Size, nil)
		if err != nil {
			writeErr(w, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get the motion graphic's test frame from its storage."))
			return
		}
		f, err := os.Open(p)
		if err != nil {
			writeErr(w, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't read the motion graphic's test frame."))
			return
		}
		body, err = motion.Still(f)
		f.Close()
		if err != nil {
			writeErr(w, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't make a still of the motion graphic's test frame."))
			return
		}
		stills.put(obj.SHA256, body)
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

// stillCache keeps the latest preview stills in memory (least recently used
// out): a cut shows the same few graphics over and over.
type stillCache struct {
	mu    sync.Mutex
	order []string
	byKey map[string][]byte
}

var stills = &stillCache{byKey: map[string][]byte{}}

const stillCacheSize = 48

func (c *stillCache) get(k string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.byKey[k]
	if ok {
		c.order = append(slices.DeleteFunc(c.order, func(x string) bool { return x == k }), k)
	}
	return b, ok
}

func (c *stillCache) put(k string, b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.byKey[k]; !ok {
		c.order = append(c.order, k)
	}
	c.byKey[k] = b
	for len(c.order) > stillCacheSize {
		delete(c.byKey, c.order[0])
		c.order = c.order[1:]
	}
}
