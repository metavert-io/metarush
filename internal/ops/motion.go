package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/store"
)

// Project motion graphics (D68): Remotion components a project carries in its
// folder (motion/<name>). `motion check` checks one and records it for a
// person to review; only versions a person approved (by hash) render.

// MotionCheckIn checks a motion graphic's folder.
type MotionCheckIn struct {
	Dir       string  `json:"dir,omitempty" jsonschema:"description=The component's folder (the one with index.tsx). Leave it out and give name to use motion/<name> of the project folder you are in."`
	Name      string  `json:"name,omitempty" jsonschema:"pattern=^[a-z][a-z0-9-]*$,maxLength=40"`
	ProjectID string  `json:"projectId,omitempty" jsonschema:"description=Project (ID or name) whose shape\\, platform and brand the test frames use. Default: the project folder's (a version drawn again: the one it was checked in)."`
	Shape     string  `json:"shape,omitempty" jsonschema:"pattern=^(vertical|landscape|widescreen|square|[0-9]{2\\,4}x[0-9]{2\\,4})$,description=Draw it in this shape: vertical\\, landscape or square\\, or an exact size like 1080x1350 (default: the shapes of the cuts that use it\\, else the project's shape\\, else both widescreen and vertical). With version: drawn in this shape too."`
	Duration  float64 `json:"duration,omitempty" jsonschema:"minimum=0,maximum=60,description=Seconds to run the graphic for in the test render (kept within its meta)."`
	NoPreview bool    `json:"noPreview,omitempty" jsonschema:"description=Skip the short preview video (faster while iterating; a person reviewing it wants one)."`
	// Version checks a version MetaRush keeps but hasn't checked in this
	// workspace — one brought in with a file from another workspace (D219) —
	// or draws a version already checked here again (D377): the same code and
	// hash, in the cuts' shapes (and shape), its approval kept.
	Version string `json:"version,omitempty" jsonschema:"description=name@version: a version checked here is drawn again in the shapes of the cuts that use it (and shape)\\, its code and approval unchanged; a version MetaRush keeps but hasn't checked here (brought in with a file from another workspace) is checked\\, with the whole version."`
	// Libraries are the shared libraries to check it with: name → version
	// (default: the newest approved one, else the newest checked).
	Libraries map[string]string `json:"libraries,omitempty" jsonschema:"description=Shared libraries to check it with: name → version (default: the newest approved\\, else the newest checked)."`
}

// MotionFrameOut is one test frame on this machine.
type MotionFrameOut struct {
	Label string  `json:"label"`
	At    float64 `json:"at" jsonschema:"description=Seconds from the graphic's start."`
	Path  string  `json:"path"`
	// Shape is the shape of video it was drawn in (D164): widescreen, vertical,
	// square (with the ratio when two share a word); Width and Height its size.
	Shape  string `json:"shape,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// MotionShapeOut is one shape a check drew a graphic in (D377).
type MotionShapeOut struct {
	Label  string `json:"label" jsonschema:"description=widescreen\\, vertical or square\\, with the ratio when two share a word."`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	For    string `json:"for,omitempty" jsonschema:"description=The cuts or deliverable it is for\\, in words."`
}

// MotionCheckOut says what a check found.
type MotionCheckOut struct {
	OK       bool                   `json:"ok" jsonschema:"description=The component passed: it is recorded and waits for a person's approval (or keeps its approval)."`
	Summary  string                 `json:"summary" jsonschema:"description=What happened\\, in plain words."`
	Name     string                 `json:"name"`
	Hash     string                 `json:"hash"`
	Version  string                 `json:"version" jsonschema:"description=name@version\\, the way a cut sheet names it."`
	State    string                 `json:"state,omitempty" jsonschema:"enum=waiting,enum=approved,enum=rejected,enum=revoked"`
	Stage    string                 `json:"stage,omitempty" jsonschema:"description=Where a failing check stopped: scan\\, types\\, bundle\\, info\\, network or render."`
	Problems []models.MotionProblem `json:"problems"`
	Imports  []models.MotionImport  `json:"imports"`
	Frames   []MotionFrameOut       `json:"frames"`
	Sheet    string                 `json:"sheet,omitempty" jsonschema:"description=The contact sheet of the test frames (an image to look at)."`
	Preview  string                 `json:"preview,omitempty" jsonschema:"description=The short preview video."`
	Meta     map[string]any         `json:"meta,omitempty"`
	Schema   json.RawMessage        `json:"schema,omitempty" jsonschema:"type=object,description=Its parameters as JSON Schema (converted from its zod schema)."`
	Ignored  []string               `json:"ignored,omitempty" jsonschema:"description=Hidden files left out of the component."`
	URL      string                 `json:"url,omitempty" jsonschema:"description=Where a person reviews and approves it in the dashboard."`
	Next     string                 `json:"next,omitempty"`
	// Shapes are the shapes it was drawn in (D377) and ShapesFrom why.
	Shapes     []MotionShapeOut `json:"shapes"`
	ShapesFrom string           `json:"shapesFrom,omitempty" jsonschema:"enum=asked,enum=cuts,enum=workspace,enum=project,enum=nothing,description=asked: the shape given; cuts: the project's cuts that use it; project: the project's shape; workspace: with no project (or one that says nothing)\\, other projects' cuts that use a graphic of that name; nothing: nothing says\\, so widescreen and vertical."`
	// Redrawn: a version checked before, drawn again (D377): the same code,
	// so its approval (or the wait for one) is unchanged.
	Redrawn bool `json:"redrawn,omitempty"`
}

// MotionRow is one motion graphic version in a list.
type MotionRow struct {
	Name        string               `json:"name"`
	Hash        string               `json:"hash"`
	Version     string               `json:"version"`
	State       string               `json:"state" jsonschema:"enum=waiting,enum=approved,enum=rejected,enum=revoked"`
	StateLabel  string               `json:"stateLabel" jsonschema:"description=The state in plain words."`
	Summary     string               `json:"summary,omitempty"`
	Region      string               `json:"region"`
	MinDuration float64              `json:"minDuration"`
	MaxDuration float64              `json:"maxDuration"`
	ProjectID   string               `json:"projectId,omitempty"`
	ProjectName string               `json:"projectName,omitempty"`
	Warnings    int                  `json:"warnings"`
	CheckedAt   time.Time            `json:"checkedAt"`
	CheckedBy   string               `json:"checkedBy,omitempty"`
	ApprovedBy  string               `json:"approvedBy,omitempty"`
	ApprovedAt  *time.Time           `json:"approvedAt,omitempty"`
	Sheet       *models.MotionObject `json:"sheet,omitempty"`
	URL         string               `json:"url"`
	// Kind "library": a shared library graphics import (D171); Libraries the
	// libraries a graphic imports; UsedBy the graphics a library's check drew.
	Kind      string   `json:"kind,omitempty" jsonschema:"enum=,enum=library"`
	Libraries []string `json:"libraries,omitempty"`
	UsedBy    []string `json:"usedBy,omitempty"`
}

// MotionListIn lists motion graphics.
type MotionListIn struct {
	All   bool   `json:"all,omitempty" jsonschema:"description=Every version\\, not only the latest of each name."`
	State string `json:"state,omitempty" jsonschema:"enum=waiting,enum=approved,enum=rejected,enum=revoked"`
}

// MotionListOut lists motion graphics, newest check first.
type MotionListOut struct {
	Components []MotionRow `json:"components"`
	Waiting    int         `json:"waiting" jsonschema:"description=How many versions wait for a person."`
}

// MotionShowIn picks a version.
type MotionShowIn struct {
	Ref string `json:"ref" jsonschema:"description=name@version\\, a version (at least 8 characters of the hash) or a name (its latest check)."`
}

// MotionSourceFile is one source file with its changes against the last approved version.
type MotionSourceFile struct {
	Path    string              `json:"path"`
	Text    string              `json:"text"`
	Change  string              `json:"change" jsonschema:"enum=same,enum=added,enum=changed,enum=removed,enum=new,description=Compared with the last approved version of the same name (new: there is none; removed: only in the approved version)."`
	Changes []cutsheet.DiffLine `json:"changes,omitempty" jsonschema:"description=The lines that differ (with a little context)."`
}

// MotionField is one parameter, from the schema.
type MotionField struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Description string `json:"description,omitempty"`
}

// MotionShowOut is everything a person reviews before approving a version.
type MotionShowOut struct {
	Row       MotionRow              `json:"row"`
	Component models.MotionComponent `json:"component"`
	Source    []MotionSourceFile     `json:"source"`
	Fields    []MotionField          `json:"fields"`
	Schema    json.RawMessage        `json:"schema" jsonschema:"type=object"`
	Preview   json.RawMessage        `json:"preview,omitempty" jsonschema:"type=object,description=The sample parameters (preview.json) the test frames use."`
	Previous  *MotionRow             `json:"previous,omitempty" jsonschema:"description=The last approved version of the same name (what the source is compared with)."`
	Versions  []MotionRow            `json:"versions" jsonschema:"description=Every version of this name\\, newest first."`
	UsedBy    []MotionUse            `json:"usedBy" jsonschema:"description=Latest cuts that use this version."`
	SheetPath string                 `json:"sheetPath,omitempty" jsonschema:"description=The contact sheet on this machine (an image to look at)."`
	// Shapes are the shapes it was drawn in and whether a person approved it
	// in each (D381): what the review page shows beside each shape's frames.
	Shapes []MotionShapeView `json:"shapes"`
}

// MotionShapeView is one shape a version was drawn in, for its review.
type MotionShapeView struct {
	Size     string `json:"size" jsonschema:"description=Its exact size (1920x1080)."`
	Label    string `json:"label"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Ratio    string `json:"ratio"`
	Approved bool   `json:"approved" jsonschema:"description=A person approved it in this shape (cuts of this ratio use it without a note)."`
	// Added: drawn by a later check than the first (D381).
	Added bool `json:"added,omitempty"`
}

// MotionUse is a cut that uses a version.
type MotionUse struct {
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	RevisionID  string `json:"revisionId"`
	Number      int    `json:"number"`
}

// MotionDecideIn approves, turns down or withdraws approval of a version. People only.
type MotionDecideIn struct {
	Ref    string `json:"ref" jsonschema:"description=name@version (at least 8 characters of the hash) or the full hash."`
	Reject bool   `json:"reject,omitempty" jsonschema:"description=motion.approve: turn it down instead."`
	Note   string `json:"note,omitempty" jsonschema:"maxLength=2000"`
	// Shapes are the shapes the person looked at (D381).
	Shapes []string `json:"shapes,omitempty" jsonschema:"maxItems=9,description=motion.approve: the shapes the person looked at and approves it in (sizes like 1920x1080\\, or vertical/landscape/square). Default: every shape it was drawn in. An approved version gets the shapes it isn't approved in yet."`
}

// MotionDecideOut is the version after the decision.
type MotionDecideOut struct {
	Row     MotionRow `json:"row"`
	Summary string    `json:"summary"`
	// Moved: cuts whose graphics moved to a shared library's approved version (D171).
	Moved []LibraryMove `json:"moved,omitempty"`
	// Packs: layout packs that use an older version of the library (D266):
	// offers a person takes (content.pack.set library).
	Packs []PackLibraryOffer `json:"packs,omitempty"`
	// ToldClaude is the conversation a turned-down note went to (D186): the
	// one that made the graphic; absent when none did.
	ToldClaude *NoteSent `json:"toldClaude,omitempty" jsonschema:"description=Turned down with a note: the Claude Code conversation the note went to (the one that made the graphic)\\, if any."`
}

// MotionNewIn scaffolds a component folder.
type MotionNewIn struct {
	Name string `json:"name" jsonschema:"pattern=^[a-z][a-z0-9-]*$,maxLength=40"`
	Dir  string `json:"dir,omitempty" jsonschema:"description=Where to put motion/<name> (default: the current folder)."`
}

// MotionNewOut is the new folder.
type MotionNewOut struct {
	Dir     string   `json:"dir"`
	Files   []string `json:"files"`
	Summary string   `json:"summary"`
	Next    string   `json:"next"`
}

var stateLabels = map[string]string{
	models.MotionWaiting: "Waiting for a person", models.MotionApproved: "Approved",
	models.MotionRejected: "Turned down", models.MotionRevoked: "Approval withdrawn",
}

func motionRow(c *models.MotionComponent, projectNames map[string]string) MotionRow {
	r := MotionRow{Name: c.Name, Hash: c.Hash, Version: c.Name + "@" + motion.Short(c.Hash), State: c.State, StateLabel: stateLabels[c.State], Summary: c.Summary, Kind: c.Kind, Libraries: c.Libraries, UsedBy: c.UsedBy,
		Region: c.Region, MinDuration: c.MinDuration, MaxDuration: c.MaxDuration, ProjectID: c.ProjectID, ProjectName: projectNames[c.ProjectID],
		Warnings: len(c.Warnings), CheckedAt: c.Check.At, CheckedBy: c.Check.By.UserName, ApprovedAt: c.ApprovedAt, Sheet: c.Sheet, URL: links.Motion(c.Hash)}
	if c.ApprovedBy != nil {
		r.ApprovedBy = c.ApprovedBy.UserName
	}
	return r
}

func projectNamesOf(ctx context.Context, a *app.App, cs []models.MotionComponent) map[string]string {
	out := map[string]string{}
	for _, c := range cs {
		if c.ProjectID == "" || out[c.ProjectID] != "" {
			continue
		}
		var p models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), c.ProjectID, &p) == nil {
			out[c.ProjectID] = p.Name
		}
	}
	return out
}

// motionDir finds the component folder for a check.
func motionDir(in *MotionCheckIn) (string, error) {
	if in.Dir != "" {
		return filepath.Abs(in.Dir)
	}
	if in.Name == "" {
		return "", invalid("Say which motion graphic to check: its folder, or its name inside a project folder.")
	}
	if !motion.ValidName(in.Name) {
		return "", apperr.New(apperr.InvalidInput, "“%s” can't name a motion graphic.", in.Name).WithHint("Use lowercase letters, digits and dashes, starting with a letter (like route-map).")
	}
	cwd, _ := os.Getwd()
	for d := cwd; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, lockPath)); err == nil {
			return filepath.Join(d, "motion", in.Name), nil
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	return filepath.Join(cwd, "motion", in.Name), nil
}

// insideAssistant reports whether dir is in the dashboard assistant's
// conversations (where Claude writes proposals).
func insideAssistant(a *app.App, dir string) bool {
	root, err := filepath.EvalSymlinks(filepath.Join(a.Cfg.AssistantDir(), "conversations"))
	if err != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, real)
	return err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) && strings.Contains(filepath.ToSlash(rel), "/proposals/")
}

// checkFrame decides the test frames' shapes, platform and brand (D377): the
// project's — named, the folder's, or for a version drawn again the one it
// was checked in — and the shapes motionShapes finds. again is a version
// already checked here that is drawn again: the shapes it was drawn in are
// kept after the new ones.
func checkFrame(ctx context.Context, a *app.App, in *MotionCheckIn, dir string, again *models.MotionComponent) (motion.CheckOptions, motionShapeSet, error) {
	o := motion.CheckOptions{Dir: dir, Duration: in.Duration, NoPreview: in.NoPreview, Platform: "generic"}
	ref := in.ProjectID
	if ref == "" {
		for d := filepath.Dir(dir); ; d = filepath.Dir(d) {
			if l, err := readLock(d); err == nil {
				ref = l.ProjectID
				break
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	fromVersion := false
	if ref == "" && again != nil && again.ProjectID != "" {
		ref, fromVersion = again.ProjectID, true
	}
	var p *models.Project
	if ref != "" {
		pp, err := resolveProject(ctx, a, ref)
		switch {
		case err == nil:
			p = pp
		case !fromVersion:
			return o, motionShapeSet{}, err
		}
	}
	if p != nil {
		o.ProjectID = p.ID
		if r, _, err := resolveRevision(ctx, a, p, "", "main"); err == nil && r != nil {
			o.Platform, o.Brand = orDefault(r.Spec.Output.Platform, "generic"), r.Spec.Brand
		}
		if o.Brand == nil {
			// No brand on the cut: the project's kit, else the workspace's (D164, D165).
			if k, err := kitForCheck(ctx, a, p.ID); err == nil && k != nil {
				o.Brand = KitBrand(k)
			}
		}
	}
	var set motionShapeSet
	if v := strings.TrimSpace(in.Shape); v != "" {
		sh, err := askedShape(v)
		if err != nil {
			return o, set, err
		}
		set = motionShapeSet{Shapes: []motion.CheckShape{{Width: sh.Width, Height: sh.Height, Platform: o.Platform}}, From: shapesAsked}
	}
	if set.From == "" || again != nil {
		found := motionShapes(ctx, a, p, filepath.Base(dir), o.Platform)
		if set.From == "" {
			set.From = found.From
		}
		set.Shapes = append(set.Shapes, found.Shapes...)
	}
	if again != nil {
		// Drawn again (D381): only shapes it lacks, by ratio. What it was drawn
		// in stays as it is in its record; the new ones are added beside it.
		have := map[string]bool{}
		for _, d := range drawnShapes(again) {
			have[editspec.ShapeOfSize(d.Width, d.Height).RatioKey()] = true
		}
		var add []motion.CheckShape
		for _, sh := range set.Shapes {
			if !have[editspec.ShapeOfSize(sh.Width, sh.Height).RatioKey()] {
				add = append(add, sh)
			}
		}
		set.Shapes = add
		if n := len(drawnShapes(again)); n+min(len(motion.TidyShapes(add)), motion.MaxShapes) > MaxDrawnShapes {
			return o, set, apperr.New(apperr.Conflict, "%s is already drawn in %d shapes; MetaRush draws a graphic in at most %d.", again.Name, n, MaxDrawnShapes).
				WithHint("Check a new version of it for more shapes.")
		}
	}
	set.Shapes = motion.TidyShapes(set.Shapes)
	o.Shapes = set.Shapes
	if len(o.Shapes) > 0 {
		o.Width, o.Height, o.Platform = o.Shapes[0].Width, o.Shapes[0].Height, o.Shapes[0].Platform
	}
	return o, set, nil
}

// MaxDrawnShapes bounds the shapes one version is drawn in (motion.MaxDrawnShapes, D381).
const MaxDrawnShapes = motion.MaxDrawnShapes

// askedShape reads --shape: a shape's word or an exact, even size (video
// previews can't be drawn at an odd size).
func askedShape(v string) (editspec.OutputShape, error) {
	var w, h int
	if n, err := fmt.Sscanf(strings.ToLower(v), "%dx%d", &w, &h); err == nil && n == 2 && (w%2 != 0 || h%2 != 0) {
		return editspec.OutputShape{}, invalid("A test frame's size has even numbers (like 1080x1350): a preview video can't be drawn %dx%d.", w, h)
	}
	sh, ok := editspec.ParseShape(strings.ToLower(v))
	if !ok {
		return editspec.OutputShape{}, invalid("Draw it vertical, landscape or square, or at an exact size like 1080x1350.")
	}
	return sh, nil
}

// Where a check's shapes come from (D377).
const (
	shapesAsked     = "asked"     // --shape
	shapesCuts      = "cuts"      // the project's cuts that use the graphic
	shapesProject   = "project"   // the project's shape (ProjectShape) with its other cuts or deliverables
	shapesWorkspace = "workspace" // with no project (or one that says nothing): other projects' cuts that use a graphic of that name
	shapesNothing   = "nothing"   // nothing says: widescreen and vertical
)

// motionShapeSet is the shapes a check draws and where they come from.
type motionShapeSet struct {
	Shapes []motion.CheckShape
	From   string
}

// checkShapes are the shapes a motion graphic's test frames are drawn in
// for a project (nil: none known), tidied (motionShapes).
func checkShapes(ctx context.Context, a *app.App, p *models.Project, name string) []motion.CheckShape {
	return motion.TidyShapes(motionShapes(ctx, a, p, name, "generic").Shapes)
}

// motionShapes are the video shapes a motion graphic's test frames are drawn
// in (D164, D377), by ratio at a test size, at most three, from the first of:
// the project's cuts whose latest versions use a graphic of that name (the
// main cut's first); the project's shape when something says it
// (ProjectShape, D378) — with its other cuts' shapes when it comes from a cut,
// every deliverable's when from the brief: a check for a project is drawn for
// that project; with no project, or one that says nothing, the workspace's
// cuts that use it; else both widescreen and vertical, never silently one.
func motionShapes(ctx context.Context, a *app.App, p *models.Project, name, platform string) motionShapeSet {
	var here, elsewhere []motion.CheckShape
	for _, c := range cutsUsingMotion(ctx, a, name) {
		// Drawn at a test size of the cut's ratio, never 4K (D379).
		ts := editspec.TestSize(c.out.Width, c.out.Height)
		sh := motion.CheckShape{Width: ts.Width, Height: ts.Height, Platform: c.out.Platform, For: c.what}
		if p != nil && c.projectID == p.ID {
			here = append(here, sh)
		} else {
			elsewhere = append(elsewhere, sh)
		}
	}
	if len(here) > 0 {
		return motionShapeSet{Shapes: here, From: shapesCuts}
	}
	if p != nil {
		if ps := ProjectShape(ctx, a, p); ps.Known {
			ts := editspec.TestSize(ps.Width, ps.Height)
			out := []motion.CheckShape{{Width: ts.Width, Height: ts.Height, Platform: platform, For: p.Name}}
			switch ps.From {
			case ShapeFromCut:
				out[0].For = CutName(ctx, a, p, ps.Branch)
				for _, c := range projectCutOutputs(ctx, a, p) {
					ts := editspec.TestSize(c.out.Width, c.out.Height)
					out = append(out, motion.CheckShape{Width: ts.Width, Height: ts.Height, Platform: c.out.Platform, For: c.what})
				}
			case ShapeFromDeliverables:
				out = nil
				for _, d := range p.Brief.Deliverables {
					if s := (editspec.Output{Preset: d.Preset}).Shape(); s.Shape != "" {
						out = append(out, motion.CheckShape{Width: s.Width, Height: s.Height, Platform: d.Platform, For: d.Name})
					}
				}
			case ShapeFromPlatforms:
				// Every platform the brief names, in its shape with its margins
				// (D397): TikTok and YouTube draw both, at most three.
				out = nil
				for _, pl := range p.Brief.Platforms {
					pl = strings.ToLower(strings.TrimSpace(pl))
					if shape := editspec.PlatformShape(pl); shape != "" && len(out) < 3 {
						s := (editspec.Output{Preset: editspec.PresetFor(shape)}).Shape()
						out = append(out, motion.CheckShape{Width: s.Width, Height: s.Height, Platform: pl, For: platformWords(pl)})
					}
				}
			}
			return motionShapeSet{Shapes: out, From: shapesProject}
		}
	}
	if len(elsewhere) > 0 {
		return motionShapeSet{Shapes: elsewhere, From: shapesWorkspace}
	}
	return motionShapeSet{Shapes: motion.BothShapes(platform), From: shapesNothing}
}

// cutUsing is the latest version of one cut, by what it is called.
type cutUsing struct {
	projectID, branch, what string
	out                     editspec.Output
}

// cutsUsingMotion are the latest versions of the workspace's cuts (archived
// projects aside) that use a motion graphic of that name, main cuts first.
func cutsUsingMotion(ctx context.Context, a *app.App, name string) []cutUsing {
	return latestCuts(ctx, a, bson.M{"archived": bson.M{"$ne": true}}, bson.M{"spec.overlays.motion.name": name})
}

// projectCutOutputs are a project's cuts' latest versions, main first.
func projectCutOutputs(ctx context.Context, a *app.App, p *models.Project) []cutUsing {
	return latestCuts(ctx, a, bson.M{"_id": p.ID}, nil)
}

func latestCuts(ctx context.Context, a *app.App, projects, revisions bson.M) []cutUsing {
	var ps []models.Project
	if a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), projects, &ps, options.Find().SetProjection(bson.M{"name": 1, "branches": 1}).SetLimit(2000)) != nil {
		return nil
	}
	type at struct {
		p      *models.Project
		branch string
	}
	where := map[string]at{}
	var heads []string
	for i := range ps {
		for b, h := range ps[i].Branches {
			if h.Head != "" {
				where[h.Head] = at{&ps[i], b}
				heads = append(heads, h.Head)
			}
		}
	}
	if len(heads) == 0 {
		return nil
	}
	filter := bson.M{"_id": bson.M{"$in": heads}}
	for k, v := range revisions {
		filter[k] = v
	}
	var rs []models.Revision
	if a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), filter, &rs, options.Find().SetProjection(bson.M{"spec.output": 1, "spec.title": 1})) != nil {
		return nil
	}
	var out []cutUsing
	for _, r := range rs {
		w := where[r.ID]
		o := r.Spec.Output
		sh := o.Shape()
		if sh.Shape == "" {
			continue
		}
		o.Width, o.Height = sh.Width, sh.Height
		out = append(out, cutUsing{projectID: w.p.ID, branch: w.branch, what: orDefault(r.Spec.Title, w.p.Name), out: o})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].branch == "main") != (out[j].branch == "main") {
			return out[i].branch == "main"
		}
		if out[i].what != out[j].what {
			return out[i].what < out[j].what
		}
		return out[i].branch < out[j].branch
	})
	return out
}

// drawnShapes are the shapes a checked version was drawn in: its recorded
// shapes, else (checked before D164) the one its check used.
func drawnShapes(c *models.MotionComponent) []models.MotionShape {
	if len(c.Shapes) > 0 {
		return c.Shapes
	}
	if c.Check.Width > 0 && c.Check.Height > 0 {
		return []models.MotionShape{{Label: motion.ShapeLabel(c.Check.Width, c.Check.Height), Width: c.Check.Width, Height: c.Check.Height}}
	}
	return nil
}

// shapesWords says the shapes a check drew, and why when nothing said which.
func shapesWords(set motionShapeSet) string {
	var ws []string
	for _, s := range set.Shapes {
		ws = append(ws, s.Label)
	}
	if len(ws) == 0 {
		return ""
	}
	if set.From == shapesNothing {
		return "Nothing says which shape it is for yet, so its test frames are " + joinAnd(ws) + "."
	}
	return "Its test frames are " + joinAnd(ws) + "."
}

// kitForCheck is the brand kit a check draws with when the cut has no brand:
// the project's own kit, else the workspace's (D165).
func kitForCheck(ctx context.Context, a *app.App, projectID string) (*models.BrandKit, error) {
	k, err := KitFor(ctx, a, projectID)
	if err != nil || k == nil || kitEmpty(k) {
		return nil, err
	}
	return k, nil
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

func checkSummary(r *motion.CheckResult) string {
	errs, warns := 0, 0
	for _, p := range r.Problems {
		if p.Severity == "error" {
			errs++
		} else {
			warns++
		}
	}
	if !r.OK {
		first := ""
		for _, p := range r.Problems {
			if p.Severity == "error" {
				first = p.Message
				if p.File != "" && p.Line > 0 {
					first = fmt.Sprintf("%s line %d: %s", p.File, p.Line, p.Message)
				}
				break
			}
		}
		return fmt.Sprintf("%s didn't pass the check (%s). %s", r.Name, pluralWord(errs, "problem"), first)
	}
	s := fmt.Sprintf("%s passed the check.", r.Name)
	if warns > 0 {
		s += fmt.Sprintf(" %s to look at.", strings.ToUpper(pluralWord(warns, "note")[:1])+pluralWord(warns, "note")[1:])
	}
	switch r.Component.State {
	case models.MotionApproved:
		s += " This exact version is already approved."
	case models.MotionWaiting:
		s += " A person needs to approve this version before it can be used in a cut."
	case models.MotionRejected:
		s += " A person turned this exact version down before."
	case models.MotionRevoked:
		s += " Approval of this exact version was withdrawn."
	}
	return s
}

func pluralWord(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return fmt.Sprintf("%d %ss", n, w)
}

// fieldsOf lists a parameter schema's fields in plain terms.
func fieldsOf(schema json.RawMessage) []MotionField {
	var s struct {
		Properties map[string]struct {
			Type        any    `json:"type"`
			Description string `json:"description"`
			Enum        []any  `json:"enum"`
			AnyOf       []any  `json:"anyOf"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	out := []MotionField{}
	if json.Unmarshal(schema, &s) != nil {
		return out
	}
	req := map[string]bool{}
	for _, r := range s.Required {
		req[r] = true
	}
	for name, p := range s.Properties {
		t := "value"
		switch v := p.Type.(type) {
		case string:
			t = map[string]string{"string": "text", "number": "number", "integer": "whole number", "boolean": "yes/no", "array": "list", "object": "group"}[v]
		case []any:
			t = "value"
		}
		if len(p.Enum) > 0 {
			var opts []string
			for _, e := range p.Enum {
				opts = append(opts, fmt.Sprint(e))
			}
			t = "one of " + strings.Join(opts, ", ")
		}
		if t == "" {
			t = "value"
		}
		out = append(out, MotionField{Name: name, Type: t, Required: req[name], Description: p.Description})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Required != out[j].Required {
			return out[i].Required
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// loadArchive fetches a version's sources from the media store (verified).
func loadArchive(ctx context.Context, a *app.App, c *models.MotionComponent) (*motion.Archive, error) {
	p, err := a.Cache.Ensure(ctx, a.Media, c.Archive.Key, c.Archive.SHA256, c.Archive.Size, nil)
	if err != nil {
		return nil, apperr.Wrap(apperr.Storage, err, "fetch the motion graphic's sources")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	ar, err := motion.Parse(b)
	if err != nil {
		return nil, apperr.Wrap(apperr.Integrity, err, "the stored sources of %s", c.Name)
	}
	if ar.Hash != c.Hash {
		return nil, apperr.New(apperr.Integrity, "the stored sources of %s don't match its version", c.Name)
	}
	return ar, nil
}

func showMotion(ctx context.Context, a *app.App, ref string) (*MotionShowOut, error) {
	c, err := motion.Resolve(ctx, a.DB, a.WorkspaceID(), ref)
	if err != nil {
		return nil, err
	}
	versions, err := motion.Find(ctx, a.DB, a.WorkspaceID(), motion.Ref{Name: c.Name})
	if err != nil {
		return nil, err
	}
	names := projectNamesOf(ctx, a, append(versions, *c))
	out := &MotionShowOut{Row: motionRow(c, names), Component: *c, Source: []MotionSourceFile{}, Fields: fieldsOf(json.RawMessage(c.SchemaJSON)),
		Schema: json.RawMessage(c.SchemaJSON), Versions: []MotionRow{}, UsedBy: []MotionUse{}}
	if len(out.Schema) == 0 {
		out.Schema = json.RawMessage(`{}`)
	}
	if c.PreviewJSON != "" && json.Valid([]byte(c.PreviewJSON)) {
		out.Preview = json.RawMessage(c.PreviewJSON)
	}
	var prev *models.MotionComponent
	for i := range versions {
		v := &versions[i]
		out.Versions = append(out.Versions, motionRow(v, names))
		if prev == nil && v.Hash != c.Hash && (v.State == models.MotionApproved || (v.ApprovedAt != nil && v.State == models.MotionRevoked)) {
			prev = v
		}
	}
	ar, err := loadArchive(ctx, a, c)
	if err != nil {
		return nil, err
	}
	var before *motion.Archive
	if prev != nil {
		r := motionRow(prev, names)
		out.Previous = &r
		before, _ = loadArchive(ctx, a, prev)
	}
	for _, f := range ar.Files {
		sf := MotionSourceFile{Path: f.Path, Text: f.Text, Change: "new"}
		if before != nil {
			old, ok := before.File(f.Path)
			switch {
			case !ok:
				sf.Change = "added"
			case old == f.Text:
				sf.Change = "same"
			default:
				sf.Change = "changed"
				sf.Changes = contextOnly(cutsheet.LineDiff(old, f.Text), 3)
			}
		}
		out.Source = append(out.Source, sf)
	}
	if before != nil {
		for _, f := range before.Files {
			if _, ok := ar.File(f.Path); !ok {
				out.Source = append(out.Source, MotionSourceFile{Path: f.Path, Change: "removed"})
			}
		}
	}
	out.UsedBy = motionUses(ctx, a, c.Hash)
	out.Shapes = shapeViews(c)
	if c.Sheet != nil && c.Sheet.Key != "" {
		if p, err := a.Cache.Ensure(ctx, a.Media, c.Sheet.Key, c.Sheet.SHA256, c.Sheet.Size, nil); err == nil {
			out.SheetPath = p
		}
	}
	return out, nil
}

// shapeViews lists the shapes a version was drawn in and whether a person
// approved it in each (D381).
func shapeViews(c *models.MotionComponent) []MotionShapeView {
	approved := c.ApprovedSizes()
	added := map[string]bool{}
	for _, d := range c.Draws {
		for _, k := range d.Shapes {
			added[k] = true
		}
	}
	labels := map[string]string{}
	for _, sh := range c.Shapes {
		labels[editspec.ShapeOfSize(sh.Width, sh.Height).Key()] = sh.Label
	}
	out := []MotionShapeView{}
	for _, k := range c.DrawnSizes() {
		var w, h int
		if _, err := fmt.Sscanf(k, "%dx%d", &w, &h); err != nil {
			continue
		}
		s := editspec.ShapeOfSize(w, h)
		v := MotionShapeView{Size: k, Label: orDefault(labels[k], motion.ShapeLabel(w, h)), Width: w, Height: h, Ratio: s.Ratio, Added: added[k]}
		for _, ak := range approved {
			if ak == k || ratioOf(ak) == s.RatioKey() {
				v.Approved = true
			}
		}
		out = append(out, v)
	}
	return out
}

func ratioOf(k string) string {
	var w, h int
	if _, err := fmt.Sscanf(k, "%dx%d", &w, &h); err != nil {
		return ""
	}
	return editspec.ShapeOfSize(w, h).RatioKey()
}

// contextOnly keeps changed lines and a few around them.
func contextOnly(lines []cutsheet.DiffLine, n int) []cutsheet.DiffLine {
	keep := make([]bool, len(lines))
	for i, l := range lines {
		if l.Op != "same" {
			for k := max(0, i-n); k <= min(len(lines)-1, i+n); k++ {
				keep[k] = true
			}
		}
	}
	var out []cutsheet.DiffLine
	for i, l := range lines {
		if keep[i] {
			out = append(out, l)
		}
	}
	return out
}

// motionUses lists the latest cut of each branch that uses a version.
func motionUses(ctx context.Context, a *app.App, hash string) []MotionUse {
	var ps []models.Project
	if a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), nil, &ps) != nil {
		return []MotionUse{}
	}
	out := []MotionUse{}
	for _, p := range ps {
		for _, b := range p.Branches {
			if b.Head == "" {
				continue
			}
			var r models.Revision
			if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), b.Head, &r) != nil {
				continue
			}
			for _, h := range editspec.MotionHashes(&r.Spec) {
				if h == hash {
					out = append(out, MotionUse{ProjectID: p.ID, ProjectName: p.Name, RevisionID: r.ID, Number: r.Number})
					break
				}
			}
		}
	}
	return out
}

func decideMotion(ctx context.Context, a *app.App, ref, verb, note string, shapes ...string) (*MotionDecideOut, error) {
	r, err := motion.ParseRef(ref)
	if err != nil {
		return nil, err
	}
	if r.Hash == "" {
		return nil, apperr.New(apperr.InvalidInput, "Approval is for one exact version of %s: say which version (for example %s@3fa9c2d1).", r.Name, r.Name).
			WithHint("Motion graphics in the dashboard lists the versions waiting for a person.").WithCommand("metarush motion list --state waiting")
	}
	c, err := motion.Resolve(ctx, a.DB, a.WorkspaceID(), ref)
	if err != nil {
		return nil, err
	}
	before := c.State
	d, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), c.Hash, verb, a.Actor, note, shapes...)
	if err != nil {
		return nil, err
	}
	action := map[string]string{motion.Approve: "motion.approve", motion.Reject: "motion.reject", motion.Revoke: "motion.revoke"}[verb]
	a.Audit(ctx, action, "motion", d.Hash, d.ProjectID, map[string]any{"name": d.Name, "hash": d.Hash, "note": note, "shapes": shapes})
	row := motionRow(d, projectNamesOf(ctx, a, []models.MotionComponent{*d}))
	summary := map[string]string{
		motion.Approve: "Approved %s. Cuts can use this exact version now; any change to its files makes a new version that needs approval again.",
		motion.Reject:  "Turned down %s. It can't be used in a cut.",
		motion.Revoke:  "Withdrew approval of %s. New renders of cuts that use it are refused until a person approves it again.",
	}[verb]
	out := &MotionDecideOut{Row: row, Summary: fmt.Sprintf(summary, row.Version)}
	if verb == motion.Approve && before == models.MotionApproved && len(d.Decisions) > 0 {
		// More shapes for an approval (D381): cuts of those ratios are covered now.
		var ws []string
		for _, k := range d.Decisions[len(d.Decisions)-1].Shapes {
			ws = append(ws, sizeWords(k))
		}
		out.Summary = fmt.Sprintf("Approved %s in %s too. Cuts in that shape use it without a note now.", row.Version, joinAnd(ws))
	}
	if verb == motion.Reject {
		// The note reaches the conversation that made the graphic (D186).
		out.ToldClaude = tellTurnedDown(ctx, a, TurnedDown{ProjectID: d.ProjectID, MotionHash: d.Hash, MotionName: d.Name, Note: note})
	}
	if verb == motion.Approve && d.Kind == models.MotionKindLibrary {
		// One approval for a theme change (D171): the latest cuts move to it.
		out.Summary = fmt.Sprintf("Approved %s, the shared library. Every graphic that uses it can use this version now.", row.Version)
		moves, problems := moveLibraryPins(ctx, a, d)
		out.Moved = moves
		out.Summary += libraryMoveWords(moves, problems)
		// Layout packs move only by a person's choice: they are offered.
		if out.Packs = PacksOnOlderLibrary(ctx, a, d.Name, d.Hash); len(out.Packs) > 0 {
			var names []string
			for _, p := range out.Packs {
				names = append(names, q(p.Project)+"'s settings")
			}
			out.Summary += " The layout pack of " + joinAnd(names) + " uses an older version: use this one there under Settings → Layout pack."
		}
	}
	return out, nil
}

const starterIndex = `import { AbsoluteFill, useCurrentFrame } from 'remotion';
import { z } from 'zod';
import { makeOverlayClock, rgba, type MotionMeta, type MotionProps } from '@metarush/motion';

// The graphic's parameters: what a cut fills in (checked before every render).
export const schema = z.object({
  title: z.string().min(1).max(60).describe('The words on screen'),
});

// What the graphic is, where it draws and how long it reads well.
export const meta: MotionMeta = {
  summary: 'A title that rises into place.',
  region: 'center',
  minDuration: 2,
  maxDuration: 6,
};

export default function Graphic({ params, durationInFrames, brand, layout }: MotionProps<typeof schema>) {
  const frame = useCurrentFrame();
  const c = makeOverlayClock(frame, durationInFrames, layout.fps, 0.8);
  const rise = c.enter(0, 0.8);
  const { content, unit } = layout;
  return (
    <AbsoluteFill style={{ pointerEvents: 'none' }}>
      <div
        style={{
          position: 'absolute',
          left: content.left,
          width: content.width,
          top: content.top,
          height: content.height,
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          textAlign: 'center',
          fontFamily: brand.fontFamily,
          fontSize: 96 * unit,
          fontWeight: 650,
          color: brand.text,
          opacity: rise * c.exit,
          transform: ` + "`translateY(${(1 - rise) * 40 * unit}px)`" + `,
          textShadow: ` + "`0 ${2 * unit}px ${18 * unit}px ${rgba(brand.background, 0.5)}`" + `,
        }}
      >
        {params.title}
      </div>
    </AbsoluteFill>
  );
}
`

func newMotion(in *MotionNewIn) (*MotionNewOut, error) {
	if !motion.ValidName(in.Name) {
		return nil, apperr.New(apperr.InvalidInput, "“%s” can't name a motion graphic.", in.Name).WithHint("Use lowercase letters, digits and dashes, starting with a letter (like route-map).")
	}
	base := in.Dir
	if base == "" {
		base, _ = os.Getwd()
	}
	base, err := filepath.Abs(base)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, "motion", in.Name)
	if filepath.Base(base) == "motion" {
		dir = filepath.Join(base, in.Name)
	}
	if _, err := os.Stat(dir); err == nil {
		return nil, apperr.New(apperr.Conflict, "%s already exists.", filepath.Join("motion", in.Name)).WithHint("Edit it, or pick another name.")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	files := map[string]string{motion.Entry: starterIndex, motion.PreviewFile: "{ \"title\": \"Hello\" }\n"}
	var names []string
	for n, body := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	sort.Strings(names)
	return &MotionNewOut{Dir: dir, Files: names, Summary: fmt.Sprintf("Started the motion graphic %s: a title that rises into place. Change it, then check it.", in.Name),
		Next: "Change it until it draws what you want, then check it so a person can approve it."}, nil
}

func init() {
	Def("motion.new", "Start a project motion graphic: motion/<name>/index.tsx (component, zod schema, meta) and preview.json", Flags{LocalOnly: true}, func(ctx context.Context, a *app.App, in *MotionNewIn) (*MotionNewOut, error) {
		return newMotion(in)
	})
	Def("motion.check", "Check a project motion graphic: canonical archive and hash, import scan, type check, bundle, test frames and a preview in a network-jailed browser; a passing version is recorded for a person to approve", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *MotionCheckIn) (*MotionCheckOut, error) {
		kept := ""
		var again *models.MotionComponent
		if v := strings.TrimSpace(in.Version); v != "" {
			name, hash, ok := strings.Cut(v, "@")
			hash = strings.ToLower(hash)
			if !ok || !motion.ValidName(name) || hash == "" {
				return nil, invalid("Say which version as name@version.")
			}
			// A version checked here is drawn again (D377): the same code and
			// hash, so what people decided about it stays.
			c, err := motion.Resolve(ctx, a.DB, a.WorkspaceID(), name+"@"+hash)
			switch {
			case err == nil:
				again, hash = c, c.Hash
				if c.Kind == models.MotionKindLibrary {
					return nil, invalid("%s is a shared library: it draws nothing of its own. Draw the graphics that use it again.", c.Name+"@"+motion.Short(c.Hash))
				}
			case !apperr.Is(err, apperr.NotFound):
				return nil, err
			case !motion.ValidHash(hash):
				// A version a file from another workspace brought (D219) is named whole.
				return nil, invalid("Say which kept version to check as name@version, with the whole version.")
			}
			tmp, err := os.MkdirTemp(a.Cfg.Scratch.Dir, "motion-kept-")
			if err != nil {
				return nil, err
			}
			defer os.RemoveAll(tmp)
			if kept, err = motion.Kept(ctx, a, name, hash, tmp); err != nil {
				return nil, err
			}
			in.Dir, in.Name = kept, ""
			if again != nil && len(in.Libraries) == 0 && len(again.CheckedWith) > 0 {
				// Drawn with the libraries it was checked with: nothing about it changes.
				in.Libraries = map[string]string{}
				for _, l := range again.CheckedWith {
					in.Libraries[l.Name] = l.Hash
				}
			}
		}
		for l, h := range in.Libraries {
			if !motion.ValidName(l) || !motion.ValidHash(h) {
				return nil, invalid("A shared library to check with is name → its whole version.")
			}
		}
		dir, err := motionDir(in)
		if err != nil {
			return nil, err
		}
		if callerOf(ctx).Remote && kept == "" && !insideAssistant(a, dir) {
			// From the dashboard, only components Claude wrote in a conversation
			// (an action card); other folders are checked from a terminal.
			return nil, apperr.New(apperr.Unauthorized, "From the dashboard, MetaRush only checks motion graphics Claude wrote in a conversation.").
				WithHint("Check one in a project folder from a terminal instead.").WithDetails(map[string]any{"reason": "local_only"}).WithCommand("metarush motion check <folder>")
		}
		o, set, err := checkFrame(ctx, a, in, dir, again)
		if err != nil {
			return nil, err
		}
		if again != nil && len(o.Shapes) == 0 {
			// Every shape asked for is drawn already (by ratio): nothing to add.
			return nothingNewToDraw(again), nil
		}
		if len(in.Libraries) > 0 {
			o.Libraries = in.Libraries
		}
		r, err := motion.CheckDir(ctx, a, o, nil)
		if err != nil {
			return nil, err
		}
		if kept != "" && r.Archive != nil && !strings.HasSuffix(strings.ToLower(in.Version), r.Archive.Hash) && (again == nil || again.Hash != r.Archive.Hash) {
			return nil, apperr.New(apperr.Integrity, "The kept version didn't read back as itself, so it wasn't checked.")
		}
		out := &MotionCheckOut{OK: r.OK, Name: r.Name, Hash: r.Archive.Hash, Version: r.Name + "@" + motion.Short(r.Archive.Hash), Stage: r.Stage,
			Problems: r.Problems, Imports: r.Imports, Frames: []MotionFrameOut{}, Sheet: r.Sheet, Preview: r.Preview, Meta: r.Meta, Schema: r.Schema, Ignored: r.Archive.Ignored,
			Shapes: []MotionShapeOut{}, ShapesFrom: set.From, Redrawn: again != nil}
		for _, f := range r.Frames {
			out.Frames = append(out.Frames, MotionFrameOut{Label: f.Label, At: f.At, Path: f.Path, Shape: f.Shape, Width: f.Width, Height: f.Height})
		}
		drew := set
		if len(r.Shapes) > 0 {
			drew.Shapes = r.Shapes
		}
		for _, sh := range drew.Shapes {
			out.Shapes = append(out.Shapes, MotionShapeOut{Label: sh.Label, Width: sh.Width, Height: sh.Height, For: sh.For})
		}
		if r.Component != nil {
			out.State = r.Component.State
			out.URL = dashboardURL(a, links.Motion(r.Archive.Hash))
		}
		out.Summary = checkSummary(r)
		if r.OK && again != nil {
			out.Summary = redrawSummary(r, drew)
		} else if r.OK && r.Component != nil && r.Component.Kind != models.MotionKindLibrary {
			out.Summary += " " + shapesWords(drew)
		}
		switch {
		case !r.OK:
			out.Next = "Fix the problems (file and line are given) and check it again."
		case out.State == models.MotionWaiting:
			out.Next = "Look at the test frames, then ask a person to approve it under Motion graphics in the dashboard."
		case out.State == models.MotionApproved:
			out.Next = "It's approved: add it to the cut as a graphic (in the cut sheet's Graphics, as motion " + out.Version + ")."
		}
		return out, nil
	})
	Def("motion.list", "List the project motion graphics checked in this workspace (latest version of each name, or every version), with their approval state", Flags{}, func(ctx context.Context, a *app.App, in *MotionListIn) (*MotionListOut, error) {
		cs, err := motion.List(ctx, a.DB, a.WorkspaceID(), in.All)
		if err != nil {
			return nil, err
		}
		names := projectNamesOf(ctx, a, cs)
		out := &MotionListOut{Components: []MotionRow{}}
		for i := range cs {
			if in.State != "" && cs[i].State != in.State {
				continue
			}
			if cs[i].State == models.MotionWaiting {
				out.Waiting++
			}
			out.Components = append(out.Components, motionRow(&cs[i], names))
		}
		return out, nil
	})
	Def("motion.show", "Show one motion graphic version: its source (with changes since the last approved version), imports, parameters, test frames, preview, decisions and the cuts that use it", Flags{}, func(ctx context.Context, a *app.App, in *MotionShowIn) (*MotionShowOut, error) {
		return showMotion(ctx, a, in.Ref)
	})
	Def("motion.approve", "Approve (or turn down) one exact version of a motion graphic after reviewing it — people only", Flags{Mutating: true, HumanOnly: true}, func(ctx context.Context, a *app.App, in *MotionDecideIn) (*MotionDecideOut, error) {
		verb := motion.Approve
		if in.Reject {
			verb = motion.Reject
		}
		return decideMotion(ctx, a, in.Ref, verb, in.Note, in.Shapes...)
	})
	Def("motion.revoke", "Withdraw approval of a motion graphic version: new renders and checks of cuts that use it are refused — people only", Flags{Mutating: true, HumanOnly: true}, func(ctx context.Context, a *app.App, in *MotionDecideIn) (*MotionDecideOut, error) {
		return decideMotion(ctx, a, in.Ref, motion.Revoke, in.Note)
	})
}

// MotionStudioSetup finds a component's folder and the frame its preview uses
// (`metarush motion studio`, a local convenience with no op of its own: it
// runs Remotion Studio in the person's browser until they stop it).
func MotionStudioSetup(ctx context.Context, a *app.App, in *MotionCheckIn) (dir string, o motion.CheckOptions, err error) {
	dir, o, _, err = MotionStudioShapes(ctx, a, in)
	return dir, o, err
}

// MotionStudioShapes is MotionStudioSetup with the shapes the graphic is
// drawn in (D377): Studio opens in the first — the asked shape, else the
// project's (ProjectShape) — and offers the others the graphic is used in.
func MotionStudioShapes(ctx context.Context, a *app.App, in *MotionCheckIn) (dir string, o motion.CheckOptions, others []MotionShapeOut, err error) {
	if dir, err = motionDir(in); err != nil {
		return "", o, nil, err
	}
	o, set, err := checkFrame(ctx, a, in, dir, nil)
	if err != nil {
		return dir, o, nil, err
	}
	first := o.Shapes
	if in.Shape == "" && o.ProjectID != "" {
		// Studio opens at the project's shape (D378); the cuts' shapes are offered.
		if p, perr := resolveProject(ctx, a, o.ProjectID); perr == nil {
			ps := ProjectShape(ctx, a, p)
			first = motion.TidyShapes(append([]motion.CheckShape{{Width: ps.Width, Height: ps.Height, Platform: o.Platform, For: p.Name}}, set.Shapes...))
		}
	}
	if len(first) > 0 {
		o.Width, o.Height = first[0].Width, first[0].Height
		for _, sh := range first[1:] {
			others = append(others, MotionShapeOut{Label: sh.Label, Width: sh.Width, Height: sh.Height, For: sh.For})
		}
	}
	return dir, o, others, nil
}

// redrawSummary says what drawing a checked version again did (D377, D381):
// the code and its version are the same, so its approval is too — in the
// shapes a person approved it in; the new shapes wait for a person.
func redrawSummary(r *motion.CheckResult, drew motionShapeSet) string {
	var ws []string
	for _, sh := range drew.Shapes {
		ws = append(ws, sh.Label)
	}
	s := fmt.Sprintf("Drew %s in %s too; the frames and previews it had stay as they were.", r.Name+"@"+motion.Short(r.Archive.Hash), joinAnd(ws))
	state := ""
	var approved []string
	if r.Component != nil {
		state = r.Component.State
		for _, k := range r.Component.ApprovedSizes() {
			approved = append(approved, sizeWords(k))
		}
	}
	switch state {
	case models.MotionApproved:
		return s + " Its code is unchanged, so its approval stands for the shapes a person approved it in (" + joinAnd(approved) + "). A person looks at the new frames and approves it in those shapes too; drawing it doesn't count as anyone's approval."
	case models.MotionRejected:
		return s + " Its code is unchanged, so a person's decision to turn it down stands."
	case models.MotionRevoked:
		return s + " Its code is unchanged, so its approval stays withdrawn."
	}
	return s + " Its code is unchanged; it still waits for a person's approval, which covers the shapes they look at."
}

// sizeWords says a size key as people do: "1920x1080" → "widescreen (16:9)".
func sizeWords(k string) string {
	var w, h int
	if _, err := fmt.Sscanf(k, "%dx%d", &w, &h); err != nil {
		return k
	}
	s := editspec.ShapeOfSize(w, h)
	return s.Word + " (" + s.Ratio + ")"
}

// nothingNewToDraw answers a redraw whose shapes were all drawn before.
func nothingNewToDraw(c *models.MotionComponent) *MotionCheckOut {
	var ws []string
	for _, k := range c.DrawnSizes() {
		ws = append(ws, sizeWords(k))
	}
	out := &MotionCheckOut{OK: true, Name: c.Name, Hash: c.Hash, Version: c.Name + "@" + motion.Short(c.Hash), State: c.State, Problems: []models.MotionProblem{}, Imports: c.Imports,
		Frames: []MotionFrameOut{}, Shapes: []MotionShapeOut{}, Redrawn: true,
		Summary: fmt.Sprintf("%s is already drawn in %s; nothing new to draw. Its frames and its approval are unchanged.", c.Name+"@"+motion.Short(c.Hash), joinAnd(ws))}
	if out.Imports == nil {
		out.Imports = []models.MotionImport{}
	}
	return out
}
