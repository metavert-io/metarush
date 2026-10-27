package remotion

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/models"
)

// Packages (D133): a source renders with exactly the packages its lockfile
// pins, installed by MetaRush from the public npm registry with install
// scripts off. A lockfile that names a package from anywhere else is refused
// before anything is installed.

// Registry is the only place packages come from.
const Registry = "https://registry.npmjs.org/"

type packageJSON struct {
	Name                 string            `json:"name"`
	Description          string            `json:"description"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	Overrides            json.RawMessage   `json:"overrides"`
	Workspaces           json.RawMessage   `json:"workspaces"`
	Type                 string            `json:"type"`
}

type lockPackage struct {
	Version   string `json:"version"`
	Resolved  string `json:"resolved"`
	Integrity string `json:"integrity"`
	Link      bool   `json:"link"`
	Dev       bool   `json:"dev"`
	Optional  bool   `json:"optional"`
}

type npmLock struct {
	LockfileVersion int                    `json:"lockfileVersion"`
	Packages        map[string]lockPackage `json:"packages"`
}

// pkgInfo is what MetaRush read from a version's package files.
type pkgInfo struct {
	pkg       packageJSON
	lock      npmLock
	lockBytes []byte
	lockHash  string // SHA-256 of package-lock.json
	envKey    string // what the installed packages depend on
	envPkg    []byte // the package.json written into the environment
	remotion  string
	packages  []models.RemotionPackage
}

// corePackages are Remotion and React; toolPackages never become part of the video.
func packageKind(name string) string {
	switch {
	case name == "remotion" || strings.HasPrefix(name, "@remotion/") || name == "react" || name == "react-dom":
		return "core"
	case name == "typescript" || strings.HasPrefix(name, "@types/") || name == "eslint" || strings.HasPrefix(name, "eslint-") ||
		strings.HasPrefix(name, "@eslint/") || name == "prettier" || strings.HasPrefix(name, "@typescript-eslint/") || name == "playwright-core" ||
		name == "playwright" || name == "@playwright/test" || name == "vitest" || name == "jest" || name == "tsx" || name == "ts-node":
		return "tools"
	}
	return "extra"
}

// readPackages validates package.json and package-lock.json and lists the
// packages. Problems are plain sentences for the person.
func readPackages(pkgBytes, lockBytes []byte) (*pkgInfo, error) {
	pi := &pkgInfo{}
	if err := json.Unmarshal(pkgBytes, &pi.pkg); err != nil {
		return nil, fmt.Errorf("its package.json can't be read (%s)", firstLine(err.Error()))
	}
	if err := json.Unmarshal(lockBytes, &pi.lock); err != nil {
		return nil, fmt.Errorf("its package-lock.json can't be read (%s)", firstLine(err.Error()))
	}
	if len(pi.pkg.Workspaces) > 0 && string(pi.pkg.Workspaces) != "null" {
		return nil, fmt.Errorf("it is a group of several packages (npm workspaces); MetaRush brings in one Remotion project at a time — import the folder of the project itself")
	}
	if pi.lock.LockfileVersion < 2 || pi.lock.Packages == nil {
		return nil, fmt.Errorf("its package-lock.json is in an old format (version %d); run npm install once with a current npm to update it", pi.lock.LockfileVersion)
	}
	all := map[string]bool{}
	for _, deps := range []map[string]string{pi.pkg.Dependencies, pi.pkg.DevDependencies, pi.pkg.OptionalDependencies} {
		for name, spec := range deps {
			all[name] = true
			if bad := badSpec(spec); bad != "" {
				return nil, fmt.Errorf("its package.json gets %s from %s; MetaRush installs packages only from the public npm registry", name, bad)
			}
		}
	}
	var bad []string
	for key, p := range pi.lock.Packages {
		if key == "" {
			continue
		}
		name := key
		if i := strings.LastIndex(key, "node_modules/"); i >= 0 {
			name = key[i+len("node_modules/"):]
		}
		switch {
		case p.Link || !strings.HasPrefix(key, "node_modules/") && !strings.Contains(key, "/node_modules/"):
			bad = append(bad, name+" (a local folder)")
		case !strings.HasPrefix(p.Resolved, Registry):
			where := "somewhere else"
			if p.Resolved == "" {
				where = "an unknown place"
			} else if strings.HasPrefix(p.Resolved, "git") || strings.Contains(p.Resolved, "github.com") {
				where = "a git repository"
			} else if strings.HasPrefix(p.Resolved, "file:") {
				where = "a file on the computer it was made on"
			}
			bad = append(bad, name+" ("+where+")")
		case !strings.HasPrefix(p.Integrity, "sha512-"):
			bad = append(bad, name+" (no checksum)")
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		if len(bad) > 4 {
			bad = append(bad[:4], fmt.Sprintf("%d more", len(bad)-4))
		}
		return nil, fmt.Errorf("its package-lock.json gets packages from outside the public npm registry or without a checksum: %s. MetaRush installs only checked packages from the registry", strings.Join(bad, ", "))
	}
	pi.remotion = pi.lock.Packages["node_modules/remotion"].Version
	if pi.remotion == "" {
		return nil, fmt.Errorf("its package-lock.json doesn't install remotion, so it isn't a Remotion project MetaRush can render")
	}
	for _, need := range []string{"@remotion/bundler", "@remotion/renderer"} {
		v := pi.lock.Packages["node_modules/"+need].Version
		if v == "" {
			return nil, fmt.Errorf("it doesn't install %s (usually through @remotion/cli), which MetaRush needs at the same version as remotion (%s) to read and render it; add @remotion/cli %s to it first", need, pi.remotion, pi.remotion)
		}
		if v != pi.remotion {
			return nil, fmt.Errorf("it installs %s %s but remotion %s; they must be the same version to render", need, v, pi.remotion)
		}
	}
	sum := sha256.Sum256(lockBytes)
	pi.lockHash = hex.EncodeToString(sum[:])
	pi.lockBytes = lockBytes
	// The environment's package.json: only what installing depends on.
	env := map[string]any{"name": "metarush-remotion-env", "private": true}
	if pi.pkg.Name != "" {
		env["name"] = pi.pkg.Name
	}
	for k, v := range map[string]map[string]string{"dependencies": pi.pkg.Dependencies, "devDependencies": pi.pkg.DevDependencies, "optionalDependencies": pi.pkg.OptionalDependencies, "peerDependencies": pi.pkg.PeerDependencies} {
		if len(v) > 0 {
			env[k] = v
		}
	}
	if len(pi.pkg.Overrides) > 0 && string(pi.pkg.Overrides) != "null" {
		env["overrides"] = pi.pkg.Overrides
	}
	pi.envPkg, _ = json.MarshalIndent(env, "", "  ")
	h := sha256.New()
	h.Write(pi.envPkg)
	h.Write([]byte{0})
	h.Write(lockBytes)
	pi.envKey = hex.EncodeToString(h.Sum(nil))[:20]
	for name := range all {
		dev := pi.pkg.DevDependencies[name] != "" && pi.pkg.Dependencies[name] == ""
		pi.packages = append(pi.packages, models.RemotionPackage{Name: name, Version: pi.lock.Packages["node_modules/"+name].Version, Dev: dev, Kind: packageKind(name)})
	}
	sort.Slice(pi.packages, func(i, j int) bool { return pi.packages[i].Name < pi.packages[j].Name })
	return pi, nil
}

// badSpec says where a dependency comes from when it isn't the registry.
func badSpec(spec string) string {
	s := strings.TrimSpace(spec)
	switch {
	case strings.HasPrefix(s, "file:"), strings.HasPrefix(s, "link:"), strings.HasPrefix(s, "./"), strings.HasPrefix(s, "../"), strings.HasPrefix(s, "/"), strings.HasPrefix(s, "~/"):
		return "a folder on the computer"
	case strings.HasPrefix(s, "git"), strings.HasPrefix(s, "github:"), strings.HasPrefix(s, "gitlab:"), strings.HasPrefix(s, "bitbucket:"), strings.Contains(s, ".git#"):
		return "a git repository"
	case strings.HasPrefix(s, "http:"), strings.HasPrefix(s, "https:"):
		return "a web address"
	case strings.Contains(s, "/") && !strings.HasPrefix(s, "npm:"):
		return "a git repository"
	}
	return ""
}

// diffPackages lists what changed between two package sets.
func diffPackages(before, after []models.RemotionPackage) []models.RemotionPackageChange {
	b := map[string]string{}
	for _, p := range before {
		b[p.Name] = p.Version
	}
	var out []models.RemotionPackageChange
	seen := map[string]bool{}
	for _, p := range after {
		seen[p.Name] = true
		if v, ok := b[p.Name]; !ok {
			out = append(out, models.RemotionPackageChange{Name: p.Name, To: p.Version})
		} else if v != p.Version {
			out = append(out, models.RemotionPackageChange{Name: p.Name, From: v, To: p.Version})
		}
	}
	for _, p := range before {
		if !seen[p.Name] {
			out = append(out, models.RemotionPackageChange{Name: p.Name, From: p.Version})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// packageWords says a package change in plain words.
func packageWords(cs []models.RemotionPackageChange) string {
	var parts []string
	for _, c := range cs {
		switch {
		case c.From == "":
			parts = append(parts, "adds "+c.Name+" "+c.To)
		case c.To == "":
			parts = append(parts, "removes "+c.Name)
		default:
			parts = append(parts, "moves "+c.Name+" from "+c.From+" to "+c.To)
		}
	}
	return joinAnd(parts)
}

func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
