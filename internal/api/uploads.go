package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/uploads"
)

// Browser uploads use a small resumable protocol (tus-like): create → PATCH chunks
// at Upload-Offset → HEAD to learn the offset after a network hiccup. Completed
// files go through exactly the same ingest pipeline as the CLI. Records live in
// internal/uploads (one JSON file each) so a reloaded dashboard rebuilds its
// tray from server state and a re-dropped file resumes at the server's offset.

const maxChunk = 64 << 20

// UploadList is GET /api/uploads: active uploads plus recently finished ones.
type UploadList struct {
	Uploads []uploads.Record `json:"uploads"`
}

// UploadCreateIn is POST /api/uploads.
type UploadCreateIn struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ProjectID   string `json:"projectId,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

type uploadAPI struct {
	s  *Server
	st *uploads.Store

	evMu   sync.Mutex
	lastEv map[string]time.Time
	lastSt map[string]string
	locks  sync.Map // upload ID → *sync.Mutex
}

func newUploads(s *Server) *uploadAPI {
	u := &uploadAPI{s: s, st: uploads.For(s.App.Cfg.StateDir()), lastEv: map[string]time.Time{}, lastSt: map[string]string{}}
	u.st.OnChange = u.changed
	return u
}

// changed announces record changes over SSE: always on a state or stage change,
// otherwise at most once a second per upload.
func (u *uploadAPI) changed(r uploads.Record) {
	key := r.State + "/" + r.Stage
	u.evMu.Lock()
	send := u.lastSt[r.ID] != key || time.Since(u.lastEv[r.ID]) >= time.Second
	if send {
		u.lastSt[r.ID], u.lastEv[r.ID] = key, time.Now()
	}
	u.evMu.Unlock()
	if send {
		u.s.broker.Local(Event{Type: "upload", ID: r.ID, ProjectID: r.ProjectID})
	}
}

func (u *uploadAPI) list(w http.ResponseWriter, r *http.Request) {
	recs := u.st.List(30 * time.Minute)
	if recs == nil {
		recs = []uploads.Record{}
	}
	writeOK(w, UploadList{Uploads: recs})
}

func (u *uploadAPI) create(w http.ResponseWriter, r *http.Request) {
	var in UploadCreateIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil || in.Size <= 0 || strings.TrimSpace(in.Name) == "" {
		writeErr(w, apperr.New(apperr.InvalidInput, "MetaRush needs the file's name and size to start the upload."))
		return
	}
	if in.Size > 500<<30 {
		writeErr(w, apperr.New(apperr.InvalidInput, "This file is larger than 500 GB, more than MetaRush takes in one file."))
		return
	}
	if len(in.Fingerprint) > 80 {
		writeErr(w, apperr.New(apperr.InvalidInput, "MetaRush couldn't read this file's fingerprint. Drop the file again."))
		return
	}
	// A path from any computer (C:\Users\…\clip.mov, a folder's Trip/clip.mov) is its last part.
	name := filepath.Base(strings.ReplaceAll(in.Name, "\\", "/"))
	if name == "" || name == "." || name == "/" || name == ".." {
		writeErr(w, apperr.New(apperr.InvalidInput, "MetaRush needs the file's name to start the upload."))
		return
	}
	rec, err := u.st.Create(name, in.Size, in.ProjectID, in.Fingerprint)
	if err != nil {
		writeErr(w, err)
		return
	}
	if p, ok := personOf(r); ok && p.UserID != "" {
		rec, _ = u.st.Update(rec.ID, true, func(x *uploads.Record) { x.UserID, x.UserName = p.UserID, p.UserName })
	}
	writeOK(w, rec)
}

func (u *uploadAPI) head(w http.ResponseWriter, r *http.Request) {
	rec, ok := u.st.Get(mux.Vars(r)["id"])
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(rec.Offset, 10))
	w.Header().Set("Upload-Length", strconv.FormatInt(rec.Size, 10))
	w.Header().Set("Upload-State", rec.State)
	w.WriteHeader(http.StatusNoContent)
}

func (u *uploadAPI) get(w http.ResponseWriter, r *http.Request) {
	rec, ok := u.st.Get(mux.Vars(r)["id"])
	if !ok {
		writeErr(w, uploadNotFound())
		return
	}
	writeOK(w, rec)
}

func (u *uploadAPI) patch(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	// One writer per upload: a retry after a client-side stall must not append
	// while the abandoned request is still being drained.
	l, _ := u.locks.LoadOrStore(id, &sync.Mutex{})
	l.(*sync.Mutex).Lock()
	defer l.(*sync.Mutex).Unlock()
	rec, ok := u.st.Get(id)
	if !ok {
		writeErr(w, uploadNotFound())
		return
	}
	if rec.State != uploads.StateReceiving {
		writeErr(w, apperr.New(apperr.Conflict, "This upload is %s, so it takes no more of the file.", uploadStateWords(rec.State)).WithDetails(map[string]any{"state": rec.State}))
		return
	}
	off, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || off != rec.Offset {
		w.Header().Set("Upload-Offset", strconv.FormatInt(rec.Offset, 10))
		writeErr(w, apperr.New(apperr.Conflict, "The upload got out of step; it carries on from the part MetaRush already has.").WithDetails(map[string]any{"offset": rec.Offset}))
		return
	}
	f, err := os.OpenFile(u.st.Part(id), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Count bytes as they arrive so progress (and the stall clock) is live
	// within a chunk, not only between chunks.
	pw := &progressWriter{w: f, flush: func(n int64) {
		u.st.Update(id, false, func(x *uploads.Record) {
			x.Offset += n
			uploads.Progress(x, uploads.StageSending, x.Offset, x.Size, time.Now().UTC())
		})
	}}
	u.st.Update(id, false, func(x *uploads.Record) { x.Disconnected = false })
	_, err = io.Copy(pw, io.LimitReader(r.Body, min(maxChunk, rec.Size-rec.Offset)))
	pw.done()
	f.Close()
	cur, _ := u.st.Get(id)
	if err != nil {
		// The sender went away mid-chunk: show it as interrupted at once rather
		// than after the stall timeout (a new PATCH clears this).
		u.st.Update(id, true, func(x *uploads.Record) { x.Disconnected = true })
		writeErr(w, apperr.Wrap(apperr.InvalidInput, err, "The upload was interrupted. Drop the same file again to carry on from where it stopped.").WithDetails(map[string]any{"offset": cur.Offset}))
		return
	}
	if cur.Offset >= cur.Size {
		cur, _ = u.st.Update(id, true, func(x *uploads.Record) {
			x.State = uploads.StateIngesting
			uploads.Progress(x, uploads.StageChecking, 0, x.Size, time.Now().UTC())
		})
		go u.ingest(id)
	} else {
		cur, _ = u.st.Update(id, true, func(*uploads.Record) {})
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(cur.Offset, 10))
	writeOK(w, cur)
}

func uploadNotFound() error {
	return apperr.New(apperr.NotFound, "MetaRush can't find that upload (it may have finished or been canceled).")
}

// uploadStateWords says where an upload is, in plain words.
func uploadStateWords(state string) string {
	switch state {
	case uploads.StateIngesting:
		return "already being added"
	case uploads.StateDone:
		return "finished"
	case uploads.StateFailed:
		return "stopped"
	case uploads.StateCanceled:
		return "canceled"
	}
	return "not taking the file now"
}

// progressWriter reports written bytes about every 256 KiB.
type progressWriter struct {
	w       io.Writer
	pending int64
	flush   func(n int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.pending += int64(n)
	if p.pending >= 256<<10 {
		p.flush(p.pending)
		p.pending = 0
	}
	return n, err
}

func (p *progressWriter) done() {
	if p.pending > 0 {
		p.flush(p.pending)
		p.pending = 0
	}
}

// ingest runs the shared pipeline on a fully received upload, persisting each
// server-side stage with byte progress.
func (u *uploadAPI) ingest(id string) {
	rec, ok := u.st.Get(id)
	if !ok {
		return
	}
	ctx := context.Background()
	via := rec.Via
	if via == "" {
		via = "dashboard"
	}
	a := *u.s.appAs(person{UserID: rec.UserID, UserName: rec.UserName}, via) // the clip is theirs
	title := ""
	if rec.Source != nil {
		title = rec.Source.Title
	}
	res := ingest.File(ctx, &a, u.st.Part(id), ingest.Options{ProjectID: rec.ProjectID, DisplayName: rec.Name, Via: via, Transcribe: true,
		Duplicates: ingest.DuplicatesSkip, Source: rec.Source, Title: title,
		Progress: func(_, phase string, done, total int64) {
			stage := uploads.StageFor(phase)
			var force bool
			u.st.Update(id, false, func(x *uploads.Record) {
				force = x.Stage != stage
				uploads.Progress(x, stage, done, total, time.Now().UTC())
			})
			if force {
				u.st.Update(id, true, func(*uploads.Record) {})
			}
		}})
	final, _ := u.st.Update(id, true, func(x *uploads.Record) {
		x.Result = &res
		switch res.Status {
		case "ingested", "deduped", "resumed", "duplicate":
			x.State = uploads.StateDone
			uploads.Progress(x, uploads.StageDone, x.Size, x.Size, time.Now().UTC())
		default:
			x.State, x.Error = uploads.StateFailed, res.Error
		}
	})
	if final.State == uploads.StateDone {
		_ = os.Remove(u.st.Part(id)) // canonical store + cache hold the bytes now
	}
}

func (u *uploadAPI) cancel(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	rec, ok := u.st.Get(id)
	if !ok {
		writeErr(w, uploadNotFound())
		return
	}
	switch rec.State {
	case uploads.StateReceiving:
		rec, _ = u.st.Update(id, true, func(x *uploads.Record) { x.State = uploads.StateCanceled })
		_ = os.Remove(u.st.Part(id))
	case uploads.StateDone, uploads.StateFailed, uploads.StateCanceled:
		// Dismiss a finished upload from every dashboard's tray.
		rec, _ = u.st.Update(id, true, func(x *uploads.Record) { x.Dismissed = true })
		if rec.State == uploads.StateFailed {
			_ = os.Remove(u.st.Part(id))
		}
	}
	writeOK(w, rec)
}

// recover restarts ingests the daemon was running when it stopped (the bytes
// are complete; ingest itself is resumable).
func (u *uploadAPI) recover() {
	for _, rec := range u.st.List(7 * 24 * time.Hour) {
		if rec.State != uploads.StateIngesting {
			continue
		}
		if st, err := os.Stat(u.st.Part(rec.ID)); err != nil || st.Size() != rec.Size {
			u.st.Update(rec.ID, true, func(x *uploads.Record) {
				x.State, x.Error = uploads.StateFailed, "The part of the file MetaRush had received is missing. Drop the file again."
			})
			continue
		}
		go u.ingest(rec.ID)
	}
}

// sweep writes one "upload interrupted" notification for uploads that stopped
// receiving bytes, and removes long-finished records.
func (u *uploadAPI) sweep(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now()
		for _, rec := range u.st.List(7 * 24 * time.Hour) {
			if rec.State == uploads.StateReceiving && !rec.Notified && now.Sub(rec.LastProgressAt) > 90*time.Second {
				u.st.Update(rec.ID, true, func(x *uploads.Record) { x.Notified = true })
				notify.UploadInterrupted(ctx, u.s.appAs(person{UserID: rec.UserID, UserName: rec.UserName}, "dashboard"), rec.ID, rec.Name, int(rec.Pct()*100), rec.ProjectID)
			}
		}
	}
}
