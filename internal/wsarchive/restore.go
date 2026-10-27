package wsarchive

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/storagemove"
	"github.com/metavert-io/metarush/internal/store"
)

// RestoreOptions say which archive to restore, and into what.
type RestoreOptions struct {
	Archive string
	// DB is the database the copy goes into: it must be empty.
	DB *store.DB
	// Store is the storage the copy uses: included media is copied into it
	// (by content address, checked); referenced media is looked for there.
	Store      media.Store
	StoreWords string
	// StateDir keeps the media copy's journal (a stopped restore continues
	// it); ScratchDir is where a single-file archive is unpacked and partial
	// copies wait.
	StateDir, ScratchDir string
	// Plan only says what a restore would do: it reads the archive and
	// checks it, and writes nothing anywhere.
	Plan     bool
	Progress func(ArchiveProgress)
}

func (o *RestoreOptions) progress(p ArchiveProgress) {
	if o.Progress != nil {
		o.Progress(p)
	}
}

// RestoreReport is what a restore did (or, as a plan, would do).
type RestoreReport struct {
	Plan      bool             `json:"plan"`
	Workspace ArchiveWorkspace `json:"workspace"`
	MadeAt    time.Time        `json:"madeAt"`
	MadeBy    ArchiveMadeBy    `json:"madeBy"`
	// FromLevel is the migration level the archive was made at; Migrated the
	// migrations the restore ran forward on the copy.
	FromLevel   int                  `json:"fromLevel"`
	ToLevel     int                  `json:"toLevel"`
	Migrated    []string             `json:"migrated"`
	Collections []RestoredCollection `json:"collections"`
	Documents   int64                `json:"documents"`
	LeftOut     []LeftOutCollection  `json:"leftOut"`
	Media       RestoredMedia        `json:"media"`
	// Elsewhere is what stays tied to the computers it was asked on.
	Elsewhere []ComputerWork `json:"elsewhere,omitempty"`
	Notes     []string       `json:"notes,omitempty"`
	// Verified: every checksum matched, every collection holds exactly what
	// the archive does (and, after the migrations, at least as much), and
	// the media is in the storage (included) or was looked for (referenced).
	Verified bool `json:"verified"`
}

// RestoredCollection is one collection the copy holds.
type RestoredCollection struct {
	Name     string `json:"name"`
	Count    int64  `json:"count"`
	Verified bool   `json:"verified"`
}

// RestoredMedia is the copy's media.
type RestoredMedia struct {
	Mode       string `json:"mode" jsonschema:"enum=referenced,enum=included"`
	Files      int    `json:"files"`
	Bytes      int64  `json:"bytes"`
	Store      string `json:"store,omitempty"`
	StoreWords string `json:"storeWords,omitempty"`
	// Copied and Present (included); Present and Missing (referenced).
	Copied  int `json:"copied"`
	Present int `json:"present"`
	Missing int `json:"missing"`
	// MissingKeys are the first missing files' keys.
	MissingKeys []string `json:"missingKeys,omitempty"`
}

// ComputerWork is what stays a computer's in the copy: work asked for on it,
// recipe runs it advances, recipes it plans by itself, Claude Code
// conversations whose sessions it holds.
type ComputerWork struct {
	Computer      string `json:"computer"`
	ID            string `json:"id"`
	Jobs          int64  `json:"jobs"`
	RecipeRuns    int64  `json:"recipeRuns"`
	Triggers      int64  `json:"triggers"`
	Conversations int64  `json:"conversations"`
}

// ErrNotEmpty refuses a database that already holds something.
func ErrNotEmpty(db *store.DB, colls []string) error {
	return apperr.New(apperr.Conflict, "That database already holds things, and a restore writes only into an empty one.").
		WithHint("MetaRush never deletes anything: restore into a new database (another name), or empty this one yourself first.").
		WithDetails(map[string]any{"reason": "not_empty", "database": db.Name, "collections": colls})
}

// CheckEmpty refuses a database that holds any collection (a test database's
// own marker aside).
func CheckEmpty(ctx context.Context, db *store.DB) error {
	colls, err := ListCollections(ctx, db)
	if err != nil {
		return apperr.Wrap(apperr.Database, err, "MetaRush couldn't look into the database.")
	}
	var names []string
	for _, c := range colls {
		if c.Name == store.CTestRun && strings.HasPrefix(db.Name, store.TestDBPrefix) {
			continue
		}
		names = append(names, c.Name)
	}
	if len(names) > 0 {
		return ErrNotEmpty(db, names)
	}
	return nil
}

// Opened is an archive ready to read: its folder and manifest.
type Opened struct {
	Dir      string
	Manifest *Manifest
	// unpacked: a single-file archive unpacked into Dir (removed by Close).
	unpacked bool
}

// Close removes what opening unpacked (never the archive itself).
func (a *Opened) Close() {
	if a != nil && a.unpacked {
		_ = os.RemoveAll(a.Dir)
	}
}

// Open opens an archive: a folder, or a single file unpacked into a new
// private folder under scratch.
func Open(path, scratch string, progress func(ArchiveProgress)) (*Opened, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, apperr.New(apperr.NotFound, "There's no archive there.").WithDetails(map[string]any{"path": path})
	}
	a := &Opened{Dir: path}
	if !st.IsDir() {
		if scratch == "" {
			scratch = os.TempDir()
		}
		if err := os.MkdirAll(scratch, 0o700); err != nil {
			return nil, err
		}
		var rnd [6]byte
		_, _ = rand.Read(rnd[:])
		dir := filepath.Join(scratch, "restore-"+hex.EncodeToString(rnd[:]))
		a.Dir, a.unpacked = dir, true
		if free, ok := freeBytes(scratch); ok && free < st.Size() {
			return nil, apperr.New(apperr.Storage, "There isn't room to unpack the archive: it needs %s and %s is free.", HumanBytes(st.Size()), HumanBytes(free)).
				WithDetails(map[string]any{"folder": scratch})
		}
		if err := unpackTar(path, dir, func(done, all int64) {
			if progress != nil {
				progress(ArchiveProgress{Phase: "unpacking", Bytes: all, BytesDone: done})
			}
		}); err != nil {
			a.Close()
			return nil, apperr.Wrap(apperr.Integrity, err, "The archive can't be unpacked: %v.", err)
		}
	}
	b, err := os.ReadFile(filepath.Join(a.Dir, ManifestName))
	if err != nil {
		a.Close()
		return nil, apperr.New(apperr.InvalidInput, "That isn't an export of a MetaRush workspace (it has no manifest.json).").WithDetails(map[string]any{"path": path})
	}
	m, err := ReadManifest(b)
	if err != nil {
		a.Close()
		return nil, apperr.Wrap(apperr.InvalidInput, err, "%s.", capitalize(err.Error()))
	}
	a.Manifest = m
	return a, nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Mismatch is a file whose bytes aren't what the manifest says.
type Mismatch struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Verify checks every file against the manifest: the collections, the media
// list and, when the media is included, every media file by its SHA-256.
func (a *Opened) Verify(progress func(ArchiveProgress)) error {
	m := a.Manifest
	var bad []Mismatch
	listed := map[string]File{}
	for _, f := range m.Files {
		listed[f.Path] = f
	}
	for _, c := range m.Collections {
		f, ok := listed[c.File]
		if !ok || f.SHA256 != c.SHA256 || f.Bytes != c.Bytes {
			bad = append(bad, Mismatch{Path: c.File, Reason: "the manifest's list of files and of collections disagree"})
		}
	}
	if _, ok := listed[MediaIndexName]; !ok {
		bad = append(bad, Mismatch{Path: MediaIndexName, Reason: "not in the manifest"})
	}
	paths := make([]string, 0, len(listed))
	for p := range listed {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for i, p := range paths {
		f := listed[p]
		if progress != nil {
			progress(ArchiveProgress{Phase: "checking", Files: len(paths), FilesDone: i})
		}
		full := filepath.Join(a.Dir, filepath.FromSlash(p))
		st, err := os.Stat(full)
		if err != nil {
			bad = append(bad, Mismatch{Path: p, Reason: "missing"})
			continue
		}
		if st.Size() != f.Bytes {
			bad = append(bad, Mismatch{Path: p, Reason: "a different size"})
			continue
		}
		sum, err := fileSHA256(full)
		if err != nil || sum != f.SHA256 {
			bad = append(bad, Mismatch{Path: p, Reason: "different bytes (its checksum doesn't match)"})
		}
	}
	if len(bad) == 0 && m.Media.Mode == MediaIncluded {
		entries, err := readMediaIndex(filepath.Join(a.Dir, MediaIndexName))
		if err != nil {
			return apperr.Wrap(apperr.Integrity, err, "The archive's list of footage can't be read.")
		}
		var done int64
		for i, e := range entries {
			if progress != nil && i%50 == 0 {
				progress(ArchiveProgress{Phase: "checking", Files: len(entries), FilesDone: i, Bytes: m.Media.Bytes, BytesDone: done})
			}
			p := MediaDir + "/" + e.Key
			full := filepath.Join(a.Dir, MediaDir, filepath.FromSlash(e.Key))
			st, err := os.Stat(full)
			switch {
			case err != nil:
				bad = append(bad, Mismatch{Path: p, Reason: "missing"})
				continue
			case st.Size() != e.Size:
				bad = append(bad, Mismatch{Path: p, Reason: "a different size"})
				continue
			case e.SHA256 == "":
				bad = append(bad, Mismatch{Path: p, Reason: "no checksum in the list"})
				continue
			}
			sum, err := fileSHA256(full)
			if err != nil || sum != e.SHA256 {
				bad = append(bad, Mismatch{Path: p, Reason: "different bytes (its checksum doesn't match)"})
			}
			done += e.Size
		}
	}
	if len(bad) > 0 {
		shown := bad
		if len(shown) > 20 {
			shown = shown[:20]
		}
		return apperr.New(apperr.Integrity, "The archive isn't as it was made: %s %s changed or missing, so nothing was restored.", plural(int64(len(bad)), "file", "files"), isAre(len(bad))).
			WithHint("Copy the archive again from where it was made, then try again.").
			WithDetails(map[string]any{"reason": "checksum", "files": shown, "count": len(bad)})
	}
	return nil
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// Restore restores an archive into an empty database (and its media into
// the storage): it verifies every checksum first, copies the media (or looks
// for it), writes the documents, sets the migration level from the manifest,
// checks every collection against the archive, runs the migrations forward
// on the copy and checks the counts again. It never deletes anything.
func Restore(ctx context.Context, o RestoreOptions) (*RestoreReport, error) {
	if o.DB == nil {
		return nil, apperr.New(apperr.NotConfigured, "Say which database to restore into.")
	}
	if o.Store == nil {
		return nil, apperr.New(apperr.NotConfigured, "Say which storage the copy uses for its footage.")
	}
	a, err := Open(o.Archive, o.ScratchDir, o.Progress)
	if err != nil {
		return nil, err
	}
	defer a.Close()
	m := a.Manifest
	if err := CheckReadable("The archive", m.Level); err != nil {
		return nil, err
	}
	o.progress(ArchiveProgress{Phase: "checking"})
	if err := a.Verify(o.Progress); err != nil {
		return nil, err
	}
	if err := CheckEmpty(ctx, o.DB); err != nil {
		return nil, err
	}
	rep := &RestoreReport{Plan: o.Plan, Workspace: m.Workspace, MadeAt: m.MadeAt, MadeBy: m.MadeBy, FromLevel: m.Level.Highest,
		Migrated: []string{}, Collections: []RestoredCollection{}, LeftOut: m.LeftOut,
		Media: RestoredMedia{Mode: m.Media.Mode, Files: m.Media.Files, Bytes: m.Media.Bytes, Store: o.Store.Locator(), StoreWords: o.StoreWords}}
	rep.Notes = append(rep.Notes, m.Notes...)
	for _, c := range m.Collections {
		rep.Documents += c.Count
	}
	entries, err := readMediaIndex(filepath.Join(a.Dir, MediaIndexName))
	if err != nil {
		return nil, apperr.Wrap(apperr.Integrity, err, "The archive's list of footage can't be read.")
	}
	// The media first: copying it is the slow part and continues when
	// started again, while the documents go into an empty database once.
	o.progress(ArchiveProgress{Phase: "media", Files: len(entries), Bytes: m.Media.Bytes})
	switch m.Media.Mode {
	case MediaIncluded:
		from := &media.FS{Root: filepath.Join(a.Dir, MediaDir)} // read only: nothing is made in the archive
		mo := storagemove.Options{Workspace: m.Workspace.ID, From: from, To: o.Store, StateDir: o.StateDir, ScratchDir: o.ScratchDir,
			Progress: func(p storagemove.Progress) {
				o.progress(ArchiveProgress{Phase: "media", Files: p.Files, FilesDone: p.FilesDone, Bytes: p.Bytes, BytesDone: p.BytesDone})
			}}
		if mo.StateDir == "" || mo.ScratchDir == "" {
			return nil, apperr.New(apperr.NotConfigured, "Say where the restore keeps its notes while it copies the footage.")
		}
		mo.StateDir = filepath.Join(mo.StateDir, storagemove.ID(m.Workspace.ID, "archive:"+m.MadeAt.Format(time.RFC3339Nano), o.Store.Locator()))
		mo.ScratchDir = filepath.Join(mo.ScratchDir, "media-"+filepath.Base(mo.StateDir))
		if o.Plan {
			s, err := storagemove.Plan(ctx, mo)
			if err != nil {
				return nil, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't look into the storage.")
			}
			rep.Media.Present, rep.Media.Copied = s.Present, s.ToCopy
			break
		}
		res, err := storagemove.Run(ctx, mo)
		if err != nil {
			return nil, apperr.Wrap(apperr.Storage, err, "The footage couldn't all be copied into the storage. Restore again: it continues where it stopped (the database is still empty).")
		}
		if !res.Verified {
			return nil, apperr.New(apperr.Storage, "%s couldn't be copied into the storage, so nothing was written to the database.", plural(int64(len(res.Problems)), "file", "files")).
				WithHint("Restore again: it continues where it stopped.").WithDetails(map[string]any{"problems": res.Problems})
		}
		rep.Media.Copied, rep.Media.Present = res.Copied, res.Present
	default:
		chk, err := checkReferenced(ctx, o.Store, m.Workspace.ID, entries)
		if err != nil {
			return nil, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't look into the storage for the workspace's footage.")
		}
		rep.Media.Present = chk.Present
		rep.Media.Missing = len(chk.Missing) + len(chk.Differ)
		for _, e := range append(chk.Missing, chk.Differ...) {
			if len(rep.Media.MissingKeys) >= 20 {
				break
			}
			rep.Media.MissingKeys = append(rep.Media.MissingKeys, e.Key)
		}
		if rep.Media.Missing > 0 {
			rep.Notes = append(rep.Notes, fmt.Sprintf("%s of the workspace's footage %s not in that storage: the copy shows them, but can't play or render them until they are there (Move the media brings them over).",
				plural(int64(rep.Media.Missing), "file", "files"), isAre(rep.Media.Missing)))
		}
		if o.Store.Locator() == m.Media.Store {
			rep.Notes = append(rep.Notes, "The copy uses the same storage as the original: anything either one deletes (emptying the Trash, clearing out a project) is gone for both.")
		}
	}
	rep.ToLevel = store.SchemaVersion()
	if o.Plan {
		for _, c := range m.Collections {
			rep.Collections = append(rep.Collections, RestoredCollection{Name: c.Name, Count: c.Count})
		}
		for _, v := range Missing(m.Level) {
			rep.Migrated = append(rep.Migrated, migrationName(v))
		}
		rep.Verified = true // every checksum matched
		return rep, nil
	}
	// The documents.
	for i, c := range m.Collections {
		o.progress(ArchiveProgress{Phase: "documents", Collection: c.Name, Collections: len(m.Collections), CollectionsDone: i})
		if err := restoreCollection(ctx, o.DB, a.Dir, c); err != nil {
			return nil, err
		}
	}
	// The migration level the archive was made at.
	for _, ap := range m.Level.Applied {
		doc := bson.M{"_id": ap.Version, "name": ap.Name, "appliedAt": ap.AppliedAt}
		if _, err := o.DB.C(store.CMigrations).InsertOne(ctx, doc); err != nil {
			return nil, apperr.Wrap(apperr.Database, err, "The migration level couldn't be set.")
		}
	}
	// The copy holds exactly what the archive does.
	for i, c := range m.Collections {
		o.progress(ArchiveProgress{Phase: "verifying", Collection: c.Name, Collections: len(m.Collections), CollectionsDone: i})
		d, err := HashCollection(ctx, o.DB, c.Name, m.Workspace.ID)
		if err != nil {
			return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read the copy of %s back.", c.Name)
		}
		if d.Count != c.Count || d.SHA256 != c.SHA256 {
			return nil, apperr.New(apperr.Integrity, "The copy of %s doesn't hold what the archive does (%d of %d records match).", c.Name, d.Count, c.Count).
				WithDetails(map[string]any{"collection": c.Name, "want": c.SHA256, "got": d.SHA256})
		}
		rep.Collections = append(rep.Collections, RestoredCollection{Name: c.Name, Count: c.Count, Verified: true})
	}
	// Then the migrations forward.
	o.progress(ArchiveProgress{Phase: "migrating"})
	applied, err := o.DB.Migrate(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "Bringing the copy up to date stopped: %v.", err).
			WithHint("Run `metarush migrate` against the copy: it picks up where it stopped.")
	}
	rep.Migrated = append(rep.Migrated, applied...)
	// The counts again: migrations only add.
	for i, c := range m.Collections {
		n, err := o.DB.C(c.Name).CountDocuments(ctx, workspaceFilter(m.Workspace.ID))
		if err != nil {
			return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't count the copy of %s.", c.Name)
		}
		if n < c.Count {
			rep.Collections[i].Verified = false
			return nil, apperr.New(apperr.Integrity, "After bringing it up to date, the copy of %s holds fewer records than the archive (%d of %d).", c.Name, n, c.Count)
		}
	}
	// The copy's footage is in this storage when it was included.
	if m.Media.Mode == MediaIncluded {
		id := models.StoreIdentity{Provider: o.Store.Name(), Locator: o.Store.Locator(), Shared: o.Store.Capabilities().Shared}
		if _, err := o.DB.C(store.CWorkspaces).UpdateOne(ctx, bson.M{"_id": m.Workspace.ID}, bson.M{"$set": bson.M{"mediaStore": id}}); err != nil {
			return nil, apperr.Wrap(apperr.Database, err, "The copy couldn't be told where its footage is.")
		}
	} else if ws, err := FindWorkspace(ctx, o.DB, m.Workspace.ID); err == nil && ws != nil && ws.MediaStore.Locator != "" && ws.MediaStore.Locator != o.Store.Locator() {
		rep.Notes = append(rep.Notes, "The workspace keeps its footage in other storage than this one: set this copy up with that storage, or move the media.")
	}
	rep.Elsewhere = elsewhere(ctx, o.DB, m.Workspace.ID)
	rep.Verified = true
	return rep, nil
}

func migrationName(v int) string {
	for _, m := range store.Migrations {
		if m.Version == v {
			return fmt.Sprintf("%d %s", m.Version, m.Name)
		}
	}
	return fmt.Sprint(v)
}

// restoreCollection makes one collection and streams its documents in.
func restoreCollection(ctx context.Context, db *store.DB, dir string, c CollectionFile) error {
	spec := Spec{Name: c.Name, Type: c.Type}
	var err error
	if spec.Options, err = fromEJSON(c.Options); err != nil {
		return apperr.Wrap(apperr.Integrity, err, "How %s is kept can't be read from the archive.", c.Name)
	}
	for _, ix := range c.Indexes {
		raw, err := fromEJSON(ix)
		if err != nil {
			return apperr.Wrap(apperr.Integrity, err, "An index of %s can't be read from the archive.", c.Name)
		}
		spec.Indexes = append(spec.Indexes, raw)
	}
	if err := spec.Make(ctx, db); err != nil {
		return apperr.Wrap(apperr.Database, err, "MetaRush couldn't make %s in the copy.", c.Name)
	}
	f, err := os.Open(filepath.Join(dir, filepath.FromSlash(c.File)))
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := InsertLines(ctx, db, c.Name, f)
	if err != nil {
		return apperr.Wrap(apperr.Database, err, "Writing %s into the copy stopped after %d of %d records.", c.Name, n, c.Count)
	}
	if n != c.Count {
		return apperr.New(apperr.Integrity, "The archive's %s holds %d records, not the %d its manifest says.", c.Name, n, c.Count)
	}
	return nil
}

// elsewhere lists what stays tied to each computer in the copy.
func elsewhere(ctx context.Context, db *store.DB, ws string) []ComputerWork {
	by := map[string]*ComputerWork{}
	get := func(id string) *ComputerWork {
		if by[id] == nil {
			by[id] = &ComputerWork{ID: id, Computer: id}
		}
		return by[id]
	}
	count := func(coll string, field string, filter bson.M, add func(*ComputerWork, int64)) {
		f := bson.M{"workspaceId": ws, field: bson.M{"$exists": true, "$ne": ""}}
		for k, v := range filter {
			f[k] = v
		}
		cur, err := db.C(coll).Aggregate(ctx, bson.A{bson.M{"$match": f}, bson.M{"$group": bson.M{"_id": "$" + field, "n": bson.M{"$sum": 1}}}})
		if err != nil {
			return
		}
		var rows []struct {
			ID string `bson:"_id"`
			N  int64  `bson:"n"`
		}
		if cur.All(ctx, &rows) != nil {
			return
		}
		for _, r := range rows {
			add(get(r.ID), r.N)
		}
	}
	count(store.CJobs, "installation", bson.M{"state": models.JobQueued}, func(c *ComputerWork, n int64) { c.Jobs += n })
	count(store.CRecipeRuns, "installation", bson.M{"state": bson.M{"$in": bson.A{models.RunRunning, models.RunWaiting}}}, func(c *ComputerWork, n int64) { c.RecipeRuns += n })
	count(store.CRecipes, "triggers.installation", nil, func(c *ComputerWork, n int64) { c.Triggers += n })
	count(store.CAssistantConversations, "installationId", bson.M{"archived": bson.M{"$ne": true}}, func(c *ComputerWork, n int64) { c.Conversations += n })
	var out []ComputerWork
	for id, c := range by {
		var inst models.Installation
		if err := db.C(store.CInstallations).FindOne(ctx, bson.M{"_id": id}).Decode(&inst); err == nil {
			c.Computer = firstOf(inst.Name, inst.Hostname, id)
		}
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Computer < out[j].Computer })
	return out
}
