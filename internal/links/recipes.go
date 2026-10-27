package links

// Recipe opens a recipe on the Library's Recipes tab (D208) — or the tab.
func Recipe(name string) string {
	if name == "" {
		return "/library/recipes"
	}
	return "/library/recipes?" + q("recipe", name)
}

// RecipeRun opens a run of a recipe: its steps, what it waits for, its cut.
func RecipeRun(runID string) string { return "/library/recipes?" + q("run", runID) }

// RecipeCompare opens two runs of a recipe side by side (D212): their review
// copies playing next to each other, then what changed between them.
func RecipeCompare(left, right string) string {
	return "/library/recipes?" + q("compare", left, "with", right)
}

// RecipePlanned opens a recipe with the run it planned by itself (D218): its
// page opens Run with… on it (nothing starts until the person runs it).
func RecipePlanned(name string) string {
	return "/library/recipes?" + q("recipe", name, "planned", "1")
}
