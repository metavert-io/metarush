package cli

import "fmt"

// cutSheetEvalCases are the live skill evals for cut sheets and project folders
// (`metarush skills test --live`). The prompts name words from the synthetic
// interview fixture; on other footage the judge still checks the workflow
// (skill and commands), not the edit itself.
func cutSheetEvalCases(projectName string) []evalCase {
	return []evalCase{
		{
			Name:       "cut-sheet-trim",
			Prompt:     fmt.Sprintf("In %q, make the first clip end right after the words \"in my tiny garage\". Do it through the cut sheet and show me what would change; don't save anything yet.", projectName),
			WantSkill:  "video-edit-revise",
			WantCmd:    []string{"metarush cut check"},
			ForbidCmds: []string{"metarush revision approve", "--allow-unlock", "metarush cut apply", "metarush edit apply"},
		},
		{
			// Folders are in video-project and in the cut sheet reference that
			// video-edit-revise carries; either is a fine route.
			Name:       "project-folder",
			Prompt:     fmt.Sprintf("I want to keep the %q cut in git. Put it in a folder called cuts/builder next to me.", projectName),
			WantSkill:  "video-project",
			AltSkills:  []string{"video-edit-revise"},
			WantCmd:    []string{"metarush project checkout"},
			ForbidCmds: []string{"metarush revision approve", "metarush project push", "metarush cut apply"},
		},
	}
}

// cutVerbEvalCases are the live evals for small edits: the skills reach for the
// editing verbs (`metarush cut move|remove-words|…`), which show the change
// first, rather than writing a cut sheet. Each prompt asks to see the change
// before anything is saved, so a run never changes the cut.
func cutVerbEvalCases(projectName string) []evalCase {
	noSave := []string{"--confirm", "metarush cut apply", "metarush edit apply", "metarush revision approve", "--allow-unlock"}
	return []evalCase{
		{
			Name:       "verb-cut-words",
			Prompt:     fmt.Sprintf("In %q, cut out the part where I say \"leaned over the fence\". Show me what would change first; don't save anything yet.", projectName),
			WantSkill:  "video-edit-revise",
			AltSkills:  []string{"video-story-editor"},
			WantCmd:    []string{"metarush cut remove-words"},
			ForbidCmds: noSave,
		},
		{
			Name:       "verb-ums",
			Prompt:     fmt.Sprintf("Take the ums and false starts out of %q. Just show me what you would remove for now.", projectName),
			WantSkill:  "video-edit-revise",
			AltSkills:  []string{"video-story-editor"},
			WantCmd:    []string{"metarush cut remove-words"},
			ForbidCmds: noSave,
		},
		{
			Name:       "verb-move-clip",
			Prompt:     fmt.Sprintf("In %q, move the b-roll clip to the start. Show me what would change first; don't save anything yet.", projectName),
			WantSkill:  "video-edit-revise",
			AltSkills:  []string{"video-story-editor"},
			WantCmd:    []string{"metarush cut move"},
			ForbidCmds: noSave,
		},
	}
}
