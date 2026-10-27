// Package config loads a MetaRush installation's configuration.
//
// Every installation lives in a home directory (default ~/.metarush, override with
// --home or METARUSH_HOME). The home holds config.yaml, the local media cache,
// scratch space, daemon state and logs. Two homes on one machine are fully
// independent installations — this is how the dev instance stays separate from an
// installed copy, and how the multi-installation tests run.
//
// config.yaml supports ${VAR} and ${VAR:default} expansion. Secret values may be
// written as "keychain:<name>" and are resolved from the OS keychain; environment
// variables (METARUSH_*) override file values. Secrets are never written into
// config.yaml by MetaRush itself.
package config

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/metavert-io/metarush/internal/secretpath"
)

// EnvPrefix prefixes every environment override.
const EnvPrefix = "METARUSH_"

// Config is the on-disk installation configuration.
type Config struct {
	Version       int           `yaml:"version"`
	Workspace     Ref           `yaml:"workspace"`
	Installation  Ref           `yaml:"installation"`
	User          UserRef       `yaml:"user"`
	Database      Database      `yaml:"database"`
	Media         Media         `yaml:"media"`
	Cache         Cache         `yaml:"cache"`
	Scratch       Scratch       `yaml:"scratch"`
	Dashboard     Dashboard     `yaml:"dashboard"`
	Worker        Worker        `yaml:"worker"`
	Transcription Transcription `yaml:"transcription"`
	Tools         Tools         `yaml:"tools"`
	Assistant     Assistant     `yaml:"assistant,omitempty"`
	// Secrets records where this installation keeps its secrets: keychain (the
	// default) or env (METARUSH_* variables, optionally from <home>/.env).
	Secrets string `yaml:"secrets,omitempty"`
	Log     Log    `yaml:"log,omitempty"`
	// Imports configures link imports (yt-dlp) on this machine.
	Imports Imports `yaml:"imports,omitempty"`
	// Resources configures the resources library on this machine: where its
	// linked folders are, and which files upload as soon as they are added.
	Resources Resources `yaml:"resources,omitempty"`
	// Improve schedules the weekly improvement report on this machine.
	Improve Improve `yaml:"improve,omitempty"`
	// Recordings limits screen recordings received on this machine (D258).
	Recordings Recordings `yaml:"recordings,omitempty"`

	home       string        `yaml:"-"`
	secretMode string        `yaml:"-"` // inferred from the raw file when Secrets is empty
	live       *liveSettings `yaml:"-"`
}

// SecretMode is keychain or env: where MetaRush stores new secrets (provider
// keys, rotated credentials) for this installation.
func (c *Config) SecretMode() string {
	switch {
	case c.Secrets != "":
		return c.Secrets
	case c.secretMode != "":
		return c.secretMode
	}
	return "keychain"
}

// Ref names a workspace or installation.
type Ref struct {
	ID   string `yaml:"id" json:"id"`
	Name string `yaml:"name" json:"name"`
}

// UserRef identifies the person operating this installation.
type UserRef struct {
	ID    string `yaml:"id" json:"id"`
	Name  string `yaml:"name" json:"name"`
	Email string `yaml:"email" json:"email"`
}

// Database configures the shared MongoDB.
type Database struct {
	URI  string `yaml:"uri"`
	Name string `yaml:"name"`
	// Atlas management-plane identity recorded at setup for auditing only. The
	// connection string cannot prove which organization owns a cluster.
	AtlasOrgID     string `yaml:"atlasOrgId,omitempty"`
	AtlasOrgName   string `yaml:"atlasOrgName,omitempty"`
	AtlasProjectID string `yaml:"atlasProjectId,omitempty"`
	// Local is set when the database is the one on this computer (D254,
	// internal/localdb): MetaRush downloaded MongoDB into <home>/database,
	// starts it when something needs it and keeps it running from the
	// daemon. URI still holds the address with its sign-in (keychain or .env).
	Local *LocalDatabase `yaml:"local,omitempty"`
}

// LocalDatabase is the database on this computer.
type LocalDatabase struct {
	// Port is where it listens on 127.0.0.1 (chosen at setup, never 27018).
	Port int `yaml:"port"`
}

// Media selects the canonical media store.
type Media struct {
	Provider   string     `yaml:"provider"` // filesystem | s3 | gcs
	Filesystem Filesystem `yaml:"filesystem,omitempty"`
	S3         S3         `yaml:"s3,omitempty"`
	GCS        GCS        `yaml:"gcs,omitempty"`
}

// Filesystem is a local or shared-mount media root.
type Filesystem struct {
	Root   string `yaml:"root"`
	Shared bool   `yaml:"shared"` // true only for a NAS/shared mount every collaborator reaches
}

// S3 configures any S3-compatible object store (Tigris, R2, B2, AWS, GCS interop).
type S3 struct {
	Preset          string `yaml:"preset,omitempty"` // tigris | r2 | b2 | aws | gcs | custom
	Endpoint        string `yaml:"endpoint"`
	Region          string `yaml:"region"`
	Bucket          string `yaml:"bucket"`
	Prefix          string `yaml:"prefix,omitempty"`
	AccessKeyID     string `yaml:"accessKeyId"`
	SecretAccessKey string `yaml:"secretAccessKey"`
	ForcePathStyle  bool   `yaml:"forcePathStyle,omitempty"`
}

// GCS configures the native Google Cloud Storage driver. Authentication is a
// service-account JSON key file (the path is stored here, never the contents)
// or, when CredentialsFile is empty, Application Default Credentials.
type GCS struct {
	Bucket          string `yaml:"bucket"`
	Prefix          string `yaml:"prefix,omitempty"`
	CredentialsFile string `yaml:"credentialsFile,omitempty"`
}

// Cache configures the local content-addressed cache.
type Cache struct {
	Dir   string  `yaml:"dir"`
	MaxGB float64 `yaml:"maxGB"`
	// PruneIntervalMinutes is how often the daemon enforces MaxGB (default 10;
	// a negative value turns automatic pruning off).
	PruneIntervalMinutes int `yaml:"pruneIntervalMinutes,omitempty"`
}

// PruneInterval is how often a long-running process enforces the cache limit
// (0 = disabled).
func (c Cache) PruneInterval() time.Duration {
	switch {
	case c.PruneIntervalMinutes < 0:
		return 0
	case c.PruneIntervalMinutes == 0:
		return 10 * time.Minute
	}
	return time.Duration(c.PruneIntervalMinutes) * time.Minute
}

// Scratch configures temporary render/derive space.
type Scratch struct {
	Dir        string `yaml:"dir"`
	RetainDays int    `yaml:"retainDays"`
}

// Dashboard configures the local HTTP server.
type Dashboard struct {
	Host       string   `yaml:"host"`
	Port       int      `yaml:"port"`
	DevOrigins []string `yaml:"devOrigins,omitempty"`
	// PublicURL is the address people use from their other computers (D128),
	// e.g. https://studio.tailXXXX.ts.net: `tailscale serve` answers there with
	// HTTPS and hands each request to this dashboard on 127.0.0.1. The
	// dashboard keeps listening on Host only. Empty: this computer only.
	PublicURL string `yaml:"publicURL,omitempty"`
	// TailscaleLogins are the Tailscale accounts that are signed in by
	// themselves when they open PublicURL (D129); each becomes a person in the
	// workspace. Empty: people coming through PublicURL need a sign-in link.
	TailscaleLogins []string `yaml:"tailscaleLogins,omitempty"`
	// MachineName is how the dashboard names this computer to people using it
	// from another one ("on Sam's MacBook"); empty uses the installation's name.
	MachineName string `yaml:"machineName,omitempty"`
}

// Worker configures the local job worker and watched inbox.
type Worker struct {
	Enabled      bool `yaml:"enabled"`
	Concurrency  int  `yaml:"concurrency"`
	HeavyRenders bool `yaml:"heavyRenders"`
	// HeavyBatteryThreshold (percent) defers heavy jobs (transcription, final
	// renders, mezzanines) only while on battery, not charging and below it.
	// Unset means 25; 0 never defers. Read it with Config.HeavyBatteryThreshold.
	HeavyBatteryThreshold *int  `yaml:"heavyBatteryThreshold,omitempty"`
	Watch                 Watch `yaml:"watch"`
}

// DefaultHeavyBatteryThreshold is used when worker.heavyBatteryThreshold is unset.
const DefaultHeavyBatteryThreshold = 25

// HeavyBatteryThreshold is the live threshold (changed at runtime by
// SetHeavyBatteryThreshold, which the dashboard's Settings control uses).
func (c *Config) HeavyBatteryThreshold() int {
	if c.live != nil {
		return int(c.live.threshold.Load())
	}
	return c.Worker.heavyThreshold()
}

func (w Worker) heavyThreshold() int {
	if w.HeavyBatteryThreshold == nil {
		return DefaultHeavyBatteryThreshold
	}
	return *w.HeavyBatteryThreshold
}

// SetHeavyBatteryThreshold changes the threshold for everything sharing this
// config (the daemon's worker picks it up on its next lease).
func (c *Config) SetHeavyBatteryThreshold(n int) {
	c.ensureLive()
	c.live.threshold.Store(int32(n))
	v := n
	c.Worker.HeavyBatteryThreshold = &v
}

// liveSettings are values that change at runtime and are read concurrently.
type liveSettings struct {
	threshold atomic.Int32
	remote    atomic.Pointer[Remote]
}

func (c *Config) ensureLive() {
	if c.live == nil {
		c.live = &liveSettings{}
		c.live.threshold.Store(int32(c.Worker.heavyThreshold()))
	}
}

// Watch configures the watched inbox folder.
type Watch struct {
	Enabled       bool   `yaml:"enabled"`
	Dir           string `yaml:"dir"`
	StableSeconds int    `yaml:"stableSeconds"`
	MoveProcessed bool   `yaml:"moveProcessed"`
	ProjectID     string `yaml:"projectId,omitempty"`
}

// Transcription selects the local transcript provider.
type Transcription struct {
	Provider string `yaml:"provider"` // auto | whispercpp | parakeet | none
	Model    string `yaml:"model,omitempty"`
	Language string `yaml:"language,omitempty"`
	// Resident keeps the transcription model loaded in memory between batches
	// and clips (default on; false runs one sherpa-onnx process per batch, D93,
	// and one whisper-cli process per clip, D121).
	Resident *bool `yaml:"resident,omitempty"`
}

// KeepModelLoaded reports whether the transcription model may stay in memory
// (transcription.resident, default true; METARUSH_TRANSCRIPTION_RESIDENT=0 turns it off).
func (t Transcription) KeepModelLoaded() bool {
	if v, ok := os.LookupEnv(EnvPrefix + "TRANSCRIPTION_RESIDENT"); ok && v != "" {
		return v != "0" && !strings.EqualFold(v, "false") && !strings.EqualFold(v, "off")
	}
	return t.Resident == nil || *t.Resident
}

// Tools pins external binaries. Empty means "look up on PATH".
type Tools struct {
	FFmpeg     string `yaml:"ffmpeg,omitempty"`
	FFprobe    string `yaml:"ffprobe,omitempty"`
	Node       string `yaml:"node,omitempty"`
	VideoDir   string `yaml:"videoDir,omitempty"` // a live checkout's video/ package: used only with DevRenderer (D75)
	WhisperCLI string `yaml:"whisperCli,omitempty"`
	// WhisperServer is whisper.cpp's whisper-server, which keeps the whisper
	// model loaded between clips. Default: the one next to whisper-cli (never
	// another on PATH: it must come from the same build).
	WhisperServer string `yaml:"whisperServer,omitempty"`
	SherpaDir     string `yaml:"sherpaDir,omitempty"`
	ModelsDir     string `yaml:"modelsDir,omitempty"`
	Claude        string `yaml:"claude,omitempty"` // Claude Code, for the dashboard assistant
	YtDlp         string `yaml:"ytDlp,omitempty"`  // yt-dlp, for link imports (YouTube, TikTok, X)
	Deno          string `yaml:"deno,omitempty"`   // the JavaScript runtime yt-dlp uses for YouTube
	Brew          string `yaml:"brew,omitempty"`   // Homebrew, only to update a Homebrew yt-dlp (default: found on PATH)
	// Vision is metarush-vision, the on-device helper that looks at frames with
	// Apple Vision (macOS). Default: <home>/tools/metarush-vision when MetaRush
	// built it there, else next to the metarush binary or on PATH.
	Vision string `yaml:"vision,omitempty"`
	// DevRenderer renders from VideoDir (a developer's live checkout, for
	// Remotion Studio work) instead of this version's own renderer (D75); a
	// VideoDir without it is an older installation's setting and is ignored.
	DevRenderer bool `yaml:"devRenderer,omitempty"`
	// NPM is npm's command-line script (npm-cli.js) or launcher; empty means
	// the npm next to Node (used only to set up the renderer).
	NPM string `yaml:"npm,omitempty"`
	// Launchctl and Systemctl install the background service (default: found
	// on PATH; tests point them at fakes).
	Launchctl string `yaml:"launchctl,omitempty"`
	Systemctl string `yaml:"systemctl,omitempty"`
	// Git, GH and D2 bring in GitHub repositories (git, gh: the person's own
	// sign-in, never stored) and draw D2 diagrams (default: found on PATH).
	Git string `yaml:"git,omitempty"`
	GH  string `yaml:"gh,omitempty"`
	D2  string `yaml:"d2,omitempty"`
	// Tailscale is the Tailscale command-line tool, read for remote access
	// (`metarush remote`, D128); default: found on PATH or in the Mac app.
	Tailscale string `yaml:"tailscale,omitempty"`
	// Rclone is the person's own rclone, through which MetaRush reads Google
	// Drive folders without syncing them (D220); default: found on PATH.
	Rclone string `yaml:"rclone,omitempty"`
}

// Improve schedules the weekly improvement report (D78): the activity log's
// signals, read by Claude Code headless with the metarush-improve skill, saved
// as improvement ideas. Off unless Weekly is set.
type Improve struct {
	Weekly bool `yaml:"weekly,omitempty"`
	// Day is the weekday (monday … sunday, default monday) and Hour the local
	// hour (0-23, default 9) after which the week's report is due.
	Day  string `yaml:"day,omitempty"`
	Hour *int   `yaml:"hour,omitempty"`
	// BudgetUSD caps one report's Claude Code spend (default 2).
	BudgetUSD float64 `yaml:"budgetUsd,omitempty"`
	Model     string  `yaml:"model,omitempty"`
}

// DefaultImproveBudgetUSD caps a report's Claude Code spend when improve.budgetUsd is unset.
const DefaultImproveBudgetUSD = 2.0

// ReportDay is the configured weekday (default Monday).
func (i Improve) ReportDay() time.Weekday {
	for d := time.Sunday; d <= time.Saturday; d++ {
		if strings.EqualFold(i.Day, d.String()) || (len(i.Day) == 3 && strings.EqualFold(i.Day, d.String()[:3])) {
			return d
		}
	}
	return time.Monday
}

// ReportHour is the configured local hour (default 9).
func (i Improve) ReportHour() int {
	if i.Hour == nil || *i.Hour < 0 || *i.Hour > 23 {
		return 9
	}
	return *i.Hour
}

// Budget is the per-report spending limit in US dollars.
func (i Improve) Budget() float64 {
	if i.BudgetUSD <= 0 {
		return DefaultImproveBudgetUSD
	}
	return i.BudgetUSD
}

// VisionToolPath is where MetaRush builds metarush-vision for an installation.
func VisionToolPath(home string) string { return filepath.Join(home, "tools", "metarush-vision") }

// Imports configures link imports on this machine.
type Imports struct {
	// Browser is the browser whose sign-in this machine last used for an import
	// (chrome | safari | firefox | brave | edge). It is only remembered as the
	// suggestion: every import asks for the sign-in again (opt-in per import).
	Browser string `yaml:"browser,omitempty"`
	// AutoUpdate lets the daemon update yt-dlp about once a week while no
	// import runs (default on; false turns it off).
	AutoUpdate *bool `yaml:"autoUpdate,omitempty"`
}

// Recordings limits the pieces of screen recordings this machine receives
// (D258): how much one recording may hold, the free space it never goes
// below, and how long pieces of a recording nobody finished are kept.
type Recordings struct {
	// MaxGB is the most one recording holds (default 100).
	MaxGB int `yaml:"maxGB,omitempty"`
	// MinFreeGB is the free disk space pieces never take it below (default 2).
	MinFreeGB int `yaml:"minFreeGB,omitempty"`
	// KeepDays is how long the pieces of a recording nobody finished are kept
	// (default 14; its maker is told two days before).
	KeepDays int `yaml:"keepDays,omitempty"`
}

// MaxBytes is the most one recording holds.
func (r Recordings) MaxBytes() int64 {
	if r.MaxGB > 0 {
		return int64(r.MaxGB) << 30
	}
	return 100 << 30
}

// MinFreeBytes is the free space pieces never take the disk below.
func (r Recordings) MinFreeBytes() int64 {
	if r.MinFreeGB > 0 {
		return int64(r.MinFreeGB) << 30
	}
	return 2 << 30
}

// Keep is how long unfinished pieces are kept.
func (r Recordings) Keep() time.Duration {
	if r.KeepDays > 0 {
		return time.Duration(r.KeepDays) * 24 * time.Hour
	}
	return 14 * 24 * time.Hour
}

// Resources configures the resources library on this machine (D79, D81).
type Resources struct {
	// UploadSmallMB: files from linked folders up to this size upload to the
	// media store as soon as they are added; larger ones wait until a cut first
	// uses them (default 25; a folder can say all or on-use instead).
	UploadSmallMB int `yaml:"uploadSmallMB,omitempty"`
	// Folders are the linked folders on this machine: the shared record's ID and
	// where the folder is here. Edited by `metarush resources link` and the dashboard.
	Folders []FolderPath `yaml:"folders,omitempty"`
	// Repos limits what a repository snapshot keeps (D104).
	Repos RepoLimits `yaml:"repos,omitempty"`
	// Pages limits website snapshots (D105).
	Pages PageLimits `yaml:"pages,omitempty"`
}

// RepoLimits bound a repository snapshot: files larger than FileKB and
// binaries are skipped, and the snapshot stops at TotalMB or Files.
type RepoLimits struct {
	FileKB  int `yaml:"fileKB,omitempty"`  // default 512
	TotalMB int `yaml:"totalMB,omitempty"` // default 64
	Files   int `yaml:"files,omitempty"`   // default 20000
	// Pictures kept from a repository's picture paths (D216): files larger than
	// PictureMB are left out, and one fetch keeps at most PicturesMB and
	// Pictures in all.
	PictureMB  int `yaml:"pictureMB,omitempty"`  // default 25
	PicturesMB int `yaml:"picturesMB,omitempty"` // default 250
	Pictures   int `yaml:"pictures,omitempty"`   // default 250
}

// PictureBytes is the largest picture a repository fetch keeps (D216).
func (r RepoLimits) PictureBytes() int64 {
	if r.PictureMB <= 0 {
		return 25 << 20
	}
	return int64(r.PictureMB) << 20
}

// PicturesBytes is the most one repository fetch keeps in pictures (D216).
func (r RepoLimits) PicturesBytes() int64 {
	if r.PicturesMB <= 0 {
		return 250 << 20
	}
	return int64(r.PicturesMB) << 20
}

// MaxPictures is the most pictures one repository fetch keeps (D216).
func (r RepoLimits) MaxPictures() int {
	if r.Pictures <= 0 {
		return 250
	}
	return r.Pictures
}

// PageLimits bound a website snapshot: how many pages a depth-1 snapshot
// takes, how long one page may take, and how tall a full-page picture gets.
type PageLimits struct {
	MaxPages    int `yaml:"maxPages,omitempty"`    // default 5 (depth 1), at most 20
	TimeoutSec  int `yaml:"timeoutSec,omitempty"`  // default 45 per page
	FullHeightK int `yaml:"fullHeightK,omitempty"` // default 16 (thousand pixels)
}

// FileBytes is the largest file a repository snapshot keeps.
func (r RepoLimits) FileBytes() int64 {
	if r.FileKB <= 0 {
		return 512 << 10
	}
	return int64(r.FileKB) << 10
}

// TotalBytes is the most a repository snapshot holds.
func (r RepoLimits) TotalBytes() int64 {
	if r.TotalMB <= 0 {
		return 64 << 20
	}
	return int64(r.TotalMB) << 20
}

// MaxFiles is the most files a repository snapshot holds.
func (r RepoLimits) MaxFiles() int {
	if r.Files <= 0 {
		return 20000
	}
	return r.Files
}

// Max is the page cap of a depth-1 snapshot.
func (p PageLimits) Max() int {
	switch {
	case p.MaxPages <= 0:
		return 5
	case p.MaxPages > 20:
		return 20
	}
	return p.MaxPages
}

// Timeout is how long one page may take.
func (p PageLimits) Timeout() int {
	if p.TimeoutSec <= 0 {
		return 45
	}
	return p.TimeoutSec
}

// FullHeight is the tallest full-page picture, in pixels.
func (p PageLimits) FullHeight() int {
	if p.FullHeightK <= 0 {
		return 16000
	}
	return p.FullHeightK * 1000
}

// FolderPath is one linked folder on this machine.
type FolderPath struct {
	ID   string `yaml:"id" json:"id"`
	Path string `yaml:"path" json:"path"`
	// Off pauses keeping it in step on this machine (the link stays).
	Off bool `yaml:"off,omitempty" json:"off,omitempty"`
}

// SmallMB is the upload-on-add limit (default 25).
func (r Resources) SmallMB() int {
	if r.UploadSmallMB <= 0 {
		return 25
	}
	return r.UploadSmallMB
}

// AutoUpdateTools reports whether the daemon may update yt-dlp by itself.
func (i Imports) AutoUpdateTools() bool { return i.AutoUpdate == nil || *i.AutoUpdate }

// ResolveHome picks the installation home: explicit flag, METARUSH_HOME, then ~/.metarush.
func ResolveHome(flag string) (string, error) {
	h := flag
	if h == "" {
		h = os.Getenv(EnvPrefix + "HOME")
	}
	if h == "" {
		u, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		h = filepath.Join(u, ".metarush")
	}
	return filepath.Abs(expandTilde(h))
}

// Default returns a configuration with sensible defaults rooted at home.
func Default(home string) *Config {
	c := &Config{
		Version:       1,
		Database:      Database{Name: "metarush"},
		Media:         Media{Provider: "filesystem", Filesystem: Filesystem{Root: filepath.Join(home, "media")}},
		Cache:         Cache{Dir: filepath.Join(home, "cache"), MaxGB: 50},
		Scratch:       Scratch{Dir: filepath.Join(home, "scratch"), RetainDays: 7},
		Dashboard:     Dashboard{Host: "127.0.0.1", Port: 4845},
		Worker:        Worker{Enabled: true, Concurrency: 2, HeavyRenders: true, Watch: Watch{StableSeconds: 6}},
		Transcription: Transcription{Provider: "auto"},
		home:          home,
	}
	c.ensureLive()
	return c
}

// Path returns the config file path for a home.
func Path(home string) string { return filepath.Join(home, "config.yaml") }

// Exists reports whether a home has been initialized.
func Exists(home string) bool {
	_, err := os.Stat(Path(home))
	return err == nil
}

// Load reads <home>/config.yaml, applies ${VAR:default} expansion, .env files,
// environment overrides and keychain references.
func Load(home string) (*Config, error) {
	if Exists(home) {
		_ = EnsureHomeIgnoreFile(home) // defense in depth for homes inside repositories (best effort)
	}
	envFile := DotEnvPath(home)
	if _, err := os.Stat(envFile); err == nil {
		// A secrets file inside a sync folder, or one the repository would commit,
		// is never used (see internal/secretpath).
		if err := secretpath.CheckUse(envFile, "the secrets file"); err != nil {
			return nil, err
		}
	}
	loadDotEnv(envFile)
	raw, err := os.ReadFile(Path(home))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("MetaRush isn't set up on this machine yet")
		}
		return nil, err
	}
	cfg := Default(home)
	if err := yaml.Unmarshal([]byte(Expand(string(raw))), cfg); err != nil {
		return nil, fmt.Errorf("MetaRush couldn't read its settings (%s): %w", tildeHome(Path(home)), err)
	}
	cfg.home = home
	cfg.secretMode = inferSecretMode(raw)
	cfg.applyEnv()
	if err := cfg.resolveSecrets(); err != nil {
		return nil, err
	}
	cfg.fillDefaults()
	if t := cfg.Worker.heavyThreshold(); t < 0 || t > 100 {
		return nil, fmt.Errorf("MetaRush couldn't read its settings (%s): the battery level below which big jobs wait (worker.heavyBatteryThreshold) must be 0 to 100, not %d", tildeHome(Path(home)), t)
	}
	if err := cfg.Log.validate(); err != nil {
		return nil, fmt.Errorf("MetaRush couldn't read its settings (%s): %w", tildeHome(Path(home)), err)
	}
	rm, err := RemoteFrom(cfg.Dashboard)
	if err != nil {
		return nil, fmt.Errorf("MetaRush couldn't read its settings (%s): %w", tildeHome(Path(home)), err)
	}
	cfg.live.threshold.Store(int32(cfg.Worker.heavyThreshold()))
	cfg.live.remote.Store(&rm)
	return cfg, nil
}

// LoadTools reads only the tools section of <home>/config.yaml (with
// ${VAR:default} expansion and METARUSH_* overrides), resolving no secrets:
// enough for commands that only run local programs, such as setting up the
// renderer, when the rest of the configuration cannot be loaded.
func LoadTools(home string) (Tools, error) {
	raw, err := os.ReadFile(Path(home))
	if err != nil {
		return Tools{}, err
	}
	c := Default(home)
	var only struct {
		Tools Tools `yaml:"tools"`
	}
	if err := yaml.Unmarshal([]byte(Expand(string(raw))), &only); err != nil {
		return Tools{}, fmt.Errorf("parse %s: %w", Path(home), err)
	}
	c.Tools = only.Tools
	c.applyEnv()
	c.fillDefaults()
	return c.Tools, nil
}

// LoadLocalDatabase reads only database.local from <home>/config.yaml
// (nil when the database is elsewhere), resolving no secrets: enough to
// start, stop or check the database on this computer when the keychain
// can't be read.
func LoadLocalDatabase(home string) (*LocalDatabase, error) {
	raw, err := os.ReadFile(Path(home))
	if err != nil {
		return nil, err
	}
	var only struct {
		Database struct {
			Local *LocalDatabase `yaml:"local"`
		} `yaml:"database"`
	}
	if err := yaml.Unmarshal([]byte(Expand(string(raw))), &only); err != nil {
		return nil, fmt.Errorf("parse %s: %w", Path(home), err)
	}
	return only.Database.Local, nil
}

// SetField changes one value (a scalar, or a whole section) in <home>/config.yaml and nothing else: the file
// is edited as a YAML tree, so comments, ${VAR} references and keychain
// references stay exactly as written (nothing is expanded or resolved). Missing
// parent mappings are created. The write is atomic and keeps 0600.
func SetField(home string, path []string, value any) error {
	if len(path) == 0 {
		return fmt.Errorf("empty config path")
	}
	p := Path(home)
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", p, err)
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s is not a YAML mapping", p)
	}
	var val yaml.Node
	if err := val.Encode(value); err != nil {
		return err
	}
	m := doc.Content[0]
	for i, key := range path {
		last := i == len(path)-1
		var child *yaml.Node
		for j := 0; j+1 < len(m.Content); j += 2 {
			if m.Content[j].Value == key {
				child = m.Content[j+1]
				break
			}
		}
		switch {
		case child == nil && last:
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &val)
		case child == nil:
			child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, child)
			m = child
		case last:
			// A scalar replaces a scalar; a mapping (a whole section such as
			// worker.watch) replaces a mapping. Nothing else in the file changes.
			if child.Kind != val.Kind && !(child.Kind == yaml.ScalarNode && child.Tag == "!!null") {
				return fmt.Errorf("%s: %s has a different shape than the new value", p, strings.Join(path, "."))
			}
			val.HeadComment, val.LineComment, val.FootComment = child.HeadComment, child.LineComment, child.FootComment
			*child = val
		default:
			if child.Kind != yaml.MappingNode {
				return fmt.Errorf("%s: %s is not a mapping", p, strings.Join(path[:i+1], "."))
			}
			m = child
		}
	}
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(4)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	_ = enc.Close()
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(buf.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Save writes the configuration (0600). Callers must not put plaintext secrets in
// cfg; use keychain references or environment variables instead.
func Save(home string, cfg *Config) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	if err := EnsureHomeIgnoreFile(home); err != nil {
		return err
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	header := "# MetaRush installation config. Secrets belong in the keychain (keychain:<name>)\n# or METARUSH_* environment variables — never in this file.\n"
	tmp := Path(home) + ".tmp"
	if err := os.WriteFile(tmp, append([]byte(header), out...), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, Path(home))
}

// Home returns the installation home this config was loaded from.
func (c *Config) Home() string { return c.home }

// SetHome is used by init before the first save.
func (c *Config) SetHome(h string) { c.home = h }

// StateDir holds daemon state, nonces and resumable uploads.
func (c *Config) StateDir() string { return filepath.Join(c.home, "state") }

// LogDir holds daemon logs.
func (c *Config) LogDir() string { return filepath.Join(c.home, "logs") }

// KeychainAccount namespaces keychain entries per home so dev and installed copies never collide.
func (c *Config) KeychainAccount(name string) string { return KeychainAccount(c.home, name) }

// KeychainAccount namespaces a secret name by home directory.
func KeychainAccount(home, name string) string {
	sum := sha256.Sum256([]byte(home))
	return hex.EncodeToString(sum[:4]) + ":" + name
}

func (c *Config) fillDefaults() {
	d := Default(c.home)
	if c.Cache.Dir == "" {
		c.Cache.Dir = d.Cache.Dir
	}
	if c.Cache.MaxGB <= 0 {
		c.Cache.MaxGB = d.Cache.MaxGB
	}
	if c.Scratch.Dir == "" {
		c.Scratch.Dir = d.Scratch.Dir
	}
	if c.Dashboard.Host == "" {
		c.Dashboard.Host = d.Dashboard.Host
	}
	if c.Dashboard.Port == 0 {
		c.Dashboard.Port = d.Dashboard.Port
	}
	if c.Worker.Concurrency <= 0 {
		c.Worker.Concurrency = d.Worker.Concurrency
	}
	if c.Worker.Watch.StableSeconds <= 0 {
		c.Worker.Watch.StableSeconds = d.Worker.Watch.StableSeconds
	}
	if c.Media.Provider == "filesystem" && c.Media.Filesystem.Root == "" {
		c.Media.Filesystem.Root = d.Media.Filesystem.Root
	}
	for _, p := range []*string{&c.Cache.Dir, &c.Scratch.Dir, &c.Media.Filesystem.Root, &c.Worker.Watch.Dir, &c.Tools.VideoDir, &c.Tools.ModelsDir, &c.Tools.SherpaDir, &c.Media.GCS.CredentialsFile, &c.Tools.YtDlp, &c.Tools.Deno, &c.Tools.Brew, &c.Tools.Vision, &c.Tools.NPM} {
		if *p != "" {
			*p = expandTilde(*p)
		}
	}
	if c.Tools.Vision == "" && c.home != "" {
		if st, err := os.Stat(VisionToolPath(c.home)); err == nil && !st.IsDir() {
			c.Tools.Vision = VisionToolPath(c.home)
		}
	}
}

func (c *Config) applyEnv() {
	str := func(name string, dst *string) {
		if v, ok := os.LookupEnv(EnvPrefix + name); ok && v != "" {
			*dst = v
		}
	}
	str("MONGODB_URI", &c.Database.URI)
	str("DATABASE_NAME", &c.Database.Name)
	for _, e := range mediaEnv {
		str(e.name, e.field(&c.Media))
	}
	str("CACHE_DIR", &c.Cache.Dir)
	str("VIDEO_DIR", &c.Tools.VideoDir)
	if v := os.Getenv(EnvPrefix + "DEV_RENDERER"); v != "" {
		c.Tools.DevRenderer = v == "1" || strings.EqualFold(v, "true")
	}
	str("NPM", &c.Tools.NPM)
	str("LAUNCHCTL", &c.Tools.Launchctl)
	str("SYSTEMCTL", &c.Tools.Systemctl)
	str("FFMPEG", &c.Tools.FFmpeg)
	str("FFPROBE", &c.Tools.FFprobe)
	str("NODE", &c.Tools.Node)
	str("CLAUDE", &c.Tools.Claude)
	str("YTDLP", &c.Tools.YtDlp)
	str("DENO", &c.Tools.Deno)
	str("BREW", &c.Tools.Brew)
	str("VISION", &c.Tools.Vision)
	str("GIT", &c.Tools.Git)
	str("GH", &c.Tools.GH)
	str("D2", &c.Tools.D2)
	str("TAILSCALE", &c.Tools.Tailscale)
	str("RCLONE", &c.Tools.Rclone)
	str("WHISPER_CLI", &c.Tools.WhisperCLI)
	str("WHISPER_SERVER", &c.Tools.WhisperServer)
	str("MODELS_DIR", &c.Tools.ModelsDir)
	str("TRANSCRIPTION", &c.Transcription.Provider)
	if v := os.Getenv(EnvPrefix + "DASHBOARD_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			c.Dashboard.Port = p
		}
	}
	if v := os.Getenv(EnvPrefix + "DEV_ORIGINS"); v != "" {
		c.Dashboard.DevOrigins = strings.Split(v, ",")
	}
}

func (c *Config) resolveSecrets() error {
	for _, p := range []*string{&c.Database.URI, &c.Media.S3.AccessKeyID, &c.Media.S3.SecretAccessKey} {
		v, err := ResolveSecret(c.home, *p)
		if err != nil {
			return err
		}
		*p = v
	}
	return nil
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::([^}]*))?\}`)

// Expand replaces ${VAR} and ${VAR:default} with environment values.
func Expand(s string) string {
	return envRef.ReplaceAllStringFunc(s, func(m string) string {
		parts := envRef.FindStringSubmatch(m)
		if v, ok := os.LookupEnv(parts[1]); ok {
			return v
		}
		return parts[2]
	})
}

func expandTilde(p string) string {
	if strings.HasPrefix(p, "~/") {
		if u, err := os.UserHomeDir(); err == nil {
			return filepath.Join(u, p[2:])
		}
	}
	return p
}

// tildeHome shows a path in the person's home folder as ~/… in messages.
func tildeHome(p string) string {
	if u, err := os.UserHomeDir(); err == nil && u != "" && strings.HasPrefix(p, u+string(filepath.Separator)) {
		return "~" + p[len(u):]
	}
	return p
}

// loadDotEnv sets variables from a KEY=VALUE file without overriding the environment.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(strings.TrimPrefix(k, "export "))
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if secretpath.NeverInherited(k) {
			continue // read only where it is needed, never put into the environment
		}
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
			rememberDotEnv(k, v)
		}
	}
}

// The variables this process took from .env files (rather than its own
// environment), with the values they had then.
var (
	dotEnvMu   sync.Mutex
	dotEnvVars = map[string]string{}
)

func rememberDotEnv(k, v string) {
	dotEnvMu.Lock()
	dotEnvVars[k] = v
	dotEnvMu.Unlock()
}

func forgetDotEnvVar(k string) {
	dotEnvMu.Lock()
	delete(dotEnvVars, k)
	dotEnvMu.Unlock()
}

// FromDotEnv reports whether this process's value of a variable came from an
// installation's .env file (MetaRush can change it there), not from the
// environment it was started with.
func FromDotEnv(name string) bool {
	dotEnvMu.Lock()
	defer dotEnvMu.Unlock()
	v, ok := dotEnvVars[name]
	return ok && os.Getenv(name) == v
}

// ForgetDotEnv unsets the variables this process took from .env files (those
// still at the value it read), so the same process started again (exec) reads
// the files afresh: the daemon, when `machine rekey` changed the secret there.
func ForgetDotEnv() {
	dotEnvMu.Lock()
	defer dotEnvMu.Unlock()
	for k, v := range dotEnvVars {
		if os.Getenv(k) == v {
			_ = os.Unsetenv(k)
		}
		delete(dotEnvVars, k)
	}
}

// LoadDotEnv is exported for the dev start script path (repo-root .env).
func LoadDotEnv(path string) { loadDotEnv(path) }
