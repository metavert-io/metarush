// Package fakerclone is a stand-in `rclone` for tests and the acceptance run:
// no Google or Dropbox account, no network. Its "Google Drives" and
// "Dropboxes" are folders on this computer named in a state file; it answers
// what MetaRush asks (D220, D306) — `version`, `listremotes --long`, `lsjson
// --files-only --hash`, `backend copyid` (Google Drive) and `copyto`
// (Dropbox) — the way rclone does, including rclone's own words when a remote
// is missing, a sign-in expired, a folder isn't there or the settings are
// locked with a password. A Dropbox remote lists Dropbox IDs (id:…) and
// Dropbox's content hash, and has no copyid.
//
// A file's Drive ID comes from its remote and path, so it stays the same when
// its contents change (as on Drive). METARUSH_FAKE_RCLONE_LOG names a file
// that gets every call's arguments (one JSON array per line).
package fakerclone

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Version is what the fake reports unless the state says otherwise.
const Version = "1.68.2"

const (
	runEnv   = "METARUSH_FAKE_RCLONE"
	stateEnv = "METARUSH_FAKE_RCLONE_STATE"
	logEnv   = "METARUSH_FAKE_RCLONE_LOG"
)

// Remote is one remote the fake has: its name, its type (drive for Google
// Drive) and the folder on this computer that holds its files.
type Remote struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Root string `json:"root"`
	// Expired: Google wants the person to sign in again.
	Expired bool `json:"expired,omitempty"`
	// Renamed maps a Dropbox file's path now to the path it had when its ID
	// was given (a renamed file keeps its ID).
	Renamed map[string]string `json:"renamed,omitempty"`
}

// State is what the fake pretends rclone has.
type State struct {
	Version string   `json:"version,omitempty"`
	Remotes []Remote `json:"remotes"`
	// Locked: rclone's settings have a password, which it is not allowed to ask for.
	Locked bool `json:"locked,omitempty"`
	// Checksums: "" all (sha256, md5, sha1), "md5" only MD5, "none" none (an older rclone).
	Checksums string `json:"checksums,omitempty"`
	// Docs are names of Google documents in each listed folder (no file to fetch: size -1).
	Docs []string `json:"docs,omitempty"`
}

// MaybeRun makes a test binary act as the fake when started through Write's
// wrapper. Call it first in TestMain.
func MaybeRun() {
	if os.Getenv(runEnv) != "1" {
		return
	}
	os.Exit(Main(os.Args[1:], os.Stdout, os.Stderr))
}

// Write puts an `rclone` wrapper in a temp dir that runs this test binary as
// the fake (its TestMain must call MaybeRun) with st as its state, and returns
// the wrapper's path and the state file.
func Write(t testing.TB, st State) (tool, stateFile string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stateFile = filepath.Join(dir, "state.json")
	if err := Save(stateFile, st); err != nil {
		t.Fatal(err)
	}
	tool = filepath.Join(dir, "rclone")
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	script := "#!/bin/sh\nexec env " + runEnv + "=1 " + stateEnv + "=" + q(stateFile) + " " + q(exe) + " \"$@\"\n"
	if err := os.WriteFile(tool, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return tool, stateFile
}

// Load reads a state file.
func Load(p string) State {
	var st State
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

// Save writes a state file.
func Save(p string, st State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

// ID is the Drive ID the fake gives a file: the same for the same remote and path.
func ID(remote, rel string) string {
	s := sha256.Sum256([]byte(remote + "\x00" + filepath.ToSlash(rel)))
	return "1fake" + hex.EncodeToString(s[:])[:28]
}

// DropboxID is the Dropbox ID the fake gives a file (id:…): the same for the
// same remote and its name, so a file keeps it when its contents change, and
// one renamed in the state keeps it through Renamed.
func DropboxID(remote, rel string) string {
	s := sha256.Sum256([]byte("dropbox\x00" + remote + "\x00" + filepath.ToSlash(rel)))
	return "id:fake" + hex.EncodeToString(s[:])[:22]
}

// dropboxContent is Dropbox's content hash (SHA-256 of each 4 MiB block's SHA-256).
func dropboxContent(b []byte) string {
	var blocks []byte
	for len(b) > 0 {
		n := min(len(b), 4<<20)
		sum := sha256.Sum256(b[:n])
		blocks = append(blocks, sum[:]...)
		b = b[n:]
	}
	sum := sha256.Sum256(blocks)
	return hex.EncodeToString(sum[:])
}

func idOf(r *Remote, rel string) string {
	if r.Type == "dropbox" {
		if orig, ok := r.Renamed[filepath.ToSlash(rel)]; ok {
			rel = orig
		}
		return DropboxID(r.Name, rel)
	}
	return ID(r.Name, rel)
}

// Main runs the fake with args and returns the exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if p := os.Getenv(logEnv); p != "" {
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			b, _ := json.Marshal(args)
			_, _ = f.Write(append(b, '\n'))
			_ = f.Close()
		}
	}
	st := Load(os.Getenv(stateEnv))
	flags, pos := split(args)
	if len(pos) == 0 {
		fmt.Fprintln(stderr, "Usage: rclone [flags] <command>")
		return 1
	}
	if pos[0] == "version" {
		v := st.Version
		if v == "" {
			v = Version
		}
		fmt.Fprintf(stdout, "rclone v%s\n- os/version: fake\n- go/version: fake\n", v)
		return 0
	}
	if st.Locked {
		fmt.Fprintln(stderr, "Failed to load config file \"/home/user/.config/rclone/rclone.conf\": unable to decrypt configuration and not allowed to ask for password - set RCLONE_CONFIG_PASS to your configuration password")
		return 1
	}
	switch pos[0] {
	case "listremotes":
		width := 0
		for _, r := range st.Remotes {
			if len(r.Name) > width {
				width = len(r.Name)
			}
		}
		for _, r := range st.Remotes {
			if flags["--long"] {
				fmt.Fprintf(stdout, "%-*s %s\n", width+1, r.Name+":", r.Type)
			} else {
				fmt.Fprintln(stdout, r.Name+":")
			}
		}
		return 0
	case "lsjson":
		if len(pos) != 2 {
			fmt.Fprintln(stderr, "Command lsjson needs 1 arguments")
			return 1
		}
		return lsjson(st, pos[1], flags, stdout, stderr)
	case "backend":
		if len(pos) != 5 || pos[1] != "copyid" {
			fmt.Fprintf(stderr, "the fake rclone only knows backend copyid <remote:> <id> <path>, not %q\n", strings.Join(pos[1:], " "))
			return 2
		}
		return copyID(st, pos[2], pos[3], pos[4], stderr)
	case "copyto":
		if len(pos) != 3 {
			fmt.Fprintln(stderr, "Command copyto needs 2 arguments")
			return 1
		}
		return copyTo(st, pos[1], pos[2], stderr)
	}
	fmt.Fprintf(stderr, "the fake rclone doesn't know the command %q\n", pos[0])
	return 2
}

// split separates flags (and their values) from the command's arguments.
func split(args []string) (map[string]bool, []string) {
	flags := map[string]bool{}
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return flags, append(pos, args[i+1:]...)
		case a == "--log-level":
			i++ // its value
		case strings.HasPrefix(a, "--"):
			k, _, _ := strings.Cut(a, "=")
			flags[k] = true
		default:
			pos = append(pos, a)
		}
	}
	return flags, pos
}

func remoteOf(st State, spec string, stderr io.Writer) (*Remote, string, int) {
	name, rel, ok := strings.Cut(spec, ":")
	if !ok || name == "" {
		fmt.Fprintf(stderr, "the fake rclone reads only remote:path, not %q\n", spec)
		return nil, "", 2
	}
	for i := range st.Remotes {
		r := &st.Remotes[i]
		if r.Name != name {
			continue
		}
		if r.Expired {
			fmt.Fprintf(stderr, "Failed to create file system for %q: couldn't fetch token: invalid_grant: maybe token expired? - try refreshing with \"rclone config reconnect %s:\"\n", spec, name)
			return nil, "", 1
		}
		return r, strings.Trim(rel, "/"), 0
	}
	fmt.Fprintf(stderr, "Failed to create file system for %q: didn't find section in config file (%q)\n", spec, name)
	return nil, "", 1
}

type item struct {
	Path     string            `json:"Path"`
	Name     string            `json:"Name"`
	Size     int64             `json:"Size"`
	MimeType string            `json:"MimeType"`
	ModTime  string            `json:"ModTime"`
	IsDir    bool              `json:"IsDir"`
	Hashes   map[string]string `json:"Hashes,omitempty"`
	ID       string            `json:"ID"`
}

func lsjson(st State, spec string, flags map[string]bool, stdout, stderr io.Writer) int {
	r, rel, code := remoteOf(st, spec, stderr)
	if r == nil {
		return code
	}
	dir := filepath.Join(r.Root, filepath.FromSlash(rel))
	es, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR : : error listing: directory not found\nFailed to lsjson with 2 errors: last error was: error in ListJSON: directory not found\n")
		return 3
	}
	out := []item{}
	for _, e := range es {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if e.IsDir() {
			if !flags["--files-only"] {
				out = append(out, item{Path: e.Name(), Name: e.Name(), Size: -1, MimeType: "inode/directory", ModTime: info.ModTime().UTC().Format(time.RFC3339Nano), IsDir: true, ID: idOf(r, filepath.Join(rel, e.Name()))})
			}
			continue
		}
		it := item{Path: e.Name(), Name: e.Name(), Size: info.Size(), MimeType: mimeOf(e.Name()), ModTime: info.ModTime().UTC().Format(time.RFC3339Nano), ID: idOf(r, filepath.Join(rel, e.Name()))}
		if flags["--hash"] && st.Checksums != "none" && r.Type == "dropbox" {
			if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
				it.Hashes = map[string]string{"dropbox": dropboxContent(b)}
			}
		} else if flags["--hash"] && st.Checksums != "none" {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err == nil {
				m := md5.Sum(b)
				it.Hashes = map[string]string{"md5": hex.EncodeToString(m[:])}
				if st.Checksums != "md5" {
					s := sha256.Sum256(b)
					it.Hashes["sha256"] = hex.EncodeToString(s[:])
				}
			}
		}
		out = append(out, it)
	}
	for _, d := range st.Docs {
		if r.Type == "dropbox" {
			break
		}
		out = append(out, item{Path: d, Name: d, Size: -1, MimeType: "application/vnd.google-apps.document", ModTime: time.Now().UTC().Format(time.RFC3339Nano), ID: ID(r.Name, filepath.Join(rel, d))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	b, _ := json.Marshal(out)
	_, _ = stdout.Write(append(b, '\n'))
	return 0
}

func copyID(st State, spec, id, dest string, stderr io.Writer) int {
	r, _, code := remoteOf(st, spec, stderr)
	if r == nil {
		return code
	}
	if r.Type != "drive" {
		fmt.Fprintf(stderr, "Failed to backend: command \"copyid\" failed: command not found\n")
		return 1
	}
	if !filepath.IsAbs(dest) {
		fmt.Fprintf(stderr, "the fake rclone copies only to a full path, not %q\n", dest)
		return 2
	}
	var found string
	_ = filepath.WalkDir(r.Root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || found != "" {
			return nil
		}
		rel, err := filepath.Rel(r.Root, p)
		if err == nil && ID(r.Name, rel) == id {
			found = p
		}
		return nil
	})
	if found == "" {
		fmt.Fprintf(stderr, "Failed to backend: command \"copyid\" failed: couldn't find file: object not found\n")
		return 4
	}
	in, err := os.Open(found)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	defer in.Close()
	if strings.HasSuffix(dest, "/") {
		dest = filepath.Join(dest, filepath.Base(found))
	}
	out, err := os.Create(dest)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := out.Close(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return 0
}

func mimeOf(name string) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); t != "" {
		t, _, _ = strings.Cut(t, ";")
		return t
	}
	return "application/octet-stream"
}

// copyTo copies one file of a remote (remote:path) to a file on this computer.
func copyTo(st State, spec, dest string, stderr io.Writer) int {
	r, rel, code := remoteOf(st, spec, stderr)
	if r == nil {
		return code
	}
	if !filepath.IsAbs(dest) {
		fmt.Fprintf(stderr, "the fake rclone copies only to a full path, not %q\n", dest)
		return 2
	}
	src := filepath.Join(r.Root, filepath.FromSlash(rel))
	if rel == "" || strings.Contains(rel, "..") {
		fmt.Fprintf(stderr, "the fake rclone copies only files, not %q\n", spec)
		return 2
	}
	in, err := os.Open(src)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR : %s: Failed to copy: path/not_found/..\n", rel)
		return 3
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := out.Close(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return 0
}
