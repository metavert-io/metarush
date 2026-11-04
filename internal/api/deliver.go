package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// The dashboard's Deliver (D422): deliver.export is LocalOnly (it writes
// files where it is told), so the dashboard gets this narrow endpoint, as
// with "Send to editing app": MetaRush chooses the folder — a new one under
// the exports folder, named after the project and the version — never the
// browser, and runs the same operation as the request's person, recorded
// like any other call (the ship gate, What may leave and Content Credentials
// included). Revealing it in Finder and downloading it as a .zip are the
// export endpoints' (export/reveal, export/download).

// DeliverHereIn asks to deliver a version's final renders.
type DeliverHereIn struct {
	RevisionID string `json:"revisionId"`
	// Presets are only these shapes (default: every shape rendered).
	Presets []string `json:"presets,omitempty"`
}

// DeliverHereOut says where the videos went and what delivery said.
type DeliverHereOut struct {
	Folder  string         `json:"folder"`
	Deliver ops.DeliverOut `json:"deliver"`
}

func (s *Server) deliverHere(w http.ResponseWriter, r *http.Request) {
	var in DeliverHereIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<14)).Decode(&in); err != nil {
		writeErr(w, apperr.Wrap(apperr.InvalidInput, err, "MetaRush couldn't read the request."))
		return
	}
	root, err := ops.ExportsFolder()
	if err != nil {
		writeErr(w, err)
		return
	}
	a := *s.appFor(r, "dashboard")
	var rev models.Revision
	var proj models.Project
	if err := a.DB.GetByID(r.Context(), store.CRevisions, a.WorkspaceID(), in.RevisionID, &rev); err != nil {
		writeErr(w, apperr.New(apperr.NotFound, "MetaRush can't find that version of the cut.").WithDetails(map[string]any{"revisionId": in.RevisionID}))
		return
	}
	_ = a.DB.GetByID(r.Context(), store.CProjects, a.WorkspaceID(), rev.ProjectID, &proj)
	folder := uniqueFolder(root, safeFolderName(fmt.Sprintf("%s version %d", proj.Name, rev.Number)))
	// The server chose the folder, so this call is local to this machine.
	out, err := ops.Call(r.Context(), &a, "deliver.export", mustJSON(ops.DeliverIn{RevisionIDs: []string{rev.ID}, Out: folder, Presets: in.Presets}), ops.Caller{Human: true})
	if err != nil {
		_ = os.Remove(folder) // only when still empty
		writeErr(w, err)
		return
	}
	writeOK(w, DeliverHereOut{Folder: folder, Deliver: *out.(*ops.DeliverOut)})
}
