package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/prefs"
)

// Estimates in a terminal (D208): what an edit that uses paid outside AI
// services is expected to cost, opened up the way the dashboard opens it —
// each part's service, model, what it draws on, units and price. --details
// adds every part and the prices each one uses.

// partsShown is how many parts of one kind print before "… and N more".
const partsShown = 6

// printCost prints an estimate's parts under a heading (nothing for an
// estimate without parts).
func printCost(e costs.Estimate, details bool) { writeCost(os.Stdout, "", e, details) }

// writeCost writes an estimate's parts, each line led by indent.
func writeCost(w io.Writer, indent string, e costs.Estimate, details bool) {
	if e.Empty() {
		return
	}
	fmt.Fprintf(w, "%sWhat it draws on (an estimate, %s in all):\n", indent, e.About)
	type group struct {
		head  string
		parts []costs.Part
	}
	var groups []*group
	byHead := map[string]*group{}
	for _, p := range e.Parts {
		head := p.Service
		if p.Model != "" {
			head += ", " + p.Model
		}
		head += ": " + p.What
		if p.Step != "" {
			head = p.Step + " — " + head
		}
		g := byHead[head]
		if g == nil {
			g = &group{head: head}
			byHead[head] = g
			groups = append(groups, g)
		}
		g.parts = append(g.parts, p)
	}
	for _, g := range groups {
		fmt.Fprintf(w, "%s  %s\n", indent, g.head)
		for i, p := range g.parts {
			if !details && i == partsShown && len(g.parts) > partsShown+1 {
				rest := costs.Of(g.parts[i:]...)
				fmt.Fprintf(w, "%s    … and %d more (%s)\n", indent, len(g.parts)-i, rest.About)
				break
			}
			price := costs.About(p.USD)
			if p.Free {
				price = "free (made before)"
			}
			line := p.Units
			if p.Source != "" {
				line = p.Source + ": " + p.Units
			}
			fmt.Fprintf(w, "%s    %s — %s\n", indent, line, price)
			if details && p.Rate != "" {
				fmt.Fprintf(w, "%s      at %s\n", indent, p.Rate)
			}
		}
	}
	for _, n := range e.Notes {
		fmt.Fprintf(w, "%s  %s\n", indent, n)
	}
}

// estimateFirstHere reports whether a terminal command shows the estimate and
// asks before it spends (D208): a person at an interactive terminal whose
// estimates are on (their own choice, or an owner's for everyone). Scripts
// and Claude Code (no terminal) go on as before: within budgets and limits.
func estimateFirstHere(c *cobra.Command, a *app.App) bool {
	return isInteractive() && !jsonMode(c) && prefs.EstimatesFirstForAsker(c.Context(), a)
}
