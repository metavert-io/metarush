package api

import (
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/abilities"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// Ability files between workspaces (D292), like Recipes' files (D219):
// ability.export and ability.import are LocalOnly, so Library → Abilities
// gets two narrow endpoints — MetaRush chooses every path, never the browser:
//
//	GET  /api/abilities/export?ref=<name[@version]>  the ability file, to save
//	POST /api/abilities/import?asVersion=1&dryRun=1   the file as the body
//
// Both run as the request's person (s.appFor), recorded like any other call.

// maxAbilityUpload is the largest ability file the dashboard sends (a
// variable only so a test can lower it).
var maxAbilityUpload int64 = 256 << 20

func (s *Server) abilityExport(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if ref == "" {
		writeErr(w, apperr.New(apperr.InvalidInput, "Say which ability to export."))
		return
	}
	a := s.appFor(r, "dashboard")
	if err := os.MkdirAll(a.Cfg.Scratch.Dir, 0o755); err != nil {
		writeErr(w, err)
		return
	}
	tmp, err := os.CreateTemp(a.Cfg.Scratch.Dir, "ability-export-")
	if err != nil {
		writeErr(w, err)
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	out, err := abilities.ExportRef(r.Context(), a, ref, tmp)
	if err != nil {
		writeErr(w, err)
		return
	}
	st, err := tmp.Stat()
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": abilities.ExportName(out.Name)}))
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, tmp)
}

func (s *Server) abilityImport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	a := s.appFor(r, "dashboard")
	if err := os.MkdirAll(a.Cfg.Scratch.Dir, 0o755); err != nil {
		writeErr(w, err)
		return
	}
	tmp, err := os.CreateTemp(a.Cfg.Scratch.Dir, "ability-import-")
	if err != nil {
		writeErr(w, err)
		return
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(r.Body, maxAbilityUpload+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		writeErr(w, apperr.Wrap(apperr.InvalidInput, err, "The ability file didn't arrive whole; send it again."))
		return
	case n > maxAbilityUpload:
		writeErr(w, apperr.New(apperr.InvalidInput, "That file is larger than an ability file can be.").WithCommand("metarush ability import <file>"))
		return
	case n == 0:
		writeErr(w, apperr.New(apperr.InvalidInput, "Choose an ability file to bring in."))
		return
	}
	yes := func(k string) bool { return q.Get(k) == "1" || q.Get(k) == "true" }
	in := abilities.AbilityImportIn{File: tmp.Name(), AsVersion: yes("asVersion"), DryRun: yes("dryRun")}
	// The server chose the file, so this call is local to this machine.
	out, err := ops.Call(r.Context(), a, "ability.import", mustJSON(in), ops.Caller{Human: true})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, out)
}
