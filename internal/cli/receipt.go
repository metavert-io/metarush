package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/receipt"
)

// projectReceiptCmd is `metarush project receipt` (D240): every request to an
// outside AI service, Claude's conversations and whose account answered,
// generated media, Auto's spending, and the cost of the video at cost.
func projectReceiptCmd() *cobra.Command {
	var in receipt.ProjectReceiptIn
	cmd := &cobra.Command{
		Use:   "receipt <project> [--version <rN>]",
		Short: "A project's receipt: what went to outside AI services and Claude, what it cost at cost (per version and project), generated media and Auto's spending",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ProjectID = args[0]
			return runOp(c, "project.receipt", in, func(o any) {
				out := o.(*receipt.ProjectReceiptOut)
				for _, l := range out.Lines {
					fmt.Println(l)
				}
				if len(out.Requests) > 0 {
					fmt.Println("Requests to outside AI services:")
					for _, r := range out.Requests {
						line := fmt.Sprintf("  %s  %s, %s", r.At.Local().Format("2 Jan 15:04"), r.What, r.Service)
						if r.Model != "" {
							line += " (" + r.Model + ")"
						}
						line += fmt.Sprintf(" · %s · %s", moneyLine(r.USD, r.Basis), r.Why)
						fmt.Println(line)
						if len(r.Sent) > 0 {
							fmt.Println("      sent: " + strings.Join(r.Sent, "; "))
						}
						fmt.Println("      " + r.DeletedSays)
					}
				}
				if v := out.Cost.Version; v != nil && len(v.Parts) > 0 {
					fmt.Printf("Version %d at cost:\n", out.Cost.Number)
					for _, p := range v.Parts {
						fmt.Printf("  %s, %s: %s\n", p.What, p.Service, moneyLine(p.USD, "charged"))
					}
				}
				fmt.Println(out.Cost.Note)
			})
		},
	}
	cmd.Flags().StringVar(&in.Revision, "version", "", "the version whose cost to show (rN or its ID); default the latest")
	cmd.Flags().StringVar(&in.Branch, "branch", "", "the cut's branch (default main)")
	return cmd
}

func moneyLine(usd float64, basis string) string {
	switch {
	case basis == "none" || usd <= 0:
		return "nothing charged"
	case usd < 0.01:
		return "less than a cent"
	case basis == "estimate":
		return fmt.Sprintf("about $%.2f", usd)
	}
	return fmt.Sprintf("$%.2f", usd)
}
