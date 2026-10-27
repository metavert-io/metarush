// Package drive reads Google Drive (D220) and Dropbox (D306) folders through
// the person's own rclone, without syncing them. MetaRush never signs in to Google itself,
// never stores a token and never asks for a Google Cloud project: the person
// adds their Drive to rclone with rclone's own sign-in (its setup, `rclone
// config`), and MetaRush runs the rclone it finds through internal/tools with
// typed arguments —
//
//   - `version` and `listremotes --long`: what is set up (no network);
//   - `lsjson --files-only --hash`: one folder's files, with their Drive IDs,
//     sizes, times and the checksums Google keeps;
//   - `backend copyid`: one file, by its Drive ID, into a file MetaRush chose,
//     then checked against the listing (size, and Google's checksum when it
//     gave one) and hashed by MetaRush.
//
// Dropbox folders (D306) work the same way through remotes of rclone's
// Dropbox type: `lsjson` gives each file's Dropbox ID and Dropbox's content
// hash; a file is fetched by its ID — the folder is listed again to find
// where that ID is now, it is copied with `copyto`, and its size and content
// hash are checked against the listing.
//
// Only remotes of the folder's own type are used (drive for Google Drive,
// dropbox for Dropbox), named exactly as rclone lists them, so nothing in a
// folder's address can make rclone read this computer's files or another
// service. MetaRush never reads rclone's
// settings file. Under `go test` the real rclone is refused; tests use
// internal/testutil/fakerclone.
package drive

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/tools"
)

// The types rclone gives the remotes MetaRush reads.
const (
	DriveType   = "drive"   // Google Drive (D220)
	DropboxType = "dropbox" // Dropbox (D306)
)

// Folder is a Google Drive or Dropbox folder as rclone names it: a remote the
// person set up and a path in it ("gdrive:Teaser/Character art"), and which
// of the two it is (Kind; "" is Google Drive).
type Folder struct {
	Remote string `json:"remote"`
	Path   string `json:"path"`
	Kind   string `json:"kind,omitempty"`
}

// String is the folder as rclone and people write it (remote:path).
func (f Folder) String() string { return f.Remote + ":" + f.Path }

func (f Folder) kind() string {
	if f.Kind == "" {
		return DriveType
	}
	return f.Kind
}

// Service names the kind of folder for people ("Google Drive", "Dropbox").
func Service(kind string) string {
	if kind == DropboxType {
		return "Dropbox"
	}
	return "Google Drive"
}

// Words names the folder for people: “Teaser/Character art” in the Google
// Drive “gdrive” (or the Dropbox “dropbox”).
func (f Folder) Words() string {
	if f.Path == "" {
		return "the " + Service(f.kind()) + " “" + f.Remote + "”"
	}
	return "“" + f.Path + "” in the " + Service(f.kind()) + " “" + f.Remote + "”"
}

// remoteName is what rclone allows in a remote's name: letters, digits, _, -,
// ., +, @ and spaces, never starting with - or a space nor ending with a space.
var remoteName = regexp.MustCompile(`^[A-Za-z0-9_.+@][A-Za-z0-9_.+@ -]*$`)

// ParseFolder reads a Google Drive folder, "remote:path" (the path may be
// empty: the remote's top).
func ParseFolder(s string) (Folder, error) { return ParseFolderOf(DriveType, s) }

// ParseFolderOf reads a folder of a kind (DriveType or DropboxType).
func ParseFolderOf(kind, s string) (Folder, error) {
	s = strings.TrimSpace(s)
	remote, p, ok := strings.Cut(s, ":")
	if !ok || remote == "" {
		example := "gdrive:Teaser/Art"
		if kind == DropboxType {
			example = "dropbox:Teaser/Art"
		}
		return Folder{}, apperr.New(apperr.InvalidInput, "A %s folder is written as the rclone remote's name, a colon and the folder, like %s.", Service(kind), example)
	}
	if len(remote) > 64 || !remoteName.MatchString(remote) || strings.HasSuffix(remote, " ") {
		return Folder{}, apperr.New(apperr.InvalidInput, "“%s” can't be the name of an rclone remote.", shorten(remote, 40)).
			WithHint("rclone names a remote with letters, digits, spaces and _ - . + @, like gdrive.")
	}
	clean, err := cleanPath(p, kind)
	if err != nil {
		return Folder{}, err
	}
	f := Folder{Remote: remote, Path: clean}
	if kind == DropboxType {
		f.Kind = DropboxType
	}
	return f, nil
}

// cleanPath checks a folder's path in a Drive: no control characters, no ..
// or . steps; slashes at the ends are dropped.
func cleanPath(p, kind string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if len(p) > 1000 || !utf8.ValidString(p) {
		return "", apperr.New(apperr.InvalidInput, "That folder's path is too long, or isn't text.")
	}
	if p == "" {
		return "", nil
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", apperr.New(apperr.InvalidInput, "A folder's path in a %s can't have empty parts or . and .. in it.", Service(kind))
		}
		for _, r := range seg {
			if r < 0x20 || r == 0x7f {
				return "", apperr.New(apperr.InvalidInput, "A folder's path in a %s can't have control characters in it.", Service(kind))
			}
		}
	}
	return p, nil
}

// Remote is one remote rclone has set up.
type Remote struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Status is how Google Drive (or Dropbox) through rclone stands on this computer.
type Status struct {
	// Kind is which: DriveType or DropboxType.
	Kind string `json:"kind"`
	// Installed: rclone is there (Version says which).
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	// Remotes are every remote rclone has; Drives the names of its remotes of
	// this kind (Google Drives, or Dropboxes).
	Remotes []Remote `json:"remotes"`
	Drives  []string `json:"drives"`
	// Problem says plainly what stops MetaRush from reading a Drive here ("" nothing);
	// Fix what to do, and Command the command that does it (shown under Details).
	Problem string `json:"problem,omitempty"`
	Fix     string `json:"fix,omitempty"`
	Command string `json:"command,omitempty"`
	// Error is what rclone said, for Details.
	Error string `json:"error,omitempty"`
}

// Ready reports that a folder of this kind can be read here.
func (s Status) Ready() bool { return s.Problem == "" && len(s.Drives) > 0 }

// errRealInTest stops a test from running the person's real rclone (their
// Google account): tests point tools.rclone at a fake.
var errRealInTest = errors.New("refusing to run the real rclone in a test (set tools.rclone to a fake)")

// Setup words, shared by the plan, the run and the checkup (doctor).
const (
	installFix = "Install rclone (from rclone.org, or with Homebrew on a Mac), then add your Google Drive with rclone's own setup: it signs you in with Google in your browser, and MetaRush never sees that sign-in."
	addFix     = "Run rclone's setup in a terminal, add a new remote of the type Google Drive and sign in when it asks. Then name the folder with the remote's name, like gdrive:Teaser/Art."

	installFixDropbox = "Install rclone (from rclone.org, or with Homebrew on a Mac), then add your Dropbox with rclone's own setup: it signs you in with Dropbox in your browser, and MetaRush never sees that sign-in."
	addFixDropbox     = "Run rclone's setup in a terminal, add a new remote of the type Dropbox and sign in when it asks. Then name the folder with the remote's name, like dropbox:Teaser/Art."
)

func fixes(kind string) (install, add string) {
	if kind == DropboxType {
		return installFixDropbox, addFixDropbox
	}
	return installFix, addFix
}

// Check reads what rclone has set up for Google Drive (no network: rclone's own list).
func Check(ctx context.Context, ts *tools.Set) Status { return CheckOf(ctx, ts, DriveType) }

// CheckOf reads what rclone has set up for a kind of folder (DriveType or DropboxType).
func CheckOf(ctx context.Context, ts *tools.Set, kind string) Status {
	st := Status{Kind: kind, Remotes: []Remote{}, Drives: []string{}}
	installFix, addFix := fixes(kind)
	if !ts.Has(tools.Rclone) {
		st.Problem, st.Fix, st.Command = Service(kind)+" folders are read through rclone, which isn't installed on this computer.", installFix, "brew install rclone"
		return st
	}
	st.Installed = true
	out, err := run(ctx, ts, 15*time.Second, nil, "version")
	if err != nil {
		st.Error = err.Error()
		if errors.Is(err, errRealInTest) {
			st.Problem = "MetaRush can't use rclone here."
			return st
		}
		st.Problem, st.Fix = "rclone is installed but didn't answer.", "Run rclone once in a terminal to see what it says, or install it again."
		return st
	}
	st.Version = versionOf(out)
	out, err = run(ctx, ts, 15*time.Second, nil, "listremotes", "--long")
	if err != nil {
		st.Error = err.Error()
		if p, fix, cmd := explain(err, Folder{}); p != "" {
			st.Problem, st.Fix, st.Command = p, fix, cmd
			return st
		}
		st.Problem, st.Fix = "rclone couldn't say which remotes it has.", "Run rclone's setup in a terminal to see what it says."
		st.Command = "rclone config"
		return st
	}
	st.Remotes = parseRemotes(out)
	for _, r := range st.Remotes {
		if r.Type == kind {
			st.Drives = append(st.Drives, r.Name)
		}
	}
	if len(st.Drives) == 0 {
		st.Problem, st.Fix, st.Command = "rclone is installed, but no "+Service(kind)+" is set up in it.", addFix, "rclone config"
	}
	return st
}

// versionOf reads "rclone v1.68.2" from `rclone version`.
func versionOf(out []byte) string {
	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "rclone")), "v")
}

// parseRemotes reads `rclone listremotes --long`: one remote a line, its name
// with a colon, spaces, its type.
func parseRemotes(out []byte) []Remote {
	var rs []Remote
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, "\r ")
		if line == "" {
			continue
		}
		i := strings.Index(line, ":") // a remote's name has no colon; a description after the type may
		if i <= 0 {
			continue
		}
		name, typ := line[:i], strings.TrimSpace(line[i+1:])
		if f := strings.Fields(typ); len(f) > 0 {
			typ = f[0]
		}
		if !remoteName.MatchString(name) {
			continue
		}
		rs = append(rs, Remote{Name: name, Type: typ})
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].Name < rs[j].Name })
	return rs
}

// Ready checks that a folder's remote is one of its kind set up here (a
// Google Drive, or a Dropbox); the error says plainly how to set it up.
func Ready(ctx context.Context, ts *tools.Set, f Folder) error {
	kind := f.kind()
	st := CheckOf(ctx, ts, kind)
	if st.Problem != "" {
		e := apperr.New(apperr.NotConfigured, "%s", st.Problem).WithHint(st.Fix).WithDetails(map[string]any{"rclone": st.Error})
		if st.Command != "" {
			e = e.WithCommand(st.Command)
		}
		return e
	}
	for _, r := range st.Remotes {
		if r.Name != f.Remote {
			continue
		}
		if r.Type != kind {
			return apperr.New(apperr.InvalidInput, "“%s” in rclone isn't a %s (it is a remote of the type %s).", f.Remote, Service(kind), r.Type).
				WithHint("Name a " + Service(kind) + " remote instead: " + joinNames(st.Drives) + ".")
		}
		return nil
	}
	return apperr.New(apperr.NotConfigured, "rclone on this computer has no %s called “%s”.", Service(kind), f.Remote).
		WithHint("Use one it has (" + joinNames(st.Drives) + "), or add it with rclone's setup and sign in when it asks.").WithCommand("rclone config")
}

func joinNames(ns []string) string {
	if len(ns) == 0 {
		return "none yet"
	}
	q := make([]string, len(ns))
	for i, n := range ns {
		q[i] = "“" + n + "”"
	}
	return strings.Join(q, ", ")
}

// File is one file of a Drive (or Dropbox) folder, as rclone lists it.
type File struct {
	// ID is the file's Drive ID (or Dropbox ID, id:…); Name its name in the folder.
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	MimeType string    `json:"mimeType,omitempty"`
	Modified time.Time `json:"modified"`
	// SHA256 and MD5 are the checksums Google keeps for it ("" when it gave none).
	SHA256 string `json:"sha256,omitempty"`
	MD5    string `json:"md5,omitempty"`
	// Dropbox is Dropbox's content hash of it (D306).
	Dropbox string `json:"dropbox,omitempty"`
}

// Checksum is the strongest checksum the service gave ("sha256:…", "md5:…",
// "dropbox:…" or "").
func (f File) Checksum() string {
	switch {
	case f.SHA256 != "":
		return "sha256:" + f.SHA256
	case f.MD5 != "":
		return "md5:" + f.MD5
	case f.Dropbox != "":
		return "dropbox:" + f.Dropbox
	}
	return ""
}

// SetChecksum fills the checksum from its written form ("sha256:…").
func (f *File) SetChecksum(c string) {
	switch kind, sum, _ := strings.Cut(c, ":"); kind {
	case "sha256":
		f.SHA256 = sum
	case "md5":
		f.MD5 = sum
	case "dropbox":
		f.Dropbox = sum
	}
}

// Picture says the file looks like a picture by its type or name (its bytes decide when it is fetched).
func (f File) Picture() bool {
	if strings.HasPrefix(f.MimeType, "image/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(f.Name)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg", ".heic", ".avif":
		return true
	}
	return false
}

// Footage says the file looks like video or sound by its type or name.
func (f File) Footage() bool {
	if strings.HasPrefix(f.MimeType, "video/") || strings.HasPrefix(f.MimeType, "audio/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(f.Name)) {
	case ".mp4", ".mov", ".m4v", ".mkv", ".webm", ".avi", ".mxf", ".mts", ".wav", ".mp3", ".m4a", ".aac", ".aiff", ".flac":
		return true
	}
	return false
}

// listItem is one entry of `rclone lsjson`.
type listItem struct {
	Path     string            `json:"Path"`
	Name     string            `json:"Name"`
	Size     int64             `json:"Size"`
	MimeType string            `json:"MimeType"`
	ModTime  time.Time         `json:"ModTime"`
	IsDir    bool              `json:"IsDir"`
	ID       string            `json:"ID"`
	Hashes   map[string]string `json:"Hashes"`
}

// driveID is what a Drive ID looks like; dropboxID a Dropbox one.
var (
	driveID   = regexp.MustCompile(`^[A-Za-z0-9_-]{8,200}$`)
	dropboxID = regexp.MustCompile(`^id:[A-Za-z0-9_-]{8,200}$`)
)

func validID(kind, id string) bool {
	if kind == DropboxType {
		return dropboxID.MatchString(id)
	}
	return driveID.MatchString(id)
}

var hexOf = map[string]*regexp.Regexp{"sha256": regexp.MustCompile(`^[0-9a-f]{64}$`), "md5": regexp.MustCompile(`^[0-9a-f]{32}$`), "dropbox": regexp.MustCompile(`^[0-9a-f]{64}$`)}

// List lists a folder's files (not its folders, not what is inside them),
// newest first. Google's own documents (no file to fetch), odd names and
// entries without an ID are left out.
func List(ctx context.Context, ts *tools.Set, f Folder) ([]File, error) {
	if err := Ready(ctx, ts, f); err != nil {
		return nil, err
	}
	out, err := run(ctx, ts, 2*time.Minute, nil, "lsjson", "--files-only", "--hash", "--", f.String())
	if err != nil {
		if p, fix, cmd := explain(err, f); p != "" {
			e := apperr.New(apperr.NotFound, "%s", p).WithHint(fix).WithDetails(map[string]any{"rclone": err.Error()})
			if cmd != "" {
				e = e.WithCommand(cmd)
			}
			return nil, e
		}
		return nil, apperr.New(apperr.ToolFailed, "rclone couldn't list %s.", f.Words()).
			WithHint("Check that this computer is online and that the folder is there, then try again.").WithDetails(map[string]any{"rclone": err.Error()})
	}
	var items []listItem
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "rclone's list of the folder %s couldn't be read.", f.Words())
	}
	files := make([]File, 0, len(items))
	for _, it := range items {
		name := it.Path
		if name == "" {
			name = it.Name
		}
		if it.IsDir || it.Size <= 0 || !validID(f.kind(), it.ID) || !fileName(name) {
			continue
		}
		fl := File{ID: it.ID, Name: name, Size: it.Size, MimeType: it.MimeType, Modified: it.ModTime.UTC()}
		for k, v := range it.Hashes {
			k, v = strings.ToLower(k), strings.ToLower(strings.TrimSpace(v))
			if re := hexOf[k]; re != nil && re.MatchString(v) {
				switch k {
				case "sha256":
					fl.SHA256 = v
				case "md5":
					fl.MD5 = v
				case "dropbox":
					fl.Dropbox = v
				}
			}
		}
		files = append(files, fl)
	}
	sort.SliceStable(files, func(i, j int) bool {
		if !files[i].Modified.Equal(files[j].Modified) {
			return files[i].Modified.After(files[j].Modified)
		}
		return files[i].Name < files[j].Name
	})
	return files, nil
}

// fileName accepts a file's name in a folder: text, no slashes or control characters.
func fileName(n string) bool {
	if n == "" || n == "." || n == ".." || len(n) > 512 || !utf8.ValidString(n) || strings.ContainsAny(n, "/\\") {
		return false
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// Fetched is a file fetched from a Drive: where it is and what it is.
type Fetched struct {
	Path    string
	Size    int64
	SHA256  string
	MD5     string
	Dropbox string
}

// FetchOption steers a fetch.
type FetchOption int

// JustListed: the file comes from a listing of the folder made moments ago in
// the same request, so rclone's set-up isn't checked again and a Dropbox
// file's place isn't looked up again (its checksum still is).
const JustListed FetchOption = 1

// Fetch copies one listed file, by its ID, into a new folder under scratch
// and checks it: its size, the service's checksum when the listing had one,
// and at most max bytes. The caller removes filepath.Dir(Path).
func Fetch(ctx context.Context, ts *tools.Set, f Folder, file File, scratch string, max int64, opts ...FetchOption) (*Fetched, error) {
	service := Service(f.kind())
	justListed := slices.Contains(opts, JustListed)
	if !validID(f.kind(), file.ID) || !fileName(file.Name) {
		return nil, apperr.New(apperr.InvalidInput, "That file can't be fetched from %s (it has no usable ID or name).", service)
	}
	if max > 0 && file.Size > max {
		return nil, apperr.New(apperr.InvalidInput, "“%s” in %s is larger than MetaRush takes from a %s folder (%s).", file.Name, f.Words(), service, sizeWords(max))
	}
	if !justListed {
		if err := Ready(ctx, ts, f); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(scratch, "drive-")
	if err != nil {
		return nil, err
	}
	dest := filepath.Join(dir, "file")
	if !filepath.IsAbs(dest) || strings.Contains(dest, ":") {
		_ = os.RemoveAll(dir)
		return nil, apperr.New(apperr.Internal, "MetaRush's scratch folder can't take a file from %s (its path has a colon).", service)
	}
	limit := 10*time.Minute + time.Duration(file.Size/(50<<20))*time.Minute
	args := []string{"backend", "copyid", "--", f.Remote + ":", file.ID, dest}
	if f.kind() == DropboxType {
		// Dropbox: find where the file with this ID is now (a renamed file is
		// still found), then copy it from there; a listing moments old says it.
		name := file.Name
		if !justListed {
			if name, err = dropboxPath(ctx, ts, f, file.ID); err != nil {
				_ = os.RemoveAll(dir)
				return nil, err
			}
		}
		args = []string{"copyto", "--", f.Remote + ":" + strings.TrimPrefix(f.Path+"/"+name, "/"), dest}
	}
	if _, err := run(ctx, ts, limit, nil, args...); err != nil {
		_ = os.RemoveAll(dir)
		if p, fix, cmd := explain(err, f); p != "" {
			e := apperr.New(apperr.NotFound, "%s", p).WithHint(fix).WithDetails(map[string]any{"rclone": err.Error()})
			if cmd != "" {
				e = e.WithCommand(cmd)
			}
			return nil, e
		}
		return nil, apperr.New(apperr.ToolFailed, "rclone couldn't fetch “%s” from %s.", file.Name, f.Words()).
			WithHint("Check that this computer is online and that the file is still there, then try again.").WithDetails(map[string]any{"rclone": err.Error()})
	}
	got, err := hashFile(dest, max, service)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	switch {
	case got.Size != file.Size:
		err = apperr.New(apperr.Conflict, "“%s” in %s changed while it was fetched (its size isn't what %s listed).", file.Name, f.Words(), service).WithHint("Look at the folder again, then try again.")
	case file.SHA256 != "" && got.SHA256 != file.SHA256, file.SHA256 == "" && file.MD5 != "" && got.MD5 != file.MD5,
		file.SHA256 == "" && file.MD5 == "" && file.Dropbox != "" && got.Dropbox != file.Dropbox:
		err = apperr.New(apperr.Conflict, "“%s” in %s isn't the file %s listed (its checksum differs): it changed since the folder was looked at.", file.Name, f.Words(), service).WithHint("Look at the folder again, then try again.")
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	got.Path = dest
	return got, nil
}

// hashFile reads a fetched file once: its size, SHA-256 and MD5, refusing more than max bytes.
func hashFile(p string, max int64, service string) (*Fetched, error) {
	fh, err := os.Open(p)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't read the file rclone fetched.")
	}
	defer fh.Close()
	s, m, d := sha256.New(), md5.New(), newDropboxHash()
	var r io.Reader = fh
	if max > 0 {
		r = io.LimitReader(fh, max+1)
	}
	n, err := io.Copy(io.MultiWriter(s, m, d), r)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't read the file rclone fetched.")
	}
	if max > 0 && n > max {
		return nil, apperr.New(apperr.InvalidInput, "The file rclone fetched is larger than MetaRush takes from a %s folder (%s).", service, sizeWords(max))
	}
	return &Fetched{Size: n, SHA256: hex.EncodeToString(s.Sum(nil)), MD5: hex.EncodeToString(m.Sum(nil)), Dropbox: d.hex()}, nil
}

// dropboxPath is where the file with a Dropbox ID is now in a folder.
func dropboxPath(ctx context.Context, ts *tools.Set, f Folder, id string) (string, error) {
	files, err := List(ctx, ts, f)
	if err != nil {
		return "", err
	}
	for _, fl := range files {
		if fl.ID == id {
			return fl.Name, nil
		}
	}
	return "", apperr.New(apperr.NotFound, "A file of %s is no longer there.", f.Words()).WithHint("Look at the folder again, then try again.")
}

// dropboxHash is Dropbox's content hash: the SHA-256 of the SHA-256s of each
// 4 MiB block, as Dropbox lists it (D306).
type dropboxHash struct {
	block   []byte
	blocks  []byte
	pending int
}

const dropboxBlock = 4 << 20

func newDropboxHash() *dropboxHash { return &dropboxHash{block: make([]byte, 0, dropboxBlock)} }

func (d *dropboxHash) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		room := dropboxBlock - len(d.block)
		take := min(room, len(p))
		d.block = append(d.block, p[:take]...)
		p = p[take:]
		if len(d.block) == dropboxBlock {
			sum := sha256.Sum256(d.block)
			d.blocks = append(d.blocks, sum[:]...)
			d.block = d.block[:0]
		}
	}
	return n, nil
}

func (d *dropboxHash) hex() string {
	blocks := d.blocks
	if len(d.block) > 0 {
		sum := sha256.Sum256(d.block)
		blocks = append(append([]byte(nil), blocks...), sum[:]...)
	}
	sum := sha256.Sum256(blocks)
	return hex.EncodeToString(sum[:])
}

// DropboxContentHash is Dropbox's content hash of bytes (for tests and the fake).
func DropboxContentHash(b []byte) string {
	d := newDropboxHash()
	_, _ = d.Write(b)
	return d.hex()
}

func sizeWords(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%d GB", n>>30)
	}
	return fmt.Sprintf("%d MB", n>>20)
}

// explain turns what rclone said into plain words: the problem, what to do
// and the command that does it ("" when it is none of the known ones).
func explain(err error, f Folder) (problem, fix, command string) {
	msg := strings.ToLower(err.Error())
	remote := f.Remote
	service := Service(f.kind())
	_, add := fixes(f.kind())
	switch {
	case strings.Contains(msg, "unable to decrypt configuration") || strings.Contains(msg, "configuration encrypted") || strings.Contains(msg, "rclone_config_pass"):
		return "rclone's settings are locked with a password, so MetaRush can't use them.",
			"Give rclone its password the way its documentation says (in the environment MetaRush runs in), or keep the " + service + " remote in settings without a password.", ""
	case strings.Contains(msg, "didn't find section in config file") || strings.Contains(msg, "not found in config file"):
		return fmt.Sprintf("rclone on this computer has no remote called “%s”.", remote), add, "rclone config"
	case strings.Contains(msg, "invalid_grant") || strings.Contains(msg, "token expired") || strings.Contains(msg, "couldn't fetch token") || strings.Contains(msg, "reconnect") ||
		strings.Contains(msg, "expired_access_token") || strings.Contains(msg, "invalid_access_token"):
		cmd := "rclone config reconnect " + remote + ":"
		if remote == "" {
			cmd = "rclone config reconnect"
		}
		if f.kind() == DropboxType {
			return "Dropbox asks you to sign in again in rclone.", "Reconnect the remote with rclone's own command in a terminal and sign in when it asks; then try again.", cmd
		}
		return "Google asks you to sign in to your Drive again in rclone.", "Reconnect the remote with rclone's own command in a terminal and sign in when it asks; then try again.", cmd
	case strings.Contains(msg, "directory not found") || strings.Contains(msg, "dir not found") || (f.kind() == DropboxType && strings.Contains(msg, "path/not_found") && !strings.Contains(msg, "copy")):
		return fmt.Sprintf("There's no folder %s.", f.Words()), "Check the folder's name and where it is in the " + service + " (letters must match exactly).", ""
	case strings.Contains(msg, "object not found") || strings.Contains(msg, "file not found") || strings.Contains(msg, "couldn't find file"):
		return fmt.Sprintf("A file of %s is no longer there.", f.Words()), "Look at the folder again, then try again.", ""
	}
	return "", "", ""
}

// run runs rclone with typed arguments (never asking for a password); its
// error carries what rclone said.
func run(ctx context.Context, ts *tools.Set, limit time.Duration, stdout io.Writer, args ...string) ([]byte, error) {
	if testing.Testing() && !ts.Explicit(tools.Rclone) {
		return nil, errRealInTest
	}
	for _, a := range args {
		if strings.ContainsRune(a, 0) {
			return nil, errors.New("rclone: an argument holds NUL")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	full := append([]string{"--ask-password=false", "--log-level", "ERROR"}, args...)
	res, err := ts.Run(ctx, tools.Spec{Tool: tools.Rclone, Args: full, Stdout: stdout})
	if err != nil {
		msg := err.Error()
		if res != nil && len(bytes.TrimSpace(res.Stderr)) > 0 {
			msg = lastLines(string(res.Stderr), 3)
		}
		if ctx.Err() == context.DeadlineExceeded {
			msg = "rclone took too long: " + msg
		}
		return nil, errors.New(msg)
	}
	if res == nil {
		return nil, nil
	}
	return res.Stdout, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return shorten(strings.Join(lines, " "), 600)
}

func shorten(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

// ParseAny reads "remote:path" for a remote of either kind, deciding which
// from what rclone says the remote is (a Google Drive or a Dropbox); any
// other kind of remote is refused.
func ParseAny(ctx context.Context, ts *tools.Set, s string) (Folder, error) {
	f, err := ParseFolder(s)
	if err != nil {
		return Folder{}, apperr.New(apperr.InvalidInput, "A cloud folder is written as the rclone remote's name, a colon and the folder, like gdrive:Teaser/Art or dropbox:Teaser/Art.")
	}
	st := CheckOf(ctx, ts, DriveType)
	if !st.Installed || (st.Error != "" && len(st.Remotes) == 0) {
		msg := st.Problem
		if !st.Installed {
			msg = "Google Drive and Dropbox folders are read through rclone, which isn't installed on this computer."
		}
		e := apperr.New(apperr.NotConfigured, "%s", msg).WithHint(st.Fix).WithDetails(map[string]any{"rclone": st.Error})
		if st.Command != "" {
			e = e.WithCommand(st.Command)
		}
		return Folder{}, e
	}
	for _, r := range st.Remotes {
		if r.Name != f.Remote {
			continue
		}
		switch r.Type {
		case DriveType:
			return f, nil
		case DropboxType:
			f.Kind = DropboxType
			return f, nil
		}
		return Folder{}, apperr.New(apperr.InvalidInput, "“%s” in rclone is a remote of the type %s: MetaRush reads only Google Drive and Dropbox remotes.", f.Remote, r.Type)
	}
	return Folder{}, apperr.New(apperr.NotConfigured, "rclone on this computer has no remote called “%s”.", f.Remote).
		WithHint("Add your Google Drive or Dropbox with rclone's own setup and sign in when it asks.").WithCommand("rclone config")
}
