package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/recorder"
)

// Screen recordings (D258): the browser starts a recording, then sends each
// track's pieces as they are recorded — an append at the host's offset, like
// the resumable uploads but with no length until the end (HEAD says the
// offset after a dropped connection or a reload) — and the clock it measured.
// Finishing, linking and cuts are ops (recording.*).

func (s *Server) recordingRoutes(api *mux.Router) {
	api.HandleFunc("/recordings", s.recordingCreate).Methods("POST")
	api.HandleFunc("/recordings/{id}/clock", s.recordingClock).Methods("POST")
	api.HandleFunc("/recordings/{id}/tracks/{track}", s.recordingOffset).Methods("HEAD")
	api.HandleFunc("/recordings/{id}/tracks/{track}", s.recordingPiece).Methods("PATCH")
}

func (s *Server) recordingCreate(w http.ResponseWriter, r *http.Request) {
	var in recorder.RecordingCreateIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeErr(w, apperr.New(apperr.InvalidInput, "MetaRush couldn't read what the recording is."))
		return
	}
	rec, err := recorder.Create(r.Context(), s.appFor(r, "dashboard"), &in, accessOf(r).public)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, rec)
}

func (s *Server) recordingClock(w http.ResponseWriter, r *http.Request) {
	var in recorder.RecordingClockIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeErr(w, apperr.New(apperr.InvalidInput, "MetaRush couldn't read the recording's clock."))
		return
	}
	rec, err := recorder.SetClock(r.Context(), s.appFor(r, "dashboard"), mux.Vars(r)["id"], &in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, rec)
}

func (s *Server) recordingOffset(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
	a := s.appFor(r, "dashboard")
	rec, err := recorder.Get(r.Context(), a, v["id"])
	if err != nil || rec.Track(v["track"]) == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(recorder.Offset(a, rec.ID, v["track"]), 10))
	w.Header().Set("Recording-State", rec.State)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) recordingPiece(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
	off, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || off < 0 {
		writeErr(w, apperr.New(apperr.InvalidInput, "A piece of the recording needs its place in the file (Upload-Offset)."))
		return
	}
	n, err := recorder.Append(r.Context(), s.appFor(r, "dashboard"), v["id"], v["track"], off, r.ContentLength, r.Body)
	w.Header().Set("Upload-Offset", strconv.FormatInt(n, 10))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, recorder.RecordingPieceOut{Offset: n})
}
