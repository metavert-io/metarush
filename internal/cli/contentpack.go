package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ops"
)

// `metarush content pack` (D263–D267): the layout pack of a project's
// Content Settings — the title cards, lower thirds, end screens, quote and
// chapter cards and call layouts every project using them draws with.
func contentPackCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "pack", Short: "The layout pack: which template or approved motion graphic draws each title card, lower third, end screen, quote card and chapter card, and how call layouts look",
		Long: `The layout pack is part of a project's Content Settings: every project using them draws
its title cards, lower thirds, end screens, quote cards, chapter cards and the picture in
picture and side-by-side layouts of call recordings with it, in the brand's colours, font
and logos. Settings without a pack use MetaRush's own, in their house style.

A graphic in a cut names its role (in a cut sheet: - intro: title card at 0:00 for 3 s ·
with title "…"); checking the change draws it with the pack. Saved versions never change:
after a change the latest cuts that don't follow the pack are listed, and
metarush content pack apply --project <project> puts it on one as a checked change.

A pack with project motion graphics is proposed (a draft) and becomes the pack only when a
person accepts it, once every graphic in it is approved.

  metarush content pack show --project acme
  metarush content pack set --project acme --role lower-third --template lower-third.cinematic --fields '{"align":"center"}'
  metarush content pack set --project acme --pip-corner bottom-left --pip-shape circle --pip-frame accent
  metarush content pack propose --project acme --file pack.json --note "Acme's spring look"
  metarush content pack accept --project acme
  metarush content pack apply --project "Spring promo" --dry-run
  metarush content pack preview --project acme --role end-screen --json`}

	var showIn ops.ContentPackIn
	show := &cobra.Command{
		Use: "show [--project <project>]", Short: "The layout pack of the settings a project uses (without --project: the default's), and a proposed pack waiting for a person", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "content.pack.show", showIn, func(o any) { printPack(c, o.(*ops.ContentPackOut)) })
		},
	}
	show.Flags().StringVar(&showIn.ProjectID, "project", "", "the project (its name or ID)")

	var in ops.ContentPackSetIn
	var fields, name, note, motionRef, library string
	var seconds float64
	var logo, rounded bool
	set := &cobra.Command{
		Use:   "set [--project <project>] [--role <part> (--template <id> | --motion <name@version>) [--fields '{…}'] [--seconds <s>] [--logo]] [--name …] [--remove <part>] [--library <name@version>] [--pip-corner …] [--pip-size …] [--pip-shape …] [--pip-frame …] [--gap …] [--frame …] [--backdrop black|brand] [--rounded] [--reset]",
		Short: "Change the layout pack of a project's own settings (without --project: the default's); settings without one start from MetaRush's own",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			fl := c.Flags()
			if fl.Changed("name") {
				in.Name = &name
			}
			if fl.Changed("note") {
				in.Note = &note
			}
			if fl.Changed("fields") {
				if !json.Valid([]byte(fields)) {
					return fail(c, apperr.InvalidInput, "--fields isn't a list of names and values MetaRush can read.", "Write it the way the pack shows a graphic's choices with --json.")
				}
				in.Fields = json.RawMessage(fields)
			}
			if fl.Changed("seconds") {
				in.Seconds = &seconds
			}
			if fl.Changed("logo") {
				in.Logo = &logo
			}
			if fl.Changed("rounded") {
				in.Rounded = &rounded
			}
			for flag, dst := range map[string]**string{"pip-corner": &in.PipCorner, "pip-size": &in.PipSize, "pip-shape": &in.PipShape, "pip-frame": &in.PipFrame,
				"gap": &in.Gap, "frame": &in.Frame, "backdrop": &in.Backdrop} {
				if fl.Changed(flag) {
					v, _ := fl.GetString(flag)
					*dst = &v
				}
			}
			caller := ops.Caller{}
			if library != "" {
				// Which code a pack draws with is a person's choice (D266).
				if !interactive() {
					return fail(c, apperr.HumanOnly, "Only a person can move a layout pack to another version of a shared library.", "Move it under Settings → Layout pack, or run this yourself in a terminal.")
				}
				if ask(fmt.Sprintf("Type %q to move the pack's graphics to the shared library %s", "use", library), "") != "use" {
					return fail(c, apperr.Canceled, "Nothing changed.", "")
				}
				in.Library = library
				caller.Human = true
			}
			if motionRef != "" {
				// Code reaches a pack only through a person (D266).
				if !interactive() {
					return fail(c, apperr.HumanOnly, "Only a person can put a motion graphic in a layout pack.", "Propose the pack instead; a person accepts it once its graphics are approved.")
				}
				if ask(fmt.Sprintf("Type %q to draw the %s with the motion graphic %s", "use", editspec.RoleWords(in.Role), motionRef), "") != "use" {
					return fail(c, apperr.Canceled, "Nothing changed.", "")
				}
				in.Motion = motionRef
				caller.Human = true
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "content.pack.set", in, caller, func(o any) { printPack(c, o.(*ops.ContentPackOut)) })
		},
	}
	fl := set.Flags()
	fl.StringVar(&in.ProjectID, "project", "", "a project with its own settings (default: the default's)")
	fl.StringVar(&in.Role, "role", "", "the part to change: title-card, lower-third, end-screen, quote-card or chapter-card (a variant after a colon: lower-third:guest)")
	fl.StringVar(&in.Template, "template", "", "a vetted template for the part (title.card, slide.title, lower-third.clean, lower-third.cinematic, endcard.cta, end.screen, slide.quote, quote.kinetic)")
	fl.StringVar(&motionRef, "motion", "", "an approved project motion graphic for the part (name@version; asks you to confirm)")
	fl.StringVar(&fields, "fields", "", `the pack's choices for the part, as JSON: how it looks ('{"align":"center"}') and words it shows when a cut gives none; '{}' clears them`)
	fl.Float64Var(&seconds, "seconds", 0, "how long the graphic stays when a cut doesn't say (0: the template's own)")
	fl.BoolVar(&logo, "logo", false, "show the brand's logo (--logo=false: don't)")
	fl.StringVar(&name, "name", "", "what people call the pack")
	fl.StringVar(&note, "note", "", "what the pack is for")
	fl.StringArrayVar(&in.Remove, "remove", nil, "take a part out of the pack: MetaRush's own fills it (repeat for more)")
	fl.String("pip-corner", "", "picture in picture: bottom-right, bottom-left, top-right or top-left")
	fl.String("pip-size", "", "picture in picture: small, medium or large")
	fl.String("pip-shape", "", "picture in picture: rounded, square or circle")
	fl.String("pip-frame", "", "picture in picture: a line in the brand's accent, primary or text colour, or none")
	fl.String("gap", "", "side by side: none, thin or wide")
	fl.String("frame", "", "side by side: a line in the brand's accent, primary or text colour, or none")
	fl.String("backdrop", "", "side by side: black or brand (the brand's background between the pictures)")
	fl.BoolVar(&rounded, "rounded", false, "side by side: rounded corners (--rounded=false: square)")
	fl.BoolVar(&in.Reset, "reset", false, "go back to MetaRush's own pack (the settings' pack is removed)")
	fl.StringVar(&library, "library", "", "move every graphic of the pack that uses a shared library to this approved version (name@version; asks you to confirm)")

	var propIn ops.ContentPackProposeIn
	var propFile string
	propose := &cobra.Command{
		Use:   "propose --file <pack.json> [--project <project>] [--note …] [--dry-run]",
		Short: "Propose a whole layout pack for a project's settings (its graphics may be project motion graphics): a draft a person accepts",
		Long: `The file is a pack as metarush content pack show --json prints it ("pack"): a name and, by part
(title-card, lower-third, end-screen, quote-card, chapter-card; a variant after a colon), a
"template" or a "motion" graphic ({"name": …, "hash": …}), its "fields", "seconds" and "logo",
and "layouts" for picture in picture and side by side. Parts left out use MetaRush's own.
Motion graphics must be checked first (metarush motion check); the pack becomes the pack
when a person accepts it, once each is approved.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if propFile == "" {
				return fail(c, apperr.InvalidInput, "Give the pack in a file: --file pack.json.", "")
			}
			b, err := readPackFile(propFile)
			if err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			propIn.Pack = *b
			return runOp(c, "content.pack.propose", propIn, func(o any) { printPack(c, o.(*ops.ContentPackOut)) })
		},
	}
	propose.Flags().StringVar(&propIn.ProjectID, "project", "", "a project with its own settings (default: the default's)")
	propose.Flags().StringVar(&propFile, "file", "", "the pack (JSON; - reads it from standard input)")
	propose.Flags().StringVar(&propIn.Note, "note", "", "what the pack is for")
	propose.Flags().BoolVar(&propIn.DryRun, "dry-run", false, "only check it and say what waits (nothing is saved)")

	var accIn ops.ContentPackAcceptIn
	accept := &cobra.Command{
		Use: "accept [--project <project>]", Short: "Make the proposed layout pack the pack of a project's settings (people only, in a terminal or the dashboard)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if !interactive() {
				return fail(c, apperr.HumanOnly, "Only a person can accept a layout pack.", "Accept it under Settings → Layout pack in the dashboard, or run this yourself in a terminal.")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			// Show the proposed pack first; accepting is of exactly that one.
			cur, err := ops.CallTyped[ops.ContentPackOut](c.Context(), a, "content.pack.show", ops.ContentPackIn{ProjectID: accIn.ProjectID}, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			if cur.Draft == nil {
				return fail(c, apperr.NotFound, "No layout pack is proposed for these settings.", "")
			}
			fmt.Println("The proposed pack:")
			for _, l := range cur.Draft.Lines {
				fmt.Println("  " + l)
			}
			if !cur.Draft.Ready {
				fmt.Println(cur.Draft.Why)
			}
			if ask(`Type "accept" to make it the layout pack`, "") != "accept" {
				return fail(c, apperr.Canceled, "Nothing changed.", "")
			}
			at := cur.Draft.At
			accIn.DraftAt = &at
			return runOpWith(c, a, "content.pack.accept", accIn, ops.Caller{Human: true}, func(o any) { printPack(c, o.(*ops.ContentPackOut)) })
		},
	}
	accept.Flags().StringVar(&accIn.ProjectID, "project", "", "a project with its own settings (default: the default's)")

	var decIn ops.ContentPackDeclineIn
	decline := &cobra.Command{
		Use: "decline [--project <project>] [--note …]", Short: "Set the proposed layout pack aside (the pack stays as it was)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "content.pack.decline", decIn, func(o any) { printPack(c, o.(*ops.ContentPackOut)) })
		},
	}
	decline.Flags().StringVar(&decIn.ProjectID, "project", "", "a project with its own settings (default: the default's)")
	decline.Flags().StringVar(&decIn.Note, "note", "", "why (kept in the activity log)")

	var f cutEditFlags
	apply := &cobra.Command{
		Use: "apply --project <project>", Short: "Make a project's latest cut follow the layout pack of the settings it uses (shows the change first)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			project, _ := c.Flags().GetString("project")
			if project == "" {
				return fail(c, apperr.InvalidInput, "Say which project's latest cut: add --project <project>.", "")
			}
			return runCutEdit(c, "content.pack.apply", &f, func(common ops.CutEditCommon) any {
				common.ProjectID = project
				return ops.ContentPackApplyIn{CutEditCommon: common}
			})
		},
	}
	apply.Flags().String("project", "", "the project whose latest cut follows the layout pack")
	f.add(apply)

	var prevIn ops.ContentPackPreviewIn
	preview := &cobra.Command{
		Use: "preview [--project <project>] [--role <role>] [--shape landscape|vertical|square] [--draft]", Short: "Previews of the layout pack in the brand and house style (with --json: a plan per graphic the dashboard plays, and where call layouts place the pictures)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "content.pack.preview", prevIn, func(o any) {
				out := o.(*ops.ContentPackPreviewOut)
				fmt.Printf("The layout pack, %s:\n", out.Shape)
				for _, p := range out.Previews {
					line := fmt.Sprintf("  %s: %s", upperFirstWord(p.Words), p.What)
					if p.Waiting {
						line += " (waits for a person's approval)"
					}
					if p.Starter {
						line += " (MetaRush's own)"
					}
					fmt.Println(line)
				}
				fmt.Println("Add --json for what the dashboard draws, or open Settings → Layout pack.")
			})
		},
	}
	preview.Flags().StringVar(&prevIn.ProjectID, "project", "", "the project (its name or ID)")
	preview.Flags().StringVar(&prevIn.Role, "role", "", "one part (title-card, lower-third:guest, picture-in-picture…)")
	preview.Flags().StringVar(&prevIn.Shape, "shape", "", "landscape, vertical or square (default: the project's latest cut's)")
	preview.Flags().BoolVar(&prevIn.Draft, "draft", false, "preview the proposed pack instead")

	cmd.AddCommand(show, set, propose, accept, decline, apply, preview)
	return cmd
}

// readPackFile reads a pack: the pack itself, or content pack show --json's output (its "pack").
func readPackFile(path string) (*editspec.LayoutPack, error) {
	var b []byte
	var err error
	if path == "-" {
		b, err = readPlan("-")
	} else {
		b, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("MetaRush couldn't read the pack's file (%v).", err)
	}
	var wrapped struct {
		Pack *editspec.LayoutPack `json:"pack"`
	}
	if json.Unmarshal(b, &wrapped) == nil && wrapped.Pack != nil {
		return wrapped.Pack, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	var p editspec.LayoutPack
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("The pack's file isn't written the way MetaRush reads it (%v).", err)
	}
	return &p, nil
}

func printPack(c *cobra.Command, o *ops.ContentPackOut) {
	fmt.Println(o.Summary)
	name := o.Pack.Name
	if o.Own && name != "" {
		fmt.Printf("%s, version %d:\n", theirs(name), o.Pack.Version)
	}
	for _, r := range o.Roles {
		line := fmt.Sprintf("  %s: %s", upperFirstWord(r.Words), r.What)
		switch {
		case r.Waiting:
			line += " (waits for a person's approval)"
		case r.Starter && o.Own:
			line += " (MetaRush's own: the pack leaves it out)"
		}
		fmt.Println(line)
	}
	if o.ReadOnly {
		fmt.Println("It is another project's settings: change it there, or give this project its own (metarush content own).")
	}
	for _, u := range o.LibraryUpdates {
		fmt.Printf("A newer approved version of the shared library %s is there for the %s: use it under Settings → Layout pack.\n", u.Name, strings.Join(u.Roles, ", "))
		if detailsMode(c) {
			fmt.Println("  metarush content pack set --library " + u.Version)
		}
	}
	if d := o.Draft; d != nil {
		who := ""
		if d.By != "" {
			who = " by " + d.By
		}
		fmt.Printf("\nA proposed pack%s waits for a person:\n", who)
		for _, l := range d.Lines {
			fmt.Println("  " + l)
		}
		if d.Ready {
			fmt.Println("A person can accept it (metarush content pack accept, or Settings → Layout pack).")
		} else {
			fmt.Println(d.Why)
		}
	}
	if len(o.LatestCuts) > 0 {
		fmt.Println("\nUse the layout pack on the latest cut (saved versions never change):")
		for _, cut := range o.LatestCuts {
			fmt.Printf("  %s, version %d: %s\n", theirs(cut.ProjectName), cut.Version, strings.Join(cut.Changes, " "))
		}
	}
	if detailsMode(c) && o.ProjectID != "" {
		fmt.Println("settings of:", o.ProjectID)
	}
}
