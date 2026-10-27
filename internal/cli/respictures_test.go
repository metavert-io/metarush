package cli

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// The pictures a repository keeps from its folders (D216) in the terminal:
// `resources add <repo> --pictures`, `resources pictures` (plain words and
// --json) and --keep/--drop, waiting for the pictures to come in.
func TestRepositoryPicturesInTheTerminal(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git isn't installed")
	}
	base := t.TempDir()
	for _, step := range [][]string{nil, {"art"}} {
		args := append([]string{filepath.Join("..", "..", "scripts", "make-fixture-repo.sh"), base}, step...)
		if out, err := exec.Command("bash", args...).CombinedOutput(); err != nil {
			t.Fatalf("fixture repo: %v %s", err, out)
		}
	}
	t.Setenv("METARUSH_GIT_BASE", base)
	a := testapp.New(t)
	home := a.Cfg.Home()
	p, err := ops.CallTyped[ops.ProjectOut](context.Background(), a, "project.create", ops.ProjectCreateIn{Name: "Teaser"}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	plain := func(r cliRun) {
		t.Helper()
		t.Logf("$ metarush %s\n%s", strings.Join(r.args, " "), r.stdout)
		if r.code != 0 {
			t.Fatalf("%v: exit %d\n%s%s", r.args, r.code, r.stdout, r.stderr)
		}
		for _, line := range strings.Split(r.stdout, "\n") {
			if fs := plainAllow.Filter(plainwords.CheckShown(line)); len(fs) > 0 {
				t.Errorf("%v: %q: %v", r.args, line, fs)
			}
		}
	}
	add := runCLI(t, home, "resources", "add", "https://github.com/acme/widget", "--project", p.Project.ID, "--pictures", "art/characters/more", "--wait")
	plain(add)
	if !strings.Contains(add.stdout, "Kept 1 picture from “art/characters/more”: 1 new.") {
		t.Fatalf("add:\n%s", add.stdout)
	}
	list := runCLI(t, home, "resources", "pictures", "widget", "--project", p.Project.ID)
	plain(list)
	if !strings.Contains(list.stdout, "1 picture kept from “art/characters/more”") || !strings.Contains(list.stdout, "widget-dot") {
		t.Fatalf("pictures:\n%s", list.stdout)
	}
	keep := runCLI(t, home, "resources", "pictures", "widget", "--project", p.Project.ID, "--keep", "art/characters", "--wait")
	plain(keep)
	if !strings.Contains(keep.stdout, "Keeping the pictures in “art/characters” of acme/widget as it was on 12 Oct 2026") || !strings.Contains(keep.stdout, "5 pictures kept from “art/characters”") ||
		!strings.Contains(keep.stdout, "Left out:") || strings.Count(keep.stdout, "Git LFS") != 1 {
		t.Fatalf("keep:\n%s", keep.stdout)
	}
	js := runCLI(t, home, "--json", "resources", "pictures", "widget", "--project", p.Project.ID)
	var env struct {
		OK   bool                `json:"ok"`
		Data ops.RepoPicturesOut `json:"data"`
	}
	out := &env.Data
	if err := json.Unmarshal([]byte(js.stdout), &env); err != nil || !env.OK || out.Kept != 5 || len(out.Pictures) != 5 || out.Pictures[0].Path != "art/characters/alice.png" || out.Commit == "" {
		t.Fatalf("--json %v %+v\n%s", err, out, js.stdout)
	}
	drop := runCLI(t, home, "resources", "pictures", "widget", "--project", p.Project.ID, "--drop", "art/characters")
	plain(drop)
	if !strings.Contains(drop.stdout, "No longer keeps “art/characters” in step") || strings.Contains(drop.stdout, "Left out") {
		t.Fatalf("drop:\n%s", drop.stdout)
	}
	// Keeping it again, waiting, as JSON: what it keeps once the pictures are in, and the fetch.
	again := runCLI(t, home, "--json", "resources", "pictures", "widget", "--project", p.Project.ID, "--keep", "art/characters", "--wait")
	var waited struct {
		Data struct {
			ops.RepoPicturesOut
			Jobs []struct {
				State string `json:"state"`
			} `json:"jobs"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(again.stdout), &waited); err != nil || waited.Data.Kept != 5 || len(waited.Data.Jobs) != 1 || waited.Data.Jobs[0].State != "succeeded" ||
		waited.Data.Pictures[4].Path != "art/characters/more/dot.gif" {
		t.Fatalf("--json --wait %v %+v\n%s", err, waited.Data, again.stdout)
	}
	// --pictures is for a repository's address.
	if r := runCLI(t, home, "resources", "add", filepath.Join(base, "nothing.png"), "--pictures", "art"); r.code == 0 {
		t.Fatalf("pictures without a repository: %s", r.stdout)
	}
}
