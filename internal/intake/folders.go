package intake

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// Linked folders (D81): a folder on someone's computer that a project (or the
// workspace library) keeps in step. New and changed files become resources or
// new versions (footage becomes sources), a file that goes away leaves its
// resource in place marked "missing from folder", and the folder is watched
// by polling like the watched inbox (D13). The shared record is in MongoDB;
// where the folder is on each machine is in that machine's config.yaml
// (resources.folders), because paths are per machine.

// folderStable is how long a file must stop changing before it is added.
const folderStable = 4 * time.Second

// retryWaits are how long a file that couldn't be added waits before it is
// tried again by itself while it stays the same: a minute, five minutes, half
// an hour, then every six hours. Its size and modification time (recorded
// when it failed) say whether it stayed the same, so a large file isn't read
// or hashed again meanwhile. A file that changes is tried at once (once it
// stops changing), and so is every one when a person looks through the folder
// again.
var retryWaits = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 6 * time.Hour}

// retryWait is the wait after the tries-th failure in a row of the same file.
func retryWait(tries int) time.Duration {
	tries = min(max(tries, 1), len(retryWaits))
	return retryWaits[tries-1]
}

// shownProblems is how many files' problems a folder's status lists (it counts them all).
const shownProblems = 10

// stateDir keeps each folder's processed files on this machine.
func stateDir(a *app.App) string { return filepath.Join(a.Cfg.StateDir(), "folders") }

func statePath(a *app.App, id string) string { return filepath.Join(stateDir(a), id+".json") }

type folderState struct {
	// Done maps a file (relative path) to the size|mtime it was added at.
	Done map[string]string `json:"done"`
	// Routes remember what each file became (resource or footage).
	Routes map[string]string `json:"routes"`
	// Failed holds the files that couldn't be added (relative path), each
	// with its problem and its next try.
	Failed map[string]*failedFile `json:"failed,omitempty"`
}

// failedFile is a file a linked folder couldn't add.
type failedFile struct {
	// Key is the file's size|mtime when it was tried: while the file has the
	// same key it waits for Next; with another one it is tried at once.
	Key     string    `json:"key"`
	Problem string    `json:"problem"` // the latest, in plain words
	Tries   int       `json:"tries"`   // failures in a row of the file as it is
	At      time.Time `json:"at"`
	Next    time.Time `json:"next"`
}

func loadState(a *app.App, id string) *folderState {
	st := &folderState{Done: map[string]string{}, Routes: map[string]string{}, Failed: map[string]*failedFile{}}
	if b, err := os.ReadFile(statePath(a, id)); err == nil {
		_ = json.Unmarshal(b, st)
	}
	if st.Done == nil {
		st.Done = map[string]string{}
	}
	if st.Routes == nil {
		st.Routes = map[string]string{}
	}
	if st.Failed == nil {
		st.Failed = map[string]*failedFile{}
	}
	for rel, f := range st.Failed {
		if f == nil {
			delete(st.Failed, rel)
		}
	}
	return st
}

func saveState(a *app.App, id string, st *folderState) {
	_ = os.MkdirAll(stateDir(a), 0o755)
	b, _ := json.Marshal(st)
	tmp := statePath(a, id) + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, statePath(a, id))
	}
}

// stateTime is when this machine's state of a folder was last written (zero
// when it never was).
func stateTime(a *app.App, id string) time.Time {
	if st, err := os.Stat(statePath(a, id)); err == nil {
		return st.ModTime()
	}
	return time.Time{}
}

// localFolders reads this machine's linked folders from config.yaml (fresh).
func localFolders(a *app.App) []config.FolderPath {
	if cfg, err := config.Load(a.Cfg.Home()); err == nil {
		return cfg.Resources.Folders
	}
	return a.Cfg.Resources.Folders
}

func saveLocalFolders(a *app.App, list []config.FolderPath) error {
	if list == nil {
		list = []config.FolderPath{}
	}
	if err := config.SetField(a.Cfg.Home(), []string{"resources", "folders"}, list); err != nil {
		return apperr.Wrap(apperr.Internal, err, "MetaRush couldn't save this machine's settings.").WithDetails(map[string]any{"file": "config.yaml"})
	}
	a.Cfg.Resources.Folders = list
	if LiveFolders != nil {
		LiveFolders.Apply(list)
	}
	return nil
}

func localPath(a *app.App, id string) (config.FolderPath, bool) {
	for _, f := range localFolders(a) {
		if f.ID == id {
			return f, true
		}
	}
	return config.FolderPath{}, false
}

// scanner keeps one linked folder in step on this machine.
type scanner struct {
	a     *app.App
	rec   models.LinkedFolder
	path  string
	state *folderState
	// stateAt is when the state file was written as state has it: another
	// MetaRush on this machine (a command in the terminal) writing it since
	// means state is read again.
	stateAt time.Time
	seen    map[string]seenFile
	status  models.LinkedFolderMachine
	last    time.Time // last publish
	pubKey  string
}

type seenFile struct {
	key   string
	since time.Time
}

// ScanResult is what one pass did.
type ScanResult struct {
	Added    int      `json:"added" jsonschema:"description=New resources."`
	Versions int      `json:"versions" jsonschema:"description=Changed files that became new versions."`
	Footage  int      `json:"footage" jsonschema:"description=Footage added as sources."`
	Missing  int      `json:"missing" jsonschema:"description=Files that went away (their resources stay\\, marked missing)."`
	Waiting  int      `json:"waiting" jsonschema:"description=Large files kept on this machine until a cut uses them."`
	Pending  []string `json:"pending" jsonschema:"description=Files still being copied in."`
	Errors   []string `json:"errors" jsonschema:"description=Files this look couldn't add\\, each with why (they are tried again)."`
}

func newScanner(a *app.App, rec models.LinkedFolder, path string) *scanner {
	at := stateTime(a, rec.ID)
	return &scanner{a: a, rec: rec, path: path, state: loadState(a, rec.ID), stateAt: at, seen: map[string]seenFile{}}
}

func fileKey(st fs.FileInfo) string { return fmt.Sprintf("%d|%d", st.Size(), st.ModTime().UnixNano()) }

// uploadNow decides whether a resource file of this size goes to the media
// store at once (the folder's upload policy).
func (s *scanner) uploadNow(size int64) bool {
	switch s.rec.Upload {
	case models.UploadAll:
		return true
	case models.UploadOnUse:
		return false
	}
	mb := s.rec.SmallMB
	if mb <= 0 {
		mb = s.a.Cfg.Resources.SmallMB()
	}
	return size <= int64(mb)<<20
}

// scan makes one pass. immediate is a person asking to look now (a folder
// linked from the terminal, Look again): files that are not changing right now
// are taken without waiting, and files that couldn't be added are tried again
// now instead of at their next try.
func (s *scanner) scan(ctx context.Context, immediate bool) ScanResult {
	res := ScanResult{Pending: []string{}, Errors: []string{}}
	now := time.Now()
	st, err := os.Stat(s.path)
	s.status = models.LinkedFolderMachine{Machine: s.a.Cfg.Installation.Name, Display: ingest.DisplayPath(s.path), State: "watching", LastScan: now.UTC(), UpdatedAt: now.UTC()}
	if err != nil || !st.IsDir() {
		s.status.State = "missing"
		s.publish(ctx)
		return res
	}
	// A command in the terminal may have looked through the folder since:
	// carry on from what it recorded.
	if at := stateTime(s.a, s.rec.ID); !at.Equal(s.stateAt) {
		s.state, s.stateAt = loadState(s.a, s.rec.ID), at
	}
	present := map[string]bool{}
	files := 0
	_ = filepath.WalkDir(s.path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != s.path && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || name == "Thumbs.db" || name == "desktop.ini" || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(s.path, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		present[rel] = true
		files++
		info, err := d.Info()
		if err != nil {
			return nil
		}
		key := fileKey(info)
		if s.state.Done[rel] == key {
			delete(s.state.Failed, rel) // back as it was when it was added
			return nil
		}
		if f := s.state.Failed[rel]; f != nil && f.Key == key {
			// Unchanged since it couldn't be added: it waits for its next try
			// (nothing is read, so a large file isn't hashed again and again).
			if !immediate && now.Before(f.Next) {
				return nil
			}
			s.try(ctx, p, rel, key, info, &res)
			return nil
		}
		seen, ok := s.seen[rel]
		stable := ok && seen.key == key && now.Sub(seen.since) >= folderStable && now.Sub(info.ModTime()) >= folderStable
		if immediate && now.Sub(info.ModTime()) >= folderStable {
			stable = true
		}
		if !ok || seen.key != key {
			s.seen[rel] = seenFile{key: key, since: now}
		}
		if !stable {
			res.Pending = append(res.Pending, rel)
			return nil
		}
		s.try(ctx, p, rel, key, info, &res)
		return nil
	})
	// Files that went away: their resources stay, marked missing, and a
	// problem adding one goes with it.
	for rel := range s.state.Failed {
		if !present[rel] {
			delete(s.state.Failed, rel)
		}
	}
	for rel, route := range s.state.Routes {
		if present[rel] {
			continue
		}
		if route == resources.RouteResource {
			n, _ := s.a.DB.C(store.CResources).UpdateMany(ctx, bson.M{"workspaceId": s.a.WorkspaceID(), "folder.folderId": s.rec.ID, "folder.relPath": rel, "missing": bson.M{"$ne": true}},
				bson.M{"$set": bson.M{"missing": true, "missingSince": now.UTC(), "updatedAt": now.UTC()}})
			if n != nil && n.ModifiedCount > 0 {
				res.Missing++
			}
		}
		delete(s.state.Routes, rel)
		delete(s.state.Done, rel)
	}
	for rel := range s.seen {
		if !present[rel] {
			delete(s.seen, rel)
		}
	}
	saveState(s.a, s.rec.ID, s.state)
	s.stateAt = stateTime(s.a, s.rec.ID)
	s.status.Files = files
	s.status.Pending = res.Pending
	s.status.Errors, s.status.Failed, s.status.RetryAt = s.problems()
	s.publish(ctx)
	return res
}

// try adds one file and records how it went: added, or its problem and when
// it is tried again.
func (s *scanner) try(ctx context.Context, full, rel, key string, info fs.FileInfo, res *ScanResult) {
	problem := s.add(ctx, full, rel, info, res)
	switch {
	case ctx.Err() != nil:
		// MetaRush is stopping: the file is tried when it runs again.
	case problem == "":
		s.state.Done[rel] = key
		delete(s.state.Failed, rel)
	default:
		problem = strings.ReplaceAll(problem, full, rel) // never the whole path on this computer
		res.Errors = append(res.Errors, rel+": "+problem)
		tries := 1
		if f := s.state.Failed[rel]; f != nil && f.Key == key {
			tries = f.Tries + 1
		}
		at := time.Now().UTC()
		s.state.Failed[rel] = &failedFile{Key: key, Problem: problem, Tries: tries, At: at, Next: at.Add(retryWait(tries))}
	}
}

// problems is what the folder's status says about the files that couldn't be
// added: each one's latest problem ("logo.png: …", the first few by name), how
// many there are, and when the next of them is tried again by itself.
func (s *scanner) problems() ([]string, int, *time.Time) {
	if len(s.state.Failed) == 0 {
		return nil, 0, nil
	}
	rels := make([]string, 0, len(s.state.Failed))
	for rel := range s.state.Failed {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	var shown []string
	var next time.Time
	for _, rel := range rels {
		f := s.state.Failed[rel]
		if len(shown) < shownProblems {
			shown = append(shown, rel+": "+f.Problem)
		}
		if next.IsZero() || f.Next.Before(next) {
			next = f.Next
		}
	}
	return shown, len(rels), &next
}

// add brings one file in: a resource, footage, or nothing (not something
// MetaRush uses). It returns what went wrong, in plain words ("" when nothing did).
func (s *scanner) add(ctx context.Context, full, rel string, info fs.FileInfo, res *ScanResult) string {
	sn, err := resources.SniffFile(full)
	if err != nil {
		return ingest.FailText(err)
	}
	switch sn.Route {
	case resources.RouteResource:
		o := resources.AddOptions{Scope: s.rec.Scope, ProjectID: s.rec.ProjectID, Folder: &models.ResourceFolderRef{FolderID: s.rec.ID, RelPath: rel},
			Source: models.ResourceSource{Kind: models.FromFolder, FolderID: s.rec.ID, RelPath: rel, Path: ingest.DisplayPath(full)},
			Defer:  !s.uploadNow(info.Size()), Via: "folder"}
		r, err := resources.AddFile(ctx, s.a, full, o)
		if err != nil {
			return ingest.FailText(err)
		}
		s.state.Routes[rel] = resources.RouteResource
		switch r.Status {
		case resources.StatusAdded:
			res.Added++
		case resources.StatusVersion:
			res.Versions++
		}
		if o.Defer && r.Status != resources.StatusSame {
			res.Waiting++
		}
	case resources.RouteFootage:
		r := ingest.File(ctx, s.a, full, ingest.Options{ProjectID: s.rec.ProjectID, Via: "folder", Transcribe: true, Duplicates: ingest.DuplicatesKeep})
		switch r.Status {
		case "ingested", "resumed":
			res.Footage++
			s.state.Routes[rel] = resources.RouteFootage
		case "deduped", "duplicate", "skipped":
			s.state.Routes[rel] = resources.RouteFootage
		default:
			if strings.TrimSpace(r.Error) == "" {
				return "MetaRush couldn't add it."
			}
			return r.Error
		}
	default:
		s.state.Routes[rel] = resources.RouteUnknown // not something MetaRush uses: left alone
	}
	return ""
}

// publish writes this machine's state of the folder into the shared record
// (on change, else at most every 30 s).
func (s *scanner) publish(ctx context.Context) {
	ws := s.a.WorkspaceID()
	nres, _ := s.a.DB.C(store.CResources).CountDocuments(ctx, bson.M{"workspaceId": ws, "folder.folderId": s.rec.ID, "removed": bson.M{"$ne": true}})
	s.status.Resources = int(nres)
	var waiting []models.Resource
	_ = s.a.DB.Find(ctx, store.CResources, ws, bson.M{"folder.folderId": s.rec.ID, "versions.stored": false}, &waiting)
	s.status.Waiting = 0
	for _, r := range waiting {
		if v := r.Latest(); v != nil && !v.Stored {
			s.status.Waiting++
		}
	}
	footage := 0
	for _, r := range s.state.Routes {
		if r == resources.RouteFootage {
			footage++
		}
	}
	s.status.Footage = footage
	key := fmt.Sprintf("%s|%d|%d|%d|%d|%v|%v|%d|%v", s.status.State, s.status.Files, s.status.Resources, s.status.Footage, s.status.Waiting, s.status.Pending, s.status.Errors,
		s.status.Failed, s.status.RetryAt)
	if key == s.pubKey && time.Since(s.last) < 30*time.Second {
		return
	}
	s.pubKey, s.last = key, time.Now()
	_ = s.a.DB.Update(context.WithoutCancel(ctx), store.CLinkedFolders, ws, s.rec.ID, bson.M{"$set": bson.M{"machines." + s.a.Cfg.Installation.ID: s.status}})
}

// FolderManager keeps this machine's linked folders in step (the daemon runs it).
type FolderManager struct {
	a        *app.App
	log      func(string, ...any)
	mu       sync.Mutex
	list     []config.FolderPath
	scanners map[string]*scanner
	kick     chan struct{}
	// asked are the folders a person asked to look through now (Rescan).
	asked map[string]bool
	// OnChange is told when something was added (the dashboard refetches).
	OnChange func()
}

// LiveFolders is the running daemon's folder manager (nil in the CLI).
var LiveFolders *FolderManager

// NewFolderManager starts the manager; it scans every few seconds until ctx ends.
func NewFolderManager(ctx context.Context, a *app.App, log func(string, ...any)) *FolderManager {
	if log == nil {
		log = func(string, ...any) {}
	}
	m := &FolderManager{a: a, log: log, scanners: map[string]*scanner{}, kick: make(chan struct{}, 1)}
	go m.run(ctx)
	return m
}

// Apply follows this machine's list of linked folders (hot reload).
func (m *FolderManager) Apply(list []config.FolderPath) {
	m.mu.Lock()
	m.list = append([]config.FolderPath{}, list...)
	m.mu.Unlock()
	m.wake()
}

// Rescan looks through a folder on the next pass as a person asked to (Look
// again): files that stopped changing are taken at once, and files that
// couldn't be added are tried again now rather than at their next try.
func (m *FolderManager) Rescan(id string) {
	m.mu.Lock()
	if m.asked == nil {
		m.asked = map[string]bool{}
	}
	m.asked[id] = true
	m.mu.Unlock()
	m.wake()
}

func (m *FolderManager) wake() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// Snapshot is a folder's latest state on this machine.
func (m *FolderManager) Snapshot(id string) (models.LinkedFolderMachine, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.scanners[id]
	if !ok {
		return models.LinkedFolderMachine{}, false
	}
	return s.status, true
}

func (m *FolderManager) run(ctx context.Context) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		m.pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.kick:
		}
	}
}

func (m *FolderManager) pass(ctx context.Context) {
	m.mu.Lock()
	list := append([]config.FolderPath{}, m.list...)
	m.mu.Unlock()
	active := map[string]bool{}
	for _, fp := range list {
		if fp.Off || fp.ID == "" || fp.Path == "" {
			continue
		}
		var rec models.LinkedFolder
		if m.a.DB.GetByID(ctx, store.CLinkedFolders, m.a.WorkspaceID(), fp.ID, &rec) != nil || rec.Unlinked {
			continue
		}
		active[fp.ID] = true
		m.mu.Lock()
		s := m.scanners[fp.ID]
		if s == nil || s.path != fp.Path {
			s = newScanner(m.a, rec, fp.Path)
			m.scanners[fp.ID] = s
		}
		s.rec = rec
		asked := m.asked[fp.ID]
		delete(m.asked, fp.ID)
		m.mu.Unlock()
		res := s.scan(ctx, asked)
		for _, e := range res.Errors {
			m.log("linked folder %s: couldn't add %s", rec.Name, e)
		}
		if res.Added+res.Versions+res.Footage+res.Missing > 0 {
			m.log("linked folder %s: %d added, %d new versions, %d footage, %d missing", rec.Name, res.Added, res.Versions, res.Footage, res.Missing)
			if m.OnChange != nil {
				m.OnChange()
			}
		}
	}
	m.mu.Lock()
	for id := range m.scanners {
		if !active[id] {
			delete(m.scanners, id)
		}
	}
	m.mu.Unlock()
}

// ---- ops ----

// LinkedFolderLinkIn links a folder.
type LinkedFolderLinkIn struct {
	Path      string `json:"path" jsonschema:"minLength=1,description=The folder on this machine (absolute or ~/…)."`
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=The project it belongs to (ID or name); empty: the workspace library."`
	Upload    string `json:"upload,omitempty" jsonschema:"enum=small,enum=all,enum=on-use,description=When files go to the media store: small (default: up to the size limit at once\\, larger ones when a cut first uses them)\\, all\\, or on-use."`
	SmallMB   int    `json:"smallMb,omitempty" jsonschema:"minimum=1,maximum=4096"`
	Existing  string `json:"existing,omitempty" jsonschema:"enum=import,enum=skip,description=Files already in it: add them (default) or only take new ones."`
}

// LinkedFolderRow is a linked folder as people see it.
type LinkedFolderRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Scope       string `json:"scope" jsonschema:"enum=project,enum=workspace"`
	ProjectID   string `json:"projectId,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
	Upload      string `json:"upload" jsonschema:"enum=small,enum=all,enum=on-use"`
	SmallMB     int    `json:"smallMb"`
	// Here is this machine's state; HerePath where the folder is here.
	Here     *models.LinkedFolderMachine  `json:"here,omitempty"`
	HerePath string                       `json:"herePath,omitempty"`
	Paused   bool                         `json:"paused,omitempty"`
	Machines []models.LinkedFolderMachine `json:"machines"`
	Unlinked bool                         `json:"unlinked,omitempty"`
	// Text says how it stands, in plain words.
	Text string `json:"text"`
}

// LinkedFolderOut is a folder after a change.
type LinkedFolderOut struct {
	Folder  LinkedFolderRow `json:"folder"`
	Scan    *ScanResult     `json:"scan,omitempty"`
	Message string          `json:"message"`
}

// LinkedFolderListIn filters linked folders.
type LinkedFolderListIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=Only this project's (ID or name); empty: all."`
}

// LinkedFolderListOut lists linked folders.
type LinkedFolderListOut struct {
	Folders []LinkedFolderRow `json:"folders"`
}

// LinkedFolderUnlinkIn stops keeping a folder in step.
type LinkedFolderUnlinkIn struct {
	Folder string `json:"folder" jsonschema:"minLength=1,description=The folder's name or ID."`
	// Here only stops on this machine (others keep it in step).
	Here bool `json:"here,omitempty"`
}

// LinkedFolderSetIn changes a linked folder.
type LinkedFolderSetIn struct {
	Folder  string `json:"folder" jsonschema:"minLength=1"`
	Upload  string `json:"upload,omitempty" jsonschema:"enum=small,enum=all,enum=on-use"`
	SmallMB int    `json:"smallMb,omitempty" jsonschema:"minimum=1,maximum=4096"`
	Pause   bool   `json:"pause,omitempty" jsonschema:"description=Stop keeping it in step on this machine (the link stays)."`
	Resume  bool   `json:"resume,omitempty"`
	// Path links the folder on this machine (where it is here).
	Path   string `json:"path,omitempty"`
	Rescan bool   `json:"rescan,omitempty" jsonschema:"description=Look through it again now."`
}

func folderRow(ctx context.Context, a *app.App, f models.LinkedFolder) LinkedFolderRow {
	row := LinkedFolderRow{ID: f.ID, Name: f.Name, Scope: f.Scope, ProjectID: f.ProjectID, Upload: f.Upload, SmallMB: f.SmallMB, Unlinked: f.Unlinked, Machines: []models.LinkedFolderMachine{}}
	if row.SmallMB <= 0 {
		row.SmallMB = a.Cfg.Resources.SmallMB()
	}
	if f.ProjectID != "" {
		var p models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), f.ProjectID, &p) == nil {
			row.ProjectName = p.Name
		}
	}
	ids := make([]string, 0, len(f.Machines))
	for id := range f.Machines {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		row.Machines = append(row.Machines, f.Machines[id])
	}
	if fp, ok := localPath(a, f.ID); ok {
		row.HerePath, row.Paused = ingest.DisplayPath(fp.Path), fp.Off
		if LiveFolders != nil {
			if s, ok := LiveFolders.Snapshot(f.ID); ok {
				row.Here = &s
			}
		}
		if row.Here == nil {
			if m, ok := f.Machines[a.Cfg.Installation.ID]; ok {
				row.Here = &m
			}
		}
	}
	switch {
	case f.Unlinked:
		row.Text = "No longer kept in step; its resources stay."
	case row.HerePath == "":
		row.Text = "Linked on another machine. Choose where it is on this one to keep it in step here too."
	case row.Paused:
		row.Text = "Paused on this machine."
	case row.Here != nil && row.Here.State == "missing":
		row.Text = "The folder can't be found. If it is on an external drive, plug it in and keeping in step carries on."
	case row.Here != nil:
		row.Text = fmt.Sprintf("Kept in step: %s from %s", plural(row.Here.Resources, "resource"), plural(row.Here.Files, "file"))
		if row.Here.Footage > 0 {
			row.Text += fmt.Sprintf(", %s", plural(row.Here.Footage, "clip"))
		}
		switch w := row.Here.Waiting; {
		case w == 1:
			row.Text += "; 1 large file uploads when a cut first uses it"
		case w > 1:
			row.Text += fmt.Sprintf("; %d large files upload when a cut first uses them", w)
		}
		row.Text += "."
		if t := failedText(row.Here, time.Now()); t != "" {
			row.Text += " " + t
		}
	case LiveFolders == nil:
		row.Text = "Linked here. MetaRush keeps it in step while it runs on this machine."
	default:
		row.Text = "Linked here; looking through it…"
	}
	return row
}

// failedText says how many files couldn't be added and when MetaRush tries
// again by itself: "1 file couldn't be added; MetaRush tries it again in about
// 5 minutes." (Which files, and why, are the status's errors.)
func failedText(m *models.LinkedFolderMachine, now time.Time) string {
	if m == nil || m.Failed == 0 {
		return ""
	}
	when := "soon"
	if m.RetryAt != nil && m.RetryAt.After(now) {
		when = jobs.PlainIn(m.RetryAt.Sub(now))
	}
	if m.Failed == 1 {
		return "1 file couldn't be added; MetaRush tries it again " + when + "."
	}
	return fmt.Sprintf("%d files couldn't be added; MetaRush tries again %s.", m.Failed, when)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// uploadChoices says when a linked folder's files may upload (the upload setting).
const uploadChoices = "Choose when its files upload: small (small files now, large ones when a cut first uses them), all, or on-use."

// findFolder resolves a linked folder by ID or name.
func findFolder(ctx context.Context, a *app.App, ref string) (*models.LinkedFolder, error) {
	ref = strings.TrimSpace(ref)
	var f models.LinkedFolder
	if ids.Valid(ids.LinkedFolder, ref) {
		if err := a.DB.GetByID(ctx, store.CLinkedFolders, a.WorkspaceID(), ref, &f); err != nil {
			return nil, apperr.New(apperr.NotFound, "That linked folder was not found. It may have been unlinked.").
				WithHint("A project's Resources tab lists its linked folders.").WithDetails(map[string]any{"folder": ref}).WithCommand("metarush resources folders")
		}
		return &f, nil
	}
	var all []models.LinkedFolder
	if err := a.DB.Find(ctx, store.CLinkedFolders, a.WorkspaceID(), bson.M{}, &all); err != nil {
		return nil, err
	}
	var hits []models.LinkedFolder
	for _, x := range all {
		if strings.EqualFold(x.Name, ref) {
			hits = append(hits, x)
		}
	}
	switch len(hits) {
	case 1:
		return &hits[0], nil
	case 0:
		return nil, apperr.New(apperr.NotFound, "There's no linked folder called %q.", ref).
			WithHint("A project's Resources tab lists its linked folders.").WithCommand("metarush resources folders")
	}
	return nil, apperr.New(apperr.InvalidInput, "%d linked folders are called %q.", len(hits), ref).
		WithHint("Choose the one you mean in its project's Resources tab.").WithCommand("metarush resources folders --json")
}

// LinkFolder links a folder on this machine and, without a daemon here,
// looks through it once.
func LinkFolder(ctx context.Context, a *app.App, in *LinkedFolderLinkIn) (*LinkedFolderOut, error) {
	dir, err := ingest.ValidateWatchDir(a, in.Path, false, false)
	if err != nil {
		return nil, err
	}
	scope, projectID := models.ScopeWorkspace, ""
	if strings.TrimSpace(in.ProjectID) != "" {
		p, err := ops.ResolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, err
		}
		scope, projectID = models.ScopeProject, p.ID
	}
	upload := in.Upload
	switch upload {
	case "":
		upload = models.UploadSmall
	case models.UploadSmall, models.UploadAll, models.UploadOnUse:
	default:
		return nil, apperr.New(apperr.InvalidInput, "%s", uploadChoices)
	}
	// The same folder linked again here is the same link.
	for _, fp := range localFolders(a) {
		if filepath.Clean(fp.Path) == dir {
			var f models.LinkedFolder
			if a.DB.GetByID(ctx, store.CLinkedFolders, a.WorkspaceID(), fp.ID, &f) == nil && !f.Unlinked {
				return &LinkedFolderOut{Folder: folderRow(ctx, a, f), Message: f.Name + " is already linked."}, nil
			}
		}
	}
	now := time.Now().UTC()
	rec := models.LinkedFolder{ID: ids.New(ids.LinkedFolder), WorkspaceID: a.WorkspaceID(), Name: filepath.Base(dir), Scope: scope, ProjectID: projectID,
		Upload: upload, SmallMB: in.SmallMB, CreatedAt: now, CreatedBy: a.Actor, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CLinkedFolders, rec); err != nil {
		return nil, err
	}
	if in.Existing == "skip" {
		st := loadState(a, rec.ID)
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if info, err := d.Info(); err == nil {
				if rel, err := filepath.Rel(dir, p); err == nil {
					st.Done[filepath.ToSlash(rel)] = fileKey(info)
				}
			}
			return nil
		})
		saveState(a, rec.ID, st)
	}
	list := append(localFolders(a), config.FolderPath{ID: rec.ID, Path: dir})
	if err := saveLocalFolders(a, list); err != nil {
		return nil, err
	}
	a.Audit(ctx, "resources.link", "folder", rec.ID, projectID, map[string]any{"name": rec.Name, "upload": upload})
	out := &LinkedFolderOut{}
	if LiveFolders == nil {
		// No daemon in this process: look through it now so the files show up.
		res := newScanner(a, rec, dir).scan(ctx, true)
		out.Scan = &res
	}
	_ = a.DB.GetByID(ctx, store.CLinkedFolders, a.WorkspaceID(), rec.ID, &rec)
	out.Folder = folderRow(ctx, a, rec)
	where := "the workspace library"
	if projectID != "" {
		where = "this project"
	}
	out.Message = fmt.Sprintf("Linked %s to %s: new and changed files come in by themselves.", rec.Name, where)
	if out.Scan != nil {
		out.Message += fmt.Sprintf(" Added %s now", plural(out.Scan.Added, "resource"))
		if out.Scan.Footage > 0 {
			out.Message += fmt.Sprintf(" and %s", plural(out.Scan.Footage, "clip"))
		}
		out.Message += "."
	}
	return out, nil
}

func init() {
	ops.Def("resources.link", "Link a folder on this machine: its pictures, SVGs, PDFs and fonts become resources (and its footage sources), kept in step as files change", ops.Flags{Mutating: true, LocalOnly: true, OwnerOnly: true}, LinkFolder)
	ops.Def("resources.folders", "List linked folders: where each is on this machine, and how it stands on every machine", ops.Flags{}, func(ctx context.Context, a *app.App, in *LinkedFolderListIn) (*LinkedFolderListOut, error) {
		filter := bson.M{}
		if strings.TrimSpace(in.ProjectID) != "" {
			p, err := ops.ResolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			filter["projectId"] = p.ID
		}
		var fs []models.LinkedFolder
		if err := a.DB.Find(ctx, store.CLinkedFolders, a.WorkspaceID(), filter, &fs); err != nil {
			return nil, err
		}
		sort.Slice(fs, func(i, j int) bool { return fs[i].CreatedAt.Before(fs[j].CreatedAt) })
		out := &LinkedFolderListOut{Folders: []LinkedFolderRow{}}
		for _, f := range fs {
			out.Folders = append(out.Folders, folderRow(ctx, a, f))
		}
		return out, nil
	})
	ops.Def("resources.unlink", "Stop keeping a linked folder in step (on every machine, or only this one); its resources stay", ops.Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *LinkedFolderUnlinkIn) (*LinkedFolderOut, error) {
		f, err := findFolder(ctx, a, in.Folder)
		if err != nil {
			return nil, err
		}
		var keep []config.FolderPath
		for _, fp := range localFolders(a) {
			if fp.ID != f.ID {
				keep = append(keep, fp)
			}
		}
		if err := saveLocalFolders(a, keep); err != nil {
			return nil, err
		}
		msg := fmt.Sprintf("%s is no longer kept in step on this machine.", f.Name)
		if !in.Here {
			if err := a.DB.Update(ctx, store.CLinkedFolders, a.WorkspaceID(), f.ID, bson.M{"$set": bson.M{"unlinked": true, "updatedAt": time.Now().UTC()}}); err != nil {
				return nil, err
			}
			f.Unlinked = true
			msg = fmt.Sprintf("%s is no longer kept in step. Its resources stay where they are.", f.Name)
		}
		a.Audit(ctx, "resources.unlink", "folder", f.ID, f.ProjectID, map[string]any{"here": in.Here})
		return &LinkedFolderOut{Folder: folderRow(ctx, a, *f), Message: msg}, nil
	})
	ops.Def("resources.folder", "Change a linked folder: when files upload, pause or resume it on this machine, link it here at a path, or look through it again", ops.Flags{Mutating: true, LocalOnly: true, OwnerOnly: true}, func(ctx context.Context, a *app.App, in *LinkedFolderSetIn) (*LinkedFolderOut, error) {
		f, err := findFolder(ctx, a, in.Folder)
		if err != nil {
			return nil, err
		}
		set := bson.M{"updatedAt": time.Now().UTC()}
		if in.Upload != "" {
			switch in.Upload {
			case models.UploadSmall, models.UploadAll, models.UploadOnUse:
				set["upload"], f.Upload = in.Upload, in.Upload
			default:
				return nil, apperr.New(apperr.InvalidInput, "%s", uploadChoices)
			}
		}
		if in.SmallMB > 0 {
			set["smallMb"], f.SmallMB = in.SmallMB, in.SmallMB
		}
		if f.Unlinked && in.Path != "" {
			set["unlinked"], f.Unlinked = false, false
		}
		if err := a.DB.Update(ctx, store.CLinkedFolders, a.WorkspaceID(), f.ID, bson.M{"$set": set}); err != nil {
			return nil, err
		}
		list := localFolders(a)
		changed := false
		if in.Path != "" {
			dir, err := ingest.ValidateWatchDir(a, in.Path, false, false)
			if err != nil {
				return nil, err
			}
			found := false
			for i := range list {
				if list[i].ID == f.ID {
					list[i].Path, found = dir, true
				}
			}
			if !found {
				list = append(list, config.FolderPath{ID: f.ID, Path: dir})
			}
			changed = true
		}
		if in.Pause || in.Resume {
			for i := range list {
				if list[i].ID == f.ID {
					list[i].Off = in.Pause && !in.Resume
					changed = true
				}
			}
		}
		if changed {
			if err := saveLocalFolders(a, list); err != nil {
				return nil, err
			}
		}
		out := &LinkedFolderOut{Message: "Saved " + f.Name + "."}
		if in.Rescan || in.Path != "" {
			if fp, ok := localPath(a, f.ID); ok && !fp.Off {
				if LiveFolders != nil {
					LiveFolders.Apply(localFolders(a))
					LiveFolders.Rescan(f.ID)
				} else {
					res := newScanner(a, *f, fp.Path).scan(ctx, true)
					out.Scan = &res
				}
			}
		}
		a.Audit(ctx, "resources.folder", "folder", f.ID, f.ProjectID, map[string]any{"upload": f.Upload, "pause": in.Pause, "resume": in.Resume, "rescan": in.Rescan})
		_ = a.DB.GetByID(ctx, store.CLinkedFolders, a.WorkspaceID(), f.ID, f)
		out.Folder = folderRow(ctx, a, *f)
		return out, nil
	})
}
