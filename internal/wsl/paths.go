package wsl

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Paths between Windows and Ubuntu (D520). Windows' drives appear in WSL as
// folders (C: is /mnt/c by default, wsl.conf's automount root otherwise), and
// Ubuntu's own files appear in Windows as \\wsl.localhost\<distro>\…. A path
// a person types or pastes from Windows — C:\Users\Sam\Videos, the same in
// quotes as File Explorer's "Copy as path" gives it, C:/Users/…, or
// \\wsl.localhost\Ubuntu\home\you\… — is turned into the path WSL shows it
// at by the same rule as `wslpath`, in Go: no program runs for it.

var (
	drivePathRe = regexp.MustCompile(`^([A-Za-z]):(?:[\\/](.*))?$`)
	wslUNCRe    = regexp.MustCompile(`(?i)^(?:\\\\|//)(?:wsl\.localhost|wsl\$)[\\/]+([^\\/]+)(?:[\\/]+(.*))?$`)
	uncRe       = regexp.MustCompile(`^(?:\\\\|//)[^\\/]+[\\/]`)
)

// PathError is a typed path MetaRush can't open from Ubuntu as it is, said in
// plain words (Plain, Next); Command is the technical step behind Details.
type PathError struct {
	Typed   string
	Plain   string
	Next    string
	Command string
}

func (e *PathError) Error() string { return e.Plain }

// mountFor is where drive letter (lower case) is mounted: the mount table's
// place for it (the automount root's first), else the automount root's.
func (i Info) mountFor(letter string) (string, bool) {
	want := filepath.Clean(i.driveRoot() + letter)
	found := ""
	for m, l := range i.Drives {
		if l != letter {
			continue
		}
		if m == want {
			return m, true
		}
		if found == "" || m < found {
			found = m
		}
	}
	if found != "" {
		return found, true
	}
	return want, len(i.Drives) == 0 // not in the table: only a guess when there is no table
}

func (i Info) driveRoot() string {
	if i.DriveRoot == "" {
		return "/mnt/"
	}
	return i.DriveRoot
}

// Unquote takes off the double quotes File Explorer's "Copy as path" puts
// around a path (and surrounding spaces).
func Unquote(typed string) string {
	s := strings.TrimSpace(typed)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

// LinuxPath is the path on this computer for what a person typed. Inside WSL
// a Windows path becomes the place WSL shows it (C:\Users\Sam → /mnt/c/Users/Sam;
// \\wsl.localhost\<this distribution>\home\you → /home/you) and quotes from
// "Copy as path" go; outside WSL, and for anything that isn't a Windows path,
// the text comes back as it was typed. A drive WSL doesn't show (one plugged
// in after it started), another distribution's files and network folders are
// a *PathError in plain words.
func (i Info) LinuxPath(typed string) (string, error) {
	if !i.In() {
		return typed, nil
	}
	s := Unquote(typed)
	if m := drivePathRe.FindStringSubmatch(s); m != nil {
		letter := strings.ToLower(m[1])
		base, mounted := i.mountFor(letter)
		if !mounted {
			return "", &PathError{Typed: typed, Plain: "Drive " + strings.ToUpper(letter) + ": isn't visible in Ubuntu: WSL shows only the drives it found when it started.",
				Next:    "Restart WSL (shut it down from Windows, then open Ubuntu again), or mount the drive in Ubuntu (docs/wsl.md says how).",
				Command: "sudo mkdir -p " + i.driveRoot() + letter + " && sudo mount -t drvfs " + strings.ToUpper(letter) + ": " + i.driveRoot() + letter}
		}
		rest := strings.ReplaceAll(m[2], `\`, "/")
		return path.Join(base, path.Clean("/"+rest)), nil
	}
	if m := wslUNCRe.FindStringSubmatch(s); m != nil {
		if i.DistroKnown && !strings.EqualFold(m[1], i.Distro) {
			return "", &PathError{Typed: typed, Plain: "That file is in another Linux on this PC (" + m[1] + "), which MetaRush in " + i.distroWord() + " can't open.",
				Next: "Copy it into your home folder in " + i.distroWord() + " first, then add it from there."}
		}
		return path.Clean("/" + strings.ReplaceAll(m[2], `\`, "/")), nil
	}
	if uncRe.MatchString(s) {
		return "", &PathError{Typed: typed, Plain: "That's a network folder's Windows address, which MetaRush in Ubuntu can't open as it is.",
			Next:    "Copy the files into your Linux home folder first, or mount the network folder in Ubuntu (docs/wsl.md says how).",
			Command: "sudo mount -t drvfs '" + s + "' /mnt/share"}
	}
	return s, nil
}

// HereLinuxPath is LinuxPath on this computer.
func HereLinuxPath(typed string) (string, error) { return Here().LinuxPath(typed) }

// Drive is the Windows drive p is on as WSL shows it ("c" for /mnt/c/…; ""
// on Ubuntu's own disk, or outside WSL). A link into a drive counts as the
// drive it leads to (files there are read the same slow way).
func (i Info) Drive(p string) string {
	if !i.In() || p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	if l := i.driveOf(abs); l != "" {
		return l
	}
	if real := resolveExisting(abs); real != abs {
		return i.driveOf(real)
	}
	return ""
}

func (i Info) driveOf(abs string) string {
	for _, m := range i.mountPoints() {
		if abs == m || strings.HasPrefix(abs, m+"/") {
			return i.Drives[m]
		}
	}
	if len(i.Drives) > 0 {
		return ""
	}
	// No mount table (it couldn't be read): the automount root's spelling.
	root := i.driveRoot()
	if rest, ok := strings.CutPrefix(abs, root); ok && len(rest) >= 1 && isLetter(rest[0]) && (len(rest) == 1 || rest[1] == '/') {
		return strings.ToLower(rest[:1])
	}
	return ""
}

// UnderDrive reports whether the absolute path p is on one of Windows'
// drives by its spelling alone (no links followed): cheap enough for every
// folder on PATH.
func (i Info) UnderDrive(p string) bool {
	return i.In() && filepath.IsAbs(p) && i.driveOf(filepath.Clean(p)) != ""
}

// OnWindows reports whether p is on one of Windows' drives (inside WSL): a
// slower place for MetaRush's files (D521).
func (i Info) OnWindows(p string) bool { return i.Drive(p) != "" }

// HereOnWindows is OnWindows on this computer.
func HereOnWindows(p string) bool { return Here().OnWindows(p) }

// WindowsPath is how Windows names p: C:\Users\Sam\x for /mnt/c/Users/Sam/x,
// and \\wsl.localhost\<distribution>\home\you\x for Ubuntu's own files (when
// WSL said the distribution's name). ok is false outside WSL.
func (i Info) WindowsPath(p string) (string, bool) {
	if !i.In() || !filepath.IsAbs(p) {
		return "", false
	}
	p = filepath.Clean(p)
	for _, m := range i.mountPoints() {
		if p == m || strings.HasPrefix(p, m+"/") {
			return strings.ToUpper(i.Drives[m]) + `:\` + strings.ReplaceAll(strings.TrimPrefix(strings.TrimPrefix(p, m), "/"), "/", `\`), true
		}
	}
	if l := i.driveOf(p); l != "" && len(i.Drives) == 0 {
		rest := strings.TrimPrefix(strings.TrimPrefix(p, i.driveRoot()+l), "/")
		return strings.ToUpper(l) + `:\` + strings.ReplaceAll(rest, "/", `\`), true
	}
	if !i.DistroKnown {
		return "", false
	}
	return `\\wsl.localhost\` + i.Distro + strings.ReplaceAll(p, "/", `\`), true
}

// resolveExisting resolves links in the longest part of p that exists.
func resolveExisting(p string) string {
	dir, rest := p, ""
	for {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			if rest == "" {
				return real
			}
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return p
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

/* ---------- Windows users' folders ---------- */

// systemProfiles are folders in C:\Users that aren't a person's.
var systemProfiles = map[string]bool{"all users": true, "default": true, "default user": true, "public": true, "defaultuser0": true,
	"wdagutilityaccount": true, "wsiaccount": true, "desktop.ini": true}

// WindowsUserFolder finds the Windows user folder p is in, by the path alone
// (inside WSL, or on any Linux where a Windows drive is mounted under /mnt):
// /mnt/c/Users/Sam/OneDrive/Clips is in Sam's folder (root /mnt/c/Users/Sam)
// and first is "OneDrive", the folder in it ("" for the user folder itself).
// Windows' names are any case.
func WindowsUserFolder(p string) (root, first string, ok bool) {
	mounts := []string{}
	if i := Here(); i.In() {
		mounts = append(mounts, i.mountPoints()...)
	}
	abs := filepath.Clean(p)
	for _, m := range mounts {
		if r, f, ok := userFolderUnder(abs, m); ok {
			return r, f, true
		}
	}
	// The usual place (/mnt/<letter>), also outside WSL.
	if rest, ok := strings.CutPrefix(abs, "/mnt/"); ok && len(rest) >= 1 && isLetter(rest[0]) && (len(rest) == 1 || rest[1] == '/') {
		return userFolderUnder(abs, "/mnt/"+rest[:1])
	}
	return "", "", false
}

func userFolderUnder(abs, mount string) (root, first string, ok bool) {
	rest, found := strings.CutPrefix(abs, mount+"/")
	if !found {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || !strings.EqualFold(parts[0], "Users") || parts[1] == "" || systemProfiles[strings.ToLower(parts[1])] {
		return "", "", false
	}
	root = mount + "/" + parts[0] + "/" + parts[1]
	if len(parts) > 2 {
		first = parts[2]
	}
	return root, first, true
}

// WindowsDriveFolder is the folder at the top of a Windows drive p is in
// (/mnt/g/My Drive/x → "My Drive"), by the path alone like WindowsUserFolder.
func WindowsDriveFolder(p string) (mount, first string, ok bool) {
	abs := filepath.Clean(p)
	mounts := []string{}
	if i := Here(); i.In() {
		mounts = append(mounts, i.mountPoints()...)
	}
	if rest, ok := strings.CutPrefix(abs, "/mnt/"); ok && len(rest) >= 1 && isLetter(rest[0]) && (len(rest) == 1 || rest[1] == '/') {
		mounts = append(mounts, "/mnt/"+rest[:1])
	}
	for _, m := range mounts {
		if rest, ok := strings.CutPrefix(abs, m+"/"); ok {
			f, _, _ := strings.Cut(rest, "/")
			return m, f, f != ""
		}
	}
	return "", "", false
}

// WindowsHome is the Windows user's own folder as WSL shows it
// (/mnt/c/Users/Sam), "" when it can't be told: USERPROFILE when WSL passes
// it on (WSLENV), else the only person's folder in C:\Users, else the one
// named like this Linux user.
func (p Probe) WindowsHome(i Info) string {
	if !i.In() {
		return ""
	}
	if up := strings.TrimSpace(p.getenv("USERPROFILE")); up != "" {
		if lp, err := i.LinuxPath(up); err == nil && filepath.IsAbs(lp) {
			if st, err := os.Stat(p.path(lp)); err == nil && st.IsDir() {
				return lp
			}
		}
	}
	c, _ := i.mountFor("c")
	users := path.Join(c, "Users")
	ents, err := os.ReadDir(p.path(users))
	if err != nil {
		return ""
	}
	var people []string
	for _, e := range ents {
		n := e.Name()
		if !e.IsDir() || strings.HasPrefix(n, ".") || systemProfiles[strings.ToLower(n)] {
			continue
		}
		people = append(people, n)
	}
	if len(people) == 1 {
		return path.Join(users, people[0])
	}
	me := strings.ToLower(firstNonEmpty(p.getenv("USER"), p.getenv("LOGNAME")))
	for _, n := range people {
		if me != "" && strings.ToLower(n) == me {
			return path.Join(users, n)
		}
	}
	return ""
}

var (
	homeOnce sync.Once
	homeHere string
)

// HereWindowsHome is WindowsHome on this computer (looked for once).
func HereWindowsHome() string {
	i := Here()
	mu.Lock()
	pretending := pretend != nil
	mu.Unlock()
	if pretending {
		return Probe{}.WindowsHome(i)
	}
	homeOnce.Do(func() { homeHere = Probe{}.WindowsHome(i) })
	return homeHere
}

// InWindowsHome reports whether p is inside the Windows user's own folder
// (the folder itself counts), so the dashboard may choose a folder there as
// it may in the Linux home (D521).
func InWindowsHome(p string) bool {
	h := HereWindowsHome()
	if h == "" || p == "" {
		return false
	}
	h, p = strings.ToLower(filepath.Clean(h)), strings.ToLower(filepath.Clean(p))
	return p == h || strings.HasPrefix(p, h+"/")
}

/* ---------- Windows' own WSL settings ---------- */

// WindowsSettings is what Windows' WSL settings file (.wslconfig in the
// Windows user's folder, which the WSL Settings app writes too) says about
// what MetaRush cares for.
type WindowsSettings struct {
	// Found: the file was found and read.
	Found bool `json:"found"`
	// IdleTimeoutMS is [general] instanceIdleTimeout: how long WSL keeps
	// Ubuntu running once its last window has closed (-1: always; nil:
	// WSL's own default of 15 seconds).
	IdleTimeoutMS *int64 `json:"idleTimeoutMs,omitempty"`
	// Networking is [wsl2] networkingMode ("" is NAT, WSL's default;
	// "mirrored" shares Windows' network).
	Networking string `json:"networking,omitempty"`
	// LocalhostOff: [wsl2] localhostForwarding=false, so in NAT mode Windows'
	// browser can't reach 127.0.0.1 in Ubuntu.
	LocalhostOff bool `json:"localhostOff,omitempty"`
}

// ReadWindowsSettings reads the Windows user's .wslconfig, when it is found.
func (p Probe) ReadWindowsSettings(i Info) WindowsSettings {
	home := p.WindowsHome(i)
	if home == "" {
		return WindowsSettings{}
	}
	b, err := os.ReadFile(p.path(path.Join(home, ".wslconfig")))
	if err != nil {
		return WindowsSettings{}
	}
	return parseWindowsSettings(b)
}

func parseWindowsSettings(b []byte) WindowsSettings {
	c := ParseConf(string(b))
	ws := WindowsSettings{Found: true, Networking: strings.ToLower(c.Get("wsl2", "networkingMode"))}
	if n, ok := c.Int("general", "instanceIdleTimeout"); ok {
		ws.IdleTimeoutMS = &n
	}
	ws.LocalhostOff = !c.Bool("wsl2", "localhostForwarding", true)
	return ws
}

// HereWindowsSettings reads this computer's.
func HereWindowsSettings() WindowsSettings {
	i := Here()
	if !i.In() {
		return WindowsSettings{}
	}
	h := HereWindowsHome()
	if h == "" {
		return WindowsSettings{}
	}
	b, err := os.ReadFile(path.Join(h, ".wslconfig"))
	if err != nil {
		return WindowsSettings{}
	}
	return parseWindowsSettings(b)
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
