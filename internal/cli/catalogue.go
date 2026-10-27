package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/catalog"
)

// metarush catalogue list|show (D272): what MetaRush can do as abilities —
// built in (the first-party catalogue, D269) and added to this workspace
// (D268) — each saying whether it works on this computer now or what it
// needs first.
func catalogueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "catalogue",
		Aliases: []string{"catalog"},
		Short:   "What MetaRush can do, as abilities: built in and added, each saying whether it works on this computer now",
		Long: `The menu of abilities: what MetaRush does built in (understand footage, fix sources, make media, bring things in,
shape the cut, check and deliver) and the abilities your workspace added, each saying whether it works on this
computer now or what it needs first (a key, a Mac, a program, an owner's approval, setting it up here).

  metarush catalogue list                    (everything, group by group)
  metarush catalogue list --works-now        (only what works on this computer now)
  metarush catalogue list --group fix
  metarush catalogue show steady-footage`,
	}
	cmd.AddCommand(catalogueListCmd(), catalogueShowCmd())
	return cmd
}

func catalogueListCmd() *cobra.Command {
	var in catalog.CatalogueListIn
	var added, builtIn bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List the abilities, built in and added, group by group, each saying whether it works here now or what it needs",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			switch {
			case added && builtIn:
				return failErr(c, fmt.Errorf("say --built-in or --added, not both"))
			case added:
				in.Kind = "added"
			case builtIn:
				in.Kind = "built-in"
			}
			return runOp(c, "catalogue.list", in, func(o any) { printMenu(o.(*catalog.CatalogueListOut), detailsMode(c)) })
		},
	}
	c.Flags().StringVar(&in.Group, "group", "", "only one part of the menu: understand, fix, make, bring, edit, deliver or added")
	c.Flags().BoolVar(&in.WorksNow, "works-now", false, "only what works on this computer now")
	c.Flags().BoolVar(&builtIn, "built-in", false, "only what MetaRush does built in")
	c.Flags().BoolVar(&added, "added", false, "only abilities added to this workspace")
	return c
}

func printMenu(o *catalog.CatalogueListOut, details bool) {
	group := ""
	for _, it := range o.Items {
		if it.Group != group {
			group = it.Group
			for _, g := range o.Groups {
				if g.Key == group {
					fmt.Printf("\n%s (%d of %d work here)\n", g.Title, g.WorksNow, g.Count)
				}
			}
		}
		fmt.Println("  " + it.Line)
		if details {
			fmt.Printf("    %s · %s\n", it.Name, strings.TrimSpace(it.Command+" "+fixedFlags(it.Fixed)))
		}
	}
	fmt.Println()
	fmt.Println(o.Message)
	if !details && len(o.Items) > 0 {
		fmt.Println("(Add --details for each one's name and command.)")
	}
}

func fixedFlags(fixed map[string]any) string {
	if len(fixed) == 0 {
		return ""
	}
	return "(with " + compactJSON(fixed) + ")"
}

func catalogueShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show one ability: what it does, what it needs, whether it works here, and what runs it",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "catalogue.show", catalog.CatalogueShowIn{Name: args[0]}, func(o any) {
				out := o.(*catalog.CatalogueShowOut)
				it := out.Item
				fmt.Println(out.Message)
				if it.GroupTitle != "" {
					fmt.Println("In the menu: " + it.GroupTitle + ".")
				}
				if len(it.Requires) > 0 {
					fmt.Println("It needs: " + strings.Join(it.Requires, ", ") + ".")
				} else {
					fmt.Println("It needs nothing more than MetaRush.")
				}
				if it.Partly != "" {
					fmt.Println("On this computer it does " + it.Partly + ".")
				}
				if w := costLine(it.Cost); w != "" {
					fmt.Println(w)
				}
				if it.Leaves != "" {
					fmt.Println(strings.ToUpper(it.Leaves[:1]) + it.Leaves[1:] + ".")
				}
				if it.PeopleOnly {
					fmt.Println("Only a person starts it, in the dashboard or their own terminal.")
				}
				if detailsMode(c) {
					printDescription(out.Description, true)
					fmt.Println("receipts name it: " + it.MadeWith)
				} else {
					fmt.Println("(Add --details for its name, command and input.)")
				}
			})
		},
	}
}

// costLine says what an ability costs, in a sentence; "It costs nothing."
// only for what has no price and uses no key (D268).
func costLine(cost string) string {
	switch cost {
	case catalog.CostFree:
		return "It costs nothing."
	case catalog.CostPriced:
		return "Each run is priced first and paid to the service that does it."
	case catalog.CostKeyed:
		return "It uses a key to a service, which may charge for what it does."
	case catalog.CostPlan:
		return "It uses your Claude Code, which counts toward your Claude plan."
	}
	return ""
}
