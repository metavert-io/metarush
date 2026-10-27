package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/fixes"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/sources"
)

// Descriptive names, info cards and fixes (D150–D153): `metarush asset names`,
// `asset card`, `asset fixes`, `cut rename`, `cut use-fixed`, `revision note`.

func assetSourceCmds() []*cobra.Command {
	return []*cobra.Command{assetNamesCmd(), assetCardCmd(), assetFixesCmd()}
}

func cutSourceCmds() []*cobra.Command {
	return []*cobra.Command{cutRenameCmd(), cutUseFixedCmd()}
}

func assetNamesCmd() *cobra.Command {
	var project, accept string
	var acceptAll bool
	cmd := &cobra.Command{
		Use:   "names [--project <p>] [--accept-all | --accept <clip,…>]",
		Short: "Better, descriptive names MetaRush suggests for clips (who is in them, what they're about); --accept-all uses them, keeping names people gave",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if acceptAll || accept != "" {
				in := sources.NamesAcceptIn{ProjectID: project}
				if accept != "" {
					for _, id := range strings.Split(accept, ",") {
						if id = strings.TrimSpace(id); id != "" {
							in.AssetIDs = append(in.AssetIDs, id)
						}
					}
				}
				return runOp(c, "asset.names.accept", in, func(o any) {
					out := o.(*sources.NamesAcceptOut)
					fmt.Println(out.Summary)
					for _, r := range out.Renamed {
						fmt.Printf("  %s (was %s)\n", theirs(r.Title), theirs(r.FileName))
					}
					for _, r := range out.Kept {
						fmt.Printf("  kept %s: %s\n", theirs(r.Title), r.Kept)
					}
					if detailsMode(c) {
						for _, r := range append(out.Renamed, out.Kept...) {
							fmt.Println("  clip:", r.AssetID)
						}
					}
				})
			}
			return runOp(c, "asset.names", sources.NamesIn{ProjectID: project}, func(o any) {
				out := o.(*sources.NamesOut)
				fmt.Println(out.Summary)
				for _, r := range out.Names {
					line := fmt.Sprintf("  %s → %s", theirs(r.Title), theirs(r.Suggestion))
					if !r.Use {
						line += " (kept: " + r.Kept + ")"
					}
					fmt.Println(line)
					if detailsMode(c) {
						fmt.Printf("    %s · file %s · clip %s\n", r.Why, theirs(r.FileName), r.AssetID)
					}
				}
				if len(out.Names) > 0 {
					fmt.Println("Use them with --accept-all (names people gave are kept).")
				}
			})
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "only this project's clips (name or ID)")
	cmd.Flags().BoolVar(&acceptAll, "accept-all", false, "use every better name that is waiting")
	cmd.Flags().StringVar(&accept, "accept", "", "use the better names of these clips (IDs, comma-separated)")
	return cmd
}

func assetCardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "card <clip-or-picture>",
		Short: "A clip's or picture's info card: its name and original file, where it came from, what MetaRush found, where it's used, its fixes",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "asset.card", fixes.ShowIn{ID: args[0]}, func(o any) { printCard(c, o.(*fixes.Card)) })
		},
	}
}

func printCard(c *cobra.Command, k *fixes.Card) {
	fmt.Println(theirs(k.Title))
	if k.FileName != "" && k.FileName != k.Title {
		fmt.Println("  File:", theirs(k.FileName))
	}
	fmt.Println(" ", k.NamedWords)
	if s := k.Suggestion; s != nil {
		fmt.Printf("  Better name: %s (%s) — use it with asset update %s --suggested\n", theirs(s.Title), strings.TrimSuffix(s.Why, "."), k.ID)
	}
	if len(k.Origin) > 0 {
		fmt.Println("Where it came from")
		for _, l := range k.Origin {
			fmt.Println("  " + l)
		}
	}
	if len(k.Found) > 0 {
		fmt.Println("What MetaRush found")
		for _, l := range k.Found {
			fmt.Println("  " + l)
		}
	}
	if len(k.Uses) > 0 {
		fmt.Println("Where it's used")
		for _, u := range k.Uses {
			fmt.Println("  " + u.Text)
		}
	}
	if k.Fixes != nil {
		printFixes(c, k.Fixes)
	}
	if detailsMode(c) {
		fmt.Println("id:", k.ID)
	}
}

func printFixes(c *cobra.Command, v *fixes.View) {
	fmt.Printf("Fixes (a %s)\n", v.KindWords)
	for _, r := range v.Fixes {
		box := "[ ]"
		if r.Ticked {
			box = "[x]"
		}
		line := fmt.Sprintf("  %s %s", box, r.Label)
		switch {
		case r.Fits:
			line += " — " + r.Why
		case r.Pending != "":
			line += " — measured " + r.Pending
		}
		fmt.Println(line)
		if r.After != "" {
			fmt.Println("      " + r.After)
		} else if detailsMode(c) && r.Measured != "" {
			fmt.Println("      " + r.Measured)
		}
	}
	fmt.Println("  " + v.UsingWords)
	if v.TrimWords != "" {
		fmt.Println("  " + v.TrimWords)
	}
	if v.Error != "" {
		fmt.Println("  " + v.Error)
	}
	if v.Copy != nil {
		for _, n := range v.Copy.Notes {
			fmt.Println("  " + n)
		}
	}
	for _, cu := range v.Cuts {
		line := fmt.Sprintf("  “%s” (%s, version %d) %s", cu.CutName, cu.ProjectName, cu.Number, cu.PlaysWords)
		if cu.Offer != "" {
			line += " — " + cu.Offer + ": cut use-fixed"
		}
		fmt.Println(line)
	}
	if v.Measuring != "" {
		fmt.Println("  " + v.Measuring)
	}
}

func assetFixesCmd() *cobra.Command {
	var tick, untick, only, kind string
	var none, again, look, wait bool
	cmd := &cobra.Command{
		Use:   "fixes <clip-or-picture> [--tick steady,colour] [--untick grain] [--only …|--none] [--kind camera|call|voice|music] [--again] [--look] [--wait]",
		Short: "A source's fixes: what fits and why; tick or untick them (the original is kept; a fixed copy is made on a worker), or measure again",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			split := func(s string) []string {
				var out []string
				for _, x := range strings.Split(s, ",") {
					if x = strings.TrimSpace(x); x != "" {
						out = append(out, x)
					}
				}
				return out
			}
			switch {
			case look && !wait:
				return runOp(c, "asset.fixes.look", fixes.ShowIn{ID: args[0]}, func(o any) {
					out := o.(*fixes.LookOut)
					fmt.Println("Measuring it again; the fixes that fit show here once it's done.")
					if detailsMode(c) && out.JobID != "" {
						fmt.Println("job:", out.JobID)
					}
				})
			case look:
				// Measure again and wait for it (here, when no machine running MetaRush takes it).
				a, err := openApp(c, "cli")
				if err != nil {
					return err
				}
				defer a.Close()
				ctx, cancel := signalCtx(c.Context())
				defer cancel()
				out, err := ops.CallTyped[fixes.LookOut](ctx, a, "asset.fixes.look", fixes.ShowIn{ID: args[0]}, ops.Caller{})
				if err != nil {
					return failErr(c, err)
				}
				if out.JobID != "" {
					if _, err := waitJobs(c, a, []string{out.JobID}, time.Hour, false); err != nil {
						return failErr(c, err)
					}
					if v, err := ops.CallTyped[fixes.View](ctx, a, "asset.fixes", fixes.ShowIn{ID: args[0]}, ops.Caller{}); err == nil {
						out.View = v
					}
				}
				return emit(c, out, func() {
					if out.View != nil {
						printFixes(c, out.View)
					}
				})
			case tick == "" && untick == "" && only == "" && !none && kind == "" && !again:
				return runOp(c, "asset.fixes", fixes.ShowIn{ID: args[0]}, func(o any) { printFixes(c, o.(*fixes.View)) })
			}
			in := fixes.SetIn{ID: args[0], Tick: split(tick), Untick: split(untick), Kind: kind, Again: again}
			if only != "" || none {
				o := split(only)
				if o == nil {
					o = []string{}
				}
				in.Only = &o
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			v, err := ops.CallTyped[fixes.View](ctx, a, "asset.fixes.set", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			if wait && len(v.JobIDs) > 0 {
				// The fixed copy is made here when no machine running MetaRush takes it.
				jobs, err := waitJobs(c, a, v.JobIDs, 3*time.Hour, false)
				if err != nil {
					return failErr(c, err)
				}
				if nv, err := ops.CallTyped[fixes.View](ctx, a, "asset.fixes", fixes.ShowIn{ID: args[0]}, ops.Caller{}); err == nil {
					v = nv
				}
				for _, j := range jobs {
					if j.State == models.JobFailed && v.Error == "" {
						v.Error = firstLineOf(j.Error)
					}
				}
			}
			return emit(c, v, func() {
				fmt.Println(v.Summary)
				printFixes(c, v)
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&tick, "tick", "", "fixes to turn on (comma-separated): steady, colour, grain, volumes, voices, blocks, loudness, hum, ends, background, edges, larger")
	f.StringVar(&untick, "untick", "", "fixes to turn off")
	f.StringVar(&only, "only", "", "exactly these fixes")
	f.BoolVar(&none, "none", false, "no fixes: the original again")
	f.StringVar(&kind, "kind", "", "what kind of source it is: camera, call, voice or music")
	f.BoolVar(&again, "again", false, "make the fixed copy again (once the better steadying is set up, say)")
	f.BoolVar(&look, "look", false, "measure it again")
	f.BoolVar(&wait, "wait", false, "wait for the fixed copy")
	return cmd
}

func cutRenameCmd() *cobra.Command {
	var branch string
	var suggested bool
	cmd := &cobra.Command{
		Use:   "rename <project> <name> | --suggested",
		Short: "Name a project's cut descriptively (“Main open day video”), or take MetaRush's suggestion; no new version is saved and approvals stay",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			in := ops.CutRenameIn{ProjectID: args[0], Branch: branch, Suggested: suggested}
			if len(args) > 1 {
				in.Title = args[1]
			}
			if in.Title == "" && !suggested {
				return fail(c, apperr.InvalidInput, "Give the cut a name, or take MetaRush's suggestion with --suggested.", "")
			}
			return runOp(c, "cut.rename", in, func(o any) {
				v := o.(*ops.CutView)
				fmt.Printf("The cut is called %s now.\n", theirs(v.Name))
			})
		},
	}
	cmd.Flags().StringVar(&branch, "branch", "", "which cut (default: the main one)")
	cmd.Flags().BoolVar(&suggested, "suggested", false, "use the name MetaRush suggests")
	return cmd
}

func cutUseFixedCmd() *cobra.Command {
	var f cutEditFlags
	var original bool
	cmd := &cobra.Command{
		Use:   "use-fixed <project> <clip-or-picture> [--original]",
		Short: "Save a new version of the cut that plays a source's fixed copy (or its original again with --original); saved versions never change",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			return runCutEdit(c, "cut.useFixed", &f, func(common ops.CutEditCommon) any {
				common.ProjectID = args[0]
				return ops.CutUseFixedIn{CutEditCommon: common, Footage: args[1], Original: original}
			})
		},
	}
	f.add(cmd)
	cmd.Flags().BoolVar(&original, "original", false, "play the original again")
	return cmd
}

func revisionNoteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "note <version> <note>",
		Short: "Change a version's note: a few words saying what it changed (the version itself never changes); an empty note removes it",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "revision.note", ops.RevisionNoteIn{ID: args[0], Note: args[1]}, func(o any) {
				r := o.(*ops.RevisionSummary)
				if r.Note == "" {
					fmt.Printf("Version %d has no note now.\n", r.Number)
					return
				}
				fmt.Printf("Version %d: %s\n", r.Number, theirs(r.Note))
			})
		},
	}
}
