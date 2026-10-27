package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// voicePreview streams a voice's sample through the daemon: the browser never
// talks to the provider, the API key never leaves this process, and the
// provider's preview files are fetched without it.
//
//	GET /api/providers/{name}/voices/{voice}/preview[?library=1]
func (s *Server) voicePreview(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
	name, voice := v["name"], v["voice"]
	if name != "elevenlabs" && name != "local" {
		writeErr(w, apperr.New(apperr.NotFound, "MetaRush doesn't know that voice service.").WithDetails(map[string]any{"provider": name}))
		return
	}
	if len(voice) == 0 || len(voice) > 120 {
		writeErr(w, apperr.New(apperr.InvalidInput, "MetaRush can't tell which voice to play."))
		return
	}
	library, _ := strconv.ParseBool(r.URL.Query().Get("library"))
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	a := *s.appFor(r, "dashboard")
	data, ct, err := voiceover.PreviewAudio(ctx, &a, name, voice, library)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(data)
}
