package wsarchive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/cache"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/secretpath"
	"github.com/metavert-io/metarush/internal/storagemove"
	"github.com/metavert-io/metarush/internal/store"
)

// ExportOptions say what to export and where.
type ExportOptions struct {
	// DB is the database read (never written: see source.go).
	DB        *store.DB
	Workspace string
	// To is the archive: a folder, or with Tar one file.
	To  string
	Tar bool
	// Media is MediaReferenced (the default) or MediaIncluded.
	Media string
	// Store is the workspace's storage (listed in both modes, read when the
	// media is included); StoreWords names it for people.
	Store      media.Store
	StoreWords string
	// Cache gives objects this computer already has (included media).
	Cache *cache.Cache
	// StateDir keeps an included copy's journal (so a stopped export
	// continues); ScratchDir its partial downloads.
	StateDir, ScratchDir string
	Scrub                *Scrubber
	// Here is the computer (installation) the export runs on when it is that
	// computer's own MetaRush making it (the dashboard): not named among the
	// computers working while the archive is made.
	Here     string
	MadeBy   ArchiveMadeBy
	Progress func(ArchiveProgress)
	Now      func() time.Time
}

// ArchiveProgress is how far an export or a restore is.
type ArchiveProgress struct {
	Phase      string `json:"phase" jsonschema:"enum=checking,enum=documents,enum=media,enum=packing,enum=unpacking,enum=migrating,enum=verifying"`
	Collection string `json:"collection,omitempty"`
	// Collections done of all; Docs written so far.
	Collections     int   `json:"collections"`
	CollectionsDone int   `json:"collectionsDone"`
	Docs            int64 `json:"docs"`
	// Files and Bytes of media (or of the archive while packing).
	Files     int   `json:"files,omitempty"`
	FilesDone int   `json:"filesDone,omitempty"`
	Bytes     int64 `json:"bytes,omitempty"`
	BytesDone int64 `json:"bytesDone,omitempty"`
}

// Result is a finished export.
type Result struct {
	Path     string    `json:"path"`
	Tar      bool      `json:"tar"`
	Bytes    int64     `json:"bytes"`
	Manifest *Manifest `json:"-"`
}

func (o *ExportOptions) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

func (o *ExportOptions) progress(p ArchiveProgress) {
	if o.Progress != nil {
		o.Progress(p)
	}
}

// checkWhole refuses a database a restore still holds: only part of a
// workspace is in it.
func checkWhole(ctx context.Context, db *store.DB) error {
	l, err := db.ReadRestoreLock(ctx)
	if err != nil {
		return apperr.Wrap(apperr.Database, err, "MetaRush couldn't read the database.")
	}
	if l.Unfinished() {
		return apperr.New(apperr.Conflict, "This database holds a restore of the workspace “%s” that didn't finish, so there is no whole workspace in it to export.", l.Name).
			WithHint("Run the same restore again to finish it first.").WithDetails(map[string]any{"reason": "restore_unfinished", "state": l.State})
	}
	return nil
}

// CheckDestination refuses a place an archive must never be written: a
// folder a cloud service syncs or a code repository (an archive holds
// people, consent records and transcripts), or a path that is already there.
func CheckDestination(path string) error {
	if strings.TrimSpace(path) == "" {
		return apperr.New(apperr.InvalidInput, "Say where to write the archive.")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "That place for the archive can't be used: %v.", err)
	}
	if _, err := os.Lstat(abs); err == nil {
		return apperr.New(apperr.Conflict, "Something is already there, and an export never writes over anything.").
			WithHint("Choose a new name for the archive.").WithDetails(map[string]any{"path": abs})
	}
	parent := filepath.Dir(abs)
	if st, err := os.Stat(parent); err != nil || !st.IsDir() {
		return apperr.New(apperr.InvalidInput, "The folder to write the archive in isn't there.").WithDetails(map[string]any{"folder": parent})
	}
	v := secretpath.Inspect(parent)
	details := map[string]any{"path": abs, "kind": v.Kind, "root": v.Root, "service": v.Service}
	switch v.Kind {
	case secretpath.KindGit:
		return apperr.New(apperr.InvalidInput, "That folder is inside a code repository, where the archive could be committed and shared.").
			WithHint("The archive holds everything about the work, people and their consent included: write it outside the repository.").WithDetails(details)
	case secretpath.KindSync:
		return apperr.New(apperr.InvalidInput, "That folder is in %s, which uploads everything saved there.", v.Service).
			WithHint("The archive holds everything about the work, people and their consent included: write it to a folder that isn't synced (or an external drive) and copy it yourself.").WithDetails(details)
	}
	if dir := secretpath.ProjectFolder(parent); dir != "" {
		details["kind"], details["root"] = "project", dir
		return apperr.New(apperr.InvalidInput, "That folder is inside a MetaRush project folder, which is shared with the project.").
			WithHint("Write the archive somewhere else.").WithDetails(details)
	}
	return nil
}

// TarPath is the archive's file name with Tar (".tar" added when it has none).
func TarPath(p string) string {
	if strings.HasSuffix(strings.ToLower(p), ".tar") {
		return p
	}
	return p + ".tar"
}

// Export writes the archive. It reads the database and the storage only; the
// archive appears at To only once it is complete (it is made beside it, in a
// folder ending in .partial).
func Export(ctx context.Context, o ExportOptions) (*Result, error) {
	if o.Media == "" {
		o.Media = MediaReferenced
	}
	if o.Media != MediaReferenced && o.Media != MediaIncluded {
		return nil, apperr.New(apperr.InvalidInput, "The media is either referenced or included.")
	}
	if o.Store == nil {
		return nil, apperr.New(apperr.NotConfigured, "Say where the workspace keeps its footage, so the archive can list it.")
	}
	dest := o.To
	if o.Tar {
		dest = TarPath(dest)
	}
	if err := CheckDestination(dest); err != nil {
		return nil, err
	}
	dest, _ = filepath.Abs(dest)
	at := o.now()
	o.progress(ArchiveProgress{Phase: "checking"})
	level, err := ReadLevel(ctx, o.DB)
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read the database's migration level.")
	}
	if err := CheckReadable("This database", level); err != nil {
		return nil, err
	}
	if err := checkWhole(ctx, o.DB); err != nil {
		return nil, err
	}
	ws, err := FindWorkspace(ctx, o.DB, o.Workspace)
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read the workspace.")
	}
	if ws == nil {
		return nil, apperr.New(apperr.NotFound, "That workspace isn't in this database.").WithDetails(map[string]any{"workspace": o.Workspace})
	}
	colls, err := ListCollections(ctx, o.DB)
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't list the database's collections.")
	}
	work := dest + ".partial"
	marker := partialMarker{Workspace: ws.ID, Media: o.Media, Store: o.Store.Locator()}
	if _, err := os.Lstat(work); err == nil {
		// What a stopped export of this archive left: with the same options
		// its documents are written again and an included copy of the media
		// continues from its journal; with other options (or none recorded)
		// the folder, this export's own, starts afresh.
		if !sameMarker(work, marker) {
			if err := os.RemoveAll(work); err != nil {
				return nil, err
			}
		} else if err := os.RemoveAll(filepath.Join(work, CollectionsDir)); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Join(work, CollectionsDir), 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(work, 0o700); err != nil {
		return nil, err
	}
	if err := writeMarker(work, marker); err != nil {
		return nil, err
	}
	m := &Manifest{Format: Format, MadeAt: at, MadeBy: o.MadeBy, Workspace: ArchiveWorkspace{ID: ws.ID, Name: ws.Name},
		Database: DatabaseRef{Name: o.DB.Name}, Level: level, Collections: []CollectionFile{}, LeftOut: []LeftOutCollection{}, Files: []File{}}
	if v, _, err := o.DB.ServerInfo(ctx); err == nil {
		m.Database.Server = v
	}
	if run := runningNow(ctx, o.DB, ws.ID, o.Here); len(run) > 0 {
		m.Notes = append(m.Notes, fmt.Sprintf("MetaRush was running on %s while the archive was made: what changed meanwhile may be only partly in it.", strings.Join(run, ", ")))
	}
	if level.Older {
		m.Notes = append(m.Notes, fmt.Sprintf("The database was at an older migration level (%d) than this MetaRush (%d); restoring brings the copy up to date and leaves the database itself as it was.", level.Highest, level.ThisMetaRush))
	}
	var exported []CollectionInfo
	for _, c := range colls {
		e, known := Catalogue[c.Name]
		switch {
		case !known:
			n, _ := o.DB.C(c.Name).EstimatedDocumentCount(ctx)
			m.LeftOut = append(m.LeftOut, LeftOutCollection{Name: c.Name, Why: "MetaRush doesn't know this collection, so nothing of it went in", Count: n, Unknown: true})
		case e.Exported():
			exported = append(exported, c)
		case e.How == LeftOut:
			n, _ := o.DB.C(c.Name).CountDocuments(ctx, workspaceFilter(ws.ID))
			m.LeftOut = append(m.LeftOut, LeftOutCollection{Name: c.Name, Why: e.Why, Count: n})
		}
	}
	var noWorkspace, others int64
	for i, c := range exported {
		f, err := exportCollection(ctx, &o, work, c, ws.ID, at, i, len(exported))
		if err != nil {
			return nil, err
		}
		m.Collections = append(m.Collections, *f)
		m.Files = append(m.Files, File{Path: f.File, Bytes: f.Bytes, SHA256: f.SHA256})
		if c.Name != store.CWorkspaces {
			if n, err := unscoped(ctx, o.DB, c.Name); err == nil {
				noWorkspace += n
			}
			if n, err := otherWorkspaces(ctx, o.DB, c.Name, ws.ID); err == nil {
				others += n
			}
		}
	}
	m.Scrubbed = summarize(m.Collections)
	if noWorkspace > 0 {
		m.Notes = append(m.Notes, fmt.Sprintf("%s belonging to no workspace stayed out.", plural(noWorkspace, "record", "records")))
	}
	if others > 0 {
		m.Notes = append(m.Notes, fmt.Sprintf("The database also holds other workspaces (%s); only “%s” is in the archive.", plural(others, "record", "records"), ws.Name))
	}
	// The media.
	o.progress(ArchiveProgress{Phase: "media", Collections: len(exported), CollectionsDone: len(exported)})
	m.Media = ArchiveMedia{Mode: o.Media, Store: o.Store.Locator(), StoreWords: o.StoreWords, Index: MediaIndexName}
	var entries []MediaEntry
	if o.Media == MediaIncluded {
		// The copy's journal and partial downloads belong to this archive:
		// an export of the same archive started again continues them.
		id := storagemove.ID(ws.ID, o.Store.Locator(), dest)
		state, scratch := filepath.Join(work, ".copy"), filepath.Join(work, ".download")
		if o.StateDir != "" {
			state = filepath.Join(o.StateDir, id)
		}
		if o.ScratchDir != "" {
			scratch = filepath.Join(o.ScratchDir, id)
		}
		entries, _, err = includeMedia(ctx, includeOptions{Workspace: ws.ID, From: o.Store, Dir: filepath.Join(work, MediaDir), Cache: o.Cache,
			StateDir: state, ScratchDir: scratch, Progress: func(p storagemove.Progress) {
				o.progress(ArchiveProgress{Phase: "media", Files: p.Files, FilesDone: p.FilesDone, Bytes: p.Bytes, BytesDone: p.BytesDone})
			}})
		if err != nil {
			return nil, apperr.Wrap(apperr.Storage, err, "The footage couldn't all be copied into the archive. Start the export again: it continues where it stopped.")
		}
		// This copy's own notes, done with: the archive holds what they tracked.
		_ = os.RemoveAll(state)
		_ = os.RemoveAll(scratch)
	} else {
		entries, err = ListMedia(ctx, o.Store, ws.ID)
		if err != nil {
			return nil, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't list the workspace's footage in its storage.")
		}
	}
	for _, e := range entries {
		m.Media.Files++
		m.Media.Bytes += e.Size
	}
	idx, err := writeMediaIndex(filepath.Join(work, MediaIndexName), entries)
	if err != nil {
		return nil, err
	}
	m.Files = append(m.Files, idx)
	sort.Slice(m.LeftOut, func(i, j int) bool { return m.LeftOut[i].Name < m.LeftOut[j].Name })
	if err := writeManifest(work, m); err != nil {
		return nil, err
	}
	// The archive is exactly what the manifest lists: anything else in the
	// folder it was made in (this export's own notes) stays out.
	listed := listedFiles(m, entries)
	if !o.Tar {
		if err := pruneUnlisted(work, listed); err != nil {
			return nil, err
		}
	}
	if err := privateTree(work); err != nil {
		return nil, err
	}
	res := &Result{Path: dest, Tar: o.Tar, Manifest: m}
	if o.Tar {
		o.progress(ArchiveProgress{Phase: "packing"})
		n, err := packTar(work, dest, listed, func(done, all int64) {
			o.progress(ArchiveProgress{Phase: "packing", Bytes: all, BytesDone: done})
		})
		if err != nil {
			return nil, err
		}
		res.Bytes = n
		// The folder it was made in is this export's own: the tar holds it all.
		if err := os.RemoveAll(work); err != nil {
			return nil, err
		}
		return res, nil
	}
	if err := os.Rename(work, dest); err != nil {
		return nil, err
	}
	res.Bytes = treeBytes(dest)
	return res, nil
}

// exportCollection writes one collection's file.
func exportCollection(ctx context.Context, o *ExportOptions, work string, c CollectionInfo, ws string, at time.Time, i, n int) (*CollectionFile, error) {
	e := Catalogue[c.Name]
	spec, err := ReadSpec(ctx, o.DB, c)
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read how %s is kept.", c.Name)
	}
	cf := &CollectionFile{Name: c.Name, How: e.How, File: CollectionsDir + "/" + c.Name + ".jsonl", Type: spec.Type}
	if cf.Options, err = ejson(spec.Options); err != nil {
		return nil, err
	}
	for _, ix := range spec.Indexes {
		b, err := ejson(ix)
		if err != nil {
			return nil, err
		}
		cf.Indexes = append(cf.Indexes, b)
	}
	f, err := os.OpenFile(filepath.Join(work, filepath.FromSlash(cf.File)), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	lw := NewLineWriter(f)
	// Fields a unique index covers are never scrubbed (two records made equal
	// would stop a restore halfway); a scrubbed record is checked against
	// them all the same.
	protect, uniqueKeys := uniqueFields(spec.Indexes)
	o.progress(ArchiveProgress{Phase: "documents", Collection: c.Name, Collections: n, CollectionsDone: i})
	err = EachDoc(ctx, o.DB, c.Name, ws, func(raw bson.Raw) error {
		doc := raw
		if e.transform != nil {
			var d bson.D
			if err := bson.Unmarshal(raw, &d); err != nil {
				return err
			}
			if d2, changed := e.transform(d, at); changed {
				b, err := bson.Marshal(d2)
				if err != nil {
					return err
				}
				doc = b
				cf.Transformed++
			}
		}
		clean, changes, err := o.Scrub.Doc(doc, protect)
		if err != nil {
			return err
		}
		if len(changes) > 0 {
			if err := uniqueClash(ctx, o.DB, c.Name, ws, clean, uniqueKeys); err != nil {
				return err
			}
			cf.Scrubbed++
			if cf.ScrubbedKinds == nil {
				cf.ScrubbedKinds = map[string]int64{}
			}
			cf.ScrubbedKinds[changes[0].Kind]++
			for _, ch := range changes {
				if len(cf.ScrubbedAt) < 20 {
					cf.ScrubbedAt = append(cf.ScrubbedAt, Scrubbed{ID: idString(raw), Field: ch.Field, Kind: ch.Kind})
				}
			}
			doc = clean
		}
		if err := lw.Write(doc); err != nil {
			return err
		}
		if lw.Count%5000 == 0 {
			o.progress(ArchiveProgress{Phase: "documents", Collection: c.Name, Collections: n, CollectionsDone: i, Docs: lw.Count})
		}
		return nil
	})
	sum, cerr := lw.Close()
	if ferr := f.Close(); cerr == nil {
		cerr = ferr
	}
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read %s.", c.Name)
	}
	if cerr != nil {
		return nil, cerr
	}
	cf.Count, cf.Bytes, cf.SHA256 = lw.Count, lw.Bytes, sum
	return cf, nil
}

// uniqueClash refuses a scrubbed record whose unique index values another
// record of the workspace already has (read only; the export stops before it
// says the archive is ready).
func uniqueClash(ctx context.Context, db *store.DB, coll, ws string, doc bson.Raw, keys [][]string) error {
	id, _ := doc.LookupErr("_id")
	for _, key := range keys {
		f := bson.D{{Key: "_id", Value: bson.D{{Key: "$ne", Value: id}}}}
		whole := true
		for _, field := range key {
			v, err := doc.LookupErr(strings.Split(field, ".")...)
			if err != nil {
				whole = false // a sparse or partial index doesn't hold it
				break
			}
			f = append(f, bson.E{Key: field, Value: v})
		}
		if !whole {
			continue
		}
		if _, ok := firstKey(key, "workspaceId"); !ok {
			f = append(f, bson.E{Key: "workspaceId", Value: ws})
		}
		n, err := db.C(coll).CountDocuments(ctx, f, options.Count().SetLimit(1))
		if err != nil {
			return err
		}
		if n > 0 {
			return apperr.New(apperr.Integrity, "Taking a key out of a record of %s would make it clash with another record, so the export stopped.", coll).
				WithDetails(map[string]any{"collection": coll, "id": idString(doc), "index": key})
		}
	}
	return nil
}

func firstKey(keys []string, want string) (int, bool) {
	for i, k := range keys {
		if k == want {
			return i, true
		}
	}
	return 0, false
}

// partialMarker records what a folder an export is being made in is for.
type partialMarker struct {
	Workspace string `json:"workspace"`
	Media     string `json:"media"`
	Store     string `json:"store"`
}

const markerName = ".export.json"

func writeMarker(work string, m partialMarker) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(work, markerName), b)
}

func sameMarker(work string, want partialMarker) bool {
	b, err := os.ReadFile(filepath.Join(work, markerName))
	if err != nil {
		return false
	}
	var got partialMarker
	return json.Unmarshal(b, &got) == nil && got == want
}

// listedFiles are the files an archive holds: the manifest, the files it
// lists and, with included media, each media file of the index.
func listedFiles(m *Manifest, entries []MediaEntry) []string {
	out := []string{ManifestName}
	for _, f := range m.Files {
		out = append(out, f.Path)
	}
	if m.Media.Mode == MediaIncluded {
		for _, e := range entries {
			out = append(out, MediaDir+"/"+e.Key)
		}
	}
	return out
}

// pruneUnlisted removes every file of an export's own folder that the
// archive doesn't list (its notes, what an earlier attempt left), and the
// folders left empty.
func pruneUnlisted(work string, listed []string) error {
	keep := make(map[string]bool, len(listed))
	for _, p := range listed {
		keep[p] = true
	}
	var dirs []string
	err := filepath.WalkDir(work, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(work, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." {
				dirs = append(dirs, p)
			}
			return nil
		}
		if !keep[rel] {
			return os.Remove(p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // only when empty
	}
	return nil
}

// treeBytes is the size of every file under a folder.
func treeBytes(root string) int64 {
	var n int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			n += info.Size()
		}
		return nil
	})
	return n
}

// freeBytes is the space left on the disk a folder is on.
func freeBytes(dir string) (int64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}

// ExportPlanOptions are an export's plan: Export's options without writing.
type ExportPlanOptions = ExportOptions

// Plan is what an export would write.
type ExportPlan struct {
	Workspace ArchiveWorkspace `json:"workspace"`
	Level     MigrationLevel   `json:"migrations"`
	// Collections go in (counts and sizes as the database keeps them).
	Collections []ExportPlanCollection `json:"collections"`
	LeftOut     []LeftOutCollection    `json:"leftOut"`
	Media       ArchiveMedia           `json:"media"`
	// Bytes is about how big the archive will be; Free the room where it goes.
	Bytes int64    `json:"bytes"`
	Free  int64    `json:"free,omitempty"`
	Path  string   `json:"path"`
	Notes []string `json:"notes,omitempty"`
}

// ExportPlanCollection is one collection an export writes.
type ExportPlanCollection struct {
	Name  string `json:"name"`
	How   string `json:"how" jsonschema:"enum=as-is,enum=transformed"`
	Why   string `json:"why"`
	Count int64  `json:"count"`
	Bytes int64  `json:"bytes"`
}

// ejsonGrowth is about how much longer a document is as Extended JSON than as BSON.
const ejsonGrowth = 1.4

// MakePlan says what an export would write, reading only.
func MakePlan(ctx context.Context, o ExportOptions) (*ExportPlan, error) {
	if o.Media == "" {
		o.Media = MediaReferenced
	}
	dest := o.To
	if o.Tar && dest != "" {
		dest = TarPath(dest)
	}
	level, err := ReadLevel(ctx, o.DB)
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read the database's migration level.")
	}
	if err := CheckReadable("This database", level); err != nil {
		return nil, err
	}
	if err := checkWhole(ctx, o.DB); err != nil {
		return nil, err
	}
	ws, err := FindWorkspace(ctx, o.DB, o.Workspace)
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read the workspace.")
	}
	if ws == nil {
		return nil, apperr.New(apperr.NotFound, "That workspace isn't in this database.").WithDetails(map[string]any{"workspace": o.Workspace})
	}
	colls, err := ListCollections(ctx, o.DB)
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't list the database's collections.")
	}
	p := &ExportPlan{Workspace: ArchiveWorkspace{ID: ws.ID, Name: ws.Name}, Level: level, Collections: []ExportPlanCollection{}, LeftOut: []LeftOutCollection{}, Path: dest}
	if run := runningNow(ctx, o.DB, ws.ID, o.Here); len(run) > 0 {
		p.Notes = append(p.Notes, fmt.Sprintf("MetaRush is running on %s: what changes while the archive is written may be only partly in it. For an exact copy, quit MetaRush there first.", strings.Join(run, ", ")))
	}
	if level.Older {
		p.Notes = append(p.Notes, fmt.Sprintf("The database is at an older migration level (%d) than this MetaRush (%d). The export reads it as it is and changes nothing; a restore brings the copy up to date.", level.Highest, level.ThisMetaRush))
	}
	for _, c := range colls {
		e, known := Catalogue[c.Name]
		switch {
		case !known:
			n, _ := o.DB.C(c.Name).EstimatedDocumentCount(ctx)
			p.LeftOut = append(p.LeftOut, LeftOutCollection{Name: c.Name, Why: "MetaRush doesn't know this collection, so nothing of it would go in", Count: n, Unknown: true})
		case e.Exported():
			n, b, err := sized(ctx, o.DB, c.Name, ws.ID)
			if err != nil {
				return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read %s.", c.Name)
			}
			est := int64(float64(b) * ejsonGrowth)
			p.Collections = append(p.Collections, ExportPlanCollection{Name: c.Name, How: e.How, Why: e.Why, Count: n, Bytes: est})
			p.Bytes += est
		case e.How == LeftOut:
			n, _ := o.DB.C(c.Name).CountDocuments(ctx, workspaceFilter(ws.ID))
			p.LeftOut = append(p.LeftOut, LeftOutCollection{Name: c.Name, Why: e.Why, Count: n})
		}
	}
	p.Media = ArchiveMedia{Mode: o.Media, StoreWords: o.StoreWords, Index: MediaIndexName}
	if o.Store != nil {
		p.Media.Store = o.Store.Locator()
		entries, err := ListMedia(ctx, o.Store, ws.ID)
		if err != nil {
			return nil, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't list the workspace's footage in its storage.")
		}
		for _, e := range entries {
			p.Media.Files++
			p.Media.Bytes += e.Size
		}
		if o.Media == MediaIncluded {
			p.Bytes += p.Media.Bytes
		}
	} else {
		p.Notes = append(p.Notes, "MetaRush can't reach the workspace's storage here, so the plan doesn't count the footage.")
	}
	if dest != "" {
		if free, ok := freeBytes(filepath.Dir(dest)); ok {
			p.Free = free
			need := p.Bytes
			if o.Tar {
				need *= 2 // the folder it is made in, then the one file
			}
			if free < need {
				p.Notes = append(p.Notes, fmt.Sprintf("There isn't room for it where it would go: it needs about %s and %s is free.", HumanBytes(need), HumanBytes(free)))
			}
		}
		if err := CheckDestination(dest); err != nil {
			var ae *apperr.Error
			if errors.As(err, &ae) {
				p.Notes = append(p.Notes, ae.Message)
			}
		}
	}
	return p, nil
}

// HumanBytes says a size in words.
func HumanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
