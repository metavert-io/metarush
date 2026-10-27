package localdb

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

func TestTargetFor(t *testing.T) {
	cases := []struct {
		goos, arch, rel, want string
	}{
		{"darwin", "arm64", "", "darwin/arm64"},
		{"darwin", "amd64", "", "darwin/amd64"},
		{"linux", "amd64", "ID=ubuntu\nVERSION_ID=\"24.04\"\nUBUNTU_CODENAME=noble\n", "linux/amd64/ubuntu2404"},
		{"linux", "arm64", "ID=ubuntu\nVERSION_ID=\"22.04\"\nUBUNTU_CODENAME=jammy\n", "linux/arm64/ubuntu2204"},
		{"linux", "amd64", "ID=ubuntu\nVERSION_ID=\"26.04\"\n", "linux/amd64/ubuntu2404"},
		{"linux", "amd64", "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=\"22\"\nUBUNTU_CODENAME=jammy\n", "linux/amd64/ubuntu2204"},
		{"linux", "amd64", "ID=debian\nVERSION_ID=\"12\"\n", "linux/amd64/debian12"},
		{"linux", "amd64", "ID=debian\nVERSION_ID=\"13\"\n", "linux/amd64/debian12"},
		{"linux", "amd64", "ID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\nVERSION_ID=\"9.4\"\n", "linux/amd64/rhel93"},
		{"linux", "arm64", "ID=\"almalinux\"\nVERSION_ID=\"8.10\"\n", "linux/arm64/rhel8"},
		{"linux", "amd64", "ID=\"amzn\"\nVERSION_ID=\"2023\"\n", "linux/amd64/amazon2023"},
		{"linux", "amd64", "ID=fedora\nVERSION_ID=41\n", ""},
		{"linux", "amd64", "ID=debian\nVERSION_ID=\"11\"\n", ""},
		{"windows", "amd64", "", ""},
	}
	for _, c := range cases {
		got, ok := TargetFor(c.goos, c.arch, c.rel)
		if c.want == "" {
			if ok {
				t.Errorf("%s/%s %q: got %s, want no build", c.goos, c.arch, c.rel, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("%s/%s %q: got %s (%v), want %s", c.goos, c.arch, c.rel, got, ok, c.want)
		}
	}
	// Debian on ARM has no build of its own: the target exists, the archive doesn't.
	if tgt, _ := TargetFor("linux", "arm64", "ID=debian\nVERSION_ID=12\n"); func() bool { _, ok := Current.For(tgt); return ok }() {
		t.Errorf("Debian on ARM should have no pinned archive")
	}
}

// Every pinned archive is MongoDB's own, on its download server, for the
// pinned version, with a SHA-256.
func TestPinsAreMongoDBs(t *testing.T) {
	hexRe := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, target := range []string{"darwin/arm64", "darwin/amd64", "linux/amd64/ubuntu2204", "linux/arm64/ubuntu2404"} {
		if _, ok := Current.For(target); !ok {
			t.Errorf("no archive for %s", target)
		}
	}
	for target, a := range Current.Archives {
		if !strings.HasPrefix(a.URL, "https://fastdl.mongodb.org/") || !strings.Contains(a.URL, "-"+Current.Version+".tgz") {
			t.Errorf("%s: %s is not MongoDB's archive of %s", target, a.URL, Current.Version)
		}
		if !hexRe.MatchString(a.SHA256) {
			t.Errorf("%s: SHA-256 %q", target, a.SHA256)
		}
	}
	if Series(Current.Version) != "8.0" {
		t.Errorf("series of %s = %s", Current.Version, Series(Current.Version))
	}
}

func TestPortsNeverTheTestDatabase(t *testing.T) {
	if ValidPort(TestPort) || ValidPort(0) || ValidPort(80) {
		t.Fatal("the test database's port, 0 and 80 must not be valid")
	}
	p, err := PickPort(t.TempDir())
	if err != nil {
		t.Skip(err)
	}
	if p == TestPort || p < PortFirst || p > PortLast {
		t.Fatalf("picked %d", p)
	}
	// A port in use is skipped.
	l, err := net.Listen("tcp", "127.0.0.1:"+itoa(p))
	if err != nil {
		t.Skip(err)
	}
	defer l.Close()
	if q, err := PickPort(t.TempDir()); err == nil && q == p {
		t.Fatalf("picked %d although it is in use", q)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// Two homes on this computer never get the same port, even while one of
// them is stopped: the port each chose is remembered (and forgotten once
// that home's database is gone).
func TestPortsAreRememberedPerComputer(t *testing.T) {
	reg := filepath.Join(t.TempDir(), "database-ports.json")
	old := registryFile
	registryFile = func() string { return reg }
	t.Cleanup(func() { registryFile = old })
	a, b := New(t.TempDir(), 0, nil), New(t.TempDir(), 0, nil)
	p, err := PickPort(a.Home)
	if err != nil {
		t.Skip(err)
	}
	a.Port = p
	if err := reserve(a.Home, p); err != nil {
		t.Fatal(err)
	}
	if err := a.writeState(State{Version: "8.0.34", Port: p}); err != nil {
		t.Fatal(err)
	}
	// a isn't running: the port is free, and still a's.
	q, err := PickPort(b.Home)
	if err != nil || q == p {
		t.Fatalf("b picked %d (a's is %d): %v", q, p, err)
	}
	if err := reserve(b.Home, p); err == nil || !strings.Contains(err.Error(), "another MetaRush on this computer") {
		t.Errorf("b took a's port: %v", err)
	}
	if again, _ := PickPort(a.Home); again != p {
		t.Errorf("a's own port isn't a's any more: %d", again)
	}
	// a's database gone: its port is free for others again.
	if err := os.RemoveAll(a.Dir()); err != nil {
		t.Fatal(err)
	}
	if err := reserve(b.Home, p); err != nil {
		t.Errorf("a port no home keeps: %v", err)
	}
}

// fakeArchive is a MongoDB-shaped archive whose mongod is a script that says
// its version, plus entries that must never be written.
func fakeArchive(t *testing.T, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, mode int64, body string, typ byte) {
		h := &tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: typ}
		if typ == tar.TypeSymlink {
			h.Size, h.Linkname = 0, "/etc/passwd"
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	top := "mongodb-test-" + version + "/"
	add(top+"bin/mongod", 0o755, "#!/bin/sh\necho \"db version v"+version+"\"\n", tar.TypeReg)
	add(top+"bin/mongos", 0o755, "#!/bin/sh\n", tar.TypeReg)
	add(top+"LICENSE-Community.txt", 0o644, "Server Side Public License\n", tar.TypeReg)
	add(top+"../../escape", 0o644, "nope\n", tar.TypeReg)
	add("evil/../../bin/mongod", 0o755, "nope\n", tar.TypeReg)
	add(top+"README", 0o644, "", tar.TypeSymlink)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func testServer(t *testing.T, home string, pin Pin) *Server {
	t.Helper()
	s := New(home, 0, tools.New(config.Tools{}))
	s.Pin, s.TargetName = &pin, "test/target"
	return s
}

func TestInstallChecksTheDownload(t *testing.T) {
	arch := fakeArchive(t, "9.9.1")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(arch)
	}))
	defer srv.Close()
	home := t.TempDir()
	good := Pin{Version: "9.9.1", Archives: map[string]Archive{"test/target": {URL: srv.URL + "/mongodb.tgz", SHA256: sha(arch)}}}

	// A download whose SHA-256 differs is never used.
	bad := good
	bad.Archives = map[string]Archive{"test/target": {URL: srv.URL + "/mongodb.tgz", SHA256: strings.Repeat("0", 64)}}
	err := testServer(t, home, bad).Install(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("a download with another SHA-256 was accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "database", "mongodb", "9.9.1")); !os.IsNotExist(err) {
		t.Fatalf("a refused download left files behind: %v", err)
	}

	s := testServer(t, home, good)
	var progressed bool
	if err := s.Install(context.Background(), func(done, total int64) { progressed = done > 0 }); err != nil {
		t.Fatal(err)
	}
	if !progressed {
		t.Error("no progress was reported")
	}
	dir := filepath.Join(home, "database", "mongodb", "9.9.1")
	for _, f := range []string{"bin/mongod", "LICENSE-Community.txt", "mongod.sha256"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s missing: %v", f, err)
		}
	}
	for _, f := range []string{"bin/mongos", "README"} {
		if _, err := os.Lstat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("%s was unpacked; only the server and its licence are kept", f)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "escape")); !os.IsNotExist(err) {
		t.Error("an entry escaped the folder")
	}
	if s.Tools.Path(tools.Mongod) != filepath.Join(dir, "bin", "mongod") {
		t.Errorf("the tool points at %q", s.Tools.Path(tools.Mongod))
	}
	// Installed: nothing is downloaded again.
	before := hits.Load()
	if err := s.Install(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != before {
		t.Error("an installed version was downloaded again")
	}
	// A changed program is noticed and replaced.
	if err := os.WriteFile(filepath.Join(dir, "bin", "mongod"), []byte("#!/bin/sh\necho changed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Install(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if hits.Load() == before {
		t.Error("a changed program was kept")
	}
	// No archive for this computer.
	none := testServer(t, t.TempDir(), Pin{Version: "9.9.1", Archives: map[string]Archive{}})
	if err := none.Install(context.Background(), nil); err != ErrNoBuild {
		t.Errorf("no archive: %v", err)
	}
}

// Tests never reach the internet: the real download server is refused.
func TestDownloadsAreOffInTests(t *testing.T) {
	s := testServer(t, t.TempDir(), Pin{Version: Current.Version, Archives: map[string]Archive{"test/target": Current.Archives["darwin/arm64"]}})
	err := s.Install(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "downloads are off in tests") {
		t.Fatalf("the real download was tried: %v", err)
	}
}

// A copy of the archive (an offline computer) is checked like a download.
func TestArchiveFromAFile(t *testing.T) {
	arch := fakeArchive(t, "9.9.2")
	f := filepath.Join(t.TempDir(), "copy.tgz")
	if err := os.WriteFile(f, arch, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvArchive, f)
	pin := Pin{Version: "9.9.2", Archives: map[string]Archive{"test/target": {URL: "https://fastdl.mongodb.org/never.tgz", SHA256: sha(arch)}}}
	if err := testServer(t, t.TempDir(), pin).Install(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	pin.Archives["test/target"] = Archive{URL: pin.Archives["test/target"].URL, SHA256: strings.Repeat("1", 64)}
	if err := testServer(t, t.TempDir(), pin).Install(context.Background(), nil); err == nil {
		t.Fatal("a copy with another SHA-256 was used")
	}
}

// A stand-in pin is accepted only for an address on this computer.
func TestPinFromEnvOnlyLoopback(t *testing.T) {
	dir := t.TempDir()
	write := func(url string) string {
		p := filepath.Join(dir, "pin.json")
		body := `{"version":"8.0.34","url":"` + url + `","sha256":"` + strings.Repeat("a", 64) + `"}`
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Setenv(EnvPin, write("https://example.invalid/mongodb.tgz"))
	if _, err := PinFromEnv(); err == nil {
		t.Fatal("a stand-in elsewhere than this computer was accepted")
	}
	t.Setenv(EnvPin, write("http://127.0.0.1:5461/mongodb.tgz"))
	p, err := PinFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	tgt, _ := Target()
	if a, ok := p.For(tgt); !ok || a.URL != "http://127.0.0.1:5461/mongodb.tgz" {
		t.Fatalf("stand-in pin: %+v", p)
	}
	t.Setenv(EnvPin, "")
	if p, _ := PinFromEnv(); p.Version != Current.Version {
		t.Fatal("without the variable the pin is Current")
	}
}

func TestUpgradePlans(t *testing.T) {
	home := t.TempDir()
	s := testServer(t, home, Pin{Version: "8.0.40", Archives: map[string]Archive{"test/target": {URL: "http://127.0.0.1:1/x.tgz", SHA256: strings.Repeat("a", 64)}}})
	if _, err := s.PlanUpgrade(); err != ErrNotInstalled {
		t.Fatalf("nothing set up: %v", err)
	}
	for _, c := range []struct {
		from, to string
		want     string // "" ok, else part of the error
		series   bool
	}{
		{"8.0.34", "8.0.40", "", false},
		{"8.0.34", "8.0.34", "up to date", false},
		{"8.0.40", "8.0.34", "up to date", false}, // never back
		{"7.0.12", "8.0.34", "", true},
		{"8.0.34", "8.2.1", "", true},
		{"6.0.1", "8.0.34", "one release at a time", true},
	} {
		if err := s.writeState(State{Version: c.from, Port: 27150, Initiated: true}); err != nil {
			t.Fatal(err)
		}
		pin := *s.Pin
		pin.Version = c.to
		s.Pin = &pin
		step, err := s.PlanUpgrade()
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s → %s: %v", c.from, c.to, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s → %s: want %q, got %v", c.from, c.to, c.want, err)
		case err == nil && step.SeriesChange != c.series:
			t.Errorf("%s → %s: series change %v", c.from, c.to, step.SeriesChange)
		}
	}
}

// realMongod is a real MongoDB server for the lifecycle test: the one
// METARUSH_TEST_MONGOD names, the development one (~/.local/mongodb), or one
// on PATH. Without one the test is skipped (the repository's CI runs MongoDB
// in a container).
func realMongod(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("METARUSH_TEST_MONGOD"); p != "" {
		return p
	}
	if h, err := os.UserHomeDir(); err == nil {
		if p := filepath.Join(h, ".local", "mongodb", "bin", "mongod"); fileExists(p) {
			return p
		}
	}
	if p, err := exec.LookPath("mongod"); err == nil {
		return p
	}
	t.Skip("no mongod on this computer (set METARUSH_TEST_MONGOD)")
	return ""
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// freePort is a port the system hands out (never the test database's).
func freePort(t *testing.T) int {
	t.Helper()
	for i := 0; i < 5; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		p := l.Addr().(*net.TCPAddr).Port
		l.Close()
		if p != TestPort {
			return p
		}
	}
	t.Fatal("no free port")
	return 0
}

// installReal puts a real server where Install would (a link to it: the
// original is never changed) with the SHA-256 Install records.
func installReal(t *testing.T, s *Server, bin string) string {
	t.Helper()
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		t.Skipf("mongod doesn't run here: %v", err)
	}
	m := regexp.MustCompile(`db version v(\d+\.\d+\.\d+)`).FindSubmatch(out)
	if m == nil {
		t.Skipf("mongod --version: %s", out)
	}
	v := string(m[1])
	dir := s.versionDir(v)
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bin, filepath.Join(dir, "bin", "mongod")); err != nil {
		t.Fatal(err)
	}
	sum, err := fileSHA(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mongod.sha256"), []byte(sum+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pin := Pin{Version: v, Archives: map[string]Archive{"test/target": {URL: "http://127.0.0.1:1/unused.tgz", SHA256: strings.Repeat("a", 64)}}}
	s.Pin = &pin
	return v
}

// The whole life of the database on this computer with a real server: set
// up (replica set, MetaRush's own sign-in), refused without it, stopped
// cleanly, started again, adopted when already running, up to date.
func TestLifecycleWithARealServer(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real database server")
	}
	bin := realMongod(t)
	home := t.TempDir()
	s := testServer(t, home, Pin{})
	s.Port = freePort(t)
	v := installReal(t, s, bin)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	t.Cleanup(func() { _ = s.Stop(context.Background()) })

	var kept, keptAdmin string
	const dbName = "metarush-test-localdb"
	fresh, err := s.Prepare(ctx, dbName, Saved{}, nil, keepInto(&kept, &keptAdmin))
	if err != nil {
		t.Fatalf("set up: %v (log: %s)", err, tail(s.LogFile()))
	}
	if !fresh || kept == "" || keptAdmin == "" || kept == keptAdmin {
		t.Fatalf("fresh %v, addresses kept %q %q", fresh, config.RedactURI(kept), config.RedactURI(keptAdmin))
	}
	if !strings.Contains(kept, "127.0.0.1:"+itoa(s.Port)) || !strings.Contains(kept, "replicaSet="+ReplSet) {
		t.Errorf("address %s", config.RedactURI(kept))
	}
	if !s.SignsIn(ctx, kept) || !s.SignsIn(ctx, keptAdmin) {
		t.Fatal("the kept addresses don't sign in")
	}
	// Without a sign-in nothing can be read or written (--auth).
	if !s.RequiresSignIn(ctx) {
		t.Error("the server answers without a sign-in")
	}
	anon, err := mongo.Connect(options.Client().ApplyURI("mongodb://127.0.0.1:" + itoa(s.Port) + "/?directConnection=true"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := anon.Database(dbName).Collection("x").InsertOne(ctx, bson.M{"a": 1}); err == nil {
		t.Error("a write without the sign-in was accepted")
	}
	if err := anon.Database(dbName).Collection("x").FindOne(ctx, bson.M{}).Err(); codeOf(err) != 13 {
		t.Errorf("a read without the sign-in: %v", err)
	}
	_ = anon.Disconnect(ctx)
	// MetaRush's own sign-in reads and writes its database, and nothing else.
	own, err := mongo.Connect(options.Client().ApplyURI(kept))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := own.Database("metarush-test-elsewhere").Collection("x").InsertOne(ctx, bson.M{"a": 1}); codeOf(err) != 13 {
		t.Errorf("MetaRush's sign-in wrote another database: %v", err)
	}
	if err := own.Database("admin").RunCommand(ctx, bson.D{{Key: "createUser", Value: "someone"}, {Key: "pwd", Value: "x"}, {Key: "roles", Value: bson.A{}}}).Err(); codeOf(err) != 13 {
		t.Errorf("MetaRush's sign-in made a user: %v", err)
	}
	if err := own.Database(dbName).CreateCollection(ctx, "made"); err != nil {
		t.Errorf("MetaRush's sign-in can't make a collection in its database: %v", err)
	}
	if _, err := own.Database(dbName).Collection("made").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "a", Value: 1}}}); err != nil {
		t.Errorf("MetaRush's sign-in can't make an index: %v", err)
	}
	_ = own.Disconnect(ctx)
	// Change streams and transactions need a primary of a replica set.
	c, err := mongo.Connect(options.Client().ApplyURI(kept))
	if err != nil {
		t.Fatal(err)
	}
	var h bson.M
	if err := c.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&h); err != nil || h["setName"] != ReplSet || h["isWritablePrimary"] != true {
		t.Errorf("hello: %v %v", h, err)
	}
	if _, err := c.Database(dbName).Collection("x").InsertOne(ctx, bson.M{"a": 1}); err != nil {
		t.Errorf("write with the sign-in: %v", err)
	}
	_ = c.Disconnect(ctx)

	st := s.Status(ctx)
	if !st.SetUp || !st.Running || st.Version != v || st.PID == 0 || st.UpgradeReady {
		t.Errorf("status %+v", st)
	}
	if info, err := os.Stat(s.keyFile()); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("key file: %v %v", info, err)
	}

	// Run again: nothing new is made; a second start adopts the running server.
	fresh, err = s.Prepare(ctx, dbName, Saved{URI: kept, AdminURI: keptAdmin}, nil, func(string, string) error { t.Error("a second sign-in was made"); return nil })
	if err != nil || fresh {
		t.Fatalf("again: fresh %v, %v", fresh, err)
	}
	pid := s.PID()
	if err := s.Start(ctx); err != nil || s.PID() != pid {
		t.Fatalf("a running server was started again: %v", err)
	}

	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if s.PID() != 0 || s.Answers(ctx) {
		t.Fatal("still running after Stop")
	}
	if err := s.Start(ctx); err != nil {
		t.Fatalf("start again: %v (log: %s)", err, tail(s.LogFile()))
	}
	if !s.SignsIn(ctx, kept) {
		t.Fatal("the sign-in was lost over a restart")
	}
	if _, err := s.PlanUpgrade(); err != ErrUpToDate {
		t.Errorf("upgrade plan: %v", err)
	}
	// The port is taken by something else: said plainly, never a second server.
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:"+itoa(s.Port))
	if err != nil {
		t.Fatal(err)
	}
	err = s.Start(ctx)
	l.Close()
	if err == nil || !strings.Contains(err.Error(), "another program") {
		t.Errorf("port in use: %v", err)
	}
}

func tail(p string) string {
	b, _ := os.ReadFile(p)
	if len(b) > 2000 {
		b = b[len(b)-2000:]
	}
	return string(b)
}
