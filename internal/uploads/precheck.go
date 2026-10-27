package uploads

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// PrecheckFile is one file the dashboard (or `metarush ingest precheck`) is
// about to upload.
type PrecheckFile struct {
	Name        string `json:"name" jsonschema:"minLength=1,maxLength=1024"`
	Size        int64  `json:"size" jsonschema:"minimum=1"`
	Fingerprint string `json:"fingerprint" jsonschema:"description=fp1:<sha256 of the first and last MiB>; see media.Fingerprint."`
	Sample      string `json:"sample,omitempty" jsonschema:"description=sp1:<sha256 of 16 evenly spaced 64 KiB chunks>; confirms a fingerprint match (media.SampleOffsets)."`
}

// PrecheckIn checks a batch before uploading it.
type PrecheckIn struct {
	Files     []PrecheckFile `json:"files" jsonschema:"minItems=1,maxItems=2000"`
	ProjectID string         `json:"projectId,omitempty" jsonschema:"description=Where the files are going (to say whether a match is already there)."`
}

// PrecheckDuplicate is an existing asset a file appears identical to.
type PrecheckDuplicate struct {
	models.DuplicateOf
	SameName bool `json:"sameName" jsonschema:"description=The file has the asset's filename (or one of its aliases)."`
	InTarget bool `json:"inTarget" jsonschema:"description=The asset is already in the target project (or the inbox when no project is given)."`
}

// PrecheckResume is an interrupted upload of the same file on this machine.
type PrecheckResume struct {
	UploadID string `json:"uploadId"`
	Offset   int64  `json:"offset"`
	Size     int64  `json:"size"`
}

// PrecheckResult is the verdict for one file (same order as the input).
type PrecheckResult struct {
	Index     int                `json:"index"`
	Duplicate *PrecheckDuplicate `json:"duplicate,omitempty"`
	// BatchDuplicateOf is the index of an earlier file in the same batch with the
	// same size and fingerprint.
	BatchDuplicateOf *int            `json:"batchDuplicateOf,omitempty"`
	Resume           *PrecheckResume `json:"resume,omitempty"`
}

// PrecheckOut lists verdicts. A fingerprint match is a strong hint, not proof:
// the full SHA-256 after upload stays authoritative.
type PrecheckOut struct {
	Results    []PrecheckResult `json:"results"`
	Duplicates int              `json:"duplicates"`
	Resumable  int              `json:"resumable"`
	Backfilled int              `json:"backfilled" jsonschema:"description=Older assets whose fingerprint was computed now (ranged reads from the store)."`
	Note       string           `json:"note"`
}

// maxBackfill bounds how many older assets one precheck fingerprints on demand.
const maxBackfill = 24

// Precheck matches files against stored assets (same size, then fingerprint;
// fingerprints of older assets are computed on demand with ranged reads),
// against each other, and against interrupted uploads that can resume.
func Precheck(ctx context.Context, a *app.App, in *PrecheckIn) (*PrecheckOut, error) {
	out := &PrecheckOut{Results: make([]PrecheckResult, len(in.Files))}
	sizes := bson.A{}
	seenSize := map[int64]bool{}
	for i, f := range in.Files {
		if f.Size <= 0 || strings.TrimSpace(f.Name) == "" {
			return nil, apperr.New(apperr.InvalidInput, "File %d needs a name and a size.", i+1).WithDetails(map[string]any{"index": i})
		}
		if f.Fingerprint != "" && !strings.HasPrefix(f.Fingerprint, media.FingerprintPrefix) {
			return nil, apperr.New(apperr.InvalidInput, "File %d's fingerprint isn't one MetaRush understands.", i+1).
				WithDetails(map[string]any{"index": i, "fingerprint": f.Fingerprint, "expected": media.FingerprintPrefix + "…"})
		}
		out.Results[i].Index = i
		if !seenSize[f.Size] {
			seenSize[f.Size] = true
			sizes = append(sizes, f.Size)
		}
	}
	var candidates []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"size": bson.M{"$in": sizes}, "state": models.AssetAvailable}, &candidates,
		options.Find().SetProjection(bson.M{"media.streams": 0, "variants": 0}).SetLimit(500)); err != nil {
		return nil, err
	}
	bySize := map[int64][]*models.Asset{}
	for i := range candidates {
		bySize[candidates[i].Size] = append(bySize[candidates[i].Size], &candidates[i])
	}
	fingerprint := func(as *models.Asset) string {
		if as.Fingerprint != "" || out.Backfilled >= maxBackfill {
			return as.Fingerprint
		}
		fctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		fp, err := media.FingerprintObject(fctx, a.Media, as.StorageKey, as.Size)
		if err != nil {
			return ""
		}
		as.Fingerprint = fp
		out.Backfilled++
		_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"fingerprint": fp}})
		return fp
	}
	// confirmed checks the sampled hash when the client sent one; a mismatch is a
	// fingerprint false positive, and that file simply uploads normally.
	confirmed := func(as *models.Asset, want string) bool {
		if want == "" {
			return true
		}
		if as.Sample == "" {
			sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			s, err := media.SampleObject(sctx, a.Media, as.StorageKey, as.Size)
			if err != nil {
				return false // cannot confirm: let the upload and the full SHA-256 decide
			}
			as.Sample = s
			_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"sample": s}})
		}
		return as.Sample == want
	}
	first := map[string]int{}
	ups := For(a.Cfg.StateDir())
	for i, f := range in.Files {
		name := filepath.Base(strings.ReplaceAll(f.Name, "\\", "/"))
		if f.Fingerprint != "" {
			k := f.Fingerprint + "|" + f.Sample + "|" + itoa(f.Size)
			if j, ok := first[k]; ok {
				jj := j
				out.Results[i].BatchDuplicateOf = &jj
			} else {
				first[k] = i
			}
			for _, as := range bySize[f.Size] {
				if fingerprint(as) != f.Fingerprint || !confirmed(as, f.Sample) {
					continue
				}
				d := &PrecheckDuplicate{DuplicateOf: ops.DescribeDuplicate(ctx, a, as), SameName: as.HasName(name)}
				if in.ProjectID == "" {
					d.InTarget = len(as.ProjectIDs) == 0 && as.Trash == nil // filed away counts; the Trash doesn't (D98)
				} else {
					for _, p := range as.ProjectIDs {
						d.InTarget = d.InTarget || p == in.ProjectID
					}
				}
				out.Results[i].Duplicate = d
				out.Duplicates++
				break
			}
		}
		if out.Results[i].Duplicate == nil {
			if r, ok := ups.FindResumable(name, f.Size, f.Fingerprint); ok {
				out.Results[i].Resume = &PrecheckResume{UploadID: r.ID, Offset: r.Offset, Size: r.Size}
				out.Resumable++
			}
		}
	}
	out.Note = "A match means the same size and the same fingerprint at the start and end of the file, checked again across the whole file. Files that don't match upload normally, and the full check after the upload has the final say."
	return out, nil
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func init() {
	ops.Def("asset.precheck", "Before uploading: find files identical to stored assets (size + first/last-MiB fingerprint), duplicates within the batch, and interrupted uploads that can resume", ops.Flags{}, Precheck)
}
