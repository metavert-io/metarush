package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

func storageCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "storage", Short: "Where this workspace's footage is kept (MetaRush's storage)"}
	var to, dashboardMove string
	var dryRun, readBack, details bool
	mig := &cobra.Command{
		Use:   "migrate --to <store-config> [--dry-run]",
		Short: "Move the footage to another storage (a cloud bucket, Google Cloud Storage or a folder): checked, can be resumed, nothing deleted",
		Long: `Copies every file of this workspace's footage to another storage, then switches this computer and the
workspace to it. --to names a YAML or JSON file with the keys of config.yaml's media section:

    provider: gcs
    gcs:
        bucket: my-media
        credentialsFile: ~/keys/metarush-media.json

    provider: s3
    s3:
        preset: r2
        endpoint: https://<account>.r2.cloudflarestorage.com
        bucket: my-media
        accessKeyId: <key id>
        secretAccessKey: ${NEW_MEDIA_SECRET}    # or keychain:<name>, never the secret itself

    provider: filesystem
    filesystem:
        root: /Volumes/Studio/MetaRush media
        shared: true

Each file is checked against its fingerprint before it is written and by the new storage as it arrives
(--read-back also downloads every file again and checks it). Files this computer already has are not
downloaded. Stop it at any time: running it again continues where it stopped. Nothing switches until every
file is in the new storage, the switch is tested the way MetaRush loads its settings (and undone if it
doesn't work), and the old storage is never changed or deleted. MetaRush running in the background here
restarts to use the new storage; what it added meanwhile is copied after. Other machines run the same move
with their own keys.

--dry-run only lists both storages and says what the move would copy.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if dashboardMove != "" {
				return runDashboardMove(c, dashboardMove)
			}
			if to == "" {
				return fail(c, apperr.InvalidInput, "Name the new storage: --to <file describing it>.", "Its --help has examples.")
			}
			if dryRun {
				return runOp(c, "storage.plan", ops.StoragePlanIn{To: to}, func(o any) { printStoragePlan(o.(*ops.StoragePlanOut), details) })
			}
			if !interactive() {
				return fail(c, apperr.HumanOnly, "Moving the footage must be done by a person.", "Run it yourself in a terminal; a dry run first says what it would copy.")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			b, _ := json.Marshal(ops.StoragePlanIn{To: to})
			po, err := ops.Call(ctx, a, "storage.plan", b, ops.Caller{Human: true})
			if err != nil {
				return failErr(c, err)
			}
			plan := po.(*ops.StoragePlanOut)
			if plan.Nothing {
				return emit(c, plan, func() { printStoragePlan(plan, details) })
			}
			if !jsonMode(c) {
				printStoragePlan(plan, details)
			}
			if ask(`Type "move" to go ahead`, "") != "move" {
				return fail(c, apperr.Canceled, "Nothing was moved.", "")
			}
			pp := newProgress(c)
			ctx = ops.WithProgress(ctx, pp.update)
			b, _ = json.Marshal(ops.StorageMigrateIn{To: to, ReadBack: readBack})
			out, err := ops.Call(ctx, a, "storage.migrate", b, ops.Caller{Human: true})
			pp.done()
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() { printStorageMove(out.(*ops.StorageMigrateOut), details) })
		},
	}
	mig.Flags().StringVar(&to, "to", "", "a YAML or JSON file describing the new storage (the keys of config.yaml's media section)")
	mig.Flags().BoolVar(&dryRun, "dry-run", false, "only say what the move would copy; change nothing")
	mig.Flags().BoolVar(&readBack, "read-back", false, "also download every file from the new storage again and check it (slower; may cost downloads)")
	mig.Flags().BoolVar(&details, "details", false, "also show where each storage is, and every file that could not be moved")
	// The process the dashboard starts for a move a person confirmed there
	// (D112); it needs that move's one-time grant on stdin.
	mig.Flags().StringVar(&dashboardMove, "dashboard-move", "", "a move confirmed in the dashboard (started by the dashboard itself)")
	_ = mig.Flags().MarkHidden("dashboard-move")
	cmd.AddCommand(mig)

	var statusDetails bool
	status := &cobra.Command{
		Use:   "status",
		Short: "Say which storage this computer uses, and whether the workspace moved to another one (then switch this computer too)",
		Long: `Says where this computer keeps the media and, when another computer moved the workspace's media
to another storage, that this one still uses the old storage: move it too, with this computer's own
keys (Settings → Storage, or storage migrate --to). Also says whether a move runs here and how the
last move started from the dashboard went.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "storage.status", ops.StorageStatusIn{}, func(o any) { printStorageStatus(o.(*ops.StorageStatusOut), statusDetails) })
		},
	}
	status.Flags().BoolVar(&statusDetails, "details", false, "also show where each storage is")
	cmd.AddCommand(status)
	return cmd
}

// runDashboardMove is the process the dashboard starts for a move a person
// confirmed there (D112): it takes the move's one-time grant from stdin, moves
// the media like storage migrate does (the activity bar shows it) and records
// how it went for Settings → Storage. It writes to the move's log file.
func runDashboardMove(c *cobra.Command, id string) error {
	if assistantMode() {
		return fail(c, apperr.HumanOnly, "Moving the footage must be done by a person.", "")
	}
	token, _ := bufio.NewReader(io.LimitReader(os.Stdin, 256)).ReadString('\n')
	a, err := openApp(c, "dashboard")
	if err != nil {
		return err
	}
	defer a.Close()
	mv, path, err := ops.ClaimDashboardMove(a, id, token)
	if err != nil {
		return failErr(c, err)
	}
	ctx, cancel := signalCtx(c.Context())
	defer cancel()
	fmt.Printf("%s moving the media to %s\n", time.Now().Format(time.RFC3339), mv.To.Name)
	out, err := ops.CallTyped[ops.StorageMigrateOut](ctx, a, "storage.migrate", ops.StorageMigrateIn{To: path, ReadBack: mv.ReadBack}, ops.Caller{Human: true})
	ops.FinishDashboardMove(a, id, out, err)
	if err != nil {
		return failErr(c, err)
	}
	fmt.Printf("%s ", time.Now().Format(time.RFC3339))
	printStorageMove(out, true)
	return nil
}

func printStorageStatus(s *ops.StorageStatusOut, details bool) {
	fmt.Println(s.Summary)
	if s.Moving && s.Progress != "" {
		fmt.Println("  " + upperFirst(s.Progress))
	}
	if mv := s.Move; mv != nil && !s.Moving && mv.Summary != "" && mv.Summary != s.Summary {
		fmt.Println("The last move started in the dashboard: " + mv.Summary)
		for _, n := range mv.Next {
			fmt.Println("  - " + n)
		}
	}
	if s.SwitchMeToo {
		fmt.Println("Switch it in Settings → Storage, or move it from a terminal with this computer's own keys (storage migrate).")
	}
	if details {
		fmt.Printf("This computer: %s (%s)\n", s.This.Name, s.This.Locator)
		if s.Workspace != nil {
			fmt.Printf("The workspace: %s (%s)\n", s.Workspace.Name, s.Workspace.Locator)
		}
		if s.JobID != "" {
			fmt.Println("Job:", s.JobID)
		}
	}
}

func printStoragePlan(p *ops.StoragePlanOut, details bool) {
	fmt.Println(p.Summary)
	for _, n := range p.Notes {
		fmt.Println("  " + n)
	}
	if details {
		fmt.Printf("From: %s (%s)\nTo:   %s (%s)\n", p.From.Name, p.From.Locator, p.To.Name, p.To.Locator)
		fmt.Printf("Files: %d (%s), there already %d, to copy %d (%s), on this computer %d, to download %s\n",
			p.Files, fmtBytes(p.Bytes), p.Present, p.ToCopy, fmtBytes(p.ToCopyBytes), p.Local, fmtBytes(p.Download))
		for _, k := range p.Differ {
			fmt.Println("  differs:", k)
		}
	}
}

func printStorageMove(r *ops.StorageMigrateOut, details bool) {
	fmt.Println(r.Summary)
	if len(r.Next) > 0 {
		fmt.Println("Next:")
		for _, n := range r.Next {
			fmt.Println("  - " + n)
		}
	}
	if details {
		fmt.Printf("From: %s (%s)\nTo:   %s (%s)\n", r.From.Name, r.From.Locator, r.To.Name, r.To.Locator)
		fmt.Printf("Files: %d (%s); copied %d (%d this run), there already %d, read back %d, downloaded %s; %.0fs; job %s\n",
			r.Files, fmtBytes(r.Bytes), r.Copied, r.CopiedNow, r.Present, r.ReadBack, fmtBytes(r.Downloaded), r.Seconds, r.JobID)
		for _, p := range r.Problems {
			fmt.Printf("  %s: %s (%s)\n", p.Reason, p.Key, p.Detail)
		}
	}
}
