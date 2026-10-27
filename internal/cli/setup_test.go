package cli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/firstrun"
	"github.com/metavert-io/metarush/internal/localdb"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/secretpath"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/tools"
)

// setupEnv gives a test its own home folder (Claude Code's skills land there,
// never in the person's own) and no inherited database address.
func setupEnv(t *testing.T) string {
	t.Helper()
	user := t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("METARUSH_MONGODB_URI", "")
	t.Setenv(localdb.EnvPin, "")
	return user
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func decodeData(t *testing.T, r cliRun, v any) {
	t.Helper()
	var env struct {
		OK   bool            `json:"ok"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &env); err != nil || !env.OK {
		t.Fatalf("%v: not ok (%d): %s %s", r.args, r.code, r.stdout, r.stderr)
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		t.Fatal(err)
	}
}

// Unattended, with a database elsewhere (the test database standing in for
// Atlas): every answer a flag, the result as JSON, the skills in Claude
// Code's folder, what works now; run again it changes nothing, and it never
// moves where things are kept.
func TestSetupUnattendedWithADatabaseElsewhere(t *testing.T) {
	user := setupEnv(t)
	db := testutil.DB(t)
	h := filepath.Join(t.TempDir(), "home")
	port := freeTCPPort(t)
	args := []string{"setup", "--yes", "--json", "--database", "elsewhere", "--db-uri", testutil.URI(), "--db-name", db.Name, "--test-db",
		"--secrets", "env", "--service=false", "--skills", "--port", strconv.Itoa(port), "--workspace-name", "Garden club", "--user-name", "Sam", "--user-email", "sam@example.com"}
	r := runCLI(t, h, args...)
	var res SetupResult
	decodeData(t, r, &res)
	if !res.Fresh || res.Workspace.Name != "Garden club" || res.Database.Local || res.Storage.Kind != "folder" || res.Storage.Folder != filepath.Join(h, "media") {
		t.Fatalf("result: %+v", res)
	}
	if res.Dashboard != "http://127.0.0.1:"+strconv.Itoa(port) {
		t.Errorf("dashboard %s", res.Dashboard)
	}
	if !res.Skills.Installed || res.Skills.Dir != filepath.Join(user, ".claude", "skills") {
		t.Errorf("skills: %+v", res.Skills)
	}
	// The plugin needs this program too, and setup says so (D319).
	if !strings.Contains(res.Skills.Plugin, "plugin for Claude Code") || !strings.Contains(res.Skills.Plugin, "metarush program") {
		t.Errorf("no word on the plugin: %q", res.Skills.Plugin)
	}
	if _, err := os.Stat(filepath.Join(user, ".claude", "skills", "video-render", "SKILL.md")); err != nil {
		t.Errorf("skills not in Claude Code's folder: %v", err)
	}
	if res.Service.Wanted || res.Service.Installed {
		t.Errorf("service: %+v", res.Service)
	}
	if len(res.WhatWorks.NoKeys) < 4 || res.WhatWorks.Claude.Says == "" || res.WhatWorks.Summary == "" {
		t.Errorf("what works: %+v", res.WhatWorks)
	}
	keys := map[string]bool{}
	for _, w := range res.WhatWorks.NoKeys {
		keys[w.Key] = true
		if w.Says == "" || w.Name == "" {
			t.Errorf("no words for %s", w.Key)
		}
	}
	for _, k := range []string{"transcripts", "editing", "renders", "checks"} {
		if !keys[k] {
			t.Errorf("what works with no keys lacks %s", k)
		}
	}
	if len(res.Next) == 0 || res.Next[len(res.Next)-1] != "metarush dashboard" {
		t.Errorf("next: %v", res.Next)
	}
	// The home works by itself: its .env holds the address config.yaml names.
	t.Setenv("METARUSH_MONGODB_URI", "")
	os.Unsetenv("METARUSH_MONGODB_URI")
	cfg, err := config.Load(h)
	if err != nil {
		t.Fatalf("the home doesn't load by itself: %v", err)
	}
	if cfg.Database.Local != nil || cfg.Database.Name != db.Name {
		t.Errorf("database settings: %+v", cfg.Database)
	}

	// Run again: nothing new is made.
	r = runCLI(t, h, "setup", "--yes", "--json", "--service=false")
	var again SetupResult
	decodeData(t, r, &again)
	if again.Fresh || again.Workspace.ID != res.Workspace.ID || again.Installation.ID != res.Installation.ID {
		t.Fatalf("run again: %+v", again)
	}
	// It never moves where things are kept.
	r = runCLI(t, h, "setup", "--yes", "--storage", "s3")
	if r.code == 0 || !strings.Contains(r.stderr, "set up on this computer already") {
		t.Errorf("a second setup changed storage: %d %s", r.code, r.stderr)
	}

	// The first run's words (op setup.status), also from the command line.
	r = runCLI(t, h, "setup", "status", "--json")
	var st struct {
		FirstRun bool `json:"firstRun"`
		NoKeys   []struct {
			Key string `json:"key"`
		} `json:"noKeys"`
		Database struct {
			Local bool   `json:"local"`
			Says  string `json:"says"`
		} `json:"database"`
	}
	decodeData(t, r, &st)
	if !st.FirstRun || len(st.NoKeys) == 0 || st.Database.Local || st.Database.Says == "" {
		t.Errorf("setup status: %+v", st)
	}
	// Plain output speaks plainly.
	r = runCLI(t, h, "setup", "--yes", "--service=false")
	if r.code != 0 {
		t.Fatalf("plain run again: %d %s", r.code, r.stderr)
	}
	setupPlain(t, "setup", r.stdout)
	r = runCLI(t, h, "setup", "status")
	setupPlain(t, "setup status", r.stdout)

	// A fresh setup's plain output speaks plainly too (a second workspace in
	// the same database). Without --skills, and with no Claude Code here, the
	// skills are left out, and setup says why.
	h2 := filepath.Join(t.TempDir(), "home")
	if err := os.RemoveAll(filepath.Join(user, ".claude")); err != nil {
		t.Fatal(err)
	}
	r = runCLI(t, h2, "setup", "--yes", "--database", "elsewhere", "--db-uri", testutil.URI(), "--db-name", db.Name, "--test-db", "--new-workspace",
		"--secrets", "env", "--service=false", "--port", strconv.Itoa(freeTCPPort(t)), "--workspace-name", "Second studio")
	if r.code != 0 || !strings.Contains(r.stdout, "Your workspace “Second studio” is ready.") || !strings.Contains(r.stdout, "What works now") {
		t.Fatalf("fresh plain setup: %d %s %s", r.code, r.stdout, r.stderr)
	}
	setupPlain(t, "fresh setup", r.stdout)
	if !strings.Contains(r.stdout, "Claude Code isn't installed, so its video skills weren't added") {
		t.Errorf("no word on the skills: %s", r.stdout)
	}
	if _, err := os.Stat(filepath.Join(user, ".claude")); !os.IsNotExist(err) {
		t.Error("skills were written for a Claude Code that isn't installed")
	}
}

// Setup asks nothing it can't: without a database address or a database
// here, it says what to pass.
func TestSetupRefusesWhatItCantDo(t *testing.T) {
	setupEnv(t)
	h := filepath.Join(t.TempDir(), "home")
	r := runCLI(t, h, "setup", "--yes", "--database", "elsewhere", "--service=false", "--skills=false", "--secrets", "env")
	if r.code == 0 || !strings.Contains(r.stderr, "No database address") {
		t.Errorf("no address: %d %s", r.code, r.stderr)
	}
	r = runCLI(t, h, "setup", "--yes", "--database-port", strconv.Itoa(localdb.TestPort), "--service=false", "--skills=false", "--secrets", "env")
	if r.code == 0 || !strings.Contains(r.stderr, "can't be the database's") {
		t.Errorf("the test database's port: %d %s", r.code, r.stderr)
	}
	r = runCLI(t, h, "setup", "--yes", "--storage", "tape", "--service=false", "--skills=false", "--secrets", "env")
	if r.code == 0 {
		t.Error("an unknown kind of storage was accepted")
	}
	if config.Exists(h) {
		t.Error("a refused setup left settings behind")
	}
}

// installRealMongod puts a real MongoDB server where setup's download would
// (a link: the original is never changed) and pins its version, so setup
// downloads nothing.
func installRealMongod(t *testing.T, h, realHome string) string {
	t.Helper()
	bin := os.Getenv("METARUSH_TEST_MONGOD")
	if bin == "" && realHome != "" {
		bin = filepath.Join(realHome, ".local", "mongodb", "bin", "mongod")
	}
	if _, err := os.Stat(bin); err != nil {
		if p, err := exec.LookPath("mongod"); err == nil {
			bin = p
		} else {
			t.Skip("no mongod on this computer (set METARUSH_TEST_MONGOD)")
		}
	}
	out, err := exec.Command(bin, "--version").Output()
	m := regexp.MustCompile(`db version v(\d+\.\d+\.\d+)`).FindSubmatch(out)
	if err != nil || m == nil {
		t.Skipf("mongod doesn't run here: %v", err)
	}
	v := string(m[1])
	dir := filepath.Join(h, "database", "mongodb", v)
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bin, filepath.Join(dir, "bin", "mongod")); err != nil {
		t.Fatal(err)
	}
	sum, err := sha256Of(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mongod.sha256"), []byte(sum+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pin := filepath.Join(t.TempDir(), "pin.json")
	body := `{"version":"` + v + `","url":"http://127.0.0.1:1/never-fetched.tgz","sha256":"` + strings.Repeat("a", 64) + `"}`
	if err := os.WriteFile(pin, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(localdb.EnvPin, pin)
	return v
}

// The suggested setup: a database on this computer (a real server standing
// in for the download), its address with MetaRush's own sign-in in the
// home's .env, started by whatever needs it, stopped with MetaRush.
func TestSetupWithTheDatabaseOnThisComputer(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real database server")
	}
	realHome, _ := os.UserHomeDir()
	setupEnv(t)
	h := filepath.Join(t.TempDir(), "home")
	v := installRealMongod(t, h, realHome)
	dbPort := freeTCPPort(t)
	srv := localdb.New(h, dbPort, tools.New(config.Tools{}))
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	r := runCLIContext(t, ctx, h, "setup", "--yes", "--json", "--db-name", "metarush-test-setup", "--test-db", "--secrets", "env",
		"--service=false", "--skills=false", "--database-port", strconv.Itoa(dbPort), "--port", strconv.Itoa(freeTCPPort(t)))
	var res SetupResult
	decodeData(t, r, &res)
	if !res.Fresh || !res.Database.Local || !res.Database.Running || res.Database.Port != dbPort || res.Database.Version != v {
		t.Fatalf("database: %+v", res.Database)
	}
	if res.Skills.Wanted || res.Skills.Installed {
		t.Errorf("skills: %+v", res.Skills)
	}
	os.Unsetenv("METARUSH_MONGODB_URI")
	cfg, err := config.Load(h)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Local == nil || cfg.Database.Local.Port != dbPort || !strings.Contains(cfg.Database.URI, localdb.User+":") {
		t.Fatalf("settings: local %+v, address %s", cfg.Database.Local, config.RedactURI(cfg.Database.URI))
	}
	raw, _ := os.ReadFile(config.Path(h))
	if strings.Contains(string(raw), "mongodb://") || strings.Contains(string(raw), localdb.AdminUser) {
		t.Error("config.yaml holds a database address")
	}
	// MetaRush's address is in the home's secrets file; the administrator's in a
	// file of its own, never an environment variable; both private.
	env, _ := os.ReadFile(config.DotEnvPath(h))
	if !strings.Contains(string(env), "METARUSH_MONGODB_URI=") || strings.Contains(string(env), localdb.AdminUser) {
		t.Error("the home's .env should hold MetaRush's address and not the administrator's")
	}
	admin, err := readAdminFile(h)
	if err != nil || !srv.Ours(admin, localdb.AdminUser) {
		t.Errorf("the administrator's address: %v", err)
	}
	for _, f := range []string{config.DotEnvPath(h), adminSecretFile(h)} {
		if st, err := os.Stat(f); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", filepath.Base(f), st, err)
		}
	}
	for _, kv := range os.Environ() {
		if strings.Contains(kv, localdb.AdminUser+":") {
			t.Errorf("the administrator's address is in the environment: %s", strings.SplitN(kv, "=", 2)[0])
		}
	}
	if !srv.RequiresSignIn(ctx) {
		t.Error("the database answers without a sign-in")
	}
	// Commands start it when it stopped.
	if err := srv.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	r = runCLIContext(t, ctx, h, "setup", "status", "--json")
	var st struct {
		Database struct {
			Local, Running bool
		} `json:"database"`
	}
	decodeData(t, r, &st)
	if !st.Database.Local || !st.Database.Running {
		t.Errorf("a command didn't start the database: %+v", st)
	}
	r = runCLIContext(t, ctx, h, "database", "status", "--json")
	var ds DatabaseStatus
	decodeData(t, r, &ds)
	if !ds.Local || ds.Status == nil || !ds.Status.Running || ds.Status.UpgradeReady {
		t.Errorf("database status: %+v", ds)
	}
	r = runCLIContext(t, ctx, h, "database", "status")
	setupPlain(t, "database status", r.stdout)
	// Run again: the same workspace, the same sign-in.
	r = runCLIContext(t, ctx, h, "setup", "--yes", "--json", "--service=false", "--skills=false")
	var again SetupResult
	decodeData(t, r, &again)
	if again.Fresh || again.Workspace.ID != res.Workspace.ID || !again.Database.Running {
		t.Fatalf("again: %+v", again)
	}
	r = runCLIContext(t, ctx, h, "doctor", "--json")
	if !strings.Contains(r.stdout, `"on this computer"`) || !strings.Contains(r.stdout, `"sign-in required"`) {
		t.Errorf("doctor has no check of the database on this computer and its sign-in: %s", r.stdout)
	}
	var doc struct {
		Checks []Check `json:"checks"`
	}
	decodeData(t, r, &doc)
	for _, ch := range doc.Checks {
		if ch.Area == "database" && ch.Status == "fail" {
			t.Errorf("doctor: %+v", ch)
		}
	}
	// `metarush stop` stops it too.
	r = runCLIContext(t, ctx, h, "stop", "--json")
	if r.code != 0 || !strings.Contains(r.stdout, `"databaseStopped": true`) {
		t.Errorf("stop: %d %s %s", r.code, r.stdout, r.stderr)
	}
	if srv.PID() != 0 {
		t.Error("the database still runs after metarush stop")
	}
	r = runCLIContext(t, ctx, h, "database", "upgrade", "--json")
	if !strings.Contains(r.stdout, `"upToDate": true`) {
		t.Errorf("upgrade: %s %s", r.stdout, r.stderr)
	}
}

// The words setup and the first run use stay plain (D109).
func TestFirstRunWordsArePlain(t *testing.T) {
	setupEnv(t)
	cfg := config.Default(t.TempDir())
	ts := tools.New(config.Tools{})
	var words []string
	for _, x := range buildWhatWorksForTest(cfg, ts) {
		words = append(words, x...)
	}
	for _, w := range words {
		for _, f := range (plainwords.Allow{}).Filter(plainwords.CheckShown(w)) {
			t.Errorf("%q shows %s", w, f)
		}
	}
}

func sha256Of(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// buildWhatWorksForTest is every sentence the first run shows.
func buildWhatWorksForTest(cfg *config.Config, ts *tools.Set) [][]string {
	w := firstrun.Build(context.Background(), cfg, ts)
	out := [][]string{{w.Summary, w.Claude.Says, w.Claude.Next}}
	for _, x := range append(w.NoKeys, w.WithKeys...) {
		out = append(out, []string{x.Name, x.Says, x.Next})
	}
	return out
}

// The interview: the suggested answers by pressing Enter, and the other
// choices by number (people's answers are read like a terminal's lines).
func TestSetupInterview(t *testing.T) {
	setupEnv(t)
	run := func(answers string, o *setupOpts) (*setupRun, error) {
		t.Helper()
		cmd := setupCommand()
		if err := cmd.ParseFlags(nil); err != nil {
			t.Fatal(err)
		}
		*o = setupOpts{service: true, skills: true, open: true, init: initOpts{secrets: "env"}}
		old := stdin
		stdin = bufio.NewReader(strings.NewReader(answers))
		defer func() { stdin = old }()
		s := &setupRun{c: cmd, o: o, h: filepath.Join(t.TempDir(), "home"), prompt: true}
		for _, step := range []func() error{s.chooseStorage, s.chooseDatabase, s.choosePort} {
			if err := step(); err != nil {
				return s, err
			}
		}
		return s, nil
	}
	// Enter, Enter, Enter, Enter: a folder in the home, the database here, the suggested port.
	var o setupOpts
	s, err := run("\n\n\n\n", &o)
	if err != nil {
		t.Fatal(err)
	}
	if o.init.mediaProvider != "filesystem" || o.init.mediaRoot != filepath.Join(s.h, "media") || o.database != "here" || o.init.port < 4845 {
		t.Errorf("suggested answers: %+v / %+v", o, o.init)
	}
	// Another folder, and a database elsewhere (its address from the environment).
	t.Setenv("METARUSH_MONGODB_URI", "mongodb://127.0.0.1:27018/?directConnection=true")
	other := filepath.Join(t.TempDir(), "footage")
	if _, err = run("1\n"+other+"\n2\n5999\n", &o); err != nil {
		t.Fatal(err)
	}
	if o.init.mediaRoot != other || o.database != "elsewhere" || o.init.dbURI == "" || o.init.port != 5999 {
		t.Errorf("chosen answers: %+v / %+v", o, o.init)
	}
	// Advanced storage: a bucket.
	t.Setenv("METARUSH_S3_SECRET_ACCESS_KEY", "x")
	if _, err = run("2\ns3\nr2\nfootage\nkey-id\n\n\n", &o); err != nil {
		t.Fatal(err)
	}
	if o.init.mediaProvider != "s3" || o.init.s3Preset != "r2" || o.init.s3Bucket != "footage" || o.init.s3AccessKeyID != "key-id" {
		t.Errorf("a bucket: %+v", o.init)
	}
}

// Setup for the database on this computer never takes another database's
// address: one an earlier setup kept is said and replaced; one the terminal
// sets is refused (MetaRush would use it instead).
func TestSetupHereIgnoresOtherDatabases(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a real database server")
	}
	realHome, _ := os.UserHomeDir()
	setupEnv(t)
	h := filepath.Join(t.TempDir(), "home")
	installRealMongod(t, h, realHome)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	t.Setenv("METARUSH_MONGODB_URI", "mongodb+srv://someone:pw@cluster0.example.mongodb.net/")
	r := runCLIContext(t, ctx, h, "setup", "--yes", "--database", "here", "--secrets", "env", "--service=false", "--skills=false")
	if r.code == 0 || !strings.Contains(r.stderr, "Your terminal names another database") {
		t.Fatalf("a database in the terminal: %d %s", r.code, r.stderr)
	}
	setupPlain(t, "refusal", r.stderr)
	os.Unsetenv("METARUSH_MONGODB_URI")
	// An earlier "elsewhere" try left another database's address in the home's secrets.
	const atlas = "mongodb+srv://someone:pw@cluster0.example.mongodb.net/" // secret-scan: allow (a made-up test address)
	if err := os.MkdirAll(h, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.DotEnvPath(h), []byte("METARUSH_MONGODB_URI="+atlas+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPort := freeTCPPort(t)
	srv := localdb.New(h, dbPort, tools.New(config.Tools{}))
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })
	args := []string{"setup", "--yes", "--json", "--db-name", "metarush-test-here", "--test-db", "--secrets", "env",
		"--service=false", "--skills=false", "--database-port", strconv.Itoa(dbPort), "--port", strconv.Itoa(freeTCPPort(t))}
	// Unattended, it is never replaced without being told to.
	r = runCLIContext(t, ctx, h, args...)
	if r.code == 0 || !strings.Contains(r.stdout, "for another database, so setup didn't replace it") || !strings.Contains(r.stdout, "--replace-saved-database") {
		t.Fatalf("replaced without being told to: %d %s %s", r.code, r.stdout, r.stderr)
	}
	if v, _ := config.DotEnvValue(h, "METARUSH_MONGODB_URI"); v != atlas {
		t.Fatal("the saved address changed although setup refused")
	}
	plain := append([]string{"setup", "--yes"}, args[3:]...) // the same run without --json
	r = runCLIContext(t, ctx, h, plain...)
	if r.code == 0 {
		t.Fatal("replaced without being told to (plain)")
	}
	setupPlain(t, "refusal to replace", r.stderr)
	// Told to, it replaces it and keeps the old one aside, saying where.
	r = runCLIContext(t, ctx, h, append(args, "--replace-saved-database")...)
	var res SetupResult
	decodeData(t, r, &res)
	if !res.Database.Local || !res.Database.Running || len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "kept aside in a private file") {
		t.Fatalf("result: %+v / %v", res.Database, res.Warnings)
	}
	prev := filepath.Join(h, "database", "previous.secret")
	if b, err := os.ReadFile(prev); err != nil || strings.TrimSpace(string(b)) != atlas {
		t.Errorf("the old address wasn't kept aside: %v", err)
	}
	if st, err := os.Stat(prev); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("the kept-aside address isn't private: %v %v", st, err)
	}
	v, _ := config.DotEnvValue(h, "METARUSH_MONGODB_URI")
	if !srv.Ours(v, localdb.User) || !srv.SignsIn(ctx, v) || !srv.RequiresSignIn(ctx) {
		t.Errorf("the home's address isn't this computer's database's: %s", config.RedactURI(v))
	}
}

// A home in a folder that syncs to the cloud gets no database: said plainly,
// with the way out in Details, and nothing written there.
func TestSetupRefusesADatabaseInASyncedFolder(t *testing.T) {
	user := setupEnv(t)
	h := filepath.Join(user, "Dropbox", "metarush")
	pin := filepath.Join(t.TempDir(), "pin.json")
	if err := os.WriteFile(pin, []byte(`{"version":"9.9.9","url":"http://127.0.0.1:1/never.tgz","sha256":"`+strings.Repeat("a", 64)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(localdb.EnvPin, pin)
	r := runCLI(t, h, "setup", "--yes", "--secrets", "env", "--service=false", "--skills=false", "--database-port", strconv.Itoa(freeTCPPort(t)))
	if r.code == 0 || !strings.Contains(r.stderr, "can't be kept in a folder that syncs to the cloud") {
		t.Fatalf("a synced home: %d %s", r.code, r.stderr)
	}
	setupPlain(t, "refusal", r.stderr)
	r = runCLI(t, h, "setup", "--yes", "--json", "--secrets", "env", "--service=false", "--skills=false", "--database-port", strconv.Itoa(freeTCPPort(t)))
	if !strings.Contains(r.stdout, "E_UNSAFE_SECRET_PATH") || !strings.Contains(r.stdout, "metarush setup --home") {
		t.Errorf("--json: %s", r.stdout)
	}
	if _, err := os.Stat(h); !os.IsNotExist(err) {
		t.Error("something was written into the synced folder")
	}
}

// Upgrading the database is a person's step: an agent (no terminal, or
// --json) may plan it with --dry-run, never run it, --yes or not.
func TestDatabaseUpgradeNeedsAPerson(t *testing.T) {
	setupEnv(t)
	h := filepath.Join(t.TempDir(), "home")
	cfg := config.Default(h)
	cfg.Secrets = "env"
	cfg.Database = config.Database{URI: "${METARUSH_MONGODB_URI}", Name: "metarush-test-upgrade", Local: &config.LocalDatabase{Port: 27199}}
	if err := config.Save(h, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h, "database"), 0o700); err != nil {
		t.Fatal(err)
	}
	// An upgrade whose last step is still to do: planned without a download or a server.
	state := `{"version":"8.0.34","port":27199,"initiated":true,"featureVersion":"7.0","featurePending":"8.0"}`
	if err := os.WriteFile(filepath.Join(h, "database", "state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"database", "upgrade", "--yes"}, {"database", "upgrade", "--yes", "--json"}, {"database", "upgrade"}} {
		r := runCLI(t, h, args...)
		if r.code == 0 || !strings.Contains(r.stderr+r.stdout, "Only a person at this computer") {
			t.Errorf("%v: %d %s %s", args, r.code, r.stdout, r.stderr)
		}
	}
	r := runCLI(t, h, "database", "upgrade", "--dry-run", "--json")
	var plan struct {
		DryRun bool `json:"dryRun"`
		Step   struct {
			Finish bool `json:"finish"`
		} `json:"step"`
	}
	decodeData(t, r, &plan)
	if !plan.DryRun || !plan.Step.Finish {
		t.Errorf("the plan: %+v", plan)
	}
	r = runCLI(t, h, "database", "upgrade", "--dry-run")
	setupPlain(t, "upgrade plan", r.stdout)
}

// What MongoDB or the system said stays in Details: people read plain words.
func TestDatabaseErrorsArePlain(t *testing.T) {
	raw := errors.New(`(Unauthorized) Command replSetInitiate requires authentication; keyFile /x/database/keyfile`)
	err := databaseErr(&localdb.Error{Plain: "the database on this computer has sign-ins MetaRush doesn't know", Cause: raw}, "The database on this computer couldn't be set up")
	ae := apperr.From(err)
	if strings.Contains(ae.Message, "Unauthorized") || strings.Contains(ae.Message, "replSet") || strings.Contains(ae.Message, "keyFile") {
		t.Errorf("raw text reaches people: %s", ae.Message)
	}
	if d, _ := ae.Details.(map[string]any); d["problem"] != raw.Error() {
		t.Errorf("the cause isn't in Details: %v", ae.Details)
	}
	for _, f := range plainwords.CheckShown(ae.Message + " " + ae.Hint) {
		t.Errorf("not plain: %s", f)
	}
}

// A setup of the database that failed says how to recover: run setup again.
func TestSetupDatabaseFailureSaysHowToRecover(t *testing.T) {
	cmd := setupCommand()
	_ = cmd.ParseFlags(nil)
	ae := apperr.From(setupDatabaseErr(cmd, &localdb.Error{Plain: "the database's administrator couldn't be made", Cause: errors.New("(Unauthorized) raw")}))
	if !strings.Contains(ae.Hint, "Run setup again") || ae.Command() != "metarush setup" || strings.Contains(ae.Message, "Unauthorized") {
		t.Errorf("%+v", ae)
	}
	for _, f := range plainwords.CheckShown(ae.Message + " " + ae.Hint) {
		t.Errorf("not plain: %s", f)
	}
}

// Keeping secrets inside a repository is a person's choice, never an agent's.
func TestSetupUnsafeSecretPathIsPeopleOnly(t *testing.T) {
	setupEnv(t)
	h := filepath.Join(t.TempDir(), "home")
	for _, args := range [][]string{{"setup", "--yes", "--allow-unsafe-secret-path"}, {"setup", "--json", "--allow-unsafe-secret-path"}} {
		r := runCLI(t, h, args...)
		if r.code == 0 || !strings.Contains(r.stderr+r.stdout, "Only a person at this computer can let MetaRush keep its passwords inside a repository") {
			t.Errorf("%v: %d %s %s", args, r.code, r.stdout, r.stderr)
		}
	}
	if config.Exists(h) {
		t.Error("something was set up")
	}
}

// A copy of the administrator's address that others can read is made
// private again before it is used.
func TestAdminAddressFileIsKeptPrivate(t *testing.T) {
	h := t.TempDir()
	if err := writeAdminFile(h, "mongodb://metarush-admin:pw@127.0.0.1:27150/", false); err != nil { // secret-scan: allow (a made-up test address)
		t.Fatal(err)
	}
	p := adminSecretFile(h)
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := readAdminFile(h)
	if err != nil || !strings.HasPrefix(v, "mongodb://metarush-admin:") {
		t.Fatalf("read: %v", err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Errorf("still %v", st.Mode().Perm())
	}
}

// init (and machine join, which runs it) with secrets in a file writes the
// database address into the home's .env, so the home works without the
// terminal that set it up; doctor warns when one is only in the environment.
func TestInitEnvSecretsLiveInTheHome(t *testing.T) {
	setupEnv(t)
	db := testutil.DB(t)
	h := filepath.Join(t.TempDir(), "home")
	t.Setenv("METARUSH_MONGODB_URI", testutil.URI())
	r := runCLI(t, h, "init", "--yes", "--test-db", "--secrets", "env", "--db-name", db.Name, "--workspace-name", "Env home",
		"--media", "filesystem", "--port", strconv.Itoa(freeTCPPort(t)))
	if r.code != 0 {
		t.Fatalf("init: %d %s", r.code, r.stderr)
	}
	if v, ok := config.DotEnvValue(h, "METARUSH_MONGODB_URI"); !ok || v != testutil.URI() {
		t.Fatalf("the home's .env lacks the database address: %q %v", v, ok)
	}
	if st, err := os.Stat(config.DotEnvPath(h)); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf(".env: %v %v", st, err)
	}
	r = runCLI(t, h, "doctor", "--json")
	if strings.Contains(r.stdout, `"secrets file"`) {
		t.Errorf("doctor warns although the address is in the home's file: %s", r.stdout)
	}
	// Only in the environment (an older home): doctor says so.
	if _, err := config.UnsetDotEnvVar(h, "METARUSH_MONGODB_URI"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("METARUSH_MONGODB_URI", testutil.URI())
	r = runCLI(t, h, "doctor", "--json")
	if !strings.Contains(r.stdout, `"secrets file"`) {
		t.Errorf("doctor doesn't say the address is only in the environment: %s", r.stdout)
	}
	r = runCLI(t, h, "doctor")
	if !strings.Contains(r.stdout, "only in this terminal's settings") {
		t.Errorf("plain doctor: %s", r.stdout)
	}
}

// A home in a repository is refused with the way out: a folder outside it.
func TestDatabaseInARepositorySaysWhereToGo(t *testing.T) {
	v := secretpath.Verdict{Path: "/r/mr-home/database/keyfile", Kind: secretpath.KindGit, Root: "/r", Service: "git"}
	ae := apperr.From(databaseErr(&localdb.Error{Plain: localdb.ErrUnsafePlace.Error(), Cause: &secretpath.UnsafeError{Verdict: v, What: "the database"}}, "The database on this computer couldn't be set up"))
	if ae.Code != apperr.UnsafeSecretPath || !strings.Contains(ae.Hint, "outside the repository") || !strings.Contains(ae.Command(), "--home <a folder outside the repository>") {
		t.Errorf("%+v", ae)
	}
	for _, f := range plainwords.CheckShown(ae.Message + " " + ae.Hint) {
		t.Errorf("not plain: %s", f)
	}
}

// init (setup's rule): an address the home's secrets already hold for
// another database is replaced only when told to, and kept aside first.
func TestInitNeverReplacesASavedAddressQuietly(t *testing.T) {
	setupEnv(t)
	db := testutil.DB(t)
	h := filepath.Join(t.TempDir(), "home")
	port := strconv.Itoa(freeTCPPort(t))
	first := testutil.URI()
	other := first + "&appName=metarush-other"
	if !strings.Contains(first, "?") {
		other = first + "?appName=metarush-other"
	}
	base := []string{"--yes", "--test-db", "--secrets", "env", "--db-name", db.Name, "--media", "filesystem", "--port", port}
	r := runCLI(t, h, append([]string{"init", "--workspace-name", "Saved", "--db-uri", first}, base...)...)
	if r.code != 0 {
		t.Fatalf("init: %d %s", r.code, r.stderr)
	}
	again := append([]string{"init", "--reconfigure", "--db-uri", other}, base...)
	r = runCLI(t, h, again...)
	if r.code == 0 || !strings.Contains(r.stderr, "already keeps the address of another database") {
		t.Fatalf("replaced without being told to: %d %s", r.code, r.stderr)
	}
	setupPlain(t, "init refusal", r.stderr)
	if v, _ := config.DotEnvValue(h, "METARUSH_MONGODB_URI"); v != first {
		t.Fatalf("the saved address changed: %s", config.RedactURI(v))
	}
	r = runCLI(t, h, append(again, "--replace-saved-database")...)
	if r.code != 0 {
		t.Fatalf("with the flag: %d %s", r.code, r.stderr)
	}
	if v, _ := config.DotEnvValue(h, "METARUSH_MONGODB_URI"); v != other {
		t.Errorf("not replaced: %s", config.RedactURI(v))
	}
	b, err := os.ReadFile(filepath.Join(h, "database", "previous.secret"))
	if err != nil || strings.TrimSpace(string(b)) != first {
		t.Errorf("the old address wasn't kept aside: %v", err)
	}
	if !strings.Contains(r.stdout, "kept aside") {
		t.Errorf("init didn't say where the old address went: %s", r.stdout)
	}
}
