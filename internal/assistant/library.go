package assistant

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/resources"
)

// libraryContext tells Claude what a project can use beyond its own footage
// (D83): the projects it links to and its resources, counted by where they
// come from ("12 here, plus 40 from Brand"), with the read-only commands
// that list them.
func libraryContext(ctx context.Context, a *app.App, projectID string) []string {
	if projectID == "" {
		return nil
	}
	view, err := projectlinks.Load(ctx, a, projectID)
	if err != nil || view == nil {
		return nil
	}
	var out []string
	if len(view.Links) > 0 {
		var names []string
		for _, l := range view.Links {
			n := view.Names[l.SourceID]
			if n == "" {
				n = l.SourceID
			}
			names = append(names, fmt.Sprintf("%q (%s)", n, linkScope(l)))
		}
		sort.Strings(names)
		out = append(out, "- Linked projects (their footage, resources, approved motion graphics and cuts can be used here, nothing copied; `metarush project links "+projectID+"`): "+strings.Join(names, ", "))
	}
	list, err := resources.Visible(ctx, a, projectID, view, resources.ListOptions{})
	if err != nil || len(list) == 0 {
		return out
	}
	here, ws := 0, 0
	from := map[string]int{}
	for _, e := range list {
		switch e.Origin {
		case resources.FromHere:
			here++
		case resources.FromWorkspace:
			ws++
		case resources.FromLinked:
			from[e.SourceID]++
		}
	}
	s := fmt.Sprintf("- Resources (logos, pictures, PDFs, fonts): %d here", here)
	var srcs []string
	for id := range from {
		srcs = append(srcs, id)
	}
	sort.Slice(srcs, func(i, j int) bool { return view.Names[srcs[i]] < view.Names[srcs[j]] })
	for _, id := range srcs {
		s += fmt.Sprintf(", plus %d from %s", from[id], view.Names[id])
	}
	if ws > 0 {
		s += fmt.Sprintf(", plus %d in the workspace library", ws)
	}
	s += "; see `metarush resources list --project " + projectID + "` and use them by name in template params (logo, image)"
	return append(out, s)
}

// linkScope says what a project link brings in, in a few words.
func linkScope(l models.ProjectLink) string {
	switch {
	case len(l.Items) > 0 && (l.Only == nil || l.Only.Empty()):
		return fmt.Sprintf("%d picked items", len(l.Items))
	case l.Only != nil && !l.Only.Empty():
		var parts []string
		if len(l.Only.Kinds) > 0 {
			parts = append(parts, "only "+strings.Join(l.Only.Kinds, ", "))
		}
		if len(l.Only.Tags) > 0 {
			parts = append(parts, "tagged "+strings.Join(l.Only.Tags, ", "))
		}
		if len(l.Only.People) > 0 {
			parts = append(parts, "with "+strings.Join(l.Only.People, ", "))
		}
		if len(l.Only.Folders) > 0 {
			parts = append(parts, "from linked folders")
		}
		return strings.Join(parts, "; ")
	}
	return "everything"
}
