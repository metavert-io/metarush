package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ops"
)

// editTransitionsCmd prints the transition library: every transition with what
// it looks like and when to use it, the ways the sound can change at a cut, the
// easings and directions, and the per-project styles. --json is the catalog
// (the same document as schema/transitions.json).
func editTransitionsCmd() *cobra.Command {
	var details bool
	cmd := &cobra.Command{
		Use: "transitions", Short: "List the transitions (picture and sound), with when to use each, their lengths, directions and the project styles",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "edit.transitions", ops.EmptyIn{}, func(o any) {
				printTransitions(o.(*editspec.TransitionsCatalog), details)
			})
		},
	}
	cmd.Flags().BoolVar(&details, "details", false, "also show the values a plan uses")
	return cmd
}

func printTransitions(cat *editspec.TransitionsCatalog, details bool) {
	tag := func(v string) string {
		if details && v != "" {
			return " (" + v + ")"
		}
		return ""
	}
	fmt.Println("Transitions")
	for _, t := range cat.Transitions {
		fmt.Printf("  %s%s — %s\n", t.Name, tag(t.Type), t.Summary)
		var facts []string
		if t.MaxDuration > 0 {
			facts = append(facts, fmt.Sprintf("usually %s (%s to %s)", secs(t.DefaultDuration), secs(t.MinDuration), secs(t.MaxDuration)))
		}
		if len(t.Directions) > 0 {
			facts = append(facts, "moves "+strings.Join(t.Directions, ", "))
		}
		if t.Overlap {
			facts = append(facts, "both shots are on screen together")
		}
		if len(facts) > 0 {
			fmt.Printf("      %s\n", strings.Join(facts, "; "))
		}
		fmt.Printf("      When: %s\n", t.WhenToUse)
		fmt.Printf("      Sound: %s\n", t.Sound)
	}
	fmt.Println("\nSound at a cut")
	for _, m := range cat.Sound {
		fmt.Printf("  %s%s — %s\n      e.g. %s\n", m.Name, tag(m.Mode), m.Summary, m.Example)
	}
	fmt.Printf("  Sounds blend over %s at a split (new cuts also blend straight cuts over %s, so they never click).\n", secs(cat.SplitBlend), secs(cat.CutBlend))
	fmt.Println("\nPacing")
	for _, e := range cat.Easings {
		fmt.Printf("  %s%s — %s\n", e.Name, tag(e.ID), e.Summary)
	}
	fmt.Println("\nStart and end")
	for _, e := range cat.Edges {
		fmt.Printf("  %s — %s\n", e.Name, e.Summary)
		if details {
			fmt.Printf("      %s\n", e.How)
		}
	}
	fmt.Println("\nProject styles")
	if details {
		fmt.Println("  (set one with `metarush project update <project> --transition-style <style>`)")
	}
	for _, s := range cat.Styles {
		fmt.Printf("  %s%s — %s\n      Voices across cuts: %s\n", s.Name, tag(s.ID), s.Summary, s.SplitEdits)
	}
}

// styleWords names a transition style for people.
func styleWords(id string) string {
	for _, s := range editspec.Styles {
		if s.ID == id {
			return s.Name + " — " + strings.TrimSuffix(s.Summary, ".")
		}
	}
	return id
}

func secs(v float64) string {
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
	return s + " s"
}
