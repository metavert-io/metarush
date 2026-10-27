// Package providers is the registry of external integrations that need an API
// key (ElevenLabs first; video-understanding providers later). It owns where
// keys live and how their status is reported:
//
//   - a key is stored only in this installation's OS keychain, or — for
//     installations set up with --secrets env — in <home>/.env (checked by
//     internal/secretpath: never in a sync folder or where git would commit it);
//     a METARUSH_<NAME>_API_KEY environment variable overrides both;
//   - keys are per machine (each machine sets its own); they are never written
//     to MongoDB, config.yaml, logs, audit details or op results — status shows
//     only a masked suffix such as ••••3f2a;
//   - non-secret choices (voice, model, settings, budgets) are shared through
//     MongoDB (workspace defaults, project overrides).
package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/zalando/go-keyring"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
)

// Provider is one integration.
type Provider interface {
	Name() string     // stable ID: "elevenlabs"
	Title() string    // display name
	Category() string // what kind of service it is: CategoryVoice, CategoryVideo (Settings groups them)
	Summary() string  // what it does for the video, in plain words
	KeyHelp() string  // where to get a key, in plain words
	Capabilities() []string
	// Test checks a key against the provider without storing it.
	Test(ctx context.Context, key string) (*TestResult, error)
}

// Integration categories (Settings → Integrations shows one section each).
const (
	CategoryVoice = "voice" // AI narration and clearer voices (ElevenLabs)
	CategoryVideo = "video" // generative video (Google Gemini)
)

// TestResult is the outcome of a connection test.
type TestResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	Usage   *Usage `json:"usage,omitempty"`
}

// Usage is what the provider reports about the account's quota.
type Usage struct {
	Plan     string     `json:"plan,omitempty"`
	Used     int64      `json:"used"`
	Limit    int64      `json:"limit"`
	Unit     string     `json:"unit"` // characters | credits
	ResetsAt *time.Time `json:"resetsAt,omitempty"`
}

var (
	regMu    sync.RWMutex
	registry = map[string]Provider{}
)

// Register adds a provider (called from init).
func Register(p Provider) {
	regMu.Lock()
	defer regMu.Unlock()
	registry[p.Name()] = p
}

// Get returns a registered provider.
func Get(name string) (Provider, error) {
	regMu.RLock()
	defer regMu.RUnlock()
	p, ok := registry[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		names, titles := make([]string, 0, len(registry)), make([]string, 0, len(registry))
		for n := range registry {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			titles = append(titles, registry[n].Title())
		}
		return nil, apperr.New(apperr.NotFound, "MetaRush has no integration called “%s”.", name).
			WithHint("It can connect to " + strings.Join(titles, ", ") + ".").WithDetails(map[string]any{"available": names})
	}
	return p, nil
}

// titleOf is a provider's display name ("ElevenLabs"), or its name when unknown.
func titleOf(name string) string {
	regMu.RLock()
	defer regMu.RUnlock()
	if p, ok := registry[name]; ok {
		return p.Title()
	}
	return name
}

// All lists providers by name.
func All() []Provider {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Provider, 0, len(registry))
	for _, p := range registry {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// EnvVar is the environment variable that supplies (and overrides) a key.
func EnvVar(name string) string {
	return config.EnvPrefix + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_API_KEY"
}

// secretName is the keychain entry for a provider key.
func secretName(name string) string { return "provider-" + name }

// Key sources.
const (
	SourceKeychain    = "keychain"
	SourceFile        = "file"        // <home>/.env (env-mode installations)
	SourceEnvironment = "environment" // set in the process environment
)

// Key returns the provider key for this installation and where it came from.
// It returns "" (and no error) when no key is configured.
func Key(cfg *config.Config, name string) (key, source string, err error) {
	// The file is read fresh: another process (the CLI, the dashboard) may
	// have just changed it while this one was running.
	if v, ok := dotEnvValue(cfg.Home(), EnvVar(name)); ok && v != "" {
		return seen(v), SourceFile, nil
	}
	if v := strings.TrimSpace(os.Getenv(EnvVar(name))); v != "" && !dotEnvHas(cfg.Home(), EnvVar(name)) {
		return seen(v), SourceEnvironment, nil
	}
	if cfg.SecretMode() == "env" {
		return "", "", nil
	}
	v, err := keyring.Get(config.KeychainService, cfg.KeychainAccount(secretName(name)))
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		return "", "", nil
	case err != nil:
		return "", "", apperr.Wrap(apperr.NotConfigured, err, "MetaRush couldn't read the %s key from this computer's keychain.", titleOf(name))
	}
	return seen(strings.TrimSpace(v)), SourceKeychain, nil
}

func dotEnvHas(home, name string) bool {
	_, ok := dotEnvValue(home, name)
	return ok
}

// dotEnvValue reads one variable from <home>/.env (refusing a file in an unsafe place).
func dotEnvValue(home, name string) (string, bool) {
	p := config.DotEnvPath(home)
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

// ValidateKeyShape rejects values that cannot be an API key (before any network call).
func ValidateKeyShape(key string) error {
	if len(key) < 16 || len(key) > 512 {
		return apperr.New(apperr.InvalidInput, "That doesn't look like an API key: keys are 16 to 512 characters long.")
	}
	for _, r := range key {
		if unicode.IsSpace(r) || !unicode.IsPrint(r) {
			return apperr.New(apperr.InvalidInput, "An API key can't contain spaces or line breaks. Copy it again, without anything around it.")
		}
	}
	return nil
}

// StoreKey saves a key where this installation keeps secrets. allowUnsafe
// passes --allow-unsafe-secret-path to the env-mode file check.
func StoreKey(cfg *config.Config, name, key string, allowUnsafe bool) (string, error) {
	key, err := ShapeKey(name, key)
	if err != nil {
		return "", err
	}
	seen(key)
	if v := os.Getenv(EnvVar(name)); v != "" && !dotEnvHas(cfg.Home(), EnvVar(name)) {
		return "", apperr.New(apperr.Conflict, "This machine's settings already give MetaRush a %s key, and it would override a saved key.", titleOf(name)).
			WithHint("Remove " + EnvVar(name) + " from this machine's environment (or change it there) instead.").WithDetails(map[string]any{"envVar": EnvVar(name)})
	}
	if cfg.SecretMode() == "env" {
		if err := config.SetDotEnvVar(cfg.Home(), EnvVar(name), key, allowUnsafe); err != nil {
			return "", err
		}
		return SourceFile, nil
	}
	if _, err := config.StoreSecret(cfg.Home(), secretName(name), key); err != nil {
		return "", apperr.Wrap(apperr.NotConfigured, err, "MetaRush couldn't save the key in this computer's keychain.")
	}
	return SourceKeychain, nil
}

// RemoveKey deletes a stored key. It refuses keys that come from the process
// environment (they are not MetaRush's to delete).
func RemoveKey(cfg *config.Config, name string) (bool, error) {
	_, src, err := Key(cfg, name)
	if err != nil {
		return false, err
	}
	switch src {
	case "":
		return false, nil
	case SourceEnvironment:
		return false, apperr.New(apperr.Conflict, "This key comes from this machine's settings, outside MetaRush, so MetaRush can't remove it.").
			WithHint("Remove " + EnvVar(name) + " where it is set.").WithDetails(map[string]any{"envVar": EnvVar(name)})
	case SourceFile:
		return config.UnsetDotEnvVar(cfg.Home(), EnvVar(name))
	}
	if err := config.DeleteSecret(cfg.Home(), secretName(name)); err != nil {
		return false, apperr.Wrap(apperr.NotConfigured, err, "MetaRush couldn't remove the key from this computer's keychain.")
	}
	return true, nil
}

// ShapeKey checks a key for an integration and returns what MetaRush keeps:
// an API key as it is, or what a KeyShaper makes of a key that isn't one
// line (a service account's JSON key, D282).
func ShapeKey(name, key string) (string, error) {
	if p, err := Get(name); err == nil {
		if ks, ok := p.(KeyShaper); ok {
			k, err := ks.NormalizeKey(key)
			if err != nil {
				return "", err
			}
			if len(k) > maxShapedKey || strings.ContainsAny(k, "\r\n\x00") {
				return "", apperr.New(apperr.InvalidInput, "That key is larger than MetaRush can keep.")
			}
			return k, nil
		}
	}
	return key, ValidateKeyShape(key)
}

// MaskFor says which key an integration has without showing it: a
// KeyShaper's words, else the last four characters.
func MaskFor(name, key string) string {
	if p, err := Get(name); err == nil {
		if ks, ok := p.(KeyShaper); ok {
			return ks.DescribeKey(key)
		}
	}
	return Mask(key)
}

// Mask shows only the last four characters of a key: ••••3f2a.
func Mask(key string) string {
	key = strings.TrimSpace(key)
	if len(key) < 12 {
		return "••••"
	}
	return "••••" + key[len(key)-4:]
}

// TestRecord is the last connection test on this machine.
type TestRecord struct {
	OK      bool      `json:"ok"`
	At      time.Time `json:"at"`
	Message string    `json:"message"`
	Usage   *Usage    `json:"usage,omitempty"`
}

// machineState is per-installation, non-secret provider state (state/providers.json, 0600).
type machineState struct {
	Masked   string      `json:"masked,omitempty"`
	SetAt    *time.Time  `json:"setAt,omitempty"`
	LastTest *TestRecord `json:"lastTest,omitempty"`
}

var stateMu sync.Mutex

func statePath(cfg *config.Config) string { return filepath.Join(cfg.StateDir(), "providers.json") }

func loadState(cfg *config.Config) map[string]machineState {
	out := map[string]machineState{}
	if b, err := os.ReadFile(statePath(cfg)); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func updateState(cfg *config.Config, name string, fn func(*machineState)) {
	stateMu.Lock()
	defer stateMu.Unlock()
	st := loadState(cfg)
	ms := st[name]
	fn(&ms)
	if ms == (machineState{}) {
		delete(st, name)
	} else {
		st[name] = ms
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	_ = os.MkdirAll(cfg.StateDir(), 0o700)
	tmp := statePath(cfg) + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, statePath(cfg))
	}
}

// Status is what the dashboard and `metarush providers status` show. It never
// contains a key.
type Status struct {
	Name         string      `json:"name"`
	Title        string      `json:"title"`
	Category     string      `json:"category" jsonschema:"enum=voice,enum=video,enum=presenter"`
	Summary      string      `json:"summary"`
	KeyHelp      string      `json:"keyHelp"`
	Capabilities []string    `json:"capabilities"`
	Configured   bool        `json:"configured"`
	Source       string      `json:"source,omitempty" jsonschema:"enum=keychain,enum=file,enum=environment"`
	Masked       string      `json:"masked,omitempty"`
	SetAt        *time.Time  `json:"setAt,omitempty"`
	LastTest     *TestRecord `json:"lastTest,omitempty"`
	EnvVar       string      `json:"envVar"`
	SecretMode   string      `json:"secretMode" jsonschema:"enum=keychain,enum=env"`
	Machine      string      `json:"machine"`
	Note         string      `json:"note,omitempty"`
}

// StatusFor builds a provider's status for this installation.
func StatusFor(cfg *config.Config, p Provider) Status {
	st := Status{Name: p.Name(), Title: p.Title(), Category: p.Category(), Summary: p.Summary(), KeyHelp: p.KeyHelp(), Capabilities: p.Capabilities(),
		EnvVar: EnvVar(p.Name()), SecretMode: cfg.SecretMode(), Machine: cfg.Installation.Name}
	key, src, err := Key(cfg, p.Name())
	ms := loadState(cfg)[p.Name()]
	switch {
	case err != nil:
		st.Note = apperr.From(err).Message
	case key != "":
		st.Configured, st.Source, st.Masked = true, src, MaskFor(p.Name(), key)
		st.SetAt = ms.SetAt
		if ms.Masked == st.Masked {
			st.LastTest = ms.LastTest // a test of an older key says nothing about this one
		}
	default:
		st.Note = "Not set up on this machine yet. Each machine needs its own key."
	}
	return st
}

// RecordTest remembers a test result for the stored key (never the key).
func RecordTest(cfg *config.Config, name, masked string, r *TestResult) {
	updateState(cfg, name, func(ms *machineState) {
		if ms.Masked != masked {
			return
		}
		ms.LastTest = &TestRecord{OK: r.OK, At: time.Now().UTC(), Message: r.Message, Usage: r.Usage}
	})
}

func recordSet(cfg *config.Config, name, masked string, r *TestResult) {
	updateState(cfg, name, func(ms *machineState) {
		now := time.Now().UTC()
		ms.Masked, ms.SetAt, ms.LastTest = masked, &now, nil
		if r != nil {
			ms.LastTest = &TestRecord{OK: r.OK, At: now, Message: r.Message, Usage: r.Usage}
		}
	})
}

func forget(cfg *config.Config, name string) {
	updateState(cfg, name, func(ms *machineState) { *ms = machineState{} })
}

// MustKey returns the configured key or a clear "not configured" error.
func MustKey(cfg *config.Config, name string) (string, error) {
	key, _, err := Key(cfg, name)
	if err != nil {
		return "", err
	}
	if key == "" {
		p, _ := Get(name)
		title := name
		if p != nil {
			title = p.Title()
		}
		return "", apperr.New(apperr.NotConfigured, "%s is not set up on this machine.", title).
			WithHint(fmt.Sprintf("Add your own %s key in Settings → Integrations. Each machine needs its own key.", title)).
			WithCommand("metarush providers set " + name)
	}
	return key, nil
}
