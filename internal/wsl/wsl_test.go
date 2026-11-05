package wsl

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// What WSL leaves on a computer, as files under a stand-in root. Kernels and
// mount lines are copied from real computers.
const (
	wsl2Release = "6.6.87.2-microsoft-standard-WSL2"
	wsl2Version = "Linux version 6.6.87.2-microsoft-standard-WSL2 (root@439a258ad544) (gcc (GCC) 11.2.0, GNU ld (GNU Binutils) 2.37) #1 SMP PREEMPT_DYNAMIC Thu Jun  5 18:30:46 UTC 2025"
	wsl1Release = "4.4.0-19041-Microsoft"
	wsl1Version = "Linux version 4.4.0-19041-Microsoft (Microsoft@Microsoft.com) (gcc version 5.4.0 (GCC) ) #1237-Microsoft Sat Sep 11 14:32:00 PST 2021"
	ubuntuGA    = "6.8.0-134-generic"
	ubuntuVer   = "Linux version 6.8.0-134-generic (buildd@lcy02-amd64-012) (x86_64-linux-gnu-gcc-13 (Ubuntu 13.3.0-6ubuntu2~24.04) 13.3.0) #134-Ubuntu SMP PREEMPT_DYNAMIC"
	// WSL 2's own mount table: C: and D: over 9p, then a virtiofs drive and
	// one whose folder has a space in it.
	wsl2Mounts = `none /usr/lib/wsl/drivers 9p ro,nosuid,nodev,noatime,dirsync,aname=drivers;fmask=222;dmask=222,mmap,access=client,msize=65536,trans=fd,rfd=7,wfd=7 0 0
/dev/sdc / ext4 rw,relatime,discard,errors=remount-ro,data=ordered 0 0
C:\134 /mnt/c 9p rw,noatime,dirsync,aname=drvfs;path=C:\;uid=1000;gid=1000;symlinkroot=/mnt/,mmap,access=client,msize=65536,trans=fd,rfd=5,wfd=5 0 0
D:\134 /mnt/d 9p rw,noatime,dirsync,aname=drvfs;path=D:\;uid=1000;gid=1000;symlinkroot=/mnt/,mmap,access=client,msize=65536,trans=fd,rfd=5,wfd=5 0 0
drvfsaE /mnt/e virtiofs rw,noatime,dirsync 0 0
G:\134 /mnt/google\040drive drvfs rw,noatime,uid=1000,gid=1000 0 0
`
	ubuntuOSRelease = "PRETTY_NAME=\"Ubuntu 24.04.3 LTS\"\nNAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nID=ubuntu\n"
)

// computer lays out files (relative path → contents; a path ending in "/" is
// a folder) under a new root.
func computer(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if strings.HasSuffix(rel, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// wsl2Files is Ubuntu in WSL 2 with systemd on and running.
func wsl2Files() map[string]string {
	return map[string]string{
		"proc/sys/kernel/osrelease":           wsl2Release + "\n",
		"proc/version":                        wsl2Version + "\n",
		"proc/sys/fs/binfmt_misc/WSLInterop":  "enabled\ninterpreter /init\nflags: PF\noffset 0\nmagic 4d5a\n",
		"proc/1/comm":                         "systemd\n",
		"proc/self/mounts":                    wsl2Mounts,
		"run/WSL/":                            "",
		"run/systemd/system/":                 "",
		"etc/wsl.conf":                        "[boot]\nsystemd=true\n\n[user]\ndefault=you\n",
		"etc/os-release":                      ubuntuOSRelease,
		"mnt/c/Users/Sam/":                    "",
		"mnt/c/Users/Public/":                 "",
		"mnt/c/Users/Default/":                "",
		"mnt/c/Windows/System32/cmd.exe":      "MZ",
		"mnt/c/Users/Sam/.wslconfig":          "[general]\ninstanceIdleTimeout=-1\n",
		"mnt/c/Users/Sam/OneDrive - Contoso/": "",
	}
}

func with(files map[string]string, change map[string]string, drop ...string) map[string]string {
	out := map[string]string{}
	for k, v := range files {
		out[k] = v
	}
	for k, v := range change {
		out[k] = v
	}
	for _, d := range drop {
		delete(out, d)
	}
	return out
}

var ubuntuEnv = map[string]string{"WSL_DISTRO_NAME": "Ubuntu", "WSL_INTEROP": "/run/WSL/1234_interop", "USER": "you"}

func TestDetect(t *testing.T) {
	drives := map[string]string{"/mnt/c": "c", "/mnt/d": "d", "/mnt/e": "e", "/mnt/google drive": "g"}
	for _, c := range []struct {
		name  string
		files map[string]string
		env   map[string]string
		goos  string
		want  Info
	}{
		{name: "a Mac", files: wsl2Files(), env: ubuntuEnv, goos: "darwin", want: Info{}},
		{name: "plain Linux", files: map[string]string{"proc/sys/kernel/osrelease": ubuntuGA, "proc/version": ubuntuVer, "proc/1/comm": "systemd",
			"run/systemd/system/": "", "etc/os-release": ubuntuOSRelease}, env: map[string]string{}, goos: "linux", want: Info{}},
		{name: "WSL 2 with systemd", files: wsl2Files(), env: ubuntuEnv, goos: "linux",
			want: Info{Version: 2, Distro: "Ubuntu", DistroKnown: true, Name: "Ubuntu", Kernel: wsl2Release, Interop: true, Systemd: true, SystemdAsked: true, DriveRoot: "/mnt/", Drives: drives}},
		{name: "WSL 2 without systemd", files: with(wsl2Files(), map[string]string{"proc/1/comm": "init\n", "etc/wsl.conf": "[user]\ndefault=you\n"}, "run/systemd/system/"),
			env: ubuntuEnv, goos: "linux",
			want: Info{Version: 2, Distro: "Ubuntu", DistroKnown: true, Name: "Ubuntu", Kernel: wsl2Release, Interop: true, DriveRoot: "/mnt/", Drives: drives}},
		{name: "WSL 2, systemd asked but WSL not restarted", files: with(wsl2Files(), map[string]string{"proc/1/comm": "init\n"}, "run/systemd/system/"),
			env: ubuntuEnv, goos: "linux",
			want: Info{Version: 2, Distro: "Ubuntu", DistroKnown: true, Name: "Ubuntu", Kernel: wsl2Release, Interop: true, SystemdAsked: true, DriveRoot: "/mnt/", Drives: drives}},
		{name: "WSL 1", files: map[string]string{"proc/sys/kernel/osrelease": wsl1Release, "proc/version": wsl1Version,
			"proc/sys/fs/binfmt_misc/WSLInterop": "enabled\n", "proc/1/comm": "init", "etc/os-release": ubuntuOSRelease},
			env: map[string]string{"WSL_DISTRO_NAME": "Ubuntu-20.04"}, goos: "linux",
			want: Info{Version: 1, Distro: "Ubuntu-20.04", DistroKnown: true, Name: "Ubuntu", Kernel: wsl1Release, Interop: true, DriveRoot: "/mnt/", Drives: map[string]string{}}},
		{name: "a container sharing WSL 2's kernel", files: map[string]string{"proc/sys/kernel/osrelease": wsl2Release, "proc/version": wsl2Version, "etc/os-release": ubuntuOSRelease},
			env: map[string]string{}, goos: "linux", want: Info{}},
		{name: "MetaRush in the background: no WSL variables, /run/WSL there", files: wsl2Files(), env: map[string]string{}, goos: "linux",
			want: Info{Version: 2, Distro: "Ubuntu", Name: "Ubuntu", Kernel: wsl2Release, Interop: true, Systemd: true, SystemdAsked: true, DriveRoot: "/mnt/", Drives: drives}},
		{name: "WSL 2 with a kernel of its own", files: with(wsl2Files(), map[string]string{"proc/sys/kernel/osrelease": "6.12.9-custom", "proc/version": "Linux version 6.12.9-custom (you@pc)"}),
			env: ubuntuEnv, goos: "linux",
			want: Info{Version: 2, Distro: "Ubuntu", DistroKnown: true, Name: "Ubuntu", Kernel: "6.12.9-custom", Interop: true, Systemd: true, SystemdAsked: true, DriveRoot: "/mnt/", Drives: drives}},
		{name: "interop off, drives at the root", files: with(wsl2Files(), map[string]string{"etc/wsl.conf": "[automount]\nroot = /\n[interop]\nenabled=false\n",
			"proc/self/mounts": "C:\\134 /c 9p rw,aname=drvfs;path=C:\\;uid=1000 0 0\n"}, "proc/sys/fs/binfmt_misc/WSLInterop"),
			env: ubuntuEnv, goos: "linux",
			want: Info{Version: 2, Distro: "Ubuntu", DistroKnown: true, Name: "Ubuntu", Kernel: wsl2Release, Systemd: true, DriveRoot: "/", Drives: map[string]string{"/c": "c"}}},
		{name: "interop entry disabled", files: with(wsl2Files(), map[string]string{"proc/sys/fs/binfmt_misc/WSLInterop": "disabled\n"}), env: ubuntuEnv, goos: "linux",
			want: Info{Version: 2, Distro: "Ubuntu", DistroKnown: true, Name: "Ubuntu", Kernel: wsl2Release, Systemd: true, SystemdAsked: true, DriveRoot: "/mnt/", Drives: drives}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := Probe{Root: computer(t, c.files), Env: c.env, GOOS: c.goos}.Detect()
			if got.Drives == nil && c.want.Drives != nil && len(c.want.Drives) == 0 {
				got.Drives = map[string]string{}
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got  %+v\nwant %+v", got, c.want)
			}
			if got.In() != (c.want.Version > 0) {
				t.Fatalf("In() = %v", got.In())
			}
		})
	}
}

// WSL's settings files are read the way WSL reads them: any case, quotes,
// comments, values with spaces.
func TestParseConf(t *testing.T) {
	c := ParseConf("\ufeff# made by hand\n[Boot]\nSystemd = TRUE ; on\n[automount]\nroot = \"/windir/\"\noptions = \"metadata,uid=1003\"\n[general]\ninstanceIdleTimeout=-1\n[wsl2]\nnetworkingMode=Mirrored\nmemory=8GB # half\n")
	if !c.Bool("boot", "systemd", false) || c.Get("automount", "root") != "/windir/" || c.Get("AUTOMOUNT", "Options") != "metadata,uid=1003" {
		t.Fatalf("conf %+v", c)
	}
	if n, ok := c.Int("general", "instanceIdleTimeout"); !ok || n != -1 {
		t.Fatalf("idle %d %v", n, ok)
	}
	if c.Get("wsl2", "memory") != "8GB" || c.Get("wsl2", "networkingmode") != "Mirrored" {
		t.Fatalf("wsl2 %+v", c["wsl2"])
	}
	if c.Bool("interop", "enabled", true) != true || c.Bool("boot", "nothing", false) {
		t.Fatal("defaults")
	}
}

func TestParseDrives(t *testing.T) {
	got := parseDrives(wsl2Mounts + "tmpfs /run tmpfs rw 0 0\n\\\\server\\134share /mnt/share 9p rw,aname=drvfs;path=\\\\server\\share 0 0\n")
	want := map[string]string{"/mnt/c": "c", "/mnt/d": "d", "/mnt/e": "e", "/mnt/google drive": "g"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("drives %v", got)
	}
}

// playing makes Here answer i for the rest of the test.
func playing(t *testing.T, i Info) {
	t.Helper()
	t.Cleanup(Pretend(i))
}

func ubuntu(c string) Info {
	return Info{Version: 2, Distro: "Ubuntu", DistroKnown: true, Interop: true, DriveRoot: "/mnt/", Drives: map[string]string{c: "c", "/mnt/d": "d"}}
}

// A path typed or pasted from Windows opens where WSL shows it, by the same
// rule as wslpath; other paths come back as typed.
func TestLinuxPath(t *testing.T) {
	i := ubuntu("/mnt/c")
	for typed, want := range map[string]string{
		`C:\Users\Sam\Videos\clip.mp4`:        "/mnt/c/Users/Sam/Videos/clip.mp4",
		`"C:\Users\Sam\My Videos\take 2.mp4"`: "/mnt/c/Users/Sam/My Videos/take 2.mp4", // File Explorer's "Copy as path"
		` c:/Users/Sam/Videos `:               "/mnt/c/Users/Sam/Videos",
		`C:`:                                  "/mnt/c",
		`C:\`:                                 "/mnt/c",
		`d:\Footage\..\Exports\cut.otio`:      "/mnt/d/Exports/cut.otio",
		`\\wsl.localhost\Ubuntu\home\you\clip.mp4`: "/home/you/clip.mp4",
		`\\wsl$\ubuntu\home\you`:                   "/home/you",
		`//wsl.localhost/Ubuntu/tmp`:               "/tmp",
		"/home/you/Videos/clip.mp4":                "/home/you/Videos/clip.mp4",
		"~/Videos":                                 "~/Videos",
		`"/home/you/a b.mp4"`:                      "/home/you/a b.mp4",
		`Videos\clip.mp4`:                          `Videos\clip.mp4`, // relative: not a Windows path to translate
	} {
		got, err := i.LinuxPath(typed)
		if err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", typed, got, err, want)
		}
	}
	for typed, says := range map[string]string{
		`E:\Footage\clip.mp4`:                   "Drive E: isn't visible in Ubuntu",
		`\\wsl.localhost\Debian\home\you\x.mp4`: "another Linux on this PC (Debian)",
		`\\nas\footage\clip.mp4`:                "network folder",
	} {
		_, err := i.LinuxPath(typed)
		pe, ok := err.(*PathError)
		if !ok || !strings.Contains(pe.Plain, says) || pe.Next == "" {
			t.Errorf("%q: %v", typed, err)
		}
	}
	// Outside WSL nothing is translated.
	if got, err := (Info{}).LinuxPath(`C:\Users\Sam`); err != nil || got != `C:\Users\Sam` {
		t.Fatalf("outside WSL: %q %v", got, err)
	}
	// Drives at the root (wsl.conf's automount root "/"), and no mount table.
	root := Info{Version: 2, DriveRoot: "/"}
	if got, _ := root.LinuxPath(`C:\x`); got != "/c/x" {
		t.Fatalf("root /: %q", got)
	}
	if got, err := root.LinuxPath(`Q:\x`); err != nil || got != "/q/x" {
		t.Fatalf("no mount table: a guess, not a refusal: %q %v", got, err)
	}
}

func TestDriveAndWindowsPath(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir()) // a Mac's /var is /private/var
	if err != nil {
		t.Fatal(err)
	}
	c := filepath.Join(tmp, "mnt", "c")
	if err := os.MkdirAll(filepath.Join(c, "Users", "Sam", "Videos"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "home", "you", "WinVideos")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(c, "Users", "Sam", "Videos"), link); err != nil {
		t.Fatal(err)
	}
	i := ubuntu(c)
	for p, want := range map[string]string{
		filepath.Join(c, "Users", "Sam", "Videos", "clip.mp4"): "c",
		c:                        "c",
		"/mnt/d/Exports":         "d",
		"/mnt/dd/x":              "",
		"/home/you/Videos":       "",
		filepath.Join(link, "x"): "c", // a link into a drive counts as the drive
	} {
		if got := i.Drive(p); got != want {
			t.Errorf("%s: %q, want %q", p, got, want)
		}
	}
	if (Info{}).OnWindows("/mnt/c/Users") {
		t.Fatal("outside WSL nothing is on the Windows side")
	}
	if !i.UnderDrive("/mnt/d/Program Files/x") || i.UnderDrive("/usr/bin") || i.UnderDrive("relative/x") {
		t.Fatal("UnderDrive")
	}
	for p, want := range map[string]string{
		filepath.Join(c, "Users", "Sam", "x.mp4"): `C:\Users\Sam\x.mp4`,
		c:                  `C:\`,
		"/home/you/x.mp4":  `\\wsl.localhost\Ubuntu\home\you\x.mp4`,
		"/tmp/open/x.html": `\\wsl.localhost\Ubuntu\tmp\open\x.html`,
	} {
		if got, ok := i.WindowsPath(p); !ok || got != want {
			t.Errorf("%s: %q %v, want %q", p, got, ok, want)
		}
	}
	i.DistroKnown = false
	if _, ok := i.WindowsPath("/home/you"); ok {
		t.Fatal("Ubuntu's own files need the distribution's name")
	}
}

// A Windows user's folder is found by the path alone: inside WSL and on any
// Linux where Windows' drive is mounted under /mnt.
func TestWindowsUserFolder(t *testing.T) {
	playing(t, Info{})
	for p, want := range map[string][2]string{
		"/mnt/c/Users/Sam/OneDrive/Clips":     {"/mnt/c/Users/Sam", "OneDrive"},
		"/mnt/c/users/you/Dropbox (Personal)": {"/mnt/c/users/you", "Dropbox (Personal)"},
		"/mnt/c/Users/Sam":                    {"/mnt/c/Users/Sam", ""},
	} {
		root, first, ok := WindowsUserFolder(p)
		if !ok || root != want[0] || first != want[1] {
			t.Errorf("%s: %q %q %v", p, root, first, ok)
		}
	}
	for _, p := range []string{"/mnt/c/Users/Public/Dropbox", "/mnt/c/Users", "/mnt/c/Program Files/x", "/home/you/OneDrive", "/mnt/cc/Users/Sam/x"} {
		if _, _, ok := WindowsUserFolder(p); ok {
			t.Errorf("%s isn't in a person's Windows folder", p)
		}
	}
	playing(t, Info{Version: 2, DriveRoot: "/", Drives: map[string]string{"/c": "c", "/g": "g"}})
	if root, first, ok := WindowsUserFolder("/c/Users/Sam/Box/x"); !ok || root != "/c/Users/Sam" || first != "Box" {
		t.Fatalf("drives at the root: %q %q %v", root, first, ok)
	}
	if m, first, ok := WindowsDriveFolder("/g/My Drive/Clips"); !ok || m != "/g" || first != "My Drive" {
		t.Fatalf("Google Drive's drive: %q %q %v", m, first, ok)
	}
}

func TestWindowsHomeAndSettings(t *testing.T) {
	root := computer(t, wsl2Files())
	p := Probe{Root: root, Env: ubuntuEnv, GOOS: "linux"}
	i := p.Detect()
	if h := p.WindowsHome(i); h != "/mnt/c/Users/Sam" {
		t.Fatalf("the only person's folder: %q", h)
	}
	ws := p.ReadWindowsSettings(i)
	if !ws.Found || ws.IdleTimeoutMS == nil || *ws.IdleTimeoutMS != -1 || ws.Networking != "" || ws.LocalhostOff {
		t.Fatalf("settings %+v", ws)
	}
	// Two people: the one named like this Linux user; neither: nobody.
	two := computer(t, with(wsl2Files(), map[string]string{"mnt/c/Users/Alex/": "", "mnt/c/Users/you/.wslconfig": "[wsl2]\nnetworkingMode=mirrored\nlocalhostForwarding=false\n"}, "mnt/c/Users/Sam/.wslconfig", "mnt/c/Users/Sam/", "mnt/c/Users/Sam/OneDrive - Contoso/"))
	p2 := Probe{Root: two, Env: ubuntuEnv, GOOS: "linux"}
	if h := p2.WindowsHome(p2.Detect()); h != "/mnt/c/Users/you" {
		t.Fatalf("named like this user: %q", h)
	}
	ws2 := p2.ReadWindowsSettings(p2.Detect())
	if ws2.Networking != "mirrored" || !ws2.LocalhostOff || ws2.IdleTimeoutMS != nil {
		t.Fatalf("settings %+v", ws2)
	}
	p3 := Probe{Root: two, Env: map[string]string{"WSL_DISTRO_NAME": "Ubuntu", "USER": "jo"}, GOOS: "linux"}
	if h := p3.WindowsHome(p3.Detect()); h != "" {
		t.Fatalf("can't tell: %q", h)
	}
	// USERPROFILE passed on by WSLENV wins.
	p4 := Probe{Root: two, Env: map[string]string{"WSL_DISTRO_NAME": "Ubuntu", "USERPROFILE": `C:\Users\Alex`}, GOOS: "linux"}
	if h := p4.WindowsHome(p4.Detect()); h != "/mnt/c/Users/Alex" {
		t.Fatalf("USERPROFILE: %q", h)
	}
}
