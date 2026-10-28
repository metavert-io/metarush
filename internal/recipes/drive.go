package recipes

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/drive"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// A Google Drive folder as an input, without syncing it (D220): an input's
// `from: {drive: "gdrive:Teaser/Art"}` names a folder of a Google Drive the
// person set up in their own rclone. The plan lists it (rclone lsjson) and
// takes the newest files the input needs — pictures or footage, by what they
// are — reusing a file a run fetched before while Google's checksum says it is
// the same; the run's first step fetches the others, one by one, by their
// Drive IDs (rclone backend copyid), checks each against the listing and keeps
// it by its content: pictures in the workspace library tied to their Drive
// file (a new version of the same picture when the file changes), footage as
// ordinary clips. Each item records the file's Drive ID, name, size, time and
// checksum, and once fetched its SHA-256. Checks never list a Drive (they use
// the recipe's own inputs); without rclone or the remote, the plan says how to
// set it up.

// Limits on what a run takes from a Drive folder.
const (
	drivePictureMax = resources.MaxFileBytes
	driveFootageMax = 64 << 30
)

// driveFolder is an input's Google Drive or Dropbox folder (D306; ok false
// when it has none).
func driveFolder(in models.RecipeInput) (drive.Folder, bool, error) {
	switch {
	case in.From == nil:
		return drive.Folder{}, false, nil
	case strings.TrimSpace(in.From.Drive) != "":
		f, err := drive.ParseFolder(in.From.Drive)
		return f, true, err
	case strings.TrimSpace(in.From.Dropbox) != "":
		f, err := drive.ParseFolderOf(drive.DropboxType, in.From.Dropbox)
		return f, true, err
	}
	return drive.Folder{}, false, nil
}

// cloudService names the service an input's folder is in.
func cloudService(in models.RecipeInput) string {
	if in.From != nil && strings.TrimSpace(in.From.Drive) == "" && strings.TrimSpace(in.From.Dropbox) != "" {
		return drive.Service(drive.DropboxType)
	}
	return drive.Service(drive.DriveType)
}

// pendingServices names the services a run still fetches files from
// ("Google Drive", "Dropbox", "Google Drive and Dropbox").
func pendingServices(ins []models.RecipeRunInput) string {
	var gd, db bool
	for _, in := range ins {
		for _, it := range in.Items {
			if pendingDrive(it) || (it.Drive != nil && it.Drive.Fetched) {
				if in.DriveKind == drive.DropboxType {
					db = true
				} else {
					gd = true
				}
			}
		}
	}
	switch {
	case gd && db:
		return "Google Drive and Dropbox"
	case db:
		return "Dropbox"
	}
	return "Google Drive"
}

// hasCloudFolder reports an input that takes its files from a Google Drive or Dropbox folder.
func hasCloudFolder(in models.RecipeInput) bool {
	return in.From != nil && (strings.TrimSpace(in.From.Drive) != "" || strings.TrimSpace(in.From.Dropbox) != "")
}

// setDriveFolders points inputs at Google Drive (or, with kind dropbox,
// Dropbox) folders (recipe.update fromDrive, fromDropbox): a folder replaces
// where the input took its values from before; an empty one takes the folder
// away.
func setDriveFolders(d *models.RecipeDoc, folders map[string]string, kind string) error {
	for _, name := range sortedKeys(folders) {
		var in *models.RecipeInput
		for i := range d.Inputs {
			if d.Inputs[i].Name == name {
				in = &d.Inputs[i]
			}
		}
		if in == nil {
			return apperr.New(apperr.InvalidInput, "The recipe has no input called “%s”.", name)
		}
		if in.Kind != models.RecipePictures && in.Kind != models.RecipeFootage {
			return apperr.New(apperr.InvalidInput, "Only pictures and footage come from a %s folder, so %s can't.", drive.Service(kind), inputLabel(*in))
		}
		v := strings.TrimSpace(folders[name])
		if v == "" {
			if in.From != nil {
				if kind == drive.DropboxType {
					in.From.Dropbox = ""
				} else {
					in.From.Drive = ""
				}
				if *in.From == (models.RecipeSource{}) {
					in.From = nil
				}
			}
			continue
		}
		f, err := drive.ParseFolderOf(kind, v)
		if err != nil {
			return err
		}
		if kind == drive.DropboxType {
			in.From = &models.RecipeSource{Dropbox: f.String()}
		} else {
			in.From = &models.RecipeSource{Drive: f.String()}
		}
	}
	return nil
}

// driveURL is a Drive file's address (provenance only: MetaRush never opens it).
func driveURL(id string) string { return "https://drive.google.com/file/d/" + id + "/view" }

// fileURL is where a file of a Google Drive or Dropbox folder is (provenance only).
func fileURL(f drive.Folder, id, name string) string {
	if f.Kind == drive.DropboxType {
		return "https://www.dropbox.com/home/" + strings.TrimPrefix(f.Path+"/"+name, "/")
	}
	return driveURL(id)
}

// pendingDrive reports an item still in Google Drive (the run fetches it).
func pendingDrive(it models.RecipeRunItem) bool { return it.ID == "" && it.Drive != nil }

// driveGot is what an input takes from a Drive (or Dropbox) folder.
type driveGot struct {
	items  []models.RecipeRunItem
	folder string
	kind   string // "" Google Drive, dropbox (D306)
	words  string
}

// fromDrive lists an input's Drive folder and takes the newest files of its
// kind, as many as it takes.
func (rs *resolver) fromDrive(in models.RecipeInput) (*driveGot, []string) {
	f, _, err := driveFolder(in)
	if err != nil {
		return nil, []string{fmt.Sprintf("%s comes from a %s folder MetaRush can't read: %s", upperFirst(inputLabel(in)), cloudService(in), strings.TrimSuffix(apperr.From(err).Message, ".")+".")}
	}
	files, err := rs.driveList(f)
	if err != nil {
		ae := apperr.From(err)
		msg := strings.TrimSuffix(ae.Message, ".") + "."
		if ae.Hint != "" {
			msg += " " + ae.Hint
		}
		return nil, []string{fmt.Sprintf("%s comes from %s: %s", upperFirst(inputLabel(in)), f.Words(), msg)}
	}
	want := countWanted(in)
	what := "picture"
	if in.Kind == models.RecipeFootage {
		what = "clip"
	}
	got := &driveGot{folder: f.String(), kind: f.Kind}
	for _, fl := range files {
		if len(got.items) >= want {
			break
		}
		max := int64(drivePictureMax)
		if in.Kind == models.RecipeFootage {
			if !fl.Footage() {
				continue
			}
			max = driveFootageMax
		} else if !fl.Picture() {
			continue
		}
		if fl.Size > max {
			continue // too large to take (said when it is the only one)
		}
		got.items = append(got.items, rs.driveItem(in.Kind, fl, f))
	}
	if len(got.items) == 0 {
		return nil, []string{fmt.Sprintf("%s has no %ss yet.", upperFirst(f.Words()), what)}
	}
	fetch := 0
	for _, it := range got.items {
		if pendingDrive(it) {
			fetch++
		}
	}
	got.words = "from " + f.Words()
	if fetch > 0 {
		got.words += fmt.Sprintf("; the run fetches %s", plural(fetch, "file"))
	}
	return got, nil
}

// driveList lists a folder once per plan.
func (rs *resolver) driveList(f drive.Folder) ([]drive.File, error) {
	if rs.drives == nil {
		rs.drives = map[string][]drive.File{}
	}
	if fs, ok := rs.drives[f.String()]; ok {
		return fs, nil
	}
	fs, err := drive.List(rs.ctx, rs.a.Tools, f)
	if err != nil {
		return nil, err
	}
	rs.drives[f.String()] = fs
	return fs, nil
}

// driveItem is a listed file as an input's item: what MetaRush already has
// of it when Google's checksum says it is the same, else the file to fetch.
func (rs *resolver) driveItem(kind string, fl drive.File, f drive.Folder) models.RecipeRunItem {
	df := &models.RecipeDriveFile{ID: fl.ID, Name: fl.Name, Size: fl.Size, Modified: fl.Modified, Checksum: fl.Checksum()}
	title := models.DefaultTitle(fl.Name)
	if fl.Checksum() != "" {
		switch kind {
		case models.RecipePictures:
			if r := resources.DriveFileResource(rs.ctx, rs.a, fl.ID); r != nil && !r.Removed && r.DriveFile != nil && r.DriveFile.Checksum == fl.Checksum() {
				if it, why := pictureItem(r, r.Current); why == "" {
					df.Fetched = true
					it.Drive = df
					return it
				}
			}
		case models.RecipeFootage:
			if as := driveAsset(rs.ctx, rs.a, fl.ID); as != nil && ((fl.SHA256 != "" && as.SHA256 == fl.SHA256) || (fl.Dropbox != "" && driveFetched(as, fl))) {
				it := clipItem(as)
				df.Fetched = true
				it.Drive = df
				return it
			}
		}
	}
	return models.RecipeRunItem{Title: title, SHA256: fl.SHA256, Drive: df}
}

// driveAsset is the newest clip fetched from a Drive file (nil none).
func driveAsset(ctx context.Context, a *app.App, driveID string) *models.Asset {
	var as []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"source.platform": bson.M{"$in": []string{models.PlatformDrive, models.PlatformDropbox}}, "source.videoId": driveID, "state": models.AssetAvailable,
		"trash": bson.M{"$exists": false}}, &as, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(1)); err != nil || len(as) == 0 {
		return nil
	}
	return &as[0]
}

// driveFetched: a clip fetched from a Dropbox file whose content hash is
// still the listing's (kept in the clip's source title as the checksum).
func driveFetched(as *models.Asset, fl drive.File) bool {
	return as.Source != nil && as.Source.Checksum != "" && as.Source.Checksum == fl.Checksum()
}

// pendingDriveFiles counts the files a run still fetches from Google Drive.
func pendingDriveFiles(ins []models.RecipeRunInput) int {
	n := 0
	for _, in := range ins {
		for _, it := range in.Items {
			if pendingDrive(it) {
				n++
			}
		}
	}
	return n
}

// itemKey is what an item is, for fingerprints and comparisons: its ID and
// SHA-256, or for a file still in Google Drive its Drive ID and checksum.
func itemKey(it models.RecipeRunItem) string {
	if pendingDrive(it) {
		return fmt.Sprintf("drive:%s@%s/%d/%s", it.Drive.ID, it.Drive.Checksum, it.Drive.Size, it.Drive.Modified.UTC().Format(time.RFC3339))
	}
	return it.ID + "@" + it.SHA256
}

// ---- the run: fetching ----

// fetchDrive fetches the files the run's inputs take from Google Drive and
// keeps them by their content (pictures in the workspace library tied to their
// Drive file, footage as clips), saving the run after each one; the run's
// lease is held while a file comes in.
func (e *engine) fetchDrive(ctx context.Context, st *models.RecipeStep) (int, error) {
	n := 0
	for i := range e.run.Inputs {
		in := &e.run.Inputs[i]
		for j := range in.Items {
			it := &in.Items[j]
			if !pendingDrive(*it) {
				continue
			}
			f, err := drive.ParseFolderOf(firstKind(in.DriveKind), in.Drive)
			if err != nil {
				return n, err
			}
			st.Detail = fmt.Sprintf("Fetching “%s” from %s.", it.Drive.Name, f.Words())
			stop := keepLease(ctx, e.a, e.run)
			err = e.fetchOne(ctx, in, it, f)
			stop()
			if err != nil {
				return n, err
			}
			n++
			if err := save(ctx, e.a, e.run); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

// fetchOne fetches one file and makes it the item's clip or picture.
func (e *engine) fetchOne(ctx context.Context, in *models.RecipeRunInput, it *models.RecipeRunItem, f drive.Folder) error {
	fl := drive.File{ID: it.Drive.ID, Name: it.Drive.Name, Size: it.Drive.Size, Modified: it.Drive.Modified}
	fl.SetChecksum(it.Drive.Checksum)
	service, kind, platform := drive.Service(f.Kind), models.FromDrive, models.PlatformDrive
	if f.Kind == drive.DropboxType {
		kind, platform = models.FromDropbox, models.PlatformDropbox
	}
	max := int64(drivePictureMax)
	if in.Kind == models.RecipeFootage {
		max = driveFootageMax
	}
	got, err := drive.Fetch(ctx, e.a.Tools, f, fl, e.a.Cfg.Scratch.Dir, max)
	if err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Dir(got.Path))
	switch in.Kind {
	case models.RecipePictures:
		res, err := resources.AddFile(ctx, e.a, got.Path, resources.AddOptions{Scope: models.ScopeWorkspace, FileName: fl.Name, Title: models.DefaultTitle(fl.Name),
			DriveFile: &models.ResourceDriveFile{ID: fl.ID, Folder: f.String(), Name: fl.Name, Checksum: it.Drive.Checksum},
			Source:    models.ResourceSource{Kind: kind, Name: fl.Name, URL: fileURL(f, fl.ID, fl.Name), Site: service, File: strings.TrimPrefix(f.Path+"/"+fl.Name, "/")}, Via: models.ViaRecipe})
		if err != nil {
			if err == resources.ErrNotResource {
				return apperr.New(apperr.InvalidInput, "“%s” in %s isn't a picture.", fl.Name, f.Words())
			}
			return err
		}
		pic, why := pictureItem(res.Resource, res.SHA256)
		if why != "" {
			return apperr.New(apperr.Conflict, "%s", why)
		}
		pic.Drive = it.Drive
		*it = pic
	case models.RecipeFootage:
		ver := ""
		if st := drive.CheckOf(ctx, e.a.Tools, firstKind(f.Kind)); st.Installed {
			ver = st.Version
		}
		r := ingest.File(ctx, e.a, got.Path, ingest.Options{FootageOnly: true, DisplayName: fl.Name, Title: models.DefaultTitle(fl.Name), Duplicates: ingest.DuplicatesKeep, Via: models.ViaRecipe,
			Source: &models.AssetSource{Platform: platform, URL: fileURL(f, fl.ID, fl.Name), VideoID: fl.ID, Title: fl.Name, Tool: "rclone", ToolVersion: ver,
				Method: models.SourceDownload, DownloadedAt: time.Now().UTC(), Checksum: fl.Checksum()}})
		if r.AssetID == "" || r.Status == "failed" || r.Status == "skipped" {
			why := strings.TrimSpace(r.Error)
			if why == "" {
				why = "it isn't video or sound MetaRush can read"
			}
			return apperr.New(apperr.InvalidInput, "“%s” from %s couldn't be added: %s", fl.Name, f.Words(), strings.TrimSuffix(why, ".")+".")
		}
		var as models.Asset
		if err := e.a.DB.GetByID(ctx, store.CAssets, e.a.WorkspaceID(), r.AssetID, &as); err != nil {
			return err
		}
		clip := clipItem(&as)
		clip.Drive = it.Drive
		*it = clip
	default:
		return apperr.New(apperr.InvalidInput, "Only pictures and footage come from a %s folder.", drive.Service(f.Kind))
	}
	it.Drive.Fetched = true
	return nil
}

// firstKind is a folder's kind, Google Drive when none is said.
func firstKind(k string) string {
	if k == "" {
		return drive.DriveType
	}
	return k
}

// keepLease pushes a run's lease forward every minute while one look works a
// long time (a file coming in from Google Drive), so no other look takes the
// run meanwhile; stop ends it.
func keepLease(ctx context.Context, a *app.App, run *models.RecipeRun) (stop func()) {
	if run.Lease == nil || run.Lease.By == "" {
		return func() {}
	}
	by := run.Lease.By
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				_, _ = a.DB.C(store.CRecipeRuns).UpdateOne(context.WithoutCancel(ctx), bson.M{"_id": run.ID, "workspaceId": a.WorkspaceID(), "lease.by": by},
					leaseFrom(by))
			}
		}
	}()
	return func() { close(done) }
}
