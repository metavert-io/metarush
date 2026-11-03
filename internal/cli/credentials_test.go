package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A private key a repository would commit isn't taken from there (D417),
// as other secrets aren't (internal/secretpath); one kept elsewhere is.
func TestAKeyGitWouldCommitIsRefused(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "-c", "maintenance.auto=false", "-c", "gc.auto=0", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	key := filepath.Join(repo, "signing-key.pem")
	if err := os.WriteFile(key, []byte("not read"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := keyFileSafe(key); err == nil || !strings.Contains(err.Error(), "git repository that would commit it") {
		t.Fatalf("a key git would commit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("signing-key.pem\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := keyFileSafe(key); err != nil {
		t.Fatalf("a key git ignores: %v", err)
	}
	elsewhere := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(elsewhere, []byte("not read"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := keyFileSafe(elsewhere); err != nil {
		t.Fatalf("a key outside any repository: %v", err)
	}
}
