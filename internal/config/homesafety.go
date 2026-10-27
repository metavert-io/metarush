package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/metavert-io/metarush/internal/secretpath"
)

// HomeIgnoreFileName is the ignore file every installation home carries.
const HomeIgnoreFileName = ".gitignore"

// homeIgnoreBody ignores everything in the home, so a home inside a repository
// (or a repository created around it later) never commits config, state, cache
// or secrets.
const homeIgnoreBody = "# MetaRush installation home: config, state, cache and secrets must never be committed.\n# Created by metarush; keep it.\n*\n"

// EnsureHomeIgnoreFile writes <home>/.gitignore ("*") when it is missing. An
// existing file is left alone.
func EnsureHomeIgnoreFile(home string) error {
	p := filepath.Join(home, HomeIgnoreFileName)
	if _, err := os.Lstat(p); err == nil {
		return nil
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return err
	}
	if _, err := f.WriteString(homeIgnoreBody); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// HomeIgnoreFileOK reports whether <home>/.gitignore exists and ignores everything.
func HomeIgnoreFileOK(home string) bool {
	b, err := os.ReadFile(filepath.Join(home, HomeIgnoreFileName))
	if err != nil {
		return false
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "*" {
			return true
		}
	}
	return false
}

// DotEnvPath is the installation's env-mode secrets file.
func DotEnvPath(home string) string { return filepath.Join(home, ".env") }

// inferSecretMode reads the unexpanded database.uri of an older config that
// predates the secrets field: keychain:… means keychain, ${…} means env.
func inferSecretMode(raw []byte) string {
	var doc struct {
		Database struct {
			URI string `yaml:"uri"`
		} `yaml:"database"`
	}
	if yaml.Unmarshal(raw, &doc) != nil {
		return ""
	}
	switch u := doc.Database.URI; {
	case IsSecretRef(u):
		return "keychain"
	case strings.Contains(u, "${"):
		return "env"
	}
	return ""
}

// DotEnvValue reads one variable from <home>/.env without putting it into this
// process's environment (a file in an unsafe place is not read).
func DotEnvValue(home, name string) (string, bool) {
	p := DotEnvPath(home)
	if _, err := os.Stat(p); err != nil || secretpath.CheckUse(p, "the secrets file") != nil {
		return "", false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(l), "export "), "=")
		if ok && strings.TrimSpace(k) == name {
			return strings.Trim(strings.TrimSpace(v), `"'`), true
		}
	}
	return "", false
}

// SetDotEnvVar sets name=value in <home>/.env (mode 0600, written atomically),
// keeping every other line, and in this process's environment (never a
// variable no program may inherit: secretpath.NeverInherited). The file's
// location is checked first: never a sync folder; inside a repository only
// when it is ignored and allowUnsafe is set.
func SetDotEnvVar(home, name, value string, allowUnsafe bool) error {
	if !validEnvName(name) {
		return fmt.Errorf("invalid variable name %q", name)
	}
	if strings.ContainsAny(value, "\n\r\x00") {
		return errors.New("value contains a line break")
	}
	if strings.TrimSpace(value) != value || strings.Trim(value, `"'`) != value {
		return errors.New("value has leading or trailing blanks or quotes, which a .env file cannot keep")
	}
	p := DotEnvPath(home)
	if err := secretpath.Check(p, "the secrets file", allowUnsafe); err != nil {
		return err
	}
	return rewriteDotEnv(p, name, quoteEnv(value), true, func() {
		if secretpath.NeverInherited(name) {
			_ = os.Unsetenv(name)
			return
		}
		_ = os.Setenv(name, value)
		rememberDotEnv(name, value)
	})
}

// UnsetDotEnvVar removes name from <home>/.env (if present) and from this
// process's environment. It reports whether the file had the variable.
func UnsetDotEnvVar(home, name string) (bool, error) {
	if !validEnvName(name) {
		return false, fmt.Errorf("invalid variable name %q", name)
	}
	p := DotEnvPath(home)
	if _, err := os.Stat(p); err != nil {
		_ = os.Unsetenv(name)
		return false, nil
	}
	had := false
	err := rewriteDotEnv(p, name, "", false, func() { _ = os.Unsetenv(name); forgetDotEnvVar(name) })
	if err == nil {
		had = true
	}
	if errors.Is(err, errNotPresent) {
		_ = os.Unsetenv(name)
		return false, nil
	}
	return had, err
}

var errNotPresent = errors.New("not present")

func rewriteDotEnv(p, name, quoted string, set bool, after func()) error {
	var lines []string
	if b, err := os.ReadFile(p); err == nil {
		lines = strings.Split(strings.TrimRight(string(b), "\n"), "\n")
		if len(lines) == 1 && lines[0] == "" {
			lines = nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	found := false
	var out []string
	for _, l := range lines {
		k, _, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(l), "export "), "=")
		if ok && strings.TrimSpace(k) == name {
			if !found && set {
				out = append(out, name+"="+quoted)
			}
			found = true
			continue
		}
		out = append(out, l)
	}
	if !found {
		if !set {
			return errNotPresent
		}
		out = append(out, name+"="+quoted)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".env.tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	body := strings.Join(out, "\n")
	if body != "" {
		body += "\n"
	}
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return err
	}
	_ = os.Chmod(p, 0o600)
	after()
	return nil
}

func validEnvName(n string) bool {
	if n == "" {
		return false
	}
	for i, r := range n {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' && i > 0) {
			return false
		}
	}
	return true
}

// quoteEnv wraps values with blanks in double quotes; loadDotEnv strips one
// layer of surrounding quotes and does no unescaping.
func quoteEnv(v string) string {
	if v == "" || strings.ContainsAny(v, " \t#") {
		return `"` + v + `"`
	}
	return v
}
