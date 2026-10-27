package sound

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/tools"
)

// Components are the downloads clearer voices need, kept per installation in
// <home>/components (like the renderer): the voice cleaner (DeepFilterNet's
// CLI) and the voice clarity score's model for clean mode, and the optional
// studio voice (Resemble Enhance in its own Python environment, with its
// model). Every download is pinned to a release and checked against its
// SHA-256 before it is used; nothing is fetched while voices are processed.

// Component names.
const (
	ComponentClean  = "clean"  // the voice cleaner + the clarity score (about 30 MB)
	ComponentStudio = "studio" // the studio voice (about 1.8 GB)
)

// DeepFilterNet's CLI, v0.5.6 (Rikorose/DeepFilterNet; MIT OR Apache-2.0).
const DeepFilterVersion = "0.5.6"

var deepFilterBuilds = map[string]struct{ asset, sha string }{
	"darwin/arm64": {"deep-filter-0.5.6-aarch64-apple-darwin", "4601e7f4e4c03e59a4c5b5000216ef3add3e808799cfccd95e14e83ea4611081"},
	"darwin/amd64": {"deep-filter-0.5.6-x86_64-apple-darwin", "d3be84003acb7c23e738ad7f70a158ec779a8d233a82e7fa3e717d112eb5b50f"},
	"linux/amd64":  {"deep-filter-0.5.6-x86_64-unknown-linux-musl", "70775e251eee44c0f2451a1e833326cf8bcbbe304d3e7cd12851e6fce72ef7da"},
	"linux/arm64":  {"deep-filter-0.5.6-aarch64-unknown-linux-gnu", "14e02a1c0028f3ca0bdf83b62b3336e56ba0556894ef295a95e8573f06557166"},
}

const deepFilterURL = "https://github.com/Rikorose/DeepFilterNet/releases/download/v0.5.6/"

// DNSMOS P.835's model (microsoft/DNS-Challenge at a pinned commit; the
// repository is CC BY 4.0, its code MIT).
const (
	dnsmosURL     = "https://raw.githubusercontent.com/microsoft/DNS-Challenge/591184a9fcb2cbdec02520fed81a32bbbf9d73ff/DNSMOS/DNSMOS/sig_bak_ovr.onnx"
	dnsmosSHA     = "269fbebdb513aa23cddfbb593542ecc540284a91849ac50516870e1ac78f6edd"
	DNSMOSVersion = "dnsmos-p835-591184a9"
)

// The studio voice: Resemble Enhance (resemble-ai/resemble-enhance, MIT) and
// its model (Hugging Face ResembleAI/resemble-enhance, MIT) at a pinned revision.
const (
	StudioPackage    = "resemble-enhance==0.0.2.dev240104122303"
	studioModelRev   = "4e3510ce4a8391159f665903544c5150bee7b2cb"
	studioModelURL   = "https://huggingface.co/ResembleAI/resemble-enhance/resolve/" + studioModelRev + "/"
	StudioVersion    = "resemble-enhance-0.0.2.dev240104122303+model-4e3510ce"
	studioRunnerName = "run.py"
)

// studioTorch is the torch and torchaudio version the studio voice runs with.
const studioTorch = "2.11.0"

// studioRequirements are the packages the studio voice runs with: matching
// torch/torchaudio wheels (on Linux from the build pickTorch chose, installed
// first), and what resemble_enhance imports. resemble-enhance itself goes in
// without its own (training-era) pins; deepspeed, a training dependency that
// does not install on macOS, is stood in for by the runner.
var studioRequirements = []string{
	"torch==" + studioTorch, "torchaudio==" + studioTorch, "numpy==2.5.4", "scipy==1.18.1", "soundfile==0.14.0", "omegaconf==2.4.0",
	"rich==15.0.0", "tqdm==4.70.1", "librosa==1.0.0", "matplotlib==3.11.2", "pandas==3.0.6", "tabulate==0.10.0",
}

// torchBuild is the torch the studio voice installs on this computer.
type torchBuild struct {
	Name   string // "pypi" (Apple silicon: Metal), "cu128", "cu126" or "cpu"
	Index  string // the pip index torch and torchaudio come from ("" = PyPI)
	Device string // where it is expected to run: mps, cuda or cpu
	// Note says, in plain words, why a graphics card won't be used ("" when
	// it will, or there is none).
	Note string
}

// torchIndex is PyTorch's own package index (CUDA and CPU-only builds).
const torchIndex = "https://download.pytorch.org/whl/"

// nvidiaGPU is an NVIDIA graphics card: its name, compute capability ("8.6";
// "" when unknown) and driver version ("550.54.14").
type nvidiaGPU struct {
	Name, Compute, Driver string
}

// errIntelMac: torch has had no builds for Intel Macs since 2.3.
var errIntelMac = errors.New("studio quality needs an Apple silicon Mac: the engine its voice model runs on has no build for Intel Macs")

// pickTorch chooses the torch build for studioTorch (2.11.0) on this computer:
//   - Apple silicon: PyPI's, which runs on the Apple GPU (Metal).
//   - Intel Macs: none (errIntelMac).
//   - Linux with an NVIDIA card and a driver of 525.60 or newer (what CUDA 12
//     needs): CUDA 12.8 for cards from Volta to Blackwell (compute capability
//     7.0 and up), CUDA 12.6 when a card is older (Maxwell, Pascal: 5.0–6.x).
//     PyPI's own Linux build needs CUDA 13 (driver 580 and Turing or newer),
//     so it is never used.
//   - Linux otherwise (no card, a driver too old, a card older than Maxwell):
//     the CPU build, which leaves out about 3 GB of CUDA libraries.
//
// The runner still checks the card when it starts and uses the CPU when the
// card can't run the model (studio_runner.py), so a wrong guess here is slow,
// never broken.
func pickTorch(goos, goarch string, gpus []nvidiaGPU) (torchBuild, error) {
	switch {
	case goos == "darwin" && goarch == "arm64":
		return torchBuild{Name: "pypi", Device: "mps"}, nil
	case goos == "darwin":
		return torchBuild{}, errIntelMac
	case goos != "linux":
		return torchBuild{}, fmt.Errorf("studio quality runs on Apple silicon Macs and Linux, not on %s", goos)
	}
	cpu := torchBuild{Name: "cpu", Index: torchIndex + "cpu", Device: "cpu"}
	if len(gpus) == 0 {
		return cpu, nil
	}
	driver := ""
	low := 99.0 // the oldest card's compute capability (99: unknown)
	for _, g := range gpus {
		if driver == "" {
			driver = g.Driver
		}
		if v, err := strconv.ParseFloat(g.Compute, 64); err == nil && v < low {
			low = v
		}
	}
	if !versionAtLeast(driver, 525, 60) {
		cpu.Note = fmt.Sprintf("The NVIDIA driver here (%s) is older than the graphics card code needs (525.60 or newer), so it runs on the processor. Update the driver, then set it up again.", orUnknown(driver))
		return cpu, nil
	}
	switch {
	case low < 5.0:
		cpu.Note = "This NVIDIA graphics card is older than the voice model's graphics code supports, so it runs on the processor."
		return cpu, nil
	case low < 7.0:
		return torchBuild{Name: "cu126", Index: torchIndex + "cu126", Device: "cuda"}, nil
	}
	return torchBuild{Name: "cu128", Index: torchIndex + "cu128", Device: "cuda"}, nil
}

// torchPins are the torch and torchaudio requirements of a build from
// PyTorch's index (their versions carry the build: 2.11.0+cu128).
func torchPins(b torchBuild) []string {
	return []string{"torch==" + studioTorch + "+" + b.Name, "torchaudio==" + studioTorch + "+" + b.Name}
}

func orUnknown(v string) string {
	if v == "" {
		return "version unknown"
	}
	return v
}

// versionAtLeast reports whether a dotted version is at least major.minor.
func versionAtLeast(v string, major, minor int) bool {
	f := strings.Split(v, ".")
	if len(f) == 0 {
		return false
	}
	ma, err := strconv.Atoi(f[0])
	if err != nil {
		return false
	}
	mi := 0
	if len(f) > 1 {
		mi, _ = strconv.Atoi(f[1])
	}
	return ma > major || (ma == major && mi >= minor)
}

// parseNvidiaSMI reads nvidia-smi's CSV lines (name, compute_cap,
// driver_version, or name, driver_version from an nvidia-smi without the
// compute_cap field).
func parseNvidiaSMI(out string) []nvidiaGPU {
	var gs []nvidiaGPU
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, ",")
		for i := range f {
			f[i] = strings.TrimSpace(f[i])
		}
		switch {
		case len(f) == 3 && f[0] != "" && versionAtLeast(f[2], 1, 0):
			g := nvidiaGPU{Name: f[0], Driver: f[2]}
			if _, err := strconv.ParseFloat(f[1], 64); err == nil {
				g.Compute = f[1]
			}
			gs = append(gs, g)
		case len(f) == 2 && f[0] != "" && versionAtLeast(f[1], 1, 0):
			gs = append(gs, nvidiaGPU{Name: f[0], Driver: f[1]})
		}
	}
	return gs
}

// nvidiaDriverVersion is the kernel module's version from
// /proc/driver/nvidia/version ("NVRM version: NVIDIA UNIX x86_64 Kernel
// Module  550.54.14  ..."), or "".
func nvidiaDriverVersion(text string) string {
	for _, l := range strings.Split(text, "\n") {
		if !strings.HasPrefix(l, "NVRM version:") {
			continue
		}
		for _, w := range strings.Fields(l) {
			if strings.Count(w, ".") >= 1 && versionAtLeast(w, 1, 0) && w[0] >= '0' && w[0] <= '9' {
				return w
			}
		}
	}
	return ""
}

// nvidiaProcFile is where Linux says which NVIDIA driver is loaded (tests
// point it elsewhere).
var nvidiaProcFile = "/proc/driver/nvidia/version"

// detectNVIDIA finds NVIDIA cards on Linux: nvidia-smi's fixed query when it
// is installed, else the driver's version file (the card itself unknown).
// nil: none (or not Linux).
func (c *Components) detectNVIDIA(ctx context.Context) []nvidiaGPU {
	if runtime.GOOS != "linux" {
		return nil
	}
	return c.nvidiaCards(ctx)
}

func (c *Components) nvidiaCards(ctx context.Context) []nvidiaGPU {
	if c.Tools.Has(tools.NvidiaSMI) {
		for _, q := range []string{"name,compute_cap,driver_version", "name,driver_version"} {
			res, err := c.Tools.Run(tools.Quiet(ctx), tools.Spec{Tool: tools.NvidiaSMI, Args: []string{"--query-gpu=" + q, "--format=csv,noheader"}})
			if err == nil {
				if gs := parseNvidiaSMI(string(res.Stdout)); len(gs) > 0 {
					return gs
				}
			}
		}
	}
	if b, err := os.ReadFile(nvidiaProcFile); err == nil {
		if v := nvidiaDriverVersion(string(b)); v != "" {
			return []nvidiaGPU{{Driver: v}}
		}
	}
	return nil
}

var studioModelFiles = []struct{ path, sha string }{
	{"enhancer_stage2/hparams.yaml", "80c3f15bc5a5b2cacf2c698699a0f6599d62911c0d53e1d6dee895c0d7cbaeac"},
	{"enhancer_stage2/ds/G/latest", "37a8eec1ce19687d132fe29051dca629d164e2c4958ba141d5f4133a33f0688f"},
	{"enhancer_stage2/ds/G/default/mp_rank_00_model_states.pt", "f9d035f318de3e6d919bc70cf7ad7d32b4fe92ec5cbe0b30029a27f5db07d9d6"},
}

//go:embed studio_runner.py
var studioRunner []byte

// Pythons the studio voice can use (torch has wheels for 3.10–3.14).
var studioPythons = []string{"python3.13", "python3.12", "python3.14"}

// Components finds and installs components for one installation home.
type Components struct {
	Home  string
	Tools *tools.Set
	// HTTP downloads (tests point it at a local server).
	HTTP *http.Client
	// BaseURLs overrides the download hosts (tests).
	BaseURLs map[string]string
}

func (c *Components) dir(name string) string { return filepath.Join(c.Home, "components", name) }

func (c *Components) cleanBinary() string {
	return filepath.Join(c.dir("voice-cleaner"), "deep-filter-"+DeepFilterVersion)
}
func (c *Components) scoreModel() string {
	return filepath.Join(c.dir("voice-score"), "sig_bak_ovr.onnx")
}
func (c *Components) studioDir() string { return c.dir("studio-voice") }
func (c *Components) studioPython() string {
	return filepath.Join(c.studioDir(), "venv", "bin", "python")
}

// ComponentStatus says whether a component is ready on this machine.
type ComponentStatus struct {
	Name      string `json:"name" jsonschema:"enum=clean,enum=studio"`
	Title     string `json:"title"`
	Installed bool   `json:"installed"`
	Size      string `json:"size" jsonschema:"description=About how much it downloads."`
	Version   string `json:"version,omitempty"`
	Problem   string `json:"problem,omitempty" jsonschema:"description=Why it cannot be used, in plain words."`
	// Studio: where it runs and how fast it was measured when it was set up.
	Device string  `json:"device,omitempty" jsonschema:"description=cuda (an NVIDIA graphics card)\\, mps (the Apple GPU) or cpu."`
	Speed  float64 `json:"speed,omitempty" jsonschema:"description=Seconds of work per second of audio\\, measured at setup."`
	// DeviceNote says why studio quality doesn't use the graphics card here.
	DeviceNote string `json:"deviceNote,omitempty" jsonschema:"description=Why studio quality doesn't use the graphics card here\\, in plain words."`
	// Details: where it lives (shown behind Details only).
	Path string `json:"path,omitempty"`
}

// studioManifest is <studio>/component.json, written once setup succeeded.
type studioManifest struct {
	Version    string    `json:"version"`
	Python     string    `json:"python"`
	Torch      string    `json:"torch"`
	Build      string    `json:"build,omitempty"` // torchBuild.Name
	Device     string    `json:"device"`
	Speed      float64   `json:"speed"` // seconds of work per second of audio
	MeasuredOn float64   `json:"measuredOn"`
	At         time.Time `json:"at"`
	// Note: why the graphics card isn't used (plain, from pickTorch);
	// Fallback: the runner's own reason when it used the CPU instead.
	Note     string `json:"note,omitempty"`
	Fallback string `json:"fallback,omitempty"`
}

// Status reports both components (and points the tool set at what is installed).
func (c *Components) Status() []ComponentStatus {
	return []ComponentStatus{c.cleanStatus(), c.studioStatus()}
}

func (c *Components) cleanStatus() ComponentStatus {
	st := ComponentStatus{Name: ComponentClean, Title: "Clean (on this computer)", Size: "about 30 MB", Version: "DeepFilterNet " + DeepFilterVersion, Path: c.dir("voice-cleaner")}
	if _, ok := deepFilterBuilds[runtime.GOOS+"/"+runtime.GOARCH]; !ok {
		st.Problem = "The voice cleaner has no build for this kind of computer."
		return st
	}
	bin := c.cleanBinary()
	if !executable(bin) {
		st.Problem = "Not set up yet."
		return st
	}
	_ = c.Tools.Use(tools.DeepFilter, bin)
	if _, err := os.Stat(c.scoreModel()); err != nil {
		st.Problem = "The voice clarity score is not set up yet."
		return st
	}
	st.Installed = true
	return st
}

func (c *Components) studioStatus() ComponentStatus {
	st := ComponentStatus{Name: ComponentStudio, Title: "Studio (on this computer, slower)", Size: "about 1.8 GB", Version: StudioVersion, Path: c.studioDir()}
	if runtime.GOOS == "darwin" && runtime.GOARCH != "arm64" {
		st.Problem = "Studio quality needs an Apple silicon Mac."
		return st
	}
	if runtime.GOOS == "linux" {
		st.Size = "about 1.8 GB, or 4.5 GB with NVIDIA graphics card support"
	}
	var man studioManifest
	b, err := os.ReadFile(filepath.Join(c.studioDir(), "component.json"))
	if err != nil || json.Unmarshal(b, &man) != nil || man.Version != StudioVersion {
		st.Problem = "Not set up yet."
		return st
	}
	if !executable(c.studioPython()) {
		st.Problem = "Its Python environment is missing; set it up again."
		return st
	}
	for _, f := range studioModelFiles {
		if _, err := os.Stat(filepath.Join(c.studioDir(), "model", f.path)); err != nil {
			st.Problem = "Its voice model is missing; set it up again."
			return st
		}
	}
	_ = c.Tools.Use(tools.StudioPython, c.studioPython())
	st.Installed, st.Device, st.Speed, st.DeviceNote = true, man.Device, man.Speed, man.Note
	return st
}

// Ready says whether a mode can run here, and why not (plain words).
func (c *Components) Ready(mode string) (bool, string) {
	switch mode {
	case "clean":
		st := c.cleanStatus()
		if !st.Installed {
			return false, "The voice cleaner is not set up on this computer yet (about 30 MB)."
		}
	case "studio":
		st := c.studioStatus()
		if !st.Installed {
			return false, "Studio quality is not set up on this computer yet. Setting it up is a one-time download of about 1.8 GB."
		}
	}
	return true, ""
}

// CleanAvailable says whether the voice cleaner has a build for this computer
// (clearer voices set it up on first use: it is small and pinned).
func (c *Components) CleanAvailable() bool {
	_, ok := deepFilterBuilds[runtime.GOOS+"/"+runtime.GOARCH]
	return ok
}

// CleanInstalled says whether the voice cleaner and the clarity score are here.
func (c *Components) CleanInstalled() bool { return c.cleanStatus().Installed }

// ScoreModel is the clarity score's model when it is installed ("" otherwise).
func (c *Components) ScoreModel() string {
	if _, err := os.Stat(c.scoreModel()); err != nil {
		return ""
	}
	return c.scoreModel()
}

// StudioDir is the studio component's folder (its model is in model/).
func (c *Components) StudioDir() string { return c.studioDir() }

// StudioRecorded is the studio version the last setup on this computer
// recorded ("" when studio was never set up here). It differs from
// StudioVersion after an upgrade that pins another version.
func (c *Components) StudioRecorded() string {
	var man studioManifest
	b, err := os.ReadFile(filepath.Join(c.studioDir(), "component.json"))
	if err != nil || json.Unmarshal(b, &man) != nil {
		return ""
	}
	return man.Version
}

func executable(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

// Progress reports setup steps (done of total, in plain words).
type Progress func(done, total int, msg string) error

// InstallClean downloads the voice cleaner and the clarity score's model.
func (c *Components) InstallClean(ctx context.Context, progress Progress) error {
	if choice.Declined(c.Home, "clean-voice") {
		// A person left it out or removed it (D325): never downloaded by itself.
		return choice.LeftOut("clean-voice", "Making voices clearer needs the voice cleaner (about 30 MB). Add it in Settings → Components.")
	}
	build, ok := deepFilterBuilds[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return errors.New("the voice cleaner has no build for this kind of computer")
	}
	if err := progress(0, 3, "Downloading the voice cleaner"); err != nil {
		return err
	}
	bin := c.cleanBinary()
	if err := c.download(ctx, c.base("deepfilter", deepFilterURL)+build.asset, bin, build.sha, 0o755, nil); err != nil {
		return fmt.Errorf("could not download the voice cleaner: %w", err)
	}
	if err := progress(1, 3, "Downloading the voice clarity score"); err != nil {
		return err
	}
	if err := c.download(ctx, c.base("dnsmos", dnsmosURL), c.scoreModel(), dnsmosSHA, 0o644, nil); err != nil {
		return fmt.Errorf("could not download the voice clarity score: %w", err)
	}
	if err := progress(2, 3, "Checking the voice cleaner"); err != nil {
		return err
	}
	if !c.Tools.Use(tools.DeepFilter, bin) {
		return fmt.Errorf("the voice cleaner can't be started on this computer (%s)", bin)
	}
	c.Tools.ForgetVersion(tools.DeepFilter)
	if v := c.Tools.Version(ctx, tools.DeepFilter); v != DeepFilterVersion {
		return fmt.Errorf("the voice cleaner does not start on this computer (version %q)", v)
	}
	return nil
}

// base is a download URL (or its test override).
func (c *Components) base(key, def string) string {
	if v, ok := c.BaseURLs[key]; ok {
		return v
	}
	return def
}

// download fetches url into dst through a temporary file, checking the
// SHA-256 (when given) before the file is moved into place.
func (c *Components) download(ctx context.Context, url, dst, sha string, mode os.FileMode, progress func(done, total int64)) error {
	if sha != "" {
		if got, err := fileSHA(dst); err == nil && got == sha {
			return os.Chmod(dst, mode)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Hour}
	}
	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("the download answered HTTP %d", res.StatusCode)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	var done int64
	buf := make([]byte, 1<<20)
	last := time.Now()
	for {
		n, rerr := res.Body.Read(buf)
		if n > 0 {
			if _, err := tmp.Write(buf[:n]); err != nil {
				tmp.Close()
				return err
			}
			h.Write(buf[:n])
			done += int64(n)
			if progress != nil && time.Since(last) > 500*time.Millisecond {
				progress(done, res.ContentLength)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			tmp.Close()
			return rerr
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); sha != "" && got != sha {
		return fmt.Errorf("the file did not match its published checksum (got %s…)", got[:12])
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
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

// findPython finds a Python the studio voice can use: Homebrew's first
// (python@3.13, 3.12, 3.14), then any on PATH.
func findPython() string {
	var dirs []string
	for _, prefix := range []string{"/opt/homebrew", "/usr/local", tools.LinuxbrewPrefix} {
		for _, v := range []string{"3.13", "3.12", "3.14"} {
			dirs = append(dirs, filepath.Join(prefix, "opt", "python@"+v, "bin", "python"+v))
		}
	}
	for _, p := range dirs {
		if executable(p) {
			return p
		}
	}
	for _, name := range studioPythons {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// InstallStudio sets up the studio voice: a Python environment with pinned
// packages (Homebrew's Python, installed with Homebrew when none is there), the
// model at a pinned revision, MetaRush's runner, and a measurement of how fast
// it runs here (on the Apple GPU when there is one).
func (c *Components) InstallStudio(ctx context.Context, progress Progress) (*ComponentStatus, error) {
	const steps = 6
	if err := progress(0, steps, "Finding Python"); err != nil {
		return nil, err
	}
	build, err := pickTorch(runtime.GOOS, runtime.GOARCH, c.detectNVIDIA(ctx))
	if err != nil {
		return nil, err
	}
	py := findPython()
	if py == "" {
		if !c.Tools.Has(tools.Brew) {
			return nil, errors.New("studio quality needs Python 3.12–3.14; install Homebrew's python@3.13 (brew install python@3.13) and try again")
		}
		if err := progress(0, steps, "Installing Python with Homebrew"); err != nil {
			return nil, err
		}
		// Only Python itself: no update of the package lists, nothing else upgraded.
		_, err := c.Tools.Run(ctx, tools.Spec{Tool: tools.Brew, Args: []string{"install", "python@3.13"},
			Env: []string{"HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_INSTALLED_DEPENDENTS_CHECK=1", "HOMEBREW_NO_ENV_HINTS=1"}})
		if err != nil {
			return nil, fmt.Errorf("Homebrew could not install Python: %w", err)
		}
		if py = findPython(); py == "" {
			return nil, errors.New("Python 3.13 was installed but MetaRush cannot find it")
		}
	}
	dir := c.studioDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	_ = os.Remove(filepath.Join(dir, "component.json")) // not ready until setup finishes
	if err := progress(1, steps, "Making its Python environment"); err != nil {
		return nil, err
	}
	if !executable(c.studioPython()) {
		if !c.Tools.Use(tools.StudioPython, py) {
			return nil, fmt.Errorf("%s cannot be started", py)
		}
		if _, err := c.Tools.Run(ctx, tools.Spec{Tool: tools.StudioPython, Args: []string{"-I", "-m", "venv", filepath.Join(dir, "venv")}}); err != nil {
			return nil, fmt.Errorf("could not make the Python environment: %w", err)
		}
	}
	if !c.Tools.Use(tools.StudioPython, c.studioPython()) {
		return nil, errors.New("the studio voice's Python environment cannot be started")
	}
	size := "about 1 GB"
	if build.Device == "cuda" {
		size = "about 4 GB with the graphics card's libraries"
	}
	if err := progress(2, steps, "Installing the studio voice ("+size+")"); err != nil {
		return nil, err
	}
	pipInstall := []string{"-I", "-m", "pip", "install", "--disable-pip-version-check", "--no-input", "--only-binary=:all:"}
	if build.Index != "" {
		// torch and torchaudio from the chosen build first (CUDA or CPU only),
		// named with the build (2.11.0+cu128), so a set-up-again replaces
		// another build; the rest then finds them installed (2.11.0+cu128 is
		// a 2.11.0).
		args := append(append(append([]string{}, pipInstall...), "--index-url", build.Index), torchPins(build)...)
		if _, err := c.Tools.Run(ctx, tools.Spec{Tool: tools.StudioPython, Args: args, Background: true}); err != nil {
			return nil, fmt.Errorf("could not install the studio voice's packages (%s build): %w", build.Name, err)
		}
	}
	pip := append(append([]string{}, pipInstall...), studioRequirements...)
	if _, err := c.Tools.Run(ctx, tools.Spec{Tool: tools.StudioPython, Args: pip, Background: true}); err != nil {
		return nil, fmt.Errorf("could not install the studio voice's packages: %w", err)
	}
	if _, err := c.Tools.Run(ctx, tools.Spec{Tool: tools.StudioPython, Args: []string{"-I", "-m", "pip", "install", "--disable-pip-version-check", "--no-input", "--no-deps", StudioPackage}}); err != nil {
		return nil, fmt.Errorf("could not install the studio voice: %w", err)
	}
	if err := progress(3, steps, "Downloading its voice model (about 710 MB)"); err != nil {
		return nil, err
	}
	for _, f := range studioModelFiles {
		var prog func(done, total int64)
		if strings.HasSuffix(f.path, ".pt") {
			prog = func(done, total int64) {
				if total > 0 {
					_ = progress(3, steps, fmt.Sprintf("Downloading its voice model (%d%%)", int(math.Round(100*float64(done)/float64(total)))))
				}
			}
		}
		if err := c.download(ctx, c.base("studio", studioModelURL)+f.path, filepath.Join(dir, "model", f.path), f.sha, 0o644, prog); err != nil {
			return nil, fmt.Errorf("could not download the studio voice model: %w", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, studioRunnerName), studioRunner, 0o644); err != nil {
		return nil, err
	}
	if err := progress(4, steps, "Checking how fast it runs here"); err != nil {
		return nil, err
	}
	man, err := c.measureStudio(ctx)
	if err != nil {
		return nil, err
	}
	man.Build, man.Note = build.Name, build.Note
	if build.Device == "cuda" && man.Device != "cuda" && man.Note == "" {
		man.Note = "The NVIDIA graphics card couldn't run the voice model, so it runs on the processor."
	}
	b, _ := json.MarshalIndent(man, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "component.json"), b, 0o644); err != nil {
		return nil, err
	}
	_ = progress(5, steps, "Studio quality is ready")
	st := c.studioStatus()
	return &st, nil
}

// measureStudio runs the studio voice on ten seconds of synthetic speech-like
// sound and records the device and speed.
func (c *Components) measureStudio(ctx context.Context) (*studioManifest, error) {
	dir, err := os.MkdirTemp("", "metarush-studio-check-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "in.wav")
	a := &Audio{Rate: 44100, Channels: 1, Data: testVoice(10, 44100)}
	if err := WriteWAV(in, a, true); err != nil {
		return nil, err
	}
	rs, err := runStudio(ctx, c.Tools, c.studioDir(), []studioJob{{In: in, Out: filepath.Join(dir, "out.wav")}}, nil, studioArgs...)
	if err != nil {
		return nil, err
	}
	res := rs[0]
	man := &studioManifest{Version: StudioVersion, Python: c.Tools.Version(ctx, tools.StudioPython), Torch: res.Torch, Device: res.Device,
		MeasuredOn: res.AudioSeconds, At: time.Now().UTC(), Fallback: res.Fallback}
	if res.AudioSeconds > 0 {
		man.Speed = math.Round(res.Seconds/res.AudioSeconds*100) / 100
	}
	return man, nil
}

// testVoice is a voice-like test signal: a buzzy tone with vowel-like
// formants, syllables and pauses, over light noise.
func testVoice(seconds float64, rate int) []float32 {
	n := int(seconds * float64(rate))
	x := make([]float32, n)
	seed := uint32(12345)
	for i := 0; i < n; i++ {
		t := float64(i) / float64(rate)
		syl := math.Max(0, math.Sin(2*math.Pi*3.2*t))
		if math.Mod(t, 2.5) > 1.9 {
			syl = 0
		}
		f0 := 140 + 20*math.Sin(2*math.Pi*0.5*t)
		v := 0.0
		for h := 1; h <= 12; h++ {
			fh := f0 * float64(h)
			amp := math.Exp(-math.Pow((fh-700)/400, 2)) + 0.6*math.Exp(-math.Pow((fh-1200)/500, 2)) + 0.1
			v += amp / float64(h) * math.Sin(2*math.Pi*fh*t)
		}
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		noise := float64(seed)/4294967296 - 0.5
		x[i] = float32(0.25*syl*v + 0.01*noise)
	}
	return x
}
