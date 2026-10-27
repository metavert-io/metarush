package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/localdb"
	"github.com/metavert-io/metarush/internal/secretpath"
)

// The addresses of the database on this computer (D254) are kept where
// MetaRush keeps secrets, never in config.yaml, a command line or the log:
// MetaRush's own in the keychain (`mongodb-uri`) or the home's .env
// (METARUSH_MONGODB_URI), as for any database; the administrator's, used
// only for upgrades, in the keychain (`mongodb-admin-uri`) or a file of its
// own (<home>/database/admin.secret, 0600, checked like every secret file),
// never in an environment variable, so no program MetaRush starts sees it.
const localAdminSecret = "mongodb-admin-uri"

// localSecrets reads and keeps one home's database addresses.
type localSecrets struct {
	home        string
	env         bool // secrets in the home's files (--secrets env), else the keychain
	allowUnsafe bool
}

func adminSecretFile(home string) string { return filepath.Join(home, "database", "admin.secret") }

// saved is what an earlier setup kept for this home.
func (l localSecrets) saved() localdb.Saved {
	var s localdb.Saved
	if l.env {
		s.URI, _ = config.DotEnvValue(l.home, config.EnvPrefix+"MONGODB_URI")
		s.AdminURI, _ = readAdminFile(l.home)
		return s
	}
	s.URI, _ = config.ResolveSecret(l.home, config.SecretRef("mongodb-uri"))
	s.AdminURI, _ = config.ResolveSecret(l.home, config.SecretRef(localAdminSecret))
	return s
}

// keep saves an address before its sign-in is made.
func (l localSecrets) keep(user, uri string) error {
	switch {
	case user == localdb.AdminUser && l.env:
		return writeAdminFile(l.home, uri, l.allowUnsafe)
	case user == localdb.AdminUser:
		_, err := config.StoreSecret(l.home, localAdminSecret, uri)
		return err
	case l.env:
		return config.SetDotEnvVar(l.home, config.EnvPrefix+"MONGODB_URI", uri, l.allowUnsafe)
	}
	_, err := config.StoreSecret(l.home, "mongodb-uri", uri)
	return err
}

func writeAdminFile(home, uri string, allowUnsafe bool) error {
	return writeSecretFile(adminSecretFile(home), "the database's administrator address", uri, allowUnsafe)
}

// writeSecretFile writes one secret into a private file (0600), where
// secretpath allows a secret file.
func writeSecretFile(p, what, uri string, allowUnsafe bool) error {
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
	if _, err := tmp.WriteString(uri + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

func readAdminFile(home string) (string, error) {
	p := adminSecretFile(home)
	if err := secretpath.CheckUse(p, "the database's administrator address"); err != nil {
		return "", err
	}
	// Readable by others (copied, restored from a backup): made private
	// again, or refused.
	if _, loose := secretpath.LooseMode(p); loose {
		if err := os.Chmod(p, 0o600); err != nil {
			return "", fmt.Errorf("the database's administrator address can be read by others on this computer, and MetaRush couldn't make it private: %w", err)
		}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// localAdminURI reads the administrator's address from where setup kept it.
func localAdminURI(cfg *config.Config) (string, error) {
	if cfg.SecretMode() == "env" {
		v, err := readAdminFile(cfg.Home())
		if err != nil || v == "" {
			return "", errors.New("the database's administrator address isn't in this MetaRush's secrets")
		}
		return v, nil
	}
	return config.ResolveSecret(cfg.Home(), config.SecretRef(localAdminSecret))
}

// keepPrevious keeps aside a database address setup is about to replace: in
// the keychain (`mongodb-uri-previous`) or a private file of its own
// (<home>/database/previous.secret). It says where, in words.
func (l localSecrets) keepPrevious(uri string) (string, error) {
	if !l.env {
		if _, err := config.StoreSecret(l.home, "mongodb-uri-previous", uri); err != nil {
			return "", err
		}
		return "in this computer's keychain", nil
	}
	if err := writeSecretFile(filepath.Join(l.home, "database", "previous.secret"), "the earlier database address", uri, l.allowUnsafe); err != nil {
		return "", err
	}
	return "in a private file in MetaRush's folder", nil
}
