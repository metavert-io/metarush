package intake

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/drive"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
)

// Bringing files in from a Google Drive or Dropbox folder (D220, D306)
// through the person's own rclone on this computer, without syncing the
// folder: add.cloud.list lists it (each file's ID, name, size, time and the
// service's checksum); add.cloud fetches the files asked for by their IDs —
// each checked against the listing — and adds them like the Add box does:
// footage to the project, pictures to its resources, each saying where it
// came from. MetaRush never signs in to either service and holds no token.

// CloudListIn names a folder.
type CloudListIn struct {
	Folder string `json:"folder" jsonschema:"description=The folder as rclone names it: remote:path (gdrive:Teaser/Art\\, dropbox:Shoot/Day 1)."`
}

// CloudFile is one file of the folder.
type CloudFile struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Checksum string    `json:"checksum,omitempty"`
	// What it looks like: footage, picture or other (its bytes decide when it comes in).
	What string `json:"what" jsonschema:"enum=footage,enum=picture,enum=other"`
}

// CloudListOut is the folder's files, newest first.
type CloudListOut struct {
	Folder  string      `json:"folder"`
	Service string      `json:"service" jsonschema:"description=Google Drive or Dropbox."`
	Words   string      `json:"words"`
	Files   []CloudFile `json:"files"`
	Summary string      `json:"summary"`
}

// CloudAddIn brings files of a folder in.
type CloudAddIn struct {
	Folder    string   `json:"folder" jsonschema:"description=The folder as rclone names it: remote:path."`
	IDs       []string `json:"ids,omitempty" jsonschema:"maxItems=200,description=The files' IDs (from add.cloud.list)."`
	Names     []string `json:"names,omitempty" jsonschema:"maxItems=200,description=Or the files' names in the folder."`
	All       bool     `json:"all,omitempty" jsonschema:"description=Every video\\, sound and picture in the folder."`
	ProjectID string   `json:"projectId,omitempty" jsonschema:"description=The project they go into (default: footage to the Inbox\\, pictures to the workspace library)."`
}

// Limits on what comes in from a cloud folder (as Recipes take, D220).
const (
	cloudPictureMax = resources.MaxFileBytes
	cloudFootageMax = 64 << 30
)

func what(f drive.File) string {
	switch {
	case f.Footage():
		return "footage"
	case f.Picture():
		return "picture"
	}
	return "other"
}

// cloudURL is where a file is, for provenance (MetaRush never opens it).
func cloudURL(f drive.Folder, fl drive.File) string {
	if f.Kind == drive.DropboxType {
		u := url.URL{Scheme: "https", Host: "www.dropbox.com", Path: "/home/" + strings.TrimPrefix(f.Path+"/"+fl.Name, "/")}
		return u.String()
	}
	return "https://drive.google.com/file/d/" + fl.ID + "/view"
}

func init() {
	ops.Def("add.cloud.list", "List a Google Drive or Dropbox folder through your own rclone on this computer (each file's ID, name, size, time and checksum), without syncing it; nothing is fetched",
		ops.Flags{LocalOnly: true}, func(ctx context.Context, a *app.App, in *CloudListIn) (*CloudListOut, error) {
			f, err := drive.ParseAny(ctx, a.Tools, in.Folder)
			if err != nil {
				return nil, err
			}
			files, err := drive.List(ctx, a.Tools, f)
			if err != nil {
				return nil, err
			}
			out := &CloudListOut{Folder: f.String(), Service: drive.Service(f.Kind), Words: f.Words(), Files: []CloudFile{}}
			footage, pictures := 0, 0
			for _, fl := range files {
				w := what(fl)
				switch w {
				case "footage":
					footage++
				case "picture":
					pictures++
				}
				out.Files = append(out.Files, CloudFile{ID: fl.ID, Name: fl.Name, Size: fl.Size, Modified: fl.Modified, Checksum: fl.Checksum(), What: w})
			}
			out.Summary = fmt.Sprintf("%s: %s (%s and %s).", upperFirstRune(f.Words()), count(len(files), "file", "files"), count(footage, "video or sound file", "videos or sound files"), count(pictures, "picture", "pictures"))
			return out, nil
		})
	ops.Def("add.cloud", "Bring files in from a Google Drive or Dropbox folder through your own rclone on this computer, by their IDs (or names, or all of them): each is fetched, checked against the folder's listing and added like the Add box does — footage to the project, pictures to its resources — saying where it came from",
		ops.Flags{Mutating: true, LocalOnly: true}, func(ctx context.Context, a *app.App, in *CloudAddIn) (*AddPathsOut, error) {
			f, err := drive.ParseAny(ctx, a.Tools, in.Folder)
			if err != nil {
				return nil, err
			}
			projectID := ""
			if strings.TrimSpace(in.ProjectID) != "" {
				p, err := ops.ResolveProject(ctx, a, in.ProjectID)
				if err != nil {
					return nil, err
				}
				projectID = p.ID
			}
			if len(in.IDs) == 0 && len(in.Names) == 0 && !in.All {
				return nil, apperr.New(apperr.InvalidInput, "Say which files to bring in from %s, or all of them.", f.Words()).WithCommand("metarush add cloud " + f.String() + " --list")
			}
			files, err := drive.List(ctx, a.Tools, f)
			if err != nil {
				return nil, err
			}
			byID, byName := map[string]drive.File{}, map[string]drive.File{}
			for _, fl := range files {
				byID[fl.ID], byName[strings.ToLower(fl.Name)] = fl, fl
			}
			var want []drive.File
			seen := map[string]bool{}
			add := func(fl drive.File) {
				if !seen[fl.ID] {
					seen[fl.ID] = true
					want = append(want, fl)
				}
			}
			var missing []string
			for _, id := range in.IDs {
				if fl, ok := byID[strings.TrimSpace(id)]; ok {
					add(fl)
				} else {
					missing = append(missing, id)
				}
			}
			for _, n := range in.Names {
				if fl, ok := byName[strings.ToLower(strings.TrimSpace(n))]; ok {
					add(fl)
				} else {
					missing = append(missing, "“"+n+"”")
				}
			}
			if in.All {
				for _, fl := range files {
					if what(fl) != "other" {
						add(fl)
					}
				}
			}
			if len(missing) > 0 {
				return nil, apperr.New(apperr.NotFound, "%s has no %s.", upperFirstRune(f.Words()), strings.Join(missing, ", ")).WithHint("List the folder to see what is in it.").
					WithCommand("metarush add cloud " + f.String() + " --list")
			}
			out := &AddPathsOut{Files: []AddedFile{}, Folders: []LinkedFolderOut{}}
			ver := ""
			if st := drive.CheckOf(ctx, a.Tools, f.Kind); st.Installed {
				ver = st.Version
			}
			for _, fl := range want {
				out.Files = append(out.Files, addCloudFile(ctx, a, f, fl, projectID, ver))
			}
			out.Summary = summarizeAdded(out)
			return out, nil
		})
}

// First-party ability (D269).
func init() {
	ops.Offer("add.cloud", ops.Offering{ID: "cloud-folders", Group: "bring", Title: "Files from Google Drive or Dropbox",
		Does:  "Brings footage and pictures in from a Google Drive or Dropbox folder through your own rclone, without syncing the folder.",
		Words: []string{"dropbox", "google drive", "cloud folder", "rclone"}, Needs: ops.Needs{Programs: []string{"rclone"}}})
}

// addCloudFile fetches one file and adds it.
func addCloudFile(ctx context.Context, a *app.App, f drive.Folder, fl drive.File, projectID, version string) AddedFile {
	af := AddedFile{Path: f.String() + "/" + fl.Name, Name: fl.Name, Route: resources.RouteUnknown}
	max := int64(cloudFootageMax)
	if what(fl) == "picture" {
		max = cloudPictureMax
	}
	// The folder was listed moments ago in this request: not again per file.
	got, err := drive.Fetch(ctx, a.Tools, f, fl, a.Cfg.Scratch.Dir, max, drive.JustListed)
	if err != nil {
		af.Status, af.Message = "failed", apperr.From(err).Message
		return af
	}
	defer os.RemoveAll(filepath.Dir(got.Path))
	service := drive.Service(f.Kind)
	if sn, _ := resources.SniffFile(got.Path); sn.Route == resources.RouteResource {
		kind := models.FromDrive
		if f.Kind == drive.DropboxType {
			kind = models.FromDropbox
		}
		scope := models.ScopeWorkspace
		if projectID != "" {
			scope = models.ScopeProject
		}
		r := addResourceFile(ctx, a, got.Path, resources.AddOptions{Scope: scope, ProjectID: projectID, FileName: fl.Name, Title: models.DefaultTitle(fl.Name),
			DriveFile: &models.ResourceDriveFile{ID: fl.ID, Folder: f.String(), Name: fl.Name, Checksum: fl.Checksum()},
			Source:    models.ResourceSource{Kind: kind, Name: fl.Name, URL: cloudURL(f, fl), Site: service, File: strings.TrimPrefix(f.Path+"/"+fl.Name, "/")}, Via: a.Actor.Via})
		r.Path, r.Name = af.Path, fl.Name // the file's own name, not the copy fetched into scratch
		return r
	}
	platform := models.PlatformDrive
	if f.Kind == drive.DropboxType {
		platform = models.PlatformDropbox
	}
	r := ingest.File(ctx, a, got.Path, ingest.Options{ProjectID: projectID, DisplayName: fl.Name, Title: models.DefaultTitle(fl.Name), Via: a.Actor.Via, Transcribe: true,
		Source: &models.AssetSource{Platform: platform, URL: cloudURL(f, fl), VideoID: fl.ID, Title: fl.Name, Tool: "rclone", ToolVersion: version, Method: models.SourceDownload, DownloadedAt: time.Now().UTC(),
			Checksum: fl.Checksum()}})
	af.Route, af.Status, af.AssetID, af.Message = resources.RouteFootage, r.Status, r.AssetID, r.Error
	switch r.Status {
	case "ingested", "resumed":
		af.Message = "Added " + r.Title + " from " + service + "."
	case "deduped", "duplicate":
		af.Message = fl.Name + " was already there."
	case "skipped":
		af.Route = resources.RouteUnknown
		af.Message = fl.Name + " isn't a video, sound, picture, PDF or font."
	}
	return af
}

func upperFirstRune(s string) string {
	for i, r := range s {
		if i == 0 && r >= 'a' && r <= 'z' {
			return string(r-32) + s[1:]
		}
		break
	}
	return s
}
