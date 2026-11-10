package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/abilities"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/tools"
)

// Community catalogues of abilities (D480–D487): `metarush ability
// catalogues list|add|remove|make`, `metarush ability browse` and `metarush
// ability import --from-catalogue` (also `metarush abilities …`). Listing and
// browsing are open to anyone, Claude included; trusting a catalogue is an
// owner's step and bringing an ability in a person's, at their own terminal.

func abilityCataloguesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "catalogues",
		Aliases: []string{"catalogue", "catalog", "catalogs"},
		Short:   "Community catalogues of abilities: the ones this workspace trusts (owners add and remove them) and making a catalogue's index",
		Long: `A community catalogue is a public list of abilities (an index served over HTTPS, often from a GitHub repository).
An owner trusts a catalogue; people browse it and bring abilities in from it. What a catalogue lists is its own word:
an ability brought in is checked on a computer here in the sandbox, and an owner approves it, before anyone runs it.

  metarush ability catalogues list
  metarush ability catalogues add https://raw.githubusercontent.com/acme/abilities/main/index.json   (owners)
  metarush ability catalogues remove acme                                                             (owners)
  metarush ability catalogues make ./abilities --out index.json    (for a catalogue's maintainer)`,
	}
	var details bool
	cmd.Args = cobra.NoArgs
	cmd.RunE = func(c *cobra.Command, _ []string) error { return listCatalogues(c, details) } // alone, it lists them
	addDetails(cmd, &details)
	cmd.AddCommand(abilityCataloguesListCmd(), abilityCataloguesAddCmd(), abilityCataloguesRemoveCmd(), abilityCataloguesMakeCmd())
	return cmd
}

// listCatalogues prints the catalogues this workspace trusts.
func listCatalogues(c *cobra.Command, details bool) error {
	return runOp(c, "ability.catalogues", abilities.AbilityCataloguesIn{}, func(o any) {
		out := o.(*abilities.AbilityCataloguesOut)
		for _, r := range out.Catalogues {
			printCatalogueRow(r, details)
		}
		for _, s := range out.Suggested {
			if !s.Trusted {
				fmt.Println("Suggested on this computer: " + s.URL + " (an owner adds it to trust it)")
			}
		}
		fmt.Println(out.Message)
		if len(out.Catalogues) > 0 {
			fmt.Println(out.Note)
		}
	})
}

// personAtTheKeys: trusting a catalogue and bringing an ability in from one
// are a person's (D486): never Claude Code or another AI app, never a script
// nobody watches.
func personAtTheKeys(c *cobra.Command, what string) error {
	if agentAtTheKeys(c) || !atTerminal() {
		return failErr(c, apperr.New(apperr.HumanOnly, "Only a person %s, at their own terminal: Claude, Auto and AI apps never do.", what).
			WithHint("Run the command yourself in a terminal, or use Library → Abilities in the dashboard."))
	}
	return nil
}

func printCatalogueRow(r abilities.CatalogueRow, details bool) {
	fmt.Println(theirsTitle(r.Title, r.Key) + fmt.Sprintf(" — %s listed, called %s here", pluralWords(r.Abilities, "ability", "abilities"), r.Key))
	if r.About != "" {
		fmt.Println("  " + theirs(r.About))
	}
	if r.Maintainer != "" {
		fmt.Println("  Kept by " + theirs(r.Maintainer) + " (its word).")
	}
	// Why a listing was left out and why a read failed are MetaRush's words
	// with only the catalogue's plain words in them, in quotes; cleaned again
	// here all the same.
	if n := len(r.LeftOut); n > 0 {
		fmt.Printf("  MetaRush left %s out:\n", pluralWords(n, "listing", "listings"))
		for _, l := range r.LeftOut {
			fmt.Printf("    %s: %s\n", ops.CleanText(l.Name), ops.CleanText(l.Why))
		}
	}
	if r.Problem != "" {
		fmt.Println("  The last time MetaRush asked: " + ops.CleanText(r.Problem) + " Its copy before stays in use.")
	}
	if details {
		fmt.Printf("  %s · version %d of %s", r.URL, r.Version, r.Date)
		if r.FetchedAt != nil {
			fmt.Printf(" · read %s", r.FetchedAt.Local().Format("2 Jan 2006 15:04"))
		}
		fmt.Println()
	}
}

func theirsTitle(title, key string) string {
	if title == "" {
		return key
	}
	return theirs(title)
}

func abilityCataloguesListCmd() *cobra.Command {
	var details bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List the catalogues of abilities this workspace trusts, and the ones this computer suggests",
		Args:  cobra.NoArgs,
		RunE:  func(c *cobra.Command, _ []string) error { return listCatalogues(c, details) },
	}
	addDetails(c, &details)
	return c
}

func abilityCataloguesAddCmd() *cobra.Command {
	var in abilities.AbilityCatalogueAddIn
	var yes bool
	c := &cobra.Command{
		Use:   "add <https-address> [--name short-name]",
		Short: "Trust a community catalogue of abilities (owners, at their own terminal): MetaRush reads its index first and says what it lists",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.URL = args[0]
			if err := personAtTheKeys(c, "trusts a catalogue of abilities"); err != nil {
				return err
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			human := ops.Caller{Human: true}
			if !yes && !jsonMode(c) {
				dry := in
				dry.DryRun = true
				look, err := ops.CallTyped[abilities.AbilityCatalogueAddOut](c.Context(), a, "ability.catalogues.add", dry, human)
				if err != nil {
					return failErr(c, err)
				}
				var page screen // a catalogue's words, before a person decides
				page.show(func() {
					printCatalogueRow(look.Catalogue, true)
					fmt.Println(look.Note)
				})
				if !page.yes("Trust it", false) {
					return fail(c, apperr.Canceled, "Nothing was added.", "")
				}
			}
			return runOpWith(c, a, "ability.catalogues.add", in, human, func(o any) {
				out := o.(*abilities.AbilityCatalogueAddOut)
				fmt.Println(out.Message)
				if out.Next != "" {
					fmt.Println(out.Next)
				}
			})
		},
	}
	c.Flags().StringVar(&in.Name, "name", "", "its short name here (default: the catalogue's own)")
	c.Flags().BoolVar(&yes, "yes", false, "don't ask first (it still takes a person at a terminal)")
	return c
}

func abilityCataloguesRemoveCmd() *cobra.Command {
	var in abilities.AbilityCatalogueRemoveIn
	var yes bool
	c := &cobra.Command{
		Use:   "remove <catalogue>",
		Short: "Stop trusting a community catalogue of abilities (owners, at their own terminal); abilities brought in from it stay",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Catalogue = args[0]
			if err := personAtTheKeys(c, "stops trusting a catalogue of abilities"); err != nil {
				return err
			}
			if !yes && !jsonMode(c) && !askYes("Stop trusting "+args[0]+"? Abilities brought in from it stay", false) {
				return fail(c, apperr.Canceled, "Nothing was removed.", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "ability.catalogues.remove", in, ops.Caller{Human: true}, func(o any) {
				fmt.Println(o.(*abilities.AbilityCatalogueRemoveOut).Message)
			})
		},
	}
	c.Flags().BoolVar(&yes, "yes", false, "don't ask first (it still takes a person at a terminal)")
	return c
}

func abilityCataloguesMakeCmd() *cobra.Command {
	var in abilities.MakeIn
	c := &cobra.Command{
		Use:   "make <folder> --out index.json",
		Short: "Make a catalogue's index from a folder of ability folders (or of repositories with them): each pinned to its repository's commit and its archive's fingerprint",
		Long: `For a catalogue's maintainer: every folder with an ability.yaml under <folder> is checked as "ability check" checks
it (its manifest; no keys, links or installed packages), pinned to its git repository's current commit — only
when that commit holds exactly the folder's files — and listed from its own manifest. The index says the
catalogue's name, title and version (one more than the index at --out when anything changed). Nothing is
fetched or published: push the commits, then publish the index (docs/ability-catalogue.md).

  metarush ability catalogues make ./abilities --out index.json --name acme --title "Acme's abilities"
  metarush ability catalogues make ./abilities --out index.json            (again: the same name and title)`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Folder = args[0]
			if strings.TrimSpace(in.Out) == "" && !in.DryRun {
				return fail(c, apperr.InvalidInput, "Say where to write the index (--out index.json), or look first with --dry-run.", "")
			}
			ts := tools.New(config.Tools{})
			if h, err := home(); err == nil {
				if cfg, err := config.Load(h); err == nil {
					ts = tools.New(cfg.Tools)
				}
			}
			out, err := abilities.MakeIndex(c.Context(), ts, in)
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() {
				for _, l := range out.Listed {
					fmt.Printf("%s — %s, %s\n", l.Name, l.Folder, l.Source)
				}
				for _, l := range out.LeftOut {
					fmt.Printf("left out %s: %s\n", l.Name, l.Why)
				}
				for _, w := range out.Warnings {
					fmt.Println("note: " + w)
				}
				fmt.Println(out.Message)
				if out.Written {
					fmt.Println("  " + out.File)
				}
			})
		},
	}
	c.Flags().StringVar(&in.Out, "out", "", "the index to write (read first: its name, title and version carry on)")
	c.Flags().StringVar(&in.Name, "name", "", "the catalogue's short name: lowercase letters, digits and dashes")
	c.Flags().StringVar(&in.Title, "title", "", "the catalogue's title")
	c.Flags().StringVar(&in.About, "about", "", "one line about the catalogue")
	c.Flags().StringVar(&in.Publisher, "publisher", "", "who publishes the abilities: a GitHub account or an https address (default: each repository's owner)")
	c.Flags().StringVar(&in.Maintainer, "maintainer", "", "who keeps the catalogue: a GitHub account or an https address")
	c.Flags().StringVar(&in.Repository, "repository", "", "the repository's address on GitHub (default: its remote called origin)")
	c.Flags().StringVar(&in.Commit, "commit", "", "pin every ability to this commit (in full); the folders must be the same there")
	c.Flags().IntVar(&in.Version, "version", 0, "the index's version (default: one more than the one at --out when anything changed)")
	c.Flags().StringVar(&in.Date, "date", "", "the index's date, like 2026-11-03 (default: today)")
	c.Flags().BoolVar(&in.Force, "force", false, "write over a file at --out that isn't an ability catalogue")
	c.Flags().BoolVar(&in.DryRun, "dry-run", false, "say what the index would list; write nothing")
	return c
}

func abilityBrowseCmd() *cobra.Command {
	var in abilities.AbilityBrowseIn
	var details bool
	c := &cobra.Command{
		Use:   "browse [words…]",
		Short: "Browse the abilities the trusted catalogues list: what each does and needs, its publisher and licence, whether it is here already (listed, not checked by MetaRush)",
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) > 0 {
				in.Search = strings.TrimSpace(in.Search + " " + strings.Join(args, " "))
			}
			return runOp(c, "ability.browse", in, func(o any) {
				out := o.(*abilities.AbilityBrowseOut)
				for _, l := range out.Abilities {
					printListed(l, details)
				}
				for _, r := range out.Catalogues {
					if r.Problem != "" {
						fmt.Printf("%s: %s Its copy before stays in use.\n", theirsTitle(r.Title, r.Key), ops.CleanText(r.Problem))
					}
				}
				fmt.Println(out.Message)
				if len(out.Abilities) > 0 {
					fmt.Println(out.Note)
				}
			})
		},
	}
	c.Flags().StringVar(&in.Catalogue, "catalogue", "", "only this catalogue (its short name here or its address)")
	c.Flags().StringVar(&in.Search, "search", "", "only abilities that match these words")
	c.Flags().StringVar(&in.Name, "name", "", "one ability: name or catalogue:name")
	c.Flags().BoolVar(&in.Refresh, "refresh", false, "read the catalogues again now")
	c.Flags().IntVar(&in.Limit, "limit", 0, "at most this many (default 100)")
	addDetails(c, &details)
	return c
}

func printListed(l abilities.ListedAbility, details bool) {
	e := l.Entry
	fmt.Printf("%s (%s) — in %s", theirs(e.Title), e.Name, theirsTitle(l.CatalogueTitle, l.Catalogue))
	if l.Publisher != "" {
		fmt.Printf(", listed by %s", theirs(l.Publisher))
	}
	fmt.Println()
	fmt.Println("  " + theirs(e.Does))
	fmt.Println("  What it reaches: " + l.Reaches)
	fmt.Println("  What leaves this computer: " + l.Leaves)
	fmt.Println("  " + l.Makes + " " + l.AIWords)
	fmt.Println("  What each computer needs:")
	for _, n := range l.Needs {
		fmt.Println("    " + n)
	}
	fmt.Println("  " + l.CostWords + " Licence: " + theirs(e.Licence) + ".")
	if e.Terms != "" {
		fmt.Println("  Its service's terms: " + theirs(e.Terms))
	}
	for _, w := range l.Warnings {
		fmt.Println("  Note: " + w)
	}
	switch {
	case l.Revoked != "":
		fmt.Println("  The catalogue took this version back: " + theirs(l.Revoked))
	case l.Here != nil:
		fmt.Printf("  Here already: version %d (%s).\n", l.Here.Number, l.Here.StateLabel)
	case l.Other != nil:
		fmt.Printf("  Another version of it is here: version %d (%s).\n", l.Other.Number, l.Other.StateLabel)
	}
	if details {
		fmt.Printf("  Its files: %s", l.SourceWords)
		if l.SourcePage != "" {
			fmt.Printf(" (%s)", l.SourcePage)
		}
		fmt.Println()
		if l.CanBringIn {
			fmt.Println("  Bring it in: metarush ability import --from-catalogue " + l.Ref)
		}
	}
}

// abilityImportFromCatalogue brings an ability in from a trusted catalogue:
// a person at their own terminal, after seeing what would come in.
func abilityImportFromCatalogue(c *cobra.Command, in abilities.AbilityCatalogueImportIn, yes bool) error {
	if err := personAtTheKeys(c, "brings an ability in from a catalogue"); err != nil {
		return err
	}
	a, err := openApp(c, "cli")
	if err != nil {
		return err
	}
	defer a.Close()
	human := ops.Caller{Human: true}
	say := func(o any) {
		out := o.(*abilities.AbilityImportOut)
		fmt.Println(out.Message)
		if out.Lineage != "" {
			fmt.Println("  " + out.Lineage)
		}
		if f := out.From; f != nil && f.Catalogue != nil && f.Catalogue.Publisher != "" {
			fmt.Println("  Listed by " + theirs(f.Catalogue.Publisher) + " (the catalogue's word).")
		}
		fmt.Println("  What leaves this computer when it runs: " + out.Leaves)
		if out.Next != "" {
			fmt.Println(out.Next)
		}
	}
	if !in.DryRun && !yes && !jsonMode(c) {
		dry := in
		dry.DryRun = true
		look, err := ops.CallTyped[abilities.AbilityImportOut](c.Context(), a, "ability.catalogues.import", dry, human)
		if err != nil {
			return failErr(c, err)
		}
		var page screen // a catalogue's words, before a person decides
		page.show(func() {
			say(look)
			if !look.New {
				return
			}
			if look.Of {
				fmt.Printf("  It comes in as a new version of the ability called %s here.\n", look.Name)
			}
			fmt.Println(abilities.ListedNote)
		})
		if !look.New {
			return nil
		}
		if !page.yes("Bring it in", false) {
			return fail(c, apperr.Canceled, "Nothing came in.", "")
		}
		// What comes in is exactly the version shown: if the catalogue lists
		// another one by now, nothing does.
		pinned, err := abilities.PinListedRef(in.From, look.Version)
		if err != nil {
			return failErr(c, err)
		}
		in.From = pinned
	}
	return runOpWith(c, a, "ability.catalogues.import", in, human, say)
}
