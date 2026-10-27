package logbook

// Plain phrases for recipes (D201–D207): saving a video that worked, its
// versions, planning and starting runs, and what runs do.
func init() {
	for k, v := range map[string]string{
		"recipe.list": "list the recipes", "recipe.show": "look at a recipe", "recipe.save": "save a cut as a recipe", "recipe.propose": "look at what a recipe from a cut would vary",
		"recipe.update": "make a draft of a recipe", "recipe.check": "check a recipe", "recipe.publish": "make a recipe version current",
		"recipe.archive": "archive a recipe", "recipe.plan": "plan a run of a recipe", "recipe.run": "start a run of a recipe",
		"recipe.again": "start a recipe's run again with changes", "recipe.runs": "list runs of recipes", "recipe.compare": "compare two runs of a recipe",
		"recipe.continue": "let a recipe run go on", "recipe.stop": "stop a recipe run",
		"recipe.diff": "compare two versions of a recipe",
		// The recipe sheet (D213) and first picks (D215).
		"recipe.checkout": "write a recipe as its sheet", "recipe.write": "save a recipe sheet as a draft",
		"recipe.picks": "keep or change the first picks of a recipe run",
		// Suggested changes from runs (D217) and planning by itself (D218).
		"recipe.suggestions": "look at the changes suggested for a recipe", "recipe.suggestions.save": "save a recipe's suggested changes as a draft",
		"recipe.triggers": "look at when a recipe plans a run by itself", "recipe.triggers.set": "change when a recipe plans a run by itself",
		// Recipe files between workspaces (D219).
		"recipe.export": "export a recipe to a file", "recipe.import": "bring in a recipe file",
		// Starter recipes (D293) and a sample first (D294).
		"recipe.starters": "look at the starter recipes", "recipe.starters.add": "add the starter recipes as drafts",
		"recipe.sample": "decide on a recipe run's sample",
	} {
		opBase[k] = v
	}
	pastOf["plan"] = "planned"
	pastOf["let"] = "let"
}
