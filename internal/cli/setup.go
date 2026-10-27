package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/api"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/firstrun"
	"github.com/metavert-io/metarush/internal/localdb"
	"github.com/metavert-io/metarush/internal/service"
	"github.com/metavert-io/metarush/internal/skillsinstall"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// setupOpts are `metarush setup`'s choices: each one is asked in the
// interview and is also a flag, so an agent can run it unattended (D253).
type setupOpts struct {
	init initOpts // the workspace, the database address and the storage (init's own settings)
	// storage: folder (a folder on this computer, the default), s3 or gcs.
	storage string
	// database: here (the database on this computer, the default) or
	// elsewhere (an address: --db-uri or METARUSH_MONGODB_URI).
	database string
	dbPort   int
	// replaceSaved lets an unattended setup replace a database address an
	// earlier setup kept for another database (a copy is kept aside).
	replaceSaved bool
	service      bool
	skills       bool
	open         bool
	// components: exactly these optional components (comma-separated);
	// noComponents: none (D325).
	components   string
	noComponents bool
}

func setupCommand() *cobra.Command {
	o := &setupOpts{}
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Set MetaRush up on this computer in one go: storage, a database, the dashboard, the background service, Claude Code's skills",
		Long: `Set MetaRush up on this computer, step by step, with a suggested answer for every question:

  where your footage and finished videos are kept (a folder on this computer; a cloud bucket as an advanced choice),
  where your projects are kept (a database on this computer that MetaRush downloads from MongoDB and looks after,
  or one you already have, such as MongoDB Atlas), the dashboard's port, MetaRush starting when you log in,
  Claude Code learning MetaRush's video skills, and the optional components to add — FFmpeg, the Remotion renderer,
  transcription on this computer, the link downloader and more, the suggested ones ticked (metarush components lists
  them). It ends by saying what works now with no keys, and what a key adds.

Every answer is also a flag; --yes takes the suggested answer for the rest, and --json prints the result, so an
agent can run it unattended. Running it again is safe: what is set up stays, what is missing is set up, and the
database is started when it isn't running. (metarush init is the older, more technical way.)`,
		RunE: func(c *cobra.Command, _ []string) error { return runSetup(c, o) },
	}
	f := cmd.Flags()
	f.StringVar(&o.storage, "storage", "", "where footage and finished videos are kept: folder (default), s3 or gcs")
	f.StringVar(&o.init.mediaRoot, "media-root", "", "the folder for footage and finished videos (default <home>/media)")
	f.BoolVar(&o.init.mediaShared, "media-shared", false, "the folder is on a shared drive every computer of the workspace reaches")
	f.StringVar(&o.init.s3Preset, "s3-preset", "", "advanced storage: tigris | r2 | b2 | aws | gcs | custom")
	f.StringVar(&o.init.s3Endpoint, "s3-endpoint", "", "advanced storage: the bucket's address")
	f.StringVar(&o.init.s3Region, "s3-region", "", "advanced storage: region")
	f.StringVar(&o.init.s3Bucket, "s3-bucket", "", "advanced storage: bucket")
	f.StringVar(&o.init.s3Prefix, "s3-prefix", "", "advanced storage: key prefix in the bucket")
	f.StringVar(&o.init.s3AccessKeyID, "s3-access-key-id", "", "advanced storage: access key ID (the secret in METARUSH_S3_SECRET_ACCESS_KEY, or asked)")
	f.BoolVar(&o.init.s3PathStyle, "s3-path-style", false, "advanced storage: path-style addressing")
	f.StringVar(&o.init.gcsBucket, "gcs-bucket", "", "advanced storage: Google Cloud Storage bucket")
	f.StringVar(&o.init.gcsPrefix, "gcs-prefix", "", "advanced storage: key prefix in the Google Cloud Storage bucket")
	f.StringVar(&o.init.gcsCredentials, "gcs-credentials", "", "advanced storage: a service-account key file (default: your gcloud sign-in)")
	f.StringVar(&o.database, "database", "", "where projects are kept: here (a database on this computer, the default) or elsewhere (--db-uri or METARUSH_MONGODB_URI)")
	f.StringVar(&o.init.dbURI, "db-uri", "", "a database elsewhere, such as MongoDB Atlas (prefer METARUSH_MONGODB_URI, out of the shell's history)")
	f.StringVar(&o.init.dbName, "db-name", "", "the database's name (default metarush)")
	f.IntVar(&o.dbPort, "database-port", 0, "the database on this computer's port on 127.0.0.1 (default: a free one from 27150)")
	f.BoolVar(&o.replaceSaved, "replace-saved-database", false, "replace a database address an earlier setup kept for another database (the old one is kept aside)")
	f.IntVar(&o.init.port, "port", 0, "the dashboard's port on 127.0.0.1 (default 4845, or the next free one)")
	f.BoolVar(&o.service, "service", true, "start MetaRush when you log in and keep it running (launchd on macOS, systemd on Linux)")
	f.BoolVar(&o.skills, "skills", true, "install MetaRush's video skills for Claude Code (~/.claude/skills; default: when Claude Code is installed)")
	f.BoolVar(&o.open, "open", true, "open the dashboard at the end (in a terminal only)")
	f.StringVar(&o.components, "components", "", "add exactly these optional components, comma-separated (metarush components lists them; default: the suggested ones)")
	f.BoolVar(&o.noComponents, "no-components", false, "add no optional components (add them later in Settings → Components)")
	f.StringVar(&o.init.workspaceName, "workspace-name", "", "the new workspace's name (default My Studio)")
	f.StringVar(&o.init.join, "join", "", "join a workspace already in the database (its ID)")
	f.BoolVar(&o.init.newWorkspace, "new-workspace", false, "make a new workspace even if the database has one")
	f.StringVar(&o.init.userName, "user-name", "", "your name (default from git)")
	f.StringVar(&o.init.userEmail, "user-email", "", "your email (default from git)")
	f.StringVar(&o.init.installName, "installation-name", "", "this computer's name (default its host name)")
	f.StringVar(&o.init.secrets, "secrets", "keychain", "where passwords and keys are kept: keychain | env (<home>/.env)")
	f.BoolVar(&o.init.allowUnsafeSecretPath, "allow-unsafe-secret-path", false, "people only: keep secrets in a home inside a git repository that ignores it (never a sync folder; never the database on this computer)")
	f.BoolVar(&o.init.yes, "yes", false, "take the suggested answer for every question not given as a flag")
	f.BoolVar(&o.init.testDB, "test-db", false, "allow a throwaway metarush-test-* database (browser runs)")
	_ = f.MarkHidden("test-db")
	cmd.AddCommand(setupStatusCmd())
	return cmd
}

// SetupResult is `metarush setup --json`: what was set up, what works now
// (D256) and what to do next.
type SetupResult struct {
	Home string `json:"home"`
	// Fresh: this run set MetaRush up; false when it was set up already.
	Fresh        bool             `json:"fresh"`
	Workspace    config.Ref       `json:"workspace"`
	Installation config.Ref       `json:"installation"`
	User         config.UserRef   `json:"user"`
	Database     SetupDatabaseOut `json:"database"`
	Storage      SetupStorageOut  `json:"storage"`
	Dashboard    string           `json:"dashboard"`
	Service      SetupServiceOut  `json:"service"`
	Skills       SetupSkillsOut   `json:"skills"`
	// Components are the optional components this run added (D325).
	Components SetupComponentsOut `json:"components"`
	// WhatWorks is what works now with no keys, what a key adds, and Claude Code.
	WhatWorks firstrun.WhatWorks `json:"whatWorks"`
	Warnings  []string           `json:"warnings,omitempty"`
	// Next are the commands for what is left (empty when nothing is).
	Next []string `json:"next"`
}

// SetupDatabaseOut is where the projects are kept.
type SetupDatabaseOut struct {
	Local bool `json:"local"`
	// Version, Port and Running are the database on this computer's.
	Version      string `json:"version,omitempty"`
	Port         int    `json:"port,omitempty"`
	Running      bool   `json:"running"`
	UpgradeReady bool   `json:"upgradeReady,omitempty"`
	Host         string `json:"host,omitempty"` // a database elsewhere: its host (never the sign-in)
	Name         string `json:"name"`
	Says         string `json:"says"`
}

// SetupStorageOut is where footage and finished videos are kept.
type SetupStorageOut struct {
	Kind   string `json:"kind" jsonschema:"enum=folder,enum=s3,enum=gcs"`
	Shared bool   `json:"shared"`
	Folder string `json:"folder,omitempty"`
	Says   string `json:"says"`
}

// SetupServiceOut is the background service.
type SetupServiceOut struct {
	Wanted    bool   `json:"wanted"`
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Problem   string `json:"problem,omitempty"`
	Says      string `json:"says"`
}

// SetupSkillsOut is Claude Code's skills.
type SetupSkillsOut struct {
	Wanted    bool   `json:"wanted"`
	Installed bool   `json:"installed"`
	Dir       string `json:"dir,omitempty"`
	Files     int    `json:"files"`
	Says      string `json:"says"`
	// Plugin says that MetaRush's plugin for Claude Code needs this program
	// too (D319), or that Claude Code can't find it yet.
	Plugin string `json:"plugin,omitempty"`
}

func runSetup(c *cobra.Command, o *setupOpts) error {
	h, err := home()
	if err != nil {
		return failErr(c, err)
	}
	prompt := !o.init.yes && interactive() && !jsonMode(c)
	if !prompt && !o.init.yes && !jsonMode(c) && !interactive() {
		o.init.yes = true // nobody to ask: the suggested answers
	}
	s := &setupRun{c: c, o: o, h: h, prompt: prompt}
	if o.init.allowUnsafeSecretPath {
		if err := s.allowUnsafeSecrets(); err != nil {
			return err
		}
	}
	ctx, cancel := signalCtx(c.Context())
	defer cancel()
	res, err := s.run(ctx)
	if err != nil {
		return err
	}
	return emit(c, res, func() { s.printEnd(res) })
}

// setupRun is one run of setup.
type setupRun struct {
	c        *cobra.Command
	o        *setupOpts
	h        string
	prompt   bool
	warnings []string // plain lines that also go into the result
	noClaude bool     // the skills were left out because Claude Code isn't installed
	pick     componentPick
}

func (s *setupRun) say(f string, a ...any) {
	if !jsonMode(s.c) {
		fmt.Printf(f+"\n", a...)
	}
}

func (s *setupRun) tech(f string, a ...any) {
	if !jsonMode(s.c) && detailsMode(s.c) {
		fmt.Printf("    "+f+"\n", a...)
	}
}

func (s *setupRun) changed(flag string) bool {
	f := s.c.Flags().Lookup(flag)
	return f != nil && f.Changed
}

func (s *setupRun) run(ctx context.Context) (*SetupResult, error) {
	if config.Exists(s.h) {
		return s.again(ctx)
	}
	o := s.o
	s.say("Setting up MetaRush on this computer. Press Enter to take the suggested answer.")
	s.tech("home: %s", s.h)
	if err := s.chooseStorage(); err != nil {
		return nil, err
	}
	if err := s.chooseDatabase(); err != nil {
		return nil, err
	}
	if err := s.choosePort(); err != nil {
		return nil, err
	}
	claude := s.skillsDefault(ctx, tools.New(config.Tools{}))
	if s.prompt {
		o.service = askYes("Start MetaRush when you log in, and keep it running?", o.service)
		if claude.Installed && !s.changed("skills") {
			o.skills = askYes("Teach Claude Code MetaRush's video skills?", o.skills)
		}
	}
	if err := s.chooseComponents(componentsEnv(config.Default(s.h), tools.New(config.Tools{})), false); err != nil {
		return nil, err
	}
	s.say("")
	// The database first: the workspace is made in it.
	localPort := 0
	if o.database == "here" {
		port, err := s.startLocalDatabase(ctx)
		if err != nil {
			return nil, err
		}
		localPort = port
	}
	io := o.init
	io.yes, io.quiet, io.localPort = true, true, localPort
	io.replaceSaved = o.replaceSaved
	if s.prompt {
		io.askReplace = func() bool {
			s.say("This MetaRush already keeps the address of another database.")
			return askYes("Replace it with the one you gave? The old one is kept aside.", false)
		}
	}
	if io.dbName == "" {
		io.dbName = "metarush"
	}
	initRes, err := initialize(s.c, &io)
	if err != nil {
		return nil, err
	}
	// With env secrets, init wrote the home's .env (the database address and
	// an S3 secret), so the home works by itself.
	cfg, err := config.Load(s.h)
	if err != nil {
		return nil, notSetUp(s.c, s.h, err)
	}
	s.say("✓ Your workspace “%s” is ready.", cfg.Workspace.Name)
	s.tech("workspace %s, this computer %s (%s)", cfg.Workspace.ID, cfg.Installation.Name, cfg.Installation.ID)
	res := &SetupResult{Home: s.h, Fresh: true, Workspace: cfg.Workspace, Installation: cfg.Installation, User: cfg.User,
		Dashboard: fmt.Sprintf("http://%s:%d", cfg.Dashboard.Host, cfg.Dashboard.Port), Next: []string{}}
	res.Warnings = append(res.Warnings, s.warnings...)
	s.warnings = nil
	if w, ok := initRes["warnings"].([]string); ok {
		res.Warnings = append(res.Warnings, w...)
		for _, x := range w {
			s.say("! %s", x)
		}
	}
	return s.finish(ctx, cfg, res)
}

// again is setup on a home that is set up: nothing that exists is changed;
// what is missing is set up and the database on this computer is started.
func (s *setupRun) again(ctx context.Context) (*SetupResult, error) {
	cfg, err := config.Load(s.h)
	if err != nil {
		return nil, notSetUp(s.c, s.h, err)
	}
	for _, f := range []string{"storage", "media-root", "s3-bucket", "gcs-bucket", "database", "db-uri", "db-name", "workspace-name", "join", "secrets"} {
		if s.changed(f) {
			return nil, failErr(s.c, apperr.New(apperr.Conflict, "MetaRush is set up on this computer already, so setup doesn't change where things are kept.").
				WithHint("Moving footage to other storage is in Settings → Storage; setup keeps everything else as it is.").
				WithDetails(map[string]any{"flag": "--" + f, "home": s.h}).WithCommand("metarush storage migrate --help"))
		}
	}
	s.say("MetaRush is set up on this computer already; checking that everything is in place.")
	s.skillsDefault(ctx, tools.New(cfg.Tools))
	if err := s.chooseComponents(componentsEnv(cfg, tools.New(cfg.Tools)), true); err != nil {
		return nil, err
	}
	s.tech("home: %s", s.h)
	if s.changed("port") && s.o.init.port != cfg.Dashboard.Port {
		if s.o.init.port <= 0 || s.o.init.port > 65535 {
			return nil, fail(s.c, apperr.InvalidInput, "That isn't a port the dashboard can use.", "")
		}
		if err := config.SetField(s.h, []string{"dashboard", "port"}, s.o.init.port); err != nil {
			return nil, failErr(s.c, err)
		}
		cfg.Dashboard.Port = s.o.init.port
		s.say("✓ The dashboard moves to its new port the next time MetaRush starts.")
	}
	if l := cfg.Database.Local; l != nil {
		srv := localdb.New(s.h, l.Port, tools.New(cfg.Tools))
		admin, _ := localAdminURI(cfg)
		if _, err := srv.Prepare(ctx, cfg.Database.Name, localdb.Saved{URI: cfg.Database.URI, AdminURI: admin}, nil, func(string, string) error {
			return errors.New("the database on this computer would need a new sign-in")
		}); err != nil {
			return nil, failErr(s.c, databaseErr(err, "The database on this computer isn't ready"))
		}
	} else {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		db, err := store.Connect(cctx, cfg.Database.URI, cfg.Database.Name)
		cancel()
		if err != nil {
			return nil, failErr(s.c, apperr.Wrap(apperr.Database, err, "MetaRush can't reach its database.").WithHint(checkupHint).WithCommand("metarush doctor"))
		}
		_ = db.Close(context.Background())
	}
	res := &SetupResult{Home: s.h, Workspace: cfg.Workspace, Installation: cfg.Installation, User: cfg.User,
		Dashboard: fmt.Sprintf("http://%s:%d", cfg.Dashboard.Host, cfg.Dashboard.Port), Next: []string{}}
	return s.finish(ctx, cfg, res)
}

// finish is the part every run shares: the database and storage said, the
// service and the skills, what works, and the dashboard.
func (s *setupRun) finish(ctx context.Context, cfg *config.Config, res *SetupResult) (*SetupResult, error) {
	ts := tools.New(cfg.Tools)
	res.Database = setupDatabaseOut(ctx, cfg, ts)
	s.say("✓ %s", res.Database.Says)
	if res.Database.Local {
		s.tech("MongoDB %s on 127.0.0.1:%d, folder %s", res.Database.Version, res.Database.Port, filepath.Join(s.h, "database"))
		if res.Database.UpgradeReady {
			res.Next = append(res.Next, "metarush stop && metarush database upgrade")
		}
	} else {
		s.tech("database %s / %s", res.Database.Host, res.Database.Name)
	}
	res.Storage = setupStorageOut(cfg)
	s.say("✓ %s", res.Storage.Says)
	s.tech("storage: %s %s", res.Storage.Kind, res.Storage.Folder)
	// The optional components before the service: a renderer added here is
	// the one the background service renders with (D325).
	res.Components = s.addComponents(ctx, componentsEnv(cfg, ts))
	for _, f := range append(append([]SetupComponentNote{}, res.Components.Failed...), res.Components.NeedsYou...) {
		if f.Command != "" && !slices.Contains(res.Next, f.Command) {
			res.Next = append(res.Next, f.Command)
		}
	}
	res.Warnings = append(res.Warnings, s.warnings...)
	s.warnings = nil
	res.Skills = s.installSkills(ctx, ts)
	res.Service = s.installService(ctx, cfg, ts)
	if !res.Service.Installed && res.Service.Wanted {
		res.Next = append(res.Next, "metarush service install")
	}
	firstrun.ForgetClaude()
	res.WhatWorks = firstrun.Build(ctx, cfg, ts)
	if !res.WhatWorks.Claude.Ready && res.WhatWorks.Claude.Command != "" {
		res.Next = append(res.Next, res.WhatWorks.Claude.Command)
	}
	for _, w := range res.WhatWorks.NoKeys {
		if !w.Ready && w.Command != "" {
			res.Next = append(res.Next, w.Command)
		}
	}
	res.Next = append(res.Next, "metarush dashboard")
	return res, nil
}

/* ---------- the interview ---------- */

func (s *setupRun) chooseStorage() error {
	o := s.o
	if o.storage == "" {
		switch {
		case o.init.s3Bucket != "" || o.init.s3Preset != "":
			o.storage = "s3"
		case o.init.gcsBucket != "":
			o.storage = "gcs"
		default:
			o.storage = "folder"
		}
		if s.prompt && !s.changed("media-root") {
			s.say("\nWhere should your footage, preview copies and finished videos be kept?")
			s.say("  1  A folder on this computer (suggested)")
			s.say("  2  Advanced: a cloud storage bucket your team shares")
			if ask("Choose 1 or 2", "1") == "2" {
				o.storage = ask("Which kind: s3 (Tigris, Cloudflare R2, Backblaze B2, AWS and others) or gcs (Google Cloud Storage)", "s3")
			}
		}
	}
	switch o.storage {
	case "folder", "filesystem":
		o.init.mediaProvider = "filesystem"
		def := filepath.Join(s.h, "media")
		if o.init.mediaRoot == "" {
			o.init.mediaRoot = def
			if s.prompt {
				o.init.mediaRoot = expandHome(ask("Folder", tildePath(def)))
			}
		}
		if abs, err := filepath.Abs(expandHome(o.init.mediaRoot)); err == nil {
			o.init.mediaRoot = abs
		}
	case "s3":
		o.init.mediaProvider = "s3"
		if s.prompt {
			o.init.s3Preset = ask("Provider (tigris, r2, b2, aws, gcs, custom)", firstNonEmpty(o.init.s3Preset, "tigris"))
			o.init.s3Bucket = ask("Bucket", o.init.s3Bucket)
			o.init.s3AccessKeyID = ask("Access key ID", o.init.s3AccessKeyID)
			if os.Getenv(config.EnvPrefix+"S3_SECRET_ACCESS_KEY") == "" {
				o.init.s3Secret = askSecret("Secret access key")
			}
		}
	case "gcs":
		o.init.mediaProvider = "gcs"
		if s.prompt {
			o.init.gcsBucket = ask("Bucket", o.init.gcsBucket)
			o.init.gcsCredentials = ask("Service-account key file (leave empty to use your gcloud sign-in)", o.init.gcsCredentials)
		}
	default:
		return fail(s.c, apperr.InvalidInput, "Storage must be folder, s3 or gcs.", "")
	}
	return nil
}

func (s *setupRun) chooseDatabase() error {
	o := s.o
	if o.init.dbURI == "" {
		o.init.dbURI = os.Getenv(config.EnvPrefix + "MONGODB_URI")
	}
	srv := localdb.New(s.h, 0, tools.New(config.Tools{}))
	if _, err := localdb.PinFromEnv(); err != nil {
		return failErr(s.c, apperr.Wrap(apperr.InvalidInput, err, "%s.", sentence(err.Error())))
	}
	here := srv.Available()
	if o.database == "" {
		o.database = "here"
		if o.init.dbURI != "" || !here {
			o.database = "elsewhere"
		}
		if s.prompt && !s.changed("db-uri") {
			s.say("\nWhere should MetaRush keep your projects?")
			if here {
				s.say("  1  In a database on this computer (suggested: MetaRush downloads it from MongoDB, about %d MB, free)", srv.DownloadMB())
				s.say("  2  In a database you already have (MongoDB Atlas, or your own server)")
				if ask("Choose 1 or 2", map[bool]string{true: "1", false: "2"}[o.database == "here"]) == "2" {
					o.database = "elsewhere"
				} else {
					o.database = "here"
				}
			} else {
				s.say("  MongoDB doesn't make a database for this kind of computer, so MetaRush needs one you already have")
				s.say("  (MongoDB Atlas has a free tier).")
			}
		}
	}
	switch o.database {
	case "here":
		if os.Getenv(config.EnvPrefix+"MONGODB_URI") != "" && !config.FromDotEnv(config.EnvPrefix+"MONGODB_URI") {
			// MetaRush takes the variable over its settings, so this home would use that database instead.
			return failErr(s.c, apperr.New(apperr.Conflict, "Your terminal names another database for MetaRush, which it would use instead of the database on this computer.").
				WithHint("Remove it from your terminal's settings first, or set MetaRush up with that database instead.").
				WithCommand("unset METARUSH_MONGODB_URI (or: metarush setup --database elsewhere)"))
		}
		if !here {
			return failErr(s.c, databaseErr(localdb.ErrNoBuild, "MetaRush can't set up a database on this computer"))
		}
		if o.init.dbURI != "" && s.changed("db-uri") {
			return fail(s.c, apperr.InvalidInput, "A database on this computer and a database address can't both be chosen.", "Leave out --db-uri, or choose --database elsewhere.")
		}
		o.init.dbURI = ""
		if o.dbPort != 0 && !localdb.ValidPort(o.dbPort) {
			return fail(s.c, apperr.InvalidInput, "That port can't be the database's.", "Choose another one, or leave --database-port out.")
		}
	case "elsewhere":
		if o.init.dbURI == "" && s.prompt {
			s.say("  Its address starts with mongodb:// or mongodb+srv:// (Atlas shows it under Connect).")
			o.init.dbURI = askSecret("Database address")
		}
		if o.init.dbURI == "" {
			return fail(s.c, apperr.InvalidInput, "No database address was given.", "Set METARUSH_MONGODB_URI, or pass --db-uri.")
		}
	default:
		return fail(s.c, apperr.InvalidInput, "The database must be here or elsewhere.", "")
	}
	return nil
}

func (s *setupRun) choosePort() error {
	o := s.o
	if o.init.port == 0 {
		o.init.port = 4845
		for p := 4845; p < 4900; p++ {
			if portOpen(p) {
				o.init.port = p
				break
			}
		}
		if s.prompt {
			v := ask("The dashboard's port on this computer", strconv.Itoa(o.init.port))
			if p, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				o.init.port = p
			}
		}
	}
	if o.init.port <= 1024 || o.init.port > 65535 || o.init.port == localdb.TestPort {
		return fail(s.c, apperr.InvalidInput, "That isn't a port the dashboard can use.", "Choose one above 1024.")
	}
	return nil
}

func portOpen(p int) bool {
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

func tildePath(p string) string {
	if u, err := os.UserHomeDir(); err == nil && u != "" && strings.HasPrefix(p, u+string(filepath.Separator)) {
		return "~" + p[len(u):]
	}
	return p
}

/* ---------- the steps ---------- */

// startLocalDatabase downloads, starts and signs in to the database on this
// computer, keeping its address (with the password) where secrets go before
// the sign-in exists.
func (s *setupRun) startLocalDatabase(ctx context.Context) (int, error) {
	o := s.o
	srv := localdb.New(s.h, o.dbPort, tools.New(config.Tools{}))
	if st, ok := srv.ReadState(); ok {
		srv.Port = st.Port // an earlier try picked it
	}
	if srv.Port == 0 {
		p, err := localdb.PickPort(s.h)
		if err != nil {
			return 0, failErr(s.c, databaseErr(err, "MetaRush can't set up a database on this computer"))
		}
		srv.Port = p
	}
	sec := localSecrets{home: s.h, env: o.init.secrets == "env", allowUnsafe: o.init.allowUnsafeSecretPath}
	saved := sec.saved()
	// An address an earlier setup kept for another database (MongoDB Atlas,
	// say) is never taken for this computer's, and never replaced without a
	// yes (a person's, or --replace-saved-database) and a copy kept aside.
	if saved.URI != "" && !srv.Ours(saved.URI, localdb.User) {
		if s.prompt {
			s.say("A database address an earlier setup kept on this computer is for another database.")
			if !askYes("Replace it with the address of the database on this computer? The old one is kept aside.", false) {
				return 0, fail(s.c, "E_CANCELED", "Nothing was changed.", "To keep using that database, set MetaRush up with it instead.")
			}
		} else if !o.replaceSaved {
			return 0, failErr(s.c, apperr.New(apperr.Conflict, "A database address an earlier setup kept on this computer is for another database, so setup didn't replace it.").
				WithHint("To set up the database on this computer anyway, let setup replace it; the old address is kept aside.").
				WithCommand(commandLine(s.c, "metarush setup", "--replace-saved-database")))
		}
		where, err := sec.keepPrevious(saved.URI)
		if err != nil {
			return 0, failErr(s.c, err)
		}
		line := "The database address an earlier setup kept for another database was replaced; the old one is kept aside " + where + "."
		s.say("! %s", line)
		s.warnings = append(s.warnings, line)
		saved.URI = ""
	}
	if saved.AdminURI != "" && !srv.Ours(saved.AdminURI, localdb.AdminUser) {
		saved.AdminURI = ""
	}
	uri := saved.URI
	keep := func(user, u string) error {
		if user == localdb.User {
			uri = u
		}
		return sec.keep(user, u)
	}
	s.say("Setting up the database on this computer (MetaRush downloads it from MongoDB, about %d MB)…", srv.DownloadMB())
	last := -1
	progress := func(done, total int64) {
		if jsonMode(s.c) || total <= 0 {
			return
		}
		if pct := int(done * 100 / total); pct/10 != last/10 {
			last = pct
			fmt.Printf("  %d%%\n", pct)
		}
	}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if w := srv.PlaceWarning(); w != "" {
		s.say("! %s", w)
		s.warnings = append(s.warnings, w)
	}
	dbName := firstNonEmpty(o.init.dbName, "metarush")
	if _, err := srv.Prepare(dctx, dbName, saved, progress, keep); err != nil {
		return 0, failErr(s.c, setupDatabaseErr(s.c, err))
	}
	o.init.dbURI = uri
	return srv.Port, nil
}

func setupDatabaseOut(ctx context.Context, cfg *config.Config, ts *tools.Set) SetupDatabaseOut {
	d := SetupDatabaseOut{Name: cfg.Database.Name}
	st := firstrun.DatabaseState(ctx, cfg, ts)
	d.Local, d.Running, d.UpgradeReady, d.Says = st.Local, st.Running, st.UpgradeReady, st.Says
	if l := cfg.Database.Local; l != nil {
		ls := localdb.New(cfg.Home(), l.Port, ts).Status(ctx)
		d.Version, d.Port = ls.Version, ls.Port
	} else {
		d.Host = config.HostOf(cfg.Database.URI)
	}
	return d
}

func setupStorageOut(cfg *config.Config) SetupStorageOut {
	switch cfg.Media.Provider {
	case "s3":
		return SetupStorageOut{Kind: "s3", Shared: true, Says: "Footage and finished videos are kept in your cloud storage bucket, which your other computers can share."}
	case "gcs":
		return SetupStorageOut{Kind: "gcs", Shared: true, Says: "Footage and finished videos are kept in your Google Cloud Storage bucket, which your other computers can share."}
	}
	out := SetupStorageOut{Kind: "folder", Shared: cfg.Media.Filesystem.Shared, Folder: cfg.Media.Filesystem.Root,
		Says: "Footage and finished videos are kept in a folder on this computer."}
	if out.Shared {
		out.Says = "Footage and finished videos are kept in a folder on a shared drive."
	}
	return out
}

// claudeSkillsDir is Claude Code's personal skills folder (CLAUDE_CONFIG_DIR
// moves Claude Code's whole folder).
func claudeSkillsDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "skills")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".claude", "skills")
}

// skillsDefault decides whether Claude Code's skills are installed unless
// --skills said so (D253): only where Claude Code is installed.
func (s *setupRun) skillsDefault(ctx context.Context, ts *tools.Set) firstrun.ClaudeCode {
	claude := firstrun.CheckClaude(ctx, ts)
	if !s.changed("skills") {
		s.o.skills = claude.Installed
		s.noClaude = !claude.Installed
	}
	return claude
}

func (s *setupRun) installSkills(ctx context.Context, ts *tools.Set) SetupSkillsOut {
	out := SetupSkillsOut{Wanted: s.o.skills}
	if !s.o.skills {
		out.Says = "Claude Code's video skills weren't installed (you chose not to)."
		if s.noClaude {
			out.Says = "Claude Code isn't installed, so its video skills weren't added. Run setup again once it is."
		}
		s.say("· %s", out.Says)
		if !s.noClaude {
			out.Plugin = pluginNote(false)
			s.say("  %s", out.Plugin)
		}
		return out
	}
	out.Dir = claudeSkillsDir()
	r, err := skillsinstall.Install(skillsinstall.Options{Dir: out.Dir})
	if err != nil {
		out.Says = "Claude Code's video skills couldn't be installed: " + strings.TrimSuffix(err.Error(), ".") + "."
		s.say("! %s", out.Says)
		return out
	}
	out.Installed, out.Files = true, len(r.Installed)+len(r.Updated)+len(r.Unchanged)
	out.Says = "Claude Code knows MetaRush's video skills."
	if len(r.Skipped) > 0 {
		out.Says = "Claude Code knows MetaRush's video skills (your own changes to some of them were kept)."
	}
	s.say("✓ %s", out.Says)
	out.Plugin = pluginNote(true)
	s.say("  %s", out.Plugin)
	s.tech("skills: %s (%d files)", out.Dir, out.Files)
	return out
}

// pluginNote is setup's word on MetaRush's plugin for Claude Code (D319): the
// plugin holds instructions, not MetaRush, so it needs this program on the
// PATH like the skills do.
func pluginNote(skillsHere bool) string {
	if _, err := exec.LookPath("metarush"); err != nil {
		return "Claude Code's skills and MetaRush's plugin for Claude Code call the metarush program by name, and your terminal can't find it yet: add its folder to your PATH."
	}
	if skillsHere {
		return "MetaRush's plugin for Claude Code brings these same skills (use one or the other) and also needs this metarush program."
	}
	return "MetaRush's plugin for Claude Code needs this metarush program too, so keep it installed."
}

func (s *setupRun) installService(ctx context.Context, cfg *config.Config, ts *tools.Set) SetupServiceOut {
	out := SetupServiceOut{Wanted: s.o.service}
	exe, err := service.StableExecutable()
	if err != nil {
		out.Problem = err.Error()
	}
	o := service.Options{Home: s.h, Exe: exe, Tools: ts}
	st := o.Get(ctx)
	if !s.o.service {
		out.Installed, out.Running = st.Installed, st.Running
		out.Says = "MetaRush doesn't start by itself when you log in; opening the dashboard starts it."
		if st.Installed {
			out.Says = "MetaRush already starts when you log in (the background service stays as it was)."
		}
		s.say("· %s", out.Says)
		return out
	}
	if st.Installed && st.Running && st.SameBinary {
		out.Installed, out.Running = true, true
		out.Says = "MetaRush starts when you log in, and runs now."
		s.say("✓ %s", out.Says)
		return out
	}
	if err == nil {
		_, err = o.Install(ctx)
	}
	if err != nil {
		out.Problem = err.Error()
		out.Says = "MetaRush couldn't be set to start when you log in; the dashboard still starts it when you open it."
		s.say("! %s", out.Says)
		s.tech("%v", err)
		return out
	}
	out.Installed = true
	out.Running = waitService(ctx, o, cfg, 40*time.Second, 0)
	out.Says = "MetaRush starts when you log in, and runs now."
	if !out.Running {
		out.Says = "MetaRush is set to start when you log in, but it hasn't started yet; its log says why."
		s.say("! %s", out.Says)
	} else {
		s.say("✓ %s", out.Says)
	}
	s.tech("service %s", o.Label())
	return out
}

/* ---------- the end ---------- */

func (s *setupRun) printEnd(res *SetupResult) {
	w := res.WhatWorks
	fmt.Println("\nWhat works now, free and on this computer:")
	for _, x := range w.NoKeys {
		mark := "✓"
		if !x.Ready {
			mark = "·"
		}
		fmt.Printf("  %s %s — %s\n", mark, x.Name, x.Says)
		if !x.Ready && x.Next != "" {
			fmt.Printf("      → %s\n", x.Next)
		}
		if detailsMode(s.c) && !x.Ready && x.Command != "" {
			fmt.Printf("      %s\n", x.Command)
		}
	}
	if len(w.WithKeys) > 0 {
		fmt.Println("\nWhat a key adds (Settings → Integrations; each is priced before it runs):")
		for _, x := range w.WithKeys {
			mark := "·"
			if x.Ready {
				mark = "✓"
			}
			fmt.Printf("  %s %s — %s\n", mark, x.Name, x.Says)
		}
	}
	fmt.Printf("\n%s\n", w.Claude.Says)
	if !w.Claude.Ready && w.Claude.Next != "" {
		fmt.Printf("  → %s\n", w.Claude.Next)
	}
	if detailsMode(s.c) && w.Claude.Command != "" && !w.Claude.Ready {
		fmt.Printf("  %s\n", w.Claude.Command)
	}
	if s.prompt && s.o.open {
		if u, err := s.openDashboard(); err == nil {
			fmt.Println("\nOpening the dashboard in your browser.")
			if detailsMode(s.c) {
				fmt.Println("  " + u)
			}
			return
		}
	}
	fmt.Println("\nOpen the dashboard whenever you like: it shows what works and what to try first.")
	if detailsMode(s.c) {
		fmt.Println("  metarush dashboard")
	} else {
		fmt.Println("(Add --details to see folders, ports and the commands behind each step.)")
	}
}

// openDashboard opens the dashboard's first run in the browser, starting
// MetaRush first when it isn't running.
func (s *setupRun) openDashboard() (string, error) {
	cfg, err := config.Load(s.h)
	if err != nil {
		return "", err
	}
	if !healthy(cfg) {
		if err := spawnDaemon(s.h, cfg); err != nil {
			return "", err
		}
		deadline := time.Now().Add(40 * time.Second)
		for !healthy(cfg) {
			if time.Now().After(deadline) {
				return "", errors.New("MetaRush isn't answering yet")
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	nonce, err := api.MintLaunchNonce(cfg.StateDir())
	if err != nil {
		return "", err
	}
	u := fmt.Sprintf("http://%s:%d/auth?t=%s&next=%s", cfg.Dashboard.Host, cfg.Dashboard.Port, nonce, "/%3Fwelcome%3D1")
	openBrowser(u)
	return u, nil
}

func setupStatusCmd() *cobra.Command {
	var refresh bool
	c := &cobra.Command{Use: "status", Short: "Say what works on this computer: free with no keys, what a key adds, Claude Code and the database",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "setup.status", firstrun.SetupStatusIn{Refresh: refresh}, func(out any) {
				st := out.(*firstrun.SetupStatusOut)
				fmt.Println(st.Summary)
				for _, x := range st.NoKeys {
					mark := "✓"
					if !x.Ready {
						mark = "·"
					}
					fmt.Printf("  %s %s — %s\n", mark, x.Name, x.Says)
					if !x.Ready && x.Next != "" {
						fmt.Printf("      → %s\n", x.Next)
					}
				}
				if len(st.WithKeys) > 0 {
					fmt.Println("What a key adds:")
					for _, x := range st.WithKeys {
						mark := "·"
						if x.Ready {
							mark = "✓"
						}
						fmt.Printf("  %s %s — %s\n", mark, x.Name, x.Says)
					}
				}
				fmt.Println(st.Claude.Says)
				if !st.Claude.Ready && st.Claude.Next != "" {
					fmt.Println("  → " + st.Claude.Next)
				}
				fmt.Println(st.Database.Says)
			})
		}}
	c.Flags().BoolVar(&refresh, "refresh", false, "ask Claude Code again instead of using the answer from the last minute")
	return c
}

// setupDatabaseErr is a failed setup of the database on this computer: what
// went wrong, and that running setup again carries on where it stopped (the
// server isn't left running half set up).
func setupDatabaseErr(c *cobra.Command, err error) error {
	ae := apperr.From(databaseErr(err, "The database on this computer couldn't be set up"))
	if ae.Code == apperr.Database {
		ae.WithHint("Run setup again: it carries on where it stopped.").WithCommand(commandLine(c, "metarush setup"))
	}
	return ae
}

// allowUnsafeSecrets: only a person may let secrets sit inside a git
// repository, and only after hearing what can go wrong.
func (s *setupRun) allowUnsafeSecrets() error {
	if !interactive() || jsonMode(s.c) {
		return failErr(s.c, apperr.New(apperr.HumanOnly, "Only a person at this computer can let MetaRush keep its passwords inside a repository.").
			WithHint("Set it up in a folder outside repositories (the suggested one in your home folder), or run setup in your own terminal."))
	}
	fmt.Println("Keeping MetaRush's passwords inside a git repository is risky, even where git ignores them: cleaning the")
	fmt.Println("repository's ignored files (git clean -fdX) deletes them, and a change to .gitignore could commit them.")
	fmt.Println("The database on this computer is never kept there.")
	if !askYes("Keep the passwords there anyway?", false) {
		return fail(s.c, "E_CANCELED", "Nothing was changed.", "")
	}
	return nil
}
