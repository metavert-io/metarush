package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
	ProjectID string  `json:"projectId,omitempty" jsonschema:"description=Project (ID or name) whose shape\\, platform and brand the test frames use. Default: the project folder's."`
	Shape     string  `json:"shape,omitempty" jsonschema:"enum=vertical,enum=landscape,enum=square,description=The frame the test frames use (default: the project's latest cut\\, else vertical)."`
	Duration  float64 `json:"duration,omitempty" jsonschema:"minimum=0,maximum=60,description=Seconds to run the graphic for in the test render (kept within its meta)."`
	NoPreview bool    `json:"noPreview,omitempty" jsonschema:"description=Skip the short preview video (faster while iterating; a person reviewing it wants one)."`
	// Version checks a version MetaRush keeps but hasn't checked in this
	// workspace — one brought in with a file from another workspace (D219).
	Version string `json:"version,omitempty" jsonschema:"description=A version MetaRush keeps but hasn't checked here (brought in with a file from another workspace): name@version\\, with the whole version."`
	// Libraries are the shared libraries to check it with: name → version
	// (default: the newest approved one, else the newest checked).
	Libraries map[string]string `json:"libraries,omitempty" jsonschema:"description=Shared libraries to check it with: name → version (default: the newest approved\\, else the newest checked)."`
}

// MotionFrameOut is one test frame on this machine.
type MotionFrameOut struct {
	Label string  `json:"label"`
	At    float64 `json:"at" jsonschema:"description=Seconds from the graphic's start."`
	Path  string  `json:"path"`
	// Shape is the shape of video it was drawn in (D164): widescreen, vertical, square.
	Shape string `json:"shape,omitempty"`
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

// checkFrame decides the test frames' shape, platform and brand: the
// project's latest cut when there is one.
func checkFrame(ctx context.Context, a *app.App, in *MotionCheckIn, dir string) (motion.CheckOptions, error) {
	o := motion.CheckOptions{Dir: dir, Duration: in.Duration, NoPreview: in.NoPreview, Width: 1080, Height: 1920, Platform: "generic"}
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
	if ref != "" {
		p, err := resolveProject(ctx, a, ref)
		if err != nil {
			return o, err
		}
		o.ProjectID = p.ID
		if r, _, err := resolveRevision(ctx, a, p, "", "main"); err == nil && r != nil {
			o.Width, o.Height, o.Platform, o.Brand = r.Spec.Output.Width, r.Spec.Output.Height, r.Spec.Output.Platform, r.Spec.Brand
		}
		if in.Shape == "" {
			o.Shapes = checkShapes(ctx, a, p, filepath.Base(dir))
		}
		if o.Brand == nil {
			// No brand on the cut: the project's kit, else the workspace's (D164, D165).
			if k, err := kitForCheck(ctx, a, p.ID); err == nil && k != nil {
				o.Brand = KitBrand(k)
			}
		}
	}
	switch in.Shape {
	case "vertical":
		o.Width, o.Height = 1080, 1920
	case "landscape":
		o.Width, o.Height = 1920, 1080
	case "square":
		o.Width, o.Height = 1080, 1080
	}
	return o, nil
}

// checkShapes are the video shapes a motion graphic's test frames are drawn in
// (D164): the shapes of the project's cuts (every branch's latest version)
// that use a graphic of that name, else of all its cuts, else of the brief's
// deliverables; at most three, the main cut's first.
func checkShapes(ctx context.Context, a *app.App, p *models.Project, name string) []motion.CheckShape {
	type cut struct {
		branch string
		out    editspec.Output
		uses   bool
		title  string
	}
	var cuts []cut
	branches := make([]string, 0, len(p.Branches))
	for b := range p.Branches {
		branches = append(branches, b)
	}
	sort.Slice(branches, func(i, j int) bool {
		if (branches[i] == "main") != (branches[j] == "main") {
			return branches[i] == "main"
		}
		return branches[i] < branches[j]
	})
	for _, b := range branches {
		var r models.Revision
		if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), p.Branches[b].Head, &r); err != nil {
			continue
		}
		s := editspec.Clone(&r.Spec)
		editspec.Normalize(s)
		c := cut{branch: b, out: s.Output, title: s.Title}
		for _, ov := range s.Overlays {
			if ov.Motion != nil && ov.Motion.Name == name {
				c.uses = true
			}
		}
		cuts = append(cuts, c)
	}
	var out []motion.CheckShape
	seen := map[[2]int]bool{}
	add := func(w, h int, platform, forWhat string) {
		if w <= 0 || h <= 0 || seen[[2]int{w, h}] || len(out) >= 3 {
			return
		}
		seen[[2]int{w, h}] = true
		out = append(out, motion.CheckShape{Label: motion.ShapeLabel(w, h), Width: w, Height: h, Platform: platform, For: forWhat})
	}
	for _, c := range cuts {
		if c.uses {
			add(c.out.Width, c.out.Height, c.out.Platform, orDefault(c.title, p.Name))
		}
	}
	if len(out) == 0 {
		for _, c := range cuts {
			add(c.out.Width, c.out.Height, c.out.Platform, orDefault(c.title, p.Name))
		}
	}
	if len(out) == 0 {
		for _, d := range p.Brief.Deliverables {
			if wh, ok := editspec.Presets[d.Preset]; ok {
				add(wh[0], wh[1], d.Platform, d.Name)
			}
		}
	}
	return out
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
	if c.Sheet != nil && c.Sheet.Key != "" {
		if p, err := a.Cache.Ensure(ctx, a.Media, c.Sheet.Key, c.Sheet.SHA256, c.Sheet.Size, nil); err == nil {
			out.SheetPath = p
		}
	}
	return out, nil
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

func decideMotion(ctx context.Context, a *app.App, ref, verb, note string) (*MotionDecideOut, error) {
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
	d, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), c.Hash, verb, a.Actor, note)
	if err != nil {
		return nil, err
	}
	action := map[string]string{motion.Approve: "motion.approve", motion.Reject: "motion.reject", motion.Revoke: "motion.revoke"}[verb]
	a.Audit(ctx, action, "motion", d.Hash, d.ProjectID, map[string]any{"name": d.Name, "hash": d.Hash, "note": note})
	row := motionRow(d, projectNamesOf(ctx, a, []models.MotionComponent{*d}))
	summary := map[string]string{
		motion.Approve: "Approved %s. Cuts can use this exact version now; any change to its files makes a new version that needs approval again.",
		motion.Reject:  "Turned down %s. It can't be used in a cut.",
		motion.Revoke:  "Withdrew approval of %s. New renders of cuts that use it are refused until a person approves it again.",
	}[verb]
	out := &MotionDecideOut{Row: row, Summary: fmt.Sprintf(summary, row.Version)}
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
		if strings.TrimSpace(in.Version) != "" {
			// A version a file from another workspace brought (D219): unpacked from what MetaRush keeps.
			name, hash, ok := strings.Cut(strings.TrimSpace(in.Version), "@")
			if !ok || !motion.ValidHash(strings.ToLower(hash)) {
				return nil, invalid("Say which kept version to check as name@version, with the whole version.")
			}
			tmp, err := os.MkdirTemp(a.Cfg.Scratch.Dir, "motion-kept-")
			if err != nil {
				return nil, err
			}
			defer os.RemoveAll(tmp)
			if kept, err = motion.Kept(ctx, a, name, strings.ToLower(hash), tmp); err != nil {
				return nil, err
			}
			in.Dir, in.Name = kept, ""
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
		o, err := checkFrame(ctx, a, in, dir)
		if err != nil {
			return nil, err
		}
		if len(in.Libraries) > 0 {
			o.Libraries = in.Libraries
		}
		r, err := motion.CheckDir(ctx, a, o, nil)
		if err != nil {
			return nil, err
		}
		if kept != "" && r.Archive != nil && !strings.HasSuffix(in.Version, r.Archive.Hash) {
			return nil, apperr.New(apperr.Integrity, "The kept version didn't read back as itself, so it wasn't checked.")
		}
		out := &MotionCheckOut{OK: r.OK, Name: r.Name, Hash: r.Archive.Hash, Version: r.Name + "@" + motion.Short(r.Archive.Hash), Stage: r.Stage,
			Problems: r.Problems, Imports: r.Imports, Frames: []MotionFrameOut{}, Sheet: r.Sheet, Preview: r.Preview, Meta: r.Meta, Schema: r.Schema, Ignored: r.Archive.Ignored}
		for _, f := range r.Frames {
			out.Frames = append(out.Frames, MotionFrameOut{Label: f.Label, At: f.At, Path: f.Path, Shape: f.Shape})
		}
		if r.Component != nil {
			out.State = r.Component.State
			out.URL = dashboardURL(a, links.Motion(r.Archive.Hash))
		}
		out.Summary = checkSummary(r)
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
		return decideMotion(ctx, a, in.Ref, verb, in.Note)
	})
	Def("motion.revoke", "Withdraw approval of a motion graphic version: new renders and checks of cuts that use it are refused — people only", Flags{Mutating: true, HumanOnly: true}, func(ctx context.Context, a *app.App, in *MotionDecideIn) (*MotionDecideOut, error) {
		return decideMotion(ctx, a, in.Ref, motion.Revoke, in.Note)
	})
}

// MotionStudioSetup finds a component's folder and the frame its preview uses
// (`metarush motion studio`, a local convenience with no op of its own: it
// runs Remotion Studio in the person's browser until they stop it).
func MotionStudioSetup(ctx context.Context, a *app.App, in *MotionCheckIn) (dir string, o motion.CheckOptions, err error) {
	if dir, err = motionDir(in); err != nil {
		return "", o, err
	}
	o, err = checkFrame(ctx, a, in, dir)
	return dir, o, err
}

var _ = errors.New
