package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// fakeLaunchctl records its calls and keeps a "loaded" flag like launchd.
const fakeLaunchctl = `#!/bin/sh
echo "$*" >> "$D/calls.log"
case "$1" in
bootout) [ -f "$D/loaded" ] || { echo "Boot-out failed: 3: No such process" >&2; exit 3; }; rm -f "$D/loaded" ;;
bootstrap) touch "$D/loaded" ;;
enable) ;;
print) [ -f "$D/loaded" ] || { echo "Could not find service" >&2; exit 113; }
  printf '%s\n' "gui/501/x = {" "	state = running" "	pid = 4242" "	last exit code = 0" "}" ;;
kickstart) [ -f "$D/loaded" ] || exit 113 ;;
*) exit 64 ;;
esac
`

// fakeSystemctl records its calls and answers `show` like systemd.
const fakeSystemctl = `#!/bin/sh
echo "$*" >> "$D/calls.log"
case "$2" in
show) printf 'LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=777\nExecMainStatus=0\nUnitFileState=enabled\n' ;;
esac
`

func fakeTool(t *testing.T, name, body string) (string, string) {
	t.Helper()
	d := t.TempDir()
	p := filepath.Join(d, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nD='"+d+"'\n"+strings.TrimPrefix(body, "#!/bin/sh\n")), 0o755); err != nil {
		t.Fatal(err)
	}
	return p, d
}

func calls(t *testing.T, dir string) []string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func exeFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bin & tools", "metarush")
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// The LaunchAgent: written under the person's (here: a temporary) home
// folder, loaded with bootstrap, read back by status, restarted with
// kickstart and removed again, all through a fake launchctl.
func TestLaunchAgentInstallStatusRestartUninstall(t *testing.T) {
	lc, dir := fakeTool(t, "launchctl", fakeLaunchctl)
	userHome := t.TempDir()
	o := Options{Home: filepath.Join(t.TempDir(), "mr home"), Exe: exeFile(t), OS: "darwin", UserHome: userHome, UID: 501,
		Tools: tools.New(config.Tools{Launchctl: lc})}
	label := o.Label()
	if !strings.HasPrefix(label, "metarush.daemon.") || o.File() != filepath.Join(userHome, "Library", "LaunchAgents", label+".plist") {
		t.Fatalf("label %s file %s", label, o.File())
	}
	ctx := context.Background()
	plan, err := o.Install(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(o.File())
	if err != nil || string(b) != plan.Content {
		t.Fatalf("plist not written: %v", err)
	}
	for _, want := range []string{"<string>" + label + "</string>", "<string>serve</string>", "<string>--home</string>", "<string>" + o.Home + "</string>",
		"<key>RunAtLoad</key>\n\t<true/>", "<key>SuccessfulExit</key>\n\t\t<false/>", filepath.Join(o.Home, "logs", "daemon.log"), "bin &amp; tools", "<key>METARUSH_SERVICE</key>"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("plist lacks %q", want)
		}
	}
	if strings.Contains(string(b), "--worker-only") {
		t.Error("full mode runs the dashboard")
	}
	if runtime.GOOS == "darwin" {
		if plutil, err := exec.LookPath("plutil"); err == nil {
			if out, err := exec.Command(plutil, "-lint", o.File()).CombinedOutput(); err != nil {
				t.Fatalf("plutil: %s", out)
			}
		}
	}
	target := "gui/501/" + label
	if got := strings.Join(calls(t, dir), "|"); got != "bootout "+target+"|enable "+target+"|bootstrap gui/501 "+o.File() {
		t.Fatalf("launchctl calls: %s", got)
	}
	st := o.Get(ctx)
	if !st.Installed || !st.Loaded || !st.Running || st.PID != 4242 || !st.SameBinary || st.Binary != o.Exe || st.WorkerOnly || st.Problem != "" || st.Platform != Launchd {
		t.Fatalf("status: %+v", st)
	}
	if err := o.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	if c := calls(t, dir); c[len(c)-1] != "kickstart -k "+target {
		t.Fatalf("restart: %v", c)
	}
	// Installing again replaces it (bootout of the loaded copy first).
	o.WorkerOnly = true
	if _, err := o.Install(ctx); err != nil {
		t.Fatal(err)
	}
	if st := o.Get(ctx); !st.WorkerOnly || !st.Running {
		t.Fatalf("worker-only status: %+v", st)
	}
	if _, err := o.Uninstall(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(o.File()); !os.IsNotExist(err) {
		t.Fatal("the plist is still there")
	}
	if st := o.Get(ctx); st.Installed {
		t.Fatalf("after uninstall: %+v", st)
	}
	if c := calls(t, dir); c[len(c)-1] != "bootout "+target {
		t.Fatalf("uninstall: %v", c)
	}
}

// The default home gets the plain label; any other home its own.
func TestLabels(t *testing.T) {
	u := t.TempDir()
	o := Options{Home: filepath.Join(u, ".metarush"), UserHome: u, OS: "darwin"}
	if o.Label() != "metarush.daemon" {
		t.Fatal(o.Label())
	}
	o.OS = "linux"
	if o.Label() != "metarush.service" || o.File() != filepath.Join(u, ".config", "systemd", "user", "metarush.service") {
		t.Fatal(o.Label(), o.File())
	}
	other := Options{Home: filepath.Join(u, "studio"), UserHome: u, OS: "linux"}
	if !strings.HasPrefix(other.Label(), "metarush-") || other.Label() == o.Label() {
		t.Fatal(other.Label())
	}
}

// The systemd user unit, through a fake systemctl.
func TestSystemdUserUnit(t *testing.T) {
	sc, dir := fakeTool(t, "systemctl", fakeSystemctl)
	userHome := t.TempDir()
	o := Options{Home: filepath.Join(t.TempDir(), "home%1"), Exe: exeFile(t), OS: "linux", UserHome: userHome, WorkerOnly: true,
		Tools: tools.New(config.Tools{Systemctl: sc})}
	ctx := context.Background()
	if _, err := o.Install(ctx); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(o.File())
	if err != nil {
		t.Fatal(err)
	}
	unit := string(b)
	for _, want := range []string{"ExecStart=\"" + o.Exe + "\" \"--home\" \"" + strings.ReplaceAll(o.Home, "%", "%%") + "\" \"serve\" \"--worker-only\"",
		"Restart=on-failure", "KillMode=process", "ExecStopPost=-\"" + o.Exe + "\" \"--home\" \"" + strings.ReplaceAll(o.Home, "%", "%%") + "\" \"service\" \"reap\"", "StandardOutput=append:" + strings.ReplaceAll(filepath.Join(o.Home, "logs", "daemon.log"), "%", "%%"), "WantedBy=default.target", "METARUSH_SERVICE=systemd"} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	if got := strings.Join(calls(t, dir), "|"); got != "--user daemon-reload|--user enable "+o.Label()+"|--user restart "+o.Label() {
		t.Fatalf("systemctl calls: %s", got)
	}
	st := o.Get(ctx)
	if !st.Installed || !st.Loaded || !st.Running || st.PID != 777 || !st.WorkerOnly || st.Binary != o.Exe || !st.SameBinary || st.Platform != Systemd {
		t.Fatalf("status: %+v", st)
	}
	if _, err := o.Uninstall(ctx); err != nil {
		t.Fatal(err)
	}
	c := calls(t, dir)
	if _, err := os.Stat(o.File()); !os.IsNotExist(err) || !strings.Contains(strings.Join(c, "|"), "--user disable --now "+o.Label()) {
		t.Fatalf("uninstall: %v", c)
	}
}

// Under `go test` the real launchctl and systemctl are never run, so no test
// can load a service into this machine.
func TestRefusesTheRealServiceManagersInTests(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		o := Options{Home: t.TempDir(), Exe: exeFile(t), OS: goos, UserHome: t.TempDir(), UID: 501, Tools: tools.New(config.Tools{})}
		_, err := o.Install(context.Background())
		if err == nil || !strings.Contains(err.Error(), "refusing to run the real") {
			t.Fatalf("%s: %v", goos, err)
		}
	}
}

func TestLogTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "daemon.log")
	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString("\n")
	}
	_ = os.WriteFile(p, []byte(b.String()), 0o644)
	lines, err := LogTail(p, 3)
	if err != nil || len(lines) != 3 || !strings.HasPrefix(lines[2], "line") {
		t.Fatalf("%v %v", lines, err)
	}
}

// After a crash the service's clean-up stops what the daemon left behind,
// never the database server, never itself, and only as the service.
func TestReapLeavesTheDatabase(t *testing.T) {
	comm := map[int]string{10: "metarush", 11: "ffmpeg", 12: "mongod", 13: "node", 14: "claude"}
	got := Reapable([]int{1, 10, 11, 12, 13, 14}, 10, func(p int) string { return comm[p] })
	if fmt.Sprint(got) != "[11 13 14]" {
		t.Errorf("reapable: %v", got)
	}
	t.Setenv("METARUSH_SERVICE", "")
	if _, _, err := Reap("metarush.service"); err != ErrNotInService {
		t.Errorf("outside the service: %v", err)
	}
}

// /proc/self/cgroup on a cgroup v2 host, a hybrid one (Ubuntu 20.04's
// default: v1 hierarchies with the unified one beside them) and a v1-only one.
func TestReapFindsTheUnifiedHierarchy(t *testing.T) {
	const unit = "/user.slice/user-1000.slice/user@1000.service/app.slice/metarush.service"
	for name, c := range map[string]struct {
		content string
		want    Cgroups
	}{
		"v2":     {"0::" + unit + "\n", Cgroups{Unified: unit}},
		"hybrid": {"12:memory:/user.slice\n2:cpu,cpuacct:/\n1:name=systemd:" + unit + "\n0::" + unit + "\n", Cgroups{Unified: unit, V1: true}},
		"v1":     {"12:memory:/user.slice\n1:name=systemd:" + unit + "\n", Cgroups{V1: true}},
	} {
		if got := ParseCgroups(c.content); got != c.want {
			t.Errorf("%s: %+v", name, got)
		}
	}
	mounted := func(paths ...string) func(string) bool {
		return func(p string) bool {
			for _, q := range paths {
				if p == q {
					return true
				}
			}
			return false
		}
	}
	if r := UnifiedRoot(mounted("/sys/fs/cgroup/cgroup.controllers")); r != "/sys/fs/cgroup" {
		t.Errorf("v2 root %q", r)
	}
	if r := UnifiedRoot(mounted("/sys/fs/cgroup/unified/cgroup.controllers")); r != "/sys/fs/cgroup/unified" {
		t.Errorf("hybrid root %q", r)
	}
	if r := UnifiedRoot(mounted()); r != "" {
		t.Errorf("v1 root %q", r)
	}
}

// fakeTarget is a process for reapAll: it may exit on TERM, and its number
// may then belong to an unrelated process (outside the group).
type fakeTarget struct {
	name     string
	exitTERM bool // exits on TERM
	reused   bool // after exiting, its number is another process's
	exited   bool
	got      []syscall.Signal
	closed   bool
}

func (f *fakeTarget) Signal(sig syscall.Signal) error {
	if f.exited {
		return syscall.ESRCH // the pidfd's process is gone; a reused number is never reached through it
	}
	f.got = append(f.got, sig)
	if sig == syscall.SIGTERM && f.exitTERM {
		f.exited = true
	}
	return nil
}
func (f *fakeTarget) Gone() bool { return f.exited }
func (f *fakeTarget) Close()     { f.closed = true }

// A process that exits on TERM is never sent KILL, even if its number is
// reused meanwhile; one that ignores TERM is killed; every handle is closed.
func TestReapNeverSignalsAReusedNumber(t *testing.T) {
	quick := &fakeTarget{name: "ffmpeg", exitTERM: true, reused: true}
	stubborn := &fakeTarget{name: "node"}
	reapAll([]reapTarget{quick, stubborn}, time.Second, func(time.Duration) {})
	if fmt.Sprint(quick.got) != fmt.Sprint([]syscall.Signal{syscall.SIGTERM}) {
		t.Errorf("the process that exited got %v", quick.got)
	}
	if fmt.Sprint(stubborn.got) != fmt.Sprint([]syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Errorf("the stubborn process got %v", stubborn.got)
	}
	if !quick.closed || !stubborn.closed {
		t.Error("a handle was left open")
	}
}
