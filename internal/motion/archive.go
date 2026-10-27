// Package motion is project motion graphics (D68–D70): Remotion components a
// project carries in its own folder (motion/<name>/index.tsx). A component is
// identified by the SHA-256 of a canonical archive of its sources, checked by
// MetaRush (import scan, type check, bundle, test frames in a network-jailed
// headless browser) and approved by a person per hash before anything renders
// it. Vetted templates stay the default; this is the controlled way to carry
// one-off motion in a project.
package motion

import (
	"bytes"
	"crypto/sha256"
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
)

// Format names the canonical archive layout. Changing anything about the
// encoding needs a new format name (every hash would change).
const Format = "metarush.motion/1"

// Limits on a component's sources (the bundle has its own size limit).
const (
	MaxFiles      = 64
	MaxFileBytes  = 512 << 10
	MaxTotalBytes = 1 << 20
	NameMax       = 40
	// Entry is the file every component exports itself from.
	Entry = "index.tsx"
	// PreviewFile holds sample parameters for test frames.
	PreviewFile = "preview.json"
)

var (
	nameRe    = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	segmentRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	hexRe     = regexp.MustCompile(`^[0-9a-f]+$`)
)

// codeExt are the extensions a component may contain.
var codeExt = map[string]bool{".tsx": true, ".ts": true, ".json": true}

// ValidName reports whether n can name a component (its folder under motion/).
func ValidName(n string) bool { return len(n) <= NameMax && nameRe.MatchString(n) }

// ValidHash reports a full SHA-256 in lowercase hex.
func ValidHash(h string) bool { return len(h) == 64 && hexRe.MatchString(h) }

// Short is the version people see: the first 8 characters of the hash.
func Short(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// File is one source file of a component, normalized.
type File struct {
	Path string `json:"path"`
	Text string `json:"text"`
}

type archiveDoc struct {
	Format string `json:"format"`
	Files  []File `json:"files"`
}

// Archive is a component's sources in canonical form: its bytes are what is
// stored (objects/<hash>) and its SHA-256 is the component's identity.
type Archive struct {
	Hash    string
	Bytes   []byte
	Files   []File
	Ignored []string // hidden files left out (names starting with a dot)
}

// File returns a file's text.
func (a *Archive) File(p string) (string, bool) {
	for _, f := range a.Files {
		if f.Path == p {
			return f.Text, true
		}
	}
	return "", false
}

// Size is the total size of the sources.
func (a *Archive) Size() int {
	n := 0
	for _, f := range a.Files {
		n += len(f.Text)
	}
	return n
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

// problem is an archive rule violation, in plain words.
func problem(f string, a ...any) error {
	return apperr.New(apperr.InvalidInput, f, a...)
}

// normalizeText turns text into the archive's form: valid UTF-8 without a
// byte-order mark, LF line endings, no NUL bytes.
func normalizeText(p string, b []byte) (string, error) {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(b) {
		return "", problem("%s isn't text MetaRush can read (it isn't UTF-8).", p)
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return "", problem("%s contains a NUL byte; it doesn't look like source code.", p)
	}
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n"), nil
}

// checkPath validates a relative, slash-separated source path.
func checkPath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || path.Clean(p) != p {
		return problem("%q isn't a plain path inside the component's folder.", p)
	}
	parts := strings.Split(p, "/")
	if len(parts) > 4 || len(p) > 128 {
		return problem("%s is nested too deeply (keep a component's files at most four folders deep).", p)
	}
	for _, s := range parts {
		if !segmentRe.MatchString(s) || s == "." || s == ".." {
			return problem("%q isn't a plain path inside the component's folder.", p)
		}
	}
	return nil
}

func checkKind(p string) error {
	base := path.Base(p)
	ext := strings.ToLower(path.Ext(p))
	switch {
	case base == "package.json" || base == "package-lock.json" || strings.HasSuffix(base, ".lock"):
		return problem("%s: a motion graphic can't bring its own packages; it uses the ones MetaRush ships.", p)
	case base == "tsconfig.json" || base == "jsconfig.json":
		return problem("%s: a motion graphic can't change how it is built; MetaRush's own settings apply.", p)
	case strings.HasSuffix(base, ".d.ts"):
		return problem("%s: type declarations aren't needed; MetaRush provides the types for what a component may import.", p)
	case codeExt[ext]:
		return nil
	case ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".webp" || ext == ".svg" ||
		ext == ".woff" || ext == ".woff2" || ext == ".ttf" || ext == ".otf" || ext == ".mp4" || ext == ".mov" || ext == ".wav" || ext == ".mp3":
		return problem("%s: motion graphics can't include images, fonts or media files yet; draw with code, or use a vetted template.", p)
	case ext == ".js" || ext == ".jsx" || ext == ".mjs" || ext == ".cjs":
		return problem("%s: write components in TypeScript (.tsx or .ts) so MetaRush can type-check them.", p)
	}
	return problem("%s: a motion graphic may only contain .tsx, .ts and .json files.", p)
}

// FromFiles builds the canonical archive from paths and contents.
func FromFiles(in map[string][]byte) (*Archive, error) {
	if len(in) == 0 {
		return nil, problem("The component's folder is empty; it needs %s.", Entry)
	}
	if len(in) > MaxFiles {
		return nil, problem("A motion graphic may have at most %d files (this one has %d).", MaxFiles, len(in))
	}
	files := make([]File, 0, len(in))
	total := 0
	for p, b := range in {
		if err := checkPath(p); err != nil {
			return nil, err
		}
		if err := checkKind(p); err != nil {
			return nil, err
		}
		if len(b) > MaxFileBytes {
			return nil, problem("%s is %d KB; a source file may be at most %d KB.", p, len(b)>>10, MaxFileBytes>>10)
		}
		t, err := normalizeText(p, b)
		if err != nil {
			return nil, err
		}
		total += len(t)
		files = append(files, File{Path: p, Text: t})
	}
	if total > MaxTotalBytes {
		return nil, problem("The component's sources add up to %d KB; the limit is %d KB.", total>>10, MaxTotalBytes>>10)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	has := false
	for _, f := range files {
		has = has || f.Path == Entry
	}
	if !has {
		return nil, problem("The component needs an %s that exports it (export default) with its schema and meta.", Entry)
	}
	b, err := encode(files)
	if err != nil {
		return nil, err
	}
	return &Archive{Hash: sum(b), Bytes: b, Files: files}, nil
}

// Build reads a component folder into its canonical archive. Hidden files and
// folders (names starting with a dot) are left out and listed; links, other
// kinds of files and anything that is not source code are refused.
func Build(dir string) (*Archive, error) {
	st, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, apperr.New(apperr.NotFound, "There's no folder called %s.", filepath.Base(dir)).WithDetails(map[string]any{"path": dir})
		}
		return nil, err
	}
	if !st.IsDir() {
		return nil, apperr.New(apperr.InvalidInput, "%s is a file. Give the motion graphic's folder (the one with %s).", filepath.Base(dir), Entry).WithDetails(map[string]any{"path": dir})
	}
	in := map[string][]byte{}
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
		if strings.HasPrefix(name, ".") {
			ignored = append(ignored, rel)
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return problem("%s is a link to somewhere else; copy the file into the component's folder instead.", rel)
		}
		if d.IsDir() {
			if name == "node_modules" {
				return problem("%s: a motion graphic can't bring its own packages; it uses the ones MetaRush ships.", rel)
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return problem("%s isn't a regular file.", rel)
		}
		if len(in) >= MaxFiles {
			return problem("A motion graphic may have at most %d files.", MaxFiles)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > MaxFileBytes {
			return problem("%s is %d KB; a source file may be at most %d KB.", rel, info.Size()>>10, MaxFileBytes>>10)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		in[rel] = b
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

// Parse reads stored archive bytes and checks that they are canonical, so
// what is unpacked is exactly what was hashed and reviewed.
func Parse(b []byte) (*Archive, error) {
	var doc archiveDoc
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("motion archive: %w", err)
	}
	if doc.Format != Format {
		return nil, fmt.Errorf("motion archive: format %q, this MetaRush reads %q", doc.Format, Format)
	}
	in := map[string][]byte{}
	for _, f := range doc.Files {
		if _, dup := in[f.Path]; dup {
			return nil, fmt.Errorf("motion archive: %s twice", f.Path)
		}
		in[f.Path] = []byte(f.Text)
	}
	a, err := FromFiles(in)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(a.Bytes, b) {
		return nil, errors.New("motion archive: not in canonical form")
	}
	return a, nil
}

// Extract writes the archive's files under dir (which must be empty or new).
func (a *Archive) Extract(dir string) error {
	for _, f := range a.Files {
		p := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(f.Text), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Ref is a component reference as people write it: name@version, where the
// version is the hash or its first characters (at least 8).
type Ref struct {
	Name string
	Hash string // full or a prefix
}

// ParseRef reads "name@3fa9c2d1", "name" (any version) or a bare hash (at
// least 8 hex characters).
func ParseRef(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	name, ver, has := strings.Cut(s, "@")
	if !has {
		if h := strings.TrimPrefix(strings.ToLower(s), "sha256:"); len(h) >= 8 && len(h) <= 64 && hexRe.MatchString(h) {
			return Ref{Hash: h}, nil
		}
		if !ValidName(s) {
			return Ref{}, problem("%q can't name a motion graphic: use lowercase letters, digits and dashes, starting with a letter (like route-map).", s)
		}
		return Ref{Name: s}, nil
	}
	if name != "" && !ValidName(name) {
		return Ref{}, problem("%q can't name a motion graphic: use lowercase letters, digits and dashes, starting with a letter (like route-map).", name)
	}
	ver = strings.ToLower(strings.TrimPrefix(ver, "sha256:"))
	if len(ver) < 8 || len(ver) > 64 || !hexRe.MatchString(ver) {
		return Ref{}, problem("“%s” isn't a version MetaRush can look up. Give the first 8 or more characters of the version.", s)
	}
	return Ref{Name: name, Hash: ver}, nil
}

func (r Ref) String() string {
	if r.Hash == "" {
		return r.Name
	}
	if r.Name == "" {
		return Short(r.Hash)
	}
	return r.Name + "@" + Short(r.Hash)
}
