package api

import (
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/recipes"
)

// Recipe files between workspaces (D219): recipe.export and recipe.import are
// LocalOnly (they write and read files where they are told), so the
// dashboard's Recipes gets two narrow endpoints instead — MetaRush chooses
// every path, never the browser:
//
//	GET  /api/recipes/export?recipe=<name>&version=<n>  the recipe file, to save
//	POST /api/recipes/import?project=&name=&dryRun=1&fileName=  the file as the body
//
// Both run as the request's person (s.appFor), recorded like any other call.

// maxRecipeUpload is the largest recipe file the dashboard sends (a
// variable only so a test can lower it).
var maxRecipeUpload int64 = 8 << 30

func (s *Server) recipeExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	in := recipes.RecipeExportIn{Recipe: strings.TrimSpace(q.Get("recipe")), Version: strings.TrimSpace(q.Get("version"))}
	if in.Recipe == "" {
		writeErr(w, apperr.New(apperr.InvalidInput, "Say which recipe to export."))
		return
	}
	if mb, err := strconv.Atoi(q.Get("maxMb")); err == nil && mb > 0 {
		in.MaxMB = mb
	}
	a := s.appFor(r, "dashboard")
	if err := os.MkdirAll(a.Cfg.Scratch.Dir, 0o755); err != nil {
		writeErr(w, err)
		return
	}
	tmp, err := os.CreateTemp(a.Cfg.Scratch.Dir, "recipe-export-")
	if err != nil {
		writeErr(w, err)
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	out, err := recipes.Export(r.Context(), a, &in, tmp)
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
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": recipes.ExportFile(out.Recipe)}))
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, tmp)
}

func (s *Server) recipeImport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	a := s.appFor(r, "dashboard")
	if err := os.MkdirAll(a.Cfg.Scratch.Dir, 0o755); err != nil {
		writeErr(w, err)
		return
	}
	tmp, err := os.CreateTemp(a.Cfg.Scratch.Dir, "recipe-import-")
	if err != nil {
		writeErr(w, err)
		return
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(r.Body, maxRecipeUpload+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		writeErr(w, apperr.Wrap(apperr.InvalidInput, err, "The recipe file didn't arrive whole; send it again."))
		return
	case n > maxRecipeUpload:
		writeErr(w, apperr.New(apperr.InvalidInput, "That file is larger than the dashboard takes; bring it in from a terminal on this computer.").WithCommand("metarush recipe import <file>"))
		return
	case n == 0:
		writeErr(w, apperr.New(apperr.InvalidInput, "Choose a recipe file to bring in."))
		return
	}
	in := recipes.RecipeImportIn{File: tmp.Name(), ProjectID: strings.TrimSpace(q.Get("project")), Name: strings.TrimSpace(q.Get("name")),
		DryRun: q.Get("dryRun") == "1" || q.Get("dryRun") == "true", FileName: strings.TrimSpace(q.Get("fileName"))}
	// The server chose the file, so this call is local to this machine.
	out, err := ops.Call(r.Context(), a, "recipe.import", mustJSON(in), ops.Caller{Human: true})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, out)
}
