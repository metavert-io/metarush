package localdb

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// keepInto records what Prepare asks to keep, by user.
func keepInto(own, admin *string) func(user, uri string) error {
	return func(user, uri string) error {
		if user == AdminUser {
			*admin = uri
		} else {
			*own = uri
		}
		return nil
	}
}

// realServer is a home with a real server installed (not started).
func realServer(t *testing.T) *Server {
	t.Helper()
	bin := realMongod(t)
	s := testServer(t, t.TempDir(), Pin{})
	s.Port = freePort(t)
	installReal(t, s, bin)
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	return s
}

func longCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func TestOursOnlyThisServersSignIns(t *testing.T) {
	s := New(t.TempDir(), 27151, nil)
	for uri, want := range map[string]bool{
		s.URI("pw"):      true,
		s.AdminURI("pw"): false, // another user
		"mongodb://metarush:pw@127.0.0.1:27152/?authSource=admin&replicaSet=metarush&directConnection=true": false, // secret-scan: allow (a made-up test address); another port
		"mongodb://metarush:pw@127.0.0.1:27151/?authSource=admin&replicaSet=rs0":                            false, // secret-scan: allow (a made-up test address); another replica set
		"mongodb+srv://metarush:pw@cluster0.example.mongodb.net/?retryWrites=true":                          false, // Atlas
		"mongodb://metarush@127.0.0.1:27151/?replicaSet=metarush":                                           false, // no password
		"": false,
	} {
		if s.Ours(uri, User) != want {
			t.Errorf("Ours(%q) = %v", uri, !want)
		}
	}
	if !s.Ours(s.AdminURI("pw"), AdminUser) {
		t.Error("the administrator's own address")
	}
}

// An address saved for another server (MongoDB Atlas kept by an earlier
// choice; here a second real server) is never taken for this one: the new
// server gets its own replica set and sign-ins, and its exception is closed.
func TestPrepareIgnoresAnotherServersAddress(t *testing.T) {
	if testing.Short() {
		t.Skip("starts real database servers")
	}
	a, b := realServer(t), realServer(t)
	ctx := longCtx(t)
	var aOwn, aAdmin string
	if _, err := a.Prepare(ctx, "metarush-test-a", Saved{}, nil, keepInto(&aOwn, &aAdmin)); err != nil {
		t.Fatal(err)
	}
	var bOwn, bAdmin string
	fresh, err := b.Prepare(ctx, "metarush-test-b", Saved{URI: aOwn, AdminURI: aAdmin}, nil, keepInto(&bOwn, &bAdmin))
	if err != nil || !fresh {
		t.Fatalf("b: %v %v", fresh, err)
	}
	if bOwn == "" || bAdmin == "" || bOwn == aOwn || !b.SignsIn(ctx, bOwn) || !b.RequiresSignIn(ctx) || b.exceptionOpen(ctx) {
		t.Errorf("b's sign-ins: own %v admin %v; requires a sign-in %v", bOwn != "", bAdmin != "", b.RequiresSignIn(ctx))
	}
	if h, _ := b.hello(ctx); h["setName"] != ReplSet {
		t.Errorf("b's replica set: %v", h["setName"])
	}
}

// A setup stopped after the administrator exists but before MetaRush's own
// sign-in: the next run uses the saved administrator, makes MetaRush's sign-in
// with its saved password, and replaces nothing that was saved.
func TestPrepareResumesAfterTheAdministrator(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real database server")
	}
	s := realServer(t)
	ctx := longCtx(t)
	const db = "metarush-test-resume"
	var own, admin string
	if _, err := s.Prepare(ctx, db, Saved{}, nil, keepInto(&own, &admin)); err != nil {
		t.Fatal(err)
	}
	ac, err := mongo.Connect(options.Client().ApplyURI(admin))
	if err != nil {
		t.Fatal(err)
	}
	if err := ac.Database("admin").RunCommand(ctx, bson.D{{Key: "dropUser", Value: User}}).Err(); err != nil {
		t.Fatal(err)
	}
	_ = ac.Disconnect(ctx)
	st, _ := s.ReadState()
	st.Initiated = false
	if err := s.writeState(st); err != nil {
		t.Fatal(err)
	}
	fresh, err := s.Prepare(ctx, db, Saved{URI: own, AdminURI: admin}, nil, func(user, _ string) error {
		t.Errorf("a new address for %s was kept", user)
		return nil
	})
	if err != nil || !fresh {
		t.Fatalf("resume: %v %v", fresh, err)
	}
	if !s.SignsIn(ctx, admin) || !s.SignsIn(ctx, own) {
		t.Error("the saved addresses don't sign in after resuming")
	}
	// Stopped before the administrator was made (its address kept, the
	// replica set made): the kept password is used, nothing new is kept.
	s2 := realServer(t)
	if err := s2.writeState(State{Version: s2.pin().Version, Port: s2.Port}); err != nil {
		t.Fatal(err)
	}
	if err := s2.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s2.ensureReplicaSet(ctx); err != nil {
		t.Fatal(err)
	}
	keptAdmin := s2.AdminURI(NewPassword())
	var own2 string
	if _, err := s2.Prepare(ctx, db, Saved{AdminURI: keptAdmin}, nil, func(user, uri string) error {
		if user == AdminUser {
			t.Error("the kept administrator address was replaced")
		}
		own2 = uri
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !s2.SignsIn(ctx, keptAdmin) || !s2.SignsIn(ctx, own2) {
		t.Error("the kept administrator password wasn't the one made")
	}
}

// Someone else used the first-sign-in exception first: Prepare says so and
// keeps no address for sign-ins it can't make.
func TestPrepareRefusesSignInsItDoesntKnow(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real database server")
	}
	s := realServer(t)
	ctx := longCtx(t)
	if err := s.writeState(State{Version: s.pin().Version, Port: s.Port}); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.ensureReplicaSet(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := s.connect(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	roles := bson.A{bson.D{{Key: "role", Value: "root"}, {Key: "db", Value: "admin"}}}
	if err := c.Database("admin").RunCommand(ctx, bson.D{{Key: "createUser", Value: "someone"}, {Key: "pwd", Value: "x"}, {Key: "roles", Value: roles}}).Err(); err != nil {
		t.Fatal(err)
	}
	_ = c.Disconnect(ctx)
	_, err = s.Prepare(ctx, "metarush-test-x", Saved{}, nil, func(user, _ string) error {
		t.Errorf("an address for %s was kept although its sign-in can't be made", user)
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "sign-ins MetaRush doesn't know") {
		t.Errorf("unknown sign-ins: %v", err)
	}
	if st, _ := s.ReadState(); st.Initiated {
		t.Error("the home counts as set up")
	}
	// Plain words; MongoDB's own text only as the cause.
	if strings.Contains(err.Error(), "replica set") || strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("not plain: %v", err)
	}
}

// The server starts with an environment built from nothing: none of
// MetaRush's variables, secrets among them, reach it.
func TestServerGetsNoSecrets(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real database server")
	}
	s := realServer(t)
	ctx := longCtx(t)
	t.Setenv("METARUSH_MONGODB_ADMIN_URI", "mongodb://metarush-admin:FAKE-admin@127.0.0.1:1/") // secret-scan: allow (a made-up test address)
	t.Setenv("METARUSH_MONGODB_URI", "mongodb://metarush:FAKE-own@127.0.0.1:1/")               // secret-scan: allow (a made-up test address)
	if err := s.writeState(State{Version: s.pin().Version, Port: s.Port}); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	env := processEnv(t, s.PID())
	if strings.Contains(env, "FAKE-") || strings.Contains(env, "METARUSH_") {
		t.Errorf("the server's environment carries MetaRush's variables: %q", env)
	}
}

// A stale pid file whose number now belongs to another database server
// (another home's) is never believed nor signalled; a confirmed stop removes
// the pid file.
func TestPidFileIsCheckedByProgramAndFolder(t *testing.T) {
	if testing.Short() {
		t.Skip("starts real database servers")
	}
	a, b := realServer(t), realServer(t)
	ctx := longCtx(t)
	for _, s := range []*Server{a, b} {
		if err := s.writeState(State{Version: s.pin().Version, Port: s.Port}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	pidA := a.PID()
	if pidA == 0 {
		t.Fatal("a's own server isn't recognized")
	}
	if err := os.MkdirAll(b.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.pidFile(), []byte(itoa(pidA)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b.PID() != 0 {
		t.Fatal("another home's server was taken for b's")
	}
	if err := b.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if !alive(pidA) || !a.Answers(ctx) {
		t.Fatal("b's stop signalled another home's server")
	}
	if st := b.Status(ctx); st.Running {
		t.Error("b counts as running")
	}
	// b starts its own server despite the stale file.
	if err := b.Start(ctx); err != nil {
		t.Fatalf("b: %v", err)
	}
	if b.PID() == 0 || b.PID() == pidA {
		t.Error("b's own server")
	}
	if err := a.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.pidFile()); !os.IsNotExist(err) {
		t.Error("a confirmed stop left the pid file")
	}
}

// A home in a folder that syncs to the cloud is refused before anything is
// downloaded, kept or written: the key file alone opens every project.
func TestPrepareRefusesASyncedHome(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	home := filepath.Join(user, "Dropbox", "metarush")
	s := testServer(t, home, Pin{Version: "9.9.9", Archives: map[string]Archive{"test/target": {URL: "http://127.0.0.1:1/never.tgz", SHA256: strings.Repeat("a", 64)}}})
	s.Port = freePort(t)
	_, err := s.Prepare(context.Background(), "metarush-test-sync", Saved{}, nil, func(user, _ string) error {
		t.Errorf("an address for %s was kept", user)
		return nil
	})
	var le *Error
	if !errors.As(err, &le) || le.Plain != ErrUnsafePlace.Error() {
		t.Fatalf("a synced home: %v", err)
	}
	if _, err := os.Stat(s.Dir()); !os.IsNotExist(err) {
		t.Error("something was written into the synced folder")
	}
	if err := s.ensureKeyFile(); err == nil {
		t.Error("a key file was written into the synced folder")
	}
}

// An upgrade that stopped before the data moved on to the new release series
// is offered again, finished by the next upgrade, and then up to date.
func TestUnfinishedUpgradeIsFinished(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real database server")
	}
	s := realServer(t)
	ctx := longCtx(t)
	var own, admin string
	if _, err := s.Prepare(ctx, "metarush-test-fcv", Saved{}, nil, keepInto(&own, &admin)); err != nil {
		t.Fatal(err)
	}
	st, _ := s.ReadState()
	if st.FeatureVersion != Series(st.Version) {
		t.Errorf("feature version at setup: %q", st.FeatureVersion)
	}
	st.FeaturePending = Series(st.Version) // as if setting it had failed after the new server started
	if err := s.writeState(st); err != nil {
		t.Fatal(err)
	}
	if status := s.Status(ctx); !status.UpgradeUnfinished || !status.UpgradeReady {
		t.Errorf("status doesn't offer to finish: %+v", status)
	}
	step, err := s.PlanUpgrade()
	if err != nil || !step.Finish {
		t.Fatalf("plan: %+v %v", step, err)
	}
	if _, err := s.Upgrade(ctx, admin, nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	st, _ = s.ReadState()
	if st.FeaturePending != "" || st.FeatureVersion != Series(st.Version) {
		t.Errorf("state after finishing: %+v", st)
	}
	if _, err := s.PlanUpgrade(); err != ErrUpToDate {
		t.Errorf("after finishing: %v", err)
	}
	if status := s.Status(ctx); status.UpgradeReady || status.UpgradeUnfinished {
		t.Errorf("status after finishing: %+v", status)
	}
}

// A whole upgrade across a release series with a real server, every step
// under one lock (it once waited on its own lock until the context ended).
// The older version is a stand-in: the same server, saying it is 7.0.99,
// its data set back to feature version 7.0.
func TestUpgradeAcrossASeries(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real database server")
	}
	bin := realMongod(t)
	s := testServer(t, t.TempDir(), Pin{})
	s.Port = freePort(t)
	newV := installReal(t, s, bin)
	if Series(newV) != "8.0" {
		t.Skipf("the stand-in older series assumes an 8.0 server, not %s", newV)
	}
	const old = "7.0.99"
	link := filepath.Join(s.Dir(), "real-mongod")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	dir := s.versionDir(old)
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	wrapper := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo \"db version v" + old + "\"; exit 0; fi\nexec \"" + link + "\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "bin", "mongod"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	sum, _ := fileSHA(filepath.Join(dir, "bin", "mongod"))
	if err := os.WriteFile(filepath.Join(dir, "mongod.sha256"), []byte(sum+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.writeState(State{Version: old, FeatureVersion: "7.0", Port: s.Port}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Stop(context.Background()) })
	ctx := longCtx(t)
	var own, admin string
	if _, err := s.Prepare(ctx, "metarush-test-series", Saved{}, nil, keepInto(&own, &admin)); err != nil {
		t.Fatalf("set up on the older version: %v", err)
	}
	if err := s.setFeatureVersion(ctx, admin, "7.0"); err != nil {
		t.Fatal(err)
	}
	step, err := s.PlanUpgrade()
	if err != nil || !step.SeriesChange || step.From != old || step.To != newV {
		t.Fatalf("plan: %+v %v", step, err)
	}
	if _, err := s.Upgrade(ctx, admin, nil); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	st, _ := s.ReadState()
	if st.Version != newV || st.FeatureVersion != "8.0" || st.FeaturePending != "" || st.UpgradedFrom != old {
		t.Errorf("state after the upgrade: %+v", st)
	}
	if fcv, err := s.featureVersion(ctx, admin); err != nil || fcv != "8.0" {
		t.Errorf("feature version: %s %v", fcv, err)
	}
	if !s.SignsIn(ctx, own) || s.PID() == 0 {
		t.Error("MetaRush's sign-in or the server after the upgrade")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("the older version was kept")
	}
	if _, err := s.PlanUpgrade(); err != ErrUpToDate {
		t.Errorf("after the upgrade: %v", err)
	}
}

// A setup that stops before the administrator exists never leaves the server
// running with MongoDB's first-sign-in exception open; run again, it carries on.
func TestPrepareNeverLeavesTheFirstSignInOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real database server")
	}
	s := realServer(t)
	ctx := longCtx(t)
	_, err := s.Prepare(ctx, "metarush-test-gap", Saved{}, nil, func(user, _ string) error {
		if user == AdminUser {
			return errors.New("the keychain said no")
		}
		return nil
	})
	if err == nil {
		t.Fatal("Prepare went on without keeping the administrator's address")
	}
	if s.PID() != 0 || s.Answers(ctx) {
		t.Fatal("the server was left running before its administrator existed")
	}
	var own, admin string
	if _, err := s.Prepare(ctx, "metarush-test-gap", Saved{}, nil, keepInto(&own, &admin)); err != nil {
		t.Fatalf("again: %v", err)
	}
	if !s.SignsIn(ctx, admin) || !s.SignsIn(ctx, own) || !s.RequiresSignIn(ctx) {
		t.Error("run again, it didn't finish")
	}
}

// A home inside a git repository gets no database, even where git ignores
// it: cleaning the repository's ignored files would delete every project.
func TestPrepareRefusesAHomeInARepository(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	if out, err := exec.Command(git, "-c", "init.defaultBranch=main", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("home/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(repo, "home")
	s := testServer(t, home, Pin{Version: "9.9.9", Archives: map[string]Archive{"test/target": {URL: "http://127.0.0.1:1/never.tgz", SHA256: strings.Repeat("a", 64)}}})
	s.Port = freePort(t)
	_, err = s.Prepare(context.Background(), "metarush-test-repo", Saved{}, nil, func(user, _ string) error {
		t.Errorf("an address for %s was kept", user)
		return nil
	})
	var le *Error
	if !errors.As(err, &le) || le.Plain != ErrUnsafePlace.Error() {
		t.Fatalf("a home in a repository: %v", err)
	}
	if _, err := os.Stat(s.Dir()); !os.IsNotExist(err) {
		t.Error("something was written into the repository")
	}
}

// A dotfiles repository at the home folder holds the suggested home too: the
// database is allowed there, with a warning naming the risk; a repository
// anywhere else is still refused.
func TestDotfilesRepositoryAtHomeIsAllowedWithAWarning(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	user := t.TempDir()
	t.Setenv("HOME", user)
	if out, err := exec.Command(git, "-c", "init.defaultBranch=main", "init", "-q", user).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	s := New(filepath.Join(user, ".metarush"), 27150, nil)
	if err := s.checkPlace(); err != nil {
		t.Fatalf("the suggested home under a dotfiles repository: %v", err)
	}
	if w := s.PlaceWarning(); !strings.Contains(w, "dotfiles repository") || !strings.Contains(w, "delete") {
		t.Errorf("warning: %q", w)
	}
	// A repository below the home folder: refused.
	repo := filepath.Join(user, "code", "project")
	if out, err := exec.Command(git, "-c", "init.defaultBranch=main", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	inner := New(filepath.Join(repo, "home"), 27151, nil)
	var le *Error
	if err := inner.checkPlace(); !errors.As(err, &le) || le.Plain != ErrUnsafePlace.Error() {
		t.Errorf("a home in a project's repository: %v", err)
	}
	if inner.PlaceWarning() != "" {
		t.Error("a refused place got a warning instead")
	}
	// No repository at all: nothing to say.
	plain := New(filepath.Join(t.TempDir(), "home"), 27152, nil)
	if plain.checkPlace() != nil || plain.PlaceWarning() != "" {
		t.Error("a plain folder")
	}
}
