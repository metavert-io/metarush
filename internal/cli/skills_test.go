package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/skillsinstall"
)

// The live cases' metarush sits next to Claude's working folder, never inside
// it, and is reached through PATH by name, as a person's own install is: found
// at ./bin/metarush, Claude ran it by its path, which the allowlist (commands
// by name) doesn't cover.
func TestEvalShimIsOnPathOutsideTheWorkingFolder(t *testing.T) {
	work := filepath.Join(t.TempDir(), "video-skills-eval-1")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	// A stand-in for the metarush binary: it writes down how it was called.
	called := filepath.Join(t.TempDir(), "called.txt")
	exe := filepath.Join(t.TempDir(), "metarush-binary")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho \"$@\" > \""+called+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin, err := evalShim(work, exe, "/eval/home")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(bin)
	if rel, err := filepath.Rel(work, bin); err != nil || !strings.HasPrefix(rel, "..") || filepath.Dir(bin) != filepath.Dir(work) {
		t.Fatalf("the shim folder %s is not next to the working folder %s", bin, work)
	}
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Fatalf("the shim left files in the working folder: %v", entries)
	}
	cmd := exec.Command("sh", "-c", "metarush project list --json")
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("metarush by name from the working folder: %v %s", err, out)
	}
	got, _ := os.ReadFile(called)
	if strings.TrimSpace(string(got)) != "--home /eval/home project list --json" {
		t.Fatalf("the shim ran the binary with %q", got)
	}
}

// --case natural-cut runs that case alone, not natural-cut-words as well; a
// part of a name still picks every case that has it.
func TestSelectCasesPrefersTheExactName(t *testing.T) {
	cases := naturalCases("Garden story", t.TempDir())
	names := func(cs []evalCase) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Name)
		}
		return out
	}
	if got := names(selectCases(cases, "natural-cut")); len(got) != 1 || got[0] != "natural-cut" {
		t.Fatalf("--case natural-cut ran %v", got)
	}
	if got := names(selectCases(cases, "cut")); len(got) != 2 || got[0] != "natural-cut" || got[1] != "natural-cut-words" {
		t.Fatalf("--case cut ran %v", got)
	}
	if got := selectCases(cases, ""); len(got) != len(cases) {
		t.Fatalf("no --case ran %d of %d cases", len(got), len(cases))
	}
}

func TestJudgeAcceptsAlternativeSkills(t *testing.T) {
	tc := evalCase{Name: "x", WantSkill: "video-project", AltSkills: []string{"video-edit-revise"}, WantCmd: []string{"metarush project checkout"}, ForbidCmds: []string{"metarush project push"}}
	ok := evalResult{Skills: []string{"video-edit-revise"}, Commands: []string{"metarush project checkout prj_1 cuts/builder --json"}}
	if pass, why := judge(tc, ok); !pass {
		t.Fatalf("an alternative skill should pass: %s", why)
	}
	wrong := evalResult{Skills: []string{"video-workspace-admin"}, Commands: ok.Commands}
	if pass, _ := judge(tc, wrong); pass {
		t.Fatal("a skill outside the list must fail")
	}
	forbidden := evalResult{Skills: ok.Skills, Commands: []string{"metarush project push cuts/builder --confirm ct_1"}}
	if pass, _ := judge(tc, forbidden); pass {
		t.Fatal("a forbidden command must fail")
	}
}

func TestMutatingAllowedFlagsWriteCommands(t *testing.T) {
	got := mutatingAllowed("Bash(metarush qa show *) Bash(metarush qa run *) Bash(metarush deliver *) Bash(metarush --help) Bash(metarush * --help) Bash(metarush doctor) Read")
	want := []string{"metarush qa run *", "metarush deliver *", "metarush * --help"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	infos, err := skillInfos()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range staticChecks(infos) {
		if !s.OK {
			t.Errorf("skill %s: %s", s.Name, s.Detail)
		}
	}
	if len(infos) != 15 {
		t.Fatalf("expected 15 bundled skills, got %d", len(infos))
	}
}

func TestTheirWordsKeepOtherVoiceIssues(t *testing.T) {
	issues := []string{"jargon B-roll", "jargon b-roll", "jargon LUFS", "internal ID prj_x"}
	// The person's footage is called broll-c.mp4: "b-roll" is their word, LUFS and IDs are not.
	got := theirWords(issues, "Make a 20-second vertical cut of Test project. interview-a.mov broll-c.mp4")
	if len(got) != 2 || got[0] != "jargon LUFS" || got[1] != "internal ID prj_x" {
		t.Fatalf("got %v", got)
	}
	if got := theirWords(issues, "nothing relevant"); len(got) != 4 {
		t.Fatalf("got %v", got)
	}
	// A folder the person named is theirs to hear back, with the file Claude put in it; other paths are not.
	paths := []string{"file path /var/folders/x/eval/Desktop/x/garden-story-v3.mp4", "file path /Users/you/.metarush/scratch/plan.json", "talks about commands: approve the command"}
	got = theirWords(paths, "Export the approved cut to /var/folders/x/eval/Desktop/x")
	if len(got) != 2 || !strings.Contains(got[0], ".metarush") || !strings.HasPrefix(got[1], "talks about") {
		t.Fatalf("got %v", got)
	}
}

// Skills are routers (D222): each says when to use it and which commands it
// leads to within 700 characters (description and when_to_use), all of them
// within 8,000, so Claude Code keeps every one in its listing; the detail is
// in their bodies, which also lead anything else to `metarush ops find`.
func TestSkillListingFitsTheBudget(t *testing.T) {
	infos, err := skillInfos()
	if err != nil {
		t.Fatal(err)
	}
	files, err := skillsinstall.Files()
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, s := range infos {
		n := skillsinstall.ListingSize(s.Description, s.WhenToUse)
		total += n
		if n > skillsinstall.MaxListingEntry {
			t.Errorf("%s takes %d characters in the listing, more than %d", s.Name, n, skillsinstall.MaxListingEntry)
		}
		if !strings.Contains(s.Description, "Routes to `metarush ") {
			t.Errorf("%s: its description doesn't say which commands it leads to (Routes to `metarush …`)", s.Name)
		}
		if strings.TrimSpace(s.WhenToUse) == "" {
			t.Errorf("%s has no when_to_use", s.Name)
		}
		if !strings.Contains(string(files[s.Name+"/SKILL.md"]), "`metarush ops find") {
			t.Errorf("%s doesn't lead a task it doesn't name to metarush ops find", s.Name)
		}
		if !strings.Contains(s.AllowedTools, "Bash(metarush ops find *)") {
			t.Errorf("%s doesn't let Claude run metarush ops find without asking", s.Name)
		}
	}
	if total > skillsinstall.MaxListing {
		t.Errorf("the skills take %d characters in the listing, more than %d", total, skillsinstall.MaxListing)
	}
	t.Logf("%d skills, %d characters in the listing (budget %d, at most %d each)", len(infos), total, skillsinstall.MaxListing, skillsinstall.MaxListingEntry)
	if r := listingBudget(infos); !r.OK {
		t.Error(r.Detail)
	}
}
