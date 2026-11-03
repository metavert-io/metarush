package ops

import (
	"context"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Where a project's shape comes from (D378).
const (
	ShapeFromCut          = "cut"
	ShapeFromDeliverables = "deliverables"
	ShapeFromPlatforms    = "platforms"
	ShapeFromFootage      = "footage"
	ShapeFromDefault      = "default"
)

// ProjectShapeOut is what shape a project's videos are (D378): the one
// answer every surface reads instead of guessing — the dashboard included.
type ProjectShapeOut struct {
	editspec.OutputShape
	// Preset is the shape's preset when the size is one ("" for 1080×1350).
	Preset string `json:"preset,omitempty" jsonschema:"enum=vertical-1080p,enum=landscape-1080p,enum=square-1080"`
	From   string `json:"from" jsonschema:"enum=cut,enum=deliverables,enum=platforms,enum=footage,enum=default,description=Where it comes from: the latest version of its main cut (else of any cut)\\, the brief's deliverables\\, the brief's platforms\\, the shape most of its footage is in\\, or MetaRush's default (vertical) when nothing says."`
	// Known: something about the project says it (not the default).
	Known bool `json:"known"`
	// Branch and RevisionID are the cut version it comes from (from: cut).
	Branch     string `json:"branch,omitempty"`
	RevisionID string `json:"revisionId,omitempty"`
	// Why says where it comes from, in plain words.
	Why string `json:"why"`
}

// ProjectShape is the shape of a project's videos (D378), from the first of:
// the latest version of its main cut, else of any cut; the brief's
// deliverables (the first that names a shape); the brief's platforms (the
// first made for a shape: YouTube widescreen, Shorts, TikTok and Reels
// vertical, D385); the shape most of its footage is in (rotation honoured);
// else vertical, MetaRush's default, not known.
func ProjectShape(ctx context.Context, a *app.App, p *models.Project) ProjectShapeOut {
	if p != nil {
		if r := latestCutVersion(ctx, a, p); r != nil {
			if s := r.Spec.Output.Shape(); s.Shape != "" {
				out := projectShapeOf(s, ShapeFromCut, "Its cut “"+CutName(ctx, a, p, r.Branch)+"” is "+s.Word+".")
				out.Branch, out.RevisionID = r.Branch, r.ID
				return out
			}
		}
		for _, d := range p.Brief.Deliverables {
			if s := (editspec.Output{Preset: d.Preset}).Shape(); s.Shape != "" {
				return projectShapeOf(s, ShapeFromDeliverables, "Its brief says “"+d.Name+"” is "+s.Word+".")
			}
		}
		for _, pl := range p.Brief.Platforms {
			if shape := editspec.PlatformShape(pl); shape != "" {
				s := (editspec.Output{Preset: editspec.PresetFor(shape)}).Shape()
				return projectShapeOf(s, ShapeFromPlatforms, "Its brief says it goes to "+platformWords(pl)+", which is "+s.Word+".")
			}
		}
		if shape, ok := footageShape(ctx, a, p.ID); ok {
			s := (editspec.Output{Preset: editspec.PresetFor(shape)}).Shape()
			return projectShapeOf(s, ShapeFromFootage, "Most of its footage is "+s.Word+".")
		}
	}
	out := projectShapeOf((editspec.Output{Preset: editspec.PresetVertical}).Shape(), ShapeFromDefault, "Nothing says yet, so MetaRush starts with vertical.")
	out.Known = false
	return out
}

func projectShapeOf(s editspec.OutputShape, from, why string) ProjectShapeOut {
	preset := ""
	if d, ok := editspec.Presets[editspec.PresetFor(s.Shape)]; ok && d[0] == s.Width && d[1] == s.Height {
		preset = editspec.PresetFor(s.Shape)
	}
	return ProjectShapeOut{OutputShape: s, Preset: preset, From: from, Known: true, Why: why}
}

// latestCutVersion is the latest version of a project's main cut, else the
// newest latest version of its other cuts (only its output is read).
func latestCutVersion(ctx context.Context, a *app.App, p *models.Project) *models.Revision {
	var heads []string
	for _, b := range p.Branches {
		if b.Head != "" {
			heads = append(heads, b.Head)
		}
	}
	if len(heads) == 0 {
		return nil
	}
	var rs []models.Revision
	if a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": heads}}, &rs,
		options.Find().SetProjection(bson.M{"branch": 1, "number": 1, "createdAt": 1, "spec.output": 1}).SetSort(bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: 1}})) != nil || len(rs) == 0 {
		return nil
	}
	if main := p.Branches["main"].Head; main != "" {
		for i := range rs {
			if rs[i].ID == main {
				return &rs[i]
			}
		}
	}
	return &rs[0]
}

// footageShape is the shape most of a project's footage is in (D378): its
// video clips counted by shape, rotation honoured; a tie goes to the longer
// total, then widescreen, vertical, square.
func footageShape(ctx context.Context, a *app.App, projectID string) (editspec.Shape, bool) {
	var as []models.Asset
	if a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"projectIds": projectID, "kind": "video"}, &as,
		options.Find().SetProjection(bson.M{"media.width": 1, "media.height": 1, "media.rotation": 1, "media.duration": 1, "media.streams.type": 1, "media.streams.width": 1, "media.streams.height": 1}).SetLimit(2000)) != nil {
		return "", false
	}
	type tally struct {
		n    int
		secs float64
	}
	count := map[editspec.Shape]*tally{}
	for i := range as {
		w, h := DisplaySize(as[i].Media)
		s := editspec.ShapeOf(w, h)
		if s == "" {
			continue
		}
		if count[s] == nil {
			count[s] = &tally{}
		}
		count[s].n++
		count[s].secs += as[i].Media.Duration
	}
	order := []editspec.Shape{editspec.ShapeLandscape, editspec.ShapeVertical, editspec.ShapeSquare}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := count[order[i]], count[order[j]]
		switch {
		case a == nil || b == nil:
			return b == nil && a != nil
		case a.n != b.n:
			return a.n > b.n
		}
		return a.secs > b.secs
	})
	if count[order[0]] == nil {
		return "", false
	}
	return order[0], true
}

// DisplaySize is footage's picture as it plays: the probe stores it with the
// rotation applied; a record whose size is still the coded one (its video
// stream's) and says it is turned a quarter is turned here.
func DisplaySize(m models.MediaInfo) (w, h int) {
	w, h = m.Width, m.Height
	if r := ((m.Rotation % 360) + 360) % 360; r == 90 || r == 270 {
		for _, st := range m.Streams {
			if st.Type == "video" && st.Width == w && st.Height == h && w != h {
				return h, w
			}
		}
	}
	return w, h
}

// platformWords names a platform for people.
func platformWords(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "youtube":
		return "YouTube"
	case "shorts":
		return "YouTube Shorts"
	case "tiktok":
		return "TikTok"
	case "reels":
		return "Instagram Reels"
	}
	return strings.TrimSpace(p)
}

// CutShape is the shape a cut's videos are (D385): the latest version of the
// cut named (its branch), or the version named (an ID or rN); with no cut
// named, the project's (ProjectShape). A name that is neither is refused.
func CutShape(ctx context.Context, a *app.App, p *models.Project, cut string) (ProjectShapeOut, error) {
	cut = strings.TrimSpace(cut)
	if cut == "" || p == nil {
		return ProjectShape(ctx, a, p), nil
	}
	var r *models.Revision
	if b, ok := p.Branches[cut]; ok && b.Head != "" {
		var head models.Revision
		if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), b.Head, &head); err != nil {
			return ProjectShapeOut{}, err
		}
		r = &head
	} else if strings.HasPrefix(cut, "rev_") || revNumRe.MatchString(cut) {
		rv, _, err := resolveRevision(ctx, a, p, cut, "")
		if err != nil {
			return ProjectShapeOut{}, err
		}
		r = rv
	}
	if r == nil {
		return ProjectShapeOut{}, apperr.New(apperr.NotFound, "“%s” has no cut called “%s”.", p.Name, cut).WithCommand("metarush project show " + p.ID)
	}
	s := r.Spec.Output.Shape()
	if s.Shape == "" {
		s = (editspec.Output{Preset: editspec.PresetVertical}).Shape() // what Normalize draws it as
	}
	out := projectShapeOf(s, ShapeFromCut, "The cut “"+CutName(ctx, a, p, r.Branch)+"” is "+s.Word+".")
	out.Branch, out.RevisionID = r.Branch, r.ID
	return out, nil
}

// NewCutOutput is the output a new cut of a project starts with when it
// names no shape (D386): the project's shape (ProjectShape, MetaRush's
// vertical when nothing says), at its preset's size or its own (4:5 stays
// 4:5), for the first platform in the brief made for that shape (else none:
// generic).
func NewCutOutput(ctx context.Context, a *app.App, p *models.Project) (editspec.Output, ProjectShapeOut) {
	ps := ProjectShape(ctx, a, p)
	out := editspec.Output{Preset: ps.Preset}
	if out.Preset == "" {
		out.Width, out.Height = ps.Width, ps.Height
	}
	if p != nil {
		for _, pl := range p.Brief.Platforms {
			if pl = strings.ToLower(strings.TrimSpace(pl)); editspec.PlatformShape(pl) == ps.Shape {
				out.Platform = pl
				break
			}
		}
	}
	return out, ps
}
