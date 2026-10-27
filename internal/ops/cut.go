package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/names"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
)

// Cut sheets (internal/cutsheet) are the Markdown form of a revision's edit spec.
// These ops show a revision as a sheet, check an edited sheet, compare it with
// its base and the current head, and save it: the sheet becomes an ordinary
// edit plan (typed ops against its base, so disjoint edits rebase) that goes
// through the same check → confirm token → immutable revision path as
// edit.check/edit.apply.

// CutShowIn picks a revision to show as a cut sheet.
type CutShowIn struct {
	ProjectID string `json:"projectId" jsonschema:"description=Project ID (or its exact name)."`
	Revision  string `json:"revision,omitempty" jsonschema:"description=Revision ID or rN (its number in the project); empty for the latest on the branch."`
	Branch    string `json:"branch,omitempty"`
	Format    string `json:"format,omitempty" jsonschema:"enum=md,enum=json,description=md (default) returns the sheet; json also returns the spec."`
}

// CutShowOut is a revision as a cut sheet.
type CutShowOut struct {
	ProjectID string           `json:"projectId"`
	Revision  *RevisionSummary `json:"revision,omitempty"`
	Starter   bool             `json:"starter" jsonschema:"description=The project has no cut yet; the sheet is a starting point listing its footage."`
	ReadOnly  bool             `json:"readOnly" jsonschema:"description=Not the latest revision on its branch: changes should start from the latest."`
	Sheet     string           `json:"sheet"`
	Spec      *editspec.Spec   `json:"spec,omitempty"`
	SheetURL  string           `json:"sheetUrl" jsonschema:"description=Where this cut sheet opens in the dashboard."`
}

// CutCheckIn is an edited cut sheet to check.
type CutCheckIn struct {
	Sheet       string `json:"sheet" jsonschema:"maxLength=4000000"`
	Instruction string `json:"instruction,omitempty" jsonschema:"maxLength=4000,description=What was asked for (overrides the sheet's change: request)."`
	Rationale   string `json:"rationale,omitempty" jsonschema:"maxLength=8000,description=Why (overrides the sheet's change: why)."`
	Origin      string `json:"origin,omitempty" jsonschema:"maxLength=200,pattern=^[a-z][a-z0-9-]*:[A-Za-z0-9_./-]+$,description=Where the sheet came from (set by MetaRush: for example cut-sheet:cli)."`
	AllowUnlock bool   `json:"allowUnlock,omitempty" jsonschema:"description=Permit changes to locked items. Only when the person explicitly asked."`
}

// CutCheckOut reports what saving the sheet would do.
type CutCheckOut struct {
	Valid          bool               `json:"valid"`
	NoChanges      bool               `json:"noChanges" jsonschema:"description=The sheet is the same cut as its base revision."`
	ProjectID      string             `json:"projectId,omitempty"`
	Branch         string             `json:"branch,omitempty"`
	BaseRevisionID string             `json:"baseRevisionId,omitempty"`
	BaseNumber     int                `json:"baseNumber,omitempty"`
	HeadRevisionID string             `json:"headRevisionId,omitempty"`
	HeadNumber     int                `json:"headNumber,omitempty"`
	HeadMoved      bool               `json:"headMoved" jsonschema:"description=Someone saved a newer revision since this sheet's base."`
	Rebased        bool               `json:"rebased" jsonschema:"description=Saving will combine these changes with the newer revision."`
	Problems       []cutsheet.Problem `json:"problems"`
	Changes        []string           `json:"changes" jsonschema:"description=What changes compared with the base (in plain words)."`
	Anchors        []cutsheet.Anchor  `json:"anchors"`
	Plan           json.RawMessage    `json:"plan,omitempty" jsonschema:"type=object,description=The edit plan this sheet becomes."`
	Diff           *editspec.Diff     `json:"diff,omitempty"`
	Duration       float64            `json:"duration"`
	SpecHash       string             `json:"specHash,omitempty"`
	ConfirmToken   string             `json:"confirmToken,omitempty"`
	ExpiresAt      *time.Time         `json:"expiresAt,omitempty"`
	Next           string             `json:"next"`
	Spec           *editspec.Spec     `json:"-"`
	sheet          *cutsheet.Sheet
	prep           *prepared
}

// CutApplyIn saves a checked cut sheet as a new revision.
type CutApplyIn struct {
	Sheet        string `json:"sheet" jsonschema:"maxLength=4000000"`
	ConfirmToken string `json:"confirmToken,omitempty" jsonschema:"description=Token from cut check for this exact sheet."`
	Instruction  string `json:"instruction,omitempty" jsonschema:"maxLength=4000"`
	Rationale    string `json:"rationale,omitempty" jsonschema:"maxLength=8000"`
	Origin       string `json:"origin,omitempty" jsonschema:"maxLength=200,pattern=^[a-z][a-z0-9-]*:[A-Za-z0-9_./-]+$"`
	AllowUnlock  bool   `json:"allowUnlock,omitempty"`
}

// CutApplyOut is the new revision, with the sheet as MetaRush now writes it
// (exact times where words were quoted).
type CutApplyOut struct {
	EditApplyOut
	Changes  []string           `json:"changes"`
	Problems []cutsheet.Problem `json:"problems" jsonschema:"description=Warnings located on the sheet lines (in plain words)."`
	Sheet    string             `json:"sheet"`
	SheetURL string             `json:"sheetUrl" jsonschema:"description=The new revision's cut sheet in the dashboard."`
}

// CutDiffIn is an edited cut sheet to compare.
type CutDiffIn struct {
	Sheet string `json:"sheet" jsonschema:"maxLength=4000000"`
}

// CutComparison compares the sheet with one revision.
type CutComparison struct {
	Revision RevisionSummary     `json:"revision"`
	Changes  []string            `json:"changes"`
	Lines    []cutsheet.DiffLine `json:"lines"`
}

// CutDiffOut compares an edited sheet with its base and with the current head.
type CutDiffOut struct {
	Valid      bool               `json:"valid"`
	Problems   []cutsheet.Problem `json:"problems"`
	Base       *CutComparison     `json:"base,omitempty"`
	Head       *CutComparison     `json:"head,omitempty" jsonschema:"description=Set when a newer revision exists than the sheet's base."`
	HeadMoved  bool               `json:"headMoved"`
	TheirEdits []string           `json:"theirEdits" jsonschema:"description=What changed between the base and the newer head (in plain words)."`
}

// CutCompareIn picks two revisions.
type CutCompareIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=Needed when a side is given as rN."`
	Left      string `json:"left"`
	Right     string `json:"right"`
}

// CutCompareOut is a readable comparison of two revisions as cut sheets.
type CutCompareOut struct {
	Left       RevisionSummary     `json:"left"`
	Right      RevisionSummary     `json:"right"`
	LeftSheet  string              `json:"leftSheet"`
	RightSheet string              `json:"rightSheet"`
	Changes    []string            `json:"changes"`
	Lines      []cutsheet.DiffLine `json:"lines"`
	Markdown   string              `json:"markdown" jsonschema:"description=The comparison as a Markdown page: the changes in words and then the sheet lines that changed."`
	SheetURL   string              `json:"sheetUrl" jsonschema:"description=The same comparison in the dashboard."`
}

// ---- footage info for sheets (lazy, from the database) ----

type sheetInfo struct {
	ctx    context.Context
	a      *app.App
	assets map[string]*models.Asset
	tx     map[string][]editspec.TranscriptSegment
	txDone map[string]bool
	voices map[string][]string
	// project is the sheet's project (resources and cuts named without a pin
	// are looked up as it sees them); view is its links.
	project string
	view    *projectlinks.View
}

// MotionResources implements cutsheet.MotionResourceLookup: the names a
// motion graphic's code uses.
func (s *sheetInfo) MotionResources(hash string) []string {
	c, err := motion.Get(s.ctx, s.a.DB, s.a.WorkspaceID(), hash)
	if err != nil {
		return nil
	}
	return c.ResourceNames()
}

// MotionLibraries implements cutsheet.MotionLibraryLookup: the shared
// libraries a graphic imports (D171).
func (s *sheetInfo) MotionLibraries(hash string) []string {
	c, err := motion.Get(s.ctx, s.a.DB, s.a.WorkspaceID(), hash)
	if err != nil {
		return nil
	}
	return c.Libraries
}

// LatestMotionLibrary implements cutsheet.MotionLibraryLookup: the version a
// sheet pins when a line names none (the newest approved one).
func (s *sheetInfo) LatestMotionLibrary(name string) string {
	c, err := motion.LatestLibrary(s.ctx, s.a.DB, s.a.WorkspaceID(), name)
	if err != nil || c == nil || c.State != models.MotionApproved {
		return ""
	}
	return c.Hash
}

// MotionVersions implements cutsheet.MotionLookup: checked versions starting with prefix.
func (s *sheetInfo) MotionVersions(prefix string) []string {
	return motion.Versions(s.ctx, s.a.DB, s.a.WorkspaceID(), prefix)
}

// MotionNote implements cutsheet.MotionLookup: where a version stands, in words.
func (s *sheetInfo) MotionNote(hash string) string {
	c, err := motion.Get(s.ctx, s.a.DB, s.a.WorkspaceID(), hash)
	if err != nil {
		return "not checked in this workspace"
	}
	switch c.State {
	case models.MotionApproved:
		if c.ApprovedBy != nil && c.ApprovedAt != nil {
			return "approved by " + firstName(c.ApprovedBy.UserName) + " · " + c.ApprovedAt.Local().Format("2 Jan 2006")
		}
		return "approved"
	case models.MotionRejected:
		return "turned down"
	case models.MotionRevoked:
		return "approval withdrawn"
	}
	return "waiting for a person to approve it"
}

func firstName(n string) string {
	if f := strings.Fields(n); len(f) > 0 {
		return f[0]
	}
	return "someone"
}

func newSheetInfo(ctx context.Context, a *app.App) *sheetInfo {
	return &sheetInfo{ctx: ctx, a: a, assets: map[string]*models.Asset{}, tx: map[string][]editspec.TranscriptSegment{}, txDone: map[string]bool{}, voices: map[string][]string{}}
}

// SheetInfo is what cut sheets show and read about the workspace — footage
// names and hashes, transcripts for quoted words, pictures, graphics, Remotion
// clips — for other forms that hold a cut sheet. projectID ("" none) is where
// names without a pin are looked up.
func SheetInfo(ctx context.Context, a *app.App, projectID string) cutsheet.Info {
	info := newSheetInfo(ctx, a)
	info.project = projectID
	return info
}

func (s *sheetInfo) asset(id string) *models.Asset {
	if as, ok := s.assets[id]; ok {
		return as
	}
	var as models.Asset
	if !ids.Valid(ids.Asset, id) || s.a.DB.GetByID(s.ctx, store.CAssets, s.a.WorkspaceID(), id, &as) != nil {
		s.assets[id] = nil
		return nil
	}
	s.assets[id] = &as
	return &as
}

func (s *sheetInfo) Footage(id string) (cutsheet.Footage, bool) {
	as := s.asset(id)
	if as == nil {
		return cutsheet.Footage{}, false
	}
	ft := cutsheet.Footage{Name: as.Name, Title: as.DisplayTitle(), SHA256: as.SHA256, Duration: as.Media.Duration, Kind: as.Kind, Speakers: s.speakersOf(as.ID)}
	switch {
	case as.Source.IsRemotion():
		ft.Source = remotionWords(as.Source.Remotion)
	case as.Source != nil:
		ft.Source = "from " + as.Source.Short()
	}
	return ft, true
}

// remotionWords says where a clip made from a Remotion source came from: "made
// from “teaser”: Intro, version 3" (D135).
func remotionWords(r *models.RemotionClipSource) string {
	name := r.SourceTitle
	if name == "" {
		name = r.SourceName
	}
	if r.Kind == models.SourceRemotionExisting {
		return "a video the Remotion project “" + name + "” already had"
	}
	w := "made from “" + name + "”: " + r.Composition
	if r.VersionNumber > 0 {
		w += ", version " + strconv.Itoa(r.VersionNumber)
	}
	if r.Props != "" {
		w += ", with its own settings"
	}
	if len(r.Frames) == 2 {
		w += fmt.Sprintf(", frames %d–%d", r.Frames[0], r.Frames[1])
	}
	return w
}

// RemotionRenderCheck refuses a new render before it is queued when the cut
// plays a clip of a Remotion version no person approved (D136; set by
// internal/remotion). Workers check again when they render.
var RemotionRenderCheck func(ctx context.Context, a *app.App, rev *models.Revision) error

// RemotionClipLookup finds the clip made of a Remotion composition for cut
// sheets (`remotion <source> <composition>` lines, D135). internal/remotion
// sets it (ops can't import that package).
var RemotionClipLookup func(ctx context.Context, a *app.App, projectID, source, composition, version, props string) cutsheet.RemotionResolved

// RemotionClip implements cutsheet.RemotionLookup.
func (s *sheetInfo) RemotionClip(source, composition, version, props string) cutsheet.RemotionResolved {
	if RemotionClipLookup == nil {
		return cutsheet.RemotionResolved{Code: "remotion_unknown", Message: "This copy of MetaRush can't look up Remotion projects."}
	}
	return RemotionClipLookup(s.ctx, s.a, s.project, source, composition, version, props)
}

// LineTimes says where each narration line lands in the cut and whether it
// fits: the derived > line under it in a cut sheet.
func (s *sheetInfo) LineTimes(spec *editspec.Spec) map[string]string {
	if spec.Voiceover == nil || len(spec.Voiceover.Beats) == 0 {
		return nil
	}
	c := editspec.Clone(spec)
	facts, err := loadFacts(s.ctx, s.a, "", c)
	if err != nil {
		return nil
	}
	return lineTimes(c, facts)
}

// lineTimes is LineTimes for a spec and its facts (c is normalized in place).
func lineTimes(c *editspec.Spec, facts editspec.Facts) map[string]string {
	out := map[string]string{}
	rep := editspec.FitReport(c, facts)
	_, total := editspec.Timeline(c)
	end := float64(total) / float64(c.Output.FPS)
	for i, f := range rep {
		out[f.BeatID] = lineTiming(f, pauseWords(rep, i, end, 0.5/float64(c.Output.FPS)))
	}
	return out
}

// pauseWords says what a tight line has no pause before, as the fit report's
// own message does (editspec's ResolvedBeat.pauseBefore): the line's time runs
// to the next line in time, or to the end of the video for the last one.
func pauseWords(rep []editspec.BeatFit, i int, end, tol float64) string {
	f := rep[i]
	until := f.Start + f.Window
	if math.Abs(until-end) <= tol {
		return "it ends right as the video does"
	}
	nextInScript := -1
	for j := i + 1; j < len(rep); j++ {
		if rep[j].Status != "unresolved" {
			nextInScript = j
			break
		}
	}
	for j, g := range rep {
		if j != i && g.Status != "unresolved" && math.Abs(g.Start-until) <= tol {
			if j != nextInScript {
				return fmt.Sprintf("no pause before line %d", g.Line)
			}
			break
		}
	}
	return "no pause before the next line"
}

func lineTiming(f editspec.BeatFit, pause string) string {
	one := func(v float64) string { return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) }
	at := "starts at " + cutsheet.Clock(math.Round(f.Start*10)/10)
	switch f.Status {
	case "unresolved":
		if i := strings.Index(f.Message, "can't be placed"); i >= 0 {
			return f.Message[i:]
		}
		return "can't be placed"
	case "ok":
		return fmt.Sprintf("%s · fits (%s of %s s)", at, one(f.Needed), one(f.Window))
	case "tight":
		return fmt.Sprintf("%s · just fits (%s of %s s), %s", at, one(f.Needed), one(f.Window), pause)
	}
	if f.Basis == "estimate" {
		return fmt.Sprintf("%s · too long: about %s s of words for %s s", at, one(f.Needed), one(f.Window))
	}
	return fmt.Sprintf("%s · too long: the audio runs %s s for %s s", at, one(f.Needed), one(f.Window))
}

// speakersOf lists who talks in a piece of footage, in order of first
// appearance: the names people gave the voices, and "Speaker N" for voices
// nobody named when there are several to tell apart. Very brief voices are
// left out.
func (s *sheetInfo) speakersOf(id string) []string {
	if names, ok := s.voices[id]; ok {
		return names
	}
	var names []string
	if vs, err := speakers.Voices(s.ctx, s.a, id, false); err == nil {
		var keep []models.AssetSpeaker
		for _, v := range vs {
			if !v.Minor {
				keep = append(keep, v)
			}
		}
		sort.SliceStable(keep, func(i, j int) bool { return keep[i].First < keep[j].First })
		seen := map[string]bool{}
		for _, v := range keep {
			name := v.Name
			if name == "" && len(keep) > 1 {
				name = v.DisplayName()
			}
			if name != "" && !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	s.voices[id] = names
	return names
}

func (s *sheetInfo) Transcript(id string) []editspec.TranscriptSegment {
	if s.txDone[id] {
		return s.tx[id]
	}
	s.txDone[id] = true
	as := s.asset(id)
	if as == nil || as.Transcript == nil || as.Transcript.State != "available" {
		return nil
	}
	segs, err := loadTranscript(s.ctx, s.a, id)
	if err != nil {
		return nil
	}
	s.tx[id] = segs
	return segs
}

// ---- resolving projects and revisions ----

var revNumRe = regexp.MustCompile(`^[rR](\d+)$`)

// resolveProject accepts a project ID or its name, matched the way people
// type names (internal/names: case and accents ignored; an exact name, else
// the only project whose name or a word of it starts with what was typed).
func resolveProject(ctx context.Context, a *app.App, ref string) (*models.Project, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, invalid("Say which project.")
	}
	var p models.Project
	if ids.Valid(ids.Project, ref) {
		if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), ref, &p); err != nil {
			return nil, apperr.New(apperr.NotFound, "That project isn't in this workspace.").WithDetails(map[string]any{"projectId": ref})
		}
		return &p, nil
	}
	var ps []models.Project
	if err := a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), bson.M{}, &ps); err != nil {
		return nil, err
	}
	var exact []models.Project
	for _, x := range ps {
		if strings.EqualFold(strings.TrimSpace(x.Name), ref) {
			exact = append(exact, x)
		}
	}
	switch len(exact) {
	case 1:
		return &exact[0], nil
	case 0:
	default:
		return nil, apperr.New(apperr.InvalidInput, "%d projects are called %q.", len(exact), ref).WithHint("Use the project's ID instead of its name.")
	}
	cands := make([]names.Candidate, 0, len(ps))
	byID := map[string]*models.Project{}
	for i := range ps {
		if ps[i].Archived {
			continue
		}
		cands = append(cands, names.Candidate{ID: ps[i].ID, Name: ps[i].Name, Weight: float64(ps[i].UpdatedAt.Unix())})
		byID[ps[i].ID] = &ps[i]
	}
	res := names.Resolve(ref, cands)
	switch {
	case res.Found != nil:
		return byID[res.Found.ID], nil
	case len(res.Ambiguous) > 0:
		return nil, apperr.New(apperr.InvalidInput, "%q could be %s.", ref, names.Quote(res.Ambiguous)).WithHint("Write more of the name, or use the project's ID.")
	case len(res.Suggest) > 0:
		return nil, apperr.New(apperr.NotFound, "There's no project called %q.", ref).WithHint("Did you mean " + names.Quote(res.Suggest) + "?")
	}
	return nil, apperr.New(apperr.NotFound, "There's no project called %q.", ref).WithHint("Your projects are listed on the dashboard's home page.").WithCommand("metarush project list")
}

// ResolveProject is resolveProject for other packages (CLI flags that take a project's name).
func ResolveProject(ctx context.Context, a *app.App, ref string) (*models.Project, error) {
	return resolveProject(ctx, a, ref)
}

// ResolveRevision finds a project's version by ID or rN, or its branch head
// when ref is empty (nil when the project has no cut yet).
func ResolveRevision(ctx context.Context, a *app.App, p *models.Project, ref, branch string) (*models.Revision, error) {
	r, _, err := resolveRevision(ctx, a, p, ref, branch)
	return r, err
}

// ProjectAssetIDs lists a project's footage (import order).
func ProjectAssetIDs(ctx context.Context, a *app.App, projectID string) []string {
	return projectAssetIDs(ctx, a, projectID)
}

func resolveProjectLoose(ctx context.Context, a *app.App, ref string) (*models.Project, error) {
	return resolveProject(ctx, a, ref)
}

// plainErr is an error's message for a person (with its hint).
func plainErr(err error) string {
	ae := apperr.From(err)
	if ae.Hint != "" {
		return ae.Message + " " + ae.Hint
	}
	return ae.Message
}

// resolveRevision finds a revision by ID, by rN within a project, or the branch
// head when ref is empty. head is the branch head's ID.
func resolveRevision(ctx context.Context, a *app.App, p *models.Project, ref, branch string) (*models.Revision, string, error) {
	if branch == "" {
		branch = "main"
	}
	head := ""
	if p != nil {
		head = p.Branches[branch].Head
	}
	ref = strings.TrimSpace(ref)
	switch {
	case ref == "" || strings.EqualFold(ref, "head") || strings.EqualFold(ref, "latest"):
		if head == "" {
			return nil, "", nil
		}
		ref = head
	case revNumRe.MatchString(ref):
		if p == nil {
			return nil, "", invalid("%s is a version number, so MetaRush needs to know the project too.", ref)
		}
		n, _ := strconv.Atoi(revNumRe.FindStringSubmatch(ref)[1])
		var rs []models.Revision
		if err := a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), bson.M{"projectId": p.ID, "number": n}, &rs); err != nil {
			return nil, "", err
		}
		if len(rs) == 0 {
			return nil, "", apperr.New(apperr.NotFound, "This project has no version %d.", n).WithHint("Check the version number in the project's history.")
		}
		return &rs[0], p.Branches[rs[0].Branch].Head, nil
	}
	r, rp, err := getRevision(ctx, a, ref)
	if err != nil {
		return nil, "", err
	}
	if p != nil && r.ProjectID != p.ID {
		return nil, "", apperr.New(apperr.InvalidInput, "That version belongs to another project.").WithDetails(map[string]any{"revision": ref, "projectId": p.ID})
	}
	return r, rp.Branches[r.Branch].Head, nil
}

// projectAssetIDs lists a project's footage in import order (IDs sort by
// creation time), so names stay stable as footage is added.
func projectAssetIDs(ctx context.Context, a *app.App, projectID string) []string {
	assets, err := listAssets(ctx, a, bson.M{"projectIds": projectID}, 1000)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(assets))
	for _, as := range assets {
		out = append(out, as.ID)
	}
	sort.Strings(out)
	return out
}

// starterOutput picks the shape and platform for a project's first sheet from
// its brief.
func starterOutput(b models.Brief) editspec.Output {
	o := editspec.Output{Preset: editspec.PresetVertical}
	for _, pl := range b.Platforms {
		pl = strings.ToLower(strings.TrimSpace(pl))
		switch pl {
		case "youtube":
			return editspec.Output{Preset: editspec.PresetLandscape, Platform: pl}
		case "shorts", "tiktok", "reels":
			return editspec.Output{Preset: editspec.PresetVertical, Platform: pl}
		}
	}
	return o
}

func baseNote(r *models.Revision) string {
	parts := []string{"r" + strconv.Itoa(r.Number), r.CreatedAt.Local().Format("2 Jan 2006 15:04")}
	if r.CreatedBy.UserName != "" {
		parts = append(parts, "by "+r.CreatedBy.UserName)
	}
	return strings.Join(parts, " · ")
}

// printRevision writes a revision as a cut sheet listing all project footage.
func printRevision(ctx context.Context, a *app.App, info *sheetInfo, r *models.Revision, withBase bool) string {
	if info.project == "" {
		info.project = r.ProjectID
	}
	o := cutsheet.Options{Info: info, Footage: projectAssetIDs(ctx, a, r.ProjectID), Branch: r.Branch}
	if withBase {
		o.ProjectID, o.BaseRevisionID, o.BaseNote = r.ProjectID, r.ID, baseNote(r)
	}
	return cutsheet.Print(&r.Spec, o)
}

// ---- checking a sheet ----

func sheetOrigin(in string, via string) string {
	if in != "" {
		return in
	}
	if via == "dashboard" {
		return "cut-sheet:studio"
	}
	return "cut-sheet:cli"
}

// checkSheet parses and validates a sheet and prepares its plan. It returns a
// result (Valid false with problems) for anything wrong with the sheet itself,
// and an error for conflicts with newer revisions.
func checkSheet(ctx context.Context, a *app.App, text, instruction, rationale, origin string, allowUnlock bool) (*CutCheckOut, error) {
	info := newSheetInfoFor(ctx, a, text)
	sheet, probs := cutsheet.Parse(text, cutsheet.ParseOptions{Info: info})
	out := &CutCheckOut{Problems: probs, Changes: []string{}, Anchors: sheet.Anchors, sheet: sheet}
	if out.Problems == nil {
		out.Problems = []cutsheet.Problem{}
	}
	if out.Anchors == nil {
		out.Anchors = []cutsheet.Anchor{}
	}
	fail := func(next string) (*CutCheckOut, error) {
		out.Valid, out.Next = false, next
		return out, nil
	}
	if cutsheet.HasErrors(probs) {
		return fail("Fix the problems (each has a line number) and check again.")
	}
	var base *models.Revision
	var proj *models.Project
	if sheet.BaseRevisionID != "" {
		r, p, err := getRevision(ctx, a, sheet.BaseRevisionID)
		if err != nil {
			out.Problems = append(out.Problems, cutsheet.Problem{Severity: "error", Code: "unknown_base", Line: 1, Col: 1, EndCol: 2,
				Message: "The version this sheet was written from (base: in the settings) isn't in this workspace.", Hint: "Start again from the latest version of the cut; MetaRush writes the right base line."})
			return fail("Fix the problems and check again.")
		}
		base, proj = r, p
		if sheet.ProjectID != "" && sheet.ProjectID != r.ProjectID {
			out.Problems = append(out.Problems, cutsheet.Problem{Severity: "error", Code: "wrong_project", Line: 1, Col: 1, EndCol: 2,
				Message: "The settings name one project, but the version this sheet was written from belongs to another.", Hint: "Remove the project: line; the base: line says which project this is."})
			return fail("Fix the problems and check again.")
		}
	} else {
		if sheet.ProjectID == "" {
			out.Problems = append(out.Problems, cutsheet.Problem{Severity: "error", Code: "missing_project", Line: 1, Col: 1, EndCol: 2,
				Message: "The settings need base: (the version this sheet was written from) or project: (for a project's first cut).", Hint: "Start from the cut sheet MetaRush writes for the project; it fills these in."})
			return fail("Fix the problems and check again.")
		}
		p, err := resolveProject(ctx, a, sheet.ProjectID)
		if err != nil {
			return nil, err
		}
		proj = p
	}
	branch := sheet.Branch
	if branch == "" {
		branch = "main"
		if base != nil {
			branch = base.Branch
		}
	}
	out.ProjectID, out.Branch = proj.ID, branch
	out.HeadRevisionID = proj.Branches[branch].Head
	if out.HeadRevisionID != "" {
		var h models.Revision
		if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), out.HeadRevisionID, &h) == nil {
			out.HeadNumber = h.Number
		}
	}
	if base != nil {
		out.BaseRevisionID, out.BaseNumber = base.ID, base.Number
		out.HeadMoved = out.HeadRevisionID != "" && out.HeadRevisionID != base.ID
	}
	if instruction == "" {
		instruction = sheet.Request
	}
	if rationale == "" {
		rationale = sheet.Why
	}
	names := cutsheet.NameFunc(sheet.Spec, info, projectAssetIDs(ctx, a, proj.ID))
	plan := editspec.Plan{Instruction: instruction, Rationale: rationale, Origin: origin, Note: sheet.Note}
	if branch != "main" {
		plan.Branch = branch
	}
	if base != nil {
		out.Changes = cutsheet.Describe(&base.Spec, sheet.Spec, names)
		if editspec.Hash(sheet.Spec) == base.SpecHash {
			out.Valid, out.NoChanges, out.Changes = true, true, []string{}
			out.SpecHash = base.SpecHash
			out.Duration = base.Duration
			out.Next = "Nothing to save: this sheet is the same cut as version " + strconv.Itoa(base.Number) + "."
			return out, nil
		}
		plan.BaseRevisionID = base.ID
		if ops, err := editspec.DiffOps(&base.Spec, sheet.Spec); err == nil {
			plan.Ops = ops
		} else {
			plan.Spec = sheet.Spec
			out.Problems = append(out.Problems, cutsheet.Problem{Severity: "warning", Code: "whole_cut", Message: "This change will be saved as a whole new cut, so it can't be combined with other edits saved in the meantime."})
		}
	} else {
		plan.ProjectID = proj.ID
		plan.Spec = sheet.Spec
		empty := &editspec.Spec{Output: sheet.Spec.Output}
		out.Changes = cutsheet.Describe(empty, sheet.Spec, names)
	}
	raw, _ := json.Marshal(plan)
	out.Plan = raw
	p, err := prepare(ctx, a, raw, allowUnlock)
	if err != nil {
		return nil, plainConflict(err, sheet)
	}
	out.prep = p
	out.Rebased = p.rebased
	var located []cutsheet.Problem
	for _, is := range append(append([]editspec.Issue{}, p.errors...), p.warnings...) {
		spec := p.spec
		if spec == nil {
			spec = sheet.Spec
		}
		located = append(located, cutsheet.Locate(is, spec, sheet.Map, info, names))
	}
	// Errors first, then in sheet order (findings without a line last).
	sort.SliceStable(located, func(i, j int) bool {
		x, y := located[i], located[j]
		if (x.Severity == "error") != (y.Severity == "error") {
			return x.Severity == "error"
		}
		if (x.Line == 0) != (y.Line == 0) {
			return y.Line == 0
		}
		return x.Line < y.Line
	})
	out.Problems = append(out.Problems, located...)
	out.Diff = p.diff
	out.Spec = p.spec
	if p.spec != nil {
		_, total := editspec.Timeline(p.spec)
		out.Duration = float64(total) / float64(p.spec.Output.FPS)
		out.SpecHash = editspec.Hash(p.spec)
	}
	out.Valid = len(p.errors) == 0
	if !out.Valid {
		out.Next = "Fix the problems (each has a line number) and check again."
	}
	return out, nil
}

// plainConflict rewrites revision conflicts and lock refusals for cut sheets.
func plainConflict(err error, sheet *cutsheet.Sheet) error {
	var ae *apperr.Error
	if !errors.As(err, &ae) {
		return err
	}
	switch ae.Code {
	case apperr.RevisionConflict:
		d, _ := ae.Details.(map[string]any)
		var items []string
		if cs, ok := d["conflicts"].([]string); ok {
			items = cs
		}
		msg := "Someone saved a newer version of this cut while you were editing, and it changes the same things you changed"
		if len(items) > 0 {
			msg += " (" + joinAnd(plainItems(items, sheet.Spec)) + ")"
		}
		return apperr.New(apperr.RevisionConflict, "%s. Nothing was overwritten.", msg).WithDetails(ae.Details).
			WithHint("Compare your sheet with the latest version and make your change again there, or save yours as a separate version (add branch: to the settings).")
	case apperr.Locked:
		what := "something that is locked"
		if id := lockedID(ae); id != "" {
			what = editspec.LockedWords(id) + ", which is locked"
			if items := plainItems([]string{id}, sheet.Spec); len(items) > 0 && id != "mix" && id != "voiceover" && id != "captions" {
				what = joinAnd(items) + ", which is locked"
			}
		}
		return apperr.New(apperr.Locked, "This change touches %s.", what).WithDetails(ae.Details).
			WithHint("Locked parts stay as they are unless the person asks to unlock them.")
	}
	return err
}

// lockedRe reads the locked item from an editspec.LockedError message ("This
// change touches “hook”, which is locked") when the error has no details.
var lockedRe = regexp.MustCompile(`touches “([^”]+)”, which is locked`)

// lockedID is the ID of the locked item a refusal names: from its details
// (edit.go's opError), else from the message.
func lockedID(ae *apperr.Error) string {
	if d, ok := ae.Details.(map[string]any); ok {
		if id, ok := d["locked"].(string); ok && id != "" {
			return id
		}
	}
	if m := lockedRe.FindStringSubmatch(ae.Message); m != nil {
		return m[1]
	}
	return ""
}

func init() {
	Def("cut.show", "Show a revision as a cut sheet (Markdown), the friendly editable form of the cut", Flags{}, func(ctx context.Context, a *app.App, in *CutShowIn) (*CutShowOut, error) {
		p, err := resolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, err
		}
		r, head, err := resolveRevision(ctx, a, p, in.Revision, in.Branch)
		if err != nil {
			return nil, err
		}
		info := newSheetInfo(ctx, a)
		out := &CutShowOut{ProjectID: p.ID, SheetURL: dashboardURL(a, links.CutSheet(p.ID, ""))}
		if r == nil {
			// No cut yet: a starting sheet that lists the project's footage.
			starter := &editspec.Spec{Title: p.Name, Output: starterOutput(p.Brief), Clips: []editspec.Clip{}}
			editspec.Normalize(starter)
			out.Starter = true
			out.Sheet = cutsheet.Print(starter, cutsheet.Options{ProjectID: p.ID, Branch: in.Branch, Info: info, Footage: projectAssetIDs(ctx, a, p.ID)})
			return out, nil
		}
		s := summarizeRev(a, *r, head)
		out.Revision = &s
		out.ReadOnly = !s.IsHead
		out.SheetURL = dashboardURL(a, links.CutSheet(p.ID, r.ID))
		out.Sheet = printRevision(ctx, a, info, r, true)
		if in.Format == "json" {
			spec := r.Spec
			out.Spec = &spec
		}
		return out, nil
	})
	Def("cut.check", "Check an edited cut sheet: problems by line, the changes in plain words, and a confirm token", Flags{}, func(ctx context.Context, a *app.App, in *CutCheckIn) (*CutCheckOut, error) {
		out, err := checkSheet(ctx, a, in.Sheet, in.Instruction, in.Rationale, sheetOrigin(in.Origin, a.Actor.Via), in.AllowUnlock)
		if err != nil {
			return nil, err
		}
		if out.Valid && !out.NoChanges && out.prep != nil {
			exp := time.Now().Add(30 * time.Minute).UTC()
			tok, err := mintToken(a, out.prep.planHash, out.BaseRevisionID, exp)
			if err != nil {
				return nil, err
			}
			out.ConfirmToken, out.ExpiresAt = tok, &exp
			out.Next = "Show the person the changes and warnings; if they agree, save the sheet (cut apply with this confirm token)."
			if out.Rebased {
				out.Next = "Someone saved a newer version meanwhile; your changes don't overlap theirs, so saving combines both. " + out.Next
			}
		}
		return out, nil
	})
	Def("cut.diff", "Compare an edited cut sheet with the revision it came from and with the latest revision", Flags{}, func(ctx context.Context, a *app.App, in *CutDiffIn) (*CutDiffOut, error) {
		info := newSheetInfo(ctx, a)
		sheet, probs := cutsheet.Parse(in.Sheet, cutsheet.ParseOptions{Info: info})
		out := &CutDiffOut{Problems: probs, TheirEdits: []string{}}
		if out.Problems == nil {
			out.Problems = []cutsheet.Problem{}
		}
		if cutsheet.HasErrors(probs) {
			return out, nil
		}
		out.Valid = true
		if sheet.BaseRevisionID == "" {
			return out, nil
		}
		base, p, err := getRevision(ctx, a, sheet.BaseRevisionID)
		if err != nil {
			return nil, err
		}
		names := cutsheet.NameFunc(sheet.Spec, info, projectAssetIDs(ctx, a, p.ID))
		mine := cutsheet.Print(sheet.Spec, cutsheet.Options{ProjectID: base.ProjectID, BaseRevisionID: base.ID, BaseNote: baseNote(base), Branch: base.Branch, Info: info, Footage: projectAssetIDs(ctx, a, p.ID)})
		head := p.Branches[base.Branch].Head
		out.Base = &CutComparison{Revision: summarizeRev(a, *base, head), Changes: cutsheet.Describe(&base.Spec, sheet.Spec, names),
			Lines: cutsheet.LineDiff(printRevision(ctx, a, info, base, true), mine)}
		if head != "" && head != base.ID {
			var h models.Revision
			if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), head, &h); err == nil {
				out.HeadMoved = true
				headSheet := printRevision(ctx, a, info, &h, false)
				mineNoBase := cutsheet.Print(sheet.Spec, cutsheet.Options{Branch: base.Branch, Info: info, Footage: projectAssetIDs(ctx, a, p.ID)})
				out.Head = &CutComparison{Revision: summarizeRev(a, h, head), Changes: cutsheet.Describe(&h.Spec, sheet.Spec, names), Lines: cutsheet.LineDiff(headSheet, mineNoBase)}
				out.TheirEdits = cutsheet.Describe(&base.Spec, &h.Spec, names)
			}
		}
		return out, nil
	})
	Def("cut.apply", "Save a checked cut sheet as a new revision (same confirm token and lock rules as edit.apply)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CutApplyIn) (*CutApplyOut, error) {
		chk, err := checkSheet(ctx, a, in.Sheet, in.Instruction, in.Rationale, sheetOrigin(in.Origin, a.Actor.Via), in.AllowUnlock)
		if err != nil {
			return nil, err
		}
		if !chk.Valid {
			return nil, apperr.New(apperr.Validation, "The cut sheet has %d problem(s).", countErrors(chk.Problems)).WithDetails(map[string]any{"problems": chk.Problems}).
				WithHint("Check the sheet to see each problem with its line.")
		}
		if chk.NoChanges {
			return nil, apperr.New(apperr.InvalidInput, "Nothing to save: this sheet is the same cut as version %d.", chk.BaseNumber)
		}
		if a.Actor.Via != "dashboard" {
			if err := checkToken(a, in.ConfirmToken, chk.prep.planHash, chk.BaseRevisionID); err != nil {
				if ae := apperr.From(err); ae.Code == apperr.ConfirmRequired {
					return nil, apperr.New(apperr.ConfirmRequired, "%s", sheetTokenMessage(ae.Message)).
						WithHint("Check this sheet first (with the same message) and look at what it changes, then save it.").
						WithCommand("metarush cut check <file>")
				}
				return nil, err
			}
		}
		res, err := commitRevision(ctx, a, chk.prep)
		if err != nil {
			return nil, plainConflict(err, chk.sheet)
		}
		var rev models.Revision
		if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), res.Revision.ID, &rev); err != nil {
			return nil, err
		}
		return &CutApplyOut{EditApplyOut: *res, Changes: chk.Changes, Problems: chk.Problems, Sheet: printRevision(ctx, a, newSheetInfo(ctx, a), &rev, true),
			SheetURL: dashboardURL(a, links.CutSheet(rev.ProjectID, rev.ID))}, nil
	})
	Def("cut.compare", "Compare two revisions as cut sheets: the changes in plain words and the lines that differ", Flags{}, func(ctx context.Context, a *app.App, in *CutCompareIn) (*CutCompareOut, error) {
		var p *models.Project
		if in.ProjectID != "" {
			pp, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			p = pp
		}
		l, lh, err := resolveRevision(ctx, a, p, in.Left, "")
		if err != nil {
			return nil, err
		}
		r, rh, err := resolveRevision(ctx, a, p, in.Right, "")
		if err != nil {
			return nil, err
		}
		if l == nil || r == nil {
			return nil, invalid("Say which two versions to compare.")
		}
		if l.ProjectID != r.ProjectID {
			return nil, invalid("The two versions belong to different projects.")
		}
		info := newSheetInfo(ctx, a)
		footage := projectAssetIDs(ctx, a, l.ProjectID)
		ls := cutsheet.Print(&l.Spec, cutsheet.Options{Info: info, Footage: footage})
		rs := cutsheet.Print(&r.Spec, cutsheet.Options{Info: info, Footage: footage})
		names := cutsheet.NameFunc(&r.Spec, info, footage)
		out := &CutCompareOut{Left: summarizeRev(a, *l, lh), Right: summarizeRev(a, *r, rh), LeftSheet: ls, RightSheet: rs,
			Changes: cutsheet.Describe(&l.Spec, &r.Spec, names), Lines: cutsheet.LineDiff(ls, rs)}
		out.Markdown = compareMarkdown(out)
		out.SheetURL = dashboardURL(a, links.CutSheetCompare(l.ProjectID, l.ID, r.ID))
		return out, nil
	})
}

func countErrors(ps []cutsheet.Problem) int {
	n := 0
	for _, p := range ps {
		if p.Severity == "error" {
			n++
		}
	}
	return n
}

func sheetTokenMessage(m string) string {
	switch {
	case strings.Contains(m, "expired"):
		return "The check of this sheet has expired (a check lasts 30 minutes)."
	case strings.Contains(m, "has changed"):
		return "The sheet has changed since it was checked, so it wasn't saved."
	}
	return "This sheet hasn't been checked yet."
}

func compareMarkdown(c *CutCompareOut) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Version %d → version %d\n\n", c.Left.Number, c.Right.Number)
	if c.Right.Instruction != "" {
		fmt.Fprintf(&b, "Asked for: “%s”\n\n", c.Right.Instruction)
	}
	b.WriteString("## What changed\n\n")
	for _, ch := range c.Changes {
		b.WriteString("- " + ch + "\n")
	}
	if cutsheet.Changed(c.Lines) {
		b.WriteString("\n## Cut sheet lines\n\n```diff\n")
		b.WriteString(cutsheet.Unified(c.Lines, 2))
		b.WriteString("```\n")
	}
	return b.String()
}
