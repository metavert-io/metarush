// Package service runs an installation's daemon as a per-user background
// service (D76), for unattended workers such as a Mac mini: a LaunchAgent on
// macOS (~/Library/LaunchAgents/<label>.plist: started at login, restarted
// when it crashes) or a systemd user unit on Linux
// (~/.config/systemd/user/<name>.service, Restart=on-failure). The service
// runs `metarush --home <home> serve`, or `serve --worker-only` (no
// dashboard), and logs to <home>/logs/daemon.log.
//
// launchctl and systemctl are run through internal/tools (allowlisted, typed
// arguments). Under `go test` the real ones are refused: tests point
// tools.launchctl / tools.systemctl at fakes and use a temporary home folder.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/tools"
)

// Platforms.
const (
	Launchd = "launchd"
	Systemd = "systemd"
)

// Options describe one installation's service.
type Options struct {
	Home string // the installation home the service runs
	// Exe is the metarush binary the service runs (default: this binary, at a
	// path that survives upgrades; see StableExecutable).
	Exe        string
	WorkerOnly bool // `serve --worker-only`: the worker and background jobs, no dashboard
	// OS is darwin or linux (default runtime.GOOS); UserHome is the person's
	// home folder (default os.UserHomeDir) and UID their user ID (default
	// os.Getuid). Tests set all three.
	OS       string
	UserHome string
	UID      int
	User     string // login name, for loginctl (default the current user)
	Tools    *tools.Set
	// Path is the PATH the service runs with (default: this binary's folder,
	// the folders of the tools MetaRush found, and the usual system folders).
	Path string
}

// Status is the service as the system sees it.
type Status struct {
	Platform  string `json:"platform" jsonschema:"enum=launchd,enum=systemd"`
	Label     string `json:"label"` // the launchd label or the systemd unit
	File      string `json:"file"`  // its plist or unit file
	Installed bool   `json:"installed"`
	Loaded    bool   `json:"loaded"` // known to launchd/systemd (enabled)
	Running   bool   `json:"running"`
	PID       int    `json:"pid,omitempty"`
	LastExit  *int   `json:"lastExit,omitempty"`
	// WorkerOnly says the service runs without the dashboard.
	WorkerOnly bool   `json:"workerOnly"`
	Binary     string `json:"binary,omitempty"` // the metarush the service runs
	SameBinary bool   `json:"sameBinary"`       // ... is the binary asking
	Home       string `json:"home"`
	Log        string `json:"log"`
	// Linger (systemd) says whether it also runs while nobody is logged in.
	Linger  *bool  `json:"linger,omitempty"`
	Problem string `json:"problem,omitempty"`
	Fix     string `json:"fix,omitempty"`
}

// Plan is what install writes and runs (shown by --dry-run).
type Plan struct {
	File     string     `json:"file"`
	Content  string     `json:"content"`
	Commands [][]string `json:"commands"`
}

func (o Options) goos() string {
	if o.OS != "" {
		return o.OS
	}
	return runtime.GOOS
}

func (o Options) platform() (string, error) {
	switch o.goos() {
	case "darwin":
		return Launchd, nil
	case "linux":
		return Systemd, nil
	}
	return "", fmt.Errorf("MetaRush can run in the background on macOS and Linux only, not on %s", o.goos())
}

func (o Options) userHome() string {
	if o.UserHome != "" {
		return o.UserHome
	}
	h, _ := os.UserHomeDir()
	return h
}

func (o Options) uid() int {
	if o.UID != 0 {
		return o.UID
	}
	return os.Getuid()
}

// Label is the service's name: "metarush.daemon" (launchd) or "metarush"
// (systemd) for the default home ~/.metarush, with a short hash of the home
// for any other, so several homes can each have one.
func (o Options) Label() string {
	suffix := ""
	if filepath.Clean(o.Home) != filepath.Join(o.userHome(), ".metarush") {
		sum := sha256.Sum256([]byte(filepath.Clean(o.Home)))
		suffix = hex.EncodeToString(sum[:4])
	}
	if p, _ := o.platform(); p == Systemd {
		if suffix != "" {
			return "metarush-" + suffix + ".service"
		}
		return "metarush.service"
	}
	if suffix != "" {
		return "metarush.daemon." + suffix
	}
	return "metarush.daemon"
}

// File is the plist or unit file path.
func (o Options) File() string {
	if p, _ := o.platform(); p == Systemd {
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" || o.UserHome != "" {
			base = filepath.Join(o.userHome(), ".config")
		}
		return filepath.Join(base, "systemd", "user", o.Label())
	}
	return filepath.Join(o.userHome(), "Library", "LaunchAgents", o.Label()+".plist")
}

// LogFile is where the service's output goes.
func (o Options) LogFile() string { return filepath.Join(o.Home, "logs", "daemon.log") }

func (o Options) args() []string {
	args := []string{o.Exe, "--home", o.Home, "serve"}
	if o.WorkerOnly {
		args = append(args, "--worker-only")
	}
	return args
}

// servicePath is the PATH the service runs with: launchd and systemd start
// services with a minimal one, and MetaRush's children (npm, yt-dlp, Claude
// Code) find their own tools on it.
func (o Options) servicePath() string {
	if o.Path != "" {
		return o.Path
	}
	var dirs []string
	add := func(d string) {
		if d != "" && !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	add(filepath.Dir(o.Exe))
	if o.Tools != nil {
		for _, t := range []string{tools.Node, tools.FFmpeg, tools.Claude, tools.YtDlp, tools.Deno, tools.Whisper, tools.Sherpa} {
			if p := o.Tools.Path(t); p != "" {
				add(filepath.Dir(p))
			}
		}
	}
	h := o.userHome()
	for _, d := range []string{"/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin", filepath.Join(h, ".local", "bin"), "/usr/bin", "/bin", "/usr/sbin", "/sbin"} {
		add(d)
	}
	return strings.Join(dirs, ":")
}

/* ---------- what install writes ---------- */

// Plist is the LaunchAgent: run at login, restarted when it exits with an
// error (a crash), not when it was stopped on purpose (exit 0).
func (o Options) Plist() string {
	var b strings.Builder
	esc := func(s string) string {
		var w strings.Builder
		_ = xml.EscapeText(&w, []byte(s))
		return w.String()
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Written by ` + "`metarush service install`" + `; ` + "`metarush service uninstall`" + ` removes it. -->
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + esc(o.Label()) + `</string>
	<key>ProgramArguments</key>
	<array>
`)
	for _, a := range o.args() {
		b.WriteString("\t\t<string>" + esc(a) + "</string>\n")
	}
	b.WriteString(`	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>` + esc(o.servicePath()) + `</string>
		<key>METARUSH_SERVICE</key>
		<string>launchd</string>
	</dict>
	<key>WorkingDirectory</key>
	<string>` + esc(o.Home) + `</string>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>StandardOutPath</key>
	<string>` + esc(o.LogFile()) + `</string>
	<key>StandardErrorPath</key>
	<string>` + esc(o.LogFile()) + `</string>
</dict>
</plist>
`)
	return b.String()
}

// Unit is the systemd user unit (Restart=on-failure: a crash restarts it, a
// stop does not).
func (o Options) Unit() string {
	q := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		return `"` + strings.ReplaceAll(s, "%", "%%") + `"`
	}
	var exec []string
	for _, a := range o.args() {
		exec = append(exec, q(a))
	}
	var reapArgs []string
	for _, a := range []string{o.Exe, "--home", o.Home, "service", "reap"} {
		reapArgs = append(reapArgs, q(a))
	}
	mode := "dashboard and worker"
	if o.WorkerOnly {
		mode = "worker only"
	}
	return "# Written by `metarush service install`; `metarush service uninstall` removes it.\n" +
		"[Unit]\nDescription=MetaRush daemon (" + mode + ") for " + strings.ReplaceAll(o.Home, "%", "%%") + "\n" +
		"Wants=network-online.target\nAfter=network-online.target\n\n" +
		"[Service]\nType=simple\nExecStart=" + strings.Join(exec, " ") + "\n" +
		"WorkingDirectory=" + q(o.Home) + "\n" +
		"Environment=" + q("PATH="+o.servicePath()) + " " + q("METARUSH_SERVICE=systemd") + "\n" +
		"Restart=on-failure\nRestartSec=10\n" +
		// Stopping or restarting the service stops MetaRush only: the database on this
		// computer (D254) runs in the same group and is stopped cleanly by `metarush stop`,
		// never with the service (systemd would otherwise signal every process in it).
		"KillMode=process\n" +
		// …and what a crashed daemon left behind (renders, Claude Code) is stopped
		// after it, the database server excepted (`metarush service reap`).
		// "-": a clean-up that finds nothing to do (cgroup v1) never marks a stop failed.
		"ExecStopPost=-" + strings.Join(reapArgs, " ") + "\n" +
		"StandardOutput=append:" + strings.ReplaceAll(o.LogFile(), "%", "%%") + "\n" +
		"StandardError=append:" + strings.ReplaceAll(o.LogFile(), "%", "%%") + "\n\n" +
		"[Install]\nWantedBy=default.target\n"
}

// InstallPlan is what Install writes and runs.
func (o Options) InstallPlan() (Plan, error) {
	p, err := o.platform()
	if err != nil {
		return Plan{}, err
	}
	if o.Exe == "" {
		return Plan{}, errors.New("no metarush binary to run")
	}
	if p == Systemd {
		return Plan{File: o.File(), Content: o.Unit(), Commands: [][]string{
			{tools.Systemctl, "--user", "daemon-reload"},
			{tools.Systemctl, "--user", "enable", o.Label()},
			{tools.Systemctl, "--user", "restart", o.Label()},
		}}, nil
	}
	target := o.target()
	return Plan{File: o.File(), Content: o.Plist(), Commands: [][]string{
		{tools.Launchctl, "bootout", target}, // a loaded older copy (not loaded is fine)
		{tools.Launchctl, "enable", target},
		{tools.Launchctl, "bootstrap", o.domain(), o.File()},
	}}, nil
}

// UninstallPlan is what Uninstall runs (then the file is removed).
func (o Options) UninstallPlan() (Plan, error) {
	p, err := o.platform()
	if err != nil {
		return Plan{}, err
	}
	if p == Systemd {
		return Plan{File: o.File(), Commands: [][]string{
			{tools.Systemctl, "--user", "disable", "--now", o.Label()},
			{tools.Systemctl, "--user", "daemon-reload"},
		}}, nil
	}
	return Plan{File: o.File(), Commands: [][]string{{tools.Launchctl, "bootout", o.target()}}}, nil
}

func (o Options) domain() string { return "gui/" + strconv.Itoa(o.uid()) }
func (o Options) target() string { return o.domain() + "/" + o.Label() }

/* ---------- doing it ---------- */

// run runs launchctl/systemctl/loginctl. Under `go test` only fakes run: a
// test can never load a service into the machine's real launchd or systemd.
func (o Options) run(ctx context.Context, cmd []string) (string, error) {
	if o.Tools == nil {
		return "", errors.New("no tools")
	}
	if testing.Testing() && !o.Tools.Explicit(cmd[0]) {
		return "", fmt.Errorf("refusing to run the real %s in a test (set tools.%s to a fake)", cmd[0], cmd[0])
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := o.Tools.Run(ctx, tools.Spec{Tool: cmd[0], Args: cmd[1:]})
	out := ""
	if res != nil {
		out = strings.TrimSpace(string(res.Stdout) + "\n" + string(res.Stderr))
	}
	return out, err
}

// Install writes the plist or unit and loads it (replacing an older one).
func (o Options) Install(ctx context.Context) (Plan, error) {
	plan, err := o.InstallPlan()
	if err != nil {
		return plan, err
	}
	if err := os.MkdirAll(filepath.Dir(o.LogFile()), 0o755); err != nil {
		return plan, err
	}
	if err := os.MkdirAll(filepath.Dir(plan.File), 0o755); err != nil {
		return plan, err
	}
	tmp := plan.File + ".tmp"
	if err := os.WriteFile(tmp, []byte(plan.Content), 0o644); err != nil {
		return plan, err
	}
	if err := os.Rename(tmp, plan.File); err != nil {
		return plan, err
	}
	for i, c := range plan.Commands {
		out, err := o.run(ctx, c)
		if err != nil {
			if p, _ := o.platform(); p == Launchd && i == 0 {
				continue // bootout of a service that was not loaded
			}
			return plan, fmt.Errorf("%s %s failed: %s", c[0], strings.Join(c[1:], " "), firstLine(out, err))
		}
	}
	return plan, nil
}

// Uninstall unloads the service and removes its file.
func (o Options) Uninstall(ctx context.Context) (Plan, error) {
	plan, err := o.UninstallPlan()
	if err != nil {
		return plan, err
	}
	if _, err := os.Stat(plan.File); err != nil {
		return plan, nil // not installed
	}
	for _, c := range plan.Commands {
		_, _ = o.run(ctx, c) // not loaded is fine
	}
	if err := os.Remove(plan.File); err != nil && !os.IsNotExist(err) {
		return plan, err
	}
	if p, _ := o.platform(); p == Systemd {
		_, _ = o.run(ctx, []string{tools.Systemctl, "--user", "daemon-reload"})
	}
	return plan, nil
}

// Restart restarts a running service (it picks up a replaced binary) or
// starts a stopped one.
func (o Options) Restart(ctx context.Context) error {
	p, err := o.platform()
	if err != nil {
		return err
	}
	if _, err := os.Stat(o.File()); err != nil {
		return errors.New("the background service is not installed")
	}
	if p == Systemd {
		out, err := o.run(ctx, []string{tools.Systemctl, "--user", "restart", o.Label()})
		if err != nil {
			return fmt.Errorf("the background service didn't restart: %s", firstLine(out, err))
		}
		return nil
	}
	if out, err := o.run(ctx, []string{tools.Launchctl, "kickstart", "-k", o.target()}); err != nil {
		// Not loaded (for example after `launchctl bootout`): load it again.
		if out2, err2 := o.run(ctx, []string{tools.Launchctl, "bootstrap", o.domain(), o.File()}); err2 != nil {
			return fmt.Errorf("the background service didn't restart: %s; loading it again didn't work either: %s", firstLine(out, err), firstLine(out2, err2))
		}
	}
	return nil
}

// Get reads the service's state.
func (o Options) Get(ctx context.Context) Status {
	p, err := o.platform()
	st := Status{Platform: p, Home: o.Home, Log: o.LogFile()}
	if err != nil {
		st.Problem = err.Error()
		return st
	}
	st.Label, st.File = o.Label(), o.File()
	b, err := os.ReadFile(st.File)
	if err != nil {
		return st
	}
	st.Installed = true
	st.Binary, st.WorkerOnly = parseProgram(string(b), p)
	if o.Exe != "" && st.Binary != "" {
		st.SameBinary = sameFile(st.Binary, o.Exe)
	}
	if p == Systemd {
		out, err := o.run(ctx, []string{tools.Systemctl, "--user", "show", o.Label(), "--property=LoadState,ActiveState,SubState,MainPID,ExecMainStatus,UnitFileState"})
		if err == nil {
			props := map[string]string{}
			for _, line := range strings.Split(out, "\n") {
				if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
					props[k] = v
				}
			}
			st.Loaded = props["UnitFileState"] == "enabled"
			st.Running = props["ActiveState"] == "active"
			st.PID, _ = strconv.Atoi(props["MainPID"])
			if s, err := strconv.Atoi(props["ExecMainStatus"]); err == nil && props["ActiveState"] != "active" {
				st.LastExit = &s
			}
		}
		name := o.User
		if name == "" {
			if u, err := user.Current(); err == nil {
				name = u.Username
			}
		}
		if name != "" && o.Tools != nil && o.Tools.Has(tools.Loginctl) {
			if out, err := o.run(ctx, []string{tools.Loginctl, "show-user", name, "--property=Linger"}); err == nil {
				v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out), "Linger=")) == "yes"
				st.Linger = &v
			}
		}
	} else {
		out, err := o.run(ctx, []string{tools.Launchctl, "print", o.target()})
		if err == nil {
			st.Loaded = true
			for _, line := range strings.Split(out, "\n") {
				k, v, ok := strings.Cut(strings.TrimSpace(line), " = ")
				if !ok {
					continue
				}
				switch k {
				case "state":
					st.Running = v == "running"
				case "pid":
					st.PID, _ = strconv.Atoi(v)
				case "last exit code":
					if n, err := strconv.Atoi(strings.Fields(v + " x")[0]); err == nil {
						st.LastExit = &n
					}
				}
			}
		}
	}
	switch {
	case !st.Loaded:
		st.Problem = "The background service is installed but not loaded."
		st.Fix = "Install the background service again (metarush service install)."
		if p == Launchd {
			st.Fix += " It also loads at your next login."
		}
	case !st.Running:
		st.Problem = "The background service is not running."
		st.Fix = "Start it again (metarush service restart); its log says why it stopped (metarush service logs)."
	case o.Exe != "" && !st.SameBinary:
		st.Problem = "The background service runs a different copy of MetaRush than this one."
		st.Fix = "Install the background service again from the copy you want it to run (metarush service install)."
	}
	return st
}

// parseProgram reads the binary and mode back from a plist or unit.
func parseProgram(content, platform string) (string, bool) {
	worker := strings.Contains(content, "--worker-only")
	if platform == Systemd {
		for _, line := range strings.Split(content, "\n") {
			if rest, ok := strings.CutPrefix(line, "ExecStart="); ok {
				rest = strings.TrimSpace(rest)
				if strings.HasPrefix(rest, `"`) {
					if end := strings.Index(rest[1:], `"`); end >= 0 {
						return strings.ReplaceAll(rest[1:end+1], "%%", "%"), worker
					}
				}
				return strings.Fields(rest + " ")[0], worker
			}
		}
		return "", worker
	}
	i := strings.Index(content, "<key>ProgramArguments</key>")
	if i < 0 {
		return "", worker
	}
	rest := content[i:]
	if j := strings.Index(rest, "<string>"); j >= 0 {
		rest = rest[j+len("<string>"):]
		if k := strings.Index(rest, "</string>"); k >= 0 {
			var v struct {
				S string `xml:",chardata"`
			}
			if xml.Unmarshal([]byte("<s>"+rest[:k]+"</s>"), &v) == nil {
				return v.S, worker
			}
			return rest[:k], worker
		}
	}
	return "", worker
}

// StableExecutable is the path of this binary that survives upgrades: a
// Homebrew Cellar path (which changes with every version) becomes the
// <prefix>/opt/metarush link.
func StableExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	if i := strings.Index(exe, "/Cellar/"); i > 0 {
		prefix, base := exe[:i], filepath.Base(exe)
		for _, cand := range []string{filepath.Join(prefix, "opt", "metarush", "bin", base), filepath.Join(prefix, "bin", base)} {
			if sameFile(cand, exe) {
				return cand, nil
			}
		}
	}
	return exe, nil
}

func sameFile(a, b string) bool {
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}

func firstLine(out string, err error) string {
	s := strings.TrimSpace(out)
	if s == "" && err != nil {
		s = err.Error()
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

/* ---------- the daemon a service (or `metarush dashboard`) started ---------- */

// Daemon is <home>/state/daemon.json, written by a running `metarush serve`.
type Daemon struct {
	PID       int       `json:"pid"`
	Port      int       `json:"port"`
	StartedAt time.Time `json:"startedAt"`
	Version   string    `json:"version"`
	// Mode is "full" (dashboard + worker) or "worker-only"; Service names the
	// service manager that started it (launchd | systemd), if any.
	Mode    string `json:"mode,omitempty"`
	Service string `json:"service,omitempty"`
}

// ReadDaemon reads a home's daemon record; ok is false when no daemon runs.
func ReadDaemon(home string) (Daemon, bool) {
	var d Daemon
	b, err := os.ReadFile(filepath.Join(home, "state", "daemon.json"))
	if err != nil || json.Unmarshal(b, &d) != nil || d.PID <= 0 {
		return d, false
	}
	if err := syscall.Kill(d.PID, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return d, false
	}
	return d, true
}

// LogTail returns the last n lines of the service log.
func LogTail(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const chunk = 256 << 10
	off := st.Size() - chunk
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && len(buf) > 0 {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // a partial first line
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if len(lines) == 1 && lines[0] == "" {
		return []string{}, nil
	}
	return lines, nil
}
