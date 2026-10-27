package abilities

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/remotion"
	"github.com/metavert-io/metarush/internal/sandbox"
	"github.com/metavert-io/metarush/internal/tools"
)

// Which program listens on an ability's one local service (D284). A port is
// only a number, and an interpreter's executable is shared by every script it
// runs: what an owner agrees to is the program listening there — its
// executable as the system knows it (lsof's txt entry on a Mac, /proc on
// Linux), and for an interpreter (Python, Node.js…) the folder it was started
// in and the script or module it runs — plus, when the manifest declares one,
// a probe the service must answer as described. The owner sees it before
// agreeing and agrees to exactly that; plans and every run check it again,
// and the run's proxy checks it on its requests.

// testListeners stands in for this computer's listeners in tests.
var testListeners func(port int) []models.AbilityLocalProgram

// lsofTimeout bounds one lsof query.
const lsofTimeout = 5 * time.Second

// listenersOn are the programs listening for TCP on a port of this computer.
func listenersOn(ctx context.Context, a *app.App, port int) ([]models.AbilityLocalProgram, error) {
	if testListeners != nil {
		return testListeners(port), nil
	}
	var pids []int
	switch runtime.GOOS {
	case "darwin":
		out, err := lsof(ctx, a, "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-Fp")
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(out, "\n") {
			if n, err := strconv.Atoi(strings.TrimPrefix(line, "p")); err == nil && strings.HasPrefix(line, "p") {
				pids = append(pids, n)
			}
		}
	case "linux":
		var err error
		if pids, err = linuxListeners(port); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("MetaRush can't tell which program listens on a port here")
	}
	var out []models.AbilityLocalProgram
	seen := map[int]bool{}
	for _, pid := range pids {
		if seen[pid] {
			continue
		}
		seen[pid] = true
		out = append(out, identify(ctx, a, pid))
	}
	return out, nil
}

// lsof runs the system's lsof (-nP -b -w, at most five seconds). Nothing
// found is no error.
func lsof(ctx context.Context, a *app.App, args ...string) (string, error) {
	if !a.Tools.Has(tools.Lsof) {
		return "", fmt.Errorf("lsof isn't on this computer")
	}
	ctx, cancel := context.WithTimeout(ctx, lsofTimeout)
	defer cancel()
	res, err := a.Tools.Run(ctx, tools.Spec{Tool: tools.Lsof, Args: append([]string{"-nP", "-b", "-w"}, args...)})
	if res == nil {
		return "", err
	}
	if err != nil && len(res.Stdout) == 0 {
		return "", nil // lsof says nothing matched by failing
	}
	return string(res.Stdout), nil
}

// identify reads a listening process: its executable, the folder it was
// started in and, for an interpreter, the script or module it runs.
func identify(ctx context.Context, a *app.App, pid int) models.AbilityLocalProgram {
	p := models.AbilityLocalProgram{PID: pid}
	var argv []string
	switch runtime.GOOS {
	case "darwin":
		// The executable and the working folder as the system has them (a
		// process can rewrite what kern.procargs2 says, not these).
		out, _ := lsof(ctx, a, "-a", "-p", strconv.Itoa(pid), "-d", "cwd,txt", "-Fn")
		fd := ""
		for _, line := range strings.Split(out, "\n") {
			switch {
			case strings.HasPrefix(line, "f"):
				fd = line[1:]
			case strings.HasPrefix(line, "n") && fd == "txt" && p.Path == "":
				p.Path = line[1:]
			case strings.HasPrefix(line, "n") && fd == "cwd" && p.Cwd == "":
				p.Cwd = line[1:]
			}
		}
		argv = darwinArgv(pid)
	case "linux":
		p.Path, _ = os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
		p.Cwd, _ = os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
		if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
			argv = strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		}
	}
	p.Name = filepath.Base(p.Path)
	if p.Path == "" {
		p.Name = ""
	}
	if interpreterRe.MatchString(strings.ToLower(p.Name)) {
		p.Interpreter = true
		p.Script = scriptOf(argv, p.Cwd)
	} else {
		p.Cwd = "" // a program of its own is its executable
	}
	return p
}

// interpreterRe names programs that run a script they are given: the
// executable alone doesn't say which program it is.
var interpreterRe = regexp.MustCompile(`^(python[0-9.]*|pythonw|node(js)?|ruby[0-9.]*|perl[0-9.]*|php[0-9.]*|java|deno|bun|lua[0-9.]*|r|rscript|julia)$`)

// optWithValue are interpreter options followed by a value (not a script).
var optWithValue = map[string]bool{"-W": true, "-X": true, "-r": true, "--require": true, "--import": true, "--loader": true, "--experimental-loader": true,
	"-I": false, "-cp": true, "-classpath": true, "--title": true, "--env-file": true}

// scriptOf is the script or module an interpreter runs ("" when it runs code
// given on its command line, or nothing): "-m http.server", or the script's
// full path.
func scriptOf(argv []string, cwd string) string {
	if len(argv) < 2 {
		return ""
	}
	args := argv[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-m" && i+1 < len(args):
			return "-m " + args[i+1]
		case a == "-jar" && i+1 < len(args):
			return abs2(cwd, args[i+1])
		case a == "-c" || a == "-e" || a == "--eval" || a == "-p" || a == "--print":
			return "" // code given on the command line: nothing names it
		case a == "--":
			if i+1 < len(args) {
				return abs2(cwd, args[i+1])
			}
			return ""
		case strings.HasPrefix(a, "-"):
			if optWithValue[a] {
				i++
			}
		default:
			return abs2(cwd, a)
		}
	}
	return ""
}

func abs2(cwd, p string) string {
	if filepath.IsAbs(p) || cwd == "" {
		return filepath.Clean(p)
	}
	return filepath.Join(cwd, p)
}

// procArgv reads kern.procargs2's answer: the argument count, the
// executable's path, padding, then the arguments.
func procArgv(b []byte) []string {
	if len(b) < 5 {
		return nil
	}
	argc := int(int32(uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24))
	rest := b[4:]
	i := bytes.IndexByte(rest, 0)
	if i < 0 {
		return nil
	}
	rest = rest[i:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	var out []string
	for len(out) < argc && len(rest) > 0 {
		j := bytes.IndexByte(rest, 0)
		if j < 0 {
			out = append(out, string(rest))
			break
		}
		out = append(out, string(rest[:j]))
		rest = rest[j+1:]
	}
	return out
}

// linuxListeners reads /proc/net/tcp{,6} for sockets listening on the port
// and finds the processes holding them.
func linuxListeners(port int) ([]int, error) {
	inodes := map[string]bool{}
	hexPort := fmt.Sprintf(":%04X", port)
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 10 || !strings.HasSuffix(fields[1], hexPort) || fields[3] != "0A" { // 0A: listening
				continue
			}
			inodes["socket:["+fields[9]+"]"] = true
		}
		fh.Close()
	}
	if len(inodes) == 0 {
		return nil, nil
	}
	procs, _ := filepath.Glob("/proc/[0-9]*")
	var pids []int
	for _, d := range procs {
		fds, _ := os.ReadDir(filepath.Join(d, "fd"))
		for _, fd := range fds {
			if l, err := os.Readlink(filepath.Join(d, "fd", fd.Name())); err == nil && inodes[l] {
				if n, err := strconv.Atoi(filepath.Base(d)); err == nil {
					pids = append(pids, n)
				}
				break
			}
		}
	}
	return pids, nil
}

// ownProgram says whether a program is one of MetaRush's own (this process,
// another MetaRush, the database it runs, anything inside its folders).
func ownProgram(a *app.App, p models.AbilityLocalProgram) bool {
	if p.PID == os.Getpid() {
		return true
	}
	if self, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(self); err == nil {
			self = real
		}
		if p.Path == self {
			return true
		}
	}
	switch strings.ToLower(p.Name) {
	case "metarush", "mongod", "metarush-vision", "whisper-server":
		return true
	}
	home, _ := os.UserHomeDir()
	homeDir := fold(canonDir(home))
	for _, h := range sandbox.HiddenFor(a.Cfg) {
		if home != "" && fold(canonDir(h)) == homeDir {
			continue // the person's home: their own programs may live there
		}
		for _, x := range []string{p.Path, p.Script} {
			if x != "" && !strings.HasPrefix(x, "-m ") && sameOrInside(x, h) {
				return true
			}
		}
	}
	return false
}

// ownPort reports one of MetaRush's own ports here (its dashboard, the
// developer ports, the database's).
func ownPort(a *app.App, port int) bool {
	for _, p := range remotion.OwnPorts(a) {
		if p == port {
			return true
		}
	}
	return false
}

// programWords names a listening program in plain words (“python3.12”);
// its path stays in the record and behind Details (D109).
func programWords(p models.AbilityLocalProgram) string {
	if p.Name == "" {
		return "a program MetaRush can't name"
	}
	return "“" + p.Name + "”"
}

// identityWords says everything that identifies a program, for an owner
// deciding: “python3.12” (“/opt/…/python3.12”), started in “/Users/…/ComfyUI”,
// running “/Users/…/ComfyUI/main.py”. Paths are the person's own, quoted.
func identityWords(p models.AbilityLocalProgram) string {
	if p.Path == "" {
		return "a program MetaRush can't name"
	}
	s := fmt.Sprintf("“%s” (“%s”)", p.Name, p.Path)
	if p.Interpreter {
		if p.Cwd != "" {
			s += fmt.Sprintf(", started in “%s”", p.Cwd)
		}
		if p.Script != "" {
			s += fmt.Sprintf(", running “%s”", p.Script)
		} else {
			s += ", running no script MetaRush can name"
		}
	}
	return s
}

// sameProgram reports two looks at the same program: its executable, and an
// interpreter's folder and script.
func sameProgram(x, y *models.AbilityLocalProgram) bool {
	if x == nil || y == nil {
		return x == y
	}
	return x.Name == y.Name && x.Path == y.Path && x.Interpreter == y.Interpreter && x.Cwd == y.Cwd && x.Script == y.Script
}

// differenceWords says how the program listening now differs from the one an
// owner agreed to.
func differenceWords(now, agreed models.AbilityLocalProgram) string {
	switch {
	case now.Name != agreed.Name:
		return fmt.Sprintf("it is now %s, not %s", programWords(now), programWords(agreed))
	case now.Path != agreed.Path:
		return fmt.Sprintf("it is now another %s, somewhere else on this computer", programWords(now))
	case now.Cwd != agreed.Cwd:
		return fmt.Sprintf("%s was started in another folder", programWords(now))
	default:
		return fmt.Sprintf("%s runs another script", programWords(now))
	}
}

// probeLocal asks the service the manifest's probe ("" when it answers as
// described, or there is none): a GET of its path, at most three seconds,
// a 200 answer in JSON that has each expected field.
func probeLocal(ctx context.Context, port int, pr *models.AbilityLocalProbe) string {
	if pr == nil {
		return ""
	}
	fail := fmt.Sprintf("The program on port %d of this computer didn't answer %s's probe as its manifest describes", port, pr.Path)
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if testDial != nil {
			return testDial(ctx, network, addr)
		}
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil, DialContext: dial},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", port, pr.Path), nil)
	if err != nil {
		return fail + "."
	}
	resp, err := client.Do(req)
	if err != nil {
		return fail + " (it didn't answer)."
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	var doc any
	if resp.StatusCode != http.StatusOK || json.Unmarshal(b, &doc) != nil {
		return fail + "."
	}
	for _, key := range pr.Expect {
		cur := doc
		for _, part := range strings.Split(key, ".") {
			m, ok := cur.(map[string]any)
			if !ok {
				return fmt.Sprintf("%s: its answer has no %s.", fail, key)
			}
			if cur, ok = m[part]; !ok {
				return fmt.Sprintf("%s: its answer has no %s.", fail, key)
			}
		}
	}
	return ""
}

// localListener looks up the one program listening on the service's port
// and says why it can't be reached ("" when it can be): nothing listens,
// more than one program does, MetaRush can't tell which, or it is
// MetaRush's own. The manifest's probe is asked only of a program that
// passed (probeLocal), so nothing is ever sent to another.
func localListener(ctx context.Context, a *app.App, l *models.AbilityLocal) (*models.AbilityLocalProgram, string) {
	ls, err := listenersOn(ctx, a, l.Port)
	switch {
	case err != nil:
		return nil, fmt.Sprintf("MetaRush can't tell which program listens on port %d of this computer.", l.Port)
	case len(ls) == 0:
		return nil, fmt.Sprintf("Nothing listens on port %d of this computer: start %s there first.", l.Port, l.Service)
	case len(ls) > 1:
		return nil, fmt.Sprintf("More than one program listens on port %d of this computer.", l.Port)
	case ls[0].Path == "":
		return nil, fmt.Sprintf("MetaRush can't tell which program listens on port %d of this computer.", l.Port)
	case ownProgram(a, ls[0]):
		return nil, fmt.Sprintf("Port %d of this computer is MetaRush's own; an ability never reaches it.", l.Port)
	}
	p := ls[0]
	p.PID = 0 // the program, not this run of it
	return &p, ""
}

// identifiable says why a program can't be agreed to ("" when it can): an
// interpreter that runs no script MetaRush can name, with no probe to tell
// the service apart (D284).
func identifiable(p *models.AbilityLocalProgram, l *models.AbilityLocal) string {
	if p.Interpreter && p.Script == "" && l.Probe == nil {
		return fmt.Sprintf("MetaRush can't tell which program listens on port %d: it is %s running code it can't name, and the ability's manifest gives no probe to ask the service. Start %s from its own script, or use a version with a probe.",
			l.Port, programWords(*p), l.Service)
	}
	return ""
}

// localChecks remembers recent checks of a run's local service for a few
// seconds (the run's proxy asks on its requests).
type localChecks struct {
	mu    sync.Mutex
	at    time.Time
	why   string
	every time.Duration
	check func() string
}

// Now checks without memory (a new connection), and remembers the answer.
func (c *localChecks) Now() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.why, c.at = c.check(), time.Now()
	return c.why
}

func (c *localChecks) Check() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < c.every {
		return c.why
	}
	c.why, c.at = c.check(), time.Now()
	return c.why
}

// localRecheck is how long a run's proxy trusts its last check.
var localRecheck = 3 * time.Second
