package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/mattes"
	"github.com/metavert-io/metarush/internal/ops"
)

// Background removal for video (D505–D514): a clip's cut-out — the people in
// it told from their background, kept once — and a cut's clip with its
// background replaced. `metarush footage cut-out|cut-outs|cut-out-preview|
// cut-out-delete` and `metarush cut background`.

// footageCutoutCmds are the footage commands for clips' cut-outs.
func footageCutoutCmds() []*cobra.Command {
	return []*cobra.Command{footageCutoutCmd(), footageCutoutsCmd(), footageCutoutPreviewCmd(), footageCutoutDeleteCmd()}
}

// footageCutoutDeleteCmd deletes a cut-out for good: a person in their own
// terminal, who sees first which versions play it and types "delete".
func footageCutoutDeleteCmd() *cobra.Command {
	var in mattes.CutoutDeleteIn
	c := &cobra.Command{
		Use:   "cut-out-delete <cut-out> --forever [--dry-run]",
		Short: "Delete a clip's cut-out for good (it can be made again) — owners, in their own terminal; refused while anything plays it (any version of a cut, a draft, a version in Recipes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ID = args[0]
			if assistantMode() || !interactive() {
				return fail(c, apperr.HumanOnly, "Only an owner can delete a cut-out, in their own terminal.", "Run this yourself in a terminal (agents and the dashboard assistant can't delete).")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			check := in
			check.DryRun = true
			res, err := ops.CallTyped[mattes.CutoutDeleteOut](c.Context(), a, "footage.cutout.delete", check, ops.Caller{Human: true})
			if err != nil {
				return failErr(c, err)
			}
			if in.DryRun || !res.Allowed {
				if jsonMode(c) {
					if err := emit(c, res, func() {}); err != nil || res.Allowed {
						return err
					}
					return &exitError{code: 4}
				}
				fmt.Println(res.Summary)
				if !res.Allowed {
					return &exitError{code: 4}
				}
				return nil
			}
			if !jsonMode(c) {
				fmt.Println(res.Summary)
			}
			if !in.Forever {
				return fail(c, apperr.InvalidInput, "Add --forever to delete it for good.", "")
			}
			if ask(fmt.Sprintf("Type %q to delete the cut-out of “%s” for good", "delete", res.Cutout.Clip), "") != "delete" {
				return fail(c, apperr.Canceled, "nothing was deleted", "")
			}
			return runOpWith(c, a, "footage.cutout.delete", in, ops.Caller{Human: true}, func(o any) {
				fmt.Println(o.(*mattes.CutoutDeleteOut).Summary)
			})
		},
	}
	c.Flags().BoolVar(&in.Forever, "forever", false, "yes, for good (it can be made again, as a new file)")
	c.Flags().BoolVar(&in.DryRun, "dry-run", false, "only say whether it can be deleted and which versions play it")
	return c
}

// parseRange reads 0:12-0:40 (or 12-40) into from and to.
func parseRange(s string) (float64, float64, error) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), "-")
	if !ok {
		return 0, 0, fmt.Errorf("a part of a clip is from-to, like 0:12-0:40, not %q", s)
	}
	from, err := clockSeconds(a)
	if err != nil {
		return 0, 0, fmt.Errorf("a part of a clip is from-to, like 0:12-0:40, not %q", s)
	}
	to, err := clockSeconds(b)
	if err != nil || to <= from {
		return 0, 0, fmt.Errorf("a part of a clip is from-to, like 0:12-0:40, not %q", s)
	}
	return from, to, nil
}

func footageCutoutCmd() *cobra.Command {
	var in mattes.CutoutIn
	var from, to, rng string
	var maxCost float64
	var wait, details bool
	c := &cobra.Command{
		Use:   "cut-out <clip> [--range 0:12-0:40 | --from … --to …] [--model local/modnet] [--wait] [--dry-run]",
		Short: "Cut the people out of a clip, or a part of it, for background removal in video: made once on a computer of the workspace that can (free; nothing leaves it) and reused by every cut",
		Long: `Cut the people out of a clip (or a part of it): MetaRush tells them from their background frame by
frame, smooths it over time and keeps the clip's picture with its background see-through, once. Cuts then
put a colour, a blur, a picture or another clip behind them (metarush cut background).

On a Mac it uses Apple Vision; elsewhere the open model MODNet, added in Settings → Components
(“Replacing a video's background”). Any computer of the workspace that can makes it; --wait waits here.

  metarush footage cut-out ast_… --range 0:12-0:40 --wait
  metarush footage cut-out ast_… --model local/modnet --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.AssetID = args[0]
			if rng != "" {
				f, t, err := parseRange(rng)
				if err != nil {
					return fail(c, apperr.InvalidInput, err.Error(), "")
				}
				in.From, in.To = &f, &t
			}
			for _, x := range []struct {
				v   string
				dst **float64
			}{{from, &in.From}, {to, &in.To}} {
				if x.v == "" {
					continue
				}
				s, err := clockSeconds(x.v)
				if err != nil || s < 0 {
					return fail(c, apperr.InvalidInput, "times are like 0:12.5 or 12.5, not "+strconv.Quote(x.v), "")
				}
				*x.dst = &s
			}
			if c.Flags().Changed("max-cost") {
				in.MaxUSD = &maxCost
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			out, err := callCutout(c, a, in)
			if err != nil {
				return failErr(c, err)
			}
			if wait && out.JobID != "" && !out.DryRun {
				if _, err := waitJobs(c, a, []string{out.JobID}, 0, jsonMode(c)); err != nil {
					return failErr(c, err)
				}
				if out, err = callCutout(c, a, in); err != nil {
					return failErr(c, err)
				}
			}
			return emit(c, out, func() {
				fmt.Println(out.Summary)
				if details {
					v := out.Matte
					fmt.Printf("cut-out: %s  state: %s  model: %s/%s\n", v.ID, v.State, v.Provider, v.Model)
					if v.SHA256 != "" {
						fmt.Printf("file: sha256:%s (%d bytes)\n", v.SHA256, v.Size)
					}
					if out.JobID != "" {
						fmt.Printf("job: %s\n", out.JobID)
					}
				}
				if out.JobID != "" && !wait && out.Matte.State != "ready" {
					fmt.Println("It is made in the background; metarush footage cut-outs " + in.AssetID + " says how far along it is.")
				}
			})
		},
	}
	f := c.Flags()
	f.StringVar(&rng, "range", "", "only this part of the clip: from-to, like 0:12-0:40")
	f.StringVar(&from, "from", "", "where the part starts (0:12.5 or 12.5; default: the clip's start)")
	f.StringVar(&to, "to", "", "where it ends (default: the clip's end)")
	f.StringVar(&in.Model, "model", "", "the model: local/apple-vision (a Mac) or local/modnet (default: the usual one where it can run)")
	f.StringVar(&in.Fixed, "fixed", "", "the clip's media: original, or a fixed copy's SHA-256 (default: what it plays now)")
	f.StringVar(&in.ProjectID, "project", "", "the project it is for: its Content Settings choose the model (default: the clip's first)")
	f.Float64Var(&maxCost, "max-cost", 0, "for a service elsewhere: the most you agree to pay, in US dollars")
	f.BoolVar(&in.DryRun, "dry-run", false, "only say what it would use and how long it would take")
	f.BoolVar(&wait, "wait", false, "wait until it is made (here, when no other computer can)")
	addDetails(c, &details)
	return c
}

func callCutout(c *cobra.Command, a *app.App, in mattes.CutoutIn) (*mattes.CutoutOut, error) {
	b, _ := json.Marshal(in)
	res, err := ops.Call(c.Context(), a, "footage.cutout", b, ops.Caller{})
	if err != nil {
		return nil, err
	}
	return res.(*mattes.CutoutOut), nil
}

func footageCutoutsCmd() *cobra.Command {
	var details bool
	c := &cobra.Command{
		Use:   "cut-outs <clip>",
		Short: "List a clip's cut-outs (background removal in video): the part each covers, how far along it is, the model and the computer that made it",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "footage.cutouts", mattes.CutoutsIn{AssetID: args[0]}, func(o any) {
				out := o.(*mattes.CutoutsOut)
				fmt.Println(out.Summary)
				for _, v := range out.Cutouts {
					fmt.Printf("  %s: %s\n", upperFirst(v.Part), v.Words)
					if details {
						fmt.Printf("    %s  %s/%s", v.ID, v.Provider, v.Model)
						if v.SHA256 != "" {
							fmt.Printf("  sha256:%s", v.SHA256)
						}
						if v.JobID != "" {
							fmt.Printf("  job %s", v.JobID)
						}
						fmt.Println()
					}
				}
				if len(out.Here) == 0 {
					fmt.Println("This computer can't make cut-outs: add “Replacing a video's background” in Settings → Components (a Mac uses Apple Vision).")
				}
			})
		},
	}
	addDetails(c, &details)
	return c
}

func footageCutoutPreviewCmd() *cobra.Command {
	var in mattes.CutoutPreviewIn
	var at, footageAt string
	var details bool
	c := &cobra.Command{
		Use:   "cut-out-preview <clip> --at 0:03 --with colour|blur|picture|footage [--colour #00b140] [--picture <name>] [--footage <clip>] [--out still.jpg]",
		Short: "Show one moment of a clip with its background replaced, as a still on this computer (nothing is kept)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.AssetID = args[0]
			t, err := clockSeconds(at)
			if err != nil || t < 0 {
				return fail(c, apperr.InvalidInput, "--at is a moment of the clip, like 0:03.5", "")
			}
			in.At = t
			if footageAt != "" {
				if in.FootageAt, err = clockSeconds(footageAt); err != nil {
					return fail(c, apperr.InvalidInput, "--footage-at is a moment of that clip, like 0:03.5", "")
				}
			}
			return runOp(c, "footage.cutout.preview", in, func(o any) {
				out := o.(*mattes.CutoutPreviewOut)
				fmt.Println(out.Summary)
				fmt.Println(out.Path)
				if details {
					fmt.Printf("%dx%d, the people found by %s\n", out.Width, out.Height, out.Model)
				}
			})
		},
	}
	f := c.Flags()
	f.StringVar(&at, "at", "0", "the moment of the clip (0:03.5)")
	f.StringVar(&in.With, "with", "blur", "what goes behind the people: colour, blur, picture or footage")
	f.StringVar(&in.Colour, "colour", "", "with colour: #rrggbb")
	f.Float64Var(&in.Blur, "blur", 0, "with blur: how strong (default 28)")
	f.StringVar(&in.Picture, "picture", "", "with picture: a picture's short name from the resources library")
	f.StringVar(&in.Footage, "footage", "", "with footage: the clip (ID) to show behind")
	f.StringVar(&footageAt, "footage-at", "", "the moment of that clip to show")
	f.Float64Var(&in.Feather, "edges", 0, "how soft the people's edges are (default 2)")
	f.StringVar(&in.ProjectID, "project", "", "the project whose pictures a picture is looked up in")
	f.StringVar(&in.Fixed, "fixed", "", "the clip's media: original, or a fixed copy's SHA-256")
	f.IntVar(&in.Width, "width", 0, "the still's width (default 960)")
	f.StringVar(&in.Out, "out", "", "a new .jpg file for the still, or a folder for it (never over a file already there; default: a folder of its own)")
	addDetails(c, &details)
	return c
}

// cutBackgroundCmd replaces what is behind the people in a clip of the cut.
func cutBackgroundCmd() *cobra.Command {
	var f cutEditFlags
	var in mattes.CutBackgroundIn
	var feather, footageAt string
	var maxCost float64
	var wait bool
	c := &cobra.Command{
		Use:   "background <project> --clip <clip> [--with colour|blur|picture|footage] [--colour #00b140] [--blur 28] [--picture <name>] [--footage <clip> --footage-at 0:03] [--edges 2] [--off] [--wait] [--dry-run | --confirm <token>]",
		Short: "Replace the background behind the people in a clip: a colour, a blur of the clip itself, a picture or another clip (background removal in video)",
		Long: `Replace what is behind the people in a clip of the latest version: a colour, a blur of the clip itself,
a picture from the resources library or another clip, their edges softened (--edges). The clip's cut-out is
made first when it has none (free on a computer of the workspace; nothing leaves it); --wait waits for it,
otherwise run the same command again once it is made. --off takes the background out again.

  metarush cut background "Garden walk" --clip 2 --with colour --colour #00b140 --wait
  metarush cut background "Garden walk" --clip hook --with picture --picture beach
  metarush cut background "Garden walk" --clip hook --with footage --footage "Waves" --footage-at 0:04
  metarush cut background "Garden walk" --clip hook --off

It shows the change first; in a terminal it asks before saving. Otherwise run it again with --confirm <token>.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			f.common.ProjectID = args[0]
			if feather != "" {
				v, err := strconv.ParseFloat(strings.TrimSpace(feather), 64)
				if err != nil {
					return fail(c, apperr.InvalidInput, "--edges is how soft the people's edges are, from 0 to 40", "")
				}
				in.Feather = &v
			}
			if footageAt != "" {
				v, err := clockSeconds(footageAt)
				if err != nil || v < 0 {
					return fail(c, apperr.InvalidInput, "--footage-at is a moment of that clip, like 0:03.5", "")
				}
				in.FootageAt = v
			}
			if c.Flags().Changed("max-cost") {
				in.MaxUSD = &maxCost
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			call := func(common ops.CutEditCommon) (*mattes.CutBackgroundOut, error) {
				x := in
				x.CutEditCommon = common
				b, _ := json.Marshal(x)
				res, err := ops.Call(c.Context(), a, "cut.background", b, ops.Caller{})
				if err != nil {
					return nil, err
				}
				return res.(*mattes.CutBackgroundOut), nil
			}
			common := f.common
			r, err := call(common)
			if err != nil {
				return failErr(c, err)
			}
			if r.Waiting && wait && r.Cutout != nil && r.Cutout.JobID != "" {
				if !jsonMode(c) {
					fmt.Println(r.Cutout.Summary)
				}
				if _, err := waitJobs(c, a, []string{r.Cutout.JobID}, 0, jsonMode(c)); err != nil {
					return failErr(c, err)
				}
				if r, err = call(common); err != nil {
					return failErr(c, err)
				}
			}
			if !r.Waiting && !r.NoChange && common.ConfirmToken == "" && !common.DryRun && interactive() && !jsonMode(c) && r.Check != nil && r.Check.ConfirmToken != "" {
				printCutEdit(c, &r.CutEditOut, "")
				if !askYes("Save this change as a new version?", false) {
					return fail(c, apperr.Canceled, "not saved", "")
				}
				common.ConfirmToken = r.Check.ConfirmToken
				if r, err = call(common); err != nil {
					return failErr(c, err)
				}
			}
			return emit(c, r, func() {
				if r.Waiting || (r.Cutout != nil && r.Cutout.DryRun && r.Check == nil) {
					fmt.Println(r.Summary)
					if r.Waiting {
						fmt.Println("Add --wait to wait for it here.")
					}
					return
				}
				next := ""
				if r.Check != nil && r.Check.ConfirmToken != "" {
					next = sameCommandWith(c, "--confirm", r.Check.ConfirmToken)
				}
				printCutEdit(c, &r.CutEditOut, next)
			})
		},
	}
	fl := c.Flags()
	fl.StringVar(&in.Clip, "clip", "", "the clip: its name in the cut or its number (1 = the first)")
	fl.StringVar(&in.With, "with", "", "what goes behind the people: colour, blur, picture or footage (default: what the clip has, else blur)")
	fl.StringVar(&in.Colour, "colour", "", "with colour: #rrggbb")
	fl.Float64Var(&in.Blur, "blur", 0, "with blur: how strong (default 28)")
	fl.StringVar(&in.Picture, "picture", "", "with picture: a picture's short name from the resources library")
	fl.StringVar(&in.Footage, "footage", "", "with footage: the clip to show behind (its name, file name or ID)")
	fl.StringVar(&footageAt, "footage-at", "", "where in that clip it starts (0:03.5)")
	fl.StringVar(&feather, "edges", "", "how soft the people's edges are, 0–40 (default 2)")
	fl.BoolVar(&in.Off, "off", false, "take the replaced background out: the clip plays as recorded again")
	fl.StringVar(&in.Model, "model", "", "the model for the clip's cut-out when it needs one (local/apple-vision, local/modnet)")
	fl.Float64Var(&maxCost, "max-cost", 0, "for a cut-out made by a service elsewhere: the most you agree to pay, in US dollars")
	fl.BoolVar(&wait, "wait", false, "wait for the clip's cut-out when it is being made, then check (or save) the change")
	_ = c.MarkFlagRequired("clip")
	f.add(c)
	return c
}
