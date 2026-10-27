package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"gopkg.in/yaml.v3"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/version"
	"github.com/metavert-io/metarush/internal/wsarchive"
)

type initOpts struct {
	configFile                               string
	workspaceName, join                      string
	newWorkspace                             bool
	dbURI, dbName                            string
	atlasOrgID, atlasOrgName, atlasProjectID string
	mediaProvider, mediaRoot                 string
	mediaShared                              bool
	s3Preset, s3Endpoint, s3Region, s3Bucket string
	s3Prefix, s3AccessKeyID                  string
	s3PathStyle                              bool
	gcsBucket, gcsPrefix, gcsCredentials     string
	cacheDir                                 string
	cacheMaxGB                               float64
	port                                     int
	userName, userEmail, installName         string
	watchDir, videoDir                       string
	secrets                                  string
	allowUnsafeSecretPath, keepBundle        bool
	reconfigure, yes, testDB                 bool
	installID                                string // pre-assigned by `machine invite` (join)
	s3Secret                                 string // set in-process by `machine join`, never a flag
	// localPort is set by `metarush setup` when the database is the one on
	// this computer (D254): config.yaml's database.local.
	localPort int
	// quiet: setup speaks for itself; initialize prints nothing.
	quiet bool
	// replaceSaved lets an unattended run replace a database address this
	// home's secrets already hold for another database (kept aside first);
	// askReplace is setup's own question (its interview).
	replaceSaved bool
	askReplace   func() bool
	// restore is an archive of a workspace (metarush workspace export) that
	// setup restores into the new, empty database before joining it (D333);
	// restored is what the restore did.
	restore  string
	restored *wsarchive.RestoreReport
}

func initCmd() *cobra.Command {
	o := &initOpts{}
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up MetaRush on this machine: its workspace, database and storage",
		Long: `Set up (or join) a MetaRush workspace on this machine.

MetaRush keeps your projects and cuts in a database and your footage in its storage
(a folder, or a cloud bucket a team shares); you choose each one. It asks as it goes;
pass --yes with flags (or --config file.yaml) to set it up without questions.
Passwords and keys are read from METARUSH_MONGODB_URI and METARUSH_S3_SECRET_ACCESS_KEY
(or asked for) and kept in the keychain, never in the settings file. Google Cloud
Storage (--media gcs) keeps only where its key file is, or uses your gcloud sign-in.`,
		RunE: func(c *cobra.Command, _ []string) error { return runInit(c, o) },
	}
	f := cmd.Flags()
	f.StringVar(&o.configFile, "config", "", "YAML file with init settings (same keys as config.yaml; secrets via env)")
	f.StringVar(&o.workspaceName, "workspace-name", "", "name for a new workspace")
	f.StringVar(&o.join, "join", "", "join an existing workspace by ID")
	f.BoolVar(&o.newWorkspace, "new-workspace", false, "create a new workspace even if the database already has one")
	f.StringVar(&o.dbURI, "db-uri", "", "MongoDB URI (prefer METARUSH_MONGODB_URI so it stays out of shell history)")
	f.StringVar(&o.dbName, "db-name", "", "database name (default metarush)")
	f.StringVar(&o.atlasOrgID, "atlas-org-id", "", "verified Atlas organization ID (recorded for auditing)")
	f.StringVar(&o.atlasOrgName, "atlas-org-name", "", "verified Atlas organization name")
	f.StringVar(&o.atlasProjectID, "atlas-project-id", "", "verified Atlas project ID")
	f.StringVar(&o.mediaProvider, "media", "", "media store: filesystem | s3 | gcs")
	f.StringVar(&o.mediaRoot, "media-root", "", "filesystem store root directory")
	f.BoolVar(&o.mediaShared, "media-shared", false, "the filesystem root is a shared mount every collaborator reaches (NAS)")
	f.StringVar(&o.s3Preset, "s3-preset", "", "tigris | r2 | b2 | aws | gcs | custom")
	f.StringVar(&o.s3Endpoint, "s3-endpoint", "", "S3-compatible endpoint URL")
	f.StringVar(&o.s3Region, "s3-region", "", "region (auto for Tigris/R2)")
	f.StringVar(&o.s3Bucket, "s3-bucket", "", "bucket name")
	f.StringVar(&o.s3Prefix, "s3-prefix", "", "key prefix inside the bucket")
	f.StringVar(&o.s3AccessKeyID, "s3-access-key-id", "", "access key ID (secret via METARUSH_S3_SECRET_ACCESS_KEY or prompt)")
	f.BoolVar(&o.s3PathStyle, "s3-path-style", false, "force path-style addressing")
	f.StringVar(&o.gcsBucket, "gcs-bucket", "", "Google Cloud Storage bucket (native driver)")
	f.StringVar(&o.gcsPrefix, "gcs-prefix", "", "key prefix inside the GCS bucket")
	f.StringVar(&o.gcsCredentials, "gcs-credentials", "", "path to a service-account JSON key file (default: Application Default Credentials); the path is stored, never the contents")
	f.StringVar(&o.cacheDir, "cache-dir", "", "local media cache directory")
	f.Float64Var(&o.cacheMaxGB, "cache-max-gb", 0, "cache size limit in GB")
	f.IntVar(&o.port, "port", 0, "dashboard port (default 4845)")
	f.StringVar(&o.userName, "user-name", "", "your name (default from git config)")
	f.StringVar(&o.userEmail, "user-email", "", "your email (default from git config)")
	f.StringVar(&o.installName, "installation-name", "", "name for this machine (default hostname)")
	f.StringVar(&o.watchDir, "watch-dir", "", "enable a watched inbox folder")
	f.StringVar(&o.videoDir, "video-dir", "", "developer setting: render from this source checkout's video/ package instead of this version's own renderer")
	f.StringVar(&o.secrets, "secrets", "keychain", "where secrets live: keychain | env")
	f.BoolVar(&o.allowUnsafeSecretPath, "allow-unsafe-secret-path", false, "with --secrets env: allow <home>/.env inside a repository that ignores it (never inside a sync folder)")
	f.BoolVar(&o.reconfigure, "reconfigure", false, "overwrite an existing config (shared data is untouched)")
	f.BoolVar(&o.replaceSaved, "replace-saved-database", false, "replace a database address this home already keeps for another database (the old one is kept aside)")
	f.BoolVar(&o.yes, "yes", false, "non-interactive: accept defaults, never prompt")
	f.BoolVar(&o.testDB, "test-db", false, "allow a throwaway metarush-test-* database on localhost (acceptance scripts)")
	_ = f.MarkHidden("test-db")
	f.StringVar(&o.installID, "installation-id", "", "use an installation ID pre-assigned by `machine invite` (prefer `machine join`)")
	_ = f.MarkHidden("installation-id")
	return cmd
}

func gitConfig(key string) string {
	out, err := exec.Command("git", "config", "--global", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func runInit(c *cobra.Command, o *initOpts) error {
	result, err := initialize(c, o)
	if err != nil {
		return err
	}
	return emit(c, result, func() {
		fmt.Println("\nDone: MetaRush is set up on this machine. Next:")
		fmt.Println("  metarush doctor                 # check that everything is ready")
		fmt.Println("  metarush skills install --user  # let Claude Code use the video skills")
		fmt.Println("  metarush dashboard              # open the dashboard")
	})
}

// notSetUp is the error for a home whose settings config.Load refused:
// MetaRush isn't set up there yet, or its settings can't be read. The reason
// (with the folder) and the command stay behind --details.
func notSetUp(c *cobra.Command, h string, err error) error {
	msg, hint := "MetaRush isn't set up on this machine yet.", "Set it up first."
	if config.Exists(h) {
		msg, hint = "MetaRush's settings on this machine couldn't be read.", "Check them, or set MetaRush up again."
	}
	return failErr(c, apperr.Wrap(apperr.NotConfigured, err, "%s", msg).WithHint(hint).WithCommand("metarush init"))
}

// initialize sets up the installation and returns a summary (no secrets).
func initialize(c *cobra.Command, o *initOpts) (map[string]any, error) {
	h, err := home()
	if err != nil {
		return nil, err
	}
	if config.Exists(h) && !o.reconfigure {
		return nil, failErr(c, apperr.New(apperr.Conflict, "MetaRush is already set up here.").
			WithHint("Add --reconfigure to change its settings, or --home to set up a separate one.").WithDetails(map[string]any{"home": h}))
	}
	if o.configFile != "" {
		b, err := os.ReadFile(o.configFile)
		if err != nil {
			return nil, err
		}
		var fc config.Config
		if err := yaml.Unmarshal([]byte(config.Expand(string(b))), &fc); err != nil {
			return nil, fail(c, "E_INVALID_INPUT", "parse --config: "+err.Error(), "")
		}
		mergeFileConfig(o, &fc)
	}
	prompt := !o.yes && interactive() && !jsonMode(c)
	cfg := config.Default(h)
	if config.Exists(h) {
		if old, err := config.Load(h); err == nil {
			cfg = old
		}
	}
	oldURI := cfg.Database.URI
	say := func(f string, a ...any) {
		if !jsonMode(c) && !o.quiet {
			fmt.Printf(f+"\n", a...)
		}
	}
	// tech prints a technical line only with --details (addresses, folders, IDs).
	tech := func(f string, a ...any) {
		if !jsonMode(c) && detailsMode(c) && !o.quiet {
			fmt.Printf(f+"\n", a...)
		}
	}
	say("Setting up MetaRush on this machine.")
	tech("  home: %s", h)
	found, err := checkHomeLocation(h, o)
	if err != nil {
		return nil, failErr(c, err)
	}
	var homeWarnings []string
	for _, w := range found {
		homeWarnings = append(homeWarnings, w.Detail)
		say("Warning: %s", w.Say)
		tech("  %s", w.Detail)
	}
	// User and machine identity.
	if o.userName == "" {
		o.userName = firstNonEmpty(cfg.User.Name, gitConfig("user.name"), app.Username())
	}
	if o.userEmail == "" {
		o.userEmail = firstNonEmpty(cfg.User.Email, gitConfig("user.email"))
	}
	if o.installName == "" {
		o.installName = firstNonEmpty(cfg.Installation.Name, app.Hostname())
	}
	if prompt {
		o.userName = ask("Your name", o.userName)
		o.userEmail = ask("Your email", o.userEmail)
		o.installName = ask("Name for this machine", o.installName)
	}
	// Database.
	if o.dbURI == "" {
		o.dbURI = os.Getenv(config.EnvPrefix + "MONGODB_URI")
	}
	if o.dbURI == "" && cfg.Database.URI != "" {
		o.dbURI = cfg.Database.URI
	}
	if o.dbURI == "" && prompt {
		say("\nDatabase: MetaRush keeps your projects and cuts in MongoDB (MongoDB Atlas, or any MongoDB server you can reach).")
		tech("  Tip: a local dev server can be started with scripts/dev-mongo.sh (mongodb://127.0.0.1:27018/?replicaSet=rs0&directConnection=true)")
		o.dbURI = askSecret("MongoDB connection string")
	}
	if o.dbURI == "" {
		return nil, fail(c, "E_INVALID_INPUT", "No database address was given.", "Set METARUSH_MONGODB_URI, or pass --db-uri.")
	}
	if o.dbName == "" {
		o.dbName = firstNonEmpty(os.Getenv(config.EnvPrefix+"DATABASE_NAME"), cfg.Database.Name, "metarush")
	}
	if prompt {
		o.dbName = ask("Database name", o.dbName)
	}
	if strings.HasPrefix(o.dbName, store.TestDBPrefix) && !(o.testDB && config.IsLocalURI(o.dbURI)) {
		return nil, fail(c, "E_INVALID_INPUT", "database names starting with "+store.TestDBPrefix+" are reserved for the test suite", "")
	}
	ctx, cancel := context.WithTimeout(c.Context(), 30*time.Second)
	defer cancel()
	say("\nConnecting to the database…")
	tech("  %s", config.RedactURI(o.dbURI))
	db, err := store.Connect(ctx, o.dbURI, o.dbName)
	if err != nil {
		return nil, failErr(c, apperr.Wrap(apperr.Database, err, "MetaRush couldn't connect to the database.").
			WithHint("Check the address and the password, and on MongoDB Atlas the network access list."))
	}
	defer db.Close(context.Background())
	ver, rs, _ := db.ServerInfo(ctx)
	say("  Connected.")
	tech("  MongoDB %s, replica set: %v", ver, rs)
	if !rs {
		say("  Note: live updates in the dashboard will be slower with this database (it isn't a replica set).")
	}
	host := config.HostOf(o.dbURI)
	isAtlas := strings.Contains(host, ".mongodb.net")
	if isAtlas && o.atlasOrgID == "" {
		say("  Note: the address alone doesn't show which MongoDB Atlas organization owns this database; worth checking for a team.")
		tech("  Verify with `atlas organizations list` / `atlas projects list` and pass --atlas-org-id/--atlas-project-id to record it.")
	}
	if !config.IsLocalURI(o.dbURI) && !strings.HasPrefix(o.dbURI, "mongodb+srv://") && !strings.Contains(o.dbURI, "tls=true") && !strings.Contains(o.dbURI, "ssl=true") {
		say("  Warning: the connection to this database isn't encrypted.")
		tech("  remote MongoDB without TLS — use mongodb+srv:// or tls=true")
	}
	// The database update gets its own time: the list of steps keeps growing,
	// and a first setup against a far-away database (Atlas) or a busy one can
	// take well over the half minute that connecting is allowed.
	if o.restore != "" {
		// A restore writes only into an empty database, and brings the copy
		// up to date itself once its records are in.
		if err := wsarchive.CheckEmpty(ctx, db); err != nil {
			return nil, failErr(c, err)
		}
	} else {
		say("  Updating the database…")
		mctx, mcancel := context.WithTimeout(c.Context(), 10*time.Minute)
		defer mcancel()
		applied, err := db.Migrate(mctx)
		if err != nil {
			return nil, failErr(c, apperr.Wrap(apperr.Database, err, "Updating the database failed.").
				WithHint("Run the setup again: it picks up where the update stopped."))
		}
		if len(applied) > 0 {
			say("  Updated the database (%s).", plural(len(applied), "step"))
			tech("  applied migrations: %s", strings.Join(applied, ", "))
		}
	}
	// The rest of the setup (the storage test, the workspace) gets a fresh
	// deadline: a long database update must not use up the time they have.
	ctx, cancel = context.WithTimeout(c.Context(), time.Minute)
	defer cancel()
	// Media store.
	if o.mediaProvider == "" {
		o.mediaProvider = firstNonEmpty(cfg.Media.Provider, "filesystem")
	}
	if prompt {
		say("\nStorage: where your footage, preview copies and renders are kept (apart from the database).")
		say("  filesystem — a folder on this machine (just you) or on a shared drive")
		say("  s3         — a cloud bucket a team shares: Tigris (Fly.io), Cloudflare R2, Backblaze B2, AWS S3, Google Cloud Storage (HMAC keys)")
		say("  gcs        — Google Cloud Storage with a service-account key file or your gcloud sign-in")
		o.mediaProvider = ask("Storage (filesystem, s3 or gcs)", o.mediaProvider)
	}
	switch o.mediaProvider {
	case "filesystem":
		cfg.Media.Provider = "filesystem"
		root := firstNonEmpty(o.mediaRoot, cfg.Media.Filesystem.Root, filepath.Join(h, "media"))
		if prompt {
			root = ask("Folder for the media", root)
			o.mediaShared = askYes("Is it on a shared drive everyone you work with can reach?", o.mediaShared)
		}
		cfg.Media.Filesystem = config.Filesystem{Root: root, Shared: o.mediaShared}
		cfg.Media.S3 = config.S3{}
		cfg.Media.GCS = config.GCS{}
	case "s3":
		cfg.Media.Provider = "s3"
		s := cfg.Media.S3
		if o.s3Preset != "" {
			s.Preset = o.s3Preset
		}
		if prompt {
			s.Preset = ask("Provider preset (tigris, r2, b2, aws, gcs, custom)", firstNonEmpty(s.Preset, "tigris"))
		}
		p := media.S3Presets[s.Preset]
		s.Endpoint = firstNonEmpty(o.s3Endpoint, s.Endpoint, p.Endpoint)
		s.Region = firstNonEmpty(o.s3Region, s.Region, p.Region)
		s.Bucket = firstNonEmpty(o.s3Bucket, s.Bucket)
		s.Prefix = firstNonEmpty(o.s3Prefix, s.Prefix)
		s.AccessKeyID = firstNonEmpty(o.s3AccessKeyID, os.Getenv(config.EnvPrefix+"S3_ACCESS_KEY_ID"), s.AccessKeyID)
		s.ForcePathStyle = o.s3PathStyle || s.ForcePathStyle || p.PathStyle
		if prompt {
			if p.Note != "" {
				say("  %s", p.Note)
			}
			s.Endpoint = ask("Endpoint", s.Endpoint)
			s.Region = ask("Region", s.Region)
			s.Bucket = ask("Bucket", s.Bucket)
			s.Prefix = ask("Key prefix (optional)", s.Prefix)
			if !config.IsSecretRef(s.AccessKeyID) {
				s.AccessKeyID = ask("Access key ID", s.AccessKeyID)
			}
		}
		secret := firstNonEmpty(o.s3Secret, os.Getenv(config.EnvPrefix+"S3_SECRET_ACCESS_KEY"))
		if secret == "" && config.IsSecretRef(s.SecretAccessKey) {
			secret, _ = config.ResolveSecret(h, s.SecretAccessKey)
		}
		if secret == "" && prompt {
			secret = askSecret("Secret access key")
		}
		if s.Bucket == "" || s.AccessKeyID == "" || secret == "" {
			return nil, fail(c, "E_INVALID_INPUT", "s3 needs a bucket, access key ID and secret", "set METARUSH_S3_SECRET_ACCESS_KEY and pass --s3-bucket/--s3-access-key-id")
		}
		s.SecretAccessKey = secret
		cfg.Media.S3 = s
		cfg.Media.GCS = config.GCS{}
	case "gcs":
		cfg.Media.Provider = "gcs"
		g := cfg.Media.GCS
		g.Bucket = firstNonEmpty(o.gcsBucket, os.Getenv(config.EnvPrefix+"GCS_BUCKET"), g.Bucket)
		g.Prefix = firstNonEmpty(o.gcsPrefix, g.Prefix)
		g.CredentialsFile = firstNonEmpty(o.gcsCredentials, os.Getenv(config.EnvPrefix+"GCS_CREDENTIALS"), g.CredentialsFile)
		if prompt {
			say("  Native Google Cloud Storage. Authenticate with a service-account JSON key file, or leave the path blank to use")
			say("  Application Default Credentials (an attached service account on GCP, GOOGLE_APPLICATION_CREDENTIALS, or")
			say("  `gcloud auth application-default login`). The service account needs roles/storage.objectUser on the bucket.")
			g.Bucket = ask("Bucket", g.Bucket)
			g.Prefix = ask("Key prefix (optional)", g.Prefix)
			g.CredentialsFile = ask("Service-account key file (blank = Application Default Credentials)", g.CredentialsFile)
		}
		if g.Bucket == "" {
			return nil, fail(c, "E_INVALID_INPUT", "gcs needs a bucket", "pass --gcs-bucket")
		}
		if g.CredentialsFile != "" {
			abs, err := filepath.Abs(expandHome(g.CredentialsFile))
			if err != nil {
				return nil, err
			}
			st, err := os.Stat(abs)
			if err != nil {
				return nil, fail(c, "E_INVALID_INPUT", "The Google Cloud key file couldn't be read ("+err.Error()+").", "Give the path of a service-account key file, or leave it out to use your gcloud sign-in.")
			}
			if st.Mode().Perm()&0o077 != 0 {
				say("  Warning: other people on this computer can read the key file; make it readable only by you.")
				tech("  %s is readable by other users (%v); chmod 600 it", abs, st.Mode().Perm())
			}
			g.CredentialsFile = abs
		}
		cfg.Media.GCS = g
		cfg.Media.S3 = config.S3{}
		cfg.Media.Filesystem = config.Filesystem{}
	default:
		return nil, fail(c, "E_INVALID_INPUT", "unknown media store "+o.mediaProvider, "use filesystem, s3 or gcs")
	}
	// Test the store with a real write/read/delete round trip.
	probeCfg := *cfg
	ms, err := app.NewMediaStore(&probeCfg)
	if err != nil {
		return nil, failErr(c, apperr.Wrap(apperr.Storage, err, "MetaRush can't use that storage.").WithHint("Check the storage settings."))
	}
	if err := storeRoundTrip(ctx, ms); err != nil {
		return nil, failErr(c, apperr.Wrap(apperr.Storage, err, "The storage test failed: a test file couldn't be saved and read back.").
			WithHint("Check the bucket name, the address and what the key may do (Google Cloud: the service account needs roles/storage.objectUser on the bucket)."))
	}
	say("  Storage works.")
	tech("  media store %s (shared: %v)", ms.Locator(), ms.Capabilities().Shared)
	if o.restore != "" {
		say("\nRestoring the workspace from %s…", filepath.Base(o.restore))
		rep, err := wsarchive.Restore(c.Context(), wsarchive.RestoreOptions{Archive: o.restore, DB: db, Store: ms, StoreWords: ops.StorageWords(cfg.Media),
			StateDir: filepath.Join(h, "state", "restore"), ScratchDir: filepath.Join(cfg.Scratch.Dir, "restore")})
		if err != nil {
			return nil, failErr(c, err)
		}
		o.restored, o.join = rep, rep.Workspace.ID
		say("  Restored “%s”: %s, each checked against the archive.", rep.Workspace.Name, plural(int(rep.Documents), "record"))
		if len(rep.Migrated) > 0 {
			say("  Brought it up to date (%s).", plural(len(rep.Migrated), "step"))
		}
		if rep.Media.Missing > 0 {
			say("  ! %s of its footage aren't in this storage yet.", plural(rep.Media.Missing, "file"))
		}
	}
	// Workspace: create or join.
	now := time.Now().UTC()
	var ws models.Workspace
	var existing []models.Workspace
	cur, err := db.C(store.CWorkspaces).Find(ctx, bson.M{})
	if err == nil {
		_ = cur.All(ctx, &existing)
	}
	joinID := firstNonEmpty(o.join, cfg.Workspace.ID)
	if joinID == "" && len(existing) > 0 && !o.newWorkspace {
		if prompt {
			// The answer is a workspace ID, so the list shows them.
			say("\nThis database already has workspace(s):")
			for _, w := range existing {
				say("  %s  %s", w.ID, w.Name)
			}
			joinID = ask("Workspace ID to join (blank = create a new one)", existing[0].ID)
		} else {
			var names []string
			list := []map[string]string{}
			for _, w := range existing {
				names = append(names, "“"+w.Name+"”")
				list = append(list, map[string]string{"id": w.ID, "name": w.Name})
			}
			which := "a workspace"
			if len(names) > 1 {
				which = "workspaces"
			}
			return nil, failErr(c, apperr.New(apperr.InvalidInput, "This database already has %s: %s.", which, strings.Join(names, ", ")).
				WithHint("Add --join with its ID to work in it, or --new-workspace to make another one.").WithDetails(map[string]any{"workspaces": list, "command": "metarush init --join <workspace id>"}))
		}
	}
	if joinID != "" {
		if err := db.C(store.CWorkspaces).FindOne(ctx, bson.M{"_id": joinID}).Decode(&ws); err != nil {
			return nil, failErr(c, apperr.New(apperr.NotFound, "That workspace isn't in this database.").WithDetails(map[string]any{"workspace": joinID, "database": o.dbName}))
		}
		if ws.MediaStore.Locator != "" && ws.MediaStore.Locator != ms.Locator() {
			say("  Warning: this workspace keeps its footage in other storage than the one you chose, so you wouldn't see the same footage as the others.")
			tech("  the workspace's media store is %s, this machine is set up for %s", ws.MediaStore.Locator, ms.Locator())
			if !o.yes && prompt && !askYes("Continue anyway?", false) {
				return nil, fail(c, "E_CANCELED", "Setup canceled.", "")
			}
		}
		say("\nJoining the workspace “%s”.", ws.Name)
		tech("  workspace %s", ws.ID)
	} else {
		name := firstNonEmpty(o.workspaceName, "My Studio")
		if prompt {
			name = ask("Workspace name", name)
		}
		ws = models.Workspace{ID: ids.New(ids.Workspace), Name: name, CreatedAt: now,
			MediaStore: models.StoreIdentity{Provider: ms.Name(), Locator: ms.Locator(), Shared: ms.Capabilities().Shared},
			// New workspaces hold versions back until the quality check and sign-offs are done (D298).
			ShipGate: &models.ShipGateSettings{On: true, UpdatedAt: now, UpdatedBy: models.Actor{Via: "cli"}}}
		ws.WorkspaceID = ws.ID
		say("\nCreating the workspace “%s”.", ws.Name)
		tech("  workspace %s", ws.ID)
	}
	// User.
	var user models.User
	if o.restored != nil {
		user = restoredPerson(ctx, db, ws, o.userEmail)
		if user.ID != "" && !strings.EqualFold(user.Email, o.userEmail) {
			o.userEmail, o.userName = user.Email, user.Name
			say("  This computer works as “%s”, the restored workspace's owner (another person: --user-email).", user.Name)
		}
	}
	if user.ID == "" {
		_ = db.C(store.CUsers).FindOne(ctx, bson.M{"workspaceId": ws.ID, "email": o.userEmail}).Decode(&user)
	}
	if user.ID == "" || o.userEmail == "" && o.restored == nil {
		user = models.User{ID: ids.New(ids.User), WorkspaceID: ws.ID, Name: o.userName, Email: o.userEmail, CreatedAt: now}
		if err := db.Insert(ctx, store.CUsers, user); err != nil {
			return nil, err
		}
	}
	instID := firstNonEmpty(cfg.Installation.ID, o.installID)
	if instID == "" {
		instID = ids.New(ids.Installation)
	}
	if !ids.Valid(ids.Installation, instID) {
		return nil, failErr(c, apperr.New(apperr.InvalidInput, "The --installation-id value isn't a valid machine ID.").WithDetails(map[string]any{"installation": instID}))
	}
	// An invited installation keeps the record `machine invite` made; a revoked
	// one cannot be brought back by re-running init.
	var prior models.Installation
	hasPrior := db.C(store.CInstallations).FindOne(ctx, bson.M{"_id": instID}).Decode(&prior) == nil
	if hasPrior && prior.WorkspaceID != ws.ID {
		return nil, failErr(c, apperr.New(apperr.Conflict, "This machine's invitation belongs to another workspace.").WithDetails(map[string]any{"installation": instID}))
	}
	if hasPrior && prior.State == models.InstallationRevoked {
		return nil, failErr(c, apperr.New(apperr.Unauthorized, "“%s” was removed from the workspace, so it can't join with that invitation.", prior.Name).
			WithHint("Ask whoever runs the workspace for a new invitation.").WithDetails(map[string]any{"installation": instID, "command": "metarush machine invite <name>"}))
	}
	actor := models.Actor{UserID: user.ID, UserName: user.Name, InstallationID: instID, Via: "cli"}
	if joinID == "" {
		ws.CreatedBy = actor
		if ws.ShipGate != nil {
			ws.ShipGate.UpdatedBy = actor
		}
		if err := db.Insert(ctx, store.CWorkspaces, ws); err != nil {
			return nil, err
		}
	}
	inst := models.Installation{ID: instID, WorkspaceID: ws.ID, Name: o.installName, Hostname: app.Hostname(), OS: runtime.GOOS, Arch: runtime.GOARCH,
		Version: version.Version, Commit: version.Commit, Built: version.Date, UserID: user.ID, CreatedAt: now, LastSeenAt: now}
	set := bson.M{"workspaceId": inst.WorkspaceID, "name": inst.Name, "hostname": inst.Hostname, "os": inst.OS, "arch": inst.Arch,
		"version": inst.Version, "commit": inst.Commit, "built": inst.Built, "userId": inst.UserID, "lastSeenAt": now, "state": models.InstallationActive}
	if cfg.Media.Provider == "s3" && !config.IsSecretRef(cfg.Media.S3.AccessKeyID) {
		set["storageKeyId"] = cfg.Media.S3.AccessKeyID // an identifier, not the secret
	}
	if hasPrior && prior.State == models.InstallationInvited {
		set["joinedAt"] = now
	}
	if _, err := db.C(store.CInstallations).UpdateOne(ctx, bson.M{"_id": inst.ID}, bson.M{"$set": set, "$setOnInsert": bson.M{"createdAt": now}}, options.UpdateOne().SetUpsert(true)); err != nil {
		return nil, err
	}
	// Assemble and persist config (secrets to keychain or env placeholders).
	cfg.Version = 1
	cfg.Workspace = config.Ref{ID: ws.ID, Name: ws.Name}
	cfg.Installation = config.Ref{ID: inst.ID, Name: inst.Name}
	cfg.User = config.UserRef{ID: user.ID, Name: user.Name, Email: user.Email}
	cfg.Database.Name = o.dbName
	switch {
	case o.localPort > 0:
		cfg.Database.Local = &config.LocalDatabase{Port: o.localPort}
	case cfg.Database.Local != nil && o.dbURI != oldURI:
		cfg.Database.Local = nil // another database now: not the one on this computer
	}
	cfg.Database.AtlasOrgID, cfg.Database.AtlasOrgName, cfg.Database.AtlasProjectID = firstNonEmpty(o.atlasOrgID, cfg.Database.AtlasOrgID), firstNonEmpty(o.atlasOrgName, cfg.Database.AtlasOrgName), firstNonEmpty(o.atlasProjectID, cfg.Database.AtlasProjectID)
	if o.cacheDir != "" {
		cfg.Cache.Dir = o.cacheDir
	}
	if o.cacheMaxGB > 0 {
		cfg.Cache.MaxGB = o.cacheMaxGB
	}
	if o.port > 0 {
		cfg.Dashboard.Port = o.port
	}
	if o.watchDir != "" {
		cfg.Worker.Watch.Enabled, cfg.Worker.Watch.Dir = true, o.watchDir
	}
	// Renders use this version's own renderer (D75). --video-dir is the
	// developer setting: render from that live checkout instead.
	if o.videoDir != "" {
		abs, err := filepath.Abs(expandHome(o.videoDir))
		if err != nil {
			return nil, err
		}
		cfg.Tools.VideoDir, cfg.Tools.DevRenderer = abs, true
	}
	cfg.Secrets = o.secrets
	// An address this home's secrets already hold for another database (an
	// earlier setup's) is never replaced without a yes, and is kept aside.
	moved, err := o.replaceSavedAddress(c, h, prompt)
	if err != nil {
		return nil, err
	}
	if moved != "" {
		homeWarnings = append(homeWarnings, moved)
		say("! %s", moved)
	}
	switch o.secrets {
	case "keychain":
		ref, err := config.StoreSecret(h, "mongodb-uri", o.dbURI)
		if err != nil {
			return nil, failErr(c, apperr.Wrap(apperr.NotConfigured, err, "The keychain couldn't keep the database password.").
				WithHint("Add --secrets env to keep passwords and keys in environment variables instead."))
		}
		cfg.Database.URI = ref
		if cfg.Media.Provider == "s3" {
			ref, err := config.StoreSecret(h, "s3-secret-access-key", cfg.Media.S3.SecretAccessKey)
			if err != nil {
				return nil, err
			}
			cfg.Media.S3.SecretAccessKey = ref
		}
	case "env":
		// The home works by itself: its .env (0600, where secretpath allows a
		// secrets file) holds what config.yaml names, so MetaRush in the
		// background and the Claude Code panel's metarush find it there — never
		// only in the environment of the terminal that ran init.
		if err := config.SetDotEnvVar(h, config.EnvPrefix+"MONGODB_URI", o.dbURI, o.allowUnsafeSecretPath); err != nil {
			return nil, failErr(c, err)
		}
		cfg.Database.URI = "${METARUSH_MONGODB_URI}"
		if cfg.Media.Provider == "s3" {
			if sec := cfg.Media.S3.SecretAccessKey; sec != "" && !strings.HasPrefix(sec, "${") {
				if err := config.SetDotEnvVar(h, config.EnvPrefix+"S3_SECRET_ACCESS_KEY", sec, o.allowUnsafeSecretPath); err != nil {
					return nil, failErr(c, err)
				}
			}
			cfg.Media.S3.SecretAccessKey = "${METARUSH_S3_SECRET_ACCESS_KEY}"
		}
	default:
		return nil, fail(c, "E_INVALID_INPUT", "--secrets must be keychain or env", "")
	}
	if err := config.Save(h, cfg); err != nil {
		return nil, err
	}
	for _, d := range []string{cfg.Cache.Dir, cfg.Scratch.Dir, cfg.StateDir(), cfg.LogDir()} {
		_ = os.MkdirAll(d, 0o755)
	}
	if o.restored != nil {
		if err := wsarchive.RecordRestore(ctx, db, o.restored, filepath.Base(o.restore), actor, inst.Name); err != nil {
			say("  Warning: the restore couldn't be written into the workspace's activity log.")
			tech("  %v", err)
		}
	}
	result := map[string]any{"home": h, "workspace": cfg.Workspace, "installation": cfg.Installation, "user": cfg.User, "database": map[string]any{"host": host, "name": o.dbName, "version": ver, "replicaSet": rs, "atlasOrgId": cfg.Database.AtlasOrgID},
		"mediaStore": ms.Locator(), "secrets": o.secrets, "dashboard": fmt.Sprintf("http://%s:%d", cfg.Dashboard.Host, cfg.Dashboard.Port)}
	if len(homeWarnings) > 0 {
		result["warnings"] = homeWarnings
	}
	return result, nil
}

// restoredPerson is who this computer works as in a restored workspace: the
// person with the email given, else its only owner, else (before anyone had
// the owner role) the person who set the workspace up.
func restoredPerson(ctx context.Context, db *store.DB, ws models.Workspace, email string) models.User {
	var u models.User
	if email != "" && db.C(store.CUsers).FindOne(ctx, bson.M{"workspaceId": ws.ID, "email": email}).Decode(&u) == nil {
		return u
	}
	var owners []models.User
	if cur, err := db.C(store.CUsers).Find(ctx, bson.M{"workspaceId": ws.ID, "role": models.RoleOwner}); err == nil {
		_ = cur.All(ctx, &owners)
	}
	switch {
	case len(owners) == 1:
		return owners[0]
	case len(owners) == 0 && ws.CreatedBy.UserID != "":
		_ = db.C(store.CUsers).FindOne(ctx, bson.M{"_id": ws.CreatedBy.UserID, "workspaceId": ws.ID}).Decode(&u)
	}
	return u
}

func storeRoundTrip(ctx context.Context, ms media.Store) error {
	tmp, err := os.CreateTemp("", "metarush-probe-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	payload := []byte("metarush store probe " + time.Now().UTC().Format(time.RFC3339Nano))
	tmp.Write(payload)
	tmp.Close()
	key := "probes/" + ids.New("prb")
	if _, err := ms.PutFile(ctx, key, tmp.Name(), media.PutOptions{ContentType: "text/plain"}, media.Resume{}, nil); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	rc, _, err := ms.Get(ctx, key, 8, 5)
	if err != nil {
		return fmt.Errorf("ranged read: %w", err)
	}
	b := make([]byte, 5)
	n, _ := rc.Read(b)
	rc.Close()
	if string(b[:n]) != string(payload[8:13]) {
		return errors.New("ranged read returned wrong bytes")
	}
	return ms.Delete(ctx, key)
}

func mergeFileConfig(o *initOpts, fc *config.Config) {
	set := func(dst *string, v string) {
		if *dst == "" && v != "" {
			*dst = v
		}
	}
	set(&o.workspaceName, fc.Workspace.Name)
	set(&o.join, fc.Workspace.ID)
	set(&o.dbURI, fc.Database.URI)
	set(&o.dbName, fc.Database.Name)
	set(&o.atlasOrgID, fc.Database.AtlasOrgID)
	set(&o.atlasProjectID, fc.Database.AtlasProjectID)
	set(&o.mediaProvider, fc.Media.Provider)
	set(&o.mediaRoot, fc.Media.Filesystem.Root)
	set(&o.s3Preset, fc.Media.S3.Preset)
	set(&o.s3Endpoint, fc.Media.S3.Endpoint)
	set(&o.s3Region, fc.Media.S3.Region)
	set(&o.s3Bucket, fc.Media.S3.Bucket)
	set(&o.s3Prefix, fc.Media.S3.Prefix)
	set(&o.s3AccessKeyID, fc.Media.S3.AccessKeyID)
	set(&o.gcsBucket, fc.Media.GCS.Bucket)
	set(&o.gcsPrefix, fc.Media.GCS.Prefix)
	set(&o.gcsCredentials, fc.Media.GCS.CredentialsFile)
	set(&o.cacheDir, fc.Cache.Dir)
	set(&o.userName, fc.User.Name)
	set(&o.userEmail, fc.User.Email)
	set(&o.installName, fc.Installation.Name)
	set(&o.watchDir, fc.Worker.Watch.Dir)
	set(&o.videoDir, fc.Tools.VideoDir)
	if o.cacheMaxGB == 0 {
		o.cacheMaxGB = fc.Cache.MaxGB
	}
	if o.port == 0 {
		o.port = fc.Dashboard.Port
	}
	if fc.Media.Filesystem.Shared {
		o.mediaShared = true
	}
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if u, err := os.UserHomeDir(); err == nil {
			return filepath.Join(u, p[2:])
		}
	}
	return p
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// replaceSavedAddress applies setup's rule to a database address this
// home's secrets (the keychain or <home>/.env) already hold for another
// database: replaced only with a yes — the question in a terminal (default
// no), --replace-saved-database otherwise — and kept aside first. It returns
// the plain line saying where the old one went ("" when nothing was there).
func (o *initOpts) replaceSavedAddress(c *cobra.Command, h string, prompt bool) (string, error) {
	var existing string
	if o.secrets == "env" {
		existing, _ = config.DotEnvValue(h, config.EnvPrefix+"MONGODB_URI")
	} else if o.secrets == "keychain" {
		existing, _ = config.ResolveSecret(h, config.SecretRef("mongodb-uri"))
	}
	if existing == "" || existing == o.dbURI {
		return "", nil
	}
	allowed := o.replaceSaved
	switch {
	case allowed:
	case o.askReplace != nil:
		allowed = o.askReplace()
	case prompt:
		fmt.Println("This MetaRush already keeps the address of another database.")
		allowed = askYes("Replace it with the one you gave? The old one is kept aside.", false)
	}
	if !allowed {
		return "", failErr(c, apperr.New(apperr.Conflict, "This MetaRush already keeps the address of another database, so it wasn't replaced.").
			WithHint("To use the one you gave, let it be replaced; the old address is kept aside.").
			WithCommand(commandLine(c, c.CommandPath(), "--replace-saved-database")))
	}
	where, err := localSecrets{home: h, env: o.secrets == "env", allowUnsafe: o.allowUnsafeSecretPath}.keepPrevious(existing)
	if err != nil {
		return "", failErr(c, err)
	}
	return "The database address this MetaRush kept for another database was replaced; the old one is kept aside " + where + ".", nil
}
