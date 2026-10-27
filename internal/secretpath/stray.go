package secretpath

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// secretEnvNames are variables whose presence marks a dotenv file as holding
// MetaRush credentials.
var secretEnvNames = []string{"METARUSH_MONGODB_URI", "METARUSH_MONGODB_ADMIN_URI", "METARUSH_S3_SECRET_ACCESS_KEY", "METARUSH_TEST_S3_SECRET_ACCESS_KEY",
	"METARUSH_MACHINE_S3_SECRET_ACCESS_KEY", "METARUSH_ELEVENLABS_API_KEY", "METARUSH_GEMINI_API_KEY"}

// IsSecretEnvName reports whether a MetaRush variable carries a secret: the
// names above, any METARUSH_ name that says it holds a key, a secret, a
// token, a password or a database address (which carries its password), and
// everything under METARUSH_PUBLISH_ (video platforms' clients and people's
// sign-ins, D305).
func IsSecretEnvName(name string) bool {
	for _, s := range secretEnvNames {
		if s == name {
			return true
		}
	}
	n, ok := strings.CutPrefix(name, "METARUSH_")
	if !ok {
		return false
	}
	return strings.HasSuffix(n, "_API_KEY") || strings.Contains(n, "SECRET") ||
		strings.Contains(n, "TOKEN") || strings.Contains(n, "PASSWORD") || strings.Contains(n, "MONGODB") && strings.HasSuffix(n, "_URI") ||
		strings.HasPrefix(n, PublishEnvPrefix)
}

// PublishEnvPrefix starts the names (after METARUSH_) under which a secrets
// file keeps video platforms' clients and people's sign-ins (D305).
const PublishEnvPrefix = "PUBLISH_"

// NeverInherited reports variables no program MetaRush starts may get, and
// that MetaRush never puts into its own environment from a .env file: video
// platforms' clients and people's sign-ins (D305) and the database
// administrator's address (D254). They are read from the file where needed.
func NeverInherited(name string) bool {
	return strings.HasPrefix(name, "METARUSH_"+PublishEnvPrefix) || name == "METARUSH_MONGODB_ADMIN_URI"
}

// Inheritable is an environment (KEY=value lines) without the variables no
// program MetaRush starts may get (NeverInherited).
func Inheritable(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); NeverInherited(k) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// WithoutSecrets is an environment (KEY=value lines) without MetaRush's
// secret variables: what a program MetaRush starts but needn't trust gets.
func WithoutSecrets(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); IsSecretEnvName(k) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// IsCredentialFileName reports names MetaRush gives files that carry credentials.
func IsCredentialFileName(name string) bool {
	l := strings.ToLower(name)
	switch {
	case strings.HasPrefix(l, "metarush-join-") && strings.HasSuffix(l, ".json"):
		return true
	case strings.HasPrefix(l, "metarush-ci-") && strings.HasSuffix(l, ".env"):
		return true
	}
	return false
}

// DotenvHasSecrets reports whether a dotenv-style file assigns a MetaRush secret
// (a value is present, not just the name).
func DotenvHasSecrets(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for n := 0; sc.Scan() && n < 2000; n++ {
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(sc.Text()), "export "))
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(strings.Trim(v, `"' `)) == "" {
			continue
		}
		k = strings.TrimSpace(k)
		for _, s := range secretEnvNames {
			if k == s {
				return true
			}
		}
		if strings.HasPrefix(k, "METARUSH_") && (strings.HasSuffix(k, "_API_KEY") || strings.HasSuffix(k, "_SECRET") || strings.HasSuffix(k, "_SECRET_ACCESS_KEY")) {
			return true
		}
	}
	return false
}

var skipDirs = map[string]bool{".git": true, "node_modules": true, "cache": true, "scratch": true, ".bundle-cache": true, "dist": true, ".selftest": true}

// FindStray lists credential files (join bundles, CI dotenv files and dotenv
// files holding MetaRush secrets) under the given directories, at most maxDepth
// levels deep and visiting at most maxEntries entries per directory. Paths in
// expected (for example the installation's own <home>/.env) are not reported.
func FindStray(dirs []string, maxDepth, maxEntries int, expected ...string) []string {
	exp := map[string]bool{}
	for _, e := range expected {
		if a, err := filepath.Abs(e); err == nil {
			exp[a] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, root := range dirs {
		root, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			continue
		}
		base := strings.Count(root, string(filepath.Separator))
		visited := 0
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			visited++
			if visited > maxEntries {
				return filepath.SkipAll
			}
			depth := strings.Count(p, string(filepath.Separator)) - base
			if d.IsDir() {
				if p != root && (skipDirs[d.Name()] || depth > maxDepth) {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || exp[p] || seen[p] {
				return nil
			}
			name := d.Name()
			if IsCredentialFileName(name) || ((name == ".env" || strings.HasSuffix(name, ".env") || strings.HasPrefix(name, ".env.")) && name != ".env.example" && DotenvHasSecrets(p)) {
				seen[p] = true
				out = append(out, p)
			}
			return nil
		})
	}
	sort.Strings(out)
	return out
}
