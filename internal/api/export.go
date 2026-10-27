package api

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/interchange"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// "Send to editing app": export.timeline is LocalOnly (it writes files where
// it is told), so the dashboard gets this narrow endpoint instead. MetaRush
// chooses the folder (a new one under ~/Movies/MetaRush exports), never the
// browser; it runs the same operation, recorded like any other call.

// SendToEditorIn asks for a timeline for an editing app.
type SendToEditorIn struct {
	RevisionID string `json:"revisionId"`
	App        string `json:"app" jsonschema:"enum=resolve,enum=finalcut,enum=premiere"`
	// Footage puts the original footage in the folder too (instant copies where the disk allows).
	Footage bool `json:"footage"`
}

// SendToEditorOut says where the timeline went.
type SendToEditorOut struct {
	Folder string                `json:"folder"`
	Export ops.ExportTimelineOut `json:"export"`
}

// ExportRevealIn opens an export folder in Finder.
type ExportRevealIn struct {
	Folder string `json:"folder"`
}

// editorFormats maps the apps people pick to the format each opens best.
var editorFormats = map[string]struct{ format, label string }{
	"resolve":  {interchange.FormatOTIO, "DaVinci Resolve"},
	"finalcut": {interchange.FormatFCPXML, "Final Cut Pro"},
	"premiere": {interchange.FormatXMEML, "Premiere Pro"},
}

func (s *Server) exportTimeline(w http.ResponseWriter, r *http.Request) {
	var in SendToEditorIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&in); err != nil {
		writeErr(w, apperr.Wrap(apperr.InvalidInput, err, "MetaRush couldn't read the request."))
		return
	}
	ed, ok := editorFormats[in.App]
	if !ok {
		writeErr(w, apperr.New(apperr.InvalidInput, "Choose DaVinci Resolve, Final Cut Pro or Premiere Pro."))
		return
	}
	root, err := ops.ExportsFolder()
	if err != nil {
		writeErr(w, err)
		return
	}
	a := *s.appFor(r, "dashboard")
	// Name the folder after the cut: "<project> version 3 for Resolve".
	var rev models.Revision
	var proj models.Project
	if err := a.DB.GetByID(r.Context(), store.CRevisions, a.WorkspaceID(), in.RevisionID, &rev); err != nil {
		writeErr(w, apperr.New(apperr.NotFound, "MetaRush can't find that version of the cut.").WithDetails(map[string]any{"revisionId": in.RevisionID}))
		return
	}
	_ = a.DB.GetByID(r.Context(), store.CProjects, a.WorkspaceID(), rev.ProjectID, &proj)
	base := safeFolderName(fmt.Sprintf("%s version %d for %s", proj.Name, rev.Number, ed.label))
	folder := uniqueFolder(root, base)
	if err := os.MkdirAll(folder, 0o755); err != nil {
		writeErr(w, apperr.Wrap(apperr.InvalidInput, err, "MetaRush couldn't make the folder “%s” for the timeline.", filepath.Base(folder)).WithDetails(map[string]any{"folder": folder}))
		return
	}
	in2 := ops.ExportTimelineIn{RevisionID: in.RevisionID, Format: ed.format, Out: filepath.Join(folder, base+interchange.Extension(ed.format))}
	if in.Footage {
		in2.Media = filepath.Join(folder, "Footage")
	}
	// The server chose every path, so this call is local to this machine.
	out, err := ops.Call(r.Context(), &a, "export.timeline", mustJSON(in2), ops.Caller{Human: true})
	if err != nil {
		_ = os.Remove(folder) // only when still empty
		writeErr(w, err)
		return
	}
	writeOK(w, SendToEditorOut{Folder: folder, Export: *out.(*ops.ExportTimelineOut)})
}

func (s *Server) exportReveal(w http.ResponseWriter, r *http.Request) {
	var in ExportRevealIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&in); err != nil {
		writeErr(w, apperr.Wrap(apperr.InvalidInput, err, "MetaRush couldn't read the request."))
		return
	}
	root, err := ops.ExportsFolder()
	if err != nil {
		writeErr(w, err)
		return
	}
	dir, err := filepath.Abs(in.Folder)
	if err != nil || !strings.HasPrefix(dir, root+string(filepath.Separator)) {
		writeErr(w, apperr.New(apperr.Unauthorized, "MetaRush only opens the folders it made for editing apps."))
		return
	}
	if err := s.App.Tools.RevealFolder(r.Context(), dir); err != nil {
		writeErr(w, apperr.Wrap(apperr.ToolMissing, err, "MetaRush couldn't open the folder on this machine."))
		return
	}
	writeOK(w, map[string]any{"opened": dir})
}

// exportDownload sends an export folder to the browser as a .zip (the
// timeline, its captions and the footage copies), for a person on another
// computer: the folder itself was made on the computer that runs MetaRush.
// Only folders MetaRush made for editing apps are sent; links and anything
// that isn't a plain file are left out. Video is stored as is (not compressed
// again).
func (s *Server) exportDownload(w http.ResponseWriter, r *http.Request) {
	root, err := ops.ExportsFolder()
	if err != nil {
		writeErr(w, err)
		return
	}
	dir, err := filepath.Abs(r.URL.Query().Get("folder"))
	if err != nil || filepath.Dir(dir) != root {
		writeErr(w, apperr.New(apperr.Unauthorized, "MetaRush only sends the folders it made for editing apps."))
		return
	}
	if st, err := os.Lstat(dir); err != nil || !st.IsDir() {
		writeErr(w, apperr.New(apperr.NotFound, "That folder isn't there any more. Make the timeline again."))
		return
	}
	name := filepath.Base(dir)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ".zip"}))
	w.Header().Set("Cache-Control", "no-store")
	zw := zip.NewWriter(w)
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil // links and special files stay behind
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return nil
		}
		hdr.Name = filepath.ToSlash(filepath.Join(name, rel))
		hdr.Method = zip.Store
		if !strings.HasSuffix(strings.ToLower(p), ".mov") && !strings.HasSuffix(strings.ToLower(p), ".mp4") && info.Size() < 64<<20 {
			hdr.Method = zip.Deflate // timelines and captions are text
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		out, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, f)
		return err
	})
	_ = zw.Close()
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// safeFolderName keeps a folder name readable and legal on every disk.
func safeFolderName(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '-'
		}
		if r < 0x20 {
			return -1
		}
		return r
	}, s)
	s = strings.Trim(strings.TrimSpace(s), ".")
	if len(s) > 100 {
		s = strings.TrimSpace(s[:100])
	}
	if s == "" {
		s = "Timeline"
	}
	return s
}

// uniqueFolder is root/base, or root/"base 2"… when that exists.
func uniqueFolder(root, base string) string {
	for n := 1; ; n++ {
		name := base
		if n > 1 {
			name = fmt.Sprintf("%s %d", base, n)
		}
		p := filepath.Join(root, name)
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			return p
		}
	}
}
