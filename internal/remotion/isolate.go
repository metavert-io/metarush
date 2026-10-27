package remotion

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/sandbox"
	"github.com/metavert-io/metarush/internal/tools"
)

// Keeping a refresh apart (D174): its steps run outside the render jail with
// the network on, so what keeps them from MetaRush's files and keys is the
// operating system's own sandbox — sandbox-exec on a Mac, bubblewrap on Linux
// — with rules MetaRush writes for each run. A computer without one refuses
// refreshes (fail closed).

// Kinds of isolation.
const (
	isoMac   = "macos-sandbox"
	isoLinux = "bubblewrap"
)

// isolation says how this computer keeps a refresh apart.
type isolation struct {
	Kind    string
	Words   string
	Problem string
}

// isolationHere is the isolation this computer offers.
func isolationHere(a *app.App) isolation {
	switch runtime.GOOS {
	case "darwin":
		if a.Tools.Has(tools.SandboxExec) {
			return isolation{Kind: isoMac, Words: "In macOS's own sandbox: no reading or writing in MetaRush's folders, your home folder or the project's original folder, no access to the keychain, and writing only in its own copy."}
		}
		return isolation{Problem: "This Mac's sandbox (sandbox-exec) isn't there, so MetaRush can't keep a refresh away from its own files and keys; it won't run one here."}
	case "linux":
		if a.Tools.Has(tools.Bwrap) {
			if why := sandbox.BwrapWorks(a.Tools, sandbox.ProbeShared); why != "" {
				return isolation{Problem: sandbox.CantRun("a refresh", why).Problem}
			}
			return isolation{Kind: isoLinux, Words: "In bubblewrap: MetaRush's folders and your home folder aren't there for it, everything else is read-only, and it writes only in its own copy."}
		}
		return isolation{Problem: "MetaRush keeps a refresh away from its own files and keys with bubblewrap, which isn't installed on this computer (sudo apt install bubblewrap); it won't run one until it is."}
	}
	return isolation{Problem: "MetaRush can keep a refresh away from its own files and keys only on a Mac or on Linux, so it won't run one on this computer."}
}

// apart is the room one refresh runs in: what it may write, what it may read
// although it is hidden around it, and what is hidden.
type apart struct {
	Root    string   // the run's own folder (work, home, tmp): read and write
	Cache   string   // its cache kept between runs (npm, browsers): read and write
	Work    string   // the copy of the project (inside Root)
	Modules string   // Linux: the package environment's node_modules, mounted read-only at Work/node_modules
	Read    []string // folders it may read inside hidden ones (Node.js's installation)
	Hide    []string // folders it may never read or write (MetaRush's, the person's home, the original)
	// Ports are MetaRush's own services' ports (the dashboard and daemon,
	// the database when it is on this computer): never reached, on any
	// address. Loopback is closed as a whole on a Mac; on Linux the steps
	// have a network of their own and reach the internet through MetaRush.
	Ports []int
	// Linux: the proxy the steps reach the internet through (its port, its
	// socket in Root) and MetaRush's own program, the forwarder in front.
	ProxyPort   int
	ProxySocket string
	Self        string
}

// devPorts are the ports a developer's MetaRush serves on (the dashboard, the
// daemon, Remotion Studio) and the installed default.
var devPorts = []int{4840, 4841, 4842, 4845}

// ownPorts are MetaRush's own services' ports a refresh never reaches: the
// dashboard's, the developer ports, and the database's when its address is
// this computer.
func ownPorts(a *app.App) []int {
	ports := append([]int{}, devPorts...)
	if a.Cfg.Dashboard.Port > 0 {
		ports = append(ports, a.Cfg.Dashboard.Port)
	}
	ports = append(ports, localDBPorts(a.Cfg.Database.URI)...)
	sort.Ints(ports)
	out := ports[:0]
	for i, p := range ports {
		if i == 0 || p != ports[i-1] {
			out = append(out, p)
		}
	}
	return out
}

// OwnPorts are MetaRush's own services' ports on this computer (the
// dashboard's, the developer ports, the database's): an ability's one local
// service is never one of them either (D284).
func OwnPorts(a *app.App) []int { return ownPorts(a) }

// localDBPorts are the ports of a mongodb:// address's hosts on this computer
// (27017 when none is given).
func localDBPorts(uri string) []int {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "mongodb" {
		return nil
	}
	var out []int
	for _, hp := range strings.Split(u.Host, ",") {
		host, port := hp, "27017"
		if h, p, err := net.SplitHostPort(hp); err == nil {
			host, port = h, p
		}
		if !isThisComputer(host) {
			continue
		}
		if n, err := strconv.Atoi(port); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func isThisComputer(host string) bool {
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	if name, err := os.Hostname(); err == nil && strings.EqualFold(strings.TrimSuffix(name, ".local"), strings.TrimSuffix(host, ".local")) {
		return true
	}
	return false
}

// hiddenFor lists the folders a refresh never sees on this computer:
// MetaRush's home, cache, scratch and media folders, the person's home and
// the source's original folder.
func hiddenFor(a *app.App, original string) []string {
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
	add(a.Cfg.Home())
	add(a.Cfg.Cache.Dir)
	add(a.Cfg.Scratch.Dir)
	if a.Cfg.Media.Provider == "filesystem" {
		add(a.Cfg.Media.Filesystem.Root)
	}
	if u, err := os.UserHomeDir(); err == nil {
		add(u)
		add(filepath.Join(u, ".metarush"))
	}
	add(original)
	return dedupeStrings(out)
}

// readableFor are folders inside hidden ones a step needs to read: Node.js's
// own installation (its binary and npm).
func readableFor(node string) []string {
	var out []string
	for _, p := range []string{node} {
		if p == "" {
			continue
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			real = p
		}
		// …/bin/node → the installation's folder (…/lib/node_modules/npm lives there).
		prefix := filepath.Dir(filepath.Dir(real))
		if prefix != "/" && prefix != "." {
			out = append(out, prefix)
		}
		if d := filepath.Dir(p); d != filepath.Dir(real) {
			out = append(out, d)
		}
	}
	return dedupeStrings(out)
}

func dedupeStrings(xs []string) []string {
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

// sbQuote writes a path as a sandbox profile string.
func sbQuote(p string) string {
	return strconv.Quote(p)
}

// macProfile is the sandbox-exec profile of one run. Later rules win: writes
// are denied everywhere, then allowed in the run's own folders; reading the
// contents of the hidden folders is denied (their names may still be looked
// up, which path resolution needs), then allowed for the run's folders and
// Node.js's installation; the keychain's services are out of reach.
func macProfile(ap *apart) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	b.WriteString("(deny file-write* (subpath \"/\"))\n")
	b.WriteString("(allow file-write* (subpath \"/dev\"))\n")
	if len(ap.Hide) > 0 {
		b.WriteString("(deny file-read-data file-read-xattr file-write*")
		for _, h := range ap.Hide {
			b.WriteString(" (subpath " + sbQuote(h) + ")")
		}
		b.WriteString(")\n")
	}
	if len(ap.Read) > 0 {
		b.WriteString("(allow file-read-data file-read-xattr")
		for _, r := range ap.Read {
			b.WriteString(" (subpath " + sbQuote(r) + ")")
		}
		b.WriteString(")\n")
	}
	b.WriteString("(allow file-read* file-write*")
	for _, w := range []string{ap.Root, ap.Cache} {
		if w != "" {
			b.WriteString(" (subpath " + sbQuote(w) + ")")
		}
	}
	b.WriteString(")\n")
	b.WriteString("(deny mach-lookup (global-name \"com.apple.SecurityServer\") (global-name \"com.apple.securityd.xpc\") (global-name \"com.apple.security.agent\") (global-name \"com.apple.security.authhost\"))\n")
	// The network stays on (D174), but never this computer: no loopback at
	// all (MetaRush's database, dashboard, media and model servers listen
	// there, some on ports chosen as they start), MetaRush's own ports on any
	// address, no unix sockets in the shared temporary folder (the database's)
	// or the hidden folders, and nothing listens for others.
	b.WriteString("(deny network-outbound (remote ip \"localhost:*\"))\n")
	if len(ap.Ports) > 0 {
		b.WriteString("(deny network-outbound")
		for _, p := range ap.Ports {
			b.WriteString(" (remote ip " + sbQuote("*:"+strconv.Itoa(p)) + ")")
		}
		b.WriteString(")\n")
	}
	b.WriteString("(deny network-outbound (remote unix-socket (subpath \"/private/tmp\"))")
	for _, h := range ap.Hide {
		b.WriteString(" (remote unix-socket (subpath " + sbQuote(h) + "))")
	}
	b.WriteString(")\n(deny network-inbound)\n")
	return b.String()
}

// bwrapArgs are bubblewrap's arguments for one run: the whole system
// read-only, the hidden folders replaced by empty ones, the run's folders
// writable, its packages read-only, a fresh /dev, /proc and /tmp.
func bwrapArgs(ap *apart, uid int) []string {
	args := []string{"--die-with-parent", "--new-session", "--unshare-ipc", "--unshare-uts", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp"}
	for _, h := range ap.Hide {
		if st, err := os.Stat(h); err == nil && st.IsDir() {
			args = append(args, "--tmpfs", h)
		}
	}
	// The session bus (keyrings) and the person's runtime folder.
	if uid >= 0 {
		if rd := fmt.Sprintf("/run/user/%d", uid); dirExists(rd) {
			args = append(args, "--tmpfs", rd)
		}
	}
	for _, r := range ap.Read {
		if dirExists(r) {
			args = append(args, "--ro-bind", r, r)
		}
	}
	for _, w := range []string{ap.Root, ap.Cache} {
		if w != "" {
			args = append(args, "--bind", w, w)
		}
	}
	if ap.Modules != "" && ap.Work != "" {
		args = append(args, "--ro-bind", ap.Modules, filepath.Join(ap.Work, "node_modules"))
	}
	args = append(args, "--chdir", ap.Work)
	return args
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// command wraps one step (node and its arguments) in the isolation.
func (ap *apart) command(kind, node string, args []string) (string, []string, error) {
	switch kind {
	case isoMac:
		return tools.SandboxExec, append([]string{"-p", macProfile(ap), node}, args...), nil
	case isoLinux:
		bw := bwrapArgs(ap, os.Getuid())
		if ap.ProxyPort > 0 {
			// A network of its own: its loopback is its own, and the only
			// way out is the forwarder to MetaRush's proxy (any public host).
			if ap.ProxySocket == "" || ap.Self == "" {
				return "", nil, fmt.Errorf("the proxy's socket and MetaRush's own program are needed")
			}
			bw = append(bw, "--unshare-net", "--ro-bind", ap.Self, ap.Self, "--",
				ap.Self, sandbox.ForwardCommand, "--listen", "127.0.0.1:"+strconv.Itoa(ap.ProxyPort), "--to", ap.ProxySocket, "--", node)
			return tools.Bwrap, append(bw, args...), nil
		}
		return tools.Bwrap, append(append(bw, "--", node), args...), nil
	}
	return "", nil, fmt.Errorf("this computer can't keep a refresh apart")
}
