package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/ops"
)

// Dual use: every conversation op (D99–D103) has a CLI command with the flags
// the dashboard's controls use. A new conversation op must be added here with
// its command.
func TestConversationOpsHaveCommands(t *testing.T) {
	commandFor := map[string]string{
		"conversation.create":   "conversation create",
		"conversation.list":     "conversation list",
		"conversation.show":     "conversation show",
		"conversation.sync":     "conversation sync",
		"conversation.nudge":    "conversation nudge",
		"conversation.update":   "conversation update",
		"conversation.pin":      "conversation pin",
		"conversation.cut":      "conversation cut",
		"conversation.angle":    "conversation angle",
		"conversation.styles":   "conversation styles",
		"conversation.settings": "conversation settings",
		"conversation.cleanup":  "conversation cleanup",
		"conversation.decide":   "conversation decide",
		"conversation.match":    "conversation match",
		"conversation.import":   "conversation import",
		"conversation.detect":   "conversation detect",
		"conversation.moments":  "conversation moments",
		"conversation.short":    "conversation short",
	}
	root := &cobra.Command{Use: "metarush"}
	for _, c := range commands() {
		root.AddCommand(c)
	}
	seen := 0
	for _, op := range ops.All() {
		if !strings.HasPrefix(op.Name, "conversation.") {
			continue
		}
		seen++
		path, ok := commandFor[op.Name]
		if !ok {
			t.Errorf("op %s has no CLI command listed here", op.Name)
			continue
		}
		c, rest, err := root.Find(strings.Fields(path))
		if err != nil || len(rest) > 0 || c.CommandPath() != "metarush "+path {
			t.Errorf("op %s: command %q not found (got %v, rest %v, err %v)", op.Name, path, c.CommandPath(), rest, err)
		}
	}
	if seen != len(commandFor) {
		t.Errorf("%d conversation ops registered, %d listed", seen, len(commandFor))
	}
	flags := map[string][]string{
		"conversation nudge":    {"member", "by", "frames", "to"},
		"conversation sync":     {"clap", "mark", "together"},
		"conversation cut":      {"style", "shape", "from", "to", "interviewer", "questions", "broll", "layout", "captions", "no-name-tags", "apply", "confirm"},
		"conversation angle":    {"at", "member", "layout", "members", "card", "broll", "dissolve", "remove", "apply"},
		"conversation settings": {"quieter", "depth", "name-tags", "member", "crop", "no-crop", "apply"},
		"conversation decide":   {"accept", "skip", "reopen", "apply"},
		"conversation match":    {"reference", "off", "apply"},
		"conversation short":    {"from", "to", "shape", "nest", "apply"},
		"conversation cleanup":  {"kinds", "dead-air"},
	}
	for path, fs := range flags {
		c, _, err := root.Find(strings.Fields(path))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		for _, f := range fs {
			if c.Flags().Lookup(f) == nil {
				t.Errorf("%s has no --%s", path, f)
			}
		}
	}
}
