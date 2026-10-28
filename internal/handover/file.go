package handover

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"filippo.io/age"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant"
)

// The handover file (D344): a gzip'd tar — the Claude Code sessions under
// sessions/<conversation>/, the conversations' folders under
// conversations/<conversation>/ and handover.json last (what it holds, every
// file's size and SHA-256, the pinned-work list and the folder settings) —
// encrypted with age's scrypt passphrase mode by default, so `age -d` opens
// it anywhere (`age -d f.age | tar xz`). It never holds a key: the
// computer's keys stay where they are (each computer has its own).

// FileFormat names the handover file's format.
const FileFormat = "metarush.handover/1"

// ageHeader starts every age file.
const ageHeader = "age-encryption.org/v1"

// ScryptWorkFactor is age's scrypt cost for a new file (2^18, about a second;
// tests lower it).
var ScryptWorkFactor = 18

// MinPassphrase is the shortest passphrase a handover file takes.
const MinPassphrase = 10

// Limits on what a file may hold when it is read.
const (
	maxFileBytes  = 8 << 30 // one file in it
	maxTotalBytes = 32 << 30
	maxEntries    = 200000
)

type passphraseKey struct{}

// WithPassphrase gives an export, a receive or a take-over the file's
// passphrase: it never travels in an operation's input, so it is in no log,
// audit or argument list (the CLI asks for it at a terminal prompt or reads
// it from standard input).
func WithPassphrase(ctx context.Context, p string) context.Context {
	return context.WithValue(ctx, passphraseKey{}, p)
}

func passphraseFrom(ctx context.Context) string {
	p, _ := ctx.Value(passphraseKey{}).(string)
	return p
}

// CheckPassphrase says whether a passphrase is long enough for a new file.
func CheckPassphrase(p string) error {
	if utf8.RuneCountInString(strings.TrimSpace(p)) < MinPassphrase {
		return apperr.New(apperr.InvalidInput, "Choose a passphrase of at least %d characters (a few words together works well).", MinPassphrase)
	}
	return nil
}

// fileManifest is handover.json.
type fileManifest struct {
	Format    string           `json:"format"`
	Handover  string           `json:"handover"`
	Workspace HandoverComputer `json:"workspace"`
	From      HandoverComputer `json:"from"`
	To        HandoverComputer `json:"to"`
	MadeAt    time.Time        `json:"madeAt"`
	MadeBy    string           `json:"madeBy"`
	MetaRush  string           `json:"metarush"`
	// AssistantDir was MetaRush's folder for Claude Code on the old computer
	// (the sessions name files under it).
	AssistantDir  string             `json:"assistantDir"`
	Conversations []fileConversation `json:"conversations"`
	Work          []HandoverWork     `json:"work"`
	Folders       []HandoverFolder   `json:"folders"`
	Files         []fileEntry        `json:"files"`
}

type fileConversation struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	SessionID string `json:"sessionId,omitempty"`
	// Session: in (in the file), missing (not on the old computer), lost
	// (Claude Code had lost it already), none (never started).
	Session string `json:"session"`
}

type fileEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

/* ---------- Writing ---------- */

// packer gathers what goes into a handover file: each file's place in it
// and where it is read from (hashed before anything is written, so the
// manifest can go first).
type packer struct {
	entries []packed
}

type packed struct {
	name, src string
}

// addFile adds one regular file (never a link) under name.
func (p *packer) addFile(name, src string) error {
	st, err := os.Lstat(src)
	if err != nil || !st.Mode().IsRegular() {
		return err
	}
	p.entries = append(p.entries, packed{name: name, src: src})
	return nil
}

// addDir adds a folder's regular files (links are left out) under prefix.
func (p *packer) addDir(prefix, dir string) error {
	if st, err := os.Lstat(dir); err != nil || !st.IsDir() {
		return nil
	}
	return filepath.WalkDir(dir, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil // folders are walked; links and other things stay out
		}
		rel, err := filepath.Rel(dir, fp)
		if err != nil {
			return err
		}
		return p.addFile(path.Join(prefix, filepath.ToSlash(rel)), fp)
	})
}

// errChanged: a file changed between being listed and being written.
var errChanged = errors.New("changed while the handover file was being made")

// writeFile writes a handover file at out (never over a file that is there):
// fill names the sessions and folders; each is hashed, then handover.json
// goes first — so a reader checks what the file holds before it unpacks
// anything — and every file after it, checked against what was hashed. With a
// passphrase it is encrypted with age's scrypt mode.
func writeFile(out, pass string, m *fileManifest, fill func(p *packer) error) (int64, error) {
	p := &packer{}
	if err := fill(p); err != nil {
		return 0, err
	}
	m.Files = make([]fileEntry, 0, len(p.entries))
	for _, e := range p.entries {
		size, sum, err := hashOf(e.src)
		if err != nil {
			return 0, err
		}
		m.Files = append(m.Files, fileEntry{Path: e.name, Size: size, SHA256: sum})
	}
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return 0, err
	}
	dir := filepath.Dir(out)
	tmp, err := os.CreateTemp(dir, ".metarush-handover-*.part")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	_ = tmp.Chmod(0o600) // made 0600 already; some drives (exFAT) keep no permissions
	var sink io.WriteCloser = nopCloser{tmp}
	if pass != "" {
		r, err := age.NewScryptRecipient(pass)
		if err != nil {
			return 0, err
		}
		r.SetWorkFactor(ScryptWorkFactor)
		if sink, err = age.Encrypt(tmp, r); err != nil {
			return 0, err
		}
	}
	gz := gzip.NewWriter(sink)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "handover.json", Mode: 0o600, Size: int64(len(mb)), ModTime: time.Now(), Typeflag: tar.TypeReg}); err != nil {
		return 0, err
	}
	if _, err := tw.Write(mb); err != nil {
		return 0, err
	}
	for i, e := range p.entries {
		if err := writeEntry(tw, e, m.Files[i]); err != nil {
			return 0, err
		}
	}
	for _, c := range []io.Closer{tw, gz, sink} {
		if err := c.Close(); err != nil {
			return 0, err
		}
	}
	if err := tmp.Sync(); err != nil {
		return 0, err
	}
	st, err := tmp.Stat()
	if err != nil {
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := placeFile(tmpName, out); err != nil {
		return 0, err
	}
	_ = os.Remove(tmpName)
	ok = true
	return st.Size(), nil
}

// writeEntry writes one file into the tar, exactly as it was hashed.
func writeEntry(tw *tar.Writer, e packed, want fileEntry) error {
	f, err := os.Open(e.src)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o600, Size: want.Size, ModTime: time.Now(), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tw, h), io.LimitReader(f, want.Size))
	if err != nil {
		return err
	}
	if n != want.Size || hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
		return fmt.Errorf("%s %w", e.src, errChanged)
	}
	return nil
}

func hashOf(p string) (int64, string, error) {
	f, err := os.Open(p)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// linkFile is os.Link (tests stand in for a drive that has no hard links).
var linkFile = os.Link

// placeFile puts the finished file at out, never over a file that is there: a
// hard link where the drive has them, else a new file made only if none is
// there (O_EXCL) — USB drives formatted exFAT or FAT keep no hard links.
func placeFile(tmp, out string) error {
	err := linkFile(tmp, out)
	if err == nil || errors.Is(err, os.ErrExist) {
		return err
	}
	dst, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	src, err := os.Open(tmp)
	if err != nil {
		dst.Close()
		_ = os.Remove(out)
		return err
	}
	defer src.Close()
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		_ = os.Remove(out)
		return err
	}
	if err := dst.Sync(); err != nil {
		dst.Close()
		_ = os.Remove(out)
		return err
	}
	return dst.Close()
}

// writable checks a file can be made in a folder (made and removed at once),
// before anything is marked.
func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".metarush-handover-check-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

/* ---------- Reading ---------- */

// errNeedsPassphrase: the file is encrypted and no passphrase came.
var errNeedsPassphrase = errors.New("needs a passphrase")

// Encrypted says whether a file is an age file (it needs a passphrase).
func Encrypted(file string) (bool, error) {
	f, err := os.Open(file)
	if err != nil {
		return false, err
	}
	defer f.Close()
	head := make([]byte, len(ageHeader))
	n, _ := io.ReadFull(f, head)
	return string(head[:n]) == ageHeader, nil
}

// openFile returns the file's tar stream (decrypting it with pass).
func openFile(file, pass string) (*tar.Reader, func(), error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, nil, apperr.Wrap(apperr.NotFound, err, "MetaRush can't open the handover file.").WithDetails(map[string]any{"file": file})
	}
	br := bufio.NewReaderSize(f, 1<<16)
	var r io.Reader = br
	if head, _ := br.Peek(len(ageHeader)); string(head) == ageHeader {
		if pass == "" {
			f.Close()
			return nil, nil, errNeedsPassphrase
		}
		id, err := age.NewScryptIdentity(pass)
		if err != nil {
			f.Close()
			return nil, nil, apperr.New(apperr.InvalidInput, "That passphrase can't be used.")
		}
		dr, err := age.Decrypt(br, id)
		if err != nil {
			f.Close()
			var nomatch *age.NoIdentityMatchError
			if errors.As(err, &nomatch) {
				return nil, nil, apperr.New(apperr.Unauthorized, "That passphrase doesn't open this handover file.").
					WithHint("Type the passphrase chosen when the file was made on the other computer.").WithDetails(map[string]any{"reason": "wrong_passphrase"})
			}
			return nil, nil, apperr.New(apperr.InvalidInput, "This isn't a handover file MetaRush can read.").WithDetails(map[string]any{"reason": "not_handover"})
		}
		r = dr
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		f.Close()
		return nil, nil, apperr.New(apperr.InvalidInput, "This isn't a handover file MetaRush can read.").WithDetails(map[string]any{"reason": "not_handover"})
	}
	return tar.NewReader(gz), func() { gz.Close(); f.Close() }, nil
}

// stageFile unpacks a handover file into dir (a private folder). Its first
// entry must be handover.json: the manifest is read and checked (check, the
// caller's own look at it, and room enough on the disk) before anything is
// unpacked, and every file after it must be listed there, the size it says
// (never more is read) and the SHA-256 it says. Nothing outside sessions/ and
// conversations/ is taken.
func stageFile(file, pass, dir string, check func(*fileManifest) error) (*fileManifest, error) {
	tr, done, err := openFile(file, pass)
	if err != nil {
		return nil, err
	}
	defer done()
	bad := func(why string) error {
		return apperr.New(apperr.InvalidInput, "This handover file is damaged or isn't one MetaRush made (%s).", why).WithDetails(map[string]any{"reason": "damaged"})
	}
	h, err := tr.Next()
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) {
			return nil, err
		}
		return nil, bad("it can't be read")
	}
	if h.Name != "handover.json" || h.Typeflag != tar.TypeReg || h.Size <= 0 || h.Size > 64<<20 {
		return nil, bad("its list isn't first")
	}
	mb, err := io.ReadAll(io.LimitReader(tr, h.Size))
	if err != nil || int64(len(mb)) != h.Size {
		return nil, bad("its list can't be read")
	}
	var m fileManifest
	if err := json.Unmarshal(mb, &m); err != nil {
		return nil, bad("its list can't be read")
	}
	if m.Format != FileFormat {
		return nil, apperr.New(apperr.InvalidInput, "This handover file was made by another version of MetaRush that this one can't read.").
			WithHint("Update MetaRush on this computer, then receive it again.").WithDetails(map[string]any{"format": m.Format})
	}
	want := map[string]fileEntry{}
	var total int64
	for _, e := range m.Files {
		if !safeName(e.Path) || e.Size < 0 || e.Size > maxFileBytes || len(e.SHA256) != 64 {
			return nil, bad("a name in its list points elsewhere")
		}
		if _, dup := want[e.Path]; dup {
			return nil, bad("its list names a file twice")
		}
		want[e.Path] = e
		if total += e.Size; total > maxTotalBytes || len(want) > maxEntries {
			return nil, bad("it is too big")
		}
	}
	if check != nil {
		if err := check(&m); err != nil {
			return nil, err
		}
	}
	if free, ok := freeBytes(dir); ok && uint64(total) > free {
		return nil, apperr.New(apperr.Conflict, "There isn't room on this computer to receive this handover file (it needs %s).", sizeWords(total)).
			WithHint("Make room, then receive it again.").WithDetails(map[string]any{"reason": "no_room", "bytes": total})
	}
	got := map[string]bool{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, bad("it ends too early")
		}
		if h.Typeflag == tar.TypeDir {
			continue
		}
		e, listed := want[h.Name]
		if !listed || got[h.Name] || h.Typeflag != tar.TypeReg || h.Size != e.Size {
			return nil, bad("a file in it doesn't match its list")
		}
		got[h.Name] = true
		dst := filepath.Join(dir, filepath.FromSlash(h.Name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return nil, err
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, bad("a file is in it twice")
		}
		hs := sha256.New()
		c, err := io.Copy(io.MultiWriter(out, hs), io.LimitReader(tr, e.Size))
		cerr := out.Close()
		if err != nil || cerr != nil || c != e.Size {
			return nil, bad("a file in it is cut short")
		}
		if hex.EncodeToString(hs.Sum(nil)) != e.SHA256 {
			return nil, bad("a file in it doesn't match its list")
		}
	}
	if len(got) != len(want) {
		return nil, bad("files are missing")
	}
	return &m, nil
}

func sizeWords(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", (n+1023)/1024)
}

// safeName: a relative name under sessions/<conversation>/ or
// conversations/<conversation>/, with nothing that climbs out.
func safeName(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.ContainsRune(name, 0) {
		return false
	}
	if path.Clean(name) != name {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) < 3 || (parts[0] != "sessions" && parts[0] != "conversations") || !assistant.ValidID(parts[1]) {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return false
		}
	}
	return true
}

// readableSession: every line of a session file is JSON, and it has one.
func readableSession(file string) bool {
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	lines := 0
	for {
		line, err := r.ReadBytes('\n')
		if t := strings.TrimSpace(string(line)); t != "" {
			if !json.Valid([]byte(t)) {
				return false
			}
			lines++
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return false
		}
	}
	return lines > 0
}

// copyTree copies a staged folder's regular files into dst (made 0700),
// never over a file that is there.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(fp string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, fp)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(fp, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// setAside moves what is at p out of the way (renamed beside it, never
// deleted) so a received copy can take its place.
func setAside(p string) error {
	if _, err := os.Lstat(p); err != nil {
		return nil
	}
	return os.Rename(p, p+".before-handover-"+time.Now().UTC().Format("20060102-150405"))
}

func sameFile(a, b string) bool {
	ha, ea := fileHash(a)
	hb, eb := fileHash(b)
	return ea == nil && eb == nil && ha == hb
}

func fileHash(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
