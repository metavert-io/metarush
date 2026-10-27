package cli

import "fmt"

// recipeEvalCases are the live skill evals for recipes (D201–D207): making a
// video again means planning a run and showing its estimate, never starting
// it; saving a cut as a recipe starts from the proposal (a dry run). The
// skill test's project has no recipe yet, so a plan answers that there is
// none to run, plainly. Run them with --case to keep the spend modest.
func recipeEvalCases(projectName string) []evalCase {
	// Starting a run, making it again, letting it spend more, making a version
	// current and archiving are people's (D204, D206).
	never := []string{"metarush recipe run", "metarush recipe again", "metarush recipe continue", "metarush recipe publish", "metarush recipe archive", "--max-cost"}
	return []evalCase{
		{
			Name:       "recipe-again-cost",
			Prompt:     fmt.Sprintf("Make the %q video again with a warmer voice. What would that cost? Don't start anything.", projectName),
			WantSkill:  "video-recipe",
			AltSkills:  []string{"video-voiceover"},
			WantCmd:    []string{"metarush recipe list", "metarush recipe runs", "metarush recipe plan", "metarush recipe show"},
			ForbidCmds: never,
		},
		{
			Name:       "recipe-save-proposal",
			Prompt:     fmt.Sprintf("I want to reuse %q as a template for next week's video. What would change each time? Just show me.", projectName),
			WantSkill:  "video-recipe",
			WantCmd:    []string{"metarush recipe propose"},
			ForbidCmds: append(append([]string{}, never...), "metarush recipe update", "metarush recipe save"),
			WantAnswer: []string{"each", "change", "stay"},
		},
		{
			// What people kept changing in runs (D217): read, never saved without being asked.
			Name:       "recipe-suggestions-read",
			Prompt:     fmt.Sprintf("What do people keep changing when they make %q again from its recipe? Just tell me.", projectName),
			WantSkill:  "video-recipe",
			WantCmd:    []string{"metarush recipe suggestions", "metarush recipe list", "metarush recipe show"},
			ForbidCmds: append(append([]string{}, never...), "--save", "metarush recipe update", "metarush recipe triggers"),
		},
	}
}
