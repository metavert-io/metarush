package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ingest"
)

// The native folder picker and "Open in Finder" act on this machine's screen,
// so they are LocalOnly ops (not reachable through /api/ops). The signed-in
// dashboard of this daemon (127.0.0.1, session cookie, CSRF header) gets these
// two narrow endpoints that call the same functions. A browser on another
// computer (remote access) never opens the picker: it would wait on a screen
// no one may be looking at (the dashboard browses the folders instead).

// pickerElsewhere refuses the folder picker for a browser on another computer.
func (s *Server) pickerElsewhere(w http.ResponseWriter, r *http.Request) bool {
	if !accessOf(r).public {
		return false
	}
	writeErr(w, apperr.New(apperr.NotConfigured, "The folder picker opens on the screen of %s, so it works only there.", s.App.Cfg.MachineName()).
		WithHint("Browse the folders instead, or send the files from this computer.").WithDetails(map[string]any{"reason": "elsewhere"}))
	return true
}

func (s *Server) watchChoose(w http.ResponseWriter, r *http.Request) {
	// Choosing the watched folder is changing it (watch.set is owner-only, D191).
	if s.ownerOnly(w, r, "watch.set") || s.pickerElsewhere(w, r) {
		return
	}
	var in ingest.WatchChooseIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&in); err != nil && err != io.EOF {
		writeErr(w, apperr.Wrap(apperr.InvalidInput, err, "MetaRush couldn't read the request."))
		return
	}
	a := *s.appFor(r, "dashboard")
	out, err := ingest.ChooseFolder(r.Context(), &a, &in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, out)
}

func (s *Server) watchReveal(w http.ResponseWriter, r *http.Request) {
	var in ingest.WatchRevealIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&in); err != nil && err != io.EOF {
		writeErr(w, apperr.Wrap(apperr.InvalidInput, err, "MetaRush couldn't read the request."))
		return
	}
	a := *s.appFor(r, "dashboard")
	out, err := ingest.RevealFolder(r.Context(), &a, &in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, out)
}
