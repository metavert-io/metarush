package assistant

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
)

// The exact technical text the panel showed in a real conversation (D140) —
// Claude Code's spilled tool output, file names, the notes' file names, "Ran a
// command → blocked", picture paths — never reaches a step line now, and the
// steps of one kind group into one line.
func TestStepLinesSpeakPlainly(t *testing.T) {
	const asset = "ast_01m5a226jfhy6wj9mh78v75rgh"
	names := map[string]string{asset: "“video1763049187”"}
	conv := "/home/me/.metarush/assistant/conversations/asc_01m5d1xjxmqtqc2aynrsxy7c0v"
	p := newParser("/home/me/.metarush/assistant/workspace", conv, "/home/me/.metarush/scratch", "/home/me/.metarush/assistant", func(id string) string { return names[id] })
	p.aliases = [][2]string{{"cache", "/home/me/.metarush/cache"}}
	n := 0
	call := func(name string, input map[string]any) string {
		n++
		id := fmt.Sprintf("toolu_%02d", n)
		b, _ := json.Marshal(map[string]any{"type": "assistant", "parent_tool_use_id": nil,
			"message": map[string]any{"content": []map[string]any{{"type": "tool_use", "id": id, "name": name, "input": input}}}})
		return string(b)
	}
	result := func(id string, denied bool, text string) string {
		b, _ := json.Marshal(map[string]any{"type": "user", "parent_tool_use_id": nil,
			"message": map[string]any{"content": []map[string]any{{"type": "tool_result", "tool_use_id": id, "is_error": denied, "content": text}}}})
		return string(b)
	}
	lines := []string{
		call("Read", map[string]any{"file_path": "/home/me/.claude/projects/-home-me--metarush-assistant-workspace/s1/tool-results/toolu_019McxbXKnk7jm1NS3WKa7VE.txt"}),
		call("Read", map[string]any{"file_path": "/home/me/.claude/projects/-home-me--metarush-assistant-workspace/s1/tool-results/be2dbzozw.txt"}),
		call("Write", map[string]any{"file_path": conv + "/proposals/t3/motion/name-bar/index.tsx", "content": "export const meta = {};"}),
		call("Write", map[string]any{"file_path": conv + "/proposals/t3/motion/name-bar/preview.json", "content": "{}"}),
		call("Read", map[string]any{"file_path": "/home/me/.metarush/assistant/workspace/.claude/skills/video-motion-design/SKILL.md"}),
		call("Read", map[string]any{"file_path": "/home/me/.metarush/assistant/workspace/.claude/skills/video-motion-design/reference/motion-code.md"}),
		call("Glob", map[string]any{"pattern": "**/*.md"}),
		call("Bash", map[string]any{"command": "ls -la motion"}),
		result("toolu_08", true, "Permission to use Bash has been denied because Claude Code is running in don't ask mode."),
		call("Bash", map[string]any{"command": "metarush footage frames " + asset + " --times 13:11 --json"}),
		result("toolu_09", true, "Error: unknown flag: --times"),
		call("Write", map[string]any{"file_path": conv + "/proposals/t3/shorts.question.json", "content": "{}"}),
	}
	for i := 0; i < 19; i++ {
		lines = append(lines, call("Read", map[string]any{"file_path": fmt.Sprintf("/home/me/.metarush/scratch/frames/%s/13m%02d.800.jpg", asset, 11+i)}))
	}
	var tools []*ToolCall
	results := map[string]*ToolCall{}
	for _, l := range lines {
		for _, e := range p.line(l).events {
			switch e.Kind {
			case "tool":
				tools = append(tools, e.Tool)
			case "tool_result":
				results[e.Tool.ID] = e.Tool
			}
		}
	}
	// One line per group, as the dashboard and the CLI print them.
	var shown []string
	for i := 0; i < len(tools); {
		j := i + 1
		for tools[i].Group != "" && j < len(tools) && tools[j].Group == tools[i].Group {
			j++
		}
		line := tools[i].Label
		if j-i > 1 && tools[i].Many != "" {
			line = tools[i].Many
			if strings.Contains(line, "%d") {
				line = fmt.Sprintf(line, j-i)
			}
		}
		if r := results[tools[j-1].ID]; r != nil && r.Outcome != "" {
			line += " → " + r.Outcome
		}
		shown = append(shown, line)
		i = j
	}
	want := []string{
		"Read the rest of 2 long results",
		"Wrote the graphic “name-bar”",
		"Read 3 of MetaRush's notes",
		"Tried something that isn't allowed here",
		"Pulled stills from “video1763049187” → didn't work",
		"Wrote a question for you",
		"Looked at 19 stills from “video1763049187”",
	}
	if strings.Join(shown, "\n") != strings.Join(want, "\n") {
		t.Fatalf("step lines:\n got %q\nwant %q", shown, want)
	}
	for _, tc := range tools {
		for _, s := range []string{tc.Label, strings.ReplaceAll(tc.Many, "%d", "2")} {
			if fs := plainwords.CheckPanel(s); len(fs) > 0 {
				t.Errorf("step %q shows %v", s, fs)
			}
		}
		if r := results[tc.ID]; r != nil && strings.Contains(r.Outcome, "blocked") {
			t.Errorf("a refusal reads %q", r.Outcome)
		}
	}
	if len(tools[len(tools)-1].Images) != 1 {
		t.Fatalf("each still is a thumbnail: %+v", tools[len(tools)-1])
	}
	// One read of MetaRush's notes is named by what it is about.
	if tools[4].Label != "Read MetaRush's notes on graphics" || tools[5].Label != "Read MetaRush's notes on making graphics" {
		t.Fatalf("notes: %q %q", tools[4].Label, tools[5].Label)
	}
}

// Cards and notes say it in plain words: no code counts, no raw check
// problem, no version codes or IDs, times to the second (D140). What Claude
// needs to carry on stays in its note.
func TestCardTextsSpeakPlainly(t *testing.T) {
	failed := &ops.MotionCheckOut{OK: false, Name: "name-bar", Hash: strings.Repeat("a", 64), Version: "name-bar@aec0bd16",
		Problems: []models.MotionProblem{{Severity: "error", Code: "bad_meta", File: "index.tsx",
			Message: "meta.minDuration and meta.maxDuration are seconds between 0.2 and 60, the first not above the second."}}}
	passed := &ops.MotionCheckOut{OK: true, Name: "name-bar", Hash: strings.Repeat("b", 64), Version: "name-bar@9d6f681d", State: models.MotionWaiting,
		Problems: []models.MotionProblem{{Severity: "warning", Code: "outside_safe_area", Message: "It draws outside the safe area (bottom, left)."}}}
	approved := &ops.MotionDecideOut{Row: ops.MotionRow{Name: "chapter-card", Hash: strings.Repeat("c", 64), Version: "chapter-card@aa83837c", State: models.MotionApproved},
		Summary: "Approved chapter-card@aa83837c. Cuts can use this exact version now; any change to its files makes a new version that needs approval again."}
	cases := map[string]string{
		"failed check":   motionCheckWords(failed),
		"passed check":   motionCheckWords(passed),
		"approved":       motionDecideWords(approved),
		"sheet details":  plainTimes("Added clip 1, no-help: video1763049187 13:11.732 → 13:34.708."),
		"old note":       PlainNote("Done from the dashboard: Check the motion graphic “name-bar” Claude wrote. Result: name-bar didn't pass the check (1 problem). meta.minDuration and meta.maxDuration are seconds between 0.2 and 60, the first not above the second (version name-bar@aec0bd16)."),
		"old approval":   PlainNote("Done from the dashboard: Approve the motion graphic “chapter-card”. Result: Approved chapter-card@aa83837c. Cuts can use this exact version now; any change to its files makes a new version that needs approval again."),
		"old save":       PlainNote("Done from the dashboard: Save the cut: Fix the flicker. Result: Saved the cut sheet as version 2 (rev_01m5d3tqz2zw4dcggss5xj1mn9)."),
		"old render":     PlainNote("Done from the dashboard: Render a review copy of version 2. Result: The render of version 2 is waiting to start (rnd_01m5d3vv6nrdg37zj00b5x4b1q)."),
		"render outcome": "The review copy of version 2 is ready (6 min 53 s). " + qaWords(models.QACounts{Errors: 1, Warnings: 2}, models.QAFail) + ".",
	}
	for what, s := range cases {
		if fs := plainwords.CheckPanel(s); len(fs) > 0 {
			t.Errorf("%s: %q shows %v", what, s, fs)
		}
	}
	if cases["failed check"] != "“name-bar” didn't pass the check: it can stay on screen for at most a minute. Claude can fix it and check it again" {
		t.Errorf("failed check: %q", cases["failed check"])
	}
	if cases["sheet details"] != "Added clip 1, no-help: video1763049187 13:11–13:34." {
		t.Errorf("times to the second: %q", cases["sheet details"])
	}
	if cases["approved"] != "Approved “chapter-card”: cuts can use it now" || cases["old approval"] != "Approved “chapter-card”. Cuts can use this exact version now; any change to its files makes a new version that needs approval again" {
		t.Errorf("approval: %q / %q", cases["approved"], cases["old approval"])
	}
	if fc := motionCheckForClaude(failed); !strings.Contains(fc, "name-bar@aec0bd16") || !strings.Contains(fc, "meta.minDuration") || !strings.Contains(fc, "index.tsx") {
		t.Errorf("Claude keeps the exact problem: %q", fc)
	}
	// A stored card from an older version reads plainly when shown.
	old := AssistantAction{Title: "Check the motion graphic “name-bar” Claude wrote", Details: []string{"Draws test frames", "68 lines of code"},
		Result: "name-bar passed the check. 1 note to look at. A person needs to approve this version before it can be used in a cut (version name-bar@9d6f681d)"}
	plainCard(&old)
	for _, s := range append(old.Details, old.Result) {
		if fs := plainwords.CheckPanel(s); len(fs) > 0 {
			t.Errorf("old card %q shows %v", s, fs)
		}
	}
	if len(old.Details) != 1 {
		t.Errorf("the code count is gone: %q", old.Details)
	}
	// Old step lines and notices are cleaned when shown.
	evs := plainEvents([]AssistantEvent{
		{Kind: "notice", Text: "Resumed the Claude Code session"},
		{Kind: "tool", Tool: &ToolCall{Label: "Read toolu_019McxbXKnk7jm1NS3WKa7VE.txt"}},
		{Kind: "tool", Tool: &ToolCall{Label: "Wrote index.tsx"}},
		{Kind: "tool", Tool: &ToolCall{Label: "Read the notes on SKILL"}},
		{Kind: "tool", Tool: &ToolCall{Label: "Ran a command"}},
		{Kind: "tool_result", Tool: &ToolCall{Outcome: "blocked"}},
		{Kind: "tool", Tool: &ToolCall{Label: "Pulled 0 stills from video1763049187"}},
	}, nil)
	for _, e := range evs {
		s := e.Text
		if e.Tool != nil {
			s = e.Tool.Label + " " + e.Tool.Outcome
		}
		if fs := plainwords.CheckPanel(s); len(fs) > 0 || strings.Contains(s, "0 stills") {
			t.Errorf("old step %q shows %v", s, fs)
		}
	}
	if len(evs) != 6 {
		t.Errorf("the resume notice is gone: %+v", evs)
	}
}

// A link starts by itself only when the person typed it in the conversation
// (D141): with or without its scheme or "www.", an address on this computer
// with its port, a video with its query; never one only Claude or a note
// mentions.
func TestTypedLinks(t *testing.T) {
	c := &conv{data: Conversation{Turns: []Turn{
		{N: 1, Prompt: "Use their logo from fernbrook.example, and see http://127.0.0.1:5334/brand.html."},
		{N: 2, Note: true, Prompt: "Done from the dashboard: … https://notes.example/page"},
		{N: 3, Prompt: "Bring in https://www.youtube.com/watch?v=aqz-KE-bpKQ please"},
	}}}
	for link, want := range map[string]bool{
		"https://www.fernbrook.example/":          true,
		"http://fernbrook.example":                true,
		"https://fernbrook.example/donate":        false, // a page Claude found on the site
		"http://127.0.0.1:5334/brand.html":        true,
		"http://127.0.0.1:5335/brand.html":        false,
		"https://youtube.com/watch?v=aqz-KE-bpKQ": true,
		"https://www.youtube.com/watch?v=other":   false,
		"https://notes.example/page":              false, // only in a dashboard note
		"file:///etc/hosts":                       false,
		"https://user:secret@fernbrook.example/":  false,
	} {
		if got := c.personTyped(link); got != want {
			t.Errorf("personTyped(%q) = %v, want %v", link, got, want)
		}
	}
}

// Every card a describer builds and every outcome it reports passes the
// panel's guard, for the cards of a real conversation.
func TestMotionProblemsHavePlainWords(t *testing.T) {
	for _, code := range []string{"bad_meta", "bad_schema", "bad_preview", "import_not_allowed", "missing_export", "syntax", "type", "bundle", "bundle_too_big",
		"resource_unknown", "resource_param", "network", "render", "outside_safe_area", "region_mismatch", "draws_nothing", "nondeterministic", "slow_frames", "x"} {
		s := motionProblemWords(models.MotionProblem{Code: code, Severity: "error", Message: "meta.region must be lower, center or full"})
		if s == "" || len(plainwords.CheckPanel(s)) > 0 || strings.Contains(strings.ToLower(s), "code") {
			t.Errorf("%s: %q", code, s)
		}
	}
}
