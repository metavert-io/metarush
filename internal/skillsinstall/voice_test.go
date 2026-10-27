package skillsinstall

import (
	"strings"
	"testing"
)

func TestVoiceIssues(t *testing.T) {
	clean := []string{
		"Maya's voice now starts a moment before we see her. The middle is eight seconds shorter; the ending is untouched.",
		"The strongest opening is around 0:27, when she laughs. I've put it first.",
		"The music now dips under her voice (that used to be called ducking).",
		"The render stopped because a clip is missing. Add the kitchen footage again and I'll try once more.",
		"Use the MetaRush dashboard to watch it.",
		// Links are for clicking: an ID inside a dashboard URL is fine.
		"Nothing is approved yet. You can approve one here: http://127.0.0.1:4845/p/prj_01jabcdefghjkmnpqrstvwxyz0",
		"Watch it [in the dashboard](http://127.0.0.1:4845/renders/rnd_01jabcdefghjkmnpqrstvwxyz0).",
		"Shall I turn the music down by about a third? Nothing changes until you say yes.",
		"The quality check found two things worth checking in version 3.",
		// A clip's own name, quoted as the voice guide asks: theirs, not a code (D113).
		"Sam's recording, “riverside_sam_raw-video-cfr_garden_show_0002”, starts a second late; I lined it up.",
	}
	for _, s := range clean {
		if got := VoiceIssues(s); len(got) > 0 {
			t.Errorf("%q flagged: %v", s, got)
		}
	}
	dirty := map[string]string{
		"I applied rev_01jabcdefghjkmnpqrstvwxyz0 to the project.": "internal ID",
		"Run it again with --allow-draft to include it.":           "command flag",
		"I ran metarush edit check on the plan.":                   "command",
		`The result was {"ok": true}.`:                             "JSON",
		"I used a J-cut into the second interview.":                "jargon J-cut",
		"The mix is at -14 LUFS.":                                  "jargon LUFS",
		"Swap the B-roll in the middle.":                           "jargon B-roll",
		"I exported it to /Users/you/Desktop/out.":                 "file path /Users/you/Desktop/out",
		"The render stopped (E_TOOL_MISSING).":                     "internal code E_TOOL_MISSING",
		"If you approve the command when it comes up, I'll do it.": "talks about commands",
		"The command needs your permission to run.":                "talks about commands",
		"Version 3 has no revision notes.":                         "jargon revision",
		"I renamed “ast_01jabcdefghjkmnpqrstvwxyz0” for you.":      "internal ID",
		"Its heavy_crop warning is about “Garden call”.":           "internal code heavy_crop",
	}
	for s, want := range dirty {
		got := strings.Join(VoiceIssues(s), "; ")
		if !strings.Contains(got, want) {
			t.Errorf("%q: got %q, want %q", s, got, want)
		}
	}
}

// Every skill links the voice guide, and the guide ships with each skill.
func TestEverySkillLinksTheVoiceGuide(t *testing.T) {
	files, err := Files()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for path, b := range files {
		if !strings.HasSuffix(path, "/SKILL.md") {
			continue
		}
		n++
		name := strings.TrimSuffix(path, "/SKILL.md")
		if !strings.Contains(string(b), "("+VoiceGuide+")") {
			t.Errorf("%s does not link %s", path, VoiceGuide)
		}
		if _, ok := files[name+"/"+VoiceGuide]; !ok {
			t.Errorf("%s has no %s", name, VoiceGuide)
		}
		if strings.Contains(strings.ToLower(string(b)), "b-roll") && strings.Contains(string(b), "description: ") {
			// descriptions are for Claude; keep them plain anyway
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "description:") && strings.Contains(strings.ToLower(line), "b-roll") {
					t.Errorf("%s: description uses B-roll", name)
				}
			}
		}
	}
	if n < 10 {
		t.Fatalf("only %d skills", n)
	}
}
