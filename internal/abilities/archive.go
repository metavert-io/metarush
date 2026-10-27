package abilities

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/secretpath"
)

// Format names the canonical archive layout (D230). Changing anything about
// the encoding needs a new format name: every version's hash would change.
const Format = "metarush.ability/1"

// Limits on an ability's folder.
const (
	MaxFiles      = 256
	MaxFileBytes  = 8 << 20
	MaxTotalBytes = 32 << 20
)

var (
	segmentRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+-]*$`)
	hexRe     = regexp.MustCompile(`^[0-9a-f]+$`)
)

// ValidHash reports a full SHA-256 in lowercase hex.
func ValidHash(h string) bool { return len(h) == 64 && hexRe.MatchString(h) }

// Short is the version people see: the first 8 characters of the hash.
func Short(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// File is one file of an ability, as the archive keeps it: text as it is
// (valid UTF-8 without NUL bytes), anything else as base64, and whether it
// is a program (executable).
type File struct {
	Path string `json:"path"`
	Text string `json:"text,omitempty"`
	Data string `json:"data,omitempty"`
	Exec bool   `json:"exec,omitempty"`
}

// Bytes are the file's contents.
func (f File) Bytes() []byte {
	if f.Data != "" {
		b, _ := base64.StdEncoding.DecodeString(f.Data)
		return b
	}
	return []byte(f.Text)
}

type archiveDoc struct {
	Format string `json:"format"`
	Files  []File `json:"files"`
}

// Archive is an ability's folder in canonical form: its bytes are what is
// stored and its SHA-256 is the version.
type Archive struct {
	Hash    string
	Bytes   []byte
	Files   []File
	Ignored []string // hidden files and folders left out (names starting with a dot)
}

// File returns a file's contents.
func (a *Archive) File(p string) ([]byte, bool) {
	for _, f := range a.Files {
		if f.Path == p {
			return f.Bytes(), true
		}
	}
	return nil, false
}

// Programs are the files marked as programs (path → true) and every other
// file (path → false).
func (a *Archive) Programs() map[string]bool {
	out := map[string]bool{}
	for _, f := range a.Files {
		out[f.Path] = f.Exec
	}
	return out
}

// Size is the folder's size in bytes.
func (a *Archive) Size() int64 {
	var n int64
	for _, f := range a.Files {
		n += int64(len(f.Bytes()))
	}
	return n
}

func problem(f string, a ...any) error { return apperr.New(apperr.InvalidInput, f, a...) }

func checkPath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || path.Clean(p) != p {
		return problem("%q isn't a plain path inside the ability's folder.", p)
	}
	parts := strings.Split(p, "/")
	if len(parts) > 6 || len(p) > 200 {
		return problem("%s is nested too deeply (keep an ability's files at most six folders deep).", p)
	}
	for _, s := range parts {
		if !segmentRe.MatchString(s) || s == "." || s == ".." {
			return problem("%q isn't a plain path inside the ability's folder.", p)
		}
	}
	// Installed packages and compiled files never come in, whether from a
	// folder (Build leaves out or refuses them first) or a file (D292).
	for i, s := range parts {
		dir := i < len(parts)-1
		if (dir && installedDirs[s]) || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".pyc") || strings.HasSuffix(s, ".pyo") {
			return problem("%s: an ability doesn't bring installed packages, compiled files or hidden files; a person sets up the programs it needs on each computer.", p)
		}
	}
	return nil
}

// installedDirs are folders of installed packages and compiled files.
var installedDirs = map[string]bool{"node_modules": true, "site-packages": true, "venv": true, ".venv": true, "__pycache__": true, "dist-packages": true}

// FileIn is one file as read from a folder.
type FileIn struct {
	Bytes []byte
	Exec  bool
}

// FromFiles builds the canonical archive: paths sorted, text kept as it is,
// other bytes as base64. Credentials are refused (D233).
func FromFiles(in map[string]FileIn) (*Archive, error) {
	if len(in) == 0 {
		return nil, problem("The ability's folder is empty; it needs %s.", ManifestFile)
	}
	if len(in) > MaxFiles {
		return nil, problem("An ability may have at most %d files (this one has %d).", MaxFiles, len(in))
	}
	files := make([]File, 0, len(in))
	var total int64
	for p, fi := range in {
		if err := checkPath(p); err != nil {
			return nil, err
		}
		if len(fi.Bytes) > MaxFileBytes {
			return nil, problem("%s is %d MB; a file of an ability may be at most %d MB.", p, len(fi.Bytes)>>20, MaxFileBytes>>20)
		}
		total += int64(len(fi.Bytes))
		if what := secretpath.SecretShape(fi.Bytes); what != "" {
			return nil, problem("%s looks like it holds %s. An ability never carries keys: it reads a stand-in from the variable its manifest names, and MetaRush puts the key in on the way out.", p, what)
		}
		if secretpath.IsCredentialFileName(path.Base(p)) {
			return nil, problem("%s is a file of MetaRush's own credentials; it can't be part of an ability.", p)
		}
		f := File{Path: p, Exec: fi.Exec}
		if utf8.Valid(fi.Bytes) && bytes.IndexByte(fi.Bytes, 0) < 0 {
			f.Text = string(fi.Bytes)
		} else {
			f.Data = base64.StdEncoding.EncodeToString(fi.Bytes)
		}
		files = append(files, f)
	}
	if total > MaxTotalBytes {
		return nil, problem("The ability's files add up to %d MB; the limit is %d MB.", total>>20, MaxTotalBytes>>20)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	if _, ok := in[ManifestFile]; !ok {
		return nil, problem("The folder needs an %s that says what the ability does and what it may reach.", ManifestFile)
	}
	b, err := encode(files)
	if err != nil {
		return nil, err
	}
	return &Archive{Hash: sum(b), Bytes: b, Files: files}, nil
}

func encode(files []File) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(archiveDoc{Format: Format, Files: files}); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Build reads an ability's folder into its canonical archive. Hidden files
// and folders (names starting with a dot) are left out and listed; links,
// other kinds of files and installed packages are refused.
func Build(dir string) (*Archive, error) {
	st, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, apperr.New(apperr.NotFound, "There's no folder called %s.", filepath.Base(dir)).WithDetails(map[string]any{"path": dir})
		}
		return nil, err
	}
	if !st.IsDir() {
		return nil, apperr.New(apperr.InvalidInput, "%s is a file. Give the ability's folder (the one with %s).", filepath.Base(dir), ManifestFile).WithDetails(map[string]any{"path": dir})
	}
	in := map[string]FileIn{}
	var ignored []string
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		name := d.Name()
		if strings.HasPrefix(name, ".") || name == "__pycache__" {
			ignored = append(ignored, rel)
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return problem("%s is a link to somewhere else; copy the file into the ability's folder instead.", rel)
		}
		if d.IsDir() {
			if name == "node_modules" || name == "site-packages" || name == "venv" {
				return problem("%s: an ability doesn't bring installed packages; a person sets up the programs it needs on each computer.", rel)
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return problem("%s isn't a regular file.", rel)
		}
		if len(in) >= MaxFiles {
			return problem("An ability may have at most %d files.", MaxFiles)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > MaxFileBytes {
			return problem("%s is %d MB; a file of an ability may be at most %d MB.", rel, info.Size()>>20, MaxFileBytes>>20)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		in[rel] = FileIn{Bytes: b, Exec: info.Mode()&0o111 != 0}
		return nil
	})
	if err != nil {
		return nil, err
	}
	a, err := FromFiles(in)
	if err != nil {
		return nil, err
	}
	sort.Strings(ignored)
	a.Ignored = ignored
	return a, nil
}

// Parse reads stored archive bytes and checks they are canonical, so what
// runs is exactly what was hashed, checked and approved.
func Parse(b []byte) (*Archive, error) {
	var doc archiveDoc
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("ability archive: %w", err)
	}
	if doc.Format != Format {
		return nil, fmt.Errorf("ability archive: format %q, this MetaRush reads %q", doc.Format, Format)
	}
	in := map[string]FileIn{}
	for _, f := range doc.Files {
		if _, dup := in[f.Path]; dup {
			return nil, fmt.Errorf("ability archive: %s twice", f.Path)
		}
		if f.Text != "" && f.Data != "" {
			return nil, fmt.Errorf("ability archive: %s is both text and data", f.Path)
		}
		data := []byte(f.Text)
		if f.Data != "" {
			d, err := base64.StdEncoding.DecodeString(f.Data)
			if err != nil {
				return nil, fmt.Errorf("ability archive: %s: %w", f.Path, err)
			}
			data = d
		}
		in[f.Path] = FileIn{Bytes: data, Exec: f.Exec}
	}
	a, err := FromFiles(in)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(a.Bytes, b) {
		return nil, errors.New("ability archive: not in canonical form")
	}
	return a, nil
}

// Extract writes the archive's files under dir: programs executable,
// everything read-only for the program (it writes only in its own folders).
func (a *Archive) Extract(dir string) error {
	for _, f := range a.Files {
		p := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o444)
		if f.Exec {
			mode = 0o555
		}
		if err := os.WriteFile(p, f.Bytes(), mode); err != nil {
			return err
		}
		if err := os.Chmod(p, mode); err != nil {
			return err
		}
	}
	return nil
}

// Manifest reads the archive's manifest.
func (a *Archive) Manifest() (*Manifest, error) {
	b, ok := a.File(ManifestFile)
	if !ok {
		return nil, problem("The folder needs an %s.", ManifestFile)
	}
	return ParseManifest(b)
}

// Notes is notes.md, cut at NotesMax ("" when there is none).
func (a *Archive) Notes() string {
	b, ok := a.File(NotesFile)
	if !ok {
		return ""
	}
	if len(b) > NotesMax {
		b = b[:NotesMax]
		for len(b) > 0 && !utf8.Valid(b) {
			b = b[:len(b)-1]
		}
		return string(b) + "\n…"
	}
	return string(b)
}

// Ref is an ability as people write it: name@version (the hash or its first
// 8 or more characters), a name (its newest version) or a bare version.
type Ref struct {
	Name string
	Hash string
}

// ParseRef reads "name@3fa9c2d1", "name" or a bare hash.
func ParseRef(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	name, ver, has := strings.Cut(s, "@")
	if !has {
		if h := strings.ToLower(s); len(h) >= 8 && len(h) <= 64 && hexRe.MatchString(h) {
			return Ref{Hash: h}, nil
		}
		if !ValidName(s) {
			return Ref{}, problem("“%s” can't name an ability: use lowercase letters, digits and dashes, starting with a letter (like stock-footage).", s)
		}
		return Ref{Name: s}, nil
	}
	if name != "" && !ValidName(name) {
		return Ref{}, problem("“%s” can't name an ability: use lowercase letters, digits and dashes, starting with a letter (like stock-footage).", name)
	}
	ver = strings.ToLower(ver)
	if len(ver) < 8 || len(ver) > 64 || !hexRe.MatchString(ver) {
		return Ref{}, problem("“%s” isn't a version MetaRush can look up. Give the first 8 or more characters of the version.", s)
	}
	return Ref{Name: name, Hash: ver}, nil
}

func (r Ref) String() string {
	switch {
	case r.Hash == "":
		return r.Name
	case r.Name == "":
		return Short(r.Hash)
	}
	return r.Name + "@" + Short(r.Hash)
}
