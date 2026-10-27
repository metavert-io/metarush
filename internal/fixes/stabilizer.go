package fixes

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/tools"
)

// Steadying uses vid.stab (D153): ffmpeg's vidstabdetect and vidstabtransform.
// The configured ffmpeg is used when it has them (Linux distributions build
// ffmpeg with vid.stab); otherwise MetaRush downloads a pinned static build
// into <home>/components/stabilizer, checks the archive and the program by
// SHA-256 and by running it, and points the stabilizer tool at it. The ffmpeg
// everything else uses is never changed. Without either, ffmpeg's basic
// deshake is used and the copy says so.

// stabBuild is one pinned download of a vid.stab-capable ffmpeg.
type stabBuild struct {
	URL    string // a zip with the program at its top
	ZipSHA string // SHA-256 of the zip as downloaded
	BinSHA string // SHA-256 of the program inside
	Entry  string // the program's name in the zip
	// Rosetta: an Intel build, used on Apple silicon only when Rosetta runs it.
	Rosetta bool
}

// stabBuilds are the pinned builds per platform, in the order tried. Checked
// 2026-10-20 (docs/sources.md): osxexperts' ffmpeg 9.0 for Apple silicon
// (the binary's SHA-256 is the one its page publishes) and evermeet's ffmpeg
// 9.0.2 for Intel Macs; both list vidstabdetect and vidstabtransform.
var stabBuilds = map[string][]stabBuild{
	"darwin/arm64": {
		{URL: "https://www.osxexperts.net/ffmpeg9arm.zip", ZipSHA: "d0c06c5c68ce48af3143b262f7a9118a7c9f67de1e237fcc24ffb14df9c67af9",
			BinSHA: "591260c945d0eef150e3bf82b0ef988bd36a9cecc18ff05d6679617159f0a95e", Entry: "ffmpeg"},
		{URL: "https://evermeet.cx/ffmpeg/ffmpeg-9.0.2.zip", ZipSHA: "4acc0be580f9b2788029eb7bd4d645ff87968911b0a62aeeb3940d42d54558d5",
			BinSHA: "a45b462cf91ed89148ae218c4577e30896485d7a6792c3673bcf5f823fa01b63", Entry: "ffmpeg", Rosetta: true},
	},
	"darwin/amd64": {
		{URL: "https://evermeet.cx/ffmpeg/ffmpeg-9.0.2.zip", ZipSHA: "4acc0be580f9b2788029eb7bd4d645ff87968911b0a62aeeb3940d42d54558d5",
			BinSHA: "a45b462cf91ed89148ae218c4577e30896485d7a6792c3673bcf5f823fa01b63", Entry: "ffmpeg"},
	},
}

// CanDownloadStabilizer reports whether MetaRush has a pinned build of the
// steadying tool for this kind of computer (components, D324).
func CanDownloadStabilizer() bool { return len(stabBuilds[runtime.GOOS+"/"+runtime.GOARCH]) > 0 }

// Stabilizer engines.
const (
	EngineVidStab = "vidstab"
	EngineDeshake = "deshake"
)

// Stabilizer finds or sets up the steadying tool on this machine.
type Stabilizer struct {
	Home  string
	Tools *tools.Set
	// Builds overrides the pinned builds (tests).
	Builds []stabBuild
	// Client downloads (default: a client with a 10-minute timeout).
	Client *http.Client
}

var stabMu sync.Mutex

func (s *Stabilizer) dir() string { return filepath.Join(s.Home, "components", "stabilizer") }

// binary is where the downloaded program lives.
func (s *Stabilizer) binary() string { return filepath.Join(s.dir(), "ffmpeg") }

func (s *Stabilizer) builds() []stabBuild {
	if s.Builds != nil {
		return s.Builds
	}
	return stabBuilds[runtime.GOOS+"/"+runtime.GOARCH]
}

// StabStatus says whether the good steadying is ready here.
type StabStatus struct {
	Ready bool `json:"ready"`
	// Engine is vidstab or deshake (the basic one); Source says where the
	// program came from: the configured ffmpeg, the downloaded build, or none.
	Engine string `json:"engine"`
	Source string `json:"source,omitempty" jsonschema:"enum=,enum=ffmpeg,enum=downloaded"`
	Path   string `json:"path,omitempty"`
	// Available: a pinned build exists for this machine (it can be downloaded).
	Available bool   `json:"available"`
	Text      string `json:"text"`
}

// hasVidStab reports whether an ffmpeg lists vid.stab's filters.
func hasVidStab(ctx context.Context, ts *tools.Set, tool string) bool {
	res, err := ts.Run(tools.Quiet(ctx), tools.Spec{Tool: tool, Args: []string{"-hide_banner", "-filters"}})
	if err != nil {
		return false
	}
	out := string(res.Stdout)
	return strings.Contains(out, "vidstabdetect") && strings.Contains(out, "vidstabtransform")
}

// Status looks for vid.stab without downloading anything, and points the
// stabilizer tool at what it finds.
func (s *Stabilizer) Status(ctx context.Context) StabStatus {
	stabMu.Lock()
	defer stabMu.Unlock()
	st := StabStatus{Engine: EngineDeshake, Available: len(s.builds()) > 0}
	if p := s.Tools.Path(tools.FFmpeg); p != "" && hasVidStab(ctx, s.Tools, tools.FFmpeg) {
		s.Tools.Use(tools.Stabilizer, p)
		st.Ready, st.Engine, st.Source, st.Path = true, EngineVidStab, "ffmpeg", p
		st.Text = "Steadying uses vid.stab in this machine's ffmpeg."
		return st
	}
	if bin := s.binary(); s.verified(bin) {
		if s.Tools.Use(tools.Stabilizer, bin) && hasVidStab(ctx, s.Tools, tools.Stabilizer) {
			st.Ready, st.Engine, st.Source, st.Path = true, EngineVidStab, "downloaded", bin
			st.Text = "Steadying uses vid.stab (set up by MetaRush)."
			return st
		}
	}
	if st.Available {
		st.Text = "The better steadying (vid.stab) is set up the first time a clip is steadied (about 25 MB)."
	} else {
		st.Text = "This machine steadies with ffmpeg's basic method; an ffmpeg with vid.stab steadies better."
	}
	return st
}

// verified reports whether the downloaded program is one of the pinned builds.
func (s *Stabilizer) verified(bin string) bool {
	sum, err := fileSHA(bin)
	if err != nil {
		return false
	}
	for _, b := range s.builds() {
		if sum == b.BinSHA {
			return true
		}
	}
	return false
}

// Ensure makes vid.stab ready, downloading a pinned build when needed. It
// returns the status afterwards; a download that fails leaves the basic
// steadying (the error says why, in words).
func (s *Stabilizer) Ensure(ctx context.Context, progress func(string)) (StabStatus, error) {
	st := s.Status(ctx)
	if st.Ready || !st.Available {
		return st, nil
	}
	if choice.Declined(s.Home, "stabilizer") {
		// A person left it out or removed it (D325): the basic steadying, never a download.
		return st, choice.LeftOut("stabilizer", "Steadying shaky footage well needs the steadying tool (about 25 MB). Add it in Settings → Components.")
	}
	var errs []string
	for _, b := range s.builds() {
		if b.Rosetta && !rosetta(ctx) {
			continue
		}
		if progress != nil {
			progress("setting up the better steadying")
		}
		if err := s.install(ctx, b); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if st = s.Status(ctx); st.Ready {
			return st, nil
		}
	}
	if len(errs) == 0 {
		return st, nil
	}
	return st, fmt.Errorf("the better steadying couldn't be set up: %s", strings.Join(errs, "; "))
}

// install downloads one build, checks it and puts the program in place.
func (s *Stabilizer) install(ctx context.Context, b stabBuild) error {
	if err := os.MkdirAll(s.dir(), 0o755); err != nil {
		return err
	}
	zipPath := filepath.Join(s.dir(), "download.zip")
	defer os.Remove(zipPath)
	// A copy MetaRush was given (tests, offline machines) instead of the network.
	if local := os.Getenv("METARUSH_STABILIZER_ZIP"); local != "" {
		if err := copyFile(local, zipPath); err != nil {
			return err
		}
	} else if err := s.download(ctx, b.URL, zipPath); err != nil {
		return err
	}
	if sum, err := fileSHA(zipPath); err != nil || sum != b.ZipSHA {
		return fmt.Errorf("the download from %s isn't the build MetaRush expects (its SHA-256 differs)", hostOf(b.URL))
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	tmp := s.binary() + ".part"
	found := false
	for _, f := range zr.File {
		if f.Name != b.Entry {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, io.LimitReader(rc, 400<<20))
		rc.Close()
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		found = true
		break
	}
	if !found {
		return fmt.Errorf("the download from %s has no program in it", hostOf(b.URL))
	}
	if sum, err := fileSHA(tmp); err != nil || sum != b.BinSHA {
		os.Remove(tmp)
		return fmt.Errorf("the program from %s isn't the build MetaRush expects (its SHA-256 differs)", hostOf(b.URL))
	}
	return os.Rename(tmp, s.binary())
}

func (s *Stabilizer) download(ctx context.Context, url, dst string) error {
	// Tests never reach the internet: only a local stand-in server.
	if testing.Testing() && !strings.HasPrefix(url, "http://127.0.0.1:") && !strings.HasPrefix(url, "http://localhost:") {
		return fmt.Errorf("downloads are off in tests (%s)", hostOf(url))
	}
	c := s.Client
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("couldn't reach %s", hostOf(url))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", hostOf(url), resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, io.LimitReader(resp.Body, 200<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func hostOf(url string) string {
	u := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	if i := strings.IndexByte(u, '/'); i > 0 {
		u = u[:i]
	}
	return u
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

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// rosetta reports whether this Mac runs Intel programs (an Intel build is
// only tried then).
var rosetta = func(ctx context.Context) bool {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return false
	}
	_, err := os.Stat("/Library/Apple/usr/libexec/oah/libRosettaRuntime")
	return err == nil
}

// ErrNoStabilizer means neither vid.stab nor deshake can run here.
var ErrNoStabilizer = errors.New("no ffmpeg on this machine can steady video")
