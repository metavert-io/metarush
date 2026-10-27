package recipes

import (
	"context"
	"fmt"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/ops"
)

// Comparing runs (recipe.compare): what changed between two makings of a
// recipe — inputs, notes, recipe versions, cost — and the cut-sheet
// difference of the versions they made (cut.compare), with both review copies.

// RecipeCompareRow is one input in two runs.
type RecipeCompareRow struct {
	Name    string `json:"name"`
	Left    string `json:"left"`
	Right   string `json:"right"`
	Changed bool   `json:"changed"`
}

// RecipeCompareOut is two runs side by side.
type RecipeCompareOut struct {
	Left     RecipeRunBrief     `json:"left"`
	Right    RecipeRunBrief     `json:"right"`
	Inputs   []RecipeCompareRow `json:"inputs"`
	Versions string             `json:"versions" jsonschema:"description=Which recipe versions made them\\, plainly."`
	Cost     string             `json:"cost"`
	Notes    []string           `json:"notes,omitempty" jsonschema:"description=What the person asked for each run."`
	Cut      *ops.CutCompareOut `json:"cut,omitempty" jsonschema:"description=The cut-sheet difference of the versions they made."`
	Renders  [2]string          `json:"renders" jsonschema:"description=Their review copies' pages (empty when there is none)."`
	// Copies are their review copies to play side by side (D212); URL opens
	// the two runs side by side on the Recipes tab.
	Copies   [2]*RecipeCopyView `json:"copies"`
	URL      string             `json:"url"`
	Summary  []string           `json:"summary"`
	Problems []string           `json:"problems,omitempty"`
}

func compareRuns(ctx context.Context, a *app.App, left, right string) (*RecipeCompareOut, error) {
	l, err := loadRun(ctx, a, left)
	if err != nil {
		return nil, err
	}
	r, err := loadRun(ctx, a, right)
	if err != nil {
		return nil, err
	}
	if l.RecipeID != r.RecipeID {
		return nil, apperr.New(apperr.InvalidInput, "Those runs are of two recipes; compare runs of one recipe.")
	}
	out := &RecipeCompareOut{Left: runBrief(ctx, a, l), Right: runBrief(ctx, a, r), Inputs: []RecipeCompareRow{}, Summary: []string{}}
	byName := map[string]string{}
	for _, in := range l.Inputs {
		byName[in.Name] = in.Words
	}
	seen := map[string]bool{}
	for _, in := range r.Inputs {
		seen[in.Name] = true
		lw, ok := byName[in.Name]
		if !ok {
			lw = "(not an input then)"
		}
		row := RecipeCompareRow{Name: in.Name, Left: lw, Right: in.Words}
		for _, li := range l.Inputs {
			if li.Name == in.Name {
				li := li
				row.Changed = !sameValue(&li, &in)
			}
		}
		if !ok {
			row.Changed = true
		}
		out.Inputs = append(out.Inputs, row)
		if row.Changed {
			out.Summary = append(out.Summary, fmt.Sprintf("%s: %s → %s", in.Name, lw, in.Words))
		}
	}
	for _, in := range l.Inputs {
		if !seen[in.Name] {
			out.Inputs = append(out.Inputs, RecipeCompareRow{Name: in.Name, Left: in.Words, Right: "(not an input any more)", Changed: true})
			out.Summary = append(out.Summary, fmt.Sprintf("%s is no longer an input", in.Name))
		}
	}
	if l.VersionNumber == r.VersionNumber {
		out.Versions = fmt.Sprintf("Both were made with version %d of the recipe.", l.VersionNumber)
	} else {
		out.Versions = fmt.Sprintf("Run %d was made with version %d of the recipe, run %d with version %d.", l.Number, l.VersionNumber, r.Number, r.VersionNumber)
		out.Summary = append(out.Summary, out.Versions)
	}
	out.Cost = fmt.Sprintf("Run %d spent %s; run %d spent %s.", l.Number, spentWords(l.Spent), r.Number, spentWords(r.Spent))
	for _, x := range []struct {
		n    int
		note string
	}{{l.Number, l.Note}, {r.Number, r.Note}} {
		if x.note != "" {
			out.Notes = append(out.Notes, fmt.Sprintf("Run %d: “%s”", x.n, x.note))
		}
	}
	if l.Revision != "" && r.Revision != "" && !l.ProjectGone && !r.ProjectGone {
		c, err := ops.CallTyped[ops.CutCompareOut](ctx, a, "cut.compare", ops.CutCompareIn{Left: l.Revision, Right: r.Revision}, ops.Caller{})
		if err == nil {
			out.Cut = c
			out.Summary = append(out.Summary, c.Changes...)
		} else {
			out.Problems = append(out.Problems, "The cuts couldn't be compared: "+apperr.From(err).Message)
		}
	} else {
		out.Problems = append(out.Problems, "A run made no version of its cut yet (or its project was cleared out), so there are no cuts to compare.")
	}
	if l.Render != "" && !l.ProjectGone {
		out.Renders[0] = ops.DashboardURL(a, links.Render(l.ProjectID, l.Render))
	}
	if r.Render != "" && !r.ProjectGone {
		out.Renders[1] = ops.DashboardURL(a, links.Render(r.ProjectID, r.Render))
	}
	out.Copies, out.URL = reviewCopies(ctx, a, l, r), ops.DashboardURL(a, links.RecipeCompare(l.ID, r.ID))
	return out, nil
}
