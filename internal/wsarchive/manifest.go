package wsarchive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Format names the archive format; restore reads only formats it knows.
const Format = "metarush.workspace-archive/1"

// Names inside an archive.
const (
	ManifestName   = "manifest.json"
	CollectionsDir = "collections"
	// MediaIndexName lists the media by content address (both modes); with
	// included media, MediaDir is a folder store holding the objects
	// (media/ws/<workspace>/objects/<aa>/<sha256>).
	MediaIndexName = "media.jsonl"
	MediaDir       = "media"
)

// Media modes.
const (
	MediaReferenced = "referenced"
	MediaIncluded   = "included"
)

// Manifest is manifest.json: what the archive holds and how to check it.
type Manifest struct {
	Format    string           `json:"format"`
	MadeAt    time.Time        `json:"madeAt"`
	MadeBy    ArchiveMadeBy    `json:"madeBy"`
	Workspace ArchiveWorkspace `json:"workspace"`
	Database  DatabaseRef      `json:"database"`
	Level     MigrationLevel   `json:"migrations"`
	Media     ArchiveMedia     `json:"media"`
	// Collections are the files of documents, one per collection.
	Collections []CollectionFile `json:"collections"`
	// LeftOut are the collections nothing of went in, with why.
	LeftOut []LeftOutCollection `json:"leftOut"`
	// Files is every file in the archive but this one, with its SHA-256.
	Files []File `json:"files"`
	// Scrubbed says how many records had something that looked like a key
	// or a password taken out, and where.
	Scrubbed ScrubSummary `json:"scrubbed"`
	// Notes are plain sentences about the copy (MetaRush was running
	// somewhere while it was made, documents belonging to no workspace…).
	Notes []string `json:"notes,omitempty"`
}

// ArchiveMadeBy is the MetaRush (and where) that made the archive.
type ArchiveMadeBy struct {
	MetaRush string `json:"metarush"`
	Commit   string `json:"commit,omitempty"`
	Machine  string `json:"machine,omitempty"`
	Person   string `json:"person,omitempty"`
}

// ArchiveWorkspace names the workspace.
type ArchiveWorkspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DatabaseRef is where the documents came from (never a sign-in).
type DatabaseRef struct {
	Name   string `json:"name"`
	Host   string `json:"host,omitempty"`
	Server string `json:"server,omitempty"`
}

// MigrationLevel is the database's migration level: every migration it had applied.
type MigrationLevel struct {
	Applied []AppliedMigration `json:"applied"`
	// Highest is the highest version applied.
	Highest int `json:"highest"`
	// ThisMetaRush is the newest migration of the MetaRush that made the
	// archive; Older says the database lacked some of its migrations.
	ThisMetaRush int  `json:"thisMetaRush"`
	Older        bool `json:"older"`
}

// AppliedMigration is one migration a database recorded.
type AppliedMigration struct {
	Version   int       `json:"version"`
	Name      string    `json:"name,omitempty"`
	AppliedAt time.Time `json:"appliedAt,omitempty"`
}

// Versions lists the applied versions.
func (l MigrationLevel) Versions() []int {
	out := make([]int, len(l.Applied))
	for i, m := range l.Applied {
		out[i] = m.Version
	}
	return out
}

// ArchiveMedia is the archive's media.
type ArchiveMedia struct {
	Mode string `json:"mode" jsonschema:"enum=referenced,enum=included"`
	// Store is where the workspace kept it (a locator, never a key);
	// StoreWords says it in words.
	Store      string `json:"store"`
	StoreWords string `json:"storeWords,omitempty"`
	Files      int    `json:"files"`
	Bytes      int64  `json:"bytes"`
	// Index is the file listing every object by content address.
	Index string `json:"index"`
}

// CollectionFile is one collection's documents.
type CollectionFile struct {
	Name string `json:"name"`
	How  string `json:"how"`
	File string `json:"file"`
	// Count is how many documents it holds; Bytes and SHA256 are the file's.
	Count  int64  `json:"count"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
	// Type and Options are how the collection was made (listCollections),
	// Indexes its indexes but _id's, each as canonical Extended JSON.
	Type    string            `json:"type,omitempty"`
	Options json.RawMessage   `json:"options,omitempty"`
	Indexes []json.RawMessage `json:"indexes,omitempty"`
	// Transformed counts documents its entry changed; Scrubbed those whose
	// text had something that looked like a key or password taken out (at
	// most the first 20 places in ScrubbedAt).
	Transformed   int64            `json:"transformed,omitempty"`
	Scrubbed      int64            `json:"scrubbed,omitempty"`
	ScrubbedKinds map[string]int64 `json:"scrubbedKinds,omitempty"`
	ScrubbedAt    []Scrubbed       `json:"scrubbedAt,omitempty"`
}

// LeftOutCollection is a collection nothing of went in.
type LeftOutCollection struct {
	Name  string `json:"name"`
	Why   string `json:"why"`
	Count int64  `json:"count"`
	// Unknown: MetaRush doesn't know the collection (named, never silent).
	Unknown bool `json:"unknown,omitempty"`
}

// File is one file of the archive.
type File struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// MediaEntry is one line of media.jsonl.
type MediaEntry struct {
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	Kind   string `json:"kind"`             // object | log | other (storagemove's kinds)
	SHA256 string `json:"sha256,omitempty"` // of the bytes as stored (objects: their content address)
}

// writeManifest writes manifest.json (0600).
func writeManifest(dir string, m *Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(dir, ManifestName), append(b, '\n'))
}

// ReadManifest reads and checks an archive's manifest.json.
func ReadManifest(b []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest.json can't be read: %w", err)
	}
	if !strings.HasPrefix(m.Format, "metarush.workspace-archive/") {
		return nil, fmt.Errorf("this isn't an export of a MetaRush workspace (format %q)", m.Format)
	}
	if m.Format != Format {
		return nil, fmt.Errorf("the archive was made by a newer MetaRush (format %q); update MetaRush to restore it", m.Format)
	}
	if m.Workspace.ID == "" {
		return nil, fmt.Errorf("manifest.json names no workspace")
	}
	for _, c := range m.Collections {
		if !safeRel(c.File) {
			return nil, fmt.Errorf("manifest.json names a file outside the archive: %q", c.File)
		}
	}
	for _, f := range m.Files {
		if !safeRel(f.Path) {
			return nil, fmt.Errorf("manifest.json names a file outside the archive: %q", f.Path)
		}
	}
	return &m, nil
}

// safeRel reports whether p is a plain relative path inside an archive.
func safeRel(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// writePrivate writes a file only its owner can read (0600), atomically.
func writePrivate(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
