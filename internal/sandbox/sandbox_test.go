package sandbox

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// The forwarder runs as this test binary inside a Linux sandbox.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == ForwardCommand {
		os.Exit(ForwardMain(os.Args[2:]))
	}
	os.Exit(m.Run())
}

// testCA makes a certificate authority and a server certificate for names
// (the "internet" of these tests is loopback servers with made-up names).
func testCA(t *testing.T, names ...string) (*x509.CertPool, tls.Certificate) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return pool, tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// seen is what the test "internet" received.
type seen struct {
	mu   sync.Mutex
	reqs []map[string]string
}

func (s *seen) handler(w http.ResponseWriter, r *http.Request) {
	got := map[string]string{"host": r.Host, "path": r.URL.Path, "auth": r.Header.Get("Authorization"), "key": r.URL.Query().Get("key"), "via": r.Header.Get("X-Metarush-Proxy")}
	s.mu.Lock()
	s.reqs = append(s.reqs, got)
	s.mu.Unlock()
	if r.URL.Path == "/redirect" {
		http.Redirect(w, r, "/echo?from=redirect", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// Repeats the key it was given (a careless API): MetaRush takes it out.
	_ = json.NewEncoder(w).Encode(map[string]string{"ok": "yes", "path": r.URL.Path, "echo": r.Header.Get("Authorization")})
}

func (s *seen) last() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.reqs) == 0 {
		return nil
	}
	return s.reqs[len(s.reqs)-1]
}

func (s *seen) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

type world struct {
	ts      *tools.Set
	iso     Isolation
	proxy   *Proxy
	room    *Room
	env     []string
	api     *seen
	direct  string // the test server's own loopback address (a program must never reach it)
	standIn string
}

const realKey = "REALKEY-0123456789abcdefghij" // secret-scan: allow (a made-up test key)

func newWorld(t *testing.T, mode string) *world {
	t.Helper()
	ts := tools.New(config.Tools{})
	iso := Here(ts)
	if !iso.OK() {
		// CI's Linux job sets METARUSH_TEST_SANDBOX=require once bubblewrap
		// can run there, so these never pass by skipping.
		if os.Getenv("METARUSH_TEST_SANDBOX") == "require" {
			t.Fatalf("no sandbox on this computer: %s (%s; %s)", iso.Problem, iso.Said, iso.Fix)
		}
		t.Skip("no sandbox on this computer: " + iso.Problem)
	}
	pool, cert := testCA(t, "api.example.test", "other.example.test")
	w := &world{ts: ts, iso: iso, api: &seen{}, standIn: "mrkey-demo-5f1e2d3c4b5a69788796"}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(w.api.handler))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	w.direct = srv.Listener.Addr().String()
	plain := httptest.NewServer(http.HandlerFunc(w.api.handler))
	t.Cleanup(plain.Close)
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, _ := net.SplitHostPort(addr)
		var d net.Dialer
		switch {
		case (host == "api.example.test" || host == "other.example.test") && port == "443":
			return d.DialContext(ctx, network, w.direct)
		case host == "plain.example.test" && port == "80":
			return d.DialContext(ctx, network, plain.Listener.Addr().String())
		}
		return nil, &net.DNSError{Err: "no such host (test)", Name: host, IsNotFound: true}
	}
	p, err := StartProxy(ProxyConfig{Hosts: []string{"api.example.test", "plain.example.test", "other.example.test"}, Token: "tok-123456",
		Keys: []ProxyKey{{Name: "demo", Value: realKey, StandIn: w.standIn, Hosts: []string{"api.example.test"}}}, Dial: dial, RootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	w.proxy = p
	root, _ := filepath.EvalSymlinks(t.TempDir())
	room := &Room{Root: root, Work: filepath.Join(root, "work"), Net: mode, ProxyPort: p.Port(), Hide: HiddenFor(nil)}
	for _, d := range []string{"work", "home", "tmp", ".metarush"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ca, preload, pyDir, err := WriteHelpers(filepath.Join(root, ".metarush"), p.CAPEM())
	if err != nil {
		t.Fatal(err)
	}
	if iso.Kind == Linux && mode == NetProxy {
		room.ProxySocket = filepath.Join(root, ".metarush", "proxy.sock")
		if err := p.ListenUnix(room.ProxySocket); err != nil {
			t.Fatal(err)
		}
		room.Self, _ = os.Executable()
	}
	w.room = room
	w.env = BaseEnv(nil, filepath.Join(root, "home"), filepath.Join(root, "tmp"))
	w.env = append(w.env, "DEMO_KEY="+w.standIn)
	if mode == NetProxy {
		w.env = append(w.env, ProxyEnv(p.URL(), ca, preload, pyDir)...)
	}
	return w
}

// run runs a program in the room's sandbox.
func (w *world) run(t *testing.T, program string, args ...string) (string, error) {
	t.Helper()
	r := *w.room
	r.Read = append(r.Read, InstallationOf(program)...)
	tool, argv, err := r.Command(w.iso.Kind, program, args)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := w.ts.Run(ctx, tools.Spec{Tool: tool, Args: argv, Dir: r.Work, Env: w.env, CleanEnv: true, Contained: true})
	out := ""
	if res != nil {
		out = string(res.Stdout)
	}
	var ef *tools.ErrFailed
	if err != nil && errorsAs(err, &ef) {
		out += ef.Stderr
	}
	return out, err
}

func errorsAs(err error, ef **tools.ErrFailed) bool {
	e, ok := err.(*tools.ErrFailed)
	if ok {
		*ef = e
	}
	return ok
}

func pythonWith(t *testing.T, module string) string {
	t.Helper()
	for _, p := range []string{"/Library/Frameworks/Python.framework/Versions/Current/bin/python3", "/opt/homebrew/bin/python3", "/usr/local/bin/python3", "/usr/bin/python3"} {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if module == "" || exec.Command(p, "-c", "import "+module).Run() == nil {
			return p
		}
	}
	t.Skip("no python3 with " + module)
	return ""
}

func nodePath(t *testing.T, ts *tools.Set) string {
	t.Helper()
	p := ts.Path(tools.Node)
	if p == "" {
		t.Skip("no node")
	}
	return p
}

func expectKeyed(t *testing.T, w *world, out string, before int) {
	t.Helper()
	if w.api.count() <= before {
		t.Fatalf("the request never reached the host:\n%s", out)
	}
	got := w.api.last()
	if got["auth"] != "Bearer "+realKey {
		t.Fatalf("the host got %q, want the real key", got["auth"])
	}
	if got["via"] != "" {
		t.Fatalf("the proxy's token leaked to the host: %q", got["via"])
	}
	if strings.Contains(out, realKey) {
		t.Fatalf("the program saw the real key:\n%s", out)
	}
	if !strings.Contains(out, w.standIn) {
		t.Fatalf("the answer should show the stand-in where the host repeated the key:\n%s", out)
	}
}

func TestProxyClientsInSandbox(t *testing.T) {
	w := newWorld(t, NetProxy)
	t.Run("curl", func(t *testing.T) {
		if _, err := os.Stat("/usr/bin/curl"); err != nil {
			t.Skip("no curl")
		}
		n := w.api.count()
		out, err := w.run(t, "/usr/bin/curl", "-sS", "--max-time", "20", "-H", "Authorization: Bearer "+w.standIn, "https://api.example.test/echo")
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		expectKeyed(t, w, out, n)
	})
	t.Run("python urllib", func(t *testing.T) {
		py := pythonWith(t, "")
		n := w.api.count()
		out, err := w.run(t, py, "-c", `import os, urllib.request
r = urllib.request.Request("https://api.example.test/echo", headers={"Authorization": "Bearer " + os.environ["DEMO_KEY"]})
print(urllib.request.urlopen(r, timeout=20).read().decode())`)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		expectKeyed(t, w, out, n)
	})
	t.Run("python requests", func(t *testing.T) {
		py := pythonWith(t, "requests")
		n := w.api.count()
		out, err := w.run(t, py, "-c", `import os, requests
print(requests.get("https://api.example.test/echo", headers={"Authorization": "Bearer " + os.environ["DEMO_KEY"]}, timeout=20).text)`)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		expectKeyed(t, w, out, n)
	})
	t.Run("python httpx", func(t *testing.T) {
		py := pythonWith(t, "httpx")
		n := w.api.count()
		out, err := w.run(t, py, "-c", `import os, httpx
print(httpx.get("https://api.example.test/echo", headers={"Authorization": "Bearer " + os.environ["DEMO_KEY"]}, timeout=20).text)`)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		expectKeyed(t, w, out, n)
	})
	t.Run("the system's python3", func(t *testing.T) {
		if _, err := os.Stat("/usr/bin/python3"); err != nil || exec.Command("/usr/bin/python3", "-c", "1").Run() != nil {
			t.Skip("no working /usr/bin/python3")
		}
		n := w.api.count()
		out, err := w.run(t, RealProgram("/usr/bin/python3"), "-c", `import os, urllib.request
r = urllib.request.Request("https://api.example.test/echo", headers={"Authorization": "Bearer " + os.environ["DEMO_KEY"]})
print(urllib.request.urlopen(r, timeout=20).read().decode())`)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		expectKeyed(t, w, out, n)
	})
	t.Run("node fetch", func(t *testing.T) {
		node := nodePath(t, w.ts)
		n := w.api.count()
		out, err := w.run(t, node, "-e", `fetch("https://api.example.test/echo", {headers: {Authorization: "Bearer " + process.env.DEMO_KEY}}).then(r => r.text()).then(t => console.log(t)).catch(e => { console.error(String(e), e.cause); process.exit(3) })`)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		expectKeyed(t, w, out, n)
	})
	t.Run("node fetch follows a redirect through the proxy", func(t *testing.T) {
		node := nodePath(t, w.ts)
		out, err := w.run(t, node, "-e", `fetch("https://api.example.test/redirect").then(r => r.json()).then(j => console.log(j.path)).catch(e => { console.error(String(e), e.cause); process.exit(3) })`)
		if err != nil || !strings.Contains(out, "/echo") {
			t.Fatalf("%v\n%s", err, out)
		}
	})
	t.Run("node https.get", func(t *testing.T) {
		node := nodePath(t, w.ts)
		n := w.api.count()
		out, err := w.run(t, node, "-e", `require("https").get("https://api.example.test/echo", {headers: {Authorization: "Bearer " + process.env.DEMO_KEY}}, r => { let b = ""; r.on("data", d => b += d); r.on("end", () => console.log(b)) }).on("error", e => { console.error(String(e)); process.exit(3) })`)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		expectKeyed(t, w, out, n)
	})
	t.Run("a host it didn't declare is refused", func(t *testing.T) {
		node := nodePath(t, w.ts)
		out, err := w.run(t, node, "-e", `fetch("https://undeclared.example.test/").then(r => { console.log(r.status); return r.text() }).then(t => console.log(t)).catch(e => { console.error(String(e)); process.exit(3) })`)
		if err != nil || !strings.Contains(out, "403") || !strings.Contains(out, "isn't one of the hosts") {
			t.Fatalf("%v\n%s", err, out)
		}
	})
	t.Run("a key on its way to another declared host is refused", func(t *testing.T) {
		if _, err := os.Stat("/usr/bin/curl"); err != nil {
			t.Skip("no curl")
		}
		n := w.api.count()
		out, _ := w.run(t, "/usr/bin/curl", "-sS", "--max-time", "20", "-H", "Authorization: Bearer "+w.standIn, "https://other.example.test/echo")
		if w.api.count() != n || !strings.Contains(out, "isn't for other.example.test") {
			t.Fatalf("the key's stand-in should not leave for another host:\n%s", out)
		}
	})
	t.Run("a key over plain HTTP is refused", func(t *testing.T) {
		if _, err := os.Stat("/usr/bin/curl"); err != nil {
			t.Skip("no curl")
		}
		n := w.api.count()
		out, _ := w.run(t, "/usr/bin/curl", "-sS", "--max-time", "20", "-H", "Authorization: Bearer "+w.standIn, "http://plain.example.test/echo")
		if w.api.count() != n || !strings.Contains(out, "only over HTTPS") {
			t.Fatalf("a key must not go over plain HTTP:\n%s", out)
		}
	})
	t.Run("plain HTTP without a key works", func(t *testing.T) {
		if _, err := os.Stat("/usr/bin/curl"); err != nil {
			t.Skip("no curl")
		}
		out, err := w.run(t, "/usr/bin/curl", "-sS", "--max-time", "20", "http://plain.example.test/echo")
		if err != nil || !strings.Contains(out, `"ok":"yes"`) {
			t.Fatalf("%v\n%s", err, out)
		}
	})
	t.Run("going around the proxy is refused", func(t *testing.T) {
		node := nodePath(t, w.ts)
		host, port, _ := net.SplitHostPort(w.direct)
		out, err := w.run(t, node, "-e", `const s = require("net").connect(`+port+`, "`+host+`"); s.on("connect", () => { console.log("CONNECTED"); process.exit(0) }); s.on("error", e => { console.log("refused", e.code); process.exit(0) })`)
		if err != nil || strings.Contains(out, "CONNECTED") {
			t.Fatalf("a direct connection must be refused: %v\n%s", err, out)
		}
	})
	t.Run("a program without the proxy's password is refused", func(t *testing.T) {
		if _, err := os.Stat("/usr/bin/curl"); err != nil {
			t.Skip("no curl")
		}
		n := w.api.count()
		out, _ := w.run(t, "/usr/bin/curl", "-sS", "--max-time", "20", "--proxy", "http://127.0.0.1:"+itoa(w.proxy.Port()), "https://api.example.test/echo")
		if w.api.count() != n {
			t.Fatalf("a request without the token went through:\n%s", out)
		}
	})
	reqs, _ := w.proxy.Requests()
	for _, r := range reqs {
		b, _ := json.Marshal(r)
		if strings.Contains(string(b), realKey) || strings.Contains(string(b), "tok-123456") {
			t.Fatalf("the request log holds a secret: %s", b)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestNetworkOff(t *testing.T) {
	w := newWorld(t, NetOff)
	node := nodePath(t, w.ts)
	out, err := w.run(t, node, "-e", `fetch("https://api.example.test/echo").then(() => console.log("REACHED")).catch(e => { console.log("no network:", String(e.cause && e.cause.code || e)); })`)
	if err != nil || strings.Contains(out, "REACHED") {
		t.Fatalf("with the network off nothing is reached: %v\n%s", err, out)
	}
	host, port, _ := net.SplitHostPort(w.proxy.ln.Addr().String())
	out, err = w.run(t, node, "-e", `const s = require("net").connect(`+port+`, "`+host+`"); s.on("connect", () => { console.log("CONNECTED"); process.exit(0) }); s.on("error", e => { console.log("refused", e.code); process.exit(0) })`)
	if err != nil || strings.Contains(out, "CONNECTED") {
		t.Fatalf("with the network off even loopback is refused: %v\n%s", err, out)
	}
}

func TestSandboxKeepsFilesApart(t *testing.T) {
	w := newWorld(t, NetOff)
	home, _ := os.UserHomeDir()
	outside := filepath.Join(home, ".metarush-sandbox-test-"+itoa(os.Getpid()))
	py := pythonWith(t, "")
	out, err := w.run(t, py, "-c", `import os, sys
home = os.path.expanduser("~")
try:
    os.listdir(sys.argv[1]); print("READ-HOME")
except Exception as e:
    print("home hidden:", type(e).__name__)
try:
    open(sys.argv[2], "w").write("x"); print("WROTE-OUTSIDE")
except Exception as e:
    print("outside refused:", type(e).__name__)
open("inside.txt", "w").write("ok"); print("wrote inside")
`, home, outside)
	defer os.Remove(outside)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, "READ-HOME") || strings.Contains(out, "WROTE-OUTSIDE") || !strings.Contains(out, "wrote inside") {
		t.Fatalf("the sandbox should hide the home folder and keep writes in the room:\n%s", out)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("a file appeared outside the room")
	}
	if b, err := os.ReadFile(filepath.Join(w.room.Work, "inside.txt")); err != nil || string(b) != "ok" {
		t.Fatalf("the room's file: %v %q", err, b)
	}
}

// TestIsolationHere records what this computer offers (go test -v shows it:
// the Linux spike reads it in CI) and that a computer that can't keep a
// program apart says so plainly, with what an administrator does about it.
func TestIsolationHere(t *testing.T) {
	iso := Here(tools.New(config.Tools{}))
	t.Logf("isolation here: kind=%q\nwords: %s\nproblem: %s\nfix: %s\nsaid: %s", iso.Kind, iso.Words, iso.Problem, iso.Fix, iso.Said)
	if runtime.GOOS == "linux" {
		for _, n := range []string{"kernel/apparmor_restrict_unprivileged_userns", "kernel/unprivileged_userns_clone", "user/max_user_namespaces"} {
			t.Logf("/proc/sys/%s = %q", n, readSysctl(n))
		}
	}
	if iso.OK() == (iso.Problem != "") || (iso.OK() && iso.Words == "") {
		t.Fatalf("either it can (with words) or it says why not: %+v", iso)
	}
}

func TestNamespaceLimits(t *testing.T) {
	set := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(n string) string { return m[n] }
	}
	if l := namespaceLimit(set(), "an ability"); l.Problem != "" {
		t.Errorf("nothing set, nothing said: %+v", l)
	}
	if l := namespaceLimit(set("kernel/apparmor_restrict_unprivileged_userns", "0", "user/max_user_namespaces", "63000"), "an ability"); l.Problem != "" {
		t.Errorf("limits off: %+v", l)
	}
	l := namespaceLimit(set("kernel/apparmor_restrict_unprivileged_userns", "1"), "a refresh")
	if !strings.Contains(l.Problem, "security settings") || !strings.Contains(l.Fix, "apparmor_parser") || strings.Contains(l.Problem, "sudo") || !strings.Contains(l.Problem, "run a refresh here") {
		t.Errorf("Ubuntu's AppArmor limit: %+v", l)
	}
	if l := namespaceLimit(set("kernel/unprivileged_userns_clone", "0"), "an ability"); !strings.Contains(l.Fix, "unprivileged_userns_clone=1") {
		t.Errorf("Debian's switch: %+v", l)
	}
	if l := namespaceLimit(set("user/max_user_namespaces", "0"), "an ability"); !strings.Contains(l.Fix, "max_user_namespaces") {
		t.Errorf("no namespaces at all: %+v", l)
	}
	if !strings.Contains(AppArmorProfile, "/usr/bin/bwrap") || !strings.Contains(AppArmorProfile, "userns,") {
		t.Error("the profile names bubblewrap and lets it make namespaces")
	}
}

// TestBwrapArgs checks bubblewrap's arguments on any computer: every
// namespace its own (the network too unless it is open), the system
// read-only, the room the only place it writes, the hidden folders never
// mounted, the forwarder in front of the program with MetaRush's network.
func TestBwrapArgs(t *testing.T) {
	root := t.TempDir()
	home, _ := os.UserHomeDir()
	r := &Room{Root: root, Work: filepath.Join(root, "work"), Write: []string{filepath.Join(root, "out")}, Read: []string{"/usr/bin/python3"}, Hide: []string{home},
		Net: NetProxy, ProxyPort: 4711, ProxySocket: filepath.Join(root, "proxy.sock"), Self: "/opt/metarush/bin/metarush"}
	tool, args, err := r.Command(Linux, "/usr/bin/python3", []string{"run.py"})
	if err != nil || tool != tools.Bwrap {
		t.Fatal(tool, err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--unshare-all", "--die-with-parent", "--new-session", "--ro-bind " + root + " " + root, "--bind " + filepath.Join(root, "out") + " " + filepath.Join(root, "out"), "--chdir " + filepath.Join(root, "work"),
		"--ro-bind /opt/metarush/bin/metarush /opt/metarush/bin/metarush", "-- /opt/metarush/bin/metarush " + ForwardCommand + " --listen 127.0.0.1:4711 --to " + filepath.Join(root, "proxy.sock") + " -- /usr/bin/python3 run.py",
		"--tmpfs /tmp", "--proc /proc", "--dev /dev"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "--share-net") || strings.Contains(joined, " "+home+" ") {
		t.Errorf("no network of the computer's and no home folder: %s", joined)
	}
	for i, a := range args {
		if a == "--bind" && args[i+1] != filepath.Join(root, "out") {
			t.Errorf("writes only in its write folders: --bind %s", args[i+1])
		}
	}
	// Without the forwarder's parts there is no network through MetaRush.
	r2 := *r
	r2.Self = ""
	if _, _, err := r2.Command(Linux, "/usr/bin/python3", nil); err == nil {
		t.Error("the proxy on Linux needs the forwarder")
	}
	r3 := *r
	r3.Net = NetOff
	_, args, _ = r3.Command(Linux, "/usr/bin/python3", []string{"run.py"})
	if j := strings.Join(args, " "); strings.Contains(j, ForwardCommand) || strings.Contains(j, "--share-net") || !strings.HasSuffix(j, "-- /usr/bin/python3 run.py") {
		t.Errorf("network off: %s", j)
	}
	// An open network goes through MetaRush too: never the computer's own.
	r4 := *r
	r4.Net = NetOpen
	_, args, _ = r4.Command(Linux, "/usr/bin/python3", nil)
	if j := strings.Join(args, " "); strings.Contains(j, "--share-net") || !strings.Contains(j, ForwardCommand) {
		t.Errorf("an open network: %s", j)
	}
}

// TestHiddenFoldersAlwaysWin: a program pinned in ~/bin doesn't open the
// home folder; a Read that is or holds a hidden folder is refused; one
// inside a hidden folder is left out; on a Mac the hidden folders are denied
// after everything allowed, and a sandboxed program can't read a file in the
// home folder even when its folder is listed to read (review 1).
func TestHiddenFoldersAlwaysWin(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, p := range InstallationOf(filepath.Join(home, "bin", "ffmpeg")) {
		if within(home, p) {
			t.Fatalf("a program in ~/bin opens %s", p)
		}
	}
	if got := InstallationOf("/usr/local/bin/node-not-there"); len(got) != 1 {
		t.Errorf("a bare folder of programs gives just the file: %v", got)
	}
	root := t.TempDir()
	room := func(read ...string) *Room {
		return &Room{Root: root, Work: root, Read: read, Hide: HiddenFor(nil)}
	}
	for _, bad := range []string{home, filepath.Dir(home), "/"} {
		if _, _, err := room(bad).Command(Mac, "/bin/echo", nil); err == nil {
			t.Errorf("reading %s is refused", bad)
		}
	}
	inside := filepath.Join(home, ".pyenv", "versions")
	if got, err := room(inside, "/opt/x").readable(); err != nil || len(got) != 1 || got[0] != "/opt/x" {
		t.Errorf("a folder inside the home folder is left out: %v %v", got, err)
	}
	prof := MacProfile(room("/opt/x"))
	if strings.LastIndex(prof, "(deny file-read*") < strings.LastIndex(prof, "(allow file-read*") {
		t.Errorf("hidden folders are denied last:\n%s", prof)
	}
	if strings.Contains(prof, "(allow file-read-metadata)") || strings.Contains(prof, "/dev/tty") {
		t.Errorf("metadata isn't allowed everywhere, and the terminal not at all:\n%s", prof)
	}
	// In the sandbox: a file in the home folder stays out of reach.
	ts := tools.New(config.Tools{})
	if Here(ts).Kind != Mac {
		return
	}
	dir, err := os.MkdirTemp(home, ".metarush-sandbox-test-")
	if err != nil {
		t.Skip("can't make a folder in the home folder")
	}
	defer os.RemoveAll(dir)
	secret := filepath.Join(dir, "private.txt")
	_ = os.WriteFile(secret, []byte("PRIVATE"), 0o600)
	r := room()
	r.Read = []string{dir} // as if a program's installation were there
	tool, argv, err := r.Command(Mac, "/bin/cat", []string{secret})
	if err != nil {
		t.Fatal(err)
	}
	res, _ := ts.Run(context.Background(), tools.Spec{Tool: tool, Args: argv, Dir: root, CleanEnv: true, Env: BaseEnv(nil, root, root), Contained: true})
	if res != nil && strings.Contains(string(res.Stdout), "PRIVATE") {
		t.Fatal("a sandboxed program read a file in the home folder")
	}
}

// TestPlainProgram builds a small Go program (net/http's defaults, nothing
// MetaRush gives it) and runs it in the sandbox with MetaRush's network: a
// plain program that reads the proxy settings and the certificate file the
// environment names works as is.
func TestPlainProgram(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go to build the program with")
	}
	w := newWorld(t, NetProxy)
	src := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(src, []byte(`package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
)

func main() {
	req, _ := http.NewRequest("GET", os.Args[1], nil)
	req.Header.Set("Authorization", "Bearer "+os.Getenv("DEMO_KEY"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(3)
	}
	b, _ := io.ReadAll(resp.Body)
	fmt.Println(string(b))
}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(w.room.Root, "plain")
	build := exec.Command(gobin, "build", "-o", bin, src)
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("couldn't build the program: %v\n%s", err, out)
	}
	n := w.api.count()
	out, err := w.run(t, bin, "https://api.example.test/echo")
	t.Logf("plain program: %v\n%s", err, out)
	if runtime.GOOS == "darwin" && strings.Contains(out, "certificate") {
		// Go on a Mac checks certificates with the system's own trust
		// settings and ignores SSL_CERT_FILE: it fails closed.
		t.Skip("a Go program on a Mac ignores the run's certificate file and fails closed (docs/abilities.md)")
	}
	if err != nil {
		t.Fatal(err)
	}
	expectKeyed(t, w, out, n)
}

func TestProxyRefusesLocalAddresses(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "192.168.1.4", "172.16.0.1", "169.254.169.254", "::1", "fd00::1", "fe80::1", "0.0.0.0", "100.64.0.1", "64:ff9b::7f00:1"} {
		if publicIP(net.ParseIP(ip)) {
			t.Errorf("%s should count as local", ip)
		}
	}
	for _, ip := range []string{"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946", "8.8.8.8"} {
		if !publicIP(net.ParseIP(ip)) {
			t.Errorf("%s should count as public", ip)
		}
	}
}

func TestHostPatterns(t *testing.T) {
	hosts := []string{"api.example.com", "*.cdn.example.com", "files.example.com:8443"}
	cases := []struct {
		host, port, scheme string
		ok                 bool
	}{
		{"api.example.com", "443", "https", true},
		{"API.example.com", "443", "https", true},
		{"api.example.com", "80", "http", true},
		{"api.example.com", "8443", "https", false},
		{"x.cdn.example.com", "443", "https", true},
		{"cdn.example.com", "443", "https", false},
		{"evilcdn.example.com", "443", "https", false},
		{"files.example.com", "8443", "https", true},
		{"files.example.com", "443", "https", false},
		{"example.com", "443", "https", false},
	}
	for _, c := range cases {
		if got := hostMatches(hosts, c.host, c.port, c.scheme); got != c.ok {
			t.Errorf("%s:%s %s = %v, want %v", c.host, c.port, c.scheme, got, c.ok)
		}
	}
}

func TestReplacerAcrossReads(t *testing.T) {
	key := "SECRET-abcdefghijklmnop"
	text := strings.Repeat("x", 40000) + key + strings.Repeat("y", 10) + key
	r := &replacer{r: &slowReader{s: text, n: 7}, pairs: [][2][]byte{{[]byte(key), []byte("STAND-IN")}}, keep: len(key) - 1}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), key) || strings.Count(string(b), "STAND-IN") != 2 || len(b) != 40000+10+2*len("STAND-IN") {
		t.Fatalf("replaced badly: %d bytes", len(b))
	}
}

type slowReader struct {
	s string
	n int
}

func (s *slowReader) Read(b []byte) (int, error) {
	if s.s == "" {
		return 0, io.EOF
	}
	n := min(s.n, len(b), len(s.s))
	copy(b, s.s[:n])
	s.s = s.s[n:]
	return n, nil
}

// TestProxyMaxSend needs no sandbox: a request carrying more than the run
// may send is refused before it leaves, with or without its length known.
func TestProxyMaxSend(t *testing.T) {
	pool, cert := testCA(t, "api.example.test")
	api := &seen{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(api.handler))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, srv.Listener.Addr().String())
	}
	p, err := StartProxy(ProxyConfig{Hosts: []string{"api.example.test"}, Token: "tok-maxsend", MaxSend: 1 << 10, Dial: dial, RootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	trust := x509.NewCertPool()
	trust.AppendCertsFromPEM(p.CAPEM())
	pu, _ := url.Parse(p.URL())
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: trust}}}
	post := func(body io.Reader, length int64) int {
		req, _ := http.NewRequest("POST", "https://api.example.test/echo", body)
		req.ContentLength = length
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	if code := post(strings.NewReader("small"), 5); code != 200 {
		t.Fatalf("a small request goes: %d", code)
	}
	n := api.count()
	if code := post(strings.NewReader(strings.Repeat("x", 4096)), 4096); code != http.StatusRequestEntityTooLarge || api.count() != n {
		t.Fatalf("a large request is refused before it leaves: %d", code)
	}
	if code := post(io.MultiReader(strings.NewReader(strings.Repeat("y", 4096))), -1); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a large request of unknown length is stopped: %d", code)
	}
}

// TestNothingOutlivesTheRun: a child the program leaves running is killed
// when the program ends (it used to write into the room after the run).
func TestNothingOutlivesTheRun(t *testing.T) {
	w := newWorld(t, NetOff)
	late := filepath.Join(w.room.Root, "tmp", "late.txt")
	start := time.Now()
	out, err := w.run(t, "/bin/sh", "-c", "(sleep 2; echo late > "+late+") & echo started")
	if err != nil || !strings.Contains(out, "started") || time.Since(start) > 10*time.Second {
		t.Fatalf("%v %q after %v", err, out, time.Since(start))
	}
	time.Sleep(3 * time.Second)
	if _, err := os.Stat(late); err == nil {
		t.Fatal("a child outlived the run and wrote into the room")
	}
}

// TestNoTerminal: even started from a person's terminal, a sandboxed program
// has none — it can't open /dev/tty to read from it or type into it. The test
// runs itself again under script(1), which gives it a terminal.
func TestNoTerminal(t *testing.T) {
	if os.Getenv("METARUSH_TTY_PROBE") == "1" {
		w := newWorld(t, NetOff)
		py := pythonWith(t, "")
		out, _ := w.run(t, py, "-c", `import os
try:
    os.open("/dev/tty", os.O_RDWR); print("TTY-OPENED")
except Exception as e:
    print("open refused", type(e).__name__)`)
		fmt.Println("PROBE:", strings.TrimSpace(out))
		return
	}
	if _, err := os.Stat("/usr/bin/script"); err != nil {
		t.Skip("no script(1)")
	}
	self, _ := os.Executable()
	args := []string{"-q", "/dev/null", self, "-test.run=^TestNoTerminal$", "-test.count=1"}
	if runtime.GOOS == "linux" {
		args = []string{"-qec", self + " -test.run=^TestNoTerminal$ -test.count=1", "/dev/null"}
	}
	cmd := exec.Command("/usr/bin/script", args...)
	cmd.Env = append(os.Environ(), "METARUSH_TTY_PROBE=1")
	b, err := cmd.CombinedOutput()
	s := string(b)
	if strings.Contains(s, "no sandbox on this computer") || strings.Contains(s, "no python3") {
		t.Skip("no sandbox or python3 here")
	}
	if strings.Contains(s, "TTY-OPENED") || !strings.Contains(s, "PROBE: open refused") {
		t.Fatalf("a sandboxed program opened the terminal: %v\n%s", err, s)
	}
}

// TestOpenNetwork: an open network is any public host, still through
// MetaRush: not this computer's other ports (the database, the dashboard),
// and nothing listens (review 7).
func TestOpenNetwork(t *testing.T) {
	ts := tools.New(config.Tools{})
	iso := Here(ts)
	if !iso.OK() {
		t.Skip("no sandbox here")
	}
	py := pythonWith(t, "")
	pool, cert := testCA(t, "anything.example.test")
	api := &seen{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(api.handler))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	local, err := net.Listen("tcp", "127.0.0.1:0") // stands for the database on this computer
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, srv.Listener.Addr().String())
	}
	p, err := StartProxy(ProxyConfig{AnyHost: true, Token: "tok-open", Dial: dial, RootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	root, _ := filepath.EvalSymlinks(t.TempDir())
	for _, d := range []string{"home", "tmp", ".metarush"} {
		_ = os.MkdirAll(filepath.Join(root, d), 0o700)
	}
	ca, preload, pyDir, err := WriteHelpers(filepath.Join(root, ".metarush"), p.CAPEM())
	if err != nil {
		t.Fatal(err)
	}
	r := &Room{Root: root, Work: root, Read: InstallationOf(py), Hide: HiddenFor(nil), Net: NetOpen, ProxyPort: p.Port()}
	if iso.Kind == Linux {
		r.ProxySocket = filepath.Join(root, ".metarush", "proxy.sock")
		if err := p.ListenUnix(r.ProxySocket); err != nil {
			t.Fatal(err)
		}
		r.Self, _ = os.Executable()
	}
	port := local.Addr().(*net.TCPAddr).Port
	tool, argv, err := r.Command(iso.Kind, py, []string{"-c", `import socket, sys, urllib.request
print("public:", urllib.request.urlopen("https://anything.example.test/echo", timeout=20).status)
try:
    socket.create_connection(("127.0.0.1", int(sys.argv[1])), timeout=3); print("LOCAL-REACHED")
except Exception as e:
    print("local refused")
try:
    s = socket.socket(); s.bind(("0.0.0.0", 0)); s.listen(1); print("LISTENING")
except Exception as e:
    print("listen refused")
`, strconv.Itoa(port)})
	if err != nil {
		t.Fatal(err)
	}
	env := append(BaseEnv(nil, filepath.Join(root, "home"), filepath.Join(root, "tmp")), ProxyEnv(p.URL(), ca, preload, pyDir)...)
	res, err := ts.Run(context.Background(), tools.Spec{Tool: tool, Args: argv, Dir: root, CleanEnv: true, Env: env, Contained: true})
	out := ""
	if res != nil {
		out = string(res.Stdout) + string(res.Stderr)
	}
	if err != nil || !strings.Contains(out, "public: 200") || strings.Contains(out, "LOCAL-REACHED") || strings.Contains(out, "LISTENING") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// keyWorld is a proxy with one key and a host that repeats what it gets in
// every way a careless service might (no sandbox needed).
func keyWorld(t *testing.T, key ProxyKey, maxSend int64) (*http.Client, *seen) {
	t.Helper()
	pool, cert := testCA(t, "api.example.test")
	api := &seen{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		api.reqs = append(api.reqs, map[string]string{"path": r.URL.Path, "auth": r.Header.Get("Authorization"), "q": r.URL.Query().Get("q")})
		api.mu.Unlock()
		q := r.URL.Query().Get("q")
		if q == "" {
			q = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		switch r.URL.Path {
		case "/gz":
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			_, _ = io.WriteString(gz, q)
			_ = gz.Close()
		case "/br":
			w.Header().Set("Content-Encoding", "br")
			_, _ = io.WriteString(w, "\x0b\x02\x80"+q)
		case "/url":
			_, _ = io.WriteString(w, r.URL.RawQuery)
		case "/forms":
			j, _ := json.Marshal(map[string]string{"k": q})
			_, _ = io.WriteString(w, string(j)+" "+strings.ReplaceAll(string(j), "/", `\/`)+" "+base64.StdEncoding.EncodeToString([]byte(q))+" "+base64.RawURLEncoding.EncodeToString([]byte(q)))
		default:
			_, _ = io.WriteString(w, "ok")
		}
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, srv.Listener.Addr().String())
	}
	p, err := StartProxy(ProxyConfig{Hosts: []string{"api.example.test"}, Token: "tok-keys", MaxSend: maxSend, Dial: dial, RootCAs: pool, Keys: []ProxyKey{key}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	trust := x509.NewCertPool()
	trust.AppendCertsFromPEM(p.CAPEM())
	pu, _ := url.Parse(p.URL())
	return &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu), DisableCompression: true, TLSClientConfig: &tls.Config{RootCAs: trust}}}, api
}

func call(t *testing.T, c *http.Client, method, path string, hdr map[string]string, body string) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, "https://api.example.test"+path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "text/plain")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Content-Encoding") + "|" + string(b)
}

// TestKeysNeverComeBack: whatever form a host repeats a key in — compressed,
// escaped for an address or for JSON, in base64 — the program gets the
// stand-in; a compression MetaRush can't open is refused (review 2).
func TestKeysNeverComeBack(t *testing.T) {
	const real = "rk/abc+def=ghij0123456789" // secret-scan: allow (made up)
	const stand = "mrkey-demo-0000aaaa"
	c, api := keyWorld(t, ProxyKey{Name: "demo", Value: real, StandIn: stand, Hosts: []string{"api.example.test"}, Query: "q"}, 0)
	leaks := func(s string) bool {
		for _, f := range KeyForms(real) {
			if strings.Contains(s, f) {
				return true
			}
		}
		return false
	}
	for _, path := range []string{"/gz", "/url", "/forms"} {
		code, got := call(t, c, "GET", path+"?q="+stand, nil, "")
		if code != 200 || leaks(got) || !strings.Contains(got, stand) {
			t.Errorf("%s: %d %q", path, code, got)
		}
		if api.last()["q"] != real {
			t.Fatalf("%s: the host got %q, not the key", path, api.last()["q"])
		}
	}
	if code, got := call(t, c, "GET", "/br?q="+stand, nil, ""); code != http.StatusBadGateway || leaks(got) {
		t.Errorf("a compression MetaRush can't open is refused: %d %q", code, got)
	}
}

// TestKeysOnlyInTheirPlace: a stand-in anywhere but the key's own place is
// refused before anything leaves (review 2).
func TestKeysOnlyInTheirPlace(t *testing.T) {
	const real = "hk-0123456789abcdef0123" // secret-scan: allow (made up)
	const stand = "mrkey-head-1111bbbb"
	c, api := keyWorld(t, ProxyKey{Name: "demo", Value: real, StandIn: stand, Hosts: []string{"api.example.test"}}, 0)
	if code, _ := call(t, c, "GET", "/ok", map[string]string{"Authorization": "Bearer " + stand}, ""); code != 200 || api.last()["auth"] != "Bearer "+real {
		t.Fatalf("its own place (the Authorization header): %d %v", code, api.last())
	}
	n := api.count()
	for name, try := range map[string]func() int{
		"another header": func() int { c, _ := call(t, c, "GET", "/ok", map[string]string{"X-Echo": stand}, ""); return c },
		"the query":      func() int { c, _ := call(t, c, "GET", "/url?q="+stand, nil, ""); return c },
		"the path":       func() int { c, _ := call(t, c, "GET", "/"+stand, nil, ""); return c },
		"the body":       func() int { c, _ := call(t, c, "POST", "/ok", nil, "key="+stand); return c },
	} {
		if code := try(); code != http.StatusForbidden {
			t.Errorf("a stand-in in %s: %d", name, code)
		}
	}
	if api.count() != n {
		t.Fatal("a request with the stand-in out of place reached the host")
	}
}

// TestSendBudget: a words-only run sends at most its budget in all —
// request lines, headers and bodies — and no request carries huge headers
// (review 3).
func TestSendBudget(t *testing.T) {
	c, api := keyWorld(t, ProxyKey{Name: "demo", Value: "bk-0123456789abcdef0123", StandIn: "mrkey-budget-22", Hosts: []string{"api.example.test"}}, 4<<10) // secret-scan: allow (made up)
	if code, _ := call(t, c, "GET", "/ok", nil, ""); code != 200 {
		t.Fatalf("a small request: %d", code)
	}
	n := api.count()
	if code, _ := call(t, c, "GET", "/ok", map[string]string{"X-Data": strings.Repeat("A", 500<<10)}, ""); code == 200 || api.count() != n {
		t.Fatalf("500 KB in a header: %d, the host got %d more", code, api.count()-n)
	}
	if code, _ := call(t, c, "GET", "/ok?"+strings.Repeat("w=x&", 1200), nil, ""); code != http.StatusRequestEntityTooLarge || api.count() != n {
		t.Fatalf("a long address counts too: %d", code)
	}
	if code, _ := call(t, c, "GET", "/ok", nil, ""); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("the budget is the run's: %d", code)
	}
}

// TestLocalService: the one service on this computer an owner agreed to
// (D284) is reached over plain HTTP on its port, by name or address; another
// port, HTTPS to it and a proxy without the allowance are refused.
func TestLocalService(t *testing.T) {
	hits := map[string]int{}
	var mu sync.Mutex
	serve := func(name string) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits[name]++
			mu.Unlock()
			_, _ = io.WriteString(w, name)
		}))
		t.Cleanup(s.Close)
		return s
	}
	svc, other := serve("service"), serve("other")
	port := svc.Listener.Addr().(*net.TCPAddr).Port
	otherPort := other.Listener.Addr().(*net.TCPAddr).Port
	get := func(p *Proxy, target string) (int, string) {
		pu, _ := url.Parse(p.URL())
		trust := x509.NewCertPool()
		trust.AppendCertsFromPEM(p.CAPEM())
		client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu), TLSClientConfig: &tls.Config{RootCAs: trust}}}
		resp, err := client.Get(target)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	p, err := StartProxy(ProxyConfig{LocalPort: port, Token: "tok-local"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	for _, host := range []string{"127.0.0.1", "localhost"} {
		if code, body := get(p, fmt.Sprintf("http://%s:%d/prompt", host, port)); code != 200 || body != "service" {
			t.Fatalf("%s:%d: %d %s", host, port, code, body)
		}
	}
	if code, _ := get(p, fmt.Sprintf("http://127.0.0.1:%d/", otherPort)); code != http.StatusForbidden {
		t.Fatalf("another port on this computer: %d", code)
	}
	if code, _ := get(p, fmt.Sprintf("https://127.0.0.1:%d/", port)); code == 200 {
		t.Fatal("HTTPS to the service is refused")
	}
	if code, _ := get(p, "http://10.0.0.1:"+strconv.Itoa(port)+"/"); code != http.StatusForbidden {
		t.Fatalf("a local network's address on the same port: %d", code)
	}
	if hits["other"] != 0 || hits["service"] != 2 {
		t.Fatalf("what the services got: %v", hits)
	}
	reqs, _ := p.Requests()
	if len(reqs) < 4 || reqs[0].Host != "127.0.0.1" || reqs[0].Refused != "" || reqs[0].Path != "/prompt" {
		t.Fatalf("the requests are recorded: %+v", reqs)
	}
	// A words-only run's budget doesn't take what goes to the service (it
	// never leaves this computer): a ComfyUI run polls for minutes.
	tight, err := StartProxy(ProxyConfig{LocalPort: port, Token: "tok-tight", MaxSend: 1 << 10, MaxRequests: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tight.Close() })
	pu, _ := url.Parse(tight.URL())
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	for i := 0; i < 5; i++ {
		resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/prompt", port), "application/json", strings.NewReader(strings.Repeat("x", 4096)))
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("request %d to the service under a words-only budget: %v %v", i, err, resp)
		}
		resp.Body.Close()
	}
	hits["service"] -= 5
	// Without the allowance, the same address is refused like any other.
	plain, err := StartProxy(ProxyConfig{Hosts: []string{"api.example.test"}, Token: "tok-none"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plain.Close() })
	if code, _ := get(plain, fmt.Sprintf("http://127.0.0.1:%d/", port)); code != http.StatusForbidden || hits["service"] != 2 {
		t.Fatalf("a run without the allowance: %d", code)
	}
	// A client's rules about what may leave (D290) refuse the run's hosts,
	// never the service: what goes there stays on this computer.
	var asked []string
	kept, err := StartProxy(ProxyConfig{LocalPort: port, Token: "tok-kept", Refuse: func(h string) string {
		asked = append(asked, h)
		return "Nothing goes to " + h + "."
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = kept.Close() })
	if code, body := get(kept, fmt.Sprintf("http://localhost:%d/prompt", port)); code != 200 || body != "service" || len(asked) != 0 {
		t.Fatalf("the service under rules that keep everything here: %d %s (asked about %v)", code, body, asked)
	}
	if !ThisComputer("127.0.0.1") || !ThisComputer("LOCALHOST.") || !ThisComputer("[::1]") || ThisComputer("10.0.0.1") || ThisComputer("api.example.test") {
		t.Fatal("this computer's own names")
	}
}

// TestProxyBreaksOffAnswersPastTheLimit: an answer past the run's size limit
// fails in the program, never arrives as a shorter file that looks whole;
// the record says why, with the port.
func TestProxyBreaksOffAnswersPastTheLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "65536")
		_, _ = w.Write(bytes.Repeat([]byte("v"), 65536))
	}))
	t.Cleanup(srv.Close)
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, srv.Listener.Addr().String())
	}
	p, err := StartProxy(ProxyConfig{Hosts: []string{"files.example.test"}, Token: "tok-big", MaxBytes: 8 << 10, Dial: dial})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	pu, _ := url.Parse(p.URL())
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	resp, err := client.Get("http://files.example.test/big.mp4")
	if err == nil {
		_, err = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("an answer past the limit fails in the program")
	}
	reqs, _ := p.Requests()
	if len(reqs) != 1 || reqs[0].Refused == "" || reqs[0].Port != 80 {
		t.Fatalf("the record: %+v", reqs)
	}
}
