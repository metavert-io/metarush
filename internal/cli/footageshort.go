package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// footageShortCmd is `metarush footage short` (D246): a short from any
// footage, as a new cut for phones (tall).
func footageShortCmd() *cobra.Command {
	var in ops.FootageShortIn
	var clip, from, to string
	var pieces []string
	var noCaptions, details bool
	c := &cobra.Command{
		Use:   "short <project> (--clip <clip> --from <time> --to <time> | --piece <clip>@<from>-<to> …) [--name …] [--look …] [--dry-run | --confirm <token>]",
		Short: "Make a short from any footage: a stretch (or several) as a new cut for phones (tall), framed as the house style says, with captions",
		Long: `Make a short from one or more stretches of the project's footage (metarush footage moments lists
moments with their times). The short is a new cut of the project — for phones (tall), the crop framed as the
house style says (faces kept in frame without one; --framing follow makes it follow the subject as it moves),
captions in the house style's look (else big and bold) — that a person approves like any cut.

  metarush footage short "Garden walk" --clip ast_… --from 0:12 --to 0:41 --name "The first tomato"
  metarush footage short "Garden walk" --piece ast_…@0:12-0:20 --piece ast_…@1:05-1:24

It shows the short first; in a terminal it asks before saving. Otherwise run it again with --confirm <token>.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ProjectID = args[0]
			if clip != "" || from != "" || to != "" {
				p, err := ops.ParseShortPiece(clip + "@" + from + "-" + to)
				if err != nil || clip == "" || from == "" || to == "" {
					return fail(c, apperr.InvalidInput, "say --clip, --from and --to together", "")
				}
				in.Pieces = append(in.Pieces, p)
			}
			for _, s := range pieces {
				p, err := ops.ParseShortPiece(s)
				if err != nil {
					return fail(c, apperr.InvalidInput, err.Error(), "")
				}
				in.Pieces = append(in.Pieces, p)
			}
			if noCaptions {
				v := false
				in.Captions = &v
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			call := func(x ops.FootageShortIn) (*ops.FootageShortOut, error) {
				b, _ := json.Marshal(x)
				res, err := ops.Call(c.Context(), a, "footage.short", b, ops.Caller{})
				if err != nil {
					return nil, err
				}
				return res.(*ops.FootageShortOut), nil
			}
			if in.ConfirmToken == "" && !in.DryRun && interactive() && !jsonMode(c) {
				dry := in
				dry.DryRun = true
				r, err := call(dry)
				if err != nil {
					return failErr(c, err)
				}
				printShort(c, r, "")
				if r.Check == nil || r.Check.ConfirmToken == "" {
					return nil
				}
				if !askYes("Make this short?", false) {
					return fail(c, apperr.Canceled, "not made", "")
				}
				in.ConfirmToken = r.Check.ConfirmToken
			}
			r, err := call(in)
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, r, func() {
				next := ""
				if r.Check != nil && r.Check.ConfirmToken != "" {
					next = sameCommandWith(c, "--confirm", r.Check.ConfirmToken)
				}
				printShort(c, r, next)
			})
		},
	}
	f := c.Flags()
	f.StringVar(&clip, "clip", "", "the clip (asset ID) of a one-stretch short")
	f.StringVar(&from, "from", "", "where the stretch starts in the clip (12.5 or 0:12.5)")
	f.StringVar(&to, "to", "", "where it ends")
	f.StringArrayVar(&pieces, "piece", nil, "a stretch as <clip>@<from>-<to> (repeat for several, in order)")
	f.StringVar(&in.Name, "name", "", "the short's name (default: from its first words)")
	f.StringVar(&in.Shape, "shape", "", "vertical (for phones, tall; the default) or square")
	f.StringVar(&in.Framing, "framing", "", "follow (the subject as it moves), faces or center (default: the house style's)")
	f.StringVar(&in.Look, "look", "", "the captions' look (metarush captions looks; default: the house style's, else bold)")
	f.BoolVar(&noCaptions, "no-captions", false, "no captions")
	f.StringVar(&in.Branch, "branch", "", "the new cut's short name (default: the first free short-N)")
	f.StringVar(&in.Why, "why", "", "why this moment makes a short (kept with the cut)")
	f.BoolVar(&in.DryRun, "dry-run", false, "only check: the short, its warnings and a confirm token")
	f.StringVar(&in.ConfirmToken, "confirm", "", "token from a check of exactly this short")
	addDetailsFlag(c, &details)
	return c
}

func printShort(c *cobra.Command, r *ops.FootageShortOut, next string) {
	details := detailsMode(c)
	if r.Applied != nil {
		fmt.Printf("Made: %s\n", r.Summary)
		for _, w := range r.Applied.Warnings {
			fmt.Println("  ! " + w.Message)
		}
		fmt.Println("Approve it like any cut once you've watched it.")
		if details {
			fmt.Printf("branch: %s\nrevision: %s\n%s\n", r.Branch, r.Applied.Revision.ID, r.Applied.URL)
		}
		return
	}
	chk := r.Check
	if chk != nil && !chk.Valid {
		fmt.Printf("This short can't be made yet: %s\n", r.Summary)
		for _, e := range chk.Errors {
			fmt.Println("  ✗ " + e.Message)
		}
		return
	}
	fmt.Println(r.Summary)
	for _, d := range r.Details {
		fmt.Println("  " + d)
	}
	if chk != nil {
		for _, w := range chk.Warnings {
			fmt.Println("  ! " + w.Message)
		}
	}
	readyToSave(c, next)
}
