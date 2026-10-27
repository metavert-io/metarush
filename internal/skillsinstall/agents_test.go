package skillsinstall

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleSkill = `---
name: video-sample
description: Do the sample thing with the person's video — a router.
when_to_use: '"what''s the sample", "make a sample"'
allowed-tools: Bash(metarush project list *)
---

# Sample

Talk to the person as [reference/voice.md](reference/voice.md) describes.
`

// A skill for other apps keeps the body, folds when_to_use into the
// description and leaves Claude Code's own fields out.
func TestAgentsSkillFoldsWhenToUse(t *testing.T) {
	got, err := AgentsSkill([]byte(sampleSkill))
	if err != nil {
		t.Fatal(err)
	}
	keys, order, body, err := Frontmatter(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "name,description" {
		t.Errorf("keys %v", order)
	}
	want := `Do the sample thing with the person's video — a router. Use it for: "what's the sample", "make a sample".`
	if keys["name"] != "video-sample" || keys["description"] != want {
		t.Errorf("got %q / %q", keys["name"], keys["description"])
	}
	_, _, srcBody, _ := Frontmatter([]byte(sampleSkill))
	if string(body) != string(srcBody) {
		t.Errorf("body changed:\n%s", body)
	}
	if probs := AgentsSkillProblems("video-sample", got); len(probs) > 0 {
		t.Errorf("problems: %v", probs)
	}
	if probs := AgentsSkillProblems("other-folder", got); len(probs) != 1 {
		t.Errorf("a name unlike its folder: %v", probs)
	}
	if probs := AgentsSkillProblems("video-sample", []byte(sampleSkill)); len(probs) != 2 {
		t.Errorf("Claude Code's own keys should be named: %v", probs)
	}
}

// Every shipped skill reads as YAML and turns into the Agent Skills format.
func TestShippedSkillsForOtherApps(t *testing.T) {
	files, err := AgentsFiles()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rel, b := range files {
		if filepath.Base(rel) != "SKILL.md" {
			continue
		}
		n++
		if probs := AgentsSkillProblems(filepath.Dir(rel), b); len(probs) > 0 {
			t.Errorf("%s: %v", rel, probs)
		}
	}
	if n == 0 {
		t.Fatal("no skills")
	}
}

// --agents installs into a .agents/skills folder and AGENTS.md, and never
// touches Claude Code's settings or CLAUDE.md.
func TestInstallForOtherApps(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Our project\n\nRun the tests.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Install(Options{Dir: filepath.Join(root, ".agents", "skills"), ProjectRoot: root, Agents: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.AgentsMD != filepath.Join(root, "AGENTS.md") || res.ClaudeMD != "" || res.Settings != "" {
		t.Errorf("result: %+v", res)
	}
	b, err := os.ReadFile(filepath.Join(root, ".agents", "skills", "video-project", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "allowed-tools") || strings.Contains(string(b), "when_to_use") {
		t.Errorf("installed a Claude Code skill for other apps:\n%s", b[:200])
	}
	md, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if !strings.HasPrefix(string(md), "# Our project\n\nRun the tests.\n\n"+beginMark) || !strings.Contains(string(md), "video-project") {
		t.Errorf("AGENTS.md:\n%s", md)
	}
	for _, p := range []string{"CLAUDE.md", ".claude"} {
		if _, err := os.Stat(filepath.Join(root, p)); err == nil {
			t.Errorf("%s written for other apps", p)
		}
	}
	// Again: nothing changes, the block isn't added twice.
	res, err = Install(Options{Dir: filepath.Join(root, ".agents", "skills"), ProjectRoot: root, Agents: true})
	if err != nil || len(res.Installed)+len(res.Updated) != 0 {
		t.Fatalf("second install: %+v %v", res, err)
	}
	md2, _ := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if string(md2) != string(md) {
		t.Error("AGENTS.md changed on a second install")
	}
}
