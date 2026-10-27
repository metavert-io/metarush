package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/intake"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// Resource files for the dashboard (D79–D81): same-origin and local-first
// (from the cache, a linked folder on this machine, or the media store),
// never a redirect. An SVG is served with a sandboxing Content-Security-Policy
// on top of the clean-up it got on the way in, so even opened on its own it
// can't run anything.
//
//	/media/resources/file/{sha}              any resource file by SHA-256 (a preview plan's resourceSources)
//	/media/resources/{id}/thumb[?v=<sha…>]    a version's thumbnail (an SVG is its own)
//	/media/resources/{id}/file[?v=…&page=N]   the file, or a PDF page's picture

const resourceCSP = "default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; sandbox"

func (s *Server) resourceFileByHash(w http.ResponseWriter, r *http.Request) {
	sha := mux.Vars(r)["sha"]
	if len(sha) != 64 || strings.Trim(sha, "0123456789abcdef") != "" {
		writeErr(w, apperr.New(apperr.NotFound, "MetaRush can't find that file."))
		return
	}
	var docs []models.Resource
	err := s.App.DB.Find(r.Context(), store.CResources, s.App.WorkspaceID(), bson.M{"$or": bson.A{
		bson.M{"versions.sha256": sha}, bson.M{"versions.pages.sha256": sha},
	}}, &docs)
	if err != nil || len(docs) == 0 {
		writeErr(w, apperr.New(apperr.NotFound, "MetaRush can't find that file."))
		return
	}
	res := &docs[0]
	for i := range docs {
		for _, v := range docs[i].Versions {
			if v.SHA256 == sha {
				s.serveResource(w, r, &docs[i], v.SHA256, 0)
				return
			}
			for _, pg := range v.Pages {
				if pg.SHA256 == sha {
					s.serveResource(w, r, &docs[i], v.SHA256, pg.Number)
					return
				}
			}
		}
	}
	s.serveResource(w, r, res, "", 0)
}

func (s *Server) resourceMedia(w http.ResponseWriter, r *http.Request) {
	v := mux.Vars(r)
	var res models.Resource
	if err := s.App.DB.GetByID(r.Context(), store.CResources, s.App.WorkspaceID(), v["id"], &res); err != nil {
		writeErr(w, apperr.New(apperr.NotFound, "MetaRush can't find that resource."))
		return
	}
	sha := ""
	if want := r.URL.Query().Get("v"); want != "" {
		for _, ver := range res.Versions {
			if strings.HasPrefix(ver.SHA256, want) {
				sha = ver.SHA256
			}
		}
		if sha == "" {
			writeErr(w, apperr.New(apperr.NotFound, "“%s” has no version like that.", res.Title))
			return
		}
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	switch v["kind"] {
	case "file":
		s.serveResource(w, r, &res, sha, page)
	case "thumb":
		ver := res.Latest()
		if sha != "" {
			ver = res.Version(sha)
		}
		if ver == nil {
			writeErr(w, apperr.New(apperr.NotFound, "“%s” has no versions yet.", res.Title))
			return
		}
		if t := ver.Thumbnail; t != nil && t.StorageKey != "" {
			p, err := s.App.Cache.Ensure(r.Context(), s.App.Media, t.StorageKey, t.SHA256, t.Size, nil)
			if err != nil {
				writeErr(w, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get the picture of “%s” from its storage.", res.Title))
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "private, max-age=86400")
			w.Header().Set("Content-Security-Policy", resourceCSP)
			serveFile(w, r, p, res.Name+"-thumb.png")
			return
		}
		if res.Kind == models.ResourceSVG {
			s.serveResource(w, r, &res, ver.SHA256, 0) // an SVG is its own thumbnail
			return
		}
		writeErr(w, apperr.New(apperr.NotFound, "“%s” has no picture.", res.Title))
	default:
		writeErr(w, apperr.New(apperr.NotFound, "MetaRush can't find that."))
	}
}

func (s *Server) serveResource(w http.ResponseWriter, r *http.Request, res *models.Resource, sha string, page int) {
	p, ver, err := resources.LocalFile(r.Context(), s.App, res, sha, page)
	if err != nil {
		writeErr(w, err)
		return
	}
	mime, name := ver.MIME, res.Name+"."+ver.Ext
	if page > 0 {
		mime, name = "image/png", res.Name+"-page-"+strconv.Itoa(page)+".png"
	}
	if mime != "" {
		w.Header().Set("Content-Type", mime)
	}
	w.Header().Set("Cache-Control", "private, max-age=86400") // a version's bytes never change
	w.Header().Set("Content-Security-Policy", resourceCSP)
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", attachment(name))
	}
	serveFile(w, r, p, name)
}

// The native folder picker acts on this machine's screen, and linking or
// changing a linked folder uses this machine's paths, so those ops are
// LocalOnly (not reachable through /api/ops). The signed-in dashboard of this
// daemon gets these narrow endpoints, which run the same ops as a person here.

// FolderChooseIn opens the native picker and links what was picked.
type FolderChooseIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=The project the folder belongs to (empty: the workspace library)."`
	Upload    string `json:"upload,omitempty" jsonschema:"enum=small,enum=all,enum=on-use"`
	Existing  string `json:"existing,omitempty" jsonschema:"enum=import,enum=skip"`
	// FolderID links an already-linked folder on this machine (it was linked on another).
	FolderID string `json:"folderId,omitempty"`
}

// FolderChooseOut is the linked folder, or Canceled.
type FolderChooseOut struct {
	Canceled bool                    `json:"canceled"`
	Linked   *intake.LinkedFolderOut `json:"linked,omitempty"`
}

func (s *Server) folderChoose(w http.ResponseWriter, r *http.Request) {
	// Linking a folder of this computer is the owners' (resources.link, D191):
	// refused before the picker opens on its screen.
	if s.ownerOnly(w, r, "resources.link") || s.pickerElsewhere(w, r) {
		return
	}
	var in FolderChooseIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&in); err != nil && err != io.EOF {
		writeErr(w, apperr.Wrap(apperr.InvalidInput, err, "MetaRush couldn't read the request."))
		return
	}
	a := *s.appFor(r, "dashboard")
	p, err := a.Tools.ChooseFolder(r.Context(), "Choose a folder to link to MetaRush", "")
	if errors.Is(err, tools.ErrUnsupported) {
		writeErr(w, apperr.New(apperr.NotConfigured, "This machine can't show a folder picker.").WithHint("Link the folder from a terminal instead.").
			WithDetails(map[string]any{"reason": "unsupported"}).WithCommand("metarush resources link <folder>"))
		return
	}
	if err != nil {
		writeErr(w, apperr.Wrap(apperr.Internal, err, "The folder picker didn't open. Try again."))
		return
	}
	if p == "" {
		writeOK(w, FolderChooseOut{Canceled: true})
		return
	}
	var out any
	if in.FolderID != "" {
		raw, _ := json.Marshal(intake.LinkedFolderSetIn{Folder: in.FolderID, Path: p})
		out, err = ops.Call(r.Context(), &a, "resources.folder", raw, ops.Caller{Human: true})
	} else {
		raw, _ := json.Marshal(intake.LinkedFolderLinkIn{Path: p, ProjectID: in.ProjectID, Upload: in.Upload, Existing: in.Existing})
		out, err = ops.Call(r.Context(), &a, "resources.link", raw, ops.Caller{Human: true})
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	linked, _ := out.(*intake.LinkedFolderOut)
	writeOK(w, FolderChooseOut{Linked: linked})
}

// folderSet changes a linked folder on this machine (upload policy, pause,
// resume, look again) through the LocalOnly resources.folder op.
func (s *Server) folderSet(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<14))
	if err != nil {
		writeErr(w, apperr.Wrap(apperr.InvalidInput, err, "MetaRush couldn't read the request."))
		return
	}
	var in intake.LinkedFolderSetIn
	if err := json.Unmarshal(body, &in); err != nil {
		writeErr(w, apperr.Wrap(apperr.InvalidInput, err, "MetaRush couldn't read the request."))
		return
	}
	if in.Path != "" {
		writeErr(w, apperr.New(apperr.InvalidInput, "Choose the folder with the folder picker."))
		return
	}
	a := *s.appFor(r, "dashboard")
	out, err := ops.Call(r.Context(), &a, "resources.folder", body, ops.Caller{Human: true})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, out)
}
