package api

import (
	"errors"
	"net/http"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/playback"
)

// mediaFile is one stored file of a source: its original or a fixed copy.
type mediaFile struct {
	key, sha string
	size     int64
}

// mediaOf picks the media a request asks for (D152): ?media=original is the
// original file and its variants even while a fixed copy is current (a
// version that plays the original), ?media=<sha> one fixed copy (a version
// that pins it), and no parameter the media the footage uses now. It reads the
// clip's playback record (D180), so the current media and the original play
// from what MetaRush remembers while the database doesn't answer; another
// fixed copy is read from the database (playback.CopyFor). nil with a nil
// error when that copy isn't known or ready; the error says the database
// didn't answer (or the request ended).
func mediaOf(r *http.Request, s *Server, as *playback.Asset) (*mediaFile, map[string]playback.Object, string, error) {
	switch m := r.URL.Query().Get("media"); {
	case m == "" || (m != "original" && as.Copy != nil && m == as.Copy.SHA256):
		cur := as.Original
		if as.Copy != nil {
			cur = *as.Copy
			if cur.MIME == "" {
				cur.MIME = as.Original.MIME
			}
		}
		return &mediaFile{cur.Key, cur.SHA256, cur.Size}, as.Variants, cur.MIME, nil
	case m == "original":
		vs := as.Variants
		if as.Copy != nil {
			vs = as.OriginalVariants
		}
		return &mediaFile{as.Original.Key, as.Original.SHA256, as.Original.Size}, vs, as.Original.MIME, nil
	default:
		c, err := playback.CopyFor(r.Context(), s.App, as.ID, m)
		if errors.Is(err, playback.ErrNotFound) || (err == nil && c.State != models.FixReady) {
			return nil, nil, "", nil
		}
		if err != nil {
			return nil, nil, "", err
		}
		return &mediaFile{c.StorageKey, c.SHA256, c.Size}, playback.Objects(c.Variants), c.MIME, nil
	}
}
