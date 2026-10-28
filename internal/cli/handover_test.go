package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/metavert-io/metarush/internal/handover"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// The passphrase is never an argument: no handover command has a flag that
// takes one, and one typed as an argument is refused.
func TestHandoverPassphraseIsNeverAnArgument(t *testing.T) {
	var find func(cmds []*cobra.Command, path ...string) *cobra.Command
	find = func(cmds []*cobra.Command, path ...string) *cobra.Command {
		for _, c := range cmds {
			if c.Name() == path[0] {
				if len(path) == 1 {
					return c
				}
				return find(c.Commands(), path[1:]...)
			}
		}
		return nil
	}
	for _, p := range [][]string{{"machine", "handover", "export"}, {"machine", "handover", "receive"}, {"machine", "take-over"}} {
		c := find(commands(), p...)
		if c == nil {
			t.Fatalf("no command %v", p)
		}
		c.Flags().VisitAll(func(f *pflag.Flag) {
			if strings.Contains(f.Name, "pass") && f.Value.Type() != "bool" {
				t.Errorf("%v --%s takes a passphrase as an argument", p, f.Name)
			}
		})
	}
	a := testapp.New(t)
	asPerson(t)
	r := runCLI(t, a.Cfg.Home(), "machine", "handover", "export", "--to", "desk", "--passphrase", "correct horse battery", "--json")
	if r.code == 0 {
		t.Fatal("a passphrase as an argument was taken")
	}
}

// Making, receiving and cleaning up after a handover, and taking over, are a
// person's at their own terminal: Claude Code, an AI app and a script nobody
// watches are refused; the passphrase comes on standard input, and a wrong
// one is refused plainly.
func TestHandoverAtAPersonsTerminal(t *testing.T) {
	handover.ScryptWorkFactor = 10
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, i := range []models.Installation{
		{ID: a.Cfg.Installation.ID, WorkspaceID: a.WorkspaceID(), Name: a.Cfg.Installation.Name, State: models.InstallationActive, CreatedAt: now, LastSeenAt: now},
		{ID: ids.New(ids.Installation), WorkspaceID: a.WorkspaceID(), Name: "desk", State: models.InstallationActive, CreatedAt: now, LastSeenAt: now},
	} {
		if err := a.DB.Insert(ctx, store.CInstallations, i); err != nil {
			t.Fatal(err)
		}
	}
	h := a.Cfg.Home()
	out := filepath.Join(t.TempDir(), "h.age")
	refusals := [][]string{
		{"machine", "handover", "export", "--to", "desk", "--out", out, "--no-passphrase", "--json"},
		{"machine", "handover", "receive", out, "--json"},
		{"machine", "handover", "clean", "--yes", "--json"},
		{"machine", "take-over", "desk", "--plan", "--json"},
		{"machine", "handover", "plan", "--to", "desk", "--json"},
	}
	asPerson(t)
	t.Setenv("CLAUDECODE", "1")
	for _, args := range refusals {
		if c, _ := errorCode(t, runCLI(t, h, args...)); c != "E_HUMAN_ONLY" {
			t.Errorf("Claude Code ran %v: %s", args, c)
		}
	}
	t.Setenv("CLAUDECODE", "")
	for _, args := range refusals {
		if c, _ := errorCode(t, runCLI(t, h, append([]string{"--agent", "codex"}, args...)...)); c != "E_HUMAN_ONLY" {
			t.Errorf("an AI app ran %v: %s", args, c)
		}
	}
	old := atTerminal
	atTerminal = func() bool { return false }
	for _, args := range refusals[:4] {
		if c, _ := errorCode(t, runCLI(t, h, args...)); c != "E_HUMAN_ONLY" {
			t.Errorf("a script nobody watches ran %v: %s", args, c)
		}
	}
	atTerminal = old

	// A person at a terminal: the dry run, then the file with the passphrase
	// on standard input.
	var plan handover.HandoverPlan
	decodeData(t, runCLI(t, h, "machine", "handover", "plan", "--to", "desk", "--json"), &plan)
	if !plan.Ready || plan.To == nil || plan.To.Name != "desk" {
		t.Fatalf("plan %+v", plan)
	}
	var ex handover.HandoverExportOut
	withStdin(t, "correct horse battery staple\n", func() {
		decodeData(t, runCLI(t, h, "machine", "handover", "export", "--to", "desk", "--out", out, "--passphrase-stdin", "--json"), &ex)
	})
	if !ex.Encrypted || ex.File != out {
		t.Fatalf("export %+v", ex)
	}
	if st, err := os.Stat(out); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("file %v %v", st, err)
	}
	// A wrong passphrase is refused plainly; the right one opens it (here it
	// says the file is for another computer).
	withStdin(t, "not the passphrase at all\n", func() {
		if c, m := errorCode(t, runCLI(t, h, "machine", "handover", "receive", out, "--passphrase-stdin", "--json")); c != "E_UNAUTHORIZED" || !strings.Contains(m, "passphrase doesn't open") {
			t.Fatalf("wrong passphrase: %s %s", c, m)
		}
	})
	withStdin(t, "correct horse battery staple\n", func() {
		if c, m := errorCode(t, runCLI(t, h, "machine", "handover", "receive", out, "--passphrase-stdin", "--json")); c != "E_CONFLICT" || !strings.Contains(m, "for “desk”") {
			t.Fatalf("right passphrase on the wrong computer: %s %s", c, m)
		}
	})
	// Without a terminal and without standard input, nothing is asked.
	var st handover.HandoverStatusOut
	decodeData(t, runCLI(t, h, "machine", "handover", "status", "--json"), &st)
	if len(st.InProgress) != 1 || st.InProgress[0].State != handover.StateExported {
		t.Fatalf("status %+v", st)
	}
}
