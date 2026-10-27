package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/metavert-io/metarush/internal/cache"
	"github.com/metavert-io/metarush/internal/ops"
)

func cacheCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "cache", Short: "This machine's copy of your footage (MetaRush's storage keeps the real one): status, download for offline, free space, repair, keep"}
	cmd.AddCommand(&cobra.Command{
		Use: "status", Short: "How much this machine's copy uses, what is kept here for offline work, and how it is kept under its limit",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "cache.status", ops.CacheStatusIn{}, func(o any) { printCacheStats(*o.(*cache.Stats), detailsMode(c)) })
		},
	})
	var dry bool
	var targetGB float64
	prune := &cobra.Command{
		Use: "prune", Short: "Free space: remove what was used longest ago from this machine's copy (never what is kept for offline work or in use; MetaRush's storage keeps everything)",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "cache.prune", ops.CachePruneIn{ToGB: targetGB, DryRun: dry}, func(o any) {
				r := o.(*cache.PruneResult)
				verb := map[bool]string{true: "Would remove", false: "Removed"}[dry]
				fmt.Printf("%s %s (%s) from this machine's copy; it %s %s now. MetaRush's storage keeps everything.\n", verb, plural(r.Removed, "file"), fmtBytes(r.FreedBytes),
					map[bool]string{true: "would use", false: "uses"}[dry], fmtBytes(r.AfterBytes))
				if r.SkippedPinned > 0 || r.SkippedInUse > 0 {
					fmt.Printf("Left alone: %d kept here for offline work, %d in use.\n", r.SkippedPinned, r.SkippedInUse)
				}
				if detailsMode(c) {
					fmt.Printf("%s %d object(s), %s (cache %s → %s; kept %d pinned, %d in use)\n", strings.ToLower(verb), r.Removed, fmtBytes(r.FreedBytes),
						fmtBytes(r.BeforeBytes), fmtBytes(r.AfterBytes), r.SkippedPinned, r.SkippedInUse)
				}
			})
		},
	}
	prune.Flags().BoolVar(&dry, "dry-run", false, "only say what would be removed")
	prune.Flags().Float64Var(&targetGB, "to-gb", 0, "free space down to this size (default: the limit in the settings)")
	cmd.AddCommand(prune)
	cmd.AddCommand(prefetchCmd(), repairCmd(), pinCmd())
	return cmd
}

func printCacheStats(st cache.Stats, details bool) {
	fmt.Printf("This machine's copy uses %s of its %s (%s).\n", fmtBytes(st.Bytes), fmtBytes(st.MaxBytes), plural(st.Objects, "file"))
	var kept []string
	if st.Pinned > 0 {
		kept = append(kept, fmt.Sprintf("%s kept here for offline work (%s)", plural(st.Pinned, "file"), fmtBytes(st.PinnedBytes)))
	}
	if st.InUse > 0 {
		kept = append(kept, fmt.Sprintf("%d in use", st.InUse))
	}
	if st.Partials > 0 {
		kept = append(kept, fmt.Sprintf("%s still downloading or stopped part way (%s)", plural(st.Partials, "file"), fmtBytes(st.PartialBytes)))
	}
	if len(kept) > 0 {
		fmt.Println(sentence(strings.Join(kept, ", ")))
	}
	q := st.Quota
	switch {
	case q == nil:
		fmt.Println("It is kept under its limit by itself while MetaRush runs in the background.")
	case q.Enforced:
		fmt.Printf("MetaRush keeps it under its limit, checking every %s (last check %s ago).\n", time.Duration(q.IntervalSeconds)*time.Second, time.Since(q.LastCheckAt).Round(time.Second))
	default:
		fmt.Println("Nothing is keeping it under its limit right now: MetaRush isn't running in the background.")
	}
	if q != nil && q.OverQuota {
		fmt.Println("  It is over its limit: what is kept here for offline work or in use can't be removed.")
	}
	if q != nil && q.LastPrune != nil {
		r := q.LastPrune
		fmt.Printf("  Last made room %s ago: %s removed, %s freed.\n", time.Since(r.At).Round(time.Second), plural(r.Removed, "file"), fmtBytes(r.FreedBytes))
	}
	if !details {
		return
	}
	fmt.Printf("\n%s\n%d objects, %s used of %s (%d pinned / %s, %d in use, %d partial downloads / %s)\n", st.Dir, st.Objects, fmtBytes(st.Bytes), fmtBytes(st.MaxBytes),
		st.Pinned, fmtBytes(st.PinnedBytes), st.InUse, st.Partials, fmtBytes(st.PartialBytes))
	switch {
	case q == nil:
		fmt.Println("quota: not enforced yet — the daemon (`metarush dashboard`/`serve`) or `worker start` prunes to cache.maxGB automatically")
	case q.Enforced:
		fmt.Printf("quota: enforced by pid %d every %s; last check %s ago\n", q.EnforcerPID, time.Duration(q.IntervalSeconds)*time.Second, time.Since(q.LastCheckAt).Round(time.Second))
	default:
		fmt.Printf("quota: %s\n", q.Note)
	}
	if q != nil && q.OverQuota {
		fmt.Printf("  over the limit: %s\n", q.Note)
	}
}

// progressPrinter renders byte progress on stderr: an updating line on a
// terminal, one line per item otherwise.
type progressPrinter struct {
	mu    sync.Mutex
	label string
	last  time.Time
	tty   bool
	off   bool
}

func newProgress(c *cobra.Command) *progressPrinter {
	return &progressPrinter{tty: term.IsTerminal(int(os.Stderr.Fd())), off: jsonMode(c)}
}

func (p *progressPrinter) update(label string, done, total int64) {
	if p.off {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fresh := label != p.label
	finished := total > 0 && done >= total
	if !fresh && !finished && time.Since(p.last) < 250*time.Millisecond {
		return
	}
	p.last = time.Now()
	pct := ""
	if total > 0 {
		pct = fmt.Sprintf("%3.0f%% %s/%s", 100*float64(done)/float64(total), fmtBytes(done), fmtBytes(total))
	} else {
		pct = fmtBytes(done)
	}
	switch {
	case p.tty:
		if fresh && p.label != "" {
			fmt.Fprintln(os.Stderr)
		}
		fmt.Fprintf(os.Stderr, "\r\033[K  %s  %s", label, pct)
	case fresh || finished:
		fmt.Fprintf(os.Stderr, "  %s  %s\n", label, pct)
	}
	p.label = label
}

func (p *progressPrinter) done() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tty && !p.off && p.label != "" {
		fmt.Fprintln(os.Stderr)
	}
	p.label = ""
}

// prefetchWords say what happened to each file a prefetch looked at.
var prefetchWords = map[string]string{"cached": "already here", "downloaded": "downloaded", "resumed": "finished downloading", "would-download": "to download", "failed": "failed", "skipped": "skipped"}

func prefetchCmd() *cobra.Command {
	in := ops.PrefetchIn{}
	cmd := &cobra.Command{
		Use:   "prefetch (--revision <id> | --project <id>) [--pin]",
		Short: "Download what a render or review needs (footage, preview copies, the latest render) so this machine works offline",
		Long: `Downloads every file a render of this version of the cut (or of any version of the project)
reads on this machine: the original footage (or its steady copy, for footage that changes its
frame rate while it plays), preview copies, thumbnails, waveforms and the latest finished
render. Each file is checked against its fingerprint. Stop it at any time: running it again
finishes what was half downloaded. --pin keeps the files here when space is freed.`,
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			pp := newProgress(c)
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			ctx = ops.WithProgress(ctx, pp.update)
			b, _ := json.Marshal(in)
			o, err := ops.Call(ctx, a, "cache.prefetch", b, ops.Caller{})
			pp.done()
			if err != nil {
				return failErr(c, err)
			}
			out := o.(*ops.PrefetchOut)
			err = emit(c, out, func() {
				what := "This version of the cut"
				if in.RevisionID == "" {
					what = "The project"
				}
				if out.DryRun {
					fmt.Printf("%s needs %s (%s): %d already here, %d to download.\n", what, plural(out.Objects, "file"), fmtBytes(out.TotalBytes), out.AlreadyCached, out.Objects-out.AlreadyCached)
				} else {
					fmt.Printf("%s needs %s (%s): %d already here, %d downloaded, %d finished downloading, %d failed, %d skipped", what, plural(out.Objects, "file"), fmtBytes(out.TotalBytes),
						out.AlreadyCached, out.Downloaded, out.Resumed, out.Failed, out.Skipped)
					if out.Pinned > 0 {
						fmt.Printf(", %d kept here for offline work", out.Pinned)
					}
					fmt.Println(".")
				}
				if out.OfflineReady && !out.DryRun {
					fmt.Println("This machine can work on it offline now.")
				}
				for _, it := range out.Items {
					if it.Status == "failed" || it.Status == "skipped" || (in.DryRun && it.Status == "would-download") {
						if detailsMode(c) {
							fmt.Printf("  %-14s %-16s %-36s %s %s\n", it.Status, it.Role, trunc(it.Name, 36), fmtBytes(it.Size), it.Error)
						} else {
							fmt.Printf("  %-20s %-14s %-36s %s\n", orText(prefetchWords[it.Status], it.Status), orText(fileWords[it.Role], it.Role), trunc(it.Name, 36), fmtBytes(it.Size))
						}
					}
				}
				if out.Note != "" {
					fmt.Println(out.Note)
				}
				if detailsMode(c) {
					fmt.Printf("%s: %d object(s), %s — %d already cached, %d downloaded, %d resumed, %d failed, %d skipped, %d pinned\n", out.Scope, out.Objects, fmtBytes(out.TotalBytes),
						out.AlreadyCached, out.Downloaded, out.Resumed, out.Failed, out.Skipped, out.Pinned)
				}
			})
			if err == nil && (out.Failed > 0 || out.Interrupted) {
				return &exitError{code: 1}
			}
			return err
		},
	}
	cmd.Flags().StringVar(&in.RevisionID, "revision", "", "the version of the cut to get ready for offline work")
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "the project to get ready for offline work and review")
	cmd.Flags().BoolVar(&in.Pin, "pin", false, "keep the files here when space is freed")
	cmd.Flags().BoolVar(&in.DryRun, "dry-run", false, "only say what would be downloaded")
	return cmd
}

func repairCmd() *cobra.Command {
	in := ops.CacheRepairIn{}
	cmd := &cobra.Command{
		Use:   "repair [--dry-run]",
		Short: "Check every file in this machine's copy, remove damaged ones and unfinished downloads, and download again what is kept for offline work",
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			pp := newProgress(c)
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			var lastCount time.Time
			ctx = ops.WithProgress(ctx, func(label string, done, total int64) {
				if strings.HasPrefix(label, "verifying") { // object counts, not bytes
					if pp.off || (!pp.tty && done != total && done%50 != 1) || (pp.tty && time.Since(lastCount) < 200*time.Millisecond && done != total) {
						return
					}
					lastCount = time.Now()
					if pp.tty {
						fmt.Fprintf(os.Stderr, "\r\033[K  %s", label)
					} else {
						fmt.Fprintf(os.Stderr, "  %s\n", label)
					}
					return
				}
				pp.update(label, done, total)
			})
			b, _ := json.Marshal(in)
			o, err := ops.Call(ctx, a, "cache.repair", b, ops.Caller{})
			if pp.tty && !pp.off {
				fmt.Fprintln(os.Stderr)
			}
			if err != nil {
				return failErr(c, err)
			}
			r := o.(*cache.RepairResult)
			err = emit(c, r, func() {
				verb := map[bool]string{true: "to remove", false: "removed"}[in.DryRun]
				fmt.Printf("Checked %s (%s): %d fine, %d damaged (%s), %d unfinished downloads (%s).\n", plural(r.Checked, "file"), fmtBytes(r.CheckedBytes), r.OK, r.Corrupt, verb, r.Partials, verb)
				if r.PinnedMissing > 0 {
					fmt.Printf("%s kept here for offline work %s missing: %d downloaded again, %d failed.\n", plural(r.PinnedMissing, "file"), map[bool]string{true: "was", false: "were"}[r.PinnedMissing == 1], r.Refetched, r.RefetchFailed)
				}
				if detailsMode(c) {
					dverb := map[bool]string{true: "would remove", false: "removed"}[in.DryRun]
					fmt.Printf("checked %d object(s) (%s) in %.1fs: %d ok, %d corrupt (%s), %d partial leftover(s) %s (%s), %d stray file(s), %d partial(s) in use left alone\n",
						r.Checked, fmtBytes(r.CheckedBytes), r.DurationSecond, r.OK, r.Corrupt, dverb, r.Partials, dverb, fmtBytes(r.PartialBytes), r.Strays, r.SkippedInUse)
					for _, p := range r.Problems {
						if p.Problem == "corrupt" || p.Action == "refetch-failed" {
							fmt.Printf("  %-15s %-14s %s %s\n", p.Problem, p.Action, p.Path, p.Error)
						}
					}
				}
			})
			if err == nil && r.RefetchFailed > 0 {
				return &exitError{code: 1}
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&in.DryRun, "dry-run", false, "only say what is wrong; change nothing")
	return cmd
}

func pinCmd() *cobra.Command {
	return &cobra.Command{
		Use: "pin <asset-id>...", Short: "Download clips and keep them on this machine for offline work", Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			var pinned []string
			for _, id := range args {
				b, _ := json.Marshal(ops.AssetShowIn{ID: id})
				out, err := ops.Call(c.Context(), a, "asset.show", b, ops.Caller{})
				if err != nil {
					return failErr(c, err)
				}
				as := out.(*ops.AssetShowOut).Asset
				if _, err := a.Cache.Ensure(c.Context(), a.Media, as.StorageKey, as.SHA256, as.Size, nil); err != nil {
					return failErr(c, err)
				}
				shas := []string{as.SHA256}
				for _, v := range as.Variants {
					if _, err := a.Cache.Ensure(c.Context(), a.Media, v.StorageKey, v.SHA256, v.Size, nil); err == nil {
						shas = append(shas, v.SHA256)
					}
				}
				if err := a.Cache.Pin(shas...); err != nil {
					return failErr(c, err)
				}
				pinned = append(pinned, id)
			}
			return emit(c, map[string]any{"pinned": pinned}, func() { fmt.Printf("Kept %s on this machine for offline work.\n", plural(len(pinned), "clip")) })
		},
	}
}
