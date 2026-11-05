// Package wsl knows when MetaRush runs inside the Windows Subsystem for Linux
// (D515): which WSL (MetaRush runs on WSL 2; WSL 1 is refused, D516), the
// Linux distribution, whether Ubuntu can start Windows programs (interop:
// the Windows browser, D518), whether systemd runs (the background service,
// D517), where Windows' drives are (/mnt/c, D520–D522) and what Windows'
// own WSL settings say about keeping Ubuntu running.
//
// Everything is read from files WSL leaves (the kernel's name, /run/WSL, the
// interop entry in binfmt_misc, /etc/wsl.conf, the mount table) and the
// environment, through a Probe whose Root and Env tests point at stand-ins;
// nothing here runs a program. Other packages ask Here (read once per
// process) and play another computer with Pretend. The package depends on
// nothing else in MetaRush, so every layer can use it.
package wsl

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Info is what MetaRush knows about the WSL it runs in. The zero Info is "not
// WSL" (a Mac, plain Linux, a container).
type Info struct {
	// Version is 2 inside WSL 2, 1 inside WSL 1 and 0 elsewhere.
	Version int `json:"version,omitempty"`
	// Distro is the distribution's name as WSL knows it (WSL_DISTRO_NAME:
	// "Ubuntu", "Ubuntu-24.04"), else as the distribution calls itself
	// (/etc/os-release NAME); DistroKnown says it is WSL's own name, the one
	// `wsl -d` and `wsl --set-version` take.
	Distro      string `json:"distro,omitempty"`
	DistroKnown bool   `json:"distroKnown,omitempty"`
	// Name is what the distribution calls itself (/etc/os-release NAME:
	// "Ubuntu"), what MetaRush's words say; Distro is for commands.
	Name string `json:"name,omitempty"`
	// Kernel is the Linux kernel's release (`uname -r`).
	Kernel string `json:"kernel,omitempty"`
	// Interop: Ubuntu can start Windows programs (rundll32.exe), so MetaRush can
	// open the Windows browser.
	Interop bool `json:"interop"`
	// Systemd: systemd runs as the first process, so the background service
	// (a systemd user unit) can be set up. SystemdAsked: /etc/wsl.conf turns
	// it on ([boot] systemd=true); asked but not running means WSL hasn't
	// restarted since.
	Systemd      bool `json:"systemd"`
	SystemdAsked bool `json:"systemdAsked"`
	// DriveRoot is where WSL mounts Windows' drives (/etc/wsl.conf's
	// automount root, "/mnt/" by default).
	DriveRoot string `json:"driveRoot,omitempty"`
	// Drives are the Windows drives mounted now (mount point → drive letter,
	// lower case), from the mount table; empty when it couldn't be read.
	Drives map[string]string `json:"drives,omitempty"`
}

// In reports whether this is WSL (1 or 2).
func (i Info) In() bool { return i.Version > 0 }

// Probe reads what WSL leaves on a computer. The zero Probe reads this
// computer; tests point Root at a folder of stand-in files and Env at a map.
type Probe struct {
	// Root is the folder that stands for "/" ("" is "/").
	Root string
	// Env is the environment (nil: this process's).
	Env map[string]string
	// GOOS is the system ("": runtime.GOOS).
	GOOS string
}

func (p Probe) path(rel string) string {
	root := p.Root
	if root == "" {
		root = "/"
	}
	return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(rel, "/")))
}

func (p Probe) getenv(k string) string {
	if p.Env != nil {
		return p.Env[k]
	}
	return os.Getenv(k)
}

func (p Probe) goos() string {
	if p.GOOS != "" {
		return p.GOOS
	}
	return runtime.GOOS
}

// read returns the start of a file (up to 256 KB), "" when it can't be read.
func (p Probe) read(rel string) string {
	f, err := os.Open(p.path(rel))
	if err != nil {
		return ""
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 256<<10))
	return string(b)
}

func (p Probe) exists(rel string) bool {
	_, err := os.Stat(p.path(rel))
	return err == nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

// Detect reads the computer. WSL is recognised by the kernel's name
// ("…-microsoft-standard-WSL2", WSL 1's "…-Microsoft") together with what
// only a WSL distribution has — WSL_DISTRO_NAME, /run/WSL, the interop entry,
// /etc/wsl.conf or /mnt/wsl — so a container that merely shares WSL's kernel
// (Docker Desktop's) isn't taken for it; a WSL 2 with a kernel of its own is
// recognised by WSL_DISTRO_NAME with /run/WSL or the interop entry.
func (p Probe) Detect() Info {
	if p.goos() != "linux" {
		return Info{}
	}
	rel := firstLine(p.read("proc/sys/kernel/osrelease"))
	ver := firstLine(p.read("proc/version"))
	low := strings.ToLower(rel + " " + ver)
	kernelSays := strings.Contains(low, "microsoft") || strings.Contains(low, "wsl")
	name := strings.TrimSpace(p.getenv("WSL_DISTRO_NAME"))
	interopFile := ""
	for _, f := range []string{"proc/sys/fs/binfmt_misc/WSLInterop", "proc/sys/fs/binfmt_misc/WSLInterop-late"} {
		if p.exists(f) {
			interopFile = f
			break
		}
	}
	runWSL := p.exists("run/WSL")
	wsl1 := kernelSays && wsl1Kernel(rel, ver) && !runWSL && p.getenv("WSL_INTEROP") == ""
	distroSays := name != "" || interopFile != "" || runWSL || p.exists("etc/wsl.conf") || p.exists("mnt/wsl")
	if !(wsl1 || kernelSays && distroSays || name != "" && (runWSL || interopFile != "")) {
		return Info{}
	}
	i := Info{Version: 2, Kernel: rel, DriveRoot: "/mnt/"}
	if wsl1 {
		i.Version = 1
	}
	i.Distro, i.DistroKnown = name, name != ""
	i.Name = parseKeyValues(p.read("etc/os-release"))["NAME"]
	if i.Distro == "" {
		i.Distro = i.Name
	}
	if interopFile != "" {
		// "enabled" or "disabled" on its first line ([interop] enabled=false
		// in wsl.conf leaves no entry at all).
		i.Interop = firstLine(p.read(interopFile)) != "disabled"
	}
	conf := ParseConf(p.read("etc/wsl.conf"))
	i.SystemdAsked = conf.Bool("boot", "systemd", false)
	if root := conf.Get("automount", "root"); root != "" {
		i.DriveRoot = strings.TrimSuffix(root, "/") + "/"
	}
	if i.Version == 2 {
		i.Systemd = p.exists("run/systemd/system") || firstLine(p.read("proc/1/comm")) == "systemd"
	}
	mounts := p.read("proc/self/mounts")
	if mounts == "" {
		mounts = p.read("proc/mounts")
	}
	i.Drives = parseDrives(mounts)
	return i
}

// PlainDistroName reports whether name is a distribution's name as WSL allows
// it (letters, digits, ".", "_" and "-"), safe to write into a setting.
func PlainDistroName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// wsl1Kernel reports WSL 1's kernel name: "4.4.0-19041-Microsoft" (and its
// /proc/version "Linux version 4.4.0-19041-Microsoft (…"), never
// "microsoft-standard" or "WSL2".
func wsl1Kernel(rel, ver string) bool {
	low := strings.ToLower(rel + " " + ver)
	if strings.Contains(low, "microsoft-standard") || strings.Contains(low, "wsl2") {
		return false
	}
	return strings.HasSuffix(rel, "-Microsoft") || strings.Contains(ver, "-Microsoft (")
}

var (
	mu      sync.Mutex
	cached  *Info
	pretend *Info
)

// Here is this computer as WSL leaves it, read once per process.
func Here() Info {
	mu.Lock()
	defer mu.Unlock()
	if pretend != nil {
		return *pretend
	}
	if cached == nil {
		i := Probe{}.Detect()
		cached = &i
	}
	return *cached
}

// Pretend makes Here answer i until restore is called: tests in other
// packages play WSL (or a computer that isn't) with it.
func Pretend(i Info) (restore func()) {
	mu.Lock()
	before := pretend
	pretend = &i
	mu.Unlock()
	return func() {
		mu.Lock()
		pretend = before
		mu.Unlock()
	}
}

/* ---------- WSL's settings files ---------- */

// Conf is an INI file as WSL reads its settings (/etc/wsl.conf, the Windows
// user's .wslconfig): sections and keys in any case, "key = value", values
// in quotes or not, "#" and ";" comments.
type Conf map[string]map[string]string

// ParseConf reads one.
func ParseConf(s string) Conf {
	c := Conf{}
	section := ""
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if end := strings.IndexByte(line, ']'); end > 0 {
				section = strings.ToLower(strings.TrimSpace(line[1:end]))
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		} else if i := strings.IndexAny(v, "#;"); i >= 0 {
			v = strings.TrimSpace(v[:i]) // a comment after an unquoted value
		}
		if c[section] == nil {
			c[section] = map[string]string{}
		}
		c[section][strings.ToLower(strings.TrimSpace(k))] = v
	}
	return c
}

// Get is a key's value ("" when it isn't set).
func (c Conf) Get(section, key string) string {
	return c[strings.ToLower(section)][strings.ToLower(key)]
}

// Bool is a true/false key (def when it isn't set or isn't either).
func (c Conf) Bool(section, key string, def bool) bool {
	switch strings.ToLower(c.Get(section, key)) {
	case "true", "yes", "1", "on":
		return true
	case "false", "no", "0", "off":
		return false
	}
	return def
}

// Int is a whole-number key (ok false when it isn't set or isn't one).
func (c Conf) Int(section, key string) (int64, bool) {
	v := strings.TrimSpace(c.Get(section, key))
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	return n, err == nil
}

// parseKeyValues reads /etc/os-release.
func parseKeyValues(s string) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		out[k] = strings.Trim(v, `"'`)
	}
	return out
}

/* ---------- Windows' drives ---------- */

// parseDrives finds Windows' drives in a mount table (/proc/self/mounts):
// WSL 2 mounts them over 9p with aname=drvfs (or as drvfs, or virtiofs),
// WSL 1 as drvfs; the letter comes from the path= option ("path=C:\"), else
// the mount point's last part.
func parseDrives(table string) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(table))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		src, mnt, typ, opts := unescapeMount(f[0]), unescapeMount(f[1]), f[2], f[3]
		drvfs := typ == "drvfs" || typ == "9p" && strings.Contains(opts, "aname=drvfs") ||
			typ == "virtiofs" && (strings.HasPrefix(strings.ToLower(src), "drvfs") || driveLetter(src) != "")
		if !drvfs {
			continue
		}
		letter := ""
		for _, o := range strings.FieldsFunc(opts, func(r rune) bool { return r == ',' || r == ';' }) {
			if v, ok := strings.CutPrefix(o, "path="); ok {
				letter = driveLetter(v)
			}
		}
		if letter == "" {
			letter = driveLetter(src)
		}
		if letter == "" {
			if b := filepath.Base(mnt); len(b) == 1 && isLetter(b[0]) {
				letter = strings.ToLower(b)
			}
		}
		if letter != "" {
			out[filepath.Clean(mnt)] = letter
		}
	}
	return out
}

// driveLetter is "c" for "C:\", "C:" or "C:\134" (the mount table's
// spelling of a backslash), "" otherwise.
func driveLetter(s string) string {
	if len(s) >= 2 && isLetter(s[0]) && s[1] == ':' && (len(s) == 2 || s[2] == '\\' || s[2] == '/') {
		return strings.ToLower(s[:1])
	}
	return ""
}

func isLetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

// unescapeMount undoes the mount table's octal escapes (\040 for a space,
// \134 for a backslash).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			n, _ := strconv.ParseUint(s[i+1:i+4], 8, 8)
			b.WriteByte(byte(n))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(b byte) bool { return b >= '0' && b <= '7' }

// mountPoints are the drives' mount points, longest first (a drive mounted
// inside another folder of drives is found before it).
func (i Info) mountPoints() []string {
	out := make([]string, 0, len(i.Drives))
	for m := range i.Drives {
		out = append(out, m)
	}
	sort.Slice(out, func(a, b int) bool {
		if len(out[a]) != len(out[b]) {
			return len(out[a]) > len(out[b])
		}
		return out[a] < out[b]
	})
	return out
}
