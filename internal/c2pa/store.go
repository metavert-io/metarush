package c2pa

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/secretpath"
)

// Where a computer keeps its signing credential (D417): the private key in
// the keychain (or, for an installation that keeps its secrets in its
// secrets file, there — never put into the environment), the certificate
// chain, which is public, in the home's private state (0600 in a 0700
// folder). Never the database, config.yaml, a log, a command line or an
// ability. The keychain takes about 4 KB per secret, so only the key goes
// there (raw PKCS #8, which fits a 4096-bit RSA key).

// secretName is the keychain entry of the private key.
const secretName = "content-credentials"

// Where the key is kept (Saved.Kept).
const (
	KeptKeychain = "keychain"
	KeptFile     = "file" // <home>/.env of an installation with secrets: env
)

// ErrNotSetUp says this computer has no signing credential.
var ErrNotSetUp = errors.New("no signing credential on this computer")

// stateFile is the credential's public half on this computer.
type stateFile struct {
	Format string    `json:"format"` // metarush.signing/1
	Chain  string    `json:"chain"`
	Alg    string    `json:"alg"`
	SetAt  time.Time `json:"setAt"`
	SetBy  string    `json:"setBy,omitempty"`
}

const stateFormat = "metarush.signing/1"

// Saved is what this computer knows about its credential, without the key.
type Saved struct {
	Certificate models.SigningCertificate
	SetAt       time.Time
	SetBy       string
	Kept        string
	// Problems say what keeps the certificate from signing now (Public only).
	Problems []string
}

func statePath(cfg *config.Config) string {
	return filepath.Join(cfg.Home(), "state", "private", "content-credentials.json")
}

// Save keeps a credential on this computer: the key first, then the chain.
// by is who set it up (a person's name, for the record here).
func Save(cfg *config.Config, c *Credential, by string, now time.Time) (*Saved, error) {
	der, err := x509.MarshalPKCS8PrivateKey(c.Key)
	if err != nil {
		return nil, apperr.Wrap(apperr.InvalidInput, err, "MetaRush can't keep that private key.")
	}
	remember(der)
	f := statePath(cfg)
	if err := secretpath.Check(f, "the signing certificate", false); err != nil {
		return nil, err
	}
	kept := KeptKeychain
	if cfg.SecretMode() == "env" {
		kept = KeptFile
		if err := config.SetDotEnvVar(cfg.Home(), secretpath.SigningKeyEnv, base64.StdEncoding.EncodeToString(der), false); err != nil {
			return nil, err
		}
	} else if _, err := config.StoreSecret(cfg.Home(), secretName, string(der)); err != nil {
		return nil, apperr.New(apperr.NotConfigured, "MetaRush couldn't keep the signing key in this computer's keychain.").
			WithHint("Unlock the keychain (or sign in to this computer's desktop once), then set it up again.")
	}
	st := stateFile{Format: stateFormat, Chain: string(c.ChainPEM()), Alg: c.Alg, SetAt: now.UTC(), SetBy: by}
	b, _ := json.MarshalIndent(st, "", "  ")
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(filepath.Dir(f), 0o700)
	tmp := f + ".part"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, f); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	return &Saved{Certificate: c.Facts(), SetAt: st.SetAt, SetBy: by, Kept: kept}, nil
}

// Load reads this computer's credential (ErrNotSetUp when it has none). It
// refuses a key that doesn't belong to the certificate kept with it.
func Load(cfg *config.Config) (*Credential, *Saved, error) {
	st, err := readState(cfg)
	if err != nil {
		return nil, nil, err
	}
	der, kept, err := readKey(cfg)
	if err != nil {
		return nil, nil, err
	}
	remember(der)
	if _, err := x509.ParsePKCS8PrivateKey(der); err != nil {
		return nil, nil, apperr.New(apperr.NotConfigured, "The signing key kept on this computer can't be read. Set Content Credentials up again.").
			WithCommand("metarush credentials setup")
	}
	c, err := Parse([]byte(st.Chain), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err != nil {
		return nil, nil, apperr.New(apperr.NotConfigured, "The signing key kept on this computer doesn't belong to the certificate kept with it. Set Content Credentials up again.").
			WithCommand("metarush credentials setup")
	}
	return c, &Saved{Certificate: c.Facts(), SetAt: st.SetAt, SetBy: st.SetBy, Kept: kept}, nil
}

// Public reads the credential's public half only (nil when there is none):
// what a status shows without reading the key.
func Public(cfg *config.Config) (*Saved, error) {
	st, err := readState(cfg)
	if err != nil {
		if errors.Is(err, ErrNotSetUp) {
			return nil, nil
		}
		return nil, err
	}
	var chain []*x509.Certificate
	rest := []byte(st.Chain)
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		if x, err := x509.ParseCertificate(blk.Bytes); err == nil {
			chain = append(chain, x)
		}
	}
	if len(chain) == 0 {
		return nil, apperr.New(apperr.NotConfigured, "The signing certificate kept on this computer can't be read. Set Content Credentials up again.").
			WithCommand("metarush credentials setup")
	}
	c := &Credential{Chain: chain, Alg: st.Alg}
	return &Saved{Certificate: c.Facts(), SetAt: st.SetAt, SetBy: st.SetBy, Kept: keptHere(cfg), Problems: c.Problems(time.Now())}, nil
}

func keptHere(cfg *config.Config) string {
	if cfg.SecretMode() == "env" {
		return KeptFile
	}
	return KeptKeychain
}

// Remove forgets this computer's credential: the key and the chain. It
// reports whether there was one.
func Remove(cfg *config.Config) (bool, error) {
	had := false
	if _, err := os.Stat(statePath(cfg)); err == nil {
		had = true
	}
	if cfg.SecretMode() == "env" {
		removed, err := config.UnsetDotEnvVar(cfg.Home(), secretpath.SigningKeyEnv)
		if err != nil {
			return had, err
		}
		had = had || removed
	} else {
		if _, err := keyring.Get(config.KeychainService, cfg.KeychainAccount(secretName)); err == nil {
			had = true
		}
		if err := config.DeleteSecret(cfg.Home(), secretName); err != nil {
			return had, apperr.New(apperr.NotConfigured, "MetaRush couldn't remove the signing key from this computer's keychain.")
		}
	}
	if err := os.Remove(statePath(cfg)); err != nil && !os.IsNotExist(err) {
		return had, err
	}
	return had, nil
}

func readState(cfg *config.Config) (*stateFile, error) {
	b, err := os.ReadFile(statePath(cfg))
	if os.IsNotExist(err) {
		return nil, ErrNotSetUp
	}
	if err != nil {
		return nil, err
	}
	var st stateFile
	if json.Unmarshal(b, &st) != nil || st.Format != stateFormat || strings.TrimSpace(st.Chain) == "" {
		return nil, apperr.New(apperr.NotConfigured, "The signing certificate kept on this computer can't be read. Set Content Credentials up again.").
			WithCommand("metarush credentials setup")
	}
	return &st, nil
}

// readKey reads the private key where this installation keeps secrets.
func readKey(cfg *config.Config) ([]byte, string, error) {
	if cfg.SecretMode() == "env" {
		v, ok := config.DotEnvValue(cfg.Home(), secretpath.SigningKeyEnv)
		if !ok || strings.TrimSpace(v) == "" {
			return nil, "", missingKey()
		}
		der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
		if err != nil {
			return nil, "", missingKey()
		}
		return der, KeptFile, nil
	}
	v, err := keyring.Get(config.KeychainService, cfg.KeychainAccount(secretName))
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		return nil, "", missingKey()
	case err != nil:
		return nil, "", apperr.New(apperr.NotConfigured, "MetaRush couldn't read the signing key from this computer's keychain.").
			WithHint("Unlock the keychain (or sign in to this computer's desktop once), then try again.")
	}
	return []byte(v), KeptKeychain, nil
}

func missingKey() error {
	return apperr.New(apperr.NotConfigured, "The signing key isn't on this computer any more (the certificate is). Set Content Credentials up again.").
		WithCommand("metarush credentials setup")
}

// remember hides every written form of the key from the activity log of
// this process (it never goes there, but nothing that leaks a key is logged).
func remember(der []byte) {
	b64 := base64.StdEncoding.EncodeToString(der)
	forms := []string{b64, base64.RawStdEncoding.EncodeToString(der), base64.URLEncoding.EncodeToString(der),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))}
	for len(b64) > 0 { // each line of the PEM form
		n := min(64, len(b64))
		forms = append(forms, b64[:n])
		b64 = b64[n:]
	}
	logbook.RememberSecret(forms...)
}
