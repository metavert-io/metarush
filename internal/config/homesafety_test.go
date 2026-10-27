package config

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/secretpath"
)

func newHome(t *testing.T, root string) string {
	t.Helper()
	h := filepath.Join(root, "mrhome")
	cfg := Default(h)
	cfg.SetHome(h)
	cfg.Secrets = "env"
	cfg.Database.URI = "${METARUSH_MONGODB_URI}"
	if err := Save(h, cfg); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestSaveWritesHomeIgnoreFile(t *testing.T) {
	h := newHome(t, t.TempDir())
	if !HomeIgnoreFileOK(h) {
		t.Fatal("Save must write <home>/.gitignore containing *")
	}
	// Older homes get one on load; an existing file is kept as is.
	if err := os.Remove(filepath.Join(h, HomeIgnoreFileName)); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(h); err != nil {
		t.Fatal(err)
	}
	if !HomeIgnoreFileOK(h) {
		t.Fatal("Load must restore the ignore file")
	}
	custom := "# mine\n*\n!keep\n"
	_ = os.WriteFile(filepath.Join(h, HomeIgnoreFileName), []byte(custom), 0o644)
	_ = EnsureHomeIgnoreFile(h)
	if b, _ := os.ReadFile(filepath.Join(h, HomeIgnoreFileName)); string(b) != custom {
		t.Fatal("an existing ignore file must not be replaced")
	}
}

func TestLoadRefusesDotEnvInSyncFolder(t *testing.T) {
	fake := t.TempDir()
	t.Setenv("HOME", fake)
	h := newHome(t, filepath.Join(fake, "Library", "CloudStorage", "Dropbox"))
	if err := os.WriteFile(DotEnvPath(h), []byte("METARUSH_MONGODB_URI=mongodb://127.0.0.1:1/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(h)
	var ue *secretpath.UnsafeError
	if !errors.As(err, &ue) || ue.Verdict.Service != "Dropbox" {
		t.Fatalf("Load must refuse a .env inside Dropbox, got %v", err)
	}
	if !strings.Contains(ue.Hint, "keychain") {
		t.Fatalf("hint must offer the keychain: %q", ue.Hint)
	}
	// Without the secrets file the home itself still loads (keychain mode works there).
	_ = os.Remove(DotEnvPath(h))
	if _, err := Load(h); err != nil {
		t.Fatalf("home without .env: %v", err)
	}
}

func TestDotEnvInRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	h := newHome(t, repo) // Save wrote <home>/.gitignore, so the home is ignored
	if err := os.WriteFile(DotEnvPath(h), []byte("METARUSH_MONGODB_URI=mongodb://127.0.0.1:1/x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(h); err != nil {
		t.Fatalf("an ignored .env may be used: %v", err)
	}
	// Writing a secret there needs the explicit escape hatch.
	var ue *secretpath.UnsafeError
	if err := SetDotEnvVar(h, "METARUSH_ELEVENLABS_API_KEY", "k1", false); !errors.As(err, &ue) || !ue.EscapeHatch {
		t.Fatalf("write inside a repository without --allow-unsafe-secret-path: %v", err)
	}
	if err := SetDotEnvVar(h, "METARUSH_ELEVENLABS_API_KEY", "k1", true); err != nil {
		t.Fatalf("escape hatch: %v", err)
	}
	// Without the home's ignore file the file would be committed: refused to use and to write.
	if err := os.Remove(filepath.Join(h, HomeIgnoreFileName)); err != nil {
		t.Fatal(err)
	}
	if err := SetDotEnvVar(h, "METARUSH_ELEVENLABS_API_KEY", "k2", true); err == nil {
		t.Fatal("a path git would commit must be refused even with the escape hatch")
	}
	if err := secretpath.CheckUse(DotEnvPath(h), ".env"); err == nil {
		t.Fatal("using a .env git would commit must be refused")
	}
}

func TestDotEnvVars(t *testing.T) {
	h := newHome(t, t.TempDir())
	p := DotEnvPath(h)
	_ = os.WriteFile(p, []byte("# comment\nMETARUSH_MONGODB_URI=mongodb://127.0.0.1:27018/x\nexport OTHER=1\n"), 0o644)
	t.Setenv("METARUSH_TEST_ONLY_KEY", "")
	if err := SetDotEnvVar(h, "METARUSH_TEST_ONLY_KEY", "abc123", false); err != nil {
		t.Fatal(err)
	}
	if err := SetDotEnvVar(h, "METARUSH_TEST_ONLY_KEY", "def456", false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if got := string(b); strings.Count(got, "METARUSH_TEST_ONLY_KEY=") != 1 || !strings.Contains(got, "METARUSH_TEST_ONLY_KEY=def456") ||
		!strings.Contains(got, "# comment") || !strings.Contains(got, "export OTHER=1") {
		t.Fatalf("rewrite lost or duplicated lines:\n%s", got)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Fatalf(".env mode %v, want 0600", st.Mode().Perm())
	}
	if os.Getenv("METARUSH_TEST_ONLY_KEY") != "def456" {
		t.Fatal("process environment not updated")
	}
	had, err := UnsetDotEnvVar(h, "METARUSH_TEST_ONLY_KEY")
	if err != nil || !had {
		t.Fatalf("unset: %v %v", had, err)
	}
	b, _ = os.ReadFile(p)
	if strings.Contains(string(b), "METARUSH_TEST_ONLY_KEY") || os.Getenv("METARUSH_TEST_ONLY_KEY") != "" {
		t.Fatalf("not removed:\n%s", b)
	}
	if had, _ := UnsetDotEnvVar(h, "METARUSH_TEST_ONLY_KEY"); had {
		t.Fatal("second unset reported a removal")
	}
	for _, bad := range []string{"a\nb", " lead", "trail ", `"quoted"`} {
		if err := SetDotEnvVar(h, "METARUSH_TEST_ONLY_KEY", bad, false); err == nil {
			t.Errorf("value %q accepted", bad)
		}
	}
	if err := SetDotEnvVar(h, "bad-name", "x", false); err == nil {
		t.Error("invalid name accepted")
	}
}

func TestSecretModeInference(t *testing.T) {
	h := newHome(t, t.TempDir())
	cfg, err := Load(h)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SecretMode() != "env" {
		t.Fatalf("explicit secrets: env, got %q", cfg.SecretMode())
	}
	if inferSecretMode([]byte("database:\n  uri: keychain:mongodb-uri\n")) != "keychain" ||
		inferSecretMode([]byte("database:\n  uri: ${METARUSH_MONGODB_URI}\n")) != "env" {
		t.Fatal("inference from older configs")
	}
}

// The database administrator's address is never put into the environment,
// even when a .env file holds it (D254: it lives in a file of its own).
// Video platforms' clients and people's sign-ins kept in the secrets file
// (D305) are read from it where needed and never put into the environment,
// where Claude Code, rclone or yt-dlp would inherit them.
func TestDotEnvNeverExportsSignIns(t *testing.T) {
	h := t.TempDir()
	const client, person = "METARUSH_PUBLISH_YOUTUBE_CLIENT", "METARUSH_PUBLISH_YOUTUBE_PERSON_USR_ABC"
	if err := os.WriteFile(DotEnvPath(h), []byte(client+`={"id":"i","secret":"s"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{client, person} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	loadDotEnv(DotEnvPath(h))
	if err := SetDotEnvVar(h, person, `{"refresh":"r"}`, false); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{client, person} {
		if _, ok := os.LookupEnv(k); ok {
			t.Errorf("%s was put into the environment", k)
		}
	}
	if v, ok := DotEnvValue(h, person); !ok || v != `{"refresh":"r"}` {
		t.Errorf("DotEnvValue: %q %v", v, ok)
	}
}

func TestDotEnvNeverExportsTheAdministrator(t *testing.T) {
	h := t.TempDir()
	if err := os.WriteFile(DotEnvPath(h), []byte("METARUSH_MONGODB_ADMIN_URI=mongodb://metarush-admin:pw@127.0.0.1:1/\nMETARUSH_SOMETHING_ELSE=1\n"), 0o600); err != nil { // secret-scan: allow (a made-up test address)
		t.Fatal(err)
	}
	t.Setenv("METARUSH_MONGODB_ADMIN_URI", "")
	os.Unsetenv("METARUSH_MONGODB_ADMIN_URI")
	t.Setenv("METARUSH_SOMETHING_ELSE", "")
	os.Unsetenv("METARUSH_SOMETHING_ELSE")
	loadDotEnv(DotEnvPath(h))
	if _, ok := os.LookupEnv("METARUSH_MONGODB_ADMIN_URI"); ok {
		t.Error("the administrator's address was put into the environment")
	}
	if os.Getenv("METARUSH_SOMETHING_ELSE") != "1" {
		t.Error("other variables are still read")
	}
	if v, ok := DotEnvValue(h, "METARUSH_SOMETHING_ELSE"); !ok || v != "1" {
		t.Errorf("DotEnvValue: %q %v", v, ok)
	}
}
