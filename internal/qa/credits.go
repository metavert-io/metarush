package qa

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"

	"github.com/metavert-io/metarush/internal/models"
)

// outsideResources notes, for the person's information only, pictures and
// fonts from the web (a page or a repository) whose licence isn't recorded:
// MetaRush can't tell whether they may be used, so the person checks. Never a
// warning or an error, never blocking.
func (r *run) outsideResources() {
	for _, res := range r.in.Resources {
		if (res.Origin != models.FromURL && res.Origin != models.FromRepo) || strings.TrimSpace(res.License) != "" {
			continue
		}
		what := "picture"
		switch {
		case res.Made:
			what = "picture made from a repository"
		case res.Kind == models.ResourceFont:
			what = "font"
		case res.Kind == models.ResourcePDF:
			what = "PDF page"
		case res.Kind == models.ResourceWebsite:
			what = "snapshot of a web page"
		}
		where := res.Site
		if where == "" {
			where = "the web"
		}
		title := strings.TrimSpace(res.Title)
		if title == "" {
			title = res.Name
		}
		r.add(&models.QAFinding{Severity: models.QAInfo, Kind: "resource_license", Basis: models.QAHeuristic,
			Message: fmt.Sprintf("The %s “%s” comes from %s and its licence isn't recorded. Check that you may use it, and credit it if needed.", what, title, where)})
	}
}

// outsideFootage notes, for the person's information, which outside footage
// (clips imported from YouTube, TikTok or X) the video uses and where: one
// info finding per source, never a warning or an error, and never blocking.
// Delivery sidecars carry the same credits.
func (r *run) outsideFootage() {
	if len(r.in.Credits) == 0 {
		return
	}
	type use struct {
		from, to float64
		clips    []string
	}
	uses := map[string]*use{}
	var order []string
	note := func(assetID string, c editspec.PlanClip) {
		credit, ok := r.in.Credits[assetID]
		if !ok || credit == "" {
			return
		}
		from := float64(c.From) / r.fps
		to := float64(c.From+c.DurationInFrames) / r.fps
		u := uses[assetID]
		if u == nil {
			u = &use{from: from, to: to}
			uses[assetID] = u
			order = append(order, assetID)
		}
		u.from, u.to = min(u.from, from), max(u.to, to)
		if !slices.Contains(u.clips, c.ID) {
			u.clips = append(u.clips, c.ID)
		}
	}
	for _, c := range r.plan.Clips {
		note(c.AssetID, c)
		// A cut from another project credits the outside footage it plays
		// (at the nested clip's time on this timeline).
		if n, ok := r.plan.Nested[c.AssetID]; ok && n.Plan != nil {
			for _, id := range editspec.PlanSourceIDs(n.Plan) {
				note(id, c)
			}
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return uses[order[i]].from < uses[order[j]].from })
	for _, id := range order {
		u := uses[id]
		r.add(&models.QAFinding{Severity: models.QAInfo, Kind: "outside_footage", Basis: models.QAHeuristic, From: u.from, To: u.to, ClipIDs: u.clips,
			Message: fmt.Sprintf("This video uses footage from %s (from %s). The notes saved with each export credit it.", r.in.Credits[id], clock(u.from))})
	}
}
