package wsarchive

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

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
	Plan bool
	// MayStore is asked once the records are in and checked, before an
	// archive's footage is copied into the storage: What may leave (a
	// client's rules keeping footage off cloud storage), which records a
	// refusal in the copy (D333).
	MayStore func(ctx context.Context, db *store.DB, workspace string) error
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
	// Held counts work held for a person: what was running, and paid work
	// that was waiting, when the archive was made.
	Held  int64    `json:"held"`
	Notes []string `json:"notes,omitempty"`
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

// ComputerWork is what stays a computer's in the copy: work asked for on
// it, Recipes' runs it advances and plans it makes by itself, Claude Code
// conversations whose sessions it holds.
type ComputerWork struct {
	Computer string `json:"computer"`
	ID       string `json:"id"`
	Jobs     int64  `json:"jobs"`
	Runs     int64  `json:"runs"` // Recipes' runs

	Triggers      int64 `json:"triggers"`
	Conversations int64 `json:"conversations"`
}

// ErrNotEmpty refuses a database that already holds something.
func ErrNotEmpty(db *store.DB, colls []string) error {
	return apperr.New(apperr.Conflict, "That database already holds things, and a restore writes only into an empty one.").
		WithHint("MetaRush never deletes anything: restore into a new database (another name), or empty this one yourself first.").
		WithDetails(map[string]any{"reason": "not_empty", "database": db.Name, "collections": colls})
}

// CheckEmpty refuses a database that holds any collection (a test database's
// own marker and a restore's marker aside: Restore reads that one itself).
func CheckEmpty(ctx context.Context, db *store.DB) error {
	names, err := holds(ctx, db)
	if err != nil {
		return apperr.Wrap(apperr.Database, err, "MetaRush couldn't look into the database.")
	}
	if len(names) > 0 {
		return ErrNotEmpty(db, names)
	}
	return nil
}

// Holds lists what a database holds — its collections but a test
// database's own marker and a restore's (or a move's) lock: empty means a
// copy may go into it.
func Holds(ctx context.Context, db *store.DB) ([]string, error) { return holds(ctx, db) }

// holds lists a database's collections but the test marker and the restore marker.
func holds(ctx context.Context, db *store.DB) ([]string, error) {
	colls, err := ListCollections(ctx, db)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, c := range colls {
		if c.Name == store.CRestoreLock || c.Name == store.CTestRun && strings.HasPrefix(db.Name, store.TestDBPrefix) {
			continue
		}
		names = append(names, c.Name)
	}
	return names, nil
}

// partial is the error of a restore that stopped once the copy's records
// began going in: its marker stays, so nothing works in the database.
func partial(err error, format string, args ...any) error {
	return apperr.Wrap(apperr.Integrity, err, format, args...).
		WithHint("The database now holds part of the copy, and MetaRush won't work in it. It never deletes anything: restore again into a new, empty database (or empty this one yourself first).").
		WithDetails(map[string]any{"reason": "partial_restore"})
}

// Opened is an archive ready to read: its folder and manifest.
type Opened struct {
	Dir      string
	Manifest *Manifest
	// ID is the archive's identity: the SHA-256 of its manifest.json.
	ID string
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
	sum := sha256.Sum256(b)
	a.Manifest, a.ID = m, hex.EncodeToString(sum[:])
	return a, nil
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// notOptions are collection options an export never writes: a view, a time
// series, a clustered or encrypted collection.
var notOptions = map[string]bool{"viewOn": true, "pipeline": true, "timeseries": true, "clusteredIndex": true, "encryptedFields": true}

// CheckCollections refuses an archive that names a collection an export
// never holds (one the catalogue leaves out — passwords, sign-in links —
// or doesn't know), names one twice, or makes one as something other than a
// plain collection. An archive is restored only as an export makes it,
// whatever its checksums say (D333).
func CheckCollections(m *Manifest) error {
	refuse := func(name, why string) error {
		return apperr.New(apperr.Integrity, "The archive holds %s, which an export never writes, so nothing was restored.", why).
			WithHint("Restore only archives MetaRush made, as they were made.").WithDetails(map[string]any{"reason": "not_an_export", "collection": name})
	}
	seen := map[string]bool{}
	for _, c := range m.Collections {
		e, ok := Catalogue[c.Name]
		switch {
		case !ok:
			return refuse(c.Name, "records of a kind MetaRush doesn't know")
		case !e.Exported():
			return refuse(c.Name, "records of a kind an export leaves out ("+e.Why+")")
		case seen[c.Name]:
			return refuse(c.Name, "the same kind of records twice")
		case c.Type != "" && c.Type != "collection":
			return refuse(c.Name, "records kept as something other than a collection")
		}
		seen[c.Name] = true
		opts, err := fromEJSON(c.Options)
		if err != nil {
			return refuse(c.Name, "settings for a collection it can't read")
		}
		if elems, _ := opts.Elements(); len(elems) > 0 {
			for _, el := range elems {
				if notOptions[el.Key()] || strings.HasPrefix(el.Key(), "$") {
					return refuse(c.Name, "a collection made as a view or another special kind")
				}
			}
		}
	}
	return nil
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
	if err := CheckCollections(m); err != nil {
		return nil, err
	}
	o.progress(ArchiveProgress{Phase: "checking"})
	if err := a.Verify(o.Progress); err != nil {
		return nil, err
	}
	// The database: empty, or holding this very restore stopped while its
	// footage was copied (it continues) or while the copy was brought up to
	// date (it finishes that).
	lock, err := o.DB.ReadRestoreLock(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't look into the database.")
	}
	others, err := holds(ctx, o.DB)
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't look into the database.")
	}
	resume := ""
	switch {
	case lock.Unfinished() && lock.Archive != a.ID:
		return nil, apperr.New(apperr.Conflict, "That database holds part of another restore, and a restore writes only into an empty one.").
			WithHint("Restore into a new, empty database: MetaRush never deletes anything.").WithDetails(map[string]any{"reason": "not_empty", "database": o.DB.Name})
	case lock.Unfinished() && (lock.State == store.RestoreMigrating || lock.State == store.RestoreFootage) && !o.Plan:
		resume = lock.State
	case len(others) > 0:
		if lock.Unfinished() {
			return nil, partial(nil, "This database holds part of a restore of this archive that stopped while its records went in.")
		}
		return nil, ErrNotEmpty(o.DB, others)
	}
	rep := &RestoreReport{Plan: o.Plan, Workspace: m.Workspace, MadeAt: m.MadeAt, MadeBy: m.MadeBy, FromLevel: m.Level.Highest,
		Migrated: []string{}, Collections: []RestoredCollection{}, LeftOut: m.LeftOut, ToLevel: store.SchemaVersion(),
		Media: RestoredMedia{Mode: m.Media.Mode, Files: m.Media.Files, Bytes: m.Media.Bytes, Store: o.Store.Locator(), StoreWords: o.StoreWords}}
	rep.Notes = append(rep.Notes, m.Notes...)
	for _, c := range m.Collections {
		rep.Documents += c.Count
	}
	entries, err := readMediaIndex(filepath.Join(a.Dir, MediaIndexName))
	if err != nil {
		return nil, apperr.Wrap(apperr.Integrity, err, "The archive's list of footage can't be read.")
	}
	mo, err := footageOptions(o, m)
	if err != nil {
		return nil, err
	}
	switch resume {
	case store.RestoreMigrating:
		verifiedFromManifest(rep, m)
		rep.Notes = append(rep.Notes, "This restore had stopped while the copy was brought up to date; it finished that now.")
		return finishRestore(ctx, o, a, rep)
	case store.RestoreFootage:
		verifiedFromManifest(rep, m)
		rep.Notes = append(rep.Notes, "This restore had stopped while its footage was copied; it went on from where it stopped.")
		return footageThenFinish(ctx, o, a, rep, mo)
	}
	// The marker first: from now on nothing else works in this database until
	// the copy is whole and checked.
	if !o.Plan {
		if err := o.DB.SetRestoreLock(ctx, a.ID, m.Workspace.ID, m.Workspace.Name, store.RestoreCopying); err != nil {
			return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't write into the database.")
		}
	}
	// Referenced footage is looked for now (nothing is written); included
	// footage is copied once the records are in and checked, after What may
	// leave said it may go into that storage.
	o.progress(ArchiveProgress{Phase: "media", Files: len(entries), Bytes: m.Media.Bytes})
	switch m.Media.Mode {
	case MediaIncluded:
		if o.Plan {
			s, err := storagemove.Plan(ctx, mo)
			if err != nil {
				return nil, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't look into the storage.")
			}
			rep.Media.Present, rep.Media.Copied = s.Present, s.ToCopy
		}
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
	// Nothing else may have come into the database meanwhile: the copy is
	// never merged with anything.
	if others, err := holds(ctx, o.DB); err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't look into the database.")
	} else if len(others) > 0 {
		_ = o.DB.SetRestoreLock(ctx, a.ID, m.Workspace.ID, m.Workspace.Name, store.RestoreAbandoned) // nothing of the copy is in it
		return nil, ErrNotEmpty(o.DB, others)
	}
	// The records, each collection checked against the archive as soon as it
	// is in.
	for i, c := range m.Collections {
		o.progress(ArchiveProgress{Phase: "documents", Collection: c.Name, Collections: len(m.Collections), CollectionsDone: i})
		if err := restoreCollection(ctx, o.DB, a.Dir, c); err != nil {
			return nil, partial(err, "Writing %s into the copy stopped.", c.Name)
		}
		d, err := HashCollection(ctx, o.DB, c.Name, m.Workspace.ID)
		if err != nil {
			return nil, partial(err, "MetaRush couldn't read the copy of %s back.", c.Name)
		}
		if d.Count != c.Count || d.SHA256 != c.SHA256 {
			return nil, partial(nil, "The copy of %s doesn't hold what the archive does (%d of %d records).", c.Name, d.Count, c.Count)
		}
		rep.Collections = append(rep.Collections, RestoredCollection{Name: c.Name, Count: c.Count, Verified: true})
	}
	if m.Media.Mode == MediaIncluded {
		if err := o.DB.SetRestoreLock(ctx, a.ID, m.Workspace.ID, m.Workspace.Name, store.RestoreFootage); err != nil {
			return nil, partial(err, "MetaRush couldn't write into the database.")
		}
		return footageThenFinish(ctx, o, a, rep, mo)
	}
	return levelThenFinish(ctx, o, a, rep)
}

// footageOptions are an included copy's storagemove options (its journal and
// partial copies named for this archive and storage).
func footageOptions(o RestoreOptions, m *Manifest) (storagemove.Options, error) {
	mo := storagemove.Options{Workspace: m.Workspace.ID, To: o.Store, StateDir: o.StateDir, ScratchDir: o.ScratchDir,
		Progress: func(p storagemove.Progress) {
			o.progress(ArchiveProgress{Phase: "media", Files: p.Files, FilesDone: p.FilesDone, Bytes: p.Bytes, BytesDone: p.BytesDone})
		}}
	if m.Media.Mode != MediaIncluded {
		return mo, nil
	}
	if mo.StateDir == "" || mo.ScratchDir == "" {
		return mo, apperr.New(apperr.NotConfigured, "Say where the restore keeps its notes while it copies the footage.")
	}
	mo.StateDir = filepath.Join(mo.StateDir, storagemove.ID(m.Workspace.ID, "archive:"+m.MadeAt.Format(time.RFC3339Nano), o.Store.Locator()))
	mo.ScratchDir = filepath.Join(mo.ScratchDir, "media-"+filepath.Base(mo.StateDir))
	return mo, nil
}

// verifiedFromManifest lists the collections a resumed restore had checked.
func verifiedFromManifest(rep *RestoreReport, m *Manifest) {
	for _, c := range m.Collections {
		rep.Collections = append(rep.Collections, RestoredCollection{Name: c.Name, Count: c.Count, Verified: true})
	}
}

// footageThenFinish copies an archive's footage into the storage — once What
// may leave allows it there (MayStore) — then sets the level and finishes.
func footageThenFinish(ctx context.Context, o RestoreOptions, a *Opened, rep *RestoreReport, mo storagemove.Options) (*RestoreReport, error) {
	m := a.Manifest
	if o.MayStore != nil {
		if err := o.MayStore(ctx, o.DB, m.Workspace.ID); err != nil {
			var ae *apperr.Error
			if errors.As(err, &ae) {
				ae.Hint = strings.TrimSpace("The records are in this database and no footage was copied: run the same restore again with the footage going into a folder on this computer, and it goes on from there. " + ae.Hint)
				return nil, ae
			}
			return nil, err
		}
	}
	o.progress(ArchiveProgress{Phase: "media", Files: m.Media.Files, Bytes: m.Media.Bytes})
	mo.From = &media.FS{Root: filepath.Join(a.Dir, MediaDir)} // read only: nothing is made in the archive
	res, err := storagemove.Run(ctx, mo)
	if err != nil {
		return nil, apperr.Wrap(apperr.Storage, err, "The footage couldn't all be copied into the storage.").
			WithHint("Run the same restore again: every record is in and checked, and it continues copying the footage where it stopped.")
	}
	if !res.Verified {
		return nil, apperr.New(apperr.Storage, "%s couldn't be copied into the storage.", plural(int64(len(res.Problems)), "file", "files")).
			WithHint("Run the same restore again: it continues where it stopped.").WithDetails(map[string]any{"problems": res.Problems})
	}
	rep.Media.Copied, rep.Media.Present = res.Copied, res.Present
	return levelThenFinish(ctx, o, a, rep)
}

// levelThenFinish writes the migration level the archive was made at, last
// of the copy's records, then finishes.
func levelThenFinish(ctx context.Context, o RestoreOptions, a *Opened, rep *RestoreReport) (*RestoreReport, error) {
	m := a.Manifest
	for _, ap := range m.Level.Applied {
		doc := bson.M{"_id": ap.Version, "name": ap.Name, "appliedAt": ap.AppliedAt}
		if _, err := o.DB.C(store.CMigrations).UpdateOne(ctx, bson.M{"_id": ap.Version}, bson.M{"$setOnInsert": doc}, options.UpdateOne().SetUpsert(true)); err != nil {
			return nil, partial(err, "The copy's migration level couldn't be set.")
		}
	}
	if err := o.DB.SetRestoreLock(ctx, a.ID, m.Workspace.ID, m.Workspace.Name, store.RestoreMigrating); err != nil {
		return nil, partial(err, "MetaRush couldn't write into the database.")
	}
	return finishRestore(ctx, o, a, rep)
}

// finishRestore brings a copy whose records are all in and checked up to
// date, checks its counts and lets go of the database.
func finishRestore(ctx context.Context, o RestoreOptions, a *Opened, rep *RestoreReport) (*RestoreReport, error) {
	m := a.Manifest
	o.progress(ArchiveProgress{Phase: "migrating"})
	applied, err := o.DB.MigrateRestoring(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "Bringing the copy up to date stopped: %v.", sentenceOf(err)).
			WithHint("Run the same restore again: every record is in and checked, and it finishes bringing the copy up to date.")
	}
	rep.Migrated = append(rep.Migrated, applied...)
	// The counts again: migrations only add.
	for i, c := range m.Collections {
		n, err := o.DB.C(c.Name).CountDocuments(ctx, workspaceFilter(m.Workspace.ID))
		if err != nil {
			return nil, partial(err, "MetaRush couldn't count the copy of %s.", c.Name)
		}
		if n < c.Count {
			rep.Collections[i].Verified = false
			return nil, partial(nil, "After bringing it up to date, the copy of %s holds fewer records than the archive (%d of %d).", c.Name, n, c.Count)
		}
	}
	// The copy's footage is in this storage when it was included.
	if m.Media.Mode == MediaIncluded {
		id := models.StoreIdentity{Provider: o.Store.Name(), Locator: o.Store.Locator(), Shared: o.Store.Capabilities().Shared}
		if _, err := o.DB.C(store.CWorkspaces).UpdateOne(ctx, bson.M{"_id": m.Workspace.ID}, bson.M{"$set": bson.M{"mediaStore": id}}); err != nil {
			return nil, partial(err, "The copy couldn't be told where its footage is.")
		}
	} else if ws, err := FindWorkspace(ctx, o.DB, m.Workspace.ID); err == nil && ws != nil && ws.MediaStore.Locator != "" && ws.MediaStore.Locator != o.Store.Locator() {
		rep.Notes = append(rep.Notes, "The workspace keeps its footage in other storage than this one: set this copy up with that storage, or move the media.")
	}
	rep.Elsewhere = elsewhere(ctx, o.DB, m.Workspace.ID)
	if n, err := o.DB.C(store.CJobs).CountDocuments(ctx, bson.M{"workspaceId": m.Workspace.ID, "state": models.JobCanceled, "held": bson.M{"$exists": true}}); err == nil && n > 0 {
		rep.Held = n
		rep.Notes = append(rep.Notes, fmt.Sprintf("%s held here for a person (it was running when the archive was made, or it costs money or acts outside the workspace): start each again (Retry) once you're sure the original isn't doing it.",
			plural(n, "piece of work is", "pieces of work are")))
	}
	if err := o.DB.SetRestoreLock(ctx, a.ID, m.Workspace.ID, m.Workspace.Name, store.RestoreDone); err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "The copy is whole, but MetaRush couldn't mark the restore finished. Run the same restore again to finish it.")
	}
	rep.Verified = true
	return rep, nil
}

// sentenceOf is an error's words without a closing stop.
func sentenceOf(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSuffix(err.Error(), ".")
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
	count(store.CRecipeRuns, "installation", bson.M{"state": bson.M{"$in": bson.A{models.RunRunning, models.RunWaiting}}}, func(c *ComputerWork, n int64) { c.Runs += n })
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
