package cli

import "fmt"

// conversationEvalCases are live cases for conversations from online calls
// (D99–D103): Claude reaches for the conversation commands — never cuts the
// recordings apart as separate clips — and saves nothing without the person.
func conversationEvalCases(projectName string) []evalCase {
	noSave := []string{"--confirm", "--apply", "metarush cut apply", "metarush edit apply", "metarush revision approve", "--allow-unlock"}
	return []evalCase{
		{
			Name:      "conversation-podcast",
			Prompt:    fmt.Sprintf("In %q, turn the two Zoom recordings into a podcast episode. Show me what you'd make before saving anything.", projectName),
			WantSkill: "video-story-editor", AltSkills: []string{"video-edit-revise"},
			WantCmd:    []string{"metarush conversation"},
			ExtraAllow: []string{"Bash(metarush conversation cut *)", "Bash(metarush conversation create *)"},
			ForbidCmds: append([]string{"metarush cut add", "metarush cut move"}, noSave...),
		},
		{
			Name:      "conversation-shorts",
			Prompt:    fmt.Sprintf("Make three shorts from the conversation in %q. Tell me which moments you'd pick first.", projectName),
			WantSkill: "video-story-editor", AltSkills: []string{"video-edit-revise", "video-footage-review"},
			WantCmd:    []string{"metarush conversation moments", "metarush conversation list", "metarush conversation show"},
			ForbidCmds: noSave,
		},
	}
}
