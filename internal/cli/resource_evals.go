package cli

import "fmt"

// resourceEvalCases are the live skill evals for resources (D79–D84, D104–
// D108): "use our logo" means finding the logo among the project's resources
// (never drawing one) and showing the change before anything is saved; the
// slide requests read the project's repository through its read-only copy
// and never run anything from it. Run them with --case to keep the spend modest.
func resourceEvalCases(projectName string) []evalCase {
	noSave := []string{"--confirm", "metarush cut apply", "metarush edit apply", "metarush revision approve", "--allow-unlock", "metarush brand set"}
	// Running anything from a repository is never right (D104).
	noRun := append(append([]string{}, noSave...), "go run", "go build", "go test", "npm install", "npm run", "npm ci", "yarn ", "pnpm ", "pip install", "make ", "cargo ", "git clone", "curl ", "wget ")
	readRepo := []string{"Read", "Grep", "Glob"}
	return []evalCase{
		{
			Name:       "slides-architecture",
			Prompt:     fmt.Sprintf("Make an architecture slide from the repo for %q. Show me what you'd add first; don't save anything yet.", projectName),
			WantSkill:  "video-motion-design",
			AltSkills:  []string{"video-story-editor", "video-edit-revise"},
			WantCmd:    []string{"metarush resources path", "metarush resources show", "metarush resources list"},
			ExtraAllow: readRepo,
			ForbidCmds: noRun,
		},
		{
			Name:       "slides-homepage",
			Prompt:     fmt.Sprintf("Show the homepage at the end of %q. Just show me the change for now.", projectName),
			WantSkill:  "video-motion-design",
			AltSkills:  []string{"video-edit-revise", "video-story-editor"},
			WantCmd:    []string{"metarush resources list", "metarush resources show"},
			ForbidCmds: noRun,
		},
		{
			Name:       "slides-code",
			Prompt:     fmt.Sprintf("Put a code snippet of the main loop on screen in %q. Don't save anything yet.", projectName),
			WantSkill:  "video-motion-design",
			AltSkills:  []string{"video-edit-revise", "video-story-editor"},
			WantCmd:    []string{"metarush resources path", "metarush resources show", "metarush resources list"},
			ExtraAllow: readRepo,
			ForbidCmds: noRun,
		},
		{
			Name:       "logo-title-card",
			Prompt:     fmt.Sprintf("Use our logo on the title card of %q. Show me what would change first; don't save anything yet.", projectName),
			WantSkill:  "video-motion-design",
			AltSkills:  []string{"video-edit-revise"},
			WantCmd:    []string{"metarush resources list", "metarush resources show", "metarush brand show"},
			ForbidCmds: noSave,
		},
		{
			Name:       "logo-corner",
			Prompt:     fmt.Sprintf("Put the logo in the top-right corner of %q for the whole video. Just show me the change for now.", projectName),
			WantSkill:  "video-motion-design",
			AltSkills:  []string{"video-edit-revise"},
			WantCmd:    []string{"metarush resources list", "metarush resources show", "metarush brand show"},
			ForbidCmds: noSave,
		},
	}
}
