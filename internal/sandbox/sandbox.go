// Package sandbox runs a program MetaRush didn't write — an ability's script
// or program (D230–D235) — inside the operating system's own sandbox, with
// rules MetaRush writes for each run: on a Mac a sandbox-exec profile that
// denies everything it doesn't allow (the system's own base rules, the
// program's installation, the run's own room), on Linux bubblewrap with only
// the system's folders, the program's installation and the room mounted.
//
// The network is one of three: off (nothing at all), MetaRush's proxy for
// this run only (on a Mac the one loopback port the proxy listens on; on
// Linux a network of its own whose only way out is a forwarder to the
// proxy's socket in the room), or open — still through the proxy, to any
// public host, never this computer or a local network (a separate,
// people-only approval).
// A computer that can't do this refuses to run the program (fail closed).
//
// Remotion refreshes (D174) keep their own, older rules in
// internal/remotion/isolate.go: they run with the network on.
package sandbox

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// Kinds of sandbox.
const (
	Mac   = "macos-sandbox"
	Linux = "bubblewrap"
)

// Network modes.
const (
	NetOff   = "off"   // no network at all
	NetProxy = "proxy" // only MetaRush's proxy for this run
	NetOpen  = "open"  // any public host, still through the proxy (a people-only approval)
)

// Isolation says how this computer keeps a program apart.
type Isolation struct {
	Kind    string `json:"kind,omitempty" jsonschema:"enum=,enum=macos-sandbox,enum=bubblewrap"`
	Words   string `json:"words,omitempty"`
	Problem string `json:"problem,omitempty"`
	// Fix is what an administrator of this computer does so it can (Details):
	// a command, or where MetaRush's documentation says how.
	Fix string `json:"fix,omitempty"`
	// Said is what bubblewrap itself said when it couldn't run (Details).
	Said string `json:"said,omitempty"`
}

// OK reports whether this computer can run a program apart.
func (i Isolation) OK() bool { return i.Kind != "" }

// Here is the isolation this computer offers.
func Here(ts *tools.Set) Isolation {
	switch runtime.GOOS {
	case "darwin":
		if ts.Has(tools.SandboxExec) {
			return Isolation{Kind: Mac, Words: "In macOS's own sandbox: it reads only the system, its own program and its own folder, writes only in its own folder, and reaches no network except what MetaRush allows."}
		}
		return Isolation{Problem: "This Mac's sandbox (sandbox-exec) isn't there, so MetaRush can't keep an ability away from your files and keys; it won't run one here."}
	case "linux":
		if ts.Has(tools.Bwrap) {
			if why := BwrapWorks(ts, ProbeApart); why != "" {
				return CantRun("an ability", why)
			}
			return Isolation{Kind: Linux, Words: "In bubblewrap: it sees only the system, its own program and its own folder, writes only in its own folder, and reaches no network except what MetaRush allows."}
		}
		return Isolation{Problem: "MetaRush keeps an ability away from your files and keys with bubblewrap, which isn't installed on this computer, so it won't run one until it is.", Fix: "sudo apt install bubblewrap (or your system's package of it)"}
	}
	return Isolation{Problem: "MetaRush can keep an ability away from your files and keys only on a Mac or on Linux, so it won't run one on this computer."}
}

// AppArmorProfile lets bubblewrap make its own sandbox on a computer whose
// AppArmor allows only listed programs to (Ubuntu 23.10 and later): an
// administrator saves it as /etc/apparmor.d/bwrap and loads it with
// apparmor_parser -r. It names only /usr/bin/bwrap. CI installs this file.
//
//go:embed bwrap.apparmor
var AppArmorProfile string

// CantRun says plainly why bubblewrap, though installed, can't make a
// sandbox here, for what would run in it ("an ability", "a refresh"), with
// what an administrator does about it and what bubblewrap said (Details).
// MetaRush refuses to run the program then; it never runs it unsandboxed.
func CantRun(what, said string) Isolation {
	l := namespaceLimit(readSysctl, what)
	if l.Problem == "" {
		l = Isolation{Problem: "bubblewrap is installed but can't make a sandbox on this computer, so MetaRush can't keep " + what + " away from your files and keys; it won't run one until it can.",
			Fix: "Run bwrap --unshare-all --ro-bind / / -- /bin/true as yourself to see why (docs/abilities.md, Linux)."}
	}
	l.Said = said
	return l
}

// namespaceLimit says whether this Linux computer is set to stop programs
// from making the namespaces bubblewrap needs, and what an administrator
// does about it. sysctl reads a setting under /proc/sys ("" when it isn't
// there).
func namespaceLimit(sysctl func(string) string, what string) Isolation {
	switch {
	case sysctl("kernel/apparmor_restrict_unprivileged_userns") == "1":
		return Isolation{Problem: "This computer's security settings let only the programs they list make a sandbox of their own, and bubblewrap isn't one of them, so MetaRush won't run " + what + " here until an administrator adds it.",
			Fix: "Save the profile in docs/abilities.md (Linux) as /etc/apparmor.d/bwrap, then: sudo apparmor_parser -r /etc/apparmor.d/bwrap. (sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0 works too, but lifts the limit for every program.)"}
	case sysctl("kernel/unprivileged_userns_clone") == "0":
		return Isolation{Problem: "This computer is set to stop programs from making a sandbox of their own, which bubblewrap needs, so MetaRush won't run " + what + " here until an administrator allows it.",
			Fix: "sudo sysctl -w kernel.unprivileged_userns_clone=1 (and the same line in /etc/sysctl.d/ to keep it)"}
	case sysctl("user/max_user_namespaces") == "0":
		return Isolation{Problem: "This computer is set to stop programs from making a sandbox of their own, which bubblewrap needs, so MetaRush won't run " + what + " here until an administrator allows it.",
			Fix: "sudo sysctl -w user.max_user_namespaces=15000 (and the same line in /etc/sysctl.d/ to keep it)"}
	}
	return Isolation{}
}

// readSysctl reads one setting under /proc/sys.
func readSysctl(name string) string {
	b, err := os.ReadFile(filepath.Join("/proc/sys", name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// What BwrapWorks tries.
const (
	// ProbeApart: every namespace its own, the network too (abilities).
	ProbeApart = "apart"
	// ProbeShared: the computer's network (Remotion refreshes, D174).
	ProbeShared = "shared"
)

var (
	bwrapMu    sync.Mutex
	bwrapProbe = map[string]string{} // bwrap's path and the probe → why it can't run ("" = it can)
)

// BwrapWorks tries bubblewrap once per process and kind of run (user
// namespaces can be off or limited): "" when it runs, else what it said.
func BwrapWorks(ts *tools.Set, probe string) string {
	p := ts.Path(tools.Bwrap)
	key := p + "\x00" + probe
	bwrapMu.Lock()
	defer bwrapMu.Unlock()
	if why, ok := bwrapProbe[key]; ok {
		return why
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := []string{"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--", "/bin/true"}
	if probe == ProbeApart {
		args = append(systemBinds(), "--dev", "/dev", "--proc", "/proc", "--unshare-all", "--die-with-parent", "--", "/bin/true")
	}
	why := ""
	if _, err := ts.Run(tools.Quiet(ctx), tools.Spec{Tool: tools.Bwrap, Args: args}); err != nil {
		var ef *tools.ErrFailed
		why = err.Error()
		if errors.As(err, &ef) && strings.TrimSpace(ef.Stderr) != "" {
			why = strings.TrimSpace(ef.Stderr)
		}
		if len(why) > 160 {
			why = why[:160] + "…"
		}
	}
	bwrapProbe[key] = why
	return why
}

// Room is where one run happens: its own folder (the ability's files, a home,
// a temporary folder, its inputs and outputs), what else it may read, and
// how it reaches the network.
type Room struct {
	// Root is the run's own folder: it reads all of it.
	Root string
	// Work is the folder it starts in (inside Root).
	Work string
	// Write are the folders inside Root it may write (its output, home and
	// temporary folders); empty: all of Root.
	Write []string
	// Read are files and folders it may read besides the system's: the
	// program and its installation (InstallationOf).
	Read []string
	// Hide are folders it must never see (MetaRush's home, cache, scratch and
	// media, the person's home folder): they always win over Read, a Read
	// inside one is left out, and a Read that is one or holds one is refused.
	Hide []string
	// Net is how it reaches the network (NetOff when empty).
	Net string
	// ProxyPort is the loopback port MetaRush's proxy listens on (NetProxy).
	// On Linux the forwarder inside the sandbox listens on the same port.
	ProxyPort int
	// ProxySocket is the proxy's socket in Root (NetProxy on Linux).
	ProxySocket string
	// Self is this MetaRush binary, which forwards to the proxy inside the
	// sandbox (NetProxy on Linux).
	Self string
}

func (r *Room) net() string {
	if r.Net == "" {
		return NetOff
	}
	return r.Net
}

// within reports whether p is base or inside it.
func within(p, base string) bool {
	p, base = filepath.Clean(p), filepath.Clean(base)
	return p == base || base == "/" || strings.HasPrefix(p, base+"/")
}

// Hidden reports whether a path is one of the folders a sandboxed program
// never sees, or inside one: hide (HiddenFor) and the person's home folder.
func Hidden(p string, hide []string) bool {
	for _, h := range hiddenAll(hide) {
		if within(p, h) {
			return true
		}
	}
	return false
}

// hiddenAll is hide with the person's home folder (as written and with its
// links followed).
func hiddenAll(hide []string) []string {
	out := append([]string{}, hide...)
	if u, err := os.UserHomeDir(); err == nil && u != "" {
		out = append(out, filepath.Clean(u))
		if real, err := filepath.EvalSymlinks(u); err == nil {
			out = append(out, real)
		}
	}
	return Dedupe(out)
}

// readable is what the room may read besides the system's: Read without what
// sits inside a hidden folder (hidden folders always win). A Read that is a
// hidden folder, holds one or isn't a full path is refused, and so is a room
// that is or sits inside or above a hidden folder.
func (r *Room) readable() ([]string, error) {
	hide := hiddenAll(r.Hide)
	for _, h := range hide {
		if within(r.Root, h) || within(h, r.Root) {
			return nil, fmt.Errorf("sandbox: the room %s and the hidden folder %s overlap", r.Root, h)
		}
	}
	var out []string
	for _, p := range r.Read {
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("sandbox: %q isn't a full path", p)
		}
		p = filepath.Clean(p)
		skip := false
		for _, h := range hide {
			switch {
			case within(h, p):
				return nil, fmt.Errorf("sandbox: reading %s would open the hidden folder %s", p, h)
			case within(p, h):
				skip = true
			}
		}
		if !skip {
			out = append(out, p)
		}
	}
	for _, w := range r.Write {
		if !within(w, r.Root) {
			return nil, fmt.Errorf("sandbox: %s isn't inside the room", w)
		}
	}
	return Dedupe(out), nil
}

// writable are the folders the room may write.
func (r *Room) writable() []string {
	if len(r.Write) == 0 {
		return []string{r.Root}
	}
	return Dedupe(r.Write)
}

// Command wraps a program and its arguments in this computer's sandbox: the
// allowlisted tool to run (sandbox-exec or bwrap) and its arguments.
func (r *Room) Command(kind, program string, args []string) (string, []string, error) {
	if r.Root == "" || r.Work == "" || !filepath.IsAbs(program) {
		return "", nil, errors.New("sandbox: a room and a program with its full path are needed")
	}
	if _, err := r.readable(); err != nil {
		return "", nil, err
	}
	switch r.net() {
	case NetOff:
	case NetProxy, NetOpen:
		if r.ProxyPort <= 0 {
			return "", nil, errors.New("sandbox: the proxy's port is missing")
		}
	default:
		return "", nil, fmt.Errorf("sandbox: unknown network mode %q", r.Net)
	}
	switch kind {
	case Mac:
		return tools.SandboxExec, append([]string{"-p", MacProfile(r), program}, args...), nil
	case Linux:
		if r.net() != NetOff {
			if r.ProxySocket == "" || r.Self == "" {
				return "", nil, errors.New("sandbox: the proxy's socket and MetaRush's own program are needed on Linux")
			}
			inner := append([]string{r.Self, ForwardCommand, "--listen", "127.0.0.1:" + strconv.Itoa(r.ProxyPort), "--to", r.ProxySocket, "--", program}, args...)
			bw := append(BwrapArgs(r), "--ro-bind", r.Self, r.Self)
			return tools.Bwrap, append(bw, append([]string{"--"}, inner...)...), nil
		}
		return tools.Bwrap, append(append(BwrapArgs(r), "--", program), args...), nil
	}
	return "", nil, errors.New("this computer can't keep an ability apart")
}

// ForwardCommand is the hidden MetaRush command that forwards, inside a
// Linux sandbox, a loopback port to the proxy's socket and then runs the
// program (internal/cli registers it; Forward does the work).
const ForwardCommand = "sandbox-forward"

func sbQuote(p string) string { return strconv.Quote(p) }

// macSystemRead are folders every program may read on a Mac besides what the
// system's own base rules (system.sb) allow: the system's programs, its
// settings, the developer tools' folders (Apple's python3 lives there).
var macSystemRead = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin", "/usr/libexec", "/private/etc", "/private/var/db/timezone",
	"/private/var/db/dyld", "/private/var/select", "/Library/Developer/CommandLineTools", "/Applications/Xcode.app/Contents/Developer"}

// MacProfile is the sandbox-exec profile of one run. It starts from "deny
// everything" and the system's own base rules (system.sb: system libraries,
// a short list of system services), then allows starting programs, reading
// the system's folders, the program and its installation and the room,
// writing only in the room's own write folders, and the network the room
// says. Looking at files' names and sizes (metadata) is allowed only there
// and on the folders above them. Hidden folders are denied last, so they win
// over everything else (later rules win). Services that would carry data out
// without the network — the clipboard, Spotlight, Apple Events, opening links
// in other apps, the keychain — stay denied because nothing allows them;
// /dev/tty too (a run has no terminal: tools.Spec.Contained).
func MacProfile(r *Room) string {
	read, err := r.readable()
	if err != nil {
		read = nil // Command refuses such a room; a profile alone reads nothing extra
	}
	var b strings.Builder
	b.WriteString("(version 1)\n(deny default)\n(import \"system.sb\")\n")
	b.WriteString("(allow process-fork)\n(allow process-exec)\n(allow signal (target self))\n(allow process-info* (target self))\n")
	b.WriteString("(allow ipc-posix-sem)\n(allow ipc-posix-shm)\n")
	// Metadata: the places it may read, and the folders above them (path
	// resolution looks at each).
	places := append(append(append([]string{}, macSystemRead...), read...), r.Root)
	b.WriteString("(allow file-read-metadata")
	for _, p := range places {
		b.WriteString(" (subpath " + sbQuote(p) + ")")
	}
	for _, a := range ancestors(places) {
		b.WriteString(" (literal " + sbQuote(a) + ")")
	}
	b.WriteString(")\n")
	b.WriteString("(allow file-read* file-map-executable")
	for _, p := range macSystemRead {
		b.WriteString(" (subpath " + sbQuote(p) + ")")
	}
	for _, p := range read {
		b.WriteString(" (subpath " + sbQuote(p) + ")")
	}
	b.WriteString(" (subpath " + sbQuote(r.Root) + "))\n")
	b.WriteString("(allow file-write*")
	for _, w := range r.writable() {
		b.WriteString(" (subpath " + sbQuote(w) + ")")
	}
	b.WriteString(")\n")
	switch r.net() {
	case NetProxy, NetOpen:
		// The whole network goes through MetaRush's proxy too (any public
		// host); nothing listens, nothing reaches this computer otherwise.
		b.WriteString("(deny network*)\n(allow network-outbound (remote ip " + sbQuote("localhost:"+strconv.Itoa(r.ProxyPort)) + "))\n")
	default:
		b.WriteString("(deny network*)\n")
	}
	if hide := hiddenAll(r.Hide); len(hide) > 0 {
		b.WriteString("(deny file-read* file-write* file-map-executable")
		for _, h := range hide {
			b.WriteString(" (subpath " + sbQuote(h) + ")")
		}
		b.WriteString(")\n")
	}
	return b.String()
}

// ancestors are the folders above paths (/ included), and the system's
// links at the top (/var, /tmp, /etc).
func ancestors(paths []string) []string {
	seen := map[string]bool{"/": true, "/var": true, "/tmp": true, "/etc": true}
	for _, p := range paths {
		for d := filepath.Dir(filepath.Clean(p)); d != "/" && d != "."; d = filepath.Dir(d) {
			seen[d] = true
		}
	}
	out := make([]string, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// linuxSystem are the folders bubblewrap mounts read-only from the system.
var linuxSystem = []string{"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/etc", "/nix/store", "/run/current-system"}

// systemBinds mounts the system's folders read-only (merged-/usr links kept
// as links).
func systemBinds() []string {
	var args []string
	for _, p := range linuxSystem {
		st, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if st.Mode()&os.ModeSymlink != 0 {
			if t, err := os.Readlink(p); err == nil {
				args = append(args, "--symlink", t, p)
			}
			continue
		}
		if st.IsDir() {
			args = append(args, "--ro-bind", p, p)
		}
	}
	return args
}

// BwrapArgs are bubblewrap's arguments for one run: only the system's
// folders (read-only), the program and its installation (read-only) and the
// room (read-only, its write folders writable) exist; a fresh /dev, /proc and
// /tmp; every namespace its own, the network too (MetaRush's network comes
// in through the forwarder). Hidden folders are never mounted, nor anything
// inside them.
func BwrapArgs(r *Room) []string {
	args := []string{"--die-with-parent", "--new-session", "--unshare-all"}
	args = append(args, systemBinds()...)
	args = append(args, "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp")
	read, _ := r.readable()
	for _, p := range read {
		if dirExists(p) || fileExists(p) {
			args = append(args, "--ro-bind", p, p)
		}
	}
	args = append(args, "--ro-bind", r.Root, r.Root)
	for _, w := range r.writable() {
		args = append(args, "--bind", w, w)
	}
	return append(args, "--chdir", r.Work)
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// HiddenFor lists the folders a sandboxed program never sees on this
// computer: MetaRush's home, cache, scratch and media folders and the
// person's home folder, with extra ones (an original folder).
func HiddenFor(cfg *config.Config, extra ...string) []string {
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if real, err := filepath.EvalSymlinks(p); err == nil {
			out = append(out, real)
		}
		out = append(out, filepath.Clean(p))
	}
	if cfg != nil {
		add(cfg.Home())
		add(cfg.Cache.Dir)
		add(cfg.Scratch.Dir)
		if cfg.Media.Provider == "filesystem" {
			add(cfg.Media.Filesystem.Root)
		}
	}
	if u, err := os.UserHomeDir(); err == nil {
		add(u)
	}
	for _, e := range extra {
		add(e)
	}
	return Dedupe(out)
}

// InstallationOf is what a sandboxed run may read of a program besides the
// system's folders: the program itself (and the link it was found through)
// and, when it belongs to an installation of its own, that installation — a
// Homebrew keg (with the other kegs, their opt links and the shared lib
// folder, where its libraries and packages are, and Homebrew's TLS settings),
// a Python.framework version,
// a folder like /opt/<name> with bin/ and lib/ — never a folder of programs
// (~/bin, /usr/local/bin) or anything above one, and never the person's home.
func InstallationOf(program string) []string {
	real, err := filepath.EvalSymlinks(program)
	if err != nil {
		real = program
	}
	out := []string{real}
	if program != real {
		out = append(out, program)
	}
	if i := strings.Index(real, "/Cellar/"); i > 0 {
		prefix := real[:i]
		out = append(out, prefix+"/Cellar", prefix+"/opt", prefix+"/lib")
		// TLS settings its OpenSSL reads at start (never the rest of etc).
		tls, _ := filepath.Glob(prefix + "/etc/openssl*")
		out = append(append(out, tls...), prefix+"/etc/ca-certificates")
	}
	if i := strings.Index(real, "/Python.framework/Versions/"); i >= 0 {
		rest := real[i+len("/Python.framework/Versions/"):]
		if ver, _, ok := strings.Cut(rest, "/"); ok && ver != "" {
			out = append(out, real[:i]+"/Python.framework/Versions/"+ver)
		}
	}
	if dir := filepath.Dir(real); filepath.Base(dir) == "bin" {
		root := filepath.Dir(dir)
		home, _ := os.UserHomeDir()
		if !commonRoots[root] && (home == "" || !within(home, root)) && dirExists(filepath.Join(root, "lib")) {
			out = append(out, root)
		}
	}
	return Dedupe(out)
}

// commonRoots hold many programs' folders: never an installation of one.
var commonRoots = map[string]bool{"/": true, "/usr": true, "/usr/local": true, "/opt": true, "/opt/homebrew": true, tools.LinuxbrewPrefix: true,
	"/snap": true, "/home": true, "/Users": true, "/Applications": true, "/Library": true, "/System": true}

// appleShims are the programs in /usr/bin that only hand over to the
// developer tools (Xcode or the Command Line Tools) on a Mac.
var appleShims = map[string]bool{"python3": true, "pip3": true, "git": true, "make": true, "clang": true, "swift": true, "ruby": false}

// RealProgram is the program that actually runs: on a Mac, Apple's
// /usr/bin/python3 (and the other developer-tool shims) hand over to the
// developer tools' own copy, which is what a sandboxed run starts and pins
// (the shim would need Xcode's whole folder and a cache outside the room).
func RealProgram(p string) string {
	if runtime.GOOS != "darwin" || filepath.Dir(p) != "/usr/bin" || !appleShims[filepath.Base(p)] {
		return p
	}
	dev := "/Library/Developer/CommandLineTools"
	if t, err := os.Readlink("/private/var/db/xcode_select_link"); err == nil && t != "" {
		dev = t
	}
	if c := filepath.Join(dev, "usr", "bin", filepath.Base(p)); fileExists(c) || symlinkTo(c) {
		return c
	}
	return p
}

func symlinkTo(p string) bool {
	_, err := filepath.EvalSymlinks(p)
	return err == nil
}

// Dedupe sorts and removes repeats and empty strings.
func Dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}
