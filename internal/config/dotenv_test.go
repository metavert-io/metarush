package config

import (
	"os"
	"path/filepath"
	"testing"
)

// MetaRush knows which variables it read from a home's .env (it can change
// those), and a restart forgets them so it reads the file again; variables
// from the shell are never touched.
func TestDotEnvVariablesAreKnown(t *testing.T) {
	const fromFile, fromShell = "METARUSH_TEST_DOTENV_FILE", "METARUSH_TEST_DOTENV_SHELL"
	t.Setenv(fromFile, "")
	_ = os.Unsetenv(fromFile)
	t.Setenv(fromShell, "shell value")
	h := t.TempDir()
	if err := os.WriteFile(filepath.Join(h, ".env"), []byte(fromFile+"=file value\n"+fromShell+"=file value too\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loadDotEnv(DotEnvPath(h))
	if os.Getenv(fromFile) != "file value" || !FromDotEnv(fromFile) {
		t.Fatal("a variable read from .env is not known as such")
	}
	if os.Getenv(fromShell) != "shell value" || FromDotEnv(fromShell) {
		t.Fatal("the shell's variable was replaced or taken for the file's")
	}
	if err := SetDotEnvVar(h, fromFile, "new value", false); err != nil {
		t.Fatal(err)
	}
	if os.Getenv(fromFile) != "new value" || !FromDotEnv(fromFile) {
		t.Fatal("a value set in .env is not known as the file's")
	}
	ForgetDotEnv()
	if _, ok := os.LookupEnv(fromFile); ok {
		t.Fatal("ForgetDotEnv kept the file's variable")
	}
	if os.Getenv(fromShell) != "shell value" {
		t.Fatal("ForgetDotEnv touched the shell's variable")
	}
	loadDotEnv(DotEnvPath(h)) // the restarted process reads the file again
	if os.Getenv(fromFile) != "new value" {
		t.Fatalf("after a restart: %q", os.Getenv(fromFile))
	}
}
