package intake

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/interchange"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
)

// AddPathsIn adds local files and folders the way the Add box does.
type AddPathsIn struct {
	Paths     []string `json:"paths" jsonschema:"minItems=1"`
	ProjectID string   `json:"projectId,omitempty" jsonschema:"description=The project everything goes into (ID or name); empty: footage to the Inbox and resources to the workspace library."`
	// Workspace puts resources in the workspace library even with a project.
	Workspace bool `json:"workspace,omitempty"`
	// Folders: link (default: keep them in step) or copy (add what is in them once).
	Folders    string `json:"folders,omitempty" jsonschema:"enum=link,enum=copy"`
	Upload     string `json:"upload,omitempty" jsonschema:"enum=small,enum=all,enum=on-use,description=Linked folders: when their files go to the media store."`
	Transcribe *bool  `json:"transcribe,omitempty"`
}

// ResourcesAddIn adds pictures, SVGs, PDFs, fonts and folders of them to the library.
type ResourcesAddIn struct {
	Paths       []string `json:"paths" jsonschema:"minItems=1"`
	ProjectID   string   `json:"projectId,omitempty" jsonschema:"description=The project (ID or name); empty: the workspace library."`
	Workspace   bool     `json:"workspace,omitempty" jsonschema:"description=The workspace library (every project sees it)."`
	Name        string   `json:"name,omitempty" jsonschema:"pattern=^[a-z][a-z0-9_-]{0\\,47}$,description=The short name (one file)."`
	Title       string   `json:"title,omitempty" jsonschema:"maxLength=200,description=The display name (one file)."`
	Tags        []string `json:"tags,omitempty"`
	Description string   `json:"description,omitempty" jsonschema:"maxLength=2000"`
	License     string   `json:"license,omitempty" jsonschema:"maxLength=500"`
	// Version adds the file as a new version of this resource (name or ID).
	Version string `json:"version,omitempty" jsonschema:"description=Add the file as a new version of this resource (its name or ID)."`
	Folders string `json:"folders,omitempty" jsonschema:"enum=link,enum=copy"`
	Upload  string `json:"upload,omitempty" jsonschema:"enum=small,enum=all,enum=on-use"`
}

// AddedFile is what one file became.
type AddedFile struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Route string `json:"route" jsonschema:"enum=footage,enum=resource,enum=unknown"`
	// Status: added (a new resource), version, same (already there), ingested
	// (new footage), deduped (footage already there), skipped, failed.
	Status     string   `json:"status"`
	Message    string   `json:"message"`
	AssetID    string   `json:"assetId,omitempty"`
	ResourceID string   `json:"resourceId,omitempty"`
	Changes    []string `json:"changes,omitempty"`
	Notes      []string `json:"notes,omitempty"`
}

// AddPathsOut is the outcome.
type AddPathsOut struct {
	Files   []AddedFile       `json:"files"`
	Folders []LinkedFolderOut `json:"folders"`
	Summary string            `json:"summary"`
}

// isTimeline reports a timeline file (or an .fcpxmld bundle) another editing app exported.
func isTimeline(path string) bool {
	head := []byte(nil)
	if strings.EqualFold(filepath.Ext(path), ".xml") {
		if f, err := os.Open(path); err == nil {
			buf := make([]byte, 4096)
			n, _ := f.Read(buf)
			head = buf[:n]
			f.Close()
		}
	}
	return interchange.IsTimelineName(path, head)
}

// addResourceFile adds one resource file with options.
func addResourceFile(ctx context.Context, a *app.App, path string, o resources.AddOptions) AddedFile {
	af := AddedFile{Path: path, Name: filepath.Base(path), Route: resources.RouteResource}
	r, err := resources.AddFile(ctx, a, path, o)
	switch {
	case errors.Is(err, resources.ErrNotResource):
		af.Route, af.Status, af.Message = r.Sniffed.Route, "skipped", r.Message
		if af.Route == resources.RouteFootage {
			af.Message = af.Name + " is a video or sound file, so it belongs with your footage: drop it on the Add box instead."
		}
	case err != nil:
		af.Status, af.Message = "failed", ingest.FailText(err)
	default:
		af.Status, af.Message, af.Changes, af.Notes = r.Status, r.Message, r.Changes, r.Notes
		if r.Resource != nil {
			af.ResourceID = r.Resource.ID
		}
	}
	return af
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

func summarizeAdded(out *AddPathsOut) string {
	var res, foot, same, failed int
	for _, f := range out.Files {
		switch f.Status {
		case resources.StatusAdded, resources.StatusVersion:
			res++
		case "ingested", "resumed":
			foot++
		case resources.StatusSame, "deduped", "duplicate":
			same++
		case "failed":
			failed++
		}
	}
	var parts []string
	if foot > 0 {
		parts = append(parts, count(foot, "clip added", "clips added"))
	}
	if res > 0 {
		parts = append(parts, count(res, "resource added", "resources added"))
	}
	if same > 0 {
		parts = append(parts, count(same, "file was already there", "files were already there"))
	}
	if len(out.Folders) > 0 {
		parts = append(parts, count(len(out.Folders), "folder linked", "folders linked"))
	}
	if failed > 0 {
		parts = append(parts, count(failed, "file couldn't be added", "files couldn't be added"))
	}
	if len(parts) == 0 {
		return "Nothing to add."
	}
	return upper(strings.Join(parts, ", ")) + "."
}

func init() {
	ops.Def("add.paths", "Add local files and folders like the Add box: footage to the project (or the Inbox), pictures, SVGs, PDFs and fonts to its resources (or the workspace library), folders as linked folders", ops.Flags{Mutating: true, LocalOnly: true}, func(ctx context.Context, a *app.App, in *AddPathsIn) (*AddPathsOut, error) {
		out := &AddPathsOut{Files: []AddedFile{}, Folders: []LinkedFolderOut{}}
		projectID := ""
		if strings.TrimSpace(in.ProjectID) != "" {
			p, err := ops.ResolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			projectID = p.ID
		}
		resProject := projectID
		if in.Workspace {
			resProject = ""
		}
		tr := in.Transcribe == nil || *in.Transcribe
		for _, raw := range in.Paths {
			path := expandHome(strings.TrimSpace(raw))
			st, err := os.Stat(path)
			if err != nil {
				out.Files = append(out.Files, AddedFile{Path: raw, Name: filepath.Base(raw), Status: "failed", Message: "Can't find " + filepath.Base(raw) + "."})
				continue
			}
			if isTimeline(path) {
				// A timeline from another editing app becomes a cut, not footage (D304).
				out.Files = append(out.Files, AddedFile{Path: raw, Name: filepath.Base(path), Route: resources.RouteUnknown, Status: "skipped",
					Message: filepath.Base(path) + " is a timeline from another editing app, not footage: make a cut from it instead."})
				continue
			}
			if st.IsDir() && in.Folders != "copy" {
				fo, err := LinkFolder(ctx, a, &LinkedFolderLinkIn{Path: path, ProjectID: resProject, Upload: in.Upload})
				if err != nil {
					out.Files = append(out.Files, AddedFile{Path: raw, Name: filepath.Base(raw), Status: "failed", Message: ingest.FailText(err)})
					continue
				}
				out.Folders = append(out.Folders, *fo)
				continue
			}
			files, skipped := ingest.Collect([]string{path}, true)
			for _, s := range skipped {
				out.Files = append(out.Files, AddedFile{Path: s.Path, Name: s.Name, Status: "failed", Message: s.Error})
			}
			for _, f := range files {
				sn, _ := resources.SniffFile(f)
				if sn.Route == resources.RouteResource {
					out.Files = append(out.Files, addResourceFile(ctx, a, f, resources.AddOptions{ProjectID: resProject, Via: a.Actor.Via}))
					continue
				}
				r := ingest.File(ctx, a, f, ingest.Options{ProjectID: projectID, Via: a.Actor.Via, Transcribe: tr})
				af := AddedFile{Path: f, Name: r.Name, Route: resources.RouteFootage, Status: r.Status, AssetID: r.AssetID, Message: r.Error}
				switch r.Status {
				case "ingested", "resumed":
					af.Message = "Added " + r.Title + "."
				case "deduped", "duplicate":
					af.Message = r.Name + " was already there."
				case "skipped":
					af.Route = resources.RouteUnknown
					af.Message = r.Name + " isn't a video, sound, picture, PDF or font."
				}
				out.Files = append(out.Files, af)
			}
		}
		out.Summary = summarizeAdded(out)
		return out, nil
	})
	ops.Def("resources.add", "Add pictures (PNG, JPEG, WebP; HEIC converted), SVGs (cleaned), PDFs (pages become pictures) and fonts to a project's resources or the workspace library; a folder is linked and kept in step", ops.Flags{Mutating: true, LocalOnly: true}, func(ctx context.Context, a *app.App, in *ResourcesAddIn) (*AddPathsOut, error) {
		out := &AddPathsOut{Files: []AddedFile{}, Folders: []LinkedFolderOut{}}
		projectID := ""
		if strings.TrimSpace(in.ProjectID) != "" && !in.Workspace {
			p, err := ops.ResolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			projectID = p.ID
		}
		var versionOf string
		if strings.TrimSpace(in.Version) != "" {
			res, err := ops.Call(ctx, a, "resources.show", mustJSON(map[string]any{"ref": in.Version, "projectId": projectID}), ops.CallerFrom(ctx))
			if err != nil {
				return nil, err
			}
			versionOf = res.(*ops.ResourceShowOut).Resource.ID
		}
		one := len(in.Paths) == 1
		for _, raw := range in.Paths {
			path := expandHome(strings.TrimSpace(raw))
			st, err := os.Stat(path)
			if err != nil {
				out.Files = append(out.Files, AddedFile{Path: raw, Name: filepath.Base(raw), Status: "failed", Message: "Can't find " + filepath.Base(raw) + "."})
				continue
			}
			if isTimeline(path) {
				// A timeline from another editing app becomes a cut, not footage (D304).
				out.Files = append(out.Files, AddedFile{Path: raw, Name: filepath.Base(path), Route: resources.RouteUnknown, Status: "skipped",
					Message: filepath.Base(path) + " is a timeline from another editing app, not footage: make a cut from it instead."})
				continue
			}
			if st.IsDir() && in.Folders != "copy" {
				fo, err := LinkFolder(ctx, a, &LinkedFolderLinkIn{Path: path, ProjectID: projectID, Upload: in.Upload})
				if err != nil {
					out.Files = append(out.Files, AddedFile{Path: raw, Name: filepath.Base(raw), Status: "failed", Message: ingest.FailText(err)})
					continue
				}
				out.Folders = append(out.Folders, *fo)
				continue
			}
			files, _ := ingest.Collect([]string{path}, true)
			for _, f := range files {
				o := resources.AddOptions{ProjectID: projectID, Tags: in.Tags, Description: in.Description, License: in.License, ResourceID: versionOf, Via: a.Actor.Via}
				if one && len(files) == 1 {
					o.Name, o.Title = in.Name, in.Title
				}
				out.Files = append(out.Files, addResourceFile(ctx, a, f, o))
			}
		}
		out.Summary = summarizeAdded(out)
		return out, nil
	})
}

var _ = models.ScopeProject

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	ops.Offer("resources.add", ops.Offering{ID: "pictures-and-pdfs", Group: "bring", Title: "Pictures, PDFs and fonts",
		Does:  "Adds pictures, logos, PDF pages and fonts for your cuts; iPhone photos are converted on the way in.",
		Words: []string{"logo", "slides from a pdf", "heic", "font"}})
}
