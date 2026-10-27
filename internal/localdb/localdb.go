// Package localdb is the database on this computer (D254): a single-member
// MongoDB replica set that MetaRush downloads from MongoDB's own download
// server (MongoDB Community Server, pinned by version and SHA-256; never
// shipped inside MetaRush's archives, whose licence differs from the
// server's), starts, stops and upgrades under <home>/database, listening only
// on 127.0.0.1 at a port MetaRush picks and always requiring a sign-in
// (--auth and the replica set's key file): an administrator, used only for
// upgrades, and MetaRush's own sign-in, which may read and write MetaRush's
// database and nothing else; both passwords are generated at setup and kept
// in the keychain (or the home's .env) like any other database address.
//
// Whoever needs it starts it (setup, the daemon, a command in a terminal);
// the daemon keeps it running (Watch). It is never stopped with a kill: a
// signal asks it to shut down cleanly and MetaRush waits. An upgrade happens
// only when a person asks (`metarush database upgrade`), one release series
// at a time; MetaRush never goes back to an older one.
//
// mongod runs through internal/tools (allowlisted as a path-only tool, set
// with Use to the binary MetaRush unpacked). Under `go test` downloads go only
// to a stand-in server on this computer.
package localdb

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/tools"
)

const (
	toolName = tools.Mongod
	// ReplSet is the replica set's name: one member, so change streams and
	// transactions work as they do on MongoDB Atlas.
	ReplSet = "metarush"
	// User is the sign-in MetaRush works with every day: allowed to read and
	// write MetaRush's own database and nothing else (a password it
	// generates, kept where the database address is kept).
	User = "metarush"
	// AdminUser is the database's administrator, made first and used only
	// to make User and to move the database to a new release series
	// (upgrades); its address is a secret of its own.
	AdminUser = "metarush-admin"
	// PortFirst..PortLast is where MetaRush looks for a free port.
	PortFirst, PortLast = 27150, 27199
	// TestPort is the repository's test and development database: never used.
	TestPort = 27018
)

// ErrNotInstalled means the database on this computer isn't set up.
var ErrNotInstalled = errors.New("the database on this computer isn't set up")

// Server is one home's database on this computer.
type Server struct {
	Home  string // the installation home
	Port  int    // its port (database.local.port in config.yaml)
	Tools *tools.Set
	// Client downloads (default: a client with a 30-minute timeout).
	Client *http.Client
	// Pin replaces the pinned version (tests); nil is PinFromEnv.
	Pin *Pin
	// OlderPin replaces Older (tests); nil is Older, or EnvPin's stand-in.
	OlderPin *Pin
	// Kernel replaces this computer's Linux kernel (tests); nil is ReadKernel().
	Kernel *Kernel
	// TargetName replaces this computer's target (tests); "" is Target().
	TargetName string
	// CacheGB is the server's memory for data (default 1).
	CacheGB float64
}

// New is the database on this computer for a home.
func New(home string, port int, ts *tools.Set) *Server {
	return &Server{Home: home, Port: port, Tools: ts}
}

// pin is the version this computer sets up: the current pin, or Older where
// this computer's Linux kernel keeps the current one from running (D339).
func (s *Server) pin() Pin {
	cur := s.current()
	if KernelRefuses(cur.Version, s.kernel()) {
		if o, ok := s.older(); ok {
			if _, ok := o.For(s.target()); ok {
				return o
			}
		}
	}
	return cur
}

// current is the version this MetaRush comes with, whatever the kernel.
func (s *Server) current() Pin {
	if s.Pin != nil {
		return *s.Pin
	}
	p, err := PinFromEnv()
	if err != nil {
		return Pin{Version: Current.Version}
	}
	return p
}

func (s *Server) older() (Pin, bool) {
	if s.OlderPin != nil {
		return *s.OlderPin, true
	}
	if s.Pin != nil {
		return Pin{}, false // a test's own pin stands alone
	}
	return olderFromEnv()
}

func (s *Server) kernel() Kernel {
	if s.Kernel != nil {
		return *s.Kernel
	}
	return ReadKernel()
}

// ErrKernel is the cause of every error that says MongoDB can't run on this
// computer's Linux kernel (errors.Is finds it under the plain words).
var ErrKernel = errors.New("MongoDB can't run on this computer's Linux kernel (SERVER-121912)")

// KernelNote says, before anything is downloaded, that this computer's Linux
// kernel keeps the MongoDB this MetaRush comes with from running, and what
// the database on this computer gets instead ("" when nothing is in the way).
func (s *Server) KernelNote() string {
	cur, k := s.current(), s.kernel()
	if !KernelRefuses(cur.Version, k) {
		return ""
	}
	why, todo := kernelWhy(cur.Version, k)
	if p := s.pin(); p.Version != cur.Version {
		return why + " So MetaRush sets up MongoDB " + p.Version + " instead: it works the same for MetaRush, and an upgrade moves it on to " +
			Series(cur.Version) + " once " + Series(cur.Version) + " runs here."
	}
	return why + " " + todo
}

func (s *Server) target() string {
	if s.TargetName != "" {
		return s.TargetName
	}
	t, _ := Target()
	return t
}

// Available reports whether MongoDB makes its server for this computer.
func (s *Server) Available() bool {
	_, ok := s.pin().For(s.target())
	return ok
}

// PinnedVersion is the version this MetaRush sets up.
func (s *Server) PinnedVersion() string { return s.pin().Version }

// DownloadMB is about how big the download is.
func (s *Server) DownloadMB() int { return s.pin().SizeMB }

// Dir is <home>/database.
func (s *Server) Dir() string { return filepath.Join(s.Home, "database") }

// DataDir holds the data.
func (s *Server) DataDir() string { return filepath.Join(s.Dir(), "data") }

// LogFile is the server's own log.
func (s *Server) LogFile() string { return filepath.Join(s.Dir(), "mongod.log") }

func (s *Server) outFile() string   { return filepath.Join(s.Dir(), "mongod.out") }
func (s *Server) pidFile() string   { return filepath.Join(s.Dir(), "mongod.pid") }
func (s *Server) keyFile() string   { return filepath.Join(s.Dir(), "keyfile") }
func (s *Server) statePath() string { return filepath.Join(s.Dir(), "state.json") }
func (s *Server) lockPath() string  { return filepath.Join(s.Dir(), "start.lock") }
func (s *Server) binary(v string) string {
	return filepath.Join(s.versionDir(v), "bin", "mongod")
}
func (s *Server) versionDir(v string) string { return filepath.Join(s.Dir(), "mongodb", v) }

// State is <home>/database/state.json: what is set up.
type State struct {
	Version   string `json:"version"`
	Port      int    `json:"port"`
	Initiated bool   `json:"initiated"` // the replica set and MetaRush's sign-in are made
	// FeatureVersion is the release series MongoDB works as (its feature
	// compatibility version, "8.0"), as MetaRush last set it.
	FeatureVersion string `json:"featureVersion,omitempty"`
	// FeaturePending is the series an upgrade still has to move the data
	// on to (it stopped after the new server started): offered again.
	FeaturePending string    `json:"featurePending,omitempty"`
	SetUpAt        time.Time `json:"setUpAt"`
	// UpgradedFrom is the version before the last upgrade.
	UpgradedFrom string     `json:"upgradedFrom,omitempty"`
	UpgradedAt   *time.Time `json:"upgradedAt,omitempty"`
}

// ReadState reads what is set up; ok is false when nothing is.
func (s *Server) ReadState() (State, bool) {
	var st State
	b, err := os.ReadFile(s.statePath())
	if err != nil || json.Unmarshal(b, &st) != nil || st.Version == "" {
		return st, false
	}
	return st, true
}

func (s *Server) writeState(st State) error {
	if err := os.MkdirAll(s.Dir(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	tmp := s.statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.statePath())
}

// ValidPort reports whether a port may be the database's.
func ValidPort(p int) bool { return p > 1024 && p < 65536 && p != TestPort }

func portFree(p int) bool {
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// URI is the address MetaRush connects with, its sign-in included.
func (s *Server) URI(password string) string { return s.uriFor(User, password) }

// AdminURI is the administrator's address (upgrades only).
func (s *Server) AdminURI(password string) string { return s.uriFor(AdminUser, password) }

func (s *Server) uriFor(user, password string) string {
	u := url.URL{Scheme: "mongodb", User: url.UserPassword(user, password), Host: "127.0.0.1:" + strconv.Itoa(s.Port), Path: "/",
		RawQuery: "authSource=admin&replicaSet=" + ReplSet + "&directConnection=true"}
	return u.String()
}

// NewPassword makes a password for MetaRush's sign-in.
func NewPassword() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

/* ---------- setting it up: setup.go ---------- */

// RequiresSignIn reports whether the server refuses a connection without a
// sign-in (it always should: doctor checks it).
func (s *Server) RequiresSignIn(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := s.connect(ctx, "")
	if err != nil {
		return false
	}
	defer c.Disconnect(context.Background())
	err = c.Database("admin").RunCommand(ctx, bson.D{{Key: "listDatabases", Value: 1}, {Key: "nameOnly", Value: true}}).Err()
	return codeOf(err) == 13 // Unauthorized
}

func waitPrimary(ctx context.Context, admin *mongo.Database, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		var h struct {
			Primary bool `bson:"isWritablePrimary"`
		}
		if err := admin.RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&h); err == nil && h.Primary {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("the database didn't become ready in time")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func codeOf(err error) int32 {
	var ce mongo.CommandError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return 0
}

// connect opens a client to the server: uri "" connects without a sign-in
// (enough to ask whether it answers, and for the first-sign-in exception).
func (s *Server) connect(ctx context.Context, uri string) (*mongo.Client, error) {
	if uri == "" {
		uri = "mongodb://127.0.0.1:" + strconv.Itoa(s.Port) + "/?directConnection=true"
	}
	return mongo.Connect(options.Client().ApplyURI(uri).SetServerSelectionTimeout(3 * time.Second).SetConnectTimeout(3 * time.Second))
}

// SignsIn reports whether an address signs in to the server (the driver
// signs in when it connects; connectionStatus names who it is).
func (s *Server) SignsIn(ctx context.Context, uri string) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c, err := s.connect(ctx, uri)
	if err != nil {
		return false
	}
	defer c.Disconnect(context.Background())
	var res struct {
		AuthInfo struct {
			Users []bson.M `bson:"authenticatedUsers"`
		} `bson:"authInfo"`
	}
	if err := c.Database("admin").RunCommand(ctx, bson.D{{Key: "connectionStatus", Value: 1}}).Decode(&res); err != nil {
		return false
	}
	return len(res.AuthInfo.Users) > 0
}

/* ---------- running it ---------- */

// Answers reports whether the server on this home's port answers.
func (s *Server) Answers(ctx context.Context) bool {
	_, ok := s.hello(ctx)
	return ok
}

// hello asks the server on the port who it is (no sign-in needed).
func (s *Server) hello(ctx context.Context) (bson.M, bool) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	c, err := s.connect(ctx, "")
	if err != nil {
		return nil, false
	}
	defer c.Disconnect(context.Background())
	var h bson.M
	if err := c.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&h); err != nil {
		return nil, false
	}
	return h, true
}

// PID is the running server's process, 0 when none runs.
func (s *Server) PID() int {
	b, err := os.ReadFile(s.pidFile())
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 || !alive(pid) || !s.isOurs(pid) {
		return 0
	}
	return pid
}

// isOurs reports whether a process is this home's database server: the
// program MetaRush unpacked under <home>/database, running on this home's
// data folder. A process number in an old pid file can belong to anything
// after a restart — another home's server, the development database —
// and nothing that fails this check is ever signalled.
func (s *Server) isOurs(pid int) bool {
	exe, args, ok := procInfo(pid)
	if !ok {
		return false
	}
	dbpath := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--dbpath" {
			dbpath = args[i+1]
		}
	}
	if dbpath == "" || !samePath(dbpath, s.DataDir()) {
		return false
	}
	if under(exe, s.Dir()) {
		return true
	}
	st, ok := s.ReadState()
	return ok && sameFile(exe, s.binary(st.Version))
}

func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

func under(p, dir string) bool {
	for _, d := range []string{dir, resolved(dir)} {
		for _, q := range []string{p, resolved(p)} {
			if strings.HasPrefix(filepath.Clean(q), filepath.Clean(d)+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func sameFile(a, b string) bool {
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Start starts the server when it isn't running and waits until it answers
// (a server still recovering after an unclean stop is waited for, never
// started twice). Concurrent callers wait for each other.
func (s *Server) Start(ctx context.Context) error {
	st, ok := s.ReadState()
	if !ok {
		return ErrNotInstalled
	}
	if s.Port == 0 {
		s.Port = st.Port
	}
	unlock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return s.startLocked(ctx, st)
}

// startLocked is Start for a caller holding the lock.
func (s *Server) startLocked(ctx context.Context, st State) error {
	pid := s.PID()
	if pid > 0 {
		if s.Answers(ctx) {
			return nil
		}
		// Ours, but not answering yet: it is starting or recovering.
		return s.waitReady(ctx, pid, nil, 5*time.Minute)
	}
	// A MongoDB this kernel stops (or would crash): said before it starts (D339).
	if k := s.kernel(); KernelRefuses(st.Version, k) {
		return cantStart(st.Version, k)
	}
	if s.Answers(ctx) || !portFree(s.Port) {
		return fmt.Errorf("another program on this computer uses the database's port (%d)", s.Port)
	}
	if !s.verifiedBinary(ctx, st.Version) {
		return errors.New("the database server MetaRush downloaded is missing or was changed; set it up again")
	}
	if err := s.ensureKeyFile(); err != nil {
		return err
	}
	if err := os.MkdirAll(s.DataDir(), 0o700); err != nil {
		return err
	}
	raiseFileLimit()
	rotate(s.LogFile(), 20<<20)
	out, err := os.OpenFile(s.outFile(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	cache := s.CacheGB
	if cache <= 0 {
		cache = 1
	}
	args := []string{"--dbpath", s.DataDir(), "--port", strconv.Itoa(s.Port), "--bind_ip", "127.0.0.1", "--nounixsocket",
		"--replSet", ReplSet, "--auth", "--keyFile", s.keyFile(), "--logpath", s.LogFile(), "--logappend", "--pidfilepath", s.pidFile(),
		"--wiredTigerCacheSizeGB", strconv.FormatFloat(cache, 'f', -1, 64), "--setParameter", "diagnosticDataCollectionEnabled=false"}
	_ = os.Remove(s.pidFile())
	// An environment built from nothing: the server inherits none of
	// MetaRush's variables (secrets among them).
	proc, err := s.Tools.Start(tools.Spec{Tool: toolName, Args: args, Stdout: out, Dir: s.Dir(), CleanEnv: true, Env: serverEnv()})
	out.Close()
	if err != nil {
		return plainErr("the database couldn't start", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- proc.Wait() }()
	return s.waitReady(ctx, proc.PID, exited, 3*time.Minute)
}

// waitReady waits until the server answers, it exits, or time runs out.
func (s *Server) waitReady(ctx context.Context, pid int, exited <-chan error, d time.Duration) error {
	deadline := time.Now().Add(d)
	for {
		if s.Answers(ctx) {
			return nil
		}
		select {
		case <-exited:
			return s.whyStopped()
		default:
		}
		if exited == nil && !alive(pid) {
			return s.whyStopped()
		}
		if time.Now().After(deadline) {
			return errors.New("the database on this computer is taking too long to start; its log says why")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// whyStopped reads the end of the server's output and log for a reason a
// person can act on.
func (s *Server) whyStopped() error {
	var text string
	for _, f := range []string{s.outFile(), s.LogFile()} {
		if b, err := os.ReadFile(f); err == nil {
			if len(b) > 64<<10 {
				b = b[len(b)-64<<10:]
			}
			text += string(b)
		}
	}
	switch {
	case strings.Contains(text, "error while loading shared libraries"):
		lib := ""
		if i := strings.Index(text, "error while loading shared libraries: "); i >= 0 {
			lib = strings.TrimSpace(strings.SplitN(text[i+len("error while loading shared libraries: "):], ":", 2)[0])
		}
		return fmt.Errorf("the database can't start: this computer lacks a library it needs (%s); install it with the system's package manager", lib)
	case strings.Contains(text, "Address already in use"):
		return fmt.Errorf("the database can't start: another program on this computer uses its port (%d)", s.Port)
	case strings.Contains(text, "Unable to lock the lock file") || strings.Contains(text, "DBPathInUse"):
		return errors.New("the database can't start: another database server is using its folder")
	case strings.Contains(text, "No space left on device"):
		return errors.New("the database can't start: the disk is full")
	case strings.Contains(text, `"id":12257600`) || strings.Contains(text, "Linux kernel versions 6.19 and newer"):
		// MongoDB's own kernel check stopped it (one MetaRush's didn't foresee).
		v := ""
		if st, ok := s.ReadState(); ok {
			v = st.Version
		}
		k := s.kernel()
		if k.Release == "" {
			k.Release = "unknown"
		}
		return cantStart(v, k)
	}
	return errors.New("the database on this computer stopped while starting; its log says why")
}

// Stop asks the server to shut down cleanly and waits for it (never a kill:
// an unclean stop means a long recovery the next time).
func (s *Server) Stop(ctx context.Context) error {
	if _, ok := s.ReadState(); !ok {
		return nil
	}
	unlock, err := s.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return s.stopLocked(ctx)
}

func (s *Server) stopLocked(ctx context.Context) error {
	pid := s.PID()
	if pid == 0 {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return plainErr("the database couldn't be asked to stop", err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for alive(pid) {
		if time.Now().After(deadline) {
			return errors.New("the database on this computer is still stopping")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	_ = os.Remove(s.pidFile()) // stopped, as confirmed: the number may soon be another process's
	return nil
}

func (s *Server) ensureKeyFile() error {
	if st, err := os.Stat(s.keyFile()); err == nil && st.Size() > 0 {
		return nil
	}
	if err := s.checkPlace(); err != nil {
		return err
	}
	b := make([]byte, 96)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir(), 0o700); err != nil {
		return err
	}
	tmp := s.keyFile() + ".tmp"
	if err := os.WriteFile(tmp, []byte(base64.StdEncoding.EncodeToString(b)+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.keyFile())
}

// lock holds <home>/database/start.lock: one process starts, stops or
// upgrades the server at a time.
func (s *Server) lock(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(s.Dir(), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.lockPath(), os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// raiseFileLimit lets the server keep the files it needs open: programs
// MetaRush starts inherit its limit once it has set one itself.
func raiseFileLimit() {
	var r syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &r) != nil {
		return
	}
	for _, want := range []uint64{64000, 10240} {
		if want > r.Max {
			want = r.Max
		}
		if want <= r.Cur {
			return
		}
		n := syscall.Rlimit{Cur: want, Max: r.Max}
		if syscall.Setrlimit(syscall.RLIMIT_NOFILE, &n) == nil {
			return
		}
	}
}

// rotate keeps one older log when the log grew past max.
func rotate(p string, max int64) {
	if st, err := os.Stat(p); err == nil && st.Size() > max {
		_ = os.Rename(p, p+".1")
	}
}

/* ---------- what it is like ---------- */

// Status is the database on this computer as MetaRush sees it.
type Status struct {
	// Available: MongoDB makes its server for this computer.
	Available bool   `json:"available"`
	SetUp     bool   `json:"setUp"`
	Version   string `json:"version,omitempty"`
	// Pinned is the version this MetaRush sets up; UpgradeReady says a
	// person can move to it (`metarush database upgrade`).
	Pinned       string `json:"pinned"`
	UpgradeReady bool   `json:"upgradeReady"`
	// UpgradeUnfinished: an upgrade stopped before the data moved on to the
	// new release series; upgrading again finishes it.
	UpgradeUnfinished bool `json:"upgradeUnfinished,omitempty"`
	// CantStart says why the version set up here can't start on this
	// computer's Linux kernel, and what to do (D339).
	CantStart string `json:"cantStart,omitempty"`
	// UpgradeWaits says why the version this MetaRush comes with can't be
	// moved to yet: this computer's Linux kernel keeps it from running.
	UpgradeWaits string `json:"upgradeWaits,omitempty"`
	// Kernel is this computer's Linux kernel when it is in the way.
	Kernel    string `json:"kernel,omitempty"`
	Running   bool   `json:"running"`
	PID       int    `json:"pid,omitempty"`
	Port      int    `json:"port,omitempty"`
	DataBytes int64  `json:"dataBytes"`
	Folder    string `json:"folder"`
	Log       string `json:"log"`
}

// Status reads the database's state without changing anything.
func (s *Server) Status(ctx context.Context) Status {
	st, ok := s.ReadState()
	out := Status{Available: s.Available(), Pinned: s.pin().Version, Folder: s.Dir(), Log: s.LogFile()}
	if !ok {
		return out
	}
	if s.Port == 0 {
		s.Port = st.Port
	}
	out.SetUp, out.Version, out.Port = st.Initiated, st.Version, s.Port
	out.UpgradeUnfinished = st.FeaturePending != ""
	k := s.kernel()
	out.UpgradeReady = out.Available && Compare(out.Pinned, st.Version) > 0 && !KernelRefuses(out.Pinned, k) || out.UpgradeUnfinished
	if KernelRefuses(st.Version, k) {
		out.CantStart, out.Kernel = cantStart(st.Version, k).Words(), k.Release
	} else if cur := s.current().Version; KernelRefuses(cur, k) && Compare(cur, st.Version) > 0 {
		out.UpgradeWaits, out.Kernel = upgradeWaits(st.Version, cur, k).Words(), k.Release
	}
	out.PID = s.PID()
	out.Running = out.PID > 0 && s.Answers(ctx)
	out.DataBytes = dirSize(s.DataDir())
	return out
}

// dirSize is what the data takes on disk, without the server's journal
// (files it sets aside ahead of time, about 200 MB even when empty).
func dirSize(dir string) int64 {
	var n int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		switch {
		case err != nil:
		case info.IsDir() && info.Name() == "journal":
			return filepath.SkipDir
		case !info.IsDir() && !strings.HasPrefix(info.Name(), "WiredTigerLog.") && !strings.HasPrefix(info.Name(), "WiredTigerPreplog."):
			n += info.Size()
		}
		return nil
	})
	return n
}

// Compare compares two versions (1.2.3) numerically.
func Compare(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// serverEnv is all the environment the database server gets.
func serverEnv() []string { return []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C"} }
