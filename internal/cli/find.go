package cli

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/catalog"
)

// opsFindCmd is `metarush ops find`: what does a task, from words describing
// it (D221). Plain lines say what each does in the command's own words; the
// operation's name and command are details.
func opsFindCmd() *cobra.Command {
	var in catalog.FindIn
	cmd := &cobra.Command{
		Use:   "find <what you want to do>...",
		Short: "Find what does a task, from a few words describing it: the best few first, one line each",
		Long: `Finds what MetaRush can do for a task you describe in plain words ("make a review copy", "who is
speaking in this clip"), best first: its operations, and any other catalogue that joins find. The same
words always give the same answer; nothing is sent anywhere and nothing changes. --details adds each
one's name and command; "metarush ops describe <name>" says how to use one.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Task = strings.Join(args, " ")
			return runOp(c, "ops.find", in, func(o any) { printFind(o.(*catalog.FindOut), detailsMode(c)) })
		},
	}
	cmd.Flags().IntVar(&in.Limit, "limit", 0, "how many to list (8 unless asked, at most 20)")
	cmd.Flags().BoolVar(&in.ReadOnly, "read-only", false, "only what changes nothing")
	cmd.Flags().StringSliceVar(&in.Kinds, "kind", nil, "only these catalogues (operation, first-party, ability)")
	return cmd
}

func printFind(o *catalog.FindOut, details bool) {
	if len(o.Hits) == 0 {
		fmt.Printf("Nothing MetaRush does matches %s. Try other words for it.\n", theirs(o.Task))
		return
	}
	fmt.Printf("For %s, best first:\n", theirs(o.Task))
	for i, h := range o.Hits {
		about := h.About
		if c, ok := catalog.CommandFor(h.Name); ok && c.Help != "" {
			about = plainHelp(c.Help) // the command's help is written for people
		}
		e := catalog.Entry{About: about, Changes: h.Changes, PeopleOnly: h.PeopleOnly, OwnerOnly: h.OwnerOnly, ThisComputerOnly: h.ThisComputerOnly}
		if !details {
			fmt.Printf("%d. %s%s\n", i+1, strings.TrimSuffix(catalog.Short(about), "."), catalog.Needs(e))
			continue
		}
		fmt.Printf("%d. %s\n", i+1, h.Line)
		if h.Command != "" {
			fmt.Printf("   %s\n", h.Command)
		}
	}
	if !details {
		fmt.Println("(Add --details to see how to run each one.)")
	}
}

// opsDescribeCmd is `metarush ops describe`: how to use one thing find
// returned.
func opsDescribeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "describe <name>",
		Short: "Say how to use one thing find returned: what it does, its input with an example, what it returns and its command",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "ops.describe", catalog.DescribeEntryIn{Name: args[0]}, func(o any) { printDescription(o.(*catalog.Description), detailsMode(c)) })
		},
	}
}

func printDescription(d *catalog.Description, details bool) {
	about := d.About
	if c, ok := catalog.CommandFor(d.Name); ok && c.Help != "" && !details {
		about = plainHelp(c.Help)
	}
	fmt.Println(strings.TrimSuffix(about, ".") + ".")
	switch {
	case d.PeopleOnly:
		fmt.Println("Only a person can do this, in the dashboard or their own terminal.")
	case d.Changes:
		fmt.Println("It changes something.")
	default:
		fmt.Println("It only looks; nothing changes.")
	}
	if d.OwnerOnly {
		fmt.Println("Only an owner of this MetaRush can do it.")
	}
	if d.ThisComputerOnly {
		fmt.Println("It works only on the computer that has the files.")
	}
	if !details {
		fmt.Println("(Add --details for its name, command and input.)")
		return
	}
	fmt.Printf("name: %s (%s)\n", d.Name, d.Kind)
	if d.Usage != "" {
		fmt.Println("command: " + d.Usage)
	}
	if len(d.Example) > 0 {
		fmt.Println("example input: " + compactJSON(d.Example))
	}
	if len(d.Returns) > 0 {
		var names []string
		for _, r := range d.Returns {
			names = append(names, r.Name)
		}
		fmt.Println("returns: " + strings.Join(names, ", "))
	}
	if d.RunWith != "" {
		fmt.Printf("runs through: %s %s\n", d.RunWith, compactJSON(d.RunInput))
	}
}

// compactJSON is v as JSON on one line (HTML characters left as they are).
func compactJSON(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSpace(b.String())
}

// flagAside is a help line's aside about its flags: "(with --wait, wait
// until the video is ready)".
var flagAside = regexp.MustCompile(`\s*\([^()]*--[a-z][^()]*\)`)

// plainHelp is a command's help line without its asides about flags, which
// belong to the command just typed, not to a list of what does a task.
func plainHelp(help string) string {
	return strings.TrimSpace(flagAside.ReplaceAllString(help, ""))
}
