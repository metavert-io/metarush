package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/skillsinstall"
)

func cliTree() *cobra.Command {
	root := &cobra.Command{Use: "metarush"}
	root.PersistentFlags().Bool("json", false, "")
	for _, c := range commands() {
		root.AddCommand(c)
	}
	return root
}

func find(root *cobra.Command, path string) *cobra.Command {
	c, rest, err := root.Find(strings.Fields(path))
	if err != nil || len(rest) > 0 || c.CommandPath() != "metarush "+path {
		return nil
	}
	return c
}

// Every speaker, people and to-do operation has a dedicated command (dual use:
// the dashboard is never the only way to do something).
func TestSpeakerPeopleTodoOpsHaveCommands(t *testing.T) {
	root := cliTree()
	byOp := map[string]string{
		"footage.speakers": "footage speakers", "footage.speakers.samples": "footage speakers", "footage.speakers.rename": "footage speakers rename",
		"footage.speakers.reject": "footage speakers reject", "footage.speakers.hint": "footage speakers hint",
		"people.list": "people list", "people.show": "people show", "people.rename": "people rename", "people.merge": "people merge", "people.split": "people split",
		"todos.list": "todos list", "todos.show": "todos show", "todos.answer": "todos answer", "todos.dismiss": "todos dismiss",
		"todos.counts": "todos count", "todos.sync": "todos sync", "asset.rights": "asset rights",
	}
	for op, path := range byOp {
		if _, ok := ops.Get(op); !ok {
			t.Errorf("op %s is not registered", op)
		}
		if find(root, path) == nil {
			t.Errorf("op %s has no command `metarush %s`", op, path)
		}
	}
	for path, flag := range map[string]string{"footage speakers": "samples", "footage speakers list": "samples", "footage transcript": "speaker", "footage speakers hint": "count", "todos answer": "suggestion"} {
		c := find(root, path)
		if c == nil || c.Flags().Lookup(flag) == nil {
			t.Errorf("`metarush %s` lacks --%s", path, flag)
		}
	}
}

// Claude Code allowlists read-only commands as prefixes ("Bash(cmd *)"), so an
// allowlisted command must not have subcommands that change things.
func TestReadOnlyAllowlistIsNotAPrefixOfMutatingCommands(t *testing.T) {
	root := cliTree()
	allowed := map[string]bool{}
	for _, c := range skillsinstall.ReadOnlyCommands {
		allowed[c] = true
	}
	known := map[string]bool{"metarush schema export": true} // writes files only; predates this rule
	for _, ro := range skillsinstall.ReadOnlyCommands {
		c := find(root, strings.TrimPrefix(ro, "metarush "))
		if c == nil {
			if ro == "metarush version" || ro == "metarush schema" {
				continue
			}
			t.Errorf("allowlisted command %q does not exist", ro)
			continue
		}
		var walk func(*cobra.Command)
		walk = func(x *cobra.Command) {
			for _, sub := range x.Commands() {
				if p := sub.CommandPath(); !allowed[p] && !known[p] && !strings.HasPrefix(p, "metarush schema ") {
					t.Errorf("%q is allowlisted, so its subcommand %q would run without asking", ro, p)
				}
				walk(sub)
			}
		}
		walk(c)
	}
	for _, mutating := range []string{"metarush footage speakers rename", "metarush footage speakers hint", "metarush todos answer", "metarush people merge", "metarush asset rights"} {
		for _, ro := range skillsinstall.ReadOnlyCommands {
			if mutating == ro || strings.HasPrefix(mutating, ro+" ") {
				t.Errorf("%q is covered by allowlisted %q", mutating, ro)
			}
		}
	}
}
