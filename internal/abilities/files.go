package abilities

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/sandbox"
	"github.com/metavert-io/metarush/internal/secretpath"
)

// Files a person sets up for an ability on each computer (D285). Every check
// runs on the file as opened — its path as the file system stores it (its
// case and Unicode form on a Mac), its link count, its first bytes — and
// the fingerprint is read from that same open file, so what is checked is
// what is pinned.

// Kinds of file a manifest may ask for (NeededFile.Kind).
const (
	FileWhisperModel      = "whisper-model"      // a whisper.cpp model
	FileWhisperTranslator = "whisper-translator" // a whisper.cpp model that can translate (multilingual, not turbo)
)

// FileKinds are the kinds MetaRush recognises by their first bytes.
var FileKinds = []string{FileWhisperModel, FileWhisperTranslator}

// MaxNeededFileBytes caps a file a person sets up for an ability (a large
// model fits).
const MaxNeededFileBytes = 16 << 30

// pinFile reads a file a person sets up for an ability: a full path to a
// regular file with one name (links followed), what it is, its size, time
// and SHA-256. It may be anywhere MetaRush can read, the person's home
// included — MetaRush copies it into each run's room, and the sandbox only
// ever sees the copy — but never where MetaRush, apps or the system keep
// sign-ins, keys and settings, and never a key store of any kind.
func pinFile(a *app.App, nf models.AbilityNeededFile, path string) (models.AbilityFilePin, error) {
	no := func(f string, args ...any) (models.AbilityFilePin, error) {
		return models.AbilityFilePin{}, apperr.New(apperr.InvalidInput, "MetaRush won't give an ability %s: %s.", path, fmt.Sprintf(f, args...)).
			WithHint("Give the file it asks for (a model, for instance), kept somewhere of its own.")
	}
	if !filepath.IsAbs(path) {
		return models.AbilityFilePin{}, apperr.New(apperr.InvalidInput, "Give the %s file's full path.", nf.Name)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return models.AbilityFilePin{}, apperr.New(apperr.NotFound, "There's no file at %s.", path)
	}
	f, err := os.OpenFile(real, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return models.AbilityFilePin{}, apperr.New(apperr.NotFound, "MetaRush can't read %s.", path)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return no("it isn't a regular file")
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && sys.Nlink > 1 {
		return no("it has more than one name on this computer (a hard link), so MetaRush can't tell where it really lives")
	}
	canon := fdPath(f)
	if canon == "" {
		canon = real
	}
	if why := notToGive(a, canon); why != "" {
		return no("%s", why)
	}
	if st.Size() > MaxNeededFileBytes {
		return models.AbilityFilePin{}, apperr.New(apperr.InvalidInput, "%s is larger than %s, the most MetaRush copies into a run.", path, sizeWords(MaxNeededFileBytes))
	}
	head := make([]byte, 64<<10)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if why := keyStore(head, canon); why != "" {
		return no("%s", why)
	}
	if text, _ := io.ReadAll(io.LimitReader(io.MultiReader(bytes.NewReader(head), f), 1<<20)); len(text) > 0 {
		if what := secretpath.SecretShape(text); what != "" {
			return no("it looks like it holds %s; MetaRush never gives an ability a key", what)
		}
	}
	kind, words := fileKind(head)
	if why := kindProblem(nf, kind, head); why != "" {
		return models.AbilityFilePin{}, apperr.New(apperr.InvalidInput, "%s isn't what “%s” asks for: %s.", path, nf.Name, why)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return models.AbilityFilePin{}, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return models.AbilityFilePin{}, err
	}
	// The time as the database keeps it (milliseconds).
	return models.AbilityFilePin{Name: nf.Name, Path: canon, Size: st.Size(), ModTime: st.ModTime().UTC().Truncate(time.Millisecond), SHA256: hex.EncodeToString(h.Sum(nil)),
		Kind: kind, About: words}, nil
}

// systemPlaces are where apps and the system keep sign-ins, settings and
// devices (the person's Library is added at run time).
var systemPlaces = []string{"/etc", "/private/etc", "/Library/Keychains", "/var/db", "/private/var/db", "/proc", "/sys", "/dev", "/private/var/run", "/run"}

// notToGive says why a file is never given to an ability ("" when it may
// be): MetaRush's own folders (its home, cache, scratch and media), where
// apps and the system keep sign-ins, settings and devices, hidden files and
// folders (where credentials live: .ssh, .aws, .env…) other than MetaRush's
// models folder, and files named as MetaRush's credentials. p is the path
// as the file system stores it; places are compared the same way, without
// case on a Mac.
func notToGive(a *app.App, p string) string {
	home, _ := os.UserHomeDir()
	homeDir := fold(canonDir(home))
	var own []string
	for _, h := range sandbox.HiddenFor(a.Cfg) {
		// The person's home is in that list (as written and with its links
		// followed); MetaRush's own folders are the rest.
		if home != "" && fold(canonDir(h)) == homeDir {
			continue
		}
		own = append(own, h)
		if sameOrInside(p, h) {
			return "it is in MetaRush's own folders"
		}
	}
	if secretpath.IsCredentialFileName(filepath.Base(p)) {
		return "it is one of MetaRush's credentials"
	}
	places := append([]string{}, systemPlaces...)
	if home != "" {
		places = append(places, filepath.Join(home, "Library"))
	}
	for _, d := range places {
		if sameOrInside(p, d) {
			return "it is where apps and the system keep sign-ins, settings and devices"
		}
	}
	// The same folders under another name (a share mounted twice, a
	// folder linked in by the system): compared by device and inode, the
	// file's own folders against the refused ones.
	if why := sameFolderAs(p, own, places); why != "" {
		return why
	}
	if m := a.Tools.ModelsDir(); m != "" && sameOrInside(p, m) && !hiddenUnder(p, m) {
		return ""
	}
	if hiddenUnder(p, "/") {
		return "it is in a hidden file or folder, where keys and settings live"
	}
	return ""
}

// fileID is a file's device and inode.
type fileID struct{ dev, ino uint64 }

func idOf(p string) (fileID, bool) {
	st, err := os.Stat(p)
	if err != nil {
		return fileID{}, false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return fileID{}, false
	}
	return fileID{uint64(sys.Dev), uint64(sys.Ino)}, true
}

// sameFolderAs says why p lies inside one of the refused folders reached by
// another path ("" when it doesn't): any of p's folders that is, by device
// and inode, one of them.
func sameFolderAs(p string, own, places []string) string {
	refused := map[fileID]string{}
	for _, d := range own {
		if id, ok := idOf(d); ok {
			refused[id] = "it is in MetaRush's own folders"
		}
	}
	for _, d := range places {
		if id, ok := idOf(d); ok {
			if _, mine := refused[id]; !mine {
				refused[id] = "it is where apps and the system keep sign-ins, settings and devices"
			}
		}
	}
	for dir := filepath.Dir(p); ; dir = filepath.Dir(dir) {
		if id, ok := idOf(dir); ok {
			if why, hit := refused[id]; hit {
				return why
			}
		}
		if next := filepath.Dir(dir); next == dir {
			return ""
		}
	}
}

// hiddenUnder reports a part of p below dir that starts with a dot.
func hiddenUnder(p, dir string) bool {
	rel, err := filepath.Rel(canonDir(dir), p)
	if err != nil {
		rel = p
	}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.HasPrefix(part, ".") && part != "." && part != ".." {
			return true
		}
	}
	return false
}

// canonDir is a folder's path as the file system stores it (itself when it
// isn't there).
func canonDir(dir string) string {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	if f, err := os.Open(dir); err == nil {
		defer f.Close()
		if c := fdPath(f); c != "" {
			return c
		}
	}
	return dir
}

// fold drops case where volumes ignore it.
func fold(p string) string {
	if foldCase {
		return strings.ToLower(p)
	}
	return p
}

// sameOrInside reports p as dir or inside it, comparing both as the file
// system stores them, and without case where volumes ignore it.
func sameOrInside(p, dir string) bool {
	p, d := fold(p), fold(canonDir(dir))
	return p == d || inside(p, d)
}

// inside reports p inside dir (not dir itself).
func inside(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, "../") && !filepath.IsAbs(rel)
}

// keyExts are names key stores, keychains and password files go by.
var keyExts = map[string]bool{".p12": true, ".pfx": true, ".pem": true, ".key": true, ".keychain": true, ".keychain-db": true, ".jks": true, ".keystore": true,
	".kdbx": true, ".kdb": true, ".gpg": true, ".pgp": true, ".asc": true, ".ppk": true, ".ovpn": true, ".mobileprovision": true, ".cer": true, ".crt": true}

// keyNames are files apps keep sign-ins in.
var keyNames = map[string]bool{"id_rsa": true, "id_dsa": true, "id_ecdsa": true, "id_ed25519": true, "key3.db": true, "key4.db": true, "logins.json": true,
	"cookies": true, "cookies.sqlite": true, "login data": true, "web data": true, "credentials": true, "credentials.json": true, "token.json": true}

// keyStore says what key store a file looks like by its first bytes or its
// name ("" when none): PKCS#12, a keychain, an SQLite database (browsers'
// cookies and saved passwords), a private key in DER, or a key file's name.
func keyStore(head []byte, p string) string {
	base := strings.ToLower(filepath.Base(p))
	switch {
	case keyExts[strings.ToLower(filepath.Ext(base))]:
		return "its name says it is a key, a certificate or a keychain"
	case keyNames[base]:
		return "its name says it holds sign-ins or a key"
	case bytes.HasPrefix(head, []byte("kych")):
		return "it is a keychain"
	case bytes.HasPrefix(head, []byte("SQLite format 3\x00")):
		return "it is a database (apps keep cookies and saved passwords in these)"
	case derKey(head):
		return "it looks like a private key or a certificate store"
	}
	return ""
}

// derKey reports DER that starts like a PKCS#12 store (SEQUENCE, version 3)
// or a private key (PKCS#1, PKCS#8 or SEC 1: SEQUENCE, version 0 or 1).
func derKey(b []byte) bool {
	if len(b) < 8 || b[0] != 0x30 {
		return false
	}
	i := 2
	switch {
	case b[1] < 0x80:
	case b[1] == 0x81:
		i = 3
	case b[1] == 0x82:
		i = 4
	case b[1] == 0x83:
		i = 5
	default:
		return false
	}
	if len(b) < i+4 || b[i] != 0x02 || b[i+1] != 0x01 {
		return false
	}
	version, next := b[i+2], b[i+3]
	switch {
	case version == 3 && next == 0x30: // PKCS#12: version 3, then authSafe
		return true
	case version == 0 && (next == 0x30 || next == 0x02): // PKCS#8 / PKCS#1
		return true
	case version == 1 && next == 0x04: // SEC 1 EC private key
		return true
	}
	return false
}

// whisperHeader reads a whisper.cpp model's header: the magic "ggml", then
// n_vocab, n_audio_ctx, n_audio_state, n_audio_head, n_audio_layer,
// n_text_ctx, n_text_state, n_text_head, n_text_layer, n_mels, ftype.
type whisperHeader struct {
	Vocab, AudioState, TextState, TextLayers int32
}

func readWhisperHeader(b []byte) (whisperHeader, bool) {
	if len(b) < 48 || binary.LittleEndian.Uint32(b) != 0x67676d6c {
		return whisperHeader{}, false
	}
	v := func(i int) int32 { return int32(binary.LittleEndian.Uint32(b[4+4*i:])) }
	return whisperHeader{Vocab: v(0), AudioState: v(2), TextState: v(6), TextLayers: v(8)}, true
}

// English-only whisper models have this vocabulary; multilingual ones more.
const whisperEnglishVocab = 51864

// whisperSize names a model by its width.
func whisperSize(h whisperHeader) string {
	switch h.AudioState {
	case 384:
		return "tiny"
	case 512:
		return "base"
	case 768:
		return "small"
	case 1024:
		return "medium"
	case 1280:
		if h.TextLayers == 4 {
			return "large turbo"
		}
		return "large"
	}
	return "unusual"
}

// fileKind says what a file is by its first bytes: a kind MetaRush checks
// for and plain words for the person setting it up.
func fileKind(head []byte) (kind, words string) {
	if h, ok := readWhisperHeader(head); ok {
		langs := "multilingual"
		if h.Vocab == whisperEnglishVocab {
			langs = "English only"
		}
		return FileWhisperModel, fmt.Sprintf("a whisper.cpp model (%s, %s)", whisperSize(h), langs)
	}
	switch {
	case bytes.HasPrefix(head, []byte("GGUF")):
		return "", "a GGUF model"
	case bytes.HasPrefix(head, []byte("PK\x03\x04")):
		return "", "a ZIP archive"
	case bytes.HasPrefix(head, []byte("%PDF-")):
		return "", "a PDF"
	case len(head) > 0 && bytes.IndexByte(head, 0) < 0:
		return "", "a text file"
	}
	return "", "a file MetaRush doesn't recognise"
}

// kindProblem says why a file isn't the kind a manifest asks for ("" when
// it is, or it asks for none).
func kindProblem(nf models.AbilityNeededFile, kind string, head []byte) string {
	switch nf.Kind {
	case "":
		return ""
	case FileWhisperModel, FileWhisperTranslator:
		h, ok := readWhisperHeader(head)
		if !ok || kind != FileWhisperModel {
			return "it isn't a whisper.cpp model"
		}
		if nf.Kind == FileWhisperTranslator {
			switch {
			case h.Vocab == whisperEnglishVocab:
				return "it is an English-only model, which can't translate"
			case h.TextLayers == 4:
				return "it is a turbo model, which was not trained to translate and writes the language it hears"
			}
		}
	}
	return ""
}
