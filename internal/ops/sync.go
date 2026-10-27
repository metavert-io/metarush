package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/cache"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// SyncStatusIn scopes a sync report.
type SyncStatusIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=Limit assets, renders, ingest sessions and jobs to one project."`
}

// SyncObject is one stored object and whether this machine has it.
type SyncObject struct {
	Role         string `json:"role"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	Cached       bool   `json:"cached"`
	Pinned       bool   `json:"pinned"`
	InUse        bool   `json:"inUse"`
	Needed       bool   `json:"needed"` // part of what `cache prefetch` fetches for offline render/review
	PartialBytes int64  `json:"partialBytes,omitempty"`
}

// SyncAsset is an asset's objects: local copy vs canonical only.
type SyncAsset struct {
	ID                 string       `json:"id"`
	Name               string       `json:"name"`
	Kind               string       `json:"kind"`
	State              string       `json:"state"`
	Objects            []SyncObject `json:"objects"`
	CachedBytes        int64        `json:"cachedBytes"`
	CanonicalOnlyBytes int64        `json:"canonicalOnlyBytes"`
	RenderReady        bool         `json:"renderReady"` // every needed object (render source, proxy, review variants) is local
}

// SyncRender is a finished render's objects.
type SyncRender struct {
	ID             string       `json:"id"`
	ProjectID      string       `json:"projectId"`
	RevisionID     string       `json:"revisionId"`
	RevisionNumber int          `json:"revisionNumber"`
	Preset         string       `json:"preset"`
	Quality        string       `json:"quality"`
	Objects        []SyncObject `json:"objects"`
}

// SyncTotals counts objects in scope.
type SyncTotals struct {
	Objects            int   `json:"objects"`
	Cached             int   `json:"cached"`
	CanonicalOnly      int   `json:"canonicalOnly"`
	Pinned             int   `json:"pinned"`
	CachedBytes        int64 `json:"cachedBytes"`
	CanonicalOnlyBytes int64 `json:"canonicalOnlyBytes"`
	NeededMissing      int   `json:"neededMissing"` // needed objects that are canonical-only
}

// SyncIngestSession is an ingest that has not finished.
type SyncIngestSession struct {
	ID             string    `json:"id"`
	SourceName     string    `json:"sourceName"`
	AssetID        string    `json:"assetId,omitempty"`
	ProjectID      string    `json:"projectId,omitempty"`
	InstallationID string    `json:"installationId"`
	State          string    `json:"state"`
	Status         string    `json:"status" jsonschema:"enum=active,enum=interrupted,enum=failed"`
	BytesDone      int64     `json:"bytesDone"`
	Size           int64     `json:"size"`
	Error          string    `json:"error,omitempty"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// SyncJob is a queued or running job.
type SyncJob struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	State     string    `json:"state"`
	Heavy     bool      `json:"heavy"`
	AssetID   string    `json:"assetId,omitempty"`
	RenderID  string    `json:"renderId,omitempty"`
	ProjectID string    `json:"projectId,omitempty"`
	Pct       float64   `json:"pct"`
	Message   string    `json:"message,omitempty"`
	Worker    string    `json:"worker,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// SyncJobs summarizes the queue.
type SyncJobs struct {
	Queued  int       `json:"queued"`
	Running int       `json:"running"`
	Jobs    []SyncJob `json:"jobs"`
}

// SyncDaemon is this installation's background daemon.
type SyncDaemon struct {
	Running   bool       `json:"running"`
	Healthy   bool       `json:"healthy"`
	PID       int        `json:"pid,omitempty"`
	Port      int        `json:"port,omitempty"`
	Version   string     `json:"version,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
}

// SyncWorker is a worker in the workspace.
type SyncWorker struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Hostname      string    `json:"hostname"`
	ThisMachine   bool      `json:"thisMachine"`
	Online        bool      `json:"online"`
	Running       int       `json:"running"`
	Concurrency   int       `json:"concurrency"`
	AcceptsHeavy  bool      `json:"acceptsHeavy"`
	OnBattery     bool      `json:"onBattery"`
	LastHeartbeat time.Time `json:"lastHeartbeat"`
}

// Reachability is a timed connectivity check.
type Reachability struct {
	Reachable bool    `json:"reachable"`
	LatencyMs float64 `json:"latencyMs"`
	Target    string  `json:"target"`
	Error     string  `json:"error,omitempty"`
}

// SyncStatusOut is what is local vs canonical-only, plus what is in flight.
type SyncStatusOut struct {
	GeneratedAt      time.Time           `json:"generatedAt"`
	InstallationID   string              `json:"installationId"`
	InstallationName string              `json:"installationName"`
	ProjectID        string              `json:"projectId,omitempty"`
	Assets           []SyncAsset         `json:"assets"`
	Renders          []SyncRender        `json:"renders"`
	Totals           SyncTotals          `json:"totals"`
	OfflineReady     bool                `json:"offlineReady"` // every needed object in scope is local
	Truncated        bool                `json:"truncated,omitempty"`
	Ingest           []SyncIngestSession `json:"ingest"`
	Jobs             SyncJobs            `json:"jobs"`
	Daemon           SyncDaemon          `json:"daemon"`
	Workers          []SyncWorker        `json:"workers"`
	ThisWorkerOnline bool                `json:"thisWorkerOnline"`
	Database         Reachability        `json:"database"`
	MediaStore       Reachability        `json:"mediaStore"`
	Cache            cache.Stats         `json:"cache"`
	Summary          []string            `json:"summary"`
}

const (
	syncLimit        = 1000
	workerOnlineFor  = 45 * time.Second
	ingestActiveWith = 30 * time.Second
)

// SyncStatus builds the report.
func SyncStatus(ctx context.Context, a *app.App, in *SyncStatusIn) (*SyncStatusOut, error) {
	return syncStatus(ctx, a, in, nil)
}

// SyncStatusOffline reports local state and reachability when the database
// could not be opened (a.DB == nil, e.g. on a plane); dbErr says why.
func SyncStatusOffline(ctx context.Context, a *app.App, in *SyncStatusIn, dbErr error) (*SyncStatusOut, error) {
	return syncStatus(ctx, a, in, dbErr)
}

func syncStatus(ctx context.Context, a *app.App, in *SyncStatusIn, dbErr error) (*SyncStatusOut, error) {
	out := &SyncStatusOut{GeneratedAt: time.Now().UTC(), InstallationID: a.Cfg.Installation.ID, InstallationName: a.Cfg.Installation.Name, ProjectID: in.ProjectID,
		Assets: []SyncAsset{}, Renders: []SyncRender{}, Ingest: []SyncIngestSession{}, Jobs: SyncJobs{Jobs: []SyncJob{}}, Workers: []SyncWorker{}, Summary: []string{}}
	out.Database = pingDB(ctx, a)
	if dbErr != nil {
		out.Database.Error = dbErr.Error()
	}
	out.MediaStore = pingStore(ctx, a.Media)
	out.Daemon = daemonState(a)
	if st, err := a.Cache.Status(); err == nil {
		out.Cache = st
	}
	if a.DB != nil && out.Database.Reachable {
		if err := syncShared(ctx, a, in, out); err != nil {
			return nil, err
		}
	}
	out.Summary = syncSummary(a, out)
	return out, nil
}

func syncShared(ctx context.Context, a *app.App, in *SyncStatusIn, out *SyncStatusOut) error {
	pins, inUse := a.Cache.Pinned(), a.Cache.InUse()
	obj := func(r objectRef, needed bool) SyncObject {
		o := SyncObject{Role: r.role, SHA256: r.sha, Size: r.size, Cached: a.Cache.Has(r.sha, r.size), Pinned: pins[r.sha], InUse: inUse[r.sha], Needed: needed}
		if !o.Cached {
			o.PartialBytes = a.Cache.PartialSize(r.sha)
		}
		if o.Size < 0 {
			if st, err := os.Stat(a.Cache.Path(r.sha)); err == nil {
				o.Size = st.Size()
			} else {
				o.Size = 0
			}
		}
		out.Totals.Objects++
		if o.Pinned {
			out.Totals.Pinned++
		}
		if o.Cached {
			out.Totals.Cached++
			out.Totals.CachedBytes += o.Size
		} else {
			out.Totals.CanonicalOnly++
			out.Totals.CanonicalOnlyBytes += o.Size
			if needed {
				out.Totals.NeededMissing++
			}
		}
		return o
	}
	assetFilter, renderFilter := bson.M{}, bson.M{"state": models.RenderAvailable}
	if in.ProjectID != "" {
		var p models.Project
		if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), in.ProjectID, &p); err != nil {
			return notFound("project", in.ProjectID)
		}
		assetFilter["projectIds"] = in.ProjectID
		renderFilter["projectId"] = in.ProjectID
	}
	var assets []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), assetFilter, &assets, options.Find().SetSort(bson.D{{Key: "updatedAt", Value: -1}}).SetLimit(syncLimit+1)); err != nil {
		return err
	}
	if len(assets) > syncLimit {
		assets, out.Truncated = assets[:syncLimit], true
	}
	assetIDs := make([]string, 0, len(assets))
	for i := range assets {
		as := &assets[i]
		assetIDs = append(assetIDs, as.ID)
		sa := SyncAsset{ID: as.ID, Name: as.Name, Kind: as.Kind, State: as.State, Objects: []SyncObject{}}
		if as.State == models.AssetAvailable {
			needed := map[string]bool{}
			for _, r := range assetObjects(as, false) { // exactly what prefetch fetches
				needed[r.role+":"+r.sha] = true
			}
			ready := true
			for _, r := range assetObjects(as, true) {
				o := obj(r, needed[r.role+":"+r.sha])
				sa.Objects = append(sa.Objects, o)
				if o.Cached {
					sa.CachedBytes += o.Size
				} else {
					sa.CanonicalOnlyBytes += o.Size
					if o.Needed {
						ready = false
					}
				}
			}
			sa.RenderReady = ready
		}
		out.Assets = append(out.Assets, sa)
	}
	var renders []models.Render
	if err := a.DB.Find(ctx, store.CRenders, a.WorkspaceID(), renderFilter, &renders, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(syncLimit+1)); err != nil {
		return err
	}
	if len(renders) > syncLimit {
		renders, out.Truncated = renders[:syncLimit], true
	}
	for i := range renders {
		rd := &renders[i]
		sr := SyncRender{ID: rd.ID, ProjectID: rd.ProjectID, RevisionID: rd.RevisionID, RevisionNumber: rd.RevisionNumber, Preset: rd.Preset, Quality: rd.Quality, Objects: []SyncObject{}}
		for _, r := range renderObjects(rd) {
			sr.Objects = append(sr.Objects, obj(r, i == 0)) // prefetch fetches the newest render
		}
		out.Renders = append(out.Renders, sr)
	}
	out.OfflineReady = out.Totals.Objects > 0 && out.Totals.NeededMissing == 0
	// Ingest sessions that have not finished.
	ingFilter := bson.M{"state": bson.M{"$in": bson.A{"hashing", "uploading", "verifying", "failed"}}}
	if in.ProjectID != "" {
		ingFilter["projectId"] = in.ProjectID
	}
	var sessions []models.IngestSession
	if err := a.DB.Find(ctx, store.CIngest, a.WorkspaceID(), ingFilter, &sessions, options.Find().SetSort(bson.D{{Key: "updatedAt", Value: -1}}).SetLimit(200)); err != nil {
		return err
	}
	for _, s := range sessions {
		st := "active"
		switch {
		case s.State == "failed":
			st = "failed"
		case time.Since(s.UpdatedAt) > ingestActiveWith:
			st = "interrupted"
		}
		out.Ingest = append(out.Ingest, SyncIngestSession{ID: s.ID, SourceName: s.SourceName, AssetID: s.AssetID, ProjectID: s.ProjectID, InstallationID: s.InstallationID,
			State: s.State, Status: st, BytesDone: s.BytesDone, Size: s.Size, Error: s.Error, UpdatedAt: s.UpdatedAt})
	}
	// Queued and running jobs.
	jobFilter := bson.M{"state": bson.M{"$in": bson.A{models.JobQueued, models.JobRunning}}}
	if in.ProjectID != "" {
		jobFilter["$or"] = bson.A{bson.M{"projectId": in.ProjectID}, bson.M{"assetId": bson.M{"$in": assetIDs}}}
	}
	js, err := a.Queue.List(ctx, jobFilter, 200)
	if err != nil {
		return err
	}
	for _, j := range js {
		sj := SyncJob{ID: j.ID, Type: j.Type, State: j.State, Heavy: j.Heavy, AssetID: j.AssetID, RenderID: j.RenderID, ProjectID: j.ProjectID, Pct: j.Progress.Pct, Message: j.Progress.Message, UpdatedAt: j.UpdatedAt}
		if j.Lease != nil && j.State == models.JobRunning {
			sj.Worker = j.Lease.OwnerName
		}
		if j.State == models.JobRunning {
			out.Jobs.Running++
		} else {
			out.Jobs.Queued++
		}
		out.Jobs.Jobs = append(out.Jobs.Jobs, sj)
	}
	// Workers.
	var ws []models.WorkerDevice
	if err := a.DB.Find(ctx, store.CWorkers, a.WorkspaceID(), bson.M{}, &ws, options.Find().SetSort(bson.D{{Key: "lastHeartbeat", Value: -1}}).SetLimit(100)); err != nil {
		return err
	}
	for _, w := range ws {
		sw := SyncWorker{ID: w.ID, Name: w.Name, Hostname: w.Hostname, ThisMachine: w.ID == a.Cfg.Installation.ID, Online: time.Since(w.LastHeartbeat) < workerOnlineFor,
			Running: w.Running, Concurrency: w.Concurrency, AcceptsHeavy: w.AcceptsHeavy, OnBattery: w.OnBattery, LastHeartbeat: w.LastHeartbeat}
		if sw.ThisMachine && sw.Online {
			out.ThisWorkerOnline = true
		}
		out.Workers = append(out.Workers, sw)
	}
	return nil
}

func pingDB(ctx context.Context, a *app.App) Reachability {
	r := Reachability{Target: a.Cfg.Database.Name}
	if a.DB == nil {
		r.Error = "not connected"
		return r
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	start := time.Now()
	err := a.DB.Client.Ping(c, nil)
	r.LatencyMs = float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		r.Error = err.Error()
		return r
	}
	r.Reachable = true
	return r
}

// pingStore makes one read-only request (a metadata lookup of an object that
// does not exist): "not found" proves the store answered and the credentials work.
func pingStore(ctx context.Context, st media.Store) Reachability {
	if st == nil {
		return Reachability{Error: "not configured"}
	}
	r := Reachability{Target: st.Locator()}
	if fs, ok := st.(*media.FS); ok {
		start := time.Now()
		info, err := os.Stat(fs.Root)
		r.LatencyMs = float64(time.Since(start).Microseconds()) / 1000
		if err != nil || !info.IsDir() {
			r.Error = "the storage folder can't be reached (is its drive connected?)"
			return r
		}
		r.Reachable = true
		return r
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := time.Now()
	_, err := st.Stat(c, "probes/sync-status-reachability")
	r.LatencyMs = float64(time.Since(start).Microseconds()) / 1000
	if err != nil && !errors.Is(err, media.ErrNotFound) {
		r.Error = err.Error()
		return r
	}
	r.Reachable = true
	return r
}

// daemonState reads state/daemon.json (written by `metarush serve`) and checks
// the daemon answers its health endpoint.
func daemonState(a *app.App) SyncDaemon {
	var st struct {
		PID       int       `json:"pid"`
		Port      int       `json:"port"`
		StartedAt time.Time `json:"startedAt"`
		Version   string    `json:"version"`
	}
	b, err := os.ReadFile(filepath.Join(a.Cfg.StateDir(), "daemon.json"))
	if err != nil || json.Unmarshal(b, &st) != nil || !cache.ProcessAlive(st.PID) {
		return SyncDaemon{}
	}
	d := SyncDaemon{Running: true, PID: st.PID, Port: st.Port, Version: st.Version, StartedAt: &st.StartedAt}
	client := http.Client{Timeout: 1500 * time.Millisecond}
	if res, err := client.Get(fmt.Sprintf("http://%s:%d/api/health", a.Cfg.Dashboard.Host, st.Port)); err == nil {
		res.Body.Close()
		d.Healthy = res.StatusCode == http.StatusOK
	}
	return d
}

func syncSummary(a *app.App, o *SyncStatusOut) []string {
	var s []string
	scope := "this workspace"
	if o.ProjectID != "" {
		scope = "this project"
	}
	if a.DB == nil || !o.Database.Reachable {
		s = append(s, "MetaRush can't reach its database, so this shows only what is on this machine: "+o.Database.Error)
	} else {
		t := o.Totals
		line := fmt.Sprintf("This machine has copies of %d of the %d files %s uses (%s); %d are only in MetaRush's storage (%s); %d are kept here for offline work.",
			t.Cached, t.Objects, scope, fmtSize(t.CachedBytes), t.CanonicalOnly, fmtSize(t.CanonicalOnlyBytes), t.Pinned)
		if o.Truncated {
			line += " (Only the first 1,000 clips and renders were counted.)"
		}
		s = append(s, line)
		switch {
		case o.ProjectID != "" && o.OfflineReady:
			s = append(s, "Ready to work offline: everything this project's renders and reviews need is on this machine, checked.")
		case o.ProjectID != "":
			s = append(s, fmt.Sprintf("Not ready to work offline: %s this project needs %s only in MetaRush's storage. Download them to this machine first (metarush cache prefetch).", countNoun(t.NeededMissing, "file"), map[bool]string{true: "is", false: "are"}[t.NeededMissing == 1]))
		}
		active, interrupted, failed := 0, 0, 0
		for _, i := range o.Ingest {
			switch i.Status {
			case "active":
				active++
			case "interrupted":
				interrupted++
			default:
				failed++
			}
		}
		if len(o.Ingest) == 0 {
			s = append(s, "Imports: nothing waiting.")
		} else {
			s = append(s, fmt.Sprintf("Imports: %d uploading, %d interrupted, %d failed. Import the same files again to carry on where they stopped.", active, interrupted, failed))
		}
		s = append(s, fmt.Sprintf("Jobs: %d waiting to start, %d running.", o.Jobs.Queued, o.Jobs.Running))
		online := 0
		for _, w := range o.Workers {
			if w.Online {
				online++
			}
		}
		this := "this one isn't taking jobs"
		if o.ThisWorkerOnline {
			this = "this one is taking jobs"
		}
		s = append(s, fmt.Sprintf("Machines running MetaRush in this workspace: %d; %s.", online, this))
	}
	switch {
	case o.Daemon.Running && o.Daemon.Healthy:
		s = append(s, fmt.Sprintf("MetaRush is running in the background on this machine (port %d).", o.Daemon.Port))
	case o.Daemon.Running:
		s = append(s, fmt.Sprintf("MetaRush is running in the background on this machine but doesn't answer (port %d).", o.Daemon.Port))
	default:
		s = append(s, "MetaRush isn't running in the background on this machine (opening the dashboard starts it).")
	}
	reach := func(name string, r Reachability) string {
		if r.Reachable && r.LatencyMs < 1 {
			return name + " answers (in under 1 ms)"
		}
		if r.Reachable {
			return fmt.Sprintf("%s answers (in %.0f ms)", name, r.LatencyMs)
		}
		return fmt.Sprintf("%s can't be reached (%s)", name, r.Error)
	}
	s = append(s, reach("The database", o.Database)+"; "+reach("MetaRush's storage", o.MediaStore)+".")
	c := o.Cache
	q := "the size limit isn't kept automatically yet (MetaRush running in the background keeps it)"
	if c.Quota != nil && c.Quota.Enforced {
		q = fmt.Sprintf("MetaRush keeps them under the size limit (last checked %s ago)", time.Since(c.Quota.LastCheckAt).Round(time.Second))
	} else if c.Quota != nil {
		q = "the size limit isn't kept right now (MetaRush isn't running in the background)"
	}
	over := ""
	if c.MaxBytes > 0 && c.Bytes > c.MaxBytes {
		over = " (over the limit)"
	}
	s = append(s, fmt.Sprintf("This machine's copies: %s of %s%s, %d in use, %s part-downloaded; %s.", fmtSize(c.Bytes), fmtSize(c.MaxBytes), over, c.InUse, fmtSize(c.PartialBytes), q))
	return s
}

func init() {
	Def("sync.status", "What is on this machine and what is only in MetaRush's storage, waiting imports and jobs, which machines run MetaRush, and whether the database and the storage answer", Flags{}, func(ctx context.Context, a *app.App, in *SyncStatusIn) (*SyncStatusOut, error) {
		return SyncStatus(ctx, a, in)
	})
}
