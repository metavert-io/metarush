package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandAndRedact(t *testing.T) {
	t.Setenv("MR_TEST_VAR", "hello")
	if got := Expand("a=${MR_TEST_VAR} b=${MR_MISSING:dflt} c=${MR_MISSING}"); got != "a=hello b=dflt c=" {
		t.Fatalf("expand: %q", got)
	}
	r := RedactURI("mongodb+srv://user:s3cret@cluster0.example.mongodb.net/?appName=x&password=p")
	if strings.Contains(r, "s3cret") || strings.Contains(r, "password=p") {
		t.Fatalf("secret leaked: %s", r)
	}
	if !IsLocalURI("mongodb://127.0.0.1:27018/?replicaSet=rs0") || IsLocalURI("mongodb+srv://u:p@c.mongodb.net") {
		t.Fatal("IsLocalURI wrong")
	}
}

func TestSaveLoadEnvSecrets(t *testing.T) {
	h := t.TempDir()
	cfg := Default(h)
	cfg.Workspace = Ref{ID: "ws_x", Name: "W"}
	cfg.Database.URI = "${METARUSH_MONGODB_URI}"
	if err := Save(h, cfg); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(Path(h))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("config perms %v", st.Mode().Perm())
	}
	os.WriteFile(filepath.Join(h, ".env"), []byte("METARUSH_MONGODB_URI=mongodb://127.0.0.1:1/\n"), 0o600)
	os.Unsetenv("METARUSH_MONGODB_URI")
	t.Cleanup(func() { os.Unsetenv("METARUSH_MONGODB_URI") })
	got, err := Load(h)
	if err != nil {
		t.Fatal(err)
	}
	if got.Database.URI != "mongodb://127.0.0.1:1/" || got.Cache.Dir == "" || got.Dashboard.Port != 4845 {
		t.Fatalf("loaded %+v", got.Database)
	}
	if b, _ := os.ReadFile(Path(h)); strings.Contains(string(b), "127.0.0.1:1") {
		t.Fatal("secret written to config.yaml")
	}
	if KeychainAccount("/a", "x") == KeychainAccount("/b", "x") {
		t.Fatal("keychain accounts must be namespaced per home")
	}
}

func TestSetFieldReplacesOnlyTheWatchSection(t *testing.T) {
	h := t.TempDir()
	cfg := Default(h)
	cfg.Workspace = Ref{ID: "ws_x", Name: "W"}
	cfg.Database.URI = "${METARUSH_MONGODB_URI}"
	cfg.Worker.Concurrency = 3
	cfg.Worker.Watch = Watch{Enabled: false, Dir: "/old/dir", StableSeconds: 9}
	if err := Save(h, cfg); err != nil {
		t.Fatal(err)
	}
	// A comment the user added by hand next to the section must survive.
	raw, _ := os.ReadFile(Path(h))
	if err := os.WriteFile(Path(h), []byte(strings.Replace(string(raw), "    watch:", "    # my footage drop\n    watch:", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	want := Watch{Enabled: true, Dir: "/Users/x/Movies/MetaRush Inbox", StableSeconds: 4, MoveProcessed: true, ProjectID: "prj_1"}
	if err := SetField(h, []string{"worker", "watch"}, want); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(Path(h))
	s := string(raw)
	for _, keep := range []string{"${METARUSH_MONGODB_URI}", "concurrency: 3", "# my footage drop", "# MetaRush installation config"} {
		if !strings.Contains(s, keep) {
			t.Fatalf("SetField lost %q:\n%s", keep, s)
		}
	}
	if strings.Contains(s, "/old/dir") || strings.Count(s, "watch:") != 1 {
		t.Fatalf("watch section not replaced in place:\n%s", s)
	}
	t.Setenv("METARUSH_MONGODB_URI", "mongodb://127.0.0.1:1/")
	got, err := Load(h)
	if err != nil {
		t.Fatal(err)
	}
	if got.Worker.Watch != want || got.Worker.Concurrency != 3 {
		t.Fatalf("loaded watch %+v concurrency %d", got.Worker.Watch, got.Worker.Concurrency)
	}
	// A mapping cannot replace a scalar (or the other way round).
	if err := SetField(h, []string{"worker", "concurrency"}, want); err == nil {
		t.Fatal("mapping accepted over a scalar")
	}
}

func TestHeavyBatteryThresholdDefaultAndSetField(t *testing.T) {
	h := t.TempDir()
	cfg := Default(h)
	if cfg.HeavyBatteryThreshold() != 25 {
		t.Fatalf("default threshold %d", cfg.HeavyBatteryThreshold())
	}
	cfg.Workspace = Ref{ID: "ws_x", Name: "W"}
	cfg.Database.URI = "${METARUSH_MONGODB_URI}"
	cfg.Media.S3.SecretAccessKey = "keychain:s3-secret"
	if err := Save(h, cfg); err != nil {
		t.Fatal(err)
	}
	if err := SetField(h, []string{"worker", "heavyBatteryThreshold"}, 10); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(Path(h))
	s := string(raw)
	for _, want := range []string{"${METARUSH_MONGODB_URI}", "keychain:s3-secret", "heavyBatteryThreshold: 10", "# MetaRush installation config"} {
		if !strings.Contains(s, want) {
			t.Fatalf("config lost %q after SetField:\n%s", want, s)
		}
	}
	if st, _ := os.Stat(Path(h)); st.Mode().Perm() != 0o600 {
		t.Fatalf("perms %v", st.Mode().Perm())
	}
	t.Setenv("METARUSH_MONGODB_URI", "mongodb://127.0.0.1:1/")
	t.Setenv("METARUSH_S3_SECRET_ACCESS_KEY", "test-only") // the keychain reference stays in the file
	got, err := Load(h)
	if err != nil {
		t.Fatal(err)
	}
	if got.HeavyBatteryThreshold() != 10 {
		t.Fatalf("loaded threshold %d", got.HeavyBatteryThreshold())
	}
	got.SetHeavyBatteryThreshold(0)
	if got.HeavyBatteryThreshold() != 0 {
		t.Fatal("live threshold not updated")
	}
	// Setting it again replaces the value in place (no duplicate keys).
	if err := SetField(h, []string{"worker", "heavyBatteryThreshold"}, 0); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(Path(h))
	if strings.Count(string(raw), "heavyBatteryThreshold") != 1 || !strings.Contains(string(raw), "heavyBatteryThreshold: 0") {
		t.Fatalf("threshold not replaced in place:\n%s", raw)
	}
	if err := os.WriteFile(Path(h), []byte(strings.ReplaceAll(string(raw), "heavyBatteryThreshold: 0", "heavyBatteryThreshold: 140")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(h); err == nil {
		t.Fatal("out-of-range threshold accepted")
	}
}
