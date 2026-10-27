package api

import (
	"bytes"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gorilla/mux"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/remotion"
	"github.com/metavert-io/metarush/internal/store"
)

// remotionMedia serves the pictures a person reviews for a version of a
// Remotion source (D137): the before/after stills of its check and a picture
// of each composition — drawn in the render jail, served as images only, so
// none of the project's code ever runs in the dashboard. Same-origin, from
// the cache.
//
//	/media/remotion/{hash}/still/{n}/{before|after}
//	/media/remotion/{hash}/poster/{composition}
//	/media/remotion/{hash}/scene/{composition}/{n}     (the map's still of scene n, D167)
//	/media/remotion/{hash}/words/{change}-{n}/{side}   (a change of words, checked with this version; change 0: only checked, D169)
//	/media/remotion/{hash}/pictures/{change}-{n}/{side}   (a change of pictures, the same way, D209)
//	/media/remotion/{hash}/original/{NAME}   (the project's own picture a setting replaces, D209: an SVG
//	                                          cleaned first, served with a sandboxing policy like resources)
func (s *Server) remotionMedia(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
	hash := v["hash"]
	if !remotionHash.MatchString(hash) {
		writeErr(w, apperr.New(apperr.NotFound, "MetaRush can't find that version of a Remotion project."))
		return
	}
	var ver models.RemotionVersion
	if err := s.App.DB.GetByID(r.Context(), store.CRemotionVersions, s.App.WorkspaceID(), s.App.WorkspaceID()+"/"+hash, &ver); err != nil {
		writeErr(w, apperr.New(apperr.NotFound, "MetaRush can't find that version of a Remotion project."))
		return
	}
	if v["kind"] == "original" {
		b, mime, err := remotion.PictureOriginal(r.Context(), s.App, &ver, v["n"])
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Cache-Control", "private, max-age=3600")
		w.Header().Set("Content-Security-Policy", resourceCSP)
		http.ServeContent(w, r, "", ver.CreatedAt, bytes.NewReader(b))
		return
	}
	var obj *models.RemotionObject
	name := "still.jpg"
	switch v["kind"] {
	case "still":
		n, err := strconv.Atoi(v["n"])
		if err == nil && ver.Check != nil && n >= 0 && n < len(ver.Check.Stills) {
			st := &ver.Check.Stills[n]
			switch v["side"] {
			case "after":
				obj = &st.After
			case "before":
				obj = st.Before
			}
			name = st.Composition + "-" + v["side"] + ".jpg"
		}
	case "poster":
		if c := ver.Composition(v["n"]); c != nil {
			obj, name = c.Poster, c.ID+".jpg"
		}
	case "words", "pictures":
		c, i, ok := strings.Cut(v["n"], "-")
		cn, err1 := strconv.Atoi(c)
		in, err2 := strconv.Atoi(i)
		if ok && err1 == nil && err2 == nil && (v["side"] == "before" || v["side"] == "after") {
			if v["kind"] == "words" {
				obj = remotion.WordsStill(r.Context(), s.App, &ver, cn, in, v["side"])
			} else {
				obj = remotion.PicturesStill(r.Context(), s.App, &ver, cn, in, v["side"])
			}
			name = v["kind"] + "-" + v["n"] + "-" + v["side"] + ".jpg"
		}
	case "scene":
		// A scene's still in the version's map (D167): /scene/{composition}/{n}.
		if n, err := strconv.Atoi(v["side"]); err == nil {
			if o, err := remotion.MapStill(r.Context(), s.App, &ver, v["n"], n); err == nil {
				obj, name = o, v["n"]+"-scene-"+v["side"]+".jpg"
			}
		}
	}
	if obj == nil || obj.Key == "" {
		writeErr(w, apperr.New(apperr.NotFound, "This version has no picture like that."))
		return
	}
	p, err := s.App.Cache.Ensure(r.Context(), s.App.Media, obj.Key, obj.SHA256, obj.Size, nil)
	if err != nil {
		writeErr(w, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get that picture from its storage."))
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; sandbox")
	serveFile(w, r, p, name)
}

var remotionHash = regexp.MustCompile(`^[0-9a-f]{64}$`)
