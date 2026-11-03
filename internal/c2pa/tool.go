package c2pa

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/sandbox"
	"github.com/metavert-io/metarush/internal/tools"
)

// Tool runs c2patool for MetaRush (D416): typed arguments through
// internal/tools, a clean environment (so no C2PA_PRIVATE_KEY, C2PA_TA_URL or
// settings of the person's reach it, and its built-in test key is never
// used: it always has MetaRush as its signer), MetaRush's own settings file
// and a private folder as its home. Under go test the real c2patool is
// refused unless config names it (a stand-in, testutil/fakec2patool).
type Tool struct {
	Tools *tools.Set
	// Dir is where each signing and reading gets a private folder of its own:
	// the home's private state (0700), which sandboxed steps never see.
	Dir string
}

// NewTool is the c2patool runner of an installation.
func NewTool(cfg *config.Config, ts *tools.Set) Tool {
	return Tool{Tools: ts, Dir: PrivateDir(cfg)}
}

// PrivateDir is the home's private state folder, where signings work.
func PrivateDir(cfg *config.Config) string {
	return filepath.Join(cfg.Home(), "state", "private")
}

// MinVersion is the oldest c2patool MetaRush signs with: the first whose
// signer answers --signer-info, as MetaRush's does (0.28; 0.28.3 is pinned).
const MinVersion = "0.28.0"

var errRealInTest = errors.New("c2pa: the real c2patool isn't run under go test (point tools.c2patool at a stand-in)")

// Ready says whether c2patool can sign here ("" ready), in plain words.
func (t Tool) Ready(ctx context.Context) string {
	if t.Tools == nil || !t.Tools.Has(tools.C2PATool) {
		return "c2patool, which puts Content Credentials into videos, isn't on this computer"
	}
	if v := t.Tools.Version(ctx, tools.C2PATool); v != "" && v != "installed" && older(v, MinVersion) {
		return fmt.Sprintf("the c2patool on this computer is %s, older than MetaRush needs (%s or newer)", v, MinVersion)
	}
	return ""
}

// older reports a dotted version below another (numbers compared one by one).
func older(v, than string) bool {
	a, b := strings.Split(v, "."), strings.Split(than, ".")
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			_, _ = fmt.Sscan(a[i], &x)
		}
		if i < len(b) {
			_, _ = fmt.Sscan(b[i], &y)
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// settingsFile is c2pa-rs's settings MetaRush gives c2patool: no thumbnail
// in the manifest (a still of a client's video would leave with it), the
// ingredients created assertions like the actions that place them (C2PA
// wants them in the same list), no remote manifests or certificate status
// fetched, and the purposes a C2PA signing certificate may name
// (TrustConfig). No trust list: every signer is "unknown" to c2patool, which
// is not a failure (verifiers decide whom they trust).
func settingsFile() []byte {
	b, _ := json.MarshalIndent(map[string]any{
		"version": 1,
		"trust":   map[string]any{"trust_config": TrustConfig},
		"builder": map[string]any{"thumbnail": map[string]any{"enabled": false}, "created_assertion_labels": []string{"c2pa.ingredient"}},
		"verify":  map[string]any{"remote_manifest_fetch": false, "ocsp_fetch": false},
	}, "", "  ")
	return b
}

// SignIn is one signing.
type SignIn struct {
	// Video is the file to sign (never changed).
	Video string
	// Manifest is the manifest definition (Manifest.Definition).
	Manifest   []byte
	Credential *Credential
	// TimestampURL is a timestamp service ("" none).
	TimestampURL string
}

// Signed is a signed copy and how it checked out.
type Signed struct {
	// Path is the signed copy, in the signing's private folder: the caller
	// places it (and checks what it placed), then calls Remove.
	Path string
	dir  string
	// SHA256 and Size are the signed copy's.
	SHA256 string
	Size   int64
	// ManifestHash is the SHA-256 of the manifest store in the copy.
	ManifestHash string
	// Report is c2patool's reading of the copy; Verified whether it checked
	// out, with Problem saying why not.
	Report   *Report
	Verified bool
	Problem  string
	// Signatures counts what MetaRush signed for c2patool (one), and the
	// copy carries exactly that signature.
	Signatures int
	// Timestamped: a timestamp service vouched for when it was signed.
	Timestamped bool
}

// Remove throws away the signing's private folder (the signed copy with it).
func (s *Signed) Remove() {
	if s != nil && s.dir != "" {
		_ = os.RemoveAll(s.dir)
	}
}

// ErrNotOurs refuses a signed copy whose signature isn't the one MetaRush
// made in that signing.
var ErrNotOurs = errors.New("c2pa: the signed copy's signature isn't MetaRush's")

// Sign has c2patool write a signed copy of the video with the manifest into
// a private folder, then checks that the copy carries exactly the signature
// MetaRush made and reads it back. A copy MetaRush didn't sign, or one that
// carries another signature, can't be read back or asked MetaRush for more
// than its one signature, is thrown away and refused; one that doesn't
// check out is returned with Verified false (the caller doesn't deliver it).
func (t Tool) Sign(ctx context.Context, in SignIn) (out *Signed, err error) {
	if t.Tools == nil {
		return nil, &tools.ErrMissing{Name: tools.C2PATool}
	}
	if testing.Testing() && !t.Tools.Explicit(tools.C2PATool) {
		return nil, errRealInTest
	}
	if in.Credential == nil {
		return nil, errors.New("c2pa: no credential")
	}
	sess, err := newSession(t.Dir, in.Credential, in.TimestampURL)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		sess.close()
		if !keep {
			_ = os.RemoveAll(sess.dir)
		}
	}()
	// c2patool tells a video's format by its name: the render is shown to it
	// as video.mp4 (a link in the private folder; it only reads it).
	video, err := nameAsVideo(sess.dir, in.Video, ".mp4")
	if err != nil {
		return nil, err
	}
	mf, sf, dst := filepath.Join(sess.dir, "manifest.json"), filepath.Join(sess.dir, "settings.json"), filepath.Join(sess.dir, "signed.mp4")
	if err := os.WriteFile(mf, in.Manifest, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(sf, settingsFile(), 0o600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, runLimit(in.Video))
	defer cancel()
	args := []string{video, "--manifest", mf, "--output", dst, "--signer-path", sess.signer, "--settings", sf}
	res, err := t.Tools.Run(ctx, tools.Spec{Tool: tools.C2PATool, Args: args, Env: cleanEnv(sess.dir), CleanEnv: true, Dir: sess.dir,
		ExtraFiles: []*os.File{sess.peer}, OnStart: func(int) { sess.started() }})
	sess.close() // nothing more is signed in this signing
	if err != nil {
		var miss *tools.ErrMissing
		if errors.As(err, &miss) {
			return nil, err
		}
		said := ""
		if res != nil {
			said = lastLines(string(res.Stderr), 4)
		}
		if why := sess.refused(); why != "" {
			said = strings.TrimSpace(said + " (MetaRush: " + why + ")")
		}
		return nil, apperr.Wrap(apperr.ToolFailed, err, "c2patool couldn't put Content Credentials into the video.").
			WithDetails(map[string]any{"c2patool": said})
	}
	ours := sess.returned()
	if len(ours) == 0 {
		// Never a copy signed with anything but this computer's certificate.
		return nil, apperr.New(apperr.Internal, "c2patool wrote Content Credentials without MetaRush signing them, so the copy was thrown away.")
	}
	if why := sess.refused(); why != "" {
		return nil, apperr.New(apperr.Internal, "c2patool asked MetaRush for more than one signing needs, so the copy was thrown away.").
			WithDetails(map[string]any{"refused": why})
	}
	out = &Signed{Path: dst, dir: sess.dir, Signatures: len(ours)}
	if out.SHA256, out.Size, err = fileSHA(dst); err != nil {
		return nil, err
	}
	store, err := manifestStore(dst)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush can't find the credentials in the signed copy, so it was thrown away.")
	}
	sum := sha256.Sum256(store)
	out.ManifestHash = hex.EncodeToString(sum[:])
	// The copy carries exactly the signature MetaRush made, nothing else.
	inFile, err := StoreSignatures(store)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush can't read the signature in the signed copy, so it was thrown away.")
	}
	if !sameSignatures(inFile, ours) {
		return nil, apperr.Wrap(apperr.Internal, ErrNotOurs, "The signed copy carries a signature MetaRush didn't make for it, so it was thrown away.").
			WithDetails(map[string]any{"inFile": len(inFile), "made": len(ours)})
	}
	rep, err := t.Read(ctx, dst)
	if err != nil {
		out.Problem = "c2patool couldn't read the signed copy back"
		keep = true
		return out, nil
	}
	out.Report = rep
	out.Verified, out.Problem = rep.Check(in.Credential)
	out.Timestamped = rep.Signer.Time != ""
	keep = true
	return out, nil
}

// sameSignatures: the signatures in a file are exactly those MetaRush made.
func sameSignatures(inFile, ours [][]byte) bool {
	if len(inFile) != len(ours) {
		return false
	}
	for i := range inFile {
		if !bytes.Equal(inFile[i], ours[i]) {
			return false
		}
	}
	return true
}

// Read has c2patool read a file's Content Credentials (Report.None: it has none).
func (t Tool) Read(ctx context.Context, path string) (*Report, error) {
	if t.Tools == nil {
		return nil, &tools.ErrMissing{Name: tools.C2PATool}
	}
	if testing.Testing() && !t.Tools.Explicit(tools.C2PATool) {
		return nil, errRealInTest
	}
	dir, err := privateDir(t.Dir)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	sf := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(sf, settingsFile(), 0o600); err != nil {
		return nil, err
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" || len(ext) > 6 {
		ext = ".mp4"
	}
	video, err := nameAsVideo(dir, path, ext)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, runLimit(path))
	defer cancel()
	res, err := t.Tools.Run(ctx, tools.Spec{Tool: tools.C2PATool, Args: []string{video, "--settings", sf}, Env: cleanEnv(dir), CleanEnv: true, Dir: dir})
	if err != nil {
		if res != nil && strings.Contains(string(res.Stderr), "No claim found") {
			return &Report{None: true}, nil
		}
		said := ""
		if res != nil {
			said = lastLines(string(res.Stderr), 4)
		}
		return nil, apperr.Wrap(apperr.ToolFailed, err, "c2patool couldn't read the video's Content Credentials.").WithDetails(map[string]any{"c2patool": said})
	}
	return ParseReport(res.Stdout)
}

// nameAsVideo links a file into a private folder under a name with the
// extension c2patool reads its format from.
func nameAsVideo(dir, path, ext string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "video"+ext)
	if err := os.Symlink(abs, p); err != nil {
		return "", err
	}
	return p, nil
}

// cleanEnv is all c2patool gets of an environment: a home and temporary
// folder of its own (so it never reads the person's c2pa settings) and the
// system's programs.
func cleanEnv(dir string) []string {
	return []string{"HOME=" + dir, "TMPDIR=" + dir, "XDG_CONFIG_HOME=" + dir, "PATH=/usr/bin:/bin", "LANG=C"}
}

// runLimit is how long c2patool may take with a file: two minutes, and a
// minute more per gigabyte (it hashes the whole video).
func runLimit(path string) time.Duration {
	d := 2 * time.Minute
	if st, err := os.Stat(path); err == nil {
		d += time.Duration(st.Size()>>30) * time.Minute
	}
	return d
}

func fileSHA(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, " "))
}

// TimestampProblem says what is wrong with a timestamp service's address
// ("" nothing): https, a named host (not an address, not this computer or a
// local network's name), no sign-in or query in it (D438). Where the name
// leads is checked too, before each signing (the caller's).
func TimestampProblem(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	switch {
	case err != nil || u.Host == "":
		return "That isn't a web address (https://timestamp.example.com)."
	case u.Scheme != "https":
		return "A timestamp service's address starts with https://."
	case u.User != nil:
		return "A timestamp service's address carries no sign-in."
	case u.RawQuery != "" || u.Fragment != "":
		return "A timestamp service's address has no ? or # part."
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	switch {
	case net.ParseIP(host) != nil:
		return "A timestamp service is named by its host name, not an address."
	case host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") ||
		strings.HasSuffix(host, ".lan") || strings.HasSuffix(host, ".home.arpa") || !strings.Contains(host, "."):
		return "A timestamp service is on the internet, not this computer or a local network."
	}
	return ""
}

// LookupHost resolves a timestamp service's name (a test puts its own here).
var LookupHost = func(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

// TimestampHostProblem says, in plain words, why MetaRush won't ask a
// timestamp service ("" it may): its address isn't one TimestampProblem
// takes, or its name doesn't lead only to the internet — never this
// computer or a local network, as the abilities' proxy refuses (D438).
// Asked when the address is set and again before each signing.
func TimestampHostProblem(ctx context.Context, raw string) string {
	if why := TimestampProblem(raw); why != "" {
		return why
	}
	u, _ := url.Parse(strings.TrimSpace(raw))
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := LookupHost(ctx, u.Hostname())
	if err != nil || len(addrs) == 0 {
		return "MetaRush can't find the timestamp service " + u.Hostname() + " (its name leads nowhere just now)."
	}
	for _, a := range addrs {
		if !sandbox.PublicIP(a.IP) {
			return "The timestamp service " + u.Hostname() + " leads to this computer or a local network, so MetaRush won't ask it."
		}
	}
	return ""
}
