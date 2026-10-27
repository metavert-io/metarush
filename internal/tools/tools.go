// Package tools runs the only external programs MetaRush spawns: ffmpeg, ffprobe,
// node (for the pinned Remotion renderer), local transcribers, Claude Code (the
// dashboard assistant, run headless with a read-only allowlist) and yt-dlp for
// link imports (with Homebrew only to update it). Commands are allowlisted by
// name and built from typed arguments — never from a shell string and never from
// model-supplied text.
package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/secretpath"
)

// Tool names.
const (
	FFmpeg  = "ffmpeg"
	FFprobe = "ffprobe"
	Node    = "node"
	Whisper = "whisper-cli"
	// WhisperServer is whisper.cpp's server from the same build as whisper-cli
	// (found next to it, or set in config): the transcription model server runs
	// it to keep the whisper model loaded between clips (internal/transcribe).
	// It is never looked up on PATH, where it could come from another build.
	WhisperServer = "whisper-server"
	Sherpa        = "sherpa-onnx-offline"
	// SherpaDiar is sherpa-onnx's offline speaker diarization CLI.
	SherpaDiar = "sherpa-onnx-offline-speaker-diarization"
	// SherpaTTS is sherpa-onnx's speech program from a build with speech
	// (the transcriber's build has none), for offline voices (D280): a pinned
	// download in <home>/components/local-models, its SHA-256 checked before
	// each run, set with Use and never looked up on PATH.
	SherpaTTS = "sherpa-onnx-offline-tts"
	// Atlas is the MongoDB Atlas CLI (`machine invite/revoke --atlas` only).
	Atlas = "atlas"
	// Claude is Claude Code, spawned headless by the dashboard assistant.
	Claude = "claude"
	// OSAScript shows the macOS folder picker (one fixed script; see ChooseFolder).
	OSAScript = "osascript"
	// Open reveals a folder in Finder (macOS) ; XDGOpen does it on Linux.
	Open    = "open"
	XDGOpen = "xdg-open"
	// Say is macOS text to speech (free scratch narration; text is passed in a file, never argv).
	Say = "say"
	// EspeakNG is the Linux scratch-narration fallback (text in a file, never argv).
	EspeakNG = "espeak-ng"
	// YtDlp downloads videos from links (YouTube, TikTok, X). MetaRush builds its
	// arguments from typed options (internal/linkimport); the link itself is the
	// only free text and always follows "--".
	YtDlp = "yt-dlp"
	// Deno is the JavaScript runtime yt-dlp needs for YouTube's signature
	// challenges (yt-dlp spawns it; MetaRush only asks its version).
	Deno = "deno"
	// Brew is Homebrew, used only to update a Homebrew-installed yt-dlp
	// (`brew upgrade yt-dlp`) and to read its installed version.
	Brew = "brew"
	// Pdftoppm and Pdfinfo are poppler's (optional, `brew install poppler`):
	// they turn a PDF resource's pages into pictures (D79). Only a file path
	// MetaRush chose and numbers are passed.
	Pdftoppm = "pdftoppm"
	Pdfinfo  = "pdfinfo"
	// Sips is macOS's image tool: it converts HEIC photos to JPEG on import.
	Sips = "sips"
	// Vision is metarush-vision, MetaRush's own helper that looks at video frames
	// with Apple Vision on macOS (labels, animals, people, faces, on-screen text).
	// internal/vision builds its arguments: frame folders and fixed options only.
	Vision = "metarush-vision"
	// Swiftc compiles metarush-vision from the Swift source embedded in the
	// binary (Xcode Command Line Tools); internal/vision passes fixed arguments.
	Swiftc = "swiftc"
	// Launchctl (macOS) and Systemctl/Loginctl (Linux) install, start, stop
	// and inspect the background service (`metarush service`, internal/service).
	Launchctl = "launchctl"
	Systemctl = "systemctl"
	Loginctl  = "loginctl"
	// Git fetches a GitHub repository at one commit for the resources library
	// (D104): ls-remote, a shallow fetch into a fresh bare repository and raw
	// blob reads (ls-tree, cat-file), with the person's own credential helper
	// and hooks off. Nothing from the repository ever runs.
	Git = "git"
	// GH is the GitHub CLI: with the person's own sign-in it reads a
	// repository's description, languages, history and contributors (gh api,
	// read-only GET requests). MetaRush never reads or stores its token.
	GH = "gh"
	// D2 draws a diagram Claude described in D2 as SVG (optional; Mermaid
	// needs nothing extra). It runs offline: --bundle=false and a proxy that
	// refuses every request.
	D2 = "d2"
	// DeepFilter is DeepFilterNet's command-line tool (MIT/Apache-2.0), the
	// noise and echo remover of the clean voice pipeline: a pinned release
	// MetaRush downloads into <home>/components (internal/sound) and points at
	// with Use. It only ever gets a WAV file and an output folder.
	DeepFilter = "deep-filter"
	// StudioPython is the Python of the optional studio voice component (a
	// virtual environment in <home>/components, set with Use). It runs only
	// MetaRush's own runner script, with typed arguments.
	StudioPython = "metarush-studio-python"
	// NvidiaSMI reads the NVIDIA graphics cards and driver (Linux) when the
	// studio voice is set up, to pick its CUDA build: one fixed query
	// (internal/sound), never anything that changes the card.
	NvidiaSMI = "nvidia-smi"
	// Self is this MetaRush binary (os.Executable, never looked up on PATH): the
	// worker runs it as the transcription model server, which keeps the speech
	// model loaded between clips in a process of its own (internal/transcribe).
	Self = "metarush"
	// Stabilizer is an ffmpeg that has vid.stab (D153), used only to steady
	// shaky footage (`vidstabdetect`, `vidstabtransform`): the configured
	// ffmpeg when it has vid.stab, else a pinned build MetaRush downloaded into
	// <home>/components/stabilizer and checked by SHA-256 (internal/fixes,
	// set with Use). It never replaces the ffmpeg everything else uses, and it
	// only gets files MetaRush chose and MetaRush's own filter settings.
	Stabilizer = "metarush-stabilizer"
	// Tailscale is the Tailscale command-line tool (remote access, D128):
	// MetaRush reads `tailscale status --json` and `tailscale serve status
	// --json`; only a person at an interactive terminal (`metarush remote
	// enable|disable --apply`) makes it change anything.
	Tailscale = "tailscale"
	// Rclone is the person's own rclone (D220): MetaRush reads a Google Drive
	// folder through the Drive remote the person set up with rclone's own
	// sign-in (`rclone config`), without syncing it — `version` and
	// `listremotes --long` to see what is set up, `lsjson` to list a folder,
	// `backend copyid` to fetch one file by its Drive ID into a file MetaRush
	// chose. MetaRush never reads rclone's settings file or a token.
	Rclone = "rclone"
	// SandboxExec (macOS) and Bwrap (bubblewrap, Linux) keep the steps of a
	// Remotion source's refresh away from MetaRush's files and keys (D174):
	// MetaRush writes the profile or the mounts; the step is Node.js with
	// typed arguments. Found only in the system's own folders, never on PATH.
	SandboxExec = "sandbox-exec"
	Bwrap       = "bwrap"
	// Mongod is the database on this computer (D254): MongoDB Community
	// Server's mongod, downloaded by MetaRush from MongoDB's own download server
	// into <home>/database (pinned by version and SHA-256, internal/localdb) and
	// set with Use. It is never looked up on PATH, where it could be another
	// version or another person's server; it only gets MetaRush's own folders,
	// the 127.0.0.1 address and fixed options.
	Mongod = "mongod"
	// Lsof (macOS) says which program listens on the one port of this computer
	// an owner let an ability reach (D284): one fixed read-only query
	// (-nP -iTCP:<port> -sTCP:LISTEN -Fp), found only in the system's folders.
	Lsof = "lsof"
)

var allowed = map[string]bool{FFmpeg: true, FFprobe: true, Node: true, Whisper: true, WhisperServer: true, Sherpa: true, SherpaDiar: true, Atlas: true, Claude: true,
	OSAScript: true, Open: true, XDGOpen: true, Say: true, EspeakNG: true, YtDlp: true, Deno: true, Brew: true, Pdftoppm: true, Pdfinfo: true, Sips: true,
	Vision: true, Swiftc: true, Launchctl: true, Systemctl: true, Loginctl: true, Git: true, GH: true, D2: true, DeepFilter: true, StudioPython: true, NvidiaSMI: true,
	Self: true, Tailscale: true, Stabilizer: true, SandboxExec: true, Bwrap: true, Rclone: true, Mongod: true, Lsof: true, SherpaTTS: true}

// Allowed says whether name is one of the programs MetaRush may run.
func Allowed(name string) bool { return allowed[name] }

// systemOnly are tools found only at fixed system paths (never PATH or
// config): what keeps a refresh apart must be the system's own.
var systemOnly = map[string][]string{SandboxExec: {"/usr/bin/sandbox-exec"}, Bwrap: {"/usr/bin/bwrap", "/bin/bwrap"}, Lsof: {"/usr/sbin/lsof", "/usr/bin/lsof"}}

// macTailscale is where the Tailscale app for macOS keeps its command-line
// tool when it isn't on PATH (the app's own binary answers CLI commands).
const macTailscale = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"

// pathOnly are tools found only where MetaRush put them (Use), never on PATH.
// WhisperServer is found next to whisper-cli instead (New).
var pathOnly = map[string]bool{StudioPython: true, WhisperServer: true, Stabilizer: true, Mongod: true, SherpaTTS: true}

// chooseFolderScript is the only AppleScript MetaRush runs. The prompt and the
// starting folder arrive as argv (data), never as script text.
var chooseFolderScript = []string{
	"-e", "on run argv",
	"-e", "set p to item 1 of argv",
	"-e", "set d to item 2 of argv",
	"-e", "activate",
	"-e", "try",
	"-e", "if d is not \"\" then",
	"-e", "return POSIX path of (choose folder with prompt p default location (POSIX file d))",
	"-e", "else",
	"-e", "return POSIX path of (choose folder with prompt p)",
	"-e", "end if",
	"-e", "on error number -128",
	"-e", "return \"\"",
	"-e", "end try",
	"-e", "end run",
}

// ErrUnsupported means this platform has no native folder picker.
var ErrUnsupported = errors.New("not supported on this platform")

// ChooseFolder shows the native macOS folder picker on this machine's screen and
// returns the chosen folder ("" when the person cancels).
func (s *Set) ChooseFolder(ctx context.Context, prompt, startDir string) (string, error) {
	if runtime.GOOS != "darwin" || !s.Has(OSAScript) {
		return "", ErrUnsupported
	}
	args := append(append([]string{}, chooseFolderScript...), prompt, startDir)
	res, err := s.Run(ctx, Spec{Tool: OSAScript, Args: args})
	if err != nil {
		return "", err
	}
	return strings.TrimRight(strings.TrimSpace(string(res.Stdout)), "/"), nil
}

// OpenURL opens a web address in the person's browser on this computer (a
// platform's sign-in page, D305): https only, or http on this computer.
//
// The address never goes on the opener's command line (which other programs
// on this computer can see, and the activity log records): it is written to
// a page of its own in a private folder (0700, the page 0600) that sends the
// browser on, and that page is opened — on a Mac in the app that opens web
// addresses (asked of the system, Safari when it can't say), never whatever
// opens .html files. done removes the page (the caller calls it once the
// browser has been there; a minute later at the latest while this process
// runs). A sign-in's state and code challenge stay out of both.
func (s *Set) OpenURL(ctx context.Context, raw string) (done func(), err error) {
	u, err := url.Parse(raw)
	if err != nil || strings.HasPrefix(raw, "-") || u.User != nil ||
		!(u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) {
		return func() {}, fmt.Errorf("refusing to open %q", raw)
	}
	tool := ""
	switch {
	case runtime.GOOS == "darwin" && s.Has(Open):
		tool = Open
	case runtime.GOOS == "linux" && s.Has(XDGOpen):
		tool = XDGOpen
	default:
		return func() {}, ErrUnsupported
	}
	dir, err := os.MkdirTemp("", "metarush-open-")
	if err != nil {
		return func() {}, err
	}
	var once sync.Once
	done = func() { once.Do(func() { _ = os.RemoveAll(dir) }) }
	page := filepath.Join(dir, "open.html")
	esc := html.EscapeString(u.String())
	body := "<!doctype html><meta charset=\"utf-8\"><meta name=\"referrer\" content=\"no-referrer\"><meta http-equiv=\"refresh\" content=\"0;url=" + esc +
		"\"><title>MetaRush</title><p style=\"font:16px system-ui;margin:3em\">Opening… <a href=\"" + esc + "\">Continue</a></p>"
	if err := os.WriteFile(page, []byte(body), 0o600); err != nil {
		done()
		return done, err
	}
	time.AfterFunc(OpenPageKept, done)
	args := []string{page}
	if tool == Open {
		args = []string{"-a", s.webBrowser(ctx), page}
	}
	if _, err = s.Run(ctx, Spec{Tool: tool, Args: args}); err != nil {
		done()
	}
	return done, err
}

// webBrowser is the app that opens web addresses on this Mac (its path), as
// the system says; Safari when it can't say.
func (s *Set) webBrowser(ctx context.Context) string {
	if s.Has(OSAScript) {
		js := `ObjC.import("AppKit"); var u = $.NSWorkspace.sharedWorkspace.URLForApplicationToOpenURL($.NSURL.URLWithString("https://example.com")); u.isNil() ? "" : u.path.js`
		if res, err := s.Run(ctx, Spec{Tool: OSAScript, Args: []string{"-l", "JavaScript", "-e", js}}); err == nil {
			if p := strings.TrimSpace(string(res.Stdout)); strings.HasSuffix(p, ".app") && filepath.IsAbs(p) && !strings.HasPrefix(p, "-") {
				return p
			}
		}
	}
	return "Safari"
}

// OpenPageKept is how long the page OpenURL opens is kept for the browser to read.
var OpenPageKept = time.Minute

// RevealFolder opens a folder in Finder (macOS) or the desktop's file manager (Linux).
func (s *Set) RevealFolder(ctx context.Context, dir string) error {
	if strings.HasPrefix(dir, "-") || !filepath.IsAbs(dir) {
		return fmt.Errorf("refusing to open %q", dir)
	}
	switch {
	case runtime.GOOS == "darwin" && s.Has(Open):
		_, err := s.Run(ctx, Spec{Tool: Open, Args: []string{dir}})
		return err
	case runtime.GOOS == "linux" && s.Has(XDGOpen):
		_, err := s.Run(ctx, Spec{Tool: XDGOpen, Args: []string{dir}})
		return err
	}
	return ErrUnsupported
}

// Set resolves tool paths for an installation.
type Set struct {
	paths map[string]string
	cfg   config.Tools
	mu    sync.Mutex
	vers  map[string]string
	// OnRun, when set, is told about every finished run (the activity log),
	// except quiet ones such as version probes. It must be fast.
	OnRun func(ctx context.Context, info RunInfo)
}

// RunInfo describes one finished run of an allowlisted program.
type RunInfo struct {
	Tool     string
	Args     []string
	Duration time.Duration
	// ExitCode is the program's exit status (-1 when it could not start or was killed).
	ExitCode int
	Err      error // nil when the program succeeded
	Canceled bool  // the context ended (cancel, timeout)
	Stderr   []byte
}

type quietKey struct{}

// Quiet marks runs that are not reported to OnRun (probes such as `ffmpeg -version`).
func Quiet(ctx context.Context) context.Context { return context.WithValue(ctx, quietKey{}, true) }

func (s *Set) report(ctx context.Context, spec Spec, start time.Time, res *Result, err error) {
	if s.OnRun == nil || ctx.Value(quietKey{}) != nil {
		return
	}
	info := RunInfo{Tool: spec.Tool, Args: spec.Args, Duration: time.Since(start), Err: err, Canceled: ctx.Err() != nil}
	if res != nil {
		info.Stderr = res.Stderr
	}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		info.ExitCode = ee.ExitCode()
	default:
		info.ExitCode = -1
	}
	s.OnRun(ctx, info)
}

// CachedVersions returns the tool versions already probed (it never runs a program).
func (s *Set) CachedVersions() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for k, v := range s.vers {
		if !strings.HasPrefix(k, "enc:") {
			out[k] = v
		}
	}
	return out
}

// New resolves tools from config, then PATH, then well-known install locations.
func New(cfg config.Tools) *Set {
	s := &Set{paths: map[string]string{}, cfg: cfg, vers: map[string]string{}}
	explicit := map[string]string{FFmpeg: cfg.FFmpeg, FFprobe: cfg.FFprobe, Node: cfg.Node, Whisper: cfg.WhisperCLI, WhisperServer: cfg.WhisperServer, Claude: cfg.Claude,
		YtDlp: cfg.YtDlp, Deno: cfg.Deno, Brew: cfg.Brew, Vision: cfg.Vision, Launchctl: cfg.Launchctl, Systemctl: cfg.Systemctl, Git: cfg.Git, GH: cfg.GH, D2: cfg.D2,
		Tailscale: cfg.Tailscale, Rclone: cfg.Rclone}
	if cfg.SherpaDir != "" {
		explicit[Sherpa] = filepath.Join(cfg.SherpaDir, "bin", Sherpa)
		explicit[SherpaDiar] = filepath.Join(cfg.SherpaDir, "bin", SherpaDiar)
	}
	home, _ := os.UserHomeDir()
	extra := []string{"/opt/homebrew/bin", filepath.Join(home, ".local", "bin"), filepath.Join(home, ".local", "node-arm64", "bin"), "/usr/local/bin", "/usr/bin", filepath.Join(home, ".claude", "local"), linuxbrewBin}
	only := onlyDirs()
	if exe, err := os.Executable(); err == nil {
		// metarush-vision built next to the binary (./bootstrap.sh puts both in bin/).
		if only == nil {
			extra = append([]string{filepath.Dir(exe)}, extra...)
		}
		if fileExec(exe) {
			s.paths[Self] = exe // the running binary itself, never another metarush on PATH
		}
	}
	if only != nil {
		extra = only // an installation that must not see this computer's own programs (tests, browser runs)
	}
	for name := range allowed {
		if name == Self {
			continue
		}
		if fixed, ok := systemOnly[name]; ok {
			for _, p := range fixed {
				if fileExec(p) {
					s.paths[name] = p
					break
				}
			}
			continue
		}
		if p := explicit[name]; p != "" {
			if fileExec(p) {
				s.paths[name] = p
			}
			continue
		}
		if pathOnly[name] {
			continue
		}
		// On Apple Silicon prefer native Homebrew binaries over Intel ones that an
		// older /usr/local Homebrew may have put first on PATH (Rosetta is slower).
		if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" && only == nil {
			if p := filepath.Join("/opt/homebrew/bin", name); fileExec(p) {
				s.paths[name] = p
				continue
			}
		}
		if only == nil {
			if p, err := exec.LookPath(name); err == nil {
				s.paths[name] = p
				continue
			}
		}
		for _, d := range extra {
			if p := filepath.Join(d, name); fileExec(p) {
				s.paths[name] = p
				break
			}
		}
		if name == Tailscale && s.paths[name] == "" && runtime.GOOS == "darwin" && only == nil && fileExec(macTailscale) {
			s.paths[name] = macTailscale
		}
	}
	// whisper-server from the same build as whisper-cli: the one next to it.
	if s.paths[WhisperServer] == "" && cfg.WhisperServer == "" && s.paths[Whisper] != "" {
		if p := filepath.Join(filepath.Dir(s.paths[Whisper]), WhisperServer); fileExec(p) {
			s.paths[WhisperServer] = p
		}
	}
	return s
}

// linuxbrewBin is where Homebrew on Linux puts its programs.
const linuxbrewBin = LinuxbrewPrefix + "/bin"

// EnvToolDirs names the only folders (separated like PATH) where MetaRush
// looks for programs it isn't told the path of: not PATH, not Homebrew's or
// the usual folders. Tests and browser runs use it to play a computer that
// has only what they put there; the system's own sandbox and lsof are still
// found at their fixed places.
const EnvToolDirs = "METARUSH_TOOL_DIRS"

// onlyDirs is EnvToolDirs's folders (nil when it isn't set).
func onlyDirs() []string {
	v, ok := os.LookupEnv(EnvToolDirs)
	if !ok {
		return nil
	}
	out := []string{}
	for _, d := range filepath.SplitList(v) {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// SearchDirs are the folders MetaRush looks in for programs it isn't told
// the path of: EnvToolDirs's when set, else the running binary's folder,
// PATH's and the usual ones (Homebrew's, ~/.local/bin…). A program appearing
// in one of them changes the folder, so a running MetaRush can watch them.
func SearchDirs() []string {
	if only := onlyDirs(); only != nil {
		return only
	}
	home, _ := os.UserHomeDir()
	var out []string
	if exe, err := os.Executable(); err == nil {
		out = append(out, filepath.Dir(exe))
	}
	out = append(out, filepath.SplitList(os.Getenv("PATH"))...)
	out = append(out, "/opt/homebrew/bin", filepath.Join(home, ".local", "bin"), filepath.Join(home, ".local", "node-arm64", "bin"), "/usr/local/bin", "/usr/bin", filepath.Join(home, ".claude", "local"), linuxbrewBin)
	seen := map[string]bool{}
	dirs := out[:0]
	for _, d := range out {
		if d != "" && !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// Rescan looks again for the programs this set hasn't found, or whose
// program is gone, with cfg (config.yaml as it is now): a component added
// while MetaRush runs is used at once. Programs set with Use stay.
func (s *Set) Rescan(cfg config.Tools) {
	fresh := New(cfg)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
	for name := range allowed {
		old, now := s.paths[name], fresh.paths[name]
		keep := old != "" && fileExec(old)
		switch {
		case now != "" && now != old && (fresh.explicitIn(name) || !keep):
			// config names it now, or the old one is gone
			s.paths[name] = now
			delete(s.vers, name)
		case !keep && old != "":
			delete(s.paths, name)
			delete(s.vers, name)
		}
	}
}

// explicitIn reports whether config names this tool's path.
func (s *Set) explicitIn(name string) bool {
	c := s.cfg
	switch name {
	case FFmpeg:
		return c.FFmpeg != ""
	case FFprobe:
		return c.FFprobe != ""
	case Node:
		return c.Node != ""
	case Whisper:
		return c.WhisperCLI != ""
	case YtDlp:
		return c.YtDlp != ""
	case Deno:
		return c.Deno != ""
	case Sherpa, SherpaDiar:
		return c.SherpaDir != ""
	case Rclone:
		return c.Rclone != ""
	case D2:
		return c.D2 != ""
	case GH:
		return c.GH != ""
	case Git:
		return c.Git != ""
	case Claude:
		return c.Claude != ""
	case Brew:
		return c.Brew != ""
	}
	return false
}

func fileExec(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

// Path returns the resolved binary path or "".
func (s *Set) Path(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paths[name]
}

// Use points an allowlisted tool at a binary that exists now (for example
// metarush-vision right after it was built) and forgets its cached version.
func (s *Set) Use(name, path string) bool {
	if _, fixed := systemOnly[name]; fixed || !allowed[name] || !fileExec(path) {
		return false
	}
	s.mu.Lock()
	s.paths[name] = path
	delete(s.vers, name)
	s.mu.Unlock()
	return true
}

// Has reports whether a tool is available.
func (s *Set) Has(name string) bool { return s.Path(name) != "" }

// Config is the tool settings this set was resolved from. The renderer folder
// is not a tool path: internal/renderer resolves it (this version's own
// renderer, or a developer checkout).
func (s *Set) Config() config.Tools { return s.cfg }

// Explicit reports whether a tool's path came from config.yaml or a METARUSH_*
// override rather than PATH (the service refuses the real launchd/systemd in tests).
func (s *Set) Explicit(name string) bool {
	switch name {
	case Launchctl:
		return s.cfg.Launchctl != ""
	case Systemctl:
		return s.cfg.Systemctl != ""
	case Node:
		return s.cfg.Node != ""
	case Claude:
		return s.cfg.Claude != ""
	case Tailscale:
		return s.cfg.Tailscale != ""
	case Rclone:
		return s.cfg.Rclone != ""
	case Brew:
		return s.cfg.Brew != ""
	}
	return false
}

// ModelsDir is where local transcription models live.
func (s *Set) ModelsDir() string {
	if s.cfg.ModelsDir != "" {
		return s.cfg.ModelsDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "metarush-models")
}

// ErrMissing is returned when a tool is not installed.
type ErrMissing struct{ Name string }

// Error says which program is missing and what MetaRush needs it for, in plain
// words, with the one step that adds it when a component brings it (D326):
// "Rendering needs Node.js, which isn't on this computer yet (about 50 MB).
// Add it in Settings → Components."
func (e *ErrMissing) Error() string {
	if Advise != nil {
		if ae := Advise(e.Name); ae != nil {
			return ae.Message
		}
	}
	what := missingFor[e.Name]
	if what == "" {
		what = "for this"
	}
	return fmt.Sprintf("%s is not installed on this machine, and MetaRush needs it %s. Install it, then try again (MetaRush's checkup, doctor, says how).", e.Name, what)
}

// AppError is the missing program as MetaRush's error: E_TOOL_MISSING, with
// the command that adds it in Details when a component brings it.
func (e *ErrMissing) AppError() *apperr.Error {
	if Advise != nil {
		if ae := Advise(e.Name); ae != nil {
			return ae
		}
	}
	return apperr.New(apperr.ToolMissing, "%s", e.Error()).WithDetails(map[string]any{"tool": e.Name})
}

// Trusted, when set (internal/components), refuses a program MetaRush
// downloaded and wrote down by its checksum that was changed since (nil for
// every other program).
var Trusted func(path string) error

// Recorded tells internal/components that MetaRush itself changed a program
// it downloaded (yt-dlp updating itself through MetaRush), so the program's
// checksum is written down again. It does nothing until components sets it.
var Recorded = func(path string) {}

// Advise, when set (internal/components), says what adds a missing program:
// its component, in plain words, with the command behind Details; nil when
// no component brings it.
var Advise func(tool string) *apperr.Error

// MissingFor is what MetaRush needs a program for, in the person's terms
// ("to render videos"); "" for a program it has no words for.
func MissingFor(name string) string { return missingFor[name] }

// missingFor says what each program is for, in the person's terms.
var missingFor = map[string]string{
	FFmpeg: "to read and convert video and sound", FFprobe: "to read video and sound files", Node: "to render videos",
	Whisper: "to make transcripts", WhisperServer: "to keep the transcription model loaded between clips", Sherpa: "to make transcripts", SherpaTTS: "to speak with offline voices",
	SherpaDiar: "to tell voices apart", Atlas: "to set up database users",
	Claude: "for the Claude Code panel", OSAScript: "to show the folder picker", Open: "to open folders", XDGOpen: "to open folders",
	Say: "for the free scratch voice", EspeakNG: "for the free scratch voice", YtDlp: "to import videos from links", Deno: "to import videos from YouTube",
	Brew: "to update the link downloader", Pdftoppm: "to show the pages of a PDF", Pdfinfo: "to show the pages of a PDF", Sips: "to convert photos",
	Vision: "to see what's on screen", Swiftc: "to build its on-screen helper", Launchctl: "to run in the background", Systemctl: "to run in the background",
	Loginctl: "to run in the background", Git: "to bring in GitHub repositories", GH: "to read GitHub repository details", D2: "to draw D2 diagrams",
	DeepFilter: "to clean up voices", StudioPython: "for studio voice clean-up", NvidiaSMI: "to read the NVIDIA graphics card",
	Tailscale: "for opening MetaRush from your other computers", Stabilizer: "to steady shaky footage",
	Rclone: "to read Google Drive folders without syncing them",
	Mongod: "for the database on this computer",
}

// Result is a finished command.
type Result struct {
	Stdout []byte
	Stderr []byte
}

// Spec describes a command invocation.
type Spec struct {
	Tool   string
	Args   []string
	Dir    string
	Env    []string
	Stdout io.Writer // optional streaming stdout (otherwise captured)
	// Stderr also receives what the program writes to stderr (still captured,
	// bounded, for errors): a refresh keeps both streams in its log (D174).
	Stderr io.Writer
	Stdin  io.Reader         // optional stdin (e.g. secrets that must not appear in argv)
	OnLine func(line string) // optional per-line stdout callback
	// MaxLine caps one stdout line for OnLine (default 4 MB); longer lines are skipped.
	MaxLine int
	// CleanEnv runs the tool with exactly Env instead of this process's environment
	// plus Env (the assistant passes Claude Code only what it needs).
	CleanEnv bool
	// Background lowers priority so renders don't make the machine sluggish.
	Background bool
	// OnStart is called with the child's PID once it is running (the assistant
	// uses it to signal a long-lived Claude Code process).
	OnStart func(pid int)
	// SameGroup keeps the program in this process's group instead of a group
	// of its own, so whatever stops this process's group stops it too (the
	// transcription model server's whisper-server: it must never outlive the
	// model server). Canceling still kills it.
	SameGroup bool
	// Contained is for code MetaRush didn't write (abilities, Remotion
	// refreshes, D174, D232): the program starts in a session of its own (so
	// it has no controlling terminal: /dev/tty can't be opened, nothing can
	// be typed into the person's terminal) and, the moment it ends, everything
	// it left running in that session's group is killed, so nothing outlives
	// the run or its time limit. Its output still streams as it comes; OnLine
	// isn't available with it.
	Contained bool
}

// Run executes an allowlisted tool. Stderr is captured (bounded).
func (s *Set) Run(ctx context.Context, spec Spec) (res *Result, err error) {
	start := time.Now()
	defer func() { s.report(ctx, spec, start, res, err) }()
	if !allowed[spec.Tool] {
		return nil, fmt.Errorf("tool %q is not allowlisted", spec.Tool)
	}
	path := s.Path(spec.Tool)
	if path == "" {
		return nil, &ErrMissing{Name: spec.Tool}
	}
	if Trusted != nil {
		if err := Trusted(path); err != nil {
			return nil, err
		}
	}
	for _, a := range spec.Args {
		if strings.ContainsRune(a, 0) {
			return nil, errors.New("argument contains NUL")
		}
	}
	cmd := exec.CommandContext(ctx, path, spec.Args...)
	cmd.Dir = spec.Dir
	if spec.CleanEnv {
		cmd.Env = append([]string{}, spec.Env...)
	} else {
		cmd.Env = append(secretpath.Inheritable(os.Environ()), spec.Env...)
	}
	cmd.Stdin = spec.Stdin
	if spec.Contained {
		return s.runContained(ctx, spec, cmd)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: !spec.SameGroup}
	cmd.Cancel = func() error {
		// Kill the whole process group (node spawns Chrome; ffmpeg may fork),
		// or just the program when it shares this process's group.
		if cmd.Process != nil {
			if spec.SameGroup {
				_ = cmd.Process.Kill()
			} else {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		}
		return nil
	}
	stderr := &boundedBuffer{max: 256 << 10}
	cmd.Stderr = stderr
	if spec.Stderr != nil {
		cmd.Stderr = io.MultiWriter(stderr, spec.Stderr)
	}
	var stdout bytes.Buffer
	var pipe io.ReadCloser
	switch {
	case spec.OnLine != nil:
		pipe, err = cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
	case spec.Stdout != nil:
		cmd.Stdout = spec.Stdout
	default:
		cmd.Stdout = &stdout
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if spec.Background && runtime.GOOS != "windows" {
		if spec.SameGroup {
			_ = syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, 10)
		} else {
			_ = syscall.Setpriority(syscall.PRIO_PGRP, cmd.Process.Pid, 10)
		}
	}
	if spec.OnStart != nil {
		spec.OnStart(cmd.Process.Pid)
	}
	if pipe != nil {
		maxLine := spec.MaxLine
		if maxLine <= 0 {
			maxLine = 4 << 20
		}
		br := bufio.NewReaderSize(pipe, 64<<10)
		for {
			line, err := readLine(br, maxLine)
			if line != nil {
				spec.OnLine(string(line))
			}
			if err != nil {
				break
			}
		}
		_, _ = io.Copy(io.Discard, pipe) // never leave the child blocked on a full pipe
	}
	err = cmd.Wait()
	res = &Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if err != nil {
		return res, &ErrFailed{Tool: spec.Tool, Err: err, Stderr: tail(stderr.String(), 2000)}
	}
	return res, nil
}

// runContained runs a Contained program: a session of its own, real pipes
// for its output (so Wait returns when the program ends, not when the last
// child it left behind closes them), and its whole group killed as soon as it
// ends, is canceled or runs out of time.
func (s *Set) runContained(ctx context.Context, spec Spec, cmd *exec.Cmd) (*Result, error) {
	if spec.OnLine != nil || spec.SameGroup {
		return nil, errors.New("a contained program streams its output to writers and has a group of its own")
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	killGroup := func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}
	cmd.Cancel = func() error { killGroup(); return nil }
	// emptyGroup kills the group until none of it is left: one kill can miss
	// a process that is forking at that moment (its new child joins the group
	// after the signal went round, then holds the output open), so it is
	// sent again until the system says the group is gone.
	emptyGroup := func() {
		if cmd.Process == nil {
			return
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); errors.Is(err, syscall.ESRCH) || time.Now().After(deadline) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	stderr := &boundedBuffer{max: 256 << 10}
	var stdout bytes.Buffer
	var outDst io.Writer = &stdout
	if spec.Stdout != nil {
		outDst = spec.Stdout
	}
	var errDst io.Writer = stderr
	if spec.Stderr != nil {
		errDst = io.MultiWriter(stderr, spec.Stderr)
	}
	if err := cmd.Start(); err != nil {
		outR.Close()
		outW.Close()
		errR.Close()
		errW.Close()
		return nil, err
	}
	// Only the program holds the write ends now: the copies end when it and
	// everything it started are gone.
	outW.Close()
	errW.Close()
	if spec.Background && runtime.GOOS != "windows" {
		_ = syscall.Setpriority(syscall.PRIO_PGRP, cmd.Process.Pid, 10)
	}
	if spec.OnStart != nil {
		spec.OnStart(cmd.Process.Pid)
	}
	copied := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(outDst, outR); copied <- struct{}{} }()
	go func() { _, _ = io.Copy(errDst, errR); copied <- struct{}{} }()
	err = cmd.Wait()
	emptyGroup() // what it left running goes with it, all of it
	// The group is gone, so its pipes are closed and the copies end; only
	// something that left the group could still hold one: stop reading soon.
	grace := time.After(time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-copied:
		case <-grace:
			outR.Close()
			errR.Close()
			<-copied
		}
	}
	outR.Close()
	errR.Close()
	res := &Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if err != nil {
		return res, &ErrFailed{Tool: spec.Tool, Err: err, Stderr: tail(stderr.String(), 2000)}
	}
	return res, nil
}

// Started is a program Start left running on its own.
type Started struct {
	PID  int
	done chan struct{}
	err  error
}

// Wait waits for it to end (only while this process image is the one that
// started it: a process that re-executes itself reaps it another way).
func (st *Started) Wait() error {
	<-st.done
	return st.err
}

// Start runs an allowlisted tool in a session of its own that outlives the
// caller's requests and contexts and keeps going while this process
// re-executes itself (moving the media from the dashboard: the daemon restarts
// with the new storage while the move finishes). Stdin is the only input (a
// one-time token, say); stdout and stderr both go to spec.Stdout, which should
// be a file so the program never writes into a pipe this process stops reading.
func (s *Set) Start(spec Spec) (*Started, error) {
	start := time.Now()
	if !allowed[spec.Tool] {
		return nil, fmt.Errorf("tool %q is not allowlisted", spec.Tool)
	}
	path := s.Path(spec.Tool)
	if path == "" {
		return nil, &ErrMissing{Name: spec.Tool}
	}
	for _, a := range spec.Args {
		if strings.ContainsRune(a, 0) {
			return nil, errors.New("argument contains NUL")
		}
	}
	cmd := exec.Command(path, spec.Args...)
	cmd.Dir = spec.Dir
	if spec.CleanEnv {
		cmd.Env = append([]string{}, spec.Env...)
	} else {
		cmd.Env = append(secretpath.Inheritable(os.Environ()), spec.Env...)
	}
	cmd.Stdin = spec.Stdin
	cmd.Stdout, cmd.Stderr = spec.Stdout, spec.Stdout
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		s.report(context.Background(), spec, start, nil, err)
		return nil, err
	}
	st := &Started{PID: cmd.Process.Pid, done: make(chan struct{})}
	go func() {
		st.err = cmd.Wait()
		s.report(context.Background(), spec, start, nil, st.err)
		close(st.done)
	}()
	return st, nil
}

// readLine returns the next newline-terminated line without its newline. Lines
// longer than max are consumed and dropped (nil line, nil error) so one huge line
// cannot stall the stream.
func readLine(br *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	tooLong := false
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			if len(buf) > 0 && !tooLong {
				return buf, nil
			}
			return nil, err
		}
		if !tooLong {
			if len(buf)+len(chunk) > max {
				tooLong, buf = true, nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if !isPrefix {
			if tooLong {
				return nil, nil
			}
			if buf == nil {
				buf = []byte{}
			}
			return buf, nil
		}
	}
}

// ErrFailed is a non-zero exit.
type ErrFailed struct {
	Tool   string
	Err    error
	Stderr string
}

func (e *ErrFailed) Error() string { return fmt.Sprintf("%s failed: %v: %s", e.Tool, e.Err, e.Stderr) }

// Unwrap returns the underlying exit error.
func (e *ErrFailed) Unwrap() error { return e.Err }

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

type boundedBuffer struct {
	buf bytes.Buffer
	max int
	mu  sync.Mutex
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len()+len(p) > b.max {
		// keep the tail: errors are at the end
		data := append(b.buf.Bytes(), p...)
		if len(data) > b.max {
			data = data[len(data)-b.max:]
		}
		b.buf.Reset()
		b.buf.Write(data)
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *boundedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}
func (b *boundedBuffer) String() string { return string(b.Bytes()) }

var (
	verRe   = regexp.MustCompile(`(\d+\.\d+(\.\d+)?)`)
	ytVerRe = regexp.MustCompile(`^\d{4}\.\d{2}\.\d{2}(\.\d+)?$`)
)

// ForgetVersion drops a cached version (after updating a tool).
func (s *Set) ForgetVersion(name string) {
	s.mu.Lock()
	delete(s.vers, name)
	s.mu.Unlock()
}

// Version returns a tool's version string (cached).
func (s *Set) Version(ctx context.Context, name string) string {
	s.mu.Lock()
	if v, ok := s.vers[name]; ok {
		s.mu.Unlock()
		return v
	}
	s.mu.Unlock()
	if !s.Has(name) {
		return ""
	}
	arg := "-version"
	switch name {
	case Node, Whisper, YtDlp, Deno, Brew, Vision, Swiftc, DeepFilter, StudioPython, Mongod:
		arg = "--version"
	case Sherpa, SherpaDiar:
		arg = "--help"
	}
	res, _ := s.Run(Quiet(ctx), Spec{Tool: name, Args: []string{arg}})
	v := "installed"
	if res != nil {
		out := string(res.Stdout) + string(res.Stderr)
		switch name {
		case FFmpeg, FFprobe, Node, Deno, Brew, DeepFilter, StudioPython:
			if m := verRe.FindString(strings.SplitN(out, "\n", 2)[0]); m != "" {
				v = m
			}
		case Whisper:
			// "whisper.cpp version: 1.9.5" (older builds have no --version).
			if m := regexp.MustCompile(`^whisper\.cpp version: (\d+\.\d+\.\d+)`).FindStringSubmatch(strings.TrimSpace(string(res.Stdout))); m != nil {
				v = m[1]
			}
		case Vision:
			// "metarush-vision 1 macOS 26.5": the output protocol, then the system.
			if f := strings.Fields(strings.TrimSpace(string(res.Stdout))); len(f) >= 2 && f[0] == Vision {
				v = f[1]
			}
		case Swiftc:
			if m := regexp.MustCompile(`Swift version (\d+\.\d+(\.\d+)?)`).FindStringSubmatch(out); m != nil {
				v = m[1]
			}
		case Mongod:
			// "db version v8.0.34"
			if m := regexp.MustCompile(`db version v(\d+\.\d+\.\d+)`).FindStringSubmatch(out); m != nil {
				v = m[1]
			}
		case YtDlp:
			// yt-dlp versions are dates: 2026.08.19 (nightlies add .123456).
			if m := ytVerRe.FindString(strings.SplitN(strings.TrimSpace(string(res.Stdout)), "\n", 2)[0]); m != "" {
				v = m
			}
		}
	}
	s.mu.Lock()
	s.vers[name] = v
	s.mu.Unlock()
	return v
}

// HasEncoder reports whether ffmpeg has a given encoder (e.g. h264_videotoolbox).
func (s *Set) HasEncoder(ctx context.Context, enc string) bool {
	key := "enc:" + enc
	s.mu.Lock()
	if v, ok := s.vers[key]; ok {
		s.mu.Unlock()
		return v == "1"
	}
	s.mu.Unlock()
	res, err := s.Run(Quiet(ctx), Spec{Tool: FFmpeg, Args: []string{"-hide_banner", "-encoders"}})
	ok := err == nil && bytes.Contains(res.Stdout, []byte(" "+enc+" "))
	s.mu.Lock()
	if ok {
		s.vers[key] = "1"
	} else {
		s.vers[key] = "0"
	}
	s.mu.Unlock()
	return ok
}

// Arch reports the CPU architecture of a binary (arm64, x86_64, universal) — used
// by doctor to warn about Intel tools running under Rosetta on Apple Silicon.
func Arch(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 8)
	if _, err := io.ReadFull(f, head); err != nil {
		return ""
	}
	switch {
	case bytes.Equal(head[:4], []byte{0xca, 0xfe, 0xba, 0xbe}):
		return "universal"
	case bytes.Equal(head[:4], []byte{0xcf, 0xfa, 0xed, 0xfe}):
		cpu := uint32(head[4]) | uint32(head[5])<<8 | uint32(head[6])<<16 | uint32(head[7])<<24
		switch cpu {
		case 0x0100000c:
			return "arm64"
		case 0x01000007:
			return "x86_64"
		}
	case bytes.HasPrefix(head, []byte("#!")):
		return "script"
	case bytes.HasPrefix(head, []byte{0x7f, 'E', 'L', 'F'}):
		return "elf"
	}
	return "unknown"
}
