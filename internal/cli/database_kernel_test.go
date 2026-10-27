package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/localdb"
	"github.com/metavert-io/metarush/internal/tools"
)

// A Linux kernel MongoDB 8 stops on (6.19 through 7.0.13): Ubuntu 26.04's,
// built from kernel.org's 7.0.11.
var brokenKernel = localdb.Kernel{Release: "7.0.0-27-generic", Version: "#27-Ubuntu SMP PREEMPT_DYNAMIC Thu Jul  9 10:01:12 UTC 2026",
	Signature: "Ubuntu 7.0.0-27.27-generic 7.0.11"}

// onKernel plays a Linux computer with kernel k for the rest of the test.
func onKernel(t *testing.T, k localdb.Kernel) {
	t.Helper()
	prev := localdb.ReadKernel
	localdb.ReadKernel = func() localdb.Kernel { return k }
	t.Cleanup(func() { localdb.ReadKernel = prev })
}

// fakeMongoDBArchive is a stand-in for MongoDB's download: a "server" that
// only says its version (it stops at once when started).
func fakeMongoDBArchive(t *testing.T, version string) (path, sum string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"bin/mongod": "#!/bin/sh\necho \"db version v" + version + "\"\n", "LICENSE-Community.txt": "Server Side Public License\n"} {
		if err := tw.WriteHeader(&tar.Header{Name: "mongodb-test-" + version + "/" + name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(t.TempDir(), "mongodb.tgz")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(buf.Bytes())
	return path, hex.EncodeToString(s[:])
}

// Setup on a computer whose kernel stops MongoDB 8 says so plainly before
// anything downloads, and sets up MongoDB 7.0 instead (a stand-in download:
// nothing reaches MongoDB's server).
func TestSetupSaysTheKernelBeforeDownloading(t *testing.T) {
	setupEnv(t)
	onKernel(t, brokenKernel)
	h := filepath.Join(t.TempDir(), "home")
	archive, sum := fakeMongoDBArchive(t, "7.0.45")
	t.Setenv(localdb.EnvArchive, archive)
	pin := filepath.Join(t.TempDir(), "pin.json")
	body := `{"version":"8.0.34","url":"http://127.0.0.1:1/never-fetched.tgz","sha256":"` + strings.Repeat("a", 64) +
		`","older":{"version":"7.0.45","url":"http://127.0.0.1:1/never-fetched-older.tgz","sha256":"` + sum + `"}}`
	if err := os.WriteFile(pin, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(localdb.EnvPin, pin)
	port := freeTCPPort(t)
	r := runCLI(t, h, "setup", "--yes", "--no-components", "--secrets", "env", "--service=false", "--skills=false", "--test-db",
		"--db-name", "metarush-test-kernel", "--database-port", strconv.Itoa(port), "--port", strconv.Itoa(freeTCPPort(t)))
	t.Cleanup(func() { _ = localdb.New(h, port, tools.New(config.Tools{})).Stop(context.Background()) })
	note := "MongoDB 8.0.34 can't start on this computer's Linux kernel (7.0.0-27-generic)"
	i, j := strings.Index(r.stdout, note), strings.Index(r.stdout, "Setting up the database on this computer")
	if i < 0 || j < 0 || i > j {
		t.Fatalf("the kernel wasn't said before the download (exit %d):\n%s", r.code, indentOut(r.stdout+r.stderr))
	}
	wantIn(t, "setup on a kernel MongoDB 8 stops", r.stdout, "MongoDB 7.0.45 instead", "works the same for MetaRush")
	setupPlain(t, "the kernel note", r.stdout)
	st, ok := localdb.New(h, port, tools.New(config.Tools{})).ReadState()
	if !ok || st.Version != "7.0.45" {
		t.Errorf("the database set up: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(h, "database", "mongodb", "8.0.34")); !os.IsNotExist(err) {
		t.Error("MongoDB 8 was unpacked anyway")
	}
}

// kernelHome is a home whose database on this computer was set up at
// version (no server: it never starts).
func kernelHome(t *testing.T, version string) string {
	t.Helper()
	setupEnv(t)
	h := filepath.Join(t.TempDir(), "home")
	cfg := config.Default(h)
	cfg.Secrets = "env"
	cfg.Workspace = config.Ref{ID: "wsp_01jabcdefghjkmnpqrstvwxyz0", Name: "Kernel"}
	cfg.Installation = config.Ref{ID: "ins_01jabcdefghjkmnpqrstvwxyz0", Name: "linux-box"}
	cfg.Database = config.Database{URI: "${METARUSH_MONGODB_URI}", Name: "metarush-test-kernel", Local: &config.LocalDatabase{Port: 27198}}
	if err := config.Save(h, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h, "database"), 0o700); err != nil {
		t.Fatal(err)
	}
	state := `{"version":"` + version + `","port":27198,"initiated":true,"featureVersion":"` + localdb.Series(version) + `"}`
	if err := os.WriteFile(filepath.Join(h, "database", "state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	return h
}

// A database MongoDB 8 set up, on a computer whose kernel now stops MongoDB 8:
// status, start, any command that needs it and the checkup say why in plain
// words and what to do; nothing moves it back to 7.0.
func TestADatabaseTheKernelStopsSaysWhatToDo(t *testing.T) {
	h := kernelHome(t, localdb.Current.Version)
	onKernel(t, brokenKernel)
	why := "MongoDB " + localdb.Current.Version + " can't start on this computer's Linux kernel (7.0.0-27-generic)"
	todo := "Updating the kernel to 7.0.14 or later fixes it"

	r := runCLI(t, h, "database", "status")
	wantIn(t, "database status", r.stdout, why, todo, "Your projects stay safe in it")
	setupPlain(t, "database status", r.stdout)

	r = runCLI(t, h, "database", "start")
	if r.code == 0 {
		t.Fatalf("it started: %s", r.stdout)
	}
	wantIn(t, "database start", r.stderr, why, todo)
	if strings.Contains(r.stderr, "Its log says") {
		t.Errorf("the log has nothing to add:\n%s", r.stderr)
	}
	setupPlain(t, "database start", r.stderr)
	r = runCLI(t, h, "database", "start", "--json")
	var doc struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Kernel string `json:"kernel"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil || doc.Error.Code != apperr.Database || doc.Error.Details.Kernel != brokenKernel.Release {
		t.Errorf("database start --json: %v %s", err, r.stdout)
	}

	// Any command that needs the database says the same.
	r = runCLI(t, h, "project", "list")
	wantIn(t, "a command that needs the database", r.stderr, "couldn't start", why, todo)

	// Never back to 7.0.
	r = runCLI(t, h, "database", "upgrade", "--dry-run")
	wantIn(t, "database upgrade --dry-run", r.stdout, "up to date")

	// The checkup.
	cfg, err := config.Load(h)
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	localDatabaseChecks(context.Background(), cfg, cfg.Database.Local, tools.New(cfg.Tools), func(area, name, status, detail, fix string, plain ...string) {
		if name == "on this computer" {
			said = append(said, status+": "+strings.Join(plain, " | "))
		}
	})
	if len(said) != 1 || !strings.HasPrefix(said[0], "fail: The database on this computer can't start: "+why) || !strings.Contains(said[0], todo) {
		t.Errorf("doctor: %q", said)
	}
	setupPlain(t, "doctor", strings.Join(said, "\n"))
}

// A database set up with MongoDB 7.0 because of the kernel stays at 7.0 while
// the kernel stops MongoDB 8: said by its status, refused by an upgrade.
func TestUpgradeTo8WaitsForTheKernel(t *testing.T) {
	h := kernelHome(t, localdb.Older.Version)
	onKernel(t, brokenKernel)
	stays := "The database stays at MongoDB " + localdb.Older.Version + " for now"
	r := runCLI(t, h, "database", "status")
	wantIn(t, "database status", r.stdout, stays)
	if strings.Contains(r.stdout, "A newer version of it comes with this MetaRush. Upgrade it") {
		t.Errorf("an upgrade that can't run was offered:\n%s", r.stdout)
	}
	setupPlain(t, "database status", r.stdout)
	r = runCLI(t, h, "database", "upgrade", "--dry-run")
	if r.code == 0 {
		t.Fatalf("an upgrade to MongoDB 8 was planned: %s", r.stdout)
	}
	wantIn(t, "database upgrade --dry-run", r.stderr, stays, "can't start on this computer's Linux kernel")
	setupPlain(t, "database upgrade --dry-run", r.stderr)
}
