package c2pa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
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
	// Video is the file to sign (never changed); Out the signed copy to write
	// (it must not exist: c2patool writes it, nothing is overwritten).
	Video, Out string
	// Manifest is the manifest definition (Manifest.Definition).
	Manifest   []byte
	Credential *Credential
	// TimestampURL is a timestamp service ("" none).
	TimestampURL string
}

// Signed is a signed copy and how it checked out.
type Signed struct {
	SHA256 string
	Size   int64
	// ManifestHash is the SHA-256 of the manifest store in the copy.
	ManifestHash string
	// Report is c2patool's reading of the copy; Verified whether it checked
	// out, with Problem saying why not.
	Report   *Report
	Verified bool
	Problem  string
	// Signatures counts what MetaRush signed for c2patool (one).
	Signatures int
	// Timestamped: a timestamp service vouched for when it was signed.
	Timestamped bool
}

// Sign has c2patool write a signed copy of the video with the manifest, then
// reads the copy back. A copy MetaRush didn't sign, or that can't be read
// back, is removed and refused; one that doesn't check out is returned with
// Verified false (the caller doesn't deliver it).
func (t Tool) Sign(ctx context.Context, in SignIn) (*Signed, error) {
	if t.Tools == nil {
		return nil, &tools.ErrMissing{Name: tools.C2PATool}
	}
	if testing.Testing() && !t.Tools.Explicit(tools.C2PATool) {
		return nil, errRealInTest
	}
	if in.Credential == nil {
		return nil, errors.New("c2pa: no credential")
	}
	if _, err := os.Lstat(in.Out); err == nil {
		return nil, fmt.Errorf("c2pa: %s is already there", filepath.Base(in.Out))
	}
	sess, err := newSession(in.Credential, in.TimestampURL)
	if err != nil {
		return nil, err
	}
	defer sess.close()
	mf, sf := filepath.Join(sess.dir, "manifest.json"), filepath.Join(sess.dir, "settings.json")
	if err := os.WriteFile(mf, in.Manifest, 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(sf, settingsFile(), 0o600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, runLimit(in.Video))
	defer cancel()
	args := []string{in.Video, "--manifest", mf, "--output", in.Out, "--signer-path", sess.signerPath(), "--settings", sf}
	res, err := t.Tools.Run(ctx, tools.Spec{Tool: tools.C2PATool, Args: args, Env: append(cleanEnv(sess.dir), sess.env()...), CleanEnv: true, Dir: sess.dir})
	if err != nil {
		_ = os.Remove(in.Out)
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
		return nil, apperr.Wrap(apperr.Internal, err, "c2patool couldn't put Content Credentials into the video.").
			WithDetails(map[string]any{"c2patool": said})
	}
	out := &Signed{Signatures: sess.signatures()}
	if out.Signatures == 0 {
		// Never a copy signed with anything but this computer's certificate.
		_ = os.Remove(in.Out)
		return nil, apperr.New(apperr.Internal, "c2patool wrote Content Credentials without MetaRush signing them, so the copy was thrown away.")
	}
	if out.SHA256, out.Size, err = fileSHA(in.Out); err != nil {
		_ = os.Remove(in.Out)
		return nil, err
	}
	if out.ManifestHash, err = ManifestHash(in.Out); err != nil {
		out.Problem = "MetaRush can't find the credentials in the signed copy"
		return out, nil
	}
	rep, err := t.Read(ctx, in.Out)
	if err != nil {
		out.Problem = "c2patool couldn't read the signed copy back"
		return out, nil
	}
	out.Report = rep
	out.Verified, out.Problem = rep.Check(in.Credential)
	out.Timestamped = rep.Signer.Time != ""
	return out, nil
}

// Read has c2patool read a file's Content Credentials (Report.None: it has none).
func (t Tool) Read(ctx context.Context, path string) (*Report, error) {
	if t.Tools == nil {
		return nil, &tools.ErrMissing{Name: tools.C2PATool}
	}
	if testing.Testing() && !t.Tools.Explicit(tools.C2PATool) {
		return nil, errRealInTest
	}
	dir, err := privateDir()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	sf := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(sf, settingsFile(), 0o600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, runLimit(path))
	defer cancel()
	res, err := t.Tools.Run(ctx, tools.Spec{Tool: tools.C2PATool, Args: []string{path, "--settings", sf}, Env: cleanEnv(dir), CleanEnv: true, Dir: dir})
	if err != nil {
		if res != nil && strings.Contains(string(res.Stderr), "No claim found") {
			return &Report{None: true}, nil
		}
		said := ""
		if res != nil {
			said = lastLines(string(res.Stderr), 4)
		}
		return nil, apperr.Wrap(apperr.Internal, err, "c2patool couldn't read the video's Content Credentials.").WithDetails(map[string]any{"c2patool": said})
	}
	return ParseReport(res.Stdout)
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
// ("" nothing): http or https, a host, no sign-in or query in it.
func TimestampProblem(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	switch {
	case err != nil || u.Host == "":
		return "That isn't a web address (http://timestamp.example.com)."
	case u.Scheme != "http" && u.Scheme != "https":
		return "A timestamp service's address starts with http:// or https://."
	case u.User != nil:
		return "A timestamp service's address carries no sign-in."
	case u.RawQuery != "" || u.Fragment != "":
		return "A timestamp service's address has no ? or # part."
	}
	return ""
}
