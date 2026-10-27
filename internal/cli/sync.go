package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ops"
)

func syncCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "sync", Short: "What this machine has a copy of, and what only MetaRush's storage has"}
	var in ops.SyncStatusIn
	var all bool
	status := &cobra.Command{
		Use:   "status [--project <id>]",
		Short: "What this machine has a copy of, imports still uploading, jobs, which machines run MetaRush, and whether the database and storage answer",
		Long: `Says, for each clip's files and each render, whether this machine has a checked copy (kept
here for offline work, or in use) or only MetaRush's storage does; imports that are
uploading, were interrupted or failed; jobs waiting or running; which machines run MetaRush;
and how fast the database and the storage answer. Works without the database too (then it
says only what this machine has).`,
		RunE: func(c *cobra.Command, _ []string) error {
			h, err := home()
			if err != nil {
				return err
			}
			cfg, err := config.Load(h)
			if err != nil {
				return notSetUp(c, h, err)
			}
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			octx, ocancel := context.WithTimeout(ctx, 20*time.Second)
			a, err := app.OpenWith(octx, cfg, "cli")
			ocancel()
			var out *ops.SyncStatusOut
			if err != nil {
				// No database: still report what this machine has and can reach.
				oa, oerr := app.OpenOffline(cfg, "cli")
				if oerr != nil {
					return failErr(c, oerr)
				}
				out, err = ops.SyncStatusOffline(ctx, oa, &in, err)
			} else {
				defer a.Close()
				b, _ := json.Marshal(in)
				var o any
				o, err = ops.Call(ctx, a, "sync.status", b, ops.Caller{})
				if err == nil {
					out = o.(*ops.SyncStatusOut)
				}
			}
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() { printSync(out, all, detailsMode(c)) })
		},
	}
	status.Flags().StringVar(&in.ProjectID, "project", "", "limit to one project")
	status.Flags().BoolVar(&all, "all", false, "list every clip, not only those this machine lacks a copy of")
	cmd.AddCommand(status)
	return cmd
}

// fileWords names a clip's stored files the way people know them.
var fileWords = map[string]string{"original": "original", "proxy": "preview copy", "mezzanine": "steady copy", "thumbnail": "thumbnail",
	"render_thumbnail": "thumbnail", "peaks": "waveform", "waveform": "waveform", "contact_sheet": "contact sheet", "render": "render"}

// ingestWords says what an unfinished import is doing.
var ingestWords = map[string]string{"active": "uploading", "interrupted": "interrupted", "failed": "failed"}

func printSync(o *ops.SyncStatusOut, all, details bool) {
	for _, s := range o.Summary {
		fmt.Println(s)
	}
	if len(o.Assets) > 0 {
		fmt.Println("\nClips (● on this machine  ○ only in MetaRush's storage  ◐ partly here  P kept here for offline work):")
	}
	shown := 0
	for _, as := range o.Assets {
		if !all && as.CanonicalOnlyBytes == 0 {
			continue
		}
		if shown++; shown > 50 && !all {
			fmt.Println("  … and more.")
			if details {
				fmt.Println("  list every clip: metarush sync status --all")
			}
			break
		}
		var parts []string
		for _, ob := range as.Objects {
			mark := "○"
			switch {
			case ob.Cached:
				mark = "●"
			case ob.PartialBytes > 0:
				mark = "◐"
			}
			if ob.Pinned {
				mark += "P"
			}
			role := ob.Role
			if !details {
				role = orText(fileWords[ob.Role], ob.Role)
			}
			parts = append(parts, fmt.Sprintf("%s%s %s", mark, role, fmtBytes(ob.Size)))
		}
		if as.State != "available" {
			parts = append(parts, "("+as.State+")")
		}
		if details {
			fmt.Printf("  %s  %-32s %s\n", as.ID, trunc(as.Name, 32), strings.Join(parts, "  "))
		} else {
			fmt.Printf("  %-32s %s\n", trunc(as.Name, 32), strings.Join(parts, "  "))
		}
	}
	if !all && shown == 0 && len(o.Assets) > 0 {
		fmt.Printf("  This machine has a copy of everything in all %s.\n", plural(len(o.Assets), "clip"))
	}
	for _, s := range o.Ingest {
		pct := 0.0
		if s.Size > 0 {
			pct = 100 * float64(s.BytesDone) / float64(s.Size)
		}
		if details {
			fmt.Printf("ingest %s %-11s %-32s %3.0f%% %s\n", s.ID, s.Status, trunc(s.SourceName, 32), pct, s.Error)
		} else {
			fmt.Printf("Adding %-32s %-11s %3.0f%%\n", trunc(s.SourceName, 32), orText(ingestWords[s.Status], s.Status), pct)
		}
	}
	for _, j := range o.Jobs.Jobs {
		w := ""
		if j.Worker != "" {
			w = "on " + j.Worker
		}
		if details {
			fmt.Printf("job %s %-8s %-13s %3.0f%% %s %s\n", j.ID, j.State, j.Type, j.Pct*100, j.Message, w)
		} else {
			state := map[string]string{"queued": "waiting", "running": "running"}[j.State]
			fmt.Printf("%-8s %-30s %3.0f%% %s %s\n", orText(state, j.State), trunc(ops.JobKindLabel(j.Type), 30), j.Pct*100, j.Message, w)
		}
	}
}
