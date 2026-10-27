package ops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/store"
)

// Project folders carry their motion graphics in motion/<name>/ (D68). The
// lock records, for each one the cut uses, its version and whether a person
// approved it; push makes sure the media store holds every version the cut
// uses; checkout and pull write the sources of versions the folder lacks.

// FolderMotion is one motion graphic in .metarush/lock.json.
type FolderMotion struct {
	Hash       string  `json:"hash"`
	State      string  `json:"state" jsonschema:"enum=waiting,enum=approved,enum=rejected,enum=revoked,enum=unknown"`
	ApprovedBy string  `json:"approvedBy,omitempty"`
	ApprovedAt *string `json:"approvedAt,omitempty"`
}

// FolderMotionOut is a motion graphic's state in a project folder.
type FolderMotionOut struct {
	Name       string `json:"name"`
	Version    string `json:"version,omitempty" jsonschema:"description=The version the cut uses (name@version)."`
	Folder     string `json:"folder,omitempty" jsonschema:"description=The version in motion/<name> now\\, when it differs from the one the cut uses."`
	State      string `json:"state" jsonschema:"enum=waiting,enum=approved,enum=rejected,enum=revoked,enum=unknown"`
	StateLabel string `json:"stateLabel"`
	Used       bool   `json:"used" jsonschema:"description=The cut uses it."`
	Message    string `json:"message,omitempty" jsonschema:"description=What to do about it\\, in plain words."`
}

func motionLabel(state string) string {
	if l, ok := stateLabels[state]; ok {
		return l
	}
	return "Not checked yet"
}

// folderMotion compares the folder's motion/ components with the versions the
// cut uses. When upload is set, versions the cut uses are put in the media
// store if it lacks them.
func folderMotion(ctx context.Context, a *app.App, dir string, s *editspec.Spec, upload bool) ([]FolderMotionOut, map[string]FolderMotion, error) {
	used := map[string]string{} // name → hash the cut uses
	if s != nil {
		for _, o := range s.Overlays {
			if o.Motion != nil {
				used[o.Motion.Name] = o.Motion.Hash
			}
		}
	}
	inFolder := map[string]*motion.Archive{}
	entries, _ := os.ReadDir(filepath.Join(dir, "motion"))
	for _, e := range entries {
		if !e.IsDir() || !motion.ValidName(e.Name()) {
			continue
		}
		if ar, err := motion.Build(filepath.Join(dir, "motion", e.Name())); err == nil {
			inFolder[e.Name()] = ar
		}
	}
	names := map[string]bool{}
	for n := range used {
		names[n] = true
	}
	for n := range inFolder {
		names[n] = true
	}
	var order []string
	for n := range names {
		order = append(order, n)
	}
	sort.Strings(order)
	lock := map[string]FolderMotion{}
	out := []FolderMotionOut{}
	for _, n := range order {
		row := FolderMotionOut{Name: n, State: "unknown"}
		hash, isUsed := used[n]
		row.Used = isUsed
		if !isUsed {
			hash = inFolder[n].Hash
		}
		row.Version = n + "@" + motion.Short(hash)
		c, err := motion.Get(ctx, a.DB, a.WorkspaceID(), hash)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			return nil, nil, err
		default:
			row.State = c.State
			if upload && isUsed && inFolder[n] != nil && inFolder[n].Hash == hash {
				if err := ensureArchive(ctx, a, c, inFolder[n]); err != nil {
					return nil, nil, err
				}
			}
		}
		row.StateLabel = motionLabel(row.State)
		if ar := inFolder[n]; ar != nil && isUsed && ar.Hash != hash {
			row.Folder = n + "@" + motion.Short(ar.Hash)
			row.Message = "The folder copy of " + n + " has changes the cut doesn't use yet: check it, have a person approve it, then switch the cut to the new version."
		}
		switch {
		case row.Message != "":
		case row.State == "unknown" && isUsed:
			row.Message = "The cut uses a version of " + n + " that this workspace hasn't checked yet. Check it, then a person approves it."
		case row.State == "unknown":
			row.Message = "Not checked yet. Check it before a cut uses it."
		case row.State == models.MotionWaiting:
			row.Message = "Waiting for a person to approve it."
		case row.State == models.MotionRevoked || row.State == models.MotionRejected:
			row.Message = "It can't be rendered: " + row.StateLabel + "."
		}
		out = append(out, row)
		if isUsed {
			fm := FolderMotion{Hash: hash, State: row.State}
			if c != nil && c.ApprovedBy != nil && c.ApprovedAt != nil {
				at := c.ApprovedAt.UTC().Format("2006-01-02T15:04:05Z")
				fm.ApprovedBy, fm.ApprovedAt = c.ApprovedBy.UserName, &at
			}
			lock[n] = fm
		}
	}
	return out, lock, nil
}

// writeMotionSources writes motion/<name>/ for versions the cut uses that the
// folder doesn't have yet (never over a folder that exists).
func writeMotionSources(ctx context.Context, a *app.App, dir string, s *editspec.Spec) {
	if s == nil {
		return
	}
	for _, o := range s.Overlays {
		if o.Motion == nil || !motion.ValidName(o.Motion.Name) {
			continue
		}
		dst := filepath.Join(dir, "motion", o.Motion.Name)
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		c, err := motion.Get(ctx, a.DB, a.WorkspaceID(), o.Motion.Hash)
		if err != nil {
			continue
		}
		ar, err := loadArchive(ctx, a, c)
		if err != nil {
			continue
		}
		_ = ar.Extract(dst)
	}
}

// ensureArchive puts a version's archive in the media store when the store
// lacks it (content-addressed: the same bytes, the same key).
func ensureArchive(ctx context.Context, a *app.App, c *models.MotionComponent, ar *motion.Archive) error {
	if _, err := a.Media.Stat(ctx, c.Archive.Key); !errors.Is(err, media.ErrNotFound) {
		return err
	}
	tmp, err := os.CreateTemp("", "metarush-motion-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(ar.Bytes); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	_, err = a.Media.PutFile(ctx, c.Archive.Key, tmp.Name(), media.PutOptions{ContentType: "application/json", SHA256: ar.Hash}, media.Resume{}, nil)
	return err
}
