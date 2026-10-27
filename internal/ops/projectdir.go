package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/secretpath"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/version"
)

// A project folder is a cut checked out to disk, like a git working copy:
//
//	cut.md               the cut sheet (edit this)
//	footage.md           a read-only reference: footage, lengths, speakers, transcript excerpts
//	.metarush/lock.json  which workspace, project, branch and base revision the files came from
//
// Media is never copied; the sheet names footage by asset ID and content hash,
// so anyone with access to the workspace renders the same video from a clone.
// Nothing secret (keys, tokens, connection strings), no logs and no caches are
// ever written into the folder.

const (
	cutFile     = "cut.md"
	footageFile = "footage.md"
)

// lockPath marks the folder as a MetaRush project folder: the log's export
// guard and anything else that must stay out of shared folders look for this
// same file (secretpath.ProjectFolder).
var lockPath = secretpath.ProjectFolderMarker

// FolderLock is .metarush/lock.json.
type FolderLock struct {
	Format      int                     `json:"format"`
	WorkspaceID string                  `json:"workspaceId"`
	ProjectID   string                  `json:"projectId"`
	Branch      string                  `json:"branch"`
	Base        FolderBase              `json:"base"`
	Files       map[string]string       `json:"files" jsonschema:"description=SHA-256 of each file as MetaRush wrote it."`
	Footage     map[string]FolderSource `json:"footage" jsonschema:"description=The sheet's footage names: asset ID and full content hash."`
	Motion      map[string]FolderMotion `json:"motion,omitempty" jsonschema:"description=The project motion graphics the cut uses: version (hash) and whether a person approved it."`
	Metarush    string                  `json:"metarush"`
}

// FolderBase is the revision the folder was written from (empty for a project
// with no cut yet).
type FolderBase struct {
	RevisionID string `json:"revisionId,omitempty"`
	Number     int    `json:"number,omitempty"`
	SpecHash   string `json:"specHash,omitempty"`
}

// FolderSource is one footage reference.
type FolderSource struct {
	AssetID string `json:"assetId"`
	SHA256  string `json:"sha256,omitempty"`
}

// ProjectCheckoutIn writes a project folder.
type ProjectCheckoutIn struct {
	ProjectID string `json:"projectId" jsonschema:"description=Project ID or exact name."`
	Dir       string `json:"dir"`
	Revision  string `json:"revision,omitempty" jsonschema:"description=Revision ID or rN; default the latest."`
	Branch    string `json:"branch,omitempty"`
	Force     bool   `json:"force,omitempty" jsonschema:"description=Overwrite cut.md/footage.md that are already in the folder."`
}

// ProjectDirIn names a project folder.
type ProjectDirIn struct {
	Dir   string `json:"dir,omitempty" jsonschema:"description=The folder (default: the current folder)."`
	Force bool   `json:"force,omitempty" jsonschema:"description=pull: discard local changes."`
}

// ProjectPushIn saves a project folder's cut.md as a new revision.
type ProjectPushIn struct {
	Dir          string `json:"dir,omitempty"`
	Message      string `json:"message,omitempty" jsonschema:"maxLength=4000,description=What changed (recorded as the revision's instruction)."`
	ConfirmToken string `json:"confirmToken,omitempty"`
	DryRun       bool   `json:"dryRun,omitempty" jsonschema:"description=Only check: show the changes and a confirm token."`
	AllowUnlock  bool   `json:"allowUnlock,omitempty"`
}

// FolderFile is a file's state in a project folder.
type FolderFile struct {
	Name  string `json:"name"`
	State string `json:"state" jsonschema:"enum=written,enum=unchanged,enum=modified,enum=missing"`
}

// FolderOut describes a project folder.
type FolderOut struct {
	Dir         string             `json:"dir"`
	ProjectID   string             `json:"projectId"`
	ProjectName string             `json:"projectName"`
	Branch      string             `json:"branch"`
	Base        FolderBase         `json:"base"`
	HeadID      string             `json:"headId,omitempty"`
	HeadNumber  int                `json:"headNumber,omitempty"`
	HeadMoved   bool               `json:"headMoved"`
	Files       []FolderFile       `json:"files"`
	Changes     []string           `json:"changes" jsonschema:"description=Local changes to cut.md compared with the base (in plain words)."`
	Problems    []cutsheet.Problem `json:"problems"`
	Valid       bool               `json:"valid"`
	Check       *CutCheckOut       `json:"check,omitempty"`
	Revision    *RevisionSummary   `json:"revision,omitempty" jsonschema:"description=push: the new revision."`
	Message     string             `json:"message" jsonschema:"description=What happened and what to do next (in plain words)."`
	Next        string             `json:"next,omitempty" jsonschema:"description=The command that does the suggested next step."`
	SheetURL    string             `json:"sheetUrl" jsonschema:"description=The folder's revision as a cut sheet in the dashboard."`
	Motion      []FolderMotionOut  `json:"motion,omitempty" jsonschema:"description=The motion graphics in motion/ and the ones the cut uses."`
}

func folderDir(dir string) (string, error) {
	if dir == "" {
		dir = "."
	}
	return filepath.Abs(dir)
}

func fileSum(p string) (string, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), true
}

func textSum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func readLock(dir string) (*FolderLock, error) {
	b, err := os.ReadFile(filepath.Join(dir, lockPath))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, apperr.New(apperr.NotFound, "%q isn't a MetaRush project folder.", filepath.Base(dir)).WithHint("Check the project out into a folder first.")
	}
	if err != nil {
		return nil, err
	}
	var l FolderLock
	if err := json.Unmarshal(b, &l); err != nil || l.ProjectID == "" {
		return nil, apperr.New(apperr.InvalidInput, "MetaRush's record of which version this folder came from is damaged.").WithHint("Check the project out again into a fresh folder.")
	}
	return &l, nil
}

// writeFile writes atomically (temp file + rename) so an interrupted write never
// leaves half a sheet.
func writeFile(p string, b []byte) error {
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// writeFolder writes the three files for revision r (nil: the project's starter sheet).
func writeFolder(ctx context.Context, a *app.App, dir string, p *models.Project, branch string, r *models.Revision) (*FolderLock, error) {
	info := newSheetInfo(ctx, a)
	var sheet string
	lock := &FolderLock{Format: 1, WorkspaceID: a.WorkspaceID(), ProjectID: p.ID, Branch: branch, Files: map[string]string{}, Footage: map[string]FolderSource{}, Metarush: version.Version}
	if r != nil {
		sheet = printRevision(ctx, a, info, r, true)
		lock.Base = FolderBase{RevisionID: r.ID, Number: r.Number, SpecHash: r.SpecHash}
		writeMotionSources(ctx, a, dir, &r.Spec)
		if _, lm, err := folderMotion(ctx, a, dir, &r.Spec, false); err == nil && len(lm) > 0 {
			lock.Motion = lm
		}
	} else {
		out, err := CallTyped[CutShowOut](ctx, a, "cut.show", CutShowIn{ProjectID: p.ID, Branch: branch}, Caller{})
		if err != nil {
			return nil, err
		}
		sheet = out.Sheet
	}
	parsed, _ := cutsheet.Parse(sheet, cutsheet.ParseOptions{})
	for _, f := range parsed.Footage {
		src := FolderSource{AssetID: f.AssetID}
		if ft, ok := info.Footage(f.AssetID); ok {
			src.SHA256 = ft.SHA256
		}
		lock.Footage[f.Name] = src
	}
	ref, err := footageReference(ctx, a, info, p, parsed.Footage)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(lockPath)), 0o755); err != nil {
		return nil, err
	}
	for name, body := range map[string]string{cutFile: sheet, footageFile: ref} {
		if err := writeFile(filepath.Join(dir, name), []byte(body)); err != nil {
			return nil, err
		}
		lock.Files[name] = textSum(body)
	}
	lb, _ := json.MarshalIndent(lock, "", "  ")
	if err := writeFile(filepath.Join(dir, lockPath), append(lb, '\n')); err != nil {
		return nil, err
	}
	return lock, nil
}

// footageReference is footage.md: what is in the project, for reading while
// editing cut.md. It is regenerated on every checkout and pull.
func footageReference(ctx context.Context, a *app.App, info *sheetInfo, p *models.Project, names []cutsheet.FootageRef) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Footage in %s\n\n", p.Name)
	b.WriteString("A reference for editing `cut.md` (MetaRush rewrites this file; changes here are not saved).\n")
	b.WriteString("Times are positions in each piece of footage. To use a moment, copy its time into a clip line,\n")
	b.WriteString("or quote the words that are said: `1. hook: interview-a from \"because I'm going\" to \"very quickly\"`.\n")
	for _, n := range names {
		as := info.asset(n.AssetID)
		if as == nil {
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n\n", n.Name)
		facts := []string{as.Name, fmtLength(as.Media.Duration)}
		if t := as.DisplayTitle(); t != models.DefaultTitle(as.Name) {
			facts = append([]string{"“" + t + "”"}, facts...)
		}
		if as.Media.Width > 0 {
			facts = append(facts, fmt.Sprintf("%d × %d", as.Media.Width, as.Media.Height))
		}
		if !as.Media.HasVideo && as.Media.HasAudio {
			facts = append(facts, "sound only")
		} else if !as.Media.HasAudio {
			facts = append(facts, "no sound")
		}
		b.WriteString(strings.Join(facts, " · ") + "\n")
		if s := as.Source; s != nil {
			// Outside footage: where it came from, for credits and reference.
			line := s.Line()
			if s.URL != "" {
				line += " · " + s.URL
			}
			if s.Title != "" && s.Title != as.DisplayTitle() {
				line += "\nOriginal title: “" + s.Title + "”"
			}
			if s.License != "" {
				line += "\nLicense stated: " + s.License
			}
			b.WriteString(line + "\n")
		}
		if as.Transcript == nil || as.Transcript.State != "available" {
			if as.Media.HasAudio {
				b.WriteString("\nNo transcript yet (analyze the footage to make one).\n")
			}
			continue
		}
		var segs []models.TranscriptSegment
		if err := a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), bson.M{"assetId": as.ID}, &segs, options.Find().SetSort(bson.D{{Key: "idx", Value: 1}}).SetProjection(bson.M{"words": 0})); err != nil {
			return "", err
		}
		// Who speaks: the voices someone named, and "Speaker N" for the
		// others when there are several to tell apart.
		labels := map[string]bool{}
		for _, s := range segs {
			if s.Speaker != "" {
				labels[s.Speaker] = true
			}
		}
		whoSaid := func(s models.TranscriptSegment) string {
			if s.SpeakerName != "" {
				return s.SpeakerName
			}
			if len(labels) > 1 {
				return models.SpeakerDisplayLabel(s.Speaker)
			}
			return ""
		}
		if voices, err := speakers.Voices(ctx, a, as.ID, false); err == nil && len(voices) > 0 {
			b.WriteString(speakers.Summary(as.DisplayTitle(), voices) + "\n")
			// What the owner wrote about the people in it (who they are, how to
			// name or title them, what to highlight or avoid).
			for _, p := range peopleIn(ctx, a, voices) {
				if p.Notes != "" {
					fmt.Fprintf(&b, "About %s: %s\n", p.Name, strings.Join(strings.Fields(p.Notes), " "))
				}
			}
		} else {
			var who []string
			seen := map[string]bool{}
			for _, s := range segs {
				if w := whoSaid(s); w != "" && !seen[w] {
					seen[w] = true
					who = append(who, w)
				}
			}
			if len(who) > 0 {
				fmt.Fprintf(&b, "Who speaks: %s.\n", strings.Join(who, ", "))
			}
		}
		if len(segs) == 0 {
			b.WriteString("\nNo speech.\n")
			continue
		}
		b.WriteString("\n")
		for i, s := range segs {
			if i == 60 {
				fmt.Fprintf(&b, "- … and %d more lines (the full transcript is in the dashboard)\n", len(segs)-60)
				break
			}
			text := strings.Join(strings.Fields(s.Text), " ")
			if r := []rune(text); len(r) > 110 {
				text = string(r[:109]) + "…"
			}
			who := whoSaid(s)
			if who != "" {
				who += ": "
			}
			fmt.Fprintf(&b, "- %s → %s %s“%s”\n", clock(s.From), clock(s.To), who, text)
		}
	}
	return b.String(), nil
}

func clock(v float64) string {
	return cutsheet.Clock(math.Round(v*100) / 100)
}

func fmtLength(v float64) string {
	v = math.Round(v*10) / 10
	if v < 60 {
		return fmt.Sprintf("%.1f s long", v)
	}
	return fmt.Sprintf("%d min %02.0f s long", int(v)/60, math.Mod(v, 60))
}

// folderState compares the files with the lock.
func folderState(dir string, l *FolderLock) []FolderFile {
	var out []FolderFile
	for _, name := range []string{cutFile, footageFile} {
		sum, ok := fileSum(filepath.Join(dir, name))
		st := "unchanged"
		switch {
		case !ok:
			st = "missing"
		case sum != l.Files[name]:
			st = "modified"
		}
		out = append(out, FolderFile{Name: name, State: st})
	}
	return out
}

func (o *FolderOut) fill(ctx context.Context, a *app.App, l *FolderLock) {
	var p models.Project
	if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), l.ProjectID, &p) == nil {
		o.ProjectName = p.Name
		o.HeadID = p.Branches[l.Branch].Head
		if o.HeadID != "" {
			var h models.Revision
			if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), o.HeadID, &h) == nil {
				o.HeadNumber = h.Number
			}
		}
	}
	o.ProjectID, o.Branch, o.Base = l.ProjectID, l.Branch, l.Base
	o.SheetURL = dashboardURL(a, links.CutSheet(l.ProjectID, l.Base.RevisionID))
	o.HeadMoved = o.HeadID != "" && o.HeadID != l.Base.RevisionID
	if o.Changes == nil {
		o.Changes = []string{}
	}
	if o.Problems == nil {
		o.Problems = []cutsheet.Problem{}
	}
}

func checkWorkspace(a *app.App, l *FolderLock) error {
	if l.WorkspaceID != a.WorkspaceID() {
		return apperr.New(apperr.Conflict, "This folder belongs to a different workspace than the one MetaRush on this machine is connected to.").WithHint("Use it on a machine that has joined that workspace.")
	}
	return nil
}

func init() {
	Def("project.checkout", "Write a project's cut into a folder (cut.md, footage.md, .metarush/lock.json) for editing and git", Flags{LocalOnly: true}, func(ctx context.Context, a *app.App, in *ProjectCheckoutIn) (*FolderOut, error) {
		dir, err := folderDir(in.Dir)
		if err != nil {
			return nil, err
		}
		p, err := resolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, err
		}
		branch := in.Branch
		if branch == "" {
			branch = "main"
		}
		r, _, err := resolveRevision(ctx, a, p, in.Revision, branch)
		if err != nil {
			return nil, err
		}
		if l, err := readLock(dir); err == nil {
			if l.ProjectID != p.ID {
				return nil, apperr.New(apperr.Conflict, "%q already holds a different project.", filepath.Base(dir)).WithHint("Choose an empty folder.")
			}
			if !in.Force {
				return nil, apperr.New(apperr.Conflict, "%q already holds this project.", filepath.Base(dir)).WithHint("Pull to update it, or check it out again with force to overwrite it.")
			}
		}
		if !in.Force {
			var there []string
			for _, name := range []string{cutFile, footageFile} {
				if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
					there = append(there, name)
				}
			}
			if len(there) > 0 {
				return nil, apperr.New(apperr.Conflict, "%q already has %s.", filepath.Base(dir), strings.Join(there, " and ")).WithDetails(map[string]any{"files": there}).WithHint("Choose another folder, or check out with force to overwrite them.")
			}
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		l, err := writeFolder(ctx, a, dir, p, branch, r)
		if err != nil {
			return nil, err
		}
		out := &FolderOut{Dir: dir, Valid: true, Files: []FolderFile{{Name: cutFile, State: "written"}, {Name: footageFile, State: "written"}}}
		out.fill(ctx, a, l)
		if r != nil {
			out.Message = fmt.Sprintf("Checked out version %d of %s. Edit cut.md, then push to save it as a new version.", r.Number, p.Name)
			out.Next = "metarush project push " + dir + " --dry-run"
		} else {
			out.Message = fmt.Sprintf("%s has no cut yet; cut.md lists its footage. Add clips under ## Clips, then push.", p.Name)
			out.Next = "metarush project push " + dir + " --dry-run"
		}
		return out, nil
	})
	Def("project.status", "Show a project folder's local changes and whether a newer revision exists", Flags{LocalOnly: true}, func(ctx context.Context, a *app.App, in *ProjectDirIn) (*FolderOut, error) {
		dir, err := folderDir(in.Dir)
		if err != nil {
			return nil, err
		}
		l, err := readLock(dir)
		if err != nil {
			return nil, err
		}
		if err := checkWorkspace(a, l); err != nil {
			return nil, err
		}
		out := &FolderOut{Dir: dir, Files: folderState(dir, l), Valid: true}
		out.fill(ctx, a, l)
		if b, err := os.ReadFile(filepath.Join(dir, cutFile)); err == nil {
			if parsed, _ := cutsheet.Parse(string(b), cutsheet.ParseOptions{Info: newSheetInfo(ctx, a)}); parsed != nil && parsed.Spec != nil {
				out.Motion, _, _ = folderMotion(ctx, a, dir, parsed.Spec, false)
			}
		}
		if out.Files[0].State == "modified" {
			b, _ := os.ReadFile(filepath.Join(dir, cutFile))
			chk, err := checkSheet(ctx, a, string(b), "", "", "cut-sheet:folder", false)
			if err != nil {
				out.Problems = append(out.Problems, cutsheet.Problem{Severity: "error", Code: apperr.From(err).Code, Message: apperr.From(err).Message, Hint: apperr.From(err).Hint})
				out.Valid = false
			} else {
				out.Changes, out.Problems, out.Valid = chk.Changes, chk.Problems, chk.Valid
				chk.ConfirmToken, chk.ExpiresAt = "", nil
				out.Check = chk
			}
		}
		switch {
		case out.Files[0].State == "missing":
			out.Message = "cut.md is missing; pulling with force writes it again."
			out.Next = "metarush project pull " + dir + " --force"
		case out.Files[0].State == "modified" && out.HeadMoved:
			out.Message = fmt.Sprintf("You have changes in cut.md, and someone saved version %d meanwhile. Pushing combines them if they don't overlap.", out.HeadNumber)
			out.Next = "metarush project push " + dir + " --dry-run"
		case out.Files[0].State == "modified":
			out.Message = "You have changes in cut.md that aren't saved yet."
			out.Next = "metarush project push " + dir + " --dry-run"
		case out.HeadMoved:
			out.Message = fmt.Sprintf("A newer version (version %d) exists. Pull to bring it in.", out.HeadNumber)
			out.Next = "metarush project pull " + dir
		default:
			out.Message = "Up to date, no local changes."
		}
		return out, nil
	})
	Def("project.pull", "Update a project folder to the latest revision (refuses to overwrite local changes unless forced)", Flags{LocalOnly: true}, func(ctx context.Context, a *app.App, in *ProjectDirIn) (*FolderOut, error) {
		dir, err := folderDir(in.Dir)
		if err != nil {
			return nil, err
		}
		l, err := readLock(dir)
		if err != nil {
			return nil, err
		}
		if err := checkWorkspace(a, l); err != nil {
			return nil, err
		}
		files := folderState(dir, l)
		var modified []string
		for _, f := range files {
			if f.State == "modified" {
				modified = append(modified, f.Name)
			}
		}
		if len(modified) > 0 && !in.Force {
			return nil, apperr.New(apperr.Conflict, "Pulling would overwrite your changes in %s.", strings.Join(modified, " and ")).WithDetails(map[string]any{"files": modified}).
				WithHint("Push them first to save them, or pull with force to throw them away.")
		}
		var p models.Project
		if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), l.ProjectID, &p); err != nil {
			return nil, apperr.New(apperr.NotFound, "The project this folder holds isn't in this workspace any more.").WithDetails(map[string]any{"projectId": l.ProjectID})
		}
		r, _, err := resolveRevision(ctx, a, &p, "", l.Branch)
		if err != nil {
			return nil, err
		}
		before := l.Base
		nl, err := writeFolder(ctx, a, dir, &p, l.Branch, r)
		if err != nil {
			return nil, err
		}
		out := &FolderOut{Dir: dir, Valid: true, Files: folderState(dir, nl)}
		for i := range out.Files {
			out.Files[i].State = "written"
		}
		out.fill(ctx, a, nl)
		switch {
		case r == nil:
			out.Message = "The project still has no cut."
		case before.RevisionID == r.ID && len(modified) == 0:
			out.Message = fmt.Sprintf("Already up to date (version %d).", r.Number)
		case len(modified) > 0:
			out.Message = fmt.Sprintf("Now at version %d; your local changes were discarded.", r.Number)
		default:
			out.Message = fmt.Sprintf("Updated from version %d to version %d.", before.Number, r.Number)
		}
		return out, nil
	})
	Def("project.push", "Save a project folder's cut.md as a new revision (same check, confirm token and lock rules as cut.apply)", Flags{LocalOnly: true, Mutating: true}, func(ctx context.Context, a *app.App, in *ProjectPushIn) (*FolderOut, error) {
		dir, err := folderDir(in.Dir)
		if err != nil {
			return nil, err
		}
		l, err := readLock(dir)
		if err != nil {
			return nil, err
		}
		if err := checkWorkspace(a, l); err != nil {
			return nil, err
		}
		b, err := os.ReadFile(filepath.Join(dir, cutFile))
		if err != nil {
			return nil, apperr.New(apperr.NotFound, "cut.md is missing from %q.", filepath.Base(dir)).WithHint("Pulling with force writes it again.")
		}
		out := &FolderOut{Dir: dir, Files: folderState(dir, l)}
		out.fill(ctx, a, l)
		if out.Files[0].State == "unchanged" {
			out.Valid, out.Message = true, "Nothing to push: cut.md has no changes."
			return out, nil
		}
		sheet := string(b)
		parsed, _ := cutsheet.Parse(sheet, cutsheet.ParseOptions{})
		if full, _ := cutsheet.Parse(sheet, cutsheet.ParseOptions{Info: newSheetInfo(ctx, a)}); full != nil && full.Spec != nil {
			// The motion graphics the cut uses travel with it: their archives are
			// in the media store (uploaded from motion/ when the store lacks them).
			if out.Motion, _, err = folderMotion(ctx, a, dir, full.Spec, !in.DryRun); err != nil {
				return nil, err
			}
		}
		if parsed.BaseRevisionID != l.Base.RevisionID {
			return nil, apperr.New(apperr.Conflict, "cut.md says it was written from a different version than this folder holds.").
				WithHint("Put back the base: line MetaRush wrote, or pull with force to start again from the latest.")
		}
		if in.DryRun || in.ConfirmToken == "" {
			chk, err := CallTyped[CutCheckOut](ctx, a, "cut.check", CutCheckIn{Sheet: sheet, Instruction: in.Message, Origin: "cut-sheet:folder", AllowUnlock: in.AllowUnlock}, Caller{})
			if err != nil {
				return nil, err
			}
			out.Check, out.Changes, out.Problems, out.Valid = chk, chk.Changes, chk.Problems, chk.Valid
			if !in.DryRun && chk.Valid && !chk.NoChanges {
				return nil, apperr.New(apperr.ConfirmRequired, "These changes haven't been checked yet.").WithDetails(map[string]any{"changes": chk.Changes}).
					WithHint("Check them first (with the same message) and look at what they change, then push.").
					WithCommand("metarush project push " + dir + " --dry-run")
			}
			switch {
			case !chk.Valid:
				out.Message = "cut.md has problems; fix them and try again."
			case chk.NoChanges:
				out.Message = "cut.md is the same cut as the version it came from; nothing to save."
			default:
				out.Message = "Ready to save as a new version."
				out.Next = "metarush project push " + dir + " --confirm " + chk.ConfirmToken
			}
			return out, nil
		}
		res, err := CallTyped[CutApplyOut](ctx, a, "cut.apply", CutApplyIn{Sheet: sheet, ConfirmToken: in.ConfirmToken, Instruction: in.Message, Origin: "cut-sheet:folder", AllowUnlock: in.AllowUnlock}, Caller{})
		if err != nil {
			return nil, err
		}
		var p models.Project
		if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), l.ProjectID, &p); err != nil {
			return nil, err
		}
		var rev models.Revision
		if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), res.Revision.ID, &rev); err != nil {
			return nil, err
		}
		nl, err := writeFolder(ctx, a, dir, &p, l.Branch, &rev)
		if err != nil {
			return nil, err
		}
		motionRows := out.Motion
		out = &FolderOut{Dir: dir, Valid: true, Files: folderState(dir, nl), Changes: res.Changes, Revision: &res.Revision, Motion: motionRows}
		out.fill(ctx, a, nl)
		out.Message = fmt.Sprintf("Saved as version %d.", res.Revision.Number)
		if res.Rebased {
			out.Message += " Someone else saved changes meanwhile; yours were combined with theirs."
		}
		return out, nil
	})
}

var _ = time.Now
