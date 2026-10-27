package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/cache"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/localdb"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/wsarchive"
)

// Exporting and restoring a workspace (D329–D333, docs/workspace-export.md).
// Both are a person's at their own terminal (never Claude Code, an AI app or a
// script nobody watches) and an owner's (this computer's command line counts
// as the owner). Export opens the database read-only: no migrations, no
// indexes, no jobs, no writes, so it works on a database an older MetaRush
// migrated and with a read-only database user. Restore writes only into an
// empty database and is not an operation (there is no workspace to call it
// in until it is done): it writes its own entry into the copy's log.

// dbSource is where another database's address comes from: never the command
// line, where it would stay in the shell's history.
type dbSource struct {
	stdin  bool
	env    string
	name   string
	refuse string // a value given on the command line: refused, never used
}

func (d *dbSource) flags(c *cobra.Command) {
	f := c.Flags()
	f.BoolVar(&d.stdin, "db-uri-stdin", false, "another database: read its address from standard input")
	f.StringVar(&d.env, "db-uri-env", "", "another database: read its address from this environment variable")
	f.StringVar(&d.name, "db-name", "", "that database's name (default metarush)")
	f.StringVar(&d.refuse, "db-uri", "", "")
	_ = f.MarkHidden("db-uri")
}

func (d *dbSource) given() bool { return d.stdin || d.env != "" || d.refuse != "" }

// uri reads the address.
func (d *dbSource) uri(c *cobra.Command) (string, error) {
	switch {
	case d.refuse != "":
		return "", apperr.New(apperr.InvalidInput, "A database address isn't taken on the command line, where it would stay in the shell's history.").
			WithHint("Pipe it in with --db-uri-stdin, or name an environment variable that holds it with --db-uri-env.")
	case d.stdin:
		b, err := io.ReadAll(io.LimitReader(c.InOrStdin(), 16<<10))
		if err != nil {
			return "", err
		}
		u := strings.TrimSpace(string(b))
		if u == "" {
			return "", apperr.New(apperr.InvalidInput, "No database address came on standard input.")
		}
		return u, nil
	default:
		u := strings.TrimSpace(os.Getenv(d.env))
		if u == "" {
			return "", apperr.New(apperr.InvalidInput, "The environment variable %s is empty.", d.env)
		}
		return u, nil
	}
}

func (d *dbSource) dbName() string { return firstNonEmpty(d.name, "metarush") }

// storageFlags describe a storage the way setup's flags do.
type storageFlags struct {
	kind                                     string
	root                                     string
	shared                                   bool
	s3Preset, s3Endpoint, s3Region, s3Bucket string
	s3Prefix, s3AccessKeyID, s3SecretEnv     string
	s3PathStyle                              bool
	gcsBucket, gcsPrefix, gcsCredentials     string
}

func (s *storageFlags) flags(c *cobra.Command, what string) {
	f := c.Flags()
	f.StringVar(&s.kind, "storage", "", what+": folder, s3 or gcs (default: this computer's own storage)")
	f.StringVar(&s.root, "media-root", "", "a folder storage's folder")
	f.BoolVar(&s.shared, "media-shared", false, "the folder is on a shared drive")
	f.StringVar(&s.s3Preset, "s3-preset", "", "tigris | r2 | b2 | aws | gcs | custom")
	f.StringVar(&s.s3Endpoint, "s3-endpoint", "", "the bucket's address")
	f.StringVar(&s.s3Region, "s3-region", "", "region")
	f.StringVar(&s.s3Bucket, "s3-bucket", "", "bucket")
	f.StringVar(&s.s3Prefix, "s3-prefix", "", "key prefix in the bucket")
	f.StringVar(&s.s3AccessKeyID, "s3-access-key-id", "", "access key ID")
	f.StringVar(&s.s3SecretEnv, "s3-secret-env", "", "the environment variable holding the secret key (default METARUSH_S3_SECRET_ACCESS_KEY; asked for at a terminal)")
	f.BoolVar(&s.s3PathStyle, "s3-path-style", false, "path-style addressing")
	f.StringVar(&s.gcsBucket, "gcs-bucket", "", "Google Cloud Storage bucket")
	f.StringVar(&s.gcsPrefix, "gcs-prefix", "", "key prefix in the Google Cloud Storage bucket")
	f.StringVar(&s.gcsCredentials, "gcs-credentials", "", "a service-account key file (default: your gcloud sign-in)")
}

func (s *storageFlags) given() bool {
	return s.kind != "" || s.root != "" || s.s3Bucket != "" || s.gcsBucket != ""
}

// media builds the storage the flags describe; secret is its key (kept out of archives).
func (s *storageFlags) media() (config.Media, string, error) {
	kind := s.kind
	switch {
	case kind != "":
	case s.s3Bucket != "":
		kind = "s3"
	case s.gcsBucket != "":
		kind = "gcs"
	default:
		kind = "folder"
	}
	switch kind {
	case "folder", "filesystem":
		if s.root == "" {
			return config.Media{}, "", apperr.New(apperr.InvalidInput, "Say which folder the storage is (--media-root).")
		}
		abs, err := filepath.Abs(expandHome(s.root))
		if err != nil {
			return config.Media{}, "", err
		}
		return config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: abs, Shared: s.shared}}, "", nil
	case "s3":
		p := media.S3Presets[s.s3Preset]
		env := firstNonEmpty(s.s3SecretEnv, config.EnvPrefix+"S3_SECRET_ACCESS_KEY")
		secret := strings.TrimSpace(os.Getenv(env))
		if secret == "" && isInteractive() {
			secret = strings.TrimSpace(promptSecret("The storage's secret access key"))
		}
		if s.s3Bucket == "" || s.s3AccessKeyID == "" || secret == "" {
			return config.Media{}, "", apperr.New(apperr.InvalidInput, "A bucket needs its name, an access key ID and its secret key.").
				WithHint("Give --s3-bucket and --s3-access-key-id, and the secret in an environment variable (--s3-secret-env) or when asked.")
		}
		return config.Media{Provider: "s3", S3: config.S3{Preset: s.s3Preset, Endpoint: firstNonEmpty(s.s3Endpoint, p.Endpoint), Region: firstNonEmpty(s.s3Region, p.Region),
			Bucket: s.s3Bucket, Prefix: s.s3Prefix, AccessKeyID: s.s3AccessKeyID, SecretAccessKey: secret, ForcePathStyle: s.s3PathStyle || p.PathStyle}}, secret, nil
	case "gcs":
		if s.gcsBucket == "" {
			return config.Media{}, "", apperr.New(apperr.InvalidInput, "Say which bucket (--gcs-bucket).")
		}
		creds := s.gcsCredentials
		if creds != "" {
			abs, err := filepath.Abs(expandHome(creds))
			if err != nil {
				return config.Media{}, "", err
			}
			creds = abs
		}
		return config.Media{Provider: "gcs", GCS: config.GCS{Bucket: s.gcsBucket, Prefix: s.gcsPrefix, CredentialsFile: creds}}, "", nil
	}
	return config.Media{}, "", apperr.New(apperr.InvalidInput, "The storage is folder, s3 or gcs.")
}

// personAtTerminal: exporting and restoring a workspace are a person's (D332):
// never Claude Code or another AI app, never a script nobody watches. The
// address of another database may come on standard input, so a terminal on
// standard input or standard error is enough.
func personAtTerminal(c *cobra.Command, what string) error {
	if agentAtTheKeys(c) || !atTerminal() {
		return failErr(c, apperr.New(apperr.HumanOnly, "Only a person %s a workspace, at their own terminal: Claude, Auto and AI apps never do.", what).
			WithHint("Run the command yourself in a terminal (the dashboard's Settings → Storage has Export too)."))
	}
	return nil
}

// atTerminal: standard input or standard error is a terminal (a variable so
// tests can play a person at one).
var atTerminal = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) || term.IsTerminal(int(os.Stderr.Fd()))
}

func workspaceArchiveCmds() []*cobra.Command {
	return []*cobra.Command{workspaceExportCmd(), workspaceExportsCmd(), workspaceRestoreCmd()}
}

func workspaceExportCmd() *cobra.Command {
	var in ops.WorkspaceExportIn
	var db dbSource
	var st storageFlags
	var workspace string
	cmd := &cobra.Command{
		Use:   "export --to <folder or file> [--media referenced|included] [--tar] [--plan]",
		Short: "Export the whole workspace into one archive for a backup or a move (owners, people only; --plan first)",
		Long: `Write everything about the work in this workspace into an archive: projects, versions, transcripts,
comments, people and their consent records, Claude Code conversations and the activity log, one file per kind of
record, with a manifest that checks every file. Passwords, keys and each computer's own settings stay out.

The footage is referenced (the default: the archive lists it by content address, and the copy expects it in the
same storage) or included (--media included: every file is copied into the archive and checked). --tar writes
one file instead of a folder. --plan says what would be written, with counts and sizes, and writes nothing.

The export only reads: it never updates the database, so it also reads a database an older MetaRush left, and a
database user that may only read is enough. Another database's address comes from standard input
(--db-uri-stdin) or an environment variable (--db-uri-env), never the command line; its storage from flags like
setup's.

The archive is private to your account on this computer (folders 0700, files 0600), but anyone who gets a copy can
read all of it: keep it as carefully as the work.

  metarush workspace export --to ~/Backups/studio --plan
  metarush workspace export --to /Volumes/Backup/studio.tar --tar --media included`,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := personAtTerminal(c, "exports"); err != nil {
				return err
			}
			if strings.TrimSpace(in.To) == "" {
				return fail(c, apperr.InvalidInput, "Say where to write the archive (--to).", "")
			}
			a, secrets, err := openExportApp(c, &db, &st, workspace)
			if err != nil {
				return err
			}
			defer a.Close()
			ctx := ops.WithExportSecrets(c.Context(), secrets...)
			c.SetContext(ctx)
			return runOpWith(c, a, "workspace.export", in, ops.Caller{Human: true}, func(o any) { printExport(c, o.(*ops.WorkspaceExportOut)) })
		},
	}
	f := cmd.Flags()
	f.StringVar(&in.To, "to", "", "where to write the archive: a new folder (or with --tar a new file)")
	f.StringVar(&in.Media, "media", "referenced", "the footage: referenced (listed by content address) or included (copied in)")
	f.BoolVar(&in.Tar, "tar", false, "write one file (.tar) instead of a folder")
	f.BoolVar(&in.Plan, "plan", false, "only say what would be written: counts, sizes, what stays out and why")
	f.StringVar(&workspace, "workspace", "", "the workspace to export when the database holds several (another database only)")
	db.flags(cmd)
	st.flags(cmd, "where the workspace keeps its footage")
	return cmd
}

func workspaceExportsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "exports",
		Short: "List the exports of the workspace made on this computer and how far each one is (owners, people only)",
		RunE: func(c *cobra.Command, _ []string) error {
			if err := personAtTerminal(c, "looks at the exports of"); err != nil {
				return err
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "workspace.exports", ops.WorkspaceExportsIn{}, ops.Caller{Human: true}, func(o any) {
				out := o.(*ops.WorkspaceExportsOut)
				if len(out.Exports) == 0 {
					fmt.Println("No exports were made on this computer yet.")
				}
				for _, e := range out.Exports {
					fmt.Printf("%s  %s — %s\n", e.StartedAt.Local().Format("2 Jan 15:04"), theirs(e.Name), e.Says)
					if detailsMode(c) {
						fmt.Printf("    %s (%s, %s)\n", e.Path, e.ID, e.State)
					}
				}
			})
		},
	}
}

// openExportApp opens the database to export read-only, as an App the
// workspace.export operation runs on: its configured workspace, or another
// database's (--db-uri-stdin/--db-uri-env). It returns the secrets that must
// never reach the archive beyond the installation's own.
func openExportApp(c *cobra.Command, src *dbSource, st *storageFlags, workspace string) (*app.App, []string, error) {
	h, err := home()
	if err != nil {
		return nil, nil, failErr(c, err)
	}
	var homeCfg *config.Config
	if config.Exists(h) {
		if homeCfg, err = config.Load(h); err != nil && !src.given() {
			return nil, nil, notSetUp(c, h, err)
		}
	}
	if homeCfg == nil && !src.given() {
		return nil, nil, notSetUp(c, h, errors.New("not set up"))
	}
	ctx, cancel := context.WithTimeout(c.Context(), 30*time.Second)
	defer cancel()
	var (
		db      *store.DB
		secrets []string
		cfg     config.Config
	)
	if homeCfg != nil {
		cfg = *homeCfg
	} else {
		tmp, err := os.MkdirTemp("", "metarush-export-")
		if err != nil {
			return nil, nil, err
		}
		cfg = *config.Default(tmp)
		cfg.SetHome(tmp)
	}
	if src.given() {
		uri, err := src.uri(c)
		if err != nil {
			return nil, nil, failErr(c, err)
		}
		secrets = append(secrets, uri)
		if u, err := url.Parse(uri); err == nil && u.User != nil {
			if pw, ok := u.User.Password(); ok {
				secrets = append(secrets, pw)
			}
		}
		if db, err = store.Connect(ctx, uri, src.dbName()); err != nil {
			return nil, nil, failErr(c, apperr.Wrap(apperr.Database, err, "MetaRush couldn't reach that database.").
				WithHint("Check the address and the password, and on MongoDB Atlas the network access list."))
		}
		ws, err := wsarchive.ChooseWorkspace(ctx, db, workspace)
		if err != nil {
			_ = db.Close(context.Background())
			return nil, nil, failErr(c, err)
		}
		mine := homeCfg != nil && homeCfg.Workspace.ID == ws.ID
		cfg.Workspace = config.Ref{ID: ws.ID, Name: ws.Name}
		cfg.Database = config.Database{Name: src.dbName()}
		if !st.given() && !mine {
			_ = db.Close(context.Background())
			return nil, nil, fail(c, apperr.InvalidInput, "Say where that workspace keeps its footage, so the archive can list it.",
				"Give its storage the way setup takes it: --storage folder --media-root <folder>, or --s3-bucket …, or --gcs-bucket ….")
		}
	} else {
		if workspace != "" && workspace != homeCfg.Workspace.ID {
			return nil, nil, fail(c, apperr.InvalidInput, "This computer's database holds its own workspace: another one's export needs that database's address.", "")
		}
		if l := homeCfg.Database.Local; l != nil {
			if err := localdb.New(homeCfg.Home(), l.Port, tools.New(homeCfg.Tools)).Start(ctx); err != nil {
				return nil, nil, failErr(c, databaseErr(err, "The database on this computer couldn't start"))
			}
		}
		if db, err = store.Connect(ctx, homeCfg.Database.URI, homeCfg.Database.Name); err != nil {
			return nil, nil, failErr(c, apperr.Wrap(apperr.Database, err, "MetaRush can't reach its database.").WithHint(checkupHint).WithCommand("metarush doctor"))
		}
	}
	if st.given() {
		m, secret, err := st.media()
		if err != nil {
			_ = db.Close(context.Background())
			return nil, nil, failErr(c, err)
		}
		cfg.Media = m
		if secret != "" {
			secrets = append(secrets, secret)
		}
	}
	ms, err := mediaForExport(cfg.Media)
	if err != nil {
		_ = db.Close(context.Background())
		return nil, nil, failErr(c, apperr.Wrap(apperr.Storage, err, "MetaRush can't use that storage."))
	}
	var ch *cache.Cache
	if homeCfg != nil {
		ch, _ = cache.New(homeCfg.Cache.Dir, homeCfg.Cache.MaxGB)
	}
	ts := tools.New(cfg.Tools)
	actor := models.Actor{UserID: cfg.User.ID, UserName: firstNonEmpty(cfg.User.Name, app.Username()), InstallationID: cfg.Installation.ID, Via: "cli"}
	a := &app.App{Cfg: &cfg, DB: db, Media: ms, Cache: ch, Tools: ts, Actor: actor, ReadOnly: true}
	if homeCfg != nil {
		// The export's own entry waits in this computer's spool: nothing is
		// written to the database it reads (the next MetaRush that runs here
		// writes it into this computer's own database).
		a.Log = logbook.Open(logbook.Options{Cfg: homeCfg, Tools: ts, Actor: actor})
	}
	return a, secrets, nil
}

// mediaForExport opens a storage without making anything in it (a folder
// storage's folder must be there already).
func mediaForExport(m config.Media) (media.Store, error) {
	if m.Provider == "" || m.Provider == "filesystem" {
		root, err := filepath.Abs(m.Filesystem.Root)
		if err != nil {
			return nil, err
		}
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("the folder %s isn't there", root)
		}
		return &media.FS{Root: root, Shared: m.Filesystem.Shared}, nil
	}
	return app.NewMediaStoreFor(m)
}

func printExport(c *cobra.Command, out *ops.WorkspaceExportOut) {
	if p := out.Plan; p != nil {
		fmt.Println(out.Summary)
		if p.Level.Older {
			fmt.Printf("The database was last brought up to date by an older MetaRush; the export reads it as it is, and a restore brings the copy up to date.\n")
		}
		fmt.Println("\nWhat goes in:")
		for _, col := range p.Collections {
			if col.Count == 0 && !detailsMode(c) {
				continue
			}
			how := ""
			if col.How == wsarchive.Transformed {
				how = " (changed: " + col.Why + ")"
			}
			fmt.Printf("  %-26s %8s records  %9s%s\n", col.Name, thousandsInt(col.Count), fmtBytes(col.Bytes), how)
		}
		if len(p.LeftOut) > 0 {
			fmt.Println("\nWhat stays out:")
			for _, l := range p.LeftOut {
				if l.Count == 0 && !l.Unknown && !detailsMode(c) {
					continue
				}
				fmt.Printf("  %s (%s): %s\n", l.Name, pluralWords(int(l.Count), "record", "records"), l.Why)
			}
		}
		if p.Media.Files > 0 {
			fmt.Printf("\nFootage: %s, %s (%s).\n", pluralWords(p.Media.Files, "file", "files"), fmtBytes(p.Media.Bytes), map[bool]string{true: "copied into the archive", false: "listed by where it is kept"}[p.Media.Mode == wsarchive.MediaIncluded])
		}
		for _, n := range p.Notes {
			fmt.Println("! " + n)
		}
		if p.Free > 0 {
			fmt.Printf("Room where it goes: %s free.\n", fmtBytes(p.Free))
		}
		fmt.Println("\n" + out.Holds)
		fmt.Println("Nothing was written. Run it again without --plan to make the archive.")
		return
	}
	e := out.Export
	if e == nil {
		fmt.Println(out.Summary)
		return
	}
	fmt.Println(e.Says)
	for _, n := range e.Notes {
		fmt.Println("! " + n)
	}
	if len(e.LeftOut) > 0 {
		fmt.Printf("Left out on purpose: %s (passwords, sign-in links, each computer's own settings).\n", strings.Join(e.LeftOut, ", "))
	}
	fmt.Println("It is in " + e.Path)
	fmt.Println("Only your account on this computer can open it; anyone who gets a copy can read all of it.")
	fmt.Println("To restore it on another computer: metarush setup --restore <archive>, or metarush workspace restore <archive> into an empty database.")
}

func thousandsInt(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func workspaceRestoreCmd() *cobra.Command {
	var db dbSource
	var st storageFlags
	var plan bool
	cmd := &cobra.Command{
		Use:   "restore <archive>",
		Short: "Restore an exported workspace into an empty database, checking every file first (people only, at a terminal; --plan first)",
		Long: `Restore an archive made by metarush workspace export into an empty database: every file's checksum is checked
first, then the footage goes into the storage (included: copied by content address and checked; referenced:
looked for there, and what is missing is named), the records are written, the migration level the archive was made
at is set, the copy is checked against the archive, and MetaRush brings it up to date. Nothing is ever deleted.

The database is another one (--db-uri-stdin or --db-uri-env, and --db-name), or this computer's when it is empty;
the storage is given like setup's (--storage folder --media-root …, --s3-bucket …, --gcs-bucket …) or this
computer's own. On a new computer, metarush setup --restore <archive> does it as part of setting MetaRush up.
--plan checks the archive and says what would happen, and writes nothing.

  metarush workspace restore ~/Backups/studio.tar --plan --db-uri-env NEW_MONGODB_URI --db-name studio-copy --storage folder --media-root ~/studio-media`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := personAtTerminal(c, "restores"); err != nil {
				return err
			}
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			t, err := openRestoreTarget(ctx, c, &db, &st)
			if err != nil {
				return err
			}
			defer t.close()
			archive, _ := filepath.Abs(expandHome(args[0]))
			rep, err := wsarchive.Restore(ctx, wsarchive.RestoreOptions{Archive: archive, DB: t.db, Store: t.store, StoreWords: t.storeWords,
				StateDir: t.stateDir, ScratchDir: t.scratchDir, Plan: plan, Progress: restoreProgress(c), MayStore: restoreMayStore(t.media, t.actor)})
			if err != nil {
				return failErr(c, err)
			}
			if !plan {
				machine := firstNonEmpty(t.machine, app.Hostname())
				if err := wsarchive.RecordRestore(ctx, t.db, rep, filepath.Base(archive), t.actor, machine); err != nil {
					fmt.Fprintf(os.Stderr, "warning: the restore couldn't be written into the copy's activity log: %v\n", err)
				}
			}
			out := restoreOut{RestoreReport: rep, Database: t.dbName, Next: restoreNext(rep, t)}
			return emit(c, out, func() { printRestore(c, rep, out.Next) })
		},
	}
	cmd.Flags().BoolVar(&plan, "plan", false, "check the archive and say what would happen; write nothing")
	db.flags(cmd)
	st.flags(cmd, "where the copy keeps its footage")
	return cmd
}

// restoreOut is `metarush workspace restore --json`.
type restoreOut struct {
	*wsarchive.RestoreReport
	Database string   `json:"database"`
	Next     []string `json:"next"`
}

// restoreTarget is where a restore writes.
type restoreTarget struct {
	db                   *store.DB
	dbName               string
	store                media.Store
	storeWords           string
	stateDir, scratchDir string
	media                config.Media
	actor                models.Actor
	machine              string
	home                 bool // this computer's own database
	cleanup              []string
}

func (t *restoreTarget) close() {
	if t.db != nil {
		_ = t.db.Close(context.Background())
	}
	for _, d := range t.cleanup {
		_ = os.RemoveAll(d) // this restore's own scratch folders
	}
}

func openRestoreTarget(ctx context.Context, c *cobra.Command, src *dbSource, st *storageFlags) (*restoreTarget, error) {
	h, err := home()
	if err != nil {
		return nil, failErr(c, err)
	}
	var homeCfg *config.Config
	if config.Exists(h) {
		homeCfg, _ = config.Load(h)
	}
	t := &restoreTarget{actor: models.Actor{UserName: firstNonEmpty(gitConfig("user.name"), app.Username()), Via: "cli"}}
	if homeCfg != nil {
		t.actor = models.Actor{UserID: homeCfg.User.ID, UserName: homeCfg.User.Name, InstallationID: homeCfg.Installation.ID, Via: "cli"}
		t.machine = homeCfg.Installation.Name
		t.stateDir = filepath.Join(homeCfg.StateDir(), "restore")
		t.scratchDir = filepath.Join(homeCfg.Scratch.Dir, "restore")
	} else {
		tmp, err := os.MkdirTemp("", "metarush-restore-")
		if err != nil {
			return nil, err
		}
		t.stateDir, t.scratchDir = filepath.Join(tmp, "state"), filepath.Join(tmp, "scratch")
		t.cleanup = append(t.cleanup, tmp)
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch {
	case src.given():
		uri, err := src.uri(c)
		if err != nil {
			return nil, failErr(c, err)
		}
		t.dbName = src.dbName()
		if strings.HasPrefix(t.dbName, store.TestDBPrefix) && !store.IsLocalURI(uri) {
			return nil, fail(c, apperr.InvalidInput, "Database names starting with "+store.TestDBPrefix+" are the test suite's.", "")
		}
		if t.db, err = store.Connect(cctx, uri, t.dbName); err != nil {
			return nil, failErr(c, apperr.Wrap(apperr.Database, err, "MetaRush couldn't reach that database.").
				WithHint("Check the address and the password, and on MongoDB Atlas the network access list."))
		}
	case homeCfg != nil:
		if l := homeCfg.Database.Local; l != nil {
			if err := localdb.New(homeCfg.Home(), l.Port, tools.New(homeCfg.Tools)).Start(cctx); err != nil {
				return nil, failErr(c, databaseErr(err, "The database on this computer couldn't start"))
			}
		}
		t.dbName, t.home = homeCfg.Database.Name, true
		if t.db, err = store.Connect(cctx, homeCfg.Database.URI, homeCfg.Database.Name); err != nil {
			return nil, failErr(c, apperr.Wrap(apperr.Database, err, "MetaRush can't reach its database.").WithHint(checkupHint).WithCommand("metarush doctor"))
		}
	default:
		return nil, fail(c, apperr.InvalidInput, "Say which empty database to restore into.",
			"Give its address on standard input (--db-uri-stdin) or in an environment variable (--db-uri-env), with --db-name; or set MetaRush up with metarush setup --restore <archive>.")
	}
	var m config.Media
	switch {
	case st.given():
		if m, _, err = st.media(); err != nil {
			t.close()
			return nil, failErr(c, err)
		}
	case homeCfg != nil:
		m = homeCfg.Media
	default:
		t.close()
		return nil, fail(c, apperr.InvalidInput, "Say which storage the copy keeps its footage in.",
			"Give it the way setup takes it: --storage folder --media-root <folder>, or --s3-bucket …, or --gcs-bucket ….")
	}
	if t.store, err = app.NewMediaStoreFor(m); err != nil {
		t.close()
		return nil, failErr(c, apperr.Wrap(apperr.Storage, err, "MetaRush can't use that storage."))
	}
	t.storeWords, t.media = ops.StorageWords(m), m
	return t, nil
}

// restoreMayStore is What may leave for a restore's footage: the restored
// workspace's own rules (its records are in by then), against the storage
// the copy uses; a refusal is recorded in the copy (D333).
func restoreMayStore(m config.Media, actor models.Actor) func(ctx context.Context, db *store.DB, ws string) error {
	return func(ctx context.Context, db *store.DB, ws string) error {
		if ops.LeaveStorage == nil {
			return nil
		}
		a := &app.App{Cfg: &config.Config{Workspace: config.Ref{ID: ws}}, DB: db, Actor: actor}
		return ops.LeaveStorage(ctx, a, m)
	}
}

// restoreProgress prints how far a restore is (on standard error, at a terminal).
func restoreProgress(c *cobra.Command) func(wsarchive.ArchiveProgress) {
	if jsonMode(c) || !term.IsTerminal(int(os.Stderr.Fd())) {
		return nil
	}
	last := ""
	return func(p wsarchive.ArchiveProgress) {
		var line string
		switch p.Phase {
		case "unpacking":
			line = "Unpacking the archive…"
		case "checking":
			line = "Checking every file of the archive…"
		case "media":
			line = "Footage…"
			if p.Files > 0 {
				line = fmt.Sprintf("Footage: %d of %d files…", p.FilesDone, p.Files)
			}
		case "documents":
			line = fmt.Sprintf("Writing the records (%d of %d kinds)…", p.CollectionsDone+1, p.Collections)
		case "verifying":
			line = "Checking the copy against the archive…"
		case "migrating":
			line = "Bringing the copy up to date…"
		}
		if line != "" && line != last {
			last = line
			fmt.Fprintf(os.Stderr, "\r\033[K%s", line)
		}
	}
}

func restoreNext(rep *wsarchive.RestoreReport, t *restoreTarget) []string {
	if rep.Plan {
		return []string{"Run it again without --plan to restore."}
	}
	var next []string
	if !t.home {
		next = append(next, fmt.Sprintf("To work in the copy on this computer: metarush setup --join %s --database elsewhere --db-name %s (the same address).", rep.Workspace.ID, t.dbName))
	}
	if len(rep.Elsewhere) > 0 {
		next = append(next, "Work tied to the computers it was asked on stays theirs until a computer hands its role over.")
	}
	next = append(next, "People with a password set it again: the archive carries no passwords (an owner sends them a new link in Settings → People).")
	return next
}

func printRestore(c *cobra.Command, rep *wsarchive.RestoreReport, next []string) {
	fmt.Fprint(os.Stderr, "\r\033[K")
	made := rep.MadeAt.Local().Format("2 January 2006 at 15:04")
	if rep.Plan {
		fmt.Printf("Every file of the archive of %s (made %s) checks out.\n", theirs(rep.Workspace.Name), made)
		fmt.Printf("A restore would write %s into the empty database", pluralWords(int(rep.Documents), "record", "records"))
	} else {
		fmt.Printf("Restored %s (made %s): %s, each checked against the archive", theirs(rep.Workspace.Name), made, pluralWords(int(rep.Documents), "record", "records"))
	}
	if len(rep.Migrated) > 0 {
		fmt.Printf(", %s up to date with %s.\n", map[bool]string{true: "then bring it", false: "then brought"}[rep.Plan], pluralWords(len(rep.Migrated), "step", "steps"))
	} else {
		fmt.Println(".")
	}
	switch rep.Media.Mode {
	case wsarchive.MediaIncluded:
		verb := "were copied into"
		if rep.Plan {
			verb = "would be copied into"
		}
		fmt.Printf("Footage: %s %s %s (%s there already).\n", pluralWords(rep.Media.Copied, "file", "files"), verb, rep.Media.StoreWords, thousandsInt(int64(rep.Media.Present)))
	default:
		fmt.Printf("Footage: %s of %s found in %s.\n", thousandsInt(int64(rep.Media.Present)), pluralWords(rep.Media.Files, "file", "files"), rep.Media.StoreWords)
		if rep.Media.Missing > 0 {
			fmt.Printf("! %s missing there.\n", pluralWords(rep.Media.Missing, "file is", "files are"))
			if detailsMode(c) {
				for _, k := range rep.Media.MissingKeys {
					fmt.Println("    " + k)
				}
			}
		}
	}
	for _, w := range rep.Elsewhere {
		var bits []string
		if w.Jobs > 0 {
			bits = append(bits, pluralWords(int(w.Jobs), "piece of work waiting", "pieces of work waiting"))
		}
		var runs []string
		if w.Runs > 0 {
			runs = append(runs, pluralWords(int(w.Runs), "run", "runs"))
		}
		if w.Triggers > 0 {
			runs = append(runs, pluralWords(int(w.Triggers), "that plans by itself", "that plan by themselves"))
		}
		if len(runs) > 0 {
			bits = append(bits, "in Recipes "+strings.Join(runs, " and "))
		}
		if w.Conversations > 0 {
			bits = append(bits, pluralWords(int(w.Conversations), "Claude Code conversation", "Claude Code conversations"))
		}
		fmt.Printf("Stays with %s: %s.\n", theirs(w.Computer), strings.Join(bits, ", "))
	}
	for _, n := range rep.Notes {
		fmt.Println("! " + n)
	}
	if !rep.Plan {
		fmt.Println("Nothing was deleted anywhere; the archive is unchanged.")
	}
	for _, n := range next {
		fmt.Println(n)
	}
}
