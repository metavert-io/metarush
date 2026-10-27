package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
)

type checkLog map[string]Check

func collect(h string) checkLog {
	out := checkLog{}
	homeSecurityChecks(h, func(area, name, status, detail, fix string, plain ...string) {
		ch := Check{Area: area, Name: name, Status: status, Detail: detail, Fix: fix}
		if len(plain) > 0 {
			ch.Say = plain[0]
		}
		if len(plain) > 1 {
			ch.Do = plain[1]
		}
		out[name] = ch
	})
	return out
}

func saveHome(t *testing.T, h, secrets string) {
	t.Helper()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Secrets = secrets
	if err := config.Save(h, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorSecretLocationChecks(t *testing.T) {
	fake := t.TempDir()
	t.Setenv("HOME", fake)
	t.Chdir(t.TempDir()) // not inside this repository

	// A safe home: everything ok.
	safe := filepath.Join(fake, ".metarush")
	saveHome(t, safe, "env")
	_ = os.WriteFile(config.DotEnvPath(safe), []byte("METARUSH_MONGODB_URI=mongodb://127.0.0.1:27018/x\n"), 0o644)
	got := collect(safe)
	for _, name := range []string{"home location", "home .gitignore", ".env location", "stray credential files"} {
		if got[name].Status != "ok" {
			t.Errorf("safe home: %s = %+v", name, got[name])
		}
	}
	if got[".env permissions"].Status != "warn" {
		t.Errorf("a 0644 .env must warn: %+v", got[".env permissions"])
	}

	// A home in a sync folder fails, and so does its .env.
	synced := filepath.Join(fake, "Library", "Mobile Documents", "com~apple~CloudDocs", "mr")
	saveHome(t, synced, "env")
	_ = os.WriteFile(config.DotEnvPath(synced), []byte("METARUSH_MONGODB_URI=x\n"), 0o600)
	got = collect(synced)
	if got["home location"].Status != "fail" || got[".env location"].Status != "fail" {
		t.Fatalf("iCloud home: %+v / %+v", got["home location"], got[".env location"])
	}

	// Stray bundles in ~/.metarush-bundles and the home are reported with paths.
	if err := os.MkdirAll(filepath.Join(fake, ".metarush-bundles"), 0o700); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(fake, ".metarush-bundles", "metarush-join-mini.json")
	_ = os.WriteFile(stray, []byte("{}"), 0o600)
	got = collect(safe)
	if c := got["stray credential files"]; c.Status != "warn" || !contains(c.Detail, stray) {
		t.Fatalf("stray bundle: %+v", c)
	}
}

func TestDoctorHomeInRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	t.Chdir(repo)
	h := filepath.Join(repo, "mr")
	saveHome(t, h, "env")
	_ = os.WriteFile(config.DotEnvPath(h), []byte("METARUSH_MONGODB_URI=mongodb://127.0.0.1:27018/x\n"), 0o600)
	got := collect(h)
	if got["home location"].Status != "warn" || got[".env location"].Status != "warn" {
		t.Fatalf("ignored home in a repository should warn: %+v / %+v", got["home location"], got[".env location"])
	}
	// Remove the home's ignore file: now git would commit it.
	_ = os.Remove(filepath.Join(h, config.HomeIgnoreFileName))
	got = collect(h)
	if got["home location"].Status != "fail" || got[".env location"].Status != "fail" || got["home .gitignore"].Status != "warn" {
		t.Fatalf("unignored home: %+v / %+v / %+v", got["home location"], got[".env location"], got["home .gitignore"])
	}
	// A credential dotenv elsewhere in the repository is found too.
	other := filepath.Join(repo, "deploy", "prod.env")
	_ = os.MkdirAll(filepath.Dir(other), 0o755)
	_ = os.WriteFile(other, []byte("METARUSH_S3_SECRET_ACCESS_KEY=abc\n"), 0o600)
	if c := collect(h)["stray credential files"]; c.Status != "warn" || !contains(c.Detail, other) {
		t.Fatalf("stray dotenv in the repository: %+v", c)
	}
}

func TestInitEnvModeRefusesUnsafeHome(t *testing.T) {
	fake := t.TempDir()
	t.Setenv("HOME", fake)
	o := &initOpts{secrets: "env"}
	_, err := checkHomeLocation(filepath.Join(fake, "Dropbox", "mr"), o)
	if !apperr.Is(apperr.From(err), apperr.UnsafeSecretPath) {
		t.Fatalf("env mode in Dropbox: %v", err)
	}
	o.allowUnsafeSecretPath = true
	if _, err := checkHomeLocation(filepath.Join(fake, "Dropbox", "mr"), o); err == nil {
		t.Fatal("the escape hatch never applies to sync folders")
	}
	// Keychain mode keeps no secrets in the home: allowed, with a warning.
	o = &initOpts{secrets: "keychain"}
	warns, err := checkHomeLocation(filepath.Join(fake, "Dropbox", "mr"), o)
	if err != nil || len(warns) != 1 {
		t.Fatalf("keychain home in Dropbox: %v %v", warns, err)
	}
	if _, err := exec.LookPath("git"); err == nil {
		repo := t.TempDir()
		_ = exec.Command("git", "init", "-q", repo).Run()
		o = &initOpts{secrets: "env"}
		var ae *apperr.Error
		_, err := checkHomeLocation(filepath.Join(repo, "mr"), o)
		if ae = apperr.From(err); !errors.As(ae, &ae) || ae.Code != apperr.UnsafeSecretPath {
			t.Fatalf("env mode inside a repository without the escape hatch: %v", err)
		}
		o.allowUnsafeSecretPath = true
		if _, err := checkHomeLocation(filepath.Join(repo, "mr"), o); err != nil {
			t.Fatalf("escape hatch with the home's own .gitignore: %v", err)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
