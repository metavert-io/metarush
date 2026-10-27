package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/zalando/go-keyring"
)

// KeychainService is the OS keychain service name for all MetaRush secrets.
const KeychainService = "MetaRush"

const keychainPrefix = "keychain:"

// IsSecretRef reports whether v is a keychain reference.
func IsSecretRef(v string) bool { return strings.HasPrefix(v, keychainPrefix) }

// SecretRef builds a keychain reference for a secret name.
func SecretRef(name string) string { return keychainPrefix + name }

// ResolveSecret returns v unchanged unless it is a keychain reference.
func ResolveSecret(home, v string) (string, error) {
	if !IsSecretRef(v) {
		return v, nil
	}
	name := strings.TrimPrefix(v, keychainPrefix)
	s, err := keyring.Get(KeychainService, KeychainAccount(home, name))
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", fmt.Errorf("MetaRush can't find its secret %q in this machine's keychain: set MetaRush up again, or set the matching METARUSH_ environment variable", name)
		}
		return "", fmt.Errorf("MetaRush couldn't read its secret %q from this machine's keychain: %w", name, err)
	}
	return s, nil
}

// StoreSecret saves a secret in the OS keychain and returns its reference.
func StoreSecret(home, name, value string) (string, error) {
	if err := keyring.Set(KeychainService, KeychainAccount(home, name), value); err != nil {
		return "", fmt.Errorf("MetaRush couldn't save its secret %q in this machine's keychain: %w", name, err)
	}
	return SecretRef(name), nil
}

// DeleteSecret removes a keychain secret (used by uninstall only when asked).
func DeleteSecret(home, name string) error {
	err := keyring.Delete(KeychainService, KeychainAccount(home, name))
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// RedactURI hides credentials in a MongoDB URI for display.
func RedactURI(uri string) string {
	if uri == "" {
		return ""
	}
	u, err := url.Parse(uri)
	if err != nil {
		return "<unparseable uri>"
	}
	if u.User != nil {
		u.User = url.UserPassword(u.User.Username(), "REDACTED")
	}
	q := u.Query()
	for k := range q {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "password") || strings.Contains(lk, "secret") || strings.Contains(lk, "token") {
			q.Set(k, "REDACTED")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// HostOf returns the host part of a MongoDB URI (no credentials).
func HostOf(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	return u.Host
}

// IsLocalURI reports whether a MongoDB URI points at this machine.
func IsLocalURI(uri string) bool {
	u, err := url.Parse(uri)
	if err != nil {
		return false
	}
	for _, h := range strings.Split(u.Host, ",") {
		host := h
		if i := strings.LastIndex(h, ":"); i >= 0 {
			host = h[:i]
		}
		if host != "127.0.0.1" && host != "localhost" && host != "[::1]" {
			return false
		}
	}
	return u.Host != ""
}
