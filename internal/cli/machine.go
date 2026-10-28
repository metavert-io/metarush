package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/machines"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/secretpath"
	"github.com/metavert-io/metarush/internal/store"
)

func machineCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "machine", Short: "Add, list and remove the machines of a workspace (each with its own storage key)",
		Long: `Each machine in a workspace gets its own storage key (and, on MongoDB Atlas, its own
database user), so a lost laptop can be removed without touching anyone else.

  admin machine:  metarush machine invite studio-mini --atlas   # writes a 0600 join bundle to ~/.metarush-bundles/
  new machine:    metarush machine join metarush-join-studio-mini.json   # deletes the bundle once joined
  later:          metarush machine revoke studio-mini --atlas
  older machines: metarush machine rekey --dry-run, then metarush machine rekey   # its own key, in place
  another host:   metarush machine handover plan --to studio-mini   # hand this computer's role over
  gone for good:  metarush machine take-over old-laptop --from <copy of its folders>

Tigris keys are minted automatically; other providers print the exact manual steps.`}
	cmd.AddCommand(machineListCmd(), machineInviteCmd(), machineJoinCmd(), machineRevokeCmd(), machineRekeyCmd(), machineCIKeyCmd(),
		machineHandoverCmd(), machineTakeOverCmd())
	return cmd
}

func machineRekeyCmd() *cobra.Command {
	in := ops.MachineRekeyIn{}
	var dryRun bool
	c := &cobra.Command{
		Use:   "rekey",
		Short: "Give this machine its own storage key in place of the one it shares with other machines — people only",
		Long: `Machines that joined before each machine had its own key share one storage key, so one of
them can't be removed without cutting off the rest. Run this on each of them: it makes a key
named metarush-<this machine> that can only reach the workspace's footage (as invite does),
tests it, keeps its secret where this machine keeps secrets (the keychain, or MetaRush's .env),
switches this machine to it and checks that MetaRush works with it. If anything fails,
everything is put back and the new key is deleted.

The old key is never deleted: once every machine has its own key, the command says so, and you
delete the old key yourself in your storage provider's console. --dry-run says what it would do.

Storage without a key service (R2, B2, S3, Google Cloud interop, custom): make the key by hand
(--dry-run lists the steps), then run it with --access-key-id and the secret in
METARUSH_MACHINE_S3_SECRET_ACCESS_KEY.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if dryRun {
				return runOp(c, "machine.rekeyPlan", in, func(o any) { printRekey(o.(*machines.MachineRekey), detailsMode(c)) })
			}
			if !interactive() {
				return fail(c, apperr.HumanOnly, "Changing this machine's storage key must be done by a person.", "Run it yourself in a terminal; --dry-run first says what it would do.")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			b, _ := json.Marshal(in)
			po, err := ops.Call(ctx, a, "machine.rekeyPlan", b, ops.Caller{Human: true})
			if err != nil {
				return failErr(c, err)
			}
			plan := po.(*machines.MachineRekey)
			if plan.AlreadyOwn || len(plan.Steps) == 0 || len(plan.ManualSteps) > 0 {
				return emit(c, plan, func() { printRekey(plan, detailsMode(c)) })
			}
			if !jsonMode(c) {
				printRekey(plan, detailsMode(c))
			}
			if ask(`Type "rekey" to go ahead`, "") != "rekey" {
				return fail(c, apperr.Canceled, "Nothing changed.", "")
			}
			out, err := ops.Call(ctx, a, "machine.rekey", b, ops.Caller{Human: true})
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() { printRekey(out.(*machines.MachineRekey), detailsMode(c)) })
		},
	}
	f := c.Flags()
	f.BoolVar(&dryRun, "dry-run", false, "only say what it would do; change nothing")
	f.StringVar(&in.KeyScope, "key-scope", "auto", "auto | prefix (objects under the store prefix) | bucket (a bucket role)")
	f.StringVar(&in.Role, "role", "", "bucket role for --key-scope bucket: ReadWrite (default) | Editor | ReadOnly")
	f.StringVar(&in.AccessKeyID, "access-key-id", "", "use a key you created by hand; secret in "+machines.EnvManualSecret)
	f.BoolVar(&in.AllowUnsafeSecretPath, "allow-unsafe-secret-path", false, "secrets in <home>/.env: allow it inside a git repository that ignores it")
	return c
}

// printRekey says what a rekey did, or would do.
func printRekey(r *machines.MachineRekey, details bool) {
	switch {
	case r.AlreadyOwn:
		fmt.Println("This machine already has a storage key of its own: nothing to do.")
	case r.DryRun:
		fmt.Println("Giving this machine its own storage key would (nothing has changed yet):")
		for i, s := range r.Steps {
			fmt.Printf("  %d. %s\n", i+1, s)
		}
	default:
		fmt.Println("This machine now has its own storage key, and the key works.")
		if r.SecretsIn == "keychain" {
			fmt.Println("  Its secret is in this computer's keychain.")
		} else {
			fmt.Println("  Its secret is in MetaRush's .env file on this computer.")
		}
	}
	if len(r.SharedWith) > 0 && !r.AlreadyOwn {
		fmt.Printf("  The old key is also used by: %s.\n", strings.Join(r.SharedWith, ", "))
	}
	if details {
		fmt.Printf("  installation %s (%s)\n", r.InstallationID, r.Name)
		fmt.Printf("  old key %s; key provider %s\n", dash(r.OldKeyID), r.KeyProvider)
		if r.KeyID != "" || r.KeyLabel != "" {
			scope := r.KeyScope
			if r.KeyRole != "" {
				scope += ", " + r.KeyRole
			}
			fmt.Printf("  new key %s %s (%s) — round trip OK: %v\n", dash(r.KeyLabel), dash(r.KeyID), dash(scope), r.KeyVerified)
		}
		if r.OldSecretRemoved {
			fmt.Println("  this computer's keychain copy of the old secret was removed")
		}
	}
	for _, w := range r.Warnings {
		fmt.Println("  warning:", w)
	}
	if len(r.ManualSteps) > 0 {
		fmt.Println("\nManual steps:")
		for _, s := range r.ManualSteps {
			fmt.Println("  -", s)
		}
	}
	if len(r.NextSteps) > 0 && !r.AlreadyOwn {
		fmt.Println("\nNext:")
		for i, s := range r.NextSteps {
			fmt.Printf("  %d. %s\n", i+1, s)
		}
	}
}

func machineListCmd() *cobra.Command {
	var keys bool
	c := &cobra.Command{
		Use: "list", Short: "The machines of this workspace, when each was last seen, and their storage keys (never the secrets)",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "machine.list", ops.MachineListIn{CheckKeys: keys}, func(o any) { printMachines(o.(*machines.MachineList), detailsMode(c)) })
		},
	}
	c.Flags().BoolVar(&keys, "keys", false, "ask the storage provider whether each key still exists")
	return c
}

// machineStates says a machine's state in words.
var machineStates = map[string]string{models.InstallationInvited: "invited, not joined yet", models.InstallationActive: "joined", models.InstallationRevoked: "removed from the workspace"}

// printMachines lists the machines by name, with when each was last seen;
// --details adds the storage keys, database users and the provider.
func printMachines(l *machines.MachineList, details bool) {
	fmt.Printf("Machines in “%s”:\n", l.Workspace.Name)
	for _, m := range l.Machines {
		name := m.Name
		if m.Current {
			name += " (this machine)"
		}
		seen := "never seen"
		if m.LastSeenAt != nil {
			seen = "last seen " + m.LastSeenAt.Local().Format("Jan 2 15:04")
		}
		fmt.Printf("  %-30s %s, %s\n", name, orText(machineStates[m.State], m.State), seen)
		for _, w := range m.Warnings {
			fmt.Printf("      ! %s\n", w)
		}
	}
	if len(l.OtherKeys) > 0 && !details {
		fmt.Printf("\n%s with MetaRush's name %s not used by any machine.\n", plural(len(l.OtherKeys), "storage key"), map[bool]string{true: "is", false: "are"}[len(l.OtherKeys) == 1])
	}
	if l.CheckError != "" && !details {
		fmt.Println("\nMetaRush couldn't check the storage keys with the provider.")
	}
	if !details {
		fmt.Println("\n(Add --details to see each machine's storage key and database user.)")
		return
	}
	fmt.Printf("\nworkspace %s (%s) — key provider: %s\n\n", l.Workspace.Name, l.Workspace.ID, l.KeyProvider)
	fmt.Printf("  %-18s %-8s %-26s %-16s %-10s %-22s %s\n", "NAME", "STATE", "STORAGE KEY", "KEY ID", "KEY", "DB USER", "LAST SEEN")
	for _, m := range l.Machines {
		name := m.Name
		if m.Current {
			name += " *"
		}
		seen := "never"
		if m.LastSeenAt != nil {
			seen = m.LastSeenAt.Local().Format("2006-01-02 15:04")
		}
		fmt.Printf("  %-18s %-8s %-26s %-16s %-10s %-22s %s\n", name, m.State, dash(m.KeyLabel), dash(m.KeyID), m.KeyStatus, dash(m.DBUser), seen)
	}
	if len(l.OtherKeys) > 0 {
		fmt.Println("\nother metarush-* keys (not used by any machine):")
		for _, k := range l.OtherKeys {
			fmt.Printf("  %-26s %-16s %s created %s\n", k.Label, k.KeyID, k.Scope, k.CreatedAt.Local().Format("2006-01-02"))
		}
	}
	if l.CheckError != "" {
		fmt.Println("\nkey check skipped:", l.CheckError)
	} else if !l.KeysChecked {
		fmt.Println("\n* = this installation. Add --keys to check each key with the provider.")
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func machineInviteCmd() *cobra.Command {
	in := ops.MachineInviteIn{}
	c := &cobra.Command{
		Use:   "invite <name>",
		Short: "Invite a new machine: its own storage key (and database user) and a join file to set it up with",
		Long: `Run it on a machine that already belongs to the workspace. It makes a storage key named
metarush-<name> that can only reach this workspace's footage (or the whole bucket with
--key-scope bucket) and tests it, with --atlas also a MongoDB Atlas database user for this
database only (it uses your Atlas CLI sign-in), and writes everything to a private join
file. The secret is never shown. Move the join file safely and delete it after joining.

Storage without a key service (R2, B2, S3, Google Cloud interop, custom): the command says
how to make the key by hand; then run it again with --access-key-id and the secret in
METARUSH_MACHINE_S3_SECRET_ACCESS_KEY.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Name = args[0]
			return runOp(c, "machine.invite", in, func(o any) {
				r := o.(*machines.MachineInvite)
				fmt.Printf("Invited “%s”.\n", r.Name)
				if r.KeyID != "" {
					if r.KeyVerified {
						fmt.Println("  It has its own storage key, and the key works.")
					} else {
						fmt.Println("  It has its own storage key (not tested yet).")
					}
				}
				if r.DBUserCreated {
					fmt.Println("  It has its own database user, which can only use this workspace's database.")
				}
				fmt.Println("  Its join file holds passwords and keys: move it to the new machine safely, then delete it.")
				if detailsMode(c) {
					fmt.Printf("  installation %s\n", r.InstallationID)
					if r.KeyID != "" {
						scope := r.KeyScope
						if r.KeyRole != "" {
							scope += ", " + r.KeyRole
						}
						fmt.Printf("  storage key  %s (%s, %s scope) — round trip OK: %v\n", r.KeyLabel, r.KeyID, scope, r.KeyVerified)
					}
					if r.DBUserCreated {
						fmt.Printf("  database     user %s (readWrite on this database only)\n", r.DBUser)
					}
					fmt.Printf("  join bundle  %s (mode 0600 — contains credentials)\n", r.Bundle)
				}
				for _, w := range r.Warnings {
					fmt.Println("  warning:", w)
				}
				if len(r.ManualSteps) > 0 {
					fmt.Println("\nManual steps:")
					for _, s := range r.ManualSteps {
						fmt.Println("  -", s)
					}
				}
				fmt.Println("\nNext:")
				for i, s := range r.NextSteps {
					fmt.Printf("  %d. %s\n", i+1, s)
				}
			})
		},
	}
	f := c.Flags()
	f.StringVar(&in.Out, "out", "", "bundle path (default ~/.metarush-bundles/metarush-join-<name>.json; never overwritten, never in a sync folder or repository)")
	f.BoolVar(&in.AllowUnsafeSecretPath, "allow-unsafe-secret-path", false, "allow --out inside a git repository that ignores it (never inside a sync folder)")
	f.StringVar(&in.KeyScope, "key-scope", "auto", "auto | prefix (objects under the store prefix) | bucket (a bucket role)")
	f.StringVar(&in.Role, "role", "", "bucket role for --key-scope bucket: ReadWrite (default) | Editor | ReadOnly")
	f.BoolVar(&in.Atlas, "atlas", false, "also create an Atlas database user metarush-<name> (readWrite on this database only)")
	f.StringVar(&in.AtlasProfile, "atlas-profile", "", "Atlas CLI profile (default: the CLI's default profile)")
	f.StringVar(&in.AtlasProjectID, "atlas-project-id", "", "Atlas project (default: the one recorded at init)")
	f.StringVar(&in.AccessKeyID, "access-key-id", "", "use a key you created by hand; secret in "+machines.EnvManualSecret)
	return c
}

func machineRevokeCmd() *cobra.Command {
	in := ops.MachineRevokeIn{}
	c := &cobra.Command{
		Use:   "revoke <name|installation-id>",
		Short: "Remove a machine from the workspace: its storage key stops working at once (with --atlas its database user too)",
		Long: `Deletes the machine's storage key (it stops working at once), with --atlas also its Atlas
database user, and marks the machine removed (the audit log keeps who did it). It refuses
to remove the machine you are on, or a key another machine still uses, unless --force.
Treat what a lost machine kept on its own disk as exposed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Name = args[0]
			return runOp(c, "machine.revoke", in, func(o any) {
				r := o.(*machines.MachineRevoke)
				if r.AlreadyRevoked {
					fmt.Printf("“%s” was already removed from the workspace.\n", r.Name)
				} else {
					fmt.Printf("Removed “%s” from the workspace.\n", r.Name)
				}
				if r.KeyID != "" {
					fmt.Println("  " + map[bool]string{true: "Its storage key was deleted.", false: "Its storage key was not deleted."}[r.KeyDeleted])
				}
				if r.DBUser != "" {
					fmt.Println("  " + map[bool]string{true: "Its database user was deleted.", false: "Its database user was not deleted."}[r.DBUserDeleted])
				}
				if detailsMode(c) {
					fmt.Printf("  installation %s\n", r.InstallationID)
					if r.KeyID != "" {
						fmt.Printf("  storage key %s %s deleted: %v\n", dash(r.KeyLabel), r.KeyID, r.KeyDeleted)
					}
					if r.DBUser != "" {
						fmt.Printf("  database user %s deleted: %v\n", r.DBUser, r.DBUserDeleted)
					}
				}
				for _, w := range r.Warnings {
					fmt.Println("  warning:", w)
				}
				if len(r.ManualSteps) > 0 {
					fmt.Println("\nStill to do by hand:")
					for _, s := range r.ManualSteps {
						fmt.Println("  -", s)
					}
				}
			})
		},
	}
	f := c.Flags()
	f.BoolVar(&in.Atlas, "atlas", false, "also delete the machine's Atlas database user")
	f.StringVar(&in.AtlasProfile, "atlas-profile", "", "Atlas CLI profile")
	f.StringVar(&in.AtlasProjectID, "atlas-project-id", "", "Atlas project (default: the one recorded at init)")
	f.BoolVar(&in.Force, "force", false, "allow revoking this installation or a key other machines share")
	return c
}

func machineJoinCmd() *cobra.Command {
	o := &initOpts{}
	c := &cobra.Command{
		Use:   "join <bundle.json>",
		Short: "Set this machine up from the join file another machine made, then check it",
		Long: `Sets this machine up like ` + "`init --join`" + `, with the join file's workspace, database and this
machine's own storage key; passwords and keys go to the keychain (or --secrets env). If
the join file has no database password, set METARUSH_MONGODB_URI (this machine's own user).
Once the join and its checks succeed, the join file (it holds passwords and keys) is
deleted: after asking in a terminal, by itself otherwise; --keep-bundle keeps it.
A join file inside a synced folder, or where git would commit it, is refused.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error { return runJoin(c, args[0], o) },
	}
	f := c.Flags()
	f.StringVar(&o.secrets, "secrets", "keychain", "where secrets live: keychain | env")
	f.BoolVar(&o.allowUnsafeSecretPath, "allow-unsafe-secret-path", false, "with --secrets env: allow <home>/.env inside a repository that ignores it (never inside a sync folder)")
	f.BoolVar(&o.keepBundle, "keep-bundle", false, "do not delete the join bundle after a successful join")
	f.BoolVar(&o.replaceSaved, "replace-saved-database", false, "replace a database address this home already keeps for another database (the old one is kept aside)")
	f.StringVar(&o.userName, "user-name", "", "your name (default from git config)")
	f.StringVar(&o.userEmail, "user-email", "", "your email (default from git config)")
	f.StringVar(&o.cacheDir, "cache-dir", "", "local media cache directory")
	f.Float64Var(&o.cacheMaxGB, "cache-max-gb", 0, "cache size limit in GB")
	f.IntVar(&o.port, "port", 0, "dashboard port (default 4845)")
	f.StringVar(&o.videoDir, "video-dir", "", "developer setting: render from this source checkout's video/ package instead of this version's own renderer")
	f.StringVar(&o.watchDir, "watch-dir", "", "enable a watched inbox folder")
	f.BoolVar(&o.testDB, "test-db", false, "allow a throwaway metarush-test-* database on localhost (tests)")
	_ = f.MarkHidden("test-db")
	return c
}

func runJoin(c *cobra.Command, path string, o *initOpts) error {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	// A bundle in a sync folder has already been uploaded; one git would commit is at risk.
	if err := secretpath.CheckUse(path, "the join bundle"); err != nil {
		return failErr(c, err)
	}
	b, warns, err := machines.ReadBundle(path)
	if err != nil {
		return fail(c, apperr.InvalidInput, err.Error(), "")
	}
	h, err := home()
	if err != nil {
		return err
	}
	if config.Exists(h) {
		return failErr(c, apperr.New(apperr.Conflict, "MetaRush is already set up here.").
			WithHint("Add --home to join from a separate folder, or remove that setup first.").WithDetails(map[string]any{"home": h}))
	}
	say := func(f string, a ...any) {
		if !jsonMode(c) {
			fmt.Printf(f+"\n", a...)
		}
	}
	for _, w := range warns {
		say("Warning: %s", w)
	}
	uri := b.Database.URI
	if uri == "" {
		uri = os.Getenv(config.EnvPrefix + "MONGODB_URI")
	}
	if uri == "" && interactive() && !jsonMode(c) {
		say("The join file has no database password. Enter this machine's own MongoDB connection string.")
		if detailsMode(c) {
			say("(host %s, database %s)", b.Database.Host, b.Database.Name)
		}
		uri = askSecret("MongoDB connection string")
	}
	if uri == "" {
		return fail(c, apperr.InvalidInput, "The join file has no database password.", "Set METARUSH_MONGODB_URI to this machine's own database user (the invitation's manual steps say how), then run this again.")
	}
	if b.Database.Host != "" && config.HostOf(uri) != b.Database.Host {
		say("Warning: that database isn't the one the workspace uses.")
		if detailsMode(c) {
			say("  the database host %s differs from the workspace's %s", config.HostOf(uri), b.Database.Host)
		}
	}
	if b.Database.URI != "" {
		if err := waitForDB(c.Context(), uri, b.Database.Name, 3*time.Minute, say); err != nil {
			return failErr(c, apperr.Wrap(apperr.Database, err, "MetaRush couldn't connect with the database user from the join file.").
				WithHint("A new Atlas user can take a minute to start working; also check that this machine's network is on the Atlas access list."))
		}
	}
	o.join, o.dbURI, o.dbName = b.Workspace.ID, uri, b.Database.Name
	o.atlasOrgID, o.atlasOrgName, o.atlasProjectID = b.Database.AtlasOrgID, b.Database.AtlasOrgName, b.Database.AtlasProjectID
	o.installName, o.installID, o.yes = b.Installation.Name, b.Installation.ID, true
	o.mediaProvider = b.Media.Provider
	switch b.Media.Provider {
	case "s3":
		s := b.Media.S3
		o.s3Preset, o.s3Endpoint, o.s3Region, o.s3Bucket, o.s3Prefix = s.Preset, s.Endpoint, s.Region, s.Bucket, s.Prefix
		o.s3AccessKeyID, o.s3Secret, o.s3PathStyle = s.AccessKeyID, s.SecretAccessKey, s.ForcePathStyle
	case "filesystem":
		o.mediaRoot, o.mediaShared = b.Media.Filesystem.Root, b.Media.Filesystem.Shared
	}
	result, err := initialize(c, o)
	if err != nil {
		return err
	}
	say("\nChecking this machine…")
	checks := runDoctor(c.Context())
	ok := true
	for _, ch := range checks {
		if ch.Status == "fail" {
			ok = false
		}
	}
	var ask func() bool
	printed := false
	if interactive() && !jsonMode(c) {
		ask = func() bool {
			printChecks(checks, detailsMode(c))
			printed = true
			return askYes("\nJoined. Delete the join file now? It holds this machine's passwords and keys", true)
		}
	}
	deleted, delErr := finishJoinBundle(path, ok, o.keepBundle, ask)
	out := map[string]any{"ok": ok, "init": result, "checks": checks, "warnings": warns, "bundle": path, "bundleDeleted": deleted}
	if delErr != nil {
		out["bundleDeleteError"] = delErr.Error()
	}
	err = emit(c, out, func() {
		if !printed {
			printChecks(checks, detailsMode(c))
		}
		next := func() {
			fmt.Println("Next:")
			fmt.Println("  metarush skills install --user  # let Claude Code use the video skills")
			fmt.Println("  metarush dashboard              # open the dashboard")
		}
		switch {
		case ok && deleted:
			fmt.Println("\nJoined. The join file was deleted; delete the copy on the machine that invited this one too.")
			next()
		case ok:
			if delErr != nil {
				fmt.Println("\nJoined, but the join file couldn't be deleted. Delete it yourself: it holds passwords and keys.")
			} else {
				fmt.Println("\nJoined. Delete the join file as soon as you can: it holds passwords and keys.")
			}
			if detailsMode(c) {
				if delErr != nil {
					fmt.Printf("error: %v\n", delErr)
				}
				fmt.Printf("delete it: rm %s\n", shellQuote(path))
			}
			next()
		default:
			fmt.Println("\nJoined, but some checks failed: see the steps above. The join file was kept; delete it once they pass.")
			if !detailsMode(c) {
				fmt.Println(checksFooter(checks))
			}
		}
	})
	if err == nil && !ok {
		return &exitError{code: 1}
	}
	return err
}

// finishJoinBundle deletes a join bundle (it holds credentials) once a join and
// its checks succeeded: after asking in a terminal (ask, default yes),
// automatically otherwise; never with --keep-bundle or when checks failed.
func finishJoinBundle(path string, ok, keep bool, ask func() bool) (bool, error) {
	if !ok || keep {
		return false, nil
	}
	if ask != nil && !ask() {
		return false, nil
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}

func waitForDB(ctx context.Context, uri, name string, max time.Duration, say func(string, ...any)) error {
	deadline := time.Now().Add(max)
	var last error
	for i := 0; ; i++ {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		db, err := store.Connect(cctx, uri, name)
		cancel()
		if err == nil {
			_ = db.Close(context.Background())
			return nil
		}
		last = err
		if time.Now().After(deadline) {
			return fmt.Errorf("could not connect with the bundle's database user: %w", redactErr(last, uri))
		}
		if i == 0 {
			say("Waiting for the new database user to become active (this can take a minute)…")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// redactErr removes a URI's password from an error message.
func redactErr(err error, uri string) error {
	if u, perr := url.Parse(uri); perr == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok && pw != "" && strings.Contains(err.Error(), pw) {
			return errors.New(strings.ReplaceAll(err.Error(), pw, "REDACTED"))
		}
	}
	return err
}

func shellQuote(p string) string {
	if strings.ContainsAny(p, " '\"$`\\") {
		return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
	}
	return p
}

func machineCIKeyCmd() *cobra.Command {
	var prefix, out string
	var replace, allowUnsafe bool
	c := &cobra.Command{
		Use:   "ci-key",
		Short: "Mint the live-bucket CI key (only bucket/ci-test/*) into a 0600 dotenv file for CI secrets",
		Long: `Mints a key labeled metarush-ci that can only reach <bucket>/<prefix>/* (an IAM
policy; no bucket role, no organization-wide operations), proves it with a round trip,
and writes METARUSH_TEST_S3_{BUCKET,ENDPOINT,REGION,ACCESS_KEY_ID,SECRET_ACCESS_KEY} to
a new 0600 file. Load it into GitHub Actions without echoing anything:

  gh secret set -f <file> --repo <owner>/<repo> && rm <file>

--replace deletes older metarush-ci keys (and their policies) after the new key works.
Tigris only; other providers: create an equivalent key by hand.`,
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			if out == "" {
				dir, err := secretpath.DefaultDir()
				if err != nil {
					return failErr(c, err)
				}
				out = filepath.Join(dir, "metarush-ci-s3.env")
			}
			if out, err = filepath.Abs(out); err != nil {
				return err
			}
			if _, err := os.Stat(out); err == nil {
				return failErr(c, apperr.New(apperr.Conflict, "That file already exists, and MetaRush never replaces it.").
					WithHint("Move or delete it, or choose another file with --out.").WithDetails(map[string]any{"file": out}))
			}
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			k, err := machines.MintCIKey(ctx, a, prefix, out, replace, allowUnsafe)
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, k, func() {
				fmt.Printf("CI key %s (%s) limited to %s/%s/* — round trip OK\n", k.Label, k.KeyID, k.Bucket, k.Prefix)
				fmt.Printf("credentials written to %s (mode 0600)\n", k.File)
				for _, r := range k.Replaced {
					fmt.Printf("deleted the previous CI key %s\n", r)
				}
				fmt.Printf("\nNext: gh secret set -f %s --repo <owner>/<repo> && rm %s\n", shellQuote(k.File), shellQuote(k.File))
			})
		},
	}
	c.Flags().StringVar(&prefix, "prefix", machines.DefaultCIPrefix, "bucket prefix the key may use (TestS3Live writes under ci-test/)")
	c.Flags().StringVar(&out, "out", "", "dotenv file to create (default ~/.metarush-bundles/metarush-ci-s3.env; never overwritten, never in a sync folder or repository)")
	c.Flags().BoolVar(&allowUnsafe, "allow-unsafe-secret-path", false, "allow --out inside a git repository that ignores it (never inside a sync folder)")
	c.Flags().BoolVar(&replace, "replace", false, "delete older metarush-ci keys after the new one works")
	return c
}
