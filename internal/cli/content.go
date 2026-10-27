package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/intake"
	"github.com/metavert-io/metarush/internal/ops"
)

// `metarush content` (D192, D193): a project's Content Settings — its brand,
// spelling list, narration voice and house rules — and whose they are: its
// own, another project's (followed live) or the default's.

func contentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "content",
		Short: "Content Settings: a project's brand, spelling list, narration voice and house rules — its own, another project's (followed) or the default's",
		Long: `Content Settings belong to a project. Each project uses the default (the project whose
"Use for new projects" is ticked), another project's (followed live: changes there show up
here; chains are followed, circles refused) or its own. New projects use the default; footage
in no project uses the default's spellings. Change the parts with their own commands:
metarush brand set, metarush transcript spelling add, metarush providers settings elevenlabs
and metarush content rules add — with --project for a project with its own settings, or
without it for the default's.

  metarush content show --project "Spring promo"
  metarush content follow fernbrook --project "Spring promo"      use fernbrook's settings, live
  metarush content follow --default --project "Spring promo" use the default settings
  metarush content own --project "Spring promo" [--copy]     its own (empty, or a copy)
  metarush content default --project fernbrook                    use fernbrook's for new projects`,
	}
	var showProject string
	show := &cobra.Command{
		Use: "show [--project <project>]", Short: "Whose Content Settings a project uses, what they hold and who else uses them (without --project: the default's)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "content.show", ops.ContentShowIn{ProjectID: showProject}, func(o any) { printContent(c, o.(*ops.ContentShowOut)) })
		},
	}
	show.Flags().StringVar(&showProject, "project", "", "the project (its name or ID); without it, the default's")
	var followProject string
	var useDefault bool
	follow := &cobra.Command{
		Use: "follow (<other project> | --default) --project <project>", Short: "Make a project use another project's settings (live), or with --default the default's", Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in := ops.ContentFollowIn{ProjectID: followProject, UseDefault: useDefault}
			if len(args) == 1 {
				in.Follow = args[0]
			}
			if in.ProjectID == "" {
				return fail(c, apperr.InvalidInput, "Say which project: add --project <project>.", "")
			}
			return runOp(c, "content.follow", in, func(o any) { printContent(c, o.(*ops.ContentShowOut)) })
		},
	}
	follow.Flags().StringVar(&followProject, "project", "", "the project that should use them")
	follow.Flags().BoolVar(&useDefault, "default", false, "use the default settings (what new projects use)")
	var ownIn ops.ContentOwnIn
	own := &cobra.Command{
		Use: "own --project <project> [--copy]", Short: "Give a project its own settings: empty (or the ones it had before), or with --copy a copy of what it uses now", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if ownIn.ProjectID == "" {
				return fail(c, apperr.InvalidInput, "Say which project: add --project <project>.", "")
			}
			return runOp(c, "content.own", ownIn, func(o any) { printContent(c, o.(*ops.ContentShowOut)) })
		},
	}
	own.Flags().StringVar(&ownIn.ProjectID, "project", "", "the project")
	own.Flags().BoolVar(&ownIn.Copy, "copy", false, "start as a copy of the settings it uses now")
	var defIn ops.ContentDefaultIn
	def := &cobra.Command{
		Use: "default --project <project>", Short: "Use a project's settings for new projects (its \"Use for new projects\"; the one default moves here)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if defIn.ProjectID == "" {
				return fail(c, apperr.InvalidInput, "Say which project's settings new projects should use: add --project <project>.", "")
			}
			return runOp(c, "content.setDefault", defIn, func(o any) { printContent(c, o.(*ops.ContentShowOut)) })
		},
	}
	def.Flags().StringVar(&defIn.ProjectID, "project", "", "the project whose settings new projects use")
	cmd.AddCommand(show, follow, own, def, houseRulesCmd(), contentLogoCmd(), contentStyleCmd(), houseSignoffsCmd(), contentPackCmd(), contentLeaveCmd(), contentGateCmd())
	return cmd
}

// `metarush content logo` (D198): the brand's five named logos.
func contentLogoCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "logo", Short: "The brand's named logos: main, for dark backgrounds, for light backgrounds, a square icon and the wordmark",
		Long: `The brand of a project's Content Settings has five named logos. Graphics that show the main
logo show the one made for their background (by contrast with the colour behind it) when
there is one; new cuts pin them as logo, logo-dark, logo-light, logo-icon and logo-wordmark.

  metarush content logo set main ~/Brand/fernbrook.svg --project fernbrook
  metarush content logo set dark fernbrook-white --project fernbrook     a picture already in the resources
  metarush content logo remove wordmark --project fernbrook`}
	var setIn ops.ContentLogoSetIn
	set := &cobra.Command{
		Use: "set <main|dark|light|icon|wordmark> <file or resource> [--project <project>]", Short: "Make a picture one of the brand's logos: a file on this computer (added to the resources first) or a resource's name", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			setIn.Which = args[0]
			if ops.LogoWhich(args[0]) == "" {
				return fail(c, apperr.InvalidInput, fmt.Sprintf("There's no logo called %q.", args[0]), "Say main, dark (for dark backgrounds), light (for light backgrounds), icon (square) or wordmark.")
			}
			setIn.Resource = args[1]
			if st, err := os.Stat(expandUserPath(args[1])); err == nil && st.Mode().IsRegular() {
				return setLogoFromFile(c, setIn, expandUserPath(args[1]))
			}
			return runOp(c, "content.logo.set", setIn, func(o any) { printLogoChange(o.(*ops.BrandShowOut)) })
		},
	}
	set.Flags().StringVar(&setIn.ProjectID, "project", "", "a project with its own settings (default: the default's, what new projects use)")
	var rmIn ops.ContentLogoRemoveIn
	rm := &cobra.Command{
		Use: "remove <main|dark|light|icon|wordmark> [--project <project>]", Short: "Take one of the brand's logos away (the picture stays in the resources; saved versions keep it)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			rmIn.Which = args[0]
			return runOp(c, "content.logo.remove", rmIn, func(o any) { printLogoChange(o.(*ops.BrandShowOut)) })
		},
	}
	rm.Flags().StringVar(&rmIn.ProjectID, "project", "", "a project with its own settings (default: the default's)")
	cmd.AddCommand(set, rm)
	return cmd
}

// expandUserPath expands a leading ~/.
func expandUserPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + p[1:]
		}
	}
	return p
}

// setLogoFromFile adds a file to the resources of the project whose settings
// hold the brand (cleaned, versioned like any picture), then names it.
func setLogoFromFile(c *cobra.Command, in ops.ContentLogoSetIn, path string) error {
	if err := assistantPath(path); err != nil {
		return fail(c, apperr.Unauthorized, err.Error(), "")
	}
	a, err := openApp(c, "cli")
	if err != nil {
		return err
	}
	defer a.Close()
	ctx, cancel := signalCtx(c.Context())
	defer cancel()
	// Whose settings: a project that uses another's is refused before anything is added.
	show, err := ops.CallTyped[ops.ContentShowOut](ctx, a, "content.show", ops.ContentShowIn{ProjectID: in.ProjectID}, ops.Caller{})
	if err != nil {
		return failErr(c, err)
	}
	if show.From == nil {
		return fail(c, apperr.NotConfigured, "No project's settings are used for new projects yet.", "Say which project's settings to change, or give a project its own settings first.")
	}
	if show.ReadOnly {
		// The plain refusal (edit them there, or give it its own): the op refuses
		// a project that uses another's settings before it looks at anything else.
		_, err := ops.CallTyped[ops.BrandShowOut](ctx, a, "content.logo.set", in, ops.Caller{})
		if err == nil {
			return fail(c, apperr.Conflict, "That project uses another project's settings.", "Change them there, or give it its own settings.")
		}
		return failErr(c, err)
	}
	added, err := ops.CallTyped[intake.AddPathsOut](ctx, a, "resources.add", intake.ResourcesAddIn{Paths: []string{path}, ProjectID: show.From.ID}, ops.Caller{})
	if err != nil {
		return failErr(c, err)
	}
	if len(added.Files) == 0 || added.Files[0].ResourceID == "" {
		msg := "That file couldn't be added."
		if len(added.Files) > 0 && added.Files[0].Message != "" {
			msg = added.Files[0].Message
		}
		return fail(c, apperr.InvalidInput, msg, "A logo is a picture (PNG, JPEG, WebP), an SVG or a PDF.")
	}
	in.Resource = added.Files[0].ResourceID
	return runOpWith(c, a, "content.logo.set", in, ops.Caller{}, func(o any) { printLogoChange(o.(*ops.BrandShowOut)) })
}

func printLogoChange(o *ops.BrandShowOut) {
	fmt.Println(o.Message)
	printLogos(o)
	if len(o.LatestCuts) > 0 {
		fmt.Println("\nUse the new brand on the latest cut (saved versions never change):")
		for _, cut := range o.LatestCuts {
			fmt.Printf("  %s, version %d\n", theirs(cut.ProjectName), cut.Version)
		}
	}
}

// `metarush content style` (D199): what new cuts start with.
func contentStyleCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "style", Short: "The house style: what new cuts of every project using the settings start with (captions, the name bar, name titles, framing, sound at cuts, an intro and an end card)",
		Long: `The house style is part of a project's Content Settings: new cuts of every project using
them start with it. With nothing set, new cuts get MetaRush's own choices. Saved versions
never change: after a change the latest cuts that don't follow it are listed, and
metarush content style apply --project <project> puts it on one as a checked change.

  metarush content style set --project fernbrook --captions on --caption-look boxed --caption-size large
  metarush content style set --project fernbrook --name-bar-height half --name-bar-animates never
  metarush content style set --project fernbrook --end-card endcard.cta --end-card-headline "Volunteer with us" --end-card-handle @fernbrook
  metarush content style apply --project "Spring promo" --dry-run`}
	var showIn ops.ContentStyleIn
	show := &cobra.Command{
		Use: "show [--project <project>]", Short: "The house style of the settings a project uses (without --project: the default's)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "content.style.show", showIn, func(o any) { printStyle(c, o.(*ops.ContentStyleOut)) })
		},
	}
	show.Flags().StringVar(&showIn.ProjectID, "project", "", "the project (its name or ID)")
	var in ops.ContentStyleSetIn
	var cutBlend float64
	var intro, introTitle, introSubtitle, end, endHeadline, endSubline, endCTA, endHandle string
	var introSecs, endSecs float64
	set := &cobra.Command{
		Use:   "set [--project <project>] [--captions on|off] [--caption-look …] [--caption-size …] [--caption-position …] [--name-bar-look band|tab] [--name-bar-height full|half] [--name-bar-animates fade|slide|never] [--name-titles clean|cinematic] [--framing faces|follow|fixed] [--cut-blend <s>] [--level-speech on|off] [--intro title.card|slide.title|none …] [--end-card endcard.cta|slide.title|none …] [--clear <part>]",
		Short: "Change the house style of a project's own settings (without --project: the default's); an empty value goes back to MetaRush's own",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			for flag, dst := range map[string]**string{"captions": &in.Captions, "caption-look": &in.CaptionLook, "caption-size": &in.CaptionSize, "caption-position": &in.CaptionPosition,
				"name-bar-look": &in.NameBarLook, "name-bar-height": &in.NameBarHeight, "name-bar-animates": &in.NameBarAnimate, "name-titles": &in.LowerThird,
				"framing": &in.Framing, "level-speech": &in.LevelSpeech} {
				if c.Flags().Changed(flag) {
					v, _ := c.Flags().GetString(flag)
					*dst = &v
				}
			}
			if c.Flags().Changed("cut-blend") {
				in.CutBlend = &cutBlend
			}
			cardFlags := func(prefix string) bool {
				for _, f := range []string{"", "-title", "-subtitle", "-headline", "-subline", "-cta", "-handle", "-seconds"} {
					if c.Flags().Lookup(prefix+f) != nil && c.Flags().Changed(prefix+f) {
						return true
					}
				}
				return false
			}
			if cardFlags("intro") {
				in.Intro = &editspec.StyleCard{Template: intro, Seconds: introSecs, Title: introTitle, Subtitle: introSubtitle}
				if intro == "" {
					return fail(c, apperr.InvalidInput, "Say which card new cuts open with: --intro title.card or slide.title (none takes it away).", "")
				}
			}
			if cardFlags("end-card") {
				in.EndCard = &editspec.StyleCard{Template: end, Seconds: endSecs, Title: endHeadline, Subtitle: endSubline, CTA: endCTA, Handle: endHandle}
				if end == "" {
					return fail(c, apperr.InvalidInput, "Say which card new cuts end with: --end-card endcard.cta or slide.title (none takes it away).", "")
				}
			}
			return runOp(c, "content.style.set", in, func(o any) { printStyle(c, o.(*ops.ContentStyleOut)) })
		},
	}
	fl := set.Flags()
	fl.StringVar(&in.ProjectID, "project", "", "a project with its own settings (default: the default's)")
	fl.String("captions", "", "on: new cuts have captions; off: they don't; empty: each cut decides")
	fl.String("caption-look", "", "clean, emphasis, boxed, bold, highlight or subtitle (metarush captions looks)")
	fl.String("caption-size", "", "small, medium or large")
	fl.String("caption-position", "", "bottom, lower, middle or top")
	fl.String("name-bar-look", "", "band (across the picture) or tab (as wide as the names)")
	fl.String("name-bar-height", "", "full or half (about half as tall; it still covers the printed names)")
	fl.String("name-bar-animates", "", "fade, slide or never (nothing moves)")
	fl.String("name-titles", "", "the name titles' look: clean or cinematic")
	fl.String("framing", "", "faces (keep faces in frame), follow (move with the subject) or fixed (the middle)")
	fl.Float64Var(&cutBlend, "cut-blend", 0.1, "seconds the sound blends over at a straight cut (0–0.5)")
	fl.String("level-speech", "", "on: voices evened out from clip to clip; off: left as recorded")
	fl.StringVar(&intro, "intro", "", "the card new cuts open with: title.card or slide.title (none takes it away)")
	fl.StringVar(&introTitle, "intro-title", "", "its title (default: each cut's title)")
	fl.StringVar(&introSubtitle, "intro-subtitle", "", "a line under it")
	fl.Float64Var(&introSecs, "intro-seconds", 0, "how long it stays (default 3)")
	fl.StringVar(&end, "end-card", "", "the card new cuts end with: endcard.cta or slide.title (none takes it away)")
	fl.StringVar(&endHeadline, "end-card-headline", "", "its headline (needed)")
	fl.StringVar(&endSubline, "end-card-subline", "", "a line under it")
	fl.StringVar(&endCTA, "end-card-cta", "", "a call to action (endcard.cta)")
	fl.StringVar(&endHandle, "end-card-handle", "", "a handle (endcard.cta)")
	fl.Float64Var(&endSecs, "end-card-seconds", 0, "how long it stays (default 4)")
	fl.StringArrayVar(&in.Clear, "clear", nil, "go back to MetaRush's own: all, captions, nameBar, lowerThird, framing, sound, cutBlend, levelSpeech, intro, endCard (repeat for more)")
	var f cutEditFlags
	apply := &cobra.Command{
		Use: "apply --project <project>", Short: "Make a project's latest cut follow the house style of the settings it uses (shows the change first)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			project, _ := c.Flags().GetString("project")
			if project == "" {
				return fail(c, apperr.InvalidInput, "Say which project's latest cut: add --project <project>.", "")
			}
			return runCutEdit(c, "content.style.apply", &f, func(common ops.CutEditCommon) any {
				common.ProjectID = project
				return ops.ContentStyleApplyIn{CutEditCommon: common}
			})
		},
	}
	apply.Flags().String("project", "", "the project whose latest cut follows the house style")
	f.add(apply)
	cmd.AddCommand(show, set, apply)
	return cmd
}

func printStyle(c *cobra.Command, o *ops.ContentStyleOut) {
	fmt.Println(o.Summary)
	for _, l := range o.Lines {
		fmt.Println("  " + l)
	}
	if o.ReadOnly {
		fmt.Println("It is another project's settings: change it there, or give this project its own (metarush content own).")
	}
	if len(o.LatestCuts) > 0 {
		fmt.Println("\nUse the house style on the latest cut (saved versions never change):")
		for _, cut := range o.LatestCuts {
			fmt.Printf("  %s, version %d: %s\n", theirs(cut.ProjectName), cut.Version, strings.Join(cut.Changes, " "))
		}
	}
	if detailsMode(c) && o.ProjectID != "" {
		fmt.Println("settings of:", o.ProjectID)
	}
}

// `metarush content signoffs` (D200): OKs every project using the settings needs.
func houseSignoffsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "signoffs", Short: "House sign-offs: OKs every project using a project's settings needs before a video goes out (a project's own are metarush cut signoffs)"}
	var listIn ops.HouseSignoffsIn
	list := &cobra.Command{
		Use: "list [--project <project>]", Short: "The house sign-offs of the settings a project uses (without --project: the default's)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "content.signoffs.list", listIn, func(o any) { printHouseSignoffs(c, o.(*ops.HouseSignoffsOut)) })
		},
	}
	list.Flags().StringVar(&listIn.ProjectID, "project", "", "the project (its name or ID)")
	var addIn ops.HouseSignoffAddIn
	add := &cobra.Command{
		Use: `add "<who OKs what>" [--project <project>]`, Short: "Add a house sign-off to a project's own settings (without --project: the default's); every project using them waits for it", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			addIn.Text = args[0]
			return runOp(c, "content.signoffs.add", addIn, func(o any) { printHouseSignoffs(c, o.(*ops.HouseSignoffsOut)) })
		},
	}
	add.Flags().StringVar(&addIn.ProjectID, "project", "", "a project with its own settings")
	var rmIn ops.HouseSignoffRemoveIn
	rm := &cobra.Command{
		Use: `remove <sign-off> [--project <project>]`, Short: "Take a house sign-off out (its number, ID or words)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			rmIn.Signoff = args[0]
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "content.signoffs.remove", rmIn, ops.Caller{Human: interactive()}, func(o any) { printHouseSignoffs(c, o.(*ops.HouseSignoffsOut)) })
		},
	}
	rm.Flags().StringVar(&rmIn.ProjectID, "project", "", "a project with its own settings")
	cmd.AddCommand(list, add, rm)
	return cmd
}

func printHouseSignoffs(c *cobra.Command, o *ops.HouseSignoffsOut) {
	fmt.Println(o.Summary)
	for i, s := range o.Signoffs {
		fmt.Printf("  %d. %s\n", i+1, theirs(s.Text))
		if detailsMode(c) {
			fmt.Println("      sign-off:", s.ID)
		}
	}
	if o.ReadOnly {
		fmt.Println("They are another project's settings: change them there, or give this project its own (metarush content own).")
	}
}

func houseRulesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "rules", Short: "House rules: rules for every video using a project's settings (a project's own rules are metarush project rules)"}
	var listIn ops.HouseRulesIn
	list := &cobra.Command{
		Use: "list [--project <project>]", Short: "The house rules of the settings a project uses (without --project: the default's)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "content.rules.list", listIn, func(o any) { printHouseRules(c, o.(*ops.HouseRulesOut)) })
		},
	}
	list.Flags().StringVar(&listIn.ProjectID, "project", "", "the project (its name or ID)")
	var addIn ops.HouseRuleAddIn
	add := &cobra.Command{
		Use: `add "<rule>" [--project <project>] [--check] [--promise] [--measure …]`, Short: "Add a house rule to a project's own settings (without --project: the default's); every project using them follows it (--promise: a promise made to the client)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			addIn.Text = args[0]
			return runOp(c, "content.rules.add", addIn, func(o any) { printHouseRules(c, o.(*ops.HouseRulesOut)) })
		},
	}
	add.Flags().StringVar(&addIn.ProjectID, "project", "", "a project with its own settings")
	add.Flags().BoolVar(&addIn.Check, "check", false, "a problem to check for in every quality check")
	add.Flags().StringVar(&addIn.Measure, "measure", "", "what the quality check measures (default: recognized from the words)")
	add.Flags().BoolVar(&addIn.Promise, "promise", false, "a promise made to the client: every quality check measures it, and a version that breaks it waits before it goes out")
	var rmIn ops.HouseRuleRemoveIn
	rm := &cobra.Command{
		Use: `remove <rule> [--project <project>]`, Short: "Take a house rule out (its number, ID or words; taking back a promise is a person's)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			rmIn.Rule = args[0]
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "content.rules.remove", rmIn, ops.Caller{Human: interactive()}, func(o any) { printHouseRules(c, o.(*ops.HouseRulesOut)) })
		},
	}
	rm.Flags().StringVar(&rmIn.ProjectID, "project", "", "a project with its own settings")
	cmd.AddCommand(list, add, rm)
	return cmd
}

func printContent(c *cobra.Command, o *ops.ContentShowOut) {
	if o.Changed != "" {
		fmt.Println(o.Changed)
	}
	fmt.Println(o.Summary)
	if len(o.Through) > 0 {
		var names []string
		for _, p := range o.Through {
			names = append(names, theirs(p.Name))
		}
		fmt.Println("Through: " + strings.Join(names, " → "))
	}
	for _, p := range o.Parts {
		fmt.Printf("  %-16s %s\n", p.Label, p.Summary)
	}
	if len(o.UsedBy) > 0 {
		var names []string
		for _, p := range o.UsedBy {
			names = append(names, theirs(p.Name))
		}
		fmt.Println("Also used by: " + strings.Join(names, ", "))
	}
	if o.Default != nil && !o.IsDefault {
		fmt.Println("New projects use " + theirs(o.Default.Name) + "'s settings.")
	}
	if o.KeptAside {
		fmt.Println("Its own settings from before are kept aside: metarush content own brings them back.")
	}
	if detailsMode(c) {
		fmt.Println("uses:", o.Uses)
		if o.From != nil {
			fmt.Println("settings of:", o.From.ID)
		}
		if o.Settings.ID != "" {
			fmt.Println("settings record:", o.Settings.ID)
		}
	}
}

func printHouseRules(c *cobra.Command, o *ops.HouseRulesOut) {
	fmt.Println(o.Summary)
	for i, r := range o.Rules {
		line := fmt.Sprintf("  %d. %s", i+1, theirs(r.Text))
		switch {
		case r.Promise:
			line += " (a promise to the client: a version that breaks it waits before it goes out)"
		case r.Check:
			line += " (checked in every quality check)"
		}
		fmt.Println(line)
		if detailsMode(c) {
			fmt.Println("      rule:", r.ID)
		}
	}
	if o.ReadOnly {
		fmt.Println("They are another project's settings: change them there, or give this project its own (metarush content own).")
	}
}
