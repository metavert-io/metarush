package abilities

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/sandbox"
	"github.com/metavert-io/metarush/internal/tools"
)

// One run of a version in the sandbox (D232): a room of its own outside
// MetaRush's folders and the person's home (the ability's files, its inputs,
// an output folder, a home and a temporary folder), an environment built
// from nothing, the network the run allows, a time limit, and only the files
// it writes in its output folder taken back. The check's test run (network
// off, D233) and real runs (D234) both go through here.

// Environment variables an ability reads.
const (
	EnvInputs = "METARUSH_INPUTS" // the inputs as JSON (a file)
	EnvOutput = "METARUSH_OUTPUT" // the folder its outputs go in
	EnvTest   = "METARUSH_TEST"   // 1 in the check's run (the network is off)
	// EnvLocalURL is the address of the one service on this computer the
	// manifest names (D284): http://127.0.0.1:<port>.
	EnvLocalURL = "METARUSH_LOCAL_URL"
	// EnvInputPrefix + NAME carries each word, number, choice or yes/no
	// input; a footage, picture or sound input is the path of its file.
	EnvInputPrefix = "METARUSH_INPUT_"
	// ResultFile is what an ability may write next to its outputs to say
	// what each file is: {"files": [{"path", "title", "licence", "source",
	// "credit", "author"}]} (credit and author: D283).
	ResultFile = "result.json"
)

// roomSpec is what one run gets.
type roomSpec struct {
	Archive  *Archive
	Manifest *Manifest
	// Programs: each program it needs → its full path on this computer.
	Programs map[string]string
	Values   map[string]any    // words, numbers, choices, yes/no
	Files    map[string]string // file inputs → a local copy to put in the room
	// Needed are the files a person set up for it (D285): each copied into
	// the room, read-only, and checked against the SHA-256 pinned at setup.
	Needed []models.AbilityFilePin
	Net    string // sandbox.NetOff, NetProxy or NetOpen
	Proxy  *sandbox.Proxy
	// StandIns: each key's variable → the stand-in the code reads.
	StandIns map[string]string
	// Keys are the real keys the proxy adds: they must not come back in what
	// it printed, its outputs or result.json, in any written form.
	Keys     []string
	Test     bool
	Timeout  time.Duration
	MaxBytes int64
	// Self is MetaRush's own program (the forwarder on Linux).
	Self string
	// Isolation is this computer's sandbox (sandbox.Here).
	Isolation sandbox.Isolation
}

// roomOutput is one file the program wrote in its output folder.
type roomOutput struct {
	Rel     string
	Abs     string
	Kind    string
	Size    int64
	SHA256  string
	OK      bool   // a kind the ability declared
	Why     string // why it is left out
	Title   string // from result.json
	Licence string
	Source  string
	Credit  string // the credit line its licence asks for (D283)
	Author  string // who made it
	// Captions (D285): their language, and whether a machine translated them.
	Language   string
	Translated bool
	Lines      int
	Model      string
}

// roomResult is how the run went.
type roomResult struct {
	Root     string
	Take     string // MetaRush's copies of its outputs (outside the room)
	Exit     int
	Seconds  float64
	TimedOut bool
	Log      string
	Outputs  []roomOutput
	Started  bool
}

// Close removes the room.
func (r *roomResult) Close() {
	if r != nil && r.Root != "" {
		makeWritable(r.Root)
		_ = os.RemoveAll(r.Root)
	}
	if r != nil && r.Take != "" {
		_ = os.RemoveAll(r.Take)
	}
}

// makeWritable lets the room be removed (the ability's files are read-only).
func makeWritable(root string) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type()&fs.ModeSymlink == 0 {
			if d.IsDir() {
				_ = os.Chmod(p, 0o700)
			} else {
				_ = os.Chmod(p, 0o600)
			}
		}
		return nil
	})
}

// newRoom makes the room's folders in this computer's temporary folder
// (never under MetaRush's home or the person's home, which the sandbox hides).
func newRoom() (string, error) {
	root, err := os.MkdirTemp("", "metarush-ability-")
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	for _, d := range []string{"ability", "in", "files", "out", "home", "tmp", ".metarush"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			_ = os.RemoveAll(root)
			return "", err
		}
	}
	return root, nil
}

// envName is an input's variable: METARUSH_INPUT_WORDS.
func envName(input string) string { return EnvInputPrefix + strings.ToUpper(input) }

// runInRoom runs the version once. The caller closes the result (the room
// goes) after taking its outputs.
func runInRoom(ctx context.Context, a *app.App, s roomSpec) (*roomResult, error) {
	iso := s.Isolation
	if !iso.OK() {
		return nil, apperr.New(apperr.NotConfigured, "%s", iso.Problem).WithDetails(map[string]any{"fix": iso.Fix, "said": iso.Said})
	}
	root, err := newRoom()
	if err != nil {
		return nil, err
	}
	res := &roomResult{Root: root}
	fail := func(err error) (*roomResult, error) {
		res.Close()
		return nil, err
	}
	work := filepath.Join(root, "ability")
	if err := s.Archive.Extract(work); err != nil {
		return fail(err)
	}
	// Inputs: a file of them all, and a variable each.
	env := map[string]string{}
	inputs := map[string]any{}
	for k, v := range s.Values {
		inputs[k] = v
		switch x := v.(type) {
		case bool:
			env[envName(k)] = map[bool]string{true: "yes", false: "no"}[x]
		case float64:
			env[envName(k)] = num(x)
		default:
			env[envName(k)] = fmt.Sprint(x)
		}
	}
	names := make([]string, 0, len(s.Files))
	for k := range s.Files {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		src := s.Files[k]
		dst := filepath.Join(root, "in", k+strings.ToLower(filepath.Ext(src)))
		if err := copyFile(src, dst); err != nil {
			return fail(err)
		}
		_ = os.Chmod(dst, 0o444)
		inputs[k] = dst
		env[envName(k)] = dst
	}
	for _, fp := range s.Needed {
		dst := filepath.Join(root, "files", fp.Name+strings.ToLower(filepath.Ext(fp.Path)))
		if err := copyNeeded(fp, dst, s.Manifest.Name); err != nil {
			return fail(err)
		}
		env[FileEnv(fp.Name)] = dst
	}
	ib, _ := json.MarshalIndent(inputs, "", "  ")
	inFile := filepath.Join(root, "in", "inputs.json")
	if err := os.WriteFile(inFile, ib, 0o444); err != nil {
		return fail(err)
	}
	// The program and what it may read.
	name, local, args, err := s.Manifest.Program()
	if err != nil {
		return fail(apperr.New(apperr.InvalidInput, "%s", err.Error()))
	}
	program := ""
	if local {
		program = filepath.Join(work, filepath.FromSlash(name))
	} else if p := s.Programs[name]; p != "" {
		program = sandbox.RealProgram(p)
	} else {
		return fail(apperr.New(apperr.NotConfigured, "%s isn't set up on this computer.", name))
	}
	var read, pathFirst []string
	for _, p := range s.Manifest.Needs.Programs {
		if full := s.Programs[p]; full != "" {
			real := sandbox.RealProgram(full)
			read = append(read, sandbox.InstallationOf(real)...)
			pathFirst = append(pathFirst, filepath.Dir(real))
		}
	}
	base := sandbox.BaseEnv(pathFirst, filepath.Join(root, "home"), filepath.Join(root, "tmp"))
	extra := []string{EnvInputs + "=" + inFile, EnvOutput + "=" + filepath.Join(root, "out")}
	if l := s.Manifest.Local; l != nil {
		extra = append(extra, fmt.Sprintf("%s=http://127.0.0.1:%d", EnvLocalURL, l.Port))
	}
	if s.Test {
		extra = append(extra, EnvTest+"=1")
	}
	for k, v := range env {
		extra = append(extra, k+"="+v)
	}
	for k, v := range s.StandIns {
		extra = append(extra, k+"="+v)
	}
	room := &sandbox.Room{Root: root, Work: work, Read: sandbox.Dedupe(read), Hide: sandbox.HiddenFor(a.Cfg), Net: s.Net,
		// It writes only in its output, home and temporary folders: its own
		// files stay as checked, and nothing it leaves can make the room again.
		Write: []string{filepath.Join(root, "out"), filepath.Join(root, "home"), filepath.Join(root, "tmp")}}
	if s.Net != sandbox.NetOff {
		if s.Proxy == nil {
			return fail(errors.New("abilities: a run through MetaRush needs its proxy"))
		}
		ca, preload, pyDir, err := sandbox.WriteHelpers(filepath.Join(root, ".metarush"), s.Proxy.CAPEM())
		if err != nil {
			return fail(err)
		}
		extra = append(extra, sandbox.ProxyEnv(s.Proxy.URL(), ca, preload, pyDir)...)
		room.ProxyPort = s.Proxy.Port()
		if iso.Kind == sandbox.Linux {
			room.ProxySocket = filepath.Join(root, ".metarush", "proxy.sock")
			if err := s.Proxy.ListenUnix(room.ProxySocket); err != nil {
				return fail(err)
			}
			room.Self = s.Self
		}
	}
	tool, argv, err := room.Command(iso.Kind, program, args)
	if err != nil {
		return fail(err)
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tail := &tailBuffer{max: 256 << 10}
	start := time.Now()
	res.Started = true
	_, runErr := a.Tools.Run(rctx, tools.Spec{Tool: tool, Args: argv, Dir: work, Env: append(base, extra...), CleanEnv: true, Stdout: tail, Stderr: tail, Background: true, Contained: true})
	res.Seconds = time.Since(start).Seconds()
	forms := keyForms(s.Keys)
	res.Log = scrub(a.Log.Redactor().String(tail.String()), forms)
	if runErr != nil {
		res.Exit = exitCode(runErr)
		if errors.Is(rctx.Err(), context.DeadlineExceeded) {
			res.TimedOut = true
		} else if ctx.Err() != nil {
			res.Close()
			return nil, ctx.Err()
		}
	}
	res.Outputs, res.Take = takeOutputs(filepath.Join(root, "out"), "", s.Manifest, s.MaxBytes, forms)
	return res, nil
}

// exitCode reads a program's exit status (-1 when it never ran or was killed).
func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if st, ok := ee.Sys().(syscall.WaitStatus); ok && st.Signaled() {
			return 128 + int(st.Signal())
		}
		return ee.ExitCode()
	}
	return -1
}

// copyNeeded puts a set-up file into the room (an APFS clone when it can, a
// copy otherwise), read-only, and checks the copy is the file that was set up.
func copyNeeded(fp models.AbilityFilePin, dst, ability string) error {
	// The file as it is now (a model moved to another drive and linked back
	// is copied, never the link).
	changed := apperr.New(apperr.NotConfigured, "The %s file changed since it was set up; a person sets it up again.", fp.Name).WithCommand("metarush ability setup " + ability + " --file " + fp.Name + "=" + fp.Path)
	src, err := filepath.EvalSymlinks(fp.Path)
	if err != nil {
		return apperr.New(apperr.NotConfigured, "The %s file set up on this computer isn't at %s any more.", fp.Name, fp.Path).WithCommand("metarush ability setup " + ability + " --file " + fp.Name + "=<path>")
	}
	// Its size first: a file that grew is refused before anything is copied.
	if st, err := os.Stat(src); err != nil || !st.Mode().IsRegular() || st.Size() != fp.Size {
		return changed
	}
	if err := cloneFile(src, dst); err != nil {
		_ = os.Remove(dst)
		if err := copyAtMost(src, dst, fp.Size+1); err != nil {
			_ = os.Remove(dst)
			return apperr.New(apperr.NotConfigured, "MetaRush couldn't copy the %s file set up on this computer (%s).", fp.Name, fp.Path)
		}
	}
	if st, err := os.Lstat(dst); err != nil || !st.Mode().IsRegular() || st.Size() != fp.Size {
		_ = os.Remove(dst)
		return changed
	}
	_ = os.Chmod(dst, 0o444)
	sha, err := fileSHA(dst)
	if err != nil || sha != fp.SHA256 {
		return changed
	}
	return nil
}

// copyAtMost copies at most n bytes of src into a new file dst.
func copyAtMost(src, dst string, n int64) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(in, n)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
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

// audioExts are sound files (the classifier takes them in as clips too).
var audioExts = map[string]bool{".wav": true, ".mp3": true, ".m4a": true, ".aac": true, ".flac": true, ".aif": true, ".aiff": true, ".ogg": true, ".opus": true}

// kindOf says what MetaRush takes a file in as (by its bytes; "" when it
// isn't anything MetaRush takes in).
func kindOf(p string) string {
	if ext := strings.ToLower(filepath.Ext(p)); ext == ".srt" || ext == ".vtt" {
		if _, _, ok := captionLines(p); ok {
			return OutCaptions
		}
		return ""
	}
	sn, err := resources.SniffFile(p)
	if err != nil {
		return ""
	}
	switch sn.Route {
	case resources.RouteFootage:
		if audioExts[strings.ToLower(filepath.Ext(p))] {
			return OutSound
		}
		return OutFootage
	case resources.RouteResource:
		switch sn.Kind {
		case "image", "svg":
			return OutPicture
		case "pdf":
			return OutDocument
		case "font":
			return OutFont
		}
	}
	return ""
}

// languageRe is a language code a captions file may say (en, pt-BR).
var languageRe = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})?$`)

// maxCaptionsBytes caps a captions file a run brings back.
const maxCaptionsBytes = 2 << 20

var (
	srtTimeRe = regexp.MustCompile(`^\d{1,2}:\d{2}:\d{2}[,.]\d{3} --> \d{1,2}:\d{2}:\d{2}[,.]\d{3}`)
	vttTimeRe = regexp.MustCompile(`^(\d{1,2}:)?\d{2}:\d{2}\.\d{3} --> (\d{1,2}:)?\d{2}:\d{2}\.\d{3}`)
)

// captionLines reads a captions file (SRT, or WebVTT starting "WEBVTT"):
// UTF-8 text of at most 2 MB whose cues have times; it says how many cues.
func captionLines(p string) (lines int, vtt, ok bool) {
	b, err := os.ReadFile(p)
	if err != nil || len(b) == 0 || len(b) > maxCaptionsBytes || !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return 0, false, false
	}
	text := strings.ReplaceAll(strings.TrimPrefix(string(b), "\uFEFF"), "\r\n", "\n")
	vtt = strings.HasPrefix(text, "WEBVTT")
	if strings.ToLower(filepath.Ext(p)) == ".vtt" && !vtt {
		return 0, false, false
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if (vtt && vttTimeRe.MatchString(line)) || (!vtt && srtTimeRe.MatchString(line)) {
			lines++
		}
	}
	return lines, vtt, lines > 0
}

// resultEntry is what result.json may say about one output.
type resultEntry struct {
	Path    string `json:"path"`
	Title   string `json:"title"`
	Licence string `json:"licence"`
	License string `json:"license"`
	Source  string `json:"source"`
	Credit  string `json:"credit"`
	Author  string `json:"author"`
	// Language and Translated say a captions file's language and whether a
	// machine translated it (D285); Model what made it.
	Language   string `json:"language"`
	Translated bool   `json:"translated"`
	Model      string `json:"model"`
}

// collectOutputs reads the output folder: regular files only, each of a kind
// the ability declared, within the size limit; everything else is left out
// with the reason.
func collectOutputs(dir string, m *Manifest, max int64, forms []string) []roomOutput {
	out, _ := takeOutputs(dir, "", m, max, forms)
	return out
}

// takeOutputs reads the output folder through an os.Root (nothing outside it
// is reachable through a link or "..") and copies each file the program made
// — opened without following a link, checked to be a regular file — into
// take, a folder of MetaRush's outside the room ("" makes one). Every check
// after that (its kind, a key inside it, its fingerprint) and whatever takes
// it in reads the copy, never the room, which the program could still change.
// The caller removes take.
func takeOutputs(dir, take string, m *Manifest, max int64, forms []string) ([]roomOutput, string) {
	if max <= 0 {
		max = m.BytesLimit()
	}
	if take == "" {
		t, err := os.MkdirTemp("", "metarush-ability-take-")
		if err != nil {
			return nil, ""
		}
		take = t
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, take
	}
	defer root.Close()
	said := map[string]resultEntry{}
	if f, err := root.OpenFile(ResultFile, os.O_RDONLY|syscall.O_NOFOLLOW, 0); err == nil {
		b, _ := io.ReadAll(io.LimitReader(f, 1<<20))
		f.Close()
		var doc struct {
			Files []resultEntry `json:"files"`
		}
		if json.Unmarshal(b, &doc) == nil {
			for _, e := range doc.Files {
				said[path.Clean(strings.TrimPrefix(e.Path, "./"))] = e
			}
		}
	}
	var out []roomOutput
	var total int64
	_ = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil || rel == "." || rel == ResultFile {
			return nil
		}
		o := roomOutput{Rel: rel}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			o.Why = "it is a link, not a file the ability made"
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			o.Why = "it isn't a regular file"
		case len(out) >= 200:
			o.Why = "a run brings back at most 200 files"
		}
		if o.Why == "" {
			o.Abs, o.Size, o.SHA256, o.Why = copyOut(root, rel, take, len(out), max-total)
		}
		if o.Why == "" {
			o.Kind = kindOf(o.Abs)
			switch {
			case o.Kind == "":
				o.Why = "it isn't footage, sound, a picture, a PDF, a font or a captions file"
			case !contains(m.Outputs, o.Kind):
				o.Why = "the ability doesn't say it makes " + kindWords(o.Kind)
			case holdsKey(o.Abs, forms):
				o.Why = "it holds one of the keys MetaRush added for the run"
			default:
				o.OK = true
				total += o.Size
			}
		}
		if !o.OK && o.Abs != "" {
			_ = os.Remove(o.Abs)
			o.Abs = ""
		}
		if e, ok := said[rel]; ok {
			// One line each: they go into credit lines people paste (D283).
			o.Title, o.Source = clip(oneLine(scrub(e.Title, forms)), 200), webAddress(clip(oneLine(scrub(e.Source, forms)), 500))
			o.Licence = clip(oneLine(scrub(firstOf(e.Licence, e.License), forms)), 200)
			o.Credit, o.Author = clip(oneLine(scrub(e.Credit, forms)), 300), clip(oneLine(scrub(e.Author, forms)), 200)
			if languageRe.MatchString(e.Language) {
				o.Language = e.Language
			}
			o.Translated, o.Model = e.Translated, clip(oneLine(scrub(e.Model, forms)), 200)
		}
		out = append(out, o)
		return nil
	})
	return out, take
}

// copyOut copies one output into take: opened inside the root without
// following a link, a regular file, at most left bytes. It returns the copy,
// its size and SHA-256, or why it is left out.
func copyOut(root *os.Root, rel, take string, n int, left int64) (string, int64, string, string) {
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", 0, "", "it is a link or can't be read"
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return "", 0, "", "it isn't a regular file"
	}
	if st.Size() > left {
		return "", 0, "", "the run's files went past its size limit"
	}
	dst := filepath.Join(take, fmt.Sprintf("%03d-%s", n, filepath.Base(rel)))
	w, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, "", "MetaRush couldn't copy it"
	}
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(f, left+1))
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dst)
		return "", 0, "", "MetaRush couldn't copy it"
	}
	if size > left {
		_ = os.Remove(dst)
		return "", 0, "", "the run's files went past its size limit"
	}
	return dst, size, hex.EncodeToString(h.Sum(nil)), ""
}

func firstOf(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return strings.TrimSpace(x)
		}
	}
	return ""
}

// webAddress keeps a file's page only when it is an http(s) address.
func webAddress(s string) string {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.ContainsAny(s, " <>") {
		return ""
	}
	return s
}

// oneLine keeps a line of text on one line (a credit from a service).
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// kindWords says a kind of output in plain words.
func kindWords(k string) string {
	switch k {
	case OutFootage:
		return "footage"
	case OutSound:
		return "sound"
	case OutPicture:
		return "pictures"
	case OutDocument:
		return "PDFs"
	case OutFont:
		return "fonts"
	case OutCaptions:
		return "captions"
	}
	return k
}

func fileSHA(p string) (string, error) {
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

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu   sync.Mutex
	max  int
	buf  []byte
	lost int64
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if over := len(t.buf) - t.max; over > 0 {
		t.lost += int64(over)
		t.buf = append([]byte(nil), t.buf[over:]...)
	}
	return len(b), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := string(t.buf)
	if t.lost > 0 {
		s = fmt.Sprintf("[%d bytes before this were left out]\n", t.lost) + s
	}
	return s
}

// keyForms are every written form of the run's keys (sandbox.KeyForms).
func keyForms(keys []string) []string {
	var out []string
	for _, k := range keys {
		if k != "" {
			out = append(out, sandbox.KeyForms(k)...)
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

// scrub takes keys, in any written form, out of text MetaRush keeps.
func scrub(s string, forms []string) string {
	for _, f := range forms {
		s = strings.ReplaceAll(s, f, "[key hidden]")
	}
	return s
}

// holdsKey reports a file holding a key in any written form (read in
// overlapping blocks, so a key across two blocks is found too).
func holdsKey(p string, forms []string) bool {
	if len(forms) == 0 {
		return false
	}
	f, err := os.Open(p)
	if err != nil {
		return true // unreadable: never taken in
	}
	defer f.Close()
	longest := 0
	needles := make([][]byte, len(forms))
	for i, k := range forms {
		longest = max(longest, len(k))
		needles[i] = []byte(k)
	}
	buf := make([]byte, 0, 256<<10+longest)
	block := make([]byte, 256<<10)
	for {
		n, err := f.Read(block)
		buf = append(buf, block[:n]...)
		for _, k := range needles {
			if bytes.Contains(buf, k) {
				return true
			}
		}
		if err != nil {
			return false
		}
		if len(buf) > longest {
			buf = append(buf[:0], buf[len(buf)-longest:]...)
		}
	}
}

// thisOS is this computer's system as manifests say it.
func thisOS() string {
	switch runtime.GOOS {
	case "darwin":
		return OSMac
	case "linux":
		return OSLinux
	}
	return runtime.GOOS
}
