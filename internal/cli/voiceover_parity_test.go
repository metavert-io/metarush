package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/ops"
)

// Dual use: every narration, mix and integration op has a CLI command (the
// dashboard is never the only way). A new op in these families must be added
// here together with its command.
func TestNarrationMixAndProviderOpsHaveCommands(t *testing.T) {
	commandFor := map[string]string{
		"voiceover.show":     "voiceover script show",
		"voiceover.parse":    "voiceover script import",
		"voiceover.fit":      "voiceover fit",
		"voiceover.scratch":  "voiceover scratch",
		"voiceover.estimate": "voiceover estimate",
		"voiceover.tts":      "voiceover tts",
		"voiceover.voices":   "voiceover voices",
		"voiceover.models":   "voiceover models",
		"voiceover.preview":  "voiceover voices", // --preview <voice> --out <file>
		"voiceover.takes":    "voiceover takes list",
		"voiceover.choose":   "voiceover takes choose",
		"voiceover.align":    "voiceover import",
		"voiceover.addTake":  "voiceover record",
		"voiceover.cleanup":  "voiceover cleanup",
		"voiceover.clone":    "voiceover clone",
		"voiceover.line":     "voiceover beat move",   // and beat add, update, remove
		"voiceover.place":    "voiceover takes place", // also voiceover record --live
		"voiceover.fitted":   "voiceover script fit",  // --propose, --keep, --use
		"voiceover.askFit":   "voiceover script fit",  // without --propose: the headless Claude Code run
		"voiceover.use":      "voiceover script use",
		"mix.show":           "mix show",
		"mix.set":            "mix set",
		"mix.preview":        "mix set", // --preview
		"provider.list":      "providers list",
		"provider.status":    "providers status",
		"provider.set":       "providers set",
		"provider.test":      "providers test",
		"provider.remove":    "providers remove",
		"provider.settings":  "providers settings",
	}
	root := &cobra.Command{Use: "metarush"}
	for _, c := range commands() {
		root.AddCommand(c)
	}
	for _, op := range ops.All() {
		if !strings.HasPrefix(op.Name, "voiceover.") && !strings.HasPrefix(op.Name, "mix.") && !strings.HasPrefix(op.Name, "provider.") {
			continue
		}
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
	for _, path := range [][]string{{"voiceover", "beat", "add"}, {"voiceover", "beat", "update"}, {"voiceover", "beat", "remove"}} {
		if c, rest, err := root.Find(path); err != nil || len(rest) > 0 || c.Name() != path[2] {
			t.Errorf("%v missing: %v", path, err)
		}
	}
	if c, _, _ := root.Find([]string{"mix", "set"}); c.Flags().Lookup("preview") == nil || c.Flags().Lookup("apply") == nil {
		t.Error("mix set has no --preview or --apply flag")
	}
	if c, _, _ := root.Find([]string{"voiceover", "beat", "move"}); c.Flags().Lookup("to") == nil || c.Flags().Lookup("exact") == nil {
		t.Error("voiceover beat move has no --to/--exact flags")
	}
	if c, _, _ := root.Find([]string{"voiceover", "voices"}); c.Flags().Lookup("preview") == nil {
		t.Error("voiceover voices has no --preview flag")
	}
	// Live takes (D85): record --live --from, and take place --at-recorded | --planned.
	if c, _, _ := root.Find([]string{"voiceover", "record"}); c.Flags().Lookup("live") == nil || c.Flags().Lookup("from") == nil {
		t.Error("voiceover record has no --live/--from flags")
	}
	if c, rest, err := root.Find([]string{"voiceover", "take", "place"}); err != nil || len(rest) > 0 || c.Flags().Lookup("at-recorded") == nil || c.Flags().Lookup("planned") == nil || c.Flags().Lookup("from") == nil {
		t.Error("voiceover take place is missing, or has no --at-recorded/--planned/--from flags")
	}
	// Script versions (D86).
	if c, _, _ := root.Find([]string{"voiceover", "script", "fit"}); c.Flags().Lookup("propose") == nil || c.Flags().Lookup("keep") == nil {
		t.Error("voiceover script fit has no --propose/--keep flags")
	}
	if c, _, _ := root.Find([]string{"voiceover", "script", "import"}); c.Flags().Lookup("spread") == nil {
		t.Error("voiceover script import has no --spread flag")
	}
}
