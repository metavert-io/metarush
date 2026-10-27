package remotion

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ManifestFormat names the canonical manifest of a version (D131).
const ManifestFormat = "metarush.remotion/1"

// ManifestFile is one file of a version: its path, size and SHA-256 (the file
// itself is the object with that hash in the media store).
type ManifestFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest lists every file of a version, sorted by path. Its canonical bytes
// (compact JSON, fixed field order) hash to the version.
type Manifest struct {
	Format string         `json:"format"`
	Files  []ManifestFile `json:"files"`
}

var hexSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidHash reports a full SHA-256 in hex.
func ValidHash(h string) bool { return hexSHA.MatchString(h) }

// NewManifest sorts the files into a manifest.
func NewManifest(files []ManifestFile) *Manifest {
	fs := append([]ManifestFile(nil), files...)
	sort.Slice(fs, func(i, j int) bool { return fs[i].Path < fs[j].Path })
	return &Manifest{Format: ManifestFormat, Files: fs}
}

// Bytes is the canonical form.
func (m *Manifest) Bytes() []byte {
	var b bytes.Buffer
	b.WriteString(`{"format":"` + ManifestFormat + `","files":[`)
	for i, f := range m.Files {
		if i > 0 {
			b.WriteByte(',')
		}
		p, _ := json.Marshal(f.Path)
		fmt.Fprintf(&b, `{"path":%s,"size":%d,"sha256":"%s"}`, p, f.Size, f.SHA256)
	}
	b.WriteString("]}")
	return b.Bytes()
}

// Hash is the version: the SHA-256 of the canonical bytes.
func (m *Manifest) Hash() string {
	s := sha256.Sum256(m.Bytes())
	return hex.EncodeToString(s[:])
}

// File finds a file by path.
func (m *Manifest) File(p string) (ManifestFile, bool) {
	i := sort.Search(len(m.Files), func(i int) bool { return m.Files[i].Path >= p })
	if i < len(m.Files) && m.Files[i].Path == p {
		return m.Files[i], true
	}
	return ManifestFile{}, false
}

// ParseManifest reads a stored manifest and checks it is the version it
// claims to be: canonical, sorted, unique, safe paths, real hashes.
func ParseManifest(b []byte, want string) (*Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("the version's file list can't be read: %w", err)
	}
	if m.Format != ManifestFormat {
		return nil, fmt.Errorf("the version's file list has an unknown format %q", m.Format)
	}
	for i, f := range m.Files {
		if !safeRel(f.Path) || !ValidHash(f.SHA256) || f.Size < 0 {
			return nil, fmt.Errorf("the version's file list has an unusable entry %q", f.Path)
		}
		if i > 0 && m.Files[i-1].Path >= f.Path {
			return nil, errors.New("the version's file list is not in order")
		}
	}
	if !bytes.Equal(m.Bytes(), b) {
		return nil, errors.New("the version's file list isn't in its canonical form")
	}
	if want != "" && m.Hash() != want {
		return nil, errors.New("the version's file list doesn't match its version")
	}
	return &m, nil
}

// Change is one file a version changes against another.
type Change struct {
	Path   string
	Change string // added | changed | removed
	Before ManifestFile
	After  ManifestFile
}

// Diff lists what changes from a to b (by path, then by content).
func Diff(a, b *Manifest) []Change {
	var out []Change
	i, j := 0, 0
	for i < len(a.Files) || j < len(b.Files) {
		switch {
		case j >= len(b.Files) || (i < len(a.Files) && a.Files[i].Path < b.Files[j].Path):
			out = append(out, Change{Path: a.Files[i].Path, Change: "removed", Before: a.Files[i]})
			i++
		case i >= len(a.Files) || b.Files[j].Path < a.Files[i].Path:
			out = append(out, Change{Path: b.Files[j].Path, Change: "added", After: b.Files[j]})
			j++
		default:
			if a.Files[i].SHA256 != b.Files[j].SHA256 {
				out = append(out, Change{Path: a.Files[i].Path, Change: "changed", Before: a.Files[i], After: b.Files[j]})
			}
			i++
			j++
		}
	}
	return out
}

// totals counts a manifest's files in and outside public/.
func totals(m *Manifest) (files int, bytes int64, pubFiles int, pubBytes int64) {
	for _, f := range m.Files {
		if isPublic(f.Path) {
			pubFiles++
			pubBytes += f.Size
		} else {
			files++
			bytes += f.Size
		}
	}
	return
}

// hasPrefixFold is strings.HasPrefix ignoring case.
func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
