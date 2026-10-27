package ops

import (
	"context"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/playback"
	"github.com/metavert-io/metarush/internal/store"
)

// PreviewPrepareIn makes a revision's preview sources local.
type PreviewPrepareIn struct {
	RevisionID string `json:"revisionId"`
	Wait       bool   `json:"wait,omitempty" jsonschema:"description=Block until every source is local (the CLI); otherwise start fetching and report progress (the dashboard polls)."`
}

// PreviewSource is one media file a preview plays.
type PreviewSource struct {
	AssetID   string `json:"assetId"`
	Title     string `json:"title"`
	Role      string `json:"role" jsonschema:"enum=proxy,enum=original"`
	Size      int64  `json:"size"`
	BytesDone int64  `json:"bytesDone"`
	Local     bool   `json:"local" jsonschema:"description=Served from this machine (cache or a local media store)."`
	Error     string `json:"error,omitempty"`
}

// PreviewPrepareOut reports how much of the preview plays from this machine.
type PreviewPrepareOut struct {
	RevisionID string          `json:"revisionId"`
	Sources    []PreviewSource `json:"sources"`
	Ready      int             `json:"ready"`
	Total      int             `json:"total"`
	BytesDone  int64           `json:"bytesDone"`
	BytesTotal int64           `json:"bytesTotal"`
	Note       string          `json:"note"`
}

type fetchState struct {
	done, total int64
	err         string
	finished    bool
	ch          chan struct{}
}

type fetcher struct {
	mu sync.Mutex
	m  map[string]*fetchState // sha → state
}

var (
	fetchersMu sync.Mutex
	fetchers   = map[string]*fetcher{}
)

func fetcherFor(a *app.App) *fetcher {
	fetchersMu.Lock()
	defer fetchersMu.Unlock()
	f, ok := fetchers[a.Cfg.Cache.Dir]
	if !ok {
		f = &fetcher{m: map[string]*fetchState{}}
		fetchers[a.Cfg.Cache.Dir] = f
	}
	return f
}

// CacheInBackground starts (once per object and process) a verified download of
// an object into the local cache and returns its progress state. The dashboard's
// media endpoint calls it for preview sources so later reads are local.
func CacheInBackground(a *app.App, key, sha string, size int64) *fetchState {
	f := fetcherFor(a)
	f.mu.Lock()
	if st, ok := f.m[sha]; ok && (!st.finished || st.err == "") {
		f.mu.Unlock()
		return st
	}
	st := &fetchState{total: size, ch: make(chan struct{})}
	f.m[sha] = st
	f.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		_, err := a.Cache.Ensure(ctx, a.Media, key, sha, size, func(done, total int64) {
			f.mu.Lock()
			st.done, st.total = done, total
			f.mu.Unlock()
		})
		f.mu.Lock()
		st.finished = true
		if err != nil {
			st.err = err.Error()
		} else {
			st.done = size
		}
		f.mu.Unlock()
		close(st.ch)
	}()
	return st
}

func (f *fetcher) snapshot(st *fetchState) (done int64, finished bool, err string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return st.done, st.finished, st.err
}

// previewObject is the object a preview plays for an asset (as revision.plan picks it).
func previewObject(as *models.Asset) (role, key, sha string, size int64) {
	if v, ok := as.Variants[models.VariantProxy]; ok && as.Media.HasVideo {
		return "proxy", v.StorageKey, v.SHA256, v.Size
	}
	return "original", as.StorageKey, as.SHA256, as.Size
}

// LocalMedia reports whether an object is served from this machine without a fetch.
func LocalMedia(a *app.App, key, sha string, size int64) bool {
	if a.Cache.Has(sha, size) {
		return true
	}
	if fs, ok := a.Media.(*media.FS); ok {
		if _, err := fs.LocalPath(key); err == nil {
			return true
		}
	}
	return false
}

func init() {
	Def("preview.prepare", "Make a revision's preview sources (proxies, audio) local so the dashboard preview plays without buffering; reports progress", Flags{}, func(ctx context.Context, a *app.App, in *PreviewPrepareIn) (*PreviewPrepareOut, error) {
		r, _, err := getRevision(ctx, a, in.RevisionID)
		if err != nil {
			return nil, err
		}
		ids := specAssetIDs(&r.Spec)
		var assets []models.Asset
		if len(ids) > 0 {
			readAt := time.Now()
			if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}}, &assets); err != nil {
				return nil, err
			}
			// The dashboard's media endpoints remember what playing these
			// needs, so the whole cut keeps playing through a short database
			// outage, also the clips not reached yet (D180).
			playback.Remember(a, readAt, assets...)
		}
		out := &PreviewPrepareOut{RevisionID: r.ID, Sources: []PreviewSource{}}
		f := fetcherFor(a)
		var waits []*fetchState
		for i := range assets {
			as := &assets[i]
			role, key, sha, size := previewObject(as)
			src := PreviewSource{AssetID: as.ID, Title: as.DisplayTitle(), Role: role, Size: size}
			switch {
			case as.State != models.AssetAvailable || sha == "":
				src.Error = "not available yet"
			case LocalMedia(a, key, sha, size):
				src.Local, src.BytesDone = true, size
			default:
				st := CacheInBackground(a, key, sha, size)
				done, finished, ferr := f.snapshot(st)
				src.BytesDone, src.Error = done, ferr
				src.Local = finished && ferr == ""
				if !finished {
					waits = append(waits, st)
				}
			}
			out.Sources = append(out.Sources, src)
		}
		if in.Wait {
			for _, st := range waits {
				select {
				case <-st.ch:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			for i := range out.Sources {
				s := &out.Sources[i]
				if s.Error == "" && !s.Local {
					var as models.Asset
					for _, x := range assets {
						if x.ID == s.AssetID {
							as = x
						}
					}
					_, key, sha, size := previewObject(&as)
					s.Local = LocalMedia(a, key, sha, size)
					if s.Local {
						s.BytesDone = s.Size
					}
				}
			}
		}
		for _, s := range out.Sources {
			out.Total++
			out.BytesTotal += s.Size
			out.BytesDone += s.BytesDone
			if s.Local {
				out.Ready++
			}
		}
		out.Note = "The preview plays these files from this machine once they are copied here; until then MetaRush streams them."
		return out, nil
	})
}
