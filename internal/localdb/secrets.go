package localdb

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/metavert-io/metarush/internal/secretpath"
)

// Where the database's addresses are kept besides the keychain (D254): the
// administrator's in a file of its own under <home>/database, never in an
// environment variable, so no program MetaRush starts sees it. A move of the
// database (D336) keeps the new database's address the same way until the
// computer switches to it.

// AdminSecret is the administrator's address in the keychain.
const AdminSecret = "mongodb-admin-uri"

// AdminSecretFile is <home>/database/admin.secret.
func AdminSecretFile(home string) string { return filepath.Join(home, "database", "admin.secret") }

// WriteSecretFile writes one secret into a private file (0600), where
// secretpath allows a secret file.
func WriteSecretFile(p, what, value string, allowUnsafe bool) error {
	if err := secretpath.Check(p, what, allowUnsafe); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".secret-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(value + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// ReadSecretFile reads a secret file, making it private again when others
// could read it (copied, restored from a backup), or refusing it.
func ReadSecretFile(p, what string) (string, error) {
	if err := secretpath.CheckUse(p, what); err != nil {
		return "", err
	}
	if _, loose := secretpath.LooseMode(p); loose {
		if err := os.Chmod(p, 0o600); err != nil {
			return "", fmt.Errorf("%s can be read by others on this computer, and MetaRush couldn't make it private: %w", what, err)
		}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
